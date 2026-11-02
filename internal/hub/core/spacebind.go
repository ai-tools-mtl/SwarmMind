// spacebind.go 中心空间成员映射登记（W07-02，docs/tasks/v3/W07-hub-routing.md
// 「Node 收件门受理面扩展：hub 下发前登记 session↔hub-scope 绑定，node 按绑定
// 放行」）：hub 向目标 node 发 space_bind 帧（协议加法帧；RequireSpacesCapable
// 版本门在本帧调用方执行——未声明空间特性的 node 绝不接收本帧），node 持久登记
// 后回 space_bind_ack，hub 收到 ack 才放行空间轮下发。
//
// AGENTS 帧请求-应答关联铁律逐条兑现（artifact.go 中继同款纪律）：
//   - 登记先于 Send：waiter 先入在途表，随后请求帧携 hub 分配的 mid 上路；
//   - 应答按 ReplyTo 关联：node 回帧 ReplyTo=请求 mid，handleFrame 按键投递；
//   - 键用本端分配编号：mid = protocol.NewMessageID()（不信任何远端提供的键）；
//   - 消费前核验预期来源：env.From ≠ 表内期望 node 的应答不投递不消费（waiter
//     留到超时，伪应答无法占位）；
//   - 超时兜底：调用方预算内未获 ack 即失败（派发侧零副作用，宁拒不瞎投）。
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// spaceBindTimeout 中心绑定登记应答预算默认值（登记是单帧幂等写盘，秒级足够；
// Config.SpaceBindTimeout 可覆盖——测试注入短预算）。
const spaceBindTimeout = 10 * time.Second

// ErrBindTimeout space_bind 应答超时（node 未应答/链路无回；派发侧据此放弃本次
// 下发，意图零登记，重试自会重新登记——登记幂等）。
var ErrBindTimeout = errors.New("core: 中心空间绑定登记应答超时")

// BindReject 目标 node 显式拒绝绑定登记（词形非法/登记落盘失败等——node err 帧
// 人话透出）。确定性失败：同形态重试仍拒，派发侧按取消收束（不再滞留重试）。
type BindReject struct {
	Code string // node 错误码（spaces_unsupported/invalid/bind_store_failed…）
	Msg  string
}

func (e *BindReject) Error() string { return "node 拒绝绑定登记（" + e.Code + "）: " + e.Msg }

// bindWaiter 在途绑定登记：按请求帧 mid 关联 node 回传的 space_bind_ack/err 帧。
type bindWaiter struct {
	node string                  // 期望应答方（目标 node）
	ch   chan *protocol.Envelope // 容量 1：单应答，重复帧丢弃
}

// SpaceBindTimeout 绑定登记应答预算生效值（New 时落定；0 = 默认 10s）。
func (c *Core) SpaceBindTimeout() time.Duration {
	if c.spaceBindTimeout > 0 {
		return c.spaceBindTimeout
	}
	return spaceBindTimeout
}

