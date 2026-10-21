// Package conn 实现 node 外连客户端（SPEC 二/4.4/4.6/九）：dial hub_url（ws/wss）、
// 注册握手（register 携 token+能力表 → register_ack 记 epoch；auth_failed 不重试）、
// 指数退避重连（0.5s×2 ±20% 抖动上限 60s，注册成功重置）、应用层心跳、入站帧
// epoch 校验（不匹配断开重连）、帧分发到订阅者，并实现 transport.Transport——
// 与 membus 端点可互换，node 调度层零改动切换（SPEC 八.3）。
//
// 每次连接尝试的 goroutine 布局与退出：写 goroutine 是连接的唯一写方（串行化），
// 退出时执行 conn.Close（解除读阻塞）；读循环退出即触发队列关闭与 attempt 收尾。
// 生命周期信号统一是连接队列的 done 通道与 attempt ctx（非优雅关闭即取消，
// L4：在途 Read/Write/握手等待随之中断）。
package conn

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// readLimitSlack 与 hub session 同口径：略超 1MiB 的帧由协议层回 err 帧而非
// ws 层直接拒收。
const readLimitSlack = 4096

// frameQueueSize 入站帧缓冲深度（与 hub session 同口径）。
const frameQueueSize = 32

// dialTimeout 拨号超时（调度节拍，非协议常量）。
const dialTimeout = 30 * time.Second

// registerAckTimeout 等待 register_ack 超时（调度节拍，非协议常量；hub 侧注册
// 处理为亚秒级，10s 足够宽容）。
const registerAckTimeout = 10 * time.Second

// ErrAuthFailed register 被拒（auth_failed）：不重试，Run 终止返回本错误
// （SPEC 4.6：auth_failed 不重试；errors.Is 判定）。
var ErrAuthFailed = errors.New("conn: register 鉴权失败")

// ErrRegisterTooLarge register 帧编码后超过 1MiB 上限（N1）：本地配置错误（能力表
// 过大），重连无意义，Run 立即返回本错误、不重试（errors.Is 判定）。
var ErrRegisterTooLarge = errors.New("conn: register 帧超过 1MiB 上限（能力表过大）")

// errRunAlreadyRunning Run 已在运行（N3：Run 只允许一个调用方）。
var errRunAlreadyRunning = errors.New("conn: Run 已在运行")

// errClosedByClose Close 已调用（N3：发布连接前复核关闭态的确定性失败）。
var errClosedByClose = errors.New("conn: 客户端已关闭")

// Config Client 构造参数。
type Config struct {
	// HubURL hub 地址（ws:// 或 wss://，含路径 /ws/node；http/https 自动改写）。
	HubURL string
	// NodeID 节点身份（token 与之绑定）。
	NodeID string
	// Token 注册令牌（每次连接全量鉴权，SPEC 二）。
	Token string
	// Agents 注册上报的能力表（由 ExecProfile 生成，永不携带本机执行配置）。
	Agents []protocol.AgentCard
	// AgentsFunc 当前能力表提供者（Loop 7 exposed 开关：重连注册须携运行时最新
	// 状态，不能用构造时的旧表覆盖 hub 注册表）；非空时优先于 Agents。
	AgentsFunc func() []protocol.AgentCard
	// TLSSkipVerify wss 跳过证书校验（仅开发/测试）。
	TLSSkipVerify bool
	// PubkeyFP hub 证书 SPKI SHA-256（base64，SPEC 二）：非空 = 强校验模式，
	// TLS 握手时比对，不匹配即失败（register_token 不出本机）。
	PubkeyFP string
	// TrustStore TOFU 信任记录（PubkeyFP 为空时的校验方式，SPEC 二：
	// data_dir/trusted_hubs.json，host:port→fp，首连成功记录，此后不匹配断开）。
	// 两者均未配置时拒绝 wss 连接（无身份校验的 TLS 不放行）。
	TrustStore *TrustStore
	// PingInterval 心跳 ping 周期；0 = protocol.PingInterval（测试注入短周期）。
	PingInterval time.Duration
	// PongTimeout pong 超时；0 = protocol.PongTimeout。
	PongTimeout time.Duration
	// OnRegistered 每次注册成功回调（重连成功后立即重发账本未确认结果，SPEC 4.5）。
	OnRegistered func()
}

