// 空间操作（W04-01）：创建、控制动作（状态转换/成员/预算/策略/接管/续约/完成/
// 定版/归档）、派发意图受理、消息预算扣账、意图收敛。全部经 Store.apply 事务：
// 重放判定先行（同 request_id 同文回原回执零副作用）→ 副本上校验+变更（授权/
// 状态机/CAS/预算/版本全部在同一事务内复核，N08）→ 原子落盘 → 替换内存态。
package collab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CreateRequest 创建请求。
type CreateRequest struct {
	RequestID  string   `json:"request_id"`           // 缺省自动生成（自动生成不承担重放回执）
	Goal       string   `json:"goal"`                 // 必填
	Acceptance string   `json:"acceptance,omitempty"` // 可检查验收条件
	Mode       Mode     `json:"mode"`                 // 必填：manual|builtin|external
	Members    []Member `json:"members,omitempty"`    // 可空（external 流程成员可空）
	Budget     Budget   `json:"budget,omitempty"`     // 三分离定义；零值 = 全不限
	Policy     *Policy  `json:"policy,omitempty"`     // 完成授权/领取约束；缺省 human_only
	By         string   `json:"by,omitempty"`         // 自报展示标签（仅留档，不承担身份）
}

// CreateSpace 创建空间（RFC §1.1/§1.2）：
//   - manual/builtin → pending_config；external → pending_leader（成员可空，
//     AC14 顺序：领取→配置成员→显式启动）。
//   - 初始负责人：manual 即以创建者建立任期 Term=1（创建本身是主人显式动作，
//     manual 无租约）；builtin 以 system 主体建立 Term=1（租约空，须续约后成为
//     有效驱动者）；external 不设负责人（待领取，Term=0）。
//
// 授权：owner 可建任意方式并设定 Policy；orchestrate 仅可建 external（成员可空）
// 且完成授权强制 human_only——请求携 finish_auth=verified_write_review 一律拒绝
// （agent 新建空间不能自授，伪造授权字段拒绝）；system 不开放创建。
func (s *Store) CreateSpace(req CreateRequest, subj Subject) (Space, bool, error) {
	if strings.TrimSpace(req.Goal) == "" {
		return Space{}, false, errf(ErrInvalid, "goal 必填")
	}
	if !req.Mode.Valid() {
		return Space{}, false, errf(ErrInvalid, "mode 须为 manual|builtin|external，得 %q", string(req.Mode))
	}
	if !req.Budget.Valid() {
		return Space{}, false, errf(ErrInvalid, "budget 计数须为非负（0=不限）")
	}
	if req.Policy != nil && !req.Policy.FinishAuth.Valid() {
		return Space{}, false, errf(ErrInvalid, "policy.finish_auth 取值非法 %q", string(req.Policy.FinishAuth))
	}
	if !subj.Valid() {
		return Space{}, false, errf(ErrInvalid, "主体缺失")
	}
	switch subj.Class {
	case ClassOwner:
		// 任意方式。
	case ClassOrchestrate:
		if req.Mode != ModeExternal {
			return Space{}, false, errf(ErrForbidden, "编排主体仅可创建 external 待领取空间（得 %s）", string(req.Mode))
		}
		if req.Policy != nil && req.Policy.FinishAuth == FinishAuthVerifiedWriteReview {
			return Space{}, false, errf(ErrForbidden, "完成授权仅主人可授（agent 新建空间不能自授 verified_write_review）")
		}
	default:
		return Space{}, false, errf(ErrForbidden, "system 主体不开放创建空间")
	}
	members, err := normalizeMembers(req.Members)
	if err != nil {
		return Space{}, false, err
	}
	opID := req.RequestID
	if opID == "" {
		opID = newSpaceID() // 自动生成：唯一，天然不重放
	}
	now := s.now()
	// 空间 ID 由请求 ID 确定性派生：回执被滚动淘汰后的极旧重放也能收敛到同一
	// 空间（幂等去重窗口语义，对照任务保留期先例），不产生第二个空间。
	spaceID := SpaceIDPrefix + derivedHex(opID)
	raw, dup, err := s.apply(opID, "create_space", req, func(next *state) (any, error) {
		if existing, ok := next.Spaces[spaceID]; ok {
			// 回执淘汰后的极旧重放：同键空间已在，返回现状（不再新建）。
			return *cloneSpace(existing), nil
		}
		sp := &Space{
			ID:         spaceID,
			Goal:       strings.TrimSpace(req.Goal),
			Acceptance: strings.TrimSpace(req.Acceptance),
			Mode:       req.Mode,
			Members:    members,
			Budget:     req.Budget,
			Policy:     Policy{FinishAuth: FinishAuthHumanOnly, ClaimPrincipals: claimPrincipalsOf(req.Policy), PolicyVersion: 1},
			Version:    1,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if req.Policy != nil && req.Policy.FinishAuth != "" {
			sp.Policy.FinishAuth = req.Policy.FinishAuth
		}
		switch req.Mode {
		case ModeManual:
			sp.Status = StatusPendingConfig
			sp.Leader = Leader{Kind: LeaderManual, Principal: subj.Name, Term: 1}
			next.TermSeq[sp.ID] = 1
		case ModeBuiltin:
			sp.Status = StatusPendingConfig
			sp.Leader = Leader{Kind: LeaderBuiltin, Principal: "system", Term: 1}
			next.TermSeq[sp.ID] = 1
		case ModeExternal:
			sp.Status = StatusPendingLeader
		}
		// 会话唯一绑定（一线程一空间；冲突即拒绝——创建零副作用由事务副本保证）。
		for i := range sp.Members {
			if sp.Members[i].SessionID == "" {
				continue
			}
			if owner, ok := next.SessionOwners[sp.Members[i].SessionID]; ok && owner != sp.ID {
				return nil, errf(ErrSessionBoundElsewhere, "会话 %s 已绑定空间 %s", sp.Members[i].SessionID, owner)
			}
			t := now
			sp.Members[i].BoundAt = &t
			next.SessionOwners[sp.Members[i].SessionID] = sp.ID
		}
		sp.appendEvent(now, "created", subj.String(), "创建空间（方式 "+string(req.Mode)+"）")
		next.Spaces[sp.ID] = sp
		return *cloneSpace(sp), nil
	})
	if err != nil {
		return Space{}, false, err
	}
	var sp Space
	_ = json.Unmarshal(raw, &sp)
	return sp, dup, nil
}

func claimPrincipalsOf(p *Policy) []string {
	if p == nil {
		return nil
	}
	return p.ClaimPrincipals
}

// normalizeMembers 成员表校验：名字非空唯一；会话 ID 清洗后请求内唯一。
func normalizeMembers(members []Member) ([]Member, error) {
	out := make([]Member, 0, len(members))
	seen := map[string]bool{}
	sessions := map[string]bool{}
	for _, m := range members {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			return nil, errf(ErrInvalid, "成员名必填")
		}
		if seen[name] {
			return nil, errf(ErrInvalid, "成员 %s 重复", name)
		}
		seen[name] = true
		if sid := strings.TrimSpace(m.SessionID); sid != "" {
			if sessions[sid] {
				return nil, errf(ErrInvalid, "会话 %s 在成员表中重复（一成员一原生线程）", sid)
			}
			sessions[sid] = true
			m.SessionID = sid
		} else {
			m.SessionID = ""
		}
		m.Role = strings.TrimSpace(m.Role)
		m.Workspace = strings.TrimSpace(m.Workspace)
		m.BoundAt = nil
		out = append(out, m)
	}
	return out, nil
}

