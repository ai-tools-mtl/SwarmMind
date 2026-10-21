package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// batchTargetsOf 从批次视图提取目标序表（断言辅助）。
func batchTargetsOf(items []BatchItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Target
	}
	return out
}

// TestControlBatchIdempotency 批次幂等三分支（修复项 2）：同键同内容返回原
// batch_id + dup（不重提交任何子任务）；同键不同内容 ErrBatchConflict；无键
// 每次新建批次。
func TestControlBatchIdempotency(t *testing.T) {
	f := newHub(t)
	f.newNode("nodeB", agentsOf("echo"), true)
	f.newNode("nodeC", agentsOf("echo"), true)
	f.registerOffline("nodeD", agentsOf("echo"))

	input := protocol.TaskInput{Args: []string{"patrol"}}
	batch, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC", "nodeD"}, input, "K-1", "admin", "")
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if batch.Dup {
		t.Error("首次创建不应为 dup")
	}
	if batch.Status != BatchStatusComplete {
		t.Errorf("全项已决后应 complete, got %s", batch.Status)
	}
	if got := len(f.st.View().Tasks); got != 2 {
		t.Fatalf("应提交 2 个子任务, got %d", got)
	}

	// 同键同内容：原 batch_id + dup=true，不新增任务/批次
	retry, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC", "nodeD"}, input, "K-1", "admin", "")
	if err != nil {
		t.Fatalf("幂等重试失败: %v", err)
	}
	if !retry.Dup || retry.BatchID != batch.BatchID {
		t.Errorf("幂等命中应返回原 batch_id + dup, got %+v", retry)
	}
	if n := len(f.st.View().Tasks); n != 2 {
		t.Errorf("幂等重试不得重复提交任务, got %d", n)
	}
	if n := len(f.st.View().Batches); n != 1 {
		t.Errorf("幂等重试不得新建批次, got %d", n)
	}

	// 同键不同内容（换目标序）：冲突
	if _, err := f.core.CreateControlBatch("echo", []string{"nodeC", "nodeB", "nodeD"}, input, "K-1", "admin", ""); !errors.Is(err, ErrBatchConflict) {
		t.Errorf("同键不同内容应 ErrBatchConflict, got %v", err)
	}
	// 同键不同内容（换 input）：冲突
	if _, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC", "nodeD"}, protocol.TaskInput{Args: []string{"CHANGED"}}, "K-1", "admin", ""); !errors.Is(err, ErrBatchConflict) {
		t.Errorf("同键不同 input 应 ErrBatchConflict, got %v", err)
	}
	// 同键不同内容（换 group_ref）：冲突（GROUP-RFC：群引用参与批次内容指纹）
	if _, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC", "nodeD"}, input, "K-1", "admin", "g-other"); !errors.Is(err, ErrBatchConflict) {
		t.Errorf("同键不同 group_ref 应 ErrBatchConflict, got %v", err)
	}

	// 无 idem_key：行为不变，每次新批次
	plain, err := f.core.CreateControlBatch("echo", []string{"nodeB"}, input, "", "admin", "")
	if err != nil {
		t.Fatalf("无键创建失败: %v", err)
	}
	if plain.Dup || plain.BatchID == batch.BatchID {
		t.Errorf("无键应新建批次: %+v", plain)
	}
	// 幂等映射只随显式 idem_key 写入
	if len(f.st.View().BatchIdems) != 1 || f.st.View().BatchIdems["K-1"] != batch.BatchID {
		t.Errorf("BatchIdems 映射不符: %+v", f.st.View().BatchIdems)
	}
}

