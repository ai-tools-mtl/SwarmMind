package strategy

// writereview_test.go 编写审查策略测试（W05-03；fake 执行器 + 真 collab Store +
// 真 Engine）：编写→冻结→审查→issue→返工→修复→复审→完成 全链（完成经 collab
// finish 走真实判据——W04-01 交接面兑现）；issue 重开回归→再返工；同一问题两
// 次未解→暂停（恢复回暂停前阶段）；collab ActSetPhase 分离边界（判定在 strategy
// 拒绝、落账在 collab）。

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// wiredWriteReview 装配一套「真 collab Store（hooks 已注入）+ 真 Engine + 运行态
// 写审空间（writer alice / reviewer bob，完成授权已开）」，并完成循环引导（未初
// 化 → drafting，经 collab ActSetPhase 落账）。返回引擎/存储/时钟/空间 ID。
func wiredWriteReview(t *testing.T) (*Engine, *collab.Store, *fakeClock, string) {
	t.Helper()
	eng := newTestEngine(t)
	st, clk := newWiredStore(t, eng)
	id, _ := wiredRunningSpace(t, st)
	v, _ := st.SpaceView(id)
	next, err := eng.BootstrapCycle(v)
	if err != nil {
		t.Fatalf("BootstrapCycle: %v", err)
	}
	landPhase(t, eng, st, id, "", next)
	if got := phaseOf2(t, st, id); got != string(PhaseDrafting) {
		t.Fatalf("引导后应为 drafting: %q", got)
	}
	return eng, st, clk, id
}

// landPhase 分离边界装配序列（生产口径）：ValidatePhaseTransition 预检（from 空
// = 引导边不预检）→ collab Control(ActSetPhase) 落账 → SetPhase 记引擎侧事实。
func landPhase(t *testing.T, eng *Engine, st *collab.Store, id string, from, to Phase) {
	t.Helper()
	if from != "" {
		if err := eng.ValidatePhaseTransition(id, from, to); err != nil {
			t.Fatalf("阶段转换预检 %s → %s: %v", string(from), string(to), err)
		}
	}
	landPhaseRaw(t, st, id, string(to), fmt.Sprintf("setphase-%s-%s-%s", from, to, id))
	if from != "" {
		if err := eng.SetPhase(id, from, to); err != nil {
			t.Fatalf("SetPhase 记引擎事实 %s → %s: %v", string(from), string(to), err)
		}
	}
}

// landPhaseRaw collab ActSetPhase 直接落账（引擎 SetPhase 与 collab 落账各半段的
// 测试助手；requestID 由调用方唯一化）。
func landPhaseRaw(t *testing.T, st *collab.Store, id, phase, requestID string) {
	t.Helper()
	sp, ok := st.Space(id)
	if !ok {
		t.Fatalf("空间 %s 不存在", id)
	}
	res, _, err := st.Control(id, collab.ControlRequest{
		RequestID: requestID, Action: collab.ActSetPhase, ExpectedVersion: sp.Version, Phase: phase,
	}, intOwner)
	if err != nil {
		t.Fatalf("ActSetPhase %s: %v", phase, err)
	}
	if !res.Applied {
		t.Fatalf("ActSetPhase %s 未实际变更", phase)
	}
}

// mustSpace2 collab 空间读取断言。
func mustSpace2(t *testing.T, st *collab.Store, id string) collab.Space {
	t.Helper()
	sp, ok := st.Space(id)
	if !ok {
		t.Fatalf("空间 %s 不存在", id)
	}
	return sp
}

// phaseOf2 直读空间阶段。
func phaseOf2(t *testing.T, st *collab.Store, id string) string {
	t.Helper()
	return mustSpace2(t, st, id).Phase
}

// spaceViewOf 空间视图读取。
func spaceViewOf(st *collab.Store, id string) collab.SpaceView {
	v, _ := st.SpaceView(id)
	return v
}

