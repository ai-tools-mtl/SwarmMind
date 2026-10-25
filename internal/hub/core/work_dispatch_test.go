package core

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// workBindReq 绑定派发请求捷径：目标 mac-mini/diskcheck（步骤 1/2 的声明目标）。
func workBindReq(workID, term string, step int64, idem string) protocol.TaskSubmitRequest {
	return protocol.TaskSubmitRequest{
		TargetNode:  "mac-mini",
		TargetAgent: "diskcheck",
		Input:       protocol.TaskInput{Args: []string{"run"}},
		SubmittedBy: "tester",
		IdemKey:     idem,
		WorkRef:     workID,
		StepSeq:     step,
		PrimeTerm:   term,
	}
}

// newWorkHub 带工作单的测试基座：注册 mac-mini（diskcheck+reporter）并以 term
// 建单（三步计划，3 依赖 1、2）。返回 (基座, node 桩, 工作单 ID)。
func newWorkHub(t *testing.T, term string) (*hubFixture, *testNode, string) {
	t.Helper()
	f := newHub(t)
	node := f.newNode("mac-mini", agentsOf("diskcheck", "reporter"), true)
	wv, _, err := f.core.CreateWork(func() WorkCreateRequest {
		req := workCreateReq("对比双机磁盘")
		req.PrimeTerm = term
		return req
	}(), "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	return f, node, wv.ID
}

// workOf 从 store 取工作单副本（断言辅助）。
func workOf(f *hubFixture, workID string) store.Work {
	t := f.t
	t.Helper()
	w, ok := f.st.View().Works[workID]
	if !ok {
		t.Fatalf("工作单 %s 不存在", workID)
	}
	return w
}

// TestWorkTakeoverAtomicSingleWinner W2 验收 1：同刻两接管者仅一方成功（CAS）；
// 相同操作重放不新增尝试/事件。
func TestWorkTakeoverAtomicSingleWinner(t *testing.T) {
	f, _, wid := newWorkHub(t, "prime-1")

	// 先绑一次派发（接管事件体含尝试计数的事实断言用）
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), ""); err != nil {
		t.Fatalf("绑定派发: %v", err)
	}

	// CAS：expected 不符 → 拒绝零写入
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-9", NewTerm: "prime-2"}, "admin"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("expected 不符应 ErrWorkTermConflict: %v", err)
	}
	if w := workOf(f, wid); w.PrimeTerm != "prime-1" || len(w.Events) != 2 {
		t.Fatalf("CAS 失败不得改写: term=%s events=%d", w.PrimeTerm, len(w.Events))
	}

	// 正常接管：prime-1 → prime-2（事件 by=新任期自报、principal=服务端主体）
	wv, dup, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "pat:taker")
	if err != nil || dup {
		t.Fatalf("接管应成功: err=%v dup=%v", err, dup)
	}
	if wv.PrimeTerm != "prime-2" {
		t.Fatalf("接管后任期应为 prime-2: %s", wv.PrimeTerm)
	}
	w := workOf(f, wid)
	last := w.Events[len(w.Events)-1]
	if last.Kind != WorkEventTakeover || last.By != "prime-2" || last.Principal != "pat:taker" {
		t.Fatalf("接管事件主体双字段: %+v", last)
	}
	evSeq := w.EventSeq

	// 后至接管者（还拿着旧读档 prime-1）→ 冲突，零写入
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-3"}, "admin"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("后至接管者应被拒: %v", err)
	}
	// 相同操作重放（expected==current==new）→ dup 返现状，零新增事件、尝试不变
	wv, dup, err = f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-2", NewTerm: "prime-2"}, "pat:taker")
	if err != nil || !dup {
		t.Fatalf("重放应幂等 dup: err=%v dup=%v", err, dup)
	}
	if w = workOf(f, wid); w.EventSeq != evSeq || len(w.Attempts) != 1 {
		t.Fatalf("重放零记账: seq=%d attempts=%d", w.EventSeq, len(w.Attempts))
	}

	// 原请求重放（expected=T1,new=T2——首次成功响应丢失的形态，审查#5）→ 成功
	// 回执 dup 返现状，零新增事件
	wv, dup, err = f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "pat:taker")
	if err != nil || !dup || wv.PrimeTerm != "prime-2" {
		t.Fatalf("原请求重放应返成功回执: err=%v dup=%v term=%s", err, dup, wv.PrimeTerm)
	}
	if w = workOf(f, wid); w.EventSeq != evSeq {
		t.Fatalf("回执重放零记账: seq=%d", w.EventSeq)
	}
	// 他方以相同参数冒名重放 → 冲突不误认 dup（主体与账本接管事件不符）
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "pat:other"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("他方同参重放应冲突: %v", err)
	}

	// 任期标签复用拒绝（审查#9，ABA）：prime-1 已在历史使用集，重新接管被拒
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-2", NewTerm: "prime-1"}, "admin"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("复用历史任期标签应拒: %v", err)
	}
	if w = workOf(f, wid); w.PrimeTerm != "prime-2" || w.EventSeq != evSeq {
		t.Fatalf("复用被拒零写入: term=%s seq=%d", w.PrimeTerm, w.EventSeq)
	}
	// 换新任期仍可；此后旧请求（expected=T1,new=T2）不再是回执（任期已 T3），
	// 复用 T2 亦拒
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-2", NewTerm: "prime-3"}, "admin"); err != nil {
		t.Fatalf("换新任期应成功: %v", err)
	}
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "pat:taker"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("任期已推进后旧回执应失效: %v", err)
	}
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-3", NewTerm: "prime-2"}, "admin"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("复用 T2 应拒: %v", err)
	}

	// 并发两接管者同 expected → 恰一方成功
	f2, _, wid2 := newWorkHub(t, "prime-1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, conflictCount := 0, 0
	for _, term := range []string{"prime-a", "prime-b"} {
		wg.Add(1)
		go func(nt string) {
			defer wg.Done()
			_, _, err := f2.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid2, ExpectedTerm: "prime-1", NewTerm: nt}, "admin")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrWorkTermConflict):
				conflictCount++
			default:
				t.Errorf("并发接管意外错误: %v", err)
			}
		}(term)
	}
	wg.Wait()
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("并发接管应恰一方成功: ok=%d conflict=%d", okCount, conflictCount)
	}
	w2 := workOf(f2, wid2)
	takeovers := 0
	for _, ev := range w2.Events {
		if ev.Kind == WorkEventTakeover {
			takeovers++
		}
	}
	if takeovers != 1 || (w2.PrimeTerm != "prime-a" && w2.PrimeTerm != "prime-b") {
		t.Fatalf("并发接管账本应恰 1 条 takeover: term=%s takeovers=%d", w2.PrimeTerm, takeovers)
	}

	// new_term 空 → ErrInvalid
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-2", NewTerm: ""}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new_term 空应 ErrInvalid: %v", err)
	}
	// 未知工作单 → ErrWorkNotFound
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: "w-none", ExpectedTerm: "", NewTerm: "t"}, "admin"); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("未知单应 ErrWorkNotFound: %v", err)
	}
	// 终态单拒绝（W2 无产生终态路径，注入为 W3 防御纵深验证）
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[wid]
		w.Status = store.WorkStatusDone
		st.Works[wid] = w
		return nil
	}); err != nil {
		t.Fatalf("注入终态: %v", err)
	}
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-2", NewTerm: "prime-4"}, "admin"); !errors.Is(err, ErrWorkClosed) {
		t.Fatalf("终态单应 ErrWorkClosed: %v", err)
	}
}

