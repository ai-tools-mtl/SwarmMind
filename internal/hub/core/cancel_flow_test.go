// cancel_flow_test.go 取消链路收敛表单测（SPEC 4.3 / DoD-2 补课，2026-10-22 双审
// 批 B 修复 4）：CancelTask 状态矩阵、cancelled 合并分支、conflict 帧 TaskRef
// 回归（2578fd8）、离线取消重连补发（至少一次语义）。
package core

import (
	"errors"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCancelTaskStateMatrix CancelTask 状态矩阵：submitted 撤回信箱、终态 409、
// 重复取消幂等（dup）。
func TestCancelTaskStateMatrix(t *testing.T) {
	f := newHub(t)
	nodeA := f.newNode("nodeA", agentsOf("echo"), false)

	// submitted（在线直接投递）→ offered：取消成功且发帧
	ack, err := f.core.Submit(submitReq("nodeA", "echo", "hello"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	got, dup, err := f.core.CancelTask(ack.TaskID, "测试取消")
	if err != nil || dup {
		t.Fatalf("offered 取消应成功非 dup, err=%v dup=%v", err, dup)
	}
	if got.Status != protocol.StatusCancelled {
		t.Fatalf("状态应 cancelled, got %s", got.Status)
	}
	if len(nodeA.cancels) != 1 || nodeA.cancels[0].TaskID != ack.TaskID {
		t.Fatalf("node 应收到 1 条 task_cancel, got %+v", nodeA.cancels)
	}
	// 终态释放 workdir 互斥（P1.5 语义不回归）
	if lock := f.lockOf("nodeA", ack.TaskID); lock != "" {
		t.Fatalf("取消后 workdir 锁应释放, got %q", lock)
	}

	// 重复取消：幂等 dup，不重复发帧
	if _, dup, err := f.core.CancelTask(ack.TaskID, "再取消"); err != nil || !dup {
		t.Fatalf("重复取消应幂等 dup, err=%v dup=%v", err, dup)
	}
	if len(nodeA.cancels) != 1 {
		t.Fatalf("重复取消不应再发帧, got %d", len(nodeA.cancels))
	}

	// 已终态（completed）取消：409 ErrNotCancellable
	// 已终态（completed）取消：409 ErrNotCancellable（显式驱动完整生命周期）
	nodeB := f.newNode("nodeB", agentsOf("echo"), false)
	ack2, err := f.core.Submit(submitReq("nodeB", "echo", "world"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	nodeB.ack(ack2.TaskID)
	nodeB.start(ack2.TaskID)
	nodeB.result(ack2.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: f.clock.Now()})
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusCompleted {
		t.Fatalf("驱动后应 completed, got %s", got)
	}
	if _, _, err := f.core.CancelTask(ack2.TaskID, "对终态取消"); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("completed 取消应 ErrNotCancellable, got %v", err)
	}
}

// TestCancelSubmittedOfflineWithdraws 信箱撤回：离线节点的 submitted 任务取消后
// 移出信箱（配额释放，重连不投递）。
func TestCancelSubmittedOfflineWithdraws(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))
	ack, err := f.core.Submit(submitReq("nodeB", "echo", "hello"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if f.countMailbox("nodeB") != 1 {
		t.Fatalf("离线任务应暂存信箱, got %d", f.countMailbox("nodeB"))
	}
	if _, _, err := f.core.CancelTask(ack.TaskID, "撤回"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if f.countMailbox("nodeB") != 0 {
		t.Fatalf("取消后应撤出信箱, got %d", f.countMailbox("nodeB"))
	}
}

// TestResultCancelledMerges 收敛表 cancelled 合并分支：hub 先取消（Result=nil），
// node 的 task_result(cancelled) 后到 → 合并真实执行数据（非 conflict）+ result_ack。
func TestResultCancelledMerges(t *testing.T) {
	f := newHub(t)
	nodeA := f.newNode("nodeA", agentsOf("echo"), false)
	ack, _ := f.core.Submit(submitReq("nodeA", "echo", "hello"), "")
	if _, _, err := f.core.CancelTask(ack.TaskID, "先取消"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	nodeA.result(ack.TaskID, protocol.TaskResult{
		Status: protocol.ResultCancelled, Error: "user_cancelled", ExitCode: -1,
	})
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCancelled {
		t.Fatalf("终态不回退仍应 cancelled, got %s", task.Status)
	}
	if task.Result == nil || task.Result.Error != "user_cancelled" {
		t.Fatalf("node 结果应合并, got %+v", task.Result)
	}
	if len(nodeA.resultAcks) != 1 || nodeA.resultAcks[0] != ack.TaskID {
		t.Fatalf("合并分支应回 result_ack, got %+v", nodeA.resultAcks)
	}
}

// TestConflictErrCarriesTaskRef 回归 2578fd8：冲突分支回 err 必须携带 TaskRef
// （否则 node 无法停重传——深夜演练抓到的每分钟重发循环）。
func TestConflictErrCarriesTaskRef(t *testing.T) {
	f := newHub(t)
	nodeA := f.newNode("nodeA", agentsOf("echo"), false)
	ack, _ := f.core.Submit(submitReq("nodeA", "echo", "hello"), "")
	nodeA.result(ack.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: f.clock.Now()})
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusCompleted {
		t.Fatalf("首次结果应完成, got %s", got)
	}

	// node 重发同任务不同内容的结果 → 冲突 → err 帧带 TaskRef
	nodeA.result(ack.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 1, FinishedAt: f.clock.Now()})
	if len(nodeA.errTaskRefs) == 0 || nodeA.errTaskRefs[len(nodeA.errTaskRefs)-1] != ack.TaskID {
		t.Fatalf("conflict err 帧应携 TaskRef, got %+v", nodeA.errTaskRefs)
	}
}

// TestResendPendingCancelsOnRegister 离线取消重连补发：node 离线期间取消（发送
// 失败仅记日志），节点注册成功（SetOnline）时对名下 cancelled 且 Result==nil 的
// 任务补发 task_cancel——至少一次语义达成。
func TestResendPendingCancelsOnRegister(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))
	ack, _ := f.core.Submit(submitReq("nodeB", "echo", "hello"), "")
	if _, _, err := f.core.CancelTask(ack.TaskID, "离线期间取消"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	// nodeB 上线：SetOnline 触发 resendPendingCancels
	nodeB := &testNode{t: f.t, ep: f.bus.Endpoint("nodeB"), autoAck: false}
	nodeB.ep.Subscribe(nodeB.handle)
	f.core.SetOnline("nodeB", true)

	if len(nodeB.cancels) != 1 || nodeB.cancels[0].TaskID != ack.TaskID {
		t.Fatalf("重连应补发 task_cancel, got %+v", nodeB.cancels)
	}
}
