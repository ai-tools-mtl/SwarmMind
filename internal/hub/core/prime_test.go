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