// SpaceBind 中心空间成员映射登记（W07-02）：向 nodeID 发 space_bind 并等待
// space_bind_ack（timeout 预算；0 = 默认）。ack 载荷回带两字段与请求逐一复核
// （语义核验：不符按异常拒绝，不冒充成功）。错误三态：node 离线（ErrNodeOffline）、
// 超时（ErrBindTimeout）、node 显式拒绝（err 帧人话透出）。幂等：重复登记同键同
// scope 由 node 侧按代次条件生效（见 SpaceBindAck），派发侧重试安全。
func (c *Core) SpaceBind(nodeID string, req protocol.SpaceBindPayload, timeout time.Duration) (SpaceBindAck, error) {
	if timeout <= 0 {
		timeout = c.SpaceBindTimeout()
	}
	mid := protocol.NewMessageID() // 本端分配编号（关联键；不信远端自报 mid）
	w := &bindWaiter{node: nodeID, ch: make(chan *protocol.Envelope, 1)}
	c.muBind.Lock()
	if c.bindClosed {
		c.muBind.Unlock()
		return SpaceBindAck{}, fmt.Errorf("%w: hub 关闭中，暂不受理绑定登记", ErrNodeOffline)
	}
	c.binds[mid] = w // 登记先于 Send（铁律；锁内零 Send——membus 同步回调重入纪律）
	c.muBind.Unlock()
	defer func() {
		c.muBind.Lock()
		delete(c.binds, mid)
		c.muBind.Unlock()
	}()

	// 请求帧 MessageID 即关联键 mid（全仓约定 ReplyTo=请求帧 MessageID——sendFrame
	// 的 MessageID 恒为新随机值只适用应答/推送场景；waiter 键必须随 MessageID 上路，
	// 错放 ReplyTo 会使 node 回帧关联不上恒超时——sendFetchRequest 同款教训锚）。
	b, merr := json.Marshal(req)
	if merr != nil {
		return SpaceBindAck{}, fmt.Errorf("space_bind 载荷编码失败: %w", merr)
	}
	if err := c.tr.Send(nodeID, &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgSpaceBind,
		MessageID: mid,
		To:        nodeID,
		Payload:   b,
	}); err != nil {
		if errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed) {
			return SpaceBindAck{}, fmt.Errorf("%w: %v", ErrNodeOffline, err)
		}
		return SpaceBindAck{}, fmt.Errorf("space_bind 发送失败: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case env := <-w.ch:
			switch env.Type {
			case protocol.MsgSpaceBindAck:
				var p protocol.SpaceBindPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil || p.Space != req.Space ||
					p.SessionID != req.SessionID || p.Generation != req.Generation {
					return SpaceBindAck{}, fmt.Errorf("space_bind 应答内容与请求不符（疑似异常对端，拒绝冒充成功）")
				}
				return SpaceBindAck{Accepted: p.Accepted, CurrentGen: p.CurrentGen}, nil
			case protocol.MsgErr:
				if env.Err != nil {
					return SpaceBindAck{}, &BindReject{Code: env.Err.Code, Msg: env.Err.Msg}
				}
				return SpaceBindAck{}, &BindReject{Code: "rejected", Msg: "node 未附拒绝原因"}
			default:
				continue // 未知帧类型：继续等待合法应答直至超时
			}
		case <-timer.C:
			return SpaceBindAck{}, ErrBindTimeout
		}
	}
}

// SpaceBindAck 绑定登记应答（代次协议——codex 复审轮 2 核心机制 1）。
type SpaceBindAck struct {
	// Accepted 本次登记/解除是否实际生效（代次不符的 ack-noop 为 false）。
	Accepted bool
	// CurrentGen node 当前登记代次（无登记 0；hub 重启后从该事实惰性重建代次
	// 计数——重启恢复从登记事实重建）。
	CurrentGen uint64
}

// SpaceUnbind 中心空间绑定解除（代次协议：解除帧携发起时的绑定代次——node 仅当
// 当前登记代次精确匹配才删除；stale 代次 ack-noop 即语义满足——新代登记已在位，
// 旧代解除不删新登记）。返回 (ack, err) 同 SpaceBind。
func (c *Core) SpaceUnbind(nodeID, sessionID string, generation uint64, timeout time.Duration) (SpaceBindAck, error) {
	return c.SpaceBind(nodeID, protocol.SpaceBindPayload{Space: "", SessionID: sessionID, Generation: generation}, timeout)
}

// deliverBindReply 绑定登记应答投递（ReplyTo 命中在途表则投递；来源不符不投递
// 不消费——waiter 留到超时，伪应答无法占位）。命中返回 true（帧已归本域处理，
// 调用方不再路由）。
func (c *Core) deliverBindReply(env *protocol.Envelope) bool {
	if env == nil || env.ReplyTo == "" {
		return false
	}
	c.muBind.Lock()
	w := c.binds[env.ReplyTo]
	if w != nil && w.node != env.From {
		want := w.node
		c.muBind.Unlock()
		log.Printf("[hub] space_bind 应答来源不符，丢弃 from=%s want=%s reply=%s", env.From, want, env.ReplyTo)
		return true
	}
	c.muBind.Unlock()
	if w == nil {
		return false
	}
	select {
	case w.ch <- env:
	default:
	}
	return true
}

// closeBinds 收束绑定登记域（Core.Close 调用）：置位 + 清表，此后新登记拒绝、
// 在途应答不再投递（调用方预算超时自收敛——waiter 等待在调用方 goroutine，无
// timer 残留）。
func (c *Core) closeBinds() {
	c.muBind.Lock()
	c.bindClosed = true
	c.binds = map[string]*bindWaiter{}
	c.muBind.Unlock()
}
