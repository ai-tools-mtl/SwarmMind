package inputs

import (
	"context"
	"fmt"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// LocalResolver 同机上游解析器（FILE-COLLAB L2 第一子轮挂载骨架）：从 node
// 账本解析依赖 idem 对应的上游任务，产物在本机 outputs 目录直接读。本机账本无
// 该 idem 的 completed 上游时返回 RemoteCandidate 失败（Resolver 组合解析器据
// 此转交 RemoteResolver 经 hub 中继取回，P4④第二子轮）；单独使用本解析器（如
// standalone）时该失败即最终失败。
type LocalResolver struct {
	// Ledger node 执行账本（上游任务条目与终态结果的权威来源；hub 下发任务的
	// 结果随 task_result 落账本，含产物 Path/SHA-256/Size 登记）。
	Ledger *ledger.Ledger
	// RootOf 上游 agent 名 → workdir 绝对路径（service 的 artifactRootFor 读侧，
	// 与 Runners 同源装配；热重载换表后取当前表）。
	RootOf func(agent string) (string, bool)
}

// ResolveUpstream 解析单个依赖 idem（InputResolver 实现）：扫描账本中携带该
// idem 的条目，取**最早 completed**（与 hub resolveIDem 满足口径一致；账本
// result_acked 条目 24h 清理后取现存最早，若与 hub 所选版本不一致，SHA 校验
// 仍保证挂载内容与登记一致）。规则：
//   - 账本无该 idem 任一条目 → RemoteCandidate 失败（上游不在本机执行/已过
//     保留期清理——组合解析器转远端取回，hub 侧可区分「异机在」与「已清理」）；
//   - 有条目但无 completed → RemoteCandidate 失败（本机版本未完成或失败；
//     同 idem 异机版本可能已 completed，组合解析器转远端）；
//   - 上游 completed 零产物登记 → 返回空清单（答案型上游，Prepare 不建目录）；
//   - 上游 agent 不在当前配置 → 失败（产物根不可定位，配置漂移属本机性失败，
//     不转远端——文案即真相）。
func (r *LocalResolver) ResolveUpstream(_ context.Context, idem string) (*UpstreamInputs, error) {
	var (
		cand    ledger.Entry
		candKey string
		found   bool
		pending bool // 有非终态条目（上游尚在执行/排队，区别于已落失败终态）
	)
	for _, e := range r.Ledger.View() {
		if e.Task.IdemKey != idem {
			continue
		}
		found = true
		if e.State != ledger.StateFinished {
			pending = true
			continue
		}
		if e.Result == nil || e.Result.Status != protocol.ResultCompleted {
			continue
		}
		key := e.Task.CreatedAt.UTC().Format("20060102150405.000000000") + "|" + e.Task.ID
		if cand.Task.ID == "" || key < candKey {
			cand, candKey = e, key
		}
	}
	switch {
	case !found:
		return nil, &Error{IDem: idem, RemoteCandidate: true,
			Reason: "本机账本无该 idem 的任务（上游不在本机执行，或已随终态保留期清理）"}
	case cand.Task.ID == "":
		if pending {
			return nil, &Error{IDem: idem, RemoteCandidate: true,
				Reason: "上游尚未完成（本机账本无 completed 版本）"}
		}
		return nil, &Error{IDem: idem, RemoteCandidate: true,
			Reason: "本机账本无该 idem 的 completed 上游版本（上游未完成或失败）"}
	}
	if len(cand.Result.Artifacts) == 0 {
		return &UpstreamInputs{TaskID: cand.Task.ID, Workdir: "", Artifacts: nil}, nil
	}
	wd, ok := r.RootOf(cand.Task.TargetAgent)
	if !ok || wd == "" {
		return nil, &Error{IDem: idem, Upstream: cand.Task.ID,
			Reason: fmt.Sprintf("上游 agent %q 不在本机配置，产物根不可定位（配置漂移）", cand.Task.TargetAgent)}
	}
	return &UpstreamInputs{
		TaskID:    cand.Task.ID,
		Workdir:   wd,
		Artifacts: append([]protocol.Artifact(nil), cand.Result.Artifacts...),
	}, nil
}
