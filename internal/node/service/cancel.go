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
// hub→node task_cancel {task_id, reason}，信封携 hub 当前任务落库租约
//（cancel_fence，P1-2）。语义至少一次+幂等——重复 cancel 对同 task_id 收敛为
// 一次执行中断、一次终态回传：
//   - 任务在途（accepted/started，执行 goroutine 存活）：触发 cancelEntry →
//     执行 goroutine 在任一等待点（workdir 互斥/并发槽/进程执行）观察到取消，
//     killProcessGroup 整组终止，账本写 finished(cancelled/user_cancelled) 后回
//     task_result(cancelled)（走既有三段确认，task_result→result_ack）；回带前
//     对账本租约与 cancel_fence 取大者重臂（P1-2：重派推进的租约经取消帧补授，
//     hub 侧按 cancel_fence ≥ 落库租约宽校验接受，不再被等值门禁拒收）；
//   - 已终态：结果未闭合的先对账重臂再重发回执（同上）；已确认/closed 审计的
//     回 task_cancelled 幂等 ack（无可取消对象）；
//   - 不在账本（取消先于投递）：除回 task_cancelled ack 外落持久化取消墓碑
//     （ledger cancels.json）——随后同 task_id 的 task_offer 后到时查墓碑拒绝
//     执行，直接以 cancelled/user_cancelled 落终态回传（finishCancelledOffer），
//     堵死「取消先于 offer，offer 后到仍执行」窗口（2026-10-22 修复轮2）；
//   - 在途注册表未命中但账本仍 accepted/started（等待点放弃的窄窗口/关闭竞态）：
//     直接账本落 cancelled 终态并回传（同样先对账重臂）；与执行 goroutine 的
//     落盘竞争由账本「先到为准」收敛（ErrWrongState → 回幂等 ack）。

