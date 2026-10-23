// service 子命令参数解析（跨平台纯函数，无系统调用，便于非 Windows 平台单测）：
// svc 主循环与服务管理器操作本体在 service_windows.go（//go:build windows 隔离），
// 非 Windows 平台走 service_other.go 占位报错，运行时行为零变化。
package main

import (
	"errors"
	"flag"
	"fmt"
)

// serviceAction service 的五个子动作（P4②：install/remove/start/stop/status）。
type serviceAction string

const (
	svcActInstall serviceAction = "install"
	svcActRemove  serviceAction = "remove"
	svcActStart   serviceAction = "start"
	svcActStop    serviceAction = "stop"
	svcActStatus  serviceAction = "status"
)

// serviceOpts 一次 service 子命令的解析结果。
type serviceOpts struct {
	action  serviceAction
	config  string // --config（install 必填；落成服务参数前再取绝对路径）
	dataDir string // --data-dir（install 可选，随服务参数透传）
}

// parseServiceArgs 解析 service 子命令参数：
//
//	node service install --config <node.yaml 绝对路径> [--data-dir DIR]
//	node service remove | start | stop | status
//
// install 强制显式 --config：服务的 BinaryPathName 落死配置路径（服务开机自启
// 不依赖当前目录），默认值 configs/node.example.yaml 绝不能进服务。
func parseServiceArgs(args []string) (*serviceOpts, error) {
	if len(args) == 0 {
		return nil, errors.New("缺少 service 子命令（install/remove/start/stop/status）")
	}
	act := serviceAction(args[0])
	switch act {
	case svcActInstall, svcActRemove, svcActStart, svcActStop, svcActStatus:
	default:
		return nil, fmt.Errorf("未知 service 子命令 %q（支持 install/remove/start/stop/status）", args[0])
	}
	opts := &serviceOpts{action: act}
	fs := flag.NewFlagSet("service "+string(act), flag.ContinueOnError)
	fs.StringVar(&opts.config, "config", "", "node.yaml 路径（install 必填，注册前取绝对路径）")
	fs.StringVar(&opts.dataDir, "data-dir", "", "覆盖配置中的 data_dir（install 可选，随服务参数透传）")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("多余参数 %q", fs.Args())
	}
	if act == svcActInstall && opts.config == "" {
		return nil, errors.New("service install 需要 --config 指定 node.yaml（服务开机自启不依赖当前目录，禁止默认示例配置）")
	}
	return opts, nil
}
