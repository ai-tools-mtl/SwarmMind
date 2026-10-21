package protocol

import "testing"

// legalPairs SPEC 4.3 合法转移全集，逐条注明出处规则。
var legalPairs = []struct {
	from, to TaskStatus
	why      string
}{
	{StatusSubmitted, StatusOffered, "主链：正常派单"},
	{StatusSubmitted, StatusAcknowledged, "收敛表：task_ack 迟到于 submitted（曾回退）→ 接受进 acknowledged"},
	{StatusOffered, StatusSubmitted, "主链：offered 30s 未收 task_ack 回退 submitted 重推"},
	{StatusOffered, StatusAcknowledged, "主链：task_ack（accepted 持久化成功）"},
	{StatusAcknowledged, StatusRunning, "主链：task_started"},
	{StatusRunning, StatusCompleted, "主链：正常完成"},
	{StatusRunning, StatusFailed, "主链：执行失败/超时"},
	{StatusRunning, StatusNeedsReview, "主链：needs_review（node_restarted_midway 等）"},
	// 收敛表：task_result 结果优先于中间态，任意非终态直达三终态
	{StatusSubmitted, StatusCompleted, "收敛表：task_result 于 submitted 直达终态"},
	{StatusSubmitted, StatusFailed, "收敛表：task_result 于 submitted 直达终态（含 expire_at 到期 failed/expired）"},
	{StatusSubmitted, StatusNeedsReview, "收敛表：task_result 于 submitted 直达终态"},
	{StatusOffered, StatusCompleted, "收敛表：task_result 于 offered 直达终态"},
	{StatusOffered, StatusFailed, "收敛表：task_result 于 offered 直达终态（含 expire_at 到期）"},
	{StatusOffered, StatusNeedsReview, "收敛表：task_result 于 offered 直达终态"},
	{StatusAcknowledged, StatusCompleted, "收敛表：task_result 于 acknowledged 直达终态"},
	{StatusAcknowledged, StatusFailed, "收敛表：task_result 于 acknowledged 直达终态（含 expire_at 到期）"},
	{StatusAcknowledged, StatusNeedsReview, "收敛表：task_result 于 acknowledged 直达终态"},
}

// TestTransitionCheckFullGrid 状态机 7×7 全组合逐格核对 + 未知状态拒绝。
func TestTransitionCheckFullGrid(t *testing.T) {
	all := []TaskStatus{
		StatusSubmitted, StatusOffered, StatusAcknowledged, StatusRunning,
		StatusCompleted, StatusFailed, StatusNeedsReview,
	}
	legal := make(map[TaskStatus]map[TaskStatus]bool)
	for _, p := range legalPairs {
		if legal[p.from] == nil {
			legal[p.from] = make(map[TaskStatus]bool)
		}
		legal[p.from][p.to] = true
	}
	for _, from := range all {
		for _, to := range all {
			want := legal[from][to]
			if got := TransitionCheck(from, to); got != want {
				t.Errorf("TransitionCheck(%s→%s)=%v, 期望 %v", from, to, got, want)
			}
		}
	}
	// 未知状态一律非法
	if TransitionCheck("unknown", StatusSubmitted) {
		t.Error("未知 from 状态应非法")
	}
	if TransitionCheck(StatusSubmitted, "unknown") {
		t.Error("未知 to 状态应非法")
	}
	// 终态永不回退：终态→终态（含自身）亦非法
	for _, term := range []TaskStatus{StatusCompleted, StatusFailed, StatusNeedsReview} {
		if TransitionCheck(term, term) {
			t.Errorf("终态 %s→%s 应非法（永不回退）", term, term)
		}
	}
}

// TestTerminalStatus 终态判定。
func TestTerminalStatus(t *testing.T) {
	for _, s := range []TaskStatus{StatusCompleted, StatusFailed, StatusNeedsReview} {
		if !s.Terminal() {
			t.Errorf("%s 应为终态", s)
		}
	}
	for _, s := range []TaskStatus{StatusSubmitted, StatusOffered, StatusAcknowledged, StatusRunning, "unknown"} {
		if s.Terminal() {
			t.Errorf("%s 不应为终态", s)
		}
	}
}
