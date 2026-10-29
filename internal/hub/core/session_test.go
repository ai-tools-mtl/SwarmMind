package core

// session_test.go 会话注册表与轮次编排单测（T2，SPEC 六.五）：创建校验矩阵、
// 操作类型账本推导（首轮失败重试仍 create）、显式序号幂等重放/冲突/跳号、首轮
// 独占、绑定守卫（能力卡漂移 409）、轮终回写三路径（result/cancel/sweep）、
// close 幂等与派发。

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// sessionFixture membus 全链基座：node 端点可收帧、hub 可回结果（真实投递链）。
// membus 同步分发（Send 即调对端 handler），node 订阅必须先于任何派发——
// fixture 构造期一次注册，offers 通道收集（容量 16 吸收多轮）。
type sessionFixture struct {
	t      *testing.T
	core   *Core
	bus    *transport.Bus
	node   *transport.Endpoint
	store  *store.Store
	offers chan *protocol.Envelope
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	f := &sessionFixture{t: t, core: c, bus: bus, node: bus.Endpoint("mac-mini"),
		store: st, offers: make(chan *protocol.Envelope, 16)}
	f.node.Subscribe(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgTaskOffer {
			select {
			case f.offers <- env:
			default:
			}
		}
	})
	return f
}

// registerSessionNode 注册带会话能力的 node（默认在线）。
func (f *sessionFixture) registerSessionNode(adapter, perm string, online bool) {
	f.t.Helper()
	card := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: perm, ProtocolVer: 1, Exposed: true,
		SessionAdapter: adapter}
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{card}); err != nil {
		f.t.Fatalf("注册: %v", err)
	}
	f.core.SetOnline("mac-mini", online)
}

