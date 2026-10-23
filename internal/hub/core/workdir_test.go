package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// ---------- P1.5 workdir 互斥（SPEC 4.10）：hub 侧门控 / 排队超时 / head-of-line / 重启恢复 ----------

// wsCard 构造携带 workspace_id 的能力卡（互斥测试专用；既有 agentsOf 不带
// WorkspaceID，覆盖「未上报不参与互斥」的兼容口径）。
func wsCard(name, wsID string) protocol.AgentCard {
	return protocol.AgentCard{
		Name: name, Kind: protocol.AgentKindCLI,
		InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "no-shell", ProtocolVer: 1,
		WorkspaceID: wsID, Exposed: true,
	}
}

// lockOf 读互斥表当前持有者（断言辅助）。
func (f *hubFixture) lockOf(nodeID, wsID string) string {
	f.t.Helper()
	return f.st.View().WorkdirLocks[nodeID][wsID]
}

// attachNode 挂载受控 node 但不重注册能力表（重启场景卡已在快照中，
// RegisterNode 全量覆盖语义会清空它）。
func (f *hubFixture) attachNode(name string, autoAck bool) *testNode {
	f.t.Helper()
	n := &testNode{t: f.t, ep: f.bus.Endpoint(name), autoAck: autoAck}
	n.ep.Subscribe(n.handle)
	f.core.SetOnline(name, true)
	return n
}

// TestWorkdirMutexSerial 同 workdir 并发提交 → 串行（DoD 增补①的 hub 侧断言，
// 更强：第二任务在第一任务终态前不得被投递）。锁生命周期：offered 占用 →
// 终态释放 → 立即补推队首同 workdir 任务。
func TestWorkdirMutexSerial(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", []protocol.AgentCard{wsCard("w", "ws-1")}, false)

	ack1, err := f.core.Submit(submitReq("nodeB", "w", "first"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if got := f.taskOf(ack1.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("第一任务应 offered, got %s", got)
	}
	if holder := f.lockOf("nodeB", "ws-1"); holder != ack1.TaskID {
		t.Fatalf("offered 即应占锁, holder=%q", holder)
	}

	// 第二任务同 workdir：互斥中 → 滞留 submitted（信箱在队，不投递）
	ack2, err := f.core.Submit(submitReq("nodeB", "w", "second"), "")
	if err != nil {
		t.Fatalf("第二任务提交失败: %v", err)
	}
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("互斥中第二任务应滞留 submitted, got %s", got)
	}
	if n := len(node.offers); n != 1 {
		t.Fatalf("互斥中不得投递第二任务, offers=%d", n)
	}
	if n := f.countMailbox("nodeB"); n != 1 {
		t.Fatalf("第二任务应留在信箱, got %d", n)
	}

	// ack/started（running）仍互斥：锁直到终态才释放
	node.ack(ack1.TaskID)
	node.start(ack1.TaskID)
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("running 期间第二任务不得投递, got %s", got)
	}

	// 第一任务终态：释放锁 → 触发补推，第二任务转 offered 并接管锁
	node.result(ack1.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: f.clock.Now()})
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("终态释放锁后第二任务应 offered, got %s", got)
	}
	if holder := f.lockOf("nodeB", "ws-1"); holder != ack2.TaskID {
		t.Fatalf("第二任务应接管锁, holder=%q", holder)
	}
	if n := len(node.offers); n != 2 {
		t.Fatalf("应恰好投递 2 次, got %d", n)
	}

	// 第二任务终态：锁归零（可再派发）
	node.result(ack2.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0})
	if holder := f.lockOf("nodeB", "ws-1"); holder != "" {
		t.Fatalf("终态后锁应释放, holder=%q", holder)
	}
}

