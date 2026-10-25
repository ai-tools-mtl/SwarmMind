package decision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// primeHomeFixture 伪造 prime-home 布局（.zcode/v2/provider_config.json 实机 shape）。
func primeHomeFixture(t *testing.T, rulesJSON string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, PrimeHomeFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"config":{"providerConfigRules":{"providerRules":[` + rulesJSON + `]}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const ruleGLM = `{"providerId":"glm-coding","config":{"access":{"apiKey":"glm-key-1"},"api":{"baseUrl":"https://open.bigmodel.cn/api/coding/paas/v4"}}}`
const ruleOther = `{"providerId":"other","config":{"access":{"apiKey":"other-key"},"api":{"baseUrl":"https://other.example/v1"}}}`

func TestPrimeHomeKey(t *testing.T) {
	t.Run("命中 GLM 规则", func(t *testing.T) {
		dir := primeHomeFixture(t, ruleOther+","+ruleGLM)
		key, base, ok := primeHomeKey(dir)
		if !ok || key != "glm-key-1" || !strings.Contains(base, "bigmodel") {
			t.Fatalf("应命中 glm 规则: %q %q %v", key, base, ok)
		}
	})
	t.Run("无 GLM 规则退第一条有 key 的", func(t *testing.T) {
		dir := primeHomeFixture(t, ruleOther)
		key, _, ok := primeHomeKey(dir)
		if !ok || key != "other-key" {
			t.Fatalf("应退第一条: %q %v", key, ok)
		}
	})
	t.Run("缺失/非法/无规则", func(t *testing.T) {
		if _, _, ok := primeHomeKey(t.TempDir()); ok {
			t.Fatalf("文件缺失应不命中")
		}
		dir := primeHomeFixture(t, `broken`)
		if _, _, ok := primeHomeKey(dir); ok {
			t.Fatalf("非法 JSON 应不命中")
		}
		dir = primeHomeFixture(t, `{"providerId":"x","config":{"access":{"apiKey":""}}}`)
		if _, _, ok := primeHomeKey(dir); ok {
			t.Fatalf("空 key 规则应不命中")
		}
	})
}

func TestPrimeHomePathResolution(t *testing.T) {
	getenv := func(k string) string {
		if k == "SWARM_PRIME_HOME" {
			return "/from-env"
		}
		return ""
	}
	if got := primeHomePath("/explicit", getenv, "/home"); got != "/explicit" {
		t.Fatalf("显式配置应最优先: %s", got)
	}
	if got := primeHomePath("", getenv, "/home"); got != "/from-env" {
		t.Fatalf("环境变量次之: %s", got)
	}
	if got := primeHomePath("", func(string) string { return "" }, "/home"); got != filepath.Join("/home", "swarm-prime-home") {
		t.Fatalf("home 回落: %s", got)
	}
}

func TestNewAssembly(t *testing.T) {
	noenv := func(string) string { return "" }
	silent := func(string, ...any) {}

	t.Run("off 不启用", func(t *testing.T) {
		s := New(Settings{Provider: "off"}, noenv, "", silent)
		if s.Enabled() {
			t.Fatal("off 应不启用")
		}
		res := s.Suggest(Request{Goal: "x", Candidates: testCandidates()})
		if res.Note != NotEnabledNote {
			t.Fatalf("未启用 note 口径: %+v", res)
		}
	})
	t.Run("非法 provider 不启用", func(t *testing.T) {
		if New(Settings{Provider: "jev-cloud"}, noenv, "", silent).Enabled() {
			t.Fatal("D1 未交付的 provider 应不启用")
		}
	})
	t.Run("三级皆空不启用", func(t *testing.T) {
		s := New(Settings{Provider: "llm-as-jev"}, noenv, t.TempDir(), silent)
		if s.Enabled() {
			t.Fatal("无 key 应不启用")
		}
	})
	t.Run("配置 key 一级命中", func(t *testing.T) {
		s := New(Settings{Provider: "llm-as-jev", APIKey: "cfg-key", Model: "m1"}, noenv, "", silent)
		if !s.Enabled() || s.ProviderName() != "llm-as-jev" {
			t.Fatalf("应启用: %+v", s)
		}
	})
	t.Run("env key 二级命中", func(t *testing.T) {
		s := New(Settings{Provider: "llm-as-jev"}, func(k string) string {
			if k == "GLM_API_KEY" {
				return "env-key"
			}
			return ""
		}, "", silent)
		if !s.Enabled() {
			t.Fatal("env key 应启用")
		}
	})
	t.Run("prime-home 三级命中且带端点", func(t *testing.T) {
		dir := primeHomeFixture(t, ruleGLM)
		// 两种注入同效：Settings.PrimeHome 显式路径 / SWARM_PRIME_HOME 环境变量。
		s2 := New(Settings{Provider: "llm-as-jev", PrimeHome: dir}, noenv, "", silent)
		if !s2.Enabled() {
			t.Fatal("prime-home key 应启用")
		}
		envHome := New(Settings{Provider: "llm-as-jev"}, func(k string) string {
			if k == "SWARM_PRIME_HOME" {
				return dir
			}
			return ""
		}, "", silent)
		if !envHome.Enabled() {
			t.Fatal("SWARM_PRIME_HOME 注入应启用")
		}
	})
	t.Run("配置端点优先于 prime-home 规则端点", func(t *testing.T) {
		dir := primeHomeFixture(t, ruleGLM)
		s := New(Settings{Provider: "llm-as-jev", PrimeHome: dir, BaseURL: "http://127.0.0.1:9/v1", APIKey: "cfg"}, noenv, "", silent)
		if !s.Enabled() {
			t.Fatal("应启用")
		}
		p, ok := s.provider.(*llmProvider)
		if !ok || p.cfg.BaseURL != "http://127.0.0.1:9/v1" {
			t.Fatalf("显式端点应最优先: %+v", s.provider)
		}
	})
	t.Run("预算注入", func(t *testing.T) {
		s := New(Settings{Provider: "llm-as-jev", APIKey: "k", TimeoutSec: 3}, noenv, "", silent)
		if s.budget.String() != "3s" {
			t.Fatalf("预算应为 3s: %v", s.budget)
		}
	})
}
