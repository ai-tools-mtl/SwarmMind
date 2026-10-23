// 任务依赖（P4.3 第一子批 / FILE-COLLAB L2，SPEC 4.12）：depends_on 以 idem_key
// 寻址（跨重跑稳定），hub 侧门控调度。blocked 不进状态机——依赖未满足的任务保持
// submitted + BlockedReason 门控标注；SupersededBy 为上游重跑后的审计标注。
// 本文件只含常量与纯函数，hub 侧图校验/复评在 internal/hub/core。
package protocol

import "strings"

// 依赖图准入限制（沿依赖图上溯计算，SPEC 4.12）。
const (
	// MaxDependsDepth 依赖深度上限：新任务沿 depends_on 上溯的最长链边数 ≤ 4
	//（链上至多 5 个任务）。
	MaxDependsDepth = 4
	// MaxDependsCascade 依赖级联上限：上溯可达的去重上游任务总数 ≤ 8。
	MaxDependsCascade = 8
)

// blocked_reason 取值格式（Task.BlockedReason；空 = 未阻塞）。
const (
	// BlockedReasonBlockedByPrefix 等待中：依赖任务尚未到终态（或 idem 已随
	// 终态清理从快照消失——7d expire 兜底）。
	BlockedReasonBlockedByPrefix = "blocked_by:"
	// BlockedReasonUpstreamFailedPrefix 上游失败：依赖任务已落
	// failed/needs_review/cancelled（独立终态不自动重跑，下游无自动放行）。
	BlockedReasonUpstreamFailedPrefix = "upstream_failed:"
)

// BlockedByReason 构造等待中门控标注。
func BlockedByReason(idem string) string { return BlockedReasonBlockedByPrefix + idem }

// UpstreamFailedReason 构造上游失败门控标注。
func UpstreamFailedReason(idem string) string { return BlockedReasonUpstreamFailedPrefix + idem }

// DepManifestMarker 依赖清单标记行：EffectiveInput 的幂等判据（已含清单的
// stdin 不重复追加），亦供调用方识别注入段。
const DepManifestMarker = "\n[swarmmind:depends_on]"

// EffectiveInput 提交输入的生效形态：声明了 depends_on 时在 stdin 尾部附加
// 依赖清单（上游 idem_key 供 agent 自取产物）。幂等：已含标记的 stdin 原样返回。
//
// 幂等指纹口径（mailbox.ContentHash / taskContentHash）与本函数共用——重试请求
// 与落库任务双方都过同一变换，保证「同键同内容」判定不受清单注入影响；未声明
// 依赖时输出与旧版逐字节一致（向后兼容）。产物挂载（workdir/inputs/<上游id>/
// 纯复制）为 FILE-COLLAB L2 第二子批能力，本批先以清单附 idem 过渡。
func EffectiveInput(in TaskInput, deps []string) TaskInput {
	out := in.Clone()
	if len(deps) == 0 || strings.Contains(out.Stdin, DepManifestMarker) {
		return out
	}
	var b strings.Builder
	b.WriteString(DepManifestMarker)
	b.WriteString("\n本任务声明了上游依赖（idem_key）：\n")
	for _, k := range deps {
		b.WriteString("- ")
		b.WriteString(k)
		b.WriteString("\n")
	}
	b.WriteString("产物自动挂载尚未提供（FILE-COLLAB L2 第二子批）；请经管理面/MCP（swarmmind_artifact）按上游任务 task_id 自取产物。\n")
	out.Stdin += b.String()
	return out
}
