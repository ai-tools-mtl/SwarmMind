// service.go 中心空间服务（W07-02 远端会话/派发 adapter + 可靠结果/恢复映射）：
//
// 派发链路（复用既有 session Task 通道，不新增任意远程执行通道）：
//
//	POST /api/v1/spaces/{id}/dispatches
//	  → Dispatch：成员寻址（目录词形 nodeID/agentName + hub 注册表会话绑定）
//	    → core.RequireSpacesCapable 版本门（W07-01 强制门首个消费者，普通任务零牵连）
//	    → core.SpaceBind 绑定登记（node 收件门受理面；ack 才放行）
//	    → collab.RecordDispatch（意图 outbox 先于执行持久，W07-01 词形单源
//	      scope=hub:<id>）→ core.SubmitSpaceTurn（Session.Space=hub:<id>，
//	      task_offer 既有投递链全白拿）→ collab.AttachDispatchTask（pending→accepted）
//	  → node 执行（收件门按绑定放行）→ task_result 既有结果链
//	  → convergeOnTaskTerminal（任务首落终态钩子）：completed/failed→converged、
//	    cancelled→cancelled+系统暂停停队、needs_review→留待核对不收敛
//	    （结果落账不触发下一轮，N04）。
//
// 可靠性纪律（AGENTS 帧请求-应答铁律 + 任务书离线兼容节逐条对照）：
//   - 在途先登记再 send：意图（outbox）与 space_bind waiter 均先登记后发送；
//   - 迟到结果验证来源后消费：任务归属（TargetNode==env.From）由 core 结果仲裁
//     保证，收敛前再核意图锚（session+seq）与 scope 词形；
//   - hub 分配请求键：任务 ID/轮序号/绑定 mid 全部 hub 侧分配，不信远端自报；
//   - 旧 term 拒绝新派发：RecordDispatch 授权层 driver 形态（term 等值）裁决；
//     已接受轮次事实照常落账，不触发暂停空间的下一步；
//   - 接受前离线→持久排队：登记完成后 node 离线，轮任务留 hub 信箱，重连补投；
//   - 未知 needs_review 不自动重发另一节点；不把 keepalive 失联解释为任务没执行
//     （accepted 意图任务未终态=仍在途，恢复对账留待核对）。
//
// 重启对账（recover）：在途意图按账本实物分类（任务/轮行），pending 无实物取消
// 收束不自动重跑；pending 有实物（受理→投递崩溃窗）接单登记收敛到 accepted；
// accepted 按任务终态分类、非终态留在途等结果、任务已清理留待核对——与 node
// spacedispatch 重启对账语义逐条对齐（宁留待核对不自动重跑）。
package spaces

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sweepInterval 派发兜底扫查节拍（var 仅为测试缩短；正确性只依赖事件驱动——
// 任务终态钩子与 Dispatch 触发，节拍只影响滞留意图重试时延。同 node
// spaceSweepInterval 先例，不做配置面）。仅扫查有在途意图的空间（HTTP 空闲、无
// 意图空间零触碰）。
var sweepInterval = 2 * time.Second

// systemSubject 中心执行侧统一主体（collab 授权面：ConvergeIntent/
// AttachDispatchTask 仅 owner/system；执行侧记账属内置服务独立可审计 system 主体
// ——与 node spacedispatch 同一主体词）。
func systemSubject() collab.Subject {
	return collab.Subject{Name: "system", Class: collab.ClassSystem}
}

// Error 派发面操作错误（REST/MCP 投影按 Code 映射；与 collab 业务错误分开——
// 路由/登记/会话面的错误不入 collab 码表）。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// 派发面错误码（REST 投影见 api spaces.go writeSpaceDispatchErr）。
const (
	CodeAgentNotExposed = "agent_not_exposed"  // 成员不在开放目录
	CodeSessionGone     = "session_not_found"  // 成员绑定会话不存在
	CodeSessionClosed   = "session_closed"     // 会话已关闭
	CodeBindingMismatch = "binding_mismatch"   // 成员与会话绑定不符
	CodeNodeOffline     = "node_offline"       // 目标 node 不在线
	CodeBindTimeout     = "bind_timeout"       // 绑定登记应答超时
	CodeBindRejected    = "bind_rejected"      // 绑定登记被 node 拒绝
	CodeTurnFailed      = "turn_submit_failed" // 轮提交确定性失败（已按取消收束）
)

