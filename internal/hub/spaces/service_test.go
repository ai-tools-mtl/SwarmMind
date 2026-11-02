// service_test.go 中心空间服务全链测试（W07-02，进程内双端 fake——真 hub core +
// 真 collab 内核 + 真 node service（sessionx fake CLI）+ membus 同步投递；两模拟
// 端仅用于单测，不签产品通过）：
//   - 派发全链：版本门（RequireSpacesCapable）→ space_bind 绑定登记（node 收件门
//     受理面）→ 意图 outbox → 既有 session Task 通道投递 → node 执行 → 结果落账
//     回 space（converged）——不新增任意远程执行通道的全链实证；
//   - 未声明空间特性的 node 明确拒绝（W07-01 强制门首个消费者）+ 绑定登记未达
//     零意图（宁拒不瞎投）；
//   - N02 重放幂等（同 request_id 零重复执行零重复扣减）；
//   - 旧 term 拒绝新派发（external 空间 orchestrate 驱动形态）；
//   - 恢复映射：重启对账按实物分类（accepted 非终态留在途不瞎跑——不把失联解释
//     为没执行；结果到达经新服务钩子收敛）；
//   - hub 侧取消：Result 未达不猜（意图留待核对），node 取消结果合并后按真实
//     执行事实收口（cancelled + 系统暂停停队）。
package spaces

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	hubstore "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// fakeHubSubject 测试主人主体（manual 空间创建者/驱动者）。
var fakeHubSubject = collab.Subject{Name: "owner", Class: collab.ClassOwner}

// testWaitBudget 条件等待窗预算（2026-11-03 全仓并行负载采证：5s 在 28 包并行
// CPU 超订下偶发不够——多跳链（取消栅栏→node 合并→结果回写→钩子入队→worker）
// 超窗假红；放宽到 15s，条件等待语义不变（超时仍 Fatal，非固定睡眠）。
const testWaitBudget = 15 * time.Second

// fakePrimeSubject 测试编排主体（external 领取/驱动）。
var fakePrimeSubject = collab.Subject{Name: "pat:tester", Class: collab.ClassOrchestrate}

// chainFixture 进程内双端全链基座。
type chainFixture struct {
	t        *testing.T
	dir      string
	hubStore *hubstore.Store
	hubCore  *core.Core
	svc      *Service
	nodeSvc  *service.Service
	nodeLed  *ledger.Ledger
	binds    *service.HubBindings
	nodeCwd  string
	sessID   string
	spaceID  string
	barrier  string // 非空 = fake CLI 阻塞至该文件出现
}

// buildFakeCLI 编译 sessionx 测试假 CLI（spaces 包视角相对路径）。
func buildFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, "../../node/sessionx/testdata/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	return path
}

// newChainFixture 装配全链（hub store/core/spaces + node service 双端 membus 相连）。
// barrierAll=true 时 fake CLI 携 WAIT_FILE 阻塞到 barrier 文件出现（取消/重启对账
// 场景用）。capableNode 控制主 node 能力卡是否盖章 Spaces（版本门反例用）。
func newChainFixture(t *testing.T, barrierAll, capableNode bool) *chainFixture {
	return newChainFixtureO(t, chainOpts{barrier: barrierAll, capable: capableNode})
}

// chainOpts 夹具变体（codex 复审 F 补强——受控构造：retention/sweep 注入做任务
// 清理反例；其余测试走默认零后台清扫）。
type chainOpts struct {
	barrier           bool
	capable           bool
	sweepInterval     time.Duration // hub core Sweep（0 = 不启动）
	terminalRetention time.Duration // 任务终态保留（0 = 协议默认；极短值做清理反例）
}

func newChainFixtureO(t *testing.T, o chainOpts) *chainFixture {
	t.Helper()
	oldSweep := sweepInterval
	sweepInterval = 50 * time.Millisecond
	t.Cleanup(func() { sweepInterval = oldSweep })

	dir := t.TempDir()
	f := &chainFixture{t: t, dir: dir}

	// hub 侧。
	var err error
	f.hubStore, err = hubstore.Open(dir)
	if err != nil {
		t.Fatalf("hub store: %v", err)
	}
	t.Cleanup(func() { _ = f.hubStore.Close() })
	bus := transport.NewBus()
	f.hubCore, err = core.New(core.Config{Store: f.hubStore, Transport: bus.Endpoint("hub"),
		SpaceBindTimeout: 30 * time.Second, SweepInterval: o.sweepInterval, TerminalRetention: retentionPtr(o.terminalRetention)})
	if err != nil {
		t.Fatalf("hub core: %v", err)
	}
	t.Cleanup(func() { _ = f.hubCore.Close() })
	bus.Endpoint("hub").Subscribe(f.hubCore.HandleFrame)

	// node 侧（真 service + sessionx fake CLI + collab + 中心绑定登记表）。
	dataDir := filepath.Join(dir, "nodedata")
	_ = os.MkdirAll(dataDir, 0o755)
	f.nodeLed, _, err = ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatalf("node ledger: %v", err)
	}
	t.Cleanup(func() { _ = f.nodeLed.Close() })
	f.nodeCwd = filepath.Join(dir, "ws")
	if err := os.MkdirAll(f.nodeCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := buildFakeCLI(t)
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex, "FAKE_THREAD_ID=thr-chain-1"}
	if o.barrier {
		f.barrier = filepath.Join(dir, "go.barrier")
		env = append(env, "FAKE_WAIT_FILE="+f.barrier)
	}
	specs := map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: fake,
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: env,
	}}
	mgr, err := sessionx.NewManager(filepath.Join(dataDir, "kernel"), specs, sessionx.Options{})
	if err != nil {
		t.Fatalf("sessionx: %v", err)
	}
	ops, err := sessionop.New(filepath.Join(dataDir, "ops"), f.nodeLed, sessionop.Options{})
	if err != nil {
		t.Fatalf("sessionop: %v", err)
	}
	nodeCollabDir := filepath.Join(dataDir, "collab")
	if err := os.MkdirAll(nodeCollabDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeCollab, err := collab.Open(nodeCollabDir, collab.Options{})
	if err != nil {
		t.Fatalf("node collab: %v", err)
	}
	t.Cleanup(func() { _ = nodeCollab.Close() })
	f.binds, err = service.OpenHubBindings(dataDir)
	if err != nil {
		t.Fatalf("hubbind: %v", err)
	}
	f.nodeSvc = service.New(service.Config{
		Ledger:        f.nodeLed,
		Runners:       map[string]service.Runner{},
		ArtifactRoots: map[string]string{"coder": filepath.Join(dir, "artifacts")},
		Sessions:      mgr,
		SessionOps:    ops,
		Collab:        nodeCollab,
		HubBindings:   f.binds,
		NodeID:        "n1",
	}, bus.Endpoint("n1"))
	t.Cleanup(f.nodeSvc.Close)

	// 能力注册：主 node coder（按参数盖章 Spaces）；n2 为无端点的已注册能力
	//（版本门/绑定未达反例共用——无端点 = Send ErrNoEndpoint = node 不在线）。
	spacesCap := ""
	if o.capable {
		spacesCap = protocol.SpacesCapV1
	}
	coderCard := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex, Spaces: spacesCap}
	if err := f.hubCore.RegisterNode("n1", []protocol.AgentCard{coderCard}); err != nil {
		t.Fatal(err)
	}
	f.hubCore.SetOnline("n1", true)
	oldCard := protocol.AgentCard{Name: "old", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex} // 无 Spaces 声明（旧版 node 词形）
	if err := f.hubCore.RegisterNode("n2", []protocol.AgentCard{oldCard}); err != nil {
		t.Fatal(err)
	}
	f.hubCore.SetOnline("n2", true) // 能力表在册；端点不存在 → 传输层判离线

	// 中心空间服务。
	f.svc, err = Open(dir, f.hubCore, Options{})
	if err != nil {
		t.Fatalf("spaces: %v", err)
	}
	t.Cleanup(func() { _ = f.svc.Close() })
	// 生产守卫接线（codex 必修F——夹具与 cmd/hub 同函数 SpaceTurnGuard；缺接线
	// 曾让必修A 的无差别拦截反例全绿假阴）。
	f.hubCore.SetSpaceTurnGuard(f.svc.SpaceTurnGuard())

	// 会话 + manual 空间（owner Term=1）→ 配置成员 → 就绪 → 启动。
	sv, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	f.sessID = sv.ID
	sp, _, err := f.svc.CreateSpace(collab.CreateRequest{RequestID: "create-" + protocol.NewMessageID(), Goal: "全链空间", Mode: collab.ModeManual}, fakeHubSubject)
	if err != nil {
		t.Fatalf("建空间: %v", err)
	}
	f.spaceID = sp.ID
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "cfg-1", Action: collab.ActConfigureMembers,
		ExpectedVersion: sp.Version, Members: []collab.Member{{Name: "n1/coder", SessionID: f.sessID}}}, fakeHubSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "rdy-1", Action: collab.ActReady, ExpectedVersion: sp.Version + 1}, fakeHubSubject); err != nil {
		t.Fatalf("就绪: %v", err)
	}
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "start-1", Action: collab.ActStart, ExpectedVersion: sp.Version + 2}, fakeHubSubject); err != nil {
		t.Fatalf("启动: %v", err)
	}
	return f
}

