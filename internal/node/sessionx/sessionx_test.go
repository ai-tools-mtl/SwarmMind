package sessionx

// 管理器行为测试（TASK-agent-session-core 改动范围 5 与 DoD 6 失败语义附加要求）：
// 全部走 fake CLI（testdata/fakecli 编译的跨平台原生二进制），绝不真实调用 codex/
// zcode 模型（真机实测属 T4）。fake 输出结构取自探针 evidence
//（docs/evidence/native-session-20261029/）的简化形态，字段对得上。
//
// 反例回归（R1 审查 R1-R4/S1，反例源 /tmp/agent-session/probe 内嵌改写）：
//   TestFailedTurnDurableAcrossRestart   —— R1 失败轮持久化
//   TestSendAfterAgentMissingRejected    —— R2 agent 缺失显式拒绝
//   TestBindingDriftRejected             —— R3 绑定/权限漂移拒绝
//   TestQueuedCancelReleasesWriter       —— R4 排队等待可取消
//   TestCrossSessionSendParallel         —— S1 双会话 Send 并行
//   TestNastyPromptJSONRoundTrip         —— S1 prompt 真 JSON 编码保真
//   TestTurnGroupReapsDelayedWriter      —— S1 孙进程延迟写入治理断言

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestMain 预备与清理：fake CLI 构建产物目录在全部测试结束后删除。
func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		_ = os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

var (
	fakeOnce sync.Once
	fakeDir  string
	fakePath string
	fakeErr  error
)

// buildFakeCLI 编译测试专用假 CLI（testdata/fakecli：真 JSON 编码器产出输出，
// 含引号/换行/反斜杠的合法 prompt 不产生非法 JSON；各平台原生二进制，Windows 亦
// 可执行——R1 审查 S1 修订）。整包一次构建复用。
func buildFakeCLI(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		fakeDir, fakeErr = os.MkdirTemp("", "sessionx-fakecli")
		if fakeErr != nil {
			return
		}
		name := "fakecli"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		fakePath = filepath.Join(fakeDir, name)
		cmd := exec.Command("go", "build", "-o", fakePath, "./testdata/fakecli")
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeErr = fmt.Errorf("构建 fakecli: %w\n%s", err, out)
		}
	})
	if fakeErr != nil {
		t.Fatalf("fake CLI 构建失败: %v", fakeErr)
	}
	return fakePath
}

// testNow 固定时钟（UTC），Created==Updated==Started==Finished 可整值断言。
func testNow() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }

// testLogger 捕获内核运维日志（损坏文件起步等）。
type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// fakeEnv fake CLI 环境整体替换面：显式 PATH + 用例注入键（排序确定）。
func fakeEnv(kv map[string]string) []string {
	env := []string{"PATH=/usr/bin:/bin"}
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		env = append(env, k+"="+kv[k])
	}
	return env
}

// codexSpec / zcodeSpec 组装 fake agent 配置（FAKE_KIND 选输出形态）；PermissionMode
// 显式给定（探针形态），TimeoutSec=30 兜底防用例悬挂。
func codexSpec(command string, kv map[string]string) AgentSpec {
	if kv == nil {
		kv = map[string]string{}
	}
	kv["FAKE_KIND"] = protocol.SessionAdapterCodex
	return AgentSpec{
		Adapter: protocol.SessionAdapterCodex, Command: command,
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: fakeEnv(kv),
	}
}

func zcodeSpec(command string, kv map[string]string) AgentSpec {
	if kv == nil {
		kv = map[string]string{}
	}
	kv["FAKE_KIND"] = protocol.SessionAdapterZcode
	return AgentSpec{
		Adapter: protocol.SessionAdapterZcode, Command: command,
		PermissionMode: "edit", TimeoutSec: 30, Env: fakeEnv(kv),
	}
}

func newManager(t *testing.T, specs map[string]AgentSpec, opt Options) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(dir, specs, opt)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, dir
}

func mustCreate(t *testing.T, m *Manager, agent, cwd, prompt string) protocol.Session {
	t.Helper()
	sess, _, err := m.Create(context.Background(), agent, cwd, prompt)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return sess
}

