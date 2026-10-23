// Package store 实现 hub 持久化层（SPEC 4.7）：全量状态快照 + 先写后见单写事务 +
// data_dir 单实例 flock。掉电耐久性不承诺（SPEC 已声明）。
package store

import (
	"maps"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// PrimeDesignation 全局统筹载体指定（2026-10-23 伦哥定案：网页指定取代能力名硬编码）。
// nil = 未指定（前端回退名字约定 zcode-dev，向后兼容）。同一时刻全网至多一个统筹。
type PrimeDesignation struct {
	NodeID    string `json:"node_id"`
	AgentName string `json:"agent_name"`
	SetAt     string `json:"set_at"` // RFC3339 UTC
	SetBy     string `json:"set_by"` // 操作者（控制台会话固定 admin）
}

// NodeRecord hub 为每个 node_id 持久化的记录：epoch 计数（SPEC 4.4）+ 能力表（DoD 7 重启恢复）。
type NodeRecord struct {
	// Epoch 注册代次：首次注册成功=1，此后每次新连接注册成功原子 +1 并随快照持久化。
	Epoch int64 `json:"epoch"`
	// Agents 最近一次上报的能力表（AgentCard 全量；绝不包含 ExecProfile 字段）。
	Agents []protocol.AgentCard `json:"agents"`
}

// Clone 深拷贝记录（AgentCard 全为标量，值拷贝足够；string 共享安全）。
func (r NodeRecord) Clone() NodeRecord {
	r.Agents = append([]protocol.AgentCard(nil), r.Agents...)
	return r
}

// Mailbox 节点信箱：submitted 状态任务的 FIFO（SPEC 4.6：出队转 offered 即释放；
// offered 超时回退 submitted 时若配额已满仍回队，允许短暂超额，不丢已确认任务）。
type Mailbox struct {
	// Tasks 信箱内任务 ID 队列（任务本体存于 State.Tasks）。
	Tasks []string `json:"tasks"`
	// Bytes 队列内任务体字节和（submitted 任务体计量口径，SPEC 4.6 信箱字节预算 8MiB）。
	// 由 mailbox 包在入队/出队/回队时按入箱记录实测维护（计量缓存，非真相源）。
	Bytes int64 `json:"bytes"`
	// Sizes 入箱时记录的原始任务体字节（task_id → 字节，P1.5 二轮必修4）：出队/
	// 回队/移除一律按该记录核销——任务体随状态迁移变化（置终态附 Result 等），
	// 按当前状态重算会使 Bytes 漂移甚至变负、放宽限额。记录随出队保留（回退重队
	// 复用同一记录），随 Remove 删除。
	Sizes map[string]int64 `json:"sizes,omitempty"`
}

// Clone 深拷贝信箱。
func (m Mailbox) Clone() Mailbox {
	m.Tasks = append([]string(nil), m.Tasks...)
	m.Sizes = maps.Clone(m.Sizes)
	return m
}

// ControlItem 控制操作批次单项目标记录（SPEC 六.五）：提交成功记 TaskID，
// 离线/未暴露/崩溃恢复未及创建的目标记 Skipped，其余校验链失败记 Error 错误码；
// 三者互斥（全空 = 待决，仅存在于 creating 批次的在途/中断条目）。
type ControlItem struct {
	// Target 目标 node_id。
	Target string `json:"target"`
	// TaskID 提交成功的任务 ID（跳过/失败时为空）。
	TaskID string `json:"task_id,omitempty"`
	// Skipped 跳过原因：offline | not_exposed | interrupted（不阻塞其余目标）。
	Skipped string `json:"skipped,omitempty"`
	// Error 其余校验链失败错误码（mailbox_full / frame_too_large 等）。
	Error string `json:"error,omitempty"`
}

// 批次生命周期取值（ControlBatch.Status；语义常量归 core，此处仅承载字段）。
const (
	// BatchStatusCreating 骨架已落库、逐项创建中；中途崩溃重启后保持此态
	//（由 core 启动恢复收尾，不复跑——子任务派生幂等键防重复执行）。
	BatchStatusCreating = "creating"
	// BatchStatusComplete 全部条目已决（提交成功/跳过/逐项错误）。
	BatchStatusComplete = "complete"
)

// ControlBatch 控制操作批次（SPEC 六.五）：管理员从中心端向多 node 同一 agent
// 派发的正式任务集合（每项均走 Submit 全链，hub 不内嵌执行体）。纯管理面记录，
// 随快照持久化，协议层与 node 账本不感知。持久化分两阶段：骨架（本结构 + 
// status=creating）先于任何子任务落库，逐项回写后收尾置 complete——任何时刻
// 崩溃，已执行任务都有批次记录可查（无孤儿窗口）。
type ControlBatch struct {
	// ID 批次标识："b-"+16 字节 hex，hub 生成。
	ID string `json:"id"`
	// Agent 目标 agent 名（node 内唯一名，全网同名复用）。
	Agent string `json:"agent"`
	// Input 下发给各目标的任务输入（同一份）。
	Input protocol.TaskInput `json:"input"`
	// SubmittedBy 提交身份（审计标签，SPEC 六.五：控制台固定 admin）。骨架
	// 持久化，逐项 Submit 与重启后审计共用。
	SubmittedBy string `json:"submitted_by"`
	// IdemKey 批次幂等键（可选，State.BatchIdems 的键）；空 = 不做批次幂等。
	IdemKey string `json:"idem_key,omitempty"`
	// GroupRef 可选群引用（GROUP-RFC 转任务闭环）：批次各子任务归属的协作群。
	// 参与批次内容指纹（同键不同 group_ref 视为不同内容）。
	GroupRef string `json:"group_ref,omitempty"`
	// Status 生命周期：creating → complete（取值见 BatchStatusXxx）。
	Status string `json:"status"`
	// CreatedAt 批次创建时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// Items 快照时点逐目标结果（顺序与请求 targets 一致）。
	Items []ControlItem `json:"items"`
}

// Clone 深拷贝批次。
func (b ControlBatch) Clone() ControlBatch {
	n := b
	n.Input = b.Input.Clone()
	n.Items = append([]ControlItem(nil), b.Items...)
	return n
}

// State hub 全量持久化状态，整体快照落盘 data_dir/hub_state.json（SPEC 4.7）。
// 写时复制纪律：事务在深拷贝副本上构造新状态，禁止原地修改已发布状态中的条目；
// 修改必须以「读旧值 → 构造新值 → 整体替换条目」方式进行。
type State struct {
	// Nodes node_id → 节点记录（epoch + 能力表）。
	Nodes map[string]NodeRecord `json:"nodes"`
	// Tasks task_id → 任务全量。
	Tasks map[string]protocol.Task `json:"tasks"`
	// Mailboxes node_id → 信箱。
	Mailboxes map[string]Mailbox `json:"mailboxes"`
	// Batches batch_id → 控制操作批次（SPEC 六.五）。
	Batches map[string]ControlBatch `json:"batches"`
	// BatchIdems 批次幂等映射：idem_key → batch_id（SPEC 4.5 同机制复用于批次）。
	// 与批次同生命周期：只在批次骨架事务内写入，批次无独立清理路径则映射同存。
	BatchIdems map[string]string `json:"batch_idems"`
	// WorkdirLocks workdir 互斥表（P1.5/SPEC 4.10）：node_id → workspace_id →
	// 占用任务 ID。互斥键 = (node_id, workspace_id)（workspace_id 由 node 经
	// AgentCard 上报的派生键，hub 不感知路径）。占用从 offered 起算、终态释放；
	// 随快照持久化，hub 重启恢复（New 时按任务集自愈校正）。
	WorkdirLocks map[string]map[string]string `json:"workdir_locks"`
	// Prime 全局统筹载体指定（2026-10-23 网页指定）；nil=未指定，前端回退名字约定。
	// 写时复制：事务内构造新 *PrimeDesignation 整体替换，禁止原地改。
	Prime *PrimeDesignation `json:"prime,omitempty"`
}

// NewState 返回各 map 已初始化的空状态。
func NewState() *State {
	return &State{
		Nodes:        make(map[string]NodeRecord),
		Tasks:        make(map[string]protocol.Task),
		Mailboxes:    make(map[string]Mailbox),
		Batches:      make(map[string]ControlBatch),
		BatchIdems:   make(map[string]string),
		WorkdirLocks: make(map[string]map[string]string),
	}
}

// normalize 将反序列化产生的 nil map 补成空 map，保证事务可直接写入。
func (s *State) normalize() {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeRecord)
	}
	if s.Tasks == nil {
		s.Tasks = make(map[string]protocol.Task)
	}
	if s.Mailboxes == nil {
		s.Mailboxes = make(map[string]Mailbox)
	}
	if s.Batches == nil {
		s.Batches = make(map[string]ControlBatch)
	}
	if s.BatchIdems == nil {
		s.BatchIdems = make(map[string]string)
	}
	if s.WorkdirLocks == nil {
		s.WorkdirLocks = make(map[string]map[string]string)
	}
}