// TestFullChainWorkdirSerialTimestamps 全链时间戳断言（DoD 增补①）：真实执行链
// 上「第二任务 started ≥ 第一任务 finished」——同 workdir 两任务并发提交，第二
// 任务的执行开始时刻不得早于第一任务结果落盘回传。/bin/sleep 0.3 拉长第一任务
// 执行窗口，使并发提交确实撞上互斥（第二任务滞留 submitted 直至第一任务终态）。
func TestFullChainWorkdirSerialTimestamps(t *testing.T) {
	f := newHub(t)
	echo := echoProfile(t)
	echo.Name = "sleeper"
	echo.Command = "/bin/sleep"
	echo.AllowArgs = true
	card := protocol.AgentCard{
		Name: echo.Name, Kind: echo.Kind, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: echo.PermissionMode,
		ProtocolVer: 1, WorkspaceID: "ws-sleep", Exposed: true,
	}
	newExecNode(f, "nodeB", map[string]execRunner{
		"sleeper": runner.New(echo, baseTestEnv2(), f.t.TempDir()),
	}, []protocol.AgentCard{card})

	type submitOutcome struct {
		ack *SubmitAck
		err error
	}
	var (
		res1, res2 submitOutcome
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		a, e := f.core.Submit(submitReq("nodeB", "sleeper", "0.3"), "nodeB") // 本机派单（边界方案 v2）
		res1 = submitOutcome{ack: a, err: e}
	}()
	time.Sleep(50 * time.Millisecond) // 确保第二任务在第一任务执行中到达
	go func() {
		defer wg.Done()
		a, e := f.core.Submit(submitReq("nodeB", "sleeper", "1"), "")
		res2 = submitOutcome{ack: a, err: e}
	}()
	wg.Wait()
	if res1.err != nil || res2.err != nil {
		t.Fatalf("提交失败: %v / %v", res1.err, res2.err)
	}
	if res1.ack == nil || res2.ack == nil || res1.ack.TaskID == res2.ack.TaskID {
		t.Fatalf("提交结果异常: %+v %+v", res1.ack, res2.ack)
	}

	// 第二任务终态收敛（串行放行后完成）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.taskOf(res2.ack.TaskID).Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t1 := f.taskOf(res1.ack.TaskID)
	t2 := f.taskOf(res2.ack.TaskID)
	if t1.Status != protocol.StatusCompleted || t2.Status != protocol.StatusCompleted {
		t.Fatalf("两任务应 completed, got %s / %s", t1.Status, t2.Status)
	}
	if t1.Result == nil || t2.Result == nil {
		t.Fatalf("终态结果缺失: %+v / %+v", t1.Result, t2.Result)
	}
	if t2.Result.StartedAt.Before(t1.Result.FinishedAt) {
		t.Fatalf("串行断言失败：第二 started(%v) 不得早于第一 finished(%v)",
			t2.Result.StartedAt, t1.Result.FinishedAt)
	}
}

// TestWorkdirQueueBusyTimeout 排队超时（DoD 增补③）：互斥阻塞排队超
// WorkdirQueueWait → failed/error=workdir_busy_timeout；未超时不动；持锁任务
// 不受波及；与 expire 同过期时 expire 优先。
func TestWorkdirQueueBusyTimeout(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", []protocol.AgentCard{wsCard("w", "ws-1")}, false)

	ack1, _ := f.core.Submit(submitReq("nodeB", "w", "run"), "") // offered，占锁
	node.ack(ack1.TaskID)
	node.start(ack1.TaskID) // running：不触发 offer 超时回退，锁稳定持有
	ack2, _ := f.core.Submit(submitReq("nodeB", "w", "queued"), "")
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("前置失败：第二任务应排队, got %s", got)
	}

	// 半程：未超时不动
	f.clock.Advance(protocol.WorkdirQueueWait / 2)
	f.core.Sweep()
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("未超 WorkdirQueueWait 不应动作, got %s", got)
	}

	// 超时：failed/workdir_busy_timeout；进程未运行 ExitCode=-1；锁不被波及
	f.clock.Advance(protocol.WorkdirQueueWait/2 + f.second())
	f.core.Sweep()
	task := f.taskOf(ack2.TaskID)
	if task.Status != protocol.StatusFailed || task.Result == nil ||
		task.Result.Error != protocol.CodeWorkdirBusyTimeout {
		t.Fatalf("应 failed/workdir_busy_timeout, got %s %+v", task.Status, task.Result)
	}
	if task.Result.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("排队超时进程未运行, ExitCode=%d", task.Result.ExitCode)
	}
	if n := f.countMailbox("nodeB"); n != 0 {
		t.Errorf("超时应释放信箱, got %d", n)
	}
	// 按入箱原始大小核销（P1.5 二轮必修4）：终态任务体附 Result 后大于入箱时，
	// 按终态核销会使 Bytes 变负、放宽限额
	if mb := f.st.View().Mailboxes["nodeB"]; mb.Bytes != 0 {
		t.Errorf("超时核销后信箱字节应精确归零, got %d", mb.Bytes)
	}
	if got := f.taskOf(ack1.TaskID).Status; got != protocol.StatusRunning {
		t.Errorf("第一任务不受波及, got %s", got)
	}
	if holder := f.lockOf("nodeB", "ws-1"); holder != ack1.TaskID {
		t.Errorf("锁仍归第一任务, holder=%q", holder)
	}

	// 边界：expire 优先——重建互斥后灌入新排队任务并推到 7d，应为 expired
	// 而非 workdir_busy_timeout
	node.result(ack1.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0})
	ack3, _ := f.core.Submit(submitReq("nodeB", "w", "again"), "") // offered，占锁
	if got := f.taskOf(ack3.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("前置失败：第三任务应 offered, got %s", got)
	}
	ack4, _ := f.core.Submit(submitReq("nodeB", "w", "stuck"), "")
	if got := f.taskOf(ack4.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("前置失败：第四任务应排队, got %s", got)
	}
	f.clock.Advance(protocol.TaskRetention + time.Minute)
	f.core.Sweep()
	task4 := f.taskOf(ack4.TaskID)
	if task4.Status != protocol.StatusFailed || task4.Result == nil || task4.Result.Error != protocol.CodeExpired {
		t.Fatalf("同过期应 expire 优先, got %s %+v", task4.Status, task4.Result)
	}
}