// readLines 读整份文本按行切（Trim 尾换行）。
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// TestCreateSendHappyPathCodex 首轮建映射 + 续轮以存储 ID 续接 + 留痕与计数。
func TestCreateSendHappyPathCodex(t *testing.T) {
	fake := buildFakeCLI(t)
	workdir := t.TempDir()
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "01a185b2-5e57-72c0-8013-643a9fe4d234"}),
	}, Options{Now: testNow})

	sess := mustCreate(t, m, "codex-dev", workdir, "first-round-prompt")
	if sess.ID == "" || !strings.HasPrefix(sess.ID, "s-") {
		t.Fatalf("平台会话 ID 形态: %q", sess.ID)
	}
	if sess.NativeID != "01a185b2-5e57-72c0-8013-643a9fe4d234" ||
		sess.AgentName != "codex-dev" || sess.Adapter != protocol.SessionAdapterCodex ||
		sess.Cwd != workdir || sess.PermissionMode != "workspace-write" ||
		sess.Status != protocol.SessionOpen || sess.TurnCount != 1 {
		t.Fatalf("首轮会话字段: %+v", sess)
	}
	if len(sess.Turns) != 1 || sess.Turns[0].Seq != 1 || sess.Turns[0].Status != protocol.TurnOK {
		t.Fatalf("首轮轮次: %+v", sess.Turns)
	}
	if sess.Turns[0].Response != "echo:first-round-prompt" {
		t.Fatalf("首轮答复: %q", sess.Turns[0].Response)
	}
	if sess.Turns[0].Usage != (protocol.TurnUsage{InputTokens: 42082, OutputTokens: 139, TotalTokens: 42221}) {
		t.Fatalf("首轮用量: %+v", sess.Turns[0].Usage)
	}
	// 四路分离：三路成立 ≠ 工作成功声明，工作结果恒 unverified（R6）。
	if sess.Turns[0].WorkResult != protocol.WorkUnverified {
		t.Fatalf("TurnOK 不得冒充工作成功: %q", sess.Turns[0].WorkResult)
	}

	sess2, turn2, err := m.Send(context.Background(), sess.ID, "followup-prompt")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sess2.NativeID != sess.NativeID {
		t.Fatalf("续轮后映射漂移: %q → %q", sess.NativeID, sess2.NativeID)
	}
	if sess2.TurnCount != 2 || len(sess2.Turns) != 2 {
		t.Fatalf("续轮计数: count=%d turns=%d", sess2.TurnCount, len(sess2.Turns))
	}
	if turn2.Seq != 2 || turn2.Status != protocol.TurnOK || turn2.Response != "echo:followup-prompt" {
		t.Fatalf("续轮轮次: %+v", turn2)
	}
	if !sess2.Turns[0].StartedAt.Equal(testNow()) || !sess2.UpdatedAt.Equal(testNow()) {
		t.Fatalf("注入时钟未生效: %+v", sess2)
	}
}

// TestCreateSendHappyPathZcode zcode 全链：映射、续轮、argv 形态留痕（--resume 在
// --json 之后、-p 之前）。
func TestCreateSendHappyPathZcode(t *testing.T) {
	fake := buildFakeCLI(t)
	workdir := t.TempDir()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "zcode-argv.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"zcode-dev": zcodeSpec(fake, map[string]string{
			"FAKE_SESSION_ID": "sess_0f95c375-87da-48b6-95a8-6108e1493bcf",
			"FAKE_ARGV_LOG":   argvLog,
		}),
	}, Options{Now: testNow})

	sess := mustCreate(t, m, "zcode-dev", workdir, "first-round-prompt")
	if sess.NativeID != "sess_0f95c375-87da-48b6-95a8-6108e1493bcf" ||
		sess.Adapter != protocol.SessionAdapterZcode || sess.TurnCount != 1 {
		t.Fatalf("首轮会话字段: %+v", sess)
	}
	if sess.Turns[0].NativeTurnID != "turn_fixed" {
		t.Fatalf("首轮原生轮次 ID: %q", sess.Turns[0].NativeTurnID)
	}
	if sess.Turns[0].Usage != (protocol.TurnUsage{InputTokens: 59170, OutputTokens: 1240, TotalTokens: 60410}) {
		t.Fatalf("首轮用量: %+v", sess.Turns[0].Usage)
	}

	if _, _, err := m.Send(context.Background(), sess.ID, "followup-prompt"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// argv 端到端：首轮 6 参 + 续轮 8 参，逐参对照探针形态。
	got := readLines(t, argvLog)
	want := []string{
		"--cwd", workdir, "--mode", "edit", "--json", "-p", "first-round-prompt",
		"--cwd", workdir, "--mode", "edit", "--json", "--resume",
		"sess_0f95c375-87da-48b6-95a8-6108e1493bcf", "-p", "followup-prompt",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("zcode 命令形态漂移:\n got %v\nwant %v", got, want)
	}
}

// TestArgvEndToEndCodex codex 命令形态端到端：exec 通用参数在 resume 之前、
// sandbox/approval/-C 逐参对照探针已验证形态。
func TestArgvEndToEndCodex(t *testing.T) {
	fake := buildFakeCLI(t)
	workdir := t.TempDir()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "01a185b2-5e57-72c0-8013-643a9fe4d234",
			"FAKE_ARGV_LOG":  argvLog,
		}),
	}, Options{Now: testNow})

	sess := mustCreate(t, m, "codex-dev", workdir, "first-round-prompt")
	if _, _, err := m.Send(context.Background(), sess.ID, "followup-prompt"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := readLines(t, argvLog)
	want := []string{
		"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write",
		"-c", `approval_policy="never"`, "-C", workdir, "first-round-prompt",
		"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write",
		"-c", `approval_policy="never"`, "-C", workdir,
		"resume", "01a185b2-5e57-72c0-8013-643a9fe4d234", "followup-prompt",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("codex 命令形态漂移:\n got %v\nwant %v", got, want)
	}
}

// TestSendUnknownSession 未知平台会话 ID 拒绝。
func TestSendUnknownSession(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1"}),
	}, Options{Now: testNow})

	_, _, err := m.Send(context.Background(), "s-nonexistent", "p")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("应 ErrSessionNotFound, got %v", err)
	}
}

