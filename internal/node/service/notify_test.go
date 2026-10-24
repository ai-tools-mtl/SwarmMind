// 终态通知挂钩测试（standalone 补盲区，2026-10-24）：通知只在 MarkFinished 成功
// （首次落终态）后触发一次；nil Notifier（networked/setup 装配口径）零调用且
// 不影响任务收尾。触发点覆盖：正常完成（本地直执行）/ 过期（offer 路径
// ExpireAt 拦截）/ 管理面取消（在途触发）——三条 MarkFinished 路径各一测
//（finishCancelledBeforeRun/finishCancelledOffer 与取消同收敛表，由 cancel
// 既有测试族保障账本语义）。2026-10-24 返工补：恢复终态通知（不经 MarkFinished
// 的旁路）、并发完成扇出、关闭打断在途执行、typed-nil dispatcher 兜底。
package service

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/notify"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// notifyRec 记录型通知桩（TerminalNotifier 最小实现）。
type notifyRec struct {
	mu    sync.Mutex
	tasks []protocol.Task
}

func (r *notifyRec) TaskFinal(t protocol.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, t)
}

func (r *notifyRec) snapshot() []protocol.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Task(nil), r.tasks...)
}

// newNotifyFixture 带通知桩的夹具（membus 黑洞汇，假 hub 端点无人订阅）：双执行
// 器——echo-demo 真实 echo（本地直执行完成路径）、hold 取消门控桩（在途取消路径，
// ctx.Done 时 ExitCode=NotRun 对齐真实 runner 被整组终止的形态）。
func newNotifyFixture(t *testing.T, notifier TerminalNotifier) (*frameRec, *ledger.Ledger, *cancelGateRunner) {
	t.Helper()
	echo := protocol.ExecProfile{
		Name: "echo-demo", Kind: protocol.AgentKindCLI,
		Command: "/bin/echo", Workdir: "/tmp",
		TimeoutSec: 5, AllowArgs: true, Exposed: true,
		PermissionMode: "no-shell",
	}
	hold := &cancelGateRunner{release: make(chan struct{})}
	runners := map[string]Runner{
		"echo-demo": runner.New(echo, map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home")),
		"hold":      hold,
	}
	led, _, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	bus := transport.NewBus()
	_ = bus.Endpoint("hub") // 假 hub 端点（无人订阅：node 出站帧自然丢弃）
	f := &frameRec{ep: bus.Endpoint("hub-send")}
	f.svc = New(Config{Ledger: led, Runners: runners, Notifier: notifier}, bus.Endpoint("n1"))
	t.Cleanup(f.svc.Close)
	return f, led, hold
}

func submitEchoDirect(t *testing.T, svc *Service, idem string) string {
	t.Helper()
	ack, err := svc.SubmitLocalDirect(protocol.TaskSubmitRequest{
		TargetNode: "n1", TargetAgent: "echo-demo", SubmittedBy: "master",
		Input: protocol.TaskInput{Args: []string{"hello"}}, IdemKey: idem,
	})
	if err != nil {
		t.Fatalf("本地提交失败: %v", err)
	}
	return ack.TaskID
}

// waitStarted 轮询账本至 started（执行异步）。
func waitStarted(t *testing.T, led *ledger.Ledger, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e, ok := led.Get(taskID)
		if ok && e.State == ledger.StateStarted {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务未在期限内 started: %+v", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNotifierFiresOnceOnLocalCompletion standalone 正常完成：通知恰一次，视图
// 携终态 Status/Result（TaskView 口径——Status=completed、结果字段可见）。
func TestNotifierFiresOnceOnLocalCompletion(t *testing.T) {
	rec := &notifyRec{}
	f, led, _ := newNotifyFixture(t, rec)
	svc := f.svc
	id := submitEchoDirect(t, svc, "n-1")
	waitTerminal(t, led, id)
	svc.Close() // 等执行 goroutine 收尾（TaskFinal 在落终态后同步调用，此处双保险）

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("通知应恰一次，got %d", len(got))
	}
	if got[0].ID != id {
		t.Fatalf("通知任务号不符: got %s want %s", got[0].ID, id)
	}
	if got[0].Status != protocol.StatusCompleted {
		t.Fatalf("通知状态应为 completed，got %s", got[0].Status)
	}
	if got[0].Result == nil || got[0].Result.ExitCode != 0 {
		t.Fatalf("通知应携带终态结果（exit=0），got %+v", got[0].Result)
	}
	if got[0].TargetAgent != "echo-demo" || got[0].SubmittedBy != "master" {
		t.Fatalf("通知任务视图字段缺失: agent=%q submitted_by=%q", got[0].TargetAgent, got[0].SubmittedBy)
	}
}

// TestNotifierNilNetworkedNoTrigger networked/setup 装配口径（Notifier=nil）：
// 任务照常收尾；notifyTerminal nil 早退零副作用（白盒直调验证 nil 安全）。
func TestNotifierNilNetworkedNoTrigger(t *testing.T) {
	f, led, _ := newNotifyFixture(t, nil)
	svc := f.svc
	if svc.cfg.Notifier != nil {
		t.Fatal("夹具应装配 nil Notifier（networked 口径）")
	}
	id := submitEchoDirect(t, svc, "n-2")
	waitTerminal(t, led, id)
	svc.notifyTerminal(id) // nil 钩子直调：不得 panic、不得触碰账本以外状态
	svc.Close()
}

// TestNotifierFiresOnExpiredTask 过期路径：已过 ExpireAt 的任务经 offer 入账，
// execute 在 spawn 前拦截置 failed/expired 终态——同样触发一次通知。
func TestNotifierFiresOnExpiredTask(t *testing.T) {
	rec := &notifyRec{}
	f, led, _ := newNotifyFixture(t, rec)
	task := echoTask("t-expired")
	task.ExpireAt = protocol.NowUTC().Add(-time.Minute) // 已过期
	f.sendOffer(t, task)
	waitTerminal(t, led, task.ID)
	f.svc.Close()

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("过期终态应通知恰一次，got %d", len(got))
	}
	if got[0].Status != protocol.StatusFailed {
		t.Fatalf("过期通知状态应为 failed，got %s", got[0].Status)
	}
	if got[0].Result == nil || got[0].Result.Error != protocol.CodeExpired {
		t.Fatalf("过期通知应带 error=expired，got %+v", got[0].Result)
	}
}

// TestNotifierFiresOnCancel 管理面取消路径（在途触发 → cancelledResult 落终态）：
// cancelled 终态触发一次通知。hold 桩阻塞至取消（先投递后触发，无竞态）。
func TestNotifierFiresOnCancel(t *testing.T) {
	rec := &notifyRec{}
	f, led, _ := newNotifyFixture(t, rec)
	task := echoTask("t-cancel")
	task.TargetAgent = "hold"
	f.sendOffer(t, task)
	waitStarted(t, led, "t-cancel")

	ce, ok := f.svc.lookupCancel("t-cancel")
	if !ok {
		t.Fatal("在途取消开关未登记")
	}
	ce.trigger() // 管理面取消语义：执行中断 → cancelled/user_cancelled 落终态
	waitTerminal(t, led, "t-cancel")
	f.svc.Close()

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("取消终态应通知恰一次，got %d", len(got))
	}
	if got[0].Status != protocol.StatusCancelled {
		t.Fatalf("通知状态应为 cancelled，got %s", got[0].Status)
	}
}

