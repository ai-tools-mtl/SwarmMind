// hub_spaces_test.go hub 模式空间工具实测（W07-02，docs/tasks/v3/W07-hub-routing.md
// REST/MCP映射表「同一组 space_* 工具Hub装配」行 + 工作块6「MCP hub 模式
// tools/list+实调」）：进程内真 hub api（真 collab 内核 + 真 spaces 服务 + 策略
// 引擎）+ hubBackend 工具链——
//   - tools/list：空间九件在 hub 清单（词典单源——与 node 面同名同 schema 键形）；
//   - 实调：PAT 编排主体 external 创建（scope 注记）→ list/status 渲染 →
//     主人专属动作 control 显式拒收（豁免⑯收窄保留面 hub 同规）；
//   - 同一参数解析面（spaceToolXxx 共享实现两后端同函数调用的装配实证）。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/spaces"
	hubstore "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/strategy"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newHubSpacesServer 起真 hub api（Spaces+Strategy 全装配）——hub 空间工具链的
// 进程内实测面。
func newHubSpacesServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := hubstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hubCore, err := core.New(core.Config{Store: st, Transport: transport.NewBus().Endpoint("hub")})
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	t.Cleanup(func() { _ = hubCore.Close() })
	stratEngine, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	svc, err := spaces.Open(dir, hubCore, spaces.Options{Hooks: stratEngine})
	if err != nil {
		t.Fatalf("spaces: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	adminToken := "admin-tok-mcp"
	ts := httptest.NewServer(api.New(api.Config{
		Core: hubCore, AdminToken: adminToken, DataDir: dir,
		Spaces: svc, Strategy: stratEngine, Version: "test",
	}).Handler())
	t.Cleanup(ts.Close)

	// 建 PAT（编排主体；admin 会话登录 → tokens API）。
	cookie := loginHub(t, ts.URL, adminToken)
	req, _ := httpNewRequestT("POST", ts.URL+"/api/v1/auth/tokens", `{"name":"mcp"}`, cookie)
	resp, err := httpClientT.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatalf("PAT 创建失败: %d", resp.StatusCode)
	}
	return ts, out.Token
}

// TestHubSpaceToolsEndToEnd hub 空间工具全链（进程内）：
// tools/list 九件在册 → PAT external 创建（scope 注记随渲染）→ list/status →
// control 主人专属动作拒收（hub 同 node 同词）→ control 白名单动作照常透传。
func TestHubSpaceToolsEndToEnd(t *testing.T) {
	ts, pat := newHubSpacesServer(t)
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = ts.URL, pat
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	// tools/list：空间九件在 hub 清单（与 node 面同名——词典单源）。
	want := []string{"swarmmind_space_create", "swarmmind_space_list", "swarmmind_space_status",
		"swarmmind_space_dispatch", "swarmmind_space_control", "swarmmind_space_messages",
		"swarmmind_space_record", "swarmmind_space_records", "swarmmind_space_strategy"}
	got := map[string]bool{}
	for _, td := range (hubBackend{}).tools() {
		got[td.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("hub 清单缺空间工具: %s", name)
		}
	}
	// hub 注记：描述携带中心空间语义（scope=hub:<id> 注记）。
	for _, td := range (hubBackend{}).tools() {
		if td.Name == "swarmmind_space_create" && !strings.Contains(td.Description, "hub:<id>") {
			t.Fatalf("hub 面描述应带中心 scope 注记: %s", td.Description)
		}
	}

	bd := hubBackend{}
	// PAT external 创建（201）。
	text, isErr := callText(t, bd, "swarmmind_space_create", map[string]any{"goal": "hub 面目标", "mode": "external"})
	if isErr || !strings.Contains(text, "空间已创建") {
		t.Fatalf("创建: %s isErr=%v", text, isErr)
	}
	// list（渲染含列表头）。
	text, isErr = callText(t, bd, "swarmmind_space_list", map[string]any{})
	if isErr || !strings.Contains(text, "中心协作空间列表") {
		t.Fatalf("list: %s isErr=%v", text, isErr)
	}
	// status（读取 ID + 版本）。
	spID := extractHubSpaceID(t, ts.URL, pat)
	text, isErr = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, spID) {
		t.Fatalf("status: %s isErr=%v", text, isErr)
	}
	// 主人专属动作拒收（hub 面同 node 同词——豁免⑯收窄保留面）。
	for _, act := range []string{"set_budget", "policy", "finalize", "archive"} {
		text, _ = callText(t, bd, "swarmmind_space_control", map[string]any{
			"space_id": spID, "action": act, "expected_version": 1})
		if !strings.Contains(text, "主人专属") {
			t.Fatalf("动作 %s 应主人专属拒收: %s", act, text)
		}
	}
	// 白名单动作透传（服务端逐动作授权裁决：PAT 非驱动者 → 403 forbidden 原话
	// 透出——工具面不做第二套权限）。
	_, isErr = callText(t, bd, "swarmmind_space_control", map[string]any{
		"space_id": spID, "action": "pause", "expected_version": 1})
	if !isErr {
		t.Fatal("PAT 非驱动者 pause 应透传服务端 403（工具面零第二套权限）")
	}
}

// extractHubSpaceID 经 REST 读首个空间 ID（与 node 面同名助手分形防撞）。
func extractHubSpaceID(t *testing.T, url, pat string) string {
	t.Helper()
	code, body, err := httpCall(url, pat, httpClientT, "GET", "/api/v1/spaces", nil)
	if err != nil || code != 200 {
		t.Fatalf("读空间列表: %d %v", code, err)
	}
	var out struct {
		Spaces []struct {
			ID string `json:"id"`
		} `json:"spaces"`
	}
	_ = json.Unmarshal(body, &out)
	if len(out.Spaces) == 0 {
		t.Fatal("应有一个空间")
	}
	return out.Spaces[0].ID
}

// loginHub / httpNewRequestT / httpClientT 测试辅助（本文件自包含）。
var httpClientT = &http.Client{}

func httpNewRequestT(method, url, body, cookie string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err == nil {
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

func loginHub(t *testing.T, url, adminToken string) string {
	t.Helper()
	resp, err := httpClientT.Post(url+"/api/v1/login", "application/json",
		strings.NewReader(`{"token":"`+adminToken+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "swarmmind_session" {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("无会话 cookie")
	return ""
}
