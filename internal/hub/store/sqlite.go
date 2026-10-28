// sqlite.go hub 状态 SQLite 持久层（SPEC 4.7/4.9）：实体分表 + key + JSON 体列，
// 事务按 diff 增量写入（禁止全表重写），旧 hub_state.json 首启自动导入。
// 内存仍驻留全量 State，SQLite 只是持久层（先写后见纪律见 transaction.go）。
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（SPEC 三白名单登记项：无 cgo 保 Windows 构建与交叉编译，单文件库零部署）

	"github.com/mtl802/swarmmind/internal/protocol"
)

// dbFileName hub 状态 SQLite 库文件名（相对 data_dir）。
const dbFileName = "hub_state.db"

// schemaVersion 当前 schema 版本（PRAGMA user_version）。演进时 +1 并在 Open
// 写迁移路径；库版本高于本代码认识的范围时拒绝启动（诚实失败优于误读）。
const schemaVersion = 1

// jsonImportedSuffix 旧 JSON 快照导入成功后的备份改名后缀
//（hub_state.json → hub_state.json.imported-<unix秒>）。
const jsonImportedSuffix = ".imported-"

// schemaDDL 实体分表：九类状态各一张表，行 = key（map 键，主键）+ body（条目
// JSON 体）。prime 为单例行（key 恒 'prime'，nil = 无行）。
const schemaDDL = `CREATE TABLE IF NOT EXISTS nodes         (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tasks          (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS mailboxes      (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS batches        (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS batch_idems    (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workdir_locks  (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS works          (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS work_idems     (key TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS prime          (key TEXT PRIMARY KEY, body TEXT NOT NULL);`

// sqliteURIPath 把文件路径转成 file: URI 的路径组件（2026-10-26 复审必修2）：
// 反斜杠转正斜杠（Windows 盘符），并转义 URI 结构保留字符（% 解码前缀、
// ? 查询分隔、# 片段分隔）——否则路径含字面量 %41 之类会被 URI 解码，数据库
// 路径与 flock 锁路径分离，破坏单实例保护。
func sqliteURIPath(path string) string {
	return strings.NewReplacer(
		"%", "%25",
		"?", "%3F",
		"#", "%23",
	).Replace(filepath.ToSlash(path))
}

// openDB 打开（或创建）data_dir 下的 SQLite 状态库。WAL + synchronous=FULL +
// busy_timeout 经 DSN _pragma 参数作用于每条新建连接；连接池限 1 条连接——
// 与单写 goroutine 模型对齐（SQLite 单写者天然吻合，SPEC 4.7）。
func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + sqliteURIPath(path) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开 SQLite 状态库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 打开 SQLite 状态库失败: %w", err)
	}
	return db, nil
}

// schemaState 读库的 schema 版本（0 = 新空库/未初始化）。
func schemaState(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: 读 schema 版本失败: %w", err)
	}
	return v, nil
}

