package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeTasksHub 假 hub：GET /api/v1/tasks 全量直出（与 handleListTasks V0 口径
// 一致——不解析不回显任何 query 参数）。命中路径与原始 query 经 channel 交付：
// handler goroutine 写、测试 goroutine 经返回的 hit() 读，channel 即同步点
// （codex 审查必须修 3：原共享变量无同步，-race 报数据竞争）。缓冲 1 存最近
// 一次请求（handler 不得阻塞）；测试侧仅在单次 toolTasks 调用后读取。
// 断言口径：MCP 筛选必须本地完成，不得虚构服务端透传参数。
func fakeTasksHub(t *testing.T, tasks []map[string]any) (*httptest.Server, func() (string, string)) {
	t.Helper()
	hit := make(chan [2]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- [2]string{r.URL.Path, r.URL.RawQuery}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
	}))
	t.Cleanup(srv.Close)
	return srv, func() (string, string) {
		t.Helper()
		select {
		case h := <-hit:
			return h[0], h[1]
		default:
			t.Fatal("未捕获到 hub 请求")
			return "", ""
		}
	}
}

// rowOf 逐行解析定位目标任务的列表行（codex 审查建议修 4：按换行 split 后在
// 目标行内断言字段，不做全文搜索——避免某行缺失的字段被其他行误绿）。
func rowOf(t *testing.T, text, fullID string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, fullID) {
			return line
		}
	}
	t.Fatalf("列表中无全号 %s 的行：\n%s", fullID, text)
	return ""
}

// taskSeed 列表条目测试数据（字段名对照 protocol.Task 的 JSON tag；created_at
// 倒序给出即模拟服务端「新任务在前」返回序）。
func taskSeed(id, status, node, agent string, createdAgo time.Duration, extra map[string]any) map[string]any {
	now := time.Now().UTC()
	m := map[string]any{
		"id":           id,
		"trace_id":     "",
		"target_node":  node,
		"target_agent": agent,
		"status":       status,
		"created_at":   now.Add(-createdAgo).Format(time.RFC3339),
		"updated_at":   now.Add(-createdAgo / 2).Format(time.RFC3339),
		"expire_at":    now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

const (
	tasksIDRun  = "t-aa11bb22cc33dd44ee55ff6600112233"
	tasksIDFail = "t-bb22cc33dd44ee55ff6600112233aa"
	tasksIDDone = "t-cc33dd44ee55ff6600112233aabb22"
	tasksIDBlk  = "t-dd44ee55ff6600112233aabb22cc33dd"
	tasksIDRev  = "t-ee55ff6600112233aabb22cc33dd44ee"

	// 终态渲染测试专用（TestToolTasksTerminalRender）。
	tasksIDCancel   = "t-11223344556677889900aabbccddeeff"
	tasksIDCancelNE = "t-2233445566778899aabbccddeeff0011"
	tasksIDOffered  = "t-3344556677889900aabbccddeeff1122"
	tasksIDAck      = "t-44556677889900aabbccddeeff334455"
)

// tasksFixture 五状态样例：running/failed/completed/submitted(依赖阻塞)/
// needs_review，提交时间新→旧排列（即 hub 返回序）。
func tasksFixture() []map[string]any {
	return []map[string]any{
		taskSeed(tasksIDRun, "running", "mac-mini", "diskcheck", 5*time.Minute, nil),
		taskSeed(tasksIDFail, "failed", "mac-mini", "coder", time.Hour, map[string]any{
			"result": map[string]any{"status": "failed", "error": "timeout", "exit_code": -1},
		}),
		taskSeed(tasksIDDone, "completed", "baidu-node", "echo", 26*time.Hour, map[string]any{
			"result": map[string]any{"status": "completed", "exit_code": 0},
		}),
		taskSeed(tasksIDBlk, "submitted", "baidu-node", "echo", 2*time.Hour, map[string]any{
			"blocked_reason": "blocked_by:up-1",
		}),
		taskSeed(tasksIDRev, "needs_review", "mac-mini", "coder", 3*time.Hour, map[string]any{
			"result": map[string]any{"status": "needs_review", "error": "node_restarted_midway"},
		}),
	}
}

// 验收①：人话渲染与请求形状——每行短码/状态人话/节点/agent/提交时间/耗时或
// 错误码、行尾附全号；请求为裸路径零 query（REST 不解析筛选参数，本地过滤）。
func TestToolTasksRender(t *testing.T) {
	saveDecideEnv(t)
	srv, hit := fakeTasksHub(t, tasksFixture())
	hubURL, hubToken = srv.URL, "tok"

	text, err := toolTasks(map[string]any{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	// 参数透传口径：请求恒为裸路径 /api/v1/tasks 且无 query（筛选在本地完成）
	gotPath, gotQuery := hit()
	if gotPath != "/api/v1/tasks" || gotQuery != "" {
		t.Fatalf("请求应为裸路径 /api/v1/tasks 且无 query，实际 %s?%s", gotPath, gotQuery)
	}
	// 状态人话五态齐备（排队三态合一、needs_review=等你确认）
	for _, want := range []string{"执行中", "失败", "已完成", "排队中", "等你确认"} {
		if !strings.Contains(text, want) {
			t.Errorf("列表缺状态人话 %q：\n%s", want, text)
		}
	}
	// 行要素逐行断言（codex 审查建议修 4）：短码+节点/agent+提交时间+耗时/
	// 错误码/阻塞标注+全号，全部在目标行内校验
	checks := []struct {
		row  string
		want []string
	}{
		{tasksIDRun, []string{"t-aa11bb22 执行中 mac-mini/diskcheck", "提交 ", "已运行 ", "｜全号 " + tasksIDRun}},
		{tasksIDFail, []string{"失败 mac-mini/coder", "耗时 ", "错误码 timeout"}},
		{tasksIDDone, []string{"已完成 baidu-node/echo", "耗时 "}},
		{tasksIDBlk, []string{"排队中 baidu-node/echo", "已排队 ", "依赖阻塞"}},
		{tasksIDRev, []string{"等你确认 mac-mini/coder", "错误码 node_restarted_midway"}},
	}
	for _, c := range checks {
		line := rowOf(t, text, c.row)
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("行 %s 缺 %q：%s", c.row, w, line)
			}
		}
	}
	// 短码必须与全号同行成对出现，且不可只给短码（短码不能当 task_id 寻址）
	if strings.Count(text, "｜全号 t-") != 5 {
		t.Errorf("应 5 行各附全号，实际 %d：\n%s", strings.Count(text, "｜全号 t-"), text)
	}
	// 终态三行（failed/completed/needs_review）给耗时，非终态两行不给（逐行扫描）
	durRows := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "耗时 ") {
			durRows++
		}
	}
	if durRows != 3 {
		t.Errorf("耗时 应只出现在 3 个终态行，实际 %d：\n%s", durRows, text)
	}
	// 顺序保持服务端「新任务在前」：running → failed → completed
	if !(strings.Index(text, tasksIDRun) < strings.Index(text, tasksIDFail) &&
		strings.Index(text, tasksIDFail) < strings.Index(text, tasksIDDone)) {
		t.Errorf("渲染顺序应保持服务端返回序（新任务在前）：\n%s", text)
	}
	// 头部统计
	if !strings.Contains(text, "符合 5 条（网内共 5 个") {
		t.Errorf("头部统计不符：\n%s", text)
	}
}

