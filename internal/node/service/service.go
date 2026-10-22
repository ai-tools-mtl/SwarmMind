// Package service 实现 node 侧任务调度装配（SPEC 4.3/4.5/4.7/4.8）：订阅 conn
// 入站帧，按 SPEC 4.7 关键顺序处理 task_offer——accepted 落盘成功 → 发 task_ack；
// started 落盘成功 → 发 task_started → spawn；finished+result 落盘成功 → 发
// task_result。重复 offer 不再执行，直接重发 ack（已 finished 附带重发 result，
// SPEC 4.3）；账本未确认结果随重连成功重发（SPEC 4.5）；result_conflict 置账本
// closed 停止重传；配置漂移（agent_not_found）直接回 failed 不进重推循环。
//
// 执行并发由信号量限制（node 执行并发，默认 2，可配）；任务执行 context 独立于
// 会话（重连不断任务），但随服务关闭取消——关闭打断在途执行时整组终止子进程，
// 以 needs_review/node_restarted_midway 收尾并回传（SPEC 4.8），绝不以
// completed 落盘。管理面取消（task_cancel）同理以执行 ctx 中断在途任务，以
// cancelled/user_cancelled 收尾（cancel.go）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mtl802/swarmmind/internal/node/agentid"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// Runner 单任务执行器（*runner.Runner 的最小接口；测试可注入桩执行器）。
// ctx 为任务中止开关：取消即由执行器整组终止子进程（服务关闭打断在途执行）。
type Runner interface {
	Run(ctx context.Context, task protocol.Task) protocol.TaskResult
}

// Config Service 构造参数。
type Config struct {
	// Ledger node 执行账本。
	Ledger *ledger.Ledger
	// Runners agent 名 → 执行器（ExecProfile 装配；不在表内即配置漂移）。
	Runners map[string]Runner
	// ArtifactRoots agent 名 → workdir 绝对路径（产物取回的路径解析基准，
	// Loop 4；与 Runners 同源装配）。P1.5 起兼作 workspace 互斥键派生源：
	// 启动时经 agentid 规范化派生各 agent 的 workspace_id。
	ArtifactRoots map[string]string
	// Concurrency 执行并发上限；0 = protocol.DefaultConcurrency。
	Concurrency int
}

// Service node 任务服务：transport.Transport 的订阅者与发送方。
type Service struct {
	cfg  Config
	tr   transport.Transport // node 端点（WSS Client 或 membus Endpoint）
	sem  chan struct{}
	wg   sync.WaitGroup
	ctx  context.Context
	stop context.CancelFunc

	resendKick chan struct{} // 容量 1：补传触发（注册成功回调/周期），并发合并（C3）

	// wsByAgent agent 名 → workspace 互斥键（P1.5，ArtifactRoots 派生；不在表内
	// 的 agent 无互斥语义）；wsLocks 执行侧互斥表（node 侧为准，见 workdir.go）。
	wsByAgent map[string]string
	wsLocks   wsMutex

	// 在途任务取消表（task_cancel 生命周期，cancel.go）：task_id → 取消开关。
	// execute 登记goroutine 注销；handleTaskCancel 依此触发执行中断。
	muCancels sync.Mutex
	cancels   map[string]*cancelEntry

	// runners/roots 执行器与产物根表（初值 = cfg.Runners/ArtifactRoots；热重载
	// SwapExecutors 整表替换，muExec 与执行期读取互斥——方案 3.5 网页编辑）。
	muExec  sync.RWMutex
	runners map[string]Runner
	roots   map[string]string
}

