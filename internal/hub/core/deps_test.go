package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// P4.3 任务依赖门控与调度测试（SPEC 4.12）：准入拒绝矩阵、依赖顺序、上游失败、
// superseded 流程、信箱占配额、Sweep 兜底、放行单向、排队超时豁免、重启恢复。

// submitDeps 提交带依赖声明的任务辅助（去重样板；断言成功）。
func submitDeps(t *testing.T, f *hubFixture, node, stdin string, deps []string, idem string) string {
	t.Helper()
	req := submitReq(node, "echo", stdin)
	req.DependsOn = deps
	req.IdemKey = idem
	ack, err := f.core.Submit(req, "")
	if err != nil {
		t.Fatalf("提交依赖任务失败: %v", err)
	}
	return ack.TaskID
}

// completeViaNode node 回传终态结果（走真实 task_result 帧链路）。
func completeViaNode(n *testNode, taskID string, status string) {
	n.result(taskID, protocol.TaskResult{Status: status, ExitCode: 0, FinalAnswer: "done:" + status})
}

// countOffers 统计 node 收到的指定任务 offer 次数。
func countOffers(n *testNode, taskID string) int {
	c := 0
	for _, o := range n.offers {
		if o.ID == taskID {
			c++
		}
	}
	return c
}

// markCompletedInStore 绕过 handleTaskResult 直接置 completed（模拟复评事件丢失：
// 无下游复评、无 dispatch）。
func markCompletedInStore(t *testing.T, f *hubFixture, taskID string) {
	t.Helper()
	if err := f.st.Update(func(st *store.State) error {
		tk := st.Tasks[taskID].Clone()
		tk.Status = protocol.StatusCompleted
		tk.Result = &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0}
		st.Tasks[taskID] = tk
		return nil
	}); err != nil {
		t.Fatalf("注入 completed 失败: %v", err)
	}
}

// markFailedInStore 绕过 handleTaskResult 直接置 failed（离线场景模拟上游失败
// 终态；无下游复评——由随后的 Sweep 兜底触发）。
func markFailedInStore(t *testing.T, f *hubFixture, taskID string) {
	t.Helper()
	if err := f.st.Update(func(st *store.State) error {
		tk := st.Tasks[taskID].Clone()
		tk.Status = protocol.StatusFailed
		tk.Result = &protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: protocol.ExitCodeNotRun}
		st.Tasks[taskID] = tk
		return nil
	}); err != nil {
		t.Fatalf("注入 failed 失败: %v", err)
	}
}

