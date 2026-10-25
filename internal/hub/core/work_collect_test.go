package core

// work_collect_test.go W3 收集/关闭 + 清理补全（TESTPLAN-W3-work 核心验收 1/2/3
// 与支撑用例）：部分报告不误关闭、blocked 无死环、done 后不可再派、任务清理后
// 工作单仍可解释、终态 Work 30 天保留清理、列表摘要 done_steps。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// collectReq 收集请求捷径（report/outcome 按需覆盖）。
func collectReq(workID, term string) WorkCollectRequest {
	return WorkCollectRequest{WorkID: workID, PrimeTerm: term, Report: "逐步骤结果：全部按计划完成，结论成立。"}
}

// completeAllSteps 三步全派全完成（步骤 3 无 DAG 门控，直接派发合法——W2 边界）。
func completeAllSteps(t *testing.T, f *hubFixture, node *testNode, wid, term string) []string {
	t.Helper()
	var ids []string
	for i, key := range []string{"k1", "k2", "k3"} {
		ack, err := f.core.Submit(workBindReq(wid, term, int64(i+1), key), "")
		if err != nil {
			t.Fatalf("派发步骤 %d: %v", i+1, err)
		}
		node.result(ack.TaskID, completeRes())
		ids = append(ids, ack.TaskID)
	}
	return ids
}