// ControlRequest 控制动作请求（写操作：request_id 必填 + expected_version 必填）。
type ControlRequest struct {
	RequestID       string `json:"request_id"`
	Action          Action `json:"action"`
	ExpectedVersion int64  `json:"expected_version"`
	Term            uint64 `json:"term,omitempty"`          // 驱动者形态携带当前任期
	ExpectedTerm    uint64 `json:"expected_term,omitempty"` // takeover CAS 读档任期（未建立=0）
	// ---- 动作参数 ----
	Members      []Member   `json:"members,omitempty"`       // configure_members 全量替换
	Budget       *Budget    `json:"budget,omitempty"`        // set_budget
	Policy       *Policy    `json:"policy,omitempty"`        // policy（字段级合并）
	TakeoverKind LeaderKind `json:"takeover_kind,omitempty"` // takeover（主人/system 任选）
	LeaseSeconds int        `json:"lease_seconds,omitempty"` // takeover/renew（0=默认 TTL）
	FinalOutcome string     `json:"final_outcome,omitempty"` // finalize: done|abandoned
	Reason       string     `json:"reason,omitempty"`        // finalize(abandoned)/set_budget 原因
	Note         string     `json:"note,omitempty"`          // 暂停原因/备注（审计）
}

// Control 控制动作总入口（RFC §三 逐动作表）。事务内顺序：空间存在 → 版本 CAS
// → 授权（纯判定）→ 动作前置校验（状态机/租约/在途/成员/预算/策略）→ 变更落账。
// 幂等指纹含目标空间维度（payload 包装 spaceID）：同 request_id 跨目标空间即
// request_conflict，绝不静默回放他空间回执（终审 M1）。
func (s *Store) Control(spaceID string, req ControlRequest, subj Subject) (ControlResult, bool, error) {
	if req.Action == "" {
		return ControlResult{}, false, errf(ErrInvalid, "action 必填")
	}
	if !req.Action.Valid() {
		return ControlResult{}, false, errf(ErrInvalid, "未知动作 %q", string(req.Action))
	}
	if req.Action == ActDispatch {
		// 终审 S2：派发不经 Control 静默 no-op（不落回执不占 request_id）——
		// 指名派发受理唯一入口是 RecordDispatch（先于执行持久化的 outbox 语义）。
		return ControlResult{}, false, errf(ErrInvalid, "action=dispatch 须经派发受理入口 RecordDispatch（Control 不受理派发，本请求未登记）")
	}
	now := s.now()
	// payload 包装目标空间：内容指纹覆盖 spaceID（与 ConvergeIntent 同构，终审 M1）。
	payload := struct {
		SpaceID string         `json:"space_id"`
		Req     ControlRequest `json:"req"`
	}{spaceID, req}
	raw, dup, err := s.apply(req.RequestID, "control:"+string(req.Action), payload, func(next *state) (any, error) {
		sp, ok := next.Spaces[spaceID]
		if !ok {
			return nil, ErrNotFound
		}
		// takeover 的并发控制权在 term CAS（expected_term 即 fencing token，任务书
		// 「takeover 带 expected_term 原子 CAS」）：term 裁决先于版本复核，使竞速
		// 败者得到精确的 term_conflict（而非被版本冲突掩盖）。
		if req.Action != ActTakeover && req.ExpectedVersion != sp.Version {
			return nil, errf(ErrVersionConflict, "预期版本 %d ≠ 现版本 %d（读档已过期，刷新后重试）", req.ExpectedVersion, sp.Version)
		}
		if err := Authorize(req.Action, sp, subj, req.Term, now); err != nil {
			return nil, err
		}
		intents := next.Intents[spaceID]
		var (
			applied bool
			note    string
			err     error
		)
		switch req.Action {
		case ActPause:
			applied, note, err = controlPause(sp, req, now)
		case ActResume:
			applied, note, err = controlResume(sp, req, now)
		case ActReady:
			applied, note, err = controlReady(sp, req, now)
		case ActStart:
			applied, note, err = controlStart(sp, req, now)
		case ActConfigureMembers:
			applied, note, err = s.controlConfigureMembers(next, sp, req, now)
		case ActSetBudget:
			applied, note, err = controlSetBudget(sp, req, now)
		case ActPolicy:
			applied, note, err = controlPolicy(sp, intents, req, now)
		case ActTakeover:
			applied, note, err = s.controlTakeover(next, sp, req, subj, now)
		case ActRenew:
			applied, note, err = s.controlRenew(sp, req, now)
		case ActFinish:
			applied, note, err = s.controlFinish(sp, intents, req, subj, now)
		case ActFinalize:
			applied, note, err = s.controlFinalize(sp, intents, req, subj, now)
		case ActArchive:
			applied, note, err = controlArchive(sp, req, now)
		}
		if err != nil {
			return nil, err
		}
		if applied {
			sp.Version++
			sp.UpdatedAt = now
			sp.appendEvent(now, string(req.Action), subj.String(), note)
		}
		return ControlResult{Space: spaceView(*cloneSpace(sp), intents), Action: req.Action, Applied: applied}, nil
	})
	if err != nil {
		return ControlResult{}, false, err
	}
	var out ControlResult
	_ = json.Unmarshal(raw, &out)
	out.Dup = dup
	return out, dup, nil
}

