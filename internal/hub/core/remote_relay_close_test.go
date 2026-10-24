// Close 与在途中继的并发收束单测（2026-10-24 复审 R2/R3 + 2026-10-25 三审 W3）：
// Close 统一收敛已启动外发（在途初始 Send/超时回调/中继转发三个外发点）——挂
// 起中的外发未完成前 Close 不返回，返回后本域零 transport 调用、零登记；登记与
// 启动 timer 同临界区发布（先登后启），短超时不丢超时兜底。
package core

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// gateTransport hub 出站 Send 闸门（复审 R2 并发测试用 Transport 实现）：hold 住
// 指定目标后，发往该目标的 Send 挂起直至 release 放行（模拟慢源 node/慢请求方
// 链路），其余目标直通 bus 的 hub 端点；Subscribe/Close 透传。挂起期间 pend 计
// 数 >0，供测试轮询「外发已启动且未完成」——这是 R2 窗口（回调/请求已过锁内复
// 核、Send 尚未落 transport）的确定性复现手段。
type gateTransport struct {
	hubEP *transport.Endpoint
	mu    sync.Mutex
	gate  map[string]chan struct{}
	pend  map[string]int
}

func newGateTransport(bus *transport.Bus) *gateTransport {
	return &gateTransport{hubEP: bus.Endpoint("hub"),
		gate: make(map[string]chan struct{}), pend: make(map[string]int)}
}

// hold 闸住发往 to 的 Send（重复 hold 覆盖旧闸，已挂起 Send 不受影响）。
func (g *gateTransport) hold(to string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate[to] = make(chan struct{})
}

// release 放行（close 闸门；此后发往 to 的 Send 直通）。
func (g *gateTransport) release(to string) {
	g.mu.Lock()
	ch, ok := g.gate[to]
	delete(g.gate, to)
	g.mu.Unlock()
	if ok {
		close(ch)
	}
}

// pending 发往 to 且尚未放行的挂起外发数。
func (g *gateTransport) pending(to string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pend[to]
}

// Send 实现 transport.Transport：闸门目标挂起（5s 无人放行按链路失败兜底，防测
// 试死挂）；放行后直通底层端点。
func (g *gateTransport) Send(to string, env *protocol.Envelope) error {
	g.mu.Lock()
	ch := g.gate[to]
	if ch != nil {
		g.pend[to]++
	}
	g.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			g.mu.Lock()
			g.pend[to]--
			g.mu.Unlock()
			return transport.ErrClosed
		}
		g.mu.Lock()
		g.pend[to]--
		g.mu.Unlock()
	}
	return g.hubEP.Send(to, env)
}

// Subscribe 实现 transport.Transport（透传 hub 端点入站回调）。
func (g *gateTransport) Subscribe(fn transport.Handler) { g.hubEP.Subscribe(fn) }

// Close 实现 transport.Transport（透传 hub 端点关闭）。
func (g *gateTransport) Close() error { return g.hubEP.Close() }

// waitPending 等待 to 的挂起外发出现（复审窗口确定性就位）。
func waitPending(g *gateTransport, to string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if g.pending(to) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return g.pending(to) > 0
}

// newRelayCore 搭建带闸门 transport 的中继测试核（含 store/bus/seed 清理）。
func newRelayCore(t *testing.T, relayTimeout time.Duration) (*Core, *transport.Bus, *gateTransport, *hubFixture) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	gt := newGateTransport(bus)
	c, err := New(Config{Store: st, Transport: gt, RelayFetchTimeout: relayTimeout, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gt.Subscribe(c.HandleFrame)
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: c}
	return c, bus, gt, f
}

// assertTableClosed 断言在途表清空且关闭位已置。
func assertTableClosed(t *testing.T, c *Core) {
	t.Helper()
	c.muArt.Lock()
	n, closed := len(c.fetches), c.artClosed
	c.muArt.Unlock()
	if n != 0 || !closed {
		t.Fatalf("在途表应清空且关闭位已置: n=%d closed=%v", n, closed)
	}
}

// TestCloseWaitsInFlightRelaySend Close 等待在途初始 Send（复审 R2 收敛点①）：
// 请求的初始 artifact_fetch Send 挂起于闸门时 Close 阻塞，放行后 Close 才返回
// ——Close 返回后本域不再有 transport 调用（timer 已停 + 黑洞不应答 → 请求方零
// 帧）。旧交错（请求读 closed=false → Close 清表返回 → 请求才 Send）使 Close 返
// 回后仍有外发残尾。
func TestCloseWaitsInFlightRelaySend(t *testing.T) {
	c, bus, gt, f := newRelayCore(t, 5*time.Second)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 黑洞：不应答 */ })
	gt.hold("nodeA")

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
		_ = nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)) // 同步分发：初始 Send 挂起于闸门
	}()
	if !waitPending(gt, "nodeA", 3*time.Second) {
		t.Fatal("初始 artifact_fetch Send 未按预期挂起于闸门")
	}

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close 应等待在途初始 Send 完成，未放行即返回: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	gt.release("nodeA")
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放行后 Close 未返回（收敛组泄漏）")
	}
	<-sent

	// 收敛后零外发：timer 已停 + 黑洞不应答 → 请求方不得收到任何帧
	time.Sleep(150 * time.Millisecond)
	if fs := got(); len(fs) != 0 {
		t.Fatalf("Close 收敛后不得再外发: %+v", fs)
	}
	assertTableClosed(t, c)
}

