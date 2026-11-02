package service

// w07_spacegate_test.go 空间来源轮收件门测试（W07-01，docs/tasks/v3/W07-hub-routing.md
// 「node 能力卡声明不支持空间特性的旧版本须明确拒绝，普通任务链路零影响」）：
//   - 未装配 collab（旧版 node / spaces 未启用）+ 携 Space 轮 → spaces_unsupported
//     明确失败，绝不静默按普通会话轮执行（不 spawn CLI）；
//   - 词形未知 scope / 与会话空间绑定不符（中心 hub:<id> 未在本机绑定）→
//     fail-closed 拒绝（W07-02 远端派发 adapter 接线前，本批只交拒绝面）；
//   - 本机空间派发轮自带 scope（submitSpaceIntent 盖章）：受理门绑定核对通过，
//     派发→收敛全链不受影响（正向锚）；
//   - 普通会话轮（无 Space）零变化（既有回归 TestSpaceDispatchRequired… 与本
//     文件正向锚共同覆盖）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newSpaceGateSvcNoCollab 无 collab 装配的服务夹具（旧版 node 词形：会话内核在、
// 空间协作存储不在）。模型取自 newSpaceSvcFixture，去 collab 段。
func newSpaceGateSvcNoCollab(t *testing.T) (*Service, *ledger.Ledger, *sessionx.Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	cwd := filepath.Join(dir, "ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := buildSessionFakeCLI(t)
	specs := map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: fake,
		PermissionMode: "workspace-write", TimeoutSec: 30,
		Env: []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex, "FAKE_THREAD_ID=thr-gate-1"},
	}}
	mgr, err := sessionx.NewManager(filepath.Join(dir, "kernel"), specs, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	ops, err := sessionop.New(filepath.Join(dir, "ops"), led, sessionop.Options{})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	bus := transport.NewBus()
	svc := New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{},
		ArtifactRoots: map[string]string{"coder": filepath.Join(dir, "artifacts")},
		Sessions:      mgr,
		SessionOps:    ops,
		NodeID:        "n1",
		// Collab 有意缺位：旧版 node 词形
	}, bus.Endpoint("n1"))
	t.Cleanup(func() { svc.Close() })
	return svc, led, mgr, cwd, dir
}

// execSpaceTurn 直投一笔携 Space 的会话轮（hub 下发词形；入口为 execute——
// 收件门在 runSessionTurnExec，与真实任务链同口）。
func execSpaceTurn(t *testing.T, svc *Service, led *ledger.Ledger, sid, space, op string) {
	t.Helper()
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "空间来源轮"}, SubmittedBy: "hub",
		IdemKey: "spgate:" + protocol.NewMessageID(), Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 2, Op: op, Cwd: "", Space: space},
	}
	if err := led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatal(err)
	}
	svc.execute(task)
}

// waitGateFailure 等任务落终态并断言失败码前缀与人话原因片段（有界轮询，非
// fixed sleep；msgSub 非空时逐字锚定——修复轮建议2：词形非法与绑定不符两类
// 拒绝原因 msg 必须分开且各自断言，防同码同断言互冒）。
func waitGateFailure(t *testing.T, led *ledger.Ledger, taskID, codePrefix, msgSub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := led.Get(taskID); ok && e.State == ledger.StateFinished {
			if e.Result == nil || !strings.HasPrefix(e.Result.Error, codePrefix) {
				t.Fatalf("应失败于 %s: %+v", codePrefix, e.Result)
			}
			if msgSub != "" && !strings.Contains(e.Result.Error, msgSub) {
				t.Fatalf("拒绝原因应含 %q: %s", msgSub, e.Result.Error)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未落终态", taskID)
}

// TestW07SpaceTurnRejectedWithoutCollab 旧版 node 词形（无 collab）：携 Space 轮
// 明确拒绝（spaces_unsupported + 升级指路），不得静默当普通会话轮执行。
func TestW07SpaceTurnRejectedWithoutCollab(t *testing.T) {
	svc, led, mgr, cwd, _ := newSpaceGateSvcNoCollab(t)
	// 建一个真实会话（首轮走内核，模拟 hub 会话成员线程）。
	sess, _, err := mgr.CreateWithID(t.Context(), protocol.NewSessionID(), "coder", cwd, "首轮")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	taskID := trackGateTask(t, svc, led, sess.ID, collab.HubScope("sp-center-1"))
	waitGateFailure(t, led, taskID, protocol.CodeSpacesUnsupported, "未装配空间协作存储")
	// 会话零触碰：内核快照轮数不变（未当普通 send 执行）。
	if s, _ := mgr.Status(sess.ID); s.TurnCount != 1 {
		t.Fatalf("被拒轮不得触碰会话: turn_count=%d", s.TurnCount)
	}
}

// trackGateTask 同 execSpaceTurn 但返回任务 ID（供失败断言定位）；两助手合一会
// 混淆返回值语义，故分开。
func trackGateTask(t *testing.T, svc *Service, led *ledger.Ledger, sid, space string) string {
	t.Helper()
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "空间来源轮"}, SubmittedBy: "hub",
		IdemKey: "spgate:" + protocol.NewMessageID(), Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 2, Op: protocol.SessionTurnOpSend, Space: space},
	}
	if err := led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatal(err)
	}
	svc.execute(task)
	return task.ID
}

