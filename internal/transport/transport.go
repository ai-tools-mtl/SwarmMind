// Package transport 定义 SwarmMind 消息传输抽象（SPEC 八.3：先用内存 transport
// 验证任务协议，再接 WebSocket——本包接口即后续 WSS 会话的替换点）。
//
// 契约：
//   - 身份由传输层强制：Send 以发送端的本端身份覆盖信封 From（对齐 SPEC 4.1
//     「hub 入站方向强制以连接鉴权身份覆盖，不信帧内值」），接收方可以信任 From。
//   - 帧按最终编码实测校验单消息上限（SPEC 4.6：完整信封序列化后 ≤1MiB）。
//   - Send 阻塞直至帧交达对端（WSS 实现为入队+写串行化，见 SPEC 4.6 发送队列）；
//     处理方不得在处理帧期间持有会阻塞回发的锁（内存实现为同步分发，回发即嵌套调用）。
package transport

import (
	"errors"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 传输层错误哨兵（errors.Is 判定）。
var (
	// ErrNoEndpoint 目标端点不存在（未注册/已注销）。
	ErrNoEndpoint = errors.New("transport: 目标端点不存在")
	// ErrClosed 本端端点已关闭。
	ErrClosed = errors.New("transport: 端点已关闭")
)

// Handler 入站帧回调。实现方必须可重入（回调内可再次 Send），且不得长期阻塞。
type Handler func(env *protocol.Envelope)

// Transport 一端的消息传输接口（hub 与 node 各持一个端点实例）。
// WSS 会话层实现本接口后，hub core / node 调度层零改动切换。
type Transport interface {
	// Send 发送一帧给 to（身份 = node_id 或 "hub"）。
	// 传输层以本端身份覆盖 env.From；返回非 nil 错误表示帧未交达对端。
	Send(to string, env *protocol.Envelope) error
	// Subscribe 注册本端入站帧回调；重复注册以最后一次为准。
	Subscribe(fn Handler)
	// Close 关闭本端端点；此后 Send 报 ErrClosed，不再接收帧。
	Close() error
}
