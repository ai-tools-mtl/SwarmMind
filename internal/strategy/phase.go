// 循环阶段模型（W05-01 交付③）：编写→冻结→审查→返工→复审→完成/暂停 的显式
// 阶段状态机。沿 collab 九态机先例（internal/collab/model.go statusTransitions）：
// 转换表显式枚举、表外路径一律非法（CanAdvancePhase 拒绝），阶段原始值独立定义、
// 不拼凑 collab 空间九态。
//
// 阶段真相唯一归属 collab.Space.Phase（W04-01 RFC §1.1「策略域不透明值：W04 只
// 存不解释；W05-01 赋义」）：本包只定义取值/转换表/纯判定；阶段落账由 W05-03 经
// Engine.SetPhase 校验后写入 collab（本包不持久化、不直改空间状态）。
//
// 关键语义（PRODUCT-PLAN-v3 §6 硬规）：发言结束、提交候选结论、审查通过和目标
// 完成是不同事件——审查通过（review/rereview 无未解阻塞问题）不等于完成，完成
// 判定归 collab.StrategyHooks.EvaluateCompletion（finish/finalize done 判据核对），
// 阶段推进判定永不建议直接跳到 completed。
package strategy

// Phase 写审循环阶段（策略域原始值；落 collab.Space.Phase 不透明字符串）。
type Phase string

const (
	PhaseDrafting  Phase = "drafting"  // 编写
	PhaseFrozen    Phase = "frozen"    // 冻结（编写产出固定版本，等待审查）
	PhaseReview    Phase = "review"    // 审查（针对冻结版本提问题）
	PhaseRework    Phase = "rework"    // 返工（存在未解问题，回原编写线程修复）
	PhaseRereview  Phase = "rereview"  // 复审（修复后新版本回原审查线程）
	PhaseCompleted Phase = "completed" // 完成（循环终态；仅经完成判据核对后进入）
	PhasePaused    Phase = "paused"    // 暂停（预算耗尽/意见两次未解/验收争议——保留进度等人工）
)

// Valid 取值校验（空串不是合法阶段：未初始化 ≠ drafting，绝不猜测）。
func (p Phase) Valid() bool {
	switch p {
	case PhaseDrafting, PhaseFrozen, PhaseReview, PhaseRework, PhaseRereview, PhaseCompleted, PhasePaused:
		return true
	}
	return false
}

// Terminal 循环终态（completed 之后阶段机不再转换；暂停不是终态——保留进度可恢复）。
func (p Phase) Terminal() bool { return p == PhaseCompleted }

// phaseTransitions 阶段转换表（显式枚举；表外路径非法）：
//   - drafting → frozen：编写产出已登记（版本存在）才可冻结；
//   - frozen → review：冻结完成且审查成员在位；
//   - review → rework / completed：存在未解问题→返工；完成→必须经完成判据
//     （EvaluateCompletion）核对，阶段边只表示「审查后允许完成」，先决条件由
//     完成判据独立把守；
//   - rework → rereview：修复后新版本（hash ≠ 被审查版本）登记才可复审；
//   - rereview → rework / completed：复审发现未解问题→再返工（循环）；
//   - paused → 各非终态 / completed：恢复恒回到暂停前记录的阶段（PausedFrom，
//     Engine.SetPhase 校验），暂停中人工定版允许 completed；
//   - completed → （无）。
var phaseTransitions = map[Phase][]Phase{
	PhaseDrafting:  {PhaseFrozen, PhasePaused},
	PhaseFrozen:    {PhaseReview, PhasePaused},
	PhaseReview:    {PhaseRework, PhaseCompleted, PhasePaused},
	PhaseRework:    {PhaseRereview, PhasePaused},
	PhaseRereview:  {PhaseRework, PhaseCompleted, PhasePaused},
	PhasePaused:    {PhaseDrafting, PhaseFrozen, PhaseReview, PhaseRework, PhaseRereview, PhaseCompleted},
	PhaseCompleted: {},
}

// PhaseEdges 全部合法阶段边（自审/测试遍历用；返回副本，调用方不可改表）。
func PhaseEdges() map[Phase][]Phase {
	out := make(map[Phase][]Phase, len(phaseTransitions))
	for from, tos := range phaseTransitions {
		out[from] = append([]Phase(nil), tos...)
	}
	return out
}

// CanAdvancePhase 阶段转换合法性（显式函数，非法转换拒绝；同相不自转）。
func CanAdvancePhase(from, to Phase) bool {
	if from == to || !from.Valid() || !to.Valid() {
		return false
	}
	for _, p := range phaseTransitions[from] {
		if p == to {
			return true
		}
	}
	return false
}

// ParsePhase 解析 collab.Space.Phase 存量字符串（未初始化/未知值返回 false——
// 判定面绝不把未知值当 drafting 猜）。
func ParsePhase(s string) (Phase, bool) {
	p := Phase(s)
	if !p.Valid() {
		return "", false
	}
	return p, true
}
