package main

// MCP 集成测试（方案 3.4 node backend 真实实现，第三轮返工必须修 1）：
//   - 进程内：nodeBackend × 真 localapi（internal/node/localapi 全装配：真账本、
//     真服务（桩执行器）、AgentState、TaskLog、FetchArtifactLocal）——node 六工具
//     端到端、幂等重派、403 prime_required 原话透传、hub 工具清单回归；
//   - 子进程：go build 出真二进制，exec stdio 握手（initialize/tools/list/
//     tools/call 全走 backend 分发）+ SWARM_TARGET 非法值启动即退出。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/localapi"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// okRunner 恒完成任务执行器（提交即 completed，wait/状态断言不依赖真实进程）。
type okRunner struct{}

func (okRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	return protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		ExitCode:    0,
		FinalAnswer: "已处理：" + task.Input.Stdin,
		StdoutTail:  "ok",
	}
}

// newLocalNode 起真 localapi 全装配（与 cmd/node 装配同源：账本/服务/AgentState/
// TaskLog/ArtifactFetch）。返回 server 与账本、产物根目录；primeAgent 空 = 未钦定
// 总理（编排接口恒 403 prime_required，透传用例用）。
func newLocalNode(t *testing.T, primeAgent string) (*httptest.Server, *ledger.Ledger, string) {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	led, _, err := ledger.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })

	bus := transport.NewBus()
	svc := svcpkg.New(svcpkg.Config{
		Ledger:        led,
		Runners:       map[string]svcpkg.Runner{"echo": okRunner{}},
		ArtifactRoots: map[string]string{"echo": workdir},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)

	profiles := []protocol.ExecProfile{{
		Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir,
		TimeoutSec: 10, Exposed: true, Description: "回声测试",
	}}
	state, err := localapi.OpenAgentState(dataDir, profiles)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := localapi.OpenTaskLog(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(localapi.New(localapi.Config{
		NodeID:        "nodeA",
		Mode:          localapi.ModeStandalone,
		Service:       svc,
		Agents:        state,
		Ledger:        led,
		TaskLog:       tl,
		LocalToken:    "tok-local",
		PrimeToken:    "tok-prime",
		PrimeAgent:    primeAgent,
		ArtifactFetch: svc.FetchArtifactLocal,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, led, workdir
}

// newNodeBackendFor 对准给定 localapi 地址的 node 后端（安全审查修复 1 口径：
// 假件只配 prime 凭证——六工具单凭证，不再下发 SWARM_NODE_TOKEN）。
func newNodeBackendFor(url string) nodeBackend {
	return newNodeBackend(func(key string) string {
		switch key {
		case "SWARM_NODE_URL":
			return url
		case "SWARM_NODE_PRIME_TOKEN":
			return "tok-prime"
		}
		return ""
	})
}

// callText 执行一次工具调用并取 text（isError 由调用方按需断言）。
func callText(t *testing.T, bd backend, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, rpcErr := bd.call(name, args)
	if rpcErr != nil {
		t.Fatalf("%s 回协议错误 %v", name, rpcErr)
	}
	if res == nil {
		t.Fatalf("%s 空应答", name)
	}
	isErr, _ := res["isError"].(bool)
	switch c := res["content"].(type) {
	case []textContent: // 进程内调用：原生类型
		if len(c) == 0 {
			t.Fatalf("%s 应答无 content：%v", name, res)
		}
		return c[0].Text, isErr
	case []any: // 经 JSON 往返（如 stdio 循环）
		if len(c) == 0 {
			t.Fatalf("%s 应答无 content：%v", name, res)
		}
		first, _ := c[0].(map[string]any)
		text, _ := first["text"].(string)
		return text, isErr
	}
	t.Fatalf("%s 应答 content 类型不符：%v", name, res)
	return "", false
}

var taskIDRe = regexp.MustCompile(`t-[0-9a-f]{32}`)

// sha256Hex 测试产物登记用哈希。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestNodeBackendToolsEndToEnd node 六工具 × 真 localapi 端到端：清单裁剪、
// nodes/agents 只读、submit→wait→status 全链、artifact 本机直取、幂等重派。
func TestNodeBackendToolsEndToEnd(t *testing.T) {
	srv, led, workdir := newLocalNode(t, "echo")
	bd := newNodeBackendFor(srv.URL)

	// 工具清单：本机六件套（无 hub 专属工具），submit 描述明示接入即总理
	names := []string{}
	for _, td := range bd.tools() {
		names = append(names, td.Name)
	}
	want := []string{"swarmmind_nodes", "swarmmind_agents", "swarmmind_submit",
		"swarmmind_status", "swarmmind_wait", "swarmmind_artifact"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("node 工具清单不符: %v", names)
	}
	for _, td := range bd.tools() {
		if td.Name == "swarmmind_submit" && !strings.Contains(td.Description, "接入即统筹：本机编排") {
			t.Fatalf("submit 描述未明示「接入即统筹：本机编排」: %s", td.Description)
		}
	}

	// ① nodes：本机信息单条（形态/统筹/账本计数）
	text, isErr := callText(t, bd, "swarmmind_nodes", nil)
	if isErr || !strings.Contains(text, "nodeA") || !strings.Contains(text, "standalone") ||
		!strings.Contains(text, "本机统筹：echo") {
		t.Fatalf("nodes 应答不符（isErr=%v）: %s", isErr, text)
	}

	// ② agents：本机目录（描述与今日执行数）
	text, isErr = callText(t, bd, "swarmmind_agents", nil)
	if isErr || !strings.Contains(text, "echo") || !strings.Contains(text, "回声测试") {
		t.Fatalf("agents 应答不符（isErr=%v）: %s", isErr, text)
	}

	// ③ submit：target_node 自动补齐本机 → 入账并执行
	text, isErr = callText(t, bd, "swarmmind_submit", map[string]any{
		"agent": "echo", "stdin": "巡检磁盘", "idem_key": "mcp-it-1",
	})
	if isErr {
		t.Fatalf("submit 不应失败: %s", text)
	}
	taskID := taskIDRe.FindString(text)
	if taskID == "" {
		t.Fatalf("submit 应答无任务号: %s", text)
	}

	// 幂等重派：同键同内容 → dup 命中原任务，不产生新任务
	text, _ = callText(t, bd, "swarmmind_submit", map[string]any{
		"agent": "echo", "stdin": "巡检磁盘", "idem_key": "mcp-it-1",
	})
	if !strings.Contains(text, taskID) || !strings.Contains(text, "幂等命中") {
		t.Fatalf("幂等重派应命中原任务 %s: %s", taskID, text)
	}

	// 等 stub 执行器落终态（≤5s），随后 status 断言终态视图
	deadline := time.Now().Add(5 * time.Second)
	for {
		text, _ = callText(t, bd, "swarmmind_status", map[string]any{"task_id": taskID})
		if strings.Contains(text, "已完成") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 5s 内未到终态: %s", text)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(text, "已处理：巡检磁盘") {
		t.Fatalf("status 应带最终答复: %s", text)
	}

	// ⑤ wait：已终态 → 立即返回
	text, isErr = callText(t, bd, "swarmmind_wait", map[string]any{
		"task_id": taskID, "timeout_sec": 5, "interval_sec": 1,
	})
	if isErr || !strings.Contains(text, "已完成") || !strings.Contains(text, "已处理：巡检磁盘") {
		t.Fatalf("wait 应答不符（isErr=%v）: %s", isErr, text)
	}

	// ⑥ artifact：登记产物 + 盘上文件 → 本机直取内容
	artData := []byte("本机产物内容：standalone 直取不经 hub。")
	artDir := filepath.Join(workdir, "outputs")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artPath := filepath.Join(artDir, "report.txt")
	if err := os.WriteFile(artPath, artData, 0o644); err != nil {
		t.Fatal(err)
	}
	art := protocol.NewTaskID()
	if err := led.AcceptOrigin(protocol.Task{
		ID: art, TargetNode: "nodeA", TargetAgent: "echo", SubmittedBy: "it",
	}, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex(artData)
	if err := led.MarkFinished(art, protocol.TaskResult{Status: protocol.ResultCompleted,
		Artifacts: []protocol.Artifact{{Path: "outputs/report.txt", Sha256: sum, Size: int64(len(artData))}}}); err != nil {
		t.Fatal(err)
	}
	text, isErr = callText(t, bd, "swarmmind_artifact", map[string]any{"task_id": art})
	if isErr || !strings.Contains(text, "本机产物内容：standalone 直取不经 hub。") {
		t.Fatalf("artifact 应答不符（isErr=%v）: %s", isErr, text)
	}

	// 边界外一例：未知工具 → 协议错误 -32602（与 hub 同型）
	if _, rpcErr := bd.call("swarmmind_group_list", nil); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("node 模式未知工具应 -32602: %+v", rpcErr)
	}
}

// TestNodeBackendPrimeRequiredPassthrough 403 prime_required 透传：服务端原话进
// isError 消息并附检查总理配置提示；凭证缺失给客户端侧可行动错误。
func TestNodeBackendPrimeRequiredPassthrough(t *testing.T) {
	srv, _, _ := newLocalNode(t, "") // 未钦定总理 → 编排接口恒 403
	bd := newNodeBackendFor(srv.URL)

	text, isErr := callText(t, bd, "swarmmind_submit", map[string]any{"agent": "echo", "stdin": "hi"})
	if !isErr {
		t.Fatalf("未钦定总理时 submit 应 isError: %s", text)
	}
	for _, want := range []string{"prime_required", "尚未钦定统筹", "请检查统筹配置"} {
		if !strings.Contains(text, want) {
			t.Fatalf("403 透传缺 %q: %s", want, text)
		}
	}

	// 客户端侧：未配置 SWARM_NODE_PRIME_TOKEN → 明确指出缺哪个环境变量
	//（修复 1 口径：node 后端单凭证，prime 缺失即一切工具不可用）
	noPrime := newNodeBackend(func(key string) string {
		if key == "SWARM_NODE_URL" {
			return srv.URL
		}
		return ""
	})
	text, isErr = callText(t, noPrime, "swarmmind_submit", map[string]any{"agent": "echo"})
	if !isErr || !strings.Contains(text, "SWARM_NODE_PRIME_TOKEN") {
		t.Fatalf("缺 prime 凭证应提示 SWARM_NODE_PRIME_TOKEN（isErr=%v）: %s", isErr, text)
	}
}

// TestNodeBackendHTTPErrorIsError 功能性修复 8（2026-10-22）：node 后端对 HTTP
// 4xx/5xx（含 404）一律 isError=true、message 透传服务端原话；swarmmind_wait 遇
// HTTP 错误（非任务终态）立即返回，不当终态结束条件、不空转轮询到超时。
func TestNodeBackendHTTPErrorIsError(t *testing.T) {
	srv, _, _ := newLocalNode(t, "echo")
	bd := newNodeBackendFor(srv.URL)
	ghost := "t-" + strings.Repeat("0", 32)

	// status 查不存在任务：localapi 404 task_not_found → isError + 服务端原话
	text, isErr := callText(t, bd, "swarmmind_status", map[string]any{"task_id": ghost})
	if !isErr {
		t.Fatalf("404 应 isError=true: %s", text)
	}
	if !strings.Contains(text, "404") || !strings.Contains(text, "任务不存在") {
		t.Fatalf("404 服务端原话透传缺关键词: %s", text)
	}

	// wait 遇 404：立即返回（<2s，timeout 给 30s 若误当非终态会空转到超时）
	start := time.Now()
	text, isErr = callText(t, bd, "swarmmind_wait", map[string]any{
		"task_id": ghost, "timeout_sec": 30, "interval_sec": 1,
	})
	if !isErr {
		t.Fatalf("wait 遇 404 应 isError=true: %s", text)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("wait 遇 HTTP 错误应立即返回, 耗时 %v: %s", el, text)
	}
	if !strings.Contains(text, "404") {
		t.Fatalf("wait 错误应透传 404 原话: %s", text)
	}

	// node 不可达（网络层）：同样 isError=true 立即返回
	srv.Close()
	text, isErr = callText(t, bd, "swarmmind_status", map[string]any{"task_id": ghost})
	if !isErr || !strings.Contains(text, "本机 node 不可达") {
		t.Fatalf("不可达应 isError=true 且注明不可达（isErr=%v）: %s", isErr, text)
	}
}

// TestNodeBackendReadOnlyPrimeToken 只读工具单凭证（安全审查修复 1 口径）：
// 仅配 SWARM_NODE_PRIME_TOKEN 时只读工具（nodes/agents/status/wait/artifact）
// 照常可用——localapi authRead 双收 prime_token，prime 认证级 ⊇ 只读。
func TestNodeBackendReadOnlyPrimeToken(t *testing.T) {
	srv, _, _ := newLocalNode(t, "echo")
	bd := newNodeBackend(func(key string) string {
		switch key {
		case "SWARM_NODE_URL":
			return srv.URL
		case "SWARM_NODE_PRIME_TOKEN":
			return "tok-prime"
		}
		return ""
	})
	text, isErr := callText(t, bd, "swarmmind_nodes", nil)
	if isErr || !strings.Contains(text, "nodeA") {
		t.Fatalf("prime_token 单凭证只读失败（isErr=%v）: %s", isErr, text)
	}
}

// TestHubToolListRegression hub 模式回归：19 工具清单与顺序不变（P4⑤ 新增
// swarmmind_tasks，位于 artifact 之后、decide 之前；P5.1 D1 新增
// swarmmind_dispatch_suggest，位于 decide 之后，hub 专属；P5 W1 新增工作单三工具
// swarmmind_work_create/get/list，位于群工具之后；P5 W2 新增
// swarmmind_work_takeover，位于工作单组末尾，hub 模式专属；P5 W3 新增
// swarmmind_work_collect，位于 takeover 之后）；分发委托
// callTool（未知工具协议错误、未配置 hub 的人话提示均保持原行为）。
func TestHubToolListRegression(t *testing.T) {
	want := []string{"swarmmind_nodes", "swarmmind_agents", "swarmmind_submit",
		"swarmmind_status", "swarmmind_wait", "swarmmind_artifact", "swarmmind_tasks",
		"swarmmind_decide", "swarmmind_dispatch_suggest",
		"swarmmind_group_create", "swarmmind_group_list", "swarmmind_group_say",
		"swarmmind_group_messages", "swarmmind_group_close",
		"swarmmind_work_create", "swarmmind_work_get", "swarmmind_work_list",
		"swarmmind_work_takeover", "swarmmind_work_collect"}
	got := []string{}
	for _, td := range (hubBackend{}).tools() {
		got = append(got, td.Name)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("hub 工具清单回归失败:\n got %v\nwant %v", got, want)
	}
	// 未配置 hub：人话提示（原行为，errResult 而非协议错误）
	res, rpcErr := (hubBackend{}).call("swarmmind_nodes", nil)
	if rpcErr != nil {
		t.Fatalf("未配置 hub 不应回协议错误: %+v", rpcErr)
	}
	text, _ := callText(t, hubBackend{}, "swarmmind_nodes", nil)
	if res == nil || !strings.Contains(text, "SWARM_HUB_URL") {
		t.Fatalf("未配置 hub 应提示 SWARM_HUB_URL: %s", text)
	}
	// 配好全局后分发委托 callTool：未知工具 -32602（原行为）
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = "http://hub-regression.test", "tok"
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })
	if _, rpcErr := (hubBackend{}).call("swarmmind_nothing", nil); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("hub 未知工具应 -32602: %+v", rpcErr)
	}
	// initialize 应答零变化（serverInfo 名仍为 swarmmind）
	init := (hubBackend{}).initResult()
	si, _ := init["serverInfo"].(map[string]any)
	if si["name"] != "swarmmind" || init["protocolVersion"] != "2024-11-05" {
		t.Fatalf("hub initialize 应答零变化被破坏: %v", init)
	}
}

// TestInitializeServerInfoVersionLock serverInfo.version 变量锁（codex 审查建议修，
// TASK-maturity-version-var 回归）：hub/node 两 backend 的 initialize 应答
// serverInfo.version 必须取自包级 version 变量（--version 与 -X main.version 注入
// 同源），禁止回退硬编码字面量（原始缺陷：两处硬编码 "0.1.1"）。
func TestInitializeServerInfoVersionLock(t *testing.T) {
	const ver = "mat-test-001"
	old := version
	version = ver
	t.Cleanup(func() { version = old })

	wantName := map[string]string{"hub": "swarmmind", "node": "swarmmind-node"}
	backends := map[string]backend{"hub": hubBackend{}, "node": nodeBackend{}}
	for name, bd := range backends {
		init := bd.initResult()
		si, ok := init["serverInfo"].(map[string]any)
		if !ok {
			t.Fatalf("%s initialize 应答缺 serverInfo: %v", name, init)
		}
		if si["name"] != wantName[name] {
			t.Fatalf("%s serverInfo.name 应为 %q, got %v", name, wantName[name], si["name"])
		}
		if si["version"] != ver {
			t.Fatalf("%s serverInfo.version 应随包级 version 变量（注入 %q）, got %v", name, ver, si["version"])
		}
	}
}

/* ---------- 子进程级：真二进制 stdio 握手 ---------- */

// buildMCPBinary 编译当前包为可执行文件（子进程测试用）；可变长传 -ldflags 参数
//（版本注入链测试用）。
func buildMCPBinary(t *testing.T, ldflags ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "swarmmind-mcp")
	args := []string{"build"}
	if len(ldflags) > 0 {
		args = append(args, "-ldflags", strings.Join(ldflags, " "))
	}
	args = append(args, "-o", bin, ".")
	cmd := exec.Command("go", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build 失败: %v\n%s", err, out)
	}
	return bin
}

