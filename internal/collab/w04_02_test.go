package collab

// w04_02_test.go W04-02 模型层增量机械测试：消息账本（N01 零执行——本包无执行
// 依赖，落账即全部事实）、意图接单登记、租约过期自动暂停（含未投递意图取消收束）、
// 系统暂停。fake 时钟 + 既有夹具同源（collab_test.go）。

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// claimExternal 领取 external 待领取空间的便捷链（创建→takeover 领取→返回空间）。
func claimExternal(t *testing.T, st *Store, subj Subject, members []Member) Space {
	t.Helper()
	sp, _, err := st.CreateSpace(CreateRequest{Goal: "g", Mode: ModeExternal, Members: members}, subj)
	if err != nil {
		t.Fatalf("创建 external: %v", err)
	}
	term := uint64(0)
	res, _, err := st.Control(sp.ID, ControlRequest{
		RequestID: "claim-" + sp.ID, Action: ActTakeover, ExpectedVersion: sp.Version,
		ExpectedTerm: term, LeaseSeconds: 1,
	}, subj)
	if err != nil {
		t.Fatalf("领取: %v", err)
	}
	return res.Space.Space
}

// runExternalRunning 领取→配置成员→ready→start 全链（返回运行态空间与当前任期）。
func runExternalRunning(t *testing.T, st *Store, subj Subject, members []Member) (Space, uint64) {
	t.Helper()
	sp := claimExternal(t, st, subj, members)
	term := sp.Leader.Term
	v := sp.Version
	ctrl := func(action Action, ver int64, extra func(*ControlRequest)) Space {
		req := ControlRequest{RequestID: string(action) + fmt.Sprint(ver), Action: action, ExpectedVersion: ver, Term: term}
		if extra != nil {
			extra(&req)
		}
		res, _, err := st.Control(sp.ID, req, subj)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return res.Space.Space
	}
	if len(members) > 0 {
		sp = ctrl(ActConfigureMembers, v, func(r *ControlRequest) { r.Members = members })
		v = sp.Version
	}
	sp = ctrl(ActReady, v, nil)
	v = sp.Version
	sp = ctrl(ActStart, v, nil)
	return sp, term
}

// dispatchOK 受理一笔派发（断言成功并返回意图与现版本）。
func dispatchOK(t *testing.T, st *Store, spaceID string, req DispatchRequest, subj Subject) (DispatchIntent, int64) {
	t.Helper()
	it, view, dup, err := st.RecordDispatch(spaceID, req, subj)
	if err != nil || dup {
		t.Fatalf("派发应受理: err=%v dup=%v", err, dup)
	}
	return it, view.Version
}

func TestW0402AttachDispatchTask(t *testing.T) {
	st, _, _ := newTestStore(t)
	members := []Member{{Name: "echo", SessionID: "sess-a"}}
	sp, term := runExternalRunning(t, st, steward, members)
	it, _ := dispatchOK(t, st, sp.ID, DispatchRequest{
		RequestID: "d1", Member: "echo", TurnSeq: 5, Term: term, ExpectedVersion: sp.Version,
	}, steward)
	if it.Status != IntentPending || it.TaskID != "" {
		t.Fatalf("无 TaskID 受理应为 pending: %+v", it)
	}
	// 接单：pending → accepted + TaskID。
	got, err := st.AttachDispatchTask(sp.ID, "d1", "t-1", systemSub)
	if err != nil || got.Status != IntentAccepted || got.TaskID != "t-1" {
		t.Fatalf("接单应成立: %+v err=%v", got, err)
	}
	// 同 ID 同 taskID 重放：幂等返回原意图。
	got, err = st.AttachDispatchTask(sp.ID, "d1", "t-1", systemSub)
	if err != nil || got.TaskID != "t-1" {
		t.Fatalf("接单重放应幂等: %+v err=%v", got, err)
	}
	// 同 ID 异 taskID：冲突（指纹不符）。
	if _, err := st.AttachDispatchTask(sp.ID, "d1", "t-2", systemSub); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("异 taskID 接单应 request_conflict: %v", err)
	}
	// orchestrate 主体拒绝（执行侧记账仅 owner/system）。
	if _, err := st.AttachDispatchTask(sp.ID, "d1", "t-3", steward); !errors.Is(err, ErrForbidden) {
		t.Fatalf("orchestrate 接单应 forbidden: %v", err)
	}
	// 收敛后：同 taskID 接单命中回执重放（journal 先行，回原结果）；异 taskID 冲突。
	if _, err := st.ConvergeIntent(sp.ID, "d1", string(IntentConverged), "t-1", "", owner); err != nil {
		t.Fatalf("收敛: %v", err)
	}
	if _, err := st.AttachDispatchTask(sp.ID, "d1", "t-1", systemSub); err != nil {
		t.Fatalf("同 taskID 接单应回执重放: %v", err)
	}
	if _, err := st.AttachDispatchTask(sp.ID, "d1", "t-9", systemSub); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("异 taskID 接单应 request_conflict（journal 已占用）: %v", err)
	}
	// IntentByRequest 读面。
	if got, ok := st.IntentByRequest(sp.ID, "d1"); !ok || got.TaskID != "t-1" {
		t.Fatalf("IntentByRequest 应命中: %+v ok=%v", got, ok)
	}
	if _, ok := st.IntentByRequest(sp.ID, "nope"); ok {
		t.Fatal("未知 request_id 不应命中")
	}
}

