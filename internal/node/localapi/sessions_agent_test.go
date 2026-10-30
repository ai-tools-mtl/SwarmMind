package localapi

// sessions_agent_test.go agent 会话面契约（W01-03，docs/tasks/v3/W01-03-rfc.md）：
// 鉴权矩阵（authOrchestrate——prime_token 需显式 mcpaccess enabled，local_token 恒
// 放行）、创建登记 201 不执行模型、pending 可查（平台 ID+受理态）、request_id 幂
// 等三态、队列上限第 17 条 409、工作区授权 403/放行、取消回执（queued/executed/
// 已终态 409）。主人旧五端点回归由 sessions_local_test.go 既有用例锁定（零删改）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// buildAgentFakeCLI 编译 sessionx 假 CLI（同 sessions_local_test 口径）。
func buildAgentFakeCLI(t *testing.T) string {
	t.Helper()
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", path, "../sessionx/testdata/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	return path
}

// agentServerHarness agent 会话面装配（fake CLI + sessionop + mcpaccess 开关可控）。
type agentServerHarness struct {
	srv  *httptest.Server
	svc  *service.Service
	led  *ledger.Ledger
	acc  *McpAccess
	kern *sessionx.Manager
}

// newAgentSessionServer 装配 agent 面 server；mode 控制假 CLI 耗时注入：
//   - ""            全轮快速完成（流程/幂等/授权用例）
//   - "long-create" 全轮长跑（执行取消用例：首轮 create 即长跑）
//   - "long-resume" 仅续轮长跑（队列/排队取消用例：create 快速发布，send 长跑）
//
// roots 非空配置工作区授权。
func newAgentSessionServer(t *testing.T, mode string, roots ...string) *agentServerHarness {
	t.Helper()
	fake := buildAgentFakeCLI(t)
	cwd := t.TempDir()
	profiles := []protocol.ExecProfile{{
		Name: "coder", Kind: protocol.AgentKindCLI, Command: fake,
		Workdir: cwd, TimeoutSec: 120, Exposed: true, PermissionMode: "workspace-write",
		SessionAdapter: protocol.SessionAdapterCodex,
	}}
	dataDir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex, "FAKE_THREAD_ID=thr-agent-1"}
	switch mode {
	case "long-create":
		env = append(env, "FAKE_SLEEP=30")
	case "long-resume":
		env = append(env, "FAKE_ON_RESUME=1", "FAKE_SLEEP=30")
	}
	kern, err := sessionx.NewManager(dataDir, map[string]sessionx.AgentSpec{
		"coder": {
			Adapter: protocol.SessionAdapterCodex, Command: fake,
			PermissionMode: "workspace-write", TimeoutSec: 120, Env: env,
		},
	}, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	ops, err := sessionop.New(dataDir, led, sessionop.Options{AllowedWorkspaceRoots: roots})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	bus := transport.NewBus()
	svc := service.New(service.Config{
		Ledger: led, Runners: map[string]service.Runner{},
		ArtifactRoots: map[string]string{"coder": cwd}, Sessions: kern, SessionOps: ops,
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	acc := NewMcpAccessMemory(false)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Agents: newState(t, t.TempDir(), profiles),
		Ledger: led, LocalToken: "tok-local", PrimeToken: "tok-prime", PrimeAgent: "coder",
		McpAccess: acc,
	}).Handler())
	t.Cleanup(srv.Close)
	return &agentServerHarness{srv: srv, svc: svc, led: led, acc: acc, kern: kern}
}

// doAgent 带 Bearer 的请求助手（同 doLocal 口径；独立命名避免矩阵混淆）。
func doAgent(t *testing.T, h *agentServerHarness, token, method, path, body string) (int, string) {
	t.Helper()
	return doLocal(t, h.srv, token, method, path, body)
}

// enableAgentMcpAccess 主人经控制台端点显式启用编排授权（W02-01 门翻转）。
func enableAgentMcpAccess(t *testing.T, h *agentServerHarness) {
	t.Helper()
	if code, body := doLocal(t, h.srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("启用编排授权应 200, got %d %s", code, body)
	}
}

