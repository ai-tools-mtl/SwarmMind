package ledger

import (
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestLedgerRecoveryAcceptedNotExpired accepted 未过期 → 继续调度，条目保持 accepted。
func TestLedgerRecoveryAcceptedNotExpired(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
	})

	l, rec := mustOpen(t, dir)
	if len(rec.Reschedule) != 1 || rec.Reschedule[0].ID != "t-1" {
		t.Fatalf("未过期 accepted 应进 Reschedule: %+v", rec.Reschedule)
	}
	if len(rec.Resend) != 0 {
		t.Errorf("未过期 accepted 不应进 Resend: %+v", rec.Resend)
	}
	if e, ok := l.Get("t-1"); !ok || e.State != StateAccepted {
		t.Errorf("条目应保持 accepted: ok=%v %+v", ok, e)
	}
}

// TestLedgerRecoveryAcceptedExpired accepted 已过期 → 不执行，写 finished(failed/expired)
// 并回传（进 Resend）；不进 Reschedule；未 acked 前每次重启都在重发队列。
func TestLedgerRecoveryAcceptedExpired(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(-time.Minute))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
	})

	l, rec := mustOpen(t, dir)
	if len(rec.Reschedule) != 0 {
		t.Errorf("已过期任务不得继续调度: %+v", rec.Reschedule)
	}
	if len(rec.Resend) != 1 {
		t.Fatalf("expired 结果应回传（进 Resend）: %+v", rec.Resend)
	}
	e := rec.Resend[0]
	if e.State != StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultFailed || e.Result.Error != protocol.CodeExpired ||
		e.Result.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("expired 降级结果不符: %+v", e)
	}
	if e.ResultAcked {
		t.Error("降级结果尚未 acked")
	}
	if got, _ := l.Get("t-1"); got.State != StateFinished {
		t.Errorf("账本条目应已写 finished: %+v", got)
	}

	// 未 acked 前再次重启：仍在重发队列（重发直至 result_ack，SPEC 4.5）
	_ = l.Close()
	_, rec2 := mustOpen(t, dir)
	if len(rec2.Resend) != 1 || rec2.Resend[0].Task.ID != "t-1" {
		t.Errorf("再次重启应仍出现在 Resend: %+v", rec2.Resend)
	}
}

// TestLedgerRecoveryStartedNeedsReview started 未 finished → 写 finished
// （status=needs_review, error=node_restarted_midway），禁止自动重新执行。
func TestLedgerRecoveryStartedNeedsReview(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkStarted("t-1"); err != nil {
			t.Fatalf("seed MarkStarted: %v", err)
		}
	})

	l, rec := mustOpen(t, dir)
	if len(rec.Reschedule) != 0 {
		t.Errorf("started 任务禁止自动重新执行，不得进 Reschedule: %+v", rec.Reschedule)
	}
	if len(rec.Resend) != 1 {
		t.Fatalf("needs_review 结果应回传（进 Resend）: %+v", rec.Resend)
	}
	e := rec.Resend[0]
	if e.State != StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultNeedsReview ||
		e.Result.Error != protocol.CodeNodeRestartedMidway ||
		e.Result.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("needs_review 降级结果不符: %+v", e)
	}
	if got, _ := l.Get("t-1"); got.State != StateFinished {
		t.Errorf("账本条目应已写 finished: %+v", got)
	}
}

// TestLedgerRecoveryFinishedUnacked finished 未 acked → 原样进重发队列，结果不丢失。
func TestLedgerRecoveryFinishedUnacked(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sampleTask("t-1", protocol.NowUTC().Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkStarted("t-1"); err != nil {
			t.Fatalf("seed MarkStarted: %v", err)
		}
		if err := l.MarkFinished("t-1", protocol.TaskResult{
			Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "原样保留", FinishedAt: protocol.NowUTC(),
		}); err != nil {
			t.Fatalf("seed MarkFinished: %v", err)
		}
	})

	_, rec := mustOpen(t, dir)
	if len(rec.Reschedule) != 0 {
		t.Errorf("finished 任务不应进 Reschedule: %+v", rec.Reschedule)
	}
	if len(rec.Resend) != 1 {
		t.Fatalf("finished 未 acked 应进 Resend: %+v", rec.Resend)
	}
	e := rec.Resend[0]
	if e.State != StateFinished || e.Result == nil || e.Result.FinalAnswer != "原样保留" {
		t.Errorf("重发条目应原样保留结果: %+v", e)
	}
}

// TestLedgerRecoveryMixed 多任务混合状态恢复：各态互不干扰（三态覆盖的合并场景）。
func TestLedgerRecoveryMixed(t *testing.T) {
	dir := t.TempDir()
	now := protocol.NowUTC()
	seedLedger(t, dir, func(l *Ledger) {
		// accepted 未过期 → Reschedule
		if err := l.Accept(sampleTask("t-live", now.Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		// accepted 已过期 → finished/failed/expired → Resend
		if err := l.Accept(sampleTask("t-exp", now.Add(-time.Minute))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		// started → finished/needs_review → Resend
		if err := l.Accept(sampleTask("t-mid", now.Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkStarted("t-mid"); err != nil {
			t.Fatalf("seed MarkStarted: %v", err)
		}
		// finished 未 acked → Resend
		if err := l.Accept(sampleTask("t-done", now.Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkFinished("t-done", protocol.TaskResult{
			Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: now,
		}); err != nil {
			t.Fatalf("seed MarkFinished: %v", err)
		}
		// finished 已 acked → 不进任何清单
		if err := l.Accept(sampleTask("t-acked", now.Add(time.Hour))); err != nil {
			t.Fatalf("seed Accept: %v", err)
		}
		if err := l.MarkFinished("t-acked", protocol.TaskResult{
			Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: now,
		}); err != nil {
			t.Fatalf("seed MarkFinished: %v", err)
		}
		if err := l.MarkResultAcked("t-acked"); err != nil {
			t.Fatalf("seed MarkResultAcked: %v", err)
		}
	})

	_, rec := mustOpen(t, dir)
	if len(rec.Reschedule) != 1 || rec.Reschedule[0].ID != "t-live" {
		t.Errorf("Reschedule 应仅含 t-live: %+v", rec.Reschedule)
	}
	if len(rec.Resend) != 3 {
		t.Fatalf("Resend 应含 t-exp/t-mid/t-done 共 3 条: %+v", rec.Resend)
	}
	got := map[string]protocol.TaskResult{}
	for _, e := range rec.Resend {
		got[e.Task.ID] = *e.Result
	}
	if r := got["t-exp"]; r.Status != protocol.ResultFailed || r.Error != protocol.CodeExpired {
		t.Errorf("t-exp 结果不符: %+v", r)
	}
	if r := got["t-mid"]; r.Status != protocol.ResultNeedsReview || r.Error != protocol.CodeNodeRestartedMidway {
		t.Errorf("t-mid 结果不符: %+v", r)
	}
	if r := got["t-done"]; r.Status != protocol.ResultCompleted || r.Error != "" {
		t.Errorf("t-done 结果不符: %+v", r)
	}
	if _, ok := got["t-acked"]; ok {
		t.Error("已 acked 的 t-acked 不应出现在 Resend")
	}
}
