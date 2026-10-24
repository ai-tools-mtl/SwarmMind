// 跨机取回解析器单测（P4④第二子轮/SPEC 4.12）：mock 中继全失败路径——
// 成功挂载 SHA 一致、离线重试后恢复、预算穷尽超时、404 已清理不重试、SHA 不
// 符永久失败、答案型上游零目录、组合解析器本机优先与远端兜底。
package inputs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// newLocalLedger 组合解析器用账本夹具（附产物根目录）。
func newLocalLedger(t *testing.T) (*ledger.Ledger, string) {
	t.Helper()
	upW := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	return led, upW
}

// noInputsNoPublish 断言输入区不含指定上游目录且无暂存残留（失败路径零发布）。
func noInputsNoPublish(t *testing.T, downW, upTaskID string) {
	t.Helper()
	noStaging(t, downW)
	entries, err := os.ReadDir(filepath.Join(downW, "inputs"))
	if err != nil {
		return // inputs/ 不存在也合规
	}
	for _, e := range entries {
		if e.Name() == upTaskID {
			t.Fatalf("失败路径不得发布输入目录: %s", e.Name())
		}
	}
}

// mockFetch RemoteFetch 桩：解析与取回可编程（错误序列/字节内容），记录调用
// 次数供重试语义断言。
type mockFetch struct {
	mu sync.Mutex

	target   *DepTarget          // ResolveDep 成功应答
	depErrs  []error             // ResolveDep 错误序列（穷尽后返 target）
	fetchFn  func(art protocol.Artifact) ([]byte, error) // FetchArtifact 行为
	fetchErr []error             // FetchArtifact 错误序列（穷尽后走 fetchFn）

	depCalls   int
	fetchCalls int
}

func (m *mockFetch) ResolveDep(ctx context.Context, idem string) (*DepTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.depCalls++
	if i := m.depCalls - 1; i < len(m.depErrs) {
		return nil, m.depErrs[i]
	}
	return m.target, ctx.Err()
}

func (m *mockFetch) FetchArtifact(ctx context.Context, taskID string, index int, meta protocol.Artifact) ([]byte, error) {
	m.mu.Lock()
	m.fetchCalls++
	i := m.fetchCalls - 1
	fn := m.fetchFn
	var seqErr error
	if i < len(m.fetchErr) {
		seqErr = m.fetchErr[i]
	}
	m.mu.Unlock()
	if seqErr != nil {
		return nil, seqErr
	}
	if fn != nil {
		return fn(meta)
	}
	return nil, ctx.Err()
}

func (m *mockFetch) calls() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.depCalls, m.fetchCalls
}

// remoteManifest 构造远端上游登记（内容=files，Path=outputs/<id>/<rel>）。
func remoteManifest(taskID string, files map[string]string) (*DepTarget, map[string][]byte) {
	tgt := &DepTarget{TaskID: taskID, TargetNode: "nodeB"}
	content := make(map[string][]byte, len(files))
	for rel, data := range files {
		b := []byte(data)
		content[rel] = b
		sum := sha256.Sum256(b)
		tgt.Artifacts = append(tgt.Artifacts, protocol.Artifact{
			Path:   fmt.Sprintf("outputs/%s/%s", taskID, rel),
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(b)),
		})
	}
	return tgt, content
}

// fastResolver 快节奏 RemoteResolver（测试：10ms 重试、30ms 尝试预算）。
func fastResolver(m RemoteFetch) *RemoteResolver {
	return &RemoteResolver{Fetch: m, RetryInterval: 10 * time.Millisecond, AttemptTimeout: 30 * time.Millisecond}
}

// TestRemoteMountFullChain 远端挂载成功全链：ResolveUpstream 产出带 Fetch 源的
// UpstreamInputs → Prepare 走既有挂载事务 → 输入区与远端字节逐件一致（SHA 对
// 照登记）。
func TestRemoteMountFullChain(t *testing.T) {
	tgt, content := remoteManifest("t-up-9", map[string]string{
		"result.txt": "remote payload",
		"sub/d.bin":  "\x07\x08bin",
	})
	m := &mockFetch{target: tgt, fetchFn: func(art protocol.Artifact) ([]byte, error) {
		for rel, b := range content {
			if strings.HasSuffix(art.Path, rel) {
				return b, nil
			}
		}
		return nil, fmt.Errorf("%w: 未知产物 %s", ErrRemoteGone, art.Path)
	}}
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		filepath.Join("t-up-9", "result.txt"): "remote payload",
		filepath.Join("t-up-9", "sub", "d.bin"): "\x07\x08bin",
	} {
		b, err := os.ReadFile(filepath.Join(downW, "inputs", rel))
		if err != nil || string(b) != want {
			t.Fatalf("挂载件 %s 不符: %v %q", rel, err, b)
		}
	}
	noStaging(t, downW)
}

