// service 子命令参数解析单测（跨平台：parseServiceArgs 是纯函数，无系统调用，
// mac/linux 上即可覆盖 install/remove/start/stop/status 全部分支）。
package main

import (
	"strings"
	"testing"
)

func TestParseServiceArgsInstall(t *testing.T) {
	opts, err := parseServiceArgs([]string{"install", "--config", `C:\swarmmind\node.yaml`, "--data-dir", `C:\swarmmind\data`})
	if err != nil {
		t.Fatalf("install 解析失败: %v", err)
	}
	if opts.action != svcActInstall {
		t.Errorf("action = %q, want install", opts.action)
	}
	if opts.config != `C:\swarmmind\node.yaml` {
		t.Errorf("config = %q, want C:\\swarmmind\\node.yaml", opts.config)
	}
	if opts.dataDir != `C:\swarmmind\data` {
		t.Errorf("dataDir = %q, want C:\\swarmmind\\data", opts.dataDir)
	}
}

// install 必填 --config：服务的 BinaryPathName 落死配置路径，示例配置/默认值
// 绝不能进服务。
func TestParseServiceArgsInstallRequiresConfig(t *testing.T) {
	if _, err := parseServiceArgs([]string{"install"}); err == nil {
		t.Fatal("install 缺 --config 应报错")
	} else if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("报错应提示 --config，got: %v", err)
	}
	// 其余动作不要求 --config（服务里已落死）
	for _, act := range []serviceAction{svcActRemove, svcActStart, svcActStop, svcActStatus} {
		if _, err := parseServiceArgs([]string{string(act)}); err != nil {
			t.Fatalf("%s 不应要求参数: %v", act, err)
		}
	}
}

func TestParseServiceArgsRejects(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // 报错须包含的片段
	}{
		{"无子命令", nil, "缺少 service 子命令"},
		{"未知子命令", []string{"restart"}, "未知 service 子命令"},
		{"多余位置参数", []string{"start", "extra"}, "多余参数"},
		{"未知旗标", []string{"install", "--force"}, "flag provided"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseServiceArgs(c.args); err == nil {
				t.Fatalf("%v 应报错", c.args)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("报错应含 %q，got: %v", c.want, err)
			}
		})
	}
}
