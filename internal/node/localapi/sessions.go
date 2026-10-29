// sessions.go 本机会话端点（T2 持久会话，SPEC 六.五「会话与轮次」+ 六.六豁免⑩）：
// 列表/详情/新建/续接/关闭，仅 local_token 可调（本机主人管理面——会话绑定本机
// 工作区与原生线程，操作语义 = 主人开线程/续线程/关线程；prime_token 是编排
// 凭证不放行，同 notify 豁免口径）。数据面走 service 本地会话入口（sessionturn.go）：
// 新建/续接为本地任务链（账本留痕 + sessionx 内核执行），关闭/查询直调内核。
// 供 T3 本机控制台使用；MCP 不映射（agent 面合法能力已由 hub 会话工具组覆盖，
// 本机原生线程管理归主人，六.六豁免⑩逐端点判定）。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// localSessionCreateRequest POST /api/v1/local/sessions 请求体。
type localSessionCreateRequest struct {
	// Agent 目标 agent 名（须声明 session_adapter）。
	Agent string `json:"agent"`
	// Cwd 会话工作目录（本机绝对路径）。
	Cwd string `json:"cwd"`
	// Prompt 首轮输入（与续轮端点同形；受理异步）。
	Prompt string `json:"prompt"`
	// PermissionMode 权限模式断言值（可选，同 hub 口径）：显式传入须与本机配置
	// 一致，不符 409；全权档位 400——本机主人可改配置提权，但平台面不注入。
	PermissionMode string `json:"permission_mode,omitempty"`
}

// handleLocalSessionCreate POST /api/v1/local/sessions：新建会话并提交首轮
// （异步——202 回执带 session_id/turn_seq=1/task_id，轮终经详情/账本查询）。
func (s *Server) handleLocalSessionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	var req localSessionCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {agent, cwd} JSON: "+err.Error())
		return
	}
	if req.Agent == "" || req.Cwd == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "agent 与 cwd 均必填")
		return
	}
	if m := req.PermissionMode; m != "" {
		if m == "yolo" || m == "danger-full-access" {
			writeErr(w, http.StatusBadRequest, "invalid_request",
				"permission_mode "+m+" 为全权档位，平台拒绝注入（提权须在 node.yaml 显式改配并重启）")
			return
		}
		if card := s.sessionCard(req.Agent); card != nil && card.PermissionMode != m {
			writeErr(w, http.StatusConflict, "binding_mismatch",
				"权限断言 "+m+" ≠ 本机配置 "+card.PermissionMode+"（断言仅供核对，非换绑通道）")
			return
		}
	}
	ack, err := s.cfg.Service.SubmitSessionTurnLocal(service.LocalSessionTurnRequest{
		NodeID: s.cfg.NodeID, Agent: req.Agent, Cwd: req.Cwd, Prompt: req.Prompt,
	})
	if err != nil {
		writeLocalSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"turn": ack})
}

// handleLocalSessionTurns POST /api/v1/local/sessions/{id}/turns：续接一轮
// （异步受理；序号由本机按内核账本分配，与 hub 轮序号同源）。请求体 {prompt}。
func (s *Server) handleLocalSessionTurns(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {prompt} JSON: "+err.Error())
		return
	}
	ack, err := s.cfg.Service.SubmitSessionTurnLocal(service.LocalSessionTurnRequest{
		NodeID: s.cfg.NodeID, SessionID: r.PathValue("id"), Prompt: req.Prompt,
	})
	if err != nil {
		writeLocalSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"turn": ack})
}

// handleLocalSessionList GET /api/v1/local/sessions：本机会话列表（内核快照，
// 创建时刻升序）。
func (s *Server) handleLocalSessionList(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	sessions := s.cfg.Service.SessionsLocal()
	if sessions == nil {
		sessions = []protocol.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleLocalSessionGet GET /api/v1/local/sessions/{id}：本机会话详情（内核快照
// 含轮次留痕全量）。
func (s *Server) handleLocalSessionGet(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	sess, ok := s.cfg.Service.SessionLocal(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "session_not_found", "会话不存在（本机内核无映射）")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

// handleLocalSessionClose POST /api/v1/local/sessions/{id}/close：关闭会话
// （同步直调内核；幂等——已关闭/无映射即达成 200）。
func (s *Server) handleLocalSessionClose(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w) {
		return
	}
	sess, err := s.cfg.Service.CloseSessionLocal(r.PathValue("id"))
	if err != nil {
		writeLocalSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

// sessionsReady 会话面装配守卫：Service 与内核齐备才放行，否则 503 诚实降级
// （旧装配形态/无会话能力 node，不静默空转）。
func (s *Server) sessionsReady(w http.ResponseWriter) bool {
	if s.cfg.Service == nil || !s.cfg.Service.SessionsEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "session_unsupported",
			"本机未装配会话内核（node 版本过旧或全部能力未声明 session_adapter）")
		return false
	}
	return true
}

// sessionCard 目标本机能力的运行时卡片（权限断言核对源；不在配置返回 nil）。
func (s *Server) sessionCard(agent string) *protocol.AgentCard {
	for _, card := range service.CardsFromProfiles(s.effectiveProfiles()) {
		if card.Name == agent {
			c := card
			return &c
		}
	}
	return nil
}

// writeLocalSessionErr 本地会话校验错误 → HTTP（RemoteError 语义映射，同本地
// 提交错误口径：invalid 400 / agent与会话不存在 404 / closed 与 in_flight 409 /
// unsupported 503 / 其余 500）。
func writeLocalSessionErr(w http.ResponseWriter, err error) {
	var re *service.RemoteError
	if errors.As(err, &re) {
		code := http.StatusInternalServerError
		switch re.Code {
		case "invalid", sessionInputCode:
			code = http.StatusBadRequest
		case protocol.CodeAgentNotFound, "session_not_found":
			code = http.StatusNotFound
		case "session_closed", "turn_in_flight", "binding_mismatch":
			code = http.StatusConflict
		case "session_unsupported":
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, re.Code, re.Msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", err.Error())
}

// sessionInputCode 本地会话输入错误码（service sessionturn.go 的 sessionErrInput
// 字面值；localapi 不引用 service 未导出常量，此处对齐并附测试锁定）。
const sessionInputCode = "input_invalid"
