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
	"strings"

	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// groupUnavailable Groups 未接线（旧配置/测试基座）时群端点统一降级 503。
func (s *Server) groupUnavailable(w http.ResponseWriter) bool {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "group_unavailable", "群聊功能未启用")
		return true
	}
	return false
}

// groupRefCheck 校验提交请求携带的 group_ref（GROUP-RFC 转任务闭环）：群必须
// 存在且进行中——创建注记消息要求群可收消息。失败写 HTTP 错误并返回 false。
// groupRef 为空直接放行。
func (s *Server) groupRefCheck(w http.ResponseWriter, groupRef string) bool {
	if groupRef == "" {
		return true
	}
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "group_unavailable", "群聊功能未启用，group_ref 不可用")
		return false
	}
	g, ok := s.cfg.Groups.GetGroup(groupRef)
	if !ok {
		writeErr(w, http.StatusNotFound, "group_not_found", "group_ref 指向的群不存在")
		return false
	}
	if g.Status == group.StatusConcluded {
		writeErr(w, http.StatusConflict, "group_concluded", "group_ref 指向的群已结束，不能再转入任务")
		return false
	}
	return true
}

// wireGroupTask 任务入群登记（GROUP-RFC group_task_created 语义）：登记群内任务
// 引用并注入 kind=system 创建消息。登记/注记失败只记日志：任务已提交生效，注记
// 属 best-effort，不回滚提交。
//
// 本函数用于批次等「任务 ID 生成于 core 内部、登记只能后置」的路径。单任务提交
// 走「先登记后提交」（见 handleSubmit：预生成 ID + AddTaskRef 先于 Submit），使
// 快速任务（membus 同步链下终态在 Submit 内即回落）的 fireTerminal 回流时引用
// 必已就位。此处登记后置存在同型竞态——任务可能在登记前已终态且回流扫描扑空——
// 故登记后对已终态任务补偿触发一次回流：RefluxTaskResult 以已回流集合单事务
// 保证恰好一次，与终态钩子并发安全。
func (s *Server) wireGroupTask(groupRef, taskID, desc string) {
	if s.cfg.Groups == nil || groupRef == "" || taskID == "" {
		return
	}
	if err := s.cfg.Groups.AddTaskRef(groupRef, taskID); err != nil {
		log.Printf("[hub] 任务入群登记失败 task=%s group=%s: %v", taskID, groupRef, err)
		return
	}
	s.noteGroupTaskCreated(groupRef, taskID, desc)
	s.refluxIfTerminal(groupRef, taskID)
}

// noteGroupTaskCreated 转任务创建注记：注入 kind=system 创建消息（group_task_
// created）；失败只记日志（best-effort，不影响任务）。
func (s *Server) noteGroupTaskCreated(groupRef, taskID, desc string) {
	if s.cfg.Groups == nil || groupRef == "" || taskID == "" {
		return
	}
	if _, err := s.cfg.Groups.PostMessage(groupRef, group.RefluxFrom, group.KindSystem, group.TaskCreatedBody(taskID, desc)); err != nil {
		log.Printf("[hub] 转任务系统消息注入失败 task=%s group=%s: %v", taskID, groupRef, err)
	}
}

// unwireGroupTask 回滚预登记（批 B 修复1）：提交失败/幂等命中时移除「先登记后
// 提交」加上的引用。失败只记日志——孤儿引用（无对应任务）在回流扫描中不命中，
// 无害；下轮同任务登记幂等覆盖。
func (s *Server) unwireGroupTask(groupRef, taskID string) {
	if s.cfg.Groups == nil || groupRef == "" || taskID == "" {
		return
	}
	if err := s.cfg.Groups.RemoveTaskRef(groupRef, taskID); err != nil {
		log.Printf("[hub] 任务入群登记回滚失败 task=%s group=%s: %v", taskID, groupRef, err)
	}
}

// refluxIfTerminal 任务已终态则补偿回流（批 B 修复1：登记后置路径的漏回流兜底）。
// 终态在登记前已落时 fireTerminal 的回流扫描扑空且不记已回流集合，此处补发；
// 终态钩子已回流过则被幂等集合挡下，恰好一次。
func (s *Server) refluxIfTerminal(groupRef, taskID string) {
	t, ok := s.cfg.Core.GetTask(taskID)
	if !ok || !t.Status.Terminal() {
		return
	}
	if _, err := s.cfg.Groups.RefluxTaskResult(t.ID, group.TaskTerminalBody(t)); err != nil {
		log.Printf("[hub] 任务终态补偿回流失败 task=%s group=%s: %v", taskID, groupRef, err)
	}
}

// taskDesc 任务描述（转任务注记用）：优先 stdin（控制台转任务弹层承载描述的
// 约定通道），退回 args 拼接。
func taskDesc(input protocol.TaskInput) string {
	if s := strings.TrimSpace(input.Stdin); s != "" {
		return s
	}
	return strings.Join(input.Args, " ")
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

// groupSayRequest 群发言请求体。from 为发言者标识；REST/MCP 发言只允许
// kind=user|agent（agent 计入发言预算）——kind=system 为 hub 内部回流专用保留字
// （创建注记/终态回流由服务端注入，见 wireGroupTask/RefluxTaskResult），外部
// 传入一律 400 拒绝，防止伪造系统注记污染时间线。
type groupSayRequest struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// handleGroupSay POST /api/v1/groups/{id}/messages：→ 201 消息对象 /
// 400 body_too_large、kind=system 保留字 / 409 budget_exhausted、group_concluded / 404。
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
	if req.Kind == group.KindSystem {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"kind=system 为系统回流保留类别（仅 hub 内部注入），发言只允许 user|agent")
		return
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
