// 服务模式文件日志（P4②）：node 以 Windows 服务运行时无控制台，标准 log 的
// 默认 stderr 不可见。选文件而非 Windows 事件日志：事件日志需先注册事件源
// （registry 写入 + 权限），且 log 包不能直写；文件追加写 + 16MiB 单代轮转
// （node.log → node.log.old），轮转在打开与每次写入时检查（返工建议5：长期
// 运行不无限增长）。本文件无平台相关系统调用、跨平台可测；仅 service_windows.go
// 在服务模式下接入 run()，非 Windows 平台运行时行为零变化。
package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	serviceLogName = "node.log"              // data_dir/logs/ 下的服务日志文件名
	serviceLogOld  = serviceLogName + ".old" // 轮转保留的旧档（仅一代，直接覆盖）
)

// serviceLogRotate 触发轮转的大小上限（16MiB）；var 仅为单测收缩阈值。
var serviceLogRotate int64 = 16 << 20

// rotatingWriter 单代轮转日志写入器：Write 前检查累计大小，超限即轮转。轮转
// 失败不阻塞日志（保原档继续追加，宁可超限不丢日志）。
type rotatingWriter struct {
	mu   sync.Mutex
	dir  string
	path string
	f    *os.File
	size int64
}

// openServiceLog 打开服务日志：data_dir/logs/node.log 追加写，打开时已超限先
// 轮转（node.log → node.log.old 留一代旧档）；返回带写入时轮转的写入器，调用
// 方负责 Close。
func openServiceLog(dataDir string) (io.WriteCloser, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{dir: dir, path: filepath.Join(dir, serviceLogName)}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// open 打开日志文件并记录当前大小基准；已超限立即轮转（原行为，跨重启上限
// 依然成立）。
func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.size = fi.Size()
	if w.size > serviceLogRotate {
		w.rotate()
	}
	return nil
}

// Write 每次写入前检查超限并轮转。log.Logger 内部互斥串行调用，锁防御其他
// io 直写方。
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, errors.New("servicelog: 已关闭")
	}
	if w.size+int64(len(p)) > serviceLogRotate {
		w.rotate()
		if w.f == nil {
			return 0, errors.New("servicelog: 轮转后重开失败")
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate 轮转（调用方持锁）：关旧档 → 改名留一代旧档 → 重开新档从空追加。
// 任何失败分支都重开原路径续写，writer 绝不停在 nil——旧实现失败即留 nil，
// 后续写入全被 Write 拒绝，日志永久丢失（返工建议修2）：改名失败（.old 被
// tail 等占用是 Windows 常态）= 保原档继续追加，下次写入再试轮转。
func (w *rotatingWriter) rotate() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if err := os.Rename(w.path, filepath.Join(w.dir, serviceLogOld)); err != nil {
		w.reopen() // 改名失败：原档原位续写
		return
	}
	w.reopen()
}

// reopen 重开原路径追加写并刷新大小基准（改名成功开新档 size=0；改名失败续旧
// 档 Stat 取实际大小，预算不失真）。瞬时占锁（杀软扫描等）重试 3 次；穷尽仍
// 失败才留 nil（文件系统彻底不可用，无进一步回退），由 Write 的 nil 防御报错。
func (w *rotatingWriter) reopen() {
	for i := 0; ; i++ {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			w.f = f
			w.size = 0
			if fi, err := f.Stat(); err == nil {
				w.size = fi.Size()
			}
			return
		}
		if i >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Close 关闭当前日志档（幂等）。
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
