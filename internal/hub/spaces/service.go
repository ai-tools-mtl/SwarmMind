// service.go 中心空间服务（W07-02 远端会话/派发 adapter + 可靠结果/恢复映射）：
//
// 派发链路（复用既有 session Task 通道，不新增任意远程执行通道）：
//
//	POST /api/v1/spaces/{id}/dispatches
//	  → Dispatch：成员寻址（目录词形 nodeID/agentName + hub 注册表会话绑定）
//	    → core.RequireSpacesCapable 版本门（W07-01 强制门首个消费者，普通任务零牵连）
//	    → core.SpaceBind 绑定登记（node 收件门受理面；ack 才放行）
//	    → collab.RecordDispatch（意图 outbox 先于执行持久，W07-01 词形单源
//	      scope=hub:<id>）→ core.SubmitSpaceTurn（Session.Space=hub:<id>，
//	      task_offer 既有投递链全白拿）→ collab.AttachDispatchTask（pending→accepted）
//	  → node 执行（收件门按绑定放行）→ task_result 既有结果链
//	  → convergeOnTaskTerminal（任务首落终态钩子）：completed/failed→converged、
//	    cancelled→cancelled+系统暂停停队、needs_review→留待核对不收敛
//	    （结果落账不触发下一轮，N04）。
//
// 可靠性纪律（AGENTS 帧请求-应答铁律 + 任务书离线兼容节逐条对照）：
//   - 在途先登记再 send：意图（outbox）与 space_bind waiter 均先登记后发送；
//   - 迟到结果验证来源后消费：任务归属（TargetNode==env.From）由 core 结果仲裁
//     保证，收敛前再核意图锚（session+seq）与 scope 词形；
//   - hub 分配请求键：任务 ID/轮序号/绑定 mid 全部 hub 侧分配，不信远端自报；
//   - 旧 term 拒绝新派发：RecordDispatch 授权层 driver 形态（term 等值）裁决；
//     已接受轮次事实照常落账，不触发暂停空间的下一步；
//   - 接受前离线→持久排队：登记完成后 node 离线，轮任务留 hub 信箱，重连补投；
//   - 未知 needs_review 不自动重发另一节点；不把 keepalive 失联解释为任务没执行
//     （accepted 意图任务未终态=仍在途，恢复对账留待核对）。
//
// 重启对账（recover）：在途意图按账本实物分类（任务/轮行），pending 无实物取消
// 收束不自动重跑；pending 有实物（受理→投递崩溃窗）接单登记收敛到 accepted；
// accepted 按任务终态分类、非终态留在途等结果、任务已清理留待核对——与 node
// spacedispatch 重启对账语义逐条对齐（宁留待核对不自动重跑）。
package spaces

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	hubstore "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sweepInterval 派发兜底扫查节拍（var 仅为测试缩短；正确性只依赖事件驱动——
// 任务终态钩子与 Dispatch 触发，节拍只影响滞留意图重试时延。同 node
// spaceSweepInterval 先例，不做配置面）。仅扫查有在途意图的空间（HTTP 空闲、无
// 意图空间零触碰）。
var sweepInterval = 2 * time.Second

// systemSubject 中心执行侧统一主体（collab 授权面：ConvergeIntent/
// AttachDispatchTask 仅 owner/system；执行侧记账属内置服务独立可审计 system 主体
// ——与 node spacedispatch 同一主体词）。
func systemSubject() collab.Subject {
	return collab.Subject{Name: "system", Class: collab.ClassSystem}
}

// Error 派发面操作错误（REST/MCP 投影按 Code 映射；与 collab 业务错误分开——
// 路由/登记/会话面的错误不入 collab 码表）。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// 派发面错误码（REST 投影见 api spaces.go writeSpaceDispatchErr）。
const (
	CodeAgentNotExposed = "agent_not_exposed"  // 成员不在开放目录
	CodeSessionGone     = "session_not_found"  // 成员绑定会话不存在
	CodeSessionClosed   = "session_closed"     // 会话已关闭
	CodeBindingMismatch = "binding_mismatch"   // 成员与会话绑定不符
	CodeNodeOffline     = "node_offline"       // 目标 node 不在线
	CodeBindTimeout     = "bind_timeout"       // 绑定登记应答超时
	CodeBindRejected    = "bind_rejected"      // 绑定登记被 node 拒绝
	CodeTurnFailed      = "turn_submit_failed" // 轮提交确定性失败（已按取消收束）
)

// Options 服务装配项。
type Options struct {
	// Hooks 策略接口（collab.StrategyHooks；hub 侧装配 strategy.Engine——纯进程内
	// 判定器，中心无 agent/管家也可运行）。nil = 完成类动作一律拒绝（W04 只开放
	// 放弃定版，同 node 未装配形态）。
	Hooks collab.StrategyHooks
	// Log 运维日志；nil = 静默。
	Log collab.Logger
	// LeaseTTL 接管/续约默认租约时长透传 collab（0 = DefaultLeaseTTL；测试注入
	// 短租约做租约过期准入反例）。负值经 collab.Open 拒绝。
	LeaseTTL time.Duration
}

// Service 中心空间服务：collab.Store 同一内核（零第二套状态机）+ 远端派发
// adapter。REST 面经内嵌 Store 直调模型层（create/control/messages/sugar 与
// node 面同形）；dispatch 经 Dispatch（本文件）走远端链。
type Service struct {
	*collab.Store
	core *core.Core

	// muDispatch 派发受理互斥段：轮号派生（会话视图读）→ RecordDispatch →
	// SubmitSpaceTurn 串行化——并发派发同会话不会派生相同预留 seq（提交面
	// SubmitTurn 事务内 (session,seq) 唯一性为第二道防线，撞号确定性冲突取消）。
	muDispatch sync.Mutex

	snap *snapshotDB
	stop chan struct{}
	done chan struct{}

	// 一次性关停（codex 建议G）：closeOnce 保证关停流程单次执行（并发 Close 不
	// 重复关通道/不跳过排空），closeDone 完成信号让所有并发调用方等到收束完成
	// 才返回（语义=「发起一次、全体等完成」）。
	closeOnce sync.Once
	closeDone chan struct{}
	snapErr   error
	storeErr  error

	// 解除登记重试面（codex 必修D③）：成员解绑/会话归属释放的 space_unbind
	// 即时尝试失败（node 离线/超时）入队（携发起时的绑定代次），兜底扫查重试
	// 直至成功。重启丢失为已登记边界：node 残留绑定 fail-closed（普通轮拒收、
	// 空间轮仍按代次核对），后续任何 space_bind 幂等覆盖自愈。
	muUnbind       sync.Mutex
	pendingUnbinds map[string]hubUnbind

	// 绑定代次（codex 复审轮 2 核心机制 1——代次身份协议）：genSeq 每 session
	// 单调分配计数；bindGens 记录本进程已成功登记的代次（进程内事实；重启后经
	// node ack current_gen 惰性重建——重启恢复从登记事实重建）。登记/解除全携
	// 代次，node 按代次条件生效：结构性消除 ABA/同空间重绑删新绑定/旧重试删新
	// 登记/并发补偿删他人绑定——比代次不比 scope。
	muBindGen sync.Mutex
	genSeq    map[string]uint64
	bindGens  map[string]uint64

	// 每空间调度互斥（codex 复审必修B-残）：贯穿「准入→提交」与「收敛→暂停」
	// 两落账的一致同步边界——advanceLocked（投递推进）/convergeOnTaskTerminal
	//（收敛+停队）/ControlSpace（pause/resume/成员/完成类控制）同锁串行，消除
	// 「读态→绑定应答→提交」窗内意图被取消后旧推进仍提交、与「收敛后暂停落账
	// 完成前推进下一棒」两竞态窗。锁内允许 SpaceBind 网络等待（秒级预算）——
	// 控制面最坏等待一次绑定往返，正确性优先；锁外操作（读面/消息面）零影响。
	muSpaceLocks sync.Mutex
	spaceLocks   map[string]*sync.Mutex

	// 关停优雅收束面（Close 三段式：关停位 → 排空 → 关库；codex 复审轮 2 核心
	// 机制 2 worker 化）：
	//   - muClose/closed 关停位：置位后新回调入口快速退出（hub 侧钩子注册在
	//     core 上无法注销——旧服务实例的注册持续可达，丢弃留痕不静默）；
	//   - convCh/convWorker：收敛异步化——core 钩子只做无阻塞入队（满时如实
	//     日志丢弃留痕，重启对账按实物收敛兜底），worker 消费队列执行收敛+停
	//     队；结构性消解两死锁面：①worker 不在 Dispatch 调度锁内被同步重入
	//     （fireTerminal 只入队）；②worker 不在帧回调内等同节点 ack（bind 网
	//     络等待只在 Dispatch/sweep 自己的 goroutine）。Close 排空=停入队→
	//     worker 排空队列余量→关库。
	muClose  sync.Mutex
	closed   bool
	convCh   chan protocol.Task
	convDone chan struct{}
	// onConvDequeued 测试观测缝（包内测试注入；生产恒 nil）：worker 出队即信号
	// ——测试据此确定性判定「队列项已被 worker 持有、正等空间锁落账」。
	onConvDequeued func()
}

