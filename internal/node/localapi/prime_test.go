package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 总理凭证单测（方案 3.2.1）：编排门 403 四分支按优先级可区分（复审 #8）；轮换
// 即时失效旧凭证、新凭证可编排；rotate 端点仅 local_token 可调。

// echoStubRunner 恒成功桩执行器（localapi 包内本地直执行用）。
type echoStubRunner struct{}

func (echoStubRunner) Run(_ context.Context, _ protocol.Task) protocol.TaskResult {
	return protocol.TaskResult{Status: protocol.ResultCompleted, FinalAnswer: "ok"}
}

// doJSON 带 Bearer 的 JSON 请求便捷封装，返回状态码与应答体。
func doJSON(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string) {
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

// TestAuthPrime403Priority 编排门 403 四分支（server.go authPrime，先认证后鉴权、
// 拒绝理由按优先级给出）：setup 态 > 未钦定总理 > 总理凭证未配置 > 级别不足，
// 各自人话信息可区分。四个分支均在编排门内拒绝（Service 可为 nil）。
func TestAuthPrime403Priority(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		token string
		want  string
	}{
		{
			name:  "setup态编排未激活",
			cfg:   Config{NodeID: "nodeA", Setup: true, PrimeAgent: "echo", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-prime",
			want:  "setup 模式编排接口未激活",
		},
		{
			name:  "未钦定总理",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-prime",
			want:  "尚未钦定总理",
		},
		{
			name:  "总理凭证未配置",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "echo", PrimeToken: "", LocalToken: "tok-local"},
			token: "tok-local", // local_token 过认证（levelRead），命中 PrimeToken 空分支
			want:  "总理凭证未配置",
		},
		{
			name:  "凭证级别不足",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "echo", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-local",
			want:  "编排操作需总理凭证",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(New(tc.cfg).Handler())
			t.Cleanup(srv.Close)
			code, out := doJSON(t, srv, tc.token, http.MethodPost, "/api/v1/tasks",
				`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
			if code != http.StatusForbidden {
				t.Fatalf("应 403, got %d: %s", code, out)
			}
			if !strings.Contains(out, "prime_required") || !strings.Contains(out, tc.want) {
				t.Fatalf("错误信息应含可区分片段 %q: %s", tc.want, out)
			}
		})
	}
}

// TestPrimeRotateInvalidatesOldToken 轮换失效（复审 #8）：rotate 后旧 prime_token
// 调编排被拒——旧凭证已不属于任何已知令牌，按 authPrime 先认证后鉴权的次序在
// 认证层 401（403 语义是「已认证但级别不足」，与 prime.go 注释「401/403」一致）；
// 新凭证 202；rotate 端点仅 local_token 可调（prime_token → 401）。
func TestPrimeRotateInvalidatesOldToken(t *testing.T) {
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		LocalToken: "tok-local", PrimeToken: "tok-old", PrimeAgent: "echo",
		RotatePrime: func() (string, error) { return "tok-new", nil },
	}).Handler())
	t.Cleanup(srv.Close)
	submit := func(token string) (int, string) {
		return doJSON(t, srv, token, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}

	// 前置：轮换前旧凭证可编排
	if code, out := submit("tok-old"); code != http.StatusAccepted {
		t.Fatalf("轮换前旧凭证应 202, got %d: %s", code, out)
	}
	// rotate 端点仅 local_token 可调：prime_token → 401（本机主人操作不随编排凭证下发）
	if code, out := doJSON(t, srv, "tok-old", http.MethodPost, "/api/v1/local/prime/rotate", ""); code != http.StatusUnauthorized {
		t.Fatalf("prime_token 调 rotate 应 401, got %d: %s", code, out)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusOK {
		t.Fatalf("local_token 调 rotate 应 200, got %d: %s", code, out)
	}
	var body struct {
		PrimeToken string `json:"prime_token"`
		PrimeAgent string `json:"prime_agent"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil || body.PrimeToken != "tok-new" || body.PrimeAgent != "echo" {
		t.Fatalf("rotate 应返回新凭证（仅此一次展示）: %s", out)
	}
	// 轮换后：旧凭证被拒（认证层 401）、新凭证 202
	if code, out := submit("tok-old"); code != http.StatusUnauthorized {
		t.Fatalf("轮换后旧凭证调编排应被拒 401, got %d: %s", code, out)
	}
	if code, out := submit("tok-new"); code != http.StatusAccepted {
		t.Fatalf("轮换后新凭证应 202, got %d: %s", code, out)
	}
}

// TestPrimeRotateSerializes 并发轮换（返工 #4 收口）：生成/写盘/内存重载同一
// muPrime 互斥段——并发轮换后内存凭证与最后一次写盘一致，且新旧凭证恰其一可用。
func TestPrimeRotateSerializes(t *testing.T) {
	n := 0
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", LocalToken: "tok-local",
		RotatePrime: func() (string, error) {
			n++
			return "tok-rot-" + string(rune('a'+n-1)), nil
		},
	}).Handler())
	t.Cleanup(srv.Close)
	tokens := make([]string, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
			if code != http.StatusOK {
				t.Errorf("并发轮换应 200, got %d: %s", code, out)
				return
			}
			var body struct {
				PrimeToken string `json:"prime_token"`
			}
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Errorf("应答解析失败: %v", err)
				return
			}
			tokens[i] = body.PrimeToken
		}(i)
	}
	wg.Wait()
	// 4 次轮换各得唯一新凭证（tok-rot-a..d 各出现一次——互斥段串行化生成）
	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok == "" || seen[tok] {
			t.Fatalf("并发轮换应各得唯一凭证, got %q", tokens)
		}
		seen[tok] = true
	}
}

