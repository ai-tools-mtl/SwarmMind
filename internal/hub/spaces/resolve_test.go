// resolve_test.go 留待核对人工裁决全链测试（W09 工作块 B，docs/tasks/v3/
// W09-hardening.md 工作块 B；采证 O3「留待核对滞留」释放入口的机械闭环）：
//   - accept：人工核验通过收束（复用 ConvergeIntent 既有收敛原语）+ 串行门解锁
//     推进同空间下一棒（核心验收——O3 无限期滞留由实测闭合）；
//   - reject：人工否决收束（不重发不转投——原任务实物与轮行锚零变化）；
//   - 幂等安全：同裁决重放 dup 命中、异裁决 request_conflict 不可改写；
//   - 运行中 resolve 409 execution_in_flight（防冒认在途，指路既有取消路径）；
//   - 非 accepted/未知意图 409；
//   - 证据缺失（任务已过保留期清理）accept 可裁决；
//   - 留痕主体断言（服务端主体独立字段落账——事件 actor，非请求自报）。
//
// needs_review 终态经真 handleTaskResult 仲裁链注入（n1 端点 task_result 帧——
// 归属 TargetNode==From、缺省租约宽容放行），终态钩子分类留待核对（outcome 空）
// 意图保持 accepted——与生产「轮终不确定」形态同链同形。
package spaces

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// waitHubTaskStatus 轮询 hub 任务状态至目标值（有界条件等待——终态落盘由结果
// 仲裁事务完成，轮询只做断言同步）。
func waitHubTaskStatus(t *testing.T, f *chainFixture, taskID string, want protocol.TaskStatus) protocol.Task {
	t.Helper()
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if got, ok := f.hubCore.GetTask(taskID); ok && got.Status == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := f.hubCore.GetTask(taskID)
	t.Fatalf("任务 %s 未达 %s（现 %s ok=%v）", taskID, want, got.Status, got.Result != nil)
	return protocol.Task{}
}

// injectNeedsReview 从 n1 端点注入 needs_review 终态结果帧（真 task_result 仲裁
// 链：任务归属核验 + 缺省租约宽容 + fireTerminal 终态钩子——中心空间轮按
// classifyTaskFact 分类留待核对，意图保持 accepted）。
func injectNeedsReview(t *testing.T, f *chainFixture, taskID string) {
	t.Helper()
	pl, err := json.Marshal(protocol.TaskResultPayload{TaskID: taskID, Status: protocol.ResultNeedsReview,
		Result: &protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: -1,
			Error: protocol.CodeNodeRestartedMidway, FinalAnswer: "轮终不确定（注入）"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.bus.Endpoint("n1").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: "tr-" + protocol.NewMessageID(), To: "hub", Payload: pl,
	}); err != nil {
		t.Fatalf("注入 needs_review 结果帧: %v", err)
	}
}

// makeAwaitingReview 铺设「留待核对（轮终不确定）」形态：barrier 夹具派发 → 等
// node 侧进入执行（账本越过 accepted）→ 注入 needs_review 终态 → 断言任务落
// needs_review、意图保持 accepted（留待核对不收敛）。waitNodeDone=true 时再放行
// node 侧执行并等其完成收尾（真结果回 hub 被 result_conflict 拒收——保留先到
// needs_review，node 停止重传账本 Closed）：后续还要向同一会话提交新轮的用例
// （accept/reject 的排队棒锚）以此为前提——needs_review 轮行恒 NativeIDOk=false
//（hub writebackSessionTurn 强制），同会话续轮恒按 create 分类、内核已发布即撞
//「平台会话 ID 已存在」（平台既有轮语义，非本块面）——排队棒改走第二成员独立
// 会话（addSecondMember），不依赖本等待；无排队棒的用例传 false 免除无关等待。
func makeAwaitingReview(t *testing.T, f *chainFixture, rid string, waitNodeDone bool) collab.DispatchIntent {
	t.Helper()
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: rid,
		ExpectedVersion: 4, Note: "裁决场景轮",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch %s: %v", rid, err)
	}
	if it.Status != collab.IntentAccepted || it.TaskID == "" {
		t.Fatalf("前置：意图应 accepted+TaskID: %+v", it)
	}
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.State != ledger.StateAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	injectNeedsReview(t, f, it.TaskID)
	waitHubTaskStatus(t, f, it.TaskID, protocol.StatusNeedsReview)
	got, ok := f.svc.IntentByRequest(f.spaceID, rid)
	if !ok || got.Status != collab.IntentAccepted {
		t.Fatalf("留待核对形态前置失败：意图应保持 accepted（现 %+v ok=%v）", got, ok)
	}
	f.releaseBarrier()
	if !waitNodeDone {
		return got
	}
	// node 侧执行收尾（真 CLI 完成 → 真结果回 hub 撞 result_conflict 保留先到 →
	// 账本 Closed 停止重传）——会话首轮内核发布完成，后续轮提交按 send 分类。
	deadline = time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if e, ok := f.nodeLed.Get(it.TaskID); ok && e.Closed {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, ok := f.nodeLed.Get(it.TaskID)
	t.Fatalf("前置：node 侧执行应收尾（真结果撞保留先到 Closed）: ok=%v closed=%v", ok, e.Closed)
	return got
}

