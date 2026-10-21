package core

import (
	"bytes"
	"encoding/json"
	"log"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// resultStatuses 合法终态映射：payload status → TaskStatus（SPEC 4.2 task_result）。
var resultStatuses = map[string]protocol.TaskStatus{
	protocol.ResultCompleted:   protocol.StatusCompleted,
	protocol.ResultFailed:      protocol.StatusFailed,
	protocol.ResultNeedsReview: protocol.StatusNeedsReview,
}

// handleTaskResult 终态回传仲裁（SPEC 4.3 收敛表）：
//   - 归属检查先于一切：任何 node 的帧只对其名下任务有效；
//   - 任意非终态（submitted/offered/acknowledged/running）→ 接受（结果优先于
//     状态机中间态），submitted 需同步释放信箱；
//   - 已同终态且结果一致（重发）→ 重发 result_ack；
//   - 已终态但结果不同（冲突）→ 保留先到，回 err(result_conflict)，不重发 ack。
//
// 终态落盘后：result_ack 回执行 node；提交方为 node 会话时另发 result_event
// （离线错过即弃，靠 task_query 补查——SPEC 已声明限制）。
func (c *Core) handleTaskResult(env *protocol.Envelope) {
	var p protocol.TaskResultPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.TaskID == "" {
		log.Printf("[hub] task_result 载荷解析失败 from=%s: %v", env.From, err)
		return
	}
	toStatus, ok := resultStatuses[p.Status]
	if !ok || p.Result == nil {
		log.Printf("[hub] task_result 非法终态 status=%q from=%s", p.Status, env.From)
		return
	}

	var (
		accepted *protocol.Task // 接受后的任务（nil=未接受）
		dupAck   bool           // 同终态同结果：重发 ack
		conflict bool           // 保留先到：回冲突
	)
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[p.TaskID]
		if !ok || t.TargetNode != env.From {
			log.Printf("[hub] task_result 未知/归属不符 task=%s from=%s", p.TaskID, env.From)
			return nil
		}
		if t.Status.Terminal() {
			if sameTerminalResult(t.Result, p.Result) {
				dupAck = true // 收敛表：重复结果重发 result_ack，禁止确认悬挂
			} else {
				conflict = true // 收敛表：冲突保留先到，回 err(result_conflict)
			}
			return nil
		}
		nt := t.Clone()
		nt.Status = toStatus
		res := p.Result.Clone()
		nt.Result = &res
		nt.UpdatedAt = c.now()
		st.Tasks[nt.ID] = nt
		releaseWorkdirLock(st, nt.TargetNode, nt.ID) // 终态释放 workdir 互斥（P1.5）
		if t.Status == protocol.StatusSubmitted {
			mailbox.Remove(st, nt.TargetNode, nt.ID) // 直达终态即释放信箱
		}
		accepted = &nt
		return nil
	})
	if err != nil {
		log.Printf("[hub] task_result 事务失败 task=%s: %v", p.TaskID, err)
		return
	}
	switch {
	case accepted != nil:
		c.noteTask(accepted)
		_ = c.sendFrame(env.From, protocol.MsgResultAck, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID}, nil)
		c.notifyResultEvent(accepted)
		c.dispatch(env.From) // 窗口释放一格，恢复出队
	case dupAck:
		_ = c.sendFrame(env.From, protocol.MsgResultAck, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID}, nil)
	case conflict:
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeResultConflict, Msg: "终态已持久化，保留先到结果"})
	}
}

// notifyResultEvent 终态推送给 node 会话提交方（SPEC 4.2 result_event）：
// SubmittedBy 命中注册表即认定 node 提交（node 提交时 hub 已覆盖 SubmittedBy 为
// 该 node_id）；node 不在线则丢弃，靠主动查询补（已知限制）。
func (c *Core) notifyResultEvent(t *protocol.Task) {
	if t.Result == nil {
		return
	}
	if _, isNode := c.st.View().Nodes[t.SubmittedBy]; !isNode {
		return
	}
	if !c.isOnline(t.SubmittedBy) {
		return // 已知限制：离线错过即弃
	}
	payload := protocol.TaskResultPayload{TaskID: t.ID, Status: string(t.Status), Result: t.Result}
	_ = c.sendFrame(t.SubmittedBy, protocol.MsgResultEvent, "", payload, nil)
}

// sameTerminalResult 报告两份结果是否「同终态且结果一致」（收敛表重发 ack 分支）。
// 按 canonical JSON 全量比对：node 侧重发的是账本留存的同一份结果，字节级一致。
func sameTerminalResult(a, b *protocol.TaskResult) bool {
	if a == nil || b == nil || a.Status != b.Status {
		return false
	}
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
