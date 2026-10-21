package session

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// TestRegisterHandshakeEpochAllocation 注册握手与 epoch 分配（SPEC 4.4）：
// 首次注册成功 epoch=1 并随快照持久化、register_ack 携新 epoch、上线回调触发；
// 同 node 二次注册 epoch 原子 +1，旧连接被替换关闭且不误发下线回调。
func TestRegisterHandshakeEpochAllocation(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)

	c1 := wsDial(t, h.wsURL(), nil)
	ack1 := wsRegister(t, c1, "nodeA", "tok-a", []protocol.AgentCard{cardDemo("echo")})
	if !ack1.OK || ack1.Epoch != 1 {
		t.Fatalf("首次注册应成功 epoch=1, got %+v", ack1)
	}
	if got := h.store.View().Nodes["nodeA"].Epoch; got != 1 {
		t.Fatalf("epoch 应持久化 1, got %d", got)
	}
	if got := h.store.View().Nodes["nodeA"].Agents; len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("能力表应随注册事务更新, got %+v", got)
	}
	if on := h.onlineOf("nodeA"); len(on) != 1 || !on[0] {
		t.Fatalf("上线回调应触发一次 true, got %v", on)
	}

	// 同 node 二次注册：epoch=2，旧连接被关闭
	c2 := wsDial(t, h.wsURL(), nil)
	ack2 := wsRegister(t, c2, "nodeA", "tok-a", nil)
	if !ack2.OK || ack2.Epoch != 2 {
		t.Fatalf("二次注册应 epoch=2, got %+v", ack2)
	}
	// S1：非优雅断开（替换走 CloseNow），且必须真的由 hub 关闭（超时即 Fatal）
	assertClosedAbnormal(t, wsReadClosed(t, c1, 2*time.Second))
	if on := h.onlineOf("nodeA"); len(on) != 2 || on[0] != true || on[1] != true {
		t.Fatalf("两次注册各触发一次上线回调且无下线, got %v", on)
	}
	if n := h.mgr.liveSessionsCount(); n != 1 {
		t.Fatalf("会话表应恰有 1 个会话, got %d", n)
	}
}

// TestRegisterAuthFailed token 鉴权失败（SPEC 4.2/4.4）：register_ack ok=false
// reason=auth_failed 后断开；不分配 epoch、不上线。
func TestRegisterAuthFailed(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)

	c := wsDial(t, h.wsURL(), nil)
	wsWriteEnv(t, c, regEnvelope("nodeA", "wrong-token", nil))
	env := wsReadEnv(t, c, 5*time.Second)
	if env.Type != protocol.MsgRegisterAck {
		t.Fatalf("期待 register_ack, got %s", env.Type)
	}
	var p protocol.RegisterAckPayload
	if err := jsonUnmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.OK || p.Reason != protocol.CodeAuthFailed {
		t.Fatalf("应拒绝且 reason=auth_failed, got %+v", p)
	}
	// S1：拒绝后为优雅关闭（ack 排空后走关闭握手），且剔除本地超时假成功
	assertClosedGraceful(t, wsReadClosed(t, c, 2*time.Second))
	if got := h.store.View().Nodes["nodeA"].Epoch; got != 0 {
		t.Fatalf("鉴权失败不得分配 epoch, got %d", got)
	}
	if on := h.onlineOf("nodeA"); len(on) != 0 {
		t.Fatalf("鉴权失败不得上线, got %v", on)
	}
}

// TestRegisterTimeout 注册首帧超时（SPEC 4.2/4.6：连接后 5s 内首帧，测试注入
// 短周期）：超时未发 register 即断开。
func TestRegisterTimeout(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, func(c *Config) {
		c.RegisterTimeout = 150 * time.Millisecond
	})
	c := wsDial(t, h.wsURL(), nil)
	start := time.Now()
	assertClosedAbnormal(t, wsReadClosed(t, c, 3*time.Second))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("断开过迟: %s", elapsed)
	}
}

// TestRegisterFirstFrameNotRegister 首帧非 register（SPEC 4.2）→ 断开。
func TestRegisterFirstFrameNotRegister(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	wsWriteEnv(t, c, &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgPing,
		MessageID: protocol.NewMessageID(),
	})
	assertClosedAbnormal(t, wsReadClosed(t, c, 2*time.Second))
	if got := h.store.View().Nodes["nodeA"].Epoch; got != 0 {
		t.Fatalf("不得分配 epoch, got %d", got)
	}
}

