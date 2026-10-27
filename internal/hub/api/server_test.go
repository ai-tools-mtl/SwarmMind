package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// fixture 管理面测试基座：store + membus + core + HTTP server（登录态客户端）。
type fixture struct {
	t    *testing.T
	bus  *transport.Bus
	st   *store.Store
	core *core.Core
	srv  *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token"}).Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// client 登录后的 HTTP 客户端（cookie 自动携带）；login=false 跳过登录（401 路径）。
func (f *fixture) client(login bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	if login {
		resp, err := hc.Post(f.srv.URL+"/api/v1/login", "application/json",
			strings.NewReader(`{"token":"test-admin-token"}`))
		if err != nil {
			f.t.Fatalf("登录失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			f.t.Fatalf("登录应 200, got %d", resp.StatusCode)
		}
	}
	return hc
}

// do 执行请求并返回状态码与响应体。
func (f *fixture) do(hc *http.Client, method, path, body string) (int, []byte) {
	f.t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		f.t.Fatalf("构造请求失败: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := &bytes.Buffer{}
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func TestLoginAndAuthGate(t *testing.T) {
	f := newFixture(t)

	// 错误令牌 401
	hc := f.client(false)
	code, _ := f.do(hc, http.MethodPost, "/api/v1/login", `{"token":"wrong"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401, got %d", code)
	}
	// 未登录访问端点一律 401（含 P3 新增端点）
	for _, tc := range [][2]string{
		{http.MethodGet, "/api/v1/tasks/t-x"},
		{http.MethodGet, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodGet, "/api/v1/directory"},
		{http.MethodPost, "/api/v1/tasks/t-x/review"},
		{http.MethodPost, "/api/v1/control-operations"},
		{http.MethodGet, "/api/v1/control-operations/b-x"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], ""); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d", tc[0], tc[1], code)
		}
	}
	// 正确令牌 → Set-Cookie
	resp, err := hc.Post(f.srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"test-admin-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(resp.Cookies()) == 0 || resp.Cookies()[0].Name != sessionCookie || resp.Cookies()[0].Value == "" {
		t.Fatalf("登录应发会话 cookie: %+v", resp.Cookies())
	}
}

func TestSubmitAndQueryChain(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}})
	hc := f.client(true)

	body := `{"target_node":"nodeB","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester","idem_key":"k-1"}`
	code, out := f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusAccepted {
		t.Fatalf("提交应 202, got %d: %s", code, out)
	}
	var ack struct {
		TaskID  string `json:"task_id"`
		TraceID string `json:"trace_id"`
		Dup     *bool  `json:"dup"`
	}
	if err := json.Unmarshal(out, &ack); err != nil || ack.TaskID == "" || ack.TraceID == "" {
		t.Fatalf("202 体不符: %s", out)
	}

	// 幂等命中：200 + dup
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusOK {
		t.Fatalf("幂等命中应 200, got %d: %s", code, out)
	}
	var dup struct {
		TaskID string `json:"task_id"`
		Dup    bool   `json:"dup"`
	}
	_ = json.Unmarshal(out, &dup)
	if !dup.Dup || dup.TaskID != ack.TaskID {
		t.Fatalf("幂等命中不符: %s", out)
	}

	// 同键不同内容 → 409
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","input":{"args":["DIFFERENT"]},"submitted_by":"tester","idem_key":"k-1"}`)
	if code != http.StatusConflict {
		t.Fatalf("同键不同内容应 409, got %d: %s", code, out)
	}

	// 目标不存在 → 404 agent_not_exposed
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"ghost","target_agent":"echo","submitted_by":"t"}`)
	if code != http.StatusNotFound || !strings.Contains(string(out), "agent_not_exposed") {
		t.Fatalf("目标不存在应 404 agent_not_exposed, got %d: %s", code, out)
	}

	// 字段缺失 → 400
	code, _ = f.do(hc, http.MethodPost, "/api/v1/tasks", `{"target_agent":"echo"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("字段缺失应 400, got %d", code)
	}

	// GET task：200 / 404
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID, "")
	if code != http.StatusOK {
		t.Fatalf("查询应 200, got %d", code)
	}
	var task protocol.Task
	if err := json.Unmarshal(out, &task); err != nil || task.ID != ack.TaskID || task.Status != protocol.StatusSubmitted {
		t.Fatalf("Task 不符: %s", out)
	}
	if code, _ = f.do(hc, http.MethodGet, "/api/v1/tasks/t-missing", ""); code != http.StatusNotFound {
		t.Fatalf("查无任务应 404, got %d", code)
	}
}

func TestAgentsNodesCatalog(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
		Description: "回声",
	}})
	hc := f.client(true)

	code, out := f.do(hc, http.MethodGet, "/api/v1/agents", "")
	if code != http.StatusOK {
		t.Fatalf("agents 应 200, got %d", code)
	}
	// card 绝不含 ExecProfile 字段（SPEC 六）：响应体不得出现执行配置键名
	for _, leak := range []string{`"workdir"`, `"command"`, `"agent_args"`, `"allow_env"`, `"timeout_sec"`, `"config_home"`} {
		if strings.Contains(string(out), leak) {
			t.Fatalf("agents 响应泄漏 ExecProfile 字段 %s: %s", leak, out)
		}
	}
	var agents struct {
		Agents []struct {
			Card   protocol.AgentCard `json:"card"`
			NodeID string             `json:"node_id"`
			Online bool               `json:"online"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(out, &agents); err != nil || len(agents.Agents) != 1 {
		t.Fatalf("agents 目录不符: %s", out)
	}
	if agents.Agents[0].Card.Name != "echo" || agents.Agents[0].NodeID != "nodeB" || agents.Agents[0].Online {
		t.Fatalf("目录条目不符: %+v", agents.Agents[0])
	}

	code, out = f.do(hc, http.MethodGet, "/api/v1/nodes", "")
	if code != http.StatusOK {
		t.Fatalf("nodes 应 200, got %d", code)
	}
	var nodes struct {
		Nodes []struct {
			NodeID   string `json:"node_id"`
			Online   bool   `json:"online"`
			Epoch    int64  `json:"epoch"`
			Inflight int    `json:"inflight"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(out, &nodes); err != nil || len(nodes.Nodes) != 1 {
		t.Fatalf("nodes 目录不符: %s", out)
	}
	if nodes.Nodes[0].NodeID != "nodeB" || nodes.Nodes[0].Epoch != 0 || nodes.Nodes[0].Inflight != 0 {
		t.Fatalf("节点条目不符: %+v", nodes.Nodes[0])
	}
}

// TestArtifactEndpointTimeout 产物端点 504 路径：node 可达但不应答。
// （完整中继链路单测在 hub/core；此处只验 HTTP 映射，超时以短周期注入。）
func TestArtifactEndpointErrors(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}})
	ack, err := f.core.Submit(protocol.TaskSubmitRequest{
		TargetNode: "nodeB", TargetAgent: "echo", SubmittedBy: "t",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = ack
	hc := f.client(true)

	// 非终态任务（无结果）→ 404 artifact_not_found
	code, out := f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID+"/artifacts/0", "")
	if code != http.StatusNotFound || !strings.Contains(string(out), protocol.CodeArtifactNotFound) {
		t.Fatalf("无产物应 404 artifact_not_found, got %d: %s", code, out)
	}
	// 任务不存在 → 404 task_not_found
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/t-missing/artifacts/0", "")
	if code != http.StatusNotFound || !strings.Contains(string(out), protocol.CodeTaskNotFound) {
		t.Fatalf("任务不存在应 404 task_not_found, got %d: %s", code, out)
	}
	// 下标非法 → 400
	code, _ = f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID+"/artifacts/xyz", "")
	if code != http.StatusBadRequest {
		t.Fatalf("下标非法应 400, got %d", code)
	}
}

