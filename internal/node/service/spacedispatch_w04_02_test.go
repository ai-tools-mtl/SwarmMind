package service

// spacedispatch_w04_02_test.go 空间派发执行侧集成测试（W04-02，docs/tasks/v3/
// W04-spaces.md N02/N04 + 串行调度/过期租约暂停/取消恢复保护/重启对账/space_
// dispatch_required）。真内核（fake CLI）+ 真账本 + 真 collab 快照；时间 UTC。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// spaceSvcFixture 空间派发夹具：真 sessionx（fake CLI）+ 真账本 + sessionop +
// collab 快照注入 service。
type spaceSvcFixture struct {
	t      *testing.T
	svc    *Service
	ep     *transport.Endpoint
	mgr    *sessionx.Manager
	led    *ledger.Ledger
	ops    *sessionop.Service
	st     *collab.Store
	dir    string
	cwd    string
	cwd2   string
	fake   string
	runLog string
	mu     sync.Mutex
}

// spaceSvcOpt 夹具装配项。
type spaceSvcOpt struct {
	envkv      map[string]string
	barrierAll bool
	leaseTTL   time.Duration // collab LeaseTTL（0 = 默认）
	sweep      time.Duration // 调度扫查节拍（0 = 默认 1s；测试可缩短）
}

func newSpaceSvcFixture(t *testing.T, opt spaceSvcOpt) *spaceSvcFixture {
	t.Helper()
	dir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	f := &spaceSvcFixture{
		t: t, dir: dir, led: led,
		cwd:    filepath.Join(dir, "ws-a"),
		cwd2:   filepath.Join(dir, "ws-b"),
		runLog: filepath.Join(dir, "run.log"),
	}
	for _, d := range []string{f.cwd, f.cwd2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.fake = buildSessionFakeCLI(t)
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
		"FAKE_THREAD_ID=thr-sp-1", "FAKE_LOG=" + f.runLog,
		"FAKE_WAIT_FILE=" + filepath.Join(dir, "go.barrier")}
	_, wantResume := opt.envkv["FAKE_ON_RESUME"]
	if !opt.barrierAll && !wantResume {
		env = env[:len(env)-1] // 无屏障形态：去掉 WAIT_FILE
	}
	if wantResume {
		env = append(env, "FAKE_ON_RESUME=1")
	}
	for _, k := range []string{"FAKE_TOUCH"} {
		if v, ok := opt.envkv[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	specs := map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: f.fake,
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: env,
	}}
	mgr, err := sessionx.NewManager(filepath.Join(dir, "kernel"), specs, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	f.mgr = mgr
	ops, err := sessionop.New(filepath.Join(dir, "ops"), led, sessionop.Options{})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	f.ops = ops
	stDir := filepath.Join(dir, "collab")
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stOpts := collab.Options{}
	if opt.leaseTTL > 0 {
		stOpts.LeaseTTL = opt.leaseTTL
	}
	st, err := collab.Open(stDir, stOpts)
	if err != nil {
		t.Fatalf("装配 collab: %v", err)
	}
	f.st = st
	if opt.sweep > 0 {
		old := spaceSweepInterval
		spaceSweepInterval = opt.sweep
		t.Cleanup(func() { spaceSweepInterval = old })
	}
	bus := transport.NewBus()
	f.ep = bus.Endpoint("hub")
	f.svc = New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{},
		ArtifactRoots: map[string]string{"coder": filepath.Join(dir, "artifacts")},
		Sessions:      mgr,
		SessionOps:    ops,
		Collab:        st,
		NodeID:        "n1",
	}, bus.Endpoint("n1"))
	t.Cleanup(func() {
		f.svc.Close()
		_ = st.Close()
	})
	return f
}

var (
	spOwner   = collab.Subject{Name: "owner", Class: collab.ClassOwner}
	spSteward = collab.Subject{Name: "prime", Class: collab.ClassOrchestrate}
)

// createSession 建一个已发布的成员会话（fake CLI 快跑首轮）。
func (f *spaceSvcFixture) createSession(cwd string) string {
	f.t.Helper()
	ack, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: cwd, Prompt: "首轮"})
	if err != nil {
		f.t.Fatalf("建会话: %v", err)
	}
	f.waitTurnFinished(ack.SessionID, 1, protocol.ResultCompleted, 8*time.Second)
	return ack.SessionID
}

