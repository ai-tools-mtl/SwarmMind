package core

import (
	"log"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// Sweep 状态清扫（后台周期或测试手动驱动），单写事务内一次完成五个阶段：
//  1. offer 超时回退：offered 超 OfferAckTimeout 未收 task_ack → submitted 回队首
//     补推；任务已过期则直接进入过期分支（SPEC 4.3：回退 vs 过期取先发生者）；
//  2. expire 清扫：非终态任务过 ExpireAt → failed/error=expired（提交方为 node
//     时随事务发布发 result_event；此后 node 结果到达按冲突行处理）；
//  3. 排队超时清扫（P1.5/SPEC 4.10）：submitted 任务因 workdir 互斥阻塞排队超
//     WorkdirQueueWait → failed/error=workdir_busy_timeout（排队不计入 agent
//     执行超时；离线/窗口满滞留不适用，维持 7d expire；与 expire 同过期时
//     expire 优先）；
//  4. 依赖门控兜底复评（P4.3/SPEC 4.12）：blocked 的 submitted 任务全量复评——
//     防御终态事务处的事件丢失（理论上单事务原子复评无丢失窗口，兜底未知路径）；
//     解除阻塞即随本轮 dispatch；
//  5. 终态清理：终态超 TerminalRetention（24h）从快照删除（idem 索引派生自任务，
//     同步失效——SPEC 4.5「与任务同生命周期清理」）。
//
// 置终态的任务随事务释放 workdir 互斥锁（P1.5）。发布后对本会话内受影响节点
// 逐一补推（在线才有动作）。
func (c *Core) Sweep() {
	now := c.now()
	var (
		terminalized []*protocol.Task // 本次置为 failed 终态的任务（发 result_event）
		reDispatch   = map[string]bool{}
		removed      []string
		depChanged   []*protocol.Task // 依赖门控复评改动任务（发布后记账，P4.3）
	)
	// mergeDepRev 汇总一处复评结果（清扫事务内多处置终态均触发下游复评）。
	mergeDepRev := func(rv *depReview) {
		if rv == nil {
			return
		}
		depChanged = append(depChanged, rv.changed...)
		for nodeID := range rv.unlocked {
			reDispatch[nodeID] = true
		}
	}
	err := c.st.Update(func(st *store.State) error {
		ids := make([]string, 0, len(st.Tasks))
		for id := range st.Tasks {
			ids = append(ids, id)
		}
		for _, id := range ids {
			t := st.Tasks[id]
			switch {
			case t.Status == protocol.StatusOffered &&
				now.Sub(t.UpdatedAt) >= protocol.OfferAckTimeout:
				// offer 超时：过期优先，否则回退 submitted 重推（互斥锁随回退保留）
				if !t.ExpireAt.IsZero() && !t.ExpireAt.After(now) {
					nt := c.expireCopy(t, now)
					st.Tasks[nt.ID] = nt
					releaseWorkdirLock(st, nt.TargetNode, nt.ID)
					mailbox.Remove(st, nt.TargetNode, nt.ID)
					terminalized = append(terminalized, &nt)
					reDispatch[nt.TargetNode] = true
					mergeDepRev(reevaluateDependents(st, nt.IdemKey)) // expired 终态同口径复评下游
				} else {
					nt := t.Clone()
					nt.Status = protocol.StatusSubmitted
					nt.UpdatedAt = now
					st.Tasks[nt.ID] = nt
					mailbox.RequeueFront(st, nt.TargetNode, &nt) // 配额满仍回队（重投预留）
					reDispatch[nt.TargetNode] = true
				}
			case !t.Status.Terminal() && !t.ExpireAt.IsZero() && !t.ExpireAt.After(now):
				// expire 清扫：非终态任意状态 → failed/expired（优先于排队超时）
				nt := c.expireCopy(t, now)
				st.Tasks[nt.ID] = nt
				releaseWorkdirLock(st, nt.TargetNode, nt.ID)
				mailbox.Remove(st, nt.TargetNode, nt.ID)
				terminalized = append(terminalized, &nt)
				reDispatch[nt.TargetNode] = true
				mergeDepRev(reevaluateDependents(st, nt.IdemKey)) // expired 终态同口径复评下游
			case t.Status == protocol.StatusSubmitted && t.BlockedReason == "" && !t.CreatedAt.IsZero() &&
				now.Sub(t.CreatedAt) >= protocol.WorkdirQueueWait && workdirBusyFor(st, &t):
				// 排队超时（P1.5）：workdir 互斥阻塞使派发迟迟未发生 →
				// failed/workdir_busy_timeout，尽快反馈提交方。只对互斥阻塞的
				// 排队生效——离线/窗口满滞留维持 7d expire 语义不变；同过期时
				// expire 优先（上一 case 已覆盖）；依赖门控阻塞（P4.3）不适用
				// 本分支——blocked 任务等的是上游终态而非 workdir，不走
				// workdir_busy_timeout（下一个 case 兜底复评）
				nt := c.busyTimeoutCopy(t, now)
				st.Tasks[nt.ID] = nt
				releaseWorkdirLock(st, nt.TargetNode, nt.ID)
				mailbox.Remove(st, nt.TargetNode, nt.ID)
				terminalized = append(terminalized, &nt)
				reDispatch[nt.TargetNode] = true
				mergeDepRev(reevaluateDependents(st, nt.IdemKey)) // 终态同口径复评下游
			case t.Status == protocol.StatusSubmitted && t.BlockedReason != "":
				// 依赖门控兜底复评（P4.3）：blocked 任务不占窗口/不占互斥，
				// 上面的排队超时分支不会命中（workdirBusyFor 恒 false）；此处
				// 复评其依赖集合，解除阻塞/更新标注。过期分支优先——7d expire
				// 语义对 blocked 任务不变
				d := t
				changed, unlocked := reevaluateTask(st, &d)
				if changed {
					st.Tasks[d.ID] = d
					cp := d
					depChanged = append(depChanged, &cp)
					if unlocked {
						reDispatch[d.TargetNode] = true
					}
				}
			case t.Status.Terminal() &&
				now.Sub(t.UpdatedAt) >= protocol.TerminalRetention:
				// 终态保留 24h 后从快照清理（idem 派生索引同步失效）
				delete(st.Tasks, id)
				releaseWorkdirLock(st, t.TargetNode, id) // 防御（不变量下不命中）
				mailbox.Remove(st, t.TargetNode, id)     // 防御（不变量下不命中）
				removed = append(removed, id)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[hub] 清扫事务失败: %v", err)
		return
	}
	for _, t := range terminalized {
		c.noteTask(t)
		c.notifyResultEvent(t)
		c.fireTerminal(t) // 清扫置终态同样走终态旁路（expire/排队超时回流群）
	}
	for _, t := range depChanged {
		c.noteTask(t)
	}
	for _, id := range removed {
		c.noteDelete(id)
	}
	for nodeID := range reDispatch {
		c.dispatch(nodeID)
	}
}

// expireCopy 构造过期终态副本（failed / error=expired，SPEC 4.3/4.6）。
func (c *Core) expireCopy(t protocol.Task, now time.Time) protocol.Task {
	nt := t.Clone()
	nt.Status = protocol.StatusFailed
	nt.Result = &protocol.TaskResult{
		Status:     protocol.ResultFailed,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeExpired,
		FinishedAt: now,
	}
	nt.UpdatedAt = now
	return nt
}

// busyTimeoutCopy 构造排队超时终态副本（failed / error=workdir_busy_timeout，
// P1.5：workdir 互斥阻塞下排队超 WorkdirQueueWait；进程未运行，ExitCode=NotRun）。
func (c *Core) busyTimeoutCopy(t protocol.Task, now time.Time) protocol.Task {
	nt := t.Clone()
	nt.Status = protocol.StatusFailed
	nt.Result = &protocol.TaskResult{
		Status:     protocol.ResultFailed,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeWorkdirBusyTimeout,
		FinishedAt: now,
	}
	nt.UpdatedAt = now
	return nt
}