// TestNotifierFiresOnRecoveredTerminal 恢复终态通知（返工必修2）：启动恢复降级
// 产生的终态（started 崩溃重启转 needs_review、离线期间过期转 failed/expired）
// 不经 MarkFinished，NotifyRecoveredTerminal 补发——每条恰一次，状态与原因如实。
// 重启不再降级的去重锚点由 ledger 侧测试锁定（TestLedgerRecoveryDowngradedListing）。
func TestNotifierFiresOnRecoveredTerminal(t *testing.T) {
	dir := t.TempDir()
	led1, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mid := echoTask("t-rec-mid")
	if err := led1.AcceptOrigin(mid, ledger.OriginLocal); err != nil {
		t.Fatalf("seed accepted: %v", err)
	}
	if err := led1.MarkStarted(mid.ID); err != nil {
		t.Fatalf("seed started: %v", err)
	}
	exp := echoTask("t-rec-exp")
	exp.ExpireAt = protocol.NowUTC().Add(-time.Minute)
	if err := led1.AcceptOrigin(exp, ledger.OriginHub); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	if err := led1.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启：恢复降级直接落终态，Downgraded 清单随 Open 出。
	led2, rcv, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	if len(rcv.Downgraded) != 2 {
		t.Fatalf("恢复应降级 2 条（needs_review + expired），got %+v", rcv.Downgraded)
	}

	notified := &notifyRec{}
	bus := transport.NewBus()
	_ = bus.Endpoint("hub")
	svc := New(Config{Ledger: led2, Notifier: notified}, bus.Endpoint("n1"))
	t.Cleanup(svc.Close)
	svc.NotifyRecoveredTerminal(rcv.Downgraded)

	got := notified.snapshot()
	if len(got) != 2 {
		t.Fatalf("恢复终态应通知恰 2 次，got %d", len(got))
	}
	byID := map[string]protocol.Task{}
	for _, tk := range got {
		byID[tk.ID] = tk
	}
	m := byID["t-rec-mid"]
	if m.ID == "" || m.Status != protocol.StatusNeedsReview ||
		m.Result == nil || m.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("t-rec-mid 通知不符: %+v", m)
	}
	e := byID["t-rec-exp"]
	if e.ID == "" || e.Status != protocol.StatusFailed ||
		e.Result == nil || e.Result.Error != protocol.CodeExpired {
		t.Fatalf("t-rec-exp 通知不符: %+v", e)
	}
}