// TestWorkCollectGates W3 验收 1：全部步骤终态才可收集；部分报告不误关闭；
// done/abandoned 终态闭合与回执幂等。
func TestWorkCollectGates(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	// 步骤 1/2 完成、步骤 3 在途：任一未终态 → work_steps_open 零写入
	ack1, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("派发 1: %v", err)
	}
	node.result(ack1.TaskID, completeRes())
	ack2, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k2"), "")
	if err != nil {
		t.Fatalf("派发 2: %v", err)
	}
	node.result(ack2.TaskID, completeRes())
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); !errors.Is(err, ErrWorkStepsOpen) {
		t.Fatalf("步骤 3 pending 应 ErrWorkStepsOpen: %v", err)
	} else if !strings.Contains(err.Error(), "#3") {
		t.Fatalf("错误文案应列出未终态步骤号: %v", err)
	}
	w := workOf(f, wid)
	if w.Status != store.WorkStatusExecuting || w.Report != "" || w.ClosedAt != nil || w.ExpiresAt != nil {
		t.Fatalf("收集被拒零写入: status=%s report=%q", w.Status, w.Report)
	}
	if len(w.Events) == 0 || w.Events[len(w.Events)-1].Kind == WorkEventCollect {
		t.Fatal("收集被拒不得记账")
	}
	// 在途 submitted 同拒
	ack3, err := f.core.Submit(workBindReq(wid, "prime-1", 3, "k3"), "")
	if err != nil {
		t.Fatalf("派发 3: %v", err)
	}
	seqInFlight := workOf(f, wid).EventSeq
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); !errors.Is(err, ErrWorkStepsOpen) || !strings.Contains(err.Error(), "submitted") {
		t.Fatalf("在途步骤应拒并指明现状: %v", err)
	}
	if workOf(f, wid).EventSeq != seqInFlight {
		t.Fatal("在途拒收零记账")
	}
	node.result(ack3.TaskID, completeRes())

	// 全 done → 收集关闭 done：单事务盖章（closed_at/expires_at=now+30d/报告/事件）
	_, dup, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp")
	if err != nil || dup {
		t.Fatalf("收集应成功: err=%v dup=%v", err, dup)
	}
	w = workOf(f, wid)
	if w.Status != store.WorkStatusDone || w.Report == "" || w.ClosedAt == nil || w.ExpiresAt == nil {
		t.Fatalf("done 终态盖章: status=%s closed=%v expires=%v", w.Status, w.ClosedAt, w.ExpiresAt)
	}
	if w.ExpiresAt.Sub(*w.ClosedAt) != WorkTerminalRetention {
		t.Fatalf("保留期应 30 天: %v", w.ExpiresAt.Sub(*w.ClosedAt))
	}
	last := w.Events[len(w.Events)-1]
	if last.Kind != WorkEventCollect || last.By != "prime-1" || last.Principal != "pat:mcp" {
		t.Fatalf("collect 事件主体双字段: %+v", last)
	}
	seqDone := w.EventSeq

	// 相同请求重放 → dup 回执零记账；报告不同/任期不同 → work_closed 不误认 dup
	if _, dup, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil || !dup {
		t.Fatalf("同参重放应 dup 回执: err=%v dup=%v", err, dup)
	}
	if w = workOf(f, wid); w.EventSeq != seqDone {
		t.Fatal("回执重放零记账")
	}
	if _, _, err := f.core.CollectWork(WorkCollectRequest{WorkID: wid, PrimeTerm: "prime-1", Report: "不同报告"}, "pat:mcp"); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("报告不同应 work_closed: %v", err)
	}
	if _, _, err := f.core.CollectWork(WorkCollectRequest{WorkID: wid, PrimeTerm: "prime-9", Report: "逐步骤结果：全部按计划完成，结论成立。"}, "pat:mcp"); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("任期不同应 work_closed 不误认 dup: %v", err)
	}
	if workOf(f, wid).EventSeq != seqDone {
		t.Fatal("终态拒绝零记账")
	}

	// —— 部分完成：failed/needs_review 不得标 done（部分报告不误关闭）——
	//（步骤 3 也送到 needs_review 终态：覆盖门先于完成性门，全终态才轮到完成性判定）
	f2, node2, wid2 := newWorkHub(t, "prime-1")
	a1, err := f2.core.Submit(workBindReq(wid2, "prime-1", 1, "p1"), "")
	if err != nil {
		t.Fatalf("派发 1: %v", err)
	}
	node2.result(a1.TaskID, completeRes())
	a2, err := f2.core.Submit(workBindReq(wid2, "prime-1", 2, "p2"), "")
	if err != nil {
		t.Fatalf("派发 2: %v", err)
	}
	node2.result(a2.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 2, Error: "boom", FinishedAt: f2.clock.Now()})
	a3, err := f2.core.Submit(workBindReq(wid2, "prime-1", 3, "p3"), "")
	if err != nil {
		t.Fatalf("派发 3: %v", err)
	}
	node2.result(a3.TaskID, protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: 0, FinishedAt: f2.clock.Now()})
	for _, outcome := range []string{"", "done"} {
		req := collectReq(wid2, "prime-1")
		req.Outcome = outcome
		if _, _, err := f2.core.CollectWork(req, "pat:mcp"); !errors.Is(err, ErrWorkStepsNotDone) {
			t.Fatalf("outcome=%q 部分完成应 ErrWorkStepsNotDone: %v", outcome, err)
		} else if !strings.Contains(err.Error(), "#2") || !strings.Contains(err.Error(), "#3") || !strings.Contains(err.Error(), "abandoned") {
			t.Fatalf("错误应列出未完成步骤并指路 abandoned: %v", err)
		}
	}
	w2 := workOf(f2, wid2)
	if w2.Status != store.WorkStatusBlocked || w2.ClosedAt != nil || w2.Events[len(w2.Events)-1].Kind == WorkEventCollect {
		t.Fatalf("部分完成拒绝零写入: status=%s last=%s", w2.Status, w2.Events[len(w2.Events)-1].Kind)
	}
	// abandoned 处置关闭：全终态即可，报告即理由
	req := collectReq(wid2, "prime-1")
	req.Outcome = "abandoned"
	req.Report = "步骤 2 失败为环境性故障，用户决策放弃剩余步骤。"
	if _, dup, err := f2.core.CollectWork(req, "pat:mcp"); err != nil || dup {
		t.Fatalf("abandoned 关闭应成功: err=%v dup=%v", err, dup)
	}
	w2 = workOf(f2, wid2)
	if w2.Status != store.WorkStatusAbandoned || w2.Report != req.Report || w2.ClosedAt == nil {
		t.Fatalf("abandoned 终态: status=%s report=%q", w2.Status, w2.Report)
	}
	last2 := w2.Events[len(w2.Events)-1]
	if last2.Kind != WorkEventCollect || !strings.Contains(last2.Body, "1/3") {
		t.Fatalf("处置事件应含终局统计: %+v", last2)
	}
	// outcome 非法
	if _, _, err := f2.core.CollectWork(WorkCollectRequest{WorkID: wid2, PrimeTerm: "prime-1", Report: "x", Outcome: "partial"}, "pat:mcp"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("非法 outcome 应 ErrInvalid: %v", err)
	}
	// report 必填
	if _, _, err := f2.core.CollectWork(WorkCollectRequest{WorkID: wid2, PrimeTerm: "prime-1"}, "pat:mcp"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 report 应 ErrInvalid: %v", err)
	}
}

