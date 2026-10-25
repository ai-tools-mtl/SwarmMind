package api

// Work 工作单端点（P5 / RFC-work-resume 三、五 W1-W2 行）：创建（幂等）+ 列表 +
// 详情（含事件游标）+ 原子接管（W2，CAS）。建单入口 v1 仅 Prime/MCP（RFC 六.六
// 裁决 1）——本组端点是 MCP 工具的传输载体与 W3 管理台只读视图的数据源，控制台
// v1 不设建单表单；无泛化 PUT/DELETE（Work 无 update/delete，状态只由生命周期
// 动作推进，RFC 四.8）。W3 交付收集/关闭与处置端点。

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
)

// workCreateRequest POST /api/v1/works 请求体（WorkCreateRequest 平铺解码）。
type workCreateRequest struct {
	core.WorkCreateRequest
}

// handleWorkCreate POST /api/v1/works：建单（同 goal+steps 指纹幂等返回同单）。
// 201 新建 / 200 幂等命中 + dup / 400 invalid_request（goal 缺失）/
// 400 invalid_work_steps（计划校验失败）/ 500 存储故障。created_by 缺省 mcp
// （建单入口 v1 仅 Prime/MCP；审计标签非身份证明，RFC 六.六裁决 2）；事件账本
// 另以独立字段落服务端验证主体（auth 中间件取得，请求不可伪造）。
func (s *Server) handleWorkCreate(w http.ResponseWriter, r *http.Request) {
	var req workCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 WorkCreateRequest JSON: "+err.Error())
		return
	}
	if req.CreatedBy == "" {
		req.CreatedBy = "mcp"
	}
	wv, dup, err := s.cfg.Core.CreateWork(req.WorkCreateRequest, authPrincipal(r))
	if err != nil {
		switch {
		case errors.Is(err, core.ErrInvalidWorkSteps):
			writeErr(w, http.StatusBadRequest, "invalid_work_steps", err.Error())
		case errors.Is(err, core.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		default:
			log.Printf("[hub] 工作单创建持久化失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "工作单创建失败")
		}
		return
	}
	code := http.StatusCreated
	if dup {
		code = http.StatusOK // 幂等命中：200 + dup（同 POST /tasks 语义）
	}
	writeJSON(w, code, workViewBody(wv, dup))
}

// handleWorkList GET /api/v1/works：工作单列表摘要（新建在前）。
func (s *Server) handleWorkList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"works": s.cfg.Core.ListWorks()})
}

// workTakeoverRequest POST /api/v1/works/{id}/takeover 请求体：expected_term 为
// 调用方读档时看到的任期（代校验，未设任期为空串）；new_term 为接管方任期声明。
type workTakeoverRequest struct {
	ExpectedTerm string `json:"expected_term"`
	NewTerm      string `json:"new_term"`
}

// handleWorkTakeover POST /api/v1/works/{id}/takeover：原子接管（P5 W2，CAS 单
// 事务）。200 成功（相同操作重放 200 + dup:true 返现状，零事件）/
// 404 work_not_found / 409 work_term_conflict（同刻仅一方接管成功）/
// 409 work_closed（终态单）/ 400 invalid_request / 500。事件以独立字段落
// 服务端验证主体（auth 中间件取得，同建单口径）。
func (s *Server) handleWorkTakeover(w http.ResponseWriter, r *http.Request) {
	var req workTakeoverRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {expected_term,new_term} JSON: "+err.Error())
		return
	}
	wv, dup, err := s.cfg.Core.TakeoverWork(core.WorkTakeoverRequest{
		WorkID:       r.PathValue("id"),
		ExpectedTerm: req.ExpectedTerm,
		NewTerm:      req.NewTerm,
	}, authPrincipal(r))
	if err != nil {
		switch {
		case errors.Is(err, core.ErrWorkNotFound):
			writeErr(w, http.StatusNotFound, "work_not_found", err.Error())
		case errors.Is(err, core.ErrWorkClosed):
			writeErr(w, http.StatusConflict, "work_closed", err.Error())
		case errors.Is(err, core.ErrWorkTermConflict):
			writeErr(w, http.StatusConflict, "work_term_conflict", err.Error())
		case errors.Is(err, core.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		default:
			log.Printf("[hub] 工作单接管持久化失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "工作单接管失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, workViewBody(wv, dup))
}

// handleWorkGet GET /api/v1/works/{id}?since_events=N：工作单详情（步骤/尝试
// 全量 + 事件账本按游标增量 + tasks_gone 读档注解，pruned_before_events 标注已裁
// 区间上界——同群消息 pruned_before_seq 口径）。
func (s *Server) handleWorkGet(w http.ResponseWriter, r *http.Request) {
	var since int64
	if raw := r.URL.Query().Get("since_events"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, http.StatusBadRequest, "invalid_request", "since_events 须为非负整数")
			return
		}
		since = v
	}
	wv, ok := s.cfg.Core.GetWork(r.PathValue("id"), since)
	if !ok {
		writeErr(w, http.StatusNotFound, "work_not_found", "工作单不存在")
		return
	}
	writeJSON(w, http.StatusOK, workViewBody(wv, false))
}

