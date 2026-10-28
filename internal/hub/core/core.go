// Package core 实现 hub 任务服务（SPEC 4.3/4.5/4.6）：提交校验链、投递与
// offer 超时回退、结果仲裁、expire 清扫、投递窗口、result_event 生成。
//
// 会话层（register/epoch/心跳，SPEC 4.4）属后续 WSS Loop：本包只依赖两个
// 会话侧原语——节点能力表注册（RegisterNode，DoD 7 随快照持久化）与在线状态
// （SetOnline）。传输经 transport.Transport 注入，内存 membus 下全链同步可测。
//
// 锁纪律：c.online/c.szMu 只保护各自的 map/计数，临界区内禁止调用 transport.Send
// 与 store.Update（membus 同步分发下回调会重入本包）。
package core

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 提交校验链错误（SPEC 六：顺序见 Submit）。errors.Is 判定。
var (
	// ErrInvalid 请求字段缺失/非法（HTTP 400）。
	ErrInvalid = errors.New("core: 无效请求")
	// ErrInvalidDeps 依赖声明不合法（P4.3/SPEC 4.12：未知 idem/自引用/重复
	// 依赖/超深度/超级联/依赖环；HTTP 400 invalid_dependency）。
	ErrInvalidDeps = errors.New("core: 依赖声明不合法")
	// ErrAgentNotExposed 目标 node 未注册、agent 不存在或 exposed=false（HTTP 404）。
	ErrAgentNotExposed = errors.New("core: 目标 agent 未接入或已下线")
	// ErrStorageFull hub 任务存储全局字节预算满（SPEC 4.6 → 503 storage_full）。
	ErrStorageFull = errors.New("core: hub 存储预算已满")
)

// sweepInterval 后台清扫周期。调度节拍（非协议常量）：正确性只依赖
// protocol.OfferAckTimeout / TaskRetention / 终态保留时长（cfg.TerminalRetention，
// 默认 protocol.TerminalRetention），节拍只影响时延。
const sweepInterval = time.Second

// SubmitAck 提交确认（HTTP 返回值；node 会话提交方另收 submit_ack 帧）。
type SubmitAck struct {
	// TaskID 持久化后的任务 ID（幂等命中时为原任务 ID）。
	TaskID string
	// TraceID 链路追踪 ID。
	TraceID string
	// Dup 幂等命中。
	Dup bool
	// Work 绑定派发结果（P5 W2/RFC-work-resume；未绑定为 nil）。HTTP 返回体
	// 展开为 work 对象；node 会话 submit_ack 帧不携带（SubmitAckPayload 不变，
	// 绑定信息经工作单查询面读取）。
	Work *WorkBindInfo
}

// Config Core 构造参数。
type Config struct {
	// Store 持久化存储（先写后见事务）。
	Store *store.Store
	// Transport hub 端传输（membus 端点；WSS 会话层实现）。
	Transport transport.Transport
	// Now 时钟源，nil = protocol.NowUTC（测试注入假时钟）。
	Now func() time.Time
	// TerminalRetention 终态保留时长（Sweep 第 5 阶段终态清理窗口，
	// maturity-retention-config）：nil = protocol.TerminalRetention 默认（既有
	// 构造方零改动行为不变）；0 = 永不自动清理（hub.yaml terminal_retention: 0s）；
	// 正值 = 配置时长。保留期同时是幂等去重窗口（SPEC 4.5 idem 与任务同生命周期）。
	TerminalRetention *time.Duration
	// RelayFetchTimeout node 发起的产物取回中继应答预算（P4④跨机取回；0 =
	// relayFetchTimeout 30s。测试注入短预算）。
	RelayFetchTimeout time.Duration
	// SweepInterval 后台清扫周期；0 = 不启动后台清扫（Sweep 手动驱动，测试用）。
	SweepInterval time.Duration
	// OnTaskTerminal 任务终态旁路钩子（可选，nil 安全）：任务首次落定终态时
	// 同步调用（result 首次接受 / 清扫置 failed 均触发；重复结果与冲突不触发）。
	// 群聊闭环「结果回流群」的接线点（GROUP-RFC）：实现方不得长时间阻塞，错误
	// 自行消化，不影响任务状态机。下游组件的附加钩子不占用本字段——经
	// Core.AddOnTaskTerminal 注册（如群 jev 主持人轮转推进，DESIGN-butler 3.6）。
	OnTaskTerminal func(protocol.Task)
	// Notifier 任务终态 IM 通知器（P4③，可选，nil=禁用）：与 OnTaskTerminal 同一
	// 触发点（首次落终态；重复结果、冲突、cancelled 合并不触发——去重即挂在
	// 转换点，Sweep/重放不重复通知）。实现方自行异步化并消化错误，不得阻塞
	// 状态机（PRODUCT-DIRECTION v2 P4③：通知失败绝不影响任务终态与主流程）。
	Notifier TerminalNotifier
}

