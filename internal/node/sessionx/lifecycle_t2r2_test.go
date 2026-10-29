package sessionx

// lifecycle_t2r2_test.go T2-R2 修复轮回归：生命周期门的失败创建交错（N1——失败
// 创建绝不拆共享门，同平台 ID 绝不出现双门并行写者）与墓碑/已关闭持久化收敛
//（N2——落盘未成功前每次关闭请求如实报真实错误，绝不虚报幂等成功）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// t2r2BlockingAdapter 阻塞型适配器包装：argv 进入即报告（entered）、等放行
//（release）再委托真实适配器——构造「创建在途」的确定性交错。
type t2r2BlockingAdapter struct {
	adapter
	entered map[string]chan struct{}
	release map[string]chan struct{}
}

func (a t2r2BlockingAdapter) argv(first bool, cwd, id, prompt string) []string {
	close(a.entered[prompt])
	<-a.release[prompt]
	return a.adapter.argv(first, cwd, id, prompt)
}

// TestT2R2FailedCreateKeepsSharedGate N1：失败创建不得拆掉有排队者的生命周期门。
// A 首个创建自登门并在 CLI 中途失败；B 排队同门续跑并成功发布；C 第三创建必须
// 经同一门串行（在 B 执行期间绝不得进入 CLI）——失败拆门会让 C 另立新门，与 B
// 对同一平台 ID 并行双写。
func TestT2R2FailedCreateKeepsSharedGate(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r2"})}, Options{Now: testNow})
	a := t2r2BlockingAdapter{adapter: m.adapters["coder"],
		entered: map[string]chan struct{}{}, release: map[string]chan struct{}{}}
	for _, p := range []string{"A", "B", "C"} {
		a.entered[p] = make(chan struct{})
		a.release[p] = make(chan struct{})
	}
	m.adapters["coder"] = a
	const sid = "s-t2r2gate0000000000000000000000000000000000000000001"
	cwd := t.TempDir()

	// A：首个创建，CLI 执行中
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 3)
	go func() { _, _, err := m.CreateWithID(ctx, sid, "coder", cwd, "A"); done <- err }()
	<-a.entered["A"]
	m.mu.Lock()
	g := m.writers[sid]
	m.mu.Unlock()
	if g == nil {
		t.Fatal("创建在途应有生命周期门")
	}

	// B：排队同一门（真实 waiter 就位后再取消 A）
	go func() { _, _, err := m.CreateWithID(context.Background(), sid, "coder", cwd, "B"); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		g.mu.Lock()
		n := len(g.waiters)
		g.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B 未排队到同一门")
		}
		time.Sleep(time.Millisecond)
	}
	// A 失败收束（CLI 执行中 ctx 取消）：门必须保留给 B
	cancel()
	close(a.release["A"])
	if err := <-done; err == nil {
		t.Fatal("A 应以失败收束")
	}
	<-a.entered["B"] // B 经同一门获得写权并执行
	// C：第三创建——门仍在，B 执行期间 C 不得进入 CLI（重叠即双写者）
	go func() { _, _, err := m.CreateWithID(context.Background(), sid, "coder", cwd, "C"); done <- err }()
	overlap := false
	select {
	case <-a.entered["C"]:
		overlap = true
	case <-time.After(150 * time.Millisecond):
	}
	close(a.release["B"])
	close(a.release["C"])
	if errB, errC := <-done, <-done; errB != nil {
		t.Fatalf("B 应成功发布: %v（C: %v）", errB, errC)
	}
	if overlap {
		t.Fatal("B 与 C 对同一平台 ID 并行进入 CLI：失败创建拆掉了仍被引用的共享门")
	}
	if sess, ok := m.Status(sid); !ok || sess.Status != protocol.SessionOpen || sess.TurnCount != 1 {
		t.Fatalf("终局应由 B 发布且仅一轮（C 被显式重复 ID 拒绝，无第二次执行）: %+v ok=%v", sess, ok)
	}
}

