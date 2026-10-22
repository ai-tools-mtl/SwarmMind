package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 管理面任务取消（task_cancel 生命周期，SPEC 4.3 cancelled 终态）：
// hub→node task_cancel {task_id, reason}。语义至少一次+幂等——重复 cancel 对同
// task_id 收敛为一次执行中断、一次终态回传：
//   - 任务在途（accepted/started，执行 goroutine 存活）：触发 cancelEntry →
//     执行 goroutine 在任一等待点（workdir 互斥/并发槽/进程执行）观察到取消，
//     killProcessGroup 整组终止，账本写 finished(cancelled/user_cancelled) 后回
//     task_result(cancelled)（走既有三段确认，task_result→result_ack）；
//   - 已终态：回 task_cancelled 幂等 ack（无可取消对象）；
//   - 不在账本（取消先于投递）：除回 task_cancelled ack 外落持久化取消墓碑
//     （ledger cancels.json）——随后同 task_id 的 task_offer 后到时查墓碑拒绝
//     执行，直接以 cancelled/user_cancelled 落终态回传（finishCancelledOffer），
//     堵死「取消先于 offer，offer 后到仍执行」窗口（2026-10-22 修复轮2）；
//   - 在途注册表未命中但账本仍 accepted/started（等待点放弃的窄窗口/关闭竞态）：
//     直接账本落 cancelled 终态并回传；与执行 goroutine 的落盘竞争由账本
//     「先到为准」收敛（ErrWrongState → 回幂等 ack）。

// cancelEntry 单个在途任务的取消开关：trigger 关闭 done 并联动已绑定的执行
// ctx；bind 在执行 ctx 创建后挂接（此前已触发则立即生效）。done 关闭即取消
// 请求成立，各等待点以 select 观察。
type cancelEntry struct {
	mu     sync.Mutex
	done   chan struct{}
	cancel context.CancelFunc // 执行 ctx（runCtx）的取消函数；bind 后非 nil
}

func newCancelEntry() *cancelEntry {
	return &cancelEntry{done: make(chan struct{})}
}

// doneCh 返回取消观察通道（关闭 = 取消请求成立）。
func (ce *cancelEntry) doneCh() <-chan struct{} { return ce.done }

// triggered 报告取消请求是否已成立。
func (ce *cancelEntry) triggered() bool {
	select {
	case <-ce.done:
		return true
	default:
		return false
	}
}

// trigger 发起取消（幂等）：关闭 done，并联动已绑定的执行 ctx。
func (ce *cancelEntry) trigger() {
	ce.mu.Lock()
	if ce.triggered() {
		ce.mu.Unlock()
		return
	}
	close(ce.done)
	c := ce.cancel
	ce.mu.Unlock()
	if c != nil {
		c()
	}
}

// bind 挂接执行 ctx 取消函数；若取消请求先于 bind 成立，立即生效（ctx 取消幂等）。
func (ce *cancelEntry) bind(cancel context.CancelFunc) {
	ce.mu.Lock()
	ce.cancel = cancel
	ce.mu.Unlock()
	if ce.triggered() {
		cancel()
	}
}

// regCancel 登记在途任务的取消开关（execute 调用方 goroutine 内同步执行，
// 先于任何 task_cancel 可达时刻——同一连接的 offer/cancel 按序处理）。
func (s *Service) regCancel(taskID string) *cancelEntry {
	ce := newCancelEntry()
	s.muCancels.Lock()
	s.cancels[taskID] = ce
	s.muCancels.Unlock()
	return ce
}

// unregCancel 注销（执行 goroutine 退出时）。
func (s *Service) unregCancel(taskID string) {
	s.muCancels.Lock()
	delete(s.cancels, taskID)
	s.muCancels.Unlock()
}

// lookupCancel 查在途任务取消开关。
func (s *Service) lookupCancel(taskID string) (*cancelEntry, bool) {
	s.muCancels.Lock()
	defer s.muCancels.Unlock()
	ce, ok := s.cancels[taskID]
	return ce, ok
}

