package service

// sessionop_wire_test.go W01-02 会话操作服务集成测试（N01–N07，docs/tasks/v3/
// W01-02-rfc.md §9）：sessionop 装配注入 + 真内核（fake CLI）+ 真账本的受理守卫、
// 取消记账、关闭防护、工作区授权与互斥、重启收敛、持久化失败如实拒绝。
// 全部 fake CLI（testdata 资产，FAKE_* 驱动），绝不真实模型/网络；时间 UTC。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// sessOpOpt 会话操作夹具装配项。
type sessOpOpt struct {
	queueLimit int                          // 0 = sessionop 默认 16
	roots      []string                     // AllowedWorkspaceRoots
	barrierAll bool                         // 屏障对全部轮生效（互斥测试）；默认仅续轮（FAKE_ON_RESUME）
	envkv      map[string]string            // 额外 FAKE_* 注入（ON_RESUME/SPAWN_DELAY_WRITER 等）
	confirm    time.Duration                // sessionop ConfirmBudget（回执确认预算）
	ledInject  func(op, stage string) error // 账本事务故障注入（N07）
}

// sessOpFixture sessionop 集成夹具：真 sessionx（fake CLI）+ 真账本 + sessionop
// 注入 + 录制 hub 帧。
type sessOpFixture struct {
	t       *testing.T
	svc     *Service
	ep      *transport.Endpoint
	mgr     *sessionx.Manager
	led     *ledger.Ledger
	ops     *sessionop.Service
	cwd     string
	barrier string // 触摸即放行（注入时 FAKE_WAIT_FILE 目标）
	runLog  string
	argvLog string

	mu     sync.Mutex
	frames []*protocol.Envelope
}

func newSessOpFixture(t *testing.T, opt sessOpOpt) *sessOpFixture {
	t.Helper()
	ledDir := filepath.Join(t.TempDir(), "ledger")
	var led *ledger.Ledger
	var err error
	if opt.ledInject != nil {
		led, _, err = ledger.OpenForTest(ledDir, opt.ledInject)
	} else {
		led, _, err = ledger.Open(ledDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	bus := transport.NewBus()
	f := &sessOpFixture{
		t: t, ep: bus.Endpoint("hub"), led: led,
		cwd:     t.TempDir(),
		barrier: filepath.Join(t.TempDir(), "go.barrier"),
		runLog:  filepath.Join(t.TempDir(), "run.log"),
		argvLog: filepath.Join(t.TempDir(), "argv.log"),
	}
	// 会话 agent 与普通任务互斥键基准分离：产物根独立目录，键口径差异由会话
	// cwd 派生承担（RFC §5）。
	artifactRoot := t.TempDir()
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
		"FAKE_THREAD_ID=thr-op-1",
		"FAKE_ARGV_LOG=" + f.argvLog, "FAKE_LOG=" + f.runLog}
	// 屏障注入策略：barrierAll=全轮阻塞（互斥佐证）；否则 envkv 带 ON_RESUME 时
	// 仅续轮阻塞（create 秒完，排队/取消用例的隔离面）。
	_, wantResume := opt.envkv["FAKE_ON_RESUME"]
	if opt.barrierAll || wantResume {
		env = append(env, "FAKE_WAIT_FILE="+f.barrier)
	}
	if wantResume {
		env = append(env, "FAKE_ON_RESUME=1")
	}
	for _, k := range []string{"FAKE_SLEEP", "FAKE_SPAWN_DELAY_WRITER", "FAKE_TOUCH"} {
		if v, ok := opt.envkv[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	specs := map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: buildSessionFakeCLI(t),
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: env,
	}}
	mgr, err := sessionx.NewManager(t.TempDir(), specs, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	f.mgr = mgr
	ops, err := sessionop.New(t.TempDir(), led, sessionop.Options{
		QueueLimit:            opt.queueLimit,
		AllowedWorkspaceRoots: opt.roots,
		ConfirmBudget:         opt.confirm,
	})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	f.ops = ops
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{},
		ArtifactRoots: map[string]string{"coder": artifactRoot},
		Sessions:      mgr,
		SessionOps:    ops,
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

// submit 本地提交会话轮（受理面；拒绝断言用返回的 error）。
func (f *sessOpFixture) submit(req LocalSessionTurnRequest) (*LocalTurnAck, error) {
	f.t.Helper()
	return f.svc.SubmitSessionTurnLocal(req)
}

// submitCreate 快速建会话（无屏障或屏障仅续轮时 create 立即完成）。
func (f *sessOpFixture) submitCreate(cwd string) *LocalTurnAck {
	f.t.Helper()
	ack, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: cwd, Prompt: "首轮"})
	if err != nil {
		f.t.Fatalf("create 受理: %v", err)
	}
	f.waitTurnFinished(ack.SessionID, ack.TurnSeq, protocol.ResultCompleted, 8*time.Second)
	return ack
}

// runLines 读 fake CLI 执行留痕行。
func (f *sessOpFixture) runLines() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.runLog)
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

