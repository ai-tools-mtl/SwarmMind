// w07_hubbind_test.go 中心空间绑定登记与收件门受理面测试（W07-02，docs/tasks/v3/
// W07-hub-routing.md 工作块3「Node 收件门受理面扩展：hub 下发前登记 session↔
// hub-scope 绑定，node 按绑定放行——W07-01 拒绝面预留的扩展点」）：
//   - space_bind 帧受理：持久登记（HandleFrame 与真实帧路由同口）；
//   - 登记后携相符 hub scope 的轮放行（create 轮经 sessionx 内核真实执行）；
//   - 未登记/异 scope 一律 fail-closed 拒绝（绑定不符话术族，登记态差异如实分述
//     不互冒——W07-01「绑定不符」人话锚保持）；
//   - 登记会话的普通轮（无 Space）执行口拒收（spaceTurnAllowed 扩展——中心空间
//     调度单入口与 hub 侧会话轮守卫两端同规）；
//   - 登记表持久化（重开可见）；损坏拒绝打开。
package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// injectHubBind 向 fixture 服务注入中心绑定登记表（真实装配路径 cmd/node 同款：
// OpenHubBindings(dir) → Config.HubBindings；fixture 既有装配不动，只加 W07-02
// registry 段）。
func injectHubBind(t *testing.T, f *spaceSvcFixture) *HubBindings {
	t.Helper()
	bindDir := filepath.Join(f.dir, "binds") // 生产为 cfg.DataDir（恒已存在）；测试自建
	if err := os.MkdirAll(bindDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binds, err := OpenHubBindings(bindDir)
	if err != nil {
		t.Fatalf("OpenHubBindings: %v", err)
	}
	f.svc.hubBind = binds // 同包装配注入（装配期一次性，先于任何帧处理——与 New 读 Config 同位）
	return binds
}

// sendSpaceBind 直发 space_bind 帧（hub 下发词形；HandleFrame 与真实帧路由同口）。
func sendSpaceBind(t *testing.T, svc *Service, sid, scope string) {
	t.Helper()
	b, err := jsonMarshal(protocol.SpaceBindPayload{Space: scope, SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	svc.HandleFrame(&protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgSpaceBind,
		MessageID: "bind-mid-" + protocol.NewMessageID(),
		From:      "hub",
		Payload:   b,
	})
}

// execHubSpaceTurn 直投一笔携 hub scope 的会话轮（execute 入口与真实任务链同口）。
// op=create 时携 cwd（首轮形态）；返回任务 ID。
func execHubSpaceTurn(t *testing.T, svc *Service, led *ledger.Ledger, sid, scope, op string, cwd string, seq int64) string {
	t.Helper()
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "中心空间轮"}, SubmittedBy: "hub",
		IdemKey: "hubsp:" + protocol.NewMessageID(), Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: op, Cwd: cwd, Space: scope},
	}
	if err := led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatal(err)
	}
	svc.execute(task)
	return task.ID
}

