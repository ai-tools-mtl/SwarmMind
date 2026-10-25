// Work 工作单 W2（P5 / RFC-work-resume 五 W2 行）：原子接管 + 绑定提交 + 多次
// 尝试 + 终态回写 + 恢复扫描。建单与只读查询见 work.go（W1）。
//
// 绑定提交（RFC 二.2 步骤 2）：Prime 经 swarmmind_submit 携 work_ref/step_seq/
// prime_term 三参派发，hub 在 Submit 的落库事务内原子完成「任务落库 + 尝试登记
// + 步骤回写 + work 状态推进 + attempt_recorded 事件」——绑定校验任一失败即
// mutate 返回错误，事务整体拒绝、任务零发布（「绑定失败不发布」）。指纹规范形
// 取请求可声明的 work_ref+step_seq（attempt_ref 为 hub 分配号不进指纹，重试
// 回放短路于 idem 查重——先于绑定——尝试唯一性由构造保证，TESTPLAN-W2 头部）。
//
// 接管（RFC 二.2 步骤 5）：CAS 单事务——expected_term 与落库 PrimeTerm 不符即
// 拒绝（同刻仅一方接管成功）；expected==current==new 为重放幂等（零写入零事件）；
// 成功则改写任期 + takeover 事件入账。旧任期编排写入（绑定派发）拒绝；任务
// 终态回写无任期校验（执行事实照收）。
//
// 终态回写（RFC 二.2 步骤 3）：任务首落终态的同一事务内回写 attempt.outcome +
// step.status + work.status + step_updated 事件（result/cancel/sweep 三路径共
// 用 writebackWorkTerminal）；「任务已终态而尝试未回填」的历史/中断窗口由启动
// 恢复扫描 recoverWorks 补齐（沿 ControlBatch recoverBatches 模板，正常路径
// 窗口为零，扫描为防御纵深）。
package core

