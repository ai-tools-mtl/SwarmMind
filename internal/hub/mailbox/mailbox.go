// Package mailbox 实现 hub 信箱与 idem 索引（SPEC 4.5/4.6）。
//
// 信箱：每 node 一条 submitted 状态任务的 FIFO——条数配额 100、字节预算 8MiB
// （submitted 任务体字节和，按入箱时 Task JSON 编码实测记录，出队/回队/移除按
// 该记录核销）；出队转 offered 即释放配额；offered 超时回退时若配额已满仍回队
// （重投预留，允许短暂超额，不丢已确认任务）。信箱只存任务 ID 与计量字节，任务
// 本体存于 store.State.Tasks。
//
// idem 索引：hub 全局作用域，直接派生自 State.Tasks（任务体携带 IdemKey 与全部
// 提交内容），不设独立存储——idem 与任务同生命周期（过期/清理同步失效）由构造保证。
// 同键同内容 → 返回原 task_id + dup；同键不同内容 → ErrConflict。
//
// 本包函数操作调用方给定的 *store.State（事务内为深拷贝副本，发布前外部不可见；
// 预检时为已发布视图，只读），自身不加锁、不做 IO。
package mailbox

import (
	"encoding/json"
	"errors"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 错误哨兵（errors.Is 判定）。
var (
	// ErrMailboxFull 信箱条数配额或字节预算已满（SPEC 4.6 → 503 mailbox_full）。
	ErrMailboxFull = errors.New("mailbox: 信箱配额/字节预算已满")
	// ErrConflict idem 同键不同内容（SPEC 4.5 → 409 conflict）。
	ErrConflict = errors.New("mailbox: idem 同键不同内容")
)

// TaskBytes 任务体字节数（submitted 任务体计量口径）：Task JSON 编码长度。
// encoding/json 对结构体字段序与 map 键序均为确定性输出，计量可复现。
func TaskBytes(t *protocol.Task) int64 {
	b, err := json.Marshal(t)
	if err != nil {
		// Task 全字段可序列化，此处不可达；防御性兜底按 0 计并注释
		return 0
	}
	return int64(len(b))
}

// settledBytes 出队/回队/移除的核销口径：入箱时记录的原始大小（Sizes；P1.5 二轮
// 必修4——任务体随状态迁移变化，置终态附 Result 后按当前状态重算会使 Bytes 变负、
// 放宽限额）。无记录的陈旧条目（旧版本快照）退回当前状态实测。
func settledBytes(mb *store.Mailbox, t *protocol.Task) int64 {
	if size, ok := mb.Sizes[t.ID]; ok {
		return size
	}
	return TaskBytes(t)
}

// ensureSizes 信箱 Sizes 记录惰性初始化（旧版本快照无该字段）。
func ensureSizes(mb *store.Mailbox) {
	if mb.Sizes == nil {
		mb.Sizes = make(map[string]int64)
	}
}

// Enqueue 任务入箱（submitted 入队）：检查条数配额与字节预算，通过则追加队尾、
// 记录入箱原始大小并累计字节计量。超限返回 ErrMailboxFull，信箱不变。
func Enqueue(st *store.State, nodeID string, t *protocol.Task) error {
	mb := st.Mailboxes[nodeID]
	size := TaskBytes(t)
	if len(mb.Tasks)+1 > protocol.MailboxQuota {
		return ErrMailboxFull
	}
	if mb.Bytes+size > protocol.MailboxByteBudget {
		return ErrMailboxFull
	}
	mb.Tasks = append(mb.Tasks, t.ID)
	ensureSizes(&mb)
	mb.Sizes[t.ID] = size
	mb.Bytes += size
	st.Mailboxes[nodeID] = mb
	return nil
}

// Dequeue 出队队首（出队转 offered 即释放配额，SPEC 4.6）：从队列移除并扣减字节
// 计量，返回该任务。防御性跳过队列中已不存在/非 submitted 状态的陈旧条目
// （正常路径不会出现：任务离开 submitted 的所有转移均同步 Remove）。
// 队列空或无有效条目返回 ok=false。
func Dequeue(st *store.State, nodeID string) (protocol.Task, bool) {
	return DequeueWhere(st, nodeID, nil)
}

// DequeueWhere 出队首个满足 eligible 的 submitted 任务（P1.5/SPEC 4.10：workdir
// 互斥门控，修复严格队首的 head-of-line blocking——队首互斥中时同 node 异 workdir
// 任务仍可派发）。按 FIFO 遍历：eligible 为 nil 视为全部合格；不合格条目按原序
// 保留在队（配额/字节计量不变），首个合格条目移除出队并扣减字节计量。防御性
// 跳过已不存在/非 submitted 的陈旧条目（与 Dequeue 同口径）。队列空或无合格
// 条目返回 ok=false，信箱不变。
func DequeueWhere(st *store.State, nodeID string, eligible func(protocol.Task) bool) (protocol.Task, bool) {
	mb := st.Mailboxes[nodeID]
	for i := 0; i < len(mb.Tasks); i++ {
		id := mb.Tasks[i]
		t, ok := st.Tasks[id]
		if !ok || t.Status != protocol.StatusSubmitted {
			// 不变量破坏（任务删除与信箱出队不同步）或状态漂移：跳过陈旧条目继续
			continue
		}
		if eligible != nil && !eligible(t) {
			continue // 门控不合格（如 workdir 互斥中）：保留在队，继续向后找
		}
		mb.Tasks = append(mb.Tasks[:i], mb.Tasks[i+1:]...)
		mb.Bytes -= settledBytes(&mb, &t) // 按入箱原始大小核销；Sizes 记录保留（回退重队复用）
		st.Mailboxes[nodeID] = mb
		return t, true
	}
	st.Mailboxes[nodeID] = mb
	return protocol.Task{}, false
}

// RequeueFront offered 超时回退：无条件插回队首（重投预留，SPEC 4.6——配额已满仍
// 回队，允许短暂超额，不丢已确认任务）并按入箱记录恢复字节计量。FIFO 语义下回
// 队首保持原提交顺序公平。
func RequeueFront(st *store.State, nodeID string, t *protocol.Task) {
	mb := st.Mailboxes[nodeID]
	size := settledBytes(&mb, t) // 入箱原始大小（记录随出队保留，回队复用同记录）
	ensureSizes(&mb)
	mb.Sizes[t.ID] = size
	mb.Tasks = append([]string{t.ID}, mb.Tasks...)
	mb.Bytes += size
	st.Mailboxes[nodeID] = mb
}

// Remove 从信箱移除任务（任务离开 submitted 状态的所有转移——提前出队、迟到 ack
// 收敛、submitted 直达终态、过期/排队超时清扫——均须调用以保持配额/字节计量
// 准确）。按入箱时记录的原始大小核销（P1.5 二轮必修4：终态任务体附 Result 后
// 大于入箱时，按当前状态核销会使 Bytes 变负）。任务不在队列时为 no-op（返回 false）。
func Remove(st *store.State, nodeID, taskID string) bool {
	mb := st.Mailboxes[nodeID]
	for i, id := range mb.Tasks {
		if id != taskID {
			continue
		}
		mb.Tasks = append(mb.Tasks[:i], mb.Tasks[i+1:]...)
		var t protocol.Task
		if cur, ok := st.Tasks[taskID]; ok {
			t = cur
		}
		mb.Bytes -= settledBytes(&mb, &t)
		delete(mb.Sizes, taskID)
		st.Mailboxes[nodeID] = mb
		return true
	}
	return false
}

// Usage 返回信箱当前排队条数与字节计量（预检口径；0 值即未建箱）。
func Usage(st *store.State, nodeID string) (count int, bytes int64) {
	mb := st.Mailboxes[nodeID]
	return len(mb.Tasks), mb.Bytes
}
