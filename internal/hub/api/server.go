// Package api 实现 hub HTTP 管理面（SPEC 六/六.五）：登录鉴权 + 五端点 +
// 核对记录 + 控制批次 + 发现目录。
//
// 安全模型（Loop 4）：绑定 127.0.0.1（装配方经 session.ValidateAPIListen 强制）
// + 管理令牌登录（hub 配置 admin_token，POST /api/v1/login 发会话 cookie，其余
// 端点校验会话）。V0 会话为内存表 + 绝对过期，重启即失效（重新登录即可）。
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/web"
	"github.com/mtl802/swarmmind/internal/notify"
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
	// Groups 群聊存储（GROUP-RFC 六 MVP）；nil 时群端点统一 503 降级
	// （旧构造方不感知该依赖）。
	Groups *group.Store
	// Enroll 节点部署包编译参数（POST /api/v1/nodes/enroll-bundle，enroll.go）；
	// nil 时该端点统一 503 降级（旧构造方与既有测试不感知）。
	Enroll *EnrollConfig
	// NodeEntry 节点接入入口（GET /api/v1/nodes/enroll-info，第六轮修复 2）：
	// 接入向导生成 hub_url 的真相源——管理台（api_listen）与节点入口（node_listen）
	// 是两个端口，前端按控制台地址推导接入地址必错（接入串按串连不上 hub）。
	// nil 时该端点 503，前端回落本地推导（旧构造方不感知）。
	NodeEntry *NodeEntryConfig
	// Notify 通知分发器（P4③ 控制台化升级：渠道 CRUD/发测试/站内通知端点）；
	// nil 时通知端点统一 503 降级（旧构造方不感知）。
	Notify *notify.Dispatcher
	// Decision 派发建议决策服务（P5.1 D1，RFC-jev-dispatch）；nil/未启用时端点
	// 恒 200 + suggestion:null（决策是建议非必经，不阻塞派单，RFC 三）。
	Decision *decision.Service
	// Version 二进制版本（cmd/hub 经 -X main.version 注入；管理台页脚数据源）。
	// 随登录成功响应与登录态账户视图下发 server_version 字段；未认证分支不返回
	//（版本号不对未认证方披露）。
	Version string
}

// NodeEntryConfig 节点接入入口描述（cmd/hub 从 hub 配置推导：协议 + node_listen）。
type NodeEntryConfig struct {
	// Scheme 接入协议：ws|wss（insecure_loopback=true → ws；TLS 模式 → wss）。
	Scheme string
	// Listen 节点会话监听地址（host:port；host 空 = 全接口。回环绑定即仅本机
	// 节点可连——insecure_loopback 模式经 session.ValidateLoopback 强制）。
	Listen string
}

// Server 管理面 HTTP 服务。
type Server struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]time.Time // 会话 ID → 过期时刻（UTC）
	tokens   *tokenStore          // 子令牌（PAT）存储；DataDir 为空时为 nil（禁用）
	accounts *accountStore        // 管理员账户（密码登录）；DataDir 为空时为 nil
	enroll   *enrollBuilder       // 部署包编译器；Enroll 配置为空时为 nil（端点 503）
}

