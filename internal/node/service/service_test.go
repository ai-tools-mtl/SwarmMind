package service

import (
	"encoding/json"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// ---------- 测试脚手架：membus 内存链路（Loop 2 同口径，替代真实 WSS） ----------

// frameRec 假 hub：常驻录制全部收帧，业务回调经 handle 后挂（可随时替换）。
type frameRec struct {
	mu     sync.Mutex
	frames []*protocol.Envelope
	user   func(*protocol.Envelope)
	ep     *transport.Endpoint
	svc    *Service
}

func newFixture(t *testing.T, profiles []protocol.ExecProfile) *frameRec {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	f := &frameRec{ep: bus.Endpoint("hub")}
	runners := make(map[string]Runner, len(profiles))
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home"))
	}
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{Ledger: led, Runners: runners}, nodeEP)
	nodeEP.Subscribe(f.svc.HandleFrame)           // New 内已订阅；显式重申以对齐「最后一次为准」
	f.ep.Subscribe(func(env *protocol.Envelope) { // 常驻录制
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		user := f.user
		f.mu.Unlock()
		if user != nil {
			user(env)
		}
	})
	t.Cleanup(f.svc.Close)
	return f
}

// handle 挂载（或替换）假 hub 的业务回调；录制不受影响。
func (f *frameRec) handle(fn func(*protocol.Envelope)) {
	f.mu.Lock()
	f.user = fn
	f.mu.Unlock()
}

func (f *frameRec) ofTypes(typs ...string) []*protocol.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := map[string]bool{}
	for _, t := range typs {
		set[t] = true
	}
	var out []*protocol.Envelope
	for _, e := range f.frames {
		if set[e.Type] {
			out = append(out, e)
		}
	}
	return out
}

// sendOffer 假 hub 投递 task_offer。
func (f *frameRec) sendOffer(t *testing.T, task protocol.Task) {
	t.Helper()
	b, err := jsonMarshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", Payload: b,
	}); err != nil {
		t.Fatalf("发送 offer: %v", err)
	}
}

func echoTask(id string) protocol.Task {
	return protocol.Task{
		ID: id, TraceID: "tr", TargetNode: "n1", TargetAgent: "echo-demo",
		Input: protocol.TaskInput{Args: []string{"hello", "svc"}}, SubmittedBy: "hub",
		Status: protocol.StatusOffered,
	}
}

func echoProfile() protocol.ExecProfile {
	return protocol.ExecProfile{
		Name: "echo-demo", Kind: protocol.AgentKindCLI,
		Command: "/bin/echo", Workdir: "/tmp",
		TimeoutSec: 5, AllowArgs: true, Exposed: true,
		PermissionMode: "no-shell",
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// ---------- 用例 ----------

// TestOfferRoundTrip 一次投递全链（SPEC 4.7 顺序）：accepted → task_ack →
// started → task_started → 执行 → finished → task_result；result_ack 后账本
// result_acked 置位。
func TestOfferRoundTrip(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})
	f.handle(func(env *protocol.Envelope) {
		switch env.Type {
		case protocol.MsgTaskResult:
			var p protocol.TaskResultPayload
			if err := jsonUnmarshal(env.Payload, &p); err != nil {
				t.Errorf("解析 result: %v", err)
				return
			}
			_ = f.ep.Send("n1", &protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgResultAck,
				MessageID: protocol.NewMessageID(), Payload: mustJSON(protocol.TaskRef{TaskID: p.TaskID}),
			})
		}
	})
	task := echoTask("t-1")
	f.sendOffer(t, task)

	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.State == ledger.StateFinished && e.ResultAcked
	}) {
		t.Fatal("账本应 finished+acked")
	}
	var acks, starts, results int
	for _, e := range f.ofTypes(protocol.MsgTaskAck, protocol.MsgTaskStarted, protocol.MsgTaskResult) {
		switch e.Type {
		case protocol.MsgTaskAck:
			acks++
		case protocol.MsgTaskStarted:
			starts++
		case protocol.MsgTaskResult:
			results++
		}
	}
	if acks != 1 || starts != 1 || results != 1 {
		t.Fatalf("应各恰好 1 帧 ack/started/result, got %d/%d/%d", acks, starts, results)
	}
	res := f.ofTypes(protocol.MsgTaskResult)[0]
	var p protocol.TaskResultPayload
	if err := jsonUnmarshal(res.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != protocol.ResultCompleted || p.Result.FinalAnswer != "hello svc\n" {
		t.Fatalf("结果不符: %+v", p)
	}
}

