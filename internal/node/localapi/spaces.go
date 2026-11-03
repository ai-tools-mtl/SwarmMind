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
//   - POST /api/v1/spaces/{id}/budget          预算 sugar（主人专属独立入口→Control set_budget）
//   - POST /api/v1/spaces/{id}/policy          策略 sugar（主人专属独立入口→Control policy）
//   - POST /api/v1/spaces/{id}/finalize        定版 sugar（主人专属独立入口→Control finalize）
//   - POST /api/v1/spaces/{id}/archive         归档 sugar（主人专属独立入口→Control archive）
//
// W05-04 策略面（本文件 control 内接线 + spaces_records.go）：
//   - POST /api/v1/spaces/{id}/control action=set_phase 经 strategy 预检后落账
//     （判定在 strategy、落账在 collab——W05-03 分离边界的端点面兑现；start 实际
//     生效后同源引导循环阶段 BootstrapCycle+ActSetPhase）；
//   - GET  /api/v1/spaces/{id}/strategy       策略投影只读（P04 策略区数据源）；
//   - POST /api/v1/spaces/{id}/records        策略循环记录登记（candidate/review/
//     issue/evidence；数据写入≠执行——不派发不推进循环）；
//   - GET  /api/v1/spaces/{id}/records?since= 记录增量读（留痕序号游标）。
//
// 鉴权分层（W04-02 豁免⑯收窄兑现）：写面（创建/控制/派发/发言）authSpaceSubject
// ——local_token=主人（恒通）；prime_token=共享编排主体「prime」（经 W02-01
// mcpaccess 门：显式启用才放行，403 mcp_disabled 人话指路）。逐动作授权在 collab
// 模型层（driver/orchestrate 形态；主人专属动作 set_budget/policy/finalize/archive
// 对编排主体 403——豁免收窄后仍人工专属，MCP 工具面不受理）。读面（列表/详情/
// 派发查询/消息读）authRead（持任一本机凭证即可，与 agent 会话查询面同界）。
// 四个 sugar 端点（W04-03，任务书 REST 表「人工预算/授权策略/定版/归档」行的
// 独立主人接口）走 authLocal（仅 local_token；prime_token 401——资源主权动作
// 不经编排凭据入口，与逐动作授权「仅主人」行同界）。sugar 只做同族动作映射
// （字段即 ControlRequest 对应字段），不新增任何旁路语义：状态机/版本/授权校验
// 全部在 Control 单事务内执行。
// 主体由服务端鉴权层解析后传入 collab，不采信请求自报（AGENTS 审计纪律）。
//
// 错误投影：collab Error.Code → HTTP（见 writeSpaceErr）；存储未装配 503
// spaces_unavailable（与 sessions 降级同形态，诚实不虚构）。
package localapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/strategy"
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

// reservedRequestIDPrefixes 保留前缀清单（W07-02 起单源 collab.ReservedRequestIDPrefixes
// ——hub api 面同表共用，两端同清单防漂移；原 localapi 内联清单语义不变）。
var reservedRequestIDPrefixes = collab.ReservedRequestIDPrefixes

