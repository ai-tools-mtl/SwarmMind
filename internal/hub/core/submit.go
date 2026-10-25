package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// Submit 任务提交入口（HTTP 管理面与 node 间 task_submit 帧共用，SPEC 4.5/六）。
// replyTo 为空；node 帧提交方走 SubmitFrame 携带请求 mid。直连 core 的内部调用
// 方主体为空：绑定派发事件 principal 留空（仅限测试/内部，REST/帧入口恒带
// 服务端已验证主体）。
func (c *Core) Submit(req protocol.TaskSubmitRequest, submitterNode string) (*SubmitAck, error) {
	return c.submit(req, submitterNode, "", "", "")
}

// SubmitWithID 携预生成任务 ID 的提交入口（管理面「先登记后提交」专用，批 B
// 修复1）：群引用登记先于提交时任务 ID 必须已知，故调用方预生成 ID（两者引用
// 同一 ID），其余校验链与 Submit 完全一致；ID 与既有任务冲突在事务内拒绝
//（ErrInvalid，概率可忽略）。taskID 为空等价 Submit。node 帧路径不经此。
// actor 为服务端验证的鉴权主体（REST 层 authPrincipal 取得：admin 会话 /
// pat:<令牌名>）——绑定派发事件以独立字段落账（审查#4：不得取 SubmittedBy 兼
// 任主体，REST 提交的 SubmittedBy 固定 admin 会丢 PAT 令牌名；SubmittedBy 语义
// 不变，仅作显示与审计标签）。
func (c *Core) SubmitWithID(req protocol.TaskSubmitRequest, taskID, submitterNode, actor string) (*SubmitAck, error) {
	return c.submit(req, submitterNode, "", taskID, actor)
}

// SubmitFrame node 会话提交入口（task_submit 帧）：replyTo 为请求帧 mid，
// submit_ack/err 帧据此回关联（node 本地 API 的请求-响应关联，Loop 4）。
// 主体=会话身份 node_id（hub 按连接覆盖，服务端已验证，非请求自报）。
func (c *Core) SubmitFrame(req protocol.TaskSubmitRequest, submitterNode, replyTo string) (*SubmitAck, error) {
	return c.submit(req, submitterNode, replyTo, "", submitterNode)
}