// TestWorkOldTermRejectedFactsFlowBack W2 验收 2：旧任期编排写入拒绝（绑定派发），
// 其任务结果事实回流不拒；接管成功后才可续派（未建立任期须先接管）。
func TestWorkOldTermRejectedFactsFlowBack(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	// T1 绑定派发（尝试 1）
	ack1, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("T1 派发: %v", err)
	}

	// 接管 prime-1 → prime-2
	if _, _, err := f.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "admin"); err != nil {
		t.Fatalf("接管: %v", err)
	}

	// 旧任期编排写入拒绝：零任务零尝试零事件
	base := workOf(f, wid)
	beforeSeq, beforeAttempts := base.EventSeq, len(base.Attempts)
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k2"), ""); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("旧任期派发应被拒: %v", err)
	}
	if _, err := f.core.Submit(func() protocol.TaskSubmitRequest {
		r := workBindReq(wid, "prime-1", 2, "k2")
		r.PrimeTerm = "" // 任期缺失同拒
		return r
	}(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("任期缺失应 ErrInvalid: %v", err)
	}
	if w := workOf(f, wid); w.EventSeq != beforeSeq || len(w.Attempts) != beforeAttempts {
		t.Fatalf("旧任期被拒不得留痕: seq=%d attempts=%d", w.EventSeq, len(w.Attempts))
	}
	if got := len(f.st.View().Tasks); got != 1 {
		t.Fatalf("被拒派发不得落任务: tasks=%d", got)
	}

	// 旧任期的在途任务终态 → 事实照收（回写无任期校验）
	node.result(ack1.TaskID, completeRes())
	task := f.taskOf(ack1.TaskID)
	if task.Status != protocol.StatusCompleted {
		t.Fatalf("任务应 completed: %s", task.Status)
	}
	w := workOf(f, wid)
	if w.Attempts[0].Outcome != store.AttemptOutcomeDone || w.Attempts[0].TaskID != ack1.TaskID {
		t.Fatalf("旧任期任务结果应照收回写: %+v", w.Attempts[0])
	}
	if w.Steps[0].Status != store.StepStatusDone {
		t.Fatalf("步骤应 done: %s", w.Steps[0].Status)
	}
	if w.EventSeq != beforeSeq+1 {
		t.Fatalf("回写应记账一条 step_updated: seq=%d", w.EventSeq)
	}

	// 未建立任期的工作单：直接派发拒绝，接管后可续派
	f2 := newHub(t)
	f2.newNode("mac-mini", agentsOf("diskcheck"), true)
	wv2, _, err := f2.core.CreateWork(workCreateReq("无任期单"), "")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	// 声明了任期但工作单从未建立任期 → ErrWorkNoTerm（先接管，区别于任期不符）
	if _, err := f2.core.Submit(workBindReq(wv2.ID, "prime-7", 1, "k1"), ""); !errors.Is(err, ErrWorkNoTerm) {
		t.Fatalf("无任期派发应 ErrWorkNoTerm: %v", err)
	}
	// 任期声明缺失 → ErrInvalid（绑定派发必须声明任期）
	if _, err := f2.core.Submit(workBindReq(wv2.ID, "", 1, "k1"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("任期缺失应 ErrInvalid: %v", err)
	}
	if _, _, err := f2.core.TakeoverWork(WorkTakeoverRequest{WorkID: wv2.ID, ExpectedTerm: "", NewTerm: "prime-7"}, "admin"); err != nil {
		t.Fatalf("接管建立任期: %v", err)
	}
	if _, err := f2.core.Submit(workBindReq(wv2.ID, "prime-7", 1, "k1"), ""); err != nil {
		t.Fatalf("接管后应可续派: %v", err)
	}
	if w := workOf(f2, wv2.ID); w.Status != store.WorkStatusExecuting || len(w.Attempts) != 1 {
		t.Fatalf("接管后续派应登记尝试: status=%s attempts=%d", w.Status, len(w.Attempts))
	}
}

// TestWorkBoundSubmitReplayAndAttempts W2 验收 3：绑定提交原子 + 请求重试同键、
// 新执行尝试新 Attempt（多次尝试）；任务清理后同键重放命中去重证据；绑定失败零发布。
func TestWorkBoundSubmitReplayAndAttempts(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	// 绑定派发：尝试 1，原子绑定不变量（task_id 恒非空）
	ack, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("绑定派发: %v", err)
	}
	if ack.Work == nil || ack.Work.AttemptSeq != 1 || ack.Work.Outcome != store.AttemptOutcomeSubmitted || ack.Work.AttemptRef == "" {
		t.Fatalf("绑定返回: %+v", ack.Work)
	}
	w := workOf(f, wid)
	if len(w.Attempts) != 1 || w.Attempts[0].TaskID != ack.TaskID || w.Attempts[0].IdemKey != "k1" {
		t.Fatalf("尝试登记: %+v", w.Attempts)
	}
	if w.Status != store.WorkStatusExecuting || w.Steps[0].Status != store.StepStatusSubmitted {
		t.Fatalf("绑定推进: work=%s step=%s", w.Status, w.Steps[0].Status)
	}
	evSeq := w.EventSeq // work_created + attempt_recorded = 2

	// 同请求重放（同键同内容）→ dup 命中原任务，仍 1 次尝试、账本零增长；
	// 活任务 dup 响应带绑定回执（审查#10：与首次提交/task_gone 同形，TaskGone=false）
	replay, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil || !replay.Dup || replay.TaskID != ack.TaskID {
		t.Fatalf("重放应 dup 命中: err=%v dup=%v id=%s", err, replay.Dup, replay.TaskID)
	}
	if replay.Work == nil || replay.Work.WorkID != wid || replay.Work.AttemptSeq != 1 ||
		replay.Work.TaskGone || replay.Work.Outcome != store.AttemptOutcomeSubmitted {
		t.Fatalf("活任务 dup 应带绑定回执: %+v", replay.Work)
	}
	if w = workOf(f, wid); len(w.Attempts) != 1 || w.EventSeq != evSeq {
		t.Fatalf("重放零新增: attempts=%d seq=%d", len(w.Attempts), w.EventSeq)
	}

	// 同键换步骤 → 冲突（绑定入指纹；键与步骤的绑定关系是键语义的一部分）
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k1"), ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("同键换步骤应冲突: %v", err)
	}

	// 新执行尝试（同步骤新键）→ 新 Attempt seq 递增，旧尝试不改写
	ack2, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k2"), "")
	if err != nil {
		t.Fatalf("新尝试派发: %v", err)
	}
	if ack2.TaskID == ack.TaskID || ack2.Work.AttemptSeq != 2 {
		t.Fatalf("新执行尝试应新 Attempt: %+v", ack2.Work)
	}
	if w = workOf(f, wid); len(w.Attempts) != 2 || w.Attempts[0].Outcome != store.AttemptOutcomeSubmitted || w.Attempts[1].Outcome != store.AttemptOutcomeSubmitted {
		t.Fatalf("尝试记录: %+v", w.Attempts)
	}

	// 终态回写尝试 1 → 新尝试再派后同键重放（活任务 dup 不受影响）
	node.result(ack.TaskID, completeRes())
	if w = workOf(f, wid); w.Attempts[0].Outcome != store.AttemptOutcomeDone {
		t.Fatalf("尝试 1 应 done: %s", w.Attempts[0].Outcome)
	}

	// 任务清理（终态保留期后 Sweep）→ 同键重放命中去重证据：零新任务零新尝试
	f.clock.Advance(protocol.TerminalRetention + time.Minute)
	f.core.Sweep()
	if _, ok := f.core.GetTask(ack.TaskID); ok {
		t.Fatalf("任务应已被清理")
	}
	dedup, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("清理后重放: %v", err)
	}
	if !dedup.Dup || dedup.TaskID != ack.TaskID || dedup.Work == nil || !dedup.Work.TaskGone {
		t.Fatalf("去重证据命中应 task_gone: dup=%v work=%+v", dedup.Dup, dedup.Work)
	}
	if dedup.Work.Outcome != store.AttemptOutcomeDone {
		t.Fatalf("去重响应应带原尝试终局: %s", dedup.Work.Outcome)
	}
	if w = workOf(f, wid); len(w.Attempts) != 2 {
		t.Fatalf("去重命中不得新增尝试: %d", len(w.Attempts))
	}
	if got := len(f.st.View().Tasks); got != 1 {
		t.Fatalf("去重命中不得落新任务: tasks=%d", got)
	}

	// 绑定失败零发布（审查#7）：构造「未盖章预检恰好放行、绑定盖章后体积复查
	// 越限」的边界输入——真实走到绑定点之后的事务整体回滚（此前用 2MiB 输入在
	// 事务外预检即拒，没到绑定点）
	huge := bindEdgeReq(t, f.core, wid)
	before := workOf(f, wid)
	beforeTasks := len(f.st.View().Tasks)
	mbCnt, mbBytes := mailbox.Usage(f.st.View(), "mac-mini")
	if _, err := f.core.Submit(huge, ""); !errors.Is(err, protocol.ErrFrameTooLarge) {
		t.Fatalf("盖章后越限应 ErrFrameTooLarge: %v", err)
	}
	after := workOf(f, wid)
	if after.EventSeq != before.EventSeq || len(after.Attempts) != len(before.Attempts) || after.Steps[1].Status != before.Steps[1].Status || after.Status != before.Status {
		t.Fatalf("绑定后失败应整体回滚（任务/尝试/步骤/状态/事件全不变）: before=%+v after=%+v", before, after)
	}
	if got := len(f.st.View().Tasks); got != beforeTasks {
		t.Fatalf("失败派发不得落任务: tasks=%d want=%d", got, beforeTasks)
	}
	if cnt, bytes := mailbox.Usage(f.st.View(), "mac-mini"); cnt != mbCnt || bytes != mbBytes {
		t.Fatalf("失败派发不得入信箱: cnt=%d/%d bytes=%d/%d", cnt, mbCnt, bytes, mbBytes)
	}
}

