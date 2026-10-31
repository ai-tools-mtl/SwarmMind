package service

// sessionturn_p6_hubloop_test.go P6 全链回归（第三轮采证 FAIL：hub 面异步取消对执行
// 中轮不生效）：真 hub core（store+transport）+ 真 node service（sessionx fake CLI +
// sessionop）经 membus 全链对接——CreateSession/SubmitTurn → offer 链受理执行 →
// CancelSessionTurn（路径 3 异步取消任务）→ 断言目标进程组被终止（executed 回执 +
// 终态时刻断言；被动等待形态=等满 SLEEP 后 turn_already_finished、轮行 unverified）。
// 根因（本测试锁定）：hub 派发门控 workdirFree 按目标 agent 注册卡 WorkspaceID 取
// 键，取消任务与目标轮同 agent 同键、目标轮自 offered 起持锁至终态——取消任务滞留
// hub 信箱至自然完成（node 侧调度旁路的 hub 半边缺失）。修复=hub 派发门控/占锁/排队
// 超时/重启补占四面豁免 sessionCancelOp。

import (
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

func TestP6HubDispatchedCancelTerminatesRunningTurn(t *testing.T) {
	bus := transport.NewBus()
	hubEP := bus.Endpoint("hub")
	nodeEP := bus.Endpoint("n1")

	// ---- hub：真 store + 真 core（dispatch/result 全链真实） ----
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	coreSvc, err := core.New(core.Config{Store: st, Transport: hubEP})
	if err != nil {
		t.Fatal(err)
	}
	hubEP.Subscribe(coreSvc.HandleFrame)
	// 注册卡携 WorkspaceID（生产 node 上报形态）——P6 缺陷只有在互斥键在案时才可复现
	cards := []protocol.AgentCard{{
		Name: "coder", Exposed: true, SessionAdapter: protocol.SessionAdapterCodex,
		PermissionMode: "workspace-write", WorkspaceID: "ws-p6",
	}}
	if err := coreSvc.RegisterNode("n1", cards); err != nil {
		t.Fatal(err)
	}
	coreSvc.SetOnline("n1", true)

	// ---- node：真 service（fake CLI 10s 长跑 + sessionop） ----
	led, _, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	cwd := t.TempDir()
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
		"FAKE_THREAD_ID=thr-p6-1", "FAKE_SLEEP=10", "FAKE_LOG=" + t.TempDir() + "/run.log"}
	mgr, err := sessionx.NewManager(t.TempDir(), map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: buildSessionFakeCLI(t),
		PermissionMode: "workspace-write", TimeoutSec: 60, Env: env,
	}}, sessionx.Options{})
	if err != nil {
		t.Fatalf("装配内核: %v", err)
	}
	ops, err := sessionop.New(t.TempDir(), led, sessionop.Options{})
	if err != nil {
		t.Fatalf("装配 sessionop: %v", err)
	}
	svc := New(Config{
		Ledger:        led,
		Runners:       map[string]Runner{},
		ArtifactRoots: map[string]string{"coder": t.TempDir()},
		Sessions:      mgr,
		SessionOps:    ops,
		Concurrency:   2,
	}, nodeEP)
	nodeEP.Subscribe(svc.HandleFrame)
	t.Cleanup(svc.Close)

	// ---- hub 建会话 + 投 10s 长轮（offer 全链下发 node 执行） ----
	sv, _, err := coreSvc.CreateSession(core.SessionCreateRequest{
		NodeID: "n1", AgentName: "coder", Cwd: cwd,
	}, "admin")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	sid := sv.Session.ID
	ref, _, err := coreSvc.SubmitTurn(sid, core.SessionTurnRequest{Prompt: "P6 长轮"}, "admin")
	if err != nil {
		t.Fatalf("投轮: %v", err)
	}
	// 等 hub 视角任务 running（node task_started 帧回链；ListSessions.LastTurn 投影）
	deadline := time.Now().Add(10 * time.Second)
	for {
		var running bool
		for _, sum := range coreSvc.ListSessions() {
			if sum.ID == sid && sum.LastTurn != nil && sum.LastTurn.Status == "running" {
				running = true
			}
		}
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s 内目标轮未进入 running")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ---- 执行中取消（路径 3 异步取消任务）并断言进程组终止时刻 ----
	t0 := time.Now()
	rc, async, err := coreSvc.CancelSessionTurn(sid, ref.TurnSeq, "P6 采证复现", "admin")
	if err != nil || !async {
		t.Fatalf("执行中取消应异步受理: rc=%+v async=%v err=%v", rc, async, err)
	}
	// 轮行终态断言：3s 内落 cancelled/turn_cancelled_executed（被动等待形态=约 7s 后
	// 自然完成 unverified，取消任务回执 turn_already_finished）
	rowDeadline := time.Now().Add(3 * time.Second)
	var status, rowErr string
	for time.Now().Before(rowDeadline) {
		for _, sum := range coreSvc.ListSessions() {
			if sum.ID == sid && sum.LastTurn != nil && sum.LastTurn.Terminal() {
				status, rowErr = sum.LastTurn.Status, sum.LastTurn.Error
			}
		}
		if status != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != store.TurnStatusCancelled || !strings.HasPrefix(rowErr, "turn_cancelled_executed") {
		t.Fatalf("P6: 执行中取消应 3s 内落 cancelled/turn_cancelled_executed，得 status=%s err=%q（耗时 %v）", status, rowErr, time.Since(t0))
	}
	// 取消任务自身回执：executed（node CancelTurn 收口）
	ct := st.View().Tasks[rc.CancelTaskID]
	if ct.Result == nil || ct.Result.Status != protocol.ResultCancelled || !strings.HasPrefix(ct.Result.Error, "turn_cancelled_executed") {
		t.Fatalf("取消任务回执应 executed: %+v", ct.Result)
	}
	t.Logf("P6 全链取消收口耗时 %v（进程组终止时刻断言 <3s）", time.Since(t0))
}