// createAgentSession 经 agent 面建会话（201），返回平台 ID。
func createAgentSession(t *testing.T, h *agentServerHarness, token, body string) string {
	t.Helper()
	code, resp := doAgent(t, h, token, http.MethodPost, "/api/v1/sessions", body)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d %s", code, resp)
	}
	var out struct {
		Session struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || !strings.HasPrefix(out.Session.ID, "s-") || out.Session.Status != "pending" {
		t.Fatalf("创建响应异常: %s", resp)
	}
	return out.Session.ID
}

// waitAgentSessionPublished 轮询至 pending 会话发布进内核（首轮完成，列表无 pending 项）。
func waitAgentSessionPublished(t *testing.T, h *agentServerHarness, token, sid string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doAgent(t, h, token, http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(body, `"status":"open"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, body := doAgent(t, h, token, http.MethodGet, "/api/v1/sessions/"+sid, "")
	t.Fatalf("10s 内首轮未发布进内核: %s", body)
}

// TestAgentSessionsAuthMatrix 鉴权矩阵（W02-01 门）：无令牌 401；prime_token 编排
// 授权未启用 403 mcp_disabled（人话指路）；主人显式启用后 prime_token 201/202 全
// 链；local_token 恒放行。
func TestAgentSessionsAuthMatrix(t *testing.T) {
	h := newAgentSessionServer(t, "")
	body := `{"agent":"coder","cwd":"` + strings.ReplaceAll(t.TempDir(), `\`, `\\`) + `"}`
	// 无令牌
	if code, _ := doAgent(t, h, "", http.MethodPost, "/api/v1/sessions", body); code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, got %d", code)
	}
	// prime_token：编排授权未启用 → 403 mcp_disabled
	code, resp := doAgent(t, h, "tok-prime", http.MethodPost, "/api/v1/sessions", body)
	if code != http.StatusForbidden || !strings.Contains(resp, "mcp_disabled") {
		t.Fatalf("未启用应 403 mcp_disabled, got %d %s", code, resp)
	}
	// local_token：主人恒放行（201）
	if code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions", body); code != http.StatusCreated {
		t.Fatalf("local_token 创建应 201, got %d %s", code, resp)
	}
	// 主人显式启用后 prime_token 全链（201 创建 + 202 投轮）
	enableAgentMcpAccess(t, h)
	sid := createAgentSession(t, h, "tok-prime", body)
	if code, resp := doAgent(t, h, "tok-prime", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"p"}`); code != http.StatusAccepted {
		t.Fatalf("启用后 prime 投轮应 202, got %d %s", code, resp)
	}
	waitAgentSessionPublished(t, h, "tok-prime", sid)
}

// TestAgentSessionsPendingVisibility pending 可查（任务书 AC01）：创建 201 登记
// （不执行模型）→ 列表/详情可见平台 ID+受理态 → 投轮后详情展示受理事实 → 首轮
// 完成发布进内核（pending 消失，同一 ID 内核快照可查）。
func TestAgentSessionsPendingVisibility(t *testing.T) {
	h := newAgentSessionServer(t, "")
	cwd := t.TempDir()
	sid := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+strings.ReplaceAll(cwd, `\`, `\\`)+`","title":"巡检"}`)

	// 详情：pending + 平台 ID + 标题
	_, body := doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/"+sid, "")
	if !strings.Contains(body, `"pending":true`) || !strings.Contains(body, `"status":"pending"`) || !strings.Contains(body, "巡检") {
		t.Fatalf("pending 详情异常: %s", body)
	}
	// 列表含 pending 项
	_, body = doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions", "")
	if !strings.Contains(body, sid) || !strings.Contains(body, `"pending":true`) {
		t.Fatalf("列表应含 pending 项: %s", body)
	}
	// 未创建的 ID 404（不误导）
	if code, _ := doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/s-nope", ""); code != http.StatusNotFound {
		t.Fatalf("未知会话应 404, got %d", code)
	}
	// 投轮（pending 会话首轮）：202 → 详情受理事实（queued/running）→ 发布
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"首轮"}`)
	if code != http.StatusAccepted {
		t.Fatalf("pending 投轮应 202, got %d %s", code, resp)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body = doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(body, `"status":"open"`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(body, `"status":"open"`) || !strings.Contains(body, "thr-agent-1") {
		t.Fatalf("首轮未发布: %s", body)
	}
	// 发布后列表：同一 ID 且无 pending 项
	_, body = doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions", "")
	if !strings.Contains(body, sid) || strings.Contains(body, `"pending":true`) {
		t.Fatalf("发布后列表不应再有 pending 项: %s", body)
	}
	// 续轮正常（内核已发布会话）
	if code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"续轮"}`); code != http.StatusAccepted {
		t.Fatalf("续轮应 202, got %d %s", code, resp)
	}
	waitAgentTurn(t, h, "tok-local", sid, 2)
}

