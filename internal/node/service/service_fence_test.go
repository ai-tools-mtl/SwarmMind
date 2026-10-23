package service

import (
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sendOfferFenced 假 hub 投递携租约的 task_offer（P4/SPEC 4.11）。
func (f *frameRec) sendOfferFenced(t *testing.T, task protocol.Task, fence int64) {
	t.Helper()
	if err := f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", FenceEpoch: fence,
		Payload: mustJSON(task),
	}); err != nil {
		t.Fatalf("发送 offer: %v", err)
	}
}

// sendTaskCancel 假 hub 发送 task_cancel 帧（信封携 hub 当前任务落库租约
// cancel_fence，P1-2；node 取消回写对账取大者后回带）。
func (f *frameRec) sendTaskCancel(t *testing.T, taskID string, fence int64) {
	t.Helper()
	if err := f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskCancel,
		MessageID: protocol.NewMessageID(), From: "hub", FenceEpoch: fence,
		Payload: mustJSON(protocol.TaskCancelPayload{TaskID: taskID, Reason: "test"}),
	}); err != nil {
		t.Fatalf("发送 task_cancel: %v", err)
	}
}

// sendConflictErr 假 hub 回 result_conflict err 帧（P1-3：结构化携带被拒 fence）。
func (f *frameRec) sendConflictErr(t *testing.T, taskID string, rejectedFence, leaseEpoch int64) {
	t.Helper()
	if err := f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgErr,
		MessageID: protocol.NewMessageID(), From: "hub",
		Payload: mustJSON(protocol.TaskRef{TaskID: taskID}),
		Err: &protocol.ErrInfo{
			Code:       protocol.CodeResultConflict,
			Msg:        "zombie_result_rejected: 任务租约过期",
			FenceEpoch: rejectedFence,
			LeaseEpoch: leaseEpoch,
		},
	}); err != nil {
		t.Fatalf("发送 err: %v", err)
	}
}

// resultFences 按到达序返回全部 task_result 帧的回带租约值。
func (f *frameRec) resultFences() []int64 {
	frames := f.ofTypes(protocol.MsgTaskResult)
	out := make([]int64, 0, len(frames))
	for _, e := range frames {
		out = append(out, e.FenceEpoch)
	}
	return out
}

// TestResultEchoesOfferFence 回带契约：task_result 的 fence_epoch 原样回带
// offer 携带的派发租约；未携租约的 offer（旧版 hub）回带缺省 0。
func TestResultEchoesOfferFence(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})

	f.sendOfferFenced(t, echoTask("t-fence-1"), 5) // 新版 hub：携租约
	f.sendOffer(t, echoTask("t-fence-2"))          // 旧版 hub：无租约字段

	if !waitFor(2*time.Second, func() bool {
		e1, ok1 := f.svc.cfg.Ledger.Get("t-fence-1")
		e2, ok2 := f.svc.cfg.Ledger.Get("t-fence-2")
		return ok1 && ok2 && e1.State == ledger.StateFinished && e2.State == ledger.StateFinished
	}) {
		t.Fatal("两任务应完成")
	}
	got := f.resultFences()
	if len(got) != 2 {
		t.Fatalf("应各回一次 result, got %v", got)
	}
	// 回带值与派发租约一一对应：5（携租约）与 0（缺省），到达序不敏感
	seen := map[int64]int{5: 0, 0: 0}
	for _, fence := range got {
		seen[fence]++
	}
	if seen[5] != 1 || seen[0] != 1 {
		t.Fatalf("回带应为 {5, 0} 各一次, got %v", got)
	}
}

// TestDuplicateOfferRearmsAndResends 重派收口（重复 offer 换新租约）：node 重臂
// 账本条目并重发结果，回执携带新租约——hub 重派后的等值仲裁由此通过。
func TestDuplicateOfferRearmsAndResends(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})

	f.sendOfferFenced(t, echoTask("t-fence-1"), 5)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-fence-1")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("任务应完成")
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-fence-1"); e.ResultAcked {
		t.Fatal("前置：假 hub 不回 result_ack，结果应停留在未确认")
	}

	// hub 重派（offer 超时回退后补推）：同 task_id、新租约
	f.sendOfferFenced(t, echoTask("t-fence-1"), 7)
	if !waitFor(2*time.Second, func() bool { return len(f.resultFences()) >= 2 }) {
		t.Fatal("重复 offer 应触发结果重发")
	}
	got := f.resultFences()
	if last := got[len(got)-1]; last != 7 {
		t.Fatalf("重发回执应携带新租约 7, got %d", last)
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-fence-1"); e.FenceEpoch != 7 {
		t.Fatalf("账本条目应重臂为 7, got %d", e.FenceEpoch)
	}
}

// TestCancelWriteBackReconcilesFence 取消收口（P1-2）：已终态未确认条目收到
// 取消帧（携 hub 当前任务落库租约 cancel_fence）→ 对账取大者重臂后重发回执，
// 回执按对账后租约回带——hub 宽校验（cancel_fence ≥ 落库租约）接受，覆盖
// 「重派推进租约而 node 未及重臂」的滞后；cancel_fence 不高于条目租约时不动。
func TestCancelWriteBackReconcilesFence(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})

	f.sendOfferFenced(t, echoTask("t-fence-1"), 3)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-fence-1")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("任务应完成")
	}

	f.sendTaskCancel(t, "t-fence-1", 9) // cancel_fence=9 > 条目租约 3（重派推进后取消）
	if !waitFor(2*time.Second, func() bool { return len(f.resultFences()) >= 2 }) {
		t.Fatal("取消命中未确认条目应触发回执重发")
	}
	got := f.resultFences()
	if last := got[len(got)-1]; last != 9 {
		t.Fatalf("取消回执应对账重臂为 cancel_fence 9 回带, got %d", last)
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-fence-1"); e.FenceEpoch != 9 {
		t.Fatalf("账本条目应对账为 9, got %d", e.FenceEpoch)
	}

	// cancel_fence ≤ 条目租约（无滞后）：不动租约，回执照发（幂等重发）
	n := len(f.resultFences())
	f.sendTaskCancel(t, "t-fence-1", 5)
	if !waitFor(2*time.Second, func() bool { return len(f.resultFences()) >= n+1 }) {
		t.Fatal("重复取消应幂等重发回执")
	}
	if got := f.resultFences(); got[len(got)-1] != 9 {
		t.Fatalf("低 cancel_fence 不得回退租约, got %d", got[len(got)-1])
	}
	if e, _ := f.svc.cfg.Ledger.Get("t-fence-1"); e.FenceEpoch != 9 {
		t.Fatalf("账本租约应保持 9, got %d", e.FenceEpoch)
	}
}