// TestRemoteRetryUntilSourceReturns 源离线→重试轮询→恢复：可重试错误在预算内
// 消失后挂载完成（DoD 2「node 回来」路径）。
func TestRemoteRetryUntilSourceReturns(t *testing.T) {
	tgt, content := remoteManifest("t-up-1", map[string]string{"r.txt": "v1"})
	m := &mockFetch{
		target:  tgt,
		depErrs: []error{ErrRemoteRetryable, ErrRemoteRetryable}, // 前两次解析暂不可取
		fetchFn: func(art protocol.Artifact) ([]byte, error) { return content["r.txt"], nil },
	}
	downW := t.TempDir()
	if err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(downW, "inputs", "t-up-1", "r.txt"))
	if err != nil || string(b) != "v1" {
		t.Fatalf("恢复后应完成挂载: %v %q", err, b)
	}
	if n, _ := m.calls(); n != 3 {
		t.Fatalf("依赖解析应恰 3 次尝试（2 失败+1 成功），got %d", n)
	}
}

// TestRemoteRetryExhaustedTimeout 源不回来→准备预算穷尽：input_prepare_failed
//（超时语义，注明暂不可取），输入区零发布零暂存（DoD 2「node 不回来」路径）。
func TestRemoteRetryExhaustedTimeout(t *testing.T) {
	m := &mockFetch{depErrs: []error{ErrRemoteRetryable}, target: &DepTarget{TaskID: "t-up-1"}}
	// target 空清单会在重试穷尽前返回成功——这里让 depErrs 恒失败：用
	// fetchErr 阶段重试穷尽（解析成功→取回恒暂不可取）。
	m.depErrs = nil
	m.fetchErr = nil
	m.fetchFn = func(art protocol.Artifact) ([]byte, error) {
		return nil, ErrRemoteRetryable
	}
	tgt, _ := remoteManifest("t-up-1", map[string]string{"r.txt": "v1"})
	m.target = tgt
	r := &RemoteResolver{Fetch: m, RetryInterval: 5 * time.Millisecond, AttemptTimeout: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	downW := t.TempDir()
	err := Prepare(ctx, r, downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-1", "超时", "暂不可取")
	noInputsNoPublish(t, downW, "t-up-1")
}

// TestRemoteGoneNoRetry 404 已清理：永久失败立即返回（恰 1 次尝试不重试），
// 文案注明已清理与上游定位。
func TestRemoteGoneNoRetry(t *testing.T) {
	tgt, _ := remoteManifest("t-up-1", map[string]string{"r.txt": "v1"})
	m := &mockFetch{target: tgt, fetchFn: func(art protocol.Artifact) ([]byte, error) {
		return nil, fmt.Errorf("%w: 上游产物已清理", ErrRemoteGone)
	}}
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-1", "已清理")
	if _, n := m.calls(); n != 1 {
		t.Fatalf("已清理为永久失败，应恰 1 次取回尝试，got %d", n)
	}
	noInputsNoPublish(t, downW, "t-up-1")
}

// TestRemoteSHAMismatchPermanent 取回内容与登记不符：中继链完整性失败，永久
// 失败立即返回。
func TestRemoteSHAMismatchPermanent(t *testing.T) {
	tgt, _ := remoteManifest("t-up-1", map[string]string{"r.txt": "registered"})
	m := &mockFetch{target: tgt, fetchFn: func(art protocol.Artifact) ([]byte, error) {
		return []byte("tampered!"), nil // 大小一致内容不同（由终检抓）
	}}
	// 取回通道本身做登记大小校验——字节数同长可过通道，SHA 终检必拦
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-1", "SHA-256")
	if _, n := m.calls(); n != 1 {
		t.Fatalf("SHA 不符为永久失败，应恰 1 次尝试，got %d", n)
	}
	noInputsNoPublish(t, downW, "t-up-1")
}

// TestRemoteAnswerTypeUpstream 答案型上游（completed 零产物）：返回空清单不建
// 目录不算失败（与 LocalResolver 口径一致）。
func TestRemoteAnswerTypeUpstream(t *testing.T) {
	m := &mockFetch{target: &DepTarget{TaskID: "t-up-0", TargetNode: "nodeB"}}
	downW := t.TempDir()
	if err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(downW, "inputs", "t-up-0")); !os.IsNotExist(err) {
		t.Fatalf("答案型上游不得建目录: %v", err)
	}
}

// TestRemoteOverCountLimitFails 超 8 件（2026-10-24 审查必修4）：件数上限对登记
// 重检在 Remote 分支同样生效——取回前即拒绝（Fetch 零调用），永久失败无发布
// 无暂存。共享 Prepare 的 Local 限额测试不能证明 Remote 分支。
func TestRemoteOverCountLimitFails(t *testing.T) {
	tgt := &DepTarget{TaskID: "t-up-9", TargetNode: "nodeB"}
	for i := 0; i < protocol.MaxArtifacts+1; i++ {
		b := []byte(fmt.Sprintf("f%d", i))
		sum := sha256.Sum256(b)
		tgt.Artifacts = append(tgt.Artifacts, protocol.Artifact{
			Path:   fmt.Sprintf("outputs/t-up-9/f%d.txt", i),
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(b)),
		})
	}
	m := &mockFetch{target: tgt, fetchFn: func(art protocol.Artifact) ([]byte, error) {
		return []byte("x"), nil
	}}
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-9", "件数超上限")
	if _, n := m.calls(); n != 0 {
		t.Fatalf("件数超限应在取回前拒绝，Fetch 不得被调用，got %d", n)
	}
	noInputsNoPublish(t, downW, "t-up-9")
}

