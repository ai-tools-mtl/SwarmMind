package api

// sessions_w01_03_test.go W01-03 REST 契约（docs/tasks/v3/W01-03-rfc.md）：创建
// request_id 幂等三态（200+dup / 409 request_conflict / 无键行为零变化）、取消
// 端点（鉴权、未投递撤回 queued 同步回执、轮行投影 cancelled、未知轮 404）、
// close 在途 409 close_in_flight、队列上限 409 turn_queue_full。

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestSessionRESTW0103CreateIdempotency 创建 request_id REST 三态。
func TestSessionRESTW0103CreateIdempotency(t *testing.T) {
	f := newFixture(t)
	f.sessionNode()
	hc := f.client(true)
	body := `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/p","request_id":"rid-rest-1"}`
	sid := f.createSession(hc, body) // 201（helper 内断言）
	// 同键同文：200 + dup + 原会话
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions", body)
	if code != http.StatusOK || !strings.Contains(string(resp), `"dup":true`) || !strings.Contains(string(resp), sid) {
		t.Fatalf("同键同文应 200+dup 返原会话, got %d %s", code, resp)
	}
	// 同键异文：409 request_conflict
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions", `{"node_id":"mac-mini","agent":"coder","cwd":"/other","request_id":"rid-rest-1"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "request_conflict") {
		t.Fatalf("同键异文应 409 request_conflict, got %d %s", code, resp)
	}
	// 无 request_id：行为零变化（201 新会话）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions", `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/p"}`)
	if code != http.StatusCreated {
		t.Fatalf("无 request_id 应 201, got %d %s", code, resp)
	}
}

// TestSessionRESTW0103CancelOffline 未投递轮取消（REST）：匿名 401、200 同步
// queued 回执、详情轮投影 cancelled、重复取消幂等、未知轮 404、close 在途 409。
func TestSessionRESTW0103CancelOffline(t *testing.T) {
	f := newFixture(t)
	// node 不注册假 handler：SetOnline 前先注册会话能力卡（离线暂存形态）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder",
		Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true, SessionAdapter: "codex"}}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	f.core.SetOnline("mac-mini", false)
	hc := f.client(true)
	sid := f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp"}`)
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"离线轮"}`)
	if code != http.StatusAccepted {
		t.Fatalf("投轮应 202, got %d %s", code, resp)
	}
	// 匿名取消 401
	if code, _ := f.do(f.client(false), http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("匿名取消应 401, got %d", code)
	}
	// close 在途拒绝（取消先于关闭的人话指路）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/close", `{}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "close_in_flight") {
		t.Fatalf("在途 close 应 409 close_in_flight, got %d %s", code, resp)
	}
	// 取消：200 同步 queued
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{"reason":"测试"}`)
	if code != http.StatusOK || !strings.Contains(string(resp), `"outcome":"queued"`) || !strings.Contains(string(resp), "turn_cancelled_queued") {
		t.Fatalf("取消应 200 queued, got %d %s", code, resp)
	}
	// 详情轮投影 cancelled
	code, resp = f.do(hc, http.MethodGet, "/api/v1/sessions/"+sid, "")
	if code != 200 || !strings.Contains(string(resp), `"status":"cancelled"`) {
		t.Fatalf("轮投影应 cancelled: %d %s", code, resp)
	}
	// 重复取消幂等回执
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns/1/cancel", `{}`)
	if code != http.StatusOK || !strings.Contains(string(resp), `"outcome":"queued"`) {
		t.Fatalf("重复取消应幂等, got %d %s", code, resp)
	}
	// 未知轮 404
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns/9/cancel", `{}`)
	if code != http.StatusNotFound || !strings.Contains(string(resp), "turn_not_found") {
		t.Fatalf("未知轮应 404 turn_not_found, got %d %s", code, resp)
	}
	// 收束后 close 成功（取消后无在途轮）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/close", `{}`)
	if code != 200 || !strings.Contains(string(resp), `"closed"`) {
		t.Fatalf("取消后 close 应 200, got %d %s", code, resp)
	}
}

// TestSessionRESTW0103QueueFull 队列上限（REST）：预置 16 条非终态轮后第 17 条
// 409 turn_queue_full。
func TestSessionRESTW0103QueueFull(t *testing.T) {
	f := newFixture(t)
	f.sessionNode()
	hc := f.client(true)
	sid := f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/q"}`)
	// 预置：seq=1 终态成功 create + seq 2..17 非终态（绕过假 node 即时完成，
	// 直接落注册表——判定口径是轮行现态）
	if err := f.st.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		s.Turns = []store.SessionTurn{{Seq: 1, TaskID: "t-seed", NativeIDOk: true,
			WorkResult: protocol.WorkUnverified, FinishedAt: time.Now()}}
		for i := int64(2); i <= 17; i++ {
			// PromptHash 留空：重放判定回退截断文本比对（"x" == "x"），供末尾
			// 幂等回执不受限断言使用。
			s.Turns = append(s.Turns, store.SessionTurn{Seq: i, TaskID: "t-fill", Prompt: "x"})
		}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatalf("预置: %v", err)
	}
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"第17条"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "turn_queue_full") {
		t.Fatalf("第 17 条应 409 turn_queue_full, got %d %s", code, resp)
	}
	// 幂等回执不受限（重放既有轮）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"x","turn_seq":2}`)
	if code != http.StatusOK || !strings.Contains(string(resp), `"dup":true`) {
		t.Fatalf("重放应 200+dup 不受队列限制, got %d %s", code, resp)
	}
}
