package service

// sessionturn_t2r1_test.go T2-R1 审查退回修复轮回归（codex 探针场景入仓，node 侧）：
// 首轮执行前绑定断言同口核验（必修2，绝不带漂移绑定 spawn CLI）、关闭与创建
// 在途的生命周期门串行化（必修4）、本机并发提交轮号原子预留（建议3）。
// T2-R2 补充：适配器断言同口核验（必修2 补完）、平台轮号受理高水位（N4）。

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestT2R1CreatePermissionMismatchGuard 必修2：帧权限断言与本机规范化实际配置
// 不符 → 首轮拒绝（binding_changed），CLI 不执行、内核映射不建立——离线暂存
// 期间本机改配提档的场景在执行口拦截。
func TestT2R1CreatePermissionMismatchGuard(t *testing.T) {
	f := newSessionSvcFixture(t, nil)
	specs := f.specs()
	sp := specs["coder"]
	sp.PermissionMode = "danger-full-access" // 本机实际配置已提档（离线暂存期间改配形态）
	runLog := filepath.Join(t.TempDir(), "run.log")
	sp.Env = append(sp.Env, "FAKE_LOG="+runLog)
	specs["coder"] = sp
	mgr, err := sessionx.NewManager(t.TempDir(), specs, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr

	// 帧断言 read-only ≠ 本机 danger-full-access：拒绝，绝不 spawn CLI
	tk := sessionTask("t-t2r1-perm", "s-t2r1perm00000000000000000000000000000000000001", 1,
		protocol.SessionTurnOpCreate, f.cwd, "p", "read-only")
	f.dispatchOffer(tk)
	p := f.waitResult(tk.ID)
	if p.Status != protocol.ResultFailed || !strings.Contains(p.Result.Error, "binding_changed") {
		t.Fatalf("断言不符应 failed/binding_changed: %+v", p)
	}
	if p.Result.Turn == nil || p.Result.Turn.NativeIDOk {
		t.Fatalf("拒绝轮不得声称原生衔接: %+v", p.Result.Turn)
	}
	if _, ok := mgr.Status(tk.Session.SessionID); ok {
		t.Fatal("拒绝轮不得建立内核映射")
	}
	if b, rerr := os.ReadFile(runLog); rerr == nil && strings.Contains(string(b), "start") {
		t.Fatalf("断言不符绝不得执行 CLI: %s", b)
	}

	// 对照：断言缺省（空 = 信任本机配置，hub 能力卡未声明权限的形态）不误伤——
	// 照常执行；全权档位断言本身由 denylist 前置拒绝（既有 TestSessionTurnGuards
	// 覆盖），断言同口的正例由 TestSessionTurnCreateAndSend 覆盖。
	tk2 := sessionTask("t-t2r1-perm-ok", "s-t2r1permok0000000000000000000000000000000000001", 1,
		protocol.SessionTurnOpCreate, f.cwd, "p", "")
	f.dispatchOffer(tk2)
	p = f.waitResult(tk2.ID)
	if p.Status != protocol.ResultCompleted {
		t.Fatalf("空断言应照常执行（不误伤）: %+v", p)
	}
}

// TestT2R1CloseDuringCreateSerializes 必修4：创建在途（CLI 执行中）收到关闭——
// 共用写者门串行化，关闭等首轮收束后落定：close 回执 completed 属实、会话终局
// closed，绝不出现「关闭成功后会话 open 复活」。
func TestT2R1CloseDuringCreateSerializes(t *testing.T) {
	mark := t.TempDir() + "/start.mark"
	f := newSessionSvcFixture(t, nil)
	specs := f.specs()
	sp := specs["coder"]
	sp.Env = append(sp.Env, "FAKE_TOUCH="+mark, "FAKE_SLEEP=0.4")
	specs["coder"] = sp
	mgr, err := sessionx.NewManager(t.TempDir(), specs, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr

	const sid = "s-t2r1closeduringcreate00000000000000000000000000001"
	done := make(chan protocol.TaskResult, 1)
	go func() {
		done <- f.svc.runSessionTurn(context.Background(), sessionTask("t-t2r1-create", sid, 1,
			protocol.SessionTurnOpCreate, f.cwd, "p", "workspace-write"))
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首轮未起跑（无 TOUCH 标记）")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 首轮执行中收到关闭（hub close 任务形态）：同步等其收束
	closed := f.svc.runSessionTurn(context.Background(), sessionTask("t-t2r1-close", sid, 0,
		protocol.SessionTurnOpClose, "", "", ""))
	created := <-done
	if created.Status != protocol.ResultCompleted || closed.Status != protocol.ResultCompleted {
		t.Fatalf("创建与关闭都应完成（串行化而非互相吞没）: created=%+v closed=%+v", created, closed)
	}
	sess, ok := mgr.Status(sid)
	if !ok || sess.Status != protocol.SessionClosed {
		t.Fatalf("关闭应成立，会话不得以 open 复活: ok=%v status=%v", ok, sess.Status)
	}
	if sess.TurnCount != 1 {
		t.Fatalf("首轮留痕应保留: %+v", sess)
	}
}

// TestT2R1LocalConcurrentTurnSeqReserved 建议3（P3 修订）：本机并发续投——localMu
// 互斥段内「推导→入账」原子预留，多请求各得连续序号/唯一幂等键；执行收束以真实
// 账本为准：三条任务全部 finished（超时明确 Fail，不静默）、按回执 TaskID 读实际
// 账本条目核对受理 seq 与幂等键（回执自拼字段不承担落账证明）。首轮经内核直建
// 不入本地账本，不计入账本收束计数（内核留痕仍核 4 轮）。
func TestT2R1LocalConcurrentTurnSeqReserved(t *testing.T) {
	f := newSessionSvcFixture(t, nil)
	mgr, err := sessionx.NewManager(t.TempDir(), f.specs(), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr
	const sid = "s-t2r1localseq00000000000000000000000000000000000001"
	if _, _, err := mgr.CreateWithID(context.Background(), sid, "coder", f.cwd, "首轮"); err != nil {
		t.Fatalf("预置首轮: %v", err)
	}
	type ack struct {
		r *LocalTurnAck
		e error
	}
	acks := make(chan ack, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			r, e := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{
				NodeID: "n1", SessionID: sid, Prompt: "并发续投",
			})
			acks <- ack{r, e}
		}(i)
	}
	bySeq := map[int64]*LocalTurnAck{}
	for i := 0; i < 3; i++ {
		a := <-acks
		if a.e != nil {
			t.Fatalf("并发续投不应失败: %v", a.e)
		}
		if bySeq[a.r.TurnSeq] != nil {
			t.Fatalf("并发续投撞号: %d", a.r.TurnSeq)
		}
		bySeq[a.r.TurnSeq] = a.r
	}
	for seq := int64(2); seq <= 4; seq++ {
		if bySeq[seq] == nil {
			t.Fatalf("序号应预留 2..4, got %v", bySeq)
		}
	}
	// 执行收束：三条真实任务全部 finished/completed（bounded 等待，超时 Fail——
	// 内核已写满而账本收尾失败的假绿零容忍）
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := 0
		for _, a := range bySeq {
			if e, ok := f.led.Get(a.TaskID); ok && e.State == ledger.StateFinished &&
				e.Result != nil && e.Result.Status == protocol.ResultCompleted {
				n++
			}
		}
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("5s 内三条并发轮未全部 finished/completed（finished=%d/3）", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 实际账本条目核对：受理 seq 与幂等键以落账为准（非回执现场拼接）
	for seq, a := range bySeq {
		e, ok := f.led.Get(a.TaskID)
		if !ok {
			t.Fatalf("任务 %s 未入账", a.TaskID)
		}
		if e.Task.Session == nil || e.Task.Session.TurnSeq != seq {
			t.Fatalf("账本轮序号与回执不符: task=%s ledger_seq=%d ack_seq=%d", a.TaskID, e.Task.Session.TurnSeq, seq)
		}
		wantIdem := "lsturn:" + sid + ":" + strconv.FormatInt(seq, 10)
		if e.Task.IdemKey != wantIdem {
			t.Fatalf("落账幂等键不符: task=%s want=%s got=%s", a.TaskID, wantIdem, e.Task.IdemKey)
		}
	}
	sess, ok := mgr.Status(sid)
	if !ok || sess.TurnCount != 4 {
		t.Fatalf("内核应留痕 4 轮: ok=%v count=%d", ok, sess.TurnCount)
	}
}

// TestT2R2CreateAdapterDriftGuard 必修2 补完：权限不变而适配器漂移（hub 绑定快照
// codex，本机装配表已换 zcode）→ 首轮拒绝（binding_changed），CLI 不执行、内核
// 映射不建立——帧 adapter 断言与本机规范化配置同口核对，权限同口不代表可换绑：
// 适配器换了原生会话 ID 就会被交给另一 CLI。
func TestT2R2CreateAdapterDriftGuard(t *testing.T) {
	f := newSessionSvcFixture(t, nil)
	specs := f.specs()
	sp := specs["coder"]
	sp.Adapter = protocol.SessionAdapterZcode // 仅换适配器，权限保持 workspace-write
	sp.Env = []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterZcode,
		"FAKE_SESSION_ID=sess-r2drift", "FAKE_THREAD_ID=thr-r2drift"}
	specs["coder"] = sp
	runLog := filepath.Join(t.TempDir(), "run.log")
	sp2 := specs["coder"]
	sp2.Env = append(sp2.Env, "FAKE_LOG="+runLog)
	specs["coder"] = sp2
	mgr, err := sessionx.NewManager(t.TempDir(), specs, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr

	// 帧断言 adapter=codex（sessionTask 投影 hub 绑定快照）≠ 本机 zcode：拒绝
	tk := sessionTask("t-t2r2-adapter", "s-t2r2adapter000000000000000000000000000000000001", 1,
		protocol.SessionTurnOpCreate, f.cwd, "p", "workspace-write")
	if tk.Session.Adapter != protocol.SessionAdapterCodex {
		t.Fatalf("夹具应携带 hub 适配器快照: %+v", tk.Session)
	}
	f.dispatchOffer(tk)
	p := f.waitResult(tk.ID)
	if p.Status != protocol.ResultFailed || !strings.Contains(p.Result.Error, "binding_changed") {
		t.Fatalf("适配器漂移应 failed/binding_changed: %+v", p)
	}
	if p.Result.Turn == nil || p.Result.Turn.NativeIDOk {
		t.Fatalf("拒绝轮不得声称原生衔接: %+v", p.Result.Turn)
	}
	if _, ok := mgr.Status(tk.Session.SessionID); ok {
		t.Fatal("适配器漂移轮不得建立内核映射（静默换绑零容忍）")
	}
	if b, rerr := os.ReadFile(runLog); rerr == nil && strings.Contains(string(b), "start") {
		t.Fatalf("适配器漂移绝不得执行 CLI: %s", b)
	}
}

// TestT2R2LocalSeqHighWaterSurvivesCleanup N4：平台轮序号受理高水位独立持久——
// 已受理未执行（取消形态）轮经保留期清理后，本机续投不得复用其序号（与 hub
// 持久轮行同一口径）。受理（AcceptOrigin）即推进高水位，不依赖 CLI 执行次数
//（TurnCount）；清理后推导仍接续。
func TestT2R2LocalSeqHighWaterSurvivesCleanup(t *testing.T) {
	f := newSessionSvcFixture(t, nil)
	mgr, err := sessionx.NewManager(t.TempDir(), f.specs(), sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.sessions = mgr
	const sid = "s-t2r2hw00000000000000000000000000000000000000000001"
	sess, _, err := mgr.CreateWithID(context.Background(), sid, "coder", f.cwd, "首轮")
	if err != nil {
		t.Fatal(err)
	}
	// 平台受理 #2 后、未进 CLI 即取消（离线暂存取消/过期的受理留痕形态）
	now := protocol.NowUTC()
	task := sessionTask("t-t2r2hw-skipped", sid, 2, protocol.SessionTurnOpSend, f.cwd, "cancelled-before-execution", "")
	task.CreatedAt = now
	if err := f.led.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	if hw := f.led.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("受理即推进高水位: hw=%d want=2", hw)
	}
	if err := f.led.MarkFinished(task.ID, protocol.TaskResult{Status: protocol.ResultCancelled, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	if seq := localNextTurnSeq(sess, f.led, sid); seq != 3 {
		t.Fatalf("取消轮占用后下一号应 3: seq=%d（TurnCount=%d）", seq, sess.TurnCount)
	}
	// 保留期清理该条目：高水位不随条目消失回退
	n, err := f.led.CleanupAcked(now.Add(48*time.Hour), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("清理: n=%d err=%v", n, err)
	}
	ack, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "清理后续投"})
	if err != nil {
		t.Fatal(err)
	}
	if ack.TurnSeq != 3 {
		t.Fatalf("清理后受理序号复用 #2 = 与 hub 口径漂移: ack=%d TurnCount=%d want=3", ack.TurnSeq, sess.TurnCount)
	}
	if hw := f.led.SessionTurnHighWater(sid); hw != 3 {
		t.Fatalf("新受理应推进高水位: hw=%d want=3", hw)
	}
}