// submit 校验链顺序固定（同键重试在信箱满时仍能取回原 task_id 的保证）：
//  1. 边界锁（方案 v2 D9）：node 会话提交强制 target == 提交方 node_id
//     （身份由会话层覆盖，可信），违规拒绝——防绕过 node localapi 直发帧
//     跨节点派单；hub HTTP 管理面（submitterNode 为空）不受此限；
//  2. 鉴权存在：提交方身份与目标字段非空（会话层 token 鉴权先于本包；HTTP 面
//     V0 绑回环，身份仅为调用方标记）；
//  2b. 绑定参数成组校验（审查#3）：work_ref/step_seq/prime_term 三参成组——
//     缺 work_ref 只带其余两参的请求按无效拒绝，不静默落为普通任务派发
//    （REST/帧入口无 MCP 层防护，core 公共入口统一把关）；
//  3. 目标 exposed=true，否则 ErrAgentNotExposed；
//  4. idem 查重——命中即返回，不做配额检查；
//  4b. 尝试去重证据只读预检（审查#1/#2，与绑定写入拆分）：任务+上游已清理的
//     同键重放在此回执补拉即返回——先于依赖准入与容量检查，不因
//     invalid_dependency/mailbox_full/storage_full 挡在回执之外；证据含绑定
//     事务内落盘的提交内容指纹（含提交主体=身份边界），换内容/换 work/换步骤/
//     换主体同键 → conflict，仅群引用不同 → 按新提交（同任务级口径）；
//  5. 依赖准入预检（事务内按快照重检——上游在两检之间被清理即拒绝）；
//  6. 构造任务 + 序列化体积实测预检：按最终 TaskOffer 完整信封编码计字节数；
//  7. 信箱配额/字节预算 + 存储全局预算预检；
//  8. 单写事务持久化（事务内全部重检，依赖准入以事务内快照为准）→ SubmitAck。
//     工作单绑定在全部校验后原子盖章；盖章后按**盖章体**重检体积与容量
//    （审查#7/#8：绑定字段计入口径，失败以 mutate 错误整体拒绝=绑定回滚）。
//
// submitterNode 非 "" 表示 node 会话提交：SubmittedBy 被覆盖为该 node_id
//（SPEC 4.9），确认与失败经帧回给该 node（submit_ack/err 帧，ReplyTo=replyTo），
// 终态经 result_event 推送。actor 为服务端验证主体（绑定事件 principal 落账，
// 与自报 prime_term/by 分开——RFC 六.2）。
func (c *Core) submit(req protocol.TaskSubmitRequest, submitterNode, replyTo, taskIDHint, actor string) (*SubmitAck, error) {
	// 1. node 会话边界锁（合并：裁剪 D9）：目标收敛为提交方本机（先于一切校验，违规不发 offer）
	if submitterNode != "" && req.TargetNode != submitterNode {
		err := ErrInvalid
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}
	if submitterNode != "" {
		req.SubmittedBy = submitterNode // hub 覆盖为该 node_id
	}

	// 2. 鉴权存在
	if req.TargetNode == "" || req.TargetAgent == "" || req.SubmittedBy == "" {
		err := ErrInvalid
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}
	// 2b. 绑定参数成组校验（审查#3）：三参成组，缺 work_ref 的半参请求拒绝
	if req.WorkRef == "" && (req.StepSeq != 0 || req.PrimeTerm != "") {
		err := fmt.Errorf("%w: 绑定派发须 work_ref/step_seq/prime_term 三参成组（缺 work_ref）", ErrInvalid)
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}
	// 依赖项规范化（TrimSpace）：先于 idem 查重执行，保证重试指纹稳定
	req.DependsOn = trimDeps(req.DependsOn)

	view := c.st.View()

	// 3. 目标存在且 exposed
	if _, ok := lookupAgent(view, req.TargetNode, req.TargetAgent); !ok {
		err := ErrAgentNotExposed
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}

	// 4. idem 查重（命中即返回，不做配额检查）；活任务 dup 响应带绑定回执
	//（审查#10：与首次提交/task_gone 响应同形，调用方免二次查询）
	if req.IdemKey != "" {
		id, dup, err := mailbox.IdemCheck(view, req)
		if err != nil {
			c.ackSubmitter(submitterNode, replyTo, nil, err) // ErrConflict
			return nil, err
		}
		if dup {
			t := view.Tasks[id]
			ack := &SubmitAck{TaskID: t.ID, TraceID: t.TraceID, Dup: true, Work: workDupInfo(view, req, t.ID)}
			c.ackSubmitter(submitterNode, replyTo, ack, nil)
			return ack, nil
		}
		// 4b. 尝试去重证据只读预检（审查#2）：回执补拉先于依赖准入/容量检查
		info, tid, hit, err := lookupAttemptEvidence(view, req)
		if err != nil {
			c.ackSubmitter(submitterNode, replyTo, nil, err)
			return nil, err
		}
		if hit {
			ack := &SubmitAck{TaskID: tid, TraceID: view.Tasks[tid].TraceID, Dup: true, Work: info}
			c.ackSubmitter(submitterNode, replyTo, ack, nil)
			return ack, nil
		}
	}

	// 5. 依赖准入预检（事务内按快照重检——上游在两检之间被清理即拒绝）
	if _, err := validateDepends(view, req); err != nil {
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}

	// 6. 构造任务 + 序列化体积实测预检（完整 TaskOffer 信封编码计字节）
	task := c.newTask(req, taskIDHint)
	if err := checkOfferSize(&task); err != nil {
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}
	size := mailbox.TaskBytes(&task)

	// 7. 信箱配额/字节预算 + 存储全局预算预检
	if err := c.checkCapacity(view, req.TargetNode, size); err != nil {
		c.ackSubmitter(submitterNode, replyTo, nil, err)
		return nil, err
	}

	// 8. 单写事务持久化（事务内全部重检；并发同键在此收敛为 dup）。
	// 工作单绑定（P5 W2）在全部校验之后、任务落库前：dup/去重证据命中已短路于
	// 绑定（重试回放不新增尝试）；绑定一旦发生，其后任何失败（盖章后体积复查、
	// 盖章体容量重检、信箱入队）以 mutate 错误整体拒绝事务（错误返回即回滚，
	// 绑定失败不发布；上方前置校验用 txnErr+nil 是因失败点尚无任何变更，提交
	// 空副本无害）。
	var (
		txnDup     *SubmitAck
		txnBind    *WorkBindInfo
		txnDedupID string // 尝试去重命中：原任务已清理，不落新任务
		txnErr     error
	)
	err := c.st.Update(func(st *store.State) error {
		if _, ok := lookupAgent(st, req.TargetNode, req.TargetAgent); !ok {
			txnErr = ErrAgentNotExposed
			return nil
		}
		if req.IdemKey != "" {
			id, dup, err := mailbox.IdemCheck(st, req)
			if err != nil {
				txnErr = err
				return nil
			}
			if dup {
				t := st.Tasks[id]
				txnDup = &SubmitAck{TaskID: t.ID, TraceID: t.TraceID, Dup: true, Work: workDupInfo(st, req, t.ID)}
				return nil
			}
			// 去重证据事务内重检（预检后并发落库的同键重放在此收敛为回执）
			info, tid, hit, err := lookupAttemptEvidence(st, req)
			if err != nil {
				txnErr = err
				return nil
			}
			if hit {
				txnBind = info
				txnDedupID = tid
				return nil
			}
		}
		if err := checkOfferSize(&task); err != nil {
			txnErr = err
			return nil
		}
		if err := c.checkCapacity(st, req.TargetNode, size); err != nil {
			txnErr = err
			return nil
		}
		if _, exists := st.Tasks[task.ID]; exists {
			// 预生成 ID 撞上既有任务（SubmitWithID 路径，概率可忽略）：防御性拒绝，
			// 绝不覆盖落库
			txnErr = ErrInvalid
			return nil
		}
		// 依赖准入重检（上游被清理/并发变更以事务内快照为准）+ 依赖门控初评：
		// 依赖未满足即落 blocked_reason，保持 submitted 不投递（P4.3，状态机不变）
		if _, err := validateDepends(st, req); err != nil {
			txnErr = err
			return nil
		}
		task.BlockedReason = evalDeps(st, req.DependsOn).blockedReason()
		if req.WorkRef != "" {
			info, err := c.bindWorkSubmit(st, &task, req, actor)
			if err != nil {
				return err
			}
			txnBind = info
			// 盖章后按盖章体重检（绑定字段计入口径，审查#7/#8）：任何失败以
			// mutate 错误整体拒绝（含绑定回写回滚）
			if err := checkOfferSize(&task); err != nil {
				return err
			}
			if err := c.checkCapacity(st, req.TargetNode, mailbox.TaskBytes(&task)); err != nil {
				return err
			}
		}
		st.Tasks[task.ID] = task
		return mailbox.Enqueue(st, task.TargetNode, &task)
	})
	if err != nil && txnErr == nil {
		txnErr = err // mutate 返回错误（绑定拒绝/盖章后复查失败/信箱入队拒绝），事务已整体丢弃
	}
	if txnErr != nil {
		c.ackSubmitter(submitterNode, replyTo, nil, txnErr)
		return nil, txnErr
	}
	if txnDup != nil {
		c.ackSubmitter(submitterNode, replyTo, txnDup, nil)
		return txnDup, nil
	}
	if txnDedupID != "" {
		// 尝试去重证据命中（RFC「不推迟项：去重证据」）：不重新执行，dup 语义
		// 返回原 task_id 与原尝试定位/现终局（WorkBindInfo.TaskGone 标注原任务
		// 是否已清理；原任务仍在的防御形态补 TraceID）
		ack := &SubmitAck{TaskID: txnDedupID, Dup: true, Work: txnBind}
		if t, ok := c.st.View().Tasks[txnDedupID]; ok {
			ack.TraceID = t.TraceID
		}
		c.ackSubmitter(submitterNode, replyTo, ack, nil)
		return ack, nil
	}

	c.noteTask(&task)
	ack := &SubmitAck{TaskID: task.ID, TraceID: task.TraceID, Work: txnBind}
	c.ackSubmitter(submitterNode, replyTo, ack, nil)
	c.dispatch(task.TargetNode)
	return ack, nil
}

