package strategy

// 停止/暂停判定测试（W05-01 交付⑤）：预算三分离各自触发 / 同一问题两次未解
// （默认阈值 2，可配置）/ 验收争议。

import (
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// hasTrigger 断言触发清单含指定种类（并抽查细节）。
func hasTrigger(t *testing.T, ev StopEvaluation, kind StopKind, detailSub string) {
	t.Helper()
	for _, tr := range ev.Triggers {
		if tr.Kind == kind {
			if detailSub != "" && !strings.Contains(tr.Detail, detailSub) {
				t.Fatalf("触发 [%s] 细节不含 %q：%s", string(kind), detailSub, tr.Detail)
			}
			return
		}
	}
	t.Fatalf("触发清单缺 %s：%+v", string(kind), ev.Triggers)
}

func noTriggerKind(t *testing.T, ev StopEvaluation, kind StopKind) {
	t.Helper()
	for _, tr := range ev.Triggers {
		if tr.Kind == kind {
			t.Fatalf("不应触发 %s：%+v", string(kind), ev.Triggers)
		}
	}
}

func TestStopBudgetThreeSeparateCounters(t *testing.T) {
	eng := newTestEngine(t)
	now := newFakeClock().Now()
	// 零值预算 = 不限制：不触发。
	ev := eng.EvaluateStop(fixtureSpace("sp-s1", string(PhaseDrafting), 0), now)
	if ev.ShouldPause {
		t.Fatalf("不限预算不得触发：%+v", ev.Triggers)
	}
	// 消息预算：恰好达到上限即触发（三分离——另两计数不动也触发）。
	sp := fixtureSpace("sp-s2", string(PhaseDrafting), 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxMessages: 10}
		v.Used = collab.BudgetUsed{Messages: 10}
	})
	ev = eng.EvaluateStop(sp, now)
	if !ev.ShouldPause {
		t.Fatal("消息预算耗尽应触发")
	}
	hasTrigger(t, ev, StopBudgetMessages, "10/10")
	noTriggerKind(t, ev, StopBudgetRounds)
	noTriggerKind(t, ev, StopBudgetWall)
	// 派发轮次预算。
	sp = fixtureSpace("sp-s3", string(PhaseDrafting), 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxDispatchRounds: 3}
		v.Used = collab.BudgetUsed{DispatchRounds: 3}
	})
	ev = eng.EvaluateStop(sp, now)
	hasTrigger(t, ev, StopBudgetRounds, "3/3")
	// 墙钟预算（未结算段计入：WallSeconds 70 + running 30s = 100 ≥ 95）。
	sp = fixtureSpace("sp-s4", string(PhaseDrafting), 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxWallSeconds: 95}
		v.Used = collab.BudgetUsed{WallSeconds: 70, RunningSince: now.Add(-30 * time.Second)}
	})
	ev = eng.EvaluateStop(sp, now)
	hasTrigger(t, ev, StopBudgetWall, "100/95")
	// 未达上限：不触发（69+30=99... 对照：95 上限，结算 60+30=90 < 95）。
	sp = fixtureSpace("sp-s5", string(PhaseDrafting), 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxWallSeconds: 95}
		v.Used = collab.BudgetUsed{WallSeconds: 60, RunningSince: now.Add(-30 * time.Second)}
	})
	if ev := eng.EvaluateStop(sp, now); ev.ShouldPause {
		t.Fatalf("预算未耗尽不得触发：%+v", ev.Triggers)
	}
}

