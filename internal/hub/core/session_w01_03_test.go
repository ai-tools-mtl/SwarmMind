package core

// session_w01_03_test.go W01-03 hub 契约补强（docs/tasks/v3/W01-03-rfc.md）：
// request_id 幂等三态（创建/投轮）、队列上限 16（第 17 条 409）、close 在途 409
//（收束后可关、幂等 dup 保留）、取消三路（未投递撤回 queued 细化记账 / 在途取消
// 任务异步回执回写轮行 / 终态先到保留）、cancelled 投影与双端记账码同值锁定、
// 协议加法字段 round-trip。

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestW0103CreateRequestIdempotency 创建 request_id 三态：同键同文 200 语义
//（dup=true 返原会话零写入）、同键异文 ErrSessionRequestConflict、无 request_id
// 行为零变化（每次新会话）。
func TestW0103CreateRequestIdempotency(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", false)
	// 首次创建（带 request_id）
	sv, dup, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", RequestID: "rid-1"}, "admin")
	if err != nil || dup || !strings.HasPrefix(sv.ID, "s-") {
		t.Fatalf("首次创建应新建: dup=%v err=%v", dup, err)
	}
	sid := sv.ID
	// 同键同文：dup 返原会话
	sv2, dup2, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", RequestID: "rid-1"}, "admin")
	if err != nil || !dup2 || sv2.ID != sid {
		t.Fatalf("同键同文应 dup 返原会话: sv=%s dup=%v err=%v", sv2.ID, dup2, err)
	}
	// 注册表恰一会话（零写入）
	if n := len(f.store.View().Sessions); n != 1 {
		t.Fatalf("重放零写入, 注册表=%d", n)
	}
	// 同键异文：409 request_conflict
	if _, _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/other", RequestID: "rid-1"}, "admin"); !errors.Is(err, ErrSessionRequestConflict) {
		t.Fatalf("同键异文应 ErrSessionRequestConflict, got %v", err)
	}
	// 无 request_id：每次新会话（既有行为零变化）
	sv3, dup3, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil || dup3 || sv3.ID == sid {
		t.Fatalf("无 request_id 应新建: dup=%v err=%v", dup3, err)
	}
	// 留痕字段：RequestID/PayloadHash 随会话落库
	got, ok := f.store.View().Sessions[sid]
	if !ok || got.RequestID != "rid-1" || got.PayloadHash == "" {
		t.Fatalf("创建登记留痕缺失: %+v", got)
	}
}

// TestW0103TurnRequestIdempotency 投轮 request_id 三态：同键同文回原回执
//（dup）、同键异文 409、行留痕 RequestID。
func TestW0103TurnRequestIdempotency(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "幂等轮", RequestID: "rid-t1"}, "admin")
	if err != nil || dup {
		t.Fatalf("受理: %v dup=%v", err, dup)
	}
	// 同键同文：回原回执（同 task/seq/trace）
	replay, dup2, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "幂等轮", RequestID: "rid-t1"}, "admin")
	if err != nil || !dup2 || replay.TaskID != ref.TaskID || replay.TurnSeq != ref.TurnSeq || replay.TraceID != ref.TraceID {
		t.Fatalf("同键同文应回原回执: %+v dup=%v err=%v", replay, dup2, err)
	}
	// 同键异文：409
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "另一段", RequestID: "rid-t1"}, "admin"); !errors.Is(err, ErrSessionRequestConflict) {
		t.Fatalf("同键异文应 ErrSessionRequestConflict, got %v", err)
	}
	// 行留痕
	s := f.store.View().Sessions[sid]
	if len(s.Turns) != 1 || s.Turns[0].RequestID != "rid-t1" {
		t.Fatalf("轮行 RequestID 留痕缺失: %+v", s.Turns)
	}
}

