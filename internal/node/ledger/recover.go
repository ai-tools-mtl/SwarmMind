package ledger

import (
	"sort"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Recovery 启动恢复结果（SPEC 4.8）。
type Recovery struct {
	// Reschedule accepted 且未过期的任务：交回本地调度继续执行（无需 hub 重派）。
	Reschedule []protocol.Task
	// Resend finished 且未 acked 的条目：重连后重发 task_result。
	// 含恢复中降级产生的 failed/expired 与 needs_review/node_restarted_midway（SPEC 4.8「并回传」）。
	// closed 条目不在内（SPEC 4.5：hub 拒收已停止重传，记录保留、不再回传）。
	Resend []Entry
}

// recover 执行启动恢复（Open 内调用，SPEC 4.8）：
//   - accepted 且已过期 → 不执行，写 finished（status=failed, error=expired）；
//   - started 未 finished → 执行结果不确定，写 finished（status=needs_review,
//     error=node_restarted_midway）；禁止自动重新执行；
//   - finished 且未 acked → 进入重发队列。
//
// 降级写走单写先写后见事务，持久化成功后才返回；无降级需求则不发起事务
// （避免每次启动都重写账本文件）。
func (l *Ledger) recover(now time.Time) (*Recovery, error) {
	needDowngrade := false
	for _, e := range l.View() {
		switch e.State {
		case StateAccepted:
			if !e.Task.ExpireAt.IsZero() && e.Task.ExpireAt.Before(now) {
				needDowngrade = true
			}
		case StateStarted:
			needDowngrade = true
		}
	}
	if needDowngrade {
		err := l.Update(func(m map[string]Entry) error {
			for id, e := range m {
				var down *protocol.TaskResult
				switch e.State {
				case StateAccepted:
					// 已过期：不执行，直接写 failed/expired 并回传
					if !e.Task.ExpireAt.IsZero() && e.Task.ExpireAt.Before(now) {
						down = &protocol.TaskResult{
							Status:     protocol.ResultFailed,
							ExitCode:   protocol.ExitCodeNotRun,
							Error:      protocol.CodeExpired,
							FinishedAt: now,
						}
					}
				case StateStarted:
					// 执行结果不确定：needs_review，禁止自动重新执行（SPEC 4.8）
					down = &protocol.TaskResult{
						Status:     protocol.ResultNeedsReview,
						ExitCode:   protocol.ExitCodeNotRun,
						Error:      protocol.CodeNodeRestartedMidway,
						FinishedAt: now,
					}
				}
				if down != nil {
					ne := e.Clone()
					ne.State = StateFinished
					ne.Result = down
					ne.ResultAcked = false
					m[id] = ne
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	rec := &Recovery{}
	for _, e := range l.View() {
		switch e.State {
		case StateAccepted:
			rec.Reschedule = append(rec.Reschedule, e.Task.Clone())
		case StateFinished:
			if !e.ResultAcked && !e.Closed {
				rec.Resend = append(rec.Resend, e.Clone())
			}
		}
	}
	// 排序保证恢复清单确定性（map 遍历无序）：调度按创建时间先进先出，重发按任务 ID。
	sort.Slice(rec.Reschedule, func(i, j int) bool {
		a, b := rec.Reschedule[i], rec.Reschedule[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	sort.Slice(rec.Resend, func(i, j int) bool {
		return rec.Resend[i].Task.ID < rec.Resend[j].Task.ID
	})
	return rec, nil
}
