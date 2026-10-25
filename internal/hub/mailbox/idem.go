package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 幂等去重（SPEC 4.5）：idem_key 作用域为 hub 全局，与任务同生命周期。
// 索引直接派生自 State.Tasks（任务体携带 IdemKey 与全部提交内容），不设独立
// 存储——任务删除（终态 24h 清理）即 idem 失效，由构造保证「同生命周期清理」。
//
// 指纹口径（批 B 对齐批次内容指纹，control.go batchContentHash）：提交内容五要素
//（目标/代理/生效输入/提交方/规范化依赖列表）+ 群引用 group_ref + 工作单绑定
//（work_ref+work_step_seq，P5 W2）。依赖列表独立哈希进指纹材料，不经
// EffectiveInput 注入的 stdin 清单间接体现——清单标记文本（protocol.DepManifestMarker）
// 可被提交方预置进 stdin 令注入跳过，依赖只走 stdin 会漏进指纹（同键同 stdin
// 换依赖得同指纹，dup 短路在准入校验之前）。判定规则：
//   - 同键同群同内容（含同依赖、同绑定）→ dup 命中原任务；
//   - 同键仅群引用不同（换群/带与不带群）→ 不同指纹 → 视为新提交产生新任务
//     （群引用是管理面路由装饰而非协议内容，不触发 409）；
//   - 同键绑定不同（换工作单/换步骤）→ ErrConflict——工作单绑定是执行去重
//     关键面非路由装饰（静默落新任务=盲目重派，正是 Work 要防的事）；
//   - 同键其余内容不同（含换依赖/预置标记文本差异）→ ErrConflict（SPEC 4.5
//     409 语义不变）。
//
// prime_term（任期声明）不进指纹：归属标签非内容（RFC 六.2），换任期后同键重放
// 命中 dup 返原任务。attempt_ref 不进指纹：hub 分配号请求形不可声明，重试回放
// 短路于 idem 查重（先于绑定事务），尝试唯一性由构造保证（TESTPLAN-W2 头部定案）。

// content 协议内容指纹载体（不含 idem_key 本身、群引用与任期声明）。字段与
// json tag 与 protocol 中对应结构严格一致，保证指纹跨版本稳定。
type content struct {
	TargetNode  string             `json:"target_node"`
	TargetAgent string             `json:"target_agent"`
	Input       protocol.TaskInput `json:"input"`
	SubmittedBy string             `json:"submitted_by"`
	// DependsOn 规范化依赖列表（normDeps：TrimSpace，保序保重；空列表统一
	// 归一为 nil）。独立参与指纹，与 stdin 清单标记文本无关。
	DependsOn []string `json:"depends_on,omitempty"`
	// WorkRef/WorkStep 工作单绑定（P5 W2）：omitempty——未绑定请求的编码与
	// 既有格式逐字节一致（历史指纹零漂移）。
	WorkRef  string `json:"work_ref,omitempty"`
	WorkStep int64  `json:"work_step_seq,omitempty"`
}

// groupContent 全量指纹载体：content 内嵌展开 + group_ref（omitempty——空群引用
// 的编码与纯 content 完全一致，新旧格式互操作天然兼容）。
type groupContent struct {
	content
	GroupRef string `json:"group_ref,omitempty"`
}

// normDeps 指纹用依赖规范化：逐项 TrimSpace（与提交链 trimDeps 同口径），空
// 列表归一为 nil——JSON 编码 nil 与 [] 不同，须统一（未声明依赖的请求与旧
// 快照任务体均为 nil）。
func normDeps(deps []string) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, len(deps))
	for i, k := range deps {
		out[i] = strings.TrimSpace(k)
	}
	return out
}

