package protocol

import "time"

// 运行常量（SPEC 4.6 全表 + 4.9 编码后预算），hub/node 共享。
const (
	// RegisterTimeout 注册首帧超时：连接后 5s 内未收到 register 即断开。
	RegisterTimeout = 5 * time.Second
	// PingInterval 应用层心跳 Ping 周期。
	PingInterval = 15 * time.Second
	// PongTimeout Pong 超时（计时基于上次 pong 收到时刻）。
	PongTimeout = 45 * time.Second
	// ReadWriteTimeout WebSocket 读写 deadline。
	ReadWriteTimeout = 60 * time.Second
	// BackoffInitial 重连退避起始间隔。
	BackoffInitial = 500 * time.Millisecond
	// BackoffMax 重连退避上限。
	BackoffMax = 60 * time.Second
	// BackoffJitter 重连退避抖动幅度（±20%）；注册成功后重置，auth_failed 不重试。
	BackoffJitter = 0.2
	// OfferAckTimeout offered 等待 task_ack 超时：30s 未确认回退 submitted 重推。
	OfferAckTimeout = 30 * time.Second
	// SendStallTimeout 数据发送队列持续无法出队的容忍时长，超时断开重连（补推幂等，安全）。
	SendStallTimeout = 60 * time.Second

	// TaskRetention 任务离线保留时长（expire_at = CreatedAt+7d；到期非终态 → failed/error=expired）。
	TaskRetention = 7 * 24 * time.Hour
	// TerminalRetention 终态保留时长：hub 快照 24h 后清理终态任务；node 账本 result_acked 条目 24h 后清理。
	TerminalRetention = 24 * time.Hour
	// WorkdirQueueWait workdir 互斥排队等待上限（P1.5/SPEC 4.10）：submitted 任务自创建
	// 起排队超过此时长仍未获得派发 → failed（error=workdir_busy_timeout）。hub 侧计时
	// 可行（不依赖 ExecProfile 的 timeout）；排队不计入 agent 执行超时（超时从 spawn 起算）。
	WorkdirQueueWait = 30 * time.Minute

	// MaxMessageBytes 单消息上限 = 1MiB，作用于完整信封序列化后字节数（按最终编码实测校验）。
	MaxMessageBytes = 1 << 20
	// MaxNodeIDLen node_id 字节长度上限（N2：From 计入信封体积计量，node_id 必须
	// 有界——配置校验拒绝超长身份，杜绝 register/结果信封被 From 撑爆）。
	MaxNodeIDLen = 64
	// SendQueueLimit 每会话数据发送队列字节上限（4MiB）；满则暂停该 node 出队。
	SendQueueLimit = 4 << 20
	// ControlQueueLimit 控制帧（ping/pong/ack 类）独立小缓冲字节上限（64KiB）；控制帧也满 → 断开。
	ControlQueueLimit = 64 << 10
	// MailboxQuota 每 node 信箱条数配额（计 submitted 状态）；满则 503 mailbox_full。
	MailboxQuota = 100
	// MailboxByteBudget 每 node 信箱字节预算（submitted 任务体字节和，8MiB）。
	MailboxByteBudget = 8 << 20
	// StorageBudget hub 任务存储全局字节预算（256MiB，含终态 24h 保留）；超则拒新提交 503 storage_full。
	StorageBudget = 256 << 20
	// DeliveryWindow 每 node 投递窗口（已投递未开始 = offered+acknowledged 计数，8）；超则暂停出队。
	DeliveryWindow = 8
	// DefaultConcurrency node 执行并发默认值（可配）。
	DefaultConcurrency = 2

	// TailLimit stdout/stderr tail 截断上限（64KiB）。
	TailLimit = 64 << 10
	// FinalAnswerLimit FinalAnswer 截断上限（512KiB；stdout 全文 UTF-8 清洗后截断，SPEC 4.9）。
	FinalAnswerLimit = 512 << 10
	// ArtifactLimit 单产物原始字节上限（640KiB；base64 后随帧仍须 ≤ MaxMessageBytes）。
	ArtifactLimit = 640 << 10
	// MaxArtifacts 单任务产物条数上限（8 条且仅存引用）。
	MaxArtifacts = 8
)

