// spaces.go 中心空间 REST 面（W07-02，docs/tasks/v3/W07-hub-routing.md「中心空间
// REST 面」：/api/v1/spaces*——create/list/status/control/dispatch/messages/
// records/strategy/candidates + budget/policy/finalize/archive sugar，与 node 面
// 同形同参；同一 collab 模型层逐动作授权零第二套）：
//   - POST /api/v1/spaces                       创建（编排主体仅 external 待领取）
//   - GET  /api/v1/spaces                       列表
//   - GET  /api/v1/spaces/{id}                  详情（全量视图 + 在途投影；scope=hub:<id> 注记）
//   - POST /api/v1/spaces/{id}/control          控制骨架（逐动作授权表；resume 在途确认门；
//     set_phase 经 strategy 预检后落账）
//   - POST /api/v1/spaces/{id}/dispatches       指名派发（远端 adapter：版本门+绑定登记+
//     既有 session Task 通道，202 受理）
//   - GET  /api/v1/spaces/{id}/dispatches/{request_id}  派发/意图查询（轮终投影）
//   - POST /api/v1/spaces/{id}/intents/{request_id}/resolve 留待核对人工裁决收束
//     （W09-B，主人专属豁免⑯同族：accept=核验通过收束/reject=否决收束）
//   - POST /api/v1/spaces/{id}/messages         发言（零唤醒记账+消息账本）
//   - GET  /api/v1/spaces/{id}/messages?since=  增量读（游标只读不唤醒，N01）
//   - POST /api/v1/spaces/{id}/budget           预算 sugar（主人专属→Control set_budget）
//   - POST /api/v1/spaces/{id}/policy           策略 sugar（主人专属→Control policy）
//   - POST /api/v1/spaces/{id}/finalize         定版 sugar（主人专属→Control finalize）
//   - POST /api/v1/spaces/{id}/archive          归档 sugar（主人专属→Control archive）
//   - GET  /api/v1/spaces/{id}/strategy         策略投影只读（W05-04 同面）
//   - POST /api/v1/spaces/{id}/records          策略循环记录登记（candidate/review/issue/evidence）
//   - GET  /api/v1/spaces/{id}/records?since=   记录增量读
//   - POST /api/v1/spaces/{id}/candidates/{cid}/finalize  候选定版（仅主人，豁免⑯）
//   - POST /api/v1/spaces/{id}/candidates/{cid}/dismiss   候选否决（仅主人，豁免⑯）
//
// 鉴权分层（hub 侧口径，W07-02）：管理会话（admin cookie）→ collab 主人主体
// （owner:admin——hub 的「人工管理会话」即 collab authz 注释的主人形态）；PAT →
// 编排主体（orchestrate:pat:<令牌名>——服务端鉴权层解析的主体，不采信请求自报；
// 既有 s.auth 门放行后按凭据族映射，mcpaccess 门是 node 本地面 hub 无此形态）。
// 逐动作授权在 collab 模型层（driver/orchestrate 形态）；主人专属动作
// set_budget/policy/finalize/archive/candidates finalize|dismiss 对编排主体 403
// （豁免⑯同规）。主体由服务端鉴权层解析后传入 collab（AGENTS 审计纪律）。
//
// 错误投影：collab Error.Code → HTTP（collab.HTTPStatus 单源，node 面同表）；
// 派发面 spaces.Error（路由/登记/会话面）单表映射见 writeSpaceDispatchErr；存储
// 未装配 503 spaces_unavailable（诚实降级）。
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/spaces"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/strategy"
)

// spaceReady 中心空间服务装配守卫（未装配 503 诚实降级）。
func (s *Server) spaceReady(w http.ResponseWriter) bool {
	if s.cfg.Spaces == nil {
		writeErr(w, http.StatusServiceUnavailable, "spaces_unavailable",
			"中心空间服务未装配（本 hub 未启用中心空间协作；升级或装配后可用）")
		return false
	}
	return true
}

// spaceStrategyReady 策略面装配守卫（引擎未装配 503 诚实降级，不假空态不假放行）。
func (s *Server) spaceStrategyReady(w http.ResponseWriter) bool {
	if s.cfg.Strategy == nil {
		writeErr(w, http.StatusServiceUnavailable, "strategy_unavailable",
			"策略引擎未装配：策略投影与记录面不可用（本 hub 未装配策略引擎；升级或装配后可用）")
		return false
	}
	return true
}

// hubOwnerSubject 管理会话主人主体（admin cookie 已过 s.adminSession 验证）。
func hubOwnerSubject() collab.Subject {
	return collab.Subject{Name: adminIdentity, Class: collab.ClassOwner}
}

// hubOrchestrateSubject PAT 编排主体（s.auth 已验证令牌；主体名 = 服务端解析的
// authPrincipal "pat:<令牌名>"——审计主体非请求自报）。共享凭据只能识别「该授权
// 主体」，不承诺同凭据调用者间隔离（任务书）。
func hubOrchestrateSubject(principal string) collab.Subject {
	if principal == "" {
		principal = "pat"
	}
	return collab.Subject{Name: principal, Class: collab.ClassOrchestrate}
}

// authSpaceSubject 空间写面统一门：会话/PAT 二分（401）→ 主体注入（owner/
// orchestrate）。逐动作授权由 collab 模型层按主体+任期判定（403 forbidden）。
// PAT 是已被授予的编排凭据（与 POST /tasks 同门同语义），无 node 侧 mcpaccess
// 二次门（那是本机主人对 prime_token 的开关，hub PAT 授予即编排凭据）。
func (s *Server) authSpaceSubject(next func(http.ResponseWriter, *http.Request, collab.Subject)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil {
				if name, ok := s.tokens.verify(strings.TrimPrefix(h, "Bearer ")); ok {
					next(w, r.WithContext(withAuthPrincipal(r.Context(), "pat:"+name)), hubOrchestrateSubject("pat:"+name))
					return
				}
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized", "令牌无效或已撤销")
			return
		}
		if rr := s.adminSession(w, r); rr != nil {
			next(w, rr, hubOwnerSubject())
		}
	}
}

