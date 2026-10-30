//go:build unix

// Unix 打开旗标（W03-02 宿主配置读写闸门，mcpcfgfile.go）：O_NONBLOCK 使 FIFO/
// 部分设备的 open 立即返回（末段被换为 FIFO 时打开原语不挂起），配合打开后 fd
// fstat 复核普通文件、非普通即关即拒（校验对象=读取对象；agentdetect openflags
// 同款先例，stdlib syscall 白名单外零新依赖）。
package localapi

import (
	"os"
	"syscall"
)

const (
	mcpcfgOpenReadFlag  = os.O_RDONLY | syscall.O_NONBLOCK
	mcpcfgOpenWriteFlag = os.O_WRONLY | syscall.O_NONBLOCK
)