// hubUnbind 待解除登记条目（key = sessionID；gen = 发起时的绑定代次——0 = 代次
// 未知，重试时先经代次探针重建）。
type hubUnbind struct {
	nodeID    string
	sessionID string
	gen       uint64
}

// Open 装配中心空间服务（dataDir 下 SQLite 快照库 + collab 内核）。快照损坏/
// 版本未知拒绝启动（宁拒启不瞎门）。装配完成即注册任务终态收敛钩子并启动兜底
// 扫查；用完须 Close。
func Open(dataDir string, c *core.Core, opt Options) (*Service, error) {
	if c == nil {
		return nil, errors.New("spaces: core 未装配")
	}
	snap, err := openSnapshotDB(dataDir)
	if err != nil {
		return nil, err
	}
	st, err := collab.Open(dataDir, collab.Options{
		Snapshot: snap,
		Hooks:    opt.Hooks,
		Log:      opt.Log,
		LeaseTTL: opt.LeaseTTL,
	})
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	s := &Service{Store: st, core: c, snap: snap, stop: make(chan struct{}), done: make(chan struct{}),
		closeDone: make(chan struct{}), pendingUnbinds: map[string]hubUnbind{}, spaceLocks: map[string]*sync.Mutex{},
		convCh: make(chan protocol.Task, 256), convDone: make(chan struct{}),
		genSeq: map[string]uint64{}, bindGens: map[string]uint64{}}
	// 双钩子统一经 hookEntry 入口（关停位检查 + 无阻塞入队——收敛异步 worker 化，
	// codex 复审轮 2 核心机制 2：fireTerminal 只入队不落账，结构性消解调度锁
	// 同步重入与帧回调内等 ack 两死锁面）。
	c.AddOnTaskTerminal(s.hookEntry)
	// 取消合并钩子（同一收敛函数双注册）：hub 侧取消首落终态时 Result 未达不猜
	//（钩子 nil 结果零操作，意图留待核对）；node 取消结果合并后按真实执行事实
	// 收口——「不把失联解释为没执行」与「事实到达即收口」两不误。
	c.AddOnTaskMerged(s.hookEntry)
	s.recover() // 装配期一次性（先于引用外返——天然不与 Close 竞态，不入排空面）
	go s.convWorker()
	go s.sweepLoop()
	return s, nil
}

// hookEntry 收敛回调统一入口（终态钩子与取消合并钩子共用）：关停位先检（已
// 关停 → 丢弃留痕，绝不带着已关库写账）；过检即在排空面计数——Close 的 Wait
// 与本 Add 同锁序，无「Wait 已开、Add 后到」的丢失窗口（集成者门禁修复）。
func (s *Service) hookEntry(t protocol.Task) {
	s.muClose.Lock()
	if s.closed {
		s.muClose.Unlock()
		if t.Session != nil {
			log.Printf("[hub] spaces: 服务已关停，任务终态回调丢弃 task=%s session=%s seq=%d（留痕不静默；重启对账按实物收敛）",
				t.ID, t.Session.SessionID, t.Session.TurnSeq)
		} else {
			log.Printf("[hub] spaces: 服务已关停，任务终态回调丢弃 task=%s（非会话轮；留痕不静默）", t.ID)
		}
		return
	}
	s.muClose.Unlock()
	// 无阻塞入队（codex 复审轮 2 核心机制 2）：fireTerminal 只入队——收敛+停队
	// 由 worker 消费执行，结构性消解调度锁同步重入与帧回调内等 ack 两死锁面；
	// 队列满如实丢弃留痕（重启对账按实物收敛兜底）。
	select {
	case s.convCh <- t:
	default:
		if t.Session != nil {
			log.Printf("[hub] spaces: 收敛队列已满，终态回调丢弃留痕 task=%s session=%s seq=%d（重启对账按实物收敛）",
				t.ID, t.Session.SessionID, t.Session.TurnSeq)
		} else {
			log.Printf("[hub] spaces: 收敛队列已满，终态回调丢弃留痕 task=%s（非会话轮；重启对账兜底）", t.ID)
		}
	}
}

// convWorker 收敛异步 worker（codex 复审轮 2 核心机制 2）：消费终态回调队列执行
// 收敛+停队落账；Close 停入队后排空队列余量再退出（排空面=「已接受执行按原契
// 约收束」的关停半边——落账绝不因关库竞态丢失）。
func (s *Service) convWorker() {
	defer close(s.convDone)
	for {
		select {
		case t := <-s.convCh:
			if s.onConvDequeued != nil {
				s.onConvDequeued()
			}
			s.convergeOnTaskTerminal(t)
		case <-s.stop:
			for { // 排空余量
				select {
				case t := <-s.convCh:
					if s.onConvDequeued != nil {
						s.onConvDequeued()
					}
					s.convergeOnTaskTerminal(t)
				default:
					return
				}
			}
		}
	}
}

// Close 优雅收束（三段式，幂等；内核 Store.Close 幂等）：
//  1. 置关停位——此后新回调入口快速退出（留痕不写账）；
//  2. 排空——先停兜底扫查并等其出临界（扫查写库全在 sweepLoop goroutine 内），
//     再等全部在途收敛回调出临界（预算兜底，超时如实日志）——「已接受执行按
//     原契约收束」的关停半边：收敛/暂停落账绝不因关库竞态丢失；
//  3. 关库——SQLite 快照库与 collab store 最后关（顺序不可倒置：先关库会让
//     在途回调写账失败，集成者门禁实锤「sql: database is closed」）。
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.muClose.Lock()
		s.closed = true
		s.muClose.Unlock()
		close(s.stop) // 一次性（Once 内；并发 Close 不再触达本通道）
		// 排空第一段：兜底扫查（写库全在 sweepLoop goroutine 内，done 即出临界）。
		<-s.done
		// 排空第二段：收敛 worker 排空队列余量（收敛+停队落账完成后才退出；
		// convDone 即排空完成——落账绝不因关库竞态丢失）。
		<-s.convDone
		// 关库（最后）。
		s.snapErr = s.snap.Close()
		s.storeErr = s.Store.Close()
		close(s.closeDone)
	})
	<-s.closeDone // 并发调用方等收束完成（一次性流程 + 完成信号）
	return s.storeErr
}

// Scope 空间 scope（collab.HubScope 词形单源；W07-01 定案「创建入口 scope 固定
// hub:<id>」——中心空间服务对内对外的唯一词形）。
func Scope(spaceID string) string { return collab.HubScope(spaceID) }

// nextBindGen 绑定代次分配（每 session 单调递增；重启后经 node ack current_gen
// 重同步——重启恢复从登记事实重建）。
func (s *Service) nextBindGen(sessionID string) uint64 {
	s.muBindGen.Lock()
	defer s.muBindGen.Unlock()
	s.genSeq[sessionID]++
	return s.genSeq[sessionID]
}

// resyncBindGen 代次计数重同步（bind stale ack-noop 的 ack.CurrentGen 事实——
// 只前进不回退）。
func (s *Service) resyncBindGen(sessionID string, current uint64) {
	s.muBindGen.Lock()
	defer s.muBindGen.Unlock()
	if current > s.genSeq[sessionID] {
		s.genSeq[sessionID] = current
	}
}

// noteBound 记录本进程已成功登记的代次（ensureBound 幂等跳过的事实源）。
func (s *Service) noteBound(sessionID string, gen uint64) {
	s.muBindGen.Lock()
	defer s.muBindGen.Unlock()
	s.bindGens[sessionID] = gen
}

// boundGenOf 本进程已成功登记的代次（0 = 未知——重启后未重建/未绑定过）。
func (s *Service) boundGenOf(sessionID string) uint64 {
	s.muBindGen.Lock()
	defer s.muBindGen.Unlock()
	return s.bindGens[sessionID]
}

