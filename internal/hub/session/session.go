package session

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// readLimitSlack websocket 层读上限在 1MiB 协议上限之外留的余量：略超限的帧仍
// 由本会话读完并按协议回 err(frame_too_large)（SPEC 4.6），极端超限帧才由 ws 层
// 直接断开。不加余量时 ws 层先于协议层拒绝，无法给出协议规定的应答帧。
const readLimitSlack = 4096

// staleEpochLimit 入站帧 epoch 连续不匹配的容忍次数，超过即断开（SPEC 4.4：连续 5 次）。
const staleEpochLimit = 5

// session 一条 node 连接的会话（SPEC 4.2/4.4/4.6）：注册握手、应用层心跳、
// epoch 校验、读循环 + 专职写 goroutine（websocket 单连接写必须串行化）。
//
// goroutine 布局与退出：读循环（run 本体）退出即 finish → 关队列 → 写 goroutine
// 排空后 Close 连接（反过来解除 Read 阻塞）→ 各 goroutine 退出 → finish 等待
// 全部结束后向 Manager 注销。生命周期信号统一是队列的 done 通道与会话 ctx
// （非优雅关闭时取消在途读写，L4），不另设关闭通道。
type session struct {
	mgr    *Manager
	nodeID string // 注册成功后有效
	epoch  int64  // 注册事务分配的会话代次（注册成功后有效）

	conn   *websocket.Conn
	q      *outboundQ
	ctx    context.Context // 会话生命周期：非优雅关闭即取消（在途 Read/Write 随之中断）
	cancel context.CancelFunc

	lastPong atomic.Int64 // 上次收到 pong 的 UnixNano（心跳计时基于上次 pong 收到时刻，SPEC 4.6）
	staleEp  atomic.Int64 // 入站帧 epoch 连续不匹配计数（有效帧清零）

	frameCh chan *protocol.Envelope // 入站帧交处理 goroutine（解耦心跳读取与出站背压）
	wg      sync.WaitGroup          // writeLoop/processLoop/heartbeat
	once    sync.Once               // 队列只关一次
}

func newSession(mgr *Manager, conn *websocket.Conn) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		mgr:     mgr,
		conn:    conn,
		q:       newOutboundQ(),
		ctx:     ctx,
		cancel:  cancel,
		frameCh: make(chan *protocol.Envelope, frameQueueSize),
	}
	// L4：队列终态（控制帧溢出/数据停滞）即取消会话 ctx——写 goroutine 的在途
	// Write 立即快速失败，不等 60s 写超时才断开
	s.q.onDead = cancel
	return s
}

// run 会话主生命周期：注册握手 → 心跳/读/处理三 goroutine → 退出清理。
// 由 Manager 在 Accept 后的独立 goroutine 调用；返回即向 Manager 注销。
func (s *session) run() {
	defer s.finish()
	s.wg.Add(1)
	go s.writeLoop()
	if !s.register() {
		return
	}
	s.wg.Add(2)
	go s.heartbeat()
	go s.processLoop()
	s.readLoop()
}

// finish 会话退出清理：关队列（写 goroutine 随之排空并 Close 连接，解除读阻塞），
// 等待本会话全部 goroutine 退出后向 Manager 注销。
func (s *session) finish() {
	s.beginClose(false)
	s.wg.Wait()
	s.cancel() // 兜底释放会话 ctx（此时全部 goroutine 已退出，握手不再在途）
	s.mgr.removeSession(s)
}

// beginClose 关闭发送队列（幂等）；graceful=true 时写 goroutine 先排空控制帧
// （关闭握手）再断开。非优雅关闭同时取消会话 ctx：在途 Write/Read 立即中断
// （L4：关闭不得被 60s 写超时拖住）——但队列已按优雅模式关闭时不得取消（会中止
// 在途握手，close 帧无法送达对端）；强断走 forceClose 无条件取消。连接的实际
// Close 一律由写 goroutine 执行（唯一写方）。
func (s *session) beginClose(graceful bool) {
	if !graceful && !s.q.graceClosed() {
		s.cancel()
	}
	s.once.Do(func() { s.q.close(graceful) })
}

// forceClose 强断（L3：优雅关闭超预算后的降级路径）：已置优雅关闭的队列降级为
// 非优雅（未进入排空的写 goroutine 改走 CloseNow），并取消在途写使排空立即中止。
func (s *session) forceClose() {
	s.cancel()
	s.q.close(false) // 已关闭时仅降级 grace（见 queue.close）
}

