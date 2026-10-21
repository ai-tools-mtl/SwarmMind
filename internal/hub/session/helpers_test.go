package session

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- 测试脚手架：hub 全栈（store+manager+core+httptest server） ----------

type hubStack struct {
	t     *testing.T
	store *store.Store
	mgr   *Manager
	core  *core.Core
	srv   *http.Server
	ln    *stoppableListener
	// serveExit 当前 srv.Serve goroutine 的退出信号（关闭 server 后重建监听前
	// 必须等它退出：Serve 的 defer l.Close() 落在监听替换之后会误关新监听器）
	serveExit chan struct{}

	onlineMu sync.Mutex
	online   map[string][]bool // 在线状态变迁记录（断言误下线用）
}

// newHubStack 起一个完整 hub（ws 明文，随机空闲端口）。
// withCore=false 时只装 Manager（会话层白盒单测）；sweep=0 不启动后台清扫。
func newHubStack(t *testing.T, tokens map[string]string, withCore bool, sweep time.Duration,
	tweak func(*Config)) *hubStack {
	return newHubStackAt(t, t.TempDir(), tokens, withCore, sweep, tweak)
}

// newHubStackAt 在指定 data_dir 上起 hub（S2：真正的磁盘重启恢复测试复用同一目录
// 销毁重建 store/core/Manager）。
func newHubStackAt(t *testing.T, dir string, tokens map[string]string, withCore bool, sweep time.Duration,
	tweak func(*Config)) *hubStack {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	h := &hubStack{t: t, store: st, online: map[string][]bool{}}
	var coreSvc *core.Core
	cfg := Config{
		Store:  st,
		Tokens: tokens,
		SetOnline: func(nodeID string, online bool) {
			h.onlineMu.Lock()
			h.online[nodeID] = append(h.online[nodeID], online)
			h.onlineMu.Unlock()
			if coreSvc != nil {
				coreSvc.SetOnline(nodeID, online)
			}
		},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	h.mgr = NewManager(cfg)
	if withCore {
		coreSvc, err = core.New(core.Config{Store: st, Transport: h.mgr, SweepInterval: sweep})
		if err != nil {
			t.Fatalf("构造 core: %v", err)
		}
		h.mgr.Subscribe(coreSvc.HandleFrame)
	}
	h.core = coreSvc

	h.ln = listenStopvable(t)
	h.startServing()
	t.Cleanup(func() {
		_ = h.mgr.CloseWithBudget(3 * time.Second)
		if h.core != nil {
			_ = h.core.Close()
		}
		if h.srv != nil { // stopServer 后为 nil
			_ = h.srv.Close()
		}
		_ = st.Close()
	})
	return h
}

// wsURL node 会话入口地址。
func (h *hubStack) wsURL() string {
	return "ws://" + h.ln.addr().String() + "/ws/node"
}

// onlineOf 返回节点在线变迁记录副本。
func (h *hubStack) onlineOf(nodeID string) []bool {
	h.onlineMu.Lock()
	defer h.onlineMu.Unlock()
	return append([]bool(nil), h.online[nodeID]...)
}

// taskOf 查询任务（hub 视角）。
func (h *hubStack) taskOf(taskID string) (protocol.Task, bool) {
	t, ok := h.store.View().Tasks[taskID]
	return t.Clone(), ok
}

// stoppableListener 可关闭/可换内核监听（宕机/恢复模拟；Listener 的替换与
// Close/Serve 并发，须同锁）。
type stoppableListener struct {
	mu sync.Mutex
	net.Listener
	closed bool
}

func listenStopvable(t *testing.T) *stoppableListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	return &stoppableListener{Listener: ln}
}

func (l *stoppableListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.Listener.Close()
}

// swapListener 原子替换底层监听并解除关闭态。
func (l *stoppableListener) swapListener(ln net.Listener) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Listener = ln
	l.closed = false
}

// addr 当前监听地址。
func (l *stoppableListener) addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Listener.Addr()
}

// startServing 以当前 ln 起一个 http.Server 服务 /ws/node（记录 Serve 退出信号，
// 供重建监听前等待旧 Serve 退出——其 defer l.Close() 若落在监听替换之后会误关
// 新监听器）。
func (h *hubStack) startServing() {
	mux := http.NewServeMux()
	mux.Handle("GET /ws/node", h.mgr)
	h.srv = &http.Server{Handler: mux}
	srv := h.srv
	wrapped := h.ln
	h.serveExit = make(chan struct{})
	go func() {
		defer close(h.serveExit)
		_ = srv.Serve(wrapped)
	}()
}

// waitOldServe 关闭当前 server 并等待其 Serve goroutine 退出（有界，防库内异常
// 挂住测试）：此后替换底层监听才安全。
func (h *hubStack) waitOldServe() {
	if h.srv == nil {
		return
	}
	_ = h.srv.Close() // 关闭监听并断开既有连接（模拟宕机）
	select {
	case <-h.serveExit:
	case <-time.After(2 * time.Second):
	}
}

// restartServer 模拟 hub 宕机后恢复：先关监听（未关则关），再重开同端口监听。
// 既有 http.Server 关闭后不可复用，重开新的。返回新 server 供 Cleanup 关闭。
func (h *hubStack) restartServer() {
	h.t.Helper()
	h.waitOldServe()
	ln, err := net.Listen("tcp", h.ln.addr().String()) // 同端口重开（SO_REUSEADDR）
	if err != nil {
		h.t.Fatalf("重启监听失败: %v", err)
	}
	h.ln.swapListener(ln)
	h.startServing()
}

