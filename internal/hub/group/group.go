// Package group 实现 hub 侧群聊/协作空间存储（GROUP-RFC 六 MVP 切法）：
// Group 一等实体 + 组内单调 seq 消息账本 + 发言预算护栏。
//
// 持久化为独立快照文件 data_dir/groups.json（GROUP-RFC 三：独立轻量信箱，
// 不与任务账本 hub_state.json 混用）。写路径对齐 SPEC 4.7 先写后见思想：
// 校验并构造变更 → 现状态整体落盘（临时文件+rename 原子替换）→ 成功后才
// 改内存可见状态；落盘失败则内存不变、调用方得到错误。并发安全由互斥锁
// 串行化（MVP 单进程内 mutex 足够；data_dir 单实例由 hub store 的 flock 兜底）。
package group

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 常量（GROUP-RFC 二/三）。
const (
	// fileName 群状态快照文件名（相对 data_dir）。
	fileName = "groups.json"
	// tmpFileName 落盘临时文件名：同目录写入后 rename，保证替换原子（SPEC 4.7）。
	tmpFileName = fileName + ".tmp"

	// GroupIDPrefix 群 ID 前缀。
	GroupIDPrefix = "g-"
	// MsgIDPrefix 群消息 ID 前缀。
	MsgIDPrefix = "gm-"

	// RefluxFrom 任务终态回流消息的发言者标识（hub 自身注记，非成员发言）。
	RefluxFrom = "hub"

	// MaxBodyBytes 单条消息体上限：8KiB（GROUP-RFC 三 独立信箱约定，按字节实测）。
	MaxBodyBytes = 8 << 10
	// MaxGroupMessages 群消息滚动窗口：组内只保留最新 200 条（GROUP-RFC 三
	// 独立信箱约定），超出从最小 seq 起丢弃最旧，防 groups.json 无限膨胀。
	MaxGroupMessages = 200
	// DefaultMaxRounds 默认发言预算：agent 消息最多 20 条（MVP 简化护栏，
	// GROUP-RFC 四.3 预算代码强制的最小集）。
	DefaultMaxRounds = 20
)

// 群生命周期（GROUP-RFC 二）：concluded 终态不可回退（对齐任务状态机哲学）；
// forming 为 V1 预留态，MVP 创建即 discussing。
const (
	StatusForming    = "forming"
	StatusDiscussing = "discussing"
	StatusConcluded  = "concluded"
)

// 消息类别（GROUP-RFC 二）。agent 消息计入发言预算；user/system 不计。
const (
	KindUser   = "user"
	KindAgent  = "agent"
	KindSystem = "system"
)

// 错误哨兵（api 层据此映射 HTTP 状态与错误码）。
var (
	// ErrNotFound 群不存在。
	ErrNotFound = errors.New("group: 群不存在")
	// ErrConcluded 群已结束：concluded 终态拒收消息（非法流转拒绝）。
	ErrConcluded = errors.New("group: 群已结束（concluded），拒收消息")
	// ErrBudgetExhausted 发言预算用尽：agent 消息数已达 budget.max_rounds。
	ErrBudgetExhausted = errors.New("group: 发言预算已用尽（budget_exhausted）")
	// ErrBodyTooLarge 消息体超 8KiB 上限。
	ErrBodyTooLarge = errors.New("group: 消息体超过 8KiB 上限")
	// ErrInvalid 参数非法（goal/host/from 为空、kind/body 非法等）。
	ErrInvalid = errors.New("group: 参数非法")
	// ErrClosed Store 已关闭后再写入。
	ErrClosed = errors.New("group: store 已关闭")
)

// Budget 群预算（MVP 仅发言轮数一维）。
type Budget struct {
	// MaxRounds agent 消息总数上限（kind=agent 计数）；创建时默认 DefaultMaxRounds。
	MaxRounds int `json:"max_rounds"`
}

