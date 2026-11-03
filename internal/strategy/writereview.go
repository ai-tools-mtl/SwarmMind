// writereview.go 编写审查策略（W05-03）：编写→冻结→审查→返工→复审→完成/暂停
// 循环的装配层驱动判定。定位（任务书 W05-03 行）：循环驱动判定**复用** W05-01
// 既有判据——阶段推进（DecidePhaseAdvance）、issue 重开投影（RecordIssue 同 key
// 重报已确认问题即回归重开，阻塞返工触发）、两次未解暂停（EvaluateStop 修复阈
// 值）；本文件只做三者的单步装配组合，不新增第二套循环语义。
//
// 分离边界（W05-03 定案，供 RFC 引用）：**判定在 strategy、落账在 collab**——
//  1. AdvanceCycle 纯判定产出三类指令之一：暂停建议 / 阶段推进建议 / 下一行动
//     成员；本包绝不改空间状态、绝不派发；
//  2. 阶段落账：装配层 ValidatePhaseTransition 预检 → collab Control(ActSetPhase)
//     事务落账（授权/版本/状态全在 collab 事务复核）→ SetPhase 记引擎侧留痕与
//     PausedFrom（引擎事实跟随落账结果，不预写）；
//  3. 暂停/恢复：装配层 collab Control(pause/resume) 落空间状态，阶段面同通道
//     ActSetPhase(paused / PausedFrom) 落账；
//  4. 下一棒派发：装配层经 spaceDispatch 通路（W04-02 串行调度）指名投递。
package strategy