// authSpaceOwner 主人专属门（sugar 四件 + 候选定版/否决，豁免⑯人工专属保留面）：
// 编排凭据（PAT）显式 403（认证已过、授权拒绝——错误呈现纪律，让调用方看到
// 「为什么」）；admin 会话放行。与逐动作授权「仅主人」行同界。
func (s *Server) authSpaceOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil {
				if _, ok := s.tokens.verify(strings.TrimPrefix(h, "Bearer ")); ok {
					writeErr(w, http.StatusForbidden, collab.CodeForbidden,
						"该动作为主人专属（预算/策略/定版/归档/候选定版否决不开放编排面——agent 不能代调或伪造人已授权）：请由主人登录 hub 控制台执行")
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

// handleSpaceCreate POST /api/v1/spaces：创建空间（scope 固定 hub:<id>——
// collab.HubScope 词彙单源，W07-01 定案）。request_id 缺省自动生成；显式提供时
// 同键同文 200+dup 返回原空间、异文 409 request_conflict。编排主体仅可建 external
// 待领取空间且完成授权强制 human_only（collab 模型层强制）。
func (s *Server) handleSpaceCreate(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req collab.CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 CreateRequest JSON: "+err.Error())
		return
	}
	if collab.RequestIDReserved(req.RequestID) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
		return
	}
	sp, dup, err := s.cfg.Spaces.CreateSpace(req, subj)
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"space": sp, "scope": spaces.Scope(sp.ID), "dup": dup})
}

