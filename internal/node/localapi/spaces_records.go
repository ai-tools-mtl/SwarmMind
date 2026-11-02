// spaces_records.go 策略面 REST（W05-04，docs/tasks/v3/W05-strategies.md REST 表
// 「记录候选/审查/材料」+「P04 策略区」行；候选定版/否决主人产品面为集成者裁决
// 修复轮补齐，SPEC 六.六豁免⑯）：
//   - GET  /api/v1/spaces/{id}/strategy        策略投影只读（循环阶段/候选及定版
//     态/问题及重开态/修订与证据缺口/停止建议；无策略数据的空间如实空态不编造）
//   - POST /api/v1/spaces/{id}/records         记录登记（record.kind 限定
//     candidate/review/issue/evidence；数据写入≠执行——本面只落策略引擎循环事实，
//     不派发不推进循环不触发任何执行）
//   - GET  /api/v1/spaces/{id}/records?since=N 记录增量读（留痕序号游标，只读；
//     每条恒带 type：四族 candidate/review/issue/evidence + 控制留痕 control 五值封闭）
//   - POST /api/v1/spaces/{id}/candidates/{cid}/finalize  候选定版（仅主人；
//     CompletionGaps 第 3 条「候选清零或显式定版」的人工授权面——W05 硬门槛
//     「讨论候选由主人/finalize 定版、默认等待人工」，完成授权不开放给 orchestrate）
//   - POST /api/v1/spaces/{id}/candidates/{cid}/dismiss   候选否决（仅主人；
//     显式淘汰留痕，不静默删除）
//
// 数据面归属：本面全部经 strategy.Engine 既有记录方法（RegisterCandidateVia/
// RegisterRevision/RecordIssue/RecordEvidence/RecordReReview/RecordCleanReview/
// ClaimFix/ReplyIssue）落引擎内存循环事实——候选/版本/问题/证据的既有校验（成员
// 资格、hash 格式、自报不证实、发现版本须已登记）原样生效，端点层零第二套校验。
// 鉴权对照既有 space 端点分级：写面 authSpaceSubject（orchestrate 可登记审查/证据
// 类非凭据记录——外部管家驱动集成的登记通道；凭据类记录本面不存在，永不暴露
// agent）；读面 authRead（持任一本机凭证即可）。
//
// 幂等口径（如实）：记录无 request_id 回执——幂等性由事实键承担（候选同提出者同
// 文去重计数、版本同内容同补丁同 ID、问题同 key 重报计数），重复提交按既有记录
// 语义收敛，不产生第二份事实。响应 201 新建 / 200 命中既有（dup=true）。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/strategy"
)

// spaceStrategyReady 策略面装配守卫（引擎未装配 503 诚实降级，不假空态不假放行）。
func (s *Server) spaceStrategyReady(w http.ResponseWriter) bool {
	if s.cfg.Strategy == nil {
		writeErr(w, http.StatusServiceUnavailable, "strategy_unavailable",
			"策略引擎未装配：策略投影与记录面不可用（本机未装配策略引擎；升级或装配后可用）")
		return false
	}
	return true
}

// handleSpaceStrategy GET /api/v1/spaces/{id}/strategy：策略投影只读（P04 策略区
// 数据源）。空间不存在 404；空间存在而引擎无循环事实 → has_loop=false 如实空态
//（候选/版本/问题均未登记是合法态，不编造）。
func (s *Server) handleSpaceStrategy(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Collab.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	proj := s.cfg.Strategy.Projection(id, view, time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"strategy": proj})
}

