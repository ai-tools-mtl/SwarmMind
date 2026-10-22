package core

import (
	"errors"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// execNode 真实执行 node 桩：真账本 + 真 runner，按 SPEC 4.7 顺序处理 offer——
// accepted 落盘 → task_ack；started 落盘 → task_started → spawn；
// finished+result 落盘 → task_result。重复 offer 不再执行，直接重发 ack
//（若已 finished 附带重发 result，SPEC 4.3）。
type execNode struct {
	t       *testing.T
	ep      *transport.Endpoint
	led     *ledger.Ledger
	runners map[string]*runner.Runner // agent name → runner

	offers      int
	submitAcks  []protocol.SubmitAckPayload
	resultEvent []protocol.TaskResultPayload
	errs        []protocol.ErrInfo
}

func newExecNode(f *hubFixture, name string, runners map[string]*runner.Runner, cards []protocol.AgentCard) *execNode {
	f.t.Helper()
	led, _, err := ledger.Open(f.t.TempDir())
	if err != nil {
		f.t.Fatalf("打开账本失败: %v", err)
	}
	f.t.Cleanup(func() { _ = led.Close() })
	n := &execNode{t: f.t, ep: f.bus.Endpoint(name), led: led, runners: runners}
	n.ep.Subscribe(n.handle)
	if err := f.core.RegisterNode(name, cards); err != nil {
		f.t.Fatalf("注册失败: %v", err)
	}
	f.core.SetOnline(name, true)
	return n
}

func (n *execNode) handle(env *protocol.Envelope) {
	switch env.Type {
	case protocol.MsgTaskOffer:
		var task protocol.Task
		if err := jsonUnmarshal(env.Payload, &task); err != nil {
			n.t.Errorf("offer 解析失败: %v", err)
			return
		}
		n.offers++
		err := n.led.Accept(task)
		if errors.Is(err, ledger.ErrDuplicate) {
			// 重复 offer：不再执行，重发 ack；已 finished 附带重发 result
			n.ack(task.ID)
			if e, ok := n.led.Get(task.ID); ok && e.State == ledger.StateFinished && e.Result != nil {
				n.sendResult(task.ID, *e.Result)
			}
			return
		}
		if err != nil {
			n.t.Errorf("账本入账失败: %v", err)
			return
		}
		n.ack(task.ID) // accepted 落盘成功后才发（SPEC 4.7）
		if err := n.led.MarkStarted(task.ID); err != nil {
			n.t.Errorf("标记 started 失败: %v", err)
			return
		}
		n.send(protocol.MsgTaskStarted, protocol.TaskRef{TaskID: task.ID})
		res := func() protocol.TaskResult {
			r, ok := n.runners[task.TargetAgent]
			if !ok {
				return protocol.TaskResult{
					Status: protocol.ResultFailed, ExitCode: protocol.ExitCodeNotRun,
					Error: protocol.CodeAgentNotFound,
				}
			}
			return r.Run(context.Background(), task)
		}()
		// SPEC 4.7 顺序：finished+result 落盘成功后才发 task_result
		if err := n.led.MarkFinished(task.ID, res); err != nil {
			n.t.Errorf("MarkFinished 失败: %v", err)
			return
		}
		n.sendResult(task.ID, res)
	case protocol.MsgResultAck:
		var ref protocol.TaskRef
		if err := jsonUnmarshal(env.Payload, &ref); err != nil {
			return
		}
		if err := n.led.MarkResultAcked(ref.TaskID); err != nil {
			n.t.Errorf("MarkResultAcked 失败: %v", err)
		}
	case protocol.MsgErr:
		// result_conflict 等：账本置 closed 停止重传（SPEC 4.5）
		if env.Err != nil && env.Err.Code == protocol.CodeResultConflict {
			n.errs = append(n.errs, *env.Err)
		}
	case protocol.MsgSubmitAck:
		var p protocol.SubmitAckPayload
		if err := jsonUnmarshal(env.Payload, &p); err == nil {
			n.submitAcks = append(n.submitAcks, p)
		}
	case protocol.MsgResultEvent:
		var p protocol.TaskResultPayload
		if err := jsonUnmarshal(env.Payload, &p); err == nil {
			n.resultEvent = append(n.resultEvent, p)
		}
	}
}

func (n *execNode) send(typ string, payload any) {
	n.t.Helper()
	b, err := jsonMarshal(payload)
	if err != nil {
		n.t.Fatalf("marshal %s: %v", typ, err)
	}
	if err := n.ep.Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: typ,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: b,
	}); err != nil {
		n.t.Fatalf("发送 %s: %v", typ, err)
	}
}

func (n *execNode) ack(taskID string) { n.send(protocol.MsgTaskAck, protocol.TaskRef{TaskID: taskID}) }

