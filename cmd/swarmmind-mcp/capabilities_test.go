package main

// swarmmind_capabilities 测试（W02-02，docs/tasks/v3/W02-mcp-access.md 权限矩阵
// 「能力元信息」行 + 验收「tools/list+capabilities 对两 backend 做实际 stdio 测试；
// 注册代码/可执行装配缺任一步即 FAIL」）：
//   - 装配：hub/node 两 backend tools() 均含 swarmmind_capabilities（注册代码），
//     描述写任务书原话「查看当前调用范围与可用能力」并注明不替代 tools/list；
//   - 渲染：单源 renderCapabilities 单测（身份/范围/授权态行/未收录键透传/空版本）；
//   - 进程内端到端：node 后端 × 真 localapi（enabled/disabled 授权态联动、会话内核
//     装配联动 sessions:true）；hub 后端 × 真 hub 管理面（真实 PAT 鉴权链）；
//   - 子进程 stdio：真二进制 hub 模式 tools/list 含 swarmmind_capabilities +
//     tools/call 返回真实能力标记（node 模式 stdio 见
//     TestSubprocessNodeStdioHandshake，同批覆盖）。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

func TestCapabilitiesToolAssembly(t *testing.T) {
	// 两 backend tools() 均装配（注册代码在位），描述含任务书原话与 tools/list 提示
	for _, name := range []string{"hub", "node"} {
		var tds []toolDef
		if name == "hub" {
			tds = (hubBackend{}).tools()
		} else {
			tds = (nodeBackend{}).tools()
		}
		found := false
		for _, td := range tds {
			if td.Name != "swarmmind_capabilities" {
				continue
			}
			found = true
			if !strings.Contains(td.Description, "查看当前调用范围与可用能力") {
				t.Fatalf("%s 模式 capabilities 描述缺任务书原话: %s", name, td.Description)
			}
			if !strings.Contains(td.Description, "tools/list") {
				t.Fatalf("%s 模式 capabilities 描述应注明以 tools/list 为准: %s", name, td.Description)
			}
			if len(td.InputSchema["properties"].(map[string]any)) != 0 {
				t.Fatalf("%s 模式 capabilities 应无参数: %v", name, td.InputSchema)
			}
		}
		if !found {
			t.Fatalf("%s 模式未装配 swarmmind_capabilities: %v", name, tds)
		}
	}

	// 渲染单源：hub 形态（无授权行、未知键透传、空版本→未知）
	hubJSON := `{"server":"hub","version":"","scope":"hub 全网","features":{"tasks":true,"sessions":true,"works":true,"groups":false,"spaces":false,"future_thing":true}}`
	out, err := renderCapabilities([]byte(hubJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"服务端：hub（版本 未知）", "调用范围：hub 全网", "任务派发与查询：可用", "协作群：未提供", "空间：未提供", "future_thing：可用", "不替代 tools/list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("hub 渲染缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MCP 编排授权") {
		t.Fatalf("hub 渲染不应出现本机授权行:\n%s", out)
	}

	// 渲染单源：node 形态（授权态两态 + 范围人话）
	nodeOn, err := renderCapabilities([]byte(`{"server":"node","version":"v1","scope":"本机","features":{"tasks":true,"mcp_access_enabled":true}}`))
	if err != nil || !strings.Contains(nodeOn, "本机 MCP 编排授权：已启用") || !strings.Contains(nodeOn, "跨机派单请经 hub 模式") {
		t.Fatalf("node 渲染（启用）不符 err=%v:\n%s", err, nodeOn)
	}
	nodeOff, err := renderCapabilities([]byte(`{"server":"node","version":"v1","scope":"本机","features":{"mcp_access_enabled":false}}`))
	if err != nil || !strings.Contains(nodeOff, "未启用") || !strings.Contains(nodeOff, "mcp_disabled") || !strings.Contains(nodeOff, "无需钦定管家") {
		t.Fatalf("node 渲染（禁用）应含未启用指引 err=%v:\n%s", err, nodeOff)
	}

	// 非法应答：解析失败如实报错
	if _, err := renderCapabilities([]byte("not-json")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestNodeCapabilitiesToolEndToEnd node 后端 × 真 localapi：授权态联动（夹具
// enabled/disabled）+ 会话内核装配联动（sessions:true）+ hub 专属能力如实未提供。
func TestNodeCapabilitiesToolEndToEnd(t *testing.T) {
	// enabled 夹具（newLocalNode：enabled=primeAgent 非空）
	srv, _, _ := newLocalNode(t, "echo")
	bd := newNodeBackendFor(srv.URL)
	text, isErr := callText(t, bd, "swarmmind_capabilities", nil)
	if isErr {
		t.Fatalf("capabilities 不应失败: %s", text)
	}
	for _, want := range []string{"服务端：node", "本机 MCP 编排授权：已启用", "任务派发与查询：可用", "协作群：未提供", "工作单（多步断点续管）：未提供", "不替代 tools/list"} {
		if !strings.Contains(text, want) {
			t.Fatalf("node capabilities 输出缺 %q:\n%s", want, text)
		}
	}
	// 该夹具无会话内核：sessions 如实未提供（不虚构）
	if !strings.Contains(text, "持久会话：未提供") {
		t.Fatalf("无会话内核夹具应 sessions 未提供:\n%s", text)
	}

	// disabled 夹具：授权态联动——capabilities 本身 authRead 可读（只读门不受
	// 编排开关影响），但授权行如实标未启用并指路
	srvOff, _, _ := newLocalNode(t, "")
	bdOff := newNodeBackendFor(srvOff.URL)
	text, isErr = callText(t, bdOff, "swarmmind_capabilities", nil)
	if isErr {
		t.Fatalf("disabled 下 capabilities 仍应可读（authRead）: %s", text)
	}
	if !strings.Contains(text, "本机 MCP 编排授权：未启用") || !strings.Contains(text, "mcp_disabled") {
		t.Fatalf("disabled 夹具授权行不符:\n%s", text)
	}

	// 会话内核装配联动：sessions:true（真会话内核夹具，真实能力标记非硬编码）
	srvSess, _, _ := newLocalSessionNode(t, "")
	text, isErr = callText(t, newNodeBackendFor(srvSess.URL), "swarmmind_capabilities", nil)
	if isErr || !strings.Contains(text, "持久会话：可用") {
		t.Fatalf("会话内核装配后应 sessions 可用（isErr=%v）:\n%s", isErr, text)
	}
}

// newHubCapServer 起真 hub 管理面（store+core+PAT 存储+版本注入），登录并创建
// PAT，返回 server 与 Bearer 明文（hub 后端/stdio 子进程共用的真实鉴权链）。
func newHubCapServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
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
	srv := httptest.NewServer(api.New(api.Config{
		Core: c, AdminToken: "tok-admin-cap", DataDir: t.TempDir(), Version: "stdio-cap-1",
	}).Handler())
	t.Cleanup(srv.Close)

	// 登录 → 创建 PAT（admin 会话专属门；PAT 是 MCP hub 面既有 Bearer 通道）
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := hc.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok-admin-cap"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应 200, got %d", resp.StatusCode)
	}
	resp, err = hc.Post(srv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"cap-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建 PAT 应 201, got %d", resp.StatusCode)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.Token == "" {
		t.Fatalf("PAT 应答缺明文: %v", err)
	}
	return srv, created.Token
}