// TestW0103QueueLimit 队列上限：非终态轮达 16 后第 17 条 ErrSessionQueueFull
//（预检 + 事务内双检同断言）；收束一条后恢复受理。
func TestW0103QueueLimit(t *testing.T) {
	f, sid := newOfflineSession(t)
	// 预置：seq=1 成功 create（终态）+ seq 2..17 共 16 条非终态 send
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		s.Turns = []store.SessionTurn{{Seq: 1, NativeIDOk: true, FinishedAt: time.Now()}}
		for i := int64(2); i <= 17; i++ {
			s.Turns = append(s.Turns, store.SessionTurn{Seq: i, TaskID: "t-fill", Prompt: "x", PromptHash: promptFingerprint("x")})
		}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "第17条非终态"}, "admin"); !errors.Is(err, ErrSessionQueueFull) {
		t.Fatalf("第 17 条非终态应 ErrSessionQueueFull, got %v", err)
	}
	// 收束一条 → 恢复受理（下一序号 18）
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		for i := range s.Turns {
			if s.Turns[i].Seq == 2 {
				s.Turns[i].FinishedAt = time.Now()
			}
		}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "恢复受理"}, "admin")
	if err != nil || ref.TurnSeq != 18 {
		t.Fatalf("收束后应受理 seq=18: %+v err=%v", ref, err)
	}
}

// TestW0103CloseInFlight close 在途拒绝：非终态轮 409 close_in_flight；收束后关
// 闭成功；已 closed 幂等 dup 保留。
func TestW0103CloseInFlight(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "offline"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.core.CloseSession(sid, "admin"); !errors.Is(err, ErrSessionCloseInFlight) {
		t.Fatalf("在途 close 应 ErrSessionCloseInFlight, got %v", err)
	}
	// 会话仍 active、轮未被波及
	if s := f.store.View().Sessions[sid]; s.Status != store.SessionStatusActive {
		t.Fatalf("拒绝应零写入, status=%s", s.Status)
	}
	// 收束（轮终回写）后关闭成功
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		for i := range s.Turns {
			if s.Turns[i].Seq == ref.TurnSeq {
				s.Turns[i].FinishedAt = time.Now()
			}
		}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, dup, err := f.core.CloseSession(sid, "admin"); err != nil || dup {
		t.Fatalf("收束后关闭应成功: dup=%v err=%v", dup, err)
	}
	if _, dup, err := f.core.CloseSession(sid, "admin"); err != nil || !dup {
		t.Fatalf("重复关闭应幂等 dup: %v %v", dup, err)
	}
}

// TestW0103CancelSubmittedQueued 未投递轮取消：确定性撤回（queued 回执）、任务
// cancelled、轮行细化为 turn_cancelled_queued、投影 cancelled、重复取消幂等回执。
func TestW0103CancelSubmittedQueued(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "offline"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	rc, async, err := f.core.CancelSessionTurn(sid, ref.TurnSeq, "测试", "admin")
	if err != nil || async {
		t.Fatalf("未投递取消应同步: rc=%+v async=%v err=%v", rc, async, err)
	}
	if rc.Outcome != "queued" || rc.Status != "cancelled" || rc.Error != turnCancelQueuedCode {
		t.Fatalf("回执应 queued/cancelled/%s: %+v", turnCancelQueuedCode, rc)
	}
	// 任务 cancelled 且已撤出信箱
	task := f.store.View().Tasks[ref.TaskID]
	if task.Status != protocol.StatusCancelled {
		t.Fatalf("任务应 cancelled: %s", task.Status)
	}
	if len(f.store.View().Mailboxes["mac-mini"].Tasks) != 0 {
		t.Fatal("信箱应已撤空")
	}
	// 轮行细化：turn_cancelled_queued（非通用 cancelled）
	view, _ := f.core.GetSession(sid)
	if view.Turns[0].Error == "" || !strings.HasPrefix(view.Turns[0].Error, turnCancelQueuedCode+":") {
		t.Fatalf("轮行应细化为 %s: %q", turnCancelQueuedCode, view.Turns[0].Error)
	}
	if view.Turns[0].Status != store.TurnStatusCancelled {
		t.Fatalf("投影应 cancelled: %s", view.Turns[0].Status)
	}
	// 幂等重放
	rc2, async2, err := f.core.CancelSessionTurn(sid, ref.TurnSeq, "测试", "admin")
	if err != nil || async2 || rc2.Outcome != "queued" {
		t.Fatalf("重复取消应幂等 queued: %+v async=%v err=%v", rc2, async2, err)
	}
}

