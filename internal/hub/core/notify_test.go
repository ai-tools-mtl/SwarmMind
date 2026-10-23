package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/notify"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 测试用退避（毫秒级）：形态等价默认 1s/2s/4s 三连退避，耗尽即放弃。
var notifyBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}

// notifyHook 假 webhook：计数并记录请求体；failN 次内一律 500，此后 200。
type notifyHook struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	failN  int
}

func newNotifyHook(t *testing.T, failN int) *notifyHook {
	h := &notifyHook{failN: failN}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.bodies = append(h.bodies, "req")
		code := http.StatusInternalServerError
		if len(h.bodies) > h.failN {
			code = http.StatusOK
		}
		h.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *notifyHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

// newHubWithNotifier newHub 的带通知器变体（其余基建同口径）。
func newHubWithNotifier(t *testing.T, n TerminalNotifier) *hubFixture {
	t.Helper()
	st, err := storeOpen(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	cr, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0, Notifier: n})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: cr, clock: clock}
	f.hubEP.Subscribe(cr.HandleFrame)
	t.Cleanup(func() {
		_ = cr.Close()
		_ = st.Close()
	})
	return f
}

func newNotifyClient(t *testing.T, url string) *notify.Client {
	t.Helper()
	c, err := notify.NewClient(notify.Config{WebhookURL: url, Backoff: notifyBackoff})
	if err != nil {
		t.Fatalf("构造 notify client: %v", err)
	}
	return c
}

// TestNotifyTerminalOncePerTask 终态只发一次：首次接受触发；重复结果（重发）
// 与冲突（保留先到）不再触发——去重挂在非终态→终态转换点。
func TestNotifyTerminalOncePerTask(t *testing.T) {
	hook := newNotifyHook(t, 0)
	nc := newNotifyClient(t, hook.srv.URL)
	f := newHubWithNotifier(t, nc)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	first := completeRes()
	node.result(ack.TaskID, first) // 非终态→completed：触发
	node.result(ack.TaskID, first) // 同终态同结果重发：不触发
	conflicting := completeRes()
	conflicting.ExitCode = 3
	node.result(ack.TaskID, conflicting) // 冲突保留先到：不触发
	nc.WaitIdle()

	if got := hook.count(); got != 1 {
		t.Fatalf("终态应恰好通知一次, got %d", got)
	}
}

// TestNotifyPayloadMirrorsTask 通知载荷如实反映任务终态字段。
func TestNotifyPayloadMirrorsTask(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	nc := newNotifyClient(t, srv.URL)
	f := newHubWithNotifier(t, nc)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	node.result(ack.TaskID, completeRes())
	nc.WaitIdle()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("应恰 1 条通知, got %d", len(bodies))
	}
	var p map[string]any
	if err := json.Unmarshal(bodies[0], &p); err != nil {
		t.Fatalf("载荷解析: %v", err)
	}
	if p["event"] != "task_final" || p["task_id"] != ack.TaskID ||
		p["status"] != "completed" || p["node_id"] != "nodeB" ||
		p["agent"] != "echo" || p["submitted_by"] != "tester" {
		t.Errorf("载荷字段不符: %v", p)
	}
}

// TestNotifyServerFailureDoesNotAffectTask webhook 恒 500：首发 + 3 次重试共
// 4 次后放弃，任务终态与后续主流程完全不受影响（P4③ 可靠性边界）。
func TestNotifyServerFailureDoesNotAffectTask(t *testing.T) {
	hook := newNotifyHook(t, 1<<30) // 永远 500
	nc := newNotifyClient(t, hook.srv.URL)
	f := newHubWithNotifier(t, nc)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	node.result(ack.TaskID, completeRes())
	nc.WaitIdle() // 重试穷尽，goroutine 全部落地

	if got := hook.count(); got != 1+len(notifyBackoff) {
		t.Fatalf("应首发 + %d 次重试 = %d 次请求后放弃, got %d", len(notifyBackoff), 1+len(notifyBackoff), got)
	}
	// 任务终态不受通知失败影响
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil || task.Result.FinalAnswer != "answer" {
		t.Fatalf("任务终态应不受影响, got %+v", task.Result)
	}
	// 主流程继续可用
	ack2, err := f.core.Submit(submitReq("nodeB", "echo", "next"), "")
	if err != nil {
		t.Fatalf("后续提交不应受影响: %v", err)
	}
	node.result(ack2.TaskID, completeRes())
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusCompleted {
		t.Fatalf("后续任务应正常终态, got %s", got)
	}
	nc.WaitIdle()
}

// TestNotifyOnCancelAndSweep 取消与清扫置终态同样触发通知（三条终态路径全口径）。
func TestNotifyOnCancelAndSweep(t *testing.T) {
	hook := newNotifyHook(t, 0)
	nc := newNotifyClient(t, hook.srv.URL)
	f := newHubWithNotifier(t, nc)
	f.newNode("nodeB", agentsOf("echo"), true)

	// 取消：running 前的 acknowledged 直接取消 → cancelled 通知
	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	if _, _, err := f.core.CancelTask(ack.TaskID, "改需求了"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	// 清扫：离线任务过 ExpireAt → failed/expired 通知
	f.registerOffline("nodeC", agentsOf("echo"))
	ack2, _ := f.core.Submit(submitReq("nodeC", "echo"), "")
	f.clock.Advance(protocol.TaskRetention + time.Second)
	f.core.Sweep()
	nc.WaitIdle()

	if got := hook.count(); got != 2 {
		t.Fatalf("取消 + 清扫应各通知一次, got %d", got)
	}
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusFailed {
		t.Fatalf("清扫任务应 failed, got %s", got)
	}
}
