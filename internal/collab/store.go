// 空间协作存储（W04-01 RFC §二）：单文件原子快照 + 内嵌操作回执表（journal）+
// pending 意图（outbox）。先写后见：所有写操作在深拷贝副本上执行 → 副本整体
// 原子落盘（独占 tmp + fsync + rename + 目录 fsync 尽力而为，0600，UTC）→ 成功
// 才替换内存可见态；校验拒绝或落盘失败副本整体丢弃，内存零变更（N05 零虚假
// 接受）。加载失败拒绝带病启动。时间一律 UTC。
package collab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// OpReceipt 操作回执（journal 条目）：操作 ID 幂等的依据。同 ID 同文回放原
// result（零执行零扣减）；同 ID 异文冲突。
type OpReceipt struct {
	OpID       string          `json:"op_id"`
	Kind       string          `json:"kind"`
	Hash       string          `json:"hash"` // kind + 请求体内容指纹
	Result     json.RawMessage `json:"result"`
	RecordedAt time.Time       `json:"recorded_at"`
}

// state 快照根结构。Intents 按 spaceID 分组追加（收敛不改写历史行，只改
// Status/UpdatedAt）；SessionOwners 全局会话归属（一线程唯一空间）；Ops 回执表
// 滚动上限淘汰；TermSeq 服务端 term 分配计数（永不复用的结构保证）；Messages
// 按 spaceID 分组的消息账本（W04-02，滚动保留最新 MaxMessagesPerSpace 条——
// 加法字段：旧快照缺省为空，加载 nil 守卫补齐，不升 stateVersion）。
type state struct {
	Version       int                         `json:"version"`
	Spaces        map[string]*Space           `json:"spaces"`
	Intents       map[string][]DispatchIntent `json:"intents"`
	SessionOwners map[string]string           `json:"session_owners"`
	Ops           map[string]OpReceipt        `json:"ops"`
	TermSeq       map[string]uint64           `json:"term_seq"`
	Messages      map[string][]SpaceMessage   `json:"messages,omitempty"`
}

func newState() *state {
	return &state{
		Version:       stateVersion,
		Spaces:        map[string]*Space{},
		Intents:       map[string][]DispatchIntent{},
		SessionOwners: map[string]string{},
		Ops:           map[string]OpReceipt{},
		TermSeq:       map[string]uint64{},
		Messages:      map[string][]SpaceMessage{},
	}
}

// clone 深拷贝（指针/切片逐一复制，副本变更不污染已发布状态）。
func (st *state) clone() *state {
	n := newState()
	n.Version = st.Version
	for id, sp := range st.Spaces {
		n.Spaces[id] = cloneSpace(sp)
	}
	for id, list := range st.Intents {
		n.Intents[id] = append(make([]DispatchIntent, 0, len(list)), list...)
	}
	for sid, spid := range st.SessionOwners {
		n.SessionOwners[sid] = spid
	}
	for id, r := range st.Ops {
		n.Ops[id] = r
	}
	for id, t := range st.TermSeq {
		n.TermSeq[id] = t
	}
	for id, list := range st.Messages {
		n.Messages[id] = append(make([]SpaceMessage, 0, len(list)), list...)
	}
	return n
}

func cloneSpace(sp *Space) *Space {
	c := *sp
	c.Members = append(make([]Member, 0, len(sp.Members)), sp.Members...)
	c.Policy.ClaimPrincipals = append(make([]string, 0, len(sp.Policy.ClaimPrincipals)), sp.Policy.ClaimPrincipals...)
	c.Events = append(make([]SpaceEvent, 0, len(sp.Events)), sp.Events...)
	if sp.ArchivedAt != nil {
		t := *sp.ArchivedAt
		c.ArchivedAt = &t
	}
	for i := range c.Members {
		if c.Members[i].BoundAt != nil {
			t := *c.Members[i].BoundAt
			c.Members[i].BoundAt = &t
		}
	}
	return &c
}

