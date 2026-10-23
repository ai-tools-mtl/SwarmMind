package core

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 事务内 benign 停止条件（dispatch 循环以之退出，非对外错误）。
var (
	errWindowFull = errors.New("core: 投递窗口已满")
	errNotQueued  = errors.New("core: 信箱无待投递任务")
)

// offerEnvelope 构造 task_offer 帧（完整 Task 载荷，SPEC 4.2/4.6 实测计量口径）。
// fence 为派发事务内捕获的该 node 当前注册代次（任务租约快照，P4/SPEC 4.11），
// node 回带结果时据此等值仲裁；0=未注册（membus 等价链路），omitempty 缺省，
// hub 侧按「缺省=当前 epoch」宽容。
func offerEnvelope(t *protocol.Task, fence int64) *protocol.Envelope {
	payload, err := json.Marshal(t)
	if err != nil {
		// Task 全字段可序列化，不可达；空载荷让 Encode 侧给出确定性结果
		payload = []byte("null")
	}
	return &protocol.Envelope{
		Version:    protocol.ProtocolVersion,
		Type:       protocol.MsgTaskOffer,
		MessageID:  protocol.NewMessageID(), // 每次推送新 mid；node 按任务 ID 幂等
		To:         t.TargetNode,
		FenceEpoch: fence,
		TraceID:    t.TraceID,
		Payload:    payload,
		ExpireAt:   t.ExpireAt,
	}
}

// dispatch 投递循环：节点在线且投递窗口（从 offered 起算，offered+acknowledged
// < 8，SPEC 4.6）有余量时持续出队补推。出队为「遍历取首个 eligible」（P1.5：
// workdir 互斥门控 + head-of-line 修复，SPEC 4.10）——队首任务互斥中时跳过，
// 同 node 异 workdir 任务照常派发。事件驱动（提交/上线/started/终态）+ 清扫
// 兜底，幂等。membus 同步分发下 Send 会嵌套执行 node 侧回链，循环内禁止持锁。
func (c *Core) dispatch(nodeID string) {
	if !c.isOnline(nodeID) {
		return
	}
	for {
		var (
			offered *protocol.Task
			fence   int64 // 派发时刻该 node 的注册代次（租约快照，事务内捕获）
		)
		err := c.st.Update(func(st *store.State) error {
			// 窗口在单写事务内重检（并发 dispatch 由单写串行化收敛）
			if inflightCount(st, nodeID) >= protocol.DeliveryWindow {
				return errWindowFull
			}
			t, ok := mailbox.DequeueWhere(st, nodeID, func(cand protocol.Task) bool {
				return workdirFree(st, cand) // workdir 互斥门控（P1.5）
			})
			if !ok {
				return errNotQueued
			}
			nt := t.Clone()
			nt.Status = protocol.StatusOffered
			nt.UpdatedAt = c.now() // offer 超时计时段（Sweep 据此回退）
			// 任务租约（P4/SPEC 4.11）：每次派发捕获该 node 当下注册代次并落库
			//（重派即换新租约），作为结果仲裁的等值基准；0=未注册代次（membus
			// 等价链路）不落库，回带缺失走缺省宽容。P2-6 写时复制：重派只刷新
			// 代次，拒收审计（Rejects/LastRejectedFence/LastAt）随任务累计保留，
			// 不得清零重建。
			if fence = st.Nodes[nodeID].Epoch; fence > 0 {
				ls := protocol.TaskLease{FenceEpoch: fence}
				if t.Lease != nil {
					ls.Rejects = t.Lease.Rejects
					ls.LastRejectedFence = t.Lease.LastRejectedFence
					ls.LastAt = t.Lease.LastAt
				}
				nt.Lease = &ls
			}
			st.Tasks[nt.ID] = nt
			acquireWorkdirLock(st, &nt) // 占用从 offered 起算（SPEC 4.10）
			offered = &nt
			return nil
		})
		if err != nil || offered == nil {
			return // 无任务/窗口满/事务失败
		}
		c.noteTask(offered)
		if err := c.tr.Send(nodeID, offerEnvelope(offered, fence)); err != nil {
			// 发送失败（对端竞态下线/关闭）：回退 submitted 并回队首，等下一次触发
			log.Printf("[hub] offer 发送失败 node=%s task=%s: %v", nodeID, offered.ID, err)
			c.revertOffer(offered)
			return
		}
	}
}

