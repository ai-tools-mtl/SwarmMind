//go:build windows

// 源文件打开的 Windows 实现（2026-10-25 三审 W1/W2）：从可信根句柄逐段
// NtCreateFile 游走，每段（含末段）以 OBJ_DONT_REPARSE 打开——内核在 open 原语
// 上约束 reparse/link 解析：被打开组件是任何 reparse point（符号链接/junction/
// 挂载点/OneDrive 占位/Dedup 等过滤类 reparse）即 STATUS_REPARSE_POINT_
// ENCOUNTERED 拒绝。每段解析锚定上一段已打开的目录句柄——检查即打开，无
// Lstat→Open 换入窗口；前缀累计由锚定关系结构保证（三审 W1 功能回归锁）。
//
// 为什么不能用 os.Root：源码核实（go1.24 os/root_windows.go openat +
// root_openat.go doInRoot），Windows 的 os.Root 打开遇 reparse point 失败
// （ELOOP）后会经 readReparseLinkAt 读链并以 errSymlink 交 doInRoot **跟随**
// （根内相对链接被跟随，仅逃逸根外的绝对/junction 目标被拒）——「不跟随」契约
// 无法经 Root 落实，与 Unix 侧同病（见 sourceopen_unix.go）。OBJ_DONT_REPARSE
// 的旗标封装 O_NOFOLLOW_ANY 在 internal/syscall/windows（不可导入），白名单
// x/sys/windows（同模块零新增，登记见 go.mod/SPEC 三）直接提供
// NtCreateFile+OBJ_DONT_REPARSE 原语。
//
// 残余边界（如实声明）：
//   - 根路径自身为配置管理路径（与 runner exec 同源可信，非 agent 可控），打开
//     根句柄允许途经 reparse point（与 Unix 实现根打开无 O_NOFOLLOW 同口径）；
//   - 拒绝一切 reparse point 比合法文件形态更宽（OneDrive 占位/Dedup 文件也被
//     拒）——产物口径为本地卷常规文件，可接受；换来的恰是「不跟随」完整语义；
//   - 无 O_NONBLOCK 需求（四审 V2 收窄声明）：命名管道不在目录句柄相对可达的
//     NTFS 命名空间（Win32 \\.\pipe 设备名重写不生效于 RootDirectory 相对名），
//     本路径（目录句柄相对打开）无命名管道/FIFO 型 open 等待；但常规文件打开仍
//     可能等待 oplock break（过滤器授予语义，非本契约约束范围）；网络重定向路
//     径不在产物根支持口径（本机 workdir）；
//   - 本实现形态测试为 windows 构建标签文件（sourceopen_windows_test.go），在
//     Windows 机器/CI 执行；开发环境 darwin 仅验证构建与 vet。
package inputs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openSourceFile 打开 root 内 rel（slash 口径、调用方已清洗净化）指向的对象，
// 返回文件与 fd fstat 元信息（常规文件校验归 OpenSourceFile 统一收口）。
func openSourceFile(root, rel string) (*os.File, fs.FileInfo, error) {
	// 根句柄：与 os.OpenRoot 同源（go1.24 openRootNolog 同用 syscall.Open——读
	// 方式自带 FILE_FLAG_BACKUP_SEMANTICS 可开目录句柄），随后显式确认目录形态。
	rootfd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("产物根不可打开: %w", err)
	}
	// 四审 V1：根句柄全退出路径释放——defer 单点关闭（无重复关闭），覆盖根校验
	// 失败/中间段失败/末段失败/fstat 失败/成功全部路径。成功路径下返回的文件句柄
	// 是末段 NtCreateFile 的产物（Windows 句柄为对象引用，不依赖根句柄存活），
	// 根句柄仅游走期间需存活，函数退出时关闭不影响已返回文件。
	defer func() { _ = syscall.CloseHandle(rootfd) }()
	var bi syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(rootfd, &bi); err != nil {
		return nil, nil, fmt.Errorf("产物根不可打开: %w", err)
	}
	if bi.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, nil, fmt.Errorf("产物根不可打开: 不是目录")
	}

	parts := strings.Split(rel, "/")
	dirfd := syscall.Handle(rootfd)
	for _, cur := range parts[:len(parts)-1] {
		h, err := openatNoReparse(dirfd, cur, windows.FILE_DIRECTORY_FILE)
		if dirfd != syscall.Handle(rootfd) {
			_ = syscall.CloseHandle(dirfd)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("路径中间段 %s 为符号链接/reparse point 或非常规目录（拒绝跟随）: %v", cur, err)
		}
		dirfd = h
	}
	h, err := openatNoReparse(dirfd, parts[len(parts)-1], 0)
	if dirfd != syscall.Handle(rootfd) {
		_ = syscall.CloseHandle(dirfd)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("末段为符号链接/reparse point 或不可读（拒绝跟随）: %v", err)
	}
	f := os.NewFile(uintptr(h), filepath.Join(root, filepath.FromSlash(rel)))
	fi, err := f.Stat() // fstat：实际打开对象的元数据
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// openatNoReparse 相对锚定目录句柄打开单个路径组件（RootDirectory 相对名；组件
// 名经 slash 拆段不含分隔符，不经 Win32 设备名/路径重写）。OBJ_DONT_REPARSE 打
// 开时约束该组件的 reparse/link 解析：组件为任何 reparse point 即
// STATUS_REPARSE_POINT_ENCOUNTERED（显式映射为「reparse point」文案，其余
// NTSTATUS 原样上抛）。extraOptions 传 FILE_DIRECTORY_FILE 约束中间段必须为真
// 实目录（STATUS_NOT_A_DIRECTORY）。
func openatNoReparse(dir syscall.Handle, name string, extraOptions uint32) (syscall.Handle, error) {
	on, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(dir),
		ObjectName:    on,
		Attributes:    windows.OBJ_DONT_REPARSE,
	}
	var h windows.Handle
	st := windows.NtCreateFile(&h,
		windows.FILE_GENERIC_READ,
		oa,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT|extraOptions,
		0, 0)
	if st != nil {
		if sts, ok := st.(windows.NTStatus); ok && sts == windows.STATUS_REPARSE_POINT_ENCOUNTERED {
			return syscall.InvalidHandle, fmt.Errorf("reparse point（拒绝跟随）")
		}
		return syscall.InvalidHandle, st
	}
	return syscall.Handle(h), nil
}
