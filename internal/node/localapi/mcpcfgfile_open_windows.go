//go:build windows

// Windows 打开旗标（W03-02 宿主配置读写闸门，mcpcfgfile.go）：Windows 无 FIFO
// open 阻塞语义（与 internal/node/inputs 口径一致，AGENTS 2026-10-25 三审 W2），
// 无需 O_NONBLOCK 对应物；打开后同样以 fd fstat 复核普通文件、非普通即关即拒。
package localapi

import "os"

const (
	mcpcfgOpenReadFlag  = os.O_RDONLY
	mcpcfgOpenWriteFlag = os.O_WRONLY
)
