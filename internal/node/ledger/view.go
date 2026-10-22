// 任务视图（方案 3.2 双路径，2026-10-22）：本地直执行后 node 本地账本是本机
// 任务的真相源，GET /api/v1/tasks/{id} 改从账本出视图——不再经 task_query 帧
// 问 hub（离线自治：hub 挂了本机查询照常可用）。
package ledger

import (
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TaskView 由账本条目生成协议层任务视图（返回副本，调用方可自由改写）。
// 状态映射（node 侧视角，账本 State → SPEC 4.3 状态机）：
//   - accepted → acknowledged（已入账本待执行；hub 侧 offered/submitted 细分
//     不在 node 视野内，node 已 ack 即为本视角的 acknowledged）；
//   - started  → running；
//   - finished → 终态 = Result.Status，携带完整结果。
// UpdatedAt 取该视角下最近可知的变更时刻（finished 用结果完成时刻，其余用创建时刻）。
func TaskView(e Entry) protocol.Task {
	e = e.normalized()
	t := e.Task.Clone()
	switch e.State {
	case StateStarted:
		t.Status = protocol.StatusRunning
		t.UpdatedAt = t.CreatedAt
	case StateFinished:
		if e.Result != nil {
			r := e.Result.Clone()
			t.Result = &r
			t.Status = protocol.TaskStatus(r.Status)
			t.UpdatedAt = r.FinishedAt
		} else {
			t.Status = protocol.StatusFailed // 防御：finished 必有结果，缺省按失败呈现
			t.UpdatedAt = t.CreatedAt
		}
	default: // accepted
		t.Status = protocol.StatusAcknowledged
		t.UpdatedAt = t.CreatedAt
	}
	return t
}