// Core hub 任务服务。
type Core struct {
	st         *store.Store
	tr         transport.Transport
	now        func() time.Time
	onTerminal func(protocol.Task)
	notifier   TerminalNotifier

	// onTerminalExtra AddOnTaskTerminal 注册的附加终态钩子（muHooks 保护：
	// 注册发生在装配方装配期，fireTerminal 触发来自各工作 goroutine，按并发
	// 纪律加锁——注册与触发无固定先后保证）。
	muHooks         sync.Mutex
	onTerminalExtra []func(protocol.Task)

	// terminalRetention 终态保留时长（New 时从 cfg.TerminalRetention 解析落定：
	// nil → protocol.TerminalRetention；0 = 永不清理）。TerminalRetention() 读侧。
	terminalRetention time.Duration

	// 在线会话表（会话层经 SetOnline 维护；V0 内存 transport 下注册即在线）。
	mu     sync.Mutex
	online map[string]bool

	// 任务体字节缓存（存储全局预算的计量，SPEC 4.6 storage_full）：
	// task_id → Task JSON 编码字节。真相源是快照本体，缓存仅免每次提交全量
	// 重编列；任务集仅在 Submit（增）/清扫清理（删）/终态落盘（变）处改动，
	// 各事务发布后经 noteTask/noteDelete 记账，New 时全量重建一次。
	szMu    sync.Mutex
	sizes   map[string]int64
	totalSz int64

	// 在途产物取回表（FetchArtifact 中继，Loop 4）：请求帧 mid → waiter。
	// artClosed = Close 已收束本域（muArt 保护）：停 relay timer、清表、不再
	// 受理新中继——防关闭后 timer 残留继续调 transport（2026-10-24 审查建议修6）。
	// artWG 收敛已启动外发（2026-10-24 复审 R2）：凡锁内 artClosed=false 复核通过
	// 的外发（在途初始 Send/超时回调/中继转发）先 Add 再锁外 Send，Close 置位后
	// Wait——Add 与 Close 置位同锁序，置位后新增复核必见 closed，Wait 期间零新增。
	muArt      sync.Mutex
	fetches    map[string]*fetchWaiter
	artClosed  bool
	artWG      sync.WaitGroup
	relayFetch time.Duration // node 发起的取回中继应答预算（0 = 默认 30s）

	sweepStop chan struct{}
	sweepDone chan struct{}
	sweepOn   bool
	closeOnce sync.Once
}