// TestDuplicateOffer 重复 offer（SPEC 4.3）：不再执行（started 不增），重发
// task_ack；已 finished 附带重发 task_result。
func TestDuplicateOffer(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})
	task := echoTask("t-2")
	f.sendOffer(t, task)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("首次执行应完成")
	}
	f.sendOffer(t, task) // 重复投递
	if !waitFor(2*time.Second, func() bool {
		return len(f.ofTypes(protocol.MsgTaskResult)) == 2
	}) {
		t.Fatal("重复 offer 应重发 result")
	}
	if n := len(f.ofTypes(protocol.MsgTaskStarted)); n != 1 {
		t.Fatalf("重复 offer 不得再次执行, started=%d", n)
	}
}

// TestAgentNotFound 配置漂移（SPEC 4.3 收敛表）：回 task_result failed
// error=agent_not_found，不进重推循环（重复 offer 走重发分支，不执行）。
func TestAgentNotFound(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})
	task := echoTask("t-3")
	task.TargetAgent = "ghost"
	f.sendOffer(t, task)
	if !waitFor(2*time.Second, func() bool {
		return len(f.ofTypes(protocol.MsgTaskResult)) == 1
	}) {
		t.Fatal("应回 task_result")
	}
	var p protocol.TaskResultPayload
	if err := jsonUnmarshal(f.ofTypes(protocol.MsgTaskResult)[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != protocol.ResultFailed || p.Result.Error != protocol.CodeAgentNotFound {
		t.Fatalf("应 failed/agent_not_found, got %+v", p)
	}
}

// TestResultConflictStopsResend 结果被拒（SPEC 4.5）：err(result_conflict) 后
// 账本置 closed，退出重发队列（OnRegistered 不再重发）。
func TestResultConflictStopsResend(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()})
	f.handle(func(env *protocol.Envelope) {
		if env.Type == protocol.MsgTaskResult {
			_ = f.ep.Send("n1", &protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgErr,
				MessageID: protocol.NewMessageID(),
				Payload:   mustJSON(protocol.TaskRef{TaskID: "t-4"}),
				Err:       &protocol.ErrInfo{Code: protocol.CodeResultConflict, Msg: "保留先到"},
			})
		}
	})
	task := echoTask("t-4")
	f.sendOffer(t, task)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.Closed
	}) {
		t.Fatal("冲突后账本应置 closed")
	}
	f.svc.ResendPending() // 模拟重连补发：closed 条目不得重发
	time.Sleep(50 * time.Millisecond)
	if n := len(f.ofTypes(protocol.MsgTaskResult)); n != 1 {
		t.Fatalf("closed 条目不得重发, results=%d", n)
	}
}

// TestResendOnRegistered 重连补发（SPEC 4.5）：finished 未 acked 的结果在注册
// 成功后立即重发；result_ack 收到后停止。
func TestResendOnRegistered(t *testing.T) {
	f := newFixture(t, []protocol.ExecProfile{echoProfile()}) // 假 hub 不回 result_ack
	task := echoTask("t-5")
	f.sendOffer(t, task)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.State == ledger.StateFinished && len(f.ofTypes(protocol.MsgTaskResult)) == 1
	}) {
		t.Fatalf("前置：应完成并发出 1 次 result, got %d", len(f.ofTypes(protocol.MsgTaskResult)))
	}
	f.svc.ResendPending() // 模拟重连成功回调
	if !waitFor(2*time.Second, func() bool {
		return len(f.ofTypes(protocol.MsgTaskResult)) == 2
	}) {
		t.Fatal("重连后应重发未确认结果")
	}
	// 收到 result_ack 后不再重发（S2：先等账本 acked 持久化完成，消除异步竞态）
	_ = f.ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgResultAck,
		MessageID: protocol.NewMessageID(),
		Payload:   mustJSON(protocol.TaskRef{TaskID: task.ID}),
	})
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.ResultAcked
	}) {
		t.Fatal("前置：账本应已置 result_acked")
	}
	f.svc.ResendPending()
	time.Sleep(50 * time.Millisecond)
	if n := len(f.ofTypes(protocol.MsgTaskResult)); n != 2 {
		t.Fatalf("已确认结果不得重发, results=%d", n)
	}
}

// ---------- S2：可阻塞桩执行器（并发峰值观测） ----------

