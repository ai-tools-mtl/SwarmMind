// Package ledger 实现 node 执行账本（SPEC 4.8）：完整任务持久化与三态恢复。
// 持久化模式与 hub store 相同（SPEC 4.7）：先写后见单写事务 + data_dir flock。
// 节点侧关键顺序：accepted 落盘成功 → 发 task_ack；started 落盘成功 → 发 task_started
// → spawn；finished+result 落盘成功 → 发 task_result。
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ledgerFileName 账本文件名（相对 data_dir；SPEC 4.8：data/ledger.json）。
const ledgerFileName = "ledger.json"

// tmpFileName 落盘临时文件名：同目录写入后 rename，保证替换原子（SPEC 4.7）。
const tmpFileName = ledgerFileName + ".tmp"

// ledgerFileVersion 账本文件结构版本。v0 = 旧版裸 map（task_id → Entry，无信封），
// v1 起为信封形态（entries + session_turn_hw；T2-R2 N4）。装载按 version 分流，
// 旧文件零迁移直读。
const ledgerFileVersion = 1

// ledgerFile 账本磁盘形态（v1 信封）。
type ledgerFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
	// SessionTurnHW 会话已受理平台轮序号高水位（session_id → max turn_seq，含在
	// 途轮；close 不占号不产生条目）。与受理同一事务推进并随本文件持久化——条目
	// 过保留期清理后高水位仍在（T2-R2 N4：本机平台轮号推导的持久下界，取消/过期/
	// 重启/清理后绝不回退复用；与内核 TurnCount 执行口径互补，谁都不替代谁）。
	SessionTurnHW map[string]int64 `json:"session_turn_hw,omitempty"`
}

// snapshot 账本已发布状态（先写后见的发布单位；两表同事务落盘、同事务发布）。
type snapshot struct {
	entries map[string]Entry
	turnHW  map[string]int64
}

// 事务故障注入阶段（与 hub store 同预设；仅包内测试设置 inject 使用，生产恒为 nil）。
const (
	// stageBeforeWrite 写盘前（副本已构造、尚未触盘）。
	stageBeforeWrite = "before_write"
	// stageAfterRename rename 之后（盘上已是新状态、内存尚未发布）。
	stageAfterRename = "after_rename"
	// stageBeforePublish 内存发布前。
	stageBeforePublish = "before_publish"
	// stageBeforeAck 向调用方 Ack 前（内存已发布、调用方尚未得到确认）。
	stageBeforeAck = "before_ack"
)

// EntryState 账本条目状态（SPEC 4.8：accepted | started | finished）。
type EntryState string

const (
	// StateAccepted 任务已入账本（accepted 落盘成功后才可发 task_ack）。
	StateAccepted EntryState = "accepted"
	// StateStarted 执行已开始（started 落盘成功后才可发 task_started 并 spawn）。
	StateStarted EntryState = "started"
	// StateFinished 终态已落盘（finished+result 落盘成功后才可发 task_result）。
	StateFinished EntryState = "finished"
)

// 账本操作错误哨兵（errors.Is 判定）。
var (
	// ErrDuplicate 重复 task_id 入账（node 对重复 task_offer 不再执行，直接重发 task_ack，SPEC 4.3）。
	ErrDuplicate = errors.New("ledger: 任务已存在")
	// ErrNotFound 任务不在账本中。
	ErrNotFound = errors.New("ledger: 任务不存在")
	// ErrWrongState 条目当前状态不允许该操作。
	ErrWrongState = errors.New("ledger: 状态不允许该操作")
	// ErrClosed 账本已关闭后再提交事务。
	ErrClosed = errors.New("ledger: 已关闭")
	// ErrIdemConflict 幂等键已被内容不同的任务占用（同键不同内容；
	// AcceptOriginIdempotent 事务内查重的 conflict 三态）。
	ErrIdemConflict = errors.New("ledger: 幂等键已被不同内容占用")
)

// IdemMatch 内容指纹比对结论（AcceptOriginIdempotent 的 match 回调返回值）。
// 指纹口径由调用方定义（本地直执行镜像 hub ContentHash 四要素+群引用），账本
// 只负责「事务内查重 + 入账」的原子性。
type IdemMatch int

const (
	// IdemExact 同键同内容：幂等命中，dup 返回原条目。
	IdemExact IdemMatch = iota
	// IdemOtherGroup 仅群引用不同：换群重试，落为新任务（不 dup 不冲突）。
	IdemOtherGroup
	// IdemDifferent 同键不同内容：事务拒绝，返回 ErrIdemConflict。
	IdemDifferent
)

