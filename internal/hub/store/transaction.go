package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// stateFileName 旧版 JSON 快照文件名（相对 data_dir；现仅作导入源与导入备份
// 命名基准，hub 常规持久化已迁 SQLite，见 SPEC 4.7/4.9）。
const stateFileName = "hub_state.json"

// 事务故障注入阶段（SPEC 八.2 预设注入点；仅包内测试设置 inject 使用，生产恒为 nil）。
// 阶段名沿 JSON 快照时代的 rename 语义：after_rename = 持久提交成功之后。
const (
	// stageBeforeWrite 写库前（副本已构造、尚未触盘）。
	stageBeforeWrite = "before_write"
	// stageAfterRename 提交（COMMIT）之后（盘上已是新状态、内存尚未发布）。
	stageAfterRename = "after_rename"
	// stageBeforePublish 内存发布前。
	stageBeforePublish = "before_publish"
	// stageBeforeAck 向调用方 Ack 前（内存已发布、调用方尚未得到确认）。
	stageBeforeAck = "before_ack"
)

// ErrClosed Store 已关闭后再提交事务。
var ErrClosed = errors.New("store: 已关闭")

// ErrPoisoned 存储处于「已提交未发布」状态（after_rename/before_publish 崩溃窗口
// 注入模拟）后拒绝后续事务：磁盘与内存基准已分岔，继续用旧内存做 diff 会永久
// 漂移（磁盘残留僵尸行）——必须重开进程以磁盘为准。生产路径 inject 恒为 nil，
// 本状态仅故障注入测试可达（真实对应进程崩溃退出，不存在「后续事务」）。
var ErrPoisoned = errors.New("store: 已提交未发布状态（须重开进程）")

// txnReq 单写队列中的一个事务请求。
type txnReq struct {
	mutate func(*State) error
	reply  chan error
}

// Store hub 持久化存储：单写 goroutine 串行事务，先写后见（SPEC 4.7）。
// 事务路径：克隆当前状态 → mutate 构造新副本 → SQLite 单事务按 diff 增量提交
//（无变化零写库）→ 成功后原子替换内存可见状态并向调用方 Ack；任一步失败则
// 丢弃副本、不发布、不 Ack。
// 内存仍驻留全量 State（View 返回内存镜像），SQLite 只是持久层。
type Store struct {
	dbPath     string
	db         *sql.DB
	lock       *Lock
	ch         chan *txnReq
	cur        atomic.Pointer[State]
	writerDone chan struct{}

	mu       sync.Mutex
	closed   bool
	poisoned bool // 已提交未发布（注入窗口），置位后拒绝后续事务（见 ErrPoisoned）
	inflight sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	// inject 故障注入钩子：按阶段返回非 nil 错误即中断事务（仅包内测试设置，生产为 nil）。
	inject func(stage string) error
}

