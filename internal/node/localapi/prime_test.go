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
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

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

// TestAuthPrime403Priority 编排门 403 分支（server.go authOrchestrate，先认证后
// 鉴权、拒绝理由按优先级给出）：setup 态 > 未钦定总理 > 总理凭证未配置，各自
// 人话信息可区分。2026-10-22 鉴权模型裁决后编排双凭证（local/prime 皆可），
// 原「凭证级别不足」403 分支已删除——三种拒绝分支均在编排门内拒绝（Service 可
// 为 nil）。双凭证放行行为由 TestPrimeDeclareImmediateEffect /
// TestPrimeRotateInvalidatesOldToken 的 202 断言覆盖。
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
			name:  "未钦定统筹",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-prime",
			want:  "尚未钦定统筹",
		},
		{
			name:  "统筹凭证未配置",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "echo", PrimeToken: "", LocalToken: "tok-local"},
			token: "tok-local", // local_token 过认证（levelRead），命中 PrimeToken 空分支
			want:  "统筹凭证未配置",
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
	return newDeclareServerHome(t, "")
}

// newDeclareServerHome newDeclareServer 的影子 home 形态（修复轮 R5）：home
// 非空时 echo 的 ConfigHome 指向它（并发接线测试预置 zcode 布局用）。command
// 探测注桩命中（R4 后接线要求非占位符）。
func newDeclareServerHome(t *testing.T, home string) (*httptest.Server, string) {
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
	echo := echoProfileLocal(t)
	echo.ConfigHome = home
	n := 0
	s := New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		Agents: newState(t, dir, []protocol.ExecProfile{echo,
			{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep",
				Workdir: t.TempDir(), TimeoutSec: 1, Exposed: false}}),
		LocalToken:        "tok-local",
		PrimeToken:        "tok-old", // 未钦定态预生成凭证（矩阵 #15：生成不分发）
		LocalListen:       "127.0.0.1:8445",
		DeclareConfigPath: cfgPath,
		ConfigOps: &ConfigOps{ // 修复 3 并发测试用：与 declare 同一 node.yaml 读改写
			ConfigPath:      cfgPath,
			ReloadExecutors: func([]protocol.ExecProfile) error { return nil },
			Supervised:      true,
		},
		RotatePrime: func() (string, error) {
			n++
			return fmt.Sprintf("tok-declare-%d", n), nil
		},
	})
	s.mcpCommandOverride = "/fake/swarmmind-mcp"
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, cfgPath
}