// clearBound 清除进程内绑定代次事实（解除成立/stale 后下次解除先探针重建）。
func (s *Service) clearBound(sessionID string) {
	s.muBindGen.Lock()
	defer s.muBindGen.Unlock()
	delete(s.bindGens, sessionID)
}

// ensureBindSession 锁外绑定登记（codex 复审轮 2 核心机制 3——锁内零网络；bind
// 携代次，stale ack-noop 经 ack.CurrentGen 重同步后重试一次；进程内已知已 bind
// 即跳过——同代幂等由 node 承担兜底）。
func (s *Service) ensureBindSession(nodeID, sessionID, scope string) error {
	if s.boundGenOf(sessionID) != 0 {
		return nil // 进程内已知已 bind
	}
	gen := s.nextBindGen(sessionID)
	ack, err := s.core.SpaceBind(nodeID, protocol.SpaceBindPayload{Space: scope, SessionID: sessionID, Generation: gen}, 0)
	if err != nil {
		return err
	}
	if !ack.Accepted {
		// stale（重启后 hub 计数落后于 node 登记事实）：重同步后重试一次。
		s.resyncBindGen(sessionID, ack.CurrentGen)
		gen = s.nextBindGen(sessionID)
		ack, err = s.core.SpaceBind(nodeID, protocol.SpaceBindPayload{Space: scope, SessionID: sessionID, Generation: gen}, 0)
		if err != nil {
			return err
		}
		if !ack.Accepted {
			return fmt.Errorf("绑定登记 stale 重同步后仍不符（node current_gen=%d）", ack.CurrentGen)
		}
	}
	s.noteBound(sessionID, gen)
	return nil
}

// lockSpace 每空间调度互斥锁定位（惰性建锁；锁面见 Service.spaceLocks 注）。
func (s *Service) lockSpace(spaceID string) *sync.Mutex {
	s.muSpaceLocks.Lock()
	defer s.muSpaceLocks.Unlock()
	m, ok := s.spaceLocks[spaceID]
	if !ok {
		m = &sync.Mutex{}
		s.spaceLocks[spaceID] = m
	}
	return m
}

// ControlSpace 控制动作（调度互斥面）：pause/resume/成员/预算/策略/完成类控制
// 在空间调度锁内执行——控制变更对投递推进/收敛停队原子可见（codex 必修B-残②：
// 消除「收敛后暂停落账完成前推进下一棒」窗）。模型层逐动作授权/状态机照常。
func (s *Service) ControlSpace(spaceID string, req collab.ControlRequest, subj collab.Subject) (collab.ControlResult, bool, error) {
	m := s.lockSpace(spaceID)
	m.Lock()
	defer m.Unlock()
	return s.Control(spaceID, req, subj)
}

// SpaceTurnGuard 中心空间会话轮守卫闭包（生产装配单源：cmd/hub 与测试夹具同函数
// 接线——codex 必修A/F）：绑定会话普通轮（space=""）→ ErrSpaceDispatchRequired
// 409 指向空间派发；空间轮核对该会话在 hub 注册表的绑定空间与本轮 scope 一致才
// 放行（不一致 → ErrSpaceScopeMismatch——否则 SubmitSpaceTurn 也会被无差别守卫
// 拒绝，正常配置→启动→dispatch 的已扣预算意图当瞬态失败永久 pending 反复补投）；
// 未绑定会话恒放行（零干预）。guard 在 core 新轮预检消费（重放回执先行返回不受
// 守卫影响）。
func (s *Service) SpaceTurnGuard() func(sessionID, space string) error {
	return func(sessionID, space string) error {
		bound, ok := s.SessionOwnerSpace(sessionID)
		if !ok {
			return nil // 未绑定：零干预
		}
		if space == "" {
			return core.ErrSpaceDispatchRequired
		}
		if space != Scope(bound) {
			return fmt.Errorf("%w: 会话 %s 绑定空间 %s，本轮声明 %s", core.ErrSpaceScopeMismatch, sessionID, Scope(bound), space)
		}
		return nil
	}
}

// ---------- 派发面 ----------

// DispatchRequest 派发请求（REST/MCP 数据面；与 node 面同形同参）。
type DispatchRequest struct {
	SpaceID         string
	Member          string
	RequestID       string // 必填（受理稳定请求标识；重放命中回执）
	ExpectedVersion int64
	Term            uint64 // 驱动者形态携当前任期（旧 term 授权层拒绝）
	Note            string // 派发说明（成员轮输入正文）
}

// Dispatch 远端派发受理（outbox 先登记后 send）：成员寻址 → 版本门 → 绑定登记 →
// 意图持久 → 既有 session Task 通道投递 → 接单登记。返回 dup=true 表示同
// request_id 重放命中（返回原意图，零重复执行零重复扣减——N02）。
func (s *Service) Dispatch(req DispatchRequest, subj collab.Subject) (collab.DispatchIntent, bool, error) {
	if strings.TrimSpace(req.SpaceID) == "" || strings.TrimSpace(req.Member) == "" {
		return collab.DispatchIntent{}, false, &Error{Code: "invalid", Msg: "space_id 与 member 必填"}
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return collab.DispatchIntent{}, false, &Error{Code: "invalid", Msg: "request_id 必填（受理稳定请求标识，重试安全）"}
	}
	// 重放预检（先于一切副作用；与 node SpaceDispatch 同序）：同 ID 同成员=同文
	// 回放；异成员=冲突。
	if it, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		if it.Member != req.Member {
			return collab.DispatchIntent{}, false, &Error{Code: collab.CodeRequestConflict,
				Msg: "request_id 已被不同内容占用（已登记成员 " + it.Member + "）"}
		}
		s.advanceSpace(req.SpaceID) // 幂等重放亦推进滞留投递（先前受理若滞留 pending）
		return it, true, nil
	}
	// 成员寻址（只读预检；权威校验在 RecordDispatch/SubmitTurn 事务内复核）。
	mt, err := s.memberTarget(req.SpaceID, req.Member)
	if err != nil {
		return collab.DispatchIntent{}, false, err
	}
	// 版本门（W07-01 强制门——声明缺失/版本不符明确拒绝指路升级，绝不静默降级
	// 为普通任务执行；普通任务路径不经过本门零牵连）。
	if err := s.core.RequireSpacesCapable(mt.nodeID, mt.agent); err != nil {
		return collab.DispatchIntent{}, false, err
	}
	// collab 侧全部校验先于绑定（codex 必修D①——绑定副作用最后）：授权/版本/
	// 状态在发 space_bind 之前以模型层既有判定复核（Authorize 纯函数 + 版本/运行
	// 态同口径比较，RecordDispatch 事务内权威复核不变——预检只决定「值不值得在
	// node 留绑定副作用」，被拒请求零 node 残留）。
	if err := s.precheckDispatch(req, subj); err != nil {
		return collab.DispatchIntent{}, false, err
	}
	// 绑定登记（node 收件门受理面；代次协议——锁外网络等待，登记失败零副作用：
	// 意图零登记，宁拒不瞎投）。
	if err := s.ensureBindSession(mt.nodeID, mt.sessionID, Scope(req.SpaceID)); err != nil {
		return collab.DispatchIntent{}, false, bindError(err)
	}
	s.invalidateUnbind(mt.sessionID) // 新代绑定成立：旧代解除条目作废（代际语义）
	s.muDispatch.Lock()
	// 空间调度锁（登记段）：受理登记与控制变更（ControlSpace 同锁）串行。登记段
	// 显式解锁后再进入 advanceSpace（其自含三段式锁面：准入→锁外 bind→重入锁
	// 时间重核→提交——空间锁不可重入，持锁调用会自死锁，codex 复审轮 2 实测锚）。
	slm := s.lockSpace(req.SpaceID)
	slm.Lock()
	needUnbind := false // D-2：登记失败时的绑定残留补偿（成功登记/重放不清偿）
	compensate := func() {
		if needUnbind {
			// 绑定残留补偿（codex 必修D-2）：本次已 bind 的会话立即按代次条件
			// 解除（代次协议结构性防删新登记）；失败入重试队列。人话日志留痕，
			// 残留后续由 bind 幂等覆盖或解除重试收敛。
			s.UnbindSession(mt.nodeID, mt.sessionID)
		}
	}
	// 互斥段内重放重检（并发派发收敛：先落者为准，后来者按重放/冲突出回执）。
	if it, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		if it.Member != req.Member {
			return collab.DispatchIntent{}, false, &Error{Code: collab.CodeRequestConflict,
				Msg: "request_id 已被不同内容占用（已登记成员 " + it.Member + "）"}
		}
		return it, true, nil
	}
	// 轮号预留（受理期）：max(会话下一序号, 本会话在途意图最大 seq+1)——受理 seq
	// 严格递增不撞号（与 node M1 受理期预留同原则；派发互斥段内推导+落意图即
	// 原子预留）。
	sv, ok := s.core.GetSession(mt.sessionID)
	if !ok {
		return collab.DispatchIntent{}, false, &Error{Code: CodeSessionGone, Msg: "成员绑定会话不存在: " + mt.sessionID}
	}
	seq := nextTurnSeqOf(sv)
	if hw := s.intentSeqHighWater(mt.sessionID); hw >= seq {
		seq = hw + 1
	}
	it, _, dup, err := s.RecordDispatch(req.SpaceID, collab.DispatchRequest{
		RequestID:       req.RequestID,
		Member:          req.Member,
		SessionID:       mt.sessionID,
		TurnSeq:         seq,
		Term:            req.Term,
		ExpectedVersion: req.ExpectedVersion,
		Note:            req.Note,
	}, subj)
	if err != nil {
		needUnbind = true // 登记失败：预检-事务窗（成员移除/暂停/预算/版本）绑定残留补偿
		slm.Unlock()
		s.muDispatch.Unlock()
		compensate()
		return collab.DispatchIntent{}, false, err
	}
	if dup {
		slm.Unlock()
		s.muDispatch.Unlock()
		return it, true, nil // 并发收敛：另一 goroutine 已受理
	}
	// 登记段解锁（空间锁不可重入——advanceSpace 自含三段式锁面，持锁调用会
	// 自死锁）；补偿旗标此时必为 false（登记已成立）。
	slm.Unlock()
	s.muDispatch.Unlock()
	compensate()
	// 投递准入（outbox 先登记后 send；锁面见 advanceSpace 三段式）。
	s.advanceSpace(req.SpaceID)
	// 返回前重读意图（attach/取消收束发生在投递段内——调用方看到当前态：
	// accepted（带 TaskID）或确定性取消收束态，不回投递前快照）。
	if cur, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		it = cur
	}
	return it, false, nil
}