// New 构造服务并订阅入站帧；启动补传 worker 与周期兜底循环；从账本重建 workdir
// 互斥表（P1.5/SPEC 4.10：accepted/started 未终态条目占锁——须在 Reschedule 交回
// 执行之前完成，恢复任务与新增任务同表串行）。用完须 Close。
func New(cfg Config, tr transport.Transport) *Service {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = protocol.DefaultConcurrency
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{
		cfg:        cfg,
		tr:         tr,
		sem:        make(chan struct{}, cfg.Concurrency),
		ctx:        ctx,
		stop:       stop,
		resendKick: make(chan struct{}, 1),
		cancels:    make(map[string]*cancelEntry),
		wsByAgent:  make(map[string]string, len(cfg.ArtifactRoots)),
		wsLocks:    newWsMutex(),
		runners:    cfg.Runners,
		roots:      cfg.ArtifactRoots,
	}
	for agent, root := range cfg.ArtifactRoots {
		s.wsByAgent[agent] = agentid.WorkspaceID(root)
	}
	// 互斥表账本重建（P1.5/SPEC 4.10）：accepted/started 未终态条目按 (created_at,
	// id) 确定序 seed——同 workspace 多条目时最早任务成为持有者，其余恢复任务
	// 在 Reschedule 同序交回后 FIFO 排队，恢复执行序确定（账本 map 遍历无序，
	// 任取持有者会破坏确定序，P1.5 二轮必修2）。
	var reseed []ledger.Entry
	for _, e := range cfg.Ledger.View() {
		if e.State != ledger.StateAccepted && e.State != ledger.StateStarted {
			continue
		}
		reseed = append(reseed, e)
	}
	sort.Slice(reseed, func(i, j int) bool {
		a, b := reseed[i].Task, reseed[j].Task
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	for _, e := range reseed {
		if ws := s.wsByAgent[e.Task.TargetAgent]; ws != "" {
			s.wsLocks.seed(ws, e.Task.ID) // 同键多条目时首个（最早）占锁
		}
	}
	tr.Subscribe(s.HandleFrame)
	s.wg.Add(2)
	go s.resendWorker()
	go s.resendLoop()
	return s
}

// Close 停止服务（SPEC 4.8 优雅关闭收尾）：取消服务 ctx——在途执行的任务 ctx
// 随之取消，执行器整组终止子进程（kill(-pgid)），随后以 needs_review 收尾落盘；
// 等待全部在途执行与补传结束后返回。任务绝不以「自然跑完的 completed」收尾。
// 账本关闭归装配方。幂等。
func (s *Service) Close() {
	s.stop()
	s.wg.Wait()
}

// HandleFrame 入站帧处理（conn.Subscribe 的回调；可重入——处理中可经 Send 回发）。
func (s *Service) HandleFrame(env *protocol.Envelope) {
	if env == nil {
		return
	}
	switch env.Type {
	case protocol.MsgTaskOffer:
		s.handleOffer(env)
	case protocol.MsgTaskCancel:
		s.handleTaskCancel(env)
	case protocol.MsgResultAck:
		s.handleResultAck(env)
	case protocol.MsgErr:
		s.handleErr(env)
	case protocol.MsgSubmitAck, protocol.MsgTaskStatus:
		// 双路径后 node 不再发起 task_submit/task_query（本地直执行 + 账本视图
		// 直出，方案 3.2）：应答帧只可能来自旧版对端或异常，记录忽略
		log.Printf("[node] 忽略帧 type=%s（本地直执行路径不产生此应答）", env.Type)
	case protocol.MsgArtifactFetch:
		s.handleArtifactFetch(env)
	case protocol.MsgResultEvent, protocol.MsgRegisterAck, protocol.MsgArtifactData:
		// result_event：提交方终态推送（本地查询走 task_query 补查，已知限制：
		// 在线错过即弃）；register_ack 由 conn 握手内联消费；artifact_data 仅
		// 应 hub 侧在途取回（node 不发起取回）
		log.Printf("[node] 记录帧 type=%s", env.Type)
	default:
		log.Printf("[node] 忽略帧 type=%s", env.Type)
	}
}

// handleOffer 任务投递入口（SPEC 4.3/4.7）：重复 offer 幂等；未知 agent 直达
// failed 终态（不进重推循环）；正常路径 accepted 落盘 → ack → 异步执行。
// 取消墓碑命中（task_cancel 先于本 offer 到达，cancel.go）→ 拒绝执行，直接以
// cancelled/user_cancelled 落终态回传（2026-10-22 修复轮2）。
func (s *Service) handleOffer(env *protocol.Envelope) {
	var task protocol.Task
	if err := json.Unmarshal(env.Payload, &task); err != nil || task.ID == "" {
		log.Printf("[node] task_offer 载荷解析失败: %v", err)
		return
	}
	if s.cfg.Ledger.HasCancelTombstone(task.ID) {
		s.finishCancelledOffer(task)
		return
	}
	err := s.cfg.Ledger.Accept(task)
	if errors.Is(err, ledger.ErrDuplicate) {
		// 重复 offer：不再执行，直接重发 task_ack；已 finished 附带重发 result（SPEC 4.3）
		log.Printf("[node] 重复 offer，重发确认 task=%s", task.ID)
		s.sendRef(protocol.MsgTaskAck, task.ID)
		if e, ok := s.cfg.Ledger.Get(task.ID); ok && e.State == ledger.StateFinished && e.Result != nil {
			s.sendResult(task.ID, *e.Result)
		}
		return
	}
	if err != nil {
		// 账本写入失败（磁盘等）：不 ack，hub 30s 回退重推（SPEC 4.5）
		log.Printf("[node] 账本入账失败 task=%s: %v", task.ID, err)
		return
	}
	s.sendRef(protocol.MsgTaskAck, task.ID) // accepted 落盘成功后才发（SPEC 4.7）
	s.execute(task)
}

// execute 异步执行单任务（并发受信号量限制）：started 落盘 → 发 task_started →
// spawn；finished+result 落盘 → 发 task_result。任务 ctx 独立于会话（重连不断
// 任务，SPEC 五）但随服务关闭（Close→stop）取消——取消即整组终止子进程，并按
// SPEC 4.8 以 needs_review 收尾，绝不把打断后的结果当 completed 落盘。
// 管理面取消（task_cancel，cancel.go）同样以执行 ctx 中断：整组终止后账本以
// cancelled/user_cancelled 收尾并回传（与关闭打断的判定见执行结果分类处）。
//
// P1.5 workdir 互斥（SPEC 4.10，node 侧为准）：互斥登记在调用方 goroutine 内
// 同步完成（reserve：空闲/自持即取得，他持则入 FIFO 队尾——登记序即授予序，
// offer 路径为到达序、恢复路径为 created_at 序）；授予等待在执行 goroutine 内
// 进行，且并发槽在授予之后才取——「先锁后槽」，等待者不占槽，杜绝 Concurrency
// 受限时「等待者占槽 + 持有者等槽」互等死锁（P1.5 二轮必修2）。锁持有至
// finished+result 落盘，同 workspace 任务严格串行。锁等待随服务关闭取消：未获
// 授予即放弃（withdraw 摘队），条目保持 accepted 由重启恢复再调度；随管理面
// 取消则账本落 cancelled 终态（不再恢复执行）。授予与取消/关闭竞态时按持有
// 走下方检查退出（defer 释放，无执行副作用）。
func (s *Service) execute(task protocol.Task) {
	ws := s.wsByAgent[task.TargetAgent]
	var wake <-chan struct{} // 非 nil：已登记排队，release 授予时关闭
	if ws != "" {
		wake = s.wsLocks.reserve(ws, task.ID) // nil=已持有（含自持重投）
	}
	ce := s.regCancel(task.ID) // task_cancel 入口（cancel.go）；goroutine 退出注销
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.unregCancel(task.ID)
		if wake != nil {
			select {
			case <-wake: // release 先置 held=本任务再 close，唤醒即持有
			case <-s.ctx.Done():
				if s.wsLocks.withdraw(ws, task.ID, wake) {
					return // 未获授予即放弃：条目保持 accepted，重启恢复再调度
				}
				// 授予与关闭竞态：持有已生效，按持有走下方关闭检查退出
			case <-ce.doneCh():
				if s.wsLocks.withdraw(ws, task.ID, wake) {
					s.finishCancelledBeforeRun(task.ID) // 管理面取消：不再执行，落终态回传
					return
				}
				// 授予与取消竞态：持有已生效，按持有走下方取消检查退出
			}
		}
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			if ws != "" {
				s.wsLocks.release(ws, task.ID)
			}
			return
		case <-ce.doneCh():
			if ws != "" {
				s.wsLocks.release(ws, task.ID)
			}
			s.finishCancelledBeforeRun(task.ID)
			return
		}
		defer func() { <-s.sem }()
		if ws != "" {
			defer s.wsLocks.release(ws, task.ID)
		}

		// runCtx：任务执行期中止开关——独立于会话，随服务关闭或管理面取消。
		runCtx, cancelRun := context.WithCancel(s.ctx)
		defer cancelRun()
		ce.bind(cancelRun) // 此前已触发取消则立即生效
		// 关闭/取消竞态（select 双就绪随机）：关闭 → 不再 started，条目保持
		// accepted，重启恢复进 Reschedule（SPEC 4.8）；管理面取消 → 落 cancelled
		// 终态（账本先到为准，与取消路径竞争失败则幂等收敛）。
		if runCtx.Err() != nil {
			if ce.triggered() {
				s.finishCancelledBeforeRun(task.ID)
			}
			return
		}

		// N4：取得槽位后、started/spawn 前重检过期——排队等待可能跨过截止时间
		//（SPEC 4.3：到期非终态 → failed/expired），不得再产生执行副作用。
		if isExpired(task, protocol.NowUTC()) {
			log.Printf("[node] 任务已过期（排队跨过截止时间），不执行 task=%s", task.ID)
			res := protocol.TaskResult{
				Status:     protocol.ResultFailed,
				ExitCode:   protocol.ExitCodeNotRun,
				Error:      protocol.CodeExpired,
				FinishedAt: protocol.NowUTC(),
			}
			if err := s.cfg.Ledger.MarkFinished(task.ID, res); err != nil {
				log.Printf("[node] 写入过期终态失败 task=%s: %v", task.ID, err)
				return
			}
			s.sendResult(task.ID, res)
			return
		}

		if err := s.cfg.Ledger.MarkStarted(task.ID); err != nil {
			// 已 finished（重复执行竞争/取消路径先落）/不存在：账本为准，结果由重发链路闭合
			log.Printf("[node] 标记 started 失败 task=%s: %v", task.ID, err)
			return
		}
		s.sendRef(protocol.MsgTaskStarted, task.ID) // started 落盘成功后、spawn 前发（SPEC 4.7）

		res := s.runTask(runCtx, task)
		// 执行结果分类：服务关闭打断了在途执行（子进程已被整组终止）——执行结果
		// 不确定，写 needs_review/node_restarted_midway 收尾并回传；管理面取消同样
		// 整组终止，写 cancelled/user_cancelled（仅当进程确被终止或未运行——进程已
		// 自然退出的保留自然终态，由账本先到为准与 hub 收敛表兜底）。禁止把打断后
		// 的结果当 completed 落盘（P2 实测缺陷根因）。
		if runCtx.Err() != nil {
			switch {
			case ce.triggered():
				if res.ExitCode == protocol.ExitCodeNotRun {
					res = cancelledResult(res)
				}
			default:
				res = interruptedResult(res)
			}
		}
		// N2：按最终编码实测计量并截断，得到最终可发送的同一结果后持久化
		//（重传内容稳定，SPEC 4.7/4.9）
		final, err := s.fitResultEnvelope(task.ID, res)
		if err != nil {
			// 无可截断字段仍超限：不落终态、不发送——账本保持 started，重启恢复
			// 走 needs_review 链路收敛；绝不把必然被拒收的超限结果写入重传链路
			log.Printf("[node] %v", err)
			return
		}
		// SPEC 4.7 顺序：finished+result 落盘成功后才发 task_result
		if err := s.cfg.Ledger.MarkFinished(task.ID, final); err != nil {
			log.Printf("[node] 写入终态失败 task=%s: %v", task.ID, err)
			return
		}
		s.sendResult(task.ID, final)
	}()
}

