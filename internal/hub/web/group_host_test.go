package web

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsoleGroupHostSnapshotReplace 群主持状态快照落地行为断言（DESIGN-butler v4
// 审查修复③，codex ui-repro 等效固化）：pullGroupMessages 收到的群快照必须整对象
// 落地——停轮快照（omitempty 省略 host_turn/host_turn_member）到达后，旧的在途
// 状态（任务 ID + 成员）不得残留在本地群对象（旧 {...旧,...新} 合并行为下两条
// 宿主断言转红）；在途但成员查不到（任务清理/派发窗口）按「第 N 轮」降级呈现、
// 不虚构成员。两场景经 node 子进程在桩 DOM 里实驱动 pullGroupMessages /
// paintGroupHost / hostSig（非源码匹配），harness 与场景判据：同目录
// group_host_behavior_test.js，runner 复用 dispatch_test.go 的 consoleHarnessRunner。
// node 不可得的环境自动 skip——源码锚点锁（下方 TestGroupHostSnapshotReplaceSources）
// 仍常驻兜底。
func TestConsoleGroupHostSnapshotReplace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可得：行为断言跳过（源码锚点锁仍生效）")
	}
	scenarios := []string{"stopped-snapshot-clears-turn", "member-lookup-missing-degrades"}
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.mjs")
	if err := os.WriteFile(runner, []byte(consoleHarnessRunner), 0o600); err != nil {
		t.Fatalf("写 runner: %v", err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(readRepo(t, "group_host_behavior_test.js")), 0o600); err != nil {
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
		t.Fatalf("群主持状态行为断言失败（node 非零退出）：%v\n%s", err, out.String())
	}
	for _, sc := range scenarios {
		if !strings.Contains(out.String(), "PASS "+sc) {
			t.Fatalf("行为断言缺场景结果 %s：\n%s", sc, out.String())
		}
	}
}

// TestGroupHostSnapshotReplaceSources 快照落地源码锚点锁（无 node 环境兜底）：
// pullGroupMessages 的群快照必须是整对象替换——合并式 {...旧,...新} 会让 omitempty
// 省略的停轮字段残留旧值（v4 审查修复③的问题根源）；分区 diff 更新入口
// paintGroupHost 必须在场（D22 纪律：替换数据源不回退到整层重绘）。
func TestGroupHostSnapshotReplaceSources(t *testing.T) {
	app := readRepo(t, "app.js")
	if !strings.Contains(app, "if (i >= 0) S.groups[i] = gv;") {
		t.Fatal("pullGroupMessages 群快照必须整对象替换（S.groups[i] = gv），禁止 {...旧,...新} 合并——停轮字段 omitempty 省略时合并残留旧值")
	}
	if strings.Contains(app, "{ ...S.groups[i], ...gv }") || strings.Contains(app, "{...S.groups[i], ...gv}") {
		t.Fatal("pullGroupMessages 群快照禁止合并式更新（停轮 host_turn 清空字段不会覆盖旧值）")
	}
	for _, anchor := range []string{
		"paintGroupHost()", // 分区 diff 更新入口（D22：局部刷主持状态分区）
		"hostSig(g)",       // 签名 diff——值不变不碰 DOM
	} {
		if !strings.Contains(app, anchor) {
			t.Fatalf("app.js 缺主持状态分区锚点 %q", anchor)
		}
	}
}