// TestControlBatchConvergedIdem 并发同键由骨架事务重检收敛：并发预检都未命中
// 时（模拟：绕过快读路径直入事务），后到者收敛为 dup/冲突，不得双写映射。
func TestControlBatchConvergedIdem(t *testing.T) {
	f := newHub(t)
	f.newNode("nodeB", agentsOf("echo"), true)

	input := protocol.TaskInput{Args: []string{"patrol"}}
	b1, _, err := f.core.createBatchSkeleton("echo", []string{"nodeB"}, input, "K", "admin", "",
		batchContentHash("echo", []string{"nodeB"}, input, ""),
		[]admissionDecision{admitSubmit})
	if err != nil {
		t.Fatalf("骨架 1 失败: %v", err)
	}
	// 同键同内容：收敛为命中 b1
	id, dup, err := f.core.createBatchSkeleton("echo", []string{"nodeB"}, input, "K", "admin", "",
		batchContentHash("echo", []string{"nodeB"}, input, ""),
		[]admissionDecision{admitSubmit})
	if err != nil || !dup || id != b1 {
		t.Errorf("事务重检应收敛为 dup(id=%s), got id=%s dup=%v err=%v", b1, id, dup, err)
	}
	// 同键不同内容：事务重检冲突
	if _, _, err := f.core.createBatchSkeleton("echo", []string{"nodeB"}, protocol.TaskInput{Args: []string{"X"}}, "K", "admin", "",
		batchContentHash("echo", []string{"nodeB"}, protocol.TaskInput{Args: []string{"X"}}, ""),
		[]admissionDecision{admitSubmit}); !errors.Is(err, ErrBatchConflict) {
		t.Errorf("事务重检应冲突, got %v", err)
	}
	// 映射唯一且指向原批次
	v := f.st.View()
	if len(v.BatchIdems) != 1 || v.BatchIdems["K"] != b1 {
		t.Errorf("并发收敛后映射应唯一: %+v", v.BatchIdems)
	}
	if len(v.Batches) != 1 {
		t.Errorf("并发同键不得新建批次: %d", len(v.Batches))
	}
}

// TestControlBatchChildIdemKeys 子任务派生幂等键（修复项 2）：子任务 IdemKey
// = batch_id+序号，与条目下标一一对应；跳过项无任务。
func TestControlBatchChildIdemKeys(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)
	f.registerOffline("nodeC", agentsOf("echo"))

	batch, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC"}, protocol.TaskInput{Args: []string{"p"}}, "K", "admin", "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if got := len(node.offers); got != 1 {
		t.Fatalf("应向 nodeB 投递 1 个 offer, got %d", got)
	}
	wantKey := batchChildIdem(batch.BatchID, 0)
	if node.offers[0].IdemKey != wantKey {
		t.Errorf("子任务幂等键应 %s, got %q", wantKey, node.offers[0].IdemKey)
	}
	// 提交方身份来自批次记录（控制台 admin）
	if node.offers[0].SubmittedBy != "admin" {
		t.Errorf("批次子任务 submitted_by 应 admin, got %q", node.offers[0].SubmittedBy)
	}
}

// TestControlBatchItemError 逐项 error 场景（修复项 2/补测试）：体积超限目标
// 逐项记 error 不阻塞批次；批次照常 complete；无任务落库。
func TestControlBatchItemError(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)
	f.registerOffline("nodeC", agentsOf("echo"))

	// stdin 超 1MiB 信封上限 → 所有可提交目标 frame_too_large
	big := strings.Repeat("x", protocol.MaxMessageBytes+1)
	batch, err := f.core.CreateControlBatch("echo", []string{"nodeB", "nodeC"}, protocol.TaskInput{Stdin: big}, "", "admin", "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if batch.Status != BatchStatusComplete {
		t.Errorf("逐项错误不阻塞批次，应 complete, got %s", batch.Status)
	}
	if batch.Items[0].TaskID != "" || batch.Items[0].Error != protocol.CodeFrameTooLarge {
		t.Errorf("nodeB 应记 error=%s: %+v", protocol.CodeFrameTooLarge, batch.Items[0])
	}
	if batch.Items[1].Skipped != SkipOffline {
		t.Errorf("nodeC 应 skip offline: %+v", batch.Items[1])
	}
	if n := len(f.st.View().Tasks); n != 0 {
		t.Errorf("超限任务不得落库, got %d", n)
	}
	if len(node.offers) != 0 {
		t.Errorf("超限目标不应收到 offer")
	}
	// 任务视图逐项状态合并：error 项无状态
	if batch.Items[0].Status != "" {
		t.Errorf("error 项不应有任务状态: %+v", batch.Items[0])
	}
}

