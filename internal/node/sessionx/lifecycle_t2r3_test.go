package sessionx

// lifecycle_t2r3_test.go T2-R3 修复轮回归：①Close「无门结论 → 锁内复核」窗口的
// 并发发布交错（P1——writerFor 得出无门后、墓碑登记前，创建完成登门并发布 open
// 会话；旧代码在「防御位」虚报 ErrSessionClosed 且会话保持 open，外层
// CloseSessionLocal 吞为幂等成功；修复后必须复核出并发出现的门、改走写者门串行
// 真关闭）；②写者门表容量上限（P2——门终身保留不回收，上限拦截唯一 ID 洪泛，
// 超限拒绝新创建并如实报错，活跃会话/重载/墓碑零影响）。
// 交错用仓内测试调度栅栏 closeAfterNoGateLookupHook 确定性复现（复刻 R3 审查
// overlay 单行插桩位），不再依赖临时 overlay。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// t2r3Wait 收取单值通道，超时明确 Fatal（防悬挂吃掉全局测试时限）。
func t2r3Wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("等待 %s 超时（30s）", what)
		var zero T
		return zero
	}
}

// TestT2R3CloseBeforeGateRegistrationMustClosePublishedSession P1：Close 在
// writerFor 得出无门结论后停在栅栏，同 ID 创建此刻完成登门、首轮与发布；Close
// 恢复后必须复核出并发出现的门并改走写者门串行真关闭——返回 nil、会话终局
// closed 且落盘、单门无双写者、续轮与重复创建被如实拒绝。旧代码在此交错下于
// 「防御位」虚报 ErrSessionClosed（外层吞为幂等成功），会话保持 open。
func TestT2R3CloseBeforeGateRegistrationMustClosePublishedSession(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r3close"})}
	m, dir := newManager(t, specs, Options{Now: testNow})
	const sid = "s-t2r3closegap0000000000000000000000000000000000000001"
	looked := make(chan struct{})
	resume := make(chan struct{})
	closeAfterNoGateLookupHook = func() {
		close(looked)
		<-resume
	}
	defer func() { closeAfterNoGateLookupHook = nil }()

	closed := make(chan error, 1)
	go func() {
		_, err := m.Close(sid)
		closed <- err
	}()
	t2r3Wait(t, looked, "Close 到达无门栅栏") // Close 已得出无门结论，停在缺口原位
	// 窗口内并发创建完整发布：登门 → 首轮 → open 会话落盘
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮"); err != nil {
		t.Fatalf("窗口内创建应成功发布: %v", err)
	}
	if snap, ok := m.Status(sid); !ok || snap.Status != protocol.SessionOpen {
		t.Fatalf("前置：创建应已发布 open 会话: ok=%v snap=%+v", ok, snap)
	}
	close(resume)
	if err := t2r3Wait(t, closed, "Close 收束"); err != nil {
		t.Fatalf("Close 必须复核出并发发布的会话并实际关闭（绝不以 ErrSessionClosed/NotFound 虚报）: %v", err)
	}
	m.mu.Lock()
	gates := len(m.writers)
	m.mu.Unlock()
	if gates != 1 {
		t.Fatalf("并发发布只应有一道生命周期门，得 %d（双门即串行保护失守）", gates)
	}
	snap, ok := m.Status(sid)
	if !ok || snap.Status != protocol.SessionClosed {
		t.Fatalf("关闭必须真落定：会话终局应 closed: ok=%v snap=%+v", ok, snap)
	}
	if _, _, err := m.Send(context.Background(), sid, "续轮"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("关闭后续轮应 ErrSessionClosed: %v", err)
	}
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "重复创建"); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("关闭后重复创建应 ErrSessionExists: %v", err)
	}
	// 落盘复核：重启后 closed 留存（关闭非内存虚报）
	m2, err := NewManager(dir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	if s, ok := m2.Status(sid); !ok || s.Status != protocol.SessionClosed {
		t.Fatalf("重启后应读到 closed: ok=%v %+v", ok, s)
	}
}

