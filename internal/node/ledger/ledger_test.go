package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

var errInjected = errors.New("注入故障")

// mustOpen 在临时目录打开账本并注册清理。
func mustOpen(t *testing.T, dir string) (*Ledger, *Recovery) {
	t.Helper()
	l, rec, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, rec
}

// seedLedger 用公开 API 预置账本状态后关闭（供重启恢复测试用）。
func seedLedger(t *testing.T, dir string, seed func(l *Ledger)) {
	t.Helper()
	l, _, err := Open(dir)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed(l)
	if err := l.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}

// sampleTask 构造测试任务。
func sampleTask(id string, expireAt time.Time) protocol.Task {
	now := protocol.NowUTC()
	return protocol.Task{
		ID:          id,
		TraceID:     "tr-" + id,
		TargetNode:  "mac-mini",
		TargetAgent: "echo-demo",
		Input:       protocol.TaskInput{Args: []string{"hello"}},
		SubmittedBy: "hana",
		Status:      protocol.StatusOffered,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpireAt:    expireAt,
	}
}

// TestLedgerAcceptStartFinishAck 正常执行链：accept → started → finished → acked，
// PendingResend 随之变化；重复 ack 幂等。
func TestLedgerAcceptStartFinishAck(t *testing.T) {
	l, rec := mustOpen(t, t.TempDir())
	if len(rec.Reschedule) != 0 || len(rec.Resend) != 0 {
		t.Fatalf("空账本不应有恢复项: %+v", rec)
	}
	now := protocol.NowUTC()
	if err := l.Accept(sampleTask("t-1", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	e, ok := l.Get("t-1")
	if !ok || e.State != StateAccepted || e.ResultAcked || e.Result != nil {
		t.Fatalf("accept 后条目不符: ok=%v %+v", ok, e)
	}
	if err := l.MarkStarted("t-1"); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := l.MarkFinished("t-1", protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "hi", FinishedAt: now,
	}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	if n := len(l.PendingResend()); n != 1 {
		t.Fatalf("finished 未 acked 应在重发队列: %d", n)
	}
	if err := l.MarkResultAcked("t-1"); err != nil {
		t.Fatalf("MarkResultAcked: %v", err)
	}
	if n := len(l.PendingResend()); n != 0 {
		t.Fatalf("acked 后不应在重发队列: %d", n)
	}
	if err := l.MarkResultAcked("t-1"); err != nil {
		t.Errorf("重复 ack 应幂等成功: %v", err)
	}
}

// TestLedgerPersistAcrossReopen 关闭重开后条目与结果完整恢复；acked 条目不再重发。
func TestLedgerPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	now := protocol.NowUTC()
	if err := l.Accept(sampleTask("t-1", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkStarted("t-1"); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := l.MarkFinished("t-1", protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "完整答复", FinishedAt: now,
	}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	if err := l.MarkResultAcked("t-1"); err != nil {
		t.Fatalf("MarkResultAcked: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	l2, rec := mustOpen(t, dir)
	if len(rec.Resend) != 0 {
		t.Errorf("acked 条目重启后不应重发: %+v", rec.Resend)
	}
	e, ok := l2.Get("t-1")
	if !ok {
		t.Fatal("重启后条目丢失")
	}
	if e.State != StateFinished || !e.ResultAcked || e.Result == nil || e.Result.FinalAnswer != "完整答复" {
		t.Errorf("重启后条目不符: %+v", e)
	}
}

// TestLedgerDuplicateAccept 重复 offer 只入账一次，且不改变已有条目状态。
func TestLedgerDuplicateAccept(t *testing.T) {
	l, _ := mustOpen(t, t.TempDir())
	task := sampleTask("t-1", protocol.NowUTC().Add(time.Hour))
	if err := l.Accept(task); err != nil {
		t.Fatalf("首次 Accept: %v", err)
	}
	if err := l.MarkStarted("t-1"); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := l.Accept(task); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("重复 Accept 应返回 ErrDuplicate，得到 %v", err)
	}
	e, _ := l.Get("t-1")
	if e.State != StateStarted {
		t.Errorf("重复 Accept 不得改变已有条目: state=%s", e.State)
	}
}

// TestLedgerStateGuards 状态守卫：不存在、非法转移、accepted 直达 finished、重复终态。
func TestLedgerStateGuards(t *testing.T) {
	l, _ := mustOpen(t, t.TempDir())
	if err := l.MarkStarted("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkStarted 未知任务应 ErrNotFound，得到 %v", err)
	}
	if err := l.MarkFinished("nope", protocol.TaskResult{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkFinished 未知任务应 ErrNotFound，得到 %v", err)
	}
	if err := l.MarkResultAcked("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkResultAcked 未知任务应 ErrNotFound，得到 %v", err)
	}

	if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// accepted → finished 允许：未 spawn 即失败的路径（如 policy_denied）
	if err := l.MarkFinished("t-1", protocol.TaskResult{
		Status: protocol.ResultFailed, ExitCode: protocol.ExitCodeNotRun, Error: protocol.CodePolicyDenied,
	}); err != nil {
		t.Fatalf("accepted→finished 应允许: %v", err)
	}
	if err := l.MarkFinished("t-1", protocol.TaskResult{Status: protocol.ResultCompleted}); !errors.Is(err, ErrWrongState) {
		t.Errorf("重复终态应 ErrWrongState，得到 %v", err)
	}
	if err := l.MarkStarted("t-1"); !errors.Is(err, ErrWrongState) {
		t.Errorf("finished→started 应 ErrWrongState，得到 %v", err)
	}
	if err := l.MarkResultAcked("t-1"); err != nil {
		t.Fatalf("MarkResultAcked: %v", err)
	}
}

// ackAt 直接回填条目的 acked 位与首次确认时刻（包内测试专用：构造历史的 first_acked_at，
// 模拟「已完成很久、确认也已很久」与「完成很久但刚刚确认」两类时间线）。
func ackAt(t *testing.T, l *Ledger, taskID string, at time.Time) {
	t.Helper()
	err := l.Update(func(m map[string]Entry) error {
		e, ok := m[taskID]
		if !ok {
			return ErrNotFound
		}
		ne := e.Clone()
		ne.ResultAcked = true
		ne.FirstAckedAt = at
		m[taskID] = ne
		return nil
	})
	if err != nil {
		t.Fatalf("回填 first_acked_at(%s): %v", taskID, err)
	}
}

// TestLedgerCleanupAcked 保留期限按首次确认时刻 first_acked_at 起算（SPEC 4.6/4.8）：
// 完成 25h 前但刚确认 → 仍保留；首次确认满 24h → 清理；未 acked → 保留；
// 重复 Ack 不得重置保留窗口。
func TestLedgerCleanupAcked(t *testing.T) {
	l, _ := mustOpen(t, t.TempDir())
	now := protocol.NowUTC()
	old := now.Add(-25 * time.Hour)

	// 首次确认在 25h 前 → 已满 24h 保留期 → 清理
	if err := l.Accept(sampleTask("t-old", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkFinished("t-old", protocol.TaskResult{Status: protocol.ResultCompleted, FinishedAt: old}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	ackAt(t, l, "t-old", old)

	// 完成 25h 前但刚确认 → 仍保留（期限从首次 Ack 起算，不从完成时刻起算）
	if err := l.Accept(sampleTask("t-just-acked", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkFinished("t-just-acked", protocol.TaskResult{Status: protocol.ResultCompleted, FinishedAt: old}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	if err := l.MarkResultAcked("t-just-acked"); err != nil {
		t.Fatalf("MarkResultAcked: %v", err)
	}
	if e, _ := l.Get("t-just-acked"); !e.FirstAckedAt.After(old) {
		t.Errorf("FirstAckedAt 应为确认时刻(%v)而非完成时刻(%v)", e.FirstAckedAt, old)
	}

	// 旧但未 acked → 保留
	if err := l.Accept(sampleTask("t-pending", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkFinished("t-pending", protocol.TaskResult{Status: protocol.ResultFailed, FinishedAt: old}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}

	n, err := l.CleanupAcked(now)
	if err != nil {
		t.Fatalf("CleanupAcked: %v", err)
	}
	if n != 1 {
		t.Errorf("应清理 1 条，实际 %d", n)
	}
	if _, ok := l.Get("t-old"); ok {
		t.Error("首次确认满 24h 的 t-old 应已被清理")
	}
	if _, ok := l.Get("t-just-acked"); !ok {
		t.Error("刚确认的 t-just-acked 不应被清理（完成于 25h 前不参与计算）")
	}
	if _, ok := l.Get("t-pending"); !ok {
		t.Error("未 acked 的 t-pending 不应被清理")
	}

	// 重复 Ack 不更新首次确认时刻：把 t-just-acked 的首次确认回填到 25h 前后再 Ack 一次，
	// 窗口不重置 → 立即满足清理条件
	ackAt(t, l, "t-just-acked", old)
	if err := l.MarkResultAcked("t-just-acked"); err != nil {
		t.Fatalf("重复 Ack 应幂等成功: %v", err)
	}
	if n, err := l.CleanupAcked(now); err != nil || n != 1 {
		t.Errorf("重复 Ack 不得重置窗口：应清理 1 条，实际 n=%d err=%v", n, err)
	}
	if _, ok := l.Get("t-just-acked"); ok {
		t.Error("重复 Ack 重置了保留窗口：t-just-acked 应已被清理")
	}
}

// TestLedgerAckRetentionAcrossRestart 确认后重启：first_acked_at 持久化，保留窗口不重置
// （重启前首次确认已满 24h 的条目，重启后立即清理，而非重新计时 24h）。
func TestLedgerAckRetentionAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	now := protocol.NowUTC()
	if err := l.Accept(sampleTask("t-1", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkFinished("t-1", protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: now,
	}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	if err := l.MarkResultAcked("t-1"); err != nil {
		t.Fatalf("MarkResultAcked: %v", err)
	}
	// 回填「首次确认发生在 25h 前」后关闭重启
	ackAt(t, l, "t-1", now.Add(-25*time.Hour))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	l2, rec := mustOpen(t, dir)
	if len(rec.Resend) != 0 {
		t.Errorf("acked 条目不应进 Resend: %+v", rec.Resend)
	}
	e, ok := l2.Get("t-1")
	if !ok || !e.ResultAcked || !e.FirstAckedAt.Equal(now.Add(-25*time.Hour)) {
		t.Fatalf("first_acked_at 应持久化且重启不重置: ok=%v %+v", ok, e)
	}
	n, err := l2.CleanupAcked(now)
	if err != nil {
		t.Fatalf("CleanupAcked: %v", err)
	}
	if n != 1 {
		t.Errorf("重启不得重置保留窗口：应立即清理 1 条，实际 %d", n)
	}
	if _, ok := l2.Get("t-1"); ok {
		t.Error("首次确认满 24h 的条目重启后应被清理")
	}
}

// TestLedgerWriteFailureNoPublishNoAck 写坏目录触发失败：临时文件路径被目录占位后
// 落盘必败 → 断言账本未变、无 Ack；移除障碍后重试成功。
func TestLedgerWriteFailureNoPublishNoAck(t *testing.T) {
	l, _ := mustOpen(t, t.TempDir())
	if err := os.Mkdir(l.tmp, 0o755); err != nil {
		t.Fatalf("占位临时路径: %v", err)
	}
	err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour)))
	if err == nil {
		t.Fatal("写盘失败必须返回错误（不 Ack）")
	}
	if _, ok := l.Get("t-1"); ok {
		t.Error("落盘失败不得发布到账本内存")
	}
	if _, err := os.Stat(l.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("落盘失败不得产生账本文件: %v", err)
	}
	if err := os.Remove(l.tmp); err != nil {
		t.Fatalf("移除障碍: %v", err)
	}
	if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("障碍移除后 Accept 应成功: %v", err)
	}
	if _, ok := l.Get("t-1"); !ok {
		t.Error("重试 Accept 应发布到账本内存")
	}
}

// TestLedgerFailureInjection before_write 注入：事务失败、账本不变；解除注入后成功。
func TestLedgerFailureInjection(t *testing.T) {
	l, _ := mustOpen(t, t.TempDir())
	l.inject = func(stage string) error {
		if stage == stageBeforeWrite {
			return errInjected
		}
		return nil
	}
	err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour)))
	if !errors.Is(err, errInjected) {
		t.Fatalf("before_write 注入应使 Accept 失败: %v", err)
	}
	if _, ok := l.Get("t-1"); ok {
		t.Error("注入失败不得发布到账本内存")
	}
	l.inject = nil
	if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("解除注入后 Accept: %v", err)
	}
}

// TestLedgerOpenLockConflict 同一 data_dir 第二个账本打开必须因 flock 冲突失败。
func TestLedgerOpenLockConflict(t *testing.T) {
	dir := t.TempDir()
	mustOpen(t, dir)
	if _, _, err := Open(dir); err == nil {
		t.Fatal("同一 data_dir 第二个 Ledger 应因 flock 冲突打开失败")
	}
}

// mustDiskEntries 直接从磁盘读账本条目（绕过内存），验证落盘内容。
func mustDiskEntries(t *testing.T, dir string) map[string]Entry {
	t.Helper()
	entries, err := loadEntries(filepath.Join(dir, ledgerFileName))
	if err != nil {
		t.Fatalf("读磁盘账本: %v", err)
	}
	return entries
}

// TestLedgerClosedStopsResend hub 拒收（result_conflict / expired）后标记 closed：
// 退出重发队列、持久化；重启恢复不再进入 Resend（停止重传，记录保留，SPEC 4.5）。
func TestLedgerClosedStopsResend(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	now := protocol.NowUTC()
	if err := l.Accept(sampleTask("t-1", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkFinished("t-1", protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: now,
	}); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}
	if n := len(l.PendingResend()); n != 1 {
		t.Fatalf("finished 未 acked 应在重发队列: %d", n)
	}

	// 守卫：未知任务 / 非 finished 条目不得标记 closed
	if err := l.MarkResendClosed("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("未知任务应 ErrNotFound，得到 %v", err)
	}
	if err := l.Accept(sampleTask("t-acc", now.Add(time.Hour))); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkResendClosed("t-acc"); !errors.Is(err, ErrWrongState) {
		t.Errorf("非 finished 条目应 ErrWrongState，得到 %v", err)
	}

	if err := l.MarkResendClosed("t-1"); err != nil {
		t.Fatalf("MarkResendClosed: %v", err)
	}
	if n := len(l.PendingResend()); n != 0 {
		t.Fatalf("closed 后应退出重发队列: %d", n)
	}
	if err := l.MarkResendClosed("t-1"); err != nil {
		t.Errorf("重复 closed 应幂等成功: %v", err)
	}
	// 记录保留（不删除）：条目仍可查询，closed 标记与结果俱在
	if e, ok := l.Get("t-1"); !ok || !e.Closed || e.State != StateFinished || e.Result == nil {
		t.Errorf("closed 条目应保留审计记录: ok=%v %+v", ok, e)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启恢复：closed 条目不进 Resend（停止重传），其余条目不受影响
	l2, rec := mustOpen(t, dir)
	if len(rec.Resend) != 0 {
		t.Errorf("closed 条目重启后不应进 Resend: %+v", rec.Resend)
	}
	if len(rec.Reschedule) != 1 || rec.Reschedule[0].ID != "t-acc" {
		t.Errorf("accepted 条目恢复不受影响: %+v", rec.Reschedule)
	}
	if e, ok := l2.Get("t-1"); !ok || !e.Closed {
		t.Errorf("closed 标记应持久化: ok=%v %+v", ok, e)
	}
	if n := len(l2.PendingResend()); n != 0 {
		t.Errorf("closed 条目重启后不得回到重发队列: %d", n)
	}
}

// TestLedgerInjectionStages 四个预设注入点逐一验证先写后见语义（与 hub store 同模式）。
func TestLedgerInjectionStages(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("预置 Accept: %v", err)
	}
	diskBefore, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatalf("读账本: %v", err)
	}

	// before_write：盘与内存都不变（写盘失败语义：副本丢弃无可见变化）
	l.inject = func(stage string) error {
		if stage == stageBeforeWrite {
			return errInjected
		}
		return nil
	}
	if err := l.Accept(sampleTask("t-2", protocol.NowUTC().Add(time.Hour))); !errors.Is(err, errInjected) {
		t.Fatalf("before_write 注入应使 Accept 失败: %v", err)
	}
	if _, ok := l.Get("t-2"); ok {
		t.Error("before_write 失败不得发布到内存")
	}
	if diskAfter, _ := os.ReadFile(l.path); string(diskAfter) != string(diskBefore) {
		t.Error("before_write 失败不得落盘")
	}

	// after_rename：盘已是新状态、内存未发布（「已提交但确认丢失」崩溃窗口模拟）
	l.inject = func(stage string) error {
		if stage == stageAfterRename {
			return errInjected
		}
		return nil
	}
	if err := l.Accept(sampleTask("t-2", protocol.NowUTC().Add(time.Hour))); !errors.Is(err, errInjected) {
		t.Fatalf("after_rename 注入应使 Accept 失败: %v", err)
	}
	if _, ok := l.Get("t-2"); ok {
		t.Error("after_rename 失败不得发布到内存")
	}
	if disk := mustDiskEntries(t, dir); len(disk) != 2 {
		t.Errorf("after_rename 时磁盘应已含新状态: %+v", disk)
	}

	// 做一次干净事务，让内存重新领先并覆写磁盘，对齐两侧
	l.inject = nil
	if err := l.Accept(sampleTask("t-2", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("对齐 Accept: %v", err)
	}

	// before_publish：表现同 after_rename（盘新、内存未发布）
	l.inject = func(stage string) error {
		if stage == stageBeforePublish {
			return errInjected
		}
		return nil
	}
	if err := l.Accept(sampleTask("t-3", protocol.NowUTC().Add(time.Hour))); !errors.Is(err, errInjected) {
		t.Fatalf("before_publish 注入应使 Accept 失败: %v", err)
	}
	if _, ok := l.Get("t-3"); ok {
		t.Error("before_publish 失败不得发布到内存")
	}
	if disk := mustDiskEntries(t, dir); len(disk) != 3 {
		t.Errorf("before_publish 时磁盘应已含新状态: %+v", disk)
	}

	// 对齐后 before_ack：内存已发布、磁盘已落盘，但调用方未获 Ack（「发布成、确认丢」）
	l.inject = nil
	if err := l.Accept(sampleTask("t-3", protocol.NowUTC().Add(time.Hour))); err != nil {
		t.Fatalf("对齐 Accept: %v", err)
	}
	l.inject = func(stage string) error {
		if stage == stageBeforeAck {
			return errInjected
		}
		return nil
	}
	if err := l.Accept(sampleTask("t-4", protocol.NowUTC().Add(time.Hour))); !errors.Is(err, errInjected) {
		t.Fatalf("before_ack 注入应使 Accept 失败: %v", err)
	}
	if _, ok := l.Get("t-4"); !ok {
		t.Error("before_ack 失败时内存应已发布（Ack 丢失≠未发布）")
	}
	if disk := mustDiskEntries(t, dir); len(disk) != 4 {
		t.Errorf("before_ack 时磁盘应已含新状态: %+v", disk)
	}
	l.inject = nil
}

// TestLedgerRecoveryDowngradePersistFailure 恢复降级写盘失败：Open 整体失败但锁已释放、
// 磁盘保持 started 原状（降级未落盘）；解除注入后重开完成降级并回传。
func TestLedgerRecoveryDowngradePersistFailure(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkStarted("t-1"); err != nil {
			t.Fatalf("seed MarkStarted: %v", err)
		}
	})

	injected := errors.New("注入恢复降级落盘失败")
	if _, _, err := open(dir, func(stage string) error {
		if stage == stageBeforeWrite {
			return injected
		}
		return nil
	}); !errors.Is(err, injected) {
		t.Fatalf("恢复降级落盘失败应使 Open 失败: %v", err)
	}
	// Open 失败后锁必须已释放（下方重开成功即证）、盘上仍是 started 原状
	disk := mustDiskEntries(t, dir)
	if e, ok := disk["t-1"]; !ok || e.State != StateStarted {
		t.Errorf("降级落盘失败不得改动磁盘状态: ok=%v %+v", ok, e)
	}

	// 解除注入重开：降级完成，needs_review 进 Resend
	l, rec := mustOpen(t, dir)
	if len(rec.Resend) != 1 || rec.Resend[0].Task.ID != "t-1" {
		t.Fatalf("解除注入后重开应完成降级并回传: %+v", rec.Resend)
	}
	if e, _ := l.Get("t-1"); e.State != StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultNeedsReview {
		t.Errorf("降级结果不符: %+v", e)
	}
}

// TestLedgerConcurrentDuplicateAccept 并发重复 Accept：恰好一次成功入账，其余 ErrDuplicate
// 且不改变条目；并发完成后磁盘与内存一致（仅一条 accepted 记录）。
func TestLedgerConcurrentDuplicateAccept(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	task := sampleTask("t-dup", protocol.NowUTC().Add(time.Hour))
	const n = 16
	var (
		wg      sync.WaitGroup
		success atomic.Int32
		dupCnt  atomic.Int32
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := l.Accept(task); {
			case err == nil:
				success.Add(1)
			case errors.Is(err, ErrDuplicate):
				dupCnt.Add(1)
			default:
				t.Errorf("并发 Accept 出现意外错误: %v", err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || dupCnt.Load() != n-1 {
		t.Errorf("应恰 1 次成功、%d 次 ErrDuplicate，实际 success=%d dup=%d",
			n-1, success.Load(), dupCnt.Load())
	}
	if e, ok := l.Get("t-dup"); !ok || e.State != StateAccepted {
		t.Errorf("入账条目应为 accepted: ok=%v %+v", ok, e)
	}
	disk := mustDiskEntries(t, dir)
	if len(disk) != 1 {
		t.Errorf("磁盘应仅 1 条记录: %d", len(disk))
	}
	if e := disk["t-dup"]; e.State != StateAccepted {
		t.Errorf("磁盘条目状态不符: %+v", e)
	}
}
