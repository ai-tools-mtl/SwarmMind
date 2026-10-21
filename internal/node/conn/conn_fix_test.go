package conn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- L5：队列通知分离与停滞时钟 ----------

// TestQueueBroadcastWakesAllProducers L5：多个阻塞生产者在数据出队后必须全部
// 及时唤醒（代次广播，杜绝单 token 通知丢唤醒）；停滞时长内未唤醒即失败。
func TestQueueBroadcastWakesAllProducers(t *testing.T) {
	q := newOutboundQ()
	defer q.close(false)
	// 占满数据队列（4MiB）
	big := make([]byte, protocol.SendQueueLimit)
	if err := q.pushData(big, time.Second); err != nil {
		t.Fatalf("填充队列: %v", err)
	}
	// 三个生产者阻塞等待空位（短 stall：丢唤醒会在 stall 后报 errDataStall）
	const producers = 3
	res := make(chan error, producers)
	var entered sync.WaitGroup
	entered.Add(producers)
	one := []byte("x")
	for i := 0; i < producers; i++ {
		go func() {
			entered.Done()
			res <- q.pushData(one, 500*time.Millisecond)
		}()
	}
	entered.Wait()
	time.Sleep(50 * time.Millisecond) // 让生产者进入等待（正确性不依赖该时长）
	// 消费者快速排空：一次 pop 释放空位并广播
	if b := q.pop(); b == nil {
		t.Fatal("应能取出大帧")
	}
	for i := 0; i < producers; i++ {
		select {
		case err := <-res:
			if err != nil {
				t.Fatalf("生产者 %d 应被广播唤醒并入队, got %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("生产者 %d 未被唤醒（丢唤醒）", i)
		}
	}
}

// TestQueueStallIgnoresCtrlActivity L5：只有控制帧活动而无数据出队进展时，
// 数据停滞时钟不得被刷新——恰在 stall 时限断开，而非被周期控制帧无限顺延。
func TestQueueStallIgnoresCtrlActivity(t *testing.T) {
	q := newOutboundQ()
	defer q.close(false)
	big := make([]byte, protocol.SendQueueLimit)
	if err := q.pushData(big, time.Second); err != nil {
		t.Fatalf("填充队列: %v", err)
	}
	// 控制帧周期入队（旧实现会 Reset 停滞计时器）
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ping := []byte("p")
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_ = q.pushCtrl(ping)
			}
		}
	}()
	const stall = 300 * time.Millisecond
	start := time.Now()
	err := q.pushData([]byte("y"), stall)
	elapsed := time.Since(start)
	if !errors.Is(err, errDataStall) {
		t.Fatalf("应 errDataStall, got %v", err)
	}
	if elapsed >= stall+250*time.Millisecond {
		t.Fatalf("控制帧活动不得刷新停滞时钟（应在 %s 附近断开）, took %s", stall, elapsed)
	}
}

// ---------- N1：register 帧体积 ----------

