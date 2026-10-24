// 产物源读取安全单测（2026-10-24 跨机取回审查必修3）：readArtifactFile 同时是
// Remote 自动挂载（artifact_fetch 中继）的源，与挂载事务必须同一安全口径——
// 源打开统一经 inputs.OpenSourceFile（内核在 open 原语上强制不跟随，平台实现
// 见 inputs/sourceopen_*.go）+ 打开后 fd 常规文件校验（目录等特殊文件拒绝）。
// Local 挂载路径拒绝的形态，Remote 源读取不得更宽。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// regArtifact 在 workdir/outputs/<taskID>/<rel> 落盘并返回登记元信息。
func regArtifact(t *testing.T, workdir, taskID, rel, content string) protocol.Artifact {
	t.Helper()
	p := filepath.Join(workdir, "outputs", taskID, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return protocol.Artifact{
		Path:   "outputs/" + taskID + "/" + rel,
		Sha256: hex.EncodeToString(sum[:]),
		Size:   int64(len(content)),
	}
}

// mustReject 断言读取被拒（结构性拒绝，artifact_not_found 永久失败口径）。
func mustReject(t *testing.T, err error, why string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s：应拒绝", why)
	}
	ae, ok := err.(*artifactError)
	if !ok {
		t.Fatalf("%s：应为 *artifactError, got %T: %v", why, err, err)
	}
	if ae.code != protocol.CodeArtifactNotFound {
		t.Fatalf("%s：应为 artifact_not_found, got %s (%s)", why, ae.code, ae.msg)
	}
	if !strings.Contains(ae.msg, "拒绝") && !strings.Contains(ae.msg, "不存在") && !strings.Contains(ae.msg, "不可读") {
		t.Fatalf("%s：文案应说明拒绝原因: %s", why, ae.msg)
	}
}

// TestReadArtifactFileValidRegular 正常常规文件全链可用（防修复误伤主路径）。
func TestReadArtifactFileValidRegular(t *testing.T) {
	work := t.TempDir()
	meta := regArtifact(t, work, "t1", "sub/a.txt", "payload")
	data, err := readArtifactFile(work, meta)
	if err != nil || string(data) != "payload" {
		t.Fatalf("常规文件应可读取: %v %q", err, data)
	}
}

// TestReadArtifactFileRejectsFinalSymlink 末段符号链接：不跟随——即便链接目标
// 内容与登记逐字节一致也拒绝（Local 挂载路径同形态拒绝，Remote 源不得更宽）。
func TestReadArtifactFileRejectsFinalSymlink(t *testing.T) {
	work := t.TempDir()
	meta := regArtifact(t, work, "t1", "result.txt", "payload")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(work, "outputs", "t1", "result.txt")
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, real); err != nil {
		t.Fatal(err)
	}
	_, err := readArtifactFile(work, meta)
	mustReject(t, err, "末段符号链接")
}

// TestReadArtifactFileRejectsIntermediateSymlink 中间目录符号链接：拒绝跟随，
// 防借道链接逃逸 workdir——外部目录内放置哈希完全一致的文件也必须拒（拒的是
// 路径形态，不是内容）。
func TestReadArtifactFileRejectsIntermediateSymlink(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	meta := regArtifact(t, work, "t1", "result.txt", "payload")
	if err := os.Remove(filepath.Join(work, "outputs", "t1", "result.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "outputs", "t1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "outputs", "t1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "result.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readArtifactFile(work, meta)
	mustReject(t, err, "中间目录符号链接")
}

// TestReadArtifactFileRejectsSpecialFile 特殊文件形态：登记路径上是目录（非常规
// 文件）即拒绝——fd fstat 常规文件校验兜住（链接指向 fifo/设备等同口径，不卡
// 死串行帧处理）。
func TestReadArtifactFileRejectsSpecialFile(t *testing.T) {
	work := t.TempDir()
	meta := regArtifact(t, work, "t1", "weird.txt", "payload")
	if err := os.Remove(filepath.Join(work, "outputs", "t1", "weird.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "outputs", "t1", "weird.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := readArtifactFile(work, meta)
	mustReject(t, err, "目录占位（非常规文件）")
}

// TestReadArtifactFileRejectsTraversal 路径穿越/绝对路径/空路径：越界口径拒绝
//（沿用既有 mismatch 码；os.Root 定界为结构兜底）。
func TestReadArtifactFileRejectsTraversal(t *testing.T) {
	work := t.TempDir()
	sum := sha256.Sum256([]byte("x"))
	for _, p := range []string{"../escape.txt", "/abs.txt", "outputs/../..", ""} {
		meta := protocol.Artifact{Path: p, Sha256: hex.EncodeToString(sum[:]), Size: 1}
		if _, err := readArtifactFile(work, meta); err == nil {
			t.Fatalf("路径 %q 应拒绝", p)
		}
	}
}