// TestCloseWaitsStartedTimeoutCallback Close 等待已启动的超时回调（复审 R2 收敛
// 点②）：timer 回调已过锁内复核并进入外发（挂起于请求方闸门）时 Close 阻塞，
// 放行后 Close 返回且请求方恰收一帧 fetch_timeout——「回调已拆表读 shutting=
// false 后 Close 才返回、回调才 Send」的窗口被收敛点消除。
func TestCloseWaitsStartedTimeoutCallback(t *testing.T) {
	c, bus, gt, f := newRelayCore(t, 50*time.Millisecond)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 黑洞：不应答 */ })
	gt.hold("nodeB") // 闸住超时回帧的外发目标

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !waitPending(gt, "nodeB", 3*time.Second) {
		t.Fatal("超时回调外发未按预期挂起于闸门")
	}

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close 应等待已启动回调外发完成，未放行即返回: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	gt.release("nodeB")
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放行后 Close 未返回（收敛组泄漏）")
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeFetchTimeout {
		t.Fatalf("放行后请求方应恰收一帧 fetch_timeout: %+v", fs)
	}
	assertTableClosed(t, c)
}

// TestCloseWaitsInFlightRelayForward Close 等待已启动的中继转发（三审 W3 收敛
// 点③，补第三个外发点的确定性并发复现）：源 node 应答已拆表停表、转发 Send 已
// 启动（挂起于请求方闸门）时 Close 阻塞，放行后 Close 才返回且请求方恰收一帧
// artifact_data——「应答已拆表、锁外转发 Send 挂起时 Close 返回」的窗口由收敛
// 点消除（前两个外发点见 TestCloseWaitsInFlightRelaySend /
// TestCloseWaitsStartedTimeoutCallback）。
func TestCloseWaitsInFlightRelayForward(t *testing.T) {
	c, bus, gt, f := newRelayCore(t, 5*time.Second) // timer 不在本测试窗口触发
	taskID, meta := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)

	// nodeA 桩：记录中继 mid，应答由测试显式驱动
	var mu sync.Mutex
	var relayMids []string
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		mu.Lock()
		relayMids = append(relayMids, env.MessageID)
		mu.Unlock()
	})

	// 请求中继（初始 Send 直通 nodeA，不受闸门影响；membus 同步分发——返回时
	// 中继请求已到达 nodeA 桩）；保存请求 mid 供恢复关联精确断言（四审 V3）
	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	req := reqEnv(protocol.MsgArtifactFetch, b)
	if err := nodeB.Send("hub", req); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	mu.Lock()
	mid := relayMids[0]
	mu.Unlock()

	// 闸住转发目标（请求方），再驱动源 node 回合法 artifact_data：拆表/复核/
	// 收敛组登记在锁内完成，转发 Send 挂起于闸门
	gt.hold("nodeB")
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		rb, _ := jsonMarshal(protocol.ArtifactDataPayload{TaskID: taskID, Index: 0, Meta: meta,
			DataBase64: base64.StdEncoding.EncodeToString([]byte("payload-1"))})
		_ = nodeA.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: mid, To: "hub", Payload: rb,
		})
	}()
	if !waitPending(gt, "nodeB", 3*time.Second) {
		t.Fatal("中继转发外发未按预期挂起于闸门")
	}

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close 应等待已启动的中继转发完成，未放行即返回: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	gt.release("nodeB")
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放行后 Close 未返回（收敛组泄漏）")
	}
	<-sent

	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgArtifactData || fs[0].ReplyTo != req.MessageID {
		t.Fatalf("放行后请求方应恰收一帧 artifact_data 转发（ReplyTo 精确关联请求 mid %s）: %+v", req.MessageID, fs)
	}
	assertTableClosed(t, c)
}

// TestRelayTinyTimeoutStillFires 先登后启（复审 R3）：1ns 超时下回调与登记同临
// 界区发布（回调起手先取 muArt）——必见已登记 waiter 并回超时帧。旧实现 timer
// 先于登记启动：短超时/调度暂停下回调先执行，表中无 waiter 即退出，后登记的
// waiter 永无超时兜底（请求方挂死至自身预算）。
func TestRelayTinyTimeoutStillFires(t *testing.T) {
	c, bus, _, f := newRelayCore(t, time.Nanosecond)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 黑洞：不应答 */ })

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeFetchTimeout {
		t.Fatalf("极短超时应仍回 err(fetch_timeout)（先登后启）: %+v", fs)
	}
	c.muArt.Lock()
	n := len(c.fetches)
	c.muArt.Unlock()
	if n != 0 {
		t.Fatalf("超时拆表后 waiter 表应清空，剩 %d", n)
	}
}

// TestCloseVsConcurrentRelayRequests 并发 Close vs 新请求/timer 回调（复审 R2）：
// 多 goroutine 持续发起取回请求的同时 Close——关闭后新请求一律 err(node_offline)
//（零登记零外发），在途请求由 Close 收束（黑洞不应答 + timer 停 → 无
// data/timeout 帧）；-race 下无竞态告警即交错面全部收敛。
func TestCloseVsConcurrentRelayRequests(t *testing.T) {
	c, bus, _, f := newRelayCore(t, 10*time.Second) // timer 不在本测试窗口触发
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 黑洞：不应答 */ })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
				_ = nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b))
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // 让若干请求先落地在途
	if err := c.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // Close 返回后继续压：必须全部 node_offline
	close(stop)
	wg.Wait()

	fs := got()
	for i, fr := range fs {
		if fr.Type != protocol.MsgErr || fr.Err.Code != protocol.CodeNodeOffline {
			t.Fatalf("帧 %d 应为 err(node_offline)（关闭后零登记零外发）: %+v", i, fr)
		}
	}
	assertTableClosed(t, c)
}