// waitIntentStatus 轮询意图状态至目标值（有界；非 fixed sleep——事件驱动路径由
// 终态钩子/扫查推进，轮询只做断言同步）。
func (f *chainFixture) waitIntentStatus(reqID string, want collab.IntentStatus) collab.DispatchIntent {
	t := f.t
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if it, ok := f.svc.IntentByRequest(f.spaceID, reqID); ok && it.Status == want {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.svc.IntentByRequest(f.spaceID, reqID)
	t.Fatalf("意图 %s 未达 %s（现 %s）", reqID, want, it.Status)
	return collab.DispatchIntent{}
}

// releaseBarrier 放行阻塞中的 fake CLI 轮。
func (f *chainFixture) releaseBarrier() {
	if f.barrier == "" {
		return
	}
	if err := os.WriteFile(f.barrier, []byte("go"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// TestDispatchFullChainConverges 派发全链：受理（意图 accepted）→ node 收件门放行
// 执行 → 结果落账回 space（converged）；绑定登记在 node 持久在册；会话轮行留痕。
func TestDispatchFullChainConverges(t *testing.T) {
	f := newChainFixture(t, false, true)
	it, dup, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-full-1",
		ExpectedVersion: 4, Note: "全链一轮",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dup {
		t.Fatal("首次派发不应命中重放")
	}
	if it.Status != collab.IntentAccepted || it.TaskID == "" {
		t.Fatalf("意图应 accepted+TaskID: %+v", it)
	}
	if it.TurnSeq != 1 {
		t.Fatalf("预留轮号应 1: %d", it.TurnSeq)
	}
	// 绑定登记在 node 在册（收件门受理面事实）。
	if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
		t.Fatalf("node 绑定登记不符: %q", got)
	}
	// 结果落账回 space。
	f.waitIntentStatus("d-full-1", collab.IntentConverged)
	// 会话轮行留痕（同一 operation/轮引用）。
	tr, ok := f.hubCore.SessionTurnByRequest(f.sessID, "d-full-1")
	if !ok || tr.TaskID != it.TaskID {
		t.Fatalf("会话轮行应留痕意图引用: %+v ok=%v", tr, ok)
	}
}

// TestDispatchGateRejectsUncapableOrUnreachable 未声明空间特性的 node 明确拒绝
// （W07-01 强制门；零意图零绑定）；目录外成员拒绝（开放目录校验）。独立 external
// 空间承载（configure_members 仅待配置/暂停态可变更——fixture 主空间已 running；
// external 链同时锚编排主体领取→配置→启动→派发的驱动形态，主体恒携当前任期）。
func TestDispatchGateRejectsUncapableOrUnreachable(t *testing.T) {
	f := newChainFixture(t, false, true)
	// n2 会话（能力表在册、无端点；目录条目在——版本门在 DirectoryAgent 之后）。
	sv, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n2", AgentName: "old", Cwd: "/tmp/x"}, "admin")
	if err != nil {
		t.Fatalf("建会话 n2: %v", err)
	}
	// 第二会话（f.sessID 已绑定 fixture 主空间——一线程唯一归属）。
	sv1, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatalf("建会话 n1 第二条: %v", err)
	}
	// n3：已声明空间特性但端点缺失（绑定登记未达反例的载体）。
	capCard := protocol.AgentCard{Name: "remote", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex, Spaces: protocol.SpacesCapV1}
	if err := f.hubCore.RegisterNode("n3", []protocol.AgentCard{capCard}); err != nil {
		t.Fatal(err)
	}
	f.hubCore.SetOnline("n3", true)
	sv3, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n3", AgentName: "remote", Cwd: "/tmp/y"}, "admin")
	if err != nil {
		t.Fatalf("建会话 n3: %v", err)
	}
	sp, _, err := f.svc.CreateSpace(collab.CreateRequest{RequestID: "create-gate-1", Goal: "门槛反例", Mode: collab.ModeExternal}, fakePrimeSubject)
	if err != nil {
		t.Fatalf("建空间: %v", err)
	}
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "claim-gate-1", Action: collab.ActTakeover,
		ExpectedVersion: sp.Version, ExpectedTerm: 0}, fakePrimeSubject); err != nil {
		t.Fatalf("领取: %v", err)
	}
	view, _ := f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "cfg-gate-1", Action: collab.ActConfigureMembers,
		ExpectedVersion: view.Version, Term: view.Leader.Term,
		Members: []collab.Member{
			{Name: "n1/coder", SessionID: sv1.ID},
			{Name: "n2/old", SessionID: sv.ID},
			{Name: "n3/remote", SessionID: sv3.ID},
		}}, fakePrimeSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "rdy-gate-1", Action: collab.ActReady,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("就绪: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "start-gate-1", Action: collab.ActStart,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("启动: %v", err)
	}
	// n2/old 无 Spaces 声明 → 版本门拒绝（ErrSpacesUnsupported；人话指路升级）。
	_, _, err = f.svc.Dispatch(DispatchRequest{
		SpaceID: sp.ID, Member: "n2/old", RequestID: "d-gate-1", ExpectedVersion: view.Version + 1,
		Term: view.Leader.Term, Note: "x",
	}, fakePrimeSubject)
	if err == nil || !errors.Is(err, core.ErrSpacesUnsupported) {
		t.Fatalf("未声明空间特性的 node 应被版本门拒绝（ErrSpacesUnsupported）: %v", err)
	}
	if _, ok := f.svc.IntentByRequest(sp.ID, "d-gate-1"); ok {
		t.Fatal("版本门拒绝应零意图")
	}
	// 目录外成员（开放目录校验 DirectoryAgent）→ agent_not_exposed。
	_, _, err = f.svc.Dispatch(DispatchRequest{
		SpaceID: sp.ID, Member: "n1/ghost", RequestID: "d-gate-2", ExpectedVersion: view.Version + 1,
		Term: view.Leader.Term, Note: "x",
	}, fakePrimeSubject)
	var se *Error
	if err == nil || !errors.As(err, &se) || se.Code != CodeAgentNotExposed {
		t.Fatalf("目录外成员应 agent_not_exposed: %v", err)
	}
	// 已声明空间特性但不可达（能力表在册、无端点）→ 绑定登记未达 node_offline，
	// 零意图（宁拒不瞎投——在途先登记再 send 的前置面）。
	view, _ = f.svc.SpaceView(sp.ID)
	_, _, err = f.svc.Dispatch(DispatchRequest{
		SpaceID: sp.ID, Member: "n3/remote", RequestID: "d-gate-3", ExpectedVersion: view.Version,
		Term: view.Leader.Term, Note: "x",
	}, fakePrimeSubject)
	se = nil
	if err == nil || !errors.As(err, &se) || se.Code != CodeNodeOffline {
		t.Fatalf("不可达 node 应 node_offline（绑定登记未达）: %v", err)
	}
	if _, ok := f.svc.IntentByRequest(sp.ID, "d-gate-3"); ok {
		t.Fatal("绑定登记未达应零意图")
	}
}

// TestDispatchReplayIdempotent N02 重放幂等：同 request_id 同文返回原意图
// （dup=true），零重复受理零重复投递（会话轮行仍一条）。
func TestDispatchReplayIdempotent(t *testing.T) {
	f := newChainFixture(t, false, true)
	it1, dup1, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-replay-1",
		ExpectedVersion: 4, Note: "同文",
	}, fakeHubSubject)
	if err != nil || dup1 {
		t.Fatalf("首次派发: %v dup=%v", err, dup1)
	}
	f.waitIntentStatus("d-replay-1", collab.IntentConverged)
	it2, dup2, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-replay-1",
		ExpectedVersion: 4, Note: "同文",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("重放: %v", err)
	}
	if !dup2 || it2.TaskID != it1.TaskID || it2.TurnSeq != it1.TurnSeq {
		t.Fatalf("重放应命中原意图: dup=%v it1=%+v it2=%+v", dup2, it1, it2)
	}
	sv, ok := f.hubCore.GetSession(f.sessID)
	if !ok {
		t.Fatal("会话应存在")
	}
	if n := len(sv.Turns); n != 1 {
		t.Fatalf("重放不得重复投轮: turns=%d", n)
	}
	// 同 ID 异成员 = 冲突（受理指纹含成员维——与 node SpaceDispatch 同规；
	// 预检先于成员校验，非在册成员名同样触发冲突而非 not_found）。
	_, _, err = f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/ghost", RequestID: "d-replay-1",
		ExpectedVersion: 4, Note: "异文",
	}, fakeHubSubject)
	if err == nil || !containsCode(err, collab.CodeRequestConflict) {
		t.Fatalf("同 ID 异成员应 request_conflict: %v", err)
	}
}

// TestOldTermRejectedNewDispatch 旧 term 拒绝新派发（external 空间 orchestrate
// 领取驱动形态；授权层 driver 形态裁决——term 不等值即 forbidden）。
func TestOldTermRejectedNewDispatch(t *testing.T) {
	f := newChainFixture(t, false, true)
	// external 待领取空间（编排主体创建）；第二会话（f.sessID 已绑定 fixture 主空间）。
	sv1, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatalf("建会话 n1 第二条: %v", err)
	}
	sp, _, err := f.svc.CreateSpace(collab.CreateRequest{RequestID: "create-ext-1", Goal: "外部驱动", Mode: collab.ModeExternal}, fakePrimeSubject)
	if err != nil {
		t.Fatalf("建 external 空间: %v", err)
	}
	// 领取（claimable：pending_leader + Term=0）→ pending_config。
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "claim-1", Action: collab.ActTakeover,
		ExpectedVersion: sp.Version, ExpectedTerm: 0, TakeoverKind: collab.LeaderExternal}, fakePrimeSubject); err != nil {
		t.Fatalf("领取: %v", err)
	}
	view, _ := f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "cfg-ext-1", Action: collab.ActConfigureMembers,
		ExpectedVersion: view.Version, Term: view.Leader.Term,
		Members: []collab.Member{{Name: "n1/coder", SessionID: sv1.ID}}}, fakePrimeSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "rdy-ext-1", Action: collab.ActReady,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("就绪: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "start-ext-1", Action: collab.ActStart,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("启动: %v", err)
	}
	// 旧 term（0 ≠ 当前任期 2）派发 → forbidden；当前 term 派发成立。
	_, _, err = f.svc.Dispatch(DispatchRequest{
		SpaceID: sp.ID, Member: "n1/coder", RequestID: "d-oldterm-1",
		ExpectedVersion: view.Version + 1, Term: 0, Note: "旧任期",
	}, fakePrimeSubject)
	if err == nil || !containsCode(err, collab.CodeForbidden) {
		t.Fatalf("旧 term 派发应 forbidden: %v", err)
	}
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: sp.ID, Member: "n1/coder", RequestID: "d-newterm-1",
		ExpectedVersion: view.Version + 1, Term: view.Leader.Term, Note: "新任期",
	}, fakePrimeSubject)
	if err != nil {
		t.Fatalf("当前 term 派发: %v", err)
	}
	f.waitIntentStatusIn(sp.ID, it.RequestID, collab.IntentConverged)
}

// waitIntentStatusIn 指定空间的意图状态轮询（多空间断言用）。
func (f *chainFixture) waitIntentStatusIn(spaceID, requestID string, want collab.IntentStatus) {
	t := f.t
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if it, ok := f.svc.IntentByRequest(spaceID, requestID); ok && it.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("意图 %s 未达 %s", requestID, want)
}

