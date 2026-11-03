package store

// w08_rollback_test.go AC20「迁移和回滚前置」锚（W08-02，docs/tasks/v3/
// W08-recovery.md 机械验收）：JSON→SQLite 导入的备份改名产物必须与原快照**逐字节
// 等价**——回滚前置=把备份文件改名回 stateFileName 即完整回到 SQLite 前状态，
// 不依赖任何「导入是有损的，回退无从谈起」的侥幸。既有
// TestSQLiteImportFromLegacyJSON 锚定导入正确性与重启不重导；本测试补回滚前置
// 半边（AC20 显式点名）。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestW08AC20ImportBackupRollbackReady 导入备份逐字节等价 + 实做改名回位
// （回滚前置，W08-02 codex 建议收窄口径）：备份文件（json.imported-<unix>）内容
// 与导入前原快照逐字节一致，且改名回 stateFileName 后材料完整就位——回滚=把
// 备份材料改回原名的纯文件操作，可供 SQLite 前版本（JSON 快照读取器）原样读取
// （旧版本二进制启动不在单测范围，材料完整性为可验证的回滚前置）。
func TestW08AC20ImportBackupRollbackReady(t *testing.T) {
	dir := t.TempDir()
	writeLegacySnapshot(t, dir, legacySnapshot)
	original, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatalf("读原快照: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("导入 Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backups := importedBackups(t, dir)
	if len(backups) != 1 {
		t.Fatalf("应恰一个导入备份: %v", backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("读导入备份: %v", err)
	}
	if string(backup) != string(original) {
		t.Fatal("导入备份必须与原快照逐字节等价（回滚前置——AC20）")
	}
	// 实做回滚动作：备份改名回原名（覆盖 SQLite 库旁的占位——回到 JSON 快照形态），
	// 字节与原快照一致=备份材料完整可回退。
	rollback := filepath.Join(dir, stateFileName)
	if err := os.Rename(backups[0], rollback); err != nil {
		t.Fatalf("回滚改名: %v", err)
	}
	restored, err := os.ReadFile(rollback)
	if err != nil {
		t.Fatalf("读回滚产物: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatal("改名回位后必须与原快照逐字节一致（备份材料完整）")
	}
}
