package core

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestPrimeDesignation 网页指定统筹载体（2026-10-23 伦哥定案）：设置/替换/撤销/校验。
func TestPrimeDesignation(t *testing.T) {
	f := newHub(t)
	// 目录里放一个 exposed 的 zcode-dev（mac-mini）与一个未 exposed 的 echo（node-b）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "zcode-dev", Exposed: true},
		{Name: "diskcheck", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "echo", Exposed: false},
	}); err != nil {
		t.Fatal(err)
	}

	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("初始应为未指定，got %+v", got)
	}
	// 未 exposed 能力拒绝指定
	if _, err := f.core.SetPrimeDesignation("node-b", "echo", "admin"); err == nil {
		t.Fatal("未 exposed 能力应被拒绝")
	}
	// 不存在的能力拒绝指定
	if _, err := f.core.SetPrimeDesignation("mac-mini", "ghost", "admin"); err == nil {
		t.Fatal("不存在能力应被拒绝")
	}
	// 正常指定：字段齐全
	d, err := f.core.SetPrimeDesignation("mac-mini", "zcode-dev", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if d.NodeID != "mac-mini" || d.AgentName != "zcode-dev" || d.SetBy != "admin" || d.SetAt == "" {
		t.Fatalf("指定字段不完整: %+v", d)
	}
	// 读回一致（深拷贝：改返回值不影响存储态）
	got := f.core.PrimeDesignation()
	if got == nil || got.AgentName != "zcode-dev" {
		t.Fatalf("读回不一致: %+v", got)
	}
	// 替换指定
	if _, err := f.core.SetPrimeDesignation("mac-mini", "diskcheck", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got.AgentName != "diskcheck" {
		t.Fatalf("替换失败: %+v", got)
	}
	// 撤销（幂等：再撤销不报错）
	if _, err := f.core.SetPrimeDesignation("", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("撤销后应为 nil: %+v", got)
	}
	if _, err := f.core.SetPrimeDesignation("", "", "admin"); err != nil {
		t.Fatalf("重复撤销应幂等: %v", err)
	}
}

// TestPrimeDesignationBoundToNode P1 回归：目录校验必须精确绑定 (node_id, agent_name)。
// 旧实现全网找同名 exposed 能力——跨节点同名能力时，把指定落到无此能力的节点也放行，
// 派发端点按 (node_id, agent_name) 找不到目标即跳过，指定形同虚设。
func TestPrimeDesignationBoundToNode(t *testing.T) {
	f := newHub(t)
	// 跨节点同名：zcode-dev 只在 mac-mini 上 exposed，node-b 没有这个能力
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "zcode-dev", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "echo", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	// node-b 无 zcode-dev（虽然全网存在同名 exposed 能力）：必须拒绝
	if _, err := f.core.SetPrimeDesignation("node-b", "zcode-dev", "admin"); err == nil {
		t.Fatal("指定到无此能力的节点应被拒绝")
	}
	// 节点不存在：必须拒绝
	if _, err := f.core.SetPrimeDesignation("ghost-node", "zcode-dev", "admin"); err == nil {
		t.Fatal("指定到不存在的节点应被拒绝")
	}
	// 精确匹配命中：正常指定
	if _, err := f.core.SetPrimeDesignation("mac-mini", "zcode-dev", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got == nil || got.NodeID != "mac-mini" {
		t.Fatalf("精确匹配应放行: %+v", got)
	}
}

// TestPrimeDesignationSurvivesUnrelatedTx 回归（P0）：任何事务都从 Clone 副本构造
// 新状态——Clone 曾遗漏 Prime 字段，导致指定后任意无关事务（如 RegisterNode）都会
// 把统筹指定丢掉并落盘为未指定。指定后执行一次无关事务，读回 Prime 必须仍在。
func TestPrimeDesignationSurvivesUnrelatedTx(t *testing.T) {
	f := newHub(t)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "zcode-dev", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.core.SetPrimeDesignation("mac-mini", "zcode-dev", "admin"); err != nil {
		t.Fatal(err)
	}
	// 无关事务：注册另一个节点（走 Update → Clone 全链）
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "echo", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	got := f.core.PrimeDesignation()
	if got == nil || got.NodeID != "mac-mini" || got.AgentName != "zcode-dev" {
		t.Fatalf("无关事务后统筹指定丢失: %+v", got)
	}
}