// Origin 任务来源（方案 3.2 双路径模型，2026-10-22）：回传过滤的依据——
// local 任务的结果/重发/needs_review 一律不得推给 hub（standalone 期账本条目
// 在切换组网后会成批触发 task_not_found 噪音）。
type Origin string

const (
	// OriginHub hub 经 WSS 下发（task_offer 链路）；既有账本文件无 origin 字段，
	// 反序列化为零值空串——Get 时归一为 hub（见 Entry.normalized）。
	OriginHub Origin = "hub"
	// OriginLocal 本机派单（localapi 本地直执行，方案 3.2）。
	OriginLocal Origin = "local"
)

// Entry 账本条目（SPEC 4.8）：task_id → {task 完整, state, result?, result_acked}，
// 另含重传终止标记 closed（SPEC 4.5）与首次确认时刻 first_acked_at（保留期限基准，SPEC 4.6）。
type Entry struct {
	// Task 完整任务输入（持久化完整任务，重启后无需 hub 重派）。
	Task protocol.Task `json:"task"`
	// State 账本状态（accepted|started|finished）。
	State EntryState `json:"state"`
	// Origin 任务来源（local|hub）：回传过滤依据（方案 3.2）。空 = 旧版账本条目
	// （origin 字段引入前落盘），一律视为 hub——老条目全部走 hub 回传链路，行为不变。
	Origin Origin `json:"origin,omitempty"`
	// Result 终态结果（state=finished 时非 nil）。
	Result *protocol.TaskResult `json:"result,omitempty"`
	// ResultAcked hub 已确认终态持久化（收到 result_ack 置位）。local 任务落终态
	// 即自确认（结果消费方就是本机账本，落盘即送达；见 MarkFinished）。
	ResultAcked bool `json:"result_acked"`
// FirstAckedAt 首次收到 result_ack 的时刻（UTC）。仅在未 acked→acked 迁移时写入一次，
// 重复 Ack 不更新：终态保留（默认 24h，terminal_retention 可配）从首次确认起算，
// 重复确认不重置窗口（SPEC 4.6）。
	FirstAckedAt time.Time `json:"first_acked_at"`
	// Closed 重传终止标记（SPEC 4.5）：task_result 被 hub 拒收（result_conflict /
	// expired 终态拒绝）后置位并持久化，停止重发；记录保留作冲突审计，不删除、不参与清理。
	// 重臂成功（RearmFence 换新代次，P1-3）即解除——新租约下的结果重发必须恢复，
	// 否则重派后结果首发丢失的任务永久滞留。
	Closed bool `json:"closed"`
	// FenceEpoch 任务租约代次快照（P4/SPEC 4.11）：task_offer 携带的派发时刻
	// hub 代次，task_result 原样回带供 hub 等值仲裁。仅 hub 显式（重）授予时
	// 变更：入账（AcceptFenced）或重臂（RearmFence——重复 offer/取消帧携新
	// 代次，二者只能经当前会话送达，zombie 无法伪造）；重连重发等 node 自发
	// 路径永不改写。旧账本条目无此字段=0，回带缺失由 hub 按「缺省=当前
	// epoch」宽容处理，向后兼容。
	FenceEpoch int64 `json:"fence_epoch,omitempty"`
	// Pgid 执行进程组 ID（maturity-orphan-guard）：spawn 成功后由 service 经
	// MarkSpawned 落盘，供 node 崩溃重启后的孤儿清扫（started 未终态条目）。
	// Windows 恒 0 不落（Job Object KILL_ON_JOB_CLOSE 由 OS 兑底，无需清扫）；
	// 旧账本条目无此字段（=0），恢复扫描直接放过（向后兼容，零迁移）。
	Pgid int64 `json:"pgid,omitempty"`
	// ProcStart 组长的进程启动时刻指纹（maturity-orphan-guard，R1 必修1）：与
	// Pgid 同事务落盘的「进程实例证据」——Linux 为 /proc/<pid>/stat 第 22 字段
	//（clock ticks since boot），darwin 为 P_starttime 微秒（Sec*1e6+Usec，R2：
	// 秒精度存在同秒不同实例碰撞，真机实证）。恢复清扫以 pid+指纹等值比对精确
	// 匹配原进程实例；指纹为平台本地格式，仅「同平台 + 同启动周期」内做等值
	// 比较（BootIdent 域校验），升级/换机后值域不同自然失配 → 放过（安全侧）。
	// 0 = 未记录（旧条目/Windows/读取失败），恢复时证据不足直接放过。
	ProcStart int64 `json:"proc_start,omitempty"`
	// BootIdent 落盘时的系统启动周期标识（maturity-orphan-guard，R2 必修1）：
	// Linux=/proc/sys/kernel/random/boot_id，darwin=kern.boottime 微秒串。账本
	// 跨 OS 重启保留，无周期校验则跨周期的同 pid/同指纹/同名无法区分；恢复时
	// 与当前周期不等即整条证据过期（孤儿必已随旧周期消亡）→ 放过。
	BootIdent string `json:"boot_ident,omitempty"`
}