// bigAgents 生成指定体积的能力表（单卡 Description 撑体积）。
func bigAgents(size int) []protocol.AgentCard {
	return []protocol.AgentCard{{
		Name: "big", Kind: protocol.AgentKindCLI,
		Description: strings.Repeat("a", size),
		InputMode:   "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}}
}

// TestRegisterLargeButLegalSucceeds N1：约 70KiB 的合法 register（携完整能力表）
// 不受 64KiB 控制小缓冲约束，正常注册。
func TestRegisterLargeButLegalSucceeds(t *testing.T) {
	url := newFakeHub(t, func(ws *websocket.Conn) {
		ws.SetReadLimit(protocol.MaxMessageBytes + readLimitSlack)
		readRegister(t, ws)
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		})
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok", Agents: bigAgents(70 << 10)})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("约 70KiB 的合法 register 应注册成功")
	}
	if c.Epoch() != 1 {
		t.Fatalf("epoch 应为 1, got %d", c.Epoch())
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 Close 退出")
	}
}

// TestRegisterTooLargeFailsFastNoRetry N1：register 编码超 1MiB → 立即返回
// ErrRegisterTooLarge（可诊断配置错误），不重试、不 panic。
func TestRegisterTooLargeFailsFastNoRetry(t *testing.T) {
	var conns atomic.Int64
	ready := make(chan struct{})
	url := newFakeHub(t, func(ws *websocket.Conn) {
		conns.Add(1)
		close(ready)
		time.Sleep(3 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok", Agents: bigAgents(1200 << 10)})
	start := time.Now()
	err := c.Run(t.Context())
	if !errors.Is(err, ErrRegisterTooLarge) {
		t.Fatalf("应返回 ErrRegisterTooLarge, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("应立即返回, took %s", elapsed)
	}
	<-ready
	time.Sleep(300 * time.Millisecond)
	if got := conns.Load(); got != 1 {
		t.Fatalf("本地配置错误不得重试, conns=%d", got)
	}
}

// ---------- N3：关闭与发布窗口 ----------

// tryReadRegister 非致命读 register：连接提前关闭时返回错误而非 Fatal。
func tryReadRegister(ws *websocket.Conn) (protocol.RegisterPayload, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	typ, data, err := ws.Read(ctx)
	if err != nil {
		return protocol.RegisterPayload{}, err
	}
	if typ != websocket.MessageText {
		return protocol.RegisterPayload{}, errors.New("非文本帧")
	}
	env, err := protocol.Decode(data)
	if err != nil {
		return protocol.RegisterPayload{}, err
	}
	var p protocol.RegisterPayload
	if err := jsonUnmarshalP(env.Payload, &p); err != nil {
		return protocol.RegisterPayload{}, err
	}
	return p, nil
}

// TestCloseDuringDialPublishWindow N3：用屏障把 Close 卡在「拨号返回、连接未发布」
// 之间——发布必须复核关闭态，取消后不得再发布可用连接、不得再注册。
func TestCloseDuringDialPublishWindow(t *testing.T) {
	var registers atomic.Int64
	url := newFakeHub(t, func(ws *websocket.Conn) {
		if _, err := tryReadRegister(ws); err != nil {
			return // 连接在注册前被关闭（正是被测窗口）
		}
		registers.Add(1)
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	gate := make(chan struct{})
	c.mu.Lock()
	c.dialGate = gate
	c.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	time.Sleep(200 * time.Millisecond) // Run 进入拨号→屏障
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(gate) // 放行：此刻 Close 已完成，发布必须被拒绝
	select {
	case <-done: // 返回值不限（取消路径为 nil）：关键是及时退出且未注册
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 Close 及时退出（关闭未覆盖未发布连接）")
	}
	if n := registers.Load(); n != 0 {
		t.Fatalf("关闭后不得再注册, registers=%d", n)
	}
	if c.Epoch() != 0 {
		t.Fatalf("不得记录 epoch, got %d", c.Epoch())
	}
}

// TestCloseConcurrentWithRun N3：Run/Close 并发无数据竞争（-race 把关），Close
// 后 Run 有界退出。
func TestCloseConcurrentWithRun(t *testing.T) {
	url := newFakeHub(t, func(ws *websocket.Conn) {
		readRegister(t, ws)
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		})
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("应注册成功")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
	}
	wg.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close 后 Run 应返回 nil, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随并发 Close 退出")
	}
}

// TestRunTwiceRejected N3：Run 并发调用返回 errRunAlreadyRunning。
func TestRunTwiceRejected(t *testing.T) {
	url := newFakeHub(t, func(ws *websocket.Conn) {
		readRegister(t, ws)
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		})
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("应注册成功")
	}
	if err := c.Run(t.Context()); err == nil {
		t.Fatal("并发 Run 应被拒绝")
	}
	_ = c.Close()
	<-done
}

// ---------- C1：node 侧协议版本校验 ----------

// TestRegisterAckWrongVersionRetries C1：register_ack 携不支持的版本 → 本次
// 握手失败，退避重连；正确版本应答后注册成功。
func TestRegisterAckWrongVersionRetries(t *testing.T) {
	var conns atomic.Int64
	url := newFakeHub(t, func(ws *websocket.Conn) {
		n := conns.Add(1)
		readRegister(t, ws)
		env := &protocol.Envelope{
			Type: protocol.MsgRegisterAck, MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		}
		if n == 1 {
			env.Version = 99 // 首连：版本不支持
		} else {
			env.Version = protocol.ProtocolVersion
		}
		writeFrame(t, ws, env)
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	if !waitForC(5*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("错误版本应答后应重连并以正确版本注册成功")
	}
	if n := conns.Load(); n < 2 {
		t.Fatalf("应至少两次连接, got %d", n)
	}
	_ = c.Close()
	<-done
}

// TestServiceStageWrongVersionDisconnects C1：服务阶段收到错误版本的帧 → 断开
// 并按新连接重连（不得吞掉继续用）。
func TestServiceStageWrongVersionDisconnects(t *testing.T) {
	var conns atomic.Int64
	url := newFakeHub(t, func(ws *websocket.Conn) {
		n := conns.Add(1)
		readRegister(t, ws)
		epoch := int64(1)
		if n > 1 {
			epoch = 2 // 每会话新 epoch：用以唯一标识「第二连接」注册成功
		}
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: epoch}),
		})
		if n == 1 {
			time.Sleep(100 * time.Millisecond) // 等客户端进入服务阶段
			writeFrame(t, ws, &protocol.Envelope{
				Version: 2, Type: protocol.MsgPing, MessageID: protocol.NewMessageID(),
			}) // 错误版本心跳 → 断开重连
		}
		time.Sleep(10 * time.Second)
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	// 等待信号必须是「第二连接注册成功」（epoch=2）：本测试首连注册本就成功，
	// LastRegisteredAt 不能区分两连——若等它，断言会抢在 v2 帧发出前执行。
	// epoch=2 ⇒ 断开重连已发生且新连接完成握手，比连接计数更强，且保证 Close
	// 不会打断注册中连接（handler 的 readRegister 不会因收尾 EOF 而 Fatal）。
	if !waitForC(5*time.Second, func() bool {
		return c.Epoch() == 2
	}) {
		t.Fatal("错误版本帧后应断开重连并以新 epoch 注册成功")
	}
	if n := conns.Load(); n < 2 {
		t.Fatalf("应至少两次连接, got %d", n)
	}
	_ = c.Close()
	<-done
}

// ---------- L4：非优雅关闭取消在途写 ----------

// TestCloseInterruptsStalledWrite L4：对端停止读取、数据队列积压、写 goroutine
// 阻塞在大帧 Write 时，Close 必须取消在途写并在预算内退出（非 60s 写超时）。
func TestCloseInterruptsStalledWrite(t *testing.T) {
	url := newFakeHub(t, func(ws *websocket.Conn) {
		readRegister(t, ws)
		writeFrame(t, ws, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
			MessageID: protocol.NewMessageID(), From: "hub",
			Payload: mustJSONP(protocol.RegisterAckPayload{OK: true, Epoch: 1}),
		})
		time.Sleep(30 * time.Second) // 不再读取：对端停止消费
	})
	c := NewClient(Config{HubURL: url, NodeID: "n1", Token: "tok"})
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	if !waitForC(3*time.Second, func() bool {
		_, ok := c.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("应注册成功")
	}
	// 持续灌入大帧直至积压进数据队列（写阻塞后帧滞留队列）
	fillDone := make(chan struct{})
	go func() {
		defer close(fillDone)
		big := strings.Repeat("x", 256<<10)
		for i := 0; i < 40; i++ {
			if err := c.Send("hub", &protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
				MessageID: protocol.NewMessageID(),
				Payload:   mustJSONP(protocol.TaskResultPayload{TaskID: "t", Result: &protocol.TaskResult{FinalAnswer: big}}),
			}); err != nil {
				return
			}
		}
	}()
	// 等待数据队列积压 ≥3MiB
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		q := c.q
		var queued int
		if q != nil {
			q.mu.Lock()
			queued = q.dataB
			q.mu.Unlock()
		}
		c.mu.Unlock()
		if queued >= 3<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	_ = c.Close()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Close 应及时退出（在途写被取消）, took %s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 未能在预算内退出（写循环仍排空数据/阻塞在途 Write）")
	}
}