// echoCard 构造 exposed 可选的 echo AgentCard（测试目录/批次/提交用）。
func echoCard(exposed bool) protocol.AgentCard {
	return protocol.AgentCard{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1,
		Exposed: exposed,
	}
}

// makeNeedsReview 经 store 事务把任务置为 needs_review（核对端点的前置状态；
// needs_review 的产生路径——node 中途失联——已在 core/账本测试覆盖，此处只造状态）。
func (f *fixture) makeNeedsReview(taskID string) {
	f.t.Helper()
	err := f.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[taskID]
		if !ok {
			return errors.New("task missing: " + taskID)
		}
		nt := t.Clone()
		nt.Status = protocol.StatusNeedsReview
		st.Tasks[taskID] = nt
		return nil
	})
	if err != nil {
		f.t.Fatalf("置 needs_review 失败: %v", err)
	}
}

// submitEcho 登录态经 HTTP 提交一个 echo 任务，返回 task_id。
func (f *fixture) submitEcho(hc *http.Client, node string) string {
	f.t.Helper()
	body := `{"target_node":"` + node + `","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester"}`
	code, out := f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusAccepted {
		f.t.Fatalf("提交应 202, got %d: %s", code, out)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(out, &ack); err != nil || ack.TaskID == "" {
		f.t.Fatalf("202 体不符: %s", out)
	}
	return ack.TaskID
}

// TestReviewRecord 核对记录（SPEC 六.五）：仅 needs_review 生效、写快照、
// 不改协议终态、GET 返回含 review、重复提交覆盖旧记录。
func TestReviewRecord(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)
	taskID := f.submitEcho(hc, "nodeB")

	// 非 needs_review 任务 → 409 not_needs_review
	code, out := f.do(hc, http.MethodPost, "/api/v1/tasks/"+taskID+"/review", `{"action":"ack_review","note":"早了"}`)
	if code != http.StatusConflict || !strings.Contains(string(out), "not_needs_review") {
		t.Fatalf("非 needs_review 应 409 not_needs_review, got %d: %s", code, out)
	}
	f.makeNeedsReview(taskID)

	// 非法动作 → 400
	code, _ = f.do(hc, http.MethodPost, "/api/v1/tasks/"+taskID+"/review", `{"action":"requeue","note":""}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法动作应 400, got %d", code)
	}
	// 任务不存在 → 404
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks/t-missing/review", `{"action":"ack_review"}`)
	if code != http.StatusNotFound || !strings.Contains(string(out), protocol.CodeTaskNotFound) {
		t.Fatalf("未知任务应 404 task_not_found, got %d: %s", code, out)
	}

	// 合法登记 → 200，协议终态保持 needs_review，by 固定 admin
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks/"+taskID+"/review", `{"action":"ack_review","note":"已人工确认"}`)
	if code != http.StatusOK {
		t.Fatalf("核对登记应 200, got %d: %s", code, out)
	}
	var task protocol.Task
	if err := json.Unmarshal(out, &task); err != nil {
		t.Fatalf("200 体应为 Task: %s", out)
	}
	if task.Status != protocol.StatusNeedsReview {
		t.Fatalf("核对不得改协议终态, got %s", task.Status)
	}
	if task.Review == nil || task.Review.By != "admin" || task.Review.Action != protocol.ReviewAckReview {
		t.Fatalf("review 记录不符: %+v", task.Review)
	}

	// GET /tasks/{id} 返回含 review
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+taskID, "")
	if code != http.StatusOK {
		t.Fatalf("查询应 200, got %d", code)
	}
	if err := json.Unmarshal(out, &task); err != nil || task.Review == nil ||
		task.Review.Note != "已人工确认" || task.Review.At.IsZero() {
		t.Fatalf("GET 应含 review 记录: %s", out)
	}

	// 重复提交覆盖旧记录
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks/"+taskID+"/review", `{"action":"mark_failed","note":"判定失败"}`)
	if code != http.StatusOK {
		t.Fatalf("重复登记应 200, got %d: %s", code, out)
	}
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+taskID, "")
	if err := json.Unmarshal(out, &task); err != nil || task.Review == nil ||
		task.Review.Action != protocol.ReviewMarkFailed || task.Review.Note != "判定失败" {
		t.Fatalf("重复登记应覆盖旧记录: %s", out)
	}
}

// TestControlBatchPartial 控制批次（SPEC 六.五）：在线已暴露目标走 Submit 全链，
// 离线/未暴露目标标 skipped 不阻塞其余；批次入快照可查；submitted_by 固定 admin。
func TestControlBatchPartial(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	f.core.SetOnline("nodeB", true)
	f.core.RegisterNode("nodeC", []protocol.AgentCard{echoCard(true)})  // 已注册但不在线
	f.core.RegisterNode("nodeE", []protocol.AgentCard{echoCard(false)}) // 在线但未暴露
	f.core.SetOnline("nodeE", true)
	// ghost 未注册
	hc := f.client(true)

	body := `{"agent":"echo","targets":["nodeB","nodeC","nodeE","ghost"],"input":{"args":["patrol"]}}`
	code, out := f.do(hc, http.MethodPost, "/api/v1/control-operations", body)
	if code != http.StatusAccepted {
		t.Fatalf("批次应 202, got %d: %s", code, out)
	}
	var batch struct {
		BatchID string `json:"batch_id"`
		Items   []struct {
			Target  string `json:"target"`
			TaskID  string `json:"task_id"`
			Skipped string `json:"skipped"`
			Status  string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &batch); err != nil || !strings.HasPrefix(batch.BatchID, "b-") || len(batch.Items) != 4 {
		t.Fatalf("202 体不符: %s", out)
	}
	// 顺序与请求 targets 一致，逐项准入符合预期
	want := []struct{ target, taskID, skipped string }{
		{"nodeB", "set", ""}, {"nodeC", "", "offline"}, {"nodeE", "", "not_exposed"}, {"ghost", "", "offline"},
	}
	for i, w := range want {
		it := batch.Items[i]
		if it.Target != w.target {
			t.Fatalf("items[%d] 顺序不符: %+v", i, it)
		}
		if w.taskID == "set" && it.TaskID == "" {
			t.Fatalf("nodeB 应提交成功: %+v", it)
		}
		if w.taskID == "" && it.TaskID != "" {
			t.Fatalf("%s 不应有 task_id: %+v", w.target, it)
		}
		if it.Skipped != w.skipped {
			t.Fatalf("%s skipped 应 %q, got %q", w.target, w.skipped, it.Skipped)
		}
	}
	taskB := batch.Items[0].TaskID

	// 提交身份：控制台批次任务 submitted_by 固定 admin
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+taskB, "")
	var task protocol.Task
	if code != http.StatusOK || json.Unmarshal(out, &task) != nil || task.SubmittedBy != "admin" {
		t.Fatalf("批次任务 submitted_by 应为 admin: %d %s", code, out)
	}

	// 批次入快照：GET 查询逐项状态（nodeB 项合并任务状态）
	code, out = f.do(hc, http.MethodGet, "/api/v1/control-operations/"+batch.BatchID, "")
	if code != http.StatusOK {
		t.Fatalf("批次查询应 200, got %d: %s", code, out)
	}
	var got struct {
		BatchID string `json:"batch_id"`
		Agent   string `json:"agent"`
		Items   []struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &got); err != nil || got.BatchID != batch.BatchID || got.Agent != "echo" ||
		len(got.Items) != 4 || got.Items[0].TaskID != taskB || got.Items[0].Status != string(protocol.StatusSubmitted) {
		t.Fatalf("批次快照查询不符: %s", out)
	}

	// 未知批次 → 404
	code, out = f.do(hc, http.MethodGet, "/api/v1/control-operations/b-missing", "")
	if code != http.StatusNotFound || !strings.Contains(string(out), "batch_not_found") {
		t.Fatalf("未知批次应 404 batch_not_found, got %d: %s", code, out)
	}
	// 空目标 → 400
	code, _ = f.do(hc, http.MethodPost, "/api/v1/control-operations", `{"agent":"echo","targets":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("空目标应 400, got %d", code)
	}
}

// TestSubmitSubmittedByOverride 会话身份覆盖（修复项 1）：控制台会话提交的
// submitted_by 一律强制 admin，请求体自报值被忽略（SPEC 六.五 提交身份）。
func TestSubmitSubmittedByOverride(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)

	code, out := f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"spoof","idem_key":"ov-1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("提交应 202, got %d: %s", code, out)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(out, &ack); err != nil || ack.TaskID == "" {
		t.Fatalf("202 体不符: %s", out)
	}
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID, "")
	var task protocol.Task
	if code != http.StatusOK || json.Unmarshal(out, &task) != nil {
		t.Fatalf("查询应 200, got %d: %s", code, out)
	}
	if task.SubmittedBy != "admin" {
		t.Fatalf("submitted_by 应被会话覆盖为 admin, got %q", task.SubmittedBy)
	}

	// 幂等重试不受自报身份干扰：覆盖后内容一致才命中 dup
	body := `{"target_node":"nodeB","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"other","idem_key":"ov-1"}`
	if code, out = f.do(hc, http.MethodPost, "/api/v1/tasks", body); code != http.StatusOK {
		t.Fatalf("同内容幂等重试应 200, got %d: %s", code, out)
	}
	var dup struct {
		TaskID string `json:"task_id"`
		Dup    bool   `json:"dup"`
	}
	if err := json.Unmarshal(out, &dup); err != nil || !dup.Dup || dup.TaskID != ack.TaskID {
		t.Fatalf("幂等命中不符: %s", out)
	}
}

