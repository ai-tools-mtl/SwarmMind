// Package protocol 定义 SwarmMind V0 线协议：信封与消息类型（SPEC 4.1/4.2）、
// 核心数据结构 Schema（4.9）、任务状态机（4.3）与运行常量（4.6）。
// 本包是 hub/node 共享的纯协议层：只含定义与纯函数，不做任何 IO 与网络。
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AgentKind agent 形态；V0 仅支持 "cli"（SPEC 4.9）。
type AgentKind string

// AgentKindCLI 唯一合法的 AgentKind 取值。
const AgentKindCLI AgentKind = "cli"

// AgentCard 对外能力声明（对齐 A2A Agent Card 语义；SPEC 4.9 双卡分离）。
// 由 node 侧从 ExecProfile 生成并上报；ExecProfile 本体永不离开本机。
type AgentCard struct {
	// Name agent 名称，node 内唯一。
	Name string `json:"name"`
	// Kind agent 形态，V0 恒为 cli。
	Kind AgentKind `json:"kind"`
	// Description 技能/用途描述，调用方据此选 agent。
	Description string `json:"description,omitempty"`
	// InputMode 固定 "prompt_text"（V0：一次性自然语言任务）。
	InputMode string `json:"input_mode"`
	// OutputMode 固定 "answer+artifacts"。
	OutputMode string `json:"output_mode"`
	// Interactive V0 恒为 false：不支持澄清/审批/继续输入，任务必须可一次给完。
	Interactive bool `json:"interactive"`
	// PermissionMode 映射 agent 自带权限机制（如 codex sandbox 模式），调用方知情。
	PermissionMode string `json:"permission_mode"`
	// ProtocolVer 适配器协议版本。
	ProtocolVer int `json:"protocol_ver"`
	// WorkspaceID workdir 互斥键（P1.5/SPEC 4.10）：node 启动时对 ExecProfile.Workdir
	// 规范化（Abs+Clean+EvalSymlinks，失败回退 Clean+Abs 并注记）后取 SHA-256 hex
	// 前 16 字符。workspace_id 由 node 上报，hub 不感知路径（路径不出本机）；
	// 互斥键 = (node_id, workspace_id)。空 = 旧版 node 未上报，不参与互斥。
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Exposed 接入开关（主权在 node）：false=执行准入关闭（新任务 404 agent_not_exposed）；
	// 注册表保留一条审计记录（名称/下线时间，不含能力详情）。
	Exposed bool `json:"exposed"`
}

// ExecProfile 本机执行配置（永不离开本机；SPEC 4.9）。
type ExecProfile struct {
	// Name 必填，node 内唯一（与 AgentCard.Name 一致）。
	Name string `json:"name"`
	// Kind 必填，仅 cli（与 AgentCard.Kind 一致）。
	Kind AgentKind `json:"kind"`
	// Command 必填，绝对路径。
	Command string `json:"command"`
	// Workdir 必填，绝对路径（进程 cwd）。
	Workdir string `json:"workdir"`
	// AgentArgs 固定附加参数（如 codex --sandbox/-mode 权限旗标），任务不可改；
	// 拼接顺序：AgentArgs 在前，任务 Args 在后。
	AgentArgs []string `json:"agent_args"`
	// TimeoutSec 超时秒数，>0。
	TimeoutSec int `json:"timeout_sec"`
	// AllowArgs 默认 false：任务带非空 args → policy_denied。
	AllowArgs bool `json:"allow_args"`
	// AllowEnv 允许任务覆盖的 env 键，默认 []；"*" 全开；"PATH" 需显式列出。
	AllowEnv []string `json:"allow_env"`
	// ConfigHome agent 配置/凭证目录（HOME 指此，不指 workdir）；空则 data_dir/home。
	ConfigHome string `json:"config_home"`
	// Description 生成 AgentCard.Description 的源。
	Description string `json:"description,omitempty"`
	// PermissionMode 映射 agent 自带权限机制，生成 Card.PermissionMode。
	PermissionMode string `json:"permission_mode"`
	// Exposed 接入开关（默认 true）：下线后新任务 404，已排队任务正常继续执行至终态；
	// 审计保留一条下线登记。
	Exposed bool `json:"exposed"`
	// StdinToArg 输入语义转换（2026-10-23 伦哥实测抓出）：true 时若任务 Args 为空且
	// stdin 非空，执行前将 stdin 整体作为单个尾参追加到命令行——供 zcode -p 等
	// 「旗标后必须接提示词参数」的 args 型 CLI；控制台/表单派单走 stdin 语义，
	// 不加此开关此类 agent 必因旗标缺参打印 usage 退出。默认 false（行为不变）。
	StdinToArg bool `json:"stdin_to_arg"`
}

