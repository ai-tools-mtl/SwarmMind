package notify

import (
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

// testNow 固定时钟（渠道创建时间确定化）。
var testNow = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

// hookServer 假 webhook：记录全部请求体与 Header；failN 次内一律 500，此后 200。
type hookServer struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	uris    []string        // 原始请求 URI（path+query，验证投递地址未被脱敏削减）
	failN   int             // 前 failN 次请求返回 500
	sleep   time.Duration   // 每次请求人为拖延（单次超时用例）
}

func newHookServer(t *testing.T, failN int) *hookServer {
	h := &hookServer{t: t, failN: failN}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("通知应为 POST, got %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(b))
		h.headers = append(h.headers, r.Header.Clone())
		h.uris = append(h.uris, r.URL.RequestURI())
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

func (h *hookServer) lastHeader() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.headers[len(h.headers)-1]
}

// lastURI 最近一次请求的 RequestURI（path+query；验证凭证随原始地址完整到达）。
func (h *hookServer) lastURI() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.uris) == 0 {
		h.t.Fatalf("webhook 未收到任何请求")
	}
	return h.uris[len(h.uris)-1]
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

// newTestStore 每用例独立渠道存储（TempDir 隔离落盘）。
func newTestStore(t *testing.T) *ChannelStore {
	t.Helper()
	s, err := OpenChannelStore(t.TempDir())
	if err != nil {
		t.Fatalf("打开渠道存储: %v", err)
	}
	return s
}

// mustCreate 创建渠道（失败即 Fatal）。
func mustCreate(t *testing.T, s *ChannelStore, typ, name string, events []string, secrets map[string]string) Channel {
	t.Helper()
	ch, err := s.Create(typ, name, events, secrets, testNow)
	if err != nil {
		t.Fatalf("创建渠道 %s/%s: %v", typ, name, err)
	}
	return ch
}

// newTestDispatcher 组装 store+inbox+Dispatcher（短退避短超时）。
func newTestDispatcher(t *testing.T, s *ChannelStore) (*Dispatcher, *Inbox) {
	t.Helper()
	ib := &Inbox{}
	return NewDispatcher(s, ib, WithTimeout(2*time.Second), WithBackoff(shortBackoff)), ib
}
