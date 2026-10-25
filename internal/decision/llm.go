// llm-as-jev provider（P5.1 D1，docs/RFC-jev-dispatch 三）：GLM-5.3-Flash 经
// OpenAI 兼容 chat/completions 改造提示充当 Jev——不做长推理，state（任务需求 +
// 能力目录快照压缩）一次判定输出 Choice（建议 + 校准概率 + 置信度）。
// 走现有 LLM API key 生态（PRIME 链路同款 GLM coding 端点），零新依赖。
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// LLM 缺省端点/模型：GLM coding plan 的 OpenAI 兼容面（与 PRIME 链路
// swarm-prime-home provider_config.json 实测同形；可被配置/prime-home 规则覆盖）。
const (
	defaultLLMBaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	defaultLLMModel   = "glm-5.3-flash"
	defaultLLMTimeout = 30 * time.Second // HTTP 客户端兜底超时；预算收敛归 Service
	// stateDescLimit 候选描述压缩上限（rune）：Jev state 贵在精炼，80 字足够
	// 表达能力差异且防个别长描述淹没判定。
	stateDescLimit = 80
	// stateCandidateCap 候选数上限：V0 目录低量（SPEC 六.五 ≤5 node），防御性上限。
	stateCandidateCap = 32
)

// LLMConfig llm-as-jev provider 配置（Settings 解析产物）。
type LLMConfig struct {
	BaseURL string        // 缺省 defaultLLMBaseURL
	APIKey  string        // 必填；空则 NewLLMProvider 返回 nil（未启用）
	Model   string        // 缺省 defaultLLMModel
	Timeout time.Duration // HTTP 客户端兜底超时，缺省 defaultLLMTimeout
	// Logf 上游原始错误留痕出口（hub 日志）：上游应答/错误体可能回显请求内容或
	// 凭据，截断≠脱敏——原文只进日志，Suggest 对外一律返回固定人话类别错误
	//（codex 审 D1 ④）。nil 落标准 log。
	Logf func(format string, args ...any)
}

// jevSystemPrompt Jev 范式提示词（RFC 一/三）：系统一判定——不生成推理文本，
// 直接输出唯一 JSON（Choice + 校准概率 + 置信度 + 一句话理由）。
const jevSystemPrompt = `你是网内任务派发决策引擎（Jev 范式：系统一判定，不做长推理）。
输入 state：任务需求 goal 与候选执行者列表 candidates（每项 id 形如 "agent@node"，含能力描述 desc、形态 kind、权限模式 permission、节点在途负载 inflight）。
任务：从候选中选出最适合执行该任务的执行者（Choice 判定），只输出一个 JSON 对象，无任何其他文字、无 markdown 围栏：
{"choice":"<候选 id>","confidence":<0到1的小数>,"probabilities":{"<候选id>":<概率>},"reason":"<不超过40字的中文理由>"}
判定规则：
- choice 必须逐字取自候选 id；所有候选都不合适时 choice 填 "none"。
- probabilities 覆盖全部候选 id 且总和为 1；概率是校准判断（有依据地分配，不无脑均匀摊派）。
- 依据优先级：能力描述与任务需求的匹配度 > 形态适配（编码类任务选编码智能体，巡检/脚本类任务选对应工具）> 节点负载（inflight 低者优先）> 权限模式是否满足任务要求。
- confidence 等于 probabilities[choice]。
- reason 只说选择依据，不复述任务。`

// chatRequest / chatResponse OpenAI 兼容 chat completions 请求应答子集。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	// Thinking GLM 混合推理开关（GLM 专属参数，OpenAI 兼容面透传）：禁用深度
	// 思考——Jev 是系统一判定不做长推理，思考模式实测会把 token 烧在
	// reasoning_content 上致 content 空应答/JSON 截断（2026-10-26 实测探针）。
	Thinking *chatThinking `json:"thinking,omitempty"`
}

type chatThinking struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// llmProvider llm-as-jev 实现。
type llmProvider struct {
	cfg LLMConfig
	hc  *http.Client
}

// NewLLMProvider 装配 llm-as-jev provider；APIKey 为空返回 nil（未启用）。
func NewLLMProvider(cfg LLMConfig) *llmProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultLLMBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultLLMModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultLLMTimeout
	}
	return &llmProvider{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}
}

func (p *llmProvider) Name() string  { return "llm-as-jev" }
func (p *llmProvider) Model() string { return p.cfg.Model }