// TaskInput 任务输入（SPEC 4.9）。
type TaskInput struct {
	// Args 追加到 command 后（ExecProfile.AgentArgs 在前）。
	Args []string `json:"args"`
	// Stdin UTF-8 文本；写完关 stdin。
	Stdin string `json:"stdin,omitempty"`
	// Env 任务 env 覆盖；键必须在 ExecProfile.AllowEnv 名单内。
	Env map[string]string `json:"env,omitempty"`
}

// TaskResult 终态结果（SPEC 4.9）；Status 取值见 ResultXxx 常量。
type TaskResult struct {
	// Status 结果终态：completed | failed | needs_review | cancelled。
	Status string `json:"status"`
	// ExitCode 进程退出码；信号终止或未运行 = -1。
	ExitCode int `json:"exit_code"`
	// FinalAnswer 完整最终答复（agent 产出正文，≤512KiB，独立于日志 tail）。
	FinalAnswer string `json:"final_answer"`
	// StdoutTail 诊断用日志尾部 64KiB（非正式结果载体）。
	StdoutTail string `json:"stdout_tail"`
	// StderrTail 诊断用日志尾部 64KiB。
	StderrTail string `json:"stderr_tail"`
	// Artifacts 产物引用（路径+哈希，node 本地保留，按需拉取）。
	Artifacts []Artifact `json:"artifacts"`
	// Error 失败/复核/取消原因：timeout / policy_denied / expired / user_cancelled /
	// node_restarted_midway / input_prepare_failed（上游产物挂载失败，P4 L2/
	// SPEC 4.12）等（取值见 CodeXxx 常量）。
	Error string `json:"error,omitempty"`
	// StartedAt 执行开始时刻（UTC）。
	StartedAt time.Time `json:"started_at,omitempty"`
	// FinishedAt 执行结束时刻（UTC）。
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Artifact 产物引用（SPEC 4.9）：内容保留在 node 本地，经 artifact_fetch/artifact_data 拉取。
type Artifact struct {
	// Path 相对 workdir 的路径（outputs/ 约定目录下）；取回时由 node 解析为本地绝对路径。
	Path string `json:"path"`
	// Sha256 文件内容 SHA-256（hex）。
	Sha256 string `json:"sha256"`
	// Size 文件字节数。
	Size int64 `json:"size"`
}

// TaskReview needs_review 任务的人工核对记录（SPEC 六.五）：管理面追加并随快照
// 持久化，不改协议终态；node 账本不感知（协议层无帧）。
type TaskReview struct {
	// By 核对人（控制台会话固定 admin）。
	By string `json:"by"`
	// At 核对时刻（UTC RFC3339）。
	At time.Time `json:"at"`
	// Action 核对动作：ack_review | mark_failed。
	Action string `json:"action"`
	// Note 人工备注。
	Note string `json:"note,omitempty"`
}

// TaskLease 任务租约（P4/SPEC 4.11，Task.Lease 载体）：FenceEpoch 是 hub 当前
// 派发的租约代次（派发事务内捕获的该 node 注册代次快照，重派即刷新），结果
// 仲裁据此与 node 回带的 fence_epoch 等值比较；其余字段是 zombie 拒收审计
//（zombie_result_rejected），零值表示无拒收。未派发/旧版记录 Lease 为 nil。
type TaskLease struct {
	// FenceEpoch 当前派发租约代次（0=未携租约，缺省宽容）。
	FenceEpoch int64 `json:"fence_epoch"`
	// Rejects 该任务累计被拒收的 zombie 结果数（zombie_result_rejected）。
	Rejects int `json:"rejects,omitempty"`
	// LastRejectedFence 最近一次被拒结果携带的回带值。
	LastRejectedFence int64 `json:"last_rejected_fence,omitempty"`
	// LastAt 最近拒收时刻（UTC）。
	LastAt time.Time `json:"last_at,omitempty"`
}

// Task 任务全量（SPEC 4.9）：hub 生成并持久化，随 offer 下发、随 task_status 查询返回。
type Task struct {
	// ID 任务唯一标识："t-"+16 字节 hex，hub 生成。
	ID string `json:"id"`
	// TraceID 链路追踪 ID。
	TraceID string `json:"trace_id"`
	// TargetNode 目标 node。
	TargetNode string `json:"target_node"`
	// TargetAgent 目标 agent（node 内唯一名）。
	TargetAgent string `json:"target_agent"`
	// Input 任务输入。
	Input TaskInput `json:"input"`
	// SubmittedBy 提交方：HTTP 提交=调用方标记；node 提交=hub 覆盖为该 node_id。
	SubmittedBy string `json:"submitted_by"`
	// IdemKey 幂等键（hub 全局作用域，与任务同生命周期）。
	IdemKey string `json:"idem,omitempty"`
	// GroupRef 群引用（GROUP-RFC 转任务闭环，管理面扩展字段）：任务归属的协作
	// 群 ID。协议帧 task_submit 不携带（node 间提交恒空）；参与提交幂等指纹——
	// 同键同群重试=dup 命中原任务，同键换群=不同指纹=新任务（对齐批次内容指纹
	// 口径，SPEC 4.5）。
	GroupRef string `json:"group_ref,omitempty"`
	// WorkRef 工作单引用（P5 W2/RFC-work-resume 绑定派发，管理面扩展字段）：
	// 任务归属的工作单 ID（w-…）。照抄 GroupRef 先例：协议帧 task_submit 不
	// 携带（node 间提交恒空）；参与提交幂等指纹（与 WorkStep 同为请求可声明的
	// 规范形——同键换工作单/换步骤=冲突 409，绑定是执行去重关键面非路由装饰）；
	// 终态回写据此定位工作单（任务终态 → attempt.outcome + step 状态同事务回写）。
	WorkRef string `json:"work_ref,omitempty"`
	// WorkStep 绑定的工作单步骤序号（1 起声明序；与 WorkRef 成对出现）。参与
	// 提交幂等指纹（规范形见 WorkRef）。0 = 未绑定。
	WorkStep int64 `json:"work_step_seq,omitempty"`
	// AttemptRef 绑定的执行尝试引用（hub 分配：<work_id>#a<seq>，W2）。照抄
	// GroupRef 管理面登记先例，但**不参与幂等指纹**——重试回放无法声明 hub
	// 分配号，指纹双方只能取请求可声明字段（WorkRef+WorkStep）；尝试唯一性由
	// 「同键重放短路于 idem 查重（先于绑定事务）」构造保证。终态回写经任务 ID
	// 在 Work.Attempts 定位尝试，本字段为管理面可读的直引键。
	AttemptRef string `json:"attempt_ref,omitempty"`
	// DependsOn 依赖的上游任务 idem_key 列表（P4.3/SPEC 4.12，提交后不可变）。
	// 以 idem 寻址跨重跑稳定；提交时准入校验（存在/无自引用/无重复/深度≤4/
	// 级联≤8），node 会话 task_submit 同样可携带。未声明为 nil。
	DependsOn []string `json:"depends_on,omitempty"`
	// BlockedReason 依赖门控标注（P4.3/SPEC 4.12；不进状态机）：依赖未满足时
	// 保持 submitted 不投递，取值 blocked_by:<idem> / upstream_failed:<idem>；
	// 空 = 未阻塞（正常调度）。随快照持久化，hub 重启恢复免费。
	BlockedReason string `json:"blocked_reason,omitempty"`
	// SupersededBy 上游重跑审计标注（P4.3/SPEC 4.12；不进状态机）：曾因
	// upstream_failed:<idem> 被挡的下游任务，遇同 idem 新版本 completed 时记录
	// 该新任务 ID；任务保持 blocked 不自动放行，人工决定是否重派（旧任务审计
	// 保留）。空 = 未被取代。
	SupersededBy string `json:"superseded_by,omitempty"`
	// Status hub 视角任务状态（SPEC 4.3 状态机）。
	Status TaskStatus `json:"status"`
	// CreatedAt 创建时刻（UTC RFC3339）。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最近状态变更时刻（UTC）。
	UpdatedAt time.Time `json:"updated_at"`
	// ExpireAt 过期时刻 = CreatedAt+7d；到期非终态 → failed/error=expired。
	ExpireAt time.Time `json:"expire_at"`
	// Result 终态结果（非终态为 nil）。
	Result *TaskResult `json:"result,omitempty"`
	// Lease 任务租约（P4/SPEC 4.11）：派发即登记（FenceEpoch=派发时刻该 node
	// 注册代次，重派刷新），结果仲裁的等值基准；Rejects/Last* 为 zombie 拒收
	// 审计（zombie_result_rejected）。nil = 未派发或旧版记录；不参与状态机
	//（不改 Status/UpdatedAt），随快照持久化供控制台/查询面审计。
	Lease *TaskLease `json:"lease,omitempty"`
	// Review 人工核对记录（needs_review 任务的管理面追加，SPEC 六.五；
	// 协议层不感知，不参与状态机与 offer 下发）。
	Review *TaskReview `json:"review,omitempty"`
}

// TaskSubmitRequest 任务提交请求（SPEC 4.9；HTTP 管理面与 node 间 task_submit 共用）。
type TaskSubmitRequest struct {
	// TargetNode 必填。
	TargetNode string `json:"target_node"`
	// TargetAgent 必填。
	TargetAgent string `json:"target_agent"`
	// Input 任务输入。
	Input TaskInput `json:"input"`
	// SubmittedBy 必填（人/agent 名）。
	SubmittedBy string `json:"submitted_by"`
	// IdemKey 可选幂等键。
	IdemKey string `json:"idem_key,omitempty"`
	// GroupRef 可选群引用（管理面扩展）：随任务落库并参与幂等指纹（同键换群=
	// 新任务，SPEC 4.5）；node 帧提交不携带，群登记由管理面在提交前完成。
	GroupRef string `json:"group_ref,omitempty"`
	// DependsOn 可选依赖声明（P4.3/SPEC 4.12）：上游任务的 idem_key 列表。
	// 全部上游 completed 才投递；提交时准入校验（未知 idem/自引用/重复依赖/
	// 深度≤4/级联≤8 全部 400 invalid_dependency）。声明后 stdin 尾部附依赖
	// 清单（EffectiveInput，参与幂等指纹）。
	DependsOn []string `json:"depends_on,omitempty"`
	// WorkRef 工作单绑定派发（P5 W2/RFC-work-resume 二.2，管理面扩展，六.六
	// 规则 7 参数扩展）：与 StepSeq、PrimeTerm 三参成组——WorkRef 非空即绑定
	// 派发，任务落库、尝试登记、步骤回写在同一事务原子完成（绑定失败不发布）。
	// 协议帧 task_submit 不携带（node 间提交恒空，同 GroupRef 口径）。
	WorkRef string `json:"work_ref,omitempty"`
	// StepSeq 绑定的步骤序号（1 起声明序；绑定派发必填，0=未绑定）。
	StepSeq int64 `json:"step_seq,omitempty"`
	// PrimeTerm 任期声明（绑定派发必填）：须与工作单落库 PrimeTerm 严格相等，
	// 否则拒绝（旧任期编排写入拒绝，RFC 二.2 接管事务）。归属标签非身份证明
	//（RFC 六.2）；不参与幂等指纹（换任期后同键重放命中 dup 返原任务）。
	PrimeTerm string `json:"prime_term,omitempty"`
}

// 结果终态取值（TaskResult.Status；与 TaskStatus 的四个终态同值，SPEC 4.3/4.9）。
const (
	// ResultCompleted 执行成功。
	ResultCompleted = "completed"
	// ResultFailed 执行失败。
	ResultFailed = "failed"
	// ResultNeedsReview 执行结果不确定，需人工核对；协议与展示层不得折叠为 failed（SPEC 4.8）。
	ResultNeedsReview = "needs_review"
	// ResultCancelled 被管理面取消：进程已整组终止（或未再执行）；独立终态，不自动重跑。
	ResultCancelled = "cancelled"
)

// ExitCodeNotRun 进程未运行或被信号终止时的退出码（SPEC 4.9：信号终止或未运行 = -1）。
const ExitCodeNotRun = -1

// 核对动作取值（TaskReview.Action；SPEC 六.五）。二者均不改协议终态：
// mark_failed 只是人工判定登记，任务保持 needs_review。
const (
	// ReviewAckReview 知悉确认：已人工核对，任务保留 needs_review 终态。
	ReviewAckReview = "ack_review"
	// ReviewMarkFailed 人工判定为失败并登记（协议终态仍为 needs_review）。
	ReviewMarkFailed = "mark_failed"
)

// NowUTC 返回当前 UTC 时刻；全工程时间基准统一 UTC（SPEC 第九节）。
func NowUTC() time.Time { return time.Now().UTC() }

// NewTaskID 生成任务 ID："t-"+16 字节 hex（SPEC 4.9，hub 生成）。
func NewTaskID() string { return "t-" + randomHex() }

// NewWorkID 生成工作单 ID："w-"+16 字节 hex（P5/RFC-work-resume 二.1，hub 生成）。
func NewWorkID() string { return "w-" + randomHex() }

// NewMessageID 生成信封去重键 mid：16 字节 hex。
func NewMessageID() string { return randomHex() }

// NewTraceID 生成链路追踪 ID：16 字节 hex。
func NewTraceID() string { return randomHex() }

// randomHex 返回 16 字节加密随机数的 hex 编码。
func randomHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败属系统级异常，直接 panic，不做静默降级
		panic("protocol: crypto/rand 读取失败: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Clone 深拷贝任务（容器逐层复制；string 不可变可共享），供先写后见事务构造新副本。
func (t Task) Clone() Task {
	n := t
	n.Input = t.Input.Clone()
	n.DependsOn = append([]string(nil), t.DependsOn...)
	if t.Result != nil {
		r := t.Result.Clone()
		n.Result = &r
	}
	if t.Review != nil {
		rv := *t.Review
		n.Review = &rv
	}
	if t.Lease != nil {
		// 写时复制纪律：*TaskLease 换新指针，禁止与旧状态共享
		ls := *t.Lease
		n.Lease = &ls
	}
	return n
}

// Clone 深拷贝任务输入。
func (in TaskInput) Clone() TaskInput {
	in.Args = append([]string(nil), in.Args...)
	if in.Env != nil {
		env := make(map[string]string, len(in.Env))
		for k, v := range in.Env {
			env[k] = v
		}
		in.Env = env
	}
	return in
}

// Clone 深拷贝结果。
func (r TaskResult) Clone() TaskResult {
	r.Artifacts = append([]Artifact(nil), r.Artifacts...)
	return r
}
