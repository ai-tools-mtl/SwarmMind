// node_sessions_test.go node 会话四工具实测（W01-04，tests/list 同一交付——不只加
// 文件，装配必须实测；SPEC 六.六 同步开发规则）：nodeBackend × 真 localapi 全装配
// （真账本 + sessionx 内核 + sessionop + fake CLI），create→send→status 全链、
// pending 登记、request_id 幂等、队列第 17 条 409 人话、unverified 强制提醒、
// cancel/close 不在工具清单（豁免⑬）、hub/node 同词表锁定（sessionErrHints 单源）。
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/localapi"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

var sessionIDRe = regexp.MustCompile(`s-[0-9a-f]{32}`)

// buildSessionFakeCLI 编译 sessionx 假 CLI（同 internal/node/localapi
// sessions_agent_test 口径；本包目录起算相对路径）。
func buildSessionFakeCLI(t *testing.T) string {
	t.Helper()
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", path, "../../internal/node/sessionx/testdata/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	return path
}

// newLocalSessionNode 起带会话内核的真 localapi（W01-03 agent 面 + W01-02 sessionop
// 全装配，与 cmd/node 生产装配同源；McpAccess 显式启用——authOrchestrate 放行
// prime_token）。mode "long-resume"：首轮快速完成、续轮长跑 30s（队列用例）；
// roots 非空配置工作区授权（越界 403 用例）。返回 server、账本与会话工作目录。
func newLocalSessionNode(t *testing.T, mode string, roots ...string) (*httptest.Server, *ledger.Ledger, string) {
	t.Helper()
	fake := buildSessionFakeCLI(t)
	cwd := t.TempDir()
	profiles := []protocol.ExecProfile{{
		Name: "coder", Kind: protocol.AgentKindCLI, Command: fake,
		Workdir: cwd, TimeoutSec: 120, Exposed: true, PermissionMode: "workspace-write",
		SessionAdapter: protocol.SessionAdapterCodex, Description: "会话测试能力",
	}}
	dataDir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex, "FAKE_THREAD_ID=thr-mcp-1"}
	if mode == "long-resume" {
		env = append(env, "FAKE_ON_RESUME=1", "FAKE_SLEEP=30")
	}
	kern, err := sessionx.NewManager(dataDir, map[string]sessionx.AgentSpec{
		"coder": {
			Adapter: protocol.SessionAdapterCodex, Command: fake,
			PermissionMode: "workspace-write", TimeoutSec: 120, Env: env,
		},
	}, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配会话内核: %v", err)
	}
	ops, err := sessionop.New(dataDir, led, sessionop.Options{AllowedWorkspaceRoots: roots})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	bus := transport.NewBus()
	svc := svcpkg.New(svcpkg.Config{
		Ledger: led, Runners: map[string]svcpkg.Runner{},
		ArtifactRoots: map[string]string{"coder": cwd}, Sessions: kern, SessionOps: ops,
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	state, err := localapi.OpenAgentState(dataDir, profiles)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(localapi.New(localapi.Config{
		NodeID: "nodeA", Mode: localapi.ModeStandalone, Service: svc, Agents: state,
		Ledger: led, LocalToken: "tok-local", PrimeToken: "tok-prime", PrimeAgent: "coder",
		McpAccess: localapi.NewMcpAccessMemory(true), // W02-01 门：编排授权启用（任务书要求启用态）
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, led, cwd
}

// cancelTurnsViaMaster 通过主人专属 REST 取消在途轮（测试清场用——同时反证豁免⑬：
// 工具面无 cancel，取消只能走主人 local_token 通道）。
func cancelTurnsViaMaster(srv *httptest.Server, sid string, upto int64) {
	cl := &http.Client{Timeout: 60 * time.Second}
	for seq := int64(1); seq <= upto; seq++ {
		_, _, _ = httpCall(srv.URL, "tok-local", cl, "POST",
			fmt.Sprintf("/api/v1/sessions/%s/turns/%d/cancel", sid, seq), map[string]any{})
	}
}

// closeMasterWithRetry 主人面关闭（测试夹具动作）：CloseGuard 在途防护存在「内核
// 轮终已投影、账本终态落盘在途」的窄窗口（内核发布先于账本 MarkFinished，全库
// 并行测试负载下窗口可观，实测踩中）——close_in_flight 时短暂重试至窗口收束。
// 已知服务端实况：主人旧面 writeLocalSessionErr 未收录 close_in_flight 落 500
// （RFC §7 定 409，localapi 面本批禁区不改）——重试按错误体判定，不依赖状态码。
func closeMasterWithRetry(t *testing.T, srv *httptest.Server, sid string) {
	t.Helper()
	cl := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, data, err := httpCall(srv.URL, "tok-local", cl, "POST", "/api/v1/local/sessions/"+sid+"/close", map[string]any{})
		if err == nil && code == http.StatusOK {
			return
		}
		if err != nil || !strings.Contains(string(data), "close_in_flight") {
			t.Fatalf("主人面 close 应 200: %d %s %v", code, data, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("主人面 close 5s 内未收束（close_in_flight 持续）: %s", data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pollSessionStatus 轮询 status 工具直至满足谓词（投递链异步）。
func pollSessionStatus(t *testing.T, bd backend, sid string, want []string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var text string
	for {
		text, _ = callText(t, bd, "swarmmind_session_status", map[string]any{"session_id": sid})
		ok := true
		for _, w := range want {
			if !strings.Contains(text, w) {
				ok = false
				break
			}
		}
		if ok {
			return text
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮询超时（未同时含 %v）: %s", want, text)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNodeBackendSessionToolsEndToEnd 全链（真 localapi + 真会话内核）：清单含四
// 工具且无 cancel/close（豁免⑬）→ create 登记（pending 态）→ 同键幂等 → list/
// status 可查登记事实（request_id/title）→ send 受理（首轮）→ 轮终 unverified
// 强制提醒 → list 活跃 → 受理重放 dup → 错误指引矩阵（未知会话/未知能力/
// permission_mode 拒绝/node_id 非本机拒绝）→ 主人面关闭后续轮 session_closed。
func TestNodeBackendSessionToolsEndToEnd(t *testing.T) {
	srv, _, cwd := newLocalSessionNode(t, "")
	bd := newNodeBackendFor(srv.URL)

	// 工具清单：node 十件套（六件套 + 会话四工具）；无取消/关闭类工具
	names := []string{}
	for _, td := range bd.tools() {
		names = append(names, td.Name)
	}
	want := []string{"swarmmind_nodes", "swarmmind_agents", "swarmmind_submit",
		"swarmmind_status", "swarmmind_wait", "swarmmind_artifact",
		"swarmmind_session_create", "swarmmind_session_send",
		"swarmmind_session_list", "swarmmind_session_status"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("node 工具清单不符: %v", names)
	}
	for _, td := range bd.tools() {
		if strings.HasPrefix(td.Name, "swarmmind_session_") &&
			(strings.HasSuffix(td.Name, "_cancel") || strings.HasSuffix(td.Name, "_close")) {
			t.Fatalf("node 会话工具组不应含取消/关闭（豁免⑬）: %s", td.Name)
		}
	}

	// ① create：登记不投轮（201），pending 态 + 指引 + request_id 回显
	text, isErr := callText(t, bd, "swarmmind_session_create", map[string]any{
		"agent": "coder", "cwd": cwd, "title": "mcp 全链", "request_id": "mcp-sess-1",
	})
	if isErr || !strings.Contains(text, "会话已创建") || !strings.Contains(text, "登记中（首轮未发布）") ||
		!strings.Contains(text, "首轮用 swarmmind_session_send 提交") || !strings.Contains(text, "mcp-sess-1") {
		t.Fatalf("create 应答不符（isErr=%v）: %s", isErr, text)
	}
	sid := sessionIDRe.FindString(text)
	if sid == "" {
		t.Fatalf("create 应答无平台会话 ID: %s", text)
	}

	// ② 同键同文重放：幂等返原会话
	text, isErr = callText(t, bd, "swarmmind_session_create", map[string]any{
		"agent": "coder", "cwd": cwd, "title": "mcp 全链", "request_id": "mcp-sess-1",
	})
	if isErr || !strings.Contains(text, sid) || !strings.Contains(text, "幂等命中") {
		t.Fatalf("create 重放应幂等命中原会话 %s: %s", sid, text)
	}

	// ③ pending 可查：创建后不 send，list 见登记项（附 request_id/title）
	text, isErr = callText(t, bd, "swarmmind_session_list", nil)
	if isErr || !strings.Contains(text, sid) || !strings.Contains(text, "登记中（首轮未发布）") ||
		!strings.Contains(text, "request_id mcp-sess-1") || !strings.Contains(text, "标题《mcp 全链》") {
		t.Fatalf("list 应见 pending 登记项（isErr=%v）: %s", isErr, text)
	}

	// ④ status：登记事实投影（平台 ID 已持久可查）
	text, _ = callText(t, bd, "swarmmind_session_status", map[string]any{"session_id": sid})
	if !strings.Contains(text, "登记中（首轮未发布）") || !strings.Contains(text, "mcp-sess-1") {
		t.Fatalf("status 应投影登记事实: %s", text)
	}

	// ⑤ send：首轮受理（202 形态——受理即回，不阻塞等待）
	text, isErr = callText(t, bd, "swarmmind_session_send", map[string]any{
		"session_id": sid, "prompt": "第一轮", "request_id": "mcp-turn-1",
	})
	if isErr || !strings.Contains(text, "轮次已受理") || !strings.Contains(text, "#1") ||
		!strings.Contains(text, "首轮") {
		t.Fatalf("send 应答不符（isErr=%v）: %s", isErr, text)
	}

	// ⑥ 轮终 unverified：内核发布 + 强制提醒（与 hub 同词表原句）
	text = pollSessionStatus(t, bd, sid, []string{"活跃", "已完成（工作结果未经证实）",
		"禁止基于未经核对的轮次下结论"}, 10*time.Second)
	if !strings.Contains(text, "轮 #1") {
		t.Fatalf("status 应含轮次投影: %s", text)
	}

	// ⑦ list：活跃 + 最近轮 unverified 词表
	text, _ = callText(t, bd, "swarmmind_session_list", nil)
	if !strings.Contains(text, sid) || !strings.Contains(text, "活跃") ||
		!strings.Contains(text, "已完成（工作结果未经证实）") {
		t.Fatalf("list 应见活跃会话与最近轮状态: %s", text)
	}

	// ⑧ 受理重放：同 request_id 同文 → dup 人话（不重复执行）
	text, isErr = callText(t, bd, "swarmmind_session_send", map[string]any{
		"session_id": sid, "prompt": "第一轮", "request_id": "mcp-turn-1",
	})
	if isErr || !strings.Contains(text, "幂等命中") || !strings.Contains(text, "未重复投递") {
		t.Fatalf("受理重放应 dup 人话（isErr=%v）: %s", isErr, text)
	}

	// 错误指引矩阵（isError + 同词表人话）
	errCases := []struct {
		name string
		call func() (string, bool)
		want []string
	}{
		{"未知会话", func() (string, bool) {
			return callText(t, bd, "swarmmind_session_status", map[string]any{"session_id": "s-" + strings.Repeat("0", 32)})
		}, []string{"会话不存在"}},
		{"未知能力", func() (string, bool) {
			return callText(t, bd, "swarmmind_session_create", map[string]any{"agent": "ghost", "cwd": cwd})
		}, []string{"未声明会话能力"}},
		{"权限断言（hub-only）", func() (string, bool) {
			return callText(t, bd, "swarmmind_session_create", map[string]any{"agent": "coder", "cwd": cwd, "permission_mode": "workspace-write"})
		}, []string{"permission_mode", "仅 hub 模式可用"}},
		{"node_id 非本机", func() (string, bool) {
			return callText(t, bd, "swarmmind_session_create", map[string]any{"agent": "coder", "cwd": cwd, "node_id": "ghost-node"})
		}, []string{"不是本机节点", "hub 模式"}},
	}
	for _, tc := range errCases {
		text, isErr := tc.call()
		for _, w := range tc.want {
			if !isErr || !strings.Contains(text, w) {
				t.Fatalf("%s 应 isError 且含 %q: %s (isErr=%v)", tc.name, w, text, isErr)
			}
		}
	}

	// 主人面关闭后续轮：409 session_closed → 同词表「会话已关闭」指引
	closeMasterWithRetry(t, srv, sid)
	text, isErr = callText(t, bd, "swarmmind_session_send", map[string]any{"session_id": sid, "prompt": "x"})
	if !isErr || !strings.Contains(text, "会话已关闭") || !strings.Contains(text, "swarmmind_session_create 新建会话") {
		t.Fatalf("关闭后续轮应 session_closed 人话指引（isErr=%v）: %s", isErr, text)
	}
}

// TestNodeBackendSessionWorkspaceDenied 工作区授权（W01-03 RFC §7 workspace_denied
// 403）：越界 cwd → 同词表人话指路（授权根配置指路）；根内 cwd 放行。
func TestNodeBackendSessionWorkspaceDenied(t *testing.T) {
	allowed := t.TempDir()
	srv, _, _ := newLocalSessionNode(t, "", allowed)
	bd := newNodeBackendFor(srv.URL)

	text, isErr := callText(t, bd, "swarmmind_session_create", map[string]any{
		"agent": "coder", "cwd": "/definitely-not-allowed",
	})
	if !isErr || !strings.Contains(text, "会话工作目录越界") || !strings.Contains(text, "session_workspace_roots") {
		t.Fatalf("越界 cwd 应 403 人话指路（isErr=%v）: %s", isErr, text)
	}
	text, isErr = callText(t, bd, "swarmmind_session_create", map[string]any{
		"agent": "coder", "cwd": allowed,
	})
	if isErr || !strings.Contains(text, "会话已创建") {
		t.Fatalf("根内 cwd 应放行（isErr=%v）: %s", isErr, text)
	}
}

// TestNodeBackendSessionQueueFull 队列上限（AC01，RFC §4）：非终态受理轮达 16 后
// 第 17 条 409 turn_queue_full → 同词表人话指引；拒绝零副作用（高水位不推进）。
// long-resume：首轮快速发布，续轮全部长跑停留非终态。清场经主人专属 REST 取消
// （工具面无 cancel——豁免⑬）。
func TestNodeBackendSessionQueueFull(t *testing.T) {
	srv, led, cwd := newLocalSessionNode(t, "long-resume")
	bd := newNodeBackendFor(srv.URL)

	text, isErr := callText(t, bd, "swarmmind_session_create", map[string]any{"agent": "coder", "cwd": cwd})
	if isErr {
		t.Fatalf("create 不应失败: %s", text)
	}
	sid := sessionIDRe.FindString(text)

	// 首轮（create 轮）快速完成并发布内核映射
	callText(t, bd, "swarmmind_session_send", map[string]any{"session_id": sid, "prompt": "首轮"})
	pollSessionStatus(t, bd, sid, []string{"活跃"}, 10*time.Second)

	// 16 条长跑 send：非终态受理轮达上限
	for i := 0; i < 16; i++ {
		text, isErr = callText(t, bd, "swarmmind_session_send", map[string]any{
			"session_id": sid, "prompt": fmt.Sprintf("p%d", i),
		})
		if isErr || !strings.Contains(text, "轮次已受理") {
			t.Fatalf("第 %d 条应受理: %s", i+2, text)
		}
	}
	// 等全部落账（seq 2..17 全部非终态在途）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if led.SessionTurnHighWater(sid) >= 17 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hw := led.SessionTurnHighWater(sid); hw != 17 {
		t.Fatalf("高水位应 17, got %d", hw)
	}
	// 第 17 条：409 turn_queue_full → 同词表人话指引
	text, isErr = callText(t, bd, "swarmmind_session_send", map[string]any{
		"session_id": sid, "prompt": "第 17 条非终态",
	})
	if !isErr || !strings.Contains(text, "排队轮已达上限") || !strings.Contains(text, "拒绝受理第 17 条") {
		t.Fatalf("第 17 条应 409 turn_queue_full 人话（isErr=%v）: %s", isErr, text)
	}
	if hw := led.SessionTurnHighWater(sid); hw != 17 {
		t.Fatalf("拒绝应零副作用（高水位不推进）, got %d", hw)
	}
	cancelTurnsViaMaster(srv, sid, 17)
}

// TestSessionErrHintSharedVocabulary hub/node 会话错误同词表锁定（防双端漂移，
// W01-04）：两后端共用 sessionErrHints 单源实现——同一错误体在两端的提示仅差标注
// 前缀；RFC §7 映射表中工具面可达的错误码全员有人话（落 JSON 原话回退=未收录，
// 即失败）。
func TestSessionErrHintSharedVocabulary(t *testing.T) {
	body := []byte(`{"error":"session_closed","message":"已关闭的会话"}`)
	hub := sessionErrHint(409, body).Error()
	node := sessionErrHintAt("本机 node", 409, body).Error()
	if !strings.Contains(hub, "会话已关闭") || !strings.Contains(hub, "swarmmind_session_create 新建会话") {
		t.Fatalf("session_closed 人话漂移: %s", hub)
	}
	if node != strings.Replace(hub, "hub 409", "本机 node 409", 1) {
		t.Fatalf("node 提示应与 hub 同词表仅差前缀:\n hub=%s\nnode=%s", hub, node)
	}
	for _, code := range []string{"session_not_found", "session_closed", "session_unsupported",
		"binding_mismatch", "turn_seq_conflict", "turn_in_flight", "turn_queue_full",
		"request_conflict", "workspace_denied", "agent_not_found",
		"input_invalid", "invalid_request"} {
		b := []byte(fmt.Sprintf(`{"error":%q,"message":"服务端原话"}`, code))
		text := sessionErrHintAt("本机 node", 409, b).Error()
		if strings.Contains(text, `{"error"`) {
			t.Fatalf("错误码 %q 未入同词表（落 JSON 原话回退）: %s", code, text)
		}
	}
}
