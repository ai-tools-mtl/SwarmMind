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
		Spaces   struct {
			Hub                    bool            `json:"hub"`
			Nodes                  map[string]bool `json:"nodes"`
			PrivateHistoryReadable bool            `json:"private_history_readable"`
		} `json:"spaces"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("应答解析失败: %v: %s", err, body)
	}
	// features.spaces 恒 false：中心空间服务未装配（W07-02 接线前如实 false——禁空壳承诺）
	if out.Features["spaces"] {
		t.Fatal("features.spaces 应恒 false（中心服务未装配如实）")
	}
	// 加法键三字段
	if out.Spaces.Hub {
		t.Fatal("spaces.hub 应 false（W07-02 接线前如实）")
	}
	if !out.Spaces.Nodes["n-new"] || out.Spaces.Nodes["n-old"] {
		t.Fatalf("节点声明投影不符: %v", out.Spaces.Nodes)
	}
	if out.Spaces.PrivateHistoryReadable {
		t.Fatal("private_history_readable 应恒 false（支持情况不表示私有历史可读）")
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
