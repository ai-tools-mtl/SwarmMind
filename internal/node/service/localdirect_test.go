package service

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 本地直执行链路单测（方案 3.2 双路径，2026-10-22）：本机入口派单本地直执行、
// 幂等三态、D8 语义、origin 堵点（local 任务不向 hub 发帧）。

// okRunner 恒成功桩执行器：回显 args 首元素为 FinalAnswer/StdoutTail。
type okRunner struct {
	mu      sync.Mutex
	started []string
}

func (r *okRunner) Run(_ context.Context, task protocol.Task) protocol.TaskResult {
	r.mu.Lock()
	r.started = append(r.started, task.ID)
	r.mu.Unlock()
	ans := ""
	if len(task.Input.Args) > 0 {
		ans = task.Input.Args[0]
	}
	return protocol.TaskResult{Status: protocol.ResultCompleted, FinalAnswer: ans, StdoutTail: ans + "\n"}
}

// quietRunner 恒成功且无输出（幂等「同键同内容」用第二个 agent 规避 workdir 互斥干扰）。
type quietRunner struct{}

func (quietRunner) Run(_ context.Context, _ protocol.Task) protocol.TaskResult {
	return protocol.TaskResult{Status: protocol.ResultCompleted, FinalAnswer: "quiet"}
}

func newDirectFixture(t *testing.T) (*Service, *ledger.Ledger, *recorder, *okRunner) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("打开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	rec := &recorder{}
	r := &okRunner{}
	// 出站录制挂在 bus 的 hub 端点——service 向 "hub" 发帧，node 端点只录收件帧
	//（复审 #7：订错端点测不出站）。
	bus.Endpoint("hub").Subscribe(func(env *protocol.Envelope) {
		rec.record(env)
	})
	svc := New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{"echo": r, "quiet": quietRunner{}},
		ArtifactRoots: map[string]string{"echo": t.TempDir(), "quiet": t.TempDir()},
	}, bus.Endpoint("nodeX"))
	t.Cleanup(svc.Close)
	return svc, led, rec, r
}

// waitTerminal 轮询账本至任务终态（执行异步，上限 5s 替代固定 sleep）。
func waitTerminal(t *testing.T, led *ledger.Ledger, taskID string) ledger.Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e, ok := led.Get(taskID)
		if ok && e.State == ledger.StateFinished {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务未在期限内到终态: %+v", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubmitLocalDirectCompleted(t *testing.T) {
	svc, led, _, _ := newDirectFixture(t)
	ack, err := svc.SubmitLocalDirect(protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "echo", SubmittedBy: "prime",
		Input: protocol.TaskInput{Args: []string{"direct-hello"}}, IdemKey: "d-1",
	})
	if err != nil {
		t.Fatalf("本地直提交失败: %v", err)
	}
	if ack.TaskID == "" || ack.TraceID == "" {
		t.Fatalf("ack 不符: %+v", ack)
	}
	e := waitTerminal(t, led, ack.TaskID)
	v := ledger.TaskView(e)
	if v.Status != protocol.StatusCompleted {
		t.Fatalf("应 completed, got %s result=%+v", v.Status, v.Result)
	}
	if v.Result == nil || v.Result.FinalAnswer != "direct-hello" {
		t.Fatalf("结果回显不符: %+v", v.Result)
	}
	if e.Origin != ledger.OriginLocal {
		t.Fatalf("origin 应为 local, got %q", e.Origin)
	}
}

func TestSubmitLocalDirectIdempotent(t *testing.T) {
	svc, led, _, _ := newDirectFixture(t)
	req := protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "echo", SubmittedBy: "prime",
		Input: protocol.TaskInput{Args: []string{"same"}}, IdemKey: "idem-1",
	}
	ack1, err := svc.SubmitLocalDirect(req)
	if err != nil || ack1.Dup {
		t.Fatalf("首次提交应非 dup: %+v err=%v", ack1, err)
	}
	waitTerminal(t, led, ack1.TaskID)

	// 同键同内容 → dup 同任务
	ack2, err := svc.SubmitLocalDirect(req)
	if err != nil || !ack2.Dup || ack2.TaskID != ack1.TaskID {
		t.Fatalf("幂等命中不符: ack=%+v err=%v", ack2, err)
	}

	// 同键不同内容 → conflict
	req2 := req
	req2.Input = protocol.TaskInput{Args: []string{"different"}}
	if _, err := svc.SubmitLocalDirect(req2); err == nil {
		t.Fatal("同键不同内容应 conflict")
	} else {
		var re *RemoteError
		if !asRemoteError(err, &re) || re.Code != "conflict" {
			t.Fatalf("应 conflict, got %v", err)
		}
	}
}

