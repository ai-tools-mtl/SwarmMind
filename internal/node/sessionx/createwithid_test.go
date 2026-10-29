package sessionx

// createwithid_test.go CreateWithID 契约（T2：会话身份由入口分配——hub 注册表 /
// node 本机入口先行登记 "s-"+hex 平台 ID，内核沿用同一 ID 建映射；T1 的 Create
// 自生成 ID 形态保持不变）。覆盖：显式 ID 建映射持久化、重复 ID 显式拒绝
//（绝不覆盖不接续）、空 ID 拒绝、恢复/续轮以同 ID 走通（注册表↔内核单一身份）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCreateWithIDExplicitID 显式平台 ID：建映射、快照携带同 ID、落盘重启后同
// ID 可续轮（注册表身份在重启后仍指向同一线程）。
func TestCreateWithIDExplicitID(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dir := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-fixed"})}, Options{Now: testNow})
	const sid = "s-explicit0000000000000000000000000000000000000000000000000001"
	sess, turn, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	if sess.ID != sid || turn.Seq != 1 || turn.Status != protocol.TurnOK || sess.NativeID != "thr-fixed" {
		t.Fatalf("显式 ID 会话快照异常: id=%s seq=%d status=%s native=%s", sess.ID, turn.Seq, turn.Status, sess.NativeID)
	}

	// 重启恢复：同 ID 续轮走通（映射持久化以平台 ID 为键）
	m2, err := NewManager(dir, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-fixed"})}, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	sess2, turn2, err := m2.Send(context.Background(), sid, "续轮")
	if err != nil {
		t.Fatalf("显式 ID 会话重启后续轮: %v", err)
	}
	if turn2.Seq != 2 || sess2.NativeID != "thr-fixed" {
		t.Fatalf("续轮快照异常: seq=%d native=%s", turn2.Seq, sess2.NativeID)
	}
}

// TestCreateWithIDDuplicateRejected 重复平台 ID：显式拒绝（ErrSessionExists），
// 既有会话零变更（不覆盖、不接续、轮次不增）。T2-R1 必修4 生命周期门后拒绝
// 收敛为 fail-fast：写者门串行下发布前可见既有映射，CLI 不再执行（无原生线程
// 浪费），无失败轮留痕（拒绝发生在任何执行之前）。
func TestCreateWithIDDuplicateRejected(t *testing.T) {
	fake := buildFakeCLI(t)
	runLog := filepath.Join(t.TempDir(), "run.log")
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "thr-1", "FAKE_LOG": runLog})}, Options{Now: testNow})
	const sid = "s-dup000000000000000000000000000000000000000000000000000001"
	if _, _, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "首轮"); err != nil {
		t.Fatalf("首次创建: %v", err)
	}
	_, turn, err := m.CreateWithID(context.Background(), sid, "coder", t.TempDir(), "重复投递")
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("重复 ID 应 ErrSessionExists, got %v", err)
	}
	if turn != nil {
		t.Fatalf("fail-fast 拒绝不应执行 CLI（无轮留痕）: %+v", turn)
	}
	if b, rerr := os.ReadFile(runLog); rerr == nil && strings.Count(string(b), "start") > 1 {
		t.Fatalf("重复创建不得二次执行 CLI: %s", b)
	}
	sess, ok := m.Status(sid)
	if !ok || sess.TurnCount != 1 || sess.NativeID != "thr-1" {
		t.Fatalf("既有会话不得被重复创建触碰: %+v ok=%v", sess, ok)
	}
}

// TestCreateWithIDEmptyRejected 空 ID 拒绝（入口契约：平台身份必填），拒绝路径
// 不建会话。
func TestCreateWithIDEmptyRejected(t *testing.T) {
	fake := buildFakeCLI(t)
	m, _ := newManager(t, map[string]AgentSpec{"coder": codexSpec(fake, nil)}, Options{Now: testNow})
	if _, _, err := m.CreateWithID(context.Background(), "  ", "coder", t.TempDir(), "p"); err == nil {
		t.Fatal("空 ID 应拒绝")
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("拒绝路径不得建会话: %d", n)
	}
}
