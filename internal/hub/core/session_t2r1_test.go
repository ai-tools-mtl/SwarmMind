package core

// session_t2r1_test.go T2-R1 审查退回修复轮回归（codex 探针场景全部入仓）：
// 并发缺省轮号事务内分配（必修1）、超长输入尾部差异 409（必修5）、重放原回执
// 稳定含任务清理后（必修6）、创建断言事务内重检（必修3）、补投复核拦截旧版
// 节点降级（必修7）、并发 close 幂等回执（建议1）。
//
// 时钟钩子纪律：blockNowTwice 挂上的 c.now 覆盖不恢复——钩子在前 N 次调用后
// 直通 protocol.NowUTC，夹具按测试新建互不干扰；测试中途写 c.now 字段会与并发
// 提交 goroutine 的字段读构成数据竞争，故绝不中途恢复。

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// blockNowTwice 返回放行函数：前两次 now() 调用（两个并发请求各自的 newTask
// 预检段）阻塞至放行——两请求都完成事务外快照后才进事务，构造「同视图并发
// 提交」的确定性交错。调用方在两请求都受理完成后才可返回（返回即放行）。
func blockNowTwice(c *Core) (release func(), waitReady func(t *testing.T)) {
	var calls atomic.Int32
	ready := make(chan struct{}, 2)
	releaseCh := make(chan struct{})
	c.now = func() time.Time {
		if calls.Add(1) <= 2 {
			ready <- struct{}{}
			<-releaseCh
		}
		return protocol.NowUTC()
	}
	return func() { close(releaseCh) }, func(t *testing.T) {
		done := make(chan struct{})
		go func() { <-ready; <-ready; close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("并发请求未同时到达事务外快照段")
		}
	}
}

// newOfflineSession 夹具速建：离线节点 + 会话能力卡 + 空会话（离线 = 提交只入
// 信箱，无派发干扰；需要补投场景再 SetOnline）。
func newOfflineSession(t *testing.T) (*sessionFixture, string) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", false)
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	return f, sv.ID
}

// TestT2R1ConcurrentAutoSeqDistinct 必修1：首轮成功后两个并发缺省序号提交——
// 序号在写事务内分配，两回执序号不同、任务/幂等键不同、轮行 (session_id,seq)
// 唯一且连续。
func TestT2R1ConcurrentAutoSeqDistinct(t *testing.T) {
	f, sid := newOfflineSession(t)
	// 预置成功首轮（seq=1 已占）
	if err := f.store.Update(func(st *store.State) error {
		s := st.Sessions[sid]
		s.Turns = []store.SessionTurn{{Seq: 1, NativeIDOk: true, FinishedAt: time.Now()}}
		st.Sessions[sid] = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, waitReady := blockNowTwice(f.core)
	type out struct {
		ref *TurnRef
		err error
	}
	outs := make(chan out, 2)
	for _, p := range []string{"a", "b"} {
		go func(p string) {
			r, _, e := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: p}, "admin")
			outs <- out{r, e}
		}(p)
	}
	waitReady(t)
	release()
	a, b := <-outs, <-outs
	if a.err != nil || b.err != nil {
		t.Fatalf("并发提交不应失败: %v %v", a.err, b.err)
	}
	if a.ref.TurnSeq == b.ref.TurnSeq || a.ref.TaskID == b.ref.TaskID {
		t.Fatalf("同会话并发提交不得撞号/撞任务: %+v %+v", a.ref, b.ref)
	}
	view, ok := f.core.GetSession(sid)
	if !ok || len(view.Turns) != 3 {
		t.Fatalf("应有 3 轮: ok=%v n=%d", ok, len(view.Turns))
	}
	seqs := map[int64]bool{}
	for _, tr := range view.Turns {
		if seqs[tr.Seq] {
			t.Fatalf("轮行序号重复: %+v", view.Turns)
		}
		seqs[tr.Seq] = true
	}
	for seq := int64(1); seq <= 3; seq++ {
		if !seqs[seq] {
			t.Fatalf("序号应连续 1..3, got %v", seqs)
		}
	}
	st := f.store.View()
	idems := map[string]bool{}
	for _, tr := range view.Turns {
		if tr.TaskID == "" {
			continue // 预置合成轮（无任务载体），不参与幂等键核对
		}
		tk := st.Tasks[tr.TaskID]
		if tk.IdemKey != turnIdemKey(sid, tr.Seq) {
			t.Fatalf("幂等键未随事务内序号同源: seq=%d idem=%s", tr.Seq, tk.IdemKey)
		}
		if idems[tk.IdemKey] {
			t.Fatalf("幂等键重复: %s", tk.IdemKey)
		}
		idems[tk.IdemKey] = true
	}
}

