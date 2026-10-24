// 跨机取回集成测试（P4④第二子轮/SPEC 4.12，FILE-COLLAB L2 跨机 DoD）：真 hub
//（core）+ 双 node service（nodeA/nodeB）经 membus 全链——上游 A node completed →
// 下游 B node 准备阶段自动 dep_resolve 定位 + artifact_fetch 中继取回挂载
//（SHA 逐字节一致断言）；源离线在准备阶段内重试恢复 / 穷尽失败、源产物已清理
// 的永久失败语义各有着落。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// gateTransport nodeA 的可控离线包装：down 时静默丢弃入站帧（hub 中继的
// artifact_fetch 送达失败=源不应答），恢复后照常投递。Send 直通（上游回结果
// 发生在 gate down 之前）。
type gateTransport struct {
	transport.Transport
	mu      sync.Mutex
	down    bool
	dropped int
}

func (g *gateTransport) Subscribe(fn transport.Handler) {
	g.Transport.Subscribe(func(env *protocol.Envelope) {
		g.mu.Lock()
		down := g.down
		g.mu.Unlock()
		if down {
			g.mu.Lock()
			g.dropped++
			g.mu.Unlock()
			return
		}
		fn(env)
	})
}

func (g *gateTransport) setDown(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.down = v
}

func (g *gateTransport) drops() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dropped
}

// stubRunner 桩执行器：把 files 写到 workdir/outputs/<task_id>/ 并按登记口径
// 回 completed+产物清单（上游 node 用；下游 node 用零产物桩）。
type stubRunner struct {
	workdir string
	files   map[string]string
}

func (r *stubRunner) Run(_ context.Context, task protocol.Task) protocol.TaskResult {
	now := protocol.NowUTC()
	arts := make([]protocol.Artifact, 0, len(r.files))
	for rel, content := range r.files {
		p := filepath.Join(r.workdir, "outputs", task.ID, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, Error: err.Error(), FinishedAt: now}
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 1, Error: err.Error(), FinishedAt: now}
		}
		sum := sha256.Sum256([]byte(content))
		arts = append(arts, protocol.Artifact{
			Path:   "outputs/" + task.ID + "/" + rel,
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(content)),
		})
	}
	return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, Artifacts: arts, FinishedAt: now}
}

// remoteFixture 双 node + 真 hub 夹具（membus 同步链）。
type remoteFixture struct {
	t        *testing.T
	bus      *transport.Bus
	hub      *core.Core
	nodeA    *Service
	nodeB    *Service
	gate     *gateTransport // nodeA 入站闸门（离线模拟）
	upWork   string         // nodeA 上游 agent workdir
	downWork string         // nodeB 下游 agent workdir
	ledA     *ledger.Ledger
	ledB     *ledger.Ledger
}