// New 构造管理面服务。
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, sessions: map[string]time.Time{}}
	if cfg.DataDir != "" {
		s.tokens = newTokenStore(cfg.DataDir)
		s.accounts = newAccountStore(cfg.DataDir)
	}
	if cfg.Enroll != nil {
		s.enroll = newEnrollBuilder(cfg.Enroll)
	}
	// 群 jev 主持人接力棒（DESIGN-butler 方案三 3.6，v4）：api 包经附加终态钩子
	// 注册接入（Config.OnTaskTerminal 由装配方 cmd/hub 的群回流闭包独占，本处
	// 不侵入）；Groups/Core 任一未接线即不注册（旧构造方与降级基座零感知）。
	if cfg.Groups != nil && cfg.Core != nil {
		cfg.Core.AddOnTaskTerminal(s.advanceHostOnTerminal)
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
	// 令牌管理组走 authAdmin（P1-03，2026-10-29 走查反馈批）：六.六豁免⑨「admin
	// 会话专属」的契约落地——PAT 可调这四条=持既授予凭据者可自行增发/撤销凭据，
	// 掏空豁免依据。此前 s.auth 的 Bearer 分支放行是越权缺口，非文档问题。
	mux.HandleFunc("POST /api/v1/auth/tokens", s.authAdmin(s.handleTokenCreate))
	mux.HandleFunc("GET /api/v1/auth/tokens", s.authAdmin(s.handleTokenList))
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", s.authAdmin(s.handleTokenRevoke))
	mux.HandleFunc("POST /api/v1/auth/tokens/{id}/purge", s.authAdmin(s.handleTokenPurge))
	mux.HandleFunc("POST /api/v1/tasks", s.auth(s.handleSubmit))
	mux.HandleFunc("GET /api/v1/tasks", s.auth(s.handleListTasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.auth(s.handleGetTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}/artifacts/{index}", s.auth(s.handleArtifact))
	mux.HandleFunc("GET /api/v1/agents", s.auth(s.handleAgents))
	mux.HandleFunc("GET /api/v1/nodes", s.auth(s.handleNodes))
	mux.HandleFunc("GET /api/v1/nodes/enroll-info", s.auth(s.handleEnrollInfo)) // 节点接入入口地址（第六轮修复 2，enroll.go）
	mux.HandleFunc("POST /api/v1/nodes/enroll-bundle", s.auth(s.handleEnroll))  // 部署包生成（enroll.go）
	mux.HandleFunc("POST /api/v1/tasks/{id}/review", s.auth(s.handleReview))
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.auth(s.handleTaskCancel))
	mux.HandleFunc("POST /api/v1/control-operations", s.auth(s.handleControlCreate))
	mux.HandleFunc("GET /api/v1/control-operations/{batch_id}", s.auth(s.handleControlGet))
	mux.HandleFunc("POST /api/v1/works", s.auth(s.handleWorkCreate))                 // 工作单创建（P5 W1，幂等）
	mux.HandleFunc("POST /api/v1/works/{id}/takeover", s.auth(s.handleWorkTakeover)) // 原子接管（P5 W2，CAS）
	mux.HandleFunc("POST /api/v1/works/{id}/collect", s.auth(s.handleWorkCollect))   // 收集/关闭（P5 W3，终态闭合）
	mux.HandleFunc("GET /api/v1/works", s.auth(s.handleWorkList))                    // 工作单列表
	mux.HandleFunc("GET /api/v1/works/{id}", s.auth(s.handleWorkGet))                // 详情（事件游标 since_events）
	mux.HandleFunc("GET /api/v1/directory", s.auth(s.handleDirectory))
	mux.HandleFunc("POST /api/v1/dispatch-suggest", s.auth(s.handleDispatchSuggest))            // 派发建议（P5.1 D1）
	mux.HandleFunc("GET /api/v1/prime", s.auth(s.handlePrimeGet))                               // 统筹指定查询（2026-10-23 网页指定）
	mux.HandleFunc("POST /api/v1/groups", s.auth(s.handleGroupCreate))                          // 群聊 MVP（GROUP-RFC 六）
	mux.HandleFunc("GET /api/v1/groups", s.auth(s.handleGroupList))                             // 群列表
	mux.HandleFunc("GET /api/v1/groups/{id}/messages", s.auth(s.handleGroupMessages))           // 增量拉取
	mux.HandleFunc("POST /api/v1/groups/{id}/messages", s.auth(s.handleGroupSay))               // 发言
	mux.HandleFunc("POST /api/v1/groups/{id}/close", s.auth(s.handleGroupClose))                // 结束（终态）
	mux.HandleFunc("GET /api/v1/notify/channels", s.auth(s.handleNotifyChannelList))            // 通知渠道（P4③ 控制台化）
	mux.HandleFunc("POST /api/v1/notify/channels", s.auth(s.handleNotifyChannelCreate))         // 创建（body 含明文凭证）
	mux.HandleFunc("PUT /api/v1/notify/channels/{id}", s.auth(s.handleNotifyChannelUpdate))     // 更新（****** 占位保留原值）
	mux.HandleFunc("DELETE /api/v1/notify/channels/{id}", s.auth(s.handleNotifyChannelDelete))  // 删除
	mux.HandleFunc("POST /api/v1/notify/channels/{id}/test", s.auth(s.handleNotifyChannelTest)) // 发测试消息
	mux.HandleFunc("GET /api/v1/notify/inbox", s.auth(s.handleNotifyInbox))                     // 站内通知（内存态，重启清空）
	mux.HandleFunc("POST /api/v1/notify/inbox/read", s.auth(s.handleNotifyInboxRead))           // 标已读（id 缺省=全部）
	mux.HandleFunc("GET /.well-known/agent.json", s.handleAgentCard)                            // A2A 发现卡（免鉴权，SPEC 六.五）
	mux.HandleFunc("POST /a2a/rpc", s.auth(s.handleA2ARPC))                                     // A2A JSON-RPC（与现有同一令牌体系）
	console := web.Handler()
	mux.Handle("GET /{$}", console)
	mux.Handle("GET /app.js", console)
	mux.Handle("GET /style.css", console)
	mux.Handle("GET /logo.svg", console)
	mux.Handle("GET /favicon.svg", console)
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in": int(sessionTTL / time.Second), "server_version": s.cfg.Version, "terminal_retention_sec": s.terminalRetentionSec()})
}

// terminalRetentionSec 终态保留生效值（秒；maturity-retention-config）：读
// Core.TerminalRetention()——hub.yaml terminal_retention 是唯一真相，Sweep
// 清理行为与暴露值同源。随登录响应/登录态账户视图下发（与 server_version
// 同面），前端保留期文案数据源；未认证分支不返回（保留期不对未认证方披露，
// 同 server_version 口径）。Core 未装配（旧构造方/测试）回落协议默认。
func (s *Server) terminalRetentionSec() int {
	if s.cfg.Core == nil {
		return int(protocol.TerminalRetention / time.Second)
	}
	return int(s.cfg.Core.TerminalRetention() / time.Second)
}

// authPrincipalKey 请求上下文键：auth 中间件解析出的服务端验证主体。
type authCtxKey int

const authPrincipalKey authCtxKey = 0

// withAuthPrincipal / authPrincipal 主体随请求上下文传递；未过 auth 中间件的
// 请求取到空串（生产路由不存在此形态）。
func withAuthPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, authPrincipalKey, principal)
}

