package localapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestOpenAgentStateWithOrchestratorRecordReboot 有声明后重启不炸（2026-10-28 生产
// 实证回归锁）：orchestrator-declare 线 OpenAgentState 构造时未初始化 orchOverride，
// 盘上 agents-state.json 带 orchestrator 字段（主人界面点过「设为管家」）后重启，
// disk.Orchestrator 非空触发 nil map 赋值 panic——node 反复崩溃（launchd exit 2）。
// 修复：构造时初始化 orchOverride。本用例锁定「含声明字段的旧文件重启可读可写」。
func TestOpenAgentStateWithOrchestratorRecordReboot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, agentsStateFileName)
	// 模拟声明已落盘的旧文件（真实生产形态：exposed + orchestrator 并存）
	old := `{"version":1,"exposed":{"echo-demo":{"exposed":true,"updated_at":"2026-10-28T10:00:00Z"}},"orchestrator":{"zcode-dev":{"orchestrator":true,"updated_at":"2026-10-28T10:30:00Z"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := []protocol.ExecProfile{{Name: "echo-demo"}, {Name: "zcode-dev"}}
	s, err := OpenAgentState(dir, baseline)
	if err != nil {
		t.Fatalf("含 orchestrator 字段的旧文件重启应可读（nil map panic 回归锁）: %v", err)
	}
	if !s.orchOverride["zcode-dev"].Orchestrator {
		t.Fatal("orchestrator 声明应加载进内存")
	}
	// 重启后再写入（persist 路径）不炸
	if _, err := s.SetAgentOrchestrator("zcode-dev", false); err != nil {
		t.Fatalf("重启后写声明不应 panic: %v", err)
	}
}
