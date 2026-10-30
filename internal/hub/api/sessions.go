package api

// sessions.go 会话（Session）与轮次（Turn）REST 端点（T2 持久会话，SPEC 六.五
// 「会话与轮次」节；上游 docs/TASK-agent-session-contract.md）。鉴权与既有管理
// API 同口径（s.auth：会话 cookie 或 Bearer PAT）。Send 语义异步：受理即回 turn
// 引用，轮终状态经详情端点查询（不做长阻塞请求）；close 幂等回执；无泛化
// PUT/DELETE（会话状态只由 create-turn/close 生命周期动作推进，同 Work 口径）。

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionCreateRequest POST /api/v1/sessions 请求体（core.SessionCreateRequest
// 平铺解码）。
type sessionCreateRequest struct {
	core.SessionCreateRequest
}

// handleSessionCreate POST /api/v1/sessions：建会话（注册表登记 + 绑定快照落定，
// 不派发任务）。201 新建 / 400（缺字段/全权档位断言）/ 404 agent_not_exposed /
// 409 binding_mismatch（权限断言不符）/ 422 session_unsupported（目标未声明
// 会话能力）。幂等（W01-03 RFC §1）：不带 request_id 行为零变化（重复调用即新
// 会话）；带 request_id 同键同文 200 + dup:true 返原会话、同键异文 409
// request_conflict。
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req sessionCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {node_id, agent, cwd} JSON: "+err.Error())
		return
	}
	sv, dup, err := s.cfg.Core.CreateSession(req.SessionCreateRequest, authPrincipal(r))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	code := http.StatusCreated
	body := map[string]any{"session": sv}
	if dup {
		code = http.StatusOK
		body["dup"] = true
	}
	writeJSON(w, code, body)
}

// handleSessionList GET /api/v1/sessions：会话列表摘要（创建时间倒序）。
func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.cfg.Core.ListSessions()})
}

// handleSessionGet GET /api/v1/sessions/{id}：会话详情（轮次账本全量 + 实时投影
// 状态；V0 低量不分页）。
func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.cfg.Core.GetSession(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "session_not_found", "会话不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sv})
}

// sessionTurnRequest POST /api/v1/sessions/{id}/turns 请求体。
type sessionTurnRequest struct {
	core.SessionTurnRequest
}

// handleSessionTurn POST /api/v1/sessions/{id}/turns：提交轮次（会话轮任务与轮次
// 账本同事务落库，异步执行）。202 受理回执（session_id/turn_seq/op/task_id/
// trace_id）/ 200 + dup（同号同文重放或同 request_id 同文重放回执——响应丢失重试
// 安全，任务清理后仍可回执）/ 400 / 404 / 409（closed / binding_mismatch /
// turn_seq_conflict / request_conflict / turn_queue_full / turn_in_flow /
// conflict 容量类）。
func (s *Server) handleSessionTurn(w http.ResponseWriter, r *http.Request) {
	var req sessionTurnRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {prompt, turn_seq?, request_id?} JSON: "+err.Error())
		return
	}
	ref, dup, err := s.cfg.Core.SubmitTurn(r.PathValue("id"), req.SessionTurnRequest, authPrincipal(r))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	code := http.StatusAccepted
	body := map[string]any{"turn": ref}
	if dup {
		code = http.StatusOK
		body["dup"] = true
	}
	writeJSON(w, code, body)
}

// handleSessionTurnCancel POST /api/v1/sessions/{id}/turns/{seq}/cancel：取消受理
// 轮（W01-03 RFC §6.2，人工专属管理动作——SPEC 六.六豁免⑬，会话工具组不提供
// 隐式 cancel）。管理面鉴权与既有会话端点同口径（s.auth：cookie/PAT）。
// 200 同步回执（终态重放 / 未投递撤回 queued）/ 202 异步受理（取消任务已投递，
// 回执经会话详情轮行查询）/ 404 / 409 turn_already_finished / 500
// cancel_unconfirmed（不虚报）。
func (s *Server) handleSessionTurnCancel(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "turn_seq 须为正整数")
		return
	}
	var req struct {
		Reason string `json:"reason,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req)
	}
	receipt, async, err := s.cfg.Core.CancelSessionTurn(r.PathValue("id"), seq, req.Reason, authPrincipal(r))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	code := http.StatusOK
	if async {
		code = http.StatusAccepted
	}
	writeJSON(w, code, map[string]any{"cancel": receipt})
}

// handleSessionClose POST /api/v1/sessions/{id}/close：关闭会话（注册表即闭 +
// close 任务随投递链收尾 node 侧原生映射）。200（重复关闭 200 + dup:true 零写
// 入零任务）/ 404。
func (s *Server) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	sv, dup, err := s.cfg.Core.CloseSession(r.PathValue("id"), authPrincipal(r))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	body := map[string]any{"session": sv}
	if dup {
		body["dup"] = true
	}
	writeJSON(w, http.StatusOK, body)
}

// writeSessionErr 会话编排错误 → HTTP 状态与错误码（SPEC 六.五）。
func writeSessionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrSessionNotFound):
		writeErr(w, http.StatusNotFound, "session_not_found", err.Error())
	case errors.Is(err, core.ErrSessionClosed):
		writeErr(w, http.StatusConflict, "session_closed", err.Error())
	case errors.Is(err, core.ErrSessionAgentUnsupported):
		writeErr(w, http.StatusUnprocessableEntity, "session_unsupported", err.Error())
	case errors.Is(err, core.ErrSessionBindingChanged):
		writeErr(w, http.StatusConflict, "binding_mismatch", err.Error())
	case errors.Is(err, core.ErrSessionTurnConflict):
		writeErr(w, http.StatusConflict, "turn_seq_conflict", err.Error())
	case errors.Is(err, core.ErrSessionTurnInFlight):
		writeErr(w, http.StatusConflict, "turn_in_flight", err.Error())
	case errors.Is(err, core.ErrSessionQueueFull):
		writeErr(w, http.StatusConflict, "turn_queue_full", err.Error())
	case errors.Is(err, core.ErrSessionCloseInFlight):
		writeErr(w, http.StatusConflict, "close_in_flight", err.Error())
	case errors.Is(err, core.ErrSessionRequestConflict):
		writeErr(w, http.StatusConflict, "request_conflict", err.Error())
	case errors.Is(err, core.ErrSessionTurnNotFound):
		writeErr(w, http.StatusNotFound, "turn_not_found", err.Error())
	case errors.Is(err, core.ErrSessionTurnNotCancellable):
		writeErr(w, http.StatusConflict, "turn_already_finished", err.Error())
	case errors.Is(err, core.ErrCancelUnconfirmed):
		writeErr(w, http.StatusInternalServerError, "cancel_unconfirmed", err.Error())
	case errors.Is(err, core.ErrAgentNotExposed):
		writeErr(w, http.StatusNotFound, protocol.CodeAgentNotExposed, err.Error())
	case errors.Is(err, mailbox.ErrMailboxFull):
		writeErr(w, http.StatusServiceUnavailable, protocol.CodeMailboxFull, err.Error())
	case errors.Is(err, core.ErrStorageFull):
		writeErr(w, http.StatusServiceUnavailable, protocol.CodeStorageFull, err.Error())
	case errors.Is(err, protocol.ErrFrameTooLarge):
		writeErr(w, http.StatusBadRequest, protocol.CodeFrameTooLarge, err.Error())
	case errors.Is(err, core.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		log.Printf("[hub] 会话编排内部错误: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "会话操作失败")
	}
}
