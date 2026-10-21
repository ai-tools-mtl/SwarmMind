// workdir 互斥（P1.5/SPEC 4.10）：互斥键 = (node_id, workspace_id)。workspace_id
// 由 node 经 AgentCard 上报（workdir 规范化后的派生哈希，hub 不感知路径）。
// hub 侧互斥表随快照持久化并在重启时按任务集自愈校正；占用从 offered 起算
// （acked 等并发槽期间互斥仍生效，槽位空出不会击穿互斥），终态释放。
// hub 门控是投递侧的准入；真正的执行互斥以 node 侧为准（spawn 前取锁至产物
// 冻结，见 node service）——本表释放与 node 实际释放的一致性由「node 终态结果
// 必然晚于产物冻结回传」保证。
package core

import (
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// workspaceOf 解析任务的 workspace 互斥键：目标 agent 注册卡上的 WorkspaceID
// （node 上报派生键，hub 不感知路径）。卡缺失（注册表变更）或为空（旧版 node
// 未上报）返回 ""——该任务不参与互斥，保持「无 workspace 上报即不受约束」的
// 兼容口径。
func workspaceOf(st *store.State, t protocol.Task) string {
	card, ok := lookupAgent(st, t.TargetNode, t.TargetAgent)
	if !ok {
		return ""
	}
	return card.WorkspaceID
}

// workdirFree 报告任务目标的 (node, workspace_id) 当前可投递（dispatch 的
// eligible 门控判定）：键空闲，或持有者就是本任务——回退任务保留自锁（offer
// 超时/发送失败回 submitted，锁随回退保留），若按他持判定则首次 offer 丢失即
// 永远卡死到过期（P1.5 二轮必修1）；自持允许重投，持有权随重投幂等延续。
func workdirFree(st *store.State, t protocol.Task) bool {
	ws := workspaceOf(st, t)
	if ws == "" {
		return true
	}
	holder := st.WorkdirLocks[t.TargetNode][ws]
	return holder == "" || holder == t.ID
}

// workdirBusyFor 报告任务正被 workdir 互斥阻塞：同键已被「其他任务」占用
// （排队超时的判定依据；被自己占用——回退保留锁的持有者——不算阻塞）。
// workspace 不可解析视为不阻塞。
func workdirBusyFor(st *store.State, t *protocol.Task) bool {
	ws := workspaceOf(st, *t)
	if ws == "" {
		return false
	}
	holder := st.WorkdirLocks[t.TargetNode][ws]
	return holder != "" && holder != t.ID
}

// acquireWorkdirLock 占用任务目标的互斥键（offer 事务内调用；同键重复占用为
// 幂等覆盖，如回退重推）。workspace 不可解析时为 no-op（不参与互斥）。
func acquireWorkdirLock(st *store.State, t *protocol.Task) {
	ws := workspaceOf(st, *t)
	if ws == "" {
		return
	}
	locks := st.WorkdirLocks[t.TargetNode]
	if locks == nil {
		locks = make(map[string]string)
		st.WorkdirLocks[t.TargetNode] = locks
	}
	locks[ws] = t.ID
}

// releaseWorkdirLock 释放该任务占用的互斥键（终态/排队超时等事务内调用）。
// 按值反查删除而非重新解析 workspace——注册卡在任务生命周期内可能变更或消失，
// 反查对键解析不可用的情况健壮。非持有者（含同键被他人接管）为 no-op。
func releaseWorkdirLock(st *store.State, nodeID, taskID string) {
	for ws, id := range st.WorkdirLocks[nodeID] {
		if id == taskID {
			delete(st.WorkdirLocks[nodeID], ws)
		}
	}
}

// rebuildWorkdirLocks hub 重启时互斥表恢复/自愈（单事务，幂等）：
//  1. 释放脏条目——持有者已终态或已不存在（快照与表理论上同事务一致，此处
//     防御手改/旧版本快照）；
//  2. 补占缺失锁——offered/acknowledged/running 的非终态任务未持锁（旧版本
//     快照无表）时按注册卡补占；submitted 不占锁（占用从 offered 起算，回退
//     任务保留其锁直到终态，与运行期语义一致）。
func (c *Core) rebuildWorkdirLocks() {
	_ = c.st.Update(func(st *store.State) error {
		for nodeID, locks := range st.WorkdirLocks {
			for ws, taskID := range locks {
				t, ok := st.Tasks[taskID]
				if !ok || t.Status.Terminal() {
					delete(locks, ws)
				}
			}
			if len(locks) == 0 {
				delete(st.WorkdirLocks, nodeID)
			}
		}
		for _, t := range st.Tasks {
			if t.Status.Terminal() || t.Status == protocol.StatusSubmitted {
				continue
			}
			if st.WorkdirLocks[t.TargetNode][workspaceOf(st, t)] != "" {
				continue // 已持锁（同键持有者以表内为准，不覆盖）
			}
			acquireWorkdirLock(st, &t)
		}
		return nil
	})
}
