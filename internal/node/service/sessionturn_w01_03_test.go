package service

// sessionturn_w01_03_test.go W01-03 取消任务执行链（docs/tasks/v3/W01-03-rfc.md
// §6.2）：hub 下发取消任务（SessionTurnRef.Op=cancel + CancelTurnSeq）经 execute
// 直调 sessionop.CancelTurn——执行取消证据分类记账（executed）、调度旁路（并发
// 槽=1 且被取消轮持槽时不自引用死锁）、未知轮如实 turn_not_found。fake CLI，绝不
// 真实模型/网络。

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// execCancelTask 经 execute 投递取消任务（hub 路径的 node 侧形态：任务入账后交
// 调度——本测试直接驱动 execute，与 offer 链入账后的执行同口径）。
func execCancelTask(f *sessOpFixture, sessionID string, seq int64) protocol.Task {
	f.t.Helper()
	now := protocol.NowUTC()
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(),
		TargetNode: "n1", TargetAgent: "coder",
		SubmittedBy: "admin", IdemKey: fmt.Sprintf("scancel:%s:%d", sessionID, seq),
		Status: protocol.StatusSubmitted, CreatedAt: now, UpdatedAt: now,
		ExpireAt: now.Add(protocol.TaskRetention),
		Session: &protocol.SessionTurnRef{
			SessionID: sessionID, TurnSeq: 0,
			Op:            protocol.SessionTurnOpCancel,
			CancelTurnSeq: seq,
		},
	}
	if err := f.led.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		f.t.Fatalf("取消任务入账: %v", err)
	}
	f.svc.execute(task)
	return task
}

// waitCancelTaskFinished 等取消任务落终态并返回结果。
func waitCancelTaskFinished(f *sessOpFixture, taskID string) protocol.TaskResult {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := f.led.Get(taskID); ok && e.State == ledger.StateFinished && e.Result != nil {
			return *e.Result
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("10s 内取消任务未落终态 task=%s", taskID)
	return protocol.TaskResult{}
}

// TestW0103CancelTaskExecutesAndBypasses 执行取消任务：并发槽=1 且被取消轮正持
// 槽（屏障阻塞中），取消任务仍立即执行（调度旁路）——回执 executed、目标轮落
// cancelled/turn_cancelled_executed、无孤儿（runner 组回收）。
func TestW0103CancelTaskExecutesAndBypasses(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{concurrency: 1, envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	createAck := f.submitCreate(f.cwd) // create 秒完（屏障仅续轮）
	// 长跑续轮：屏障阻塞（持并发槽与 cwd 锁）
	turnAck, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: createAck.SessionID, Prompt: "长轮"})
	if err != nil {
		t.Fatalf("长轮受理: %v", err)
	}
	f.waitRunLogLine("start", 2) // fake CLI 已 spawn（create+长轮）
	// 取消任务（若不旁路：并发槽被长轮占满 → 取消任务排队等槽 → 死锁至屏障超时）
	task := execCancelTask(f, createAck.SessionID, turnAck.TurnSeq)
	res := waitCancelTaskFinished(f, task.ID)
	if res.Status != protocol.ResultCancelled || !strings.HasPrefix(res.Error, "turn_cancelled_executed") {
		t.Fatalf("取消任务回执应 executed: %+v", res)
	}
	// 目标轮终态：cancelled + 记账码（取消分类由执行 goroutine 依内核证据记账）
	f.waitTurnFinished(turnAck.SessionID, turnAck.TurnSeq, protocol.ResultCancelled, 5*time.Second)
	e, ok := f.led.Get(turnAck.TaskID)
	if !ok || e.Result == nil || !strings.HasPrefix(e.Result.Error, "turn_cancelled_executed") {
		t.Fatalf("目标轮应取消记账: ok=%v err=%v", ok, e.Result)
	}
}