// gateRunner 可阻塞、可计数的桩执行器：Run 进入即计数（cur/peak），阻塞在 hold
// 上直至释放；用于证明执行并发恰受信号量限制。
type gateRunner struct {
	cur  atomic.Int64
	peak atomic.Int64
	ran  atomic.Int64
	hold chan struct{} // 关闭即放行全部
	held chan struct{} // 每次进入 Run 时发一个信号（带缓冲，不阻塞）
}

func newGateRunner(hold chan struct{}) *gateRunner {
	return &gateRunner{hold: hold, held: make(chan struct{}, 64)}
}

func (g *gateRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	n := g.cur.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	g.ran.Add(1)
	select {
	case g.held <- struct{}{}:
	default:
	}
	<-g.hold
	g.cur.Add(-1)
	return protocol.TaskResult{
		Status: protocol.ResultCompleted, ExitCode: 0,
		FinalAnswer: "done " + task.ID, FinishedAt: protocol.NowUTC(),
	}
}

// TestExecuteConcurrency 执行并发上限（node 执行并发，SPEC 4.6）：S2——用可控
// 阻塞任务记录实际执行并发峰值，证明峰值恰为上限（删除信号量即失败），且全部
// 任务无死锁收敛。
func TestExecuteConcurrency(t *testing.T) {
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	const limit = 2
	hold := make(chan struct{})
	// S2：放行幂等并挂 t.Cleanup——任一 Fatal 提前退出时也必须放行被 hold 阻塞
	// 的执行 goroutine，否则 t.Cleanup(svc.Close) 的 wg.Wait 永久挂起
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { close(hold) }) }
	t.Cleanup(release)
	g := newGateRunner(hold)
	nodeEP := bus.Endpoint("n1")
	hubEP := bus.Endpoint("hub")
	svc := New(Config{Ledger: led, Runners: map[string]Runner{"g": g}, Concurrency: limit}, nodeEP)
	t.Cleanup(svc.Close)

	// 投 6 个任务；每进入一个 Run 收到 held 信号
	var ids []string
	for i := 0; i < 6; i++ {
		id := "t-g" + string(rune('a'+i))
		ids = append(ids, id)
		b, _ := json.Marshal(protocol.Task{ID: id, TraceID: "tr", TargetNode: "n1",
			TargetAgent: "g", SubmittedBy: "hub", Status: protocol.StatusOffered})
		if err := sendOfferRaw(hubEP, b); err != nil {
			t.Fatal(err)
		}
	}
	// 阶段一：前 2 个占满槽位后放行 → 峰值必为 2（超限即失败）
	for i := 0; i < limit; i++ {
		select {
		case <-g.held:
		case <-time.After(2 * time.Second):
			t.Fatalf("仅 %d 个任务进入执行（期待 %d）", i, limit)
		}
	}
	select {
	case <-g.held: // 第 3 个不应进入（信号量已满）
		t.Fatal("超过并发上限的任务进入了执行")
	case <-time.After(150 * time.Millisecond):
	}
	release() // 放行全部
	if !waitFor(3*time.Second, func() bool {
		for _, id := range ids {
			e, ok := led.Get(id)
			if !ok || e.State != ledger.StateFinished {
				return false
			}
		}
		return true
	}) {
		t.Fatal("全部任务应执行完成")
	}
	if p := g.peak.Load(); p != limit {
		t.Fatalf("执行并发峰值应恰为 %d, got %d（信号量缺失或泄漏）", limit, p)
	}
	if g.ran.Load() != 6 {
		t.Fatalf("6 个任务应各执行一次, got %d", g.ran.Load())
	}
}

// sendOfferRaw 辅助：经端点发 task_offer（自定义载荷）。
func sendOfferRaw(ep *transport.Endpoint, payload []byte) error {
	return ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", Payload: payload,
	})
}

// TestSendOfflineSilent 未连接时发送静默（补推链路自愈，SPEC 4.5）：对端关闭后
// 发帧只记录错误，不得 panic / 阻塞。
func TestSendOfflineSilent(t *testing.T) {
	f := newFixture(t, nil)
	_ = f.ep.Close() // 模拟 hub 端点关闭
	done := make(chan struct{})
	go func() {
		f.svc.sendRef(protocol.MsgTaskAck, "t-x")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("离线发送不得阻塞")
	}
}

// ---------- N2：task_result 最终编码预算 ----------

// bigResultRunner 返回构造结果的桩执行器。
type bigResultRunner struct{ res protocol.TaskResult }

func (b bigResultRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	return b.res
}

