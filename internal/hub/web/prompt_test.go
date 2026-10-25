// prompt_test.go Prime 提示词与管理台结构锁（P5 W3，TESTPLAN-W3-work 核心 4 +
// 支撑「TestConsoleWorkViewSources」）：
//  1. 提示词与接口一致（RFC W3 行验收）：app.js PRIME_PROMPT 引用的 swarmmind_*
//    工具名 ⊆ cmd/swarmmind-mcp 实际装配集——提示词不得给出接口不存在的用法；
//  2. 提示词两处一致（PRIME-DESIGN 既有约定）：app.js 模板体与 deploy/prime/
//    PRIME.md 行为节逐字节同文，单改文档不算改行为；
//  3. W3 六条锚文案在场（建单时机/绑定三参/断点续管/重试预算/收集与部分汇总/
//    工具口径）且 {{GOAL}} 模板标记完好；
//  4. 工作单抽屉稳定 DOM 结构（D22 纪律）：分区壳 + 动作区一次建壳恒定节点、
//    不在分区 diff 内；无整层重建回潮。
package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readRepo 以本文件所在目录（internal/hub/web）为基准读仓库内文件。
func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// extractPrimePrompt 从 app.js 源码截取 PRIME_PROMPT 模板体（反引号字符串字面量）。
func extractPrimePrompt(t *testing.T, appJS string) string {
	t.Helper()
	const marker = "const PRIME_PROMPT = `"
	i := strings.Index(appJS, marker)
	if i < 0 {
		t.Fatal("app.js 缺 PRIME_PROMPT 模板")
	}
	rest := appJS[i+len(marker):]
	j := strings.Index(rest, "`;")
	if j < 0 {
		t.Fatal("PRIME_PROMPT 模板未闭合")
	}
	// 模板字面量内的转义反引号（\`）归一化为 JS 运行时实际字符——锚文案与
	// PRIME.md 比对都基于运行时形态
	return strings.ReplaceAll(rest[:j], "\\`", "`")
}

// mcpToolNames 从 cmd/swarmmind-mcp/main.go 源码提取实际装配的工具名
// （tools() 列表 {Name: "swarmmind_xxx", ...} 为唯一登记点）。
func mcpToolNames(t *testing.T, mainGo string) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`Name: "(swarmmind_[a-z_]+)"`)
	ms := re.FindAllStringSubmatch(mainGo, -1)
	if len(ms) == 0 {
		t.Fatal("cmd/swarmmind-mcp/main.go 未提取到任何工具登记（正则失配需随实现更新）")
	}
	out := make(map[string]bool, len(ms))
	for _, m := range ms {
		out[m[1]] = true
	}
	return out
}

// TestPrimePromptWorkContract W3 核心 4「提示词与接口一致」：六条锚文案 + 工具名
// 子集 + 模板标记 + 两处一致（app.js ↔ deploy/prime/PRIME.md）。
func TestPrimePromptWorkContract(t *testing.T) {
	appJS := readRepo(t, "app.js")
	prompt := extractPrimePrompt(t, appJS)

	// 六条锚文案（W3 增补节「工作单（多步工作的断点续管）」+ 循环/幂等键接线；
	// W3 审查②⑥修订后口径：无在途即可收集、pending 随放弃跳过、键含工作单维度）
	for _, want := range []string{
		"## 工作单（多步工作的断点续管）",
		"**建单时机**",
		"**绑定派发**：建单后的每次派发，swarmmind_submit 必须携 `work_ref`（工作单 ID）+ `step_seq`（步骤号）+ `prime_term`（当前任期）三参",
		"**断点续管**：任期开始先 swarmmind_work_list",
		"swarmmind_work_takeover（expected_term=读档任期，new_term=本任期）取得续派权",
		"**重试预算**：同一工作单步骤的尝试 ≤2 次",
		"**收集与部分汇总**：无在途执行后 swarmmind_work_collect 关闭工作单",
		"确认放弃剩余步骤用 outcome=abandoned 并在报告写明理由",
		"未派发的步骤随放弃关闭自动记为跳过",
		"绑定工作单的派发用 `prime-<任期>-<工作单>-<步骤>-<尝试>`",
		"**幂等键与接管重放**：绑定派发键 `prime-<任期>-<工作单>-<步骤>-<尝试>`",
		"传输重试**保留原键原内容**（同键重放命中幂等回执，不会重复执行）",
		"原尝试还在 submitted/running 就继续 wait 跟踪等终态，不换键重派",
		"先建工作单再派发",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("PRIME_PROMPT 缺 W3 锚文案 %q", want)
		}
	}
	if n := strings.Count(prompt, "{{GOAL}}"); n != 1 {
		t.Fatalf("{{GOAL}} 模板标记应恰 1 处, got %d", n)
	}
	if !strings.Contains(appJS, "PRIME_PROMPT.includes('{{GOAL}}')") ||
		!strings.Contains(appJS, "PRIME_PROMPT.replace('{{GOAL}}', g)") {
		t.Error("app.js 目标拼接应保留 {{GOAL}} 替换逻辑（模板契约）")
	}

	// 工具名子集：提示词引用的 swarmmind_* 全部在 MCP 实际装配集内
	tools := mcpToolNames(t, readRepo(t, "../../../cmd/swarmmind-mcp/main.go"))
	ref := regexp.MustCompile(`swarmmind_[a-z_]+`)
	seen := map[string]bool{}
	for _, m := range ref.FindAllStringSubmatch(prompt, -1) {
		seen[m[0]] = true
	}
	for name := range seen {
		if !tools[name] {
			t.Errorf("PRIME_PROMPT 引用工具 %s 不在 MCP 装配集（提示词不得给出接口不存在的用法）", name)
		}
	}
	// W3 五个面向统筹的工具必须被提示词提及（工具给了不代表会用对：提示词没用法=没用对）
	for _, name := range []string{"swarmmind_work_create", "swarmmind_work_list", "swarmmind_work_get", "swarmmind_work_takeover", "swarmmind_work_collect"} {
		if !seen[name] {
			t.Errorf("PRIME_PROMPT 未提及 %s（工作单六条缺工具接线）", name)
		}
	}

	// 两处一致：PRIME.md 行为节（自「## 分配决策」起）与模板体逐字节同文
	// （PRIME.md 前部为客户端配置文档，不在同步面内；行为提示词体必须一致——
	// 单改文档不能视为运行行为已更新，PRIME-DESIGN 既有约定）
	anchor := "## 分配决策"
	i := strings.Index(prompt, anchor)
	if i < 0 {
		t.Fatal("PRIME_PROMPT 缺分配决策节")
	}
	suffix := prompt[i:]
	primeMD := readRepo(t, "../../../deploy/prime/PRIME.md")
	if !strings.Contains(primeMD, suffix) {
		t.Errorf("deploy/prime/PRIME.md 行为节与 PRIME_PROMPT 模板体不一致（两处须同批同改）")
	}
	if strings.Count(primeMD, suffix) != 1 {
		t.Errorf("PRIME.md 行为节应恰出现 1 次")
	}
}