// interruptedResult 服务关闭打断在途执行的收尾结果（SPEC 4.8）：status 置
// needs_review（独立终态，不得折叠为 failed），error=node_restarted_midway；
// ExitCode 与已采集的 stdout/stderr tail、产物清单原样保留，供人工核对。
func interruptedResult(res protocol.TaskResult) protocol.TaskResult {
	res.Status = protocol.ResultNeedsReview
	res.Error = protocol.CodeNodeRestartedMidway
	return res
}

// isExpired 任务过期判定（ExpireAt 零值视为不过期——恢复与测试路径允许无期限任务）。
func isExpired(t protocol.Task, now time.Time) bool {
	return !t.ExpireAt.IsZero() && t.ExpireAt.Before(now)
}

// runTask 选择执行器并执行；未知 agent 即配置漂移（SPEC 4.3）：failed/
// agent_not_found，不进重推循环（结果已入账本，重复 offer 走重发分支）。
func (s *Service) runTask(ctx context.Context, task protocol.Task) protocol.TaskResult {
	r, ok := s.runnerFor(task.TargetAgent)
	if !ok {
		return protocol.TaskResult{
			Status:     protocol.ResultFailed,
			ExitCode:   protocol.ExitCodeNotRun,
			Error:      protocol.CodeAgentNotFound,
			FinishedAt: protocol.NowUTC(),
		}
	}
	return r.Run(ctx, task)
}