// authPrincipal 服务端验证的鉴权主体（RFC 六.2：审计事件以独立字段落账，
// 与请求自报标签分开存储——请求体自报身份不作数）。
func authPrincipal(r *http.Request) string {
	v, _ := r.Context().Value(authPrincipalKey).(string)
	return v
}

// adminSession 会话 cookie 校验 + admin 主体注入（auth 与 authAdmin 共用的会话
// 分支）；无效时已写 401 应答并返回 nil。惰性清理过期会话的语义保持不变。
func (s *Server) adminSession(w http.ResponseWriter, r *http.Request) *http.Request {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录（先 POST /api/v1/login）")
		return nil
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
		return nil
	}
	return r.WithContext(withAuthPrincipal(r.Context(), adminIdentity))
}

// authAdmin admin 会话专属门禁（P1-03，2026-10-29 走查反馈批）：令牌管理组
// （创建/列表/撤销/purge，SPEC 六.六豁免⑨）只对 hub admin 会话开放。PAT 是已被
// 授予的凭据，持它再管理令牌组 = 可自行增发/撤销凭据的提权通道，一律不放行：
// Bearer 头在场即不进会话分支——有效 PAT 403（身份成立、授权不成立），无效凭据
// 401；匿名（无凭据）401。生产唯一使用方是网页 admin（api() 只带会话 cookie，
// 不发 Authorization 头），无破坏面；脚本用 PAT 调其余 Bearer 路由不受影响。
func (s *Server) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil {
				if _, ok := s.tokens.verify(strings.TrimPrefix(h, "Bearer ")); ok {
					writeErr(w, http.StatusForbidden, "admin_session_required",
						"令牌管理仅限管理员会话（PAT 不得管理令牌组，请用 admin 登录 hub 控制台）")
					return
				}
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized", "令牌无效或已撤销")
			return
		}
		if rr := s.adminSession(w, r); rr != nil {
			next(w, rr)
		}
	}
}

// auth 会话校验中间件：无/失效会话一律 401（五端点全部受保护）。校验通过的
// 请求把服务端验证主体注入上下文：admin 会话 → adminIdentity；Bearer PAT →
// pat:<令牌名>（tokens.verify 命中即身份成立）。令牌管理组不走本中间件的
// Bearer 分支（authAdmin 专属，见上）。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		/* 优先级：会话 cookie → Bearer PAT（2026-10-21 验收：GitHub 式子令牌）。 */
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil {
				if name, ok := s.tokens.verify(strings.TrimPrefix(h, "Bearer ")); ok {
					next(w, r.WithContext(withAuthPrincipal(r.Context(), "pat:"+name)))
					return
				}
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized", "令牌无效或已撤销")
			return
		}
		if rr := s.adminSession(w, r); rr != nil {
			next(w, rr)
		}
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

// hubSubmitRequest POST /api/v1/tasks 请求体：TaskSubmitRequest 全字段平铺解码
// （含 group_ref——管理面扩展，GROUP-RFC 转任务闭环；协议帧 task_submit 不携带，
// node 间提交恒空。字段已在 protocol.TaskSubmitRequest 上，嵌入后直接平铺）。
type hubSubmitRequest struct {
	protocol.TaskSubmitRequest
}