// memberTarget 成员寻址（W07-02 路由面）：成员名 = 开放目录词形
// "nodeID/agentName"（DirectoryAgent 同一判定的数据面），绑定会话 = hub 注册表
// 会话——成员名与绑定会话的 node/agent 逐一核对（防跨机冒绑）。命中返回路由
// 三元组。
type memberRoute struct {
	nodeID    string
	agent     string
	sessionID string
}

func (s *Service) memberTarget(spaceID, member string) (memberRoute, error) {
	nodeID, agent, ok := splitMemberName(member)
	if !ok {
		return memberRoute{}, &Error{Code: "invalid", Msg: "member 词形非法（须为目录形态 nodeID/agentName）: " + member}
	}
	if _, ok := s.core.DirectoryAgent(nodeID, agent); !ok {
		return memberRoute{}, &Error{Code: CodeAgentNotExposed,
			Msg: fmt.Sprintf("成员 %s 不在当前开放目录（exposed 能力未命中；先接入/开放该能力）", member)}
	}
	sp, ok := s.Space(spaceID)
	if !ok {
		return memberRoute{}, collab.ErrNotFound
	}
	var sessionID string
	for _, m := range sp.Members {
		if m.Name == member {
			if m.SessionID == "" {
				return memberRoute{}, &Error{Code: "invalid",
					Msg: "成员 " + member + " 未绑定原生会话（先 configure_members 携 session_id 绑定 hub 会话线程）"}
			}
			sessionID = m.SessionID
			break
		}
	}
	if sessionID == "" {
		return memberRoute{}, collab.ErrMemberNotFound
	}
	sv, ok := s.core.GetSession(sessionID)
	if !ok {
		return memberRoute{}, &Error{Code: CodeSessionGone, Msg: "成员绑定会话不存在: " + sessionID}
	}
	if sv.Status != store.SessionStatusActive {
		return memberRoute{}, &Error{Code: CodeSessionClosed, Msg: "成员绑定会话已关闭: " + sessionID}
	}
	if sv.NodeID != nodeID || sv.AgentName != agent {
		return memberRoute{}, &Error{Code: CodeBindingMismatch,
			Msg: fmt.Sprintf("成员 %s 与会话 %s 绑定不符（会话在 %s/%s）——拒绝跨机冒绑", member, sessionID, sv.NodeID, sv.AgentName)}
	}
	return memberRoute{nodeID: nodeID, agent: agent, sessionID: sessionID}, nil
}

// precheckDispatch 派发 collab 侧校验前置（codex 必修D①：全部校验通过才发
// space_bind——被拒请求在 node 零绑定残留）。只读复核，复用模型层既有判定：
// 版本 CAS 口径同 RecordDispatch；授权走 collab.Authorize 纯函数（逐动作表同一
// 判定，零第二套）；运行态门槛同 RecordDispatch。权威复核仍在 RecordDispatch
// 事务内（预检与落账窗口内变化照样被拒——N08 语义不变）。
func (s *Service) precheckDispatch(req DispatchRequest, subj collab.Subject) error {
	sp, ok := s.Space(req.SpaceID)
	if !ok {
		return collab.ErrNotFound
	}
	if req.ExpectedVersion != sp.Version {
		return fmt.Errorf("%w: 预期版本 %d ≠ 现版本 %d（读档已过期，刷新后重试）", collab.ErrVersionConflict, req.ExpectedVersion, sp.Version)
	}
	if err := collab.Authorize(collab.ActDispatch, &sp, subj, req.Term, protocol.NowUTC()); err != nil {
		return err
	}
	if sp.Status != collab.StatusRunning {
		return fmt.Errorf("%w: dispatch：仅运行态可派发（现 %s；先显式启动）", collab.ErrIllegalTransition, sp.Status)
	}
	// 预算只读核算（codex 必修D-2：预检覆盖 RecordDispatch 的预算拒绝面——
	// 三计数分离同口径；权威复核仍在 RecordDispatch 事务内）。
	if sp.Budget.MaxDispatchRounds > 0 && sp.Used.DispatchRounds >= sp.Budget.MaxDispatchRounds {
		return fmt.Errorf("%w: dispatch_round 已达上限 %d", collab.ErrBudgetExhausted, sp.Budget.MaxDispatchRounds)
	}
	if sp.Budget.MaxWallSeconds > 0 && wallSecondsNow(sp.Used) >= int64(sp.Budget.MaxWallSeconds) {
		return fmt.Errorf("%w: 运行墙钟已达上限 %ds", collab.ErrBudgetExhausted, sp.Budget.MaxWallSeconds)
	}
	return nil
}

// wallSecondsNow 墙钟用量快照（running 段含未结算；与 collab BudgetUsed 同口径
// 的只读复算——包内字段 exported，算术本地）。
func wallSecondsNow(u collab.BudgetUsed) int64 {
	total := u.WallSeconds
	if !u.RunningSince.IsZero() && protocol.NowUTC().After(u.RunningSince) {
		total += int64(protocol.NowUTC().Sub(u.RunningSince) / time.Second)
	}
	return total
}

// splitMemberName 成员名拆解（目录词形 nodeID/agentName；nodeID 不含 /、agent
// 非空——与目录条目一一对应）。
func splitMemberName(member string) (nodeID, agent string, ok bool) {
	member = strings.TrimSpace(member)
	i := strings.Index(member, "/")
	if i <= 0 || i == len(member)-1 {
		return "", "", false
	}
	return member[:i], member[i+1:], true
}