// Options 装配项。
type Options struct {
	// Now 时钟源（测试注入 fake 时钟）；nil = protocol.NowUTC（全工程 UTC 基准）。
	Now func() time.Time
	// Log 运维日志；nil = 静默。
	Log Logger
	// LeaseTTL 接管/续约的租约时长；0 = DefaultLeaseTTL；负值装配拒绝。
	LeaseTTL time.Duration
	// MaxReceipts 操作回执滚动上限；0 = DefaultMaxReceipts；负值装配拒绝。
	MaxReceipts int
	// Hooks 策略接口实现（W05-01；装配期一次性注入——Open 后不可换，规避运行期
	// 热替换的判定/落账一致性问题；nil = 完成类动作一律拒绝，W04 只开放放弃定版）。
	Hooks StrategyHooks
}

// Store 空间协作存储：互斥串行 + 先写后见 + 操作 ID 幂等。
type Store struct {
	path        string
	tmp         string
	now         func() time.Time
	log         Logger
	leaseTTL    time.Duration
	maxReceipts int
	hooks       StrategyHooks // 装配期定形只读（Open 后不可变，事务内无锁读取）

	mu     sync.Mutex
	st     *state
	closed bool
	// failPersist 落盘故障注入（仅包内测试；生产恒 nil）。
	failPersist func() error
}

// Open 打开（或初始化）数据目录下的空间存储。快照不存在从空状态起步；存在但
// 损坏/版本未知 → 报错（拒绝带病启动，同 W02-01 mcp_access 口径）。
func Open(dir string, opt Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("collab: 数据目录为空")
	}
	if opt.LeaseTTL < 0 {
		return nil, fmt.Errorf("collab: LeaseTTL 为负: %s", opt.LeaseTTL)
	}
	if opt.MaxReceipts < 0 {
		return nil, fmt.Errorf("collab: MaxReceipts 为负: %d", opt.MaxReceipts)
	}
	now := opt.Now
	if now == nil {
		now = protocol.NowUTC
	}
	log := opt.Log
	if log == nil {
		log = nopLogger{}
	}
	ttl := opt.LeaseTTL
	if ttl == 0 {
		ttl = DefaultLeaseTTL
	}
	maxReceipts := opt.MaxReceipts
	if maxReceipts == 0 {
		maxReceipts = DefaultMaxReceipts
	}
	path := filepath.Join(dir, fileName)
	st, err := loadState(path)
	if err != nil {
		return nil, err
	}
	return &Store{
		path: path, tmp: path + ".tmp",
		now: now, log: log, leaseTTL: ttl, maxReceipts: maxReceipts,
		hooks: opt.Hooks,
		st:    st,
	}, nil
}

// loadState 读取并解析快照；不存在视为空状态。
func loadState(path string) (*state, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newState(), nil
		}
		return nil, fmt.Errorf("collab: 读取空间快照失败: %w", err)
	}
	st := newState()
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("collab: 空间快照损坏 %s: %w（拒绝带病启动；请人工检查或移除该文件）", path, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("collab: 空间快照版本未知 %s: got %d want %d（拒绝猜测意图）", path, st.Version, stateVersion)
	}
	if st.Spaces == nil {
		st.Spaces = map[string]*Space{}
	}
	if st.Intents == nil {
		st.Intents = map[string][]DispatchIntent{}
	}
	if st.SessionOwners == nil {
		st.SessionOwners = map[string]string{}
	}
	if st.Ops == nil {
		st.Ops = map[string]OpReceipt{}
	}
	if st.TermSeq == nil {
		st.TermSeq = map[string]uint64{}
	}
	if st.Messages == nil {
		st.Messages = map[string][]SpaceMessage{}
	}
	return st, nil
}

