// 任务依赖门控与调度（P4.3 第一子批 / FILE-COLLAB L2，SPEC 4.12）：
//
//   - 准入校验（validateDepends，提交校验链内预检+事务重检）：依赖以 idem_key
//     寻址（跨重跑稳定），拒绝未知 idem（不存在或已随终态清理）、自引用、重复
//     依赖、超深度（上溯最长链 >4）、超级联（上溯去重上游总数 >8）、依赖环
//     （边只指向已存在任务天然无环；防御 idem 跨清理期复用构成的环）。
//   - 门控（blocked 不进状态机）：依赖未满足的任务保持 submitted，BlockedReason
//     标注（blocked_by:<idem> / upstream_failed:<idem>），七态/收敛表/终态规则
//     零改动；blocked 任务留信箱占配额（信箱满拒新提交的既有语义不变）。
//   - 调度：上游到终态的事务处（result/cancel/sweep 置终态）同步复评下游——
//     依赖全 completed → 解除阻塞进入正常投递；上游 failed/needs_review/
//     cancelled → 下游保持 blocked 更新标注。Sweep 每秒兜底扫描防事件丢失。
//   - superseded：曾因 upstream_failed:<idem> 被挡的下游任务，遇同 idem 新版本
//     completed → 记 SupersededBy（不自动放行，人工决定是否重派，旧任务审计保留）。
//
// 本文件全部为事务内纯函数：操作调用方给定的 *store.State（事务副本），自身
// 不加锁、不发帧、不做 IO；帧触发与 noteTask 记账由调用方在事务发布后进行。
package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// idemState 依赖 idem 在当前快照中的解析态（evalDeps 单依赖结论）。
type idemState string

const (
	// idemSatisfied 存在 completed 任务（任一版本满足；返回满足任务 ID）。
	idemSatisfied idemState = "satisfied"
	// idemPending 存在非终态任务（等待中），或 idem 已无任务（终态清理后消失
	// ——保持等待，7d expire 兜底）。
	idemPending idemState = "pending"
	// idemFailed 仅有 failed/needs_review/cancelled 终态任务（上游失败）。
	idemFailed idemState = "failed"
)

// depEval 依赖集合复评结论（按 DependsOn 声明序取首个，保证标注确定）。
type depEval struct {
	satisfied map[string]string // idem → 满足它的上游任务 ID（新版本）
	pending   string            // 首个等待中的依赖 idem
	failed    string            // 首个失败终态的依赖 idem
}

// allSatisfied 报告全部依赖已满足。
func (ev depEval) allSatisfied() bool { return ev.pending == "" && ev.failed == "" }

// blockedReason 复评结论 → 门控标注（上游失败优先于等待——不可自愈的失败需要
// 人工介入，等待类可自行恢复）；全满足返回空。
func (ev depEval) blockedReason() string {
	switch {
	case ev.failed != "":
		return protocol.UpstreamFailedReason(ev.failed)
	case ev.pending != "":
		return protocol.BlockedByReason(ev.pending)
	}
	return ""
}

// resolveIDem 解析单个依赖 idem：扫描快照中携带该 idem 的任务（正常至多一个；
// group_ref 装饰边可致同键双活——宽松口径：任一 completed 即视为满足）。
// 返回解析态与关联任务 ID：satisfied 取最早 completed（首个满足版本）、failed
// 取最新终态、pending 取最新非终态；无任务 → pending（ vanished 保持等待）。
func resolveIDem(st *store.State, idem string) (idemState, string) {
	var (
		satID   string // 最早 completed
		satAt   string // (CreatedAt, ID) 比较锚
		failID  string
		failKey string
		liveID  string
		liveKey string
		found   bool
	)
	for _, t := range st.Tasks {
		if t.IdemKey != idem {
			continue
		}
		found = true
		key := t.CreatedAt.UTC().Format("20060102150405.000000000") + "|" + t.ID
		switch {
		case t.Status == protocol.StatusCompleted:
			if satID == "" || key < satAt {
				satID, satAt = t.ID, key
			}
		case !t.Status.Terminal():
			if key > liveKey {
				liveID, liveKey = t.ID, key
			}
		default:
			if key > failKey {
				failID, failKey = t.ID, key
			}
		}
	}
	switch {
	case !found:
		return idemPending, "" // idem 消失（终态清理）：保持等待，7d expire 兜底
	case satID != "":
		return idemSatisfied, satID
	case liveID != "":
		return idemPending, liveID
	default:
		return idemFailed, failID
	}
}