// requestIDReserved 中央保留前缀门：id（去围边后）命中任一内部命名空间前缀 →
// 400 reserved_request_id（人话列明保留清单）返回 true；未命中 false。调用方在
// 解码后、一切校验与事务之前先过本门（零副作用不占回执）。四写入口统一复用，
// 单点维护保留清单。
func (s *Server) requestIDReserved(w http.ResponseWriter, id string) bool {
	if collab.RequestIDReserved(id) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
		return true
	}
	return false
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
	if s.requestIDReserved(w, req.RequestID) {
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

// handleSpaceList GET /api/v1/spaces：列表（创建序）。归档过滤（W08-02）：默认
// 隐藏已归档空间（archived=true 不出现在列表——归档只是列表收纳，详情/记录/消息
// 读路径不关闭仍可回查）；`?include_archived=true` 显式包含（控制台页签计数依赖
// 全量视图，经本参数显式取全量）。应答附 `archived_hidden`（本次被过滤隐藏的
// 归档空间数；include_archived=true 时恒 0）——消费者不把「列表没有」误读成
// 「不存在」。
func (s *Server) handleSpaceList(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	all := s.cfg.Collab.Spaces()
	spaces := make([]collab.Space, 0, len(all))
	hidden := 0
	for _, sp := range all {
		if sp.Archived && !includeArchived {
			hidden++
			continue
		}
		spaces = append(spaces, sp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spaces": spaces, "total": len(spaces), "archived_hidden": hidden,
	})
}

// spaceDiag 空间诊断投影（W08-02 执行/问题诊断面，P09 数据源；SPEC 六.六
// swarmmind_space_status 同批行）：全部为既有事实的只读 join 投影，零影子状态、
// 零新存储——「必要新增诊断字段不另建影子状态」的字面兑现。
type spaceDiag struct {
	// InFlight 在途/排队意图数（= space.in_flight 同源投影，冗余给出便于单一
	// 诊断消费方只读 diag）。
	InFlight int `json:"in_flight"`
	// AwaitingReview 其中轮终不确定（needs_review 终态已落账、意图按纪律留待
	// 核对未收敛）的在途意图数（W04-02 收敛分类「不确定→留待核对不重跑」的可见
	// 化——needs_review/留待核对在诊断面如实可见，不冒充运行中）。0 省略。
	AwaitingReview int `json:"awaiting_review,omitempty"`
}

// spaceDiagOf 计算空间诊断投影（node 面：意图 × 账本轮终 join；hub 面同形函数
// 见 internal/hub/api/spaces.go——两端各自 join 本端轮终事实源，语义逐条同规）。
// 轮终不确定判定：账本轮已 finished 且 Result.Status=needs_review（重启对账与
// 活路径同款「留待核对」分类的查询侧投影）。
func (s *Server) spaceDiagOf(id string, view collab.SpaceView) spaceDiag {
	d := spaceDiag{InFlight: view.InFlight}
	if d.InFlight == 0 || s.cfg.Ledger == nil {
		return d
	}
	for _, it := range s.cfg.Collab.Intents(id) {
		if !it.Status.InFlight() {
			continue
		}
		e, ok := ledgerTurnOf(s.cfg.Ledger, it.SessionID, it.TurnSeq)
		if !ok || e.State != ledger.StateFinished || e.Result == nil {
			continue
		}
		if e.Result.Status == protocol.ResultNeedsReview {
			d.AwaitingReview++
		}
	}
	return d
}

// ledgerTurnOf 账本轮次查找（session+seq → 条目；agentPendingView 同款遍历口径，
// 本地包内单点维护）。
func ledgerTurnOf(l *ledger.Ledger, sessionID string, seq int64) (ledger.Entry, bool) {
	if l == nil || sessionID == "" || seq <= 0 {
		return ledger.Entry{}, false
	}
	for _, e := range l.View() {
		sr := e.Task.Session
		if sr != nil && sr.SessionID == sessionID && sr.TurnSeq == seq {
			return e, true
		}
	}
	return ledger.Entry{}, false
}

// handleSpaceGet GET /api/v1/spaces/{id}：详情（视图含成员/负责人/预算用量/
// 策略/在途投影）。已归档空间照常可查（W08-02：归档只隐藏列表，详情读路径不
// 关闭）；应答附 `diag` 诊断投影（spaceDiag——在途与留待核对可见）。
func (s *Server) handleSpaceGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Collab.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": view, "diag": s.spaceDiagOf(id, view)})
}

