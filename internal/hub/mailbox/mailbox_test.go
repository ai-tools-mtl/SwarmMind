package mailbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// newTask 构造可入队的 submitted 任务（id 唯一由调用方保证）。
func newTask(id string, stdin string) *protocol.Task {
	return &protocol.Task{
		ID:          id,
		TraceID:     "trace-" + id,
		TargetNode:  "n1",
		TargetAgent: "echo-demo",
		Input:       protocol.TaskInput{Stdin: stdin},
		SubmittedBy: "hana",
		Status:      protocol.StatusSubmitted,
		CreatedAt:   protocol.NowUTC(),
		ExpireAt:    protocol.NowUTC().Add(protocol.TaskRetention),
	}
}

// mustEnqueue 按真实时序登记任务体（store.Tasks）并入箱；失败即测试致命。
func mustEnqueue(t *testing.T, st *store.State, task *protocol.Task) {
	t.Helper()
	st.Tasks[task.ID] = *task
	if err := Enqueue(st, task.TargetNode, task); err != nil {
		t.Fatalf("入队 %s 失败: %v", task.ID, err)
	}
}

func newState() *store.State {
	return store.NewState()
}

// TestEnqueueQuota 条数配额 100：第 101 条拒绝且信箱不变（SPEC 4.6）。
func TestEnqueueQuota(t *testing.T) {
	st := newState()
	for i := 0; i < protocol.MailboxQuota; i++ {
		mustEnqueue(t, st, newTask("t-"+string(rune('a'+i%26))+string(rune('0'+i/26)), ""))
	}
	if err := Enqueue(st, "n1", newTask("t-overflow", "")); !errors.Is(err, ErrMailboxFull) {
		t.Errorf("第 101 条应 ErrMailboxFull, got %v", err)
	}
	count, _ := Usage(st, "n1")
	if count != protocol.MailboxQuota {
		t.Errorf("配额满后条数应为 %d, got %d", protocol.MailboxQuota, count)
	}
}

// TestEnqueueByteBudget 字节预算 8MiB：超预算拒绝（SPEC 4.6）。
// 单条任务体 ~900KiB（合法提交上限内），9 条 ~7.9MiB 仍在预算内，第 10 条必超。
func TestEnqueueByteBudget(t *testing.T) {
	st := newState()
	big := strings.Repeat("x", 900<<10)
	for i := 0; i < 9; i++ {
		mustEnqueue(t, st, newTask("t-big-"+string(rune('a'+i)), big))
	}
	_, bytes := Usage(st, "n1")
	if bytes > protocol.MailboxByteBudget {
		t.Fatalf("前置失败：前 9 条已超预算 bytes=%d", bytes)
	}
	if err := Enqueue(st, "n1", newTask("t-over-budget", big)); !errors.Is(err, ErrMailboxFull) {
		t.Errorf("超字节预算应 ErrMailboxFull, got %v", err)
	}
	_, bytes = Usage(st, "n1")
	if bytes > protocol.MailboxByteBudget {
		t.Errorf("预算执行后字节和 %d 超过预算 %d", bytes, int64(protocol.MailboxByteBudget))
	}
}

// TestDequeueReleases 出队转 offered 即释放配额：满信箱出队一条后可再入队一条。
func TestDequeueReleases(t *testing.T) {
	st := newState()
	first := newTask("t-first", "")
	mustEnqueue(t, st, first)
	for i := 0; i < protocol.MailboxQuota-1; i++ {
		mustEnqueue(t, st, newTask("t-"+string(rune('a'+i%26))+string(rune('0'+i/26)), ""))
	}
	if err := Enqueue(st, "n1", newTask("t-overflow", "")); !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("前置失败：信箱应已满, got %v", err)
	}

	got, ok := Dequeue(st, "n1")
	if !ok || got.ID != first.ID {
		t.Fatalf("出队应为队首 %s, got %v ok=%v", first.ID, got.ID, ok)
	}
	if err := Enqueue(st, "n1", newTask("t-overflow", "")); err != nil {
		t.Errorf("出队释放配额后入队失败: %v", err)
	}
	count, _ := Usage(st, "n1")
	if count != protocol.MailboxQuota {
		t.Errorf("释放后再入队条数应回到 %d, got %d", protocol.MailboxQuota, count)
	}
}

