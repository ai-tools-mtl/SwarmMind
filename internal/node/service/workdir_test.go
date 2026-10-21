package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/agentid"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// ---------- P1.5 workdir 互斥（SPEC 4.10，node 侧为准）：串行 / 并行 / 重启重建 ----------

// tsRunner 记录起止时刻、可按任务阻塞的桩执行器：Run 进入即记 StartedAt，等待
// 放行门（未设门即直通），返回前记 FinishedAt——时刻均为真实墙钟（串行断言依据）。
type tsRunner struct {
	mu       sync.Mutex
	gates    map[string]chan struct{}
	entered  map[string]time.Time
	finished map[string]time.Time
}

func newTsRunner() *tsRunner {
	return &tsRunner{
		gates:    map[string]chan struct{}{},
		entered:  map[string]time.Time{},
		finished: map[string]time.Time{},
	}
}

// gate 设置任务的放行门（阻塞 Run 直至 release）。
func (r *tsRunner) gate(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gates[taskID] = make(chan struct{})
}

// release 放行任务（未设门为 no-op）。
func (r *tsRunner) release(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.gates[taskID]; ok {
		close(ch)
		delete(r.gates, taskID)
	}
}

func (r *tsRunner) enteredAt(taskID string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entered[taskID]
}

func (r *tsRunner) finishedAt(taskID string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished[taskID]
}

func (r *tsRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	start := protocol.NowUTC()
	r.mu.Lock()
	r.entered[task.ID] = start
	ch := r.gates[task.ID]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done(): // 服务关闭：仍按常规返回（service 层会覆写 needs_review）
		}
	}
	fin := protocol.NowUTC()
	r.mu.Lock()
	r.finished[task.ID] = fin
	r.mu.Unlock()
	return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, StartedAt: start, FinishedAt: fin}
}

// newSvcWithRoots 装配带 workspace 派生源（ArtifactRoots）的服务：单一桩执行器
// 承接全部 agent 名，假 hub 只录制。
func newSvcWithRoots(t *testing.T, r Runner, concurrency int, roots map[string]string) (*frameRec, *transport.Endpoint) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	f := &frameRec{ep: bus.Endpoint("hub")}
	nodeEP := bus.Endpoint("n1")
	runners := map[string]Runner{"seq": r, "alt": r}
	f.svc = New(Config{Ledger: led, Runners: runners, Concurrency: concurrency, ArtifactRoots: roots}, nodeEP)
	nodeEP.Subscribe(f.svc.HandleFrame)
	f.ep.Subscribe(func(env *protocol.Envelope) {
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		f.mu.Unlock()
	})
	t.Cleanup(f.svc.Close)
	return f, nodeEP
}

// wsTask 构造目标 agent 的 offer 任务。
func wsTask(id, agent string) protocol.Task {
	return protocol.Task{
		ID: id, TraceID: "tr", TargetNode: "n1", TargetAgent: agent,
		Input: protocol.TaskInput{Args: []string{"x"}}, SubmittedBy: "hub",
		Status: protocol.StatusOffered,
	}
}

// TestNodeWorkdirSerialTimestamps node 侧串行（DoD 增补①执行侧断言）：同
// workspace 两任务并发到达，第二任务进入执行（started）不得早于第一任务结果
// 落盘前的 FinishedAt——互斥占用覆盖 spawn 前准备到结果落盘。
func TestNodeWorkdirSerialTimestamps(t *testing.T) {
	r := newTsRunner()
	r.gate("t-s1")
	shared := filepath.Join(t.TempDir(), "ws")
	f, hubEP := newSvcWithRoots(t, r, 2, map[string]string{"seq": shared})

	t1, t2 := wsTask("t-s1", "seq"), wsTask("t-s2", "seq")
	bigOffer(t, hubEP, t1)
	bigOffer(t, hubEP, t2)

	// 第一任务进入执行；互斥生效窗口内第二任务不得进入
	if !waitFor(2*time.Second, func() bool { return !r.enteredAt("t-s1").IsZero() }) {
		t.Fatal("第一任务应进入执行")
	}
	time.Sleep(150 * time.Millisecond)
	if !r.enteredAt("t-s2").IsZero() {
		t.Fatal("互斥中第二任务不得进入执行")
	}

	r.release("t-s1")
	if !waitFor(3*time.Second, func() bool {
		return !r.finishedAt("t-s1").IsZero() && !r.enteredAt("t-s2").IsZero()
	}) {
		t.Fatal("放行后第二任务应进入执行")
	}
	// DoD 断言：第二任务 started ≥ 第一任务 finished
	if r.enteredAt("t-s2").Before(r.finishedAt("t-s1")) {
		t.Fatalf("串行断言失败: 第二 started(%v) < 第一 finished(%v)",
			r.enteredAt("t-s2"), r.finishedAt("t-s1"))
	}
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get("t-s2")
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("第二任务应落终态")
	}
}

