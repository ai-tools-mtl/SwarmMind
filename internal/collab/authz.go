// 逐动作授权纯判定（W04-01 RFC §三）：主体由调用方传入服务端已验证身份——本包
// 不读 HTTP、不采信请求自报。「主人」= Node local_token / Hub 人工管理会话；
// PAT/编排凭据为 orchestrate，不冒充人工；内置服务使用独立可审计 system 主体。
// 授权通过 ≠ 动作成立：状态机/租约/CAS/预算/版本校验仍在 Store 事务内执行，
// 不因角色跳过（任务书逐动作表前置限制）。
package collab

import (
	"strings"
	"time"
)

// SubjectClass 主体类别（服务端鉴权层解析，随 Subject 传入）。
type SubjectClass string

const (
	// ClassOwner 主人：Node local_token / Hub 人工管理会话。
	ClassOwner SubjectClass = "owner"
	// ClassOrchestrate 编排凭据主体：Node 共享编排凭据（prime_token）/ Hub PAT。
	// 共享凭据只能识别「该授权主体」，不承诺同凭据调用者间隔离（任务书）。
	ClassOrchestrate SubjectClass = "orchestrate"
	// ClassSystem 内置服务独立可审计主体。
	ClassSystem SubjectClass = "system"
)

// Valid 取值校验。
func (c SubjectClass) Valid() bool {
	switch c {
	case ClassOwner, ClassOrchestrate, ClassSystem:
		return true
	}
	return false
}

// Subject 服务端已验证主体。
type Subject struct {
	Name  string       `json:"name"`
	Class SubjectClass `json:"class"`
}

// Valid 主体非空校验。
func (s Subject) Valid() bool { return strings.TrimSpace(s.Name) != "" && s.Class.Valid() }

// String 展示形态（Class:Name，审计事件 Actor 用）。
func (s Subject) String() string { return string(s.Class) + ":" + s.Name }

// Action 控制动作（逐动作授权表的键）。
type Action string

const (
	ActPause            Action = "pause"
	ActResume           Action = "resume"
	ActReady            Action = "ready"    // 配置完成标记（pending_config→ready，成员须齐）
	ActStart            Action = "start"    // 显式启动（ready→running）
	ActDispatch         Action = "dispatch" // 指名派发（模型判定面；执行受理 W04-02 接线）
	ActConfigureMembers Action = "configure_members"
	ActSetBudget        Action = "set_budget"
	ActPolicy           Action = "policy"
	ActTakeover         Action = "takeover"
	ActRenew            Action = "renew"
	ActFinish           Action = "finish"
	ActFinalize         Action = "finalize"
	ActArchive          Action = "archive"
	// ActSetPhase 循环阶段落账（W05-03 最小追加）：经策略判定后由装配层调用写
	// Space.Phase——策略判定与状态落账分离（判定在 strategy，落账在 collab）。
	ActSetPhase Action = "set_phase"
)

// Valid 取值校验。
func (a Action) Valid() bool {
	switch a {
	case ActPause, ActResume, ActReady, ActStart, ActDispatch, ActConfigureMembers,
		ActSetBudget, ActPolicy, ActTakeover, ActRenew, ActFinish, ActFinalize, ActArchive,
		ActSetPhase:
		return true
	}
	return false
}

// isDriver 驱动者判定（RFC §三）：主体与负责人同族 + 同名 + 携带当前任期 +
// 租约有效。manual 负责人不设租约（LeaseValid 恒真）。
func isDriver(sp *Space, subj Subject, term uint64, now time.Time) bool {
	if !renewableDriver(sp, subj, term) {
		return false
	}
	return sp.Leader.LeaseValid(now)
}

// renewableDriver 续约身份判定（不含租约时效）：同族 + 同名 + 当前任期。
func renewableDriver(sp *Space, subj Subject, term uint64) bool {
	l := sp.Leader
	if l.Term == 0 || term != l.Term {
		return false
	}
	if l.Principal == "" || subj.Name != l.Principal {
		return false
	}
	return leaderClass(l.Kind) == subj.Class
}

// Authorize 逐动作授权判定（纯函数，零副作用）。返回 nil = 授权通过；仍须经
// Store 事务内状态/版本/CAS/预算校验后动作才成立。
//
//	owner：manual 模式或完成显式接管（持当前任期）后可 resume/start/dispatch；
//	其余 owner 动作见逐动作表。orchestrate：仅当前驱动者形态（合法领取外部
//	空间后）；takeover 仅限领取四条件（claimable）。system：驱动者形态同权。
func Authorize(act Action, sp *Space, subj Subject, term uint64, now time.Time) error {
	if !subj.Valid() {
		return errf(ErrInvalid, "主体缺失（name/class 必填且取值合法）")
	}
	if sp == nil {
		return ErrNotFound
	}
	isOwner := subj.Class == ClassOwner
	driver := isDriver(sp, subj, term, now)
	switch act {
	case ActPause, ActConfigureMembers, ActReady:
		// 主人或当前驱动者（ready=配置完成标记，与 configure_members 同权限族）。
		if isOwner || driver {
			return nil
		}
	case ActResume, ActStart, ActDispatch, ActSetPhase:
		// 当前驱动者；主人在 manual 模式或完成显式接管（成为驱动者）后。
		// set_phase 与 resume/start 同族（驱动者推进循环阶段；策略转换合法性
		// 判定归 strategy，本表只定「谁可驱动阶段」）。
		if driver {
			return nil
		}
		if isOwner && sp.Mode == ModeManual {
			return nil
		}
	case ActSetBudget, ActPolicy, ActFinalize, ActArchive:
		// 仅主人。
		if isOwner {
			return nil
		}
	case ActTakeover:
		// 主人显式 CAS 任选 kind；编排主体仅可领取待领取空间（claimable 四条件，
		// 由事务内复核）；system 内置服务以 owner 语义接管。
		if isOwner || subj.Class == ClassSystem {
			return nil
		}
		if subj.Class == ClassOrchestrate && sp.claimable(subj.Name) {
			return nil
		}
	case ActRenew:
		// 续约身份判定：同主体/同族/当前 term（租约时效不在授权层判——首期授予
		//（builtin 创建时租约空）与「过期拒绝复活」的时效裁决在 controlRenew：
		// LeaseUntil 零值=未授予可首授；已过期=拒绝复活）。
		if renewableDriver(sp, subj, term) {
			return nil
		}
	case ActFinish:
		// 仅当前驱动者（含明确授权的内置服务——其驱动者形态即 system 主体持任期）。
		if driver {
			return nil
		}
	default:
		return errf(ErrInvalid, "未知动作 %q", string(act))
	}
	return errf(ErrForbidden, "动作 %s 拒绝：主体 %s 非该动作有权主体（当前任期 %d、负责人 %s/%s）",
		string(act), subj.String(), sp.Leader.Term, string(sp.Leader.Kind), sp.Leader.Principal)
}

// claimable 领取四条件（RFC §1.3）：external 待领取空间 + 从未建立任期 + 领取
// 约束允许。租约过期后的接管不在此列（由人确认，编排主体不自动抢回）。
func (sp *Space) claimable(principal string) bool {
	if sp.Mode != ModeExternal || sp.Status != StatusPendingLeader || sp.Leader.Term != 0 {
		return false
	}
	return sp.Policy.claimAllowed(principal)
}
