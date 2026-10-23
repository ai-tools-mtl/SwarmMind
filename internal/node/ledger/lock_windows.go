//go:build windows

package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockFileName data_dir 内单实例锁文件名（与 lock.go unix 侧同文件名同语义，
// SPEC 4.7：OS 文件锁，进程退出自动释放；不用 O_EXCL 锁文件，避免 SIGKILL 残留
// 永久阻塞重启）。
const lockFileName = "lock"

// lockRangeLow/lockRangeHigh LockFileEx 锁定范围（0 ~ 2^64-1，整文件对齐 flock）。
const (
	lockRangeLow  = ^uint32(0)
	lockRangeHigh = ^uint32(0)
)

// Lock data_dir 单实例文件锁句柄（Windows 侧）。
type Lock struct {
	h windows.Handle
}

// AcquireLock 对 dataDir 加排他单实例锁（对齐 unix 侧 flock LOCK_EX|LOCK_NB）：
// CreateFile 独占打开（shareMode=0，其他进程/本进程再次打开即被 OS 拒绝）+
// LockFileEx 整文件独占非阻塞锁双保险；目录不存在则创建；已有其他实例持锁时
// 返回错误（调用方应退出报错，SPEC 4.7）。句柄随进程退出由 OS 关闭，崩溃/被杀
// 不留永久锁，与 flock「进程退出自动释放」同语义。
func AcquireLock(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("ledger: 创建 data_dir %s 失败: %w", dataDir, err)
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(dataDir, lockFileName))
	if err != nil {
		return nil, fmt.Errorf("ledger: 锁文件路径非法: %w", err)
	}
	h, err := windows.CreateFile(path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // 不共享：独占（第二个实例任何打开方式都报共享冲突）
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("ledger: data_dir %s 已被其他实例锁定（共享冲突）: %w", dataDir, err)
		}
		return nil, fmt.Errorf("ledger: 打开锁文件失败: %w", err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockRangeLow, lockRangeHigh, ol); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("ledger: data_dir %s 已被其他实例锁定（LockFileEx 冲突）: %w", dataDir, err)
	}
	return &Lock{h: h}, nil
}

// Release 释放锁并关闭句柄；进程退出亦会自动释放（SPEC 4.7）。幂等。
func (l *Lock) Release() error {
	if l == nil || l.h == 0 {
		return nil
	}
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(l.h, 0, lockRangeLow, lockRangeHigh, ol)
	err := windows.CloseHandle(l.h)
	l.h = 0
	return err
}