// TestRegisterUnsupportedVersion 协议版本不支持（SPEC 4.1：回 err 帧并断开）。
func TestRegisterUnsupportedVersion(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	env := regEnvelope("nodeA", "tok-a", nil)
	env.Version = 99
	wsWriteEnv(t, c, env)
	got := wsReadEnv(t, c, 2*time.Second)
	if got.Type != protocol.MsgErr || got.Err == nil || got.Err.Code != protocol.CodeUnsupportedVersion {
		t.Fatalf("应回 err(unsupported_version), got %+v", got)
	}
	assertClosedGraceful(t, wsReadClosed(t, c, 2*time.Second))
}

// C1：注册之后的每个服务阶段帧仍须校验协议版本——错误版本的业务帧不得进入
// core，统一回 err(unsupported_version) 并断开（SPEC 4.1）。
func TestServiceStageUnsupportedVersion(t *testing.T) {
	var gotMu sync.Mutex
	var got []*protocol.Envelope
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, true, 0, nil)
	h.mgr.Subscribe(func(env *protocol.Envelope) {
		gotMu.Lock()
		got = append(got, env)
		gotMu.Unlock()
	})
	c := wsDial(t, h.wsURL(), nil)
	ack := wsRegister(t, c, "nodeA", "tok-a", nil)
	if !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	h.mgr.mu.RLock()
	s := h.mgr.sessions["nodeA"]
	h.mgr.mu.RUnlock()
	if s == nil {
		t.Fatal("会话应已在表")
	}
	// v=2 的心跳 / card_update / task_result（正确 epoch 搭配错误版本）
	for _, typ := range []string{protocol.MsgPing, protocol.MsgCardUpdate, protocol.MsgTaskAck} {
		wsWriteEnv(t, c, &protocol.Envelope{
			Version: 2, Type: typ, MessageID: protocol.NewMessageID(),
			SessionEpoch: ack.Epoch,
			Payload:      mustJSON(protocol.CardUpdatePayload{Agents: []protocol.AgentCard{cardDemo("hacked")}}),
		})
	}
	got2 := wsReadEnv(t, c, 2*time.Second)
	if got2.Type != protocol.MsgErr || got2.Err == nil || got2.Err.Code != protocol.CodeUnsupportedVersion {
		t.Fatalf("应回 err(unsupported_version), got %+v", got2)
	}
	if got2.SessionEpoch != ack.Epoch {
		t.Fatalf("错误帧应携当前 epoch, got %d want %d", got2.SessionEpoch, ack.Epoch)
	}
	assertClosedGraceful(t, wsReadClosed(t, c, 2*time.Second))
	// 事件等待替代固定 sleep：q.done 关闭即 readLoop 已退出、此后不可能再入队。
	// 坏帧若被（错误地）入队，其处理器调用必然发生在 err 应答发出后 µs 级内、
	// 远早于 close 交换完成——此刻断言 got 即终值。不等待全会话收尾：优雅路径中
	// CloseRead 兜底 Close 与关闭握手在库内互等 c.closed，上界为 waitGoroutines
	// 的 15s 定时器，与「帧是否路由到 core」无关
	<-s.q.done
	// 业务状态未变：core 未收到任何路由帧，能力表未被错误版本 card_update 覆盖
	gotMu.Lock()
	n := len(got)
	gotMu.Unlock()
	if n != 0 {
		t.Fatalf("错误版本帧不得路由到 core, got %d", n)
	}
	if got := h.store.View().Nodes["nodeA"].Agents; len(got) != 0 {
		t.Fatalf("错误版本 card_update 不得写入, got %+v", got)
	}
}

// TestHeartbeatTimeout 心跳超时（SPEC 4.6：ping 15s / pong 45s，计时基于上次
// pong 收到时刻；测试注入短周期）：不回 pong 即断开。
func TestHeartbeatTimeout(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, func(c *Config) {
		c.PingInterval = 20 * time.Millisecond
		c.PongTimeout = 150 * time.Millisecond
	})
	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 只读不回：hub ping 到达但无 pong → 超时断开
	start := time.Now()
	assertClosedAbnormal(t, wsReadClosed(t, c, 3*time.Second))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("断开过迟: %s", elapsed)
	}
}