// Group 群聊/协作空间（GROUP-RFC 二 MVP 字段集）。
type Group struct {
	ID        string    `json:"group_id"`
	Goal      string    `json:"goal"`
	Status    string    `json:"status"`
	Host      string    `json:"host"`
	Members   []string  `json:"members"`
	Budget    Budget    `json:"budget"`
	CreatedAt time.Time `json:"created_at"`
	// ClosedBy/ClosedAt 首次关闭审计；重复关闭幂等，不改写首次记录。
	ClosedBy string     `json:"closed_by,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// TaskRefs 群内任务引用列表（GROUP-RFC 六「结论转正式任务」：追加去重，
	// 随快照持久化；终态回流据此反查任务归属群）。
	TaskRefs []string `json:"task_refs,omitempty"`
}

// GroupMessage 群消息（GROUP-RFC 二）：seq 由 hub 组内单调分配，是历史
// 补齐一致性的唯一锚点（思想同 epoch）。
type GroupMessage struct {
	MsgID     string    `json:"msg_id"`
	GroupID   string    `json:"group_id"`
	Seq       int64     `json:"seq"`
	From      string    `json:"from"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// state 快照根结构。Messages 按 seq 升序追加，超 MaxGroupMessages 滚动裁剪
// （seq 单调账本 Seqs 不回退，窗口只在头部留缺口）。
type state struct {
	Groups   map[string]Group          `json:"groups"`
	Messages map[string][]GroupMessage `json:"messages"`
	// Seqs group_id → 已分配的最大 seq（重启后继续单调，不受裁剪影响）。
	Seqs map[string]int64 `json:"seqs"`
	// AgentMsgs group_id → 已入账 agent 消息总数（发言预算计量口径）。独立于
	// 滚动窗口累计：窗口只保最新 200 条，旧 agent 消息被裁不能让预算「失忆」
	// 重新放行。随快照持久化。
	AgentMsgs map[string]int `json:"agent_msgs,omitempty"`
	// Refluxed 已回流任务集合：task_id → true（终态回流幂等锚，随快照持久化，
	// 重复终态事件/重启重放都只回流一次）。
	Refluxed map[string]bool `json:"refluxed,omitempty"`
}

func newState() *state {
	return &state{
		Groups:    make(map[string]Group),
		Messages:  make(map[string][]GroupMessage),
		Seqs:      make(map[string]int64),
		AgentMsgs: make(map[string]int),
		Refluxed:  make(map[string]bool),
	}
}

// Store 群聊存储：互斥锁串行读写 + 先写后见落盘。
type Store struct {
	path string
	tmp  string

	mu     sync.Mutex
	st     *state
	closed bool
}

// Open 打开（或初始化）data_dir 下的群状态存储；快照不存在则从空状态开始。
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, fileName)
	st, err := loadState(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, tmp: filepath.Join(dir, tmpFileName), st: st}, nil
}

// loadState 读取并解析快照；文件不存在视为空状态（首次启动）。
func loadState(path string) (*state, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("group: 读取群状态快照失败: %w", err)
	}
	st := newState()
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("group: 群状态快照损坏 %s: %w", path, err)
	}
	// 预算下界护栏：快照是唯一可能注入 MaxRounds<1 的入口（创建路径恒为
	// DefaultMaxRounds 常量）。0/负值会使预算恒真判耗尽，故加载时归一为默认
	// 20；归一后的内存状态随下次先写后见落盘覆盖手改值。
	for id, g := range st.Groups {
		if g.Budget.MaxRounds < 1 {
			g.Budget.MaxRounds = DefaultMaxRounds
			st.Groups[id] = g
		}
	}
	// 旧快照迁移：滚动裁剪落地前快照无 agent_msgs 字段（零值），按现存消息
	// 重新累计——旧快照从未裁剪、现存消息即全部历史，计数精确；迁移结果随
	// 下次先写后见落盘持久化。
	for id := range st.Groups {
		if st.AgentMsgs[id] == 0 {
			st.AgentMsgs[id] = countAgent(st.Messages[id])
		}
	}
	return st, nil
}

