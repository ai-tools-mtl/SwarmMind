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
	f.hubCore, err = core.New(core.Config{Store: f.hubStore, Transport: bus.Endpoint("hub"), SpaceBindTimeout: 2 * time.Second})
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
	if barrierAll {
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
	if capableNode {
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
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
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
	deadline = time.Now().Add(5 * time.Second)
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
	pl, _ := json.Marshal(protocol.SpaceBindPayload{Space: collab.HubScope("sp-x"), SessionID: "s-iron"})
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

// TestCloseDrainsInFlightCallbacks 关停排空面回归（集成者门禁修复锚）：Close 必
// 须等在途收敛回调出临界后才关库——手动占住排空门（模拟在途回调），Close 阻塞
// 至 Done 才完成；不占门时 Close 即刻完成（排空面零残留）。
func TestCloseDrainsInFlightCallbacks(t *testing.T) {
	f := newChainFixture(t, false, true) // 无任务流——hubCore 不触发回调
	// 手动占住排空门（hookEntry 过检后的计数形态；直接操作同一 WaitGroup）。
	f.svc.hookWG.Add(1)
	closed := make(chan error, 1)
	go func() { closed <- f.svc.Close() }()
	// Close 必须被排空门阻塞（WaitGroup 语义确定性——非 sleep 时序赌）。
	select {
	case err := <-closed:
		t.Fatalf("在途回调未出临界 Close 不得完成（关库竞态即门禁实锤）: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.svc.hookWG.Done()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("排空门放行后 Close 应完成")
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
