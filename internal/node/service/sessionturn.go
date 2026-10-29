// sessionturn.go 会话轮执行与本地会话入口（T2 持久会话，SPEC 六.五「会话与
// 轮次」；上游 docs/TASK-agent-session-contract.md）。
//
// 执行路径：会话轮任务（Task.Session != nil）在 execute 的既有纪律内分支执行——
// workdir 互斥、并发信号量、过期复检、started/finished 落盘、结果回传与重发全部
// 与普通任务共用（投递链白拿），仅执行器从 runner 换为 sessionx 同步内核
//（runSessionTurn）；普通任务路径零变化。身份分离见 protocol.SessionTurnRef。
//
// 轮终映射（四路分离的投影，写进 TaskResult 供 hub 轮终回写）：
//   - 内核三路成立 → completed + TurnOutcome{NativeIDOk:true}（WorkResult 沿
//     内核判定，T2 恒 unverified）；
//   - 内核失败/守卫拒绝 → failed + TurnOutcome{NativeIDOk:false} + Error 留痕；
//   - 服务关闭/取消打断 → 沿 execute 既有 interrupted/cancelled 收尾（平台不
//     确定，hub 按 failed 留痕——SPEC 六.五写明，不引入新审批状态机）。
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionPermissionDenylist 提权档位拒绝清单（T2 红线：权限模式绝不注入 yolo；
// 与 hub core 拒绝清单同源双防线——hub 提交口拦截在先，node 执行口复核在后，
// 卡在两侧之间变更时由本表兜底）。codex danger-full-access / zcode yolo 为两家
// CLI 的全权档位。
var sessionPermissionDenylist = map[string]bool{
	"yolo":               true,
	"danger-full-access": true,
}

// sessionTurnError 会话轮执行错误码（TaskResult.Error 首位，供 hub 轮行留痕与
// 调用方判定；语义与 sessionx 显式拒绝面对齐）。
const (
	sessionErrUnsupported = "session_unsupported"      // node 未装配会话内核
	sessionErrAgent       = protocol.CodeAgentNotFound // agent 不在会话装配表
	sessionErrNotFound    = "session_not_found"        // 平台会话无内核映射
	sessionErrClosed      = "session_closed"           // 会话已关闭
	sessionErrBinding     = "binding_changed"          // 绑定漂移（适配器/权限）
	sessionErrExists      = "session_exists"           // 平台 ID 重复（极端窗口）
	sessionErrPermission  = "permission_denied"        // 提权档位断言
	sessionErrInput       = "input_invalid"            // cwd/prompt 非法
	sessionErrTurnFailed  = "turn_failed"              // 内核轮次失败（退出码/解析/原生 ID）
)

