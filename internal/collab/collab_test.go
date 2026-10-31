package collab

// W04-01 模型层机械测试（fake 时钟 + fake 执行器代理，任务书 W04-N01–N08 属模型
// 层部分 + 自拟边界）。执行器在本包不可达（无执行依赖）；「执行器调用」以
// 「派发意图产生数」为代理断言——意图是执行侧唯一入口（outbox），消息/游标/
// 读接口从不产生意图即 N01 的零调用。
//
// 各用例与 RFC §七映射表对应；断言主体与展示标签分开（Subject 承担权限，
// Leader.Principal 仅归属展示）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock 可推进时钟（UTC 基准）。
type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{cur: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	c.mu.Unlock()
}

// newTestStore 开临时目录存储（fake 时钟注入；leaseTTL 毫秒级加速租约用例）。
func newTestStore(t *testing.T) (*Store, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	clk := newFakeClock()
	st, err := Open(dir, Options{Now: clk.Now, LeaseTTL: 50 * time.Millisecond, Log: testLogger{t}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, clk, dir
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) {
	l.t.Logf("collab: "+format, args...)
}

var (
	owner     = Subject{Name: "owner", Class: ClassOwner}
	steward   = Subject{Name: "steward-a", Class: ClassOrchestrate}
	stewardB  = Subject{Name: "steward-b", Class: ClassOrchestrate}
	systemSub = Subject{Name: "system", Class: ClassSystem}
)

func mustSpace(t *testing.T, st *Store, id string) Space {
	t.Helper()
	sp, ok := st.Space(id)
	if !ok {
		t.Fatalf("空间 %s 不存在", id)
	}
	return sp
}

func mustControl(t *testing.T, st *Store, spaceID string, req ControlRequest, subj Subject) ControlResult {
	t.Helper()
	res, dup, err := st.Control(spaceID, req, subj)
	if err != nil {
		t.Fatalf("Control %s: %v", req.Action, err)
	}
	if dup {
		t.Fatalf("Control %s: 意外重放命中", req.Action)
	}
	if !res.Applied {
		t.Fatalf("Control %s: 未实际变更", req.Action)
	}
	return res
}

// mustRunning 建成员齐备并推进到 running 的 manual 空间（返回当前版本）。
func mustRunning(t *testing.T, st *Store, extra ...func(*CreateRequest)) (string, int64) {
	t.Helper()
	req := CreateRequest{
		Goal: "目标", Mode: ModeManual,
		Members: []Member{{Name: "alice", Role: "writer", SessionID: "sess-alice"}},
	}
	for _, f := range extra {
		f(&req)
	}
	sp, _, err := st.CreateSpace(req, owner)
	if err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, sp.ID, ControlRequest{RequestID: "rdy-" + sp.ID, Action: ActReady, ExpectedVersion: 1}, owner)
	mustControl(t, st, sp.ID, ControlRequest{RequestID: "start-" + sp.ID, Action: ActStart, ExpectedVersion: 2}, owner)
	return sp.ID, 3
}

// ---------- 状态机（自拟边界） ----------

func TestStateMachineTransitionTable(t *testing.T) {
	legal := map[Status][]Status{
		StatusPendingConfig: {StatusReady, StatusAbandoned},
		StatusPendingLeader: {StatusPendingConfig, StatusAbandoned},
		StatusReady:         {StatusRunning, StatusCompleted, StatusAbandoned},
		StatusRunning:       {StatusPaused, StatusBlocked, StatusNeedsReview, StatusCompleted, StatusAbandoned},
		StatusPaused:        {StatusRunning, StatusBlocked, StatusNeedsReview, StatusCompleted, StatusAbandoned},
		StatusBlocked:       {StatusRunning, StatusPaused, StatusCompleted, StatusAbandoned},
		StatusNeedsReview:   {StatusRunning, StatusPaused, StatusCompleted, StatusAbandoned},
		StatusCompleted:     {},
		StatusAbandoned:     {},
	}
	all := []Status{StatusPendingConfig, StatusPendingLeader, StatusReady, StatusRunning,
		StatusPaused, StatusBlocked, StatusNeedsReview, StatusCompleted, StatusAbandoned}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, s := range legal[from] {
				if s == to {
					want = true
					break
				}
			}
			if got := CanTransition(from, to); got != want {
				t.Fatalf("CanTransition(%s,%s)=%v want %v", from, to, got, want)
			}
		}
	}
	// 表外路径经 Control 拒绝：pending_config 直接 pause。
	st, _, _ := newTestStore(t)
	sp, _, err := st.CreateSpace(CreateRequest{RequestID: "c1", Goal: "g", Mode: ModeManual}, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.Control(sp.ID, ControlRequest{RequestID: "p1", Action: ActPause, ExpectedVersion: 1}, owner)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("pending_config 直接 pause 应非法转换, got %v", err)
	}
	// 终态不可重入：abandoned 后再 finalize/archive 之外的一切转换拒绝。
	mustControl(t, st, sp.ID, ControlRequest{RequestID: "f1", Action: ActFinalize, ExpectedVersion: 1, FinalOutcome: "abandoned", Reason: "不再需要"}, owner)
	_, _, err = st.Control(sp.ID, ControlRequest{RequestID: "s1", Action: ActStart, ExpectedVersion: 2}, owner)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("终态 start 应拒绝, got %v", err)
	}
}