// TestHeartbeatAlive 正常心跳：逐 ping 回 pong（携当前 epoch）→ 连接跨越
// 数倍 pong 超时后仍存活（单 goroutine 顺序读写，避免并发使用连接）。
func TestHeartbeatAlive(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, func(c *Config) {
		c.PingInterval = 20 * time.Millisecond
		c.PongTimeout = 200 * time.Millisecond
	})
	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK || ack.Epoch != 1 {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 应答 40 轮 ping（约 800ms = 4 倍 pong 超时），全程不被断开
	for i := 0; i < 40; i++ {
		env := wsReadEnv(t, c, 2*time.Second)
		if env.Type != protocol.MsgPing {
			t.Fatalf("期待 ping, got %s", env.Type)
		}
		wsWriteEnv(t, c, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgPong,
			MessageID: protocol.NewMessageID(), To: "hub",
			SessionEpoch: env.SessionEpoch, ReplyTo: env.MessageID,
		})
	}
	// 存活可证：应用层 ping 仍得到 pong
	wsWriteEnv(t, c, &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgPing,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1,
	})
	got := wsReadEnv(t, c, 2*time.Second)
	if got.Type != protocol.MsgPong {
		t.Fatalf("连接应存活并回 pong, got %s", got.Type)
	}
}

// TestStaleEpochIgnoredThenDisconnect 入站帧 epoch 校验（SPEC 4.4）：不匹配帧
// 忽略+计数、有效帧清零计数、连续 5 次断开；被忽略帧不得路由到订阅回调。
// S1：阈值前用观测点确认第 4 次仍存活、阈值后验证真的由 hub 关闭。
func TestStaleEpochIgnoredThenDisconnect(t *testing.T) {
	var gotMu sync.Mutex
	var got []*protocol.Envelope
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	h.mgr.Subscribe(func(env *protocol.Envelope) {
		gotMu.Lock()
		got = append(got, env)
		gotMu.Unlock()
	})

	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	h.mgr.mu.RLock()
	s := h.mgr.sessions["nodeA"]
	h.mgr.mu.RUnlock()
	if s == nil {
		t.Fatal("会话应已在表")
	}
	// 模拟「另一连接完成再注册」：store 当前代次抬到 2，本连接成为旧连接残余
	if err := h.store.Update(func(st *store.State) error {
		rec := st.Nodes["nodeA"]
		rec.Epoch = 2
		st.Nodes["nodeA"] = rec
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskAck,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1, // 旧代次
		Payload: mustJSON(protocol.TaskRef{TaskID: "t-x"}),
	}
	for i := 0; i < 2; i++ {
		wsWriteEnv(t, c, stale)
	}
	// S1：以读循环的连续不匹配计数为事件源（=2 即两帧已被消费且走忽略路径），
	// 替代固定 sleep；此时再断言无路由，负向结论确定
	if !waitFor(2*time.Second, func() bool { return s.staleEp.Load() == 2 }) {
		t.Fatal("前置：两帧旧代次应已被读循环消费")
	}
	gotMu.Lock()
	n := len(got)
	gotMu.Unlock()
	if n != 0 {
		t.Fatalf("旧代次帧应被忽略, got %d", n)
	}
	// 有效代次帧：正常路由且清零连续计数
	valid := *stale
	valid.SessionEpoch = 2
	valid.MessageID = protocol.NewMessageID()
	wsWriteEnv(t, c, &valid)
	if !waitFor(2*time.Second, func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		return len(got) == 1
	}) {
		t.Fatal("当前代次帧应路由到回调")
	}
	// 连续 4 次旧代次（未达 5）：会话仍在表（连接未被断开，S1 阈值前存活观测点）。
	// 计数事件源：=4 即四帧已消费且未达断开阈值
	for i := 0; i < 4; i++ {
		wsWriteEnv(t, c, stale)
	}
	if !waitFor(2*time.Second, func() bool { return s.staleEp.Load() == 4 }) {
		t.Fatalf("前置：连续不匹配计数应达 4, got %d", s.staleEp.Load())
	}
	if h.mgr.liveSessionsCount() != 1 {
		t.Fatal("连续 4 次不匹配不得断开（阈值应为 5）")
	}
	// 第 5 次连续不匹配 → 断开（且必须由 hub 关闭，非本地超时）
	wsWriteEnv(t, c, stale)
	assertClosedAbnormal(t, wsReadClosed(t, c, 2*time.Second))
	if !waitFor(2*time.Second, func() bool { return h.mgr.liveSessionsCount() == 0 }) {
		t.Fatal("断开后会话应注销")
	}
}

