// W05-01 策略接口面（W04-01 RFC §四，只定接口不定实现）：无副作用验证器——
// 候选校验 / 完成判据 / 阶段推进判定。实现由 W05-01 供给，collab 在完成类动作
// （finish / finalize done）与阶段推进点回调。hooks 未装配时完成类动作一律拒绝
// （ErrFinishPolicyUnavailable）：W04 只开放主人 finalize→abandoned，不提供
// 「无判据的完成」。
package collab

// CompletionVerdict 完成判据结论（纯判定结果，不改状态）。
type CompletionVerdict struct {
	// Satisfied 验收是否满足。
	Satisfied bool
	// Reason 人话结论（落审计事件）。
	Reason string
	// EvidenceRefs 判据引用的证据定位（版本号/测试回执/问题清单等）。
	EvidenceRefs []string
}

// PhaseDecision 阶段推进判定（纯判定；是否落账由调用方事务决定）。
type PhaseDecision struct {
	// Advance 是否建议推进。
	Advance bool
	// NextPhase 建议的下一阶段（策略域值；W05 赋义）。
	NextPhase string
	// Reason 人话依据（落审计事件）。
	Reason string
}

// CandidateRecord 候选记录校验入参（W04-01 最小形态：W05-01 结构化 schema
// 落地时按需扩展字段；本包不解释内容）。
type CandidateRecord struct {
	Member string `json:"member"` // 候选来源成员
	Body   string `json:"body"`   // 候选正文（FINAL: 前缀或结构化载荷）
}

// StrategyHooks 策略接口面（W05-01 实现；Store 经 SetHooks 一次性注入）。
//
// 实现约束：三个方法都必须无副作用（纯判定/读档），不得派发执行、不得改写
// 空间状态——判据的消费与落账一律由 collab 事务完成，保证「决策窗口内变化仍
// 事务复核」（N08：判定发生在 mutate 副本上，与扣账/状态变更同事务提交）。
type StrategyHooks interface {
	// EvaluateCompletion 完成判据：验收是否满足。
	EvaluateCompletion(space SpaceView) CompletionVerdict
	// ValidateCandidate 候选校验：FINAL 候选/结构化候选合法性（非法候选拒绝，
	// 不静默改用别的候选——E10）。
	ValidateCandidate(space SpaceView, candidate CandidateRecord) error
	// DecidePhaseAdvance 阶段推进判定。
	DecidePhaseAdvance(space SpaceView) PhaseDecision
}