// controlPause 暂停：running/blocked/needs_review → paused。只阻止新增派发，
// 不取消在途、不换任期（在途结果照收落账）。
func controlPause(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if !CanTransition(sp.Status, StatusPaused) {
		return false, "", errf(ErrIllegalTransition, "pause：状态 %s 不可暂停（暂停针对运行/受阻/待核对）", sp.Status)
	}
	sp.Used.settleRunning(now)
	sp.Status = StatusPaused
	sp.PausedReason = truncateNote(req.Note)
	return true, "暂停（" + firstNonEmpty(truncateNote(req.Note), "阻止新增派发；在途照收") + "）", nil
}

// controlResume 恢复：paused/blocked/needs_review → running（恢复后须显式驱动，
// 结果回流不自动推进）。
func controlResume(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if !CanTransition(sp.Status, StatusRunning) {
		return false, "", errf(ErrIllegalTransition, "resume：状态 %s 不可恢复为运行", sp.Status)
	}
	sp.Status = StatusRunning
	sp.PausedReason = ""
	sp.Used.RunningSince = now
	return true, "恢复运行（继续由当前负责人驱动）", nil
}

// controlReady 就绪标记：pending_config → ready（成员须齐——可运行以成员在位
// 为最小配置）。
func controlReady(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if !CanTransition(sp.Status, StatusReady) {
		return false, "", errf(ErrIllegalTransition, "ready：状态 %s 不可标记就绪", sp.Status)
	}
	if len(sp.Members) == 0 {
		return false, "", ErrNoMembers
	}
	sp.Status = StatusReady
	return true, "配置就绪（可运行，等待显式启动）", nil
}

