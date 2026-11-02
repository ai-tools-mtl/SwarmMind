package api

// w07_capabilities_spaces_test.go hub capabilities 空间投影测试（W07-01，
// docs/tasks/v3/W07-hub-routing.md REST/MCP 映射「版本范围」行：返回当前中心与
// 目标 Node 支持情况，不表示私有历史可读）：features.spaces 恒 false（中心空间
// 服务 W07-02 接线前如实不虚构）；加法键 spaces 投影 hub/nodes/
// private_history_readable 三字段，节点表按能力卡声明如实（既有应答字段零变化，
// 既有断言见 capabilities_test.go）。

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

func TestW07CapabilitiesSpacesProjection(t *testing.T) {
	f := newFixture(t)
	oldCard := protocol.AgentCard{Name: "old-coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true}
	newCard := oldCard
	newCard.Name = "new-coder"
	newCard.Spaces = protocol.SpacesCapV1
	if err := f.core.RegisterNode("n-old", []protocol.AgentCard{oldCard}); err != nil {
		t.Fatal(err)
	}
	if err := f.core.RegisterNode("n-new", []protocol.AgentCard{oldCard, newCard}); err != nil {
		t.Fatal(err)
	}

	hc := f.client(true)
	code, body := f.do(hc, http.MethodGet, "/api/v1/capabilities", "")
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d", code)
	}
	var out struct {
		Features map[string]bool `json:"features"`
		Spaces   map[string]any  `json:"spaces"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("应答解析失败: %v: %s", err, body)
	}
	// features.spaces 恒 false：中心空间服务未装配（W07-02 接线前如实 false——禁空壳承诺）
	if out.Features["spaces"] {
		t.Fatal("features.spaces 应恒 false（中心服务未装配如实）")
	}
	// 加法键三字段：键存在性 + 类型 + 值三重断言（防「删键后零值假绿」——codex
	// 复审建议1：仅断言值 false 时响应删键仍绿）。
	if _, ok := out.Spaces["hub"]; !ok {
		t.Fatalf("spaces.hub 键必须存在: %v", out.Spaces)
	}
	if _, ok := out.Spaces["nodes"]; !ok {
		t.Fatalf("spaces.nodes 键必须存在: %v", out.Spaces)
	}
	if _, ok := out.Spaces["private_history_readable"]; !ok {
		t.Fatalf("spaces.private_history_readable 键必须存在: %v", out.Spaces)
	}
	hub, ok := out.Spaces["hub"].(bool)
	if !ok || hub {
		t.Fatalf("spaces.hub 应为布尔 false（W07-02 接线前如实）: %v", out.Spaces["hub"])
	}
	phr, ok := out.Spaces["private_history_readable"].(bool)
	if !ok || phr {
		t.Fatalf("spaces.private_history_readable 应为布尔 false（支持情况不表示私有历史可读）: %v", out.Spaces["private_history_readable"])
	}
	nodesRaw, ok := out.Spaces["nodes"].(map[string]any)
	if !ok {
		t.Fatalf("spaces.nodes 应为对象: %v", out.Spaces["nodes"])
	}
	if v, ok := nodesRaw["n-new"].(bool); !ok || !v {
		t.Fatalf("n-new 投影应布尔 true: %v", nodesRaw)
	}
	if v, ok := nodesRaw["n-old"].(bool); !ok || v {
		t.Fatalf("n-old 投影应布尔 false: %v", nodesRaw)
	}
	// 无注册 node：节点表空 map 如实（非 null/缺省——调用方按同形解析）
	if err := f.core.RegisterNode("n-empty", nil); err != nil {
		t.Fatal(err)
	}
	_, body = f.do(hc, http.MethodGet, "/api/v1/capabilities", "")
	var out2 struct {
		Spaces struct {
			Nodes map[string]bool `json:"nodes"`
		} `json:"spaces"`
	}
	if err := json.Unmarshal(body, &out2); err != nil {
		t.Fatal(err)
	}
	if v, ok := out2.Spaces.Nodes["n-empty"]; !ok || v {
		t.Fatalf("无能力卡 node 投影应 false 且在场: %v (ok=%v)", out2.Spaces.Nodes["n-empty"], ok)
	}
}
