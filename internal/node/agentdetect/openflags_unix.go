//go:build unix

// Unix 打开旗标：O_NONBLOCK 使 FIFO/部分设备的 open 立即返回（宿主配置末段
// 被换为 FIFO 时打开原语不挂起）；配合打开后 fd fstat 复核普通文件、非普通
// 即关即拒（校验对象=读取对象）。stdlib syscall 白名单外零新依赖。
package agentdetect

import (
	"os"
	"syscall"
)

const openNoBlockFlag = os.O_RDONLY | syscall.O_NONBLOCK
