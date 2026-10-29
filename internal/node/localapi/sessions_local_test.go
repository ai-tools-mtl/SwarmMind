package localapi

// sessions_local_test.go 本机会话端点（T2，SPEC 六.五 + 六.六豁免⑩）：authLocal
// 门禁（local_token 专属，prime_token 不放行）、全链（新建+首轮 → 详情 → 续轮 →
// 关闭 → 关闭后拒绝）、503 诚实降级、权限断言核对。

import (
	"encoding/json"
	"io"
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
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// buildLocalFakeCLI 编译 sessionx 假 CLI（本包视角相对路径 ../sessionx/testdata）。
func buildLocalFakeCLI(t *testing.T) string {
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

// newSessionLocalServer 装配带会话内核的本机 API（session agent=coder，codex
// 形态 fake；返回 server 与会话内核句柄）。
func newSessionLocalServer(t *testing.T) (*httptest.Server, *sessionx.Manager) {
	t.Helper()
	fake := buildLocalFakeCLI(t)
	cwd := t.TempDir()
	profiles := []protocol.ExecProfile{{
		Name: "coder", Kind: protocol.AgentKindCLI, Command: fake,
		Workdir: cwd, TimeoutSec: 30, Exposed: true, PermissionMode: "workspace-write",
		SessionAdapter: protocol.SessionAdapterCodex,
	}}
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	mgr, err := sessionx.NewManager(t.TempDir(), map[string]sessionx.AgentSpec{
		"coder": {
			Adapter: protocol.SessionAdapterCodex, Command: fake,
			PermissionMode: "workspace-write", TimeoutSec: 30,
			Env: []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex, "FAKE_THREAD_ID=thr-local-1"},
		},
	}, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	bus := transport.NewBus()
	svc := service.New(service.Config{
		Ledger: led, Runners: map[string]service.Runner{},
		ArtifactRoots: map[string]string{"coder": cwd}, Sessions: mgr,
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Agents: newState(t, t.TempDir(), profiles),
		Ledger:     led,
		LocalToken: "tok-local", PrimeToken: "tok-prime", PrimeAgent: "coder",
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, mgr
}

// doLocal 带 Bearer 的请求助手。
func doLocal(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestLocalSessionsAuthGate 门禁：无令牌 401；prime_token 401（authLocal——本机
// 原生线程管理是主人专属，编排凭证不放行，六.六豁免⑩口径）；local_token 放行。
func TestLocalSessionsAuthGate(t *testing.T) {
	srv, _ := newSessionLocalServer(t)
	for _, tc := range [][2]string{
		{"POST", "/api/v1/local/sessions"},
		{"GET", "/api/v1/local/sessions"},
		{"POST", "/api/v1/local/sessions/s-x/close"},
	} {
		if code, _ := doLocal(t, srv, "", tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 无令牌应 401, got %d", tc[0], tc[1], code)
		}
		if code, _ := doLocal(t, srv, "tok-prime", tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s prime_token 应 401（主人专属）, got %d", tc[0], tc[1], code)
		}
	}
	if code, _ := doLocal(t, srv, "tok-local", http.MethodGet, "/api/v1/local/sessions", ""); code != http.StatusOK {
		t.Fatalf("local_token 列表应 200, got %d", code)
	}
}

// TestLocalSessionsFlow 全链：权限断言核对（yolo 400 / 不符 409）→ 新建+首轮
//（202 回执）→ 轮终详情（native ID/轮数）→ 续轮 → 关闭 → 关闭后续轮 409 →
// 重复关闭幂等 → 列表与 404。
func TestLocalSessionsFlow(t *testing.T) {
	srv, _ := newSessionLocalServer(t)
	createBody := `{"agent":"coder","cwd":"` + strings.ReplaceAll(t.TempDir(), `\`, `\\`) + `","prompt":"首轮"}`

	// 权限断言核对（不发任务）
	if code, body := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions",
		`{"agent":"coder","cwd":"/tmp/x","prompt":"p","permission_mode":"yolo"}`); code != http.StatusBadRequest {
		t.Fatalf("yolo 断言应 400, got %d %s", code, body)
	}
	if code, body := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions",
		`{"agent":"coder","cwd":"/tmp/x","prompt":"p","permission_mode":"read-only"}`); code != http.StatusConflict {
		t.Fatalf("断言不符应 409, got %d %s", code, body)
	}

	code, body := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions", createBody)
	if code != http.StatusAccepted {
		t.Fatalf("新建应 202, got %d %s", code, body)
	}
	var ack struct {
		Turn struct {
			SessionID string `json:"session_id"`
			TurnSeq   int64  `json:"turn_seq"`
			TaskID    string `json:"task_id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal([]byte(body), &ack); err != nil || ack.Turn.SessionID == "" || ack.Turn.TurnSeq != 1 {
		t.Fatalf("受理回执异常: %s", body)
	}
	sid := ack.Turn.SessionID

	waitLocalTurn(t, srv, sid, 1)

	// 续轮
	if code, body = doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions/"+sid+"/turns", `{"prompt":"续轮"}`); code != http.StatusAccepted {
		t.Fatalf("续轮应 202, got %d %s", code, body)
	}
	waitLocalTurn(t, srv, sid, 2)

	// 详情含原生 ID（映射建立）与轮数
	_, body = doLocal(t, srv, "tok-local", http.MethodGet, "/api/v1/local/sessions/"+sid, "")
	if !strings.Contains(body, "thr-local-1") || !strings.Contains(body, `"turn_count":2`) {
		t.Fatalf("详情应含原生映射与轮数: %s", body)
	}

	// 关闭 + 关闭后续轮拒绝 + 重复关闭幂等
	if code, _ := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions/"+sid+"/close", `{}`); code != http.StatusOK {
		t.Fatal("关闭应 200")
	}
	if code, body := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions/"+sid+"/turns", `{"prompt":"x"}`); code != http.StatusConflict || !strings.Contains(body, "session_closed") {
		t.Fatalf("关闭后续轮应 409 session_closed, got %d %s", code, body)
	}
	if code, body := doLocal(t, srv, "tok-local", http.MethodPost, "/api/v1/local/sessions/"+sid+"/close", `{}`); code != http.StatusOK || !strings.Contains(body, `"closed"`) {
		t.Fatalf("重复关闭应幂等 200, got %d %s", code, body)
	}
	if _, body = doLocal(t, srv, "tok-local", http.MethodGet, "/api/v1/local/sessions", ""); !strings.Contains(body, sid) {
		t.Fatalf("列表应含会话: %s", body)
	}
	if code, _ := doLocal(t, srv, "tok-local", http.MethodGet, "/api/v1/local/sessions/s-nope", ""); code != http.StatusNotFound {
		t.Fatalf("未知会话应 404, got %d", code)
	}
}

// waitLocalTurn 等待指定轮到 ok（本地链异步执行）。
func waitLocalTurn(t *testing.T, srv *httptest.Server, sid string, seq int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body := doLocal(t, srv, "tok-local", http.MethodGet, "/api/v1/local/sessions/"+sid, "")
		if strings.Contains(body, `"turn_count":`+itoaInt64(seq)) && strings.Contains(body, `"status":"ok"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("5s 内轮 #%d 未完成", seq)
}

func itoaInt64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestLocalSessionsUnsupportedDowngrade 未装配会话内核：端点组 503 诚实降级
//（不静默空转）。
func TestLocalSessionsUnsupportedDowngrade(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA")) // 无 Sessions
	srv := httptest.NewServer(New(Config{NodeID: "nodeA", Service: svc, LocalToken: "tok-local"}).Handler())
	t.Cleanup(srv.Close)
	for _, tc := range [][2]string{{"POST", "/api/v1/local/sessions"}, {"GET", "/api/v1/local/sessions"}} {
		if code, body := doLocal(t, srv, "tok-local", tc[0], tc[1], `{}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "session_unsupported") {
			t.Fatalf("%s %s 应 503 session_unsupported, got %d %s", tc[0], tc[1], code, body)
		}
	}
}
