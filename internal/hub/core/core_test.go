package core

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// fakeClock 可手动推进的时钟（membus 同步链路 + SweepInterval=0 下全单线程，
// 无数据竞争；mutex 防御未来并发用法）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// hubFixture hub 侧测试基座：bus + store + core（后台清扫关闭，Sweep 手动驱动）。
type hubFixture struct {
	t     *testing.T
	bus   *transport.Bus
	hubEP *transport.Endpoint
	st    *store.Store
	core  *Core
	clock *fakeClock
}

func newHub(t *testing.T) *hubFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	core, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: core, clock: clock}
	f.hubEP.Subscribe(core.HandleFrame)
	t.Cleanup(func() {
		_ = core.Close()
		_ = st.Close()
	})
	return f
}

// testNode 受控 node 桩：记录收帧；autoAck 时收到 offer 即回 task_ack
// （不 started，便于控制投递窗口占用），其余动作由测试显式驱动。
type testNode struct {
	t  *testing.T
	ep *transport.Endpoint

	offers      []protocol.Task
	submitAcks  []protocol.SubmitAckPayload
	resultAcks  []string
	resultEvent []protocol.TaskResultPayload
	errs        []protocol.ErrInfo
	autoAck     bool

	// ReplyTo 关联记录（Loop 4：本地请求-应答匹配断言用）。
	submitAckReplyTo []string
	errReplyTo       []string
	statuses         []*protocol.Envelope // 原始 task_status 帧
}

// newNode 注册并上线一个受控 node。
func (f *hubFixture) newNode(name string, agents []protocol.AgentCard, autoAck bool) *testNode {
	f.t.Helper()
	if err := f.core.RegisterNode(name, agents); err != nil {
		f.t.Fatalf("注册 node %s 失败: %v", name, err)
	}
	n := &testNode{t: f.t, ep: f.bus.Endpoint(name), autoAck: autoAck}
	n.ep.Subscribe(n.handle)
	f.core.SetOnline(name, true)
	return n
}

// registerOffline 仅注册不上线（离线信箱场景）。
func (f *hubFixture) registerOffline(name string, agents []protocol.AgentCard) {
	f.t.Helper()
	if err := f.core.RegisterNode(name, agents); err != nil {
		f.t.Fatalf("注册 node %s 失败: %v", name, err)
	}
	f.core.SetOnline(name, false)
}

func (n *testNode) handle(env *protocol.Envelope) {
	switch env.Type {
	case protocol.MsgTaskOffer:
		var task protocol.Task
		if err := json.Unmarshal(env.Payload, &task); err != nil {
			n.t.Errorf("offer 载荷解析失败: %v", err)
			return
		}
		n.offers = append(n.offers, task)
		if n.autoAck {
			n.ack(task.ID)
		}
	case protocol.MsgSubmitAck:
		var p protocol.SubmitAckPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			n.t.Errorf("submit_ack 载荷解析失败: %v", err)
			return
		}
		n.submitAcks = append(n.submitAcks, p)
		n.submitAckReplyTo = append(n.submitAckReplyTo, env.ReplyTo)
	case protocol.MsgTaskStatus:
		cp := *env
		n.statuses = append(n.statuses, &cp)
	case protocol.MsgResultAck:
		var p protocol.TaskRef
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			n.t.Errorf("result_ack 载荷解析失败: %v", err)
			return
		}
		n.resultAcks = append(n.resultAcks, p.TaskID)
	case protocol.MsgResultEvent:
		var p protocol.TaskResultPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			n.t.Errorf("result_event 载荷解析失败: %v", err)
			return
		}
		n.resultEvent = append(n.resultEvent, p)
	case protocol.MsgErr:
		if env.Err != nil {
			n.errs = append(n.errs, *env.Err)
			n.errReplyTo = append(n.errReplyTo, env.ReplyTo)
		}
	}
}

func (n *testNode) send(typ string, payload any) {
	n.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		n.t.Fatalf("marshal %s: %v", typ, err)
	}
	if err := n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: typ,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: b,
	}); err != nil {
		n.t.Fatalf("发送 %s 失败: %v", typ, err)
	}
}

func (n *testNode) ack(taskID string) { n.send(protocol.MsgTaskAck, protocol.TaskRef{TaskID: taskID}) }
func (n *testNode) start(taskID string) {
	n.send(protocol.MsgTaskStarted, protocol.TaskRef{TaskID: taskID})
}
func (n *testNode) result(taskID string, res protocol.TaskResult) {
	n.send(protocol.MsgTaskResult, protocol.TaskResultPayload{
		TaskID: taskID, Status: res.Status, Result: &res,
	})
}

// agentsOf 构造 exposed=true 的能力表；指定 "-" 后缀的名字注册为 exposed=false。
func agentsOf(names ...string) []protocol.AgentCard {
	out := make([]protocol.AgentCard, 0, len(names))
	for _, name := range names {
		exposed := true
		if len(name) > 1 && name[len(name)-1] == '-' {
			exposed = false
			name = name[:len(name)-1]
		}
		out = append(out, protocol.AgentCard{
			Name: name, Kind: protocol.AgentKindCLI,
			InputMode: "prompt_text", OutputMode: "answer+artifacts",
			PermissionMode: "no-shell", ProtocolVer: 1, Exposed: exposed,
		})
	}
	return out
}

// submitReq 构造提交请求（每次新 Input，避免用例间共享底层数组）。
func submitReq(node, agent string, args ...string) protocol.TaskSubmitRequest {
	return protocol.TaskSubmitRequest{
		TargetNode: node, TargetAgent: agent,
		Input:       protocol.TaskInput{Args: append([]string(nil), args...)},
		SubmittedBy: "tester",
	}
}

// taskOf 从 store 取任务副本（断言辅助）。
func (f *hubFixture) taskOf(id string) protocol.Task {
	f.t.Helper()
	t, ok := f.core.GetTask(id)
	if !ok {
		f.t.Fatalf("任务 %s 不存在", id)
	}
	return t
}
