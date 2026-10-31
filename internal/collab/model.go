// 空间内核模型（W04-01 RFC §一）：Space 九态状态机、Member 线程绑定、
// Leader 任期/租约、DispatchIntent 持久对账锚、预算三分离、审计事件。
// 类型只承载数据与纯判定，全部状态变更经 Store 事务（store.go/ops.go）。
package collab

import (
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Mode 调度方式（产品计划 §5 三方式；跟随最近一次成功 takeover 的 kind）。
type Mode string

const (
	ModeManual   Mode = "manual"   // 手动：用户指定执行者与任务
	ModeBuiltin  Mode = "builtin"  // 内置协作：明确流程规则（+可选 jev 语义选择）
	ModeExternal Mode = "external" // 外部管家：经 MCP 显式派发
)

// Valid 调度方式取值校验。
func (m Mode) Valid() bool {
	switch m {
	case ModeManual, ModeBuiltin, ModeExternal:
		return true
	}
	return false
}

// Status 空间状态（v3 九态原始值独立定义，不拼凑现有任务/群状态，RFC §1.2）。
type Status string

const (
	StatusPendingConfig Status = "pending_config" // 待配置
	StatusPendingLeader Status = "pending_leader" // 待管家领取
	StatusReady         Status = "ready"          // 可运行（配置完成待启动）
	StatusRunning       Status = "running"        // 运行
	StatusPaused        Status = "paused"         // 暂停（只停新增派发，在途照收）
	StatusBlocked       Status = "blocked"        // 受阻
	StatusNeedsReview   Status = "needs_review"   // 待核对
	StatusCompleted     Status = "completed"      // 已完成（终态）
	StatusAbandoned     Status = "abandoned"      // 已放弃（终态）
)

// Terminal 终态判定（终态仅可 archive 标记，状态机不再转换）。
func (s Status) Terminal() bool { return s == StatusCompleted || s == StatusAbandoned }

// statusTransitions 状态转换表（显式枚举；表外路径非法）。resume 后一律回
// running（paused/blocked/needs_review 只可能来自 running 之后的生命周期，
// ready 态不可 pause——暂停恒指「运行中的新增派发被阻止」）。pending_config →
// pending_leader 供主人 takeover(open-claim) 开放外部领取（终审 S1：takeover
// 的状态变更一律经本表 CanTransition 判定，不留直赋的表外通道）。
var statusTransitions = map[Status][]Status{
	StatusPendingConfig: {StatusReady, StatusPendingLeader, StatusAbandoned},
	StatusPendingLeader: {StatusPendingConfig, StatusAbandoned},
	StatusReady:         {StatusRunning, StatusCompleted, StatusAbandoned},
	StatusRunning:       {StatusPaused, StatusBlocked, StatusNeedsReview, StatusCompleted, StatusAbandoned},
	StatusPaused:        {StatusRunning, StatusBlocked, StatusNeedsReview, StatusCompleted, StatusAbandoned},
	StatusBlocked:       {StatusRunning, StatusPaused, StatusCompleted, StatusAbandoned},
	StatusNeedsReview:   {StatusRunning, StatusPaused, StatusCompleted, StatusAbandoned},
	StatusCompleted:     {},
	StatusAbandoned:     {},
}

// CanTransition 状态转换合法性（显式函数，非法转换拒绝）。
func CanTransition(from, to Status) bool {
	if from == to {
		return false
	}
	for _, s := range statusTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// LeaderKind 负责人类型（= 调度主体形态；与 Mode 同词表）。
type LeaderKind string

const (
	LeaderManual   LeaderKind = "manual"
	LeaderBuiltin  LeaderKind = "builtin"
	LeaderExternal LeaderKind = "external"
)

// Valid 取值校验。
func (k LeaderKind) Valid() bool {
	switch k {
	case LeaderManual, LeaderBuiltin, LeaderExternal:
		return true
	}
	return false
}

// Mode 负责人类型 → 调度方式（同值域映射）。
func (k LeaderKind) Mode() Mode { return Mode(k) }

// leaderClass 负责人类型 ↔ 主体类别同族判定（manual↔owner / builtin↔system /
// external↔orchestrate；RFC §三「驱动者」判定要素）。
func leaderClass(k LeaderKind) SubjectClass {
	switch k {
	case LeaderManual:
		return ClassOwner
	case LeaderBuiltin:
		return ClassSystem
	case LeaderExternal:
		return ClassOrchestrate
	}
	return ""
}

// Leader 负责人（唯一有效调度者）。Term 服务端分配、空间内单调递增、永不复用
// （对照 Work 调用方自选标签 + TermsUsed 拒复用：v3 服务端计数结构性消灭 ABA）。
type Leader struct {
	Kind       LeaderKind `json:"kind"`
	Principal  string     `json:"principal"`             // 服务端已验证主体名（归属展示，不自证身份）
	Term       uint64     `json:"term"`                  // 0 = 未建立任期（待领取）
	LeaseUntil time.Time  `json:"lease_until,omitempty"` // manual 恒零值（不设租约）
}

// LeaseValid 租约有效判定：manual 不设租约（主人本位）；external/builtin 须
// LeaseUntil 非零且未过期（边界：now == LeaseUntil 视为未过期，严格过期
// now > LeaseUntil——判定单钟唯一权威，不设双钟宽限窗，RFC §1.3）。
func (l Leader) LeaseValid(now time.Time) bool {
	if l.Kind == LeaderManual {
		return true
	}
	if l.LeaseUntil.IsZero() {
		return false
	}
	return !now.After(l.LeaseUntil)
}

// Valid 有效负责人：任期已建立 + 主体在位 + 租约有效。
func (l Leader) Valid(now time.Time) bool {
	return l.Term > 0 && l.Principal != "" && l.LeaseValid(now)
}

// Member 空间成员：一成员一原生线程（SessionID 唯一绑定，全局经
// sessionOwners 映射保证一线程唯一归属空间）。
type Member struct {
	Name      string     `json:"name"`                // agent 名（派发目标）
	Role      string     `json:"role,omitempty"`      // 职责（writer/reviewer/discussant…，W05 解释）
	Workspace string     `json:"workspace,omitempty"` // 工作区（绝对路径）
	SessionID string     `json:"session_id,omitempty"`
	BoundAt   *time.Time `json:"bound_at,omitempty"`
}

// Budget 预算定义三分离（RFC §1.5）：三计数各自判耗尽、各自拒绝；0 = 不限制；
// 负值非法。旧 Group.MaxRounds（发言数）只与 MaxMessages 同族，不得当执行次数。
type Budget struct {
	MaxMessages       int `json:"max_messages"`        // 累计消息条数上限
	MaxDispatchRounds int `json:"max_dispatch_rounds"` // 执行派发轮次上限
	MaxWallSeconds    int `json:"max_wall_seconds"`    // 运行墙钟累计上限（秒）
}

// Valid 预算定义校验（负值非法）。
func (b Budget) Valid() bool {
	return b.MaxMessages >= 0 && b.MaxDispatchRounds >= 0 && b.MaxWallSeconds >= 0
}

// BudgetUsed 预算用量（三计数分离计量）。RunningSince 为进入 running 的时刻
// （墙钟累计锚点：离开 running 时结算 WallSeconds）。
type BudgetUsed struct {
	Messages       int       `json:"messages"`
	DispatchRounds int       `json:"dispatch_rounds"`
	WallSeconds    int64     `json:"wall_seconds"`
	RunningSince   time.Time `json:"running_since,omitempty"`
}

// wallSecondsNow 当前墙钟用量（running 中含未结算段）。
func (u BudgetUsed) wallSecondsNow(now time.Time) int64 {
	total := u.WallSeconds
	if !u.RunningSince.IsZero() && now.After(u.RunningSince) {
		total += int64(now.Sub(u.RunningSince) / time.Second)
	}
	return total
}

// settleRunning 结算 running 段（离开 running 时调用）：墙钟入账、锚点清零。
func (u *BudgetUsed) settleRunning(now time.Time) {
	if u.RunningSince.IsZero() {
		return
	}
	if now.After(u.RunningSince) {
		u.WallSeconds += int64(now.Sub(u.RunningSince) / time.Second)
	}
	u.RunningSince = time.Time{}
}

// FinishAuth 完成授权（RFC §三）：默认 human_only——完成须主人 finalize；
// verified_write_review 由主人预开后，当前驱动者可经 finish 完成授权闭环
// （仍须策略判据通过）。
type FinishAuth string

const (
	FinishAuthHumanOnly           FinishAuth = "human_only"
	FinishAuthVerifiedWriteReview FinishAuth = "verified_write_review"
)

// Valid 取值校验（空 = human_only 默认）。
func (f FinishAuth) Valid() bool {
	switch f {
	case "", FinishAuthHumanOnly, FinishAuthVerifiedWriteReview:
		return true
	}
	return false
}

// Policy 空间策略（仅主人可变更；PolicyVersion 随变更单调递增并审计）。
type Policy struct {
	FinishAuth FinishAuth `json:"finish_auth"`
	// ClaimPrincipals 允许领取的外部主体清单（空 = 任意持编排凭据主体可领取）。
	ClaimPrincipals []string `json:"claim_principals,omitempty"`
	// PolicyVersion 策略版本（每次 policy 变更 +1，随意图/完成动作落账引用）。
	PolicyVersion int64 `json:"policy_version"`
}

// claimAllowed 领取约束判定。
func (p Policy) claimAllowed(principal string) bool {
	if len(p.ClaimPrincipals) == 0 {
		return true
	}
	for _, n := range p.ClaimPrincipals {
		if n == principal {
			return true
		}
	}
	return false
}

// IntentStatus 执行意图状态。
type IntentStatus string

const (
	IntentPending   IntentStatus = "pending"   // 已落账未接单（outbox 态）
	IntentAccepted  IntentStatus = "accepted"  // 执行侧已接单（TaskID 在位）
	IntentConverged IntentStatus = "converged" // 执行事实已收束落账
	IntentCancelled IntentStatus = "cancelled" // 显式取消收束
)

// InFlight 在途判定（pending/accepted；成员变更与完成类动作的前置护栏）。
func (s IntentStatus) InFlight() bool {
	return s == IntentPending || s == IntentAccepted
}

// DispatchIntent 执行意图（RFC §1.6）：派发先于执行持久化（outbox），至少持久
// 关联 space/member/thread/session/turn/task/request/term/version——即使 Task
// 清理也能对账。收敛一律经显式 Converge 调用（不凭回调自动重跑）。
type DispatchIntent struct {
	RequestID  string       `json:"request_id"`
	SpaceID    string       `json:"space_id"`
	Member     string       `json:"member"`
	SessionID  string       `json:"session_id,omitempty"`
	TurnSeq    int64        `json:"turn_seq"`
	TaskID     string       `json:"task_id,omitempty"` // 空 = pending（执行侧未接单）
	Term       uint64       `json:"term"`              // 派发时当前任期（旧任期写入拒绝的锚）
	VersionRef int64        `json:"version_ref"`       // 派发时空间版本引用
	Status     IntentStatus `json:"status"`
	Note       string       `json:"note,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// SpaceEvent 空间审计事件（滚动保留最新 MaxEventsPerSpace 条；解释面）。
type SpaceEvent struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Actor string    `json:"actor"` // 服务端已验证主体（Class:Name），非请求自报
	Note  string    `json:"note,omitempty"`
}

// Space 协作空间（快照持久化形态）。
type Space struct {
	ID           string       `json:"id"`
	Goal         string       `json:"goal"`
	Acceptance   string       `json:"acceptance,omitempty"`
	Mode         Mode         `json:"mode"`
	Status       Status       `json:"status"`
	Phase        string       `json:"phase,omitempty"`
	Members      []Member     `json:"members"`
	Leader       Leader       `json:"leader"`
	Policy       Policy       `json:"policy"`
	Budget       Budget       `json:"budget"`
	Used         BudgetUsed   `json:"used"`
	Version      int64        `json:"version"`
	Archived     bool         `json:"archived"`
	ArchivedAt   *time.Time   `json:"archived_at,omitempty"`
	FinalOutcome string       `json:"final_outcome,omitempty"` // done | abandoned
	FinalReason  string       `json:"final_reason,omitempty"`
	FinalBy      string       `json:"final_by,omitempty"`
	PausedReason string       `json:"paused_reason,omitempty"`
	WaitReason   string       `json:"wait_reason,omitempty"`
	Events       []SpaceEvent `json:"events,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Terminal 终态。
func (sp *Space) Terminal() bool { return sp.Status.Terminal() }

// memberOf 按名查成员（命中返回下标）。
func (sp *Space) memberOf(name string) int {
	for i := range sp.Members {
		if sp.Members[i].Name == name {
			return i
		}
	}
	return -1
}

// inFlightMembers 有在途意图的成员名集合。
func (sp *Space) inFlightMembers(intents []DispatchIntent) map[string]bool {
	set := map[string]bool{}
	for _, it := range intents {
		if it.SpaceID == sp.ID && it.Status.InFlight() {
			set[it.Member] = true
		}
	}
	return set
}

// inFlightCount 空间在途意图数。
func (sp *Space) inFlightCount(intents []DispatchIntent) int {
	n := 0
	for _, it := range intents {
		if it.SpaceID == sp.ID && it.Status.InFlight() {
			n++
		}
	}
	return n
}

// appendEvent 追加审计事件（滚动裁剪最旧；仅在 mutate 事务副本上调用）。
func (sp *Space) appendEvent(now time.Time, kind, actor, note string) {
	sp.Events = append(sp.Events, SpaceEvent{At: now, Kind: kind, Actor: actor, Note: truncateNote(note)})
	if len(sp.Events) > MaxEventsPerSpace {
		sp.Events = append(make([]SpaceEvent, 0, MaxEventsPerSpace), sp.Events[len(sp.Events)-MaxEventsPerSpace:]...)
	}
}

// truncateNote 备注清洗截断（防快照膨胀）。
func truncateNote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxNoteBytes {
		return s
	}
	return s[:MaxNoteBytes]
}

// SpaceView 空间对外视图（REST 出口形态；快照 Space 加运行投影）。
type SpaceView struct {
	Space
	InFlight int `json:"in_flight"`
}

// spaceView 由快照构造视图（附带在途计数投影）。
func spaceView(sp Space, intents []DispatchIntent) SpaceView {
	return SpaceView{Space: sp, InFlight: sp.inFlightCount(intents)}
}

// ControlResult 控制动作结果（REST/MCP 出口）。
type ControlResult struct {
	Space   SpaceView `json:"space"`
	Action  Action    `json:"action"`
	Dup     bool      `json:"dup,omitempty"` // 操作 ID 重放命中（返回原回执）
	Applied bool      `json:"applied"`       // 本次是否实际变更（archive 幂等命中为 false）
}

// newSpaceID 空间 ID（sp- + 随机 hex，protocol 同源）。
func newSpaceID() string { return SpaceIDPrefix + protocol.NewMessageID() }
