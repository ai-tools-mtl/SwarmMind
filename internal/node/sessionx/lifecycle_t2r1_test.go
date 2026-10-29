package sessionx

// lifecycle_t2r1_test.go T2-R1 必修4 内核回归：映射发布前的关闭一致性——
// 关闭墓碑持久化、关闭后的创建不再发布 open（不执行 CLI）、跨重启留存；
// 创建在途的关闭经生命周期门串行化等首轮收束后落定。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCloseTombstoneHonorsLaterCreate 关闭墓碑：无映射会话的关闭落持久化墓碑，
// 其后同 ID 创建直接拒绝（ErrSessionClosed、CLI 不执行、映射不建立）；重启后
// 墓碑留存（关闭决定跨进程不失效，绝不「关闭成功后会话复活」）。
func TestCloseTombstoneHonorsLaterCreate(t *testing.T) {
	fake := buildFakeCLI(t)
	runLog := filepath.Join(t.TempDir(), "run.log")
	m, dir := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t", "FAKE_LOG": runLog})}, Options{Now: testNow})
	const sid = "s-tomb00000000000000000000000000000000000000000000000000001"

	// 无映射关闭：契约维持 ErrSessionNotFound（调用方幂等语义不变），意图落墓碑
	if _, err := m.Close(sid); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("无映射关闭应 ErrSessionNotFound, got %v", err)
	}
	// 墓碑持久化即时落盘（重启复核在同一 manager 生命周期外）
	// 关闭后的创建：直接拒绝，绝不执行 CLI、绝不发布 open
	_, turn, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "关闭后的创建")
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("墓碑应拒绝创建, got %v", err)
	}
	if turn != nil {
		t.Fatalf("墓碑拒绝不执行 CLI（无轮留痕）: %+v", turn)
	}
	if _, ok := m.Status(sid); ok {
		t.Fatal("墓碑拒绝不得发布映射")
	}
	if b, rerr := os.ReadFile(runLog); rerr == nil && strings.Contains(string(b), "start") {
		t.Fatalf("墓碑拒绝绝不得 spawn CLI: %s", b)
	}

	// 重启：墓碑自 sessions.json 恢复，关闭决定跨进程留存
	m2, err := NewManager(dir, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-t"})}, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	if _, _, err := m2.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "重启后的创建"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("重启后墓碑仍应拒绝创建, got %v", err)
	}
	// 既有会话不受墓碑影响：正常会话的关闭/续轮语义不变
	const sid2 = "s-alive0000000000000000000000000000000000000000000000000001"
	if _, _, err := m2.CreateWithID(context.Background(), sid2, "coder", t.TempDir(), "正常首轮"); err != nil {
		t.Fatalf("正常创建: %v", err)
	}
	if _, _, err := m2.Send(context.Background(), sid2, "续轮"); err != nil {
		t.Fatalf("墓碑不误伤既有会话续轮: %v", err)
	}
}

// TestCloseDuringCreateWaitsAndCloses 创建在途的关闭：共用写者门串行化——关闭
// 等首轮收束后落定（不虚假成功、会话不以 open 复活），首轮留痕保留。
func TestCloseDuringCreateWaitsAndCloses(t *testing.T) {
	fake := buildFakeCLI(t)
	mark := filepath.Join(t.TempDir(), "start.mark")
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{
		"FAKE_THREAD_ID": "thr-w", "FAKE_TOUCH": mark, "FAKE_SLEEP": "0.4",
	})}, Options{Now: testNow})
	const sid = "s-gate00000000000000000000000000000000000000000000000000001"

	done := make(chan error, 1)
	go func() {
		_, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮")
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首轮未起跑（无 TOUCH 标记）")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 首轮执行中关闭：Close 排生命周期门等首轮收束，然后正常关闭
	sess, err := m.Close(sid)
	if err != nil {
		t.Fatalf("创建在途的关闭应等收束后成立: %v", err)
	}
	if sess.Status != protocol.SessionClosed {
		t.Fatalf("关闭后快照应 closed: %+v", sess)
	}
	if err := <-done; err != nil {
		t.Fatalf("首轮应照常完成（关闭不打断执行）: %v", err)
	}
	final, ok := m.Status(sid)
	if !ok || final.Status != protocol.SessionClosed || final.TurnCount != 1 {
		t.Fatalf("终局应 closed 且首轮留痕保留: ok=%v %+v", ok, final)
	}
}