func TestW0402MessageLedger(t *testing.T) {
	st, _, _ := newTestStore(t)
	sp, _, err := st.CreateSpace(CreateRequest{
		Goal: "g", Mode: ModeManual,
		Members: []Member{{Name: "echo", SessionID: "sess-a"}},
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	// 主人发言（不指名成员）→ seq 1。
	_, dup, err := st.RecordMessage(sp.ID, MessageRequest{
		RequestID: "m1", ExpectedVersion: sp.Version, Text: "主人开场",
	}, owner)
	if err != nil || dup {
		t.Fatalf("主人发言: %v", err)
	}
	// 指名成员发言 → seq 2（归属成员在册；版本随消息 +1）。
	if _, _, err := st.RecordMessage(sp.ID, MessageRequest{
		RequestID: "m2", ExpectedVersion: sp.Version + 1, Text: "管家代转", Member: "echo",
	}, steward); err != nil {
		t.Fatalf("成员归属发言: %v", err)
	}
	// 伪造成员（不在册）拒绝且零副作用。
	vBefore := mustSpace(t, st, sp.ID).Version
	if _, _, err := st.RecordMessage(sp.ID, MessageRequest{
		RequestID: "m3", ExpectedVersion: vBefore, Text: "伪造", Member: "ghost",
	}, steward); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("伪造成员应 member_not_found: %v", err)
	}
	if v := mustSpace(t, st, sp.ID).Version; v != vBefore {
		t.Fatalf("拒绝零副作用被破坏: %d → %d", vBefore, v)
	}
	// 增量读 since。
	msgs, pruned := st.SpaceMessages(sp.ID, 1)
	if len(msgs) != 1 || msgs[0].Seq != 2 || msgs[0].Member != "echo" || pruned != 0 {
		t.Fatalf("since=1 应只读 seq2: %+v pruned=%d", msgs, pruned)
	}
	// 重放：同键同文命中原回执（零重复计数）。
	if _, dup, err := st.RecordMessage(sp.ID, MessageRequest{
		RequestID: "m1", ExpectedVersion: sp.Version, Text: "主人开场",
	}, owner); err != nil || !dup {
		t.Fatalf("消息重放应 dup: %v dup=%v", err, dup)
	}
	if u := mustSpace(t, st, sp.ID).Used.Messages; u != 2 {
		t.Fatalf("重放不得重复计数: %d", u)
	}
}

func TestW0402MessageLedgerPrune(t *testing.T) {
	st, _, _ := newTestStore(t)
	sp, _, err := st.CreateSpace(CreateRequest{Goal: "g", Mode: ModeManual}, owner)
	if err != nil {
		t.Fatal(err)
	}
	old := maxMessagesPerSpace
	maxMessagesPerSpace = 5
	t.Cleanup(func() { maxMessagesPerSpace = old })
	ver := sp.Version
	for i := 0; i < 7; i++ {
		if _, _, err := st.RecordMessage(sp.ID, MessageRequest{
			RequestID: fmt.Sprintf("pm%d", i), ExpectedVersion: ver, Text: fmt.Sprint(i),
		}, owner); err != nil {
			t.Fatalf("消息 %d: %v", i, err)
		}
		ver++
	}
	msgs, pruned := st.SpaceMessages(sp.ID, 0)
	if len(msgs) != 5 || msgs[0].Seq != 3 || pruned != 2 {
		t.Fatalf("滚动窗口应保留最新 5 条（pruned_before=2）: len=%d first=%d pruned=%d", len(msgs), msgs[0].Seq, pruned)
	}
}

func TestW0402PauseExpired(t *testing.T) {
	st, clk, _ := newTestStore(t)
	members := []Member{{Name: "echo", SessionID: "sess-a"}}
	sp, term := runExternalRunning(t, st, steward, members)

	// 一笔已投递（accepted）+ 一笔未投递（pending）。
	it1, ver := dispatchOK(t, st, sp.ID, DispatchRequest{
		RequestID: "d1", Member: "echo", TurnSeq: 2, Term: term, ExpectedVersion: sp.Version, TaskID: "t-1",
	}, steward)
	if it1.Status != IntentAccepted {
		t.Fatalf("携 TaskID 受理应为 accepted: %+v", it1)
	}
	if _, _, _, err := st.RecordDispatch(sp.ID, DispatchRequest{
		RequestID: "d2", Member: "echo", TurnSeq: 3, Term: term, ExpectedVersion: ver,
	}, steward); err != nil {
		t.Fatalf("d2 受理: %v", err)
	}

	// 租约未过期：PauseExpired 零变更。
	if _, n, err := st.PauseExpired(sp.ID, "不应触发"); err != nil || n != 0 {
		t.Fatalf("未过期不应触发: n=%d err=%v", n, err)
	}
	if got := mustSpace(t, st, sp.ID); got.Status != StatusRunning {
		t.Fatalf("未过期不得暂停: %s", got.Status)
	}

	// 推过租约到期点（LeaseSeconds=1s）：暂停 + 未投递意图取消 + 已投递不动。
	clk.Advance(1200 * time.Millisecond)
	updated, n, err := st.PauseExpired(sp.ID, "负责人租约已过期（调度前置检查）")
	if err != nil {
		t.Fatalf("过期暂停: %v", err)
	}
	if updated.Status != StatusPaused || n != 1 || updated.PausedReason == "" {
		t.Fatalf("应暂停并取消 1 条未投递: status=%s n=%d reason=%q", updated.Status, n, updated.PausedReason)
	}
	got2, _ := st.IntentByRequest(sp.ID, "d2")
	if got2.Status != IntentCancelled {
		t.Fatalf("未投递意图应取消收束: %+v", got2)
	}
	got1, _ := st.IntentByRequest(sp.ID, "d1")
	if got1.Status != IntentAccepted {
		t.Fatalf("已投递意图不受暂停影响（在途照收）: %+v", got1)
	}
	// 已投递意图照常收敛（N04：在途结果落账）。
	if _, err := st.ConvergeIntent(sp.ID, "d1", string(IntentConverged), "t-1", "轮终", owner); err != nil {
		t.Fatalf("暂停后在途收敛应成功: %v", err)
	}
	// 暂停后新派发拒绝（只停新增；租约已过期——授权先于状态判定，403 forbidden）。
	if _, _, _, err := st.RecordDispatch(sp.ID, DispatchRequest{
		RequestID: "d3", Member: "echo", TurnSeq: 4, Term: term, ExpectedVersion: updated.Version,
	}, steward); !errors.Is(err, ErrForbidden) {
		t.Fatalf("暂停后派发应拒绝（租约过期授权先行）: %v", err)
	}
	// 同事件重放：幂等（零新增取消）。
	if _, n, err := st.PauseExpired(sp.ID, "负责人租约已过期（调度前置检查）"); err != nil || n != 0 {
		t.Fatalf("重放应幂等零变更: n=%d err=%v", n, err)
	}
}

func TestW0402SystemPause(t *testing.T) {
	st, clk, _ := newTestStore(t)
	sp, _, err := st.CreateSpace(CreateRequest{
		Goal: "g", Mode: ModeManual, Members: []Member{{Name: "echo"}},
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	// 待配置态：系统暂停零变更（幂等）。
	if _, applied, err := st.SystemPause(sp.ID, "x"); err != nil || applied {
		t.Fatalf("非运行态系统暂停应零变更: applied=%v err=%v", applied, err)
	}
	// manual 即运行前提：ready→start。
	res, _, err := st.Control(sp.ID, ControlRequest{RequestID: "r1", Action: ActReady, ExpectedVersion: sp.Version}, owner)
	if err != nil {
		t.Fatal(err)
	}
	res, _, err = st.Control(sp.ID, ControlRequest{RequestID: "s1", Action: ActStart, ExpectedVersion: res.Space.Version}, owner)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Second)
	updated, applied, err := st.SystemPause(sp.ID, "成员轮被取消（人工介入）")
	if err != nil || !applied || updated.Status != StatusPaused {
		t.Fatalf("运行态系统暂停应成立: applied=%v status=%s err=%v", applied, updated.Status, err)
	}
	if updated.Used.WallSeconds == 0 {
		t.Fatal("系统暂停应结算 running 墙钟（settleRunning）")
	}
	// 幂等（状态判定）：再次系统暂停零变更（applied=false，非运行态无需暂停）。
	if _, applied, err := st.SystemPause(sp.ID, "成员轮被取消（人工介入）"); err != nil || applied {
		t.Fatalf("再次系统暂停应零变更: applied=%v err=%v", applied, err)
	}
}