// ContentHash 提交内容全量指纹（含群引用与工作单绑定）：canonical JSON
//（encoding/json 确定性输出）的 SHA-256 hex。Input 过 EffectiveInput 生效形态
//（P4.3：声明 depends_on 时 stdin 尾部附依赖清单）——与 newTask 落库的任务体
// 同一变换，重试指纹与落库任务指纹逐字节一致；规范化依赖列表独立参与（不依赖
// 清单标记文本）；工作单绑定取请求可声明的规范形（work_ref+step_seq，P5 W2）。
func ContentHash(req protocol.TaskSubmitRequest) string {
	c := groupContent{
		content: content{
			TargetNode:  req.TargetNode,
			TargetAgent: req.TargetAgent,
			Input:       protocol.EffectiveInput(req.Input, req.DependsOn),
			SubmittedBy: req.SubmittedBy,
			DependsOn:   normDeps(req.DependsOn),
			WorkRef:     req.WorkRef,
			WorkStep:    req.StepSeq,
		},
		GroupRef: req.GroupRef,
	}
	return contentHashJSON(c)
}

// BaseContentHash 协议内容指纹（不含群引用）：换群判定基准（同键全量指纹不合
// 但基准合 → 仅群引用不同，视为新提交而非冲突）。工作单绑定属协议内容（差异
// 即冲突，P5 W2），须在此参与——否则同键换绑定会被换群分支误吞为新任务。
func BaseContentHash(req protocol.TaskSubmitRequest) string {
	return contentHashJSON(content{
		TargetNode:  req.TargetNode,
		TargetAgent: req.TargetAgent,
		Input:       protocol.EffectiveInput(req.Input, req.DependsOn),
		SubmittedBy: req.SubmittedBy,
		DependsOn:   normDeps(req.DependsOn),
		WorkRef:     req.WorkRef,
		WorkStep:    req.StepSeq,
	})
}

// contentHashJSON 指纹计算：canonical JSON 的 SHA-256 hex。
func contentHashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// 纯标量+slice+map 结构，不可达；防御性返回空串（调用方按不同内容处理）
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// taskContentHash 从已持久化任务体提取同口径全量指纹（EffectiveInput 幂等：
// 落库任务的 stdin 已含依赖清单，原样返回不重复追加）。工作单绑定以落库规范形
//（WorkRef+WorkStep——与请求侧 WorkRef+StepSeq 同形同值）参与。
func taskContentHash(t *protocol.Task) string {
	return ContentHash(protocol.TaskSubmitRequest{
		TargetNode:  t.TargetNode,
		TargetAgent: t.TargetAgent,
		Input:       t.Input,
		SubmittedBy: t.SubmittedBy,
		GroupRef:    t.GroupRef,
		DependsOn:   t.DependsOn,
		WorkRef:     t.WorkRef,
		StepSeq:     t.WorkStep,
	})
}

// taskBaseContentHash 从已持久化任务体提取协议内容指纹（不含群引用）。
func taskBaseContentHash(t *protocol.Task) string {
	return BaseContentHash(protocol.TaskSubmitRequest{
		TargetNode:  t.TargetNode,
		TargetAgent: t.TargetAgent,
		Input:       t.Input,
		SubmittedBy: t.SubmittedBy,
		DependsOn:   t.DependsOn,
		WorkRef:     t.WorkRef,
		StepSeq:     t.WorkStep,
	})
}

// IdemCheck 幂等查重（提交校验链第 3 步，先于配额检查）：
//   - 请求无 idem_key → (零值, false, nil)，不做查重；
//   - 同键同群同内容 → (原 task_id, true, nil)，命中即返回不做配额检查；
//   - 同键仅群引用不同 → (零值, false, nil)——按新提交处理产生新任务（批 B 口径）；
//   - 同键其余内容不同 → (零值, false, ErrConflict)；
//   - 键不存在 → (零值, false, nil)。
func IdemCheck(st *store.State, req protocol.TaskSubmitRequest) (taskID string, dup bool, err error) {
	if req.IdemKey == "" {
		return "", false, nil
	}
	want := ContentHash(req)
	wantBase := BaseContentHash(req)
	for id, t := range st.Tasks {
		if t.IdemKey != req.IdemKey {
			continue
		}
		if taskContentHash(&t) == want {
			return id, true, nil
		}
		if taskBaseContentHash(&t) == wantBase {
			continue // 仅群引用不同（换群重试）：不 dup 不冲突，落为新任务
		}
		return "", false, ErrConflict
	}
	return "", false, nil
}