// Close 标记关闭：此后写入一律拒绝（幂等）。无后台 goroutine，无需排空；
// 读接口在关闭后仍可用（返回已落盘状态）。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// mutate 先写后见事务（SPEC 4.7 模式）：在深拷贝副本上执行变更 → 副本整体
// 落盘（临时文件+rename）→ 成功后原子替换内存可见状态；校验拒绝或落盘失败
// 则副本整体丢弃，内存零变更，调用方得到错误。
func (s *Store) mutate(mutateFn func(next *state) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	next := s.st.clone()
	if err := mutateFn(next); err != nil {
		return err
	}
	if err := s.persistState(next); err != nil {
		return err
	}
	s.st = next
	return nil
}

// clone 深拷贝状态（Group 的 Members/TaskRefs 经 cloneGroup 一并复制；string
// 等不可变值共享安全），消息切片 cap==len，追加时分配新底层数组，不污染已
// 发布状态。
func (st *state) clone() *state {
	n := newState()
	for id, g := range st.Groups {
		n.Groups[id] = cloneGroup(g)
	}
	for id, msgs := range st.Messages {
		n.Messages[id] = append(make([]GroupMessage, 0, len(msgs)), msgs...)
	}
	for id, sq := range st.Seqs {
		n.Seqs[id] = sq
	}
	for id, n2 := range st.AgentMsgs {
		n.AgentMsgs[id] = n2
	}
	for id, ok := range st.Refluxed {
		n.Refluxed[id] = ok
	}
	return n
}

// cloneGroup 深拷贝群对象：Members/TaskRefs 切片逐一复制（GroupMessage 等其余
// 出口均为值类型，结构体赋值即拷贝）。读出口（GetGroup/ListGroups/CloseGroup/
// CreateGroup 返回值）一律经此发布——否则调用方拿到与内部状态共享底层数组的
// 切片引用，可绕过锁 append/改写污染 store（审查例证）。
func cloneGroup(g Group) Group {
	g.Members = append(make([]string, 0, len(g.Members)), g.Members...)
	g.TaskRefs = append(make([]string, 0, len(g.TaskRefs)), g.TaskRefs...)
	return g
}

// appendAndTrim 追加消息并执行 MaxGroupMessages 滚动裁剪（GROUP-RFC 三 独立
// 信箱 200 条）：超窗口从最小 seq 起丢弃最旧，只保留最新 200 条。seq 账本
// （Seqs）与 agent 预算计数（AgentMsgs）独立累计，不随裁剪回退。仅在持有锁的
// mutate 事务内调用；裁剪随事务落盘，groups.json 不无限膨胀。落盘失败的副本
// 整体丢弃，裁剪同样不生效（与写路径同一原子性）。
func (st *state) appendAndTrim(groupID string, msg GroupMessage) {
	msgs := append(st.Messages[groupID], msg)
	if len(msgs) > MaxGroupMessages {
		msgs = append(make([]GroupMessage, 0, MaxGroupMessages), msgs[len(msgs)-MaxGroupMessages:]...)
	}
	st.Messages[groupID] = msgs
}

// persistState 将新状态副本写入临时文件并 rename 原子替换快照（SPEC 4.7：
// 临时文件+rename；对齐 internal/hub/store 落盘惯例，含 fsync）。
func (s *Store) persistState(st *state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("group: 写临时文件失败: %w", err)
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("group: 写临时文件失败: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("group: 刷盘失败: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("group: 关闭临时文件失败: %w", err)
	}
	if err = os.Rename(s.tmp, s.path); err != nil {
		return fmt.Errorf("group: rename 落盘失败: %w", err)
	}
	return nil
}