// handleSubmit POST /api/v1/tasks：校验链在 core.Submit（顺序 SPEC 4.5/六）；
// 202 新建 / 200 幂等命中 + dup / 409 同键不同内容 / 404 / 400 / 503。
// submitted_by 会话固定 admin（SPEC 六.五 提交身份）：登录会话即控制台，身份
// 由会话决定，忽略请求体自报（防伪造审计标签）；node 帧提交在 core 按连接
// 身份覆盖为 node_id。
//
// group_ref 非空走「先登记后提交」（批 B 修复1）：groupRefCheck 通过后、Submit
// 之前完成 AddTaskRef——快速任务（同步链下终态在 Submit 内即回落）的 fireTerminal
// 回流时引用必已就位，不再有「登记先于终态失败 → 回流永久丢失」竞态。任务 ID
// 预生成（登记需要 task_id 而 ID 由 Submit 生成），经 SubmitWithID 使登记与落库
// 引用同一 ID；Submit 失败或 dup 命中即回滚刚加的引用。dup 时原任务按指纹规则
// 必然同群（group_ref 已入指纹，SPEC 4.5），其引用首次提交时已登记；此处再幂等
// 登记一次，自愈首次登记失败的历史缺口（GroupStore 追加去重，不重复登记）。
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req hubSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 TaskSubmitRequest JSON: "+err.Error())
		return
	}
	req.SubmittedBy = adminIdentity
	if !s.groupRefCheck(w, req.GroupRef) {
		return
	}
	var preID string
	if req.GroupRef != "" {
		preID = protocol.NewTaskID()
		if err := s.cfg.Groups.AddTaskRef(req.GroupRef, preID); err != nil {
			writeGroupErr(w, err) // 校验与登记间的窄窗口：群被关/删（409/404）或落盘失败
			return
		}
	}
	// 绑定派发事件主体取服务端验证主体（authPrincipal：admin 会话/PAT 令牌名，
	// P5 W2 审查#4）——SubmittedBy 仍固定 admin 兼容既有语义（显示与审计标签）。
	ack, err := s.cfg.Core.SubmitWithID(req.TaskSubmitRequest, preID, "", authPrincipal(r))
	if err != nil {
		s.unwireGroupTask(req.GroupRef, preID) // 提交失败：回滚刚加的引用
		writeSubmitErr(w, err)
		return
	}
	if ack.Dup {
		s.unwireGroupTask(req.GroupRef, preID) // 命中原任务：预登记的 ID 未落库，回滚
		if req.GroupRef != "" {
			_ = s.cfg.Groups.AddTaskRef(req.GroupRef, ack.TaskID) // 幂等重登记（自愈缺口）
		}
	} else {
		s.noteGroupTaskCreated(req.GroupRef, ack.TaskID, taskDesc(req.Input))
	}
	code := http.StatusAccepted
	body := map[string]any{"task_id": ack.TaskID, "trace_id": ack.TraceID}
	if ack.Work != nil {
		body["work"] = ack.Work // 绑定派发结果（P5 W2：工作单/步骤/尝试定位）
	}
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
	case errors.Is(err, core.ErrInvalidDeps):
		// 依赖声明不合法（P4.3/SPEC 4.12：未知 idem/自引用/重复/超深度/超级联/环）
		writeErr(w, http.StatusBadRequest, protocol.CodeInvalidDependency, err.Error())
	case errors.Is(err, core.ErrWorkNotFound):
		writeErr(w, http.StatusNotFound, "work_not_found", err.Error())
	case errors.Is(err, core.ErrWorkClosed):
		writeErr(w, http.StatusConflict, "work_closed", err.Error())
	case errors.Is(err, core.ErrWorkTermConflict):
		// 接管 CAS 失败或旧任期编排写入拒绝（P5 W2/RFC-work-resume 二.2）
		writeErr(w, http.StatusConflict, "work_term_conflict", err.Error())
	case errors.Is(err, core.ErrWorkNoTerm):
		writeErr(w, http.StatusConflict, "work_no_term", err.Error())
	case errors.Is(err, core.ErrWorkStepLocked):
		writeErr(w, http.StatusConflict, "work_step_locked", err.Error())
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
	// related_works 拆解挂接下钻（DESIGN-butler 方案二，2026-10-28）：OriginTask
	// 反查的工作单摘要（创建降序；无挂接为空数组不省略字段——前端判定面稳定）。
	// 任务是账本副本只读，附加字段平级输出不污染账本形态。
	body := struct {
		protocol.Task
		RelatedWorks []core.WorkSummary `json:"related_works"`
	}{Task: t, RelatedWorks: s.cfg.Core.RelatedWorks(t.ID)}
	writeJSON(w, http.StatusOK, body)
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