// validateDepends 依赖声明准入校验（SPEC 4.12；提交校验链 idem 查重之后、体积
// 预检之前预检，单写事务内重检——上游在两检之间被清理即事务内拒绝）。返回
// 规范化（TrimSpace）后的依赖列表。逐项规则：
//
//  1. 空项/纯空白项拒绝；
//  2. 重复依赖拒绝；
//  3. 自引用拒绝（ownIdem = req.IdemKey；未携 idem 的提交无从自引用）；
//  4. 未知 idem 拒绝（快照无携带该 idem 的任务）；
//  5. 沿依赖图上溯：深度（最长链边数）> MaxDependsDepth 拒绝；级联（去重上游
//     任务总数）> MaxDependsCascade 拒绝；
//  6. 依赖环拒绝：上游链解析回 ownIdem（idem 跨终态清理期被复用可构造环）。
func validateDepends(st *store.State, req protocol.TaskSubmitRequest) ([]string, error) {
	deps := make([]string, 0, len(req.DependsOn))
	seen := make(map[string]bool, len(req.DependsOn))
	for _, k := range req.DependsOn {
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("%w: 依赖项为空", ErrInvalidDeps)
		}
		if seen[k] {
			return nil, fmt.Errorf("%w: 重复依赖 %q", ErrInvalidDeps, k)
		}
		if req.IdemKey != "" && k == req.IdemKey {
			return nil, fmt.Errorf("%w: 自引用 %q", ErrInvalidDeps, k)
		}
		seen[k] = true
		deps = append(deps, k)
	}
	// 上溯 BFS：frontier 为 idem 层；depth 计边层数、cascade 计去重上游任务数。
	// visited 以 idem 为界（预置 ownIdem——链上任何一层解析回自身 idem 即环）。
	visited := make(map[string]bool)
	if req.IdemKey != "" {
		visited[req.IdemKey] = true
	}
	frontier := append([]string(nil), deps...)
	depth, cascade := 0, 0
	for len(frontier) > 0 {
		depth++
		if depth > protocol.MaxDependsDepth {
			return nil, fmt.Errorf("%w: 依赖深度超过上限 %d", ErrInvalidDeps, protocol.MaxDependsDepth)
		}
		var next []string
		for _, k := range frontier {
			if req.IdemKey != "" && k == req.IdemKey {
				return nil, fmt.Errorf("%w: 依赖环（上游链引用自身 idem %q）", ErrInvalidDeps, k)
			}
			if visited[k] {
				continue
			}
			visited[k] = true
			_, id := resolveIDem(st, k)
			if id == "" {
				return nil, fmt.Errorf("%w: 未知依赖 idem %q（不存在或已清理）", ErrInvalidDeps, k)
			}
			cascade++
			if cascade > protocol.MaxDependsCascade {
				return nil, fmt.Errorf("%w: 依赖级联超过上限 %d", ErrInvalidDeps, protocol.MaxDependsCascade)
			}
			up := st.Tasks[id]
			next = append(next, up.DependsOn...)
		}
		frontier = next
	}
	return deps, nil
}

// evalDeps 复评依赖集合：逐项解析，按声明序取首个 pending/failed 供标注。
func evalDeps(st *store.State, deps []string) depEval {
	ev := depEval{satisfied: make(map[string]string, len(deps))}
	for _, k := range deps {
		state, id := resolveIDem(st, k)
		switch state {
		case idemSatisfied:
			ev.satisfied[k] = id
		case idemFailed:
			if ev.failed == "" {
				ev.failed = k
			}
		default:
			if ev.pending == "" {
				ev.pending = k
			}
		}
	}
	return ev
}