// normalized 归一 origin：空（旧版条目）视为 hub。
func (e Entry) normalized() Entry {
	if e.Origin == "" {
		e.Origin = OriginHub
	}
	return e
}

// Clone 深拷贝条目（容器逐层复制），供先写后见事务构造新副本。
func (e Entry) Clone() Entry {
	e.Task = e.Task.Clone()
	if e.Result != nil {
		r := e.Result.Clone()
		e.Result = &r
	}
	return e
}

// txnReq 单写队列中的一个事务请求。
type txnReq struct {
	op     string // 操作名（故障注入按 op+stage 定位，见 inject）
	mutate func(map[string]Entry) error
	reply  chan error
}

// Ledger node 执行账本（data_dir/ledger.json）：单写 goroutine 串行事务，先写后见
// （SPEC 4.7/4.8）。事务路径与 hub store 相同：克隆 → mutate → 临时文件+rename →
// 原子发布 → Ack；失败丢弃副本、不发布、不 Ack。
type Ledger struct {
	path       string
	tmp        string
	lock       *Lock
	ch         chan *txnReq
	cur        atomic.Pointer[snapshot]
	writerDone chan struct{}
	cancels    *cancelTombstones // 取消墓碑 sidecar（cancels.go；与账本同 data_dir/flock）

	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	// inject 故障注入钩子：按「操作名+阶段」返回非 nil 错误即中断事务（op 为
	// 触发事务的导出方法名，如 "RearmFence"；仅测试设置，生产为 nil）。
	inject func(op, stage string) error

	// orphans 启动恢复收集的崩溃孤儿清单（maturity-orphan-guard；recover 填充，
	// OrphanGroups 读口）：service.New 消费做身份校验清扫。
	orphans []OrphanGroup
}

// Open 打开 node 账本：取 data_dir 单实例 flock（冲突即报错）、加载 ledger.json、
// 执行启动恢复（SPEC 4.8）并把恢复产生的降级写持久化后才返回。
// 返回的 *Recovery 给出恢复清单：Reschedule 交回本地调度，Resend 待重发 task_result。
func Open(dataDir string) (*Ledger, *Recovery, error) {
	return open(dataDir, nil)
}

// OpenForTest Open 的可注入变体（跨包测试专用，如 service 端到端注入重臂写盘
// 失败）：inject 按「操作名+阶段」拦截事务；生产恒走 Open。
func OpenForTest(dataDir string, inject func(op, stage string) error) (*Ledger, *Recovery, error) {
	return open(dataDir, inject)
}

// open Open 的可注入变体：inject 透传账本事务（含启动恢复的降级写），
// 供包内测试模拟恢复期落盘失败；生产恒走 Open（inject=nil）。
func open(dataDir string, inject func(op, stage string) error) (*Ledger, *Recovery, error) {
	lock, err := AcquireLock(dataDir)
	if err != nil {
		return nil, nil, err
	}
	l := &Ledger{
		path:       filepath.Join(dataDir, ledgerFileName),
		tmp:        filepath.Join(dataDir, tmpFileName),
		lock:       lock,
		ch:         make(chan *txnReq),
		writerDone: make(chan struct{}),
		inject:     inject,
	}
	entries, hw, err := loadState(l.path)
	if err != nil {
		_ = lock.Release()
		return nil, nil, err
	}
	cancels, err := openCancelTombstones(dataDir)
	if err != nil {
		_ = lock.Release()
		return nil, nil, err
	}
	l.cancels = cancels
	l.cur.Store(&snapshot{entries: entries, turnHW: hw})
	go l.writeLoop()
	rec, err := l.recover(protocol.NowUTC())
	if err != nil {
		_ = l.Close()
		return nil, nil, err
	}
	l.orphans = append([]OrphanGroup(nil), rec.Orphans...) // R2 建议修：与 Recovery 断开别名，读口两侧互不影响
	return l, rec, nil
}