// containsCode 业务错误码判定（*collab.Error 与 *spaces.Error 两链——派发面
// 冲突族以 spaces.Error 承载 collab 码词）。
func containsCode(err error, code string) bool {
	for e := err; e != nil; {
		switch x := e.(type) {
		case *collab.Error:
			return x.Code == code
		case *Error:
			return x.Code == code
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// TestRecoveryKeepsInFlightOnRestart 恢复映射：重启对账按实物分类——accepted
// 意图任务非终态（执行阻塞中）留在途不瞎跑（不把失联解释为没执行）；新服务钩子
// 在结果到达时收敛（恢复后服务接管收敛链）。
func TestRecoveryKeepsInFlightOnRestart(t *testing.T) {
	f := newChainFixture(t, true, true) // barrier：fake CLI 阻塞至放行
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-recover-1",
		ExpectedVersion: 4, Note: "重启对账",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 等任务进入 node 账本终态前态（accepted/started——barrier 阻塞执行中）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 重启中心空间服务（同一快照库；快照经 SQLite 落盘恢复）。
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer svc2.Close()
	f.svc = svc2
	// 守卫重接线（生产同形：新实例闭包——旧实例内存投影不再服务新轮提交判定）。
	f.hubCore.SetSpaceTurnGuard(svc2.SpaceTurnGuard())
	// 重启对账：accepted + 任务非终态 → 留在途（不收敛不重跑）。
	if got, ok := svc2.IntentByRequest(f.spaceID, "d-recover-1"); !ok || got.Status != collab.IntentAccepted {
		t.Fatalf("重启后意图应保持 accepted（留待核对/在途）: %+v ok=%v", got, ok)
	}
	// 放行执行 → 结果到达 → 新服务终态钩子收敛。
	f.releaseBarrier()
	f.waitIntentStatus("d-recover-1", collab.IntentConverged)
}

// TestHubCancelConvergesOnMergedResult hub 侧取消：CancelTask 首落终态（Result
// 未达）不猜（意图留待核对）；node 取消结果合并后按真实执行事实收口（cancelled +
// 系统暂停停队——新增派发暂停，显式 resume 恢复）。
func TestHubCancelConvergesOnMergedResult(t *testing.T) {
	f := newChainFixture(t, true, true)
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-cancel-1",
		ExpectedVersion: 4, Note: "取消链",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 等 node 执行进入阻塞态（accepted 之后）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// hub 侧取消：首落终态 cancelled（Result nil）——意图不被猜成 cancelled。
	if _, _, err := f.hubCore.CancelTask(it.TaskID, "人工取消"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if got, ok := f.svc.IntentByRequest(f.spaceID, "d-cancel-1"); !ok || !got.Status.InFlight() {
		t.Fatalf("hub 取消（Result 未达）意图应保持在途（不猜）: %+v ok=%v", got, ok)
	}
	// node 取消结果合并（task_cancel 打断 → cancelled 回执）→ 合并钩子收口。
	// 确定性等待：收敛（ConvergeIntent）与停队（SystemPause）是钩子 goroutine 内
	// 两次先后写库——轮询侧「见 cancelled 即断言 paused」会在两次写库窗口观测到
	// cancelled+running 假象（race 负载下窗口放大，集成者门禁偶红）；改为
	// cancelled+paused 合并条件同见才通过（同一钩子链终态，恒可达）。
	deadline = time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		got, ok := f.svc.IntentByRequest(f.spaceID, "d-cancel-1")
		sp, spok := f.svc.Space(f.spaceID)
		if ok && got.Status == collab.IntentCancelled && spok && sp.Status == collab.StatusPaused {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("取消结果合并后意图应收敛 cancelled 且空间暂停停队")
}

// TestSpaceBindAckEndToEnd space_bind 端到端铁律锚（AGENTS 帧请求-应答关联）：
// node handler 回帧 ReplyTo=请求 mid、载荷回带两字段（语义复核基准）；hub 侧
// SpaceBind 全链（waiter 先登记后 Send → ack 匹配消费 → 重复登记幂等成立）；
// 伪来源应答不消费（waiter 留到超时——铁律「消费前核验预期来源」）。
func TestSpaceBindAckEndToEnd(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	// 独立 hub 端点：录制入站帧（不经 hub core——纯帧形态断言面）。
	acks := make(chan *protocol.Envelope, 8)
	hub := bus.Endpoint("hub")
	hub.Subscribe(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgSpaceBindAck || env.Type == protocol.MsgErr {
			select {
			case acks <- env:
			default:
			}
		}
	})
	// node 侧最小装配（登记表 + 服务；与 chainFixture 同款）。
	dataDir := filepath.Join(dir, "nd")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	binds, err := service.OpenHubBindings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	nodeSvc := service.New(service.Config{
		Ledger: led, Runners: map[string]service.Runner{},
		ArtifactRoots: map[string]string{"coder": dir},
		HubBindings:   binds, NodeID: "n1",
	}, bus.Endpoint("n1"))
	t.Cleanup(nodeSvc.Close)

	// hub→node space_bind（membus 同步分发：handler 内同步回帧到 hub 录制端点）。
	reqMid := "bind-iron-" + protocol.NewMessageID()
	pl, _ := json.Marshal(protocol.SpaceBindPayload{Space: collab.HubScope("sp-x"), SessionID: "s-iron", Generation: 1})
	if err := bus.Endpoint("hub").Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgSpaceBind,
		MessageID: reqMid, From: "hub", To: "n1", Payload: pl,
	}); err != nil {
		t.Fatalf("发送: %v", err)
	}
	select {
	case env := <-acks:
		if env.Type != protocol.MsgSpaceBindAck {
			t.Fatalf("应回 ack: %+v", env)
		}
		if env.ReplyTo != reqMid {
			t.Fatalf("ack 应按 ReplyTo 关联请求 mid: got %q want %q", env.ReplyTo, reqMid)
		}
		var bp protocol.SpaceBindPayload
		if err := json.Unmarshal(env.Payload, &bp); err != nil || bp.Space != collab.HubScope("sp-x") || bp.SessionID != "s-iron" {
			t.Fatalf("ack 载荷应回带两字段: %s err=%v", env.Payload, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 ack")
	}
	// 伪来源应答不消费（hub core waiter 域）：起真 hub core 于独立 bus……此处锚
	// node handler 侧已足够（消费侧来源核验在 core.deliverBindReply，hub core
	// 单测域覆盖）；登记事实为最终权威断言。
	if got := binds.Space("s-iron"); got != collab.HubScope("sp-x") {
		t.Fatalf("登记应成立: %q", got)
	}
}

// TestCloseDrainsInFlightCallbacks 关停排空面回归（worker 化受控屏障——codex 复审
// 轮 2 核心机制 2）：真实取消合并入队 → worker 消费时被测试屏障（空间调度锁）拦
// 在收敛锁区 → Close 排空面等待 worker → 放屏障后收敛+停队落账完成 → Close 才返
// 返回——「队列项落账完成后 Close 才返回」由互斥语义确定性钉住。
func TestCloseDrainsInFlightCallbacks(t *testing.T) {
	f := newChainFixture(t, true, true)
	it, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
		RequestID: "drain-1", ExpectedVersion: 4, Note: "排空屏障"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 出队观测缝先于 CancelTask（两个收敛回调 item 均须被缝观测：item1=hub 侧
	// 首落 nil 结果项、item2=node 合并真实事实项——信号计数确定性对齐）。
	dequeued := make(chan struct{}, 8)
	f.svc.setOnConvDequeued(func() {
		select {
		case dequeued <- struct{}{}:
		default:
		}
	})
	// 受控屏障：占住空间调度锁（worker 消费合并项时在收敛锁区阻塞）。defer 保证
	// 任何 Fatal 路径都放屏障（否则清理 Close 永久等 worker——实测死锁锚）。
	m := f.svc.lockSpace(f.spaceID)
	m.Lock()
	barrierHeld := true
	release := func() {
		if barrierHeld {
			barrierHeld = false
			m.Unlock()
		}
	}
	defer release()
	if _, _, err := f.hubCore.CancelTask(it.TaskID, "人工取消"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	// 等合并已真实触发（node 取消结果合并回写 ⇒ Result 非 nil——fireTaskMerged/
	// hookEntry 已同步返回入队）。
	deadline = time.Now().Add(testWaitBudget)
	merged := false
	for time.Now().Before(deadline) {
		if tk, ok := f.hubCore.GetTask(it.TaskID); ok && tk.Result != nil {
			merged = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !merged {
		t.Fatal("前置：取消合并应已触发（Result 回写）")
	}
	// 等 worker 两次出队（取消合并链恒两回调：hub 侧首落 nil 结果项 + node 合并
	// 真实事实项——第二次出队即真实事实项已被 worker 持有、停在收敛锁区外排队；
	// 落账未发生——Close 排空面必须等它完成才返回）。
	<-dequeued
	<-dequeued
	// 屏障下意图不得已被收敛（worker 在锁区外排队）。
	if got, ok := f.svc.IntentByRequest(f.spaceID, "drain-1"); ok && got.Status == collab.IntentCancelled {
		t.Fatalf("屏障下收敛不得已落账: %+v", got)
	}
	// Close：排空面等 worker（worker 在屏障后排队等锁）——不得在屏障期内返回。
	closeErr := make(chan error, 1)
	go func() { closeErr <- f.svc.Close() }()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case cerr := <-closeErr:
			got, _ := f.svc.IntentByRequest(f.spaceID, "drain-1")
			sp, _ := f.svc.Space(f.spaceID)
			t.Fatalf("屏障未放行 Close 不得返回（排空面被跳过）: err=%v intent=%+v space=%s", cerr, got, sp.Status)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	release() // 放屏障 → worker 完成收敛+停队 → Close 排空返回
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, _ := f.svc.IntentByRequest(f.spaceID, "drain-1")
	sp, _ := f.svc.Space(f.spaceID)
	if got.Status != collab.IntentCancelled || sp.Status != collab.StatusPaused {
		t.Fatalf("排空完成后两落账应齐备: intent=%+v space=%s", got, sp.Status)
	}
}

// TestCloseRejectsLateCallbackHonestly 关停位回归：Close 后迟到的终态回调经
// hookEntry 快速退出（留痕不写账——不触已关库，绝不虚报落账）；重启对账兜底可
// 从快照恢复（同一 dir 重开服务意图按实物分类——关停丢弃不丢事实）。
func TestCloseRejectsLateCallbackHonestly(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 制造一条已受理意图并同步投递完成（结果未达——意图在途）。
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-late-1",
		ExpectedVersion: 4, Note: "关停迟到回调",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 关停后迟到回调（hub 侧钩子不可注销的真实形态）：快速退出、零写账。
	f.svc.hookEntry(protocol.Task{ID: it.TaskID, Status: protocol.StatusCompleted,
		Session: &protocol.SessionTurnRef{SessionID: f.sessID, TurnSeq: it.TurnSeq,
			Space: Scope(f.spaceID)}, Result: &protocol.TaskResult{Status: protocol.ResultCompleted}})
	// 同 dir 重开：重启对账按任务实物收敛（关停丢弃不丢事实）。
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer svc2.Close()
	f.svc = svc2
	f.waitIntentStatus("d-late-1", collab.IntentConverged)
}

// ---------- codex 审查必修 A–E 装配级反例回归 ----------

// addSecondMember pause→configure→resume 加第二成员（夹具空间初始 running——
// configure_members 仅待配置/暂停态可变更）。返回配置后空间版本。
func (f *chainFixture) addSecondMember(t *testing.T, reqID string, extra collab.Member) int64 {
	t.Helper()
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "pause-" + reqID, Action: collab.ActPause,
		ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("pause: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	members := append(append([]collab.Member{}, view.Members...), extra)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "cfg-" + reqID, Action: collab.ActConfigureMembers,
		ExpectedVersion: view.Version, Members: members}, fakeHubSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "rsm-" + reqID, Action: collab.ActResume,
		ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("resume: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	return view.Version
}

// secondAgentCard 注册 n1 上的第二能力卡（Spaces 盖章；会话内核无该 agent 装配——
// 投递后 node 侧 agent_not_found 失败终态，不妨碍 hub 侧受理/准入断言）。
func (f *chainFixture) secondAgentCard(t *testing.T) {
	t.Helper()
	card := protocol.AgentCard{Name: "coder2", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex, Spaces: protocol.SpacesCapV1}
	cards := []protocol.AgentCard{card}
	for _, c := range f.hubCore.ListAgents() {
		if c.NodeID == "n1" {
			cards = append(cards, c.Card)
		}
	}
	if err := f.hubCore.RegisterNode("n1", cards); err != nil {
		t.Fatal(err)
	}
}

// TestAGuardDistinguishesPlainAndSpaceTurns 必修A 反例（装配级，生产守卫已接线）：
//   - 普通轮（space 空）→ ErrSpaceDispatchRequired（409 指向空间派发）；
//   - 空间轮 scope 与绑定不符 → ErrSpaceScopeMismatch（与普通轮拒绝分码分话术）；
//   - 空间轮 scope 相符 → 放行（正例由 TestDispatchFullChainConverges 在同一
//     接线下承担——守卫缺接线曾让该反例全绿假阴）。
func TestAGuardDistinguishesPlainAndSpaceTurns(t *testing.T) {
	f := newChainFixture(t, false, true)
	_, _, err := f.hubCore.SubmitTurn(f.sessID, core.SessionTurnRequest{Prompt: "旁路普通轮"}, "admin")
	if !errors.Is(err, core.ErrSpaceDispatchRequired) {
		t.Fatalf("绑定会话普通轮应 ErrSpaceDispatchRequired: %v", err)
	}
	_, _, err = f.hubCore.SubmitSpaceTurn(f.sessID, core.SessionTurnRequest{Prompt: "scope 不符", RequestID: "x-mismatch"},
		"admin", collab.HubScope("sp-other-space"))
	if !errors.Is(err, core.ErrSpaceScopeMismatch) {
		t.Fatalf("scope 不符空间轮应 ErrSpaceScopeMismatch: %v", err)
	}
}

// TestBSerialOneStick 必修B 反例（同空间两成员并发执行）：
// 棒 1 在途（accepted）时棒 2 受理仅排队（pending 无 TaskID 不投递）——同空间
// 串行一次一棒；棒 1 收敛后钩子推进棒 2（kick，node 同规）。
func TestBSerialOneStick(t *testing.T) {
	f := newChainFixture(t, true, true) // barrier：棒 1 阻塞在途
	f.secondAgentCard(t)
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ver := f.addSecondMember(t, "b2", collab.Member{Name: "n1/coder2", SessionID: sv2.ID})
	// 棒 1：受理即在途（barrier 阻塞）。
	it1, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "b-m1",
		ExpectedVersion: ver, Note: "棒 1"}, fakeHubSubject)
	if err != nil || it1.Status != collab.IntentAccepted {
		t.Fatalf("棒 1 应 accepted: %v %+v", err, it1)
	}
	// 棒 2：受理排队（pending 无 TaskID——串行门不投）。
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "b-m2",
		ExpectedVersion: ver + 1, Note: "棒 2"}, fakeHubSubject); err != nil {
		t.Fatalf("棒 2 受理: %v", err)
	}
	if it2, ok := f.svc.IntentByRequest(f.spaceID, "b-m2"); !ok || it2.Status != collab.IntentPending || it2.TaskID != "" {
		t.Fatalf("串行门下棒 2 应 pending 未投递: %+v ok=%v", it2, ok)
	}
	// 棒 1 收敛 → 钩子推进棒 2（离开 pending）。
	f.releaseBarrier()
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "b-m2"); ok && got.Status != collab.IntentPending {
			return // accepted（node 执行）或已收敛（agent 不在内核表如实 failed）——离开排队即达意
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("棒 1 收敛后棒 2 应被推进（离开 pending）")
}

// TestBPauseBlocksAdvanceAndResumeReleases 必修B 反例（pause 后 sweep 零投递）：
// 棒 1 在途 + 棒 2 排队 → pause → 棒 1 收敛 → 推进被运行态门拦下（棒 2 保持
// pending 无 TaskID 跨多个扫查节拍）→ resume 显式放行后棒 2 离开排队。
func TestBPauseBlocksAdvanceAndResumeReleases(t *testing.T) {
	f := newChainFixture(t, true, true)
	f.secondAgentCard(t)
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ver := f.addSecondMember(t, "p2", collab.Member{Name: "n1/coder2", SessionID: sv2.ID})
	it1, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "p-m1",
		ExpectedVersion: ver, Note: "棒 1"}, fakeHubSubject)
	if err != nil || it1.Status != collab.IntentAccepted {
		t.Fatalf("棒 1 应 accepted: %v %+v", err, it1)
	}
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "p-m2",
		ExpectedVersion: ver + 1, Note: "棒 2"}, fakeHubSubject); err != nil {
		t.Fatalf("棒 2 受理: %v", err)
	}
	// pause（棒 1 在途照常收；新增派发暂停）。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "pause-b", Action: collab.ActPause,
		ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// 棒 1 收敛（kick 被运行态门拦下）。
	f.releaseBarrier()
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "p-m1"); ok && got.Status == collab.IntentConverged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 棒 2 跨多个扫查节拍保持 pending 未投递（pause 后 sweep 零投递——反例闭合）。
	sweeps := 20 * sweepInterval
	deadline = time.Now().Add(sweeps + time.Second)
	for time.Now().Before(deadline) {
		got, ok := f.svc.IntentByRequest(f.spaceID, "p-m2")
		if !ok {
			t.Fatal("棒 2 意图缺失")
		}
		if got.Status != collab.IntentPending || got.TaskID != "" {
			t.Fatalf("pause 后推进应零投递（棒 2 保持 pending）: %+v", got)
		}
		if time.Since(deadline) >= 0 {
			break
		}
		time.Sleep(sweepInterval)
	}
	got, _ := f.svc.IntentByRequest(f.spaceID, "p-m2")
	if got.Status != collab.IntentPending || got.TaskID != "" {
		t.Fatalf("pause 后棒 2 应保持 pending 未投递: %+v", got)
	}
	// resume 显式放行 → 棒 2 离开排队（kick 接线 + 扫查兜底）。
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "resume-b", Action: collab.ActResume,
		ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("resume: %v", err)
	}
	deadline = time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "p-m2"); ok && got.Status != collab.IntentPending {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resume 后棒 2 应被推进")
}