// runSessionTurn 执行一轮会话（execute 分支入口；task.Session 必非 nil）。
// 返回值沿任务结果链：completed/failed + TurnOutcome 产物。
func (s *Service) runSessionTurn(ctx context.Context, task protocol.Task) protocol.TaskResult {
	ref := task.Session
	failed := func(code, msg string) protocol.TaskResult {
		log.Printf("[node] 会话轮失败 task=%s session=%s seq=%d op=%s: %s", task.ID, ref.SessionID, ref.TurnSeq, ref.Op, msg)
		return protocol.TaskResult{
			Status:     protocol.ResultFailed,
			ExitCode:   protocol.ExitCodeNotRun,
			Error:      code + ": " + msg,
			FinishedAt: protocol.NowUTC(),
			Turn:       &protocol.TurnOutcome{TurnSeq: ref.TurnSeq, NativeIDOk: false, WorkResult: protocol.WorkUnverified},
		}
	}
	if s.sessions == nil {
		return failed(sessionErrUnsupported, "本机未装配会话内核（agent 未声明 session_adapter 或 node 版本过旧）")
	}
	if ref.PermissionMode != "" && sessionPermissionDenylist[ref.PermissionMode] {
		return failed(sessionErrPermission, fmt.Sprintf("权限模式 %q 为全权档位，平台拒绝执行（提权须本机主人显式改配）", ref.PermissionMode))
	}
	switch ref.Op {
	case protocol.SessionTurnOpCreate:
		if ref.Cwd == "" {
			return failed(sessionErrInput, "create 轮缺 cwd（会话工作目录必填）")
		}
		// 首轮绑定断言核验（T2-R1 必修2 / T2-R2 补完，绝不带漂移绑定 spawn CLI）：
		// 帧断言值（适配器 + 权限模式）与本机装配表规范化后的实际配置同口核对——
		// 离线暂存期间本机改配重启（换适配器或权限提档）后，hub 旧绑定不再被静默
		// 接受执行。权限不变而适配器漂移同样拒绝：适配器换了，原生会话 ID 就会被
		// 交给另一 CLI（实测反例入仓）。断言缺省 = 信任本机配置（旧版 hub 帧无
		// 断言字段的形态，真相源即本机）；续轮不依赖帧断言（node 侧存储绑定守卫
		// 与 hub 注册表守卫覆盖）。
		adapter, perm, ok := s.sessions.BindingOf(task.TargetAgent)
		if !ok {
			return failed(sessionErrAgent, fmt.Sprintf("agent %q 不在本机会话装配表（配置可能已删除或改名），拒绝首轮执行", task.TargetAgent))
		}
		if ref.Adapter != "" && ref.Adapter != adapter {
			return failed(sessionErrBinding, fmt.Sprintf("适配器断言 %q ≠ 本机实际配置 %q（权限断言 %q/实际 %q，离线暂存期间配置可能已漂移）；拒绝执行，绝不静默换绑", ref.Adapter, adapter, ref.PermissionMode, perm))
		}
		if ref.PermissionMode != "" && ref.PermissionMode != perm {
			return failed(sessionErrBinding, fmt.Sprintf("权限断言 %q ≠ 本机实际配置 %q（adapter %s，离线暂存期间配置可能已漂移）；拒绝执行，绝不静默换绑", ref.PermissionMode, perm, adapter))
		}
		sess, turn, err := s.sessions.CreateWithID(ctx, ref.SessionID, task.TargetAgent, ref.Cwd, promptOf(task))
		if err != nil {
			return failed(sessionErrCodeOf(err), err.Error())
		}
		log.Printf("[node] 会话首轮建立 task=%s session=%s native=%s adapter=%s perm=%s",
			task.ID, sess.ID, sess.NativeID, sess.Adapter, sess.PermissionMode)
		return turnCompleted(ref.TurnSeq, turn)
	case protocol.SessionTurnOpSend:
		sess, turn, err := s.sessions.Send(ctx, ref.SessionID, promptOf(task))
		if err != nil {
			return failed(sessionErrCodeOf(err), err.Error())
		}
		log.Printf("[node] 会话续轮完成 task=%s session=%s seq=%d（原生线程 %s）",
			task.ID, sess.ID, turn.Seq, sess.NativeID)
		return turnCompleted(ref.TurnSeq, turn)
	case protocol.SessionTurnOpClose:
		// 关闭是管理动作收尾：目标态已达成的三类（已关闭/无映射/agent 配置漂移后
		// 的残账）一律幂等 completed——注册表是关闭决定的权威，node 清理为收尾
		// 非前提；真实错误（持久化失败）照实 failed。
		_, err := s.sessions.Close(ref.SessionID)
		switch {
		case err == nil,
			errors.Is(err, sessionx.ErrSessionClosed),
			errors.Is(err, sessionx.ErrSessionNotFound),
			errors.Is(err, sessionx.ErrAgentMissing):
			log.Printf("[node] 会话关闭收尾 task=%s session=%s（%s）", task.ID, ref.SessionID, closeOutcomeNote(err))
			return protocol.TaskResult{
				Status:     protocol.ResultCompleted,
				ExitCode:   0,
				FinishedAt: protocol.NowUTC(),
				Turn:       &protocol.TurnOutcome{TurnSeq: 0, NativeIDOk: true, WorkResult: protocol.WorkUnverified},
			}
		default:
			return failed(sessionErrTurnFailed, err.Error())
		}
	default:
		return failed(sessionErrInput, fmt.Sprintf("未知会话轮操作 %q", ref.Op))
	}
}

