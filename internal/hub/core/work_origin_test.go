// work_origin_test.go OriginTask 挂接（DESIGN-butler 方案二，2026-10-28）：挂接
// 锚点校验（存在 + Orchestrator 卡精确匹配）、幂等命名空间（指纹含 origin）、
// RelatedWorks 反查降序、重启持久。
package core

import (
	"errors"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// originFixture 标准基座：mac-mini 挂 butler（Orchestrator）+ worker（普通卡），
// 提交一个指向 butler 的主任务作为挂接锚点，返回 (taskID, store, core, dataDir)。
func originFixture(t *testing.T) (string, *store.Store, *Core, string) {
	t.Helper()
	dir := t.TempDir()
	st, c := reopenHub(t, dir)
	if err := c.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "butler", Orchestrator: true, Exposed: true},
		{Name: "worker", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	ack, err := c.Submit(submitReq("mac-mini", "butler", "复合任务"), "")
	if err != nil {
		t.Fatal(err)
	}
	return ack.TaskID, st, c, dir
}

func TestCreateWorkOriginAnchor(t *testing.T) {
	taskID, _, c, _ := originFixture(t)

	// 挂接到 butler 卡收到的任务：成功，OriginTask 落库
	w, _, err := c.CreateWork(WorkCreateRequest{
		Goal: "拆解执行复合任务", OriginTask: taskID,
		Steps: []WorkStepInput{{Desc: "子步", TargetNode: "mac-mini", TargetAgent: "worker"}},
	}, "tester")
	if err != nil {
		t.Fatalf("合法挂接应成功: %v", err)
	}
	if w.OriginTask != taskID {
		t.Fatalf("OriginTask 应落库: got %q want %q", w.OriginTask, taskID)
	}

	// 锚点任务不存在 → 拒绝
	_, _, err = c.CreateWork(WorkCreateRequest{
		Goal: "x", OriginTask: "t-nonexistent",
		Steps: []WorkStepInput{{Desc: "s", TargetNode: "mac-mini", TargetAgent: "worker"}},
	}, "tester")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("锚点不存在应 ErrInvalid, got %v", err)
	}

	// 锚点任务的派发目标不是 Orchestrator 卡（worker）→ 拒绝
	ack, _ := c.Submit(submitReq("mac-mini", "worker", "普通任务"), "")
	_, _, err = c.CreateWork(WorkCreateRequest{
		Goal: "y", OriginTask: ack.TaskID,
		Steps: []WorkStepInput{{Desc: "s", TargetNode: "mac-mini", TargetAgent: "worker"}},
	}, "tester")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("非统筹卡锚点应 ErrInvalid, got %v", err)
	}
}

