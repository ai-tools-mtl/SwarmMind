// 重启不确定收敛（W01-02 RFC §6）：启动期扫描账本非终态会话受理轮，落账为待核
// 对终态——绝不自动重跑、绝不重投 CLI。started 条目已由 ledger.recover 降级
//（needs_review/node_restarted_midway，零改动），本文件只处理 accepted 条目：
// ledger.recover 只把过期 accepted 降级，未过期 accepted 进入 Reschedule 清单，
// 会话轮若被重投将二次执行已中断的 CLI 轮——这里在装配期先落终态，使恢复重投的
// 执行入口 MarkStarted 必败（ErrWrongState），从机制上封死重跑。
package sessionop

import (
	"fmt"
	"sort"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// RecoverUncertainTurns 启动期一次性收敛（service 装配期、任何 Reschedule 执行
// 前调用）：accepted 且非过期的会话受理轮（Task.Session != nil、TurnSeq>0）落
// 终态 finished(needs_review, node_restarted_midway)——与 ledger.recover 对
// started 条目的既有降级同口径（local 条目经 MarkFinished 既有规则自确认，不进
// 重发队列）。幂等：终态条目跳过，重复执行零变更。返回收敛条数；非先到为准类
// 落盘失败如实返回错误（部分收敛时计数如实，下次启动对剩余条目重试）。
func (s *Service) RecoverUncertainTurns(now time.Time) (int, error) {
	var targets []protocol.Task
	for _, e := range s.led.View() {
		if e.State != ledger.StateAccepted {
			continue
		}
		sr := e.Task.Session
		if sr == nil || sr.TurnSeq <= 0 {
			continue
		}
		targets = append(targets, e.Task.Clone())
	}
	if len(targets) == 0 {
		return 0, nil
	}
	// 账本 map 遍历无序：按 (created_at, id) 确定序收敛（与恢复调度同序）。
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	n := 0
	var firstErr error
	for _, t := range targets {
		res := protocol.TaskResult{
			Status:     protocol.ResultNeedsReview,
			ExitCode:   protocol.ExitCodeNotRun,
			Error:      protocol.CodeNodeRestartedMidway,
			FinishedAt: now,
		}
		if err := s.led.MarkFinished(t.ID, res); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("sessionop: 会话轮重启收敛落盘失败 task=%s session=%s seq=%d: %w",
					t.ID, t.Session.SessionID, t.Session.TurnSeq, err)
			}
			continue // 先到为准（ErrWrongState）与其余真实失败统一如实上报
		}
		n++
		s.log.Printf("sessionop: 会话轮重启收敛 task=%s session=%s seq=%d → 待核对（不重跑）",
			t.ID, t.Session.SessionID, t.Session.TurnSeq)
	}
	return n, firstErr
}