// Close 关闭存储：此后写入一律拒绝（幂等）。无后台 goroutine；读接口关闭后
// 仍可用。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// apply 操作事务通用骨架（RFC §二）：
//  1. 重放判定先行——同 OpID 同文（kind+内容指纹）返回原回执（dup=true，零执行
//     零扣减）；同 OpID 异文 ErrRequestConflict。重放命中先于一切校验（原请求
//     重放返回旧回执，不受当前版本/状态影响）。
//  2. 未命中：深拷贝副本上执行 fn（全部校验与变更）→ fn 失败副本整体丢弃；
//  3. 成功：回执写入副本 → 副本整体原子落盘 → 落盘成功才替换内存可见态；
//     落盘失败错误如实上抛（无回执、内存不变，重试重新执行）。
//
// OpID 为空拒绝（所有写操作必须有稳定请求标识）。
func (s *Store) apply(opID, kind string, payload any, fn func(st *state) (any, error)) ([]byte, bool, error) {
	if opID == "" {
		return nil, false, errf(ErrInvalid, "request_id 必填（写操作稳定请求标识）")
	}
	hash := opHash(kind, payload)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, ErrClosed
	}
	if r, ok := s.st.Ops[opID]; ok {
		if r.Kind == kind && r.Hash == hash {
			return r.Result, true, nil
		}
		return nil, false, errf(ErrRequestConflict, "request_id %s 已被 %s 操作占用（内容指纹不符）", opID, r.Kind)
	}
	next := s.st.clone()
	res, err := fn(next)
	if err != nil {
		return nil, false, err // 副本整体丢弃：内存零变更、无回执
	}
	raw, merr := json.Marshal(res)
	if merr != nil {
		return nil, false, fmt.Errorf("collab: 操作结果序列化失败: %w", merr)
	}
	next.Ops[opID] = OpReceipt{OpID: opID, Kind: kind, Hash: hash, Result: raw, RecordedAt: s.now()}
	trimOps(next.Ops, s.maxReceipts)
	if err := s.persist(next); err != nil {
		return nil, false, err // 无回执、内存不变：不虚报成功（N05）
	}
	s.st = next
	return raw, false, nil
}

// opHash 操作内容指纹（kind + payload 规范 JSON 的 SHA-256）。
func opHash(kind string, payload any) string {
	b, err := json.Marshal(payload)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", payload))
	}
	sum := sha256.Sum256(append([]byte(kind+"|"), b...))
	return hex.EncodeToString(sum[:])
}

// trimOps 回执表滚动淘汰（超限按 RecordedAt 丢最旧；O(n) 选择足够——淘汰至多
// 一个，且仅在越限时发生）。
func trimOps(ops map[string]OpReceipt, limit int) {
	if len(ops) <= limit {
		return
	}
	var oldestID string
	var oldest time.Time
	first := true
	for id, r := range ops {
		if first || r.RecordedAt.Before(oldest) || (r.RecordedAt.Equal(oldest) && id < oldestID) {
			oldestID, oldest, first = id, r.RecordedAt, false
		}
	}
	if oldestID != "" {
		delete(ops, oldestID)
	}
}

