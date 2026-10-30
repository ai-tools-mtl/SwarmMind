package sessionop

// sessionop_test.go 会话操作服务单元测试（W01-02 N03 证据分类 / N05 授权口径 /
// N07 请求登记持久化失败 / 重启收敛 / 队列与关闭守卫口径）：fake 账本（真
// ledger，t.TempDir）+ 故障注入，绝不真实模型/网络；时间 UTC。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// newTestService 夹具：真账本 + sessionop（可注入队列上限/允许根/确认预算）。
func newTestService(t *testing.T, opt Options) (*Service, *ledger.Ledger, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	led, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	if opt.Now == nil {
		opt.Now = protocol.NowUTC
	}
	s, err := New(dir, led, opt)
	if err != nil {
		t.Fatal(err)
	}
	return s, led, dir
}

// acceptSessionTask 入账一条会话受理轮（seq>0）。
func acceptSessionTask(t *testing.T, led *ledger.Ledger, id, sid string, seq int64) {
	t.Helper()
	now := protocol.NowUTC()
	task := protocol.Task{
		ID: id, TraceID: "tr-" + id, TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "p"}, SubmittedBy: "local",
		IdemKey: "lsturn:" + sid + ":" + itoa(seq),
		Status:  protocol.StatusSubmitted, CreatedAt: now, UpdatedAt: now,
		ExpireAt: now.Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: protocol.SessionTurnOpSend},
	}
	if err := led.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		t.Fatalf("入账 %s: %v", id, err)
	}
}

