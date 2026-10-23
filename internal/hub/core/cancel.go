package core

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 管理面任务取消（task_cancel 生命周期，SPEC 4.3 cancelled 终态）：
// hub 侧状态先收敛（cancelled 终态 + 释放信箱/互斥锁），再向目标 node 尽力送达
// task_cancel 帧；node 整组终止在途执行后以 task_result(cancelled) 走既有三段
// 确认链路回传真实执行数据（收敛表 cancelled 分支合并落盘）。cancelled 与
// needs_review 同级独立终态：不折叠为 failed、不自动重跑。

// ErrNotCancellable 任务已终态且非 cancelled（completed/failed/needs_review 不可
// 取消，HTTP 409）；cancelled 自身的重复取消按幂等命中处理（返回 dup=true）。
var ErrNotCancellable = errors.New("core: 任务已终态，不可取消")

// CancelTask 取消任务（HTTP POST /tasks/{id}/cancel 与管理面共用）。
// 仅非终态可取消：submitted 未投递直接撤回（移出信箱）；offered/acknowledged/
// running 另发 task_cancel 通知 node（node 幂等，已终态/未知回 task_cancelled
// ack）。事务内先收敛 hub 状态，发布后尽力送达取消帧（node 离线发送失败仅记
// 日志——状态已终态，node 迟到结果按冲突行保留 cancelled 先到，进程终止缺口
// 由 node 侧超时兜底）。返回取消后的任务与幂等命中标记（此前已是 cancelled）。
func (c *Core) CancelTask(taskID, reason string) (protocol.Task, bool, error) {
	var (
		out         protocol.Task
		dup         bool
		cancelFence int64 // 该任务当前落库租约（cancel_fence，P1-2）：node 取消回执对账基准
	)
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[taskID]
		if !ok {
			return ErrTaskNotFound
		}
		if t.Status.Terminal() {
			if t.Status == protocol.StatusCancelled {
				dup = true // 重复取消：幂等命中，返回现状
				out = t.Clone()
				return nil
			}
			return ErrNotCancellable
		}
		nt := t.Clone()
		nt.Status = protocol.StatusCancelled
		nt.UpdatedAt = c.now()
		// Result 留空：node 的 task_result(cancelled) 回传真实执行数据后由
		// 收敛表 cancelled 分支合并（见 handleTaskResult）。
		st.Tasks[nt.ID] = nt
		releaseWorkdirLock(st, nt.TargetNode, nt.ID) // 终态释放 workdir 互斥（P1.5）
		if t.Status == protocol.StatusSubmitted {
			mailbox.Remove(st, nt.TargetNode, nt.ID) // 未投递直接撤回，释放信箱配额
		}
		out = nt
		if t.Lease != nil {
			cancelFence = t.Lease.FenceEpoch
		}
		return nil
	})
	if err != nil {
		return protocol.Task{}, false, err
	}
	if dup {
		return out, true, nil
	}
	c.noteTask(&out)
	// task_cancel 尽力送达（submitted 撤回路径也发：ack 超时回退的任务可能已
	// 在 node 手中，迟到执行由 node 侧取消与 hub 冲突收敛共同兜底）。P1-2：帧
	// 携该任务当前落库租约（cancel_fence）——重派推进租约而 node 未及重臂时
	//（如 ack 丢后重派、重派 offer 送达失败留 submitted），node 对账取大者回
	// cancelled 回执，hub 按 cancel_fence ≥ 落库租约宽校验接受，取消结果不滞留。
	if err := c.sendFrameFence(out.TargetNode, protocol.MsgTaskCancel, "",
		cancelFence, protocol.TaskCancelPayload{TaskID: out.ID, Reason: reason}, nil); err != nil {
		log.Printf("[hub] task_cancel 发送失败（node 离线/会话竞态，重连补发携 cancel_fence 重试） node=%s task=%s: %v",
			out.TargetNode, out.ID, err)
	}
	// result_event 不在此发：取消时 Result 恒为 nil（真实执行数据由 node 结果
	// 后到时合并，handleTaskResult cancelled 分支内推送）；node 提交方靠查询补。
	c.fireTerminal(&out) // 群聊回流等终态旁路（cancelled 首次落终态即触发）
	c.dispatch(out.TargetNode)
	return out, false, nil
}

// resendPendingCancels 节点注册成功取消补发（批 B 修复3，task_cancel 至少一次
// 语义）：node 离线期间管理面取消的任务，task_cancel 尽力送达可能失败（CancelTask
// 只记日志，状态已终态不重试）——节点重新注册成功（会话层注册事务发布后回调
// SetOnline 上线）时，对其名下 status=cancelled 且 Result==nil（真实执行数据尚待
// node 回传合并）的任务逐个补发 task_cancel，帧携各自任务当前落库租约
//（cancel_fence，P1-2：node 对账取大者后回执宽校验收敛）。node 侧幂等：在跑则
// 终止执行，已终态/不在账本回 task_cancelled ack；已合并结果（Result 非 nil）的
// 任务无需再补。触发时机对齐注册成功处（session.register → SetOnline(true)），
// membus 测试链路同口径。
func (c *Core) resendPendingCancels(nodeID string) {
	type cancelPing struct {
		id    string
		fence int64 // 该任务当前落库租约（cancel_fence）
	}
	var pings []cancelPing
	for id, t := range c.st.View().Tasks {
		if t.TargetNode == nodeID && t.Status == protocol.StatusCancelled && t.Result == nil {
			var fence int64
			if t.Lease != nil {
				fence = t.Lease.FenceEpoch
			}
			pings = append(pings, cancelPing{id: id, fence: fence})
		}
	}
	if len(pings) == 0 {
		return
	}
	for _, p := range pings {
		if err := c.sendFrameFence(nodeID, protocol.MsgTaskCancel, "",
			p.fence, protocol.TaskCancelPayload{TaskID: p.id, Reason: "hub 重连补发"}, nil); err != nil {
			log.Printf("[hub] 补发 task_cancel 失败（node 已再离线，下轮注册再补） node=%s task=%s: %v",
				nodeID, p.id, err)
		}
	}
	log.Printf("[hub] 节点注册取消补发 node=%s 补发 task_cancel 数量=%d", nodeID, len(pings))
}

// handleTaskCancelledAck node→hub 的 task_cancelled ack：任务在 node 侧已终态或
// 不存在（无可取消对象）。hub 状态在 CancelTask 事务内已收敛，本帧仅观测记录；
// node 已执行完的迟到结果经重发链路到达后按冲突行保留 cancelled 先到。
func (c *Core) handleTaskCancelledAck(env *protocol.Envelope) {
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[hub] task_cancelled 载荷解析失败 from=%s: %v", env.From, err)
		return
	}
	t, ok := c.st.View().Tasks[ref.TaskID]
	if !ok || t.TargetNode != env.From {
		log.Printf("[hub] task_cancelled 未知/归属不符 task=%s from=%s", ref.TaskID, env.From)
		return
	}
	log.Printf("[hub] 取消确认 task=%s node=%s hub_status=%s（node 侧已终态或不在账本）",
		ref.TaskID, env.From, t.Status)
}