// TestSendUnknownNode 目标端点不存在（与 membus ErrNoEndpoint 语义对齐）。
func TestSendUnknownNode(t *testing.T) {
	h := newHubStack(t, nil, false, 0, nil)
	err := h.mgr.Send("ghost", &protocol.Envelope{Type: protocol.MsgTaskOffer})
	if !errors.Is(err, transport.ErrNoEndpoint) {
		t.Fatalf("应返回 ErrNoEndpoint, got %v", err)
	}
}

// TestOversizeFrameReply 帧超限（SPEC 4.6）：回 err(frame_too_large)，连接保持
// 可用（随后 ping 仍得到 pong）。C2：已注册会话的错误帧必须携当前 epoch。
func TestOversizeFrameReply(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 超限帧（>1MiB 但在读上限余量内）线上直发：ws 层放行、协议层拒收
	raw := []byte(`{"v":1,"type":"card_update","mid":"x","epoch":1,"payload":"` + string(make([]byte, protocol.MaxMessageBytes)) + `"}`)
	ctx, cancel := contextWithTimeout(5 * time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("发送超限帧: %v", err)
	}
	got := wsReadEnv(t, c, 2*time.Second)
	if got.Type != protocol.MsgErr || got.Err == nil || got.Err.Code != protocol.CodeFrameTooLarge {
		t.Fatalf("应回 err(frame_too_large), got %+v", got)
	}
	if got.SessionEpoch != 1 { // C2：已注册会话错误帧携当前 epoch
		t.Fatalf("错误帧应携当前 epoch=1, got %d", got.SessionEpoch)
	}
	// 连接仍可用
	wsWriteEnv(t, c, &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgPing,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1,
	})
	if pong := wsReadEnv(t, c, 2*time.Second); pong.Type != protocol.MsgPong {
		t.Fatalf("超限帧后连接应保持可用, got %s", pong.Type)
	}
}

// TestOversizeFrameUnregisteredEpoch0 C2 对照：未注册会话的错误帧允许 epoch=0。
func TestOversizeFrameUnregisteredEpoch0(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	raw := []byte(`{"v":1,"type":"register","mid":"x","payload":"` + string(make([]byte, protocol.MaxMessageBytes)) + `"}`)
	ctx, cancel := contextWithTimeout(5 * time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("发送超限帧: %v", err)
	}
	got := wsReadEnv(t, c, 2*time.Second)
	if got.Type != protocol.MsgErr || got.Err == nil || got.Err.Code != protocol.CodeFrameTooLarge {
		t.Fatalf("应回 err(frame_too_large), got %+v", got)
	}
	if got.SessionEpoch != 0 {
		t.Fatalf("未注册错误帧应 epoch=0, got %d", got.SessionEpoch)
	}
}

// TestManagerCloseGraceful 优雅关闭（SPEC 九：先遍历会话发 Close 再等退出）：
// 客户端收到关闭、会话注销、下线回调触发、关闭后 Send 报端点不存在。
func TestManagerCloseGraceful(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 客户端排空并完成关闭握手（hub 的 Close 会等对端回帧，无人应答则阻塞至超时）
	go func() {
		_ = wsReadErr(t, c, 5*time.Second)
		_ = c.Close(websocket.StatusNormalClosure, "bye")
	}()
	if err := h.mgr.CloseWithBudget(2 * time.Second); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if on := h.onlineOf("nodeA"); len(on) != 2 || !on[0] || on[1] {
		t.Fatalf("在线变迁应为 [true,false], got %v", on)
	}
	if !waitFor(2*time.Second, func() bool {
		return errors.Is(h.mgr.Send("nodeA", &protocol.Envelope{}), transport.ErrNoEndpoint)
	}) {
		t.Fatal("Close 后 Send 应报 ErrNoEndpoint")
	}
}

// ---------- L1：注册串行化 + 残余帧/事务失效 ----------