// TestHubCapabilitiesToolEndToEnd hub 后端 × 真 hub 管理面（真实 PAT Bearer 链）：
// 版本实查、装配标记与 Server 构造一致（Core 接线→核心三能力 true；Groups 未
// 接线→false 如实）；无私密断言（输出不含 PAT 明文/admin 令牌）。
func TestHubCapabilitiesToolEndToEnd(t *testing.T) {
	srv, pat := newHubCapServer(t)
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = srv.URL, pat
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	text, err := toolCapabilities()
	if err != nil {
		t.Fatalf("capabilities 失败: %v", err)
	}
	for _, want := range []string{"服务端：hub（版本 stdio-cap-1）", "调用范围：hub 全网",
		"任务派发与查询：可用", "持久会话：可用", "工作单（多步断点续管）：可用",
		"协作群：未提供", "空间：未提供", "不替代 tools/list"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hub capabilities 输出缺 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, pat) || strings.Contains(text, "tok-admin-cap") || strings.Contains(text, "smsw_") {
		t.Fatalf("输出不得含凭据字样:\n%s", text)
	}
}

// TestSubprocessStdioCapabilitiesHub 真二进制 stdio（hub 模式，真 hub 管理面）：
// tools/list 含 swarmmind_capabilities（注册+可执行装配双在位）→ tools/call
// 返回真实能力标记（版本实查）。node 模式 stdio 同批覆盖见
// TestSubprocessNodeStdioHandshake（mcp_integration_test.go）。
func TestSubprocessStdioCapabilitiesHub(t *testing.T) {
	bin := buildMCPBinary(t)
	srv, pat := newHubCapServer(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"SWARM_TARGET=hub",
		"SWARM_HUB_URL="+srv.URL,
		"SWARM_HUB_TOKEN="+pat,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Wait()
	})

	w := bufio.NewWriter(stdin)
	r := bufio.NewScanner(stdout)
	r.Buffer(make([]byte, 0, 1<<20), 4<<20)
	next := func(id int) mcpResp {
		t.Helper()
		if !r.Scan() {
			t.Fatalf("子进程无应答（id=%d）: %v", id, r.Err())
		}
		var resp mcpResp
		if err := json.Unmarshal(r.Bytes(), &resp); err != nil {
			t.Fatalf("应答行非法（id=%d）: %v: %s", id, err, r.Bytes())
		}
		return resp
	}

	writeRPC(w, 1, "initialize", map[string]any{})
	resp := next(1)
	var initRes struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(resp.Result, &initRes); err != nil {
		t.Fatalf("initialize 应答解析失败: %v", err)
	}
	if initRes.ServerInfo.Name != "swarmmind" || initRes.ServerInfo.Version != "dev" {
		t.Fatalf("hub initialize 应答不符: %+v", initRes)
	}
	if !strings.Contains(initRes.Instructions, "swarmmind_capabilities") || !strings.Contains(initRes.Instructions, "tools/list") {
		t.Fatalf("hub instructions 应含能力发现指引: %q", initRes.Instructions)
	}

	// tools/list：24 工具且含 swarmmind_capabilities（W02-02 注册+装配）
	writeRPC(w, 2, "tools/list", map[string]any{})
	resp = next(2)
	var listRes struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listRes); err != nil {
		t.Fatalf("tools/list 应答解析失败: %v", err)
	}
	if len(listRes.Tools) != 24 {
		t.Fatalf("hub 模式应 24 工具, got %d", len(listRes.Tools))
	}
	hasCap := false
	for _, td := range listRes.Tools {
		if td.Name == "swarmmind_capabilities" {
			hasCap = true
		}
	}
	if !hasCap {
		t.Fatalf("hub tools/list 应含 swarmmind_capabilities")
	}

	// tools/call：真实能力标记（版本注入实查 + 核心能力 true）
	writeRPC(w, 3, "tools/call", map[string]any{"name": "swarmmind_capabilities", "arguments": map[string]any{}})
	resp = next(3)
	if !bytes.Contains(resp.Result, []byte("服务端：hub（版本 stdio-cap-1）")) ||
		!bytes.Contains(resp.Result, []byte("持久会话：可用")) ||
		!bytes.Contains(resp.Result, []byte("不替代 tools/list")) {
		t.Fatalf("tools/call capabilities 应答不符: %s", resp.Result)
	}
	// 空间支持投影（W07-01）：中心未装配如实 + 隐私边界随行人话（真端到端链）
	if !bytes.Contains(resp.Result, []byte("空间协作：中心服务未装配")) ||
		!bytes.Contains(resp.Result, []byte("不表示任何一端私有会话历史可读")) {
		t.Fatalf("capabilities 应答缺空间投影/隐私边界行: %s", resp.Result)
	}
	if bytes.Contains(resp.Result, []byte(pat)) {
		t.Fatalf("stdio 应答不得含 PAT 明文: %s", resp.Result)
	}

	// 优雅收尾：关 stdin → 循环自然退出
	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("子进程未随 stdin 关闭退出")
	}
}
