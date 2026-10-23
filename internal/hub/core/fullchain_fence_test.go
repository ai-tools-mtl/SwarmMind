// fullchain_fence_test.go P2-7 补测：任务租约（P4/SPEC 4.11 + 评审修复）在
// 真实 hub+node fixture（真账本 + membus 全链，非桩）下的跨会话行为——
//   - 跨会话 needs_review 回传：等值基准是任务落库租约而非 node 当前代次；
//   - ACK 丢失 → offer 超时重派（重派即换新租约）→ 重派收口重臂 → 结果按
//     对账后租约重发并被 hub 接受（P1-1/P1-3 收口链）。
package core

import (
	"context"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// gateExecRunner 门控执行器：阻塞至放行后返回预设结果（真实恢复/完成窗口控制）。
type gateExecRunner struct {
	release chan struct{}
	res     protocol.TaskResult
}

func (g *gateExecRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	<-g.release
	r := g.res
	r.FinishedAt = protocol.NowUTC()
	return r
}

// waitForCore 全链测试轮询（membus 同步链 + 异步执行体，无内置等待）。
func waitForCore(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// echoCard 构造 exposed=true 的 echo-demo 能力卡。
func echoCard() []protocol.AgentCard {
	return []protocol.AgentCard{{
		Name: "echo-demo", Kind: protocol.AgentKindCLI,
		InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}}
}

// TestFullChainNeedsReviewAcrossSessions 跨会话 needs_review 回传（真实链）：
// node 持租约 2 执行中「重启」（换代 epoch 3），恢复以 needs_review 收尾回传——
// 回执携账本租约 2、SessionEpoch=3；hub 门禁等值基准是任务落库租约（2），跨会话
// 合法回传接受（不误拒、不重派），三段确认闭合（result_ack 记账）。
func TestFullChainNeedsReviewAcrossSessions(t *testing.T) {
	f := newHub(t)
	gate := &gateExecRunner{release: make(chan struct{}), res: protocol.TaskResult{
		Status: protocol.ResultNeedsReview, ExitCode: protocol.ExitCodeNotRun,
		Error: protocol.CodeNodeRestartedMidway,
	}}
	nodeB := newExecNode(f, "nodeB", map[string]execRunner{"echo-demo": gate}, echoCard())
	nodeB.asyncExec = true
	f.bumpEpoch("nodeB", 2)
	nodeB.epoch.Store(2)

	ack, err := f.core.Submit(submitReq("nodeB", "echo-demo", "across"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if !waitForCore(2*time.Second, func() bool {
		return f.taskOf(ack.TaskID).Status == protocol.StatusRunning &&
			func() bool { e, ok := nodeB.led.Get(ack.TaskID); return ok && e.State == ledger.StateStarted }()
	}) {
		t.Fatalf("前置：任务应在途 running, got %s", f.taskOf(ack.TaskID).Status)
	}
	if task := f.taskOf(ack.TaskID); task.Lease == nil || task.Lease.FenceEpoch != 2 {
		t.Fatalf("前置：派发租约应为 2, got %+v", task.Lease)
	}

	// node「重启」：换代 epoch 3（原会话失效），原执行丢失，恢复回传
	f.bumpEpoch("nodeB", 3)
	nodeB.epoch.Store(3)
	close(gate.release)

	if !waitForCore(2*time.Second, func() bool {
		return f.taskOf(ack.TaskID).Status == protocol.StatusNeedsReview
	}) {
		t.Fatalf("跨会话 needs_review 应被接受, got %s", f.taskOf(ack.TaskID).Status)
	}
	task := f.taskOf(ack.TaskID)
	if task.Result == nil || task.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("恢复结果应合并, got %+v", task.Result)
	}
	if task.Lease == nil || task.Lease.FenceEpoch != 2 {
		t.Fatalf("任务租约不得被改写, got %+v", task.Lease)
	}
	if !waitForCore(2*time.Second, func() bool {
		e, ok := nodeB.led.Get(ack.TaskID)
		return ok && e.State == ledger.StateFinished && e.ResultAcked
	}) {
		t.Fatal("三段确认应闭合（result_ack 记账）")
	}
}

// TestFullChainAckLostRedispatchRearmResend ACK 丢失后结果重发（真实链，P2-7）：
// ack 丢 → hub offer 超时回退重派（租约 2→3）→ 重复 offer 到达 node 重臂收口
//（账本 2→3）→ ack 恢复抵达 → 原执行完成按对账后租约 3 回带 → hub 等值接受 →
// completed + result_ack，任务不滞留。
func TestFullChainAckLostRedispatchRearmResend(t *testing.T) {
	f := newHub(t)
	gate := &gateExecRunner{release: make(chan struct{}), res: protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "re-dispatched\n",
	}}
	nodeB := newExecNode(f, "nodeB", map[string]execRunner{"echo-demo": gate}, echoCard())
	nodeB.asyncExec = true
	nodeB.dropAcks.Store(true) // ack 丢失
	f.bumpEpoch("nodeB", 2)
	nodeB.epoch.Store(2)

	ack, err := f.core.Submit(submitReq("nodeB", "echo-demo", "lost-ack"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if !waitForCore(2*time.Second, func() bool {
		e, ok := nodeB.led.Get(ack.TaskID)
		return ok && e.State == ledger.StateStarted
	}) {
		t.Fatal("前置：node 应已在途执行")
	}
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("前置：ack 丢失后 hub 应维持 offered, got %s", got)
	}

	// node 换代 + ack 恢复；offer 超时回退 → 重派（租约 2→3）
	f.bumpEpoch("nodeB", 3)
	nodeB.epoch.Store(3)
	nodeB.dropAcks.Store(false)
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()

	if !waitForCore(2*time.Second, func() bool {
		e, _ := nodeB.led.Get(ack.TaskID)
		return f.taskOf(ack.TaskID).Status == protocol.StatusAcknowledged && e.FenceEpoch == 3
	}) {
		t.Fatalf("重派收口应重臂为 3 并确认, hub=%s ledger=%+v",
			f.taskOf(ack.TaskID).Status, func() any { e, _ := nodeB.led.Get(ack.TaskID); return e.FenceEpoch }())
	}

	// 原执行完成：按对账后租约 3 回带 → hub 接受
	close(gate.release)
	if !waitForCore(2*time.Second, func() bool {
		return f.taskOf(ack.TaskID).Status == protocol.StatusCompleted
	}) {
		t.Fatalf("重派后结果应被接受, got %s", f.taskOf(ack.TaskID).Status)
	}
	task := f.taskOf(ack.TaskID)
	if task.Result == nil || task.Result.FinalAnswer != "re-dispatched\n" {
		t.Fatalf("结果应合并, got %+v", task.Result)
	}
	if !waitForCore(2*time.Second, func() bool {
		e, ok := nodeB.led.Get(ack.TaskID)
		return ok && e.State == ledger.StateFinished && e.ResultAcked && e.FenceEpoch == 3
	}) {
		t.Fatal("node 账本应 finished+acked 且租约对齐 3")
	}
	if nodeB.offers != 2 {
		t.Fatalf("应恰两次投递（原派+重派）, got %d", nodeB.offers)
	}
}
