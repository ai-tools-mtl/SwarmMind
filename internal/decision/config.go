// 决策服务装配与密钥解析（P5.1 D1）：GLM API key 三级回落——hub.yaml decision.api_key
// → 环境变量 GLM_API_KEY → swarm-prime-home（`.zcode/v2/provider_config.json` 的
// providerRules[].config.access.apiKey，PRIME 链路既有做法）。不硬编码任何密钥；
// 解析仅装配时一次，日志只记来源类别与端点/型号，不落 key 值。
package decision

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Settings hub.yaml decision 段解析产物（cmd/hub 装配注入）。
type Settings struct {
	Provider   string // llm-as-jev（D1 唯一启用值）；off/空 = 不启用
	Model      string // 缺省 defaultLLMModel
	BaseURL    string // 缺省 defaultLLMBaseURL（显式配置优先于 prime-home 规则 baseUrl）
	APIKey     string // 一级：显式配置
	PrimeHome  string // prime-home 路径（三级解析）；空回落 SWARM_PRIME_HOME，再回落 $HOME/swarm-prime-home
	TimeoutSec int    // 单次建议预算秒数；<=0 取 DefaultBudget（15s，GLM 尾延迟实测口径）
}

// PrimeHomeFile prime-home 内 GLM 凭证载体（zcode v2 provider 配置实测布局）。
const PrimeHomeFile = ".zcode/v2/provider_config.json"

// primeRule provider_config.json 的单条 provider 规则（仅取需要的字段）。
type primeRule struct {
	ProviderID string `json:"providerId"`
	Config     struct {
		Access struct {
			APIKey string `json:"apiKey"`
		} `json:"access"`
		API struct {
			BaseURL string `json:"baseUrl"`
		} `json:"api"`
	} `json:"config"`
}

func (r primeRule) isGLM() bool {
	id := strings.ToLower(r.ProviderID)
	return strings.Contains(id, "glm") || strings.Contains(strings.ToLower(r.Config.API.BaseURL), "bigmodel")
}

// primeHomeConfig 顶层解析视图。
type primeHomeConfig struct {
	Config struct {
		ProviderConfigRules struct {
			ProviderRules []primeRule `json:"providerRules"`
		} `json:"providerConfigRules"`
	} `json:"config"`
}

// New 装配决策服务。返回 *Service（未启用时 Enabled()=false 的可用实例，查询恒
// 降级——端点形态稳定，调用方无需探活）。logf 注入便于测试静默；nil 用 log.Printf。
func New(s Settings, getenv func(string) string, homeDir string, logf func(format string, args ...any)) *Service {
	if logf == nil {
		logf = log.Printf
	}
	prov := strings.ToLower(strings.TrimSpace(s.Provider))
	if prov != "llm-as-jev" {
		if prov != "" && prov != "off" {
			logf("[hub] decision.provider=%q 非法（D1 合法值 llm-as-jev，或留空/off 不启用）：决策服务不启用", s.Provider)
		}
		return &Service{}
	}
	budget := time.Duration(s.TimeoutSec) * time.Second
	if s.TimeoutSec <= 0 {
		budget = DefaultBudget
	}
	key, source := strings.TrimSpace(s.APIKey), "config"
	if key == "" && getenv != nil {
		key = strings.TrimSpace(getenv("GLM_API_KEY"))
		source = "env"
	}
	baseURL := strings.TrimSpace(s.BaseURL)
	if key == "" {
		pk, pBase, ok := primeHomeKey(primeHomePath(s.PrimeHome, getenv, homeDir))
		if ok {
			key, source = pk, "prime-home"
			if baseURL == "" {
				baseURL = pBase
			}
		}
	}
	if key == "" {
		logf("[hub] 决策服务未启用：llm-as-jev 缺少 API key（decision.api_key / GLM_API_KEY / prime-home 三级皆空）")
		return &Service{}
	}
	if baseURL == "" {
		baseURL = defaultLLMBaseURL
	}
	// HTTP 客户端兜底超时给足预算外的网络余量；硬预算收敛归 Service.call。
	// Logf=hub 日志出口：上游原始错误只进日志（codex 审 D1 ④），对外固定人话类别。
	p := NewLLMProvider(LLMConfig{BaseURL: baseURL, APIKey: key, Model: strings.TrimSpace(s.Model), Timeout: budget + 22*time.Second, Logf: logf})
	if p == nil {
		return &Service{}
	}
	logf("[hub] 决策服务已启用：llm-as-jev（model=%s，key 来源 %s，端点 %s，预算 %s）——决策是建议非必经，失败自动降级不阻塞派单",
		p.Model(), source, baseURL, budget)
	return NewService(p, budget)
}

// primeHomePath prime-home 目录解析：显式配置 → SWARM_PRIME_HOME → $HOME/swarm-prime-home。
func primeHomePath(configured string, getenv func(string) string, homeDir string) string {
	if p := strings.TrimSpace(configured); p != "" {
		return p
	}
	if getenv != nil {
		if p := strings.TrimSpace(getenv("SWARM_PRIME_HOME")); p != "" {
			return p
		}
	}
	if homeDir != "" {
		return filepath.Join(homeDir, "swarm-prime-home")
	}
	return ""
}

// primeHomeKey 读 prime-home 凭证：providerRules 中取含非空 apiKey 的规则，优先
// glm / bigmodel（GLM coding plan），一并返回规则 baseUrl（未显式配置端点时采用）。
// 文件缺失/非法/无规则命中 → ("","",false)：不算装配错误，决策服务优雅停用。
func primeHomeKey(dir string) (key, baseURL string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, PrimeHomeFile))
	if err != nil {
		return "", "", false
	}
	var cfg primeHomeConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", "", false
	}
	rules := cfg.Config.ProviderConfigRules.ProviderRules
	// 两轮扫描：先偏好 GLM/bigmodel 规则，再退第一条有 key 的规则。
	for _, pick := range []func(primeRule) bool{func(r primeRule) bool { return r.isGLM() }, func(primeRule) bool { return true }} {
		for _, r := range rules {
			if r.Config.Access.APIKey != "" && pick(r) {
				return r.Config.Access.APIKey, r.Config.API.BaseURL, true
			}
		}
	}
	return "", "", false
}