// bindEdgeReq 构造「未盖章预检恰好放行、绑定盖章后越限」的边界请求（审查#7）：
// 以盖章前后 offer 信封的字节差为余量取 stdin 填充，使两者跨在 1MiB 帧上限
// 两侧，并自检两条边界成立。任务 ID/mid 均定长 hex、时钟不推进时时间串等长，
// 测量与真实提交逐字节同口径。
func bindEdgeReq(t *testing.T, c *Core, wid string) protocol.TaskSubmitRequest {
	t.Helper()
	probe := workBindReq(wid, "prime-1", 2, "k-edge")
	base := c.newTask(probe, "")
	stamped := base
	stamped.WorkRef, stamped.WorkStep, stamped.AttemptRef = wid, 2, attemptRef(wid, 2)
	lenOf := func(tp *protocol.Task) int {
		b, err := json.Marshal(offerEnvelope(tp, 1<<40))
		if err != nil {
			t.Fatalf("信封编码: %v", err)
		}
		return len(b)
	}
	delta := lenOf(&stamped) - lenOf(&base)
	if delta <= 0 {
		t.Fatalf("盖章应增字节: delta=%d", delta)
	}
	pad := protocol.MaxMessageBytes - lenOf(&base) - delta/2
	req := probe
	req.Input.Stdin = strings.Repeat("x", pad)
	// 自检：未盖章放行、盖章越限
	unstamped := c.newTask(req, "")
	if err := checkOfferSize(&unstamped); err != nil {
		t.Fatalf("未盖章应放行（构造失准 pad=%d）: %v", pad, err)
	}
	s := unstamped
	s.WorkRef, s.WorkStep, s.AttemptRef = wid, 2, attemptRef(wid, 2)
	if err := checkOfferSize(&s); err == nil {
		t.Fatalf("盖章后应越限（构造失准 delta=%d pad=%d）", delta, pad)
	}
	return req
}