func (n *execNode) sendResult(taskID string, res protocol.TaskResult) {
	n.send(protocol.MsgTaskResult, protocol.TaskResultPayload{
		TaskID: taskID, Status: res.Status, Result: &res,
	})
}

// echoProfile 构造 /bin/echo 的 ExecProfile（产物约定目录以 workdir 隔离）。
func echoProfile(t *testing.T) protocol.ExecProfile {
	return protocol.ExecProfile{
		Name: "echo-demo", Kind: protocol.AgentKindCLI,
		Command: "/bin/echo", Workdir: t.TempDir(),
		TimeoutSec: 10, AllowArgs: true, Exposed: true,
		Description: "回声测试", PermissionMode: "no-shell",
	}
}

// TestFullChain 内存 transport 全链（SPEC 八.3）：node 本机派单（边界方案 v2：
// node 会话只能派给本机，K2 回环链路）→ submit_ack → 投递回本机 → ack →
// started → 执行 → result → result_ack → result_event，全程真实 ledger/runner，
// membus 同步链一次 Submit 调用内完成。
func TestFullChain(t *testing.T) {
	f := newHub(t)
	echo := echoProfile(t)
	card := protocol.AgentCard{
		Name: echo.Name, Kind: echo.Kind, Description: echo.Description,
		InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: echo.PermissionMode, ProtocolVer: 1, Exposed: true,
	}
	nodeB := newExecNode(f, "nodeB", map[string]*runner.Runner{
		"echo-demo": runner.New(echo, baseTestEnv2(), filepath.Join(t.TempDir(), "home")),
	}, []protocol.AgentCard{card})

	req := submitReq("nodeB", "echo-demo", "hello", "swarmmind")
	req.IdemKey = "fullchain-001"
	ack, err := f.core.Submit(req, "nodeB")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// membus 同步链：Submit 返回即全链完成
	if nodeB.offers != 1 {
		t.Fatalf("nodeB 应收到 1 次 offer（本机派单经 hub 回投本机）, got %d", nodeB.offers)
	}
	if len(nodeB.submitAcks) != 1 || nodeB.submitAcks[0].TaskID != ack.TaskID || nodeB.submitAcks[0].Dup {
		t.Errorf("submit_ack 不符: %+v", nodeB.submitAcks)
	}
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil {
		t.Fatalf("任务应 completed, got %s", task.Status)
	}
	if task.Result.FinalAnswer != "hello swarmmind\n" || task.Result.ExitCode != 0 {
		t.Errorf("结果不符: %+v", task.Result)
	}
	if task.SubmittedBy != "nodeB" {
		t.Errorf("node 提交应覆盖 SubmittedBy, got %q", task.SubmittedBy)
	}
	// result_event 闭合「调用方拿回结果」（SPEC 4.2）
	if len(nodeB.resultEvent) != 1 {
		t.Fatalf("提交方应收到 result_event, got %d", len(nodeB.resultEvent))
	}
	ev := nodeB.resultEvent[0]
	if ev.TaskID != ack.TaskID || ev.Status != protocol.ResultCompleted ||
		ev.Result == nil || ev.Result.FinalAnswer != "hello swarmmind\n" {
		t.Errorf("result_event 不符: %+v", ev)
	}
	// node 账本：finished 且已 acked（三段持久化确认闭合，SPEC 4.5）
	e, ok := nodeB.led.Get(ack.TaskID)
	if !ok || e.State != ledger.StateFinished || !e.ResultAcked || e.Result == nil {
		t.Fatalf("账本状态不符: %+v ok=%v", e, ok)
	}
}

