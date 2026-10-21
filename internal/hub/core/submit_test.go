package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestSubmitChainRejections 校验链前两级逐级拒绝（SPEC 六）：
// 鉴权存在（字段缺失）→ 目标 exposed（node 未注册/agent 不存在/exposed=false）。
func TestSubmitChainRejections(t *testing.T) {
	f := newHub(t)
	f.newNode("nodeB", agentsOf("echo", "hidden-"), false)

	bad := []struct {
		name string
		req  protocol.TaskSubmitRequest
		want error
	}{
		{"缺 target_node", protocol.TaskSubmitRequest{TargetAgent: "echo", SubmittedBy: "u"}, ErrInvalid},
		{"缺 target_agent", protocol.TaskSubmitRequest{TargetNode: "nodeB", SubmittedBy: "u"}, ErrInvalid},
		{"缺 submitted_by", protocol.TaskSubmitRequest{TargetNode: "nodeB", TargetAgent: "echo"}, ErrInvalid},
		{"node 未注册", submitReq("ghost", "echo"), ErrAgentNotExposed},
		{"agent 不存在", submitReq("nodeB", "no-such"), ErrAgentNotExposed},
		{"agent 未接入", submitReq("nodeB", "hidden"), ErrAgentNotExposed},
	}
	for _, tc := range bad {
		ack, err := f.core.Submit(tc.req, "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: 期望 %v, got %v", tc.name, tc.want, err)
		}
		if ack != nil {
			t.Errorf("%s: 拒绝时不应返回 ack", tc.name)
		}
	}
	// 拒绝路径无任务落库
	if n := len(f.st.View().Tasks); n != 0 {
		t.Errorf("拒绝后不应有任务落库, got %d", n)
	}
}

// TestSubmitIdempotency idem 三分支 + 重复确认重放（SPEC 4.5：去重后必须重放 Ack）。
func TestSubmitIdempotency(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo")) // 离线，任务留在信箱便于观察
	nodeA := f.newNode("nodeA", agentsOf("unused"), false)

	req := submitReq("nodeB", "echo", "hello")
	req.IdemKey = "demo-001"

	ack1, err := f.core.Submit(req, "nodeA")
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	if ack1.Dup {
		t.Error("首次提交不应为 dup")
	}
	if len(nodeA.submitAcks) != 1 || nodeA.submitAcks[0].TaskID != ack1.TaskID {
		t.Fatalf("node 提交方应收到 1 条 submit_ack, got %+v", nodeA.submitAcks)
	}

	// 同键同内容：原 task_id + dup=true，且重放 submit_ack
	ack2, err := f.core.Submit(req, "nodeA")
	if err != nil {
		t.Fatalf("幂等重试失败: %v", err)
	}
	if !ack2.Dup || ack2.TaskID != ack1.TaskID || ack2.TraceID != ack1.TraceID {
		t.Errorf("幂等命中应返回原 id/trace, got %+v want task=%s", ack2, ack1.TaskID)
	}
	if len(nodeA.submitAcks) != 2 || !nodeA.submitAcks[1].Dup {
		t.Errorf("重复提交应重放 submit_ack(dup=true), got %+v", nodeA.submitAcks)
	}

	// 同键不同内容：冲突
	conflict := submitReq("nodeB", "echo", "CHANGED")
	conflict.IdemKey = "demo-001"
	if _, err := f.core.Submit(conflict, "nodeA"); !errors.Is(err, mailbox.ErrConflict) {
		t.Errorf("同键不同内容应 ErrConflict, got %v", err)
	}
	// 冲突帧已回给 node 提交方
	if len(nodeA.errs) != 1 || nodeA.errs[0].Code != "conflict" {
		t.Errorf("提交方应收到 err(conflict), got %+v", nodeA.errs)
	}
	// 冲突不产生新任务
	if n := len(f.st.View().Tasks); n != 1 {
		t.Errorf("冲突不应落库新任务, got %d", n)
	}
}

// TestSubmitIdemBeforeQuota 链序：信箱配额满时同键重试仍取回原 task_id（命中即
// 返回，不做配额检查）；同键不同内容在满箱时仍报冲突（SPEC 4.5 顺序保证）。
func TestSubmitIdemBeforeQuota(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	first := submitReq("nodeB", "echo", "first")
	first.IdemKey = "key-first"
	ack1, err := f.core.Submit(first, "")
	if err != nil {
		t.Fatalf("首条提交失败: %v", err)
	}

	// 灌满配额（离线 node，任务全部滞留 submitted）
	for i := 0; i < protocol.MailboxQuota-1; i++ {
		r := submitReq("nodeB", "echo", "filler")
		if _, err := f.core.Submit(r, ""); err != nil {
			t.Fatalf("填充第 %d 条失败: %v", i+2, err)
		}
	}
	if mb := f.countMailbox("nodeB"); mb != protocol.MailboxQuota {
		t.Fatalf("前置失败：信箱应为 %d, got %d", protocol.MailboxQuota, mb)
	}
	// 配额满，新任务拒收
	if _, err := f.core.Submit(submitReq("nodeB", "echo", "new"), ""); !errors.Is(err, mailbox.ErrMailboxFull) {
		t.Errorf("满箱应 ErrMailboxFull, got %v", err)
	}
	// 但同键重试命中即返回（先于配额检查）
	ack2, err := f.core.Submit(first, "")
	if err != nil {
		t.Fatalf("满箱同键重试应命中, got %v", err)
	}
	if !ack2.Dup || ack2.TaskID != ack1.TaskID {
		t.Errorf("满箱幂等命中应返回原 id, got %+v want %s", ack2, ack1.TaskID)
	}
	// 满箱下同键不同内容仍冲突
	conflict := submitReq("nodeB", "echo", "different")
	conflict.IdemKey = "key-first"
	if _, err := f.core.Submit(conflict, ""); !errors.Is(err, mailbox.ErrConflict) {
		t.Errorf("满箱同键不同内容应 ErrConflict, got %v", err)
	}
}