// ---------- 创建与初始态（含伪造授权字段拒绝） ----------

func TestCreateSpaceInitialStatesAndAuthz(t *testing.T) {
	st, _, _ := newTestStore(t)

	man, _, err := st.CreateSpace(CreateRequest{RequestID: "m1", Goal: "g", Mode: ModeManual,
		Members: []Member{{Name: "a", SessionID: "s-a"}}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if man.Status != StatusPendingConfig || man.Leader.Kind != LeaderManual || man.Leader.Term != 1 || man.Leader.Principal != "owner" {
		t.Fatalf("manual 初始态不符: %+v", man)
	}
	built, _, err := st.CreateSpace(CreateRequest{RequestID: "b1", Goal: "g", Mode: ModeBuiltin}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if built.Leader.Kind != LeaderBuiltin || built.Leader.Principal != "system" || !built.Leader.LeaseUntil.IsZero() {
		t.Fatalf("builtin 初始负责人不符: %+v", built.Leader)
	}
	if built.Leader.Valid(time.Now()) {
		t.Fatalf("builtin 未续约前负责人不应有效")
	}
	ext, _, err := st.CreateSpace(CreateRequest{RequestID: "e1", Goal: "g", Mode: ModeExternal}, steward)
	if err != nil {
		t.Fatal(err)
	}
	if ext.Status != StatusPendingLeader || ext.Leader.Term != 0 {
		t.Fatalf("external 初始态不符: %+v", ext)
	}
	// 编排主体建 manual 拒绝；建 external 携完成授权字段拒绝（agent 不能自授）。
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "x1", Goal: "g", Mode: ModeManual}, steward); !errors.Is(err, ErrForbidden) {
		t.Fatalf("orchestrate 建 manual 应拒, got %v", err)
	}
	_, _, err = st.CreateSpace(CreateRequest{RequestID: "x2", Goal: "g", Mode: ModeExternal,
		Policy: &Policy{FinishAuth: FinishAuthVerifiedWriteReview}}, steward)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("orchestrate 自授完成授权应拒, got %v", err)
	}
	// system 不开放创建。
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "x3", Goal: "g", Mode: ModeManual}, systemSub); !errors.Is(err, ErrForbidden) {
		t.Fatalf("system 创建应拒, got %v", err)
	}
	// 同键同文回放返回原空间；同键异文冲突。
	sp2, dup, err := st.CreateSpace(CreateRequest{RequestID: "m1", Goal: "g", Mode: ModeManual,
		Members: []Member{{Name: "a", SessionID: "s-a"}}}, owner)
	if err != nil || !dup || sp2.ID != man.ID {
		t.Fatalf("同键同文应回原空间 dup=true, got dup=%v err=%v", dup, err)
	}
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "m1", Goal: "换个目标", Mode: ModeManual}, owner); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("同键异文应冲突, got %v", err)
	}
}

// ---------- N01：普通消息/补拉零执行 ----------

func TestN01MessagesNeverWake(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st, func(r *CreateRequest) { r.Budget = Budget{MaxMessages: 2} })
	for i := 0; i < 2; i++ {
		if _, dup, err := st.RecordMessage(id, MessageRequest{RequestID: fmt.Sprintf("msg-%d", i), ExpectedVersion: ver + int64(i)}, owner); err != nil || dup {
			t.Fatalf("消息 %d: err=%v dup=%v", i, err, dup)
		}
	}
	// 执行器代理：消息从不产生派发意图。
	if n := len(st.RecoverIntents()); n != 0 {
		t.Fatalf("消息不得触发执行（在途意图=%d）", n)
	}
	if intents := st.Intents(id); len(intents) != 0 {
		t.Fatalf("消息不得产生意图: %+v", intents)
	}
	// message_count 独立预算：第 3 条拒绝。
	if _, _, err := st.RecordMessage(id, MessageRequest{RequestID: "msg-2", ExpectedVersion: 5}, owner); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("message_count 耗尽应拒, got %v", err)
	}
	// 拒绝零副作用：版本不动、计数不动。
	sp := mustSpace(t, st, id)
	if sp.Version != 5 || sp.Used.Messages != 2 {
		t.Fatalf("拒绝后零副作用: version=%d used=%+v", sp.Version, sp.Used)
	}
}

// ---------- N02：指名一次 + 重放不重复扣 ----------