// TestWorkBlockedExitsAndClosedRejected W3 验收 2：blocked 无死环（续派补救 /
// 处置放弃两出口）、无任期与旧任期收集拒绝、done 后三写入口全闭。
func TestWorkBlockedExitsAndClosedRejected(t *testing.T) {
	// 出口 A：blocked → 续派阻断步骤 → 全 done → collect → done
	f, node, wid := newWorkHub(t, "prime-1")
	a1, _ := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	node.result(a1.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, Error: "timeout", FinishedAt: f.clock.Now()})
	if w := workOf(f, wid); w.Status != store.WorkStatusBlocked {
		t.Fatalf("前置: 应 blocked: %s", w.Status)
	}
	a2, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k2"), "") // 续派（新尝试新键）
	if err != nil {
		t.Fatalf("blocked 续派应放行: %v", err)
	}
	if w := workOf(f, wid); w.Status != store.WorkStatusExecuting {
		t.Fatalf("续派解除阻断 → executing: %s", w.Status)
	}
	node.result(a2.TaskID, completeRes())
	completeAllStepsRemaining(t, f, node, wid, "prime-1", []int64{2, 3})
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("补救后收集: %v", err)
	}
	if w := workOf(f, wid); w.Status != store.WorkStatusDone {
		t.Fatalf("出口 A 应 done: %s", w.Status)
	}

	// done 后三写入口全闭（零写入）
	seqClosed := workOf(f, wid).EventSeq
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k9"), ""); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("done 后绑定派发应 ErrWorkClosed: %v", err)
	}
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "admin"); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("done 后接管应 ErrWorkClosed: %v", err)
	}
	if _, _, err := f.core.CollectWork(WorkCollectRequest{WorkID: wid, PrimeTerm: "prime-1", Report: "再收集"}, "pat:mcp"); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("done 后再收集应 ErrWorkClosed: %v", err)
	}
	if workOf(f, wid).EventSeq != seqClosed {
		t.Fatal("终态拒绝零写入")
	}

	// 出口 B：blocked → 直接处置放弃（真实阻断场景，W3 审查②：步骤 1 failed、
	// 步骤 2/3 从未派发——下游 pending 永远到不了终态，abandoned 是唯一出口，
	// 未派发步骤随关闭记为 skipped）
	f2, node2, wid2 := newWorkHub(t, "prime-1")
	b1, _ := f2.core.Submit(workBindReq(wid2, "prime-1", 1, "j1"), "")
	node2.result(b1.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, Error: "boom", FinishedAt: f2.clock.Now()})
	if w := workOf(f2, wid2); w.Status != store.WorkStatusBlocked {
		t.Fatalf("前置: 应 blocked: %s", w.Status)
	}
	// 在途步骤（submitted）时处置同样拒——先等收束
	b2, err := f2.core.Submit(workBindReq(wid2, "prime-1", 2, "j2"), "")
	if err != nil {
		t.Fatalf("派发 2: %v", err)
	}
	req := collectReq(wid2, "prime-1")
	req.Outcome = "abandoned"
	if _, _, err := f2.core.CollectWork(req, "pat:mcp"); !errors.Is(err, ErrWorkStepsOpen) {
		t.Fatalf("在途步骤处置放弃应 ErrWorkStepsOpen: %v", err)
	}
	node2.result(b2.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, Error: "boom", FinishedAt: f2.clock.Now()})
	// 步骤 1/2 failed（全终态）、步骤 3 pending：处置放弃放行，pending 随关闭记 skipped
	seqB := workOf(f2, wid2).EventSeq
	if _, dup, err := f2.core.CollectWork(req, "pat:mcp"); err != nil || dup {
		t.Fatalf("出口 B 处置关闭（含 pending 步骤）: err=%v dup=%v", err, dup)
	}
	w2 := workOf(f2, wid2)
	if w2.Status != store.WorkStatusAbandoned || w2.ClosedAt == nil {
		t.Fatalf("出口 B 应 abandoned: %s", w2.Status)
	}
	if w2.Steps[0].Status != store.StepStatusFailed || w2.Steps[1].Status != store.StepStatusFailed || w2.Steps[2].Status != store.StepStatusSkipped {
		t.Fatalf("未派发步骤应随关闭记 skipped: %+v", w2.Steps)
	}
	lastB := w2.Events[len(w2.Events)-1]
	if lastB.Kind != WorkEventCollect || !strings.Contains(lastB.Body, "0/3") || !strings.Contains(lastB.Body, "1 步未执行随关闭跳过") {
		t.Fatalf("处置事件应含终局统计与跳过计数: %+v", lastB)
	}
	skipEv := w2.Events[len(w2.Events)-2]
	if skipEv.Kind != WorkEventStepUpdated || !strings.Contains(skipEv.Body, "#3") || !strings.Contains(skipEv.Body, "skipped") {
		t.Fatalf("跳过步骤应落 step_updated 事件: %+v", skipEv)
	}
	if n := w2.EventSeq - seqB; n != 2 {
		t.Fatalf("关闭应恰记 2 条（跳过+收集）: %d", n)
	}

	// 出口 B'：needs_review 阻断（提示词禁重跑）+ 下游 pending —— 处置放弃合法
	f5, node5, wid5 := newWorkHub(t, "prime-1")
	n1, _ := f5.core.Submit(workBindReq(wid5, "prime-1", 1, "q1"), "")
	node5.result(n1.TaskID, protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: 0, FinishedAt: f5.clock.Now()})
	if w := workOf(f5, wid5); w.Status != store.WorkStatusBlocked || w.Steps[0].Status != store.StepStatusNeedsReview {
		t.Fatalf("前置: 应 blocked+needs_review: %s/%s", w.Status, w.Steps[0].Status)
	}
	reqN := collectReq(wid5, "prime-1")
	reqN.Outcome = "abandoned"
	reqN.Report = "步骤 1 结果待人工核对（禁重跑），等待决策期间整单处置放弃。"
	if _, _, err := f5.core.CollectWork(reqN, "pat:mcp"); err != nil {
		t.Fatalf("needs_review 阻断场景处置放弃应放行: %v", err)
	}
	if w := workOf(f5, wid5); w.Status != store.WorkStatusAbandoned ||
		w.Steps[0].Status != store.StepStatusNeedsReview || w.Steps[1].Status != store.StepStatusSkipped {
		t.Fatalf("needs_review 步骤保持原状、pending 记 skipped: %+v", w.Steps)
	}

	// 任期门：无任期先拒（work_no_term）；旧任期收集拒绝（编排写入同派发规）
	f4, _, wid3 := newWorkHub(t, "") // 建单未设任期
	if _, _, err := f4.core.CollectWork(collectReq(wid3, ""), "pat:mcp"); !errors.Is(err, ErrWorkNoTerm) {
		t.Fatalf("无任期收集应 ErrWorkNoTerm: %v", err)
	}
	f3, _, wid4 := newWorkHub(t, "prime-1")
	if _, _, err := f3.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid4, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "admin"); err != nil {
		t.Fatalf("接管: %v", err)
	}
	seqT := workOf(f3, wid4).EventSeq
	if _, _, err := f3.core.CollectWork(collectReq(wid4, "prime-1"), "pat:mcp"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("旧任期收集应 ErrWorkTermConflict: %v", err)
	}
	if workOf(f3, wid4).EventSeq != seqT {
		t.Fatal("旧任期拒收零记账")
	}
}

