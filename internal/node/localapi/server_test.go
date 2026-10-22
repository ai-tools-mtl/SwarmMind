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
		PrimeToken: "tok-prime",
		PrimeAgent: "echo",
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

	// 编排分级（矩阵 #14）：local_token 调派单 → 403 prime_required（双路径下
	// 本地直执行不依赖 hub，原 hub_unreachable 503 场景已随旧链路删除）
	code, out := do("tok-local", http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	if code != http.StatusForbidden || !strings.Contains(out, "prime_required") {
		t.Fatalf("local_token 调编排应 403 prime_required, got %d: %s", code, out)
	}
	// 总理凭证过编排门：target 本机合法，但 fixture 无 runner → 404 agent_not_found
	//（说明 prime 门与 D8 均已通过，到达业务校验层）
	code, out = do("tok-prime", http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	if code != http.StatusNotFound || !strings.Contains(out, protocol.CodeAgentNotFound) {
		t.Fatalf("总理派单应过编排门到达业务校验（404 agent_not_found）, got %d: %s", code, out)
	}

	// 坏请求体 → 400（总理凭证，请求体解析在编排门之后）
	if code, _ = do("tok-prime", http.MethodPost, "/api/v1/tasks", `not-json`); code != http.StatusBadRequest {
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
		PrimeToken: "tok-prime",
		PrimeAgent: "echo",
	}).Handler())
	t.Cleanup(srv.Close)

	// 分级先于校验：local_token 调编排 → 403 prime_required（矩阵 #14），
	// 此时连 D8 都未执行——鉴权与业务校验的顺序由本断言钉死。
	reqL, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tasks",
		strings.NewReader(`{"target_node":"nodeB","target_agent":"echo","submitted_by":"p"}`))
	reqL.Header.Set("Authorization", "Bearer tok-local")
	respL, err := http.DefaultClient.Do(reqL)
	if err != nil {
		t.Fatal(err)
	}
	defer respL.Body.Close()
	bL, _ := io.ReadAll(respL.Body)
	if respL.StatusCode != http.StatusForbidden || !strings.Contains(string(bL), "prime_required") {
		t.Fatalf("local_token 调编排应 403 prime_required, got %d: %s", respL.StatusCode, bL)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tasks",
		strings.NewReader(`{"target_node":"nodeB","target_agent":"echo","submitted_by":"p"}`))
	req.Header.Set("Authorization", "Bearer tok-prime")
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

	// 本机目标不被此锁误伤：总理凭证过 D8 后到达业务校验层（fixture 无 runner
	// → 404 agent_not_found；本地直执行不再经 hub，无 hub_unreachable 场景）
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tasks",
		strings.NewReader(`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`))
	req2.Header.Set("Authorization", "Bearer tok-prime")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound || !strings.Contains(string(b2), protocol.CodeAgentNotFound) {
		t.Fatalf("本机目标应过 D8 到达业务校验（404 agent_not_found）, got %d: %s", resp2.StatusCode, b2)
	}
}