// register 注册握手（SPEC 4.2/4.4）：连接后 RegisterTimeout 内首帧必须是合法
// register；token 绑定 node_id 验证；单写事务内 epoch+1 持久化 + 能力表更新；
// 成功 → 会话表替换旧连接 → register_ack（携新 epoch）→ 上线补推；事务失败则
// 不发 ack、断开。
//
// L1：提交持久化 → 会话表发布 → ack → 上线回调 → 旧连接清理，整段按 node 串行
// （mgr.nodeLock）。同 node 并发注册时「先提交后发布」的逆序覆盖从此不可能发生：
// 后到注册在前一注册发布/ack 完成前无法进入提交。
func (s *session) register() bool {
	m := s.mgr
	env, err := s.readFrame(s.ctx, m.regTimeout())
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			s.replyErr("", protocol.CodeFrameTooLarge, "register 帧超过 1MiB 上限")
			s.beginClose(true)
		}
		log.Printf("[hub] register 首帧接收失败: %v", err)
		return false
	}
	if err := checkVersion(env); err != nil {
		s.replyErr("", protocol.CodeUnsupportedVersion, err.Error())
		s.beginClose(true)
		log.Printf("[hub] 拒绝不支持的协议版本 v=%d（SPEC 4.1：回 err 帧并断开）", env.Version)
		return false
	}
	if env.Type != protocol.MsgRegister {
		// SPEC 4.2：连接后首帧必须是 register，否则断开
		log.Printf("[hub] 首帧非 register（type=%s），断开", env.Type)
		return false
	}
	var p protocol.RegisterPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.NodeID == "" {
		log.Printf("[hub] register 载荷解析失败: %v", err)
		return false
	}

	// token 绑定 node_id 验证（每次连接全量鉴权，SPEC 二）；常数时间比较
	token, ok := m.cfg.Tokens[p.NodeID]
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(p.RegisterToken)) != 1 {
		ack := encodeFrame(registerAckEnvelope(&protocol.RegisterAckPayload{
			OK: false, Reason: protocol.CodeAuthFailed,
		}, 0))
		_ = s.q.pushCtrl(ack)
		s.beginClose(true) // ack 排空后走关闭握手（SPEC 4.2：拒绝则断开）
		log.Printf("[hub] register 鉴权失败 node=%s", p.NodeID)
		return false
	}

	// L1：同 node 注册全程串行化（持久化→发布→ack→上线→旧连接清理）
	nodeMu := m.nodeLock(p.NodeID)
	nodeMu.Lock()
	defer nodeMu.Unlock()

	// L3：等锁复核——node 锁可能被前一注册的完整提交持有，等锁期间本连接可能
	// 已断（会话 ctx 取消）或 Manager 已关闭；两者任一发生都不得再执行提交事务
	//（epoch 只增不减，白耗代次且关停后仍写盘）。
	if s.ctx.Err() != nil {
		log.Printf("[hub] 注册放弃（等锁期间连接已断）node=%s", p.NodeID)
		return false
	}
	if m.isClosed() {
		log.Printf("[hub] 注册放弃（等锁期间 Manager 已关闭）node=%s", p.NodeID)
		return false
	}

	// 单写事务：epoch+1 持久化 + 能力表更新（SPEC 4.4；失败则不发 ack、断开）
	var newEpoch int64
	cards := append([]protocol.AgentCard(nil), p.Agents...)
	err = m.cfg.Store.Update(func(st *store.State) error {
		rec := st.Nodes[p.NodeID]
		rec.Epoch++
		rec.Agents = cards
		st.Nodes[p.NodeID] = rec
		newEpoch = rec.Epoch
		return nil
	})
	if err != nil {
		log.Printf("[hub] register 事务失败 node=%s: %v", p.NodeID, err)
		return false
	}
	if m.hookAfterRegisterPersist != nil { // 测试注入点：暂停于「已提交未发布」窗口
		m.hookAfterRegisterPersist(p.NodeID)
	}

	s.nodeID = p.NodeID
	s.epoch = newEpoch
	if !m.replaceSession(s) { // 会话表替换旧连接（旧连接随后被关闭）；Manager 已关闭则拒绝发布
		log.Printf("[hub] register 放弃发布（会话管理器已关闭）node=%s epoch=%d", p.NodeID, newEpoch)
		return false // 不发 ack；finish 注销，不在关停后留下可路由会话
	}

	// L3：发布后复核——Close 可插入发布与 ack/上线回调之间；已关闭则撤回发布
	//（会话表摘除本会话，避免关停后遗留可路由表项），不发 ack、不触发上线回调
	if m.isClosed() {
		m.mu.Lock()
		if cur := m.sessions[s.nodeID]; cur == s {
			delete(m.sessions, s.nodeID)
		}
		m.mu.Unlock()
		log.Printf("[hub] register 发布后遇关闭，撤回发布 node=%s epoch=%d", s.nodeID, newEpoch)
		return false
	}

	ack := encodeFrame(registerAckEnvelope(&protocol.RegisterAckPayload{OK: true, Epoch: newEpoch}, newEpoch))
	if err := s.q.pushCtrl(ack); err != nil {
		log.Printf("[hub] register_ack 发送失败 node=%s: %v", s.nodeID, err)
		return false
	}
	if m.cfg.SetOnline != nil {
		m.cfg.SetOnline(s.nodeID, true) // 上线即触发离线信箱补推（DoD 5；L2：与下线事件同锁有序）
	}
	log.Printf("[hub] node 注册成功 node=%s epoch=%d agents=%d", s.nodeID, newEpoch, len(p.Agents))
	return true
}