func TestStopFixRoundsThreshold(t *testing.T) {
	// 默认阈值 2（SwarmMind 规则）：同一问题修复两次未过复审 → 触发。
	eng := newTestEngine(t)
	const sp = "sp-s6"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssue(t, eng, sp, v1, SeverityMust)
	space := fixtureSpace(sp, string(PhaseRework), 0)
	// 第一次未解：不触发。
	if err := eng.ClaimFix(sp, id, v1, 1, "修一"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	if ev := eng.EvaluateStop(space, newFakeClock().Now()); ev.ShouldPause {
		t.Fatalf("一次未解不得触发（阈值 2）：%+v", ev.Triggers)
	}
	// 第二次未解：触发 + 上报形态留痕。
	if err := eng.ClaimFix(sp, id, v1, 2, "修二"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	ev := eng.EvaluateStop(space, newFakeClock().Now())
	if !ev.ShouldPause {
		t.Fatal("两次未解应触发暂停判定")
	}
	hasTrigger(t, ev, StopFixRounds, "2 次修复未过复审")
	// must 才计次？——任一级别问题都触发停止（意见两次未解是通用规则）；此处
	// should 级同样计次。
	const sp2 = "sp-s7"
	eng2 := newTestEngine(t)
	w1 := mustRegisterRevision(t, eng2, sp2, "w1", 1)
	id2 := mustIssue(t, eng2, sp2, w1, SeverityShould)
	for i := int64(1); i <= 2; i++ {
		if err := eng2.ClaimFix(sp2, id2, w1, i, "修"); err != nil {
			t.Fatal(err)
		}
		if err := eng2.RecordReReview(sp2, id2, ReReviewInput{Reviewer: "bob", AgainstHash: w1, Result: ReReviewStillOpen}); err != nil {
			t.Fatal(err)
		}
	}
	if ev := eng2.EvaluateStop(fixtureSpace(sp2, string(PhaseRework), 0), newFakeClock().Now()); !ev.ShouldPause {
		t.Fatal("should 级两次未解同样触发")
	}
}

func TestStopFixRoundsConfigurableLimit(t *testing.T) {
	// 其他项目显式配置阈值 3：两次未解不触发，第三次才触发。
	eng, err := NewEngine(Config{MaxUnresolvedFixRounds: 3, Now: newFakeClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	const sp = "sp-s8"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssue(t, eng, sp, v1, SeverityMust)
	space := fixtureSpace(sp, string(PhaseRework), 0)
	for i := int64(1); i <= 2; i++ {
		if err := eng.ClaimFix(sp, id, v1, i, "修"); err != nil {
			t.Fatal(err)
		}
		if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
			t.Fatal(err)
		}
	}
	if ev := eng.EvaluateStop(space, newFakeClock().Now()); ev.ShouldPause {
		t.Fatalf("阈值 3：两次未解不得触发：%+v", ev.Triggers)
	}
	if err := eng.ClaimFix(sp, id, v1, 3, "修三"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v1, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	if ev := eng.EvaluateStop(space, newFakeClock().Now()); !ev.ShouldPause {
		t.Fatal("阈值 3：三次未解应触发")
	}
	// 负值配置拒绝。
	if _, err := NewEngine(Config{MaxUnresolvedFixRounds: -1}); err == nil {
		t.Fatal("负阈值应拒绝装配")
	}
}

// 失效确认计入未解：确认因版本变更作废 = 仍未解（防反复改版绕过阈值）。
func TestStopInvalidatedConfirmationCountsAsUnresolved(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-s9"
	v1 := mustRegisterRevision(t, eng, sp, "v1", 1)
	id := mustIssueFixed(t, eng, sp, v1, SeverityMust) // 对 v1 确认
	// 版本变更 → 确认失效 → 计入一次未解。
	v2 := mustRegisterRevision(t, eng, sp, "v2", 2)
	// 再补一次显式未解 → 累计两次 → 触发。
	if err := eng.ClaimFix(sp, id, v2, 2, "随 v2 重修"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordReReview(sp, id, ReReviewInput{Reviewer: "bob", AgainstHash: v2, Result: ReReviewStillOpen}); err != nil {
		t.Fatal(err)
	}
	ev := eng.EvaluateStop(fixtureSpace(sp, string(PhaseRework), 0), newFakeClock().Now())
	if !ev.ShouldPause {
		t.Fatal("失效确认 + 显式未解应累计达阈值触发")
	}
	hasTrigger(t, ev, StopFixRounds, "")
	_ = v2
}

func TestStopAcceptanceDispute(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-sa"
	eng.SetAcceptanceDispute(sp, true, "验收口径分歧：交付物范围未定")
	ev := eng.EvaluateStop(fixtureSpace(sp, string(PhaseReview), 0), newFakeClock().Now())
	if !ev.ShouldPause {
		t.Fatal("验收争议应触发暂停判定")
	}
	hasTrigger(t, ev, StopAcceptanceDispute, "验收口径分歧")
	// 解除后不再触发。
	eng.SetAcceptanceDispute(sp, false, "口径已对齐")
	if ev := eng.EvaluateStop(fixtureSpace(sp, string(PhaseReview), 0), newFakeClock().Now()); ev.ShouldPause {
		t.Fatalf("争议解除不得触发：%+v", ev.Triggers)
	}
}

// 暂停保留进度：停止判定是纯读——判定后循环事实（候选/版本/问题/留痕）原样
// 可查（不删材料，恢复后继续）。
func TestStopPreservesProgress(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-sb"
	satisfiedFixture(t, eng, sp)
	eng.SetAcceptanceDispute(sp, true, "争议")
	if ev := eng.EvaluateStop(fixtureSpace(sp, string(PhaseReview), 0), newFakeClock().Now()); !ev.ShouldPause {
		t.Fatal("应触发")
	}
	snap, ok := eng.Snapshot(sp)
	if !ok || len(snap.Candidates) != 1 || len(snap.Revisions) != 1 || len(snap.Issues) != 1 || len(snap.Trail) == 0 {
		t.Fatalf("暂停保留进度：循环事实须原样保留：%+v", snap)
	}
}