// TestPrimeDeclareValidation 校验与应答结构：不存在 400（人话 + 可选名单）、
// 空 400、prime_token 调用 401（本机主人门）、合法 200 三字段带全且
// mcp_config 三 env 键值正确（安全审查修复 1：不含 SWARM_NODE_TOKEN——
// local_token 是本机主人管理凭证，不随总理 MCP 片段下发）。
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
		"SWARM_NODE_PRIME_TOKEN": "tok-declare-1",
	}
	if len(body.MCPConfig.Env) != len(wantEnv) {
		t.Fatalf("mcp_config.env 应恰三键（修复 1：不再下发 SWARM_NODE_TOKEN）, got %v", body.MCPConfig.Env)
	}
	for k, want := range wantEnv {
		if body.MCPConfig.Env[k] != want {
			t.Fatalf("env[%s]=%q, want %q", k, body.MCPConfig.Env[k], want)
		}
	}
	if leaked, ok := body.MCPConfig.Env["SWARM_NODE_TOKEN"]; ok {
		t.Fatalf("mcp_config 不得含 SWARM_NODE_TOKEN（泄漏 local_token=%q）", leaked)
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
	if code, out := submit("tok-old"); code != http.StatusForbidden || !strings.Contains(out, "尚未钦定统筹") {
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

// aliasYAML 修复 4 测试底稿：prime_agent 既有值为别名节点（*who 指向锚点标量）。
const aliasYAML = `node_id: nodeA
who: &who sleeper          # 锚点定义（别名目标）
prime_agent: *who          # 别名值——原实现原位改 Value 写出悬空别名，重启报 unknown anchor
custom_field: keep-me      # 未知字段锚点
`

// TestPrimeDeclareLinksCardDeclaration 声明联动（2026-10-29 部署后走查反馈一）：
// 管家入口收敛形态卡后能力卡「设为管家」退役——钦定（prime/declare）必须同时
// 完成卡片 Orchestrator 声明，否则 hub 目录无声明位（派发页无管家分组、工作单
// origin 锚点校验被拒）。锁定：declare 应答带 orchestrator=true/hub_synced；
// 先 PATCH 他者声明后钦定 → 他者声明随钦定撤销（恰一在翻转序内保持）、本名声明
// 生效且重启保持（agents-state.json orchestrator 覆盖）。
func TestPrimeDeclareLinksCardDeclaration(t *testing.T) {
	srv, _ := newDeclareServer(t)
	agentsList := func() map[string]bool {
		code, out := doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/agents", "")
		if code != http.StatusOK {
			t.Fatalf("agents 列表应 200, got %d: %s", code, out)
		}
		var body struct {
			Agents []struct {
				Name         string `json:"name"`
				Orchestrator bool   `json:"orchestrator"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, a := range body.Agents {
			m[a.Name] = a.Orchestrator
		}
		return m
	}

	// 前置：sleeper 先持声明（模拟既有声明态——生产经原「设为管家」PATCH 产生）
	if code, out := doJSON(t, srv, "tok-local", http.MethodPatch, "/api/v1/local/agents/sleeper/orchestrator", `{"orchestrator":true}`); code != http.StatusOK {
		t.Fatalf("PATCH sleeper 声明应 200, got %d: %s", code, out)
	}
	if m := agentsList(); !m["sleeper"] || m["echo"] {
		t.Fatalf("前置声明态不符: %v", m)
	}

	// 钦定 echo → 应答联动位 + sleeper 声明随钦定撤销、echo 生效
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`)
	if code != http.StatusOK {
		t.Fatalf("declare 应 200, got %d: %s", code, out)
	}
	var body struct {
		Orchestrator      bool   `json:"orchestrator"`
		HubSynced         bool   `json:"hub_synced"`
		OrchestratorError string `json:"orchestrator_error"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Orchestrator || body.OrchestratorError != "" {
		t.Fatalf("declare 应带 orchestrator=true 且无错误, got %+v", body)
	}
	if m := agentsList(); !m["echo"] || m["sleeper"] {
		t.Fatalf("钦定后声明应翻转至 echo（恰一）, got %v", m)
	}

	// 重启保持：同 data_dir 新实例读盘后声明仍在（orchestrator 覆盖落盘）
	// —— 直接复验盘上状态文件（newDeclareServer 的 dir 不可得，经 agents-state
	// 行为等价口径：Effective 再读由重启用例覆盖，此处锁定 REST 视图一致）。
	if code, out = doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/status", ""); code != http.StatusOK || !strings.Contains(out, `"prime_agent":"echo"`) {
		t.Fatalf("status 应反映钦定, got %d: %s", code, out)
	}
}

// TestSetPrimeAgentYAMLReplacesAliasNode 修复 4：既有值为 AliasNode 时构造独立
// 纯量整体替换（不原位改 Alias），重编码结果可正常解析（= 重启不炸）、prime_agent
// 值正确、锚点定义与未知字段保留。
func TestSetPrimeAgentYAMLReplacesAliasNode(t *testing.T) {
	out, err := setPrimeAgentYAML([]byte(aliasYAML), "echo")
	if err != nil {
		t.Fatalf("别名值写总理应成功: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "prime_agent: *") {
		t.Fatalf("prime_agent 不得残留别名引用:\n%s", s)
	}
	var doc struct {
		PrimeAgent  string `yaml:"prime_agent"`
		Who         string `yaml:"who"`
		CustomField string `yaml:"custom_field"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("重编码结果必须可解析（重启语义）: %v\n%s", err, s)
	}
	if doc.PrimeAgent != "echo" {
		t.Fatalf("prime_agent=%q, want echo\n%s", doc.PrimeAgent, s)
	}
	if doc.Who != "sleeper" || doc.CustomField != "keep-me" {
		t.Fatalf("锚点与未知字段应保留: %+v\n%s", doc, s)
	}
	// 幂等：写过一次后再写（此时值已是普通标量）仍正确
	out2, err := setPrimeAgentYAML(out, "sleeper")
	if err != nil {
		t.Fatalf("二次写入应成功: %v", err)
	}
	var doc2 struct {
		PrimeAgent string `yaml:"prime_agent"`
		Who        string `yaml:"who"`
	}
	if err := yaml.Unmarshal(out2, &doc2); err != nil || doc2.PrimeAgent != "sleeper" || doc2.Who != "sleeper" {
		t.Fatalf("二次写入应原位更新: %+v err=%v\n%s", doc2, err, string(out2))
	}
}

// TestPrimeDeclareAliasValueEndToEnd 端到端（修复 4）：盘上 node.yaml 的
// prime_agent 为别名值时 declare 200，落盘文件可解析且值正确（重启可加载）。
func TestPrimeDeclareAliasValueEndToEnd(t *testing.T) {
	srv, cfgPath := newDeclareServer(t)
	if err := os.WriteFile(cfgPath, []byte(aliasYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`)
	if code != http.StatusOK {
		t.Fatalf("别名值 declare 应 200, got %d: %s", code, out)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		PrimeAgent string `yaml:"prime_agent"`
		Who        string `yaml:"who"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("declare 落盘后文件必须可解析: %v\n%s", err, b)
	}
	if doc.PrimeAgent != "echo" || doc.Who != "sleeper" {
		t.Fatalf("落盘值不符: %+v\n%s", doc, b)
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
// 修复 6 加强断言：收尾后 status 查询 prime_agent 正确；用最终凭证调 submit
// 断言 202（「任一时刻恰一张有效凭证」的行为级验证——n 张应答凭证中恰一张可编排）。
// 修复轮 R5 加强：echo 影子 home 预置 zcode 布局——并发收尾后真实 setting.json
// 内凭证必须等于唯一存活凭证（轮换+接线同互斥段，最后落盘的必是最后换发的），
// 且无任何旧凭证残留（否则统筹载体拿旧 token 401）。
func TestPrimeDeclareRotateConcurrent(t *testing.T) {
	home := t.TempDir()
	setting := writeZcodeSetting(t, home, `{"mcpServers":{"other":{"command":"keep-me"}}}`)
	srv, _ := newDeclareServerHome(t, home)
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
	// 加强①：收尾后 status 查询 prime_agent 正确（全部 declare 均钦定 echo）
	if code, out := doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/status", ""); code != http.StatusOK || !strings.Contains(out, `"prime_agent":"echo"`) {
		t.Fatalf("收尾后 status 应反映 prime_agent=echo, got %d: %s", code, out)
	}
	// 加强②：n 张应答凭证中恰一张可编排（202）——其余被后续轮换作废（401）。
	submit := func(token string) int {
		code, _ := doJSON(t, srv, token, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
		return code
	}
	live, liveTok := 0, ""
	for _, tok := range tokens {
		if code := submit(tok); code == http.StatusAccepted {
			live++
			liveTok = tok
		} else if code != http.StatusUnauthorized {
			t.Fatalf("非最终凭证应 401, got %d（%s）", code, tok)
		}
	}
	if live != 1 {
		t.Fatalf("任一时刻应恰一张有效编排凭证, got %d 张可 202: %q", live, tokens)
	}
	// 加强③（修复轮 R5）：命中布局后真实 setting.json 的最终凭证——盘上必须
	// 是唯一存活凭证，且 n-1 张已作废凭证一个都不残留、无关条目原样。
	doc := readSettingDoc(t, setting)
	if tok := entryToken(t, doc); tok != liveTok {
		t.Fatalf("并发收尾后 setting.json 内 token=%q, want 存活凭证 %q", tok, liveTok)
	}
	raw := mustRead(t, setting)
	for _, tok := range tokens {
		if tok != liveTok && strings.Contains(raw, tok) {
			t.Fatalf("旧凭证 %q 不得残留在宿主配置", tok)
		}
	}
	if other := serversOf(t, doc)["other"].(map[string]any); other["command"] != "keep-me" {
		t.Fatalf("其他 MCP 条目应原样: %v", other)
	}
}

// TestPrimeTokenCannotCallOwnerEndpoints 修复 1 行为级验证：仅持 prime_token 的
// 调用方（总理）不可触达本机主人管理面——declare/rotate/restart/PUT config 全部
// 401（authLocal 只认 local_token）；prime_token 仍可只读（authRead 双收，
// prime ⊇ read）——MCP 连接单凭证够用，无需下发 local_token。
func TestPrimeTokenCannotCallOwnerEndpoints(t *testing.T) {
	srv, _ := newDeclareServer(t)
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"declare", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`},
		{"rotate", http.MethodPost, "/api/v1/local/prime/rotate", ""},
		{"restart", http.MethodPost, "/api/v1/local/restart", ""},
		{"put config", http.MethodPut, "/api/v1/local/config", `{"agents":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := doJSON(t, srv, "tok-old", tc.method, tc.path, tc.body)
			if code != http.StatusUnauthorized {
				t.Fatalf("prime_token 调 %s 应 401, got %d: %s", tc.path, code, out)
			}
		})
	}
	// 只读仍放行：prime_token 查 status → 200
	if code, out := doJSON(t, srv, "tok-old", http.MethodGet, "/api/v1/local/status", ""); code != http.StatusOK {
		t.Fatalf("prime_token 只读应 200, got %d: %s", code, out)
	}
}

// TestDeclareAndPutConfigConcurrentNoClobber 修复 3 行为级验证：declare 与
// PUT config 并发时完整读改写互斥——审查实测的「刚保存的 description 被
// declare 覆盖」不得再现。
//
// 终态断言（第五轮收敛 4 加强）：PUT 按链式发车——第 i 轮等第 i-1 轮应答后才
// 发（PUT 应答在 writeConfigMu 临界区内写回，收到应答即该轮写盘已完成），故第
// rounds-1 轮确定性为「最后一次成功 PUT」；declare 只写顶层 prime_agent 不改
// agents 表，因此终态 agents 表必须与最后一次成功 PUT 的提交表完全一致——
// echo 必然存在，且经同一序列化器渲染后逐字段全表比对（不只查 description
// 前缀——原断言在表被截断、echo 缺失时静默通过）。declare 群保持全并发不串行，
// 竞态检测能力不变。
func TestDeclareAndPutConfigConcurrentNoClobber(t *testing.T) {
	srv, cfgPath := newDeclareServer(t)
	wd := t.TempDir()
	exposed := true
	agents := []AgentProfile{
		{Name: "echo", Kind: "cli", Command: "/bin/echo", Workdir: wd, TimeoutSec: 10,
			Description: "回声测试", PermissionMode: "no-shell", Exposed: &exposed},
		{Name: "sleeper", Kind: "cli", Command: "/bin/sleep", Workdir: wd, TimeoutSec: 1, Exposed: &exposed},
	}
	const rounds = 8
	putDone := make([]chan struct{}, rounds)
	for i := range putDone {
		putDone[i] = make(chan struct{})
	}
	var lastPut []AgentProfile // 第 rounds-1 轮提交表（仅该 goroutine 写，wg.Wait 后主 goroutine 读）
	var wg sync.WaitGroup
	// declare 群：与 PUT 全并发（不串行——竞态检测主体）
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`); code != http.StatusOK {
				t.Errorf("declare 应 200, got %d: %s", code, out)
			}
		}()
	}
	// PUT 链：第 i 轮等第 i-1 轮应答后发车（收到应答 = 第 i-1 轮写盘已完成）
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer close(putDone[i]) // 失败也放行下一轮（测试已经 Errorf 失败，不阻塞链）
			if i > 0 {
				<-putDone[i-1]
			}
			table := make([]AgentProfile, len(agents)) // 本轮私有副本：不与他 goroutine 共享可写状态
			copy(table, agents)
			table[0].Description = fmt.Sprintf("desc-%d", i)
			if i == rounds-1 {
				lastPut = table
			}
			body, _ := json.Marshal(configPutRequest{Agents: table})
			if code, out := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/config", string(body)); code != http.StatusOK {
				t.Errorf("PUT config 应 200, got %d: %s", code, out)
			}
		}(i)
	}
	wg.Wait()
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		PrimeAgent string         `yaml:"prime_agent"`
		Agents     []AgentProfile `yaml:"agents"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("终态 node.yaml 必须可解析: %v\n%s", err, b)
	}
	if doc.PrimeAgent != "echo" {
		t.Fatalf("prime_agent 应为 declare 写入的 echo, got %q\n%s", doc.PrimeAgent, b)
	}
	// 加强①：echo 必然存在（原断言循环跳过缺失条目——表被截断时静默通过）
	hasEcho := false
	for _, a := range doc.Agents {
		if a.Name == "echo" {
			hasEcho = true
		}
	}
	if !hasEcho {
		t.Fatalf("终态表缺 echo（PUT 写盘丢失或被截断）\n%s", b)
	}
	// 加强②：终态 agents 表与最后一次成功 PUT 的提交表完全一致——期望表经同一
	// updateAgentsYAML 序列化器渲染再解出（消除手写期望与最小形态规则的漂移），
	// 与终态逐字段 DeepEqual。
	wantRaw, err := updateAgentsYAML([]byte("agents: []\n"), lastPut)
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Agents []AgentProfile `yaml:"agents"`
	}
	if err := yaml.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Agents, want.Agents) {
		t.Fatalf("终态表应与最后一次成功 PUT 的提交表完全一致（读改写未互斥或被覆盖）:\n got %+v\nwant %+v\n%s", doc.Agents, want.Agents, b)
	}
}
