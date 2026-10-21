package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// stateFileName hub 状态快照文件名（相对 data_dir）。
const stateFileName = "hub_state.json"

// tmpFileName 落盘临时文件名：同目录写入后 rename，保证替换原子（SPEC 4.7）。
const tmpFileName = stateFileName + ".tmp"

// 事务故障注入阶段（SPEC 八.2 预设注入点；仅包内测试设置 inject 使用，生产恒为 nil）。
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

// ErrClosed Store 已关闭后再提交事务。
var ErrClosed = errors.New("store: 已关闭")

// txnReq 单写队列中的一个事务请求。
type txnReq struct {
	mutate func(*State) error
	reply  chan error
}

// Store hub 持久化存储：单写 goroutine 串行事务，先写后见（SPEC 4.7）。
// 事务路径：克隆当前状态 → mutate 构造新副本 → 临时文件+rename 落盘 →
// 成功后原子替换内存可见状态并向调用方 Ack；任一步失败则丢弃副本、不发布、不 Ack。
type Store struct {
	path       string
	tmp        string
	lock       *Lock
	ch         chan *txnReq
	cur        atomic.Pointer[State]
	writerDone chan struct{}

	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	// inject 故障注入钩子：按阶段返回非 nil 错误即中断事务（仅包内测试设置，生产为 nil）。
	inject func(stage string) error
}

// Open 打开（或初始化）data_dir 下的 hub 状态存储。
// 先取 data_dir 单实例 flock（冲突即报错），再加载已有快照；快照不存在则从空状态开始。
func Open(dir string) (*Store, error) {
	lock, err := AcquireLock(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		path:       filepath.Join(dir, stateFileName),
		tmp:        filepath.Join(dir, tmpFileName),
		lock:       lock,
		ch:         make(chan *txnReq),
		writerDone: make(chan struct{}),
	}
	st, err := loadState(s.path)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	s.cur.Store(st)
	go s.writeLoop()
	return s, nil
}

// loadState 读取并解析快照；文件不存在视为空状态（首次启动）。
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

// Update 提交一个先写后见事务。mutate 在新状态副本（深拷贝）上执行，可自由增改条目，
// 但不得假设副本的任何改动在事务外可见。
// 返回 nil：状态已落盘并发布——此刻起 View 可见，调用方即可对外 Ack。
// 返回错误分两种语义：
//   - rename 前失败（mutate 拒绝 / 写临时文件 / rename，含 before_write 注入）：副本已
//     丢弃，内存与磁盘均无任何可见变化（不发布、不 Ack），属普通失败，可直接重试；
//   - rename 后失败（after_rename / before_publish / before_ack，注入模拟真实崩溃窗口）：
//     新状态已提交磁盘（rename 成）乃至已发布内存（before_ack），但调用方未获 Ack——
//     「已提交但确认丢失」，重启后以磁盘为准。调用方须按幂等重试处理，不得假设事务未发生。
func (s *Store) Update(mutate func(*State) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.inflight.Add(1)
	s.mu.Unlock()
	defer s.inflight.Done()

	req := &txnReq{mutate: mutate, reply: make(chan error, 1)}
	// Close 等 inflight 归零后才关 ch，此处不会向已关闭通道发送
	s.ch <- req
	return <-req.reply
}

// Close 停止接收新事务，排空在途事务后结束单写 goroutine 并释放 flock；幂等。
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.inflight.Wait()
		close(s.ch)
		<-s.writerDone
		s.closeErr = s.lock.Release()
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
	next := s.cur.Load().Clone() // 构造新副本；此后任一步失败即整体丢弃
	if err = mutate(next); err != nil {
		return fmt.Errorf("store: 事务被拒绝: %w", err)
	}
	if err = s.persist(next); err != nil {
		return err
	}
	if err = s.injectFail(stageAfterRename); err != nil {
		return err
	}
	if err = s.injectFail(stageBeforePublish); err != nil {
		return err
	}
	s.cur.Store(next) // 原子发布（此前失败路径均不触发布）
	if err = s.injectFail(stageBeforeAck); err != nil {
		return err
	}
	return nil
}

// persist 将新状态写入临时文件并 rename 原子替换快照（SPEC 4.7：临时文件+rename）。
func (s *Store) persist(st *State) error {
	if err := s.injectFail(stageBeforeWrite); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("store: 写临时文件失败: %w", err)
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: 写临时文件失败: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: 刷盘失败: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("store: 关闭临时文件失败: %w", err)
	}
	if err = os.Rename(s.tmp, s.path); err != nil {
		return fmt.Errorf("store: rename 落盘失败: %w", err)
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
