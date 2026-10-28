// Package decision 实现 hub 内嵌决策服务（P5.1 D1，docs/RFC-jev-dispatch）：
// 「派给谁」的 System One 化。DecisionProvider 抽象（provider 可切换：D1 交付
// llm-as-jev，jev-cloud / local-open 属 D3）+ 建议查询入口。
//
// 核心语义（RFC 三）：**决策是建议不是必经**——provider 的一切失败（未启用/key
// 缺失/超时/解析失败/目标越权）在 Service.Suggest 收敛为「无建议 + 降级原因」，
// 绝不上抛阻塞派单；调用方（REST/MCP/控制台）拿到的结果恒可用，派单路径不依赖本包。
//
// Jev 范式映射：state = 任务需求 + 能力目录快照压缩（在线 agent@node 候选），
// Choice 原语 = 候选中选一个，输出建议 + 校准置信度 + 概率分布。D1 仅 Choice；
// Noul/Score 与多问题并行属 D3，不预占。
package decision

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// DefaultBudget 单次建议查询预算：llm-as-jev 走 GLM coding 端点实测中位 ~5s、
// 尾部 >8s（2026-10-26 首轮 22 场景实测：4 例 8s 预算超时，答出者全对），15s
// 覆盖尾延迟；决策是建议非必经，超时降级不阻塞派单，仅是人多等一会。
const DefaultBudget = 15 * time.Second

// ErrNoFit provider 正常应答但判断候选均不适合（choice=none）：不是 provider
// 故障，Service 以非降级语义转述。
var ErrNoFit = errors.New("decision: 候选均不适合该任务")

// Candidate 候选执行者（Choice 原语的选项）：在线且 exposed 的 agent@node。
type Candidate struct {
	NodeID         string // 归属节点
	Agent          string // agent 名（node 内唯一）
	Description    string // 能力描述（AgentCard.Description）
	Kind           string // 形态（cli 等）
	PermissionMode string // agent 自带权限机制描述
	Inflight       int    // 归属节点在途任务数（负载信号）
}

// Key 候选唯一标识：agent@node（LLM 应答 choice 与概率分布的键）。
func (c Candidate) Key() string { return c.Agent + "@" + c.NodeID }

// Request 建议查询请求：任务需求 + 有限候选集。
type Request struct {
	Goal       string     // 任务需求原文（goal 或步骤描述）
	Candidates []Candidate // 候选执行者（在线 ∩ exposed，调用方从目录组装）
}

// Suggestion 建议结果（Choice 判定）。
type Suggestion struct {
	TargetNode    string             // 建议节点
	TargetAgent   string             // 建议 agent
	Confidence    float64            // choice 的校准置信概率 [0,1]
	Probabilities map[string]float64 // 候选概率分布（键 = agent@node，和为 1）
	Reason        string             // 一句话理由（provider 给出，展示层自行取舍）
	Provider      string             // 来源标签（如 llm-as-jev），Service 盖章
	Model         string             // 判定模型（llm-as-jev 为 LLM 型号）
}

// Provider 决策提供者抽象：实现方负责把 Request 压成 state、执行判定并回结构化
// 建议。实现不得阻塞超预算（Service 以 context + 硬超时双保险收敛）。
type Provider interface {
	Name() string
	Model() string
	Suggest(ctx context.Context, req Request) (*Suggestion, error)
}

// Result 建议查询出口（REST/MCP 统一转述形态）：Suggestion 非 nil 即有建议；
// 无建议时 Degraded 区分「provider 故障降级」与「正常无建议」（无候选/choice=none）。
type Result struct {
	Suggestion *Suggestion
	Degraded   bool   // true = 决策链路故障降级（provider 错误/超时/越权目标）
	Note       string // 无建议时的人话原因（供 UI/agent 转述）
	Provider   string // 参与判定的 provider 名（未启用为空）
}

// Service 决策服务：Provider 容器 + 预算收敛 + 候选校验。零值不可用（New 装配）。
type Service struct {
	provider Provider // nil = 未启用
	budget   time.Duration
}

// NewService 装配决策服务；budget <=0 取 DefaultBudget。provider 为 nil 即未启用
// （查询恒返回「决策服务未启用」）。
func NewService(p Provider, budget time.Duration) *Service {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return &Service{provider: p, budget: budget}
}

// Enabled 是否已启用（装配方启动日志与 REST note 用）。
func (s *Service) Enabled() bool { return s != nil && s.provider != nil }

// ProviderName 来源标签（未启用为空）。
func (s *Service) ProviderName() string {
	if !s.Enabled() {
		return ""
	}
	return s.provider.Name()
}

// NotEnabledNote 未启用时的人话说明（REST note 与装配方日志共用口径）。
const NotEnabledNote = "决策服务未启用（hub.yaml 未配置 decision 段或密钥不可得）：可手动选择执行者，派单不受影响"

