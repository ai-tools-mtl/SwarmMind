package strategy

// 建议4：循环事实恢复入口测试——Snapshot → 新 Engine RestoreLoop → 状态语义
// 等价（暂停恢复可用/完成判定一致/停止判定一致）；损坏快照拒绝。

import (
	"reflect"
	"strings"
	"testing"
)

// 往返等价：快照 → 恢复 → 快照深等价；判定面（完成/停止/阶段推进）结论一致；
// 暂停恢复记忆（PausedFrom）在恢复后可用。
func TestRestoreLoopRoundTripEquivalence(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-rs1"
	satisfiedFixture(t, eng, sp)
	// 叠加：should 问题（独立 key：不同文件行/标题——同 key 会触发重开投影）、
	// 干净复审、暂停态、争议（覆盖全部恢复字段的往返）。
	shouldID, err := eng.RecordIssue(sp, IssueInput{
		Reviewer: "bob", Severity: SeverityShould, File: "internal/foo/baz.go", Line: 7,
		Title: "命名不一致", FoundInHash: mkhash("v1"), FoundInRound: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReplyIssue(sp, shouldID.ID, "本轮处理"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordCleanReview(sp, "bob", mkhash("v1"), "补一轮零发现复审"); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetPhase(sp, PhaseReview, PhasePaused); err != nil {
		t.Fatal(err)
	}
	eng.SetAcceptanceDispute(sp, true, "验收口径分歧")
	snapA, ok := eng.Snapshot(sp)
	if !ok {
		t.Fatal("快照缺失")
	}
	// 新引擎恢复。
	eng2, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := eng2.RestoreLoop(sp, snapA); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	snapB, ok := eng2.Snapshot(sp)
	if !ok {
		t.Fatal("恢复后快照缺失")
	}
	if !reflect.DeepEqual(snapA, snapB) {
		t.Fatal("恢复快照须与原快照深等价")
	}
	// 语义等价①：完成判定一致（暂停阶段前提允许 + 全满足 fixture → satisfied）。
	spacePaused := fixtureSpace(sp, string(PhasePaused), 0)
	vA, vB := eng.EvaluateCompletion(spacePaused), eng2.EvaluateCompletion(spacePaused)
	if vA.Satisfied != vB.Satisfied || !vB.Satisfied {
		t.Fatalf("完成判定不一致：A=%v B=%v（%s / %s）", vA.Satisfied, vB.Satisfied, vA.Reason, vB.Reason)
	}
	// 语义等价②：暂停恢复可用（PausedFrom 随恢复携带）。
	if err := eng2.SetPhase(sp, PhasePaused, PhaseReview); err != nil {
		t.Fatalf("恢复后暂停恢复不可用：%v", err)
	}
	// 语义等价③：停止判定一致（争议触发）。
	now := newFakeClock().Now()
	evA, evB := eng.EvaluateStop(fixtureSpace(sp, string(PhasePaused), 0), now), eng2.EvaluateStop(fixtureSpace(sp, string(PhasePaused), 0), now)
	if evA.ShouldPause != evB.ShouldPause || !evB.ShouldPause {
		t.Fatalf("停止判定不一致：A=%+v B=%+v", evA, evB)
	}
	// 语义等价④：阶段推进判定一致（恢复后 should 未解问题照常触发返工建议）。
	dA, dB := eng.DecidePhaseAdvance(fixtureSpace(sp, string(PhaseReview), 0)), eng2.DecidePhaseAdvance(fixtureSpace(sp, string(PhaseReview), 0))
	if dA.Advance != dB.Advance || dA.NextPhase != dB.NextPhase {
		t.Fatalf("阶段推进判定不一致：A=%+v B=%+v", dA, dB)
	}
}

// 损坏拒绝：ID 重复/字段非法/hash 非法/序号倒挂/目标已占用，一律拒绝且零副作用。
func TestRestoreLoopRejectsCorrupt(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-rs2"
	satisfiedFixture(t, eng, sp)
	snap, _ := eng.Snapshot(sp)

	mutate := func(f func(*SpaceLoop)) SpaceLoop {
		s := SpaceLoop{
			SpaceID: snap.SpaceID, LastReviewedHash: snap.LastReviewedHash,
			LastReviewedAt: snap.LastReviewedAt, PausedFrom: snap.PausedFrom,
		}
		s.Candidates = append([]Candidate(nil), snap.Candidates...)
		s.Revisions = append([]Revision(nil), snap.Revisions...)
		// Issue 内部切片（Fixes/ReReviews）必须逐条深拷贝——浅拷贝共享底层数组，
		// 用例间变异会串写共享快照。
		for _, is := range snap.Issues {
			cp := is
			cp.Fixes = append([]FixAttempt(nil), is.Fixes...)
			cp.ReReviews = append([]ReReviewRecord(nil), is.ReReviews...)
			s.Issues = append(s.Issues, cp)
		}
		s.CleanReviews = append([]CleanReviewRecord(nil), snap.CleanReviews...)
		s.Trail = append([]TrailEvent(nil), snap.Trail...)
		if snap.Dispute != nil {
			d := *snap.Dispute
			s.Dispute = &d
		}
		f(&s)
		return s
	}

	cases := []struct {
		name    string
		spaceID string
		snap    SpaceLoop
		sub     string
	}{
		{"空间不匹配", "sp-other", snap, "≠ 目标空间"},
		{"候选 ID 重复", sp, mutate(func(s *SpaceLoop) {
			s.Candidates = append(s.Candidates, s.Candidates[0])
		}), "候选 ID 非法或重复"},
		{"候选状态非法", sp, mutate(func(s *SpaceLoop) {
			s.Candidates[0].Status = "gone"
		}), "字段非法"},
		{"版本 hash 非法", sp, mutate(func(s *SpaceLoop) {
			s.Revisions[0].ContentSHA256 = "nothex"
		}), "hash 格式非法"},
		{"问题级别非法", sp, mutate(func(s *SpaceLoop) {
			s.Issues[0].Severity = "huge"
		}), "字段非法"},
		{"问题序号倒挂", sp, mutate(func(s *SpaceLoop) {
			s.Issues[0].LastReportedSeq = s.Issues[0].EventSeq + 1
		}), "事件序号"},
		{"复审记录序号越界", sp, mutate(func(s *SpaceLoop) {
			s.Issues[0].ReReviews[0].Seq = s.Issues[0].EventSeq + 10
		}), "复审记录非法"},
		{"修复记录 hash 非法", sp, mutate(func(s *SpaceLoop) {
			s.Issues[0].Fixes[0].VersionHash = "nothex"
		}), "修复记录 hash 非法"},
		{"PausedFrom 非法", sp, mutate(func(s *SpaceLoop) {
			s.PausedFrom = Phase("junk")
		}), "PausedFrom 非法"},
		{"复审锚 hash 非法", sp, mutate(func(s *SpaceLoop) {
			s.LastReviewedHash = "nothex"
		}), "last_reviewed_hash 格式非法"},
		{"干净复审 hash 非法", sp, mutate(func(s *SpaceLoop) {
			s.CleanReviews = append(s.CleanReviews, CleanReviewRecord{Reviewer: "bob", AgainstHash: "nothex"})
		}), "零发现复审记录 hash 非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh, err := NewEngine(DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			err = fresh.RestoreLoop(tc.spaceID, tc.snap)
			if err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("损坏快照应拒绝（含 %q），得 %v", tc.sub, err)
			}
			// 拒绝零副作用：未建空间事实。
			if _, ok := fresh.Snapshot(tc.spaceID); ok {
				t.Fatal("拒绝后不得建空间事实")
			}
		})
	}
	// 目标空间已占用拒绝。
	if err := eng.RestoreLoop(sp, snap); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("目标已占用应拒绝：%v", err)
	}
}

