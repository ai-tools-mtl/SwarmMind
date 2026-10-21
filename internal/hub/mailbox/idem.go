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

// content 请求内容指纹的载体：提交内容四要素（不含 idem_key 本身）。
// 字段与 json tag 与 protocol 中对应结构严格一致，保证指纹跨版本稳定。
type content struct {
	TargetNode  string             `json:"target_node"`
	TargetAgent string             `json:"target_agent"`
	Input       protocol.TaskInput `json:"input"`
	SubmittedBy string             `json:"submitted_by"`
}

// ContentHash 提交内容指纹：canonical JSON（encoding/json 确定性输出）的 SHA-256 hex。
func ContentHash(req protocol.TaskSubmitRequest) string {
	c := content{
		TargetNode:  req.TargetNode,
		TargetAgent: req.TargetAgent,
		Input:       req.Input.Clone(),
		SubmittedBy: req.SubmittedBy,
	}
	b, err := json.Marshal(c)
	if err != nil {
		// 纯标量+slice+map 结构，不可达；防御性返回空串（调用方按不同内容处理）
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// taskContentHash 从已持久化任务体提取同口径内容指纹。
func taskContentHash(t *protocol.Task) string {
	return ContentHash(protocol.TaskSubmitRequest{
		TargetNode:  t.TargetNode,
		TargetAgent: t.TargetAgent,
		Input:       t.Input,
		SubmittedBy: t.SubmittedBy,
	})
}

// IdemCheck 幂等查重（提交校验链第 3 步，先于配额检查）：
//   - 请求无 idem_key → (零值, false, nil)，不做查重；
//   - 同键同内容 → (原 task_id, true, nil)，命中即返回不做配额检查；
//   - 同键不同内容 → (零值, false, ErrConflict)；
//   - 键不存在 → (零值, false, nil)。
func IdemCheck(st *store.State, req protocol.TaskSubmitRequest) (taskID string, dup bool, err error) {
	if req.IdemKey == "" {
		return "", false, nil
	}
	want := ContentHash(req)
	for id, t := range st.Tasks {
		if t.IdemKey != req.IdemKey {
			continue
		}
		if taskContentHash(&t) == want {
			return id, true, nil
		}
		return "", false, ErrConflict
	}
	return "", false, nil
}
