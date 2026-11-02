package ledger

// w07_mixedrun_test.go 旧版混跑落盘 fixture（W07-01）：真实 ledger.json 文件夹具
// 双向容忍实证——
//   - 旧 node 落盘词形（SessionTurnRef.Space 字段引入前的账本条目）经真实 Open
//     装载路径直读：条目/会话轮/高水位完好，Space 解码零值（旧文件零迁移）；
//   - 新 node 落盘词形（会话轮携 space 来源标记）经同一装载路径直读：space 字段
//     原样保真（新 node 降级回旧版二进制时该字段被丢弃、解码零值、装载不报错——
//     同一 stdlib 容忍语义，由 protocol 层 fixture 反向锁定）。
// 高水位与终态结果随夹具核对，确保不是「只解了 JSON 没进真路径」。

import (
	"os"
	"path/filepath"
	"testing"
)

// openWithFixture 把夹具落为 data_dir/ledger.json 后走真实 Open 装载路径。
func openWithFixture(t *testing.T, fixture string) *Ledger {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("读取夹具 %s: %v", fixture, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.json"), b, 0o600); err != nil {
		t.Fatalf("落夹具: %v", err)
	}
	l, _, err := Open(dir)
	if err != nil {
		t.Fatalf("装载夹具账本 %s: %v", fixture, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// TestW07LegacyLedgerLoads 旧 node 落盘词形直读：无 space 字段的账本条目经真实
// 装载路径零迁移可读，会话轮身份与高水位完好，Space 解码零值。
func TestW07LegacyLedgerLoads(t *testing.T) {
	l := openWithFixture(t, "w07_legacy_ledger.json")
	e, ok := l.Get("t-w07legacy0001")
	if !ok {
		t.Fatal("旧条目应装载在场")
	}
	if e.State != StateFinished || e.Result == nil || e.Result.Status != "completed" {
		t.Fatalf("旧条目终态/结果漂移: state=%v result=%v", e.State, e.Result)
	}
	s := e.Task.Session
	if s == nil || s.SessionID != "s-w07legacy000001" || s.TurnSeq != 1 || s.Op != "create" {
		t.Fatalf("旧条目会话轮身份漂移: %+v", s)
	}
	if s.Space != "" {
		t.Fatalf("旧条目不得解码出空间标记: %q", s.Space)
	}
	if hw := l.SessionTurnHighWater("s-w07legacy000001"); hw != 1 {
		t.Fatalf("高水位应随夹具恢复: %d", hw)
	}
}

// TestW07SpaceRefLedgerLoads 新 node 落盘词形直读：会话轮携 space 来源标记的
// 账本条目经同一装载路径装载，space 字段原样保真（非丢弃非改写）——node 账本
// 对协议加法字段的落盘容忍/保真双向实证。
func TestW07SpaceRefLedgerLoads(t *testing.T) {
	l := openWithFixture(t, "w07_space_ref_ledger.json")
	e, ok := l.Get("t-w07spaceref01")
	if !ok {
		t.Fatal("新词形条目应装载在场")
	}
	s := e.Task.Session
	if s == nil || s.SessionID != "s-w07spaceref00001" || s.TurnSeq != 2 {
		t.Fatalf("新词形条目会话轮身份漂移: %+v", s)
	}
	if s.Space != "sp-w07fixture0001" {
		t.Fatalf("space 来源标记应原样保真: %q", s.Space)
	}
	if e.Origin != OriginLocal || e.State != StateAccepted {
		t.Fatalf("来源/状态应随夹具恢复: origin=%v state=%v", e.Origin, e.State)
	}
}