// handleSpaceList GET /api/v1/spaces：列表（创建序；scope 注记）。归档过滤
// （W08-02）：默认隐藏已归档空间（归档只是列表收纳——详情/记录/消息读路径不
// 关闭仍可回查）；`?include_archived=true` 显式包含（控制台页签计数依赖全量
// 视图，经本参数显式取全量）。应答附 `archived_hidden`（本次被过滤隐藏的归档
// 空间数）——消费者不把「列表没有」误读成「不存在」。
func (s *Server) handleSpaceList(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	all := s.cfg.Spaces.Spaces()
	spacesList := make([]collab.Space, 0, len(all))
	hidden := 0
	for _, sp := range all {
		if sp.Archived && !includeArchived {
			hidden++
			continue
		}
		spacesList = append(spacesList, sp)
	}
	out := make([]map[string]any, 0, len(spacesList))
	for _, sp := range spacesList {
		out = append(out, map[string]any{"space": sp, "scope": spaces.Scope(sp.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spaces": spacesList, "scopes": scopeMap(spacesList), "total": len(spacesList),
		"archived_hidden": hidden,
	})
}

// scopeMap 空间 ID → 中心 scope 词形（列表注记；collab.HubScope 单源构造）。
func scopeMap(list []collab.Space) map[string]string {
	m := make(map[string]string, len(list))
	for _, sp := range list {
		m[sp.ID] = spaces.Scope(sp.ID)
	}
	return m
}

// spaceDiag 空间诊断投影（W08-02 执行/问题诊断面，P09 数据源；与 node 面
// internal/node/localapi spaceDiag 同形同语义——两端各自 join 本端轮终事实源）。
// 全部为既有事实的只读 join 投影，零影子状态。awaiting_review_intents（W09-B
// 留待核对释放入口数据源）为 accepted 形态的留待核对意图清单（可裁决集）——
// awaiting_review 计数还含 pending 崩溃窗形态（不可裁决），两者口径差如实保留。
type spaceDiag struct {
	InFlight              int                `json:"in_flight"`                         // 在途/排队意图数（= space.in_flight 同源）
	AwaitingReview        int                `json:"awaiting_review,omitempty"`         // 留待核对未收敛数（轮终不确定 + 执行证据缺失两类）
	AwaitingReviewIntents []spaceAwaitIntent `json:"awaiting_review_intents,omitempty"` // 可裁决留待核对意图（accepted 形态；hub 管理面数据源）
}

// spaceAwaitIntent 可裁决留待核对意图行（服务端 join 投影；reason：
// needs_review=轮终不确定、task_gone=证据缺失（任务已过保留期清理））。
type spaceAwaitIntent struct {
	RequestID string `json:"request_id"`
	Member    string `json:"member,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	Reason    string `json:"reason"`
}

// hubSpaceDiagOf 计算中心空间诊断投影（与 spaces 服务重启对账分类同源）：
//   - accepted：按意图登记的任务行实物判定——needs_review 终态（轮终不确定）、
//     任务行已过保留期清理（执行证据缺失，turnFactMissing 同源）都按纪律留待
//     核对未收敛即计入；
//   - pending 崩溃窗（轮行已落库、Attach 未落）：按 request_id 锚精确取本意图
//     轮行（镜像 intentTurnFact/turnFactMissing——codex 必修3），轮行在案而任务
//     已清理=证据缺失留待核对；任务 needs_review 终态同规；无轮行=未投递，
//     不计入（取消收束语义另行处理，不冒认）。
func (s *Server) hubSpaceDiagOf(id string, view collab.SpaceView) spaceDiag {
	d := spaceDiag{InFlight: view.InFlight}
	if d.InFlight == 0 || s.cfg.Core == nil {
		return d
	}
	for _, it := range s.cfg.Spaces.Intents(id) {
		switch {
		case it.Status == collab.IntentAccepted && it.TaskID != "":
			t, ok := s.cfg.Core.GetTask(it.TaskID)
			if !ok || t.Status == protocol.StatusNeedsReview {
				d.AwaitingReview++
				reason := "needs_review"
				if !ok {
					reason = "task_gone" // 任务行已过保留期清理（证据缺失）
				}
				d.AwaitingReviewIntents = append(d.AwaitingReviewIntents, spaceAwaitIntent{
					RequestID: it.RequestID, Member: it.Member, TaskID: it.TaskID, Reason: reason,
				})
			}
		case it.Status == collab.IntentPending && it.SessionID != "":
			tr, ok := s.cfg.Core.SessionTurnByRequest(it.SessionID, it.RequestID)
			if !ok || tr.Seq != it.TurnSeq {
				continue // 未投递（无轮行）：不算留待核对
			}
			t, ok := s.cfg.Core.GetTask(tr.TaskID)
			if !ok || t.Status == protocol.StatusNeedsReview {
				d.AwaitingReview++ // pending 崩溃窗形态计入计数；不在可裁决清单（resolve 准入仅 accepted）
			}
		}
	}
	return d
}

// handleSpaceGet GET /api/v1/spaces/{id}：详情（视图含成员/负责人/预算用量/
// 策略/在途投影；scope=hub:<id> 注记）。已归档空间照常可查（W08-02：归档只隐藏
// 列表，详情读路径不关闭）；应答附 `diag` 诊断投影（在途与留待核对可见）。
func (s *Server) handleSpaceGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Spaces.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": view, "scope": spaces.Scope(view.ID), "diag": s.hubSpaceDiagOf(id, view)})
}

// handleSpaceControl POST /api/v1/spaces/{id}/control：控制骨架（逐动作授权表，
// collab 模型层判定——与 node 面同一模型零第二套）。request_id 必填 + expected_version
// 必填；takeover 另有 expected_term CAS。恢复保护：存在在途/排队意图时 resume 须
// 携 confirm=true 显式确认。set_phase 先经 strategy.ValidatePhaseTransition 无副作用
// 预检后放行 Control 落账（判定在 strategy、落账在 collab）；start 实际生效后
// BootstrapCycle 引导 + ActSetPhase(drafting) 同通道落账。重放判定先于预检
// （ServedIdentical——回执先于预检，阶段推进后原请求重放不被新状态 409 卡死）。
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
	if collab.RequestIDReserved(req.RequestID) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
		return
	}
	// 成员解绑解除面（codex 必修D②）：configure_members 前取旧成员视图——应用后
	// diff 被移除的绑定并下发 node 解除登记（普通轮恢复放行；失败入 Service 重试
	// 面）。零副作用预取（只读）。
	var preMembers []collab.Member
	preGens := map[string]uint64{} // 移除前各会话的绑定代次快照（R2①：原子快照解绑）
	if req.Action == collab.ActConfigureMembers {
		if pre, ok := s.cfg.Spaces.SpaceView(id); ok {
			preMembers = pre.Members
		}
		for _, pm := range preMembers {
			if pm.SessionID != "" {
				preGens[pm.SessionID] = s.cfg.Spaces.BoundGenOf(pm.SessionID)
			}
		}
	}
	var setPhaseFrom strategy.Phase
	if req.Action == collab.ActSetPhase && !s.cfg.Spaces.ServedIdentical(id, req) {
		from, ok := s.setPhasePrecheck(w, id, req)
		if !ok {
			return
		}
		setPhaseFrom = from
	}
	if req.Action == collab.ActResume && !req.Confirm && s.cfg.Spaces.InFlightCount(id) > 0 {
		writeErr(w, http.StatusConflict, collab.CodeExecutionInFlight,
			"空间存在在途/排队执行意图：恢复须待收束，或携 confirm:true 显式确认继续（继续将按现有队列依次派发；预算/负责人/版本变化见空间详情）")
		return
	}
	// 控制动作经调度互斥面（codex 复审必修B-残②）：pause/resume/成员/完成类
	// 变更与投递推进/收敛停队同锁串行。
	res, dup, err := s.cfg.Spaces.ControlSpace(id, req, subj)
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	// 解除登记（codex 必修D②）：成员移除/换绑会话在 collab 事务内释放归属成功
	// 后，向 node 下发解除（hub 注册表已无归属才发——换绑他空间的会话由新空间
	// 派发时 bind 幂等覆盖）。解除失败由 Service.UnbindSession 重试面兜底。
	if req.Action == collab.ActConfigureMembers && !dup && res.Applied && len(preMembers) > 0 {
		kept := map[string]bool{}
		for _, m := range res.Space.Members {
			kept[m.Name+"\x00"+m.SessionID] = true
		}
		for _, om := range preMembers {
			if om.SessionID == "" || kept[om.Name+"\x00"+om.SessionID] {
				continue
			}
			if _, stillBound := s.cfg.Spaces.SessionOwnerSpace(om.SessionID); stillBound {
				continue // 已归属其他空间：不解除（新空间派发 bind 覆盖）
			}
			if sv, ok := s.cfg.Core.GetSession(om.SessionID); ok {
				// 携**移除前快照代次**解除（codex 复审轮 3 必修 R2①：归属检查
				// 与代次读取同一快照——并发新空间 bind 推进代次后，node 代次不符
				// 自然 noop，快照代次解不删新绑定）。gen=0（重启后代次未知）诚实
				// 跳过：node 若有残留绑定 fail-closed（普通轮拒收）+ 后续 bind
				// 幂等覆盖自愈——探针追逐当前值的窗不开放。
				if preGen := preGens[om.SessionID]; preGen != 0 {
					s.cfg.Spaces.UnbindSessionGen(sv.NodeID, om.SessionID, preGen)
				} else {
					log.Printf("[hub] spaces: 移除会话绑定代次未知（重启后未重建），跳过解除 session=%s（node 残留 fail-closed 自愈）", om.SessionID)
				}
			}
		}
	}
	// resume 放行（node NotifySpaceResumed 同规）：恢复后已受理排队意图继续投递。
	if req.Action == collab.ActResume && !dup && res.Applied {
		s.cfg.Spaces.KickSpace(id)
	}
	// 策略引擎跟随落账（W05-03 分离边界，node 面同构）：start 生效 → 引导；
	// set_phase 生效 → 引擎留痕。dup 命中零回放（重放不回放留痕）。
	if s.cfg.Strategy != nil && !dup {
		switch req.Action {
		case collab.ActStart:
			s.bootstrapCycleAfterStart(id, subj)
		case collab.ActSetPhase:
			to := strategy.Phase(strings.TrimSpace(req.Phase))
			if setPhaseFrom.Valid() && to.Valid() && setPhaseFrom != to {
				if err := s.cfg.Strategy.SetPhase(id, setPhaseFrom, to); err != nil {
					log.Printf("[hub] set_phase 引擎留痕失败 space=%s %s→%s: %v", id, setPhaseFrom, to, err)
				}
			}
		}
	}
	if s.cfg.Strategy != nil && dup && req.Action == collab.ActStart {
		if view, ok := s.cfg.Spaces.SpaceView(id); ok && !strategy.Phase(strings.TrimSpace(view.Phase)).Valid() {
			s.bootstrapCycleAfterStart(id, subj)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// setPhasePrecheck set_phase 端点面预检（W05-04 同面移植；无副作用，拒绝先于一切
// 事务——不落回执、不占 request_id）：非法词形 400 / 不合法转换 409 / completed
// 终态直写门 / 同值幂等直通 / 404 / 引擎未装配 503。
func (s *Server) setPhasePrecheck(w http.ResponseWriter, spaceID string, req collab.ControlRequest) (strategy.Phase, bool) {
	if s.cfg.Strategy == nil {
		writeErr(w, http.StatusServiceUnavailable, "strategy_unavailable",
			"策略引擎未装配：set_phase 须经策略判定面预检后落账（本 hub 未装配策略引擎；升级或装配后可用）")
		return "", false
	}
	to := strategy.Phase(strings.TrimSpace(req.Phase))
	if !to.Valid() {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"phase 词形非法："+humanPhaseError(req.Phase)+"；合法词形 drafting|frozen|review|rework|rereview|paused|completed（须为表内转换，先读 GET strategy 看当前阶段）")
		return "", false
	}
	view, ok := s.cfg.Spaces.SpaceView(spaceID)
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
		return from, true
	}
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

// bootstrapCycleAfterStart start 实际生效后的循环引导（幂等；引导目标经同通道
// ActSetPhase 落账）。落账失败如实日志不回滚 start。
func (s *Server) bootstrapCycleAfterStart(spaceID string, subj collab.Subject) {
	view, ok := s.cfg.Spaces.SpaceView(spaceID)
	if !ok {
		return
	}
	next, err := s.cfg.Strategy.BootstrapCycle(view)
	if err != nil {
		log.Printf("[hub] start 后循环引导失败 space=%s: %v", spaceID, err)
		return
	}
	if string(next) == view.Phase {
		return
	}
	_, _, err = s.cfg.Spaces.ControlSpace(spaceID, collab.ControlRequest{
		RequestID:       "boot-" + spaceID + "-" + string(next),
		Action:          collab.ActSetPhase,
		ExpectedVersion: view.Version,
		Term:            view.Leader.Term,
		Phase:           string(next),
		Note:            "start 后循环阶段引导",
	}, subj)
	if err != nil {
		log.Printf("[hub] start 后阶段引导落账失败 space=%s →%s: %v", spaceID, next, err)
	}
}

// humanPhaseError 非法词形人话（与 node 面同文）。
func humanPhaseError(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "phase 必填（目标循环阶段）"
	}
	return strconv.Quote(trimmed) + " 不是合法阶段词"
}

// stripStrategyPrefix 错误文案去包前缀。
func stripStrategyPrefix(msg string) string {
	if p := "strategy: "; len(msg) > len(p) && msg[:len(p)] == p {
		return msg[len(p):]
	}
	return msg
}

// spaceDispatchRequest POST /api/v1/spaces/{id}/dispatches 请求体（与 node 面同形）。
type spaceDispatchRequest struct {
	Member          string `json:"member"`
	RequestID       string `json:"request_id,omitempty"`
	ExpectedVersion int64  `json:"expected_version"`
	Term            uint64 `json:"term,omitempty"`
	Note            string `json:"note,omitempty"`
}

// handleSpaceDispatch POST /api/v1/spaces/{id}/dispatches：指名派发（远端 adapter
// 受理，202 异步——意图持久+预算扣减即时成立，轮经既有 session Task 通道投递，
// 不阻塞等待）。同 request_id 重放命中回执零重复执行零重复扣减（N02）。成员词形
// = 开放目录形态 nodeID/agentName（与 configure_members 配置一致）。
func (s *Server) handleSpaceDispatch(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceDispatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {member, expected_version, note?} JSON: "+err.Error())
		return
	}
	if collab.RequestIDReserved(req.RequestID) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
		return
	}
	if strings.TrimSpace(req.Member) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "member 必填（指名成员派发；成员词形 nodeID/agentName）")
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
	intent, dup, err := s.cfg.Spaces.Dispatch(spaces.DispatchRequest{
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

// handleSpaceDispatchGet GET /api/v1/spaces/{id}/dispatches/{request_id}：意图查询
// （轮终投影：意图 + hub 会话轮行状态 join——pending/awaiting_node/running/
// done/unverified/failed/cancelled + 意图状态）。W08-02 诊断注记（与 node 面
// 同形同语义，全为既有事实投影零影子状态）：
//   - `task_gone`：关联任务已过保留期清理（轮行 TaskGone 投影）——历史缺口如实
//     可见，轮次注记仍在（会话轮账本不随任务清理失联）；
//   - `awaiting_review`：留待核对未收敛（与 spaces 服务重启对账分类同源）——
//     accepted 意图的任务行 needs_review 终态（轮终不确定）、任务行已过保留期
//     清理（执行证据缺失），或 pending 崩溃窗轮行在案而任务已清理（必修3，
//     intentTurnFact/turnFactMissing 镜像——按 request_id 锚精确取本意图轮行，
//     hub 轮行 RequestID 单调不复用无撞号面）；
//   - `binding`：成员会话绑定登记态（session_id + generation——中心绑定代次投影，
//     gen>0=已登记；0=未登记/已解除）。
func (s *Server) handleSpaceDispatchGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	id := r.PathValue("id")
	it, ok := s.cfg.Spaces.IntentByRequest(id, r.PathValue("request_id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "dispatch_not_found", "意图不存在（request_id 未受理或已滚动出空间）: "+r.PathValue("request_id"))
		return
	}
	body := map[string]any{"dispatch": it}
	// 绑定登记态（诊断最小集：意图状态/轮终态分类/绑定登记态/重启对账注记）。
	gen := uint64(0)
	if it.SessionID != "" {
		gen = s.cfg.Spaces.BoundGenOf(it.SessionID)
	}
	body["binding"] = map[string]any{"session_id": it.SessionID, "generation": gen}
	// 任务事实注记（与 hubSpaceDiagOf 同源判定）：
	//   - accepted：按意图登记的任务行实物——任务行已过保留期清理=历史缺口
	//    （task_gone）且执行证据缺失（留待核对）；needs_review 终态=轮终不确定
	//    （留待核对，不自动收敛重跑）；
	//   - pending 崩溃窗（轮行已落库、Attach 未落，codex 必修3）：按 request_id 锚
	//    精确取本意图轮行（镜像 spaces 服务 intentTurnFact/turnFactMissing）——
	//    轮行在案而任务已清理=证据缺失留待核对，如实注记不冒认。
	if it.Status == collab.IntentAccepted && it.TaskID != "" && s.cfg.Core != nil {
		if t, ok := s.cfg.Core.GetTask(it.TaskID); !ok {
			body["task_gone"] = true       // 任务已过保留期清理（历史缺口注记）
			body["awaiting_review"] = true // 执行证据缺失=留待核对
		} else if t.Status == protocol.StatusNeedsReview {
			body["awaiting_review"] = true // 轮终不确定=留待核对（不自动收敛重跑）
		}
	}
	if it.Status == collab.IntentPending && it.SessionID != "" && s.cfg.Core != nil {
		if tr, ok := s.cfg.Core.SessionTurnByRequest(it.SessionID, it.RequestID); ok && tr.Seq == it.TurnSeq {
			if _, ok := s.cfg.Core.GetTask(tr.TaskID); !ok {
				body["awaiting_review"] = true // 轮行在案+任务已清理=证据缺失留待核对
			}
		}
	}
	if it.SessionID != "" && s.cfg.Core != nil {
		if sv, ok := s.cfg.Core.GetSession(it.SessionID); ok {
			for _, tv := range sv.Turns {
				if tv.Seq == it.TurnSeq {
					body["turn"] = map[string]any{
						"session_id": it.SessionID,
						"turn_seq":   tv.Seq,
						"task_id":    tv.TaskID,
						"state":      tv.Status,
						"error":      tv.Error,
						"task_gone":  tv.TaskGone, // 任务已过保留期清理（历史缺口注记）
					}
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// spaceIntentResolveRequest POST /api/v1/spaces/{id}/intents/{request_id}/resolve
// 请求体（W09-B 留待核对人工裁决）：verdict 必填 accept|reject；note 可选（裁决
// 说明，落意图注记）。无身份字段——主体恒由服务端鉴权层解析（审计红线）。
type spaceIntentResolveRequest struct {
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
}

// authSpaceResolveOwner 留待核对人工裁决主人专属门（豁免⑯同族——人工核验裁决
// 不开放编排面：agent 不能代人工核验、更不能自证不确定轮次的结果；与 budget/
// policy/finalize/archive/candidates finalize|dismiss 同界。文案为裁决面专属词形，
// 不复用 authSpaceOwner 既有文案——既有端点应答零漂移）。有效 PAT 显式 403
// （认证已过、授权拒绝——错误呈现纪律），admin 会话放行并以服务端主体
// （hubOwnerSubject=owner:admin）下传。
func (s *Server) authSpaceResolveOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if s.tokens != nil {
				if _, ok := s.tokens.verify(strings.TrimPrefix(h, "Bearer ")); ok {
					writeErr(w, http.StatusForbidden, collab.CodeForbidden,
						"该裁决为主人专属人工动作（留待核对意图的人工核验/否决收束不开放编排面——agent 不能代人工核验或自证结果）：请由主人登录 hub 控制台执行")
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

// handleSpaceIntentResolve POST /api/v1/spaces/{id}/intents/{request_id}/resolve：
// 留待核对意图人工裁决收束（W09-B，采证 O3 释放入口）。accept=人工核验通过收束
// （复用 spaces 服务 ConvergeIntent 既有收敛原语——禁第二套收敛实现）；
// reject=人工否决收束（accepted→cancelled+注记，不重发不转投）。准入边界与幂等
// （dup 门同五入口口径）在 spaces.ResolveIntent 单源；错误投影同派发面
// （invalid 400 / execution_in_flight·illegal_transition·request_conflict 409 /
// forbidden 403）。响应 `{"dispatch": intent, "dup": bool}` 恒 200。
func (s *Server) handleSpaceIntentResolve(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceIntentResolveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {verdict, note?} JSON: "+err.Error())
		return
	}
	it, dup, err := s.cfg.Spaces.ResolveIntent(r.PathValue("id"), r.PathValue("request_id"), req.Verdict, req.Note, hubOwnerSubject())
	if err != nil {
		writeSpaceDispatchErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dispatch": it, "dup": dup})
}

// spaceMessageRequest POST /api/v1/spaces/{id}/messages 请求体（与 node 面同形）。
type spaceMessageRequest struct {
	Text            string `json:"text"`
	Member          string `json:"member,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	ExpectedVersion int64  `json:"expected_version"`
}

// handleSpaceMessagePost POST /api/v1/spaces/{id}/messages：发言（零唤醒纪律 N01
// ——只落消息账本与 message_count 预算，绝不触发任何执行）。发言主体（node 面
// 同规）：指名成员=以该成员身份落时间线（成员须在空间内，owner/orchestrate 均可
// 代转）；不指名=主人发言（仅 owner——编排主体不能冒充主人，须指名成员）。
func (s *Server) handleSpaceMessagePost(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) {
		return
	}
	var req spaceMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 {text, member?, expected_version} JSON: "+err.Error())
		return
	}
	if collab.RequestIDReserved(req.RequestID) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
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
	rec, dup, err := s.cfg.Spaces.RecordMessage(r.PathValue("id"), collab.MessageRequest{
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
	if _, ok := s.cfg.Spaces.SpaceView(r.PathValue("id")); !ok {
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
	msgs, pruned := s.cfg.Spaces.SpaceMessages(r.PathValue("id"), since)
	if msgs == nil {
		msgs = []collab.SpaceMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "pruned_before_seq": pruned})
}

// spaceSugarRequest POST /api/v1/spaces/{id}/budget|policy|finalize|archive 请求体
// （与 node 面同形：字段即 ControlRequest 对应动作参数，无旁路语义）。
type spaceSugarRequest struct {
	RequestID       string         `json:"request_id,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	Budget          *collab.Budget `json:"budget,omitempty"`
	Policy          *collab.Policy `json:"policy,omitempty"`
	FinalOutcome    string         `json:"final_outcome,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}

// spaceSugar 四个主人专属 sugar 端点共用实现（authSpaceOwner 门已保证主体=主人
// 管理会话；collab 模型层逐动作授权不变）。sugar 只做同族动作映射。
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
		requestID = "sugar-" + string(act) + "-" + protocol.NewTraceID()
	}
	if collab.RequestIDReserved(requestID) {
		writeErr(w, http.StatusBadRequest, "reserved_request_id", collab.ReservedRequestIDMessage)
		return
	}
	res, dup, err := s.cfg.Spaces.ControlSpace(r.PathValue("id"), collab.ControlRequest{
		RequestID:       requestID,
		Action:          act,
		ExpectedVersion: req.ExpectedVersion,
		Budget:          req.Budget,
		Policy:          req.Policy,
		FinalOutcome:    req.FinalOutcome,
		Reason:          req.Reason,
	}, hubOwnerSubject())
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// handleSpaceBudget POST /api/v1/spaces/{id}/budget：预算调整（仅主人；不自动
// resume——改预算不恢复运行）。
func (s *Server) handleSpaceBudget(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActSetBudget)
}

// handleSpacePolicy POST /api/v1/spaces/{id}/policy：完成授权/领取约束变更（仅主人）。
func (s *Server) handleSpacePolicy(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActPolicy)
}

