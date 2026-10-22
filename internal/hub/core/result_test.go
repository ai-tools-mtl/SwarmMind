package core

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

func completeRes() protocol.TaskResult {
	return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0,
		FinalAnswer: "answer", FinishedAt: newFakeClock().Now()}
}

// TestResultArbitration 三分支仲裁（SPEC 4.3 收敛表）：
// 非终态接受 / 同终态同结果重发 ack / 冲突保留先到回 err(result_conflict)。
func TestResultArbitration(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	// 分支 1：非终态（acknowledged）接受
	ack1, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	first := completeRes()
	node.result(ack1.TaskID, first)
	task := f.taskOf(ack1.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil || task.Result.FinalAnswer != "answer" {
		t.Fatalf("非终态收到 result 应接受为终态, got %+v", task.Result)
	}
	if len(node.resultAcks) != 1 {
		t.Fatalf("接受后应回 result_ack, got %v", node.resultAcks)
	}

	// 分支 2：同终态同结果（重发）→ 重发 result_ack，无冲突
	node.result(ack1.TaskID, first)
	if len(node.resultAcks) != 2 {
		t.Fatalf("重复结果应重发 result_ack, got %v", node.resultAcks)
	}
	if len(node.errs) != 0 {
		t.Fatalf("重复结果不应报冲突, got %+v", node.errs)
	}

	// 分支 3：已终态但结果不同 → 保留先到，回 err(result_conflict)，不重发 ack
	conflicting := completeRes()
	conflicting.ExitCode = 3
	node.result(ack1.TaskID, conflicting)
	task = f.taskOf(ack1.TaskID)
	if task.Result == nil || task.Result.ExitCode != 0 {
		t.Errorf("冲突应保留先到结果, got %+v", task.Result)
	}
	if len(node.resultAcks) != 2 {
		t.Errorf("冲突不得重发 result_ack, got %v", node.resultAcks)
	}
	if len(node.errs) != 1 || node.errs[0].Code != protocol.CodeResultConflict {
		t.Errorf("冲突应回 err(result_conflict), got %+v", node.errs)
	}

	// 分支 1 补充：result 直达（submitted 未出队即收到，如 hub 重启丢投递态）
	f.registerOffline("nodeD", agentsOf("echo"))
	ack2, _ := f.core.Submit(submitReq("nodeD", "echo"), "")
	nodeD := f.newNode("nodeD", agentsOf("echo"), true) // 上线（任务仍在信箱？——离线提交未出队）
	_ = nodeD
	// nodeB 冒认 nodeD 任务已在归属检查覆盖；此处验证 submitted 直达终态
	nodeD.result(ack2.TaskID, first)
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusCompleted {
		t.Errorf("submitted 直达终态应接受, got %s", got)
	}
	if n := f.countMailbox("nodeD"); n != 0 {
		t.Errorf("直达终态应释放信箱, got %d", n)
	}
}

// TestResultEventToNodeSubmitter 提交方为 node 会话时生成 result_event（SPEC 4.2）。
// 边界方案 v2：node 只能本机派单，提交方与执行方同为 nodeA。
func TestResultEventToNodeSubmitter(t *testing.T) {
	f := newHub(t)
	nodeA := f.newNode("nodeA", agentsOf("echo"), false) // 提交方 = 执行方

	ack, err := f.core.Submit(submitReq("nodeA", "echo"), "nodeA")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 执行方（=本机）上报终态触发 result_event
	nodeA.result(ack.TaskID, completeRes())
	// membus 同步链：result 处理返回时 result_event 已推完
	if len(nodeA.resultEvent) != 1 {
		t.Fatalf("node 提交方应收到 1 条 result_event, got %d", len(nodeA.resultEvent))
	}
	ev := nodeA.resultEvent[0]
	if ev.TaskID != ack.TaskID || ev.Status != protocol.ResultCompleted || ev.Result == nil {
		t.Errorf("result_event 内容不符: %+v", ev)
	}
	// HTTP 提交方（无 node 会话）不收 result_event：另提一条走 HTTP 语义
	nodeA.resultEvent = nil
	ack2, err := f.core.Submit(submitReq("nodeA", "echo", "via-http"), "")
	if err != nil {
		t.Fatalf("HTTP 提交失败: %v", err)
	}
	nodeA.result(ack2.TaskID, completeRes())
	if len(nodeA.resultEvent) != 0 {
		t.Errorf("HTTP 提交方不应经 nodeA 收到 result_event")
	}
}

// TestExpireSweep expire 清扫（SPEC 4.3：非终态任意状态到期 → failed/expired；
// 提交方 node 会话在线收 result_event、离线即弃（SPEC 已声明限制，靠补查）；
// 此后迟到结果按冲突行处理）。
func TestExpireSweep(t *testing.T) {
	f := newHub(t)

	// 场景 1：目标（=本机，node 只能本机派单）离线 → 任务滞留 submitted 入信箱；
	// 到期清扫 failed/expired 并释放信箱。
	f.registerOffline("nodeA", agentsOf("echo"))
	ack, _ := f.core.Submit(submitReq("nodeA", "echo"), "nodeA")
	// 未到期：不动
	f.clock.Advance(protocol.TaskRetention - f.second())
	f.core.Sweep()
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("未到期不应清扫, got %s", got)
	}
	// 到期：failed/expired + 释放信箱
	f.clock.Advance(2 * f.second())
	f.core.Sweep()
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusFailed || task.Result == nil || task.Result.Error != protocol.CodeExpired {
		t.Fatalf("到期应 failed/expired, got %s %+v", task.Status, task.Result)
	}
	if n := f.countMailbox("nodeA"); n != 0 {
		t.Errorf("过期应释放信箱, got %d", n)
	}

	// 场景 2：提交方（=执行方）在线、不 ack（滞留 offered）；到期清扫并推
	// result_event；过期后 node 结果到达按冲突行处理。
	nodeA := f.newNode("nodeA", agentsOf("echo"), false)
	ack2, _ := f.core.Submit(submitReq("nodeA", "echo", "sweep-offered"), "nodeA")
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("不 ack 应滞留 offered, got %s", got)
	}
	f.clock.Advance(protocol.TaskRetention + 2*f.second())
	f.core.Sweep()
	task2 := f.taskOf(ack2.TaskID)
	if task2.Status != protocol.StatusFailed || task2.Result == nil || task2.Result.Error != protocol.CodeExpired {
		t.Fatalf("到期应 failed/expired, got %s %+v", task2.Status, task2.Result)
	}
	if len(nodeA.resultEvent) != 1 || nodeA.resultEvent[0].Status != protocol.ResultFailed {
		t.Fatalf("提交方应收到 failed result_event, got %+v", nodeA.resultEvent)
	}

	// 过期后 node 结果到达：按冲突行处理（终态 + 不同结果 → err(result_conflict)）
	nodeA.result(ack2.TaskID, completeRes())
	if got := f.taskOf(ack2.TaskID).Status; got != protocol.StatusFailed {
		t.Errorf("过期后迟到 result 不得改写终态, got %s", got)
	}
	if len(nodeA.errs) != 1 || nodeA.errs[0].Code != protocol.CodeResultConflict {
		t.Errorf("迟到结果应回 err(result_conflict), got %+v", nodeA.errs)
	}
}

