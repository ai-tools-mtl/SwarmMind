package strategy

// 候选 schema 与 FINAL: 前缀登记兼容测试（W05-01 交付①）：
// FINAL 前缀登记 / 多条并存 / 无前缀不登记 / 同文去重 ID 稳定 / 定版-否决状态
// 推进 / 消息裁剪不丢候选索引（候选独立存储 + 原文引用 + 来源序号）。

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFinalPrefixRegistration(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-c1"
	// 无前缀：不登记（HasFinalPrefix=false；Register 拒绝）。
	if HasFinalPrefix("方案一不错") {
		t.Fatal("无前缀不应判为候选发言")
	}
	if _, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "方案一不错", Round: 1}); !errors.Is(err, ErrNotFinalForm) {
		t.Fatalf("无前缀登记应拒绝 ErrNotFinalForm，得 %v", err)
	}
	// 前缀后空白：同拒。
	if _, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL:   ", Round: 1}); !errors.Is(err, ErrNotFinalForm) {
		t.Fatalf("空白候选应拒绝，得 %v", err)
	}
	// 前缀登记：字段齐备（提出者/提出轮/原文引用/登记时刻/摘要/pending）。
	cand, created, err := eng.RegisterCandidate(sp, CandidateInput{
		Proposer: "alice", Body: "FINAL: 采用方案一\n理由：证据可分层", Round: 3, MsgSeq: 11,
	})
	if err != nil || !created {
		t.Fatalf("FINAL 登记失败：%v created=%v", err, created)
	}
	if cand.Proposer != "alice" || cand.Round != 3 || cand.Status != CandidatePending {
		t.Fatalf("候选字段不符：%+v", cand)
	}
	if cand.FinalText != "FINAL: 采用方案一\n理由：证据可分层" {
		t.Fatalf("原文引用须完整保留（含前缀）：%q", cand.FinalText)
	}
	if cand.Summary != "采用方案一" {
		t.Fatalf("摘要取首行：%q", cand.Summary)
	}
	if cand.SourceMsgSeq != 11 || cand.SeenCount != 1 || cand.RegisteredAt.IsZero() {
		t.Fatalf("溯源字段不符：%+v", cand)
	}
	if !strings.HasPrefix(cand.ID, "cand-") || len(cand.ID) != len("cand-")+12 {
		t.Fatalf("候选 ID 形态不符：%q", cand.ID)
	}
}

func TestCandidatesCoexistAndDedup(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-c2"
	a, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "bob", Body: "FINAL: 方案二", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("不同原文须不同候选（多条并存）")
	}
	if got := eng.PendingCandidates(sp); len(got) != 2 {
		t.Fatalf("多条候选并存，得 %d", len(got))
	}
	// 同文重申：去重到同一 ID（ID 稳定），SeenCount 推进，不新建。
	again, created, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 2})
	if err != nil || created {
		t.Fatalf("同文重申应去重：created=%v err=%v", created, err)
	}
	if again.ID != a.ID || again.SeenCount != 2 {
		t.Fatalf("去重候选不符：%+v", again)
	}
	if got := eng.PendingCandidates(sp); len(got) != 2 {
		t.Fatalf("去重后仍 2 条，得 %d", len(got))
	}
}

func TestCandidateLifecycleTransitions(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-c3"
	cand, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	// pending → finalized：定版事实落字段。
	fin, err := eng.FinalizeCandidate(sp, cand.ID, "owner")
	if err != nil || fin.Status != CandidateFinalized || fin.FinalizedAt == nil || fin.FinalizedBy != "owner" {
		t.Fatalf("定版不符：%+v err=%v", fin, err)
	}
	// 已定版幂等命中。
	if again, err := eng.FinalizeCandidate(sp, cand.ID, "owner"); err != nil || again.Status != CandidateFinalized {
		t.Fatalf("重复定版应幂等：%v", err)
	}
	// 定版 ≠ 完成：候选定版不产生任何完成判定（判定输入仍缺版本/证据）。
	if v := eng.EvaluateCompletion(fixtureSpace(sp, string(PhaseReview), 0)); v.Satisfied {
		t.Fatal("仅候选定版不得判定完成（登记≠交付）")
	}
	// dismissed → finalized 允许（改判）。
	d1, _, _ := eng.RegisterCandidate(sp, CandidateInput{Proposer: "bob", Body: "FINAL: 方案二", Round: 1})
	if _, err := eng.DismissCandidate(sp, d1.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if got, err := eng.FinalizeCandidate(sp, d1.ID, "owner"); err != nil || got.Status != CandidateFinalized {
		t.Fatalf("否决后改判定版应允许：%v", err)
	}
	// 不存在候选。
	if _, err := eng.FinalizeCandidate(sp, "cand-nope", "owner"); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("未知候选应拒绝：%v", err)
	}
}

