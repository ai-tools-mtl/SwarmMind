// 源打开原语安全单测（2026-10-24 复审 R1，POSIX 形态）：openFinal/OpenSourceFile
// 的内核级不跟随与不阻塞——Lstat 预检拦得住静态形态，拦不住 Lstat→Open 窗口内
// 换入的符号链接（os.Root 允许根内链接）与换入的无写端 FIFO（open 挂起则 fd 校
// 验无从执行）；两者必须由 open 原语本身（O_NOFOLLOW|O_NONBLOCK）承担。FIFO/
// mkfifo 为 unix 语义（windows 无此攻击面，旗标恒 0），故本文件 unix 限定。
//go:build unix

package inputs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// boundedRun 防阻塞回归护栏：fn 5s 内未返回即判失败（FIFO open 挂起的旧病症状
// ——hub 超时解不开源 node 的串行帧阻塞，必须由 O_NONBLOCK 保证 open 立即返回）。
func boundedRun(t *testing.T, what string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s：5s 未返回——打开被阻塞（O_NONBLOCK 缺失）", what)
		return nil
	}
}

// mustFailWith 断言失败且文案含 want 子串。
func mustFailWith(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("应拒绝（%s）", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("文案应含 %q，got: %v", want, err)
	}
}

// TestOpenSourceFileKernelNoFollow 末段 TOCTOU 窗口（复审 R1）：等价「Lstat 预检
// 时还是常规文件、检查后被换入符号链接」——Unix 实现全程无 Lstat 预检（检查即
// 打开的 openat 游走），直接以符号链接路径调用 openSourceFile（模拟攻击者赢下任
// 何窗口），内核 O_NOFOLLOW 必须在 open 时拒绝；链接目标本身可打开（常规文件），
// 证明拒绝来自不跟随而非目标不可达。
func TestOpenSourceFileKernelNoFollow(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(target); err != nil {
		t.Fatalf("链接目标应可打开（证明拒绝来自不跟随）: %v", err)
	} else {
		_ = f.Close()
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "art.txt")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := openSourceFile(dir, "art.txt"); err == nil {
		_ = f.Close()
		t.Fatal("末段换入符号链接应在 open 时被 O_NOFOLLOW 拒绝")
	}
}

// TestOpenSourceFileRejectsFIFO 登记文件被换为无写端 FIFO（复审 R1）：旧实现
// os.Open 挂起（fd 校验无从执行，hub 超时解不开源 node 串行帧阻塞）——O_NONBLOCK
// 打开立即返回，fstat 以非常规文件拒绝（快速失败 + 不卡死调用方）。
func TestOpenSourceFileRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "outputs", "t1", "f.txt")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	err := boundedRun(t, "无写端 FIFO 读取", func() error {
		f, _, ferr := OpenSourceFile(dir, "outputs/t1/f.txt")
		if ferr == nil {
			_ = f.Close()
		}
		return ferr
	})
	mustFailWith(t, err, "非常规")
}

// TestOpenSourceFileRejectsInRootIntermediateSymlink 中间目录换入根内链接：每段
// openat 锚定上一段目录 fd 且 O_NOFOLLOW——链接在 open 时被内核拒绝（即便链接
// 目标内容与登记一致：拒的是路径形态，不是内容）。
func TestOpenSourceFileRejectsInRootIntermediateSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "outputs", "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "f.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "outputs", "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err := OpenSourceFile(dir, "outputs/link/f.txt")
	mustFailWith(t, err, "中间段")
}