// Suggest 建议查询总入口。语义保证（RFC 三）：
//   - 恒返回 Result（不 panic、不阻塞派单）：provider 错误/超时/越权 → 无建议 +
//     Degraded=true + Note；无候选/空 goal → 无建议 + 说明；
//   - choice 不在候选集的目标不采信（越权目标一律弃用）；
//   - 概率分布收敛到候选集内、和为 1；Confidence 与 probabilities[choice] 一致。
func (s *Service) Suggest(req Request) Result {
	if !s.Enabled() {
		return Result{Note: NotEnabledNote}
	}
	name := s.provider.Name()
	if strings.TrimSpace(req.Goal) == "" {
		return Result{Provider: name, Note: "任务描述为空，无法给出建议"}
	}
	if len(req.Candidates) == 0 {
		return Result{Provider: name, Note: "网内暂无在线能力候选：可稍后重试，或手动选择执行者"}
	}
	sug, err := s.call(req)
	if err != nil {
		if errors.Is(err, ErrNoFit) {
			return Result{Provider: name, Note: "决策模型判断当前候选均不适合该任务：可补充候选或手动指派"}
		}
		return Result{Provider: name, Degraded: true, Note: "决策模型暂不可用（" + err.Error() + "）：可手动选择执行者，派单不受影响"}
	}
	key := sug.TargetAgent + "@" + sug.TargetNode
	if !containsCandidate(req.Candidates, key) {
		return Result{Provider: name, Degraded: true, Note: "决策模型给出的目标不在网内候选中，已弃用本次建议：可重试或手动选择"}
	}
	sug.Provider = name
	if sug.Model == "" {
		sug.Model = s.provider.Model()
	}
	sug.Probabilities = normalizeProbs(sug.Probabilities, req.Candidates, key, sug.Confidence)
	sug.Confidence = clamp01(sug.Probabilities[key])
	return Result{Suggestion: sug, Provider: name}
}

// call 预算内执行 provider：goroutine + 硬超时双保险——provider 不配合 ctx 时
// 也按时返回降级（结果经缓冲通道传递，迟到结果弃用，无数据竞争）。
func (s *Service) call(req Request) (*Suggestion, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.budget)
	defer cancel()
	type outcome struct {
		sug *Suggestion
		err error
	}
	ch := make(chan outcome, 1) // 缓冲 1：超时弃用后 provider 侧写入不阻塞、被 GC
	go func() {
		sug, err := s.provider.Suggest(ctx, req)
		ch <- outcome{sug, err}
	}()
	select {
	case o := <-ch:
		return o.sug, o.err
	case <-ctx.Done():
		return nil, errors.New("决策超时（预算 " + s.budget.String() + "）")
	}
}

// containsCandidate 候选集精确匹配（agent@node）。
func containsCandidate(cands []Candidate, key string) bool {
	for _, c := range cands {
		if c.Key() == key {
			return true
		}
	}
	return false
}

// normalizeProbs 概率分布收敛（校准口径 + 缺失值补齐，codex 审 D1 ①闭合）：
//   - P(choice) 恒等于 confidence；conf 缺失（<=0）回落 probabilities[choice]；
//     两者皆缺按均匀分布 1/n 兜底——凭空补 1.0 是虚假确定，均匀即天然低置信
//     （单候选时 1 是唯一合法分布，非造假）；
//   - 其余候选：显式正数权重保留相对比例，缺失（键缺失/非正数/候选外）按显式
//     权重均值填补（无任何显式信号则 (1-conf) 内均匀）——缺失不补 0（隐含
//     「绝无可能」同样无依据）；
//   - 结果恒覆盖全部候选且和为 1（浮点容差内）。
func normalizeProbs(probs map[string]float64, cands []Candidate, choiceKey string, conf float64) map[string]float64 {
	n := len(cands)
	if conf <= 0 {
		if v := probs[choiceKey]; v > 0 {
			conf = v
		}
	}
	conf = clamp01(conf)
	if conf <= 0 {
		conf = 1 / float64(n) // provider 什么都没给：均匀兜底=低置信，不凭空 100%
	}
	out := map[string]float64{choiceKey: conf}
	if n == 1 {
		out[choiceKey] = 1 // 唯一候选：1 是唯一合法分布
		return out
	}
	weights := make([]float64, n)
	sum, known := 0.0, 0
	for i, c := range cands {
		if c.Key() == choiceKey {
			continue
		}
		if v := probs[c.Key()]; v > 0 {
			weights[i] = v
			sum += v
			known++
		}
	}
	fill := 1.0
	if known > 0 {
		fill = sum / float64(known) // 缺失候选按显式均值填补（均匀填补口径）
	}
	total := sum
	for i := range cands {
		if cands[i].Key() == choiceKey {
			continue
		}
		if weights[i] == 0 {
			weights[i] = fill
			total += fill // 仅填补项增量；显式权重已计入 sum
		}
	}
	scale := (1 - conf) / total
	for i := range cands {
		if k := cands[i].Key(); k != choiceKey {
			out[k] = weights[i] * scale
		}
	}
	return out
}