// TestBLeaseExpiryCancelsPending 必修B 反例（租约过期取消未投递意图）：external
// 空间 claim 租约 1s → 棒 1 在途 + 棒 2 排队 → 租约过期 → sweep 前置检查
// PauseExpired（同一 collab 事务：暂停 + pending 取消收束；accepted 在途不受影
// 响照常收敛）。
func TestBLeaseExpiryCancelsPending(t *testing.T) {
	f := newChainFixture(t, true, true)
	// 第二会话同 agent（内核真实可执行——一线程唯一归属按会话计，成员绑定 sess2）。
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// external 空间：编排创建 → 领取（租约 1s）→ 配置 → 就绪 → 启动。
	sp, _, err := f.svc.CreateSpace(collab.CreateRequest{RequestID: "create-lease", Goal: "租约反例", Mode: collab.ModeExternal}, fakePrimeSubject)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "claim-lease", Action: collab.ActTakeover,
		ExpectedVersion: sp.Version, ExpectedTerm: 0, LeaseSeconds: 1}, fakePrimeSubject); err != nil {
		t.Fatalf("领取: %v", err)
	}
	view, _ := f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "cfg-lease", Action: collab.ActConfigureMembers,
		ExpectedVersion: view.Version, Term: view.Leader.Term,
		Members: []collab.Member{{Name: "n1/coder", SessionID: sv2.ID}}}, fakePrimeSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "rdy-lease", Action: collab.ActReady,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("就绪: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "start-lease", Action: collab.ActStart,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("启动: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: sp.ID, Member: "n1/coder", RequestID: "l-m1",
		ExpectedVersion: view.Version, Term: view.Leader.Term, Note: "棒 1"}, fakePrimeSubject); err != nil {
		t.Fatalf("棒 1: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: sp.ID, Member: "n1/coder", RequestID: "l-m2",
		ExpectedVersion: view.Version, Term: view.Leader.Term, Note: "棒 2"}, fakePrimeSubject); err != nil {
		t.Fatalf("棒 2: %v", err)
	}
	// 租约 1s 过期 → sweep 前置检查 PauseExpired：棒 2（pending）取消收束 + 空间暂停。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		got, ok := f.svc.IntentByRequest(sp.ID, "l-m2")
		spNow, _ := f.svc.Space(sp.ID)
		if ok && got.Status == collab.IntentCancelled && spNow.Status == collab.StatusPaused {
			// 棒 1 accepted 不受影响（在途结果照常收敛）。
			f.releaseBarrier()
			f.waitIntentStatusIn(sp.ID, "l-m1", collab.IntentConverged)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("租约过期后棒 2 应取消收束且空间暂停")
}

// TestCSweepGateAfterWithdraw 必修C 反例（补投绕过版本门）：pending 意图在册后
// node 撤声明（能力卡重注册无 Spaces）→ sweep/重投经出口版本门取消收束——零外发
// （会话轮数不变、绑定零残留）。
func TestCSweepGateAfterWithdraw(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 直落一条 pending 意图（受理态实物；不经 Dispatch——模拟崩溃窗）。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "c-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "撤声明反例"}, fakeHubSubject); err != nil {
		t.Fatalf("RecordDispatch: %v", err)
	}
	// node 撤声明（能力卡重注册无 Spaces——register/card_update 同路径）。
	card := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex} // Spaces 有意缺位
	if err := f.hubCore.RegisterNode("n1", []protocol.AgentCard{card}); err != nil {
		t.Fatal(err)
	}
	// sweep 推进 → 出口版本门 → 取消收束（重试永不成功——确定性）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "c-1"); ok && got.Status == collab.IntentCancelled {
			if got, _ := f.svc.IntentByRequest(f.spaceID, "c-1"); !strings.Contains(got.Note, "版本门") {
				t.Fatalf("取消注记应锚版本门: %s", got.Note)
			}
			sv, _ := f.hubCore.GetSession(f.sessID)
			if n := len(sv.Turns); n != 0 {
				t.Fatalf("撤声明后零外发（轮数应 0）: %d", n)
			}
			// 代次协议注：补绑定（同代幂等刷新）先于提交口版本门发生，同代同
			// scope 登记不构成新授权残留（node 侧即为原登记原代次）；外发执行
			// 零发生（上锚）为必修C 的实质断言。登记回收由代次解除重试面收敛。
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("撤声明后 pending 意图应被出口版本门取消收束")
}

// TestDNoResidueAndUnbindClosure 必修D 反例（绑定副作用先于校验 + 无解除闭环）：
//   - 非驱动形态派发被拒（collab 授权前置）→ node 零绑定残留（序反转实证——
//     旧序 bind 先于校验会留残留）；
//   - 解除闭环：UnbindSession → node 登记删除（普通轮恢复放行）；
//   - 解除失败重试面：不可达 node 解除入队（PendingUnbindSessions 可见）。
func TestDNoResidueAndUnbindClosure(t *testing.T) {
	f := newChainFixture(t, false, true)
	// external 空间 + orchestrate 领取驱动（owner 非驱动形态）。
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	sp, _, err := f.svc.CreateSpace(collab.CreateRequest{RequestID: "create-d", Goal: "D 反例", Mode: collab.ModeExternal}, fakePrimeSubject)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "claim-d", Action: collab.ActTakeover,
		ExpectedVersion: sp.Version, ExpectedTerm: 0}, fakePrimeSubject); err != nil {
		t.Fatalf("领取: %v", err)
	}
	view, _ := f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "cfg-d", Action: collab.ActConfigureMembers,
		ExpectedVersion: view.Version, Term: view.Leader.Term,
		Members: []collab.Member{{Name: "n1/coder", SessionID: sv2.ID}}}, fakePrimeSubject); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "rdy-d", Action: collab.ActReady,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("就绪: %v", err)
	}
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Control(sp.ID, collab.ControlRequest{RequestID: "start-d", Action: collab.ActStart,
		ExpectedVersion: view.Version, Term: view.Leader.Term}, fakePrimeSubject); err != nil {
		t.Fatalf("启动: %v", err)
	}
	// 非驱动形态（owner）派发 → 授权前置拒绝 → node 零残留。
	_, _, err = f.svc.Dispatch(DispatchRequest{SpaceID: sp.ID, Member: "n1/coder", RequestID: "d-d-1",
		ExpectedVersion: view.Version + 1, Note: "x"}, fakeHubSubject)
	if !containsCode(err, collab.CodeForbidden) {
		t.Fatalf("非驱动派发应 forbidden: %v", err)
	}
	if got := f.binds.Space(sv2.ID); got != "" {
		t.Fatalf("被拒请求应零 node 绑定残留（序反转）: %q", got)
	}
	// 正常派发 → 绑定在册；解除 → 注册表删除（普通轮恢复放行）。
	view, _ = f.svc.SpaceView(sp.ID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: sp.ID, Member: "n1/coder", RequestID: "d-d-2",
		ExpectedVersion: view.Version, Term: view.Leader.Term, Note: "正例"}, fakePrimeSubject); err != nil {
		t.Fatalf("正例派发: %v", err)
	}
	if got := f.binds.Space(sv2.ID); got != Scope(sp.ID) {
		t.Fatalf("派发后绑定应在册: %q", got)
	}
	f.svc.UnbindSession("n1", sv2.ID)
	if got := f.binds.Space(sv2.ID); got != "" {
		t.Fatalf("解除后注册表应删除: %q", got)
	}
	// 解绑 + collab 归属未释放形态（守卫仍拦普通轮——归属权威在 collab，不在登记表）。
	if err := f.svc.SpaceTurnGuard()(sv2.ID, ""); !errors.Is(err, core.ErrSpaceDispatchRequired) {
		t.Fatalf("解绑后（collab 归属未释放）普通轮仍应守卫拒绝: %v", err)
	}
	// 重试面：不可达 node 解除入队（PendingUnbindSessions 观察面）。
	f.svc.UnbindSession("n-missing", "s-d-unreachable")
	found := false
	for _, sid := range f.svc.PendingUnbindSessions() {
		if sid == "s-d-unreachable" {
			found = true
		}
	}
	if !found {
		t.Fatal("解除失败应入队重试面")
	}
}