// TestNodeWorkdirDifferentWorkdirParallel 异 workdir 并行（DoD 增补②）：不同
// workspace 两任务并发到达且互不阻塞——第二任务在第一任务仍在执行时进入
// （时间区间重叠）。
func TestNodeWorkdirDifferentWorkdirParallel(t *testing.T) {
	r := newTsRunner()
	r.gate("t-p1")
	base := t.TempDir()
	_, hubEP := newSvcWithRoots(t, r, 2, map[string]string{
		"seq": filepath.Join(base, "ws-a"),
		"alt": filepath.Join(base, "ws-b"),
	})

	t1, t2 := wsTask("t-p1", "seq"), wsTask("t-p2", "alt")
	bigOffer(t, hubEP, t1)
	bigOffer(t, hubEP, t2)

	// 两任务同时在执行（t-p1 未放行即 t-p2 进入 → 并行成立）
	if !waitFor(2*time.Second, func() bool {
		return !r.enteredAt("t-p1").IsZero() && !r.enteredAt("t-p2").IsZero()
	}) {
		t.Fatalf("异 workdir 任务应并行进入执行: p1=%v p2=%v", r.enteredAt("t-p1"), r.enteredAt("t-p2"))
	}
	r.release("t-p1")
	if !waitFor(3*time.Second, func() bool {
		return !r.finishedAt("t-p1").IsZero() && !r.finishedAt("t-p2").IsZero()
	}) {
		t.Fatal("两任务应完成")
	}
	// 区间相交验证（P1.5 二轮建议修）：[p1 entered,finished] 与 [p2 entered,finished]
	// 必须相交——p2 started 不得晚于 p1 finished，且 p1 started 不得晚于 p2 finished
	if r.enteredAt("t-p2").After(r.finishedAt("t-p1")) || r.enteredAt("t-p1").After(r.finishedAt("t-p2")) {
		t.Fatalf("并行断言失败：执行区间未相交 p1=[%v,%v] p2=[%v,%v]",
			r.enteredAt("t-p1"), r.finishedAt("t-p1"), r.enteredAt("t-p2"), r.finishedAt("t-p2"))
	}
}

