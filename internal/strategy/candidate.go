// 候选 schema 与 FINAL: 前缀登记兼容（W05-01 交付①）。
//
// 兼容契约（W04-01 RFC §5.1 / PRODUCT-PLAN-v3 §6）：`FINAL:` 前缀语义沿用旧群
// 不变（internal/hub/group FinalPrefix 同字面量——本包不 import 旧群包，旧群与
// v3 空间零耦合），发言以此开头即登记候选。硬规：文本前缀判定**登记候选**，
// 不判定完成/交付——候选定版（Finalized）与目标完成（EvaluateCompletion）是
// 不同事件，不能仅凭 FINAL: 显示为已交付（双端展示红线同理）。
//
// 候选独立于消息账本存储：消息裁剪不丢候选索引（对照旧群 FinalSeen 持久化标记
// 先例），候选自带 FINAL 原文引用与来源消息序号（溯源展示用）。
package strategy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// FinalPrefix FINAL 收敛候选前缀（沿用旧群字面量，语义不变）。
const FinalPrefix = "FINAL:"

// 候选登记限额（登记范围/大小限制继承精神；超限拒绝而非静默截断丢原文）。
const (
	// MaxSummaryBytes 候选内容摘要上限（字节）——摘要截断展示，原文完整保留在
	// FinalText。
	MaxSummaryBytes = 200
	// MaxCandidateBodyBytes 候选原文（FINAL 发言正文）上限（字节）——防快照膨胀，
	// 超限拒绝登记并要求提出者拆分。
	MaxCandidateBodyBytes = 64 << 10
)

// CandidateStatus 候选状态：pending → finalized | dismissed。
// 多条候选并存；完成判据要求「候选清零或显式候选定版」（无 pending 且存在
// finalized）。
type CandidateStatus string

const (
	CandidatePending   CandidateStatus = "pending"   // 待定版
	CandidateFinalized CandidateStatus = "finalized" // 已定版（主人/finalize 显式）
	CandidateDismissed CandidateStatus = "dismissed" // 已否决（显式，不静默淘汰）
)

// Valid 取值校验。
func (s CandidateStatus) Valid() bool {
	switch s {
	case CandidatePending, CandidateFinalized, CandidateDismissed:
		return true
	}
	return false
}

// Candidate 候选结论记录（独立索引，不随消息裁剪丢失）。
type Candidate struct {
	// ID 稳定 ID（cand- + 内容派生 hex）：同一提出者同一原文重复发言去重到同
	// 一候选（SeenCount 计数），不产生第二条。
	ID string `json:"id"`
	// Summary 内容摘要（FINAL 正文首行截断展示）。
	Summary string `json:"summary"`
	// Proposer 提出者（空间成员名）。
	Proposer string `json:"proposer"`
	// Round 提出轮（调用方轮次锚：派发轮次/消息轮）。
	Round int64 `json:"round"`
	// FinalText FINAL 原文引用（含前缀完整保留）。
	FinalText string `json:"final_text"`
	// RegisteredAt 登记时刻（UTC）。
	RegisteredAt time.Time `json:"registered_at"`
	// Status 候选状态（pending/finalized/dismissed）。
	Status CandidateStatus `json:"status"`
	// SourceMsgSeq 来源消息序号（0=未随消息登记；消息裁剪不影响本索引）。
	SourceMsgSeq int64 `json:"source_msg_seq,omitempty"`
	// SeenCount 同文重复发言次数（≥1；去重不重复登记）。
	SeenCount int `json:"seen_count"`
	// FinalizedAt / FinalizedBy 定版事实（FinalizedBy 仅归属展示，授权归 W05-02
	// 定版端点门——审计主体原则同 AGENTS.md）。
	FinalizedAt *time.Time `json:"finalized_at,omitempty"`
	FinalizedBy string     `json:"finalized_by,omitempty"`
}

// HasFinalPrefix 发言是否登记候选（文本前缀判定登记，不判定完成/交付）。
func HasFinalPrefix(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), FinalPrefix)
}

