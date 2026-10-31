package strategy

// 审查/证据 schema 测试（W05-01 交付②）：issue 稳定 ID / 级别 / 修复次数推进 /
// 复审结论推进 / 版本变更旧 review 失效留痕 / 版本 ID 稳定 / 证据来源分层把守。

import (
	"errors"
	"strings"
	"testing"
)

func TestRevisionRegistrationAndStableID(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-r1"
	// 参数校验：hash 格式/基线/作者。
	if _, _, err := eng.RegisterRevision(sp, RevisionInput{ContentSHA256: "nothex", Baseline: "b", Author: "a"}); !errors.Is(err, ErrRevisionInvalid) {
		t.Fatalf("hash 格式错应拒绝：%v", err)
	}
	if _, _, err := eng.RegisterRevision(sp, RevisionInput{ContentSHA256: mkhash("v1"), Baseline: "", Author: "a"}); !errors.Is(err, ErrRevisionInvalid) {
		t.Fatalf("缺基线应拒绝：%v", err)
	}
	if _, _, err := eng.RegisterRevision(sp, RevisionInput{ContentSHA256: mkhash("v1"), Baseline: "b", Author: ""}); !errors.Is(err, ErrRevisionInvalid) {
		t.Fatalf("缺作者应拒绝：%v", err)
	}
	rev1, created, err := eng.RegisterRevision(sp, RevisionInput{
		Baseline: "main@abc", ContentSHA256: mkhash("v1"), PatchSHA256: mkhash("p1"),
		AuthorRound: 1, Author: "alice",
		Dependencies: []ArtifactRef{{Kind: "artifact", Ref: "art-1", SHA256: mkhash("dep")}},
	})
	if err != nil || !created {
		t.Fatalf("登记失败：%v created=%v", err, created)
	}
	// 版本 ID 稳定（AC13 两 ID 之一）：同内容同补丁重复登记幂等（同版本）。
	same, created2, err := eng.RegisterRevision(sp, RevisionInput{
		Baseline: "main@abc", ContentSHA256: mkhash("v1"), PatchSHA256: mkhash("p1"),
		AuthorRound: 2, Author: "alice",
	})
	if err != nil || created2 || same.ID != rev1.ID {
		t.Fatalf("同内容同补丁应幂等同版本：created=%v id=%s/%s err=%v", created2, same.ID, rev1.ID, err)
	}
	if eng.CurrentRevisionHash(sp) != mkhash("v1") {
		t.Fatalf("当前版本 hash 不符：%s", eng.CurrentRevisionHash(sp))
	}
	// 新版本（补丁不同 → 版本不同）。
	rev2, created3, err := eng.RegisterRevision(sp, RevisionInput{
		Baseline: "main@def", ContentSHA256: mkhash("v1"), PatchSHA256: mkhash("p2"),
		AuthorRound: 2, Author: "alice",
	})
	if err != nil || !created3 || rev2.ID == rev1.ID {
		t.Fatalf("补丁不同应产生新版本：%v", err)
	}
}

func TestIssueStableIDAndProgression(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-r2"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssue(t, eng, sp, hash, SeverityMust)
	if !strings.HasPrefix(id, "iss-") || len(id) != len("iss-")+12 {
		t.Fatalf("问题 ID 形态不符：%q", id)
	}
	// 稳定 ID：同（提出者,文件:行,标题）重报 → 同 ID（Occurrences 推进，不新建）。
	dup, err := eng.RecordIssue(sp, IssueInput{
		Reviewer: "bob", Severity: SeverityMust, File: "internal/foo/bar.go", Line: 42,
		Title: "边界检查缺失", FoundInHash: hash, FoundInRound: 1,
	})
	if err != nil || dup.ID != id || dup.Occurrences != 2 {
		t.Fatalf("同 key 重报应同 ID：id=%s/%s occ=%d err=%v", dup.ID, id, dup.Occurrences, err)
	}
	// 不同行 → 不同 ID（新问题）。
	other, err := eng.RecordIssue(sp, IssueInput{
		Reviewer: "bob", Severity: SeverityShould, File: "internal/foo/bar.go", Line: 43,
		Title: "命名不一致", FoundInHash: hash, FoundInRound: 1,
	})
	if err != nil || other.ID == id {
		t.Fatalf("不同位置应不同问题 ID：%s", other.ID)
	}
	snap, _ := eng.Snapshot(sp)
	if len(snap.Issues) != 2 {
		t.Fatalf("问题应 2 条：%d", len(snap.Issues))
	}
	// 级别落档。
	if snap.Issues[0].Severity != SeverityMust || snap.Issues[1].Severity != SeverityShould {
		t.Fatalf("级别不符：%+v", snap.Issues)
	}
	// 修复次数推进：声明 1 次（pending）→ 复审确认。
	if err := eng.ClaimFix(sp, id, hash, 1, "补上边界检查"); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if len(snap.Issues[0].Fixes) != 1 || snap.Issues[0].Fixes[0].Result != "" {
		t.Fatalf("修复声明应为待复核：%+v", snap.Issues[0].Fixes)
	}
	// 复审登记前的结论直写拒绝（invalidated 是投影态不可人工直写）。
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: "invalidated"}); !errors.Is(err, ErrReReviewInvalid) {
		t.Fatalf("invalidated 直写应拒绝：%v", err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if got := IssueEffectiveState(&snap.Issues[0], hash); got != IssueStillOpen {
		t.Fatalf("复审仍未解应 still_open，得 %s", got)
	}
	if !IssueBlocking(&snap.Issues[0], hash) {
		t.Fatal("must+still_open 应阻塞")
	}
	// 回复次数推进。
	if err := eng.ReplyIssue(sp, id, "本轮已重写边界分支"); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if snap.Issues[0].Replies != 1 {
		t.Fatalf("回复次数应 1：%d", snap.Issues[0].Replies)
	}
	// 第二次修复 + 对当前版本复审确认 → 阻塞解除。
	if err := eng.ClaimFix(sp, id, hash, 2, "二次修复"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewConfirmed}); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if got := IssueEffectiveState(&snap.Issues[0], hash); got != IssueConfirmedFixed {
		t.Fatalf("确认已修应 confirmed_fixed，得 %s", got)
	}
	if IssueBlocking(&snap.Issues[0], hash) {
		t.Fatal("确认已修不应阻塞")
	}
	if len(snap.Issues[0].Fixes) != 2 {
		t.Fatalf("修复次数应 2：%d", len(snap.Issues[0].Fixes))
	}
}