// runnerFor 取 agent 执行器（热重载换表的读侧；不在表内 ok=false）。
func (s *Service) runnerFor(agent string) (Runner, bool) {
	s.muExec.RLock()
	defer s.muExec.RUnlock()
	r, ok := s.runners[agent]
	return r, ok
}

// artifactRootFor 取 agent 产物根（同 runnerFor）。
func (s *Service) artifactRootFor(agent string) (string, bool) {
	s.muExec.RLock()
	defer s.muExec.RUnlock()
	root, ok := s.roots[agent]
	return root, ok
}

// SwapExecutors 热重载执行器表（方案 3.5 网页编辑）：整体替换 Runners 与
// ArtifactRoots，与执行期读取（runnerFor/artifactRootFor）互斥。在途任务已持有
// 旧 runner 引用，跑完不受影响；新任务取新表。wsByAgent（workspace 互斥键）保持
// 启动值不重建——重建需迁移在途持锁，收益不抵复杂度；workdir 热变更后互斥仍按
// 旧键保守串行（同 agent 任务依旧串行），重启后收敛新键。
func (s *Service) SwapExecutors(runners map[string]Runner, roots map[string]string) {
	s.muExec.Lock()
	defer s.muExec.Unlock()
	s.runners = runners
	s.roots = roots
}

