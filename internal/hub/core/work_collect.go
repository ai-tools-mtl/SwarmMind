// Work 工作单 W3（P5 / RFC-work-resume 五 W3 行）：收集/关闭 + 终态保留清理 +
// 读档解释面补全。建单与只读查询见 work.go（W1）；接管/绑定派发/终态回写见
// work_dispatch.go（W2）。
//
// 收集（RFC 二.2 步骤 4）：swarmmind_work_collect 单事务终态闭合——校验通过即
// status 落 done/abandoned、ClosedAt/ExpiresAt 盖章、Report 落库、collect 事件
// 入账。部分报告不误关闭：outcome=done 要求全部步骤 done，存在未完成步骤拒绝
// （hub 不把部分完成静默标成完成）；处置关闭 outcome=abandoned 要求无在途执行
// 即可，未派发（pending）步骤随关闭同事务记为 skipped（跳过=决定不执行——
// needs_review 阻断禁重跑、上游失败未补救时下游 pending 永远到不了终态，放弃
// 剩余步骤是唯一出口，审查②）。收集门=全部步骤终态**且全部尝试收束**（W2 允许
// 同步骤并发尝试，A 在途 B 先完成时步骤呈 done 而执行未收束，审查①）；关闭后
// 迟到的任务结果不回写（writebackWorkTerminal/recoverWorks 终态单保护）。
// collecting 状态 V0 无写入路径（保留枚举，自动推进与 blocked 推导语义冲突，
// 见 TESTPLAN-W3 头部定案）。
//
// 清理（RFC 六.3）：终态 Work 自关闭起保留 30 天（Sweep 第六阶段整单删除并同步
// 清除建单幂等指纹映射），未完成 Work 不自动清理。Work 不延长 Task 与产物保留：
// 任务仍按 24h 清理，Work 尝试记录（outcome/幂等键/内容指纹）是清理后的执行记忆。
package core

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
)

// WorkTerminalRetention 终态 Work 保留期：自 ClosedAt 起 30 天（RFC 六.3），
// 到期 Sweep 整单删除。未完成 Work 无 ExpiresAt 永不清理。
const WorkTerminalRetention = 30 * 24 * time.Hour

// Work 事件类别（WorkEvent.Kind，W3）：collect 收集/处置关闭（终态闭合事务内落账）。
const WorkEventCollect = "collect"

// W3 工作单错误（errors.Is 判定；HTTP 映射见 api handleWorkCollect）。
var (
	// ErrWorkStepsOpen 存在未终态步骤（pending/submitted/running），收集未到
	// 时机（HTTP 409 work_steps_open；错误文案列出步骤号与现状）。
	ErrWorkStepsOpen = errors.New("core: 存在未终态步骤")
	// ErrWorkStepsNotDone 非全部步骤 done 而请求完成关闭——部分完成不得标成
	// 完成（HTTP 409 work_steps_not_done；指路 outcome=abandoned 处置关闭）。
	ErrWorkStepsNotDone = errors.New("core: 存在未完成步骤")
)

// WorkCollectRequest 收集/关闭请求（RFC 二.2 步骤 4）。
type WorkCollectRequest struct {
	// WorkID 工作单 ID。
	WorkID string
	// PrimeTerm 调用方任期声明（须与落库任期严格相等——收集是编排写入，旧
	// 任期拒绝；未建立任期先接管）。
	PrimeTerm string
	// Report 收集报告（必填非空）：任务 24h 清理后的执行摘要载体（RFC 六.3），
	// 处置关闭时兼作处置理由。
	Report string
	// Outcome 关闭方式：空或 "done"=完成关闭（要求全部步骤 done）；
	// "abandoned"=处置放弃（要求全部步骤终态即可）。
	Outcome string
}

// workStepSettled 步骤是否终态（done/failed/needs_review/skipped）。pending/
// submitted/running 为未终态——收集的步骤覆盖门（全部步骤终态才可收集）。
func workStepSettled(s string) bool {
	switch s {
	case store.StepStatusDone, store.StepStatusFailed, store.StepStatusNeedsReview, store.StepStatusSkipped:
		return true
	}
	return false
}