// TestSubmitSizeLimit 序列化体积实测预检：完整 TaskOffer 信封编码超 1MiB 拒收。
func TestSubmitSizeLimit(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	huge := submitReq("nodeB", "echo")
	huge.Input.Stdin = strings.Repeat("x", protocol.MaxMessageBytes) // 载荷本身超限
	if _, err := f.core.Submit(huge, ""); !errors.Is(err, protocol.ErrFrameTooLarge) {
		t.Errorf("超限应 ErrFrameTooLarge, got %v", err)
	}
	// 略低于上限的合法提交成功（留出信封其余字段余量）
	ok := submitReq("nodeB", "echo")
	ok.Input.Stdin = strings.Repeat("x", protocol.MaxMessageBytes-(64<<10))
	if _, err := f.core.Submit(ok, ""); err != nil {
		t.Errorf("限内提交应成功, got %v", err)
	}
}

// TestSubmitMailboxByteBudget 信箱字节预算（SPEC 4.6 8MiB，submitted 任务体字节和）。
func TestSubmitMailboxByteBudget(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))

	big := strings.Repeat("x", 900<<10) // 单条任务体 ~900KiB，合法提交
	for i := 0; i < 9; i++ {
		r := submitReq("nodeB", "echo")
		r.Input.Stdin = big
		if _, err := f.core.Submit(r, ""); err != nil {
			t.Fatalf("第 %d 条提交失败: %v", i+1, err)
		}
	}
	last := submitReq("nodeB", "echo")
	last.Input.Stdin = big
	if _, err := f.core.Submit(last, ""); !errors.Is(err, mailbox.ErrMailboxFull) {
		t.Errorf("超字节预算应 ErrMailboxFull, got %v", err)
	}
}

// TestSubmitStorageFull 存储全局预算分支（直接注入缓存计数验证拒绝路径，
// 全量灌库 256MiB 非单测成本承受范围）。
func TestSubmitStorageFull(t *testing.T) {
	f := newHub(t)
	f.newNode("nodeB", agentsOf("echo"), false)

	f.core.szMu.Lock()
	f.core.sizes["ghost-task"] = protocol.StorageBudget
	f.core.totalSz = protocol.StorageBudget
	f.core.szMu.Unlock()

	if _, err := f.core.Submit(submitReq("nodeB", "echo"), ""); !errors.Is(err, ErrStorageFull) {
		t.Errorf("存储满应 ErrStorageFull, got %v", err)
	}
	// 释放预算后恢复
	f.core.szMu.Lock()
	f.core.totalSz = 0
	f.core.szMu.Unlock()
	if _, err := f.core.Submit(submitReq("nodeB", "echo"), ""); err != nil {
		t.Errorf("预算恢复后应成功, got %v", err)
	}
}

// TestNodeSubmitAckFrames node 会话提交方的帧面：submit_ack 到达、失败回 err 帧。
func TestNodeSubmitAckFrames(t *testing.T) {
	f := newHub(t)
	f.registerOffline("nodeB", agentsOf("echo"))
	nodeA := f.newNode("nodeA", agentsOf("misc"), false)

	r := submitReq("nodeB", "echo")
	r.TargetAgent = "no-such" // 目标不存在 → err(agent_not_exposed)
	if _, err := f.core.Submit(r, "nodeA"); !errors.Is(err, ErrAgentNotExposed) {
		t.Fatalf("期望 ErrAgentNotExposed, got %v", err)
	}
	if len(nodeA.errs) != 1 || nodeA.errs[0].Code != protocol.CodeAgentNotExposed {
		t.Fatalf("node 提交方应收到 err(agent_not_exposed), got %+v", nodeA.errs)
	}
	// 成功路径 → submit_ack
	if _, err := f.core.Submit(submitReq("nodeB", "echo"), "nodeA"); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if len(nodeA.submitAcks) != 1 || nodeA.submitAcks[0].Dup {
		t.Errorf("应收到 1 条 submit_ack(dup=false), got %+v", nodeA.submitAcks)
	}
	// node 提交方身份覆盖：SubmittedBy=node_id
	tasks := f.st.View().Tasks
	for _, tk := range tasks {
		if tk.SubmittedBy != "nodeA" {
			t.Errorf("node 提交应覆盖 SubmittedBy=node_id, got %q", tk.SubmittedBy)
		}
	}
}

// countMailbox 测试辅助：信箱排队条数。
func (f *hubFixture) countMailbox(nodeID string) int {
	return len(f.st.View().Mailboxes[nodeID].Tasks)
}
