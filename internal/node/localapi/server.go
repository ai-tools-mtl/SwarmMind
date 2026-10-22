// Package localapi 实现 node 本地回环 API（SPEC 六：默认 127.0.0.1:8445，仅本机
// 进程可用）：POST /api/v1/tasks 与 GET /api/v1/tasks/{id} 经 WSS 会话转发为
// task_submit/task_query 帧（目标收敛为本机：提交仅限本机 agent、查询仅限本机
// 任务，跨机协作经 hub 发起）；GET /api/v1/agents 只返回本机能力目录（node 无
// 全网视图）；/api/v1/local/* 为本地控制台管理面（Loop 7，见 local.go）；
// GET / 挂本地控制台前端（internal/node/web，embed）。
//
// 鉴权：本机管理令牌（node 配置 local_token，Authorization: Bearer）；监听地址
// 强制回环（代码校验，非文档约束）。
package localapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	nodeweb "github.com/mtl802/swarmmind/internal/node/web"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// maxBodyBytes 请求体上限（与 hub 管理面同口径；提交体另有完整信封实测校验）。
const maxBodyBytes = 4 << 20

// Config Server 构造参数。
type Config struct {
	// NodeID 本机身份（目录展示用）。
	NodeID string
	// Service node 任务服务（提交/查询的数据面，经 WSS 会话帧转发）。
	Service *service.Service
	// Agents 本机 agent 运行时状态（Loop 7：exposed 开关真相源，YAML 基线 +
	// agents-state.json 覆盖）。目录、开关与注册能力表同源于此。
	Agents *AgentState
	// Ledger node 执行账本（今日任务数与账本三态计数的数据源，可为 nil）。
	Ledger *ledger.Ledger
	// TaskLog 本机提交任务日志（我的任务列表数据源，可为 nil）。
	TaskLog *TaskLog
	// LocalToken 本机管理令牌（Authorization: Bearer；空 = 拒绝一切请求，装配方
	// 在为空时生成并回填，见 cmd/node）。
	LocalToken string
	// CardsReporter 能力表上报钩子（Loop 7：开关变更即 card_update 上报 hub；
	// 装配方接 service.SendCardUpdate）。nil = 开关仅本机生效。
	CardsReporter func(cards []protocol.AgentCard) error
	// Online 与 hub 的连接状态（nil = 未知，按 false 展示）。
	Online func() bool
	// Epoch 当前会话代次（nil = 0）。
	Epoch func() int64
	// LastRegisteredAt 最近一次注册成功时刻（nil = 从未）。
	LastRegisteredAt func() (time.Time, bool)
}

// Server node 本地 API。
type Server struct {
	cfg Config
}

// New 构造本地 API 服务。
func New(cfg Config) *Server { return &Server{cfg: cfg} }

// effectiveProfiles 本机 effective ExecProfile 视图（Agents 未装配时空表）。
func (s *Server) effectiveProfiles() []protocol.ExecProfile {
	if s.cfg.Agents == nil {
		return nil
	}
	return s.cfg.Agents.Effective()
}

// ValidateListen 校验监听地址为回环（127.0.0.1/::1）——node 不监听任何公网端口
// （SPEC 二，代码强制）。
func ValidateListen(listenAddr string) error {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("localapi: 监听地址非法 %q: %w", listenAddr, err)
	}
	if host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("localapi: 本地 API 只允许绑定回环地址（127.0.0.1/::1），got %q", listenAddr)
}

// Handler 返回路由（装配方挂到 http.Server）。
//
// 本地控制台静态资源（Loop 7）：GET / 控制台入口 + /app.js /style.css，免令牌
// （令牌输入页必须可达，由前端持令牌调 /api/v1/*）；与 hub 控制台同约定。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tasks", s.auth(s.handleSubmit))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.auth(s.handleQuery))
	mux.HandleFunc("GET /api/v1/agents", s.auth(s.handleAgents))
	mux.HandleFunc("GET /api/v1/local/agents", s.auth(s.handleLocalAgents))
	mux.HandleFunc("PATCH /api/v1/local/agents/{name}/exposed", s.auth(s.handleSetExposed))
	mux.HandleFunc("GET /api/v1/local/status", s.auth(s.handleLocalStatus))
	mux.HandleFunc("GET /api/v1/local/tasks", s.auth(s.handleLocalTasks))
	console := nodeweb.Handler()
	mux.Handle("GET /{$}", console)
	mux.Handle("GET /app.js", console)
	mux.Handle("GET /style.css", console)
	return mux
}

