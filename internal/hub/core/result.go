package core

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	protocol.ResultCancelled:   protocol.StatusCancelled,
}

// handleTaskResult 终态回传仲裁（SPEC 4.3 收敛表 + P4 任务租约 4.11）：
//   - 归属检查先于一切：任何 node 的帧只对其名下任务有效；
//   - 任务租约门禁（P4，先于状态仲裁）：回带 fence_epoch 与该任务当前派发租约
//     （Task.Lease.FenceEpoch，派发/重派事务内落库）等值才进入仲裁——重派后旧
//     派单的迟到回写（zombie 写）据此外科式排除，且不误伤「同租约跨会话的合法
//     重发」（进程重启恢复重发、offer 超时前的迟到回执，SPEC 4.8/4.5）；P1-2
//     宽校验：cancelled 回执按 cancel_fence ≥ 落库租约接受（取消是管理面意图，
//     覆盖重派推进租约后 node 未及重臂的滞后）；缺省（0，旧版 node/旧账本/未携
//     租约派发）按「缺省=当前租约」宽容放行；过期
//     → 拒收 zombie 结果：任务维持原状态（不改 Status/UpdatedAt/Result），Lease
//     审计注记 zombie_result_rejected，回 err(result_conflict) 促停 node 重发
//     （node 置账本 closed）。送达方身份由会话层担保（帧只经当前会话进入），
//     租约担保派发代次一致，两层正交；
//   - 任意非终态（submitted/offered/acknowledged/running）→ 接受（结果优先于
//     状态机中间态），submitted 需同步释放信箱；
//   - 已 cancelled 且结果未落（hub 先取消，Result 为 nil）→ cancelled 分支：合并
//     node 回传的真实执行数据（退出码/日志尾/产物），重复回发 ack；
//   - 已同终态且结果一致（重发）→ 重发 result_ack；
//   - 已终态但结果不同（含取消后 node 迟到的自然终态）→ 保留先到，
//     回 err(result_conflict)，不重发 ack。
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
		merged   bool           // cancelled 分支：合并结果（终态钩子已在取消时触发过）
		dupAck   bool           // 同终态同结果：重发 ack
		conflict bool           // 保留先到：回冲突
		zombie   bool           // 租约过期（zombie 写）：维持原状态拒收
		zLease   int64          // zombie 分支事务内捕获的落库租约（P1-4：事务外不得重读 Tasks——Sweep 并发删除超保留期任务时零值解引用）
		depRev   *depReview     // 依赖门控下游复评（P4.3：首次落终态事务内原子复评）
	)
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[p.TaskID]
		if !ok || t.TargetNode != env.From {
			log.Printf("[hub] task_result 未知/归属不符 task=%s from=%s", p.TaskID, env.From)
			return nil
		}
		var lease int64
		if t.Lease != nil {
			lease = t.Lease.FenceEpoch
		}
		// P4 任务租约门禁（先于状态仲裁）。P1-2 宽校验：cancelled 回执是管理面
		// 取消意图的回执——帧携 cancel_fence（hub 当前任务落库租约，经当前会话
		// 送达，zombie 无法伪造），node 对账取大者回带，故 ≥ 落库租约即接受，
		// 覆盖「重派推进租约而 node 未及重臂」的取消回执滞后；其余终态维持
		// 等值。过期 → 拒收 zombie 结果：任务维持原状态，只追加固化审计字段。
		stale := false
		if env.FenceEpoch != 0 {
			if toStatus == protocol.StatusCancelled {
				stale = env.FenceEpoch < lease
			} else {
				stale = env.FenceEpoch != lease
			}
		}
		if stale {
			nt := t.Clone()
			ls := protocol.TaskLease{FenceEpoch: lease, LastRejectedFence: env.FenceEpoch, LastAt: c.now()}
			if t.Lease != nil {
				ls.Rejects = t.Lease.Rejects + 1
			} else {
				ls.Rejects = 1
			}
			nt.Lease = &ls
			st.Tasks[nt.ID] = nt
			zombie = true
			zLease = lease
			return nil
		}
		if t.Status.Terminal() {
			switch {
			case t.Status == protocol.StatusCancelled && t.Result == nil && toStatus == protocol.StatusCancelled:
				// 收敛表 cancelled 分支：hub 先取消（无结果），node 的取消结果后到——
				// 合并真实执行数据，终态不回退（仍为 cancelled）
				nt := t.Clone()
				res := p.Result.Clone()
				nt.Result = &res
				nt.UpdatedAt = c.now()
				st.Tasks[nt.ID] = nt
				accepted = &nt
				merged = true
			case sameTerminalResult(t.Result, p.Result):
				dupAck = true // 收敛表：重复结果重发 result_ack，禁止确认悬挂
			default:
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
		writebackWorkTerminal(st, &nt, nt.UpdatedAt) // 工作单终态回写（P5 W2：事实与任务终态同事务）
		writebackSessionTurn(st, &nt, nt.UpdatedAt)  // 会话轮终回写（T2：轮行与任务终态同事务）
		if t.Status == protocol.StatusSubmitted {
			mailbox.Remove(st, nt.TargetNode, nt.ID) // 直达终态即释放信箱
		}
		accepted = &nt
		// 依赖门控（P4.3）：上游到终态在同一事务内复评下游——全依赖 completed 的
		// 下游解除阻塞（发布后 dispatch 投递），上游 failed/needs_review/cancelled
		// 的下游更新 blocked_reason；superseded 标注在此产生
		depRev = reevaluateDependents(st, nt.IdemKey)
		return nil
	})
	if err != nil {
		log.Printf("[hub] task_result 事务失败 task=%s: %v", p.TaskID, err)
		return
	}
	if zombie {
		// P1-4：租约值取自拒收事务内捕获的 zLease——事务外重读 Tasks[id] 会与
		// Sweep 的终态清理（删除超保留期任务）竞态，零值条目的 Lease 解引用 panic。
		log.Printf("[hub] zombie_result_rejected 拒收过期租约结果 task=%s from=%s fence_epoch=%d current_lease=%d（任务维持原状态，已记 Lease 审计）",
			p.TaskID, env.From, env.FenceEpoch, zLease)
		// 回 result_conflict：node 据此置账本 closed 停止重发， zombie 结果不再
		// 周期性重入仲裁（载荷携 TaskRef，同冲突帧口径；ErrInfo 结构化携带被拒
		// fence 与当时落库租约——P1-3：node 仅当被拒 fence 与账本条目当前 fence
		// 等值才置 closed，拒收旧代次不得关闭已重臂条目的新租约重发）
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID},
			&protocol.ErrInfo{
				Code: protocol.CodeResultConflict,
				Msg: fmt.Sprintf("zombie_result_rejected: 任务租约过期（fence_epoch=%d ≠ current_lease=%d），拒绝回写，任务维持原状态",
					env.FenceEpoch, zLease),
				FenceEpoch: env.FenceEpoch,
				LeaseEpoch: zLease,
			})
		return
	}
	switch {
	case accepted != nil:
		c.noteTask(accepted)
		noteDepReview(c, depRev)
		_ = c.sendFrame(env.From, protocol.MsgResultAck, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID}, nil)
		c.notifyResultEvent(accepted)
		if merged {
			// cancelled 分支：终态在 CancelTask 事务时已落、终态旁路已触发过，
			// 此处只补真实执行数据，不重复触发（fireTerminal 语义：首次落终态）
			c.dispatch(env.From)
			break
		}
		c.fireTerminal(accepted)     // 群聊闭环回流等终态旁路（GROUP-RFC）
		c.dispatch(env.From)         // 窗口释放一格，恢复出队
		dispatchDepReview(c, depRev) // 解除阻塞的下游任务进入正常投递（P4.3）
	case dupAck:
		_ = c.sendFrame(env.From, protocol.MsgResultAck, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID}, nil)
	case conflict:
		// 必须携带 TaskRef：node 依赖它定位账本条目置 closed 停止重传（2026-10-22
		// 深夜演练抓到：Payload nil 导致 node 重发循环每分钟一次）
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID,
			protocol.TaskRef{TaskID: p.TaskID},
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
