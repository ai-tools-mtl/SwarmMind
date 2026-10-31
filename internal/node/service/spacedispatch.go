// spacedispatch.go 空间派发执行侧（W04-02，docs/tasks/v3/W04-spaces.md W04-02 行 +
// docs/tasks/v3/W04-01-rfc.md §八交接面）：持久意图（outbox）→ 串行调度 → 既有会话
// 投递链（复用 execute/runSessionTurn，不另造投递链）→ 轮终收敛分类（ConvergeIntent）
// → 租约过期自动暂停 / 取消停队 / resume 放行 → 重启对账闭环（绝不凭回调自动重跑）。
//
// 调度链路（单空间严格串行；跨空间并行）：
//
//	POST /spaces/{id}/dispatches
//	  → SpaceDispatch：localMu 内派生轮号 → collab.RecordDispatch（意图持久+预算
//	    扣减，N02 重放命中回执零重复扣）→ kick
//	  → advanceSpace（单飞）：运行态 + 租约有效 + 无 accepted 在途 → 取队首 pending
//	    → submitSpaceIntent：localMu 内复核轮号 → AdmissionGuard → 账本入账 →
//	    AttachDispatchTask（pending→accepted）→ execute（既有调度）
//	  → 成员轮终（execute MarkFinished 后）→ spaceConvergeTurn 分类：
//	    completed→converged / failed→按记账码如实 converged / cancelled→cancelled
//	    + 系统暂停停队（不再派发下一棒） / 未知（needs_review）→留待核对不收敛
//	  → kick → 下一意图（串行推进）
//
// 纪律锚点：轮号单源三层——受理期预留（max(账本高水位, 本会话在途意图最大 seq)+1，
// 快速连续受理严格递增不撞号）、投递期以意图登记 seq 入账（一致性校验：账本高水
// 位 ≥ 意图 seq 即真漂移 → 取消收敛+系统暂停，绝不静默跳过）、advance 漂移容错
// （跳过漂移队首取下一棒，保队列流动）；调度前置检查只在「有新增派发意图」时触发
// （HTTP 空闲不判死；accepted-only 不暂停——收敛后无 pending 自然停）；恢复走
// 显式 resume（在途时须 confirm——localapi 门）；重启对账：无实物的 pending（未投
// 递）取消收束、有实物按账本终态分类（与 accepted 同口径），cancelled 收敛补系统
// 暂停停队（对齐活路径），needs_review 留待核对，绝不重跑。
package service

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// spaceSystemSubject 空间执行侧统一主体（collab 授权面：ConvergeIntent/
// AttachDispatchTask 仅 owner/system；执行侧记账属内置服务独立可审计 system 主体）。
func spaceSystemSubject() collab.Subject {
	return collab.Subject{Name: "system", Class: collab.ClassSystem}
}

// SpaceDispatchRequest 空间派发请求（localapi POST /spaces/{id}/dispatches 数据面）。
type SpaceDispatchRequest struct {
	// SpaceID 目标空间 ID。
	SpaceID string
	// Member 指名成员（须在空间内且已绑定原生会话）。
	Member string
	// RequestID 稳定受理请求 ID（必填；重放命中回执零重复执行零重复扣减，N02）。
	RequestID string
	// ExpectedVersion 预期空间版本（乐观锁，事务内复核）。
	ExpectedVersion int64
	// Term 调用方声明的当前任期（驱动者形态；owner-manual 形态可省略）。
	Term uint64
	// Note 派发说明（作为成员轮输入正文投递；同时持久于意图 Note）。
	Note string
}