func TestN02DispatchIdempotentReplay(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st, func(r *CreateRequest) { r.Budget = Budget{MaxDispatchRounds: 5} })
	req := DispatchRequest{RequestID: "d1", Member: "alice", TurnSeq: 1, Term: 1, ExpectedVersion: ver}
	it, view, dup, err := st.RecordDispatch(id, req, owner)
	if err != nil || dup {
		t.Fatalf("首次派发: err=%v dup=%v", err, dup)
	}
	// 执行侧未接单（TaskID 空）→ outbox pending 态；term/版本引用在位。
	if it.Status != IntentPending || it.TaskID != "" || it.Term != 1 || it.VersionRef != ver+1 {
		t.Fatalf("意图字段不符: %+v", it)
	}
	if view.Space.Used.DispatchRounds != 1 || view.InFlight != 1 {
		t.Fatalf("预算扣减/在途投影不符: %+v", view)
	}
	// 同 ID 重放：原回执、单意图、不重复扣。
	it2, _, dup2, err := st.RecordDispatch(id, req, owner)
	if err != nil || !dup2 {
		t.Fatalf("重放应 dup=true, err=%v", err)
	}
	if it2.RequestID != it.RequestID || it2.TaskID != it.TaskID {
		t.Fatalf("重放回执不符: %+v vs %+v", it2, it)
	}
	if n := len(st.Intents(id)); n != 1 {
		t.Fatalf("重放不得新增意图: %d", n)
	}
	if used := mustSpace(t, st, id).Used.DispatchRounds; used != 1 {
		t.Fatalf("重放不得重复扣预算: %d", used)
	}
	// 同 ID 异文冲突。
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", TurnSeq: 2, Term: 1, ExpectedVersion: ver}, owner); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("同键异文应冲突, got %v", err)
	}
}

// ---------- N03：租约过期 + 两方 CAS 接管只一成功 ----------

func TestN03LeaseExpiryAndCASClaim(t *testing.T) {
	st, clk, _ := newTestStore(t)
	ext, _, err := st.CreateSpace(CreateRequest{RequestID: "e1", Goal: "g", Mode: ModeExternal}, steward)
	if err != nil {
		t.Fatal(err)
	}
	// 未领取者不能配置成员（N10 前置）。
	_, _, err = st.Control(ext.ID, ControlRequest{RequestID: "cm0", Action: ActConfigureMembers, ExpectedVersion: 1,
		Members: []Member{{Name: "a"}}}, stewardB)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("未领取 configure_members 应拒, got %v", err)
	}
	// 领取：CAS expected_term=0 → 任期 1 + 租约。
	res := mustControl(t, st, ext.ID, ControlRequest{RequestID: "claim-a", Action: ActTakeover, ExpectedVersion: 1,
		ExpectedTerm: 0, TakeoverKind: LeaderExternal}, steward)
	if res.Space.Status != StatusPendingConfig || res.Space.Leader.Term != 1 || res.Space.Leader.Principal != "steward-a" {
		t.Fatalf("领取结果不符: %+v", res.Space.Leader)
	}
	// 驱动者续约；非驱动者（错主体/错任期）拒绝。
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "rn1", Action: ActRenew, ExpectedVersion: 2, Term: 1}, steward); err != nil {
		t.Fatalf("驱动者续约应成功: %v", err)
	}
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "rn2", Action: ActRenew, ExpectedVersion: 3, Term: 1}, stewardB); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非驱动者续约应拒, got %v", err)
	}
	// 边界：恰好到期时刻视为未过期，续约成功。
	clk.Advance(50 * time.Millisecond) // now == LeaseUntil
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "rn3", Action: ActRenew, ExpectedVersion: 3, Term: 1}, steward); err != nil {
		t.Fatalf("租约边界（now==LeaseUntil）应未过期可续: %v", err)
	}
	// 过期：renew 不能复活；编排主体接管拒绝（过期接管由人确认，不自动抢回）。
	clk.Advance(51 * time.Millisecond)
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "rn4", Action: ActRenew, ExpectedVersion: 4, Term: 1}, steward); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("过期续约应拒, got %v", err)
	}
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "claim-b", Action: ActTakeover, ExpectedVersion: 4,
		ExpectedTerm: 1, TakeoverKind: LeaderExternal}, stewardB); !errors.Is(err, ErrForbidden) {
		t.Fatalf("编排主体过期接管应拒（等主人确认）, got %v", err)
	}
	// 两方 CAS 接管只一成功（并发；owner 双方构造——编排主体无过期接管资格）。
	const racers = 2
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, conflict := 0, 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := st.Control(ext.ID, ControlRequest{
				RequestID: fmt.Sprintf("takeover-race-%d", i), Action: ActTakeover, ExpectedVersion: 4,
				ExpectedTerm: 1, TakeoverKind: LeaderManual,
			}, owner)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrTermConflict):
				conflict++
			default:
				t.Errorf("接管竞速意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if okCount != 1 || conflict != racers-1 {
		t.Fatalf("两方 CAS 接管应恰一成功: ok=%d conflict=%d", okCount, conflict)
	}
	sp := mustSpace(t, st, ext.ID)
	if sp.Leader.Term != 2 || sp.Leader.Kind != LeaderManual {
		t.Fatalf("接管后任期应为 2/manual: %+v", sp.Leader)
	}
	// 旧任期驱动者派发拒绝（N07 一部分：旧 term 写入）。
	_, _, err = st.Control(ext.ID, ControlRequest{RequestID: "rn-old", Action: ActRenew, ExpectedVersion: 5, Term: 1}, steward)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("旧任期续约应拒, got %v", err)
	}
}

