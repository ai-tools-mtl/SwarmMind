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

// TestW08AC20ImportBackupRollbackReady 导入备份逐字节等价（回滚前置）：
// 备份文件名（json.imported-<unix>）改名回 stateFileName 后，与导入前的原快照
// 逐字节一致——回滚=一次改名，零数据损失。
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
}