// New 构造任务服务并重建字节缓存。Transport 的入站回调由装配方接 HandleFrame。
func New(cfg Config) (*Core, error) {
	if cfg.Store == nil || cfg.Transport == nil {
		return nil, ErrInvalid
	}
	now := cfg.Now
	if now == nil {
		now = protocol.NowUTC
	}
	retention := protocol.TerminalRetention
	if cfg.TerminalRetention != nil {
		retention = *cfg.TerminalRetention
	}
	c := &Core{
		st:                cfg.Store,
		tr:                cfg.Transport,
		now:               now,
		onTerminal:        cfg.OnTaskTerminal,
		notifier:          cfg.Notifier,
		terminalRetention: retention,
		online:            make(map[string]bool),
		sizes:             make(map[string]int64),
		fetches:           make(map[string]*fetchWaiter),
		relayFetch:        cfg.RelayFetchTimeout,
		sweepStop:         make(chan struct{}),
		sweepDone:         make(chan struct{}),
	}
	for _, t := range cfg.Store.View().Tasks {
		tt := t
		c.noteTask(&tt)
	}
	c.rebuildWorkdirLocks() // workdir 互斥表快照恢复 + 按任务集自愈（P1.5/SPEC 4.10）
	c.recoverBatches()      // creating 批次创建中断收尾（见 control.go）
	c.recoverWorks()        // 工作单终态回写窗口收尾（P5 W2，见 work_dispatch.go）
	if cfg.SweepInterval > 0 {
		c.sweepOn = true
		go c.sweepLoop(cfg.SweepInterval)
	}
	return c, nil
}

// Close 停止后台清扫（若有），并收束在途产物取回域（承诺范围=已受理的 node
// 中继生命周期，2026-10-25 三审 W4 收窄表述）：停存活 relay timer、清在途表、
// 置 artClosed（此后 timer 回调与新中继请求不再受理/不再外发），随后等待已受
// 理中继的已启动外发单元（在途初始 Send/超时回调/中继转发，复审 R2 统一收敛
// 点）完成。不在承诺内的两处残余：① 无状态拒绝回帧（invalid/forbidden/
// artifact_not_found/关闭后 node_offline）即发即弃、不占收敛组——Close 后仍可
// 能发出，至多得 transport 已关的 ErrClosed（调用方忽略）；② hub 自身取回
//（HTTP 数据面）的无 timer waiter 只清表、未纳入收敛组——其生命周期归调用方
// 自身超时预算收敛。幂等。Store/Transport 的关闭归装配方。
func (c *Core) Close() error {
	c.closeOnce.Do(func() {
		if c.sweepOn {
			close(c.sweepStop)
			<-c.sweepDone
		}
		c.muArt.Lock()
		c.artClosed = true
		for mid, w := range c.fetches {
			if w.timer != nil {
				w.timer.Stop()
			}
			delete(c.fetches, mid)
		}
		c.muArt.Unlock()
		// 置位后外发复核必见 closed → Wait 期间零新增 Add（Add 与置位同锁序，
		// 契约见 artWG 字段注释）；此处等在途外发完成后再返回。
		c.artWG.Wait()
	})
	return nil
}

// sweepLoop 周期清扫直至 Close。
func (c *Core) sweepLoop(interval time.Duration) {
	defer close(c.sweepDone)
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-c.sweepStop:
			return
		case <-tk.C:
			c.Sweep()
		}
	}
}

// TerminalRetention 终态保留时长生效值（New 时落定；0 = 永不自动清理）。管理面
// 经此向登录响应/账户视图下发 terminal_retention_sec（前端保留期文案数据源，
// 与 server_version 同面下发）；hub.yaml 是唯一真相，Core 行为与暴露值同源。
func (c *Core) TerminalRetention() time.Duration { return c.terminalRetention }

// RegisterNode 登记节点能力表（会话层 register 事务的等价物；V0 由装配方直接
// 调用）。随快照持久化，hub 重启后能力表恢复（DoD 7）。epoch 计数归会话层，
// 本方法不动 NodeRecord.Epoch。
func (c *Core) RegisterNode(nodeID string, agents []protocol.AgentCard) error {
	cp := append([]protocol.AgentCard(nil), agents...)
	return c.st.Update(func(st *store.State) error {
		rec := st.Nodes[nodeID]
		rec.Agents = cp
		st.Nodes[nodeID] = rec
		return nil
	})
}