// Options 服务装配项。
type Options struct {
	// Hooks 策略接口（collab.StrategyHooks；hub 侧装配 strategy.Engine——纯进程内
	// 判定器，中心无 agent/管家也可运行）。nil = 完成类动作一律拒绝（W04 只开放
	// 放弃定版，同 node 未装配形态）。
	Hooks collab.StrategyHooks
	// Log 运维日志；nil = 静默。
	Log collab.Logger
}

// Service 中心空间服务：collab.Store 同一内核（零第二套状态机）+ 远端派发
// adapter。REST 面经内嵌 Store 直调模型层（create/control/messages/sugar 与
// node 面同形）；dispatch 经 Dispatch（本文件）走远端链。
type Service struct {
	*collab.Store
	core *core.Core

	// muDispatch 派发受理互斥段：轮号派生（会话视图读）→ RecordDispatch →
	// SubmitSpaceTurn 串行化——并发派发同会话不会派生相同预留 seq（提交面
	// SubmitTurn 事务内 (session,seq) 唯一性为第二道防线，撞号确定性冲突取消）。
	muDispatch sync.Mutex

	snap *snapshotDB
	stop chan struct{}
	done chan struct{}

	// 关停优雅收束面（Close 三段式：关停位 → 排空 → 关库）：
	//   - muClose/closed 关停位：置位后新回调入口快速退出（hub 侧钩子注册在
	//     core 上无法注销——旧服务实例的注册持续可达，重开新实例后旧钩子同样
	//     由此拦下；丢弃留痕不静默，重启对账按实物收敛兜底）；
	//   - hookWG 在途收敛回调排空面：hookEntry 入口计数，Close 在关库前排空
	//     全部在途回调（否则回调在库关后写账失败——收敛/暂停丢落账，违反
	//     「已接受执行按原契约收束」语义，集成者门禁实锤）。
	muClose sync.Mutex
	closed  bool
	hookWG  sync.WaitGroup
}

// hookDrainBudget 关停排空预算（var 仅为测试调整）。回调链为有界写库
// （collab 事务 + 日志，毫秒级），预算只作悬挂兜底——超时如实日志后关库，残留
// 回调由关停位快速退出留痕。
var hookDrainBudget = 5 * time.Second

// Open 装配中心空间服务（dataDir 下 SQLite 快照库 + collab 内核）。快照损坏/
// 版本未知拒绝启动（宁拒启不瞎门）。装配完成即注册任务终态收敛钩子并启动兜底
// 扫查；用完须 Close。
func Open(dataDir string, c *core.Core, opt Options) (*Service, error) {
	if c == nil {
		return nil, errors.New("spaces: core 未装配")
	}
	snap, err := openSnapshotDB(dataDir)
	if err != nil {
		return nil, err
	}
	st, err := collab.Open(dataDir, collab.Options{
		Snapshot: snap,
		Hooks:    opt.Hooks,
		Log:      opt.Log,
	})
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	s := &Service{Store: st, core: c, snap: snap, stop: make(chan struct{}), done: make(chan struct{})}
	// 双钩子统一经 hookEntry 入口（关停位检查 + 在途排空计数；见 Service 注）。
	c.AddOnTaskTerminal(s.hookEntry)
	// 取消合并钩子（同一收敛函数双注册）：hub 侧取消首落终态时 Result 未达不猜
	//（钩子内 nil 结果零操作，意图留待核对）；node 取消结果合并后按真实执行事实
	// 收口——「不把失联解释为没执行」与「事实到达即收口」两不误。
	c.AddOnTaskMerged(s.hookEntry)
	s.recover() // 装配期一次性（先于引用外返——天然不与 Close 竞态，不入排空面）
	go s.sweepLoop()
	return s, nil
}

// hookEntry 收敛回调统一入口（终态钩子与取消合并钩子共用）：关停位先检（已
// 关停 → 丢弃留痕，绝不带着已关库写账）；过检即在排空面计数——Close 的 Wait
// 与本 Add 同锁序，无「Wait 已开、Add 后到」的丢失窗口（集成者门禁修复）。
func (s *Service) hookEntry(t protocol.Task) {
	s.muClose.Lock()
	if s.closed {
		s.muClose.Unlock()
		if t.Session != nil {
			log.Printf("[hub] spaces: 服务已关停，任务终态回调丢弃 task=%s session=%s seq=%d（留痕不静默；重启对账按实物收敛）",
				t.ID, t.Session.SessionID, t.Session.TurnSeq)
		} else {
			log.Printf("[hub] spaces: 服务已关停，任务终态回调丢弃 task=%s（非会话轮；留痕不静默）", t.ID)
		}
		return
	}
	s.hookWG.Add(1)
	s.muClose.Unlock()
	defer s.hookWG.Done()
	s.convergeOnTaskTerminal(t)
}