// TestT2R1PromptSuffixConflict 必修5：8KiB 展示截断不参与幂等判定——同号尾部
// 异文必须 409（含任务清理后，指纹随轮行保留）；尾部同文重放 dup 返原回执。
func TestT2R1PromptSuffixConflict(t *testing.T) {
	f, sid := newOfflineSession(t)
	prefix := strings.Repeat("x", store.MaxSessionTurnPrompt)
	orig, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: prefix + "A", TurnSeq: 1}, "admin")
	if err != nil || dup {
		t.Fatalf("首轮提交: dup=%v err=%v", dup, err)
	}
	// 同号尾部异文：409（截断前缀一致也绝不吞为重放）
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: prefix + "B", TurnSeq: 1}, "admin"); !errors.Is(err, ErrSessionTurnConflict) {
		t.Fatalf("尾部异文应 ErrSessionTurnConflict, got %v", err)
	}
	// 同号同文重放：dup + 原回执
	replay, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: prefix + "A", TurnSeq: 1}, "admin")
	if err != nil || !dup || replay.TaskID != orig.TaskID || replay.TraceID != orig.TraceID || replay.Op != orig.Op {
		t.Fatalf("同文重放应 dup 返原回执: ref=%+v dup=%v err=%v", replay, dup, err)
	}
	// 展示留痕截断、指纹完整落行
	tr, ok := findTurn(f.store.View().Sessions[sid], 1)
	if !ok {
		t.Fatal("轮行缺失")
	}
	if len(tr.Prompt) > store.MaxSessionTurnPrompt {
		t.Fatalf("展示留痕应截断: %d", len(tr.Prompt))
	}
	if tr.PromptHash == "" || tr.PromptHash != promptFingerprint(prefix+"A") {
		t.Fatalf("行内指纹异常: %q", tr.PromptHash)
	}
	// 任务清理（模拟保留期清扫）后：指纹仍在行内，判定不变
	if err := f.store.Update(func(st *store.State) error {
		delete(st.Tasks, orig.TaskID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: prefix + "B", TurnSeq: 1}, "admin"); !errors.Is(err, ErrSessionTurnConflict) {
		t.Fatalf("任务清理后尾部异文仍应 409, got %v", err)
	}
	if r, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: prefix + "A", TurnSeq: 1}, "admin"); err != nil || !dup || r.TraceID != orig.TraceID {
		t.Fatalf("任务清理后同文重放仍应返原回执: %+v dup=%v err=%v", r, dup, err)
	}
}

// TestT2R1ReplayReceiptStable 必修6：重放返回原轮受理回执——首轮终态后重放
// 仍是 create/原 trace_id（不随会话现态推导改写）。
func TestT2R1ReplayReceiptStable(t *testing.T) {
	f, sid := newOfflineSession(t)
	orig, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "p", TurnSeq: 1}, "admin")
	if err != nil {
		t.Fatalf("首轮提交: %v", err)
	}
	if orig.Op != protocol.SessionTurnOpCreate || orig.TraceID == "" {
		t.Fatalf("原回执异常: %+v", orig)
	}
	// 轮终（create 成立： NativeIDOk 落行）后重放
	if err := f.store.Update(func(st *store.State) error {
		cur := st.Sessions[sid]
		cur.Turns[0].NativeIDOk = true
		cur.Turns[0].FinishedAt = time.Now()
		st.Sessions[sid] = cur
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	replay, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "p", TurnSeq: 1}, "admin")
	if err != nil || !dup {
		t.Fatalf("重放: dup=%v err=%v", dup, err)
	}
	if replay.Op != orig.Op || replay.TraceID != orig.TraceID || replay.TaskID != orig.TaskID {
		t.Fatalf("重放回执改变: original=%+v replay=%+v", orig, replay)
	}
}

