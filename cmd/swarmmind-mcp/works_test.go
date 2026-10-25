// works_test.go 工作单工具 MCP 层实测（SPEC 六.六 同步开发规则③）：真实 api
// server 全链（MCP 工具函数 → HTTP → core.CreateWork），覆盖建单幂等/读档/
// 列表/事件游标与 hub-only 装配（node 模式不设，不静默降级）。
package main

import (
	"fmt"
	"strings"
	"testing"
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
