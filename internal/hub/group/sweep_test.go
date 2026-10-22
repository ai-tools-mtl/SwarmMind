// sweep_test.go 群存储周期维护与失败补偿（2026-10-22 修复轮2）：
//   - 群消息 24h 过期清理（GROUP-RFC 三 独立轻量信箱约定兑现），与 200 条滚动
//     裁剪并存、恰好 24h 整不裁；
//   - RefluxTaskResult 存储失败 → 待补偿队列（内存先记账）→ Sweep 重试成功即清；
//     队列随快照持久化（重启恢复）；
//   - cloneGroup 的 ClosedAt 深拷贝（读出口改写不穿透 store）。
package group

import (
	"errors"
	"os"
	"testing"
	"time"
)

// breakStorage 将 data_dir 置为只读，使下一次先写后见落盘失败（模拟存储错误）。
// 返回恢复函数；测试结束前必须恢复，否则 TempDir 清理失败。
func breakStorage(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("收紧目录权限: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// newSweepStore 建群 + 登记任务引用的基座。
func newSweepStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	g, err := s.CreateGroup("goal", "admin", nil)
	if err != nil {
		t.Fatalf("建群: %v", err)
	}
	if err := s.AddTaskRef(g.ID, "t-comp"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	return s, g.ID, "t-comp"
}

// TestRefluxPendingCompensation 回流失败补偿闭环：存储失败 → 入队（内存记账）
// → 存储恢复 → Sweep 重试 → 回流落账、队列清空。
func TestRefluxPendingCompensation(t *testing.T) {
	s, gid, taskID := newSweepStore(t)
	dir := s.path[:len(s.path)-len(fileName)] // data_dir（path 去掉文件名）

	// 存储失败：回流报错且自动入队（内存先记账）
	breakStorage(t, dir)
	if _, err := s.RefluxTaskResult(taskID, "任务 t-comp 已完成：报告"); err == nil {
		t.Fatal("存储失败时应报错")
	}
	s.mu.Lock()
	_, queued := s.st.RefluxPending[taskID]
	s.mu.Unlock()
	if !queued {
		t.Fatal("回流失败后应已入待补偿队列（内存）")
	}

	// 存储恢复：Sweep 重试成功，消息落账、队列与幂等集合就位
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("恢复目录权限: %v", err)
	}
	if n := s.Sweep(time.Now().UTC()); n != 0 {
		t.Fatalf("无过期消息时 Sweep 不应清理, got %d", n)
	}
	msgs, _, err := s.ListMessages(gid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != KindSystem || msgs[0].Body != "任务 t-comp 已完成：报告" {
		t.Fatalf("补偿回流应落账一条 system 消息, got %+v", msgs)
	}
	s.mu.Lock()
	_, stillQueued := s.st.RefluxPending[taskID]
	refluxed := s.st.Refluxed[taskID]
	s.mu.Unlock()
	if stillQueued {
		t.Fatal("补偿成功后应出队")
	}
	if !refluxed {
		t.Fatal("补偿回流应记入已回流集合（幂等锚）")
	}

	// 幂等：补偿完成后重复终态钩子不再二次回流
	if ok, err := s.RefluxTaskResult(taskID, "任务 t-comp 已完成：报告"); err != nil || ok {
		t.Fatalf("补偿后重复回流应 (false,nil), got %v err=%v", ok, err)
	}
}

// TestRefluxPendingPersistRestart 补偿队列持久化：存储健康时入队随快照落盘，
// 重启后 Sweep 恢复补偿（终态钩子只触发一次，重启不能丢队列）。
func TestRefluxPendingPersistRestart(t *testing.T) {
	s, gid, taskID := newSweepStore(t)
	dir := s.path[:len(s.path)-len(fileName)]

	if err := s.EnqueueRefluxPending(taskID, "任务 t-comp 已失败：磁盘写满"); err != nil {
		t.Fatalf("入队: %v", err)
	}
	if err := s.EnqueueRefluxPending(taskID, "重复入队应幂等"); err != nil {
		t.Fatalf("重复入队: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer func() { _ = s2.Close() }()
	s2.mu.Lock()
	e, ok := s2.st.RefluxPending[taskID]
	s2.mu.Unlock()
	if !ok || e.Body != "任务 t-comp 已失败：磁盘写满" {
		t.Fatalf("重启后队列应保留首次入队条目, got %+v (ok=%v)", e, ok)
	}
	if n := s2.Sweep(time.Now().UTC()); n != 0 {
		t.Fatalf("Sweep 返回值应为过期消息数 0, got %d", n)
	}
	msgs, _, _ := s2.ListMessages(gid, 0)
	if len(msgs) != 1 || msgs[0].Body != "任务 t-comp 已失败：磁盘写满" {
		t.Fatalf("重启后补偿回流应落账, got %+v", msgs)
	}
	s2.mu.Lock()
	_, queued := s2.st.RefluxPending[taskID]
	s2.mu.Unlock()
	if queued {
		t.Fatal("重启补偿成功后应出队")
	}
}

// TestRefluxPendingNoCompensationTarget 无补偿对象（群引用已不存在）时 Sweep
// 放弃出队，不无限重试。
func TestRefluxPendingNoCompensationTarget(t *testing.T) {
	s, _, taskID := newSweepStore(t)
	if err := s.EnqueueRefluxPending(taskID, "任务 t-comp 已完成：x"); err != nil {
		t.Fatal(err)
	}
	// 摘除引用（RemoveTaskRef 幂等回滚路径）：补偿对象消失
	if err := s.RemoveTaskRef(s.ListGroups()[0].ID, taskID); err != nil {
		t.Fatal(err)
	}
	s.Sweep(time.Now().UTC())
	s.mu.Lock()
	_, queued := s.st.RefluxPending[taskID]
	s.mu.Unlock()
	if queued {
		t.Fatal("无补偿对象时应放弃出队")
	}
}

// TestEnqueueRefluxPendingValidation 入队参数与关闭语义。
func TestEnqueueRefluxPendingValidation(t *testing.T) {
	s, _, _ := newSweepStore(t)
	if err := s.EnqueueRefluxPending(" ", "body"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 task_id 应 ErrInvalid, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRefluxPending("t-x", "body"); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后入队应 ErrClosed, got %v", err)
	}
}

// TestMessageExpiry 24h 过期清理（GROUP-RFC 三）：PostMessage 路径前缀裁剪、
// 恰好 24h 整保留、Sweep 全量清理、pruned_before_seq 口径与滚动裁剪一致。
func TestMessageExpiry(t *testing.T) {
	s, gid, _ := newSweepStore(t)
	now := time.Now().UTC()

	// 三条消息：两条超 24h（回拨 created_at），一条 1h 前
	for i := 0; i < 3; i++ {
		if _, err := s.PostMessage(gid, "u", KindUser, "msg"); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	msgs := s.st.Messages[gid]
	msgs[0].CreatedAt = now.Add(-MaxMessageAge - time.Minute)
	msgs[1].CreatedAt = now.Add(-MaxMessageAge - time.Second)
	msgs[2].CreatedAt = now.Add(-time.Hour)
	s.mu.Unlock()

	// PostMessage 触发过期清理：两条超龄前缀被裁，1h 前的与新消息保留
	if _, err := s.PostMessage(gid, "u", KindUser, "new"); err != nil {
		t.Fatal(err)
	}
	got, prunedBefore, err := s.ListMessages(gid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("超龄消息应被过期清理, got %d 条", len(got))
	}
	if got[0].Body != "msg" || got[0].Seq != 3 {
		t.Fatalf("应从 seq=3 起保留, got %+v", got[0])
	}
	if got[1].Body != "new" {
		t.Fatalf("新消息应在尾, got %+v", got[1])
	}
	if prunedBefore != 2 {
		t.Fatalf("pruned_before_seq 应为 2（已裁上界）, got %d", prunedBefore)
	}

	// 恰好 24h 整：不裁（「超」才过期）
	s.mu.Lock()
	s.st.Messages[gid][0].CreatedAt = now.Add(-MaxMessageAge)
	s.mu.Unlock()
	if kept, n := pruneExpiredMessages(s.st.Messages[gid], now); n != 0 || len(kept) != 2 {
		t.Fatalf("恰好 24h 整应保留, dropped=%d", n)
	}

	// Sweep 全量清理：把剩余消息也拨到超龄后清空
	s.mu.Lock()
	for i := range s.st.Messages[gid] {
		s.st.Messages[gid][i].CreatedAt = now.Add(-MaxMessageAge - time.Hour)
	}
	s.mu.Unlock()
	if n := s.Sweep(now); n != 2 {
		t.Fatalf("Sweep 应清理 2 条过期消息, got %d", n)
	}
	got, _, _ = s.ListMessages(gid, 0)
	if len(got) != 0 {
		t.Fatalf("全过期后应清空, got %d 条", len(got))
	}
}

// TestExpiryAndTrimCoexist 过期清理与 200 条滚动裁剪并存：先过期裁前缀、再
// 滚动裁窗口，两者叠加后窗口与上界口径仍正确。
func TestExpiryAndTrimCoexist(t *testing.T) {
	s, gid, _ := newSweepStore(t)
	now := time.Now().UTC()

	// 160 条：前 100 条回拨为超龄
	for i := 0; i < 160; i++ {
		if _, err := s.PostMessage(gid, "u", KindUser, "m"); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	for i := 0; i < 100; i++ {
		s.st.Messages[gid][i].CreatedAt = now.Add(-MaxMessageAge - time.Hour)
	}
	s.mu.Unlock()
	// 再灌 60 条凑满 220（未清理态，灌的过程中过期 100 条已被逐条 prune → 存量 120）
	for i := 0; i < 60; i++ {
		if _, err := s.PostMessage(gid, "u", KindUser, "m"); err != nil {
			t.Fatal(err)
		}
	}
	// 下一条 PostMessage 先过期裁 100 → 120，再落新消息 121 ≤ 200，滚动不触发
	if _, err := s.PostMessage(gid, "u", KindUser, "m"); err != nil {
		t.Fatal(err)
	}
	got, prunedBefore, _ := s.ListMessages(gid, 0)
	if len(got) != 121 {
		t.Fatalf("过期清理后应存 121 条（220-100 过期 +1 新）, got %d", len(got))
	}
	if got[0].Seq != 101 || prunedBefore != 100 {
		t.Fatalf("应从 seq=101 起、上界 100, got seq=%d pruned=%d", got[0].Seq, prunedBefore)
	}

	// 再灌 100 条触发滚动裁剪：过期（100）与滚动（丢最旧）并存，窗口回到 200
	for i := 0; i < 100; i++ {
		if _, err := s.PostMessage(gid, "u", KindUser, "m"); err != nil {
			t.Fatal(err)
		}
	}
	got, prunedBefore, _ = s.ListMessages(gid, 0)
	if len(got) != MaxGroupMessages {
		t.Fatalf("滚动窗口应回到 200, got %d", len(got))
	}
	if got[0].Seq != 122 || prunedBefore != 121 {
		t.Fatalf("并存裁剪后应从 seq=122 起、上界 121, got seq=%d pruned=%d",
			got[0].Seq, prunedBefore)
	}
}

// TestCloneGroupClosedAtDeepCopy 读出口的 ClosedAt 指针新分配：调用方解引用
// 改写不穿透 store 内部状态（2026-10-22 修复轮2 补齐）。
func TestCloneGroupClosedAtDeepCopy(t *testing.T) {
	s, _, _ := newSweepStore(t)
	gid := s.ListGroups()[0].ID
	if _, err := s.CloseGroup(gid, "admin"); err != nil {
		t.Fatal(err)
	}
	g1, ok := s.GetGroup(gid)
	if !ok || g1.ClosedAt == nil {
		t.Fatalf("关闭后应读到 ClosedAt, ok=%v", ok)
	}
	*g1.ClosedAt = time.Time{} // 越权改写尝试
	g2, _ := s.GetGroup(gid)
	if g2.ClosedAt == nil || g2.ClosedAt.IsZero() {
		t.Fatal("ClosedAt 被调用方改写穿透了 store（应为深拷贝）")
	}
	// 快照内 clone 事务路径同样不共享指针：CloseGroup 返回值再改写不污染后续读
	closed, _ := s.CloseGroup(gid, "admin") // 重复关闭幂等，返回首次审计
	if closed.ClosedAt != nil {
		*closed.ClosedAt = time.Time{}
	}
	g3, _ := s.GetGroup(gid)
	if g3.ClosedAt == nil || g3.ClosedAt.IsZero() {
		t.Fatal("CloseGroup 返回值的 ClosedAt 改写穿透了 store（应为深拷贝）")
	}
}
