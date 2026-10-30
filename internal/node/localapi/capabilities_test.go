package localapi

// capabilities 端点测试（W02-02，docs/tasks/v3/W02-mcp-access.md 权限矩阵
// 「能力元信息」行）：authRead 矩阵（未认证 401/local|prime 双收 200）、字段契约
// （server/version/scope/features）、mcp_access_enabled 与夹具授权态联动、
// sessions 与会话内核装配联动（Service 无 sessionop 内核 → false）、无私密断言
// （响应不含本机令牌/凭据字样）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/transport"
)

// newCapSrv 起 capabilities 测试 Server（独立构造：授权态/版本可注入；Service
// 沿用 server_test 的 newService——无会话内核，验证 sessions 如实 false）。
func newCapSrv(t *testing.T, enabled bool) *httptest.Server {
	t.Helper()
	bus := transport.NewBus()
	srv := httptest.NewServer(New(Config{
		NodeID:     "nodeA",
		Service:    newService(t, bus.Endpoint("nodeA")),
		Agents:     newState(t, t.TempDir(), nil),
		Version:    "cap-node-test",
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  NewMcpAccessMemory(enabled),
	}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func capGet(t *testing.T, srv *httptest.Server, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf
}

func TestCapabilitiesEndpointAuthReadMatrix(t *testing.T) {
	srv := newCapSrv(t, true)

	// 未认证 / 错误令牌 → 401（元信息不匿名披露）
	for _, tok := range []string{"", "tok-wrong"} {
		if code, _ := capGet(t, srv, tok); code != http.StatusUnauthorized {
			t.Fatalf("token=%q 应 401, got %d", tok, code)
		}
	}
	// local_token / prime_token 双收（authRead：元信息无敏感，持任一本机凭证可读）
	for _, tok := range []string{"tok-local", "tok-prime"} {
		if code, _ := capGet(t, srv, tok); code != http.StatusOK {
			t.Fatalf("token=%q 应 200（authRead 双收）, got %d", tok, code)
		}
	}
}

func TestCapabilitiesEndpointFieldsAndEnabled(t *testing.T) {
	// enabled=true：mcp_access_enabled 如实 true
	srv := newCapSrv(t, true)
	code, body := capGet(t, srv, "tok-prime")
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d", code)
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
	if out.Server != "node" || out.Scope != "本机" || out.Version != "cap-node-test" {
		t.Fatalf("身份/范围/版本不符: %+v", out)
	}
	if !out.Features["mcp_access_enabled"] {
		t.Fatalf("enabled 夹具应 mcp_access_enabled=true: %v", out.Features)
	}
	if out.Features["sessions"] {
		t.Fatalf("无会话内核夹具应 sessions=false（与端点 503 降级同一判定源）: %v", out.Features)
	}
	if !out.Features["tasks"] {
		t.Fatalf("Service 已装配应 tasks=true: %v", out.Features)
	}
	for _, k := range []string{"works", "groups", "dispatch_suggest", "spaces"} {
		if out.Features[k] {
			t.Fatalf("features.%s 应为 false（hub 专属/未实现，如实不虚构）: %v", k, out.Features)
		}
	}

	// 无私密断言：响应不含本机令牌/凭据字样
	for _, secret := range []string{"tok-local", "tok-prime", "prime_token", "local_token", "credential", "secret"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("capabilities 响应含敏感字样 %q: %s", secret, body)
		}
	}

	// enabled=false：授权态联动（编排门同源 McpAccess.Enabled()）
	srvOff := newCapSrv(t, false)
	code, body = capGet(t, srvOff, "tok-prime")
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d", code)
	}
	var outOff struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(body, &outOff); err != nil {
		t.Fatalf("应答解析失败: %v: %s", err, body)
	}
	if outOff.Features["mcp_access_enabled"] {
		t.Fatalf("disabled 夹具应 mcp_access_enabled=false: %v", outOff.Features)
	}
}
