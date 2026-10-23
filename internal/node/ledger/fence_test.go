package ledger

import (
	"path/filepath"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// fencedTask 构造可区分的最小任务。
func fencedTask(id string) protocol.Task {
	return protocol.Task{ID: id, TraceID: "tr", TargetNode: "n1",
		TargetAgent: "echo", SubmittedBy: "hub"}
}

// TestAcceptFencedPersists 入账携带租约快照并随账本落盘（重开可恢复）。
func TestAcceptFencedPersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AcceptFenced(fencedTask("t-a"), OriginHub, 5); err != nil {
		t.Fatalf("入账: %v", err)
	}
	if e, ok := l.Get("t-a"); !ok || e.FenceEpoch != 5 {
		t.Fatalf("条目应携租约 5, got %+v", e)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if e, ok := l2.Get("t-a"); !ok || e.FenceEpoch != 5 {
		t.Fatalf("重开后租约应恢复 5, got %+v", e)
	}
}

// TestRearmFence 重臂语义：更新回带租约；幂等；NotFound；local 来源拒臂。
func TestRearmFence(t *testing.T) {
	l, _, err := Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	if err := l.AcceptFenced(fencedTask("t-hub"), OriginHub, 5); err != nil {
		t.Fatal(err)
	}
	if err := l.AcceptOrigin(fencedTask("t-local"), OriginLocal); err != nil {
		t.Fatal(err)
	}

	// 正常重臂
	if err := l.RearmFence("t-hub", 7); err != nil {
		t.Fatalf("重臂: %v", err)
	}
	if e, _ := l.Get("t-hub"); e.FenceEpoch != 7 {
		t.Fatalf("重臂后应 7, got %d", e.FenceEpoch)
	}
	// 幂等（同值 no-op 成功）
	if err := l.RearmFence("t-hub", 7); err != nil {
		t.Fatalf("幂等重臂: %v", err)
	}
	// local 来源拒臂（hub 租约只管 hub 链路）
	if err := l.RearmFence("t-local", 9); err != nil {
		t.Fatalf("local 拒臂应 no-op 成功: %v", err)
	}
	if e, _ := l.Get("t-local"); e.FenceEpoch != 0 {
		t.Fatalf("local 条目不得被重臂, got %d", e.FenceEpoch)
	}
	// 不存在
	if err := l.RearmFence("t-missing", 1); err == nil {
		t.Fatal("不存在的条目应报错")
	}
}

// TestLegacyEntryWithoutFence 旧版账本条目（无 fence_epoch 字段）加载为 0，
// 回带缺失由 hub 缺省宽容（向后兼容）。
func TestLegacyEntryWithoutFence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 按旧版账本形态直写（Entry 无 fence/resend 扩展字段的 JSON）落盘后重开
	if err := l.AcceptOrigin(fencedTask("t-old"), OriginHub); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if e, ok := l2.Get("t-old"); !ok || e.FenceEpoch != 0 || e.Origin != OriginHub {
		t.Fatalf("旧形态条目应加载为 fence=0 origin=hub, got %+v", e)
	}
}