// turnCompleted 内核轮次成功 → 任务结果（回复正文经 FinalAnswer 承载；TurnOutcome
// 携轮序号/原生 ID 校验/工作结果/用量——hub 轮终回写的数据源）。
func turnCompleted(seq int64, turn *protocol.Turn) protocol.TaskResult {
	res := protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		ExitCode:    0,
		FinalAnswer: turn.Response,
		FinishedAt:  protocol.NowUTC(),
		Turn: &protocol.TurnOutcome{
			TurnSeq:    seq,
			NativeIDOk: true,
			WorkResult: turn.WorkResult,
			Usage:      turn.Usage,
		},
	}
	if turn.Status != protocol.TurnOK {
		// 防御位：内核三路未成立不应出现在成功分支（正常不可达）；如实降级失败，
		// 绝不把解析不完整的轮次冒充完成。
		res.Status = protocol.ResultFailed
		res.ExitCode = turn.ExitCode
		res.Error = sessionErrTurnFailed + ": " + turn.Error
		res.Turn.NativeIDOk = false
	}
	return res
}

// sessionErrCodeOf 内核错误 → 轮次错误码（首位可判定；细节随 msg 留痕）。
func sessionErrCodeOf(err error) string {
	switch {
	case errors.Is(err, sessionx.ErrUnknownAgent):
		return sessionErrAgent
	case errors.Is(err, sessionx.ErrSessionNotFound):
		return sessionErrNotFound
	case errors.Is(err, sessionx.ErrSessionClosed):
		return sessionErrClosed
	case errors.Is(err, sessionx.ErrSessionExists):
		return sessionErrExists
	case errors.Is(err, sessionx.ErrAgentMissing):
		return sessionErrAgent
	case errors.Is(err, sessionx.ErrBindingChanged):
		return sessionErrBinding
	}
	return sessionErrTurnFailed
}

// closeOutcomeNote 关闭收尾日志注记（幂等达成 vs 正常关闭）。
func closeOutcomeNote(err error) string {
	switch {
	case err == nil:
		return "已关闭"
	case errors.Is(err, sessionx.ErrSessionClosed):
		return "本就已关闭，幂等达成"
	case errors.Is(err, sessionx.ErrSessionNotFound):
		return "无内核映射（本机无此会话），幂等达成"
	default:
		return "agent 配置已漂移，残账随关闭放行"
	}
}

// promptOf 会话轮输入正文（任务 stdin 即提示词；会话轮不使用 args/env 通道）。
func promptOf(task protocol.Task) string { return task.Input.Stdin }

// LocalSessionTurnRequest 本地会话轮提交请求（localapi 本机会话端点数据面）。
type LocalSessionTurnRequest struct {
	// NodeID 本机节点 ID（localapi 边界锁已校验为配置值）。
	NodeID string
	// SessionID 平台会话 ID；空 = 新建会话（ID 本机分配，create 恒 seq=1）。
	// 非空 = 续投既有会话（尚无成功首轮时按 create 重试，序号接内核账本）。
	SessionID string
	// Agent 目标 agent 名（仅新建必填且须在会话装配表；续投沿内核存储绑定，
	// 传入值被忽略——防指定漂移）。
	Agent string
	// Cwd 会话工作目录（新建必填，本机绝对路径；续投沿内核存储绑定，忽略传入值）。
	Cwd string
	// Prompt 本轮输入。
	Prompt string
}

