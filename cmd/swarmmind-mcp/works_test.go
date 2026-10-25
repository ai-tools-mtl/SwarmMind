// works_test.go 工作单工具 MCP 层实测（SPEC 六.六 同步开发规则③）：真实 api
// server 全链（MCP 工具函数 → HTTP → core.CreateWork），覆盖建单幂等/读档/
// 列表/事件游标与 hub-only 装配（node 模式不设，不静默降级）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// TestWorkToolsHubOnly 工具面装配：hub 模式含三工作单工具；node 模式不含
// （RFC 三：hub-only 工具 node 模式明确不可用，不静默降级）。
func TestWorkToolsHubOnly(t *testing.T) {
	want := map[string]bool{"swarmmind_work_create": false, "swarmmind_work_get": false, "swarmmind_work_list": false}
	for _, td := range tools() {
		if _, ok := want[td.Name]; ok {
			want[td.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("hub 模式工具清单缺 %s", name)
		}
	}
	for _, td := range (nodeBackend{}).tools() {
		if _, ok := want[td.Name]; ok {
			t.Fatalf("node 模式不应装配 %s（hub-only 不静默降级）", td.Name)
		}
	}
}

// TestWorkToolsFullFlow 全链：建单 → 幂等重建返同单 → 列表 → 读档（全量/游标）→
// 校验失败人话。
func TestWorkToolsFullFlow(t *testing.T) {
	realHub(t)

	steps := []any{
		map[string]any{"desc": "盘点磁盘", "target_node": "mac-mini", "target_agent": "diskcheck"},
		map[string]any{"desc": "汇总报告", "target_node": "mac-mini", "target_agent": "reporter", "depends_on": []any{float64(1)}},
	}
	out := mustCall(t, "swarmmind_work_create", map[string]any{
		"goal": "对比双机磁盘", "steps": steps, "prime_term": "prime-3",
	})
	if !strings.Contains(out, "工作单已创建") || !strings.Contains(out, "w-") {
		t.Fatalf("建单输出异常: %s", out)
	}
	wid := out[strings.Index(out, "w-"):]
	if i := strings.IndexAny(wid, "\n"); i >= 0 {
		wid = wid[:i]
	}
	wid = strings.TrimSpace(wid)

	// 幂等重建：返同单、明示未重复建单
	out = mustCall(t, "swarmmind_work_create", map[string]any{"goal": "对比双机磁盘", "steps": steps})
	if !strings.Contains(out, wid) || !strings.Contains(out, "幂等命中") {
		t.Fatalf("重建应返同单 %s: %s", wid, out)
	}

	// 列表
	out = mustCall(t, "swarmmind_work_list", map[string]any{})
	if !strings.Contains(out, wid) || !strings.Contains(out, "计划中") || !strings.Contains(out, "2 步") {
		t.Fatalf("列表应含工作单与状态: %s", out)
	}

	// 读档：全量（1 条建单事件）。步骤输出含 depends_on（与 REST 对齐，codex
	// 审查必须修 1）；事件含服务端验证主体（真实铸的 PAT 名 mcp-test，RFC 六.2
	// 与自报 by=mcp 分开陈述，codex 审查必须修 2 的 MCP 展示面）
	out = mustCall(t, "swarmmind_work_get", map[string]any{"work_id": wid})
	for _, want := range []string{"对比双机磁盘", "计划中", "prime-3", "#1 盘点磁盘", "#2 汇总报告", "（依赖 #1）", "[#1] work_created", "（mcp｜主体 pat:mcp-test）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("读档输出缺 %q: %s", want, out)
		}
	}
	// 游标增量：since_events=1 → 无新事件
	out = mustCall(t, "swarmmind_work_get", map[string]any{"work_id": wid, "since_events": float64(1)})
	if !strings.Contains(out, "没有新事件") {
		t.Fatalf("游标后应无新事件: %s", out)
	}

	// 未知工作单：人话指引
	res, rpcErr := callTool("swarmmind_work_get", map[string]any{"work_id": "w-missing"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, "swarmmind_work_list") {
		t.Fatalf("未知单应人话报错: %v %v", rpcErr, res)
	}

	// 步骤校验失败：人话（invalid_work_steps 定位文案）——自引用在 MCP 依赖
	// 校验看来是合法整数且界内，放行到 hub 由 hub 终检拒绝（MCP 收紧后保留
	// hub 路径覆盖的变体）
	res, rpcErr = callTool("swarmmind_work_create", map[string]any{
		"goal": "自引用 hub 终检",
		"steps": []any{
			map[string]any{"desc": "a", "target_node": "n", "target_agent": "x", "depends_on": []any{float64(1)}},
		},
	})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, "步骤计划不合法") {
		t.Fatalf("自引用应 hub 人话报错: %v %v", rpcErr, res)
	}

	// 依赖严格校验拒绝矩阵（codex 复审必须修回归）：非整数不截断（[1.9] 不得
	// 变 [1]）、字符串/裸数字不丢弃（"1" 不得静默没了）、越界拒绝——全部返回
	// 工具错误且不发送建单请求（跑完下方断言 hub 零建单）
	rejects := []struct {
		name string
		deps any
		want string
	}{
		{"非整数", []any{1.9}, "非整数序号 1.9"},
		{"字符串依赖", "1", "收到的是字符串"},
		{"裸数字依赖", float64(1), "收到的是数字"},
		{"下界越界", []any{float64(0)}, "越界"},
		{"上界越界", []any{float64(3)}, "越界"},
		{"字符串元素", []any{"1"}, "含非数字元素"},
		{"布尔元素", []any{true}, "含非数字元素"},
	}
	for _, tc := range rejects {
		res, rpcErr := callTool("swarmmind_work_create", map[string]any{
			"goal": "依赖校验拒绝·" + tc.name,
			"steps": []any{
				map[string]any{"desc": "a", "target_node": "n", "target_agent": "x"},
				map[string]any{"desc": "b", "target_node": "n", "target_agent": "x", "depends_on": tc.deps},
			},
		})
		if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, tc.want) {
			t.Fatalf("%s 应拒绝且报 %q: rpcErr=%v res=%v", tc.name, tc.want, rpcErr, res)
		}
	}

	// 被拒变体（MCP 七变体 + hub 自引用）之后列表不得出现任何对应工作单
	// （校验失败不得建单；前文「对比双机磁盘」为放行路径所建，不受影响）
	out = mustCall(t, "swarmmind_work_list", map[string]any{})
	for _, leaked := range []string{"依赖校验拒绝", "自引用 hub 终检"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("被拒变体不得建单，列表出现 %q: %s", leaked, out)
		}
	}

	// 空数组/null = 未声明依赖，不误拒（null 与 hub REST nil 切片语义一致）
	for i, dv := range []any{[]any{}, nil} {
		out = mustCall(t, "swarmmind_work_create", map[string]any{
			"goal": fmt.Sprintf("空依赖形态 %d", i),
			"steps": []any{
				map[string]any{"desc": "a", "target_node": "n", "target_agent": "x", "depends_on": dv},
			},
		})
		if !strings.Contains(out, "工作单已创建") {
			t.Fatalf("空依赖形态 %v 应放行: %s", dv, out)
		}
	}
}