// checkVersion 信封协议版本校验（C1/SPEC 4.1：所有阶段的合法信封都必须携当前
// 版本，不满足回 err(unsupported_version) 并断开）。
func checkVersion(env *protocol.Envelope) error {
	if env.Version != protocol.ProtocolVersion {
		return fmt.Errorf("不支持的协议版本 v=%d（当前 v=%d）", env.Version, protocol.ProtocolVersion)
	}
	return nil
}

// readLoop 注册后的入站读循环（SPEC 4.4/4.6）：版本校验（不支持→回 err 断开）→
// epoch 校验（不匹配忽略 + 日志，连续 5 次断开）→ ping/pong 本地处理 →
// card_update 会话层处理 → 其余帧交处理 goroutine 路由到 hub core（From 以连接
// 鉴权身份覆盖，不信帧内值）。
func (s *session) readLoop() {
	for {
		env, err := s.readFrame(s.ctx, protocol.ReadWriteTimeout)
		if err != nil {
			if errors.Is(err, protocol.ErrFrameTooLarge) {
				// 帧已消费、连接健康：按协议回 err 帧后继续（SPEC 4.6）
				s.replyErr("", protocol.CodeFrameTooLarge, "帧超过 1MiB 上限")
				continue
			}
			return // 读失败/超时：连接已死或对端失联，退出即清理
		}
		// C1：注册之后的每个合法信封仍须校验协议版本（SPEC 4.1）
		if err := checkVersion(env); err != nil {
			s.replyErr("", protocol.CodeUnsupportedVersion, err.Error())
			s.beginClose(true)
			log.Printf("[hub] 服务阶段拒绝协议版本 v=%d type=%s（回 err 后断开）", env.Version, env.Type)
			return
		}
		// register 豁免仅限首帧；此后携旧 epoch 的 register 同样按残余帧处理
		if env.SessionEpoch != s.mgr.currentEpoch(s.nodeID) {
			n := s.staleEp.Add(1)
			want := s.mgr.currentEpoch(s.nodeID)
			log.Printf("[hub] 入站帧 epoch 不匹配（忽略）node=%s got=%d want=%d 连续=%d type=%s",
				s.nodeID, env.SessionEpoch, want, n, env.Type)
			if n >= staleEpochLimit {
				log.Printf("[hub] epoch 连续不匹配达上限，断开 node=%s", s.nodeID)
				s.beginClose(false)
				return
			}
			continue
		}
		s.staleEp.Store(0)

		switch env.Type {
		case protocol.MsgPing:
			s.replyPong(env)
		case protocol.MsgPong:
			s.lastPong.Store(time.Now().UnixNano())
		case protocol.MsgCardUpdate:
			s.handleCardUpdate(env)
		default:
			env.From = s.nodeID // 身份由传输层强制覆盖（SPEC 4.1）
			select {
			case s.frameCh <- env:
			case <-s.q.done:
				return
			}
		}
	}
}

// processLoop 入站帧处理 goroutine：按到达顺序同步调用 Manager 订阅的回调
// （core.HandleFrame）。与读循环分离，使心跳读取不被出站背压阻塞。
// 换代失效（L1）：出队后复核本会话仍是该 node 的当前路由会话，被替换会话残留在
// frameCh 中的帧不再进入 core（事务级复核另见 handleCardUpdate）。
func (s *session) processLoop() {
	defer s.wg.Done()
	for {
		select {
		case env := <-s.frameCh:
			if s.mgr.replaced(s) {
				continue // 会话已被新连接替换：残余帧丢弃
			}
			if fn := s.mgr.inboundHandler(); fn != nil {
				fn(env)
			}
		case <-s.q.done:
			return
		}
	}
}