// SubmitSessionTurnLocal 本地会话轮提交（localapi 本机会话端点数据面，T3 本机
// 控制台的前置）：构造会话轮任务 → 账本入账（origin=local，幂等键=本地轮键）→
// 交既有调度执行（与 hub 下发会话轮同一 runSessionTurn，投递差异只在无帧回传
// ——local 任务不向 hub 发帧，sendRef/sendResult origin 堵点既有语义）。
//
// 与 SubmitLocalDirect 同型校验：目标必须本机装配表内且声明会话能力；seq 由
// 入口分配（create=1 起新会话，续投=账本 max+1）且在 localMu 互斥段内「推导→
// 入账」原子预留（T2-R1 建议3：与 hub 侧写事务内分配轮号同一原则——多个并发
// 续投各得连续序号，绝不撞号/撞幂等键）。幂等键 "lsturn:<sid>:<seq>" 仅本地
// 账本作用域（本地无响应丢失重放面，键为防御位）。
func (s *Service) SubmitSessionTurnLocal(req LocalSessionTurnRequest) (*LocalTurnAck, error) {
	if s.sessions == nil {
		return nil, &RemoteError{Code: sessionErrUnsupported, Msg: "本机未装配会话内核（agent 未声明 session_adapter）"}
	}
	s.localMu.Lock()
	op, seq, sessionID, cwd, agentName, err := s.localSessionOp(req)
	if err != nil {
		s.localMu.Unlock()
		return nil, err
	}
	now := protocol.NowUTC()
	task := protocol.Task{
		ID:          protocol.NewTaskID(),
		TraceID:     protocol.NewTraceID(),
		TargetNode:  req.NodeID,
		TargetAgent: agentName,
		Input:       protocol.TaskInput{Stdin: req.Prompt},
		SubmittedBy: "local",
		IdemKey:     fmt.Sprintf("lsturn:%s:%d", sessionID, seq),
		Status:      protocol.StatusSubmitted,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpireAt:    now.Add(protocol.TaskRetention),
		Session: &protocol.SessionTurnRef{
			SessionID: sessionID,
			TurnSeq:   seq,
			Op:        op,
			Cwd:       cwd,
		},
	}
	if err := s.cfg.Ledger.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		s.localMu.Unlock()
		return nil, fmt.Errorf("本地入账失败: %w", err)
	}
	s.localMu.Unlock() // 入账即序号预留完成，执行异步不持锁
	s.execute(task)
	return &LocalTurnAck{SessionID: sessionID, TurnSeq: seq, Op: op, TaskID: task.ID, TraceID: task.TraceID}, nil
}

// LocalTurnAck 本地会话轮受理回执（同步语义与 hub TurnRef 同型：受理即回引用，
// 轮终经本机会话详情/账本查询）。
type LocalTurnAck struct {
	SessionID string `json:"session_id"`
	TurnSeq   int64  `json:"turn_seq"`
	Op        string `json:"op"`
	TaskID    string `json:"task_id"`
	TraceID   string `json:"trace_id"`
}

// SessionsEnabled 会话内核是否装配（localapi 端点 503 降级判定）。
func (s *Service) SessionsEnabled() bool { return s.sessions != nil }

// localSessionOp 本地轮的操作类型与序号推导（与 hub SubmitTurn 同一账本推导
// 语义：无成功 create 轮即 create；seq = 平台账本 max+1（含在途轮，localMu 互斥
// 段内调用即原子预留——T2-R1 建议3，与 hub 写事务内分配轮号同原则），新会话恒
// 1）。返回执行所需的 sessionID 与 cwd（新建=本机分配 ID+请求 cwd；续投沿内核
// 存储绑定，防路径漂移）。调用方须持 s.localMu。
func (s *Service) localSessionOp(req LocalSessionTurnRequest) (op string, seq int64, sessionID, cwd, agentName string, rerr error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", 0, "", "", "", &RemoteError{Code: sessionErrInput, Msg: "prompt 必填"}
	}
	if req.SessionID == "" {
		// 新会话：ID 本机分配（与 hub 注册表同形 "s-"+hex），create 恒 seq=1；
		// agent 必须在会话装配表（create 路径显式给名）
		if !s.sessions.HasAgent(req.Agent) {
			return "", 0, "", "", "", &RemoteError{Code: sessionErrAgent,
				Msg: fmt.Sprintf("agent %q 未声明会话能力（session_adapter 未配置）或不在本机配置", req.Agent)}
		}
		if strings.TrimSpace(req.Cwd) == "" {
			return "", 0, "", "", "", &RemoteError{Code: sessionErrInput, Msg: "新建会话须给 cwd（会话工作目录）"}
		}
		if !filepath.IsAbs(req.Cwd) {
			return "", 0, "", "", "", &RemoteError{Code: sessionErrInput,
				Msg: fmt.Sprintf("cwd 必须为本机绝对路径，得 %q", req.Cwd)}
		}
		return protocol.SessionTurnOpCreate, 1, protocol.NewSessionID(), req.Cwd, req.Agent, nil
	}
	// 续投既有会话：agent 沿内核存储绑定（调用方不传名，防指定漂移）
	sess, ok := s.sessions.Status(req.SessionID)
	if !ok {
		return "", 0, "", "", "", &RemoteError{Code: sessionErrNotFound, Msg: "会话不存在（内核无映射）"}
	}
	if sess.Status != protocol.SessionOpen {
		return "", 0, "", "", "", &RemoteError{Code: sessionErrClosed, Msg: "会话已关闭（拒绝续轮）"}
	}
	for _, t := range sess.Turns {
		if t.Status == protocol.TurnOK {
			return protocol.SessionTurnOpSend, localNextTurnSeq(sess, s.cfg.Ledger, req.SessionID), sess.ID, sess.Cwd, sess.AgentName, nil
		}
	}
	// 尚无成功首轮：续投仍为 create（首轮失败后重试，内核无映射残留）。
	// 首轮独占：在途 create 未终态前拒绝新轮（与 hub 预检同语义——本地在途
	// 判定按账本条目现态）。
	for _, e := range s.cfg.Ledger.View() {
		if e.Task.Session != nil && e.Task.Session.SessionID == req.SessionID && e.State != ledger.StateFinished {
			return "", 0, "", "", "", &RemoteError{Code: "turn_in_flight",
				Msg: fmt.Sprintf("首轮（seq=%d）尚在执行，成功建立原生映射前不接受新轮", e.Task.Session.TurnSeq)}
		}
	}
	return protocol.SessionTurnOpCreate, localNextTurnSeq(sess, s.cfg.Ledger, req.SessionID), sess.ID, sess.Cwd, sess.AgentName, nil
}