// TestHeadOfLineWorkdirSkip head-of-line 修复（DoD 增补④）：队首任务互斥中，
// 同 node 异 workdir 任务仍可派发；队首任务在锁释放后恢复投递（保持 FIFO 公平）。
func TestHeadOfLineWorkdirSkip(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", []protocol.AgentCard{wsCard("a", "ws-a"), wsCard("b", "ws-b")}, false)

	ack1, _ := f.core.Submit(submitReq("nodeB", "a", "head"), "")  // offered，占 ws-a
	ack2, _ := f.core.Submit(submitReq("nodeB", "a", "queue"), "") // 队首互斥中
	ack3, _ := f.core.Submit(submitReq("nodeB", "b", "other"), "") // 异 workdir，应可派发

	if got := f.taskOf(ack3.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("队首互斥中异 workdir 任务应仍可派发, got %s", got)
	}
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("互斥任务应保留在队, got %s", got)
	}
	if len(node.offers) != 2 || node.offers[0].ID != ack1.TaskID || node.offers[1].ID != ack3.TaskID {
		t.Fatalf("投递顺序应为 [t1 t3], got %v", offerIDs(node.offers))
	}
	if holder := f.lockOf("nodeB", "ws-b"); holder != ack3.TaskID {
		t.Fatalf("ws-b 应由 t3 占锁, holder=%q", holder)
	}

	// ws-a 释放后队首任务恢复投递
	node.result(ack1.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0})
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("锁释放后队首任务应恢复投递, got %s", got)
	}
	if n := len(node.offers); n != 3 {
		t.Fatalf("应累计投递 3 次, got %d", n)
	}
}

// TestWorkdirFallbackSelfLockReoffer 回退任务自锁重投（P1.5 二轮必修1）：offer
// 超时回退 submitted 时锁随回退保留（自锁）——eligible 门控必须接受「持有者=
// 本任务」允许重投，否则首次 offer 丢失即卡死到过期。锁持有权随重投幂等延续。
func TestWorkdirFallbackSelfLockReoffer(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", []protocol.AgentCard{wsCard("w", "ws-1")}, false) // 不 ack

	ack, err := f.core.Submit(submitReq("nodeB", "w", "lost-offer"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("前置失败：应 offered, got %s", got)
	}
	if n := len(node.offers); n != 1 {
		t.Fatalf("前置失败：应已投递 1 次, got %d", n)
	}

	// offer 丢失（无 ack）超时 → 回退 submitted，锁随回退保留（自锁）
	f.core.SetOnline("nodeB", false) // 下线阻断补推，先观察回退中间态
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("回退后应 submitted, got %s", got)
	}
	if holder := f.lockOf("nodeB", "ws-1"); holder != ack.TaskID {
		t.Fatalf("回退应保留自锁, holder=%q", holder)
	}
	if n := f.countMailbox("nodeB"); n != 1 {
		t.Fatalf("回退任务应回队首, got %d", n)
	}

	// 自锁不得阻塞重投：重新上线触发补推 → 立即重投 offered（持有权延续）
	f.core.SetOnline("nodeB", true)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("自锁任务应允许重投, got %s", got)
	}
	if n := len(node.offers); n != 2 {
		t.Fatalf("应重投第 2 次, got %d", n)
	}
	if holder := f.lockOf("nodeB", "ws-1"); holder != ack.TaskID {
		t.Fatalf("重投后持有权应延续, holder=%q", holder)
	}
}