// newManualSpace 建 manual 空间（owner 即负责人）+ ready + start（成员绑定会话）。
func (f *spaceSvcFixture) newManualSpace(sid string) collab.Space {
	f.t.Helper()
	return f.newManualSpaceBudget(sid, collab.Budget{})
}

func (f *spaceSvcFixture) newManualSpaceBudget(sid string, budget collab.Budget) collab.Space {
	f.t.Helper()
	sp, _, err := f.st.CreateSpace(collab.CreateRequest{
		Goal: "g", Mode: collab.ModeManual, Budget: budget,
		Members: []collab.Member{{Name: "coder", SessionID: sid}},
	}, spOwner)
	if err != nil {
		f.t.Fatalf("建空间: %v", err)
	}
	res, _, err := f.st.Control(sp.ID, collab.ControlRequest{RequestID: "r-" + sp.ID, Action: collab.ActReady, ExpectedVersion: sp.Version}, spOwner)
	if err != nil {
		f.t.Fatalf("ready: %v", err)
	}
	if _, _, err := f.st.Control(sp.ID, collab.ControlRequest{RequestID: "s-" + sp.ID, Action: collab.ActStart, ExpectedVersion: res.Space.Version}, spOwner); err != nil {
		f.t.Fatalf("start: %v", err)
	}
	return f.mustSpace(sp.ID)
}

func (f *spaceSvcFixture) mustSpace(id string) collab.Space {
	f.t.Helper()
	sp, ok := f.st.Space(id)
	if !ok {
		f.t.Fatalf("空间 %s 不存在", id)
	}
	return sp
}

// dispatch 派发一笔（owner；版本取现档）。
func (f *spaceSvcFixture) dispatch(spaceID, requestID, note string) (collab.DispatchIntent, bool, error) {
	f.t.Helper()
	sp := f.mustSpace(spaceID)
	return f.svc.SpaceDispatch(SpaceDispatchRequest{
		SpaceID: spaceID, Member: "coder", RequestID: requestID,
		ExpectedVersion: sp.Version, Note: note,
	}, spOwner)
}

func (f *spaceSvcFixture) intent(spaceID, requestID string) (collab.DispatchIntent, bool) {
	return f.st.IntentByRequest(spaceID, requestID)
}

// waitIntent 等意图到指定状态。
func (f *spaceSvcFixture) waitIntent(spaceID, requestID string, want collab.IntentStatus, within time.Duration) collab.DispatchIntent {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if it, ok := f.intent(spaceID, requestID); ok && it.Status == want {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.intent(spaceID, requestID)
	f.t.Fatalf("%v 内意图 %s 未到 %s（现 %+v）", within, requestID, want, it)
	return collab.DispatchIntent{}
}

// waitSpaceStatus 等空间到达目标状态（reasonSub 非空时须含于暂停原因）——条件探
// 针（轮询+deadline，W01-02 waitCtrlFinished/waitHolderEmpty 同型）：系统自动暂
// 停/取消停队等异步后果一律等其可观测，不做固定时长假设。
func (f *spaceSvcFixture) waitSpaceStatus(spaceID string, want collab.Status, reasonSub string, within time.Duration) collab.Space {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		sp := f.mustSpace(spaceID)
		if sp.Status == want && (reasonSub == "" || strings.Contains(sp.PausedReason, reasonSub)) {
			return sp
		}
		time.Sleep(10 * time.Millisecond)
	}
	sp := f.mustSpace(spaceID)
	f.t.Fatalf("%v 内空间未到 %s（现 %s 暂停原因=%q）", within, want, sp.Status, sp.PausedReason)
	return sp
}

func (f *spaceSvcFixture) runLines() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.runLog)
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

func (f *spaceSvcFixture) waitRunLine(line string, n int) {
	f.t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if countLines(f.runLines(), line) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("6s 内执行留痕未达 %d 个 %q: %v", n, line, f.runLines())
}

func (f *spaceSvcFixture) releaseBarrier() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		f.t.Fatalf("放行屏障: %v", err)
	}
}

func (f *spaceSvcFixture) waitTurnFinished(sid string, seq int64, status string, within time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e, ok := findLedgerTurn(f.led, sid, seq); ok && e.State == ledger.StateFinished && e.Result != nil && e.Result.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("%v 内 session=%s seq=%d 未落终态 %s", within, sid, seq, status)
}