// TestT2R3CloseRacePublishPersistFailureHonest P1×N2：同一交错下落盘不可用——
// Close 经复核改走门内路径置 closed 后持久化失败，必须如实上抛真实错误（不得被
// ErrSessionClosed/NotFound 幂等面吞并）；内存 closed 保留，落盘恢复后幂等收敛
// 且 closed 确已落盘。
func TestT2R3CloseRacePublishPersistFailureHonest(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r3dirty"})}
	m, dir := newManager(t, specs, Options{Now: testNow})
	const sid = "s-t2r3dirtygap000000000000000000000000000000000000001"
	looked := make(chan struct{})
	resume := make(chan struct{})
	closeAfterNoGateLookupHook = func() {
		close(looked)
		<-resume
	}
	defer func() { closeAfterNoGateLookupHook = nil }()

	closed := make(chan error, 1)
	go func() {
		_, err := m.Close(sid)
		closed <- err
	}()
	t2r3Wait(t, looked, "Close 到达无门栅栏")
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮"); err != nil {
		t.Fatalf("窗口内创建应成功发布: %v", err)
	}
	// 破坏落盘（撤文件换目录：rename 永久失败）
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sessions.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	close(resume)
	err := t2r3Wait(t, closed, "Close 收束")
	if err == nil || errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("持久化失败必须如实上抛（不得虚报关闭/幂等达成）: %v", err)
	}
	if snap, ok := m.Status(sid); !ok || snap.Status != protocol.SessionClosed {
		t.Fatalf("内存态应已 closed（下次落盘收敛）: ok=%v snap=%+v", ok, snap)
	}
	// 落盘恢复：重试收敛为幂等 ErrSessionClosed，且 closed 确已落盘
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if _, retry := m.Close(sid); !errors.Is(retry, ErrSessionClosed) {
		t.Fatalf("落盘恢复后应幂等达成 ErrSessionClosed: %v", retry)
	}
	m2, err2 := NewManager(dir, specs, Options{Now: testNow})
	if err2 != nil {
		t.Fatalf("重开: %v", err2)
	}
	if s, ok := m2.Status(sid); !ok || s.Status != protocol.SessionClosed {
		t.Fatalf("重启后应读到 closed: ok=%v %+v", ok, s)
	}
}

// TestT2R3GateTableCapRejectsNewCreates P2：门终身保留不回收，容量上限兜底——
// 超限后新创建拒绝并如实报错（CLI 不执行、无轮次产物），既有 ID 的门复用/续轮/
// 关闭零影响；取消的失败尝试同样拒于门表登记（不再无界累积），墓碑登记不计入
// 门表容量。
func TestT2R3GateTableCapRejectsNewCreates(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r3cap"})}, Options{Now: testNow, MaxSessionGates: 2})
	ctx := context.Background()
	cwd := t.TempDir()
	for _, id := range []string{"s-t2r3cap-a", "s-t2r3cap-b"} {
		if _, _, err := m.CreateWithID(ctx, id, "coder", cwd, "首轮"); err != nil {
			t.Fatalf("建 %s: %v", id, err)
		}
	}
	// 第三个新 ID：超限拒绝，CLI 未执行（无轮次产物）
	sess, turn, err := m.CreateWithID(ctx, "s-t2r3cap-c", "coder", cwd, "超限创建")
	if !errors.Is(err, ErrGateTableFull) {
		t.Fatalf("超限新创建应 ErrGateTableFull: %v", err)
	}
	if turn != nil || sess.ID != "" {
		t.Fatalf("超限拒绝不得进入执行、不得返回会话: turn=%+v sess=%+v", turn, sess)
	}
	// 既有 ID 重投沿用既有门：显式冲突而非超限拒绝
	if _, _, err := m.CreateWithID(ctx, "s-t2r3cap-a", "coder", cwd, "重复创建"); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("既有 ID 应走门复用路径报 ErrSessionExists: %v", err)
	}
	// 既有会话续轮与关闭零影响
	if _, _, err := m.Send(ctx, "s-t2r3cap-a", "续轮"); err != nil {
		t.Fatalf("超限后既有会话续轮不受影响: %v", err)
	}
	if _, err := m.Close("s-t2r3cap-b"); err != nil {
		t.Fatalf("超限后既有会话关闭不受影响: %v", err)
	}
	// 取消的失败尝试同样拒于门表登记；墓碑登记不受门表上限约束
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := m.CreateWithID(cctx, "s-t2r3cap-d", "coder", cwd, "取消尝试"); !errors.Is(err, ErrGateTableFull) {
		t.Fatalf("超限后取消尝试应拒于门表登记: %v", err)
	}
	if _, err := m.Close("s-t2r3cap-tomb"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("墓碑登记不受门表上限约束: %v", err)
	}
	m.mu.Lock()
	gates := len(m.writers)
	m.mu.Unlock()
	if gates != 2 {
		t.Fatalf("门表应稳定在上限 2，得 %d", gates)
	}
}