// loadState 读取并解析账本全量状态（条目 + 会话轮高水位）。文件不存在视为空账本
// （首次启动）。v1 信封按 version 识别；旧版裸 map（无 version 字段）零迁移直读，
// 高水位自条目重算（受理条目本身携带 Task.Session.TurnSeq）——新旧两形态装载后
// 都做一次「存储值 ∪ 条目扫描」取大，防手改/降级文件的高水位缺漏。
// origin 逐条归一：旧版账本条目无 origin 字段，一律视为 hub（老条目继续走
// hub 回传链路，行为不变）。
func loadState(path string) (map[string]Entry, map[string]int64, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Entry{}, map[string]int64{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: 读取账本失败: %w", err)
	}
	var entries map[string]Entry
	var hw map[string]int64
	var env ledgerFile
	if json.Unmarshal(b, &env) == nil && env.Version >= 1 {
		entries = env.Entries
		hw = env.SessionTurnHW
	} else if jerr := json.Unmarshal(b, &entries); jerr != nil {
		// 兼容旧形态；两态都解析失败即账本损坏
		return nil, nil, fmt.Errorf("ledger: 账本损坏 %s: %w", path, jerr)
	}
	if entries == nil {
		entries = map[string]Entry{}
	}
	for id, e := range entries {
		entries[id] = e.normalized()
	}
	if hw == nil {
		hw = map[string]int64{}
	}
	for _, e := range entries { // 重算兜底：max(存储高水位, 条目扫描)
		if s := e.Task.Session; s != nil && s.TurnSeq > hw[s.SessionID] {
			hw[s.SessionID] = s.TurnSeq
		}
	}
	return entries, hw, nil
}

// loadEntries 仅读条目（磁盘核验辅助；完整状态装载走 loadState）。
func loadEntries(path string) (map[string]Entry, error) {
	entries, _, err := loadState(path)
	return entries, err
}

// View 返回当前已发布账本快照（先写后见：只可能是已完整落盘的状态）。
// 快照按引用零拷贝返回（性能考量，刻意不做副本），调用方不得修改返回的 map 及其中任何
// Entry——Task/Result 内层的 slice、map 与账本共享底层数据，原地改写即污染已发布状态；
// 需要修改一律走 Accept/MarkXxx 等 Update(mutate) 事务，长期持有或跨 goroutine 传递
// 请自行 Clone（Get 即返回副本）。
func (l *Ledger) View() map[string]Entry { return l.cur.Load().entries }

// SessionTurnHighWater 会话已受理平台轮序号高水位（含在途轮；close 不占号不入账
// 恒不影响）。与受理同事务推进、随账本文件持久化、不随条目保留期清理回退
//（T2-R2 N4）——本机平台轮号推导的持久下界，与内核 TurnCount（执行口径，不含
// 未进 CLI 的受理轮）互补；无受理记录返回 0。
func (l *Ledger) SessionTurnHighWater(sessionID string) int64 {
	return l.cur.Load().turnHW[sessionID]
}

// OrphanGroups 启动恢复收集的崩溃孤儿清单（maturity-orphan-guard；Open 时固定，
// 快照读）：started 降级时登记 pgid>0 的条目。service.New 消费做身份校验清扫。
// 返回内部切片的副本（R1 建议修：读口不外泄可变底层数据）。
func (l *Ledger) OrphanGroups() []OrphanGroup {
	out := make([]OrphanGroup, len(l.orphans))
	copy(out, l.orphans)
	return out
}

// Get 查询单个任务条目（返回副本，修改不影响账本）。
func (l *Ledger) Get(taskID string) (Entry, bool) {
	e, ok := l.View()[taskID]
	return e.Clone(), ok
}

// OriginOf 查任务来源（快照读，零拷贝；发送堵点高频调用用此，勿用 Get）。
// 任务不在账本返回 ""（调用方按非 local 处理——hub 链路迟到帧仍走回传语义）。
func (l *Ledger) OriginOf(taskID string) Origin {
	e := l.View()[taskID]
	return e.Origin
}

// PendingResend 返回 finished、未 acked 且未 closed 的条目（重发队列；SPEC 4.5：每次重连
// 成功后立即重发，此后每 60s 一次，直至 result_ack；收到 result_conflict / expired 拒绝后
// 调用方 MarkResendClosed 置位，条目退出队列，不再重发）。
func (l *Ledger) PendingResend() []Entry {
	var out []Entry
	for _, e := range l.View() {
		if e.State == StateFinished && !e.ResultAcked && !e.Closed {
			out = append(out, e.Clone())
		}
	}
	return out
}

