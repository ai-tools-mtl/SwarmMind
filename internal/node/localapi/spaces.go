// spaces.go 协作空间 REST（W04-01 主人面骨架 + W04-02 派发/消息开线，docs/tasks/
// v3/W04-01-rfc.md §六 + W04-spaces.md 任务书）：
//   - POST /api/v1/spaces                      创建（编排主体仅 external 待领取）
//   - GET  /api/v1/spaces                      列表
//   - GET  /api/v1/spaces/{id}                 详情（全量视图 + 在途投影）
//   - POST /api/v1/spaces/{id}/control         控制骨架（逐动作授权表；resume 在途确认门）
//   - POST /api/v1/spaces/{id}/dispatches      指名派发（意图持久+串行调度，202 受理）
//   - GET  /api/v1/spaces/{id}/dispatches/{request_id}  派发/意图查询（轮终投影）
//   - POST /api/v1/spaces/{id}/messages        发言（零唤醒记账+消息账本）
//   - GET  /api/v1/spaces/{id}/messages?since= 增量读（游标只读不唤醒，N01）
//
// 鉴权分层（W04-02 豁免⑯收窄兑现）：写面（创建/控制/派发/发言）authSpaceSubject
// ——local_token=主人（恒通）；prime_token=共享编排主体「prime」（经 W02-01
// mcpaccess 门：显式启用才放行，403 mcp_disabled 人话指路）。逐动作授权在 collab
// 模型层（driver/orchestrate 形态；主人专属动作 set_budget/policy/finalize/archive
// 对编排主体 403——豁免收窄后仍人工专属，MCP 工具面不受理）。读面（列表/详情/
// 派发查询/消息读）authRead（持任一本机凭证即可，与 agent 会话查询面同界）。
// 主体由服务端鉴权层解析后传入 collab，不采信请求自报（AGENTS 审计纪律）。
//
// 错误投影：collab Error.Code → HTTP（见 writeSpaceErr）；存储未装配 503
// spaces_unavailable（与 sessions 降级同形态，诚实不虚构）。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// spaceReady collab 存储装配守卫（未装配 503 诚实降级）。
func (s *Server) spaceReady(w http.ResponseWriter) bool {
	if s.cfg.Collab == nil {
		writeErr(w, http.StatusServiceUnavailable, "spaces_unavailable",
			"空间协作存储未装配（本机未启用 spaces；升级或装配后可用）")
		return false
	}
	return true
}

// spaceOwnerSubject 本机主人主体（服务端门已验证 local_token；collab 不读
// HTTP，主体由调用方传入——AGENTS 审计主体纪律）。
func spaceOwnerSubject() collab.Subject {
	return collab.Subject{Name: "owner", Class: collab.ClassOwner}
}

// spaceOrchestrateSubject 共享编排主体（prime_token 持有者）。共享凭据只能识别
// 「该授权主体」，不承诺同凭据调用者间隔离（任务书）；principal 名 prime 参与
// 驱动者判定（external 领取后 Leader.Principal）与领取约束匹配。
func spaceOrchestrateSubject() collab.Subject {
	return collab.Subject{Name: "prime", Class: collab.ClassOrchestrate}
}

// authSpaceSubject 空间写面统一门：认证（401）→ setup 拦截 → 编排凭据 mcpaccess
// 门（403 mcp_disabled）→ 主体注入（owner / orchestrate）。逐动作授权由 collab
// 模型层按主体+任期判定（403 forbidden）。
func (s *Server) authSpaceSubject(next func(http.ResponseWriter, *http.Request, collab.Subject)) http.HandlerFunc {
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
		if s.cfg.Setup {
			writeErr(w, http.StatusForbidden, "prime_required", "setup 模式编排接口未激活：请先在「接入中心端」完成接入并配置 agents")
			return
		}
		if level == levelRead { // local_token：主人恒通
			next(w, r, spaceOwnerSubject())
			return
		}
		// prime_token：共享编排凭据，显式启用是唯一放行态（与任务编排门同源）。
		if !s.cfg.McpAccess.Enabled() {
			writeErr(w, http.StatusForbidden, "mcp_disabled",
				"编排授权未启用：请由本机主人在控制台启用 MCP 编排授权后重试（调用权与管家角色已解耦，启用无需钦定管家；本机主人 local_token 不受影响）")
			return
		}
		next(w, r, spaceOrchestrateSubject())
	}
}

// handleSpaceCreate POST /api/v1/spaces：创建空间。request_id 缺省自动生成
// （自动生成不承担重放回执）；显式提供时同键同文 200+dup 返回原空间、异文
// 409 request_conflict（与 hub 提交幂等同心智）。编排主体仅可建 external 待领取
// 空间且完成授权强制 human_only（collab 模型层强制，伪造授权字段 403）。
func (s *Server) handleSpaceCreate(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req collab.CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 CreateRequest JSON: "+err.Error())
		return
	}
	sp, dup, err := s.cfg.Collab.CreateSpace(req, subj)
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"space": sp, "dup": dup})
}