// TestWorkBindingValidationMatrix 绑定准入矩阵：未知单/越界步骤/终态单/锁定步骤/
// 成对校验；失败/needs_review 步骤再派放行。
func TestWorkBindingValidationMatrix(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	if _, err := f.core.Submit(workBindReq("w-none", "prime-1", 1, "k1"), ""); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("未知单: %v", err)
	}
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 0, "k1"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("step_seq=0: %v", err)
	}
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 9, "k1"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("step_seq 越界: %v", err)
	}

	// 三参成组校验（审查#3）：缺 work_ref 只带 step_seq/prime_term → ErrInvalid，
	// 不得静默落为普通任务派发（REST/帧入口无 MCP 层防护，core 统一把关）
	partial := workBindReq("", "prime-1", 1, "k-partial")
	if _, err := f.core.Submit(partial, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("缺 work_ref 半参应 ErrInvalid: %v", err)
	}
	solo := workBindReq("", "prime-1", 0, "k-solo")
	solo.StepSeq = 0 // 只剩 prime_term
	if _, err := f.core.Submit(solo, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("孤 prime_term 应 ErrInvalid: %v", err)
	}
	if got := len(f.st.View().Tasks); got != 0 {
		t.Fatalf("半参拒绝不得落任务: %d", got)
	}
	// node 帧路径同规
	if _, err := f.core.SubmitFrame(partial, "mac-mini", "mid-p"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("node 帧半参应 ErrInvalid: %v", err)
	}

	// done 步骤再派锁定（走全链：派发→完成）
	ack, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("派发: %v", err)
	}
	node.result(ack.TaskID, completeRes())
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k9"), ""); !errors.Is(err, ErrWorkStepLocked) {
		t.Fatalf("done 步骤再派应锁定: %v", err)
	}

	// failed 步骤再派放行（新尝试）：先派步骤 2 置 failed，再以新键重派
	ackFail, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k-fail"), "")
	if err != nil {
		t.Fatalf("派发步骤 2: %v", err)
	}
	node.result(ackFail.TaskID, protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, FinishedAt: f.clock.Now()})
	if w := workOf(f, wid); w.Status != store.WorkStatusBlocked {
		t.Fatalf("failed 步骤应 blocked: %s", w.Status)
	}
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k-retry"), ""); err != nil {
		t.Fatalf("failed 步骤再派应放行: %v", err)
	}
	if w := workOf(f, wid); len(w.Attempts) != 3 || w.Steps[1].Status != store.StepStatusSubmitted || w.Status != store.WorkStatusExecuting {
		t.Fatalf("再派登记并解除 blocked: attempts=%d step=%s work=%s", len(w.Attempts), w.Steps[1].Status, w.Status)
	}

	// skipped 步骤锁定（W2 无产生路径，注入为防御纵深验证）
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[wid]
		w.Steps[2].Status = store.StepStatusSkipped
		st.Works[wid] = w
		return nil
	}); err != nil {
		t.Fatalf("注入 skipped: %v", err)
	}
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 3, "k3"), ""); !errors.Is(err, ErrWorkStepLocked) {
		t.Fatalf("skipped 步骤再派应锁定: %v", err)
	}

	// node 帧路径同链同语义（SubmitFrame 冒烟：成功绑定 + 旧任期拒绝）
	f2, node2, wid2 := newWorkHub(t, "prime-1")
	frameAck, err := f2.core.SubmitFrame(workBindReq(wid2, "prime-1", 1, "k1"), "mac-mini", "mid-1")
	if err != nil {
		t.Fatalf("node 帧绑定派发: %v", err)
	}
	if frameAck.Work == nil || frameAck.Work.WorkID != wid2 {
		t.Fatalf("node 帧绑定返回: %+v", frameAck.Work)
	}
	if len(node2.submitAcks) != 1 || node2.submitAcks[0].TaskID != frameAck.TaskID {
		t.Fatalf("node 帧应收 submit_ack: %+v", node2.submitAcks)
	}
	if _, _, err := f2.core.TakeoverWork(WorkTakeoverRequest{WorkID: wid2, ExpectedTerm: "prime-1", NewTerm: "prime-2"}, "admin"); err != nil {
		t.Fatalf("接管: %v", err)
	}
	if _, err := f2.core.SubmitFrame(workBindReq(wid2, "prime-1", 2, "k2"), "mac-mini", "mid-2"); !errors.Is(err, ErrWorkTermConflict) {
		t.Fatalf("node 帧旧任期应拒: %v", err)
	}
}

// ack2Of 取工作单指定步骤最新尝试的任务 ID（矩阵用例辅助）。
func ack2Of(f *hubFixture, wid string, step int64) string {
	f.t.Helper()
	w := workOf(f, wid)
	for i := len(w.Attempts) - 1; i >= 0; i-- {
		if w.Attempts[i].StepSeq == step {
			return w.Attempts[i].TaskID
		}
	}
	f.t.Fatalf("步骤 #%d 无尝试", step)
	return ""
}

