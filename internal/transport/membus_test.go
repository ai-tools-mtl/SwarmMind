package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestEndpointSyncDispatch 同步分发：Send 在调用方 goroutine 内执行对端 Handler，
// 且 From 被强制覆盖为发送端身份（不信帧内值）。
func TestEndpointSyncDispatch(t *testing.T) {
	bus := NewBus()
	a := bus.Endpoint("node-a")
	b := bus.Endpoint("node-b")

	var gotFrom, gotTo string
	var sameGoroutine = make(chan struct{})
	b.Subscribe(func(env *protocol.Envelope) {
		gotFrom, gotTo = env.From, env.To
		close(sameGoroutine)
	})

	payload, _ := json.Marshal(protocol.TaskRef{TaskID: "t-1"})
	err := a.Send("node-b", &protocol.Envelope{
		Version: protocol.ProtocolVersion,
		Type:    protocol.MsgPing,
		From:    "forged-identity", // 伪造身份必须被传输层覆盖
		To:      "node-b",
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	select {
	case <-sameGoroutine:
	default:
		t.Error("Handler 未在 Send 调用方 goroutine 内同步执行")
	}
	if gotFrom != "node-a" {
		t.Errorf("From 未被强制覆盖: got %q", gotFrom)
	}
	if gotTo != "node-b" {
		t.Errorf("To 不符: got %q", gotTo)
	}
}

// TestSendErrors 目标不存在、本端已关闭、对端已关闭三类错误。
func TestSendErrors(t *testing.T) {
	bus := NewBus()
	a := bus.Endpoint("a")
	zombie := bus.Endpoint("zombie")
	_ = zombie.Close()

	if err := a.Send("ghost", &protocol.Envelope{Type: protocol.MsgPing}); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("无对端应 ErrNoEndpoint, got %v", err)
	}
	if err := a.Send("zombie", &protocol.Envelope{Type: protocol.MsgPing}); !errors.Is(err, ErrClosed) {
		t.Errorf("对端已关闭应 ErrClosed, got %v", err)
	}
	_ = a.Close()
	if err := a.Send("ghost", &protocol.Envelope{Type: protocol.MsgPing}); !errors.Is(err, ErrClosed) {
		t.Errorf("本端已关闭应 ErrClosed, got %v", err)
	}
}

// TestSendFrameTooLarge 超过 1MiB 的帧按最终编码实测拒绝（SPEC 4.6）。
func TestSendFrameTooLarge(t *testing.T) {
	bus := NewBus()
	a, b := bus.Endpoint("a"), bus.Endpoint("b")
	b.Subscribe(func(env *protocol.Envelope) { t.Error("超限帧不应投递") })

	big := bytes.Repeat([]byte("x"), protocol.MaxMessageBytes)
	payload, _ := json.Marshal(map[string]string{"s": string(big)})
	err := a.Send("b", &protocol.Envelope{Type: protocol.MsgPing, Payload: payload})
	if !errors.Is(err, protocol.ErrFrameTooLarge) {
		t.Errorf("超限应 ErrFrameTooLarge, got %v", err)
	}
}

// TestEndpointReuse 同名端点在总线上复用同一实例。
func TestEndpointReuse(t *testing.T) {
	bus := NewBus()
	if bus.Endpoint("hub") != bus.Endpoint("hub") {
		t.Error("同名端点应复用同一实例")
	}
}

// TestReentrantSend 回发可重入：Handler 内向本端回发不阻塞、不重入锁死。
func TestReentrantSend(t *testing.T) {
	bus := NewBus()
	hub, node := bus.Endpoint("hub"), bus.Endpoint("node")

	acked := false
	hub.Subscribe(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgTaskAck {
			acked = true
		}
	})
	node.Subscribe(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgTaskOffer {
			// 接收回调内向对端回发（嵌套 Send，不持锁）
			_ = node.Send("hub", &protocol.Envelope{Type: protocol.MsgTaskAck, To: "hub"})
		}
	})

	if err := hub.Send("node", &protocol.Envelope{Type: protocol.MsgTaskOffer, To: "node"}); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if !acked {
		t.Error("嵌套回发的 task_ack 未送达")
	}
}