// TestRestoreLoopTrailSeqStrictness 留痕序号收紧锚（终审建议1）：序号形态二值——
// 混序（部分零部分非零）/负高水位/矛盾（FactSeq=0 有非零序号）拒绝且零副作用；
// 旧格式（全零 Trail 且 FactSeq==0）整体容忍，恢复后 RecordsSince 对零序号最旧
// 事件 prunedBefore=0（负上界不作为游标语义透出）。
func TestRestoreLoopTrailSeqStrictness(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-rs3"
	mk := func(seqs []int64, fact int64) SpaceLoop {
		s := SpaceLoop{SpaceID: sp, FactSeq: fact}
		for _, q := range seqs {
			s.Trail = append(s.Trail, TrailEvent{Seq: q, Kind: TrailPhaseChanged, Note: "锚点事件"})
		}
		return s
	}
	for _, tc := range []struct {
		name string
		snap SpaceLoop
		sub  string
	}{
		{"混序拒绝", mk([]int64{1, 0, 2}, 2), "混序"},
		{"负高水位拒绝", mk(nil, -1), "负值"},
		{"矛盾拒绝", mk([]int64{1, 2}, 0), "超高水位"},
		{"全零高水位矛盾拒绝", mk([]int64{0, 0}, 7), "矛盾"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh, err := NewEngine(DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.RestoreLoop(sp, tc.snap); err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("应拒绝（含 %q），得 %v", tc.sub, err)
			}
			if _, ok := fresh.Snapshot(sp); ok {
				t.Fatal("拒绝零副作用：不得建空间事实")
			}
		})
	}
	// 旧格式容忍 + 就地迁移：全零 Trail + FactSeq==0 → 重编 1..n。
	if err := eng.RestoreLoop(sp, mk([]int64{0, 0, 0}, 0)); err != nil {
		t.Fatalf("旧格式全零应容忍: %v", err)
	}
	snapM, ok := eng.Snapshot(sp)
	if !ok {
		t.Fatal("旧格式恢复后应有空间事实")
	}
	if snapM.FactSeq != 3 {
		t.Fatalf("迁移应置 FactSeq=窗口长度: %d", snapM.FactSeq)
	}
	for i, ev := range snapM.Trail {
		if ev.Seq != int64(i+1) {
			t.Fatalf("迁移应按切片序重编 1..n: Trail[%d].Seq=%d", i, ev.Seq)
		}
	}
	if _, pruned := eng.RecordsSince(sp, 0); pruned != 0 {
		t.Fatalf("迁移窗口最旧序号 1 prunedBefore 应为 0: %d", pruned)
	}
}