// submitLocked 投递单条已登记意图（既有 session Task 通道；失败分类收束）：
//   - 确定性失败（会话关闭/不存在/绑定漂移/轮号冲突/输入非法）→ 意图按取消收束
//     （如实注记，绝不留幽灵执行窗口），错误如实上抛（CodeTurnFailed）；
//   - 瞬态失败（队列满/在途首轮/容量）→ 意图保持 pending，兜底扫查重试。
//
// 调用约定：调用方持空间调度锁（锁内零网络应答等待——bind 在锁外 ensureBound，
// 提交本体为 hub 侧落库+无应答投递帧，codex 复审轮 2 核心机制 3）。
func (s *Service) submitLocked(spaceID string, it collab.DispatchIntent) {
	// 版本门覆盖全部实际外发路径（codex 必修C：首投/补投/重试统一在出口核对——
	// node 撤声明后 sweep 补投/补绑定不再绕过门）。确定性拒绝 → 意图取消收束
	//（撤声明后该 node 不可再承载空间轮，滞留重试无意义）。
	if nodeID, agent, ok := splitMemberName(it.Member); !ok {
		s.cancelIntent(spaceID, it, "成员词形非法（须 nodeID/agentName）：意图取消收束")
		return
	} else if err := s.core.RequireSpacesCapable(nodeID, agent); err != nil {
		s.cancelIntent(spaceID, it, "版本门拒绝（"+err.Error()+"）：意图取消收束")
		return
	}
	ref, dup, err := s.core.SubmitSpaceTurn(it.SessionID, core.SessionTurnRequest{
		Prompt:    it.Note,
		TurnSeq:   it.TurnSeq,
		RequestID: it.RequestID,
	}, systemSubject().String(), Scope(spaceID))
	if err != nil {
		if isDeterministicTurnErr(err) {
			s.cancelIntent(spaceID, it, "轮提交确定性失败（"+err.Error()+"）：意图取消收束")
			return
		}
		log.Printf("[hub] 空间意图投递暂缓 space=%s intent=%s session=%s seq=%d: %v（兜底扫查重试）",
			spaceID, it.RequestID, it.SessionID, it.TurnSeq, err)
		return
	}
	if dup {
		// 重放命中（先前受理的轮已落库——崩溃窗恢复路径）：按轮行实物四态核验
		//（codex 必修E）接单登记——matched 补关联；conflict 如实取消；missing
		// 留待核对；none 理论不可达（dup 蕴含轮行在）防御日志。
		tr, st := s.intentTurnFact(it)
		switch st {
		case turnFactMatched:
			s.attachIntent(spaceID, it, tr.TaskID)
		case turnFactConflict:
			s.cancelIntent(spaceID, it, "重放关联冲突（轮行/任务来源标记与本意图不符）：取消收束")
		case turnFactMissing:
			log.Printf("[hub] 重放关联证据缺失（任务已清理），意图留待核对 space=%s intent=%s", spaceID, it.RequestID)
		default:
			log.Printf("[hub] 重放命中但无轮行（防御位）space=%s intent=%s", spaceID, it.RequestID)
		}
		return
	}
	s.attachIntent(spaceID, it, ref.TaskID)
}

// attachIntent 接单登记（pending→accepted；执行侧记账 system 主体）。失败不回滚
// 轮（轮已入账会照常执行，结果照常回流）——意图滞留 pending 由兜底扫查按轮行实物
// 补登记，重启对账按实物收敛（同 node AttachDispatchTask 失败路径语义）。
func (s *Service) attachIntent(spaceID string, it collab.DispatchIntent, taskID string) {
	if _, err := s.AttachDispatchTask(spaceID, it.RequestID, taskID, systemSubject()); err != nil {
		log.Printf("[hub] 空间意图接单登记失败 space=%s intent=%s task=%s: %v（轮已受理，扫查/重启对账收敛）",
			spaceID, it.RequestID, taskID, err)
		return
	}
	log.Printf("[hub] 中心空间派发投递 space=%s member=%s intent=%s task=%s session=%s seq=%d",
		spaceID, it.Member, it.RequestID, taskID, it.SessionID, it.TurnSeq)
}

// cancelIntent 确定性不可投递的意图收束（system 主体取消——零执行事实，取消是
// 最如实的账；失败滞留由重启对账兜底）。返回 CodeTurnFailed 操作错误（受理已
// 成立、投递失败如实可见）。
func (s *Service) cancelIntent(spaceID string, it collab.DispatchIntent, note string) error {
	if _, err := s.ConvergeIntent(spaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID, note, systemSubject()); err != nil {
		log.Printf("[hub] 不可投递意图取消收束失败 space=%s intent=%s: %v（重启对账兜底）", spaceID, it.RequestID, err)
		return &Error{Code: CodeTurnFailed, Msg: note}
	}
	log.Printf("[hub] 中心空间意图取消收束 space=%s intent=%s: %s", spaceID, it.RequestID, note)
	return &Error{Code: CodeTurnFailed, Msg: note}
}

// nodeIDOf 意图目标 node（兜底重试登记的寻址：会话注册表 node 绑定为唯一真相；
// 会话缺失返回空串——SpaceBind 对空 node 的发送失败按暂缓处理）。
func (s *Service) nodeIDOf(it collab.DispatchIntent) string {
	if sv, ok := s.core.GetSession(it.SessionID); ok {
		return sv.NodeID
	}
	return ""
}

// UnbindSession 解除登记（codex 必修D②解除闭环；代次协议——比代次不比 scope）：
// 成员解绑/会话归属释放后向 node 下发 space_unbind（携发起时的绑定代次；代次未
// 知先经代次探针从 node 登记事实重建）。即时尝试；失败入队由兜底扫查重试（解除
// 失败重试面）。stale ack-noop = 新代登记已在位——语义满足（结构性保证旧代解除
// 不删新登记）。重启队列丢失为已登记边界：node 残留绑定 fail-closed（普通轮拒
// 收），后续任何 space_bind 幂等覆盖自愈。
func (s *Service) UnbindSession(nodeID, sessionID string) {
	if !s.unbindOnce(nodeID, sessionID) {
		s.muUnbind.Lock()
		s.pendingUnbinds[sessionID] = hubUnbind{nodeID: nodeID, sessionID: sessionID}
		s.muUnbind.Unlock()
		log.Printf("[hub] 中心空间解除登记未达（入队重试）session=%s", sessionID)
	}
}

// unbindOnce 单次解除尝试（返回 true = 终态：解除成立或语义满足）。代次未知
// （boundGen 空——重启后未重建）先经代次探针（gen=0 解除=node 零副作用回
// current_gen）从登记事实重建，再按事实解除。
func (s *Service) unbindOnce(nodeID, sessionID string) bool {
	gen := s.boundGenOf(sessionID)
	if gen == 0 {
		ack, err := s.core.SpaceUnbind(nodeID, sessionID, 0, 0)
		if err != nil {
			log.Printf("[hub] 中心空间解除探针未达 session=%s: %v", sessionID, err)
			return false
		}
		gen = ack.CurrentGen
		if gen == 0 {
			s.clearBound(sessionID)
			log.Printf("[hub] 中心空间解除探针：node 无登记（语义已满足）session=%s", sessionID)
			return true
		}
	}
	ack, err := s.core.SpaceUnbind(nodeID, sessionID, gen, 0)
	if err != nil {
		log.Printf("[hub] 中心空间解除登记未达 session=%s gen=%d: %v", sessionID, gen, err)
		return false
	}
	if ack.Accepted {
		s.clearBound(sessionID)
		log.Printf("[hub] 中心空间绑定已解除 session=%s gen=%d", sessionID, gen)
		return true
	}
	// stale ack-noop：node 当前登记代次已前移——新代登记在位，旧代解除语义满足
	//（结构性：旧代解除删不掉新登记）；进程内事实同步清除（下次解除先探针）。
	s.clearBound(sessionID)
	log.Printf("[hub] 中心空间解除 ack-noop（node 登记代次 %d 已前移于本进程事实 %d，语义满足）session=%s",
		ack.CurrentGen, gen, sessionID)
	return true
}

// invalidateUnbind 解除登记代际作废（codex 复审必修D-1）：同 session 新 bind
// 成立即作废旧代解除条目——旧重试不再下发（node 条件删除为第二道防线）。绑定
// 成功路径（Dispatch/补绑定）调用。
func (s *Service) invalidateUnbind(sessionID string) {
	s.muUnbind.Lock()
	if _, ok := s.pendingUnbinds[sessionID]; ok {
		delete(s.pendingUnbinds, sessionID)
		s.muUnbind.Unlock()
		log.Printf("[hub] 旧代解除登记已作废（session 重新绑定成立）session=%s", sessionID)
		return
	}
	s.muUnbind.Unlock()
}

// KickSpace 显式推进单空间投递（api resume 面接线——node NotifySpaceResumed 同
// 规：恢复后已受理排队意图继续投递；扫查兜底不依赖本调用）。
func (s *Service) KickSpace(spaceID string) { s.advanceSpace(spaceID) }