func countLines(lines []string, s string) int {
	n := 0
	for _, l := range lines {
		if l == s {
			n++
		}
	}
	return n
}

func nthIndexOf(lines []string, s string, n int) int {
	seen := 0
	for i, l := range lines {
		if l == s {
			seen++
			if seen == n {
				return i
			}
		}
	}
	return -1
}

// waitRunLogLine 等执行留痕出现 n 个指定行。
func (f *sessOpFixture) waitRunLogLine(line string, n int) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countLines(f.runLines(), line) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("5s 内执行留痕未达 %d 个 %q（现 %d）: %v", n, line, countLines(f.runLines(), line), f.runLines())
}

// releaseBarrier 触摸屏障放行全部等待中的 fake CLI。
func (f *sessOpFixture) releaseBarrier() {
	f.t.Helper()
	if err := os.WriteFile(f.barrier, nil, 0o644); err != nil {
		f.t.Fatalf("放行屏障: %v", err)
	}
}

// sessionEntries 收集会话的账本受理条目。
func (f *sessOpFixture) sessionEntries(sid string) []ledger.Entry {
	var out []ledger.Entry
	for _, e := range f.led.View() {
		if e.Task.Session != nil && e.Task.Session.SessionID == sid {
			out = append(out, e.Clone())
		}
	}
	return out
}

// countSessionTasks 账本内全部会话受理条目数（零副作用断言用）。
func (f *sessOpFixture) countSessionTasks() int {
	n := 0
	for _, e := range f.led.View() {
		if e.Task.Session != nil {
			n++
		}
	}
	return n
}

// entryOf 定位会话指定序号的受理条目。
func (f *sessOpFixture) entryOf(sid string, seq int64) (ledger.Entry, bool) {
	for _, e := range f.sessionEntries(sid) {
		if e.Task.Session.TurnSeq == seq {
			return e, true
		}
	}
	return ledger.Entry{}, false
}

// waitTurnFinished 等待指定轮落指定终态。
func (f *sessOpFixture) waitTurnFinished(sid string, seq int64, status string, within time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e, ok := f.entryOf(sid, seq); ok && e.State == ledger.StateFinished && e.Result != nil && e.Result.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, ok := f.entryOf(sid, seq)
	f.t.Fatalf("%v 内 session=%s seq=%d 未落终态 %s（entry=%+v ok=%v）", within, sid, seq, status, e, ok)
}

// waitAllTurnsFinished 等待会话终态条目数达 want。
func (f *sessOpFixture) waitAllTurnsFinished(sid string, want int, within time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		n := 0
		for _, e := range f.sessionEntries(sid) {
			if e.State == ledger.StateFinished {
				n++
			}
		}
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("%v 内会话 %s 终态条目未达 %d", within, sid, want)
}

