package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantSha(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TestScanArtifacts 登记/排序/路径口径：outputs/<task_id>/ 下文件登记为相对
// workdir 的斜杠路径 + SHA-256 + 大小；无目录 → 空。
func TestScanArtifacts(t *testing.T) {
	taskID := "t-abcdef"
	r, workdir := newRunner(t, nil)
	writeFile(t, filepath.Join(workdir, "outputs", taskID, "b.txt"), "BBB")
	writeFile(t, filepath.Join(workdir, "outputs", taskID, "sub", "a.txt"), "AA")
	// 无关目录不入册
	writeFile(t, filepath.Join(workdir, "outputs", "other-task", "x.txt"), "X")

	arts, skipped := scanArtifacts(workdir, taskID)
	if skipped != 0 {
		t.Errorf("skipped = %d", skipped)
	}
	if len(arts) != 2 {
		t.Fatalf("应登记 2 条, got %d", len(arts))
	}
	// 排序按相对路径字典序："outputs/t-abcdef/b.txt" < "outputs/t-abcdef/sub/a.txt"
	if arts[0].Path != "outputs/"+taskID+"/b.txt" || arts[0].Size != 3 || arts[0].Sha256 != wantSha(t, "BBB") {
		t.Errorf("首条登记不符: %+v", arts[0])
	}
	if arts[1].Path != "outputs/"+taskID+"/sub/a.txt" || arts[1].Size != 2 || arts[1].Sha256 != wantSha(t, "AA") {
		t.Errorf("次条登记不符: %+v", arts[1])
	}
	_ = r
}

// TestScanArtifactsLimits 限额：>8 条跳过、单文件 >640KiB 跳过、符号链接跳过，
// 均以 skipped 注记（SPEC 4.8/4.9）。
func TestScanArtifactsLimits(t *testing.T) {
	taskID := "t-limits"
	r, workdir := newRunner(t, nil)
	dir := filepath.Join(workdir, "outputs", taskID)

	// 10 个合法文件（>8）+ 1 个超大文件 + 1 个符号链接
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(dir, string(rune('a'+i))+".txt"), "x")
	}
	writeFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("B", protocol.ArtifactLimit+1))
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	arts, skipped := scanArtifacts(workdir, taskID)
	if len(arts) != protocol.MaxArtifacts {
		t.Errorf("应登记 %d 条, got %d", protocol.MaxArtifacts, len(arts))
	}
	for _, a := range arts {
		if a.Path == "outputs/"+taskID+"/big.bin" {
			t.Error("超大文件不应登记")
		}
		if a.Path == "outputs/"+taskID+"/link.txt" {
			t.Error("符号链接不应登记")
		}
	}
	// skipped = 2 超条数 + 1 超大小 + 1 符号链接
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4", skipped)
	}
	_ = r
}

// TestRunArtifactsEndToEnd 进程产出的产物经 Run 全流程登记（进程结束后扫描冻结）。
func TestRunArtifactsEndToEnd(t *testing.T) {
	taskID := "t-e2e-artifacts"
	workdir := t.TempDir()
	p := protocol.ExecProfile{
		Name: "a", Kind: protocol.AgentKindCLI,
		Command: "/bin/sh", Workdir: workdir,
		TimeoutSec: 10, AllowArgs: true,
	}
	r := New(p, baseTestEnv(), t.TempDir())
	task := simpleTask("-c", `mkdir -p outputs/`+taskID+` && printf result > outputs/`+taskID+`/answer.txt && echo done`)
	task.ID = taskID
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted || res.FinalAnswer != "done\n" {
		t.Fatalf("执行异常: %s %q", res.Status, res.FinalAnswer)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("应登记 1 条产物, got %d", len(res.Artifacts))
	}
	a := res.Artifacts[0]
	if a.Path != "outputs/"+taskID+"/answer.txt" || a.Size != 6 || a.Sha256 != wantSha(t, "result") {
		t.Errorf("产物登记不符: %+v", a)
	}
}
