package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 幂等去重（SPEC 4.5）：idem_key 作用域为 hub 全局，与任务同生命周期。
// 索引直接派生自 State.Tasks（任务体携带 IdemKey 与全部提交内容），不设独立
// 存储——任务删除（终态 24h 清理）即 idem 失效，由构造保证「同生命周期清理」。
//
// 指纹口径（批 B 对齐批次内容指纹，control.go batchContentHash）：提交内容四要素
// + 群引用 group_ref。判定规则：
//   - 同键同群同内容 → dup 命中原任务；
//   - 同键仅群引用不同（换群/带与不带群）→ 不同指纹 → 视为新提交产生新任务
//     （群引用是管理面路由装饰而非协议内容，不触发 409）；
//   - 同键其余内容不同 → ErrConflict（SPEC 4.5 409 语义不变）。

// content 协议内容指纹载体（不含 idem_key 本身与群引用）。字段与 json tag 与
// protocol 中对应结构严格一致，保证指纹跨版本稳定。
type content struct {
	TargetNode  string             `json:"target_node"`
	TargetAgent string             `json:"target_agent"`
	Input       protocol.TaskInput `json:"input"`
	SubmittedBy string             `json:"submitted_by"`
}

// groupContent 全量指纹载体：content 内嵌展开 + group_ref（omitempty——空群引用
// 的编码与纯 content 完全一致，新旧格式互操作天然兼容）。
type groupContent struct {
	content
	GroupRef string `json:"group_ref,omitempty"`
}

// ContentHash 提交内容全量指纹（含群引用）：canonical JSON（encoding/json 确定性
// 输出）的 SHA-256 hex。Input 过 EffectiveInput 生效形态（P4.3：声明 depends_on
// 时 stdin 尾部附依赖清单）——与 newTask 落库的任务体同一变换，重试指纹与落库
// 任务指纹逐字节一致。
func ContentHash(req protocol.TaskSubmitRequest) string {
	c := groupContent{
		content: content{
			TargetNode:  req.TargetNode,
			TargetAgent: req.TargetAgent,
			Input:       protocol.EffectiveInput(req.Input, req.DependsOn),
			SubmittedBy: req.SubmittedBy,
		},
		GroupRef: req.GroupRef,
	}
	return contentHashJSON(c)
}

// BaseContentHash 协议内容指纹（不含群引用）：换群判定基准（同键全量指纹不合
// 但基准合 → 仅群引用不同，视为新提交而非冲突）。
func BaseContentHash(req protocol.TaskSubmitRequest) string {
	return contentHashJSON(content{
		TargetNode:  req.TargetNode,
		TargetAgent: req.TargetAgent,
		Input:       protocol.EffectiveInput(req.Input, req.DependsOn),
		SubmittedBy: req.SubmittedBy,
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
// 落库任务的 stdin 已含依赖清单，原样返回不重复追加）。
func taskContentHash(t *protocol.Task) string {
	return ContentHash(protocol.TaskSubmitRequest{
		TargetNode:  t.TargetNode,
		TargetAgent: t.TargetAgent,
		Input:       t.Input,
		SubmittedBy: t.SubmittedBy,
		GroupRef:    t.GroupRef,
		DependsOn:   t.DependsOn,
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