// 验收②：筛选与截断（status/node_id/agent 精确匹配、组合筛选、limit 截断、
// 空命中的人话提示）。
func TestToolTasksFilters(t *testing.T) {
	saveDecideEnv(t)
	srv, _ := fakeTasksHub(t, tasksFixture())
	hubURL, hubToken = srv.URL, "tok"

	t.Run("按状态筛选", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"status": "failed"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(text, "符合 1 条") || !strings.Contains(text, "筛选：状态=failed") {
			t.Fatalf("头部应标注筛选条件与命中数：\n%s", text)
		}
		if !strings.Contains(text, tasksIDFail) {
			t.Fatalf("status=failed 应命中失败任务：\n%s", text)
		}
		if strings.Contains(text, tasksIDRun) || strings.Contains(text, tasksIDDone) || strings.Contains(text, tasksIDRev) {
			t.Fatalf("status=failed 原样精确匹配，不得混入其他状态：\n%s", text)
		}
	})
	t.Run("按状态筛 needs_review", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"status": "needs_review"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(text, "符合 1 条") || !strings.Contains(text, "等你确认") || !strings.Contains(text, tasksIDRev) {
			t.Fatalf("status=needs_review 应命中待核对任务：\n%s", text)
		}
	})
	t.Run("按节点筛选", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"node_id": "mac-mini"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(text, "符合 3 条") || strings.Contains(text, "baidu-node/") {
			t.Fatalf("node_id=mac-mini 应只剩 3 条且无 baidu-node 行：\n%s", text)
		}
	})
	t.Run("按 agent 筛选", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"agent": "echo"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(text, "符合 2 条") || strings.Contains(text, "mac-mini/diskcheck") {
			t.Fatalf("agent=echo 应只剩 2 条：\n%s", text)
		}
	})
	t.Run("组合筛选", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"status": "failed", "node_id": "baidu-node"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		// fixture 中 baidu-node 只有 completed/submitted，failed 都在 mac-mini → 零命中
		if !strings.Contains(text, "没有符合筛选条件的任务（网内共 5 个）") {
			t.Fatalf("failed+baidu-node 无命中，应人话提示空结果：\n%s", text)
		}
	})
	t.Run("limit 截断", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"limit": float64(2)})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if strings.Count(text, "｜全号 t-") != 2 {
			t.Fatalf("limit=2 应只渲染 2 行：\n%s", text)
		}
		if !strings.Contains(text, "仅显示最新 2 条，其余 3 条未展示") {
			t.Fatalf("截断应附收窄指引：\n%s", text)
		}
	})
	t.Run("筛选无命中", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"status": "cancelled"})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !strings.Contains(text, "没有符合筛选条件的任务（网内共 5 个）") {
			t.Fatalf("空命中应提示总量：\n%s", text)
		}
	})
}

