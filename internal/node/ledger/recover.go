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
	// Downgraded 本次恢复新降级产生的终态条目（accepted 过期 → failed/expired、
	// started → needs_review/node_restarted_midway）：仅在降级单写事务内收集，
	// 无降级为 nil。去重锚点（返工必修2）：降级是非终态→终态的一次性转换，重启
	// 后条目已 finished 不再进降级分支，同一任务至多出现在一次 Recovery 里——
	// standalone 终态通知据此恰发一次（service.NotifyRecoveredTerminal，cmd/node
	// 装配处调用）。按任务 ID 排序，清单确定。
	Downgraded []Entry
	// Orphans 崩溃孤儿清单（maturity-orphan-guard）：started 降级时登记了 pgid
	// 的条目——崩溃时仍在跑的执行进程组，service 装配执行器后做身份校验清扫
	//（宁漏不误，安全语义见 runner.CleanupOrphanGroup）。按任务 ID 排序，清单确定。
	// 仅 ledger 内部消费（OrphanGroups() 读口），无需经 Open 返回值外泄。
	Orphans []OrphanGroup
}

// OrphanGroup 一条待清扫的崩溃孤儿（maturity-orphan-guard）。
type OrphanGroup struct {
	TaskID      string
	TargetAgent string
	Pgid        int64
	ProcStart   int64 // 组长启动时刻指纹（pid+指纹等值匹配的证据，0=无证据必放过）
	BootIdent   string // 落盘时的启动周期标识（与当前周期不等=证据过期，放过）
}

// recover 执行启动恢复（Open 内调用，SPEC 4.8）：
//   - accepted 且已过期 → 不执行，写 finished（status=failed, error=expired）；
//   - started 未 finished → 执行结果不确定，写 finished（status=needs_review,
//     error=node_restarted_midway）；禁止自动重新执行；
//   - finished 且未 acked → 进入重发队列。
//
// 降级写走单写先写后见事务，持久化成功后才返回；无降级需求则不发起事务
// （避免每次启动都重写账本文件）。新降级条目记入 Recovery.Downgraded（恢复
// 终态通知的触发清单，去重锚点见该字段注释）。
func (l *Ledger) recover(now time.Time) (*Recovery, error) {
	needDowngrade := false
	orphans := []OrphanGroup{}
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
	var downgraded []Entry
	if needDowngrade {
		err := l.updateOp("recover", func(m map[string]Entry) error {
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
					// 孤儿登记（maturity-orphan-guard）：started 降级时若登记了
					// pgid，提交恢复清单——崩溃时仍在跑的执行进程组，待 service
					// 装配执行器后做身份校验清扫；pgid=0（旧条目/Windows）不登记。
					// ProcStart/BootIdent 随行：清扫侧以同周期+指纹等值精确匹配原实例。
					if e.Pgid > 0 {
						orphans = append(orphans, OrphanGroup{
							TaskID: id, TargetAgent: e.Task.TargetAgent, Pgid: e.Pgid,
							ProcStart: e.ProcStart, BootIdent: e.BootIdent,
						})
					}
				}
				if down != nil {
					ne := e.Clone()
					ne.State = StateFinished
					ne.Result = down
					if e.normalized().Origin == OriginLocal {
						// local 条目恢复降级为终态时本机消费即送达（返工 #6）：
						// ResultAcked=true + FirstAckedAt=now，不进重发队列——
						// 否则 standalone 期滞留，切换组网后成批推 hub 换 task_not_found
						// 噪音（hub 下发条目保持未 acked，重发链路不变）。
						ne.ResultAcked = true
						ne.FirstAckedAt = now
					} else {
						ne.ResultAcked = false
					}
					m[id] = ne
					downgraded = append(downgraded, ne) // 新降级终态（ne 已深拷贝），通知触发清单
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		// map 遍历无序：通知清单按任务 ID 排序保持确定。
		sort.Slice(downgraded, func(i, j int) bool { return downgraded[i].Task.ID < downgraded[j].Task.ID })
	}

	rec := &Recovery{Downgraded: downgraded, Orphans: orphans}
	sort.Slice(rec.Orphans, func(i, j int) bool { return rec.Orphans[i].TaskID < rec.Orphans[j].TaskID })
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