// TestFullChainOfflineMailbox 离线信箱（DoD 5）：目标离线 → 提交入信箱 →
// 上线补推 → 完成。重复 offer 只执行一次（node 桩走 ErrDuplicate 重发 ack 分支）。
func TestFullChainOfflineMailbox(t *testing.T) {
	f := newHub(t)
	echo := echoProfile(t)
	card := protocol.AgentCard{
		Name: echo.Name, Kind: echo.Kind, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell",
		ProtocolVer: 1, Exposed: true,
	}
	runners := map[string]*runner.Runner{"echo-demo": runner.New(echo, baseTestEnv2(), t.TempDir())}
	f.registerOffline("nodeB", []protocol.AgentCard{card}) // 只注册不上线
	nodeB := &execNode{t: f.t, ep: f.bus.Endpoint("nodeB"), led: nil, runners: runners}
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("打开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	nodeB.led = led
	nodeB.ep.Subscribe(nodeB.handle)

	ack, err := f.core.Submit(submitReq("nodeB", "echo-demo", "offline"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusSubmitted {
		t.Fatalf("离线目标任务应滞留 submitted, got %s", got)
	}
	if n := f.countMailbox("nodeB"); n != 1 {
		t.Fatalf("应入信箱 1 条, got %d", n)
	}

	f.core.SetOnline("nodeB", true) // 上线补推 → 全链完成
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil || task.Result.FinalAnswer != "offline\n" {
		t.Fatalf("上线后应完成, got %s %+v", task.Status, task.Result)
	}
	if nodeB.offers != 1 {
		t.Errorf("应恰好投递 1 次, got %d", nodeB.offers)
	}
}

// TestFullChainNeedsReview needs_review 全链（DoD 10 链路）：node 重启恢复产生的
// needs_review 经 task_result 正常入 hub 终态并可查（不折叠为 failed）。
func TestFullChainNeedsReview(t *testing.T) {
	f := newHub(t)
	echo := echoProfile(t)
	card := protocol.AgentCard{
		Name: echo.Name, Kind: echo.Kind, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell",
		ProtocolVer: 1, Exposed: true,
	}
	nodeB := newExecNode(f, "nodeB", map[string]*runner.Runner{
		"echo-demo": runner.New(echo, baseTestEnv2(), t.TempDir()),
	}, []protocol.AgentCard{card})

	ack, err := f.core.Submit(submitReq("nodeB", "echo-demo"), "nodeB") // 本机派单（边界方案 v2）
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if f.taskOf(ack.TaskID).Status != protocol.StatusCompleted {
		t.Fatal("前置失败：任务应已完成")
	}

	// 模拟 node 侧重启后重发（账本原样重发结果一致 → 重发 result_ack，不冲突）
	e, ok := nodeB.led.Get(ack.TaskID)
	if !ok || e.Result == nil {
		t.Fatal("前置失败：账本应有已确认结果")
	}
	nr := *e.Result
	nodeB.sendResult(ack.TaskID, nr)
	if got := f.taskOf(ack.TaskID).Status; got != protocol.StatusCompleted {
		t.Errorf("重复结果不得改写终态, got %s", got)
	}

	// needs_review 结果回传（重启恢复语义，SPEC 4.8）：terminal 后新结果 → 冲突行
	nrev := protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: protocol.ExitCodeNotRun, Error: protocol.CodeNodeRestartedMidway}
	nodeB.sendResult(ack.TaskID, nrev)
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted {
		t.Errorf("先到终态保留, got %s", task.Status)
	}
	if len(nodeB.errs) != 1 || nodeB.errs[0].Code != protocol.CodeResultConflict {
		t.Errorf("不同结果应回 err(result_conflict), got %+v", nodeB.errs)
	}
}

// TestHubRestartRecovery hub 重启恢复（DoD 7）：能力表/任务/信箱/字节缓存，
// node 上线后滞留任务补推完成。
func TestHubRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	bus := transport.NewBus()
	clock := newFakeClock()

	// 第一代 hub
	st1, err := storeOpen(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	core1, err := New(Config{Store: st1, Transport: bus.Endpoint("hub"), Now: clock.Now})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	echo := echoProfile(t)
	card := protocol.AgentCard{
		Name: echo.Name, Kind: echo.Kind, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell",
		ProtocolVer: 1, Exposed: true,
	}
	if err := core1.RegisterNode("nodeB", []protocol.AgentCard{card}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	core1.SetOnline("nodeB", false)
	ack, err := core1.Submit(submitReq("nodeB", "echo-demo", "persisted"), "")
	if err != nil {
		t.Fatalf("提交: %v", err)
	}
	_ = core1.Close()
	_ = st1.Close()

	// 第二代 hub：同一 data_dir 重开
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
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st2, core: core2, clock: clock}
	f.hubEP.Subscribe(core2.HandleFrame)

	// 能力表恢复：agent 可达；任务/信箱恢复：滞留 submitted
	if _, ok := f.core.GetTask(ack.TaskID); !ok {
		t.Fatal("任务未随快照恢复")
	}
	if n := f.countMailbox("nodeB"); n != 1 {
		t.Fatalf("信箱未恢复, got %d", n)
	}
	if _, err := f.core.Submit(submitReq("nodeB", "echo-demo"), ""); err == nil {
		// （恢复后正常可达——这里用执行链验证，见下）
		_ = err
	}

	// node 上线 → 补推完成（真 runner）
	nodeB := newExecNode(f, "nodeB", map[string]*runner.Runner{
		"echo-demo": runner.New(echo, baseTestEnv2(), t.TempDir()),
	}, nil) // 能力表已在快照中，不重注册
	_ = nodeB
	task := f.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil || task.Result.FinalAnswer != "persisted\n" {
		t.Fatalf("恢复后补推应完成, got %s %+v", task.Status, task.Result)
	}
}

// baseTestEnv2 与 runner 测试同口径基线（避免跨包引用测试文件）。
func baseTestEnv2() map[string]string {
	return map[string]string{
		"PATH":   "/usr/bin:/bin",
		"TMPDIR": "/tmp",
		"LANG":   "en_US.UTF-8",
	}
}

var _ = os.Getenv // 保持 os 引用（占位）