// handleTaskSubmit node 本机派单帧入口（task_submit：hub 鉴权后走同 HTTP 等价
// 流程；node 会话目标收敛为本机，边界锁见 submit 步骤 1）。
func (c *Core) handleTaskSubmit(env *protocol.Envelope) {
	var req protocol.TaskSubmitRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		log.Printf("[hub] task_submit 载荷解析失败 from=%s: %v", env.From, err)
		return
	}
	// Submit 已向 node 提交方回 submit_ack/err 帧（成败均帧化，ReplyTo=请求 mid）
	_, _ = c.SubmitFrame(req, env.From, env.MessageID)
}

// ackSubmitter 向 node 会话提交方回帧：成功为 submit_ack，失败为 err 帧，
// ReplyTo=提交请求 mid（HTTP 提交方 replyTo 为空——靠返回值与 GET 轮询，不发帧）。
func (c *Core) ackSubmitter(submitterNode, replyTo string, ack *SubmitAck, err error) {
	if submitterNode == "" {
		return
	}
	if err == nil {
		payload := protocol.SubmitAckPayload{TaskID: ack.TaskID, TraceID: ack.TraceID, Dup: ack.Dup}
		_ = c.sendFrame(submitterNode, protocol.MsgSubmitAck, replyTo, payload, nil)
		return
	}
	code := submitErrCode(err)
	_ = c.sendFrame(submitterNode, protocol.MsgErr, replyTo, nil, &protocol.ErrInfo{Code: code, Msg: err.Error()})
}