// 消息裁剪不丢候选索引：候选独立于消息账本存储——后续任意写入操作后，候选
// （原文引用/来源序号）仍在索引中可查（对照旧群 FinalSeen 持久化先例）。
func TestCandidateIndexSurvivesMessageTrimming(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-c4"
	cand, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 采用方案一", Round: 2, MsgSeq: 5})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟消息裁剪：消息账本侧删除历史（本包不存消息——唯一保证即候选索引独立）；
	// 后续继续发生循环事实（登记版本），再读候选。
	if _, _, err := eng.RegisterRevision(sp, RevisionInput{Baseline: "b", ContentSHA256: mkhash("v1"), AuthorRound: 1, Author: "alice"}); err != nil {
		t.Fatal(err)
	}
	snap, ok := eng.Snapshot(sp)
	if !ok || len(snap.Candidates) != 1 {
		t.Fatalf("候选索引丢失：%+v ok=%v", snap.Candidates, ok)
	}
	got := snap.Candidates[0]
	if got.ID != cand.ID || got.FinalText != "FINAL: 采用方案一" || got.SourceMsgSeq != 5 {
		t.Fatalf("候选原文/溯源须保留：%+v", got)
	}
	if got.Status != CandidatePending {
		t.Fatalf("候选状态不符：%s", got.Status)
	}
}

func TestValidateCandidate(t *testing.T) {
	eng := newTestEngine(t)
	sp := fixtureSpace("sp-c5", string(PhaseDrafting), 0)
	// 成员 + FINAL 形式：通过。
	if err := eng.ValidateCandidate(sp, collabCandidate("alice", "FINAL: 方案一")); err != nil {
		t.Fatalf("合法候选应通过：%v", err)
	}
	// 越权提出者：拒绝不静默换人（E10）。
	if err := eng.ValidateCandidate(sp, collabCandidate("mallory", "FINAL: 方案一")); !errors.Is(err, ErrProposerNotMember) {
		t.Fatalf("非成员提出者应拒绝：%v", err)
	}
	// 空提出者。
	if err := eng.ValidateCandidate(sp, collabCandidate("", "FINAL: 方案一")); !errors.Is(err, ErrCandidateNoProposer) {
		t.Fatalf("空提出者应拒绝：%v", err)
	}
	// 非 FINAL 结构化形式：本版仅 FINAL 文本（结构化 schema 归 W05-02），拒绝不猜。
	if err := eng.ValidateCandidate(sp, collabCandidate("alice", `{"plan":"x"}`)); !errors.Is(err, ErrNotFinalForm) {
		t.Fatalf("结构化载荷应拒绝（W05-02 归属）：%v", err)
	}
	// 超限。
	huge := "FINAL: " + strings.Repeat("字", MaxCandidateBodyBytes)
	if err := eng.ValidateCandidate(sp, collabCandidate("alice", huge)); !errors.Is(err, ErrCandidateTooLarge) {
		t.Fatalf("超限应拒绝：%v", err)
	}
}

// FinalSummary 截断不破坏 UTF-8 多字节序列。
func TestFinalSummaryMultibyteBoundary(t *testing.T) {
	long := strings.Repeat("策略域阶段值", 100) // 600 字节多字节串
	got := FinalSummary(long)
	if !utf8.ValidString(got) {
		t.Fatalf("截断摘要破坏 UTF-8：%q", got)
	}
	if len(got) > MaxSummaryBytes+len("…") {
		t.Fatalf("摘要超限：%d 字节", len(got))
	}
	if !strings.HasPrefix(long, got[:len(got)-len("…")]) {
		t.Fatal("摘要应是原文前缀")
	}
}
