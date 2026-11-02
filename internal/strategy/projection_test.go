package strategy

// projection_test.go 策略投影只读面机械测试（W05-04 P04 策略区数据源）：如实空态
// 不编造 / 事实投影五组齐全 / records 增量读游标（单调 + 裁剪上界）/ 四族映射。
// 纯引擎层，零网络零模型；复用 helpers_test fixture。

import (
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// TestProjectionHonestEmpty 无策略数据空间：HasLoop=false、无候选/版本/问题、缺口
// 不编造（Gaps 留空）；停止建议仍给（预算判定不依赖循环事实）；未知阶段词如实
// 标注 PhaseValid=false。
func TestProjectionHonestEmpty(t *testing.T) {
	eng := newTestEngine(t)
	sp := fixtureSpace("sp-empty", "", 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxMessages: 1}
		v.Used = collab.BudgetUsed{Messages: 1}
	})
	p := eng.Projection("sp-empty", sp, time.Now().UTC())
	if p.HasLoop || len(p.Candidates) > 0 || len(p.Revisions) > 0 || len(p.Issues) > 0 {
		t.Fatalf("无事实空间应如实空态: %+v", p)
	}
	if len(p.Gaps) != 0 {
		t.Fatalf("无事实空间不应编造缺口清单: %+v", p.Gaps)
	}
	if !p.Stop.ShouldPause || len(p.Stop.Triggers) == 0 {
		t.Fatalf("消息预算耗尽应出现在停止建议: %+v", p.Stop)
	}
	if p.Phase != "" || p.PhaseValid {
		t.Fatalf("未初始化阶段应空且标注无效: %q %v", p.Phase, p.PhaseValid)
	}
	// 未知阶段词：原样透传 + 无效标注（不猜 drafting）。
	sp.Phase = "mystery"
	if p := eng.Projection("sp-empty", sp, time.Now().UTC()); p.Phase != "mystery" || p.PhaseValid {
		t.Fatalf("未知阶段词应透传并标注无效: %+v", p)
	}
}

// TestProjectionWithFacts 事实投影：候选定版态/问题重开投影/版本与复审锚/缺口
// 清单逐项可见（AC13 展示面：自报证据未证实不粉饰）。
func TestProjectionWithFacts(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-proj"
	// v1 登记 + must 问题（未修）——缺口应点名证据/版本匹配/候选。
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	issueID := mustIssue(t, eng, sp, v1, SeverityMust)
	// 自报证据：记录面拒绝，投影面自然无已证实层（不编造）。
	if err := eng.RecordEvidence(sp, v1, LayerCLIExit,
		Evidence{Verified: true, Source: SourceSelfReport, Ref: "agent-said-so"}); err == nil {
		t.Fatalf("自报标证实应记录面拒绝")
	}
	spView := fixtureSpace(sp, string(PhaseReview), 0, func(v *collab.SpaceView) {
		v.Acceptance = "验收在位"
	})
	p := eng.Projection(sp, spView, time.Now().UTC())
	if !p.HasLoop || p.CurrentRevision != v1 || p.LastReviewedHash != v1 {
		t.Fatalf("投影应含版本与初审锚: %+v", p)
	}
	if len(p.Candidates) != 0 || len(p.Revisions) != 1 || len(p.Issues) != 1 {
		t.Fatalf("投影事实计数不符: cand=%d rev=%d issue=%d", len(p.Candidates), len(p.Revisions), len(p.Issues))
	}
	iv := p.Issues[0]
	if iv.ID != issueID || iv.State != IssueOpen || !iv.Blocking {
		t.Fatalf("问题投影应 open 且阻塞: %+v", iv)
	}
	var gapFields []string
	for _, g := range p.Gaps {
		gapFields = append(gapFields, g.Field)
	}
	// 零候选登记不产生候选缺口（清零判据只在候选在位时起作用）；证据与未解问题
	// 必须点名。
	if !containsStr(gapFields, "evidence") || !containsStr(gapFields, "issue") {
		t.Fatalf("缺口应点名证据与未解问题: %v", gapFields)
	}
	// 闭环：四层证实 + 修复复审 + 候选定版 → 缺口清零（版本匹配由复审锚推进）。
	mustEvidence(t, eng, sp, v1)
	if err := eng.ClaimFix(sp, issueID, v1, 1, "已修"); err != nil {
		t.Fatalf("ClaimFix: %v", err)
	}
	if err := eng.RecordReReview(sp, issueID, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewConfirmed}); err != nil {
		t.Fatalf("RecordReReview: %v", err)
	}
	mustCandidateFinalized(t, eng, sp, "FINAL: 采用方案一")
	p = eng.Projection(sp, spView, time.Now().UTC())
	if len(p.Gaps) != 0 {
		t.Fatalf("判据全满足后缺口应清零: %+v", p.Gaps)
	}
	if p.Issues[0].State != IssueConfirmedFixed || p.Issues[0].Blocking {
		t.Fatalf("复审确认后投影应 confirmed_fixed 且不阻塞: %+v", p.Issues[0])
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Status != CandidateFinalized {
		t.Fatalf("候选投影应显式定版态（定版≠交付）: %+v", p.Candidates)
	}
}

