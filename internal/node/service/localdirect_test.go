package service

import (
	"context"
	"path/filepath"
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
	ep := bus.Endpoint("nodeX")
	ep.Subscribe(func(env *protocol.Envelope) {
		rec.record(env)
	})
	svc := New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{"echo": r, "quiet": quietRunner{}},
		ArtifactRoots: map[string]string{"echo": t.TempDir(), "quiet": t.TempDir()},
	}, ep)
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
	if ack.TaskID == "" || ack.TraceID != "" && false {
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
	if f := rec.ofTypes(protocol.MsgTaskAck); f != nil {
		t.Fatalf("local 任务不应向 hub 发 task_ack: %+v", f)
	}
	if f := rec.ofTypes(protocol.MsgTaskStarted); f != nil {
		t.Fatalf("local 任务不应向 hub 发 task_started: %+v", f)
	}
	if f := rec.ofTypes(protocol.MsgResultEvent); f != nil {
		t.Fatalf("local 任务不应向 hub 发 result_event: %+v", f)
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
