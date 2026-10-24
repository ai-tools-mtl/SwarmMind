// node 侧任务终态通知挂钩（standalone 通知补盲区，2026-10-24）：hub_url 为空的
// standalone 模式 hub 不存在，任务终态无任何 IM 通知——本文件把终态落账本与
// 通知钩子接起来。通知归 hub 的 networked 模式装配方不传 Notifier（nil），零
// 调用零重复；hub 路由任务只存在于 networked 模式，天然不会经此触发。
package service

import (
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TerminalNotifier 任务终态通知器（Config.Notifier，可选依赖，nil=禁用）。
// 结构化实现见 internal/notify.Dispatcher（与 hub core.TerminalNotifier 同形，
// 两侧接口各自声明）。实现方自行异步化并消化错误，不得阻塞执行收尾 goroutine
// （通知失败绝不影响任务终态与主流程，PRODUCT-DIRECTION v2 P4③ 同口径）；
// 同任务同终态本方法只会被调用一次（见 notifyTerminal）。
type TerminalNotifier interface {
	TaskFinal(t protocol.Task)
}

// notifyTerminal 终态旁路通知：仅在本任务 MarkFinished 成功（首次落终态）后
// 调用——账本「先到为准」，后续重复收尾/取消竞争失败方一律报错，成功即首次
// 转换，天然去重（与 hub core.fireTerminal 挂在首次接受点同构）。视图经
// ledger.TaskView 出（补终态 Status/Result 与耗时口径）。nil 钩子零开销。
func (s *Service) notifyTerminal(taskID string) {
	if s.cfg.Notifier == nil {
		return
	}
	e, ok := s.cfg.Ledger.Get(taskID)
	if !ok || e.State != ledger.StateFinished || e.Result == nil {
		return // 防御：只在确有终态结果时通知
	}
	s.cfg.Notifier.TaskFinal(ledger.TaskView(e))
}

// NotifyRecoveredTerminal 启动恢复新产生终态的通知旁路（返工必修2）：恢复降级
// 发生在 ledger.Open（service 尚未构造），不经 MarkFinished，notifyTerminal 运行
// 期旁路接不到——恢复产生的 needs_review（started 崩溃重启）/expired（离线期间
// 过期）恰是最需要通知的场景。entries 传 Recovery.Downgraded：降级是 recover 单
// 写事务内的一次性非终态→终态转换，每任务至多一条、重启后已 finished 不再降级，
// 同一任务只通知一次（去重锚点在账本恢复逻辑；本方法不做二次去重，调用点唯一
// ——cmd/node 装配处）。nil 钩子零开销（networked/setup 装配口径，通知归 hub）。
func (s *Service) NotifyRecoveredTerminal(entries []ledger.Entry) {
	if s.cfg.Notifier == nil {
		return
	}
	for _, e := range entries {
		if e.State != ledger.StateFinished || e.Result == nil {
			continue // 防御：与 notifyTerminal 同口径，只通知确有终态结果的条目
		}
		s.cfg.Notifier.TaskFinal(ledger.TaskView(e))
	}
}