// TestDequeueSkipsStale 队列中非 submitted 陈旧条目被防御性跳过。
func TestDequeueSkipsStale(t *testing.T) {
	st := newState()
	stale := newTask("t-stale", "")
	mustEnqueue(t, st, stale)
	second := newTask("t-second", "")
	mustEnqueue(t, st, second)
	// 模拟陈旧：队首任务已被置为 offered（正常路径会先出队）
	tk := st.Tasks["t-stale"]
	tk.Status = protocol.StatusOffered
	st.Tasks["t-stale"] = tk

	got, ok := Dequeue(st, "n1")
	if !ok || got.ID != second.ID {
		t.Errorf("应跳过陈旧条目出队 %s, got %v ok=%v", second.ID, got.ID, ok)
	}
}

// TestRequeueFrontOverQuota offered 超时回退：配额已满仍回队（重投预留，短暂超额）。
func TestRequeueFrontOverQuota(t *testing.T) {
	st := newState()
	back := newTask("t-back", "")
	mustEnqueue(t, st, back)
	for i := 0; i < protocol.MailboxQuota-1; i++ {
		mustEnqueue(t, st, newTask("t-"+string(rune('a'+i%26))+string(rune('0'+i/26)), ""))
	}
	if _, ok := Dequeue(st, "n1"); !ok { // 出队转 offered（配额释放）
		t.Fatal("出队失败")
	}
	// 重新填满配额
	mustEnqueue(t, st, newTask("t-overflow", ""))

	RequeueFront(st, "n1", back) // 配额满仍回队
	count, _ := Usage(st, "n1")
	if count != protocol.MailboxQuota+1 {
		t.Errorf("回队应短暂超额为 %d, got %d", protocol.MailboxQuota+1, count)
	}
	head := st.Mailboxes["n1"].Tasks[0]
	if head != back.ID {
		t.Errorf("回队应插队首, got %s", head)
	}
}

// TestRemove 移除回补配额与字节计量。
func TestRemove(t *testing.T) {
	st := newState()
	a := newTask("t-a", strings.Repeat("x", 1000))
	b := newTask("t-b", "")
	mustEnqueue(t, st, a)
	mustEnqueue(t, st, b)
	if !Remove(st, "n1", "t-a") {
		t.Fatal("Remove 应命中")
	}
	count, bytes := Usage(st, "n1")
	if count != 1 || bytes != TaskBytes(b) {
		t.Errorf("移除后 count=%d bytes=%d, want 1/%d", count, bytes, TaskBytes(b))
	}
	if Remove(st, "n1", "t-a") {
		t.Error("重复移除应 no-op")
	}
}

// TestRemoveSettlesEnqueueSize 移除按入箱原始大小核销（P1.5 二轮必修4）：任务体
// 随状态迁移变化（置终态附 Result 后大于入箱时）——按当前状态核销会使 Bytes 变
// 负、放宽限额；核销后必须精确归零。
func TestRemoveSettlesEnqueueSize(t *testing.T) {
	st := newState()
	tk := newTask("t-drift", "")
	mustEnqueue(t, st, tk)
	if _, bytes := Usage(st, "n1"); bytes != TaskBytes(tk) {
		t.Fatalf("前置失败：入箱计量 %d != 实测 %d", bytes, TaskBytes(tk))
	}
	// 模拟 submitted 直达终态：状态变更 + Result 附加（任务体显著变大）
	drifted := st.Tasks["t-drift"]
	drifted.Status = protocol.StatusFailed
	drifted.Result = &protocol.TaskResult{
		Status: protocol.ResultFailed, ExitCode: 1, Error: "boom",
		FinalAnswer: strings.Repeat("x", 512), FinishedAt: protocol.NowUTC(),
	}
	st.Tasks["t-drift"] = drifted
	if TaskBytes(&drifted) <= TaskBytes(tk) {
		t.Fatalf("前置失败：终态任务体应大于入箱时 (%d <= %d)", TaskBytes(&drifted), TaskBytes(tk))
	}

	if !Remove(st, "n1", "t-drift") {
		t.Fatal("Remove 应命中")
	}
	count, bytes := Usage(st, "n1")
	if count != 0 || bytes != 0 {
		t.Errorf("应按入箱原始大小核销归零, got count=%d bytes=%d", count, bytes)
	}
	if bytes < 0 {
		t.Errorf("Bytes 不得变负（放宽限额）, got %d", bytes)
	}
}

