package strategy

// 循环阶段模型测试（W05-01 交付③）：全合法边走通 + 7×7 全枚举表外拒绝 +
// 暂停恢复目标校验（沿 collab 九态机先例）。

import (
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// TestPhaseMachineFullEnumeration 7×7 全枚举：合法边恰为转换表（18 条），表外
// 全部拒绝；同相不自转；非法取值拒绝。
func TestPhaseMachineFullEnumeration(t *testing.T) {
	all := []Phase{PhaseDrafting, PhaseFrozen, PhaseReview, PhaseRework, PhaseRereview, PhaseCompleted, PhasePaused}
	edges := PhaseEdges()
	total := 0
	for _, from := range all {
		total += len(edges[from])
	}
	if total != 18 {
		t.Fatalf("合法边数应为 18（drafting2 frozen2 review3 rework2 rereview3 paused6 completed0），得 %d", total)
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, e := range edges[from] {
				if e == to {
					want = true
					break
				}
			}
			if from == to {
				want = false // 同相不自转
			}
			if got := CanAdvancePhase(from, to); got != want {
				t.Fatalf("%s → %s：期望 %v 得 %v", string(from), string(to), want, got)
			}
		}
	}
	// 非法取值/空值。
	if CanAdvancePhase(Phase("nope"), PhaseFrozen) || CanAdvancePhase(PhaseDrafting, Phase("nope")) || CanAdvancePhase("", PhaseFrozen) {
		t.Fatal("非法取值应拒绝")
	}
	// 表外高频误用路径显式抽查。
	illegal := [][2]Phase{
		{PhaseDrafting, PhaseReview},    // 跳过冻结
		{PhaseDrafting, PhaseCompleted}, // 未经审查完成
		{PhaseFrozen, PhaseCompleted},   // 未经审查完成
		{PhaseFrozen, PhaseRework},      // 未审查先返工
		{PhaseRework, PhaseReview},      // 返工不回审查（回复审）
		{PhaseRework, PhaseCompleted},   // 未复审完成
		{PhaseCompleted, PhaseDrafting}, // 终态重入
		{PhaseCompleted, PhasePaused},
		{PhaseDrafting, PhaseRereview},
	}
	for _, p := range illegal {
		if CanAdvancePhase(p[0], p[1]) {
			t.Fatalf("表外路径应拒绝：%s → %s", string(p[0]), string(p[1]))
		}
	}
	// 取值/解析。
	if !PhaseDrafting.Valid() || Phase("").Valid() || Phase("x").Valid() {
		t.Fatal("Phase.Valid 不符")
	}
	if p, ok := ParsePhase("review"); !ok || p != PhaseReview {
		t.Fatal("ParsePhase 合法值不符")
	}
	if _, ok := ParsePhase(""); ok {
		t.Fatal("空阶段不得解析成功（未初始化 ≠ drafting，绝不猜测）")
	}
	if _, ok := ParsePhase("running"); ok {
		t.Fatal("collab 空间九态值不得冒认循环阶段")
	}
}

// TestSetPhaseHappyCycle 全合法边走通：编写→冻结→审查→返工→复审→（循环）→
// 暂停→恢复→完成。
func TestSetPhaseHappyCycle(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-ph1"
	steps := [][2]Phase{
		{PhaseDrafting, PhaseFrozen},
		{PhaseFrozen, PhaseReview},
		{PhaseReview, PhaseRework},
		{PhaseRework, PhaseRereview},
		{PhaseRereview, PhaseCompleted}, // 复审后完成（判据由 finish 把守）
	}
	for i, s := range steps {
		if err := eng.SetPhase(sp, s[0], s[1]); err != nil {
			t.Fatalf("step %d %s→%s: %v", i, string(s[0]), string(s[1]), err)
		}
	}
	// 循环路径：审查→返工→复审→再返工（新一轮问题）。
	const sp2 = "sp-ph2"
	for _, s := range [][2]Phase{
		{PhaseDrafting, PhaseFrozen},
		{PhaseFrozen, PhaseReview},
		{PhaseReview, PhaseRework},
		{PhaseRework, PhaseRereview},
		{PhaseRereview, PhaseRework}, // 复审发现新问题 → 再返工
	} {
		if err := eng.SetPhase(sp2, s[0], s[1]); err != nil {
			t.Fatalf("循环路径 %s→%s: %v", string(s[0]), string(s[1]), err)
		}
	}
}