// Accept 将任务写入账本（state=accepted；调用方须待本方法成功后再发 task_ack，SPEC 4.7）。
// 重复 task_id 返回 ErrDuplicate 且账本不变（重复 offer 只执行一次，SPEC 4.3 / DoD 8）。
// origin 记 hub（task_offer 链路专用；本地直执行走 AcceptOrigin）。
func (l *Ledger) Accept(t protocol.Task) error {
	return l.AcceptOrigin(t, OriginHub)
}

// AcceptOrigin 按来源入账（方案 3.2 双路径）：Accept 的带 origin 变体。
// 本地直执行记 OriginLocal，hub 下发记 OriginHub；重复 task_id 返回 ErrDuplicate。
func (l *Ledger) AcceptOrigin(t protocol.Task, origin Origin) error {
	return l.AcceptFenced(t, origin, 0)
}

// AcceptFenced 带租约代次入账（P4/SPEC 4.11）：AcceptOrigin 的 fence 变体，
// task_offer 链路专用——把派发帧携带的 fence_epoch 快照随条目持久化，
// task_result 据此回带。fence=0 = 派发帧未携租约（旧版 hub），条目照常入账。
func (l *Ledger) AcceptFenced(t protocol.Task, origin Origin, fence int64) error {
	return l.updateOp("AcceptFenced", func(m map[string]Entry) error {
		if _, ok := m[t.ID]; ok {
			return ErrDuplicate
		}
		m[t.ID] = Entry{Task: t.Clone(), State: StateAccepted, Origin: origin, FenceEpoch: fence}
		return nil
	})
}

// RearmFence 重臂任务租约（P4/SPEC 4.11）：把条目回带用的 fence_epoch 更新为
// hub 帧携带的新代次。仅 hub 显式授予的帧（重复 task_offer=重派收口、
// task_cancel=取消收口——node 侧对账取大者后调用，P1-2）触发——二者只能经当前
// 会话送达，重臂即承认「本条目的收口回执对齐 hub 当前代次」；zombie 进程无当前
// 会话，收不到帧即无法自臂。node 自发路径（重连重发/恢复执行）永不调用本方法。
// 幂等（同值 no-op）；origin=local 条目拒臂（hub 租约只管 hub 链路）。
// 换新代次成功时同时解除 closed（P1-3）：拒收关闭绑定的是被拒旧代次，重臂即
// 承认新租约下的结果尚未收口，须恢复重发——同值 no-op 不解除（同代次被拒的
// 审计保留，重发只会再被拒）。
func (l *Ledger) RearmFence(taskID string, fence int64) error {
	return l.updateOp("RearmFence", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.normalized().Origin != OriginHub || e.FenceEpoch == fence {
			return nil
		}
		ne := e.Clone()
		ne.FenceEpoch = fence
		ne.Closed = false
		m[taskID] = ne
		return nil
	})
}

// AcceptOriginIdempotent 幂等入账（方案 3.2 本地直执行幂等，返工 #3）：同键查重
// 与入账在同一 Update 事务内完成——查重三态在单写 goroutine 串行域内重检，消除
// 「先扫快照再独立入账」两步之间的 TOCTOU 窗口（并发同键恰有一个新任务、其余
// dup；并发同键异内容恰一成功一 conflict）。
//   - idemKey 为空：无幂等语义，直接入账（等价 AcceptOrigin）；
//   - 扫描作用域 = 同 origin 且 Task.IdemKey 相同的条目，逐条调 match 判定三态；
//   - 返回 dup=true 时 dupTask 为命中的原任务（ID/TraceID 供 ack 回填），账本不变；
//   - ErrIdemConflict：事务拒绝，账本不变。
//
// match 在写 goroutine 内调用：须为纯函数（无副作用、不阻塞、不得再触碰账本）。
func (l *Ledger) AcceptOriginIdempotent(t protocol.Task, origin Origin, idemKey string,
	match func(existing protocol.Task) IdemMatch) (dupTask protocol.Task, dup bool, err error) {
	if idemKey == "" {
		return protocol.Task{}, false, l.AcceptOrigin(t, origin)
	}
	err = l.updateOp("AcceptOriginIdempotent", func(m map[string]Entry) error {
		for _, e := range m {
			if e.normalized().Origin != origin || e.Task.IdemKey != idemKey {
				continue
			}
			switch match(e.Task) {
			case IdemExact:
				dupTask = e.Task.Clone()
				dup = true
				return nil
			case IdemOtherGroup:
				continue // 换群重试：不 dup 不冲突，继续看其余同键条目
			default:
				return ErrIdemConflict
			}
		}
		m[t.ID] = Entry{Task: t.Clone(), State: StateAccepted, Origin: origin}
		return nil
	})
	if err != nil {
		return protocol.Task{}, false, err
	}
	return dupTask, dup, nil
}

