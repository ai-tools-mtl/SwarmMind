// Package strategy 实现 v3 W05-01「策略接口、候选/审查/证据 schema、循环阶段
// 模型、停止/完成状态转换」：写审循环策略引擎，实现 collab.StrategyHooks 三方法
// 无副作用验证器（W04-01 RFC §四接口面），供 W04-03 完成类动作与 W05-02/03 装
// 配层消费。
//
// 定位（任务书 W05-01 行）：仅依 W04-01 的接口定义；提供无副作用验证器，不依赖
// 完整 API/UI，从而无循环——本包零既有文件改动，只 import internal/collab（接口
// 与 SpaceView）+ stdlib。
//
// 结构（四文件分工）：
//   - candidate.go 候选 schema + FINAL: 前缀登记兼容（登记 ≠ 完成/交付）；
//   - review.go    Revision/Issue/证据四层分离 schema + 版本变更旧复审失效留痕；
//   - phase.go     编写→冻结→审查→返工→复审→完成/暂停 显式阶段机（表外非法）；
//   - stop.go      预算耗尽/意见两次未解/验收争议 → 暂停保留进度判定。
//
// 副作用纪律：StrategyHooks 三方法（EvaluateCompletion/ValidateCandidate/
// DecidePhaseAdvance）只读 collab SpaceView + 本包循环事实做纯判定（只取读锁，
// 不产生时间戳、不改任何状态）；循环事实写入面（Register*/Record*/Set*）是独立
// 方法族，由 W05-02/03 装配层在消息落账/审查回执到达时调用并自行持久化。判定
// 面绝不猜测：任何证据缺失 → 拒绝并给精确缺口清单。
package strategy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// DefaultFixRoundsLimit 同一问题修复未过复审的默认暂停阈值（SwarmMind 自身规则
// = 2，上报 Hana/用户裁决；其他项目按项目规则经 Config 显式配置）。
const DefaultFixRoundsLimit = 2

// MaxTrailEvents 单空间留痕事件滚动上限（超限丢最旧；判定主链在候选/版本/问题
// 字段，trail 是解释面——同 collab MaxEventsPerSpace 先例）。
const MaxTrailEvents = 512

// Config 策略引擎配置。
type Config struct {
	// MaxUnresolvedFixRounds 同一问题修复未过复审的暂停阈值；0 = 默认
	// DefaultFixRoundsLimit（SwarmMind=2）；负值 NewEngine 拒绝。
	MaxUnresolvedFixRounds int
	// Now 记录时钟（写入面时间戳；nil = time.Now().UTC——时间一律 UTC）。
	Now func() time.Time
}

// DefaultConfig SwarmMind 自身规则默认配置（修复两次未解即暂停上报）。
func DefaultConfig() Config { return Config{MaxUnresolvedFixRounds: DefaultFixRoundsLimit} }

// Error 策略域业务错误（Code 供 W05-02/03 投影；同码 errors.Is 判定成立——
// 形态对照 collab.Error；不直接复用 collab 哨兵：其 msg 不导出，且策略域拒绝
// 语义（候选非法/证据非法）与 collab 空间操作错误码分域）。
type Error struct {
	Code string
	msg  string
}

func (e *Error) Error() string { return "strategy: " + e.msg }

// Is 同码即同类（支持包装错误链）。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t != nil && t.Code == e.Code
}

// errf 构造带上下文的策略域错误（保留哨兵 Code）。
func errf(sentinel *Error, format string, args ...any) *Error {
	e := *sentinel
	if format != "" {
		e.msg = sentinel.msg + ": " + fmt.Sprintf(format, args...)
	}
	return &e
}

// ErrInvalid 通用参数非法（复用哨兵的码分域前缀见各文件哨兵族；本哨兵用于
// 未归类的入参错误）。
var ErrInvalid = &Error{Code: "invalid_argument", msg: "参数非法"}

// TrailKind 留痕事件种类（失效/定版/复审等关键循环事实的解释面）。
type TrailKind string

