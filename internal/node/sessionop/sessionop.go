// Package sessionop 会话操作服务（W01-02，docs/tasks/v3/W01-02-rfc.md）：在
// sessionx 内核与 node ledger 之上编排会话受理与生命周期操作——创建请求 ID 持久
// 登记、逐会话有界串行队列、取消原语与竞态收敛、关闭在途防护、工作区授权与真实
// 路径互斥键派生、重启不确定收敛。
//
// 不动点（W01-01 基准 §4）：sessionx 内核四原语、写者门、墓碑、绑定守卫语义零
// 改动；本包只做受理层编排与账本记账。HTTP/MCP 接线属 W01-03。
//
// 取消收敛定案（RFC §3）：取消意图登记先于触发；取消终态一律由执行 goroutine 依
// 自身位置与证据记账（单写者），CancelTurn 只登记意图、触发执行 ctx 取消、等待
// 收敛后按账本终态如实出回执——排队取消确定性终止（未 spawn，序号消耗），执行取
// 消以 runner 治理的整组终止证据记账，证据缺失落待核对（turn_cancel_outcome_
// unknown），绝不虚报已停止；对已自然终态轮先到保留（已完成不可取消）。
package sessionop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// DefaultQueueLimit 逐会话非终态受理轮默认上限（W01-01 基准 §4：第 17 条拒绝）。
const DefaultQueueLimit = 16

// 错误码（W01-01 基准 §4 命名；HTTP 状态码投影归 W01-03 接线）。
const (
	CodeQueueFull            = "turn_queue_full"
	CodeCloseInFlight        = "close_in_flight"
	CodeRequestConflict      = "request_conflict"
	CodeWorkspaceDenied      = "workspace_denied"
	CodeCancelQueued         = "turn_cancelled_queued"
	CodeCancelExecuted       = "turn_cancelled_executed"
	CodeCancelOutcomeUnknown = "turn_cancel_outcome_unknown"
	CodeTurnAlreadyFinished  = "turn_already_finished"
	CodeTurnNotFound         = "turn_not_found"
	CodeCancelUnconfirmed    = "cancel_unconfirmed"
)

// 取消回执 outcome 取值。
const (
	OutcomeQueued   = "queued"   // 排队取消成立（未 spawn，确定性终止）
	OutcomeExecuted = "executed" // 执行取消成立（进程组已证终止并回收）
	OutcomeUnknown  = "unknown"  // 取消触发但停止事实未证实（账本落 failed 待核对）
)

// 显式拒绝面（errors.Is 判定）。
var (
	// ErrQueueFull 会话非终态受理轮已达上限（受理前拒绝，零副作用）。
	ErrQueueFull = errors.New("sessionop: 会话非终态受理轮已达队列上限")
	// ErrCloseInFlight 存在非终态受理轮，拒绝关闭（先取消或等待收束）。
	ErrCloseInFlight = errors.New("sessionop: 存在非终态受理轮，拒绝关闭")
	// ErrRequestConflict 同 request_id 已被不同内容占用（同键异文）。
	ErrRequestConflict = errors.New("sessionop: request_id 已被不同内容占用")
	// ErrWorkspaceDenied 会话工作目录（归一后）越出允许根。
	ErrWorkspaceDenied = errors.New("sessionop: 会话工作目录越出允许的工作区根")
	// ErrTurnFinished 轮已终态且非取消类：先到保留，已完成不可取消。
	ErrTurnFinished = errors.New("sessionop: 轮已终态，不可取消（先到保留）")
	// ErrTurnUnknown 无该轮受理记录（高水位之下或从未受理）。
	ErrTurnUnknown = errors.New("sessionop: 无该轮受理记录")
	// ErrCancelUnconfirmed 取消已触发但终态未确认落盘：如实拒绝出回执，绝不虚报。
	ErrCancelUnconfirmed = errors.New("sessionop: 取消已登记但终态未确认落盘")
)

// Logger 运维日志接口（*log.Logger 直接兼容）。
type Logger interface {
	Printf(format string, args ...any)
}

