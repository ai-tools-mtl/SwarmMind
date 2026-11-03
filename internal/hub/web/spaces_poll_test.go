package web

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsoleSpacesPollGuards 协作空间轮询/详情守卫行为断言（W07-03 codex 审查
// 必修1/必修2 反例回归；真代码行为验证——场景判据随 spaces_poll_behavior_test.js
// 维护）：
//   - poll-picks-up-remote-change（必修1）：空间列表经 loadAll 统一轮询链——他端
//     建/归档空间后本端仅经轮询即更新（不再依赖进页一次性加载/手动重载）；
//   - first-load-failure-self-heals（必修1 残半）：首载失败进与文案一致的失败态，
//     轮询恢复后列表自动就绪；
//   - detail-no-rollback（必修2）：详情页请求序号守卫——R2(v2) 先落、R1(v1) 后到
//     不回退；A→B→A 中旧 A 应答放行也不回退；同空间双在途 v5→v4 不回退。
//
// harness（桩+驱动）与场景：spaces_poll_behavior_test.js，runner 复用
// consoleHarnessRunner（同目录 dispatch_test.go）。node 不可得的环境自动 skip。
func TestConsoleSpacesPollGuards(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：行为断言跳过")
	}
	scenarios := []string{
		"poll-picks-up-remote-change",     // 必修1：真实 pollTick 接线（他端建/归档可见）
		"first-load-failure-self-heals",   // 必修1：首载失败轮询自恢复
		"non200-same-shape-not-committed", // 必修1 复审回归：500 同形响应七资源均不提交
		"relogin-rebuilds-state",          // 必修2 复审回归：登出清理指纹（seq 保持单调），重登重建状态与入口；503 不假亮
		"logout-pending-old-request",      // 必修2 终审：登出时挂起旧请求跨会话——seq 单调不归零，OLD 不得覆盖 NEW（codex 终审反例全程）
		"detail-no-rollback",              // 必修2：详情乱序不回退（A→B→A/同空间双在途）
		"message-cursor-advances",         // 建议修B：since 游标增量推进
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.mjs")
	if err := os.WriteFile(runner, []byte(consoleHarnessRunner), 0o600); err != nil {
		t.Fatalf("写 runner: %v", err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(readRepo(t, "spaces_poll_behavior_test.js")), 0o600); err != nil {
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
		t.Fatalf("空间轮询/守卫行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// TestConsoleSpacesWiringSources 空间页轮询链接入源码锚（无 node 环境兜底）：
// spaces 必须是 loadAll 的第七资源（503 业务专态经 validate(code) 成功提交），
// 详情取数必须携请求序号守卫（必修1/必修2 的结构面）；独立 loadSpaces 已退役。
func TestConsoleSpacesWiringSources(t *testing.T) {
	app := readRepo(t, "app.js")
	for _, want := range []string{
		`loadResource('spaces', '/api/v1/spaces?include_archived=true'`, // 必修1：loadAll 第七资源（W08-02：显式包含归档——页签计数需全量视图）
		`code === 503`, // 未装配业务专态走成功提交（非陈旧失败）
		`commit('nodes', nodes); commit('tasks', tasks); commit('directory', directory); commit('prime', prime); commit('works', works); commit('sessions', sessions); commit('spaces', spaces);`,
		`S.spacesUnsupported = value.unavailable;`, // 专态随提交管道落态
		`works: 0, sessions: 0, spaces: 0 }`,       // seq 表初始化含 spaces 键（++undefined=NaN 会使 seq 校验恒真静默丢提交——实锤过真页面假绿的键缺失回归锚）
		`sessions: '', spaces: '' }`,               // resSnap 指纹表同上
		`spacePageReqSeq`,                          // 必修2：详情请求序号守卫
		`seqAtReq !== spacePageReqSeq || S.loginGen !== genAtReq`,
		`function refreshSpacesView() {`, // 动作后即时刷新统一入口（独立 loadSpaces 退役）
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js 缺空间接线锚点 %q", want)
		}
	}
	if strings.Contains(app, "async function loadSpaces()") {
		t.Fatal("独立 loadSpaces 应已退役（双加载链会绕开指纹门控）")
	}
}