// TestT2R3GateTableCapLoadBypass P2：容量上限只约束新创建——持久化重载按盘上
// 会话重建门不受上限约束（活跃会话永不因上限被拒，宁可满载也不拒服务），重载后
// 新创建仍受限。
func TestT2R3GateTableCapLoadBypass(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r3capload"})}
	m, dir := newManager(t, specs, Options{Now: testNow, MaxSessionGates: 2})
	ctx := context.Background()
	cwd := t.TempDir()
	for _, id := range []string{"s-t2r3load-a", "s-t2r3load-b"} {
		if _, _, err := m.CreateWithID(ctx, id, "coder", cwd, "首轮"); err != nil {
			t.Fatalf("建 %s: %v", id, err)
		}
	}
	if _, err := m.Close("s-t2r3load-b"); err != nil {
		t.Fatalf("关 B: %v", err)
	}
	// 上限 1 < 盘上 2 会话：重载必须成功且按盘上会话重建双门
	m2, err := NewManager(dir, specs, Options{Now: testNow, MaxSessionGates: 1})
	if err != nil {
		t.Fatalf("重载不得因上限拒绝既有会话: %v", err)
	}
	m2.mu.Lock()
	gates := len(m2.writers)
	m2.mu.Unlock()
	if gates != 2 {
		t.Fatalf("重载应按盘上会话重建 2 门，得 %d", gates)
	}
	if s, ok := m2.Status("s-t2r3load-b"); !ok || s.Status != protocol.SessionClosed {
		t.Fatalf("重载后应读到 B closed: ok=%v %+v", ok, s)
	}
	if _, _, err := m2.Send(ctx, "s-t2r3load-a", "续轮"); err != nil {
		t.Fatalf("重载后既有会话续轮: %v", err)
	}
	// 重载后新创建仍受限（门表 2 ≥ 上限 1）
	if _, _, err := m2.CreateWithID(ctx, "s-t2r3load-new", "coder", cwd, "新创建"); !errors.Is(err, ErrGateTableFull) {
		t.Fatalf("重载后新创建应仍受上限约束: %v", err)
	}
}

// TestT2R3GateTableCapOptions P2 装配面：负值上限装配拒绝（宁可不起也不带病
// 装配）；零值归一为默认上限。
func TestT2R3GateTableCapOptions(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{"coder": codexSpec(fake, nil)}
	if _, err := NewManager(t.TempDir(), specs, Options{Now: testNow, MaxSessionGates: -1}); err == nil {
		t.Fatal("负值上限应装配拒绝")
	}
	m, _ := newManager(t, specs, Options{Now: testNow})
	if m.maxGates != DefaultMaxSessionGates {
		t.Fatalf("零值上限应归一为默认 %d，得 %d", DefaultMaxSessionGates, m.maxGates)
	}
}