// localNextTurnSeq 本机平台轮号推导（与 hub store nextTurnSeq 同一语义：既有轮
// max+1；close 不占号）。基准取双源较大者——内核 TurnCount（持久、含全部已执行
// 轮）与账本受理高水位 SessionTurnHighWater（含在途未终轮与已清理条目的受理序
// 号，localMu 互斥段内推导+入账即原子预留——T2-R1 建议3，与 hub 写事务内分配
// 轮号同一原则）。T2-R2 N4：高水位与受理同事务推进并随账本文件持久化，取消/
// 过期/重启/保留期清理后不回退——替换原先「扫描账本现存条目取 max」的口径
//（条目被 CleanupAcked 清理后即回退、与 hub 持久轮行漂移）；内核 TurnCount
// 单独用会撞并发预留（执行收束才推进）且不含未进 CLI 的受理轮，二者取大互为
// 补位，谁都不替代谁。
func localNextTurnSeq(sess protocol.Session, l *ledger.Ledger, sessionID string) int64 {
	base := int64(sess.TurnCount)
	if hw := l.SessionTurnHighWater(sessionID); hw > base {
		base = hw
	}
	return base + 1
}

// CloseSessionLocal 本地关闭会话（同步直调内核——本机操作无投递链；幂等语义同
// hub 侧：已关闭/无映射即达成）。返回关闭后的会话快照。创建在途时内核侧共用
// 写者门串行化：本调用等首轮收束后关闭（会话绝不以 open 复活，T2-R1 必修4）；
// 映射尚未发布且创建未开始时，内核落持久化关闭墓碑——后续到达的创建不再发布。
func (s *Service) CloseSessionLocal(sessionID string) (protocol.Session, error) {
	if s.sessions == nil {
		return protocol.Session{}, &RemoteError{Code: sessionErrUnsupported, Msg: "本机未装配会话内核"}
	}
	sess, err := s.sessions.Close(sessionID)
	switch {
	case err == nil,
		errors.Is(err, sessionx.ErrSessionClosed),
		errors.Is(err, sessionx.ErrSessionNotFound),
		errors.Is(err, sessionx.ErrAgentMissing):
		return sess, nil // 幂等达成（快照可能零值——无映射时无快照可返）
	default:
		return sess, err
	}
}

// SessionLocal 本地会话详情（内核快照直出；不存在 ok=false）。
func (s *Service) SessionLocal(sessionID string) (protocol.Session, bool) {
	if s.sessions == nil {
		return protocol.Session{}, false
	}
	return s.sessions.Status(sessionID)
}

// SessionsLocal 本地会话列表（内核快照，创建时刻升序）。
func (s *Service) SessionsLocal() []protocol.Session {
	if s.sessions == nil {
		return nil
	}
	return s.sessions.List()
}

// SessionAgentsLocal 本地会话能力名集（内核装配表投影；目录面判定会话能力用）。
func (s *Service) SessionAgentsLocal() map[string]bool {
	out := map[string]bool{}
	if s.sessions == nil {
		return out
	}
	for _, sess := range s.sessions.List() {
		out[sess.AgentName] = true
	}
	return out
}
