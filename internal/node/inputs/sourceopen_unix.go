//go:build unix

// 源文件打开的 Unix 实现（2026-10-24 复审 R1）：从可信根路径逐段 openat 游走，
// 「不跟随符号链接/不阻塞」全部由内核在 open 原语上强制——不能用 os.Root：其
// Unix 实现末段遇 ELOOP 会读链接检查根内性并重试（跟随根内链接），用户传入的
// O_NOFOLLOW 被库逻辑架空（go1.24 os/root_unix.go rootOpenFileNolog）；也不能用
// 标准库 syscall：darwin 桩不导出 openat（Darwin 走 libc，无 SYS_OPENAT 常量）。
// 故经 golang.org/x/sys/unix（依赖白名单登记项，用途扩展登记见 go.mod 注释与
// SPEC 三：同模块零新增，darwin/linux 编译产物本就需含源打开逻辑）。
// 中间段 O_DIRECTORY|O_NOFOLLOW（非目录即拒、链接 ELOOP，且每段解析锚定在上一
// 段已打开的目录 fd 上——检查即打开，无 Lstat→Open 缺口）；末段
// O_NOFOLLOW|O_NONBLOCK（链接 ELOOP，FIFO 无写端 open 立即返回交由常规文件校验
// 拒绝——open 不返回则校验无从执行，hub 超时解不开源 node 的串行帧阻塞）。
// O_CLOEXEC 全程携带。
package inputs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openSourceFile 打开 root 内 rel（slash 口径、调用方已清洗净化）指向的常规文
// 件，返回文件与 fd fstat 元信息（常规文件校验归 OpenSourceFile 统一收口）。
func openSourceFile(root, rel string) (*os.File, fs.FileInfo, error) {
	dirfd, err := openRetry(func() (int, error) {
		return unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("产物根不可打开: %w", err)
	}
	parts := strings.Split(rel, "/")
	for _, cur := range parts[:len(parts)-1] {
		nfd, err := openRetry(func() (int, error) {
			return unix.Openat(dirfd, cur,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		})
		_ = unix.Close(dirfd)
		if err != nil {
			return nil, nil, fmt.Errorf("路径中间段 %s 为符号链接或非常规目录（拒绝跟随）: %w", cur, err)
		}
		dirfd = nfd
	}
	fd, err := openRetry(func() (int, error) {
		return unix.Openat(dirfd, parts[len(parts)-1],
			unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	})
	_ = unix.Close(dirfd)
	if err != nil {
		return nil, nil, fmt.Errorf("末段为符号链接或不可读（拒绝跟随）: %w", err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(root, filepath.FromSlash(rel)))
	fi, err := f.Stat() // fstat：实际打开对象的元数据
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// openRetry EINTR 重试（open/openat 可被信号打断后返回 EINTR）。
func openRetry(fn func() (int, error)) (int, error) {
	for {
		fd, err := fn()
		if err != unix.EINTR {
			return fd, err
		}
	}
}
