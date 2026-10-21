package mailbox

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// enqueueAll 依次入箱 n 条任务（task id 形如 "t-1"…"t-n"，同一 node；任务体
// 同步写入 st.Tasks——信箱只存 ID，与 Submit 的持久化路径同构）。
func enqueueAll(t *testing.T, st *store.State, node string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		task := protocol.Task{
			ID: "t-" + string(rune('0'+i)), TargetNode: node, TargetAgent: "a",
			Status: protocol.StatusSubmitted,
		}
		st.Tasks[task.ID] = task
		if err := Enqueue(st, node, &task); err != nil {
			t.Fatalf("入队 t-%d: %v", i, err)
		}
	}
}

// TestDequeueWhereSkipsIneligible P1.5 head-of-line 修复：队首不合格（workdir
// 互斥中）时跳过取首个合格者；不合格条目按原序保留、配额/字节计量不变。
func TestDequeueWhereSkipsIneligible(t *testing.T) {
	st := store.NewState()
	enqueueAll(t, st, "n1", 3)
	_, before := Usage(st, "n1")

	// 仅 t-2 合格（如 t-1 互斥中）：应出队 t-2，t-1/t-3 原序保留
	got, ok := DequeueWhere(st, "n1", func(t protocol.Task) bool { return t.ID == "t-2" })
	if !ok || got.ID != "t-2" {
		t.Fatalf("应出队 t-2, got %q ok=%v", got.ID, ok)
	}
	mb := st.Mailboxes["n1"]
	if len(mb.Tasks) != 2 || mb.Tasks[0] != "t-1" || mb.Tasks[1] != "t-3" {
		t.Fatalf("余队应 [t-1 t-3], got %v", mb.Tasks)
	}
	if _, now := Usage(st, "n1"); before-now != TaskBytes(&got) {
		t.Fatalf("字节计量应恰扣出队任务: before=%d now=%d task=%d", before, now, TaskBytes(&got))
	}

	// 全部不合格：ok=false 且信箱原样
	_, ok = DequeueWhere(st, "n1", func(protocol.Task) bool { return false })
	if ok {
		t.Fatal("全不合格应返回 ok=false")
	}
	if cnt, _ := Usage(st, "n1"); cnt != 2 {
		t.Fatalf("不合格遍历不得改动信箱, got %d 条", cnt)
	}

	// eligible=nil 等价 Dequeue：FIFO 取队首 t-1
	got, ok = DequeueWhere(st, "n1", nil)
	if !ok || got.ID != "t-1" {
		t.Fatalf("nil 谓词应按 FIFO 出队 t-1, got %q ok=%v", got.ID, ok)
	}
}

// TestDequeueWhereEmptyAndStale 空信箱与陈旧条目防御：队列空 ok=false；
// 非 submitted 条目跳过（与 Dequeue 同口径）。
func TestDequeueWhereEmptyAndStale(t *testing.T) {
	st := store.NewState()
	if _, ok := DequeueWhere(st, "n1", nil); ok {
		t.Fatal("空信箱应 ok=false")
	}
	enqueueAll(t, st, "n1", 2)
	st.Tasks["t-1"] = func() protocol.Task { // 队首漂移为非 submitted（防御路径）
		tk := st.Tasks["t-1"]
		tk.Status = protocol.StatusOffered
		return tk
	}()
	got, ok := DequeueWhere(st, "n1", nil)
	if !ok || got.ID != "t-2" {
		t.Fatalf("陈旧队首应跳过取 t-2, got %q ok=%v", got.ID, ok)
	}
}