// TestCloseSemantics 关闭：状态落盘、续轮拒绝、重复关闭显式错误、未知关闭拒绝。
func TestCloseSemantics(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dataDir := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1"}),
	}, Options{Now: testNow})
	sess := mustCreate(t, m, "codex-dev", t.TempDir(), "p")

	if _, err := m.Close("s-nonexistent"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("关闭未知会话应 ErrSessionNotFound, got %v", err)
	}
	closed, err := m.Close(sess.ID)
	if err != nil || closed.Status != protocol.SessionClosed {
		t.Fatalf("Close: err=%v status=%s", err, closed.Status)
	}
	if _, _, err := m.Send(context.Background(), sess.ID, "p"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("关闭后续轮应 ErrSessionClosed, got %v", err)
	}
	if _, err := m.Close(sess.ID); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("重复关闭应 ErrSessionClosed, got %v", err)
	}
	// 关闭态经重启（重开 manager）保留。
	m2, err := NewManager(dataDir, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1"}),
	}, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	got, ok := m2.Status(sess.ID)
	if !ok || got.Status != protocol.SessionClosed {
		t.Fatalf("关闭态未持久化: ok=%v %+v", ok, got)
	}
}

// TestFailedTurnDurableAcrossRestart R1 反例回归：失败续轮（ID 不一致拒绝）的留痕
// 在重启后必须完整——失败轮字段、序号不丢，序号不被复用，落盘映射保持原值。
func TestFailedTurnDurableAcrossRestart(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"a": codexSpec(fake, map[string]string{
		"FAKE_THREAD_ID": "old", "FAKE_MISMATCH_ID": "wrong",
	})}
	m, dataDir := newManager(t, specs, Options{Now: testNow})
	sess := mustCreate(t, m, "a", t.TempDir(), "first")
	_, turn, err := m.Send(context.Background(), sess.ID, "next")
	if err == nil || turn == nil || turn.Status != protocol.TurnFailed {
		t.Fatalf("预置失败续轮: err=%v turn=%+v", err, turn)
	}

	// 重开：失败轮已持久化（R1：旧实现 Send 失败跳过落盘，重启后 TurnCount 退回）。
	m2, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	got, ok := m2.Status(sess.ID)
	if !ok || got.TurnCount != 2 || len(got.Turns) != 2 {
		t.Fatalf("失败 Send 重开后丢失: ok=%v count=%d", ok, got.TurnCount)
	}
	failed := got.Turns[1]
	if failed.Seq != 2 || failed.Status != protocol.TurnFailed ||
		!strings.Contains(failed.Error, "native_id_mismatch") {
		t.Fatalf("失败轮字段/序号漂移: %+v", failed)
	}
	if got.NativeID != "old" {
		t.Fatalf("落盘映射漂移: %q", got.NativeID)
	}

	// 序号不复用：重启后再续轮（仍 mismatch）得 seq=3。
	_, turn3, err := m2.Send(context.Background(), sess.ID, "next2")
	if err == nil || turn3 == nil || turn3.Seq != 3 {
		t.Fatalf("失败轮持久化后序号应继续递增: err=%v turn=%+v", err, turn3)
	}
	if got2, _ := m2.Status(sess.ID); got2.TurnCount != 3 {
		t.Fatalf("TurnCount 应为 3: %d", got2.TurnCount)
	}
}

// TestSendAfterAgentMissingRejected R2 反例回归：持久会话的 agent 在重开后缺席
// （配置删除 / 改名）必须显式可识别拒绝，绝不带 nil 适配器进入执行（旧实现 panic）。
func TestSendAfterAgentMissingRejected(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"a": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "id"})}
	m, dataDir := newManager(t, specs, Options{Now: testNow})
	sess := mustCreate(t, m, "a", t.TempDir(), "first")

	// 配置删除：重开装配表为空。
	m2, err := NewManager(dataDir, nil, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	_, turn, err := m2.Send(context.Background(), sess.ID, "next")
	if !errors.Is(err, ErrAgentMissing) {
		t.Fatalf("agent 删除后应 ErrAgentMissing, got %v", err)
	}
	if turn != nil {
		t.Fatalf("未进入执行的核验失败不得返回轮次: %+v", turn)
	}
	if got, _ := m2.Status(sess.ID); got.TurnCount != 1 {
		t.Fatalf("核验失败不得消耗轮号: %d", got.TurnCount)
	}

	// 配置改名：原 agent 名缺席，同样拒绝。
	m3, err := NewManager(dataDir, map[string]AgentSpec{
		"renamed": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "id"}),
	}, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开2: %v", err)
	}
	if _, _, err := m3.Send(context.Background(), sess.ID, "next"); !errors.Is(err, ErrAgentMissing) {
		t.Fatalf("agent 改名后应 ErrAgentMissing, got %v", err)
	}
}

