// projection.go 策略投影只读面（W05-04 P04 策略区：GET /api/v1/spaces/{id}/strategy
// 的数据源）。定位：把引擎内存循环事实组装成单份深拷贝投影——循环阶段、候选及定
// 版态、问题及重开态、修订与证据缺口、停止建议五组；只读不判定不改状态（停止/
// 缺口是既有纯判定方法的组合调用）。无策略数据的空间如实空态（HasLoop=false），
// 不编造。
//
// 文件域纪律：本文件为 W05-04 新增，不触碰 discussion/writereview 及其测试（另一
// agent 收尾域）；事实结构（Candidate/Revision/Issue 等）原样投影不出第二套 schema
// ——单一事实源，前端词典只做展示词映射。
package strategy

import (
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// Projection 单空间策略投影（P04 策略区读面；JSON 形态即 REST 应答体）。
type Projection struct {
	// SpaceID 投影目标空间。
	SpaceID string `json:"space_id"`
	// HasLoop 引擎是否持有该空间循环事实（false = 无策略数据：候选/版本/问题均
	// 未登记——如实空态，不编造数据）。
	HasLoop bool `json:"has_loop"`
	// Phase 循环阶段（collab.Space.Phase 真相原样透传；空 = 未初始化——显式启动
	// 时由装配引导 drafting）。PhaseValid 当前值是否为合法阶段词（未知值如实标注）。
	Phase      string `json:"phase"`
	PhaseValid bool   `json:"phase_valid"`
	// PausedFrom 暂停前阶段（暂停恢复目标；空 = 非暂停态或未记录）。
	PausedFrom Phase `json:"paused_from,omitempty"`
	// CurrentRevision 当前固定版本内容 hash（空 = 尚无登记版本）。
	CurrentRevision string `json:"current_revision,omitempty"`
	// LastReviewedHash / LastReviewedAt 最近复审锚（版本匹配判定依据；代码改变
	// 旧 review 失效的可视锚）。
	LastReviewedHash string    `json:"last_reviewed_hash,omitempty"`
	LastReviewedAt   time.Time `json:"last_reviewed_at,omitempty"`
	// Candidates 候选及定版态（登记序；pending/finalized/dismissed 原样——FINAL
	// 前缀登记 ≠ 已交付，定版态显式展示）。
	Candidates []Candidate `json:"candidates,omitempty"`
	// Revisions 修订及证据四层（登记序，末位 = 当前版本；证据逐层 verified/source
	// 原样——自报来源未证实在投影中可见，不粉饰）。
	Revisions []Revision `json:"revisions,omitempty"`
	// Issues 问题及重开态（登记序；有效状态投影由前端按 IssueEffectiveState 同规
	// 展示——本投影附带 EffectiveState 免二次推导，见 IssueViews）。
	Issues []IssueView `json:"issues,omitempty"`
	// CleanReviews 空间级零发现复审记录（审查通过的显式锚）。
	CleanReviews []CleanReviewRecord `json:"clean_reviews,omitempty"`
	// Dispute 验收争议登记（非 nil 且 Disputed = 争议在位——停止条件之一）。
	Dispute *DisputeRecord `json:"dispute,omitempty"`
	// Gaps 完成判据缺口清单（有循环事实时给出；空切片 = 判据全满足。仅完成判据
	// 展示面，不等于可自动完成——完成授权仍归 W04 逐动作表）。
	Gaps []Gap `json:"gaps,omitempty"`
	// Stop 停止建议（EvaluateStop：预算耗尽/修复阈值/验收争议——建议暂停保留进度，
	// 落账归控制面，本投影不自动暂停）。
	Stop StopEvaluation `json:"stop"`
	// FactSeq 留痕序号高水位（records 增量读起点提示；0 = 无事实）。
	FactSeq int64 `json:"fact_seq,omitempty"`
}

// IssueView 问题条目（Issue 原样 + 预推导有效状态/阻塞判定——前端免二次实现
// IssueEffectiveState 同规投影）。
type IssueView struct {
	Issue
	// State 有效状态投影（open/confirmed_fixed/still_open/stale——重报已确认问题
	// 即回归重开的投影在此可见）。
	State IssueState `json:"state"`
	// Blocking 是否阻塞（must/should 且非对当前版本的已修确认）。
	Blocking bool `json:"blocking"`
}

// Projection 组装单空间策略投影（纯读；space 为调用方读档的最新空间视图——
// Gaps/Stop 判定消费它，与本引擎循环事实做组合）。now 传判定时钟（墙钟预算
// running 段计入；装配层传 time.Now().UTC() 或可注入时钟）。
func (e *Engine) Projection(spaceID string, space collab.SpaceView, now time.Time) Projection {
	p := Projection{SpaceID: spaceID, Stop: e.EvaluateStop(space, now)}
	if phase, ok := ParsePhase(space.Phase); ok {
		p.Phase, p.PhaseValid = string(phase), true
	} else {
		p.Phase = space.Phase
	}
	snap, ok := e.Snapshot(spaceID)
	if !ok {
		return p // 无策略数据：如实空态（停止建议仍给——预算判定不依赖循环事实）
	}
	p.HasLoop = true
	p.PausedFrom = snap.PausedFrom
	p.LastReviewedHash = snap.LastReviewedHash
	p.LastReviewedAt = snap.LastReviewedAt
	p.Candidates = snap.Candidates
	p.Revisions = snap.Revisions
	p.CleanReviews = snap.CleanReviews
	p.Dispute = snap.Dispute
	p.FactSeq = snap.FactSeq
	current := e.CurrentRevisionHash(spaceID)
	p.CurrentRevision = current
	for i := range snap.Issues {
		is := &snap.Issues[i]
		p.Issues = append(p.Issues, IssueView{
			Issue:    *is,
			State:    IssueEffectiveState(is, current),
			Blocking: IssueBlocking(is, current),
		})
	}
	p.Gaps = e.CompletionGaps(space)
	return p
}

// RecordsSince 留痕记录增量读（W05-04 GET /records?since 数据源）：Seq > since 的
// 留痕事件升序；prunedBefore = 滚动裁剪后不可再取的区间上界（最旧保留序号-1，
// 0 = 无裁剪；最旧序号为 0（旧格式无序号事实）时上界无意义，返回 0——负值不作
// 为游标语义透出）。无循环事实返回空集（事实不存在 ≠ 错误，调用方如实空态）。
func (e *Engine) RecordsSince(spaceID string, since int64) (records []TrailEvent, prunedBefore int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	lp, ok := e.loops[spaceID]
	if !ok {
		return nil, 0
	}
	if len(lp.Trail) > 0 && lp.Trail[0].Seq > 0 {
		prunedBefore = lp.Trail[0].Seq - 1
	}
	for i := range lp.Trail {
		if lp.Trail[i].Seq > since {
			records = append(records, lp.Trail[i])
		}
	}
	return records, prunedBefore
}

// RecordFamily 留痕事件 → 记录四族映射（records 面关键词：candidate/review/issue/
// evidence）。控制留痕（phase_changed/dispute_*）不属于四类记录，返回空串——
// 读面如实标注来源，不把阶段/争议事实冒充记录。
func (k TrailKind) RecordFamily() string {
	switch k {
	case TrailCandidateRegistered, TrailCandidateRestated, TrailCandidateFinalized, TrailCandidateDismissed:
		return "candidate"
	case TrailRevisionRegistered, TrailReviewInvalidated, TrailFixClaimed, TrailReReviewRecorded, TrailCleanReview:
		return "review"
	case TrailIssueRecorded, TrailIssueRereported, TrailIssueReopened, TrailIssueReplied:
		return "issue"
	case TrailEvidenceRecorded:
		return "evidence"
	}
	return ""
}
