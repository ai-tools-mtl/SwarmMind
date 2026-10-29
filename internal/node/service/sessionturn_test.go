package service

// sessionturn_test.go 会话轮执行链测试（T2，SPEC 六.五）：offer 分派 → sessionx
// fake CLI 执行 → task_result 携轮终产物；create 建映射 / send 显式 ID 续接 /
// close 收尾 / 未装配与提权拒绝 / 结果链白拿（账本终态后重 offer 重发同一结果）。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"context"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// buildSessionFakeCLI 编译 sessionx 测试假 CLI（复用同一 testdata 资产；服务包
// 视角相对路径 ../sessionx/testdata/fakecli）。
func buildSessionFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, "../sessionx/testdata/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	return path
}

// sessionSvcFixture 会话执行链夹具：真 sessionx 内核（fake CLI）+ 真账本 + 录制
// hub 帧。
type sessionSvcFixture struct {
	t     *testing.T
	svc   *Service
	ep    *transport.Endpoint
	mgr   *sessionx.Manager
	led   *ledger.Ledger
	cwd   string
	fake  string
	envkv map[string]string

	mu     sync.Mutex
	frames []*protocol.Envelope
}

func newSessionSvcFixture(t *testing.T, envkv map[string]string) *sessionSvcFixture {
	t.Helper()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	bus := transport.NewBus()
	f := &sessionSvcFixture{t: t, ep: bus.Endpoint("hub"), led: led,
		cwd: t.TempDir(), fake: buildSessionFakeCLI(t), envkv: envkv}
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{
		Ledger:  led,
		Runners: map[string]Runner{},
		// workdir 互斥表：会话 agent 与普通任务同纪律（互斥键派生自产物根）
		ArtifactRoots: map[string]string{"coder": f.cwd},
	}, nodeEP)
	nodeEP.Subscribe(f.svc.HandleFrame)
	f.ep.Subscribe(func(env *protocol.Envelope) {
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		f.mu.Unlock()
	})
	t.Cleanup(f.svc.Close)
	return f
}