// TestBindingDriftRejected R3 反例回归（上游 VISION 硬要求）：同名配置在会话存活
// 期间变更权限模式或适配器，续轮必须显式拒绝——CLI 实际执行绑定必须与会话记录
// 一致，禁止静默迁移/升权。
func TestBindingDriftRejected(t *testing.T) {
	fake := buildFakeCLI(t)

	t.Run("permission-drift", func(t *testing.T) {
		dir := t.TempDir()
		argvLog := filepath.Join(dir, "argv.log")
		specs := func(mode string) map[string]AgentSpec {
			spec := codexSpec(fake, map[string]string{
				"FAKE_THREAD_ID": "id", "FAKE_ARGV_LOG": argvLog,
			})
			spec.PermissionMode = mode
			return map[string]AgentSpec{"a": spec}
		}
		m, dataDir := newManager(t, specs("read-only"), Options{Now: testNow})
		sess := mustCreate(t, m, "a", t.TempDir(), "first")
		if sess.PermissionMode != "read-only" {
			t.Fatalf("预置权限: %+v", sess)
		}
		before, serr := os.Stat(argvLog)
		if serr != nil {
			t.Fatalf("首轮应已留 argv: %v", serr)
		}

		// 同名配置悄悄提权（read-only → workspace-write）后重开续轮。
		m2, err := NewManager(dataDir, specs("workspace-write"), Options{Now: testNow})
		if err != nil {
			t.Fatalf("重开: %v", err)
		}
		_, turn, err := m2.Send(context.Background(), sess.ID, "next")
		if !errors.Is(err, ErrBindingChanged) ||
			!strings.Contains(err.Error(), "read-only") ||
			!strings.Contains(err.Error(), "workspace-write") {
			t.Fatalf("权限漂移应显式拒绝并写明双方模式, got %v", err)
		}
		if turn != nil {
			t.Fatalf("拒绝轮不得返回轮次: %+v", turn)
		}
		// CLI 不得被调用（argv 留痕无增长 = 未执行）。
		after, serr := os.Stat(argvLog)
		if serr != nil || after.Size() != before.Size() {
			t.Fatalf("拒绝后续绑不得触碰 CLI: before=%d after=%v err=%v", before.Size(), after, serr)
		}
		got, _ := m2.Status(sess.ID)
		if got.PermissionMode != "read-only" || got.TurnCount != 1 {
			t.Fatalf("会话记录不得被漂移触碰: %+v", got)
		}
	})

	t.Run("adapter-drift", func(t *testing.T) {
		specs := func(adapter string) map[string]AgentSpec {
			if adapter == protocol.SessionAdapterCodex {
				return map[string]AgentSpec{"a": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-codex"})}
			}
			return map[string]AgentSpec{"a": zcodeSpec(fake, map[string]string{"FAKE_SESSION_ID": "sess-zcode"})}
		}
		m, dataDir := newManager(t, specs(protocol.SessionAdapterCodex), Options{Now: testNow})
		sess := mustCreate(t, m, "a", t.TempDir(), "first")

		m2, err := NewManager(dataDir, specs(protocol.SessionAdapterZcode), Options{Now: testNow})
		if err != nil {
			t.Fatalf("重开: %v", err)
		}
		_, turn, err := m2.Send(context.Background(), sess.ID, "next")
		if !errors.Is(err, ErrBindingChanged) ||
			!strings.Contains(err.Error(), "codex") || !strings.Contains(err.Error(), "zcode") {
			t.Fatalf("适配器漂移应显式拒绝, got %v", err)
		}
		if turn != nil {
			t.Fatalf("拒绝轮不得返回轮次: %+v", turn)
		}
		if got, _ := m2.Status(sess.ID); got.TurnCount != 1 {
			t.Fatalf("核验失败不得消耗轮号: %d", got.TurnCount)
		}
	})
}

// TestQueuedCancelReleasesWriter R4 反例回归：写者位被占时排队中的 Send 必须在
// 截止期后返回（旧实现 sync.Mutex.Lock 不可取消，200ms 后仍阻塞）；取消的排队轮
// 不得 spawn、不得消耗轮号。
func TestQueuedCancelReleasesWriter(t *testing.T) {
	fake := buildFakeCLI(t)
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"a": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "id", "FAKE_ARGV_LOG": argvLog, "FAKE_SLEEP": "2",
		}),
	}, Options{Now: testNow})
	sess := mustCreate(t, m, "a", t.TempDir(), "first")
	// 基线在排队前取：首轮 Create 的 argv 留痕必须不被取消轮追加。
	baseline, serr := os.Stat(argvLog)
	if serr != nil {
		t.Fatalf("argv 留痕: %v", serr)
	}

	// 占住写者位（模拟前轮长跑）。
	g, err := m.writerFor(sess.ID)
	if err != nil {
		t.Fatalf("writerFor: %v", err)
	}
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, serr := m.Send(ctx, sess.ID, "next")
		done <- serr
	}()
	select {
	case serr := <-done:
		if serr == nil {
			t.Fatalf("截止期已过应报错")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("排队中的 Send 未在截止期后返回（写者等待不可取消）")
	}
	// 取消的排队轮不得 spawn（argv 留痕无增长）、不得消耗轮号。
	<-time.After(100 * time.Millisecond) // 留出假 spawn 暴露窗口
	after, serr := os.Stat(argvLog)
	if serr != nil || after.Size() != baseline.Size() {
		t.Fatalf("取消轮不得 spawn CLI: before=%d after=%v", baseline.Size(), after)
	}
	if got, _ := m.Status(sess.ID); got.TurnCount != 1 {
		t.Fatalf("取消轮不得消耗轮号: %d", got.TurnCount)
	}
}