// Close 优雅收束（三段式，幂等；内核 Store.Close 幂等）：
//  1. 置关停位——此后新回调入口快速退出（留痕不写账）；
//  2. 排空——先停兜底扫查并等其出临界（扫查写库全在 sweepLoop goroutine 内），
//     再等全部在途收敛回调出临界（预算兜底，超时如实日志）——「已接受执行按
//     原契约收束」的关停半边：收敛/暂停落账绝不因关库竞态丢失；
//  3. 关库——SQLite 快照库与 collab store 最后关（顺序不可倒置：先关库会让
//     在途回调写账失败，集成者门禁实锤「sql: database is closed」）。
func (s *Service) Close() error {
	s.muClose.Lock()
	s.closed = true
	s.muClose.Unlock()
	// 排空第一段：兜底扫查（写库全在 sweepLoop goroutine 内，done 即出临界）。
	select {
	case <-s.stop:
	default:
		close(s.stop)
		<-s.done
	}
	// 排空第二段：在途收敛回调。
	drained := make(chan struct{})
	go func() {
		s.hookWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(hookDrainBudget):
		log.Printf("[hub] spaces: 关停排空超时（%s）——残留在途回调将由关停位退出留痕（超时兜底如实日志，不静默）", hookDrainBudget)
	}
	// 关库（最后）。
	_ = s.snap.Close()
	return s.Store.Close()
}

// Scope 空间 scope（collab.HubScope 词形单源；W07-01 定案「创建入口 scope 固定
// hub:<id>」——中心空间服务对内对外的唯一词形）。
func Scope(spaceID string) string { return collab.HubScope(spaceID) }

// ---------- 派发面 ----------

// DispatchRequest 派发请求（REST/MCP 数据面；与 node 面同形同参）。
type DispatchRequest struct {
	SpaceID         string
	Member          string
	RequestID       string // 必填（受理稳定请求标识；重放命中回执）
	ExpectedVersion int64
	Term            uint64 // 驱动者形态携当前任期（旧 term 授权层拒绝）
	Note            string // 派发说明（成员轮输入正文）
}

// Dispatch 远端派发受理（outbox 先登记后 send）：成员寻址 → 版本门 → 绑定登记 →
// 意图持久 → 既有 session Task 通道投递 → 接单登记。返回 dup=true 表示同
// request_id 重放命中（返回原意图，零重复执行零重复扣减——N02）。
func (s *Service) Dispatch(req DispatchRequest, subj collab.Subject) (collab.DispatchIntent, bool, error) {
	if strings.TrimSpace(req.SpaceID) == "" || strings.TrimSpace(req.Member) == "" {
		return collab.DispatchIntent{}, false, &Error{Code: "invalid", Msg: "space_id 与 member 必填"}
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return collab.DispatchIntent{}, false, &Error{Code: "invalid", Msg: "request_id 必填（受理稳定请求标识，重试安全）"}
	}
	// 重放预检（先于一切副作用；与 node SpaceDispatch 同序）：同 ID 同成员=同文
	// 回放；异成员=冲突。
	if it, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		if it.Member != req.Member {
			return collab.DispatchIntent{}, false, &Error{Code: collab.CodeRequestConflict,
				Msg: "request_id 已被不同内容占用（已登记成员 " + it.Member + "）"}
		}
		s.sweepSpace(req.SpaceID) // 幂等重放亦推进滞留投递（先前受理若滞留 pending）
		return it, true, nil
	}
	// 成员寻址（只读预检；权威校验在 RecordDispatch/SubmitTurn 事务内复核）。
	mt, err := s.memberTarget(req.SpaceID, req.Member)
	if err != nil {
		return collab.DispatchIntent{}, false, err
	}
	// 版本门（W07-01 强制门——声明缺失/版本不符明确拒绝指路升级，绝不静默降级
	// 为普通任务执行；普通任务路径不经过本门零牵连）。
	if err := s.core.RequireSpacesCapable(mt.nodeID, mt.agent); err != nil {
		return collab.DispatchIntent{}, false, err
	}
	// 绑定登记（node 收件门受理面；幂等——重复登记刷新；失败零副作用：意图零
	// 登记，宁拒不瞎投）。登记先于一切 hub 侧持久化变更。
	scope := Scope(req.SpaceID)
	if err := s.core.SpaceBind(mt.nodeID, protocol.SpaceBindPayload{Space: scope, SessionID: mt.sessionID}, 0); err != nil {
		return collab.DispatchIntent{}, false, bindError(err)
	}
	s.muDispatch.Lock()
	defer s.muDispatch.Unlock()
	// 互斥段内重放重检（并发派发收敛：先落者为准，后来者按重放/冲突出回执）。
	if it, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		if it.Member != req.Member {
			return collab.DispatchIntent{}, false, &Error{Code: collab.CodeRequestConflict,
				Msg: "request_id 已被不同内容占用（已登记成员 " + it.Member + "）"}
		}
		return it, true, nil
	}
	// 轮号预留（受理期）：max(会话下一序号, 本会话在途意图最大 seq+1)——受理 seq
	// 严格递增不撞号（与 node M1 受理期预留同原则；派发互斥段内推导+落意图即
	// 原子预留）。
	sv, ok := s.core.GetSession(mt.sessionID)
	if !ok {
		return collab.DispatchIntent{}, false, &Error{Code: CodeSessionGone, Msg: "成员绑定会话不存在: " + mt.sessionID}
	}
	seq := nextTurnSeqOf(sv)
	if hw := s.intentSeqHighWater(mt.sessionID); hw >= seq {
		seq = hw + 1
	}
	it, _, dup, err := s.RecordDispatch(req.SpaceID, collab.DispatchRequest{
		RequestID:       req.RequestID,
		Member:          req.Member,
		SessionID:       mt.sessionID,
		TurnSeq:         seq,
		Term:            req.Term,
		ExpectedVersion: req.ExpectedVersion,
		Note:            req.Note,
	}, subj)
	if err != nil {
		return collab.DispatchIntent{}, false, err
	}
	if dup {
		return it, true, nil // 并发收敛：另一 goroutine 已受理
	}
	// 投递（outbox 先登记后 send；失败分类收束，见 submitIntent）。
	if err := s.submitIntent(req.SpaceID, it, false); err != nil {
		return it, false, err
	}
	// 返回前重读意图（attach/取消收束发生在投递段内——调用方看到当前态：
	// accepted（带 TaskID）或确定性取消收束态，不回投递前快照）。
	if cur, ok := s.IntentByRequest(req.SpaceID, req.RequestID); ok {
		it = cur
	}
	return it, false, nil
}