// SetOnline 更新节点在线状态；上线即触发补推（离线期间入箱的任务，DoD 5）与
// 取消补发（离线期间取消、task_cancel 未达的任务，批 B 修复3——注册成功处的
// 至少一次取消语义）。
func (c *Core) SetOnline(nodeID string, online bool) {
	c.mu.Lock()
	c.online[nodeID] = online
	c.mu.Unlock()
	if online {
		c.resendPendingCancels(nodeID)
		c.dispatch(nodeID)
	}
}

// isOnline 查询节点在线状态。
func (c *Core) isOnline(nodeID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.online[nodeID]
}

// GetTask 查询任务（task_status/HTTP GET 的数据源；副本返回）。
func (c *Core) GetTask(taskID string) (protocol.Task, bool) {
	t, ok := c.st.View().Tasks[taskID]
	return t.Clone(), ok
}

// HandleFrame 入站帧路由（transport.Subscribe 的回调）。身份信任传输层注入的
// env.From（SPEC 4.1：hub 不信帧内值）；register/epoch 属会话层，不经此路由。
// L1：入口代次复核——session 读循环的 epoch 校验与处理器调用之间存在换代窗口
//（同 node 新连接完成注册发布后，旧会话已入队的残余帧仍可能到达本入口），此处
// 以 store 为真相源再复核一次，旧代次帧一律丢弃、不进入任何 core 事务。补推/
// 重发幂等，丢弃安全（node 按未确认语义重发）；membus 内存链路双方代次恒为 0，
// 天然通过。
func (c *Core) HandleFrame(env *protocol.Envelope) {
	if env == nil {
		return
	}
	if env.Version != protocol.ProtocolVersion {
		log.Printf("[hub] 忽略不支持的协议版本 v=%d from=%s", env.Version, env.From)
		return
	}
	if cur := c.st.View().Nodes[env.From].Epoch; cur != env.SessionEpoch {
		log.Printf("[hub] 丢弃代次不匹配帧（疑似被替换旧会话残余）type=%s from=%s got=%d want=%d",
			env.Type, env.From, env.SessionEpoch, cur)
		return
	}
	switch env.Type {
	case protocol.MsgTaskSubmit:
		c.handleTaskSubmit(env)
	case protocol.MsgTaskQuery:
		c.handleTaskQuery(env)
	case protocol.MsgDepResolve:
		c.handleDepResolve(env)
	case protocol.MsgArtifactFetch:
		// node 发起的产物取回中继（P4④跨机取回/SPEC 4.12）：鉴权后按原 mid
		// 中继执行 node，应答转发回请求 node。
		c.handleNodeFetch(env)
	case protocol.MsgArtifactData:
		c.handleFetchReply(env)
	case protocol.MsgErr:
		// node 应答错误帧（如 artifact_mismatch/not_found）：ReplyTo 命中在途
		// 取回则投递，否则记录（result_conflict 等面向 node 的 err 不经此）
		c.handleFetchReply(env)
	case protocol.MsgTaskAck:
		c.handleTaskAck(env)
	case protocol.MsgTaskStarted:
		c.handleTaskStarted(env)
	case protocol.MsgTaskResult:
		c.handleTaskResult(env)
	case protocol.MsgTaskCancelled:
		c.handleTaskCancelledAck(env)
	case protocol.MsgPing:
		_ = c.sendFrame(env.From, protocol.MsgPong, env.MessageID, nil, nil)
	case protocol.MsgPong, protocol.MsgCardUpdate, protocol.MsgRegister:
		// 会话层职责（后续 WSS Loop）；V0 内存链路不产生
	default:
		log.Printf("[hub] 忽略未知帧 type=%s from=%s", env.Type, env.From)
	}
}