// CreateGroup 建群：goal/host 必填，创建即 discussing，预算默认 20 轮。
// members 拷贝存储（防调用方别名改写）。
func (s *Store) CreateGroup(goal, host string, members []string) (Group, error) {
	goal = strings.TrimSpace(goal)
	host = strings.TrimSpace(host)
	if goal == "" || host == "" {
		return Group{}, fmt.Errorf("%w: goal 与 host 必填", ErrInvalid)
	}
	ms := append([]string(nil), members...)
	if ms == nil {
		ms = []string{}
	}
	g := Group{
		ID:        GroupIDPrefix + protocol.NewMessageID(),
		Goal:      goal,
		Status:    StatusDiscussing,
		Host:      host,
		Members:   ms,
		Budget:    Budget{MaxRounds: DefaultMaxRounds},
		CreatedAt: time.Now().UTC(),
	}
	err := s.mutate(func(next *state) error {
		next.Groups[g.ID] = g
		next.Messages[g.ID] = nil
		next.Seqs[g.ID] = 0
		return nil
	})
	if err != nil {
		return Group{}, err
	}
	return cloneGroup(g), nil
}

// PostMessage 发言：校验 → 预算/终态护栏 → 分配组内单调 seq → 滚动裁剪
// （超 MaxGroupMessages 丢最旧）→ 落盘 → 返回。
// 落盘失败不占用 seq、不计预算（内存未变，重试重新分配）。
func (s *Store) PostMessage(groupID, from, kind, body string) (GroupMessage, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return GroupMessage{}, fmt.Errorf("%w: from 必填", ErrInvalid)
	}
	switch kind {
	case KindUser, KindAgent, KindSystem:
	default:
		return GroupMessage{}, fmt.Errorf("%w: kind 须为 user|agent|system", ErrInvalid)
	}
	if strings.TrimSpace(body) == "" {
		return GroupMessage{}, fmt.Errorf("%w: body 不能为空", ErrInvalid)
	}
	if len(body) > MaxBodyBytes {
		return GroupMessage{}, fmt.Errorf("%w: %d 字节 > %d 字节", ErrBodyTooLarge, len(body), MaxBodyBytes)
	}
	var msg GroupMessage
	err := s.mutate(func(next *state) error {
		g, ok := next.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		if g.Status == StatusConcluded {
			return ErrConcluded
		}
		// 预算按 AgentMsgs 独立累计判定（非现存消息计数）：滚动窗口裁掉旧
		// agent 消息不得让预算「失忆」重新放行。
		if kind == KindAgent && next.AgentMsgs[groupID] >= g.Budget.MaxRounds {
			return ErrBudgetExhausted
		}
		msg = GroupMessage{
			MsgID:     MsgIDPrefix + protocol.NewMessageID(),
			GroupID:   groupID,
			Seq:       next.Seqs[groupID] + 1,
			From:      from,
			Kind:      kind,
			Body:      body,
			CreatedAt: time.Now().UTC(),
		}
		if kind == KindAgent {
			next.AgentMsgs[groupID]++
		}
		next.appendAndTrim(groupID, msg)
		next.Seqs[groupID] = msg.Seq
		return nil
	})
	if err != nil {
		return GroupMessage{}, err
	}
	return msg, nil
}

// ListMessages 增量拉取：返回 seq > sinceSeq 的消息（升序）。第二返回值
// prunedBeforeSeq 为滚动裁剪已丢消息的最大 seq（即现存最旧消息 Seq-1；从未
// 裁剪时为 0）——sinceSeq < prunedBeforeSeq 说明请求区间有拉不回的缺口，调用方
// 应按 prunedBeforeSeq 对齐游标（REST 响应以 pruned_before_seq 字段透出）。
// 群不存在报 ErrNotFound；sinceSeq<0 按 0 处理（全量补齐语义，GROUP-RFC 三
// group_sync）。
func (s *Store) ListMessages(groupID string, sinceSeq int64) ([]GroupMessage, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Groups[groupID]; !ok {
		return nil, 0, ErrNotFound
	}
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	msgs := s.st.Messages[groupID]
	// 裁剪只从头部（最小 seq）丢、seq 从 1 连续分配，故现存最旧 Seq-1 即已裁
	// 区间上界，无需额外持久化游标。
	var prunedBefore int64
	if len(msgs) > 0 {
		prunedBefore = msgs[0].Seq - 1
	}
	out := make([]GroupMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Seq > sinceSeq {
			out = append(out, m)
		}
	}
	return out, prunedBefore, nil
}