// 验收③：终态渲染以 Status 为准（codex 审查必须修 2 + 建议修 5）。cancelled +
// Result=nil 是真实状态（hub 取消即收敛终态，node 真实执行数据后到才合并
// Result，internal/hub/core/cancel.go CancelTask「Result 留空」）：渲染「已取消」
// 且时长定格到取消时刻（updated_at），不得误显示「已排队」且随 now 持续增长；
// updated_at 缺失（EndTime 无）则不显示时长；offered/acknowledged 排队三态合一。
func TestToolTasksTerminalRender(t *testing.T) {
	saveDecideEnv(t)
	now := time.Now().UTC()
	// cancelled+Result=nil，取消时刻=提交后 10 分钟：定格耗时恰为 10m00s
	//（created/updated 同一 now 基准相减，数值确定，防断言抖动）
	cancelled := taskSeed(tasksIDCancel, "cancelled", "mac-mini", "coder", 0, nil)
	cancelled["created_at"] = now.Add(-2 * time.Hour).Format(time.RFC3339)
	cancelled["updated_at"] = now.Add(-110 * time.Minute).Format(time.RFC3339)
	// cancelled+Result=nil 且 updated_at 为零值（EndTime 缺失）：不显示时长
	cancelledNoEnd := taskSeed(tasksIDCancelNE, "cancelled", "baidu-node", "echo", time.Hour, map[string]any{
		"updated_at": "0001-01-01T00:00:00Z",
	})
	srv, _ := fakeTasksHub(t, []map[string]any{
		cancelled,
		cancelledNoEnd,
		taskSeed(tasksIDOffered, "offered", "mac-mini", "diskcheck", 10*time.Minute, nil),
		taskSeed(tasksIDAck, "acknowledged", "baidu-node", "echo", 5*time.Minute, nil),
	})
	hubURL, hubToken = srv.URL, "tok"

	text, err := toolTasks(map[string]any{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	line := rowOf(t, text, tasksIDCancel)
	if !strings.Contains(line, "已取消") || !strings.Contains(line, "耗时 10m00s") {
		t.Errorf("取消态应渲染「已取消」且时长定格取消时刻 10m00s：%s", line)
	}
	if strings.Contains(line, "已排队") || strings.Contains(line, "已运行") {
		t.Errorf("取消态不得显示排队/运行时长（会随 now 持续增长）：%s", line)
	}
	line = rowOf(t, text, tasksIDCancelNE)
	if !strings.Contains(line, "已取消") || strings.Contains(line, "耗时") || strings.Contains(line, "已排队") {
		t.Errorf("无 EndTime 的取消态应只渲染已取消、不给时长：%s", line)
	}
	for _, id := range []string{tasksIDOffered, tasksIDAck} {
		line = rowOf(t, text, id)
		if !strings.Contains(line, "排队中") || !strings.Contains(line, "已排队 ") {
			t.Errorf("排队三态应合一渲染排队中+已排队时长：%s", line)
		}
		if strings.Contains(line, "耗时") {
			t.Errorf("非终态不得出现耗时：%s", line)
		}
	}
}

// 验收④：参数校验（codex 审查必须修 1 + 建议修 4/5）。断言一律走 callTool
// 分发入口（hubBackend.call 委托 callTool）取 isError，不直接断言 toolTasks 的
// error——校验拒绝最终以 isError=true 文本交付给 MCP 调用方。
func TestToolTasksParamValidation(t *testing.T) {
	saveDecideEnv(t)
	srv, _ := fakeTasksHub(t, tasksFixture())
	hubURL, hubToken = srv.URL, "tok"

	t.Run("合法 limit 边界 1/100 与 json.Number", func(t *testing.T) {
		for _, lim := range []any{float64(1), float64(100), json.Number("5")} {
			if _, err := toolTasks(map[string]any{"limit": lim}); err != nil {
				t.Fatalf("limit=%v 应合法，got：%v", lim, err)
			}
		}
	})
	t.Run("limit=1 只渲染 1 行", func(t *testing.T) {
		text, err := toolTasks(map[string]any{"limit": float64(1)})
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if strings.Count(text, "｜全号 t-") != 1 {
			t.Fatalf("limit=1 应只渲染 1 行：\n%s", text)
		}
	})
	t.Run("非法 limit 一律参数错误（含 1e100 巨值不绕上限）", func(t *testing.T) {
		cases := []any{
			float64(0),     // 0 明确拒绝
			float64(-3),    // 负数明确拒绝
			float64(2.5),   // 非整数拒绝
			float64(101),   // 超上限拒绝
			float64(1e100), // 超出 int64 巨值：转换前拒绝，杜绝 int 回绕为负绕过 100 上限
			"20",           // 非数字类型拒绝
			json.Number("abc"),
		}
		for _, lim := range cases {
			text, isErr := callText(t, hubBackend{}, "swarmmind_tasks", map[string]any{"limit": lim})
			if !isErr {
				t.Errorf("limit=%v 应拒绝为 isError=true，got 正常文本：%s", lim, text)
			}
			if !strings.Contains(text, "limit") {
				t.Errorf("错误信息应点名 limit，got：%s", text)
			}
		}
	})
	t.Run("1e100 拒绝信息明确提示上限 100", func(t *testing.T) {
		text, isErr := callText(t, hubBackend{}, "swarmmind_tasks", map[string]any{"limit": float64(1e100)})
		if !isErr || !strings.Contains(text, "100") {
			t.Errorf("超界巨值应拒绝且提示上限 100，isErr=%v：%s", isErr, text)
		}
	})
	t.Run("非法 status 拒绝而非空结果", func(t *testing.T) {
		text, isErr := callText(t, hubBackend{}, "swarmmind_tasks", map[string]any{"status": "runnin"})
		if !isErr || !strings.Contains(text, "status 非法") {
			t.Errorf("非法 status 应拒绝为 isError 并提示 status 非法，isErr=%v：%s", isErr, text)
		}
		if strings.Contains(text, "没有符合筛选条件的任务") {
			t.Errorf("非法 status 不得误报为空结果：%s", text)
		}
	})
}

// 验收⑤：schema 声明齐全（hub 工具清单含 swarmmind_tasks，四个筛选参数在册；
// node 模式不设本工具——列表是 hub 视角）。
func TestToolTasksSchema(t *testing.T) {
	var td *toolDef
	for _, d := range (hubBackend{}).tools() {
		if d.Name == "swarmmind_tasks" {
			td = &d
			break
		}
	}
	if td == nil {
		t.Fatal("hub 工具清单缺 swarmmind_tasks")
	}
	if !strings.Contains(td.Description, "列出网内任务") || !strings.Contains(td.Description, "筛选") {
		t.Fatalf("描述应人话说明能做什么：%s", td.Description)
	}
	props, _ := td.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"status", "node_id", "agent", "limit"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema 缺筛选参数 %s", k)
		}
	}
	if req, ok := td.InputSchema["required"]; ok {
		t.Errorf("列表工具不应有必填参数：%v", req)
	}
	for _, d := range (nodeBackend{}).tools() {
		if d.Name == "swarmmind_tasks" {
			t.Fatal("node 模式不应设 swarmmind_tasks（列表是 hub 视角）")
		}
	}
}

