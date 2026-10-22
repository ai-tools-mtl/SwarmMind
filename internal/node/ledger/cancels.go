// cancels.go node 侧取消墓碑（2026-10-22 修复轮2）：task_cancel 先于 task_offer
// 到达时任务尚不在账本（offer 丢失后重推、取消补发先于重投等竞态），此前仅回
// task_cancelled ack——随后同 task_id 的 offer 后到仍会照常执行。墓碑把「该任务
// 已被管理面取消」持久化下来：offer 到达查墓碑即拒绝执行，直接以
// cancelled/user_cancelled 落账本终态并回传（堵死「取消先于 offer，offer 后到
// 仍执行」窗口）。
//
// 持久化为 data_dir 下独立 sidecar cancels.json：与 ledger.json 同目录、同 flock
// 保护；不并入 ledger.json 是为兼容既有账本文件格式（旧账本零迁移、旧代码可读
// 新账本）。写入对齐账本落盘纪律：临时文件+rename 原子替换，互斥锁串行。清理
// 两条路：消费即清（offer 命中墓碑、cancelled 终态落盘后——账本条目此后由
// ErrDuplicate 挡住重复执行，墓碑使命完成）与 TTL 兜底（超 TerminalRetention 的
// 墓碑在加载时与每次新增时顺带清理——对应任务即便后到也早过保留期，无需再挡）。
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// cancelsFileName 取消墓碑 sidecar 文件名（相对 data_dir）。
const cancelsFileName = "cancels.json"

// cancelsTmpName 落盘临时文件名：同目录写入后 rename，保证替换原子（SPEC 4.7）。
const cancelsTmpName = cancelsFileName + ".tmp"

// cancelTombstones 取消墓碑集合：task_id → 墓碑写入时刻（UTC，TTL 基准）。
type cancelTombstones struct {
	path string
	tmp  string

	mu sync.Mutex
	m  map[string]time.Time
}

// openCancelTombstones 加载（或初始化）墓碑集合；顺带执行一次 TTL 清理（清理
// 结果随下次写入持久化；只读不写时超期墓碑在内存即已不可见）。
func openCancelTombstones(dataDir string) (*cancelTombstones, error) {
	c := &cancelTombstones{
		path: filepath.Join(dataDir, cancelsFileName),
		tmp:  filepath.Join(dataDir, cancelsTmpName),
		m:    map[string]time.Time{},
	}
	b, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil // 首次启动：空墓碑集合
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: 读取取消墓碑失败: %w", err)
	}
	if err := json.Unmarshal(b, &c.m); err != nil {
		return nil, fmt.Errorf("ledger: 取消墓碑文件损坏 %s: %w", c.path, err)
	}
	c.pruneExpiredLocked(protocol.NowUTC()) // 加载即 TTL 清理（内存生效，随下次写入持久化）
	return c, nil
}

// add 登记墓碑（幂等，重复登记不更新时刻）。顺带 TTL 清理超期墓碑并持久化。
func (c *cancelTombstones) add(taskID string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked(now)
	if _, ok := c.m[taskID]; ok {
		return nil
	}
	c.m[taskID] = now
	return c.persistLocked()
}

// has 报告墓碑是否存在（TTL 内）。
func (c *cancelTombstones) has(taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[taskID]
	return ok
}

// clear 消费墓碑（幂等：不存在视为已清，不报错）。
func (c *cancelTombstones) clear(taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[taskID]; !ok {
		return nil
	}
	delete(c.m, taskID)
	return c.persistLocked()
}

// cleanup 清理超 TTL 的墓碑（TTL 兜底入口；幂等）。
func (c *cancelTombstones) cleanup(now time.Time, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := c.pruneExpiredLocked(now)
	if !removed {
		return nil
	}
	return c.persistLocked()
}

// pruneExpiredLocked TTL 清理（须持锁）；报告是否有变化。
func (c *cancelTombstones) pruneExpiredLocked(now time.Time) bool {
	changed := false
	for id, at := range c.m {
		if at.Add(protocol.TerminalRetention).Before(now) {
			delete(c.m, id)
			changed = true
		}
	}
	return changed
}

// persistLocked 临时文件+rename 原子落盘（须持锁）。
func (c *cancelTombstones) persistLocked() error {
	b, err := json.Marshal(c.m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(c.tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: 写取消墓碑临时文件失败: %w", err)
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("ledger: 写取消墓碑临时文件失败: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("ledger: 取消墓碑刷盘失败: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("ledger: 关闭取消墓碑临时文件失败: %w", err)
	}
	if err = os.Rename(c.tmp, c.path); err != nil {
		return fmt.Errorf("ledger: 取消墓碑 rename 落盘失败: %w", err)
	}
	return nil
}