// handleSpaceList GET /api/v1/spaces：全量列表（创建序）。
func (s *Server) handleSpaceList(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	spaces := s.cfg.Collab.Spaces()
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces, "total": len(spaces)})
}

// handleSpaceGet GET /api/v1/spaces/{id}：详情（视图含成员/负责人/预算用量/
// 策略/在途投影）。
func (s *Server) handleSpaceGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	view, ok := s.cfg.Collab.SpaceView(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": view})
}

// handleSpaceControl POST /api/v1/spaces/{id}/control：控制骨架（逐动作授权表，
// collab 模型层判定）。request_id 必填（写操作稳定请求标识，重放返回原回执）；
// expected_version 必填（乐观锁；takeover 另有 expected_term CAS，term 裁决先于
// 版本复核）。恢复保护（W04-02）：存在在途/排队意图时 resume 须携 confirm=true
// 显式确认（无在途直通）——「人工介入后不自动续跑」的放行由人显式给出。
func (s *Server) handleSpaceControl(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req collab.ControlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 ControlRequest JSON: "+err.Error())
		return
	}
	id := r.PathValue("id")
	if req.Action == collab.ActResume && !req.Confirm && s.cfg.Collab.InFlightCount(id) > 0 {
		writeErr(w, http.StatusConflict, collab.CodeExecutionInFlight,
			"空间存在在途/排队执行意图：恢复须待收束，或携 confirm:true 显式确认继续（继续将按现有队列依次派发；预算/负责人/版本变化见空间详情）")
		return
	}
	res, dup, err := s.cfg.Collab.Control(id, req, subj)
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	// 控制动作统一 200（语义回执在体：applied=是否实际变更、dup=操作 ID 重放命中）。
	// resume 实际生效即放行串行调度器继续投递排队意图（W04-02 kick）。
	if req.Action == collab.ActResume && res.Applied && s.cfg.Service != nil {
		s.cfg.Service.NotifySpaceResumed(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// spaceDispatchRequest POST /api/v1/spaces/{id}/dispatches 请求体。
type spaceDispatchRequest struct {
	Member          string `json:"member"`
	RequestID       string `json:"request_id,omitempty"`
	ExpectedVersion int64  `json:"expected_version"`
	Term            uint64 `json:"term,omitempty"` // 驱动者形态携当前任期
	Note            string `json:"note,omitempty"` // 派发说明（成员轮输入）
}

// handleSpaceDispatch POST /api/v1/spaces/{id}/dispatches：指名派发受理（202 异步
// ——意图持久+预算扣减即时成立，串行调度器投递既有会话链，不阻塞等待）。同
// request_id 重放命中回执零重复执行零重复扣减（N02）。执行侧接线归 service
//（SpaceDispatch——派发不经 control：终审 S2）。
func (s *Server) handleSpaceDispatch(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceDispatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {member, expected_version, note?} JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Member) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "member 必填（指名成员派发）")
		return
	}
	if req.ExpectedVersion <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "expected_version 必填（读档乐观锁）")
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "disp-" + protocol.NewTraceID() // 自动生成（不承担重放回执）
	}
	intent, dup, err := s.cfg.Service.SpaceDispatch(service.SpaceDispatchRequest{
		SpaceID:         r.PathValue("id"),
		Member:          req.Member,
		RequestID:       requestID,
		ExpectedVersion: req.ExpectedVersion,
		Term:            req.Term,
		Note:            req.Note,
	}, subj)
	if err != nil {
		writeSpaceDispatchErr(w, err)
		return
	}
	code := http.StatusAccepted
	if dup {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"dispatch": intent, "dup": dup})
}