// realHubCore 真实 hub 全链基座（暴露 core 供注册 node；其余同 realHub）。
func realHubCore(t *testing.T) *core.Core {
	t.Helper()
	c, _ := realHubCoreBus(t)
	return c
}

// realHubCoreBus 同 realHubCore，另回传 bus（W3 收集全链：以 node 端点身份回传
// 终态结果帧造步骤终态）。
func realHubCoreBus(t *testing.T) (*core.Core, *transport.Bus) {
	t.Helper()
	realHub(t)
	// realHub 不回传 core：重建一份同配置核心不可行（store 已被占用），这里
	// 直接经 core 包重开不可取——改为在 realHub 的 server 之外另开 store 会
	// 指向不同 hub。故本基座独立搭建（拷贝 realHub 形态并回传 core）。
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	dataDir := t.TempDir()
	srv := httptest.NewServer(api.New(api.Config{Core: c, AdminToken: "tok", DataDir: dataDir}).Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok"}`))
	if err != nil {
		t.Fatalf("登录: %v", err)
	}
	resp.Body.Close()
	resp, err = cl.Post(srv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"mcp-test"}`))
	if err != nil {
		t.Fatalf("铸 PAT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("铸 PAT 应 201, got %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("PAT 响应解析失败: %v", err)
	}
	hubURL, hubToken = srv.URL, out.Token
	return c, bus
}

// TestWorkToolsW2BindingAndTakeover W2 六.六 同批（规则 11）：接管工具装配
// （hub-only）、真实全链（建单→接管→绑定派发→读档见尝试）、冲突人话、
// step_seq 严格校验（请求不发出）、node 模式绑定参数显式拒绝。
func TestWorkToolsW2BindingAndTakeover(t *testing.T) {
	c := realHubCore(t)
	// 注册目标 node（diskcheck exposed）
	cards := []protocol.AgentCard{{Name: "diskcheck", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true}}
	if err := c.RegisterNode("mac-mini", cards); err != nil {
		t.Fatalf("注册 node: %v", err)
	}
	c.SetOnline("mac-mini", true)

	// 工具面：hub 含 work_takeover；node 模式不含（hub-only 同批装配）
	found := false
	for _, td := range tools() {
		if td.Name == "swarmmind_work_takeover" {
			found = true
		}
	}
	if !found {
		t.Fatal("hub 模式工具清单缺 swarmmind_work_takeover")
	}
	for _, td := range (nodeBackend{}).tools() {
		if td.Name == "swarmmind_work_takeover" {
			t.Fatal("node 模式不应装配 swarmmind_work_takeover（hub-only）")
		}
	}

	// 全链：建单（带任期）→ 绑定派发 → 接管 → 读档见尝试
	out := mustCall(t, "swarmmind_work_create", map[string]any{
		"goal": "W2 绑定全链", "prime_term": "prime-1",
		"steps": []any{map[string]any{"desc": "盘点磁盘", "target_node": "mac-mini", "target_agent": "diskcheck"}},
	})
	wid := extractID(out, "w-")

	out2 := mustCall(t, "swarmmind_submit", map[string]any{
		"node_id": "mac-mini", "agent": "diskcheck", "stdin": "run",
		"idem_key": "k1", "work_ref": wid, "step_seq": float64(1), "prime_term": "prime-1",
	})
	if !strings.Contains(out2, "绑定工作单 "+wid) || !strings.Contains(out2, "尝试 #1") {
		t.Fatalf("绑定派发输出: %s", out2)
	}

	out = mustCall(t, "swarmmind_work_takeover", map[string]any{"work_id": wid, "expected_term": "prime-1", "new_term": "prime-2"})
	if !strings.Contains(out, "已接管") || !strings.Contains(out, "prime-2") {
		t.Fatalf("接管输出: %s", out)
	}
	// 重放幂等
	out = mustCall(t, "swarmmind_work_takeover", map[string]any{"work_id": wid, "expected_term": "prime-2", "new_term": "prime-2"})
	if !strings.Contains(out, "重放幂等命中") {
		t.Fatalf("重放输出: %s", out)
	}
	// 旧任期接管冲突：人话 + isError
	res, rpcErr := callTool("swarmmind_work_takeover", map[string]any{"work_id": wid, "expected_term": "prime-1", "new_term": "prime-9"})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, "任期不符") {
		t.Fatalf("接管冲突应人话报错: %v %v", rpcErr, res)
	}
	// 旧任期派发拒绝：人话
	res, rpcErr = callTool("swarmmind_submit", map[string]any{
		"node_id": "mac-mini", "agent": "diskcheck", "stdin": "run",
		"idem_key": "k2", "work_ref": wid, "step_seq": float64(1), "prime_term": "prime-1",
	})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, "任期不符") {
		t.Fatalf("旧任期派发应人话报错: %v %v", rpcErr, res)
	}

	// 读档：尝试与 executing 状态可见
	out = mustCall(t, "swarmmind_work_get", map[string]any{"work_id": wid})
	for _, want := range []string{"尝试#1（步骤#1）", "执行中", "attempt_recorded", "takeover", "prime-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("读档缺 %q: %s", want, out)
		}
	}

	// status 透传工作单绑定行（绑定派发任务回读定位）
	tid := extractID(out2, "t-")
	if tid == "" {
		t.Fatalf("派发输出缺任务 ID")
	}
	out3 := mustCall(t, "swarmmind_status", map[string]any{"task_id": tid})
	if !strings.Contains(out3, "工作单绑定："+wid+" 步骤 #1") {
		t.Fatalf("status 应透传工作单绑定行: %s", out3)
	}
}

// extractID 从工具输出截取指定前缀 ID（行首到行尾）。
func extractID(out, prefix string) string {
	i := strings.Index(out, prefix)
	if i < 0 {
		return ""
	}
	id := out[i:]
	if j := strings.IndexAny(id, " \n：（"); j >= 0 {
		id = id[:j]
	}
	return strings.TrimSpace(id)
}

// TestWorkBindingSubmitStrictValidation 绑定参数严格校验（零改写，请求不发出；
// 援引 W1 depends_on 两段式口径）：step_seq 非整数/非数字/越界、三参不成组、
// 任期缺失——全部工具层拒绝，hub 零请求。
func TestWorkBindingSubmitStrictValidation(t *testing.T) {
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tasks" {
			seen = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"t-x"}`))
	}))
	defer srv.Close()
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = srv.URL, "tok"
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	variants := []struct {
		name string
		args map[string]any
	}{
		{"非整数 step_seq", map[string]any{"step_seq": 2.5, "work_ref": "w-1", "prime_term": "t1"}},
		{"字符串 step_seq", map[string]any{"step_seq": "1", "work_ref": "w-1", "prime_term": "t1"}},
		{"零 step_seq", map[string]any{"step_seq": float64(0), "work_ref": "w-1", "prime_term": "t1"}},
		{"缺任期", map[string]any{"step_seq": float64(1), "work_ref": "w-1"}},
		{"任期空白", map[string]any{"step_seq": float64(1), "work_ref": "w-1", "prime_term": "  "}},
		{"缺步骤号", map[string]any{"work_ref": "w-1", "prime_term": "t1"}},
		{"缺工作单", map[string]any{"step_seq": float64(1), "prime_term": "t1"}},
	}
	for _, v := range variants {
		args := map[string]any{"node_id": "n1", "agent": "echo"}
		for k, val := range v.args {
			args[k] = val
		}
		if _, err := toolSubmit(args); err == nil {
			t.Fatalf("%s 应被拒绝", v.name)
		}
		if seen {
			t.Fatalf("%s 被拒时请求不得发出", v.name)
		}
	}

	// 合法三参：透传（数值归一 int64）
	if _, err := toolSubmit(map[string]any{
		"node_id": "n1", "agent": "echo", "stdin": "x",
		"work_ref": "w-1", "step_seq": float64(2), "prime_term": " t1 ",
	}); err != nil {
		t.Fatalf("合法绑定不应报错: %v", err)
	}
	if !seen {
		t.Fatal("请求未达 hub")
	}
}