// MarkStarted accepted→started（调用方须待成功后再发 task_started 并 spawn，SPEC 4.7）。
func (l *Ledger) MarkStarted(taskID string) error {
	return l.updateOp("MarkStarted", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.State != StateAccepted {
			return fmt.Errorf("%w: %s → started", ErrWrongState, e.State)
		}
		ne := e.Clone()
		ne.State = StateStarted
		m[taskID] = ne
		return nil
	})
}

// MarkSpawned 在 started 条目上登记执行进程组证据三元组（maturity-orphan-guard）：
// pgid + 组长启动时刻指纹 procStart + 启动周期 bootIdent，spawn 成功后尽快落盘，
// 使 node 崩溃重启后能以「同周期 + pid + 指纹等值」精确识别重启前在跑的孤儿实例
//（R1 必修1/R2 必修1：仅有 pgid 无法排除复用给同名异路径进程的误杀面，秒级指纹
// 存在同秒碰撞）。落盘失败仅日志不致命（降级方向=恢复时放过清扫，安全侧）；终态
// 条目不接受（去终态后的 pgid 无意义）。窗口声明：spawn 与落盘之间崩溃则证据丢失，
// 恢复时放过清扫（任务仍正常收敛 needs_review，安全方向，概率极小）。
func (l *Ledger) MarkSpawned(taskID string, pgid, procStart int64, bootIdent string) error {
	if pgid <= 0 {
		return nil // Windows（恒 0）与异常值不落盘
	}
	return l.updateOp("MarkSpawned", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.State != StateStarted {
			return fmt.Errorf("%w: %s 非进行中，不登记 pgid", ErrWrongState, e.State)
		}
		ne := e.Clone()
		ne.Pgid = pgid
		ne.ProcStart = procStart
		ne.BootIdent = bootIdent
		m[taskID] = ne
		return nil
	})
}

// MarkFinished 写入终态：accepted/started → finished 并持久化 result
// （accepted 直达 finished 用于未 spawn 即失败的路径，如 policy_denied / expired）。
// 调用方须待成功后再发 task_result（SPEC 4.7）；重复终态返回 ErrWrongState，
// 由调用方按「以先到为准」的幂等语义处理（SPEC 4.3 收敛表）。
//
// local 来源条目落终态即自确认（ResultAcked=true，方案 3.2）：结果消费方就是
// 本机账本，落盘即送达——不经 hub 确认，天然退出重发队列（切换组网后不成批
// 推 hub），终态超保留期清理（CleanupAcked，默认 24h 可配）同样按
// first_acked_at 起算收敛。
func (l *Ledger) MarkFinished(taskID string, res protocol.TaskResult) error {
	return l.updateOp("MarkFinished", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.State == StateFinished {
			return fmt.Errorf("%w: 已为 finished", ErrWrongState)
		}
		ne := e.Clone()
		ne.State = StateFinished
		r := res.Clone()
		ne.Result = &r
		if ne.Origin == "" {
			ne.Origin = OriginHub // 防御：未归一条目按 hub 处理
		}
		if ne.Origin == OriginLocal {
			ne.ResultAcked = true
			ne.FirstAckedAt = protocol.NowUTC()
		} else {
			ne.ResultAcked = false
		}
		m[taskID] = ne
		return nil
	})
}

// MarkResultAcked 收到 result_ack：置 result_acked 并记录首次确认时刻 first_acked_at
// （保留期限起算点，SPEC 4.6）；对已 acked 条目幂等（no-op 成功，不更新 first_acked_at）。
func (l *Ledger) MarkResultAcked(taskID string) error {
	return l.updateOp("MarkResultAcked", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.State != StateFinished || e.Result == nil {
			return fmt.Errorf("%w: %s 非 finished 终态", ErrWrongState, e.State)
		}
		if e.ResultAcked {
			return nil
		}
		ne := e.Clone()
		ne.ResultAcked = true
		ne.FirstAckedAt = protocol.NowUTC()
		m[taskID] = ne
		return nil
	})
}