// TestNativeIDMismatchRejected 续轮回带与存储不一致：本轮失败、映射不动、失败轮
// 留痕、落盘映射保持原值（codex「另起线程」模拟 + zcode 换 sessionId 模拟）。
func TestNativeIDMismatchRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agentSpec func(fake string) map[string]AgentSpec
		stored    string
	}{
		{
			name: "codex",
			agentSpec: func(fake string) map[string]AgentSpec {
				return map[string]AgentSpec{"codex-dev": codexSpec(fake, map[string]string{
					"FAKE_THREAD_ID": "tid-stored", "FAKE_MISMATCH_ID": "tid-new-thread",
				})}
			},
			stored: "tid-stored",
		},
		{
			name: "zcode",
			agentSpec: func(fake string) map[string]AgentSpec {
				return map[string]AgentSpec{"zcode-dev": zcodeSpec(fake, map[string]string{
					"FAKE_SESSION_ID": "sess_stored", "FAKE_MISMATCH_ID": "sess_new",
				})}
			},
			stored: "sess_stored",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := buildFakeCLI(t)
			m, dataDir := newManager(t, tc.agentSpec(fake), Options{Now: testNow})
			sess := mustCreate(t, m, tc.name+"-dev", t.TempDir(), "p")

			_, turn, err := m.Send(context.Background(), sess.ID, "p2")
			if err == nil || !strings.Contains(err.Error(), "native_id_mismatch") {
				t.Fatalf("应 native_id_mismatch 失败, got %v", err)
			}
			if turn.Status != protocol.TurnFailed {
				t.Fatalf("轮次应 failed: %+v", turn)
			}
			got, ok := m.Status(sess.ID)
			if !ok || got.NativeID != tc.stored {
				t.Fatalf("映射必须保持原值: ok=%v native=%q", ok, got.NativeID)
			}
			if got.TurnCount != 2 || got.Turns[1].Status != protocol.TurnFailed {
				t.Fatalf("失败轮应留痕: count=%d", got.TurnCount)
			}
			if !strings.Contains(got.Turns[1].Error, "native_id_mismatch") {
				t.Fatalf("失败轮错误留痕: %q", got.Turns[1].Error)
			}
			// 落盘侧映射保持原值（重开核验，防内存态假绿）；失败轮随 R1 一并落盘。
			m2, err := NewManager(dataDir, tc.agentSpec(fake), Options{Now: testNow})
			if err != nil {
				t.Fatalf("重开: %v", err)
			}
			got2, ok := m2.Status(sess.ID)
			if !ok || got2.NativeID != tc.stored || got2.TurnCount != 2 {
				t.Fatalf("落盘漂移: ok=%v native=%q count=%d", ok, got2.NativeID, got2.TurnCount)
			}
		})
	}
}

// TestFirstTurnNativeIDMissingNoSession 首轮输出无原生 ID：显式失败、会话不建立、
// 磁盘不落映射。
func TestFirstTurnNativeIDMissingNoSession(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dataDir := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{
			// 事件流合法但缺 thread.started（exit 0）
			"FAKE_STDOUT": `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hi"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":6}}`,
		}),
	}, Options{Now: testNow})

	sess, turn, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if err == nil || !strings.Contains(err.Error(), "native_id_missing") {
		t.Fatalf("应 native_id_missing 失败, got %v", err)
	}
	if sess.ID != "" {
		t.Fatalf("会话不得建立: %+v", sess)
	}
	if turn.Status != protocol.TurnFailed {
		t.Fatalf("轮次应 failed: %+v", turn)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("List 应为空, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "sessions.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("失败首轮不得落盘 sessions.json: %v", err)
	}
}

// TestFirstTurnIncompleteNoSession codex 退出码 0 且解析成功但未见 turn.completed：
// 原生轮次完成判据不成立，会话不建立（三路分离判定）。
func TestFirstTurnIncompleteNoSession(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{
			"FAKE_STDOUT": `{"type":"thread.started","thread_id":"tid-x"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"half-way"}}`,
		}),
	}, Options{Now: testNow})

	_, turn, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if err == nil || !strings.Contains(err.Error(), "turn_incomplete") {
		t.Fatalf("应 turn_incomplete 失败, got %v", err)
	}
	if turn.Status != protocol.TurnFailed || len(m.List()) != 0 {
		t.Fatalf("失败轮 status=%s list=%d", turn.Status, len(m.List()))
	}
}

// TestExitNonZeroFailed 退出码非 0：failed + 退出码与 stderr 摘录留痕。
func TestExitNonZeroFailed(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1", "FAKE_EXIT_CODE": "7"}),
	}, Options{Now: testNow})

	_, turn, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if err == nil || !strings.Contains(err.Error(), "exit_code=7") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("应 exit_code=7 失败含 stderr 摘录, got %v", err)
	}
	if turn.ExitCode != 7 || turn.Status != protocol.TurnFailed {
		t.Fatalf("轮次: exit=%d status=%s", turn.ExitCode, turn.Status)
	}
	if len(m.List()) != 0 {
		t.Fatalf("失败首轮不得建会话")
	}
}