// nopLogger 未装配日志时的静默兜底。
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// Options 装配项。
type Options struct {
	// QueueLimit 逐会话非终态受理轮上限；0 = DefaultQueueLimit；负值装配拒绝。
	QueueLimit int
	// AllowedWorkspaceRoots 允许的会话工作区根（绝对路径；空 = 放行保持现状）。
	// 判定前与 cwd 一并经 resolveWorkspace 归一为真实路径（根与 cwd 同一函数同一
	// 策略，codex 终审必修项）。
	AllowedWorkspaceRoots []string
	// Now 时钟源（测试注入）；nil = protocol.NowUTC（全工程 UTC 基准）。
	Now func() time.Time
	// Log 运维日志；nil = 静默。
	Log Logger
	// ConfirmBudget CancelTurn 收敛后等待账本终态落盘的确认预算（含落盘重试窗
	// 口与在途句柄登记窗口）；0 = 2s。仅影响回执等待时长，不影响取消本身的执行
	//（测试可缩短以加速诚实失败路径验证）。
	ConfirmBudget time.Duration
}

// Service 会话操作服务：受理守卫（队列/工作区）、取消句柄登记与收敛、创建请求
// ID 持久登记、重启不确定收敛。由 node service 装配注入（Config.SessionOps）。
type Service struct {
	led        *ledger.Ledger
	queueLimit int
	roots      []string // 归一后的允许根（resolveWorkspace 产物，与 cwd 同一策略）
	now        func() time.Time
	log        Logger
	confirm    time.Duration

	// mu 守护 turns 句柄表；锁序恒 s.mu → turnHandle.mu，绝不反向。
	mu    sync.Mutex
	turns map[string]map[int64]*turnHandle

	// 请求登记（requests.go）
	reqMu    sync.Mutex
	reqPath  string
	reqs     map[string]RequestRecord
	reqLog   Logger
	reqInject func(op string) error // 落盘故障注入（仅包内测试；生产恒 nil）
}

// turnHandle 单轮取消句柄：受理时登记（先于任何取消可达时刻），执行 goroutine
// 绑定取消开关并在收口时收敛。requested 置位后不可撤销（取消是管理面意图）。
type turnHandle struct {
	taskID string
	mu     sync.Mutex
	cancel func() // 执行取消开关（service cancelEntry.trigger）；未绑定前为 nil

	requested     bool // 取消意图已登记（置位后不可撤销——取消是管理面意图）
	done          chan struct{}
	convergedFlag bool // done 是否已关闭（防重复 close）
}

// New 装配会话操作服务。dataDir 供请求登记持久化；led 为 node 执行账本
//（队列计数/取消回执/重启收敛的真相源）。
func New(dataDir string, led *ledger.Ledger, opt Options) (*Service, error) {
	if dataDir == "" {
		return nil, errors.New("sessionop: dataDir 为空")
	}
	if led == nil {
		return nil, errors.New("sessionop: ledger 未装配")
	}
	if opt.QueueLimit < 0 {
		return nil, fmt.Errorf("sessionop: QueueLimit 为负: %d", opt.QueueLimit)
	}
	limit := opt.QueueLimit
	if limit == 0 {
		limit = DefaultQueueLimit
	}
	now := opt.Now
	if now == nil {
		now = protocol.NowUTC
	}
	log := opt.Log
	if log == nil {
		log = nopLogger{}
	}
	confirm := opt.ConfirmBudget
	if confirm <= 0 {
		confirm = 2 * time.Second
	}
	// 允许根与 cwd 走同一归一策略（同一 resolveWorkspace 函数，codex 终审必修
	// 项）：根尚不存在且祖先含符号链接时，若根整体回退 Abs+Clean 保留别名形态而
	// cwd 消解了别名，同一路径会出现「根对不上 cwd」的自相矛盾授权失败——统一
	// 走最近已存在祖先策略后两者恒归一为同一真身。
	roots := make([]string, 0, len(opt.AllowedWorkspaceRoots))
	for _, r := range opt.AllowedWorkspaceRoots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		canonical, fallback := resolveWorkspace(r)
		if fallback {
			log.Printf("sessionop: 允许根 %q 归一回退（EvalSymlinks 全链不可解析，按 Abs+Clean 保留）: %s", r, canonical)
		}
		roots = append(roots, canonical)
	}
	s := &Service{
		led:        led,
		queueLimit: limit,
		roots:      roots,
		now:        now,
		log:        log,
		confirm:    confirm,
		turns:      map[string]map[int64]*turnHandle{},
		reqPath:    filepath.Join(dataDir, requestsFileName),
		reqs:       map[string]RequestRecord{},
		reqLog:     log,
	}
	s.loadRequests()
	return s, nil
}