// TestW0503WriteReviewFullChain 全链（fake 执行器=测试逐轮驱动 + 真 collab Store
// + 真 Engine）：引导 drafting → alice 编写登记 v1 → 冻结 → 审查（bob）→ must
// 问题 → 返工 → alice 修复声明 + 登记 v2 → 复审确认 → 完成门（四层证据 + finish
// 走真实判据）→ collab completed。
func TestW0503WriteReviewFullChain(t *testing.T) {
	eng, st, clk, id := wiredWriteReview(t)
	space := func() collab.SpaceView { return spaceViewOf(st, id) }
	// 编写：无版本不推进，指派 writer。
	d := eng.AdvanceCycle(space(), clk.Now())
	if d.ShouldPause || d.Advance || d.NextActor != "alice" || d.ActorAction != ActorWrite {
		t.Fatalf("drafting 应指派 alice 编写：%+v", d)
	}
	v1 := mustRegisterRevision(t, eng, id, "v1", 1)
	// 编写产出在案 → 建议冻结。
	d = eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseFrozen {
		t.Fatalf("有版本应建议冻结：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseDrafting, PhaseFrozen)
	if got := phaseOf2(t, st, id); got != string(PhaseFrozen) {
		t.Fatalf("冻结落账不符: %q", got)
	}
	// 冻结完成 → 建议审查（reviewer bob 在位）。
	d = eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseReview {
		t.Fatalf("冻结应推进审查：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseFrozen, PhaseReview)
	// 审查：无阻塞问题不推进，指派 reviewer。
	d = eng.AdvanceCycle(space(), clk.Now())
	if d.Advance || d.NextActor != "bob" || d.ActorAction != ActorReview {
		t.Fatalf("review 应指派 bob 审查：%+v", d)
	}
	// bob 提出 must 问题（针对 v1）→ 返工触发。
	issID := mustIssue(t, eng, id, v1, SeverityMust)
	d = eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseRework {
		t.Fatalf("must 未解应返工：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseReview, PhaseRework)
	// 返工：指派 writer 修复；修复声明 + 新版本登记 → 复审触发。
	d = eng.AdvanceCycle(space(), clk.Now())
	if d.NextActor != "alice" || d.ActorAction != ActorFix {
		t.Fatalf("rework 应指派 alice 修复：%+v", d)
	}
	v2 := mustRegisterRevision(t, eng, id, "v2", 2)
	if err := eng.ClaimFix(id, issID, v2, 1, "已按层拆分证据"); err != nil {
		t.Fatalf("ClaimFix: %v", err)
	}
	d = eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseRereview {
		t.Fatalf("修复后新版本应复审：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseRework, PhaseRereview)
	// 复审：指派 reviewer；确认已修（对 v2）后不推进（审查通过≠完成）。
	d = eng.AdvanceCycle(space(), clk.Now())
	if d.NextActor != "bob" || d.ActorAction != ActorReReview {
		t.Fatalf("rereview 应指派 bob 复审：%+v", d)
	}
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: v2, Result: ReReviewConfirmed}); err != nil {
		t.Fatalf("RecordReReview: %v", err)
	}
	d = eng.AdvanceCycle(space(), clk.Now())
	if d.Advance {
		t.Fatalf("复审通过不推进（完成判定归判据）：%+v", d)
	}
	// 完成门：证据按 v2 补齐（四层分离）→ finish 经 collab hooks 真实判据通过。
	mustEvidence(t, eng, id, v2)
	if verdict := eng.EvaluateCompletion(space()); !verdict.Satisfied {
		t.Fatalf("完成判据应满足：%s", verdict.Reason)
	}
	sp := mustSpace2(t, st, id)
	res, _, err := st.Control(id, collab.ControlRequest{
		RequestID: "finish-full", Action: collab.ActFinish, ExpectedVersion: sp.Version, Term: 1,
	}, intOwner)
	if err != nil {
		t.Fatalf("finish 应经真实判据通过：%v", err)
	}
	if !res.Applied || res.Space.Status != collab.StatusCompleted {
		t.Fatalf("完成落账不符：%s applied=%v", res.Space.Status, res.Applied)
	}
}

// TestW0503ReopenRegressionReissuesRework 重开回归：复审确认后同 key 重报 → 重
// 开投影生效 → 复审阶段再返工（W05-01 重开投影经循环驱动生效）。
func TestW0503ReopenRegressionReissuesRework(t *testing.T) {
	eng, st, clk, id := wiredWriteReview(t)
	space := func() collab.SpaceView { return spaceViewOf(st, id) }
	v1 := mustRegisterRevision(t, eng, id, "v1", 1)
	landPhase(t, eng, st, id, PhaseDrafting, PhaseFrozen)
	landPhase(t, eng, st, id, PhaseFrozen, PhaseReview)
	issID := mustIssue(t, eng, id, v1, SeverityMust)
	// 审查发现 → 返工 → 修复（v2）→ 复审确认。
	landPhase(t, eng, st, id, PhaseReview, PhaseRework)
	v2 := mustRegisterRevision(t, eng, id, "v2", 2)
	if err := eng.ClaimFix(id, issID, v2, 1, "修复"); err != nil {
		t.Fatal(err)
	}
	landPhase(t, eng, st, id, PhaseRework, PhaseRereview)
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: v2, Result: ReReviewConfirmed}); err != nil {
		t.Fatal(err)
	}
	// 复审通过态：不推进。
	if d := eng.AdvanceCycle(space(), clk.Now()); d.Advance {
		t.Fatalf("确认后不应推进：%+v", d)
	}
	// 同 key 重报（回归）→ 重开投影 → 复审阶段再返工。
	if _, err := eng.RecordIssue(id, IssueInput{
		Reviewer: "bob", Severity: SeverityMust, File: "internal/foo/bar.go", Line: 42,
		Title: "边界检查缺失", FoundInHash: v2, FoundInRound: 3,
	}); err != nil {
		t.Fatal(err)
	}
	d := eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseRework {
		t.Fatalf("回归重开应再返工：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseRereview, PhaseRework)
	if got := phaseOf2(t, st, id); got != string(PhaseRework) {
		t.Fatalf("再返工落账不符: %q", got)
	}
}

// TestW0503TwoUnresolvedPause 同一问题两次修复未过复审 → EvaluateStop 修复阈值
// 触发暂停建议 → 装配序列落账（collab pause + ActSetPhase paused，引擎 PausedFrom
// 记忆）→ 显式恢复只许回暂停前阶段。
func TestW0503TwoUnresolvedPause(t *testing.T) {
	eng, st, clk, id := wiredWriteReview(t)
	space := func() collab.SpaceView { return spaceViewOf(st, id) }
	v1 := mustRegisterRevision(t, eng, id, "v1", 1)
	landPhase(t, eng, st, id, PhaseDrafting, PhaseFrozen)
	landPhase(t, eng, st, id, PhaseFrozen, PhaseReview)
	issID := mustIssue(t, eng, id, v1, SeverityMust)
	// 两轮修复未过（still_open × 2，默认阈值 2）。
	if err := eng.ClaimFix(id, issID, v1, 1, "修了"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	if err := eng.ClaimFix(id, issID, v1, 2, "又修了"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	// 停止判定优先：暂停建议（修复阈值触发）压倒阶段推进/指派。
	d := eng.AdvanceCycle(space(), clk.Now())
	if !d.ShouldPause {
		t.Fatalf("两次未解应建议暂停：%+v", d)
	}
	hasFixRounds := false
	for _, trig := range d.PauseTriggers {
		if trig.Kind == StopFixRounds {
			hasFixRounds = true
		}
	}
	if !hasFixRounds {
		t.Fatalf("暂停触发应含修复阈值：%+v", d.PauseTriggers)
	}
	// 装配序列：引擎 SetPhase(review → paused) 校验 + collab 落账 + 空间 pause。
	if err := eng.SetPhase(id, PhaseReview, PhasePaused); err != nil {
		t.Fatalf("SetPhase paused: %v", err)
	}
	landPhaseRaw(t, st, id, string(PhasePaused), "setphase-paused")
	sp := mustSpace2(t, st, id)
	if _, _, err := st.Control(id, collab.ControlRequest{
		RequestID: "pause-2x", Action: collab.ActPause, ExpectedVersion: sp.Version, Note: "同一问题两次未解上报",
	}, intOwner); err != nil {
		t.Fatalf("ActPause: %v", err)
	}
	// 引擎侧 PausedFrom 记忆：恢复到非暂停前阶段拒绝，回暂停前阶段通过。
	if err := eng.ValidatePhaseTransition(id, PhasePaused, PhaseDrafting); err == nil ||
		!strings.Contains(err.Error(), "暂停恢复") {
		t.Fatalf("恢复到 drafting 应拒绝：%v", err)
	}
	if err := eng.ValidatePhaseTransition(id, PhasePaused, PhaseReview); err != nil {
		t.Fatalf("恢复到暂停前阶段应通过：%v", err)
	}
	// 恢复落账（collab resume + ActSetPhase 回 review）。
	sp = mustSpace2(t, st, id)
	if _, _, err := st.Control(id, collab.ControlRequest{
		RequestID: "resume-2x", Action: collab.ActResume, ExpectedVersion: sp.Version,
	}, intOwner); err != nil {
		t.Fatalf("ActResume: %v", err)
	}
	landPhaseRaw(t, st, id, string(PhaseReview), "setphase-resume")
	if err := eng.SetPhase(id, PhasePaused, PhaseReview); err != nil {
		t.Fatalf("SetPhase 恢复: %v", err)
	}
	if got := phaseOf2(t, st, id); got != string(PhaseReview) {
		t.Fatalf("恢复后阶段不符: %q", got)
	}
}

// TestW0503ReworkLoopStillOpen 经典返工环：复审 still_open → 再返工（修复声明结
// 论已回填、阻塞仍在）→ 返工中等待新版本（当前版本已复审过）——零进度不循环推
// 进，修复后新版本登记才放行复审。
func TestW0503ReworkLoopStillOpen(t *testing.T) {
	eng, st, clk, id := wiredWriteReview(t)
	space := func() collab.SpaceView { return spaceViewOf(st, id) }
	v1 := mustRegisterRevision(t, eng, id, "v1", 1)
	landPhase(t, eng, st, id, PhaseDrafting, PhaseFrozen)
	landPhase(t, eng, st, id, PhaseFrozen, PhaseReview)
	issID := mustIssue(t, eng, id, v1, SeverityMust)
	landPhase(t, eng, st, id, PhaseReview, PhaseRework)
	v2 := mustRegisterRevision(t, eng, id, "v2", 2)
	if err := eng.ClaimFix(id, issID, v2, 1, "修复"); err != nil {
		t.Fatal(err)
	}
	landPhase(t, eng, st, id, PhaseRework, PhaseRereview)
	// 复审入口：修复声明待复审 → 等待（不零进度回返工）。
	if d := eng.AdvanceCycle(space(), clk.Now()); d.Advance {
		t.Fatalf("复审入口应等待结论：%+v", d)
	}
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: v2, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	// still_open：结论已回填（无待复核声明）、阻塞仍在 → 再返工。
	d := eng.AdvanceCycle(space(), clk.Now())
	if !d.Advance || d.NextPhase != PhaseRework {
		t.Fatalf("still_open 应再返工：%+v", d)
	}
	landPhase(t, eng, st, id, PhaseRereview, PhaseRework)
	// 返工中：当前版本已复审过（锚= v2）→ 等新版本。
	if d := eng.AdvanceCycle(space(), clk.Now()); d.Advance || d.NextActor != "alice" {
		t.Fatalf("无新版本应等待修复：%+v", d)
	}
}

// TestW0503AnchorsRequireRegisteredRevision 终审建议1：问题登记/复审/零发现复审
// 的版本锚定须为「本空间已登记版本」（对照 RecordEvidence ErrRevisionNotFound 先
// 例）——未登记 hash 拒绝、零副作用（初审锚不被陈旧 hash 污染、锚推进不发生）、
// 跨空间已登记 hash 不冒认。
func TestW0503AnchorsRequireRegisteredRevision(t *testing.T) {
	eng, st, clk, id := wiredWriteReview(t)
	_ = clk
	ghost := mkhash("ghost") // 合法 hex、未登记
	// 问题登记：未登记发现版本拒绝，且初审锚不被初始化（零副作用）。
	if _, err := eng.RecordIssue(id, IssueInput{
		Reviewer: "bob", Severity: SeverityMust, File: "x.go", Line: 1,
		Title: "幽灵版本问题", FoundInHash: ghost, FoundInRound: 1,
	}); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("未登记发现版本应拒绝：%v", err)
	}
	if snap, ok := eng.Snapshot(id); ok && snap.LastReviewedHash != "" {
		t.Fatalf("拒绝路径不得初始化初审锚：%q", snap.LastReviewedHash)
	}
	// 正常链：登记 v1 → 问题锚定 v1（初审锚=v1）→ 复审对未登记版本拒绝（锚不动）。
	v1 := mustRegisterRevision(t, eng, id, "v1", 1)
	issID := mustIssue(t, eng, id, v1, SeverityMust)
	if err := eng.ClaimFix(id, issID, v1, 1, "修复"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(id, issID, ReReviewInput{Reviewer: "bob", AgainstHash: ghost, Result: ReReviewConfirmed}); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("复审对未登记版本应拒绝：%v", err)
	}
	snap, _ := eng.Snapshot(id)
	if snap.LastReviewedHash != v1 {
		t.Fatalf("拒绝路径锚不得推进：%q", snap.LastReviewedHash)
	}
	// 零发现复审对未登记版本拒绝（锚不动）；已登记版本放行。
	if err := eng.RecordCleanReview(id, "bob", ghost, "零发现"); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("零发现复审对未登记版本应拒绝：%v", err)
	}
	if snap, _ := eng.Snapshot(id); len(snap.CleanReviews) != 0 || snap.LastReviewedHash != v1 {
		t.Fatalf("拒绝路径零发现记录/锚不符：%+v", snap.LastReviewedHash)
	}
	if err := eng.RecordCleanReview(id, "bob", v1, "零发现"); err != nil {
		t.Fatalf("已登记版本零发现复审应放行：%v", err)
	}
	// 跨空间不冒认：hash 在本空间已登记、他空间未登记——他空间记录拒绝。
	if _, err := eng.RecordIssue("sp-other-space", IssueInput{
		Reviewer: "bob", Severity: SeverityInfo, File: "y.go", Line: 2,
		Title: "他空间问题", FoundInHash: v1, FoundInRound: 1,
	}); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("他空间未登记版本应拒绝：%v", err)
	}
	_ = st
}

// TestW0503SeparationBoundary 分离边界：非法转换在 strategy 拒绝（落账前预检拦
// 截——collab 不被调用）；collab 侧对值只做基校验不解释语义（本包把守转换表，
// collab 把守授权/版本/状态——边界即「判定在 strategy、落账在 collab」）。
func TestW0503SeparationBoundary(t *testing.T) {
	eng, st, _, id := wiredWriteReview(t)
	sp := mustSpace2(t, st, id)
	// 表外路径预检拒绝（drafting → review 跳过冻结）——见下方拒绝零副作用断言。
	if err := eng.ValidatePhaseTransition(id, Phase("junk"), PhaseFrozen); err == nil {
		t.Fatal("非法来源应拒绝")
	}
	if err := eng.ValidatePhaseTransition(id, PhaseDrafting, Phase("junk")); err == nil {
		t.Fatal("非法目标应拒绝")
	}
	// 拒绝零副作用：引擎无新增留痕（引导边留痕不计）、collab 阶段未动。
	before, _ := eng.Snapshot(id)
	if err := eng.ValidatePhaseTransition(id, PhaseDrafting, PhaseReview); err == nil {
		t.Fatal("表外路径应拒绝")
	}
	if snap, ok := eng.Snapshot(id); ok && len(snap.Trail) != len(before.Trail) {
		t.Fatalf("拒绝路径不得新增留痕：%+v", snap.Trail)
	}
	if got := phaseOf2(t, st, id); got != string(PhaseDrafting) {
		t.Fatalf("拒绝路径 collab 阶段不得变化: %q", got)
	}
	// collab 侧值基校验：合法词形即落（不解释转换语义——分离边界另一面）。
	landPhaseRaw(t, st, id, string(PhaseFrozen), "sep-frozen")
	if got := phaseOf2(t, st, id); got != string(PhaseFrozen) {
		t.Fatalf("collab 落账不符: %q", got)
	}
	// SetPhase 判定 + 落账全序（表内边）：frozen → review。
	if err := eng.SetPhase(id, PhaseFrozen, PhaseReview); err != nil {
		t.Fatalf("SetPhase 表内边应通过: %v", err)
	}
	landPhaseRaw(t, st, id, string(PhaseReview), "sep-review")
	if got := phaseOf2(t, st, id); got != string(PhaseReview) {
		t.Fatalf("落账后阶段不符: %q", got)
	}
	// 错误形态：errors.Is 按码判定可用。
	if err := eng.ValidatePhaseTransition(id, PhaseReview, PhaseDrafting); !errors.Is(err, ErrInvalid) {
		t.Fatalf("非法转换应 ErrInvalid：%v", err)
	}
	_ = sp
}