// workCollectRequest POST /api/v1/works/{id}/collect 请求体：prime_term 任期声明
// （编排写入须与落库任期一致）；report 收集报告必填；outcome 关闭方式（空/"done"=
// 完成关闭，"abandoned"=处置放弃）。
type workCollectRequest struct {
	PrimeTerm string `json:"prime_term"`
	Report    string `json:"report"`
	Outcome   string `json:"outcome"`
}

// handleWorkCollect POST /api/v1/works/{id}/collect：收集并关闭（P5 W3，单事务
// 终态闭合——RFC 二.2 步骤 4）。200 成功（相同请求重放三同源 200+dup:true 返现状
// 零事件）/ 404 work_not_found / 409 work_closed（已终态且非同源回执）/
// 409 work_no_term / 409 work_term_conflict（旧任期收集拒绝）/ 409 work_steps_open
// （存在未终态步骤）/ 409 work_steps_not_done（部分完成不得标成 done，指路
// abandoned）/ 400 invalid_request（report 空、outcome 非法）/ 500。事件以独立
// 字段落服务端验证主体（auth 中间件取得，同建单/接管口径）。
func (s *Server) handleWorkCollect(w http.ResponseWriter, r *http.Request) {
	var req workCollectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {prime_term,report,outcome} JSON: "+err.Error())
		return
	}
	wv, dup, err := s.cfg.Core.CollectWork(core.WorkCollectRequest{
		WorkID:    r.PathValue("id"),
		PrimeTerm: req.PrimeTerm,
		Report:    req.Report,
		Outcome:   req.Outcome,
	}, authPrincipal(r))
	if err != nil {
		switch {
		case errors.Is(err, core.ErrWorkNotFound):
			writeErr(w, http.StatusNotFound, "work_not_found", err.Error())
		case errors.Is(err, core.ErrWorkClosed):
			writeErr(w, http.StatusConflict, "work_closed", err.Error())
		case errors.Is(err, core.ErrWorkNoTerm):
			writeErr(w, http.StatusConflict, "work_no_term", err.Error())
		case errors.Is(err, core.ErrWorkTermConflict):
			writeErr(w, http.StatusConflict, "work_term_conflict", err.Error())
		case errors.Is(err, core.ErrWorkStepsOpen):
			writeErr(w, http.StatusConflict, "work_steps_open", err.Error())
		case errors.Is(err, core.ErrWorkStepsNotDone):
			writeErr(w, http.StatusConflict, "work_steps_not_done", err.Error())
		case errors.Is(err, core.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		default:
			log.Printf("[hub] 工作单收集持久化失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "工作单收集失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, workViewBody(wv, dup))
}

// workCollectBody 工作单响应体形态：store.Work 平铺 + tasks_gone 读档注解
// （W3 接口补全：尝试任务已过保留期清理的列表，注解描述 work 自身事实，故随
// work 对象输出而非顶层）。
type workCollectBody struct {
	store.Work
	TasksGone []string `json:"tasks_gone,omitempty"`
}

// workViewBody 详情响应体：WorkView 平铺 + dup 标记（仅创建响应可能为 true；
// GET 恒 false，omitempty 不输出）。
func workViewBody(wv *core.WorkView, dup bool) map[string]any {
	body := map[string]any{
		"work":                 workCollectBody{Work: wv.Work, TasksGone: wv.TasksGone},
		"pruned_before_events": wv.PrunedBeforeEvents,
	}
	if dup {
		body["dup"] = true
	}
	return body
}