const (
	TrailCandidateRegistered TrailKind = "candidate_registered"
	TrailCandidateRestated   TrailKind = "candidate_restated"
	TrailCandidateFinalized  TrailKind = "candidate_finalized"
	TrailCandidateDismissed  TrailKind = "candidate_dismissed"
	TrailRevisionRegistered  TrailKind = "revision_registered"
	// TrailReviewInvalidated 代码改变使旧 review 失效（留痕：旧/新版本 + 受影响问题）。
	TrailReviewInvalidated TrailKind = "review_invalidated"
	TrailEvidenceRecorded  TrailKind = "evidence_recorded"
	TrailIssueRecorded     TrailKind = "issue_recorded"
	TrailIssueRereported   TrailKind = "issue_rereported"
	TrailIssueReopened     TrailKind = "issue_reopened"
	TrailIssueReplied      TrailKind = "issue_replied"
	TrailFixClaimed        TrailKind = "fix_claimed"
	TrailReReviewRecorded  TrailKind = "rereview_recorded"
	TrailDisputeSet        TrailKind = "dispute_set"
	TrailDisputeCleared    TrailKind = "dispute_cleared"
	TrailCleanReview       TrailKind = "clean_review" // 空间级复审零发现（建议3）
	TrailPhaseChanged      TrailKind = "phase_changed"
)

// TrailEvent 留痕事件（滚动保留最新 MaxTrailEvents 条）。
type TrailEvent struct {
	At   time.Time `json:"at"`
	Kind TrailKind `json:"kind"`
	Note string    `json:"note,omitempty"`
}