// termSeq 永不复用（边界）：接管序列 term 单调递增。
func TestTermNeverReused(t *testing.T) {
	st, _, _ := newTestStore(t)
	ext, _, _ := st.CreateSpace(CreateRequest{RequestID: "e1", Goal: "g", Mode: ModeExternal}, steward)
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "c1", Action: ActTakeover, ExpectedVersion: 1, ExpectedTerm: 0, TakeoverKind: LeaderExternal}, steward)
	// 主人切 manual（term 2）→ 再开放 external（term 清 0）→ 再领取（term 3，不复用 1）。
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "c2", Action: ActTakeover, ExpectedVersion: 2, ExpectedTerm: 1, TakeoverKind: LeaderManual}, owner)
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "c3", Action: ActTakeover, ExpectedVersion: 3, ExpectedTerm: 2, TakeoverKind: LeaderExternal}, owner)
	sp := mustSpace(t, st, ext.ID)
	if sp.Status != StatusPendingLeader || sp.Leader.Term != 0 {
		t.Fatalf("开放领取后应待领取无任期: %+v", sp)
	}
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "c4", Action: ActTakeover, ExpectedVersion: 4, ExpectedTerm: 0, TakeoverKind: LeaderExternal}, steward)
	if got := mustSpace(t, st, ext.ID).Leader.Term; got != 3 {
		t.Fatalf("任期应服务端分配不复用（want 3）: %d", got)
	}
}

// ---------- N04：暂停结果落账不再派 ----------

func TestN04PauseConvergesNoFurtherDispatch(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner); err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, id, ControlRequest{RequestID: "p1", Action: ActPause, ExpectedVersion: ver + 1, Note: "人工介入"}, owner)
	// 在途结果落账（收敛不触发任何下一轮）。
	it, err := st.ConvergeIntent(id, "d1", "converged", "", "结果已核", systemSub)
	if err != nil || it.Status != IntentConverged {
		t.Fatalf("收敛: %v %+v", err, it)
	}
	if n := st.InFlightCount(id); n != 0 {
		t.Fatalf("收敛后在途应清零: %d", n)
	}
	// 暂停态派发拒绝（不再派）。
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d2", Member: "alice", Term: 1, ExpectedVersion: ver + 2}, owner); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("暂停态派发应拒, got %v", err)
	}
	// 收敛幂等：同 outcome 重放原样；异 outcome 冲突。
	if _, err := st.ConvergeIntent(id, "d1", "converged", "", "", systemSub); err != nil {
		t.Fatalf("同 outcome 收敛应幂等: %v", err)
	}
	if _, err := st.ConvergeIntent(id, "d1", "cancelled", "", "", systemSub); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("异 outcome 改写应拒, got %v", err)
	}
	// 恢复后可再派；收敛结果持久（重开可查）。
	mustControl(t, st, id, ControlRequest{RequestID: "r1", Action: ActResume, ExpectedVersion: ver + 2}, owner)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d3", Member: "alice", Term: 1, ExpectedVersion: ver + 3}, owner); err != nil {
		t.Fatalf("恢复后派发应成功: %v", err)
	}
}

// ---------- N05：持久化失败零虚假接受 / 崩溃恢复不重发 ----------

func TestN05PersistFailureZeroFalseAccept(t *testing.T) {
	st, _, dir := newTestStore(t)
	id, ver := mustRunning(t, st)
	st.failPersist = func() error { return errors.New("注入：盘满") }
	_, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner)
	if err == nil {
		t.Fatal("落盘失败必须报错")
	}
	// 零虚假接受：无意图、无扣减、无版本推进、无回执。
	if n := st.InFlightCount(id); n != 0 {
		t.Fatalf("失败不得留意图: %d", n)
	}
	sp := mustSpace(t, st, id)
	if sp.Used.DispatchRounds != 0 || sp.Version != ver {
		t.Fatalf("失败零副作用: %+v", sp)
	}
	st.failPersist = nil
	// 解除后同 ID 重试恰一次（重放窗口内第一次成功才登记回执）。
	_, _, dup, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner)
	if err != nil || dup {
		t.Fatalf("重试应成功且非重放: err=%v dup=%v", err, dup)
	}
	if n := len(st.Intents(id)); n != 1 {
		t.Fatalf("重试后恰一意图: %d", n)
	}
	// 崩溃恢复：重开存储后同 ID 重放命中原回执，不重发（执行器代理=意图仍 1）。
	st2, err := Open(dir, Options{Now: st.now, LeaseTTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	_, _, dup2, err := st2.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner)
	if err != nil || !dup2 {
		t.Fatalf("重开重放应命中回执: dup=%v err=%v", dup2, err)
	}
	if n := len(st2.Intents(id)); n != 1 {
		t.Fatalf("崩溃恢复不得重发（意图=%d）", n)
	}
	if n := len(st2.RecoverIntents()); n != 1 {
		t.Fatalf("在途意图应保留供对账: %d", n)
	}
}

// ---------- N06：在途护栏（成员改/结束/归档拒绝） ----------