// TestT2R2FailedCreateKeepsGateForClose N1 交错变体：失败创建 + 排队期间 Close——
// 关闭沿同一门排在重试创建之后，等其收束后落定，会话绝不以 open 复活。
func TestT2R2FailedCreateKeepsGateForClose(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r2"})}, Options{Now: testNow})
	a := t2r2BlockingAdapter{adapter: m.adapters["coder"],
		entered: map[string]chan struct{}{}, release: map[string]chan struct{}{}}
	for _, p := range []string{"A", "B"} {
		a.entered[p] = make(chan struct{})
		a.release[p] = make(chan struct{})
	}
	m.adapters["coder"] = a
	const sid = "s-t2r2gateclose00000000000000000000000000000000000001"
	cwd := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { _, _, err := m.CreateWithID(ctx, sid, "coder", cwd, "A"); done <- err }()
	<-a.entered["A"]
	m.mu.Lock()
	g := m.writers[sid]
	m.mu.Unlock()
	go func() { _, _, err := m.CreateWithID(context.Background(), sid, "coder", cwd, "B"); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		g.mu.Lock()
		n := len(g.waiters)
		g.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B 未排队到同一门")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(a.release["A"])
	if err := <-done; err == nil {
		t.Fatal("A 应以失败收束")
	}
	<-a.entered["B"] // 重试创建经同一门执行中
	// 关闭排队同一门：等 B 收束后关闭成立（创建先收束、关闭后落定）
	closed := make(chan error, 1)
	go func() { _, err := m.Close(sid); closed <- err }()
	// 关闭不得越过 B 抢先落定：门串行下关闭必须等 B（若失败拆门，关闭会在 B
	// 发布前经墓碑路径返回，随后会话以 open 复活）
	time.Sleep(80 * time.Millisecond)
	close(a.release["B"])
	if err := <-done; err != nil {
		t.Fatalf("重试创建应成功: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("关闭应成立: %v", err)
	}
	if sess, ok := m.Status(sid); !ok || sess.Status != protocol.SessionClosed || sess.TurnCount != 1 {
		t.Fatalf("终局应 closed 且首轮留痕保留: ok=%v %+v", ok, sess)
	}
}

// TestT2R2TombstonePersistFailureStaysFailed N2：墓碑首次持久化失败后，重复关闭
// 必须继续报真实错误（绝不因幂等分支虚报成功）；落盘恢复后幂等收敛。
func TestT2R2TombstonePersistFailureStaysFailed(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dir := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r2"})}, Options{Now: testNow})
	const sid = "s-t2r2tomb00000000000000000000000000000000000000000001"
	// sessions.json 变为目录：rename 永久失败（持久化持续不可用的确定性形态）
	if err := os.Mkdir(filepath.Join(dir, "sessions.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Close(sid); err == nil || errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("首次关闭必须报持久化失败（不得虚报无映射幂等）: %v", err)
	}
	if _, retry := m.Close(sid); retry == nil || errors.Is(retry, ErrSessionNotFound) || errors.Is(retry, ErrSessionClosed) {
		t.Fatalf("重试关闭在落盘未成功前必须继续报真实错误: %v", retry)
	}
	// 落盘恢复：重试保存收敛，此后幂等语义照常
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if _, third := m.Close(sid); !errors.Is(third, ErrSessionNotFound) {
		t.Fatalf("落盘恢复后应收敛为幂等 ErrSessionNotFound: %v", third)
	}
	// 墓碑已落盘：后续创建被拒绝（关闭决定成立）
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "墓碑后的创建"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("墓碑应拒绝创建: %v", err)
	}
}

// TestT2R2ClosedSessionPersistRetry N2 同查已建立会话：关闭落盘失败后，「已关闭」
// 快速返回在落盘未确认前必须继续报真实错误；落盘恢复后幂等达成。
func TestT2R2ClosedSessionPersistRetry(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dir := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r2"})}, Options{Now: testNow})
	const sid = "s-t2r2closed0000000000000000000000000000000000000000001"
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮"); err != nil {
		t.Fatalf("建会话: %v", err)
	}
	// 破坏落盘（撤文件换目录：rename 永久失败）
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sessions.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Close(sid); err == nil || errors.Is(err, ErrSessionClosed) {
		t.Fatalf("关闭落盘失败必须报真实错误: %v", err)
	}
	if _, retry := m.Close(sid); retry == nil || errors.Is(retry, ErrSessionClosed) {
		t.Fatalf("已关闭但未落盘：重试必须继续报真实错误（不得虚报幂等达成）: %v", retry)
	}
	// 落盘恢复：重试保存收敛，幂等回执成立
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	sess, third := m.Close(sid)
	if !errors.Is(third, ErrSessionClosed) || sess.Status != protocol.SessionClosed {
		t.Fatalf("落盘恢复后应幂等达成: sess=%+v err=%v", sess, third)
	}
	// 重启复核：closed 状态确已落盘（内存态有磁盘背书）
	m2, err := NewManager(dir, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t2r2"})}, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	if s, ok := m2.Status(sid); !ok || s.Status != protocol.SessionClosed {
		t.Fatalf("重启后应读到 closed: ok=%v %+v", ok, s)
	}
}