// TestTerminalCleanup 终态保留 24h 后从快照清理，idem 索引同步失效（SPEC 4.5/4.6）。
func TestTerminalCleanup(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	req := submitReq("nodeB", "echo")
	req.IdemKey = "cleanup-key"
	ack, _ := f.core.Submit(req, "")
	node.result(ack.TaskID, completeRes())

	// 未到期：保留
	f.clock.Advance(protocol.TerminalRetention - f.second())
	f.core.Sweep()
	if _, ok := f.core.GetTask(ack.TaskID); !ok {
		t.Fatal("终态未满 24h 不应清理")
	}
	// 到期：删除 + idem 失效（同键重新提交得到新任务）
	f.clock.Advance(2 * f.second())
	f.core.Sweep()
	if _, ok := f.core.GetTask(ack.TaskID); ok {
		t.Fatal("终态超 24h 应从快照清理")
	}
	ack2, err := f.core.Submit(req, "")
	if err != nil {
		t.Fatalf("清理后同键提交应作为新任务, got %v", err)
	}
	if ack2.Dup || ack2.TaskID == ack.TaskID {
		t.Errorf("清理后 idem 应失效, got dup=%v task=%s", ack2.Dup, ack2.TaskID)
	}
}

// TestSizeCacheConsistency 字节缓存与快照自洽（noteTask/noteDelete 记账不漂移）。
func TestSizeCacheConsistency(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)

	var want int64
	for i := 0; i < 5; i++ {
		ack, err := f.core.Submit(submitReq("nodeB", "echo"), "")
		if err != nil {
			t.Fatalf("提交失败: %v", err)
		}
		node.result(ack.TaskID, completeRes())
		tk := f.taskOf(ack.TaskID)
		want += int64(len(mustMarshal(t, &tk)))
	}
	if got := f.core.totalBytes(); got != want {
		t.Errorf("缓存合计 %d != 实测合计 %d", got, want)
	}
	// 清理后缓存归零、快照无残留
	f.clock.Advance(protocol.TerminalRetention + 2*f.second())
	f.core.Sweep()
	if got := f.core.totalBytes(); got != 0 {
		t.Errorf("清理后缓存应归零, got %d", got)
	}
	if n := len(f.st.View().Tasks); n != 0 {
		t.Errorf("清扫后不应残留任务, got %d", n)
	}
}
