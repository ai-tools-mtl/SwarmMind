package store

// session_test.go 会话注册表持久化（T2，SPEC 六.五）：round-trip、diff 增量写、
// v1→v2 schema 迁移（旧库补 sessions 表，既有数据零触碰）。
import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

func testSession(id string) Session {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	closed := now.Add(time.Hour)
	return Session{
		ID: id, NodeID: "mac-mini", AgentName: "coder",
		Adapter: protocol.SessionAdapterCodex, PermissionMode: "workspace-write",
		Cwd: "/tmp/proj", Status: SessionStatusClosed,
		Turns: []SessionTurn{
			{Seq: 1, TaskID: "t-1", Prompt: "首轮", Response: "echo:首轮", NativeIDOk: true,
				WorkResult: protocol.WorkUnverified, CreatedAt: now, FinishedAt: now.Add(time.Minute)},
			{Seq: 2, TaskID: "t-2", Prompt: "失败轮", Error: "turn_failed: exit 4",
				CreatedAt: now.Add(2 * time.Minute), FinishedAt: now.Add(3 * time.Minute)},
		},
		CreatedAt: now, UpdatedAt: closed, ClosedAt: &closed,
	}
}

// TestSessionRoundTrip 会话条目随事务落盘 → 重开恢复逐字段一致（含轮次账本与
// ClosedAt 指针语义）。
func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := testSession("s-roundtrip")
	if err := st.Update(func(s *State) error { s.Sessions[want.ID] = want; return nil }); err != nil {
		t.Fatalf("写会话: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer st2.Close()
	got, ok := st2.View().Sessions[want.ID]
	if !ok {
		t.Fatal("重开后会话丢失")
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("round-trip 形态漂移:\n got %s\nwant %s", gb, wb)
	}
}

// TestSessionDiffIncremental 未变会话不触写（diff 纪律）：只改 a（b 原样），
// diff 应恰含 sessions 表一条 UPSERT、无 DELETE、无其它表条目。
func TestSessionDiffIncremental(t *testing.T) {
	a, b := testSession("s-a"), testSession("s-b")
	changed := a
	changed.Status = SessionStatusActive
	changed.ClosedAt = nil
	oldState := NewState()
	oldState.Sessions[a.ID] = testSession("s-a")
	oldState.Sessions[b.ID] = b
	newState := NewState()
	newState.Sessions[a.ID] = changed
	newState.Sessions[b.ID] = b
	dd, err := diffState(oldState, newState)
	if err != nil {
		t.Fatalf("diffState: %v", err)
	}
	if len(dd) != 1 || dd[0].table != "sessions" || len(dd[0].upserts) != 1 || len(dd[0].deletes) != 0 {
		t.Fatalf("应恰一条 sessions UPSERT: %+v", dd)
	}
	if dd[0].upserts[0].key != a.ID {
		t.Fatalf("UPSERT 键应 %s: %s", a.ID, dd[0].upserts[0].key)
	}
}

// TestSQLiteV1ToV2Migration 旧库（schema v1，无 sessions 表）打开即迁移：补表 +
// 盖 v2，既有数据原样可读、新表立即可写。
func TestSQLiteV1ToV2Migration(t *testing.T) {
	dir := t.TempDir()
	// 手工构造 v1 库：九表 DDL（无 sessions）+ user_version=1 + 一条既有任务
	db, err := sql.Open("sqlite", "file:"+sqliteURIPath(dir+"/"+dbFileName))
	if err != nil {
		t.Fatalf("构造 v1 库: %v", err)
	}
	v1DDL := `CREATE TABLE IF NOT EXISTS nodes         (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tasks          (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS mailboxes      (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS batches        (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS batch_idems    (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workdir_locks  (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS works          (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS work_idems     (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS prime          (key TEXT PRIMARY KEY, body TEXT NOT NULL);`
	if _, err := db.Exec(v1DDL); err != nil {
		t.Fatalf("v1 DDL: %v", err)
	}
	legacyTask := `{"id":"t-old","trace_id":"tr","target_node":"n1","target_agent":"a","input":{"args":[]},"submitted_by":"admin","status":"completed","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:01:00Z","expire_at":"2026-10-08T00:00:00Z"}`
	if _, err := db.Exec(`INSERT INTO tasks (key, body) VALUES ('t-old', ?)`, legacyTask); err != nil {
		t.Fatalf("播种 v1 任务: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("盖 v1 版本: %v", err)
	}
	db.Close()

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("v1 库打开（应自动迁移）: %v", err)
	}
	defer st.Close()
	// 既有数据零触碰
	if _, ok := st.View().Tasks["t-old"]; !ok {
		t.Fatal("迁移后既有任务丢失")
	}
	// 新表立即可写 + 重启可读
	want := testSession("s-migrated")
	if err := st.Update(func(s *State) error { s.Sessions[want.ID] = want; return nil }); err != nil {
		t.Fatalf("迁移库写会话: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("迁移库重开: %v", err)
	}
	defer st2.Close()
	if _, ok := st2.View().Sessions[want.ID]; !ok {
		t.Fatal("迁移库会话重启后丢失")
	}
	// 版本盖章 v2（防旧代码误读）
	var v int
	d2, err := sql.Open("sqlite", "file:"+sqliteURIPath(dir+"/"+dbFileName)+"?mode=ro")
	if err != nil {
		t.Fatalf("只读开库: %v", err)
	}
	defer d2.Close()
	if err := d2.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("迁移后 user_version 应 %d, got %d err=%v", schemaVersion, v, err)
	}
}

// TestSQLiteFutureVersionRejected 库版本高于本代码：拒绝启动（诚实失败优于误读，
// 既有契约回归）。
func TestSQLiteFutureVersionRejected(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+sqliteURIPath(dir+"/"+dbFileName))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("盖版本: %v", err)
	}
	db.Close()
	if st, err := Open(dir); err == nil {
		st.Close()
		t.Fatal("高版本库应拒绝启动")
	}
}
