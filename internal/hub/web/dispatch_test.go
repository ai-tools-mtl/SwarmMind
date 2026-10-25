package web

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsoleDispatchSuggestSources 派发建议 UI 来源锁（P5.1 D1，TESTPLAN-D1-jev D1）：
// 派单页建议区挂点与采纳动线必须在场——「建议派给 X（本地决策模型）」一键采纳
// （建议可覆盖不强制；无建议时采纳按钮不出现，人话 note 转述），建议区局部更新
// 不整页重绘（表单保护同款纪律）。本能力为 hub 控制台独有（决策服务 hub 内嵌），
// 不触碰 shared block（双端一致由 node 端既有锁测试回归证明）。
func TestConsoleDispatchSuggestSources(t *testing.T) {
	app := readRepo(t, "app.js")
	for _, anchor := range []string{
		"/api/v1/dispatch-suggest", // 建议数据源端点
		"ctrl-suggest",             // 获取/重试动作
		"ctrl-adopt",               // 一键采纳动作
		"ctrl-suggest-line",        // 建议区容器（局部更新挂点）
		"suggestLineHtml",          // 四态渲染
		"paintSuggestLine",         // 局部更新唯一入口
		"建议派给",                    // 人话文案（RFC 四.2 口径）
		"本地决策模型",                  // 来源标签（RFC v1.1 裁决②：建议+来源先行）
		"可覆盖",                      // 建议仅供参考可覆盖
		"再试一次",                    // 无建议出口（重试入口）
	} {
		if !strings.Contains(app, anchor) {
			t.Fatalf("app.js 缺建议区锚点 %q", anchor)
		}
	}
	// 采纳动作必须回填两级下拉（S.C.node/agent），不是只弹提示。
	if !strings.Contains(app, "S.C.node = sg.target_node") || !strings.Contains(app, "S.C.agent = sg.target_agent") {
		t.Fatal("ctrl-adopt 应回填两级下拉选择（建议可覆盖：采纳后照常改选）")
	}
}

// TestConsoleDispatchSuggestFingerprint 描述指纹行为断言（codex 审 D1 ②复审重写：
// 原 strings.Contains 源码锚点检查升级为真代码行为验证）——经 node 子进程在桩 DOM
// 里加载 app.js 全文，捕获全局 click/input 委托处理器实驱动（非源码匹配），四场景：
// 输入失效（复现序列：翻译→改编译失败→旧采纳按钮不残留）、迟到响应丢弃、
// 采纳点击重验拒绝（DOM 真相为准）、建议区按钮不触发表单提交（五态渲染零缺 type）。
// harness（桩+驱动）与场景判据：同目录 dispatch_suggest_behavior_test.js，runner 由
// consoleHarnessRunner 生成。node 不可得的环境自动 skip——源码锚点锁
//（TestConsoleDispatchSuggestSources / TestConsoleDispatchSuggestButtonType）仍常驻兜底。
func TestConsoleDispatchSuggestFingerprint(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：行为断言跳过（源码锚点锁仍生效）")
	}
	scenarios := []string{"stale-input", "late-response", "adopt-reject", "button-no-submit"}
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.mjs")
	if err := os.WriteFile(runner, []byte(consoleHarnessRunner), 0o600); err != nil {
		t.Fatalf("写 runner: %v", err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(readRepo(t, "dispatch_suggest_behavior_test.js")), 0o600); err != nil {
		t.Fatalf("写 harness: %v", err)
	}
	app := filepath.Join(dir, "app.js")
	if err := os.WriteFile(app, []byte(readRepo(t, "app.js")), 0o600); err != nil {
		t.Fatalf("写 app.js: %v", err)
	}
	cmd := exec.Command(node, append([]string{runner, app, harness}, scenarios...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("建议区行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// consoleHarnessRunner 行为断言 runner：裸 vm 上下文里按「桩 + app.js + 驱动」顺序
// 求值（同一脚本共享词法作用域），逐场景执行并要求逐条 PASS——场景内断言抛错即
// node 非零退出，Go 侧以退出码 + PASS 标记判定。
const consoleHarnessRunner = `// runner：裸 vm 上下文里按「桩 + app.js + 驱动」顺序求值，逐场景执行并要求 PASS。
// 用法：node runner.mjs <app.js> <harness.js> <scenario>...
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const [, , appPath, harnessPath, ...scenarios] = process.argv;
if (!appPath || !harnessPath || !scenarios.length) {
  console.error('用法: node runner.mjs <app.js> <harness.js> <scenario>...');
  process.exit(2);
}
const MARK = '/* ==== APP.JS INSERTED BELOW BY RUNNER ==== */';
const harness = readFileSync(harnessPath, 'utf8');
const cut = harness.indexOf(MARK);
if (cut < 0) throw new Error('harness 缺 app 插入标记');
const source = harness.slice(0, cut) + '\n' + readFileSync(appPath, 'utf8') + '\n' + harness.slice(cut + MARK.length);

// init() 的 render 路径如有桩面缺口会以未处理拒绝炸进程——留痕放行，场景断言是判据
//（桩缺口会在场景里以确定性方式暴露）。
process.on('unhandledRejection', e => console.error('[unhandledRejection]', (e && e.message) || e));

const ctx = vm.createContext({
  console, setTimeout, clearTimeout,
  setInterval: () => ({ unref() {} }), clearInterval() {},
  queueMicrotask, AbortController,
});
new vm.Script(source, { filename: 'hub-console-harness.js' }).runInContext(ctx);
for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0));   // init() 微观任务链落地
if (typeof ctx.__hs_run !== 'function') throw new Error('harness 未挂 __hs_run（app.js 顶层求值中断？）');
for (const s of scenarios) console.log(await ctx.__hs_run(s));
`

// TestConsoleDispatchSuggestButtonType 建议区按钮误提交拦截（codex 审 D1 ⑤，
// D 组走查实bug回归锁；行为级四场景锁见 TestConsoleDispatchSuggestFingerprint
// 场景四，本源码锁为无 node 环境兜底）：建议区全部按钮必须 type="button"——
// 表单内缺省 submit，点击建议动作连带触发整表提交。失效态重试按钮随描述指纹
// 复审改为条件文案（失效说明「请重新获取」→按钮同词「重新获取」，与渲染态
// 闸门呈现一致；无建议/降级仍「再试一次」）。
func TestConsoleDispatchSuggestButtonType(t *testing.T) {
	app := readRepo(t, "app.js")
	for _, anchor := range []string{
		`<button type="button" data-act="ctrl-adopt"`, // 采纳按钮
		`button('重新获取', 'ctrl-suggest', 'link', 'type="button"')`, // 渲染态闸门失效出口
		`button(sg.note === SUGGEST_STALE_NOTE ? '重新获取' : '再试一次', 'ctrl-suggest', 'link', 'type="button"')`, // note 态（失效/再试条件文案）
		`button('获取派发建议（本地决策模型）', 'ctrl-suggest', 'link', 'type="button"')`,                            // 入口
	} {
		if !strings.Contains(app, anchor) {
			t.Fatalf("app.js 建议区按钮缺 type=\"button\" 锚点 %q", anchor)
		}
	}
}