// ---------- 受理守卫（队列上限 / 工作区授权 / 关闭防护） ----------

// nonTerminalTurnCount 会话非终态已受理轮计数（RFC §2 口径）：accepted/started、
// TurnSeq>0、含在途与排队；close 任务（TurnSeq=0）与终态不计。账本 View 为发布后
// 不可变快照，计数无锁安全；受理原子性由调用方在 service.localMu 互斥段内
// 「守卫 → 入账」保证（RFC §2）。
func (s *Service) nonTerminalTurnCount(sessionID string) int {
	n := 0
	for _, e := range s.led.View() {
		if e.State == ledger.StateFinished {
			continue
		}
		sr := e.Task.Session
		if sr == nil || sr.TurnSeq <= 0 || sr.SessionID != sessionID {
			continue
		}
		n++
	}
	return n
}

// AdmissionGuard 队列上限守卫：非终态受理轮已达上限即拒绝（受理前调用，零副
// 作用——无账本条目、高水位不推进、CLI 不执行）。
func (s *Service) AdmissionGuard(sessionID string) error {
	if n := s.nonTerminalTurnCount(sessionID); n >= s.queueLimit {
		return fmt.Errorf("%w: 会话 %s 非终态受理轮已达 %d 条，拒绝受理第 %d 条", ErrQueueFull, sessionID, s.queueLimit, n+1)
	}
	return nil
}

// CloseGuard 关闭在途防护：存在非终态受理轮即拒绝关闭（RFC §4；调用方须在
// service.localMu 互斥段内判定，防「检查后新轮插入」）。
func (s *Service) CloseGuard(sessionID string) error {
	if n := s.nonTerminalTurnCount(sessionID); n > 0 {
		return fmt.Errorf("%w: 会话 %s 尚有 %d 条非终态受理轮（先取消或等待收束）", ErrCloseInFlight, sessionID, n)
	}
	return nil
}

// AuthorizeWorkspace 工作区授权：未配置根 = 放行（保持现状）；cwd 与允许根均经
// resolveWorkspace 归一（根与 cwd 同一策略，根已在装配时归一），归一后 cwd 必须
// 等于某根或位于某根之内（前缀含路径分隔符边界；根本身为文件系统根 "/" 时不追
// 加分隔符——"//x" 形态的伪前缀会误拒全部子目录，codex 联审必修项）。解析失败
// 策略见 resolveWorkspace：最近已存在祖先解析后拼接剩余段，全部失败回退
// Abs+Clean 并在此注记回退态（s.log，codex 终审必修项：注释与行为一致）——目录
// 存在性由执行层如实失败，授权不因回退放行越界路径。
func (s *Service) AuthorizeWorkspace(cwd string) error {
	if len(s.roots) == 0 {
		return nil
	}
	resolved, fallback := resolveWorkspace(cwd)
	if fallback {
		s.log.Printf("sessionop: 会话工作目录 %q 归一回退（EvalSymlinks 全链不可解析，按 Abs+Clean 保留）: %s", cwd, resolved)
	}
	for _, root := range s.roots {
		prefix := root
		if !strings.HasSuffix(root, string(filepath.Separator)) {
			prefix = root + string(filepath.Separator)
		}
		if resolved == root || strings.HasPrefix(resolved, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q（归一 %q 不在任何允许根内）", ErrWorkspaceDenied, cwd, resolved)
}

// resolveWorkspace 会话工作区路径归一（根与 cwd 同一函数同一策略——允许根装配
// 与授权判定对同一路径必须得到同一归一结果，否则「根留别名、cwd 消别名」令同
// 路径自身授权失败，codex 终审必修项）：优先整体 EvalSymlinks；失败（路径尚不
// 存在等）向上找最近的已存在祖先解析其真实路径后拼接剩余段——防止 /var→
// /private/var 这类前缀别名让新建子目录被误判越界，且祖先为文件系统根 "/" 时拼
// 接经 Join（内建 Clean）不产生 "//xxx" 双斜杠形态（codex 终审必修项，实测
// /__missing/a/b 曾归一为 //__missing/a/b）；连祖先都解析不了（全链不存在）回
// 退 Abs+Clean，fallback=true 交调用方注记回退态。只读路径运算，无文件系统变更。
func resolveWorkspace(path string) (canonical string, fallback bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path), true
	}
	abs = filepath.Clean(abs)
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, false
	}
	rest := ""
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break // 到达文件系统根仍不可解析：全链不存在，回退
		}
		rest = string(filepath.Separator) + filepath.Base(cur) + rest
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), false // Join 清洗 r="/" 时的 "//" 双斜杠
		}
	}
	return abs, true
}

