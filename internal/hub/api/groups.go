// groups.go 群聊/协作空间 REST 端点（GROUP-RFC 六 MVP：hub 侧）。
// 全部走 s.auth 鉴权（会话/PAT，与既有管理面同一令牌体系）。MVP 语义：
// 创建即 discussing；concluded 终态拒收消息（409）；agent 发言预算
// （max_rounds，默认 20）用尽返回 budget_exhausted（409）；消息体 8KiB
// 上限超限返回 body_too_large（400）。护栏均由 group.Store 代码强制。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mtl802/swarmmind/internal/decision"
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
	// AutoMembers 智能选人（DESIGN-butler 方案三，2026-10-28 伦哥口径：群聊是
	// 多 agent 协作，成员由中心端 jev 判定、无需人工确认）：true 时忽略 members，
	// 由决策服务从全网在线∩exposed 能力中多选参与集合（SuggestMembers，贡献潜力
	// 降序），建群后对每个成员并发派发群协作任务（任务即唤醒与执行载体）。降级：
	// 决策不可用/无合适成员 → 422 带人话原因，建群不发生（主人回退手选路径）。
	AutoMembers bool `json:"auto_members,omitempty"`
}

// handleGroupCreate POST /api/v1/groups：→ 201 群对象 / 400。
// auto_members=true：jev 选成员 → 建群 → 每成员并发派发群协作任务（submit 正式
// 任务即唤醒与执行载体，group_ref 挂群，终态自动回流；幂等键 groupJoinIdem）。
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if s.groupUnavailable(w) {
		return
	}
	var req groupCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {goal, members[], auto_members?}")
		return
	}
	principal := authPrincipal(r)
	if req.AutoMembers {
		members, apiErr := s.autoSelectMembers(req.Goal)
		if apiErr != nil {
			apiErr.write(w)
			return
		}
		req.Members = members
	}
	g, err := s.cfg.Groups.CreateGroup(req.Goal, adminIdentity, req.Members)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	if req.AutoMembers {
		s.dispatchGroupCollab(g, principal) // best-effort：群已建成，单成员派发失败只记日志
	}
	writeJSON(w, http.StatusCreated, g)
}

// apiErr 简易错误载体（autoSelectMembers 降级路径）。
type apiErr struct {
	code int
	kind string
	msg  string
}

func (e *apiErr) write(w http.ResponseWriter) {
	writeErr(w, e.code, e.kind, e.msg)
}

// autoSelectMembers jev 智能选人（DESIGN-butler 方案三）：SuggestMembers 多选 →
// 转群成员存储形态（node/agent，与控制台建群 members 语义一致）。降级矩阵：
// 决策未启用/失败 → 503 decision_unavailable + Note；全不适合/空集合 → 422
// decision_no_fit + Note（主人回退手选路径，建群不发生——宁缺毋滥不硬凑）。
func (s *Server) autoSelectMembers(goal string) ([]string, *apiErr) {
	if s.cfg.Decision == nil || !s.cfg.Decision.Enabled() {
		return nil, &apiErr{http.StatusServiceUnavailable, "decision_unavailable",
			"决策服务未启用（hub.yaml 未配置 decision 段或密钥不可得）：请手动选择成员建群"}
	}
	res := s.cfg.Decision.SuggestMembers(decision.Request{Goal: goal, Candidates: s.dispatchCandidates()})
	if res.Degraded {
		return nil, &apiErr{http.StatusServiceUnavailable, "decision_unavailable", res.Note}
	}
	if len(res.Members) == 0 {
		return nil, &apiErr{http.StatusUnprocessableEntity, "decision_no_fit", res.Note}
	}
	members := make([]string, 0, len(res.Members))
	for _, key := range res.Members {
		agent, node, ok := strings.Cut(key, "@")
		if !ok {
			continue
		}
		members = append(members, node+"/"+agent)
	}
	if len(members) == 0 {
		return nil, &apiErr{http.StatusUnprocessableEntity, "decision_no_fit", "智能选人结果为空：请手动选择成员建群"}
	}
	return members, nil
}