// addSecondMember 追加第二成员 n1/coder2（独立会话）：pause → configure_members
//（在途成员保留原绑定——添加不受 member_in_flight 门限制）→ resume（confirm——
// 留待核对意图在途需显式确认）。排队棒经独立会话真实执行（同会话续轮在
// needs_review 行下按 create 分类撞内核已发布——见 makeAwaitingReview 注）。
func addSecondMember(t *testing.T, f *chainFixture, sessionID string) {
	t.Helper()
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "pause-2nd-" + protocol.NewMessageID(),
		Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
		t.Fatalf("暂停（配成员前置）: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	members := append(append([]collab.Member{}, view.Members...), collab.Member{Name: "n1/coder2", SessionID: sessionID})
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "cfg-2nd-" + protocol.NewMessageID(),
		Action: collab.ActConfigureMembers, ExpectedVersion: view.Version, Members: members}, fakeHubSubject); err != nil {
		t.Fatalf("配第二成员: %v", err)
	}
	view, _ = f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "res-2nd-" + protocol.NewMessageID(),
		Action: collab.ActResume, ExpectedVersion: view.Version, Confirm: true}, fakeHubSubject); err != nil {
		t.Fatalf("恢复: %v", err)
	}
}

// TestResolveAcceptConvergesAndUnlocksSerialGate 核心验收（O3 闭环）：accept 裁决
// 复用既有收敛原语落账（converged + 人工核验注记 + 服务端主体事件留痕），串行门
// 因意图真实收敛解锁——排队中的下一棒被 advanceSpace 推进接单（accepted），全链
// 照常完成（converged）；被裁决轮零重发（轮行锚与任务实物不变）。
func TestResolveAcceptConvergesAndUnlocksSerialGate(t *testing.T) {
	f := newChainFixture(t, true, true)
	it := makeAwaitingReview(t, f, "d-res-1", true)
	// 排队棒走第二成员独立会话（同会话续轮在 needs_review 行下按 create 分类撞
	// 内核已发布——平台既有轮语义，见 makeAwaitingReview 注；独立会话让「下一棒
	// 真实执行完成」锚全链诚实）。
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatalf("建第二会话: %v", err)
	}
	addSecondMember(t, f, sv2.ID)
	// 同空间排入下一棒：串行门被 accepted 的 d-res-1 占住 → 保持 pending。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "d-res-2",
		ExpectedVersion: view.Version, Note: "串行门下一棒",
	}, fakeHubSubject); err != nil {
		t.Fatalf("Dispatch d-res-2: %v", err)
	}
	f.waitIntentStatus("d-res-2", collab.IntentPending)
	// accept 裁决：人工核验通过收束。
	got, dup, err := f.svc.ResolveIntent(f.spaceID, "d-res-1", VerdictAccept, "人工核对轮产物无误", fakeHubSubject)
	if err != nil || dup {
		t.Fatalf("ResolveIntent accept: dup=%v err=%v", dup, err)
	}
	if got.Status != collab.IntentConverged {
		t.Fatalf("accept 裁决应收敛 converged: %+v", got)
	}
	if !containsAll(got.Note, "人工核验通过（accept）", "人工核对轮产物无误") {
		t.Fatalf("裁决注记应含人工核验词与调用方 note: %q", got.Note)
	}
	// 留痕主体断言（AGENTS 审计红线：主体为服务端解析主体 owner:owner——fakeHubSubject
	// 经空间锁面传入；事件账本独立字段落账，非请求自报）。裁决推进了下一棒，事件
	// 表随后还有 intent_accepted 等新条目——按 kind+主体+意图 ID 扫描定位裁决事件。
	sp, ok := f.svc.Space(f.spaceID)
	if !ok {
		t.Fatal("空间缺失")
	}
	found := false
	for _, ev := range sp.Events {
		if ev.Kind == "intent_converged" && ev.Actor == fakeHubSubject.String() && strings.Contains(ev.Note, "d-res-1") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("裁决事件应 intent_converged 且主体为服务端主体 %s（事件表 %+v）", fakeHubSubject.String(), sp.Events)
	}
	// 被裁决轮零重发：轮行锚仍指原任务，任务实物终态不变（裁决不改写事实）。
	tr, ok := f.hubCore.SessionTurnByRequest(f.sessID, "d-res-1")
	if !ok || tr.TaskID != it.TaskID {
		t.Fatalf("被裁决轮轮行锚不得变化: %+v ok=%v want task=%s", tr, ok, it.TaskID)
	}
	if tk, ok := f.hubCore.GetTask(it.TaskID); !ok || tk.Status != protocol.StatusNeedsReview {
		t.Fatalf("任务实物不可篡改（needs_review 终态保留）: ok=%v", ok)
	}
	// 核心验收：串行门解锁——排队中的 d-res-2 被 advanceSpace 推进接单。
	f.waitIntentStatus("d-res-2", collab.IntentAccepted)
	// 全链照常完成：下一棒真实执行（首轮内核已发布 → send 分类）→ completed 收敛
	// （非 failed:session_exists 记账路径——B 任务实物断言钉死真执行）。
	f.waitIntentStatus("d-res-2", collab.IntentConverged)
	gotB, _ := f.svc.IntentByRequest(f.spaceID, "d-res-2")
	tk, ok := f.hubCore.GetTask(gotB.TaskID)
	if !ok || tk.Status != protocol.StatusCompleted {
		t.Fatalf("下一棒应真实执行完成（completed 非失败记账）: ok=%v status=%s", ok, tk.Status)
	}
}

