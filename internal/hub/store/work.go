package store

import "time"

// Work 工作单（P5/RFC-work-resume 二.1）：能活过 Prime 生命周期的编排事实载体
//——任务层的计划、失败与进度意图落 hub 快照，Prime 崩溃/掉线后接管者读档可续。
// 实体链 Work → Step → Attempt → Task（尝试与任务解耦：请求级重试沿用同一尝试
// 键幂等去重，新的执行尝试分配新 Attempt）。纯事实记录：决策由任期内的 Prime
// 做并登记，hub 只保证事实不丢（RFC 二.3）。
//
// 写纪律与 ControlBatch 同款：写时复制（State.Clone 深拷贝后事务内改副本），
// 事实变更与事件记账必须同事务提交（事实与事件一致，W1 验收 4）。
//
// 生命周期：planning 创建即此态；executing/blocked/collecting 由 W2 派发/门控
// 推进；done/abandoned 为终态（W3 收集/处置落 closed_at+expires_at，终态保留
// 30 天，未完成 Work 不清理——RFC 六.3，Sweep 不触碰本实体）。

// Work 生命周期取值（Work.Status）。
const (
	// WorkStatusPlanning 建单完成、尚未派发。
	WorkStatusPlanning = "planning"
	// WorkStatusExecuting 任一步骤已派发执行中（W2 派发事务推进）。
	WorkStatusExecuting = "executing"
	// WorkStatusBlocked 存在阻断（needs_review/上游失败等，决策在 Prime）。
	WorkStatusBlocked = "blocked"
	// WorkStatusCollecting 全部步骤终态、收集中（W3 collect 前置态）。
	WorkStatusCollecting = "collecting"
	// WorkStatusDone 收集完成终态（W3）。
	WorkStatusDone = "done"
	// WorkStatusAbandoned 人工处置放弃终态（W3）。
	WorkStatusAbandoned = "abandoned"
)

// Step 执行状态取值（Step.Status）。
const (
	StepStatusPending   = "pending"
	StepStatusSubmitted = "submitted"
	StepStatusRunning   = "running"
	StepStatusDone      = "done"
	StepStatusFailed    = "failed"
	StepStatusSkipped   = "skipped"
	// StepStatusNeedsReview 步骤最新尝试落 needs_review 终态（W2 终态回写）。
	// RFC 二.1 六值之外的扩展：SPEC 4.8 禁止把 needs_review 折叠为 failed——
	// 接管者必须能区分「失败可重试」与「待人工核对不可自动重跑」。
	StepStatusNeedsReview = "needs_review"
)

// Attempt 结果取值（Attempt.Outcome；submitted 为在途，其余终局）。
const (
	AttemptOutcomeSubmitted  = "submitted"
	AttemptOutcomeDone       = "done"
	AttemptOutcomeFailed     = "failed"
	AttemptOutcomeSuperseded = "superseded"
	// AttemptOutcomeNeedsReview 最新任务落 needs_review 终态（W2 回写映射）。
	// RFC 二.1 四值之外的扩展，理由同 StepStatusNeedsReview：不折叠（SPEC 4.8），
	// Work 独立记忆须在任务 24h 清理后仍可解释终局。
	AttemptOutcomeNeedsReview = "needs_review"
	// AttemptOutcomeCancelled 任务被管理面取消（W2 回写映射，RFC 四值之外的
	// 扩展）：取消是事实不是失败，折叠为 failed 会在任务清理后丢失取消痕迹；
	// 对应步骤状态按 failed 记（步骤执行未成功，可再派新尝试）。
	AttemptOutcomeCancelled = "cancelled"
)

// MaxWorkEvents Work 事件账本滚动窗口：单 Work 只保留最新 200 条事件，超出从
// 最小 seq 起丢弃最旧（沿群消息模式，GROUP-RFC 三独立信箱同款约定）。账本是
// 滚动窗口不是永久审计账本（RFC 二.1）；EventSeq 独立单调累计，不随裁剪回退。
const MaxWorkEvents = 200

