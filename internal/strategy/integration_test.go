package strategy

// collab.StrategyHooks 适配器集成测试（真实 collab Store 装配，零网络零模型）：
// 引擎经 collab.Options.Hooks 装配期注入；finish/finalize(done) 走引擎判据——
// 证据缺失 → completion_not_satisfied + 精确缺口清单；主人 finalize(abandoned)
// 不经判据照常可用（W04 契约：无判据只开放放弃定版）。
//
// 边界登记（W05-03 交接面）：collab 尚无写 Space.Phase 的动作（阶段落账归
// W05-03 接线），故「全满足→完成」路径在本文件以 fixture SpaceView（阶段已置）
// 在适配器层验证（TestCompletionAllSatisfiedPass）；本集成验证装配 + 拒绝面 +
// 放弃定版面。

import (
	"errors"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

var (
	intOwner  = collab.Subject{Name: "owner", Class: collab.ClassOwner}
	intSystem = collab.Subject{Name: "system", Class: collab.ClassSystem}
)

// newWiredStore 建 hooks 已装配的 collab Store（fake 时钟）。
func newWiredStore(t *testing.T, eng *Engine) (*collab.Store, *fakeClock) {
	t.Helper()
	dir := t.TempDir()
	clk := newFakeClock()
	st, err := collab.Open(dir, collab.Options{Now: clk.Now, Hooks: eng})
	if err != nil {
		t.Fatalf("collab.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, clk
}

func mustControl(t *testing.T, st *collab.Store, spaceID string, req collab.ControlRequest, subj collab.Subject) collab.ControlResult {
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

// wiredRunningSpace 建成员齐备、完成授权已开的 running 写审空间。
func wiredRunningSpace(t *testing.T, st *collab.Store) (string, int64) {
	t.Helper()
	sp, _, err := st.CreateSpace(collab.CreateRequest{
		Goal:       "写审循环目标",
		Acceptance: "验收：go test 全绿",
		Mode:       collab.ModeManual,
		Members: []collab.Member{
			{Name: "alice", Role: "writer", SessionID: "sess-alice"},
			{Name: "bob", Role: "reviewer", SessionID: "sess-bob"},
		},
		Policy: &collab.Policy{FinishAuth: collab.FinishAuthVerifiedWriteReview},
	}, intOwner)
	if err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, sp.ID, collab.ControlRequest{RequestID: "rdy-" + sp.ID, Action: collab.ActReady, ExpectedVersion: 1}, intOwner)
	mustControl(t, st, sp.ID, collab.ControlRequest{RequestID: "start-" + sp.ID, Action: collab.ActStart, ExpectedVersion: 2}, intOwner)
	return sp.ID, 3
}

// 完成类动作经引擎判据：证据缺失 → completion_not_satisfied + 缺口清单进错误
// 人话；主人放弃定版不经判据；判据满足路径在适配器层已证（见文件头边界登记）。
func TestEngineHooksWiredIntoCollabStore(t *testing.T) {
	eng := newTestEngine(t)
	st, _ := newWiredStore(t, eng)
	id, ver := wiredRunningSpace(t, st)

	// finish（驱动者=owner manual 持任期 1）：引擎判据拒绝 + 精确缺口。
	_, _, err := st.Control(id, collab.ControlRequest{RequestID: "fin-1", Action: collab.ActFinish, ExpectedVersion: ver, Term: 1}, intOwner)
	if !errors.Is(err, collab.ErrCompletionNotSatisfied) {
		t.Fatalf("证据缺失 finish 应 completion_not_satisfied，得 %v", err)
	}
	if !strings.Contains(err.Error(), "无登记版本") {
		t.Fatalf("缺口清单须点名缺口：%v", err)
	}
	// 拒绝零副作用：版本不动、状态不动。
	sp := mustSpace(t, st, id)
	if sp.Version != ver || sp.Status != collab.StatusRunning {
		t.Fatalf("拒绝应零副作用：ver=%d status=%s", sp.Version, sp.Status)
	}

	// finalize(done) 同判据。
	_, _, err = st.Control(id, collab.ControlRequest{RequestID: "fz-1", Action: collab.ActFinalize, ExpectedVersion: ver, FinalOutcome: "done"}, intOwner)
	if !errors.Is(err, collab.ErrCompletionNotSatisfied) {
		t.Fatalf("finalize(done) 同判据应拒，得 %v", err)
	}

	// finalize(abandoned)：不经判据照常可用（人工放弃定版）。
	mustControl(t, st, id, collab.ControlRequest{RequestID: "fz-2", Action: collab.ActFinalize, ExpectedVersion: ver, FinalOutcome: "abandoned", Reason: "需求变更放弃"}, intOwner)
	sp = mustSpace(t, st, id)
	if sp.Status != collab.StatusAbandoned || sp.FinalOutcome != "abandoned" {
		t.Fatalf("放弃定版不符：%+v", sp)
	}
}

// 判据满足路径（fixture 阶段置位）+ 真实 store 空间：手动构造 SpaceView 验证
// 适配器返回 Satisfied（阶段真相由 W05-03 落账，见文件头边界登记）。
func TestEngineSatisfiedVerdictShape(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-int-sat"
	satisfiedFixture(t, eng, sp)
	verdict := eng.EvaluateCompletion(fixtureSpace(sp, string(PhaseReview), 0))
	if !verdict.Satisfied || len(verdict.EvidenceRefs) == 0 || verdict.Reason == "" {
		t.Fatalf("全满足判定形态不符：%+v", verdict)
	}
}

// hooks 未装配时 collab 完成类动作拒绝（W04 契约回归锚：本包不破坏缺省行为）。
func TestCollabWithoutHooksStillRejectsFinish(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	st, err := collab.Open(dir, collab.Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sp, _, err := st.CreateSpace(collab.CreateRequest{
		Goal: "g", Acceptance: "a", Mode: collab.ModeManual,
		Members: []collab.Member{{Name: "alice", Role: "writer", SessionID: "s1"}},
		Policy:  &collab.Policy{FinishAuth: collab.FinishAuthVerifiedWriteReview},
	}, intOwner)
	if err != nil {
		t.Fatal(err)
	}
	mustControl(t, st, sp.ID, collab.ControlRequest{RequestID: "r", Action: collab.ActReady, ExpectedVersion: 1}, intOwner)
	mustControl(t, st, sp.ID, collab.ControlRequest{RequestID: "s", Action: collab.ActStart, ExpectedVersion: 2}, intOwner)
	if _, _, err := st.Control(sp.ID, collab.ControlRequest{RequestID: "f", Action: collab.ActFinish, ExpectedVersion: 3, Term: 1}, intOwner); !errors.Is(err, collab.ErrFinishPolicyUnavailable) {
		t.Fatalf("无 hooks finish 应 finish_policy_unavailable，得 %v", err)
	}
}

// 适配器接口断言（运行时形态校验，编译期断言在 strategy.go var _ 处）。
func TestEngineImplementsStrategyHooks(t *testing.T) {
	var hooks collab.StrategyHooks = (*Engine)(nil)
	_ = hooks
	eng := newTestEngine(t)
	var _ collab.StrategyHooks = eng
}

// mustSpace 按名取空间（集成断言助手）。
func mustSpace(t *testing.T, st *collab.Store, id string) collab.Space {
	t.Helper()
	sp, ok := st.Space(id)
	if !ok {
		t.Fatalf("空间 %s 不存在", id)
	}
	return sp
}

// intSystem 引用锚（system 主体形态留给 W05-03 内置服务装配用例）。
var _ = intSystem