// handleResultAck 终态已持久化（SPEC 4.5）：账本置 result_acked，重发停止。
func (s *Service) handleResultAck(env *protocol.Envelope) {
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[node] result_ack 载荷解析失败: %v", err)
		return
	}
	if err := s.cfg.Ledger.MarkResultAcked(ref.TaskID); err != nil {
		log.Printf("[node] result_ack 记账失败 task=%s: %v", ref.TaskID, err)
	}
}

// handleErr 协议错误帧：result_conflict（终态冲突保留先到）或 expired 拒收 →
// 账本置 closed 停止重传，记录保留作冲突审计（SPEC 4.5）。
func (s *Service) handleErr(env *protocol.Envelope) {
	if env.Err == nil || env.Err.Code != protocol.CodeResultConflict {
		log.Printf("[node] 收到 err 帧 code=%s msg=%s", errCode(env), envErrMsg(env))
		return
	}
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[node] result_conflict 帧缺 task 引用: %v", err)
		return
	}
	if err := s.cfg.Ledger.MarkResendClosed(ref.TaskID); err != nil {
		log.Printf("[node] 置 closed 失败 task=%s: %v", ref.TaskID, err)
		return
	}
	log.Printf("[node] 结果被拒收（result_conflict），停止重传 task=%s", ref.TaskID)
}

