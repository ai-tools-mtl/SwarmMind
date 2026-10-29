// session.go 会话注册表与轮次编排（T2 持久会话，SPEC 六.五「会话与轮次」；
// 上游 docs/VISION-persistent-agent-collaboration.md 三/5.2 与
// docs/TASK-agent-session-contract.md）。
//
// 架构裁决（任务书「会话轮复用任务投递链但身份分离」）：会话轮是携带
// SessionTurnRef 身份段的**普通任务**——跨机传输走既有任务投递链（信箱离线
// 暂存、offer/ack/started/result 三段确认、幂等去重、重连补投、租约仲裁全部
// 白拿），身份严格分离：Task.Session 携平台 session id 与平台轮序号，Task ID
// 仅作为本次执行的可靠性载体（禁止把 Task ID 当会话身份，也禁止会话身份顶替
// Task 身份）。node 侧按 Session 段识别会话轮并调 sessionx 同步内核执行，轮终
// 沿任务结果链回传（TaskResult.Turn），本包在同一事务写回轮次账本。
//
// 轮次状态为投影态：未终态轮由关联任务现态实时投影（读侧），任务中间态转移
// 零侵入既有 ack/started 路径；终态轮由轮终回写落定（与任务首落终态同事务，
// result/cancel/sweep 三路径共用 writebackSessionTurn）。
//
// 幂等键（任务书：session id + turn 序号）：turn task 的 IdemKey =
// 「sturn:<session_id>:<seq>」、close 任务 = 「sclose:<session_id>」；重放判定
// 的权威在轮次账本行（任务与轮行同事务落库；行内持久化完整输入指纹 PromptHash
// 与受理时定案的 Op/TraceID——任务过保留期清理后仍按指纹同源比对回执、按留痕
// 返还原回执，响应丢失重试安全）。close 动作不占轮次序号（管理动作非对话轮）。
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionSubmitter 会话轮任务的提交方显示标签：REST 管理面固定 admin（同
// POST /tasks 口径，SPEC 六.五 提交身份；MCP 经 REST 同值，鉴权依据是令牌本身，
// 显示与审计标签不承担身份证明）。
const sessionSubmitter = "admin"

// 会话编排错误（errors.Is 判定；HTTP 映射见 api writeSessionErr）。
var (
	// ErrSessionNotFound 会话不存在（HTTP 404 session_not_found）。
	ErrSessionNotFound = errors.New("core: 会话不存在")
	// ErrSessionClosed 会话已关闭：拒绝续轮（HTTP 409 session_closed）。
	ErrSessionClosed = errors.New("core: 会话已关闭")
	// ErrSessionAgentUnsupported 目标 agent 未声明会话能力（能力卡
	// SessionAdapter 为空——旧版 node 或未配置 session_adapter；HTTP 422
	// session_unsupported，指路 node 侧配置）。
	ErrSessionAgentUnsupported = errors.New("core: 目标 agent 未声明会话能力")
	// ErrSessionBindingChanged 绑定守卫拒绝：创建时权限断言与能力卡不符，或续轮
	// 时能力卡与创建时绑定快照漂移（适配器/权限模式任一变更；HTTP 409
	// binding_mismatch）。VISION 硬要求：绑定变化可追踪，禁止静默换绑或升权；
	// node 侧 sessionx 内核守卫为第二道防线（卡在投递后漂移时兜底）。
	ErrSessionBindingChanged = errors.New("core: 会话绑定与当前能力声明不一致，拒绝静默迁移")
	// ErrSessionTurnConflict 轮次序号冲突：显式 turn_seq 跳号（gap）或与既有轮
	// 同号不同文（HTTP 409 turn_seq_conflict）。
	ErrSessionTurnConflict = errors.New("core: 轮次序号冲突")
	// ErrSessionTurnInFlight 首轮未成立期间拒绝新轮：会话尚无成功 create 轮时
	// 不允许排队第二轮（两个 create 并发在 node 侧必有一个撞显式重复 ID 拒绝，
	// 预检即拒给人话；HTTP 409 turn_in_flight）。create 成立后的续轮允许排队
	//（sessionx 单写者同会话串行，VISION「同一线程的输入排序执行」）。
	ErrSessionTurnInFlight = errors.New("core: 会话首轮尚未成立，暂不接受新轮")
)

// 会话轮任务幂等键（任务书：session id + turn 序号；close 动作不占号）。
func turnIdemKey(sessionID string, seq int64) string {
	return fmt.Sprintf("sturn:%s:%d", sessionID, seq)
}