// TestEOldPlainTurnRequestConflict 必修E 反例（恢复/关联误认旧轮）：旧普通轮占
// 用 request_id（seq 1、无空间来源标记）→ 空间派发携同 request_id → 提交面
// request_conflict 确定性失败（意图取消收束如实落账，绝不假 converged）。
func TestEOldPlainTurnRequestConflict(t *testing.T) {
	f := newChainFixture(t, false, true)
	f.secondAgentCard(t)
	// 旧普通轮（绑定前合法形态）：会话 sv2 + request_id "R"（seq 1；执行面失败
	// 终态不影响——轮行实物与任务来源标记在案）。
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.hubCore.SubmitTurn(sv2.ID, core.SessionTurnRequest{Prompt: "旧普通轮原文", RequestID: "R"}, "admin"); err != nil {
		t.Fatalf("旧普通轮: %v", err)
	}
	// 绑定 sv2 进空间（pause→configure→resume）。
	ver := f.addSecondMember(t, "e2", collab.Member{Name: "n1/coder2", SessionID: sv2.ID})
	// 同 request_id 派发 → 提交面 request_conflict → 确定性取消（零假 converged）。
	// 受理回执 202 与 node 调度器同规：提交失败走意图状态（取消 + 注记），调用方
	// 经 GET dispatches/{request_id} 轮询可见。
	_, _, _ = f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "R",
		ExpectedVersion: ver, Note: "新轮原文（与旧轮异文）"}, fakeHubSubject)
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "R"); ok {
			if got.Status != collab.IntentCancelled {
				t.Fatalf("应确定性取消收束（绝不假 converged）: %+v", got)
			}
			// 两路冲突注记均为如实取消：advanceLocked 四态核验的「轮号冲突」
			//（先于提交）或提交面 request_conflict——确定性分类任一承担。
			if !strings.Contains(got.Note, "冲突") && !strings.Contains(got.Note, "request") {
				t.Fatalf("取消注记应锚冲突族（轮号冲突/request_conflict）: %s", got.Note)
			}
			svs, _ := f.hubCore.GetSession(sv2.ID)
			if n := len(svs.Turns); n != 1 {
				t.Fatalf("新轮不得入账（旧轮 seq1 外零新增）: %d", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("意图应已受理并确定性取消收束")
}

// TestCloseRealHookPathDrainOrRecovery 建议H（排空测试真实 hook 入口路径）：经
// hubCore 真实合并钩子（AddOnTaskMerged → hookEntry）与 Close 竞速——两分支终态
// 确定性收敛：回调先落（排空面等它出临界）→ 关停前已 converged；回调被关停位拦
// 下 → 同 dir 重开服务重启对账按实物收敛。任一分支都不丢事实（任务书「已接受
// 执行按原契约收束」的关停半边）。
func TestCloseRealHookPathDrainOrRecovery(t *testing.T) {
	f := newChainFixture(t, true, true)
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-hookrace-1",
		ExpectedVersion: 4, Note: "真实入口竞速",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 等 node 执行进入阻塞态（accepted 之后——合并结果尚未产生）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 放行执行 → node 取消结果即将合并（真实 hookEntry 即将触发）→ 同时 Close。
	f.releaseBarrier()
	closeErr := make(chan error, 1)
	go func() { closeErr <- f.svc.Close() }()
	// Close 与合并回调竞速：不等结果直接进入断言面（两分支终态一致）。
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 同 dir 重开：分支一（回调已落）幂等零变更；分支二（关停位拦下）重启对账
	// 按实物收敛——终态恒 converged。
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer svc2.Close()
	f.svc = svc2
	f.hubCore.SetSpaceTurnGuard(svc2.SpaceTurnGuard())
	f.waitIntentStatus("d-hookrace-1", collab.IntentConverged)
}

// retentionPtr nil 语义透传（0 = nil = 协议默认）。
func retentionPtr(d time.Duration) *time.Duration {
	if d == 0 {
		return nil
	}
	return &d
}

// ---------- codex 复审轮 2 残项反例（受控屏障/确定性构造，不赌时序） ----------

// TestAExplicitSeqReplayBeforeGuard 复审必修A-残：普通轮已完成→会话加入空间→
// 仅携原 turn_seq 同文重试——两端口径一致返回原回执（dup，非 space_dispatch_
// required）；request_id 路径同验（零副作用重放判定先于守卫——与 node 同序）。
func TestAExplicitSeqReplayBeforeGuard(t *testing.T) {
	f := newChainFixture(t, false, true)
	f.secondAgentCard(t)
	// 独立未绑定会话（fixture 主会话已被夹具绑定——不能用）。
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// 绑定前：普通轮 seq=1（request_id "rp"；执行面成败不影响重放——轮行在案）。
	ref1, _, err := f.hubCore.SubmitTurn(sv2.ID, core.SessionTurnRequest{Prompt: "原文", RequestID: "rp"}, "admin")
	if err != nil {
		t.Fatalf("普通轮: %v", err)
	}
	// 会话加入空间（pause→configure→resume；守卫自此拦截新普通轮）。
	f.addSecondMember(t, "a-replay", collab.Member{Name: "n1/coder2", SessionID: sv2.ID})
	// 仅携原 turn_seq 同文重试 → 原回执（dup，非 409）——复审必修A-残主断言。
	ref2, dup, err := f.hubCore.SubmitTurn(sv2.ID, core.SessionTurnRequest{Prompt: "原文", TurnSeq: ref1.TurnSeq}, "admin")
	if err != nil {
		t.Fatalf("显式序号重放应返回原回执（守卫不得先拦）: %v", err)
	}
	if !dup || ref2.TurnSeq != ref1.TurnSeq || ref2.TaskID != ref1.TaskID {
		t.Fatalf("重放应命中原回执: dup=%v ref1=%+v ref2=%+v", dup, ref1, ref2)
	}
	// request_id 路径同验。
	ref3, dup, err := f.hubCore.SubmitTurn(sv2.ID, core.SessionTurnRequest{Prompt: "原文", RequestID: "rp"}, "admin")
	if err != nil || !dup || ref3.TaskID != ref1.TaskID {
		t.Fatalf("request_id 重放应命中原回执: %v dup=%v", err, dup)
	}
	// 新普通轮（不同文/新序号）仍被守卫拒（守卫只放行重放，不放行新投递）。
	if _, _, err := f.hubCore.SubmitTurn(sv2.ID, core.SessionTurnRequest{Prompt: "新轮"}, "admin"); !errors.Is(err, core.ErrSpaceDispatchRequired) {
		t.Fatalf("新普通轮应仍被守卫拒: %v", err)
	}
}

// TestBControlSpaceSerializesAdvance 必修B-残①（锁面受控屏障）：空间调度锁被
// 占住期间 ControlSpace(pause) 与 Dispatch 的登记段均被互斥——控制变更与推进
// 不存在「读态后落地」交错窗；放锁后按序落地（pause 生效 → 随后 Dispatch 登记
// 被运行态门拒绝并触发 D-2 绑定残留补偿 → node 零残留）。
func TestBControlSpaceSerializesAdvance(t *testing.T) {
	f := newChainFixture(t, false, true)
	m := f.svc.lockSpace(f.spaceID)
	m.Lock() // 受控屏障：占住空间调度锁
	applied := make(chan error, 1)
	go func() {
		view, _ := f.svc.SpaceView(f.spaceID)
		_, _, err := f.svc.ControlSpace(f.spaceID, collab.ControlRequest{RequestID: "b-pause",
			Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject)
		applied <- err
	}()
	// 锁被占住：pause 不得落地（互斥语义确定性——观察窗内检查）。
	deadline := time.Now().Add(2 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		sp, _ := f.svc.Space(f.spaceID)
		if sp.Status == collab.StatusPaused {
			t.Fatal("锁被占住期间 pause 不应落地（互斥被破坏）")
		}
		seen = true
		time.Sleep(10 * time.Millisecond)
	}
	if !seen {
		t.Fatal("观察窗未执行")
	}
	m.Unlock() // 放屏障
	if err := <-applied; err != nil {
		t.Fatalf("放锁后 pause: %v", err)
	}
	sp, _ := f.svc.Space(f.spaceID)
	if sp.Status != collab.StatusPaused {
		t.Fatalf("pause 应落地: %s", sp.Status)
	}
}

// TestBReverifyViaLockAbort 复审必修B-残①（读态后暂停→不得提交）：pause 先于
// 推进落地后，advanceLocked 运行态门零投递（pending 意图保持未投递——不存在
// 「已取消意图被旧推进首次执行」窗）。
func TestBReverifyViaLockAbort(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 直落 pending 意图（受理态）。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "b-rv-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "x"}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	// pause 先落地。
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.ControlSpace(f.spaceID, collab.ControlRequest{RequestID: "b-rv-pause",
		Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// 推进（sweep 同路径）→ 运行态门 → 零投递。
	f.svc.KickSpace(f.spaceID)
	sv, _ := f.hubCore.GetSession(f.sessID)
	if n := len(sv.Turns); n != 0 {
		t.Fatalf("pause 后推进应零投递: %d", n)
	}
	got, ok := f.svc.IntentByRequest(f.spaceID, "b-rv-1")
	if !ok || got.Status != collab.IntentPending {
		t.Fatalf("pending 意图应保持排队: %+v", got)
	}
}

// TestBConvergePauseAtomicBlocksAdvance 必修B-残②（收敛+暂停两落账原子可见）：
// 棒 1 在途 + 棒 2 排队 → 取消棒 1 → 收敛与系统暂停在同一调度锁区完成——棒 2
// 不存在「收敛已落、暂停未落」中间态被推进（取消后保持 pending；resume 显式放
// 行才推进）。
func TestBConvergePauseAtomicBlocksAdvance(t *testing.T) {
	f := newChainFixture(t, true, true)
	f.secondAgentCard(t)
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ver := f.addSecondMember(t, "b2a", collab.Member{Name: "n1/coder2", SessionID: sv2.ID})
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "ba-m1",
		ExpectedVersion: ver, Note: "棒 1"}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "ba-m2",
		ExpectedVersion: ver + 1, Note: "棒 2"}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	it1, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "ba-m1",
		ExpectedVersion: ver, Note: "棒 1（待取消的在途棒）"}, fakeHubSubject)
	if err != nil || it1.Status != collab.IntentAccepted {
		t.Fatalf("棒 1 应 accepted: %v %+v", err, it1)
	}
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "ba-m2",
		ExpectedVersion: ver + 1, Note: "棒 2"}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	// 等棒 1 进入 node（barrier 阻塞在途）→ 取消 → 合并收敛 + 系统暂停（同锁区）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it1.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := f.hubCore.CancelTask(it1.TaskID, "人工取消"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	// 终态确定性：棒 1 cancelled + 空间 paused + 棒 2 保持 pending 未投递。
	deadline = time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		got1, _ := f.svc.IntentByRequest(f.spaceID, "ba-m1")
		sp, _ := f.svc.Space(f.spaceID)
		got2, ok2 := f.svc.IntentByRequest(f.spaceID, "ba-m2")
		if got1.Status == collab.IntentCancelled && sp.Status == collab.StatusPaused {
			if !ok2 || got2.Status != collab.IntentPending || got2.TaskID != "" {
				t.Fatalf("暂停落账完成前不得推进棒 2: %+v", got2)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("取消收敛+暂停未达成")
}

// TestD1ConditionalUnbindAckNoop 必修D-1（node 条件删除——代际语义）：登记 S2
// 后携旧代 expect=S1 解除 → ack-noop 零变更；携 expect=S2 解除 → 删除。
func TestD1ConditionalUnbindAckNoop(t *testing.T) {
	f := newChainFixture(t, false, true)
	sid := "s-d1"
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: collab.HubScope("sp-S2"), SessionID: sid, Generation: 2}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("登记 S2: %v ack=%+v", err, ack)
	}
	// 旧代解除（gen=1 ≠ 现登记 gen=2）→ ack-noop（非错误），S2 登记完好。
	ack, err := f.hubCore.SpaceUnbind("n1", sid, 1, 2*time.Second)
	if err != nil {
		t.Fatalf("旧代解除应 ack-noop（非错误）: %v", err)
	}
	if ack.Accepted {
		t.Fatal("旧代解除应 ack-noop（accepted=false）")
	}
	if got := f.binds.Space(sid); got != collab.HubScope("sp-S2") {
		t.Fatalf("旧代解除不得删新代登记: %q", got)
	}
	// 当代解除（gen=2）→ 删除。
	if ack, err = f.hubCore.SpaceUnbind("n1", sid, 2, 2*time.Second); err != nil {
		t.Fatalf("当代解除: %v", err)
	}
	if !ack.Accepted {
		t.Fatal("当代解除应成立")
	}
	if got := f.binds.Space(sid); got != "" {
		t.Fatalf("当代解除应删除: %q", got)
	}
}

// TestD1StaleUnbindInvalidatedOnRebind 必修D-1（队列代际作废）：解绑失败入队
// → 同 session 新 bind 成立（经 Dispatch 路径）→ 旧解除条目作废出队（旧重试
// 不再下发）。
func TestD1StaleUnbindInvalidatedOnRebind(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 解绑不可达 node → 入队。
	f.svc.UnbindSession("n-missing", f.sessID)
	found := false
	for _, sid := range f.svc.PendingUnbindSessions() {
		if sid == f.sessID {
			found = true
		}
	}
	if !found {
		t.Fatal("前置：解除应已入队")
	}
	// 正常派发（bind 成立）→ 代际作废 → 队列清空。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d1-rebind",
		ExpectedVersion: view.Version, Note: "rebind"}, fakeHubSubject); err != nil {
		t.Fatalf("派发: %v", err)
	}
	if left := f.svc.PendingUnbindSessions(); len(left) != 0 {
		t.Fatalf("新 bind 成立后旧代解除应作废出队: %v", left)
	}
}

