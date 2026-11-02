// Package spaces 实现 hub 中心空间服务（W07-02，docs/tasks/v3/W07-hub-routing.md
// 子任务表第二行「Hub共享空间服务装配SQLite adapter、远端会话/派发adapter、可靠
// 请求结果/恢复映射」）。
//
// 装配裁决（同一内核零分叉）：空间状态机/journal/回执/意图全部复用
// internal/collab.Store 内核（node 本机空间同一实现，零第二套状态机），仅持久化
// 面按 hub 惯例装配——SQLite key+JSON body 表（hub_state.db 同款惯例：单文件库 +
// WAL + synchronous=FULL + busy_timeout，单连接单写者），快照整份 JSON 单行存
// 放（collab 快照是单文档状态，行式拆表会把内核 journal 结构泄进 hub store——
// 单行快照保持内核封装边界，等价于 hub store「key+JSON body」行的最小形态）。
// 原子性：单事务 UPSERT（SQLite 事务原子）；损坏（JSON 解析失败/版本未知）拒绝
// 带病启动——空间状态是协作事实唯一真相，宁拒启不瞎门（对齐 node spaces.json
// 先例）。
//
// 远端派发：成员线程是 hub 注册表会话（T2），派发复用既有 session Task 通道
// （core.SubmitSpaceTurn → task_offer），不新增任意远程执行通道；下发前过
// core.RequireSpacesCapable 版本门（W07-01 交付的强制门，本包为首个消费者）+
// space_bind 绑定登记（node 收件门受理面）+ 开放目录成员校验（DirectoryAgent）。
// 结果收敛挂任务终态链（core.AddOnTaskTerminal），恢复对账按任务/轮行实物分类，
// 绝不自动重发、绝不把失联解释为没执行。
package spaces

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（hub store 同款，SPEC 三白名单登记项）
)

// dbFileName 中心空间快照库文件名（相对 data_dir；独立于 hub_state.db——快照
// 生命周期归 collab 内核管，不入 hub store diff 事务面）。
const dbFileName = "hub_spaces.db"

// snapshotKey 快照单行键（整份快照一行的最小 key+JSON body 形态）。
const snapshotKey = "snapshot"

// schemaDDL 快照表（key+JSON body，hub store 同款惯例；IF NOT EXISTS 幂等建表）。
const schemaDDL = `CREATE TABLE IF NOT EXISTS spaces (key TEXT PRIMARY KEY, body TEXT NOT NULL);`

// snapshotDB SQLite 快照存储（collab.SnapshotStore 实现）。单连接单写者
// （WAL + synchronous=FULL + busy_timeout，hub store openDB 同款 DSN）。
type snapshotDB struct {
	db *sql.DB
}

// openSnapshotDB 打开（或初始化）data_dir 下的空间快照库。
func openSnapshotDB(dir string) (*snapshotDB, error) {
	if dir == "" {
		return nil, errors.New("spaces: 数据目录为空")
	}
	path := filepath.Join(dir, dbFileName)
	dsn := "file:" + sqliteURIPath(path) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("spaces: 打开空间快照库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("spaces: 打开空间快照库失败: %w", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("spaces: 建快照表失败: %w", err)
	}
	return &snapshotDB{db: db}, nil
}

// Load 读取快照（不存在 ok=false）。
func (s *snapshotDB) Load() (b []byte, ok bool, err error) {
	var body []byte
	e := s.db.QueryRow(`SELECT body FROM spaces WHERE key = ?`, snapshotKey).Scan(&body)
	switch {
	case errors.Is(e, sql.ErrNoRows):
		return nil, false, nil
	case e != nil:
		return nil, false, fmt.Errorf("spaces: 读快照失败: %w", e)
	}
	return body, true, nil
}

// Save 原子持久化快照（单事务 UPSERT；WAL+synchronous=FULL 下事务提交即落盘）。
func (s *snapshotDB) Save(b []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("spaces: 开启快照事务失败: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO spaces (key, body) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET body = excluded.body`, snapshotKey, string(b)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("spaces: 写快照失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("spaces: 提交快照事务失败: %w", err)
	}
	return nil
}

// Close 关闭库（装配方收尾；幂等安全）。
func (s *snapshotDB) Close() error { return s.db.Close() }

// sqliteURIPath 文件路径 → file: URI 路径组件（hub store sqlite.go 同款：Windows
// 盘符反斜杠转正斜杠 + URI 结构保留字符转义，防路径与锁路径分离）。
func sqliteURIPath(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return r.Replace(filepath.ToSlash(path))
}
