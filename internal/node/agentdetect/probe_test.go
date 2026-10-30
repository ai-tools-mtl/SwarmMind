// 安装层 --version 子进程纪律测试（全 fake：临时 shell 脚本，绝不调用真实
// codex/zcode，绝无网络）。
package agentdetect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript 造临时可执行 shell 脚本（全 fake 探测目标：绝不调用真实
// codex/zcode）。
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	return path
}

// TestProbeVersionSuccess 探测成功：输出原样留痕（不解析）。
func TestProbeVersionSuccess(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "fake-cli version 1.2.3"`)
	res := probeVersion(context.Background(), cmd, 2*time.Second, 1024)
	if res.Timeout || res.Canceled || res.StartErr != nil || res.NonZero || res.Truncated || res.WaitErr != nil {
		t.Fatalf("探测应成功，实得 %+v", res)
	}
	if !strings.Contains(res.Raw, "fake-cli version 1.2.3") {
		t.Fatalf("输出应原样留痕，实得 %q", res.Raw)
	}
}

// TestProbeVersionTimeout 超时：判 Timeout（进程 kill 后已 Wait 收尸，不验证
// 僵尸数——WaitDelay 兜底返回即证明未挂死）。
func TestProbeVersionTimeout(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "slow-cli", "sleep 5")
	res := probeVersion(context.Background(), cmd, 150*time.Millisecond, 1024)
	if !res.Timeout {
		t.Fatalf("应判超时，实得 %+v", res)
	}
}

// TestProbeVersionNonZero 非零退出：退出码保留，stderr 合流留痕。
func TestProbeVersionNonZero(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fail-cli", "echo boom >&2\nexit 3")
	res := probeVersion(context.Background(), cmd, 2*time.Second, 1024)
	if !res.NonZero || res.ExitCode != 3 {
		t.Fatalf("应判非零退出且退出码 3，实得 %+v", res)
	}
	if !strings.Contains(res.Raw, "boom") {
		t.Fatalf("stderr 应合流留痕，实得 %q", res.Raw)
	}
}

// TestProbeVersionOverLimit 输出超限：截断注记 + 恰好保留上限长度前缀。
func TestProbeVersionOverLimit(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "loud-cli",
		"printf '%064d\\n' 1; printf '%064d\\n' 2; printf '%064d\\n' 3; printf '%064d\\n' 4")
	res := probeVersion(context.Background(), cmd, 2*time.Second, 64)
	if !res.Truncated {
		t.Fatalf("应判超限截断，实得 %+v", res)
	}
	if int64(len(res.Raw)) != 64 {
		t.Fatalf("截断后应恰为上限长度 64，实得 %d", len(res.Raw))
	}
}

// TestProbeVersionStartFailure 非可执行文件：启动失败如实报（不伪装成功）。
func TestProbeVersionStartFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-exec")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := probeVersion(context.Background(), path, 2*time.Second, 1024)
	if res.StartErr == nil {
		t.Fatalf("无执行位应启动失败，实得 %+v", res)
	}
}

// TestDetectInstalledSymlinkAnnotated 符号链接命令：探测通过且如实注记 symlink
// （Lstat 不跟随；exec 按调用方给出的路径原样执行，本包不展开）。
func TestDetectInstalledSymlinkAnnotated(t *testing.T) {
	real := writeScript(t, t.TempDir(), "real-cli", `echo "real 1.0"`)
	link := filepath.Join(t.TempDir(), "linked-cli")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("造符号链接失败: %v", err)
	}
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostZcode, Command: link})
	if rep.Installed.Status != StatusPass {
		t.Fatalf("符号链接命令应照常探测通过，实得 %+v", rep.Installed)
	}
	if len(rep.Installed.Paths) != 1 || !rep.Installed.Paths[0].Symlink || !rep.Installed.Paths[0].Exists {
		t.Fatalf("应如实注记 symlink=true 且 exists=true，实得 %+v", rep.Installed.Paths)
	}
	if !strings.Contains(rep.Installed.Reason, "符号链接") {
		t.Fatalf("Reason 应含符号链接注记，实得 %q", rep.Installed.Reason)
	}
}

// TestDetectInstalledRejectsRelativeCommand 裸名命令拒绝：杜绝 exec 隐式 PATH
// 搜索（不构成安装证据，也不构成扫描）。
func TestDetectInstalledRejectsRelativeCommand(t *testing.T) {
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostCodex, Command: "codex"})
	if rep.Installed.Status != StatusFail {
		t.Fatalf("裸名命令应拒绝，实得 %+v", rep.Installed)
	}
	if !strings.Contains(rep.Installed.Reason, "绝对路径") {
		t.Fatalf("原因应说明绝对路径要求，实得 %q", rep.Installed.Reason)
	}
	if rep.Installed.Paths[0].Exists {
		t.Fatalf("拒绝路径不得声称存在，实得 %+v", rep.Installed.Paths)
	}
}

// TestDetectInstalledMissingCommand 绝对路径不存在：如实 fail。
func TestDetectInstalledMissingCommand(t *testing.T) {
	s := New(Options{})
	missing := filepath.Join(t.TempDir(), "no-such-cli")
	rep := s.Detect(context.Background(), Request{Host: HostCodex, Command: missing})
	if rep.Installed.Status != StatusFail {
		t.Fatalf("路径不存在应 fail，实得 %+v", rep.Installed)
	}
	if !strings.Contains(rep.Installed.Reason, "不存在") {
		t.Fatalf("原因应说明路径不存在，实得 %q", rep.Installed.Reason)
	}
	if rep.Installed.Paths[0].Exists {
		t.Fatalf("不存在不得记 exists=true，实得 %+v", rep.Installed.Paths)
	}
}
