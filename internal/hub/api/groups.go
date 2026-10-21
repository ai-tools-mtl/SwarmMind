// groups.go 群聊/协作空间 REST 端点（GROUP-RFC 六 MVP：hub 侧）。
// 全部走 s.auth 鉴权（会话/PAT，与既有管理面同一令牌体系）。MVP 语义：
// 创建即 discussing；concluded 终态拒收消息（409）；agent 发言预算
// （max_rounds，默认 20）用尽返回 budget_exhausted（409）；消息体 8KiB
// 上限超限返回 body_too_large（400）。护栏均由 group.Store 代码强制。
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/mtl802/swarmmind/internal/hub/group"
)

// groupUnavailable Groups 未接线（旧配置/测试基座）时群端点统一降级 503。
func (s *Server) groupUnavailable(w http.ResponseWriter) bool {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "group_unavailable", "群聊功能未启用")
		return true
	}
	return false
}

// groupCreateRequest 建群请求体（GROUP-RFC 六 MVP：人建群；host 固定会话
// 身份 admin，对齐 SPEC 六.五 提交身份纪律）。
type groupCreateRequest struct {
	Goal    string   `json:"goal"`
	Members []string `json:"members"`
}

// handleGroupCreate POST /api/v1/groups：→ 201 群对象 / 400。
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	var req groupCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {goal, members[]}")
		return
	}
	g, err := s.cfg.Groups.CreateGroup(req.Goal, adminIdentity, req.Members)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// handleGroupList GET /api/v1/groups：全量群列表（按创建时间升序）。
func (s *Server) handleGroupList(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": s.cfg.Groups.ListGroups()})
}

// handleGroupMessages GET /api/v1/groups/{id}/messages?since=N：增量拉取
// （GROUP-RFC 三 group_sync 补齐语义：返回 seq > since 的消息，升序）。
func (s *Server) handleGroupMessages(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", "since 须为整数")
			return
		}
		since = v
	}
	msgs, err := s.cfg.Groups.ListMessages(r.PathValue("id"), since)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// groupSayRequest 群发言请求体。from 为发言者标识；kind 须 user|agent|system
// （agent 计入发言预算）。MVP 阶段 from 由调用方声明，node 侧接入批次再按
// 连接身份强制。
type groupSayRequest struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// handleGroupSay POST /api/v1/groups/{id}/messages：→ 201 消息对象 /
// 400 body_too_large / 409 budget_exhausted、group_concluded / 404。
func (s *Server) handleGroupSay(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	var req groupSayRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {from, kind, body}")
		return
	}
	if req.Kind == "" {
		req.Kind = group.KindUser // 缺省按人插话（GROUP-RFC 六：人可随时插话）
	}
	msg, err := s.cfg.Groups.PostMessage(r.PathValue("id"), req.From, req.Kind, req.Body)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, msg)
}

// groupCloseRequest 关群请求体（可省略；by 缺省 admin）。
type groupCloseRequest struct {
	By string `json:"by"`
}

// handleGroupClose POST /api/v1/groups/{id}/close：→ 200 群对象（重复关闭
// 幂等，原样返回首次审计）/ 404。
func (s *Server) handleGroupClose(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	var req groupCloseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {by?}")
		return
	}
	if req.By == "" {
		req.By = adminIdentity
	}
	g, err := s.cfg.Groups.CloseGroup(r.PathValue("id"), req.By)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// writeGroupErr group.Store 哨兵错误 → HTTP 状态与错误码。
func writeGroupErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, group.ErrNotFound):
		writeErr(w, http.StatusNotFound, "group_not_found", err.Error())
	case errors.Is(err, group.ErrConcluded):
		writeErr(w, http.StatusConflict, "group_concluded", err.Error())
	case errors.Is(err, group.ErrBudgetExhausted):
		writeErr(w, http.StatusConflict, "budget_exhausted", err.Error())
	case errors.Is(err, group.ErrBodyTooLarge):
		writeErr(w, http.StatusBadRequest, "body_too_large", err.Error())
	case errors.Is(err, group.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		log.Printf("[hub] 群操作内部错误: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "群操作失败")
	}
}
