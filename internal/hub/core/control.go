package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 控制操作批次（SPEC 六.五）：中心端「控制能力」的实现载体——管理员向全网
// 任意 node 的巡检类 agent 派发正式任务（每项目标走 Submit 全链，hub 不内嵌
// 执行体，服务执行始终在 node 侧）。
//
// 持久化两阶段（消除「已执行任务无批次记录」孤儿窗口）：
//  1. 单事务持久化批次骨架（batch_id + targets 快照 + status=creating + idem
//     映射）——此后任何时刻崩溃，批次记录先于任何子任务存在；
//  2. 逐项 Submit（每项以 batch_id+序号派生子幂等键），每项完成后单事务回写
//     批次条目；全部条目已决后置 status=complete。
//
// 中途崩溃重启：creating 批次由启动恢复（recoverBatches）收尾——已创建子任务
// 按派生幂等键补记回条目（各自幂等键防重复执行），未及创建项标 skipped=
// interrupted；批次不复跑。准入快照时点：先冻结逐项目标判定（一次性读在线表+
// 能力表），离线/未暴露目标标 skipped 不阻塞其余；提交与否以 Submit 事务内
// 重检为准。

// 批次条目跳过原因（ControlItem.Skipped）。
const (
	// SkipOffline 目标 node 未注册或不在线（不可达）。
	SkipOffline = "offline"
	// SkipNotExposed 目标在线但 agent 不存在或 exposed=false。
	SkipNotExposed = "not_exposed"
	// SkipInterrupted 崩溃恢复标记：创建中断时未及创建的目标项（见 recoverBatches）。
	SkipInterrupted = "interrupted"
)

// 批次生命周期取值（ControlBatch.Status，定义见 store）。
const (
	BatchStatusCreating = store.BatchStatusCreating
	BatchStatusComplete = store.BatchStatusComplete
)

// ErrBatchConflict 批次幂等同键不同内容（HTTP 409，语义同 SPEC 4.5 任务 409）。
var ErrBatchConflict = errors.New("core: 批次同键不同内容")

// BatchItem 批次单项目录（HTTP 返回体；快照记录 + 查询时点任务状态合并）。
type BatchItem struct {
	// Target 目标 node_id。
	Target string `json:"target"`
	// TaskID 提交成功的任务 ID（跳过/失败为空）。
	TaskID string `json:"task_id,omitempty"`
	// Skipped 跳过原因：offline | not_exposed | interrupted。
	Skipped string `json:"skipped,omitempty"`
	// Error 其余校验链失败错误码（mailbox_full / frame_too_large 等）。
	Error string `json:"error,omitempty"`
	// Status 任务当前状态（查询时点合并；跳过/失败为空）。
	Status string `json:"status,omitempty"`
}

// BatchView 控制操作批次视图（POST 202 响应体与 GET 查询响应体共用）。
type BatchView struct {
	// BatchID 批次标识。
	BatchID string `json:"batch_id"`
	// Agent 目标 agent 名。
	Agent string `json:"agent"`
	// Status 批次生命周期：creating | complete。
	Status string `json:"status"`
	// CreatedAt 批次创建时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	// Items 逐目标结果（顺序与请求 targets 一致）。
	Items []BatchItem `json:"items"`
	// Dup 幂等命中（仅 POST 响应为 true；GET 恒 false，omitempty 不输出）。
	Dup bool `json:"dup,omitempty"`
}

// batchContent 批次内容指纹载体：agent + 目标序表 + 输入（不含 idem_key 本身）。
// 目标顺序敏感（items 顺序与请求一致，乱序视为不同内容）。字段名与 json tag
// 固定，保证指纹跨版本稳定（同 mailbox/idem 口径）。
type batchContent struct {
	Agent   string             `json:"agent"`
	Targets []string           `json:"targets"`
	Input   protocol.TaskInput `json:"input"`
}