// workAttemptSettled 尝试是否收束（结局非 submitted）。W2 允许同步骤并发尝试：
// 步骤状态以最后落盘终态为准，在途尝试的存在只能从尝试表判定——收集的尝试收束
// 门（全部尝试终局才可关闭，审查①）。
func workAttemptSettled(outcome string) bool {
	return outcome != store.AttemptOutcomeSubmitted
}

// CollectWork 收集并关闭工作单：单事务终态闭合（头部定案校验序）——report 必填
// → 存在性 → 终态回执幂等（prime_term/report/outcome 三同源 → dup=true 返现状
// 零写入零事件；不同 → work_closed）→ 任期（no_term/term_conflict）→ 步骤/尝试
// 收束门（work_steps_open：在途步骤或在途尝试；abandoned 下 pending 不算开步，
// 随关闭记为 skipped）→ 完成性（work_steps_not_done，部分报告不误关闭）→ 落终态。
//
// actor 服务端验证的鉴权主体（REST 层取得，事件以独立字段落账，与自报任期分开
// 存储——RFC 六.2，同 CreateWork/TakeoverWork 口径）。传空则事件无主体字段（仅限
// 直连 core 的内部调用方；REST 入口恒非空）。
func (c *Core) CollectWork(req WorkCollectRequest, actor string) (*WorkView, bool, error) {
	if req.WorkID == "" {
		return nil, false, ErrInvalid
	}
	report := strings.TrimSpace(req.Report)
	if report == "" {
		return nil, false, fmt.Errorf("%w: report 必填（任务清理后工作单报告是唯一执行摘要）", ErrInvalid)
	}
	outcome := strings.TrimSpace(req.Outcome)
	if outcome == "" {
		outcome = "done"
	}
	if outcome != "done" && outcome != "abandoned" {
		return nil, false, fmt.Errorf("%w: outcome 须为 done 或 abandoned（收到 %q）", ErrInvalid, req.Outcome)
	}
	term := strings.TrimSpace(req.PrimeTerm)
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
			// 终态回执幂等：相同收集请求响应丢失后原样重放——任期、报告、关闭
			// 方式与落库三同源 → 返回成功回执（dup=true 零写入零事件）；任一
			// 不同（含他方以相同参数冒领）按已终态拒绝（对齐接管回执同源纪律）
			if term == w.PrimeTerm && report == strings.TrimSpace(w.Report) &&
				outcome == w.Status {
				dup = true
				wv = workViewOf(w, 0)
				return nil
			}
			return fmt.Errorf("%w: 工作单已 %s（终态不可再收集/再派发/再接管）", ErrWorkClosed, w.Status)
		}
		if w.PrimeTerm == "" {
			return fmt.Errorf("%w: 建单未设任期且未接管过，先 swarmmind_work_takeover 建立任期", ErrWorkNoTerm)
		}
		if term != w.PrimeTerm {
			return fmt.Errorf("%w: 声明任期 %q ≠ 当前任期 %q（收集是编排写入，旧任期拒绝）", ErrWorkTermConflict, term, w.PrimeTerm)
		}
		// 收集门（审查①②）：全部步骤终态且全部尝试收束才可收集。pending 在
		// abandoned 下不算开步（随关闭处置为 skipped）；submitted/running 与
		// 在途尝试任何一种在场都拒——W2 并发尝试下步骤呈 done 不代表执行收束
		var openSteps, notDone, skipSteps []string
		doneCount := 0
		for i := range w.Steps {
			s := &w.Steps[i]
			switch s.Status {
			case store.StepStatusPending:
				if outcome == "abandoned" {
					skipSteps = append(skipSteps, fmt.Sprintf("#%d", s.Seq))
					continue
				}
				openSteps = append(openSteps, fmt.Sprintf("#%d(%s)", s.Seq, s.Status))
			case store.StepStatusSubmitted, store.StepStatusRunning:
				openSteps = append(openSteps, fmt.Sprintf("#%d(%s)", s.Seq, s.Status))
			case store.StepStatusDone:
				doneCount++
			default:
				notDone = append(notDone, fmt.Sprintf("#%d(%s)", s.Seq, s.Status))
			}
		}
		if len(openSteps) > 0 {
			return fmt.Errorf("%w: 步骤 %s 未收束（在途步骤等终态回写；待派发步骤随 outcome=abandoned 处置放弃，完成关闭要求全部执行）", ErrWorkStepsOpen, strings.Join(openSteps, "、"))
		}
		var openAttempts []string
		for _, a := range w.Attempts {
			if !workAttemptSettled(a.Outcome) {
				openAttempts = append(openAttempts, fmt.Sprintf("#%d(步骤 #%d)", a.Seq, a.StepSeq))
			}
		}
		if len(openAttempts) > 0 {
			return fmt.Errorf("%w: 尝试 %s 仍在途（同步骤并发尝试未收束——全部尝试终局后才可收集，等任务终态回写）", ErrWorkStepsOpen, strings.Join(openAttempts, "、"))
		}
		if outcome == "done" && len(notDone) > 0 {
			// 部分报告不误关闭（RFC 二.2 步骤 4）：存在 failed/needs_review/
			// skipped 步骤时不得标成完成；处置放弃走 outcome=abandoned
			return fmt.Errorf("%w: 步骤 %s 未完成——部分完成不能标成 done；确认放弃剩余步骤用 outcome=abandoned（报告写明处置理由）", ErrWorkStepsNotDone, strings.Join(notDone, "、"))
		}
		// 单事务终态闭合：状态 + 盖章 + 报告 + 未派发步骤处置 + 事件
		now := c.now()
		w.Status = outcome
		w.Report = report
		w.ClosedAt = &now
		exp := now.Add(WorkTerminalRetention)
		w.ExpiresAt = &exp
		for i := range w.Steps {
			if w.Steps[i].Status == store.StepStatusPending {
				w.Steps[i].Status = store.StepStatusSkipped
				appendWorkEvent(&w, now, WorkEventStepUpdated, term, actor,
					fmt.Sprintf("处置放弃：步骤 #%d 未执行，随关闭记为 skipped（跳过=决定不执行，非完成）", w.Steps[i].Seq))
			}
		}
		if outcome == store.WorkStatusDone {
			appendWorkEvent(&w, now, WorkEventCollect, term, actor,
				fmt.Sprintf("收集关闭：%d 步全部完成（done），报告已存档", doneCount))
		} else {
			skipped := len(skipSteps)
			if skipped > 0 {
				appendWorkEvent(&w, now, WorkEventCollect, term, actor,
					fmt.Sprintf("处置关闭：%d/%d 步完成、%d 步未执行随关闭跳过（abandoned）——理由见报告", doneCount, len(w.Steps), skipped))
			} else {
				appendWorkEvent(&w, now, WorkEventCollect, term, actor,
					fmt.Sprintf("处置关闭：%d/%d 步完成，剩余放弃（abandoned）——理由见报告", doneCount, len(w.Steps)))
			}
		}
		st.Works[w.ID] = w
		wv = workViewOf(w, 0)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return wv, dup, nil
}

