package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// shortBackoff 测试用退避序列（毫秒级，等价默认 1s/2s/4s 的三连退避形态）。
var shortBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}

// hookServer 假 webhook：记录全部请求体；failN 次内一律 500，此后 200。
type hookServer struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	bodies []string
	failN  int           // 前 failN 次请求返回 500
	sleep  time.Duration // 每次请求人为拖延（单次超时用例）
}

func newHookServer(t *testing.T, failN int) *hookServer {
	h := &hookServer{t: t, failN: failN}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("通知应为 POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type 应为 application/json, got %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(b))
		code := http.StatusInternalServerError
		if len(h.bodies) > h.failN {
			code = http.StatusOK
		}
		sleep := h.sleep
		h.mu.Unlock()
		if sleep > 0 {
			time.Sleep(sleep)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookServer) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func (h *hookServer) lastBody() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) == 0 {
		h.t.Fatalf("webhook 未收到任何请求")
	}
	return h.bodies[len(h.bodies)-1]
}

// testTask 构造带全字段的终态任务。
func testTask(status protocol.TaskStatus, result *protocol.TaskResult) protocol.Task {
	return protocol.Task{
		ID:          "t-abc123",
		TargetNode:  "mac-mini",
		TargetAgent: "coder",
		SubmittedBy: "lun-guo",
		Status:      status,
		CreatedAt:   time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 10, 24, 10, 3, 20, 0, time.UTC), // 3m20s
		Result:      result,
	}
}

func newTestClient(t *testing.T, url, format string) *Client {
	t.Helper()
	c, err := NewClient(Config{WebhookURL: url, Format: format, Timeout: 2 * time.Second, Backoff: shortBackoff})
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	return c
}

// TestNewClientRejectsUnknownFormat 格式启动即校验，不静默回落。
func TestNewClientRejectsUnknownFormat(t *testing.T) {
	if _, err := NewClient(Config{WebhookURL: "http://x", Format: "slack"}); err == nil {
		t.Fatal("未知格式应报错")
	}
	for _, f := range []string{"", FormatRaw, FormatFeishu} {
		if _, err := NewClient(Config{WebhookURL: "http://x", Format: f}); err != nil {
			t.Fatalf("格式 %q 应合法: %v", f, err)
		}
	}
}

// TestTaskFinalRawPayload raw 载荷结构与字段齐全性（P4③ 设计字段表）。
func TestTaskFinalRawPayload(t *testing.T) {
	h := newHookServer(t, 0)
	c := newTestClient(t, h.srv.URL, FormatRaw)
	c.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		ExitCode:    0,
		FinalAnswer: "done",
		Error:       "", // completed 无错误
	}))
	c.WaitIdle()

	if got := h.count(); got != 1 {
		t.Fatalf("终态应恰好通知一次, got %d", got)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(h.lastBody()), &p); err != nil {
		t.Fatalf("载荷应为合法 JSON: %v", err)
	}
	want := map[string]any{
		"event":        "task_final",
		"task_id":      "t-abc123",
		"status":       "completed",
		"node_id":      "mac-mini",
		"agent":        "coder",
		"submitted_by": "lun-guo",
		"duration_sec": float64(200), // 3m20s
		"summary":      "done",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("字段 %s = %v, want %v", k, p[k], v)
		}
	}
	for _, absent := range []string{"error"} { // 缺失字段必须省略而非空串
		if _, ok := p[absent]; ok {
			t.Errorf("缺失字段 %s 不应出现", absent)
		}
	}
}

// TestTaskFinalRawSummaryTruncation summary 取 FinalAnswer 前 200 字符（rune 计）。
func TestTaskFinalRawSummaryTruncation(t *testing.T) {
	h := newHookServer(t, 0)
	c := newTestClient(t, h.srv.URL, FormatRaw)
	long := ""
	for i := 0; i < 300; i++ {
		long += "汉"
	}
	c.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{
		Status: protocol.ResultCompleted, FinalAnswer: long,
	}))
	c.WaitIdle()

	var p struct{ Summary string }
	if err := json.Unmarshal([]byte(h.lastBody()), &p); err != nil {
		t.Fatalf("载荷解析: %v", err)
	}
	if n := len([]rune(p.Summary)); n != summaryMaxLen {
		t.Fatalf("summary 应截断到 %d 字符, got %d", summaryMaxLen, n)
	}
	if p.Summary != string([]rune(long)[:summaryMaxLen]) {
		t.Fatal("summary 应为前 200 字符前缀")
	}
}

