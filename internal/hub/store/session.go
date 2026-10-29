// session.go 会话注册表与轮次账本（T2 持久会话，SPEC 六.五「会话与轮次」；
// 上游 docs/VISION-persistent-agent-collaboration.md 三/5.2）。hub 保存可路由的
// 会话索引与轮次账本；原生会话 ID 映射与轮次留痕的执行真相在 node 侧
//（internal/node/sessionx，data_dir/sessions.json），hub 注册表承担跨机寻址、
// 绑定快照与轮终投影。写纪律与 Work 同款：写时复制（State.Clone 深拷贝后事务内
// 改副本），任务落库 + 轮次登记同事务（会话轮任务与轮次账本行原子一致）。
//
// 保留策略（VISION 5.2：活跃会话索引不随普通任务终态清理失联）：T2 会话实体
// 无自动清理、无过期——轮终产物随轮次行保留，关联任务过保留期清理后轮次账本
// 仍完整可读（任务 ID 留档供审计对账）；独立保留策略随产品化批次另行设计。
package store

import (
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 会话状态取值（Session.Status）。
const (
	// SessionStatusActive 会话存活：可续轮。
	SessionStatusActive = "active"
	// SessionStatusClosed 平台侧已关闭：拒绝续轮（close 任务仍照常投递 node
	// 收尾原生映射——注册表状态与投递状态分离，关闭先于 node 确认成立）。
	SessionStatusClosed = "closed"
)

// 轮次状态取值（SessionTurn.Status；任务书状态机六值）。
const (
	// TurnStatusPending 轮已受理、任务未送达 node（submitted，信箱排队/离线暂存）。
	TurnStatusPending = "pending"
	// TurnStatusAwaitingNode 任务已投递 node 未确认开始（offered/acknowledged）。
	TurnStatusAwaitingNode = "awaiting_node"
	// TurnStatusRunning node 执行中（任务 running）。
	TurnStatusRunning = "running"
	// TurnStatusDone 轮终执行成立且工作结果已证实。T2 无改判通道（内核恒
	// unverified），本值为保留态：验收链路显式改判（T1 protocol 契约预告）落地
	// 前无到达路径，SPEC 如实声明。
	TurnStatusDone = "done"
	// TurnStatusUnverified 轮终执行成立、工作结果未经证实。T2 完成轮恒此态
	//（内核第四路无判定通道，禁止文本关键词猜测）。
	TurnStatusUnverified = "unverified"
	// TurnStatusFailed 轮终执行不成立（退出码/解析/原生 ID/绑定守卫/内核拒绝/
	// 过期/取消/平台中断——Error 留痕细分原因）。
	TurnStatusFailed = "failed"
)

// Session 会话注册表条目（hub 全局；平台会话 ID 即 node 侧 sessionx 内核键）。
// 绑定快照（Adapter/PermissionMode/Cwd）在创建时从目标能力卡与请求落定：send
// 轮不重带 cwd/权限，续接一律以 node 侧存储绑定执行；hub 侧快照用于投递前
// 绑定守卫（能力卡漂移 → 显式拒绝，node 侧内核守卫为第二道防线）。
type Session struct {
	// ID 平台会话 ID："s-"+16 字节 hex，hub 创建时分配。
	ID string `json:"id"`
	// NodeID 目标节点。
	NodeID string `json:"node_id"`
	// AgentName 目标 agent 名（node 内唯一）。
	AgentName string `json:"agent_name"`
	// Adapter 绑定快照：会话适配器类型（能力卡 SessionAdapter 投影，创建时点）。
	Adapter string `json:"adapter"`
	// PermissionMode 绑定快照：权限模式（能力卡 PermissionMode 投影，创建时点；
	// 可能与适配器保守默认一致或为主人显式配置；空 = 卡未声明）。
	PermissionMode string `json:"permission_mode,omitempty"`
	// Cwd 绑定快照：会话工作目录（node 本机绝对路径；create 轮下发值）。
	Cwd string `json:"cwd"`
	// Status SessionStatusActive | SessionStatusClosed。
	Status string `json:"status"`
	// Turns 轮次账本（按 Seq 升序；终态行不再变更，未终态行随任务现态投影）。
	Turns []SessionTurn `json:"turns"`
	// CreatedAt 创建时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最近状态变更时刻（UTC）。
	UpdatedAt time.Time `json:"updated_at"`
	// ClosedAt 关闭时刻（未关闭 nil——指针承载 omitempty，对齐 Work.ClosedAt 口径）。
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// Clone 深拷贝会话（写时复制纪律；对齐 Work.Clone）。
func (s Session) Clone() Session {
	n := s
	n.Turns = make([]SessionTurn, len(s.Turns))
	for i, t := range s.Turns {
		n.Turns[i] = t.clone()
	}
	if s.ClosedAt != nil {
		t := *s.ClosedAt
		n.ClosedAt = &t
	}
	return n
}

// SessionTurn 轮次账本行。Status 为投影态：未终态行（FinishedAt 零值）由关联
// 任务现态实时投影（pending/awaiting_node/running，读侧 GetSession 计算，任务
// 中间态转移不写注册表——零侵入既有 ack/started 路径）；终态行（FinishedAt 非
// 零）由轮终回写落定（done/unverified/failed，随任务终态同事务，先到不再变更）。
type SessionTurn struct {
	// Seq 平台轮序号（会话内 1 起连续单调；close 动作不占号；受理事务内分配，
	// (session_id, seq) 唯一）。
	Seq int64 `json:"seq"`
	// TaskID 关联的会话轮任务 ID（可靠性载体；幂等去重与轮终回写锚点）。
	TaskID string `json:"task_id"`
	// Op 本轮受理时定案的实际操作类型（create/send；受理事务内由账本推导落定，
	// 不随会话后续状态改写——重放回执以此为准，T2-R1 必修6）。
	Op string `json:"op,omitempty"`
	// TraceID 关联任务的链路追踪 ID（受理时留痕；重放回执原样返还原回执，
	// 不依赖关联任务是否仍在保留期内，T2-R1 必修6）。
	TraceID string `json:"trace_id,omitempty"`
	// Prompt 本轮输入展示留痕（截断至 MaxSessionTurnPrompt；注册表自限——完整
	// 输入随任务账本保留至保留期清理）。
	Prompt string `json:"prompt"`
	// PromptHash 完整规范化输入的稳定指纹（SHA-256 hex，规范化=trim+UTF-8 清洗；
	// 重放同源判定的权威——展示截断不参与判定，同号异文必须 409；任务清理后
	// 指纹仍在本行，T2-R1 必修5）。
	PromptHash string `json:"prompt_hash,omitempty"`
	// Response 轮终答复正文（node 回传 FinalAnswer 原样落账；未终态为空）。
	Response string `json:"response,omitempty"`
	// NativeIDOk 原生 ID 校验成立（轮终回写；未终态 false）。
	NativeIDOk bool `json:"native_id_ok"`
	// WorkResult 工作结果判定（unverified；验收链路显式改判后可为他值）。
	WorkResult string `json:"work_result,omitempty"`
	// Usage token 用量摘要（轮终回写；未终态零值）。
	Usage protocol.TurnUsage `json:"usage"`
	// Error 失败原因（Status=failed 时非空；错误码+定位，执行事实留痕）。
	Error string `json:"error,omitempty"`
	// CreatedAt 轮受理时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// FinishedAt 轮终时刻（UTC；零值 = 未终态，状态由任务现态投影）。
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Terminal 轮次是否已终（FinishedAt 非零；终态行不再变更）。
func (t SessionTurn) Terminal() bool { return !t.FinishedAt.IsZero() }

func (t SessionTurn) clone() SessionTurn {
	return t // 全标量字段（Usage 为 protocol 纯标量结构），值拷贝足够
}

// MaxSessionTurnPrompt 轮次行 Prompt 留痕截断上限（8KiB）：任务本体（含完整
// 输入）随任务账本保留至保留期清理，轮次行只留可读留痕——防超长提示词把注册表
// 快照顶爆（注册表无清理路径，必须自限）。截断按 UTF-8 边界（截断注记由读面
// 呈现，行内不加标记污染原文比对）。
const MaxSessionTurnPrompt = 8 << 10

// MaxSessionTurnResponse 轮次行 Response 留痕截断上限（512KiB，对齐
// protocol.FinalAnswerLimit——node 侧已按此截断，此处为防御同值）。
const MaxSessionTurnResponse = 512 << 10