// handleSpaceFinalize POST /api/v1/spaces/{id}/finalize：定版（仅主人；done 仍过
// 策略判据，abandoned 必填原因且无未收束执行）。
func (s *Server) handleSpaceFinalize(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActFinalize)
}

// handleSpaceArchive POST /api/v1/spaces/{id}/archive：归档（仅主人；幂等标记）。
func (s *Server) handleSpaceArchive(w http.ResponseWriter, r *http.Request) {
	s.spaceSugar(w, r, collab.ActArchive)
}

// writeSpaceErr collab 错误 → HTTP 投影（映射单源 collab.HTTPStatus，node 面同表；
// 未知码归 500）。
func writeSpaceErr(w http.ResponseWriter, err error) {
	var ce *collab.Error
	if !errors.As(err, &ce) {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeErr(w, collab.HTTPStatus(ce.Code), ce.Code, stripCollabPrefix(err.Error()))
}

// stripCollabPrefix 错误文案去包前缀（collab: 已由错误码承担）。
func stripCollabPrefix(msg string) string {
	if p := "collab: "; len(msg) > len(p) && msg[:len(p)] == p {
		return msg[len(p):]
	}
	return msg
}

// writeSpaceDispatchErr 派发面错误投影：collab 业务错误（模型层拒绝）与
// spaces.Error（路由/登记/会话面）两源；能力版本门（core.ErrSpacesUnsupported）
// 显式 409 spaces_unsupported（业务拒绝可补齐——升级后重试，非服务端故障）。
func writeSpaceDispatchErr(w http.ResponseWriter, err error) {
	var se *spaces.Error
	if errors.As(err, &se) {
		code := http.StatusInternalServerError
		switch se.Code {
		case "invalid":
			code = http.StatusBadRequest
		case spaces.CodeAgentNotExposed, spaces.CodeSessionGone:
			code = http.StatusNotFound
		case spaces.CodeSessionClosed, spaces.CodeBindingMismatch, spaces.CodeTurnFailed:
			code = http.StatusConflict
		case spaces.CodeNodeOffline, spaces.CodeBindTimeout, spaces.CodeBindRejected:
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, se.Code, se.Msg)
		return
	}
	if errors.Is(err, core.ErrSpacesUnsupported) {
		writeErr(w, http.StatusConflict, protocol.CodeSpacesUnsupported,
			"目标 node 未声明空间协作能力或版本不受支持：请核对双方版本（升级 node 或 hub）后重试")
		return
	}
	writeSpaceErr(w, err)
}

// ---------- 策略面（W05-04 同面移植） ----------

// spaceRecordRequest POST /api/v1/spaces/{id}/records 请求体（与 node 面同形：
// kind 判别联合；字段按既有记录方法入参一一对应，端点层只做装配不做解释）。
type spaceRecordRequest struct {
	Kind string `json:"kind"`
	// ---- candidate ----
	Member string `json:"member,omitempty"`
	Body   string `json:"body,omitempty"`
	Round  *int64 `json:"round,omitempty"`
	// ---- review（op 判别）----
	Op string `json:"op,omitempty"`
	// ---- review/revision ----
	Baseline      string                 `json:"baseline,omitempty"`
	ContentSHA256 string                 `json:"content_sha256,omitempty"`
	PatchSHA256   string                 `json:"patch_sha256,omitempty"`
	Author        string                 `json:"author,omitempty"`
	AuthorRound   *int64                 `json:"author_round,omitempty"`
	Dependencies  []strategy.ArtifactRef `json:"dependencies,omitempty"`
	// ---- review/rereview ----
	IssueID     string `json:"issue_id,omitempty"`
	Reviewer    string `json:"reviewer,omitempty"`
	AgainstHash string `json:"against_hash,omitempty"`
	Result      string `json:"result,omitempty"`
	VersionHash string `json:"version_hash,omitempty"`
	// ---- review/clean_review·reply·通用备注 ----
	Note string `json:"note,omitempty"`
	// ---- issue ----
	Severity     string `json:"severity,omitempty"`
	File         string `json:"file,omitempty"`
	Line         *int   `json:"line,omitempty"`
	Title        string `json:"title,omitempty"`
	FoundInHash  string `json:"found_in_version,omitempty"`
	FoundInRound *int64 `json:"found_in_round,omitempty"`
	// ---- evidence ----
	Version  string `json:"version,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Source   string `json:"source,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

// recordKindError 记录面错误 → HTTP（400 参数非法 / 403 越权 / 404 目标不存在；
// 人话原样透出，错误码用 strategy 域码）。
func recordKindError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	msg := stripStrategyPrefix(err.Error())
	sc := "invalid_argument"
	var ser *strategy.Error
	if errors.As(err, &ser) {
		sc = ser.Code
		switch {
		case ser.Code == "proposer_not_member":
			code = http.StatusForbidden
		case ser.Code == "revision_not_found" || ser.Code == "issue_not_found" || ser.Code == "candidate_not_found":
			code = http.StatusNotFound
		}
	}
	writeErr(w, code, sc, msg)
}

// handleSpaceRecordPost POST /api/v1/spaces/{id}/records：策略循环记录登记（数据
// 写入≠执行——不派发不推进循环不触发任何执行）。verified=true 边界与 node 面同规
// （编排主体一律 403——已证实标记只能由主人登记）。
func (s *Server) handleSpaceRecordPost(w http.ResponseWriter, r *http.Request, subj collab.Subject) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	var req spaceRecordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 records JSON: "+err.Error())
		return
	}
	if req.Verified && subj.Class != collab.ClassOwner {
		writeErr(w, http.StatusForbidden, collab.CodeForbidden,
			"已证实标记（verified=true）只能由主人登记（人工核对证据）：编排主体不得自证——「有权人工核对」与来源回执事实不经编排凭据声明（非自报来源标已证实同样拒绝，来源声明不能自证自身）；人工核对证据请由主人登记")
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Spaces.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	if view.Archived || view.Status.Terminal() {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
			"终态/已归档空间只读：不再收策略循环记录（材料保留可查，登记面向活跃空间开放）")
		return
	}
	round := int64(0)
	if req.Round != nil {
		round = *req.Round
	}
	switch req.Kind {
	case "candidate":
		s.recordCandidate(w, id, view, req, round)
	case "review":
		s.recordReview(w, id, req)
	case "issue":
		s.recordIssue(w, id, req)
	case "evidence":
		s.recordEvidence(w, id, req)
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"kind 须为 candidate|review|issue|evidence（记录四族；数据写入只是事实登记，不等于执行——执行仍须 space dispatch）, 得 "+strconv.Quote(req.Kind)+"")
	}
}