// Client node 外连客户端，实现 transport.Transport（本端身份 = NodeID，对端 "hub"）。
type Client struct {
	cfg Config

	mu      sync.Mutex
	q       *outboundQ // 当前连接发送队列；nil = 未连接（Send 报端点不存在）
	epoch   int64      // 当前会话代次（register_ack 成功后记录，SPEC 4.4）
	handler transport.Handler
	// closedState / running / cancelRun 同受 mu 保护（N3：关闭状态读写单锁，
	// 杜绝 Run/Close 并发 data race）。
	closedState bool
	running     bool
	cancelRun   context.CancelFunc

	lastReg  atomic.Int64 // 最近一次注册成功的 UnixNano（最近同步时间暴露）
	lastPong atomic.Int64 // 上次收到 pong 的 UnixNano（心跳计时基准，SPEC 4.6）
	// dialGate 测试注入点（生产恒 nil）：非空时 attempt 在拨号返回后、发布连接
	// 前等待该通道（N3：覆盖 Close 落在拨号返回与发布之间的窗口）。
	dialGate chan struct{}

	closedOnce sync.Once
}

// NewClient 构造客户端。
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Run 重连主循环：拨号 → 注册 → 服务直至断开 → 退避重试，直至 ctx 取消；
// 鉴权失败（ErrAuthFailed）与本地配置错误（ErrRegisterTooLarge）不重试、立即
// 返回（SPEC 4.6）。ctx 取消返回 nil。并发调用返回 errRunAlreadyRunning（N3）。
func (c *Client) Run(ctx context.Context) error {
	// N2：node_id 有界（From 计入信封体积；超长属本地配置错误，不进入重试）
	if len(c.cfg.NodeID) > protocol.MaxNodeIDLen {
		return fmt.Errorf("conn: node_id 长度 %d 字节超过上限 %d", len(c.cfg.NodeID), protocol.MaxNodeIDLen)
	}
	u, err := normalizeURL(c.cfg.HubURL)
	if err != nil {
		return err // 配置错误，不进入重试
	}
	c.mu.Lock()
	if c.closedState {
		c.mu.Unlock()
		return transport.ErrClosed
	}
	if c.running {
		c.mu.Unlock()
		return errRunAlreadyRunning
	}
	c.running = true
	runCtx, cancel := context.WithCancel(ctx)
	c.cancelRun = cancel
	runDone := make(chan struct{})
	c.mu.Unlock()
	defer func() {
		cancel()
		// N3：先等本 Run 的取消 watcher 退出再清 running——watcher 退出前可能
		// 尚未执行 dropCurrent，若先放开 running，新 Run 发布的新连接会被旧
		// watcher 的 dropCurrent 误关。
		<-runDone
		c.mu.Lock()
		c.running = false
		c.cancelRun = nil
		c.mu.Unlock()
	}()

	// ctx 取消传导：断开当前连接（写 goroutine 随之 Close 连接，解除读阻塞），
	// attempt 得以返回、Run 得以退出
	go func() {
		defer close(runDone)
		<-runCtx.Done()
		c.dropCurrent()
	}()

	bo := NewBackoff(0, 0, 0) // 协议常量：0.5s ×2 ±20% 上限 60s
	for {
		if err := runCtx.Err(); err != nil {
			return nil
		}
		err := c.attempt(runCtx, u, bo)
		if errors.Is(err, ErrAuthFailed) {
			return err // token 错误重连无意义（SPEC 4.6）
		}
		if errors.Is(err, ErrRegisterTooLarge) {
			return err // 本地配置错误，重连无意义（N1）
		}
		if runCtx.Err() != nil {
			return nil
		}
		if errors.Is(err, errClosedByClose) {
			return transport.ErrClosed // Close 后不再进入重连循环（N3）
		}
		if err != nil {
			log.Printf("[node] 连接断开，%s 后重连: %v", bo.Preview(), err)
		}
		Sleep(runCtx, bo.Next())
	}
}

// Close 停止客户端：终止 Run 与当前连接（幂等）。
func (c *Client) Close() error {
	c.closedOnce.Do(func() {
		c.mu.Lock()
		c.closedState = true
		cancel := c.cancelRun
		c.mu.Unlock()
		c.dropCurrent()
		if cancel != nil {
			cancel()
		}
	})
	return nil
}