// 验收⑥：空列表与错误路径（hub 5xx 人话透传、不可达报错——经 callTool 包装为
// isError=true 文本）。
func TestToolTasksEmptyAndErrors(t *testing.T) {
	saveDecideEnv(t)

	t.Run("空列表", func(t *testing.T) {
		srv, _ := fakeTasksHub(t, []map[string]any{})
		hubURL, hubToken = srv.URL, "tok"
		text, err := toolTasks(map[string]any{})
		if err != nil || !strings.Contains(text, "网内还没有任务记录") {
			t.Fatalf("空列表应人话提示，got %q err=%v", text, err)
		}
	})
	t.Run("hub 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		}))
		defer srv.Close()
		hubURL, hubToken = srv.URL, "tok"
		_, err := toolTasks(map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "hub 返回 500") {
			t.Fatalf("非 200 应报错并带状态码，got %v", err)
		}
	})
	t.Run("hub 不可达", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // 立即关闭制造连接失败
		hubURL, hubToken = srv.URL, "tok"
		if _, err := toolTasks(map[string]any{}); err == nil {
			t.Fatal("hub 不可达应报错")
		}
	})
	t.Run("坏 JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not-json{"))
		}))
		defer srv.Close()
		hubURL, hubToken = srv.URL, "tok"
		_, err := toolTasks(map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "应答解析失败") {
			t.Fatalf("坏 JSON 应报解析错误，got %v", err)
		}
	})
}
