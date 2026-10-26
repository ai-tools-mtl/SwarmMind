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
	"maps"
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

	// inject 墓碑落盘故障注入（仅包内测试设置，生产恒为 nil）：op+stage 拦截，
	// 与 Ledger.inject 同风格。
	inject func(op, stage string) error
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

// add 登记墓碑（幂等，重复登记不更新时刻）。顺带 TTL 清理超期墓碑。
// 先写后见（2026-10-26 双审缺陷修复）：在副本上构造目标状态，落盘成功才替换
// 内存 map——persist 失败内存不污染、同键重试不得假成功（旧实现先改内存后写盘，
// 失败后重试命中内存直接跳过写盘，重启丢墓碑，迟到 offer 可执行已取消任务）。
func (c *cancelTombstones) add(taskID string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.pruneCloneLocked(now)
	if _, ok := next[taskID]; ok {
		return nil
	}
	next[taskID] = now
	if err := c.persistMapLocked("AddCancelTombstone", next); err != nil {
		return err
	}
	c.m = next
	return nil
}

// has 报告墓碑是否存在（TTL 内）。
func (c *cancelTombstones) has(taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[taskID]
	return ok
}

// clear 消费墓碑（幂等：不存在视为已清，不报错）。先写后见同 add：落盘成功
// 才从内存移除，persist 失败墓碑仍在内存（消费方可重试）。
func (c *cancelTombstones) clear(taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[taskID]; !ok {
		return nil
	}
	next := make(map[string]time.Time, len(c.m))
	maps.Copy(next, c.m)
	delete(next, taskID)
	if err := c.persistMapLocked("ClearCancelTombstone", next); err != nil {
		return err
	}
	c.m = next
	return nil
}

// cleanup 清理超 TTL 的墓碑（TTL 兕底入口；幂等）。先写后见同 add。
func (c *cancelTombstones) cleanup(now time.Time, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.pruneCloneLocked(now)
	if len(next) == len(c.m) {
		return nil
	}
	if err := c.persistMapLocked("CleanupCancelTombstones", next); err != nil {
		return err
	}
	c.m = next
	return nil
}

// pruneCloneLocked TTL 清理的纯函数形态（须持锁）：返回剔除超期条目的新副本，
// 不改 c.m。TTL 基准 TerminalRetention（对应任务即便后到也早过保留期，无需再挡）。
func (c *cancelTombstones) pruneCloneLocked(now time.Time) map[string]time.Time {
	next := make(map[string]time.Time, len(c.m))
	for id, at := range c.m {
		if at.Add(protocol.TerminalRetention).Before(now) {
			continue
		}
		next[id] = at
	}
	return next
}

// pruneExpiredLocked TTL 清理（须持锁，原地改）；仅供加载路径使用：刚从盘上读出
// 的集合清理后内存生效，盘上旧样无不一致风险，清理结果随下次成功写入持久化。
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

// persistMapLocked 指定集合的临时文件+rename 原子落盘（须持锁）：序列化参数集合
// 而非 c.m——先写后见纪律的落盘半步，成功后由调用方替换内存。op 供故障注入定位。
func (c *cancelTombstones) persistMapLocked(op string, m map[string]time.Time) error {
	if c.inject != nil {
		if err := c.inject(op, stageBeforeWrite); err != nil {
			return err
		}
	}
	b, err := json.Marshal(m)
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