// heartbeat 应用层心跳（SPEC 4.6）：每 PingInterval 发一次 ping（控制帧队列）；
// 上次 pong 距今超 PongTimeout（计时基于上次 pong 收到时刻）→ 断开。
func (s *session) heartbeat() {
	defer s.wg.Done()
	s.lastPong.Store(time.Now().UnixNano())
	tk := time.NewTicker(s.mgr.pingInterval())
	defer tk.Stop()
	for {
		select {
		case <-s.q.done:
			return
		case <-tk.C:
			if err := s.q.pushCtrl(encodeFrame(pingEnvelope(s))); err != nil {
				log.Printf("[hub] 心跳 ping 发送失败 node=%s: %v", s.nodeID, err)
				return
			}
			age := time.Since(time.Unix(0, s.lastPong.Load()))
			if age >= s.mgr.pongTimeout() {
				log.Printf("[hub] 心跳超时（%s 未收到 pong），断开 node=%s", age.Round(time.Millisecond), s.nodeID)
				s.beginClose(false)
				return
			}
		}
	}
}

// writeLoop 专职写 goroutine（唯一写方，串行化 websocket 写，SPEC 第九节）。
// L4：每次取帧前先处理关闭态——非优雅关闭立即 CloseNow（丢弃队列残余、取消在途
// 写），优雅关闭只排空控制帧再握手；不在已死队列上继续排空数据帧。
// 每帧写超时 ReadWriteTimeout（会话 ctx 取消即中止在途写）；退出路径一律由本
// goroutine 执行 conn.Close——并发 Read 由此解除阻塞。
func (s *session) writeLoop() {
	defer s.wg.Done()
	for {
		if closed, grace := s.q.state(); closed {
			if grace {
				// 优雅关闭：先排空控制帧（如 register_ack / auth 拒绝应答）再握手。
				// 入站残余帧静默丢弃（协议错误断开前对端连发的帧）：不读而关会触发
				// RST、冲掉对端已接收的应答帧。不用库的 CloseRead——它读到数据帧会
				// 自动回 close 1008 PolicyViolation，覆盖优雅握手（CI macos 实测：
				// client 连发坏帧挤入缓冲，client 收到 1008 而非 1000，2026-10-24
				// 诊断实锤）。ping/pong 由库在读路径内自动应答；close 握手完成后
				// Read 返回错误、循环退出；s.ctx 取消兜底。
				s.drainCtrlGraceful()
				go s.discardInbound()
				s.closeHandshake()
			} else {
				// 非优雅（心跳超时/停滞/被替换/读失败/强断）：立即断，解除并发 Read 阻塞
				_ = s.conn.CloseNow()
			}
			return
		}
		if b := s.q.pop(); b != nil {
			wctx, cancel := context.WithTimeout(s.ctx, protocol.ReadWriteTimeout)
			err := s.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				log.Printf("[hub] 帧写出失败 node=%s: %v", s.nodeID, err)
				s.q.close(false)
			}
			continue
		}
		select {
		case <-s.q.dataReady:
		case <-s.q.done:
		}
	}
}

// drainCtrlGraceful 优雅排空控制帧；任一帧写出失败（含强断取消）即中止排空，
// 后续统一走 CloseNow。
func (s *session) drainCtrlGraceful() {
	for _, b := range s.q.drainCtrl() {
		wctx, cancel := context.WithTimeout(s.ctx, protocol.ReadWriteTimeout)
		err := s.conn.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			return
		}
	}
}

// closeHandshake 关闭握手，受会话关闭信号约束（L3）：库内握手固定 5s 超时、
// 不受 ctx 控制，对端不回帧时会拖住关闭预算——强断（forceClose 取消会话 ctx）
// 时立即 CloseNow 关闭底层连接，令在途握手中止，保证预算可兑现。
func (s *session) closeHandshake() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.conn.Close(websocket.StatusNormalClosure, "hub close")
	}()
	select {
	case <-done:
	case <-s.ctx.Done():
		_ = s.conn.CloseNow()
		<-done
	}
}

// discardInbound 静默丢弃入站残余帧直至连接关闭（优雅关闭专用，替代库的
// CloseRead：后者对数据帧回 1008，会把优雅关闭变成 PolicyViolation 踢出）。
// 唯一 reader（readLoop 已退出）；对端 close 回帧也由此循环消费。
func (s *session) discardInbound() {
	// 对端 close 回帧被本循环消费后由这里关闭底层连接（原 CloseRead 的库内
	// 行为：握手完成即关底层；不补则 client 侧读挂起、连接泄漏）。
	defer s.conn.CloseNow()
	for {
		if _, _, err := s.conn.Read(s.ctx); err != nil {
			return
		}
	}
}