// TestWorkTerminalWriteback W2 验收 4 正常路径：快速终态不丢 + 四映射逐项 +
// 重复回传零重复记账。
func TestWorkTerminalWriteback(t *testing.T) {
	// 快速终态：绑定后立即回传（bus 同步链，绑定事务发布后结果即到）
	f, node, wid := newWorkHub(t, "prime-1")
	ack, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k1"), "")
	if err != nil {
		t.Fatalf("派发: %v", err)
	}
	node.result(ack.TaskID, completeRes())
	w := workOf(f, wid)
	if w.Attempts[0].Outcome != store.AttemptOutcomeDone || w.Steps[0].Status != store.StepStatusDone || w.Status != store.WorkStatusExecuting {
		t.Fatalf("completed 映射: %+v status=%s", w.Attempts[0], w.Status)
	}
	baseSeq := w.EventSeq
	// 重复回传同终态 → dup ack 分支零重复记账
	node.result(ack.TaskID, completeRes())
	if w = workOf(f, wid); w.EventSeq != baseSeq || w.Attempts[0].Outcome != store.AttemptOutcomeDone {
		t.Fatalf("重复回传零记账: seq=%d outcome=%s", w.EventSeq, w.Attempts[0].Outcome)
	}
	if len(node.errs) != 0 {
		t.Fatalf("同终态重发不应冲突: %+v", node.errs)
	}

	// failed 映射（含 expire：任务超期 Sweep 置终态）
	ack2, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k2"), "")
	if err != nil {
		t.Fatalf("派发 2: %v", err)
	}
	f2 := workOf(f, wid)
	seqBefore := f2.EventSeq
	f.clock.Advance(protocol.TaskRetention + time.Minute)
	f.core.Sweep()
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusFailed {
		t.Fatalf("expire 应 failed: %s", got)
	}
	w = workOf(f, wid)
	if w.Attempts[1].Outcome != store.AttemptOutcomeFailed || w.Steps[1].Status != store.StepStatusFailed {
		t.Fatalf("failed 映射: %+v", w.Attempts[1])
	}
	if w.Status != store.WorkStatusBlocked {
		t.Fatalf("failed 应 blocked: %s", w.Status)
	}
	last := w.Events[len(w.Events)-1]
	if last.Kind != WorkEventStepUpdated || last.By != "hub" || last.Principal != "" {
		t.Fatalf("系统回写事件主体: %+v", last)
	}
	if w.EventSeq != seqBefore+1 {
		t.Fatalf("回写应恰记账一条: %d", w.EventSeq)
	}

	// needs_review 映射
	ack3, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k3"), "")
	if err != nil {
		t.Fatalf("派发 3: %v", err)
	}
	node.result(ack3.TaskID, protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: 0, FinishedAt: f.clock.Now()})
	if w = workOf(f, wid); w.Attempts[2].Outcome != store.AttemptOutcomeNeedsReview || w.Steps[1].Status != store.StepStatusNeedsReview || w.Status != store.WorkStatusBlocked {
		t.Fatalf("needs_review 映射: %+v step=%s work=%s", w.Attempts[2], w.Steps[1].Status, w.Status)
	}

	// cancelled 映射（CancelTask 首落终态同事务回写）；done 步骤（步骤 1）先验锁定
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "k4"), ""); !errors.Is(err, ErrWorkStepLocked) {
		t.Fatalf("done 步骤再派应锁定（步骤 1 已 done）: %v", err)
	}
	// 对在途步骤 2 的尝试取消（k3 已 needs_review 终态，取消不可——直接再派一步骤 2 新尝试再取消）
	ack5, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k5"), "")
	if err != nil {
		t.Fatalf("派发 5: %v", err)
	}
	if _, _, err := f.core.CancelTask(ack5.TaskID, "演练"); err != nil {
		t.Fatalf("取消: %v", err)
	}
	w = workOf(f, wid)
	if w.Attempts[3].Outcome != store.AttemptOutcomeCancelled || w.Steps[1].Status != store.StepStatusFailed {
		t.Fatalf("cancelled 映射: %+v step=%s", w.Attempts[3], w.Steps[1].Status)
	}
	// 重复取消幂等：不新增回写事件
	seqNow := workOf(f, wid).EventSeq
	if _, dup, err := f.core.CancelTask(ack5.TaskID, "演练"); err != nil || !dup {
		t.Fatalf("重复取消应 dup: %v %v", dup, err)
	}
	if workOf(f, wid).EventSeq != seqNow {
		t.Fatalf("重复取消零记账")
	}
}

// TestWorkNeedsReviewBlocksDownstreamNoRerun W2 验收 5：needs_review 阻断下游
//（沿任务级 DependsOn 现有门控），且 hub 零自动重跑；人工再派允许（决策在 Prime）。
func TestWorkNeedsReviewBlocksDownstreamNoRerun(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	// 上游：步骤 1 绑定派发 → needs_review
	up, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "up-key"), "")
	if err != nil {
		t.Fatalf("上游派发: %v", err)
	}
	node.result(up.TaskID, protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: 0, FinishedAt: f.clock.Now()})
	offersBefore := len(node.offers) // 上游自身的 offer（autoAck 桩）不计数，只看下游是否被投递
	w := workOf(f, wid)
	if w.Attempts[0].Outcome != store.AttemptOutcomeNeedsReview || w.Status != store.WorkStatusBlocked {
		t.Fatalf("上游 needs_review 回写: %+v work=%s", w.Attempts[0], w.Status)
	}

	// 下游：步骤 2 派发并声明任务级依赖上游 idem（沿现有门控）
	down, err := f.core.Submit(func() protocol.TaskSubmitRequest {
		r := workBindReq(wid, "prime-1", 2, "down-key")
		r.DependsOn = []string{"up-key"}
		return r
	}(), "")
	if err != nil {
		t.Fatalf("下游派发: %v", err)
	}
	dt := f.taskOf(down.TaskID)
	if !strings.HasPrefix(dt.BlockedReason, "upstream_failed:") {
		t.Fatalf("下游应被上游失败门控阻塞: %q", dt.BlockedReason)
	}

	// hub 零自动重跑：推进时钟 + Sweep + dispatch 后无新任务无新尝试
	f.clock.Advance(2 * time.Minute)
	f.core.Sweep()
	f.core.dispatch("mac-mini")
	if w = workOf(f, wid); len(w.Attempts) != 2 {
		t.Fatalf("hub 不得自动重跑（新尝试）: %d", len(w.Attempts))
	}
	if got := len(f.st.View().Tasks); got != 2 {
		t.Fatalf("hub 不得自动重跑（新任务）: %d", got)
	}
	if len(node.offers) != offersBefore {
		t.Fatalf("阻塞下游不得投递: offers=%d before=%d", len(node.offers), offersBefore)
	}

	// 人工再派（needs_review 步骤本身，新键）允许：决策在 Prime，blocked→executing
	again, err := f.core.Submit(workBindReq(wid, "prime-1", 1, "up-key-2"), "")
	if err != nil {
		t.Fatalf("人工再派应允许: %v", err)
	}
	w = workOf(f, wid)
	if w.Status != store.WorkStatusExecuting || len(w.Attempts) != 3 || w.Attempts[2].TaskID != again.TaskID {
		t.Fatalf("人工再派登记: work=%s attempts=%d", w.Status, len(w.Attempts))
	}
}

