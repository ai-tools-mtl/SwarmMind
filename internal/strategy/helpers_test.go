package strategy

// W05-01 测试公共助手（fixture 驱动，零网络零模型）。

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// collabCandidate collab 候选记录 fixture。
func collabCandidate(member, body string) collab.CandidateRecord {
	return collab.CandidateRecord{Member: member, Body: body}
}

// fakeClock 可推进时钟（UTC 基准；形态对照 collab 包测试助手）。
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{cur: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = c.cur.Add(d)
}

// newTestEngine 固定时钟引擎（SwarmMind 默认阈值 2）。
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// mkhash 确定性内容 hash（SHA-256 hex64 fixture）。
func mkhash(name string) string {
	sum := sha256.Sum256([]byte("content:" + name))
	return hex.EncodeToString(sum[:])
}

// verifiedEvidence 已证实证据 fixture（来源分层必填）。
func verifiedEvidence(src EvidenceSource, ref string) Evidence {
	return Evidence{Verified: true, Source: src, Ref: ref}
}

// mustRegisterRevision 登记版本 fixture（失败即 Fatal，返回内容 hash）。
func mustRegisterRevision(t *testing.T, eng *Engine, spaceID, contentName string, round int64) string {
	t.Helper()
	rev, created, err := eng.RegisterRevision(spaceID, RevisionInput{
		Baseline:      "baseline-" + contentName,
		ContentSHA256: mkhash(contentName),
		AuthorRound:   round,
		Author:        "alice",
	})
	if err != nil {
		t.Fatalf("RegisterRevision %s: %v", contentName, err)
	}
	if !created {
		t.Fatalf("RegisterRevision %s: 期望新建版本", contentName)
	}
	return rev.ContentSHA256
}

// mustEvidence 登记四层分离已证实证据 fixture（CLI 回执/协议回执/服务端轮事实/
// 有权人工核对）。
func mustEvidence(t *testing.T, eng *Engine, spaceID, hash string) {
	t.Helper()
	layers := []struct {
		layer EvidenceLayer
		ev    Evidence
	}{
		{LayerCLIExit, verifiedEvidence(SourceExecutorReceipt, "cmd-run-1")},
		{LayerProtocol, verifiedEvidence(SourceProtocolReceipt, "parse-1")},
		{LayerRoundDone, verifiedEvidence(SourceHostObservation, "turn-1")},
		{LayerWorkAccept, verifiedEvidence(SourceHumanConfirm, "owner")},
	}
	for _, l := range layers {
		if err := eng.RecordEvidence(spaceID, hash, l.layer, l.ev); err != nil {
			t.Fatalf("RecordEvidence %s: %v", string(l.layer), err)
		}
	}
}

// mustIssueFixed 问题修复闭环 fixture（提出→修复声明→复审确认，返回问题 ID）。
func mustIssueFixed(t *testing.T, eng *Engine, spaceID, hash string, sev Severity) string {
	t.Helper()
	id := mustIssue(t, eng, spaceID, hash, sev)
	if err := eng.ClaimFix(spaceID, id, hash, 1, "已修"); err != nil {
		t.Fatalf("ClaimFix: %v", err)
	}
	if err := eng.RecordReReview(spaceID, id, ReReviewInput{Reviewer: "bob", AgainstHash: hash, Result: ReReviewConfirmed}); err != nil {
		t.Fatalf("RecordReReview: %v", err)
	}
	return id
}

// mustIssue 问题登记 fixture（同一文件:行:标题固定键）。
func mustIssue(t *testing.T, eng *Engine, spaceID, foundInHash string, sev Severity) string {
	t.Helper()
	is, err := eng.RecordIssue(spaceID, IssueInput{
		Reviewer: "bob", Severity: sev, File: "internal/foo/bar.go", Line: 42,
		Title: "边界检查缺失", FoundInHash: foundInHash, FoundInRound: 1,
	})
	if err != nil {
		t.Fatalf("RecordIssue: %v", err)
	}
	return is.ID
}

// mustCandidateFinalized 候选登记并定版 fixture。
func mustCandidateFinalized(t *testing.T, eng *Engine, spaceID, body string) string {
	t.Helper()
	cand, created, err := eng.RegisterCandidate(spaceID, CandidateInput{Proposer: "alice", Body: body, Round: 1, MsgSeq: 7})
	if err != nil {
		t.Fatalf("RegisterCandidate: %v", err)
	}
	if !created {
		t.Fatalf("RegisterCandidate: 期望新建候选")
	}
	if _, err := eng.FinalizeCandidate(spaceID, cand.ID, "owner"); err != nil {
		t.Fatalf("FinalizeCandidate: %v", err)
	}
	return cand.ID
}

// satisfiedFixture 全满足 fixture：版本 v1 + 四层证据 + must 问题修复闭环 +
// 候选定版；配合 fixtureSpace("sp-x", PhaseReview, 0) 即完成判据全绿。
func satisfiedFixture(t *testing.T, eng *Engine, spaceID string) string {
	t.Helper()
	hash := mustRegisterRevision(t, eng, spaceID, "v1", 1)
	mustEvidence(t, eng, spaceID, hash)
	mustIssueFixed(t, eng, spaceID, hash, SeverityMust)
	mustCandidateFinalized(t, eng, spaceID, "FINAL: 采用方案一，按层分离证据字段")
	return hash
}