// reevaluateTask 单个下游任务复评（事务内调用；返回是否改动与是否解除阻塞）：
//   - 仅门控 submitted 任务（已投递/终态不回收——解除阻塞是单向的，依赖均为
//     终态不再变化，已放行任务不因上游清理被重新挡回）；
//   - 曾因 upstream_failed:<idem> 被挡且该 idem 现由新版本满足 → 记
//     SupersededBy（superseded 语义：上游重跑不自动解锁，任务保持 blocked，
//     人工决定是否重派；FILE-COLLAB L2）；
//   - 其余按复评结论更新 BlockedReason；由 blocked 转空即解除阻塞。
func reevaluateTask(st *store.State, d *protocol.Task) (changed, unlocked bool) {
	if d.Status != protocol.StatusSubmitted || len(d.DependsOn) == 0 {
		return false, false
	}
	wasBlocked := d.BlockedReason != ""
	ev := evalDeps(st, d.DependsOn)
	if d.SupersededBy == "" && strings.HasPrefix(d.BlockedReason, protocol.BlockedReasonUpstreamFailedPrefix) {
		oldIDem := strings.TrimPrefix(d.BlockedReason, protocol.BlockedReasonUpstreamFailedPrefix)
		if id, ok := ev.satisfied[oldIDem]; ok {
			d.SupersededBy = id
			changed = true
		}
	}
	if d.SupersededBy != "" {
		return changed, false // 已被上游新版本取代：冻结门控（保持 blocked 现状）
	}
	if !wasBlocked {
		return changed, false // 未阻塞任务不复评（放行单向；依赖终态不会回退）
	}
	reason := ev.blockedReason()
	if reason != d.BlockedReason {
		d.BlockedReason = reason
		changed = true
	}
	return changed, changed && d.BlockedReason == ""
}

// depReview 一次下游复评的结果：改动任务（调用方事务发布后 noteTask 记账）与
// 解除阻塞任务的目标节点集（发布后逐节点 dispatch）。
type depReview struct {
	changed  []*protocol.Task
	unlocked map[string]bool
}

// reevaluateDependents 上游到终态后的下游复评（result/cancel/sweep 置终态事务内
// 调用）：对 DependsOn 含 upstreamIDem 的全部任务逐一 reevaluateTask。单事务内
// 上游终态与下游放行原子生效——无「上游已 completed 而下游仍 blocked」的事件
// 丢失窗口（Sweep 兜底仅防御未知路径）。
func reevaluateDependents(st *store.State, upstreamIDem string) *depReview {
	if upstreamIDem == "" {
		return nil
	}
	ids := make([]string, 0, len(st.Tasks))
	for id, t := range st.Tasks {
		if t.IdemKey == upstreamIDem {
			continue // 自引用在准入已拒；防御跳过上游自身
		}
		if !depContains(t.DependsOn, upstreamIDem) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids) // map 遍历序不定，标注与收集顺序确定化
	rv := &depReview{unlocked: make(map[string]bool)}
	for _, id := range ids {
		d := st.Tasks[id]
		changed, unlocked := reevaluateTask(st, &d)
		if !changed {
			continue
		}
		st.Tasks[id] = d
		cp := d
		rv.changed = append(rv.changed, &cp)
		if unlocked {
			rv.unlocked[d.TargetNode] = true
		}
	}
	return rv
}

// depContains 报告 deps 是否含 idem（依赖列表为去重小集合，线性扫描）。
func depContains(deps []string, idem string) bool {
	for _, k := range deps {
		if k == idem {
			return true
		}
	}
	return false
}

// noteDepReview 复评改动任务的事务发布后记账（任务体字节缓存，SPEC 4.6 计量
// 随 BlockedReason/SupersededBy 变化更新）。
func noteDepReview(c *Core, rv *depReview) {
	if rv == nil {
		return
	}
	for _, t := range rv.changed {
		c.noteTask(t)
	}
}

// dispatchDepReview 解除阻塞的下游任务按目标节点进入正常投递（事务发布后调用；
// 离线节点由 dispatch 内部自判，上线补推覆盖）。
func dispatchDepReview(c *Core, rv *depReview) {
	if rv == nil {
		return
	}
	for nodeID := range rv.unlocked {
		c.dispatch(nodeID)
	}
}
