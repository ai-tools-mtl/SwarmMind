// sessions_api.go agent 会话面（W01-03，docs/tasks/v3/W01-03-rfc.md；SPEC 六.五
// 「会话与轮次」v3 增量）：`/api/v1/sessions*` 与 hub 同语义——创建（201 登记不执
// 行模型不投轮）、列表/详情（内核快照 ∪ 受理中会话 pending 投影）、投轮（202 受
// 理）。鉴权 authOrchestrate（编排凭据经 W02-01 门：prime_token 需显式 mcpaccess
// enabled；主人 local_token 恒放行），与本地任务编排同一门。数据面走 service
// SubmitAgentSessionTurn / sessionop 请求登记（创建持久记录平台 ID，pending 首轮
// 发布前可查——不靠浏览器留 ID）。
//
// cancel 不在本面：POST /{id}/turns/{seq}/cancel 为人工专属（authLocal，主人
// local_token 专属，prime_token 一律 401——取消是终局干预，开放 agent 面即互删
// 通道，SPEC 六.六豁免⑬）；close 同源豁免（主人旧 /api/v1/local/sessions/{id}/
// close 保持唯一本机关闭入口）。
package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// agentCancelHTTPBudget 取消端点等待上限（service 会话取消预算之上的人机可感
// 上限，RFC §6.1；ctx 只约束调用方等待，不撤销已登记的取消意图）。
const agentCancelHTTPBudget = 35 * time.Second

// agentSessionCreateRequest POST /api/v1/sessions 请求体（W01-03 RFC §1）。
type agentSessionCreateRequest struct {
	// Agent 目标 agent 名（须声明 session_adapter）。
	Agent string `json:"agent"`
	// Cwd 会话工作目录（本机绝对路径；经 AuthorizeWorkspace 强制，越界 403）。
	Cwd string `json:"cwd"`
	// Title 会话标题（可选；仅受理登记期可见，内核注册表无 title 字段）。
	Title string `json:"title,omitempty"`
	// RequestID 稳定创建请求 ID（可选）：同键同文返回原会话、异文 409；缺省
	// 自动生成（创建必须在返回前持久记录平台 ID，pending 可查不依赖调用方纪律）。
	RequestID string `json:"request_id,omitempty"`
}

// handleAgentSessionCreate POST /api/v1/sessions：创建登记（201，不执行模型不投
// 轮——首轮经 POST turns 显式提交）。幂等：同键同文 200+dup 返原会话、同键异文
// 409 request_conflict。
func (s *Server) handleAgentSessionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	var req agentSessionCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {agent, cwd} JSON: "+err.Error())
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "req-" + protocol.NewTraceID() // 自动登记：pending 可查闭环（RFC §1）
	}
	payloadHash := service.FingerprintSessionPayload(req.Agent, req.Cwd, req.Title)

	// 重放/冲突预检（回执查询不受当前配置状态阻断——agent 能力/工作区校验只拦
	// 新创建，RFC §1）。
	if rec, ok := s.cfg.Service.LookupAgentSessionRequest(requestID); ok {
		if rec.Kind == sessionop.RequestKindSession && rec.PayloadHash == payloadHash {
			s.writeAgentSessionView(w, http.StatusOK, rec, true)
			return
		}
		writeErr(w, http.StatusConflict, "request_conflict",
			"request_id 已被不同内容占用（已登记 session="+rec.SessionID+"）")
		return
	}

	// 新创建校验（与主人面创建同一判定源：会话装配表 + cwd 绝对路径 + 工作区授权）。
	if req.Agent == "" || strings.TrimSpace(req.Cwd) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "agent 与 cwd 均必填")
		return
	}
	if !s.cfg.Service.SessionAgentConfigured(req.Agent) {
		writeErr(w, http.StatusNotFound, protocol.CodeAgentNotFound,
			"agent "+req.Agent+" 未声明会话能力（session_adapter 未配置）或不在本机配置")
		return
	}
	if !filepath.IsAbs(req.Cwd) {
		writeErr(w, http.StatusBadRequest, "input_invalid", "cwd 必须为本机绝对路径，得 "+req.Cwd+"")
		return
	}
	if err := s.cfg.Service.AuthorizeSessionWorkspace(req.Cwd); err != nil {
		writeAgentSessionErr(w, err)
		return
	}

	sid := protocol.NewSessionID()
	if _, _, err := s.cfg.Service.RegisterAgentSessionCreation(requestID, sid, req.Agent, req.Cwd, req.Title, payloadHash); err != nil {
		if errors.Is(err, sessionop.ErrRequestConflict) {
			// 并发同键同文创建：登记原子仲裁，败者经 hash 复查一致收敛为重放（RFC §1）。
			if rec, ok := s.cfg.Service.LookupAgentSessionRequest(requestID); ok &&
				rec.Kind == sessionop.RequestKindSession && rec.PayloadHash == payloadHash {
				s.writeAgentSessionView(w, http.StatusOK, rec, true)
				return
			}
			writeErr(w, http.StatusConflict, "request_conflict", "request_id 已被不同内容占用")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", "创建登记落盘失败（未创建，重试安全）: "+err.Error())
		return
	}
	rec, _ := s.cfg.Service.LookupAgentSessionRequest(requestID)
	s.writeAgentSessionView(w, http.StatusCreated, rec, false)
}

