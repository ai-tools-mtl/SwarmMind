//go:build !windows

// 非 Windows 平台占位：服务化子命令不可用（Linux 用 systemd、macOS 用 launchd，
// 部署模板与步骤见 docs/DEPLOY-P2.md）。stub 仅保持跨平台编译面一致，运行时
// 行为零变化（既有 systemd/launchd 路径不受任何影响）。
package main

import "errors"

// serviceCommand Windows 专属，其余平台如实报错。
func serviceCommand(_ []string) error {
	return errors.New("service 子命令仅 Windows 支持（Linux 用 systemd / macOS 用 launchd，见 docs/DEPLOY-P2.md）")
}

// maybeRunService 非 Windows 恒不命中（无 SCM 概念）。
func maybeRunService(_, _ string) (bool, error) {
	return false, nil
}