// WorkStep 计划步骤（RFC 二.1 Step）。seq 由 hub 建单时按声明序赋 1..N
// （唯一性由构造保证），depends_on 引用该序号，建单后计划定死（RFC 四.5：
// 不做任意计划编辑）。
type WorkStep struct {
	// Seq 步骤序号（Work 内唯一，1 起连续）。
	Seq int64 `json:"seq"`
	// Desc 步骤描述（做什么）。
	Desc string `json:"desc"`
	// TargetNode 执行节点（派发时经 Submit 链准入校验，建单只存声明）。
	TargetNode string `json:"target_node"`
	// TargetAgent 执行 agent 名。
	TargetAgent string `json:"target_agent"`
	// DependsOn 依赖的前置步骤 seq 列表（声明序保留；空 = 无依赖）。
	DependsOn []int64 `json:"depends_on,omitempty"`
	// Status 执行状态（StepStatusXxx；W1 建单恒 pending）。
	Status string `json:"status"`
	// Note 备注（失败矩阵决策登记等，任期内的 Prime 写）。
	Note string `json:"note,omitempty"`
}

// WorkAttempt 执行尝试（RFC 二.1 Attempt）：一次真实的任务派发记录。请求级
// 重试沿用同一尝试（同幂等键去重命中原任务）；换内容/换任期续派分配新尝试；
// 换任期本身不产生新尝试（RFC 二.1 实体链裁决）。任务 24h 归档清理后本记录
// 即 Work 的去重证据（30 天内仍能解释执行事实，RFC 二.1 独立记忆）。
type WorkAttempt struct {
	// Seq 尝试序号（Work 内单调分配，1 起，不回退）。
	Seq int64 `json:"seq"`
	// StepSeq 所属步骤。
	StepSeq int64 `json:"step_seq"`
	// TaskID 绑定的任务 ID（t-…；W2 派发事务原子绑定，W1 建单为空）。
	TaskID string `json:"task_id,omitempty"`
	// IdemKey 本次尝试的派发幂等键（请求级重试同键；任务清理后仍留档）。
	IdemKey string `json:"idem_key,omitempty"`
	// ContentHash/BaseContentHash 提交内容指纹（P5 W2 审查修正）：绑定事务内
	// 由 core 按 mailbox.ContentHash/BaseContentHash 同口径计算落盘（store 不
	// 依赖 mailbox——指纹字符串由调用方传入）。任务 24h 清理后任务级指纹查重
	// 失效，本对指纹即同键重放的判定依据（全量合=回执补拉命中；仅群引用不同=
	// 按新提交；其余=409 conflict，与任务级 IdemCheck 同构）——含目标/输入/
	// 提交主体/依赖/绑定与群引用，提交主体入指纹即身份边界（知 Work+键+步骤的
	// 他方不得冒领原 task_id）。
	ContentHash string `json:"content_hash,omitempty"`
	// BaseContentHash 协议内容指纹（不含群引用）：同键仅群引用不同按新提交
	// 处理（换群重试口径），与全量指纹配合区分「换群」与「换内容」。
	BaseContentHash string `json:"base_content_hash,omitempty"`
	// Outcome 尝试结局（AttemptOutcomeXxx）。
	Outcome string `json:"outcome"`
}