// ---------- 取消句柄：登记 / 绑定 / 意图 / 收敛 ----------

// BeginTurn 登记受理轮取消句柄（幂等：同 (sessionID,seq,taskID) 重复登记零操作；
// 异 taskID 视为异常重复执行，收敛旧句柄后替换）。受理互斥段内调用（登记先于
// 任何取消可达时刻）；seq<=0（close 任务）不登记。
func (s *Service) BeginTurn(sessionID string, seq int64, taskID string) {
	if seq <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.turns[sessionID]
	if old, ok := m[seq]; ok {
		if old.taskID == taskID {
			return
		}
		// 防御位：同轮位异任务（重复执行不应发生）——收敛旧句柄防等待悬挂
		s.convergeHandleLocked(old)
		delete(m, seq)
	}
	if m == nil {
		m = map[int64]*turnHandle{}
		s.turns[sessionID] = m
	}
	m[seq] = &turnHandle{taskID: taskID, done: make(chan struct{})}
}

// BindCancel 绑定执行取消开关（execute 入口调用）。取消意图先于绑定成立的
//（受理互斥段与 execute 之间的窄窗口）立即触发，保证意图必然生效。
func (s *Service) BindCancel(sessionID string, seq int64, taskID string, cancel func()) {
	h := s.lookup(sessionID, seq)
	if h == nil || h.taskID != taskID {
		return
	}
	h.mu.Lock()
	h.cancel = cancel
	fire := h.requested
	h.mu.Unlock()
	if fire && cancel != nil {
		cancel()
	}
}

// IsCancelRequested 报告该轮是否已有取消意图（执行 goroutine 依此分类记账）。
func (s *Service) IsCancelRequested(sessionID string, seq int64) bool {
	h := s.lookup(sessionID, seq)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requested
}

// ConvergeTurn 收敛句柄（执行 goroutine 收口调用，含取消/自然终态/弃队全部路径；
// 弃队无终态路径用 AbortTurn）。收敛即从在途表移除（表只含在途轮，有界），句柄
// 本身交由等待者持有引用。
func (s *Service) ConvergeTurn(sessionID string, seq int64, taskID string) {
	s.convergeTurn(sessionID, seq, taskID)
}

// AbortTurn 无终态弃队路径的收敛（服务关闭弃队/恢复重排等）：释放等待者，回执
// 由账本实况如实判定（不虚报）。
func (s *Service) AbortTurn(sessionID string, seq int64, taskID string) {
	s.convergeTurn(sessionID, seq, taskID)
}

func (s *Service) convergeTurn(sessionID string, seq int64, taskID string) {
	if seq <= 0 {
		return
	}
	s.mu.Lock()
	if m := s.turns[sessionID]; m != nil {
		if h := m[seq]; h != nil && h.taskID == taskID {
			delete(m, seq)
			if len(m) == 0 {
				delete(s.turns, sessionID)
			}
			s.convergeHandleLocked(h)
		}
	}
	s.mu.Unlock()
}

// convergeHandleLocked 收敛句柄（调用方持 s.mu；锁序 s.mu → h.mu）：置位并关闭
// done，等待者解除阻塞。
func (s *Service) convergeHandleLocked(h *turnHandle) {
	h.mu.Lock()
	already := h.convergedFlag
	h.convergedFlag = true
	h.mu.Unlock()
	if !already {
		close(h.done)
	}
}

