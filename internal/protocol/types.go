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
	// Status 结果终态：completed | failed | needs_review。
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
	// Error 失败/复核原因：timeout / policy_denied / expired / node_restarted_midway 等
	//（取值见 CodeXxx 常量）。
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
}

// 结果终态取值（TaskResult.Status；与 TaskStatus 的三个终态同值，SPEC 4.3/4.9）。
const (
	// ResultCompleted 执行成功。
	ResultCompleted = "completed"
	// ResultFailed 执行失败。
	ResultFailed = "failed"
	// ResultNeedsReview 执行结果不确定，需人工核对；协议与展示层不得折叠为 failed（SPEC 4.8）。
	ResultNeedsReview = "needs_review"
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
	if t.Result != nil {
		r := t.Result.Clone()
		n.Result = &r
	}
	if t.Review != nil {
		rv := *t.Review
		n.Review = &rv
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