// Open 打开（或初始化）data_dir 下的 hub 状态存储。
// 先取 data_dir 单实例 flock（冲突即报错），再按 schema 版本分路：
//   - 新空库（user_version=0）：若旧版 hub_state.json 存在则单事务导入全部状态
//     并将 json 备份改名 hub_state.json.imported-<unix秒>；导入（含解析）失败保留
//     json 原样、报错退出。json 不存在则建空 schema 正常启动。
//   - 已初始化库：以 SQLite 为准全量加载（并存 json 不再导入、不改动）。
//   - 库版本高于本代码：拒绝启动（防止旧代码误读新 schema）。
func Open(dir string) (*Store, error) {
	lock, err := AcquireLock(dir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) {
		_ = lock.Release()
		return nil, err
	}
	dbPath := filepath.Join(dir, dbFileName)
	jsonPath := filepath.Join(dir, stateFileName)
	db, err := openDB(dbPath)
	if err != nil {
		return fail(err)
	}
	version, err := schemaState(db)
	if err != nil {
		_ = db.Close()
		return fail(err)
	}
	if version > schemaVersion {
		_ = db.Close()
		return fail(fmt.Errorf("store: 状态库 schema 版本 %d 高于本程序支持的 %d，请升级程序", version, schemaVersion))
	}
	if version == 0 {
		if err := initFreshDB(db, jsonPath); err != nil {
			_ = db.Close()
			return fail(err)
		}
	} else if version < schemaVersion {
		// 旧库迁移：schemaDDL 全 IF NOT EXISTS，单事务补建缺失表并盖新版本号
		//（既有表与数据零触碰；失败整体回滚，下次启动重试）。
		tx, err := db.Begin()
		if err != nil {
			_ = db.Close()
			return fail(fmt.Errorf("store: 开启迁移事务失败: %w", err))
		}
		if err := initSchemaTx(tx); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			return fail(fmt.Errorf("store: 提交迁移事务失败: %w", err))
		}
	}
	st, err := loadStateFromDB(db)
	if err != nil {
		_ = db.Close()
		return fail(err)
	}
	s := &Store{
		dbPath:     dbPath,
		db:         db,
		lock:       lock,
		ch:         make(chan *txnReq),
		writerDone: make(chan struct{}),
	}
	s.cur.Store(st)
	go s.writeLoop()
	return s, nil
}

// initFreshDB 新库初始化：存在旧 JSON 快照则同事务「建表+导入+盖版本」，成功后
// 将 json 备份改名；json 缺失仅建表盖章。解析/写库失败时 json 原样保留（调用方
// 报错退出，下次启动重试导入）。
func initFreshDB(db *sql.DB, jsonPath string) error {
	var imported *State
	if _, err := os.Stat(jsonPath); err == nil {
		st, err := loadState(jsonPath)
		if err != nil {
			return err
		}
		imported = st
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: 探测旧快照失败: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开启初始化事务失败: %w", err)
	}
	if err := initSchemaTx(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if imported != nil {
		if err := importStateTx(tx, imported); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交初始化事务失败: %w", err)
	}
	if imported != nil {
		backup := importedBackupName(jsonPath, time.Now())
		if err := os.Rename(jsonPath, backup); err != nil {
			// 状态已入库：备份改名失败不回滚导入（重开以 SQLite 为准），
			// 但本此启动报错退出，由运维清理同名冲突后重试
			return fmt.Errorf("store: 旧快照备份改名失败（%s → %s）: %w", jsonPath, backup, err)
		}
	}
	return nil
}

// loadState 读取并解析旧版 JSON 快照；文件不存在视为空状态。
func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取 hub 状态快照失败: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("store: hub 状态快照损坏 %s: %w", path, err)
	}
	st.normalize()
	return &st, nil
}

// View 返回当前已发布的只读状态快照（先写后见：只可能是已完整落盘的状态）。
// 快照按引用零拷贝返回（性能考量，刻意不做副本），调用方不得修改返回的 *State 及其任何
// 内层容器——Nodes/Tasks/Mailboxes 与任务体中的 map、slice 均与已发布状态共享底层数据，
// 原地改写即污染已发布状态；需要修改一律走 Update 的 mutate（在私有深拷贝上执行）。
// 需长期持有或跨 goroutine 传递时自行 Clone（State.Clone 为深拷贝）。
func (s *Store) View() *State { return s.cur.Load() }

// SetInjectForTest 设置事务故障注入钩子（跨包测试专用，如 core 注入持久化失败
// 验证不发布不 Ack；须在 Open 完成启动自愈事务后调用；生产零调用）。
func (s *Store) SetInjectForTest(inject func(stage string) error) {
	s.inject = inject
}