// TestDependsAdmissionRejections 依赖准入拒绝矩阵（SPEC 4.12：未知 idem/自引用/
// 重复依赖/空项/超深度/超级联/依赖环 → ErrInvalidDeps）。
func TestDependsAdmissionRejections(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	// 铺链 up-c1←up-c2←up-c3←up-c4（各带 idem，供深度边界）
	for i, idem := range []string{"up-c1", "up-c2", "up-c3", "up-c4"} {
		var deps []string
		if i > 0 {
			deps = []string{[]string{"up-c1", "up-c2", "up-c3"}[i-1]}
		}
		submitDeps(t, f, "nodeB", idem, deps, idem)
	}

	bad := []struct {
		name string
		deps []string
		idem string
	}{
		{"未知 idem", []string{"ghost-x"}, ""},
		{"未知 idem 混合法", []string{"up-c1", "ghost-x"}, ""},
		{"自引用", []string{"self-1"}, "self-1"},
		{"重复依赖", []string{"up-c1", "up-c1"}, ""},
		{"空项", []string{""}, ""},
	}
	for _, tc := range bad {
		req := submitReq("nodeB", "echo", "x")
		req.DependsOn = tc.deps
		req.IdemKey = tc.idem
		if _, err := f.core.Submit(req, ""); !errors.Is(err, ErrInvalidDeps) {
			t.Errorf("%s: 期望 ErrInvalidDeps, got %v", tc.name, err)
		}
	}

	// 深度边界：第 5 层任务（深度 4 边）放行；第 6 层（深度 5 边）拒绝
	submitDeps(t, f, "nodeB", "c5", []string{"up-c4"}, "up-c5")
	six := submitReq("nodeB", "echo", "c6")
	six.DependsOn = []string{"up-c5"}
	if _, err := f.core.Submit(six, ""); !errors.Is(err, ErrInvalidDeps) {
		t.Errorf("深度 5 应拒绝, got %v", err)
	}

	// 级联边界：直依赖 8 个上游放行、9 个拒绝
	upIDs := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		r := submitReq("nodeB", "echo", "fan")
		r.IdemKey = "fan-" + string(rune('a'+i))
		if _, err := f.core.Submit(r, ""); err != nil {
			t.Fatalf("级联上游 %d 提交失败: %v", i, err)
		}
		upIDs = append(upIDs, r.IdemKey)
	}
	eight := submitReq("nodeB", "echo", "fan8")
	eight.DependsOn = upIDs[:8]
	if _, err := f.core.Submit(eight, ""); err != nil {
		t.Errorf("级联 8 应放行, got %v", err)
	}
	nine := submitReq("nodeB", "echo", "fan9")
	nine.DependsOn = upIDs
	if _, err := f.core.Submit(nine, ""); !errors.Is(err, ErrInvalidDeps) {
		t.Errorf("级联 9 应拒绝, got %v", err)
	}

	// 依赖环：C(idem c-1) 依赖 up-c1；up-c1 经终态清理移除后，新任务复用 idem
	// up-c1 并依赖 c-1 → 上溯链解析回自身 idem，环拒绝（防御 idem 复用构环）
	submitDeps(t, f, "nodeB", "c-task", []string{"up-c1"}, "c-1")
	removeByIDem(t, f, "up-c1")
	cycle := submitReq("nodeB", "echo", "cycle")
	cycle.IdemKey = "up-c1"
	cycle.DependsOn = []string{"c-1"}
	if _, err := f.core.Submit(cycle, ""); !errors.Is(err, ErrInvalidDeps) {
		t.Errorf("依赖环应拒绝, got %v", err)
	}
}