// TestT2R1CreateAssertionRecheckedInTransaction 必修3：预检后能力卡在创建竞态中
// 提档（workspace-write → danger-full-access），事务内基于同一张当前卡重检不符
// → 创建整体拒绝、注册表零发布。
func TestT2R1CreateAssertionRecheckedInTransaction(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", false)
	// now() 首调在 CreateSession 预检之后、写事务之前（now := c.now() 段）——
	// 恰好构造「预检读旧卡、事务读新卡」的竞态窗口。
	var swapped atomic.Bool
	f.core.now = func() time.Time {
		if !swapped.Swap(true) {
			f.registerSessionNode("codex", "danger-full-access", false) // 竞态提档
		}
		return protocol.NowUTC()
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", PermissionMode: "workspace-write"}, "admin"); !errors.Is(err, ErrSessionBindingChanged) {
		t.Fatalf("事务内断言重检应拒绝, got %v", err)
	}
	if n := len(f.store.View().Sessions); n != 0 {
		t.Fatalf("拒绝事务不得发布会话, 注册表=%d", n)
	}
}

// TestT2R1CloseConcurrentDup 建议1：并发 close 收敛零写入——恰一条 close 任务、
// 恰一次记账；落库方 dup=false、收敛方 dup=true（与既有重复 close 幂等口径一致）。
func TestT2R1CloseConcurrentDup(t *testing.T) {
	f, sid := newOfflineSession(t)
	release, waitReady := blockNowTwice(f.core)
	type out struct {
		dup bool
		err error
	}
	outs := make(chan out, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, d, e := f.core.CloseSession(sid, "admin")
			outs <- out{d, e}
		}()
	}
	waitReady(t)
	release()
	a, b := <-outs, <-outs
	if a.err != nil || b.err != nil {
		t.Fatalf("close 不应失败: %v %v", a.err, b.err)
	}
	if a.dup == b.dup {
		t.Fatalf("恰一方落库（dup=false），收敛方 dup=true: %v,%v", a.dup, b.dup)
	}
	closeTasks := 0
	for _, tk := range f.store.View().Tasks {
		if tk.Session != nil && tk.Session.Op == protocol.SessionTurnOpClose {
			closeTasks++
		}
	}
	if closeTasks != 1 {
		t.Fatalf("close 任务应恰一条, got %d", closeTasks)
	}
	f.core.szMu.Lock()
	n := len(f.core.sizes)
	f.core.szMu.Unlock()
	if n != 1 {
		t.Fatalf("记账应恰一条, got %d", n)
	}
}

// TestT2R1DispatchRecheckGatesDowngrade 必修7：补投出队事务复核——离线暂存期间
// 节点回退旧版/撤声明/改配，上线补投不再下发会话任务；任务+轮行同事务 failed
// 留账；能力与绑定未变时正常补投（对照）。
func TestT2R1DispatchRecheckGatesDowngrade(t *testing.T) {
	t.Run("capability_revoked_turn", func(t *testing.T) {
		f, sid := newOfflineSession(t)
		ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "offline"}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		f.registerSessionNode("", "workspace-write", false) // 撤回会话声明（旧版回退形态）
		f.core.SetOnline("mac-mini", true)
		f.assertNoOffer(t)
		f.assertTurnFailed(t, ref, sid, "session_unsupported")
	})
	t.Run("binding_drift_turn", func(t *testing.T) {
		f, sid := newOfflineSession(t)
		ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "offline"}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		f.registerSessionNode("zcode", "workspace-write", false) // 适配器漂移
		f.core.SetOnline("mac-mini", true)
		f.assertNoOffer(t)
		f.assertTurnFailed(t, ref, sid, "binding_changed")
	})
	t.Run("capability_revoked_close", func(t *testing.T) {
		f, sid := newOfflineSession(t)
		if _, _, err := f.core.CloseSession(sid, "admin"); err != nil {
			t.Fatal(err)
		}
		f.registerSessionNode("", "workspace-write", false)
		f.core.SetOnline("mac-mini", true)
		f.assertNoOffer(t) // close 任务同样不投旧版节点（仅能力复核，不核对 closed 快照）
		for _, tk := range f.store.View().Tasks {
			if tk.Session != nil && tk.Session.Op == protocol.SessionTurnOpClose {
				if !tk.Status.Terminal() || tk.Status != protocol.StatusFailed ||
					!strings.HasPrefix(tk.Result.Error, "session_unsupported") {
					t.Fatalf("close 任务应留账失败: %+v", tk)
				}
			}
		}
	})
	t.Run("normal_dispatch_when_capable", func(t *testing.T) {
		f, sid := newOfflineSession(t)
		ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "offline"}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		f.core.SetOnline("mac-mini", true) // 能力与绑定未变：正常补投
		offer := f.waitOffer()
		var offered protocol.Task
		if err := json.Unmarshal(offer.Payload, &offered); err != nil || offered.ID != ref.TaskID {
			t.Fatalf("正常补投不得被拦: %v", err)
		}
	})
}

