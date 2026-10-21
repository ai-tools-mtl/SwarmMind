package api

// A2A 兼容端点测试（SPEC 六.五「A2A 兼容端点」）：发现卡结构与免鉴权、
// message/send 全链（含幂等/错误映射）、tasks/get 状态映射、未知方法与
// 非法请求、鉴权门。复用 server_test.go 的 fixture 与辅助工具。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// a2aResp 测试侧的 JSON-RPC 响应视图。
type a2aResp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *a2aRPCError    `json:"error"`
}

// a2aCall 以给定客户端调用 /a2a/rpc，返回 HTTP 状态码与解析后的 JSON-RPC 响应。
func a2aCall(t *testing.T, f *fixture, hc *http.Client, method, params string) (int, a2aResp) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	code, out := f.do(hc, http.MethodPost, "/a2a/rpc", body)
	var resp a2aResp
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("响应非 JSON-RPC: %d %s", code, out)
	}
	return code, resp
}

// a2aTaskView 测试侧的 A2A Task 形状视图。
type a2aTaskView struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State     string `json:"state"`
		Timestamp string `json:"timestamp"`
	} `json:"status"`
	Kind     string            `json:"kind"`
	Metadata map[string]string `json:"metadata"`
}

// parseA2ATask 解出 result 中的 A2A Task。
func parseA2ATask(t *testing.T, resp a2aResp) a2aTaskView {
	t.Helper()
	var v a2aTaskView
	if err := json.Unmarshal(resp.Result, &v); err != nil {
		t.Fatalf("result 非 A2A Task: %s", resp.Result)
	}
	return v
}

// setTaskStatus 经 store 事务直改任务状态（A2A 状态映射表断言用；这些状态的
// 产生路径已在 core/账本测试覆盖，此处只造状态）。
func (f *fixture) setTaskStatus(taskID string, st protocol.TaskStatus) {
	f.t.Helper()
	err := f.st.Update(func(s *store.State) error {
		t, ok := s.Tasks[taskID]
		if !ok {
			return errors.New("task missing: " + taskID)
		}
		nt := t.Clone()
		nt.Status = st
		s.Tasks[taskID] = nt
		return nil
	})
	if err != nil {
		f.t.Fatalf("置 %s 失败: %v", st, err)
	}
}

// TestAgentCard 发现卡：结构断言、skills 由 exposed 目录聚合（未暴露不出）、
// 免鉴权可达、白名单（无执行配置/令牌/工作区互斥键）。
func TestAgentCard(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1,
		WorkspaceID: "abcdef0123456789", Exposed: true, Description: "回声测试",
	}})
	f.core.RegisterNode("nodeE", []protocol.AgentCard{{
		Name: "secret", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1,
		Exposed: false, // 审计登记：不入发现卡
	}})

	// 免鉴权：无 cookie 直接可达（A2A 客户端发现惯例）
	hc := f.client(false)
	code, out := f.do(hc, http.MethodGet, "/.well-known/agent.json", "")
	if code != http.StatusOK {
		t.Fatalf("发现卡应免鉴权 200, got %d", code)
	}
	// 白名单：执行配置/令牌/工作区互斥键/审计开关一概不出
	for _, leak := range []string{"workdir", "command", "permission_mode", "workspace_id",
		"exposed", "allow_env", "timeout_sec", "smsw_", "test-admin-token", "secret"} {
		if strings.Contains(string(out), leak) {
			t.Fatalf("发现卡泄漏 %q: %s", leak, out)
		}
	}
	var card struct {
		ProtocolVersion string `json:"protocolVersion"`
		Name            string `json:"name"`
		Description     string `json:"description"`
		URL             string `json:"url"`
		Version         string `json:"version"`
		Capabilities    struct {
			Streaming         bool `json:"streaming"`
			PushNotifications bool `json:"pushNotifications"`
		} `json:"capabilities"`
		DefaultInputModes  []string `json:"defaultInputModes"`
		DefaultOutputModes []string `json:"defaultOutputModes"`
		Skills             []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(out, &card); err != nil {
		t.Fatalf("发现卡非合法 JSON: %v: %s", err, out)
	}
	if card.ProtocolVersion != "0.2.6" || card.Name != "SwarmMind" ||
		card.Description != "跨机器 agent 协作网络" || card.Version != "0.2.0" {
		t.Fatalf("发现卡头部不符: %+v", card)
	}
	// url 从请求 Host 推导（httptest：http://127.0.0.1:port/）
	if want := f.srv.URL + "/"; card.URL != want {
		t.Fatalf("url 应为 %q, got %q", want, card.URL)
	}
	if card.Capabilities.Streaming || card.Capabilities.PushNotifications {
		t.Fatalf("V0 能力位应全 false: %+v", card.Capabilities)
	}
	if len(card.DefaultInputModes) != 1 || card.DefaultInputModes[0] != "text" ||
		len(card.DefaultOutputModes) != 1 || card.DefaultOutputModes[0] != "text" {
		t.Fatalf("默认输入输出模式应为 [text]: %+v", card)
	}
	// skills：仅 exposed 条目，id=node/agent
	if len(card.Skills) != 1 {
		t.Fatalf("应只含 1 条 exposed skill: %+v", card.Skills)
	}
	sk := card.Skills[0]
	if sk.ID != "nodeB/echo" || sk.Name != "echo" || sk.Description != "回声测试" || len(sk.Tags) == 0 {
		t.Fatalf("skill 不符: %+v", sk)
	}
}