// revertOffer offer 发送失败回退：offered → submitted + 回队首（任务已持久化，
// 不丢）。workdir 互斥锁随回退保留（见 dispatch 的占用从 offered 起算；重推
// 幂等覆盖同键）——回退属投递层抖动而非 workdir 空闲，期间同 workdir 后续
// 任务仍应排队。
func (c *Core) revertOffer(t *protocol.Task) {
	var reverted *protocol.Task
	err := c.st.Update(func(st *store.State) error {
		cur, ok := st.Tasks[t.ID]
		if !ok || cur.Status != protocol.StatusOffered {
			return errNotQueued // 已被结果/回退抢占
		}
		nt := cur.Clone()
		nt.Status = protocol.StatusSubmitted
		nt.UpdatedAt = c.now()
		st.Tasks[nt.ID] = nt
		mailbox.RequeueFront(st, nt.TargetNode, &nt)
		reverted = &nt
		return nil
	})
	if err == nil && reverted != nil {
		c.noteTask(reverted)
	}
}

// inflightCount 投递窗口计数：该 node offered+acknowledged 状态任务数（SPEC 4.6；
// P1.5 复核口径 = 从 offered 起算——两个 acked 等并发槽期间窗口仍占用，槽一空
// 不会全量 spawn 击穿 workdir 互斥；running 不占窗口）。
func inflightCount(st *store.State, nodeID string) int {
	n := 0
	for _, t := range st.Tasks {
		if t.TargetNode != nodeID {
			continue
		}
		if t.Status == protocol.StatusOffered || t.Status == protocol.StatusAcknowledged {
			n++
		}
	}
	return n
}

// handleTaskAck 任务已入 node 账本（SPEC 4.3）：offered→acknowledged；迟到 ack
// （曾回退 submitted）按收敛表接受并同步信箱；重复/终态后忽略。
func (c *Core) handleTaskAck(env *protocol.Envelope) {
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[hub] task_ack 载荷解析失败 from=%s: %v", env.From, err)
		return
	}
	var acked *protocol.Task
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[ref.TaskID]
		if !ok || t.TargetNode != env.From {
			return nil // 未知任务/归属不符：忽略（任何 node 的帧只对其名下任务有效）
		}
		switch t.Status {
		case protocol.StatusOffered, protocol.StatusSubmitted:
			// offered 正常路径；submitted 为收敛表「task_ack 迟到（曾回退）→ 接受」
			nt := t.Clone()
			nt.Status = protocol.StatusAcknowledged
			nt.UpdatedAt = c.now()
			st.Tasks[nt.ID] = nt
			if t.Status == protocol.StatusSubmitted {
				mailbox.Remove(st, nt.TargetNode, nt.ID) // 离开 submitted 即释放信箱
			}
			acked = &nt
		case protocol.StatusAcknowledged:
			// 重复 ack（重复 offer 的重发）：静默
		default:
			// 收敛表：终态后 task_ack 忽略 + 告警
			log.Printf("[hub] task_ack 迟到忽略 task=%s status=%s from=%s", ref.TaskID, t.Status, env.From)
		}
		return nil
	})
	if err == nil && acked != nil {
		c.noteTask(acked)
	}
	// ack 不改变投递窗口占用（offered/acknowledged 同计），无补推动作
}

// handleTaskStarted 执行已开始（SPEC 4.3）：acknowledged→running；此后窗口释放
// 一格，触发补推（暂停的出队恢复）。其余状态忽略（状态机不允许的转移即丢弃）。
func (c *Core) handleTaskStarted(env *protocol.Envelope) {
	var ref protocol.TaskRef
	if err := json.Unmarshal(env.Payload, &ref); err != nil || ref.TaskID == "" {
		log.Printf("[hub] task_started 载荷解析失败 from=%s: %v", env.From, err)
		return
	}
	var started *protocol.Task
	err := c.st.Update(func(st *store.State) error {
		t, ok := st.Tasks[ref.TaskID]
		if !ok || t.TargetNode != env.From {
			return nil
		}
		if t.Status != protocol.StatusAcknowledged {
			// 收敛表：终态/submitted 后 started 忽略 + 告警
			log.Printf("[hub] task_started 忽略 task=%s status=%s from=%s", ref.TaskID, t.Status, env.From)
			return nil
		}
		nt := t.Clone()
		nt.Status = protocol.StatusRunning
		nt.UpdatedAt = c.now()
		st.Tasks[nt.ID] = nt
		started = &nt
		return nil
	})
	if err == nil && started != nil {
		c.noteTask(started)
		c.dispatch(env.From) // 窗口释放一格，恢复出队
	}
}
