package core

import (
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// bumpEpoch 直接推进 node 注册代次（等价会话层重连注册的 epoch+1 持久化，
// P4 租约测试用：membus 链路不跑注册握手，代次恒 0）。
func (f *hubFixture) bumpEpoch(nodeID string, n int64) {
	f.t.Helper()
	if err := f.st.Update(func(st *store.State) error {
		rec := st.Nodes[nodeID]
		rec.Epoch = n
		st.Nodes[nodeID] = rec
		return nil
	}); err != nil {
		f.t.Fatalf("推进 epoch: %v", err)
	}
}

// reRegisterAs 模拟 node 重连换代：注册事务推进 hub 代次 + node 桩同步拿到新
// 代次（真实链路经 register_ack；帧层 SessionEpoch 与 fence 断言解耦）。
func (f *hubFixture) reRegisterAs(node *testNode, nodeID string, n int64) {
	f.bumpEpoch(nodeID, n)
	node.epoch = n
}

// resultFenced 携租约回带发送 task_result（P4 契约测试专用）。
func (n *testNode) resultFenced(taskID string, fence int64, res protocol.TaskResult) {
	n.t.Helper()
	b, err := jsonMarshal(protocol.TaskResultPayload{TaskID: taskID, Status: res.Status, Result: &res})
	if err != nil {
		n.t.Fatalf("marshal result: %v", err)
	}
	if err := n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub",
		SessionEpoch: n.epoch, FenceEpoch: fence, Payload: b,
	}); err != nil {
		n.t.Fatalf("发送 result: %v", err)
	}
}

// TestTaskLeaseDispatchSnapshot 派发事务内落库租约快照：offer 帧与任务记录
// 携同一代次（P4/SPEC 4.11）；重派（offer 超时回退后补推）刷新租约。
func TestTaskLeaseDispatchSnapshot(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false) // 不 autoAck：任务停在 offered
	f.reRegisterAs(node, "nodeB", 2)

	ack, err := f.core.Submit(submitReq("nodeB", "echo"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	task := f.taskOf(ack.TaskID)
	if task.Lease == nil || task.Lease.FenceEpoch != 2 {
		t.Fatalf("任务应落库派发租约 fence=2, got %+v", task.Lease)
	}
	if len(node.offerEnvs) != 1 || node.offerEnvs[0].FenceEpoch != 2 {
		t.Fatalf("offer 帧应携派发租约 fence=2, got %+v", node.offerEnvs)
	}

	// offer 超时回退 → 补推：租约换新（fence=3）
	f.reRegisterAs(node, "nodeB", 3)
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	task = f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusOffered || task.Lease == nil || task.Lease.FenceEpoch != 3 {
		t.Fatalf("重派后任务应 offered 且租约刷新 fence=3, got status=%s lease=%+v", task.Status, task.Lease)
	}
	if n := len(node.offerEnvs); n != 2 || node.offerEnvs[1].FenceEpoch != 3 {
		t.Fatalf("重派 offer 应携新租约 fence=3, got %d 帧", n)
	}
}

// TestZombieResultRejectedAndRedispatchAccepts 重派后旧派单回写拒收（P4 核心
// 场景）：任务已换新租约，旧 fence 的迟到结果维持原状态拒收并记审计；新租约
// 回写正常接受。
func TestZombieResultRejectedAndRedispatchAccepts(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), false)
	f.reRegisterAs(node, "nodeB", 2)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "") // offer fence=2，未确认

	// 重派换代（模拟重连 epoch+1 后补推）：租约 2 → 3，node 确认新派单
	f.reRegisterAs(node, "nodeB", 3)
	f.clock.Advance(protocol.OfferAckTimeout + f.second())
	f.core.Sweep()
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusOffered {
		t.Fatalf("前置：重派后应 offered, got %s", got)
	}
	node.ack(ack.TaskID)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusAcknowledged {
		t.Fatalf("前置：新派单应确认, got %s", got)
	}

	// 旧派单（fence=2）的 zombie 回写到达：拒收，任务维持 acknowledged
	node.resultFenced(ack.TaskID, 2, completeRes())
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusAcknowledged || task.Result != nil {
		t.Fatalf("zombie 回写应维持原状态 acknowledged, got %s result=%+v", task.Status, task.Result)
	}
	if task.Lease == nil || task.Lease.Rejects != 1 || task.Lease.LastRejectedFence != 2 || task.Lease.FenceEpoch != 3 {
		t.Fatalf("应记 zombie 拒收审计(rejects=1 last=2 lease=3), got %+v", task.Lease)
	}
	if len(node.resultAcks) != 0 {
		t.Fatalf("zombie 回写不得回 result_ack, got %v", node.resultAcks)
	}
	if len(node.errs) != 1 || node.errs[0].Code != protocol.CodeResultConflict ||
		!strings.Contains(node.errs[0].Msg, "zombie_result_rejected") {
		t.Fatalf("应回 err(result_conflict) 且注明 zombie_result_rejected, got %+v", node.errs)
	}

	// 当前派单（fence=3）的正常回写：接受
	node.resultFenced(ack.TaskID, 3, completeRes())
	task = f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil {
		t.Fatalf("当前租约回写应接受, got %s result=%+v", task.Status, task.Result)
	}
	if len(node.resultAcks) != 1 {
		t.Fatalf("接受后应回 result_ack, got %v", node.resultAcks)
	}
}

// TestResultFenceLenientMissingFence 向后兼容：旧版 node 回写不带 fence_epoch
//（缺省 0）→ 按「缺省=当前租约」宽容接受，不产生审计。
func TestResultFenceLenientMissingFence(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true)
	f.reRegisterAs(node, "nodeB", 2)

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "") // lease fence=2
	node.start(ack.TaskID)
	node.result(ack.TaskID, completeRes()) // 无 fence_epoch（旧版 node 帧形态）

	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted {
		t.Fatalf("缺省 fence 回写应宽容接受, got %s", task.Status)
	}
	if task.Lease == nil || task.Lease.Rejects != 0 || task.Lease.LastRejectedFence != 0 {
		t.Fatalf("宽容路径不得记拒收审计, got %+v", task.Lease)
	}
	if len(node.errs) != 0 {
		t.Fatalf("宽容路径不得回 err, got %+v", node.errs)
	}
}

// TestResultFenceNoLeaseRejectsForged 任务未携租约（membus 等价链路 lease=0）
// 时，显式非零回写与租约不符 → 拒收（门禁对伪造/串线回写同样生效）。
func TestResultFenceNoLeaseRejectsForged(t *testing.T) {
	f := newHub(t)
	node := f.newNode("nodeB", agentsOf("echo"), true) // 未 bump：派发无租约

	ack, _ := f.core.Submit(submitReq("nodeB", "echo"), "")
	node.start(ack.TaskID)
	node.resultFenced(ack.TaskID, 9, completeRes())

	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusRunning || task.Result != nil {
		t.Fatalf("无租约任务的伪造回写应维持原状态, got %s", task.Status)
	}
	if task.Lease == nil || task.Lease.Rejects != 1 || task.Lease.LastRejectedFence != 9 {
		t.Fatalf("应记拒收审计, got %+v", task.Lease)
	}
}