// TestSettleRoundtripBytes 出队→回退重队→移除全周期计量守恒：核销一律按入箱
// 记录，跨状态迁移（offered↔submitted、终态附加 Result）不漂移。
func TestSettleRoundtripBytes(t *testing.T) {
	st := newState()
	tk := newTask("t-roundtrip", strings.Repeat("y", 300))
	mustEnqueue(t, st, tk)
	_, size0 := Usage(st, "n1")

	got, ok := Dequeue(st, "n1") // 出队转 offered：核销入箱大小，记录保留
	if !ok || got.ID != tk.ID {
		t.Fatalf("出队失败: ok=%v", ok)
	}
	if _, bytes := Usage(st, "n1"); bytes != 0 {
		t.Errorf("出队后应归零, got %d", bytes)
	}
	// 回退：任务已转 submitted（回退态），照原记录恢复计量
	back := st.Tasks[tk.ID]
	RequeueFront(st, "n1", &back)
	if _, bytes := Usage(st, "n1"); bytes != size0 {
		t.Errorf("回队应按原记录恢复 %d, got %d", size0, bytes)
	}
	// 直达终态移除：仍按原记录核销，不变负
	drifted := st.Tasks[tk.ID]
	drifted.Status = protocol.StatusFailed
	drifted.Result = &protocol.TaskResult{Status: protocol.ResultFailed, Error: "x"}
	st.Tasks[tk.ID] = drifted
	if !Remove(st, "n1", tk.ID) {
		t.Fatal("Remove 应命中")
	}
	if _, bytes := Usage(st, "n1"); bytes != 0 {
		t.Errorf("全周期核销后应归零, got %d", bytes)
	}
}

// TestIdemCheck 三分支：无键放行、同键同内容命中、同键不同内容冲突。
func TestIdemCheck(t *testing.T) {
	st := newState()
	req := protocol.TaskSubmitRequest{
		TargetNode:  "n1",
		TargetAgent: "echo-demo",
		Input:       protocol.TaskInput{Args: []string{"hello"}, Stdin: "s"},
		SubmittedBy: "hana",
		IdemKey:     "demo-001",
	}
	orig := newTask("t-orig", "")
	orig.IdemKey = req.IdemKey
	orig.TargetNode = req.TargetNode
	orig.TargetAgent = req.TargetAgent
	orig.SubmittedBy = req.SubmittedBy
	orig.Input = req.Input.Clone()
	st.Tasks[orig.ID] = *orig

	// 无键：放行不做查重
	if _, dup, err := IdemCheck(st, protocol.TaskSubmitRequest{TargetNode: "x"}); dup || err != nil {
		t.Errorf("无键应放行, got dup=%v err=%v", dup, err)
	}
	// 同键同内容：命中原任务
	id, dup, err := IdemCheck(st, req)
	if err != nil || !dup || id != orig.ID {
		t.Errorf("同键同内容应命中 %s, got id=%s dup=%v err=%v", orig.ID, id, dup, err)
	}
	// 同键不同内容：冲突
	req.Input.Args = []string{"changed"}
	if _, dup, err := IdemCheck(st, req); !errors.Is(err, ErrConflict) {
		t.Errorf("同键不同内容应 ErrConflict, got dup=%v err=%v", dup, err)
	}
	// 键不存在
	req.IdemKey = "demo-002"
	if id, dup, err := IdemCheck(st, req); id != "" || dup || err != nil {
		t.Errorf("未命中应放行, got id=%s dup=%v err=%v", id, dup, err)
	}
}

// TestContentHashStability 指纹确定性：同内容同指纹、Env map 键序无关。
func TestContentHashStability(t *testing.T) {
	a := protocol.TaskSubmitRequest{
		TargetNode: "n1", TargetAgent: "a",
		Input:       protocol.TaskInput{Env: map[string]string{"K1": "v1", "K2": "v2"}, Args: []string{"x"}},
		SubmittedBy: "u",
	}
	b := protocol.TaskSubmitRequest{
		SubmittedBy: "u",
		TargetAgent: "a", TargetNode: "n1",
		Input: protocol.TaskInput{Args: []string{"x"}, Env: map[string]string{"K2": "v2", "K1": "v1"}},
	}
	if ContentHash(a) != ContentHash(b) {
		t.Error("同内容（字段序/map 键序不同）指纹应一致")
	}
	c := a
	c.Input.Args = []string{"y"}
	if ContentHash(a) == ContentHash(c) {
		t.Error("不同内容指纹应不同")
	}
}