// TestResolveRejectCancelsNoRedispatch reject 裁决：accepted→cancelled + 注记；
// 不重发不转投（原任务实物/轮行锚零变化、零新任务）——串行门同样解锁推进排队棒。
func TestResolveRejectCancelsNoRedispatch(t *testing.T) {
	f := newChainFixture(t, true, true)
	it := makeAwaitingReview(t, f, "d-rej-1", true)
	sv2, _, err := f.hubCore.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder2", Cwd: f.nodeCwd}, "admin")
	if err != nil {
		t.Fatalf("建第二会话: %v", err)
	}
	addSecondMember(t, f, sv2.ID)
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder2", RequestID: "d-rej-2",
		ExpectedVersion: view.Version, Note: "否决后排队棒",
	}, fakeHubSubject); err != nil {
		t.Fatalf("Dispatch d-rej-2: %v", err)
	}
	f.waitIntentStatus("d-rej-2", collab.IntentPending)
	got, dup, err := f.svc.ResolveIntent(f.spaceID, "d-rej-1", VerdictReject, "结果不可采信", fakeHubSubject)
	if err != nil || dup {
		t.Fatalf("ResolveIntent reject: dup=%v err=%v", dup, err)
	}
	if got.Status != collab.IntentCancelled {
		t.Fatalf("reject 裁决应收束 cancelled: %+v", got)
	}
	if !containsAll(got.Note, "人工否决收束（reject）", "结果不可采信") {
		t.Fatalf("否决注记不符: %q", got.Note)
	}
	// 不重发不转投：意图账本仅一条 d-rej-1（cancelled）、轮行锚仍指原任务、
	// 原任务实物仍是 needs_review（零新任务零改写）。
	intents := f.svc.Intents(f.spaceID)
	n := 0
	for _, x := range intents {
		if x.RequestID == "d-rej-1" {
			n++
			if x.Status != collab.IntentCancelled {
				t.Fatalf("d-rej-1 应 cancelled: %+v", x)
			}
		}
	}
	if n != 1 {
		t.Fatalf("d-rej-1 应恰一条（不重发不转投）: %d", n)
	}
	tr, ok := f.hubCore.SessionTurnByRequest(f.sessID, "d-rej-1")
	if !ok || tr.TaskID != it.TaskID {
		t.Fatalf("否决后轮行锚不得变化: %+v ok=%v", tr, ok)
	}
	if tk, ok := f.hubCore.GetTask(it.TaskID); !ok || tk.Status != protocol.StatusNeedsReview {
		t.Fatalf("原任务实物不变: ok=%v", ok)
	}
	// 串行门解锁（reject 亦是真实收敛）：排队棒推进接单并真实执行完成。
	f.waitIntentStatus("d-rej-2", collab.IntentAccepted)
	f.waitIntentStatus("d-rej-2", collab.IntentConverged)
	gotB, _ := f.svc.IntentByRequest(f.spaceID, "d-rej-2")
	tk, ok := f.hubCore.GetTask(gotB.TaskID)
	if !ok || tk.Status != protocol.StatusCompleted {
		t.Fatalf("否决后排队棒应真实执行完成: ok=%v status=%s", ok, tk.Status)
	}
}

