package localapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/transport"
)

// 导航统一 + 双端任务筛选一致性锁（TASK-nav-filter-20261026 v2）。
// 结构面：双端导航词表/navnum 格式/aria/workspace 行序、hub 筛选统一构造函数与五处
// 重建点、select 选项生命周期、node 来源筛选状态与词典出处（ORIGIN_WORDS 收进
// 「抽屉展示词典 shared block」，双端逐字节同文由 sharedBlock 承载）、文案迁移
// 完整清单（含 prime.go Go 端）——源码锚点锁。
// 行为面：node myTasksCard 渲染过滤（来源 chip 计数/留痕行归 local/零命中空态且
// 「加载更早」翻页不阻断/footer 作用域/与派单表单 S.f 互不干扰）、hub filteredTasks
// node 条件交集、blankFilters 形态、taskNodeOptions 选中机器从快照消失保留——经
// node 子进程加载真实 embed 资产抽块求值实测（先例：web_prompt_zone_test.go、
// internal/hub/web/dispatch_test.go）；node 不可得环境自动 skip，结构锁常驻兜底。

// fetchAsset 起本地 node localapi 服务取 embed 资产（与 web_drawer_sync_test 同款）。
func fetchAsset(t *testing.T, path string) string {
	t.Helper()
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// extractTopFunc 提取顶层函数源码（marker 起、至首个行首顶格闭括号行；本仓 app.js
// 顶层函数闭括号恒顶格，抽块思路同 sharedBlock——不复制实现防漂移）。
func extractTopFunc(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("源码缺 %s", marker)
	}
	j := strings.Index(src[i:], "\n}")
	if j < 0 {
		t.Fatalf("%s 未找到顶格闭括号", marker)
	}
	return src[i : i+j+2]
}