// TestW0103CancelTaskUnknownTurn 取消任务对未知轮：回执 turn_not_found 如实失败
//（hub 侧兜底 CancelTask 的触发依据，RFC §6.2 路径 3 兜底）。
func TestW0103CancelTaskUnknownTurn(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{})
	createAck := f.submitCreate(f.cwd)
	task := execCancelTask(f, createAck.SessionID, 99)
	res := waitCancelTaskFinished(f, task.ID)
	if res.Status != protocol.ResultFailed || !strings.HasPrefix(res.Error, "turn_not_found") {
		t.Fatalf("未知轮回执应 turn_not_found: %+v", res)
	}
}

// TestW0103CancelTaskZeroSeqRejected cancel 轮缺 CancelTurnSeq：input_invalid
//（协议防御位）。
func TestW0103CancelTaskZeroSeqRejected(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{})
	createAck := f.submitCreate(f.cwd)
	now := protocol.NowUTC()
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(),
		TargetNode: "n1", TargetAgent: "coder", SubmittedBy: "admin",
		Status: protocol.StatusSubmitted, CreatedAt: now, UpdatedAt: now,
		ExpireAt: now.Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: createAck.SessionID, TurnSeq: 0, Op: protocol.SessionTurnOpCancel},
	}
	if err := f.led.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	f.svc.execute(task)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := f.led.Get(task.ID); ok && e.State == ledger.StateFinished {
			if e.Result == nil || !strings.HasPrefix(e.Result.Error, "input_invalid") {
				t.Fatalf("缺 CancelTurnSeq 应 input_invalid: %+v", e.Result)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("5s 内取消任务未落终态")
}

// dispatchOfferSessOp 以 hub 身份向节点下发 offer（sessOpFixture 版；与
// sessionSvcFixture.dispatchOffer 同型——真实 offer 链形态）。
func dispatchOfferSessOp(f *sessOpFixture, task protocol.Task) {
	f.t.Helper()
	payload, err := json.Marshal(task)
	if err != nil {
		f.t.Fatal(err)
	}
	env := &protocol.Envelope{Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", To: "n1", Payload: payload}
	if err := f.ep.Send("n1", env); err != nil {
		f.t.Fatalf("下发 offer: %v", err)
	}
}

// TestW0103HubOfferTurnCancelWhileRunning P6 复现（第三轮采证：hub 下发取消对执行
// 中轮不生效——node 侧被动等待至自然完成）：目标轮经真实 offer 链受理（handleOffer
// BeginTurn → execute BindCancel），执行中投递取消任务。预期=触发执行取消、进程组
// 终止、回执 executed/turn_cancelled_executed（进程组终止时刻断言：取消任务受理后
// 3s 内落终态——被动等待形态为 ~SLEEP 剩余时长的 turn_already_finished）。
func TestW0103HubOfferTurnCancelWhileRunning(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{envkv: map[string]string{"FAKE_SLEEP": "10"}})
	now := protocol.NowUTC()
	sid := protocol.NewSessionID()
	target := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(),
		TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "P6 长轮"}, SubmittedBy: "admin",
		Status: protocol.StatusSubmitted, CreatedAt: now, UpdatedAt: now,
		ExpireAt: now.Add(protocol.TaskRetention),
		Session: &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 1, Op: protocol.SessionTurnOpCreate,
			Cwd: f.cwd, PermissionMode: "workspace-write", Adapter: protocol.SessionAdapterCodex},
	}
	dispatchOfferSessOp(f, target)
	f.waitRunLogLine("start", 1) // fake CLI 已 spawn（10s 长跑执行中）
	time.Sleep(2 * time.Second)  // 采证同时序：启动 2s 后取消
	task := execCancelTask(f, sid, 1)
	t0 := time.Now()
	res := waitCancelTaskFinished(f, task.ID)
	elapsed := time.Since(t0)
	if res.Status != protocol.ResultCancelled || !strings.HasPrefix(res.Error, "turn_cancelled_executed") {
		t.Fatalf("P6: 执行中取消应 executed/turn_cancelled_executed，得 %+v（被动等待形态=failed/turn_already_finished）", res)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("P6: 取消收口过慢（疑似被动等待，未触发进程组终止）: %v", elapsed)
	}
	f.waitTurnFinished(sid, 1, protocol.ResultCancelled, 5*time.Second)
}