// auth 本机管理令牌校验（Authorization: Bearer <local_token>，恒时比对）。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.LocalToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "本机管理令牌缺失或错误（Authorization: Bearer <local_token>）")
			return
		}
		next(w, r)
	}
}

// handleSubmit POST /api/v1/tasks：转 task_submit 帧 → submit_ack/err 帧。
// 应答码与 hub 管理面对齐（202/200 dup/409/404/400/503）；hub 不可达 503。
// 边界锁（方案 v2 D8）：node 本地 API 只能派给本机——target_node 非本机
// node_id 直接 400 拒绝，不发帧、不留提交日志、不静默改写目标。
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req protocol.TaskSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 TaskSubmitRequest JSON: "+err.Error())
		return
	}
	if req.TargetNode != s.cfg.NodeID {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("本机 API 仅可派单给本机 node %s（收到 target_node=%q；跨机派单经 hub 发起）", s.cfg.NodeID, req.TargetNode))
		return
	}
	ctx := r.Context()
	ack, err := s.cfg.Service.SubmitLocal(ctx, req)
	if err != nil {
		writeRemoteErr(w, err)
		return
	}
	code := http.StatusAccepted
	body := map[string]any{"task_id": ack.TaskID, "trace_id": ack.TraceID}
	if ack.Dup {
		code = http.StatusOK
		body["dup"] = true
	}
	// 本机留痕（Loop 7 我的任务）：submit_ack 成功即登记；同 task_id 幂等，
	// 幂等重试不重复留痕
	if s.cfg.TaskLog != nil {
		_ = s.cfg.TaskLog.Record(TaskLogEntry{
			TaskID: ack.TaskID, TraceID: ack.TraceID,
			TargetNode: req.TargetNode, TargetAgent: req.TargetAgent,
			SubmittedBy: req.SubmittedBy, IdemKey: req.IdemKey,
			CreatedAt: protocol.NowUTC(),
		})
	}
	writeJSON(w, code, body)
}

// handleQuery GET /api/v1/tasks/{id}：转 task_query 帧 → task_status 帧。
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	t, err := s.cfg.Service.QueryLocal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRemoteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// localAgentEntry 本机目录单条。
type localAgentEntry struct {
	Card   protocol.AgentCard `json:"card"`
	NodeID string             `json:"node_id"`
	Online bool               `json:"online"`
}

// handleAgents GET /api/v1/agents：本机能力目录（本机意愿，含未暴露；由
// AgentState effective 视图生成，与注册/开关同源）。node 无全网视图——不代理
// hub 目录（方案 v2：封侧门，全网目录唯一出口在 hub 侧）。
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	online := s.online()
	local := make([]localAgentEntry, 0)
	for _, card := range service.CardsFromProfiles(s.effectiveProfiles()) {
		local = append(local, localAgentEntry{Card: card, NodeID: s.cfg.NodeID, Online: online})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id": s.cfg.NodeID,
		"local":   local,
	})
}

// writeRemoteErr 数据面错误 → HTTP 状态（与 hub 管理面错误码对齐）。
func writeRemoteErr(w http.ResponseWriter, err error) {
	var re *service.RemoteError
	switch {
	case errors.As(err, &re):
		writeErr(w, remoteStatus(re.Code), re.Code, re.Msg)
	default:
		writeErr(w, http.StatusServiceUnavailable, protocol.CodeHubUnreachable, err.Error())
	}
}

// remoteStatus hub 校验链错误码 → HTTP 状态（SPEC 六）。
func remoteStatus(code string) int {
	switch code {
	case protocol.CodeAgentNotExposed:
		return http.StatusNotFound
	case "conflict":
		return http.StatusConflict
	case protocol.CodeMailboxFull, protocol.CodeStorageFull:
		return http.StatusServiceUnavailable
	case protocol.CodeFrameTooLarge, "invalid":
		return http.StatusBadRequest
	case protocol.CodeTaskNotFound, protocol.CodeArtifactNotFound:
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// writeJSON 统一 JSON 应答。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 统一错误体。
func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"error": errCode, "message": msg})
}