// DisputeRecord 验收争议登记（不确定转人工——停止条件之一）。
type DisputeRecord struct {
	Disputed bool      `json:"disputed"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

// SpaceLoop 单空间写审循环事实（本包只存事实，不存阶段——阶段真相唯一归属
// collab.Space.Phase，W05-03 经 Engine.SetPhase 校验后写入 collab）。
type SpaceLoop struct {
	SpaceID      string              `json:"space_id"`
	Candidates   []Candidate         `json:"candidates,omitempty"`
	Revisions    []Revision          `json:"revisions,omitempty"` // 登记序；末位 = 当前版本
	Issues       []Issue             `json:"issues,omitempty"`
	CleanReviews []CleanReviewRecord `json:"clean_reviews,omitempty"` // 空间级复审零发现锚（建议3）
	Dispute      *DisputeRecord      `json:"dispute,omitempty"`
	// LastReviewedHash / LastReviewedAt 最近一次审查针对的版本与时刻（版本匹配
	// 判定锚：代码改变使旧 review 失效；初审登记首条问题时锚定发现版本，复审/
	// 零发现复审随后推进本锚——锚只被审查事实推进，问题重报不回拨）。
	LastReviewedHash string    `json:"last_reviewed_hash,omitempty"`
	LastReviewedAt   time.Time `json:"last_reviewed_at,omitempty"`
	// PausedFrom 暂停前的循环阶段（恢复目标校验；SetPhase 进 paused 记、出 paused 清）。
	PausedFrom Phase `json:"paused_from,omitempty"`
	// Trail 留痕事件（失效留痕等；滚动上限 MaxTrailEvents）。
	Trail []TrailEvent `json:"trail,omitempty"`
}

// Engine 写审循环策略引擎（collab.StrategyHooks 适配器；装配期注入 collab
// Store Options.Hooks）。并发安全：判定面读锁、写入面写锁。
type Engine struct {
	mu    sync.RWMutex
	cfg   Config
	now   func() time.Time
	loops map[string]*SpaceLoop
}

// 编译期断言：Engine 实现 collab.StrategyHooks（W04-01 接口面，装配点注入）。
var _ collab.StrategyHooks = (*Engine)(nil)

// NewEngine 构造策略引擎（collab.Open 前装配：Options{Hooks: eng}）。
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.MaxUnresolvedFixRounds < 0 {
		return nil, fmt.Errorf("strategy: MaxUnresolvedFixRounds 为负: %d", cfg.MaxUnresolvedFixRounds)
	}
	if cfg.MaxUnresolvedFixRounds == 0 {
		cfg.MaxUnresolvedFixRounds = DefaultFixRoundsLimit
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{cfg: cfg, now: now, loops: map[string]*SpaceLoop{}}, nil
}

// fixLimit 生效阈值（调用方持锁或构造后不变——cfg 只读，直接读安全）。
func (e *Engine) fixLimit() int { return e.cfg.MaxUnresolvedFixRounds }

// loop 取（或建）空间循环事实（调用方持写锁）。
func (e *Engine) loop(spaceID string, create bool) *SpaceLoop {
	if lp, ok := e.loops[spaceID]; ok {
		return lp
	}
	if !create {
		return nil
	}
	lp := &SpaceLoop{SpaceID: spaceID}
	e.loops[spaceID] = lp
	return lp
}

// appendTrail 追加留痕事件（滚动裁剪最旧；调用方持写锁）。
func (e *Engine) appendTrail(lp *SpaceLoop, kind TrailKind, note string) {
	lp.Trail = append(lp.Trail, TrailEvent{At: e.now(), Kind: kind, Note: note})
	if len(lp.Trail) > MaxTrailEvents {
		lp.Trail = append(make([]TrailEvent, 0, MaxTrailEvents), lp.Trail[len(lp.Trail)-MaxTrailEvents:]...)
	}
}

// SetPhase 循环阶段变更校验与记录：实现收敛至 writereview.go（W05-03 分离边界
// ——判定体 ValidatePhaseTransition 单一真相，本文件不再重复）。

// Snapshot 循环事实快照（深拷贝出包；W05-02/03 展示/对账读面）。
func (e *Engine) Snapshot(spaceID string) (SpaceLoop, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	lp, ok := e.loops[spaceID]
	if !ok {
		return SpaceLoop{}, false
	}
	out := SpaceLoop{SpaceID: lp.SpaceID, PausedFrom: lp.PausedFrom,
		LastReviewedHash: lp.LastReviewedHash, LastReviewedAt: lp.LastReviewedAt}
	for _, c := range lp.Candidates {
		out.Candidates = append(out.Candidates, cloneCandidate(c))
	}
	for i := range lp.Revisions {
		out.Revisions = append(out.Revisions, cloneRevision(&lp.Revisions[i]))
	}
	for i := range lp.Issues {
		out.Issues = append(out.Issues, cloneIssue(&lp.Issues[i]))
	}
	if lp.Dispute != nil {
		d := *lp.Dispute
		out.Dispute = &d
	}
	out.CleanReviews = append([]CleanReviewRecord(nil), lp.CleanReviews...)
	out.Trail = append([]TrailEvent(nil), lp.Trail...)
	return out, true
}

// RestoreLoop 恢复单空间循环事实（终审建议4：进程重启恢复入口；Snapshot 深拷贝
// 可逆导入）。完整性校验（损坏拒绝，宁拒恢复不带病）：空间 ID 匹配、ID 唯一且
// 前缀合法、枚举取值合法、hash 格式合法、事件序号单调不倒挂；恢复目标空间不得
// 已有循环事实（恢复只在空引擎/未加载空间上进行）。事实原样导入，不重盖时间戳。
func (e *Engine) RestoreLoop(spaceID string, snap SpaceLoop) error {
	if strings.TrimSpace(spaceID) == "" {
		return errf(ErrInvalid, "恢复目标空间 ID 必填")
	}
	if snap.SpaceID != "" && snap.SpaceID != spaceID {
		return errf(ErrInvalid, "快照空间 %q ≠ 目标空间 %q", snap.SpaceID, spaceID)
	}
	if snap.PausedFrom != "" && !snap.PausedFrom.Valid() {
		return errf(ErrInvalid, "PausedFrom 非法 %q", string(snap.PausedFrom))
	}
	if snap.LastReviewedHash != "" && !validHash(snap.LastReviewedHash) {
		return errf(ErrInvalid, "last_reviewed_hash 格式非法")
	}
	candIDs := map[string]bool{}
	for i := range snap.Candidates {
		c := &snap.Candidates[i]
		if !strings.HasPrefix(c.ID, "cand-") || candIDs[c.ID] {
			return errf(ErrInvalid, "候选 ID 非法或重复：%q", c.ID)
		}
		candIDs[c.ID] = true
		if !c.Status.Valid() || strings.TrimSpace(c.Proposer) == "" {
			return errf(ErrInvalid, "候选 %s 字段非法", c.ID)
		}
	}
	revIDs := map[string]bool{}
	for i := range snap.Revisions {
		r := &snap.Revisions[i]
		if !strings.HasPrefix(r.ID, "rev-") || revIDs[r.ID] {
			return errf(ErrInvalid, "版本 ID 非法或重复：%q", r.ID)
		}
		revIDs[r.ID] = true
		if !validHash(r.ContentSHA256) || (r.PatchSHA256 != "" && !validHash(r.PatchSHA256)) {
			return errf(ErrInvalid, "版本 %s hash 格式非法", r.ID)
		}
		for j := range r.Dependencies {
			if d := &r.Dependencies[j]; d.SHA256 != "" && !validHash(d.SHA256) {
				return errf(ErrInvalid, "版本 %s 依赖 hash 非法", r.ID)
			}
		}
	}
	issueIDs := map[string]bool{}
	for i := range snap.Issues {
		is := &snap.Issues[i]
		if !strings.HasPrefix(is.ID, "iss-") || issueIDs[is.ID] {
			return errf(ErrInvalid, "问题 ID 非法或重复：%q", is.ID)
		}
		issueIDs[is.ID] = true
		if !is.Severity.Valid() || !validHash(is.FoundInVersion) {
			return errf(ErrInvalid, "问题 %s 字段非法", is.ID)
		}
		if is.EventSeq <= 0 || is.LastReportedSeq <= 0 || is.EventSeq < is.LastReportedSeq {
			return errf(ErrInvalid, "问题 %s 事件序号非法/倒挂", is.ID)
		}
		for j := range is.ReReviews {
			rec := &is.ReReviews[j]
			if !rec.Result.Valid() || !validHash(rec.AgainstHash) || rec.Seq <= 0 || rec.Seq > is.EventSeq {
				return errf(ErrInvalid, "问题 %s 复审记录非法", is.ID)
			}
		}
		for j := range is.Fixes {
			f := &is.Fixes[j]
			if !validHash(f.VersionHash) || (f.VerifiedAgainst != "" && !validHash(f.VerifiedAgainst)) {
				return errf(ErrInvalid, "问题 %s 修复记录 hash 非法", is.ID)
			}
			if f.Result != "" && !f.Result.Valid() {
				return errf(ErrInvalid, "问题 %s 修复结论非法", is.ID)
			}
		}
	}
	for i := range snap.CleanReviews {
		if cr := &snap.CleanReviews[i]; !validHash(cr.AgainstHash) {
			return errf(ErrInvalid, "零发现复审记录 hash 非法")
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.loops[spaceID]; exists {
		return errf(ErrInvalid, "空间 %s 循环事实已存在（恢复仅在未加载空间上进行）", spaceID)
	}
	lp := &SpaceLoop{
		SpaceID:          spaceID,
		LastReviewedHash: snap.LastReviewedHash,
		LastReviewedAt:   snap.LastReviewedAt,
		PausedFrom:       snap.PausedFrom,
	}
	for _, c := range snap.Candidates {
		lp.Candidates = append(lp.Candidates, cloneCandidate(c))
	}
	for i := range snap.Revisions {
		lp.Revisions = append(lp.Revisions, cloneRevision(&snap.Revisions[i]))
	}
	for i := range snap.Issues {
		lp.Issues = append(lp.Issues, cloneIssue(&snap.Issues[i]))
	}
	for _, cr := range snap.CleanReviews {
		lp.CleanReviews = append(lp.CleanReviews, cr)
	}
	if snap.Dispute != nil {
		d := *snap.Dispute
		lp.Dispute = &d
	}
	lp.Trail = append([]TrailEvent(nil), snap.Trail...)
	e.loops[spaceID] = lp
	return nil
}

// SetAcceptanceDispute 验收争议登记/解除（停止条件之一；留痕）。
func (e *Engine) SetAcceptanceDispute(spaceID string, disputed bool, note string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	if disputed {
		lp.Dispute = &DisputeRecord{Disputed: true, Note: note, At: e.now()}
		e.appendTrail(lp, TrailDisputeSet, note)
	} else {
		if lp.Dispute != nil && lp.Dispute.Disputed {
			e.appendTrail(lp, TrailDisputeCleared, note)
		}
		lp.Dispute = nil
	}
}

// ---------- 缺口清单与完成判据 ----------

// Gap 完成判据缺口（结构化给 UI/报告；Reason 汇总人话给 collab 审计事件）。
type Gap struct {
	Field  string `json:"field"`  // 缺口域（acceptance/phase/candidate/issue/evidence/version/inflight）
	Detail string `json:"detail"` // 精确缺口描述（绝不猜测：缺什么写什么）
}

// CompletionGaps 完成判据缺口清单（纯判定；空切片 = 全部满足）。
//
// 五输入（任务书 W05-01 交付④）+ 阶段前提，逐项独立判定、全部缺口一次列全：
//  1. 验收条件在位（W04-01 RFC §1.1：acceptance 可空但 finalize(done) 时判据必需）；
//  2. 阶段前提（审查/复审通过后或暂停中人工定版——编写/冻结中不存在完成）；
//  3. 候选清零或显式候选定版（无 pending，且登记过候选时至少一条 finalized）；
//  4. 必须修 issue 清零（must 级对当前版本全部 confirmed_fixed；should/info 不阻塞）；
//  5. 测试证据合格（当前版本四层分离全部已证实且非 agent 自报——自报保持未知）；
//  6. 版本匹配（当前版本存在 + 最近复审针对当前版本——代码改变旧 review 失效）；
//  7. 无在途执行（collab InFlight = 0）。
func (e *Engine) CompletionGaps(space collab.SpaceView) []Gap {
	var gaps []Gap
	add := func(field, format string, args ...any) {
		gaps = append(gaps, Gap{Field: field, Detail: fmt.Sprintf(format, args...)})
	}
	// 1. 验收条件。
	if strings.TrimSpace(space.Acceptance) == "" {
		add("acceptance", "验收条件缺失：空间未配置 acceptance（完成判据必需）")
	}
	// 2. 阶段前提。
	phase, ok := ParsePhase(space.Phase)
	if !ok {
		add("phase", "循环阶段未初始化或未知（%q）：无从判定完成前提", space.Phase)
	} else {
		switch phase {
		case PhaseReview, PhaseRereview, PhasePaused:
			// 审查/复审通过后可完成；暂停中允许人工定版。
		case PhaseDrafting, PhaseFrozen:
			add("phase", "阶段 %s 尚未进入审查：不存在完成（编写/冻结中）", string(phase))
		case PhaseRework:
			add("phase", "阶段 %s 存在未解问题：先返工复审再完成", string(phase))
		case PhaseCompleted:
			add("phase", "阶段已为 completed：终态不可重入（由空间状态机拒绝）")
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	current := e.currentHashLocked(space.ID)
	lp := e.loops[space.ID]
	// 3. 候选清零或显式候选定版。
	if lp != nil {
		var pending, finalized int
		for i := range lp.Candidates {
			switch lp.Candidates[i].Status {
			case CandidatePending:
				pending++
			case CandidateFinalized:
				finalized++
			}
		}
		total := len(lp.Candidates)
		if pending > 0 {
			add("candidate", "存在未定版候选 %d 条（共 %d 条）：候选须清零或显式定版", pending, total)
		} else if total > 0 && finalized == 0 {
			add("candidate", "候选 %d 条均未定版：须显式候选定版", total)
		}
	}
	// 4. 必须修 issue 清零。
	if lp != nil {
		var open []string
		for i := range lp.Issues {
			is := &lp.Issues[i]
			if is.Severity == SeverityMust && IssueBlocking(is, current) {
				open = append(open, fmt.Sprintf("%s [%s] %s", is.ID, IssueEffectiveState(is, current), is.String()))
			}
		}
		if len(open) > 0 {
			add("issue", "必须修问题未清零（%d 条）：%s", len(open), strings.Join(open, "；"))
		}
	}
	// 5. 测试证据合格（四层分离逐一核对；自报来源判未证实）。
	if current == "" {
		add("version", "无登记版本：编写产出未固定（版本/证据/审查均无从谈起）")
	} else if lp != nil {
		rev := e.revisionByHashLocked(lp, current)
		layers := []struct {
			layer EvidenceLayer
			slot  *Evidence
		}{
			{LayerCLIExit, rev.TestCLI},
			{LayerProtocol, rev.TestProto},
			{LayerRoundDone, rev.TestRound},
			{LayerWorkAccept, rev.TestAccept},
		}
		var missing []string
		for _, l := range layers {
			if !EvidenceVerified(l.slot) {
				detail := "缺失"
				if l.slot != nil {
					detail = fmt.Sprintf("未证实（来源 %s：%s）", string(l.slot.Source),
						map[bool]string{true: "agent 自报不得证实", false: "标记未证实或来源不可证实"}[l.slot.Source == SourceSelfReport])
				}
				missing = append(missing, fmt.Sprintf("%s=%s", string(l.layer), detail))
			}
		}
		if len(missing) > 0 {
			add("evidence", "测试证据不合格（四层分离须全部证实且非自报）：%s", strings.Join(missing, "；"))
		}
		// 6. 版本匹配：最近复审须针对当前版本（代码改变旧 review 失效）。
		if lp.LastReviewedHash != current {
			add("version", "版本不匹配：最近复审针对 %s，当前版本 %s（代码改变旧 review 失效）",
				tail(lp.LastReviewedHash, 12), tail(current, 12))
		}
	}
	// 7. 无在途。
	if space.InFlight > 0 {
		add("inflight", "存在在途执行 %d 项：先收束或核对", space.InFlight)
	}
	return gaps
}

// EvaluateCompletion 完成判据（collab.StrategyHooks；纯判定无副作用）：任何证据
// 缺失 → 拒绝并给精确缺口清单（绝不猜测）。EvidenceRefs 携判据引用定位（当前版
// 本/定版候选/最近复审锚）落审计事件。
func (e *Engine) EvaluateCompletion(space collab.SpaceView) collab.CompletionVerdict {
	gaps := e.CompletionGaps(space)
	if len(gaps) == 0 {
		current := e.CurrentRevisionHash(space.ID)
		var refs []string
		if snap, ok := e.Snapshot(space.ID); ok {
			refs = append(refs, "version:"+current, "reviewed:"+snap.LastReviewedHash)
			for _, c := range snap.Candidates {
				if c.Status == CandidateFinalized {
					refs = append(refs, "candidate:"+c.ID)
				}
			}
		}
		return collab.CompletionVerdict{
			Satisfied:    true,
			Reason:       fmt.Sprintf("完成判据满足：验收条件在位、候选清零/定版、必须修清零、四层测试证据证实、版本匹配（%s…）、无在途", tail(current, 12)),
			EvidenceRefs: refs,
		}
	}
	fields := make([]string, 0, len(gaps))
	details := make([]string, 0, len(gaps))
	for i, g := range gaps {
		fields = append(fields, g.Field)
		details = append(details, fmt.Sprintf("缺口%d[%s] %s", i+1, g.Field, g.Detail))
	}
	return collab.CompletionVerdict{
		Satisfied: false,
		Reason:    fmt.Sprintf("完成判据未满足（%d 项缺口：%s）：%s", len(gaps), strings.Join(fields, ","), strings.Join(details, " ")),
	}
}

// ValidateCandidate 候选校验（collab.StrategyHooks；纯判定）：结构合法性（FINAL:
// 前缀形式、非空、大小限额）+ 提出者成员资格。非法候选拒绝，不静默改用别的候选
// （E10）。结构化候选 schema 归 W05-02（登记范围届时扩展；本版仅 FINAL 文本形式）。
func (e *Engine) ValidateCandidate(space collab.SpaceView, candidate collab.CandidateRecord) error {
	proposer := strings.TrimSpace(candidate.Member)
	if proposer == "" {
		return ErrCandidateNoProposer
	}
	member := false
	for i := range space.Members {
		if space.Members[i].Name == proposer {
			member = true
			break
		}
	}
	if !member {
		return errf(ErrProposerNotMember, "提出者 %q 不在空间成员内（非法候选拒绝，不静默换人）", proposer)
	}
	if len(candidate.Body) > MaxCandidateBodyBytes {
		return fmt.Errorf("%w: %d > %d 字节", ErrCandidateTooLarge, len(candidate.Body), MaxCandidateBodyBytes)
	}
	if _, err := ParseFinal(candidate.Body); err != nil {
		return err
	}
	return nil
}

// DecidePhaseAdvance 阶段推进判定（collab.StrategyHooks；纯判定）。条件口径：
//   - drafting → frozen：已登记编写产出（当前版本存在）；
//   - frozen → review：审查角色成员在位；
//   - review → rework：存在未解阻塞问题（must/should）；
//   - review 无阻塞问题 → 不推进：审查通过 ≠ 完成，完成判定归 finish/finalize
//     判据核对（PLAN §6 硬规）；
//   - rework → rereview：修复后新版本（当前版本 hash ≠ 最近复审版本）；
//   - rereview：修复声明待复审（pendingFix）先判等待——进入复审时遗留问题尚未
//     复审是预期态；复审轮已发生（无待复核声明）仍有未解阻塞问题 → rework；
//     无 → 不推进（等待完成判据）；
//   - paused/completed/未初始化 → 不推进。
func (e *Engine) DecidePhaseAdvance(space collab.SpaceView) collab.PhaseDecision {
	phase, ok := ParsePhase(space.Phase)
	if !ok {
		return collab.PhaseDecision{Advance: false, Reason: fmt.Sprintf("循环阶段未初始化或未知（%q）：不推进", space.Phase)}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	current := e.currentHashLocked(space.ID)
	lp := e.loops[space.ID]
	switch phase {
	case PhaseDrafting:
		if current == "" {
			return collab.PhaseDecision{Advance: false, Reason: "尚无登记版本：继续编写"}
		}
		return collab.PhaseDecision{Advance: true, NextPhase: string(PhaseFrozen),
			Reason: fmt.Sprintf("编写产出已登记（版本 %s…）：冻结进入审查准备", tail(current, 12))}
	case PhaseFrozen:
		reviewer, okr := reviewerMember(space)
		if !okr {
			return collab.PhaseDecision{Advance: false, Reason: "无审查角色成员：冻结保持（先配置 reviewer 角色）"}
		}
		return collab.PhaseDecision{Advance: true, NextPhase: string(PhaseReview),
			Reason: "冻结完成：审查成员在位（" + reviewer + "）"}
	case PhaseReview:
		if blocking := blockingIssues(lp, current); len(blocking) > 0 {
			return collab.PhaseDecision{Advance: true, NextPhase: string(PhaseRework),
				Reason: fmt.Sprintf("存在未解阻塞问题 %d 条：%s", len(blocking), strings.Join(blocking, "，"))}
		}
		return collab.PhaseDecision{Advance: false, Reason: "审查通过（无未解阻塞问题）：审查通过≠完成，完成判定归 finish/finalize 判据"}
	case PhaseRework:
		if current == "" {
			return collab.PhaseDecision{Advance: false, Reason: "返工中：尚无登记版本"}
		}
		if lp == nil || lp.LastReviewedHash == "" {
			// 空锚防御（W05-03 全链驱动补定）：无被审版本即无「修复后」可比——
			// 恢复的旧快照/异常态不零进度循环推进，等待审查登记或人工核对。
			return collab.PhaseDecision{Advance: false,
				Reason: "返工中：尚无被审版本锚（审查结论未登记）：等待审查登记或人工核对"}
		}
		if lp.LastReviewedHash == current {
			return collab.PhaseDecision{Advance: false,
				Reason: fmt.Sprintf("返工中：当前版本 %s… 已复审过，尚无修复后新版本", tail(current, 12))}
		}
		return collab.PhaseDecision{Advance: true, NextPhase: string(PhaseRereview),
			Reason: fmt.Sprintf("修复后新版本 %s… 已登记：进入复审", tail(current, 12))}
	case PhaseRereview:
		if pendingFix(lp) {
			// 复审中（W05-03 全链驱动补定）：修复声明待复审——进入复审时遗留问题
			// 尚未复审是预期态，等登记结论再判定（否则入口即零进度循环回返工）。
			return collab.PhaseDecision{Advance: false,
				Reason: "复审中：修复声明待复审（登记复审结论后判定）"}
		}
		if blocking := blockingIssues(lp, current); len(blocking) > 0 {
			return collab.PhaseDecision{Advance: true, NextPhase: string(PhaseRework),
				Reason: fmt.Sprintf("复审发现未解阻塞问题 %d 条：%s", len(blocking), strings.Join(blocking, "，"))}
		}
		return collab.PhaseDecision{Advance: false, Reason: "复审通过（当前版本无未解阻塞问题）：完成判定归 finish/finalize 判据"}
	case PhasePaused, PhaseCompleted:
		return collab.PhaseDecision{Advance: false, Reason: fmt.Sprintf("阶段 %s：阶段机不推进（暂停恢复/完成走控制动作）", string(phase))}
	}
	return collab.PhaseDecision{Advance: false, Reason: "未知阶段：不推进"}
}

// reviewerMember 查审查角色成员（Role=reviewer/review 不分大小写；W05-03 配置
// 编写/审查角色——无角色不猜测，冻结保持）。
func reviewerMember(space collab.SpaceView) (string, bool) {
	for i := range space.Members {
		role := strings.ToLower(strings.TrimSpace(space.Members[i].Role))
		if role == "reviewer" || role == "review" {
			return space.Members[i].Name, true
		}
	}
	return "", false
}

// blockingIssues 未解阻塞问题清单（must/should 且非对当前版本的已修确认；
// 调用方持锁）。
func blockingIssues(lp *SpaceLoop, current string) []string {
	if lp == nil {
		return nil
	}
	var out []string
	for i := range lp.Issues {
		if IssueBlocking(&lp.Issues[i], current) {
			out = append(out, lp.Issues[i].ID)
		}
	}
	sort.Strings(out)
	return out
}

// pendingFix 存在待复核的修复声明（Result 空 = 已声明修复、复审结论未回填）；
// 调用方持锁。复审阶段的「等待复审轮」判定锚。
func pendingFix(lp *SpaceLoop) bool {
	if lp == nil {
		return false
	}
	for i := range lp.Issues {
		for j := range lp.Issues[i].Fixes {
			if lp.Issues[i].Fixes[j].Result == "" {
				return true
			}
		}
	}
	return false
}