// TestSpaceDispatchSerialConvergeReplay 串行调度主链 + N02 重放：同空间两笔派发
// 严格串行（第二棒等第一棒收敛才投递），轮终收敛分类 converged；同 request_id
// 重放零重复执行零重复扣（N02）。
func TestSpaceDispatchSerialConvergeReplay(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	sid := f.createSession(f.cwd)
	sp := f.newManualSpace(sid)

	// d1：投递并阻塞在屏障（accepted）。
	if _, dup, err := f.dispatch(sp.ID, "d1", "第一棒"); err != nil || dup {
		t.Fatalf("d1 受理: err=%v dup=%v", err, dup)
	}
	f.waitIntent(sp.ID, "d1", collab.IntentAccepted, 5*time.Second)
	f.waitRunLine("start", 2) // create + d1 已 spawn

	// d2：受理（意图持久+预算扣减）但不投递（串行等待）。
	it2, _, err := f.dispatch(sp.ID, "d2", "第二棒")
	if err != nil {
		t.Fatalf("d2 受理: %v", err)
	}
	if it2.Status != collab.IntentPending {
		t.Fatalf("d2 应为 pending（串行等待）: %+v", it2)
	}
	// 串行纪律断言（确定性采样点，零固定等待）：SpaceDispatch 返回即同步 kick 已
	// 完成——若串行门失效，d2 的投递（账本受理入账+接单登记）必然已同步发生；此处
	// 立即采样即可判定，不依赖执行侧 spawn 时序。异步入口（d1 收敛 hook / 扫查节
	// 拍）在同刻不可能投递（d1 accepted 在途、串行门同源），端到端顺序由本测试尾
	// 部执行留痕排序断言闭环。
	if _, occupied := findLedgerTurn(f.led, sid, 3); occupied {
		t.Fatalf("串行纪律被破坏（d2 在 d1 收敛前已投递，账本 seq3 已受理）")
	}
	if n := countLines(f.runLines(), "start"); n != 2 {
		t.Fatalf("串行纪律被破坏（d2 在 d1 收敛前 spawn）: start=%d", n)
	}

	// N02：同 request_id 重放——dup 回执、不新增意图、不重复扣。
	got, dup, err := f.dispatch(sp.ID, "d1", "第一棒")
	if err != nil || !dup || got.Status != collab.IntentAccepted {
		t.Fatalf("d1 重放应 dup 命中原意图: dup=%v err=%v %+v", dup, err, got)
	}
	if n := len(f.st.Intents(sp.ID)); n != 2 {
		t.Fatalf("重放不得新增意图: %d", n)
	}
	if u := f.mustSpace(sp.ID).Used.DispatchRounds; u != 2 {
		t.Fatalf("重放不得重复扣减（现 2 笔受理）: %d", u)
	}

	// 放行：d1 收敛 → kick → d2 投递 → 收敛（严格先后）。d2 投递后屏障已放行，
	// 秒级跑完——accepted 是瞬态（10ms 轮询可能错过），不对其断言；投递事实由
	// start#3 + converged 终态承载。
	f.releaseBarrier()
	f.waitIntent(sp.ID, "d1", collab.IntentConverged, 8*time.Second)
	f.waitRunLine("start", 3)
	f.waitIntent(sp.ID, "d2", collab.IntentConverged, 8*time.Second)
	// 串行端到端佐证（执行留痕排序，条件到达后采样）：d2 的 spawn（start#3）必须
	// 在 d1 的 end（end#2）之后——无执行重叠；三棒（create/d1/d2）各留痕一对。
	lines := f.runLines()
	if n := countLines(lines, "start"); n != 3 {
		t.Fatalf("执行留痕应恰 3 个 start（create/d1/d2）: %d", n)
	}
	d1End := nthIndexOf(lines, "end", 2)
	if d2Start := nthIndexOf(lines, "start", 3); d2Start < d1End {
		t.Fatalf("同空间两棒执行重叠（非串行）：d2 spawn 在 d1 end 之前: %v", lines)
	}
	if u := f.mustSpace(sp.ID).Used.DispatchRounds; u != 2 {
		t.Fatalf("派发预算应为 2: %d", u)
	}
}