// initSchemaTx 建表并盖章 schema 版本（事务内执行：DDL 与 user_version 均随
// 事务回滚，导入原子性覆盖「建表+导数据+版本」全过程）。
func initSchemaTx(tx *sql.Tx) error {
	if _, err := tx.Exec(schemaDDL); err != nil {
		return fmt.Errorf("store: 建表失败: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("store: 盖 schema 版本失败: %w", err)
	}
	return nil
}

// dbQueryer *sql.DB 与 *sql.Tx 的公共查询面（loadStateFromDB 同时服务启动
// 加载与 ReadSnapshot 读事务）。
type dbQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// loadStateFromDB 从 SQLite 全量加载重建内存 State（启动恢复，SPEC 4.7）。
// q 传入 *sql.DB（启动）或 *sql.Tx（ReadSnapshot 读事务，保跨表一致）。
func loadStateFromDB(q dbQueryer) (*State, error) {
	st := NewState()
	if err := scanTable(q, "nodes", func(k string, body []byte) error {
		var r NodeRecord
		if err := json.Unmarshal(body, &r); err != nil {
			return fmt.Errorf("store: nodes.%s 条目损坏: %w", k, err)
		}
		st.Nodes[k] = r
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanTable(q, "tasks", func(k string, body []byte) error {
		var t protocol.Task
		if err := json.Unmarshal(body, &t); err != nil {
			return fmt.Errorf("store: tasks.%s 条目损坏: %w", k, err)
		}
		st.Tasks[k] = t
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanTable(q, "mailboxes", func(k string, body []byte) error {
		var m Mailbox
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("store: mailboxes.%s 条目损坏: %w", k, err)
		}
		st.Mailboxes[k] = m
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanTable(q, "batches", func(k string, body []byte) error {
		var b ControlBatch
		if err := json.Unmarshal(body, &b); err != nil {
			return fmt.Errorf("store: batches.%s 条目损坏: %w", k, err)
		}
		st.Batches[k] = b
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanStringTable(q, "batch_idems", st.BatchIdems); err != nil {
		return nil, err
	}
	if err := scanTable(q, "workdir_locks", func(k string, body []byte) error {
		var locks map[string]string
		if err := json.Unmarshal(body, &locks); err != nil {
			return fmt.Errorf("store: workdir_locks.%s 条目损坏: %w", k, err)
		}
		st.WorkdirLocks[k] = locks
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanTable(q, "works", func(k string, body []byte) error {
		var w Work
		if err := json.Unmarshal(body, &w); err != nil {
			return fmt.Errorf("store: works.%s 条目损坏: %w", k, err)
		}
		st.Works[k] = w
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanStringTable(q, "work_idems", st.WorkIdems); err != nil {
		return nil, err
	}
	var primeBody []byte
	err := q.QueryRow(`SELECT body FROM prime WHERE key = 'prime'`).Scan(&primeBody)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 未指定统筹（nil，前端回退名字约定）
	case err != nil:
		return nil, fmt.Errorf("store: 读 prime 失败: %w", err)
	default:
		var legacy PrimeDesignation
		if err := json.Unmarshal(primeBody, &legacy); err != nil {
			return nil, fmt.Errorf("store: prime 条目损坏: %w", err)
		}
		_ = legacy // 统筹指定退役（2026-10-28 客户端声明制）：历史行不进运行态，
		// diffPrime 对 nil 态生成 delete，旧表行随首次写事务自动清理。
	}
	return st, nil
}

// scanTable 遍历单表全部行；body 以字节切片交给回调（零拷贝借用，回调内消费）。
func scanTable(q dbQueryer, table string, fn func(key string, body []byte) error) error {
	rows, err := q.Query("SELECT key, body FROM " + table)
	if err != nil {
		return fmt.Errorf("store: 读 %s 表失败: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var body []byte
		if err := rows.Scan(&k, &body); err != nil {
			return fmt.Errorf("store: 读 %s 表失败: %w", table, err)
		}
		if err := fn(k, body); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: 读 %s 表失败: %w", table, err)
	}
	return nil
}

// scanStringTable map[string]string 型映射表（幂等键映射：body 即 JSON 字符串标量）。
func scanStringTable(q dbQueryer, table string, into map[string]string) error {
	return scanTable(q, table, func(k string, body []byte) error {
		var v string
		if err := json.Unmarshal(body, &v); err != nil {
			return fmt.Errorf("store: %s.%s 条目损坏: %w", table, k, err)
		}
		into[k] = v
		return nil
	})
}

// ---------- diff 写入（SPEC 4.7：仅变化条目触写，禁止全表重写） ----------

// kv 单行写入载荷。
type kv struct {
	key  string
	body string
}

// tableDiff 单表变化集：UPSERT 与 DELETE 两个方向。
type tableDiff struct {
	table   string
	upserts []kv
	deletes []string
}

// diffState 对比 old/new 全部九类状态，产出增量变化集。条目相等比较用 JSON
// 序列化比较（Go encoding/json 对 map 键排序、struct 按字段序输出，形态稳定；
// 量级小，成本可忽略）。无任何变化返回 nil（调用方跳过写库）。
func diffState(old, next *State) ([]tableDiff, error) {
	var diffs []tableDiff
	if err := diffMap("nodes", old.Nodes, next.Nodes, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("tasks", old.Tasks, next.Tasks, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("mailboxes", old.Mailboxes, next.Mailboxes, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("batches", old.Batches, next.Batches, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("batch_idems", old.BatchIdems, next.BatchIdems, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("workdir_locks", old.WorkdirLocks, next.WorkdirLocks, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("works", old.Works, next.Works, &diffs); err != nil {
		return nil, err
	}
	if err := diffMap("work_idems", old.WorkIdems, next.WorkIdems, &diffs); err != nil {
		return nil, err
	}
	if err := diffPrime(old.Prime, next.Prime, &diffs); err != nil {
		return nil, err
	}
	return diffs, nil
}

// diffMap 单表对比：新增/变更 → UPSERT，删除 → DELETE，未变 → 跳过。
func diffMap[T any](table string, oldM, newM map[string]T, diffs *[]tableDiff) error {
	d := tableDiff{table: table}
	for k, nv := range newM {
		if ov, ok := oldM[k]; ok {
			same, err := jsonEqual(ov, nv)
			if err != nil {
				return fmt.Errorf("store: %s.%s 对比失败: %w", table, k, err)
			}
			if same {
				continue
			}
		}
		b, err := json.Marshal(nv)
		if err != nil {
			return fmt.Errorf("store: %s.%s 序列化失败: %w", table, k, err)
		}
		d.upserts = append(d.upserts, kv{key: k, body: string(b)})
	}
	for k := range oldM {
		if _, ok := newM[k]; !ok {
			d.deletes = append(d.deletes, k)
		}
	}
	if len(d.upserts)+len(d.deletes) > 0 {
		*diffs = append(*diffs, d)
	}
	return nil
}

// diffPrime 单例统筹指定：nil/非 nil 与值变化统一按行 upsert/delete 表达。
func diffPrime(old, next *PrimeDesignation, diffs *[]tableDiff) error {
	oldNil, nextNil := old == nil, next == nil
	if oldNil && nextNil {
		return nil
	}
	if !oldNil && !nextNil {
		same, err := jsonEqual(*old, *next)
		if err != nil {
			return fmt.Errorf("store: prime 对比失败: %w", err)
		}
		if same {
			return nil
		}
	}
	d := tableDiff{table: "prime"}
	if nextNil {
		d.deletes = append(d.deletes, "prime")
	} else {
		b, err := json.Marshal(*next)
		if err != nil {
			return fmt.Errorf("store: prime 序列化失败: %w", err)
		}
		d.upserts = append(d.upserts, kv{key: "prime", body: string(b)})
	}
	*diffs = append(*diffs, d)
	return nil
}

// jsonEqual 两值 JSON 序列化比较（相等性口径：序列化形态一致）。
func jsonEqual(a, b any) (bool, error) {
	ab, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

// applyDiffs 在事务内应用增量变化集（UPSERT/DELETE 仅变化行）。
func applyDiffs(tx *sql.Tx, diffs []tableDiff) error {
	for _, d := range diffs {
		for _, row := range d.upserts {
			if _, err := tx.Exec(
				"INSERT INTO "+d.table+" (key, body) VALUES (?, ?) "+
					"ON CONFLICT(key) DO UPDATE SET body = excluded.body",
				row.key, row.body); err != nil {
				return fmt.Errorf("store: 写 %s.%s 失败: %w", d.table, row.key, err)
			}
		}
		for _, k := range d.deletes {
			if _, err := tx.Exec("DELETE FROM "+d.table+" WHERE key = ?", k); err != nil {
				return fmt.Errorf("store: 删 %s.%s 失败: %w", d.table, k, err)
			}
		}
	}
	return nil
}

// importStateTx 单事务全量导入旧 JSON 快照状态（仅导入路径：新空库，先清后插
// 的全表写入只发生在这里；常规事务走 diffState，禁止全表重写）。
func importStateTx(tx *sql.Tx, st *State) error {
	for _, t := range []string{"nodes", "tasks", "mailboxes", "batches", "batch_idems",
		"workdir_locks", "works", "work_idems", "prime"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("store: 清空 %s 表失败: %w", t, err)
		}
	}
	if err := insertMapTx(tx, "nodes", st.Nodes); err != nil {
		return err
	}
	if err := insertMapTx(tx, "tasks", st.Tasks); err != nil {
		return err
	}
	if err := insertMapTx(tx, "mailboxes", st.Mailboxes); err != nil {
		return err
	}
	if err := insertMapTx(tx, "batches", st.Batches); err != nil {
		return err
	}
	if err := insertMapTx(tx, "batch_idems", st.BatchIdems); err != nil {
		return err
	}
	if err := insertMapTx(tx, "workdir_locks", st.WorkdirLocks); err != nil {
		return err
	}
	if err := insertMapTx(tx, "works", st.Works); err != nil {
		return err
	}
	if err := insertMapTx(tx, "work_idems", st.WorkIdems); err != nil {
		return err
	}
	if st.Prime != nil {
		b, err := json.Marshal(*st.Prime)
		if err != nil {
			return fmt.Errorf("store: prime 序列化失败: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO prime (key, body) VALUES ('prime', ?)", string(b)); err != nil {
			return fmt.Errorf("store: 写 prime 失败: %w", err)
		}
	}
	return nil
}

// insertMapTx 整表插入（导入路径）。
func insertMapTx[T any](tx *sql.Tx, table string, m map[string]T) error {
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("store: %s.%s 序列化失败: %w", table, k, err)
		}
		if _, err := tx.Exec("INSERT INTO "+table+" (key, body) VALUES (?, ?)", k, string(b)); err != nil {
			return fmt.Errorf("store: 写 %s.%s 失败: %w", table, k, err)
		}
	}
	return nil
}

// importedBackupName 旧 JSON 快照导入后的备份文件名（同目录，unix 秒戳）。
func importedBackupName(jsonPath string, now time.Time) string {
	return jsonPath + jsonImportedSuffix + fmt.Sprint(now.Unix())
}

// ReadSnapshot 从 data_dir 的状态库独立读出全量状态（2026-10-26 复审必修3：
// 只读连接 + 单读事务，保跨表一致——并发提交下不会拼出「旧 works + 新
// work_idems」这类从未存在过的混合态；库不存在时报错，绝不创建）。供核心包
// 测试做「落盘一致/零污染」断言与运维检查；生产读写路径一律经 Store（先写后见
// 事务），不经本函数。
func ReadSnapshot(dir string) (*State, error) {
	dbPath := filepath.Join(dir, dbFileName)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("store: 状态库不存在: %w", err)
	}
	dsn := "file:" + sqliteURIPath(dbPath) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 只读打开状态库失败: %w", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil) // DEFERRED 读事务：首个读起快照，跨表一致
	if err != nil {
		return nil, fmt.Errorf("store: 开启读事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := loadStateFromDB(tx)
	if err != nil {
		return nil, err
	}
	return st, tx.Commit()
}