// dropCurrent 断开当前连接的发送队列（若已连接）：写 goroutine 退出时执行
// conn.Close，解除本侧一切读阻塞。
func (c *Client) dropCurrent() {
	c.mu.Lock()
	q := c.q
	c.mu.Unlock()
	if q != nil {
		q.close(false)
	}
}

// Send 实现 transport.Transport：to 必须为 "hub"（V0 拓扑只有 hub 一个对端）；
// 未连接 = hub 端点不存在（transport.ErrNoEndpoint，与 membus 语义对齐）。
// 以本端身份覆盖 From、携当前会话 epoch（SPEC 4.1/4.4），按帧类型入
// 控制/数据队列；数据队列满阻塞等待空位（背压），持续停滞断开重连。
func (c *Client) Send(to string, env *protocol.Envelope) error {
	if env == nil {
		return transport.ErrClosed
	}
	if to != "hub" {
		return transport.ErrNoEndpoint
	}
	c.mu.Lock()
	q := c.q
	epoch := c.epoch
	c.mu.Unlock()
	if q == nil {
		return transport.ErrNoEndpoint
	}
	// 身份与代次由传输层强制（SPEC 4.1/4.4）；先盖章后编码（WSS 传字节，
	// membus 传对象——盖章必须发生在序列化之前）。
	msg := *env
	msg.From = c.cfg.NodeID
	msg.Version = protocol.ProtocolVersion
	msg.SessionEpoch = epoch
	b, err := protocol.Encode(&msg)
	if err != nil {
		return err
	}
	if isControlType(msg.Type) {
		return q.pushCtrl(b)
	}
	return q.pushData(b, protocol.SendStallTimeout)
}

// Subscribe 实现 transport.Transport：注册入站帧回调（重复注册以最后一次为准）。
func (c *Client) Subscribe(fn transport.Handler) {
	c.mu.Lock()
	c.handler = fn
	c.mu.Unlock()
}

// LastRegisteredAt 返回最近一次注册成功时刻（最近同步时间暴露）；
// 从未注册成功返回 false。
func (c *Client) LastRegisteredAt() (time.Time, bool) {
	ns := c.lastReg.Load()
	if ns == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// Epoch 返回当前会话代次（未注册为 0）。
func (c *Client) Epoch() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// Online 报告当前是否存在已发布连接（注册握手中即视为未在线；本地 API 目录展示用）。
func (c *Client) Online() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.q != nil
}

// EndpointIdentity 本端身份（=NodeID，Send 盖章的 From；node service 结果编码
// 计量据此把真实 From 计入信封体积，N2）。
func (c *Client) EndpointIdentity() string { return c.cfg.NodeID }