// completeAllStepsRemaining 依次派发并完成剩余步骤（W3 测试辅助）。
func completeAllStepsRemaining(t *testing.T, f *hubFixture, node *testNode, wid, term string, seqs []int64) {
	t.Helper()
	for i, seq := range seqs {
		ack, err := f.core.Submit(workBindReq(wid, term, seq, "r"+term+string(rune('a'+i))), "")
		if err != nil {
			t.Fatalf("派发步骤 %d: %v", seq, err)
		}
		node.result(ack.TaskID, completeRes())
	}
}

// TestWorkSurvivesTaskCleanup W3 验收 3 前半：任务 24h 清理后工作单仍是可解释的
// 执行记忆（步骤/尝试/事件/报告全量可读 + tasks_gone 注解）。
func TestWorkSurvivesTaskCleanup(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")
	taskIDs := completeAllSteps(t, f, node, wid, "prime-1")
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("收集: %v", err)
	}
	// 清理前 tasks_gone 为空
	if wv, ok := f.core.GetWork(wid, 0); !ok || len(wv.TasksGone) != 0 {
		t.Fatalf("清理前 tasks_gone 应空: %v %v", ok, wv.TasksGone)
	}
	// 回拨任务终态时刻过 24h → Sweep 清理任务
	err := f.st.Update(func(st *store.State) error {
		for _, id := range taskIDs {
			tk := st.Tasks[id]
			tk.UpdatedAt = f.clock.Now().Add(-protocol.TerminalRetention - time.Minute)
			st.Tasks[id] = tk
		}
		return nil
	})
	if err != nil {
		t.Fatalf("回拨: %v", err)
	}
	f.core.Sweep()
	if len(f.st.View().Tasks) != 0 {
		t.Fatalf("任务应已清理: %d", len(f.st.View().Tasks))
	}
	// 工作单仍全量可解释
	wv, ok := f.core.GetWork(wid, 0)
	if !ok {
		t.Fatal("工作单不得随任务清理")
	}
	if wv.Status != store.WorkStatusDone || wv.Report == "" || len(wv.Steps) != 3 {
		t.Fatalf("执行摘要缺失: status=%s report=%d", wv.Status, len(wv.Report))
	}
	if len(wv.Attempts) != 3 {
		t.Fatalf("尝试记录应 3 条: %d", len(wv.Attempts))
	}
	for i, a := range wv.Attempts {
		if a.Outcome != store.AttemptOutcomeDone || a.IdemKey == "" || a.ContentHash == "" || a.BaseContentHash == "" || a.TaskID != taskIDs[i] {
			t.Fatalf("尝试 %d 去重证据不全: %+v", i+1, a)
		}
	}
	if len(wv.Events) < 5 {
		t.Fatalf("事件账本应含建单/派发/回写/收集: %d", len(wv.Events))
	}
	if len(wv.TasksGone) != 3 {
		t.Fatalf("tasks_gone 应标注 3 个已清理任务: %v", wv.TasksGone)
	}
	gone := map[string]bool{}
	for _, id := range wv.TasksGone {
		gone[id] = true
	}
	for _, id := range taskIDs {
		if !gone[id] {
			t.Fatalf("tasks_gone 缺 %s", id)
		}
	}
}