// Suggest 一次 chat/completions 判定：state 压缩 → JSON 应答解析 → 候选内定位。
// 错误纪律（codex 审 D1 ④）：一切上游错误经 logf 留原始内容后，对外只返回固定
// 人话类别（原文不进 Service note → 不泄入 REST/MCP/控制台）。
func (p *llmProvider) Suggest(ctx context.Context, req Request) (*Suggestion, error) {
	logf := p.cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	if len(req.Candidates) > stateCandidateCap {
		logf("[decision] llm-as-jev 候选 %d 超过 state 上限 %d，已按稳定序截断（超出部分不参与判定）",
			len(req.Candidates), stateCandidateCap)
	}
	body := chatRequest{
		Model: p.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: jevSystemPrompt},
			{Role: "user", Content: buildState(req)},
		},
		Temperature: 0.2,
		MaxTokens:   2000, // 应答本体 <100 token，余量防异常形态截断（解析失败降级成本高于余量成本）
		Thinking:    &chatThinking{Type: "disabled"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("决策请求构造失败")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		logf("[decision] llm-as-jev 请求构造失败（原始错误仅入日志）: %v", err)
		return nil, errors.New("决策请求构造失败")
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(httpReq)
	if err != nil {
		logf("[decision] llm-as-jev 网络请求失败（原始错误仅入日志）: %v", err)
		return nil, errors.New("决策模型网络请求失败")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		logf("[decision] llm-as-jev 应答读取失败（原始错误仅入日志）: %v", err)
		return nil, errors.New("决策模型应答读取失败")
	}
	if resp.StatusCode != http.StatusOK {
		logf("[decision] llm-as-jev 端点 HTTP %d（原始应答仅入日志）: %.500s", resp.StatusCode, string(data))
		return nil, fmt.Errorf("决策模型端点返回 HTTP %d", resp.StatusCode)
	}
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		logf("[decision] llm-as-jev 应答非合法 JSON（原始内容仅入日志）: %.500s", string(data))
		return nil, errors.New("决策模型应答格式异常")
	}
	if out.Error != nil && out.Error.Message != "" {
		logf("[decision] llm-as-jev 上游拒绝本次请求（原始信息仅入日志）: %s", out.Error.Message)
		return nil, errors.New("决策模型拒绝了本次请求")
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return nil, errors.New("决策模型应答为空")
	}
	dec, err := parseDecision(out.Choices[0].Message.Content)
	if err != nil {
		logf("[decision] llm-as-jev 应答解析失败（原始内容仅入日志）: %.500s", out.Choices[0].Message.Content)
		return nil, err
	}
	if dec.Choice == "" || dec.Choice == "none" {
		return nil, ErrNoFit
	}
	// choice → 候选定位：精确 agent@node → 宽松去空白 → 首个 @ 切分双字段匹配。
	for _, c := range req.Candidates {
		if c.Key() == dec.Choice || c.Key() == strings.TrimSpace(dec.Choice) {
			return &Suggestion{TargetNode: c.NodeID, TargetAgent: c.Agent,
				Confidence: dec.Confidence, Probabilities: dec.Probabilities, Reason: dec.Reason}, nil
		}
	}
	if agent, node, ok := strings.Cut(dec.Choice, "@"); ok {
		for _, c := range req.Candidates {
			if c.Agent == strings.TrimSpace(agent) && c.NodeID == strings.TrimSpace(node) {
				return &Suggestion{TargetNode: c.NodeID, TargetAgent: c.Agent,
					Confidence: dec.Confidence, Probabilities: dec.Probabilities, Reason: dec.Reason}, nil
			}
		}
	}
	logf("[decision] llm-as-jev choice 不在候选中（原始值仅入日志）: %q", truncateRunes(dec.Choice, 60))
	return nil, errors.New("决策模型给出的目标不在候选中")
}

// llmDecision 应答 JSON 判定体。
type llmDecision struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Reason        string             `json:"reason"`
}

// parseDecision 容错解析模型应答：剥 markdown 围栏与前后杂文、截取首尾大括号
// 之间内容解析（LLM 偶发「好的，以下是判定：{...}」形态）。失败返回固定人话类别
//（原始内容由调用方留日志，codex 审 D1 ④）。
func parseDecision(content string) (*llmDecision, error) {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var dec llmDecision
	if err := json.Unmarshal([]byte(s), &dec); err != nil {
		return nil, errors.New("决策模型应答不是合法判定 JSON")
	}
	dec.Choice = strings.TrimSpace(dec.Choice)
	dec.Reason = strings.TrimSpace(dec.Reason)
	return &dec, nil
}

// buildState 压缩 state：goal + 候选（描述截断、字段短名）单行 JSON——Jev state
// 贵在精炼，压缩快照既省 token 又稳定判定。候选超 stateCandidateCap 时按稳定序
// 截断，并在 state 内带说明标记（模型可感知、日志可追踪，codex 审 D1 ⑤）。
func buildState(req Request) string {
	type candDTO struct {
		ID         string `json:"id"`
		Desc       string `json:"desc,omitempty"`
		Kind       string `json:"kind,omitempty"`
		Permission string `json:"permission,omitempty"`
		Inflight   int    `json:"inflight,omitempty"`
	}
	n := len(req.Candidates)
	if n > stateCandidateCap {
		n = stateCandidateCap
	}
	cands := make([]candDTO, 0, n)
	for _, c := range req.Candidates[:n] {
		cands = append(cands, candDTO{
			ID:         c.Key(),
			Desc:       truncateRunes(strings.TrimSpace(c.Description), stateDescLimit),
			Kind:       c.Kind,
			Permission: c.PermissionMode,
			Inflight:   c.Inflight,
		})
	}
	state := map[string]any{"goal": strings.TrimSpace(req.Goal), "candidates": cands}
	if len(req.Candidates) > stateCandidateCap {
		state["candidates_note"] = fmt.Sprintf("候选共 %d 个，超过上限 %d，仅按稳定序列出前 %d 个，其余未参与本次判定",
			len(req.Candidates), stateCandidateCap, stateCandidateCap)
	}
	b, err := json.Marshal(state)
	if err != nil {
		return req.Goal // 不可达（纯字符串 map）；防御性回落原文
	}
	return string(b)
}

// truncateRunes 按 rune 截断（中文不截半字），截断尾部加 …。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