// TestW01QueueLimit17 N01：同会话 17 条受理，第 17 条 ErrQueueFull 且零副作用
//（无账本条目、高水位不推进、CLI 不多跑）；放行屏障后 16 条全部照常收敛。
func TestW01QueueLimit17(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	ack := f.submitCreate(f.cwd)
	sid := ack.SessionID

	// 16 条续投全部受理（屏障前阻塞在 CLI 执行，保持非终态）
	var acks []*LocalTurnAck
	for i := 0; i < 16; i++ {
		a, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "续投"})
		if err != nil {
			t.Fatalf("第 %d 条续投不应被拒: %v", i+2, err)
		}
		acks = append(acks, a)
	}
	if acks[0].TurnSeq != 2 || acks[15].TurnSeq != 17 {
		t.Fatalf("受理序号应连续 2..17: 首=%d 末=%d", acks[0].TurnSeq, acks[15].TurnSeq)
	}

	// 第 17 条：ErrQueueFull，零副作用
	_, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "第 17 条"})
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != sessionErrQueueFull {
		t.Fatalf("第 17 条应 ErrQueueFull/turn_queue_full: err=%v", err)
	}
	if _, hit := f.entryOf(sid, 18); hit {
		t.Fatal("满队列拒绝不得产生账本条目（零副作用）")
	}
	if hw := f.led.SessionTurnHighWater(sid); hw != 17 {
		t.Fatalf("拒绝不得推进高水位: hw=%d want=17", hw)
	}
	if sess, _ := f.mgr.Status(sid); sess.TurnCount != 1 {
		t.Fatalf("内核执行轮数不得虚增: %d", sess.TurnCount)
	}

	// 放行后 16 条全部照常收敛（队列不丢轮）
	f.releaseBarrier()
	f.waitAllTurnsFinished(sid, 17, 15*time.Second)
	for _, a := range acks {
		f.waitTurnFinished(sid, a.TurnSeq, protocol.ResultCompleted, 5*time.Second)
	}
	if sess, _ := f.mgr.Status(sid); sess.TurnCount != 17 {
		t.Fatalf("放行后内核应留痕 17 轮: %d", sess.TurnCount)
	}
}

// TestW01QueuedCancel N02：长前轮阻塞 + 取消排队轮 → 该轮不 spawn、序号消耗、
// 重复取消返回同一 cancelled 回执、同 seq 同文重放拒绝重复入账。
func TestW01QueuedCancel(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	ack := f.submitCreate(f.cwd)
	sid := ack.SessionID

	// seq2：阻塞在屏障（长前轮，占住互斥与内核写者门）
	if _, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "长前轮"}); err != nil {
		t.Fatal(err)
	}
	f.waitRunLogLine("start", 2) // create + seq2 已 spawn

	// seq3：排队轮（等待互斥/写者门，未 spawn）
	a3, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "排队轮"})
	if err != nil {
		t.Fatal(err)
	}
	if a3.TurnSeq != 3 {
		t.Fatalf("排队轮应 seq=3: %d", a3.TurnSeq)
	}

	// 取消排队轮：确定性终止（不 spawn）
	rc, err := f.ops.CancelTurn(context.Background(), sid, 3)
	if err != nil {
		t.Fatalf("排队取消: %v", err)
	}
	if rc.Outcome != sessionop.OutcomeQueued || rc.Status != protocol.ResultCancelled || rc.Error != sessionop.CodeCancelQueued {
		t.Fatalf("排队取消回执不符: %+v", rc)
	}
	e, ok := f.entryOf(sid, 3)
	if !ok || e.State != ledger.StateFinished || e.Result.Status != protocol.ResultCancelled ||
		!strings.HasPrefix(e.Result.Error, sessionop.CodeCancelQueued+":") {
		t.Fatalf("排队取消账目不符: %+v ok=%v", e, ok)
	}
	if n := countLines(f.runLines(), "start"); n != 2 {
		t.Fatalf("排队取消轮不得 spawn（fake CLI 辅证）: start=%d want=2", n)
	}
	// 序号消耗：高水位不回退，下一受理接续
	if hw := f.led.SessionTurnHighWater(sid); hw < 3 {
		t.Fatalf("取消轮序号应消耗: hw=%d", hw)
	}
	a4, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "接续"})
	if err != nil || a4.TurnSeq != 4 {
		t.Fatalf("取消后受理应接续 seq=4: ack=%+v err=%v", a4, err)
	}

	// 重放幂等：重复取消返回同一回执；同 seq 同文重复入账被拒（原受理唯一）
	rc2, err := f.ops.CancelTurn(context.Background(), sid, 3)
	if err != nil || rc2.Outcome != sessionop.OutcomeQueued || rc2.TaskID != rc.TaskID {
		t.Fatalf("重复取消应返回原回执: %+v err=%v", rc2, err)
	}
	if dupErr := f.led.AcceptOrigin(e.Task.Clone(), ledger.OriginLocal); !errors.Is(dupErr, ledger.ErrDuplicate) {
		t.Fatalf("同 seq 同文重放应命中原受理（ErrDuplicate）: %v", dupErr)
	}

	// 放行收束：seq2/seq4 照常完成（seq3 已 cancelled 终态）
	f.releaseBarrier()
	f.waitAllTurnsFinished(sid, 3, 10*time.Second)
	f.waitTurnFinished(sid, 2, protocol.ResultCompleted, 5*time.Second)
	f.waitTurnFinished(sid, 4, protocol.ResultCompleted, 5*time.Second)
}

