// 源打开原语 Windows 形态单测（2026-10-25 三审 W1/W2）：逐段 NtCreateFile+
// OBJ_DONT_REPARSE 游走的前缀累计与「打开时约束 reparse/link 解析」。junction
// 经 cmd mklink /J 创建（无需特权）；符号链接需 SeCreateSymbolicLinkPrivilege
// （开发者模式/管理员），无特权时跳过对应子测试。本文件为 windows 构建标签，
// 在 Windows 机器/CI 执行（开发环境 darwin 仅验证构建与 vet）。
//go:build windows

package inputs

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeJunction 无特权创建 junction（mount point reparse point）。
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("创建 junction 失败: %v (%s)", err, out)
	}
}

// makeSymlink 创建符号链接；无特权时跳过当前测试（受限处如实声明：符号链接
// 形态由具备特权的 Windows CI/机器覆盖，junction 用例不受此限）。
func makeSymlink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("创建符号链接失败（需开发者模式/特权）: %v", err)
	}
}

// mustRejectWith 断言失败且文案含 want 子串。
func mustRejectWith(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("应拒绝（%s）", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("文案应含 %q，got: %v", want, err)
	}
}

// TestOpenSourceFileWindowsNestedPrefix 前缀累计（三审 W1 功能回归锁）：多层
// 嵌套 outputs/t1/sub/a.txt 逐段锚定打开可读。旧实现逐段 Lstat 未累计前缀——
// 第二段起以根直接子名检查恒失败，Windows 上 Local/Remote 源读取基本不可用。
func TestOpenSourceFileWindowsNestedPrefix(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "outputs", "t1", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, fi, err := openSourceFile(dir, "outputs/t1/sub/a.txt")
	if err != nil {
		t.Fatalf("嵌套前缀应逐段累计打开: %v", err)
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		t.Fatalf("应为常规文件: %v", fi.Mode())
	}
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "payload" {
		t.Fatalf("打开的 fd 应可读出登记内容: %v %q", err, b)
	}
}

// TestOpenSourceFileWindowsRejectsJunction reparse point 打开时拒绝（三审 W2，
// junction 无需特权）：末段与中间段的 junction（目标为根内真实目录、内容形态
// 完全合法）都在 open 时被 OBJ_DONT_REPARSE 拒绝——拒的是路径形态不是内容，
// os.Root 在同形态下会读链跟随（根内链接），本契约必须更严。
func TestOpenSourceFileWindowsRejectsJunction(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "outputs", "t1", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "f.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(dir, "outputs", "t1", "jlink"), real)

	// 末段 junction：即便目标内容与登记一致也拒绝（不跟随）
	f, _, err := openSourceFile(dir, "outputs/t1/jlink")
	if err == nil {
		_ = f.Close()
		t.Fatal("末段 junction 应在打开时被拒绝（OBJ_DONT_REPARSE）")
	}
	mustRejectWith(t, err, "拒绝跟随")

	// 中间段 junction：拒绝且锚定关系不受影响
	_, _, err = openSourceFile(dir, "outputs/t1/jlink/f.txt")
	mustRejectWith(t, err, "中间段")

	// 根外逃逸 junction：同样拒绝（不跟随=一律拒，非逃逸检测口径）
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(dir, "outputs", "t1", "jout"), outside)
	if f, _, err := openSourceFile(dir, "outputs/t1/jout/x.txt"); err == nil {
		_ = f.Close()
		t.Fatal("根外 junction 中间段应拒绝")
	}
}

// TestOpenSourceFileWindowsRejectsSymlink 符号链接形态（三审 W2，需特权，无则
// 跳过）：根内相对链接正是 os.Root 会读链跟随的形态——本契约必须在 open 时
// 拒绝，且证明拒绝来自不跟随（链接目标自身可打开）。
func TestOpenSourceFileWindowsRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "outputs", "t1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outputs", "t1", "real.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(filepath.Join(dir, "outputs", "t1", "real.txt")); err != nil {
		t.Fatalf("链接目标应可打开（证明拒绝来自不跟随）: %v", err)
	} else {
		_ = f.Close()
	}
	makeSymlink(t, filepath.Join(dir, "outputs", "t1", "art.txt"), "real.txt")

	f, _, err := openSourceFile(dir, "outputs/t1/art.txt")
	if err == nil {
		_ = f.Close()
		t.Fatal("末段符号链接应在打开时被拒绝（os.Root 会跟随根内链接，本契约更严）")
	}
	mustRejectWith(t, err, "拒绝跟随")

	// 中间段符号链接：链接指向**目录**（相对根内目标），打开其子文件——链接组
	// 件位于中间段（四审 V3：旧形态 ldir 实为末段且目标为文件，未覆盖中间段）
	if err := os.MkdirAll(filepath.Join(dir, "outputs", "t1", "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outputs", "t1", "realdir", "f.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeSymlink(t, filepath.Join(dir, "outputs", "t1", "ldir"), "realdir")
	_, _, err = openSourceFile(dir, "outputs/t1/ldir/f.txt")
	mustRejectWith(t, err, "中间段")
}