// TestA2AMessageSend message/send 全链：官方 kind 形状与简写 type 形状均受理；
// 任务真实入库（REST 可查、submitted_by=a2a、text 进 args）；messageId 幂等
// 重试去重、同键不同内容冲突；目标/参数错误 → -32602。
func TestA2AMessageSend(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)

	// 官方形状：kind:"text" + messageId
	params := `{"message":{"role":"user","parts":[{"kind":"text","text":"巡检 nodeB"}],"messageId":"msg-1"},"metadata":{"target_node":"nodeB","target_agent":"echo"}}`
	code, resp := a2aCall(t, f, hc, "message/send", params)
	if code != http.StatusOK || resp.Error != nil {
		t.Fatalf("message/send 应成功: %d %s", code, resp.Result)
	}
	task := parseA2ATask(t, resp)
	if !strings.HasPrefix(task.ID, "t-") || task.ContextID == "" ||
		task.Status.State != "submitted" || task.Kind != "task" {
		t.Fatalf("A2A task 形状不符: %+v", task)
	}
	if task.Metadata["swarmmind_status"] != "submitted" {
		t.Fatalf("metadata 应保留原始状态: %+v", task.Metadata)
	}

	// REST 同链验证：任务真实入库，身份 a2a，text 进 args
	code, out := f.do(hc, http.MethodGet, "/api/v1/tasks/"+task.ID, "")
	var pt protocol.Task
	if code != http.StatusOK || json.Unmarshal(out, &pt) != nil {
		t.Fatalf("REST 查询应 200: %d %s", code, out)
	}
	if pt.TargetNode != "nodeB" || pt.TargetAgent != "echo" || pt.SubmittedBy != a2aIdentity ||
		len(pt.Input.Args) != 1 || pt.Input.Args[0] != "巡检 nodeB" {
		t.Fatalf("入库任务不符: %+v", pt)
	}

	// 简写变体：type:"text"（无 messageId）同样受理
	code, resp = a2aCall(t, f, hc, "message/send",
		`{"message":{"role":"user","parts":[{"type":"text","text":"第二条"}]},"metadata":{"target_node":"nodeB","target_agent":"echo"}}`)
	if resp.Error != nil {
		t.Fatalf("type 变体应受理: %s", resp.Error.Message)
	}
	if got := parseA2ATask(t, resp).ID; got == task.ID {
		t.Fatal("不同请求应产生不同任务")
	}

	// messageId 幂等：同 messageId 同内容重试 → 同一 task_id
	code, resp = a2aCall(t, f, hc, "message/send", params)
	if resp.Error != nil || parseA2ATask(t, resp).ID != task.ID {
		t.Fatalf("messageId 重试应命中同一任务: %s / %+v", resp.Result, resp.Error)
	}
	// 同 messageId 不同内容 → -32000（幂等冲突，与 REST idem 语义一致）
	_, resp = a2aCall(t, f, hc, "message/send",
		`{"message":{"role":"user","parts":[{"kind":"text","text":"变了"}],"messageId":"msg-1"},"metadata":{"target_node":"nodeB","target_agent":"echo"}}`)
	if resp.Error == nil || resp.Error.Code != rpcServerError {
		t.Fatalf("同 messageId 不同内容应 -32000: %+v", resp.Error)
	}

	// 目标不存在 → -32602；缺 text/缺 target → -32602
	_, resp = a2aCall(t, f, hc, "message/send",
		`{"message":{"parts":[{"kind":"text","text":"x"}]},"metadata":{"target_node":"ghost","target_agent":"echo"}}`)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("目标不存在应 -32602: %+v", resp.Error)
	}
	_, resp = a2aCall(t, f, hc, "message/send",
		`{"message":{"parts":[{"kind":"text","text":"x"}]},"metadata":{"target_node":"nodeB"}}`)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("缺 target_agent 应 -32602: %+v", resp.Error)
	}
	_, resp = a2aCall(t, f, hc, "message/send",
		`{"message":{"parts":[]},"metadata":{"target_node":"nodeB","target_agent":"echo"}}`)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("空 parts 应 -32602: %+v", resp.Error)
	}
}