// spaceRecordRequest POST /api/v1/spaces/{id}/records 请求体（kind 判别联合；字段
// 按既有记录方法入参一一对应，端点层只做装配不做解释）。
type spaceRecordRequest struct {
	Kind string `json:"kind"` // candidate|review|issue|evidence
	// ---- candidate ----
	Member string `json:"member,omitempty"` // 候选提出者（空间成员；FINAL: 文本）
	Body   string `json:"body,omitempty"`   // FINAL: 前缀发言原文
	Round  *int64 `json:"round,omitempty"`  // 提出轮（缺省 0）
	// ---- review（op 判别）----
	Op string `json:"op,omitempty"` // revision|rereview|clean_review|claim_fix|reply
	// ---- review/revision ----
	Baseline      string                 `json:"baseline,omitempty"`       // 代码基线（commit/工作区锚）
	ContentSHA256 string                 `json:"content_sha256,omitempty"` // 内容 SHA-256 hex64
	PatchSHA256   string                 `json:"patch_sha256,omitempty"`   // 补丁 SHA-256 hex64（可空）
	Author        string                 `json:"author,omitempty"`         // 编写成员
	AuthorRound   *int64                 `json:"author_round,omitempty"`   // 编写轮次（缺省 0）
	Dependencies  []strategy.ArtifactRef `json:"dependencies,omitempty"`   // 依赖产物引用
	// ---- review/rereview ----
	IssueID     string `json:"issue_id,omitempty"`     // 复审/修复/回复目标问题
	Reviewer    string `json:"reviewer,omitempty"`     // 复审者
	AgainstHash string `json:"against_hash,omitempty"` // 复审针对版本（SHA-256 hex64）
	Result      string `json:"result,omitempty"`       // confirmed_fixed|still_open
	VersionHash string `json:"version_hash,omitempty"` // 修复声明版本（claim_fix）
	// ---- review/clean_review·reply·通用备注 ----
	Note string `json:"note,omitempty"`
	// ---- issue ----
	Severity     string `json:"severity,omitempty"`         // must|should|info
	File         string `json:"file,omitempty"`             // 文件
	Line         *int   `json:"line,omitempty"`             // 行号
	Title        string `json:"title,omitempty"`            // 标题
	FoundInHash  string `json:"found_in_version,omitempty"` // 发现版本（SHA-256 hex64）
	FoundInRound *int64 `json:"found_in_round,omitempty"`   // 发现时编写轮次（缺省 0）
	// ---- evidence ----
	Version  string `json:"version,omitempty"`  // 证据绑定版本（SHA-256 hex64）
	Layer    string `json:"layer,omitempty"`    // cli_exit|protocol_parse|round_complete|work_accept
	Source   string `json:"source,omitempty"`   // 证据来源（agent_self_report 永不可证实）
	Verified bool   `json:"verified,omitempty"` // 已证实标记（自报标记即 400）
	Ref      string `json:"ref,omitempty"`      // 回执号/hash/人工主体
}

// recordKindError 记录面错误 → HTTP（400 参数非法 / 403 越权 / 404 目标不存在；
// 人话原样透出，错误码用 strategy 域码——数据面错误不入 collab 码表）。
func recordKindError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	msg := stripStrategyPrefix(err.Error())
	sc := "invalid_argument"
	var se *strategy.Error
	if errors.As(err, &se) {
		sc = se.Code
		switch {
		case se.Code == "proposer_not_member":
			code = http.StatusForbidden
		case se.Code == "revision_not_found" || se.Code == "issue_not_found" || se.Code == "candidate_not_found":
			code = http.StatusNotFound
		}
	}
	writeErr(w, code, sc, msg)
}

// handleSpaceRecordPost POST /api/v1/spaces/{id}/records：策略循环记录登记。
// kind 逐族分派到引擎既有记录方法；终态/已归档空间拒收（时间线只读同规——事实
// 登记服务于运行中的循环，终态后材料只读）。
func (s *Server) handleSpaceRecordPost(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	var req spaceRecordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 records JSON: "+err.Error())
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Collab.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	if view.Archived || view.Status.Terminal() {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
			"终态/已归档空间只读：不再收策略循环记录（材料保留可查，登记面向活跃空间开放）")
		return
	}
	round := int64(0)
	if req.Round != nil {
		round = *req.Round
	}
	switch req.Kind {
	case "candidate":
		s.recordCandidate(w, id, view, req, round)
	case "review":
		s.recordReview(w, id, req)
	case "issue":
		s.recordIssue(w, id, req)
	case "evidence":
		s.recordEvidence(w, id, req)
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"kind 须为 candidate|review|issue|evidence（记录四族；数据写入只是事实登记，不等于执行——执行仍须 space dispatch）, 得 "+strconv.Quote(req.Kind)+"")
	}
}