// TestControlBatchFreezeAdmission 冻结准入（建议修）：统一时点快照判定
// skip，与请求序一致；未注册目标归 offline。
func TestControlBatchFreezeAdmission(t *testing.T) {
	f := newHub(t)
	f.newNode("nodeB", agentsOf("echo"), true)
	f.newNode("nodeC", agentsOf("echo"), true)
	f.registerOffline("nodeD", agentsOf("echo", "secret-"))
	f.core.RegisterNode("nodeE", []protocol.AgentCard{}) // 在线但能力表为空
	f.core.SetOnline("nodeE", true)

	view := f.st.View()
	got := f.core.freezeAdmission(view, "echo", []string{"nodeB", "nodeC", "nodeD", "nodeE", "ghost"})
	want := []admissionDecision{admitSubmit, admitSubmit, admitOffline, admitNotExposed, admitOffline}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("decisions[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// blockingNode 受控 node 桩：收到 offer 即通知测试并阻塞，模拟 node 失联
//（dispatch 的 Send 永不返回），制造「任务已落库、批次条目未回写」的真实中断。
type blockingNode struct {
	ep      *transport.Endpoint
	entered chan struct{} // offer 已到达（任务已发布）
	release chan struct{}
}

func newBlockingNode(f *hubFixture, name string) *blockingNode {
	f.t.Helper()
	if err := f.core.RegisterNode(name, agentsOf("echo")); err != nil {
		f.t.Fatalf("注册 %s 失败: %v", name, err)
	}
	bn := &blockingNode{entered: make(chan struct{}, 8), release: make(chan struct{})}
	ep := f.bus.Endpoint(name)
	ep.Subscribe(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgTaskOffer {
			bn.entered <- struct{}{}
			<-bn.release // 阻塞 dispatch：CreateControlBatch 停在逐项 Submit 中
		}
	})
	f.core.SetOnline(name, true)
	return bn
}

// TestControlBatchCrashRecovery 崩溃恢复（修复项 3）：创建中真实中断（阻塞
// dispatch 模拟失联，条目 0 已落库未回写、条目 1 未及创建）→ 关闭重开 store +
// 新 Core（= 重启）→ 恢复补记已完成项、未创建项标 interrupted；重启后查询
// 批次可见两类项；原创建者事后收尾在已关闭 store 上失败、批次保持 creating。
func TestControlBatchCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	core1, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core1 失败: %v", err)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: core1, clock: clock}
	blocked := newBlockingNode(f, "nodeB")
	f.newNode("nodeC", agentsOf("echo"), true) // 正常桩：但本场景走不到（前项中断）

	errCh := make(chan error, 1)
	var batch *BatchView
	go func() {
		b, err := core1.CreateControlBatch("echo", []string{"nodeB", "nodeC"},
			protocol.TaskInput{Args: []string{"patrol"}}, "K-CRASH", "admin", "")
		batch = b
		errCh <- err
	}()

	// 等待中断点：条目 0 的任务已发布、offer 已到达 nodeB（CreateControlBatch
	// 阻塞在 Submit 的 dispatch 内，条目回写尚未发生）
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未到达中断点（offer 未到达阻塞 node）")
	}

	view := st.View()
	if len(view.Tasks) != 1 {
		t.Fatalf("中断点应有且仅有 1 个已落库任务, got %d", len(view.Tasks))
	}
	var batchID string
	for id := range view.Batches {
		batchID = id
	}
	if batchID == "" || view.Batches[batchID].Status != BatchStatusCreating {
		t.Fatalf("中断点批次应存在且 creating: %+v", view.Batches)
	}
	if it := view.Batches[batchID].Items[0]; it.TaskID != "" {
		t.Errorf("条目 0 应处于「已落库未回写」窗口, got %+v", it)
	}
	if it := view.Batches[batchID].Items[1]; it.TaskID != "" || it.Skipped != "" {
		t.Errorf("条目 1 应空白（未及创建）, got %+v", it)
	}

	// 「崩溃」：关闭旧 store，重开 + 新 Core（启动恢复）
	if err := core1.Close(); err != nil {
		t.Fatalf("core1 关闭失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store 关闭失败: %v", err)
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	core2, err := New(Config{Store: st2, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core2 失败: %v", err)
	}
	t.Cleanup(func() { _ = core2.Close() })

	// 重启后查询批次：已完成项补记（按派生幂等键），未创建项标 interrupted
	got, ok := core2.GetControlBatch(batchID)
	if !ok {
		t.Fatal("重启后批次不可见")
	}
	orphan := got.Items[0]
	if orphan.TaskID == "" || orphan.Skipped != "" {
		t.Errorf("已完成项应补记 task_id: %+v", orphan)
	}
	if orphan.Status != string(protocol.StatusOffered) {
		t.Errorf("补记项应合并任务当前状态 offered, got %q", orphan.Status)
	}
	// 补记的是崩溃窗口内已落库的那个任务（非新建）
	crashed := st2.View().Tasks[orphan.TaskID]
	if crashed.ID != orphan.TaskID || crashed.IdemKey != batchChildIdem(batchID, 0) {
		t.Errorf("补记任务应持派生幂等键 %s: %+v", batchChildIdem(batchID, 0), crashed)
	}
	if got.Items[1].TaskID != "" || got.Items[1].Skipped != SkipInterrupted {
		t.Errorf("未创建项应标 interrupted: %+v", got.Items[1])
	}
	if n := len(st2.View().Tasks); n != 1 {
		t.Errorf("恢复不得新建任务, got %d", n)
	}
	// 幂等键恢复语义：同派生键重复创建命中原任务，不重复执行
	ack, err := core2.Submit(protocol.TaskSubmitRequest{
		TargetNode: "nodeB", TargetAgent: "echo",
		Input: protocol.TaskInput{Args: []string{"patrol"}}, SubmittedBy: "admin",
		IdemKey: batchChildIdem(batchID, 0),
	}, "")
	if err != nil || !ack.Dup || ack.TaskID != orphan.TaskID {
		t.Errorf("派生键重创建应幂等命中原任务: ack=%+v err=%v", ack, err)
	}
	if n := len(st2.View().Tasks); n != 1 {
		t.Errorf("派生键命中不得新建任务, got %d", n)
	}

	// 释放阻塞：原创建者对已关闭 store 收尾必然失败，批次停留 creating
	close(blocked.release)
	if err := <-errCh; err == nil {
		t.Fatal("创建者在已关闭 store 上收尾应报错")
	}
	if batch != nil {
		t.Errorf("失败创建不应返回批次视图: %+v", batch)
	}
	if b := st2.View().Batches[batchID]; b.Status != BatchStatusCreating {
		t.Errorf("中断批次应保持 creating, got %s", b.Status)
	}

	// 再次重启：已决条目原样保留，恢复幂等（不重复标记/不改写）
	if err := core2.Close(); err != nil {
		t.Fatalf("core2 关闭失败: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("st2 关闭失败: %v", err)
	}
	st3, err := store.Open(dir)
	if err != nil {
		t.Fatalf("三次打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st3.Close() })
	core3, err := New(Config{Store: st3, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core3 失败: %v", err)
	}
	t.Cleanup(func() { _ = core3.Close() })
	again, ok := core3.GetControlBatch(batchID)
	if !ok {
		t.Fatal("三次重启后批次不可见")
	}
	if again.Items[0].TaskID != orphan.TaskID || again.Items[1].Skipped != SkipInterrupted {
		t.Errorf("恢复应幂等: %+v", again.Items)
	}
	if got := st3.View().Tasks[orphan.TaskID].IdemKey; got != batchChildIdem(batchID, 0) {
		t.Errorf("恢复后任务幂等键不变: %q", got)
	}
}