// TestNotifierFanOutConcurrentCompletions 并发完成扇出（返工建议5）：并发限制下
// 多任务陆续完成，每任务通知恰一次——notifyTerminal 挂在 MarkFinished 首次成功
// 点，并发收尾不产生重复或丢失。
func TestNotifierFanOutConcurrentCompletions(t *testing.T) {
	rec := &notifyRec{}
	f, led, _ := newNotifyFixture(t, rec)
	const n = 6
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, submitEchoDirect(t, f.svc, fmt.Sprintf("n-fan-%d", i)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		done := 0
		for _, id := range ids {
			if e, ok := led.Get(id); ok && e.State == ledger.StateFinished {
				done++
			}
		}
		if done == n || time.Now().After(deadline) {
			if done != n {
				t.Fatalf("并发任务未全部终态（%d/%d）", done, n)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.svc.Close() // 等执行 goroutine 收尾（通知在落终态后同步调用）

	got := rec.snapshot()
	if len(got) != n {
		t.Fatalf("并发完成应通知恰 %d 次，got %d", n, len(got))
	}
	seen := map[string]bool{}
	for _, tk := range got {
		if seen[tk.ID] {
			t.Fatalf("任务 %s 重复通知", tk.ID)
		}
		seen[tk.ID] = true
		if tk.Status != protocol.StatusCompleted {
			t.Errorf("任务 %s 应为 completed，got %s", tk.ID, tk.Status)
		}
	}
}

// TestNotifierFiresOnCloseInterrupt 关闭打断在途执行（返工建议5）：Close 取消
// 执行 ctx → interruptedResult 以 needs_review/node_restarted_midway 落终态 →
// 通知恰一次。恢复与关闭产生的 needs_review 恰是最需要人工介入的场景。
func TestNotifierFiresOnCloseInterrupt(t *testing.T) {
	rec := &notifyRec{}
	f, led, _ := newNotifyFixture(t, rec)
	task := echoTask("t-close")
	task.TargetAgent = "hold"
	f.sendOffer(t, task)
	waitStarted(t, led, "t-close")
	f.svc.Close() // 关闭即取消在途执行；wg 等待收尾（含通知同步调用）

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("关闭打断终态应通知恰一次，got %d", len(got))
	}
	if got[0].ID != "t-close" || got[0].Status != protocol.StatusNeedsReview {
		t.Fatalf("通知应为 needs_review 终态: %+v", got[0])
	}
	if got[0].Result == nil || got[0].Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("通知应带 error=node_restarted_midway，got %+v", got[0].Result)
	}
}

// TestNotifierTypedNilDispatcherSafe 实际装配曾出现的 typed-nil 形态兜底（返工
// 必修1/5 回归）：nil *notify.Dispatcher 装进接口后 Notifier != nil，notifyTerminal
// 会真走到 TaskFinal——Dispatcher 自身 nil 判空挡住发送，任务照常收尾、不 panic。
// 装配方已收口杜绝该形态（cmd/node notify_test 锁定门控）；此处锁定 service 在
// 该形态下的防御安全。
func TestNotifierTypedNilDispatcherSafe(t *testing.T) {
	f, led, _ := newNotifyFixture(t, (*notify.Dispatcher)(nil))
	id := submitEchoDirect(t, f.svc, "n-typed-nil")
	waitTerminal(t, led, id)
	f.svc.Close() // 不 panic 即通过（nil dispatcher 无通知可断言）
}
