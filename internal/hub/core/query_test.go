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

	// 拒绝路径（目标不存在）：err 帧 ReplyTo=请求 mid
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