// readFrame 读一条文本帧并解码。超限帧（>1MiB）消费后返回 ErrFrameTooLarge
// 供调用方按协议回 err 帧；其余读错误原样返回。读受 parent 会话 ctx 约束
// （非优雅关闭立即解除 Read 阻塞，L4）。
func (s *session) readFrame(parent context.Context, timeout time.Duration) (*protocol.Envelope, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	typ, data, err := s.conn.Read(ctx)
	if err != nil {
		// ws 层直接拒收的极端超限帧（超出读上限余量）：转译为协议层错误语义
		if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
			return nil, protocol.ErrFrameTooLarge
		}
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errors.New("session: 非文本帧")
	}
	return protocol.Decode(data)
}

// handleCardUpdate 能力表全量更新（SPEC 4.2：会话层职责，不经 core 路由）。
// L1：事务内重检本会话代次——排队等写的 card_update 在新会话注册（epoch 推进）
// 之后落库会被拒绝，旧连接残余不再覆盖新会话的能力表（先读后写全部在同一
// 单写事务内，无锁外 TOCTOU）。
func (s *session) handleCardUpdate(env *protocol.Envelope) {
	if s.mgr.hookBeforeCardUpdate != nil { // 测试注入点：卡在事务提交前
		s.mgr.hookBeforeCardUpdate(s.nodeID)
	}
	var p protocol.CardUpdatePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		log.Printf("[hub] card_update 载荷解析失败 node=%s: %v", s.nodeID, err)
		return
	}
	cards := append([]protocol.AgentCard(nil), p.Agents...)
	err := s.mgr.cfg.Store.Update(func(st *store.State) error {
		rec := st.Nodes[s.nodeID]
		if rec.Epoch != s.epoch {
			return fmt.Errorf("session: card_update 代次过期（会话 epoch=%d，当前=%d）", s.epoch, rec.Epoch)
		}
		rec.Agents = cards
		st.Nodes[s.nodeID] = rec
		return nil
	})
	if err != nil {
		log.Printf("[hub] card_update 事务被拒绝 node=%s epoch=%d: %v", s.nodeID, s.epoch, err)
		return
	}
	log.Printf("[hub] 能力表更新 node=%s agents=%d", s.nodeID, len(cards))
}

// replyPong 回 pong（控制帧队列，携本会话 epoch 供对端校验）。
func (s *session) replyPong(req *protocol.Envelope) {
	env := &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgPong,
		MessageID:    protocol.NewMessageID(),
		From:         "hub",
		To:           s.nodeID,
		ReplyTo:      req.MessageID,
		SessionEpoch: s.epoch,
	}
	_ = s.q.pushCtrl(encodeFrame(env))
}

// replyErr 回 err 帧（控制帧队列）。C2：已注册会话（s.epoch>0）的错误帧必须携
// 当前 epoch，真实 node 按 epoch 校验消费该帧；未注册时 epoch=0（对端尚无代次）。
func (s *session) replyErr(replyTo, code, msg string) {
	env := &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgErr,
		MessageID:    protocol.NewMessageID(),
		From:         "hub",
		To:           s.nodeID,
		ReplyTo:      replyTo,
		SessionEpoch: s.epoch,
		Err:          &protocol.ErrInfo{Code: code, Msg: msg},
	}
	_ = s.q.pushCtrl(encodeFrame(env))
}

// registerAckEnvelope 构造 register_ack 帧（epoch=0 表示拒绝应答，不携代次）。
func registerAckEnvelope(p *protocol.RegisterAckPayload, epoch int64) *protocol.Envelope {
	payload, err := json.Marshal(p)
	if err != nil {
		payload = []byte("null") // 不可达：RegisterAckPayload 全字段可序列化
	}
	return &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgRegisterAck,
		MessageID:    protocol.NewMessageID(),
		From:         "hub",
		SessionEpoch: epoch,
		Payload:      payload,
	}
}

// pingEnvelope 构造心跳 ping 帧（空载荷，SPEC 4.2）。
func pingEnvelope(s *session) *protocol.Envelope {
	return &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgPing,
		MessageID:    protocol.NewMessageID(),
		From:         "hub",
		To:           s.nodeID,
		SessionEpoch: s.epoch,
	}
}

// encodeFrame 信封编码；Envelope 全字段可序列化，失败属不可达，防御性 panic。
func encodeFrame(env *protocol.Envelope) []byte {
	b, err := protocol.Encode(env)
	if err != nil {
		panic("session: 信封编码失败: " + err.Error())
	}
	return b
}