// TestSpaceDispatchCrossSpaceParallel 跨空间并行：两个空间各一笔派发同时投递
// （各自会话、不同工作区——并发槽充足时并行 spawn）。
func TestSpaceDispatchCrossSpaceParallel(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}, sweep: 100 * time.Millisecond})
	sidA := f.createSession(f.cwd)
	sidB := f.createSession(f.cwd2)
	spA := f.newManualSpace(sidA)
	// 第二个空间复用coder会话不同绑：成员绑定 sidB。
	spB, _, err := f.st.CreateSpace(collab.CreateRequest{
		Goal: "g2", Mode: collab.ModeManual,
		Members: []collab.Member{{Name: "coder", SessionID: sidB}},
	}, spOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.Control(spB.ID, collab.ControlRequest{RequestID: "rb", Action: collab.ActReady, ExpectedVersion: spB.Version}, spOwner); err != nil {
		t.Fatal(err)
	}
	res, _, err := f.st.Control(spB.ID, collab.ControlRequest{RequestID: "sb", Action: collab.ActStart, ExpectedVersion: spB.Version + 1}, spOwner)
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	vB := f.mustSpace(spB.ID).Version

	if _, _, err := f.svc.SpaceDispatch(SpaceDispatchRequest{SpaceID: spA.ID, Member: "coder", RequestID: "pa", ExpectedVersion: f.mustSpace(spA.ID).Version, Note: "A"}, spOwner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.SpaceDispatch(SpaceDispatchRequest{SpaceID: spB.ID, Member: "coder", RequestID: "pb", ExpectedVersion: vB, Note: "B"}, spOwner); err != nil {
		t.Fatal(err)
	}
	f.waitIntent(spA.ID, "pa", collab.IntentAccepted, 5*time.Second)
	f.waitIntent(spB.ID, "pb", collab.IntentAccepted, 5*time.Second)
	f.waitRunLine("start", 4) // 两会话首轮 + 两棒并行 spawn
	f.releaseBarrier()
	f.waitIntent(spA.ID, "pa", collab.IntentConverged, 8*time.Second)
	f.waitIntent(spB.ID, "pb", collab.IntentConverged, 8*time.Second)
}

// TestSpaceDispatchRequiredRejectsNormalSends 空间绑定会话的普通续轮拒绝（方案 A）：
// 主人 local 面 + agent 面一律 space_dispatch_required；非绑定会话回归零变化；
// hub 下发旁路轮被执行口守卫拒绝（spaceTurnAllowed）。
func TestSpaceDispatchRequiredRejectsNormalSends(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	sid := f.createSession(f.cwd)
	f.newManualSpace(sid)

	// 主人 local 面：绑定会话续轮拒绝（零副作用——无账本条目）。
	entriesBefore := f.countSessionTasks()
	_, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "绕过"})
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != collab.CodeSpaceDispatchRequired {
		t.Fatalf("绑定会话续轮应 space_dispatch_required: %v", err)
	}
	// agent 面：同规。
	if _, _, err := f.svc.SubmitAgentSessionTurn(AgentSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "绕过2"}); !errors.As(err, &re) || re.Code != collab.CodeSpaceDispatchRequired {
		t.Fatalf("agent 面绑定会话投轮应 space_dispatch_required: %v", err)
	}
	// 重放路径不受守卫影响：已知受理轮（seq=1）同文重放回原回执。
	ack, dup, err := f.svc.SubmitAgentSessionTurn(AgentSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "首轮", TurnSeq: 1})
	if err != nil || !dup || ack.TurnSeq != 1 {
		t.Fatalf("绑定前受理轮重放应 dup 回执: dup=%v err=%v", dup, err)
	}
	if n := f.countSessionTasks(); n != entriesBefore {
		t.Fatalf("拒绝零副作用被破坏: %d → %d", entriesBefore, n)
	}
	// 非空间会话零变化（回归锚）：新会话照常建轮执行。
	free := f.createSession(f.cwd2)
	if _, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", SessionID: free, Prompt: "自由续轮"}); err != nil {
		t.Fatalf("非绑定会话续轮应放行: %v", err)
	}
	f.waitTurnFinished(free, 2, protocol.ResultCompleted, 8*time.Second)

	// hub 下发旁路轮：执行口守卫拒绝（伪造 SubmittedBy 也拦得住——TaskID 不可伪造）。
	bypass := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "hub 旁路"}, SubmittedBy: "space:forge",
		IdemKey: "bypass-1", Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 2, Op: protocol.SessionTurnOpSend, Cwd: f.cwd},
	}
	if err := f.led.AcceptOrigin(bypass, ledger.OriginHub); err != nil {
		t.Fatal(err)
	}
	startsBefore := countLines(f.runLines(), "start")
	f.svc.execute(bypass)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := f.led.Get(bypass.ID); ok && e.State == ledger.StateFinished {
			if e.Result == nil || !strings.HasPrefix(e.Result.Error, collab.CodeSpaceDispatchRequired) {
				t.Fatalf("hub 旁路轮应 space_dispatch_required 失败: %+v", e.Result)
			}
			if n := countLines(f.runLines(), "start"); n != startsBefore {
				t.Fatalf("旁路轮不得 spawn: before=%d after=%d", startsBefore, n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("旁路轮未落终态")
}

func (f *spaceSvcFixture) countSessionTasks() int {
	n := 0
	for _, e := range f.led.View() {
		if e.Task.Session != nil {
			n++
		}
	}
	return n
}

// TestSpaceLeaseExpiryAutoPause 过期租约自动暂停：租约过期且存在待投递意图 →
// 系统自动暂停（原因=租约过期）+ 未投递意图取消收束；已投递在途照常收敛（N04）；
// 恢复走显式 resume（主人接管后）。
func TestSpaceLeaseExpiryAutoPause(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{
		envkv: map[string]string{"FAKE_ON_RESUME": "1"},
		sweep: 40 * time.Millisecond,
	})
	sid := f.createSession(f.cwd)

	// external 空间：创建（编排主体）→ 领取（租约 1s）→ 配置 → ready → start。
	sp, _, err := f.st.CreateSpace(collab.CreateRequest{Goal: "g", Mode: collab.ModeExternal,
		Members: []collab.Member{{Name: "coder", SessionID: sid}}}, spSteward)
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := f.st.Control(sp.ID, collab.ControlRequest{
		RequestID: "claim", Action: collab.ActTakeover, ExpectedVersion: sp.Version,
		ExpectedTerm: 0, LeaseSeconds: 1,
	}, spSteward)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}
	term := claim.Space.Leader.Term
	cfg, _, err := f.st.Control(sp.ID, collab.ControlRequest{
		RequestID: "cfg", Action: collab.ActConfigureMembers, ExpectedVersion: claim.Space.Version, Term: term,
		Members: []collab.Member{{Name: "coder", SessionID: sid}},
	}, spSteward)
	if err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	rdy, _, err := f.st.Control(sp.ID, collab.ControlRequest{RequestID: "rdy", Action: collab.ActReady, ExpectedVersion: cfg.Space.Version, Term: term}, spSteward)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.Control(sp.ID, collab.ControlRequest{RequestID: "str", Action: collab.ActStart, ExpectedVersion: rdy.Space.Version, Term: term}, spSteward); err != nil {
		t.Fatalf("启动: %v", err)
	}
	// d1 投递（屏障阻塞，accepted）；d2 排队（pending）。
	stewardDispatch := func(requestID string, ver int64) error {
		_, _, err := f.svc.SpaceDispatch(SpaceDispatchRequest{
			SpaceID: sp.ID, Member: "coder", RequestID: requestID, ExpectedVersion: ver, Term: term, Note: requestID,
		}, spSteward)
		return err
	}
	if err := stewardDispatch("d1", f.mustSpace(sp.ID).Version); err != nil {
		t.Fatalf("d1: %v", err)
	}
	f.waitIntent(sp.ID, "d1", collab.IntentAccepted, 5*time.Second)
	f.waitRunLine("start", 2)
	if err := stewardDispatch("d2", f.mustSpace(sp.ID).Version); err != nil {
		t.Fatalf("d2: %v", err)
	}

	// 越过租约（1s）+ 扫查节拍（40ms）：自动暂停为可观测条件——条件探针等待
	//（截止 8s > 租约 1s + 扫查节拍 + 余量；不看钟数拍，等状态到达）。
	f.waitSpaceStatus(sp.ID, collab.StatusPaused, "租约", 8*time.Second)
	f.waitIntent(sp.ID, "d2", collab.IntentCancelled, 3*time.Second)
	if it, _ := f.intent(sp.ID, "d1"); it.Status != collab.IntentAccepted {
		t.Fatalf("已投递在途不受影响: %+v", it)
	}

	// 在途照常收敛（N04）：放行后 d1 轮终 → converged；暂停态不投新棒。
	f.releaseBarrier()
	f.waitIntent(sp.ID, "d1", collab.IntentConverged, 8*time.Second)
	if n := countLines(f.runLines(), "start"); n != 2 {
		t.Fatalf("暂停态不得投新棒: start=%d", n)
	}

	// 恢复走显式 resume：主人接管（manual，新任期）后 resume（无在途直通）。
	tk, _, err := f.st.Control(sp.ID, collab.ControlRequest{
		RequestID: "tk", Action: collab.ActTakeover, ExpectedVersion: f.mustSpace(sp.ID).Version,
		ExpectedTerm: term, TakeoverKind: collab.LeaderManual,
	}, spOwner)
	if err != nil {
		t.Fatalf("主人接管: %v", err)
	}
	if _, _, err := f.st.Control(sp.ID, collab.ControlRequest{
		RequestID: "rs", Action: collab.ActResume, ExpectedVersion: tk.Space.Version,
	}, spOwner); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := f.mustSpace(sp.ID); got.Status != collab.StatusRunning {
		t.Fatalf("resume 后应 running: %s", got.Status)
	}
}