// controlStart 显式启动：ready → running（成员须齐 + 有效负责人 + 租约有效）。
func controlStart(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if !CanTransition(sp.Status, StatusRunning) {
		return false, "", errf(ErrIllegalTransition, "start：状态 %s 不可启动（先 ready 就绪）", sp.Status)
	}
	if len(sp.Members) == 0 {
		return false, "", ErrNoMembers
	}
	if !sp.Leader.Valid(now) {
		return false, "", errf(ErrForbidden, "启动要求有效负责人（当前任期 %d、租约有效性不足；external/builtin 须先接管/领取并续约）", sp.Leader.Term)
	}
	sp.Status = StatusRunning
	sp.Used.RunningSince = now
	return true, "显式启动（运行；派发由当前负责人执行）", nil
}

// controlConfigureMembers 成员变更：仅 pending_config/paused；涉及在途成员的
// 移除或换绑拒绝（先收束或核对）；会话唯一绑定全局校验。
func (s *Store) controlConfigureMembers(next *state, sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if sp.Status != StatusPendingConfig && sp.Status != StatusPaused {
		return false, "", errf(ErrIllegalTransition, "configure_members：仅待配置/暂停态可变更成员（现 %s）", sp.Status)
	}
	members, err := normalizeMembers(req.Members)
	if err != nil {
		return false, "", err
	}
	inFlight := sp.inFlightMembers(next.Intents[sp.ID])
	for name := range inFlight {
		old := sp.memberOf(name)
		newIdx := -1
		for i := range members {
			if members[i].Name == name {
				newIdx = i
				break
			}
		}
		if newIdx < 0 || members[newIdx].SessionID != sp.Members[old].SessionID {
			return false, "", errf(ErrMemberInFlight, "成员 %s 存在在途执行，不可移除或换绑（先收束或核对）", name)
		}
	}
	// 解绑移除成员的会话（仅当归属本空间）。
	for _, old := range sp.Members {
		if old.SessionID == "" {
			continue
		}
		still := false
		for _, m := range members {
			if m.Name == old.Name && m.SessionID == old.SessionID {
				still = true
				break
			}
		}
		if !still && next.SessionOwners[old.SessionID] == sp.ID {
			delete(next.SessionOwners, old.SessionID)
		}
	}
	// 绑定新会话（全局唯一归属校验）。
	for i := range members {
		if members[i].SessionID == "" {
			continue
		}
		if owner, ok := next.SessionOwners[members[i].SessionID]; ok && owner != sp.ID {
			return false, "", errf(ErrSessionBoundElsewhere, "会话 %s 已绑定空间 %s", members[i].SessionID, owner)
		}
		if members[i].BoundAt == nil {
			t := now
			members[i].BoundAt = &t
		}
		next.SessionOwners[members[i].SessionID] = sp.ID
	}
	sp.Members = members
	return true, fmt.Sprintf("成员变更（现 %d 名）", len(members)), nil
}

// controlSetBudget 预算变更（仅主人；不自动 resume——改预算不恢复运行）。
func controlSetBudget(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if sp.Status.Terminal() {
		return false, "", errf(ErrIllegalTransition, "set_budget：终态空间不可变更预算")
	}
	if req.Budget == nil {
		return false, "", errf(ErrInvalid, "budget 必填")
	}
	if !req.Budget.Valid() {
		return false, "", errf(ErrInvalid, "budget 计数须为非负（0=不限）")
	}
	sp.Budget = *req.Budget
	return true, "预算变更（" + truncateNote(req.Reason) + "）", nil
}

// controlPolicy 策略变更（仅主人）：待配置/暂停且无在途；策略版本递增。
func controlPolicy(sp *Space, intents []DispatchIntent, req ControlRequest, now time.Time) (bool, string, error) {
	if sp.Status != StatusPendingConfig && sp.Status != StatusPaused {
		return false, "", errf(ErrIllegalTransition, "policy：仅待配置/暂停态可变更（现 %s）", sp.Status)
	}
	if sp.inFlightCount(intents) > 0 {
		return false, "", ErrExecutionInFlight
	}
	if req.Policy == nil {
		return false, "", errf(ErrInvalid, "policy 必填")
	}
	if req.Policy.FinishAuth != "" {
		if !req.Policy.FinishAuth.Valid() {
			return false, "", errf(ErrInvalid, "finish_auth 取值非法 %q", string(req.Policy.FinishAuth))
		}
		sp.Policy.FinishAuth = req.Policy.FinishAuth
	}
	if req.Policy.ClaimPrincipals != nil {
		sp.Policy.ClaimPrincipals = append(make([]string, 0, len(req.Policy.ClaimPrincipals)), req.Policy.ClaimPrincipals...)
	}
	sp.Policy.PolicyVersion++
	return true, fmt.Sprintf("策略变更（finish_auth=%s policy_version=%d）", string(sp.Policy.FinishAuth), sp.Policy.PolicyVersion), nil
}

