package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// writeTombstoneFile 直写 sidecar（测试回拨时刻用；内存映射同 cancels.json 格式）。
func writeTombstoneFile(dir string, m map[string]time.Time) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, cancelsFileName), b, 0o644)
}

// TestCancelTombstoneLifecycle 墓碑生命周期：登记 → 命中 → 跨重启持久 → 消费
// 即清 → TTL 兜底（2026-10-22 修复轮2：取消先于 offer 的执行窗口封堵）。
func TestCancelTombstoneLifecycle(t *testing.T) {
	dir := t.TempDir()
	l, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 登记 + 幂等（重复登记不报错、不更新时刻）
	if err := l.AddCancelTombstone("t-tb-1"); err != nil {
		t.Fatalf("登记墓碑: %v", err)
	}
	if err := l.AddCancelTombstone("t-tb-1"); err != nil {
		t.Fatalf("重复登记应幂等: %v", err)
	}
	if !l.HasCancelTombstone("t-tb-1") {
		t.Fatal("登记后应命中墓碑")
	}

	// 跨重启持久（sidecar 落盘）
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if !l2.HasCancelTombstone("t-tb-1") {
		t.Fatal("重启后墓碑应仍在（cancels.json 持久化）")
	}

	// 消费即清
	if err := l2.ClearCancelTombstone("t-tb-1"); err != nil {
		t.Fatalf("消费墓碑: %v", err)
	}
	if l2.HasCancelTombstone("t-tb-1") {
		t.Fatal("清除后不应再命中")
	}
	if err := l2.ClearCancelTombstone("t-tb-1"); err != nil {
		t.Fatalf("重复清除应幂等: %v", err)
	}
}

// TestCancelTombstoneTTL 超保留期的墓碑：加载时与显式清理时均被清除。
func TestCancelTombstoneTTL(t *testing.T) {
	dir := t.TempDir()
	l, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AddCancelTombstone("t-tb-old"); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 手写 sidecar 回拨写入时刻至保留期之前（add 的 now 参数不透出，直改文件）
	at := protocol.NowUTC().Add(-protocol.TerminalRetention - time.Minute)
	if err := writeTombstoneFile(dir, map[string]time.Time{"t-tb-old": at}); err != nil {
		t.Fatal(err)
	}

	// 加载即清（内存不可见）
	l2, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l2.HasCancelTombstone("t-tb-old") {
		_ = l2.Close()
		t.Fatal("超保留期墓碑加载时应被清除")
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}

	// 未超期的保留
	future := protocol.NowUTC().Add(-time.Minute)
	if err := writeTombstoneFile(dir, map[string]time.Time{"t-tb-new": future}); err != nil {
		t.Fatal(err)
	}
	l3, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l3.Close() }()
	if err := l3.CleanupCancelTombstones(protocol.NowUTC()); err != nil {
		t.Fatalf("显式清理: %v", err)
	}
	if !l3.HasCancelTombstone("t-tb-new") {
		t.Fatal("未超期墓碑不应被清理")
	}
}