// CloseGroup 结束群：discussing/forming → concluded（终态不可回退）。
// 重复关闭幂等：原样返回首次关闭后的群，不改写 closed_by/closed_at。
func (s *Store) CloseGroup(groupID, by string) (Group, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return Group{}, fmt.Errorf("%w: by 必填", ErrInvalid)
	}
	var closed Group
	err := s.mutate(func(next *state) error {
		g, ok := next.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		if g.Status == StatusConcluded {
			closed = g // 幂等命中
			return nil
		}
		now := time.Now().UTC()
		g.Status = StatusConcluded
		g.ClosedBy = by
		g.ClosedAt = &now
		closed = g
		next.Groups[groupID] = g
		return nil
	})
	if err != nil {
		return Group{}, err
	}
	return cloneGroup(closed), nil
}

// ListGroups 全量群列表（按创建时间升序，同时刻按 ID 稳定排序）。逐群深拷贝
// 发布，调用方改写返回值不触及内部状态。
func (s *Store) ListGroups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Group, 0, len(s.st.Groups))
	for _, g := range s.st.Groups {
		out = append(out, cloneGroup(g))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GetGroup 查单个群（存在与否第二返回值）。深拷贝发布（cloneGroup）。
func (s *Store) GetGroup(groupID string) (Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.st.Groups[groupID]
	if !ok {
		return Group{}, false
	}
	return cloneGroup(g), true
}

// AddTaskRef 登记群内任务引用（GROUP-RFC 六「结论转正式任务」）：追加去重
// （幂等，重复登记不产生重复条目），随快照持久化。concluded 群拒绝登记——
// 转任务的创建注记消息要求群可收消息，与 PostMessage 护栏同口径。
func (s *Store) AddTaskRef(groupID, taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("%w: task_id 必填", ErrInvalid)
	}
	return s.mutate(func(next *state) error {
		g, ok := next.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		if g.Status == StatusConcluded {
			return ErrConcluded
		}
		if taskRefIn(g.TaskRefs, taskID) {
			return nil // 幂等命中：已登记
		}
		g.TaskRefs = append(g.TaskRefs, taskID)
		next.Groups[groupID] = g
		return nil
	})
}

// RemoveTaskRef 移除群内任务引用（批 B 修复1「先登记后提交」的失败回滚路径）：
// 引用不存在时幂等无害（no-op）。不检查群终态——回滚登记是账本修正而非消息
// 写入，群在「校验通过→提交失败」窗口内被关闭时回滚仍须生效。
func (s *Store) RemoveTaskRef(groupID, taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("%w: task_id 必填", ErrInvalid)
	}
	return s.mutate(func(next *state) error {
		g, ok := next.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		for i, r := range g.TaskRefs {
			if r != taskID {
				continue
			}
			g.TaskRefs = append(g.TaskRefs[:i], g.TaskRefs[i+1:]...)
			next.Groups[groupID] = g
			return nil
		}
		return nil // 未登记：幂等命中
	})
}