// TestWorkRecoverBackfillWindow W2 验收 4 恢复路径：任务已终态而尝试未回填的
// 中断窗口 → 重启恢复扫描补齐 + 幂等；全正常路径后不存在空 task_id 尝试。
func TestWorkRecoverBackfillWindow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	bus.Endpoint("hub").Subscribe(c.HandleFrame) // 帧入口（对齐 newHub 装配）
	if err := c.RegisterNode("mac-mini", agentsOf("diskcheck")); err != nil {
		t.Fatalf("注册: %v", err)
	}
	c.SetOnline("mac-mini", true)
	req := workCreateReq("恢复窗口")
	req.PrimeTerm = "prime-1"
	wv, _, err := c.CreateWork(req, "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	bindReq := workBindReq(wv.ID, "prime-1", 1, "k1")
	bindReq.TargetAgent = "diskcheck"
	ack, err := c.Submit(bindReq, "")
	if err != nil {
		t.Fatalf("绑定派发: %v", err)
	}
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: clock.Now()}
	payload := mustMarshal(t, protocol.TaskResultPayload{TaskID: ack.TaskID, Status: res.Status, Result: &res})
	if err := bus.Endpoint("mac-mini").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", From: "mac-mini", Payload: payload,
	}); err != nil {
		t.Fatalf("回传终态: %v", err)
	}
	if w := workOf(&hubFixture{t: t, st: st}, wv.ID); w.Attempts[0].Outcome != store.AttemptOutcomeDone {
		t.Fatalf("正常路径回写应已发生: %+v", w.Attempts[0])
	}
	// 注入窗口：回滚工作单侧回写（模拟回写事务前的历史/中断形态）
	if err := st.Update(func(s *store.State) error {
		w := s.Works[wv.ID]
		w.Attempts[0].Outcome = store.AttemptOutcomeSubmitted
		w.Steps[0].Status = store.StepStatusSubmitted
		w.Status = store.WorkStatusExecuting
		w.Events = w.Events[:len(w.Events)-1] // 摘掉 step_updated（work_created+attempt_recorded 保留）
		st := s
		st.Works[wv.ID] = w
		return nil
	}); err != nil {
		t.Fatalf("注入窗口: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store Close: %v", err)
	}

	// 重启 → 恢复扫描补齐 + 恢复事件
	st2, c2 := reopenHubWithClock(t, dir, clock)
	w := workOf(&hubFixture{t: t, st: st2}, wv.ID)
	if w.Attempts[0].Outcome != store.AttemptOutcomeDone || w.Steps[0].Status != store.StepStatusDone {
		t.Fatalf("恢复扫描应补齐: %+v step=%s", w.Attempts[0], w.Steps[0].Status)
	}
	last := w.Events[len(w.Events)-1]
	if last.Kind != WorkEventStepUpdated || !strings.Contains(last.Body, "恢复扫描补记") {
		t.Fatalf("恢复事件: %+v", last)
	}
	evSeq := w.EventSeq
	if err := c2.Close(); err != nil {
		t.Fatalf("c2 Close: %v", err)
	}
	_ = st2.Close()

	// 二次重启幂等：零新增事件
	st3, c3 := reopenHubWithClock(t, dir, clock)
	defer func() { _ = c3.Close(); _ = st3.Close() }()
	w = workOf(&hubFixture{t: t, st: st3}, wv.ID)
	if w.EventSeq != evSeq || w.Attempts[0].Outcome != store.AttemptOutcomeDone {
		t.Fatalf("二次重启应幂等: seq=%d outcome=%s", w.EventSeq, w.Attempts[0].Outcome)
	}

	// 不变量：正常绑定路径不产生空 task_id 尝试
	for _, ww := range st3.View().Works {
		for _, a := range ww.Attempts {
			if a.TaskID == "" {
				t.Fatalf("空 task_id 尝试（原子绑定破坏）: work=%s attempt=%d", ww.ID, a.Seq)
			}
		}
	}
}