func TestWebNavFilterStructure(t *testing.T) {
	nodeJS := fetchAsset(t, "/app.js")
	nodeCSS := fetchAsset(t, "/style.css")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubCSS, err := os.ReadFile("../../hub/web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	primeGo, err := os.ReadFile("prime.go")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr, hubCSSStr := string(hubJS), string(hubCSS)

	// ---- 方案 B：导航词表（双端同概念同词、顺序定案） ----
	for _, want := range []string{
		`const menus = [['overview', '总览'], ['nodes', '机器'], ['tasks', '任务'], ['control', '派发'], ['works', '工作单'], ['groups', '群聊'], ['policy', '策略容量'], ['tokens', '设置']];`,
		"head('任务',",
		"head('派发',",
		"下一步：查看任务 →",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q", want)
		}
	}
	if !strings.Contains(string(nodeJS), `const PAGES = [['overview', '总览'], ['services', '服务'], ['tasks', '任务'], ['wizard', '连接']];`) {
		t.Error("node app.js PAGES 词表应与新导航定案一致（总览/服务/任务/连接）")
	}
	for _, want := range []string{
		"<h1>服务</h1>",
		"<h1>任务</h1>",
		"去看服务",
		"「服务 → 能力配置」",
		"完整清单见「任务」页。",
		"去「任务」选一个本机服务",
	} {
		if !strings.Contains(string(nodeJS), want) {
			t.Errorf("node app.js 应含 %q", want)
		}
	}
	// hub 端不得保留旧页名（用户可见面；注释不带标点不误伤）
	for _, ban := range []string{
		"head('任务监管'", "head('任务派发'", "「任务监管」页", "「任务派发」页", "查看任务监管",
		"['tasks', '任务监管']", "['control', '任务派发']",
	} {
		if strings.Contains(hubJSStr, ban) {
			t.Errorf("hub app.js 不应再含 %q（导航词表/文案迁移清单）", ban)
		}
	}
	for _, ban := range []string{
		"<h1>我的服务", "<h1>派任务", "去看我的服务", "「我的服务 → 能力配置」", "「我的服务」",
		"去「派任务」", "「派任务 · 我的任务」", "['services', '我的服务']", "['tasks', '派任务·我的任务']",
	} {
		if strings.Contains(string(nodeJS), ban) {
			t.Errorf("node app.js 不应再含 %q（导航词表/文案迁移清单）", ban)
		}
	}
	// Go 端返回文案（JS grep 查不到，单独锁）：prime.go 指路同改
	if strings.Contains(string(primeGo), "「我的服务 → 能力配置」") {
		t.Error("prime.go 不应再含旧页名「我的服务 → 能力配置」")
	}
	if !strings.Contains(string(primeGo), "「服务 → 能力配置」") {
		t.Error("prime.go 应含「服务 → 能力配置」（导航词迁移 Go 端同步）")
	}

	// ---- 方案 B：视觉与无障碍同构（navnum/aria/workspace 行序） ----
	for _, src := range []struct{ side, js string }{{"hub", hubJSStr}, {"node", string(nodeJS)}} {
		if !strings.Contains(src.js, `aria-label="旅程导航"`) {
			t.Errorf("%s app.js nav 应含 aria-label=\"旅程导航\"", src.side)
		}
		if !strings.Contains(src.js, `aria-current="page"`) {
			t.Errorf("%s app.js nav 应含 aria-current=\"page\"", src.side)
		}
		if !strings.Contains(src.js, `<span class="navnum">0${i + 1}</span>`) {
			t.Errorf("%s app.js navnum 应为 01-0N 同格式", src.side)
		}
		if !strings.Contains(src.js, `<div class="workspace"><small`) ||
			strings.Index(src.js, `<div class="workspace"><small`) > strings.Index(src.js, `<strong class="workspace-brand">`) {
			t.Errorf("%s app.js workspace 行序应为 small 在上、strong 在下（与 hub 同构）", src.side)
		}
	}
	if strings.Contains(string(nodeJS), `const nums = ['①', '②', '③', '④']`) {
		t.Error("node app.js 不应再带圈数字 navnum（01-04 同格式）")
	}

	// ---- 方案 A1：ORIGIN_WORDS 词典同源（shared block 内双端逐字节同文） ----
	dict := sharedBlock(t, nodeJS, hubJSStr, "抽屉展示词典 shared block")
	if !strings.Contains(dict, `const ORIGIN_WORDS = { hub: 'hub 下发', local: '本机派单' };`) {
		t.Error("抽屉展示词典 shared block 应含 ORIGIN_WORDS（双端逐字节同文由 sharedBlock 保证）")
	}
	for _, want := range []string{
		"ORIGIN_WORDS[origin === 'hub' ? 'hub' : 'local']", // originBadge 消费词典
		`· 来源 ${ORIGIN_WORDS[local ? 'local' : 'hub']}`,  // 抽屉来源行消费词典
		"tf: { origin: '' }",
		"S.tf = { origin: '' };", // resetPrimeState 会话边界复位
		"case 'tf-origin':",
		"case 'tf-clear':",
		"没有符合筛选的任务",
		"筛选作用于已载入的",
		"function originOf(e)",
	} {
		if !strings.Contains(string(nodeJS), want) {
			t.Errorf("node app.js 应含 %q（来源筛选 A1）", want)
		}
	}
	if strings.Contains(string(nodeJS), `'hub 下发' : '本机派单'`) {
		t.Error("node app.js 不应再复制来源字面量（ORIGIN_WORDS 单一出处）")
	}

	// ---- 方案 A2：hub 统一构造函数与五处重建点 + select 生命周期 ----
	for _, want := range []string{
		"function blankFilters() {",
		"return { status: '', trace: '', by: '', error: '', from: '', to: '', node: '' };",
		"f: blankFilters(),",                     // 初始声明
		"{ ...blankFilters(), status: arg }",     // route 下钻（保留 status 直达语义，陷阱⑤）
		"{ ...blankFilters(), status }",          // go 下钻
		"if (f.node && t.target_node !== f.node) return false;",
		"S.f.from || S.f.to || S.f.node", // filtered 判定含 node
		"const advOpen = !!(S.f.by || S.f.error || S.f.from || S.f.to);", // node 不进高级折叠区
		"<label>目标机器<select data-f=\"node\">",
		"<option value=\"\">全部机器</option>",
		"function taskNodeOptions() {",
		"if (S.f.node) set.add(S.f.node);", // 选中机器从快照消失保留（不静默回退）
		"当前不在任务快照中",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（目标机器筛选 A2）", want)
		}
	}
	if n := strings.Count(hubJSStr, "S.f = blankFilters()"); n < 4 {
		t.Errorf("hub app.js S.f = blankFilters() 重建点应 ≥4（登录/会话失效/登出/清除筛选），实际 %d", n)
	}
	if strings.Contains(hubJSStr, "{ status: '', trace: '', by: '', error: '', from: '', to: '' }") {
		t.Error("hub app.js 不应再含旧 6 字段 S.f 字面量（统一构造函数后漏点防回潮）")
	}

	// ---- 样式：node 零计数 chip 退灰与 hub 同文 ----
	for _, want := range []string{".filters button.zero{opacity:.55}", ".filters button.zero.on{opacity:1}"} {
		if !strings.Contains(nodeCSS, want) {
			t.Errorf("node style.css 应含 %q", want)
		}
		if !strings.Contains(hubCSSStr, want) {
			t.Errorf("hub style.css 应含 %q", want)
		}
	}
}