// handleNodes GET /api/v1/nodes：节点总览。应答附顶层 terminal_retention_sec
// （终态保留生效秒数，terminalRetentionSec() 同源——Sweep 清理行为与暴露值一致；
// maturity-retention-config R2 必修④：MCP swarmmind_nodes 透传数据源，与登录
// 响应/账户视图同值；六.六规则 15）。
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":                  s.cfg.Core.ListNodes(),
		"terminal_retention_sec": s.terminalRetentionSec(),
	})
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

// cancelRequest 取消任务请求体（reason 可空）。
type cancelRequest struct {
	Reason string `json:"reason"`
}

// handleTaskCancel POST /api/v1/tasks/{id}/cancel（管理面任务取消，SPEC 4.3
// cancelled 终态）：hub 事务内收敛状态并尽力下发 task_cancel，node 终态结果经
// task_result 异步回传（前端轮询可见）。202 取消已受理 / 200 幂等命中（此前已
// cancelled）/ 409 已终态不可取消（completed/failed/needs_review）/ 404 不存在。
func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		// 空请求体合法（reason 可省），其余解码失败拒绝
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {reason}")
		return
	}
	t, dup, err := s.cfg.Core.CancelTask(r.PathValue("id"), req.Reason)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrTaskNotFound):
			writeErr(w, http.StatusNotFound, protocol.CodeTaskNotFound, "任务不存在或已过保留期")
		case errors.Is(err, core.ErrNotCancellable):
			writeErr(w, http.StatusConflict, "not_cancellable", "任务已终态，不可取消")
		default:
			log.Printf("[hub] 取消任务内部错误: %v", err)
			writeErr(w, http.StatusInternalServerError, "internal", "取消失败")
		}
		return
	}
	code := http.StatusAccepted
	if dup {
		code = http.StatusOK // 幂等命中：200（同 POST /tasks dup 语义）
	}
	writeJSON(w, code, t)
}

// controlOpRequest 控制操作批次请求体。
type controlOpRequest struct {
	Agent   string             `json:"agent"`
	Targets []string           `json:"targets"`
	Input   protocol.TaskInput `json:"input"`
	// IdemKey 可选批次幂等键：同键同内容重试返回原批次（200 + dup:true），
	// 同键不同内容 409 conflict。
	IdemKey string `json:"idem_key,omitempty"`
	// GroupRef 可选群引用（GROUP-RFC 转任务闭环）：批次各子任务创建成功后
	// 逐一登记进群并注入创建注记消息。
	GroupRef string `json:"group_ref,omitempty"`
}

// handleControlCreate POST /api/v1/control-operations：两阶段持久化（骨架先于
// 子任务落库），逐目标走 Submit 全链（离线/未暴露目标标 skipped 不阻塞其余）；
// 202 返回 batch_id + items；幂等命中 200 + dup；submitted_by 会话固定 admin
// （SPEC 六.五）。group_ref 非空时先校验群可收消息，批次新建（非 dup）成功后
// 逐项登记入群。
func (s *Server) handleControlCreate(w http.ResponseWriter, r *http.Request) {
	var req controlOpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {agent, targets, input}")
		return
	}
	if !s.groupRefCheck(w, req.GroupRef) {
		return
	}
	batch, err := s.cfg.Core.CreateControlBatch(req.Agent, req.Targets, req.Input, req.IdemKey, adminIdentity, req.GroupRef)
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
	if !batch.Dup {
		for _, it := range batch.Items {
			s.wireGroupTask(req.GroupRef, it.TaskID, taskDesc(req.Input))
		}
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

// handlePrimeGet GET /api/v1/prime：统筹指定汇聚展示（2026-10-28 客户端声明制——
// 指定权在 node（AgentCard.Orchestrator 随能力表上报），hub 只读汇聚：
// 恰一声明 → designation=该条目；多声明 → designation=null + conflicts 清单
// （回退名字约定，控制台告警）；零声明 → designation=null（回退名字约定）。
// 中心端写入口（PUT /api/v1/prime）已随客户端声明制退役。
func (s *Server) handlePrimeGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"designation": s.cfg.Core.PrimeDesignation(),
		"conflicts":   s.cfg.Core.PrimeConflicts(),
	})
}

/* handlePrimeSet PUT /api/v1/prime 已退役（2026-10-28 客户端声明制）：中心端不再
   提供统筹指定写入口，路由注册同步删除；客户端经能力表 Orchestrator 声明。 */

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
