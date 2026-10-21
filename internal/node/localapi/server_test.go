package localapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	apipkg "github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 本地 API 单测：令牌鉴权、hub 不可达映射 503、能力目录（本机 + 代理失败如实呈现）
// + 全网目录代理端到端。提交/查询的数据面（SubmitLocal/QueryLocal 关联链）在
// service 包覆盖。

// newService 开账本并构造 node service（挂在给定端点上）。
func newService(t *testing.T, ep transport.Transport) *svcpkg.Service {
	t.Helper()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	s := svcpkg.New(svcpkg.Config{Ledger: led, Runners: map[string]svcpkg.Runner{}}, ep)
	t.Cleanup(s.Close)
	return s
}

// newState 以指定基线构造 AgentState（挂在给定临时目录上；nil = 无 agent）。
func newState(t *testing.T, dir string, profiles []protocol.ExecProfile) *AgentState {
	t.Helper()
	st, err := OpenAgentState(dir, profiles)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// echoProfileLocal 本机 echo ExecProfile（本机视角测试用，exposed=true）。
func echoProfileLocal(t *testing.T) protocol.ExecProfile {
	return protocol.ExecProfile{
		Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
		Workdir: t.TempDir(), TimeoutSec: 10, AllowArgs: true, Exposed: true,
		Description: "回声测试", PermissionMode: "no-shell",
	}
}

func TestLocalAuthGate(t *testing.T) {
	bus := transport.NewBus()
	ep := bus.Endpoint("nodeA") // bus 上无 "hub" 端点 → SubmitLocal 报 hub 不可达
	svc := newService(t, ep)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), []protocol.ExecProfile{echoProfileLocal(t)}),
		Online:     func() bool { return false },
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	do := func(token, method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
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
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 无令牌 / 错令牌 → 401（ Loop 7 新端点同门）
	for _, path := range []string{"/api/v1/agents", "/api/v1/local/agents", "/api/v1/local/status", "/api/v1/local/tasks"} {
		for _, tok := range []string{"", "wrong"} {
			code, out := do(tok, http.MethodGet, path, "")
			if code != http.StatusUnauthorized {
				t.Fatalf("令牌 %q GET %s 应 401, got %d: %s", tok, path, code, out)
			}
		}
	}
	if code, out := do("", http.MethodPatch, "/api/v1/local/agents/echo/exposed", `{"exposed":false}`); code != http.StatusUnauthorized {
		t.Fatalf("无令牌 PATCH exposed 应 401, got %d: %s", code, out)
	}

	// hub 不可达 → 503 hub_unreachable
	code, out := do("tok-local", http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","submitted_by":"p"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(out, protocol.CodeHubUnreachable) {
		t.Fatalf("hub 不可达应 503 hub_unreachable, got %d: %s", code, out)
	}

	// 坏请求体 → 400
	if code, _ = do("tok-local", http.MethodPost, "/api/v1/tasks", `not-json`); code != http.StatusBadRequest {
		t.Fatalf("坏请求体应 400, got %d", code)
	}

	// 本机能力目录：card 不含 ExecProfile 字段；未配代理时 network 如实呈现 error
	code, out = do("tok-local", http.MethodGet, "/api/v1/agents", "")
	if code != http.StatusOK {
		t.Fatalf("agents 应 200, got %d: %s", code, out)
	}
	for _, leak := range []string{`"workdir"`, `"command"`, `"agent_args"`} {
		if strings.Contains(out, leak) {
			t.Fatalf("本机目录泄漏执行配置 %s: %s", leak, out)
		}
	}
	var body struct {
		NodeID string `json:"node_id"`
		Local  []struct {
			Card   protocol.AgentCard `json:"card"`
			NodeID string             `json:"node_id"`
		} `json:"local"`
		Network struct {
			Error string `json:"error"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body.NodeID != "nodeA" || len(body.Local) != 1 || body.Local[0].Card.Name != "echo" {
		t.Fatalf("本机目录不符: %s", out)
	}
	if body.Network.Error == "" {
		t.Fatalf("未配置代理时 network 应如实呈现 error: %s", out)
	}
}

// TestHubAgentsProxyE2E 全网目录代理（配 hub_api_url/token）：经真 hub 管理面
// 登录 + 查询，代理结果含其他 node 的 agent。
func TestHubAgentsProxyE2E(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	hubAPI := httptest.NewServer(apipkg.New(apipkg.Config{Core: c, AdminToken: "hub-admin"}).Handler())
	t.Cleanup(hubAPI.Close)

	nodeEP := bus.Endpoint("nodeA")
	svc := newService(t, nodeEP)

	if err := c.RegisterNode("nodeB", []protocol.AgentCard{{
		Name: "remote", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}}); err != nil {
		t.Fatal(err)
	}

	local := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:    newState(t, t.TempDir(), nil),
		HubAPIURL: hubAPI.URL, HubAPIToken: "hub-admin",
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(local.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req, _ := http.NewRequest(http.MethodGet, local.URL+"/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer tok-local")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"remote"`) {
		t.Fatalf("全网目录代理应含 nodeB remote: %d %s", resp.StatusCode, b)
	}
}