func TestN06InFlightGuards(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner); err != nil {
		t.Fatal(err)
	}
	v := ver + 1
	// 暂停后改成员：移除在途成员拒绝。
	mustControl(t, st, id, ControlRequest{RequestID: "p1", Action: ActPause, ExpectedVersion: v}, owner)
	v++
	_, _, err := st.Control(id, ControlRequest{RequestID: "cm1", Action: ActConfigureMembers, ExpectedVersion: v,
		Members: []Member{{Name: "bob", SessionID: "s-bob"}}}, owner)
	if !errors.Is(err, ErrMemberInFlight) {
		t.Fatalf("移除在途成员应拒, got %v", err)
	}
	// 改在途成员会话同样拒绝。
	_, _, err = st.Control(id, ControlRequest{RequestID: "cm2", Action: ActConfigureMembers, ExpectedVersion: v,
		Members: []Member{{Name: "alice", SessionID: "s-alice-2"}}}, owner)
	if !errors.Is(err, ErrMemberInFlight) {
		t.Fatalf("换绑在途成员应拒, got %v", err)
	}
	// 有在途：finish/finalize/archive 拒绝。
	if _, _, err := st.Control(id, ControlRequest{RequestID: "fin1", Action: ActFinish, ExpectedVersion: v, Term: 1}, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("未开完成授权 finish 应拒, got %v", err)
	}
	if _, _, err := st.Control(id, ControlRequest{RequestID: "fz1", Action: ActFinalize, ExpectedVersion: v, FinalOutcome: "abandoned", Reason: "x"}, owner); !errors.Is(err, ErrExecutionInFlight) {
		t.Fatalf("在途放弃应拒, got %v", err)
	}
	if _, _, err := st.Control(id, ControlRequest{RequestID: "ar1", Action: ActArchive, ExpectedVersion: v}, owner); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("非终态归档应拒, got %v", err)
	}
	// 收束后放行：收敛在途 → 改成员（保留 alice）→ 放弃（原因必填）→ 归档幂等。
	if _, err := st.ConvergeIntent(id, "d1", "converged", "", "结果核对完成", systemSub); err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, id, ControlRequest{RequestID: "cm3", Action: ActConfigureMembers, ExpectedVersion: v,
		Members: []Member{{Name: "alice", SessionID: "sess-alice"}, {Name: "bob", SessionID: "s-bob"}}}, owner)
	v++
	if _, _, err := st.Control(id, ControlRequest{RequestID: "fz2", Action: ActFinalize, ExpectedVersion: v, FinalOutcome: "abandoned"}, owner); !errors.Is(err, ErrInvalid) {
		t.Fatalf("放弃缺原因应拒, got %v", err)
	}
	mustControl(t, st, id, ControlRequest{RequestID: "fz3", Action: ActFinalize, ExpectedVersion: v, FinalOutcome: "abandoned", Reason: "目标取消"}, owner)
	v++
	arch := mustControl(t, st, id, ControlRequest{RequestID: "ar2", Action: ActArchive, ExpectedVersion: v}, owner)
	v++
	// 归档幂等：重复归档不重复变更。
	arch2, _, err := st.Control(id, ControlRequest{RequestID: "ar3", Action: ActArchive, ExpectedVersion: v}, owner)
	if err != nil || arch2.Applied {
		t.Fatalf("重复归档应幂等命中（applied=false）, got %+v err=%v", arch2, err)
	}
	if !arch.Space.Archived || !mustSpace(t, st, id).Archived {
		t.Fatalf("归档标记未落: %+v", arch.Space)
	}
}

// ---------- N07：越 scope/member/旧 term/伪造主体拒绝矩阵 ----------

