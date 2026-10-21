//go:build unix

package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFileName data_dir 内单实例锁文件名（SPEC 4.7：OS 文件锁 flock，进程退出自动释放；
// 不用 O_EXCL 锁文件，避免 SIGKILL 残留永久阻塞重启）。
const lockFileName = "lock"

// Lock data_dir 单实例文件锁句柄。
type Lock struct {
	f *os.File
}

// AcquireLock 对 dataDir 加 flock 排他非阻塞锁；目录不存在则创建；
// 已有其他实例持锁时返回错误（调用方应退出报错，SPEC 4.7）。
func AcquireLock(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("ledger: 创建 data_dir %s 失败: %w", dataDir, err)
	}
	f, err := os.OpenFile(filepath.Join(dataDir, lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ledger: 打开锁文件失败: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("ledger: data_dir %s 已被其他实例锁定（flock 冲突）: %w", dataDir, err)
	}
	return &Lock{f: f}, nil
}

// Release 释放锁并关闭锁文件；进程退出亦会自动释放（SPEC 4.7）。
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
