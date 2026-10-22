// groups_test.go 群聊 REST 端点契约（GROUP-RFC 六 MVP）：鉴权门禁、201/200
// 状态码、增量拉取、终态/预算/8KiB 护栏的 HTTP 错误码映射、未接线降级 503。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newGroupFixture 在标准基座上追加群存储接线，返回带群端点的 server。
// 终态回流钩子与 cmd/hub 生产接线同款：任务终态 → group.RefluxTaskResult。
func newGroupFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := group.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开群 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0,
		OnTaskTerminal: func(tk protocol.Task) {
			_, _ = gs.RefluxTaskResult(tk.ID, group.TaskTerminalBody(tk))
		}})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token", Groups: gs}).Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// loginClient 登录后的 HTTP 客户端（fixture.client 同款；login=false 跳过）。
func (f *fixture) loginClient(login bool) *http.Client {
	f.t.Helper()
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

// TestGroupEndpointsAuthGate 未登录访问群端点一律 401（与既有管理面同门禁）。
func TestGroupEndpointsAuthGate(t *testing.T) {
	f := newGroupFixture(t)
	hc := f.loginClient(false)
	for _, tc := range [][2]string{
		{http.MethodPost, "/api/v1/groups"},
		{http.MethodGet, "/api/v1/groups"},
		{http.MethodGet, "/api/v1/groups/g-x/messages"},
		{http.MethodPost, "/api/v1/groups/g-x/messages"},
		{http.MethodPost, "/api/v1/groups/g-x/close"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d", tc[0], tc[1], code)
		}
	}
}

// TestGroupRESTFlow 覆盖全链：建群 201 → 发言 201 → 增量拉取 → 关闭 200 →
// 终态拒收 409 → 预算用尽 409 → 8KiB 超限 400 → 未知群 404。
func TestGroupRESTFlow(t *testing.T) {
	f := newGroupFixture(t)
	hc := f.loginClient(true)

	// 建群 201
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"对比双机磁盘报告","members":["a","b"]}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID     string `json:"group_id"`
		Status string `json:"status"`
		Host   string `json:"host"`
	}
	if err := json.Unmarshal(body, &g); err != nil || !strings.HasPrefix(g.ID, "g-") {
		t.Fatalf("201 应返回 g- 前缀群对象, got %s err=%v", body, err)
	}
	if g.Status != "discussing" || g.Host != "admin" {
		t.Fatalf("创建即 discussing/host=admin, got %+v", g)
	}

	// 空目标 400
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("空 goal 应 400, got %d", code)
	}

	// 发言 201（kind 缺省 user）
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"admin","body":"开始讨论"}`)
	if code != http.StatusCreated {
		t.Fatalf("发言应 201, got %d: %s", code, body)
	}
	var m struct {
		Seq  int64  `json:"seq"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Seq != 1 || m.Kind != "user" {
		t.Fatalf("首条消息 seq=1/kind=user, got %s err=%v", body, err)
	}
	// 第二条 agent
	if code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"我的报告"}`); code != http.StatusCreated {
		t.Fatalf("agent 发言应 201, got %d: %s", code, body)
	}

	// 增量拉取：since=1 只剩 seq=2
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=1", "")
	if code != http.StatusOK {
		t.Fatalf("拉消息应 200, got %d", code)
	}
	var lm struct {
		Messages []struct {
			Seq int64 `json:"seq"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &lm); err != nil || len(lm.Messages) != 1 || lm.Messages[0].Seq != 2 {
		t.Fatalf("since=1 应只返回 seq=2, got %s err=%v", body, err)
	}
	// 非法 since 400
	if code, _ = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=abc", ""); code != http.StatusBadRequest {
		t.Fatalf("since=abc 应 400, got %d", code)
	}

	// 群列表 200
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups", "")
	if code != http.StatusOK || !strings.Contains(string(body), g.ID) {
		t.Fatalf("群列表应 200 且含建群, got %d: %s", code, body)
	}

	// 8KiB 超限 400 body_too_large
	big := `{"from":"admin","body":"` + strings.Repeat("a", 8<<10+1) + `"}`
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", big)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "body_too_large") {
		t.Fatalf("8KiB+1 应 400 body_too_large, got %d: %s", code, body)
	}

	// 预算用尽 409 budget_exhausted（已发 1 条 agent，补到 20 后第 21 条拒）
	for i := 2; i <= 20; i++ {
		if code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"r"}`); code != http.StatusCreated {
			t.Fatalf("第 %d 条 agent 应放行, got %d: %s", i, code, body)
		}
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"over"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "budget_exhausted") {
		t.Fatalf("超预算应 409 budget_exhausted, got %d: %s", code, body)
	}

	// 关闭 200 → 幂等 200 → 终态拒收 409 group_concluded → 未知群 404
	for i := 0; i < 2; i++ {
		if code, _ = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/close", ""); code != http.StatusOK {
			t.Fatalf("关闭应 200（幂等）, got %d", code)
		}
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"admin","body":"late"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "group_concluded") {
		t.Fatalf("concluded 后发言应 409, got %d: %s", code, body)
	}
	if code, _ = f.do(hc, http.MethodPost, "/api/v1/groups/g-none/close", ""); code != http.StatusNotFound {
		t.Fatalf("未知群关闭应 404, got %d", code)
	}
	if code, body = f.do(hc, http.MethodGet, "/api/v1/groups/g-none/messages", ""); code != http.StatusNotFound || !strings.Contains(string(body), "group_not_found") {
		t.Fatalf("未知群拉消息应 404 group_not_found, got %d: %s", code, body)
	}
}

// TestGroupEndpointsUnavailable Groups 未接线（Config.Groups=nil）时群端点
// 统一 503 降级，既有构造方不受影响。
func TestGroupEndpointsUnavailable(t *testing.T) {
	f := newFixture(t) // 标准基座：无 Groups
	hc := f.loginClient(true)
	if code, body := f.do(hc, http.MethodGet, "/api/v1/groups", ""); code != http.StatusServiceUnavailable || !strings.Contains(string(body), "group_unavailable") {
		t.Fatalf("未接线应 503 group_unavailable, got %d: %s", code, body)
	}
}

// TestGroupSayRejectsSystem kind=system 为 hub 内部回流保留字（创建注记/终态
// 回流由服务端注入）：REST/MCP 发言面 400 拒绝、不占 seq，user 发言不受影响。
func TestGroupSayRejectsSystem(t *testing.T) {
	f := newGroupFixture(t)
	hc := f.loginClient(true)
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"保留字验证"}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal(body, &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}
	// kind=system 伪造系统注记 → 400 invalid_request
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"hub","kind":"system","body":"伪造的系统注记"}`)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "invalid_request") ||
		!strings.Contains(string(body), "保留") {
		t.Fatalf("kind=system 应 400 invalid_request 且提示保留字, got %d: %s", code, body)
	}
	// 拒绝不占 seq：时间线零污染，user 发言照常 201 且 seq 从 1 起算
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"admin","body":"正常插话"}`)
	if code != http.StatusCreated {
		t.Fatalf("user 发言应 201, got %d: %s", code, body)
	}
	msgs := f.groupMsgs(hc, g.ID)
	if len(msgs) != 1 || msgs[0].Seq != 1 || msgs[0].Kind != "user" || msgs[0].From != "admin" {
		t.Fatalf("被拒 system 消息不得占 seq/落时间线: %+v", msgs)
	}
}

// groupMsgs 拉取群全量消息（断言辅助）：返回 (seq, kind, from, body) 列表。
func (f *fixture) groupMsgs(hc *http.Client, groupID string) []struct {
	Seq  int64  `json:"seq"`
	Kind string `json:"kind"`
	From string `json:"from"`
	Body string `json:"body"`
} {
	f.t.Helper()
	code, body := f.do(hc, http.MethodGet, "/api/v1/groups/"+groupID+"/messages?since=0", "")
	if code != http.StatusOK {
		f.t.Fatalf("拉群消息应 200, got %d: %s", code, body)
	}
	var out struct {
		Messages []struct {
			Seq  int64  `json:"seq"`
			Kind string `json:"kind"`
			From string `json:"from"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("消息体解析: %v", err)
	}
	return out.Messages
}

