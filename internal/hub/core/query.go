package core

import (
	"encoding/json"
	"log"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// handleTaskQuery node 补查路径（SPEC 4.2：task_query → task_status，result_event
// 离线错过的补查）。边界锁（方案 v2 D10）：node 会话只能查询本机任务——
// Task.TargetNode == env.From，他节点任务拒绝（forbidden）；任务真相源仍是 hub
// 存储，管理面 GET /tasks/{id}（管理员全量可见）不经此路径。查无此任务回
// err(task_not_found)。ReplyTo=请求 mid 供提交方关联。
func (c *Core) handleTaskQuery(env *protocol.Envelope) {
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[hub] task_query 载荷解析失败 from=%s: %v", env.From, err)
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "invalid", Msg: "task_query 载荷非法"})
		return
	}
	t, ok := c.GetTask(ref.TaskID)
	if !ok {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeTaskNotFound, Msg: "任务不存在或已过保留期"})
		return
	}
	if t.TargetNode != env.From {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "forbidden", Msg: "仅可查询本机任务（跨机任务状态经 hub 发起查询）"})
		return
	}
	_ = c.sendFrame(env.From, protocol.MsgTaskStatus, env.MessageID, t, nil)
}
