// 派发建议端点（P5.1 D1，docs/RFC-jev-dispatch）：POST /api/v1/dispatch-suggest。
// 「派给谁」建议查询——决策是建议非必经：provider 一切失败（未启用/超时/解析失败/
// 目标越权）一律 200 + suggestion:null + degraded + note 人话原因，绝不 5xx、绝不
// 阻塞派单（RFC 三）；仅请求体非法 400。候选来自 hub 目录真相源（在线 ∩ exposed），
// 请求不携带候选参数（单一天真相，防调用方伪造候选集）。
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mtl802/swarmmind/internal/decision"
)

// dispatchSuggestRequest 请求体：goal 或 step_desc（工作单步骤描述同参异名，
// RFC 四联动场景 3「Work 步骤派发逐步骤给建议」的入口预留）。
type dispatchSuggestRequest struct {
	Goal     string `json:"goal"`
	StepDesc string `json:"step_desc"`
}

// dispatchSuggestion 建议目标（与 control-operations 的 target 语义同形）。
type dispatchSuggestion struct {
	TargetAgent string `json:"target_agent"`
	TargetNode  string `json:"target_node"`
}

// dispatchSuggestResponse 应答体：suggestion 为 null 即无建议（note 说明原因）。
// confidence/probabilities 按 RFC v1.1 裁决②透传（控制台展示保守：建议+来源标签）。
type dispatchSuggestResponse struct {
	Suggestion    *dispatchSuggestion `json:"suggestion"`
	Confidence    float64             `json:"confidence"`
	Probabilities map[string]float64  `json:"probabilities"`
	Provider      string              `json:"provider"`
	Degraded      bool                `json:"degraded"`
	Note          string              `json:"note,omitempty"`
	Reason        string              `json:"reason,omitempty"`
}

// handleDispatchSuggest POST /api/v1/dispatch-suggest（会话/PAT 同权，只读查询面）。
func (s *Server) handleDispatchSuggest(w http.ResponseWriter, r *http.Request) {
	var req dispatchSuggestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {goal 或 step_desc}")
		return
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		goal = strings.TrimSpace(req.StepDesc)
	}
	if goal == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "goal（或 step_desc）必填：一句话描述任务需求")
		return
	}
	if !s.cfg.Decision.Enabled() {
		writeJSON(w, http.StatusOK, dispatchSuggestResponse{
			Degraded: true,
			Note:     "决策服务未启用（hub.yaml 未配置 decision 段或密钥不可得）：可手动选择执行者，派单不受影响",
		})
		return
	}
	res := s.cfg.Decision.Suggest(decision.Request{Goal: goal, Candidates: s.dispatchCandidates()})
	resp := dispatchSuggestResponse{
		Provider: res.Provider,
		Degraded: res.Degraded,
		Note:     res.Note,
	}
	if res.Suggestion != nil {
		resp.Suggestion = &dispatchSuggestion{
			TargetAgent: res.Suggestion.TargetAgent,
			TargetNode:  res.Suggestion.TargetNode,
		}
		resp.Confidence = res.Suggestion.Confidence
		resp.Probabilities = res.Suggestion.Probabilities
		resp.Reason = res.Suggestion.Reason
	}
	writeJSON(w, http.StatusOK, resp)
}

// dispatchCandidates 在线 ∩ exposed 候选（目录真相源 + 节点负载合并，稳定序——
// 决策对 state 顺序敏感，同目录同答案）。
func (s *Server) dispatchCandidates() []decision.Candidate {
	inflight := make(map[string]int)
	for _, n := range s.cfg.Core.ListNodes() {
		if n.Online {
			inflight[n.NodeID] = n.Inflight
		}
	}
	cands := make([]decision.Candidate, 0, 8)
	for _, d := range s.cfg.Core.ListDirectory() {
		if !d.Online {
			continue
		}
		cands = append(cands, decision.Candidate{
			NodeID:         d.NodeID,
			Agent:          d.Card.Name,
			Description:    d.Card.Description,
			Kind:           string(d.Card.Kind),
			PermissionMode: d.Card.PermissionMode,
			Inflight:       inflight[d.NodeID],
		})
	}
	decision.SortCandidates(cands)
	return cands
}