// TestNodeSubmitRejectsWorkBinding node 模式绑定参数显式拒绝（不静默忽略——
// 静默丢弃=调用方以为绑定生效而实际没有）。
func TestNodeSubmitRejectsWorkBinding(t *testing.T) {
	for _, k := range []string{"work_ref", "step_seq", "prime_term"} {
		_, err := (nodeBackend{}).toolSubmit(map[string]any{"agent": "echo", k: "x"})
		if err == nil || !strings.Contains(err.Error(), "仅 hub 模式可用") {
			t.Fatalf("node 模式 %s 应显式拒绝: %v", k, err)
		}
	}
}

// bindSubmitAndFinish 直连 core 绑定派发一步并以 node 端点身份回传终态
//（MCP 层只验收集工具语义；派发/回写链 W2 已覆盖）。
func bindSubmitAndFinish(t *testing.T, c *core.Core, bus *transport.Bus, wid, term string, step int64, key, result string) {
	t.Helper()
	ack, err := c.Submit(protocol.TaskSubmitRequest{
		TargetNode: "mac-mini", TargetAgent: "diskcheck",
		Input:       protocol.TaskInput{Args: []string{"run"}},
		SubmittedBy: "tester",
		IdemKey:     key, WorkRef: wid, StepSeq: step, PrimeTerm: term,
	}, "")
	if err != nil {
		t.Fatalf("派发步骤 %d: %v", step, err)
	}
	st, res := protocol.ResultCompleted, &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "ok"}
	if result == "failed" {
		st = protocol.ResultFailed
		res = &protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 2, Error: "boom"}
	} else if result == "needs_review" {
		st = protocol.ResultNeedsReview
		res = &protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: 0}
	}
	b, err := json.Marshal(protocol.TaskResultPayload{TaskID: ack.TaskID, Status: st, Result: res})
	if err != nil {
		t.Fatalf("结果编码: %v", err)
	}
	if err := bus.Endpoint("mac-mini").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: b,
	}); err != nil {
		t.Fatalf("结果回传: %v", err)
	}
}