// removeByIDem 从快照删除携带 idem 的任务（模拟终态 24h 保留清理）。
func removeByIDem(t *testing.T, f *hubFixture, idem string) {
	t.Helper()
	if err := f.st.Update(func(st *store.State) error {
		for id, tk := range st.Tasks {
			if tk.IdemKey == idem {
				delete(st.Tasks, id)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("清理事务失败: %v", err)
	}
}

// TestDependsOrdering 依赖顺序断言：上游未到终态前下游保持 submitted+blocked
// 不投递（即便节点在线、窗口有余量）；上游 completed 后同事务解除阻塞并投递，
// offer 携依赖清单（stdin 附上游 idem，挂载为 L2 第二子批）。
func TestDependsOrdering(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	upID := submitDeps(t, f, "nodeB", "upstream", nil, "up-1")
	downID := submitDeps(t, f, "nodeB", "downstream", []string{"up-1"}, "")

	td := f.taskOf(downID)
	if td.Status != protocol.StatusSubmitted || td.BlockedReason != "blocked_by:up-1" {
		t.Fatalf("下游应 submitted+blocked_by:up-1, got %s/%q", td.Status, td.BlockedReason)
	}
	if len(n.offers) != 1 || n.offers[0].ID != upID {
		t.Fatalf("上游未终态前只应投递上游, got %d offers", len(n.offers))
	}

	completeViaNode(n, upID, protocol.ResultCompleted)

	td = f.taskOf(downID)
	if td.BlockedReason != "" {
		t.Fatalf("上游 completed 后下游应解除阻塞, got %q", td.BlockedReason)
	}
	if len(n.offers) != 2 || n.offers[1].ID != downID {
		t.Fatalf("解除阻塞后下游应投递, got %d offers", len(n.offers))
	}
	// 依赖顺序：上游先到终态、下游 offer 后发
	if tu := f.taskOf(upID); tu.Status != protocol.StatusCompleted {
		t.Fatalf("上游应先到终态, got %s", tu.Status)
	}
	// offer 载荷 stdin 附依赖清单（上游 idem 供 agent 自取）
	manifest := n.offers[1].Input.Stdin
	if !strings.Contains(manifest, protocol.DepManifestMarker) || !strings.Contains(manifest, "up-1") {
		t.Errorf("offer stdin 应附依赖清单, got %q", manifest)
	}
	// 落库任务体同口径（查询面可见）
	if !strings.Contains(f.taskOf(downID).Input.Stdin, "up-1") {
		t.Error("落库任务 stdin 应附依赖清单")
	}
}

// TestDependsUpstreamFailed 上游 failed/needs_review/cancelled → 下游保持
// blocked 且 blocked_reason 更新为 upstream_failed:<idem>（不进状态机）。
func TestDependsUpstreamFailed(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	for _, status := range []string{protocol.ResultFailed, protocol.ResultNeedsReview} {
		upID := submitDeps(t, f, "nodeB", "up", nil, "dep-"+status)
		downID := submitDeps(t, f, "nodeB", "down", []string{"dep-" + status}, "")
		completeViaNode(n, upID, status)
		td := f.taskOf(downID)
		if td.Status != protocol.StatusSubmitted || td.BlockedReason != "upstream_failed:dep-"+status {
			t.Errorf("%s: 期望 upstream_failed 标注, got %s/%q", status, td.Status, td.BlockedReason)
		}
		if offers := countOffers(n, downID); offers != 0 {
			t.Errorf("%s: 上游失败下游不得投递, got %d offers", status, offers)
		}
	}

	// cancelled：管理面取消终态同口径（CancelTask 事务内复评）
	upID := submitDeps(t, f, "nodeB", "up", nil, "dep-cancel")
	downID := submitDeps(t, f, "nodeB", "down", []string{"dep-cancel"}, "")
	if _, _, err := f.core.CancelTask(upID, "测试取消"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	td := f.taskOf(downID)
	if td.BlockedReason != "upstream_failed:dep-cancel" {
		t.Errorf("cancelled 应更新 upstream_failed, got %q", td.BlockedReason)
	}
}

// TestSupersededFlow superseded 流程：上游 failed → 下游 blocked(upstream_failed)；
// 上游任务经终态清理移除后同 idem 新版本提交并 completed → 旧下游任务记
// SupersededBy（审计可见、不进状态机、不自动放行，人工决定是否重派）。
func TestSupersededFlow(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	up1 := submitDeps(t, f, "nodeB", "v1", nil, "sw-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"sw-1"}, "")
	completeViaNode(n, up1, protocol.ResultFailed)
	if td := f.taskOf(downID); td.BlockedReason != "upstream_failed:sw-1" {
		t.Fatalf("前置失败：下游应 upstream_failed:sw-1, got %q", td.BlockedReason)
	}

	// 模拟 24h 终态保留清理：up1 出快照，idem sw-1 释放
	removeByIDem(t, f, "sw-1")

	// 上游重跑：同 idem 新版本提交并 completed
	up2 := submitDeps(t, f, "nodeB", "v2", nil, "sw-1")
	completeViaNode(n, up2, protocol.ResultCompleted)

	td := f.taskOf(downID)
	if td.SupersededBy != up2 {
		t.Errorf("旧下游应记 superseded_by=%s, got %q", up2, td.SupersededBy)
	}
	if td.Status != protocol.StatusSubmitted || td.BlockedReason != "upstream_failed:sw-1" {
		t.Errorf("superseded 不进状态机：应保持 submitted+blocked, got %s/%q", td.Status, td.BlockedReason)
	}
	if countOffers(n, downID) != 0 {
		t.Error("superseded 任务不得自动放行投递")
	}
}

// TestDependsBlockedOccupiesQuota blocked 任务留信箱占配额：信箱满拒新提交的
// 既有语义不变（SPEC 4.12）。
func TestDependsBlockedOccupiesQuota(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	submitDeps(t, f, "nodeB", "up", nil, "up-1")
	submitDeps(t, f, "nodeB", "down", []string{"up-1"}, "") // blocked，占 1 配额
	for i := 0; i < protocol.MailboxQuota-2; i++ {
		if _, err := f.core.Submit(submitReq("nodeB", "echo", "fill"), ""); err != nil {
			t.Fatalf("填充第 %d 条失败: %v", i+3, err)
		}
	}
	if mb := f.countMailbox("nodeB"); mb != protocol.MailboxQuota {
		t.Fatalf("前置失败：信箱应满, got %d", mb)
	}
	if _, err := f.core.Submit(submitReq("nodeB", "echo", "new"), ""); !errors.Is(err, mailbox.ErrMailboxFull) {
		t.Errorf("blocked 占配额下信箱满应拒新提交, got %v", err)
	}
}

// TestDependsSweepFallback Sweep 兜底：终态事务处的复评事件丢失（直接改库模拟）
// 后，兜底扫描解除下游阻塞（防事件丢失）。
func TestDependsSweepFallback(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	upID := submitDeps(t, f, "nodeB", "up", nil, "up-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"up-1"}, "")

	markCompletedInStore(t, f, upID)
	if td := f.taskOf(downID); td.BlockedReason == "" {
		t.Fatal("前置失败：注入后下游应仍 blocked")
	}

	f.core.Sweep() // 兜底复评 + 解除阻塞随本轮 dispatch

	td := f.taskOf(downID)
	if td.BlockedReason != "" {
		t.Fatalf("Sweep 兜底应解除阻塞, got %q", td.BlockedReason)
	}
	if countOffers(n, downID) != 1 {
		t.Errorf("Sweep 解除阻塞后应投递下游, got %d offers", countOffers(n, downID))
	}
}

// TestDependsUnblockedNotReblocked 放行单向：已解除阻塞的下游任务不因上游
// 终态清理（idem 消失）被重新挡回（依赖均为终态不再变化）。
func TestDependsUnblockedNotReblocked(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo")) // 离线：任务滞留信箱便于观察

	upID := submitDeps(t, f, "nodeB", "up", nil, "up-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"up-1"}, "")

	// 事件丢失模拟 + Sweep 解除阻塞（离线不投递，保持 submitted）
	markCompletedInStore(t, f, upID)
	f.core.Sweep()
	if td := f.taskOf(downID); td.BlockedReason != "" {
		t.Fatalf("前置失败：Sweep 应解除阻塞, got %q", td.BlockedReason)
	}

	// 上游被终态清理移除 → 下游不得被重新阻塞
	removeByIDem(t, f, "up-1")
	f.core.Sweep()
	td := f.taskOf(downID)
	if td.BlockedReason != "" {
		t.Errorf("已放行任务不得被重新阻塞, got %q", td.BlockedReason)
	}
	if td.Status != protocol.StatusSubmitted {
		t.Errorf("离线滞留应保持 submitted, got %s", td.Status)
	}
}

// TestDependsMixedStates 多依赖混合状态：等待中与失败并存时标注取失败优先；
// 部分满足不投递；失败依赖不可自愈（保持 blocked，人工重派）；全部满足才放行。
func TestDependsMixedStates(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	depOK := submitDeps(t, f, "nodeB", "ok", nil, "mx-ok")
	depOK2 := submitDeps(t, f, "nodeB", "ok2", nil, "mx-ok2")
	depBad := submitDeps(t, f, "nodeB", "bad", nil, "mx-bad")
	depSlow := submitDeps(t, f, "nodeB", "slow", nil, "mx-slow")
	downID := submitDeps(t, f, "nodeB", "down", []string{"mx-slow", "mx-bad", "mx-ok"}, "")
	// 双依赖全满足路径（对照）
	bothID := submitDeps(t, f, "nodeB", "both", []string{"mx-ok", "mx-ok2"}, "")

	// 全未终态：blocked_by 取声明序首个
	if td := f.taskOf(downID); td.BlockedReason != "blocked_by:mx-slow" {
		t.Fatalf("应 blocked_by 首个声明项, got %q", td.BlockedReason)
	}
	// slow 满足、bad 未终态：仍等待 bad
	completeViaNode(n, depSlow, protocol.ResultCompleted)
	if td := f.taskOf(downID); td.BlockedReason != "blocked_by:mx-bad" {
		t.Fatalf("应转 blocked_by:mx-bad, got %q", td.BlockedReason)
	}
	// bad 失败：失败优先标注 upstream_failed
	completeViaNode(n, depBad, protocol.ResultFailed)
	if td := f.taskOf(downID); td.BlockedReason != "upstream_failed:mx-bad" {
		t.Fatalf("失败应优先标注, got %q", td.BlockedReason)
	}
	// ok 满足也无法自愈失败依赖：保持 blocked（人工重派）
	completeViaNode(n, depOK, protocol.ResultCompleted)
	if td := f.taskOf(downID); td.BlockedReason != "upstream_failed:mx-bad" {
		t.Fatalf("失败依赖不可自愈, got %q", td.BlockedReason)
	}
	if countOffers(n, downID) != 0 {
		t.Errorf("含失败依赖的任务不得投递, got %d", countOffers(n, downID))
	}
	// 对照：双依赖全部 completed → 放行投递
	completeViaNode(n, depOK2, protocol.ResultCompleted)
	if td := f.taskOf(bothID); td.BlockedReason != "" {
		t.Fatalf("全依赖满足应放行, got %q", td.BlockedReason)
	}
	if countOffers(n, bothID) != 1 {
		t.Errorf("放行后应投递一次, got %d", countOffers(n, bothID))
	}
}

// TestDependsBlockedSurvivesQueueTimeout 依赖阻塞不走 P1.5 workdir 排队超时：
// blocked 任务等待的是上游终态而非 workdir，仅 7d expire 兜底。
func TestDependsBlockedSurvivesQueueTimeout(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	submitDeps(t, f, "nodeB", "up", nil, "up-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"up-1"}, "")

	f.clock.Advance(protocol.WorkdirQueueWait + time.Minute)
	f.core.Sweep()
	td := f.taskOf(downID)
	if td.Status != protocol.StatusSubmitted || td.BlockedReason != "blocked_by:up-1" {
		t.Errorf("依赖阻塞不得被排队超时置失败, got %s/%q", td.Status, td.BlockedReason)
	}
	// 7d expire 兜底照常
	f.clock.Advance(protocol.TaskRetention)
	f.core.Sweep()
	td = f.taskOf(downID)
	if td.Status != protocol.StatusFailed || td.Result == nil || td.Result.Error != protocol.CodeExpired {
		t.Errorf("blocked 任务 7d 应 failed/expired, got %s/%+v", td.Status, td.Result)
	}
}

// TestDependsRestartRecovery hub 重启恢复：依赖门控字段全在快照（depends_on/
// blocked_reason），重开后台任务保持阻塞、Sweep 兜底可解除（全在快照，恢复免费）。
func TestDependsRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	st1, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	clock := newFakeClock()
	core1, err := New(Config{Store: st1, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	if err := core1.RegisterNode("nodeB", agentsOf("echo")); err != nil {
		t.Fatal(err)
	}
	core1.SetOnline("nodeB", false) // 离线：任务滞留信箱

	f1 := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st1, core: core1, clock: clock}
	upID := submitDeps(t, f1, "nodeB", "up", nil, "up-1")
	downID := submitDeps(t, f1, "nodeB", "down", []string{"up-1"}, "")

	_ = core1.Close()
	_ = st1.Close()

	// 第二代 hub：同一 data_dir 重开
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	defer func() { _ = st2.Close() }()
	core2, err := New(Config{Store: st2, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	defer func() { _ = core2.Close() }()

	// 依赖字段随快照恢复
	td, ok := core2.GetTask(downID)
	if !ok {
		t.Fatal("任务未随快照恢复")
	}
	if len(td.DependsOn) != 1 || td.DependsOn[0] != "up-1" || td.BlockedReason != "blocked_by:up-1" {
		t.Fatalf("依赖字段未恢复: deps=%v reason=%q", td.DependsOn, td.BlockedReason)
	}

	// 重启后 Sweep 兜底：上游未终态保持 blocked；注入 completed 后解除
	core2.Sweep()
	if td, _ := core2.GetTask(downID); td.BlockedReason != "blocked_by:up-1" {
		t.Fatalf("重启后应保持阻塞, got %q", td.BlockedReason)
	}
	if err := st2.Update(func(st *store.State) error {
		tk := st.Tasks[upID].Clone()
		tk.Status = protocol.StatusCompleted
		tk.Result = &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0}
		st.Tasks[upID] = tk
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	core2.Sweep()
	if td, _ := core2.GetTask(downID); td.BlockedReason != "" {
		t.Fatalf("重启后 Sweep 兜底应解除阻塞, got %q", td.BlockedReason)
	}
}

// TestUpstreamFailedHistorySurvivesCleanup 清理后 Sweep（P1 修复）：下游
// upstream_failed:<idem> 后旧上游被终态清理，Sweep 复评不得把标注降级改写为
// blocked_by（失败史持久保留）；同 idem 新版本 completed 时补记 superseded_by
// 且保持阻塞——人工决定是否重派，不得静默解除阻塞。
func TestUpstreamFailedHistorySurvivesCleanup(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	up1 := submitDeps(t, f, "nodeB", "v1", nil, "fh-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"fh-1"}, "")
	completeViaNode(n, up1, protocol.ResultFailed)
	if td := f.taskOf(downID); td.BlockedReason != "upstream_failed:fh-1" {
		t.Fatalf("前置失败：下游应 upstream_failed:fh-1, got %q", td.BlockedReason)
	}

	// 旧上游清理（idem 短暂消失）→ Sweep 复评：失败史不得被降级抹除
	removeByIDem(t, f, "fh-1")
	f.core.Sweep()
	td := f.taskOf(downID)
	if td.BlockedReason != "upstream_failed:fh-1" {
		t.Fatalf("清理后 Sweep 不得降级失败史, got %q", td.BlockedReason)
	}
	if td.SupersededBy != "" {
		t.Errorf("无新版本不得记 superseded_by, got %q", td.SupersededBy)
	}
	if offers := countOffers(n, downID); offers != 0 {
		t.Errorf("失败史任务不得投递, got %d offers", offers)
	}

	// 同 idem 新版本 completed：补记 superseded_by，保持阻塞（人工决定重派）
	up2 := submitDeps(t, f, "nodeB", "v2", nil, "fh-1")
	completeViaNode(n, up2, protocol.ResultCompleted)
	td = f.taskOf(downID)
	if td.SupersededBy != up2 {
		t.Errorf("应记 superseded_by=%s, got %q", up2, td.SupersededBy)
	}
	if td.Status != protocol.StatusSubmitted || td.BlockedReason != "upstream_failed:fh-1" {
		t.Errorf("superseded 不进状态机：应保持 submitted+失败史标注, got %s/%q", td.Status, td.BlockedReason)
	}
	if countOffers(n, downID) != 0 {
		t.Error("superseded 任务不得自动放行投递")
	}
}

// TestUpstreamFailedHistorySurvivesRerun 重跑期间 Sweep（P1 修复）：旧上游
// 清理后同 idem 新版本运行中（非终态，复评解析为 pending）——Sweep 不得把
// 失败史降级为 blocked_by；新版本 completed 时按失败史补记 superseded_by。
func TestUpstreamFailedHistorySurvivesRerun(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	up1 := submitDeps(t, f, "nodeB", "v1", nil, "rr-1")
	downID := submitDeps(t, f, "nodeB", "down", []string{"rr-1"}, "")
	completeViaNode(n, up1, protocol.ResultFailed)
	removeByIDem(t, f, "rr-1")

	// 重跑新版本已投递运行中（非终态）→ Sweep 复评：保持失败史标注
	up2 := submitDeps(t, f, "nodeB", "v2", nil, "rr-1")
	if f.taskOf(up2).Status == protocol.StatusCompleted {
		t.Fatal("前置失败：新版本应处于运行中")
	}
	f.core.Sweep()
	td := f.taskOf(downID)
	if td.BlockedReason != "upstream_failed:rr-1" {
		t.Fatalf("重跑运行中 Sweep 不得降级失败史, got %q", td.BlockedReason)
	}
	if td.SupersededBy != "" {
		t.Errorf("新版本未 completed 不得记 superseded_by, got %q", td.SupersededBy)
	}

	// 新版本 completed：失败史识别生效 → superseded_by + 保持阻塞
	completeViaNode(n, up2, protocol.ResultCompleted)
	td = f.taskOf(downID)
	if td.SupersededBy != up2 || td.BlockedReason != "upstream_failed:rr-1" {
		t.Errorf("应记 superseded_by=%s 并保持失败史标注, got %q/%q", up2, td.SupersededBy, td.BlockedReason)
	}
	if countOffers(n, downID) != 0 {
		t.Error("superseded 任务不得自动放行投递")
	}
}

// TestUpstreamFailedHistorySurvivesRestart 重启后 Sweep（P1 修复）：失败史
// 标注随快照持久化，重启复评不降级；同 idem 新版本 completed → 补记
// superseded_by 且保持阻塞。
func TestUpstreamFailedHistorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	st1, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	clock := newFakeClock()
	core1, err := New(Config{Store: st1, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	f1 := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st1, core: core1, clock: clock}
	f1.registerOffline("nodeB", agentsOf("echo")) // 离线：上游失败经注入+Sweep 兜底

	upID := submitDeps(t, f1, "nodeB", "up", nil, "rh-1")
	downID := submitDeps(t, f1, "nodeB", "down", []string{"rh-1"}, "")
	markFailedInStore(t, f1, upID)
	core1.Sweep()
	if td := f1.taskOf(downID); td.BlockedReason != "upstream_failed:rh-1" {
		t.Fatalf("前置失败：下游应 upstream_failed:rh-1, got %q", td.BlockedReason)
	}
	removeByIDem(t, f1, "rh-1") // 旧上游清理后关停

	_ = core1.Close()
	_ = st1.Close()

	// 第二代 hub：同一 data_dir 重开
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	defer func() { _ = st2.Close() }()
	core2, err := New(Config{Store: st2, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	defer func() { _ = core2.Close() }()

	// 失败史随快照恢复；重启后 Sweep 不得降级
	td, ok := core2.GetTask(downID)
	if !ok {
		t.Fatal("任务未随快照恢复")
	}
	if td.BlockedReason != "upstream_failed:rh-1" {
		t.Fatalf("失败史未恢复: %q", td.BlockedReason)
	}
	core2.Sweep()
	if td, _ = core2.GetTask(downID); td.BlockedReason != "upstream_failed:rh-1" {
		t.Fatalf("重启后 Sweep 不得降级失败史, got %q", td.BlockedReason)
	}

	// 同 idem 新版本 completed（离线注入）→ Sweep 补记 superseded_by
	f2 := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st2, core: core2, clock: clock}
	f2.registerOffline("nodeB", agentsOf("echo"))
	up2 := submitDeps(t, f2, "nodeB", "v2", nil, "rh-1")
	markCompletedInStore(t, f2, up2)
	core2.Sweep()
	td, _ = core2.GetTask(downID)
	if td.SupersededBy != up2 {
		t.Errorf("重启后应记 superseded_by=%s, got %q", up2, td.SupersededBy)
	}
	if td.Status != protocol.StatusSubmitted || td.BlockedReason != "upstream_failed:rh-1" {
		t.Errorf("应保持 submitted+失败史标注, got %s/%q", td.Status, td.BlockedReason)
	}
}

// TestUpstreamFailedHistoryNoUnlockViaOtherDeps 失败史冻结不受其余依赖影响
// （P1 修复）：失败 idem 被清理（消失）后其余依赖全部满足——常规复评会得出
// 「全满足」结论解除阻塞，冻结口径下必须保持 blocked 且无 superseded_by
// （失败 idem 无 completed 新版本，人工决定重派或 7d expire 兜底）。
func TestUpstreamFailedHistoryNoUnlockViaOtherDeps(t *testing.T) {
	f := newHub(t)
	n := f.newNode("nodeB", agentsOf("echo"), true)

	badID := submitDeps(t, f, "nodeB", "bad", nil, "fx-bad")
	okID := submitDeps(t, f, "nodeB", "ok", nil, "fx-ok")
	downID := submitDeps(t, f, "nodeB", "down", []string{"fx-bad", "fx-ok"}, "")
	completeViaNode(n, badID, protocol.ResultFailed)
	if td := f.taskOf(downID); td.BlockedReason != "upstream_failed:fx-bad" {
		t.Fatalf("前置失败：应 upstream_failed:fx-bad, got %q", td.BlockedReason)
	}

	// 失败上游清理 + 其余依赖满足：不得解除阻塞、不得降级标注
	removeByIDem(t, f, "fx-bad")
	completeViaNode(n, okID, protocol.ResultCompleted)
	f.core.Sweep()
	td := f.taskOf(downID)
	if td.BlockedReason != "upstream_failed:fx-bad" {
		t.Fatalf("其余依赖满足不得越过失败史解锁, got %q", td.BlockedReason)
	}
	if td.SupersededBy != "" {
		t.Errorf("失败 idem 无新版本不得记 superseded_by, got %q", td.SupersededBy)
	}
	if countOffers(n, downID) != 0 {
		t.Errorf("失败史任务不得投递, got %d offers", countOffers(n, downID))
	}
}

// TestDependsDepthLongestChain 长短路径汇合（P1 修复）：深度按逐节点最长链
// 计（记忆化 DFS），去重集合只用于级联计数——既有链 A←B←C←D←E，新任务声明
// [E, C] 时两路径在 C 汇合，全局去重 BFS 按短路径（3 边）放行实际 5 边的
// 最长链；修复后按最长链拒绝。共享祖先在去重口径下只计一次。
func TestDependsDepthLongestChain(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	// 链 gp-a←gp-b←gp-c←gp-d←gp-e
	chain := []string{"gp-a", "gp-b", "gp-c", "gp-d", "gp-e"}
	for i, idem := range chain {
		var deps []string
		if i > 0 {
			deps = []string{chain[i-1]}
		}
		submitDeps(t, f, "nodeB", idem, deps, idem)
	}

	// 长短路径汇合：经 gp-e 最长 5 边（>4）须拒绝（修复前被去重按 3 边放行）
	nLong := submitReq("nodeB", "echo", "n-long")
	nLong.DependsOn = []string{"gp-e", "gp-c"}
	if _, err := f.core.Submit(nLong, ""); !errors.Is(err, ErrInvalidDeps) {
		t.Errorf("汇合最长链 5 边应拒绝, got %v", err)
	}

	// 恰 4 边的汇合放行：声明 [gp-d, gp-c]，经 gp-d 4 边；共享祖先
	// gp-c/gp-b/gp-a 去重只计一次，上游共 4 个 ≤ 8
	nEdge := submitReq("nodeB", "echo", "n-edge")
	nEdge.DependsOn = []string{"gp-d", "gp-c"}
	if _, err := f.core.Submit(nEdge, ""); err != nil {
		t.Errorf("汇合最长链恰 4 边应放行, got %v", err)
	}

	// 短路径单独声明不受链上更深节点存在的影响
	nShort := submitReq("nodeB", "echo", "n-short")
	nShort.DependsOn = []string{"gp-c"}
	if _, err := f.core.Submit(nShort, ""); err != nil {
		t.Errorf("短路径 3 边应放行, got %v", err)
	}
}