// writeAgentSessionView 会话单视图应答（内核已发布=内核快照；未发布=pending 投影）。
func (s *Server) writeAgentSessionView(w http.ResponseWriter, code int, rec sessionop.RequestRecord, dup bool) {
	body := map[string]any{}
	if dup {
		body["dup"] = true
	}
	if sess, ok := s.cfg.Service.SessionLocal(rec.SessionID); ok {
		body["session"] = sess
	} else {
		body["session"] = s.agentPendingView(rec)
	}
	writeJSON(w, code, body)
}

// agentPendingTurnView pending 会话受理轮投影（账本受理事实，RFC §2）。
type agentPendingTurnView struct {
	Seq    int64  `json:"seq"`
	State  string `json:"state"` // queued | running | finished
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// agentPendingSessionView 受理中会话投影（字段与内核 protocol.Session 同形另加
// pending/request_id/title；status=「pending」为受理登记事实投影值——注册表两值
// active/closed 不变，W01-01 §3）。
type agentPendingSessionView struct {
	ID             string                 `json:"id"`
	AgentName      string                 `json:"agent_name"`
	Adapter        string                 `json:"adapter,omitempty"`
	Cwd            string                 `json:"cwd"`
	PermissionMode string                 `json:"permission_mode,omitempty"`
	Status         string                 `json:"status"`
	TurnCount      int                    `json:"turn_count"`
	Turns          []agentPendingTurnView `json:"turns"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	Pending        bool                   `json:"pending"`
	RequestID      string                 `json:"request_id,omitempty"`
	Title          string                 `json:"title,omitempty"`
}

// agentPendingView 创建登记 → pending 投影（受理态由账本事实合成）。
func (s *Server) agentPendingView(rec sessionop.RequestRecord) agentPendingSessionView {
	v := agentPendingSessionView{
		ID:        rec.SessionID,
		AgentName: rec.Agent,
		Cwd:       rec.Cwd,
		Status:    "pending",
		Turns:     []agentPendingTurnView{},
		CreatedAt: rec.AcceptedAt,
		UpdatedAt: rec.AcceptedAt,
		Pending:   true,
		RequestID: rec.RequestID,
		Title:     rec.Title,
	}
	if card := s.sessionCard(rec.Agent); card != nil {
		v.Adapter = card.SessionAdapter
		v.PermissionMode = card.PermissionMode
	}
	if s.cfg.Ledger != nil {
		for _, e := range s.cfg.Ledger.View() {
			sr := e.Task.Session
			if sr == nil || sr.TurnSeq <= 0 || sr.SessionID != rec.SessionID {
				continue
			}
			t := agentPendingTurnView{Seq: sr.TurnSeq}
			switch e.State {
			case ledger.StateFinished:
				t.State = "finished"
				if e.Result != nil {
					t.Status = string(e.Result.Status)
					if code, _, _ := strings.Cut(e.Result.Error, ":"); code != "" {
						t.Error = code
					}
				}
			case ledger.StateStarted:
				t.State = "running"
			default:
				t.State = "queued"
			}
			v.Turns = append(v.Turns, t)
			v.TurnCount++
			if e.Task.CreatedAt.After(v.UpdatedAt) {
				v.UpdatedAt = e.Task.CreatedAt
			}
		}
	}
	return v
}

// handleAgentSessionList GET /api/v1/sessions：内核快照 ∪ 受理中会话（创建时刻
// 升序；pending 项在首轮发布前可查平台 ID+受理态——「未列出=未受理」误导禁绝，
// RFC §2）。
func (s *Server) handleAgentSessionList(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	kernel := s.cfg.Service.SessionsLocal()
	if kernel == nil {
		kernel = []protocol.Session{}
	}
	merged := make([]any, 0, len(kernel)+4)
	for _, sess := range kernel {
		merged = append(merged, sess)
	}
	kernelIDs := make(map[string]bool, len(kernel))
	for _, sess := range kernel {
		kernelIDs[sess.ID] = true
	}
	for _, rec := range s.cfg.Service.AgentSessionCreations() {
		if kernelIDs[rec.SessionID] {
			continue // 首轮已发布：内核快照为准（同一 ID 两者互斥，防御去重）
		}
		merged = append(merged, s.agentPendingView(rec))
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return agentSessionCreatedAt(merged[i]).Before(agentSessionCreatedAt(merged[j]))
	})
	writeJSON(w, http.StatusOK, map[string]any{"sessions": merged})
}

// agentSessionCreatedAt 列表排序键（内核条目/ pending 投影统一创建时刻）。
func agentSessionCreatedAt(v any) time.Time {
	switch s := v.(type) {
	case protocol.Session:
		return s.CreatedAt
	case agentPendingSessionView:
		return s.CreatedAt
	}
	return time.Time{}
}

// handleAgentSessionGet GET /api/v1/sessions/{id}：详情（内核快照或 pending 投影；
// 都无 = 404 session_not_found——受理登记事实保证创建即 consequent 可查）。
func (s *Server) handleAgentSessionGet(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	id := r.PathValue("id")
	if sess, ok := s.cfg.Service.SessionLocal(id); ok {
		writeJSON(w, http.StatusOK, map[string]any{"session": sess})
		return
	}
	if rec, ok := s.cfg.Service.AgentSessionCreation(id); ok {
		writeJSON(w, http.StatusOK, map[string]any{"session": s.agentPendingView(rec)})
		return
	}
	writeErr(w, http.StatusNotFound, "session_not_found", "会话不存在（本机内核无映射且无受理登记）")
}

// agentSessionTurnRequest POST /api/v1/sessions/{id}/turns 请求体（RFC §3）。
type agentSessionTurnRequest struct {
	Prompt    string `json:"prompt"`
	TurnSeq   int64  `json:"turn_seq,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// handleAgentSessionTurns POST /api/v1/sessions/{id}/turns：投轮受理（202；复用
// sessionop 守卫——队列上限 409 turn_queue_full、关闭防护、序号账本同源；pending
// 会话以预分配平台 ID 受理 create 轮）。同 request_id 同文回原受理（200+dup）、
// 异文 409 request_conflict。
func (s *Server) handleAgentSessionTurns(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	var req agentSessionTurnRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {prompt, turn_seq?, request_id?} JSON: "+err.Error())
		return
	}
	ack, dup, err := s.cfg.Service.SubmitAgentSessionTurn(service.AgentSessionTurnRequest{
		NodeID:    s.cfg.NodeID,
		SessionID: r.PathValue("id"),
		Prompt:    req.Prompt,
		TurnSeq:   req.TurnSeq,
		RequestID: req.RequestID,
	})
	if err != nil {
		writeAgentSessionErr(w, err)
		return
	}
	code := http.StatusAccepted
	body := map[string]any{"turn": ack}
	if dup {
		code = http.StatusOK
		body["dup"] = true
	}
	writeJSON(w, code, body)
}

// handleAgentSessionTurnCancel POST /api/v1/sessions/{id}/turns/{seq}/cancel：
// 轮取消（人工专属，authLocal；SPEC 六.六豁免⑬——prime_token 一律 401，agent
// 编排面无取消能力）。同步回执 queued/executed/unknown 如实（RFC §6.1）。
func (s *Server) handleAgentSessionTurnCancel(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "turn_seq 须为正整数")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), agentCancelHTTPBudget)
	defer cancel()
	rc, err := s.cfg.Service.CancelSessionTurnLocal(ctx, r.PathValue("id"), seq)
	if err != nil {
		writeAgentSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancel": rc})
}

// writeAgentSessionErr agent 会话面错误 → HTTP（RFC §7 映射表；RemoteError 语义）。
func writeAgentSessionErr(w http.ResponseWriter, err error) {
	var re *service.RemoteError
	if errors.As(err, &re) {
		code := http.StatusInternalServerError
		switch re.Code {
		case "invalid", sessionInputCode:
			code = http.StatusBadRequest
		case protocol.CodeAgentNotFound, "session_not_found", "turn_not_found":
			code = http.StatusNotFound
		case "workspace_denied":
			code = http.StatusForbidden
		case "session_closed", "turn_in_flight", "turn_queue_full", "request_conflict",
			"turn_seq_conflict", "turn_already_finished", "close_in_flight", "binding_mismatch":
			code = http.StatusConflict
		case "session_unsupported":
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, re.Code, re.Msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", err.Error())
}
