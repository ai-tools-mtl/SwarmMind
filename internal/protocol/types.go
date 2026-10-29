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
	// AllowArgs 任务参数授权声明（maturity-dispatch-args-ux）：映射 ExecProfile.AllowArgs，
	// 调用方据此选择传参方式（true=可携 args；false=带非空 args 将 policy_denied）。
	// 输入语义属能力声明而非本机执行细节，故进 Card（Hub 目录/控制台据此呈现输入面）。
	// 注意：本字段声明的 false 才代表 node 实际拒绝；旧版 node 缺字段解码出的 false
	// 语义见 StdinToArg 注（混跑降级）。
	AllowArgs bool `json:"allow_args"`
	// StdinToArg 输入语义转换声明（同上批次）：映射 ExecProfile.StdinToArg；true=
	// 任务 Args 为空且 stdin 非空时 stdin 整体转单个尾参。控制台据此提示「描述将
	// 作为命令行参数传递」。混跑降级口径（旧版二进制实跑实证：解码双向兼容，
	// 能力发现降级）：旧版 node 的 Card 无此二字段，新版解码为零值 false——该 false
	// 只是「字段缺失」的呈现值，不代表 node 实际配置（旧 node 自身 ExecProfile 可能
	// allow_args=true，携 args 的任务照常执行成功），不得把未知宣称为不接受参数；
	// 旧版 hub 解码则丢弃未知字段（目录不展示），新 node 携 args 派发经既有
	// TaskInput.Args 透传照常执行。混跑期建议先升 node（或同步升级）；需传参的
	// 任务以 CLI/REST 直派，不受目录呈现影响。
	StdinToArg bool `json:"stdin_to_arg"`
	// Orchestrator 统筹载体声明（2026-10-28 客户端声明制）：node 声明本能力为智行
	// 管家；hub 全网汇聚（恰一声明生效；多声明冲突回退名字约定并告警；零声明回退
	// 名字约定）。混跑兼容同 StdinToArg：旧端缺字段解码零值 false。
	Orchestrator bool `json:"orchestrator,omitempty"`
	// SessionAdapter 会话接入声明（T2 持久会话，SPEC 六.五「会话与轮次」）：
	// SessionAdapterCodex | SessionAdapterZcode——node.yaml 该能力配置
	// session_adapter 后经能力卡上报（接入分级声明，VISION 四）。空 = 无持久
	// 会话能力：hub 建会话 422 session_unsupported（旧版 node 卡缺字段解码为空，
	// 同口径拒绝并指路升级）。混跑兼容：旧 hub 解码丢弃未知字段（目录不展示，
	// 会话面不可用——能力发现降级，任务面不受影响）。
	SessionAdapter string `json:"session_adapter,omitempty"`
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
	// Orchestrator 统筹载体声明（2026-10-28 伦哥定案：客户端声明，hub 只接收汇聚）：
	// true = 本能力承担智行管家职责（接收复合目标拆解派发汇总，不亲自执行子任务）。
	// hub 全网汇聚：恰一声明即生效；多声明为配置漂移冲突（无生效管家，回退名字约定
	// + 控制台警告）；零声明回退能力名含 zcode-dev 的名字约定。
	Orchestrator bool `json:"orchestrator,omitempty"`
	// SessionAdapter 会话适配器声明（T2 持久会话）：非空 = 该能力支持持久会话，
	// 取值 SessionAdapterCodex | SessionAdapterZcode（经能力卡上报 hub；空 = 不
	// 支持会话）。会话轮执行经 sessionx 内核（同 Command、PermissionMode 与
	// TimeoutSec 语义；进程环境由装配方按 SPEC 五给干净基线）。
	SessionAdapter string `json:"session_adapter,omitempty"`
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
	// Turn 会话轮执行产物（T2 持久会话；nil=普通任务结果，既有形态零变化）。
	// 轮终按任务结果链回传：hub 据此写会话轮次账本（SPEC 六.五「会话与轮次」）。
	Turn *TurnOutcome `json:"turn,omitempty"`
}