// sweepWorks 终态 Work 到期清理（Sweep 第六阶段，RFC 六.3）：done/abandoned 过
// ExpiresAt 整单删除并同步清除 WorkIdems 指纹映射（悬空映射会令同指纹重建走
// 悬空防御分支重登——语义虽收敛，快照不留垃圾）。未完成 Work 无 ExpiresAt 永不
// 清理。返回被删单 ID 列表供调用方记日志。
func sweepWorks(st *store.State, now time.Time) []string {
	var removed []string
	for id, w := range st.Works {
		if w.Status != store.WorkStatusDone && w.Status != store.WorkStatusAbandoned {
			continue
		}
		if w.ExpiresAt == nil || w.ExpiresAt.After(now) {
			continue
		}
		delete(st.Works, id)
		for fp, wid := range st.WorkIdems {
			if wid == id {
				delete(st.WorkIdems, fp)
			}
		}
		removed = append(removed, id)
	}
	return removed
}

// tasksGoneForWork 读档解释面注解（W3 接口补全）：尝试绑定的任务 ID 已过 24h
// 保留期清理的列表——Work 独立记忆仍解释终局（attempt.outcome/幂等键/内容指纹
// 随单保留），缺口在「任务详情为何查不到」，据此标注。仅 GetWork 读档路径计算
// （建单/接管/收集响应是写入视角现状，不携带注解）。
func tasksGoneForWork(st *store.State, w store.Work) []string {
	var gone []string
	for _, a := range w.Attempts {
		if a.TaskID != "" {
			if _, ok := st.Tasks[a.TaskID]; !ok {
				gone = append(gone, a.TaskID)
			}
		}
	}
	return gone
}
