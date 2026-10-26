package service

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 2026-10-26 双审缺陷2回归：取消链路落盘失败不得回确认——闭环确认必须以持久化
// 为前提，ack 掉则 hub 视取消闭环，node 重启后丢失防复活保障（墓碑/cancelled
// 终态），迟到 offer 可执行已取消任务。修复口径：落盘失败只记日志不发确认，
// hub 至少一次取消重发携帧重试（与重臂失败只跳过发送的既有口径一致）。

// TestCancelTombstonePersistFailSuppressesAck 缺陷2-A：task_cancel 命中未知任务
// 且墓碑落盘失败 → 不发 task_cancelled ack、内存无墓碑；hub 取消重发（注入解除）
// 后重试：ack + 墓碑落盘。
func TestCancelTombstonePersistFailSuppressesAck(t *testing.T) {
	var mu sync.Mutex
	failCancels := true
	led, _, err := ledger.OpenForTest(filepath.Join(t.TempDir(), "ledger"), nil)
	if err != nil {
		t.Fatal(err)
	}
	led.InjectCancelsForTest(func(op, stage string) error {
		mu.Lock()
		defer mu.Unlock()
		if failCancels {
			return errors.New("注入: 墓碑落盘失败")
		}
		return nil
	})
	f := newFixtureWith(t, map[string]Runner{
		"echo-demo": runner.New(echoProfile(), map[string]string{"PATH": "/usr/bin:/bin"}, filepath.Join(t.TempDir(), "home")),
	}, led)

	const id = "t-tomb-fail"
	f.sendCancel(t, id, "先取消")
	time.Sleep(300 * time.Millisecond) // 给处理路径时间（期望是无 ack）
	if n := len(f.ofTypes(protocol.MsgTaskCancelled)); n != 0 {
		t.Fatalf("墓碑落盘失败不得发取消 ack, got %d", n)
	}
	if f.svc.cfg.Ledger.HasCancelTombstone(id) {
		t.Fatal("落盘失败墓碑不得在内存假成功")
	}

	// 恢复：hub 至少一次取消重发 → 墓碑落盘成功 → ack
	mu.Lock()
	failCancels = false
	mu.Unlock()
	f.sendCancel(t, id, "重发")
	if !waitFor(2*time.Second, func() bool { return len(f.ofTypes(protocol.MsgTaskCancelled)) >= 1 }) {
		t.Fatal("恢复后重试应发取消 ack")
	}
	if !f.svc.cfg.Ledger.HasCancelTombstone(id) {
		t.Fatal("恢复后墓碑应落盘")
	}
}

// TestCancelFinishPersistFailSuppressesAck 缺陷2-B：在途任务取消命中「在途注册
// 表未命中」窗口（等待点放弃/关闭竞态），取消终态落盘失败 → 不发取消 ack 也不
// 发 result、账本保持 started；恢复后取消重发 → 终态落盘并回执。
func TestCancelFinishPersistFailSuppressesAck(t *testing.T) {
	var mu sync.Mutex
	failFinish := false
	led, _, err := ledger.OpenForTest(filepath.Join(t.TempDir(), "ledger"), func(op, stage string) error {
		if op == "MarkFinished" && stage == "before_write" { // 对齐 ledger 包内 stageBeforeWrite 常量值
			mu.Lock()
			defer mu.Unlock()
			if failFinish {
				return errors.New("注入: 取消终态落盘失败")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := &cancelGateRunner{release: make(chan struct{})}
	defer close(g.release) // 测试结束放行门控 runner（ctx 取消后收尾 ErrWrongState 幂等忽略）
	f := newFixtureWith(t, map[string]Runner{
		"echo-demo": g,
	}, led)

	const id = "t-fin-fail"
	f.sendOffer(t, echoTask(id))
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(id)
		return ok && e.State == ledger.StateStarted
	}) {
		t.Fatal("前置：任务应在途（started，gate 挂住）")
	}
	// 模拟「等待点放弃/关闭竞态」窗口：在途注册表未命中 → 取消走账本直落分支
	f.svc.unregCancel(id)

	mu.Lock()
	failFinish = true
	mu.Unlock()
	f.sendTaskCancel(t, id, 0)
	time.Sleep(300 * time.Millisecond)
	if n := len(f.ofTypes(protocol.MsgTaskCancelled)); n != 0 {
		t.Fatalf("终态落盘失败不得发取消 ack, got %d", n)
	}
	if n := len(f.resultFencesFor(id)); n != 0 {
		t.Fatalf("终态落盘失败不得发 result, got %d", n)
	}
	if e, _ := f.svc.cfg.Ledger.Get(id); e.State != ledger.StateStarted {
		t.Fatalf("账本应保持 started（终态未落上）, got %s", e.State)
	}

	// 恢复：hub 取消重发 → MarkFinished 成功 → 终态 + 回执
	mu.Lock()
	failFinish = false
	mu.Unlock()
	f.sendTaskCancel(t, id, 0)
	if !waitFor(2*time.Second, func() bool {
		e, ok := f.svc.cfg.Ledger.Get(id)
		return ok && e.State == ledger.StateFinished
	}) {
		t.Fatal("恢复后取消应完成终态落盘")
	}
	if !waitFor(2*time.Second, func() bool {
		return len(f.resultFencesFor(id)) >= 1 || len(f.ofTypes(protocol.MsgTaskCancelled)) >= 1
	}) {
		t.Fatal("恢复后应有回执（task_result 或幂等 ack）")
	}
}