// TestInvalidOutputParseFailed 退出码 0 但输出非法：解析失败判 failed（退出码与
// 解析分离）。
func TestInvalidOutputParseFailed(t *testing.T) {
	fake := buildFakeCLI(t)
	for _, tc := range []struct{ name, stdout, wantErr string }{
		{"codex-garbage", "not json at all", "output_parse_failed"},
		{"zcode-garbage", `<html>oops</html>`, "output_parse_failed"},
		{"zcode-no-sessionid", `{"turnId":"t1","response":"r"}`, "sessionId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := codexSpec(fake, map[string]string{"FAKE_STDOUT": tc.stdout})
			if strings.HasPrefix(tc.name, "zcode") {
				spec = zcodeSpec(fake, map[string]string{"FAKE_STDOUT": tc.stdout})
			}
			m, _ := newManager(t, map[string]AgentSpec{"fake-dev": spec}, Options{Now: testNow})

			_, turn, err := m.Create(context.Background(), "fake-dev", t.TempDir(), "p")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("应 %q 失败, got %v", tc.wantErr, err)
			}
			if turn.Status != protocol.TurnFailed || turn.ExitCode != 0 {
				t.Fatalf("轮次: status=%s exit=%d（退出码 0 不代表成功）", turn.Status, turn.ExitCode)
			}
		})
	}
}

// TestCodexCorruptedItemSpliceRejected S2 反例回归（R2 复审，管理器全链）：
// 损坏 item 拼进否则完整的事件流（thread.started + item.completed + turn.completed、
// 退出码 0）必须整轮判失败——不得以零值/吞掉的 item 冒充完整成功建会话、落映射。
func TestCodexCorruptedItemSpliceRejected(t *testing.T) {
	fake := buildFakeCLI(t)
	for _, tc := range []struct{ name, item string }{
		{"item-null", `"item":null`},
		{"item-emptyobj", `"item":{}`},
		{"agentmsg-text-null", `"item":{"id":"item_0","type":"agent_message","text":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dataDir := newManager(t, map[string]AgentSpec{
				"codex-dev": codexSpec(fake, map[string]string{
					"FAKE_THREAD_ID": "tid-splice",
					"FAKE_STDOUT": `{"type":"thread.started","thread_id":"tid-splice"}
{"type":"turn.started"}
{"type":"item.completed",` + tc.item + `}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":6}}`,
				}),
			}, Options{Now: testNow})

			_, turn, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
			if err == nil || !strings.Contains(err.Error(), "载荷损坏") {
				t.Fatalf("损坏 item 拼接流应整轮失败, got %v", err)
			}
			if turn == nil || turn.Status != protocol.TurnFailed || turn.ExitCode != 0 {
				t.Fatalf("轮次: %+v", turn)
			}
			if len(m.List()) != 0 {
				t.Fatalf("损坏载荷轮不得建会话")
			}
			if _, serr := os.Stat(filepath.Join(dataDir, "sessions.json")); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("损坏载荷轮不得落盘映射: %v", serr)
			}
		})
	}
}

// TestTurnTimeoutFailed 超时：turn 失败并如实标注「进程已终止、原生侧状态不确定」，
// 且及时返回（进程组终止生效）。
func TestTurnTimeoutFailed(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1", "FAKE_SLEEP": "5"}),
	}, Options{Now: testNow})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, turn, err := m.Create(ctx, "codex-dev", t.TempDir(), "p")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), protocol.CodeTimeout) ||
		!strings.Contains(err.Error(), "原生侧状态不确定") {
		t.Fatalf("应 timeout 失败并标注原生侧状态不确定, got %v", err)
	}
	if turn.Status != protocol.TurnFailed || turn.ExitCode != protocol.ExitCodeNotRun {
		t.Fatalf("轮次: status=%s exit=%d", turn.Status, turn.ExitCode)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时未及时终止: %v", elapsed)
	}
	if len(m.List()) != 0 {
		t.Fatalf("失败首轮不得建会话")
	}
}

// TestSameSessionSendSerialized 单写者：同会话并发 Send 严格串行——fake 以
// start/end 序贯留痕，若互斥失效必然出现 start/start 交叠。
func TestSameSessionSendSerialized(t *testing.T) {
	fake := buildFakeCLI(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "seq.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-1", "FAKE_LOG": logPath, "FAKE_SLEEP": "0.3",
		}),
	}, Options{Now: testNow})
	sess := mustCreate(t, m, "codex-dev", t.TempDir(), "p")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, errs[idx] = m.Send(context.Background(), sess.ID, fmt.Sprintf("p-%d", idx))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发 Send[%d] 失败: %v", i, err)
		}
	}
	got := readLines(t, logPath)
	want := []string{"start", "end", "start", "end", "start", "end"} // Create + 2×Send
	if !slices.Equal(got, want) {
		t.Fatalf("同会话轮次未串行化（写者门失效）:\n got %v\nwant %v", got, want)
	}
	got2, _ := m.Status(sess.ID)
	if got2.TurnCount != 3 {
		t.Fatalf("TurnCount 应为 3, got %d", got2.TurnCount)
	}
}