// MarkResendClosed 结果被 hub 拒收（err(result_conflict) 或 expired 终态拒绝）后停止重传：
// 置 closed 标记并持久化（SPEC 4.5：停止重传，账本标记 closed，冲突审计日志保留，不删除
// 记录）。条目须为 finished 终态；对已 closed 条目幂等；closed 条目退出重发队列，
// 重启恢复也不再进入 Resend。
func (l *Ledger) MarkResendClosed(taskID string) error {
	return l.updateOp("MarkResendClosed", func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		if e.State != StateFinished || e.Result == nil {
			return fmt.Errorf("%w: %s 非 finished 终态", ErrWrongState, e.State)
		}
		if e.Closed {
			return nil
		}
		ne := e.Clone()
		ne.Closed = true
		m[taskID] = ne
		return nil
	})
}

// CleanupAcked 清理 result_acked 超保留期的条目（SPEC 4.6/4.8；maturity-
// retention-config 起保留期由调用方注入：node.yaml terminal_retention 解析值，
// 默认 protocol.TerminalRetention）。期限基准为首次确认时刻 first_acked_at
// （不从完成时刻起算；重复 Ack 不更新、重启不重置，零值防御性跳过）。
// closed 条目未被 ack、不在清理范围（SPEC 4.5：冲突审计记录保留）。
// retention ≤ 0 不清理（0 = 永不自动清理，terminal_retention: 0s 显式语义；
// 负值配置层已拒，防御同口径）。返回清理条数；由 service 运行循环 1 分钟周期
// 调用（接线见 service.cleanupLoop）。无超期条目时快照预检早退、不进写事务
//（周期到点常态零写盘，不引入无谓 IO）。
func (l *Ledger) CleanupAcked(now time.Time, retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	// 零写盘快查（View 快照只读，发布后不可变）：无候选条目直接返回。
	overdue := false
	for _, e := range l.View() {
		if e.ResultAcked && e.Result != nil && !e.FirstAckedAt.IsZero() &&
			e.FirstAckedAt.Add(retention).Before(now) {
			overdue = true
			break
		}
	}
	if !overdue {
		return 0, nil
	}
	removed := 0
	err := l.updateOp("CleanupAcked", func(m map[string]Entry) error {
		for id, e := range m {
			if !e.ResultAcked || e.Result == nil || e.FirstAckedAt.IsZero() {
				continue
			}
			if e.FirstAckedAt.Add(retention).Before(now) {
				delete(m, id)
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ---------- 取消墓碑（cancels.go；task_cancel 先于 task_offer 的执行窗口封堵） ----------

// AddCancelTombstone 登记取消墓碑并持久化（幂等）：task_cancel 命中「不在账本」
// 的任务时由 service.handleTaskCancel 调用——随后同 task_id 的 task_offer 到达即
// 拒绝执行（handleOffer 查 HasCancelTombstone）。
func (l *Ledger) AddCancelTombstone(taskID string) error {
	return l.cancels.add(taskID, protocol.NowUTC())
}

// HasCancelTombstone 报告任务是否带未消费的取消墓碑。
func (l *Ledger) HasCancelTombstone(taskID string) bool { return l.cancels.has(taskID) }

// InjectCancelsForTest 设置墓碑落盘故障注入（跨包测试专用，如 service 端到端
// 注入墓碑落盘失败；op+stage 拦截与 Ledger.inject 同风格；生产零调用）。
func (l *Ledger) InjectCancelsForTest(inject func(op, stage string) error) {
	l.cancels.mu.Lock()
	l.cancels.inject = inject
	l.cancels.mu.Unlock()
}

// ClearCancelTombstone 消费墓碑（幂等）：cancelled 终态落盘成功后清除——账本
// 条目此后由 ErrDuplicate 挡住重复执行，墓碑使命完成。
func (l *Ledger) ClearCancelTombstone(taskID string) error { return l.cancels.clear(taskID) }

// CleanupCancelTombstones 清理超保留期的墓碑（TTL 兜底；加载与新增时已顺带清理，
// 此方法供调用方周期触发的显式入口）。
func (l *Ledger) CleanupCancelTombstones(now time.Time) error {
	return l.cancels.cleanup(now, protocol.TerminalRetention)
}

// Update 提交一个先写后见事务（模式同 hub store，SPEC 4.7）。mutate 在新副本上执行。
// 成功（返回 nil）= 已落盘并发布，此刻起 View 可见、调用方可对外 Ack。
// 失败分两种语义：
//   - rename 前失败（mutate 拒绝 / 写临时文件 / rename）：副本整体丢弃，内存与磁盘均无
//     任何可见变化（不发布、不 Ack），属普通失败，可直接重试；
//   - rename 后失败（after_rename / before_publish / before_ack，注入模拟真实崩溃窗口）：
//     新状态已提交磁盘（rename 成）乃至已发布内存（before_ack），但调用方未获 Ack——
//     「已提交但确认丢失」，重启后以磁盘为准。调用方须按幂等重试处理，不得假设事务未发生。
func (l *Ledger) Update(mutate func(map[string]Entry) error) error {
	return l.updateOp("Update", mutate)
}

// updateOp Update 的操作名标注变体：op 随事务进入单写队列，供故障注入按
// 「op+阶段」拦截（仅测试路径使用；生产 inject=nil 时与 Update 等价）。
func (l *Ledger) updateOp(op string, mutate func(map[string]Entry) error) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrClosed
	}
	l.inflight.Add(1)
	l.mu.Unlock()
	defer l.inflight.Done()

	req := &txnReq{op: op, mutate: mutate, reply: make(chan error, 1)}
	// Close 等 inflight 归零后才关 ch，此处不会向已关闭通道发送
	l.ch <- req
	return <-req.reply
}

// Close 停止接收新事务，排空在途事务后结束单写 goroutine 并释放 flock；幂等。
func (l *Ledger) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		l.inflight.Wait()
		close(l.ch)
		<-l.writerDone
		l.closeErr = l.lock.Release()
	})
	return l.closeErr
}