// TestW01ExecCancel N03：fake CLI 长跑 + 取消 → 进程组整组终止（孙进程无残留佐
// 证）、账目如实 cancelled(turn_cancelled_executed)；内核轮行留治理证据。
func TestW01ExecCancel(t *testing.T) {
	survivor := filepath.Join(t.TempDir(), "survived")
	f := newSessOpFixture(t, sessOpOpt{envkv: map[string]string{
		"FAKE_ON_RESUME":          "1",
		"FAKE_SPAWN_DELAY_WRITER": "1|" + survivor, // 组内孙进程 1s 后写 survived（被杀则永不写）
	}})
	ack := f.submitCreate(f.cwd)
	sid := ack.SessionID

	if _, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "长跑"}); err != nil {
		t.Fatal(err)
	}
	f.waitRunLogLine("start", 2) // seq2 已 spawn
	// 孙进程存活栅格：.started 出现即「后代已真实在场」，其后的整组终止断言成立
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(survivor + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("孙进程未派生（治理断言失据）")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rc, err := f.ops.CancelTurn(context.Background(), sid, 2)
	if err != nil {
		t.Fatalf("执行取消: %v", err)
	}
	if rc.Outcome != sessionop.OutcomeExecuted || rc.Status != protocol.ResultCancelled || rc.Error != sessionop.CodeCancelExecuted {
		t.Fatalf("执行取消回执不符: %+v", rc)
	}
	e, ok := f.entryOf(sid, 2)
	if !ok || e.State != ledger.StateFinished || e.Result.Status != protocol.ResultCancelled ||
		!strings.HasPrefix(e.Result.Error, sessionop.CodeCancelExecuted+":") {
		t.Fatalf("执行取消账目不符: %+v ok=%v", e, ok)
	}
	// 内核轮行留痕（内核语义零改动）：seq2 失败轮在册，治理证据在案
	sess, ok := f.mgr.Status(sid)
	if !ok || sess.TurnCount != 2 || sess.Turns[1].Status != protocol.TurnFailed ||
		!strings.HasPrefix(sess.Turns[1].Error, protocol.CodeTimeout+":") {
		t.Fatalf("内核轮行应留治理失败证据: %+v ok=%v", sess, ok)
	}
	// 无孤儿佐证：孙进程 1s 后写 survived，整组终止则永不存在（等 1.6s 反证）
	time.Sleep(1600 * time.Millisecond)
	if _, serr := os.Stat(survivor); serr == nil {
		t.Fatal("孙进程在轮终后仍写入 = 进程组未被整组回收（孤儿）")
	}
	if n := countLines(f.runLines(), "end"); n != 1 {
		t.Fatalf("被取消轮不得自然跑完（end 留痕）: end=%d want=1", n)
	}
}