func TestN07AuthorizationMatrix(t *testing.T) {
	st, _, _ := newTestStore(t)
	// 编排主体领取非 external / 非待领取 → 拒绝。
	man, _, _ := st.CreateSpace(CreateRequest{RequestID: "m1", Goal: "g", Mode: ModeManual}, owner)
	if _, _, err := st.Control(man.ID, ControlRequest{RequestID: "c0", Action: ActTakeover, ExpectedVersion: 1, ExpectedTerm: 1, TakeoverKind: LeaderExternal}, steward); !errors.Is(err, ErrForbidden) {
		t.Fatalf("编排主体接管 manual 空间应拒, got %v", err)
	}
	// 领取约束：claim_principals 名单外主体拒绝。
	ext, _, _ := st.CreateSpace(CreateRequest{RequestID: "e1", Goal: "g", Mode: ModeExternal,
		Policy: &Policy{ClaimPrincipals: []string{"steward-a"}}}, owner)
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "c1", Action: ActTakeover, ExpectedVersion: 1, ExpectedTerm: 0, TakeoverKind: LeaderExternal}, stewardB); !errors.Is(err, ErrForbidden) {
		t.Fatalf("名单外领取应拒, got %v", err)
	}
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "c2", Action: ActTakeover, ExpectedVersion: 1, ExpectedTerm: 0, TakeoverKind: LeaderExternal}, steward)
	// 领取后的驱动者编排主体：越权动作全拒（主人专属面）。
	for _, tc := range []struct {
		id   string
		req  ControlRequest
		code error
	}{
		{"set_budget", ControlRequest{RequestID: "z1", Action: ActSetBudget, ExpectedVersion: 2, Budget: &Budget{MaxMessages: 9}}, ErrForbidden},
		{"policy", ControlRequest{RequestID: "z2", Action: ActPolicy, ExpectedVersion: 2, Policy: &Policy{FinishAuth: FinishAuthVerifiedWriteReview}}, ErrForbidden},
		{"finalize", ControlRequest{RequestID: "z3", Action: ActFinalize, ExpectedVersion: 2, FinalOutcome: "abandoned", Reason: "x"}, ErrForbidden},
		{"archive", ControlRequest{RequestID: "z4", Action: ActArchive, ExpectedVersion: 2}, ErrForbidden},
	} {
		if _, _, err := st.Control(ext.ID, tc.req, steward); !errors.Is(err, tc.code) {
			t.Fatalf("%s 编排主体应拒, got %v", tc.id, err)
		}
	}
	// 驱动者推进 external 空间到运行态（配置→就绪→启动），再验证 pause/resume 权限：
	// 驱动者 pause 允许；owner 在 external 空间无任期 resume 拒绝（须先接管）。
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "cm0", Action: ActConfigureMembers, ExpectedVersion: 2, Term: 1,
		Members: []Member{{Name: "a", SessionID: "s-a"}}}, steward)
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "rd0", Action: ActReady, ExpectedVersion: 3, Term: 1}, steward)
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "st0", Action: ActStart, ExpectedVersion: 4, Term: 1}, steward)
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "p1", Action: ActPause, ExpectedVersion: 5, Term: 1}, steward)
	if _, _, err := st.Control(ext.ID, ControlRequest{RequestID: "r1", Action: ActResume, ExpectedVersion: 6}, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner 未接管 resume external 空间应拒, got %v", err)
	}
	mustControl(t, st, ext.ID, ControlRequest{RequestID: "r2", Action: ActResume, ExpectedVersion: 6, Term: 1}, steward)
	// 越范围成员派发拒绝；成员未绑线程拒绝。
	id, ver := mustRunning(t, st)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d0", Member: "mallory", Term: 1, ExpectedVersion: ver}, owner); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("越范围成员应拒, got %v", err)
	}
	// 成员未绑线程拒绝：暂停态去掉会话绑定，恢复后派发拒绝。
	mustControl(t, st, id, ControlRequest{RequestID: "p9", Action: ActPause, ExpectedVersion: ver}, owner)
	_, _, err := st.Control(id, ControlRequest{RequestID: "cm9", Action: ActConfigureMembers, ExpectedVersion: ver + 1,
		Members: []Member{{Name: "alice"}}}, owner) // 去掉会话绑定
	if err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, id, ControlRequest{RequestID: "r9", Action: ActResume, ExpectedVersion: ver + 2}, owner)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver + 3}, owner); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("成员未绑线程派发应拒, got %v", err)
	}
	// 旧 term 派发：接管换任期后旧驱动者 term 拒绝。
	mustControl(t, st, id, ControlRequest{RequestID: "tk1", Action: ActTakeover, ExpectedVersion: ver + 3, ExpectedTerm: 1, TakeoverKind: LeaderBuiltin}, owner)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d2", Member: "alice", Term: 1, ExpectedVersion: ver + 4}, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("旧 term 派发应拒, got %v", err)
	}
}

// ---------- N08：预算 + 决策窗口变化仍事务复核 ----------

func TestN08BudgetRaceTransactionRecheck(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st, func(r *CreateRequest) { r.Budget = Budget{MaxDispatchRounds: 1} })
	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, budgetRejected := 0, 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 竞速者带读档版本进场；版本被并发赢家推进时按事务复核语义刷新重读
			// 一次（对应真实调用方「409 → 刷新重试」路径），最终必落在预算复核。
			exp := ver
			var err error
			for retry := 0; retry < 3; retry++ {
				_, _, _, err = st.RecordDispatch(id, DispatchRequest{
					RequestID: fmt.Sprintf("race-d-%d", i), Member: "alice", Term: 1, ExpectedVersion: exp,
				}, owner)
				if err == nil || !errors.Is(err, ErrVersionConflict) {
					break
				}
				if v, ok := st.SpaceView(id); ok {
					exp = v.Version
				}
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrBudgetExhausted):
				budgetRejected++
			default:
				t.Errorf("竞速意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if okCount != 1 || budgetRejected != racers-1 {
		t.Fatalf("预算 1 次竞速应恰一成功: ok=%d rejected=%d", okCount, budgetRejected)
	}
	if used := mustSpace(t, st, id).Used.DispatchRounds; used != 1 {
		t.Fatalf("扣减恰一次: %d", used)
	}
	if n := len(st.Intents(id)); n != 1 {
		t.Fatalf("竞速不得多派意图: %d", n)
	}
	// 决策窗口变化：调用方基于旧读档决策，apply 期版本复核拦截。
	view, _ := st.SpaceView(id)
	st.RecordMessage(id, MessageRequest{RequestID: "m-interfere", ExpectedVersion: view.Version}, owner)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d-stale", Member: "alice", Term: 1, ExpectedVersion: view.Version}, owner); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("决策窗口变化应事务复核拦截, got %v", err)
	}
}

// ---------- 策略接口（W05-01 输入）：完成判据闭环 ----------

type fakeHooks struct {
	verdict  CompletionVerdict
	candErr  error
	phase    PhaseDecision
	verdicts int
}

func (h *fakeHooks) EvaluateCompletion(SpaceView) CompletionVerdict {
	h.verdicts++
	return h.verdict
}
func (h *fakeHooks) ValidateCandidate(_ SpaceView, _ CandidateRecord) error { return h.candErr }
func (h *fakeHooks) DecidePhaseAdvance(SpaceView) PhaseDecision             { return h.phase }

