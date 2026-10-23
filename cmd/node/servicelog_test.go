// 服务日志（servicelog.go）与 SCM Stop 桥接（mergeStopChan）单测：两者均为
// 无系统调用的跨平台逻辑，mac/linux 上覆盖服务模式行为的核心纯逻辑面。
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenServiceLogCreatesAndAppends(t *testing.T) {
	dataDir := t.TempDir()
	f, err := openServiceLog(dataDir)
	if err != nil {
		t.Fatalf("打开服务日志失败: %v", err)
	}
	if _, err := f.Write([]byte("first")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	f.Close()
	f2, err := openServiceLog(dataDir)
	if err != nil {
		t.Fatalf("二次打开失败: %v", err)
	}
	defer f2.Close()
	if _, err := f2.Write([]byte("second")); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "logs", serviceLogName))
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if string(b) != "firstsecond" {
		t.Fatalf("应追加写不截断，got %q", b)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "logs", serviceLogOld)); !os.IsNotExist(err) {
		t.Fatalf("未超限不应产生旧档: %v", err)
	}
}

// 超限轮转：现有文件超阈值 → 改名 .old（旧档仅一代）→ 新文件从空追加。
func TestOpenServiceLogRotatesOversize(t *testing.T) {
	oldLimit := serviceLogRotate
	serviceLogRotate = 16
	defer func() { serviceLogRotate = oldLimit }()

	dataDir := t.TempDir()
	logPath := filepath.Join(dataDir, "logs", serviceLogName)
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("0123456789012345678"), 0o644); err != nil { // 18B > 16B
		t.Fatal(err)
	}
	f, err := openServiceLog(dataDir)
	if err != nil {
		t.Fatalf("轮转后打开失败: %v", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(dataDir, "logs", serviceLogOld))
	if err != nil || string(old) != "0123456789012345678" {
		t.Fatalf("旧档应完整保留，got %q err %v", old, err)
	}
	cur, err := os.ReadFile(logPath)
	if err != nil || string(cur) != "new" {
		t.Fatalf("新档应从空追加，got %q err %v", cur, err)
	}
}

// 写入时轮转（返工建议5）：打开时不超限，写入累计超阈值即轮转——长期运行不
// 无限增长，不依赖进程重启。
func TestServiceLogRotatesOnWrite(t *testing.T) {
	oldLimit := serviceLogRotate
	serviceLogRotate = 16
	defer func() { serviceLogRotate = oldLimit }()

	dataDir := t.TempDir()
	f, err := openServiceLog(dataDir)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if _, err := f.Write([]byte("0123456789")); err != nil { // 10B，未超限
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("abcdefghij")); err != nil { // 累计 20B > 16B：先轮转再写入
		t.Fatal(err)
	}
	f.Close()
	old, err := os.ReadFile(filepath.Join(dataDir, "logs", serviceLogOld))
	if err != nil || string(old) != "0123456789" {
		t.Fatalf("旧档应含轮转前内容，got %q err %v", old, err)
	}
	cur, err := os.ReadFile(filepath.Join(dataDir, "logs", serviceLogName))
	if err != nil || string(cur) != "abcdefghij" {
		t.Fatalf("新档应只含轮转后写入，got %q err %v", cur, err)
	}
}

// 改名失败回退（返工建议修2）：.old 被占（用目录占位模拟，跨平台 rename 必败）
// → 保原档继续追加，writer 不停在 nil——旧实现此分支后写入全被拒，日志永久丢失。
func TestServiceLogRotateRenameFailsKeepsWriting(t *testing.T) {
	oldLimit := serviceLogRotate
	serviceLogRotate = 16
	defer func() { serviceLogRotate = oldLimit }()

	dataDir := t.TempDir()
	logPath := filepath.Join(dataDir, "logs", serviceLogName)
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("0123456789012345678"), 0o644); err != nil { // 18B > 16B
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataDir, "logs", serviceLogOld), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := openServiceLog(dataDir)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("kept")); err != nil {
		t.Fatalf("改名失败分支写入不应报错: %v", err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil || string(b) != "0123456789012345678kept" {
		t.Fatalf("改名失败应保原档继续追加，got %q err %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "logs", serviceLogOld)); err != nil || !fi.IsDir() {
		t.Fatalf(".old 占位不应被改动: %v", err)
	}
}

// Close 后再写报错不 panic（服务关闭路径）。
func TestServiceLogWriteAfterClose(t *testing.T) {
	f, err := openServiceLog(t.TempDir())
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("二次关闭应幂等: %v", err)
	}
	if _, err := f.Write([]byte("x")); err == nil {
		t.Fatal("关闭后写入应报错")
	}
}

// ch 为 nil 原样返回父 ctx（前台/非 Windows 恒此路径，行为零变化）。
func TestMergeStopChanNilReturnsParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := mergeStopChan(parent, nil); got != parent {
		t.Fatal("nil 通道应原样返回父 ctx")
	}
}

// 通道关闭 → ctx 取消（SCM Stop 桥接主路径）。
func TestMergeStopChanFiresOnClose(t *testing.T) {
	ch := make(chan struct{})
	ctx := mergeStopChan(context.Background(), ch)
	select {
	case <-ctx.Done():
		t.Fatal("未关闭前不应取消")
	default:
	}
	close(ch)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("通道关闭后 ctx 应取消")
	}
}

// 父 ctx 先取消（如 SIGTERM）→ 子 ctx 取消、桥接 goroutine 收口不留悬挂 cancel。
func TestMergeStopChanParentCancelWins(t *testing.T) {
	ch := make(chan struct{})
	defer close(ch) // 父取消后再关通道，验证 goroutine 已退出不再误触发（无害）
	parent, cancel := context.WithCancel(context.Background())
	ctx := mergeStopChan(parent, ch)
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("父取消后子 ctx 应取消")
	}
}