// TestWebTaskFilterBehavior 筛选行为锁（node 子进程实测，环境无 node 自动 skip）：
// node myTasksCard 渲染过滤 + hub filteredTasks/taskNodeOptions/blankFilters 纯函数。
func TestWebTaskFilterBehavior(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：筛选行为断言跳过（结构锚点锁仍生效）")
	}
	nodeJS := fetchAsset(t, "/app.js")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr := string(hubJS)

	// ---- node 侧 harness：esc 原文行 + 桩 + 抽屉展示词典 shared block（含
	// ORIGIN_WORDS）+ 抽取的真实函数（badge/listStatusBadge/originBadge/originOf/
	// myTasksCard），场景驱动（无反引号，容纳 Go 原始字面量；sharedBlock 抽块起点在
	// 注释体内，组装时补回 /* 使其成合法注释）。 ----
	var escLine string
	for _, l := range strings.Split(nodeJS, "\n") {
		if strings.HasPrefix(l, "const esc = v =>") {
			escLine = l
			break
		}
	}
	if escLine == "" {
		t.Fatal("app.js 应含块外 esc 定义行（const esc = v => …）")
	}
	dictBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉展示词典 shared block")
	nodeFns := ""
	for _, marker := range []string{
		"function badge(status) {",
		"function listStatusBadge(e) {",
		"function originBadge(origin) {",
		"function originOf(e) {",
		"function myTasksCard() {",
	} {
		nodeFns += "\n" + extractTopFunc(t, nodeJS, marker)
	}
	nodeHarness := escLine + `
const S = { tasks: [], tasksTotal: 0, tasksMoreBusy: false, tf: { origin: '' }, f: { stage: 'idle', target: '', stdin: '草稿', idem: 'x', message: '' } };
function timeShort() { return '12:00:00'; }
function button(label, act, cls, inner) { return '<button data-act="' + act + '"' + (inner ? ' ' + inner : '') + '>' + label + '</button>'; }
/*` + dictBlk + nodeFns + "\n" + nodeFilterDriver

	// ---- hub 侧 harness：抽取的纯函数（blankFilters/taskNodeOptions/filteredTasks）
	// + S 桩，场景驱动。 ----
	hubFns := ""
	for _, marker := range []string{
		"function blankFilters() {",
		"function taskNodeOptions() {",
		"function filteredTasks() {",
	} {
		hubFns += "\n" + extractTopFunc(t, hubJSStr, marker)
	}
	hubHarness := "\nconst S = { f: null, tasks: [] };" + hubFns + "\n" + hubFilterDriver

	dir := t.TempDir()
	for name, src := range map[string]string{"nav_filter_node.js": nodeHarness, "nav_filter_hub.js": hubHarness} {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatalf("写 harness: %v", err)
		}
	}

	run := func(file string, scenarios []string) string {
		t.Helper()
		cmd := exec.Command(nodeBin, append([]string{filepath.Join(dir, file)}, scenarios...)...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("筛选行为断言失败（node 非零退出）：%v\n%s", err, out.String())
		}
		for _, sc := range scenarios {
			if !strings.Contains(out.String(), "PASS "+sc) {
				t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
			}
		}
		return out.String()
	}

	run("nav_filter_node.js", []string{
		"all-no-filter",        // 无筛选：全量行 + 三 chip 计数 + 无作用域说明
		"hub-filter",           // hub 筛选：仅 hub 行 + footer 作用域 1/3
		"local-counts-trace",   // 留痕行（无 status）计入 local 筛选与计数
		"trace-default-local",  // origin 缺失行按 local 归口（缺省语义）
		"zero-hit-more-alive",  // 零命中：空态+清除筛选，翻页入口不被阻断（codex 必须修项）
		"chips-zero-gray",      // 零计数 chip 带退灰类、不禁用
		"tf-form-independent",  // S.tf 筛选与派单表单 S.f 草稿互不干扰；清除回全量
	})
	run("nav_filter_hub.js", []string{
		"blank-shape",          // blankFilters 七字段全空
		"node-intersection",    // node 与 status/trace 交集过滤
		"node-gone-kept",       // 选中机器从快照消失：选项保留、结果零命中
		"options-unfiltered",   // 选项来自未过滤快照（非筛选结果）
		"options-dedupe-sorted", // 去重排序、空 target_node 不入选
	})
}

