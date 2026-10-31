package main

// node_spaces_test.go node 空间六工具实测（W04-02，SPEC 六.六豁免⑯收窄兑现）：
// 进程内 backend 打真 localapi（Collab 全装配）走完整外部管家链（创建 external 待
// 领取 → 领取 → 配置成员 → ready/start → 派发（成员未绑线程如实拒绝）+ 主人专属
// 动作显式拒收 + 消息只读增量）；真二进制 stdio 覆盖 tools/list 含空间六工具与
// tools/call 实调。绝不真实模型/网络。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	localapi "github.com/mtl802/swarmmind/internal/node/localapi"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newSpaceNode 起真 localapi（Collab + Service 全装配，与 cmd/node 同源）——空间
// 工具链的进程内实测面。
func newSpaceNode(t *testing.T) *httptest.Server {
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
	store, err := collab.Open(dataDir, collab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:        led,
		Runners:       map[string]svcpkg.Runner{"echo": okRunner{}},
		ArtifactRoots: map[string]string{"echo": workdir},
		NodeID:        "nodeA",
		Collab:        store,
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(localapi.New(localapi.Config{
		NodeID:     "nodeA",
		Mode:       localapi.ModeStandalone,
		Service:    svc,
		Ledger:     led,
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  localapi.NewMcpAccessMemory(true),
		Collab:     store,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestNodeSpaceToolsEndToEnd 空间六工具全链（进程内，真 localapi + 真 collab）：
// N10 顺序（创建 external 待领取→未领取前 configure 拒→领取→配置→就绪→启动→派发）
// + 豁免保留面（主人专属动作显式拒收）+ N01（消息零唤醒）+ N02（同 ID 重放）。
func TestNodeSpaceToolsEndToEnd(t *testing.T) {
	srv := newSpaceNode(t)
	bd := newNodeBackendFor(srv.URL)

	// 1. 创建 external 待领取空间（成员可空）。
	text, isErr := callText(t, bd, "swarmmind_space_create", map[string]any{
		"goal": "交付 v3 空间", "mode": "external", "request_id": "sp-c1",
	})
	if isErr || !strings.Contains(text, "空间已创建") || !strings.Contains(text, "pending_leader") {
		t.Fatalf("创建 external 应成功且标注待领取: err=%v text=%s", isErr, text)
	}
	spID := extractSpaceID(t, text)
	// 同 request_id 重放：幂等命中。
	text, isErr = callText(t, bd, "swarmmind_space_create", map[string]any{
		"goal": "交付 v3 空间", "mode": "external", "request_id": "sp-c1",
	})
	if isErr || !strings.Contains(text, "幂等命中") {
		t.Fatalf("同键重放应幂等命中: err=%v text=%s", isErr, text)
	}

	// 2. 列表与详情。
	text, isErr = callText(t, bd, "swarmmind_space_list", map[string]any{})
	if isErr || !strings.Contains(text, spID) || !strings.Contains(text, "待领取") {
		t.Fatalf("列表应含新空间: err=%v text=%s", isErr, text)
	}
	text, isErr = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "待领取") || !strings.Contains(text, "版本 1") {
		t.Fatalf("详情应展示待领取/版本: err=%v text=%s", isErr, text)
	}

	// 3. 未领取前 configure_members：403（无负责人非驱动者——N10 顺序断言）。
	got := errText(t, bd, "swarmmind_space_control", map[string]any{
		"space_id": spID, "action": "configure_members", "expected_version": 1,
		"members": []any{map[string]any{"name": "echo"}},
	})
	if !strings.Contains(got, "forbidden") {
		t.Fatalf("未领取 configure_members 应 403 forbidden（N10）: %s", got)
	}

	// 4. 领取（takeover CAS expected_term=0）→ 新任期可查。
	text, isErr = callText(t, bd, "swarmmind_space_control", map[string]any{
		"space_id": spID, "action": "takeover", "expected_version": 1, "expected_term": 0,
	})
	if isErr || !strings.Contains(text, "接管成功") || !strings.Contains(text, "任期 1") {
		t.Fatalf("领取应成功且新任期 1: err=%v text=%s", isErr, text)
	}
	text, _ = callText(t, bd, "swarmmind_space_status", map[string]any{"space_id": spID})
	if !strings.Contains(text, "external/prime 任期 1") {
		t.Fatalf("领取后详情应展示 external/prime 任期 1: %s", text)
	}

	// 5. 配置成员 → ready → start（驱动者形态携 term=1）。
	ctrl := func(action string, extra map[string]any) (string, bool) {
		args := map[string]any{"space_id": spID, "action": action, "term": 1}
		for k, v := range extra {
			args[k] = v
		}
		return callText(t, bd, "swarmmind_space_control", args)
	}
	text, isErr = ctrl("configure_members", map[string]any{
		"expected_version": 2, "request_id": "cm-1",
		"members": []any{map[string]any{"name": "echo", "role": "writer"}},
	})
	if isErr || !strings.Contains(text, "已执行") {
		t.Fatalf("configure_members 应成功: err=%v text=%s", isErr, text)
	}
	if text, isErr = ctrl("ready", map[string]any{"expected_version": 3}); isErr {
		t.Fatalf("ready 应成功: %s", text)
	}
	if text, isErr = ctrl("start", map[string]any{"expected_version": 4}); isErr || !strings.Contains(text, "running") {
		t.Fatalf("start 应成功进运行: err=%v text=%s", isErr, text)
	}

	// 6. 派发（成员未绑线程——如实拒绝，绝不静默投递）。
	errText1 := errText(t, bd, "swarmmind_space_dispatch", map[string]any{
		"space_id": spID, "member": "echo", "expected_version": 5, "term": 1, "note": "干活",
	})
	if !strings.Contains(errText1, "未绑定原生会话") {
		t.Fatalf("未绑线程派发应显式拒绝: %s", errText1)
	}

	// 7. 主人专属动作显式拒收（豁免⑯收窄保留面——工具面不受理）。
	for _, act := range []string{"set_budget", "policy", "finalize", "archive"} {
		got := errText(t, bd, "swarmmind_space_control", map[string]any{
			"space_id": spID, "action": act, "expected_version": 5,
		})
		if !strings.Contains(got, "主人专属") {
			t.Fatalf("control %s 应被工具面显式拒收（主人专属）: %s", act, got)
		}
	}

	// 8. 消息零唤醒：读空态 → 主人发言面外（编排主体不指名成员 403）→ 指名成员
	// 发言 → 增量读可见（N01：全过程零执行——echo runner 从未被派发，账本空）。
	text, isErr = callText(t, bd, "swarmmind_space_messages", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "无新消息") {
		t.Fatalf("空消息读应如实: err=%v text=%s", isErr, text)
	}
	// 编排主体直连 REST 不指名成员 → 403（不能冒充主人发言）。
	code, _ := spaceREST(t, srv, "tok-prime", "POST", "/api/v1/spaces/"+spID+"/messages",
		`{"text":"冒充主人","expected_version":5}`)
	if code != 403 {
		t.Fatalf("编排主体不指名成员发言应 403, got %d", code)
	}
	// 指名成员发言（代转）→ 201；增量读可见。
	code, body := spaceREST(t, srv, "tok-prime", "POST", "/api/v1/spaces/"+spID+"/messages",
		`{"text":"管家代转的讨论","member":"echo","expected_version":5,"request_id":"m1"}`)
	if code != 201 {
		t.Fatalf("指名成员发言应 201, got %d: %s", code, body)
	}
	text, isErr = callText(t, bd, "swarmmind_space_messages", map[string]any{"space_id": spID})
	if isErr || !strings.Contains(text, "#1") || !strings.Contains(text, "成员 echo") || !strings.Contains(text, "管家代转") {
		t.Fatalf("增量读应含消息: err=%v text=%s", isErr, text)
	}
	// 同 request_id 重放：200 dup 不重复计数。
	code, _ = spaceREST(t, srv, "tok-prime", "POST", "/api/v1/spaces/"+spID+"/messages",
		`{"text":"管家代转的讨论","member":"echo","expected_version":5,"request_id":"m1"}`)
	if code != 200 {
		t.Fatalf("消息重放应 200 dup, got %d", code)
	}
	// N01 零执行佐证：账本无任何任务（消息/游标不触发执行）。
	if n := ledgerTaskCount(t, srv); n != 0 {
		t.Fatalf("消息与游标不得触发执行（账本应空）: %d", n)
	}

	// 9. 派发重放幂等（N02）的全链断言在 service/localapi 层（w04_02 文件，
	// 需会话内核夹具）；此处锁工具面的幂等回执文本已在步骤 1 覆盖（创建重放）。
}

// errText 执行一次预期失败的调用并取错误文本。
func errText(t *testing.T, bd backend, name string, args map[string]any) string {
	t.Helper()
	res, rpcErr := bd.call(name, args)
	if rpcErr != nil {
		return "rpc:" + rpcErr.Message
	}
	if m, ok := res["isError"].(bool); ok && m {
		if arr, ok := res["content"].([]any); ok && len(arr) > 0 {
			if item, ok := arr[0].(map[string]any); ok {
				if s, ok := item["text"].(string); ok {
					return s
				}
			}
		}
	}
	return fmt.Sprintf("非错误应答: %v", res)
}

// extractSpaceID 从创建输出取空间 ID（sp- 前缀）。
func extractSpaceID(t *testing.T, text string) string {
	t.Helper()
	i := strings.Index(text, "sp-")
	if i < 0 {
		t.Fatalf("输出无空间 ID: %s", text)
	}
	rest := text[i:]
	j := strings.IndexAny(rest, " \n：:")
	if j < 0 {
		j = len(rest)
	}
	return rest[:j]
}

// spaceREST 直发 REST（消息面主体矩阵断言用）。
func spaceREST(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ledgerTaskCount 账本任务总数（零执行佐证）。
func ledgerTaskCount(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	code, body := spaceREST(t, srv, "tok-prime", "GET", "/api/v1/local/tasks", "")
	if code != 200 {
		t.Fatalf("本地任务列表应 200, got %d: %s", code, body)
	}
	var out struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	return out.Total
}

// TestSubprocessNodeStdioSpaces 真二进制 stdio（node 模式，Collab 全装配 localapi）：
// tools/list 含空间六工具；tools/call 实调 create/list 与主人专属拒收（豁免收窄
// 的 stdio 实测承诺）。
func TestSubprocessNodeStdioSpaces(t *testing.T) {
	bin := buildMCPBinary(t)
	srv := newSpaceNode(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"SWARM_TARGET=node",
		"SWARM_NODE_URL="+srv.URL,
		"SWARM_NODE_PRIME_TOKEN=tok-prime",
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
	writeRPC(w, 1, "initialize", map[string]any{})
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
	next(1)
	writeRPC(w, 2, "tools/list", map[string]any{})
	resp := next(2)
	var listRes struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listRes); err != nil {
		t.Fatalf("tools/list 解析失败: %v", err)
	}
	want := map[string]bool{
		"swarmmind_space_create": false, "swarmmind_space_list": false, "swarmmind_space_status": false,
		"swarmmind_space_dispatch": false, "swarmmind_space_control": false, "swarmmind_space_messages": false,
	}
	for _, td := range listRes.Tools {
		if _, ok := want[td.Name]; ok {
			want[td.Name] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Fatalf("tools/list 缺空间工具 %s", name)
		}
	}
	call := func(id int, name string, args map[string]any) string {
		t.Helper()
		writeRPC(w, id, "tools/call", map[string]any{"name": name, "arguments": args})
		resp := next(id)
		var out struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(resp.Result, &out); err != nil {
			t.Fatalf("tools/call 应答解析失败: %v: %s", err, resp.Result)
		}
		if len(out.Content) == 0 {
			t.Fatalf("tools/call 无内容: %s", resp.Result)
		}
		return out.Content[0].Text
	}
	// 实调创建（真 collab 落盘）+ 列表可见。
	text := call(3, "swarmmind_space_create", map[string]any{"goal": "stdio 空间", "mode": "external"})
	spID := extractSpaceID(t, text)
	text = call(4, "swarmmind_space_list", map[string]any{})
	if !strings.Contains(text, spID) {
		t.Fatalf("stdio 列表应含新建空间: %s", text)
	}
	// 主人专属动作：stdio 实调显式拒收（豁免保留面）。
	if got := call(5, "swarmmind_space_control", map[string]any{"space_id": spID, "action": "finalize", "expected_version": 1}); !strings.Contains(got, "主人专属") {
		t.Fatalf("stdio finalize 应拒收（主人专属）: %s", got)
	}
}