// TestConsoleWorkViewSources 工作单管理台结构锁（源码面）：分区壳 + 稳定动作区 +
// 无整层重建回潮 + 词典归属（shared block 外）。
func TestConsoleWorkViewSources(t *testing.T) {
	appJS := readRepo(t, "app.js")
	for _, want := range []string{
		"const WORK_ZONES = ['head', 'intro', 'steps', 'attempts', 'report', 'events'];",
		"function workShellHtml() {",
		`drawerZonesApply(dr, workZones(S.workDrawer.work), S.workDrawer.zones, WORK_ZONES)`, // 复用 shared block 分区机制（泛化 names）
		`id="work-actions" data-work-actions`, // 动作区独立于分区 diff（一次建壳恒定节点）
		"function workActionsHtml() {",
		"function applyWorkFormState() {",
		"function fetchWorkDetail() {",
		"const WORK_STATES = {",
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("hub app.js 应含 %q", want)
		}
	}
	// 动作区不得进分区模板（分区 diff 会重建表单丢草稿——阶段 D 必须修 3 同款）
	if strings.Contains(appJS, "actions: `<section") || strings.Contains(appJS, "actions: workActionsHtml()") {
		t.Error("工作单动作区不得作为分区内容产出（表单节点须一次建恒定）")
	}
	// 工作单抽屉刷新路径禁止整层重建（workZones 模板串不得直写 .drawer/.layer innerHTML）
	for _, ban := range []string{
		".drawer').innerHTML = workZones(",
		"innerHTML = workZones(",
	} {
		if strings.Contains(appJS, ban) {
			t.Errorf("hub app.js 不应含 %q（工作单抽屉刷新走分区 diff）", ban)
		}
	}

	// 写动作三重迟到防护（W3 审查④，源码锁）：三个写动作各含「身份校验 + 写前
	// 作废在途读档 + 成功推进本地回写版本」，读档按版本丢弃过时应答
	if n := strings.Count(appJS, "if (S.workDrawer !== wd) return;"); n != 3 {
		t.Errorf("三个写动作（接管/续派/处置）都应含抽屉身份校验，得 %d 处", n)
	}
	if n := strings.Count(appJS, "wd.seq++;"); n != 3 {
		t.Errorf("三个写动作都应在发送前作废在途读档（wd.seq++），得 %d 处", n)
	}
	if n := strings.Count(appJS, "wd.rev++;"); n != 3 {
		t.Errorf("三个写动作都应在成功/事实变更后推进本地回写版本（wd.rev++），得 %d 处", n)
	}
	for _, want := range []string{
		"if ((wd.rev || 0) !== rev) return;", // 读档在途期间发生写回写：过时应答丢弃
		"rev: 0,",                            // 抽屉状态携带本地回写版本
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("hub app.js 应含 %q（写动作三重迟到防护）", want)
		}
	}

	// 稳定 DOM 属性先比后写 + 下拉签名收敛（W3 审查⑤，源码锁）：hidden 属性
	// 不得无条件写；签名须以「选中步骤状态」为粒度，未选中步骤状态变化不触发
	// 重建；选中步不变时不得重预填（草稿保护）
	for _, want := range []string{
		"if (sec.hidden !== terminal) sec.hidden = terminal;",
		"if (box && box.hidden !== noDispatch) box.hidden = noDispatch;",
		"if (sel.value !== prev) workStepPicked();",
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("hub app.js 应含 %q（稳定 DOM 先比后写/草稿保护）", want)
		}
	}
	// 旧签名（全部可派步骤状态入签名）不得回潮：未选中步骤状态变化会重建选项
	// 并覆盖用户改过的执行者草稿
	if strings.Contains(appJS, "sig = eligible.map(s => `${s.seq}:${s.status}`)") {
		t.Error("续派下拉签名不得含全部步骤状态（审查⑤：收敛到选中步骤，防无关步骤状态变化冲掉草稿）")
	}
}