func TestCreateWorkOriginIdempotence(t *testing.T) {
	taskID, _, c, _ := originFixture(t)
	ack2, _ := c.Submit(submitReq("mac-mini", "butler", "复合任务二号"), "")

	steps := []WorkStepInput{{Desc: "子步", TargetNode: "mac-mini", TargetAgent: "worker"}}
	// 同计划 + 同 origin → 幂等命中同一单
	w1, dup1, _ := c.CreateWork(WorkCreateRequest{Goal: "g", OriginTask: taskID, Steps: steps}, "tester")
	w1b, dup1b, _ := c.CreateWork(WorkCreateRequest{Goal: "g", OriginTask: taskID, Steps: steps}, "tester")
	if w1.ID != w1b.ID || !dup1b {
		t.Fatalf("同 origin 同计划应命中同一单: %s vs %s dup=%v", w1.ID, w1b.ID, dup1b)
	}
	_ = dup1
	// 同计划 + 不同 origin → 不撞单（幂等命名空间，R3' P1-3）
	w2, _, err := c.CreateWork(WorkCreateRequest{Goal: "g", OriginTask: ack2.TaskID, Steps: steps}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w2.ID == w1.ID {
		t.Fatal("不同 origin 的同计划不得撞单")
	}
	// 无 origin 的同计划 → 第三张单（历史口径：无 origin 指纹与有 origin 互不相撞）
	w3, _, err := c.CreateWork(WorkCreateRequest{Goal: "g", Steps: steps}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w3.ID == w1.ID || w3.ID == w2.ID {
		t.Fatal("无 origin 同计划不得与有 origin 单相撞")
	}
}

func TestRelatedWorksOrderAndEmpty(t *testing.T) {
	taskID, _, c, _ := originFixture(t)
	steps := []WorkStepInput{{Desc: "子步", TargetNode: "mac-mini", TargetAgent: "worker"}}
	wa, _, _ := c.CreateWork(WorkCreateRequest{Goal: "第一张", OriginTask: taskID, Steps: steps}, "tester")
	wb, _, _ := c.CreateWork(WorkCreateRequest{Goal: "第二张", OriginTask: taskID, Steps: steps}, "tester")

	rel := c.RelatedWorks(taskID)
	if len(rel) != 2 {
		t.Fatalf("应反查 2 单, got %d", len(rel))
	}
	if rel[0].ID != wb.ID || rel[1].ID != wa.ID {
		t.Fatalf("应创建时间降序（最新在前）: %v", rel)
	}
	// 无挂接任务 → 空切片
	if got := c.RelatedWorks("t-none"); len(got) != 0 {
		t.Fatalf("无挂接应空, got %v", got)
	}
}

func TestWorkOriginPersistsAcrossRestart(t *testing.T) {
	taskID, st, c, dir := originFixture(t)
	steps := []WorkStepInput{{Desc: "子步", TargetNode: "mac-mini", TargetAgent: "worker"}}
	w1, _, _ := c.CreateWork(WorkCreateRequest{Goal: "g", OriginTask: taskID, Steps: steps}, "tester")
	_ = st.Close()

	st2, c2 := reopenHub(t, dir)
	defer func() { _ = st2.Close() }()
	got, ok := c2.st.View().Works[w1.ID]
	if !ok {
		t.Fatal("重启后工作单应仍在")
	}
	if got.OriginTask != taskID {
		t.Fatalf("重启后 OriginTask 应持久: got %q", got.OriginTask)
	}
	if len(c2.RelatedWorks(taskID)) != 1 {
		t.Fatal("重启后反查应仍命中")
	}
}

// TestOrchestratorCardStateViewAgree 统筹卡判定双形态一致锁（R1-01）：建单事务内
// 的纯函数 orchestratorCardInState（State 副本）与 View 侧 IsOrchestratorCard 对
// 同一状态必须同判——exposed/未 exposed、有无声明、未知 node/agent 逐卡对照。
func TestOrchestratorCardStateViewAgree(t *testing.T) {
	_, _, c, _ := originFixture(t)
	// 追加一组混淆卡：hidden 带声明但未 exposed（两形态都不得判真）
	if err := c.RegisterNode("win-box", []protocol.AgentCard{
		{Name: "hidden-butler", Orchestrator: true},
		{Name: "plain", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	for nodeID, rec := range c.st.View().Nodes {
		for _, card := range rec.Agents {
			want := c.IsOrchestratorCard(nodeID, card.Name)
			got := orchestratorCardInState(c.st.View(), nodeID, card.Name)
			if got != want {
				t.Fatalf("判定不一致 %s/%s: state=%v view=%v", nodeID, card.Name, got, want)
			}
		}
	}
	// 未知 node / 未知 agent：双假
	if orchestratorCardInState(c.st.View(), "ghost", "x") || c.IsOrchestratorCard("ghost", "x") {
		t.Fatal("未知 node 应双假")
	}
	if orchestratorCardInState(c.st.View(), "mac-mini", "ghost") || c.IsOrchestratorCard("mac-mini", "ghost") {
		t.Fatal("未知 agent 应双假")
	}
}

// TestCreateWorkOriginReceiptAfterRevocation 幂等回执不重校验（R1-01 场景一）：
// 建单成功后管家声明被撤销（同卡重报去 Orchestrator 标记），原样重试必须返回
// 原单 dup 回执，而非 ErrInvalid。
func TestCreateWorkOriginReceiptAfterRevocation(t *testing.T) {
	taskID, _, c, _ := originFixture(t)
	req := WorkCreateRequest{Goal: "retry", OriginTask: taskID,
		Steps: []WorkStepInput{{Desc: "s", TargetNode: "mac-mini", TargetAgent: "worker"}}}
	w, _, err := c.CreateWork(req, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "butler", Exposed: true}, {Name: "worker", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, dup, err := c.CreateWork(req, "tester")
	if err != nil {
		t.Fatalf("声明撤销后重试应返回原单回执而非拒绝: %v", err)
	}
	if !dup || got.ID != w.ID {
		t.Fatalf("应命中原单: dup=%v id=%s want %s", dup, got.ID, w.ID)
	}
}

// TestCreateWorkOriginReceiptAfterTaskCleanup 幂等回执不重校验（R1-01 场景二）：
// 主任务先于工作单过保留期清理后，原样重试仍返回原单 dup 回执。
func TestCreateWorkOriginReceiptAfterTaskCleanup(t *testing.T) {
	taskID, st, c, _ := originFixture(t)
	req := WorkCreateRequest{Goal: "retry", OriginTask: taskID,
		Steps: []WorkStepInput{{Desc: "s", TargetNode: "mac-mini", TargetAgent: "worker"}}}
	w, _, err := c.CreateWork(req, "tester")
	if err != nil {
		t.Fatal(err)
	}
	// 保留期清扫的账本效果等价模拟：任务从账本移除（工作单与幂等映射独立保留）
	if err := st.Update(func(s *store.State) error {
		delete(s.Tasks, taskID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, dup, err := c.CreateWork(req, "tester")
	if err != nil {
		t.Fatalf("主任务清理后重试应返回原单回执而非拒绝: %v", err)
	}
	if !dup || got.ID != w.ID {
		t.Fatalf("应命中原单: dup=%v id=%s want %s", dup, got.ID, w.ID)
	}
}

// TestCreateWorkOriginNewCreateStillValidated 确定新建仍校验（R1-01 反向面）：
// 幂等豁免只覆盖回执命中；锚点缺失（主任务已清理）或声明已撤销时，不同计划的
// 新建请求一律 ErrInvalid——软校验不因 R1-01 放松。
func TestCreateWorkOriginNewCreateStillValidated(t *testing.T) {
	taskID, st, c, _ := originFixture(t)
	steps := []WorkStepInput{{Desc: "s", TargetNode: "mac-mini", TargetAgent: "worker"}}
	if _, _, err := c.CreateWork(WorkCreateRequest{Goal: "第一单", OriginTask: taskID, Steps: steps}, "tester"); err != nil {
		t.Fatal(err)
	}
	// 撤销声明 + 清理主任务
	if err := c.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "butler", Exposed: true}, {Name: "worker", Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *store.State) error {
		delete(s.Tasks, taskID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 锚点缺失的新建 → ErrInvalid
	if _, _, err := c.CreateWork(WorkCreateRequest{Goal: "第二单", OriginTask: taskID, Steps: steps}, "tester"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("锚点已清理的新建应 ErrInvalid, got %v", err)
	}
	// 任务存在但声明已撤销的新建 → ErrInvalid
	ack, err := c.Submit(submitReq("mac-mini", "butler", "复合任务三号"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CreateWork(WorkCreateRequest{Goal: "第三单", OriginTask: ack.TaskID, Steps: steps}, "tester"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("声明已撤销的新建应 ErrInvalid, got %v", err)
	}
}

// TestWorkContentHashHistoricalGolden 历史 golden 锁（R1-07/审查附注）：无 origin
// 的建单指纹与 origin 字段加入前字节一致——既有 WorkIdems 映射零失效的升级回归
// 锁（快照迁移/排查依赖指纹跨版本稳定）。hash 固定，不随实现重排漂移。
func TestWorkContentHashHistoricalGolden(t *testing.T) {
	got := workContentHash("g", normalizeWorkSteps([]WorkStepInput{{Desc: "s", TargetNode: "n", TargetAgent: "a"}}), "")
	if got != "aaf13801dd0b269b4f74678aac5b6ea7a40aaaeb7937bf8e04bb884104a01a3e" {
		t.Fatalf("无 origin 历史 hash 漂移（既有 WorkIdems 将失配）: %s", got)
	}
}