// handleTaskCancel task_cancel 入口：见包内「管理面任务取消」注释。
func (s *Service) handleTaskCancel(env *protocol.Envelope) {
	var p protocol.TaskCancelPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.TaskID == "" {
		log.Printf("[node] task_cancel 载荷解析失败: %v", err)
		return
	}
	// 1) 在途：触发执行中断（执行 goroutine 负责整组终止、落盘与回传）
	if ce, ok := s.lookupCancel(p.TaskID); ok {
		log.Printf("[node] 收到取消，中断在途执行 task=%s reason=%q", p.TaskID, p.Reason)
		ce.trigger()
		return
	}
	// 2) 已终态或不在账本：幂等 ack（无可取消对象；结果由重发链路闭合）。
	//    不在账本（取消先于投递）额外落取消墓碑并持久化：随后同 task_id 的
	//    task_offer 后到时查墓碑拒绝执行（finishCancelledOffer，handleOffer），
	//    堵死「取消先于 offer，offer 后到仍执行」窗口（2026-10-22 修复轮2）。
	e, ok := s.cfg.Ledger.Get(p.TaskID)
	if !ok || e.State == ledger.StateFinished {
		if !ok {
			if err := s.cfg.Ledger.AddCancelTombstone(p.TaskID); err != nil {
				log.Printf("[node] 取消墓碑落盘失败 task=%s: %v", p.TaskID, err)
			}
			log.Printf("[node] 取消命中未入账任务，落墓碑防后到 offer 执行 task=%s", p.TaskID)
		} else {
			log.Printf("[node] 取消幂等命中（已终态）task=%s state=%s", p.TaskID, e.State)
		}
		s.sendRef(protocol.MsgTaskCancelled, p.TaskID)
		return
	}
	// 3) 账本在途但在途注册表未命中（等待点放弃/关闭竞态窗口）：直接落 cancelled
	// 终态并回传；与执行 goroutine 竞争由账本先到为准收敛
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.MarkFinished(p.TaskID, res); err != nil {
		log.Printf("[node] 取消落盘未生效（先到终态为准）task=%s: %v", p.TaskID, err)
		s.sendRef(protocol.MsgTaskCancelled, p.TaskID)
		return
	}
	log.Printf("[node] 任务已取消（未再执行）task=%s reason=%q", p.TaskID, p.Reason)
	s.sendResult(p.TaskID, res)
}

// cancelledResult 执行中断的取消收尾结果（对齐 interruptedResult 模式）：status
// 置 cancelled（独立终态，不折叠为 failed），error=user_cancelled；已采集的
// 退出码/stdout/stderr tail/产物清单原样保留。
func cancelledResult(res protocol.TaskResult) protocol.TaskResult {
	res.Status = protocol.ResultCancelled
	res.Error = protocol.CodeUserCancelled
	return res
}

// finishCancelledBeforeRun 未获执行即取消（等待互斥/并发槽期间被打断）：账本
// finished(cancelled/user_cancelled) + 回传 task_result。账本已有终态（先到为准，
// SPEC 4.3 收敛表）返回 false，不写不发。
func (s *Service) finishCancelledBeforeRun(taskID string) bool {
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.MarkFinished(taskID, res); err != nil {
		log.Printf("[node] 取消落盘未生效（先到终态为准）task=%s: %v", taskID, err)
		return false
	}
	s.sendResult(taskID, res)
	return true
}

// finishCancelledOffer 取消先于投递（offer 命中取消墓碑）的收口：任务不再执行，
// 以 cancelled/user_cancelled 落账本终态并回传 task_result——hub 侧该任务已
// cancelled 且无结果，收敛表 cancelled 分支合并真实数据。落盘成功即消费墓碑
// （账本条目此后由 ErrDuplicate 挡重复执行）；落盘失败保留墓碑，重复 offer 再试。
func (s *Service) finishCancelledOffer(task protocol.Task) {
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.Accept(task); err != nil && !errors.Is(err, ledger.ErrDuplicate) {
		// 账本写入失败（磁盘等）：不 ack 不发结果，保留墓碑，hub 回退重推（SPEC 4.5）
		log.Printf("[node] 墓碑收口入账失败 task=%s: %v", task.ID, err)
		return
	}
	if err := s.cfg.Ledger.MarkFinished(task.ID, res); err != nil {
		// 已有终态（先到为准）：墓碑使命已尽（ErrDuplicate 挡重复），清墓碑，
		// 结果由重发链路闭合
		log.Printf("[node] 墓碑收口落终态未生效（先到终态为准）task=%s: %v", task.ID, err)
		_ = s.cfg.Ledger.ClearCancelTombstone(task.ID)
		return
	}
	if err := s.cfg.Ledger.ClearCancelTombstone(task.ID); err != nil {
		log.Printf("[node] 清取消墓碑失败 task=%s: %v", task.ID, err)
	}
	log.Printf("[node] 任务在取消后投递（墓碑命中），不执行 task=%s", task.ID)
	s.sendResult(task.ID, res)
}