// Execute 本地调度入口（启动恢复 Reschedule 交回执行，SPEC 4.8；无需再 Accept，
// 条目已在账本 accepted 态）。任务执行 context 独立于会话（SPEC 五）。
func (s *Service) Execute(task protocol.Task) {
	s.execute(task)
}

// ResendPending 立即重发账本全部未确认结果（同步，测试与 worker 内部用）。
// origin 过滤（方案 3.2，zcode 审查补强）：只有 hub 下发（origin=hub）的结果
// 才回传 hub——standalone 期本地任务的结果帧在切换组网后不得成批推 hub
//（local 来源条目落终态即自确认，正常不在队列；此处为双保险并免日志噪音）。
func (s *Service) ResendPending() {
	for _, e := range s.cfg.Ledger.PendingResend() {
		if e.Origin != ledger.OriginHub {
			continue
		}
		log.Printf("[node] 重发未确认结果 task=%s", e.Task.ID)
		s.sendResult(e.Task.ID, *e.Result)
	}
}

// ResendPendingAsync 触发一次补传（C3/SPEC 4.5：每次重连成功后立即重发）：
// 非阻塞、可重入、并发合并——同时至多一个补传在跑（与周期补传共用同一 worker，
// 并发有界），不长时间阻塞注册回调所在的连接生命周期。
func (s *Service) ResendPendingAsync() {
	select {
	case s.resendKick <- struct{}{}:
	default:
	}
}

// resendWorker 补传 worker：串行处理补传触发（合并重复触发），直至服务停止。
func (s *Service) resendWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.resendKick:
			s.ResendPending()
		}
	}
}

// resendInterval 未确认结果重发周期（SPEC 4.5：每次重连成功后立即重发，此后每
// 60s 一次，直至 result_ack / 拒收置 closed）。
const resendInterval = 60 * time.Second

// resendLoop 重发兜底节拍，直至收到 result_ack / 拒收置 closed / 服务停止。
// 触发经 resendKick 与注册回调合并（并发有界，C3）。
func (s *Service) resendLoop() {
	defer s.wg.Done()
	tk := time.NewTicker(resendInterval)
	defer tk.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tk.C:
			s.ResendPendingAsync()
		}
	}
}

// originOf 任务来源（快照读零拷贝；不在账本返回 ""，按非 local 处理——hub 链路
// 的迟到帧仍走回传语义）。
func (s *Service) originOf(taskID string) ledger.Origin {
	return s.cfg.Ledger.OriginOf(taskID)
}

// sendRef 发送 {task_id} 引用帧（task_ack/task_started）。origin 堵点（方案 3.2）：
// local 任务不向 hub 发任何帧——hub 不认识本机自派任务，帧只会换来 task_not_found
// 噪音；本机任务的过程可见性由账本视图直出（ledger.TaskView）。
func (s *Service) sendRef(typ, taskID string) {
	if s.originOf(taskID) == ledger.OriginLocal {
		return
	}
	s.send(typ, protocol.TaskRef{TaskID: taskID})
}

// sendResult 发送 task_result 帧（SPEC 4.2 载荷）。origin 堵点同 sendRef：
// local 任务的结果只回写本地账本，绝不推 hub（含取消/中断收尾等所有路径）。
func (s *Service) sendResult(taskID string, res protocol.TaskResult) {
	if s.originOf(taskID) == ledger.OriginLocal {
		return
	}
	s.send(protocol.MsgTaskResult, protocol.TaskResultPayload{
		TaskID: taskID, Status: res.Status, Result: &res,
	})
}

