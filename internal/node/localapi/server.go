// Package localapi 实现 node 本地回环 API（SPEC 六：默认 127.0.0.1:8445，仅本机
// 进程可用）。方案 3.2 双路径（2026-10-22）后提交/查询均为本地数据面：
//   - POST /api/v1/tasks 本地直执行——本地构造任务、账本入账（origin=local）、
//     runner 执行、结果回写本地账本，不经 hub 帧往返（组不组网行为一致）；
//   - GET /api/v1/tasks/{id} 从本地账本出视图（ledger.TaskView）——离线可查；
//   - GET /api/v1/agents 只返回本机能力目录（node 无全网视图）；
//   - /api/v1/local/* 为本地控制台管理面（Loop 7，见 local.go）+ 总理凭证
//     （prime.go）+ setup 向导（setup.go，仅 setup 模式装配）；
//   - GET / 挂本地控制台前端（internal/node/web，embed）。
//
// 鉴权分级（方案 3.2.1 唯一总理）：local_token 只读（agents/status/tasks 类）；
// 编排操作（submit，后续 review/cancel）必须 prime_token——local_token 调编排
// → 403 可测（环防护）。监听地址强制回环（代码校验，非文档约束）。
package localapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

// 形态取值（Config.Mode；控制台按此分形态呈现）。
const (
	ModeSetup      = "setup"      // node.yaml 不存在：仅起控制台 + 接入向导
	ModeStandalone = "standalone" // hub_url 为空：全功能本机自治
	ModeNetworked  = "networked"  // hub_url 非空：组网在线
)

// Config Server 构造参数。
type Config struct {
	// NodeID 本机身份（目录展示用）。
	NodeID string
	// Mode 运行形态（setup|standalone|networked；状态展示用，空 = networked）。
	Mode string
	// Service node 任务服务（本地直执行的数据面）。
	Service *service.Service
	// Agents 本机 agent 运行时状态（Loop 7：exposed 开关真相源，YAML 基线 +
	// agents-state.json 覆盖）。目录、开关与注册能力表同源于此。
	Agents *AgentState
	// Ledger node 执行账本（本地任务视图、今日任务数与三态计数的数据源；本地
	// 查询的真相源，nil 时查询端点 503）。
	Ledger *ledger.Ledger
	// TaskLog 本机提交任务日志（我的任务列表数据源，可为 nil）。
	TaskLog *TaskLog
	// LocalToken 本机管理令牌（只读 + 控制台管理面；Authorization: Bearer；空 =
	// 拒绝一切请求，装配方在为空时生成并回填，见 cmd/node）。
	LocalToken string
	// PrimeToken 总理编排凭证（submit 等编排操作专属；初始值来自 data_dir/
	// prime_token，轮换经 SetPrimeToken 即时更新）。空 = 编排接口恒 403。
	PrimeToken string
	// PrimeAgent node.yaml prime_agent 声明（唯一总理，仅展示与激活开关；空 =
	// 尚未钦定，编排接口保持 403，矩阵 #15）。
	PrimeAgent string
	// Setup setup 模式：编排接口一律 403（无 agents 可编排，先完成接入与能力配置）。
	Setup bool
	// RotatePrime 总理凭证轮换回调（重生成 + 原子写盘，返回新凭证；装配方接线，
	// 见 cmd/node）。nil = 轮换端点 503。
	RotatePrime func() (string, error)
	// SetupOps setup 向导装配方（写盘路径与落盘回调；nil = setup 端点 503）。
	SetupOps *SetupOps
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

	// primeTok 当前总理凭证（轮换即时生效；muPrime 串行读写）。
	muPrime  sync.Mutex
	primeTok string
}

// New 构造本地 API 服务。
func New(cfg Config) *Server {
	return &Server{cfg: cfg, primeTok: cfg.PrimeToken}
}

// SetPrimeToken 总理凭证轮换后即时重载（旧凭证立即失效——下一请求按新凭证比对）。
func (s *Server) SetPrimeToken(tok string) {
	s.muPrime.Lock()
	s.primeTok = tok
	s.muPrime.Unlock()
}

// primeToken 当前总理凭证快照。
func (s *Server) primeToken() string {
	s.muPrime.Lock()
	defer s.muPrime.Unlock()
	return s.primeTok
}

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

// 鉴权级别：levelNone 未认证；levelRead 只读（local_token 或 prime_token）；
// levelPrime 编排（仅 prime_token）。
type tokenLevel int

const (
	levelNone tokenLevel = iota
	levelRead
	levelPrime
)

// classify 恒时比对令牌并定级。
func (s *Server) classify(token string) tokenLevel {
	if token == "" {
		return levelNone
	}
	if s.cfg.LocalToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.LocalToken)) == 1 {
		return levelRead
	}
	if s.primeToken() != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.primeToken())) == 1 {
		return levelPrime
	}
	return levelNone
}

