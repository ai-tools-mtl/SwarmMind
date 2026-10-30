//go:build windows

// Windows 打开旗标：Windows 无 FIFO open 阻塞语义（与 internal/node/inputs
// 口径一致，AGENTS 2026-10-25 三审 W2），无需 O_NONBLOCK 对应物；打开后同样
// 以 fd fstat 复核普通文件、非普通即关即拒（校验对象=读取对象）。
package agentdetect

import "os"

const openNoBlockFlag = os.O_RDONLY
