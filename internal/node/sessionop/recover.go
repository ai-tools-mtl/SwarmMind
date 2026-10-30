// 重启不确定收敛（W01-02 RFC §6）：启动期扫描账本非终态会话受理轮，落账为待核
// 对终态——绝不自动重跑、绝不重投 CLI。started 条目已由 ledger.recover 降级
//（needs_review/node_restarted_midway，零改动），本文件只处理 accepted 条目：
// ledger.recover 只把过期 accepted 降级，未过期 accepted 进入 Reschedule 清单，
// 会话轮若被重投将二次执行已中断的 CLI 轮——这里在装配期先落终态，使恢复重投的
// 执行入口 MarkStarted 必败（ErrWrongState），从机制上封死重跑。
//
// 未收敛阻断集（W01-02 codex 终审必修①）：收敛落盘可能失败（磁盘满等），失败
// 条目保持 accepted，重投的 MarkStarted 会成功——「MarkStarted 必败」这层防线
// 只在收敛成功后成立。故凡收敛尝试过但终态未确认落盘的条目一律登记进阻断集，
// 执行入口（service execute → RecoveryBlocked 守卫）对阻断条目拒绝进入执行：先
// 就地重试收敛一次（RetryUncertainTurn，成功即消除阻断、条目转终态仍由
// MarkStarted 拦住），仍失败则保持 accepted 不执行不 spawn，直至某次收敛成功消
// 除。阻断集为进程内态：重启由 RecoverUncertainTurns 重新收敛或重新阻断，不跨
// 进程持久。
package sessionop

import (
	"fmt"
	"sort"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// turnKey 阻断集键：会话 + 轮序号（受理轮唯一身份）。
type turnKey struct {
	sessionID string
	seq       int64
}

// RecoverUncertainTurns 启动期一次性收敛（service 装配期、任何 Reschedule 执行
// 前调用）：accepted 且非过期的会话受理轮（Task.Session != nil、TurnSeq>0）落
// 终态 finished(needs_review, node_restarted_midway)——与 ledger.recover 对
// started 条目的既有降级同口径（local 条目经 MarkFinished 既有规则自确认，不进
// 重发队列）。幂等：终态条目跳过，重复执行零变更。返回收敛条数；落盘失败的条
// 目登记进未收敛阻断集（恢复重投将被执行入口拒绝），并如实返回错误（部分收敛
// 时计数如实，下次启动对剩余条目重试）。
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
			s.blockMu.Lock()
			s.blocked[turnKey{t.Session.SessionID, t.Session.TurnSeq}] = t.ID
			s.blockMu.Unlock()
			s.log.Printf("sessionop: 会话轮重启收敛落盘失败，已加入未收敛阻断集（恢复重投拒绝执行）task=%s session=%s seq=%d: %v",
				t.ID, t.Session.SessionID, t.Session.TurnSeq, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("sessionop: 会话轮重启收敛落盘失败 task=%s session=%s seq=%d: %w",
					t.ID, t.Session.SessionID, t.Session.TurnSeq, err)
			}
			continue // 先到为准（ErrWrongState）与其余真实失败统一如实上报
		}
		s.clearBlocked(t.Session.SessionID, t.Session.TurnSeq) // 防御：旧阻断随收敛成功消除
		n++
		s.log.Printf("sessionop: 会话轮重启收敛 task=%s session=%s seq=%d → 待核对（不重跑）",
			t.ID, t.Session.SessionID, t.Session.TurnSeq)
	}
	return n, firstErr
}

// RecoveryBlocked 报告该轮是否在未收敛阻断集（收敛尝试过但终态未确认落盘）。
// service execute 入口守卫用：阻断条目的恢复重投不得进入执行。
func (s *Service) RecoveryBlocked(sessionID string, seq int64) bool {
	s.blockMu.Lock()
	defer s.blockMu.Unlock()
	_, ok := s.blocked[turnKey{sessionID, seq}]
	return ok
}

// RetryUncertainTurn 阻断条目的就地重试收敛（execute 入口守卫命中时调用，每次
// 重投入口至多重试一次）：对账本实物重落待核对终态——成功即消除阻断并返回 true
//（条目已终态，后续 MarkStarted 必败仍拦住执行）；失败保持阻断返回 false（调用
// 方须拒绝执行）。非阻断条目、或账本受理记录已不在（先到终态/保留期清理）直接
// 返回 true 放行——执行入口由 MarkStarted 先到为准与账本实况如实判定。
func (s *Service) RetryUncertainTurn(sessionID string, seq int64, taskID string, now time.Time) bool {
	s.blockMu.Lock()
	blockedTask, ok := s.blocked[turnKey{sessionID, seq}]
	s.blockMu.Unlock()
	if !ok {
		return true
	}
	if blockedTask != taskID {
		// 异任务重放同轮位：不消除阻断也不代他任务收敛（受理轮唯一，异常重复执
		// 行由执行链既有幂等约束拦截），守卫按阻断处理。
		return false
	}
	e, found := findTurnEntry(s.led, sessionID, seq)
	if !found {
		// 受理记录已不在（保留期清理/异常重放）：消除阻断，账本实况如实判定。
		s.clearBlocked(sessionID, seq)
		return true
	}
	if e.State == ledger.StateFinished {
		// 他路径已收敛（先到为准）：消除阻断，MarkStarted 必败拦住执行。
		s.clearBlocked(sessionID, seq)
		return true
	}
	res := protocol.TaskResult{
		Status:     protocol.ResultNeedsReview,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeNodeRestartedMidway,
		FinishedAt: now,
	}
	if err := s.led.MarkFinished(taskID, res); err != nil {
		s.log.Printf("sessionop: 会话轮重试收敛仍失败（保持阻断，恢复重投继续拒绝执行）task=%s session=%s seq=%d: %v",
			taskID, sessionID, seq, err)
		return false
	}
	s.clearBlocked(sessionID, seq)
	s.log.Printf("sessionop: 会话轮重试收敛成功 task=%s session=%s seq=%d → 待核对（不重跑）", taskID, sessionID, seq)
	return true
}

// clearBlocked 消除阻断登记（无登记为 no-op）。
func (s *Service) clearBlocked(sessionID string, seq int64) {
	s.blockMu.Lock()
	delete(s.blocked, turnKey{sessionID, seq})
	s.blockMu.Unlock()
}