// ParseFinal 解析 FINAL 发言正文（前缀后须有非空内容；空白候选拒绝）。
func ParseFinal(body string) (string, error) {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, FinalPrefix) {
		return "", fmt.Errorf("%w: 缺 %q 前缀", ErrNotFinalForm, FinalPrefix)
	}
	content := strings.TrimSpace(trimmed[len(FinalPrefix):])
	if content == "" {
		return "", fmt.Errorf("%w: 前缀后无内容", ErrNotFinalForm)
	}
	return content, nil
}

// FinalSummary 候选内容摘要（FINAL 正文首行；超 MaxSummaryBytes 截断）。
func FinalSummary(content string) string {
	line := content
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if len(line) <= MaxSummaryBytes {
		return line
	}
	cut := line[:MaxSummaryBytes]
	// 不截断 UTF-8 多字节序列中间（尾半字节回退）。
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && size == 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut + "…"
}

// CandidateInput 候选登记入参（消息账本侧由 W05-02 在 FINAL 发言落账时调用；
// 成员资格校验归 ValidateCandidate（携 SpaceView），本结构只承载数据）。
type CandidateInput struct {
	Proposer string // 提出者成员名（必填）
	Body     string // FINAL 发言原文（须 FinalPrefix 前缀）
	Round    int64  // 提出轮（必填≥0）
	MsgSeq   int64  // 来源消息序号（可 0）
}

// 候选错误（Code 供 W05-02/03 投影；errors.Is 按码判定）。
var (
	// ErrNotFinalForm 非 FINAL: 前缀形式（不登记候选——文本前缀是登记唯一依据，
	// 不静默改用别的候选，E10 同源）。
	ErrNotFinalForm = &Error{Code: "candidate_not_final_form", msg: "候选正文须为 FINAL: 前缀形式"}
	// ErrCandidateEmpty 候选内容为空。
	ErrCandidateEmpty = &Error{Code: "candidate_empty", msg: "候选内容为空"}
	// ErrCandidateTooLarge 候选原文超限。
	ErrCandidateTooLarge = &Error{Code: "candidate_too_large", msg: "候选原文超过大小上限"}
	// ErrCandidateNoProposer 提出者为空。
	ErrCandidateNoProposer = &Error{Code: "candidate_no_proposer", msg: "候选提出者为空"}
	// ErrProposerNotMember 提出者不在空间成员内（越权候选拒绝，不偷偷换人）。
	ErrProposerNotMember = &Error{Code: "proposer_not_member", msg: "候选提出者不在空间成员内"}
	// ErrCandidateNotFound 候选不存在。
	ErrCandidateNotFound = &Error{Code: "candidate_not_found", msg: "候选不存在"}
)

// candidateID 稳定候选 ID：同空间同提出者同原文 → 同 ID（去重锚）。
func candidateID(spaceID, proposer, content string) string {
	sum := sha256.Sum256([]byte("cand\x00" + spaceID + "\x00" + proposer + "\x00" + content))
	return "cand-" + hex.EncodeToString(sum[:])[:12]
}