// WorkEvent Work 事件账本条目：seq 单调、编号稳定、随事实同事务原子提交；
// 滚动保留最新 MaxWorkEvents 条（头部丢弃，EventSeq 不回退——现存最旧 Seq-1
// 即已裁区间上界，游标语义同群消息 pruned_before_seq）。
type WorkEvent struct {
	// Seq 事件序号（Work 内从 1 连续单调分配）。
	Seq int64 `json:"seq"`
	// At 事件时刻（UTC）。
	At time.Time `json:"at"`
	// Kind 事件类别（work_created / step_updated / attempt_recorded / takeover /
	// collect 等；语义常量归 core，随批次演进补充）。
	Kind string `json:"kind"`
	// By 动作方（prime_term / mcp / admin 等审计标签；**请求自报**，逻辑归属非
	// 身份证明，RFC 六.2）。
	By string `json:"by"`
	// Principal 服务端验证的鉴权主体（REST 层自鉴权中间件取得：admin 会话 /
	// pat:<令牌名>；RFC 六.2 裁决——与自报 By/prime_term 分开存储，请求不可
	// 伪造；事件持久化后不随后续操作改写）。
	Principal string `json:"principal,omitempty"`
	// Body 事件正文（人话/结构化摘要）。
	Body string `json:"body"`
}

// Work 工作单全量（字段集 RFC 二.1）。
type Work struct {
	// ID 工作单标识："w-"+16 字节 hex，hub 生成。
	ID string `json:"id"`
	// Goal 工作目标（一句话全貌）。
	Goal string `json:"goal"`
	// PrimeTerm 建单/最近接管任期的逻辑归属标识（非身份证明，RFC 六.2；
	// W2 接管事务为权威改写点）。
	PrimeTerm string `json:"prime_term,omitempty"`
	// TermsUsed 历史使用过的任期标签（P5 W2 审查修正：任期标签为调用方自选
	// 字符串，无服务端单调代次——以历史使用集拒绝复用，防 ABA：旧任期标签
	// 重新接管成功会重新放行更旧的编排写入。建单任期播种，每次接管成功追加；
	// 接管重放（new==current）不追加）。
	TermsUsed []string `json:"terms_used,omitempty"`
	// Status 生命周期（WorkStatusXxx）。
	Status string `json:"status"`
	// Steps 计划步骤（建单定死，seq 1..N 升序）。
	Steps []WorkStep `json:"steps"`
	// Attempts 执行尝试记录（追加，seq 单调；W2 起产生）。
	Attempts []WorkAttempt `json:"attempts,omitempty"`
	// Events 事件账本（滚动保留最新 MaxWorkEvents 条，头部丢弃）。
	Events []WorkEvent `json:"events"`
	// EventSeq 已分配的最大事件 seq（重启后继续单调，不受裁剪影响——对齐
	// 群账本 Seqs 口径）。
	EventSeq int64 `json:"event_seq"`
	// Report 收集报告（W3 collect 落入；随 Work 保留 30 天，RFC 六.3）。
	Report string `json:"report,omitempty"`
	// CreatedAt 建单时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// ClosedAt 终态时刻（W3 done/abandoned 落入；未完成 nil——指针承载
	// omitempty，对齐 Group.ClosedAt 口径）。
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	// ExpiresAt 终态保留截止 = *ClosedAt+30d（到期清理，W3 Sweep 落实；
	// 未完成 Work nil 不清理）。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Clone 深拷贝工作单（切片逐层复制；string/time 不可变共享安全；*time.Time
// 换新指针——写时复制纪律，禁止事务内改写穿透已发布状态，对齐 cloneGroup）。
func (w Work) Clone() Work {
	n := w
	n.Steps = make([]WorkStep, len(w.Steps))
	for i, s := range w.Steps {
		n.Steps[i] = s.clone()
	}
	n.Attempts = append([]WorkAttempt(nil), w.Attempts...)
	n.Events = append([]WorkEvent(nil), w.Events...)
	n.TermsUsed = append([]string(nil), w.TermsUsed...)
	if w.ClosedAt != nil {
		t := *w.ClosedAt
		n.ClosedAt = &t
	}
	if w.ExpiresAt != nil {
		t := *w.ExpiresAt
		n.ExpiresAt = &t
	}
	return n
}

func (s WorkStep) clone() WorkStep {
	s.DependsOn = append([]int64(nil), s.DependsOn...)
	return s
}