// waitTurnCompleted 等任务落 completed 终态（有界轮询，非 fixed sleep）。
func waitTurnCompleted(t *testing.T, led *ledger.Ledger, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := led.Get(taskID); ok && e.State == ledger.StateFinished {
			if e.Result == nil || e.Result.Status != protocol.ResultCompleted {
				t.Fatalf("应完成: %+v", e.Result)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未落终态", taskID)
}

// gateFailureMsg 取终态失败人话（waitGateFailure 断言前的分锚读取）。
func gateFailureMsg(t *testing.T, led *ledger.Ledger, taskID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := led.Get(taskID); ok && e.State == ledger.StateFinished && e.Result != nil {
			return e.Result.Error
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未落终态", taskID)
	return ""
}

// TestW07SpaceBindRegistersAndPersists space_bind 受理：登记持久落盘；重开登记表
// 绑定可见（重启后中心空间续轮仍可核对——绑定随会话生命周期）。
func TestW07SpaceBindRegistersAndPersists(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	binds := injectHubBind(t, f)
	bindDir := filepath.Join(f.dir, "binds")
	scope := collab.HubScope("sp-center-1")
	sid := "s-bindtarget1"
	sendSpaceBind(t, f.svc, sid, scope)
	e, ok := binds.Get(sid)
	if !ok {
		t.Fatal("space_bind 应登记绑定")
	}
	if e.Space != scope {
		t.Fatalf("登记 scope 不符: %s", e.Space)
	}
	// 非中心词形拒绝（本机空间 ID 不经中心登记——space_bind 只收 hub:<id>）。
	sendSpaceBind(t, f.svc, "s-bindtarget2", "sp-local-9")
	if _, ok := binds.Get("s-bindtarget2"); ok {
		t.Fatal("非中心词形不得登记")
	}
	// 重开登记表：持久化恢复。
	binds2, err := OpenHubBindings(bindDir)
	if err != nil {
		t.Fatalf("重开登记表: %v", err)
	}
	if e2, ok := binds2.Get(sid); !ok || e2.Space != scope {
		t.Fatalf("登记应持久化: %+v ok=%v", e2, ok)
	}
}

// TestW07HubScopeTurnAllowedAfterBind 登记后携相符 hub scope 的 create 轮放行
//（收件门核对登记绑定 → sessionx 内核真实执行完成）。
func TestW07HubScopeTurnAllowedAfterBind(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	binds := injectHubBind(t, f)
	sid := "s-hubturn1"
	scope := collab.HubScope("sp-center-1")
	if err := binds.Put(sid, scope, protocol.NowUTC()); err != nil {
		t.Fatalf("登记: %v", err)
	}
	taskID := execHubSpaceTurn(t, f.svc, f.led, sid, scope, protocol.SessionTurnOpCreate, f.cwd, 1)
	waitTurnCompleted(t, f.led, taskID)
}

// TestW07HubScopeTurnRejectedWrongOrMissing 绑定不符族：异 scope（登记了别的 hub
// 空间）与未登记两形态都拒绝，「绑定不符」词族保持（W07-01 人话锚），登记态差异
// 如实分述（重新登记指路 / 未登记）不互冒。
func TestW07HubScopeTurnRejectedWrongOrMissing(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	binds := injectHubBind(t, f)
	sidBound := "s-hubbound1"
	sidNone := "s-hubnone1"
	if err := binds.Put(sidBound, collab.HubScope("sp-center-1"), protocol.NowUTC()); err != nil {
		t.Fatalf("登记: %v", err)
	}
	// 异 scope：登记 hub:x，轮冒充 hub:y。
	taskID := execHubSpaceTurn(t, f.svc, f.led, sidBound, collab.HubScope("sp-center-2"), protocol.SessionTurnOpSend, "", 2)
	waitGateFailure(t, f.led, taskID, protocol.CodeSpacesUnsupported, "绑定不符")
	if msg := gateFailureMsg(t, f.led, taskID); !strings.Contains(msg, "重新 space_bind 登记") {
		t.Fatalf("异 scope 拒绝话术应注记登记态: %s", msg)
	}
	// 未登记：词形合法但登记表无此会话。
	taskID = execHubSpaceTurn(t, f.svc, f.led, sidNone, collab.HubScope("sp-center-1"), protocol.SessionTurnOpSend, "", 2)
	waitGateFailure(t, f.led, taskID, protocol.CodeSpacesUnsupported, "绑定不符")
	if msg := gateFailureMsg(t, f.led, taskID); !strings.Contains(msg, "未登记或绑定已变更") {
		t.Fatalf("未登记拒绝话术应如实分述: %s", msg)
	}
}

// TestW07HubBoundSessionRejectsPlainTurns 执行口守卫扩展：登记会话的普通轮（无
// Space 标记）拒绝并指向空间派发——中心空间调度单入口（hub 侧会话轮守卫两端同
// 规）；携相符 scope 的同会话轮放行（两守卫不互斥）。
func TestW07HubBoundSessionRejectsPlainTurns(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	binds := injectHubBind(t, f)
	sid := "s-hubplain1"
	scope := collab.HubScope("sp-center-1")
	if err := binds.Put(sid, scope, protocol.NowUTC()); err != nil {
		t.Fatalf("登记: %v", err)
	}
	// 先经登记相符的 create 轮建立内核会话（seq=1；收件门放行路径）。
	createID := execHubSpaceTurn(t, f.svc, f.led, sid, scope, protocol.SessionTurnOpCreate, f.cwd, 1)
	waitTurnCompleted(t, f.led, createID)
	// 普通续轮（无 Space 标记；seq=2 续轮形态）。
	task := protocol.Task{
		ID: protocol.NewTaskID(), TraceID: protocol.NewTraceID(), TargetNode: "n1", TargetAgent: "coder",
		Input: protocol.TaskInput{Stdin: "普通续轮旁路"}, SubmittedBy: "hub",
		IdemKey: "hubplain:" + protocol.NewMessageID(), Status: protocol.StatusSubmitted,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
		ExpireAt: protocol.NowUTC().Add(protocol.TaskRetention),
		Session:  &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 2, Op: protocol.SessionTurnOpSend},
	}
	if err := f.led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatalf("入账: %v", err)
	}
	f.svc.execute(task)
	waitGateFailure(t, f.led, task.ID, collab.CodeSpaceDispatchRequired, "空间派发")
	// 携相符 scope 的同会话轮放行。
	taskID := execHubSpaceTurn(t, f.svc, f.led, sid, scope, protocol.SessionTurnOpSend, "", 3)
	waitTurnCompleted(t, f.led, taskID)
}

// TestW07HubBindCorruptRefusesOpen 登记表损坏拒绝打开（执行准入证据——宁拒启不
// 瞎门，同 spaces.json 口径；cmd/node 装配路径拒绝启动）。
func TestW07HubBindCorruptRefusesOpen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hubBindFileName), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHubBindings(dir); err == nil {
		t.Fatal("损坏登记表应拒绝打开")
	}
}