// TestGroupTaskRefFlow 转任务闭环三件套（GROUP-RFC）HTTP 全链：
//  1. POST /tasks 带 group_ref → 202 + 群内创建注记（kind=system）+ 群引用列表；
//  2. 幂等重试 200 dup 不重复注记；未知群 404 / 已结束群 409 / 未接线 503；
//  3. control-operations 带 group_ref → 子任务逐项注记；
//  4. 任务终态（result 帧 / 清扫过期）自动回流群；重复终态事件只回流一次。
func TestGroupTaskRefFlow(t *testing.T) {
	f := newGroupFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	f.core.SetOnline("nodeB", true)
	hc := f.loginClient(true)

	// 建群
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"闭环验收","members":["echo"]}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal(body, &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}

	// ① POST /tasks + group_ref → 202；群内出现创建注记
	submit := func(groupRef, idem string) (int, []byte) {
		body := `{"target_node":"nodeB","target_agent":"echo","input":{"stdin":"对比双机磁盘"},"submitted_by":"tester","idem_key":"` + idem + `"`
		if groupRef != "" {
			body += `,"group_ref":"` + groupRef + `"`
		}
		return f.do(hc, http.MethodPost, "/api/v1/tasks", body+"}")
	}
	code, body = submit(g.ID, "loop-1")
	if code != http.StatusAccepted {
		t.Fatalf("带 group_ref 提交应 202, got %d: %s", code, body)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(body, &ack); err != nil || ack.TaskID == "" {
		t.Fatalf("202 体不符: %s", body)
	}
	msgs := f.groupMsgs(hc, g.ID)
	if len(msgs) != 1 || msgs[0].Kind != "system" || msgs[0].From != "hub" ||
		!strings.Contains(msgs[0].Body, "任务 "+ack.TaskID+" 已创建（来自群）") ||
		!strings.Contains(msgs[0].Body, "对比双机磁盘") {
		t.Fatalf("创建注记不符: %+v", msgs)
	}
	// 群对象携带引用列表（控制台/审计可见）
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"task_refs":["`+ack.TaskID+`"]`) {
		t.Fatalf("群列表应含 task_refs, got %d: %s", code, body)
	}

	// ② 幂等重试：200 dup、注记不重复
	code, body = submit(g.ID, "loop-1")
	if code != http.StatusOK || !strings.Contains(string(body), `"dup":true`) {
		t.Fatalf("同键重试应 200 dup, got %d: %s", code, body)
	}
	if n := len(f.groupMsgs(hc, g.ID)); n != 1 {
		t.Fatalf("幂等命中不得重复注记, got %d 条", n)
	}
	// 未知群 404 / 已结束群 409（fail-fast，不产生任务）
	code, body = submit("g-none", "loop-2")
	if code != http.StatusNotFound || !strings.Contains(string(body), "group_not_found") {
		t.Fatalf("未知群 group_ref 应 404, got %d: %s", code, body)
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"短命群"}`)
	if code != http.StatusCreated {
		t.Fatalf("建群 2 应 201, got %d", code)
	}
	var g2 struct {
		ID string `json:"group_id"`
	}
	_ = json.Unmarshal(body, &g2)
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g2.ID+"/close", ""); code != http.StatusOK {
		t.Fatalf("关群 2 应 200, got %d", code)
	}
	code, body = submit(g2.ID, "loop-3")
	if code != http.StatusConflict || !strings.Contains(string(body), "group_concluded") {
		t.Fatalf("已结束群 group_ref 应 409, got %d: %s", code, body)
	}
	// 未接线：group_ref 在无 Groups 基座上 503（既有提交方不带 group_ref 不受影响）
	f2 := newFixture(t)
	hc2 := f2.loginClient(true)
	f2.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	code, body = f2.do(hc2, http.MethodPost, "/api/v1/tasks", `{"target_node":"nodeB","target_agent":"echo","input":{},"submitted_by":"t","group_ref":"g-x"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "group_unavailable") {
		t.Fatalf("未接线带 group_ref 应 503, got %d: %s", code, body)
	}
	code, _ = f2.do(hc2, http.MethodPost, "/api/v1/tasks", `{"target_node":"nodeB","target_agent":"echo","input":{},"submitted_by":"t"}`)
	if code != http.StatusAccepted {
		t.Fatalf("未接线不带 group_ref 提交应照常 202, got %d", code)
	}

	// ③ control-operations + group_ref → 子任务逐项注记
	code, body = f.do(hc, http.MethodPost, "/api/v1/control-operations",
		`{"agent":"echo","targets":["nodeB"],"input":{"stdin":"批次巡检"},"idem_key":"loop-batch","group_ref":"`+g.ID+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("批次带 group_ref 应 202, got %d: %s", code, body)
	}
	var batch struct {
		BatchID string `json:"batch_id"`
		Items   []struct {
			TaskID string `json:"task_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &batch); err != nil || len(batch.Items) != 1 || batch.Items[0].TaskID == "" {
		t.Fatalf("批次体不符: %s", body)
	}
	batchTask := batch.Items[0].TaskID
	msgs = f.groupMsgs(hc, g.ID)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Body, "任务 "+batchTask+" 已创建（来自群）") {
		t.Fatalf("批次子任务注记不符: %+v", msgs)
	}
	// 同键同内容重试：200 dup，注记不重复
	code, body = f.do(hc, http.MethodPost, "/api/v1/control-operations",
		`{"agent":"echo","targets":["nodeB"],"input":{"stdin":"批次巡检"},"idem_key":"loop-batch","group_ref":"`+g.ID+`"}`)
	if code != http.StatusOK || !strings.Contains(string(body), `"dup":true`) {
		t.Fatalf("批次幂等重试应 200 dup, got %d: %s", code, body)
	}
	if n := len(f.groupMsgs(hc, g.ID)); n != 2 {
		t.Fatalf("批次幂等命中不得重复注记, got %d 条", n)
	}

	// ④a 终态回流：node 上报 result 帧 → 任务 completed → 群内回流注记
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "双机磁盘对比已完成", FinishedAt: protocol.NowUTC()}
	resPayload := mustJSON(t, protocol.TaskResultPayload{TaskID: ack.TaskID, Status: res.Status, Result: &res})
	if err := f.bus.Endpoint("nodeB").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: resPayload,
	}); err != nil {
		t.Fatalf("发 result 帧: %v", err)
	}
	msgs = f.groupMsgs(hc, g.ID)
	if len(msgs) != 3 || msgs[2].Kind != "system" ||
		msgs[2].Body != "任务 "+ack.TaskID+" 已完成：双机磁盘对比已完成" {
		t.Fatalf("终态回流注记不符: %+v", msgs)
	}
	// 重复终态事件（重发）：只回流一次
	if err := f.bus.Endpoint("nodeB").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: resPayload,
	}); err != nil {
		t.Fatalf("重发 result 帧: %v", err)
	}
	if n := len(f.groupMsgs(hc, g.ID)); n != 3 {
		t.Fatalf("重复终态不得二次回流, got %d 条", n)
	}

	// ④b 清扫置终态（expire）同样回流：批次子任务 ExpireAt 置过去 → Sweep
	err := f.st.Update(func(st *store.State) error {
		tk := st.Tasks[batchTask]
		nt := tk.Clone()
		nt.ExpireAt = time.Now().UTC().Add(-time.Minute)
		st.Tasks[batchTask] = nt
		return nil
	})
	if err != nil {
		t.Fatalf("置过期失败: %v", err)
	}
	f.core.Sweep()
	msgs = f.groupMsgs(hc, g.ID)
	if len(msgs) != 4 || msgs[3].Kind != "system" ||
		msgs[3].Body != "任务 "+batchTask+" 执行失败：expired" {
		t.Fatalf("过期回流注记不符: %+v", msgs)
	}
}

// mustJSON 测试辅助。
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