// TestSpaceCancelStopsQueue 取消保护：成员轮取消 → 意图 cancelled + 系统暂停
// （不再派发下一棒）；显式 resume 放行排队意图继续。
func TestSpaceCancelStopsQueue(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	sid := f.createSession(f.cwd)
	sp := f.newManualSpace(sid)

	if _, _, err := f.dispatch(sp.ID, "c1", "被取消棒"); err != nil {
		t.Fatal(err)
	}
	f.waitIntent(sp.ID, "c1", collab.IntentAccepted, 5*time.Second)
	f.waitRunLine("start", 2)
	if _, _, err := f.dispatch(sp.ID, "c2", "排队棒"); err != nil {
		t.Fatal(err)
	}

	// 主人取消在途成员轮（sessionop 执行取消）。
	rc, err := f.ops.CancelTurn(nil2ctx(), sid, 2)
	if err != nil || rc.Outcome != sessionop.OutcomeExecuted {
		t.Fatalf("取消在途轮: rc=%+v err=%v", rc, err)
	}
	f.waitTurnFinished(sid, 2, protocol.ResultCancelled, 8*time.Second)
	f.waitIntent(sp.ID, "c1", collab.IntentCancelled, 5*time.Second)
	// 停队（条件探针）：系统暂停是收敛 hook 中 converge 之后的异步后果——等其可
	// 观测（不做固定时长假设），再在暂停态采样点做负断言。
	f.waitSpaceStatus(sp.ID, collab.StatusPaused, "取消", 5*time.Second)
	// 负断言（确定性采样点）：暂停成立后不存在任何可投递 c2 的路径（advance 对非
	// 运行态早退；c2 的同步 kick 在受理返回时已完成且因 c1 在途被串行门挡下）——
	// 立即采样即判定。
	if it, _ := f.intent(sp.ID, "c2"); it.Status != collab.IntentPending {
		t.Fatalf("取消后排队意图不得投递: %+v", it)
	}
	if n := countLines(f.runLines(), "start"); n != 2 {
		t.Fatalf("取消后不得 spawn 新棒: %v", f.runLines())
	}

	// 显式 resume（manual 主人即驱动者；直连模型层——confirm 门属 REST 层，
	// 在 localapi 测试覆盖）→ 队列放行 c2。
	if _, _, err := f.st.Control(sp.ID, collab.ControlRequest{
		RequestID: "rs2", Action: collab.ActResume, ExpectedVersion: f.mustSpace(sp.ID).Version, Term: 1,
	}, spOwner); err != nil {
		t.Fatalf("resume: %v", err)
	}
	f.waitIntent(sp.ID, "c2", collab.IntentAccepted, 5*time.Second)
	f.releaseBarrier()
	f.waitIntent(sp.ID, "c2", collab.IntentConverged, 8*time.Second)
}