// TestRegisterSerializeCommitPublish L1（核心竞态）：屏障暂停 A 于「已提交持久化、
// 未发布会话表」窗口；B 完成完整注册；释放 A。断言最终持久化 epoch、唯一路由会话
// 与 ack 一致——A 的迟到发布不得覆盖 B（旧实现此处会写回 epoch=1 会话并关闭 B）。
func TestRegisterSerializeCommitPublish(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	gate := make(chan struct{})
	h.mgr.hookAfterRegisterPersist = func(nodeID string) {
		<-gate // 屏障：A 卡在「先提交后发布」逆序窗口
	}

	cA := wsDial(t, h.wsURL(), nil)
	ackA := make(chan protocol.RegisterAckPayload, 1)
	go func() { ackA <- wsRegister(t, cA, "nodeA", "tok-a", nil) }()
	if !waitFor(2*time.Second, func() bool {
		return h.store.View().Nodes["nodeA"].Epoch == 1 // A 已提交、尚未发布
	}) {
		t.Fatal("前置：A 应已持久化 epoch=1")
	}

	cB := wsDial(t, h.wsURL(), nil)
	ackB := make(chan protocol.RegisterAckPayload, 1)
	go func() { ackB <- wsRegister(t, cB, "nodeA", "tok-a", nil) }()

	close(gate) // 释放 A：A 发布并 ack，随后 B 才能进入提交
	a := <-ackA
	b := <-ackB
	if !a.OK || a.Epoch != 1 {
		t.Fatalf("A 应成功 epoch=1, got %+v", a)
	}
	if !b.OK || b.Epoch != 2 {
		t.Fatalf("B 应成功 epoch=2, got %+v", b)
	}
	// 一致性：持久化 epoch == 唯一路由会话代次 == 最新 ack
	if got := h.store.View().Nodes["nodeA"].Epoch; got != 2 {
		t.Fatalf("持久化 epoch 应为 2, got %d", got)
	}
	if n := h.mgr.liveSessionsCount(); n != 1 {
		t.Fatalf("会话表应恰有 1 个会话, got %d", n)
	}
	// 最新注册的 B 仍可路由（旧实现中 B 被逆序覆盖关闭）：B 发有效帧应得到 pong
	wsWriteEnv(t, cB, &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgPing,
		MessageID: protocol.NewMessageID(), SessionEpoch: 2,
	})
	if p := wsReadEnv(t, cB, 2*time.Second); p.Type != protocol.MsgPong || p.SessionEpoch != 2 {
		t.Fatalf("新会话应存活且被路由, got %+v", p)
	}
	// A 应已被替换关闭
	assertClosedAbnormal(t, wsReadClosed(t, cA, 2*time.Second))
}

// TestStaleCardUpdateRejectedAfterGeneration L1：旧会话的 card_update 事务在新
// epoch 发布之后才落库时必须被事务内代次复核拒绝，不得覆盖新会话能力表。
func TestStaleCardUpdateRejectedAfterGeneration(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	cA := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, cA, "nodeA", "tok-a", []protocol.AgentCard{cardDemo("a1")}); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	h.mgr.hookBeforeCardUpdate = func(nodeID string) {
		once.Do(func() { close(entered) })
		<-gate // 卡住旧会话的 card_update，令其事务在新注册发布之后才提交
	}

	// A 发 card_update[stale]（此刻仍是当前会话，readLoop 放行）
	oldEnv := &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgCardUpdate,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1,
		Payload: mustJSON(protocol.CardUpdatePayload{Agents: []protocol.AgentCard{cardDemo("stale")}}),
	}
	wsWriteEnv(t, cA, oldEnv)
	select { // 等待 card_update 真正卡在事务提交前（注入点内部）
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("card_update 应到达事务注入点")
	}

	// 新会话 B 注册：epoch=2，能力表 [b1]
	cB := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, cB, "nodeA", "tok-a", []protocol.AgentCard{cardDemo("b1")}); !ack.OK || ack.Epoch != 2 {
		t.Fatalf("B 注册应 epoch=2, got %+v", ack)
	}
	close(gate) // 放行旧 card_update：事务内代次复核应拒绝
	if !waitFor(2*time.Second, func() bool {
		ags := h.store.View().Nodes["nodeA"].Agents
		return len(ags) == 1 && ags[0].Name == "b1"
	}) {
		ags := h.store.View().Nodes["nodeA"].Agents
		t.Fatalf("旧会话 card_update 不得覆盖新会话能力表, got %+v", ags)
	}
	if got := h.store.View().Nodes["nodeA"].Epoch; got != 2 {
		t.Fatalf("epoch 应保持 2, got %d", got)
	}
}