// attempt 一次连接尝试：拨号 → 写 goroutine → 注册握手 → 心跳/读/处理 goroutine
// → 服务直至断开。返回断开原因；鉴权失败返回 ErrAuthFailed（包一层保原因可链）。
// 全程受 attempt ctx 约束（L4）：取消（Run 退出/Close）即中止在途读写与握手等待。
func (c *Client) attempt(ctx context.Context, u string, bo *Backoff) error {
	dialOpts := &websocket.DialOptions{}
	// TLS 身份校验（SPEC 二）：wss 且非 skip 模式必须配指纹——PubkeyFP 强校验，
	// 否则 TOFU（首连成功记录，此后不匹配拒绝）。tofuFP 由 verify 回调在「无记录
	// 首连」时置位，注册成功后落盘。
	var trustKey, tofuFP string
	if isWSS(u) {
		switch {
		case c.cfg.TLSSkipVerify: // 仅开发/测试：跳过校验
			dialOpts.HTTPClient = &http.Client{
				Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			}
		default:
			key, kerr := trustHostPort(u)
			if kerr != nil {
				return kerr // 本地配置错误，不进入重试
			}
			trustKey = key
			tc := &tls.Config{InsecureSkipVerify: true} // 证书链校验由指纹校验替代（自签场景）
			tc.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				cert, err := parsePeerCert(rawCerts)
				if err != nil {
					return err
				}
				fp := FingerprintSPKI(cert.RawSubjectPublicKeyInfo)
				if c.cfg.PubkeyFP != "" {
					return VerifyFingerprint(fp, c.cfg.PubkeyFP)
				}
				if c.cfg.TrustStore == nil {
					return fmt.Errorf("wss 连接未配置身份校验（hub_pubkey_fp 与 TOFU 记录均缺失），拒绝连接")
				}
				if old, ok := c.cfg.TrustStore.Get(trustKey); ok {
					if subtle.ConstantTimeCompare([]byte(old), []byte(fp)) != 1 {
						return fmt.Errorf("TOFU 校验失败：hub=%s 证书指纹变更（记录 %s 实得 %s）——可能中间人或 hub 重装，拒绝连接", trustKey, old, fp)
					}
					return nil
				}
				tofuFP = fp // 首连：注册成功后记录（SPEC 二）
				return nil
			}
			dialOpts.HTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}
		}
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, u, dialOpts)
	cancelDial()
	if err != nil {
		return fmt.Errorf("dial %s: %w", u, err)
	}
	conn.SetReadLimit(protocol.MaxMessageBytes + readLimitSlack)

	attCtx, attCancel := context.WithCancel(ctx)
	defer attCancel()

	// N1：握手前实测完整编码体积——超限直接返回可诊断配置错误，不重试不 panic
	cards := c.cfg.Agents
	if c.cfg.AgentsFunc != nil {
		cards = c.cfg.AgentsFunc() // 每次连接取运行时能力表（exposed 开关实时状态）
	}
	reg := &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgRegister,
		MessageID: protocol.NewMessageID(),
		From:      c.cfg.NodeID,
		To:        "hub",
		Payload:   mustMarshalPayload(protocol.RegisterPayload{NodeID: c.cfg.NodeID, RegisterToken: c.cfg.Token, Agents: cards}),
	}
	regBytes, err := protocol.Encode(reg)
	if err != nil {
		_ = conn.CloseNow()
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			return fmt.Errorf("%w: %v", ErrRegisterTooLarge, err)
		}
		return fmt.Errorf("register 编码失败: %w", err)
	}

	if c.dialGate != nil { // 测试屏障：Close 落在拨号返回与发布之间的窗口
		<-c.dialGate
	}

	// N3：发布连接与关闭态复核同一临界区——Close 落在拨号返回与发布之间时，
	// 不得再发布可用连接（否则关闭覆盖不到这条新连接）
	q := newOutboundQ()
	c.mu.Lock()
	if c.closedState || attCtx.Err() != nil {
		c.mu.Unlock()
		_ = conn.CloseNow()
		return errClosedByClose
	}
	c.q = q
	c.epoch = 0
	c.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go c.writeLoop(conn, q, attCtx, &wg)
	// L4：队列非优雅关闭即取消 attempt ctx——在途 Write/Read/握手等待立即中断
	go func() {
		<-q.done
		if _, grace := q.state(); !grace {
			attCancel()
		}
	}()
	defer func() {
		// C1：已按优雅关闭的队列不降级——错误版本 err 帧正等待写 goroutine 排空
		// 送达后再断开；其余路径（读失败/取消/停滞）维持非优雅强断语义不变
		if closed, grace := q.state(); !(closed && grace) {
			q.close(false)
		}
		wg.Wait() // 写 goroutine 末尾 conn.Close，解除本侧读阻塞
		c.mu.Lock()
		if c.q == q {
			c.q = nil
			c.epoch = 0
		}
		c.mu.Unlock()
	}()

	// 注册握手（SPEC 4.2/4.4）：发 register 携 token+能力表 → 等 register_ack
	if err := q.pushHandshake(regBytes); err != nil {
		return fmt.Errorf("register 发送失败: %w", err)
	}
	epoch, err := awaitRegisterAck(attCtx, conn, q, c.cfg.NodeID, registerAckTimeout)
	if err != nil {
		return err // 拒绝（ErrAuthFailed 包装）或读失败/超时
	}
	c.mu.Lock()
	c.epoch = epoch
	c.mu.Unlock()
	c.lastReg.Store(time.Now().UnixNano())
	bo.Reset() // 注册成功重置退避（SPEC 4.6）
	log.Printf("[node] 注册成功 node=%s epoch=%d", c.cfg.NodeID, epoch)

	// TOFU 首连记录（SPEC 二：首连成功将 host:port→fp 记入 trusted_hubs.json；
	// 写失败仅告警不阻断）
	if tofuFP != "" {
		if err := c.cfg.TrustStore.Put(trustKey, tofuFP); err != nil {
			log.Printf("[node] TOFU 记录写失败（仅告警不阻断）hub=%s: %v", trustKey, err)
		} else {
			log.Printf("[node] TOFU 首连已记录 hub=%s fp=%s", trustKey, tofuFP)
		}
		tofuFP = ""
	}

	// 服务阶段：读/处理/心跳三 goroutine，直至连接断开
	frameCh := make(chan *protocol.Envelope, frameQueueSize)
	readDone := make(chan error, 1)
	wg.Add(3)
	go c.readLoop(attCtx, conn, q, frameCh, readDone, &wg)
	go c.processLoop(frameCh, q, &wg)
	go c.heartbeat(q, &wg)
	if c.cfg.OnRegistered != nil {
		c.cfg.OnRegistered() // 重连成功立即重发未确认结果（SPEC 4.5）
	}
	return <-readDone
}