// TestW07SpaceTurnGateWordformAndBinding 已装配 collab：词形未知 / 绑定不符
// fail-closed 拒绝（两类拒绝原因 msg 分开锚定——修复轮建议2）；绑定一致的
// scope（本机空间 ID）受理门核对通过。
func TestW07SpaceTurnGateWordformAndBinding(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	sid := f.createSession(f.cwd)
	sp := f.newManualSpace(sid)

	// 词形未知：任意字符串冒充 scope 一律拒绝（人话=词形非法，与绑定不符互斥）。
	taskID := trackGateTask(t, f.svc, f.led, sid, "not-a-scope")
	waitGateFailure(t, f.led, taskID, protocol.CodeSpacesUnsupported, "词形非法")
	// 中心 scope 未在本机绑定（hub:<id> 词形合法但无绑定）→ 绑定不符拒绝
	//（W07-02 接线前 fail-closed）。
	taskID = trackGateTask(t, f.svc, f.led, sid, collab.HubScope("sp-center-1"))
	waitGateFailure(t, f.led, taskID, protocol.CodeSpacesUnsupported, "绑定不符")
	// 绑定到别的空间：真实第二空间（第二会话绑定，非伪造 ID）——会话绑在 sp、
	// 轮冒充 sp2 来源 → 绑定不符拒绝（人话带被冒充 scope 与实际绑定）。
	sid2 := f.createSession(f.cwd2)
	sp2 := f.newManualSpace(sid2)
	if sp2.ID == sp.ID {
		t.Fatal("第二空间应独立建空间")
	}
	taskID = trackGateTask(t, f.svc, f.led, sid, sp2.ID)
	waitGateFailure(t, f.led, taskID, protocol.CodeSpacesUnsupported, "绑定不符")

	// 正向锚：本机空间派发（意图轮自带 Space=空间 ID）受理→收敛全链照常——
	// 收件门与既有意图核对（spaceTurnAllowed）叠加不互斥。
	if _, _, err := f.dispatch(sp.ID, "d-w07", "空间来源轮正向锚"); err != nil {
		t.Fatalf("派发: %v", err)
	}
	it := f.waitIntent(sp.ID, "d-w07", collab.IntentConverged, 8*time.Second)
	if it.Status != collab.IntentConverged {
		t.Fatalf("意图应收敛: %+v", it)
	}
}

// TestW07SpaceTurnOrdinaryUnaffected 普通会话轮（Session 无 Space）零变化回归锚：
// 无 collab 服务上普通续轮照常执行（普通任务链路零牵连）。
func TestW07SpaceTurnOrdinaryUnaffected(t *testing.T) {
	svc, led, mgr, cwd, _ := newSpaceGateSvcNoCollab(t)
	sess, _, err := mgr.CreateWithID(t.Context(), protocol.NewSessionID(), "coder", cwd, "首轮")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "普通续轮"}, SubmittedBy: "hub",
		IdemKey: "spgate:" + protocol.NewMessageID(), Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sess.ID, TurnSeq: 2, Op: protocol.SessionTurnOpSend},
	}
	if err := led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatal(err)
	}
	svc.execute(task)
	// 轮成功落 completed（内核执行），Space 缺省零变化。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := led.Get(task.ID); ok && e.State == ledger.StateFinished {
			if e.Result == nil || e.Result.Status != protocol.ResultCompleted {
				t.Fatalf("普通会话轮应照常执行: %+v", e.Result)
			}
			if s, _ := mgr.Status(sess.ID); s.TurnCount != 2 {
				t.Fatalf("普通轮应真实执行: turn_count=%d", s.TurnCount)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("普通会话轮未落终态")
}