// waitTurnTerminal 轮询会话详情直到末轮终态（投递链异步，bounded 等待）。
func (f *sessionFixture) waitTurnTerminal(sessionID string, wantStatus string) SessionView {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sv, ok := f.core.GetSession(sessionID)
		if ok && len(sv.Turns) > 0 {
			last := sv.Turns[len(sv.Turns)-1]
			if last.Terminal() {
				if last.Status != wantStatus {
					f.t.Fatalf("末轮终态应 %s, got %s（error=%s）", wantStatus, last.Status, last.Error)
				}
				return *sv
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("轮次 3s 内未终态 session=%s", sessionID)
	return SessionView{}
}

// waitOffer 收节点端点的下一帧（bounded；订阅在 fixture 构造期已就位）。
func (f *sessionFixture) waitOffer() *protocol.Envelope {
	f.t.Helper()
	select {
	case env := <-f.offers:
		return env
	case <-time.After(3 * time.Second):
		f.t.Fatal("3s 内未收到 task_offer")
		return nil
	}
}

// TestSessionCreateValidation 创建校验矩阵：缺字段 400 / 未接入 404 / 无会话
// 声明 422 / 全权档位 400 / 权限断言不符 409（errors.Is 判定，HTTP 映射归 api 层）。
func TestSessionCreateValidation(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", true)

	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp/x"}, "admin"); err != nil {
		t.Fatalf("合法创建应成功: %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder"}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("缺 cwd 应 ErrInvalid, got %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "ghost", AgentName: "coder", Cwd: "/tmp"}, "admin"); !errors.Is(err, ErrAgentNotExposed) {
		t.Fatalf("未知节点应 ErrAgentNotExposed, got %v", err)
	}
	// 无会话能力：注册普通卡
	if err := f.core.RegisterNode("plain", []protocol.AgentCard{{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", ProtocolVer: 1, Exposed: true}}); err != nil {
		t.Fatalf("注册 plain: %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "plain", AgentName: "coder", Cwd: "/tmp"}, "admin"); !errors.Is(err, ErrSessionAgentUnsupported) {
		t.Fatalf("无声明应 ErrSessionAgentUnsupported, got %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", PermissionMode: "yolo"}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("yolo 断言应 ErrInvalid, got %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", PermissionMode: "danger-full-access"}, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("danger-full-access 断言应 ErrInvalid, got %v", err)
	}
	if _, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp", PermissionMode: "read-only"}, "admin"); !errors.Is(err, ErrSessionBindingChanged) {
		t.Fatalf("断言不符应 ErrSessionBindingChanged, got %v", err)
	}
}

// TestSessionTurnOpDerivation 操作类型账本推导：create 失败（NativeIDOk=false 终
// 态轮）后重试仍 create；create 成立后恒 send。显式序号重放/冲突/跳号/首轮独占。
func TestSessionTurnOpDerivation(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", true)
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp/proj"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	sid := sv.ID

	// 首轮受理：create、seq=1、offer 携带 cwd 与权限快照
	ref, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "首轮"}, "admin")
	if err != nil || dup {
		t.Fatalf("首轮提交: ref=%+v dup=%v err=%v", ref, dup, err)
	}
	if ref.Op != protocol.SessionTurnOpCreate || ref.TurnSeq != 1 {
		t.Fatalf("首轮应 create#1: %+v", ref)
	}
	offer := f.waitOffer()
	var offered protocol.Task
	if err := json.Unmarshal(offer.Payload, &offered); err != nil || offered.Session == nil {
		t.Fatalf("offer 载荷解析: %v", err)
	}
	if offered.Session.SessionID != sid || offered.Session.Op != protocol.SessionTurnOpCreate ||
		offered.Session.Cwd != "/tmp/proj" || offered.Session.PermissionMode != "workspace-write" {
		t.Fatalf("offer 身份段异常: %+v", offered.Session)
	}
	if offered.Input.Stdin != "首轮" || offered.IdemKey != turnIdemKey(sid, 1) {
		t.Fatalf("offer 输入/幂等键异常: stdin=%q idem=%q", offered.Input.Stdin, offered.IdemKey)
	}

	// 首轮在途：新轮拒绝（首轮独占）
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "第二条"}, "admin"); !errors.Is(err, ErrSessionTurnInFlight) {
		t.Fatalf("首轮在途应 ErrSessionTurnInFlight, got %v", err)
	}

	// 首轮失败终态（模拟 node 侧失败：无原生映射）
	f.core.HandleFrame(resultFrame(f.node, offered.ID, protocol.ResultFailed, &protocol.TaskResult{
		Status: protocol.ResultFailed, ExitCode: 4, Error: "turn_failed: exit 4", FinishedAt: protocol.NowUTC(),
		Turn: &protocol.TurnOutcome{TurnSeq: 1, NativeIDOk: false, WorkResult: protocol.WorkUnverified},
	}))
	view := f.waitTurnTerminal(sid, store.TurnStatusFailed)
	if view.Turns[0].Error == "" || view.Turns[0].NativeIDOk {
		t.Fatalf("失败轮留痕异常: %+v", view.Turns[0])
	}

	// 重试仍 create（无成功首轮），序号接 2；同号异文冲突、同号同文重放 dup
	ref2, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "重试首轮", TurnSeq: 2}, "admin")
	if err != nil || ref2.Op != protocol.SessionTurnOpCreate || ref2.TurnSeq != 2 {
		t.Fatalf("重试应 create#2: ref=%+v err=%v", ref2, err)
	}
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "不同内容", TurnSeq: 2}, "admin"); !errors.Is(err, ErrSessionTurnConflict) {
		t.Fatalf("同号异文应 ErrSessionTurnConflict, got %v", err)
	}
	ref2b, dup, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "重试首轮", TurnSeq: 2}, "admin")
	if err != nil || !dup || ref2b.TaskID != ref2.TaskID {
		t.Fatalf("同号同文重放应 dup 返原轮: ref=%+v dup=%v err=%v", ref2b, dup, err)
	}
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "跳号", TurnSeq: 9}, "admin"); !errors.Is(err, ErrSessionTurnConflict) {
		t.Fatalf("跳号应 ErrSessionTurnConflict, got %v", err)
	}

	// 首轮成功后：后续轮恒 send
	f.core.HandleFrame(resultFrame(f.node, ref2.TaskID, protocol.ResultCompleted, &protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "echo:重试首轮", FinishedAt: protocol.NowUTC(),
		Turn: &protocol.TurnOutcome{TurnSeq: 2, NativeIDOk: true, WorkResult: protocol.WorkUnverified,
			Usage: protocol.TurnUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
	}))
	view = f.waitTurnTerminal(sid, store.TurnStatusUnverified)
	if view.Turns[1].Response != "echo:重试首轮" || !view.Turns[1].NativeIDOk || view.Turns[1].Usage.TotalTokens != 15 {
		t.Fatalf("unverified 轮留痕异常: %+v", view.Turns[1])
	}
	ref3, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "续聊"}, "admin")
	if err != nil || ref3.Op != protocol.SessionTurnOpSend || ref3.TurnSeq != 3 {
		t.Fatalf("成功后应 send#3: ref=%+v err=%v", ref3, err)
	}
}