func TestSetPhasePauseResumeMemory(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-ph3"
	// review → paused：记录暂停前阶段。
	if err := eng.SetPhase(sp, PhaseReview, PhasePaused); err != nil {
		t.Fatal(err)
	}
	snap, _ := eng.Snapshot(sp)
	if snap.PausedFrom != PhaseReview {
		t.Fatalf("PausedFrom 应记 review，得 %s", snap.PausedFrom)
	}
	// 恢复到错误阶段：拒绝（恢复恒回暂停前阶段）。
	if err := eng.SetPhase(sp, PhasePaused, PhaseDrafting); err == nil || !strings.Contains(err.Error(), "暂停恢复") {
		t.Fatalf("恢复到非暂停前阶段应拒绝：%v", err)
	}
	// 恢复到暂停前阶段：通过，PausedFrom 清除。
	if err := eng.SetPhase(sp, PhasePaused, PhaseReview); err != nil {
		t.Fatal(err)
	}
	snap, _ = eng.Snapshot(sp)
	if snap.PausedFrom != Phase("") {
		t.Fatalf("离开暂停应清 PausedFrom，得 %s", snap.PausedFrom)
	}
	// 暂停中人工定版允许（review→paused→completed）。
	if err := eng.SetPhase(sp, PhaseReview, PhasePaused); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetPhase(sp, PhasePaused, PhaseCompleted); err != nil {
		t.Fatalf("暂停中人工定版应允许：%v", err)
	}
}

func TestSetPhaseIllegalRejected(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-ph4"
	if err := eng.SetPhase(sp, PhaseDrafting, PhaseReview); err == nil {
		t.Fatal("跳过冻结应拒绝")
	}
	if err := eng.SetPhase(sp, PhaseFrozen, PhaseCompleted); err == nil {
		t.Fatal("未经审查完成应拒绝")
	}
	if err := eng.SetPhase(sp, PhaseDrafting, PhaseDrafting); err == nil {
		t.Fatal("同相自转应拒绝")
	}
	if err := eng.SetPhase(sp, Phase("junk"), PhaseFrozen); err == nil {
		t.Fatal("非法来源阶段应拒绝")
	}
	// 非法转换不落留痕（拒绝在变更前）。
	snap, ok := eng.Snapshot(sp)
	if ok && len(snap.Trail) != 0 {
		t.Fatalf("拒绝路径不得留痕：%+v", snap.Trail)
	}
}