// TurnOutcome 会话轮执行产物（node→hub 随 task_result 回传；回复正文经
// TaskResult.FinalAnswer 原样承载，不在此重复）。轮终四路判定的投影：
//   - 执行成立（三路齐备）→ 任务终态 completed、NativeIDOk=true；
//   - 执行不成立（退出码/解析/原生 ID/绑定守卫/内核拒绝）→ 任务终态 failed、
//     NativeIDOk=false、错误随 TaskResult.Error 留痕；
//   - WorkResult 沿用内核判定（T2 恒 unverified——CLI 输出无法证明工作成功，
//     禁止文本关键词猜测；验收链路显式改判属后续批次）。
type TurnOutcome struct {
	// TurnSeq 平台轮序号（回显 Task.Session.TurnSeq，供 hub 定位轮次账本行）。
	TurnSeq int64 `json:"turn_seq"`
	// NativeIDOk 原生 ID 校验成立：create 取回非空原生 ID / send 回带与存储一致
	//（false = 本轮未与既有原生线程确认衔接，详见 TaskResult.Error）。
	NativeIDOk bool `json:"native_id_ok"`
	// WorkResult 工作结果判定（protocol.WorkUnverified；四路分离第四路）。
	WorkResult string `json:"work_result"`
	// Usage token 用量摘要（内核解析产物，解析不出为零值）。
	Usage TurnUsage `json:"usage"`
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
// （zombie_result_rejected），零值表示无拒收。未派发/旧版记录 Lease 为 nil。
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
	// Session 会话轮身份段（T2 持久会话，SPEC 六.五「会话与轮次」；nil=普通任务，
	// 既有语义零变化）。会话轮复用任务投递链（信箱/幂等/补投/租约全白拿）但身份
	// 严格分离：本段携带平台 session id 与平台轮序号，Task.ID 仅作为本次执行的
	// 可靠性载体——禁止把 Task ID 当会话身份，也禁止会话身份顶替 Task 身份
	//（VISION 第三节）。幂等键按 session id + turn seq 派生（core 层会话轮键）。
	Session *SessionTurnRef `json:"session,omitempty"`
}

// 会话轮操作取值（SessionTurnRef.Op）。
const (
	// SessionTurnOpCreate 首轮：node 侧执行首轮并建立平台↔原生映射（Create）。
	SessionTurnOpCreate = "create"
	// SessionTurnOpSend 续轮：以存储原生 ID 续接（Send）。
	SessionTurnOpSend = "send"
	// SessionTurnOpClose 关闭：node 侧关闭会话（Close），随任务链投递保证离线
	// node 重连后补执行；不占轮次序号（管理动作非对话轮）。
	SessionTurnOpClose = "close"
)