func TestFinishAndFinalizeDonePolicyGates(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	hooks := &fakeHooks{verdict: CompletionVerdict{Satisfied: false, Reason: "必须修未清零"}}
	st, err := Open(dir, Options{Now: clk.Now, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, ver := mustRunning(t, st)
	// 未开完成授权（默认 human_only）：finish 拒绝，等待人工定版。
	if _, _, err := st.Control(id, ControlRequest{RequestID: "f0", Action: ActFinish, ExpectedVersion: ver, Term: 1}, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("human_only finish 应拒, got %v", err)
	}
	// 主人开启完成授权（策略变更须暂停/待配置态且无在途）：先暂停，改完恢复。
	mustControl(t, st, id, ControlRequest{RequestID: "pp", Action: ActPause, ExpectedVersion: ver}, owner)
	mustControl(t, st, id, ControlRequest{RequestID: "pol", Action: ActPolicy, ExpectedVersion: ver + 1, Policy: &Policy{FinishAuth: FinishAuthVerifiedWriteReview}}, owner)
	mustControl(t, st, id, ControlRequest{RequestID: "rr", Action: ActResume, ExpectedVersion: ver + 2}, owner)
	if _, _, err := st.Control(id, ControlRequest{RequestID: "f1", Action: ActFinish, ExpectedVersion: ver + 3, Term: 1}, owner); !errors.Is(err, ErrCompletionNotSatisfied) {
		t.Fatalf("判据未满足 finish 应拒, got %v", err)
	}
	hooks.verdict = CompletionVerdict{Satisfied: true, Reason: "必须修清零、测试全绿"}
	res := mustControl(t, st, id, ControlRequest{RequestID: "f2", Action: ActFinish, ExpectedVersion: ver + 3, Term: 1}, owner)
	if res.Space.Status != StatusCompleted || res.Space.FinalOutcome != "done" {
		t.Fatalf("finish 完成不符: %+v", res.Space)
	}
	if hooks.verdicts < 2 {
		t.Fatalf("判据回调次数不符: %d", hooks.verdicts)
	}
	// 无 hooks：finalize done 拒绝（完成判据策略未装配），abandoned 可用。
	st3, _, _ := newTestStore(t)
	id3, ver3 := mustRunning(t, st3)
	if _, _, err := st3.Control(id3, ControlRequest{RequestID: "fd", Action: ActFinalize, ExpectedVersion: ver3, FinalOutcome: "done"}, owner); !errors.Is(err, ErrFinishPolicyUnavailable) {
		t.Fatalf("无判据 finalize done 应拒, got %v", err)
	}
	mustControl(t, st3, id3, ControlRequest{RequestID: "fa", Action: ActFinalize, ExpectedVersion: ver3, FinalOutcome: "abandoned", Reason: "放弃"}, owner)
}

// ---------- 会话唯一绑定（一线程一空间） ----------

func TestSessionUniqueBinding(t *testing.T) {
	st, _, _ := newTestStore(t)
	a, _, _ := st.CreateSpace(CreateRequest{RequestID: "a", Goal: "g", Mode: ModeManual,
		Members: []Member{{Name: "alice", SessionID: "sess-1"}}}, owner)
	if sid, ok := st.SessionOwnerSpace("sess-1"); !ok || sid != a.ID {
		t.Fatalf("归属查询不符: %s %v", sid, ok)
	}
	// 另一空间绑同会话拒绝（创建与 configure 两路）。
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "b", Goal: "g", Mode: ModeManual,
		Members: []Member{{Name: "bob", SessionID: "sess-1"}}}, owner); !errors.Is(err, ErrSessionBoundElsewhere) {
		t.Fatalf("跨空间会话绑定应拒, got %v", err)
	}
	b, _, _ := st.CreateSpace(CreateRequest{RequestID: "b2", Goal: "g", Mode: ModeManual}, owner)
	if _, _, err := st.Control(b.ID, ControlRequest{RequestID: "cb1", Action: ActConfigureMembers, ExpectedVersion: 1,
		Members: []Member{{Name: "bob", SessionID: "sess-1"}}}, owner); !errors.Is(err, ErrSessionBoundElsewhere) {
		t.Fatalf("configure 跨空间绑会话应拒, got %v", err)
	}
	// 原空间解绑后可他处绑定。
	mustControl(t, st, a.ID, ControlRequest{RequestID: "ca1", Action: ActConfigureMembers, ExpectedVersion: 1,
		Members: []Member{{Name: "alice"}}}, owner)
	mustControl(t, st, b.ID, ControlRequest{RequestID: "cb2", Action: ActConfigureMembers, ExpectedVersion: 1,
		Members: []Member{{Name: "bob", SessionID: "sess-1"}}}, owner)
	if sid, _ := st.SessionOwnerSpace("sess-1"); sid != b.ID {
		t.Fatalf("解绑后归属应迁移: %s", sid)
	}
}

// ---------- 墙钟预算（三分离第三维） ----------