// TestRemoteOverSizeLimitNoFetch 登记单件超 640KiB（2026-10-24 审查必修4）：
// 暂存取回前拒绝——登记已超限，Fetch 不得被调用；永久失败无发布无暂存。
func TestRemoteOverSizeLimitNoFetch(t *testing.T) {
	tgt := &DepTarget{TaskID: "t-up-1", TargetNode: "nodeB", Artifacts: []protocol.Artifact{{
		Path: "outputs/t-up-1/big.bin", Sha256: strings.Repeat("a", 64), Size: protocol.ArtifactLimit + 1,
	}}}
	m := &mockFetch{target: tgt}
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-1", "单件超上限")
	if _, n := m.calls(); n != 0 {
		t.Fatalf("登记已超限，Fetch 不得被调用，got %d", n)
	}
	noInputsNoPublish(t, downW, "t-up-1")
}

// TestRemoteReturnedSizeMismatchFails 实际返回大小不符（2026-10-24 审查必修4）：
// 取回链终检（字节数对照登记）拦截，永久失败立即返回，无发布无暂存。
func TestRemoteReturnedSizeMismatchFails(t *testing.T) {
	tgt, _ := remoteManifest("t-up-1", map[string]string{"r.txt": "v1"})
	m := &mockFetch{target: tgt, fetchFn: func(art protocol.Artifact) ([]byte, error) {
		return []byte("v1-padded"), nil // 实得 9 字节 ≠ 登记 2 字节
	}}
	downW := t.TempDir()
	err := Prepare(context.Background(), fastResolver(m), downW, "t-down", []string{"dep-x"})
	mustErr(t, err, "t-up-1", "大小")
	if _, n := m.calls(); n != 1 {
		t.Fatalf("大小不符为永久失败，应恰 1 次尝试，got %d", n)
	}
	noInputsNoPublish(t, downW, "t-up-1")
}

// TestResolverLocalPreferred 组合解析器：本机命中优先（不触远端）；本机无
// completed 转远端；本机配置漂移不转远端（文案即真相）；远端禁用时本地失败
// 原样返回（standalone 行为不变）。
func TestResolverLocalPreferred(t *testing.T) {
	ctx := context.Background()
	led, upW := newLocalLedger(t)
	ledgerTask(t, led, "t-v1", "dep-local", "agent-a", time.Now(), protocol.ResultCompleted,
		artifactsOf(t, upW, "t-v1", map[string]string{"a.txt": "x"}))

	local := &LocalResolver{Ledger: led, RootOf: func(string) (string, bool) { return upW, true }}
	mock := &mockFetch{target: &DepTarget{TaskID: "t-remote"}}
	remote := fastResolver(mock)
	comp := &Resolver{Local: local, Remote: remote}

	// 本机命中：直接返回本地结果，远端零调用
	up, err := comp.ResolveUpstream(ctx, "dep-local")
	if err != nil || up.TaskID != "t-v1" {
		t.Fatalf("本机命中应直接返回: %v %+v", err, up)
	}
	if n, _ := mock.calls(); n != 0 {
		t.Fatalf("本机命中不得触远端，got %d 次", n)
	}

	// 本机无 → 远端兜底
	up, err = comp.ResolveUpstream(ctx, "dep-remote")
	if err != nil || up.TaskID != "t-remote" {
		t.Fatalf("本机无应转远端: %v %+v", err, up)
	}

	// 配置漂移（本机有 completed 但 agent 根缺失）：不转远端
	drift := &LocalResolver{Ledger: led, RootOf: func(string) (string, bool) { return "", false }}
	compDrift := &Resolver{Local: drift, Remote: remote}
	_, err = compDrift.ResolveUpstream(ctx, "dep-local")
	mustErr(t, err, "产物根不可定位")
	if n, _ := mock.calls(); n != 1 { // 仍只有上面 dep-remote 那 1 次
		t.Fatalf("配置漂移不得转远端，远端调用数异常: %d", n)
	}

	// 远端禁用（standalone）：本地 RemoteCandidate 失败原样返回
	_, err = (&Resolver{Local: local}).ResolveUpstream(ctx, "dep-missing")
	mustErr(t, err, "无该 idem 的任务")
}
