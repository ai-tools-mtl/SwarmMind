package conn

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// TestBackoffSequence 退避计算（SPEC 4.6：0.5s 起 ×2 ±20% 抖动上限 60s；
// 注册成功重置）：逐级倍增、抖动边界、封顶、重置。
func TestBackoffSequence(t *testing.T) {
	b := NewBackoff(0, 0, 0) // 协议常量
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	for i, base := range want {
		got := b.Next()
		lo := time.Duration(float64(base) * 0.79) // 抖动 ±20%，留边界余量
		hi := time.Duration(float64(base) * 1.21)
		if got < lo || got > hi {
			t.Fatalf("第 %d 次退避应在 [%s,%s], got %s", i+1, lo, hi, got)
		}
	}
	// 封顶：多次 Next 后不超过 60s×1.2
	for i := 0; i < 20; i++ {
		if got := b.Next(); got > 72*time.Second {
			t.Fatalf("退避应封顶 60s±抖动, got %s", got)
		}
	}
	b.Reset()
	if got := b.Next(); got > 600*time.Millisecond || got < 400*time.Millisecond {
		t.Fatalf("Reset 后应回到 0.5s±抖动, got %s", got)
	}
}

// TestAuthFailedNoRetry auth_failed 不重试（SPEC 4.6）：拒绝应答后 Run 立即返回
// ErrAuthFailed，且不再发起第二次注册。
func TestAuthFailedNoRetry(t *testing.T) {
	var registers atomic.Int64
	ready := make(chan struct{})
	url := newFakeHub(t, func(conn *websocket.Conn) {
		registers.Add(1)
		close(ready)
		// 回拒绝应答
		writeFrame(t, conn, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: false, Reason: protocol.CodeAuthFailed}),
		})
		time.Sleep(500 * time.Millisecond) // 保持连接片刻，观察是否重连
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "bad"})
	start := time.Now()
	err := c.Run(context.Background())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("应返回 ErrAuthFailed, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("auth_failed 应立即返回, took %s", elapsed)
	}
	<-ready
	time.Sleep(300 * time.Millisecond) // 若有重试，第二次注册应已发生
	if got := registers.Load(); got != 1 {
		t.Fatalf("auth_failed 不得重试, registers=%d", got)
	}
}

// TestSendBeforeConnect 未连接时 Send 报端点不存在（与 membus 语义对齐），
// 最近同步时间不可得。
func TestSendBeforeConnect(t *testing.T) {
	c := NewClient(Config{HubURL: "ws://127.0.0.1:1/ws/node", NodeID: "n1", Token: "t"})
	err := c.Send("hub", &protocol.Envelope{Type: protocol.MsgTaskAck})
	if !errors.Is(err, transport.ErrNoEndpoint) {
		t.Fatalf("应 ErrNoEndpoint, got %v", err)
	}
	err = c.Send("other", &protocol.Envelope{})
	if !errors.Is(err, transport.ErrNoEndpoint) {
		t.Fatalf("非 hub 目标应 ErrNoEndpoint, got %v", err)
	}
	if _, ok := c.LastRegisteredAt(); ok {
		t.Fatal("未注册不得有最近同步时间")
	}
}

// TestRegisterAckEpochAndSyncTime 注册成功记录 epoch 与最近同步时间；
// ctx 取消后 Run 返回 nil（正常停止重连循环）。
func TestRegisterAckEpochAndSyncTime(t *testing.T) {
	url := newFakeHub(t, func(conn *websocket.Conn) {
		env := readRegister(t, conn)
		writeFrame(t, conn, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 7}),
		})
		_ = env
		time.Sleep(10 * time.Second) // 悬挂直至测试结束断开
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("应注册成功并暴露最近同步时间")
	}
	if c.Epoch() != 7 {
		t.Fatalf("epoch 应为 7, got %d", c.Epoch())
	}
	ts, ok := c.LastRegisteredAt()
	if !ok || time.Since(ts) > 3*time.Second {
		t.Fatalf("最近同步时间异常: %v %v", ts, ok)
	}
	// 已连接时 Send 可入队（不报端点不存在）
	if err := c.Send("hub", &protocol.Envelope{Type: protocol.MsgTaskAck}); err != nil {
		t.Fatalf("已连接 Send 不应报错, got %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ctx 取消后 Run 应返回 nil, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}

// TestRunStopsAfterClose Close 后 Run 立即返回（不进入重连循环）。
func TestRunStopsAfterClose(t *testing.T) {
	url := newFakeHub(t, func(conn *websocket.Conn) {
		readRegister(t, conn)
		writeFrame(t, conn, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		})
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("应注册成功")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close 后 Run 应返回 nil, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 Close 退出")
	}
}

// ---------- 假 hub（httptest 同型：websocket.Accept，可注入注册期行为） ----------

// newFakeHub 启动假 hub，返回 ws 入口地址（127.0.0.1 随机空闲端口）。
func newFakeHub(t *testing.T, onConn func(conn *websocket.Conn)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/node", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		onConn(conn)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "ws://" + ln.Addr().String() + "/ws/node"
}

func writeFrame(t *testing.T, conn *websocket.Conn, env *protocol.Envelope) {
	t.Helper()
	b, err := protocol.Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// readRegister 读取一帧并校验为 register（返回载荷供断言）。
func readRegister(t *testing.T, conn *websocket.Conn) protocol.RegisterPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatal("非文本帧")
	}
	env, err := protocol.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.MsgRegister {
		t.Fatalf("期待 register, got %s", env.Type)
	}
	var p protocol.RegisterPayload
	if err := jsonUnmarshalP(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustJSONP(v any) []byte {
	b, err := jsonMarshalP(v)
	if err != nil {
		panic(err)
	}
	return b
}

func jsonMarshalP(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshalP(b []byte, v any) error { return json.Unmarshal(b, v) }

func waitForC(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