// mcpResp JSON-RPC 应答行。
type mcpResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeRPC 写一行 JSON-RPC 请求。
func writeRPC(w *bufio.Writer, id int, method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	w.WriteString(string(b) + "\n")
	w.Flush()
}

// TestSubprocessNodeStdioHandshake 真子进程（go build 出的二进制）stdio 握手：
// initialize → tools/list → tools/call 全走 backend 分发（node 模式，真 localapi）。
func TestSubprocessNodeStdioHandshake(t *testing.T) {
	bin := buildMCPBinary(t)
	srv, _, _ := newLocalNode(t, "echo")

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"SWARM_TARGET=node",
		"SWARM_NODE_URL="+srv.URL,
		"SWARM_NODE_PRIME_TOKEN=tok-prime", // 修复 1 口径：单凭证，不配 SWARM_NODE_TOKEN
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

	// initialize：协议版本 + node instructions（接入即总理）
	writeRPC(w, 1, "initialize", map[string]any{})
	resp := next(1)
	var initRes struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(resp.Result, &initRes); err != nil {
		t.Fatalf("initialize 应答解析失败: %v: %s", err, resp.Result)
	}
	if initRes.ProtocolVersion != "2024-11-05" || initRes.ServerInfo.Name != "swarmmind-node" ||
		!strings.Contains(initRes.Instructions, "接入即统筹：本机编排") {
		t.Fatalf("initialize 应答不符: %+v", initRes)
	}

	// tools/list：node 模式六件套
	writeRPC(w, 2, "tools/list", map[string]any{})
	resp = next(2)
	var listRes struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listRes); err != nil {
		t.Fatalf("tools/list 应答解析失败: %v", err)
	}
	if len(listRes.Tools) != 6 {
		t.Fatalf("node 模式应 6 工具, got %d", len(listRes.Tools))
	}

	// tools/call：nodes 打到真 localapi
	writeRPC(w, 3, "tools/call", map[string]any{"name": "swarmmind_nodes", "arguments": map[string]any{}})
	resp = next(3)
	if !bytes.Contains(resp.Result, []byte("nodeA")) || !bytes.Contains(resp.Result, []byte("本机统筹：echo")) {
		t.Fatalf("tools/call nodes 应答不符: %s", resp.Result)
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

// TestSubprocessInitializeVersionInjection -X main.version 注入链端到端锁
// （TASK-maturity-version-var 回归）：go build -ldflags "-X main.version=…" 后
// initialize 应答 serverInfo.version 必须为注入值——锁 Makefile LDFLAGS 真实生效
//（原始缺陷形态：main 包无 version 变量，链接器静默忽略 -X 无任何报错）。
func TestSubprocessInitializeVersionInjection(t *testing.T) {
	const ver = "mat-test-002"
	bin := buildMCPBinary(t, "-X main.version="+ver)

	cmd := exec.Command(bin)
	cmd.Env = os.Environ() // 默认 hub 模式：initialize 应答不触网，无需任何配置
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
	writeRPC(w, 1, "initialize", map[string]any{})
	if !r.Scan() {
		t.Fatalf("子进程无 initialize 应答: %v", r.Err())
	}
	var resp mcpResp
	if err := json.Unmarshal(r.Bytes(), &resp); err != nil {
		t.Fatalf("initialize 应答行非法: %v: %s", err, r.Bytes())
	}
	var initRes struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &initRes); err != nil {
		t.Fatalf("initialize 应答解析失败: %v: %s", err, resp.Result)
	}
	if initRes.ServerInfo.Version != ver {
		t.Fatalf("serverInfo.version 应为 -X 注入值 %q, got %q", ver, initRes.ServerInfo.Version)
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

// TestSubprocessInvalidTarget SWARM_TARGET 非法值：启动即报错退出并列出合法值。
func TestSubprocessInvalidTarget(t *testing.T) {
	bin := buildMCPBinary(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SWARM_TARGET=cluster")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("非法 SWARM_TARGET 应非零退出")
	}
	if out := stderr.String(); !strings.Contains(out, "非法") || !strings.Contains(out, "hub") || !strings.Contains(out, "node") {
		t.Fatalf("报错应列出合法值: %s", out)
	}
}
