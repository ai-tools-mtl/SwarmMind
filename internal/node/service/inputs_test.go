package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// ---------- 脚手架：双 agent（独立 workdir）+ ArtifactRoots 装配 ----------

// inputsFixture 上游/下游双 agent 夹具：上游产物目录可登记，下游执行后校验
// 输入区。复用 membus 假 hub 录制（同 service_test.go 口径）。
type inputsFixture struct {
	frameRec
	upWork   string
	downWork string
	led      *ledger.Ledger
}

func newInputsFixture(t *testing.T) *inputsFixture {
	t.Helper()
	upWork, downWork := t.TempDir(), t.TempDir()
	profiles := []protocol.ExecProfile{
		{Name: "up-agent", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
			Workdir: upWork, TimeoutSec: 5, AllowArgs: true, Exposed: true, PermissionMode: "no-shell"},
		{Name: "down-agent", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
			Workdir: downWork, TimeoutSec: 5, AllowArgs: true, Exposed: true, PermissionMode: "no-shell"},
	}
	runners := make(map[string]Runner, len(profiles))
	roots := make(map[string]string, len(profiles))
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home"))
		roots[p.Name] = p.Workdir
	}
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })

	bus := transport.NewBus()
	f := &inputsFixture{
		frameRec: frameRec{ep: bus.Endpoint("hub")},
		upWork:   upWork,
		downWork: downWork,
		led:      led,
	}
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{
		Ledger: led, Runners: runners, ArtifactRoots: roots,
		InputPrepareTimeout: 5 * time.Second,
	}, nodeEP)
	f.ep.Subscribe(func(env *protocol.Envelope) {
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		f.mu.Unlock()
	})
	t.Cleanup(f.svc.Close)
	return f
}

// registerUpstream 落盘上游产物并向账本登记 completed 终态（Path/SHA-256/Size
// 同 runner 扫描口径）。
func (f *inputsFixture) registerUpstream(t *testing.T, taskID, idem string, files map[string]string) {
	t.Helper()
	arts := make([]protocol.Artifact, 0, len(files))
	for rel, content := range files {
		p := filepath.Join(f.upWork, "outputs", taskID, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		arts = append(arts, protocol.Artifact{
			Path:   fmt.Sprintf("outputs/%s/%s", taskID, rel),
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(content)),
		})
	}
	now := protocol.NowUTC()
	task := protocol.Task{ID: taskID, TraceID: "tr-up", TargetNode: "n1", TargetAgent: "up-agent",
		SubmittedBy: "hub", IdemKey: idem, CreatedAt: now, UpdatedAt: now}
	if err := f.led.Accept(task); err != nil {
		t.Fatal(err)
	}
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, Artifacts: arts, FinishedAt: now}
	if err := f.led.MarkFinished(taskID, res); err != nil {
		t.Fatal(err)
	}
}

// depTask 构造携带 depends_on 的下游任务（目标 down-agent）。
func (f *inputsFixture) depTask(id string, idems ...string) protocol.Task {
	return protocol.Task{
		ID: id, TraceID: "tr-down", TargetNode: "n1", TargetAgent: "down-agent",
		Input: protocol.TaskInput{Args: []string{"down"}}, SubmittedBy: "hub",
		DependsOn: idems, Status: protocol.StatusOffered,
	}
}

// resultOf 等待任务到终态并返回结果（超时 Fatal）。
func (f *inputsFixture) resultOf(t *testing.T, taskID string) protocol.TaskResult {
	t.Helper()
	if !waitFor(3*time.Second, func() bool {
		e, ok := f.led.Get(taskID)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatalf("任务应在限时内到终态 task=%s", taskID)
	}
	e, _ := f.led.Get(taskID)
	return *e.Result
}

// noInputs 断言下游输入区不含指定上游目录且无暂存残留。
func noInputs(t *testing.T, downWork, upTaskID string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(downWork, "inputs"))
	if err != nil {
		return // inputs/ 不存在也合规
	}
	for _, e := range entries {
		if e.Name() == upTaskID {
			t.Fatalf("失败路径不得发布输入目录: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("暂存残留: %s", e.Name())
		}
	}
}

// ---------- 用例 ----------

// TestInputsMountFullChain 挂载成功全链：依赖放行的下游任务在执行前把上游产物
// 挂载到 workdir/inputs/<上游task_id>/（SHA 校验通过），任务正常 completed。
func TestInputsMountFullChain(t *testing.T) {
	f := newInputsFixture(t)
	f.registerUpstream(t, "t-up-1", "up-1", map[string]string{
		"result.txt": "upstream output",
		"sub/d.bin":  "\x01\x02data",
	})
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("下游应 completed: %+v", res)
	}
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("t-up-1", "result.txt"), "upstream output"},
		{filepath.Join("t-up-1", "sub", "d.bin"), "\x01\x02data"},
	} {
		b, err := os.ReadFile(filepath.Join(f.downWork, "inputs", c.rel))
		if err != nil || string(b) != c.want {
			t.Fatalf("挂载件 %s 不符: %v %q", c.rel, err, b)
		}
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 1 {
		t.Fatalf("应恰一次 started, got %d", n)
	}
}

