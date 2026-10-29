package localapi

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
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

// orchEffective 有效声明位快照（断言助手）。
func orchEffective(t *testing.T, s *AgentState) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	for _, p := range s.Effective() {
		m[p.Name] = p.Orchestrator
	}
	return m
}

// TestReplaceOrchestratorHolderScenarios 原子替换声明持有人（2026-10-29 走查反馈批
// P1-01）四类场景：YAML 单基线声明、YAML 历史双基线声明、覆盖层声明、落盘失败
// 整集保留。成功态恒为「恰一在新持有人」；失败态旧有效声明原样保留（先写后见）。
func TestReplaceOrchestratorHolderScenarios(t *testing.T) {
	profiles := func(orchNames ...string) []protocol.ExecProfile {
		on := map[string]bool{}
		for _, n := range orchNames {
			on[n] = true
		}
		return []protocol.ExecProfile{
			{Name: "a", Orchestrator: on["a"]},
			{Name: "b", Orchestrator: on["b"]},
			{Name: "c"},
		}
	}

	t.Run("YAML单基线声明翻转", func(t *testing.T) {
		dir := t.TempDir()
		s := newState(t, dir, profiles("a")) // 基线 a=true
		if _, err := s.ReplaceOrchestratorHolder("b"); err != nil {
			t.Fatalf("翻转应成功: %v", err)
		}
		m := orchEffective(t, s)
		if !m["b"] || m["a"] || m["c"] {
			t.Fatalf("翻转后恰一在 b, got %v", m)
		}
		// 重启保持：同 data_dir 重开，声明位不变（覆盖集 a=false 落盘压住基线）
		s2, err := OpenAgentState(dir, profiles("a"))
		if err != nil {
			t.Fatal(err)
		}
		m2 := orchEffective(t, s2)
		if !m2["b"] || m2["a"] {
			t.Fatalf("重开后恰一在 b（a 覆盖压制基线）, got %v", m2)
		}
	})

	t.Run("YAML历史双基线声明收敛", func(t *testing.T) {
		dir := t.TempDir()
		s := newState(t, dir, profiles("a", "b")) // 基线 a、b 双声明（历史漂移形态）
		if _, err := s.ReplaceOrchestratorHolder("c"); err != nil {
			t.Fatalf("翻转应成功: %v", err)
		}
		m := orchEffective(t, s)
		if !m["c"] || m["a"] || m["b"] {
			t.Fatalf("翻转后恰一在 c（双基线同时压制）, got %v", m)
		}
		if s2, err := OpenAgentState(dir, profiles("a", "b")); err != nil {
			t.Fatal(err)
		} else if m2 := orchEffective(t, s2); !m2["c"] || m2["a"] || m2["b"] {
			t.Fatalf("重开后恰一在 c, got %v", m2)
		}
	})

	t.Run("覆盖层声明改写", func(t *testing.T) {
		dir := t.TempDir()
		s := newState(t, dir, profiles()) // 基线全 false；声明经覆盖层产生
		if _, err := s.SetAgentOrchestrator("a", true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReplaceOrchestratorHolder("b"); err != nil {
			t.Fatalf("翻转应成功: %v", err)
		}
		m := orchEffective(t, s)
		if !m["b"] || m["a"] {
			t.Fatalf("翻转后恰一在 b, got %v", m)
		}
		if rec, ok := s.orchOverride["a"]; !ok || rec.Orchestrator {
			t.Fatalf("旧持有人覆盖项应显式改写为 false, got %+v (ok=%v)", rec, ok)
		}
	})

	t.Run("落盘失败整集保留", func(t *testing.T) {
		dir := t.TempDir()
		s := newState(t, dir, profiles("a"))
		// rename 失败构造：状态文件路径替换为目录（rename 文件→目录 EISDIR）
		if err := os.RemoveAll(s.path); err != nil { // 此时尚未落盘，RemoveAll 兼容不存在
			t.Fatal(err)
		}
		if err := os.Mkdir(s.path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(s.path) })
		before := orchEffective(t, s)
		if _, err := s.ReplaceOrchestratorHolder("b"); err == nil {
			t.Fatal("落盘失败应返回错误")
		}
		if after := orchEffective(t, s); !sameOrchMap(before, after) || !after["a"] {
			t.Fatalf("落盘失败旧有效声明必须原样保留, before=%v after=%v", before, after)
		}
	})

	t.Run("未知agent拒绝", func(t *testing.T) {
		dir := t.TempDir()
		s := newState(t, dir, profiles())
		if _, err := s.ReplaceOrchestratorHolder("ghost"); !errors.Is(err, ErrAgentNotFound) {
			t.Fatalf("未知 agent 应 ErrAgentNotFound, got %v", err)
		}
	})
}

func sameOrchMap(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestSetAgentOrchestratorGrantUsesEffectiveTruth 授新互斥按有效真相（P1-01 互斥
// 修正回归）：基线 a=true 经覆盖层撤除后（PATCH orchestrator=false 口径），授 b
// 必须成功——旧实现按原始基线遍历，会以「yaml 基线」为由拒绝合法翻转；这也是
// 完成页「重试声明」补写路径的收敛前提。
func TestSetAgentOrchestratorGrantUsesEffectiveTruth(t *testing.T) {
	dir := t.TempDir()
	s := newState(t, dir, []protocol.ExecProfile{{Name: "a", Orchestrator: true}, {Name: "b"}})
	if _, err := s.SetAgentOrchestrator("a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgentOrchestrator("b", true); err != nil {
		t.Fatalf("覆盖层压制基线声明后授新应成功: %v", err)
	}
	if m := orchEffective(t, s); !m["b"] || m["a"] {
		t.Fatalf("恰一应在 b, got %v", m)
	}
	// 撤 b 后恰零（基线 a 不复活——覆盖 false 仍在）
	if _, err := s.SetAgentOrchestrator("b", false); err != nil {
		t.Fatal(err)
	}
	if m := orchEffective(t, s); m["a"] || m["b"] {
		t.Fatalf("全撤后应恰零且基线声明不复活, got %v", m)
	}
}

// TestReplaceOrchestratorHolderConcurrentWithGrants 并发 PATCH 场景（2026-10-29
// 走查反馈批 P1-01 自验项）：原子替换与并发单点授予（PATCH orchestrator=true 的
// SetAgentOrchestrator 路径）同锁串行——终态必须恰一声明，任何交错不得出现双
// 声明或损坏（mu 串行化 + ReplaceOrchestratorHolder 整集替换是唯一的恰一保证）。
func TestReplaceOrchestratorHolderConcurrentWithGrants(t *testing.T) {
	dir := t.TempDir()
	profiles := []protocol.ExecProfile{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"},
	}
	s := newState(t, dir, profiles)
	var wg sync.WaitGroup
	names := []string{"a", "b", "c", "d"}
	for _, n := range names {
		wg.Add(2)
		go func(n string) { defer wg.Done(); _, _ = s.ReplaceOrchestratorHolder(n) }(n)
		go func(n string) { defer wg.Done(); _, _ = s.SetAgentOrchestrator(n, true) }(n)
	}
	wg.Wait()
	held := []string{}
	for n, orch := range orchEffective(t, s) {
		if orch {
			held = append(held, n)
		}
	}
	if len(held) != 1 {
		t.Fatalf("并发授予+原子替换终态必须恰一声明, got %v", held)
	}
}