// SessionTurnRef 会话轮身份段（协议层纯数据；投递/注册表/状态机见 hub core 与
// node service）。create 携 cwd 与绑定断言值（适配器+权限）；send/close 以
// node 侧存储的会话绑定为唯一真相（防路径漂移与静默换绑），不重带 cwd。
type SessionTurnRef struct {
	// SessionID 平台会话 ID（hub 注册表分配，"s-"+16 字节 hex；与 node 侧
	// sessionx 持久映射同键）。
	SessionID string `json:"session_id"`
	// TurnSeq 平台轮序号（1 起；create 恒 1 起，close 不占号恒 0）。参与幂等键。
	TurnSeq int64 `json:"turn_seq"`
	// Op 操作：SessionTurnOpXxx。
	Op string `json:"op"`
	// Cwd 会话工作目录（create 必填，node 本机绝对路径；send/close 恒空）。
	Cwd string `json:"cwd,omitempty"`
	// PermissionMode 权限模式断言值（create 可选；hub 侧与能力卡核对后原样下发
	// 供 node 复核，非换绑通道——真相源是 node 侧配置；绝不注入 yolo，node 侧
	// 对提权档位显式拒绝，SPEC 六.五）。
	PermissionMode string `json:"permission_mode,omitempty"`
	// Adapter 适配器断言值（create/send 携带，close 不携）：hub 注册表绑定快照的
	// 适配器名，node 首轮执行入口与本机装配表规范化配置同口核对（T2-R2 必修2
	// 补完——权限与适配器可独立漂移：权限不变而适配器漂移时原生会话 ID 会被
	// 交给另一 CLI，同属换绑，须在 spawn 前拒绝）。缺省（旧版 hub 帧无此字段）
	// = 信任本机配置；续轮不依赖本字段（node 侧存储绑定守卫已覆盖）。
	Adapter string `json:"adapter,omitempty"`
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

// NewSessionID 生成平台会话 ID："s-"+16 字节 hex（T2 持久会话，hub 注册表分配；
// 与 node 侧 sessionx 内核映射同键，格式对齐 sessionx 生成形态）。
func NewSessionID() string { return "s-" + randomHex() }

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
	if t.Session != nil {
		// 写时复制纪律：*SessionTurnRef 换新指针（同 Lease 口径）
		sr := *t.Session
		n.Session = &sr
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
	if r.Turn != nil {
		// 写时复制纪律：*TurnOutcome 换新指针（同 Task.Lease 口径）
		t := *r.Turn
		r.Turn = &t
	}
	return r
}

// ---------- 会话内核契约（T1，VISION 持久会话 Session-Turn 概念模型）----------
// 本节只含数据契约（结构与取值常量），不带行为：会话四原语与双适配器实现位于
// internal/node/sessionx（命名避开 internal/hub/session 的节点连接语义）。上游
// 证据：docs/TASK-agent-session-core.md 与 docs/archive/TESTREPORT-native-session-20261029.md
//（本机双 CLI 两轮续聊探针）。

// 会话适配器类型取值（Session.Adapter；实现见 internal/node/sessionx）。
const (
	// SessionAdapterCodex codex exec 每轮子进程：JSONL 事件流（thread.started /
	// item / turn.completed），续轮经 resume 子命令显式携 thread id。
	SessionAdapterCodex = "codex"
	// SessionAdapterZcode zcode headless 每轮子进程：整份终态 JSON（sessionId /
	// turnId / response / usage），续轮经 --resume 显式携 sessionId。
	SessionAdapterZcode = "zcode"
)

// 会话状态取值（Session.Status）。
const (
	// SessionOpen 会话存活：可继续 Send 续轮。
	SessionOpen = "open"
	// SessionClosed 平台侧已关闭：拒绝续轮；原生线程留存在 CLI 自身存储不受影响
	//（探针适配决策 3：每轮进程回收与会话记录保留分开管理）。
	SessionClosed = "closed"
)

// 轮次执行结果取值（Turn.Status；四路分离的前三路——进程退出码 / 结构解析 /
// 原生轮次完成。第四路「工作是否成功」独立建模为 Turn.WorkResult）。
const (
	// TurnOK 本轮执行成立：退出码 0、输出解析成功、原生轮次完成判据成立（codex=
	// 事件流见 turn.completed / zcode=整份终态 JSON 可解析且 sessionId 在场）三路
	// 齐备。它不声明「工作成功」——T1 无法从 CLI 输出证明工作成败（探针 zcode-r1
	// 正文明说 Bash 无权限工作未完成而三路全过），工作结果见 Turn.WorkResult。
	TurnOK = "ok"
	// TurnFailed 本轮执行不成立：三路任一不成立（退出码非 0 / 输出非法 / 超时 /
	// 原生 ID 缺失或回带不一致）。
	TurnFailed = "failed"
)

// 工作结果取值（Turn.WorkResult；四路分离的第四路，与 Status 独立改判）。
const (
	// WorkUnverified 工作结果未经证实：CLI 输出中不存在可机器判定的「工作成功」
	// 证据，内核不做文本关键词猜测，真实答复（Turn.Response）原样保留供后续验收
	// 链路（T2+）显式改判。T1 全部轮次（含 TurnOK 与 failed）恒为此值。
	WorkUnverified = "unverified"
)

// Session 平台会话记录（node 侧持久化 data_dir/sessions.json 的条目形态）。
// 核心承载是平台 ID ↔ 原生会话 ID 映射：续轮一律以存储的 NativeID 显式续接，
// 回带不一致判本轮失败且映射不动，绝不静默换线程（探针适配决策 1）。
type Session struct {
	// ID 平台会话 ID："s-"+16 字节 hex，sessionx 生成。
	ID string `json:"id"`
	// AgentName 目标 agent 名（node 内唯一，对齐 ExecProfile.Name 语义）。
	AgentName string `json:"agent_name"`
	// Adapter 适配器类型：SessionAdapterCodex | SessionAdapterZcode。
	Adapter string `json:"adapter"`
	// Cwd 会话工作目录（绝对路径；每轮 CLI 的 -C/--cwd 与进程 cwd 同值）。
	Cwd string `json:"cwd"`
	// NativeID 原生会话 ID（codex thread id / zcode sessionId），首轮从输出取回。
	NativeID string `json:"native_id"`
	// PermissionMode 权限模式（创建时的显式配置或适配器保守默认；随会话持久化供
	// 审计。codex=沙箱名（审批策略恒 never）；zcode=--mode 值。绝不默认提权）。
	PermissionMode string `json:"permission_mode"`
	// Status SessionOpen | SessionClosed。
	Status string `json:"status"`
	// TurnCount 已执行轮数（含失败轮；平台侧自计——zcode projection.turnCount 恒 1，
	// 探针明示不可当累计轮数）。
	TurnCount int `json:"turn_count"`
	// Turns 轮次留痕（按 Seq 升序，含失败轮摘要；Response 为解析后的答复正文，
	// 上限对齐 FinalAnswerLimit）。
	Turns []Turn `json:"turns"`
	// CreatedAt 创建时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最近状态变更时刻（UTC）。
	UpdatedAt time.Time `json:"updated_at"`
}

// Turn 单轮摘要（同步阻塞式内核：一轮调用 = 一次 CLI 进程生命周期，全程留痕）。
type Turn struct {
	// Seq 轮序号：1 起，平台侧累计（Create=1，此后每个 Send 递增，含失败轮）。
	Seq int64 `json:"seq"`
	// NativeTurnID 原生轮次 ID（zcode turnId；codex JSONL 无独立轮次 ID，为空）。
	NativeTurnID string `json:"native_turn_id,omitempty"`
	// Status TurnOK | TurnFailed（仅覆盖三路：退出码 / 解析 / 原生轮次完成）。
	Status string `json:"status"`
	// WorkResult 工作结果判定（第四路，与 Status 分离）：T1 恒 WorkUnverified——
	// 输出无法证明工作成功，禁止文本关键词猜测；验收链路（T2+）可显式改判。
	WorkResult string `json:"work_result"`
	// Response 本轮 agent 答复正文（适配器解析产物，非 stdout 原文；UTF-8 清洗后
	// 截断至 FinalAnswerLimit）。
	Response string `json:"response"`
	// ExitCode 进程退出码；信号终止或未运行 = ExitCodeNotRun。
	ExitCode int `json:"exit_code"`
	// Error 失败原因（Status=failed 时非空；成功轮为空）。
	Error string `json:"error,omitempty"`
	// Usage token 用量摘要（解析不出时零值）。
	Usage TurnUsage `json:"usage"`
	// StartedAt 本轮开始时刻（UTC）。
	StartedAt time.Time `json:"started_at"`
	// FinishedAt 本轮结束时刻（UTC）。
	FinishedAt time.Time `json:"finished_at"`
}

// TurnUsage 单轮 token 用量摘要（两家 CLI 输出字段的公共最小集）。
type TurnUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// TotalTokens zcode 直取 totalTokens；codex 输出无独立总数字段，取 input+output 派生。
	TotalTokens int64 `json:"total_tokens"`
}