// awaitRegisterAck 注册阶段内联读：等 register_ack（携新 epoch）；其他帧忽略。
// C1：合法信封必须携当前协议版本，不满足先回 err(unsupported_version)（经优雅
// 排空送达对端）再握手失败断开重连。读受 parent attempt ctx 约束（L4：取消立即
// 返回）。
func awaitRegisterAck(parent context.Context, ws *websocket.Conn, q *outboundQ, from string, timeout time.Duration) (int64, error) {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(parent, time.Until(deadline))
		typ, data, err := ws.Read(ctx)
		cancel()
		if err != nil {
			if parent.Err() != nil {
				return 0, fmt.Errorf("等待 register_ack（attempt 已取消）: %w", parent.Err())
			}
			if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
				return 0, protocol.ErrFrameTooLarge
			}
			return 0, fmt.Errorf("等待 register_ack: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		env, err := protocol.Decode(data)
		if err != nil {
			continue // 非法帧忽略（注册阶段无法判定归属）
		}
		if env.Type != protocol.MsgRegisterAck {
			continue
		}
		if env.Version != protocol.ProtocolVersion {
			replyUnsupportedVersion(q, from, 0, env.Version)
			return 0, fmt.Errorf("register_ack 协议版本不支持 v=%d（当前 v=%d）", env.Version, protocol.ProtocolVersion)
		}
		var p protocol.RegisterAckPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return 0, fmt.Errorf("register_ack 载荷解析失败: %w", err)
		}
		if !p.OK {
			// 拒绝（auth_failed）：Run 层不重试（SPEC 4.6）
			return 0, fmt.Errorf("%w: %s", ErrAuthFailed, p.Reason)
		}
		return p.Epoch, nil
	}
}

// replyUnsupportedVersion 回 err(unsupported_version) 并按优雅模式关闭队列（C1/
// SPEC 4.1：先回 err 帧再断开）：写 goroutine 排空该帧送达对端后立即断开，
// attempt 收尾不降级已优雅关闭的队列。
func replyUnsupportedVersion(q *outboundQ, from string, epoch int64, gotVersion int) {
	_ = q.pushCtrl(encodeFrame(errEnvelope(from, protocol.CodeUnsupportedVersion,
		fmt.Sprintf("协议版本不支持 v=%d（当前 v=%d）", gotVersion, protocol.ProtocolVersion), epoch)))
	q.close(true)
}