// TestCrossSessionParallel 跨会话并行不互锁（Create 形态）：A 的轮次在等屏障文件
// 时 B 的轮次已跑完 touch——若存在全局互斥，A 只能超时空等（确定性判定，不依赖计时）。
func TestCrossSessionParallel(t *testing.T) {
	fake := buildFakeCLI(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "b-started")
	aLog := filepath.Join(dir, "a.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"agent-a": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-a", "FAKE_LOG": aLog, "FAKE_WAIT_FILE": marker,
		}),
		"agent-b": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-b", "FAKE_TOUCH": marker,
		}),
	}, Options{Now: testNow})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, agent := range []string{"agent-a", "agent-b"} {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			_, _, errs[idx] = m.Create(context.Background(), name, t.TempDir(), "p")
		}(i, agent)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发 Create[%d] 失败: %v", i, err)
		}
	}
	if !slices.Contains(readLines(t, aLog), "saw") {
		t.Fatalf("跨会话未并行（B 未在 A 在途时启动）: %v", readLines(t, aLog))
	}
}

// TestCrossSessionSendParallel S1 反例回归：两个**已有**会话的 Send 并行不互锁——
// A 停在等 B 的屏障文件（若存在全局互斥则 B 到不了 touch，屏障永不出现，A 的等
// 待轮询超时后无 saw 留痕，确定性 FAIL）。FAKE_ON_RESUME=1 使屏障动作仅在续轮
// 生效：Create 相不触发屏障，saw 留痕只能来自 Send 相的真实并行。
func TestCrossSessionSendParallel(t *testing.T) {
	fake := buildFakeCLI(t)
	dir := t.TempDir()
	markerB := filepath.Join(dir, "b-started")
	aLog := filepath.Join(dir, "a.log")
	m, _ := newManager(t, map[string]AgentSpec{
		"agent-a": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-a", "FAKE_LOG": aLog, "FAKE_WAIT_FILE": markerB,
			"FAKE_ON_RESUME": "1",
		}),
		"agent-b": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-b", "FAKE_TOUCH": markerB,
			"FAKE_ON_RESUME": "1",
		}),
	}, Options{Now: testNow})
	sessA := mustCreate(t, m, "agent-a", t.TempDir(), "p")
	sessB := mustCreate(t, m, "agent-b", t.TempDir(), "p")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, errs[0] = m.Send(context.Background(), sessA.ID, "pa")
	}()
	go func() {
		defer wg.Done()
		_, _, errs[1] = m.Send(context.Background(), sessB.ID, "pb")
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发 Send[%d] 失败: %v", i, err)
		}
	}
	if !slices.Contains(readLines(t, aLog), "saw") {
		t.Fatalf("跨会话 Send 未并行（B 未在 A 在途时跑完 touch）: %v", readLines(t, aLog))
	}
	if gotA, _ := m.Status(sessA.ID); gotA.TurnCount != 2 {
		t.Fatalf("A 轮数: %d", gotA.TurnCount)
	}
	if gotB, _ := m.Status(sessB.ID); gotB.TurnCount != 2 {
		t.Fatalf("B 轮数: %d", gotB.TurnCount)
	}
}

// TestNastyPromptJSONRoundTrip S1 反例回归：含引号/换行/反斜杠/制表符/非 ASCII 的
// 合法 prompt 经真 JSON 编码器往返保真（旧 sh fake 字符串拼接会产出非法 JSON）。
func TestNastyPromptJSONRoundTrip(t *testing.T) {
	fake := buildFakeCLI(t)
	prompt := "he said \"hi\"\nline2\ttab\\back /slash 日本語🚀"
	for _, kind := range []string{protocol.SessionAdapterCodex, protocol.SessionAdapterZcode} {
		t.Run(kind, func(t *testing.T) {
			var spec AgentSpec
			if kind == protocol.SessionAdapterCodex {
				spec = codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-nasty"})
			} else {
				spec = zcodeSpec(fake, map[string]string{"FAKE_SESSION_ID": "sid-nasty"})
			}
			m, _ := newManager(t, map[string]AgentSpec{"nasty": spec}, Options{Now: testNow})
			sess, turn, err := m.Create(context.Background(), "nasty", t.TempDir(), prompt)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if want := "echo:" + prompt; turn.Response != want {
				t.Fatalf("首轮往返保真失败:\n got %q\nwant %q", turn.Response, want)
			}
			_, turn2, err := m.Send(context.Background(), sess.ID, prompt+"#2")
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if want := "echo:" + prompt + "#2"; turn2.Response != want {
				t.Fatalf("续轮往返保真失败:\n got %q\nwant %q", turn2.Response, want)
			}
		})
	}
}