// TestResolveIdempotentAndCrossVerdict 幂等安全：同裁决重放 dup 命中零重复变更
// （dup 门同既有五入口口径）；异裁决 request_conflict 不可改写收束结论。
func TestResolveIdempotentAndCrossVerdict(t *testing.T) {
	f := newChainFixture(t, true, true)
	makeAwaitingReview(t, f, "d-idem-1", false)
	first, dup, err := f.svc.ResolveIntent(f.spaceID, "d-idem-1", VerdictAccept, "首次裁决", fakeHubSubject)
	if err != nil || dup || first.Status != collab.IntentConverged {
		t.Fatalf("首次 accept: dup=%v err=%v status=%s", dup, err, first.Status)
	}
	noteFirst := first.Note
	again, dup, err := f.svc.ResolveIntent(f.spaceID, "d-idem-1", VerdictAccept, "重放裁决", fakeHubSubject)
	if err != nil || !dup {
		t.Fatalf("重放 accept 应 dup 命中: dup=%v err=%v", dup, err)
	}
	if again.Status != collab.IntentConverged || again.Note != noteFirst {
		t.Fatalf("重放应原样返回原意图（零重复变更）: %+v", again)
	}
	// 异裁决不可改写：converged 意图 reject → request_conflict。
	if _, _, err := f.svc.ResolveIntent(f.spaceID, "d-idem-1", VerdictReject, "", fakeHubSubject); !isRequestConflict(err) {
		t.Fatalf("异裁决应 request_conflict: %v", err)
	}
	// 收束结论不变。
	got, _ := f.svc.IntentByRequest(f.spaceID, "d-idem-1")
	if got.Status != collab.IntentConverged {
		t.Fatalf("异裁决被拒后收束结论不得变化: %+v", got)
	}
	// 非法词形 → invalid（400 域）。
	if _, _, err := f.svc.ResolveIntent(f.spaceID, "d-idem-1", "maybe", "", fakeHubSubject); !isInvalidSpaceErr(err) {
		t.Fatalf("非法 verdict 应 invalid: %v", err)
	}
}

