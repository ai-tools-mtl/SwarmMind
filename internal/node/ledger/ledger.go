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
	// 重复 Ack 不更新：终态保留 24h 从首次确认起算，重复确认不重置窗口（SPEC 4.6）。
	FirstAckedAt time.Time `json:"first_acked_at"`
	// Closed 重传终止标记（SPEC 4.5）：task_result 被 hub 拒收（result_conflict /
	// expired 终态拒绝）后置位并持久化，停止重发；记录保留作冲突审计，不删除、不参与清理。
	Closed bool `json:"closed"`
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
	cur        atomic.Pointer[map[string]Entry]
	writerDone chan struct{}
	cancels    *cancelTombstones // 取消墓碑 sidecar（cancels.go；与账本同 data_dir/flock）

	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	// inject 故障注入钩子：按阶段返回非 nil 错误即中断事务（仅包内测试设置，生产为 nil）。
	inject func(stage string) error
}

// Open 打开 node 账本：取 data_dir 单实例 flock（冲突即报错）、加载 ledger.json、
// 执行启动恢复（SPEC 4.8）并把恢复产生的降级写持久化后才返回。
// 返回的 *Recovery 给出恢复清单：Reschedule 交回本地调度，Resend 待重发 task_result。
func Open(dataDir string) (*Ledger, *Recovery, error) {
	return open(dataDir, nil)
}

// open Open 的可注入变体：inject 透传账本事务（含启动恢复的降级写），
// 供包内测试模拟恢复期落盘失败；生产恒走 Open（inject=nil）。
func open(dataDir string, inject func(string) error) (*Ledger, *Recovery, error) {
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
	entries, err := loadEntries(l.path)
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
	l.cur.Store(&entries)
	go l.writeLoop()
	rec, err := l.recover(protocol.NowUTC())
	if err != nil {
		_ = l.Close()
		return nil, nil, err
	}
	return l, rec, nil
}

// loadEntries 读取并解析账本；文件不存在视为空账本（首次启动）。
// origin 逐条归一：旧版账本条目无 origin 字段，一律视为 hub（老条目继续走
// hub 回传链路，行为不变）。
func loadEntries(path string) (map[string]Entry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: 读取账本失败: %w", err)
	}
	entries := map[string]Entry{}
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("ledger: 账本损坏 %s: %w", path, err)
	}
	for id, e := range entries {
		entries[id] = e.normalized()
	}
	return entries, nil
}

// View 返回当前已发布账本快照（先写后见：只可能是已完整落盘的状态）。
// 快照按引用零拷贝返回（性能考量，刻意不做副本），调用方不得修改返回的 map 及其中任何
// Entry——Task/Result 内层的 slice、map 与账本共享底层数据，原地改写即污染已发布状态；
// 需要修改一律走 Accept/MarkXxx 等 Update(mutate) 事务，长期持有或跨 goroutine 传递
// 请自行 Clone（Get 即返回副本）。
func (l *Ledger) View() map[string]Entry { return *l.cur.Load() }

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
	return l.Update(func(m map[string]Entry) error {
		if _, ok := m[t.ID]; ok {
			return ErrDuplicate
		}
		m[t.ID] = Entry{Task: t.Clone(), State: StateAccepted, Origin: origin}
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
	err = l.Update(func(m map[string]Entry) error {
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
	return l.Update(func(m map[string]Entry) error {
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

// MarkFinished 写入终态：accepted/started → finished 并持久化 result
// （accepted 直达 finished 用于未 spawn 即失败的路径，如 policy_denied / expired）。
// 调用方须待成功后再发 task_result（SPEC 4.7）；重复终态返回 ErrWrongState，
// 由调用方按「以先到为准」的幂等语义处理（SPEC 4.3 收敛表）。
//
// local 来源条目落终态即自确认（ResultAcked=true，方案 3.2）：结果消费方就是
// 本机账本，落盘即送达——不经 hub 确认，天然退出重发队列（切换组网后不成批
// 推 hub），终态 24h 清理（CleanupAcked）同样按 first_acked_at 起算收敛。
func (l *Ledger) MarkFinished(taskID string, res protocol.TaskResult) error {
	return l.Update(func(m map[string]Entry) error {
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
	return l.Update(func(m map[string]Entry) error {
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
	return l.Update(func(m map[string]Entry) error {
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

// CleanupAcked 清理 result_acked 超 TerminalRetention 的条目（SPEC 4.6/4.8：
// node 账本 result_acked 条目 24h 后清理）。期限基准为首次确认时刻 first_acked_at
// （不从完成时刻起算；重复 Ack 不更新、重启不重置，零值防御性跳过）。
// closed 条目未被 ack、不在清理范围（SPEC 4.5：冲突审计记录保留）。
// 返回清理条数；由 node 主循环周期性调用（后续 Loop 接线）。
func (l *Ledger) CleanupAcked(now time.Time) (int, error) {
	removed := 0
	err := l.Update(func(m map[string]Entry) error {
		for id, e := range m {
			if !e.ResultAcked || e.Result == nil || e.FirstAckedAt.IsZero() {
				continue
			}
			if e.FirstAckedAt.Add(protocol.TerminalRetention).Before(now) {
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
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrClosed
	}
	l.inflight.Add(1)
	l.mu.Unlock()
	defer l.inflight.Done()

	req := &txnReq{mutate: mutate, reply: make(chan error, 1)}
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
		req.reply <- l.apply(req.mutate)
	}
}

// apply 执行一次先写后见事务；返回 nil 即完成「落盘 + 发布 + Ack」。
func (l *Ledger) apply(mutate func(map[string]Entry) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ledger: 事务 panic（副本已丢弃）: %v", r)
		}
	}()
	cur := *l.cur.Load()
	next := make(map[string]Entry, len(cur)) // 构造新副本；此后任一步失败即整体丢弃
	for id, e := range cur {
		next[id] = e.Clone()
	}
	if err = mutate(next); err != nil {
		return fmt.Errorf("ledger: 事务被拒绝: %w", err)
	}
	if err = l.persist(next); err != nil {
		return err
	}
	if err = l.injectFail(stageAfterRename); err != nil {
		return err
	}
	if err = l.injectFail(stageBeforePublish); err != nil {
		return err
	}
	l.cur.Store(&next) // 原子发布（此前失败路径均不触发布）
	if err = l.injectFail(stageBeforeAck); err != nil {
		return err
	}
	return nil
}

// persist 将新账本写入临时文件并 rename 原子替换（SPEC 4.7：临时文件+rename）。
func (l *Ledger) persist(entries map[string]Entry) error {
	if err := l.injectFail(stageBeforeWrite); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
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
func (l *Ledger) injectFail(stage string) error {
	if l.inject == nil {
		return nil
	}
	return l.inject(stage)
}