// SpaceDispatch 空间派发受理（意图持久先行，outbox 语义）：localMu 互斥段内
// 「重放预检 → 成员会话轮号派生 → collab.RecordDispatch（授权/运行态/成员/预算/
// 版本全事务复核）」；受理成功后 kick 串行调度器投递。RecordDispatch 失败零副作用
// （无意图无扣减无账本条目）；投递异步，轮引用经 GET /dispatches/{request_id} 查询。
// 返回 dup=true 表示同 request_id 重放命中（返回原意图，不重复受理不重复投递）。
func (s *Service) SpaceDispatch(req SpaceDispatchRequest, subj collab.Subject) (collab.DispatchIntent, bool, error) {
	if s.collab == nil {
		return collab.DispatchIntent{}, false, &RemoteError{Code: "spaces_unavailable", Msg: "空间协作存储未装配（本机未启用 spaces）"}
	}
	if strings.TrimSpace(req.SpaceID) == "" || strings.TrimSpace(req.Member) == "" {
		return collab.DispatchIntent{}, false, &RemoteError{Code: "input_invalid", Msg: "space_id 与 member 必填"}
	}
	s.localMu.Lock()
	// 重放预检（先于轮号派生：已受理意图持原始轮号——派生高水位已推进，先派生会
	// 得到新号造成同键异文假冲突）。同 ID 同成员=同文回放；异成员=冲突。
	if it, ok := s.collab.IntentByRequest(req.SpaceID, req.RequestID); ok {
		s.localMu.Unlock()
		if it.Member != req.Member {
			return collab.DispatchIntent{}, false, &RemoteError{Code: "request_conflict",
				Msg: "request_id 已被不同内容占用（已登记成员 " + it.Member + "）"}
		}
		s.kick(req.SpaceID) // 幂等重放亦推进调度（先前受理若因投递失败滞留 pending）
		return it, true, nil
	}
	// 成员会话定位（RecordDispatch 为授权/状态/预算/版本的唯一权威，此处读档仅为
	// 轮号派生定位会话；成员不存在/未绑线程时交 RecordDispatch 产出规范错误）。
	sid, derr := s.spaceMemberSession(req.SpaceID, req.Member)
	if derr == nil {
		_, seq, _, _, aerr := s.deriveSessionTurn(sid)
		if aerr == nil {
			// M1 必修·受理期轮号预留锚定在途意图：派生基准（账本高水位+1）只反映
			// 已入账轮——「已受理未投递」的意图不在账本，快速连续受理会在同一窗口
			// 派生出相同 seq。基准扩为 max(账本派生号, 本会话全部在途意图最大 seq+1)
			// （pending+accepted 都计入），受理 seq 严格递增不撞号；localMu 互斥段
			// 内推导+落意图即原子预留（与 T2-R1 账本受理序号同一原则）。
			if ihw := s.spaceIntentSeqHighWater(sid); ihw >= seq {
				seq = ihw + 1
			}
			it, _, _, rerr := s.collab.RecordDispatch(req.SpaceID, collab.DispatchRequest{
				RequestID:       req.RequestID,
				Member:          req.Member,
				SessionID:       sid,
				TurnSeq:         seq,
				Term:            req.Term,
				ExpectedVersion: req.ExpectedVersion,
				Note:            req.Note,
			}, subj)
			if rerr != nil {
				s.localMu.Unlock()
				return collab.DispatchIntent{}, false, rerr
			}
			s.localMu.Unlock()
			s.kick(req.SpaceID)
			return it, false, nil
		}
		// 会话面派生失败（未装配会话内核/会话不存在/已关闭/队列满）：fail-closed
		// 零副作用——不落意图（调用方修好会话面后可同 request_id 重试）。
		s.localMu.Unlock()
		return collab.DispatchIntent{}, false, derr2remote(aerr)
	}
	s.localMu.Unlock()
	// 成员/会话定位失败：走 RecordDispatch 产出规范错误（member_not_found 等）。
	_, _, _, err := s.collab.RecordDispatch(req.SpaceID, collab.DispatchRequest{
		RequestID: req.RequestID, Member: req.Member, Term: req.Term,
		ExpectedVersion: req.ExpectedVersion, Note: req.Note,
	}, subj)
	if err != nil {
		return collab.DispatchIntent{}, false, err
	}
	return collab.DispatchIntent{}, false, &RemoteError{Code: "input_invalid", Msg: "成员会话定位失败（见空间成员绑定）"}
}