// writeLoop 单写 goroutine：串行处理事务直至通道关闭。
func (l *Ledger) writeLoop() {
	defer close(l.writerDone)
	for req := range l.ch {
		req.reply <- l.apply(req.op, req.mutate)
	}
}

// apply 执行一次先写后见事务；返回 nil 即完成「落盘 + 发布 + Ack」。
func (l *Ledger) apply(op string, mutate func(map[string]Entry) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ledger: 事务 panic（副本已丢弃）: %v", r)
		}
	}()
	cur := l.cur.Load()
	next := make(map[string]Entry, len(cur.entries)) // 构造新副本；此后任一步失败即整体丢弃
	for id, e := range cur.entries {
		next[id] = e.Clone()
	}
	nextHW := make(map[string]int64, len(cur.turnHW))
	for sid, v := range cur.turnHW {
		nextHW[sid] = v
	}
	if err = mutate(next); err != nil {
		return fmt.Errorf("ledger: 事务被拒绝: %w", err)
	}
	// 会话轮高水位与受理同事务推进（T2-R2 N4）：账本内任何会话轮条目
	//（Task.Session 非 nil 且 TurnSeq>0）即推进对应会话的已受理 max——与条目
	// 同事务落盘发布，取消/过期/重启/保留期清理后不回退（高水位随文件留存，
	// 条目可被清理）。max 扫描幂等且单调，任何操作（含恢复降级写）重复执行
	// 不产生额外变更；close 恒 TurnSeq=0 不占号。
	for _, e := range next {
		if s := e.Task.Session; s != nil && s.TurnSeq > nextHW[s.SessionID] {
			nextHW[s.SessionID] = s.TurnSeq
		}
	}
	if err = l.persist(op, next, nextHW); err != nil {
		return err
	}
	if err = l.injectFail(op, stageAfterRename); err != nil {
		return err
	}
	if err = l.injectFail(op, stageBeforePublish); err != nil {
		return err
	}
	l.cur.Store(&snapshot{entries: next, turnHW: nextHW}) // 原子发布（此前失败路径均不触发布）
	if err = l.injectFail(op, stageBeforeAck); err != nil {
		return err
	}
	return nil
}

// persist 将新账本写入临时文件并 rename 原子替换（SPEC 4.7：临时文件+rename）。
func (l *Ledger) persist(op string, entries map[string]Entry, hw map[string]int64) error {
	if err := l.injectFail(op, stageBeforeWrite); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ledgerFile{Version: ledgerFileVersion, Entries: entries, SessionTurnHW: hw}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: 写临时文件失败: %w", err)
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("ledger: 写临时文件失败: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("ledger: 刷盘失败: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("ledger: 关闭临时文件失败: %w", err)
	}
	if err = os.Rename(l.tmp, l.path); err != nil {
		return fmt.Errorf("ledger: rename 落盘失败: %w", err)
	}
	return nil
}

// injectFail 调用注入钩子；未设置时恒返回 nil。
func (l *Ledger) injectFail(op, stage string) error {
	if l.inject == nil {
		return nil
	}
	return l.inject(op, stage)
}
