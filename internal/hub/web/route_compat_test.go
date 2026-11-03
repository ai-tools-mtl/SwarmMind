package web

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsoleRouteCompatBehavior 旧锚点/深链兼容行为断言（W08-03 工作块2/5 回归
// 锚；真代码行为验证——场景判据随 route_compat_behavior_test.js 维护）：
//   - deep-link-session-detail / deep-link-space-detail / deep-link-task-status-filter /
//     deep-link-group：#/sessions/{id}、#/spaces/{id}、#/tasks/{status}、#/groups/{id}
//     经真实 hashchange 事件直达（W01-05 P02 / W07-03 P04 / 既有语义）；
//   - unknown-hash-falls-back：未知 hash 回落总览（不 404 不白屏——精简导航不产生
//     死链，W08 任务书「旧锚点可跳转」的兜底面）；
//   - legacy-session-entry-jumps：goSessionDetail 旧入口兼容跳转（W01-05 700px 会话
//     抽屉退役后的兼容面——W08 任务书 T3「旧锚点可跳转」hub 侧字面兑现）；
//   - unauthed-deep-link-hits-login：未登录深链收敛登录页（不白屏）。
//
// harness（桩+驱动）与场景：route_compat_behavior_test.js，runner 复用
// consoleHarnessRunner（同目录 dispatch_test.go）。node 不可得的环境自动 skip。
func TestConsoleRouteCompatBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：行为断言跳过")
	}
	scenarios := []string{
		"deep-link-session-detail",      // #/sessions/{id} 直达详情页
		"deep-link-space-detail",        // #/spaces/{id} 直达详情页
		"deep-link-task-status-filter",  // #/tasks/{status} 深链筛选
		"deep-link-group",               // #/groups/{id} 直达群视图
		"unknown-hash-falls-back",       // 未知 hash 回落总览（不 404 不白屏）
		"legacy-session-entry-jumps",    // 旧入口 goSessionDetail 兼容跳转
		"unauthed-deep-link-hits-login", // 未登录深链收敛登录页
		"nav-renders-resource-entry",    // W08-03 导航词迁移呈现 + #/nodes 深链存活
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.mjs")
	if err := os.WriteFile(runner, []byte(consoleHarnessRunner), 0o600); err != nil {
		t.Fatalf("写 runner: %v", err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(readRepo(t, "route_compat_behavior_test.js")), 0o600); err != nil {
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
		t.Fatalf("旧锚点/深链兼容行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// TestConsoleRouteCompatSources 旧锚点/深链兼容源码锚（无 node 环境兜底；行为级
// 锁见 TestConsoleRouteCompatBehavior）：hash 驱动接线、未知回落总览、旧入口兼容
// 跳转与空间 404 回列表的映射面必须在场。
func TestConsoleRouteCompatSources(t *testing.T) {
	app := readRepo(t, "app.js")
	for _, want := range []string{
		"window.addEventListener('hashchange', route);",                                      // hash 驱动接线
		"S.page = PAGE_IDS.includes(p) ? p : 'overview';",                                    // 未知 hash 回落总览（不 404）
		`location.hash = '#/sessions/' + encodeURIComponent(id);   // 旧入口兼容跳转`,               // W01-05 旧锚点全走详情页
		"if (r.code === 404) { toast('会话不存在'); location.hash = '#/sessions'; return; }",      // 详情失效回列表
		"if (sr && sr.code === 404) { toast('空间不存在'); location.hash = '#/spaces'; return; }", // 空间详情失效回列表
	} {
		if !strings.Contains(app, want) {
			t.Errorf("hub app.js 缺旧锚点/深链兼容锚点 %q", want)
		}
	}
}