// send 经 node 端点发帧；未连接（transport.ErrNoEndpoint）静默——ack/started
// 丢失由 hub 回退重推自愈，task_result 丢失由重发链路闭合（SPEC 4.5）。
func (s *Service) send(typ string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[node] 载荷编码失败 type=%s: %v", typ, err)
		return
	}
	env := &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      typ,
		MessageID: protocol.NewMessageID(),
		To:        "hub",
		Payload:   b,
	}
	if err := s.tr.Send("hub", env); err != nil && !isBenignSendErr(err) {
		log.Printf("[node] 帧发送失败 type=%s: %v", typ, err)
	}
}

// isBenignSendErr 对端不存在/本端已关闭的发送失败：链路自愈语义覆盖（SPEC 4.5）。
func isBenignSendErr(err error) bool {
	return errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed)
}

// sendWithID 以指定 mid 发帧（帧关联键；card_update 上报用）。
func (s *Service) sendWithID(typ, mid string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("载荷编码失败 type=%s: %w", typ, err)
	}
	return s.tr.Send("hub", &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      typ,
		MessageID: mid,
		To:        "hub",
		Payload:   b,
	})
}

// trEndpointIdentity 端点本端身份（计量用；membus/WSS 均经 Send 覆盖 From，
// 这里尽力取已知值——取不到时用保守占位长度）。
func (s *Service) trEndpointIdentity() string {
	type identifier interface{ EndpointIdentity() string }
	if id, ok := s.tr.(identifier); ok {
		return id.EndpointIdentity()
	}
	return ""
}

// ---------- N2：task_result 最终编码预算（SPEC 4.9「最终编码实测、超限截断并标 truncated」） ----------

// resultEnvelopeSizeBudget 计量预算：完整信封 ≤1MiB（SPEC 4.6），为发送期元数据
// 波动（epoch 位数、mid/ReplyTo 长度等）留 4KiB 余量——From 已按真实 node_id
// 计入计量，不再依赖余量覆盖。
const resultEnvelopeSizeBudget = protocol.MaxMessageBytes - 4096

// fitResultEnvelope 构造最终可发送的 task_result：以真实发送元数据（含真实 From
// =本端身份）编码实测完整信封体积，超预算时按 UTF-8 边界以优先级依次缩减
// stderr_tail → stdout_tail → final_answer（诊断 tail 优先牺牲），并注记 truncated。
// 返回的终态结果将被持久化，重传路径（PendingResend/重复 offer）从账本取同一结果，
// 内容稳定。三个可截断字段全部耗尽仍超限（如 task_id/err 体量异常）：返回明确
// 错误，调用方不落终态、不发送（N2：绝不发出必然超限被拒的帧）。
func (s *Service) fitResultEnvelope(taskID string, res protocol.TaskResult) (protocol.TaskResult, error) {
	r := res
	from := s.trEndpointIdentity()
	truncated := false
	for resultEnvelopeLen(taskID, from, r) > resultEnvelopeSizeBudget {
		cut := pickTruncatable(&r)
		if cut == nil {
			return res, fmt.Errorf("task_result 编码超限且无可截断字段（task_id=%s 长度=%d，不发送）",
				taskID, len(taskID))
		}
		*cut = cutUTF8(*cut, len(*cut)/2)
		truncated = true
	}
	if truncated {
		r.Error = appendNote(r.Error, protocol.CodeTruncated)
	}
	return r, nil
}

// pickTruncatable 按优先级取首个非空可截断字段：stderr_tail → stdout_tail →
// final_answer（诊断 tail 优先牺牲，SPEC 4.9 缩减次序）。全空返回 nil。
func pickTruncatable(r *protocol.TaskResult) *string {
	for _, p := range []*string{&r.StderrTail, &r.StdoutTail, &r.FinalAnswer} {
		if len(*p) > 0 {
			return p
		}
	}
	return nil
}