// readLoop 入站读循环（SPEC 4.4 node 视角）：版本校验（不支持→断开重连，C1）→
// epoch 不匹配 → 断开并按新连接重连；ping/pong 本地处理；其余帧交处理 goroutine
// 分发给订阅者。退出时向 readDone 报告断开原因（本 goroutine 是 readDone 唯一发送方）。
func (c *Client) readLoop(parent context.Context, ws *websocket.Conn, q *outboundQ, frameCh chan *protocol.Envelope, readDone chan error, wg *sync.WaitGroup) {
	defer wg.Done()
	err := func() error {
		for {
			env, err := readFrame(parent, ws, protocol.ReadWriteTimeout)
			if err != nil {
				if parent.Err() != nil {
					return fmt.Errorf("连接已取消: %w", parent.Err())
				}
				if errors.Is(err, protocol.ErrFrameTooLarge) {
					_ = q.pushCtrl(encodeFrame(errEnvelope(c.cfg.NodeID, protocol.CodeFrameTooLarge, "帧超过 1MiB 上限", c.muEpoch())))
					continue
				}
				return err
			}
			// C1：服务阶段每个合法信封仍须校验协议版本，不满足先回
			// err(unsupported_version)（优雅排空送达）再断开重连
			if env.Version != protocol.ProtocolVersion {
				log.Printf("[node] 入站帧协议版本不支持 v=%d type=%s（回 err 后断开重连）", env.Version, env.Type)
				replyUnsupportedVersion(q, c.cfg.NodeID, c.muEpoch(), env.Version)
				return fmt.Errorf("入站帧协议版本不支持 v=%d type=%s（断开重连）", env.Version, env.Type)
			}
			if env.SessionEpoch != c.muEpoch() {
				err := fmt.Errorf("入站帧 epoch 不匹配 got=%d want=%d type=%s（断开重连）",
					env.SessionEpoch, c.muEpoch(), env.Type)
				log.Printf("[node] %v", err)
				return err
			}
			switch env.Type {
			case protocol.MsgPing:
				_ = q.pushCtrl(encodeFrame(&protocol.Envelope{
					Version:      protocol.ProtocolVersion,
					Type:         protocol.MsgPong,
					MessageID:    protocol.NewMessageID(),
					From:         c.cfg.NodeID,
					To:           "hub",
					ReplyTo:      env.MessageID,
					SessionEpoch: c.muEpoch(),
				}))
			case protocol.MsgPong:
				c.lastPong.Store(time.Now().UnixNano())
			default:
				select {
				case frameCh <- env:
				case <-q.done:
					return errors.New("连接已关闭")
				}
			}
		}
	}()
	readDone <- err
}

// processLoop 入站帧处理 goroutine：按到达顺序调用订阅回调（node 侧任务调度）。
func (c *Client) processLoop(frameCh chan *protocol.Envelope, q *outboundQ, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case env := <-frameCh:
			c.mu.Lock()
			fn := c.handler
			c.mu.Unlock()
			if fn != nil {
				fn(env)
			}
		case <-q.done:
			return
		}
	}
}

// heartbeat 应用层心跳（SPEC 4.6）：每 PingInterval 发 ping；上次 pong 距今
// 超 PongTimeout → 断开重连。
func (c *Client) heartbeat(q *outboundQ, wg *sync.WaitGroup) {
	defer wg.Done()
	c.lastPong.Store(time.Now().UnixNano())
	interval := c.cfg.PingInterval
	if interval <= 0 {
		interval = protocol.PingInterval
	}
	pongTimeout := c.cfg.PongTimeout
	if pongTimeout <= 0 {
		pongTimeout = protocol.PongTimeout
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-q.done:
			return
		case <-tk.C:
			if err := q.pushCtrl(encodeFrame(&protocol.Envelope{
				Version:      protocol.ProtocolVersion,
				Type:         protocol.MsgPing,
				MessageID:    protocol.NewMessageID(),
				From:         c.cfg.NodeID,
				To:           "hub",
				SessionEpoch: c.muEpoch(),
			})); err != nil {
				log.Printf("[node] 心跳 ping 发送失败: %v", err)
				return
			}
			age := time.Since(time.Unix(0, c.lastPong.Load()))
			if age >= pongTimeout {
				log.Printf("[node] 心跳超时（%s 未收到 pong），断开重连", age.Round(time.Millisecond))
				q.close(false)
				return
			}
		}
	}
}

