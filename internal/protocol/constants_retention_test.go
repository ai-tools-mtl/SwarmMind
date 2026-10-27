package protocol

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestParseTerminalRetention terminal_retention 配置解析（maturity-retention-config，
// hub.yaml 与 node.yaml 同源实现）：
//   - 空 = 未配置 → 默认 TerminalRetention（老配置文件零改动行为不变）；
//   - "0s" = 永不自动清理（显式语义）；
//   - 字符串 "0"（字面零免单位，Go ParseDuration 契约）= 永不自动清理，同 0s；
//   - 亚秒正值（500ms）= 拒绝：生效值按整秒下发（terminal_retention_sec），
//     亚秒整除失真为 0 会被误读成「不自动清理」而实际照删（R2 必修①）；
//   - 合法带单位时长原样解析；
//   - 非零无单位 / 负数 / 乱码 → 报错（报错含人话指引，调用方拒绝启动）。
func TestParseTerminalRetention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", TerminalRetention, true},
		{"0s", 0, true},
		{"0", 0, true}, // 字面零免单位（ParseDuration 契约）；yaml 裸写数字 0 经标量转换同样落成 "0"（YAML 路径见 TestParseTerminalRetentionYAML）
		{"30m", 30 * time.Minute, true},
		{"24h", 24 * time.Hour, true},
		{"720h", 720 * time.Hour, true},
		{"90s", 90 * time.Second, true},
		{"1s", time.Second, true},                 // 最小粒度边界内
		{"999ms", 0, false},                       // 亚秒（<1s）拒绝（R2 必修①）
		{"500ms", 0, false},                       // 亚秒实测展示失真形态（500ms 整除秒=0 → 误显「不自动清理」）
		{"1500ms", 1500 * time.Millisecond, true}, // ≥1s 合法（500ms 超出部分进秒位）
		{"-1h", 0, false},
		{"abc", 0, false},
		{"24", 0, false}, // 无单位
	}
	for _, c := range cases {
		got, err := ParseTerminalRetention(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("ParseTerminalRetention(%q) 不应报错: %v", c.in, err)
				continue
			}
			if got != c.want {
				t.Errorf("ParseTerminalRetention(%q) = %v, 期望 %v", c.in, got, c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseTerminalRetention(%q) 应报错，实际返回 %v", c.in, got)
			continue
		}
		if !strings.Contains(err.Error(), "0s") {
			t.Errorf("ParseTerminalRetention(%q) 报错应指路 0s（永不清理写法）: %v", c.in, err)
		}
	}
	// 亚秒拒绝报错文案须含人话指引「最小粒度 1 秒」（R2 必修①：报错文案同步要求）。
	_, err := ParseTerminalRetention("500ms")
	if err == nil || !strings.Contains(err.Error(), "最小粒度 1 秒") {
		t.Errorf("亚秒拒绝报错应说明最小粒度 1 秒，got: %v", err)
	}
}

// TestParseTerminalRetentionYAML YAML 加载路径（R2 必修②/建议⑤）：裸写数字 0、
// 字符串 "0"、"0s" 三态等价——yaml.v3 对标量做类型转换，裸 0 落成字符串 "0"，
// ParseDuration 对字面 0 免单位，三者均 = 永不自动清理（推翻旧声明「裸 0 在 yaml
// 层被 string 字段拒绝」——实测失实，声明认账）。结构与 cmd/hub、cmd/node 配置
// 字段同形（string 字段 + 同名 yaml tag）。
func TestParseTerminalRetentionYAML(t *testing.T) {
	t.Parallel()
	var cfg struct {
		TerminalRetention string `yaml:"terminal_retention"`
	}
	for name, doc := range map[string]string{
		"裸写数字0":  "terminal_retention: 0\n",
		"字符串0":   "terminal_retention: \"0\"\n",
		"显式0s":   "terminal_retention: 0s\n",
		"带单位24h": "terminal_retention: 24h\n",
		"未配置":    "data_dir: data\n",
	} {
		cfg.TerminalRetention = ""
		if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
			t.Fatalf("%s：yaml 解析不应报错: %v", name, err)
		}
		got, err := ParseTerminalRetention(cfg.TerminalRetention)
		want := time.Hour * 24 // 带单位与未配置 → 默认 24h
		if name != "带单位24h" && name != "未配置" {
			want = 0 // 三种零写法 → 永不自动清理
		}
		if err != nil {
			t.Errorf("%s（落盘 %q）：不应报错: %v", name, cfg.TerminalRetention, err)
			continue
		}
		if got != want {
			t.Errorf("%s（落盘 %q）= %v，期望 %v", name, cfg.TerminalRetention, got, want)
		}
	}
}
