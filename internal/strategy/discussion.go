// discussion.go 有限讨论策略（W05-02）：讨论轮推进判定——jev/主人选择下一位发
// 言者（候选=成员名单，成员资格与发言预算逐项校验）、顺序或显式并发指名、候选
// 登记双通道（FINAL 前缀既有链路 + 结构化通道占位）、候选定版收束与预算暂停。
//
// 定位（任务书 W05-02 行 + 硬门槛）：讨论只组织成员内容，不由 Hub/策略生成内容；
// 内置规则只推进确定动作（预算过滤/成员校验/定版收束），开放需求拆解由用户/外
// 部管家给计划。派发经 spaceDispatch 通路由装配层驱动（W04-02 串行调度），策略
// 层只产出「下一棒是谁」的判定——无副作用纯函数 + 与 collab 状态的读取组合
// （发言计数由装配层从 collab 消息账本投影传入，本包不另存计数：单一事实源）。
//
// 停止纪律：候选定版（主人 finalize 显式动作）→ 讨论收束不再派发（定版 ≠ 完
// 成/交付——完成仍走 EvaluateCompletion 判据）；全局预算耗尽（EvaluateStop 复
// 用）或全员发言预算耗尽 → 暂停建议保留进度；jev 失败/未配置/非法应答 → 手选
// 出口（错误如实），不增加执行量、不静默换人（E10 同源）。
package strategy