// nodeFilterDrive 场景驱动（ok 抛错即 node 非零退出，Go 侧以退出码 + PASS 标记判定）。
const nodeFilterDriver = `
const ok = (cond, msg) => { if (!cond) throw new Error('断言失败: ' + msg); };
const count = (s, re) => (s.match(re) || []).length;
const mkTask = (id, origin, status) => ({
  task_id: id, origin, status: status === undefined ? 'completed' : status,
  target_agent: 'echo', target_node: 'n1', created_at: '2026-10-26T01:00:00Z', idem_key: 'k-' + id,
});
const base = () => {
  S.tasks = [mkTask('a', 'local'), mkTask('b', 'hub'), mkTask('c', 'local')];
  S.tasksTotal = 3; S.tasksMoreBusy = false; S.tf = { origin: '' };
};
const SCENARIOS = {
  'all-no-filter': () => {
    base();
    const html = myTasksCard();
    ok(count(html, /data-task=/g) === 3, '无筛选应出全量 3 行');
    ok(count(html, /data-act="tf-origin"/g) === 3, '应恰三枚来源 chip');
    ok(html.includes('全部 3') && html.includes(ORIGIN_WORDS.local + ' 2') && html.includes(ORIGIN_WORDS.hub + ' 1'), 'chip 计数正确');
    ok(html.includes('class="on"') === true, '全部 chip 选中态');
    ok(!html.includes('筛选作用于已载入的'), '无筛选不显示作用域说明');
    ok(!html.includes('data-act="tf-clear"'), '无筛选不显示清除按钮');
  },
  'hub-filter': () => {
    base();
    S.tf.origin = 'hub';
    const html = myTasksCard();
    ok(count(html, /data-task=/g) === 1 && html.includes('data-task="b"'), 'hub 筛选仅剩 hub 行');
    ok(html.includes('筛选作用于已载入的 1 / 3 条'), 'footer 作用域 1/3');
    ok(!html.includes('data-task="a"') && !html.includes('data-task="c"'), 'local 行不出现');
  },
  'local-counts-trace': () => {
    base();
    S.tasks.push(mkTask('t-trace', 'local', ''));   // 留痕行：无 status
    S.tasksTotal = 4;
    S.tf.origin = 'local';
    const html = myTasksCard();
    ok(count(html, /data-task=/g) === 3, 'local 筛选含留痕行共 3 行');
    ok(html.includes('仅存留痕'), '留痕行按仅存留痕呈现');
    ok(html.includes('筛选作用于已载入的 3 / 4 条'), 'footer 作用域 3/4');
    S.tf.origin = '';
    ok(myTasksCard().includes(ORIGIN_WORDS.local + ' 3'), '留痕行计入 local chip 计数');
  },
  'trace-default-local': () => {
    base();
    S.tasks = [mkTask('a', 'local'), mkTask('c', 'local')];
    S.tasks.push({ task_id: 't-noorigin', target_agent: 'echo', target_node: 'n1', created_at: '2026-10-26T02:00:00Z' });
    S.tasksTotal = 3;
    S.tf.origin = 'local';
    ok(count(myTasksCard(), /data-task=/g) === 3, 'origin 缺失行按 local 归口参与筛选');
    S.tf.origin = 'hub';
    ok(myTasksCard().includes('没有符合筛选的任务'), '缺失行不计入 hub');
  },
  'zero-hit-more-alive': () => {
    base();
    S.tasks = [mkTask('a', 'local'), mkTask('c', 'local')];
    S.tasksTotal = 5;   // 还有更早历史可翻
    S.tf.origin = 'hub';
    const html = myTasksCard();
    ok(html.includes('没有符合筛选的任务'), '零命中显示空态');
    ok(html.includes('data-act="tf-clear"') && html.includes('清除筛选'), '空态带清除筛选');
    ok(html.includes('data-act="tasks-more"') && html.includes('加载更早的任务'), '零命中不阻断翻页入口（codex 必须修项）');
    ok(html.includes('筛选作用于已载入的 0 / 2 条'), 'footer 作用域 0/2');
  },
  'chips-zero-gray': () => {
    base();
    S.tasks = [mkTask('a', 'local')];
    S.tasksTotal = 1;
    const html = myTasksCard();
    ok(/data-origin="hub"[^>]*class="[^"]*zero/.test(html), '零计数 chip 带退灰类');
    ok(!/data-origin="hub"[^>]*disabled/.test(html), '零计数 chip 不禁用（可选中看空态）');
  },
  'tf-form-independent': () => {
    base();
    S.tf.origin = 'hub';
    const before = myTasksCard();
    S.f.stdin = '改草稿不影响筛选'; S.f.stage = 'acked';
    ok(myTasksCard() === before, '派单表单 S.f 变化不影响来源筛选渲染');
    S.tf = { origin: '' };
    ok(count(myTasksCard(), /data-task=/g) === 3, '清除筛选回全量');
  },
};
for (const name of process.argv.slice(2)) { SCENARIOS[name](); console.log('PASS ' + name); }
`

