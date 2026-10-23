// 单实例锁跨平台契约测试（lock.go unix flock / lock_windows.go CreateFile 独占
// + LockFileEx）：两平台同一套断言，darwin/linux 本机跑 unix 侧，Windows 真机
// 跑 windows 侧（返工必修1）。覆盖 SPEC 4.7 契约：互斥、释放后可重入、目录
// 创建、幂等释放。
package ledger

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLockExclusiveSecondAcquireFails 同一 data_dir 第二次加锁必须失败（单实例契约）。
func TestLockExclusiveSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	l1, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	defer l1.Release()
	if _, err := AcquireLock(dir); err == nil {
		t.Fatal("同 data_dir 第二次加锁应失败（其他实例持锁）")
	}
}

// TestLockReleaseAllowsReacquire 释放后可重新加锁（等价进程退出自动释放后重启）。
func TestLockReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	l1, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	l2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("释放后应可重新加锁: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatalf("二次释放失败: %v", err)
	}
}

// TestLockCreatesDirAndFile data_dir 不存在时创建，锁文件落位 data_dir/lock。
func TestLockCreatesDirAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("嵌套目录加锁失败: %v", err)
	}
	defer l.Release()
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Fatalf("锁文件应存在: %v", err)
	}
}

// TestLockReleaseIdempotent 重复 Release 与 nil 接收者不 panic 不报错。
func TestLockReleaseIdempotent(t *testing.T) {
	dir := t.TempDir()
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("首次释放失败: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("二次释放应幂等: %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil 接收者应幂等: %v", err)
	}
}