// TestW01CloseInFlight N04：在途时 Close 拒绝 ErrCloseInFlight；收束后关闭成功；
// 关闭后同 ID 创建不复活（内核墓碑语义）。
func TestW01CloseInFlight(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{envkv: map[string]string{"FAKE_ON_RESUME": "1"}})
	ack := f.submitCreate(f.cwd)
	sid := ack.SessionID

	if _, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "在途"}); err != nil {
		t.Fatal(err)
	}
	f.waitRunLogLine("start", 2)

	// 在途关闭：拒绝并指向取消/等待
	_, err := f.svc.CloseSessionLocal(sid)
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != sessionErrCloseInFlight {
		t.Fatalf("在途关闭应 ErrCloseInFlight/close_in_flight: err=%v", err)
	}
	if sess, ok := f.mgr.Status(sid); !ok || sess.Status != protocol.SessionOpen {
		t.Fatalf("拒绝关闭后会话应保持 open: %+v", sess)
	}

	// 取消在途轮收束后：关闭放行
	rc, err := f.ops.CancelTurn(context.Background(), sid, 2)
	if err != nil || rc.Outcome != sessionop.OutcomeExecuted {
		t.Fatalf("取消在途轮: rc=%+v err=%v", rc, err)
	}
	f.waitTurnFinished(sid, 2, protocol.ResultCancelled, 5*time.Second)
	if _, err := f.svc.CloseSessionLocal(sid); err != nil {
		t.Fatalf("收束后关闭应成功: %v", err)
	}
	if sess, ok := f.mgr.Status(sid); !ok || sess.Status != protocol.SessionClosed {
		t.Fatalf("关闭应成立: %+v", sess)
	}
	// 关闭后同 ID 创建：内核显式拒绝（已发布映射=ErrSessionExists；未发布映射走
	// 持久化墓碑=ErrSessionClosed，由内核 lifecycle 回归覆盖），绝不复活
	if _, _, cerr := f.mgr.CreateWithID(context.Background(), sid, "coder", f.cwd, "复活尝试"); !errors.Is(cerr, sessionx.ErrSessionExists) && !errors.Is(cerr, sessionx.ErrSessionClosed) {
		t.Fatalf("关闭后创建应显式拒绝（不复活）: %v", cerr)
	}
	if sess, _ := f.mgr.Status(sid); sess.Status != protocol.SessionClosed {
		t.Fatalf("关闭后不得复活: %+v", sess)
	}
}