// closeIdemKey 关闭任务幂等键（同会话重复 close 经注册表状态回执，键为防御位）。
func closeIdemKey(sessionID string) string {
	return "sclose:" + sessionID
}

// 提权档位拒绝清单（T2 红线：权限模式绝不注入 yolo——REST/MCP 显式断言值经此
// 拦截，node 侧执行前复核同一清单为第二道防线）。codex danger-full-access /
// zcode yolo 为两家 CLI 的全权档位。
var forbiddenPermissionModes = map[string]bool{
	"yolo":               true,
	"danger-full-access": true,
}

// SessionCreateRequest 建会话请求（POST /api/v1/sessions）。
type SessionCreateRequest struct {
	// NodeID 目标节点（必填）。
	NodeID string `json:"node_id"`
	// AgentName 目标 agent 名（必填；须为能力卡带 SessionAdapter 声明的会话能力）。
	AgentName string `json:"agent"`
	// Cwd 会话工作目录（必填，node 本机绝对路径；绝对性由 node 侧执行前校验——
	// hub 不做平台相关路径判定，防跨平台误拒）。
	Cwd string `json:"cwd"`
	// PermissionMode 权限模式断言值（可选）：显式传入时必须与目标能力卡声明一致
	//（核对后写入绑定快照），不符 409；缺省用 node 侧配置（能力卡值即真相）。
	// 传 yolo/全权档位一律 400——平台绝不注入提权模式，换绑走显式迁移通道。
	PermissionMode string `json:"permission_mode,omitempty"`
}

// SessionView 会话详情视图：注册表条目 + 轮次投影（每轮带实时状态；未终态轮由
// 关联任务现态投影，终态轮按留痕字段定）。嵌入条目的 Turns 被外层同名 JSON 键
// 遮蔽（深度规则，外层胜出），不出双份。
type SessionView struct {
	store.Session
	Turns []SessionTurnView `json:"turns"`
}

// SessionTurnView 单轮投影视图。
type SessionTurnView struct {
	store.SessionTurn
	// Status 轮次状态（store.TurnStatusXxx 六值；未终态=任务现态投影）。
	Status string `json:"status"`
	// TaskGone 关联任务已过保留期清理（终态轮正常注解——轮终产物随行保留，
	// 任务详情不可再查；VISION S-10：会话账本不随任务清理失联）。
	TaskGone bool `json:"task_gone,omitempty"`
}

