package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 产物取回全链回归（fetch_timeout 实测缺陷，Loop 4 二轮）：membus + hub core +
// 真 node service（账本 + runner）全链，artifact_fetch → artifact_data 关联取回。
// 此前的桩测试回显请求 ReplyTo、掩盖了「hub 请求 MessageID 与 waiter 关联键不
// 一致」的缺陷——本测试以真实 node 侧回帧约定（ReplyTo=请求 MessageID）为准。

// writerRunner writer 场景桩执行器：向 workdir/outputs/{task_id}/ 写产物文件，
// 回 completed 结果（产物登记路径相对 workdir，SHA-256/大小与文件一致，与
// runner scanArtifacts/SWARM_OUTPUTS_DIR 同一口径）。
type writerRunner struct {
	workdir string
	content []byte
}

// Run 实现 service.Runner。
func (w *writerRunner) Run(_ context.Context, task protocol.Task) protocol.TaskResult {
	dir := filepath.Join(w.workdir, "outputs", task.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return protocol.TaskResult{Status: protocol.ResultFailed, Error: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), w.content, 0o644); err != nil {
		return protocol.TaskResult{Status: protocol.ResultFailed, Error: err.Error()}
	}
	sum := sha256.Sum256(w.content)
	return protocol.TaskResult{
		Status:   protocol.ResultCompleted,
		ExitCode: 0,
		Artifacts: []protocol.Artifact{{
			Path:   "outputs/" + task.ID + "/result.txt",
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(w.content)),
		}},
		FinishedAt: protocol.NowUTC(),
	}
}

// TestFullChainArtifactFetch 全链取回：提交 → 执行登记产物 → FetchArtifact 中继
// 取回内容一致。任何一环关联失败即 30s 量级超时，测试用 5s 上限快速暴露。
func TestFullChainArtifactFetch(t *testing.T) {
	f := newHub(t)
	workdir := t.TempDir()
	content := []byte("fullchain-artifact-payload")

	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("打开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := service.New(service.Config{
		Ledger: led,
		Runners: map[string]service.Runner{
			"writer": &writerRunner{workdir: workdir, content: content},
		},
		ArtifactRoots: map[string]string{"writer": workdir},
	}, f.bus.Endpoint("nodeB"))
	t.Cleanup(svc.Close)

	if err := f.core.RegisterNode("nodeB", agentsOf("writer")); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	f.core.SetOnline("nodeB", true)

	ack, err := f.core.Submit(submitReq("nodeB", "writer", "produce"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 执行为异步 goroutine：等任务终态（含产物登记）
	deadline := time.Now().Add(5 * time.Second)
	for {
		task := f.taskOf(ack.TaskID)
		if task.Status == protocol.StatusCompleted && task.Result != nil && len(task.Result.Artifacts) == 1 {
			break
		}
		if task.Status == protocol.StatusFailed || time.Now().After(deadline) {
			t.Fatalf("任务未按期完成: status=%s result=%+v", task.Status, task.Result)
		}
		time.Sleep(5 * time.Millisecond)
	}

	art, err := f.core.FetchArtifact(ack.TaskID, 0, 5*time.Second)
	if err != nil {
		t.Fatalf("全链产物取回失败: %v", err)
	}
	if string(art.Data) != string(content) {
		t.Errorf("产物内容不符: got %q want %q", art.Data, content)
	}
	if art.Meta.Path != "outputs/"+ack.TaskID+"/result.txt" {
		t.Errorf("产物路径不符: %s", art.Meta.Path)
	}
}
