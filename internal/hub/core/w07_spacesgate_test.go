package core

// w07_spacesgate_test.go 空间特性版本门测试（W07-01，docs/tasks/v3/W07-hub-routing.md
// 「增加空间特性的版本门，不支持时明确拒绝，普通 Task 不受牵连」「不把旧node当
// 有新特性」）：门槛数据源（SpacesCapability）与拒绝面（RequireSpacesCapable）
// 按节点能力表行为锁定——未声明（旧版 node 词形）/未知 node/未知 agent 一律拒
// 绝并附人话指路升级；声明 node 放行。目录成员校验点（DirectoryAgent）与节点级
// 投影（SpacesNodeSupport）同批。普通任务路径不经过本门（结构零调用，无测试
// 必要——本文件即拒绝面唯一权威锚）。

import (
	"errors"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

func TestW07SpacesGate(t *testing.T) {
	f := newHub(t)
	oldCard := protocol.AgentCard{Name: "old-coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true}
	newCard := protocol.AgentCard{Name: "new-coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex, Spaces: protocol.SpacesCapV1}
	f.newNode("n-old", []protocol.AgentCard{oldCard}, true)
	f.newNode("n-new", []protocol.AgentCard{newCard}, true)

	// 未声明（旧 node 词形）：查询如实 false，拒绝面人话指路升级。
	if f.core.SpacesCapable("n-old", "old-coder") {
		t.Fatal("未声明 node 不得视为支持空间特性（不把旧 node 当有新特性）")
	}
	err := f.core.RequireSpacesCapable("n-old", "old-coder")
	if !errors.Is(err, ErrSpacesUnsupported) {
		t.Fatalf("未声明应 ErrSpacesUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "n-old") || !strings.Contains(err.Error(), "old-coder") || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("拒绝错误应带 node/agent 定位与升级指路: %v", err)
	}
	if v, ok := f.core.SpacesCapability("n-old", "old-coder"); ok || v != "" {
		t.Fatalf("未声明查询应 (\"\",false), got (%q,%v)", v, ok)
	}

	// 未知 node / 未知 agent：同拒绝面（不猜）。
	if f.core.SpacesCapable("n-none", "old-coder") || f.core.SpacesCapable("n-old", "no-agent") {
		t.Fatal("未知 node/agent 不得视为支持空间特性")
	}
	if err := f.core.RequireSpacesCapable("n-none", "old-coder"); !errors.Is(err, ErrSpacesUnsupported) {
		t.Fatalf("未知 node 应拒绝: %v", err)
	}

	// 声明 node：放行；声明版本可读（W07-02 按版本判可用面）。
	if !f.core.SpacesCapable("n-new", "new-coder") {
		t.Fatal("声明 node 应支持空间特性")
	}
	if v, ok := f.core.SpacesCapability("n-new", "new-coder"); !ok || v != protocol.SpacesCapV1 {
		t.Fatalf("声明版本应如实可读: (%q,%v)", v, ok)
	}
	if err := f.core.RequireSpacesCapable("n-new", "new-coder"); err != nil {
		t.Fatalf("声明 node 应放行: %v", err)
	}
}

func TestW07SpacesNodeSupportProjection(t *testing.T) {
	f := newHub(t)
	oldCard := protocol.AgentCard{Name: "old-coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "rw", ProtocolVer: 1, Exposed: true}
	newCard := oldCard
	newCard.Name = "new-coder"
	newCard.Spaces = protocol.SpacesCapV1
	f.newNode("n-old", []protocol.AgentCard{oldCard}, true)
	f.newNode("n-new", []protocol.AgentCard{oldCard, newCard}, true) // 混合表：任一卡声明即 node 级支持

	support := f.core.SpacesNodeSupport()
	if len(support) != 2 {
		t.Fatalf("投影应覆盖全部注册 node: %v", support)
	}
	if support["n-old"] {
		t.Fatal("未声明 node 投影应 false")
	}
	if !support["n-new"] {
		t.Fatal("任一卡声明即 node 级 true")
	}
}

func TestW07DirectoryAgentMemberCheck(t *testing.T) {
	f := newHub(t)
	exposed := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "rw", ProtocolVer: 1, Exposed: true, Spaces: protocol.SpacesCapV1}
	hidden := exposed
	hidden.Name = "shadow"
	hidden.Exposed = false // 审计登记：不在开放目录
	f.newNode("n1", []protocol.AgentCard{exposed, hidden}, true)

	card, ok := f.core.DirectoryAgent("n1", "coder")
	if !ok || card.Name != "coder" || card.Spaces != protocol.SpacesCapV1 {
		t.Fatalf("开放目录成员应命中: (%+v,%v)", card, ok)
	}
	if _, ok := f.core.DirectoryAgent("n1", "shadow"); ok {
		t.Fatal("exposed=false 审计登记不得出现在开放目录（成员必须来自当前开放目录）")
	}
	if _, ok := f.core.DirectoryAgent("n2", "coder"); ok {
		t.Fatal("跨 node 名不得命中")
	}
}

// TestW07SpacesGateUnsupportedVersions 修复轮必修1反例回归：声明版本不受支持
// （v0 旧版 / garbage 词形 / 空白）三拒绝面——查询（SpacesCapability/SpacesCapable）
// 如实 false、拒绝面 ErrSpacesUnsupported 且话术三分（旧版本指路升级 / 词形非法
// 异常注记 / 未声明升级指路）、节点投影 false（不把声明过但不受支持的 node 当
// 有新特性）。
func TestW07SpacesGateUnsupportedVersions(t *testing.T) {
	f := newHub(t)
	base := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "rw", ProtocolVer: 1, Exposed: true}
	cases := []struct {
		node    string
		version string
		msgSub  string // 各话术锚（词形非法 ≠ 旧版本 ≠ 未声明）
	}{
		{"n-v0", "v0", "旧于本 hub 支持"},
		{"n-garbage", "garbage", "词形非法"},
		{"n-blank", "  ", "词形非法"},
	}
	for _, c := range cases {
		card := base
		card.Spaces = c.version
		f.newNode(c.node, []protocol.AgentCard{card}, true)
	}

	for _, c := range cases {
		// 查询面：声明了但版本不受支持 → 如实 false（只判非空的旧实现全放行）。
		if f.core.SpacesCapable(c.node, "coder") {
			t.Fatalf("%s 声明 %q 不得视为受支持", c.node, c.version)
		}
		if v, ok := f.core.SpacesCapability(c.node, "coder"); ok || v != "" {
			t.Fatalf("%s SpacesCapability 应 (\"\",false), got (%q,%v)", c.node, v, ok)
		}
		// 拒绝面：话术按声明态分流。
		err := f.core.RequireSpacesCapable(c.node, "coder")
		if !errors.Is(err, ErrSpacesUnsupported) {
			t.Fatalf("%s 应 ErrSpacesUnsupported, got %v", c.node, err)
		}
		if !strings.Contains(err.Error(), c.msgSub) {
			t.Fatalf("%s（声明 %q）拒绝话术应含 %q: %v", c.node, c.version, c.msgSub, err)
		}
	}
	// 话术互斥锚：v0 话术不含「词形非法」，garbage 话术不含「旧于本 hub 支持」。
	if err := f.core.RequireSpacesCapable("n-v0", "coder"); strings.Contains(err.Error(), "词形非法") {
		t.Fatalf("v0 属旧版本不得冒充词形非法: %v", err)
	}
	if err := f.core.RequireSpacesCapable("n-garbage", "coder"); strings.Contains(err.Error(), "旧于本 hub 支持") {
		t.Fatalf("garbage 属词形非法不得冒充旧版本: %v", err)
	}

	// 投影面：三 node 均 false（含声明过但不受支持的）。
	support := f.core.SpacesNodeSupport()
	for _, c := range cases {
		if support[c.node] {
			t.Fatalf("%s（声明 %q）投影应 false", c.node, c.version)
		}
	}
	// 受支持 node 同表对照：true 在场（排除「整表为空」的假绿）。
	f.newNode("n-ok", []protocol.AgentCard{func() protocol.AgentCard {
		c := base
		c.Spaces = protocol.SpacesCapV1
		return c
	}()}, true)
	if !f.core.SpacesNodeSupport()["n-ok"] {
		t.Fatal("受支持 node 投影应 true")
	}
}