// DecidePhaseAdvance 条件矩阵（W05-01 交付④第三条）。
func TestDecidePhaseAdvanceMatrix(t *testing.T) {
	t.Run("drafting_无版本不推进", func(t *testing.T) {
		eng := newTestEngine(t)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d1", string(PhaseDrafting), 0))
		if d.Advance {
			t.Fatalf("无版本不得冻结：%+v", d)
		}
	})
	t.Run("drafting_有版本_冻结", func(t *testing.T) {
		eng := newTestEngine(t)
		mustRegisterRevision(t, eng, "sp-d2", "v1", 1)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d2", string(PhaseDrafting), 0))
		if !d.Advance || d.NextPhase != string(PhaseFrozen) {
			t.Fatalf("编写完成应建议冻结：%+v", d)
		}
	})
	t.Run("frozen_无审查角色不推进", func(t *testing.T) {
		eng := newTestEngine(t)
		sp := fixtureSpace("sp-d3", string(PhaseFrozen), 0, func(v *collab.SpaceView) {
			v.Members = []collab.Member{{Name: "alice", Role: "writer", SessionID: "sess-alice"}}
		})
		d := eng.DecidePhaseAdvance(sp)
		if d.Advance {
			t.Fatalf("无审查角色应保持冻结：%+v", d)
		}
	})
	t.Run("frozen_审查角色在位_审查", func(t *testing.T) {
		eng := newTestEngine(t)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d4", string(PhaseFrozen), 0))
		if !d.Advance || d.NextPhase != string(PhaseReview) {
			t.Fatalf("冻结完成应建议审查：%+v", d)
		}
	})
	t.Run("review_有must未解_返工", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-d5", "v1", 1)
		mustIssue(t, eng, "sp-d5", hash, SeverityMust)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d5", string(PhaseReview), 0))
		if !d.Advance || d.NextPhase != string(PhaseRework) {
			t.Fatalf("存在未解 must 应建议返工：%+v", d)
		}
	})
	t.Run("review_有should未解_返工", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-d6", "v1", 1)
		mustIssue(t, eng, "sp-d6", hash, SeverityShould)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d6", string(PhaseReview), 0))
		if !d.Advance || d.NextPhase != string(PhaseRework) {
			t.Fatalf("should 未解也应返工：%+v", d)
		}
	})
	t.Run("review_仅info未解_不推进_审查通过非完成", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-d7", "v1", 1)
		mustIssue(t, eng, "sp-d7", hash, SeverityInfo)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d7", string(PhaseReview), 0))
		if d.Advance || d.NextPhase != "" {
			t.Fatalf("info 不阻塞，审查通过≠完成：%+v", d)
		}
		if !strings.Contains(d.Reason, "完成判定归") {
			t.Fatalf("Reason 须声明完成判定归属：%s", d.Reason)
		}
	})
	t.Run("rework_无新版本不推进", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-d8", "v1", 1)
		id := mustIssue(t, eng, "sp-d8", hash, SeverityMust)
		if err := eng.RecordReReview("sp-d8", id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewStillOpen}); err != nil {
			t.Fatal(err)
		}
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d8", string(PhaseRework), 0))
		if d.Advance {
			t.Fatalf("尚无修复后新版本不得复审：%+v", d)
		}
	})
	t.Run("rework_修复后新版本_复审", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-d9", "v1", 1)
		id := mustIssue(t, eng, "sp-d9", hash, SeverityMust)
		if err := eng.RecordReReview("sp-d9", id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewStillOpen}); err != nil {
			t.Fatal(err)
		}
		mustRegisterRevision(t, eng, "sp-d9", "v2", 2)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-d9", string(PhaseRework), 0))
		if !d.Advance || d.NextPhase != string(PhaseRereview) {
			t.Fatalf("修复后新版本应建议复审：%+v", d)
		}
	})
	t.Run("rereview_新阻塞问题_再返工", func(t *testing.T) {
		eng := newTestEngine(t)
		hash := mustRegisterRevision(t, eng, "sp-da", "v1", 1)
		mustRegisterRevision(t, eng, "sp-da", "v2", 2)
		mustIssue(t, eng, "sp-da", hash, SeverityMust) // 针对 v2 提出的新问题
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-da", string(PhaseRereview), 0))
		if !d.Advance || d.NextPhase != string(PhaseRework) {
			t.Fatalf("复审发现新问题应再返工：%+v", d)
		}
	})
	t.Run("rereview_无阻塞_等待完成判据", func(t *testing.T) {
		eng := newTestEngine(t)
		mustRegisterRevision(t, eng, "sp-db", "v1", 1)
		d := eng.DecidePhaseAdvance(fixtureSpace("sp-db", string(PhaseRereview), 0))
		if d.Advance {
			t.Fatalf("复审通过不推进（完成判定归 finish/finalize）：%+v", d)
		}
	})
	t.Run("paused_completed_未知_不推进", func(t *testing.T) {
		eng := newTestEngine(t)
		for _, ph := range []string{string(PhasePaused), string(PhaseCompleted), "", "junk"} {
			d := eng.DecidePhaseAdvance(fixtureSpace("sp-dc", ph, 0))
			if d.Advance {
				t.Fatalf("阶段 %q 不得推进：%+v", ph, d)
			}
		}
	})
}
