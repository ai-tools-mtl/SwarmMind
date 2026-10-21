// Package api 实现 hub HTTP 管理面（SPEC 六/六.五）：登录鉴权 + 五端点 +
// 核对记录 + 控制批次 + 发现目录。
//
// 安全模型（Loop 4）：绑定 127.0.0.1（装配方经 session.ValidateAPIListen 强制）
// + 管理令牌登录（hub 配置 admin_token，POST /api/v1/login 发会话 cookie，其余
// 端点校验会话）。V0 会话为内存表 + 绝对过期，重启即失效（重新登录即可）。
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/web"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 会话 cookie 与时效（V0 简单实现：内存会话表，绝对过期）。
const (
	sessionCookie = "swarmmind_session"
	sessionTTL    = 24 * time.Hour
	// maxBodyBytes 请求体上限：提交体经完整信封实测另校验 1MiB，这里仅防滥用。
	maxBodyBytes = 4 << 20
	// artifactFetchTimeout 产物中继取回超时（SPEC 六：30s → 504）。
	artifactFetchTimeout = 30 * time.Second
)

// Config Server 构造参数。
type Config struct {
	// Core hub 任务服务（五端点的数据面）。
	Core *core.Core
	// AdminToken 管理令牌（登录凭据）；为空时拒绝一切登录（管理面只读不可用）。
	AdminToken string
	// DataDir 数据目录（子令牌 api_tokens.json 的存放位置；空则禁用 PAT 功能）。
	DataDir string
}

// Server 管理面 HTTP 服务。
type Server struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]time.Time // 会话 ID → 过期时刻（UTC）
	tokens   *tokenStore          // 子令牌（PAT）存储；DataDir 为空时为 nil（禁用）
	accounts *accountStore        // 管理员账户（密码登录）；DataDir 为空时为 nil
}

// New 构造管理面服务。
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, sessions: map[string]time.Time{}}
	if cfg.DataDir != "" {
		s.tokens = newTokenStore(cfg.DataDir)
		s.accounts = newAccountStore(cfg.DataDir)
	}
	return s
}

// Handler 返回路由（装配方挂到 http.Server）。
//
// 静态资源（Loop 6 控制台）：GET / 控制台入口 + /app.js /style.css，免鉴权
// （登录页必须可达，登录后由前端 hash 路由接管）；/api/v1/* 数据接口除 login 外
// 一律要求会话（前端收 401 自动跳登录页）。两侧路径不冲突。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/login-password", s.handleLoginPassword)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleAccountSetup)
	mux.HandleFunc("GET /api/v1/auth/account", s.handleAccountStatus) // 免鉴权：登录页需据此选表单（未登录仅返回 initialized 布尔）
	mux.HandleFunc("POST /api/v1/auth/account/update", s.auth(s.handleAccountUpdate))
	mux.HandleFunc("POST /api/v1/auth/tokens", s.auth(s.handleTokenCreate))
	mux.HandleFunc("GET /api/v1/auth/tokens", s.auth(s.handleTokenList))
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", s.auth(s.handleTokenRevoke))
	mux.HandleFunc("POST /api/v1/tasks", s.auth(s.handleSubmit))
	mux.HandleFunc("GET /api/v1/tasks", s.auth(s.handleListTasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.auth(s.handleGetTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}/artifacts/{index}", s.auth(s.handleArtifact))
	mux.HandleFunc("GET /api/v1/agents", s.auth(s.handleAgents))
	mux.HandleFunc("GET /api/v1/nodes", s.auth(s.handleNodes))
	mux.HandleFunc("POST /api/v1/tasks/{id}/review", s.auth(s.handleReview))
	mux.HandleFunc("POST /api/v1/control-operations", s.auth(s.handleControlCreate))
	mux.HandleFunc("GET /api/v1/control-operations/{batch_id}", s.auth(s.handleControlGet))
	mux.HandleFunc("GET /api/v1/directory", s.auth(s.handleDirectory))
	console := web.Handler()
	mux.Handle("GET /{$}", console)
	mux.Handle("GET /app.js", console)
	mux.Handle("GET /style.css", console)
	return mux
}

// ---------- 鉴权 ----------

// loginRequest 登录请求体。
type loginRequest struct {
	Token string `json:"token"`
}

// handleLogin POST /api/v1/login：校验管理令牌 → 发会话 cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {token}")
		return
	}
	if s.cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.cfg.AdminToken)) != 1 {
		log.Printf("[hub] 管理面登录失败 remote=%s", r.RemoteAddr)
		writeErr(w, http.StatusUnauthorized, "auth_failed", "管理令牌错误")
		return
	}
	sid, err := newSessionID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "生成会话失败")
		return
	}
	s.mu.Lock()
	s.sessions[sid] = time.Now().UTC().Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // 回环明文 HTTP：无 Secure 属性（部署随 TLS 一起设计）
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in": int(sessionTTL / time.Second)})
}