// spaceMemberSession 读档定位成员绑定的原生会话（受理预检；权威校验在
// RecordDispatch 事务内复核）。
func (s *Service) spaceMemberSession(spaceID, member string) (string, error) {
	sp, ok := s.collab.Space(spaceID)
	if !ok {
		return "", &RemoteError{Code: collab.CodeNotFound, Msg: "空间不存在: " + spaceID}
	}
	for _, m := range sp.Members {
		if m.Name == member {
			if m.SessionID == "" {
				return "", &RemoteError{Code: "input_invalid", Msg: "成员 " + member + " 未绑定原生会话（先 configure_members 绑定线程）"}
			}
			return m.SessionID, nil
		}
	}
	return "", &RemoteError{Code: collab.CodeMemberNotFound, Msg: "成员 " + member + " 不在空间内"}
}

// spaceIntentSeqHighWater 本会话全部在途意图的最大已声称轮号（M1 受理期预留的
// 基准成分：pending+accepted 都计入——pending 未入账、accepted 在途，二者占用的
// seq 都不能被新受理复用）。会话未绑定空间（无从产生意图）返回 0。仅查会话归属
// 的单一空间（一线程唯一归属），O(该空间意图数)。
func (s *Service) spaceIntentSeqHighWater(sid string) int64 {
	if s.collab == nil {
		return 0
	}
	spaceID, ok := s.collab.SessionOwnerSpace(sid)
	if !ok {
		return 0
	}
	var hw int64
	for _, it := range s.collab.Intents(spaceID) {
		if it.SessionID == sid && it.Status.InFlight() && it.TurnSeq > hw {
			hw = it.TurnSeq
		}
	}
	return hw
}

// derr2remote 派生错误归一（deriveSessionTurn 已返回 RemoteError）。
func derr2remote(err error) error {
	var re *RemoteError
	if errors.As(err, &re) {
		return re
	}
	return &RemoteError{Code: "internal", Msg: err.Error()}
}

// deriveSessionTurn 会话轮操作/序号派生（与 agentSessionOp 同一判定源；须持
// localMu。抽出复用形：状态读取 + 操作推导，投递链单一真相）。
func (s *Service) deriveSessionTurn(sid string) (op string, seq int64, cwd, agentName string, err error) {
	sess, inKernel := s.sessions.Status(sid)
	return s.agentSessionOp(sess, inKernel, sid)
}

// spaceSweepInterval 串行调度兜底扫查节拍（var 仅为测试缩短；正确性只依赖事件
// 驱动 kick，节拍只影响滞留重试时延——同账本清理节拍先例，不做配置面）。
var spaceSweepInterval = time.Second

// kick 触发单空间调度推进（单飞：进行中的推进与重复触发合并；节拍扫查兜底任何
// 丢失唤醒）。collab 未装配零操作。
func (s *Service) kick(spaceID string) {
	if s.collab == nil {
		return
	}
	s.muSpaceAdv.Lock()
	if s.spaceAdv[spaceID] {
		s.muSpaceAdv.Unlock()
		return
	}
	s.spaceAdv[spaceID] = true
	s.muSpaceAdv.Unlock()
	defer func() {
		s.muSpaceAdv.Lock()
		delete(s.spaceAdv, spaceID)
		s.muSpaceAdv.Unlock()
	}()
	s.advanceSpace(spaceID)
}

// NotifySpaceResumed 空间恢复通知（localapi control resume 成功后调用）：放行
// 串行调度器继续投递排队意图。
func (s *Service) NotifySpaceResumed(spaceID string) { s.kick(spaceID) }