// controlTakeover 接管（CAS 单事务）：expected_term 与当前任期不符即拒绝（同刻
// 两方接管只一成功）；term 服务端分配递增、永不复用。状态变更一律经 CanTransition
// 判定（终审 S1：表外转换直赋即双真相）。
//   - 主人/system 选择 manual/builtin：直接就任（manual 不设租约；builtin/external
//     授予租约），调度方式随接管 kind 切换，不改状态；
//   - 主人/system 选择 external：开放领取——清空负责人与任期（旧任期随之失效，
//     E14「不自动抢回」的 owner 侧出口），空间 pending_config → pending_leader
//     （转换表边；其余状态拒绝 illegal_transition——运行/暂停空间的重新开放领取
//     须先收束并放弃定版后重建，不设运行中换轨的表外通道）；
//   - 编排主体：仅领取（claimable 四条件已在授权层于本事务副本上复核，其中
//     Status==pending_leader 为领取前置），固定 external，领取成功
//     pending_leader → pending_config。
func (s *Store) controlTakeover(next *state, sp *Space, req ControlRequest, subj Subject, now time.Time) (bool, string, error) {
	if sp.Status.Terminal() {
		return false, "", errf(ErrIllegalTransition, "takeover：终态空间不可接管")
	}
	if req.ExpectedTerm != sp.Leader.Term {
		return false, "", errf(ErrTermConflict, "expected_term %d ≠ 当前任期 %d（同刻仅一方接管成功；刷新读档后重试）", req.ExpectedTerm, sp.Leader.Term)
	}
	if req.ExpectedVersion != sp.Version {
		return false, "", errf(ErrVersionConflict, "预期版本 %d ≠ 现版本 %d（读档已过期，刷新后重试）", req.ExpectedVersion, sp.Version)
	}
	kind := req.TakeoverKind
	if subj.Class == ClassOrchestrate {
		kind = LeaderExternal // 领取固定 external
	}
	if !kind.Valid() {
		return false, "", errf(ErrInvalid, "takeover_kind 须为 manual|builtin|external，得 %q", string(kind))
	}
	prev := sp.Leader
	if kind == LeaderExternal {
		// 主人开放领取（或编排主体领取——claimable 保证 Term==0 时才可达此处）。
		if subj.Class == ClassOrchestrate {
			if !CanTransition(sp.Status, StatusPendingConfig) {
				return false, "", errf(ErrIllegalTransition, "takeover(claim)：状态 %s 不可转待配置", sp.Status)
			}
			ttl, err := s.leaseTTLFor(req.LeaseSeconds)
			if err != nil {
				return false, "", err
			}
			newTerm := next.TermSeq[sp.ID] + 1
			next.TermSeq[sp.ID] = newTerm
			sp.Leader = Leader{Kind: LeaderExternal, Principal: subj.Name, Term: newTerm, LeaseUntil: now.Add(ttl)}
			sp.Mode = ModeExternal
			sp.Status = StatusPendingConfig // 领取成功：转待配置（成员可空，配置后显式启动）
			return true, fmt.Sprintf("领取：任期 %d → %s/%s 任期 %d（租约至 %s）", prev.Term,
				string(kind), subj.Name, newTerm, sp.Leader.LeaseUntil.UTC().Format(time.RFC3339)), nil
		}
		if !CanTransition(sp.Status, StatusPendingLeader) {
			return false, "", errf(ErrIllegalTransition, "takeover(open-claim)：仅待配置空间可开放外部领取（现 %s；运行中空间先收束并放弃定版）", sp.Status)
		}
		sp.Leader = Leader{Kind: LeaderExternal}
		sp.Mode = ModeExternal
		sp.Status = StatusPendingLeader
		return true, fmt.Sprintf("开放外部领取：任期 %d（%s/%s）失效，空间转待领取（领取约束 policy_version=%d 不变）",
			prev.Term, string(prev.Kind), prev.Principal, sp.Policy.PolicyVersion), nil
	}
	ttl, err := s.leaseTTLFor(req.LeaseSeconds)
	if err != nil {
		return false, "", err
	}
	newTerm := next.TermSeq[sp.ID] + 1
	next.TermSeq[sp.ID] = newTerm
	// builtin 就任主体恒为 system（内置服务独立可审计主体——选 builtin 即把驱动
	// 交给内置服务；主人要亲自驱动选 manual）。external 领取主体即领取者。
	principal := subj.Name
	if kind == LeaderBuiltin {
		principal = "system"
	}
	leader := Leader{Kind: kind, Principal: principal, Term: newTerm}
	if kind != LeaderManual {
		leader.LeaseUntil = now.Add(ttl)
	}
	sp.Leader = leader
	sp.Mode = kind.Mode()
	return true, fmt.Sprintf("接管：%s/%s 任期 %d → %s/%s 任期 %d", string(prev.Kind), prev.Principal, prev.Term,
		string(kind), subj.Name, newTerm), nil
}

