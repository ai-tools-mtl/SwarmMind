// workdir 互斥（P1.5/SPEC 4.10，node 侧为准）：同一 workspace（workdir 规范化
// 派生键）上的受管执行串行——互斥占用从 spawn 前准备（started 落盘之前）到进程
// 组退出 + 产物冻结 + finished 结果落盘（锁随执行 goroutine 退出释放）。hub 的
// 互斥表只是投递视图，node 侧本表是执行真相：覆盖 hub 门控窗口之外的窗口
// （offer 回退重推、hub 重启、node 重启恢复重调度）。
//
// 等待互斥在调用方 goroutine 内同步登记（reserve），授予等待在执行 goroutine 内
// 进行且不占并发槽（并发槽在授予后才取）——「先锁后槽」杜绝恢复场景的
// 「等待者占槽 + 持有者等槽」互等死锁；登记序即 FIFO 授予序（到达序/恢复
// created_at 序），执行序确定。
//
// 锁等待可被服务 ctx 取消（优雅关闭）：取消的等待者放弃执行（withdraw 摘队），
// 账本条目保持 accepted，重启恢复走 Reschedule 再调度——不产生任何执行副作用
// （对齐 SPEC 4.8 关闭竞态语义）。授予与取消竞态时按持有处理（配对释放）。
// 重启后互斥表从账本重建：accepted/started 未终态条目按 (created_at, id) 确定序
// 占锁（最早任务为持有者，其余恢复任务在其后排队）；started 条目实际在账本启动
// 恢复阶段已收敛为 needs_review，此处按状态判定保持与规格口径一致。
package service

import (
	"sync"
)

// wsWaiter 等锁者：FIFO 队列元素，wake 关闭即授予。
type wsWaiter struct {
	taskID string
	wake   chan struct{}
}

// wsMutex workspace 互斥表：wsID → 持有任务 ID，同键等待者 FIFO。
type wsMutex struct {
	mu      sync.Mutex
	held    map[string]string
	waiters map[string][]wsWaiter
}

func newWsMutex() wsMutex {
	return wsMutex{held: map[string]string{}, waiters: map[string][]wsWaiter{}}
}

// seed 账本重建：空闲则占（同键多 accepted 条目时首个生效，其余在 execute 排队）。
// 调用方按 (created_at, id) 确定序遍历账本——持有者=最早任务，恢复执行序确定。
func (m *wsMutex) seed(ws, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.held[ws]; !ok {
		m.held[ws] = taskID
	}
}

// reserve 取得 ws 互斥（调用方 goroutine 内同步登记，不阻塞）：空闲或已由本任务
// 持有 → 取得并返回 nil（调用方即持有）；已被其他任务占用 → 追加 FIFO 等待者并
// 返回 wake（release 授予时先置 held=本任务再关闭）。同步登记使等待序=到达序
// （offer 路径）或恢复清单的 created_at 序（Reschedule 路径），执行序确定。
func (m *wsMutex) reserve(ws, taskID string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.held[ws]; ok && id != taskID {
		w := wsWaiter{taskID: taskID, wake: make(chan struct{})}
		m.waiters[ws] = append(m.waiters[ws], w)
		return w.wake
	}
	m.held[ws] = taskID
	return nil
}

// withdraw 摘除排队等待者（服务关闭放弃执行）：返回 false 表示授予已生效
// （held=本任务，授予与取消竞态），调用方须按持有处理并配对 release。
func (m *wsMutex) withdraw(ws, taskID string, wake <-chan struct{}) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[ws] == taskID {
		return false
	}
	for i, x := range m.waiters[ws] {
		if x.wake == wake {
			m.waiters[ws] = append(m.waiters[ws][:i], m.waiters[ws][i+1:]...)
			break
		}
	}
	if len(m.waiters[ws]) == 0 {
		delete(m.waiters, ws)
	}
	return true
}

// release 释放持有权并把锁交给队首等待者（FIFO 唤醒）；非持有者为 no-op。
func (m *wsMutex) release(ws, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[ws] != taskID {
		return
	}
	if q := m.waiters[ws]; len(q) > 0 {
		w := q[0]
		if len(q) == 1 {
			delete(m.waiters, ws)
		} else {
			m.waiters[ws] = q[1:]
		}
		m.held[ws] = w.taskID
		close(w.wake)
		return
	}
	delete(m.held, ws)
}

// holder 查询当前持有者（启动重建断言/测试用）。
func (m *wsMutex) holder(ws string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held[ws]
}
