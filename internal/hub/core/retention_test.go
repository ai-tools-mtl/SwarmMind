package core

import (
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newHubWithRetention 保留期注入变体基座（maturity-retention-config）：retention
// 经 cfg.TerminalRetention 注入 Core（nil = 协议默认 24h），Sweep 手动驱动 +
// fakeClock 时间注入，无真实 sleep。
func newHubWithRetention(t *testing.T, retention *time.Duration) *hubFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	bus := transport.NewBus()
	clock := newFakeClock()
	core, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), Now: clock.Now, SweepInterval: 0, TerminalRetention: retention})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: core, clock: clock}
	f.hubEP.Subscribe(core.HandleFrame)
	t.Cleanup(func() {
		_ = core.Close()
		_ = st.Close()
	})
	return f
}

// TestSweepTerminalRetentionConfig 终态保留参数化（maturity-retention-config）：
// 短值（1h）按注入值清理并同步失效幂等索引；0（terminal_retention: 0s）永不
// 清理、幂等索引恒有效（同键重提恒 dup 原任务）。默认 nil → 24h 语义由既有
// result_test.go 终态清理用例锁定，此处不重复。
func TestSweepTerminalRetentionConfig(t *testing.T) {
	t.Run("short_value_1h", func(t *testing.T) {
		h := time.Hour
		f := newHubWithRetention(t, &h)
		if got := f.core.TerminalRetention(); got != h {
			t.Fatalf("TerminalRetention() 应返回注入值 %v, got %v", h, got)
		}
		node := f.newNode("nodeB", agentsOf("echo"), true)
		req := submitReq("nodeB", "echo")
		req.IdemKey = "short-retention-key"
		ack, _ := f.core.Submit(req, "")
		node.result(ack.TaskID, completeRes())

		// 未到期：保留
		f.clock.Advance(h - f.second())
		f.core.Sweep()
		if _, ok := f.core.GetTask(ack.TaskID); !ok {
			t.Fatal("终态未满注入保留期不应清理")
		}
		// 到期：清理 + idem 同步失效（同键重提=新任务）
		f.clock.Advance(2 * f.second())
		f.core.Sweep()
		if _, ok := f.core.GetTask(ack.TaskID); ok {
			t.Fatal("终态超注入保留期应从快照清理")
		}
		ack2, err := f.core.Submit(req, "")
		if err != nil {
			t.Fatalf("清理后同键提交应作为新任务: %v", err)
		}
		if ack2.Dup || ack2.TaskID == ack.TaskID {
			t.Errorf("清理后 idem 应失效, got dup=%v task=%s", ack2.Dup, ack2.TaskID)
		}
	})

	t.Run("zero_never_clean", func(t *testing.T) {
		zero := time.Duration(0)
		f := newHubWithRetention(t, &zero)
		if got := f.core.TerminalRetention(); got != 0 {
			t.Fatalf("TerminalRetention() 应返回 0, got %v", got)
		}
		node := f.newNode("nodeB", agentsOf("echo"), true)
		req := submitReq("nodeB", "echo")
		req.IdemKey = "never-clean-key"
		ack, _ := f.core.Submit(req, "")
		node.result(ack.TaskID, completeRes())

		f.clock.Advance(100 * 24 * time.Hour) // 远超任何保留期
		f.core.Sweep()
		if _, ok := f.core.GetTask(ack.TaskID); !ok {
			t.Fatal("retention=0（terminal_retention: 0s）终态任务不应被清理（永不自动清理语义）")
		}
		// 幂等索引恒有效：同键重提仍返回原任务（去重窗口=保留期窗口，永不清理即永不失效）
		ack2, err := f.core.Submit(req, "")
		if err != nil {
			t.Fatalf("同键重提失败: %v", err)
		}
		if !ack2.Dup || ack2.TaskID != ack.TaskID {
			t.Errorf("retention=0 时同键重提应 dup 原任务, got dup=%v task=%s", ack2.Dup, ack2.TaskID)
		}
	})

	t.Run("default_nil_24h", func(t *testing.T) {
		f := newHubWithRetention(t, nil)
		if got := f.core.TerminalRetention(); got != protocol.TerminalRetention {
			t.Fatalf("未注入应回落协议默认 %v, got %v", protocol.TerminalRetention, got)
		}
	})
}