// persist 原子落盘（独占 tmp + fsync + rename + 目录 fsync 尽力而为；0600）。
// failPersist 为包内测试故障注入点（生产恒 nil）。
func (s *Store) persist(st *state) error {
	if s.failPersist != nil {
		if err := s.failPersist(); err != nil {
			return fmt.Errorf("collab: 落盘失败（注入）: %w", err)
		}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(s.tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// 上次落盘崩溃残留的 tmp：清掉重试一次（磁盘旧正本未动，安全）。
		if rmErr := os.Remove(s.tmp); rmErr != nil {
			return fmt.Errorf("collab: 清理残留临时文件失败: %w", rmErr)
		}
		f, err = os.OpenFile(s.tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return fmt.Errorf("collab: 写临时文件失败: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(s.tmp)
		return fmt.Errorf("collab: 写临时文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(s.tmp)
		return fmt.Errorf("collab: 刷盘失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(s.tmp)
		return fmt.Errorf("collab: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(s.tmp, s.path); err != nil {
		_ = os.Remove(s.tmp)
		return fmt.Errorf("collab: rename 落盘失败: %w", err)
	}
	if d, derr := os.Open(filepath.Dir(s.path)); derr == nil {
		if serr := d.Sync(); serr != nil {
			s.log.Printf("collab: 空间快照目录 fsync 失败（尽力而为，rename 已完成）: %v", serr)
		}
		_ = d.Close()
	}
	return nil
}

// ---------- 只读查询（快照读，返回深拷贝） ----------

// Space 查单个空间（不存在 ok=false）。
func (s *Store) Space(id string) (Space, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.st.Spaces[id]
	if !ok {
		return Space{}, false
	}
	return *cloneSpace(sp), true
}

// SpaceView 空间视图（附在途投影）。
func (s *Store) SpaceView(id string) (SpaceView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.st.Spaces[id]
	if !ok {
		return SpaceView{}, false
	}
	return spaceView(*cloneSpace(sp), s.st.Intents[id]), true
}

// Spaces 全量空间列表（创建时刻升序，同时刻按 ID 稳定排序）。
func (s *Store) Spaces() []Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Space, 0, len(s.st.Spaces))
	for _, sp := range s.st.Spaces {
		out = append(out, *cloneSpace(sp))
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && spaceLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func spaceLess(a, b Space) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// Intents 空间执行意图列表（登记序）。
func (s *Store) Intents(spaceID string) []DispatchIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.st.Intents[spaceID]
	return append(make([]DispatchIntent, 0, len(list)), list...)
}

// InFlightCount 空间在途意图数。
func (s *Store) InFlightCount(spaceID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, it := range s.st.Intents[spaceID] {
		if it.Status.InFlight() {
			n++
		}
	}
	return n
}

// RecoverIntents 全部在途意图（W04-02 对账入口：进程恢复按实物收敛，本包不派生
// 任何执行、不自动重跑）。
func (s *Store) RecoverIntents() []DispatchIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DispatchIntent, 0)
	for _, list := range s.st.Intents {
		for _, it := range list {
			if it.Status.InFlight() {
				out = append(out, it)
			}
		}
	}
	return out
}

// IntentByRequest 按请求 ID 查空间意图（W04-02 派发受理重放预检与
// GET /dispatches/{request_id} 查询的数据源；登记序扫描，意图列表有界）。
func (s *Store) IntentByRequest(spaceID, requestID string) (DispatchIntent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.st.Intents[spaceID] {
		if it.RequestID == requestID {
			return it, true
		}
	}
	return DispatchIntent{}, false
}

// SpaceMessages 空间消息增量读（since 之后的消息升序；返回裁剪位置——窗口外
// 更早消息已滚动淘汰，调用方以 pruned_before 校正游标，同旧群信箱约定）。
func (s *Store) SpaceMessages(spaceID string, since int64) (msgs []SpaceMessage, prunedBefore int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.st.Messages[spaceID]
	if len(list) > 0 {
		prunedBefore = list[0].Seq - 1 // 窗口首条之前的 seq 已淘汰
	}
	for _, m := range list {
		if m.Seq > since {
			msgs = append(msgs, m)
		}
	}
	return msgs, prunedBefore
}

// SessionOwnerSpace 会话归属空间查询（一线程唯一归属；sessionop W04-02 接线的
// 判定源：命中即拒绝普通 session_send，指向 space dispatch，RFC §1.4 方案 A）。
func (s *Store) SessionOwnerSpace(sessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spid, ok := s.st.SessionOwners[sessionID]
	return spid, ok
}

// maxMessagesPerSpace 消息滚动上限生效值（var 仅为测试缩小——生产恒等于
// MaxMessagesPerSpace；同账本清理节拍先例，不做配置面）。
var maxMessagesPerSpace = MaxMessagesPerSpace

// leaseTTLFor 单次授予/续约时长：显式秒数（>0）截到上限，否则默认 TTL。
func (s *Store) leaseTTLFor(seconds int) (time.Duration, error) {
	if seconds < 0 {
		return 0, errf(ErrInvalid, "lease_seconds 为负: %d", seconds)
	}
	if seconds == 0 {
		return s.leaseTTL, nil
	}
	if seconds > MaxLeaseSeconds {
		return 0, errf(ErrInvalid, "lease_seconds %d 超上限 %d（租约必须可过期）", seconds, MaxLeaseSeconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
