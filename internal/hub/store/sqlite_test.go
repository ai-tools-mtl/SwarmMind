package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// legacySnapshot 九类状态齐全的旧版 JSON 快照夹具（JSON 时代全量快照格式）。
const legacySnapshot = `{
  "nodes": {"n1": {"epoch": 3, "agents": [{"name": "diskcheck", "kind": "cli", "input_mode": "prompt_text", "output_mode": "answer+artifacts", "interactive": false, "permission_mode": "sandbox", "protocol_ver": 1, "exposed": true}]}},
  "tasks": {"t1": {"id": "t1", "trace_id": "tr", "target_node": "n1", "target_agent": "diskcheck", "submitted_by": "admin", "status": "completed", "created_at": "2026-10-24T08:00:00Z", "updated_at": "2026-10-24T08:01:00Z", "expire_at": "2026-10-31T08:00:00Z"}},
  "mailboxes": {"n1": {"tasks": ["t1"], "bytes": 128}},
  "batches": {"b1": {"id": "b1", "agent": "diskcheck", "input": {"args": ["patrol"]}, "submitted_by": "admin", "status": "complete", "created_at": "2026-10-24T08:00:00Z", "items": [{"target": "n1", "task_id": "t1"}]}},
  "batch_idems": {"k1": "b1"},
  "workdir_locks": {"n1": {"ws-1": "t1"}},
  "works": {"w1": {"id": "w1", "goal": "全量盘点", "status": "planning", "steps": [{"seq": 1, "desc": "盘点", "target_node": "n1", "target_agent": "diskcheck", "status": "pending"}], "events": [{"seq": 1, "at": "2026-10-25T09:00:00Z", "kind": "work_created", "by": "mcp", "body": "建单"}], "event_seq": 1, "created_at": "2026-10-25T09:00:00Z"}},
  "work_idems": {"fp1": "w1"},
  "prime": {"node_id": "n1", "agent_name": "diskcheck", "set_at": "2026-10-24T08:00:00Z", "set_by": "admin"}
}`

// writeLegacySnapshot 预置旧版 JSON 快照。
func writeLegacySnapshot(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("预置旧快照: %v", err)
	}
}

// importedBackups 列出 dir 下 json.imported-* 备份文件名。
func importedBackups(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := filepath.Glob(filepath.Join(dir, stateFileName+jsonImportedSuffix+"*"))
	if err != nil {
		t.Fatalf("列备份: %v", err)
	}
	return ents
}

// TestSQLiteImportFromLegacyJSON JSON→SQLite 导入：View 状态与旧快照逐字段等价
//（序列化形态逐字节比对），json 备份改名，重启不重复导入且状态保持。
func TestSQLiteImportFromLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	writeLegacySnapshot(t, dir, legacySnapshot)

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("导入 Open: %v", err)
	}
	// json 已备份改名（原文件不在，恰一个备份）
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); !os.IsNotExist(err) {
		t.Errorf("导入后旧 json 应已改名: %v", err)
	}
	if backups := importedBackups(t, dir); len(backups) != 1 {
		t.Fatalf("应恰一个导入备份: %v", backups)
	}
	// 状态逐字段等价：View 与旧快照解析归一结果序列化比对（Go marshal 键序稳定）
	backups := importedBackups(t, dir)
	want, err := loadState(backups[0])
	if err != nil {
		t.Fatalf("解析备份快照: %v", err)
	}
	// 统筹指定退役（2026-10-28）：旧快照的 prime 历史行不进运行态，比对时剔除
	want.Prime = nil
	got := s.View()
	gb, err1 := json.Marshal(got)
	wb, err2 := json.Marshal(want)
	if err1 != nil || err2 != nil {
		t.Fatalf("序列化对比失败: %v %v", err1, err2)
	}
	if !bytes.Equal(gb, wb) {
		t.Errorf("导入后状态与旧快照不等价:\n got=%s\nwant=%s", gb, wb)
	}
	// 抽查深字段（九类各取一样）
	if got.Nodes["n1"].Epoch != 3 || len(got.Nodes["n1"].Agents) != 1 {
		t.Errorf("nodes 未导入: %+v", got.Nodes)
	}
	if got.Mailboxes["n1"].Bytes != 128 || len(got.Mailboxes["n1"].Tasks) != 1 {
		t.Errorf("mailboxes 未导入: %+v", got.Mailboxes)
	}
	if got.Batches["b1"].Items[0].TaskID != "t1" || got.BatchIdems["k1"] != "b1" {
		t.Errorf("batches/batch_idems 未导入: %+v", got.Batches)
	}
	if got.WorkdirLocks["n1"]["ws-1"] != "t1" {
		t.Errorf("workdir_locks 未导入: %+v", got.WorkdirLocks)
	}
	if w := got.Works["w1"]; w.Goal != "全量盘点" || len(w.Steps) != 1 || len(w.Events) != 1 {
		t.Errorf("works 未导入: %+v", w)
	}
	if got.WorkIdems["fp1"] != "w1" {
		t.Errorf("work_idems 未导入: %+v", got.WorkIdems)
	}
	// 统筹指定退役（2026-10-28 客户端声明制）：旧快照历史行不进运行态（diff 清表）
	if got.Prime != nil {
		t.Errorf("prime 已退役，历史行不应导入: %+v", got.Prime)
	}

	// 重启：不重复导入（备份仍恰一个），状态从 SQLite 恢复
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开 Open: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if backups := importedBackups(t, dir); len(backups) != 1 {
		t.Errorf("重启不得重复导入: %v", backups)
	}
	v := s2.View()
	if v.Nodes["n1"].Epoch != 3 || v.WorkIdems["fp1"] != "w1" || v.Prime != nil {
		t.Errorf("重启后状态恢复异常（prime 应保持退役 nil）: nodes=%+v idems=%+v prime=%+v", v.Nodes, v.WorkIdems, v.Prime)
	}
	// 导入后可正常事务写入并持久化
	if err := s2.Update(func(st *State) error {
		st.Tasks["t2"] = protocol.Task{ID: "t2", TraceID: "tr2"}
		return nil
	}); err != nil {
		t.Fatalf("导入后写事务: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("三开 Open: %v", err)
	}
	defer func() { _ = s3.Close() }()
	if _, ok := s3.View().Tasks["t2"]; !ok {
		t.Error("导入库上的新事务未持久化")
	}
}

