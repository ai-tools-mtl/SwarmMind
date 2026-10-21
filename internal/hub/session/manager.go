package session

import (
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// frameQueueSize 每会话入站帧缓冲深度（帧已解码，含载荷引用）。处理 goroutine
// 被 core 出站背压阻塞时吸收抖动；打满后读循环阻塞，由 TCP 反压对端。
const frameQueueSize = 32

// defaultCloseBudget Manager.Close（transport.Transport 语义）的默认关闭预算。
const defaultCloseBudget = 5 * time.Second

// errCloseBudgetExceeded 关闭预算耗尽且强断后仍有会话未退出。
var errCloseBudgetExceeded = errors.New("session: 关闭预算内未能结束全部会话")

// Manager hub 会话表与 WSS 入口（SPEC 二/4.4）：实现 transport.Transport——
//   - Send(to, env)：按 node_id 定位会话入队（hub 身份 "hub" 覆盖 From、帧携当前
//     epoch），与 membus 端点可互换，hub core 零改动切换（SPEC 八.3）；
//   - Subscribe(fn)：注册入站帧回调（装配方接 core.HandleFrame），所有会话的
//     合法入站帧都路由到该回调。
//
// 并发纪律：mu 只保护 sessions/all 表与 closed 标志；Send 定位会话后在锁外入队；
// 同 node 的注册提交/会话发布/ack/上下线回调按 node 串行（nodeLock，回调可重入
// Send/Update——不持全局锁调 core，L1/L2）。
type Manager struct {
	cfg Config

	mu       sync.RWMutex
	sessions map[string]*session   // node_id → 当前会话（注册成功的连接）
	all      map[*session]struct{} // 全部已接受连接（含注册中，Close 快照覆盖，L3）
	closed   bool
	handler  transport.Handler

	nodeLocks sync.Map // node_id → *sync.Mutex：同 node 注册/上下线事件串行化（L1/L2）

	wg sync.WaitGroup // 存活会话数（含注册中的连接），Close 等待归零

	// 测试注入点（生产恒 nil）：
	//   hookAfterRegisterPersist 注册事务提交后、会话表发布前（L1 逆序窗口）
	//   hookBeforeCardUpdate     card_update 事务提交前
	hookAfterRegisterPersist func(nodeID string)
	hookBeforeCardUpdate     func(nodeID string)
}

// Config Manager 构造参数。
type Config struct {
	// Store 持久化存储（register 事务：epoch+1 持久化 + 能力表更新，SPEC 4.4）。
	Store *store.Store
	// Tokens node_id → register_token 映射（每 node 独立，token 绑定身份，SPEC 二）。
	Tokens map[string]string
	// SetOnline 节点在线状态回调（装配方接 core.SetOnline；上线即触发补推）。
	// 按 node 串行调用且过期通知已被抑制（L2）：回调方无需自行判序。
	SetOnline func(nodeID string, online bool)
	// RegisterTimeout 注册首帧超时；0 = protocol.RegisterTimeout（测试注入短周期）。
	RegisterTimeout time.Duration
	// PingInterval 应用层心跳 ping 周期；0 = protocol.PingInterval。
	PingInterval time.Duration
	// PongTimeout pong 超时；0 = protocol.PongTimeout。
	PongTimeout time.Duration
}

// NewManager 构造会话管理器。
func NewManager(cfg Config) *Manager {
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]string{}
	}
	return &Manager{cfg: cfg, sessions: map[string]*session{}, all: map[*session]struct{}{}}
}

// nodeLock 取（或建）node 专属串行锁。
func (m *Manager) nodeLock(nodeID string) *sync.Mutex {
	v, _ := m.nodeLocks.LoadOrStore(nodeID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ServeHTTP 实现 /ws/node 入口（http.Handler）：升级为 websocket 连接并交给
// 会话生命周期 goroutine。注册握手在会话 goroutine 内完成（5s 首帧超时）。
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	conn.SetReadLimit(protocol.MaxMessageBytes + readLimitSlack)

	s := newSession(m, conn)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "hub shutting down")
		return
	}
	// Add 与 closed 检查同锁：保证与 Close 的 wg.Wait 之间有 happens-before
	//（WaitGroup 要求 Add 先于 Wait 或计数非零，且不得并发）
	m.wg.Add(1)
	m.all[s] = struct{}{} // L3：注册中的连接同样纳入跟踪，关闭快照不漏
	m.mu.Unlock()

	go s.run()
}

// Send 实现 transport.Transport（hub 端点，身份 "hub"）：定位目标 node 的当前
// 会话 → 按最终编码实测校验上限 → 以 hub 身份覆盖 From、携会话当前 epoch →
// 按帧类型入控制/数据队列。会话不存在（未注册/已掉线）返回 transport.ErrNoEndpoint；
// 数据队列满时阻塞等待空位（背压传导到 core 出队），队列关闭返回错误。
func (m *Manager) Send(to string, env *protocol.Envelope) error {
	if env == nil {
		return transport.ErrClosed
	}
	m.mu.RLock()
	s := m.sessions[to]
	m.mu.RUnlock()
	if s == nil {
		return transport.ErrNoEndpoint
	}
	// 身份与代次由传输层强制（SPEC 4.1/4.4）：node 校验入站 epoch，须携当前值。
	// 先盖章后编码——WSS 链路上传输的是字节，与 membus 传对象不同，盖章必须
	// 发生在序列化之前。
	msg := *env
	msg.From = "hub"
	msg.Version = protocol.ProtocolVersion
	msg.SessionEpoch = s.epoch
	b, err := protocol.Encode(&msg)
	if err != nil {
		return err
	}
	if isControlType(msg.Type) {
		return s.q.pushCtrl(b)
	}
	return s.q.pushData(b, protocol.SendStallTimeout)
}

