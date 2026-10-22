package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// cancelFrame 假 hub 投递 task_cancel。
func (f *frameRec) sendCancel(t *testing.T, taskID, reason string) {
	t.Helper()
	if err := f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskCancel,
		MessageID: protocol.NewMessageID(), From: "hub",
		Payload: mustJSON(protocol.TaskCancelPayload{TaskID: taskID, Reason: reason}),
	}); err != nil {
		t.Fatalf("发送 task_cancel: %v", err)
	}
}

// TestCancelBeforeOfferNotExecuted 取消先于投递（2026-10-22 修复轮2）：task_cancel
// 命中未知任务 → task_cancelled ack + 持久化墓碑；随后同 task_id 的 task_offer
// 到达 → 查墓碑拒绝执行，直接 task_result(cancelled/user_cancelled)，绝不产生
// started/执行副作用；重复 offer 走 ErrDuplicate 重发同一结果。
func TestCancelBeforeOfferNotExecuted(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})
	const id = "t-cancel-first"

	// 1) 取消未知任务：回 task_cancelled ack
	f.sendCancel(t, id, "先取消")
	if !waitFor(2*time.Second, func() bool { return len(f.ofTypes(protocol.MsgTaskCancelled)) == 1 }) {
		t.Fatalf("未见 task_cancelled ack，收帧=%v", f.ofTypes(protocol.MsgTaskCancelled))
	}
	if !f.svc.cfg.Ledger.HasCancelTombstone(id) {
		t.Fatal("取消未知任务后应落墓碑")
	}

	// 2) 同 id 的 offer 后到：不执行，直接 cancelled 终态
	f.sendOffer(t, echoTask(id))
	var result *protocol.TaskResultPayload
	if !waitFor(2*time.Second, func() bool {
		for _, env := range f.ofTypes(protocol.MsgTaskResult) {
			var p protocol.TaskResultPayload
			if json.Unmarshal(env.Payload, &p) == nil && p.TaskID == id {
				result = &p
				return true
			}
		}
		return false
	}) {
		t.Fatal("墓碑命中 offer 未回 task_result")
	}
	if result.Status != protocol.ResultCancelled || result.Result == nil ||
		result.Result.Error != protocol.CodeUserCancelled {
		t.Fatalf("应 cancelled/user_cancelled, got %+v", result)
	}
	if len(f.ofTypes(protocol.MsgTaskStarted)) != 0 {
		t.Fatal("墓碑命中任务不得产生 task_started（执行副作用）")
	}
	if len(f.ofTypes(protocol.MsgTaskAck)) != 0 {
		t.Fatal("墓碑收口不走 ack→执行链，不应有 task_ack")
	}
	e, ok := f.svc.cfg.Ledger.Get(id)
	if !ok || e.State != ledger.StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultCancelled {
		t.Fatalf("账本应 finished(cancelled), got ok=%v state=%s", ok, e.State)
	}
	if f.svc.cfg.Ledger.HasCancelTombstone(id) {
		t.Fatal("终态落盘后墓碑应已消费清除")
	}

	// 3) 重复 offer：ErrDuplicate 重发同一结果（仍不执行）
	f.sendOffer(t, echoTask(id))
	if !waitFor(2*time.Second, func() bool { return len(f.ofTypes(protocol.MsgTaskResult)) >= 2 }) {
		t.Fatal("重复 offer 应重发 task_result")
	}
	if len(f.ofTypes(protocol.MsgTaskStarted)) != 0 {
		t.Fatal("重复 offer 不得触发执行")
	}
}