// TestSessionTurnOfflineStaging 离线暂存补投（任务书 DoD2）：node 离线提交轮 →
// pending 投影 → 上线即补投（同一轮任务 offer 送达）。
func TestSessionTurnOfflineStaging(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", false) // 离线
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建（离线亦可登记）: %v", err)
	}
	ref, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "离线轮"}, "admin")
	if err != nil {
		t.Fatalf("离线提交应入信箱: %v", err)
	}
	// 投影 pending（信箱排队）
	sv2, ok := f.core.GetSession(sv.ID)
	if !ok || sv2.Turns[0].Status != store.TurnStatusPending {
		t.Fatalf("离线轮应投影 pending: %+v", sv2)
	}
	// 上线即补投
	f.core.SetOnline("mac-mini", true)
	offer := f.waitOffer()
	var offered protocol.Task
	if err := json.Unmarshal(offer.Payload, &offered); err != nil || offered.ID != ref.TaskID {
		t.Fatalf("上线补投 offer 异常: %v", err)
	}
}

// TestSessionBindingGuardDrift 绑定守卫：能力卡漂移（adapter/权限任一变更）拒绝
// 续轮（409 语义），创建时点快照不变。
func TestSessionBindingGuardDrift(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", true)
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	// 权限漂移
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "read-only", ProtocolVer: 1, Exposed: true, SessionAdapter: "codex"}}); err != nil {
		t.Fatalf("改卡: %v", err)
	}
	if _, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "x"}, "admin"); !errors.Is(err, ErrSessionBindingChanged) {
		t.Fatalf("权限漂移应拒绝, got %v", err)
	}
	// 适配器漂移（降级声明 zcode）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true, SessionAdapter: "zcode"}}); err != nil {
		t.Fatalf("改卡: %v", err)
	}
	if _, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "x"}, "admin"); !errors.Is(err, ErrSessionBindingChanged) {
		t.Fatalf("适配器漂移应拒绝, got %v", err)
	}
	// 能力下线（卡撤会话声明 → SessionAdapter=""）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true}}); err != nil {
		t.Fatalf("改卡: %v", err)
	}
	if _, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "x"}, "admin"); !errors.Is(err, ErrSessionBindingChanged) {
		t.Fatalf("声明撤销应拒绝（快照漂移）, got %v", err)
	}
}

