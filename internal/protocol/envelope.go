package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ProtocolVersion 当前信封版本号（Envelope.Version，SPEC 4.1；不支持的版本回 err 帧并断开）。
const ProtocolVersion = 1

// ErrFrameTooLarge 帧编码后超过 MaxMessageBytes（1MiB）上限（SPEC 4.6；用 errors.Is 判定）。
var ErrFrameTooLarge = errors.New("frame_too_large")

// 消息类型常量（SPEC 4.2 消息类型表，Envelope.Type 取值）。
const (
	// MsgRegister node→hub 注册（连接后 RegisterTimeout 内首帧）。
	MsgRegister = "register"
	// MsgRegisterAck hub→node 注册应答（携分配 epoch；拒绝则断开 auth_failed）。
	MsgRegisterAck = "register_ack"
	// MsgPing 双向应用层心跳。
	MsgPing = "ping"
	// MsgPong 双向心跳应答。
	MsgPong = "pong"
	// MsgCardUpdate node→hub 能力表全量更新。
	MsgCardUpdate = "card_update"
	// MsgTaskSubmit node→hub 间派单（hub 鉴权后走同 HTTP 等价流程）。
	MsgTaskSubmit = "task_submit"
	// MsgSubmitAck hub→提交方：提交已持久化（dup=true 幂等命中）。
	MsgSubmitAck = "submit_ack"
	// MsgTaskOffer hub→node 推任务（完整 Task）。
	MsgTaskOffer = "task_offer"
	// MsgTaskAck node→hub：任务已入账本（accepted 持久化成功后发）。
	MsgTaskAck = "task_ack"
	// MsgTaskStarted node→hub：执行已开始（started 持久化成功后、spawn 前发）。
	MsgTaskStarted = "task_started"
	// MsgTaskResult node→hub 终态回传（status ∈ completed/failed/needs_review/cancelled，含 FinalAnswer 与产物引用）。
	MsgTaskResult = "task_result"
	// MsgResultAck hub→node：终态已持久化。
	MsgResultAck = "result_ack"
	// MsgResultEvent hub→提交方（node 会话时）终态推送；HTTP 提交方靠 GET /tasks/{id} 轮询。
	MsgResultEvent = "result_event"
	// MsgTaskQuery 提交方→hub 查询任务状态（result_event 离线错过的补查路径）。
	MsgTaskQuery = "task_query"
	// MsgTaskStatus hub→提交方查询响应（完整 Task）。
	MsgTaskStatus = "task_status"
	// MsgArtifactFetch 提交方→hub 请求取回第 index 个产物（hub 中继到执行 node）。
	MsgArtifactFetch = "artifact_fetch"
	// MsgArtifactData 执行 node→请求方（经 hub 中继）产物内容。
	MsgArtifactData = "artifact_data"
	// MsgTaskCancel hub→node 取消任务（管理面 task_cancel 生命周期）：node 整组终止
	// 在途执行，账本落 finished(cancelled/user_cancelled) 后回 task_result(cancelled)；
	// 任务已终态或不在账本则回 task_cancelled 幂等 ack（至少一次+幂等，重复 cancel 安全）。
	MsgTaskCancel = "task_cancel"
	// MsgTaskCancelled node→hub 取消确认 ack：任务已终态或不在账本（无可取消对象）。
	// 执行中断路径不走本帧——回 task_result(cancelled) 走三段确认（task_result→result_ack）。
	MsgTaskCancelled = "task_cancelled"
	// MsgErr 双向错误帧；协议级错误（版本/epoch 残余/格式）另按规则断开。
	MsgErr = "err"
)

// Envelope WebSocket 文本帧信封：每帧一个 JSON（SPEC 4.1）。
// 字段与 json tag 严格照抄 SPEC；入站 From 由 hub 以连接鉴权身份覆盖，不信帧内值。
type Envelope struct {
	// Version 协议版本，当前 1。
	Version int `json:"v"`
	// Type 消息类型（MsgXxx 常量）。
	Type string `json:"type"`
	// MessageID 去重键。
	MessageID string `json:"mid"`
	// From 发送方身份。
	From string `json:"from"`
	// To 接收方身份。
	To string `json:"to"`
	// ReplyTo 关联的请求消息 ID。
	ReplyTo string `json:"reply_to,omitempty"`
	// IdemKey 幂等键。
	IdemKey string `json:"idem,omitempty"`
	// SessionEpoch 会话代次（SPEC 4.4；register 帧豁免校验）。
	SessionEpoch int64 `json:"epoch,omitempty"`
	// TraceID 链路追踪 ID。
	TraceID string `json:"trace_id,omitempty"`
	// Payload 类型相关载荷（SPEC 4.2 表）。
	Payload json.RawMessage `json:"payload"`
	// Err 错误体（type=err 或携带错误语义时）。
	Err *ErrInfo `json:"err,omitempty"`
	// Retryable 调用方可重试标记。
	Retryable bool `json:"retryable,omitempty"`
	// ExpireAt 失效时刻（UTC）。
	ExpireAt time.Time `json:"expire_at,omitempty"`
}

// ErrInfo 协议错误体（SPEC 4.1）。
type ErrInfo struct {
	// Code 错误码（CodeXxx 常量）。
	Code string `json:"code"`
	// Msg 人类可读补充信息。
	Msg string `json:"msg"`
}

// Encode 序列化信封为一条文本帧字节，并按 SPEC 4.6 以最终编码实测校验 1MiB 上限。
func Encode(e *Envelope) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxMessageBytes {
		return nil, fmt.Errorf("%w: 编码后 %d 字节 > 上限 %d", ErrFrameTooLarge, len(b), MaxMessageBytes)
	}
	return b, nil
}

// Decode 解析一条文本帧为信封；超过 1MiB 上限的帧直接拒绝（SPEC 4.6）。
func Decode(b []byte) (*Envelope, error) {
	if len(b) > MaxMessageBytes {
		return nil, fmt.Errorf("%w: 帧长 %d 字节 > 上限 %d", ErrFrameTooLarge, len(b), MaxMessageBytes)
	}
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