// TestRestoreLoopLegacyMigration 旧格式迁移回归（终审必修6②，codex 反例闭环）：
// [0,0]/FactSeq=0 恢复（迁移 1..2）→ 正常 appendTrail（SetPhase 留痕从 3 续）→
// Snapshot 全序号连续严格递增（不再产出混序）→ 新引擎 RestoreLoop 成功（写入器的
// 数据不被自己的读者拒收——原洞：恢复后追加产出 [0,0,1]/FactSeq=1 混序被再恢复
// 拒「写入器损坏」自锁）；迁移后续号不撞；大 n 抽查（600 条零序号 > MaxTrailEvents
// 裁剪窗——迁移对裁剪后窗口重编自洽：窗口内 1..600 连续、追加 601.. 续号、再裁剪
// 后窗口与 FactSeq/RecordsSince 上界一致，快照对新引擎可恢复）。
func TestRestoreLoopLegacyMigration(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-rs4"
	legacy := SpaceLoop{SpaceID: sp}
	legacy.Trail = append(legacy.Trail, TrailEvent{Kind: TrailPhaseChanged, Note: "旧事件一"}, TrailEvent{Kind: TrailPhaseChanged, Note: "旧事件二"})
	if err := eng.RestoreLoop(sp, legacy); err != nil {
		t.Fatalf("旧格式恢复应成功: %v", err)
	}
	// 正常追加：迁移后从 n+1=3 续号，不撞。
	if err := eng.SetPhase(sp, PhaseDrafting, PhasePaused); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	snap, ok := eng.Snapshot(sp)
	if !ok {
		t.Fatal("快照缺失")
	}
	if snap.FactSeq != 3 || len(snap.Trail) != 3 {
		t.Fatalf("迁移+追加后应 FactSeq=3/Trail=3: %d/%d", snap.FactSeq, len(snap.Trail))
	}
	for i := 1; i < len(snap.Trail); i++ {
		if snap.Trail[i].Seq != snap.Trail[i-1].Seq+1 {
			t.Fatalf("序号应连续严格递增（迁移+续号）: %+v", snap.Trail)
		}
	}
	// 写入器的数据被自己的读者接受：新引擎恢复迁移产物成功。
	eng2, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := eng2.RestoreLoop(sp, snap); err != nil {
		t.Fatalf("迁移产物应可再恢复（不得产出混序自锁）: %v", err)
	}
	// 大 n 抽查：600 条零序号（> MaxTrailEvents=512 裁剪窗）。
	const bigN = 600
	eng3, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	big := SpaceLoop{SpaceID: sp}
	for i := 0; i < bigN; i++ {
		big.Trail = append(big.Trail, TrailEvent{Kind: TrailCandidateRestated, Note: "旧批量事件"})
	}
	if err := eng3.RestoreLoop(sp, big); err != nil {
		t.Fatalf("大 n 旧格式恢复应成功: %v", err)
	}
	bigSnap, ok := eng3.Snapshot(sp)
	if !ok || bigSnap.FactSeq != bigN || len(bigSnap.Trail) != bigN {
		t.Fatalf("大 n 迁移应 FactSeq=%d/Trail=%d: %d/%d ok=%v", bigN, bigN, bigSnap.FactSeq, len(bigSnap.Trail), ok)
	}
	for i := 1; i < len(bigSnap.Trail); i++ {
		if bigSnap.Trail[i].Seq != bigSnap.Trail[i-1].Seq+1 {
			t.Fatalf("大 n 迁移窗口应连续 1..n: Trail[%d].Seq=%d", i, bigSnap.Trail[i].Seq)
		}
	}
	// 续号越过裁剪窗：追加 3 条 → Seq 601..603，窗口裁至最新 512 条仍连续，FactSeq=603。
	for i := 0; i < 3; i++ {
		if err := eng3.SetPhase(sp, PhaseDrafting, PhasePaused); err != nil {
			t.Fatalf("追加 %d: %v", i, err)
		}
		if err := eng3.SetPhase(sp, PhasePaused, PhaseDrafting); err != nil {
			t.Fatalf("回拉 %d: %v", i, err)
		}
	}
	after, ok := eng3.Snapshot(sp)
	if !ok || after.FactSeq != bigN+6 || len(after.Trail) != MaxTrailEvents {
		t.Fatalf("裁剪后应 FactSeq=%d/窗口=%d: %d/%d ok=%v", bigN+6, MaxTrailEvents, after.FactSeq, len(after.Trail), ok)
	}
	for i := 1; i < len(after.Trail); i++ {
		if after.Trail[i].Seq != after.Trail[i-1].Seq+1 {
			t.Fatalf("裁剪窗口应连续严格递增: Trail[%d].Seq=%d", i, after.Trail[i].Seq)
		}
	}
	// 读者一致性：裁剪窗快照对新引擎可恢复；RecordsSince 上界=窗口最旧序号-1。
	eng4, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := eng4.RestoreLoop(sp, after); err != nil {
		t.Fatalf("裁剪窗快照应可恢复: %v", err)
	}
	_, pruned := eng4.RecordsSince(sp, 0)
	if pruned != after.Trail[0].Seq-1 {
		t.Fatalf("prunedBefore 应=窗口最旧序号-1: %d vs %d", pruned, after.Trail[0].Seq-1)
	}
}