// TestA2ATasksGet tasks/get 与状态映射表：submitted→submitted、running→working、
// completed→completed、needs_review→input-required（原始状态保留）；未知任务
// -32001；缺 id -32602。
func TestA2ATasksGet(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)
	taskID := f.submitEcho(hc, "nodeB") // REST 交叉通道提交

	get := func(id string) a2aTaskView {
		t.Helper()
		_, resp := a2aCall(t, f, hc, "tasks/get", `{"id":"`+id+`"}`)
		if resp.Error != nil {
			t.Fatalf("tasks/get %s 不应报错: %+v", id, resp.Error)
		}
		return parseA2ATask(t, resp)
	}

	task := get(taskID)
	if task.Status.State != "submitted" || task.Metadata["swarmmind_status"] != "submitted" {
		t.Fatalf("submitted 映射不符: %+v", task)
	}
	f.setTaskStatus(taskID, protocol.StatusRunning)
	if task = get(taskID); task.Status.State != "working" {
		t.Fatalf("running 应映射 working: %+v", task.Status)
	}
	f.setTaskStatus(taskID, protocol.StatusCompleted)
	if task = get(taskID); task.Status.State != "completed" {
		t.Fatalf("completed 应原样映射: %+v", task.Status)
	}
	f.setTaskStatus(taskID, protocol.StatusFailed)
	if task = get(taskID); task.Status.State != "failed" {
		t.Fatalf("failed 应原样映射: %+v", task.Status)
	}
	f.makeNeedsReview(taskID)
	if task = get(taskID); task.Status.State != "input-required" ||
		task.Metadata["swarmmind_status"] != "needs_review" {
		t.Fatalf("needs_review 应映射 input-required 且保留原值: %+v", task)
	}

	// 未知任务 → -32001（A2A TaskNotFoundError）；缺 id → -32602
	_, resp := a2aCall(t, f, hc, "tasks/get", `{"id":"t-missing"}`)
	if resp.Error == nil || resp.Error.Code != rpcTaskNotFound {
		t.Fatalf("未知任务应 -32001: %+v", resp.Error)
	}
	_, resp = a2aCall(t, f, hc, "tasks/get", `{}`)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("缺 id 应 -32602: %+v", resp.Error)
	}
}

// TestA2AUnknownMethod 未知方法 → -32601；非法 JSON → -32700；缺 method → -32600。
func TestA2AUnknownMethod(t *testing.T) {
	f := newFixture(t)
	hc := f.client(true)

	code, resp := a2aCall(t, f, hc, "tasks/cancel", `{"id":"t-x"}`)
	if code != http.StatusOK || resp.Error == nil || resp.Error.Code != rpcMethodNotFound || resp.Result != nil {
		t.Fatalf("未知方法应 200+-32601: %d %+v", code, resp.Error)
	}
	_, out := f.do(hc, http.MethodPost, "/a2a/rpc", `{not json`)
	if !strings.Contains(string(out), "-32700") {
		t.Fatalf("非法 JSON 应 -32700: %s", out)
	}
	_, out = f.do(hc, http.MethodPost, "/a2a/rpc", `{"jsonrpc":"2.0","id":2}`)
	if !strings.Contains(string(out), "-32600") {
		t.Fatalf("缺 method 应 -32600: %s", out)
	}
}

// TestA2AAuthGate /a2a/rpc 走 s.auth：未登录 401（HTTP 层，非 JSON-RPC error）、
// 错误 Bearer PAT 401；发现卡保持免鉴权。
func TestA2AAuthGate(t *testing.T) {
	f := newFixture(t)
	hc := f.client(false)

	code, out := f.do(hc, http.MethodPost, "/a2a/rpc",
		`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"t-x"}}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录 RPC 应 401, got %d: %s", code, out)
	}
	// 错误 PAT：Bearer 头存在但不匹配 → 401（不回退 cookie）
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/a2a/rpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"t-x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer smsw_bogus")
	respB, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respB.Body.Close()
	if respB.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误 PAT 应 401, got %d", respB.StatusCode)
	}
	// 发现卡仍免鉴权
	if code, _ = f.do(f.client(false), http.MethodGet, "/.well-known/agent.json", ""); code != http.StatusOK {
		t.Fatalf("发现卡应保持免鉴权, got %d", code)
	}
}