// TestSpaceRestartReconcile 重启对账闭环：pending（未投递）取消收束不自动重跑；
// accepted（已投递但轮终不确定）留待核对；已收敛意图不动。两 agent 夹具：fast
// （快跑）与 slow（屏障阻塞续轮）分属两空间，构造「已收敛/在途/排队」三种重启实物。
func TestSpaceRestartReconcile(t *testing.T) {
	dir := t.TempDir()
	fake := buildSessionFakeCLI(t)
	barrier := filepath.Join(dir, "go.barrier")
	runLog := filepath.Join(dir, "run.log")
	touch := filepath.Join(dir, "touch.marker")
	ledDir := filepath.Join(dir, "ledger")
	kernDir := filepath.Join(dir, "kernel")
	opsDir := filepath.Join(dir, "ops")
	stDir := filepath.Join(dir, "collab")
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(dir, "art")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}

	mkSpecs := func(withTouch bool) map[string]sessionx.AgentSpec {
		env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
			"FAKE_THREAD_ID=thr-re", "FAKE_LOG=" + runLog}
		if withTouch {
			env = append(env, "FAKE_TOUCH="+touch)
		}
		slow := append(append([]string{}, env...), "FAKE_WAIT_FILE="+barrier, "FAKE_ON_RESUME=1")
		return map[string]sessionx.AgentSpec{
			"coder": {Adapter: protocol.SessionAdapterCodex, Command: fake, PermissionMode: "workspace-write", TimeoutSec: 30, Env: env},
			"slow":  {Adapter: protocol.SessionAdapterCodex, Command: fake, PermissionMode: "workspace-write", TimeoutSec: 60, Env: slow},
		}
	}

	// ---- 代 1：两会话/两空间/三笔意图（已收敛、在途、排队）。----
	led1, _, err := ledger.Open(ledDir)
	if err != nil {
		t.Fatal(err)
	}
	mgr1, err := sessionx.NewManager(kernDir, mkSpecs(false), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops1, err := sessionop.New(opsDir, led1, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st1, err := collab.Open(stDir, collab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bus1 := transport.NewBus()
	svc1 := New(Config{
		Ledger: led1, Runners: map[string]Runner{}, ArtifactRoots: map[string]string{"coder": art, "slow": art},
		Sessions: mgr1, SessionOps: ops1, Collab: st1, NodeID: "n1",
	}, bus1.Endpoint("n1"))
	f1 := &spaceSvcFixture{t: t, svc: svc1, mgr: mgr1, led: led1, ops: ops1, st: st1, dir: dir, cwd: ws, runLog: runLog}
	fastSID := f1.createSession(ws)
	slowAck, err := svc1.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", Agent: "slow", Cwd: ws, Prompt: "首轮"})
	if err != nil {
		t.Fatal(err)
	}
	f1.waitTurnFinished(slowAck.SessionID, 1, protocol.ResultCompleted, 8*time.Second)
	spA := f1.newManualSpace(fastSID) // 成员名 coder 绑 fastSID
	// 第二空间：成员绑定 slow 会话。
	spB, _, err := st1.CreateSpace(collab.CreateRequest{
		Goal: "gB", Mode: collab.ModeManual,
		Members: []collab.Member{{Name: "coder", SessionID: slowAck.SessionID}},
	}, spOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st1.Control(spB.ID, collab.ControlRequest{RequestID: "rb", Action: collab.ActReady, ExpectedVersion: spB.Version}, spOwner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st1.Control(spB.ID, collab.ControlRequest{RequestID: "sb", Action: collab.ActStart, ExpectedVersion: spB.Version + 1}, spOwner); err != nil {
		t.Fatal(err)
	}
	// done-1（fast 空间，收敛）。
	if _, _, err := f1.dispatch(spA.ID, "done-1", "已完成棒"); err != nil {
		t.Fatal(err)
	}
	f1.waitIntent(spA.ID, "done-1", collab.IntentConverged, 8*time.Second)
	// inflight-1（slow 空间，屏障阻塞在途）+ queued-1（排队）。
	spBv := func() int64 { s, _ := st1.Space(spB.ID); return s.Version }
	if _, _, err := f1.svc.SpaceDispatch(SpaceDispatchRequest{SpaceID: spB.ID, Member: "coder", RequestID: "inflight-1", ExpectedVersion: spBv(), Note: "在途棒"}, spOwner); err != nil {
		t.Fatal(err)
	}
	f1.waitIntent(spB.ID, "inflight-1", collab.IntentAccepted, 5*time.Second)
	// inflight-1 真实 spawn 的确定性信号：4 个 start = fast 首轮 + fast done-1 +
	// slow 首轮 + inflight-1（slow 续轮与 done-1 同 cwd，其 spawn 等 done-1 的
	// workdir 锁释放——异步于意图 accepted，条件探针等到真实 spawn 并阻塞屏障，
	// 绝不以受理态冒充 spawn 态）。
	f1.waitRunLine("start", 4)
	if _, _, err := f1.svc.SpaceDispatch(SpaceDispatchRequest{SpaceID: spB.ID, Member: "coder", RequestID: "queued-1", ExpectedVersion: spBv(), Note: "排队棒"}, spOwner); err != nil {
		t.Fatal(err)
	}
	// 排队纪律断言（确定性采样点，零固定等待）：SpaceDispatch 返回即同步 kick 已
	// 完成——若串行门失效（inflight-1 accepted 在途仍放行投递），接单登记必然已
	// 同步落店；此处立即读店即判定。重启后的对账断言（queued-1 → cancelled）对任
	// 何异步旁路闭环兜底。
	if it, _ := st1.IntentByRequest(spB.ID, "queued-1"); it.Status != collab.IntentPending {
		t.Fatalf("queued-1 应 pending（不得在 inflight-1 收敛前投递）: %+v", it)
	}

	// ---- 重启：全链 Close + 重开（同 dataDir）。----
	svc1.Close()
	_ = st1.Close()
	_ = led1.Close()
	led2, _, err := ledger.Open(ledDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	mgr2, err := sessionx.NewManager(kernDir, mkSpecs(true), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops2, err := sessionop.New(opsDir, led2, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := collab.Open(stDir, collab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	svc2 := New(Config{
		Ledger: led2, Runners: map[string]Runner{}, ArtifactRoots: map[string]string{"coder": art, "slow": art},
		Sessions: mgr2, SessionOps: ops2, Collab: st2, NodeID: "n1",
	}, transport.NewBus().Endpoint("n2"))
	t.Cleanup(svc2.Close)

	// 对账断言：queued-1 → cancelled（未投递不自动重跑）。
	deadline := time.Now().Add(5 * time.Second)
	var qit collab.DispatchIntent
	var ok bool
	for time.Now().Before(deadline) {
		if qit, ok = st2.IntentByRequest(spB.ID, "queued-1"); ok && qit.Status == collab.IntentCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok || qit.Status != collab.IntentCancelled {
		t.Fatalf("未投递意图应对账取消: %+v ok=%v", qit, ok)
	}
	if !strings.Contains(qit.Note, "不自动重跑") {
		t.Fatalf("取消注记应对账语义: %q", qit.Note)
	}
	// inflight-1：轮终不确定（Close 中断 → needs_review）→ 留待核对（保持 accepted）。
	e, okLed := findLedgerTurn(led2, slowAck.SessionID, 2)
	if !okLed || e.State != ledger.StateFinished || e.Result.Status != protocol.ResultNeedsReview {
		t.Fatalf("在途轮应被会话收敛为待核对终态: ok=%v %+v", okLed, e)
	}
	if it, _ := st2.IntentByRequest(spB.ID, "inflight-1"); it.Status != collab.IntentAccepted {
		t.Fatalf("不确定轮终的意图应留待核对（accepted）: %+v", it)
	}
	// done-1：已收敛不动。
	if it, _ := st2.IntentByRequest(spA.ID, "done-1"); it.Status != collab.IntentConverged {
		t.Fatalf("已收敛意图不得被对账改写: %+v", it)
	}
	// 绝不自动重跑：fake CLI 零新执行（touch 标记不存在、留痕无新 start、内核轮数不增）。
	if _, serr := os.Stat(touch); serr == nil {
		t.Fatal("重启后不得自动重跑（启动标记存在）")
	}
	sess, _ := mgr2.Status(slowAck.SessionID)
	if sess.TurnCount != 2 { // 首轮 + inflight-1（中断轮内核留痕）
		t.Fatalf("内核轮数不得虚增: %d", sess.TurnCount)
	}
	lines, _ := os.ReadFile(runLog)
	// 代 1 合法执行：两会话首轮 + done-1 + inflight-1 = 4 个 start。
	if n := strings.Count(string(lines), "start"); n != 4 {
		t.Fatalf("重启后不得重跑（start 留痕）: %d", n)
	}
	// 空间状态不因重启变更（HTTP 空闲不判死——重启不暂停不回拨租约）。
	if got, ok := st2.Space(spB.ID); !ok || got.Status != collab.StatusRunning {
		t.Fatalf("重启不得改空间状态: ok=%v status=%s", ok, got.Status)
	}
}

func nil2ctx() context.Context { return context.Background() }