// memberTarget 成员寻址（W07-02 路由面）：成员名 = 开放目录词形
// "nodeID/agentName"（DirectoryAgent 同一判定的数据面），绑定会话 = hub 注册表
// 会话——成员名与绑定会话的 node/agent 逐一核对（防跨机冒绑）。命中返回路由
// 三元组。
type memberRoute struct {
	nodeID    string
	agent     string
	sessionID string
}

func (s *Service) memberTarget(spaceID, member string) (memberRoute, error) {
	nodeID, agent, ok := splitMemberName(member)
	if !ok {
		return memberRoute{}, &Error{Code: "invalid", Msg: "member 词形非法（须为目录形态 nodeID/agentName）: " + member}
	}
	if _, ok := s.core.DirectoryAgent(nodeID, agent); !ok {
		return memberRoute{}, &Error{Code: CodeAgentNotExposed,
			Msg: fmt.Sprintf("成员 %s 不在当前开放目录（exposed 能力未命中；先接入/开放该能力）", member)}
	}
	sp, ok := s.Space(spaceID)
	if !ok {
		return memberRoute{}, collab.ErrNotFound
	}
	var sessionID string
	for _, m := range sp.Members {
		if m.Name == member {
			if m.SessionID == "" {
				return memberRoute{}, &Error{Code: "invalid",
					Msg: "成员 " + member + " 未绑定原生会话（先 configure_members 携 session_id 绑定 hub 会话线程）"}
			}
			sessionID = m.SessionID
			break
		}
	}
	if sessionID == "" {
		return memberRoute{}, collab.ErrMemberNotFound
	}
	sv, ok := s.core.GetSession(sessionID)
	if !ok {
		return memberRoute{}, &Error{Code: CodeSessionGone, Msg: "成员绑定会话不存在: " + sessionID}
	}
	if sv.Status != store.SessionStatusActive {
		return memberRoute{}, &Error{Code: CodeSessionClosed, Msg: "成员绑定会话已关闭: " + sessionID}
	}
	if sv.NodeID != nodeID || sv.AgentName != agent {
		return memberRoute{}, &Error{Code: CodeBindingMismatch,
			Msg: fmt.Sprintf("成员 %s 与会话 %s 绑定不符（会话在 %s/%s）——拒绝跨机冒绑", member, sessionID, sv.NodeID, sv.AgentName)}
	}
	return memberRoute{nodeID: nodeID, agent: agent, sessionID: sessionID}, nil
}

