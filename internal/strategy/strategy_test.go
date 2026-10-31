package strategy

// W05-01 fixture 驱动机械测试（零网络零模型）：完成判据缺口清单 + 自报证据拒绝
// + 判定面再验。公共助手见 helpers_test.go；候选/审查/阶段机/停止/集成各有分文件。

import (
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// fixtureSpace 空间视图 fixture（writer/reviewer 成员齐备；可变项显式传参）。
func fixtureSpace(id, phase string, inFlight int, mutate ...func(*collab.SpaceView)) collab.SpaceView {
	sp := collab.SpaceView{
		Space: collab.Space{
			ID:         id,
			Goal:       "写审循环目标",
			Acceptance: "验收：go test 全绿",
			Phase:      phase,
			Status:     collab.StatusRunning,
			Members: []collab.Member{
				{Name: "alice", Role: "writer", SessionID: "sess-alice"},
				{Name: "bob", Role: "reviewer", SessionID: "sess-bob"},
			},
		},
		InFlight: inFlight,
	}
	for _, f := range mutate {
		f(&sp)
	}
	return sp
}

// wantGap 断言缺口清单含指定域（并抽查细节子串）。
func wantGap(t *testing.T, gaps []Gap, field, detailSub string) {
	t.Helper()
	for _, g := range gaps {
		if g.Field == field {
			if detailSub != "" && !strings.Contains(g.Detail, detailSub) {
				t.Fatalf("缺口 [%s] 细节不含 %q：%s", field, detailSub, g.Detail)
			}
			return
		}
	}
	t.Fatalf("缺口清单缺域 %s：%+v", field, gaps)
}

// wantNoGap 断言缺口清单不含指定域。
func wantNoGap(t *testing.T, gaps []Gap, field string) {
	t.Helper()
	for _, g := range gaps {
		if g.Field == field {
			t.Fatalf("缺口清单不应含域 %s：%+v", field, gaps)
		}
	}
}

// ---------- 完成验证（任务书：全满足→pass；各单缺口→拒绝+缺口清单精确） ----------

func TestCompletionAllSatisfiedPass(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-full"
	hash := satisfiedFixture(t, eng, sp)
	space := fixtureSpace(sp, string(PhaseReview), 0)
	if gaps := eng.CompletionGaps(space); len(gaps) != 0 {
		t.Fatalf("全满足 fixture 应零缺口：%+v", gaps)
	}
	verdict := eng.EvaluateCompletion(space)
	if !verdict.Satisfied {
		t.Fatalf("完成判据应满足：%s", verdict.Reason)
	}
	// EvidenceRefs 携判据引用（版本/复审锚/定版候选）落审计事件。
	var hasVersion, hasCandidate bool
	for _, ref := range verdict.EvidenceRefs {
		if ref == "version:"+hash {
			hasVersion = true
		}
		if strings.HasPrefix(ref, "candidate:") {
			hasCandidate = true
		}
	}
	if !hasVersion || !hasCandidate {
		t.Fatalf("EvidenceRefs 缺版本/候选引用：%v", verdict.EvidenceRefs)
	}
}

func TestCompletionSingleGapsRejected(t *testing.T) {
	cases := []struct {
		name      string
		build     func(t *testing.T, eng *Engine) collab.SpaceView
		wantField string // "" = 应零缺口（控制组）
		wantSub   string
	}{
		{
			name: "验收条件缺失",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-a1")
				return fixtureSpace("sp-a1", string(PhaseReview), 0, func(v *collab.SpaceView) { v.Acceptance = "" })
			},
			wantField: "acceptance",
			wantSub:   "acceptance",
		},
		{
			name: "阶段未进审查",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-a2")
				return fixtureSpace("sp-a2", string(PhaseDrafting), 0)
			},
			wantField: "phase",
			wantSub:   "尚未进入审查",
		},
		{
			name: "阶段未初始化",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-a3")
				return fixtureSpace("sp-a3", "", 0)
			},
			wantField: "phase",
			wantSub:   "未初始化",
		},
		{
			name: "存在未定版候选",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-a4")
				if _, _, err := eng.RegisterCandidate("sp-a4", CandidateInput{Proposer: "bob", Body: "FINAL: 方案二", Round: 2}); err != nil {
					t.Fatal(err)
				}
				return fixtureSpace("sp-a4", string(PhaseReview), 0)
			},
			wantField: "candidate",
			wantSub:   "未定版候选 1 条",
		},
		{
			name: "候选均未定版",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				hash := mustRegisterRevision(t, eng, "sp-a5", "v1", 1)
				mustEvidence(t, eng, "sp-a5", hash)
				mustIssueFixed(t, eng, "sp-a5", hash, SeverityMust)
				cand, _, err := eng.RegisterCandidate("sp-a5", CandidateInput{Proposer: "alice", Body: "FINAL: 方案三", Round: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := eng.DismissCandidate("sp-a5", cand.ID, "owner"); err != nil {
					t.Fatal(err)
				}
				return fixtureSpace("sp-a5", string(PhaseReview), 0)
			},
			wantField: "candidate",
			wantSub:   "均未定版",
		},
		{
			name: "必须修问题未清零",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				hash := mustRegisterRevision(t, eng, "sp-a6", "v1", 1)
				mustEvidence(t, eng, "sp-a6", hash)
				mustIssue(t, eng, "sp-a6", hash, SeverityMust)
				mustCandidateFinalized(t, eng, "sp-a6", "FINAL: 方案一")
				return fixtureSpace("sp-a6", string(PhaseReview), 0)
			},
			wantField: "issue",
			wantSub:   "必须修问题未清零",
		},
		{
			name: "should 级不阻塞完成（控制组）",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				hash := mustRegisterRevision(t, eng, "sp-a7", "v1", 1)
				mustEvidence(t, eng, "sp-a7", hash)
				id := mustIssue(t, eng, "sp-a7", hash, SeverityShould)
				// should 问题未修复（still_open），但对当前版本的复审已发生（版本
				// 锚在位）——完成不被 should 阻塞。
				if err := eng.RecordReReview("sp-a7", id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewStillOpen}); err != nil {
					t.Fatal(err)
				}
				mustCandidateFinalized(t, eng, "sp-a7", "FINAL: 方案一")
				return fixtureSpace("sp-a7", string(PhaseReview), 0)
			},
			wantField: "",
		},
		{
			name: "自报证据全链拒绝",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				hash := mustRegisterRevision(t, eng, "sp-a9", "v1", 1)
				// 自报来源 Verified=true：记录面直接拒绝。
				if err := eng.RecordEvidence("sp-a9", hash, LayerCLIExit, Evidence{Verified: true, Source: SourceSelfReport, Ref: "self"}); err == nil {
					t.Fatal("自报标证实应记录面拒绝")
				}
				// 退化为自报未证实：登记成功但判定面永不成立。
				for _, l := range []EvidenceLayer{LayerCLIExit, LayerProtocol, LayerRoundDone, LayerWorkAccept} {
					if err := eng.RecordEvidence("sp-a9", hash, l, Evidence{Verified: false, Source: SourceSelfReport, Ref: "self"}); err != nil {
						t.Fatalf("RecordEvidence %s: %v", string(l), err)
					}
				}
				mustIssueFixed(t, eng, "sp-a9", hash, SeverityMust)
				mustCandidateFinalized(t, eng, "sp-a9", "FINAL: 方案一")
				return fixtureSpace("sp-a9", string(PhaseReview), 0)
			},
			wantField: "evidence",
			wantSub:   "agent 自报不得证实",
		},
		{
			name: "版本不匹配（最近复审非当前版本）",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				hash := mustRegisterRevision(t, eng, "sp-b1", "v1", 1)
				mustEvidence(t, eng, "sp-b1", hash)
				id := mustIssue(t, eng, "sp-b1", hash, SeverityMust)
				// 复审锚在 v1（still_open），随后登记 v2（未修复未复审）：
				// LastReviewedHash(v1) ≠ 当前(v2) → 版本不匹配。
				if err := eng.RecordReReview("sp-b1", id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewStillOpen}); err != nil {
					t.Fatal(err)
				}
				mustRegisterRevision(t, eng, "sp-b1", "v2", 2)
				mustCandidateFinalized(t, eng, "sp-b1", "FINAL: 方案一")
				return fixtureSpace("sp-b1", string(PhaseReview), 0)
			},
			wantField: "version",
			wantSub:   "版本不匹配",
		},
		{
			name: "代码改变旧复审失效",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-b2")
				mustRegisterRevision(t, eng, "sp-b2", "v2", 2) // 新版本未复审
				return fixtureSpace("sp-b2", string(PhaseReview), 0)
			},
			wantField: "version",
			wantSub:   "代码改变旧 review 失效",
		},
		{
			name: "无登记版本",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				return fixtureSpace("sp-b3", string(PhaseReview), 0)
			},
			wantField: "version",
			wantSub:   "无登记版本",
		},
		{
			name: "存在在途执行",
			build: func(t *testing.T, eng *Engine) collab.SpaceView {
				satisfiedFixture(t, eng, "sp-b4")
				return fixtureSpace("sp-b4", string(PhaseReview), 2)
			},
			wantField: "inflight",
			wantSub:   "在途执行 2 项",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := newTestEngine(t)
			space := tc.build(t, eng)
			gaps := eng.CompletionGaps(space)
			verdict := eng.EvaluateCompletion(space)
			if tc.wantField == "" {
				if verdict.Satisfied == false || len(gaps) != 0 {
					t.Fatalf("控制组应零缺口全满足：%+v / %s", gaps, verdict.Reason)
				}
				return
			}
			if verdict.Satisfied {
				t.Fatalf("应拒绝完成（缺口 %d 项）：%s", len(gaps), verdict.Reason)
			}
			wantGap(t, gaps, tc.wantField, tc.wantSub)
			// 缺口清单进 Reason（落 collab 审计事件的人话形态）。
			if !strings.Contains(verdict.Reason, "缺口") || !strings.Contains(verdict.Reason, tc.wantField) {
				t.Fatalf("Reason 缺缺口清单形态：%s", verdict.Reason)
			}
		})
	}
}