// recordCandidate kind=candidate：成员候选登记（FINAL: 前缀文本通道）。
func (s *Server) recordCandidate(w http.ResponseWriter, spaceID string, view collab.SpaceView, req spaceRecordRequest, round int64) {
	if err := s.cfg.Strategy.ValidateCandidate(view, collab.CandidateRecord{Member: req.Member, Body: req.Body}); err != nil {
		recordKindError(w, err)
		return
	}
	cand, created, err := s.cfg.Strategy.RegisterCandidateVia(spaceID, strategy.ChannelFinalText, strategy.CandidateInput{
		Proposer: req.Member, Body: req.Body, Round: round,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	code := http.StatusCreated
	if !created {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"candidate": cand, "created": created})
}

// recordReview kind=review：op 判别五操作。
func (s *Server) recordReview(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	authorRound := int64(0)
	if req.AuthorRound != nil {
		authorRound = *req.AuthorRound
	}
	switch req.Op {
	case "revision":
		rev, created, err := s.cfg.Strategy.RegisterRevision(spaceID, strategy.RevisionInput{
			Baseline:      req.Baseline,
			ContentSHA256: req.ContentSHA256,
			PatchSHA256:   req.PatchSHA256,
			AuthorRound:   authorRound,
			Author:        req.Author,
			Dependencies:  req.Dependencies,
		})
		if err != nil {
			recordKindError(w, err)
			return
		}
		code := http.StatusCreated
		if !created {
			code = http.StatusOK
		}
		writeJSON(w, code, map[string]any{"revision": rev, "created": created})
	case "rereview":
		if err := s.cfg.Strategy.RecordReReview(spaceID, req.IssueID, strategy.ReReviewInput{
			Reviewer: req.Reviewer, AgainstHash: req.AgainstHash,
			Result: strategy.ReReviewStatus(req.Result), Note: req.Note,
		}); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "rereview"})
	case "clean_review":
		if err := s.cfg.Strategy.RecordCleanReview(spaceID, req.Reviewer, req.AgainstHash, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "clean_review"})
	case "claim_fix":
		if err := s.cfg.Strategy.ClaimFix(spaceID, req.IssueID, req.VersionHash, authorRound, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "claim_fix"})
	case "reply":
		if err := s.cfg.Strategy.ReplyIssue(spaceID, req.IssueID, req.Note); err != nil {
			recordKindError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "reply"})
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"review 记录须携 op=revision|rereview|clean_review|claim_fix|reply, 得 "+strconv.Quote(req.Op)+"")
	}
}

