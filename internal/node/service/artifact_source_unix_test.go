// Remote 源读取 FIFO 回归（2026-10-24 复审 R1，unix 限定）：登记文件被换为无写
// 端 FIFO 时 readArtifactFile 必须快速拒绝而非挂起——hub 的 artifact_fetch 中继
// 超时解不开源 node 的串行帧阻塞，不阻塞只能靠源侧 open 原语（O_NONBLOCK）。
//go:build unix

package service

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestReadArtifactFileRejectsFIFO 无写端 FIFO 占位登记路径：Lstat 预检放行（非符
// 号链接/目录），open 环节 O_NONBLOCK 立即返回、fd fstat 拒绝。断言 5s 内返回
// （防 open 阻塞回归）且按 artifact_not_found 口径拒绝。
func TestReadArtifactFileRejectsFIFO(t *testing.T) {
	work := t.TempDir()
	fifo := filepath.Join(work, "outputs", "t1", "pipe.txt")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := protocol.Artifact{Path: "outputs/t1/pipe.txt", Sha256: hex.EncodeToString(make([]byte, 32)), Size: 4}
	done := make(chan error, 1)
	go func() {
		_, err := readArtifactFile(work, meta)
		done <- err
	}()
	select {
	case err := <-done:
		mustReject(t, err, "无写端 FIFO 占位")
	case <-time.After(5 * time.Second):
		t.Fatal("FIFO 读取 5s 未返回——open 被阻塞（O_NONBLOCK 缺失）")
	}
}
