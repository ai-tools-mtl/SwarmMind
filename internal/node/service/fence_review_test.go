// fence_review_test.go 任务租约评审修复（commit fa54c8f codex 审查 7 条）的
// node 侧回归：P1-1 重臂写盘失败不确认、P1-2 在途取消回写对账取大者、
// P1-3 拒收 fence 收窄置 closed + 重臂解除 closed（交错+首发丢失）。
package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// cancelGateRunner 门控执行器：Run 阻塞至取消（ctx.Done，ExitCode=NotRun——对齐真实
// runner 被整组终止的形态）或显式放行；P1-2 在途取消窗口控制用。
type cancelGateRunner struct{ release chan struct{} }

func (g *cancelGateRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	select {
	case <-g.release:
		return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: protocol.NowUTC()}
	case <-ctx.Done():
		return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: protocol.ExitCodeNotRun, FinishedAt: protocol.NowUTC()}
	}
}

// newCancelGateFixture 单 agent 门控执行器夹具（echo-demo 门控 + echo-fast 即完）。
func newCancelGateFixture(t *testing.T) (*frameRec, *cancelGateRunner) {
	t.Helper()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	g := &cancelGateRunner{release: make(chan struct{})}
	return newFixtureWith(t, map[string]Runner{
		"echo-demo": g,
		"echo-fast": runner.New(echoProfile(), map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home")),
	}, led), g
}

// TestRearmPersistFailureSuppressesAck P1-1：重复 offer（重派收口）重臂落盘失败
// → 立即返回，不发 task_ack 也不重发 result——hub 已存新租约，旧租约回执必被
// 拒收，ack 又会让任务离开 offered 不再重派，任务将永久滞留；不 ack 则 hub 按
// offer-ack 超时重推本帧。恢复后同一重复 offer 再到：重臂成功 → ack + 按新租约
// 重发 result（收口闭合）。
func TestRearmPersistFailureSuppressesAckAndResend(t *testing.T) {
	var mu sync.Mutex
	failRearm := false
	led, _, err := ledger.OpenForTest(filepath.Join(t.TempDir(), "ledger"), func(op, stage string) error {
		if op == "RearmFence" {
			mu.Lock()
			defer mu.Unlock()
			if failRearm {
				return errors.New("注入重臂写盘失败")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixtureWith(t, map[string]Runner{
		"echo-demo": runner.New(echoProfile(), map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home")),
	}, led)

	f.sendOfferFenced(t, echoTask("t-rf-1"), 5)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-rf-1")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("任务应完成")
	}
	// task_result 由执行器 goroutine 在 MarkFinished 之后异步发出——账本可见
	// 不代表帧已抵达记录器，断言计数前先等帧到位（否则偶发 0 帧）
	if !waitFor(2*time.Second, func() bool { return len(f.resultFences()) >= 1 }) {
		t.Fatalf("前置：首次结果帧应发出, got %v", f.resultFences())
	}
	if n := len(f.ofTypes(protocol.MsgTaskAck)); n != 1 {
		t.Fatalf("前置：首次 offer 应恰一次 ack, got %d", n)
	}

	// 重臂写盘失败：重复 offer（新租约 7）不得产生 ack/result
	mu.Lock()
	failRearm = true
	mu.Unlock()
	f.sendOfferFenced(t, echoTask("t-rf-1"), 7)
	if n := len(f.ofTypes(protocol.MsgTaskAck)); n != 1 {
		t.Fatalf("重臂失败后不得发 ack, got %d 次", n)
	}
	if n := len(f.resultFences()); n != 1 {
		t.Fatalf("重臂失败后不得重发 result, got %d 次", n)
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-rf-1"); e.FenceEpoch != 5 || e.ResultAcked {
		t.Fatalf("账本应保持旧租约 5 且未确认, got fence=%d acked=%v", e.FenceEpoch, e.ResultAcked)
	}

	// 恢复：hub 重推同一重复 offer → 重臂成功 → ack + 新租约回执
	mu.Lock()
	failRearm = false
	mu.Unlock()
	f.sendOfferFenced(t, echoTask("t-rf-1"), 7)
	if !waitFor(2*time.Second, func() bool {
		return len(f.ofTypes(protocol.MsgTaskAck)) >= 2 && len(f.resultFences()) >= 2
	}) {
		t.Fatal("恢复后重复 offer 应重发 ack 与 result")
	}
	if got := f.resultFences(); got[len(got)-1] != 7 {
		t.Fatalf("重发回执应携带新租约 7, got %d", got[len(got)-1])
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-rf-1"); e.FenceEpoch != 7 {
		t.Fatalf("账本条目应重臂为 7, got %d", e.FenceEpoch)
	}
}

// TestInFlightCancelWriteBackTakesMaxFence P1-2 交错（在途）：node 持旧租约 2
// 执行中，hub 重派推进租约至 7 后管理面取消（cancel_fence=7）——node 取消回写
// 前对「账本租约 2 vs cancel_fence 7」取大者重臂，回执按 7 回带（hub 宽校验
// cancel_fence ≥ 落库租约接受），取消结果不再被等值门禁拒收置 closed。
func TestInFlightCancelWriteBackTakesMaxFence(t *testing.T) {
	f, _ := newCancelGateFixture(t)

	f.sendOfferFenced(t, echoTask("t-cf-1"), 2)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-cf-1")
		return ok && e.State == ledger.StateStarted
	}) {
		t.Fatal("前置：任务应在途（started）")
	}

	f.sendTaskCancel(t, "t-cf-1", 7) // 重派推进后的取消（cancel_fence=7 > 条目 2）
	// 对账重臂是 MarkFinished 之后的一次独立事务：等待条件须含 fence==7，
	// 否则可能观察到「已落终态、尚未重臂发布」的中间态
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-cf-1")
		return ok && e.State == ledger.StateFinished && e.Result != nil &&
			e.Result.Status == protocol.ResultCancelled && e.FenceEpoch == 7
	}) {
		t.Fatal("取消应完成在途收尾并对账重臂为 7")
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-cf-1"); e.FenceEpoch != 7 {
		t.Fatalf("取消回写应对账重臂为 7, got %d", e.FenceEpoch)
	}
	// 回执帧由执行器 goroutine 异步发出且跨任务帧序无串行化保证——按任务过滤
	// 并等帧到位断言（全局 resultFences() 的末帧在多任务并存时属于谁不确定）
	if !waitFor(2*time.Second, func() bool {
		g := f.resultFencesFor("t-cf-1")
		return len(g) > 0 && g[len(g)-1] == 7
	}) {
		t.Fatalf("取消回执应按对账后租约 7 回带, got %v", f.resultFencesFor("t-cf-1"))
	}

	// 旧版 hub（取消帧不携租约，fence=0）：不重臂，回执按条目租约回带
	f.sendOfferFenced(t, echoTask("t-cf-2"), 3)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-cf-2")
		return ok && e.State == ledger.StateStarted
	}) {
		t.Fatal("前置：第二任务应在途")
	}
	f.sendTaskCancel(t, "t-cf-2", 0)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-cf-2")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("第二任务应取消收尾")
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-cf-2"); e.FenceEpoch != 3 {
		t.Fatalf("fence=0 取消不得改写租约, got %d", e.FenceEpoch)
	}
	if !waitFor(2*time.Second, func() bool {
		g := f.resultFencesFor("t-cf-2")
		return len(g) > 0 && g[len(g)-1] == 3
	}) {
		t.Fatalf("回执应按条目租约 3 回带, got %v", f.resultFencesFor("t-cf-2"))
	}
}