// hubFilterDriver hub 筛选纯函数场景驱动。
const hubFilterDriver = `
const ok = (cond, msg) => { if (!cond) throw new Error('断言失败: ' + msg); };
const mkT = (id, node, status) => ({ id, target_node: node, status, submitted_by: 'admin', trace_id: 'tr-' + id, created_at: '2026-10-26T01:00:00Z', result: null });
const SCENARIOS = {
  'blank-shape': () => {
    const bf = blankFilters();
    ok(Object.keys(bf).length === 7, '七字段');
    for (const k of ['status', 'trace', 'by', 'error', 'from', 'to', 'node']) ok(bf[k] === '', k + ' 应为空串');
  },
  'node-intersection': () => {
    S.f = blankFilters();
    S.tasks = [mkT('a', 'n1', 'completed'), mkT('b', 'n2', 'failed'), mkT('c', 'n1', 'failed')];
    S.f.node = 'n1';
    ok(filteredTasks().length === 2, 'node 筛选命中 2 行');
    S.f.status = 'failed';
    ok(filteredTasks().length === 1 && filteredTasks()[0].id === 'c', 'node+status 交集');
    S.f.status = ''; S.f.trace = 'tr-b';
    ok(filteredTasks().length === 0, 'node+trace 无交集为零结果（导出同源受筛）');
  },
  'node-gone-kept': () => {
    S.f = blankFilters();
    S.tasks = [mkT('a', 'n2', 'completed')];
    S.f.node = 'n1';
    const opts = taskNodeOptions();
    ok(opts.includes('n1'), '选中机器从快照消失仍保留在选项（不静默回退全部机器）');
    ok(filteredTasks().length === 0, '保留选中项显示零结果');
  },
  'options-unfiltered': () => {
    S.f = blankFilters();
    S.tasks = [mkT('a', 'n1', 'completed'), mkT('b', 'n2', 'failed')];
    S.f.node = 'n1';
    ok(taskNodeOptions().length === 2, '选项来自未过滤快照而非筛选结果');
  },
  'options-dedupe-sorted': () => {
    S.f = blankFilters();
    S.tasks = [mkT('a', 'n2', 'completed'), mkT('b', 'n1', 'completed'), mkT('c', 'n2', 'failed'), mkT('d', '', 'completed')];
    const opts = taskNodeOptions();
    ok(JSON.stringify(opts) === JSON.stringify(['n1', 'n2']), '去重排序且空 target_node 不入选');
  },
};
for (const name of process.argv.slice(2)) { SCENARIOS[name](); console.log('PASS ' + name); }
`
