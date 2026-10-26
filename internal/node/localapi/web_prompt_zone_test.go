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

// TestWebPromptZoneShapeBehavior 任务指令分区形态判据行为锁（t20261026-prompt-args-md，
// docs/TASK-prompt-args-md-20261026.md DoD；t20261026-prompt-preview 预览折叠判据同锁）。
// web_drawer_sync_test 的 shared block
// 字符串锁只保「双端同文」不保「行为正确」——args 形态判据边界（换行/300 字符阈值/
// 双源/单源）、预览折叠判据边界（1200 字符阈值/展开收起态从状态算/原文态联动共享/短
// 内容不折叠）、esc 转义纪律、原文切换态稳定在此经 node 子进程加载真实 embed 资产抽块
// 求值实测（先例：internal/hub/web/dispatch_test.go TestConsoleDispatchSuggestFingerprint；
// node 不可得环境自动 skip，TestWebDrawerSharedBlocks 关键行源码锁仍常驻兜底）。
// 抽块经 sharedBlock 双端逐字节比对——hub 侧一致性由该锁承载，行为断言跑 node 侧一份
// 即全量。harness 组成：块外 esc 取资产原文行 + button/S 极简桩 + 三个 shared block
// （md 渲染器/抽屉展示词典/抽屉答复与日志）+ 场景驱动，逐场景 PASS 行为判据。
func TestWebPromptZoneShapeBehavior(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：形态判据行为断言跳过（源码锚点锁仍生效）")
	}
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.js 应 200, got %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	nodeJS := string(b)
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr := string(hubJS)

	mdBlk := sharedBlock(t, nodeJS, hubJSStr, "md 渲染器 shared block")
	dictBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉展示词典 shared block")
	logBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉答复与日志 shared block")

	// esc 取资产原文行（块外共用件，不复制防漂移）：mdToHtml 引用降级与原文态都走它
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

	// harness：esc 原文行 + 桩 + shared blocks + 场景驱动（全单引号字符串，无反引号，
	// 容纳 Go 原始字面量；shared block 经参数注入。sharedBlock 抽块起点在注释体内、
	// 不含开头 /*，组装时补回使其成合法 JS 注释）。*/
	harness := escLine + `
const S = { drawer: { promptRaw: false, promptExpanded: false } };
function button(label, act, cls) { return '<button data-act="' + act + '" class="' + (cls || '') + '">' + label + '</button>'; }
/*` + mdBlk + "\n/*" + dictBlk + "\n/*" + logBlk + "\n" + promptZoneHarnessDriver

	dir := t.TempDir()
	file := filepath.Join(dir, "prompt_zone_harness.js")
	if err := os.WriteFile(file, []byte(harness), 0o600); err != nil {
		t.Fatalf("写 harness: %v", err)
	}
	scenarios := []string{
		"newline-in-arg-md",        // 判据一：参数自身含换行 → 文本形态 md 渲染（标题/粗体/列表）
		"threshold-300-pills",      // 判据二边界内：恰 300 字符 → 短参数胶囊（与旧形态逐字节一致）
		"threshold-301-md",         // 判据二边界外：301 字符 → 文本形态
		"multi-arg-short-pills",    // 判据坑位：多参数 join 含换行为拼接伪影，不得误判文本形态
		"dual-short-sections",      // 双源：stdin md 小节 + args 短参数胶囊小节 + 共享原文切换
		"dual-long-sections",       // 双源：双 md 小节
		"dual-raw-single-pre",      // raw 态：单 pre 全部原文（stdin:/args: 行前缀），文案不变
		"single-stdin-no-subtitle", // 单源 stdin：无小节标题（现状形态）
		"single-args-md-raw",       // 单源 args 长文本 raw：单 pre 原文
		"both-empty-omit",          // 皆空分区省略
		"esc-discipline",           // 不可信面：md 形态与原文态均无 HTML 直通
		"toggle-state-stable",      // 切换态由调用方持有：同态产出一致（分区 diff 跳过写 DOM）
		"fold-default-over-threshold", // 判据三（t20261026-prompt-preview）：>1200 字符默认折叠（容器+遮罩+展开按钮）
		"fold-boundary-at-threshold",  // 判据三边界：恰 1200 全显现状形态；1201 折叠；双源合并计长
		"fold-expand-collapse-cycle",  // 展开态从状态算 HTML：prompt-open+收起+无遮罩，收起回折叠
		"fold-raw-shares-expand",      // 原文态同样折叠，展开态跨 raw/渲染共享
		"fold-short-no-preview",       // 短内容（<1200）无折叠无按钮（现状形态）
	}
	cmd := exec.Command(nodeBin, append([]string{file}, scenarios...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("形态判据行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// promptZoneHarnessDriver 形态判据场景驱动（同源判据与断言集中一处；ok 抛错即 node
// 非零退出，Go 侧以退出码 + PASS 标记判定）。
const promptZoneHarnessDriver = `
const ok = (cond, msg) => { if (!cond) throw new Error('断言失败: ' + msg); };
const SCENARIOS = {
  'newline-in-arg-md': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '', args: ['# 部署提示词\n\n**目标**：验证渲染\n\n- 第一步\n- 第二步'] } });
    ok(html.includes('<h3>任务指令</h3>'), '分区标题');
    ok(html.includes('<div class="md-body">'), '长文本应走 md 形态');
    ok(html.includes('<h1 class="md-h md-h1">部署提示词</h1>'), 'md 标题应渲染');
    ok(html.includes('<strong>目标</strong>'), 'md 粗体应渲染');
    ok(html.includes('<li>第一步</li>'), 'md 列表应渲染');
    ok(!html.includes('cmd-args'), '文本形态不得回退逐参胶囊');
    ok(html.includes('data-act="prompt-raw"'), '应有查看原文切换');
  },
  'threshold-300-pills': () => {
    S.drawer.promptRaw = false;
    const a = 'x'.repeat(300);
    const html = promptZoneHtml({ input: { stdin: '', args: [a] } });
    const legacy = '<h3>任务指令</h3><p class="muted small">本任务以命令行参数下达（逐参数展示，保留边界）：</p><p class="cmd-args"><code>' + a + '</code></p>';
    ok(html === legacy, '恰 300 字符（阈值内）应与旧短参数形态逐字节一致');
    ok(!html.includes('prompt-raw'), '短参数单源不应有原文切换');
  },
  'threshold-301-md': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '', args: ['y'.repeat(301)] } });
    ok(html.includes('<div class="md-body">'), '301 字符应判文本形态走 md');
    ok(!html.includes('cmd-args'), '文本形态不得回退胶囊');
    ok(html.includes('data-act="prompt-raw"'), '文本形态应有原文切换');
    ok(!html.includes('md-log-label'), '单源不显示小节标题');
  },
  'multi-arg-short-pills': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '', args: ['--model', 'opus', '--dry-run'] } });
    ok((html.match(/<code>/g) || []).length === 3, '三参数应逐参胶囊');
    ok(!html.includes('md-body'), '多参数短命令不得误判文本形态');
    ok(!html.includes('prompt-raw'), '短参数单源不应有原文切换');
  },
  'dual-short-sections': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '# 任务说明\n', args: ['--flag', 'v'] } });
    ok(html.includes('<b class="md-log-label">任务说明（stdin）</b>'), 'stdin 小节标题');
    ok(html.includes('<b class="md-log-label">命令行参数（args · 2 段）</b>'), 'args 小节标题带段数');
    ok((html.match(/class="md-body"/g) || []).length === 1, '仅 stdin 走 md');
    ok(html.includes('cmd-args'), '短参数并存仍胶囊');
    ok(html.includes('data-act="prompt-raw"'), '并存共享一个原文切换');
  },
  'dual-long-sections': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '# 说明', args: ['## 参数细则\n- a\n- b'] } });
    ok((html.match(/class="md-body"/g) || []).length === 2, '双源各一 md 块');
    ok(html.includes('任务说明（stdin）') && html.includes('命令行参数（args · 1 段）'), '双小节标题');
    ok(html.includes('<h2 class="md-h md-h2">参数细则</h2>'), 'args 文本应 md 渲染');
  },
  'dual-raw-single-pre': () => {
    S.drawer.promptRaw = true;
    const html = promptZoneHtml({ input: { stdin: '# 说明', args: ['--flag', 'v'] } });
    ok(html.includes('当前为原始输入。'), 'raw 态文案不变');
    ok((html.match(/<pre>/g) || []).length === 1, 'raw 态单 pre 全部原文');
    ok(html.includes('stdin:\n# 说明\n\nargs:\n--flag\nv'), 'stdin/args 行前缀齐备');
    ok(!html.includes('md-body') && !html.includes('cmd-args'), 'raw 态无渲染/胶囊形态');
    S.drawer.promptRaw = false;
  },
  'single-stdin-no-subtitle': () => {
    S.drawer.promptRaw = false;
    const html = promptZoneHtml({ input: { stdin: '# 纯 stdin', args: [] } });
    ok(!html.includes('md-log-label'), '单源不显示小节标题');
    ok(html.includes('<div class="md-body">'), 'stdin md 渲染');
    ok(html.includes('data-act="prompt-raw"'), '原文切换在场（现状）');
  },
  'single-args-md-raw': () => {
    S.drawer.promptRaw = true;
    const a = 'z'.repeat(301);
    const html = promptZoneHtml({ input: { stdin: '', args: [a] } });
    ok(html.includes('当前为原始输入。'), 'raw 态文案不变');
    ok((html.match(/<pre>/g) || []).length === 1, '单源 args raw 单 pre');
    ok(html.includes(a), '原文完整');
    S.drawer.promptRaw = false;
  },
  'both-empty-omit': () => {
    S.drawer.promptRaw = false;
    ok(promptZoneHtml({ input: { stdin: '', args: [] } }) === '', '皆空分区省略');
    ok(promptZoneHtml({}) === '', '无 input 字段分区省略');
  },
  'esc-discipline': () => {
    const evil = '<img src=x onerror=alert(1)>';
    const md = promptZoneHtml({ input: { stdin: '', args: [evil + '\n\n正文'] } });
    ok(!md.includes('<img'), 'md 形态不得有 HTML 直通');
    ok(md.includes('&lt;img'), 'md 形态应转义字面显示');
    S.drawer.promptRaw = true;
    const raw = promptZoneHtml({ input: { stdin: '', args: [evil + '\n\n正文'] } });
    ok(!raw.includes('<img') && raw.includes('&lt;img'), '原文态经 esc 转义');
    S.drawer.promptRaw = false;
  },
  'toggle-state-stable': () => {
    S.drawer.promptRaw = true;
    const t = { input: { stdin: '# 说明', args: ['a'] } };
    ok(promptZoneHtml(t) === promptZoneHtml(t), '同态两次产出一致（分区 diff 可跳过写 DOM，原文态跨轮询保持）');
    ok(promptZoneHtml(t).includes('当前为原始输入。'), 'promptRaw 由调用方持有，函数不重置');
    S.drawer.promptRaw = false;
    ok(!promptZoneHtml(t).includes('当前为原始输入。'), '切回渲染态');
  },
  'fold-default-over-threshold': () => {
    S.drawer.promptRaw = false;
    S.drawer.promptExpanded = false;
    const long = '# 长提示词\n\n' + '内容行。'.repeat(300);
    const html = promptZoneHtml({ input: { stdin: '', args: [long] } });
    ok(html.includes('<div class="prompt-preview">'), '默认折叠容器（无 prompt-open 类）');
    ok(html.includes('<div class="prompt-fade"></div>'), '折叠态底部渐隐遮罩在场');
    ok(html.includes('data-act="prompt-expand"'), '展开按钮（prompt-expand 动作）');
    ok(html.includes('>展开全部</button>'), '按钮文案「展开全部」');
    ok(!html.includes('>收起</button>'), '折叠态不出现「收起」');
    ok(html.includes('<h1 class="md-h md-h1">长提示词</h1>'), '折叠容器内仍走 md 渲染（形式不变只加折叠层）');
    ok(html.includes('data-act="prompt-raw"'), '原文切换保留');
  },
  'fold-boundary-at-threshold': () => {
    S.drawer.promptRaw = false;
    S.drawer.promptExpanded = false;
    const body = 'a'.repeat(1200);
    const at = promptZoneHtml({ input: { stdin: '', args: [body] } });
    ok(!at.includes('prompt-preview') && !at.includes('prompt-fade') && !at.includes('prompt-expand'), '恰 1200 字符全显（现状形态，无折叠无按钮）');
    const over = promptZoneHtml({ input: { stdin: '', args: [body + 'b'] } });
    ok(over.includes('<div class="prompt-preview">') && over.includes('data-act="prompt-expand"'), '1201 字符默认折叠');
    const dual = promptZoneHtml({ input: { stdin: 'x'.repeat(700), args: ['y'.repeat(700)] } });
    ok(dual.includes('<div class="prompt-preview">'), 'stdin+args 合并计长超阈值即折叠');
    ok((dual.match(/prompt-expand/g) || []).length === 1, '双源折叠一个按钮控制全部');
    ok(dual.includes('任务说明（stdin）') && dual.includes('命令行参数（args · 1 段）'), '双源小节结构不变');
  },
  'fold-expand-collapse-cycle': () => {
    S.drawer.promptRaw = false;
    const long = '# 长提示词\n\n' + '内容行。'.repeat(300);
    const t = { input: { stdin: '', args: [long] } };
    S.drawer.promptExpanded = true;
    const open = promptZoneHtml(t);
    ok(open.includes('prompt-preview prompt-open'), '展开态容器带 prompt-open');
    ok(!open.includes('prompt-fade'), '展开态遮罩不渲染');
    ok(open.includes('data-act="prompt-expand"'), '展开态仍有切换按钮');
    ok(open.includes('>收起</button>') && !open.includes('>展开全部</button>'), '按钮文案变「收起」');
    S.drawer.promptExpanded = false;
    const folded = promptZoneHtml(t);
    ok(folded.includes('<div class="prompt-preview">') && folded.includes('prompt-fade'), '收起回折叠态');
    ok(folded === promptZoneHtml(t), '同态产出一致（展开态从状态算 HTML，轮询 diff 可跳过）');
  },
  'fold-raw-shares-expand': () => {
    S.drawer.promptRaw = true;
    S.drawer.promptExpanded = false;
    const long = '# 长提示词\n\n' + '内容行。'.repeat(300);
    const t = { input: { stdin: '', args: [long] } };
    const rawFolded = promptZoneHtml(t);
    ok(rawFolded.includes('<div class="prompt-preview">') && rawFolded.includes('<pre>') && rawFolded.includes('prompt-fade'), '原文态同样折叠（pre 在折叠容器内）');
    S.drawer.promptExpanded = true;
    const rawOpen = promptZoneHtml(t);
    ok(rawOpen.includes('prompt-preview prompt-open') && rawOpen.includes('<pre>') && !rawOpen.includes('prompt-fade'), '原文态展开全显');
    S.drawer.promptRaw = false;
    const renderOpen = promptZoneHtml(t);
    ok(renderOpen.includes('prompt-preview prompt-open'), '展开态跨 raw/渲染共享同一个 promptExpanded');
    S.drawer.promptExpanded = false;
    ok(promptZoneHtml(t).includes('<div class="prompt-preview">'), '复位回折叠');
  },
  'fold-short-no-preview': () => {
    S.drawer.promptRaw = false;
    S.drawer.promptExpanded = false;
    const html = promptZoneHtml({ input: { stdin: '', args: ['# 部署提示词\n\n**目标**：短文本'] } });
    ok(!html.includes('prompt-preview') && !html.includes('prompt-fade'), '短内容无折叠容器无遮罩');
    ok(!html.includes('prompt-expand'), '短内容无展开按钮（现状形态）');
    S.drawer.promptRaw = true;
    const rawHtml = promptZoneHtml({ input: { stdin: '# 说明', args: ['--flag', 'v'] } });
    S.drawer.promptRaw = false;
    ok(!rawHtml.includes('prompt-preview') && !rawHtml.includes('prompt-expand'), '短内容原文态同样不折叠');
  },
};
const names = process.argv.slice(2);
if (!names.length) { console.error('用法: node prompt_zone_harness.js <scenario>...'); process.exit(2); }
for (const n of names) {
  const fn = SCENARIOS[n];
  if (!fn) { console.error('未知场景: ' + n); process.exit(2); }
  fn();
  console.log('PASS ' + n);
}
`