func itoa(n int64) string {
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

// TestAdmissionGuardQueueLimit N01 口径：非终态受理计数达上限即拒；终态不计。
func TestAdmissionGuardQueueLimit(t *testing.T) {
	s, led, _ := newTestService(t, Options{QueueLimit: 3})
	const sid = "s-queue0000000000000000000000000000000000000001"
	for i := int64(1); i <= 3; i++ {
		acceptSessionTask(t, led, "t-q-"+itoa(i), sid, i)
	}
	if err := s.AdmissionGuard(sid); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("第 4 条应 ErrQueueFull: %v", err)
	}
	// 其余会话不受影响
	if err := s.AdmissionGuard("s-other0000000000000000000000000000000000001"); err != nil {
		t.Fatalf("其他会话不应受限: %v", err)
	}
	// 终态不计：收敛一条后再受理放行
	e, _ := findTurnEntry(led, sid, 1)
	if err := led.MarkFinished(e.Task.ID, protocol.TaskResult{Status: protocol.ResultCancelled, FinishedAt: protocol.NowUTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmissionGuard(sid); err != nil {
		t.Fatalf("终态收敛后应放行: %v", err)
	}
}

// TestCloseGuardInFlight N04 口径：存在非终态受理轮即拒绝关闭；收束后放行。
func TestCloseGuardInFlight(t *testing.T) {
	s, led, _ := newTestService(t, Options{})
	const sid = "s-closeg000000000000000000000000000000000000001"
	acceptSessionTask(t, led, "t-cg-1", sid, 2)
	if err := s.CloseGuard(sid); !errors.Is(err, ErrCloseInFlight) {
		t.Fatalf("在途应 ErrCloseInFlight: %v", err)
	}
	e, _ := findTurnEntry(led, sid, 2)
	if err := led.MarkFinished(e.Task.ID, protocol.TaskResult{Status: protocol.ResultCompleted, FinishedAt: protocol.NowUTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseGuard(sid); err != nil {
		t.Fatalf("收束后应放行: %v", err)
	}
}

// TestAuthorizeWorkspaceRoots N05 口径：空根放行；配置根后根内放行、越界拒绝、
// 符号链接别名按 EvalSymlinks 真实路径判定（别名入根放行、别名出根拒绝）。
func TestAuthorizeWorkspaceRoots(t *testing.T) {
	// 空 = 放行（现状保持）
	s0, _, _ := newTestService(t, Options{})
	if err := s0.AuthorizeWorkspace("/anywhere/at/all"); err != nil {
		t.Fatalf("未配置根应放行: %v", err)
	}

	base := t.TempDir()
	root := filepath.Join(base, "root")
	inner := filepath.Join(root, "inner")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{inner, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	aliasIn := filepath.Join(root, "alias-in")  // → inner（根内别名）
	aliasOut := filepath.Join(root, "alias-out") // → outside（别名越界）
	for _, a := range []struct{ link, target string }{{aliasIn, inner}, {aliasOut, outside}} {
		if err := os.Symlink(a.target, a.link); err != nil {
			t.Skipf("symlink 不可用: %v", err)
		}
	}
	s, _, _ := newTestService(t, Options{AllowedWorkspaceRoots: []string{root}})
	for _, ok := range []string{root, inner, aliasIn} {
		if err := s.AuthorizeWorkspace(ok); err != nil {
			t.Fatalf("根内 %s 应放行: %v", ok, err)
		}
	}
	for _, bad := range []string{outside, aliasOut, filepath.Join(base, "elsewhere")} {
		if err := s.AuthorizeWorkspace(bad); !errors.Is(err, ErrWorkspaceDenied) {
			t.Fatalf("越界 %s 应 ErrWorkspaceDenied: %v", bad, err)
		}
	}
	// 前缀边界：同前缀的同级目录不算根内
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeWorkspace(sibling); !errors.Is(err, ErrWorkspaceDenied) {
		t.Fatalf("同名前缀同级目录应拒绝: %v", err)
	}
}

// TestAuthorizeWorkspaceRootEdgeAndFallback codex 联审必修项回归：根为文件系统根
// "/" 时不得因 "root+分隔符" 拼出 "//" 伪前缀误拒全部子目录；解析失败（cwd 不
// 存在，EvalSymlinks 失败）按 Abs+Clean 回退结果判定——根内回退放行、根外回退
// 仍拒绝（授权不因回退放行越界路径）。
func TestAuthorizeWorkspaceRootEdgeAndFallback(t *testing.T) {
	sRoot, _, _ := newTestService(t, Options{AllowedWorkspaceRoots: []string{"/"}})
	for _, ok := range []string{"/tmp", "/tmp/sessions/a", "/"} {
		if err := sRoot.AuthorizeWorkspace(ok); err != nil {
			t.Fatalf("根 / 下 %s 应放行: %v", ok, err)
		}
	}

	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, _, _ := newTestService(t, Options{AllowedWorkspaceRoots: []string{root}})
	// 回退放行：根内不存在的 cwd（EvalSymlinks 失败）按 Abs+Clean 判定入根
	nonexistentIn := filepath.Join(root, "not-yet-created")
	if err := s.AuthorizeWorkspace(nonexistentIn); err != nil {
		t.Fatalf("根内不存在目录（回退路径）应放行: %v", err)
	}
	// 回退拒绝：根外不存在的 cwd 仍拒绝
	nonexistentOut := filepath.Join(base, "elsewhere", "missing")
	if err := s.AuthorizeWorkspace(nonexistentOut); !errors.Is(err, ErrWorkspaceDenied) {
		t.Fatalf("根外不存在目录（回退路径）应 ErrWorkspaceDenied: %v", err)
	}
}

// TestResolveRequestLifecycle N01 同源幂等语义：同键同文返回原受理；同键异文
// ErrRequestConflict；Lookup 可查回。
func TestResolveRequestLifecycle(t *testing.T) {
	s, _, dir := newTestService(t, Options{})
	const rid = "req-1"
	rec, dup, err := s.ResolveRequest(rid, "s-a", 1, "hash-1")
	if err != nil || dup {
		t.Fatalf("首次登记: rec=%+v dup=%v err=%v", rec, dup, err)
	}
	if rec.SessionID != "s-a" || rec.TurnSeq != 1 || rec.PayloadHash != "hash-1" {
		t.Fatalf("登记内容不符: %+v", rec)
	}
	if rec.AcceptedAt.IsZero() || !rec.AcceptedAt.UTC().Equal(rec.AcceptedAt) {
		t.Fatalf("受理时刻应非零 UTC: %+v", rec)
	}
	got, dup, err := s.ResolveRequest(rid, "s-a", 1, "hash-1")
	if err != nil || !dup {
		t.Fatalf("同键同文应 dup=true 返回原受理: dup=%v err=%v", dup, err)
	}
	if got.SessionID != rec.SessionID || got.TurnSeq != rec.TurnSeq || got.PayloadHash != rec.PayloadHash {
		t.Fatalf("原受理不一致: got=%+v want=%+v", got, rec)
	}
	if _, _, err := s.ResolveRequest(rid, "s-b", 1, "hash-1"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("同键异 session 应冲突: %v", err)
	}
	if _, _, err := s.ResolveRequest(rid, "s-a", 2, "hash-1"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("同键异 seq 应冲突: %v", err)
	}
	if _, _, err := s.ResolveRequest(rid, "s-a", 1, "hash-2"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("同键异文应冲突: %v", err)
	}
	if _, ok := s.LookupRequest(rid); !ok {
		t.Fatal("Lookup 应查回登记")
	}
	// 落盘断言：文件存在且含登记
	b, err := os.ReadFile(filepath.Join(dir, requestsFileName))
	if err != nil || !strings.Contains(string(b), rid) {
		t.Fatalf("登记应持久化: err=%v body=%s", err, b)
	}
	if _, _, err := s.ResolveRequest("", "s-a", 1, "h"); err == nil {
		t.Fatal("空 request_id 应拒绝")
	}
}

// TestResolveRequestPersistFailureNotRetained N07：登记落盘失败 → 报错且内存登记
// 不保留（盘为真相），重试可成功——绝不虚报登记成功。
func TestResolveRequestPersistFailureNotRetained(t *testing.T) {
	s, _, dir := newTestService(t, Options{})
	boom := errors.New("boom: disk full")
	s.reqMu.Lock()
	s.reqInject = func(string) error { return boom }
	s.reqMu.Unlock()
	if _, _, err := s.ResolveRequest("req-x", "s-a", 1, "h"); !errors.Is(err, boom) {
		t.Fatalf("落盘失败应如实报错: %v", err)
	}
	if _, ok := s.LookupRequest("req-x"); ok {
		t.Fatal("落盘失败后内存登记不得保留（盘为真相）")
	}
	s.reqMu.Lock()
	s.reqInject = nil
	s.reqMu.Unlock()
	rec, dup, err := s.ResolveRequest("req-x", "s-a", 1, "h")
	if err != nil || dup {
		t.Fatalf("清除注入后重试应成功: dup=%v err=%v", dup, err)
	}
	if rec.TurnSeq != 1 || rec.SessionID != "s-a" {
		t.Fatalf("重试登记内容不符: %+v", rec)
	}
	if _, ok := s.LookupRequest("req-x"); !ok {
		t.Fatal("重试登记应可查回")
	}
	if b, rerr := os.ReadFile(filepath.Join(dir, requestsFileName)); rerr != nil || !strings.Contains(string(b), "req-x") {
		t.Fatalf("重试登记应落盘: err=%v body=%s", rerr, b)
	}
}

// TestClassifyCancelTurn N03 证据口径：治理标记+退出码-1=executed；退出码-1 无
// 治理标记=unknown（待核对，绝不虚报）；无内核轮行=queued；真实退出码=natural。
func TestClassifyCancelTurn(t *testing.T) {
	governance := protocol.CodeTimeout + ": context 已取消或到期，进程已整组终止，原生侧状态不确定"
	cases := []struct {
		name string
		turn *protocol.Turn
		want CancelClass
	}{
		{"无内核轮行=未spawn", nil, CancelClassQueued},
		{"治理整组终止=有停止证据", &protocol.Turn{ExitCode: protocol.ExitCodeNotRun, Error: governance}, CancelClassExecuted},
		{"退出码-1无治理标记=证据缺失", &protocol.Turn{ExitCode: protocol.ExitCodeNotRun, Error: "spawn_failed: exec: no such file"}, CancelClassUnknown},
		{"真实退出码=自然终态", &protocol.Turn{ExitCode: 0, Status: protocol.TurnOK, Error: ""}, CancelClassNatural},
		{"非零退出码=自然失败", &protocol.Turn{ExitCode: 3, Status: protocol.TurnFailed, Error: "exit_code=3: boom"}, CancelClassNatural},
	}
	for _, c := range cases {
		if got := ClassifyCancelTurn(c.turn); got != c.want {
			t.Fatalf("%s: got=%d want=%d", c.name, got, c.want)
		}
	}
}

// TestCancelReceiptMapping 回执映射：取消类记账码出对应 outcome；自然终态先到
// 保留（ErrTurnFinished）。
func TestCancelReceiptMapping(t *testing.T) {
	if r, err := receiptOf("s-a", 3, "t-1", protocol.ResultCancelled, CodeCancelQueued+": 未 spawn"); err != nil || r.Outcome != OutcomeQueued || r.Status != protocol.ResultCancelled || r.Error != CodeCancelQueued {
		t.Fatalf("queued 回执: %+v err=%v", r, err)
	}
	if r, err := receiptOf("s-a", 3, "t-1", protocol.ResultCancelled, CodeCancelExecuted+": 已终止"); err != nil || r.Outcome != OutcomeExecuted {
		t.Fatalf("executed 回执: %+v err=%v", r, err)
	}
	if r, err := receiptOf("s-a", 3, "t-1", protocol.ResultFailed, CodeCancelOutcomeUnknown+": 待核对"); err != nil || r.Outcome != OutcomeUnknown || r.Status != protocol.ResultFailed {
		t.Fatalf("unknown 回执: %+v err=%v", r, err)
	}
	if _, err := receiptOf("s-a", 3, "t-1", protocol.ResultCompleted, ""); !errors.Is(err, ErrTurnFinished) {
		t.Fatalf("自然完成轮取消应先到保留: %v", err)
	}
	if _, err := receiptOf("s-a", 3, "t-1", protocol.ResultFailed, "turn_failed: exit_code=2"); !errors.Is(err, ErrTurnFinished) {
		t.Fatalf("自然失败轮取消应先到保留: %v", err)
	}
}

// TestRecoverUncertainTurns N06 口径：accepted 会话受理轮收敛为待核对终态且不触
// 普通任务；幂等（二次收敛零变更）。
func TestRecoverUncertainTurns(t *testing.T) {
	s, led, dir := newTestService(t, Options{})
	const sid = "s-recover0000000000000000000000000000000000001"
	acceptSessionTask(t, led, "t-rc-sess", sid, 2)
	// 普通任务不受收敛影响
	now := protocol.NowUTC()
	normal := protocol.Task{ID: "t-rc-normal", TraceID: "tr-n", TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "p"}, SubmittedBy: "local", Status: protocol.StatusSubmitted,
		CreatedAt: now, UpdatedAt: now, ExpireAt: now.Add(protocol.TaskRetention)}
	if err := led.AcceptOrigin(normal, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverUncertainTurns(protocol.NowUTC())
	if err != nil || n != 1 {
		t.Fatalf("应收敛 1 条会话轮: n=%d err=%v", n, err)
	}
	e, ok := led.Get("t-rc-sess")
	if !ok || e.State != ledger.StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultNeedsReview || e.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("会话受理轮应落待核对终态: %+v ok=%v", e, ok)
	}
	if e2, ok := led.Get("t-rc-normal"); !ok || e2.State != ledger.StateAccepted {
		t.Fatalf("普通任务不得被收敛: state=%v", e2.State)
	}
	// 幂等：二次收敛零变更
	if n2, err := s.RecoverUncertainTurns(protocol.NowUTC()); err != nil || n2 != 0 {
		t.Fatalf("二次收敛应零变更: n=%d err=%v", n2, err)
	}
	// 收敛落盘（重启可见）
	_ = led.Close()
	led2, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	if e3, ok := led2.Get("t-rc-sess"); !ok || e3.State != ledger.StateFinished {
		t.Fatalf("收敛应持久化: %+v", e3)
	}
}

// TestCancelTurnTurnNotFound 未受理轮取消 → ErrTurnUnknown；正 seq 约束。
func TestCancelTurnTurnNotFound(t *testing.T) {
	s, _, _ := newTestService(t, Options{ConfirmBudget: 300 * time.Millisecond})
	ctx := context.Background()
	if _, err := s.CancelTurn(ctx, "s-none00000000000000000000000000000000000001", 5); !errors.Is(err, ErrTurnUnknown) {
		t.Fatalf("无受理记录应 ErrTurnUnknown: %v", err)
	}
	if _, err := s.CancelTurn(ctx, "s-x", 0); err == nil {
		t.Fatal("seq<=0 应拒绝")
	}
}