// RegisterCandidate 登记候选（FINAL: 前缀发言 → 候选索引；独立存储，消息裁剪
// 不丢）。同（提出者,原文）重复发言去重：SeenCount 计数、不重复登记（ID 稳定）。
// created=false 表示命中既有候选（去重）。成员资格由调用方先经 ValidateCandidate
// 校验（非法候选拒绝，不静默登记）。
func (e *Engine) RegisterCandidate(spaceID string, in CandidateInput) (Candidate, bool, error) {
	if strings.TrimSpace(in.Proposer) == "" {
		return Candidate{}, false, ErrCandidateNoProposer
	}
	if in.Round < 0 {
		return Candidate{}, false, fmt.Errorf("%w: 提出轮 %d 为负", ErrInvalid, in.Round)
	}
	if len(in.Body) > MaxCandidateBodyBytes {
		return Candidate{}, false, fmt.Errorf("%w: %d > %d 字节", ErrCandidateTooLarge, len(in.Body), MaxCandidateBodyBytes)
	}
	content, err := ParseFinal(in.Body)
	if err != nil {
		return Candidate{}, false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	id := candidateID(spaceID, strings.TrimSpace(in.Proposer), content)
	for i := range lp.Candidates {
		if lp.Candidates[i].ID == id {
			lp.Candidates[i].SeenCount++
			e.appendTrail(lp, TrailCandidateRestated, fmt.Sprintf("%s 第 %d 次重申（轮 %d）", id, lp.Candidates[i].SeenCount, in.Round))
			return cloneCandidate(lp.Candidates[i]), false, nil
		}
	}
	now := e.now()
	cand := Candidate{
		ID:           id,
		Summary:      FinalSummary(content),
		Proposer:     strings.TrimSpace(in.Proposer),
		Round:        in.Round,
		FinalText:    strings.TrimSpace(in.Body),
		RegisteredAt: now,
		Status:       CandidatePending,
		SourceMsgSeq: in.MsgSeq,
		SeenCount:    1,
	}
	lp.Candidates = append(lp.Candidates, cand)
	e.appendTrail(lp, TrailCandidateRegistered, fmt.Sprintf("%s 提出者 %s 轮 %d：%s", id, cand.Proposer, in.Round, cand.Summary))
	return cloneCandidate(cand), true, nil
}

// FinalizeCandidate 候选定版（主人/finalize 显式动作；定版 ≠ 完成交付）。pending
// 或 dismissed → finalized；已 finalized 幂等命中原样返回。
func (e *Engine) FinalizeCandidate(spaceID, candidateID, by string) (Candidate, error) {
	return e.transitionCandidate(spaceID, candidateID, CandidateFinalized, by)
}

// DismissCandidate 候选否决（显式淘汰，留痕；不静默删除）。
func (e *Engine) DismissCandidate(spaceID, candidateID, by string) (Candidate, error) {
	return e.transitionCandidate(spaceID, candidateID, CandidateDismissed, by)
}

func (e *Engine) transitionCandidate(spaceID, id string, to CandidateStatus, by string) (Candidate, error) {
	if !to.Valid() || to == CandidatePending {
		return Candidate{}, fmt.Errorf("%w: 非法候选目标态 %q", ErrInvalid, string(to))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	for i := range lp.Candidates {
		if lp.Candidates[i].ID == id {
			if lp.Candidates[i].Status == to {
				return cloneCandidate(lp.Candidates[i]), nil // 幂等命中
			}
			now := e.now()
			lp.Candidates[i].Status = to
			if to == CandidateFinalized {
				lp.Candidates[i].FinalizedAt = &now
				lp.Candidates[i].FinalizedBy = by
			} else {
				lp.Candidates[i].FinalizedAt = nil
				lp.Candidates[i].FinalizedBy = ""
			}
			kind := TrailCandidateFinalized
			if to == CandidateDismissed {
				kind = TrailCandidateDismissed
			}
			e.appendTrail(lp, kind, fmt.Sprintf("%s → %s（by %s）", id, string(to), by))
			return cloneCandidate(lp.Candidates[i]), nil
		}
	}
	return Candidate{}, ErrCandidateNotFound
}

// PendingCandidates 待定版候选（定版/否决的不再返回；多条并存按登记序）。
func (e *Engine) PendingCandidates(spaceID string) []Candidate {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Candidate
	if lp, ok := e.loops[spaceID]; ok {
		for _, c := range lp.Candidates {
			if c.Status == CandidatePending {
				out = append(out, cloneCandidate(c))
			}
		}
	}
	return out
}

// CurrentRevisionHash 当前固定版本内容 hash（无登记版本返回空）。
func (e *Engine) CurrentRevisionHash(spaceID string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.currentHashLocked(spaceID)
}

// currentHashLocked 当前版本 hash（调用方持锁）。
func (e *Engine) currentHashLocked(spaceID string) string {
	if lp, ok := e.loops[spaceID]; ok && len(lp.Revisions) > 0 {
		return lp.Revisions[len(lp.Revisions)-1].ContentSHA256
	}
	return ""
}

// cloneCandidate 候选深拷贝（出包防共享底层数组被改）。
func cloneCandidate(c Candidate) Candidate {
	if c.FinalizedAt != nil {
		t := *c.FinalizedAt
		c.FinalizedAt = &t
	}
	return c
}