// TestHubRestartWorkdirLockRecovery hub 重启互斥表恢复（DoD 增补⑤ hub 侧）：
// 快照携带互斥表，重开后门控继续生效；终态后释放恢复派发。
func TestHubRestartWorkdirLockRecovery(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	clock := newFakeClock()

	// 第一代：任务 offered（持锁）后 hub 关闭
	st1, err := storeOpen(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	core1, err := New(Config{Store: st1, Transport: bus.Endpoint("hub"), Now: clock.Now})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	if err := core1.RegisterNode("nodeB", []protocol.AgentCard{wsCard("w", "ws-1")}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	// 第一代先挂哑 node（不 ack）：offer 可达，任务停在 offered 持锁
	dummy := &testNode{t: t, ep: bus.Endpoint("nodeB"), autoAck: false}
	dummy.ep.Subscribe(dummy.handle)
	core1.SetOnline("nodeB", true)
	ack, err := core1.Submit(submitReq("nodeB", "w", "lock-holder"), "")
	if err != nil {
		t.Fatalf("提交: %v", err)
	}
	if got, _ := core1.GetTask(ack.TaskID); got.Status != protocol.StatusOffered {
		t.Fatalf("前置失败：应 offered, got %s", got.Status)
	}
	_ = core1.Close()
	_ = st1.Close()

	// 第二代：同一 data_dir 重开——互斥表随快照恢复
	st2, err := storeOpen(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	defer func() { _ = st2.Close() }()
	core2, err := New(Config{Store: st2, Transport: bus.Endpoint("hub"), Now: clock.Now})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	defer func() { _ = core2.Close() }()
	if holder := st2.View().WorkdirLocks["nodeB"]["ws-1"]; holder != ack.TaskID {
		t.Fatalf("快照应恢复互斥锁, holder=%q want %q", holder, ack.TaskID)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st2, core: core2, clock: clock}
	f.hubEP.Subscribe(core2.HandleFrame)

	// 恢复后的门控继续生效：node 先上线再提交（排除假阳性——离线同样滞留
	// submitted，不能作为互斥门控的证据），同 workdir 新任务应排队且不投递
	nodeB := f.attachNode("nodeB", false) // 能力表已在快照，不重注册
	ack2, err := core2.Submit(submitReq("nodeB", "w", "after-restart"), "")
	if err != nil {
		t.Fatalf("重启后提交: %v", err)
	}
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("重启后互斥应继续阻塞, got %s", got)
	}
	if n := len(nodeB.offers); n != 0 {
		t.Fatalf("互斥中新任务不得投递, offers=%d", n)
	}

	// 原任务终态（重启前已 offered，node 照常 ack/started/result）→ 释放并补推
	nodeB.ack(ack.TaskID)
	nodeB.start(ack.TaskID)
	nodeB.result(ack.TaskID, protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0})
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("重启后锁释放应恢复派发, got %s", got)
	}
	if n := len(nodeB.offers); n != 1 {
		t.Fatalf("锁释放后应补推 1 次, got %d", n)
	}
}

// TestHubRestartLockRebuildFromTasks 互斥表自愈：快照缺表（旧版本数据）时按
// 任务集补占 offered/acknowledged/running 的锁；submitted 不占（占用从 offered
// 起算）。
func TestHubRestartLockRebuildFromTasks(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	clock := newFakeClock()

	st1, err := storeOpen(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	core1, err := New(Config{Store: st1, Transport: bus.Endpoint("hub"), Now: clock.Now})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	if err := core1.RegisterNode("nodeB", []protocol.AgentCard{wsCard("w", "ws-1")}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	dummy := &testNode{t: t, ep: bus.Endpoint("nodeB"), autoAck: false} // offer 可达
	dummy.ep.Subscribe(dummy.handle)
	core1.SetOnline("nodeB", true)
	offered, _ := core1.Submit(submitReq("nodeB", "w", "will-offer"), "")
	core1.SetOnline("nodeB", false)
	queued, _ := core1.Submit(submitReq("nodeB", "w", "still-queued"), "")
	if got, _ := core1.GetTask(offered.TaskID); got.Status != protocol.StatusOffered {
		t.Fatalf("前置失败：应 offered, got %s", got.Status)
	}
	_ = core1.Close()
	_ = st1.Close()

	// 手术：从快照剥离 workdir_locks（模拟旧版本快照）
	if err := stripWorkdirLocks(dir); err != nil {
		t.Fatalf("剥离快照互斥表: %v", err)
	}

	st2, err := storeOpen(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	defer func() { _ = st2.Close() }()
	core2, err := New(Config{Store: st2, Transport: bus.Endpoint("hub"), Now: clock.Now})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	defer func() { _ = core2.Close() }()

	// 补占：offered 任务恢复持锁；queued（submitted）不占
	if holder := st2.View().WorkdirLocks["nodeB"]["ws-1"]; holder != offered.TaskID {
		t.Fatalf("应按任务集补占锁, holder=%q want %q", holder, offered.TaskID)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st2, core: core2, clock: clock}
	f.hubEP.Subscribe(core2.HandleFrame)
	if got := f.taskOf(queued.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("前置失败：排队任务应滞留, got %s", got)
	}
}

// stripWorkdirLocks 从快照 JSON 剥离 workdir_locks 键（模拟旧版本快照）。
func stripWorkdirLocks(dir string) error {
	path := filepath.Join(dir, "hub_state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	delete(raw, "workdir_locks")
	nb, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, nb, 0o644)
}

// offerIDs 投递顺序断言辅助。
func offerIDs(offers []protocol.Task) []string {
	ids := make([]string, 0, len(offers))
	for _, t := range offers {
		ids = append(ids, t.ID)
	}
	return ids
}