// recordIssue kind=issue：审查问题登记。
func (s *Server) recordIssue(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	round := int64(0)
	if req.FoundInRound != nil {
		round = *req.FoundInRound
	}
	line := 0
	if req.Line != nil {
		line = *req.Line
	}
	is, err := s.cfg.Strategy.RecordIssue(spaceID, strategy.IssueInput{
		Reviewer: req.Reviewer, Severity: strategy.Severity(req.Severity),
		File: req.File, Line: line, Title: req.Title,
		FoundInHash: req.FoundInHash, FoundInRound: round, Note: req.Note,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"issue": is})
}

// recordEvidence kind=evidence：测试执行证据登记。
func (s *Server) recordEvidence(w http.ResponseWriter, spaceID string, req spaceRecordRequest) {
	err := s.cfg.Strategy.RecordEvidence(spaceID, req.Version, strategy.EvidenceLayer(req.Layer), strategy.Evidence{
		Verified: req.Verified, Source: strategy.EvidenceSource(req.Source), Ref: req.Ref,
	})
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "op": "evidence"})
}

// handleSpaceStrategy GET /api/v1/spaces/{id}/strategy：策略投影只读（P04 策略区
// 数据源；无策略数据的空间如实空态不编造）。
func (s *Server) handleSpaceStrategy(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Spaces.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	proj := s.cfg.Strategy.Projection(id, view, time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"strategy": proj})
}

