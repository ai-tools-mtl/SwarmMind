package api

// capabilities 端点测试（W02-02，docs/tasks/v3/W02-mcp-access.md 权限矩阵
// 「能力元信息」行）：鉴权（未认证 401 / 会话 200）、字段契约（server/version/
// scope/features）、装配标记与实际接线一致（Groups/Decision/Notify nil→false、
// 接线→true；spaces 恒 false）、无私密断言（响应不含任何令牌/凭据字样）。

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

func TestCapabilitiesEndpoint(t *testing.T) {
	f := newFixture(t)

	// 未登录 401（元信息与其他管理 API 同门，不对未认证方披露版本）
	hc := f.client(false)
	if code, _ := f.do(hc, http.MethodGet, "/api/v1/capabilities", ""); code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d", code)
	}

	// 裸装配（仅 Core）：核心三能力 true，未接线的依赖 false 如实
	hc = f.client(true)
	code, body := f.do(hc, http.MethodGet, "/api/v1/capabilities", "")
	if code != http.StatusOK {
		t.Fatalf("登录后应 200, got %d", code)
	}
	var out struct {
		Server   string          `json:"server"`
		Version  string          `json:"version"`
		Scope    string          `json:"scope"`
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("应答解析失败: %v: %s", err, body)
	}
	if out.Server != "hub" || out.Scope != "hub 全网" {
		t.Fatalf("身份/范围不符: %+v", out)
	}
	// 该 fixture 构造未注入 Version → 空串如实透传（工具渲染层负责「未知」人话）
	if out.Version != "" {
		t.Fatalf("未注入 Version 应为空串如实, got %q", out.Version)
	}
	for _, k := range []string{"tasks", "sessions", "works"} {
		if !out.Features[k] {
			t.Fatalf("features.%s 应为 true（Core 已接线）: %v", k, out.Features)
		}
	}
	for _, k := range []string{"groups", "dispatch_suggest", "notify", "spaces"} {
		if out.Features[k] {
			t.Fatalf("features.%s 应为 false（未接线/未实现如实）: %v", k, out.Features)
		}
	}

	// 无私密断言：响应不含令牌/凭据字样（admin 令牌值、PAT 前缀、凭据键名）
	for _, secret := range []string{"test-admin-token", "smsw_", "token", "credential", "secret", "prime"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("capabilities 响应含敏感字样 %q: %s", secret, body)
		}
	}
}

// TestCapabilitiesAssemblyReflection 装配标记反映实际接线（版本注入 + Groups
// 接线 → true；Decision/Notify 未接线 → false 如实——反映「装配态」而非硬编码）。
func TestCapabilitiesAssemblyReflection(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := group.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	srv := httptest.NewServer(New(Config{
		Core: c, AdminToken: "test-admin-token", Groups: gs,
		Version: "cap-test-version",
	}).Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	resp, err := hc.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"test-admin-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应 200, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200, got %d", resp.StatusCode)
	}
	var out struct {
		Version  string          `json:"version"`
		Features map[string]bool `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Version != "cap-test-version" {
		t.Fatalf("version 应为注入值, got %q", out.Version)
	}
	for _, k := range []string{"tasks", "sessions", "works", "groups"} {
		if !out.Features[k] {
			t.Fatalf("features.%s 应为 true（已接线）: %v", k, out.Features)
		}
	}
	for _, k := range []string{"dispatch_suggest", "notify", "spaces"} {
		if out.Features[k] {
			t.Fatalf("features.%s 应为 false（未接线/未实现如实）: %v", k, out.Features)
		}
	}
}