// RefluxTaskResult 任务终态回流群（GROUP-RFC 闭环「结果自动回流」）：查引用了
// 该任务的群，注入一条 from=hub 的 kind=system 消息并记入已回流集合，单事务
// 原子完成。幂等：同任务只回流一次（重复终态事件/重启重放返回 false）。
//
// 回流是 hub 自身的账本注记，绕过 concluded 拒收与 agent 发言预算（system 类
// 本就不计预算）——群结束后时间线保留只读，但成员派发的任务结果仍须可回溯。
// 任务未被任何群引用返回 (false, nil)。
func (s *Store) RefluxTaskResult(taskID, body string) (bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return false, fmt.Errorf("%w: task_id 必填", ErrInvalid)
	}
	if strings.TrimSpace(body) == "" {
		return false, fmt.Errorf("%w: body 不能为空", ErrInvalid)
	}
	if len(body) > MaxBodyBytes {
		return false, fmt.Errorf("%w: %d 字节 > %d 字节", ErrBodyTooLarge, len(body), MaxBodyBytes)
	}
	var posted bool
	err := s.mutate(func(next *state) error {
		if next.Refluxed[taskID] {
			return nil // 幂等命中：已回流
		}
		now := time.Now().UTC()
		for gid, g := range next.Groups {
			if !taskRefIn(g.TaskRefs, taskID) {
				continue
			}
			msg := GroupMessage{
				MsgID:     MsgIDPrefix + protocol.NewMessageID(),
				GroupID:   gid,
				Seq:       next.Seqs[gid] + 1,
				From:      RefluxFrom,
				Kind:      KindSystem,
				Body:      body,
				CreatedAt: now,
			}
			next.appendAndTrim(gid, msg)
			next.Seqs[gid] = msg.Seq
			posted = true
		}
		if posted {
			next.Refluxed[taskID] = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return posted, nil
}

// taskRefIn 引用列表包含判定（线性扫描；群内引用数为个位量级，不值得建索引）。
func taskRefIn(refs []string, taskID string) bool {
	for _, r := range refs {
		if r == taskID {
			return true
		}
	}
	return false
}

// countAgent 统计 agent 消息数（发言预算计量口径）。
func countAgent(msgs []GroupMessage) int {
	n := 0
	for i := range msgs {
		if msgs[i].Kind == KindAgent {
			n++
		}
	}
	return n
}

// ---------- 群闭环消息文案（GROUP-RFC group_task_created 语义） ----------

// 终态 → 人话状态词（与控制台 STATES 词典同口径）。cancelled 取消时 Result 可为
// nil（真实执行数据由 node 后到时合并），TaskTerminalBody 已兜底可读。
var terminalLabels = map[string]string{
	"completed":    "已完成",
	"failed":       "执行失败",
	"needs_review": "待人工核对",
	"cancelled":    "已取消",
}

// TaskCreatedBody 转任务创建注记：注入群的 kind=system 消息正文。
// 格式：任务 {task_id} 已创建（来自群）：{描述截断 100 字（按 rune，UTF-8 安全）}。
func TaskCreatedBody(taskID, desc string) string {
	return fmt.Sprintf("任务 %s 已创建（来自群）：%s", taskID, truncateRunes(desc, 100))
}

// TaskTerminalBody 任务终态回流注记：格式「任务 {task_id} {状态}：{FinalAnswer
// 截断 200 字}」；FinalAnswer 为空（如 expired/workdir_busy_timeout 类失败）时
// 回退展示错误码，保证消息可读非空。
func TaskTerminalBody(t protocol.Task) string {
	label := terminalLabels[string(t.Status)]
	if label == "" {
		label = string(t.Status)
	}
	answer := ""
	if t.Result != nil {
		answer = t.Result.FinalAnswer
		if answer == "" {
			answer = t.Result.Error
		}
	}
	if strings.TrimSpace(answer) == "" {
		answer = "（无最终答复）"
	}
	return fmt.Sprintf("任务 %s %s：%s", t.ID, label, truncateRunes(answer, 200))
}

// truncateRunes 按 rune 截断（中文按字计，GROUP-RFC「截断 N 字」口径）；
// 超长以 … 结尾。
func truncateRunes(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n]) + "…"
}