// TestTurnGroupReapsDelayedWriter S1 反例回归（R2 复审 S1 修订）：fake 先派生组内
// 延迟写手（不改进程组）并等孙进程 started 自证栅格命中后才阻塞/退出，超时与正常
// 两路的轮终整组收尾都必须终止它。started 文件在轮终时必须已存在——证明孙进程
// 真实派生于杀组之前（旧形态 sleep 在派生前、截止先杀父进程，超时路断言空过假绿）；
// 轮次返回 1.2s 后（写手原定 1s 落笔）survived marker 文件不得存在。
func TestTurnGroupReapsDelayedWriter(t *testing.T) {
	fake := buildFakeCLI(t)
	for _, timeout := range []bool{false, true} {
		name := map[bool]string{false: "normal-end", true: "timeout"}[timeout]
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "child-survived")
			started := marker + ".started"
			kv := map[string]string{
				"FAKE_THREAD_ID":          "id",
				"FAKE_SPAWN_DELAY_WRITER": "1|" + marker,
			}
			if timeout {
				// 超时路：派生栅格命中后 fake 才以 sleep 占住进程超过截止期——截止
				// 杀组时孙进程已真实在场。
				kv["FAKE_SLEEP"] = "5"
			}
			m, _ := newManager(t, map[string]AgentSpec{"a": codexSpec(fake, kv)}, Options{Now: testNow})
			deadline := 5 * time.Second
			if timeout {
				deadline = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			_, _, err := m.Create(ctx, "a", dir, "first")
			if timeout && err == nil {
				t.Fatal("超时路应失败")
			}
			if !timeout && err != nil {
				t.Fatalf("正常路失败: %v", err)
			}
			// 反空过断言：孙进程确曾在杀组前存活（自证栅格已落盘）。
			if _, serr := os.Stat(started); serr != nil {
				t.Fatalf("孙进程未在轮终前真实派生（治理路径未被执行）: %v", serr)
			}
			time.Sleep(1200 * time.Millisecond)
			if _, serr := os.Stat(marker); serr == nil {
				t.Fatalf("组内孙进程在轮终整组收尾后存活并写入")
			}
		})
	}
}

// TestStatusListReturnCopies 快照深拷贝：调用方改动不得穿透内核状态。
func TestStatusListReturnCopies(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1"}),
	}, Options{Now: testNow})
	sess := mustCreate(t, m, "codex-dev", t.TempDir(), "p")

	s, _ := m.Status(sess.ID)
	s.NativeID = "hacked"
	s.Status = protocol.SessionClosed
	s.Turns = append(s.Turns, protocol.Turn{Seq: 99})
	got, _ := m.Status(sess.ID)
	if got.NativeID == "hacked" || got.Status != protocol.SessionOpen || len(got.Turns) != 1 {
		t.Fatalf("快照未隔离: %+v", got)
	}
	lst := m.List()
	lst[0].NativeID = "hacked"
	got2, _ := m.Status(sess.ID)
	if got2.NativeID == "hacked" {
		t.Fatalf("List 快照未隔离")
	}
}

// TestCreateInputValidations 装配外入参校验：未知 agent / 相对 cwd / 空 prompt。
func TestCreateInputValidations(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-1"}),
	}, Options{Now: testNow})

	if _, _, err := m.Create(context.Background(), "nope", t.TempDir(), "p"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("应 ErrUnknownAgent, got %v", err)
	}
	if _, _, err := m.Create(context.Background(), "codex-dev", "relative/path", "p"); err == nil ||
		!strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("相对 cwd 应拒绝, got %v", err)
	}
	if _, _, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "   "); err == nil {
		t.Fatalf("空 prompt 应拒绝")
	}
}

// TestManagerConfigValidation 装配期配置校验：未知适配器 / 相对命令 / 负超时拒绝
// 启动；空 agent 表允许（Create 时才报未知）。
func TestManagerConfigValidation(t *testing.T) {
	fake := buildFakeCLI(t)
	cases := []struct {
		name string
		spec AgentSpec
	}{
		{"unknown-adapter", AgentSpec{Adapter: "claude", Command: fake}},
		{"relative-command", AgentSpec{Adapter: protocol.SessionAdapterCodex, Command: "codex"}},
		{"negative-timeout", AgentSpec{Adapter: protocol.SessionAdapterCodex, Command: fake, TimeoutSec: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManager(t.TempDir(), map[string]AgentSpec{"x": tc.spec}, Options{Now: testNow}); err == nil {
				t.Fatalf("装配应拒绝")
			}
		})
	}
	if _, err := NewManager(t.TempDir(), nil, Options{Now: testNow}); err != nil {
		t.Fatalf("空 agent 表应允许: %v", err)
	}
}

// TestEnvReplacesNotAppends AgentSpec.Env 整体替换不追加：注入键可达，未注入的
// 父进程环境键不可达（T2 给 SPEC 五干净基线的通道语义）。
func TestEnvReplacesNotAppends(t *testing.T) {
	t.Setenv("FAKE_ONLY_IN_PARENT", "parent-leak")
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{
			"FAKE_THREAD_ID": "tid-env", "FAKE_ECHO_ENV": "FAKE_MARKER", "FAKE_MARKER": "xyz-123",
		}),
	}, Options{Now: testNow})

	sess, _, err := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	resp := sess.Turns[0].Response
	if !strings.Contains(resp, "env:xyz-123") {
		t.Fatalf("注入 env 未达: %q", resp)
	}
	if strings.Contains(resp, "parent-leak") {
		t.Fatalf("Env 应整体替换而非追加（父进程键泄漏）: %q", resp)
	}
}