// bearer 摘取 Authorization: Bearer 值。
func bearer(r *http.Request) (string, bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// Handler 返回路由（装配方挂到 http.Server）。
//
// 本地控制台静态资源（Loop 7）：GET / 控制台入口 + /app.js /style.css，免令牌
// （令牌输入页必须可达，由前端持令牌调 /api/v1/*）；与 hub 控制台同约定。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tasks", s.authPrime(s.handleSubmit))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.authRead(s.handleQuery))
	mux.HandleFunc("GET /api/v1/agents", s.authRead(s.handleAgents))
	mux.HandleFunc("GET /api/v1/local/agents", s.authRead(s.handleLocalAgents))
	mux.HandleFunc("PATCH /api/v1/local/agents/{name}/exposed", s.authRead(s.handleSetExposed))
	mux.HandleFunc("GET /api/v1/local/status", s.authRead(s.handleLocalStatus))
	mux.HandleFunc("GET /api/v1/local/tasks", s.authRead(s.handleLocalTasks))
	mux.HandleFunc("POST /api/v1/local/prime/rotate", s.authLocal(s.handlePrimeRotate))
	mux.HandleFunc("POST /api/v1/local/setup/parse", s.authLocal(s.handleSetupParse))
	mux.HandleFunc("POST /api/v1/local/setup/apply", s.authLocal(s.handleSetupApply))
	mux.HandleFunc("POST /api/v1/local/setup/disconnect", s.authLocal(s.handleSetupDisconnect))
	console := nodeweb.Handler()
	mux.Handle("GET /{$}", console)
	mux.Handle("GET /app.js", console)
	mux.Handle("GET /style.css", console)
	return mux
}

// authRead 只读门：local_token 或 prime_token 皆可（查目录/状态/任务/取产物视图）。
func (s *Server) authRead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok || s.classify(token) == levelNone {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "本机管理令牌缺失或错误（Authorization: Bearer <local_token>）")
			return
		}
		next(w, r)
	}
}

// authLocal 本机主人门：仅 local_token（控制台管理面：总理轮换、setup 写盘）。
// prime_token 不放行——轮换与写盘是本机主人操作，不随编排凭证下发。
func (s *Server) authLocal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.LocalToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "本机管理令牌缺失或错误（Authorization: Bearer <local_token>）")
			return
		}
		next(w, r)
	}
}

// authPrime 编排门（方案 3.2.1 唯一总理）：先认证（401）后鉴权（403），拒绝
// 理由按优先级人话给出——setup 模式未接入 > 未钦定总理 > 凭证级别不足。
func (s *Server) authPrime(next http.HandlerFunc) http.HandlerFunc {
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
		if s.cfg.PrimeAgent == "" {
			writeErr(w, http.StatusForbidden, "prime_required", "尚未钦定总理（node.yaml 未声明 prime_agent），编排接口未激活；本机主人可在 node.yaml 声明 prime_agent 并重启")
			return
		}
		if s.primeToken() == "" {
			writeErr(w, http.StatusForbidden, "prime_required", "总理凭证未配置（data_dir/prime_token 缺失且生成失败），编排接口未激活")
			return
		}
		if level != levelPrime {
			writeErr(w, http.StatusForbidden, "prime_required",
				fmt.Sprintf("编排操作需总理凭证 prime_token（本机总理：%s；local_token 仅只读，用 local_token 调编排接口 → 403）", s.cfg.PrimeAgent))
			return
		}
		next(w, r)
	}
}

// handleSubmit POST /api/v1/tasks：本地直执行（方案 3.2 双路径）——本地构造
// 任务、账本入账（origin=local）、runner 执行、结果回写本地账本。应答码与 hub
// 管理面对齐（202/200 dup/409/404/400）；无 503 hub_unreachable——本地路径
// 不依赖 hub 在线（离线自治）。
// 边界锁（裁剪方案 D8）：node 本地 API 只能派给本机——target_node 非本机
// node_id 直接 400 拒绝，不入账、不留提交日志、不静默改写目标。
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
	if s.cfg.Service == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "任务服务未装配（装配方须传入 Service）")
		return
	}
	ack, err := s.cfg.Service.SubmitLocalDirect(req)
	if err != nil {
		writeSubmitErr(w, err)
		return
	}
	code := http.StatusAccepted
	body := map[string]any{"task_id": ack.TaskID, "trace_id": ack.TraceID}
	if ack.Dup {
		code = http.StatusOK
		body["dup"] = true
	}
	// 本机留痕（Loop 7 我的任务）：入账成功即登记；同 task_id 幂等，幂等重试不重复留痕
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

// handleQuery GET /api/v1/tasks/{id}：本地账本视图直出（方案 3.2——本机任务
// 真相源是本机账本，不再经 task_query 帧；离线可查）。hub 下发任务在 offer
// 入账后同样在此可查（node 侧视角：accepted→acknowledged、started→running）。
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Ledger == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "账本未装配（装配方须传入 Ledger）")
		return
	}
	e, ok := s.cfg.Ledger.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, "任务不存在或已过保留期（终态保留 24h，离线任务 7 天）")
		return
	}
	writeJSON(w, http.StatusOK, ledger.TaskView(e))
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

// writeSubmitErr 提交校验链错误 → HTTP 状态（与 hub 管理面错误码对齐；
// 本地路径无 hub_unreachable——入账/磁盘类内部错误 → 500）。
func writeSubmitErr(w http.ResponseWriter, err error) {
	var re *service.RemoteError
	if errors.As(err, &re) {
		writeErr(w, remoteStatus(re.Code), re.Code, re.Msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", err.Error())
}

// remoteStatus 校验链错误码 → HTTP 状态（SPEC 六）。
func remoteStatus(code string) int {
	switch code {
	case protocol.CodeAgentNotExposed, protocol.CodeAgentNotFound:
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