// 版本变更旧 review 失效留痕：新版本登记 → 旧确认进失效投影（stale），留痕
// 事件记旧/新版本与受影响问题；原复审记录按序保留不抹改。
func TestVersionChangeInvalidatesOldReviewWithTrail(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-r3"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssueFixed(t, eng, sp, v1, SeverityMust) // 修复闭环（对 v1 确认）
	snap, _ := eng.Snapshot(sp)
	before := len(snap.Issues[0].ReReviews)
	if before == 0 {
		t.Fatal("复审记录应存在")
	}
	// 新版本登记（代码改变）。
	v2 := mustRegisterRevision(t, eng, sp, "v2", 2)
	snap, _ = eng.Snapshot(sp)
	// 失效投影：对 v1 的确认 → stale（阻塞回归）。
	if got := IssueEffectiveState(&snap.Issues[0], v2); got != IssueStale {
		t.Fatalf("旧确认应失效投影 stale，得 %s", got)
	}
	if !IssueBlocking(&snap.Issues[0], v2) {
		t.Fatal("失效后应重新阻塞")
	}
	// 留痕：trail 含失效事件（旧/新版本 + 问题 ID；事件记尾 12 位 hex）。
	found := false
	for _, ev := range snap.Trail {
		if ev.Kind == TrailReviewInvalidated {
			found = true
			if !strings.Contains(ev.Note, tail(v1, 12)) || !strings.Contains(ev.Note, tail(v2, 12)) || !strings.Contains(ev.Note, id) {
				t.Fatalf("失效留痕缺旧/新版本或问题 ID：%s", ev.Note)
			}
		}
	}
	if !found {
		t.Fatal("缺 TrailReviewInvalidated 留痕")
	}
	// 原复审记录保留（不抹改）。
	if len(snap.Issues[0].ReReviews) != before || snap.Issues[0].ReReviews[0].Result != ReReviewConfirmed {
		t.Fatalf("原复审记录须保留：%+v", snap.Issues[0].ReReviews)
	}
	// 对新版本复审 → 确认恢复，版本锚推进 v2。
	if err := eng.ClaimFix(sp, id, v2, 2, "随新版本重验"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v2, Result: ReReviewConfirmed}); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if got := IssueEffectiveState(&snap.Issues[0], v2); got != IssueConfirmedFixed {
		t.Fatalf("新版本确认应恢复，得 %s", got)
	}
	if snap.LastReviewedHash != v2 || snap.LastReviewedAt.IsZero() {
		t.Fatalf("最近复审锚应推进 v2：%s", snap.LastReviewedHash)
	}
	// 完成判据版本缺口消失（其余输入补齐后）。
	mustCandidateFinalized(t, eng, sp, "FINAL: 方案一")
	gaps := eng.CompletionGaps(fixtureSpace(sp, string(PhaseReview), 0))
	wantNoGap(t, gaps, "version")
	wantNoGap(t, gaps, "issue")
}

func TestEvidenceRecordingRules(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-r4"
	hash := mustRegisterRevision(t, eng, sp, "v1", 1)
	// 未登记版本锚定拒绝。
	if err := eng.RecordEvidence(sp, mkhash("ghost"), LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "x")); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("证据须绑定已登记版本：%v", err)
	}
	// 未知层/未知来源。
	if err := eng.RecordEvidence(sp, hash, "nope", verifiedEvidence(SourceExecutorReceipt, "x")); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("未知层应拒绝：%v", err)
	}
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, Evidence{Verified: true, Source: "twitter", Ref: "x"}); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("未知来源应拒绝：%v", err)
	}
	// 缺定位 Ref。
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, Evidence{Verified: true, Source: SourceExecutorReceipt}); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("缺 Ref 应拒绝：%v", err)
	}
	// 自报标证实：记录面拒绝。
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, Evidence{Verified: true, Source: SourceSelfReport, Ref: "self"}); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("自报标证实应拒绝：%v", err)
	}
	// 回执证实：通过并落档到对应分离字段。
	if err := eng.RecordEvidence(sp, hash, LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "cmd-run-1")); err != nil {
		t.Fatal(err)
	}
	snap, _ := eng.Snapshot(sp)
	rev := snap.Revisions[0]
	if !EvidenceVerified(rev.TestCLI) || rev.TestProto != nil {
		t.Fatalf("四层分离落档不符：cli=%+v proto=%+v", rev.TestCLI, rev.TestProto)
	}
}

// 证据绑定版本：hash 变更后旧版本证据不迁移（新版本须重新证实——证据绑定真实
// 执行回执/hash）。
func TestEvidenceBoundToVersion(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-r5"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	mustEvidence(t, eng, sp, v1)
	v2 := mustRegisterRevision(t, eng, sp, "v2", 2)
	gaps := eng.CompletionGaps(fixtureSpace(sp, string(PhaseReview), 0))
	wantGap(t, gaps, "evidence", "cli_exit=缺失") // v2 无任何证据
	_ = v2
}