// PendingUnbindSessions 待解除登记会话清单（运维/测试观察面；解除重试面状态）。
func (s *Service) PendingUnbindSessions() []string {
	s.muUnbind.Lock()
	defer s.muUnbind.Unlock()
	out := make([]string, 0, len(s.pendingUnbinds))
	for sid := range s.pendingUnbinds {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out
}

// retryUnbinds 解除登记重试（兜底扫查节拍；成功即出队）。
func (s *Service) retryUnbinds() {
	s.muUnbind.Lock()
	pending := make([]hubUnbind, 0, len(s.pendingUnbinds))
	for _, u := range s.pendingUnbinds {
		pending = append(pending, u)
	}
	s.muUnbind.Unlock()
	for _, u := range pending {
		if !s.unbindOnce(u.nodeID, u.sessionID) {
			continue // 留队下次扫查重试
		}
		s.muUnbind.Lock()
		delete(s.pendingUnbinds, u.sessionID)
		s.muUnbind.Unlock()
		log.Printf("[hub] 中心空间绑定解除重试收口 session=%s", u.sessionID)
	}
}

// isDeterministicTurnErr 轮提交确定性失败（重试永不成功——取消收束）。
func isDeterministicTurnErr(err error) bool {
	return errors.Is(err, core.ErrSessionNotFound) ||
		errors.Is(err, core.ErrSessionClosed) ||
		errors.Is(err, core.ErrSessionBindingChanged) ||
		errors.Is(err, core.ErrSessionAgentUnsupported) ||
		errors.Is(err, core.ErrSessionTurnConflict) ||
		errors.Is(err, core.ErrSessionRequestConflict) || // codex 必修E：旧普通轮占用 request_id——重试永不成功，如实取消落账
		errors.Is(err, core.ErrInvalid)
}

// isDeterministicBindErr 绑定登记确定性失败（node 显式拒绝——重试同一形态仍拒）。
func isDeterministicBindErr(err error) bool {
	var br *core.BindReject
	return errors.As(err, &br)
}

// bindError SpaceBind 错误 → 派发面操作错误（REST 投影）。
func bindError(err error) error {
	var br *core.BindReject
	switch {
	case errors.Is(err, core.ErrNodeOffline):
		return &Error{Code: CodeNodeOffline, Msg: "目标 node 不在线，绑定登记未达：" + err.Error() + "（node 重连后重试；已受理意图不受影响——轮任务持久排队补投）"}
	case errors.Is(err, core.ErrBindTimeout):
		return &Error{Code: CodeBindTimeout, Msg: "绑定登记应答超时：" + err.Error() + "（node 无响应；稍后重试）"}
	case errors.As(err, &br):
		return &Error{Code: CodeBindRejected, Msg: err.Error()}
	}
	return &Error{Code: CodeBindRejected, Msg: err.Error()}
}

// ---------- 结果收敛（可靠结果映射） ----------

// convergeOnTaskTerminal 任务首落终态钩子（装配期经 core.AddOnTaskTerminal 注册；
// core 结果仲裁保证归属（TargetNode==env.From）与租约等值后才落终态——迟到/伪
// 来源结果到不了本钩子）。中心空间轮（Session.Space 恒 hub:<id>）按终态分类收敛：
//   - completed → converged（执行事实成立）；
//   - failed → converged + 记账码如实注记（失败是已收束的执行事实）；
//   - cancelled → cancelled + 系统暂停停队（人工介入优先，resume 显式放行）；
//   - needs_review 等不确定 → 不收敛：意图留待核对（在途），绝不猜测、绝不重跑、
//     绝不自动重发另一节点；
//   - 任务非空间轮 / scope 词形非法 → 零操作（普通任务与异常帧零牵连）。
//
// 已接受轮次事实照常落账，不触发暂停空间的下一步（收敛不 kick——结果落账不自动
// 推进，N04；驱动者显式再派发）。
func (s *Service) convergeOnTaskTerminal(t protocol.Task) {
	if t.Session == nil || t.Session.TurnSeq <= 0 || t.Result == nil {
		return
	}
	spaceID := collab.HubScopeID(t.Session.Space)
	if spaceID == "" {
		return // 非中心空间轮（本机空间轮/普通轮）：本服务零操作
	}
	outcome, note, halt := classifyTaskFact(t)
	if outcome == "" {
		log.Printf("[hub] 中心空间轮终不确定，留待核对（不收敛不重跑不转投）space=%s session=%s seq=%d task=%s status=%s err=%q",
			spaceID, t.Session.SessionID, t.Session.TurnSeq, t.ID, t.Status, t.Result.Error)
		return
	}
	it, ok := s.intentByTurn(spaceID, t.Session.SessionID, t.Session.TurnSeq)
	if !ok {
		return // 无在途意图锚（重放/已收敛）：零操作
	}
	// 空间调度锁（codex 复审必修B-残②）：收敛与停队两落账在同一锁区完成——推进
	// 面（Dispatch/advance/sweep 同锁）要么见到「收敛前（accepted 在途→等待）」
	// 要么见到「暂停后（运行态门拦下）」，不存在「收敛已落、暂停未落」的中间态
	// 被抢跑推进下一棒。
	m := s.lockSpace(spaceID)
	m.Lock()
	defer m.Unlock()
	if _, err := s.ConvergeIntent(spaceID, it.RequestID, outcome, it.TaskID, note, systemSubject()); err != nil {
		log.Printf("[hub] 中心空间意图收敛落账失败 space=%s intent=%s: %v（重启对账按实物重试）", spaceID, it.RequestID, err)
		return
	}
	log.Printf("[hub] 中心空间意图收敛 space=%s intent=%s → %s（%s）", spaceID, it.RequestID, outcome, note)
	if !halt {
		// 串行队列推进（node spaceConvergeTurn kick 同规，调度锁内）：已受理的
		// 排队意图继续投递——结果落账不「产生」新派发（N04），但已受理队列照常
		// 推进；paused 空间由准入运行态门拦下（不触发暂停空间的下一步）。worker
		// 路径无 bind 网络等待（bind 归 advanceSpace 的锁外段）。
		s.kickLocked(spaceID)
	} else {
		if _, applied, perr := s.Store.SystemPause(spaceID, "成员轮被取消（人工介入）：新增派发暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[hub] 取消后系统暂停失败 space=%s: %v", spaceID, perr)
		} else if applied {
			log.Printf("[hub] 中心空间已因成员轮取消自动暂停 space=%s", spaceID)
		}
	}
}

// classifyTaskFact 任务终态 → 意图收敛分类（outcome 空 = 不确定留待核对；与 node
// classifySpaceTurn 同表：completed→converged / failed→记账码如实 converged /
// cancelled→cancelled+停队 / 其余（needs_review 等）→留待核对）。
func classifyTaskFact(t protocol.Task) (outcome, note string, halt bool) {
	code := ""
	if t.Result != nil {
		code = t.Result.Error
	}
	if i := strings.Index(code, ":"); i > 0 {
		code = code[:i]
	}
	switch t.Status {
	case protocol.StatusCompleted:
		return string(collab.IntentConverged), "成员轮完成（执行事实落账）", false
	case protocol.StatusFailed:
		return string(collab.IntentConverged), "failed:" + code + "（轮终失败，按记账码如实落账）", false
	case protocol.StatusCancelled:
		return string(collab.IntentCancelled), firstSegment(code, "cancelled"), true
	default:
		return "", "", false
	}
}