// advanceSpace 单空间调度推进（调度前置检查面——只在空间存在待投递意图时动作；
// HTTP 空闲、无意图的空间零触碰，租约过期不判死）：
//  1. 非运行态 → 不投递（排队意图等待显式 resume）；
//  2. 存在 accepted 在途 → 串行等待（轮终收敛 hook 驱动下一步）；
//  3. 首个 pending 处执行租约前置检查（S1 口径：仅存在待投递意图时触发——
//     accepted-only 时不暂停，收敛后无 pending 自然停）：租约过期 → PauseExpired
//     （暂停原因=租约过期，未投递意图同事务取消收束——意图授权随租约失效，绝不
//     在过期权限下执行）；
//  4. 队首漂移（账本高水位 ≥ 意图 seq——该轮号已被真实受理占用）→ 取消收敛留痕、
//     跳过取下一棒（M1 容错：保后续可投递，不静默跳过也绝不死锁全队）；
//  5. 队首健康 → submitSpaceIntent 投递（每次至多一棒，串行保证）。
func (s *Service) advanceSpace(spaceID string) {
	sp, ok := s.collab.Space(spaceID)
	if !ok {
		return
	}
	if sp.Status != collab.StatusRunning {
		return
	}
	intents := s.collab.Intents(spaceID)
	for _, it := range intents {
		if it.Status == collab.IntentAccepted {
			return // 串行门：已有在途投递，等其收敛
		}
	}
	now := protocol.NowUTC()
	leaseChecked := false
	for _, it := range intents {
		if it.Status != collab.IntentPending {
			continue
		}
		if !leaseChecked {
			// S1 口径：租约前置检查仅在存在待投递意图时执行（无人等待投递时，租约
			// 过期只是事实记录，不构成暂停理由——避免过度暂停）。
			if !sp.Leader.LeaseValid(now) && !sp.Leader.LeaseUntil.IsZero() {
				reason := "负责人租约已过期（调度前置检查，任期 " + itoa(sp.Leader.Term) + "）：自动暂停，恢复须显式 resume"
				updated, cancelled, err := s.collab.PauseExpired(spaceID, reason)
				if err != nil {
					log.Printf("[node] 空间租约过期自动暂停失败 space=%s: %v（下次前置检查重试）", spaceID, err)
					return
				}
				log.Printf("[node] 空间租约过期自动暂停 space=%s 任期=%d 取消未投递意图=%d 暂停原因=%q",
					spaceID, updated.Leader.Term, cancelled, updated.PausedReason)
				return
			}
			leaseChecked = true
		}
		// M1 容错：队首 seq 与账本高水位冲突（真漂移）→ 取消收敛留痕，跳过取下一
		// 棒；此处不做系统暂停（容错路径保队列流动——投递期 tripwire 才停队）。
		if s.cfg.Ledger.SessionTurnHighWater(it.SessionID) >= it.TurnSeq {
			note := "轮号漂移（账本高水位 ≥ 意图 seq，该轮号已被真实受理占用）：取消收束留痕"
			if _, cerr := s.collab.ConvergeIntent(spaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID, note, spaceSystemSubject()); cerr != nil {
				log.Printf("[node] 漂移队首取消收敛失败 space=%s intent=%s: %v（下次推进重试）", spaceID, it.RequestID, cerr)
				return
			}
			log.Printf("[node] 空间意图轮号漂移取消 space=%s intent=%s seq=%d 高水位=%d（跳过取下一棒）",
				spaceID, it.RequestID, it.TurnSeq, s.cfg.Ledger.SessionTurnHighWater(it.SessionID))
			continue
		}
		s.submitSpaceIntent(spaceID, it)
		return // 串行：一次推进至多投递一棒
	}
}