// TestInputsSHAMismatchFails SHA 不符拒派：上游产物盘上内容与登记不一致 →
// failed/input_prepare_failed（注明上游与文件），不 started 不执行，输入区
// 零发布零暂存。
func TestInputsSHAMismatchFails(t *testing.T) {
	f := newInputsFixture(t)
	f.registerUpstream(t, "t-up-1", "up-1", map[string]string{"result.txt": "registered"})
	// 登记后篡改上游产物（等长，专触发 SHA 口径）
	if err := os.WriteFile(filepath.Join(f.upWork, "outputs", "t-up-1", "result.txt"),
		[]byte("regisTERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultFailed || res.ExitCode != protocol.ExitCodeNotRun {
		t.Fatalf("应 failed 且未运行: %+v", res)
	}
	if !strings.HasPrefix(res.Error, protocol.CodeInputPrepareFailed) ||
		!strings.Contains(res.Error, "t-up-1") || !strings.Contains(res.Error, "result.txt") {
		t.Fatalf("error 应注明 input_prepare_failed+上游+文件: %q", res.Error)
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 0 {
		t.Fatalf("不得 started/执行, started=%d", n)
	}
	noInputs(t, f.downWork, "t-up-1")
}

// TestInputsMissingArtifactFails 上游产物缺失（登记有、盘上无）→ 同失败语义。
func TestInputsMissingArtifactFails(t *testing.T) {
	f := newInputsFixture(t)
	f.registerUpstream(t, "t-up-1", "up-1", map[string]string{"result.txt": "x"})
	if err := os.Remove(filepath.Join(f.upWork, "outputs", "t-up-1", "result.txt")); err != nil {
		t.Fatal(err)
	}
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, protocol.CodeInputPrepareFailed) {
		t.Fatalf("应 failed/input_prepare_failed: %+v", res)
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 0 {
		t.Fatalf("不得执行, started=%d", n)
	}
	noInputs(t, f.downWork, "t-up-1")
}

// TestInputsUpstreamNotInLedgerFails 上游不在本机账本（异机执行/已清理；本子轮
// 不支持跨机取回）→ 如实 failed，不冒进执行。
func TestInputsUpstreamNotInLedgerFails(t *testing.T) {
	f := newInputsFixture(t)
	f.sendOffer(t, f.depTask("t-down-1", "missing-idem"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, protocol.CodeInputPrepareFailed) {
		t.Fatalf("应 failed/input_prepare_failed: %+v", res)
	}
	if !strings.Contains(res.Error, "missing-idem") {
		t.Fatalf("error 应注明依赖 idem: %q", res.Error)
	}
	noInputs(t, f.downWork, "")
}

// craftUpstreamArts 登记超限上游（9 件 / 单件超 640KiB 两个口径）。
func craftUpstreamArts(t *testing.T, f *inputsFixture, taskID, idem string, arts []protocol.Artifact) {
	t.Helper()
	now := protocol.NowUTC()
	task := protocol.Task{ID: taskID, TraceID: "tr-up", TargetNode: "n1", TargetAgent: "up-agent",
		SubmittedBy: "hub", IdemKey: idem, CreatedAt: now, UpdatedAt: now}
	if err := f.led.Accept(task); err != nil {
		t.Fatal(err)
	}
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, Artifacts: arts, FinishedAt: now}
	if err := f.led.MarkFinished(taskID, res); err != nil {
		t.Fatal(err)
	}
}

// TestInputsTooManyArtifactsFails 超 8 件（对登记重检）→ failed。
func TestInputsTooManyArtifactsFails(t *testing.T) {
	f := newInputsFixture(t)
	arts := make([]protocol.Artifact, 0, protocol.MaxArtifacts+1)
	for i := 0; i < protocol.MaxArtifacts+1; i++ {
		content := fmt.Sprintf("file-%d", i)
		sum := sha256.Sum256([]byte(content))
		arts = append(arts, protocol.Artifact{
			Path:   fmt.Sprintf("outputs/t-up-1/f%d.txt", i),
			Sha256: hex.EncodeToString(sum[:]),
			Size:   int64(len(content)),
		})
	}
	craftUpstreamArts(t, f, "t-up-1", "up-1", arts)
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, "件数超上限") {
		t.Fatalf("应 failed 且注明件数超限: %+v", res)
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 0 {
		t.Fatalf("不得执行, started=%d", n)
	}
	noInputs(t, f.downWork, "t-up-1")
}

// TestInputsOversizeArtifactFails 单件超 640KiB（登记口径）→ failed。
func TestInputsOversizeArtifactFails(t *testing.T) {
	f := newInputsFixture(t)
	craftUpstreamArts(t, f, "t-up-1", "up-1", []protocol.Artifact{{
		Path: "outputs/t-up-1/big.bin", Sha256: strings.Repeat("a", 64), Size: protocol.ArtifactLimit + 1,
	}})
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, "超上限") {
		t.Fatalf("应 failed 且注明单件超限: %+v", res)
	}
	noInputs(t, f.downWork, "t-up-1")
}

// TestNoDependsZeroImpact 红线：无 depends_on 任务零行为变化——不触碰输入区，
// 正常执行 completed。
func TestNoDependsZeroImpact(t *testing.T) {
	f := newInputsFixture(t)
	task := protocol.Task{
		ID: "t-plain", TraceID: "tr", TargetNode: "n1", TargetAgent: "down-agent",
		Input: protocol.TaskInput{Args: []string{"plain"}}, SubmittedBy: "hub",
		Status: protocol.StatusOffered,
	}
	f.sendOffer(t, task)
	res := f.resultOf(t, "t-plain")
	if res.Status != protocol.ResultCompleted || res.FinalAnswer != "plain\n" {
		t.Fatalf("无依赖任务应照常 completed: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.downWork, "inputs")); !os.IsNotExist(err) {
		t.Fatalf("无依赖任务不得创建输入区: %v", err)
	}
}

// TestInputsZeroArtifactUpstream 答案型上游（completed 无产物登记）：下游正常
// 执行，不失败不建目录。
func TestInputsZeroArtifactUpstream(t *testing.T) {
	f := newInputsFixture(t)
	f.registerUpstream(t, "t-up-1", "up-1", nil)
	f.sendOffer(t, f.depTask("t-down-1", "up-1"))

	res := f.resultOf(t, "t-down-1")
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("零产物上游不应阻塞下游: %+v", res)
	}
	noInputs(t, f.downWork, "t-up-1")
}

// TestInputsPrepareTimeoutFails 独立超时：输入准备超时 → failed（error 注明
// 超时），不执行。
func TestInputsPrepareTimeoutFails(t *testing.T) {
	upWork, downWork := t.TempDir(), t.TempDir()
	profiles := []protocol.ExecProfile{
		{Name: "up-agent", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
			Workdir: upWork, TimeoutSec: 5, AllowArgs: true, Exposed: true, PermissionMode: "no-shell"},
		{Name: "down-agent", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
			Workdir: downWork, TimeoutSec: 5, AllowArgs: true, Exposed: true, PermissionMode: "no-shell"},
	}
	runners := make(map[string]Runner, len(profiles))
	roots := make(map[string]string, len(profiles))
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home"))
		roots[p.Name] = p.Workdir
	}
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })

	bus := transport.NewBus()
	f := &inputsFixture{frameRec: frameRec{ep: bus.Endpoint("hub")}, downWork: downWork, led: led}
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{
		Ledger: led, Runners: runners, ArtifactRoots: roots,
		InputPrepareTimeout: time.Nanosecond, // 独立超时压到最小，必超时
	}, nodeEP)
	f.ep.Subscribe(func(env *protocol.Envelope) {
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		f.mu.Unlock()
	})
	t.Cleanup(f.svc.Close)

	// 上游账本条目（登记产物；纳秒级超时使挂载在首个文件前即中止）
	now := protocol.NowUTC()
	upTask := protocol.Task{ID: "t-up-1", TraceID: "tr", TargetNode: "n1", TargetAgent: "up-agent",
		SubmittedBy: "hub", IdemKey: "up-1", CreatedAt: now, UpdatedAt: now}
	if err := led.Accept(upTask); err != nil {
		t.Fatal(err)
	}
	content := "x"
	sum := sha256.Sum256([]byte(content))
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: now,
		Artifacts: []protocol.Artifact{{Path: "outputs/t-up-1/a.txt", Sha256: hex.EncodeToString(sum[:]), Size: 1}}}
	if err := led.MarkFinished("t-up-1", res); err != nil {
		t.Fatal(err)
	}

	f.sendOffer(t, f.depTask("t-down-1", "up-1"))
	got := f.resultOf(t, "t-down-1")
	if got.Status != protocol.ResultFailed || !strings.Contains(got.Error, protocol.CodeInputPrepareFailed) {
		t.Fatalf("应 failed/input_prepare_failed: %+v", got)
	}
	if !strings.Contains(got.Error, "超时") {
		t.Fatalf("error 应注明准备超时: %q", got.Error)
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 0 {
		t.Fatalf("不得执行, started=%d", n)
	}
	noInputs(t, downWork, "t-up-1")
}