// lookup 取在途句柄（无锁嵌套：s.mu 内只取指针）。
func (s *Service) lookup(sessionID string, seq int64) *turnHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns[sessionID][seq]
}

// ---------- CancelTurn 原语 ----------

// CancelReceipt 取消回执（账本终态的投影；重放幂等）。
type CancelReceipt struct {
	SessionID string `json:"session_id"`
	TurnSeq   int64  `json:"turn_seq"`
	TaskID    string `json:"task_id"`
	// Outcome queued | executed | unknown（RFC §3）。
	Outcome string `json:"outcome"`
	// Status 账本终态：cancelled（queued/executed）| failed（unknown 待核对）。
	Status string `json:"status"`
	// Error 记账码：turn_cancelled_queued / turn_cancelled_executed /
	// turn_cancel_outcome_unknown。
	Error string `json:"error"`
}

// CancelTurn 取消原语（RFC §3）：登记取消意图 → 触发执行取消（排队轮确定性终止
// 不 spawn；执行轮经 runner 治理整组回收）→ 等待收敛 → 按账本终态出回执。
// 对已自然终态轮先到保留（ErrTurnFinished，已完成不可取消）；对已取消轮幂等返回
// 原回执。ctx 只约束调用方等待，不撤销已登记的取消意图。
func (s *Service) CancelTurn(ctx context.Context, sessionID string, seq int64) (*CancelReceipt, error) {
	if seq <= 0 {
		return nil, fmt.Errorf("%s: turn_seq 必须为正（close 任务不可取消）", CodeTurnNotFound)
	}
	h := s.lookup(sessionID, seq)
	if h == nil {
		// 受理在途但句柄未登记（受理与 execute 登记之间的窄窗口，RFC §10）：
		// bounded 等待登记后照常取消；超时如实拒绝（不虚报已受理取消），调用方
		// 可重试。已终态/从未受理的轮不在此列（receipt 如实判定）。
		if e, ok := findTurnEntry(s.led, sessionID, seq); ok && e.State != ledger.StateFinished {
			deadline := s.now().Add(s.confirm)
			for h == nil {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("%s: 等待取消句柄登记被中断: %w", CodeCancelUnconfirmed, err)
				}
				if !s.now().Before(deadline) {
					return nil, fmt.Errorf("sessionop: %s seq=%d 在途但取消句柄未登记（受理登记窗口），请重试", sessionID, seq)
				}
				sleepConfirmPoll(5 * time.Millisecond)
				h = s.lookup(sessionID, seq)
			}
		}
	}
	if h != nil {
		s.requestCancelInHandle(h)
		select {
		case <-h.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: 取消已登记，等待收束被中断（轮仍会按意图收敛）: %w", CodeCancelUnconfirmed, ctx.Err())
		}
	}
	return s.receipt(ctx, sessionID, seq)
}

// requestCancelInHandle 登记意图并触发取消开关（意图登记先于触发）。
func (s *Service) requestCancelInHandle(h *turnHandle) {
	h.mu.Lock()
	first := !h.requested
	h.requested = true
	c := h.cancel
	h.mu.Unlock()
	if first && c != nil {
		c()
	}
}

// receipt 按账本终态出取消回执（账本是真相）：等待终态落盘确认（预算内轮询；
// 执行 goroutine 在收敛句柄与 MarkFinished 之间存在微秒级窗口，落盘失败则预算
// 耗尽如实报 ErrCancelUnconfirmed）。终态非取消类 = 先到保留（ErrTurnFinished）。
func (s *Service) receipt(ctx context.Context, sessionID string, seq int64) (*CancelReceipt, error) {
	deadline := s.now().Add(s.confirm)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: 回执确认被中断: %w", CodeCancelUnconfirmed, err)
		}
		e, ok := findTurnEntry(s.led, sessionID, seq)
		if ok && e.State == ledger.StateFinished && e.Result != nil {
			return receiptOf(sessionID, seq, e.Task.ID, e.Result.Status, e.Result.Error)
		}
		if !ok {
			// 无受理记录：高水位覆盖该序号 = 已收敛且条目被保留期清理（终态既往），
			// 否则从未受理。
			if s.led.SessionTurnHighWater(sessionID) >= seq {
				return nil, fmt.Errorf("%w: %s seq=%d（受理记录已过保留期清理，按已终态处理）", ErrTurnFinished, sessionID, seq)
			}
			return nil, fmt.Errorf("%w: %s seq=%d", ErrTurnUnknown, sessionID, seq)
		}
		if !s.now().Before(deadline) {
			return nil, fmt.Errorf("%w: %s seq=%d（取消已触发，终态落盘未确认，不虚报结果）", ErrCancelUnconfirmed, sessionID, seq)
		}
		sleepConfirmPoll(20 * time.Millisecond)
	}
}

