package localapi

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 旧锚点/深链兼容 + 版本状态归位锚（W08-03 工作块 2/3/5）。
// 行为面：node app.js 经 node 子进程在裸 vm 里按「桩 + app.js + 驱动」全文件求值
//（与 hub web 包 consoleHarnessRunner 同构的 runner；harness 与场景判据随
// web_route_compat_behavior_test.js 维护）——#/sessions/{id}、#/spaces/{id}、#/mcp
// 深链直达、未知 hash 回落总览（不 404 不白屏）、setup 形态深链收敛向导页、T3 任务
// 页会话迁移说明卡经真实点击委托跳转「会话」页、goSessionDetail 旧入口兼容跳转。
// 源码面：hash 接线/未知回落/兼容跳转/版本状态归位（server_version → 页脚 v<版本>
// 双端同构）常驻兜底。node 不可得的环境自动 skip，源码锚点锁仍生效。

// nodeRouteHarnessRunner 与 hub web 包 consoleHarnessRunner 同构的裸 vm runner：
// 按「桩 + app.js + 驱动」顺序求值（同一脚本共享词法作用域），逐场景执行并要求
// PASS——场景内断言抛错即 node 非零退出，Go 侧以退出码 + PASS 标记判定。
// （node app.js 求值面多 localStorage/navigator/requestAnimationFrame 三桩。）
const nodeRouteHarnessRunner = `// runner：裸 vm 上下文里按「桩 + app.js + 驱动」顺序求值，逐场景执行并要求 PASS。
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
  requestAnimationFrame: fn => setTimeout(fn, 0), cancelAnimationFrame: t => clearTimeout(t),
});
new vm.Script(source, { filename: 'node-console-harness.js' }).runInContext(ctx);
for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0));   // boot() 微观任务链落地
if (typeof ctx.__hs_run !== 'function') throw new Error('harness 未挂 __hs_run（app.js 顶层求值中断？）');
for (const s of scenarios) console.log(await ctx.__hs_run(s));
`

// TestWebRouteCompatBehavior 旧锚点/深链兼容行为断言（W08-03 回归锚；场景判据随
// web_route_compat_behavior_test.js 维护，逐条含义见该文件头注）。
func TestWebRouteCompatBehavior(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：行为断言跳过（源码锚点锁仍生效）")
	}
	scenarios := []string{
		"deep-link-session-detail",           // #/sessions/{id} 直达详情页（W01-05 P02）
		"deep-link-space-detail",             // #/spaces/{id} 直达详情页（W04-03 P04）
		"deep-link-mcp-subpage",              // #/mcp 直达 MCP 接入子页（W03-04 P07）
		"unknown-hash-falls-back",            // 未知 hash 回落总览（不 404 不白屏）
		"setup-mode-converges-wizard",        // setup 形态深链收敛向导页
		"legacy-task-page-migrate-card-jump", // T3 任务页会话迁移卡 →「会话」页
		"legacy-go-session-detail",           // 旧入口 goSessionDetail 兼容跳转
		"nav-renders-resource-entry",         // W08-03 导航词迁移呈现 + #/services 深链存活
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.mjs")
	if err := os.WriteFile(runner, []byte(nodeRouteHarnessRunner), 0o600); err != nil {
		t.Fatalf("写 runner: %v", err)
	}
	harness := filepath.Join(dir, "harness.js")
	if b, err := os.ReadFile(filepath.Join("web_route_compat_behavior_test.js")); err != nil {
		t.Fatalf("读 harness: %v", err)
	} else if err := os.WriteFile(harness, b, 0o600); err != nil {
		t.Fatalf("写 harness: %v", err)
	}
	app := filepath.Join(dir, "app.js")
	if err := os.WriteFile(app, []byte(fetchAsset(t, "/app.js")), 0o600); err != nil {
		t.Fatalf("写 app.js: %v", err)
	}
	cmd := exec.Command(nodeBin, append([]string{runner, app, harness}, scenarios...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("旧锚点/深链兼容行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// TestWebRouteCompatSources 旧锚点/深链兼容源码锚（无 node 环境兜底）。
func TestWebRouteCompatSources(t *testing.T) {
	app := fetchAsset(t, "/app.js")
	for _, want := range []string{
		"window.addEventListener('hashchange', route);",                        // hash 驱动接线
		"allowed.some(p => p[0] === ph) ? ph : allowed[0][0]",                  // 未知 hash 回落首入口（不 404）
		`location.hash = '#/sessions/' + encodeURIComponent(id);   // 旧入口兼容跳转`, // W01-05 旧锚点全走详情页
		`data-go="sessions"`,         // 任务页迁移说明卡跳转入口（T3 旧锚点可跳转）
		"function sessMigrateCard()", // 迁移说明卡（任务页旧会话区块位置）
	} {
		if !strings.Contains(app, want) {
			t.Errorf("node app.js 缺旧锚点/深链兼容锚点 %q", want)
		}
	}
}

// TestWebVersionAndEntryStateAnchors 版本状态归位锚（W08-03 工作块3：版本状态=
// server_version/构建信息如实呈现——双端页脚 versionFoot 消费 API 下发值，禁止
// 硬编码；值下发链路已由 hub api account_test.go / node local.go 锁定，本锁固化
// web 呈现面消费接线）。通知归位（hub 设置页分区 / node 服务页卡片）与设置域入口
// 已由 TestWebNotifyFormSharedBlock 等既有锁覆盖，此处不重复。
func TestWebVersionAndEntryStateAnchors(t *testing.T) {
	nodeJS := fetchAsset(t, "/app.js")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"function versionFoot()", // 双端同构页脚版本函数
		`id="server-version"`,    // 页脚锚（值驱动更新挂点）
		"server_version",         // 数据源字段（禁硬编码版本号）
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 缺版本状态锚点 %q", want)
		}
		if !strings.Contains(string(hubJS), want) {
			t.Errorf("hub app.js 缺版本状态锚点 %q", want)
		}
	}
	// 版本号不得硬编码进资产（v 后必为占位/动态拼接——出现 v1.2.3 形态字面量即回归）。
	for side, src := range map[string]string{"node": nodeJS, "hub": string(hubJS)} {
		if strings.Contains(src, ">v1.") || strings.Contains(src, "'v1.'") {
			t.Errorf("%s app.js 版本号疑似硬编码（应取 server_version 下发值）", side)
		}
	}
}