// TestW0103CancelRunningAsync 在途轮取消：取消任务投递（协议加法字段 round-trip）
// → node 回执（取消任务结果）回写轮行 → 投影 cancelled；终态轮先到保留。
func TestW0103CancelRunningAsync(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "running"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// 目标任务进入 running（已投递形态）
	if err := f.store.Update(func(st *store.State) error {
		task := st.Tasks[ref.TaskID]
		task.Status = protocol.StatusRunning
		st.Tasks[ref.TaskID] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rc, async, err := f.core.CancelSessionTurn(sid, ref.TurnSeq, "测试", "admin")
	if err != nil || !async {
		t.Fatalf("在途取消应异步: rc=%+v async=%v err=%v", rc, async, err)
	}
	if rc.Outcome != "" || rc.CancelTaskID == "" {
		t.Fatalf("异步回执应携取消任务引用: %+v", rc)
	}
	// 取消任务落库：Op=cancel + CancelTurnSeq（协议加法字段），幂等键 scancel:*
	ct := f.store.View().Tasks[rc.CancelTaskID]
	if ct.Session == nil || ct.Session.Op != protocol.SessionTurnOpCancel || ct.Session.CancelTurnSeq != ref.TurnSeq {
		t.Fatalf("取消任务身份段异常: %+v", ct.Session)
	}
	if ct.IdemKey != "scancel:"+sid+":1" {
		t.Fatalf("取消任务幂等键异常: %s", ct.IdemKey)
	}
	// 协议 round-trip：CancelTurnSeq 经 JSON 编解码保持
	raw, _ := json.Marshal(ct.Session)
	var back protocol.SessionTurnRef
	if err := json.Unmarshal(raw, &back); err != nil || back.CancelTurnSeq != ref.TurnSeq {
		t.Fatalf("CancelTurnSeq round-trip 失败: %s %v", raw, err)
	}
	// 重复取消收敛为同一取消任务（幂等 dup）
	rc2, async2, err := f.core.CancelSessionTurn(sid, ref.TurnSeq, "测试", "admin")
	if err != nil || !async2 || rc2.CancelTaskID != rc.CancelTaskID {
		t.Fatalf("重复取消应收敛同一取消任务: %+v err=%v", rc2, err)
	}
	// node 侧回执：取消任务结果（executed 记账）→ 轮行回写 + 投影 cancelled
	res := &protocol.TaskResult{Status: protocol.ResultCancelled, ExitCode: protocol.ExitCodeNotRun,
		Error: turnCancelExecutedCode + ": 本轮执行已被取消（进程组已整组终止并回收）", FinishedAt: protocol.NowUTC(),
		Turn: &protocol.TurnOutcome{TurnSeq: 0, NativeIDOk: false, WorkResult: protocol.WorkUnverified}}
	payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: rc.CancelTaskID, Status: protocol.ResultCancelled, Result: res})
	f.core.handleTaskResult(&protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult, MessageID: protocol.NewMessageID(),
		From: "mac-mini", To: "hub", Payload: payload,
	})
	view, _ := f.core.GetSession(sid)
	if !strings.HasPrefix(view.Turns[0].Error, turnCancelExecutedCode+":") || view.Turns[0].Status != store.TurnStatusCancelled {
		t.Fatalf("轮行应由取消回执落终态: %+v", view.Turns[0])
	}
	// 终态后取消：先到保留（幂等回执 executed）
	rc3, async3, err := f.core.CancelSessionTurn(sid, ref.TurnSeq, "测试", "admin")
	if err != nil || async3 || rc3.Outcome != "executed" {
		t.Fatalf("终态取消应幂等回执: %+v async=%v err=%v", rc3, async3, err)
	}
}

// TestW0103CancelFinishedTurnRejected 已自然终态轮不可取消（先到保留）。
func TestW0103CancelFinishedTurnRejected(t *testing.T) {
	f, sid := newOfflineSession(t)
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		s.Turns = []store.SessionTurn{{Seq: 1, TaskID: "t-done", NativeIDOk: true,
			WorkResult: protocol.WorkUnverified, FinishedAt: time.Now(), Prompt: "p", PromptHash: promptFingerprint("p")}}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.core.CancelSessionTurn(sid, 1, "", "admin"); !errors.Is(err, ErrSessionTurnNotCancellable) {
		t.Fatalf("已完成轮应 ErrSessionTurnNotCancellable, got %v", err)
	}
	// 未知轮 404
	if _, _, err := f.core.CancelSessionTurn(sid, 9, "", "admin"); !errors.Is(err, ErrSessionTurnNotFound) {
		t.Fatalf("未知轮应 ErrSessionTurnNotFound, got %v", err)
	}
}

