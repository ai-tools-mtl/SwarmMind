//go:build unix

// FIFO 测试辅助（仅 Unix）：stdlib syscall.Mkfifo 造命名管道（零新依赖），
// 用于「末段被换为 FIFO 时打开原语不挂起」的限时用例。
package agentdetect

import (
	"syscall"
	"testing"
)

// makeFifoForTest 在指定路径造 FIFO；须在 t.TempDir 内使用（清理随 TempDir）。
func makeFifoForTest(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("造 FIFO 失败 %s: %v", path, err)
	}
}
