package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeRoundtrip 全字段信封编码→解码往返，逐字段核对（含时间与 ErrInfo）。
func TestEnvelopeRoundtrip(t *testing.T) {
	expire := time.Date(2026, 10, 21, 12, 30, 0, 123456789, time.UTC)
	env := &Envelope{
		Version:      ProtocolVersion,
		Type:         MsgTaskResult,
		MessageID:    "mid-001",
		From:         "node-a",
		To:           "hub",
		ReplyTo:      "mid-000",
		IdemKey:      "idem-001",
		SessionEpoch: 7,
		TraceID:      "trace-001",
		Payload:      json.RawMessage(`{"task_id":"t-abcd","status":"completed"}`),
		Err:          &ErrInfo{Code: CodeResultConflict, Msg: "结果冲突"},
		Retryable:    true,
		ExpireAt:     expire,
	}
	b, err := Encode(env)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Version != env.Version {
		t.Errorf("Version=%d, 期望 %d", got.Version, env.Version)
	}
	if got.Type != env.Type {
		t.Errorf("Type=%q, 期望 %q", got.Type, env.Type)
	}
	if got.MessageID != env.MessageID {
		t.Errorf("MessageID=%q, 期望 %q", got.MessageID, env.MessageID)
	}
	if got.From != env.From || got.To != env.To {
		t.Errorf("From/To=%q/%q, 期望 %q/%q", got.From, got.To, env.From, env.To)
	}
	if got.ReplyTo != env.ReplyTo {
		t.Errorf("ReplyTo=%q, 期望 %q", got.ReplyTo, env.ReplyTo)
	}
	if got.IdemKey != env.IdemKey {
		t.Errorf("IdemKey=%q, 期望 %q", got.IdemKey, env.IdemKey)
	}
	if got.SessionEpoch != env.SessionEpoch {
		t.Errorf("SessionEpoch=%d, 期望 %d", got.SessionEpoch, env.SessionEpoch)
	}
	if got.TraceID != env.TraceID {
		t.Errorf("TraceID=%q, 期望 %q", got.TraceID, env.TraceID)
	}
	if string(got.Payload) != string(env.Payload) {
		t.Errorf("Payload=%s, 期望 %s", got.Payload, env.Payload)
	}
	if got.Err == nil || got.Err.Code != env.Err.Code || got.Err.Msg != env.Err.Msg {
		t.Errorf("Err=%+v, 期望 %+v", got.Err, env.Err)
	}
	if got.Retryable != env.Retryable {
		t.Errorf("Retryable=%v, 期望 %v", got.Retryable, env.Retryable)
	}
	if !got.ExpireAt.Equal(expire) {
		t.Errorf("ExpireAt=%v, 期望 %v", got.ExpireAt, expire)
	}
}

// TestEnvelopeJSONKeys 必现键与 omitempty 键的金样核对（json tag 严格按 SPEC 4.1）。
func TestEnvelopeJSONKeys(t *testing.T) {
	env := Envelope{
		Version:   1,
		Type:      MsgPing,
		MessageID: "m1",
		From:      "a",
		To:        "b",
		Payload:   json.RawMessage("null"),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"v", "type", "mid", "from", "to", "payload"} {
		if _, ok := m[k]; !ok {
			t.Errorf("必现键 %q 缺失（编码结果：%s）", k, b)
		}
	}
	// 注：SPEC 4.1 的 tag 逐字照抄，ExpireAt 带 omitempty；但 Go encoding/json 的
	// omitempty 对 struct（time.Time）不生效，零值会序列化为零时刻字符串——
	// 这是按 SPEC 原样实现的行为，实现不做偏离。
	for _, k := range []string{"reply_to", "idem", "epoch", "trace_id", "err", "retryable"} {
		if _, ok := m[k]; ok {
			t.Errorf("零值下 omitempty 键 %q 不应出现（编码结果：%s）", k, b)
		}
	}
	if got := string(m["expire_at"]); got != `"0001-01-01T00:00:00Z"` {
		t.Errorf("零值 ExpireAt 应序列化为零时刻字符串，得到 %s", got)
	}
}

// TestFrameSizeLimit 超限帧编码/解码两侧均按最终编码实测拒绝（SPEC 4.6）。
func TestFrameSizeLimit(t *testing.T) {
	payload := json.RawMessage(`"` + strings.Repeat("a", MaxMessageBytes) + `"`)
	env := &Envelope{
		Version: ProtocolVersion, Type: MsgTaskOffer, MessageID: "m-big",
		From: "hub", To: "node-a", Payload: payload,
	}
	if _, err := Encode(env); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("超限 Encode 应返回 ErrFrameTooLarge，得到 %v", err)
	}
	oversize := bytes.Repeat([]byte(" "), MaxMessageBytes+1)
	if _, err := Decode(oversize); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("超限 Decode 应返回 ErrFrameTooLarge，得到 %v", err)
	}
	// 正常小帧不受影响
	small := &Envelope{Version: ProtocolVersion, Type: MsgPing, MessageID: "m1", From: "a", To: "b", Payload: json.RawMessage("null")}
	if _, err := Encode(small); err != nil {
		t.Fatalf("正常帧 Encode: %v", err)
	}
}

// TestEnvelopeFenceEpoch 任务租约字段（P4/SPEC 4.11）：携带时往返保留；零值
// omitempty 不出现在编码结果（旧版对端缺省语义的载体形态）。
func TestEnvelopeFenceEpoch(t *testing.T) {
	env := &Envelope{
		Version:    ProtocolVersion,
		Type:       MsgTaskResult,
		MessageID:  "mid-fence",
		From:       "node-a",
		To:         "hub",
		FenceEpoch: 42,
		Payload:    json.RawMessage(`{}`),
	}
	b, err := Encode(env)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(b), `"fence_epoch":42`) {
		t.Fatalf("编码结果应含 fence_epoch=42: %s", b)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.FenceEpoch != 42 {
		t.Fatalf("FenceEpoch=%d, 期望 42", got.FenceEpoch)
	}

	zero := &Envelope{Version: ProtocolVersion, Type: MsgPing, MessageID: "m1", Payload: json.RawMessage("null")}
	bz, err := Encode(zero)
	if err != nil {
		t.Fatalf("Encode 零值: %v", err)
	}
	if strings.Contains(string(bz), "fence_epoch") {
		t.Fatalf("零值 FenceEpoch 不应出现（omitempty）: %s", bz)
	}
}