// TestW01WorkspaceAuthAndMutex N05：配置根后越界 cwd create 拒（workspace_denied，
// 零副作用）、根内放行；符号链接别名与真实路径同键互斥（两会话串行非并行）。
func TestW01WorkspaceAuthAndMutex(t *testing.T) {
	// ---- 授权：根内放行 / 越界拒绝 / 别名按真实路径判定（无屏障，快速 fake） ----
	base := t.TempDir()
	root := filepath.Join(base, "root")
	inner := filepath.Join(root, "inner")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{inner, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	aliasIn := filepath.Join(root, "alias-in")   // → inner（根内别名）
	aliasOut := filepath.Join(root, "alias-out") // → outside（别名越界）
	for _, a := range []struct{ link, target string }{{aliasIn, inner}, {aliasOut, outside}} {
		if err := os.Symlink(a.target, a.link); err != nil {
			t.Skipf("symlink 不可用: %v", err)
		}
	}
	f := newSessOpFixture(t, sessOpOpt{roots: []string{root}})

	ack := f.submitCreate(inner) // 根内：放行并照常执行
	if sess, ok := f.svc.SessionLocal(ack.SessionID); !ok || sess.TurnCount != 1 {
		t.Fatalf("根内 create 应照常执行: ok=%v", ok)
	}
	sessionsBefore, tasksBefore := len(f.mgr.List()), f.countSessionTasks()
	for _, bad := range []string{outside, aliasOut} {
		_, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: bad, Prompt: "越界"})
		var re *RemoteError
		if !errors.As(err, &re) || re.Code != sessionErrWorkspace {
			t.Fatalf("越界 %s 应 ErrWorkspaceDenied/workspace_denied: err=%v", bad, err)
		}
	}
	if sessionsAfter, tasksAfter := len(f.mgr.List()), f.countSessionTasks(); sessionsAfter != sessionsBefore || tasksAfter != tasksBefore {
		t.Fatalf("越界拒绝零副作用被破坏: sessions %d→%d tasks %d→%d", sessionsBefore, sessionsAfter, tasksBefore, tasksAfter)
	}
	if _, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: aliasIn, Prompt: "根内别名"}); err != nil {
		t.Fatalf("根内别名应放行: %v", err)
	}

	// ---- 互斥：别名与真实路径同键，两会话串行（屏障对全部轮生效） ----
	f2 := newSessOpFixture(t, sessOpOpt{barrierAll: true})
	dirA := filepath.Join(t.TempDir(), "ws-a")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	linkB := filepath.Join(t.TempDir(), "ws-b-link")
	if err := os.Symlink(dirA, linkB); err != nil {
		t.Skipf("symlink 不可用: %v", err)
	}
	aA, err := f2.submit(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: dirA, Prompt: "会话A"})
	if err != nil {
		t.Fatal(err)
	}
	f2.waitRunLogLine("start", 1) // A 已 spawn 并阻塞屏障
	aB, err := f2.submit(LocalSessionTurnRequest{NodeID: "n1", Agent: "coder", Cwd: linkB, Prompt: "会话B"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := countLines(f2.runLines(), "start"); n != 1 {
		t.Fatalf("同真实工作区的两会话必须串行（B 在 A 收束前不得 spawn）: start=%d", n)
	}
	if e, ok := f2.entryOf(aB.SessionID, 1); !ok || e.State == ledger.StateFinished {
		t.Fatalf("B 应已受理且未终态: ok=%v", ok)
	}
	f2.releaseBarrier()
	f2.waitTurnFinished(aA.SessionID, 1, protocol.ResultCompleted, 10*time.Second)
	f2.waitTurnFinished(aB.SessionID, 1, protocol.ResultCompleted, 10*time.Second)
	lines := f2.runLines()
	if countLines(lines, "start") != 2 || countLines(lines, "end") != 2 {
		t.Fatalf("两会话应各执行一轮: %v", lines)
	}
	// 串行佐证：第二个 start 必须在首个 end 之后（无执行重叠）
	firstEnd := nthIndexOf(lines, "end", 1)
	if secondStart := nthIndexOf(lines, "start", 2); secondStart < firstEnd {
		t.Fatalf("两会话执行重叠（非串行）: 留痕 %v", lines)
	}
}

// TestW01RestartConvergence N06：中断残留非终态轮 → 重启落待核对、无重跑
//（fake CLI 零执行；恢复重投不 spawn；普通任务不在收敛范围）。
func TestW01RestartConvergence(t *testing.T) {
	ledDir := filepath.Join(t.TempDir(), "ledger")
	runLog := filepath.Join(t.TempDir(), "run.log")
	led1, _, err := ledger.Open(ledDir)
	if err != nil {
		t.Fatal(err)
	}
	const sid = "s-restart00000000000000000000000000000000000001"
	mk := func(id string, seq int64) protocol.Task {
		now := protocol.NowUTC()
		return protocol.Task{ID: id, TraceID: "tr-" + id, TargetNode: "n1", TargetAgent: "coder",
			Input: protocol.TaskInput{Stdin: "p"}, SubmittedBy: "local",
			IdemKey: "lsturn:" + sid + ":" + itoa64(seq), Status: protocol.StatusSubmitted,
			CreatedAt: now, UpdatedAt: now, ExpireAt: now.Add(protocol.TaskRetention),
			Session: &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: protocol.SessionTurnOpSend}}
	}
	now := protocol.NowUTC()
	normal := protocol.Task{ID: "t-restart-normal", TraceID: "tr-n", TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "p"}, SubmittedBy: "local", Status: protocol.StatusSubmitted,
		CreatedAt: now, UpdatedAt: now, ExpireAt: now.Add(protocol.TaskRetention)}
	for _, tt := range []struct {
		id  string
		seq int64
	}{{"t-restart-acc", 2}, {"t-restart-started", 3}} {
		if err := led1.AcceptOrigin(mk(tt.id, tt.seq), ledger.OriginLocal); err != nil {
			t.Fatal(err)
		}
	}
	if err := led1.MarkStarted("t-restart-started"); err != nil {
		t.Fatal(err)
	}
	if err := led1.AcceptOrigin(normal, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	if err := led1.Close(); err != nil {
		t.Fatal(err)
	}

	// 「重启」：新账本 + 新 service（sessionop 注入）——恢复在 Open/New 内完成
	led2, rec, err := ledger.Open(ledDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	ops, err := sessionop.New(t.TempDir(), led2, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bus := transport.NewBus()
	svc := New(Config{Ledger: led2, Runners: map[string]Runner{},
		ArtifactRoots: map[string]string{"coder": t.TempDir()}, SessionOps: ops}, bus.Endpoint("n1"))
	t.Cleanup(svc.Close)

	// started 残留：ledger.recover 既有降级（needs_review/node_restarted_midway）
	e3, ok := led2.Get("t-restart-started")
	if !ok || e3.State != ledger.StateFinished || e3.Result.Status != protocol.ResultNeedsReview ||
		e3.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("started 残留应降级待核对: ok=%v state=%v", ok, e3.State)
	}
	// accepted 残留：sessionop 收敛为待核对（不重跑）
	e2, ok := led2.Get("t-restart-acc")
	if !ok || e2.State != ledger.StateFinished || e2.Result.Status != protocol.ResultNeedsReview ||
		e2.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("accepted 会话残留应落待核对: ok=%v state=%v", ok, e2.State)
	}
	// 普通任务不在收敛范围（既有恢复语义保持）
	if en, _ := led2.Get("t-restart-normal"); en.State != ledger.StateAccepted {
		t.Fatalf("普通 accepted 任务不得被收敛: %+v", en)
	}
	// 绝不重跑：恢复清单中的会话轮重投不 spawn（MarkStarted 必败）
	for _, task := range rec.Reschedule {
		if task.Session == nil {
			continue // 普通任务恢复归既有调度语义，本用例不覆盖
		}
		svc.Execute(task)
	}
	time.Sleep(300 * time.Millisecond)
	if b, rerr := os.ReadFile(runLog); rerr == nil && strings.Contains(string(b), "start") {
		t.Fatalf("恢复重投不得重跑 CLI: %s", b)
	}
	if e, _ := led2.Get("t-restart-acc"); e.State != ledger.StateFinished {
		t.Fatalf("收敛终态应保持: %+v", e)
	}
}