// controlRenew 续约（仅当前驱动者，授权层判定）：同主体/当前 term；租约未过期
// （或从未授予——builtin 创建态 LeaseUntil 零值的首期授予）；过期不能 renew
// 复活（ErrLeaseExpired，接管由主人显式确认）。
func (s *Store) controlRenew(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if sp.Leader.Kind == LeaderManual {
		return false, "", errf(ErrInvalid, "manual 负责人不设租约，无需续约")
	}
	if !sp.Leader.LeaseUntil.IsZero() && !sp.Leader.LeaseValid(now) {
		return false, "", ErrLeaseExpired
	}
	ttl, err := s.leaseTTLFor(req.LeaseSeconds)
	if err != nil {
		return false, "", err
	}
	sp.Leader.LeaseUntil = now.Add(ttl)
	return true, fmt.Sprintf("租约续至 %s（任期 %d）", sp.Leader.LeaseUntil.UTC().Format(time.RFC3339), sp.Leader.Term), nil
}

// controlFinish 完成请求（当前驱动者，含明确授权的内置服务）：主人预先开启
// verified_write_review 完成授权；无在途；策略判据通过（hooks）才落完成。
// 授权默认 human_only——未开启一律拒绝并等待人工定版。
func (s *Store) controlFinish(sp *Space, intents []DispatchIntent, req ControlRequest, subj Subject, now time.Time) (bool, string, error) {
	return s.applyCompletion(sp, intents, req, subj, now, "finish")
}

// controlFinalize 定版（仅主人）：done 仍通过策略验证（判据未装配/未满足即拒）；
// abandoned 必填原因且无未收束执行（不得假清零任务）。
func (s *Store) controlFinalize(sp *Space, intents []DispatchIntent, req ControlRequest, subj Subject, now time.Time) (bool, string, error) {
	switch req.FinalOutcome {
	case "done":
		return s.applyCompletion(sp, intents, req, subj, now, "finalize:done")
	case "abandoned":
		if strings.TrimSpace(req.Reason) == "" {
			return false, "", errf(ErrInvalid, "放弃定版必须填写原因")
		}
		if sp.inFlightCount(intents) > 0 {
			return false, "", ErrExecutionInFlight
		}
		if !CanTransition(sp.Status, StatusAbandoned) {
			return false, "", errf(ErrIllegalTransition, "finalize(abandoned)：状态 %s 不可放弃", sp.Status)
		}
		sp.Used.settleRunning(now)
		sp.Status = StatusAbandoned
		sp.FinalOutcome = "abandoned"
		sp.FinalReason = truncateNote(req.Reason)
		sp.FinalBy = subj.String()
		return true, "放弃定版（" + truncateNote(req.Reason) + "）", nil
	case "":
		return false, "", errf(ErrInvalid, "final_outcome 必填（done|abandoned）")
	default:
		return false, "", errf(ErrInvalid, "final_outcome 须为 done|abandoned，得 %q", req.FinalOutcome)
	}
}

// applyCompletion 完成（finish 与 finalize done 共用）：授权语义不同（finish 驱
// 动者须主人预开完成授权；finalize done 主人直接定版），判据要求相同——hooks
// 判定通过 + 无在途 + 状态机允许。
func (s *Store) applyCompletion(sp *Space, intents []DispatchIntent, req ControlRequest, subj Subject, now time.Time, via string) (bool, string, error) {
	if via == "finish" && sp.Policy.FinishAuth != FinishAuthVerifiedWriteReview {
		return false, "", errf(ErrForbidden, "完成授权未开启（默认 human_only）：等待主人定版；开启仅主人管理面")
	}
	if !CanTransition(sp.Status, StatusCompleted) {
		return false, "", errf(ErrIllegalTransition, "%s：状态 %s 不可完成（终态不可重入；待配置空间先配置或放弃）", via, sp.Status)
	}
	if sp.inFlightCount(intents) > 0 {
		return false, "", ErrExecutionInFlight
	}
	if s.hooks == nil {
		return false, "", ErrFinishPolicyUnavailable
	}
	verdict := s.hooks.EvaluateCompletion(spaceView(*cloneSpace(sp), intents))
	if !verdict.Satisfied {
		return false, "", errf(ErrCompletionNotSatisfied, "%s", firstNonEmpty(truncateNote(verdict.Reason), "判据未满足"))
	}
	sp.Used.settleRunning(now)
	sp.Status = StatusCompleted
	sp.FinalOutcome = "done"
	sp.FinalBy = subj.String()
	sp.FinalReason = truncateNote(verdict.Reason)
	return true, "完成定版（经 " + via + "：" + truncateNote(verdict.Reason) + "）", nil
}

// controlArchive 归档：仅终态；无在途；幂等标记（重复归档不重复变更）。
// 归档=列表收纳，不删材料、不关原生线程。
func controlArchive(sp *Space, req ControlRequest, now time.Time) (bool, string, error) {
	if !sp.Status.Terminal() {
		return false, "", errf(ErrIllegalTransition, "archive：仅已完成/已放弃空间可归档（现 %s）", sp.Status)
	}
	if sp.Archived {
		return false, "", nil // 幂等命中：已归档
	}
	sp.Archived = true
	t := now
	sp.ArchivedAt = &t
	return true, "归档（列表收纳；材料与原生线程保留）", nil
}