// Update 提交一个先写后见事务。mutate 在新状态副本（深拷贝）上执行，可自由增改条目，
// 但不得假设副本的任何改动在事务外可见。
// 返回 nil：状态已落盘并发布——此刻起 View 可见，调用方即可对外 Ack。
// 返回错误分两种语义：
//   - 提交前失败（mutate 拒绝 / 写库 / 提交，含 before_write 注入）：副本已
//     丢弃，内存与磁盘均无任何可见变化（不发布、不 Ack），属普通失败，可直接重试；
//   - 提交后失败（after_rename / before_publish / before_ack，注入模拟真实崩溃窗口）：
//     新状态已提交磁盘乃至已发布内存（before_ack），但调用方未获 Ack——
//     「已提交但确认丢失」，重启后以磁盘为准。调用方须按幂等重试处理，不得假设事务未发生。
//     after_rename / before_publish 失败额外置毒（ErrPoisoned，后续 Update 拒绝）：
//     真实对应进程崩溃退出，不存在后续事务；测试中继续使用须重开 Store。
func (s *Store) Update(mutate func(*State) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.poisoned {
		s.mu.Unlock()
		return ErrPoisoned
	}
	s.inflight.Add(1)
	s.mu.Unlock()
	defer s.inflight.Done()

	req := &txnReq{mutate: mutate, reply: make(chan error, 1)}
	// Close 等 inflight 归零后才关 ch，此处不会向已关闭通道发送
	s.ch <- req
	return <-req.reply
}

// Close 停止接收新事务，排空在途事务后结束单写 goroutine，关闭 SQLite 连接并释放
// flock；幂等。两步清理均尝试执行并合并错误（db 关闭失败不跳过锁释放，避免
// closeOnce 幂等保护下锁资源滞留）。
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.inflight.Wait()
		close(s.ch)
		<-s.writerDone
		dbErr := s.db.Close()
		lockErr := s.lock.Release()
		s.closeErr = errors.Join(dbErr, lockErr)
	})
	return s.closeErr
}

// writeLoop 单写 goroutine：串行处理事务直至通道关闭。
func (s *Store) writeLoop() {
	defer close(s.writerDone)
	for req := range s.ch {
		req.reply <- s.apply(req.mutate)
	}
}

// apply 执行一次先写后见事务；返回 nil 即完成「落盘 + 发布 + Ack」。
func (s *Store) apply(mutate func(*State) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("store: 事务 panic（副本已丢弃）: %v", r)
		}
	}()
	old := s.cur.Load()
	next := old.Clone() // 构造新副本；此后任一步失败即整体丢弃
	if err = mutate(next); err != nil {
		return fmt.Errorf("store: 事务被拒绝: %w", err)
	}
	if err = s.persist(old, next); err != nil {
		return err
	}
	if err = s.injectFail(stageAfterRename); err != nil {
		s.mu.Lock()
		s.poisoned = true // 盘已提交、内存未发布：基准分岔，拒绝后续事务（ErrPoisoned）
		s.mu.Unlock()
		return err
	}
	if err = s.injectFail(stageBeforePublish); err != nil {
		s.mu.Lock()
		s.poisoned = true
		s.mu.Unlock()
		return err
	}
	s.cur.Store(next) // 原子发布（此前失败路径均不触发布）
	if err = s.injectFail(stageBeforeAck); err != nil {
		return err
	}
	return nil
}

// persist 按增量 diff 将新状态写入 SQLite：对比 old/next，仅 UPSERT/DELETE 变化
// 条目，单事务提交；diff 为空零写库（无变化事务发布照常、不触盘）。
func (s *Store) persist(old, next *State) error {
	if err := s.injectFail(stageBeforeWrite); err != nil {
		return err
	}
	diffs, err := diffState(old, next)
	if err != nil {
		return err
	}
	if len(diffs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开启事务失败: %w", err)
	}
	if err := applyDiffs(tx, diffs); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交事务失败: %w", err)
	}
	return nil
}

// injectFail 调用注入钩子；未设置时恒返回 nil。
func (s *Store) injectFail(stage string) error {
	if s.inject == nil {
		return nil
	}
	return s.inject(stage)
}