// 证据层缺失的精确缺口（四层分离：缺哪层点名哪层）。
func TestCompletionEvidenceLayerMissingPrecise(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-ev"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	// 只登记两层（缺 LayerRoundDone 与 LayerWorkAccept）。
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "cmd-run-1")); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordEvidence(sp, hash, LayerProtocol, verifiedEvidence(SourceProtocolReceipt, "parse-1")); err != nil {
		t.Fatal(err)
	}
	mustIssueFixed(t, eng, sp, hash, SeverityMust)
	mustCandidateFinalized(t, eng, sp, "FINAL: 方案一")
	gaps := eng.CompletionGaps(fixtureSpace(sp, string(PhaseReview), 0))
	wantGap(t, gaps, "evidence", "round_complete=缺失")
	wantGap(t, gaps, "evidence", "work_accept=缺失")
	wantNoGap(t, gaps, "candidate")
	if verdict := eng.EvaluateCompletion(fixtureSpace(sp, string(PhaseReview), 0)); verdict.Satisfied {
		t.Fatal("缺层应拒绝")
	}
}

// 已证实层不降级：后到未证实/自报记录不得覆盖已证实事实。
func TestEvidenceNoDowngradeAfterVerified(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-dg"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "cmd-run-1")); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, Evidence{Verified: false, Source: SourceSelfReport, Ref: "self"}); err == nil {
		t.Fatal("已证实层被未证实覆盖应拒绝")
	}
	// 同级更新（已证实换新回执）允许。
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "cmd-run-2")); err != nil {
		t.Fatalf("同级已证实更新应允许：%v", err)
	}
	snap, _ := eng.Snapshot(sp)
	if got := snap.Revisions[0].TestCLI.Ref; got != "cmd-run-2" {
		t.Fatalf("同级更新应生效，得 %s", got)
	}
}

// 判定面再验：即使构造出「自报 + Verified=true」的记录（绕过记录面），判定仍
// 判未证实——双层把守的第二道。
func TestEvidenceVerifiedJudgmentNeverTrustsSelfReport(t *testing.T) {
	ev := Evidence{Verified: true, Source: SourceSelfReport, Ref: "agent 说全绿"}
	if EvidenceVerified(&ev) {
		t.Fatal("自报来源判定面必须判未证实")
	}
	if SourceSelfReport.CanVerify() {
		t.Fatal("自报来源不得可证实")
	}
	if !ev.Source.Valid() {
		t.Fatal("自报是合法来源取值（只是永不可证实）")
	}
	for _, s := range []EvidenceSource{SourceExecutorReceipt, SourceProtocolReceipt, SourceHostObservation, SourceHumanConfirm} {
		e := Evidence{Verified: true, Source: s, Ref: "r"}
		if !EvidenceVerified(&e) {
			t.Fatalf("来源 %s 已证实应成立", string(s))
		}
	}
}