import (
	"fmt"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// ActorAction 写审循环行动词表（投递正文/展示用；值即语义）。
const (
	ActorWrite    = "write"    // 编写（产出登记版本）
	ActorReview   = "review"   // 审查（对冻结版本提问题）
	ActorFix      = "fix"      // 返工（回原编写线程修复并登记新版本）
	ActorReReview = "rereview" // 复审（对修复后新版本复审）
)

// CycleDirective 写审循环单步驱动判定（AdvanceCycle 输出；纯判定结果，三类指令
// 互斥优先级：暂停 > 阶段推进 > 下一行动成员）。
type CycleDirective struct {
	// ShouldPause 暂停建议（EvaluateStop 任一触发：预算耗尽/同一问题修复阈值/
	// 验收争议）。落账由装配层执行（collab pause + ActSetPhase paused），本判定
	// 不自动等于暂停。
	ShouldPause   bool          `json:"should_pause,omitempty"`
	PauseTriggers []StopTrigger `json:"pause_triggers,omitempty"`
	// Advance 阶段推进建议（DecidePhaseAdvance.Advance 原样投影）。
	Advance     bool   `json:"advance,omitempty"`
	NextPhase   Phase  `json:"next_phase,omitempty"`
	PhaseReason string `json:"phase_reason,omitempty"`
	// NextActor 当前阶段应行动成员（不推进且非暂停时产出；同角色多成员取成员
	// 名单序首个——判定可复现；无对应角色为空，手选/配置出口归装配层）。
	NextActor   string `json:"next_actor,omitempty"`
	ActorAction string `json:"actor_action,omitempty"`
	// Reason 结论人话（轮终注记/展示）。
	Reason string `json:"reason"`
}

// AdvanceCycle 写审循环单步驱动判定（W05-03 装配层入口；纯判定无副作用）：
//  1. 停止判定优先（EvaluateStop：预算三分离耗尽/同一问题修复 N 次未过复审/
//     验收争议）——暂停建议压倒一切推进（额度耗尽保留进度，不换模型不无限重试）；
//  2. 阶段推进判定（复用 DecidePhaseAdvance）：issue 重开投影生效即返工触发
//     （review/rereview 阻塞计数含重开），修复后新版本登记即复审触发；
//  3. 不推进：产出当前阶段下一行动成员（角色定位；在途执行时不产出——等轮终
//     收敛，绝不叠投）。
func (e *Engine) AdvanceCycle(space collab.SpaceView, now time.Time) CycleDirective {
	// 1. 停止判定优先。
	if stop := e.EvaluateStop(space, now); stop.ShouldPause {
		return CycleDirective{
			ShouldPause:   true,
			PauseTriggers: stop.Triggers,
			Reason:        "停止判定触发：建议暂停保留进度（恢复须显式；见 pause_triggers）",
		}
	}
	phase, ok := ParsePhase(space.Phase)
	if !ok {
		return CycleDirective{Reason: fmt.Sprintf("循环阶段未初始化或未知（%q）：先经 BootstrapCycle 引导 drafting 再驱动", space.Phase)}
	}
	// 2. 阶段推进判定。
	if dec := e.DecidePhaseAdvance(space); dec.Advance {
		return CycleDirective{
			Advance:     true,
			NextPhase:   Phase(dec.NextPhase),
			PhaseReason: dec.Reason,
			Reason:      dec.Reason,
		}
	}
	// 3. 下一行动成员（在途时不产出：串行调度一次一棒，等轮终收敛）。
	if space.InFlight > 0 {
		return CycleDirective{Reason: fmt.Sprintf("存在在途执行 %d 项：等待轮终收敛后再驱动", space.InFlight)}
	}
	member, action, ok := actorForPhase(space, phase)
	if !ok {
		return CycleDirective{Reason: phaseWaitReason(phase)}
	}
	return CycleDirective{
		NextActor:   member,
		ActorAction: action,
		Reason:      fmt.Sprintf("阶段 %s 等待行动：%s 应执行 %s", string(phase), member, action),
	}
}

// actorForPhase 阶段 → 行动成员定位（纯判定）。drafting/rework → 编写角色；
// review/rereview → 审查角色；frozen 无行动者（推进判定自动建议，角色缺位时
// 保持等待）；paused/completed/未知无行动者。
func actorForPhase(space collab.SpaceView, phase Phase) (member, action string, ok bool) {
	switch phase {
	case PhaseDrafting:
		if m, found := writerMember(space); found {
			return m, ActorWrite, true
		}
	case PhaseReview:
		if m, found := reviewerMember(space); found {
			return m, ActorReview, true
		}
	case PhaseRework:
		if m, found := writerMember(space); found {
			return m, ActorFix, true
		}
	case PhaseRereview:
		if m, found := reviewerMember(space); found {
			return m, ActorReReview, true
		}
	}
	return "", "", false
}

// writerMember 查编写角色成员（Role=writer|write|author 不分大小写，成员名单序
// 首个；与 reviewerMember 同判定形态——无角色不猜测，手选/配置出口归装配层）。
func writerMember(space collab.SpaceView) (string, bool) {
	for i := range space.Members {
		role := strings.ToLower(strings.TrimSpace(space.Members[i].Role))
		if role == "writer" || role == "write" || role == "author" {
			return space.Members[i].Name, true
		}
	}
	return "", false
}

// phaseWaitReason 无行动者时的人话依据（配置/状态出口指路）。
func phaseWaitReason(phase Phase) string {
	switch phase {
	case PhaseFrozen:
		return "冻结中：等待阶段推进判定（审查角色缺位时先配置 reviewer）"
	case PhasePaused:
		return "阶段暂停中：恢复须显式（回到暂停前阶段）"
	case PhaseCompleted:
		return "循环已完成（终态）"
	}
	return fmt.Sprintf("阶段 %s 无可定位行动成员（先配置 writer/reviewer 角色或人工驱动）", string(phase))
}

// ---------- 阶段落账判定面（分离边界的 strategy 侧） ----------

// ValidatePhaseTransition 阶段转换预检（无副作用；W05-03 分离边界：装配层先本
// 方法校验 → collab Control(ActSetPhase) 落账 → 成功后 SetPhase 记引擎侧事实）。
// 规则与 SetPhase 完全同源：来源合法 + 表内边 + 暂停恢复恒回 PausedFrom（暂停中
// 人工定版到 completed 豁免——phaseTransitions 表边）。
func (e *Engine) ValidatePhaseTransition(spaceID string, from, to Phase) error {
	if !from.Valid() {
		return errf(ErrInvalid, "来源阶段非法 %q", string(from))
	}
	if !CanAdvancePhase(from, to) {
		return errf(ErrInvalid, "非法阶段转换 %s → %s（表外路径）", string(from), string(to))
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if from == PhasePaused && to != PhaseCompleted {
		lp := e.loop(spaceID, false)
		if lp == nil || to != lp.PausedFrom {
			want := Phase("")
			if lp != nil {
				want = lp.PausedFrom
			}
			return errf(ErrInvalid, "暂停恢复须回到暂停前阶段 %s，得 %s", string(want), string(to))
		}
	}
	return nil
}

// BootstrapCycle 写审循环引导判定（装配层在空间显式启动后调用；幂等）：阶段未
// 初始化 → 引导至 drafting（留痕）并返回目标；已初始化 → 原样返回当前阶段零
// 变更。drafting 之后全部转换走 phaseTransitions 表（引导是唯一表外边，显式方法
// 承担、不进转换表——转换表 18 边冻结由 phase_test 全枚举锁）。公开壳：锁内实现
// 完整返回（写锁已释放）后按实际引导触发持久化通知——已初始化幂等零变更不通知。
func (e *Engine) BootstrapCycle(space collab.SpaceView) (Phase, error) {
	phase, changed, err := e.bootstrapCycle(space)
	if changed {
		e.notifyPersist()
	}
	return phase, err
}

// bootstrapCycle BootstrapCycle 的锁内实现（返回 changed = 是否实际引导变更）。
func (e *Engine) bootstrapCycle(space collab.SpaceView) (Phase, bool, error) {
	if p, ok := ParsePhase(space.Phase); ok {
		return p, false, nil // 已初始化：幂等零变更
	}
	if strings.TrimSpace(space.ID) == "" {
		return "", false, errf(ErrInvalid, "空间 ID 必填")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(space.ID, true)
	e.appendTrail(lp, TrailPhaseChanged, "循环初始化 → drafting（引导边）")
	return PhaseDrafting, true, nil
}

// SetPhase 循环阶段变更校验与记录——原有语义不变，判定体收敛至
// ValidatePhaseTransition（单一真相）：非法转换拒绝；暂停恢复恒须回到暂停前记录
// 的阶段。校验通过后留痕并维护 PausedFrom（paused 记、离开清）。公开壳：锁内实
// 现完整返回（写锁已释放）后按实际变更触发持久化通知——校验拒绝不通知。
func (e *Engine) SetPhase(spaceID string, from, to Phase) error {
	if err := e.setPhase(spaceID, from, to); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// setPhase SetPhase 的锁内实现。
func (e *Engine) setPhase(spaceID string, from, to Phase) error {
	if err := e.ValidatePhaseTransition(spaceID, from, to); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	if to == PhasePaused {
		lp.PausedFrom = from
	} else {
		lp.PausedFrom = ""
	}
	e.appendTrail(lp, TrailPhaseChanged, fmt.Sprintf("%s → %s", string(from), string(to)))
	return nil
}
