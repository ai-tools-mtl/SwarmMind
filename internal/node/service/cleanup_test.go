package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// cleanupWiringFixture 账本终态清理接线测试基座：真实账本 + 短清理节拍
//（cleanupInterval 包内 var，测试缩短后恢复）+ TerminalRetention 注入。
func cleanupWiringFixture(t *testing.T, retention time.Duration) (*Service, *ledger.Ledger) {
	t.Helper()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	bus := transport.NewBus()
	ret := retention
	svc := New(Config{Ledger: led, TerminalRetention: &ret}, bus.Endpoint("n1"))
	t.Cleanup(svc.Close)
	return svc, led
}

// seedFinishedFinished 落一个 finished 条目并按 acked 与否整形：acked=true 时
// 置 result_acked 并把 first_acked_at 回填到 old（超窗）；acked=false 保持未确认
//（去重红线保护对象）。
func seedFinishedEntry(t *testing.T, led *ledger.Ledger, id string, acked bool, old time.Time) {
	t.Helper()
	task := protocol.Task{ID: id, TargetAgent: "echo", TargetNode: "n1", CreatedAt: protocol.NowUTC()}
	if err := led.AcceptOrigin(task, ledger.OriginHub); err != nil {
		t.Fatalf("AcceptOrigin(%s): %v", id, err)
	}
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinishedAt: old}
	if err := led.MarkFinished(id, res); err != nil {
		t.Fatalf("MarkFinished(%s): %v", id, err)
	}
	if !acked {
		return
	}
	if err := led.MarkResultAcked(id); err != nil {
		t.Fatalf("MarkResultAcked(%s): %v", id, err)
	}
	if err := led.Update(func(m map[string]ledger.Entry) error {
		e := m[id].Clone()
		e.FirstAckedAt = old
		m[id] = e
		return nil
	}); err != nil {
		t.Fatalf("回填 first_acked_at(%s): %v", id, err)
	}
}

// TestServiceCleanupLoopWiring CleanupAcked 接入运行循环锁（maturity-retention-config，
// 消除「清理函数从未接入运行循环」已知差异）：清理节拍缩短为 20ms 真实短等待，
// 断言——超窗 acked 条目被周期清理；超窗未 acked 条目保留（去重红线）；retention=0
// 时超窗 acked 条目保留（永不清理）。
func TestServiceCleanupLoopWiring(t *testing.T) {
	oldInterval := cleanupInterval
	cleanupInterval = 20 * time.Millisecond
	t.Cleanup(func() { cleanupInterval = oldInterval })

	t.Run("short_retention_cleans_acked_keeps_unacked", func(t *testing.T) {
		svc, led := cleanupWiringFixture(t, time.Minute)
		now := protocol.NowUTC()
		old := now.Add(-2 * time.Hour)
		seedFinishedEntry(t, led, "t-acked-old", true, old)
		seedFinishedEntry(t, led, "t-unacked-old", false, old)

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := led.Get("t-acked-old"); !ok {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, ok := led.Get("t-acked-old"); ok {
			t.Fatal("retention=1m：超窗 acked 条目应被运行循环周期清理（接线锁）")
		}
		if _, ok := led.Get("t-unacked-old"); !ok {
			t.Fatal("超窗未 acked 条目不得清理（去重红线：只清 result_acked）")
		}
		if svc.TerminalRetention() != time.Minute {
			t.Fatalf("TerminalRetention() 应返回注入值, got %v", svc.TerminalRetention())
		}
	})

	t.Run("zero_never_cleans", func(t *testing.T) {
		svc, led := cleanupWiringFixture(t, 0)
		now := protocol.NowUTC()
		seedFinishedEntry(t, led, "t-acked-ancient", true, now.Add(-100*24*time.Hour))

		time.Sleep(300 * time.Millisecond) // 数个清理节拍
		if _, ok := led.Get("t-acked-ancient"); !ok {
			t.Fatal("retention=0（terminal_retention: 0s）：超窗 acked 条目不应被清理（永不清理语义）")
		}
		if svc.TerminalRetention() != 0 {
			t.Fatalf("TerminalRetention() 应返回 0, got %v", svc.TerminalRetention())
		}
	})

	t.Run("default_nil_24h", func(t *testing.T) {
		led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = led.Close() })
		bus := transport.NewBus()
		svc := New(Config{Ledger: led}, bus.Endpoint("n1"))
		t.Cleanup(svc.Close)
		if svc.TerminalRetention() != protocol.TerminalRetention {
			t.Fatalf("未注入应回落协议默认 %v, got %v", protocol.TerminalRetention, svc.TerminalRetention())
		}
	})
}
