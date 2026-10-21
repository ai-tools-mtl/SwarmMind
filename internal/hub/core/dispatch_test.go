package core

import (
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestDeliveryWindow 投递窗口（offered+acknowledged ≤8，超则暂停出队；started
// 释放一格后恢复，SPEC 4.6）。
func TestDeliveryWindow(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true) // autoAck：offer→acknowledged（占窗口）

	ids := make([]string, 0, protocol.DeliveryWindow+1)
	for i := 0; i < protocol.DeliveryWindow; i++ {
		ack, err := f.core.Submit(submitReq("nodeB", "echo"), "")
		if err != nil {
			t.Fatalf("第 %d 条提交失败: %v", i+1, err)
		}
		if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusAcknowledged {
			t.Fatalf("第 %d 条应已 acknowledged（autoAck）, got %s", i+1, got)
		}
		ids = append(ids, ack.TaskID)
	}
	// 第 9 条：窗口满 → 暂停出队，滞留 submitted
	ack9, err := f.core.Submit(submitReq("nodeB", "echo"), "")
	if err != nil {
		t.Fatalf("第 9 条提交应入箱成功, got %v", err)
	}
	if got := f.taskOf(ack9.TaskID).Status; got != protocol.StatusSubmitted {
		t.Errorf("窗口满时第 9 条应滞留 submitted, got %s", got)
	}
	if n := len(node.offers); n != protocol.DeliveryWindow {
		t.Errorf("窗口满前应恰好投递 %d 次, got %d", protocol.DeliveryWindow, n)
	}

	// started 释放一格 → 恢复出队（autoAck 下新 offer 立即转 acknowledged）
	node.start(ids[0])
	if got := f.taskOf(ack9.TaskID).Status; got != protocol.StatusAcknowledged {
		t.Errorf("started 释放窗口后第 9 条应恢复投递（并 auto-ack）, got %s", got)
	}
	if n := len(node.offers); n != protocol.DeliveryWindow+1 {
		t.Errorf("恢复后应投递 %d 次, got %d", protocol.DeliveryWindow+1, n)
	}

	// 其余全部 started（进入 running，不再占窗口），随后重新灌满窗口
	for i := 1; i < protocol.DeliveryWindow; i++ {
		node.start(ids[i])
	}
	for i := 0; i < protocol.DeliveryWindow-1; i++ {
		if _, err := f.core.Submit(submitReq("nodeB", "echo"), ""); err != nil {
			t.Fatalf("回填第 %d 条失败: %v", i+1, err)
		}
	}
	ack10, err := f.core.Submit(submitReq("nodeB", "echo"), "")
	if err != nil {
		t.Fatalf("第 10 条提交失败: %v", err)
	}
	if got := f.taskOf(ack10.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("窗口再度占满应滞留, got %s", got)
	}
	// acknowledged 任务直达终态 → 释放一格（窗口只计 offered+acknowledged，
	// running 不占窗口，其终态不释放）→ 恢复出队
	node.result(ack9.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: f.clock.Now()})
	if got := f.taskOf(ack10.TaskID).Status; got != protocol.StatusAcknowledged {
		t.Errorf("终态释放窗口后第 10 条应恢复投递, got %s", got)
	}
}

// TestOfferAckTimeoutFallback offered 30s 无 ack → 回退 submitted 重推（SPEC 4.3）。
func TestOfferAckTimeoutFallback(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false) // 不 ack

	ack, err := f.core.Submit(submitReq("nodeB", "echo"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("应已 offered, got %s", got)
	}
	if n := len(node.offers); n != 1 {
		t.Fatalf("应已投递 1 次, got %d", n)
	}

	// 未到超时：清扫不动作
	f.clock.Advance(protocol.OfferAckTimeout / 2)
	f.core.Sweep()
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("未超时不应回退, got %s", got)
	}

	// 超过 30s：回退 submitted → 回队首 → 立即重推 offered
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusOffered {
		t.Errorf("回退+重推后应为 offered, got %s", task.Status)
	}
	if n := len(node.offers); n != 2 {
		t.Errorf("应重推第 2 次, got %d", n)
	}
	if !task.UpdatedAt.Equal(f.clock.Now()) {
		t.Errorf("重推应刷新 UpdatedAt（重开 30s 计时）, got %v", task.UpdatedAt)
	}
	// 重推期间不在信箱（offered 不占配额）
	if n := f.countMailbox("nodeB"); n != 0 {
		t.Errorf("offered 不应占用信箱, got %d", n)
	}
}