// splitMemberName 成员名拆解（目录词形 nodeID/agentName；nodeID 不含 /、agent
// 非空——与目录条目一一对应）。
func splitMemberName(member string) (nodeID, agent string, ok bool) {
	member = strings.TrimSpace(member)
	i := strings.Index(member, "/")
	if i <= 0 || i == len(member)-1 {
		return "", "", false
	}
	return member[:i], member[i+1:], true
}

// submitIntent 投递单条已登记意图（既有 session Task 通道；失败分类收束）：
//   - 确定性失败（会话关闭/不存在/绑定漂移/轮号冲突/输入非法）→ 意图按取消收束
//     （如实注记，绝不留幽灵执行窗口），错误如实上抛（CodeTurnFailed）；
//   - 瞬态失败（队列满/在途首轮/容量/离线登记）→ 意图保持 pending，兜底扫查重试，
//     零错误上抛（受理已成立，投递滞留不是受理失败）。
func (s *Service) submitIntent(spaceID string, it collab.DispatchIntent, rebind bool) error {
	if rebind { // 兜底重试路径自愈登记（幂等；登记丢失形态如 node 重新初始化数据目录）
		if err := s.core.SpaceBind(s.nodeIDOf(it), protocol.SpaceBindPayload{Space: Scope(spaceID), SessionID: it.SessionID}, 0); err != nil {
			if isDeterministicBindErr(err) {
				return s.cancelIntent(spaceID, it, "绑定登记被目标 node 拒绝（"+err.Error()+"）：意图取消收束")
			}
			log.Printf("[hub] 空间意图投递暂缓（绑定登记未达）space=%s intent=%s: %v（下次扫查重试）", spaceID, it.RequestID, err)
			return nil
		}
	}
	ref, dup, err := s.core.SubmitSpaceTurn(it.SessionID, core.SessionTurnRequest{
		Prompt:    it.Note,
		TurnSeq:   it.TurnSeq,
		RequestID: it.RequestID,
	}, systemSubject().String(), Scope(spaceID))
	if err != nil {
		if isDeterministicTurnErr(err) {
			return s.cancelIntent(spaceID, it, "轮提交确定性失败（"+err.Error()+"）：意图取消收束")
		}
		log.Printf("[hub] 空间意图投递暂缓 space=%s intent=%s session=%s seq=%d: %v（兜底扫查重试）",
			spaceID, it.RequestID, it.SessionID, it.TurnSeq, err)
		return nil
	}
	if dup {
		// 重放命中（先前受理的轮已落库——崩溃窗恢复路径）：按轮行实物接单登记。
		if tr, ok := s.core.SessionTurnByRequest(it.SessionID, it.RequestID); ok {
			s.attachIntent(spaceID, it, tr.TaskID)
		}
		return nil
	}
	s.attachIntent(spaceID, it, ref.TaskID)
	return nil
}

// attachIntent 接单登记（pending→accepted；执行侧记账 system 主体）。失败不回滚
// 轮（轮已入账会照常执行，结果照常回流）——意图滞留 pending 由兜底扫查按轮行实物
// 补登记，重启对账按实物收敛（同 node AttachDispatchTask 失败路径语义）。
func (s *Service) attachIntent(spaceID string, it collab.DispatchIntent, taskID string) {
	if _, err := s.AttachDispatchTask(spaceID, it.RequestID, taskID, systemSubject()); err != nil {
		log.Printf("[hub] 空间意图接单登记失败 space=%s intent=%s task=%s: %v（轮已受理，扫查/重启对账收敛）",
			spaceID, it.RequestID, taskID, err)
		return
	}
	log.Printf("[hub] 中心空间派发投递 space=%s member=%s intent=%s task=%s session=%s seq=%d",
		spaceID, it.Member, it.RequestID, taskID, it.SessionID, it.TurnSeq)
}

// cancelIntent 确定性不可投递的意图收束（system 主体取消——零执行事实，取消是
// 最如实的账；失败滞留由重启对账兜底）。返回 CodeTurnFailed 操作错误（受理已
// 成立、投递失败如实可见）。
func (s *Service) cancelIntent(spaceID string, it collab.DispatchIntent, note string) error {
	if _, err := s.ConvergeIntent(spaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID, note, systemSubject()); err != nil {
		log.Printf("[hub] 不可投递意图取消收束失败 space=%s intent=%s: %v（重启对账兜底）", spaceID, it.RequestID, err)
		return &Error{Code: CodeTurnFailed, Msg: note}
	}
	log.Printf("[hub] 中心空间意图取消收束 space=%s intent=%s: %s", spaceID, it.RequestID, note)
	return &Error{Code: CodeTurnFailed, Msg: note}
}