// clamp01 钳制到 [0,1]（NaN 归 0）。
func clamp01(v float64) float64 {
	if !(v > 0) { // NaN 与负数一并归 0
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// SortCandidates 候选稳定序（node_id, agent）：LLM 对 state 顺序敏感，map 遍历
// 序随机会让同 state 得到不同答案（对照控制台 sortDirectoryStable 同一纪律）。
func SortCandidates(cands []Candidate) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].NodeID != cands[j].NodeID {
			return cands[i].NodeID < cands[j].NodeID
		}
		return cands[i].Agent < cands[j].Agent
	})
}

// memberSelector 成员多选判定可选接口（R3' P1-6 纪律：多选与单选语义独立，不
// 复用 Choice 概率分布——provider 按需实现；D1 llm-as-jev 已实现，mock 可自行选配）。
type memberSelector interface {
	SuggestMembers(ctx context.Context, goal string, cands []Candidate) (members []string, reason string, err error)
}

// MemberResult 成员多选查询出口（与 Result 同构的降级收敛形态）。
type MemberResult struct {
	Members  []string // 参与成员键（agent@node，贡献潜力降序）；nil = 无集合
	Degraded bool     // true = 决策链路故障降级（provider 错误/超时）
	Note     string   // 无集合时的人话原因（供 UI/agent 转述）
	Reason   string   // 组队理由（provider 给出，展示层自行取舍）
	Provider string   // 来源标签
}

// SuggestMembers 协作成员多选查询总入口（方案三群聊协作）。语义保证与 Suggest
// 同款：恒返回 MemberResult（provider 错误/超时 → Degraded + Note；越界目标剔除；
// 全不适合 → 非 degraded 的「无合适成员」转述）；零值 Service 恒未启用。
func (s *Service) SuggestMembers(req Request) MemberResult {
	if !s.Enabled() {
		return MemberResult{Note: NotEnabledNote}
	}
	name := s.provider.Name()
	if strings.TrimSpace(req.Goal) == "" {
		return MemberResult{Provider: name, Note: "协作目标为空，无法给出组队建议"}
	}
	if len(req.Candidates) == 0 {
		return MemberResult{Provider: name, Note: "网内暂无在线能力候选：可稍后重试，或手动选择成员"}
	}
	ms, ok := s.provider.(memberSelector)
	if !ok {
		return MemberResult{Provider: name, Degraded: true,
			Note: "决策提供者不支持协作选人判定（仅单选建议可用）"}
	}
	// 预算内执行 provider（call() 同款骨架，R1-04）：WithTimeout 派生预算 ctx
	// 传入 provider——预算耗尽同时取消上游调用（HTTP 连接/模型请求即释放，配合
	// 取消的 provider 立即退出，不占资源到 HTTP 独立超时）；容量 1 缓冲通道保留
	// 为「provider 不配合取消」时的返回兜底，迟到结果弃用，无数据竞争。
	ctx, cancel := context.WithTimeout(context.Background(), s.budget)
	defer cancel()
	type outcome struct {
		members []string
		reason  string
		err     error
	}
	ch := make(chan outcome, 1)
	go func() {
		m, r, err := ms.SuggestMembers(ctx, req.Goal, req.Candidates)
		ch <- outcome{m, r, err}
	}()
	var (
		members []string
		reason  string
		callErr error
	)
	select {
	case o := <-ch:
		members, reason, callErr = o.members, o.reason, o.err
	case <-ctx.Done():
		callErr = errors.New("决策超时（预算 " + s.budget.String() + "）")
	}
	if callErr != nil {
		if errors.Is(callErr, ErrNoFit) {
			return MemberResult{Provider: name, Note: "决策模型判断当前候选均不适合该协作：可手动选择成员"}
		}
		return MemberResult{Provider: name, Degraded: true,
			Note: "决策模型暂不可用（" + callErr.Error() + "）：可手动选择成员，建群不受影响"}
	}
	// 越界剔除 + 去重（出口守卫，DESIGN-butler 方案三）：候选稳定序为权威序，
	// provider 应答序仅作参考——剔除后为空视同「无有效成员」人话降级。
	valid := map[string]bool{}
	for _, c := range req.Candidates {
		valid[c.Key()] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(members))
	for _, m := range members {
		if valid[m] && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return MemberResult{Provider: name, Note: "决策模型未给出有效成员（候选外目标已剔除）：可手动选择成员"}
	}
	return MemberResult{Members: out, Reason: reason, Provider: name}
}