// TestTaskFinalRawMinimalTask 缺失字段（无提交方/无节点/无耗时/无结果）整体省略。
func TestTaskFinalRawMinimalTask(t *testing.T) {
	h := newHookServer(t, 0)
	c := newTestClient(t, h.srv.URL, FormatRaw)
	c.TaskFinal(protocol.Task{ID: "t-min", Status: protocol.StatusCancelled}) // 取消即终态，Result=nil
	c.WaitIdle()

	var p map[string]any
	if err := json.Unmarshal([]byte(h.lastBody()), &p); err != nil {
		t.Fatalf("载荷解析: %v", err)
	}
	for _, k := range []string{"event", "task_id", "status"} {
		if _, ok := p[k]; !ok {
			t.Errorf("必备字段 %s 缺失", k)
		}
	}
	for _, k := range []string{"node_id", "agent", "submitted_by", "duration_sec", "error", "summary"} {
		if _, ok := p[k]; ok {
			t.Errorf("缺失字段 %s 不应出现", k)
		}
	}
}

// TestTaskFinalFeishuFormat 飞书文本消息：包装格式 + 四终态人话文案。
func TestTaskFinalFeishuFormat(t *testing.T) {
	cases := []struct {
		name   string
		status protocol.TaskStatus
		result *protocol.TaskResult
		want   string
	}{
		{"completed", protocol.StatusCompleted, &protocol.TaskResult{Status: protocol.ResultCompleted},
			"✅ 任务完成 t-abc123 · mac-mini/coder · 耗时 3m20s"},
		{"failed", protocol.StatusFailed, &protocol.TaskResult{Status: protocol.ResultFailed, Error: protocol.CodeTimeout},
			"❌ 任务失败 t-abc123 · mac-mini/coder · 耗时 3m20s（原因：timeout）"},
		{"needs_review", protocol.StatusNeedsReview, &protocol.TaskResult{Status: protocol.ResultNeedsReview},
			"⚠️ 任务结果拿不准，等你确认 t-abc123 · mac-mini/coder · 耗时 3m20s"},
		{"cancelled", protocol.StatusCancelled, nil,
			"🚫 任务已取消 t-abc123 · mac-mini/coder · 耗时 3m20s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookServer(t, 0)
			c := newTestClient(t, h.srv.URL, FormatFeishu)
			c.TaskFinal(testTask(tc.status, tc.result))
			c.WaitIdle()

			var m feishuMessage
			if err := json.Unmarshal([]byte(h.lastBody()), &m); err != nil {
				t.Fatalf("载荷解析: %v", err)
			}
			if m.MsgType != "text" {
				t.Errorf("msg_type = %q, want text", m.MsgType)
			}
			if m.Content.Text != tc.want {
				t.Errorf("文案 = %q, want %q", m.Content.Text, tc.want)
			}
		})
	}
}

// TestRetryUntilSuccess 前 2 次 500，第 3 次（首次+2 重试）成功即停。
func TestRetryUntilSuccess(t *testing.T) {
	h := newHookServer(t, 2)
	c := newTestClient(t, h.srv.URL, FormatRaw)
	c.TaskFinal(testTask(protocol.StatusCompleted, nil))
	c.WaitIdle()

	if got := h.count(); got != 3 {
		t.Fatalf("第 3 次成功应共 3 次请求, got %d", got)
	}
}

// TestRetryExhaustedGiveUp 恒 500：首次 + 3 次重试共 4 次后放弃，只记日志不 panic。
func TestRetryExhaustedGiveUp(t *testing.T) {
	h := newHookServer(t, 1<<30) // 永远 500
	c := newTestClient(t, h.srv.URL, FormatRaw)
	c.TaskFinal(testTask(protocol.StatusFailed, &protocol.TaskResult{Error: protocol.CodeTimeout}))
	c.WaitIdle()

	if got := h.count(); got != 1+len(shortBackoff) {
		t.Fatalf("应首发 + %d 次重试 = %d 次请求, got %d", len(shortBackoff), 1+len(shortBackoff), got)
	}
}

// TestPerAttemptTimeout 单次请求超时按 Timeout 计：服务端拖过超时即判失败并重试。
func TestPerAttemptTimeout(t *testing.T) {
	h := newHookServer(t, 0)
	h.mu.Lock()
	h.sleep = 200 * time.Millisecond
	h.mu.Unlock()
	c, err := NewClient(Config{WebhookURL: h.srv.URL, Timeout: 20 * time.Millisecond, Backoff: shortBackoff})
	if err != nil {
		t.Fatalf("构造 client: %v", err)
	}
	c.TaskFinal(testTask(protocol.StatusCompleted, nil))
	c.WaitIdle()

	if got := h.count(); got != 1+len(shortBackoff) {
		t.Fatalf("超时应触发全部重试共 %d 次请求, got %d", 1+len(shortBackoff), got)
	}
}

// TestEmptyURLDisabled 空 webhook=禁用：TaskFinal 直接返回，无 goroutine 无 panic。
func TestEmptyURLDisabled(t *testing.T) {
	c := newTestClient(t, "", FormatRaw)
	c.TaskFinal(testTask(protocol.StatusCompleted, nil))
	c.WaitIdle() // 立即返回即无在途
}