// nodeIDOf 意图目标 node（兜底重试登记的寻址：会话注册表 node 绑定为唯一真相；
// 会话缺失返回空串——SpaceBind 对空 node 的发送失败按暂缓处理）。
func (s *Service) nodeIDOf(it collab.DispatchIntent) string {
	if sv, ok := s.core.GetSession(it.SessionID); ok {
		return sv.NodeID
	}
	return ""
}

// isDeterministicTurnErr 轮提交确定性失败（重试永不成功——取消收束）。
func isDeterministicTurnErr(err error) bool {
	return errors.Is(err, core.ErrSessionNotFound) ||
		errors.Is(err, core.ErrSessionClosed) ||
		errors.Is(err, core.ErrSessionBindingChanged) ||
		errors.Is(err, core.ErrSessionAgentUnsupported) ||
		errors.Is(err, core.ErrSessionTurnConflict) ||
		errors.Is(err, core.ErrInvalid)
}

// isDeterministicBindErr 绑定登记确定性失败（node 显式拒绝——重试同一形态仍拒）。
func isDeterministicBindErr(err error) bool {
	var br *core.BindReject
	return errors.As(err, &br)
}

// bindError SpaceBind 错误 → 派发面操作错误（REST 投影）。
func bindError(err error) error {
	var br *core.BindReject
	switch {
	case errors.Is(err, core.ErrNodeOffline):
		return &Error{Code: CodeNodeOffline, Msg: "目标 node 不在线，绑定登记未达：" + err.Error() + "（node 重连后重试；已受理意图不受影响——轮任务持久排队补投）"}
	case errors.Is(err, core.ErrBindTimeout):
		return &Error{Code: CodeBindTimeout, Msg: "绑定登记应答超时：" + err.Error() + "（node 无响应；稍后重试）"}
	case errors.As(err, &br):
		return &Error{Code: CodeBindRejected, Msg: err.Error()}
	}
	return &Error{Code: CodeBindRejected, Msg: err.Error()}
}

// ---------- 结果收敛（可靠结果映射） ----------

// convergeOnTaskTerminal 任务首落终态钩子（装配期经 core.AddOnTaskTerminal 注册；
// core 结果仲裁保证归属（TargetNode==env.From）与租约等值后才落终态——迟到/伪
// 来源结果到不了本钩子）。中心空间轮（Session.Space 恒 hub:<id>）按终态分类收敛：
//   - completed → converged（执行事实成立）；
//   - failed → converged + 记账码如实注记（失败是已收束的执行事实）；
//   - cancelled → cancelled + 系统暂停停队（人工介入优先，resume 显式放行）；
//   - needs_review 等不确定 → 不收敛：意图留待核对（在途），绝不猜测、绝不重跑、
//     绝不自动重发另一节点；
//   - 任务非空间轮 / scope 词形非法 → 零操作（普通任务与异常帧零牵连）。
//
// 已接受轮次事实照常落账，不触发暂停空间的下一步（收敛不 kick——结果落账不自动
// 推进，N04；驱动者显式再派发）。
func (s *Service) convergeOnTaskTerminal(t protocol.Task) {
	if t.Session == nil || t.Session.TurnSeq <= 0 || t.Result == nil {
		return
	}
	spaceID := collab.HubScopeID(t.Session.Space)
	if spaceID == "" {
		return // 非中心空间轮（本机空间轮/普通轮）：本服务零操作
	}
	outcome, note, halt := classifyTaskFact(t)
	if outcome == "" {
		log.Printf("[hub] 中心空间轮终不确定，留待核对（不收敛不重跑不转投）space=%s session=%s seq=%d task=%s status=%s err=%q",
			spaceID, t.Session.SessionID, t.Session.TurnSeq, t.ID, t.Status, t.Result.Error)
		return
	}
	it, ok := s.intentByTurn(spaceID, t.Session.SessionID, t.Session.TurnSeq)
	if !ok {
		return // 无在途意图锚（重放/已收敛）：零操作
	}
	if _, err := s.ConvergeIntent(spaceID, it.RequestID, outcome, it.TaskID, note, systemSubject()); err != nil {
		log.Printf("[hub] 中心空间意图收敛落账失败 space=%s intent=%s: %v（重启对账按实物重试）", spaceID, it.RequestID, err)
		return
	}
	log.Printf("[hub] 中心空间意图收敛 space=%s intent=%s → %s（%s）", spaceID, it.RequestID, outcome, note)
	if halt {
		if _, applied, perr := s.Store.SystemPause(spaceID, "成员轮被取消（人工介入）：新增派发暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[hub] 取消后系统暂停失败 space=%s: %v", spaceID, perr)
		} else if applied {
			log.Printf("[hub] 中心空间已因成员轮取消自动暂停 space=%s", spaceID)
		}
	}
}

