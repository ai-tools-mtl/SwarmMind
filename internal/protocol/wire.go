package protocol

// 线载荷结构（SPEC 4.2 消息类型表的 payload 定义）。
// 各帧 Payload 即本文件结构的 JSON 编码；hub/node 共享，保证两侧编解码一致。

// TaskRef 单任务引用载荷：task_ack / task_started / result_ack / task_query 共用
// （SPEC 4.2：{task_id}）。
type TaskRef struct {
	// TaskID 任务 ID。
	TaskID string `json:"task_id"`
}

// SubmitAckPayload submit_ack 载荷（SPEC 4.2：{task_id, trace_id, dup}）。
type SubmitAckPayload struct {
	// TaskID 持久化后的任务 ID。
	TaskID string `json:"task_id"`
	// TraceID 链路追踪 ID。
	TraceID string `json:"trace_id"`
	// Dup 幂等命中（返回原 task_id）。
	Dup bool `json:"dup"`
}

// TaskCancelPayload task_cancel 载荷（hub→node）：取消指定任务。
type TaskCancelPayload struct {
	// TaskID 任务 ID。
	TaskID string `json:"task_id"`
	// Reason 取消原因（人话，随日志/审计展示；可空）。
	Reason string `json:"reason,omitempty"`
}

// TaskResultPayload task_result / result_event 载荷（SPEC 4.2：{task_id, status, result}）。
type TaskResultPayload struct {
	// TaskID 任务 ID。
	TaskID string `json:"task_id"`
	// Status 终态：completed | failed | needs_review | cancelled（ResultXxx 常量）。
	Status string `json:"status"`
	// Result 终态结果（含 FinalAnswer 与产物引用）。
	Result *TaskResult `json:"result"`
}

// CardUpdatePayload card_update 载荷（SPEC 4.2：{agents[]}，能力表全量更新）。
type CardUpdatePayload struct {
	// Agents AgentCard 全量。
	Agents []AgentCard `json:"agents"`
}

// RegisterPayload register 载荷（SPEC 4.2：{node_id, register_token, agents[]}）。
type RegisterPayload struct {
	// NodeID 节点身份。
	NodeID string `json:"node_id"`
	// RegisterToken 注册令牌（与 node_id 绑定）。
	RegisterToken string `json:"register_token"`
	// Agents 能力表全量。
	Agents []AgentCard `json:"agents"`
}

// ArtifactFetchPayload artifact_fetch 载荷（SPEC 4.2：{task_id, index}，
// 提交方→hub 请求取回第 index 个产物，hub 中继到执行 node）。
type ArtifactFetchPayload struct {
	// TaskID 任务 ID。
	TaskID string `json:"task_id"`
	// Index 产物下标（任务 Result.Artifacts 序）。
	Index int `json:"index"`
}

// DepResolvePayload dep_resolve 载荷（P4④第二子轮/SPEC 4.12：{idem}，
// node→hub 按依赖 idem 请求上游任务定位与产物清单）。
type DepResolvePayload struct {
	// Idem 依赖的上游任务 idem_key。
	Idem string `json:"idem"`
}

// ArtifactDataPayload artifact_data 载荷（SPEC 4.2：{task_id, index, meta,
// data_base64}，执行 node→请求方经 hub 中继；单文件原始 ≤640KiB，base64 后
// 随帧 ≤1MiB，更大产物 V1 分块）。
type ArtifactDataPayload struct {
	// TaskID 任务 ID。
	TaskID string `json:"task_id"`
	// Index 产物下标。
	Index int `json:"index"`
	// Meta 产物引用（路径/SHA-256/大小，与登记一致）。
	Meta Artifact `json:"meta"`
	// DataBase64 产物内容（base64 标准编码）。
	DataBase64 string `json:"data_base64"`
}

// RegisterAckPayload register_ack 载荷（SPEC 4.2：{ok, epoch, reason?}）。
type RegisterAckPayload struct {
	// OK 注册是否成功。
	OK bool `json:"ok"`
	// Epoch 分配的会话代次（成功时携带）。
	Epoch int64 `json:"epoch"`
	// Reason 拒绝原因（失败时携带，CodeXxx）。
	Reason string `json:"reason,omitempty"`
}