// TestWorkAttemptEvidenceBoundary W2 审查#1（P1）：尝试去重证据的指纹与身份
// 边界——同键换内容/换 work/换步骤/换提交主体一律 conflict（非 dup），仅任期
// 不同（旧任期回执补拉）与仅群引用不同按既有口径放行；证据在任务清理后仍生效。
func TestWorkAttemptEvidenceBoundary(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")
	wv2, _, err := f.core.CreateWork(func() WorkCreateRequest {
		req := workCreateReq("第二张工作单")
		req.PrimeTerm = "prime-1"
		return req
	}(), "pat:mcp-test")
	if err != nil {
		t.Fatalf("建第二单: %v", err)
	}

	base := workBindReq(wid, "prime-1", 1, "k1")
	ack, err := f.core.Submit(base, "")
	if err != nil {
		t.Fatalf("绑定派发: %v", err)
	}
	node.result(ack.TaskID, completeRes())
	// 原任务过保留期清理：任务级指纹查重失效，此后全靠尝试去重证据
	f.clock.Advance(protocol.TerminalRetention + time.Minute)
	f.core.Sweep()
	if _, ok := f.core.GetTask(ack.TaskID); ok {
		t.Fatalf("原任务应已清理")
	}
	evSeq := workOf(f, wid).EventSeq

	// 同键换输入 → conflict（证据含内容指纹，不是 dup）
	changed := base
	changed.Input = protocol.TaskInput{Args: []string{"run", "--deep"}}
	if _, err := f.core.Submit(changed, ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("同键换内容应 conflict: %v", err)
	}
	// 同键换步骤 → conflict（键与步骤绑定关系在证据指纹内）
	if _, err := f.core.Submit(workBindReq(wid, "prime-1", 2, "k1"), ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("同键换步骤应 conflict: %v", err)
	}
	// 跨 Work 同键 → conflict（任务级指纹冲突在任务清理后由证据兜底，不得产生新执行）
	if _, err := f.core.Submit(workBindReq(wv2.ID, "prime-1", 1, "k1"), ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("跨 Work 同键应 conflict: %v", err)
	}
	// 换提交主体 → conflict（身份边界：知 Work+键+步骤的他方不得冒领原 task_id）
	impersonated := base
	impersonated.SubmittedBy = "mallory"
	if _, err := f.core.Submit(impersonated, ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("换提交主体应 conflict: %v", err)
	}

	// 同键同内容仅任期不同（旧任期回执补拉）→ dup 命中原尝试（任期不进指纹）
	oldTerm := base
	oldTerm.PrimeTerm = "prime-0"
	dup, err := f.core.Submit(oldTerm, "")
	if err != nil || !dup.Dup || dup.TaskID != ack.TaskID {
		t.Fatalf("旧任期回执补拉应 dup: err=%v dup=%+v", err, dup)
	}
	if dup.Work == nil || !dup.Work.TaskGone || dup.Work.Outcome != store.AttemptOutcomeDone || dup.Work.AttemptRef != wid+"#a1" {
		t.Fatalf("回执应带原尝试终局: %+v", dup.Work)
	}
	// 全程零新任务零新尝试零事件
	if w := workOf(f, wid); len(w.Attempts) != 1 || w.EventSeq != evSeq {
		t.Fatalf("边界冲突不得留痕: attempts=%d seq=%d", len(w.Attempts), w.EventSeq)
	}
	if got := len(f.st.View().Tasks); got != 0 {
		t.Fatalf("不得落新任务: %d", got)
	}
}

// TestWorkReceiptReplayBeatsAdmission W2 审查#2：回执补拉先于派发准入——依赖
// 已清理的重放不报 invalid_dependency，信箱/全局预算满仍能取回回执。
func TestWorkReceiptReplayBeatsAdmission(t *testing.T) {
	f, node, wid := newWorkHub(t, "prime-1")

	// 腿一：绑定派发声明依赖上游 idem，双双终态清理后重放——旧链会在
	// validateDepends（未知 idem）挡在证据查重之前
	up, err := f.core.Submit(protocol.TaskSubmitRequest{
		TargetNode: "mac-mini", TargetAgent: "diskcheck",
		Input: protocol.TaskInput{Args: []string{"seed"}}, SubmittedBy: "tester", IdemKey: "up-key",
	}, "")
	if err != nil {
		t.Fatalf("上游提交: %v", err)
	}
	bound := workBindReq(wid, "prime-1", 1, "k1")
	bound.DependsOn = []string{"up-key"}
	ack, err := f.core.Submit(bound, "")
	if err != nil {
		t.Fatalf("绑定派发: %v", err)
	}
	// 腿二的载体：无依赖的绑定任务 k2（同批清理）
	bound2 := workBindReq(wid, "prime-1", 2, "k2")
	ack2, err := f.core.Submit(bound2, "")
	if err != nil {
		t.Fatalf("绑定派发 2: %v", err)
	}
	node.result(up.TaskID, completeRes())
	node.result(ack.TaskID, completeRes())
	node.result(ack2.TaskID, completeRes())
	f.clock.Advance(protocol.TerminalRetention + time.Minute)
	f.core.Sweep()
	if _, ok := f.core.GetTask(ack.TaskID); ok {
		t.Fatalf("绑定任务应已清理")
	}

	// 依赖已清理的重放 → 回执命中（非 invalid_dependency）
	dup, err := f.core.Submit(bound, "")
	if err != nil || !dup.Dup || dup.TaskID != ack.TaskID || dup.Work == nil || !dup.Work.TaskGone {
		t.Fatalf("依赖清理后重放应回执命中: err=%v dup=%+v", err, dup.Work)
	}

	// 腿二：信箱字节计满注入 → 回执补拉仍可达（先于容量检查）
	if err := f.st.Update(func(st *store.State) error {
		mb := st.Mailboxes["mac-mini"]
		mb.Bytes = protocol.MailboxByteBudget
		st.Mailboxes["mac-mini"] = mb
		return nil
	}); err != nil {
		t.Fatalf("注入信箱满: %v", err)
	}
	dup2, err := f.core.Submit(bound2, "")
	if err != nil || !dup2.Dup || !dup2.Work.TaskGone {
		t.Fatalf("信箱满也应取回回执: err=%v work=%+v", err, dup2.Work)
	}
	// 全局预算满注入 → 同上（storage_full 不挡回执）
	f.core.totalSz = protocol.StorageBudget
	dup3, err := f.core.Submit(bound2, "")
	if err != nil || !dup3.Dup || !dup3.Work.TaskGone {
		t.Fatalf("全局预算满也应取回回执: err=%v work=%+v", err, dup3.Work)
	}

	// 全程零新任务零新尝试
	if w := workOf(f, wid); len(w.Attempts) != 2 {
		t.Fatalf("回执补拉不得新增尝试: %d", len(w.Attempts))
	}
	if got := len(f.st.View().Tasks); got != 0 {
		t.Fatalf("回执补拉不得落新任务: %d", got)
	}
}