import (
	"fmt"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// SpeakerSelector 下一位发言者选择器（jev 语义选人抽象；实现由装配层注入——
// decision.Service 适配或测试 fake）。本包不 import 内部决策包：接口最小面，
// 预算/降级收敛由实现方承担（与 decision.Service「建议不是必经」同纪律）。
type SpeakerSelector interface {
	// SelectSpeaker 从候选名单（成员名，已过发言预算过滤，名单序）中选出下一位
	// 发言者。返回空名或名单外成员均按非法应答处理（调用方拒绝，不静默换人）。
	SelectSpeaker(goal string, candidates []string) (member string, reason string, err error)
}

// SpeakerSelectorFunc 函数适配器（装配层闭包注入用）。
type SpeakerSelectorFunc func(goal string, candidates []string) (member string, reason string, err error)

// SelectSpeaker 接口实现。
func (f SpeakerSelectorFunc) SelectSpeaker(goal string, candidates []string) (string, string, error) {
	return f(goal, candidates)
}

// DiscussionPolicy 讨论策略参数（W05-04 P08 决策设置面接入前的装配层显式传参；
// 零值 = 顺序单棒 + 成员发言不限次）。
type DiscussionPolicy struct {
	// MaxSpeechesPerMember 单成员发言上限（次；0 = 不限）——发言预算按成员计，
	// 耗尽者退出点名候选（额度收束保留原生会话，不换模型不无限重试）。
	MaxSpeechesPerMember int
	// ParallelSpeakers 显式并发指名棒数（>1 生效：一次点名前 N 名有预算成员，
	// 成员名单序；0/1 = 顺序单棒由选择器点名）。
	ParallelSpeakers int
}

// DiscussionDirective 讨论轮推进判定输出（纯判定结果；派发/暂停落账由装配层执
// 行——Next 逐棒经 spaceDispatch 受理，Pause 经 collab pause，ManualNeeded 呈
// 主人手选）。
type DiscussionDirective struct {
	// Next 下一棒发言者（顺序=1 名；显式并发=多名；空 = 本轮不派发）。
	Next []string `json:"next,omitempty"`
	// Done 讨论收束：候选已定版（无 pending 且至少一条 finalized）——不再派发；
	// 定版 ≠ 完成交付（完成走 EvaluateCompletion 判据）。
	Done bool `json:"done,omitempty"`
	// Pause 非 nil = 暂停建议（全局预算耗尽复用 EvaluateStop；全员发言预算耗
	// 尽产成员预算触发）——保留进度，恢复须显式。
	Pause *StopEvaluation `json:"pause,omitempty"`
	// ManualNeeded 非空 = 手选出口说明（jev 未配置/失败/非法应答——错误如实，
	// 不增加执行量）；装配层呈主人，主人经 PickSpeakers 显式指名。
	ManualNeeded string `json:"manual_needed,omitempty"`
	// Reason 结论人话（轮终注记/展示）。
	Reason string `json:"reason"`
}

// 讨论域错误（Code 供装配层投影；成员资格判定与 ValidateCandidate 提出者校验
// 同纪律——非法拒绝，不静默换人）。
var (
	// ErrSpeakerEmpty 指名发言者为空。
	ErrSpeakerEmpty = &Error{Code: "speaker_empty", msg: "指名发言者为空"}
	// ErrSpeakerNotMember 指名发言者不在空间成员内（候选=成员名单，越界拒绝）。
	ErrSpeakerNotMember = &Error{Code: "speaker_not_member", msg: "指名发言者不在空间成员内"}
	// ErrSpeakerBudgetExhausted 指名发言者发言预算已耗尽（额度收束，不绕过）。
	ErrSpeakerBudgetExhausted = &Error{Code: "speaker_budget_exhausted", msg: "发言者成员发言预算已耗尽"}
	// ErrStructuredCandidateDeferred 结构化候选 schema 本版未定义（登记双通道之
	// 一的占位拒绝：契约先行、schema 随 W05-04「P04 策略区/P08 决策设置 + 外部
	// 管家驱动」批次落定并扩展登记范围——拒绝信息显式指明归属，不留悬空猜测）。
	ErrStructuredCandidateDeferred = &Error{Code: "candidate_structured_deferred",
		msg: "结构化候选 schema 本版未定义（归属 W05-04 外部管家驱动批次扩展；本版候选仅 FINAL: 前缀文本形式）"}
)

// StopBudgetSpeeches 成员发言预算耗尽触发（讨论域；与全局预算三分离触发同形态
// ——同入 StopEvaluation，暂停判定单一出口）。
const StopBudgetSpeeches StopKind = "budget_member_speeches"

// CandidateChannel 候选登记通道（W05-02 双通道）。
type CandidateChannel string

const (
	// ChannelFinalText FINAL: 前缀文本通道（既有链路：发言以此开头即登记候选，
	// 文本前缀判定登记不判定完成/交付）。
	ChannelFinalText CandidateChannel = "final_text"
	// ChannelStructured 结构化载荷通道（本版占位：登记即拒绝并指明归属）。
	ChannelStructured CandidateChannel = "structured"
)

// RegisterCandidateVia 候选登记双通道入口（W05-02；复用 RegisterCandidate）：
// FINAL 前缀文本通道原样走既有链路（成员资格由调用方先经 ValidateCandidate 校
// 验）；结构化通道本版显式拒绝（ErrStructuredCandidateDeferred 指明 schema 归属
// W05-04），通道存在性即后续扩展锚点——登记契约先行，避免届时加法字段破坏调用
// 形态。未知通道拒绝。
func (e *Engine) RegisterCandidateVia(spaceID string, channel CandidateChannel, in CandidateInput) (Candidate, bool, error) {
	switch channel {
	case ChannelFinalText:
		return e.RegisterCandidate(spaceID, in)
	case ChannelStructured:
		return Candidate{}, false, ErrStructuredCandidateDeferred
	default:
		return Candidate{}, false, errf(ErrInvalid, "未知候选登记通道 %q（须为 final_text|structured）", string(channel))
	}
}

// SpeakerEligible 发言预算未耗尽的成员名单（候选=成员名单；成员名单序，判定可
// 复现）。纯函数：发言计数由装配层从 collab 消息账本投影传入（按 Member 归属计
// 数——主人发言不计成员预算）。
func SpeakerEligible(space collab.SpaceView, counts map[string]int64, pol DiscussionPolicy) []string {
	out := make([]string, 0, len(space.Members))
	for _, m := range space.Members {
		if speechesLeft(counts[m.Name], pol) > 0 {
			out = append(out, m.Name)
		}
	}
	return out
}

// speechesLeft 成员剩余发言额度（0=不限；耗尽为负不出现——计数非负）。
func speechesLeft(used int64, pol DiscussionPolicy) int64 {
	if pol.MaxSpeechesPerMember <= 0 {
		return 1 // 不限：恒有额度（哨值 1，仅判 >0）
	}
	return int64(pol.MaxSpeechesPerMember) - used
}

// ValidateSpeaker 选人校验（NextSpeaker 的 jev 应答校验与主人手选共用同一判定；
// 成员名单内 + 发言预算未耗尽。非法拒绝，不静默换人——E10 同源）。
func (e *Engine) ValidateSpeaker(space collab.SpaceView, counts map[string]int64, pol DiscussionPolicy, member string) error {
	member = strings.TrimSpace(member)
	if member == "" {
		return ErrSpeakerEmpty
	}
	in := false
	for i := range space.Members {
		if space.Members[i].Name == member {
			in = true
			break
		}
	}
	if !in {
		return errf(ErrSpeakerNotMember, "指名 %q 不在空间成员内（候选=成员名单，越界应答拒绝）", member)
	}
	if left := speechesLeft(counts[member], pol); left <= 0 {
		return errf(ErrSpeakerBudgetExhausted, "成员 %s 发言 %d 次已达上限 %d（额度收束；提高上限属显式配置变更）",
			member, counts[member], pol.MaxSpeechesPerMember)
	}
	return nil
}

// NextSpeaker 讨论轮推进判定（W05-02 装配层入口；纯判定 + 选择器只读查询，无
// 状态变更无派发）：
//  1. 全局停止判定优先（EvaluateStop 复用：消息/轮次/墙钟预算耗尽、验收争议
//     ——讨论域无 issue，修复阈值天然不触发）；
//  2. 候选定版收束（无 pending 且至少一条 finalized → Done，不再派发）；
//  3. 候选=成员名单 × 发言预算过滤；全员耗尽 → 暂停建议（成员预算触发 + 全局
//     判据复查合并）；空间无成员 → 手选出口；
//  4. 显式并发指名（ParallelSpeakers>1）：一次点名前 N 名有预算成员（成员序）；
//  5. 顺序单棒：选择器点名（未配置 → 手选出口；失败/非法应答 → 手选出口，错误
//     如实，不增加执行量）。
func (e *Engine) NextSpeaker(space collab.SpaceView, now time.Time, counts map[string]int64, pol DiscussionPolicy, sel SpeakerSelector) DiscussionDirective {
	// 1. 全局停止判定优先。
	if stop := e.EvaluateStop(space, now); stop.ShouldPause {
		return DiscussionDirective{Pause: &stop,
			Reason: "停止判定触发：建议暂停保留进度（恢复须显式；见 pause 触发项）"}
	}
	// 2. 候选定版收束。
	if done, note := e.discussionConverged(space.ID); done {
		return DiscussionDirective{Done: true, Reason: note}
	}
	// 3. 候选=成员名单 × 发言预算。
	eligible := SpeakerEligible(space, counts, pol)
	if len(space.Members) == 0 {
		return DiscussionDirective{ManualNeeded: "空间无成员：先 configure_members 配置成员再开讨论", Reason: "无候选可点名"}
	}
	if len(eligible) == 0 {
		var triggers []StopTrigger
		for _, m := range space.Members {
			triggers = append(triggers, StopTrigger{Kind: StopBudgetSpeeches,
				Detail: fmt.Sprintf("成员 %s 发言预算耗尽（%d/%d）", m.Name, counts[m.Name], pol.MaxSpeechesPerMember)})
		}
		stop := StopEvaluation{ShouldPause: true, Triggers: triggers}
		return DiscussionDirective{Pause: &stop,
			Reason: "全员发言预算耗尽：建议暂停保留进度（恢复须显式；提高上限属显式配置变更）"}
	}
	// 4. 显式并发指名（>1：一次点名前 N 名有预算成员，成员名单序）。
	if pol.ParallelSpeakers > 1 {
		n := pol.ParallelSpeakers
		if n > len(eligible) {
			n = len(eligible)
		}
		return DiscussionDirective{Next: append([]string(nil), eligible[:n]...),
			Reason: fmt.Sprintf("显式并发指名 %d 名（成员序；各棒经 spaceDispatch 受理）", n)}
	}
	// 5. 顺序单棒：选择器点名。
	if sel == nil {
		return DiscussionDirective{ManualNeeded: "未配置决策选择器（jev）：可由主人手动指名下一位发言者", Reason: "选择器缺位：不增加执行量"}
	}
	member, why, err := sel.SelectSpeaker(space.Goal, eligible)
	if err != nil {
		return DiscussionDirective{ManualNeeded: fmt.Sprintf("决策选择暂不可用（%v）：可由主人手动指名下一位发言者", err),
			Reason: "jev 选择失败：不增加执行量（错误如实）"}
	}
	if verr := e.ValidateSpeaker(space, counts, pol, member); verr != nil {
		return DiscussionDirective{ManualNeeded: fmt.Sprintf("决策应答非法（%v）：可由主人手动指名下一位发言者", verr),
			Reason: "选人校验拒绝：不静默换人、不增加执行量"}
	}
	return DiscussionDirective{Next: []string{strings.TrimSpace(member)}, Reason: why}
}

// PickSpeakers 主人手选出口（NextSpeaker 手选路径的对偶入口）：逐名校验（成员
// 资格 + 发言预算），任一非法整单拒绝（部分非法不静默换人补位）；全部合法才产
// 出指名。主人权威在「从合法候选中选」，不在绕过额度——提高上限属显式配置变更
// （DiscussionPolicy），不设旁路。
func (e *Engine) PickSpeakers(space collab.SpaceView, counts map[string]int64, pol DiscussionPolicy, members []string) (DiscussionDirective, error) {
	if len(members) == 0 {
		return DiscussionDirective{}, ErrSpeakerEmpty
	}
	cleaned := make([]string, 0, len(members))
	for _, m := range members {
		if err := e.ValidateSpeaker(space, counts, pol, m); err != nil {
			return DiscussionDirective{}, err
		}
		cleaned = append(cleaned, strings.TrimSpace(m))
	}
	why := "主人手选指名"
	if len(cleaned) > 1 {
		why = fmt.Sprintf("主人手选显式并发指名 %d 名", len(cleaned))
	}
	return DiscussionDirective{Next: cleaned, Reason: why}, nil
}

// discussionConverged 候选定版收束判定（读引擎候选索引；调用方判定主链）：
// 无 pending 且至少一条 finalized → 收束（定版 ≠ 完成交付）。零候选不收束
// （讨论尚无结论，继续在预算内推进）。
func (e *Engine) discussionConverged(spaceID string) (bool, string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	lp, ok := e.loops[spaceID]
	if !ok {
		return false, ""
	}
	var pending, finalized int
	for i := range lp.Candidates {
		switch lp.Candidates[i].Status {
		case CandidatePending:
			pending++
		case CandidateFinalized:
			finalized++
		}
	}
	if pending == 0 && finalized > 0 {
		return true, fmt.Sprintf("候选已定版收束（finalized %d 条、无 pending）：讨论不再派发（定版≠交付，完成走判据核对）", finalized)
	}
	return false, ""
}
