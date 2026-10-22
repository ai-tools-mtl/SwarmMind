package localapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 本地 API 单测：令牌鉴权、hub 不可达映射 503、能力目录（仅本机，无 network 键）、
// 派单边界锁（跨节点目标 400，不发帧不留痕）。提交/查询的数据面
// （SubmitLocal/QueryLocal 关联链）在 service 包覆盖。

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

	// hub 不可达 → 503 hub_unreachable（目标为本机，已过 D8 边界锁）
	code, out := do("tok-local", http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(out, protocol.CodeHubUnreachable) {
		t.Fatalf("hub 不可达应 503 hub_unreachable, got %d: %s", code, out)
	}

	// 坏请求体 → 400
	if code, _ = do("tok-local", http.MethodPost, "/api/v1/tasks", `not-json`); code != http.StatusBadRequest {
		t.Fatalf("坏请求体应 400, got %d", code)
	}

	// 本机能力目录：card 不含 ExecProfile 字段；node 无全网视图——响应无 network 键
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
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body.NodeID != "nodeA" || len(body.Local) != 1 || body.Local[0].Card.Name != "echo" {
		t.Fatalf("本机目录不符: %s", out)
	}
	if strings.Contains(out, `"network"`) {
		t.Fatalf("本机目录不得含 network 键（node 无全网视图）: %s", out)
	}

	// 本机状态：node 只呈现本机事实——响应无 mailbox/inflight 键
	code, out = do("tok-local", http.MethodGet, "/api/v1/local/status", "")
	if code != http.StatusOK {
		t.Fatalf("local/status 应 200, got %d: %s", code, out)
	}
	for _, gone := range []string{`"mailbox"`, `"inflight"`} {
		if strings.Contains(out, gone) {
			t.Fatalf("local/status 不得含 %s 键（hub 侧事实不代理展示）: %s", gone, out)
		}
	}
}

// TestSubmitCrossNodeRejected 边界锁（方案 v2 D8）：本地 API 派单 target_node 为
// 外机 → 400 拒绝，不发 task_submit 帧、不留提交日志、不静默改写目标。
func TestSubmitCrossNodeRejected(t *testing.T) {
	bus := transport.NewBus()
	frames := 0 // hub 侧帧计数：D8 拒绝必须发生在发帧之前
	bus.Endpoint("hub").Subscribe(func(env *protocol.Envelope) { frames++ })
	svc := newService(t, bus.Endpoint("nodeA"))

	tl, err := OpenTaskLog(filepath.Join(t.TempDir(), "tasklog"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), []protocol.ExecProfile{echoProfileLocal(t)}),
		TaskLog:    tl,
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tasks",
		strings.NewReader(`{"target_node":"nodeB","target_agent":"echo","submitted_by":"p"}`))
	req.Header.Set("Authorization", "Bearer tok-local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("跨节点派单应 400, got %d: %s", resp.StatusCode, b)
	}
	if frames != 0 {
		t.Fatalf("拒绝不得发任何帧, got %d", frames)
	}
	if l := tl.List(); len(l) != 0 {
		t.Fatalf("拒绝不得留提交日志: %+v", l)
	}
	if !strings.Contains(string(b), "nodeB") {
		t.Fatalf("错误信息应如实回显违规目标（不静默改写）: %s", b)
	}

	// 本机目标不被此锁误伤（链路本身另见 service 包全链测试）
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tasks",
		strings.NewReader(`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`))
	req2.Header.Set("Authorization", "Bearer tok-local")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	_ = resp2.Body.Close()
	// bus 上无 "hub" 端点 → hub_unreachable（503），而非 400——边界锁只拦外机目标
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("本机目标应放行进入转发链（此处 hub 不可达 503）, got %d", resp2.StatusCode)
	}
}