// submitErrCode 提交失败错误 → 协议错误码（SPEC 4.6 CodeXxx；"conflict" 对应
// SPEC 4.5 同键不同内容的 409 语义，"invalid" 对应请求字段缺失与 node 会话
// 跨节点目标违规——边界锁，方案 v2 D9）。
func submitErrCode(err error) string {
	switch {
	case errors.Is(err, ErrAgentNotExposed):
		return protocol.CodeAgentNotExposed
	case errors.Is(err, mailbox.ErrMailboxFull):
		return protocol.CodeMailboxFull
	case errors.Is(err, ErrStorageFull):
		return protocol.CodeStorageFull
	case errors.Is(err, protocol.ErrFrameTooLarge):
		return protocol.CodeFrameTooLarge
	case errors.Is(err, mailbox.ErrConflict):
		return "conflict"
	case errors.Is(err, ErrInvalidDeps):
		return protocol.CodeInvalidDependency
	case errors.Is(err, ErrWorkNotFound):
		return "work_not_found"
	case errors.Is(err, ErrWorkClosed):
		return "work_closed"
	case errors.Is(err, ErrWorkTermConflict):
		return "work_term_conflict"
	case errors.Is(err, ErrWorkNoTerm):
		return "work_no_term"
	case errors.Is(err, ErrWorkStepLocked):
		return "work_step_locked"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	}
	return "internal"
}

// trimDeps 依赖项规范化：逐项 TrimSpace（提交入口先于 idem 查重执行，保证
// 重试指纹稳定；空项留待 validateDepends 拒绝）。
func trimDeps(deps []string) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, len(deps))
	for i, k := range deps {
		out[i] = strings.TrimSpace(k)
	}
	return out
}

// newTask 按提交请求构造任务（ID/时间/身份 hub 生成，SPEC 4.9）。idHint 非空
// 时沿用调用方预生成的任务 ID（SubmitWithID「先登记后提交」路径）。声明依赖时
// Input 过 EffectiveInput 生效形态（stdin 尾部附依赖清单，与幂等指纹同一变换；
// 产物自动挂载为 L2 第二子批，本批先附上游 idem 供 agent 自取）。
func (c *Core) newTask(req protocol.TaskSubmitRequest, idHint string) protocol.Task {
	now := c.now()
	id := idHint
	if id == "" {
		id = protocol.NewTaskID()
	}
	return protocol.Task{
		ID:          id,
		TraceID:     protocol.NewTraceID(),
		TargetNode:  req.TargetNode,
		TargetAgent: req.TargetAgent,
		Input:       protocol.EffectiveInput(req.Input, req.DependsOn),
		SubmittedBy: req.SubmittedBy,
		IdemKey:     req.IdemKey,
		GroupRef:    req.GroupRef,
		DependsOn:   append([]string(nil), req.DependsOn...),
		Status:      protocol.StatusSubmitted,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpireAt:    now.Add(protocol.TaskRetention),
	}
}

// checkCapacity 容量预检/重检：信箱条数与字节（经 mailbox.Usage 计量）+ 存储
// 全局预算（经字节缓存合计）。SPEC 4.5 链序中位于体积实测之后、持久化之前。
func (c *Core) checkCapacity(st *store.State, nodeID string, incoming int64) error {
	count, bytes := mailbox.Usage(st, nodeID)
	if count+1 > protocol.MailboxQuota || bytes+incoming > protocol.MailboxByteBudget {
		return mailbox.ErrMailboxFull
	}
	if c.totalBytes()+incoming > protocol.StorageBudget {
		return ErrStorageFull
	}
	return nil
}

// checkOfferSize 序列化体积实测（SPEC 4.6：构建含 ID/时间/身份的完整信封后
// marshal 计字节数，超 1MiB 拒收，不用余量估算）。fence 按代次位数上限量级
// 计入（P4/SPEC 4.11：派发帧携带租约快照，实测取保守上界）。
func checkOfferSize(t *protocol.Task) error {
	_, err := protocol.Encode(offerEnvelope(t, 1<<40))
	return err
}

// lookupAgent 目标 node 已注册且 agent exposed=true 才可达（SPEC 六：未接入 →
// agent_not_exposed，node 不存在同口径拒绝）。
func lookupAgent(st *store.State, nodeID, agent string) (protocol.AgentCard, bool) {
	rec, ok := st.Nodes[nodeID]
	if !ok {
		return protocol.AgentCard{}, false
	}
	for _, card := range rec.Agents {
		if card.Name == agent && card.Exposed {
			return card, true
		}
	}
	return protocol.AgentCard{}, false
}