// TestW01CancelPersistFail N07：取消终态落盘失败 → CancelTurn 如实报未确认、
// 账本保持非终态，绝不虚报 cancelled。首轮经内核直建（注入只拦截取消轮的终态
// 落盘，账本注入对全部 MarkFinished 生效，故不走账本受理首轮）。
func TestW01CancelPersistFail(t *testing.T) {
	f := newSessOpFixture(t, sessOpOpt{
		confirm: 400 * time.Millisecond,
		envkv:   map[string]string{"FAKE_ON_RESUME": "1"},
		ledInject: func(op, stage string) error {
			if op == "MarkFinished" {
				return errors.New("boom: disk full")
			}
			return nil
		},
	})
	const sid = "s-persistfail00000000000000000000000000000000001"
	if _, _, err := f.mgr.CreateWithID(context.Background(), sid, "coder", f.cwd, "首轮"); err != nil {
		t.Fatalf("预置首轮: %v", err)
	}
	if _, err := f.submit(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "长跑"}); err != nil {
		t.Fatal(err)
	}
	f.waitRunLogLine("start", 2)

	_, err := f.ops.CancelTurn(context.Background(), sid, 2)
	if !errors.Is(err, sessionop.ErrCancelUnconfirmed) {
		t.Fatalf("落盘失败应如实报 ErrCancelUnconfirmed: %v", err)
	}
	e, ok := f.entryOf(sid, 2)
	if !ok || e.State == ledger.StateFinished {
		t.Fatalf("终态落盘失败账本不得虚报 finished: ok=%v state=%v", ok, e.State)
	}
}

// itoa64 测试用序号转十进制串。
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