// cancelEntry 单个在途任务的取消开关：trigger 关闭 done 并联动已绑定的执行
// ctx；bind 在执行 ctx 创建后挂接（此前已触发则立即生效）。done 关闭即取消
// 请求成立，各等待点以 select 观察。cancelFence 记录取消帧携带的 hub 当前任务
// 落库租约（P1-2：取消回写对账取大者的基准；重复取消取更大者，noteCancelFence
// 先于 trigger 执行——done 关闭前的写入对收尾 goroutine 可见）。
type cancelEntry struct {
	mu     sync.Mutex
	done   chan struct{}
	cancel context.CancelFunc // 执行 ctx（runCtx）的取消函数；bind 后非 nil
	fence  int64              // 取消帧携 cancel_fence（多次取消取大者）
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

// noteCancelFence 记录取消帧携带的 cancel_fence（幂等取大者）；须先于 trigger
// 调用，保证收尾 goroutine 经 done 关闭观察到取消时对账基准已就绪。
func (ce *cancelEntry) noteCancelFence(fence int64) {
	if fence <= 0 {
		return
	}
	ce.mu.Lock()
	if fence > ce.fence {
		ce.fence = fence
	}
	ce.mu.Unlock()
}

// cancelFence 返回已记录的 cancel_fence（0=取消帧未携租约，旧版 hub）。
func (ce *cancelEntry) cancelFence() int64 {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	return ce.fence
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
// 任务租约（P4/SPEC 4.11 + P1-2）：取消帧携带 hub 当前任务落库租约
//（cancel_fence）。取消执行本身无需门禁：取消⇒任务 hub 侧终态⇒同 task_id
// 永不重派（旧取消不可能命中新派单执行），重复取消由墓碑/幂等 ack 收敛；
// zombie 进程无当前会话收不到取消帧即无法伪造新 cancel_fence。取消回写
//（task_result(cancelled)）回带前按「账本租约 vs cancel_fence 取大者」重臂：
// 重派已推进租约而 node 未及重臂的滞后（如 ack 丢后重派、重派 offer 送达失败
// 留 submitted）经此补授，hub 对 cancelled 回执按 cancel_fence ≥ 落库租约宽
// 校验接受，真实取消结果不再滞留 node。
func (s *Service) handleTaskCancel(env *protocol.Envelope) {
	var p protocol.TaskCancelPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.TaskID == "" {
		log.Printf("[node] task_cancel 载荷解析失败: %v", err)
		return
	}
	// 1) 在途：记录对账基准并触发执行中断（执行 goroutine 负责整组终止、落盘
	//    对账重臂与回传）
	if ce, ok := s.lookupCancel(p.TaskID); ok {
		log.Printf("[node] 收到取消（cancel_fence=%d），中断在途执行 task=%s reason=%q",
			env.FenceEpoch, p.TaskID, p.Reason)
		ce.noteCancelFence(env.FenceEpoch)
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
				// 墓碑落盘失败：不发取消 ack——闭环确认必须以持久化为前提
				//（2026-10-26 双审缺陷修复）。hub 至少一次取消重发会携帧重试；
				// 若 ack 掉，重启后墓碑缺失，迟到 offer 必执行已取消任务。
				log.Printf("[node] 取消墓碑落盘失败，待取消重发重试 task=%s: %v", p.TaskID, err)
				return
			}
			log.Printf("[node] 取消命中未入账任务，落墓碑防后到 offer 执行 task=%s", p.TaskID)
		} else if e.ResultAcked {
			log.Printf("[node] 取消幂等命中（终态已确认）task=%s state=%s", p.TaskID, e.State)
		} else if err := s.rearmTo(p.TaskID, env.FenceEpoch); err != nil {
			// P1-1 同口径：对账重臂落盘失败不重发不 ack——此时回执仍按旧租约
			// 回带必被宽校验前的滞后再拒；hub 至少一次取消重发会再携
			// cancel_fence 对账重试（重臂成功同时解除 closed）。
			log.Printf("[node] 取消对账重臂失败，待取消重发重试 task=%s: %v", p.TaskID, err)
			return
		} else {
			cur, ok := s.cfg.Ledger.Get(p.TaskID)
			if !ok || cur.Closed {
				// 同值 no-op 且已 closed：拒收审计保留，重发只会再被拒
				log.Printf("[node] 取消命中已 closed 条目（审计保留）task=%s", p.TaskID)
			} else {
				// 已终态且结果未闭合：立即重发回执（重臂成功即含 closed 解除，
				// P1-3），回执按对账后的租约回带，hub cancelled 分支合并收敛
				log.Printf("[node] 取消命中未确认终态，对账后重发回执 task=%s state=%s fence=%d",
					p.TaskID, cur.State, cur.FenceEpoch)
				if cur.Result != nil {
					s.sendResult(p.TaskID, *cur.Result)
					return
				}
			}
		}
		s.sendRef(protocol.MsgTaskCancelled, p.TaskID)
		return
	}
	// 3) 账本在途但在途注册表未命中（等待点放弃/关闭竞态窗口）：先对账重臂再落
	//    cancelled 终态并回传；与执行 goroutine 竞争由账本先到为准收敛。
	//    重臂失败仍落终态（在途条目不得滞留，重启会重执行已取消任务），只跳过
	//    发送——结果滞留重发队列，hub 取消重发再对账闭合。
	rearmErr := s.rearmTo(p.TaskID, env.FenceEpoch)
	if rearmErr != nil {
		log.Printf("[node] 取消对账重臂失败 task=%s: %v", p.TaskID, rearmErr)
	}
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.MarkFinished(p.TaskID, res); err != nil {
		if errors.Is(err, ledger.ErrWrongState) {
			// 先到终态为准（幂等收敛，SPEC 4.3）：终态已由先到者落盘，回执由
			// 重发链路闭合，此处回幂等 ack 正确。
			log.Printf("[node] 取消落盘命中既有终态（先到为准）task=%s: %v", p.TaskID, err)
			s.sendRef(protocol.MsgTaskCancelled, p.TaskID)
			return
		}
		// 落盘失败（磁盘等）：终态未落上，不发确认——ack 掉则 hub 视取消闭环，
		// 重启后丢失 cancelled 终态（2026-10-26 双审缺陷修复）。hub 至少一次
		// 取消重发携帧重试，与本函数上方重臂失败只跳过发送的口径一致。
		log.Printf("[node] 取消终态落盘失败，待取消重发重试 task=%s: %v", p.TaskID, err)
		return
	}
	s.notifyTerminal(p.TaskID) // 首次落终态即通知（standalone 补盲区）
	if rearmErr != nil {
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
// finished(cancelled/user_cancelled) + 回传 task_result。cancelFence 为取消帧
// 携带的 hub 当前任务租约（P1-2）：落盘前先对账取大者重臂，回执按对账后租约
// 回带（hub 宽校验收敛）。账本已有终态（先到为准，SPEC 4.3 收敛表）返回
// false，不写不发；对账重臂失败落盘照常（在途条目不得滞留），跳过发送，结果
// 滞留重发队列由 hub 取消重发再对账闭合。
func (s *Service) finishCancelledBeforeRun(taskID string, cancelFence int64) bool {
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.rearmTo(taskID, cancelFence); err != nil {
		log.Printf("[node] 取消对账重臂失败，待取消重发重试 task=%s: %v", taskID, err)
	}
	if err := s.cfg.Ledger.MarkFinished(taskID, res); err != nil {
		log.Printf("[node] 取消落盘未生效（先到终态为准）task=%s: %v", taskID, err)
		return false
	}
	s.notifyTerminal(taskID) // 首次落终态即通知（重臂竞争早退也不漏——终态已成立）
	if cancelFence > 0 && cancelFence > s.fenceOf(taskID) {
		return true // 重臂未生效（落盘失败竞争）：不按旧租约回发，留重发/对账闭合
	}
	s.sendResult(taskID, res)
	return true
}

// finishCancelledOffer 取消先于投递（offer 命中取消墓碑）的收口：任务不再执行，
// 以 cancelled/user_cancelled 落账本终态并回传 task_result——hub 侧该任务已
// cancelled 且无结果，收敛表 cancelled 分支合并真实数据。入账携带本次 offer 的
// 租约快照（P4：回执按派发代次回带，等值仲裁通过）。落盘成功即消费墓碑
// （账本条目此后由 ErrDuplicate 挡重复执行）；落盘失败保留墓碑，重复 offer 再试。
func (s *Service) finishCancelledOffer(task protocol.Task, fence int64) {
	res := protocol.TaskResult{
		Status:     protocol.ResultCancelled,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      protocol.CodeUserCancelled,
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.AcceptFenced(task, ledger.OriginHub, fence); err != nil && !errors.Is(err, ledger.ErrDuplicate) {
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
	s.notifyTerminal(task.ID) // 首次落终态即通知（standalone 补盲区）
	if err := s.cfg.Ledger.ClearCancelTombstone(task.ID); err != nil {
		log.Printf("[node] 清取消墓碑失败 task=%s: %v", task.ID, err)
	}
	log.Printf("[node] 任务在取消后投递（墓碑命中），不执行 task=%s", task.ID)
	s.sendResult(task.ID, res)
}