// handleSpaceControl POST /api/v1/spaces/{id}/control：控制骨架（逐动作授权表，
// collab 模型层判定）。request_id 必填（写操作稳定请求标识，重放返回原回执）；
// expected_version 必填（乐观锁；takeover 另有 expected_term CAS，term 裁决先于
// 版本复核）。恢复保护（W04-02）：存在在途/排队意图时 resume 须携 confirm=true
// 显式确认（无在途直通）——「人工介入后不自动续跑」的放行由人显式给出。
// set_phase 端点面接线（W05-04）：先经 strategy.ValidatePhaseTransition 无副作用
// 预检（18 边转换表+暂停恢复约束）后放行 Control 落账，见 setPhasePrecheck。
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
	// 内部保留前缀门（终审必修4①/必修5 全入口收口）：中央 helper 统一拦 collab
	// 全局回执表的内部 opID 命名空间（保留清单见 reservedRequestIDPrefixes）——
	// 客户端撞号会占住回执使内部动作 conflict、重试永败。显式 400 人话（MCP 面
	// 同理由服务端承担：工具透传本 400 人话即可）。
	if s.requestIDReserved(w, req.RequestID) {
		return
	}
	// set_phase 门（W05-04 接线，取代 W05-03 的 501 显式拒开）：判定在 strategy、
	// 落账在 collab——REST 直调不再可能落表外边/旁路暂停恢复约束（预检拒绝先于
	// 一切事务，零副作用不占 request_id）。
	// 重放判定先于预检（终审必修2②）：已受理同文请求（原回执在册）跳过预检直落
	// Control 取原回执——预检裁决的是「新转换」，阶段推进后原请求重放不得被新状态
	// 409 卡死（回执先于预检，同 apply「重放命中先于一切校验」序）；未命中/异文走
	// 正常路径（异文由 apply 报 request_conflict）。
	var setPhaseFrom strategy.Phase // 预检解析的来源阶段（落账后引擎留痕用）
	if req.Action == collab.ActSetPhase && !s.cfg.Collab.ServedIdentical(id, req) {
		from, ok := s.setPhasePrecheck(w, id, req)
		if !ok {
			return
		}
		setPhaseFrom = from
	}
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
	// 策略引擎跟随落账（W05-03 分离边界：引擎事实跟随落账结果，不预写）：
	//   - start 实际生效 → BootstrapCycle 引导（幂等）+ ActSetPhase(drafting) 落账
	//     ——「装配层在空间显式启动后调用」的既定契约，阶段面自此有真相可投影；
	//   - set_phase 实际生效 → SetPhase 记引擎侧留痕与 PausedFrom。
	// dup 门（终审必修2①）：重放命中返回原回执（Applied 为落账时的原值）——引擎
	// 副作用绝不随重放回放，「dup 不回放留痕」由此才成立（重放 SetPhase 会清
	// PausedFrom、重放引导会占落账事务——原回执语义只有零副作用重放才保得住）。
	if s.cfg.Strategy != nil && !dup {
		switch req.Action {
		case collab.ActStart:
			s.bootstrapCycleAfterStart(id, subj)
		case collab.ActSetPhase:
			to := strategy.Phase(strings.TrimSpace(req.Phase))
			if setPhaseFrom.Valid() && to.Valid() && setPhaseFrom != to {
				if err := s.cfg.Strategy.SetPhase(id, setPhaseFrom, to); err != nil {
					// 落账已成立而引擎留痕失败（预检与落账窗口内被并发变更——版本
					// CAS 使其几乎不可能）：如实日志，不回滚不改判（collab 是阶段
					// 真相，引擎事实是解释面）。
					log.Printf("[spaces] set_phase 引擎留痕失败 space=%s %s→%s: %v", id, setPhaseFrom, to, err)
				}
			}
		}
	}
	// start 重放收敛引导（终审必修4②）：start 已在册（dup 命中）而循环阶段仍空/
	// 非法 → 幂等引导补课（首次引导失败——如引导落账事务冲突——后重试 start 的
	// 收敛路径；已初始化时阶段合法不进入本分支，BootstrapCycle 幂等命中零开销）。
	if s.cfg.Strategy != nil && dup && req.Action == collab.ActStart {
		if view, ok := s.cfg.Collab.SpaceView(id); ok && !strategy.Phase(strings.TrimSpace(view.Phase)).Valid() {
			s.bootstrapCycleAfterStart(id, subj)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// setPhasePrecheck set_phase 端点面预检（W05-04 接线；无副作用，拒绝先于一切事务
// ——不落回执、不占 request_id，同 request_id 改投他动作照常受理；已受理同文重放
// 在调用方先经 ServedIdentical 短路，不到本函数）。分层表述：
//   - 400 invalid_request：非法词形（空/未知词/含非法字符——值基错误在预检即拒，
//     不再透传模型层）；
//   - 409 illegal_transition：不合法转换（阶段未初始化、当前阶段未知、表外边、
//     暂停恢复约束——strategy.ValidatePhaseTransition 人话原样透出）+ completed
//     终态直写门（终审必修3：终态只能经完成动作判据核验落账，直写绕过判据且不可
//     重入卡死空间）；
//   - 同值幂等（from == to）不预检直通模型层：collab 落账面承担运行面状态门槛与
//     同值零变更（applied=false），与 archive 先例同形（completed→completed 同值
//     亦走此路——不视为新转换）；
//   - 404 not_found：空间不存在（读档即判，语义与模型层一致）；
//   - 503 strategy_unavailable：策略引擎未装配（装配缺失诚实降级，不假放行）。
//
// 返回解析出的来源阶段（仅合法词形时有效；同值幂等路径也返回——调用方落账后留痕
// 分支会因 from==to 自然跳过）。
func (s *Server) setPhasePrecheck(w http.ResponseWriter, spaceID string, req collab.ControlRequest) (strategy.Phase, bool) {
	if s.cfg.Strategy == nil {
		writeErr(w, http.StatusServiceUnavailable, "strategy_unavailable",
			"策略引擎未装配：set_phase 须经策略判定面预检后落账（本机未装配策略引擎；升级或装配后可用）")
		return "", false
	}
	to := strategy.Phase(strings.TrimSpace(req.Phase))
	if !to.Valid() {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"phase 词形非法："+humanPhaseError(req.Phase)+"；合法词形 drafting|frozen|review|rework|rereview|paused|completed（须为表内转换，先读 GET strategy 看当前阶段）")
		return "", false
	}
	view, ok := s.cfg.Collab.SpaceView(spaceID)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+spaceID)
		return "", false
	}
	from := strategy.Phase(strings.TrimSpace(view.Phase))
	if !from.Valid() {
		if view.Phase == "" {
			writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
				"循环阶段未初始化：无从校验转换（显式启动 start 时由装配引导 drafting；引导后经表内边推进）")
		} else {
			writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
				"当前阶段 "+strconv.Quote(view.Phase)+" 不是合法阶段词：阶段真相疑似异常，先人工核对空间状态（预检拒绝，未做任何变更）")
		}
		return "", false
	}
	if from == to {
		return from, true // 同值幂等：交模型层落运行面门槛与零变更裁决
	}
	// completed 终态门（终审必修3）：completed 只能经完成动作落账（finish/finalize
	// 过完成判据核验）——REST 直写绕过判据（无版本无证据 200），且阶段终态不可重入
	//（完成判据 gap「阶段已为 completed：终态不可重入」）会把空间永久卡死在完成
	// 不可达面。表内 →completed 边保留在 ValidatePhaseTransition 不动（进程内装配
	// advance/完成链消费方不受影响），仅端点预检面拦直写；completed→completed 同值
	// 已在上方幂等直通，不视为新转换。
	if to == strategy.PhaseCompleted {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
			"completed 不能经 set_phase 直接写入：终态只能经完成动作落账（finish/finalize 过完成判据核验——直接写绕过判据，且终态不可重入会卡死空间）；完成请走主人 finalize 或 finish 判据链")
		return "", false
	}
	if err := s.cfg.Strategy.ValidatePhaseTransition(spaceID, from, to); err != nil {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition, stripStrategyPrefix(err.Error()))
		return "", false
	}
	return from, true
}

