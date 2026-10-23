// cancel_fence_test.go 任务租约评审修复（commit fa54c8f codex 审查 7 条）的
// hub 侧回归：P1-2 取消帧携任务落库租约 + cancelled 回执宽校验（含重派滞后
// 交错顺序）、P2-6 重派写时复制保留租约审计、P1-4 拒收路径与终态清扫并发不
// panic、P1-3 拒收 err 结构化携带 fence。
package core

import (
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCancelFrameCarriesTaskLease P1-2：task_cancel 信封携「该任务当前落库租约」
// 而非 node 当前注册代次——node 未重臂的滞后（重派推进租约）恰由该值补授。
func TestCancelFrameCarriesTaskLease(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeA", agentsOf("echo"), true)
	f.reRegisterAs(node, "nodeA", 2)

	ack, err := f.core.Submit(submitReq("nodeA", "echo", "hello"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// autoAck → acknowledged，租约定格为派发快照 2
	if got := f.taskOf(ack.TaskID); got.Lease == nil || got.Lease.FenceEpoch != 2 {
		t.Fatalf("前置：任务租约应为 2, got %+v", got.Lease)
	}

	// node 重连换代（epoch 5），任务未重派：租约仍 2
	f.reRegisterAs(node, "nodeA", 5)
	if _, dup, err := f.core.CancelTask(ack.TaskID, "测试"); err != nil || dup {
		t.Fatalf("取消失败: err=%v dup=%v", err, dup)
	}
	if n := len(node.cancelFences); n != 1 || node.cancelFences[0] != 2 {
		t.Fatalf("cancel 帧应携任务落库租约 2（非 node 代次 5）, got %v", node.cancelFences)
	}
}

// TestCancelledResultGateCancelFence P1-2 交错顺序全流程（重派未送达后取消）：
// node 持租约 2 → ack 丢 → hub 重派存租约 3 → 管理（重派后）取消携 cancel_fence=3
// → node 按旧租约 2 的取消回执被门禁拒收（fence < 租约，审计照记）；按对账后
// 租约 3 的取消回执被宽校验接受并合并；缺省（0，旧版 node）仍宽容。
func TestCancelledResultGateCancelFence(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false) // 不 autoAck：ack 丢失由不回模拟
	f.reRegisterAs(node, "nodeB", 2)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo", "hello"), "") // offered，租约 2
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("前置：应 offered, got %s", got)
	}

	// 重派换代：租约 2 → 3（重派 offer 已发出，node 未重臂——桩不处理重臂）
	f.reRegisterAs(node, "nodeB", 3)
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	task := f.taskOf(ack.TaskID)
	if task.Lease == nil || task.Lease.FenceEpoch != 3 {
		t.Fatalf("前置：重派后租约应为 3, got %+v", task.Lease)
	}

	// 重派后取消：cancel_fence=3（hub 当前任务租约）
	if _, _, err := f.core.CancelTask(ack.TaskID, "重派后取消"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if n := len(node.cancelFences); n != 1 || node.cancelFences[0] != 3 {
		t.Fatalf("cancel 帧应携 cancel_fence=3, got %v", node.cancelFences)
	}

	// node 按旧租约 2 回 cancelled：fence < cancel_fence → zombie 拒收，任务维持
	// cancelled 且 Result 仍空；err 结构化携带被拒 fence 与落库租约（P1-3）
	node.resultFenced(ack.TaskID, 2, cancelledRes())
	task = f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCancelled || task.Result != nil {
		t.Fatalf("旧租约取消回执应拒收维持原状态, got %s result=%+v", task.Status, task.Result)
	}
	if task.Lease == nil || task.Lease.Rejects != 1 || task.Lease.LastRejectedFence != 2 {
		t.Fatalf("应记 zombie 拒收审计, got %+v", task.Lease)
	}
	if len(node.errs) != 1 || node.errs[0].FenceEpoch != 2 || node.errs[0].LeaseEpoch != 3 {
		t.Fatalf("拒收 err 应结构化携带 fence_epoch=2/lease_epoch=3, got %+v", node.errs)
	}

	// node 对账取大者后按 3 回 cancelled：cancel_fence ≥ 落库租约 → 宽校验接受合并
	cr := cancelledRes() // 同一份结果值：后续缺省回执按「同终态同结果」走 dup ack 分支
	node.resultFenced(ack.TaskID, 3, cr)
	task = f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCancelled || task.Result == nil || task.Result.Error != "user_cancelled" {
		t.Fatalf("对账后取消回执应合并真实数据, got %s %+v", task.Status, task.Result)
	}
	if len(node.resultAcks) != 1 {
		t.Fatalf("合并分支应回 result_ack, got %v", node.resultAcks)
	}

	// 缺省宽容不回归：旧版 node 不带 fence 的取消回执亦接受（重复结果 → dup ack）
	n := len(node.resultAcks)
	node.resultFenced(ack.TaskID, 0, cr)
	if got := len(node.resultAcks); got != n+1 {
		t.Fatalf("缺省 fence 的取消回执应宽容接受（dup ack）, got %d → %d", n, got)
	}
}

// cancelledRes 取消收尾结果（与 node cancelledResult 同形）。
func cancelledRes() protocol.TaskResult {
	return protocol.TaskResult{
		Status: protocol.ResultCancelled, ExitCode: protocol.ExitCodeNotRun,
		Error: "user_cancelled", FinishedAt: protocol.NowUTC(),
	}
}

// TestRedispatchPreservesLeaseAudit P2-6：重派写时复制只刷新代次，拒收审计
//（Rejects/LastRejectedFence/LastAt）随任务累计保留，不得清零重建。
func TestRedispatchPreservesLeaseAudit(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false)
	f.reRegisterAs(node, "nodeB", 2)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo", "hello"), "") // offered，租约 2
	node.resultFenced(ack.TaskID, 9, completeRes())                  // zombie 拒收一次
	task := f.taskOf(ack.TaskID)
	if task.Lease == nil || task.Lease.Rejects != 1 || task.Lease.LastRejectedFence != 9 || task.Lease.LastAt.IsZero() {
		t.Fatalf("前置：应记拒收审计, got %+v", task.Lease)
	}

	// 重派（换代后补推）：租约 2 → 3，审计保留
	f.reRegisterAs(node, "nodeB", 3)
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	task = f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusOffered || task.Lease == nil || task.Lease.FenceEpoch != 3 {
		t.Fatalf("重派后应 offered 且租约 3, got %s %+v", task.Status, task.Lease)
	}
	if task.Lease.Rejects != 1 || task.Lease.LastRejectedFence != 9 || task.Lease.LastAt.IsZero() {
		t.Fatalf("重派不得清零租约审计, got %+v", task.Lease)
	}
}

// TestZombieRejectRacesSweepCleanup P1-4：拒收路径在事务外不再重读 Tasks[id]——
// 与 Sweep 的终态清理（删除超保留期任务）并发锤打，不发生零值条目 Lease 解引用
// panic（原实现 stored := View().Tasks[id].Lease.FenceEpoch 在删除竞态下 panic）。
func TestZombieRejectRacesSweepCleanup(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)
	f.reRegisterAs(node, "nodeB", 2)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo", "hello"), "")
	node.ack(ack.TaskID)
	node.start(ack.TaskID)
	node.result(ack.TaskID, completeRes())
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusCompleted {
		t.Fatalf("前置：应 completed, got %s", got)
	}
	// 回退 UpdatedAt 超终态保留期：Sweep 可删除
	if err := f.st.Update(func(st *store.State) error {
		tk := st.Tasks[ack.TaskID]
		tk.UpdatedAt = f.clock.Now().Add(-protocol.TerminalRetention - time.Hour)
		st.Tasks[tk.ID] = tk
		return nil
	}); err != nil {
		t.Fatalf("回退 UpdatedAt: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			node.resultFenced(ack.TaskID, 9, completeRes()) // zombie 拒收路径
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			f.core.Sweep() // 终态清理路径
		}
	}()
	wg.Wait()
}