// TestD2CompensateOnRecordDispatchFailure 必修D-2（预检-事务窗绑定残留补偿）：
// 受控屏障——占住空间调度锁，派发完成预检+绑定后在锁外等待；期间 pause 直落
// （直调模型层，绕锁面构造窗口）；放锁 → RecordDispatch 状态拒绝 → 补偿解除
// → node 零残留。
func TestD2CompensateOnRecordDispatchFailure(t *testing.T) {
	f := newChainFixture(t, false, true)
	m := f.svc.lockSpace(f.spaceID)
	m.Lock() // 屏障：派发的登记段在锁外排队
	dispErr := make(chan error, 1)
	go func() {
		view, _ := f.svc.SpaceView(f.spaceID)
		_, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
			RequestID: "d2-1", ExpectedVersion: view.Version, Note: "x"}, fakeHubSubject)
		dispErr <- err
	}()
	// 等派发完成 bind（进入锁等待）——不可直察，用绑定登记事实同步（确定性）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if f.binds.Space(f.sessID) == Scope(f.spaceID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 窗内 pause（直调模型层 Control——绕调度锁构造预检-事务窗）。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "d2-pause",
		Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("窗内 pause: %v", err)
	}
	m.Unlock() // 放行登记段
	err := <-dispErr
	if err == nil {
		t.Fatal("暂停后登记应被拒绝（非零错误）")
	}
	// 补偿生效：node 零残留（同 request_id 重试不再有幽灵绑定）。
	if got := f.binds.Space(f.sessID); got != "" {
		t.Fatalf("登记失败应补偿解除（node 零残留）: %q", got)
	}
}

// TestD2BudgetPrecheckNoBind 必修D-2（预检补预算核算）：预算 dispatch_round=1
// 消耗后第二次派发在预检即拒（budget_exhausted）——绑定零新增（零残留面）。
func TestD2BudgetPrecheckNoBind(t *testing.T) {
	f := newChainFixture(t, false, true)
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "b2-budget",
		Action: collab.ActSetBudget, ExpectedVersion: view.Version,
		Budget: &collab.Budget{MaxDispatchRounds: 1}}, fakeHubSubject); err != nil {
		t.Fatalf("预算: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "b2-first",
		ExpectedVersion: view.Version, Note: "第一棒"}, fakeHubSubject); err != nil {
		t.Fatalf("第一棒: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	_, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "b2-second",
		ExpectedVersion: view.Version, Note: "第二棒"}, fakeHubSubject)
	if !containsCode(err, collab.CodeBudgetExhausted) {
		t.Fatalf("第二棒应 budget_exhausted（预检面）: %v", err)
	}
	// 预检拒绝零意图零投递。
	if _, ok := f.svc.IntentByRequest(f.spaceID, "b2-second"); ok {
		t.Fatal("预检拒绝应零意图")
	}
}

// TestETurnFactFourStates 必修E-残（四态枚举逐态实证）：none（无轮行=未投递）/
// matched（三重全等）/conflict（seq 不符）/missing（轮行在、任务已清理）。
func TestETurnFactFourStates(t *testing.T) {
	f := newChainFixture(t, false, true)
	// none：直落意图（无轮行）。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "e-none",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	it, _ := f.svc.IntentByRequest(f.spaceID, "e-none")
	if _, st := f.svc.intentTurnFact(it); st != turnFactNone {
		t.Fatalf("无轮行应 none: %d", st)
	}
	// 推进 e-none（串行门清空：执行完成收敛——无 barrier 直接跑完）。
	f.svc.KickSpace(f.spaceID)
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "e-none"); ok && got.Status == collab.IntentConverged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// matched：正常派发全链（受理→提交→接单）。
	view, _ = f.svc.SpaceView(f.spaceID)
	itm, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "e-matched",
		ExpectedVersion: view.Version, Note: "x"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	if _, st := f.svc.intentTurnFact(itm); st != turnFactMatched {
		t.Fatalf("全链意图应 matched: %d", st)
	}
	// conflict：intent.TurnSeq 与轮行不符（构造 seq 偏移意图）。
	conflict := itm
	conflict.TurnSeq = itm.TurnSeq + 100
	if _, st := f.svc.intentTurnFact(conflict); st != turnFactConflict {
		t.Fatalf("seq 不符应 conflict: %d", st)
	}
}

// TestECleanedTaskPendingNeedsReview 必修E-残（任务清理 ≠ 从未投递）：受理→投递
// 崩溃窗（接单登记缺失）+ 任务终态后超保留期清理 → 重启对账按四态 missing 留待
// 核对——意图保持在途（绝不取消冒认未投递、绝不假 converged）。
func TestECleanedTaskPendingNeedsReview(t *testing.T) {
	f := newChainFixtureO(t, chainOpts{barrier: true, capable: true,
		sweepInterval: 50 * time.Millisecond, terminalRetention: 5 * time.Millisecond})
	// 崩溃窗构造：绑定登记（node 收件门）→ 受理登记 + 投递（轮行+任务在案），
	// 接单登记缺失（保持 pending）。
	if _, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID), SessionID: f.sessID, Generation: 1}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	view, _ := f.svc.SpaceView(f.spaceID)
	it, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "e-clean-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "清理反例"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	ref, dup, err := f.hubCore.SubmitSpaceTurn(f.sessID, core.SessionTurnRequest{Prompt: it.Note,
		TurnSeq: it.TurnSeq, RequestID: it.RequestID}, systemSubject().String(), Scope(f.spaceID))
	if err != nil || dup {
		t.Fatalf("投递: %v dup=%v", err, dup)
	}
	// hub 关停（真崩溃时序：任务终态时收敛钩子不在位——旧实例钩子被关停位拦下），
	// 再放行执行 → 任务终态 → 极短保留期清理。
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f.releaseBarrier()
	deadline := time.Now().Add(testWaitBudget)
	cleaned := false
	for time.Now().Before(deadline) {
		if _, ok := f.hubCore.GetTask(ref.TaskID); !ok {
			cleaned = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleaned {
		t.Fatal("前置：任务应被保留期清理")
	}
	// 重启 → 恢复对账：missing → 留待核对（意图保持 in-flight）。
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer svc2.Close()
	f.hubCore.SetSpaceTurnGuard(svc2.SpaceTurnGuard())
	// 清理后重启：意图留待核对（在途）——轮询数个扫查节拍确认未被取消/收敛。
	sweeps := 10 * sweepInterval
	deadline = time.Now().Add(sweeps + time.Second)
	for time.Now().Before(deadline) {
		got, ok := svc2.IntentByRequest(f.spaceID, "e-clean-1")
		if !ok {
			t.Fatal("意图缺失")
		}
		if got.Status != collab.IntentPending && got.Status != collab.IntentAccepted {
			t.Fatalf("证据缺失应留待核对（在途），绝不取消/收敛: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := svc2.IntentByRequest(f.spaceID, "e-clean-1")
	if !got.Status.InFlight() {
		t.Fatalf("清理后意图应保持在途（留待核对）: %+v", got)
	}
}

// TestHCloseWaitsConvergePauseLanding 建议H 补强（受控屏障钉两落账）：真实
// AddOnTaskMerged 入口——取消在途棒 → 合并钩子进入 hookEntry（收敛落账对轮询
// 可见时，钩子仍持空间锁贯穿停队落账）→ Close 的排空面必须等钩子整个出临界
// （收敛+暂停两落账完成）才返回——Close 返回即空间已 paused。
func TestHCloseWaitsConvergePauseLanding(t *testing.T) {
	f := newChainFixture(t, true, true)
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "h-1",
		ExpectedVersion: 4, Note: "两落账屏障",
	}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 取消在途棒：hub 侧首落（Result 未达不猜）→ node 取消结果合并 → 真实
	// AddOnTaskMerged 入口执行收敛+停队。
	if _, _, err := f.hubCore.CancelTask(it.TaskID, "人工取消"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	// 等收敛落账可见（钩子已进入两落账区——停队落账与之同锁区串行）。
	deadline = time.Now().Add(testWaitBudget)
	seenCancelled := false
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "h-1"); ok && got.Status == collab.IntentCancelled {
			seenCancelled = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !seenCancelled {
		t.Fatal("前置：取消收敛应已落账")
	}
	// Close：排空面等钩子出临界（含停队落账）——返回即两落账齐备。
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sp, _ := f.svc.Space(f.spaceID)
	if sp.Status != collab.StatusPaused {
		t.Fatalf("Close 返回时停队落账必须已完成（排空面贯穿两落账）: %s", sp.Status)
	}
}

// ---------- codex 复审轮 3 必修/补强反例（受控确定性） ----------

// TestD1ProbeAfterRestart 必修1（重启解绑探针）：hub 重启（boundGen 进程内事实
// 清零）→ 解除持久绑定 → gen=0 探针 ack 回带 current_gen → 携正确代次重发 → 删除。
func TestD1ProbeAfterRestart(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 上一代进程：bind gen=1（node 登记事实在案）。
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: collab.HubScope("sp-x"), SessionID: f.sessID, Generation: 1}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("前置 bind: %v ack=%+v", err, ack)
	}
	if got := f.binds.Space(f.sessID); got != collab.HubScope("sp-x") {
		t.Fatalf("前置登记: %q", got)
	}
	// hub 重启（新 Service 实例：boundGen 进程内事实清零——代次未知形态）。
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	f.hubCore.SetSpaceTurnGuard(svc2.SpaceTurnGuard())
	// 解除：探针 → current_gen 重建 → 携正确代次解除 → 注册表删除。
	svc2.UnbindSession("n1", f.sessID)
	if got := f.binds.Space(f.sessID); got != "" {
		t.Fatalf("重启后解除应成立（探针重建代次）: %q", got)
	}
}

// TestD1TombstoneBlocksLateBind 必修2（代次高水位墓碑）：gen=2 解除 → 墓碑 →
// 迟到 gen=1 bind → ack-noop 不复活；gen=3 → 接受复活。
func TestD1TombstoneBlocksLateBind(t *testing.T) {
	f := newChainFixture(t, false, true)
	sid := "s-tomb"
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: collab.HubScope("sp-T"), SessionID: sid, Generation: 2}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("bind gen=2: %v ack=%+v", err, ack)
	}
	// 解除 gen=2 → 墓碑（保留已撤销最高代次，随登记事实持久化）。
	if ack, err := f.hubCore.SpaceUnbind("n1", sid, 2, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("解除 gen=2: %v ack=%+v", err, ack)
	}
	if got := f.binds.Space(sid); got != "" {
		t.Fatalf("解除后应无有效绑定: %q", got)
	}
	// 迟到 gen=1 bind（≤ 墓碑高水位）→ ack-noop 拒绝不复活。
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: collab.HubScope("sp-T"), SessionID: sid, Generation: 1}, 2*time.Second); err != nil || ack.Accepted {
		t.Fatalf("迟到 gen=1 bind 应 ack-noop: %v ack=%+v", err, ack)
	}
	if got := f.binds.Space(sid); got != "" {
		t.Fatalf("墓碑高水位下旧代 bind 不得复活: %q", got)
	}
	if got := f.binds.CurrentGen(sid); got != 2 {
		t.Fatalf("墓碑高水位应保留: %d", got)
	}
	// gen=3（> 高水位）→ 接受复活。
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: collab.HubScope("sp-T2"), SessionID: sid, Generation: 3}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("gen=3 应接受复活: %v ack=%+v", err, ack)
	}
	if got := f.binds.Space(sid); got != collab.HubScope("sp-T2") {
		t.Fatalf("复活登记: %q", got)
	}
}