// assertNoOffer 断言窗口期内无 task_offer（复核拦截 = 不投递）。
func (f *sessionFixture) assertNoOffer(t *testing.T) {
	t.Helper()
	select {
	case env := <-f.offers:
		t.Fatalf("复核不符仍下发 offer: %s", env.Type)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestT2R2MixedQueueSessionRejectKeepsDispatching N3：被拒会话轮不得截断同队列
// 普通任务的投递——混合队列（会话轮在前、普通任务在后）+ 节点撤回会话声明上线：
// 会话任务留账 failed 旁路通知后，投递循环继续，普通任务照常收到 offer
//（「普通任务路径零变化」含投递循环不被截断）。
func TestT2R2MixedQueueSessionRejectKeepsDispatching(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "session"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := protocol.Task{ID: "t-t2r2-ordinary", TargetNode: "mac-mini", TargetAgent: "coder",
		Status: protocol.StatusSubmitted, Input: protocol.TaskInput{Stdin: "ordinary"},
		CreatedAt: time.Now(), ExpireAt: time.Now().Add(time.Hour)}
	if err := f.store.Update(func(st *store.State) error {
		st.Tasks[ordinary.ID] = ordinary
		return mailbox.Enqueue(st, "mac-mini", &ordinary)
	}); err != nil {
		t.Fatal(err)
	}
	f.registerSessionNode("", "workspace-write", false) // 撤回会话声明
	f.core.SetOnline("mac-mini", true)
	// 普通任务必须照常派发（会话轮被拒只旁路自身，不截断队列）
	offer := f.waitOffer()
	var offered protocol.Task
	if err := json.Unmarshal(offer.Payload, &offered); err != nil || offered.ID != ordinary.ID {
		t.Fatalf("被拒会话轮之后的普通任务应照常派发: err=%v offered=%s", err, offered.ID)
	}
	f.assertTurnFailed(t, ref, sid, "session_unsupported")
}

// TestT2R2CreateFrameCarriesAdapterSnapshot 必修2 补完：hub 下发的 create 帧
// 携带绑定快照的适配器断言（与权限快照成组），node 执行入口据其同口核验——
// 权限不变而适配器漂移的静默换绑反例由此封堵。
func TestT2R2CreateFrameCarriesAdapterSnapshot(t *testing.T) {
	f, sid := newOfflineSession(t)
	ref, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "p", TurnSeq: 1}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("mac-mini", true)
	offer := f.waitOffer()
	var offered protocol.Task
	if err := json.Unmarshal(offer.Payload, &offered); err != nil {
		t.Fatal(err)
	}
	if offered.ID != ref.TaskID || offered.Session == nil {
		t.Fatalf("offer 任务异常: offered=%s session=%+v", offered.ID, offered.Session)
	}
	if offered.Session.Adapter != "codex" || offered.Session.PermissionMode != "workspace-write" {
		t.Fatalf("create 帧应携带绑定快照 (adapter/permission)=(codex/workspace-write): %+v", offered.Session)
	}
}

// assertTurnFailed 断言任务与轮行同事务 failed 留账（错误码前缀匹配）。
func (f *sessionFixture) assertTurnFailed(t *testing.T, ref *TurnRef, sessionID, code string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, ok := f.core.GetTask(ref.TaskID)
		if ok && tk.Status.Terminal() {
			if tk.Status != protocol.StatusFailed || !strings.HasPrefix(tk.Result.Error, code) {
				t.Fatalf("任务应 failed/%s: got=%+v", code, tk.Result.Error)
			}
			sv, ok := f.core.GetSession(sessionID)
			if !ok || len(sv.Turns) == 0 {
				t.Fatalf("轮行缺失: ok=%v", ok)
			}
			last := sv.Turns[len(sv.Turns)-1]
			if !last.Terminal() || last.Status != store.TurnStatusFailed || !strings.HasPrefix(last.Error, code) {
				t.Fatalf("轮行应 failed/%s: %+v", code, last)
			}
			if last.NativeIDOk {
				t.Fatalf("拦截轮不得声称原生衔接: %+v", last)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 2s 内未终态 task=%s", ref.TaskID)
}