// newSvcWithRunner 装配单一桩执行器的服务（假 hub 只录制）。
func newSvcWithRunner(t *testing.T, r Runner, concurrency int) (*frameRec, *transport.Endpoint) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	f := &frameRec{ep: bus.Endpoint("hub")}
	nodeEP := bus.Endpoint("n1")
	f.svc = New(Config{Ledger: led, Runners: map[string]Runner{"big": r}, Concurrency: concurrency}, nodeEP)
	f.ep.Subscribe(func(env *protocol.Envelope) {
		f.mu.Lock()
		cp := *env
		f.frames = append(f.frames, &cp)
		f.mu.Unlock()
	})
	t.Cleanup(f.svc.Close)
	return f, nodeEP
}

// bigOffer 投递指定任务（走 "hub" 端点）。
func bigOffer(t *testing.T, ep *transport.Endpoint, task protocol.Task) {
	t.Helper()
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Send("n1", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskOffer,
		MessageID: protocol.NewMessageID(), From: "hub", Payload: b,
	}); err != nil {
		t.Fatalf("发送 offer: %v", err)
	}
}

// TestResultOversizeEncodedTruncated N2（SPEC 4.9）：大量 '<' 使最终 JSON 编码爆帧
// （512KiB '<' 默认转义约 3MiB）→ 持久化与发送的均为截断后的同一结果，线上信封
// ≤1MiB、标 truncated、可被 result_ack 闭合；重发内容与首发逐字节一致。
func TestResultOversizeEncodedTruncated(t *testing.T) {
	res := protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		ExitCode:    0,
		FinalAnswer: string(make([]byte, 512<<10)), // 全 0x00？改用 '<'
	}
	res.FinalAnswer = ""
	res.FinalAnswer = strings.Repeat("<", 512<<10) // json 默认转义 \u003c，约 3MiB
	res.FinishedAt = protocol.NowUTC()
	f, hubEP := newSvcWithRunner(t, bigResultRunner{res: res}, 1)

	task := echoTask("t-big")
	task.TargetAgent = "big"
	bigOffer(t, hubEP, task)

	var raw []byte
	if !waitFor(3*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		if !ok || e.State != ledger.StateFinished {
			return false
		}
		fr := f.ofTypes(protocol.MsgTaskResult)
		if len(fr) == 0 {
			return false
		}
		raw = fr[0].Payload
		return true
	}) {
		t.Fatal("任务应完成并发出 result")
	}
	// 线上信封按最终编码实测 ≤1MiB（含信封开销）
	env := &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: raw,
	}
	eb, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(eb) > protocol.MaxMessageBytes {
		t.Fatalf("线上信封应 ≤1MiB, got %d", len(eb))
	}
	// 持久化结果与发送结果一致，且已截断标注
	e, _ := f.svc.cfg.Ledger.Get(task.ID)
	if e.Result == nil || len(e.Result.FinalAnswer) >= 512<<10 {
		t.Fatalf("持久化结果应已截断, len=%d", len(e.Result.FinalAnswer))
	}
	if !strings.Contains(e.Result.Error, protocol.CodeTruncated) {
		t.Fatalf("应标注 truncated, got %q", e.Result.Error)
	}
	var p protocol.TaskResultPayload
	if err := jsonUnmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Result.FinalAnswer != e.Result.FinalAnswer {
		t.Fatalf("持久化结果与发送结果不一致")
	}
	// 重发（模拟重连补传）：逐字节稳定
	f.svc.ResendPending()
	fr := f.ofTypes(protocol.MsgTaskResult)
	if len(fr) != 2 {
		t.Fatalf("应重发一次, got %d", len(fr))
	}
	if string(fr[1].Payload) != string(fr[0].Payload) {
		t.Fatal("重发内容应与首发逐字节一致（同一持久化结果）")
	}
}

// TestResultUTF8BoundaryTruncation N2：多字节字符截断不破坏 UTF-8。
func TestResultUTF8BoundaryTruncation(t *testing.T) {
	res := protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		FinalAnswer: strings.Repeat("😀", 300<<10), // 每字 4B，约 1.2MB，无转义
		FinishedAt:  protocol.NowUTC(),
	}
	f, hubEP := newSvcWithRunner(t, bigResultRunner{res: res}, 1)
	task := echoTask("t-utf8")
	task.TargetAgent = "big"
	bigOffer(t, hubEP, task)
	if !waitFor(3*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(task.ID)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("任务应完成")
	}
	e, _ := f.svc.cfg.Ledger.Get(task.ID)
	if !utf8.ValidString(e.Result.FinalAnswer) {
		t.Fatal("截断后应为合法 UTF-8")
	}
	if len(e.Result.FinalAnswer)%4 != 0 {
		t.Fatalf("4 字节 rune 截断应落回完整 rune 边界, len=%d", len(e.Result.FinalAnswer))
	}
	if !strings.Contains(e.Result.Error, protocol.CodeTruncated) {
		t.Fatalf("应标注 truncated, got %q", e.Result.Error)
	}
}