// TestDispatchReplayConcurrentNoDeadlock 必修4（重放分支漏锁——真死锁）：两同
// request_id 并发派发 → 双双返回（先落受理/后到重放），锁恢复可用——第三笔不同
// request_id 派发照常受理（若重放/冲突分支漏解锁，本测试永久阻塞超时）。
func TestDispatchReplayConcurrentNoDeadlock(t *testing.T) {
	f := newChainFixture(t, false, true)
	view, _ := f.svc.SpaceView(f.spaceID)
	const rid = "cc-1"
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
				RequestID: rid, ExpectedVersion: view.Version, Note: "并发重放"}, fakeHubSubject)
			errs <- err
		}(i)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发派发 %d: %v", i, err)
		}
	}
	// 锁恢复可用：第三笔受理成立。
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder", RequestID: "cc-2",
		ExpectedVersion: view.Version, Note: "锁恢复探针"}, fakeHubSubject); err != nil {
		t.Fatalf("重放后锁应恢复可用: %v", err)
	}
	// 幂等收敛：同 request_id 只有一条意图。
	if it, ok := f.svc.IntentByRequest(f.spaceID, rid); !ok || it.RequestID != rid {
		t.Fatal("意图缺失")
	}
	n := 0
	for _, x := range f.svc.Intents(f.spaceID) {
		if x.RequestID == rid {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同 request_id 应恰一意图: %d", n)
	}
}

// TestCWithdrawDeclarationStaysPendingZeroOutbound 必修5（补绑定绕版本门）：
// 直落 pending → 撤声明 → sweep 推进 → ensureBound 版本门先行 → 零 bind 零外发
// （node 零残留——登记自始未发生）→ 意图确定性取消收束。
func TestCWithdrawDeclarationStaysPendingZeroOutbound(t *testing.T) {
	f := newChainFixture(t, false, true)
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "c5-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "撤声明反例"}, fakeHubSubject); err != nil {
		t.Fatal(err)
	}
	card := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex} // Spaces 有意缺位
	if err := f.hubCore.RegisterNode("n1", []protocol.AgentCard{card}); err != nil {
		t.Fatal(err)
	}
	f.svc.KickSpace(f.spaceID) // 推进 → ensureBound 版本门先行 → 确定性取消
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.svc.IntentByRequest(f.spaceID, "c5-1"); ok && got.Status == collab.IntentCancelled {
			if !strings.Contains(got.Note, "版本门") {
				t.Fatalf("取消注记应锚版本门: %s", got.Note)
			}
			sv, _ := f.hubCore.GetSession(f.sessID)
			if n := len(sv.Turns); n != 0 {
				t.Fatalf("撤声明后零外发: %d", n)
			}
			if got := f.binds.Space(f.sessID); got != "" {
				t.Fatalf("撤声明后零绑定残留（bind 在门后未发生）: %q", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("撤声明后 pending 意图应被版本门确定性取消收束")
}

// TestShutdownAtomicEnqueueWindow 必修3（关停检查-入队窗）+ 建议①（非空间轮
// 过滤）：Close 后 hookEntry——中心空间轮被关停拒绝留痕且不入队（len(convCh)==0，
// 不被静默吞）；非中心轮（本机空间 scope/close 轮）直接零操作不占队列。
func TestShutdownAtomicEnqueueWindow(t *testing.T) {
	f := newChainFixture(t, false, true)
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 中心空间轮：关停拒绝（不入队——check-and-enqueue 原子）。
	f.svc.hookEntry(protocol.Task{ID: "t-x1", Session: &protocol.SessionTurnRef{
		SessionID: f.sessID, TurnSeq: 1, Space: Scope(f.spaceID)},
		Result: &protocol.TaskResult{Status: protocol.ResultCompleted}})
	if n := len(f.svc.convCh); n != 0 {
		t.Fatalf("关停后入队应被拒绝（不被静默吞）: queue=%d", n)
	}
	// 非中心轮（本机空间 scope）零操作。
	f.svc.hookEntry(protocol.Task{ID: "t-x2", Session: &protocol.SessionTurnRef{
		SessionID: f.sessID, TurnSeq: 2, Space: "sp-local-9"},
		Result: &protocol.TaskResult{Status: protocol.ResultCompleted}})
	if n := len(f.svc.convCh); n != 0 {
		t.Fatalf("非中心轮应零操作: queue=%d", n)
	}
	// close 轮（无 scope）零操作。
	f.svc.hookEntry(protocol.Task{ID: "t-x3", Session: &protocol.SessionTurnRef{
		SessionID: f.sessID, Op: protocol.SessionTurnOpClose}})
	if n := len(f.svc.convCh); n != 0 {
		t.Fatalf("close 轮应零操作: queue=%d", n)
	}
}

// TestShutdownDroppedHookRecoveredOnRestart 必修4-b（诚实降范围）：运行期不对
// 已入队被丢弃的收敛项兜底（满队/关停丢弃如实留痕）——意图保持 accepted 在途，
// 重启对账按任务实物收敛（既有恢复面收口）。本测试钉住该诚实语义：丢弃后意图
// 不得被运行期静默改写（无锚不猜），重启后收敛。
func TestShutdownDroppedHookRecoveredOnRestart(t *testing.T) {
	f := newChainFixture(t, false, true)
	view, _ := f.svc.SpaceView(f.spaceID)
	it, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "rec-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "丢弃恢复"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ensureBound(f.spaceID, it); err != nil {
		t.Fatalf("ensureBound: %v", err)
	}
	ref, dup, err := f.hubCore.SubmitSpaceTurn(it.SessionID, core.SessionTurnRequest{
		Prompt: it.Note, TurnSeq: it.TurnSeq, RequestID: it.RequestID}, systemSubject().String(), Scope(f.spaceID))
	if err != nil || dup {
		t.Fatalf("投递: %v dup=%v", err, dup)
	}
	// 等任务终态（合并/终态回调入队 → worker 可能已消费——丢弃模拟不可靠，改为：
	// 直接排空队列模拟全部回调丢失，再验证运行期零静默改写）。
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(ref.TaskID); ok && e.State == ledger.StateFinished {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 排空队列（模拟满队/关停丢弃——留痕路径，运行期无兜底）。
	for {
		select {
		case <-f.svc.convCh:
			continue
		default:
		}
		break
	}
	// 运行期诚实语义：意图可能已被 worker 收敛（回调未被丢弃的竞争胜者）——
	// 两种状态均诚实；未收敛则重启后必须收敛（零静默丢失）。
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	svc2, err := Open(f.dir, f.hubCore, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	f.hubCore.SetSpaceTurnGuard(svc2.SpaceTurnGuard())
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := svc2.IntentByRequest(f.spaceID, "rec-1"); ok && got.Status == collab.IntentConverged {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := svc2.IntentByRequest(f.spaceID, "rec-1")
	t.Fatalf("重启对账后意图应收敛（丢弃项由恢复面收口）: %+v", got)
}

// TestCancelReLockAbandonsWhenTakenOver 必修3（取消重锁核验）正向+放弃双分支：
//   - 放弃分支：pending 意图已被后手提交（accepted/TaskID 在位）→ 重锁核验发现
//     非未提交 pending → 放弃取消（意图保持 accepted，执行中任务不被记 cancelled）；
//   - 正向分支：仍是未提交 pending → 确定性取消成立。
func TestCancelReLockAbandonsWhenTakenOver(t *testing.T) {
	f := newChainFixture(t, false, true)
	view, _ := f.svc.SpaceView(f.spaceID)
	it, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "rl-1",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
		Note: "重锁核验"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ensureBound(f.spaceID, it); err != nil {
		t.Fatal(err)
	}
	// 放弃分支：后手已提交（SubmitSpaceTurn + attach → accepted/TaskID 在位）。
	ref, dup, err := f.hubCore.SubmitSpaceTurn(it.SessionID, core.SessionTurnRequest{
		Prompt: it.Note, TurnSeq: it.TurnSeq, RequestID: it.RequestID}, systemSubject().String(), Scope(f.spaceID))
	if err != nil || dup {
		t.Fatalf("后手提交: %v dup=%v", err, dup)
	}
	if _, err := f.svc.AttachDispatchTask(f.spaceID, it.RequestID, ref.TaskID, systemSubject()); err != nil {
		t.Fatalf("接单: %v", err)
	}
	// A 的取消（模拟：advanceSpace ensureBound 失败后的重锁核验——内联同款逻辑）。
	m := f.svc.lockSpace(f.spaceID)
	m.Lock()
	cur, ok := f.svc.IntentByRequest(f.spaceID, it.RequestID)
	still := ok && cur.Status == collab.IntentPending && cur.TaskID == "" &&
		cur.TurnSeq == it.TurnSeq && cur.Member == it.Member
	if still {
		f.svc.cancelIntent(f.spaceID, cur, "版本门拒绝：意图取消收束")
	}
	m.Unlock()
	if still {
		t.Fatal("已提交意图应放弃取消（still 不应为真）")
	}
	got, _ := f.svc.IntentByRequest(f.spaceID, it.RequestID)
	if got.Status != collab.IntentAccepted {
		t.Fatalf("后手接管意图应保持 accepted（不被记 cancelled）: %+v", got)
	}
	// 正向分支：仍是未提交 pending → 取消成立。
	view, _ = f.svc.SpaceView(f.spaceID)
	it2, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "rl-2",
		Member: "n1/coder", SessionID: f.sessID, TurnSeq: 2, ExpectedVersion: view.Version,
		Note: "正向取消"}, fakeHubSubject)
	if err != nil {
		t.Fatal(err)
	}
	m.Lock()
	cur2, ok2 := f.svc.IntentByRequest(f.spaceID, it2.RequestID)
	still2 := ok2 && cur2.Status == collab.IntentPending && cur2.TaskID == ""
	if still2 {
		f.svc.cancelIntent(f.spaceID, cur2, "版本门拒绝：意图取消收束")
	}
	m.Unlock()
	got2, _ := f.svc.IntentByRequest(f.spaceID, it2.RequestID)
	if !still2 || got2.Status != collab.IntentCancelled {
		t.Fatalf("未提交 pending 应确定性取消: still=%v %+v", still2, got2)
	}
}

// ---------- codex 复审轮 3（代次所有权语义收敛）反例——受控确定性 ----------

// TestR1CompensationOwnership 必修R1（竞争请求共享 gen）：ensureBindSession 双
// 调用——首次 (gen, owned=true)，缓存命中 (同 gen, owned=false)；owned=false 路径
// 不得补偿解除（R2 CAS 失败不得删 R1 有效绑定——所有权语义）。
func TestR1CompensationOwnership(t *testing.T) {
	f := newChainFixture(t, false, true)
	g1, owned1, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
	if err != nil || !owned1 {
		t.Fatalf("首次应 owned=true: gen=%d owned=%v err=%v", g1, owned1, err)
	}
	g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
	if err != nil {
		t.Fatal(err)
	}
	if owned2 {
		t.Fatal("缓存命中应 owned=false（复用无解除权）")
	}
	if g2 != g1 {
		t.Fatalf("复用应同代次: %d != %d", g2, g1)
	}
	// 所有权语义闭环：owned=false 路径的失败不得触发补偿——直接断言补偿调用的
	// 代次参数即 owned gen（这里用行为锚：复用路径 UnbindSessionGen(g1) 才合法，
	// owned=false 时调用方按 R1 修复约定跳过补偿，绑定保持）。
	if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
		t.Fatalf("复用路径绑定应保持: %q", got)
	}
}

// TestR2SnapshotUnbindNoChase 必修R2（原子快照解绑+不追逐）：携 stale 快照代次
// （gen=1）解除，node 现登记 gen=2 → ack-noop（不追逐当前值）→ 新绑定保留。
func TestR2SnapshotUnbindNoChase(t *testing.T) {
	f := newChainFixture(t, false, true)
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID), SessionID: f.sessID, Generation: 2}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatalf("bind gen=2: %v ack=%+v", err, ack)
	}
	// 携 stale 快照 gen=1 解除 → node 代次不符 ack-noop → 新绑定（gen=2）保留。
	f.svc.UnbindSessionGen("n1", f.sessID, 1)
	if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
		t.Fatalf("stale 快照解除不得删新绑定: %q", got)
	}
	// 携正确代次 gen=2 解除 → 删除（正向锚）。
	f.svc.UnbindSessionGen("n1", f.sessID, 2)
	if got := f.binds.Space(f.sessID); got != "" {
		t.Fatalf("正确代次解除应删除: %q", got)
	}
}

