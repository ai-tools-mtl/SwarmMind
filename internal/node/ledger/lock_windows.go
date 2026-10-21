//go:build !unix

package ledger

import "errors"

// Lock data_dir 单实例锁句柄（Windows 占位）。
type Lock struct{}

// AcquireLock Windows flock 实现随 V1 服务化补充（SPEC 4.7）；当前直接报错拒绝启动。
func AcquireLock(dataDir string) (*Lock, error) {
	return nil, errors.New("ledger: Windows flock 单实例锁随 V1 服务化补充（SPEC 4.7）")
}

// Release 空实现。
func (l *Lock) Release() error { return nil }