// handleSpaceRecordsGet GET /api/v1/spaces/{id}/records?since=N：记录增量读（type
// 恒带五值封闭 candidate/review/issue/evidence/control——与 node 面同词表）。
func (s *Server) handleSpaceRecordsGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.cfg.Spaces.SpaceView(id); !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, http.StatusBadRequest, "invalid_request", "since 须为非负整数（已见最大记录 seq）")
			return
		}
		since = v
	}
	trail, pruned := s.cfg.Strategy.RecordsSince(id, since)
	records := make([]map[string]any, 0, len(trail))
	for _, ev := range trail {
		m := map[string]any{"seq": ev.Seq, "at": ev.At, "kind": string(ev.Kind), "note": ev.Note}
		if family := ev.Kind.RecordFamily(); family != "" {
			m["type"] = family
		} else {
			m["type"] = "control"
		}
		records = append(records, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "pruned_before_seq": pruned})
}

// handleSpaceCandidateFinalize POST /api/v1/spaces/{id}/candidates/{cid}/finalize：
// 候选定版（仅主人——豁免⑯人工完成授权面）。
func (s *Server) handleSpaceCandidateFinalize(w http.ResponseWriter, r *http.Request) {
	s.spaceCandidateTransition(w, r, strategy.CandidateFinalized)
}

