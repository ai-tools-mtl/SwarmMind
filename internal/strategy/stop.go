// 停止/暂停判定（W05-01 交付⑤）：预算耗尽 / 同一问题修复 N 次未过复审 / 验收
// 争议 → 建议暂停并保留进度（不换模型、不无限重试——PLAN §6「耗尽或不确定时
// 保留进度并暂停」）。纯判定：暂停动作本身由调用方（W04 control pause）落账，
// 本包不改空间状态。
//
// 「同一问题修两次不过」按项目规则可配置（Config.MaxUnresolvedFixRounds）：
// SwarmMind 自身默认 2（上报 Hana/用户裁决），其他项目显式配置。
package strategy

import (
	"fmt"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
)

// StopKind 停止触发种类。
type StopKind string

const (
	// StopBudgetMessages 消息预算耗尽（三分离计数之一）。
	StopBudgetMessages StopKind = "budget_messages"
	// StopBudgetRounds 派发轮次预算耗尽（三分离计数之一）。
	StopBudgetRounds StopKind = "budget_dispatch_rounds"
	// StopBudgetWall 墙钟预算耗尽（三分离计数之一；running 未结算段计入）。
	StopBudgetWall StopKind = "budget_wall_seconds"
	// StopFixRounds 同一问题修复 N 次未过复审（默认阈值 2 = SwarmMind 规则）。
	StopFixRounds StopKind = "fix_rounds_unresolved"
	// StopAcceptanceDispute 验收争议（显式登记；不确定转人工裁决）。
	StopAcceptanceDispute StopKind = "acceptance_dispute"
)

// StopTrigger 单项停止触发。
type StopTrigger struct {
	Kind   StopKind `json:"kind"`
	Detail string   `json:"detail"`
}

// StopEvaluation 停止判定结论（ShouldPause = 任一触发；空触发 = 不建议暂停）。
type StopEvaluation struct {
	ShouldPause bool          `json:"should_pause"`
	Triggers    []StopTrigger `json:"triggers,omitempty"`
}

// fixFailures 末段连续「修复未过复审」次数（终审定案，建议2：只统计**当前仍处
// 未解态**的失败，不全量累计历史）——从最近一次修复向前数：
//   - still_open：计一次，继续向前（负结论现在仍然成立）；
//   - confirmed 且对当前版本有效：终止统计并返回累计值——其之前的失败已清账，
//     其之后（更晚）的失败已计入（「最新确认对当前版本时归零」指它作为最近一次
//     修复时的形态）；
//   - confirmed 但已因版本变更失效：不算修复失败（修复本身未被判负，只是版本又
//     变了——完成门的版本匹配另把它拦住），跳过继续向前；
//   - pending（复核中）：统计截止（还在等，不算失败）。
//
// 反例已封：confirm@v1→失效→confirm@v2→失效→confirm@v3（对当前版本）→ 归零，
// 不触发「2 次未解上报」（全量累计把实际已解的历史失效确认为误报）。
func fixFailures(is *Issue, currentHash string) int {
	n := 0
	for i := len(is.Fixes) - 1; i >= 0; i-- {
		f := &is.Fixes[i]
		switch f.Result {
		case ReReviewStillOpen:
			n++
		case ReReviewConfirmed:
			if f.VerifiedAgainst == currentHash {
				return n // 有效确认终止统计：其前失败已清账，其后失败已计入 n
			}
			// 失效确认：跳过（不算失败），继续向前找末段连续失败
		default:
			return n // pending（复核中）：统计截止
		}
	}
	return n
}

// EvaluateStop 停止条件判定（纯读：空间视图 + 循环事实）。
//
// 预算三分离（collab Budget）：三计数各自判耗尽、各自触发（0 = 不限制）；墙钟
// 用量按 collab 同一语义计入 running 未结算段（对照 BudgetUsed.wallSecondsNow，
// 整秒截断）。
func (e *Engine) EvaluateStop(space collab.SpaceView, now time.Time) StopEvaluation {
	var triggers []StopTrigger
	b := space.Budget
	u := space.Used
	if b.MaxMessages > 0 && u.Messages >= b.MaxMessages {
		triggers = append(triggers, StopTrigger{Kind: StopBudgetMessages,
			Detail: fmt.Sprintf("消息预算耗尽 %d/%d", u.Messages, b.MaxMessages)})
	}
	if b.MaxDispatchRounds > 0 && u.DispatchRounds >= b.MaxDispatchRounds {
		triggers = append(triggers, StopTrigger{Kind: StopBudgetRounds,
			Detail: fmt.Sprintf("派发轮次预算耗尽 %d/%d", u.DispatchRounds, b.MaxDispatchRounds)})
	}
	wall := u.WallSeconds
	if !u.RunningSince.IsZero() && now.After(u.RunningSince) {
		wall += int64(now.Sub(u.RunningSince) / time.Second)
	}
	if b.MaxWallSeconds > 0 && wall >= int64(b.MaxWallSeconds) {
		triggers = append(triggers, StopTrigger{Kind: StopBudgetWall,
			Detail: fmt.Sprintf("墙钟预算耗尽 %d/%d 秒", wall, b.MaxWallSeconds)})
	}
	e.mu.RLock()
	limit := e.fixLimit()
	current := e.currentHashLocked(space.ID)
	var escalated []string
	if lp, ok := e.loops[space.ID]; ok {
		for i := range lp.Issues {
			is := &lp.Issues[i]
			if n := fixFailures(is, current); n >= limit {
				escalated = append(escalated, fmt.Sprintf("%s（%d 次修复未过复审，%s:%d）", is.ID, n, is.File, is.Line))
			}
		}
		if lp.Dispute != nil && lp.Dispute.Disputed {
			triggers = append(triggers, StopTrigger{Kind: StopAcceptanceDispute,
				Detail: "验收争议待人工裁决：" + lp.Dispute.Note})
		}
	}
	e.mu.RUnlock()
	if len(escalated) > 0 {
		triggers = append(triggers, StopTrigger{Kind: StopFixRounds,
			Detail: fmt.Sprintf("同一问题修复 %d 次未过复审达阈值（上报裁决）：%s", limit, joinList(escalated))})
	}
	return StopEvaluation{ShouldPause: len(triggers) > 0, Triggers: triggers}
}

// joinList 小清单拼接（留痕/判定人话形态）。
func joinList(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "；"
		}
		out += s
	}
	return out
}