// Clone 深拷贝整个状态（容器逐层复制；string 等不可变值共享安全），
// 供单写事务构造新副本（SPEC 4.7「构造新状态副本」）。
func (s *State) Clone() *State {
	n := &State{
		Nodes:        make(map[string]NodeRecord, len(s.Nodes)),
		Tasks:        make(map[string]protocol.Task, len(s.Tasks)),
		Mailboxes:    make(map[string]Mailbox, len(s.Mailboxes)),
		Batches:      make(map[string]ControlBatch, len(s.Batches)),
		BatchIdems:   make(map[string]string, len(s.BatchIdems)),
		WorkdirLocks: make(map[string]map[string]string, len(s.WorkdirLocks)),
	}
	for id, r := range s.Nodes {
		n.Nodes[id] = r.Clone()
	}
	for id, t := range s.Tasks {
		n.Tasks[id] = t.Clone()
	}
	for id, m := range s.Mailboxes {
		n.Mailboxes[id] = m.Clone()
	}
	for id, b := range s.Batches {
		n.Batches[id] = b.Clone()
	}
	for k, v := range s.BatchIdems {
		n.BatchIdems[k] = v
	}
	for nodeID, locks := range s.WorkdirLocks {
		inner := make(map[string]string, len(locks))
		for ws, taskID := range locks {
			inner[ws] = taskID
		}
		n.WorkdirLocks[nodeID] = inner
	}
	return n
}
