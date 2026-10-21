package transport

import (
	"sync"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Bus 内存消息总线：命名端点注册表 + 同步分发（SPEC 八.3 内存 transport）。
// Send 即在调用方 goroutine 内同步执行对端 Handler——测试全链因此完全确定
// （无 goroutine 交错）；代价是 Handler 必须可重入且回发链不得持锁。
// Bus 零值不可用，须经 NewBus 构造。
type Bus struct {
	mu  sync.RWMutex
	eps map[string]*Endpoint
}

// NewBus 创建空总线。
func NewBus() *Bus {
	return &Bus{eps: make(map[string]*Endpoint)}
}

// Endpoint 取回（或创建）名为 name 的端点；同名端点在 Bus 生命周期内复用。
// name 即该端发帧的强制身份（From 覆盖值），hub 用 "hub"，node 用 node_id。
func (b *Bus) Endpoint(name string) *Endpoint {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.eps[name]; ok {
		return e
	}
	e := &Endpoint{bus: b, name: name}
	b.eps[name] = e
	return e
}

// get 取端点快照（不校验关闭态——关闭态由 Endpoint.Send/deliver 自查）。
func (b *Bus) get(name string) *Endpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.eps[name]
}

// Endpoint 总线上的一个通信端点，实现 Transport。
type Endpoint struct {
	bus  *Bus
	name string

	mu      sync.Mutex
	handler Handler
	closed  bool
}

// Name 返回端点名（即本端强制身份）。
func (e *Endpoint) Name() string { return e.name }

// EndpointIdentity 本端身份（与 Name 同值；transport 实现的统一身份访问点，
// node service 结果编码计量据此把真实 From 计入信封体积，N2）。
func (e *Endpoint) EndpointIdentity() string { return e.name }

// Send 实现 Transport：编码实测校验上限 → 定位对端 → 以本端身份覆盖 From →
// 同步调用对端 Handler。env 传入后即归传输层所有，Send 返回前调用方不得再修改。
func (e *Endpoint) Send(to string, env *protocol.Envelope) error {
	if env == nil {
		return ErrClosed
	}
	// 与真实传输一致：按最终编码实测单消息上限（SPEC 4.6）
	if _, err := protocol.Encode(env); err != nil {
		return err
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrClosed
	}
	e.mu.Unlock()

	peer := e.bus.get(to)
	if peer == nil {
		return ErrNoEndpoint
	}
	// 身份由传输层强制覆盖（不信帧内值）；浅拷贝隔离调用方信封，
	// Payload（json.RawMessage）按约定不可变，共享安全。
	msg := *env
	msg.From = e.name
	return peer.deliver(&msg)
}

// Subscribe 实现 Transport。
func (e *Endpoint) Subscribe(fn Handler) {
	e.mu.Lock()
	e.handler = fn
	e.mu.Unlock()
}

// Close 实现 Transport；幂等。
func (e *Endpoint) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

// deliver 同步投递到本端 Handler；Handler 调用在锁外（回调内可回发/关闭本端）。
func (e *Endpoint) deliver(env *protocol.Envelope) error {
	e.mu.Lock()
	fn, closed := e.handler, e.closed
	e.mu.Unlock()
	if closed || fn == nil {
		return ErrClosed
	}
	fn(env)
	return nil
}