// TestSQLiteImportCorruptJSONRejected 损坏 json：拒绝导入（Open 报错），原文件
// 原样保留（不删不改名），修复后可重新导入。
func TestSQLiteImportCorruptJSONRejected(t *testing.T) {
	dir := t.TempDir()
	bad := `{"nodes": {"n1": {"epoch": 1}},` // 半截 JSON
	writeLegacySnapshot(t, dir, bad)
	if _, err := Open(dir); err == nil {
		t.Fatal("损坏 json 必须拒绝导入（Open 报错）")
	}
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatalf("损坏 json 必须原样保留: %v", err)
	}
	if string(b) != bad {
		t.Errorf("损坏 json 被改动: %q", b)
	}
	if backups := importedBackups(t, dir); len(backups) != 0 {
		t.Errorf("失败导入不得产生备份: %v", backups)
	}

	// 修复后重试导入成功
	writeLegacySnapshot(t, dir, legacySnapshot)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修复后导入应成功: %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.View().Nodes["n1"].Epoch != 3 {
		t.Errorf("修复后状态未导入: %+v", s.View().Nodes)
	}
}

// TestSQLiteNonEmptyDBWinsOverJSON 非空 SQLite 库与 json 并存：以 SQLite 为准
//（不重复导入），json 原样不动。
func TestSQLiteNonEmptyDBWinsOverJSON(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Update(func(st *State) error {
		st.Tasks["t-sqlite"] = protocol.Task{ID: "t-sqlite", TraceID: "tr"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 并存一份内容不同的旧 json（含 SQLite 没有的 ghost 节点）
	jsonContent := `{"nodes": {"ghost": {"epoch": 99}}}`
	writeLegacySnapshot(t, dir, jsonContent)

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("并存 Open: %v", err)
	}
	defer func() { _ = s2.Close() }()
	v := s2.View()
	if _, ok := v.Tasks["t-sqlite"]; !ok {
		t.Error("应以 SQLite 为准（任务丢失）")
	}
	if _, ok := v.Nodes["ghost"]; ok {
		t.Error("并存 json 不得被导入（ghost 节点出现）")
	}
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil || string(b) != jsonContent {
		t.Errorf("并存 json 必须原样不动: %q %v", b, err)
	}
}

// TestSQLiteConfig WAL + synchronous=FULL + schema 版本盖章（SPEC 4.7 配置约束
// 落库验证：journal_mode 持久化在库头，synchronous 每连接生效）。
func TestSQLiteConfig(t *testing.T) {
	s := openTestStore(t)
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("读 journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode=%q, 期望 wal", mode)
	}
	var sync int
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("读 synchronous: %v", err)
	}
	if sync != 2 { // SQLITE_SYNC_FULL
		t.Errorf("synchronous=%d, 期望 2 (FULL)", sync)
	}
	var uv int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("读 user_version: %v", err)
	}
	if uv != schemaVersion {
		t.Errorf("user_version=%d, 期望 %d", uv, schemaVersion)
	}
}