// handleSpaceDispatchGet GET /api/v1/spaces/{id}/dispatches/{request_id}：意图
// 查询（轮终投影：意图 + 账本轮状态 join——pending/accepted/收敛/取消 + 轮的
// 排队/运行/终态）。
func (s *Server) handleSpaceDispatchGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	it, ok := s.cfg.Collab.IntentByRequest(r.PathValue("id"), r.PathValue("request_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "dispatch_not_found", "意图不存在（request_id 未受理或已滚动出空间）: "+r.PathValue("request_id"))
		return
	}
	body := map[string]any{"dispatch": it}
	if it.TaskID != "" && s.cfg.Ledger != nil {
		if e, ok := s.cfg.Ledger.Get(it.TaskID); ok {
			turn := map[string]any{"task_id": it.TaskID, "state": e.State}
			if e.Result != nil {
				turn["status"] = e.Result.Status
				turn["error"] = e.Result.Error
				turn["final_answer"] = e.Result.FinalAnswer
			}
			body["turn"] = turn
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// spaceMessageRequest POST /api/v1/spaces/{id}/messages 请求体。
type spaceMessageRequest struct {
	Text            string `json:"text"`
	Member          string `json:"member,omitempty"` // 发言归属成员（空=主人发言）
	RequestID       string `json:"request_id,omitempty"`
	ExpectedVersion int64  `json:"expected_version"`
}

// handleSpaceMessagePost POST /api/v1/spaces/{id}/messages：发言（普通发言零唤醒
// 纪律 N01——只落消息账本与 message_count 预算，绝不触发任何执行；空间查询/发言
// 不代表获得调度权）。发言主体（RFC 定案）：指名成员=以该成员身份落时间线（成员
// 须在空间内，owner/orchestrate 均可代转）；不指名=主人发言（仅 owner——编排主体
// 不能冒充主人，须指名成员）。
func (s *Server) handleSpaceMessagePost(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {text, member?, expected_version} JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "text 必填（消息正文）")
		return
	}
	if req.ExpectedVersion <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "expected_version 必填（读档乐观锁）")
		return
	}
	if req.Member == "" && subj.Class != collab.ClassOwner {
		writeErr(w, http.StatusForbidden, collab.CodeForbidden,
			"编排主体不能冒充主人发言：发言归属须指名空间成员（member 字段）；调度权另经 space dispatch")
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "msg-" + protocol.NewTraceID()
	}
	rec, dup, err := s.cfg.Collab.RecordMessage(r.PathValue("id"), collab.MessageRequest{
		RequestID:       requestID,
		ExpectedVersion: req.ExpectedVersion,
		Text:            req.Text,
		Member:          req.Member,
	}, subj)
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"record": rec, "dup": dup})
}

// handleSpaceMessagesGet GET /api/v1/spaces/{id}/messages?since=N：增量读（游标
// 只读不唤醒——N01；滚动窗口外经 pruned_before_seq 校正）。
func (s *Server) handleSpaceMessagesGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	if _, ok := s.cfg.Collab.SpaceView(r.PathValue("id")); !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+r.PathValue("id"))
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, http.StatusBadRequest, "invalid_request", "since 须为非负整数（已见最大 seq）")
			return
		}
		since = v
	}
	msgs, pruned := s.cfg.Collab.SpaceMessages(r.PathValue("id"), since)
	if msgs == nil {
		msgs = []collab.SpaceMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "pruned_before_seq": pruned})
}

// writeSpaceDispatchErr 派发面错误投影：collab 错误与 RemoteError（会话面派生
// 失败）两源。
func writeSpaceDispatchErr(w http.ResponseWriter, err error) {
	var re *service.RemoteError
	if errors.As(err, &re) {
		code := http.StatusInternalServerError
		switch re.Code {
		case "invalid", "input_invalid":
			code = http.StatusBadRequest
		case collab.CodeNotFound, "member_not_found":
			code = http.StatusNotFound
		case collab.CodeForbidden, "mcp_disabled":
			code = http.StatusForbidden
		case collab.CodeIllegalTransition, collab.CodeVersionConflict, collab.CodeRequestConflict,
			collab.CodeMemberInFlight, collab.CodeExecutionInFlight, collab.CodeBudgetExhausted,
			collab.CodeSessionBoundElsewhere, collab.CodeSpaceDispatchRequired, collab.CodeNoMembers,
			"turn_in_flight", "session_closed":
			code = http.StatusConflict
		case "spaces_unavailable":
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, re.Code, re.Msg)
		return
	}
	writeSpaceErr(w, err)
}

// writeSpaceErr collab 错误 → HTTP 投影（RFC §一错误码表；未知码归 500）。
func writeSpaceErr(w http.ResponseWriter, err error) {
	var ce *collab.Error
	if !errors.As(err, &ce) {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	code := http.StatusInternalServerError
	switch ce.Code {
	case collab.CodeInvalid:
		code = http.StatusBadRequest
	case collab.CodeNotFound:
		code = http.StatusNotFound
	case collab.CodeForbidden:
		code = http.StatusForbidden
	case collab.CodeIllegalTransition, collab.CodeVersionConflict, collab.CodeRequestConflict,
		collab.CodeLeaseExpired, collab.CodeTermConflict, collab.CodeMemberInFlight,
		collab.CodeExecutionInFlight, collab.CodeBudgetExhausted,
		collab.CodeSessionBoundElsewhere, collab.CodeSpaceDispatchRequired,
		collab.CodeMemberNotFound, collab.CodeNoMembers:
		code = http.StatusConflict
	case collab.CodeFinishPolicyUnavailable:
		code = http.StatusServiceUnavailable
	}
	writeErr(w, code, ce.Code, stripCollabPrefix(err.Error()))
}

// stripCollabPrefix 错误文案去包前缀（collab: 已由错误码承担；人话正文直接透出）。
func stripCollabPrefix(msg string) string {
	for _, p := range []string{"collab: "} {
		if len(msg) > len(p) && msg[:len(p)] == p {
			return msg[len(p):]
		}
	}
	return msg
}