// TestReplacedSessionFrameDropped L1：换代后旧会话已入 frameCh 的业务帧不得进入
// core（processLoop 代次复核），换代前的帧正常路由。
func TestReplacedSessionFrameDropped(t *testing.T) {
	release := make(chan struct{})
	firstBlocked := make(chan struct{})
	var once sync.Once
	var gotMu sync.Mutex
	var got []*protocol.Envelope
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	h.mgr.Subscribe(func(env *protocol.Envelope) {
		once.Do(func() { close(firstBlocked) })
		<-release // 第一帧阻塞处理器，为注入+换代制造确定性窗口
		gotMu.Lock()
		got = append(got, env)
		gotMu.Unlock()
	})
	cA := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, cA, "nodeA", "tok-a", nil); !ack.OK || ack.Epoch != 1 {
		t.Fatalf("注册失败: %+v", ack)
	}
	h.mgr.mu.RLock()
	s := h.mgr.sessions["nodeA"]
	h.mgr.mu.RUnlock()
	if s == nil {
		t.Fatal("会话应已在表")
	}

	// F1（当前代次）：经 readLoop 正常路由，占住处理器
	wsWriteEnv(t, cA, &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskAck,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1,
		Payload: mustJSON(protocol.TaskRef{TaskID: "t-first"}),
	})
	select {
	case <-firstBlocked:
	case <-time.After(2 * time.Second):
		t.Fatal("前置：F1 应进入处理器")
	}
	// F2 直接注入 frameCh（换代前已入队，模拟 readLoop 校验后的残余帧）
	s.frameCh <- &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskAck,
		MessageID: protocol.NewMessageID(), SessionEpoch: 1,
		Payload: mustJSON(protocol.TaskRef{TaskID: "t-stale"}),
	}
	// 换代：新连接真实注册，会话表替换（旧会话关闭）
	cB := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, cB, "nodeA", "tok-a", nil); !ack.OK || ack.Epoch != 2 {
		t.Fatalf("换代注册应 epoch=2, got %+v", ack)
	}
	close(release) // F1 完成；F2 出队时旧会话已被替换 → 丢弃（或随队列关闭失效）。
	// S1：等旧会话 goroutine 全部退出再断言（processLoop 已不可能再调用处理器），
	// 替代固定 sleep
	waitSessionFinished(t, s, 2*time.Second)
	gotMu.Lock()
	n := len(got)
	gotMu.Unlock()
	if n != 1 {
		t.Fatalf("换代后残余帧不得进入 core（仅 F1 路由）, got %d", n)
	}
}

// ---------- L2：上下线事件按 node 有序，过期通知不覆盖 ----------

// TestOnlineEventsOrderedAgainstRegister L2：下线回调与并发注册串行化——旧会话
// removeSession 的迟到 false 不得覆盖新会话的 true（core 在线表保持在线）。
func TestOnlineEventsOrderedAgainstRegister(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, true, 0, nil)
	c1 := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c1, "nodeA", "tok-a", []protocol.AgentCard{cardDemo("svc")}); !ack.OK || ack.Epoch != 1 {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 替换注册（epoch=2）：旧连接 c1 关闭 → removeSession(c1) 判 cur≠s，不下线
	c2 := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c2, "nodeA", "tok-a", []protocol.AgentCard{cardDemo("svc")}); !ack.OK || ack.Epoch != 2 {
		t.Fatalf("二次注册失败: %+v", ack)
	}
	assertClosedAbnormal(t, wsReadClosed(t, c1, 2*time.Second))
	// 在线状态最终为 true（无迟到 false 覆盖），提交可立即进入投递（不再滞留 submitted）
	ackSub, err := h.core.Submit(submitReq("nodeA", "svc", "x"), "")
	if err != nil {
		t.Fatalf("提交应成功: %v", err)
	}
	if !waitFor(2*time.Second, func() bool {
		tk, ok := h.taskOf(ackSub.TaskID)
		return ok && tk.Status != protocol.StatusSubmitted
	}) {
		t.Fatalf("在线节点提交应立即投递（在线表未被迟到 false 覆盖）, got %v", h.onlineOf("nodeA"))
	}
	// 会话表唯一且为新会话
	if n := h.mgr.liveSessionsCount(); n != 1 {
		t.Fatalf("会话表应恰有 1 个会话, got %d", n)
	}
}

// ---------- L3：Close 覆盖全部连接 + 关闭预算 ----------

