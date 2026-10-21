//go:build unix

package store

import "testing"

// TestAcquireLockMutualExclusion flock 互斥：同目录二次加锁失败，释放后可重取；不同目录互不影响。
func TestAcquireLockMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	l1, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("首次加锁: %v", err)
	}
	if l2, err := AcquireLock(dir); err == nil {
		_ = l2.Release()
		t.Fatal("同一 data_dir 第二次加锁应失败（flock 互斥）")
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("释放: %v", err)
	}
	l2, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("释放后重新加锁: %v", err)
	}
	l2.Release()

	dir2 := t.TempDir()
	l3, err := AcquireLock(dir2)
	if err != nil {
		t.Fatalf("另一目录加锁应不受影响: %v", err)
	}
	l3.Release()
}

// TestAcquireLockCreatesDir 目录不存在时自动创建。
func TestAcquireLockCreatesDir(t *testing.T) {
	dir := t.TempDir() + "/nested/data"
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer l.Release()
}
