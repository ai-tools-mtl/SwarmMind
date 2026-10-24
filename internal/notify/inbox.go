// 站内通知中心（inbox 渠道的投递目的地）：内存环形列表 + 未读标记。
//
// 装配边界（2026-10-24 包提升时定案）：hub 控制台专属（顶栏铃铛与通知中心的
// 目的地）——node 不装配（node 控制台无铃铛/站内动线）：Dispatcher 以 nil
// inbox 装配时 inbox 渠道被跳过；node 管理面在渠道准入处直接拒绝 inbox 类型
//（宁可明确报错，不收一条永远发不出去的渠道配置，同 sms 口径）。
//
// 轻实现边界（设计定稿，如实标注）：不持久化——hub 重启即清空；列表上限
// inboxCap 条（最新在前，超出丢最旧）；站内列表全局一份，同一终态事件无论
// 命中几个 inbox 渠道只投一条（聚合在 Dispatcher.TaskFinal——inbox 渠道只
// 承担开关与事件过滤职责，多渠道定义不产生多条通知）。
package notify

import (
	"strconv"
	"sync"
	"time"
)

// inboxCap 站内通知内存上限（控制台展示最近 20 条，存储留 50 条余量）。
const inboxCap = 50

// InboxItem 一条站内通知。
type InboxItem struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	NodeID    string    `json:"node_id,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Error     string    `json:"error,omitempty"`
	Test      bool      `json:"test,omitempty"` // 渠道「发测试」产生的演示条目
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// Inbox 站内通知内存表。零值可用。
type Inbox struct {
	mu      sync.Mutex
	items   []InboxItem // 最新在前
	nextSeq int
}

// Push 写入一条站内通知（新条目未读），超出上限丢最旧。
func (ib *Inbox) Push(it InboxItem) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	ib.nextSeq++
	it.ID = "n-" + time.Now().UTC().Format("20060102") + "-" + strconv.Itoa(ib.nextSeq)
	it.CreatedAt = time.Now().UTC()
	it.Read = false
	ib.items = append([]InboxItem{it}, ib.items...)
	if len(ib.items) > inboxCap {
		ib.items = ib.items[:inboxCap]
	}
}

// Recent 返回最近 n 条副本与未读总数。
func (ib *Inbox) Recent(n int) ([]InboxItem, int) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if n > len(ib.items) {
		n = len(ib.items)
	}
	out := make([]InboxItem, n)
	copy(out, ib.items[:n])
	unread := 0
	for _, it := range ib.items {
		if !it.Read {
			unread++
		}
	}
	return out, unread
}

// Unread 未读总数。
func (ib *Inbox) Unread() int {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	n := 0
	for _, it := range ib.items {
		if !it.Read {
			n++
		}
	}
	return n
}

// MarkRead 标记单条已读；返回是否存在。
func (ib *Inbox) MarkRead(id string) bool {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	for i := range ib.items {
		if ib.items[i].ID == id {
			ib.items[i].Read = true
			return true
		}
	}
	return false
}

// MarkAllRead 全部标已读，返回影响条数。
func (ib *Inbox) MarkAllRead() int {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	n := 0
	for i := range ib.items {
		if !ib.items[i].Read {
			ib.items[i].Read = true
			n++
		}
	}
	return n
}