// itoa 无格式依赖的 uint64 十进制（日志/原因文案拼装用）。
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// submitSpaceIntent 投递单条待执行意图（复用既有会话投递链：操作类型解析 →
// 轮号一致性校验 → 受理守卫 → 账本入账 → AttachDispatchTask（pending→accepted）
// → execute——不另造投递链）。M1 必修·投递期以意图已登记 seq 为准，不得重派生：
// 派生仅取操作类型/cwd/agent；校验「账本高水位 < 意图 seq」即按序入账（记账顺序
// 与 seq 一致性核对），高水位 ≥ 意图 seq = 真漂移 → 该意图按取消收敛 + 系统暂停
// 停队 + 日志如实，绝不静默跳过。可重试失败（队列满/入账失败）保持 pending 等下
// 次推进；确定性不可达（会话已关/不存在）按取消收束（如实注记，绝不留幽灵执行
// 窗口）。
func (s *Service) submitSpaceIntent(spaceID string, it collab.DispatchIntent) {
	sid := it.SessionID
	s.localMu.Lock()
	if s.sessions == nil {
		s.localMu.Unlock()
		s.cancelUndeliverableIntent(spaceID, it, "本机未装配会话内核，意图不可投递")
		return
	}
	op, _, cwd, agentName, err := s.deriveSessionTurn(sid)
	if err != nil {
		s.localMu.Unlock()
		var re *RemoteError
		if errors.As(err, &re) && (re.Code == "session_not_found" || re.Code == "session_closed") {
			s.cancelUndeliverableIntent(spaceID, it, "成员会话不可达（"+re.Code+"）：意图取消收束")
			return
		}
		// 队列满等瞬态：保持 pending，下次推进重试。
		log.Printf("[node] 空间意图投递暂缓 space=%s intent=%s session=%s: %v", spaceID, it.RequestID, sid, err)
		return
	}
	// M1·投递期轮号一致性校验（记账顺序与 seq 一致）：意图 seq 必须严格接在账本
	// 高水位之后；≥ 即真漂移（该轮号已被其他真实受理占用）——取消收敛 + 系统暂停
	// 停队（异常态落成可见状态等人工核对），绝不静默跳过也绝不叠投。
	if hw := s.cfg.Ledger.SessionTurnHighWater(sid); hw >= it.TurnSeq {
		s.localMu.Unlock()
		note := "轮号真漂移（账本高水位 ≥ 意图 seq）：取消收束并暂停停队，待人工核对"
		if _, cerr := s.collab.ConvergeIntent(spaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID, note, spaceSystemSubject()); cerr != nil {
			log.Printf("[node] 空间意图真漂移取消收敛失败 space=%s intent=%s: %v（重启对账兜底）", spaceID, it.RequestID, cerr)
			return
		}
		if _, applied, perr := s.collab.SystemPause(spaceID, "意图轮号真漂移（投递期一致性校验失败）：串行队列暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[node] 漂移停队系统暂停失败 space=%s: %v", spaceID, perr)
		} else if applied {
			log.Printf("[node] 空间已因意图轮号真漂移暂停停队 space=%s", spaceID)
		}
		log.Printf("[node] 空间意图轮号真漂移 space=%s intent=%s 意图seq=%d 账本高水位=%d：取消收敛+暂停停队",
			spaceID, it.RequestID, it.TurnSeq, hw)
		return
	}
	seq := it.TurnSeq // 投递期以意图登记 seq 为准（受理期已预留，不重派生）
	now := protocol.NowUTC()
	task := protocol.Task{
		ID:          protocol.NewTaskID(),
		TraceID:     protocol.NewTraceID(),
		TargetNode:  s.cfg.NodeID,
		TargetAgent: agentName,
		Input:       protocol.TaskInput{Stdin: it.Note},
		SubmittedBy: "space:" + spaceID, // 展示标签（留档）；身份权威在意图与主体账
		IdemKey:     "lsturn:" + sid + ":" + itoa64i(it.TurnSeq),
		Status:      protocol.StatusSubmitted,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpireAt:    now.Add(protocol.TaskRetention),
		Session: &protocol.SessionTurnRef{
			SessionID: sid,
			TurnSeq:   seq,
			Op:        op,
			Cwd:       cwd,
		},
	}
	if s.sessionOps != nil {
		if err := s.sessionOps.AdmissionGuard(sid); err != nil {
			s.localMu.Unlock()
			log.Printf("[node] 空间意图投递暂缓（受理守卫）space=%s intent=%s: %v", spaceID, it.RequestID, err)
			return // 瞬态：保持 pending 重试
		}
		if op == protocol.SessionTurnOpCreate {
			if err := s.sessionOps.AuthorizeWorkspace(cwd); err != nil {
				s.localMu.Unlock()
				s.cancelUndeliverableIntent(spaceID, it, "成员会话工作区授权拒绝："+err.Error())
				return
			}
		}
	}
	if err := s.cfg.Ledger.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		s.localMu.Unlock()
		log.Printf("[node] 空间意图投递失败（入账）space=%s intent=%s task=%s: %v（保持 pending）", spaceID, it.RequestID, task.ID, err)
		return
	}
	if s.sessionOps != nil {
		s.sessionOps.BeginTurn(sid, seq, task.ID)
	}
	s.regCancel(task.ID)
	if _, aerr := s.collab.AttachDispatchTask(spaceID, it.RequestID, task.ID, spaceSystemSubject()); aerr != nil {
		// 接单登记失败：轮已入账会照常执行（结果照常回流）；意图滞留 pending 由
		// 轮号漂移防御挡住叠投，重启对账按账本实物收敛。
		log.Printf("[node] 空间意图接单登记失败 space=%s intent=%s task=%s: %v（轮已受理，重启对账收敛）", spaceID, it.RequestID, task.ID, aerr)
	}
	s.localMu.Unlock()
	log.Printf("[node] 空间派发投递 space=%s member=%s intent=%s task=%s session=%s seq=%d op=%s",
		spaceID, it.Member, it.RequestID, task.ID, sid, seq, op)
	s.execute(task)
}