func newRemoteFixture(t *testing.T, hubRelayTimeout time.Duration) *remoteFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	hub, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"),
		RelayFetchTimeout: hubRelayTimeout, SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	bus.Endpoint("hub").Subscribe(hub.HandleFrame)

	f := &remoteFixture{t: t, bus: bus, hub: hub, upWork: t.TempDir(), downWork: t.TempDir()}

	// nodeA：上游 agent（桩执行器写产物）+ 入站闸门（离线模拟）
	ledA, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledA.Close() })
	f.gate = &gateTransport{Transport: bus.Endpoint("nodeA")}
	f.nodeA = New(Config{
		Ledger: ledA,
		Runners: map[string]Runner{
			"up-agent":   &stubRunner{workdir: f.upWork, files: map[string]string{"result.txt": "remote payload", "sub/d.bin": "\x07\x08bin"}},
			"down-agent": &stubRunner{},
		},
		ArtifactRoots: map[string]string{"up-agent": f.upWork, "down-agent": f.downWork},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(f.nodeA.Close)
	f.gate.Subscribe(f.nodeA.HandleFrame) // 闸门包装：以闸门为 Subscribe 入口

	// nodeB：下游 agent（零产物桩）+ 跨机取回快节奏（20ms 重试/300ms 尝试预算）
	ledB, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledB.Close() })
	f.nodeB = New(Config{
		Ledger: ledB,
		Runners: map[string]Runner{"down-agent": &stubRunner{}},
		ArtifactRoots: map[string]string{"down-agent": f.downWork},
		InputPrepareTimeout: 3 * time.Second,
	}, bus.Endpoint("nodeB"))
	t.Cleanup(f.nodeB.Close)
	bus.Endpoint("nodeB").Subscribe(f.nodeB.HandleFrame)
	f.nodeB.EnableRemoteInputs(20*time.Millisecond, 300*time.Millisecond)

	for _, nodeID := range []string{"nodeA", "nodeB"} {
		if err := hub.RegisterNode(nodeID, []protocol.AgentCard{{
			Name: "up-agent", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
			OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
		}, {
			Name: "down-agent", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
			OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
		}}); err != nil {
			t.Fatal(err)
		}
		hub.SetOnline(nodeID, true)
	}
	f.ledA, f.ledB = ledA, ledB
	return f
}

// submitUpstream 提交上游任务（nodeA/up-agent，idem=dep-1）并等待其 completed。
func (f *remoteFixture) submitUpstream() string {
	f.t.Helper()
	ack, err := f.hub.Submit(protocol.TaskSubmitRequest{
		TargetNode: "nodeA", TargetAgent: "up-agent",
		Input:       protocol.TaskInput{Args: []string{"up"}},
		SubmittedBy: "tester", IdemKey: "dep-1",
	}, "")
	if err != nil {
		f.t.Fatalf("提交上游失败: %v", err)
	}
	if !f.awaitTerminal(f.ledA, ack.TaskID, protocol.ResultCompleted) {
		f.t.Fatalf("上游应 completed（A 侧账本）")
	}
	return ack.TaskID
}

// submitDownstream 提交下游任务（nodeB/down-agent，依赖 idem=dep-1）。
func (f *remoteFixture) submitDownstream() string {
	f.t.Helper()
	ack, err := f.hub.Submit(protocol.TaskSubmitRequest{
		TargetNode: "nodeB", TargetAgent: "down-agent",
		Input:       protocol.TaskInput{Args: []string{"down"}},
		SubmittedBy: "tester", DependsOn: []string{"dep-1"},
	}, "")
	if err != nil {
		f.t.Fatalf("提交下游失败: %v", err)
	}
	return ack.TaskID
}

