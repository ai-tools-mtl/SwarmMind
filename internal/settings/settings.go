// Package settings 实现管理面配置项的通用 JSON 存储（P4③ 通知渠道可配置化的
// 起步抽象；2026-10-24 随 notify 包一同自 internal/hub/settings 提升——hub/node
// 通用组件不住 hub 名下）：data_dir 下一个 JSON 文件对应一个 Store，原子写
//（临时文件+rename，对齐仓库纪律与 SPEC 4.7 先写后见思想），互斥锁串行化，
// 运行时可改。
//
// 抽象边界：只管「读写 + 原子落盘 + 调用方隔离副本」，不含校验与业务语义——
// 校验归各配置项自己的领域包（notify_channels 是第一个用户）。副本隔离经 JSON
// round-trip 实现：配置快照体量小（ KB 级），正确性优先于拷贝开销。
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 单个配置文件的内存态 + 磁盘持久化。T 为快照根结构（含切片/映射时
// View/Update 返回深拷贝副本，调用方改动不泄漏进已发布状态）。
type Store[T any] struct {
	path string
	tmp  string

	mu  sync.Mutex
	val T
}

// Open 打开（或初始化）data_dir 下名为 name 的配置存储；文件不存在时以 zero
// 值 T 起步（首次启动），文件损坏则报错（配置损坏不该静默清零，与 group 快照
// 同口径：让启动失败尽早暴露）。
func Open[T any](dataDir, name string) (*Store[T], error) {
	path := filepath.Join(dataDir, name)
	s := &Store[T]{path: path, tmp: path + ".tmp"}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings: 读取 %s 失败: %w", path, err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("settings: 配置文件损坏 %s: %w", path, err)
	}
	s.val = v
	return s, nil
}

// View 返回当前快照的深拷贝副本（调用方随便改不影响存储状态）。
func (s *Store[T]) View() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.val)
}

// Update 事务式修改：在副本上执行 mutate → 落盘成功后才替换内存可见状态；
// mutate 返回错误或落盘失败则内存零变更，调用方得到错误。mutate 内对副本的
// 改动（含校验后的规范化）随成功一并生效。
func (s *Store[T]) Update(mutate func(v *T) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.val)
	if err := mutate(&next); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: 序列化 %s 失败: %w", s.path, err)
	}
	if err := os.WriteFile(s.tmp, b, 0o600); err != nil {
		return fmt.Errorf("settings: 写临时文件失败: %w", err)
	}
	if err := os.Rename(s.tmp, s.path); err != nil {
		return fmt.Errorf("settings: 原子替换 %s 失败: %w", s.path, err)
	}
	s.val = next
	return nil
}

// clone JSON round-trip 深拷贝：T 序列化不可失败（来自 json.Unmarshal 的合法
// 值），错误分支防御性返回零值。
func clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		var zero T
		return zero
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		var zero T
		return zero
	}
	return out
}