// TestProjectionPausedFrom 暂停面：SetPhase 进 paused 记 PausedFrom，投影可见
// （恢复目标校验的人话展示源）。
func TestProjectionPausedFrom(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-paused"
	if err := eng.SetPhase(sp, PhaseDrafting, PhasePaused); err != nil {
		t.Fatalf("SetPhase paused: %v", err)
	}
	p := eng.Projection(sp, fixtureSpace(sp, string(PhasePaused), 0), time.Now().UTC())
	if !p.HasLoop || p.PausedFrom != PhaseDrafting {
		t.Fatalf("投影应携带暂停前阶段: %+v", p)
	}
}

// TestRecordsSinceCursor records 增量读：Seq 单调、since 游标、滚动裁剪上界
// pruned_before_seq（裁剪只丢事件不回拨序号）。
func TestRecordsSinceCursor(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-recs"
	mustRegisterRevision(t, eng, sp, "v1", 1)
	mustIssue(t, eng, sp, mkhash("v1"), SeverityMust)
	cands, pruned := eng.RecordsSince(sp, 0)
	if len(cands) != 2 || pruned != 0 {
		t.Fatalf("增量读应得 2 条零裁剪: %d pruned=%d", len(cands), pruned)
	}
	if cands[0].Seq >= cands[1].Seq {
		t.Fatalf("序号应严格递增: %d %d", cands[0].Seq, cands[1].Seq)
	}
	// since=首条：只回第二条。
	rest, _ := eng.RecordsSince(sp, cands[0].Seq)
	if len(rest) != 1 || rest[0].Seq != cands[1].Seq {
		t.Fatalf("since 游标应只回未见记录: %+v", rest)
	}
	// 滚动裁剪：刷过 MaxTrailEvents 后最旧不可再取，但序号高水位不回拨。
	for i := 0; i < MaxTrailEvents; i++ {
		if _, _, err := eng.RegisterCandidateVia(sp, ChannelFinalText,
			CandidateInput{Proposer: "alice", Body: "FINAL: 第 " + itoa(i) + " 号候选", Round: 1}); err != nil {
			t.Fatalf("候选批量登记 %d: %v", i, err)
		}
	}
	all, pruned := eng.RecordsSince(sp, 0)
	if len(all) != MaxTrailEvents || pruned != all[0].Seq-1 {
		t.Fatalf("裁剪后应恒 %d 条且上界=最旧序号-1: len=%d pruned=%d", MaxTrailEvents, len(all), pruned)
	}
	snap, ok := eng.Snapshot(sp)
	if !ok || snap.FactSeq < cands[1].Seq {
		t.Fatalf("FactSeq 高水位不得回拨: %d", snap.FactSeq)
	}
	if all[len(all)-1].Seq != snap.FactSeq {
		t.Fatalf("最新事件序号应等于高水位: %d vs %d", all[len(all)-1].Seq, snap.FactSeq)
	}
}

// TestRecordFamilyMapping 四族映射：记录事件各归其族；控制留痕（阶段/争议）不冒充记录。
func TestRecordFamilyMapping(t *testing.T) {
	cases := map[TrailKind]string{
		TrailCandidateRegistered: "candidate", TrailCandidateRestated: "candidate",
		TrailCandidateFinalized: "candidate", TrailCandidateDismissed: "candidate",
		TrailRevisionRegistered: "review", TrailReviewInvalidated: "review",
		TrailFixClaimed: "review", TrailReReviewRecorded: "review", TrailCleanReview: "review",
		TrailIssueRecorded: "issue", TrailIssueRereported: "issue",
		TrailIssueReopened: "issue", TrailIssueReplied: "issue",
		TrailEvidenceRecorded: "evidence",
		TrailPhaseChanged:     "", TrailDisputeSet: "", TrailDisputeCleared: "",
	}
	for k, want := range cases {
		if got := k.RecordFamily(); got != want {
			t.Errorf("%s.RecordFamily() = %q, want %q", string(k), got, want)
		}
	}
}

// TestProjectionStopFixRounds 修复阈值投影：同问题连续 2 次修复未过复审 → 停止
// 建议携 fix_rounds 触发（P04 区停止建议展示源）。
func TestProjectionStopFixRounds(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-stop"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssue(t, eng, sp, v1, SeverityMust)
	for i := 0; i < DefaultFixRoundsLimit; i++ {
		if err := eng.ClaimFix(sp, id, v1, int64(i+1), "再修"); err != nil {
			t.Fatalf("ClaimFix %d: %v", i, err)
		}
		if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
			t.Fatalf("RecordReReview %d: %v", i, err)
		}
	}
	p := eng.Projection(sp, fixtureSpace(sp, string(PhaseRework), 0), time.Now().UTC())
	found := false
	for _, tg := range p.Stop.Triggers {
		if tg.Kind == StopFixRounds {
			found = true
		}
	}
	if !p.Stop.ShouldPause || !found {
		t.Fatalf("修复阈值达限应出现在停止建议: %+v", p.Stop)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// itoa 测试内 int 十进制（避免 import strconv 单用途）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