// TestCloseCoversRegisteringConnections L3：已接受未注册 / 已持久化未发布的连接
// 均纳入关闭快照；Close 后注册不得发布（无 ack、无上线回调、无遗留会话），
// Close 在预算内有界返回。
func TestCloseCoversRegisteringConnections(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)

	// c0：已升级、未发 register
	c0 := wsDial(t, h.wsURL(), nil)
	// c1：注册已持久化、卡在发布前（注入点）
	gate := make(chan struct{})
	h.mgr.hookAfterRegisterPersist = func(nodeID string) { <-gate }
	c1 := wsDial(t, h.wsURL(), nil)
	wsWriteEnv(t, c1, regEnvelope("nodeA", "tok-a", nil))
	if !waitFor(2*time.Second, func() bool {
		return h.store.View().Nodes["nodeA"].Epoch == 1
	}) {
		t.Fatal("前置：c1 应已持久化 epoch=1")
	}

	// 关闭（预算 1.5s）：快照须同时覆盖 c0/c1
	closeDone := make(chan error, 1)
	go func() { closeDone <- h.mgr.CloseWithBudget(1500 * time.Millisecond) }()
	time.Sleep(100 * time.Millisecond) // 让 Close 取完快照
	close(gate)                        // 放行注册：发布应被拒绝

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close 应有界成功, got %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close 未在预算内返回")
	}
	// c1：关闭前排空应答——不得出现 register_ack（S1 精确断言：等到的必须是
	// 对端关闭本身，本地超时即 Fatal，不把任意读错误当关闭成功）
	wsReadClosedNoFrame(t, c1, 2*time.Second, protocol.MsgRegisterAck)
	// c0：未注册连接同样被关闭（同口径剔除超时假成功）
	wsReadClosed(t, c0, 2*time.Second)
	if n := h.mgr.liveSessionsCount(); n != 0 {
		t.Fatalf("关闭后应零活跃会话, got %d", n)
	}
	if on := h.onlineOf("nodeA"); len(on) != 0 {
		t.Fatalf("关停后的注册不得触发上线回调, got %v", on)
	}
}

// TestCloseBudgetForcedClose L3：预算耗尽时强断（对端不应答握手 → 取消会话 ctx
// 后 CloseNow 中止在途握手），Close 有界返回。
func TestCloseBudgetForcedClose(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeA": "tok-a"}, false, 0, nil)
	c := wsDial(t, h.wsURL(), nil)
	if ack := wsRegister(t, c, "nodeA", "tok-a", nil); !ack.OK {
		t.Fatalf("注册失败: %+v", ack)
	}
	// 客户端停止读取：hub 的优雅关闭握手无应答（库内 5s 超时不可控），
	// 预算耗尽必须强断兜底
	start := time.Now()
	if err := h.mgr.CloseWithBudget(300 * time.Millisecond); err != nil {
		t.Fatalf("强断兜底后应成功关闭, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close 应有界返回, took %s", elapsed)
	}
	// 连接须被关闭（close 帧与 TCP 强断的到达次序有竞态，不判定状态码）
	wsReadClosed(t, c, 2*time.Second)
}

// ---------- C4：管理面监听校验 ----------

// TestValidateAPIListen C4：api_listen 无条件只允许 127.0.0.1（空 host/0.0.0.0/
// 公网地址/::1 拒绝）；校验与 TLS 模式无关（函数本身模式无关，调用点见 cmd/hub）。
func TestValidateAPIListen(t *testing.T) {
	for _, bad := range []string{"0.0.0.0:8444", ":8444", "192.168.1.5:8444", "[::1]:8444", "example.com:8444"} {
		if err := ValidateAPIListen(bad); err == nil {
			t.Errorf("api_listen %q 应被拒绝", bad)
		}
	}
	if err := ValidateAPIListen("127.0.0.1:8444"); err != nil {
		t.Errorf("api_listen 127.0.0.1 应被接受, got %v", err)
	}
}

// TestRegisterVersionCheckHelper C1 辅助函数行为。
func TestRegisterVersionCheckHelper(t *testing.T) {
	if err := checkVersion(&protocol.Envelope{Version: protocol.ProtocolVersion}); err != nil {
		t.Fatalf("当前版本应通过, got %v", err)
	}
	if err := checkVersion(&protocol.Envelope{Version: 2}); err == nil {
		t.Fatal("错误版本应报错")
	}
}