// handleSpaceCandidateDismiss POST /api/v1/spaces/{id}/candidates/{cid}/dismiss：
// 候选否决（仅主人；显式淘汰留痕）。
func (s *Server) handleSpaceCandidateDismiss(w http.ResponseWriter, r *http.Request) {
	s.spaceCandidateTransition(w, r, strategy.CandidateDismissed)
}

// spaceCandidateTransition 两端点共用实现（引擎单方法直调，端点层零第二套校验；
// 定版主体 = 服务端门派生固定标识 admin——请求无 body 无从自报）。
func (s *Server) spaceCandidateTransition(w http.ResponseWriter, r *http.Request, to strategy.CandidateStatus) {
	if !s.spaceReady(w) || !s.spaceStrategyReady(w) {
		return
	}
	id := r.PathValue("id")
	view, ok := s.cfg.Spaces.SpaceView(id)
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+id)
		return
	}
	if view.Archived || view.Status.Terminal() {
		writeErr(w, http.StatusConflict, collab.CodeIllegalTransition,
			"终态/已归档空间只读：候选定版/否决面向活跃空间开放（材料保留可查）")
		return
	}
	cid := r.PathValue("cid")
	var cand strategy.Candidate
	var err error
	if to == strategy.CandidateFinalized {
		cand, err = s.cfg.Strategy.FinalizeCandidate(id, cid, adminIdentity)
	} else {
		cand, err = s.cfg.Strategy.DismissCandidate(id, cid, adminIdentity)
	}
	if err != nil {
		recordKindError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": cand})
}
