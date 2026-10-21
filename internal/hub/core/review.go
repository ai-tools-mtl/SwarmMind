package core

import (
	"errors"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 核对记录持久化（SPEC 六.五）：needs_review 任务的人工处理登记。纯管理面：
// 不改协议终态（保持 needs_review），node 账本与协议层不感知（无帧）；
// 重复提交覆盖旧记录。

// ErrNotNeedsReview 任务不处于 needs_review，核对登记不适用（HTTP 409）。
var ErrNotNeedsReview = errors.New("core: 任务不在 needs_review，不可登记核对记录")

// reviewActions 核对动作白名单（protocol.ReviewXxx）。
var reviewActions = map[string]bool{
	protocol.ReviewAckReview:  true,
	protocol.ReviewMarkFailed: true,
}

// RecordReview 在任务上追加人工核对记录并随快照持久化。action 必须在白名单内；
// 任务必须存在且处于 needs_review；重复提交覆盖旧记录。UpdatedAt 不动——核对
// 非协议状态变更。返回登记后的任务副本。
func (c *Core) RecordReview(taskID, action, note, by string) (protocol.Task, error) {
	if !reviewActions[action] {
		return protocol.Task{}, ErrInvalid
	}
	var out protocol.Task
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[taskID]
		if !ok {
			return ErrTaskNotFound
		}
		if t.Status != protocol.StatusNeedsReview {
			return ErrNotNeedsReview
		}
		nt := t.Clone()
		nt.Review = &protocol.TaskReview{By: by, At: c.now(), Action: action, Note: note}
		st.Tasks[taskID] = nt
		out = nt
		return nil
	})
	if err != nil {
		return protocol.Task{}, err
	}
	c.noteTask(&out) // 任务体字节记账随 review 联动（存储预算计量口径）
	return out, nil
}
