// persist.go 循环事实持久化文件层（W09 收尾加固 A，FINAL-REPORT §4.3 / 采证 O2）：
// Engine.Snapshot/RestoreLoop 的装配层接线——node/hub 两端同态同形（同名同格式
// data_dir/strategy_loops.json，写入策略同取「变更即同步写」零丢失窗；循环事实
// 变更频率 = 轮终/候选级，低频）。
//
// 文件形态：{"version":1,"loops":[SpaceLoop...]}（MarshalIndent 人读友好，对照
// collab spaces.json 先例）；时间一律 UTC（写入面时间戳由 Engine 时钟统一 UTC，
// 序列化即 RFC3339 Z 形态）。落盘原子写（固定独占 tmp + fsync + rename + 目录
// fsync 尽力而为，0600；AGENTS.md 禁直接覆盖写，对照 collab Store.persist 同款）。
//
// 启动恢复：LoadLoops 文件不存在 = 零操作（老部署/首次启动，如实空态）；JSON 损
// 坏 / version 缺失或未知 / 任一条目 RestoreLoop 校验失败 = 错误返回（装配层拒绝
// 启动，错误列明空间 ID 与原因）——同 spaces.json 宁拒启口径：部分恢复会让完成
// 判据半真半假，不带病。
//
// 死锁纪律：SaveLoops 组包经 Engine.LoopIDs/Snapshot 取读锁——只允许在 Config.
// OnPersist 触发点（写锁释放后，见 notifyPersist）调用；RestoreLoop 不触发通知
// （恢复导入的输入即本文件，见其注记）。
package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// LoopStoreFile 循环事实持久化文件名（node/hub 两端同态同形：data_dir 下同名）。
const LoopStoreFile = "strategy_loops.json"

// loopStoreVersion 磁盘格式版本（唯一受支持值；缺失或未知拒绝——不猜格式）。
const loopStoreVersion = 1

// loopStoreFile 磁盘文档形态。
type loopStoreFile struct {
	Version int         `json:"version"`
	Loops   []SpaceLoop `json:"loops"`
}

// loopsStoreMu 落盘串行锁：并发变更各自触发 OnPersist（同步写）时，两次组包在
// 同一目标文件上 temp+rename 会交错——串行化后每次写都是完整当前状态（写旧不写
// 错：后写覆盖先写，内容均为合法快照点）。
var loopsStoreMu sync.Mutex

// NewPersistedEngine 构造带持久化接线的策略引擎（node/hub 装配单源，两端同态同
// 形）：Config.OnPersist → SaveLoops 变更即同步写 dir/strategy_loops.json（落盘
// 失败经 logf 记日志、不中断引擎判定——判定可用性优先，丢失窗由下次变更补写全
// 量快照），随后启动恢复 LoadLoops（文件缺失 = 零操作空态；损坏/校验失败 = 错误
// 返回，装配层拒绝启动）。logf 可 nil（静默）。
func NewPersistedEngine(dir string, cfg Config, logf func(format string, args ...any)) (*Engine, error) {
	var eng *Engine
	cfg.OnPersist = func() {
		if err := SaveLoops(dir, eng); err != nil && logf != nil {
			logf("strategy: 循环事实落盘失败 dir=%s: %v", dir, err)
		}
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		return nil, err
	}
	if err := LoadLoops(dir, eng); err != nil {
		return nil, err
	}
	return eng, nil
}

// SaveLoops 引擎全部已载入循环事实全量快照落盘（原子写 0600；SnapshotsAll 单读
// 锁一致组包——枚举与逐空间取照同临界区，并发变更下文件恒为内存某时点的完整合
// 法形态；loops 按空间 ID 排序——文件内容确定性）。可在 OnPersist 回调（写锁释
// 放后）或测试中直接调用。
func SaveLoops(dir string, eng *Engine) error {
	if eng == nil {
		return fmt.Errorf("strategy: SaveLoops 引擎为空")
	}
	loopsStoreMu.Lock()
	defer loopsStoreMu.Unlock()
	doc := loopStoreFile{Version: loopStoreVersion, Loops: eng.SnapshotsAll()}
	b, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return fmt.Errorf("strategy: 循环事实序列化失败: %w", err)
	}
	b = append(b, '\n')
	if err := atomicWriteLoops(filepath.Join(dir, LoopStoreFile), b); err != nil {
		return fmt.Errorf("strategy: 循环事实落盘失败: %w", err)
	}
	return nil
}

// atomicWriteLoops 原子写（固定独占 tmp + fsync + rename + 目录 fsync 尽力而为；
// 0600）。形态对照 collab Store.persist：上次落盘崩溃残留的 tmp 清掉重试一次
// （磁盘旧正本未动，安全）；目录 fsync 失败记日志可观测（同 collab 口径——rename
// 已完成，正本已在位，不回滚不报错）。
func atomicWriteLoops(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		if rmErr := os.Remove(tmp); rmErr != nil {
			return fmt.Errorf("清理残留临时文件失败: %w", rmErr)
		}
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename 落盘失败: %w", err)
	}
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		if serr := d.Sync(); serr != nil {
			log.Printf("strategy: 循环事实目录 fsync 失败（尽力而为，rename 已完成）: %v", serr)
		}
		_ = d.Close()
	} else {
		log.Printf("strategy: 循环事实目录打开失败（目录 fsync 跳过，rename 已完成）: %v", derr)
	}
	return nil
}

// LoadLoops 启动恢复：读 dir/strategy_loops.json 逐空间 RestoreLoop 导入引擎。
// 文件不存在 = 零操作（返回 nil——老部署/首次启动，如实空态）；JSON 损坏 /
// version 缺失或未知 / 任一条目校验失败 = 错误返回（错误列明条目与空间 ID 及原
// 因；引擎零残留——失败条目之前已导入的空间由调用方弃用整个引擎，装配层拒绝启
// 动，不部分带病运行）。
func LoadLoops(dir string, eng *Engine) error {
	if eng == nil {
		return fmt.Errorf("strategy: LoadLoops 引擎为空")
	}
	path := filepath.Join(dir, LoopStoreFile)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 老部署/首次启动：零操作，如实空态
		}
		return fmt.Errorf("strategy: 读取循环事实文件失败: %w", err)
	}
	var doc loopStoreFile
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("strategy: %s 损坏（JSON 解析失败，拒绝启动）: %w", LoopStoreFile, err)
	}
	if doc.Version != loopStoreVersion {
		return fmt.Errorf("strategy: %s version=%d 非 %d（version 缺失或未知格式，拒绝启动——同 spaces.json 宁拒启口径）",
			LoopStoreFile, doc.Version, loopStoreVersion)
	}
	for i := range doc.Loops {
		entry := doc.Loops[i]
		if err := eng.RestoreLoop(entry.SpaceID, entry); err != nil {
			return fmt.Errorf("strategy: %s 条目 %d（空间 %q）恢复失败，拒绝启动: %w", LoopStoreFile, i, entry.SpaceID, err)
		}
	}
	return nil
}