// TestNodeRestartMutexRebuild node 重启互斥表账本重建（DoD 增补⑤ node 侧）：
// accepted 未终态条目重启后占锁（Reschedule 交回执行前即生效）；started 条目
// 已在账本恢复期收敛为 needs_review 终态、不占锁。
func TestNodeRestartMutexRebuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	workdir := filepath.Join(t.TempDir(), "ws")

	// 第一代：两个 accepted 条目（同 agent 同 workdir）+ 一个 started 条目
	led1, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t1, t2, t3 := wsTask("t-r1", "seq"), wsTask("t-r2", "seq"), wsTask("t-r3", "seq")
	for _, tk := range []protocol.Task{t1, t2, t3} {
		if err := led1.Accept(tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := led1.MarkStarted(t3.ID); err != nil { // started：恢复期收敛 needs_review
		t.Fatal(err)
	}
	if err := led1.Close(); err != nil {
		t.Fatal(err)
	}

	// 第二代：重开账本（恢复先于 service 装配）→ 互斥表重建
	led2, rec, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	if len(rec.Reschedule) != 2 {
		t.Fatalf("恢复清单应含 2 个 accepted 条目, got %d", len(rec.Reschedule))
	}
	r := newTsRunner()
	bus := transport.NewBus()
	nodeEP := bus.Endpoint("n1")
	// 单槽并发（P1.5 二轮建议修，暴露旧实现死锁：先抢并发槽再等互斥——等待者
	// 占住唯一槽而持有者等槽即互等）
	svc := New(Config{
		Ledger: led2, Runners: map[string]Runner{"seq": r}, Concurrency: 1,
		ArtifactRoots: map[string]string{"seq": workdir},
	}, nodeEP)
	t.Cleanup(svc.Close)

	// 重建确定序（P1.5 二轮必修2）：seed 按 (created_at, id) 遍历——最早任务
	// （同刻则 ID 最小）为持有者，不再依赖账本 map 遍历序
	ws := agentid.WorkspaceID(workdir)
	if holder := svc.wsLocks.holder(ws); holder != t1.ID {
		t.Fatalf("accepted 条目应按确定序占锁（最早优先）, holder=%q want %q", holder, t1.ID)
	}
	if holder := svc.wsLocks.holder(ws); holder == t3.ID {
		t.Fatal("started 条目恢复期已收敛终态，不得占锁")
	}

	// 重建的锁约束恢复执行：持有者占道，另一任务等待至其 finished（等待者不占
	// 并发槽，单槽下持有者必然可获得槽位执行）
	r.gate(t1.ID)
	svc.Execute(t1)
	svc.Execute(t2)
	if !waitFor(2*time.Second, func() bool { return !r.enteredAt(t1.ID).IsZero() }) {
		t.Fatalf("持有者应立即进入执行")
	}
	time.Sleep(100 * time.Millisecond)
	if !r.enteredAt(t2.ID).IsZero() {
		t.Fatal("重建锁应阻塞另一同 workdir 恢复任务")
	}
	r.release(t1.ID)
	if !waitFor(3*time.Second, func() bool {
		return !r.finishedAt(t1.ID).IsZero() && !r.finishedAt(t2.ID).IsZero()
	}) {
		t.Fatal("两恢复任务应先后完成")
	}
	if r.enteredAt(t2.ID).Before(r.finishedAt(t1.ID)) {
		t.Fatalf("恢复串行断言失败: waiter started(%v) < holder finished(%v)",
			r.enteredAt(t2.ID), r.finishedAt(t1.ID))
	}
}

// TestNodeWorkdirWaiterNotInConcurrencySlot 等待者不占并发槽（P1.5 二轮必修2）：
// 同 workdir 等待者在获授互斥前不得占用执行并发槽——异 workdir 任务须能用空余槽
// 立即执行（旧实现先抢槽后等锁：槽被等待者占住，异 workdir 任务被饿死）。
func TestNodeWorkdirWaiterNotInConcurrencySlot(t *testing.T) {
	r := newTsRunner()
	r.gate("t-w1") // 同 ws 持有者占道不放
	base := t.TempDir()
	_, hubEP := newSvcWithRoots(t, r, 2, map[string]string{
		"seq": filepath.Join(base, "ws-a"),
		"alt": filepath.Join(base, "ws-b"),
	})

	t1, t2, t3 := wsTask("t-w1", "seq"), wsTask("t-w2", "seq"), wsTask("t-w3", "alt")
	bigOffer(t, hubEP, t1)
	bigOffer(t, hubEP, t2)
	bigOffer(t, hubEP, t3)

	// 持有者 t1 进入执行；同 ws 等待者 t2 互斥滞留
	if !waitFor(2*time.Second, func() bool { return !r.enteredAt("t-w1").IsZero() }) {
		t.Fatal("第一任务应进入执行")
	}
	time.Sleep(100 * time.Millisecond)
	if !r.enteredAt("t-w2").IsZero() {
		t.Fatal("同 workdir 等待者不得进入执行")
	}
	// 等待者未占槽：异 workdir t3 借第二槽立即并行执行
	if !waitFor(2*time.Second, func() bool { return !r.enteredAt("t-w3").IsZero() }) {
		t.Fatalf("异 workdir 任务应立即进入执行（等待者不得占用并发槽）: w1=%v w2=%v w3=%v",
			r.enteredAt("t-w1"), r.enteredAt("t-w2"), r.enteredAt("t-w3"))
	}
	// 释放持有者：等待者获授互斥后完成（同 ws 严格串行）
	r.release("t-w1")
	if !waitFor(3*time.Second, func() bool {
		return !r.finishedAt("t-w1").IsZero() && !r.finishedAt("t-w2").IsZero()
	}) {
		t.Fatal("同 workdir 两任务应先后完成")
	}
	if r.enteredAt("t-w2").Before(r.finishedAt("t-w1")) {
		t.Fatalf("串行断言失败: w2 started(%v) < w1 finished(%v)",
			r.enteredAt("t-w2"), r.finishedAt("t-w1"))
	}
}

// TestCardsFromProfilesWorkspaceID 能力卡携带 workspace 互斥键（SPEC 4.10：
// node 上报派生键、路径不出本机）：与 agentid 直算一致；不同 workdir 不同键；
// 卡内无路径字段。
func TestCardsFromProfilesWorkspaceID(t *testing.T) {
	base := t.TempDir()
	wsA := filepath.Join(base, "a")
	wsB := filepath.Join(base, "b")
	cards := CardsFromProfiles([]protocol.ExecProfile{
		{Name: "a", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: wsA, TimeoutSec: 1, Exposed: true},
		{Name: "b", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: wsB, TimeoutSec: 1, Exposed: true},
	})
	if len(cards) != 2 {
		t.Fatalf("应生成 2 张卡, got %d", len(cards))
	}
	if cards[0].WorkspaceID != agentid.WorkspaceID(wsA) {
		t.Fatalf("卡片键应与 agentid 直算一致: %q vs %q", cards[0].WorkspaceID, agentid.WorkspaceID(wsA))
	}
	if cards[0].WorkspaceID == cards[1].WorkspaceID {
		t.Fatalf("不同 workdir 应不同键: %q", cards[0].WorkspaceID)
	}
	b, err := json.Marshal(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, wsA) || strings.Contains(s, "workdir") {
		t.Fatalf("能力卡不得泄露路径字段: %s", s)
	}
}
