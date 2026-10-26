// 「同时通知」开关（2026-10-26 伦哥裁决，TASK-notify-unify 改动三）：networked
// 模式下 hub 渠道照常通知，本机任务终态是否**跟发**本机渠道由本开关决定——
// true（默认）=本机渠道也发一份（渠道未配置自然静默，零副作用）；false=本机
// 静默（hub 一份照发）。standalone 本机通知即唯一通知，开关不参与门控（存储
// 仍在，读方忽略）；setup 无任务不触达。
//
// 实现口径（任务书钦定）：每次 TaskFinal 读一次盘（KB 级小文件，notifyTerminal
// 本就运行在收尾 goroutine 不阻塞执行），不做内存缓存——开关要运行时即时生效
//（管理面 PUT 落盘，下一个终态即按新值），盘是唯一真相、读盘是最简单正确解，
// 引入锁/缓存只会带来失效复杂度。读失败按默认 true 处理：配置损坏不吞通知。
//
// 写路径串行化（codex 审查必修 1）：settings.Open 每次调用都返回新 Store，其
// 内部互斥锁只在单实例内生效——并发 SetAlsoNotify 各持各的 Store，读-改-写
// 互不知晓，且共用同一固定 .tmp 临时文件互相截断，rename 可能把半截 JSON 发布
// 成正式文件（后续读盘全走「损坏回退 true」）。故按最终落盘路径做包级串行化：
// 同路径的写事务（Open 读盘 → Update 落盘）持同一把 per-path 锁，读盘设计不变。
// 读路径（AlsoNotify）不加锁：落盘走 temp+rename 原子替换，读者只见完整新旧
// 其一，无撕裂可能。
package notify

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/mtl802/swarmmind/internal/settings"
)

// SettingsFile notify_settings.json 快照根结构。
type SettingsFile struct {
	// AlsoNotify 同时通知开关。指针形态承载「文件未写 = 默认 true」——bool 零值
	// 是 false，直接用 bool 会把未配置误读成关闭。落盘时由 SetAlsoNotify 写具体值。
	AlsoNotify *bool `json:"also_notify,omitempty"`
}

// settingsFileName 开关存储文件名（读写两路共用，锁键由此派生）。
const settingsFileName = "notify_settings.json"

// 写路径 per-path 锁登记表（惰性建锁；锁与 Store 同生灭不同——表项常驻，路径
// 集合有限（每 data_dir 每文件名一项），无泄漏之虞）。
var (
	writeLocksMu sync.Mutex
	writeLocks   = map[string]*sync.Mutex{}
)

// lockWritePath 取 dataDir/name 的写锁，返回解锁函数。锁键是最终落盘路径——
// 与 settings.Store 内部 tmp=path+".tmp" 一一对应，串行化即消除 .tmp 互踩。
func lockWritePath(dataDir, name string) func() {
	p := filepath.Join(dataDir, name)
	writeLocksMu.Lock()
	mu, ok := writeLocks[p]
	if !ok {
		mu = &sync.Mutex{}
		writeLocks[p] = mu
	}
	writeLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// AlsoNotify 读「同时通知」开关（每次终态事件调用，读盘）：未配置/读失败 =
// 默认 true（跟发；渠道未配置自然静默）。无锁读：写侧 temp+rename 原子替换，
// 本侧只见完整文件。
func AlsoNotify(dataDir string) bool {
	st, err := settings.Open[SettingsFile](dataDir, settingsFileName)
	if err != nil {
		return true
	}
	v := st.View().AlsoNotify
	return v == nil || *v
}

// SetAlsoNotify 写「同时通知」开关：per-path 锁串行化整个读-改-写（Open 读盘
// 取最新底稿 → Update 原子落盘），并发保存不再互相截断或丢更新。
func SetAlsoNotify(dataDir string, v bool) error {
	unlock := lockWritePath(dataDir, settingsFileName)
	defer unlock()
	st, err := settings.Open[SettingsFile](dataDir, settingsFileName)
	if err != nil {
		return fmt.Errorf("notify: 打开通知设置失败: %w", err)
	}
	return st.Update(func(f *SettingsFile) error {
		f.AlsoNotify = &v
		return nil
	})
}