// TestWorkRecoverOrderingAfterWindow W2 审查#6：恢复扫描按任务终态落盘顺序
// 补写（同步骤两尝试、后注册的尝试先终态——登记序补写会以旧尝试事实覆盖新终局）。
func TestWorkRecoverOrderingAfterWindow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	fx := &hubFixture{t: t, st: st}
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	if err := c.RegisterNode("mac-mini", agentsOf("diskcheck")); err != nil {
		t.Fatalf("注册: %v", err)
	}
	c.SetOnline("mac-mini", true)
	req := workCreateReq("恢复排序")
	req.PrimeTerm = "prime-1"
	wv, _, err := c.CreateWork(req, "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	// 同步骤两次尝试（并发尝试合法）：尝试 1（kA）先注册但后终态（failed），
	// 尝试 2（kB）后注册先终态（completed）
	ackA, err := c.Submit(workBindReq(wv.ID, "prime-1", 1, "kA"), "")
	if err != nil {
		t.Fatalf("派发 A: %v", err)
	}
	ackB, err := c.Submit(workBindReq(wv.ID, "prime-1", 1, "kB"), "")
	if err != nil {
		t.Fatalf("派发 B: %v", err)
	}
	clock.Advance(time.Minute)
	sendResultFrame(t, bus, "mac-mini", ackB.TaskID, protocol.ResultCompleted, clock)
	clock.Advance(time.Minute)
	sendResultFrame(t, bus, "mac-mini", ackA.TaskID, protocol.ResultFailed, clock)
	if w := workOf(fx, wv.ID); w.Steps[0].Status != store.StepStatusFailed || w.Attempts[0].Outcome != store.AttemptOutcomeFailed {
		t.Fatalf("活路径终局应以最后事实为准: step=%s a0=%s", w.Steps[0].Status, w.Attempts[0].Outcome)
	}
	// 注入窗口：两尝试回 submitted、步骤回 submitted、work 回 executing、摘掉两条 step_updated
	if err := st.Update(func(s *store.State) error {
		w := s.Works[wv.ID]
		w.Attempts[0].Outcome = store.AttemptOutcomeSubmitted
		w.Attempts[1].Outcome = store.AttemptOutcomeSubmitted
		w.Steps[0].Status = store.StepStatusSubmitted
		w.Status = store.WorkStatusExecuting
		w.Events = w.Events[:len(w.Events)-2]
		s.Works[wv.ID] = w
		return nil
	}); err != nil {
		t.Fatalf("注入窗口: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store Close: %v", err)
	}

	// 重启恢复：按终态时序（kB done 在前、kA failed 在后）补写——步骤终局
	// failed、work blocked，与活路径一致；登记序补写会错成 step=done
	st2, c2 := reopenHubWithClock(t, dir, clock)
	defer func() { _ = c2.Close(); _ = st2.Close() }()
	w := workOf(&hubFixture{t: t, st: st2}, wv.ID)
	if w.Attempts[0].Outcome != store.AttemptOutcomeFailed || w.Attempts[1].Outcome != store.AttemptOutcomeDone {
		t.Fatalf("尝试结局: %+v", w.Attempts)
	}
	if w.Steps[0].Status != store.StepStatusFailed || w.Status != store.WorkStatusBlocked {
		t.Fatalf("恢复应按终态落盘顺序收敛（failed 后到为准）: step=%s work=%s", w.Steps[0].Status, w.Status)
	}
	// 恢复事件两条且顺序与终态时序一致（先 B 后 A）
	var recovery []store.WorkEvent
	for _, ev := range w.Events {
		if strings.Contains(ev.Body, "恢复扫描补记") {
			recovery = append(recovery, ev)
		}
	}
	if len(recovery) != 2 ||
		!strings.Contains(recovery[0].Body, ackB.TaskID) || !strings.Contains(recovery[1].Body, ackA.TaskID) {
		t.Fatalf("恢复事件应按终态时序: %+v", recovery)
	}
}

// TestWorkBindStorageBudgetRecheck W2 审查#8：全局存储预算按绑定后盖章体复核
// ——预算余量卡在盖章增量中间，未盖章体预检放行、盖章体重检越限（ErrStorageFull
// 以 mutate 错误整体拒绝，绑定回写全部回滚）。
func TestWorkBindStorageBudgetRecheck(t *testing.T) {
	f, _, wid := newWorkHub(t, "prime-1")
	probe := workBindReq(wid, "prime-1", 2, "k-budget")
	probe.Input.Stdin = strings.Repeat("x", 4096)
	base := f.core.newTask(probe, "")
	stamped := base
	stamped.WorkRef, stamped.WorkStep, stamped.AttemptRef = wid, 2, attemptRef(wid, 2)
	taskDelta := mailbox.TaskBytes(&stamped) - mailbox.TaskBytes(&base)
	if taskDelta <= 0 {
		t.Fatalf("盖章应增字节: %d", taskDelta)
	}
	// 抬高全局字节缓存：余量 = 未盖章体 + 盖章增量/2（预检放行、盖章后越限）
	f.core.totalSz = protocol.StorageBudget - mailbox.TaskBytes(&base) - taskDelta/2
	before := workOf(f, wid)
	beforeTasks := len(f.st.View().Tasks)
	if _, err := f.core.Submit(probe, ""); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("盖章体超全局预算应 ErrStorageFull: %v", err)
	}
	after := workOf(f, wid)
	if after.EventSeq != before.EventSeq || len(after.Attempts) != len(before.Attempts) ||
		after.Steps[1].Status != before.Steps[1].Status || after.Status != before.Status {
		t.Fatalf("预算拒绝应整体回滚: before=%+v after=%+v", before, after)
	}
	if got := len(f.st.View().Tasks); got != beforeTasks {
		t.Fatalf("预算拒绝不得落任务: %d", got)
	}
}

// sendResultFrame 手工回传任务结果帧（裸 bus 恢复用例用，对齐 TestWorkRecoverBackfillWindow）。
func sendResultFrame(t *testing.T, bus *transport.Bus, from, taskID string, status string, clock *fakeClock) {
	t.Helper()
	res := protocol.TaskResult{Status: status, ExitCode: 0, FinishedAt: clock.Now()}
	if status == protocol.ResultFailed {
		res.ExitCode = 1
	}
	payload := mustMarshal(t, protocol.TaskResultPayload{TaskID: taskID, Status: res.Status, Result: &res})
	if err := bus.Endpoint(from).Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", From: from, Payload: payload,
	}); err != nil {
		t.Fatalf("回传终态: %v", err)
	}
}

// reopenHubWithClock 带 注入时钟的重启（恢复用例用：恢复事件时刻取注入时钟）。
func reopenHubWithClock(t *testing.T, dir string, clock *fakeClock) (*store.Store, *Core) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	c, err := New(Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	return st, c
}
