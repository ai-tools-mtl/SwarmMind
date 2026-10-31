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

// fixFailures 连续修复未过复审的次数（对当前版本的失效投影计入：确认被版本变
// 更作废 = 仍未解——防反复改版绕过阈值）。pending 复核中的修复不计（还在等）。
func fixFailures(is *Issue, currentHash string) int {
	n := 0
	for i := range is.Fixes {
		f := &is.Fixes[i]
		switch f.Result {
		case ReReviewStillOpen:
			n++
		case ReReviewConfirmed:
			if f.VerifiedAgainst != currentHash {
				n++ // 确认因版本变更失效 → 计入未解
			}
		default:
			// pending（复核中）不计
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
