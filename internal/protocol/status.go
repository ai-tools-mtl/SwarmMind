package protocol

// TaskStatus 任务状态（hub 视角，SPEC 4.3）。
type TaskStatus string

// 任务状态全集：submitted → offered → acknowledged → running → 四终态（SPEC 4.3）。
const (
	// StatusSubmitted 已持久化待投递（信箱配额计的就是此状态）。
	StatusSubmitted TaskStatus = "submitted"
	// StatusOffered 已投递 node，等待 task_ack（OfferAckTimeout 未收回退 submitted）。
	StatusOffered TaskStatus = "offered"
	// StatusAcknowledged node 已入账本，等待执行开始。
	StatusAcknowledged TaskStatus = "acknowledged"
	// StatusRunning 执行中。
	StatusRunning TaskStatus = "running"
	// StatusCompleted 终态：执行成功。
	StatusCompleted TaskStatus = "completed"
	// StatusFailed 终态：执行失败（含 expired）。
	StatusFailed TaskStatus = "failed"
	// StatusNeedsReview 终态：执行结果不确定需人工核对；不得折叠为 failed（SPEC 4.8）。
	StatusNeedsReview TaskStatus = "needs_review"
	// StatusCancelled 终态：被管理面取消（task_cancel 生命周期，error=user_cancelled）。
	// 与 needs_review 同级独立终态：不折叠为 failed，不自动重跑（SPEC 4.3）。
	StatusCancelled TaskStatus = "cancelled"
)

// Terminal 报告 s 是否终态。终态（completed/failed/needs_review/cancelled）永不回退（SPEC 4.3）。
func (s TaskStatus) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled:
		return true
	}
	return false
}

// validTransitions 合法转移表：SPEC 4.3 主链 + offered 30s 回退 + 迟到事件收敛 + 终态冻结。
var validTransitions = map[TaskStatus][]TaskStatus{
	// submitted：正常派单；task_ack 迟到（曾回退）直接进 acknowledged（收敛表）；
	// task_result 结果优先于中间态，可直达终态；管理面取消直达 cancelled。
	StatusSubmitted: {StatusOffered, StatusAcknowledged, StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled},
	// offered：30s 无 ack 回退 submitted；收到 task_ack 进 acknowledged；result 直达终态；
	// 管理面取消直达 cancelled（撤回投递 + task_cancel 通知 node）。
	StatusOffered: {StatusSubmitted, StatusAcknowledged, StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled},
	// acknowledged：task_started 进 running；result 直达终态；管理面取消直达 cancelled。
	StatusAcknowledged: {StatusRunning, StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled},
	// running：正常进入四终态之一（含管理面取消：task_cancel → node 整组终止）。
	StatusRunning: {StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled},
	// 终态永不回退：无任何出边（含终态间互转）。
	StatusCompleted:   {},
	StatusFailed:      {},
	StatusNeedsReview: {},
	StatusCancelled:   {},
}

// TransitionCheck 报告 from→to 是否为 SPEC 4.3 状态机的合法转移。
// 覆盖：主链、offered 30s 回退、task_ack 迟到收敛、task_result 对任意非终态直达终态；
// 终态无出边（永不回退）；未知状态一律非法。
func TransitionCheck(from, to TaskStatus) bool {
	for _, next := range validTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}