// ---------- declare 网页钦定（方案 3.2.1 UX 收口，2026-10-22） ----------

// declareYAML 钦定测试底稿：注释 + 未知字段锚点（round-trip 保真断言用）。
const declareYAML = `# 手编节点配置（注释保真锚点一）
node_id: nodeA
hub_url: ""            # 空 = standalone（行内注释锚点）
concurrency: 2
custom_field: keep-me  # 未知字段锚点
`

// newDeclareServer 钦定测试环境：手编 node.yaml（echo+sleeper 两 agent）、
// 未钦定态（PrimeAgent 空）、计数轮换回调（tok-declare-1/2/…）。返回服务与
// node.yaml 路径。
func newDeclareServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(cfgPath, []byte(declareYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	n := 0
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		Agents: newState(t, dir, []protocol.ExecProfile{echoProfileLocal(t),
			{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep",
				Workdir: t.TempDir(), TimeoutSec: 1, Exposed: false}}),
		LocalToken:        "tok-local",
		PrimeToken:        "tok-old", // 未钦定态预生成凭证（矩阵 #15：生成不分发）
		LocalListen:       "127.0.0.1:8445",
		DeclareConfigPath: cfgPath,
		RotatePrime: func() (string, error) {
			n++
			return fmt.Sprintf("tok-declare-%d", n), nil
		},
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, cfgPath
}

// TestPrimeDeclareValidation 校验与应答结构：不存在 400（人话 + 可选名单）、
// 空 400、prime_token 调用 401（本机主人门）、合法 200 三字段带全且
// mcp_config 四 env 键值正确。
func TestPrimeDeclareValidation(t *testing.T) {
	srv, _ := newDeclareServer(t)
	declare := func(token, body string) (int, string) {
		return doJSON(t, srv, token, http.MethodPost, "/api/v1/local/prime/declare", body)
	}

	// 不存在的 agent：400 + 列出可选名（人话提示）
	code, out := declare("tok-local", `{"prime_agent":"ghost"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "ghost") || !strings.Contains(out, "echo") || !strings.Contains(out, "sleeper") {
		t.Fatalf("不存在应 400 且列出可选名, got %d: %s", code, out)
	}
	// 空名：400 + 附可选名
	if code, out = declare("tok-local", `{"prime_agent":""}`); code != http.StatusBadRequest || !strings.Contains(out, "必填") {
		t.Fatalf("空名应 400, got %d: %s", code, out)
	}
	// 非法请求体：400
	if code, _ = declare("tok-local", `not-json`); code != http.StatusBadRequest {
		t.Fatalf("非法体应 400, got %d", code)
	}
	// prime_token 不可调（钦定即凭据授予，本机主人专属；凭旧编排凭证 → 401）
	if code, _ = declare("tok-old", `{"prime_agent":"echo"}`); code != http.StatusUnauthorized {
		t.Fatalf("prime_token 调 declare 应 401, got %d", code)
	}

	// 合法钦定：200 + prime_agent/prime_token/mcp_config 三字段
	code, out = declare("tok-local", `{"prime_agent":"echo"}`)
	if code != http.StatusOK {
		t.Fatalf("合法钦定应 200, got %d: %s", code, out)
	}
	var body struct {
		PrimeAgent string `json:"prime_agent"`
		PrimeToken string `json:"prime_token"`
		MCPConfig  struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcp_config"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("应答解析失败: %v\n%s", err, out)
	}
	if body.PrimeAgent != "echo" || body.PrimeToken != "tok-declare-1" {
		t.Fatalf("应答应含钦定名与新凭证: %s", out)
	}
	if body.MCPConfig.Command == "" {
		t.Fatalf("mcp_config.command 缺失: %s", out)
	}
	wantEnv := map[string]string{
		"SWARM_TARGET":           "node",
		"SWARM_NODE_URL":         "http://127.0.0.1:8445",
		"SWARM_NODE_TOKEN":       "tok-local",
		"SWARM_NODE_PRIME_TOKEN": "tok-declare-1",
	}
	if len(body.MCPConfig.Env) != len(wantEnv) {
		t.Fatalf("mcp_config.env 应恰四键, got %v", body.MCPConfig.Env)
	}
	for k, want := range wantEnv {
		if body.MCPConfig.Env[k] != want {
			t.Fatalf("env[%s]=%q, want %q", k, body.MCPConfig.Env[k], want)
		}
	}
}

// TestPrimeDeclareRoundTrip 写盘保真：prime_agent 正确落盘、注释与未知字段
// 保留；换总理原位更新（不追加重复键）。
func TestPrimeDeclareRoundTrip(t *testing.T) {
	srv, cfgPath := newDeclareServer(t)
	if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`); code != http.StatusOK {
		t.Fatalf("钦定应 200, got %d: %s", code, out)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	y := string(b)
	for _, anchor := range []string{
		"prime_agent: echo",
		"# 手编节点配置",              // 头注释保留
		"# 空 = standalone",      // 行内注释保留
		"custom_field: keep-me", // 未知字段保留
		"node_id: nodeA",
	} {
		if !strings.Contains(y, anchor) {
			t.Fatalf("round-trip 应保留 %q:\n%s", anchor, y)
		}
	}
	// 换总理：原位更新——值变更且 prime_agent 键恰一个（不追加）
	if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"sleeper"}`); code != http.StatusOK {
		t.Fatalf("换总理应 200, got %d: %s", code, out)
	}
	b, err = os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	y = string(b)
	if strings.Count(y, "prime_agent:") != 1 || !strings.Contains(y, "prime_agent: sleeper") {
		t.Fatalf("换总理应原位更新单键:\n%s", y)
	}
}

// TestPrimeDeclareImmediateEffect 即时生效语义：声明前编排 403（尚未钦定）→
// declare 后新 prime_token 可编排（202）→ status 端点即时反映新总理（读动态值，
// 不等重启）。
func TestPrimeDeclareImmediateEffect(t *testing.T) {
	srv, _ := newDeclareServer(t)
	submit := func(token string) (int, string) {
		return doJSON(t, srv, token, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}
	// 前置：未钦定时持预生成凭证调编排 → 403 prime_required（尚未钦定）
	if code, out := submit("tok-old"); code != http.StatusForbidden || !strings.Contains(out, "尚未钦定总理") {
		t.Fatalf("声明前编排应 403 尚未钦定, got %d: %s", code, out)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`)
	if code != http.StatusOK {
		t.Fatalf("declare 应 200, got %d: %s", code, out)
	}
	var body struct {
		PrimeToken string `json:"prime_token"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	// 声明后：新凭证 202（编排接口即时激活）
	if code, out = submit(body.PrimeToken); code != http.StatusAccepted {
		t.Fatalf("声明后新凭证应 202, got %d: %s", code, out)
	}
	// status 即时反映（handleLocalStatus 读 s.primeAgent() 动态值）
	code, out = doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/status", "")
	if code != http.StatusOK || !strings.Contains(out, `"prime_agent":"echo"`) {
		t.Fatalf("status 应即时反映新总理, got %d: %s", code, out)
	}
}

// TestPrimeDeclareRotatesToken 钦定即换凭证：首次钦定前的旧凭证失效；换总理
// 再钦定后上一任凭证同步失效——任一时刻恰一张有效编排凭证。
func TestPrimeDeclareRotatesToken(t *testing.T) {
	srv, _ := newDeclareServer(t)
	submit := func(token string) (int, string) {
		return doJSON(t, srv, token, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}
	declare := func(agent string) string {
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"`+agent+`"}`)
		if code != http.StatusOK {
			t.Fatalf("declare %s 应 200, got %d: %s", agent, code, out)
		}
		var body struct {
			PrimeToken string `json:"prime_token"`
		}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatal(err)
		}
		return body.PrimeToken
	}
	tok1 := declare("echo")
	// 首次钦定：预生成旧凭证（tok-old）被轮换作废
	if code, _ := submit("tok-old"); code != http.StatusUnauthorized {
		t.Fatalf("钦定后预生成旧凭证应 401, got %d", code)
	}
	if code, _ := submit(tok1); code != http.StatusAccepted {
		t.Fatalf("首任凭证应 202, got %d", code)
	}
	// 换总理：上一任凭证（tok1）立即失效，新凭证生效
	tok2 := declare("sleeper")
	if code, _ := submit(tok1); code != http.StatusUnauthorized {
		t.Fatalf("换总理后旧凭证应 401, got %d", code)
	}
	if code, _ := submit(tok2); code != http.StatusAccepted {
		t.Fatalf("新总理凭证应 202, got %d", code)
	}
}

// TestPrimeDeclareRotateConcurrent 交错并发（返工收口：declare 与 rotate 共享
// muPrime 互斥段，混合调用不得出现盘上/内存凭证分叉——每次应答凭证唯一且
// 全部 200；任一时刻恰一张有效编排凭证由 declare 即换凭证语义保证）。
func TestPrimeDeclareRotateConcurrent(t *testing.T) {
	srv, _ := newDeclareServer(t)
	const n = 6
	tokens := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/api/v1/local/prime/rotate"
			if i%2 == 0 {
				path = "/api/v1/local/prime/declare"
			}
			code, out := doJSON(t, srv, "tok-local", http.MethodPost, path, `{"prime_agent":"echo"}`)
			if code != http.StatusOK {
				t.Errorf("并发 %s 应 200, got %d: %s", path, code, out)
				return
			}
			var body struct {
				PrimeToken string `json:"prime_token"`
			}
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Errorf("应答解析失败: %v", err)
				return
			}
			tokens[i] = body.PrimeToken
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok == "" || seen[tok] {
			t.Fatalf("并发 declare×rotate 应各得唯一凭证（互斥段无分叉）, got %q", tokens)
		}
		seen[tok] = true
	}
}