// auth 会话校验中间件：无/失效会话一律 401（五端点全部受保护）。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		/* 优先级：会话 cookie → Bearer PAT（2026-10-21 验收：GitHub 式子令牌）。 */
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil && s.tokens.verify(strings.TrimPrefix(h, "Bearer ")) {
				next(w, r)
				return
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized", "令牌无效或已撤销")
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录（先 POST /api/v1/login）")
			return
		}
		s.mu.Lock()
		exp, ok := s.sessions[c.Value]
		now := time.Now().UTC()
		if ok && now.After(exp) {
			delete(s.sessions, c.Value) // 惰性清理过期会话
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "会话无效或已过期，请重新登录")
			return
		}
		next(w, r)
	}
}

// newSessionID 生成 256bit 随机会话 ID（hex）。
func newSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------- 五端点（SPEC 六表格） ----------

// handleSubmit POST /api/v1/tasks：校验链在 core.Submit（顺序 SPEC 4.5/六）；
// 202 新建 / 200 幂等命中 + dup / 409 同键不同内容 / 404 / 400 / 503。
// submitted_by 会话固定 admin（SPEC 六.五 提交身份）：登录会话即控制台，身份
// 由会话决定，忽略请求体自报（防伪造审计标签）；node 帧提交在 core 按连接
// 身份覆盖为 node_id。
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req protocol.TaskSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 TaskSubmitRequest JSON: "+err.Error())
		return
	}
	req.SubmittedBy = adminIdentity
	ack, err := s.cfg.Core.Submit(req, "")
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
	writeJSON(w, code, body)
}

// writeSubmitErr 提交校验链错误 → HTTP 状态与错误码（SPEC 六）。
func writeSubmitErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrAgentNotExposed):
		writeErr(w, http.StatusNotFound, protocol.CodeAgentNotExposed, err.Error())
	case errors.Is(err, mailbox.ErrConflict):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, mailbox.ErrMailboxFull):
		writeErr(w, http.StatusServiceUnavailable, protocol.CodeMailboxFull, err.Error())
	case errors.Is(err, core.ErrStorageFull):
		writeErr(w, http.StatusServiceUnavailable, protocol.CodeStorageFull, err.Error())
	case errors.Is(err, protocol.ErrFrameTooLarge):
		writeErr(w, http.StatusBadRequest, protocol.CodeFrameTooLarge, err.Error())
	case errors.Is(err, core.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		log.Printf("[hub] 管理面提交内部错误: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "提交失败")
	}
}

// handleListTasks GET /api/v1/tasks：全量任务列表（Loop 6 控制台总览统计与
// 任务监管页数据源）。V0 回环低量不分页，筛选由前端完成。
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.cfg.Core.ListTasks()})
}

// handleGetTask GET /api/v1/tasks/{id}：完整 Task（含状态/结果/needs_review）。
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.cfg.Core.GetTask(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, "任务不存在或已过保留期")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleArtifact GET /api/v1/tasks/{id}/artifacts/{index}：经 artifact_fetch/
// artifact_data 中继取回产物内容；30s 超时 → 504（SPEC 六）。
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "产物下标须为非负整数")
		return
	}
	art, err := s.cfg.Core.FetchArtifact(r.PathValue("id"), index, artifactFetchTimeout)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrTaskNotFound):
			writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, err.Error())
		case errors.Is(err, core.ErrArtifactNotFound):
			writeErr(w, http.StatusNotFound, protocol.CodeArtifactNotFound, err.Error())
		case errors.Is(err, core.ErrNodeOffline):
			writeErr(w, http.StatusServiceUnavailable, protocol.CodeNodeOffline, err.Error())
		case errors.Is(err, core.ErrFetchTimeout):
			writeErr(w, http.StatusGatewayTimeout, protocol.CodeFetchTimeout, err.Error())
		case errors.Is(err, core.ErrArtifactMismatch):
			writeErr(w, http.StatusConflict, protocol.CodeArtifactMismatch, err.Error())
		default:
			log.Printf("[hub] 管理面产物取回失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "产物取回失败")
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Artifact-Path", art.Meta.Path)
	w.Header().Set("X-Artifact-Sha256", art.Meta.Sha256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(art.Data)
}

// handleAgents GET /api/v1/agents：全网能力目录（card=AgentCard，绝不包含
// ExecProfile 字段——执行配置不出本机，SPEC 六）。
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.cfg.Core.ListAgents()})
}