// DispatchRequest 派发受理请求（模型层；执行接线 W04-02）。
type DispatchRequest struct {
	RequestID       string `json:"request_id"`
	Member          string `json:"member"`
	SessionID       string `json:"session_id,omitempty"` // 校验与成员绑定一致
	TurnSeq         int64  `json:"turn_seq"`
	Term            uint64 `json:"term"`              // 调用方声明的当前任期（旧任期写入拒绝）
	TaskID          string `json:"task_id,omitempty"` // 执行侧已接单时在位
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note,omitempty"`
}

// RecordDispatch 指名派发受理（RFC §1.5/§1.6）：授权（驱动者/主人-manual）→
// 运行态 → 成员在位且已绑线程 → 预算三计中 dispatch_round/墙钟事务内复核扣减 →
// 意图持久落账（outbox：先于执行）→ 版本递增。同 request_id 重放命中回执原样
// 返回，不重复扣减（N02）。幂等指纹含目标空间维度（payload 包装 spaceID）：
// 同 ID 跨目标空间即 request_conflict（终审 M1）。
func (s *Store) RecordDispatch(spaceID string, req DispatchRequest, subj Subject) (DispatchIntent, SpaceView, bool, error) {
	now := s.now()
	payload := struct {
		SpaceID string          `json:"space_id"`
		Req     DispatchRequest `json:"req"`
	}{spaceID, req}
	raw, dup, err := s.apply(req.RequestID, "dispatch", payload, func(next *state) (any, error) {
		sp, ok := next.Spaces[spaceID]
		if !ok {
			return nil, ErrNotFound
		}
		if req.ExpectedVersion != sp.Version {
			return nil, errf(ErrVersionConflict, "预期版本 %d ≠ 现版本 %d", req.ExpectedVersion, sp.Version)
		}
		if err := Authorize(ActDispatch, sp, subj, req.Term, now); err != nil {
			return nil, err
		}
		if sp.Status != StatusRunning {
			return nil, errf(ErrIllegalTransition, "dispatch：仅运行态可派发（现 %s；先显式启动）", sp.Status)
		}
		idx := sp.memberOf(req.Member)
		if idx < 0 {
			return nil, errf(ErrMemberNotFound, "成员 %q 不在空间内", req.Member)
		}
		if sp.Members[idx].SessionID == "" {
			return nil, errf(ErrInvalid, "成员 %s 未绑定原生会话（先 configure_members 绑定线程）", req.Member)
		}
		if req.SessionID != "" && req.SessionID != sp.Members[idx].SessionID {
			return nil, errf(ErrInvalid, "session_id %q 与成员绑定 %q 不一致（一成员一原生线程）", req.SessionID, sp.Members[idx].SessionID)
		}
		// 预算事务内复核（决策窗口内变化仍拦下，N08）：dispatch_round 与墙钟。
		if sp.Budget.MaxDispatchRounds > 0 && sp.Used.DispatchRounds >= sp.Budget.MaxDispatchRounds {
			return nil, errf(ErrBudgetExhausted, "dispatch_round 已达上限 %d", sp.Budget.MaxDispatchRounds)
		}
		if sp.Budget.MaxWallSeconds > 0 && sp.Used.wallSecondsNow(now) >= int64(sp.Budget.MaxWallSeconds) {
			return nil, errf(ErrBudgetExhausted, "运行墙钟已达上限 %ds", sp.Budget.MaxWallSeconds)
		}
		sp.Version++
		sp.Used.DispatchRounds++
		it := DispatchIntent{
			RequestID:  req.RequestID,
			SpaceID:    sp.ID,
			Member:     req.Member,
			SessionID:  sp.Members[idx].SessionID,
			TurnSeq:    req.TurnSeq,
			TaskID:     req.TaskID,
			Term:       sp.Leader.Term,
			VersionRef: sp.Version,
			Status:     IntentAccepted,
			Note:       truncateNote(req.Note),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if req.TaskID == "" {
			it.Status = IntentPending // outbox 态：意图已持久、执行侧未接单
		}
		next.Intents[sp.ID] = append(next.Intents[sp.ID], it)
		sp.appendEvent(now, "dispatch", subj.String(), fmt.Sprintf("指名 %s（意图 %s、任期 %d）", req.Member, string(it.Status), it.Term))
		return struct {
			Intent DispatchIntent `json:"intent"`
			Space  SpaceView      `json:"space"`
		}{it, spaceView(*cloneSpace(sp), next.Intents[sp.ID])}, nil
	})
	if err != nil {
		return DispatchIntent{}, SpaceView{}, false, err
	}
	var out struct {
		Intent DispatchIntent `json:"intent"`
		Space  SpaceView      `json:"space"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Intent, out.Space, dup, nil
}

// MessageRecord 消息预算扣账结果。
type MessageRecord struct {
	SpaceID string     `json:"space_id"`
	Used    BudgetUsed `json:"used"`
}

// MessageRequest 消息预算扣账请求。
type MessageRequest struct {
	RequestID       string `json:"request_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note,omitempty"`
}

// RecordMessage 普通发言预算扣账（RFC §1.5）：消息计数独立于执行轮次；落账本身
// 不触发任何执行（N01——消息与游标不唤醒成员）。终态空间拒收（时间线只读）。
// 主体做非空/取值基校验（终审 S3）；「谁能向空间发言」的细粒度授权归 W04-02
// messages 端点门（本模型层不承担），扣账只对已过门的有效主体记账。幂等指纹含
// 目标空间维度（终审 M1，同 Control/RecordDispatch）。
func (s *Store) RecordMessage(spaceID string, req MessageRequest, subj Subject) (MessageRecord, bool, error) {
	if !subj.Valid() {
		return MessageRecord{}, false, errf(ErrInvalid, "主体缺失（name/class 必填且取值合法；消息授权门归 W04-02 messages 端点）")
	}
	payload := struct {
		SpaceID string         `json:"space_id"`
		Req     MessageRequest `json:"req"`
	}{spaceID, req}
	raw, dup, err := s.apply(req.RequestID, "message", payload, func(next *state) (any, error) {
		sp, ok := next.Spaces[spaceID]
		if !ok {
			return nil, ErrNotFound
		}
		if req.ExpectedVersion != sp.Version {
			return nil, errf(ErrVersionConflict, "预期版本 %d ≠ 现版本 %d", req.ExpectedVersion, sp.Version)
		}
		if sp.Status.Terminal() {
			return nil, errf(ErrIllegalTransition, "message：终态空间拒收消息（时间线只读）")
		}
		if sp.Budget.MaxMessages > 0 && sp.Used.Messages >= sp.Budget.MaxMessages {
			return nil, errf(ErrBudgetExhausted, "message_count 已达上限 %d", sp.Budget.MaxMessages)
		}
		sp.Version++
		sp.Used.Messages++
		return MessageRecord{SpaceID: sp.ID, Used: sp.Used}, nil
	})
	if err != nil {
		return MessageRecord{}, false, err
	}
	var out MessageRecord
	_ = json.Unmarshal(raw, &out)
	return out, dup, nil
}

// ConvergeIntent 意图收敛（执行事实落账；W04-02 接线）：仅 owner/system 主体
// （执行侧记账）；在途意图按 outcome 收束——结果落账不再派（不触发任何下一轮，
// N04）。幂等：已按同 outcome 收敛的意图原样返回；异 outcome 冲突。
func (s *Store) ConvergeIntent(spaceID, requestID, outcome, taskID, note string, subj Subject) (DispatchIntent, error) {
	if subj.Class != ClassOwner && subj.Class != ClassSystem {
		return DispatchIntent{}, errf(ErrForbidden, "意图收敛仅执行侧主体（owner/system）")
	}
	switch IntentStatus(outcome) {
	case IntentConverged, IntentCancelled:
	default:
		return DispatchIntent{}, errf(ErrInvalid, "outcome 须为 converged|cancelled，得 %q", outcome)
	}
	now := s.now()
	var result DispatchIntent
	_, _, err := s.apply("converge:"+requestID+":"+outcome, "converge_intent",
		struct {
			SpaceID   string `json:"space_id"`
			RequestID string `json:"request_id"`
			Outcome   string `json:"outcome"`
		}{spaceID, requestID, outcome}, func(next *state) (any, error) {
			if _, ok := next.Spaces[spaceID]; !ok {
				return nil, ErrNotFound
			}
			list := next.Intents[spaceID]
			for i := range list {
				if list[i].RequestID != requestID {
					continue
				}
				it := &list[i]
				if !it.Status.InFlight() {
					if IntentStatus(outcome) == it.Status {
						result = *it
						return result, nil // 幂等命中：已按同 outcome 收敛
					}
					return nil, errf(ErrIllegalTransition, "意图 %s 已按 %s 收敛，不可改写为 %s", requestID, it.Status, outcome)
				}
				it.Status = IntentStatus(outcome)
				if taskID != "" {
					it.TaskID = taskID
				}
				if note != "" {
					it.Note = truncateNote(note)
				}
				it.UpdatedAt = now
				result = *it
				if sp, ok := next.Spaces[spaceID]; ok {
					sp.appendEvent(now, "intent_converged", subj.String(),
						fmt.Sprintf("意图 %s（%s→%s）执行事实落账（不触发下一轮）", requestID, string(it.Status), outcome))
				}
				return result, nil
			}
			return nil, errf(ErrInvalid, "意图 %s 不在空间 %s", requestID, spaceID)
		})
	if err != nil {
		return DispatchIntent{}, err
	}
	return result, nil
}

// derivedHex 由请求 ID 派生定长 hex（确定性空间 ID；sha256 前 16 字节）。
func derivedHex(s string) string {
	sum := sha256.Sum256([]byte("space-id|" + s))
	return hex.EncodeToString(sum[:16])
}

// firstNonEmpty 首个非空串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