func TestSubmitLocalDirectAgentNotFound(t *testing.T) {
	svc, _, _, _ := newDirectFixture(t)
	_, err := svc.SubmitLocalDirect(protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "ghost", SubmittedBy: "prime",
	})
	var re *RemoteError
	if !asRemoteError(err, &re) || re.Code != protocol.CodeAgentNotFound {
		t.Fatalf("非本机 agent 应 agent_not_found（D8 语义）, got %v", err)
	}
}

func TestSubmitLocalDirectNoHubFrames(t *testing.T) {
	svc, led, rec, _ := newDirectFixture(t)
	ack, err := svc.SubmitLocalDirect(protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "echo", SubmittedBy: "prime",
		Input: protocol.TaskInput{Args: []string{"silent"}},
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	waitTerminal(t, led, ack.TaskID)
	time.Sleep(50 * time.Millisecond) // 留出异步帧窗口
	// 出站帧录制在 hub 端点（见 newDirectFixture）；出站结果帧类型为 task_result
	//（service.sendResult → MsgTaskResult，对照结果回传处），非 result_event。
	for _, typ := range []string{protocol.MsgTaskAck, protocol.MsgTaskStarted, protocol.MsgTaskResult} {
		if f := rec.ofTypes(typ); f != nil {
			t.Fatalf("local 任务不应向 hub 发 %s: %+v", typ, f)
		}
	}

	// origin 过滤（ResendPending，方案 3.2 双保险）：origin=hub 的 finished 未
	// acked 条目必须重发；origin=local 的同状态条目（异常滞留——正常路径落终态
	// 即自确认）不得重发。
	hubTask := protocol.Task{
		ID: "hub-1", TraceID: "tr-hub", TargetNode: "nodeX", TargetAgent: "quiet",
		SubmittedBy: "hub", Status: protocol.StatusOffered,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
	}
	if err := led.AcceptOrigin(hubTask, ledger.OriginHub); err != nil {
		t.Fatalf("seed hub 条目: %v", err)
	}
	if err := led.MarkStarted("hub-1"); err != nil {
		t.Fatalf("seed MarkStarted: %v", err)
	}
	if err := led.MarkFinished("hub-1", protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "hub-done", FinishedAt: protocol.NowUTC(),
	}); err != nil {
		t.Fatalf("seed MarkFinished: %v", err)
	}
	localID := ack.TaskID // 已 finished 且自确认；人为翻回未 acked 模拟异常滞留
	if err := led.Update(func(m map[string]ledger.Entry) error {
		e := m[localID]
		ne := e.Clone()
		ne.ResultAcked = false
		m[localID] = ne
		return nil
	}); err != nil {
		t.Fatalf("local 条目翻回未 acked: %v", err)
	}
	rec.reset()
	svc.ResendPending()
	if n := rec.count(protocol.MsgTaskResult); n != 1 {
		t.Fatalf("ResendPending 应恰重发 1 条（仅 origin=hub）, got %d 帧", n)
	}
	if f := rec.ofTypes(protocol.MsgTaskResult); f == nil || !strings.Contains(string(f.Payload), "hub-1") {
		t.Fatalf("重发的应是 origin=hub 条目 hub-1, got %+v", f)
	}
}

// asRemoteError errors.As 便捷封装。
func asRemoteError(err error, target **RemoteError) bool {
	if err == nil {
		return false
	}
	if re, ok := err.(*RemoteError); ok {
		*target = re
		return true
	}
	return false
}

// countingRunner 计数桩执行器（并发幂等的「执行次数恰 1」断言用）。
type countingRunner struct {
	mu sync.Mutex
	n  int
}

func (r *countingRunner) Run(_ context.Context, _ protocol.Task) protocol.TaskResult {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return protocol.TaskResult{Status: protocol.ResultCompleted, FinalAnswer: "counted"}
}

// newConcurrentFixture 并发幂等用最小基座：单 agent（countingRunner）+ 账本。
func newConcurrentFixture(t *testing.T, r Runner) (*Service, *ledger.Ledger) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("打开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{"cnt": r},
		ArtifactRoots: map[string]string{"cnt": t.TempDir()},
	}, bus.Endpoint("nodeX"))
	t.Cleanup(svc.Close)
	return svc, led
}