// handleNodes GET /api/v1/nodes：节点总览。
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"nodes": s.cfg.Core.ListNodes()})
}

// ---------- P3 增补端点（SPEC 六.五） ----------

// adminIdentity 控制台会话固定身份：submitted_by 与核对记录 by 均为此值
// （SPEC 六.五 提交身份：仅作显示与审计标签，鉴权依据是会话本身）。
const adminIdentity = "admin"

// reviewRequest 核对登记请求体。
type reviewRequest struct {
	Action string `json:"action"`
	Note   string `json:"note"`
}

// handleReview POST /api/v1/tasks/{id}/review：仅 needs_review 任务有效；
// 写 review {by,at,action,note} 入快照，不改协议终态；重复提交覆盖旧记录。
// 200 登记后任务 / 404 task_not_found / 409 not_needs_review / 400 非法动作。
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	var req reviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {action, note}")
		return
	}
	t, err := s.cfg.Core.RecordReview(r.PathValue("id"), req.Action, req.Note, adminIdentity)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", "action 须为 ack_review|mark_failed")
		case errors.Is(err, core.ErrTaskNotFound):
			writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, err.Error())
		case errors.Is(err, core.ErrNotNeedsReview):
			writeErr(w, http.StatusConflict, "not_needs_review", err.Error())
		default:
			log.Printf("[hub] 核对登记内部错误: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "核对登记失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// controlOpRequest 控制操作批次请求体。
type controlOpRequest struct {
	Agent   string             `json:"agent"`
	Targets []string           `json:"targets"`
	Input   protocol.TaskInput `json:"input"`
	// IdemKey 可选批次幂等键：同键同内容重试返回原批次（200 + dup:true），
	// 同键不同内容 409 conflict。
	IdemKey string `json:"idem_key,omitempty"`
}

// handleControlCreate POST /api/v1/control-operations：两阶段持久化（骨架先于
// 子任务落库），逐目标走 Submit 全链（离线/未暴露目标标 skipped 不阻塞其余）；
// 202 返回 batch_id + items；幂等命中 200 + dup；submitted_by 会话固定 admin
// （SPEC 六.五）。
func (s *Server) handleControlCreate(w http.ResponseWriter, r *http.Request) {
	var req controlOpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {agent, targets, input}")
		return
	}
	batch, err := s.cfg.Core.CreateControlBatch(req.Agent, req.Targets, req.Input, req.IdemKey, adminIdentity)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrInvalid):
			writeErr(w, http.StatusBadRequest, "invalid_request", "agent 与非空 targets 必填")
		case errors.Is(err, core.ErrBatchConflict):
			writeErr(w, http.StatusConflict, "conflict", "批次同键不同内容")
		default:
			log.Printf("[hub] 控制批次持久化失败: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "批次创建失败")
		}
		return
	}
	code := http.StatusAccepted
	if batch.Dup {
		code = http.StatusOK // 幂等命中：200 + dup（同 POST /tasks 语义）
	}
	writeJSON(w, code, batch)
}

// handleControlGet GET /api/v1/control-operations/{batch_id}：批次与逐项状态
// （逐项任务状态为查询时点快照合并）。
func (s *Server) handleControlGet(w http.ResponseWriter, r *http.Request) {
	batch, ok := s.cfg.Core.GetControlBatch(r.PathValue("batch_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "batch_not_found", "批次不存在")
		return
	}
	writeJSON(w, http.StatusOK, batch)
}

// handleDirectory GET /api/v1/directory：全网 exposed=true 的 AgentCard 目录
// （不含 ExecProfile 字段；中心端与 node 目录代理共用，SPEC 六.五）。
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"directory": s.cfg.Core.ListDirectory()})
}

// ---------- 编码工具 ----------

// writeJSON 统一 JSON 应答。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 统一错误体 {"error": code, "message": msg}。
func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"error": errCode, "message": msg})
}
