// 依赖解析（P4④第二子轮 / FILE-COLLAB L2 跨机取回，SPEC 4.12）：下游 node 的
// 产物挂载准备阶段，本机账本无依赖 idem 的 completed 上游时经 dep_resolve 帧
// 向 hub 请求上游定位与产物清单。hub 按 resolveIDem 同口径（最早 completed）
// 解析，并做依赖归属鉴权——请求方 node 名下必须存在依赖该 idem 的任务，防止
// node 会话借道刺探任意任务（与 task_query 的本机边界锁同一安全考量，D10）。
// 应答为脱敏 Task：只含定位与清单所需字段（ID/TargetNode/TargetAgent/IdemKey/
// Status/CreatedAt + Result{Status, Artifacts}），上游任务输入等隐私字段不出
// hub（node 会话不经历管理面的全量可见）。
package core

import (
	"encoding/json"
	"log"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// handleDepResolve node→hub 依赖解析：{idem} → task_status（脱敏上游 Task）或
// err。ReplyTo=请求 mid 供请求方关联。失败语义（请求方按码分类）：
//   - forbidden：请求方名下无依赖该 idem 的任务（越权，永久）；
//   - task_not_found：idem 无 completed 上游（不存在/已随终态清理/上游未完成
//     ——门控放行后不会出现未完成，多为已清理，永久）。
func (c *Core) handleDepResolve(env *protocol.Envelope) {
	var p protocol.DepResolvePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.Idem == "" {
		log.Printf("[hub] dep_resolve 载荷解析失败 from=%s: %v", env.From, err)
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "invalid", Msg: "dep_resolve 载荷非法"})
		return
	}
	st := c.st.View()
	if !nodeDependsOnIDem(st, env.From, p.Idem) {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeForbidden, Msg: "本 node 名下无依赖该 idem 的任务（越权拒绝）"})
		return
	}
	state, id := resolveIDem(st, p.Idem)
	if state != idemSatisfied {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeTaskNotFound, Msg: "依赖 idem 无 completed 上游（不存在或已随终态清理）"})
		return
	}
	t, ok := c.GetTask(id)
	if !ok || t.Result == nil {
		// resolveIDem 与 GetTask 之间的快照差（清理竞态）：按不存在收敛，请求方
		// 重试将得到稳定的 not_found。
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeTaskNotFound, Msg: "上游任务已清理"})
		return
	}
	_ = c.sendFrame(env.From, protocol.MsgTaskStatus, env.MessageID, redactUpstreamTask(t), nil)
}

// nodeDependsOnIDem 依赖归属鉴权：node 名下（TargetNode==nodeID）是否存在
// DependsOn 含该 idem 的任务。产物挂载与取回均为依赖的衍生权限。
func nodeDependsOnIDem(st *store.State, nodeID, idem string) bool {
	for _, t := range st.Tasks {
		if t.TargetNode == nodeID && depContains(t.DependsOn, idem) {
			return true
		}
	}
	return false
}

// redactUpstreamTask 上游任务的脱敏视图：定位与清单所需字段之外全部剥离
//（Input/SubmittedBy 等内容隐私不出 hub；node 会话边界，D10 同源）。
func redactUpstreamTask(t protocol.Task) protocol.Task {
	out := protocol.Task{
		ID:          t.ID,
		TraceID:     t.TraceID,
		TargetNode:  t.TargetNode,
		TargetAgent: t.TargetAgent,
		IdemKey:     t.IdemKey,
		Status:      t.Status,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		ExpireAt:    t.ExpireAt,
	}
	if t.Result != nil {
		out.Result = &protocol.TaskResult{
			Status:     t.Result.Status,
			ExitCode:   t.Result.ExitCode,
			Artifacts:  append([]protocol.Artifact(nil), t.Result.Artifacts...),
			FinishedAt: t.Result.FinishedAt,
		}
	}
	return out
}