// bootstrapCycleAfterStart start 实际生效后的循环引导（W05-03 契约「装配层在空间
// 显式启动后调用」；幂等——已初始化零变更）：引导目标经同通道 ActSetPhase 落账，
// 使阶段面与引擎事实同源成立。落账失败如实日志不回滚 start（阶段引导可重放：幂等
// 引导 + 同值落账零变更）。
func (s *Server) bootstrapCycleAfterStart(spaceID string, subj collab.Subject) {
	view, ok := s.cfg.Collab.SpaceView(spaceID)
	if !ok {
		return
	}
	next, err := s.cfg.Strategy.BootstrapCycle(view)
	if err != nil {
		log.Printf("[spaces] start 后循环引导失败 space=%s: %v", spaceID, err)
		return
	}
	if string(next) == view.Phase {
		return // 已初始化（幂等命中）：无需落账
	}
	_, _, err = s.cfg.Collab.Control(spaceID, collab.ControlRequest{
		RequestID:       "boot-" + spaceID + "-" + string(next),
		Action:          collab.ActSetPhase,
		ExpectedVersion: view.Version,
		// Term 携当前任期：引导落账与 start 同主体同形态（driver 族授权逐动作
		// 复核——external/builtin 驱动者形态须 term 匹配；owner-manual 不查 term）。
		Term:  view.Leader.Term,
		Phase: string(next),
		Note:  "start 后循环阶段引导",
	}, subj)
	if err != nil {
		log.Printf("[spaces] start 后阶段引导落账失败 space=%s →%s: %v", spaceID, next, err)
	}
}

