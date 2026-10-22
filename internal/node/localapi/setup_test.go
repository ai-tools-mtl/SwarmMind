package localapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// setup 向导写盘单测（方案 3.3，复审 #8）：渲染字段与默认值、data_dir 落盘前
// 绝对路径化、yaml.Node 编码器对特殊字符的转义保真（返工 #10）、原子写盘
// （0600、无临时残留、覆盖写）。

// parseRendered 把渲染产物解析回结构体（转义保真的判定基准）。
func parseRendered(t *testing.T, out string) struct {
	NodeID        string `yaml:"node_id"`
	HubURL        string `yaml:"hub_url"`
	RegisterToken string `yaml:"register_token"`
	LocalListen   string `yaml:"local_listen"`
	DataDir       string `yaml:"data_dir"`
	Concurrency   int    `yaml:"concurrency"`
} {
	t.Helper()
	var cfg struct {
		NodeID        string `yaml:"node_id"`
		HubURL        string `yaml:"hub_url"`
		RegisterToken string `yaml:"register_token"`
		LocalListen   string `yaml:"local_listen"`
		DataDir       string `yaml:"data_dir"`
		Concurrency   int    `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("渲染产物应可解析: %v\n%s", err, out)
	}
	return cfg
}

// TestRenderSetupYamlFields 字段如实渲染 + 空缺取默认（listen/concurrency/data_dir）
// + data_dir 绝对路径化（方案 3.3：防 cwd 漂移双 data_dir 双 token）。
func TestRenderSetupYamlFields(t *testing.T) {
	v := protocol.JoinValues{
		HubURL: "wss://hub.example.com/ws", NodeID: "node-a", Token: strings.Repeat("ab", 32),
	}
	out, err := renderSetupYaml(v, &SetupOps{
		ConfigPath: "/x/node.yaml", DataDir: "data", LocalListen: "127.0.0.1:9001", Concurrency: 3,
	})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	cfg := parseRendered(t, out)
	if cfg.NodeID != v.NodeID || cfg.HubURL != v.HubURL || cfg.RegisterToken != v.Token {
		t.Fatalf("三元组未如实渲染: %+v", cfg)
	}
	if cfg.LocalListen != "127.0.0.1:9001" || cfg.Concurrency != 3 {
		t.Fatalf("listen/concurrency 不符: %+v", cfg)
	}
	if abs, _ := filepath.Abs("data"); cfg.DataDir != abs {
		t.Fatalf("data_dir 应绝对路径化: got %q want %q", cfg.DataDir, abs)
	}

	// 默认值分支：LocalListen/Concurrency/DataDir 空缺
	out2, err := renderSetupYaml(v, &SetupOps{ConfigPath: "/x/node.yaml"})
	if err != nil {
		t.Fatalf("默认渲染失败: %v", err)
	}
	cfg2 := parseRendered(t, out2)
	absRoot, _ := filepath.Abs("data")
	if cfg2.LocalListen != "127.0.0.1:8445" ||
		cfg2.Concurrency != protocol.DefaultConcurrency ||
		cfg2.DataDir != absRoot {
		t.Fatalf("默认值不符: %+v", cfg2)
	}
}

// TestRenderSetupYamlEscapes 特殊字符经 yaml.Node 编码器转义（返工 #10）：值含
// 引号/冒号/花括号/空格#等 yaml 语法字符时编码器加引号保真，读取回与原值一致
// （JoinValues 白名单拦不住的场景以编码器兜底——注入面双保险）。
func TestRenderSetupYamlEscapes(t *testing.T) {
	v := protocol.JoinValues{
		HubURL: `ws://hub.example:8080/x #frag`, NodeID: `node"x`, Token: `tok$: {y}`,
	}
	out, err := renderSetupYaml(v, &SetupOps{ConfigPath: "/x/node.yaml", DataDir: "d"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	cfg := parseRendered(t, out)
	if cfg.NodeID != v.NodeID || cfg.HubURL != v.HubURL || cfg.RegisterToken != v.Token {
		t.Fatalf("特殊字符未被转义保真:\nwant %+v\ngot  %+v\n%s", v, cfg, out)
	}
}

// TestWriteConfigFile 原子写盘：自动建父目录、路径正确、权限 0600（内嵌
// register_token 凭据）、内容完整、覆盖写、无临时文件残留。
func TestWriteConfigFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf", "node.yaml")
	content := "node_id: node-a\nhub_url: wss://hub/ws\n"
	if err := writeConfigFile(p, content); err != nil {
		t.Fatalf("写盘失败: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != content {
		t.Fatalf("内容不符: %q err=%v", b, err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置内嵌凭据应 0600, got %v", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}

	// 覆盖写：原路径原子替换
	if err := writeConfigFile(p, "node_id: node-b\n"); err != nil {
		t.Fatalf("覆盖写失败: %v", err)
	}
	if b, _ = os.ReadFile(p); string(b) != "node_id: node-b\n" {
		t.Fatalf("覆盖写未生效: %q", b)
	}
}

// TestClearHubURL 断开组网的 yaml.Node 最小编辑（矩阵 #9 服务端）：仅清 hub_url，
// 注释与未知字段保留；文件不存在/无 hub_url 字段报明确错误；重复断开幂等。
func TestClearHubURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.yaml")
	orig := "# 手编配置\nnode_id: node-a\nhub_url: wss://hub/ws\nregister_token: " + strings.Repeat("ab", 32) + "\n# 尾注\n"
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearHubURL(p); err != nil {
		t.Fatalf("断开失败: %v", err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.Contains(s, "node_id: node-a") || !strings.Contains(s, "register_token") {
		t.Fatalf("其余字段应保留: %s", s)
	}
	if !strings.Contains(s, "hub_url:") || strings.Contains(s, "wss://") {
		t.Fatalf("hub_url 键应保留且值已清空: %s", s)
	}
	// 已断开后重复调用：幂等成功
	if err := clearHubURL(p); err != nil {
		t.Fatalf("重复断开应幂等成功: %v", err)
	}
	// 文件不存在：明确错误
	if err := clearHubURL(filepath.Join(dir, "ghost.yaml")); err == nil {
		t.Fatal("文件不存在应报错")
	}
}