// classifyTaskFact 任务终态 → 意图收敛分类（outcome 空 = 不确定留待核对；与 node
// classifySpaceTurn 同表：completed→converged / failed→记账码如实 converged /
// cancelled→cancelled+停队 / 其余（needs_review 等）→留待核对）。
func classifyTaskFact(t protocol.Task) (outcome, note string, halt bool) {
	code := ""
	if t.Result != nil {
		code = t.Result.Error
	}
	if i := strings.Index(code, ":"); i > 0 {
		code = code[:i]
	}
	switch t.Status {
	case protocol.StatusCompleted:
		return string(collab.IntentConverged), "成员轮完成（执行事实落账）", false
	case protocol.StatusFailed:
		return string(collab.IntentConverged), "failed:" + code + "（轮终失败，按记账码如实落账）", false
	case protocol.StatusCancelled:
		return string(collab.IntentCancelled), firstSegment(code, "cancelled"), true
	default:
		return "", "", false
	}
}

// firstSegment 错误首段或缺省词（同 node spacedispatch）。
func firstSegment(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

// intentByTurn 按意图锚（session+seq）找在途意图（迟到结果消费的锚核验：意图与
// 任务同轮号才收敛——防伪任务帧冒领意图）。登记序扫描，意图列表有界。
func (s *Service) intentByTurn(spaceID, sessionID string, seq int64) (collab.DispatchIntent, bool) {
	for _, it := range s.Intents(spaceID) {
		if it.SessionID == sessionID && it.TurnSeq == seq && it.Status.InFlight() {
			return it, true
		}
	}
	return collab.DispatchIntent{}, false
}

// ---------- 恢复对账（重启映射） ----------

// recover 重启对账闭环（装配期一次性；先于兜底扫查）：在途意图按账本实物分类，
// 绝不自动重跑——
//   - pending：先查轮行实物——有实物（受理→投递崩溃窗：轮已落库、接单登记未及
//     落盘）接单登记收敛到 accepted（任务已终态则立即按实物分类——终态钩子不再
//     重放）；无实物取消收束（「进程恢复不得凭回调自动重跑」，恢复后由驱动者显式
//     重新派发）；
//   - accepted：任务存在且终态 → 按实物分类（cancelled 对齐活路径系统暂停停队）；
//     任务存在非终态 → 留在途（任务链补投/执行中，结果到达时终态钩子收敛——不把
//     失联解释为没执行）；任务已过保留期清理 → 留待核对（事实不可考，绝不猜测、
//     绝不自动重发另一节点）。
func (s *Service) recover() {
	for _, it := range s.RecoverIntents() {
		switch it.Status {
		case collab.IntentPending:
			tr, ok := s.core.SessionTurnByRequest(it.SessionID, it.RequestID)
			if !ok {
				if _, err := s.ConvergeIntent(it.SpaceID, it.RequestID, string(collab.IntentCancelled), it.TaskID,
					"中心恢复对账：未投递意图不自动重跑（恢复后显式重新派发）", systemSubject()); err != nil {
					log.Printf("[hub] 重启对账：pending 意图取消收束失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
					continue
				}
				log.Printf("[hub] 重启对账：未投递意图已取消收束 space=%s intent=%s member=%s", it.SpaceID, it.RequestID, it.Member)
				continue
			}
			// 有轮行实物：接单登记收敛（同文重放路径 AttachDispatchTask 幂等命中
			// 原结果；已收敛意图 attach 报 illegal_transition → 按实物分类兜底）。
			if _, err := s.AttachDispatchTask(it.SpaceID, it.RequestID, tr.TaskID, systemSubject()); err == nil {
				log.Printf("[hub] 重启对账：崩溃窗意图接单登记收敛 space=%s intent=%s task=%s", it.SpaceID, it.RequestID, tr.TaskID)
			}
			s.convergeIntentByTaskFact(it, "pending+实物")
		case collab.IntentAccepted:
			s.convergeIntentByTaskFact(it, "accepted")
		}
	}
}

// convergeIntentByTaskFact 按任务实物收敛单条在途意图（重启对账共用体）：任务
// 终态 → 分类收敛；非终态 → 留在途；任务已清理 → 留待核对。失败（落盘）留待
// 下次启动重试。
func (s *Service) convergeIntentByTaskFact(it collab.DispatchIntent, via string) {
	taskID := it.TaskID
	if taskID == "" {
		// pending 崩溃窗回退路径：轮行实物即任务锚。
		if tr, ok := s.core.SessionTurnByRequest(it.SessionID, it.RequestID); ok {
			taskID = tr.TaskID
		}
	}
	if taskID == "" {
		log.Printf("[hub] 重启对账：意图无任务实物，留待核对 space=%s intent=%s via=%s", it.SpaceID, it.RequestID, via)
		return
	}
	t, ok := s.core.GetTask(taskID)
	if !ok {
		log.Printf("[hub] 重启对账：意图任务已过保留期清理，留待核对 space=%s intent=%s task=%s via=%s", it.SpaceID, it.RequestID, taskID, via)
		return
	}
	if t.Result == nil {
		log.Printf("[hub] 重启对账：意图任务非终态，留在途等结果 space=%s intent=%s task=%s state=%s via=%s", it.SpaceID, it.RequestID, taskID, t.Status, via)
		return
	}
	outcome, note, halt := classifyTaskFact(t)
	if outcome == "" {
		log.Printf("[hub] 重启对账：轮终不确定（%s），意图留待核对 space=%s intent=%s via=%s", t.Status, it.SpaceID, it.RequestID, via)
		return
	}
	if _, err := s.ConvergeIntent(it.SpaceID, it.RequestID, outcome, it.TaskID, note+"（重启对账）", systemSubject()); err != nil {
		log.Printf("[hub] 重启对账：意图收敛失败 space=%s intent=%s: %v（下次启动重试）", it.SpaceID, it.RequestID, err)
		return
	}
	log.Printf("[hub] 重启对账：意图按任务实物收敛 space=%s intent=%s → %s via=%s", it.SpaceID, it.RequestID, outcome, via)
	if halt {
		if _, applied, perr := s.Store.SystemPause(it.SpaceID, "重启对账：成员轮取消（人工介入）落账，新增派发暂停，显式 resume 恢复"); perr != nil {
			log.Printf("[hub] 重启对账取消停队系统暂停失败 space=%s: %v", it.SpaceID, perr)
		} else if applied {
			log.Printf("[hub] 重启对账：空间已因取消意图收敛暂停停队 space=%s", it.SpaceID)
		}
	}
}

// ---------- 兜底扫查 ----------

// sweepLoop 周期扫查（仅扫有在途意图的空间）：pending 意图补投递（轮行实物补接
// 单登记；无实物重新提交——绑定登记幂等自愈）。随 Close 停止。
func (s *Service) sweepLoop() {
	defer close(s.done)
	tk := time.NewTicker(sweepInterval)
	defer tk.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tk.C:
			for _, sp := range s.Spaces() {
				if s.InFlightCount(sp.ID) > 0 {
					s.sweepSpace(sp.ID)
				}
			}
		}
	}
}