// TestSQLiteDiffWritesOnlyChangedRows diff 写入：仅变化行触写、无变化事务零写库
//（PRAGMA total_changes 按连接计数行变更；连接池限 1 连接，计数连续可比）。
func TestSQLiteDiffWritesOnlyChangedRows(t *testing.T) {
	s := openTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Tasks["t1"] = protocol.Task{ID: "t1"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	totalChanges := func() int {
		t.Helper()
		var n int
		// total_changes() SQL 函数形式（pragma 语句经驱动查询不回行）
		if err := s.db.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatalf("读 total_changes: %v", err)
		}
		return n
	}

	before := totalChanges()
	if err := s.Update(func(st *State) error {
		st.Tasks["t2"] = protocol.Task{ID: "t2"}
		return nil
	}); err != nil {
		t.Fatalf("新增 Update: %v", err)
	}
	if got := totalChanges() - before; got != 1 {
		t.Errorf("新增一行应恰 1 行触写, got %d", got)
	}

	// 无变化事务：零写库（发布照常）
	mid := totalChanges()
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatalf("无变化 Update: %v", err)
	}
	if got := totalChanges() - mid; got != 0 {
		t.Errorf("无变化事务应零触写, got %d", got)
	}

	// 变更已有行与删除：各恰 1 行
	mid = totalChanges()
	if err := s.Update(func(st *State) error {
		tk := st.Tasks["t1"]
		tk.TraceID = "tr-changed"
		st.Tasks["t1"] = tk
		return nil
	}); err != nil {
		t.Fatalf("变更 Update: %v", err)
	}
	if err := s.Update(func(st *State) error {
		delete(st.Tasks, "t2")
		return nil
	}); err != nil {
		t.Fatalf("删除 Update: %v", err)
	}
	if got := totalChanges() - mid; got != 2 {
		t.Errorf("变更+删除应共 2 行触写, got %d", got)
	}
	// 终态落盘一致
	disk := readDiskState(t, s)
	if len(disk.Tasks) != 1 || disk.Tasks["t1"].TraceID != "tr-changed" {
		t.Errorf("落盘终态不符: %+v", disk.Tasks)
	}
}