// TestW0103CancelUnknownCodeFailed 待核对记账码投影 failed（绝不记成功）。
func TestW0103ProjectCancelCodes(t *testing.T) {
	f, sid := newOfflineSession(t)
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		s.Turns = []store.SessionTurn{
			{Seq: 1, TaskID: "t-q", FinishedAt: time.Now(), Error: turnCancelQueuedCode + ": x"},
			{Seq: 2, TaskID: "t-e", FinishedAt: time.Now(), Error: turnCancelExecutedCode + ": x"},
			{Seq: 3, TaskID: "t-u", FinishedAt: time.Now(), Error: turnCancelUnknownCode + ": x"},
			{Seq: 4, TaskID: "t-f", FinishedAt: time.Now(), Error: session_unsupported_code + ": x"},
		}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, _ := f.core.GetSession(sid)
	want := []string{store.TurnStatusCancelled, store.TurnStatusCancelled, store.TurnStatusFailed, store.TurnStatusFailed}
	for i, w := range want {
		if view.Turns[i].Status != w {
			t.Fatalf("seq=%d 投影应 %s, got %s", i+1, w, view.Turns[i].Status)
		}
	}
}

// session_unsupported_code 与 node 侧 sessionErrUnsupported 同值的跨端契约字面
//（本文件投影用例引用；与 turnCancel*Code 同域）。
const session_unsupported_code = "session_unsupported"

// TestW0103BookingCodeParity 双端记账码同值锁定（hub 不 import node 包，以字面
// 常量对齐；本用例即对齐契约，漂移即红）。
func TestW0103BookingCodeParity(t *testing.T) {
	if turnCancelQueuedCode != sessionop.CodeCancelQueued ||
		turnCancelExecutedCode != sessionop.CodeCancelExecuted ||
		turnCancelUnknownCode != sessionop.CodeCancelOutcomeUnknown {
		t.Fatalf("双端取消记账码漂移: hub=(%s,%s,%s) node=(%s,%s,%s)",
			turnCancelQueuedCode, turnCancelExecutedCode, turnCancelUnknownCode,
			sessionop.CodeCancelQueued, sessionop.CodeCancelExecuted, sessionop.CodeCancelOutcomeUnknown)
	}
	if protocol.SessionQueueLimit != sessionop.DefaultQueueLimit {
		t.Fatalf("队列上限双端漂移: hub=%d node=%d", protocol.SessionQueueLimit, sessionop.DefaultQueueLimit)
	}
}

// TestW0103CancelTaskBypassSemantics 取消任务投递形态：不占轮次号、目标会话行
// 不被改写（注册表状态/轮次账本不动，RFC §6.2）、取消任务入目标 node 信箱。
func TestW0103CancelTaskBypassSemantics(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "running"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(func(st *store.State) error {
		task := st.Tasks[ref.TaskID]
		task.Status = protocol.StatusRunning
		st.Tasks[ref.TaskID] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rc, _, err := f.core.CancelSessionTurn(sid, 1, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	s := f.store.View().Sessions[sid]
	if s.Status != store.SessionStatusActive || len(s.Turns) != 1 || s.Turns[0].Terminal() {
		t.Fatalf("取消任务不得改写会话注册表: status=%s turns=%+v", s.Status, s.Turns)
	}
	// 目标轮任务不被取消（cancel 是轮级意图，任务终态由 node 结果链落）
	if task := f.store.View().Tasks[ref.TaskID]; task.Status != protocol.StatusRunning {
		t.Fatalf("目标轮任务不得被本地改写: %s", task.Status)
	}
	// 取消任务在目标 node 信箱
	inMailbox := false
	for _, id := range f.store.View().Mailboxes["mac-mini"].Tasks {
		if id == rc.CancelTaskID {
			inMailbox = true
		}
	}
	if !inMailbox {
		t.Fatal("取消任务应入目标 node 信箱")
	}
}
