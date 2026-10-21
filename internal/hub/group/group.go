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

	// MaxBodyBytes 单条消息体上限：8KiB（GROUP-RFC 三 独立信箱约定，按字节实测）。
	MaxBodyBytes = 8 << 10
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

// state 快照根结构（groups.json 内容）。Messages 按 seq 升序追加（append-only）。
type state struct {
	Groups   map[string]Group          `json:"groups"`
	Messages map[string][]GroupMessage `json:"messages"`
	// Seqs group_id → 已分配的最大 seq（重启后继续单调）。
	Seqs map[string]int64 `json:"seqs"`
}

func newState() *state {
	return &state{
		Groups:   make(map[string]Group),
		Messages: make(map[string][]GroupMessage),
		Seqs:     make(map[string]int64),
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

// clone 深拷贝状态（Group.Members 一并复制；string 等不可变值共享安全），
// 消息切片 cap==len，追加时分配新底层数组，不污染已发布状态。
func (st *state) clone() *state {
	n := newState()
	for id, g := range st.Groups {
		g.Members = append(make([]string, 0, len(g.Members)), g.Members...)
		n.Groups[id] = g
	}
	for id, msgs := range st.Messages {
		n.Messages[id] = append(make([]GroupMessage, 0, len(msgs)), msgs...)
	}
	for id, sq := range st.Seqs {
		n.Seqs[id] = sq
	}
	return n
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
	return g, nil
}

// PostMessage 发言：校验 → 预算/终态护栏 → 分配组内单调 seq → 落盘 → 返回。
// 落盘失败不占用 seq（内存未变，重试重新分配）。
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
		if kind == KindAgent && countAgent(next.Messages[groupID]) >= g.Budget.MaxRounds {
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
		next.Messages[groupID] = append(next.Messages[groupID], msg)
		next.Seqs[groupID] = msg.Seq
		return nil
	})
	if err != nil {
		return GroupMessage{}, err
	}
	return msg, nil
}

// ListMessages 增量拉取：返回 seq > sinceSeq 的消息（升序）。群不存在报
// ErrNotFound；sinceSeq<0 按 0 处理（全量补齐语义，GROUP-RFC 三 group_sync）。
func (s *Store) ListMessages(groupID string, sinceSeq int64) ([]GroupMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Groups[groupID]; !ok {
		return nil, ErrNotFound
	}
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	msgs := s.st.Messages[groupID]
	out := make([]GroupMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Seq > sinceSeq {
			out = append(out, m)
		}
	}
	return out, nil
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
	return closed, nil
}

// ListGroups 全量群列表（按创建时间升序，同时刻按 ID 稳定排序）。
func (s *Store) ListGroups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Group, 0, len(s.st.Groups))
	for _, g := range s.st.Groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
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