// ---------- N4：取得槽位后、started/spawn 前重检过期 ----------

// TestExpiredAfterQueueWaitNotExecuted N4（SPEC 4.3：到期非终态 → failed/expired）：
// 并发 1，阻塞任务占住槽位，第二任务排队期间过期；释放后第二任务不产生执行副作用，
// 账本 failed/expired 并回传。
func TestExpiredAfterQueueWaitNotExecuted(t *testing.T) {
	hold := make(chan struct{})
	// S2：同 TestExecuteConcurrency——Fatal 提前退出时也必须放行，防 Close 挂起
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { close(hold) }) }
	t.Cleanup(release)
	g := newGateRunner(hold)
	f, hubEP := newSvcWithRunner(t, g, 1)

	// 任务一：占住唯一槽位
	t1 := echoTask("t-n4a")
	t1.TargetAgent = "big"
	bigOffer(t, hubEP, t1)
	select {
	case <-g.held:
	case <-time.After(2 * time.Second):
		t.Fatal("任务一应进入执行")
	}

	// 任务二：50ms 后过期，排队等待
	t2 := echoTask("t-n4b")
	t2.TargetAgent = "big"
	t2.ExpireAt = protocol.NowUTC().Add(50 * time.Millisecond)
	bigOffer(t, hubEP, t2)
	time.Sleep(200 * time.Millisecond) // 让 t2 跨过截止时间

	release() // 释放槽位
	if !waitFor(3*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(t2.ID)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("任务二应写入终态")
	}
	e, _ := f.svc.cfg.Ledger.Get(t2.ID)
	if e.Result.Status != protocol.ResultFailed || e.Result.Error != protocol.CodeExpired {
		t.Fatalf("任务二应 failed/expired, got %+v", e.Result)
	}
	if g.ran.Load() != 1 {
		t.Fatalf("过期任务不得执行（副作用必须不存在）, ran=%d", g.ran.Load())
	}
	// 结果应回传
	if !waitFor(2*time.Second, func() bool {
		for _, fr := range f.ofTypes(protocol.MsgTaskResult) {
			var p protocol.TaskResultPayload
			if jsonUnmarshal(fr.Payload, &p) == nil && p.TaskID == t2.ID {
				return true
			}
		}
		return false
	}) {
		t.Fatal("过期任务结果应回传")
	}
}

// TestExecuteExpiredRecoveredTask N4（恢复调度入口）：accepted 条目恢复后在
// Execute 前过期 → 不执行，failed/expired 持久化并回传。
func TestExecuteExpiredRecoveredTask(t *testing.T) {
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	ran := atomic.Int64{}
	r := countedRunner{ran: &ran}
	bus := transport.NewBus()
	nodeEP := bus.Endpoint("n1")
	svc := New(Config{Ledger: led, Runners: map[string]Runner{"big": r}}, nodeEP)
	t.Cleanup(svc.Close)

	task := echoTask("t-n4c")
	task.TargetAgent = "big"
	task.ExpireAt = protocol.NowUTC().Add(80 * time.Millisecond)
	if err := led.Accept(task); err != nil { // 模拟恢复清单中的 accepted 条目
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // 跨过截止时间
	svc.Execute(task)                  // 恢复调度入口
	if !waitFor(2*time.Second, func() bool {
		e, ok := led.Get(task.ID)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("应写入终态")
	}
	e, _ := led.Get(task.ID)
	if e.Result.Status != protocol.ResultFailed || e.Result.Error != protocol.CodeExpired {
		t.Fatalf("应 failed/expired, got %+v", e.Result)
	}
	if ran.Load() != 0 {
		t.Fatalf("过期任务不得执行, ran=%d", ran.Load())
	}
}

// countedRunner 计数桩执行器。
type countedRunner struct{ ran *atomic.Int64 }

func (c countedRunner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	c.ran.Add(1)
	return protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: protocol.NowUTC()}
}