// stopServer 模拟 hub 宕机：关监听 + 强断全部既有会话连接（进程死亡时 OS 会关
// socket；http.Server.Close 不管 hijacked 的 WebSocket 连接——SPEC 九已知陷阱，
// 测试需显式断开）。
func (h *hubStack) stopServer() {
	h.t.Helper()
	_ = h.srv.Close()
	h.srv = nil
	h.mgr.mu.RLock()
	live := make([]*session, 0, len(h.mgr.sessions))
	for _, s := range h.mgr.sessions {
		live = append(live, s)
	}
	h.mgr.mu.RUnlock()
	for _, s := range live {
		s.beginClose(false)
	}
}

// ---------- 原始 websocket 测试客户端（注册握手/epoch/心跳级用例） ----------

// wsDial 拨号 node 入口。
func wsDial(t *testing.T, url string, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("拨号 %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() }) // CloseNow：不做关闭握手等待（对端可能已死）
	return conn
}

// wsWriteEnv 发送一帧。
func wsWriteEnv(t *testing.T, conn *websocket.Conn, env *protocol.Envelope) {
	t.Helper()
	b, err := protocol.Encode(env)
	if err != nil {
		t.Fatalf("编码帧: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("发送帧 type=%s: %v", env.Type, err)
	}
}

// wsReadEnv 读一帧（超时即 Fatal）。
func wsReadEnv(t *testing.T, conn *websocket.Conn, timeout time.Duration) *protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("读取帧: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("非文本帧")
	}
	env, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("解码帧: %v", err)
	}
	return env
}

// wsReadErr 读帧直至出错（不区分错误来源；仅用于结果被忽略的排空场景）。
func wsReadErr(t *testing.T, conn *websocket.Conn, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return err
		}
	}
}

// wsReadClosed 读帧直至对端关闭连接，返回关闭错误（S1：剔除测试自身超时导致的
// 「假成功」——ctx deadline 先到即 Fatal，不把本地读超时当作对端关闭）。
func wsReadClosed(t *testing.T, conn *websocket.Conn, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("等待对端关闭超时（连接未被 hub 关闭）: %v", err)
			}
			return err
		}
	}
}

// wsReadClosedNoFrame 读帧直至对端关闭；期间收到 forbidden 中任一帧型即 Fatal。
// S1 精确断言：本地读超时（连接未被 hub 关闭）即 Fatal，不把任意读错误当作关闭
// 成功；用于「关闭前排空应答且不得出现某帧（如 register_ack）」的负向断言。
func wsReadClosedNoFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration, forbidden ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("等待对端关闭超时（连接未被 hub 关闭）: %v", err)
			}
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue
		}
		for _, f := range forbidden {
			if env.Type == f {
				t.Fatalf("关闭前收到禁止的帧 type=%s（应为关闭本身）", env.Type)
			}
		}
	}
}

// waitSessionFinished 等待会话全部 goroutine 退出（事件等待替代固定 sleep，
// S1）：session.finish 在 wg.Wait 之后才注销，此处直接等会话 wg 归零——
// 之后该会话不可能再调用任何处理器/写任何状态，负向断言自此确定。
func waitSessionFinished(t *testing.T, s *session, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("会话 goroutine 未在预算内退出")
	}
}

// assertClosedAbnormal 断言关闭错误为非优雅断开（CloseNow：无关闭握手状态码）。
func assertClosedAbnormal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("连接应已关闭")
	}
	if st := websocket.CloseStatus(err); st == websocket.StatusNormalClosure || st == websocket.StatusGoingAway {
		t.Fatalf("应为非优雅断开（无正常关闭状态码）, got %v (%s)", err, st)
	}
}

// assertClosedGraceful 断言关闭错误为优雅关闭握手（关闭状态码为 normal/going away）。
func assertClosedGraceful(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("连接应已关闭")
	}
	if st := websocket.CloseStatus(err); st != websocket.StatusNormalClosure && st != websocket.StatusGoingAway {
		t.Fatalf("应为优雅关闭（normal/going away）, got %v (%s)", err, st)
	}
}

// regEnvelope 构造 register 帧。
func regEnvelope(nodeID, token string, agents []protocol.AgentCard) *protocol.Envelope {
	return &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgRegister,
		MessageID: protocol.NewMessageID(),
		To:        "hub",
		Payload:   mustJSON(protocol.RegisterPayload{NodeID: nodeID, RegisterToken: token, Agents: agents}),
	}
}

// wsRegister 发送 register 并等待 register_ack，返回载荷。
func wsRegister(t *testing.T, conn *websocket.Conn, nodeID, token string, agents []protocol.AgentCard) protocol.RegisterAckPayload {
	t.Helper()
	wsWriteEnv(t, conn, regEnvelope(nodeID, token, agents))
	for {
		env := wsReadEnv(t, conn, 5*time.Second)
		if env.Type == protocol.MsgPing {
			continue
		}
		if env.Type != protocol.MsgRegisterAck {
			t.Fatalf("期待 register_ack, got %s", env.Type)
		}
		var p protocol.RegisterAckPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			t.Fatalf("解析 register_ack: %v", err)
		}
		return p
	}
}

// cardDemo 测试用能力表。
func cardDemo(name string) protocol.AgentCard {
	return protocol.AgentCard{
		Name: name, Kind: protocol.AgentKindCLI, Description: "测试",
		InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}
}

// mustJSON 测试辅助编码。
func mustJSON(v any) []byte {
	b, err := jsonMarshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// contextWithTimeout 测试辅助：后台 ctx + 超时。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// waitFor 轮询直至 cond 成立或超时（替代固定 sleep）。
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