import (
	"errors"
	"fmt"
	"log"
	"sort" // MUTATION-D
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// Work 事件类别（WorkEvent.Kind，W2 起新增；RFC 二.1 命名集）。
const (
	// WorkEventAttemptRecorded 步骤派发、尝试登记（绑定事务内落账）。
	WorkEventAttemptRecorded = "attempt_recorded"
	// WorkEventStepUpdated 步骤状态回写（任务终态同事务/恢复扫描补记）。
	WorkEventStepUpdated = "step_updated"
	// WorkEventTakeover 任期接管（CAS 成功事务内落账；重放幂等不记账）。
	WorkEventTakeover = "takeover"
)

// W2 工作单错误（errors.Is 判定；HTTP 映射见 api writeSubmitErr/workTakeover）。
var (
	// ErrWorkNotFound 工作单不存在（HTTP 404 work_not_found）。
	ErrWorkNotFound = errors.New("core: 工作单不存在")
	// ErrWorkClosed 工作单已终态 done/abandoned，不可再接管或派发（HTTP 409
	// work_closed；W2 无产生终态的路径，校验为 W3 防御纵深）。
	ErrWorkClosed = errors.New("core: 工作单已终态")
	// ErrWorkTermConflict 任期不符：接管 CAS 失败（同刻另一方已接管）或旧任期
	// 编排写入被拒（HTTP 409 work_term_conflict）。
	ErrWorkTermConflict = errors.New("core: 工作单任期不符")
	// ErrWorkNoTerm 工作单未建立任期（建单未设 prime_term 且未接管过），派发
	// 前须先接管（HTTP 409 work_no_term）。
	ErrWorkNoTerm = errors.New("core: 工作单尚未建立任期")
	// ErrWorkStepLocked 步骤已 done/skipped（计划定死，完成步骤不再派，RFC
	// 四.5 不做计划编辑；HTTP 409 work_step_locked）。
	ErrWorkStepLocked = errors.New("core: 步骤已完成或已跳过")
)

// WorkTakeoverRequest 接管请求（RFC 二.2 步骤 5）：expected_term 为调用方读档
// 时看到的任期（代校验，乐观锁）；new_term 为接管方新任期（必填非空）。
type WorkTakeoverRequest struct {
	// WorkID 工作单 ID。
	WorkID string
	// ExpectedTerm 期望的当前任期（读档值；工作单从未设任期为空串）。
	ExpectedTerm string
	// NewTerm 接管方任期声明（必填）。
	NewTerm string
}

// TakeoverWork 原子接管：单事务 CAS——expected_term ≠ 落库 PrimeTerm 即拒绝
//（同刻两接管者仅一方成功，后至者 conflict）；expected==current==new 为相同
// 操作重放，返回 dup=true、零写入零事件（RFC「相同操作重放不新增尝试」）；
// 成功则改写 PrimeTerm 并落 takeover 事件。终态单（done/abandoned）拒绝。
//
// actor 服务端验证的鉴权主体（REST 层取得，事件以独立字段落账，与自报任期
// 分开存储——RFC 六.2；同 CreateWork 口径）。传空则事件无主体字段（仅限直连
// core 的内部调用方；REST 入口恒非空）。
func (c *Core) TakeoverWork(req WorkTakeoverRequest, actor string) (*WorkView, bool, error) {
	if req.WorkID == "" {
		return nil, false, ErrInvalid
	}
	newTerm := strings.TrimSpace(req.NewTerm)
	if newTerm == "" {
		return nil, false, fmt.Errorf("%w: new_term 必填（接管方任期声明）", ErrInvalid)
	}
	var (
		dup bool
		wv  *WorkView
	)
	err := c.st.Update(func(st *store.State) error {
		w, ok := st.Works[req.WorkID]
		if !ok {
			return ErrWorkNotFound
		}
		if w.Status == store.WorkStatusDone || w.Status == store.WorkStatusAbandoned {
			return ErrWorkClosed
		}
		if req.ExpectedTerm != w.PrimeTerm {
			// 原请求重放回执（审查#5）：响应丢失的相同接管请求原样重放——任期已
			// 是 new_term 且账本最近一条接管事件与之同源（by=new_term 且主体一致；
			// 主体不一致=其他接管者的写入，按冲突拒绝不误认 dup）→ 返回成功回执
			//（dup=true 返现状，零写入零事件）。
			if w.PrimeTerm == newTerm && takeoverReceiptMatch(&w, newTerm, actor) {
				dup = true
				wv = workViewOf(w, 0)
				return nil
			}
			return fmt.Errorf("%w: 期望任期 %q ≠ 当前任期 %q（同刻仅一方接管成功）", ErrWorkTermConflict, req.ExpectedTerm, w.PrimeTerm)
		}
		if newTerm == w.PrimeTerm {
			// 相同操作重放：任期已就位，幂等返回现状（零写入零事件）
			dup = true
			wv = workViewOf(w, 0)
			return nil
		}
		// 任期标签复用拒绝（审查#9，ABA）：标签为调用方自选字符串，服务端无单调
		// 代次——旧标签重新接管成功会重新放行更旧的编排写入，故历史使用过的标签
		// 不得复用（接管方须换新标签）。重放（new==current）不受此限。
		for _, used := range w.TermsUsed {
			if used == newTerm {
				return fmt.Errorf("%w: 任期标签 %q 已被历史任期使用过（接管须换新标签，防旧任期 ABA 重新放行）", ErrWorkTermConflict, newTerm)
			}
		}
		prev := w.PrimeTerm
		w.PrimeTerm = newTerm
		w.TermsUsed = append(w.TermsUsed, newTerm)
		appendWorkEvent(&w, c.now(), WorkEventTakeover, newTerm, actor,
			fmt.Sprintf("接管：任期 %s → %s（接管时 %d 步 %d 次尝试）", termLabel(prev), newTerm, len(w.Steps), len(w.Attempts)))
		st.Works[w.ID] = w
		wv = workViewOf(w, 0)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return wv, dup, nil
}

// termLabel 事件正文用任期显示（未设任期显式标注，不留空歧义）。
func termLabel(t string) string {
	if t == "" {
		return "（无任期）"
	}
	return t
}

// takeoverReceiptMatch 接管成功回执判定（TakeoverWork 原请求重放用）：倒序取
// 账本最近一条 takeover 事件，要求 by=声明新任期且主体同源。actor 非空时主体
// 必须相等（他人以相同参数的接管写入按冲突拒绝——同刻仅一方接管语义不因重放
// 回执放宽）；actor 为空（直连 core 内部调用方）只按 by 判定——事件滚动裁剪后
// 找不到接管事件时按不匹配处理（安全侧落 conflict）。账本窗口内若存在更早的
// 同名标签接管（ABA 复用），主体校验兜底；主体皆空的内部调用不受回执承诺。
func takeoverReceiptMatch(w *store.Work, newTerm, actor string) bool {
	for i := len(w.Events) - 1; i >= 0; i-- {
		ev := &w.Events[i]
		if ev.Kind != WorkEventTakeover {
			continue
		}
		return ev.By == newTerm && (actor == "" || ev.Principal == "" || ev.Principal == actor)
	}
	return false
}

// WorkBindInfo 绑定派发结果（SubmitAck.Work；HTTP 返回体展开为 work 对象）。
type WorkBindInfo struct {
	// WorkID 工作单 ID。
	WorkID string `json:"work_id"`
	// StepSeq 绑定步骤序号。
	StepSeq int64 `json:"step_seq"`
	// AttemptSeq 尝试序号（Work 内单调）。
	AttemptSeq int64 `json:"attempt_seq"`
	// AttemptRef 尝试引用（<work_id>#a<seq>，管理面直引键）。
	AttemptRef string `json:"attempt_ref"`
	// Outcome 尝试结局：绑定即 submitted；尝试去重命中时为该尝试现终局。
	Outcome string `json:"outcome"`
	// TaskGone 尝试去重命中且原任务已过保留期清理（不重复执行；task_id 在
	// SubmitAck.TaskID 原样返回）。
	TaskGone bool `json:"task_gone,omitempty"`
}

// attemptRef 尝试引用格式（hub 分配号；work_id 全局唯一故引用全局唯一）。
func attemptRef(workID string, seq int64) string {
	return fmt.Sprintf("%s#a%d", workID, seq)
}

// workDupInfo 活任务 dup 响应的绑定回执补全（审查#10）：同键重放命中原任务时
// 响应带原尝试定位与现终局（TaskGone=false），与首次提交/去重证据命中同形——
// 调用方免二次查询工作单即可定位尝试。未绑定请求返回 nil（响应形态不变）。
func workDupInfo(st *store.State, req protocol.TaskSubmitRequest, taskID string) *WorkBindInfo {
	if req.WorkRef == "" {
		return nil
	}
	w, ok := st.Works[req.WorkRef]
	if !ok {
		return nil
	}
	for i := range w.Attempts {
		if w.Attempts[i].TaskID != taskID {
			continue
		}
		a := &w.Attempts[i]
		return &WorkBindInfo{
			WorkID: w.ID, StepSeq: a.StepSeq, AttemptSeq: a.Seq,
			AttemptRef: attemptRef(w.ID, a.Seq), Outcome: a.Outcome,
		}
	}
	return nil
}

// lookupAttemptEvidence 尝试去重证据只读查重（审查#1/#2：与绑定写入拆分的独立
// 读操作，预检与事务重检共用，零写入零事件；先于依赖准入与容量检查——同键重放
// 是对既有尝试的回执补拉而非新编排写入，任务 24h 清理后任务级指纹查重失效，本
// 证据是「同键重放不重新执行」的持久判定，RFC 不推迟项）。全局扫描同 idem 键的
// 已登记尝试，按绑定事务内落盘的提交内容指纹判定（与 mailbox.IdemCheck 同构）：
//   - 全量指纹合（目标/输入/提交主体/依赖/绑定/群引用全同）→ 命中：返回原尝试
//     回执（task_gone 按任务现存判定）；
//   - 仅群引用不同 → 未命中（群引用是路由装饰，按新提交处理，同任务级换群口径）；
//   - 其余不合（换内容/换 work/换步骤/换提交主体）→ ErrConflict——提交主体入
//     指纹即身份边界，知 Work+键+步骤的他方不得冒领原 task_id。
//
// 未命中调用方继续派发准入；命中时 TaskGone=false 表示原任务仍在（活任务重放
// 已被任务级 idem 查重短路，此处命中为防御形态）。
func lookupAttemptEvidence(st *store.State, req protocol.TaskSubmitRequest) (info *WorkBindInfo, taskID string, hit bool, err error) {
	if req.IdemKey == "" {
		return nil, "", false, nil
	}
	wantFull := mailbox.ContentHash(req)
	wantBase := mailbox.BaseContentHash(req)
	for _, w := range st.Works {
		for i := range w.Attempts {
			a := &w.Attempts[i]
			if a.IdemKey != req.IdemKey || a.TaskID == "" || a.ContentHash == "" {
				continue
			}
			if a.ContentHash == wantFull {
				_, live := st.Tasks[a.TaskID]
				return &WorkBindInfo{
					WorkID: w.ID, StepSeq: a.StepSeq, AttemptSeq: a.Seq,
					AttemptRef: attemptRef(w.ID, a.Seq), Outcome: a.Outcome, TaskGone: !live,
				}, a.TaskID, true, nil
			}
			if a.BaseContentHash != wantBase {
				return nil, "", false, fmt.Errorf("%w: 幂等键 %q 已绑定工作单 %s 步骤 #%d 尝试 #%d 的不同内容（不得改绑或换内容重放）", mailbox.ErrConflict, req.IdemKey, w.ID, a.StepSeq, a.Seq)
			}
			// 仅群引用不同：继续扫描（同任务级换群重试口径，按新提交处理）
		}
	}
	return nil, "", false, nil
}

// bindWorkSubmit 绑定派发（Submit 落库事务内调用）：分配新尝试 + 回写步骤/work
// 状态 + attempt_recorded 事件 + 任务体盖章（WorkRef/WorkStep/AttemptRef），
// 全部在调用方的同一事务内。尝试去重证据查重已拆分为独立的只读操作
//（lookupAttemptEvidence，审查#1/#2），由提交链先于本函数执行——事务内重检在
// 依赖准入/容量检查之前、预检在 idem 查重之后（回执补拉不因依赖被清理或预算
// 满挡住），本函数只做派发准入与原子绑定写入。
//
// 事件主体（AGENTS「审计/事件主体禁止采信请求自报」）：principal 取提交链
// 服务端已验证身份（审查#4：由调用方独立传入——REST 提交=鉴权中间件解析主体
//（admin 会话/PAT 令牌名），node 帧提交=会话 node_id，直连 core 内部调用为空；
// 不取 task.SubmittedBy——REST 侧该值固定 admin，会丢失 PAT 主体）；by 为自报
// 任期 prime_term（归属标签非身份证明，RFC 六.2）。
//
// 准入规则（hub 不抢编排权，RFC 二.3——只校验事实边界，不校验步骤 DAG 顺序）：
// 工作单存在且未终态；已建立任期且与声明严格相等（旧任期写入拒绝）；步骤存在；
// 步骤未 done/skipped（计划定死）。失败/needs_review 步骤再派放行（新尝试，
// 决策在 Prime）。状态推进：planning→executing；blocked 时对携带阻断的步骤
// 本身（failed/needs_review）再派 → executing（阻断被决策处理）。同键重放不会
// 到达本函数（回执短路于提交链证据查重，先于准入）。
func (c *Core) bindWorkSubmit(st *store.State, task *protocol.Task, req protocol.TaskSubmitRequest, actor string) (*WorkBindInfo, error) {
	term := strings.TrimSpace(req.PrimeTerm)
	if req.StepSeq < 1 || term == "" {
		return nil, fmt.Errorf("%w: 绑定派发须同时给出 step_seq（≥1）与 prime_term（任期声明）", ErrInvalid)
	}
	w, ok := st.Works[req.WorkRef]
	if !ok {
		return nil, ErrWorkNotFound
	}
	if w.Status == store.WorkStatusDone || w.Status == store.WorkStatusAbandoned {
		return nil, ErrWorkClosed
	}
	if w.PrimeTerm == "" {
		return nil, fmt.Errorf("%w: 建单未设任期且未接管过，先 swarmmind_work_takeover 建立任期", ErrWorkNoTerm)
	}
	if term != w.PrimeTerm {
		return nil, fmt.Errorf("%w: 声明任期 %q ≠ 当前任期 %q（旧任期编排写入拒绝；任务结果事实回流不受此限）", ErrWorkTermConflict, term, w.PrimeTerm)
	}
	stepIdx := -1
	for i := range w.Steps {
		if w.Steps[i].Seq == req.StepSeq {
			stepIdx = i
			break
		}
	}
	if stepIdx < 0 {
		return nil, fmt.Errorf("%w: 步骤 #%d 不存在（1..%d）", ErrInvalid, req.StepSeq, len(w.Steps))
	}
	if w.Steps[stepIdx].Status == store.StepStatusDone || w.Steps[stepIdx].Status == store.StepStatusSkipped {
		return nil, fmt.Errorf("%w: 步骤 #%d 现状 %s（计划建单定死，完成步骤不再派）", ErrWorkStepLocked, req.StepSeq, w.Steps[stepIdx].Status)
	}
	// 分配新尝试（Work 内 seq 单调不回退）+ 回写 + 事件（同事务原子）
	seq := int64(1)
	if len(w.Attempts) > 0 {
		seq = w.Attempts[len(w.Attempts)-1].Seq + 1
	}
	prevStepStatus := w.Steps[stepIdx].Status
	w.Steps[stepIdx].Status = store.StepStatusSubmitted
	if w.Status == store.WorkStatusPlanning ||
		(w.Status == store.WorkStatusBlocked && workStepBlockedHere(prevStepStatus)) {
		w.Status = store.WorkStatusExecuting
	}
	w.Attempts = append(w.Attempts, store.WorkAttempt{
		Seq:             seq,
		StepSeq:         req.StepSeq,
		TaskID:          task.ID,
		IdemKey:         req.IdemKey,
		ContentHash:     mailbox.ContentHash(req),
		BaseContentHash: mailbox.BaseContentHash(req),
		Outcome:         store.AttemptOutcomeSubmitted,
	})
	task.WorkRef = w.ID
	task.WorkStep = req.StepSeq
	task.AttemptRef = attemptRef(w.ID, seq)
	appendWorkEvent(&w, c.now(), WorkEventAttemptRecorded, term, actor,
		fmt.Sprintf("步骤 #%d 派发：尝试 #%d → 任务 %s（目标 %s/%s）", req.StepSeq, seq, task.ID, task.TargetNode, task.TargetAgent))
	st.Works[w.ID] = w
	return &WorkBindInfo{
		WorkID: w.ID, StepSeq: req.StepSeq, AttemptSeq: seq,
		AttemptRef: attemptRef(w.ID, seq), Outcome: store.AttemptOutcomeSubmitted,
	}, nil
}

// workStepBlockedHere 阻断中的工作单对该步骤再派是否视为阻断被处理（该步骤
// 自身处于 failed/needs_review——阻断的来源步骤被重新决策派发）。
func workStepBlockedHere(prevStepStatus string) bool {
	return prevStepStatus == store.StepStatusFailed || prevStepStatus == store.StepStatusNeedsReview
}

// writebackWorkTerminal 任务首落终态的同一事务内回写工作单事实（result/cancel/
// sweep 三终态路径共用）：attempt.outcome + step.status + work.status（非 done
// 终局即 blocked）+ step_updated 事件。t.WorkRef 为空即未绑定 no-op；找不到
// 对应尝试为防御 no-op（原子绑定先于落库，不可达）。并发尝试同一步骤时，步骤
// 状态以最后落盘的终态事实为准（单写事务串行化，last-writer-wins）；各尝试
// outcome 随各自任务回写互不覆盖。系统回写事件 by=hub、principal 空（服务端
// 自身动作，无外部请求主体）。
func writebackWorkTerminal(st *store.State, t *protocol.Task, now time.Time) {
	if t.WorkRef == "" {
		return
	}
	w, ok := st.Works[t.WorkRef]
	if !ok {
		return
	}
	for i := range w.Attempts {
		if w.Attempts[i].TaskID != t.ID {
			continue
		}
		a := &w.Attempts[i]
		outcome, stepStatus := terminalWorkMapping(t.Status)
		a.Outcome = outcome
		for j := range w.Steps {
			if w.Steps[j].Seq == a.StepSeq {
				w.Steps[j].Status = stepStatus
				break
			}
		}
		if t.Status != protocol.StatusCompleted {
			w.Status = store.WorkStatusBlocked // 存在阻断（failed/needs_review/cancelled），决策在 Prime
		}
		appendWorkEvent(&w, now, WorkEventStepUpdated, "hub", "",
			fmt.Sprintf("任务 %s 终态 %s：步骤 #%d → %s（尝试 #%d）", t.ID, t.Status, a.StepSeq, stepStatus, a.Seq))
		st.Works[w.ID] = w
		return
	}
}

// terminalWorkMapping 任务终态 → (attempt.outcome, step.status)（映射表见
// TESTPLAN-W2 头部；needs_review/cancelled 不折叠为 failed——SPEC 4.8 与 Work
// 独立记忆可解释性，枚举扩展理由见 store 常量注释）。
func terminalWorkMapping(s protocol.TaskStatus) (outcome, stepStatus string) {
	switch s {
	case protocol.StatusCompleted:
		return store.AttemptOutcomeDone, store.StepStatusDone
	case protocol.StatusNeedsReview:
		return store.AttemptOutcomeNeedsReview, store.StepStatusNeedsReview
	case protocol.StatusCancelled:
		// 取消是事实非失败：尝试记 cancelled；步骤执行未成功按 failed 记（可再派）
		return store.AttemptOutcomeCancelled, store.StepStatusFailed
	default:
		return store.AttemptOutcomeFailed, store.StepStatusFailed
	}
}

// recoverWorks 启动恢复（终态回写窗口收尾，沿 recoverBatches 模板）：正常路径
// 回写与任务终态同事务（原子，无窗口），本扫描为防御纵深——补齐「任务已终态
// 而尝试仍 submitted」的历史/中断窗口（旧版本数据、快照手工修补等）。任务未
// 终态的 submitted 尝试为合法在途，跳过；任务已清理的 submitted 尝试跳过
//（清理必以终态+保留期为前提，回写先于清理，不可达时防御不臆补）。
//
// 覆盖规则（审查#6）：同 Work 多个待回填尝试按**任务终态落盘时间**排序依次
// 补写（同刻按尝试序），步骤/工作单状态以最后补写的事实为准——与活路径单写
// 事务串行化的 last-writer-wins 同序，登记序补写会以旧尝试事实覆盖新终局
//（旧尝试 failed 把已 done 步骤改回 failed）。
func (c *Core) recoverWorks() {
	err := c.st.Update(func(st *store.State) error {
		for id, w := range st.Works {
			type backfill struct {
				attemptIdx int
				task       protocol.Task
			}
			var pend []backfill
			for i := range w.Attempts {
				a := &w.Attempts[i]
				if a.Outcome != store.AttemptOutcomeSubmitted || a.TaskID == "" {
					continue
				}
				t, ok := st.Tasks[a.TaskID]
				if !ok || !t.Status.Terminal() {
					continue
				}
				pend = append(pend, backfill{attemptIdx: i, task: t})
			}
			if len(pend) == 0 {
				continue
			}
			sort.Slice(pend, func(x, y int) bool {
				tx, ty := pend[x].task, pend[y].task
				if !tx.UpdatedAt.Equal(ty.UpdatedAt) {
					return tx.UpdatedAt.Before(ty.UpdatedAt)
				}
				return w.Attempts[pend[x].attemptIdx].Seq < w.Attempts[pend[y].attemptIdx].Seq
			})
			for _, bf := range pend {
				a := &w.Attempts[bf.attemptIdx]
				outcome, stepStatus := terminalWorkMapping(bf.task.Status)
				a.Outcome = outcome
				for j := range w.Steps {
					if w.Steps[j].Seq == a.StepSeq {
						w.Steps[j].Status = stepStatus
						break
					}
				}
				if bf.task.Status != protocol.StatusCompleted {
					w.Status = store.WorkStatusBlocked
				}
				appendWorkEvent(&w, c.now(), WorkEventStepUpdated, "hub", "",
					fmt.Sprintf("恢复扫描补记：任务 %s 已终态 %s 而尝试未回填（中断窗口），按任务现态补记：步骤 #%d → %s（尝试 #%d）",
						bf.task.ID, bf.task.Status, a.StepSeq, stepStatus, a.Seq))
			}
			st.Works[id] = w
		}
		return nil
	})
	if err != nil {
		log.Printf("[hub] 工作单恢复扫描失败（下轮重启重试）: %v", err)
	}
}