// cancelUndeliverableIntent 确定性不可投递的意图收束（system 主体取消——零执行
// 事实，取消是最如实的账；失败滞留由重启对账兜底）。
func (s *Service) cancelUndeliverableIntent(spaceID string, it collab.DispatchIntent, note string) {
	if _, err := s.collab.ConvergeIntent(spaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID, note, spaceSystemSubject()); err != nil {
		log.Printf("[node] 不可投递意图取消收束失败 space=%s intent=%s: %v（重启对账兜底）", spaceID, it.RequestID, err)
		return
	}
	log.Printf("[node] 空间意图取消收束（未投递）space=%s intent=%s: %s", spaceID, it.RequestID, note)
}

// spaceConvergeTurn 成员轮终收敛分类（execute 终态落盘后调用；W04-01 RFC §1.6
// 「不以 CLI 模型回复文本猜成功」——分类只读账本终态实物）：
//   - completed → converged（执行事实成立）；
//   - failed → converged + 记账码如实注记（失败是已收束的执行事实；串行队列推进）；
//   - cancelled → cancelled + 系统暂停停队（不再派发下一棒——人工介入优先，
//     resume 显式放行）；
//   - 其他（needs_review 等不确定）→ 不收敛：意图留待核对（在途），队列停进，
//     人工核对后经会话/任务面处置——绝不猜测、绝不重跑。
//
// 收敛成功即 kick（converged 时推进下一棒）。收敛失败（落盘）留待重启对账。
func (s *Service) spaceConvergeTurn(task protocol.Task, res protocol.TaskResult) {
	if s.collab == nil || task.Session == nil || task.Session.TurnSeq <= 0 {
		return
	}
	spaceID, ok := s.collab.SessionOwnerSpace(task.Session.SessionID)
	if !ok {
		return
	}
	var it *collab.DispatchIntent
	intents := s.collab.Intents(spaceID)
	for i := range intents {
		iv := intents[i]
		if iv.SessionID == task.Session.SessionID && iv.TurnSeq == task.Session.TurnSeq && iv.Status.InFlight() {
			it = &intents[i]
			break
		}
	}
	if it == nil {
		return
	}
	outcome, note, halt := classifySpaceTurn(res)
	if outcome == "" {
		log.Printf("[node] 空间意图轮终不确定，留待核对（不收敛不重跑）space=%s intent=%s session=%s seq=%d status=%s err=%q",
			spaceID, it.RequestID, task.Session.SessionID, task.Session.TurnSeq, res.Status, res.Error)
		return
	}
	if _, err := s.collab.ConvergeIntent(spaceID, it.RequestID, outcome, it.TaskID, note, spaceSystemSubject()); err != nil {
		log.Printf("[node] 空间意图收敛落账失败 space=%s intent=%s: %v（重启对账按账本实物重试）", spaceID, it.RequestID, err)
		return
	}
	log.Printf("[node] 空间意图收敛 space=%s intent=%s → %s（%s）", spaceID, it.RequestID, outcome, note)
	if halt {
		// 取消停队（W04-02 取消保护）：不再派发下一棒——系统暂停把「人工已介入」
		// 落成可见状态（调度条暂停原因），resume 显式放行。
		if _, applied, perr := s.collab.SystemPause(spaceID, "成员轮被取消（人工介入）：串行队列暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[node] 取消后系统暂停失败 space=%s: %v", spaceID, perr)
		} else if applied {
			log.Printf("[node] 空间已因成员轮取消自动暂停（不再派发下一棒）space=%s", spaceID)
		}
		return
	}
	s.kick(spaceID)
}