// specs 会话装配表：codex 形态 fake（FAKE_KIND 经 Env 注入）。
func (f *sessionSvcFixture) specs() map[string]sessionx.AgentSpec {
	kv := map[string]string{"FAKE_KIND": protocol.SessionAdapterCodex, "FAKE_THREAD_ID": "thr-svc-1"}
	for k, v := range f.envkv {
		kv[k] = v
	}
	env := []string{"PATH=/usr/bin:/bin"}
	env = append(env, "FAKE_KIND="+kv["FAKE_KIND"], "FAKE_THREAD_ID="+kv["FAKE_THREAD_ID"])
	for _, k := range []string{"FAKE_ARGV_LOG", "FAKE_LOG"} {
		if v, ok := kv[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	if v, ok := kv["FAKE_ON_RESUME"]; ok {
		env = append(env, "FAKE_ON_RESUME="+v)
	}
	spec := sessionx.AgentSpec{
		Adapter: protocol.SessionAdapterCodex, Command: f.fake,
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: env,
	}
	return map[string]sessionx.AgentSpec{"coder": spec}
}

// dispatchOffer 以 hub 身份下发会话轮 offer（真实 dispatch 链形态：完整 Task）。
func (f *sessionSvcFixture) dispatchOffer(task protocol.Task) {
	f.t.Helper()
	payload, err := json.Marshal(task)
	if err != nil {
		f.t.Fatal(err)
	}
	env := &protocol.Envelope{Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", To: "n1", Payload: payload}
	if err := f.ep.Send("n1", env); err != nil {
		f.t.Fatalf("下发 offer: %v", err)
	}
}

// sessionTask 构造会话轮任务（hub SubmitTurn 落库形态的最小投影：绑定快照
// adapter/permission 随帧下发，node 执行入口据其同口核验——T2-R2 必修2 补完后
// 的帧契约）。adapter 固定 codex = 本夹具能力卡声明的会话适配器。
func sessionTask(id, sid string, seq int64, op, cwd, prompt, perm string) protocol.Task {
	now := protocol.NowUTC()
	return protocol.Task{
		ID: id, TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: prompt}, SubmittedBy: "admin",
		Status: protocol.StatusSubmitted, CreatedAt: now, UpdatedAt: now,
		ExpireAt: now.Add(protocol.TaskRetention),
		Session: &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: op, Cwd: cwd,
			PermissionMode: perm, Adapter: protocol.SessionAdapterCodex},
	}
}

// waitResult 等待指定任务的 task_result 帧。
func (f *sessionSvcFixture) waitResult(taskID string) protocol.TaskResultPayload {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, env := range f.frames {
			if env.Type != protocol.MsgTaskResult {
				continue
			}
			var p protocol.TaskResultPayload
			if json.Unmarshal(env.Payload, &p) == nil && p.TaskID == taskID && p.Result != nil {
				f.mu.Unlock()
				return p
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("5s 内未收到 task=%s 的结果帧", taskID)
	return protocol.TaskResultPayload{}
}

// TestSessionTurnCreateAndSend 全链：create 轮建映射（fake 回 thread id）→
// send 轮显式 ID 续接（argv 留痕含 resume 与原生 ID）→ 结果帧携带轮终产物。
func TestSessionTurnCreateAndSend(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	f := newSessionSvcFixture(t, map[string]string{"FAKE_ARGV_LOG": argvLog})
	mgr, err := sessionx.NewManager(t.TempDir(), f.specs(), sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	f.svc.sessions = mgr

	// create：completed + NativeIDOk + 答复正文；内核建立映射
	t1 := sessionTask("t-sess-1", "s-svc0000000000000000000000000000000000000000000000001", 1,
		protocol.SessionTurnOpCreate, f.cwd, "首轮", "workspace-write")
	f.dispatchOffer(t1)
	p := f.waitResult(t1.ID)
	if p.Status != protocol.ResultCompleted || p.Result.FinalAnswer != "echo:首轮" {
		t.Fatalf("create 轮结果异常: %+v", p)
	}
	if p.Result.Turn == nil || !p.Result.Turn.NativeIDOk || p.Result.Turn.TurnSeq != 1 ||
		p.Result.Turn.WorkResult != protocol.WorkUnverified {
		t.Fatalf("create 轮产物异常: %+v", p.Result.Turn)
	}
	sess, ok := f.svc.SessionLocal("s-svc0000000000000000000000000000000000000000000000001")
	if !ok || sess.NativeID != "thr-svc-1" || sess.TurnCount != 1 {
		t.Fatalf("内核映射未建立: %+v ok=%v", sess, ok)
	}

	// send：argv 留痕含 resume 子命令与原生 ID（显式 ID 续接，绝不静默新线程）
	t2 := sessionTask("t-sess-2", sess.ID, 2, protocol.SessionTurnOpSend, "", "续轮", "")
	f.dispatchOffer(t2)
	p = f.waitResult(t2.ID)
	if p.Status != protocol.ResultCompleted || p.Result.Turn.TurnSeq != 2 {
		t.Fatalf("send 轮结果异常: %+v", p)
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("读 argv 留痕: %v", err)
	}
	for _, want := range []string{"resume", "thr-svc-1"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("send 轮 argv 留痕缺 %q: %s", want, argv)
		}
	}

	// 账本视角：会话轮任务按普通任务语义留痕（不破坏既有任务语义）
	if e, ok := f.led.Get(t2.ID); !ok || e.State != ledger.StateFinished || e.Result == nil {
		t.Fatalf("会话轮任务账本留痕异常: %+v ok=%v", e, ok)
	}
}

// TestSessionTurnClose 关闭轮：内核会话置 closed、结果 completed（幂等达成口径
// 由内核测试覆盖，此处验证执行链与回传）。
func TestSessionTurnClose(t *testing.T) {
	f := newSessionSvcFixture(t, nil)
	mgr, err := sessionx.NewManager(t.TempDir(), f.specs(), sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	f.svc.sessions = mgr
	// 预置会话（首轮已成立）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, _, err := mgr.CreateWithID(ctx, "s-close000000000000000000000000000000000000000001", "coder", f.cwd, "首轮"); err != nil {
		t.Fatalf("预置会话: %v", err)
	}
	tk := sessionTask("t-sess-close", "s-close000000000000000000000000000000000000000001", 0,
		protocol.SessionTurnOpClose, "", "", "")
	f.dispatchOffer(tk)
	p := f.waitResult(tk.ID)
	if p.Status != protocol.ResultCompleted {
		t.Fatalf("close 应 completed: %+v", p)
	}
	if sess, ok := f.svc.SessionLocal("s-close000000000000000000000000000000000000000001"); !ok || sess.Status != protocol.SessionClosed {
		t.Fatalf("内核会话应已关闭: %+v", sess)
	}
}

// TestSessionTurnGuards 守卫矩阵：未装配内核 failed session_unsupported；全权
// 档位 failed permission_denied（绝不注入 yolo 的执行口防线）；未知操作拒绝。
func TestSessionTurnGuards(t *testing.T) {
	// 未装配
	f := newSessionSvcFixture(t, nil)
	tk := sessionTask("t-g1", "s-x1", 1, protocol.SessionTurnOpCreate, f.cwd, "p", "")
	f.dispatchOffer(tk)
	p := f.waitResult(tk.ID)
	if p.Status != protocol.ResultFailed || !strings.Contains(p.Result.Error, "session_unsupported") {
		t.Fatalf("未装配应 failed session_unsupported: %+v", p)
	}
	f.svc.Close()

	// 全权档位
	f2 := newSessionSvcFixture(t, nil)
	mgr, err := sessionx.NewManager(t.TempDir(), f2.specs(), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f2.svc.sessions = mgr
	tk2 := sessionTask("t-g2", "s-x2", 1, protocol.SessionTurnOpCreate, f2.cwd, "p", "danger-full-access")
	f2.dispatchOffer(tk2)
	p = f2.waitResult(tk2.ID)
	if p.Status != protocol.ResultFailed || !strings.Contains(p.Result.Error, "permission_denied") {
		t.Fatalf("全权档位应 failed permission_denied: %+v", p)
	}
}

// TestSessionTurnResendChain 结果链白拿：终态落账后重复 offer → 重发 task_ack +
// 同一结果（重传内容稳定，会话轮不做任何二次执行——内核单写者与账本幂等共同
// 保证；此处锁「重复 offer 不再执行」的既有语义对会话轮成立）。
func TestSessionTurnResendChain(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	runLog := filepath.Join(t.TempDir(), "run.log")
	f := newSessionSvcFixture(t, map[string]string{"FAKE_ARGV_LOG": argvLog, "FAKE_LOG": runLog})
	mgr, err := sessionx.NewManager(t.TempDir(), f.specs(), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr
	tk := sessionTask("t-sess-r1", "s-resend0000000000000000000000000000000000000001", 1,
		protocol.SessionTurnOpCreate, f.cwd, "首轮", "")
	f.dispatchOffer(tk)
	p := f.waitResult(tk.ID)
	if p.Status != protocol.ResultCompleted {
		t.Fatalf("首轮应 completed: %+v", p)
	}
	before := f.countType(protocol.MsgTaskResult)
	// 重复 offer（hub 回退重推形态）：不再执行、直接重发 ack + result
	f.dispatchOffer(tk)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.countType(protocol.MsgTaskResult) > before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runBytes, err := os.ReadFile(runLog)
	if err != nil {
		t.Fatalf("读执行留痕: %v", err)
	}
	if runs := strings.Count(string(runBytes), "start"); runs != 1 {
		t.Fatalf("重复 offer 不得重复执行（fake CLI 应只跑一次）: %d", runs)
	}
	last := f.waitResult(tk.ID) // 重发的是账本留存的同一结果
	if last.Status != protocol.ResultCompleted || last.Result.FinalAnswer != "echo:首轮" {
		t.Fatalf("重发结果应与首轮一致: %+v", last)
	}
}

// ---------- 小工具（测试内联，避免依赖未导出助手） ----------

func (f *sessionSvcFixture) countType(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, env := range f.frames {
		if env.Type == typ {
			n++
		}
	}
	return n
}