// SessionSummary 会话列表摘要（不含轮次明细）。
type SessionSummary struct {
	ID             string           `json:"id"`
	NodeID         string           `json:"node_id"`
	AgentName      string           `json:"agent_name"`
	Adapter        string           `json:"adapter"`
	PermissionMode string           `json:"permission_mode,omitempty"`
	Cwd            string           `json:"cwd"`
	Status         string           `json:"status"`
	TurnCount      int              `json:"turn_count"`
	LastTurn       *SessionTurnView `json:"last_turn,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	ClosedAt       *time.Time       `json:"closed_at,omitempty"`
}

// TurnRef 轮次受理回执（POST turns 响应；轮终状态经详情端点查询——异步，
// 不做长阻塞请求，任务书 Send 语义）。
type TurnRef struct {
	SessionID string `json:"session_id"`
	TurnSeq   int64  `json:"turn_seq"`
	// Op 本轮回执的操作类型（create/send）：受理事务内由账本推导定案并持久化在
	// 轮行（store.SessionTurn.Op）——重放回执原样返还原轮（T2-R1 必修6：Op 不随
	// 会话后续状态改写，首轮重放不因 create 已成功被误标 send）；调用方不指定操作。
	Op string `json:"op"`
	// TaskID 关联任务 ID（可靠性载体；轮终经任务结果链回写轮次账本）。
	TaskID string `json:"task_id"`
	// TraceID 链路追踪 ID。
	TraceID string `json:"trace_id"`
}

// SessionTurnRequest 续轮请求（POST /api/v1/sessions/{id}/turns）。
type SessionTurnRequest struct {
	// Prompt 本轮输入（必填；原样作为任务 stdin 下发，node 侧 sessionx 内核
	// 另有非空校验）。
	Prompt string `json:"prompt"`
	// TurnSeq 显式轮序号（可选）：响应丢失重试安全——与既有轮同号同文回执
	// dup，同号异文 409；跳号 409。缺省由 hub 分配下一序号（无重放保护）。
	TurnSeq int64 `json:"turn_seq,omitempty"`
}

// CreateSession 建会话（注册表登记，不派发任何任务——首轮经 SubmitTurn 显式
// 提交）：目标校验（node 注册 + agent exposed + 会话能力声明）→ 绑定快照落定
//（能力卡 adapter/权限投影 + 请求 cwd）→ 分配平台 ID 落库 active。
//
// actor 为服务端验证的鉴权主体（注册表无事件账本，本参数保留签名一致性；
// 审计经首轮任务的既有提交链）。创建动作幂等语义：无幂等键，重复调用即新会话
//（会话是长命协作线程，重复创建为合法意图；去重责任在调用方）。
func (c *Core) CreateSession(req SessionCreateRequest, actor string) (*SessionView, error) {
	_ = actor
	nodeID := strings.TrimSpace(req.NodeID)
	agent := strings.TrimSpace(req.AgentName)
	cwd := strings.TrimSpace(req.Cwd)
	if nodeID == "" || agent == "" || cwd == "" {
		return nil, fmt.Errorf("%w: node_id/agent/cwd 均必填", ErrInvalid)
	}
	if m := strings.TrimSpace(req.PermissionMode); m != "" {
		if forbiddenPermissionModes[m] {
			return nil, fmt.Errorf("%w: permission_mode %q 为全权档位，平台拒绝注入（提权须 node 主人显式改配）", ErrInvalid, m)
		}
	}
	view := c.st.View()
	card, ok := lookupAgent(view, nodeID, agent)
	if !ok {
		return nil, ErrAgentNotExposed
	}
	if card.SessionAdapter == "" {
		return nil, fmt.Errorf("%w: %s/%s（node 未声明 session_adapter，会话接入须在 node.yaml 该能力下配置，旧版节点须升级）", ErrSessionAgentUnsupported, nodeID, agent)
	}
	if m := strings.TrimSpace(req.PermissionMode); m != "" && m != card.PermissionMode {
		return nil, fmt.Errorf("%w: 权限断言 %q ≠ 能力卡声明 %q（断言仅供核对，非换绑通道）", ErrSessionBindingChanged, m, card.PermissionMode)
	}
	now := c.now()
	sess := store.Session{
		ID:             protocol.NewSessionID(),
		NodeID:         nodeID,
		AgentName:      agent,
		Adapter:        card.SessionAdapter,
		PermissionMode: card.PermissionMode,
		Cwd:            cwd,
		Status:         store.SessionStatusActive,
		Turns:          []store.SessionTurn{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err := c.st.Update(func(st *store.State) error {
		// 事务内重检目标（注册/下线与预检竞态以快照为准）；绑定快照取事务内
		// 卡片（与预检漂移时以事务内为准，拒绝即副本整体丢弃零发布）。
		card, ok := lookupAgent(st, nodeID, agent)
		if !ok {
			return ErrAgentNotExposed
		}
		if card.SessionAdapter == "" {
			return ErrSessionAgentUnsupported
		}
		// 权限断言事务内重检（T2-R1 必修3）：预检后能力卡更新的竞态窗口内，显式
		// 断言必须与事务内同一张当前卡重新核对——不符整体拒绝（mutate 错误回滚
		// 零发布），禁止静默按提权后的新卡落绑定。
		if m := strings.TrimSpace(req.PermissionMode); m != "" && m != card.PermissionMode {
			return fmt.Errorf("%w: 权限断言 %q ≠ 能力卡声明 %q（事务内重检，卡片在创建竞态中已变更）", ErrSessionBindingChanged, m, card.PermissionMode)
		}
		sess.Adapter = card.SessionAdapter
		sess.PermissionMode = card.PermissionMode
		st.Sessions[sess.ID] = sess
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SessionView{Session: sess, Turns: []SessionTurnView{}}, nil
}

// SubmitTurn 提交轮次（会话轮任务落库 + 轮次账本登记同事务原子；返回受理回执，
// 轮终异步经任务结果链回写）。校验序对齐任务提交链（SPEC 4.5：目标 → 会话边界
// → 序号/重放 → 绑定守卫 → 首轮独占 → 体积/容量 → 事务内全重检）：
//
//  1. 会话存在且 active；
//  2. prompt 非空；
//  3. 操作类型由账本推导：无成功 create 轮（无 NativeIDOk 行）即 create——
//     首轮失败后的重试按 create 重来（node 侧无映射残留），对调用方透明；
//  4. 显式 turn_seq：跳号 409；与既有轮同号同文 = 响应丢失重放，返回原轮回执
//     （dup，零写入；任务已清理仍可回执——轮行是重放判定的权威，同源判定用
//     行内完整输入指纹 PromptHash 而非展示截断文本）；同号异文 409；缺省序号
//     在写事务内分配（next=max+1，事务串行保证 (session_id,seq) 唯一——T2-R1
//     必修1：并发提交不得产生同会话同序号）；
//  5. 绑定守卫：当前能力卡（adapter/权限模式）与创建时绑定快照严格相等，漂移
//     409（node 侧内核守卫为第二道防线）；
//  6. 首轮独占：create 轮在途（未终态）时拒绝新轮 409（两个 create 并发必撞
//     node 侧显式重复 ID 拒绝，预检即拒）；续轮不设限（sessionx 单写者排队）；
//  7. 体积实测 + 容量预检（与任务提交同口径，会话轮任务体含 Session 段）；
//  8. 单写事务：全重检 → 任务落库（IdemKey=会话轮键）+ 信箱入队 + 轮行登记。
func (c *Core) SubmitTurn(sessionID string, req SessionTurnRequest, actor string) (*TurnRef, bool, error) {
	view := c.st.View()
	sess, ok := view.Sessions[sessionID]
	if !ok {
		return nil, false, ErrSessionNotFound
	}
	if sess.Status != store.SessionStatusActive {
		return nil, false, fmt.Errorf("%w: %s（关闭后拒绝续轮；原生线程留存 node 侧不受影响）", ErrSessionClosed, sessionID)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, false, fmt.Errorf("%w: prompt 必填", ErrInvalid)
	}
	nextSeq := nextTurnSeq(sess)
	op, hasCreate := sessionOp(sess)
	// 显式序号：重放回执 / 冲突 / 跳号（预检；事务内重检为准）。先于首轮独占——
	// 幂等回执补拉不重校验（同 submit 链 idem 查重先于容量/依赖准入的口径），
	// 在途轮的重放仍须拿到回执而非被守卫挡住。
	if req.TurnSeq > 0 && req.TurnSeq != nextSeq {
		tr, existed := findTurn(sess, req.TurnSeq)
		if !existed {
			return nil, false, fmt.Errorf("%w: turn_seq=%d 跳号（下一序号为 %d，轮次必须连续）", ErrSessionTurnConflict, req.TurnSeq, nextSeq)
		}
		if !sameTurnPrompt(tr, prompt) {
			return nil, false, fmt.Errorf("%w: turn_seq=%d 已被不同内容占用（响应丢失重试须原样重发同文）", ErrSessionTurnConflict, req.TurnSeq)
		}
		// 原轮回执（T2-R1 必修6）：Op/TraceID 以轮行留痕为准，不随会话现态改写；
		// 旧行（无留痕字段）防御回退按现态推导。
		return &TurnRef{SessionID: sessionID, TurnSeq: tr.Seq, Op: rowTurnOp(tr, op), TaskID: tr.TaskID, TraceID: tr.TraceID}, true, nil
	}
	if !hasCreate {
		// 首轮独占（预检）：create 在途即拒（事务内重检为准）
		for _, t := range sess.Turns {
			if !t.Terminal() {
				return nil, false, fmt.Errorf("%w: 首轮（seq=%d）尚在执行，成功建立原生映射前不接受新轮", ErrSessionTurnInFlight, t.Seq)
			}
		}
	}
	// 绑定守卫（预检）
	card, ok := lookupAgent(view, sess.NodeID, sess.AgentName)
	if !ok {
		return nil, false, ErrAgentNotExposed
	}
	if card.SessionAdapter != sess.Adapter || card.PermissionMode != sess.PermissionMode {
		return nil, false, fmt.Errorf("%w: 能力卡漂移 持久=(%s/%s) 当前=(%s/%s)；换绑须显式重建会话", ErrSessionBindingChanged, sess.Adapter, sess.PermissionMode, card.SessionAdapter, card.PermissionMode)
	}

	seq := nextSeq
	if req.TurnSeq > 0 {
		seq = req.TurnSeq
	}
	reqView := protocol.TaskSubmitRequest{
		TargetNode:  sess.NodeID,
		TargetAgent: sess.AgentName,
		Input:       protocol.TaskInput{Stdin: prompt},
		SubmittedBy: sessionSubmitter,
		IdemKey:     turnIdemKey(sessionID, seq),
	}
	task := c.newTask(reqView, "")
	task.Session = &protocol.SessionTurnRef{
		SessionID:      sessionID,
		TurnSeq:        seq,
		Op:             op,
		Cwd:            sess.Cwd,
		PermissionMode: sess.PermissionMode,
		Adapter:        sess.Adapter, // 绑定快照随帧下发，node 首轮 spawn 前同口核验（T2-R2 必修2 补完）
	}
	if err := checkOfferSize(&task); err != nil {
		return nil, false, err
	}
	size := mailbox.TaskBytes(&task)
	if err := c.checkCapacity(view, sess.NodeID, size); err != nil {
		return nil, false, err
	}

	var (
		txnRef *TurnRef
		txnDup bool
		txnErr error
	)
	err := c.st.Update(func(st *store.State) error {
		s, ok := st.Sessions[sessionID]
		if !ok {
			txnErr = ErrSessionNotFound
			return nil
		}
		if s.Status != store.SessionStatusActive {
			txnErr = ErrSessionClosed
			return nil
		}
		next := nextTurnSeq(s)
		opNow, hasCreateNow := sessionOp(s)
		if req.TurnSeq > 0 && req.TurnSeq != next { // 重放/冲突先于首轮独占（同预检序）
			tr, existed := findTurn(s, req.TurnSeq)
			if !existed {
				txnErr = fmt.Errorf("%w: turn_seq=%d 跳号（下一序号为 %d）", ErrSessionTurnConflict, req.TurnSeq, next)
				return nil
			}
			if !sameTurnPrompt(tr, prompt) {
				txnErr = fmt.Errorf("%w: turn_seq=%d 已被不同内容占用", ErrSessionTurnConflict, req.TurnSeq)
				return nil
			}
			// 原轮回执（必修6）：轮行留痕权威，同预检分支口径
			txnRef = &TurnRef{SessionID: sessionID, TurnSeq: tr.Seq, Op: rowTurnOp(tr, opNow), TaskID: tr.TaskID, TraceID: tr.TraceID}
			txnDup = true
			return nil
		}
		if !hasCreateNow {
			for _, t := range s.Turns {
				if !t.Terminal() {
					txnErr = fmt.Errorf("%w: 首轮（seq=%d）尚在执行", ErrSessionTurnInFlight, t.Seq)
					return nil
				}
			}
		}
		cardNow, ok := lookupAgent(st, s.NodeID, s.AgentName)
		if !ok {
			txnErr = ErrAgentNotExposed
			return nil
		}
		if cardNow.SessionAdapter != s.Adapter || cardNow.PermissionMode != s.PermissionMode {
			txnErr = fmt.Errorf("%w: 能力卡漂移 持久=(%s/%s) 当前=(%s/%s)", ErrSessionBindingChanged, s.Adapter, s.PermissionMode, cardNow.SessionAdapter, cardNow.PermissionMode)
			return nil
		}
		// 序号事务内定案（T2-R1 必修1）：缺省序号取写事务内重算的 next（并发提交
		// 由单写串行化收敛为连续序号，绝不复用事务外捕获值）；显式序号照用。二者
		// 都防御性断言 (session_id, seq) 唯一——撞号即冲突拒绝，绝不双行同号。
		txnSeq := next
		if req.TurnSeq > 0 {
			txnSeq = req.TurnSeq
		}
		if _, exists := findTurn(s, txnSeq); exists {
			txnErr = fmt.Errorf("%w: turn_seq=%d 已被占用（事务内唯一性防御）", ErrSessionTurnConflict, txnSeq)
			return nil
		}
		taskNow := task
		taskNow.IdemKey = turnIdemKey(sessionID, txnSeq) // 序号定案后同源改键（缺省序号路径键随 seq 变）
		taskNow.Session = &protocol.SessionTurnRef{SessionID: sessionID, TurnSeq: txnSeq, Op: opNow, Cwd: s.Cwd, PermissionMode: s.PermissionMode, Adapter: s.Adapter}
		if _, exists := st.Tasks[taskNow.ID]; exists {
			txnErr = ErrInvalid // 预生成 ID 撞既有任务（概率可忽略）：防御性拒绝
			return nil
		}
		if err := checkOfferSize(&taskNow); err != nil {
			return err
		}
		if err := c.checkCapacity(st, s.NodeID, mailbox.TaskBytes(&taskNow)); err != nil {
			return err
		}
		st.Tasks[taskNow.ID] = taskNow
		if err := mailbox.Enqueue(st, taskNow.TargetNode, &taskNow); err != nil {
			return err
		}
		s.Turns = append(s.Turns, store.SessionTurn{
			Seq:        txnSeq,
			TaskID:     taskNow.ID,
			Op:         opNow,
			TraceID:    taskNow.TraceID,
			Prompt:     truncateRunes(prompt, store.MaxSessionTurnPrompt),
			PromptHash: promptFingerprint(prompt),
			CreatedAt:  c.now(),
		})
		s.UpdatedAt = c.now()
		st.Sessions[sessionID] = s
		txnRef = &TurnRef{SessionID: sessionID, TurnSeq: txnSeq, Op: opNow, TaskID: taskNow.ID, TraceID: taskNow.TraceID}
		return nil
	})
	if err != nil && txnErr == nil {
		txnErr = err
	}
	if txnErr != nil {
		return nil, false, txnErr
	}
	if txnDup {
		return txnRef, true, nil
	}
	if txnRef != nil {
		if t, ok := c.st.View().Tasks[txnRef.TaskID]; ok {
			txnRef.TraceID = t.TraceID
			c.noteTask(&t)
		}
		c.dispatch(sess.NodeID)
	}
	return txnRef, false, nil
}

// CloseSession 关闭会话（单事务：注册表置 closed + close 任务落库入信箱）。
// 注册表关闭即成立（重复 close 幂等回执 dup，响应丢失重试安全）；close 任务照走
// 投递链（离线 node 重连补执行）收尾 node 侧原生映射——注册表状态与投递状态
// 分离，node 清理为收尾动作非关闭前提。close 不占轮次序号。
func (c *Core) CloseSession(sessionID string, actor string) (*SessionView, bool, error) {
	_ = actor
	view := c.st.View()
	sess, ok := view.Sessions[sessionID]
	if !ok {
		return nil, false, ErrSessionNotFound
	}
	if sess.Status == store.SessionStatusClosed {
		return sessionViewOf(sess, view), true, nil // 幂等回执：零写入零任务
	}
	reqView := protocol.TaskSubmitRequest{
		TargetNode:  sess.NodeID,
		TargetAgent: sess.AgentName,
		SubmittedBy: sessionSubmitter,
		IdemKey:     closeIdemKey(sessionID),
	}
	task := c.newTask(reqView, "")
	task.Session = &protocol.SessionTurnRef{SessionID: sessionID, Op: protocol.SessionTurnOpClose}
	if err := checkOfferSize(&task); err != nil {
		return nil, false, err
	}
	size := mailbox.TaskBytes(&task)
	if err := c.checkCapacity(view, sess.NodeID, size); err != nil {
		return nil, false, err
	}
	now := c.now()
	var closed store.Session
	placed := false // 本请求是否落库了 close 任务（并发收敛零写入时不记账不派发）
	err := c.st.Update(func(st *store.State) error {
		s, ok := st.Sessions[sessionID]
		if !ok {
			return ErrSessionNotFound
		}
		if s.Status == store.SessionStatusClosed {
			closed = s
			return nil // 并发 close 收敛：零写入
		}
		if _, exists := st.Tasks[task.ID]; exists {
			return ErrInvalid
		}
		if err := checkOfferSize(&task); err != nil {
			return err
		}
		if err := c.checkCapacity(st, s.NodeID, mailbox.TaskBytes(&task)); err != nil {
			return err
		}
		st.Tasks[task.ID] = task
		if err := mailbox.Enqueue(st, task.TargetNode, &task); err != nil {
			return err
		}
		s.Status = store.SessionStatusClosed
		s.UpdatedAt = now
		s.ClosedAt = &now
		st.Sessions[sessionID] = s
		closed = s
		placed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if placed {
		c.noteTask(&task)
		c.dispatch(task.TargetNode)
	}
	// dup = 本请求未落库（先到的 close 已收敛或并发收敛零写入）：与既有幂等回执
	// 同语义（T2-R1 建议1，SPEC 六.五 close 行「重复 close 200+dup:true」）。
	return sessionViewOf(closed, c.st.View()), !placed, nil
}

// GetSession 会话详情（只读）：轮次投影（未终态轮按关联任务现态投影状态；
// 终态轮按留痕字段定 done/unverified/failed）+ task_gone 注解。
func (c *Core) GetSession(sessionID string) (*SessionView, bool) {
	view := c.st.View()
	sess, ok := view.Sessions[sessionID]
	if !ok {
		return nil, false
	}
	return sessionViewOf(sess, view), true
}

// ListSessions 会话列表摘要（创建时间倒序、同刻按 ID 稳定排序——同 ListWorks
// 口径；V0 低量不分页）。LastTurn 取最大 seq 轮的投影。
func (c *Core) ListSessions() []SessionSummary {
	view := c.st.View()
	out := make([]SessionSummary, 0, len(view.Sessions))
	for _, s := range view.Sessions {
		sum := SessionSummary{
			ID: s.ID, NodeID: s.NodeID, AgentName: s.AgentName,
			Adapter: s.Adapter, PermissionMode: s.PermissionMode, Cwd: s.Cwd,
			Status: s.Status, TurnCount: len(s.Turns),
			CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ClosedAt: s.ClosedAt,
		}
		for i := range s.Turns {
			if sum.LastTurn == nil || s.Turns[i].Seq > sum.LastTurn.Seq {
				tv := projectTurn(view, s.Turns[i])
				sum.LastTurn = &tv
			}
		}
		// CoW 纪律：LastTurn 已换新指针，Summary 不与已发布状态共享可变体
		if sum.ClosedAt != nil {
			t := *sum.ClosedAt
			sum.ClosedAt = &t
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// sessionViewOf 注册表条目 → 详情视图（轮次逐条投影）。
func sessionViewOf(s store.Session, view *store.State) *SessionView {
	sv := &SessionView{Session: s.Clone(), Turns: make([]SessionTurnView, 0, len(s.Turns))}
	for _, t := range s.Turns {
		sv.Turns = append(sv.Turns, projectTurn(view, t))
	}
	return sv
}

// projectTurn 单轮投影：终态行按留痕定（NativeIDOk 且无错误 → unverified/done
// 按 WorkResult 分流；否则 failed），未终态行按关联任务现态投影（pending/
// awaiting_node/running）；任务缺失按防御值 failed/turn_task_missing（正常不可
// 达：轮终回写与任务终态同事务，清理必以终态+保留期为前提）。终态轮任务已过
// 保留期清理时同样注解 task_gone（T2-R1 建议2：轮终产物随行保留，任务详情不可
// 再查——与 SessionTurnView 注释及 SPEC 六.五口径一致）。
func projectTurn(view *store.State, t store.SessionTurn) SessionTurnView {
	tv := SessionTurnView{SessionTurn: t}
	if _, ok := view.Tasks[t.TaskID]; !ok {
		tv.TaskGone = true
	}
	switch {
	case t.Terminal():
		if t.NativeIDOk && t.Error == "" {
			if t.WorkResult == protocol.WorkUnverified {
				tv.Status = store.TurnStatusUnverified
			} else {
				tv.Status = store.TurnStatusDone
			}
		} else {
			tv.Status = store.TurnStatusFailed
		}
	default:
		task, ok := view.Tasks[t.TaskID]
		if !ok {
			tv.Status = store.TurnStatusFailed
			tv.TaskGone = true
			if tv.Error == "" {
				tv.Error = "turn_task_missing"
			}
			break
		}
		switch task.Status {
		case protocol.StatusSubmitted:
			tv.Status = store.TurnStatusPending
		case protocol.StatusOffered, protocol.StatusAcknowledged:
			tv.Status = store.TurnStatusAwaitingNode
		case protocol.StatusRunning:
			tv.Status = store.TurnStatusRunning
		default:
			// 防御位：任务已终态而轮行未回写（正常不可达——同事务回写）
			tv.Status = store.TurnStatusFailed
			if tv.Error == "" {
				tv.Error = "turn_writeback_missing"
			}
		}
	}
	return tv
}

// writebackSessionTurn 会话轮终回写（任务首落终态的同一事务内调用；result/
// cancel/sweep 三终态路径共用，语义同 writebackWorkTerminal）：轮行落终态留痕
//（答复正文/原生 ID 校验/工作结果/用量/错误），先到保留（重复回写与迟到数据
// 零变更）。close 任务不占轮次号、无轮行可写，天然 no-op；会话行不存在为防御
// no-op（注册表是会话身份权威，任务迟到不复活已删条目——T2 无删除路径，防御
// 纵深）。非 completed 终态一律 NativeIDOk=false：failed/cancelled 自不待言，
// needs_review（node 中断收尾）为平台不确定——原生侧是否完成不可证，按失败
// 留痕 + Error 保留 node_restarted_midway 定位（轮次状态机无不确定值，不引入
// 新审批状态机；SPEC 六.五写明）。
func writebackSessionTurn(st *store.State, t *protocol.Task, now time.Time) {
	if t.Session == nil || t.Session.Op == protocol.SessionTurnOpClose {
		return
	}
	sess, ok := st.Sessions[t.Session.SessionID]
	if !ok {
		return
	}
	for i := range sess.Turns {
		if sess.Turns[i].Seq != t.Session.TurnSeq {
			continue
		}
		tr := &sess.Turns[i]
		if tr.Terminal() {
			return // 先到保留（防御重复回写）
		}
		tr.FinishedAt = now
		if t.Result != nil {
			tr.Response = truncateRunes(t.Result.FinalAnswer, store.MaxSessionTurnResponse)
			if t.Result.Turn != nil {
				tr.NativeIDOk = t.Result.Turn.NativeIDOk
				tr.WorkResult = t.Result.Turn.WorkResult
				tr.Usage = t.Result.Turn.Usage
			}
			if t.Status != protocol.StatusCompleted {
				tr.NativeIDOk = false
				if tr.Error == "" {
					tr.Error = t.Result.Error
				}
			} else if !tr.NativeIDOk && tr.Error == "" {
				// 防御位：completed 但内核未确认原生 ID（正常不可达——node 侧执行
				// 成功必回 NativeIDOk=true）；如实标失败留痕，不冒充完成。
				tr.Error = "turn_native_id_unconfirmed"
			}
		} else {
			// hub 侧取消（Result 留空给 node cancelled 回执合并——cancel.go）：
			// 按任务终态留痕 failed（error=取消终态值），node 迟到的取消回执合并
			// 数据不再改写轮行（先到保留——轮终产物以本行为准）。
			tr.NativeIDOk = false
			tr.Error = string(t.Status)
		}
		sess.UpdatedAt = now
		st.Sessions[sess.ID] = sess
		return
	}
}

// nextTurnSeq 下一轮序号（max+1；close 不占号故 Seq 连续）。
func nextTurnSeq(s store.Session) int64 {
	var max int64
	for _, t := range s.Turns {
		if t.Seq > max {
			max = t.Seq
		}
	}
	return max + 1
}

// sessionOp 会话当前操作类型：无成功 create 轮（无 NativeIDOk 行）即 create
//（首轮失败后重试按 create 重来）；否则 send。
func sessionOp(s store.Session) (op string, hasCreate bool) {
	for _, t := range s.Turns {
		if t.NativeIDOk {
			return protocol.SessionTurnOpSend, true
		}
	}
	return protocol.SessionTurnOpCreate, false
}

// findTurn 按序号定位轮行。
func findTurn(s store.Session, seq int64) (store.SessionTurn, bool) {
	for _, t := range s.Turns {
		if t.Seq == seq {
			return t, true
		}
	}
	return store.SessionTurn{}, false
}

// sameTurnPrompt 重放同源判定（T2-R1 必修5）：以行内完整规范化输入指纹
// （PromptHash，SHA-256）为准——展示留痕 Prompt 截断至 8KiB 不参与判定，超长
// 输入的尾部差异（恰可能是关键指令）必须 409 同号异文，绝不被截断前缀吞为重放；
// 指纹随轮行持久化，任务清理后判定依旧成立。旧行（无指纹字段）防御回退按截断
// 文本比对。
func sameTurnPrompt(t store.SessionTurn, prompt string) bool {
	if t.PromptHash != "" {
		return t.PromptHash == promptFingerprint(prompt)
	}
	return t.Prompt == truncateRunes(strings.TrimSpace(prompt), store.MaxSessionTurnPrompt)
}

// promptFingerprint 完整规范化输入的稳定指纹（与轮行留痕同一规范化：trim →
// UTF-8 清洗；SHA-256 hex）。
func promptFingerprint(prompt string) string {
	sum := sha256.Sum256([]byte(strings.ToValidUTF8(strings.TrimSpace(prompt), "\uFFFD")))
	return hex.EncodeToString(sum[:])
}

// rowTurnOp 轮行留痕的操作类型（重放回执权威）；旧行无留痕时按会话现态推导
// 防御回退（nowOp = 重放推导时点的账本推导值）。
func rowTurnOp(t store.SessionTurn, nowOp string) string {
	if t.Op != "" {
		return t.Op
	}
	return nowOp
}

// truncateRunes 按 rune 截断至上限字节（对齐 sessionx sanitize 的清洗+截断纪律：
// UTF-8 清洗在前，截断点回落完整 rune）。
func truncateRunes(s string, limit int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 {
		rn, size := utf8.DecodeLastRuneInString(s)
		if !(rn == utf8.RuneError && size == 1) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
