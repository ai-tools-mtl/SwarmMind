package conn

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- C3：重连成功事件触发立即补传（真实 Client+service+ledger+runner） ----------

// resends 录制收到的 task_result 帧。
type resends struct {
	mu     sync.Mutex
	frames []*protocol.Envelope
}

func (r *resends) add(env *protocol.Envelope) {
	r.mu.Lock()
	cp := *env
	r.frames = append(r.frames, &cp)
	r.mu.Unlock()
}

func (r *resends) all() []*protocol.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*protocol.Envelope(nil), r.frames...)
}

// TestResendOnReconnectRegisteredEvent C3/SPEC 4.5（生产装配同构）：假 hub 收到
// 首次 task_result 后不回 result_ack 并断开连接；Client 重连注册成功事件经
// OnRegistered→补传（不调用 ResendPending），断言远小于 60s 周期内以新 epoch
// 补传，result_ack 后账本闭合。
func TestResendOnReconnectRegisteredEvent(t *testing.T) {
	var conns atomic.Int64
	firstClosed := make(chan struct{})
	var closeOnce sync.Once
	rec := &resends{}
	registered := make(chan struct{}) // 首次注册成功（第二次连接等待其先行）

	url := newFakeHub(t, func(ws *websocket.Conn) {
		ws.SetReadLimit(protocol.MaxMessageBytes + readLimitSlack)
		n := conns.Add(1)
		// 读 register → 回 ack（epoch=连接序号）
		reg := readRegister(t, ws)
		_ = reg
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: n}),
		})
		if n == 1 {
			close(registered)
			// 下发任务 offer
			task := protocol.Task{
				ID: "t-c3", TraceID: "tr", TargetNode: "n1", TargetAgent: "echo",
				Input: protocol.TaskInput{Args: []string{"hello", "c3"}}, SubmittedBy: "hub",
			}
			b, _ := json.Marshal(task)
			writeFrame(t, ws, &protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
				MessageID: protocol.NewMessageID(), From: "hub",
				SessionEpoch: 1, Payload: b,
			})
			// 读帧：回 pong、收 task_result——收到首个 result 即断开（不回 result_ack）
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				typ, data, err := ws.Read(ctx)
				cancel()
				if err != nil {
					return
				}
				if typ != websocket.MessageText {
					continue
				}
				env, err := protocol.Decode(data)
				if err != nil {
					continue
				}
				switch env.Type {
				case protocol.MsgPing:
					writeFrame(t, ws, &protocol.Envelope{
						Version: protocol.ProtocolVersion, Type: protocol.MsgPong,
						MessageID: protocol.NewMessageID(), To: "hub",
						SessionEpoch: env.SessionEpoch, ReplyTo: env.MessageID,
					})
				case protocol.MsgTaskAck, protocol.MsgTaskStarted:
					// 记录即可
				case protocol.MsgTaskResult:
					rec.add(env)
					closeOnce.Do(func() { close(firstClosed) })
					return // 断开：模拟 result_ack 丢失 + 连接中断
				}
			}
		}
		// 第二次连接：等首次注册先行（保证 epoch 顺序），随后收补传 result
		<-registered
		deadline := time.Now().Add(10 * time.Second)
		acked := false
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			typ, data, err := ws.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			env, err := protocol.Decode(data)
			if err != nil {
				continue
			}
			switch env.Type {
			case protocol.MsgPing:
				writeFrame(t, ws, &protocol.Envelope{
					Version: protocol.ProtocolVersion, Type: protocol.MsgPong,
					MessageID: protocol.NewMessageID(), To: "hub",
					SessionEpoch: env.SessionEpoch, ReplyTo: env.MessageID,
				})
			case protocol.MsgTaskResult:
				rec.add(env)
				if !acked {
					acked = true
					writeFrame(t, ws, &protocol.Envelope{
						Version: protocol.ProtocolVersion, Type: protocol.MsgResultAck,
						MessageID: protocol.NewMessageID(), To: "hub",
						SessionEpoch: env.SessionEpoch,
						Payload:      mustJSONP(protocol.TaskRef{TaskID: "t-c3"}),
					})
				}
			}
		}
	})

	// 真实 node 装配（与 cmd/node/main.go 同构：OnRegistered→补传闭包）
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	profile := protocol.ExecProfile{
		Name: "echo", Kind: protocol.AgentKindCLI,
		Command: "/bin/echo", Workdir: t.TempDir(),
		TimeoutSec: 5, AllowArgs: true, Exposed: true, PermissionMode: "no-shell",
	}
	runners := map[string]service.Runner{
		"echo": runner.New(profile, map[string]string{"PATH": "/usr/bin:/bin"}, t.TempDir()),
	}
	var svc *service.Service
	client := NewClient(Config{
		HubURL: url, NodeID: "n1", Token: "tok",
		Agents: service.CardsFromProfiles([]protocol.ExecProfile{profile}),
		OnRegistered: func() {
			if svc != nil {
				svc.ResendPendingAsync()
			}
		},
	})
	svc = service.New(service.Config{Ledger: led, Runners: runners}, client)
	t.Cleanup(svc.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	t.Cleanup(func() { _ = client.Close() })

	// 首次结果送达
	select {
	case <-firstClosed:
	case <-time.After(10 * time.Second):
		t.Fatal("首次 result 未送达")
	}
	// 补传应远小于 60s 周期内到达（退避 0.5s + 注册即补传）
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.all()) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	frames := rec.all()
	if len(frames) < 2 {
		t.Fatalf("重连注册事件应触发补传（2 次 result）, got %d", len(frames))
	}
	if got := frames[1].SessionEpoch; got < 2 {
		t.Fatalf("补传应携新 epoch, got %d", got)
	}
	// result_ack 闭合：账本 finished+acked
	if !waitForC(5*time.Second, func() bool {
		e, ok := led.Get("t-c3")
		return ok && e.State == ledger.StateFinished && e.ResultAcked
	}) {
		e, _ := led.Get("t-c3")
		t.Fatalf("账本应 finished+acked, got %+v", e)
	}
	_ = client.Close()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 Close 退出")
	}
}