// resultFencesFor 按 task_id 过滤的回带租约序（多任务并存时逐任务断言用）。
func (f *frameRec) resultFencesFor(taskID string) []int64 {
	var out []int64
	for _, e := range f.ofTypes(protocol.MsgTaskResult) {
		var p protocol.TaskResultPayload
		if jsonUnmarshal(e.Payload, &p) == nil && p.TaskID == taskID {
			out = append(out, e.FenceEpoch)
		}
	}
	return out
}

// TestRejectedFenceGatesCloseAndRearmReopens P1-3 交错+首发丢失：
//   - 被拒 fence == 条目 fence（A）→ closed 停止重发；随后重派收口重臂换新
//     代次 → closed 解除、按新租约重发（新租约结果的首发即此重发，不再被
//     「closed 跳过」卡死）；
//   - 被拒 fence ≠ 条目 fence（B，旧代次拒收）→ 不关闭，重发保留；
//   - err 未携 fence（C，旧版对端/普通终态冲突）→ 按既有规则无条件关闭。
func TestRejectedFenceGatesCloseAndRearmReopens(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})

	// A：等值拒收 → closed → 重臂解除并重发
	f.sendOfferFenced(t, echoTask("t-rj-a"), 2)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-rj-a")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("A 应完成")
	}
	f.sendConflictErr(t, "t-rj-a", 2, 3) // hub 拒收当前代次 2
	if e, _ := f.svc.cfg.Ledger.Get("t-rj-a"); !e.Closed {
		t.Fatal("A：被拒 fence 等于条目 fence 应置 closed")
	}
	n := len(f.resultFencesFor("t-rj-a"))
	f.svc.ResendPending() // closed 退出重发队列
	if got := len(f.resultFencesFor("t-rj-a")); got != n {
		t.Fatalf("A：closed 后不得重发, got %d → %d", n, got)
	}
	f.sendOfferFenced(t, echoTask("t-rj-a"), 3) // 重派收口（新租约 3）
	if !waitFor(2*time.Second, func() bool { return len(f.resultFencesFor("t-rj-a")) >= n+1 }) {
		t.Fatal("A：重臂后应恢复重发")
	}
	if got := f.resultFencesFor("t-rj-a"); got[len(got)-1] != 3 {
		t.Fatalf("A：重发应携带新租约 3, got %d", got[len(got)-1])
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-rj-a"); e.Closed || e.FenceEpoch != 3 {
		t.Fatalf("A：重臂应解除 closed 且租约为 3, got closed=%v fence=%d", e.Closed, e.FenceEpoch)
	}

	// B：非等值拒收（旧代次）→ 不关闭
	f.sendOfferFenced(t, echoTask("t-rj-b"), 2)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-rj-b")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("B 应完成")
	}
	f.sendConflictErr(t, "t-rj-b", 9, 3) // 拒收针对 fence 9 ≠ 条目 2
	if e, _ := f.svc.cfg.Ledger.Get("t-rj-b"); e.Closed {
		t.Fatal("B：拒收旧代次不得关闭当前条目")
	}
	nB := len(f.resultFencesFor("t-rj-b"))
	f.svc.ResendPending()
	if got := len(f.resultFencesFor("t-rj-b")); got != nB+1 {
		t.Fatalf("B：未关闭条目应继续重发, got %d → %d", nB, got)
	}

	// C：err 无结构化 fence（旧版对端）→ 无条件关闭（既有行为不回归）
	f.sendOfferFenced(t, echoTask("t-rj-c"), 2)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-rj-c")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("C 应完成")
	}
	f.sendConflictErr(t, "t-rj-c", 0, 0)
	if e, _ := f.svc.cfg.Ledger.Get("t-rj-c"); !e.Closed {
		t.Fatal("C：旧版 err 帧应无条件置 closed")
	}
}