// Subscribe 实现 transport.Transport：注册入站帧回调（重复注册以最后一次为准）。
func (m *Manager) Subscribe(fn transport.Handler) {
	m.mu.Lock()
	m.handler = fn
	m.mu.Unlock()
}

// inboundHandler 返回当前入站回调。
func (m *Manager) inboundHandler() transport.Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.handler
}

// Close 优雅关闭（transport.Transport 语义）：以默认预算执行 CloseWithBudget。
func (m *Manager) Close() error {
	return m.CloseWithBudget(defaultCloseBudget)
}

// CloseWithBudget 优雅关闭并遵守关闭预算（SPEC 九：先遍历会话发 Close 帧再等
// 退出）：关闭封住新会话发布（注册中的连接注册结果直接判负），对全部已接受
// 连接（含注册中，L3）走优雅关闭（排空控制帧 → websocket 关闭握手）并等待
// 退出；预算耗尽则强断（取消在途读写、CloseNow）后再等一轮。此后 Send 一律
// 报端点不存在。Store 与 HTTP server 的关闭归装配方。
func (m *Manager) CloseWithBudget(budget time.Duration) error {
	if budget <= 0 {
		budget = defaultCloseBudget
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	live := make([]*session, 0, len(m.all))
	for s := range m.all {
		live = append(live, s)
	}
	m.mu.Unlock()
	for _, s := range live {
		s.beginClose(true) // 排空控制帧后关闭握手
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		// 预算耗尽：强断剩余会话（取消在途读写/降级优雅排空），再给一轮预算收尾
		for _, s := range live {
			s.forceClose()
		}
		select {
		case <-done:
			return nil
		case <-time.After(budget):
			return errCloseBudgetExceeded
		}
	}
}

// replaceSession 注册事务成功后会话表替换：同 node 的旧连接立即关闭（SPEC 4.4
// 「会话表替换旧连接 → 旧会话清理」）。旧会话退出经 removeSession 时发现表中
// 已非自己，不会误发下线回调。Manager 已关闭时拒绝发布（L3：关停后的注册不得
// 入表、不得上线），返回 false 由调用方放弃并断开。
func (m *Manager) replaceSession(s *session) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	old := m.sessions[s.nodeID]
	m.sessions[s.nodeID] = s
	m.mu.Unlock()
	if old != nil {
		log.Printf("[hub] 旧会话被替换，关闭 node=%s", s.nodeID)
		old.beginClose(false)
	}
	return true
}

// removeSession 会话退出注销：从全部连接跟踪表摘除（wg 随之归账）；仅当表中
// 仍是本会话时摘除会话表并回调下线（避免被替换的旧会话误判节点离线）。
// L2：判定与下线回调按 node 串行——新会话注册（上线回调）与本旧会话的下线
// 回调顺序确定，锁外回调不再可逆序覆盖；过期（cur≠s）的下线通知被抑制。
func (m *Manager) removeSession(s *session) {
	if s.nodeID == "" { // 未完成注册的连接：无会话表项、无在线状态
		m.mu.Lock()
		delete(m.all, s)
		m.mu.Unlock()
		m.wg.Done()
		return
	}
	nodeMu := m.nodeLock(s.nodeID)
	nodeMu.Lock()
	m.mu.Lock()
	delete(m.all, s)
	cur := m.sessions[s.nodeID]
	if cur == s {
		delete(m.sessions, s.nodeID)
	}
	m.mu.Unlock()
	if cur == s && m.cfg.SetOnline != nil {
		m.cfg.SetOnline(s.nodeID, false)
	}
	nodeMu.Unlock()
	m.wg.Done()
}

// replaced 报告会话是否已被同 node 新连接替换（processLoop 残余帧失效判定，L1）。
func (m *Manager) replaced(s *session) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[s.nodeID] != s
}

// isClosed 报告 Manager 是否已进入关闭态（L3：等锁路径与发布/回调路径的关闭态
// 复核用——node 锁可能被另一注册的完整提交持有较久，等锁期间 Close 可插入）。
func (m *Manager) isClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

// currentEpoch 查询 node 当前持久化代次（入站帧 epoch 校验基准，SPEC 4.4：
// 「不等于该 node 当前 epoch」——以 store 为真相源，被替换的旧连接读到新代次，
// 残余帧因此被忽略）。
func (m *Manager) currentEpoch(nodeID string) int64 {
	return m.cfg.Store.View().Nodes[nodeID].Epoch
}

// liveSessionsCount 当前会话表条数（测试观测用）。
func (m *Manager) liveSessionsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// regTimeout / pingInterval / pongTimeout 配置读取（0 回落协议常量）。
func (m *Manager) regTimeout() time.Duration {
	if m.cfg.RegisterTimeout > 0 {
		return m.cfg.RegisterTimeout
	}
	return protocol.RegisterTimeout
}

func (m *Manager) pingInterval() time.Duration {
	if m.cfg.PingInterval > 0 {
		return m.cfg.PingInterval
	}
	return protocol.PingInterval
}

func (m *Manager) pongTimeout() time.Duration {
	if m.cfg.PongTimeout > 0 {
		return m.cfg.PongTimeout
	}
	return protocol.PongTimeout
}

// isControlType hub→node 控制帧（走 64KiB 独立小缓冲，SPEC 4.6）：心跳应答、
// 各类确认与错误帧；task_offer/result_event/task_status 等数据帧走 4MiB 大队列。
func isControlType(typ string) bool {
	switch typ {
	case protocol.MsgPing, protocol.MsgPong, protocol.MsgRegisterAck,
		protocol.MsgResultAck, protocol.MsgSubmitAck, protocol.MsgErr:
		return true
	}
	return false
}
