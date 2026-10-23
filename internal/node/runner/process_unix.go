//go:build unix

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup Unix 进程组治理（SPEC 五：V0 必做）：spawn 前置独立
// 进程组（pgid = 子进程 pid），使超时可整组终止（含 agent 派生的孙进程）。
// 返回 error 为对齐 Windows 侧签名（Job 装配可失败需致命拦截）；Unix 侧 setpgid
// 装配恒成功，恒 nil。
func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// InitSelfJobGuard Windows 兜底 Job 专属（process_windows.go，返工必修4③）：
// Unix 侧无对应概念，异常退出后组员由 init 收割，恒 nil。
func InitSelfJobGuard() error { return nil }

// launchGoverned spawn 全程（Start + Wait，语义等价 exec.Cmd.Run 一体化）：
// 抽成平台钩子是给 Windows 在 Start 与 Wait 之间挂接 Job Object（Assign 需要
// 进程句柄）；Unix 无中间动作，直通即 Run。
func launchGoverned(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}

// killProcessGroup 整组终止：kill(-pgid, SIGKILL)。cmd.Cancel（超时/取消）调用；
// 进程已退出时返回其退出错误（exec 语义允许）。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// reapWaitInterval 整组收尾的探活轮询间隔（SIGKILL 后组员由 init 收割，毫秒级
// 消失；上限 reapWaitRounds×reapWaitInterval 为异常兜底，避免 Run 永久阻塞）。
const (
	reapWaitInterval = 10 * time.Millisecond
	reapWaitRounds   = 200 // 上限 2s
)

// reapProcessGroup 正常返回路径的整组收尾：直接子进程已 Wait，但后台派生的组员
// （daemon 化孙进程）可能仍存活并继续写 workdir——探测整组存活则 SIGKILL 终止，
// 并轮询至全组消失（kill(-pgid,0) 回 ESRCH），确保产物扫描（早于 service 侧
// workdir 互斥释放）时目录再无组内写入者（P1.5 二轮必修3）。组内存活只可能是
// 子进程的派生成员（pgid 唯一来源于 Setpgid），无误伤面。
func reapProcessGroup(cmd *exec.Cmd) {
	pgid := cmd.Process.Pid
	for i := 0; i < reapWaitRounds; i++ {
		if err := syscall.Kill(-pgid, syscall.Signal(0)); err != nil {
			return // 全组已退出（ESRCH；个别组员 SIGKILL 后未及收割时继续等）
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		time.Sleep(reapWaitInterval)
	}
}