// batchContentHash 批次内容指纹：canonical JSON（encoding/json 确定性输出）的
// SHA-256 hex。
func batchContentHash(agent string, targets []string, input protocol.TaskInput) string {
	b, err := json.Marshal(batchContent{Agent: agent, Targets: targets, Input: input.Clone()})
	if err != nil {
		// 纯标量+slice+map 结构，不可达；防御性返回空串（调用方按不同内容处理）
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// storedBatchHash 已持久化批次 → 同口径指纹（targets 取自 items 序；恢复标记
// interrupted 不改 Target，指纹不受恢复影响）。
func storedBatchHash(b store.ControlBatch) string {
	targets := make([]string, len(b.Items))
	for i, it := range b.Items {
		targets[i] = it.Target
	}
	return batchContentHash(b.Agent, targets, b.Input)
}

// batchChildIdem 批次子任务派生幂等键：batch_id+序号（batch_id 全局唯一故键
// 全局唯一）。用途：Submit 返回后、条目回写前的崩溃窗口内任务已落库，重启恢复
// 据此识别「已创建未登记」项补记 task_id；同键重复创建在 Submit 幂等链命中返回
// 原任务，不重复执行。
func batchChildIdem(batchID string, idx int) string {
	return fmt.Sprintf("%s#%d", batchID, idx)
}

// admissionDecision 冻结准入判定。
type admissionDecision int

const (
	admitSubmit admissionDecision = iota // 尝试提交（Submit 事务内重检为准）
	admitOffline                         // 跳过：离线/未注册
	admitNotExposed                      // 跳过：在线但未暴露
)

// freezeAdmission 一次性读在线表 + 能力表视图，对全部目标出统一时点初判
//（快照一致性：同一批次的准入判定取自同一时刻的目录，不随逐项提交漂移）。
func (c *Core) freezeAdmission(view *store.State, agent string, targets []string) []admissionDecision {
	decisions := make([]admissionDecision, len(targets))
	for i, target := range targets {
		switch {
		case !c.isOnline(target):
			decisions[i] = admitOffline
		default:
			if _, ok := lookupAgent(view, target, agent); !ok {
				decisions[i] = admitNotExposed
			} else {
				decisions[i] = admitSubmit
			}
		}
	}
	return decisions
}

// CreateControlBatch 创建控制操作批次：两阶段持久化（包注释）。
//   - idemKey 非空时批次幂等：同键同内容返回原批次（Dup=true，不重提交任何
//     子任务）；同键不同内容 ErrBatchConflict；幂等预检 + 骨架事务内重检，
//     并发同键由单写串行化收敛。
//   - submittedBy 由调用方按会话身份传入（控制台固定 admin，SPEC 六.五）。
//   - 任一目标被跳过不影响整体成败；返回 202 批次视图（幂等命中 200）。
//
// 骨架落库后逐项回写失败（存储故障）时返回错误：已创建子任务已落库且各持派生
// 幂等键，批次停留 creating，重启恢复收尾，不复跑。
func (c *Core) CreateControlBatch(agent string, targets []string, input protocol.TaskInput, idemKey, submittedBy string) (*BatchView, error) {
	if agent == "" || len(targets) == 0 || submittedBy == "" {
		return nil, ErrInvalid
	}
	fingerprint := batchContentHash(agent, targets, input)

	// 幂等预检（快读路径；权威判定在骨架事务内重检）
	if idemKey != "" {
		view := c.st.View()
		if prev, ok := view.BatchIdems[idemKey]; ok {
			if b, ok := view.Batches[prev]; ok {
				if storedBatchHash(b) == fingerprint {
					v := batchViewOf(b, view)
					v.Dup = true
					return v, nil
				}
				return nil, ErrBatchConflict
			}
		}
	}

	// 阶段 1：冻结准入 + 骨架单事务落库
	decisions := c.freezeAdmission(c.st.View(), agent, targets)
	batchID, dup, err := c.createBatchSkeleton(agent, targets, input, idemKey, submittedBy, fingerprint, decisions)
	if err != nil {
		return nil, err
	}
	if dup {
		// 并发同键被骨架事务收敛为命中（快读路径未命中才走到这）
		view := c.st.View()
		if b, ok := view.Batches[batchID]; ok {
			v := batchViewOf(b, view)
			v.Dup = true
			return v, nil
		}
	}

	// 阶段 2：逐项创建 + 回写 + 收尾
	if err := c.fillBatchItems(batchID); err != nil {
		return nil, err
	}
	view := c.st.View()
	b, ok := view.Batches[batchID]
	if !ok {
		return nil, ErrInvalid // 不可达：本事务创建的骨架必然可见
	}
	return batchViewOf(b, view), nil
}

// createBatchSkeleton 阶段 1：幂等重检 + 骨架单事务落库（batch_id + targets
// 快照 + 冻结 skip 判定 + status=creating + idem→batch_id 映射）。
// 返回 (batch_id, dup)：dup=true 时 batch_id 为已存在批次 ID（同键同内容，
// 并发幂等命中，本事务未写入任何数据）。
func (c *Core) createBatchSkeleton(agent string, targets []string, input protocol.TaskInput, idemKey, submittedBy string, fingerprint string, decisions []admissionDecision) (string, bool, error) {
	now := c.now()
	batchID := "b-" + protocol.NewMessageID()
	var (
		dupID    string
		conflict bool
	)
	err := c.st.Update(func(st *store.State) error {
		if idemKey != "" {
			if prev, ok := st.BatchIdems[idemKey]; ok {
				if b, ok := st.Batches[prev]; ok && storedBatchHash(b) == fingerprint {
					dupID = prev
					return nil
				}
				conflict = true
				return nil
			}
		}
		items := make([]store.ControlItem, len(targets))
		for i, target := range targets {
			it := store.ControlItem{Target: target}
			switch decisions[i] {
			case admitOffline:
				it.Skipped = SkipOffline
			case admitNotExposed:
				it.Skipped = SkipNotExposed
			}
			items[i] = it
		}
		st.Batches[batchID] = store.ControlBatch{
			ID:          batchID,
			Agent:       agent,
			Input:       input.Clone(),
			SubmittedBy: submittedBy,
			IdemKey:     idemKey,
			Status:      BatchStatusCreating,
			CreatedAt:   now,
			Items:       items,
		}
		if idemKey != "" {
			st.BatchIdems[idemKey] = batchID
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if conflict {
		return "", false, ErrBatchConflict
	}
	if dupID != "" {
		return dupID, true, nil
	}
	return batchID, false, nil
}

// fillBatchItems 阶段 2：逐项创建（每项以 batch_id+序号派生子幂等键），每项
// 完成后单事务回写批次条目；全部条目已决后收尾置 complete。逐项 Submit 失败
// 仅记 error/skip 不阻塞其余；条目回写失败即中止（批次停留 creating，剩余项
// 留待重启恢复标 interrupted）。
func (c *Core) fillBatchItems(batchID string) error {
	b, ok := c.st.View().Batches[batchID]
	if !ok {
		return nil // 防御：骨架不可达即无事可做
	}
	for i := range b.Items {
		it := b.Items[i]
		if it.TaskID != "" || it.Skipped != "" || it.Error != "" {
			continue // 已决（冻结跳过项此前已记录）
		}
		ack, err := c.Submit(protocol.TaskSubmitRequest{
			TargetNode:  it.Target,
			TargetAgent: b.Agent,
			Input:       b.Input.Clone(),
			SubmittedBy: b.SubmittedBy,
			IdemKey:     batchChildIdem(batchID, i),
		}, "")
		switch {
		case err == nil:
			it.TaskID = ack.TaskID
		case isSkipTargetErr(err):
			it.Skipped = SkipNotExposed // 冻结判定后下线/取消暴露：以事务重检为准
		default:
			it.Error = submitErrCode(err)
		}
		if err := c.setBatchItem(batchID, i, it); err != nil {
			return fmt.Errorf("批次条目回写失败: %w", err)
		}
	}
	if err := c.completeBatch(batchID); err != nil {
		return fmt.Errorf("批次收尾失败: %w", err)
	}
	return nil
}

// setBatchItem 单事务回写批次条目（每项完成后落盘，崩溃窗口最小化到单项：
// 任务已落库、条目未回写时由 recoverBatches 按派生幂等键补记）。
func (c *Core) setBatchItem(batchID string, idx int, it store.ControlItem) error {
	return c.st.Update(func(st *store.State) error {
		b, ok := st.Batches[batchID]
		if !ok {
			return nil // 防御（批次无删除路径）
		}
		b.Items[idx] = it
		st.Batches[batchID] = b
		return nil
	})
}

// completeBatch 收尾单事务：全部条目已决 → status=complete。失败时批次停留
// creating（条目已全部回写，重启恢复幂等无害）。
func (c *Core) completeBatch(batchID string) error {
	return c.st.Update(func(st *store.State) error {
		b, ok := st.Batches[batchID]
		if !ok {
			return nil // 防御
		}
		b.Status = BatchStatusComplete
		st.Batches[batchID] = b
		return nil
	})
}

// recoverBatches 启动恢复（创建中断收尾）：status=creating 的批次已无在途
// 创建者（单进程内创建随进程消亡），逐项收尾——条目已决保持原样；空白条目按
// 派生幂等键查任务：命中补记 task_id（崩溃窗口「任务已落库、条目更新丢失」），
// 未命中标 skipped=interrupted（未及创建）。批次保持 creating：未全部完成，
// 且永不复跑（补记项与原任务同 ID，重复创建同键幂等命中）。
func (c *Core) recoverBatches() {
	err := c.st.Update(func(st *store.State) error {
		idems := make(map[string]string, len(st.Tasks))
		for id, t := range st.Tasks {
			if t.IdemKey != "" {
				idems[t.IdemKey] = id
			}
		}
		for bid, b := range st.Batches {
			if b.Status != BatchStatusCreating {
				continue
			}
			changed := false
			for i := range b.Items {
				it := b.Items[i]
				if it.TaskID != "" || it.Skipped != "" || it.Error != "" {
					continue
				}
				if id, ok := idems[batchChildIdem(bid, i)]; ok {
					it.TaskID = id
				} else {
					it.Skipped = SkipInterrupted
				}
				b.Items[i] = it
				changed = true
			}
			if changed {
				st.Batches[bid] = b
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[hub] 批次崩溃恢复失败（下轮重启重试）: %v", err)
	}
}

// GetControlBatch 查询批次（逐项任务状态为查询时点快照合并）。不存在返回 false。
func (c *Core) GetControlBatch(batchID string) (*BatchView, bool) {
	b, ok := c.st.View().Batches[batchID]
	if !ok {
		return nil, false
	}
	return batchViewOf(b, c.st.View()), true
}

// batchViewOf 快照记录 → HTTP 视图：提交成功项合并任务当前状态。
func batchViewOf(b store.ControlBatch, view *store.State) *BatchView {
	items := make([]BatchItem, len(b.Items))
	for i, it := range b.Items {
		items[i] = BatchItem{Target: it.Target, TaskID: it.TaskID, Skipped: it.Skipped, Error: it.Error}
		if it.TaskID != "" {
			if t, ok := view.Tasks[it.TaskID]; ok {
				items[i].Status = string(t.Status)
			}
		}
	}
	return &BatchView{BatchID: b.ID, Agent: b.Agent, Status: b.Status, CreatedAt: b.CreatedAt, Items: items}
}

// isSkipTargetErr 目标不可达类错误 → skipped:not_exposed（其余校验链失败为
// 逐项 error，不阻塞批次）。
func isSkipTargetErr(err error) bool {
	return errors.Is(err, ErrAgentNotExposed)
}