func TestWallClockBudget(t *testing.T) {
	st, clk, _ := newTestStore(t)
	id, ver := mustRunning(t, st, func(r *CreateRequest) { r.Budget = Budget{MaxWallSeconds: 2} })
	clk.Advance(3 * time.Second)
	if _, _, _, err := st.RecordDispatch(id, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: ver}, owner); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("墙钟耗尽派发应拒, got %v", err)
	}
	// 暂停结算：running 1s + paused + resume 0.5s（截断 0）→ 用量 1 < 2 放行；
	// 再走 1s → 累计 2 达上限拒。
	st2, clk2, _ := newTestStore(t)
	id2, ver2 := mustRunning(t, st2, func(r *CreateRequest) { r.Budget = Budget{MaxWallSeconds: 2} })
	clk2.Advance(1 * time.Second)
	mustControl(t, st2, id2, ControlRequest{RequestID: "p", Action: ActPause, ExpectedVersion: ver2}, owner)
	sp := mustSpace(t, st2, id2)
	if sp.Used.WallSeconds != 1 || !sp.Used.RunningSince.IsZero() {
		t.Fatalf("暂停应结算墙钟: %+v", sp.Used)
	}
	mustControl(t, st2, id2, ControlRequest{RequestID: "r", Action: ActResume, ExpectedVersion: ver2 + 1}, owner)
	clk2.Advance(500 * time.Millisecond)
	if _, _, _, err := st2.RecordDispatch(id2, DispatchRequest{RequestID: "d", Member: "alice", Term: 1, ExpectedVersion: ver2 + 2}, owner); err != nil {
		t.Fatalf("墙钟 1s（未达上限 2s）派发应放行: %v", err)
	}
	clk2.Advance(1 * time.Second)
	if _, _, _, err := st2.RecordDispatch(id2, DispatchRequest{RequestID: "d2", Member: "alice", Term: 1, ExpectedVersion: ver2 + 3}, owner); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("墙钟累计 2s 达上限应拒, got %v", err)
	}
}

// ---------- 内置流程（builtin：system 主体续约后驱动） ----------

func TestBuiltinLeaderLeaseFlow(t *testing.T) {
	st, clk, _ := newTestStore(t)
	b, _, _ := st.CreateSpace(CreateRequest{RequestID: "b1", Goal: "g", Mode: ModeBuiltin,
		Members: []Member{{Name: "alice", SessionID: "sess-alice"}}}, owner)
	// 未续约：owner 非驱动者且 mode 非 manual → start 拒绝。
	if _, _, err := st.Control(b.ID, ControlRequest{RequestID: "s0", Action: ActStart, ExpectedVersion: 1}, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("builtin 未续约 start 应拒, got %v", err)
	}
	mustControl(t, st, b.ID, ControlRequest{RequestID: "r1", Action: ActReady, ExpectedVersion: 1}, owner)
	// system 续约成有效驱动者 → 启动、派发。
	mustControl(t, st, b.ID, ControlRequest{RequestID: "rn1", Action: ActRenew, ExpectedVersion: 2, Term: 1}, systemSub)
	mustControl(t, st, b.ID, ControlRequest{RequestID: "s1", Action: ActStart, ExpectedVersion: 3, Term: 1}, systemSub)
	if _, _, _, err := st.RecordDispatch(b.ID, DispatchRequest{RequestID: "d1", Member: "alice", Term: 1, ExpectedVersion: 4}, systemSub); err != nil {
		t.Fatalf("system 驱动派发应成功: %v", err)
	}
	// 租约过期后 system 续约拒绝（复活禁止），须主人接管。
	clk.Advance(51 * time.Millisecond)
	if _, _, err := st.Control(b.ID, ControlRequest{RequestID: "rn2", Action: ActRenew, ExpectedVersion: 5, Term: 1}, systemSub); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("过期续约应拒, got %v", err)
	}
}

// ---------- 回执滚动淘汰与确定性空间 ID ----------

func TestReceiptCapAndDerivedSpaceID(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	st, err := Open(dir, Options{Now: clk.Now, MaxReceipts: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sp1, _, err := st.CreateSpace(CreateRequest{RequestID: "op-1", Goal: "g1", Mode: ModeManual}, owner)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 5; i++ {
		if _, _, err := st.CreateSpace(CreateRequest{RequestID: fmt.Sprintf("op-%d", i), Goal: fmt.Sprintf("g%d", i), Mode: ModeManual}, owner); err != nil {
			t.Fatal(err)
		}
	}
	// op-1 回执已被淘汰：重放仍收敛同一空间（确定性派生 ID），不产生第二个空间。
	again, dup, err := st.CreateSpace(CreateRequest{RequestID: "op-1", Goal: "g1", Mode: ModeManual}, owner)
	if err != nil || dup {
		t.Fatalf("淘汰后重放: dup=%v err=%v", dup, err)
	}
	if again.ID != sp1.ID {
		t.Fatalf("确定性空间 ID 收敛失败: %s vs %s", again.ID, sp1.ID)
	}
	if n := len(st.Spaces()); n != 5 {
		t.Fatalf("重放不得新建空间: %d", n)
	}
}

// ---------- 快照损坏拒绝带病启动 ----------

func TestCorruptSnapshotRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("损坏快照应拒绝启动")
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"spaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("未知版本应拒绝启动")
	}
	// 正常路径：快照权限 0600（原子写纪律）。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "c", Goal: "g", Mode: ModeManual}, owner); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("快照权限应 0600, got %o", perm)
	}
}
