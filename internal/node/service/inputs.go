// 上游产物挂载（P4 L2 第二子批 + P4④第二子轮跨机取回，FILE-COLLAB L2 / SPEC
// 4.12）：依赖门控放行的下游任务在 started/spawn 前的输入准备。解析（本机账本
// 优先，跨机经 dep_resolve/artifact_fetch 中继取回）、纯复制、逐文件 SHA-256
// 校验、原子发布全部在 internal/node/inputs；本文件只做 service 装配与失败
// 语义收口。
//
// 时序与语义（对照 execute 主链）：
//   - 挂载点在过期重检（N4）之后、MarkStarted 之前——输入就绪前不 started 不
//     spawn（FILE-COLLAB L2），挂载期间任务保持 accepted（hub 视角
//     acknowledged，挂载耗时落在 acknowledged→started 之间，无 hub 侧计时依赖）；
//   - 独立超时（Config.InputPrepareTimeout，默认 inputs.DefaultTimeout），随
//     任务 runCtx 一并被打断（服务关闭/管理面取消）；
//   - 失败 → accepted 直达 failed（error=input_prepare_failed+上游/文件定位，
//     ExitCodeNotRun，不产生执行副作用，对齐 policy_denied/expired 的
//     pre-started 失败先例）；挂载期间管理面取消 → 走取消收口
//     （finishCancelledBeforeRun），服务关闭 → 条目保持 accepted 由重启恢复
//     再调度（重启后重挂：先按发布 journal 回放收敛上次崩溃残留，再对已存在
//     目录幂等替换）；Prepare 只在末次发布后复查 ctx 仍未超时/取消才返回成功
//     ——超时/取消后必返回失败，主链不得 MarkStarted；
//   - 挂载发生在下游 agent 的 workdir 互斥持有期内（execute 主链已获锁），
//     同 agent 任务输入区写入天然串行；
//   - 无依赖任务不进入此路径（红线：无 depends_on 零行为变化）。
package service

import (
	"context"
	"log"

	"github.com/mtl802/swarmmind/internal/node/inputs"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// prepareInputs 下游任务输入准备：返回 true 表示任务生命周期已在本函数收口
//（失败终态/取消收口/关闭放弃），调用方直接返回；false = 就绪，继续主链
//（started → spawn）。
func (s *Service) prepareInputs(task protocol.Task, runCtx context.Context, ce *cancelEntry) bool {
	workdir, ok := s.artifactRootFor(task.TargetAgent)
	if !ok || workdir == "" {
		// 与 runner 的 ExecProfile.Workdir 同源装配；缺失即配置异常，无法定位
		// 输入区，按挂载失败收敛（不冒进执行）。
		s.finishInputPrepareFailed(task, &inputs.Error{
			Reason: "目标 agent " + task.TargetAgent + " 的 workdir 未配置，输入区不可定位",
		})
		return true
	}
	ctx, cancel := context.WithTimeout(runCtx, s.cfg.InputPrepareTimeout)
	defer cancel()
	// 组合解析器（P4④第二子轮/SPEC 4.12）：本机优先（账本有 completed 上游走
	// 本地复制，行为零变化）；本机无 completed 且启用了跨机取回（EnableRemoteInputs，
	// networked 模式）→ 经 hub 中继取回异机产物（dep_resolve/artifact_fetch，
	// 重试轮询与失败分类在 inputs.Resolver/RemoteResolver）。
	resolver := &inputs.Resolver{
		Local: &inputs.LocalResolver{Ledger: s.cfg.Ledger, RootOf: s.artifactRootFor},
	}
	if rr := s.remoteResolver(); rr != nil {
		resolver.Remote = rr
	}
	if err := inputs.Prepare(ctx, resolver, workdir, task.ID, task.DependsOn); err != nil {
		log.Printf("[node] 输入准备失败 task=%s: %v", task.ID, err)
		// 竞态收口先于失败语义：取消与关闭的 ctx 中断不落 input_prepare_failed
		if ce.triggered() {
			s.finishCancelledBeforeRun(task.ID, ce.cancelFence())
			return true
		}
		if runCtx.Err() != nil {
			return true // 服务关闭打断：条目保持 accepted，重启恢复再调度（SPEC 4.8）
		}
		s.finishInputPrepareFailed(task, err)
		return true
	}
	return false
}

// finishInputPrepareFailed 挂载失败的终态收口：failed/input_prepare_failed，
// ExitCodeNotRun、StartedAt 零值（未执行），落盘成功即通知与回传（三段确认
// 与其他终态同链路）。落盘竞争失败（先到终态为准）由账本幂等语义收敛。
func (s *Service) finishInputPrepareFailed(task protocol.Task, err error) {
	res := protocol.TaskResult{
		Status:     protocol.ResultFailed,
		ExitCode:   protocol.ExitCodeNotRun,
		Error:      err.Error(),
		FinishedAt: protocol.NowUTC(),
	}
	if err := s.cfg.Ledger.MarkFinished(task.ID, res); err != nil {
		log.Printf("[node] 写入挂载失败终态失败 task=%s: %v", task.ID, err)
		return
	}
	log.Printf("[node] 上游产物挂载失败，任务不执行 task=%s error=%s", task.ID, res.Error)
	s.notifyTerminal(task.ID) // 失败=终态，同走终态旁路（standalone 通知）
	s.sendResult(task.ID, res)
}