// sendFrame 构造并发送 hub→node 帧；返回传输错误（帧构造不可失败，Payload 由
// 调用方保证可序列化）。replyTo 关联请求 mid。
func (c *Core) sendFrame(to, typ, replyTo string, payload any, errInfo *protocol.ErrInfo) error {
	return c.sendFrameFence(to, typ, replyTo, 0, payload, errInfo)
}

// sendFrameFence sendFrame 的带租约变体（P4/SPEC 4.11）：fence 供 task_offer 携
// 派发时刻该 node 的注册代次、task_cancel 携该任务当前落库租约（cancel_fence，
// P1-2）；其余帧恒 0=缺省。
func (c *Core) sendFrameFence(to, typ, replyTo string, fence int64, payload any, errInfo *protocol.ErrInfo) error {
	env := &protocol.Envelope{
		Version:    protocol.ProtocolVersion,
		Type:       typ,
		MessageID:  protocol.NewMessageID(),
		To:         to,
		ReplyTo:    replyTo,
		FenceEpoch: fence,
		Err:        errInfo,
	}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		env.Payload = b
	}
	return c.tr.Send(to, env)
}

// sizeOf 查缓存的任务体字节。
func (c *Core) sizeOf(taskID string) int64 {
	c.szMu.Lock()
	defer c.szMu.Unlock()
	return c.sizes[taskID]
}

// totalBytes 存储全局任务体字节和（含终态保留——默认 24h，terminal_retention 可配，SPEC 4.6）。
func (c *Core) totalBytes() int64 {
	c.szMu.Lock()
	defer c.szMu.Unlock()
	return c.totalSz
}

// noteTask 事务发布后记账（或更新）任务体字节。
func (c *Core) noteTask(t *protocol.Task) {
	n := mailboxTaskBytes(t)
	c.szMu.Lock()
	defer c.szMu.Unlock()
	c.totalSz += n - c.sizes[t.ID]
	c.sizes[t.ID] = n
}

// noteDelete 事务发布后核销任务字节记账。
func (c *Core) noteDelete(taskID string) {
	c.szMu.Lock()
	defer c.szMu.Unlock()
	c.totalSz -= c.sizes[taskID]
	delete(c.sizes, taskID)
}

// TerminalNotifier 任务终态 IM 通知器（P4③，可选依赖，nil=禁用）。实现方自行
// 异步化并消化错误，不得阻塞状态机（通知失败绝不影响任务终态与主流程，
// PRODUCT-DIRECTION v2 P4③）；同任务同终态本方法只会被调用一次。
type TerminalNotifier interface {
	TaskFinal(t protocol.Task)
}

// AddOnTaskTerminal 注册附加终态旁路钩子（幂等无去重，重复注册即重复触发）。
// 语义约束与 Config.OnTaskTerminal 相同：首次落终态时同步调用，实现方不得长
// 时间阻塞（预算内调用须自行异步化），错误自行消化，不影响任务状态机。
func (c *Core) AddOnTaskTerminal(hook func(protocol.Task)) {
	c.muHooks.Lock()
	defer c.muHooks.Unlock()
	c.onTerminalExtra = append(c.onTerminalExtra, hook)
}

// fireTerminal 终态旁路钩子触发（nil 安全；同步调用，见 Config.OnTaskTerminal
// 与 Config.Notifier）。
func (c *Core) fireTerminal(t *protocol.Task) {
	if c.onTerminal != nil {
		c.onTerminal(*t)
	}
	c.muHooks.Lock()
	hooks := append(make([]func(protocol.Task), 0, len(c.onTerminalExtra)), c.onTerminalExtra...)
	c.muHooks.Unlock()
	for _, h := range hooks {
		h(*t)
	}
	if c.notifier != nil {
		c.notifier.TaskFinal(*t)
	}
}

// mailboxTaskBytes Task JSON 编码字节数（计量口径与 mailbox.TaskBytes 一致）。
func mailboxTaskBytes(t *protocol.Task) int64 {
	b, err := json.Marshal(t)
	if err != nil {
		return 0
	}
	return int64(len(b))
}