// firstSegment 错误首段或缺省词（同 node spacedispatch）。
func firstSegment(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

// turnFactState 意图轮行实物核验结论（codex 复审必修E-残——四态取代 bool）：
//   - turnFactNone：无轮行 = 未投递（可取消收束——「不自动重跑」语义）；
//   - turnFactMatched：request_id+预留 seq+任务空间来源三重全等 = 补接单收敛；
//   - turnFactConflict：轮行在案但 seq/来源标记不符 = 旧轮撞号冒领（如实取消
//     注记冲突，绝不冒充 converged）；
//   - turnFactMissing：轮行在案、任务已过保留期清理 = 执行事实不可核验 → 留待
//     核对（needs_review 同族语义——绝不释放在途、绝不取消冒认未投递）。
type turnFactState int

const (
	turnFactNone turnFactState = iota
	turnFactMatched
	turnFactConflict
	turnFactMissing
)

// intentTurnFact 意图轮行实物定位（四态；调用点：advanceLocked 崩溃窗/提交重放
// 补关联/pending 恢复/convergeIntentByTaskFact 空回退——逐一对齐各态语义）。
func (s *Service) intentTurnFact(it collab.DispatchIntent) (hubstore.SessionTurn, turnFactState) {
	tr, ok := s.core.SessionTurnByRequest(it.SessionID, it.RequestID)
	if !ok {
		return hubstore.SessionTurn{}, turnFactNone
	}
	if tr.Seq != it.TurnSeq {
		return hubstore.SessionTurn{}, turnFactConflict
	}
	t, ok := s.core.GetTask(tr.TaskID)
	if !ok {
		// 任务已清理：Attach 失败+超保留期的形态——执行事实不可核验。
		return hubstore.SessionTurn{}, turnFactMissing
	}
	if t.Session == nil || t.Session.SessionID != it.SessionID || t.Session.Space != Scope(it.SpaceID) {
		return hubstore.SessionTurn{}, turnFactConflict
	}
	return tr, turnFactMatched
}

// intentByTurn 按意图锚（session+seq）找在途意图（迟到结果消费的锚核验：意图与
// 任务同轮号才收敛——防伪任务帧冒领意图）。登记序扫描，意图列表有界。
func (s *Service) intentByTurn(spaceID, sessionID string, seq int64) (collab.DispatchIntent, bool) {
	for _, it := range s.Intents(spaceID) {
		if it.SessionID == sessionID && it.TurnSeq == seq && it.Status.InFlight() {
			return it, true
		}
	}
	return collab.DispatchIntent{}, false
}

// ---------- 恢复对账（重启映射） ----------

// recover 重启对账闭环（装配期一次性；先于兜底扫查）：在途意图按账本实物分类，
// 绝不自动重跑——
//   - pending：先查轮行实物——有实物（受理→投递崩溃窗：轮已落库、接单登记未及
//     落盘）接单登记收敛到 accepted（任务已终态则立即按实物分类——终态钩子不再
//     重放）；无实物取消收束（「进程恢复不得凭回调自动重跑」，恢复后由驱动者显式
//     重新派发）；
//   - accepted：任务存在且终态 → 按实物分类（cancelled 对齐活路径系统暂停停队）；
//     任务存在非终态 → 留在途（任务链补投/执行中，结果到达时终态钩子收敛——不把
//     失联解释为没执行）；任务已过保留期清理 → 留待核对（事实不可考，绝不猜测、
//     绝不自动重发另一节点）。
func (s *Service) recover() {
	for _, it := range s.RecoverIntents() {
		switch it.Status {
		case collab.IntentPending:
			// 轮行实物四态核验（codex 复审必修E-残：none=未投递可取消；matched=
			// 补接单收敛；conflict=如实取消注记冲突；missing=证据缺失留待核对
			// ——任务清理 ≠ 从未投递，绝不取消冒认、绝不释放在途）。
			tr, st := s.intentTurnFact(it)
			switch st {
			case turnFactNone:
				if _, err := s.ConvergeIntent(it.SpaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID,
					"中心恢复对账：未投递意图不自动重跑（恢复后显式重新派发）", systemSubject()); err != nil {
					log.Printf("[hub] 重启对账：pending 意图取消收束失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
					continue
				}
				log.Printf("[hub] 重启对账：未投递意图已取消收束 space=%s intent=%s member=%s", it.SpaceID, it.RequestID, it.Member)
				continue
			case turnFactConflict:
				if _, err := s.ConvergeIntent(it.SpaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID,
					"中心恢复对账：轮行/任务来源标记与本意图冲突（旧轮撞号嫌疑）——取消收束待人工核对", systemSubject()); err != nil {
					log.Printf("[hub] 重启对账：冲突意图取消收束失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
					continue
				}
				log.Printf("[hub] 重启对账：冲突意图已取消收束（注记冲突）space=%s intent=%s", it.SpaceID, it.RequestID)
				continue
			case turnFactMissing:
				log.Printf("[hub] 重启对账：意图轮证据缺失（任务已清理），留待核对 space=%s intent=%s member=%s", it.SpaceID, it.RequestID, it.Member)
				continue
			}
			_ = tr
			// 有轮行实物：接单登记收敛（同文重放路径 AttachDispatchTask 幂等命中
			// 原结果；已收敛意图 attach 报 illegal_transition → 按实物分类兜底）。
			if _, err := s.AttachDispatchTask(it.SpaceID, it.RequestID, tr.TaskID, systemSubject()); err == nil {
				log.Printf("[hub] 重启对账：崩溃窗意图接单登记收敛 space=%s intent=%s task=%s", it.SpaceID, it.RequestID, tr.TaskID)
			}
			s.convergeIntentByTaskFact(it, "pending+实物")
		case collab.IntentAccepted:
			s.convergeIntentByTaskFact(it, "accepted")
		}
	}
}

// convergeIntentByTaskFact 按任务实物收敛单条在途意图（重启对账共用体）：任务
// 终态 → 分类收敛；非终态 → 留在途；任务已清理 → 留待核对。失败（落盘）留待
// 下次启动重试。
func (s *Service) convergeIntentByTaskFact(it collab.DispatchIntent, via string) {
	taskID := it.TaskID
	if taskID == "" {
		// pending 崩溃窗回退路径：轮行实物即任务锚（四态核验同 intentTurnFact——
		// missing/conflict 不取锚，交由恢复对账主流程按态处置）。
		if tr, st := s.intentTurnFact(it); st == turnFactMatched {
			taskID = tr.TaskID
		}
	}
	if taskID == "" {
		log.Printf("[hub] 重启对账：意图无任务实物，留待核对 space=%s intent=%s via=%s", it.SpaceID, it.RequestID, via)
		return
	}
	t, ok := s.core.GetTask(taskID)
	if !ok {
		log.Printf("[hub] 重启对账：意图任务已过保留期清理，留待核对 space=%s intent=%s task=%s via=%s", it.SpaceID, it.RequestID, taskID, via)
		return
	}
	if t.Result == nil {
		log.Printf("[hub] 重启对账：意图任务非终态，留在途等结果 space=%s intent=%s task=%s state=%s via=%s", it.SpaceID, it.RequestID, taskID, t.Status, via)
		return
	}
	outcome, note, halt := classifyTaskFact(t)
	if outcome == "" {
		log.Printf("[hub] 重启对账：轮终不确定（%s），意图留待核对 space=%s intent=%s via=%s", t.Status, it.SpaceID, it.RequestID, via)
		return
	}
	if _, err := s.ConvergeIntent(it.SpaceID, it.RequestID, outcome, it.TaskID, note+"（重启对账）", systemSubject()); err != nil {
		log.Printf("[hub] 重启对账：意图收敛失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
		return
	}
	log.Printf("[hub] 重启对账：意图按任务实物收敛 space=%s intent=%s → %s via=%s", it.SpaceID, it.RequestID, outcome, via)
	if halt {
		if _, applied, perr := s.Store.SystemPause(it.SpaceID, "重启对账：成员轮取消（人工介入）落账，新增派发暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[hub] 重启对账取消停队系统暂停失败 space=%s: %v", it.SpaceID, perr)
		} else if applied {
			log.Printf("[hub] 重启对账：空间已因取消意图收敛暂停停队 space=%s", it.SpaceID)
		}
	}
}

// ---------- 兜底扫查 ----------

// sweepLoop 周期扫查（仅扫有在途意图的空间）：pending 意图补投递（轮行实物补接
// 单登记；无实物重新提交——绑定登记幂等自愈）。随 Close 停止。
func (s *Service) sweepLoop() {
	defer close(s.done)
	tk := time.NewTicker(sweepInterval)
	defer tk.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tk.C:
			s.retryUnbinds() // 解除登记重试面（codex 必修D③）
			for _, sp := range s.Spaces() {
				if s.InFlightCount(sp.ID) > 0 {
					s.advanceSpace(sp.ID)
				}
			}
		}
	}
}

// advanceSpace 单空间投递推进（codex 必修B——对齐 node advanceSpace 准入语义，
// 复用 collab 既有状态/租约判定零第二套）：
//  1. 非运行态 → 不投（paused/blocked/needs_review 排队意图等待显式 resume；
//     pause 后 sweep 零投递——codex 实证反例闭合）；
//  2. 存在 accepted 在途 → 串行门（同空间一次一棒——双成员并发执行反例闭合；
//     轮终收敛钩子推进下一棒，节点间并行不受影响）；
//  3. 首个 pending 处执行租约前置检查（与 node advanceSpace S1 同口径：仅在
//     存在待投递意图时触发——HTTP 空闲不判死）：租约过期 → PauseExpired（同一
//     collab 事务：暂停 + 未投递意图取消收束——意图授权随租约失效）；
//  4. 队首 pending 崩溃窗（轮行实物三重核验在册）→ 补接单登记即收敛 accepted；
//  5. 队首健康 → submitIntent 投递这一棒（每次推进至多一棒，串行保证）。
//
// 并发重入（Dispatch/收敛钩子/sweep 三入口）安全：重复推进对同一 pending 的
// 提交幂等收敛（SubmitSpaceTurn 同 request_id+seq 重放命中、AttachDispatchTask
// 同任务幂等）——不叠投不双跑。accepted 留在途等结果（不把失联解释为没执行）。
func (s *Service) advanceSpace(spaceID string) {
	// 三段式（codex 复审轮 2 核心机制 3——锁内零网络等待）：
	//   1. 锁内准入判定（运行态/串行门/租约前置/四态实物核验）与意图预留；
	//   2. 解锁执行 bind（携代次——网络应答等待只在锁外）；
	//   3. 重入锁时间重核（意图仍 pending 未接单 + 空间仍 running + 租约仍未过
	//      期）后提交——窗内 pause/取消/租约过期即放弃提交（意图按既有分类由
	//      sweep/恢复收束；已发生的 bind 由代次解除补偿/幂等覆盖收尾）。
	m := s.lockSpace(spaceID)
	m.Lock()
	cand, ok := s.admissionLocked(spaceID)
	m.Unlock()
	if !ok {
		return
	}
	if err := s.ensureBound(spaceID, cand); err != nil {
		log.Printf("[hub] 空间意图投递暂缓（绑定登记未达）space=%s intent=%s: %v（下次扫查重试）", spaceID, cand.RequestID, err)
		return
	}
	m.Lock()
	defer m.Unlock()
	// 重入锁时间重核。
	cur, ok := s.IntentByRequest(spaceID, cand.RequestID)
	if !ok || cur.Status != collab.IntentPending || cur.TaskID != "" {
		return // 已取消/已收束/已接单：放弃提交（不留幽灵执行）
	}
	sp, ok := s.Space(spaceID)
	if !ok || sp.Status != collab.StatusRunning {
		return // paused/blocked/needs_review：排队意图等显式 resume
	}
	now := protocol.NowUTC()
	if !sp.Leader.LeaseValid(now) && !sp.Leader.LeaseUntil.IsZero() {
		return // 租约过期：下次推进 PauseExpired 处置（取消未投递意图）
	}
	s.submitLocked(spaceID, cur)
}

// ensureBound 锁外绑定登记（意图维度便捷形；代次协议见 ensureBindSession）。
func (s *Service) ensureBound(spaceID string, it collab.DispatchIntent) error {
	if s.boundGenOf(it.SessionID) != 0 {
		return nil
	}
	nodeID := s.nodeIDOf(it)
	if nodeID == "" {
		return fmt.Errorf("意图目标 node 定位失败（会话注册表无 %s）", it.SessionID)
	}
	return s.ensureBindSession(nodeID, it.SessionID, Scope(spaceID))
}

// kickLocked 调度锁内推进（收敛 worker 路径：准入→提交无网络应答等待；bind 在
// advanceSpace 的锁外段——本路径候选均为进程内已知已 bind 的意图，ensureBound
// 直通跳过）。
func (s *Service) kickLocked(spaceID string) {
	cand, ok := s.admissionLocked(spaceID)
	if !ok {
		return
	}
	if s.boundGenOf(cand.SessionID) == 0 {
		// 理论不可达（收敛推进的候选必经 advanceSpace 锁外 bind 先行）——防御
		// 交回全三段推进（锁内零网络纪律优先于路径优化）。
		s.muUnbind.Lock()
		s.muUnbind.Unlock()
		go s.advanceSpace(spaceID)
		return
	}
	// 时间重核后提交（同 advanceSpace 第三段）。
	cur, ok := s.IntentByRequest(spaceID, cand.RequestID)
	if !ok || cur.Status != collab.IntentPending || cur.TaskID != "" {
		return
	}
	sp, ok := s.Space(spaceID)
	if !ok || sp.Status != collab.StatusRunning {
		return
	}
	now := protocol.NowUTC()
	if !sp.Leader.LeaseValid(now) && !sp.Leader.LeaseUntil.IsZero() {
		return
	}
	s.submitLocked(spaceID, cur)
}

// admissionLocked 投递准入判定（调用方持空间调度锁；返回需提交的候选意图）：
//   - 非运行态 → 不投（paused/blocked/needs_review 排队意图等待显式 resume）；
//   - 存在 accepted 在途 → 串行门（同空间一次一棒）；
//   - 首个 pending 处租约前置检查（S1 口径：仅存在待投递意图时触发）——租约过期
//     → PauseExpired（同一 collab 事务：暂停 + 未投递意图取消收束）；
//   - pending 四态实物核验：matched 补接单（本棒已消耗）/conflict 取消收束/
//     missing 留待核对 / none 返回候选（需 bind+提交）。
func (s *Service) admissionLocked(spaceID string) (collab.DispatchIntent, bool) {
	sp, ok := s.Space(spaceID)
	if !ok || sp.Status != collab.StatusRunning {
		return collab.DispatchIntent{}, false
	}
	intents := s.Intents(spaceID)
	for i := range intents {
		if intents[i].Status == collab.IntentAccepted {
			return collab.DispatchIntent{}, false // 串行门：已有在途投递，等其收敛
		}
	}
	now := protocol.NowUTC()
	for i := range intents {
		it := intents[i]
		if it.Status != collab.IntentPending {
			continue
		}
		if !sp.Leader.LeaseValid(now) && !sp.Leader.LeaseUntil.IsZero() {
			reason := "负责人租约已过期（调度前置检查，任期 " + strconvFormatUint(sp.Leader.Term) + "）：自动暂停，恢复须显式 resume"
			updated, cancelled, err := s.Store.PauseExpired(spaceID, reason)
			if err != nil {
				log.Printf("[hub] 空间租约过期自动暂停失败 space=%s: %v（下次前置检查重试）", spaceID, err)
				return collab.DispatchIntent{}, false
			}
			log.Printf("[hub] 空间租约过期自动暂停 space=%s 任期=%d 取消未投递意图=%d 暂停原因=%q",
				spaceID, updated.Leader.Term, cancelled, updated.PausedReason)
			return collab.DispatchIntent{}, false
		}
		tr, st := s.intentTurnFact(it)
		switch st {
		case turnFactMatched:
			s.attachIntent(spaceID, it, tr.TaskID) // 崩溃窗：补接单登记（本棒已消耗）
			return collab.DispatchIntent{}, false
		case turnFactConflict:
			s.cancelIntent(spaceID, it, "轮号冲突（intentTurnFact："+it.RequestID+" 预留 seq "+itoa64(it.TurnSeq)+" 与在案轮行/任务来源标记不符）：取消收束")
			return collab.DispatchIntent{}, false
		case turnFactMissing:
			log.Printf("[hub] 空间意图轮证据缺失（任务已清理），留待核对 space=%s intent=%s", spaceID, it.RequestID)
			return collab.DispatchIntent{}, false
		}
		return it, true // none：候选（bind + 提交）
	}
	return collab.DispatchIntent{}, false
}

// itoa64 int64 十进制（核验注记拼装）。
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	uv := uint64(v)
	if neg {
		uv = uint64(-uv)
	}
	var b [24]byte
	i := len(b)
	for uv > 0 {
		i--
		b[i] = byte('0' + uv%10)
		uv /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// strconvFormatUint uint64 十进制（日志/原因文案拼装；避免引 strconv 单用途）。
func strconvFormatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// ---------- 询价辅助 ----------

// intentSeqHighWater 本会话全部在途意图的最大已声称轮号（受理期预留基准成分：
// pending+accepted 都计入——占用的 seq 不能被新受理复用；与 node
// spaceIntentSeqHighWater 同口径）。会话未绑定空间返回 0。
func (s *Service) intentSeqHighWater(sessionID string) int64 {
	spaceID, ok := s.SessionOwnerSpace(sessionID)
	if !ok {
		return 0
	}
	var hw int64
	for _, it := range s.Intents(spaceID) {
		if it.SessionID == sessionID && it.Status.InFlight() && it.TurnSeq > hw {
			hw = it.TurnSeq
		}
	}
	return hw
}

// nextTurnSeqOf 会话视图的下一轮序号（max+1；与 core session 同口径——close 不
// 占号故 Seq 连续）。
func nextTurnSeqOf(sv *core.SessionView) int64 {
	var max int64
	for _, t := range sv.Turns {
		if t.Seq > max {
			max = t.Seq
		}
	}
	return max + 1
}