// groupCollabStdin 群协作任务指引模板（DESIGN-butler 方案三 3.2/R1-03）：任务即
// 唤醒与执行载体——agent 在任务内经 MCP 群工具读写共享消息、贡献协作，FINAL:
// 前缀收敛最终版本候选。每次 group_say 示例（含 FINAL）都显式携带 kind="agent"：
// REST/MCP 缺 kind 固定按 user 插话处理（不计预算、FINAL 不登记），指引若漏带
// 该参数，照做即绕过预算与收敛链。预算耗尽指引（budget_exhausted 后立即收尾，
// 不再发必被拒绝的消息）一并给出。
func groupCollabStdin(g group.Group, member string) string {
	return fmt.Sprintf(`你是协作群 %s 的成员（%s），与其他 agent 协作完成以下目标：
%s

协作方式（swarmmind MCP 群工具）：
1. 先用 swarmmind_group_messages（group_id=%s，since=0）读取已有讨论——其他成员的观点与进展都在里面，先读再说话，避免重复劳动。
2. 贡献你的部分：需要落正式工作用 swarmmind_submit（务必带 group_ref="%s"，结果会自动回流群）；分析观点直接成文。
3. 用 swarmmind_group_say（group_id=%s，from="%s"，kind="agent"）把贡献/观点发到群里——必须带 kind="agent"，发言才计入群预算并参与收敛；漏传会被当成用户插话，FINAL 不被登记。
4. 当你认为已形成「最终版本」（共识达成或你的贡献完成），用 swarmmind_group_say（group_id=%s，from="%s"，kind="agent"）发一条以 FINAL: 开头的总结发言（恰发一条，发出后即收尾）。
注意：群发言预算全群共享，agent 发言计入（本群上限 %d 条），不要刷屏；预算用尽（budget_exhausted）或 FINAL 已发出后立即收尾，不要再发必被拒绝的消息。正式工作成果以 submit 任务结果为准（自动回流，无需复述全文）。`,
		g.ID, member, g.Goal, g.ID, g.ID, g.ID, member, g.ID, member, g.Budget.MaxRounds)
}

// groupJoinIdem 协作任务幂等键（R1-02）：(group, node, agent) 三元组长度前缀
// 编码。成员标识可含「-」「/」（node/agent 命名不设字符限制），无边界信息的
// 拼接会让不同成员碰撞出同键（如 a-b/c 与 a/b-c 同群曾同键 → 第二成员被判
// 「同键不同内容」确定性漏派）；长度前缀让键内边界自描述、编码可逆，同成员
// 重放键稳定（幂等语义不变）。
func groupJoinIdem(groupID, node, agent string) string {
	return fmt.Sprintf("gjoin-%s-%d:%s-%d:%s", groupID, len(node), node, len(agent), agent)
}

// dispatchGroupCollab 协作任务并发派发（DESIGN-butler 方案三 3.2 步骤 3）：对每个
// 成员各派一个群协作任务（submit 正式任务即唤醒与执行载体），顺序落库（hub 单写
// 事务），执行并发由 node 并发槽控制。best-effort：单成员失败只记日志不回滚群——
// 部分成员缺席时其余协作照常，缺席成员可由主人手动补派（同 idem 键幂等）。
func (s *Server) dispatchGroupCollab(g group.Group, principal string) {
	for _, m := range g.Members {
		node, agent, ok := strings.Cut(m, "/")
		if !ok {
			node, agent = "", m
		}
		idem := groupJoinIdem(g.ID, node, agent)
		preID := protocol.NewTaskID()
		if err := s.cfg.Groups.AddTaskRef(g.ID, preID); err != nil {
			log.Printf("[hub] 协作任务预登记失败 group=%s member=%s: %v", g.ID, m, err)
			continue
		}
		req := protocol.TaskSubmitRequest{
			TargetNode:  node,
			TargetAgent: agent,
			Input:       protocol.TaskInput{Stdin: groupCollabStdin(g, m)},
			SubmittedBy: adminIdentity,
			IdemKey:     idem,
			GroupRef:    g.ID,
		}
		ack, err := s.cfg.Core.SubmitWithID(req, preID, "", principal)
		if err != nil {
			s.unwireGroupTask(g.ID, preID)
			log.Printf("[hub] 协作任务派发失败 group=%s member=%s: %v", g.ID, m, err)
			continue
		}
		if ack.Dup {
			s.unwireGroupTask(g.ID, preID)
			_ = s.cfg.Groups.AddTaskRef(g.ID, ack.TaskID)
		} else {
			s.noteGroupTaskCreated(g.ID, ack.TaskID, "协作任务（成员 "+m+"）")
		}
	}
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
// pruned_before_seq 为滚动裁剪已丢消息的最大 seq（GROUP-RFC 三 200 条滚动
// 窗口）：since 落在被裁区间时消息从现存最旧起返回，调用方据此对齐游标。
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
	msgs, prunedBefore, err := s.cfg.Groups.ListMessages(r.PathValue("id"), since)
	if err != nil {
		writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "pruned_before_seq": prunedBefore})
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
	// FINAL 收敛候选登记（DESIGN-butler 方案三 3.1，2026-10-28）：成员发言以
	// FINAL: 前缀开买即登记「最终版本候选」——best-effort 注一条 kind=system 注记
	//（群页高亮展示）；注记失败不影响发言本体（log 后照常返回）。
	if req.Kind == group.KindAgent && strings.HasPrefix(strings.TrimSpace(req.Body), "FINAL:") {
		if _, nerr := s.cfg.Groups.PostMessage(r.PathValue("id"), "hub",
			group.KindSystem, "成员 "+req.From+" 登记最终版本候选（FINAL）"); nerr != nil {
			log.Printf("[hub] FINAL 注记失败 group=%s member=%s: %v", r.PathValue("id"), req.From, nerr)
		}
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
