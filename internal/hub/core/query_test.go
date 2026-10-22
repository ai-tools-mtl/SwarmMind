package core

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// task_query/task_submit 的 ReplyTo 关联（Loop 4：node 本地 API 请求-响应匹配）。

func TestTaskQueryReply(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), false)
	ack, err := f.core.Submit(submitReq("nodeB", "echo", "x"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 命中：task_status 应答，ReplyTo=请求 mid
	n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskQuery,
		MessageID: "q-1", To: "hub",
		Payload: mustMarshal(t, protocol.TaskRef{TaskID: ack.TaskID}),
	})
	if len(n.statuses) != 1 || n.statuses[0].ReplyTo != "q-1" {
		t.Fatalf("应收到 1 个 task_status 且 ReplyTo=q-1: %+v", n.statuses)
	}
	var got protocol.Task
	if err := jsonUnmarshal(n.statuses[0].Payload, &got); err != nil || got.ID != ack.TaskID {
		t.Fatalf("task_status 载荷不符: %v %+v", err, got)
	}

	// 查无此任务：err(task_not_found)，ReplyTo 关联
	n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskQuery,
		MessageID: "q-2", To: "hub",
		Payload: mustMarshal(t, protocol.TaskRef{TaskID: "t-missing"}),
	})
	if len(n.errs) == 0 || n.errs[len(n.errs)-1].Code != protocol.CodeTaskNotFound {
		t.Fatalf("应回 err(task_not_found): %+v", n.errs)
	}
	if n.errReplyTo[len(n.errReplyTo)-1] != "q-2" {
		t.Fatalf("err 帧 ReplyTo 应为 q-2: %q", n.errReplyTo[len(n.errReplyTo)-1])
	}
}

func TestSubmitFrameReplyTo(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), false)

	// 成功路径：submit_ack.ReplyTo=请求 mid
	n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskSubmit,
		MessageID: "s-1", To: "hub",
		Payload: mustMarshal(t, submitReq("nodeB", "echo", "x")),
	})
	if len(n.submitAcks) != 1 || n.submitAckReplyTo[0] != "s-1" || n.submitAcks[0].Dup {
		t.Fatalf("submit_ack 关联不符: acks=%+v replyTo=%v", n.submitAcks, n.submitAckReplyTo)
	}

	// 拒绝路径（本机目标未知 agent）：err 帧 ReplyTo=请求 mid
	n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskSubmit,
		MessageID: "s-2", To: "hub",
		Payload: mustMarshal(t, submitReq("nodeB", "ghost", "x")),
	})
	if len(n.errs) == 0 || n.errs[len(n.errs)-1].Code != protocol.CodeAgentNotExposed {
		t.Fatalf("应回 err(agent_not_exposed): %+v", n.errs)
	}
	if n.errReplyTo[len(n.errReplyTo)-1] != "s-2" {
		t.Fatalf("err 帧 ReplyTo 应为 s-2: %q", n.errReplyTo[len(n.errReplyTo)-1])
	}
}

// TestSubmitFrameCrossNodeRejected 边界锁（方案 v2 D9）：node 会话 task_submit
// 目标为其他节点 → 拒绝（invalid），不建任务、不发 offer、无 submit_ack。
func TestSubmitFrameCrossNodeRejected(t *testing.T) {
	f := newHub(t)
	nodeB := f.newNode("nodeB", agentsOf("echo"), false)
	f.newNode("nodeA", agentsOf("echo"), false) // 合法远端目标，照样不可派

	nodeB.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskSubmit,
		MessageID: "s-x", To: "hub",
		Payload: mustMarshal(t, submitReq("nodeA", "echo", "cross")),
	})
	if len(nodeB.errs) != 1 || nodeB.errs[0].Code != "invalid" {
		t.Fatalf("跨节点 task_submit 应回 err(invalid): %+v", nodeB.errs)
	}
	if nodeB.errReplyTo[len(nodeB.errReplyTo)-1] != "s-x" {
		t.Fatalf("err 帧 ReplyTo 应为 s-x: %q", nodeB.errReplyTo[len(nodeB.errReplyTo)-1])
	}
	if len(nodeB.submitAcks) != 0 {
		t.Fatalf("违规提交不得有 submit_ack: %+v", nodeB.submitAcks)
	}
	if len(f.st.View().Tasks) != 0 {
		t.Fatalf("违规提交不得建任务: %+v", f.st.View().Tasks)
	}

	// Submit 直调同口径拒绝（node 会话路径与帧路径共用 submit 校验链）
	if _, err := f.core.Submit(submitReq("nodeA", "echo", "cross"), "nodeB"); err == nil {
		t.Fatal("node 会话跨节点提交应被拒绝")
	}
	// HTTP 管理面（submitterNode 为空）不受此锁限制：全网派单能力唯一在 hub
	if _, err := f.core.Submit(submitReq("nodeA", "echo", "via-http"), ""); err != nil {
		t.Fatalf("hub HTTP 管理面向 nodeA 派单应放行: %v", err)
	}
}

// TestTaskQueryCrossNodeRejected 边界锁（方案 v2 D10）：task_query 只能查本机
// 任务——他节点任务拒绝（forbidden），本机任务照常补查。
func TestTaskQueryCrossNodeRejected(t *testing.T) {
	f := newHub(t)
	nodeA := f.newNode("nodeA", agentsOf("echo"), false)
	nodeB := f.newNode("nodeB", agentsOf("echo"), false)

	// nodeA 名下任务（HTTP 管理面提交）
	ack, err := f.core.Submit(submitReq("nodeA", "echo", "own"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 他节点（nodeB）查询 → err(forbidden)
	nodeB.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskQuery,
		MessageID: "q-x", To: "hub",
		Payload: mustMarshal(t, protocol.TaskRef{TaskID: ack.TaskID}),
	})
	if len(nodeB.errs) != 1 || nodeB.errs[0].Code != "forbidden" {
		t.Fatalf("他节点任务查询应回 err(forbidden): %+v", nodeB.errs)
	}
	if nodeB.errReplyTo[len(nodeB.errReplyTo)-1] != "q-x" {
		t.Fatalf("err 帧 ReplyTo 应为 q-x: %q", nodeB.errReplyTo[len(nodeB.errReplyTo)-1])
	}
	if len(nodeB.statuses) != 0 {
		t.Fatalf("违规查询不得回 task_status: %+v", nodeB.statuses)
	}

	// 本机（nodeA）查询同名任务 → task_status 照常
	nodeA.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskQuery,
		MessageID: "q-ok", To: "hub",
		Payload: mustMarshal(t, protocol.TaskRef{TaskID: ack.TaskID}),
	})
	if len(nodeA.statuses) != 1 || nodeA.statuses[0].ReplyTo != "q-ok" {
		t.Fatalf("本机任务补查应回 task_status: %+v", nodeA.statuses)
	}
}