// TestControlBatchIdempotencyHTTP 批次幂等（修复项 2，HTTP 面）：同键同内容
// 200 + dup + 原 batch_id（不重复提交子任务）；同键不同内容 409；GET 返回
// status；批次任务 submitted_by=admin。
func TestControlBatchIdempotencyHTTP(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	f.core.SetOnline("nodeB", true)
	f.core.RegisterNode("nodeC", []protocol.AgentCard{echoCard(true)}) // 离线
	hc := f.client(true)

	const body = `{"agent":"echo","targets":["nodeB","nodeC"],"input":{"args":["patrol"]},"idem_key":"op-001"}`
	code, out := f.do(hc, http.MethodPost, "/api/v1/control-operations", body)
	if code != http.StatusAccepted {
		t.Fatalf("批次应 202, got %d: %s", code, out)
	}
	var batch struct {
		BatchID string `json:"batch_id"`
		Status  string `json:"status"`
		Dup     bool   `json:"dup"`
		Items   []struct {
			Target string `json:"target"`
			TaskID string `json:"task_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &batch); err != nil || batch.Dup || batch.Status != "complete" ||
		len(batch.Items) != 2 || batch.Items[0].TaskID == "" {
		t.Fatalf("202 体不符: %s", out)
	}

	// 同键同内容：200 + dup + 原 batch_id，任务数不变
	tasksBefore := len(f.st.View().Tasks)
	code, out = f.do(hc, http.MethodPost, "/api/v1/control-operations", body)
	if code != http.StatusOK {
		t.Fatalf("幂等命中应 200, got %d: %s", code, out)
	}
	var dupBatch struct {
		BatchID string `json:"batch_id"`
		Dup     bool   `json:"dup"`
	}
	if err := json.Unmarshal(out, &dupBatch); err != nil || !dupBatch.Dup || dupBatch.BatchID != batch.BatchID {
		t.Fatalf("幂等命中不符: %s", out)
	}
	if n := len(f.st.View().Tasks); n != tasksBefore {
		t.Fatalf("幂等命中不得重复提交子任务: before=%d after=%d", tasksBefore, n)
	}

	// 同键不同内容：409 conflict
	code, out = f.do(hc, http.MethodPost, "/api/v1/control-operations",
		`{"agent":"echo","targets":["nodeB"],"input":{"args":["patrol"]},"idem_key":"op-001"}`)
	if code != http.StatusConflict || !strings.Contains(string(out), "conflict") {
		t.Fatalf("同键不同内容应 409, got %d: %s", code, out)
	}

	// GET 返回 status 与逐项任务状态
	code, out = f.do(hc, http.MethodGet, "/api/v1/control-operations/"+batch.BatchID, "")
	if code != http.StatusOK {
		t.Fatalf("批次查询应 200, got %d: %s", code, out)
	}
	var got struct {
		Status string `json:"status"`
		Items  []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &got); err != nil || got.Status != "complete" ||
		len(got.Items) != 2 || got.Items[0].Status != string(protocol.StatusSubmitted) {
		t.Fatalf("批次查询不符: %s", out)
	}

	// 批次子任务提交身份 admin
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+batch.Items[0].TaskID, "")
	var task protocol.Task
	if code != http.StatusOK || json.Unmarshal(out, &task) != nil || task.SubmittedBy != "admin" {
		t.Fatalf("批次子任务 submitted_by 应 admin: %d %s", code, out)
	}
	if task.IdemKey == "" {
		t.Fatal("批次子任务应携带派生幂等键")
	}
}

// TestDirectory 发现目录（SPEC 六.五）：仅 exposed=true 条目，绝不包含
// ExecProfile 字段；在线状态随条目返回。
func TestDirectory(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true), {
		Name: "admin-tools", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1,
		Exposed: false, // 审计登记：不入目录
	}})
	f.core.SetOnline("nodeB", true)
	f.core.RegisterNode("nodeC", []protocol.AgentCard{echoCard(true)}) // 离线
	hc := f.client(true)

	code, out := f.do(hc, http.MethodGet, "/api/v1/directory", "")
	if code != http.StatusOK {
		t.Fatalf("directory 应 200, got %d", code)
	}
	// 不含 ExecProfile 字段（SPEC 六/六.五：执行配置不出本机）。allow_args/stdin_to_arg
	// 自 maturity-dispatch-args-ux 起为能力卡输入语义声明（SPEC 4.9，同 Exposed/
	// PermissionMode 一类的卡上布尔声明），不属泄漏；其余执行配置键仍禁。
	for _, leak := range []string{`"workdir"`, `"command"`, `"agent_args"`, `"allow_env"`, `"timeout_sec"`, `"config_home"`} {
		if strings.Contains(string(out), leak) {
			t.Fatalf("directory 响应泄漏 ExecProfile 字段 %s: %s", leak, out)
		}
	}
	// 输入语义声明键必须在场且为布尔（目录消费方据 Win 呈现传参面，maturity-dispatch-args-ux）。
	for _, key := range []string{`"allow_args":false`, `"stdin_to_arg":false`} {
		if !strings.Contains(string(out), key) {
			t.Fatalf("directory 能力卡缺输入语义声明 %s: %s", key, out)
		}
	}
	var dir struct {
		Directory []struct {
			Card   protocol.AgentCard `json:"card"`
			NodeID string             `json:"node_id"`
			Online bool               `json:"online"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(out, &dir); err != nil || len(dir.Directory) != 2 {
		t.Fatalf("directory 应只含 2 条 exposed=true: %s", out)
	}
	seen := map[string]bool{}
	for _, e := range dir.Directory {
		if !e.Card.Exposed {
			t.Fatalf("exposed=false 条目不得入目录: %+v", e)
		}
		seen[e.NodeID] = true
		if e.Card.Name != "echo" {
			t.Fatalf("目录条目不符: %+v", e)
		}
	}
	if !seen["nodeB"] || !seen["nodeC"] {
		t.Fatalf("目录缺节点: %+v", seen)
	}
	// 在线状态按节点区分
	for _, e := range dir.Directory {
		if want := e.NodeID == "nodeB"; e.Online != want {
			t.Fatalf("%s online 应 %v, got %v", e.NodeID, want, e.Online)
		}
	}
}