// awaitTerminal 等待账本条目到指定终态。
func (f *remoteFixture) awaitTerminal(led *ledger.Ledger, taskID, status string) bool {
	f.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := led.Get(taskID); ok && e.State == ledger.StateFinished &&
			e.Result != nil && e.Result.Status == status {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// result 读终态结果（已 awaitTerminal 保证存在）。
func (f *remoteFixture) result(led *ledger.Ledger, taskID string) protocol.TaskResult {
	e, _ := led.Get(taskID)
	return *e.Result
}

// TestRemoteMountCrossNode 双 node 全链（DoD 1）：上游 A node completed → 下游
// B node 准备阶段自动跨机取回挂载，inputs/ 与上游 outputs/ 逐字节一致（SHA-256
// 对照），任务正常 completed。
func TestRemoteMountCrossNode(t *testing.T) {
	f := newRemoteFixture(t, 300*time.Millisecond)
	upID := f.submitUpstream()
	downID := f.submitDownstream()

	if !f.awaitTerminal(f.ledB, downID, protocol.ResultCompleted) {
		t.Fatalf("下游应 completed: %+v", f.result(f.ledB, downID))
	}
	for _, rel := range []string{"result.txt", filepath.Join("sub", "d.bin")} {
		src, err := os.ReadFile(filepath.Join(f.upWork, "outputs", upID, rel))
		if err != nil {
			t.Fatalf("读上游产物失败: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(f.downWork, "inputs", upID, rel))
		if err != nil {
			t.Fatalf("读挂载件失败: %v", err)
		}
		if string(src) != string(got) {
			t.Fatalf("挂载件与上游不一致: %s", rel)
		}
	}
}

// TestRemoteSourceOfflineRecovers 源离线→重试恢复（DoD 2 路径一）：A 在上游
// completed 后离线，B 准备阶段重试轮询；A 回来后同一次准备内完成挂载。
func TestRemoteSourceOfflineRecovers(t *testing.T) {
	f := newRemoteFixture(t, 150*time.Millisecond)
	upID := f.submitUpstream()
	f.gate.setDown(true) // A 离线
	downID := f.submitDownstream()
	// 等其进入取回重试（闸门已有丢弃），随后恢复 A 在线
	deadline := time.Now().Add(3 * time.Second)
	for f.gate.drops() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.gate.drops() == 0 {
		t.Fatalf("A 离线期间应有被丢弃的中继帧")
	}
	f.gate.setDown(false)

	if !f.awaitTerminal(f.ledB, downID, protocol.ResultCompleted) {
		t.Fatalf("源恢复后下游应 completed: %+v", f.result(f.ledB, downID))
	}
	got, err := os.ReadFile(filepath.Join(f.downWork, "inputs", upID, "result.txt"))
	if err != nil || string(got) != "remote payload" {
		t.Fatalf("恢复后应完成挂载: %v %q", err, got)
	}
}

// TestRemoteSourceOfflineExhausted 源不回来（DoD 2 路径二）：准备预算穷尽 →
// failed/input_prepare_failed（注明暂不可取），不 started 不执行，输入区零发布。
func TestRemoteSourceOfflineExhausted(t *testing.T) {
	f := newRemoteFixture(t, 100*time.Millisecond)
	upID := f.submitUpstream()
	f.gate.setDown(true) // A 离线且不恢复
	downID := f.submitDownstream()

	if !f.awaitTerminal(f.ledB, downID, protocol.ResultFailed) {
		t.Fatalf("下游应 failed: %+v", f.result(f.ledB, downID))
	}
	res := f.result(f.ledB, downID)
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, protocol.CodeInputPrepareFailed) {
		t.Fatalf("应 failed/input_prepare_failed: %+v", res)
	}
	if !strings.Contains(res.Error, "暂不可取") {
		t.Fatalf("应注明暂不可取: %s", res.Error)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Fatalf("未运行应 ExitCodeNotRun: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.downWork, "inputs", upID)); !os.IsNotExist(err) {
		t.Fatalf("失败路径不得发布输入目录: %v", err)
	}
}

// TestRemoteArtifactGone 源产物已清理（保留期口径）：A 侧产物文件删除 → 取回
// 404 → 永久失败（不重试），文案注明已清理与上游定位。
func TestRemoteArtifactGone(t *testing.T) {
	f := newRemoteFixture(t, 300*time.Millisecond)
	upID := f.submitUpstream()
	// 上游产物文件删除（保留期已过的极端形态；登记仍在 hub/A 账本）
	if err := os.Remove(filepath.Join(f.upWork, "outputs", upID, "result.txt")); err != nil {
		t.Fatal(err)
	}
	downID := f.submitDownstream()

	if !f.awaitTerminal(f.ledB, downID, protocol.ResultFailed) {
		t.Fatalf("下游应 failed: %+v", f.result(f.ledB, downID))
	}
	res := f.result(f.ledB, downID)
	if !strings.Contains(res.Error, protocol.CodeInputPrepareFailed) ||
		!strings.Contains(res.Error, upID) || !strings.Contains(res.Error, "已过保留期清理") {
		t.Fatalf("失败文案应注明上游与已清理: %s", res.Error)
	}
}