// TestOfferTimeoutThenExpire 回退与过期的优先级：任务已过期则置 failed/expired
// 而非回退重推（SPEC 4.3「任务过期则置 failed/expired」）。
func TestOfferTimeoutThenExpire(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false)

	ack, err := f.core.Submit(submitReq("nodeB", "echo"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 直接推到过期时刻（expire = created+7d），此时早已超过 offer 30s 超时
	f.clock.Advance(protocol.TaskRetention + protocol.OfferAckTimeout)
	f.core.Sweep()
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusFailed || task.Result == nil || task.Result.Error != protocol.CodeExpired {
		t.Errorf("过期任务应 failed/expired, got %+v", task.Result)
	}
	if n := len(node.offers); n != 1 {
		t.Errorf("过期不应重推, got %d 次投递", n)
	}
}

// TestLateAckConvergence 收敛表：task_ack 迟到（曾回退 submitted）→ 接受并释放信箱。
// 节点下线时回退任务滞留信箱；hub 对「半开连接」残存会话的迟到帧仍按收敛表处理。
func TestLateAckConvergence(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	f.core.SetOnline("nodeB", false) // 下线：回退后无补推
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep() // 回退 submitted（滞留信箱）
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("回退后应为 submitted, got %s", got)
	}
	if n := f.countMailbox("nodeB"); n != 1 {
		t.Fatalf("回退后应滞留信箱 1 条, got %d", n)
	}

	node.ack(ack.TaskID) // 迟到 ack：submitted → acknowledged
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusAcknowledged {
		t.Errorf("迟到 ack 应收敛为 acknowledged, got %s", task.Status)
	}
	if n := f.countMailbox("nodeB"); n != 0 {
		t.Errorf("离开 submitted 应释放信箱, got %d", n)
	}
	// 重复 ack 幂等：无变化
	node.ack(ack.TaskID)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusAcknowledged {
		t.Errorf("重复 ack 不应改变状态, got %s", got)
	}
}

// TestLateStartedIgnored 收敛表：started 在非 acknowledged 状态被忽略。
func TestLateStartedIgnored(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "") // offered
	node.start(ack.TaskID)                                  // offered → started：状态机不允许，忽略
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Errorf("offered 收到 started 应忽略, got %s", got)
	}

	node.ack(ack.TaskID)
	node.start(ack.TaskID)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusRunning {
		t.Fatalf("正常 started 应转 running, got %s", got)
	}
	// 重复 started 忽略
	node.start(ack.TaskID)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusRunning {
		t.Errorf("重复 started 不应改变状态, got %s", got)
	}
	// 终态后 started 忽略
	node.result(ack.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0})
	node.start(ack.TaskID)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusCompleted {
		t.Errorf("终态后 started 不得回退, got %s", got)
	}
}

// TestOwnershipCheck 归属检查：任何 node 的帧只对其名下任务有效（SPEC 4.3）。
func TestOwnershipCheck(t *testing.T) {
	f := newHub(t)
	nodeB := f.newNode("nodeB", agentsOf("echo"), true)
	nodeC := f.newNode("nodeC", agentsOf("other"), false)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	// nodeC 冒认 nodeB 的任务：ack/result 均被忽略
	nodeC.ack(ack.TaskID)
	nodeC.result(ack.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: -1})
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusAcknowledged {
		t.Errorf("越权帧应被忽略, got %s", task.Status)
	}
	if len(nodeC.resultAcks) != 0 {
		t.Errorf("越权 result 不得获得 result_ack, got %v", nodeC.resultAcks)
	}
	_ = nodeB
}

// second 测试辅助：略大于零的推进量（确保 Sweep 的 >= 判定成立）。
func (f *hubFixture) second() time.Duration { return time.Second }
