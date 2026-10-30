package store

// session_w01_03_test.go W01-03 加法字段 round-trip（RequestID/PayloadHash/
// TurnRequestID）：随事务落盘 → 重开恢复逐字段一致（旧条目缺省零值零迁移）。

import (
	"testing"
	"time"
)

// TestSessionW0103FieldsRoundTrip W01-03 加法字段随快照持久化：重开不丢、零值
// 字段不出现（omitempty）。
func TestSessionW0103FieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now().UTC()
	want := testSession("s-w0103")
	want.RequestID = "rid-create-1"
	want.PayloadHash = "deadbeef"
	want.Turns = []SessionTurn{{
		Seq: 1, TaskID: "t-1", Op: "create", TraceID: "tr-1",
		Prompt: "p", PromptHash: "cafe", RequestID: "rid-turn-1", CreatedAt: now,
	}}
	if err := st.Update(func(s *State) error { s.Sessions[want.ID] = want; return nil }); err != nil {
		t.Fatalf("写会话: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer st2.Close()
	got, ok := st2.View().Sessions[want.ID]
	if !ok {
		t.Fatal("重开后会话丢失")
	}
	if got.RequestID != "rid-create-1" || got.PayloadHash != "deadbeef" {
		t.Fatalf("创建幂等字段丢失: %+v", got)
	}
	if len(got.Turns) != 1 || got.Turns[0].RequestID != "rid-turn-1" {
		t.Fatalf("轮行 RequestID 丢失: %+v", got.Turns)
	}
}