// humanPhaseError 非法词形人话（空值与脏值分开表述）。
func humanPhaseError(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "phase 必填（目标循环阶段）"
	}
	return strconv.Quote(trimmed) + " 不是合法阶段词"
}

// stripStrategyPrefix 错误文案去包前缀（strategy: 已由错误码承担；人话正文直出）。
func stripStrategyPrefix(msg string) string {
	for _, p := range []string{"strategy: "} {
		if len(msg) > len(p) && msg[:len(p)] == p {
			return msg[len(p):]
		}
	}
	return msg
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
// （SpaceDispatch——派发不经 control：终审 S2）。
func (s *Server) handleSpaceDispatch(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceDispatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {member, expected_version, note?} JSON: "+err.Error())
		return
	}
	// 保留前缀门（终审必修5 全入口收口）：派发 request_id 同享全局回执表，且收敛/
	// 任务关联的内部键（converge:/attach:）由派发 request_id 派生——客户端撞号即
	// 占内部回执。先于一切校验与事务零副作用。
	if s.requestIDReserved(w, req.RequestID) {
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
// 排队/运行/终态）。W08-02 诊断注记（全为既有事实投影，零影子状态）：
//   - `task_gone`：意图登记的执行任务已过保留期清理（账本无该任务条目）——历史
//     缺口如实可见；意图/轮次注记仍在（任务清理 ≠ 从未执行，收敛事实与审计事件
//     在 collab 账本不受任务保留期影响）；
//   - `awaiting_review`：意图在途且账本轮终为 needs_review——按纪律留待核对未
//     收敛（不自动重跑），诊断面如实可见；
//   - `binding`：成员会话绑定登记态（session_id + registered——归属空间核对，
//     解绑/换绑后 registered=false 如实）。
func (s *Server) handleSpaceDispatchGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	spaceID := r.PathValue("id")
	it, ok := s.cfg.Collab.IntentByRequest(spaceID, r.PathValue("request_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "dispatch_not_found", "意图不存在（request_id 未受理或已滚动出空间）: "+r.PathValue("request_id"))
		return
	}
	body := map[string]any{"dispatch": it}
	// 绑定登记态（诊断最小集：意图状态/轮终态分类/绑定登记态/重启对账注记）。
	binding := map[string]any{"session_id": it.SessionID, "registered": false}
	if owner, ok := s.cfg.Collab.SessionOwnerSpace(it.SessionID); ok && owner == spaceID {
		binding["registered"] = true
	}
	body["binding"] = binding
	// 轮终 join：账本轮事实按 session+seq 定位（与 service.findLedgerTurn 同款键
	// ——pending 意图 TaskID 未在位（受理→投递窗/未投递）同样可查轮事实；诊断
	// join 不依赖 TaskID 在位，W08-02）。
	var (
		turnFound bool
		review    bool
	)
	if it.SessionID != "" && s.cfg.Ledger != nil {
		if e, ok := ledgerTurnOf(s.cfg.Ledger, it.SessionID, it.TurnSeq); ok {
			turnFound = true
			turn := map[string]any{"task_id": e.Task.ID, "state": e.State}
			if e.Result != nil {
				turn["status"] = e.Result.Status
				turn["error"] = e.Result.Error
				turn["final_answer"] = e.Result.FinalAnswer
				review = it.Status.InFlight() && e.State == ledger.StateFinished &&
					e.Result.Status == protocol.ResultNeedsReview
			}
			body["turn"] = turn
		}
	}
	if it.TaskID != "" && !turnFound {
		body["task_gone"] = true // 任务已过保留期清理（历史缺口注记，W08-02）
	}
	if review {
		body["awaiting_review"] = true // 在途+轮终不确定=留待核对（可见化）
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
	// 保留前缀门（终审必修5 全入口收口）：消息 request_id 同享全局回执表。先于
	// 一切校验与事务零副作用。
	if s.requestIDReserved(w, req.RequestID) {
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

// spaceSugarRequest POST /api/v1/spaces/{id}/budget|policy|finalize|archive 请求体
// （W04-03 sugar）。字段与 ControlRequest 对应动作参数一一对应（同族映射，无旁路
// 语义）；request_id 缺省自动生成（同 create/dispatch/messages 口径——自动生成不
// 承担重放回执，重试安全的调用方应显式携带）。expected_version 必填（读档乐观锁，
// 与 control/dispatch/messages 同纪）。
type spaceSugarRequest struct {
	RequestID       string         `json:"request_id,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	Budget          *collab.Budget `json:"budget,omitempty"`        // budget
	Policy          *collab.Policy `json:"policy,omitempty"`        // policy（字段级合并）
	FinalOutcome    string         `json:"final_outcome,omitempty"` // finalize: done|abandoned
	Reason          string         `json:"reason,omitempty"`        // finalize(abandoned)/budget 原因
}

// spaceSugar 四个主人专属 sugar 端点的共用实现：映射 Control 同族动作（authLocal
// 门已保证主体=主人 local_token；collab 模型层逐动作授权不变——仅主人行照常裁决）。
func (s *Server) spaceSugar(w http.ResponseWriter, r *http.Request, act collab.Action) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceSugarRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {expected_version, ...动作参数} JSON: "+err.Error())
		return
	}
	if req.ExpectedVersion <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request", "expected_version 必填（读档乐观锁）")
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "sugar-" + string(act) + "-" + protocol.NewTraceID() // 自动生成（不承担重放回执）
	}
	// 保留前缀门（终审必修5 sugar 入口收口，codex 三审实证遗漏）：sugar 直调
	// Control 同享全局回执表——T 的 /budget 携 boot-S-drafting 可占住 S 的引导
	// 回执键使其 conflict 卡死（自动生成族 sugar- 前缀非保留，不受门影响）。
	if s.requestIDReserved(w, requestID) {
		return
	}
	res, dup, err := s.cfg.Collab.Control(r.PathValue("id"), collab.ControlRequest{
		RequestID:       requestID,
		Action:          act,
		ExpectedVersion: req.ExpectedVersion,
		Budget:          req.Budget,
		Policy:          req.Policy,
		FinalOutcome:    req.FinalOutcome,
		Reason:          req.Reason,
	}, spaceOwnerSubject())
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	// 控制动作统一 200（语义回执在体：applied=是否实际变更——archive 幂等命中为
	// false、dup=操作 ID 重放命中）。
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// handleSpaceBudget POST /api/v1/spaces/{id}/budget：预算调整（仅主人；记原因、
// 不自动 resume——改预算不恢复运行，与 control set_budget 同语义）。
func (s *Server) handleSpaceBudget(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActSetBudget)
}

// handleSpacePolicy POST /api/v1/spaces/{id}/policy：完成授权/领取约束变更（仅主人；
// 待配置/暂停且无在途——策略版本随变更递增并审计）。
func (s *Server) handleSpacePolicy(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActPolicy)
}

// handleSpaceFinalize POST /api/v1/spaces/{id}/finalize：定版（仅主人；done 仍过
// 策略判据，abandoned 必填原因且无未收束执行）。
func (s *Server) handleSpaceFinalize(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActFinalize)
}

// handleSpaceArchive POST /api/v1/spaces/{id}/archive：归档（仅主人；仅已完成/已放弃、
// 无在途；幂等标记——重复归档零变更 applied=false）。
func (s *Server) handleSpaceArchive(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActArchive)
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
		// S4：成员 configure 填错 session_id（绑定串无内核映射也无受理登记）的派发
		// → 404（会话面事实不存在，非服务端故障）。
		case collab.CodeNotFound, "member_not_found", "session_not_found":
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

// writeSpaceErr collab 错误 → HTTP 投影（RFC §一错误码表；未知码归 500）。映射
// 表单源 collab.HTTPStatus（W07-02 起 hub api 面同表共用，两端同码即同状态）。
func writeSpaceErr(w http.ResponseWriter, err error) {
	var ce *collab.Error
	if !errors.As(err, &ce) {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeErr(w, collab.HTTPStatus(ce.Code), ce.Code, stripCollabPrefix(err.Error()))
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