// waitAgentTurn 轮询至内核 turn_count 达标。
func waitAgentTurn(t *testing.T, h *agentServerHarness, token, sid string, count int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doAgent(t, h, token, http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(body, fmt.Sprintf(`"turn_count":%d`, count)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, body := doAgent(t, h, token, http.MethodGet, "/api/v1/sessions/"+sid, "")
	t.Fatalf("10s 内轮数未达 %d: %s", count, body)
}

// TestAgentSessionsRequestIdempotency request_id 幂等三态（创建/投轮各自：同键同
// 文回原受理、同键异文 409 request_conflict、无 request_id 行为零变化）。
func TestAgentSessionsRequestIdempotency(t *testing.T) {
	h := newAgentSessionServer(t, "")
	cwd := strings.ReplaceAll(t.TempDir(), `\`, `\\`)
	// 创建：同键同文 → 200 dup 返原会话
	sid := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+cwd+`","request_id":"rid-create-1"}`)
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions", `{"agent":"coder","cwd":"`+cwd+`","request_id":"rid-create-1"}`)
	if code != http.StatusOK || !strings.Contains(resp, `"dup":true`) || !strings.Contains(resp, sid) {
		t.Fatalf("同键同文创建应 200+dup 返原会话, got %d %s", code, resp)
	}
	// 创建：同键异文 → 409 request_conflict
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions", `{"agent":"coder","cwd":"`+cwd+`","title":"另一份","request_id":"rid-create-1"}`)
	if code != http.StatusConflict || !strings.Contains(resp, "request_conflict") {
		t.Fatalf("同键异文创建应 409 request_conflict, got %d %s", code, resp)
	}
	// 创建：无 request_id → 每次新会话（行为零变化）
	sid2 := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+cwd+`"}`)
	if sid2 == sid {
		t.Fatal("无 request_id 应分配新会话")
	}
	// 投轮：同键同文 → 200 dup 返原回执（同 task_id）
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"幂等轮","request_id":"rid-turn-1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("投轮应 202, got %d %s", code, resp)
	}
	var ack struct {
		Turn struct {
			TaskID  string `json:"task_id"`
			TurnSeq int64  `json:"turn_seq"`
		} `json:"turn"`
	}
	_ = json.Unmarshal([]byte(resp), &ack)
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"幂等轮","request_id":"rid-turn-1"}`)
	if code != http.StatusOK || !strings.Contains(resp, `"dup":true`) || !strings.Contains(resp, ack.Turn.TaskID) {
		t.Fatalf("同键同文投轮应 200+dup 返原回执, got %d %s", code, resp)
	}
	// 投轮：同键异文 → 409
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"另一段","request_id":"rid-turn-1"}`)
	if code != http.StatusConflict || !strings.Contains(resp, "request_conflict") {
		t.Fatalf("同键异文投轮应 409, got %d %s", code, resp)
	}
	waitAgentSessionPublished(t, h, "tok-local", sid) // 首轮完成后再测显式序号（首轮独占期不受理新轮）
	// 显式 turn_seq 重放：同文回原回执、异文 409、跳号 409
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"幂等轮","turn_seq":1}`)
	if code != http.StatusOK || !strings.Contains(resp, ack.Turn.TaskID) {
		t.Fatalf("显式同号同文应重放回执, got %d %s", code, resp)
	}
	if code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"不同","turn_seq":1}`); code != http.StatusConflict || !strings.Contains(resp, "turn_seq_conflict") {
		t.Fatalf("同号异文应 409 turn_seq_conflict, got %d %s", code, resp)
	}
	if code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"跳号","turn_seq":9}`); code != http.StatusConflict || !strings.Contains(resp, "turn_seq_conflict") {
		t.Fatalf("跳号应 409, got %d %s", code, resp)
	}
}

// TestAgentSessionsQueueLimit 队列上限（AC01）：非终态受理轮达 16 后第 17 条 409
// turn_queue_full，零副作用（不产生第 17 号受理）。long-resume：create 快速发布，
// send 全部长跑停留非终态。
func TestAgentSessionsQueueLimit(t *testing.T) {
	h := newAgentSessionServer(t, "long-resume")
	cwd := strings.ReplaceAll(t.TempDir(), `\`, `\\`)
	sid := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+cwd+`"}`)
	if code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"首轮"}`); code != http.StatusAccepted {
		t.Fatalf("首轮应 202, got %d %s", code, resp)
	}
	waitAgentSessionPublished(t, h, "tok-local", sid) // create 完成进入 send 形态（15 条长跑 send 排队）
	for i := 0; i < 16; i++ {
		code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", fmt.Sprintf(`{"prompt":"p%d"}`, i))
		if code != http.StatusAccepted {
			t.Fatalf("第 %d 条应 202, got %d %s", i+1, code, resp)
		}
	}
	// 等待 16 条 send 全部落账（seq 2..17；全部非终态）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.led.SessionTurnHighWater(sid) >= 17 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hw := h.led.SessionTurnHighWater(sid); hw != 17 {
		t.Fatalf("高水位应 17, got %d", hw)
	}
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"第17条非终态"}`)
	if code != http.StatusConflict || !strings.Contains(resp, "turn_queue_full") {
		t.Fatalf("第 17 条非终态应 409 turn_queue_full, got %d %s", code, resp)
	}
	if hw := h.led.SessionTurnHighWater(sid); hw != 17 {
		t.Fatalf("拒绝应零副作用（高水位不推进）, got %d", hw)
	}
	// 清场：取消全部在途轮，避免测试进程等待 30s fake
	for seq := int64(1); seq <= 16; seq++ {
		_, _ = doAgent(t, h, "tok-local", http.MethodPost, fmt.Sprintf("/api/v1/sessions/%s/turns/%d/cancel", sid, seq), `{}`)
	}
}