// resultEnvelopeLen 按真实发送路径编码实测完整信封体积（Service.send 的同型
// 构造 + 传输层盖章字段；From 计入真实本端身份，N2）。
func resultEnvelopeLen(taskID, from string, res protocol.TaskResult) int {
	b, err := json.Marshal(protocol.TaskResultPayload{TaskID: taskID, Status: res.Status, Result: &res})
	if err != nil {
		return math.MaxInt // 不可编码视为无穷大，触发截断
	}
	env := &protocol.Envelope{
		Version:      protocol.ProtocolVersion,
		Type:         protocol.MsgTaskResult,
		MessageID:    protocol.NewMessageID(), // 32 字节 hex，与真实发送同长
		From:         from,                    // 真实发送身份（传输层盖章值）计入计量
		To:           "hub",
		SessionEpoch: 1 << 40, // 保守：代次位数上限量级
		Payload:      b,
	}
	eb, err := json.Marshal(env)
	if err != nil {
		return math.MaxInt
	}
	return len(eb)
}

// cutUTF8 按 UTF-8 边界截断字符串至 ≤limit 字节（截断点回落到完整 rune）。
func cutUTF8(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		if limit <= 0 {
			return ""
		}
		return s
	}
	s = s[:limit]
	for len(s) > 0 {
		rn, size := utf8.DecodeLastRuneInString(s)
		if !(rn == utf8.RuneError && size == 1) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// appendNote 向 Error 追加注记（"; " 连接，主错误码保持首位供 contains 断言）。
func appendNote(err, note string) string {
	if err == "" {
		return note
	}
	return err + "; " + note
}

// SendCardUpdate 上报能力表全量更新（card_update，SPEC 4.2；Loop 7 exposed 开关
// 变更即时上报 hub）。离线（transport.ErrNoEndpoint）原样返回——调用方按「待同步」
// 呈现；重连注册携全量能力表，状态自动收敛，无需补发。
func (s *Service) SendCardUpdate(cards []protocol.AgentCard) error {
	if cards == nil {
		cards = []protocol.AgentCard{}
	}
	return s.sendWithID(protocol.MsgCardUpdate, protocol.NewMessageID(), protocol.CardUpdatePayload{Agents: cards})
}

// CardsFromProfiles 由 ExecProfile 生成 AgentCard（SPEC 4.9 双卡分离：对外能力
// 声明，绝不包含本机执行配置）。P1.5 起携带 WorkspaceID（SPEC 4.10：workdir
// 规范化派生的互斥键，node 上报、hub 不感知路径；EvalSymlinks 失败回退
// Clean+Abs 时由 agentid 注记并在此记日志）。
func CardsFromProfiles(profiles []protocol.ExecProfile) []protocol.AgentCard {
	cards := make([]protocol.AgentCard, 0, len(profiles))
	for _, p := range profiles {
		canonical, fallback := agentid.Resolve(p.Workdir)
		if fallback {
			log.Printf("[node] agent %q workdir 规范化回退（EvalSymlinks 失败，多为目录不存在）: %s → %s",
				p.Name, p.Workdir, canonical)
		}
		cards = append(cards, protocol.AgentCard{
			Name:           p.Name,
			Kind:           p.Kind,
			Description:    p.Description,
			InputMode:      "prompt_text",
			OutputMode:     "answer+artifacts",
			Interactive:    false, // V0 恒为 false（SPEC 4.9）
			PermissionMode: p.PermissionMode,
			ProtocolVer:    1,
			WorkspaceID:    agentid.WorkspaceID(p.Workdir),
			Exposed:        p.Exposed,
		})
	}
	return cards
}

// errCode / envErrMsg 安全取错误帧字段。
func errCode(env *protocol.Envelope) string {
	if env.Err == nil {
		return ""
	}
	return env.Err.Code
}

func envErrMsg(env *protocol.Envelope) string {
	if env.Err == nil {
		return ""
	}
	return env.Err.Msg
}
