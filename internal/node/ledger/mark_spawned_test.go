package ledger

// MarkSpawned 测试（maturity-orphan-guard）：pgid+ProcStart 落盘语义与
// Windows/旧条目兼容。ProcStart 为组长启动时刻指纹（pid+starttime 实例匹配
// 证据，R1 必修1）——与 pgid 同事务落盘，重启后供清扫侧精确匹配原实例。

import (
	"errors"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestMarkSpawnedAndPersist spawn 后 pgid+procStart+bootIdent 落盘且跨重启保留
//（崩溃恢复的数据源）。
func TestMarkSpawnedAndPersist(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	task := sampleTask("t-spawn-1", time.Now().Add(time.Hour))
	if err := l.Accept(task); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := l.MarkStarted(task.ID); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := l.MarkSpawned(task.ID, 4242, 987654, "boot-a"); err != nil {
		t.Fatalf("MarkSpawned: %v", err)
	}
	e, ok := l.Get(task.ID)
	if !ok || e.Pgid != 4242 || e.ProcStart != 987654 || e.BootIdent != "boot-a" {
		t.Fatalf("证据三元组应 (4242, 987654, boot-a), got (%v, %v, %q) ok=%v", e.Pgid, e.ProcStart, e.BootIdent, ok)
	}
	_ = l.Close()

	// 重开后仍在（持久化）
	l2, _ := mustOpen(t, dir)
	e2, ok := l2.Get(task.ID)
	if !ok || e2.Pgid != 4242 || e2.ProcStart != 987654 || e2.BootIdent != "boot-a" {
		t.Fatalf("重启后证据应保留, got (%v, %v, %q) ok=%v", e2.Pgid, e2.ProcStart, e2.BootIdent, ok)
	}
}

// TestMarkSpawnedGuards Windows 语义（pgid=0 不落盘）、终态拒绝、未知任务拒绝。
func TestMarkSpawnedGuards(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	task := sampleTask("t-spawn-2", time.Now().Add(time.Hour))
	if err := l.Accept(task); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	// pgid=0（Windows，指纹/周期恒零值）恒不落盘且不报错
	if err := l.MarkStarted(task.ID); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := l.MarkSpawned(task.ID, 0, 0, ""); err != nil {
		t.Fatalf("pgid=0 应无操作不报错, got %v", err)
	}
	if e, _ := l.Get(task.ID); e.Pgid != 0 || e.ProcStart != 0 || e.BootIdent != "" {
		t.Fatalf("pgid=0 不应落盘, got (%d, %d, %q)", e.Pgid, e.ProcStart, e.BootIdent)
	}
	// 未知任务拒绝
	if err := l.MarkSpawned("t-absent", 1, 1, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知任务应 ErrNotFound, got %v", err)
	}
	// 终态后拒绝（WrongState）
	_ = l.MarkFinished(task.ID, protocol.TaskResult{Status: protocol.ResultCompleted, FinishedAt: protocol.NowUTC()})
	if err := l.MarkSpawned(task.ID, 7, 7, "b"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("终态条目应 ErrWrongState, got %v", err)
	}
}