// recordCandidate kind=candidate：成员候选登记（FINAL: 前缀文本通道）。成员资格/
// 大小/词形先经 ValidateCandidate（越权提出者 403，非法候选零登记——AC12 口径），
// 再经 RegisterCandidateVia final_text 通道落索引（同文去重计数）。
func (s *Server) recordCandidate(w http.ResponseWriter, spaceID string, view collab.SpaceView, req spaceRecordRequest, round int64) {
	if err := s.cfg.Strategy.ValidateCandidate(view, collab.CandidateRecord{Member: req.Member, Body: req.Body}); err != nil {
		recordKindError(w, err)
		return
	}
	cand, created, err := s.cfg.Strategy.RegisterCandidateVia(spaceID, strategy.ChannelFinalText, strategy.CandidateInput{
		Proposer: req.Member, Body: req.Body, Round: round,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	code := http.StatusCreated
	if !created {
		code = http.StatusOK // 同文重放：去重命中既有候选（SeenCount 计数）
	}
	writeJSON(w, code, map[string]any{"candidate": cand, "created": created})
}

// recordReview kind=review：op 判别五操作（revision/rereview/clean_review/claim_fix/
// reply）——审查域记录统一入口。
func (s *Server) recordReview(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	authorRound := int64(0)
	if req.AuthorRound != nil {
		authorRound = *req.AuthorRound
	}
	switch req.Op {
	case "revision":
		rev, created, err := s.cfg.Strategy.RegisterRevision(spaceID, strategy.RevisionInput{
			Baseline:      req.Baseline,
			ContentSHA256: req.ContentSHA256,
			PatchSHA256:   req.PatchSHA256,
			AuthorRound:   authorRound,
			Author:        req.Author,
			Dependencies:  req.Dependencies,
		})
		if err != nil {
			recordKindError(w, err)
			return
		}
		code := http.StatusCreated
		if !created {
			code = http.StatusOK // 同内容同补丁 = 同版本（幂等命中）
		}
		writeJSON(w, code, map[string]any{"revision": rev, "created": created})
	case "rereview":
		if err := s.cfg.Strategy.RecordReReview(spaceID, req.IssueID, strategy.ReReviewInput{
			Reviewer: req.Reviewer, AgainstHash: req.AgainstHash,
			Result: strategy.ReReviewStatus(req.Result), Note: req.Note,
		}); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "rereview"})
	case "clean_review":
		if err := s.cfg.Strategy.RecordCleanReview(spaceID, req.Reviewer, req.AgainstHash, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "clean_review"})
	case "claim_fix":
		if err := s.cfg.Strategy.ClaimFix(spaceID, req.IssueID, req.VersionHash, authorRound, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "claim_fix"})
	case "reply":
		if err := s.cfg.Strategy.ReplyIssue(spaceID, req.IssueID, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "reply"})
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"review 记录须携 op=revision|rereview|clean_review|claim_fix|reply, 得 "+strconv.Quote(req.Op)+"")
	}
}

// recordIssue kind=issue：审查问题登记（发现版本须已登记——锚定校验在引擎；
// 同 key 重报计数/重开投影由既有语义承担）。
func (s *Server) recordIssue(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	round := int64(0)
	if req.FoundInRound != nil {
		round = *req.FoundInRound
	}
	line := 0
	if req.Line != nil {
		line = *req.Line
	}
	is, err := s.cfg.Strategy.RecordIssue(spaceID, strategy.IssueInput{
		Reviewer: req.Reviewer, Severity: strategy.Severity(req.Severity),
		File: req.File, Line: line, Title: req.Title,
		FoundInHash: req.FoundInHash, FoundInRound: round, Note: req.Note,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"issue": is})
}

// recordEvidence kind=evidence：测试执行证据登记（绑定已登记版本；agent 自报标
// 证实记录面即拒——AC13 口径的第一道把守，判定面 EvidenceVerified 再验）。
func (s *Server) recordEvidence(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	err := s.cfg.Strategy.RecordEvidence(spaceID, req.Version, strategy.EvidenceLayer(req.Layer), strategy.Evidence{
		Verified: req.Verified, Source: strategy.EvidenceSource(req.Source), Ref: req.Ref,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "evidence"})
}

// authSpaceOwner 候选定版/否决主人专属门（镜像 sugar 家族 authLocal 形态：认证
// 401 → 编排凭据显式 403 人话指路主人控制台 → next）：完成授权类动作不经编排凭据
// 入口（orchestrate/prime 永不放行——W05 硬门槛「讨论候选由主人/finalize 定版、
// 默认等待人工」）。prime_token 在此给 403 而非 401：认证已过、授权拒绝——让调用
// 方看到「为什么」而不是猜令牌错（错误呈现纪律：授权拒绝原因如实可见）。
func (s *Server) authSpaceOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		level := levelNone
		if ok {
			level = s.classify(token)
		}
		if level == levelNone {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "本机管理令牌缺失或错误（Authorization: Bearer <local_token|prime_token>）")
			return
		}
		if level != levelRead { // 仅 local_token（主人）放行；prime_token 编排主体 403
			writeErr(w, http.StatusForbidden, collab.CodeForbidden,
				"候选定版/否决是主人人工动作（完成授权不开放给编排主体——讨论候选默认等待主人定版）：请由本机主人在控制台「协作空间」页操作")
			return
		}
		next(w, r)
	}
}