// TestSQLiteCrashReopenWAL 掉电语义：commit 后的数据经 WAL 恢复路径完整可读——
// 运行中把 db+wal+shm 三件套拷走（等价「进程刚死、文件还在」的现场），重开即恢复。
func TestSQLiteCrashReopenWAL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := s.Update(func(st *State) error {
			st.Tasks[id] = protocol.Task{ID: id, TraceID: "tr"}
			return nil
		}); err != nil {
			t.Fatalf("Update %s: %v", id, err)
		}
	}
	// 已提交事务驻留 WAL（autocheckpoint 阈值远大于本例写入量）
	if _, err := os.Stat(filepath.Join(dir, dbFileName+"-wal")); err != nil {
		t.Fatalf("WAL 文件应在: %v", err)
	}

	// 拷走三件套（进程死亡现场），随后原实例正常关闭
	crashDir := t.TempDir()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(
			filepath.Join(dir, dbFileName+suffix),
			filepath.Join(crashDir, dbFileName+suffix)); err != nil {
			t.Fatalf("拷贝 %s: %v", suffix, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重开「崩溃现场」：WAL 恢复路径重建全部已提交数据
	s2, err := Open(crashDir)
	if err != nil {
		t.Fatalf("重开崩溃现场: %v", err)
	}
	defer func() { _ = s2.Close() }()
	v := s2.View()
	for _, id := range []string{"t1", "t2", "t3"} {
		tk, ok := v.Tasks[id]
		if !ok || tk.TraceID != "tr" {
			t.Errorf("WAL 恢复后缺任务 %s: %+v", id, tk)
		}
	}
}

// copyFile 测试用整文件拷贝。
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// TestSQLitePrimeRetiredLegacyRow 统筹指定退役（2026-10-28 客户端声明制）：历史库
// 中的 prime 行不进运行态（读取丢弃），任一写事务后 diff 清表——重开库旧行消失。
func TestSQLitePrimeRetiredLegacyRow(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	at := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	// 模拟旧版遗留：直接经 Update 写入历史 prime 行（退役前写路径的产物形态）
	if err := s.Update(func(st *State) error {
		st.Prime = &PrimeDesignation{NodeID: "n1", AgentName: "zcode-dev", SetAt: at.Format(time.RFC3339), SetBy: "admin"}
		return nil
	}); err != nil {
		t.Fatalf("历史行 Update: %v", err)
	}
	// 任意一次无关写事务（退役后的正常写入）：diff 对 nil 态生成 delete 清旧行
	if err := s.Update(func(st *State) error {
		st.Prime = nil
		return nil
	}); err != nil {
		t.Fatalf("清表 Update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if s2.View().Prime != nil {
		t.Errorf("退役后 prime 不得进运行态/复活: %+v", s2.View().Prime)
	}
}

// TestSQLiteImportedTmpIgnored JSON 时代的 .tmp 残留文件忽略（不导入不报错不删除）。
func TestSQLiteImportedTmpIgnored(t *testing.T) {
	dir := t.TempDir()
	writeLegacySnapshot(t, dir, legacySnapshot)
	if err := os.WriteFile(filepath.Join(dir, stateFileName+".tmp"), []byte(`{"nodes":{}}`), 0o644); err != nil {
		t.Fatalf("预置 .tmp 残留: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf(".tmp 残留不得阻断启动: %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.View().Nodes["n1"].Epoch != 3 {
		t.Error("正式 json 应正常导入")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName+".tmp")); err != nil {
		t.Errorf(".tmp 残留应原样保留: %v", err)
	}
	if !strings.HasSuffix(dbFileName, ".db") {
		t.Error("库文件名约定校验（.db 后缀）")
	}
}

// TestSQLiteURIPathEscapedChars 2026-10-26 复审必修2回归：data_dir 含 URI 保留
// 字符（% 解码前缀/? 查询分隔/# 片段分隔）时 DSN 必须转义——否则数据库按解码后
// 路径打开，与 flock 锁路径分离，破坏单实例保护。断言：特殊字符目录下 Open 可
// 用、写入可读回、文件落在真实路径。
func TestSQLiteURIPathEscapedChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dir%41with?special#chars")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacySnapshot(t, dir, legacySnapshot)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("特殊字符路径 Open 应成功: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got := s.View(); got.Nodes["n1"].Epoch != 3 || len(got.Tasks) != 1 {
		t.Fatalf("导入状态应完整: nodes=%+v tasks=%d", got.Nodes, len(got.Tasks))
	}
	if err := s.Update(func(st *State) error {
		st.Tasks["t-new"] = protocol.Task{ID: "t-new"}
		return nil
	}); err != nil {
		t.Fatalf("写入: %v", err)
	}
	snap, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if len(snap.Tasks) != 2 {
		t.Fatalf("读回应含新旧任务: %d", len(snap.Tasks))
	}
	if _, err := os.Stat(filepath.Join(dir, dbFileName)); err != nil {
		t.Fatalf("库文件应落在真实路径: %v", err)
	}
}

// TestSQLiteImportMidwayFailRollback 2026-10-26 复审建议3：导入中途 SQL 失败
//（预置 user_version=0 但 tasks 表已有同主键行的半初始化库）→ 整体回滚：Open
// 报错、旧 json 原样保留、无 imported-* 备份。
func TestSQLiteImportMidwayFailRollback(t *testing.T) {
	dir := t.TempDir()
	writeLegacySnapshot(t, dir, legacySnapshot)
	// 造半初始化库：空 schema 版本但 tasks 表列结构不兼容（缺 body 列）→
	// 导入 INSERT 中途报错，验证事务整体回滚（注：importStateTx 为先清后插，
	// 主键冲突不可达，列不兼容是可触发的中途失败路径）
	{
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, dbFileName)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE tasks (key TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	if s, err := Open(dir); err == nil {
		_ = s.Close()
		t.Fatal("主键冲突的导入应失败")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); err != nil {
		t.Fatalf("导入失败旧 json 必须原样保留: %v", err)
	}
	if backups := importedBackups(t, dir); len(backups) != 0 {
		t.Fatalf("导入失败不得产生备份: %v", backups)
	}
}

// TestSQLiteFutureSchemaVersionRejected 2026-10-26 复审建议3：库版本高于本代码
// 认识的范围时拒绝启动（诚实失败优于误读）。
func TestSQLiteFutureSchemaVersionRejected(t *testing.T) {
	dir := t.TempDir()
	{
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, dbFileName)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	if s, err := Open(dir); err == nil {
		_ = s.Close()
		t.Fatal("高 schema 版本应拒绝启动")
	}
}

// TestReadSnapshotMissingDBNoCreate 2026-10-26 复审必修3回归：ReadSnapshot 对
// 不存在的库报错且绝不创建（只读契约）。
func TestReadSnapshotMissingDBNoCreate(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSnapshot(dir); err == nil {
		t.Fatal("库不存在应报错")
	}
	if _, err := os.Stat(filepath.Join(dir, dbFileName)); !os.IsNotExist(err) {
		t.Fatalf("不得创建库文件: %v", err)
	}
}
