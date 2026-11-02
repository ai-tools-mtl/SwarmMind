package localapi

// agentsstate_w07_test.go 能力卡空间声明盖章测试（W07-01）：SetSpacesCapability
// 注入后 Cards() 对全部能力卡盖章 AgentCard.Spaces（register/card_update 同源）；
// 未注入（旧词形/未装配 collab）不落 spaces 键。声明版本恒 protocol.SpacesCapV1，
// 值随注入参数（不给生产代码留第二个真值源）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// jsonContainsBytes 子串判定（测试可读性薄封装）。
func jsonContainsBytes(s, sub string) bool { return strings.Contains(s, sub) }

func TestW07AgentStateSpacesStamping(t *testing.T) {
	baseline := []protocol.ExecProfile{
		{Name: "coder", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 30, PermissionMode: "workspace-write", Exposed: true},
		{Name: "reviewer", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 30, PermissionMode: "read-only", Exposed: true},
	}
	dir := t.TempDir()

	// 未注入：不声明（未装配 collab 词形）。
	st, err := OpenAgentState(dir, baseline)
	if err != nil {
		t.Fatal(err)
	}
	cards := st.Cards()
	if len(cards) != 2 {
		t.Fatalf("能力卡数: %d", len(cards))
	}
	for _, c := range cards {
		if c.Spaces != "" {
			t.Fatalf("未装配不得声明空间特性: %+v", c)
		}
	}
	// 线上形态：序列化不落 spaces 键（omitempty，旧 hub 零感知）。
	b, _ := json.Marshal(cards[0])
	if jsonContainsBytes(string(b), `"spaces"`) {
		t.Fatalf("未声明卡不得落 spaces 键: %s", b)
	}

	// 注入（collab 装配同源，cmd/node 装配序）：全部卡盖章。
	st2, err := OpenAgentState(t.TempDir(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	st2.SetSpacesCapability(protocol.SpacesCapV1)
	cards2 := st2.Cards()
	for _, c := range cards2 {
		if c.Spaces != protocol.SpacesCapV1 {
			t.Fatalf("装配后应全部声明空间特性: %+v", c)
		}
	}
	b2, _ := json.Marshal(cards2[0])
	if !jsonContainsBytes(string(b2), `"spaces":"v1"`) {
		t.Fatalf("声明卡应落 spaces 键: %s", b2)
	}
}

// TestW07AgentsEndpointCardConsistency 修复轮必修2反例回归：单一卡片装配点
// （AgentState.Cards）统一三出口——/api/v1/agents 目录卡与注册/card_update 卡
// 同装配同值（spaces:"v1" 一致）；collab 未装配态两出口一致无声明（不存在
// 「目录卡缺字段、注册卡有字段」的声明漂移）。
func TestW07AgentsEndpointCardConsistency(t *testing.T) {
	baseline := []protocol.ExecProfile{
		{Name: "coder", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 30, PermissionMode: "workspace-write", Exposed: true},
	}

	// 装配态：目录端点卡与 AgentState.Cards()（register/card_update 同源）逐字段一致。
	st, err := OpenAgentState(t.TempDir(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSpacesCapability(protocol.SpacesCapV1)
	srv := New(Config{NodeID: "n1", Mode: ModeStandalone, Agents: st, LocalToken: "tok-local"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer tok-local")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/agents 应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Local []struct {
			Card protocol.AgentCard `json:"card"`
		} `json:"local"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("应答解析失败: %v", err)
	}
	if len(out.Local) != 1 {
		t.Fatalf("目录条目数: %d", len(out.Local))
	}
	want := st.Cards()[0]
	got := out.Local[0].Card
	if got.Spaces != protocol.SpacesCapV1 || got != want {
		t.Fatalf("目录卡与注册卡漂移: 目录=%+v 注册=%+v", got, want)
	}

	// 未装配态（无 SetSpacesCapability）：目录端点与 Cards() 两出口一致空声明。
	st2, err := OpenAgentState(t.TempDir(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := New(Config{NodeID: "n1", Mode: ModeStandalone, Agents: st2, LocalToken: "tok-local"})
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req2.Header.Set("Authorization", "Bearer tok-local")
	srv2.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("未装配态应 200, got %d", rec2.Code)
	}
	var out2 struct {
		Local []struct {
			Card protocol.AgentCard `json:"card"`
		} `json:"local"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if len(out2.Local) != 1 || out2.Local[0].Card.Spaces != "" {
		t.Fatalf("未装配态目录卡不得声明 spaces: %+v", out2.Local)
	}
	if out2.Local[0].Card != st2.Cards()[0] {
		t.Fatalf("未装配态两出口不一致: 目录=%+v 注册=%+v", out2.Local[0].Card, st2.Cards()[0])
	}
}
