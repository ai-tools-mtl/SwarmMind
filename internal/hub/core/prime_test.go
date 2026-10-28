package core

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestPrimeFromDirectory 聚合三态（2026-10-28 客户端声明制）：零声明 nil（回退名字
// 约定）、恰一声明生效、多声明冲突（nil + 冲突清单）。声明随 AgentCard.Orchestrator
// 上报，hub 只读汇聚，无写入口。
func TestPrimeFromDirectory(t *testing.T) {
	f := newHub(t)
	// 零声明：nil（前端回退名字约定）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "echo", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("零声明应为 nil, got %+v", got)
	}
	if len(f.core.PrimeConflicts()) != 0 {
		t.Fatal("零声明应无冲突")
	}
	// 未 exposed 的声明不算（目录口径=exposed=true）
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "hidden-orch", Orchestrator: true, Exposed: false},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("未 exposed 的声明不应生效, got %+v", got)
	}
	// 恰一声明：生效
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "echo", Exposed: true},
		{Name: "zcode-dev", Orchestrator: true, Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	got := f.core.PrimeDesignation()
	if got == nil || got.NodeID != "mac-mini" || got.AgentName != "zcode-dev" {
		t.Fatalf("恰一声明应生效: %+v", got)
	}
	if len(f.core.PrimeConflicts()) != 0 {
		t.Fatalf("单声明应无冲突: %+v", f.core.PrimeConflicts())
	}
	// 第二台机器也声明 → 冲突态：无生效管家 + 冲突清单（含双方，稳定排序）
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "orch-b", Orchestrator: true, Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("多声明应为冲突态（nil）, got %+v", got)
	}
	conf := f.core.PrimeConflicts()
	if len(conf) != 2 {
		t.Fatalf("冲突清单应含双方, got %+v", conf)
	}
	if conf[0].NodeID != "mac-mini" || conf[0].AgentName != "zcode-dev" ||
		conf[1].NodeID != "node-b" || conf[1].AgentName != "orch-b" {
		t.Fatalf("冲突清单应稳定排序: %+v", conf)
	}
	// 撤销一侧声明 → 回到恰一生效
	if err := f.core.RegisterNode("node-b", []protocol.AgentCard{
		{Name: "orch-b", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got == nil || got.AgentName != "zcode-dev" {
		t.Fatalf("撤销一侧后应恢复单声明生效: %+v", got)
	}
}

// TestPrimeDeclFollowsUnexpose exposed 撤下（声明能力下线）后声明自然失效：
// 目录口径（exposed=true）与聚合一致，撤下的能力不再充当管家（回退名字约定）。
func TestPrimeDeclFollowsUnexpose(t *testing.T) {
	f := newHub(t)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "zcode-dev", Orchestrator: true, Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got == nil {
		t.Fatal("声明应生效")
	}
	// 能力下线（exposed=false）：声明随目录口径失效
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "zcode-dev", Orchestrator: true, Exposed: false},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.core.PrimeDesignation(); got != nil {
		t.Fatalf("下线能力的声明应失效, got %+v", got)
	}
}
