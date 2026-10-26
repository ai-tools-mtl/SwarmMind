package ledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 2026-10-26 双审缺陷1回归：取消墓碑先写后见纪律。旧实现 add/clear 先改内存
// 后落盘、persist 失败不回滚——add 同键重试命中内存假成功（永久跳过写盘，重启
// 丢墓碑，迟到 offer 可执行已取消任务）。修复后：副本构造 → persist 成功 →
// 才替换内存；本组测试用注入钩子逐场景断言。

// newTestCancels 构造独立墓碑集合（包内单元测试用，不启 Ledger/flock）。
func newTestCancels(t *testing.T) *cancelTombstones {
	t.Helper()
	dir := t.TempDir()
	return &cancelTombstones{
		path: filepath.Join(dir, cancelsFileName),
		tmp:  filepath.Join(dir, cancelsTmpName),
		m:    map[string]time.Time{},
	}
}

// readCancelsFile 读盘上墓碑文件（断言持久化真实发生/真实移除）。
func readCancelsFile(t *testing.T, c *cancelTombstones) map[string]time.Time {
	t.Helper()
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatalf("读墓碑文件: %v", err)
	}
	var m map[string]time.Time
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("解析墓碑文件: %v", err)
	}
	return m
}

// TestCancelsAddPersistFailNoMemoryPollution add 落盘失败不得污染内存、同键
// 重试不得假成功；解除注入后重试真实落盘（内存与盘一致）。
func TestCancelsAddPersistFailNoMemoryPollution(t *testing.T) {
	c := newTestCancels(t)
	now := protocol.NowUTC()

	c.inject = func(op, stage string) error { return errors.New("注入: 磁盘满") }
	if err := c.add("t-1", now); err == nil {
		t.Fatal("注入写盘失败应返回错误")
	}
	if c.has("t-1") {
		t.Fatal("persist 失败后内存不得污染（has 应为 false）")
	}
	// 同键重试：注入未解除，必须再次尝试写盘并再失败（旧实现命中内存假成功）
	if err := c.add("t-1", now); err == nil {
		t.Fatal("注入未解除时同键重试不得假成功")
	}
	if c.has("t-1") {
		t.Fatal("重试失败后内存仍不得污染")
	}
	// 解除注入：重试成功，内存与盘一致
	c.inject = nil
	if err := c.add("t-1", now); err != nil {
		t.Fatalf("解除注入后 add 应成功: %v", err)
	}
	if !c.has("t-1") {
		t.Fatal("成功后墓碑应在内存")
	}
	if _, ok := readCancelsFile(t, c)["t-1"]; !ok {
		t.Fatal("盘上应有墓碑（持久化真实发生）")
	}
}

// TestCancelsClearPersistFailKept clear 落盘失败墓碑必须仍在内存（语义：
// 「未清除成功」，消费方可重试），解除注入后重试清除且盘上真实移除。
func TestCancelsClearPersistFailKept(t *testing.T) {
	c := newTestCancels(t)
	now := protocol.NowUTC()
	if err := c.add("t-2", now); err != nil {
		t.Fatalf("add: %v", err)
	}

	c.inject = func(op, stage string) error { return errors.New("注入: 磁盘满") }
	if err := c.clear("t-2"); err == nil {
		t.Fatal("注入写盘失败 clear 应返回错误")
	}
	if !c.has("t-2") {
		t.Fatal("clear persist 失败后墓碑必须仍在内存")
	}
	c.inject = nil
	if err := c.clear("t-2"); err != nil {
		t.Fatalf("解除注入后 clear 应成功: %v", err)
	}
	if c.has("t-2") {
		t.Fatal("成功清除后墓碑应不在内存")
	}
	if _, ok := readCancelsFile(t, c)["t-2"]; ok {
		t.Fatal("盘上墓碑应已移除")
	}
}

// TestCancelsCleanupPersistFailNoMemoryPrune cleanup 落盘失败内存不清理
//（先写后见同口径：盘未落，内存不替换）。
func TestCancelsCleanupPersistFailNoMemoryPrune(t *testing.T) {
	c := newTestCancels(t)
	now := protocol.NowUTC()
	if err := c.add("t-3", now); err != nil {
		t.Fatalf("add: %v", err)
	}
	c.inject = func(op, stage string) error { return errors.New("注入: 磁盘满") }
	if err := c.cleanup(now.Add(protocol.TerminalRetention+time.Hour), time.Hour); err == nil {
		t.Fatal("注入写盘失败 cleanup 应返回错误")
	}
	if !c.has("t-3") {
		t.Fatal("cleanup persist 失败后墓碑不得从内存消失")
	}
	c.inject = nil
	if err := c.cleanup(now.Add(protocol.TerminalRetention+time.Hour), time.Hour); err != nil {
		t.Fatalf("解除注入后 cleanup 应成功: %v", err)
	}
	if c.has("t-3") {
		t.Fatal("成功清理后墓碑应不在内存")
	}
}
