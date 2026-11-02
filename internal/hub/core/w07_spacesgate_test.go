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
