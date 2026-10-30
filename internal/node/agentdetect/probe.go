// --version 子进程纪律（任务书硬约束）：只运行调用方显式给出的命令，argv 固定
// [<command>, "--version"]，不经 shell、不接受附加参数；超时 kill 后必 Wait；
// 输出合流设上限、超限截断并持续排空管道；最小干净 env；路径存在判定 Lstat
// 不跟随符号链接。
package agentdetect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// probeWaitDelay ctx 到点 kill 之后 Wait 对「孙进程持有管道」的等待上界
// （os/exec ≥ go1.20）：防 --version 某些包装脚本 spawn 的子进程握住 stdout
// 导致 Wait 挂死。正常 CLI 远小于此值。
const probeWaitDelay = time.Second

// minimalProbeEnv 干净最小环境：固定 PATH（命令为绝对路径，本探测自身无需
// PATH 搜索；给最小 PATH 是为 CLI 内部可能的组件查找兜底）+ LANG=C（稳定
// 输出）。绝不继承进程全量环境——防凭证/代理等环境变量泄漏进子进程。
var minimalProbeEnv = []string{
	"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	"LANG=C",
}

// probeResult --version 探测结果（原样留痕 + 分类失败原因素材）。
type probeResult struct {
	Raw       string // stdout+stderr 合流原样输出（超限截断后的前缀）
	Truncated bool   // 输出超上限被截断
	Timeout   bool   // 探测超时（进程已 kill 并 Wait）
	Canceled  bool   // 调用方 ctx 取消
	StartErr  error  // 启动失败（路径不存在/格式错误/权限等）
	NonZero   bool   // 退出码非零
	ExitCode  int    // NonZero 时的退出码
	WaitErr   error  // 启动后、非退出码类异常（罕见）
}

// probeVersion 执行 <command> --version。ctx 取消/超时 → kill 后必 Wait 收尸
// （AGENTS exec 纪律：kill 不 Wait = 僵尸堆积）。
func probeVersion(ctx context.Context, command string, timeout time.Duration, limit int64) probeResult {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// argv 数组直传：不经 shell、无拼接、调用方无法注入附加参数——命令行恒为
	// [<command>, "--version"] 两个元素。
	cmd := exec.CommandContext(runCtx, command, "--version")
	cmd.Env = minimalProbeEnv
	capBuf := &cappedBuffer{max: limit}
	cmd.Stdout = capBuf
	cmd.Stderr = capBuf
	cmd.WaitDelay = probeWaitDelay

	var res probeResult
	if err := cmd.Start(); err != nil {
		res.StartErr = err
		return res
	}
	waitErr := cmd.Wait()
	res.Raw = capBuf.buf.String()
	res.Truncated = capBuf.overflow
	if ctxErr := runCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			res.Timeout = true
		} else {
			res.Canceled = true
		}
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.NonZero = true
		res.ExitCode = exitErr.ExitCode()
		return res
	}
	if waitErr != nil {
		res.WaitErr = waitErr
	}
	return res
}

// nonZeroReason 非零退出的原因文案（输出仍留痕，但不采信为安装证据）。
func (r probeResult) nonZeroReason() string {
	return fmt.Sprintf("--version 退出码 %d（非零，输出仅留痕不采信为安装证据）", r.ExitCode)
}

// overLimitReason 输出超限的原因文案（截断留痕 + 不采信）。
func (r probeResult) overLimitReason(limit int64) string {
	return fmt.Sprintf("版本输出超过捕获上限 %d 字节（已截断留痕；正常安装的 --version 远小于上限，超限即异常）", limit)
}

// pathFacts Lstat 存在判定结果（不跟随符号链接）。
type pathFacts struct {
	Exists  bool
	Symlink bool
	Regular bool
	Err     error
}

// ref 折算成路径引用元数据（路径只回引用，永不回内容）。
func (f pathFacts) ref(path, kind string) PathRef {
	return PathRef{Path: path, Kind: kind, Exists: f.Exists, Regular: f.Regular, Symlink: f.Symlink}
}

// lstatPath Lstat 探路径事实：存在/符号链接/普通文件三判定一次完成，不跟随
// 链接（任务书：链接=如实注记 symlink，不做逃逸展开）。
func lstatPath(path string) pathFacts {
	fi, err := os.Lstat(path)
	if err != nil {
		return pathFacts{Err: err}
	}
	return pathFacts{
		Exists:  true,
		Symlink: fi.Mode()&os.ModeSymlink != 0,
		Regular: fi.Mode().IsRegular(),
	}
}

// isAbsPath 绝对路径判定（filepath.IsAbs 直封装：裸名/相对路径在调用方侧拒绝，
// 杜绝 exec.Command 的隐式 PATH 搜索）。
func isAbsPath(p string) bool {
	return filepath.IsAbs(p)
}

// cappedBuffer 上限捕获缓冲：超限后不再存储但持续消费字节（排空管道，防子进程
// 写 stdout 阻塞死锁），overflow 置位供截断注记。
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int64
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - int64(c.buf.Len()); room < int64(len(p)) {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.overflow = true
		return len(p), nil // 全量吞下计数，只是不再存储
	}
	c.buf.Write(p)
	return len(p), nil
}