// TestSubmitLocalDirectGroupChangeNewTask 换群重试（复审 #8）：同键同内容仅
// GroupRef 不同 → 落为新任务（wantBase 分支，IdemOtherGroup），非 dup 非 conflict。
func TestSubmitLocalDirectGroupChangeNewTask(t *testing.T) {
	svc, led, _, _ := newDirectFixture(t)
	req := protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "echo", SubmittedBy: "prime",
		Input: protocol.TaskInput{Args: []string{"grp"}}, IdemKey: "grp-1", GroupRef: "g-A",
	}
	ack1, err := svc.SubmitLocalDirect(req)
	if err != nil || ack1.Dup {
		t.Fatalf("首次提交应非 dup: ack=%+v err=%v", ack1, err)
	}
	waitTerminal(t, led, ack1.TaskID)

	req2 := req
	req2.GroupRef = "g-B"
	ack2, err := svc.SubmitLocalDirect(req2)
	if err != nil || ack2.Dup {
		t.Fatalf("换群重试应落新任务（非 dup 非 conflict）: ack=%+v err=%v", ack2, err)
	}
	if ack2.TaskID == ack1.TaskID {
		t.Fatalf("换群重试应为新 task_id, got 同一 %s", ack2.TaskID)
	}
	e2 := waitTerminal(t, led, ack2.TaskID)
	if e2.Task.GroupRef != "g-B" {
		t.Fatalf("新任务应携带新群引用 g-B, got %q", e2.Task.GroupRef)
	}
}

// TestSubmitLocalDirectConcurrentIdempotent 并发幂等（复审 #8）：N 个 goroutine
// 并发同键同内容提交 → 恰 1 个非 dup 新任务、其余全 dup 命中同一任务、runner
// 执行次数恰 1（AcceptOriginIdempotent 单写事务内重检消除 TOCTOU）。
func TestSubmitLocalDirectConcurrentIdempotent(t *testing.T) {
	cr := &countingRunner{}
	svc, led := newConcurrentFixture(t, cr)
	const n = 8
	req := protocol.TaskSubmitRequest{
		TargetNode: "nodeX", TargetAgent: "cnt", SubmittedBy: "prime",
		Input: protocol.TaskInput{Args: []string{"same"}}, IdemKey: "race-1",
	}
	type outcome struct {
		ack *protocol.SubmitAckPayload
		err error
	}
	results := make([]outcome, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ack, err := svc.SubmitLocalDirect(req)
			results[i] = outcome{ack: ack, err: err}
		}(i)
	}
	wg.Wait()

	winner := ""
	fresh, dup := 0, 0
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("同键同内容并发不得报错: %v", r.err)
		}
		if r.ack.Dup {
			dup++
			if winner != "" && r.ack.TaskID != winner {
				t.Fatalf("dup 应命中同一任务: %s vs %s", r.ack.TaskID, winner)
			}
		} else {
			fresh++
			if winner != "" {
				t.Fatalf("非 dup 新任务应恰 1 个, got ≥2: %s / %s", winner, r.ack.TaskID)
			}
			winner = r.ack.TaskID
		}
	}
	if fresh != 1 || dup != n-1 {
		t.Fatalf("应恰 1 新任务 + %d dup, got fresh=%d dup=%d", n-1, fresh, dup)
	}
	waitTerminal(t, led, winner)
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.n != 1 {
		t.Fatalf("runner 应恰执行 1 次, got %d", cr.n)
	}
}

// TestSubmitLocalDirectConcurrentConflict 并发同键异内容（复审 #8）：恰 1 成功、
// 其余全部 conflict（事务内查重，不得双写）。
func TestSubmitLocalDirectConcurrentConflict(t *testing.T) {
	svc, led := newConcurrentFixture(t, &countingRunner{})
	const n = 8
	acks := make([]*protocol.SubmitAckPayload, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			acks[i], errs[i] = svc.SubmitLocalDirect(protocol.TaskSubmitRequest{
				TargetNode: "nodeX", TargetAgent: "cnt", SubmittedBy: "prime",
				Input: protocol.TaskInput{Args: []string{"content-" + strconv.Itoa(i)}}, IdemKey: "race-2",
			})
		}(i)
	}
	wg.Wait()

	ok, conflict, winner := 0, 0, ""
	for i := range errs {
		if errs[i] == nil {
			ok++
			winner = acks[i].TaskID
			continue
		}
		var re *RemoteError
		if !asRemoteError(errs[i], &re) || re.Code != "conflict" {
			t.Fatalf("异内容并发应 conflict, got %v", errs[i])
		}
		conflict++
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("应恰 1 成功 + %d conflict, got ok=%d conflict=%d", n-1, ok, conflict)
	}
	waitTerminal(t, led, winner)
}