// TestResolveRejectsPendingUnknownAndRunning 准入边界反例（宁窄勿宽）：
//   - 任务仍在执行中的 accepted → 409 execution_in_flight 指路既有取消路径
//     （防冒认在途），意图保持 accepted（用首棒自身任务运行态——barrier 阻塞执行中，
//     零注入零竞态）；
//   - pending（排队待投递）→ 409 illegal_transition（不在裁决面）；
//   - 未知意图 → 409 illegal_transition。
func TestResolveRejectsPendingUnknownAndRunning(t *testing.T) {
	f := newChainFixture(t, true, true)
	// 首棒派发（barrier 阻塞执行中，任务非终态）。
	it, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-edge-1",
		ExpectedVersion: 4, Note: "运行中反例",
	}, fakeHubSubject)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if tk, ok := f.hubCore.GetTask(it.TaskID); ok && tk.Status == protocol.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tk, ok := f.hubCore.GetTask(it.TaskID); !ok || tk.Status != protocol.StatusRunning {
		t.Fatalf("前置：任务应运行中（现 %+v ok=%v）", tk, ok)
	}
	// 运行中裁决 → 409 execution_in_flight（防冒认在途，指路既有取消路径）。
	_, _, err = f.svc.ResolveIntent(f.spaceID, "d-edge-1", VerdictAccept, "", fakeHubSubject)
	if !isExecutionInFlight(err) {
		t.Fatalf("运行中裁决应 409 execution_in_flight: %v", err)
	}
	if !containsAll(err.Error(), "cancel", "既有取消路径") {
		t.Fatalf("409 应指路既有取消路径: %v", err)
	}
	if cur, _ := f.svc.IntentByRequest(f.spaceID, "d-edge-1"); cur.Status != collab.IntentAccepted {
		t.Fatalf("运行中裁决被拒后意图保持 accepted: %+v", cur)
	}
	// pending：串行门被 accepted 首棒占住的排队棒。
	view, _ := f.svc.SpaceView(f.spaceID)
	if _, _, err := f.svc.Dispatch(DispatchRequest{
		SpaceID: f.spaceID, Member: "n1/coder", RequestID: "d-edge-2",
		ExpectedVersion: view.Version, Note: "pending 反例",
	}, fakeHubSubject); err != nil {
		t.Fatalf("Dispatch d-edge-2: %v", err)
	}
	f.waitIntentStatus("d-edge-2", collab.IntentPending)
	if _, _, err := f.svc.ResolveIntent(f.spaceID, "d-edge-2", VerdictAccept, "", fakeHubSubject); !isIllegalTransition(err) {
		t.Fatalf("pending 意图应 409 illegal_transition: %v", err)
	}
	// 未知意图。
	if _, _, err := f.svc.ResolveIntent(f.spaceID, "d-nope", VerdictAccept, "", fakeHubSubject); !isIllegalTransition(err) {
		t.Fatalf("未知意图应 409 illegal_transition: %v", err)
	}
	// 首棒照常收束（取消路径语义不在本块——放行验证空间流转不受裁决拒绝影响；
	// 排队棒随串行门解锁推进接单）。
	f.releaseBarrier()
	f.waitIntentStatus("d-edge-1", collab.IntentConverged)
	f.waitIntentStatus("d-edge-2", collab.IntentAccepted)
}

// TestResolveEvidenceMissingAccept 证据缺失形态（任务已过保留期清理）：accepted
// 意图的任务行被清理后 accept 裁决可收束（②证据缺失分支的机械闭环）。
func TestResolveEvidenceMissingAccept(t *testing.T) {
	f := newChainFixtureO(t, chainOpts{barrier: true, capable: true,
		sweepInterval: 50 * time.Millisecond, terminalRetention: 5 * time.Millisecond})
	it := makeAwaitingReview(t, f, "d-gone-1", false)
	// 极短保留期扫查清理任务行（证据缺失形态）。
	deadline := time.Now().Add(testWaitBudget)
	cleaned := false
	for time.Now().Before(deadline) {
		if _, ok := f.hubCore.GetTask(it.TaskID); !ok {
			cleaned = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleaned {
		t.Fatal("前置：任务应被保留期清理（证据缺失形态）")
	}
	if got, _ := f.svc.IntentByRequest(f.spaceID, "d-gone-1"); got.Status != collab.IntentAccepted {
		t.Fatalf("证据缺失意图应保持 accepted（留待核对）: %+v", got)
	}
	got, dup, err := f.svc.ResolveIntent(f.spaceID, "d-gone-1", VerdictAccept, "人工线下核对通过", fakeHubSubject)
	if err != nil || dup || got.Status != collab.IntentConverged {
		t.Fatalf("证据缺失 accept 裁决应收束: dup=%v err=%v status=%s", dup, err, got.Status)
	}
	if !containsAll(got.Note, "人工核验通过（accept）", "人工线下核对通过") {
		t.Fatalf("注记不符: %q", got.Note)
	}
}

// ---------- 断言辅助 ----------

// containsAll 报告 s 是否包含全部子串。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// isRequestConflict / isIllegalTransition / isExecutionInFlight / isInvalidSpaceErr
// 错误码断言（resolve 准入边界的 HTTP 投影码域——与 collab.HTTPStatus 同表）。
func isRequestConflict(err error) bool {
	return hasCollabCode(err, collab.CodeRequestConflict)
}

func isIllegalTransition(err error) bool {
	return hasCollabCode(err, collab.CodeIllegalTransition)
}

func isExecutionInFlight(err error) bool {
	return hasCollabCode(err, collab.CodeExecutionInFlight)
}

func isInvalidSpaceErr(err error) bool {
	var se *Error
	return errors.As(err, &se) && se.Code == "invalid"
}

func hasCollabCode(err error, code string) bool {
	var ce *collab.Error
	if errors.As(err, &ce) {
		return ce.Code == code
	}
	return false
}