// TestWorkToolsW3Collect W3 六.六 同批（规则 12）：swarmmind_work_collect 装配
// （hub-only）、真实全链（建单→绑定派发→终态→收集 done/abandoned/回执）、
// 部分完成人话报错、空 report 工具层拒绝（请求不发出）、node 模式不装配。
func TestWorkToolsW3Collect(t *testing.T) {
	c, bus := realHubCoreBus(t)
	cards := []protocol.AgentCard{{Name: "diskcheck", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true}}
	if err := c.RegisterNode("mac-mini", cards); err != nil {
		t.Fatalf("注册 node: %v", err)
	}
	c.SetOnline("mac-mini", true)

	// 工具面：hub 含 collect；node 模式不含（hub-only 同批装配）
	found := false
	for _, td := range tools() {
		if td.Name == "swarmmind_work_collect" {
			found = true
		}
	}
	if !found {
		t.Fatal("hub 模式工具清单缺 swarmmind_work_collect")
	}
	for _, td := range (nodeBackend{}).tools() {
		if td.Name == "swarmmind_work_collect" {
			t.Fatal("node 模式不应装配 swarmmind_work_collect（hub-only）")
		}
	}

	// 全链：建单（3 步）→ 逐步绑定派发并到 done 终态 → 收集关闭 done
	wv, _, err := c.CreateWork(core.WorkCreateRequest{Goal: "W3 收集全链", PrimeTerm: "prime-1", Steps: []core.WorkStepInput{
		{Desc: "盘点磁盘", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
		{Desc: "盘点内存", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
		{Desc: "汇总", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
	}}, "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	wid := wv.ID
	for i, key := range []string{"m1", "m2", "m3"} {
		bindSubmitAndFinish(t, c, bus, wid, "prime-1", int64(i+1), key, "done")
	}
	out := mustCall(t, "swarmmind_work_collect", map[string]any{
		"work_id": wid, "prime_term": "prime-1", "report": "三步全部完成。",
	})
	for _, want := range []string{"已收集关闭", "3 步全部完成", "30 天"} {
		if !strings.Contains(out, want) {
			t.Fatalf("收集输出缺 %q: %s", want, out)
		}
	}
	// 同参重放 → 幂等回执
	out = mustCall(t, "swarmmind_work_collect", map[string]any{
		"work_id": wid, "prime_term": "prime-1", "report": "三步全部完成。",
	})
	if !strings.Contains(out, "关闭回执") {
		t.Fatalf("重放应幂等回执: %s", out)
	}
	// 终态后再收集（不同报告）→ 人话 work_closed
	res, rpcErr := callTool("swarmmind_work_collect", map[string]any{
		"work_id": wid, "prime_term": "prime-1", "report": "另写",
	})
	if rpcErr != nil || res["isError"] != true || !strings.Contains(res["content"].([]textContent)[0].Text, "已终态") {
		t.Fatalf("终态再收集应人话报错: %v %v", rpcErr, res)
	}

	// W3 审查③：MCP 读档与 REST 全量对齐——报告/创建与盖章时刻/幂等键/尝试引用/
	// 双指纹经 work_get 可读（任务清理后 Prime 不经 REST 也拿得回执行摘要与原键）；
	// work_list 透传 done_steps
	out = mustCall(t, "swarmmind_work_get", map[string]any{"work_id": wid})
	for _, want := range []string{"收集报告", "三步全部完成。", "键 m1", wid + "#a1", "创建 ", "关闭 ", "保留至"} {
		if !strings.Contains(out, want) {
			t.Fatalf("work_get 输出缺 %q: %s", want, out)
		}
	}
	if !regexp.MustCompile(`[0-9a-f]{64}`).MatchString(out) {
		t.Fatalf("work_get 输出应含内容指纹（64 hex 双指纹）: %s", out)
	}
	out = mustCall(t, "swarmmind_work_list", nil)
	if !strings.Contains(out, "3/3 步") {
		t.Fatalf("work_list 应透传 done_steps（3/3 步）: %s", out)
	}

	// W3 审查②：失败阻断 + 下游 pending（从未派发）——abandoned 处置放行，
	// 未派发步骤随关闭记为跳过（needs_review 阻断场景同出口，hub 拒绝误标完成）
	wv3, _, err := c.CreateWork(core.WorkCreateRequest{Goal: "W3 阻断放弃", PrimeTerm: "prime-1", Steps: []core.WorkStepInput{
		{Desc: "会失败的一步", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
		{Desc: "永远轮不到的一步", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
	}}, "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单 3: %v", err)
	}
	bindSubmitAndFinish(t, c, bus, wv3.ID, "prime-1", 1, "q1", "failed")
	out = mustCall(t, "swarmmind_work_collect", map[string]any{
		"work_id": wv3.ID, "prime_term": "prime-1", "report": "步骤一失败，放弃剩余。", "outcome": "abandoned",
	})
	if !strings.Contains(out, "已处置关闭") || !strings.Contains(out, "0/2 步完成、1 步未派发随关闭跳过") {
		t.Fatalf("pending 步骤应随放弃关闭记跳过: %s", out)
	}

	// 部分完成（步骤 2 failed）：缺省 outcome → work_steps_not_done 人话指路；
	// outcome=abandoned → 处置关闭成功
	wv2, _, err := c.CreateWork(core.WorkCreateRequest{Goal: "W3 部分完成", PrimeTerm: "prime-1", Steps: []core.WorkStepInput{
		{Desc: "步骤一", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
		{Desc: "步骤二", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
	}}, "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单 2: %v", err)
	}
	wid2 := wv2.ID
	bindSubmitAndFinish(t, c, bus, wid2, "prime-1", 1, "n1", "done")
	bindSubmitAndFinish(t, c, bus, wid2, "prime-1", 2, "n2", "failed")
	res, rpcErr = callTool("swarmmind_work_collect", map[string]any{
		"work_id": wid2, "prime_term": "prime-1", "report": "x",
	})
	if rpcErr != nil || res["isError"] != true ||
		!strings.Contains(res["content"].([]textContent)[0].Text, "abandoned") {
		t.Fatalf("部分完成应人话指路 abandoned: %v %v", rpcErr, res)
	}
	out = mustCall(t, "swarmmind_work_collect", map[string]any{
		"work_id": wid2, "prime_term": "prime-1", "report": "步骤二失败，放弃剩余。", "outcome": "abandoned",
	})
	if !strings.Contains(out, "已处置关闭") || !strings.Contains(out, "1/2") {
		t.Fatalf("abandoned 输出: %s", out)
	}

	// 工具层校验：空 report / 缺任期 / 非法 outcome → 拒绝且请求不发出
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/collect") {
			seen = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"work":{"id":"w-x","status":"done","steps":[]}}`))
	}))
	defer srv.Close()
	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = srv.URL, "tok"
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })
	for _, v := range []struct {
		name string
		args map[string]any
	}{
		{"空 report", map[string]any{"work_id": "w-1", "prime_term": "t1", "report": "  "}},
		{"缺 report", map[string]any{"work_id": "w-1", "prime_term": "t1"}},
		{"缺任期", map[string]any{"work_id": "w-1", "report": "x"}},
		{"非法 outcome", map[string]any{"work_id": "w-1", "prime_term": "t1", "report": "x", "outcome": "partial"}},
	} {
		if _, err := toolWorkCollect(v.args); err == nil {
			t.Fatalf("%s 应被拒绝", v.name)
		}
		if seen {
			t.Fatalf("%s 被拒时请求不得发出", v.name)
		}
	}
	// 合法形态透传（outcome 缺省不携带——hub 侧归一 done）
	if _, err := toolWorkCollect(map[string]any{"work_id": "w-1", "prime_term": "t1", "report": "x"}); err != nil {
		t.Fatalf("合法收集不应报错: %v", err)
	}
	if !seen {
		t.Fatal("请求未达 hub")
	}
}