// TestWorkSweepTerminalRetention W3 验收 3 后半：终态 Work 30 天保留到期清理
//（整单删+幂等指纹映射同删）；未完成 Work 永不清理。
func TestWorkSweepTerminalRetention(t *testing.T) {
	f, node, widDone := newWorkHub(t, "prime-1")
	completeAllSteps(t, f, node, widDone, "prime-1")
	if _, _, err := f.core.CollectWork(collectReq(widDone, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("收集 done 单: %v", err)
	}
	// abandoned 单（先全部步骤终态——处置关闭同样要过步骤覆盖门）
	f2, node2, widAbandoned := newWorkHub(t, "prime-1")
	completeAllSteps(t, f2, node2, widAbandoned, "prime-1")
	req := collectReq(widAbandoned, "prime-1")
	req.Outcome = "abandoned"
	req.Report = "全部执行完毕但用户决定不采用结果，处置放弃。"
	if _, _, err := f2.core.CollectWork(req, "pat:mcp"); err != nil {
		t.Fatalf("收集 abandoned 单: %v", err)
	}
	// 未完成单（同基座建不了两个 work？可以——不同 goal）与未到期单
	f3, _, widOpen := newWorkHub(t, "prime-1")
	wvOpen, _, err := f3.core.CreateWork(func() WorkCreateRequest {
		r := workCreateReq("另一个目标")
		r.PrimeTerm = "prime-1"
		return r
	}(), "pat:mcp")
	if err != nil {
		t.Fatalf("建未完成单: %v", err)
	}
	widOpen = wvOpen.ID

	// 回拨：done 单 -31 天（到期）、abandoned 单 -29 天（未到期）
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[widDone]
		old := f.clock.Now().Add(-WorkTerminalRetention - 24*time.Hour)
		w.ClosedAt = &old
		exp := old.Add(WorkTerminalRetention)
		w.ExpiresAt = &exp
		st.Works[widDone] = w
		return nil
	}); err != nil {
		t.Fatalf("回拨: %v", err)
	}
	// 把 abandoned/open 单搬进同一基座不可行（三 fixture 独立）——分基座各自验证：
	// 基座 f：done 到期单 + 未完成单（widOpen 在 f3，先验证 f 的清理）
	f.core.Sweep()
	if _, ok := f.st.View().Works[widDone]; ok {
		t.Fatal("到期 done 单应被清理")
	}
	for fp, widMap := range f.st.View().WorkIdems {
		if widMap == widDone {
			t.Fatalf("幂等指纹映射应同删: %s→%s", fp, widMap)
		}
	}
	// 同 goal+steps 重建 = 新单（映射已清，不走悬空分支误命中）
	wv2, dup, err := f.core.CreateWork(workCreateReq("对比双机磁盘"), "pat:mcp")
	if err != nil || dup {
		t.Fatalf("重建应新建: err=%v dup=%v", err, dup)
	}
	if wv2.ID == widDone {
		t.Fatal("重建应得新 ID")
	}
	// 基座 f2：abandoned 未到期不清
	f2.core.Sweep()
	if _, ok := f2.st.View().Works[widAbandoned]; !ok {
		t.Fatal("未到期 abandoned 单不得清理")
	}
	// 基座 f3：未完成单（无 ExpiresAt）不清
	f3.core.Sweep()
	if _, ok := f3.st.View().Works[widOpen]; !ok {
		t.Fatal("未完成单不得清理（RFC 六.3）")
	}
}

