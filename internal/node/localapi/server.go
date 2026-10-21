// Package localapi 实现 node 本地回环 API（SPEC 六：默认 127.0.0.1:8445，仅本机
// 进程可用）：POST /api/v1/tasks 与 GET /api/v1/tasks/{id} 经 WSS 会话转发为
// task_submit/task_query 帧（语义与 hub 管理面一致，跨机互派闭环：本机进程 →
// 本机 node → hub → 目标 node）；GET /api/v1/agents 返回本机能力目录并尽力代理
// hub 全网目录；/api/v1/local/* 为本地控制台管理面（Loop 7，见 local.go）；
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
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	nodeweb "github.com/mtl802/swarmmind/internal/node/web"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// maxBodyBytes 请求体上限（与 hub 管理面同口径；提交体另有完整信封实测校验）。
const maxBodyBytes = 4 << 20

// hubProxyTimeout 全网目录代理的登录/取回时限（调度节拍，非协议常量）。
const hubProxyTimeout = 10 * time.Second

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
	// HubAPIURL hub 管理面地址（如 http://127.0.0.1:8444）：GET /api/v1/agents 的
	// 全网目录代理与 GET /api/v1/local/status 的信箱余量代理源；空 = 不代理。
	// 已知限制（V0）：hub 管理面绑其主机回环，双机部署下需经公网反代可达
	//（SPEC 六.五：node 配置指向带鉴权的反代地址）。
	HubAPIURL string
	// HubAPIToken hub 管理令牌（代理登录用；随 HubAPIURL 一起配置）。
	HubAPIToken string
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

	hubMu     sync.Mutex
	hubClient *http.Client // 携会话 cookie；登录失败/过期时重建
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
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req protocol.TaskSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 TaskSubmitRequest JSON: "+err.Error())
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
// AgentState effective 视图生成，与注册/开关同源）+ hub 全网目录代理（best
// effort；不可达时以 error 字段如实呈现，不影响本机目录）。
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	online := s.online()
	local := make([]localAgentEntry, 0)
	for _, card := range service.CardsFromProfiles(s.effectiveProfiles()) {
		local = append(local, localAgentEntry{Card: card, NodeID: s.cfg.NodeID, Online: online})
	}
	body := map[string]any{
		"node_id": s.cfg.NodeID,
		"local":   local,
	}
	if s.cfg.HubAPIURL != "" {
		network, err := s.fetchHubAgents()
		if err != nil {
			body["network"] = map[string]any{"error": err.Error()}
		} else {
			body["network"] = network
		}
	} else {
		body["network"] = map[string]any{"error": "未配置 hub_api_url，全网目录代理不可用"}
	}
	writeJSON(w, http.StatusOK, body)
}

// fetchHubAgents 代理 hub 全网目录：惰性登录（会话 cookie 复用），401 时重登一次。
func (s *Server) fetchHubAgents() (any, error) {
	client, err := s.hubSession()
	if err != nil {
		return nil, err
	}
	body, status, err := s.hubGetPath(client, "/api/v1/agents")
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized { // 会话过期：重登一次
		if client, err = s.hubLogin(true); err != nil {
			return nil, err
		}
		if body, status, err = s.hubGetPath(client, "/api/v1/agents"); err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("hub 目录查询失败（HTTP %d）", status)
	}
	var out any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("hub 目录解析失败: %w", err)
	}
	return out, nil
}

// hubGetPath GET hub 管理面指定路径（目录代理与信箱余量代理共用）。
func (s *Server) hubGetPath(client *http.Client, path string) ([]byte, int, error) {
	resp, err := client.Get(strings.TrimSuffix(s.cfg.HubAPIURL, "/") + path)
	if err != nil {
		return nil, 0, fmt.Errorf("hub 目录不可达: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("hub 目录读取失败: %w", err)
	}
	return b, resp.StatusCode, nil
}

// hubSession 取（或建）带会话的 hub 客户端；未登录先登录。
func (s *Server) hubSession() (*http.Client, error) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.hubClient != nil {
		return s.hubClient, nil
	}
	return s.hubLoginLocked()
}

// hubLogin 重建会话（force=true 时丢弃旧会话）。
func (s *Server) hubLogin(force bool) (*http.Client, error) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if force {
		s.hubClient = nil
	}
	if s.hubClient != nil {
		return s.hubClient, nil
	}
	return s.hubLoginLocked()
}

// hubLoginLocked 登录并缓存客户端（调用方持 hubMu）。
func (s *Server) hubLoginLocked() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: hubProxyTimeout, Jar: jar}
	loginURL := strings.TrimSuffix(s.cfg.HubAPIURL, "/") + "/api/v1/login"
	body := map[string]string{"token": s.cfg.HubAPIToken}
	b, _ := json.Marshal(body)
	resp, err := client.Post(loginURL, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return nil, fmt.Errorf("hub 登录不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub 登录失败（HTTP %d）：核对 hub_api_token", resp.StatusCode)
	}
	s.hubClient = client
	return client, nil
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
