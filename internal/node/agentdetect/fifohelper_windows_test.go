//go:build windows

// Windows 无 FIFO open 阻塞语义（AGENTS 2026-10-25 三审 W2 同口径）：FIFO
// 防挂起用例仅在 Unix 平台执行，此处跳过。
package agentdetect

import "testing"

func makeFifoForTest(t *testing.T, _ string) {
	t.Skip("Windows 无 FIFO 语义：FIFO 防挂起用例仅在 Unix 平台执行")
}