// 协议/结果错误码：err 帧 ErrInfo.Code 与 TaskResult.Error 的约定取值（出处见各注释）。
const (
	// CodeAuthFailed register 鉴权失败（SPEC 4.2 register_ack）。
	CodeAuthFailed = "auth_failed"
	// CodeUnsupportedVersion 协议版本不支持（SPEC 4.1：回 err 帧并断开）。
	CodeUnsupportedVersion = "unsupported_version"
	// CodeFrameTooLarge 帧超过单消息上限（SPEC 4.6）。
	CodeFrameTooLarge = "frame_too_large"
	// CodeMailboxFull 信箱条数/字节预算满（SPEC 4.6）。
	CodeMailboxFull = "mailbox_full"
	// CodeStorageFull hub 存储全局预算满（SPEC 4.6）。
	CodeStorageFull = "storage_full"
	// CodeAgentNotExposed 目标 agent 未接入/已下线（SPEC 4.9 AgentCard.Exposed）。
	CodeAgentNotExposed = "agent_not_exposed"
	// CodeAgentNotFound 目标 agent 不在当前配置（SPEC 4.3 配置漂移行）。
	CodeAgentNotFound = "agent_not_found"
	// CodeResultConflict 终态冲突：保留先到结果（SPEC 4.3 收敛表）。
	CodeResultConflict = "result_conflict"
	// CodeArtifactMismatch 产物 SHA-256 与登记不一致（SPEC 4.8）。
	CodeArtifactMismatch = "artifact_mismatch"
	// CodeExpired 任务过期（SPEC 4.3/4.8）。
	CodeExpired = "expired"
	// CodeTimeout 执行超时（SPEC 4.9 TaskResult.Error）。
	CodeTimeout = "timeout"
	// CodePolicyDenied 白名单/权限拒绝（SPEC 4.9 TaskResult.Error）。
	CodePolicyDenied = "policy_denied"
	// CodeNodeRestartedMidway started 未 finished 时 node 重启（SPEC 4.8）。
	CodeNodeRestartedMidway = "node_restarted_midway"
	// CodeUserCancelled 管理面取消（task_cancel 生命周期：TaskResult.Error 标记）。
	CodeUserCancelled = "user_cancelled"
	// CodeTruncated 编码后超限被截断（SPEC 4.9 编码后预算）。
	CodeTruncated = "truncated"
	// CodeTaskNotFound 任务不存在（task_query 查无此任务 / 已过保留期清理；err 帧码）。
	CodeTaskNotFound = "task_not_found"
	// CodeArtifactNotFound 任务无终态结果、产物下标越界或文件已不存在（err 帧码）。
	CodeArtifactNotFound = "artifact_not_found"
	// CodeWorkdirBusyTimeout workdir 互斥排队超时（P1.5/SPEC 4.10：排队超
	// WorkdirQueueWait 未获得派发，hub 置 failed）。
	CodeWorkdirBusyTimeout = "workdir_busy_timeout"
	// CodeInvalidDependency 依赖声明不合法（P4.3/SPEC 4.12：未知 idem/自引用/
	// 重复依赖/超深度/超级联/依赖环，提交校验链拒绝；HTTP 400 同码）。
	CodeInvalidDependency = "invalid_dependency"
)

// 管理面/本地 API 附加错误码：hub 侧中继与连接状态（非协议帧码，HTTP 错误体用）。
const (
	// CodeNodeOffline 目标 node 当前不在线，无法中继取回产物。
	CodeNodeOffline = "node_offline"
	// CodeFetchTimeout 产物中继取回超时（SPEC 六：30s → 504）。
	CodeFetchTimeout = "fetch_timeout"
	// CodeHubUnreachable node 本地 API 场景：与 hub 的会话未建立/不可达。
	CodeHubUnreachable = "hub_unreachable"
)