// handleSpaceCandidateFinalize POST /api/v1/spaces/{id}/candidates/{cid}/finalize：
// 候选定版（仅主人——CompletionGaps 第 3 条「候选清零或显式定版」的人工授权面，
// 含候选的讨论空间完成可达性由本端点承担）。终态/已归档空间 409 拒收（同 records
// POST 门）；引擎语义原样（端点层零第二套校验）：候选不存在 404 candidate_not_found、
// 已定版幂等命中原样返回、dismissed→finalized 重定版按 transitionCandidate 既有
// 状态机放行。定版主体由服务端门派生固定标识 owner（authSpaceOwner 已验证
// local_token；请求无 body 无从自报——AGENTS 审计主体铁律）。无 request_id/乐观锁
// （引擎内存事实无 CAS，幂等由候选状态机承担——同 records 面口径）。
func (s *Server) handleSpaceCandidateFinalize(w http.ResponseWriter, r *http.Request) {
	s.spaceCandidateTransition(w, r, strategy.CandidateFinalized)
}

// handleSpaceCandidateDismiss POST /api/v1/spaces/{id}/candidates/{cid}/dismiss：
// 候选否决（仅主人；显式淘汰留痕，不静默删除——engine 语义原样：已否决再否决
// 幂等命中，pending/finalized→dismissed 按 transitionCandidate 既有状态机放行）。
func (s *Server) handleSpaceCandidateDismiss(w http.ResponseWriter, r *http.Request) {
	s.spaceCandidateTransition(w, r, strategy.CandidateDismissed)
}

// spaceCandidateTransition 两端点共用实现：403 由 authSpaceOwner 门先行承担，此处
// 只做空间在位/活跃门与引擎单方法直调（TrailCandidateFinalized/Dismissed 留痕、
// RecordFamily=candidate 由引擎承担——records 增量读可见）。
func (s *Server) spaceCandidateTransition(w http.ResponseWriter, r *http.Request, to strategy.CandidateStatus) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Collab.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	if view.Archived || view.Status.Terminal() {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
			"终态/已归档空间只读：候选定版/否决面向活跃空间开放（材料保留可查）")
		return
	}
	cid := r.PathValue("cid")
	by := spaceOwnerSubject().Name // 服务端派生固定主体（门已验证 local_token）
	var cand strategy.Candidate
	var err error
	if to == strategy.CandidateFinalized {
		cand, err = s.cfg.Strategy.FinalizeCandidate(id, cid, by)
	} else {
		cand, err = s.cfg.Strategy.DismissCandidate(id, cid, by)
	}
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": cand})
}

// handleSpaceRecordsGet GET /api/v1/spaces/{id}/records?since=N：记录增量读
//（since=已见最大留痕序号；滚动裁剪区间以 pruned_before_seq 如实标注）。空间不
// 存在 404；无循环事实 → 空集（事实不存在 ≠ 错误）。
func (s *Server) handleSpaceRecordsGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.cfg.Collab.SpaceView(id); !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, http.StatusBadRequest, "invalid_request", "since 须为非负整数（已见最大记录 seq）")
			return
		}
		since = v
	}
	trail, pruned := s.cfg.Strategy.RecordsSince(id, since)
	records := make([]map[string]any, 0, len(trail))
	for _, ev := range trail {
		m := map[string]any{"seq": ev.Seq, "at": ev.At, "kind": string(ev.Kind), "note": ev.Note}
		// type 恒带（五值封闭）：四族记录 candidate/review/issue/evidence + 控制留痕
		// control（phase_changed/dispute_* 等——与 SPEC 六.六 swarmmind_space_records
		// 行「控制留痕如实标 control，不冒充记录」文实对齐）。
		if family := ev.Kind.RecordFamily(); family != "" {
			m["type"] = family
		} else {
			m["type"] = "control"
		}
		records = append(records, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "pruned_before_seq": pruned})
}