// TestWorkCollectAttemptsGate W3 审查①：同步骤并发尝试未收束不得收集——A 在途
// B 先完成时步骤呈 done，收集须拒（work_steps_open 指明在途尝试）；全部尝试终局
// 后才可关闭。关闭后迟到的任务结果不破坏终态见 TestWorkClosedWritebackImmune。
func TestWorkCollectAttemptsGate(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")
	a1, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "c1"), "")
	if err != nil {
		t.Fatalf("派发尝试 A: %v", err)
	}
	a1b, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "c1b"), "") // 同步骤并发尝试（步骤 submitted，未 done）
	if err != nil {
		t.Fatalf("派发尝试 B: %v", err)
	}
	node.result(a1.TaskID, completeRes()) // A 先落终态：步骤 → done，尝试 B 仍 submitted
	completeAllStepsRemaining(t, f, node, wid, "prime-1", []int64{2, 3})
	w := workOf(f, wid)
	if w.Steps[0].Status != store.StepStatusDone {
		t.Fatalf("前置: 步骤 1 应 done（B 未落终态不改写）: %s", w.Steps[0].Status)
	}
	seqGate := w.EventSeq
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); !errors.Is(err, ErrWorkStepsOpen) ||
		!strings.Contains(err.Error(), "尝试") || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("步骤全终态但尝试 B 在途应拒收并指明尝试: %v", err)
	}
	if w = workOf(f, wid); w.EventSeq != seqGate || w.Report != "" || w.ClosedAt != nil {
		t.Fatalf("收束门拒收零写入: seq=%d report=%q", w.EventSeq, w.Report)
	}
	node.result(a1b.TaskID, completeRes()) // B 终局 → 全部尝试收束
	if w = workOf(f, wid); w.Attempts[1].Outcome != store.AttemptOutcomeDone || w.Steps[0].Status != store.StepStatusDone {
		t.Fatalf("尝试 B 回写: %+v step=%s", w.Attempts[1], w.Steps[0].Status)
	}
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("全收束后收集: %v", err)
	}
	if w := workOf(f, wid); w.Status != store.WorkStatusDone {
		t.Fatalf("应 done: %s", w.Status)
	}
}