// classifySpaceTurn 轮终结果 → 意图收敛分类（outcome 空 = 不确定留待核对）。
func classifySpaceTurn(res protocol.TaskResult) (outcome, note string, halt bool) {
	code := res.Error
	if i := strings.Index(code, ":"); i > 0 {
		code = code[:i]
	}
	switch res.Status {
	case protocol.ResultCompleted:
		return string(collab.IntentConverged), "成员轮完成（执行事实落账）", false
	case protocol.ResultFailed:
		return string(collab.IntentConverged), "failed:" + code + "（轮终失败，按记账码如实落账）", false
	case protocol.ResultCancelled:
		return string(collab.IntentCancelled), firstSegment(res.Error, "cancelled"), true
	default:
		return "", "", false
	}
}

// firstSegment 错误首段或缺省词。
func firstSegment(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if i := strings.Index(s, ":"); i > 0 {
		return s[:i]
	}
	return s
}

// itoa64i int64 十进制（意图轮号幂等键拼装；与 itoa64 语义一致供生产代码使用）。
func itoa64i(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	uv := uint64(v)
	if neg {
		uv = uint64(-v)
	}
	var b [24]byte
	i := len(b)
	for uv > 0 {
		i--
		b[i] = byte('0' + uv%10)
		uv /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// recoverSpaceIntents 重启对账闭环（New 装配期一次性；先于任何恢复重投——与
// sessionop 重启收敛同序位之后执行，账本条目均已终态化）：按实物分类、绝不重跑。
//   - accepted（已投递）：按账本终态实物分类（completed→converged / failed→记账
//     码如实 / cancelled→cancelled+系统暂停停队（S2：与活路径一致，重启后如实停
//     队等人工确认）/ 其他→留待核对）；账本无实物或非终态 → 留待核对（在途），
//     绝不猜测；
//   - pending：先查账本实物——有实物（M1 受理→投递崩溃窗：AcceptOrigin 已落、接
//     单登记未及落盘）按实物分类（同 accepted 口径，绝不臆断未投递）；无实物才
//     取消收束（「进程恢复不得凭回调自动重跑」，恢复后由调用方显式重新派发）。
func (s *Service) recoverSpaceIntents() {
	if s.collab == nil {
		return
	}
	for _, it := range s.collab.RecoverIntents() {
		switch it.Status {
		case collab.IntentPending:
			if _, delivered := findLedgerTurn(s.cfg.Ledger, it.SessionID, it.TurnSeq); !delivered {
				if _, err := s.collab.ConvergeIntent(it.SpaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID,
					"进程恢复对账：未投递意图不自动重跑（恢复后显式重新派发）", spaceSystemSubject()); err != nil {
					log.Printf("[node] 重启对账：pending 意图取消收束失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
					continue
				}
				log.Printf("[node] 重启对账：未投递意图已取消收束（不自动重跑）space=%s intent=%s member=%s", it.SpaceID, it.RequestID, it.Member)
				continue
			}
			// 有账本实物：已投递（受理→投递崩溃窗），按实物分类。
			s.convergeIntentByLedgerFact(it, "pending+实物")
		case collab.IntentAccepted:
			s.convergeIntentByLedgerFact(it, "accepted")
		}
	}
}

// convergeIntentByLedgerFact 按账本实物收敛单条在途意图（重启对账共用体）：
// completed→converged / failed→记账码如实 / cancelled→cancelled+系统暂停停队（S2
// 对齐活路径）/ 其他→留待核对不收敛。失败（落盘）留待下次启动重试。
func (s *Service) convergeIntentByLedgerFact(it collab.DispatchIntent, via string) {
	e, ok := findLedgerTurn(s.cfg.Ledger, it.SessionID, it.TurnSeq)
	if !ok {
		log.Printf("[node] 重启对账：意图轮无账本实物，留待核对 space=%s intent=%s session=%s seq=%d via=%s", it.SpaceID, it.RequestID, it.SessionID, it.TurnSeq, via)
		return
	}
	if e.State != ledger.StateFinished || e.Result == nil {
		log.Printf("[node] 重启对账：意图轮非终态，留待核对 space=%s intent=%s state=%v via=%s", it.SpaceID, it.RequestID, e.State, via)
		return
	}
	outcome, note, halt := classifySpaceTurn(*e.Result)
	if outcome == "" {
		log.Printf("[node] 重启对账：轮终不确定（%s），意图留待核对 space=%s intent=%s via=%s", e.Result.Status, it.SpaceID, it.RequestID, via)
		return
	}
	if _, err := s.collab.ConvergeIntent(it.SpaceID, it.RequestID, outcome, it.TaskID, note+"（重启对账）", spaceSystemSubject()); err != nil {
		log.Printf("[node] 重启对账：意图收敛失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
		return
	}
	log.Printf("[node] 重启对账：意图按账本实物收敛 space=%s intent=%s → %s via=%s", it.SpaceID, it.RequestID, outcome, via)
	if halt {
		// S2：取消收敛与活路径同语义——系统暂停把「已取消/不再派发」落成可见状态，
		// 重启后如实停队等人工确认（显式 resume 恢复）。
		if _, applied, perr := s.collab.SystemPause(it.SpaceID, "重启对账：成员轮取消（人工介入）落账，串行队列暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[node] 重启对账取消停队系统暂停失败 space=%s: %v", it.SpaceID, perr)
		} else if applied {
			log.Printf("[node] 重启对账：空间已因取消意图收敛暂停停队 space=%s", it.SpaceID)
		}
	}
}

// spaceSweepLoop 串行调度兜底节拍（仅扫查有在途意图的空间——「有新增派发意图」
// 才构成调度活动；HTTP 空闲、无意图空间零触碰不判死）。随服务 ctx 停止。
func (s *Service) spaceSweepLoop() {
	defer s.wg.Done()
	tk := time.NewTicker(spaceSweepInterval)
	defer tk.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tk.C:
			for _, sp := range s.collab.Spaces() {
				if s.collab.InFlightCount(sp.ID) > 0 {
					s.kick(sp.ID)
				}
			}
		}
	}
}

// spaceTurnAllowed 执行口守卫（runSessionTurnExec 入口）：空间绑定会话的建轮/
// 续轮只允许「本调度器投递的意图轮」（意图 accepted 且 TaskID 与本任务一致——
// 服务端生成的任务 ID 不可伪造，hub 下发或本机直投的旁路轮一律拒绝并指向
// space dispatch）。cancel/close 管理动作放行（取消保护依赖成员轮取消）。
// 非空间会话零变化（未绑定恒放行）。
func (s *Service) spaceTurnAllowed(task protocol.Task) bool {
	if s.collab == nil || task.Session == nil || task.Session.TurnSeq <= 0 {
		return true
	}
	if task.Session.Op != protocol.SessionTurnOpCreate && task.Session.Op != protocol.SessionTurnOpSend {
		return true
	}
	spaceID, bound := s.collab.SessionOwnerSpace(task.Session.SessionID)
	if !bound {
		return true
	}
	for _, it := range s.collab.Intents(spaceID) {
		if it.SessionID == task.Session.SessionID && it.TurnSeq == task.Session.TurnSeq &&
			it.Status == collab.IntentAccepted && it.TaskID == task.ID {
			return true
		}
	}
	return false
}