// writeLoop 专职写 goroutine（唯一写方）。L4：每次取帧前先处理关闭态——非优雅
// 关闭立即 CloseNow（丢弃队列残余、取消在途写），不在已死队列上继续排空数据帧。
// 优雅关闭（C1：err 帧断开路径）先排空控制帧（必达对端：控制帧先于 FIN 写出，
// 对端未读也留在接收缓冲）再 CloseNow——不做关闭握手等待：库内握手对不应答的
// 对端固定拖 5s 且 CloseNow 无法中断已启动的握手（waitGoroutines 只能等其退出），
// 会拖住重连节拍。每帧写超时 ReadWriteTimeout（attempt ctx 取消即中止在途写）。
func (c *Client) writeLoop(ws *websocket.Conn, q *outboundQ, attCtx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		if closed, grace := q.state(); closed {
			if grace {
				for _, b := range q.drainCtrl() {
					wctx, cancel := context.WithTimeout(attCtx, protocol.ReadWriteTimeout)
					_ = ws.Write(wctx, websocket.MessageText, b)
					cancel()
				}
				_ = ws.CloseNow()
			} else {
				// 非优雅（重连/停滞/epoch 失配/读失败/取消）：立即断，解除并发 Read 阻塞
				_ = ws.CloseNow()
			}
			return
		}
		if b := q.pop(); b != nil {
			wctx, cancel := context.WithTimeout(attCtx, protocol.ReadWriteTimeout)
			err := ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				log.Printf("[node] 帧写出失败: %v", err)
				q.close(false)
			}
			continue
		}
		select {
		case <-q.dataReady:
		case <-q.done:
		}
	}
}

// muEpoch 锁内读当前 epoch。
func (c *Client) muEpoch() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// readFrame 读一条文本帧并解码；超限转译为 ErrFrameTooLarge。读受 parent
// attempt ctx 约束（L4：取消立即解除 Read 阻塞）。
func readFrame(parent context.Context, ws *websocket.Conn, timeout time.Duration) (*protocol.Envelope, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	typ, data, err := ws.Read(ctx)
	if err != nil {
		if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
			return nil, protocol.ErrFrameTooLarge
		}
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errors.New("conn: 非文本帧")
	}
	return protocol.Decode(data)
}

// errEnvelope 构造 node→hub 错误帧（from=本端真实身份）。
func errEnvelope(from, code, msg string, epoch int64) *protocol.Envelope {
	return &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgErr,
		MessageID:    protocol.NewMessageID(),
		From:         from,
		To:           "hub",
		SessionEpoch: epoch,
		Err:          &protocol.ErrInfo{Code: code, Msg: msg},
	}
}

// encodeFrame 信封编码；失败属不可达，防御性 panic。
func encodeFrame(env *protocol.Envelope) []byte {
	b, err := protocol.Encode(env)
	if err != nil {
		panic("conn: 信封编码失败: " + err.Error())
	}
	return b
}

// mustMarshalPayload 载荷编码；失败属不可达。
func mustMarshalPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("conn: 载荷编码失败: " + err.Error())
	}
	return b
}

// isWSS 报告 URL 是否为 wss（TLS 校验路径判定）。
func isWSS(raw string) bool {
	return strings.HasPrefix(raw, "wss:")
}

// parsePeerCert 取对端证书链叶子证书（指纹校验对象）。
func parsePeerCert(rawCerts [][]byte) (*x509.Certificate, error) {
	if len(rawCerts) == 0 {
		return nil, errors.New("hub 未出示证书（拒绝连接）")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, fmt.Errorf("解析 hub 证书失败: %w", err)
	}
	return cert, nil
}

// normalizeURL ws/wss 透传，http/https 改写；其余拒绝。
func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("conn: hub_url 非法 %q: %w", raw, err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("conn: hub_url scheme 非法 %q（须 ws/wss）", raw)
	}
	return u.String(), nil
}

// isControlType node→hub 控制帧（走 64KiB 独立小缓冲，SPEC 4.6）：心跳、注册确认
// 类小型帧；task_result/task_submit 等可能到大帧走 4MiB 数据队列。register 不在
// 表内（N1：注册走 pushHandshake 专用路径，携完整能力表可至 1MiB）。
func isControlType(typ string) bool {
	switch typ {
	case protocol.MsgPing, protocol.MsgPong,
		protocol.MsgTaskAck, protocol.MsgTaskStarted:
		return true
	}
	return false
}

// Preview 返回当前退避时长（日志用，不推进状态）。
func (b *Backoff) Preview() time.Duration { return b.cur }