// TestWorkClosedWritebackImmune W3 审查①：关闭后迟到的任务结果不破坏终态——
// writebackWorkTerminal 与恢复扫描对终态单（done/abandoned）零写入（防御纵深：
// collect 的全尝试收束门使正常流程不可达，异常数据/迟到结果不得改写 30 天存档）。
func TestWorkClosedWritebackImmune(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")
	taskIDs := completeAllSteps(t, f, node, wid, "prime-1")
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("收集: %v", err)
	}
	before := workOf(f, wid)
	// 模拟迟到回写：已完成任务的事实被改写为 failed（异常/迟到数据）后走回写路径
	if err := f.st.Update(func(st *store.State) error {
		tk := st.Tasks[taskIDs[0]]
		tk.Status = protocol.StatusFailed
		st.Tasks[taskIDs[0]] = tk
		return nil
	}); err != nil {
		t.Fatalf("构造迟到事实: %v", err)
	}
	tk := f.taskOf(taskIDs[0])
	writebackWorkTerminal(f.st.View(), &tk, f.clock.Now())
	after := workOf(f, wid)
	if after.Status != store.WorkStatusDone || after.Report != before.Report ||
		after.ClosedAt == nil || !after.ClosedAt.Equal(*before.ClosedAt) ||
		after.EventSeq != before.EventSeq || len(after.Events) != len(before.Events) {
		t.Fatalf("迟到回写不得触碰终态单: status=%s seq=%d→%d", after.Status, before.EventSeq, after.EventSeq)
	}
	for i := range after.Steps {
		if after.Steps[i].Status != store.StepStatusDone {
			t.Fatalf("步骤不得被迟到回写改写: #%d → %s", after.Steps[i].Seq, after.Steps[i].Status)
		}
	}
	// 恢复扫描同样不补写终态单（把尝试改回 submitted 制造历史窗口形态）
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[wid]
		w.Attempts[0].Outcome = store.AttemptOutcomeSubmitted
		st.Works[wid] = w
		return nil
	}); err != nil {
		t.Fatalf("构造恢复窗口: %v", err)
	}
	f.core.recoverWorks()
	if w := workOf(f, wid); w.Attempts[0].Outcome != store.AttemptOutcomeSubmitted || w.EventSeq != before.EventSeq {
		t.Fatalf("恢复扫描不得补写终态单: outcome=%s seq=%d", w.Attempts[0].Outcome, w.EventSeq)
	}
}

// TestWorkListSummaryDoneSteps 支撑：列表摘要 done_steps 与终态 closed_at 指针。
func TestWorkListSummaryDoneSteps(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")
	sum := f.core.ListWorks()
	if len(sum) != 1 || sum[0].DoneSteps != 0 || sum[0].ClosedAt != nil {
		t.Fatalf("初始摘要: %+v", sum[0])
	}
	a1, _ := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	node.result(a1.TaskID, completeRes())
	sum = f.core.ListWorks()
	if sum[0].DoneSteps != 1 {
		t.Fatalf("完成 1 步后 done_steps 应 1: %d", sum[0].DoneSteps)
	}
	completeAllStepsRemaining(t, f, node, wid, "prime-1", []int64{2, 3})
	if _, _, err := f.core.CollectWork(collectReq(wid, "prime-1"), "pat:mcp"); err != nil {
		t.Fatalf("收集: %v", err)
	}
	sum = f.core.ListWorks()
	if sum[0].DoneSteps != 3 || sum[0].ClosedAt == nil {
		t.Fatalf("终态摘要: done=%d closed=%v", sum[0].DoneSteps, sum[0].ClosedAt)
	}
}
