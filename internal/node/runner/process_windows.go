//go:build windows

package runner

import (
	"os/exec"
)

// configureProcessGroup Windows 占位（SPEC 五：Windows Job Object V1 补齐）。
// V0 不承诺 Windows 进程组；超时退化为仅杀直接子进程。
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup Windows 占位：直接杀主进程（Job Object V1）。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// reapProcessGroup Windows 占位（V0 不承诺 Windows 进程组，Job Object V1 补齐）：
// 正常返回路径无整组收尾，与超时路径同口径仅覆盖直接子进程。
func reapProcessGroup(pgid int) {}
