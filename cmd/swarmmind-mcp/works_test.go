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
	return c
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