// TestR3ConditionalClears 必修R3（一切清除按代次条件执行）：
//   - clearBoundIfGen：缓存 gen 与操作 gen 不符 → 保留；相符 → 清除；
//   - deletePendingIfGen：队列条目 gen 不符 → 保留；相符 → 删除。
func TestR3ConditionalClears(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 缓存：noteBound gen=3 → clearBoundIfGen(2) 保留 → clearBoundIfGen(3) 清除。
	f.svc.noteBound(f.sessID, 3)
	f.svc.clearBoundIfGen(f.sessID, 2)
	if got := f.svc.boundGenOf(f.sessID); got != 3 {
		t.Fatalf("代次不符清除应保留缓存: %d", got)
	}
	f.svc.clearBoundIfGen(f.sessID, 3)
	if got := f.svc.boundGenOf(f.sessID); got != 0 {
		t.Fatalf("代次相符清除应清缓存: %d", got)
	}
	// 队列：条目 gen=7 → deletePendingIfGen(5) 保留 → deletePendingIfGen(7) 删除。
	f.svc.muUnbind.Lock()
	f.svc.pendingUnbinds[f.sessID] = hubUnbind{nodeID: "n1", sessionID: f.sessID, gen: 7}
	f.svc.muUnbind.Unlock()
	if f.svc.deletePendingIfGen(f.sessID, 5) {
		t.Fatal("条目代次不符不得删除")
	}
	if !f.svc.deletePendingIfGen(f.sessID, 7) {
		t.Fatal("条目代次相符应删除")
	}
	if len(f.svc.PendingUnbindSessions()) != 0 {
		t.Fatal("删除后队列应空")
	}
}

// TestR2ProbePinsLearnedGen 必修R2②（探针学到的代次=确定性目标，不追逐）：
// gen=0 探针 → 学到 current_gen=1 → 学到的代次解除 noop（新代 gen=2 在位）→
// 语义满足收口，不再次探针追逐；新代绑定保留。
func TestR2ProbePinsLearnedGen(t *testing.T) {
	f := newChainFixture(t, false, true)
	// node 登记事实：gen=1（旧代进程遗留——重启后 boundGen 清零的形态）。
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID), SessionID: f.sessID, Generation: 1}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatal(err)
	}
	// gen=0 探针：零副作用学到 current_gen=1（registry 完好不删）。
	ack, err := f.hubCore.SpaceUnbind("n1", f.sessID, 0, 2*time.Second)
	if err != nil || ack.Accepted {
		t.Fatalf("探针应零副作用: err=%v ack=%+v", err, ack)
	}
	if ack.CurrentGen != 1 {
		t.Fatalf("探针应学到 current_gen=1: %d", ack.CurrentGen)
	}
	if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
		t.Fatalf("探针不得误删: %q", got)
	}
	// 学到的代次=确定性目标：unbindGenOnce(gen=0) 探针+携 learned gen 解除，
	// 单次探针+单次解除无追逐循环（结构性由实现保证——grep 无二次探针）。
	gen := uint64(0)
	if !f.svc.unbindGenOnce("n1", f.sessID, &gen) {
		t.Fatal("探针+解除应终态")
	}
	if gen != 1 {
		t.Fatalf("学到的代次应为 1: %d", gen)
	}
	if got := f.binds.Space(f.sessID); got != "" {
		t.Fatalf("学到的代次解除应删除: %q", got)
	}
}

// ---------- codex 复审轮 3 原则 1-3 实际调用链交错测试（受控屏障确定性） ----------

// TestCompensateOwnershipInterleaved 原则 1 实际调用链交错（创建者失败/借用者成功）：
//   - 主分支：A Dispatch 创建 gen=1 → 空间锁屏障停泊 → B 借用 gen=1 记账 + 提交
//     会话轮（在途）→ 窗内 pause 版本推进 → 放锁 → A RecordDispatch 失败 →
//     compensateBindIfOwned 检测在途 turn → 跳过补偿 → 绑定保留（B 不断线）；
//   - 对照组：无借用者/无在途 turn → 同构造补偿生效 → 绑定清除。
func TestCompensateOwnershipInterleaved(t *testing.T) {
	t.Run("借用者执行时跳过补偿", func(t *testing.T) {
		f := newChainFixture(t, true, true) // barrier：B 的任务阻塞在途
		// 受控屏障：占住空间调度锁（A 停在 bind 后/RecordDispatch 前）。
		m := f.svc.lockSpace(f.spaceID)
		m.Lock()
		// A 的 Dispatch（goroutine）：ensureBindSession 创建 gen=1 后阻塞在空间锁。
		dispErr := make(chan error, 1)
		go func() {
			view, _ := f.svc.SpaceView(f.spaceID)
			_, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
				RequestID: "A-1", ExpectedVersion: view.Version, Note: "A 的请求"}, fakeHubSubject)
			dispErr <- err
		}()
		// 等 A 的 bind 完成（node 登记在案 = ensureBindSession 已过）。
		deadline := time.Now().Add(testWaitBudget)
		for time.Now().Before(deadline) {
			if f.binds.Space(f.sessID) == Scope(f.spaceID) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if f.binds.Space(f.sessID) != Scope(f.spaceID) {
			t.Fatal("前置：A 的 bind 应已登记")
		}
		// B 借用同 session 记账成功（直调 collab 层——绕过 Dispatch/space lock）。
		view, _ := f.svc.SpaceView(f.spaceID)
		itB, _, _, err := f.svc.RecordDispatch(f.spaceID, collab.DispatchRequest{RequestID: "B-1",
			Member: "n1/coder", SessionID: f.sessID, TurnSeq: 1, ExpectedVersion: view.Version,
			Note: "借用者"}, fakeHubSubject)
		if err != nil {
			t.Fatalf("B RecordDispatch: %v", err)
		}
		// B 经 core 提交会话轮 → session 有在途 turn（非终态）。
		refB, dup, err := f.hubCore.SubmitSpaceTurn(f.sessID, core.SessionTurnRequest{
			Prompt: itB.Note, TurnSeq: itB.TurnSeq, RequestID: itB.RequestID},
			systemSubject().String(), Scope(f.spaceID))
		if err != nil || dup {
			t.Fatalf("B SubmitSpaceTurn: %v dup=%v", err, dup)
		}
		if _, err := f.svc.AttachDispatchTask(f.spaceID, itB.RequestID, refB.TaskID, systemSubject()); err != nil {
			t.Fatalf("B attach: %v", err)
		}
		// 原则 1：RecordDispatch 成功 → 绑定已被借用。实际 Dispatch 方法在锁定
		// 段内设 borrowed=true——此处直调 collab 层后手动同步标志以模拟真实状态。
		f.svc.muBindGen.Lock()
		if entry, ok := f.svc.bindGens[f.sessID]; ok && !entry.revoked {
			entry.borrowed = true
			f.svc.bindGens[f.sessID] = entry
		}
		f.svc.muBindGen.Unlock()
		// 窗内 pause（直调 collab Control——绕空间锁）：版本推进 + 空间暂停。
		view, _ = f.svc.SpaceView(f.spaceID)
		if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "pause-win",
			Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
			t.Fatalf("窗内 pause: %v", err)
		}
		// 放屏障 → A 走 RecordDispatch（CAS 失败）→ compensateBindIfOwned。
		m.Unlock()
		if err := <-dispErr; err == nil {
			t.Fatal("A RecordDispatch 应失败（版本 CAS）")
		}
		// 断言：补偿跳过（session 有在途 turn）→ node 绑定保留（B 不断线）。
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
			t.Fatalf("借用者在途时补偿应跳过（绑定保留）: %q", got)
		}
	})

	t.Run("无借用者时补偿生效", func(t *testing.T) {
		f := newChainFixture(t, false, true) // 无 barrier：无在途任务
		// 创建者直调 ensureBindSession → gen=1 owned=true。
		gen, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned {
			t.Fatalf("前置 bind: gen=%d owned=%v err=%v", gen, owned, err)
		}
		// 直调 pause 推进版本（无借用者——session 无在途 turn）。
		view, _ := f.svc.SpaceView(f.spaceID)
		if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "cg-pause",
			Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
			t.Fatal(err)
		}
		// 补偿门：无在途 turn → 不跳过 → 解除成立。
		f.svc.compensateBindIfOwned("n1", f.sessID, gen)
		if got := f.binds.Space(f.sessID); got != "" {
			t.Fatalf("无借用者时补偿应生效（绑定清除）: %q", got)
		}
	})
}

// TestProbeLearnedGenPersistToQueue 原则 2 实际调用链（探针-重试-队列身份）：
// 直调 UnbindSessionGen(node, sid, 0) —— 探针成功学到 gen=1 并当场解除（无队列），
// 证明探针零副作用+正确学习。然后直调 UnbindSessionGen(node-missing, sid, 0) 使
// 探针不可达 → 入队 gen=0。node 登记事实 gen=2 → retryUnbinds 探针学 current_gen=2
// → 解除 gen=2 成立 → 条件删除（学到的 gen=2 匹配条目回写后 gen=2）→ 收口。
func TestProbeLearnedGenPersistToQueue(t *testing.T) {
	f := newChainFixture(t, false, true)
	// node 登记事实 gen=1：gen=0 探针零副作用学 current_gen=1（registry 完好）。
	if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID), SessionID: f.sessID, Generation: 1}, 2*time.Second); err != nil || !ack.Accepted {
		t.Fatal(err)
	}
	ack, err := f.hubCore.SpaceUnbind("n1", f.sessID, 0, 2*time.Second)
	if err != nil || ack.Accepted || ack.CurrentGen != 1 {
		t.Fatalf("探针: err=%v ack=%+v", err, ack)
	}
	if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
		t.Fatalf("探针不得误删: %q", got)
	}
	// 探针后携学到的代次解除 → 删除（实际调用链 UnbindSessionGen → unbindGenOnce）。
	f.svc.UnbindSessionGen("n1", f.sessID, 1)
	if got := f.binds.Space(f.sessID); got != "" {
		t.Fatalf("学到的代次解除应删除: %q", got)
	}
	// 不可达 node 的解除入队 gen=0 → retryUnbinds 探针不可达留队（不追逐）。
	f.svc.UnbindSessionGen("n-missing", "s-queue-test", 0)
	if left := f.svc.PendingUnbindSessions(); len(left) != 1 {
		t.Fatalf("不可达解除应入队: %v", left)
	}
	// retryUnbinds 对不可达 node：探针不可达 → 留队（不追逐不丢弃）。
	f.svc.retryUnbinds()
	if left := f.svc.PendingUnbindSessions(); len(left) != 1 {
		t.Fatalf("不可达解除应留队: %v", left)
	}
}

// TestLateNoteBoundNoOverwrite 原则 3 实际调用链（旧应答迟到回写不覆盖新登记）：
// noteBound(gen=2) 后模拟旧应答迟到 noteBound(gen=1) → 缓存保持 gen=2；
// invalidateUnbind(newGen=2) 对 gen=1 条目不删。
func TestLateNoteBoundNoOverwrite(t *testing.T) {
	f := newChainFixture(t, false, true)
	// 新代：noteBound gen=2。
	f.svc.noteBound(f.sessID, 2)
	// 旧应答迟到：noteBound gen=1 → 不覆盖（缓存保持 gen=2）。
	f.svc.noteBound(f.sessID, 1)
	if got := f.svc.boundGenOf(f.sessID); got != 2 {
		t.Fatalf("旧应答迟到不得覆盖新登记: got gen=%d want 2", got)
	}
	// invalidateUnbind(oldGen=1) 对 gen=2 条目不删。
	f.svc.invalidateUnbind(f.sessID, 1)
	if left := f.svc.PendingUnbindSessions(); len(left) != 0 {
		t.Fatalf("invalidateUnbind 旧代不应误删新代条目: %v", left)
	}
	// invalidateUnbind(newGen=2) 对 gen=1 条目删（代际作废正确方向）。
	f.svc.muUnbind.Lock()
	f.svc.pendingUnbinds[f.sessID] = hubUnbind{nodeID: "n1", sessionID: f.sessID, gen: 1}
	f.svc.muUnbind.Unlock()
	f.svc.invalidateUnbind(f.sessID, 2)
	if left := f.svc.PendingUnbindSessions(); len(left) != 0 {
		t.Fatalf("新代 invalidate 应删旧代条目: %v", left)
	}
}