// sweepSpace 单空间滞留投递补推进（只动 pending；accepted 留在途等结果）。
func (s *Service) sweepSpace(spaceID string) {
	for _, it := range s.Intents(spaceID) {
		if it.Status != collab.IntentPending {
			continue
		}
		// 轮行实物在先（崩溃窗）：补接单登记即收敛到 accepted。
		if tr, ok := s.core.SessionTurnByRequest(it.SessionID, it.RequestID); ok {
			s.attachIntent(spaceID, it, tr.TaskID)
			continue
		}
		if err := s.submitIntent(spaceID, it, true); err != nil {
			log.Printf("[hub] 空间滞留意图补投递失败 space=%s intent=%s: %v（下次扫查重试）", spaceID, it.RequestID, err)
		}
	}
}

// ---------- 询价辅助 ----------

// intentSeqHighWater 本会话全部在途意图的最大已声称轮号（受理期预留基准成分：
// pending+accepted 都计入——占用的 seq 不能被新受理复用；与 node
// spaceIntentSeqHighWater 同口径）。会话未绑定空间返回 0。
func (s *Service) intentSeqHighWater(sessionID string) int64 {
	spaceID, ok := s.SessionOwnerSpace(sessionID)
	if !ok {
		return 0
	}
	var hw int64
	for _, it := range s.Intents(spaceID) {
		if it.SessionID == sessionID && it.Status.InFlight() && it.TurnSeq > hw {
			hw = it.TurnSeq
		}
	}
	return hw
}

// nextTurnSeqOf 会话视图的下一轮序号（max+1；与 core session 同口径——close 不
// 占号故 Seq 连续）。
func nextTurnSeqOf(sv *core.SessionView) int64 {
	var max int64
	for _, t := range sv.Turns {
		if t.Seq > max {
			max = t.Seq
		}
	}
	return max + 1
}