// receiptOf 由账本终态出回执；取消类记账码映射 outcome，其余 = 先到保留。
func receiptOf(sessionID string, seq int64, taskID, status, errCode string) (*CancelReceipt, error) {
	switch {
	case strings.HasPrefix(errCode, CodeCancelQueued+":"),
		strings.HasPrefix(errCode, CodeCancelExecuted+":"),
		strings.HasPrefix(errCode, CodeCancelOutcomeUnknown+":"):
		outcome := OutcomeQueued
		switch {
		case strings.HasPrefix(errCode, CodeCancelExecuted+":"):
			outcome = OutcomeExecuted
		case strings.HasPrefix(errCode, CodeCancelOutcomeUnknown+":"):
			outcome = OutcomeUnknown
		}
		code, _, _ := strings.Cut(errCode, ":")
		return &CancelReceipt{
			SessionID: sessionID, TurnSeq: seq, TaskID: taskID,
			Outcome: outcome, Status: status, Error: code,
		}, nil
	}
	return nil, fmt.Errorf("%w: %s seq=%d（终态 %s/%s，已完成不可取消）", ErrTurnFinished, sessionID, seq, status, firstSegment(errCode))
}

// firstSegment 取错误串首段（码：消息 的码部分）。
func firstSegment(s string) string {
	code, _, _ := strings.Cut(s, ":")
	return code
}

// findTurnEntry 按会话与轮序号定位受理条目（快照扫描；轮受理唯一）。
func findTurnEntry(l *ledger.Ledger, sessionID string, seq int64) (ledger.Entry, bool) {
	for _, e := range l.View() {
		sr := e.Task.Session
		if sr == nil || sr.TurnSeq != seq || sr.SessionID != sessionID {
			continue
		}
		return e.Clone(), true
	}
	return ledger.Entry{}, false
}

// ---------- 取消证据分类（纯函数，RFC §3 证据口径） ----------

// CancelClass 取消证据分类（spawn 边界 = CLI 进程真实启动）。
type CancelClass int

const (
	// CancelClassQueued 未 spawn（内核轮行不存在：调度/内核门排队期弃队）。
	CancelClassQueued CancelClass = iota
	// CancelClassExecuted 已 spawn 且有停止证据：治理整组终止（错误前缀 timeout:
	// 且退出码 -1；runner 收尾已整组回收，无孤儿）。
	CancelClassExecuted
	// CancelClassUnknown 已 spawn 但停止证据缺失：待核对（绝不虚报已停止）。
	CancelClassUnknown
	// CancelClassNatural 自然终态（真实退出码/自然失败）：先到保留。
	CancelClassNatural
)

// ClassifyCancelTurn 依内核轮行证据分类（纯函数；turn 为 nil 表示未进入内核执行）。
func ClassifyCancelTurn(turn *protocol.Turn) CancelClass {
	if turn == nil {
		return CancelClassQueued
	}
	if turn.ExitCode == protocol.ExitCodeNotRun {
		if strings.HasPrefix(turn.Error, protocol.CodeTimeout+":") {
			// runner 治理：context 取消/到期 → 进程组整组终止 + Wait + Reap。
			return CancelClassExecuted
		}
		return CancelClassUnknown
	}
	return CancelClassNatural
}

// ---------- 小工具 ----------

// sleepConfirmPoll 回执轮询间隔（var 仅为测试可缩短；生产不注入）。
var sleepConfirmPoll = time.Sleep