// TestSessionCloseFlow 关闭流：active→closed + close 任务派发（offer 携 close
// op、不占轮号）；重复 close 幂等 dup 零新任务；closed 后续轮 409；close 任务
// 终态不写轮次账本。
func TestSessionCloseFlow(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", true)
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	sid := sv.ID
	closed, dup, err := f.core.CloseSession(sid, "admin")
	if err != nil || dup || closed.Status != store.SessionStatusClosed || closed.ClosedAt == nil {
		t.Fatalf("关闭: sv=%+v dup=%v err=%v", closed, dup, err)
	}
	// 重复 close：幂等回执
	if _, dup2, err := f.core.CloseSession(sid, "admin"); err != nil || !dup2 {
		t.Fatalf("重复关闭应幂等 dup, err=%v", err)
	}
	// closed 后续轮拒绝
	if _, _, err := f.core.SubmitTurn(sid, SessionTurnRequest{Prompt: "x"}, "admin"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("关闭后续轮应 ErrSessionClosed, got %v", err)
	}
	// close 任务派发：offer 携 close 身份段、不进轮次账本
	offer := f.waitOffer()
	var offered protocol.Task
	if err := json.Unmarshal(offer.Payload, &offered); err != nil || offered.Session == nil {
		t.Fatalf("close offer 解析: %v", err)
	}
	if offered.Session.Op != protocol.SessionTurnOpClose || offered.Session.TurnSeq != 0 ||
		offered.IdemKey != closeIdemKey(sid) {
		t.Fatalf("close 任务身份段异常: %+v idem=%s", offered.Session, offered.IdemKey)
	}
	// close 任务终态：completed、注册表不变、轮次账本零增长
	f.core.HandleFrame(resultFrame(f.node, offered.ID, protocol.ResultCompleted, &protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: protocol.NowUTC(),
		Turn: &protocol.TurnOutcome{NativeIDOk: true, WorkResult: protocol.WorkUnverified},
	}))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, ok := f.core.GetTask(offered.ID)
		if ok && tk.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	final, ok := f.core.GetSession(sid)
	if !ok || len(final.Turns) != 0 || final.Status != store.SessionStatusClosed {
		t.Fatalf("close 终态不得写轮次账本: turns=%d status=%s", len(final.Turns), final.Status)
	}
}

// TestSessionCancelWriteback 管理面取消在途轮：轮行 failed 留痕（error 携取消
// 码），NativeIDOk=false（T2 轮状态机无不确定值，SPEC 六.五）。
func TestSessionCancelWriteback(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", true)
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	ref, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "会被取消"}, "admin")
	if err != nil {
		t.Fatalf("提交: %v", err)
	}
	f.waitOffer() // 已派发
	if _, _, err := f.core.CancelTask(ref.TaskID, "管理面取消"); err != nil {
		t.Fatalf("取消: %v", err)
	}
	view := f.waitTurnTerminal(sv.ID, store.TurnStatusFailed)
	if view.Turns[0].NativeIDOk || view.Turns[0].Error == "" {
		t.Fatalf("取消轮留痕异常: %+v", view.Turns[0])
	}
}

// TestSessionSweepExpiryWriteback 过期清扫（sweep 路径）：7d 过期轮 failed/
// expired 留痕（writebackSessionTurn 第三终态路径）。
func TestSessionSweepExpiryWriteback(t *testing.T) {
	f := newSessionFixture(t)
	f.registerSessionNode("codex", "workspace-write", false) // 离线滞留至过期
	sv, err := f.core.CreateSession(SessionCreateRequest{NodeID: "mac-mini", AgentName: "coder", Cwd: "/tmp"}, "admin")
	if err != nil {
		t.Fatalf("创建: %v", err)
	}
	ref, _, err := f.core.SubmitTurn(sv.ID, SessionTurnRequest{Prompt: "永不到达"}, "admin")
	if err != nil {
		t.Fatalf("提交: %v", err)
	}
	// 时钟推进过 ExpireAt 后手动 Sweep（SweepInterval=0 不自动）
	f.core.now = func() time.Time { return protocol.NowUTC().Add(protocol.TaskRetention + time.Hour) }
	f.core.Sweep()
	view := f.waitTurnTerminal(sv.ID, store.TurnStatusFailed)
	if view.Turns[0].TaskID != ref.TaskID || view.Turns[0].Error != protocol.CodeExpired {
		t.Fatalf("过期轮留痕异常: %+v", view.Turns[0])
	}
}

// resultFrame 构造 node 身份的 task_result 帧（真实会话路径：HandleFrame 入口）。
func resultFrame(ep *transport.Endpoint, taskID, status string, res *protocol.TaskResult) *protocol.Envelope {
	payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: taskID, Status: status, Result: res})
	return &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), From: "mac-mini", To: "hub",
		FenceEpoch: 0, // membus 等价链路：缺省宽容
		Payload:    payload,
	}
}