// TestAgentSessionsWorkspaceDenied 工作区授权：配置根后越界 create 403
// workspace_denied（零副作用）、根内放行；未配置根放行保持现状（RFC §5）。
func TestAgentSessionsWorkspaceDenied(t *testing.T) {
	root := t.TempDir()
	h := newAgentSessionServer(t, "", root)
	rootJSON := strings.ReplaceAll(root, `\`, `\\`)
	outside := strings.ReplaceAll(t.TempDir(), `\`, `\\`)
	// 越界 403
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions", `{"agent":"coder","cwd":"`+outside+`"}`)
	if code != http.StatusForbidden || !strings.Contains(resp, "workspace_denied") {
		t.Fatalf("越界应 403 workspace_denied, got %d %s", code, resp)
	}
	if n := len(h.svc.AgentSessionCreations()); n != 0 {
		t.Fatalf("拒绝应零副作用（无登记）, got %d", n)
	}
	// 根内放行（子目录尚不存在：最近已存在祖先归一，同根判定放行）
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions", `{"agent":"coder","cwd":"`+rootJSON+`/sub/dep"}`)
	if code != http.StatusCreated {
		t.Fatalf("根内应 201, got %d %s", code, resp)
	}
	// 未配置根 = 放行（对照）
	h2 := newAgentSessionServer(t, "")
	if code, _ := doAgent(t, h2, "tok-local", http.MethodPost, "/api/v1/sessions", `{"agent":"coder","cwd":"`+outside+`"}`); code != http.StatusCreated {
		t.Fatalf("未配置根应放行 201, got %d", code)
	}
}

// TestAgentSessionCancelExecuted 取消执行中轮：回执 executed、轮终 cancelled
//（记账码 turn_cancelled_executed）、已终态再取消 409 turn_already_finished。
func TestAgentSessionCancelExecuted(t *testing.T) {
	h := newAgentSessionServer(t, "long-create")
	cwd := strings.ReplaceAll(t.TempDir(), `\`, `\\`)
	sid := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+cwd+`"}`)
	if code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"长跑"}`); code != http.StatusAccepted {
		t.Fatalf("投轮应 202, got %d %s", code, resp)
	}
	// 等待进入执行（账本受理轮 started → pending 投影 state=running）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(body, `"state":"running"`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// prime_token 401（人工专属，agent 面无取消）
	if code, _ := doAgent(t, h, "tok-prime", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("prime_token 取消应 401, got %d", code)
	}
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{}`)
	if code != http.StatusOK || !strings.Contains(resp, `"outcome":"executed"`) || !strings.Contains(resp, "turn_cancelled_executed") {
		t.Fatalf("执行取消应 200 executed, got %d %s", code, resp)
	}
	// 轮终 cancelled（pending 投影展示账本事实）
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(body, `"error":"turn_cancelled_executed"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, body := doAgent(t, h, "tok-local", http.MethodGet, "/api/v1/sessions/"+sid, "")
	t.Fatalf("5s 内轮行未落取消终态: %s", body)
}

// TestAgentSessionCancelQueuedAndFinished 排队取消确定性（不 spawn）+ 已终态 409
// + 未知轮 404。long-resume：seq1 create 快速完成；seq2 send 长跑占执行位；seq3
// send 排队（workdir 互斥/内核门被占）。
func TestAgentSessionCancelQueuedAndFinished(t *testing.T) {
	h := newAgentSessionServer(t, "long-resume")
	cwd := strings.ReplaceAll(t.TempDir(), `\`, `\\`)
	sid := createAgentSession(t, h, "tok-local", `{"agent":"coder","cwd":"`+cwd+`"}`)
	if code, _ := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"首轮"}`); code != http.StatusAccepted {
		t.Fatal("首轮应 202")
	}
	waitAgentSessionPublished(t, h, "tok-local", sid)
	// seq=2 长跑占执行位
	if code, _ := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"占位轮"}`); code != http.StatusAccepted {
		t.Fatal("占位轮应 202")
	}
	// seq=3 排队 → 取消 → queued 回执（确定性不 spawn）
	if code, _ := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"排队轮"}`); code != http.StatusAccepted {
		t.Fatal("排队轮应 202")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.led.SessionTurnHighWater(sid) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/3/cancel", `{}`)
	if code != http.StatusOK || !strings.Contains(resp, `"outcome":"queued"`) || !strings.Contains(resp, "turn_cancelled_queued") {
		t.Fatalf("排队取消应 200 queued, got %d %s", code, resp)
	}
	// 幂等重放：再取消同轮返回同一 queued 回执
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/3/cancel", `{}`)
	if code != http.StatusOK || !strings.Contains(resp, `"outcome":"queued"`) {
		t.Fatalf("重复取消应幂等 queued, got %d %s", code, resp)
	}
	// 已完成轮（seq=1 create）不可取消：409 turn_already_finished
	code, resp = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{}`)
	if code != http.StatusConflict || !strings.Contains(resp, "turn_already_finished") {
		t.Fatalf("已完成轮取消应 409 turn_already_finished, got %d %s", code, resp)
	}
	// 未知轮 404
	if code, resp := doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/9/cancel", `{}`); code != http.StatusNotFound || !strings.Contains(resp, "turn_not_found") {
		t.Fatalf("未知轮应 404 turn_not_found, got %d %s", code, resp)
	}
	// 清场
	_, _ = doAgent(t, h, "tok-local", http.MethodPost, "/api/v1/sessions/"+sid+"/turns/2/cancel", `{}`)
}
