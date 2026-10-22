// 本地直执行提交（方案 3.2 双路径执行链路，2026-10-22）：node 本机入口
// （localapi 网页/MCP）的派单不再经 hub 帧往返——本地构造任务、账本入账
// （origin=local）、runner 执行、结果回写本地账本。组不组网行为一致；
// hub 挂了/断网本机照常干活（离线自治）。hub 下发任务（origin=hub）仍走
// task_offer 现有链路（service.go），两路径共享 runner/ledger/workdir 互斥。
//
// 旧 SubmitLocal/QueryLocal 经 hub 帧往返路径已删除（localreq.go 不复存在）：
// 本地任务 hub 不可见，帧查询必然 task_not_found；查询改由账本视图直出
//（ledger.TaskView）。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// RemoteError 本地提交校验链拒绝（localapi 按码映射 HTTP 状态；与 hub 管理面
// 错误码同源：agent_not_found / conflict / invalid）。
type RemoteError struct {
	Code string
	Msg  string
}

// Error 实现 error。
func (e *RemoteError) Error() string {
	return fmt.Sprintf("本地校验拒绝（%s）: %s", e.Code, e.Msg)
}

// SubmitLocalDirect 本地直执行提交（localapi POST /api/v1/tasks 数据面）：
// 校验 → 本地构造任务 → 幂等查重+入账（同一账本事务内，返工 #3）→ 交本地调度
// 执行。成功返回 SubmitAck 同型载荷（Dup=true 表示幂等命中原任务）。
//
// 校验链（对齐 hub 提交链语义，边界锁沿用裁剪方案 D8）：
//  1. 目标字段非空（target_node 非本机已在 localapi 拒绝，不经此）；
//  2. 目标 agent 必须在本机配置（Runners）——非本机 agent 拒绝（D8 校验语义）；
//     已配置但 exposed=false 不拦：exposed 是对 hub 准入的开关，本机主人派单
//     不受其约束（主权在本机）；
//  3. 幂等查重与入账原子化（账本 AcceptOriginIdempotent，单写事务内重检三态）：
//     同键同内容 → dup 返回原任务；同键仅群引用不同 → 新任务（与 hub 批 B 口径
//     一致）；同键其余内容不同 → conflict。查重不在事务外预扫——并发同键提交
//     在写 goroutine 串行域内收敛，恰有一个新任务，其余 dup/conflict；
//  4. 入账（origin=local）成功后才执行（账本为准，先写后见）。
func (s *Service) SubmitLocalDirect(req protocol.TaskSubmitRequest) (*protocol.SubmitAckPayload, error) {
	// 1. 目标字段存在
	if req.TargetNode == "" || req.TargetAgent == "" || req.SubmittedBy == "" {
		return nil, &RemoteError{Code: "invalid", Msg: "target_node / target_agent / submitted_by 均为必填"}
	}
	// 2. 目标必须在本机配置（D8 语义：本机入口只派本机 agent；热重载换表读侧）
	if _, ok := s.runnerFor(req.TargetAgent); !ok {
		return nil, &RemoteError{Code: protocol.CodeAgentNotFound,
			Msg: fmt.Sprintf("agent %q 不在本机配置中（本地派单目标只能本机 agent；配置漂移请更新 node.yaml）", req.TargetAgent)}
	}
	// 3. 本地构造任务（镜像 hub newTask：ID/时间本机生成，ExpireAt=+7d）
	now := protocol.NowUTC()
	task := protocol.Task{
		ID:          protocol.NewTaskID(),
		TraceID:     protocol.NewTraceID(),
		TargetNode:  req.TargetNode,
		TargetAgent: req.TargetAgent,
		Input:       req.Input.Clone(),
		SubmittedBy: req.SubmittedBy,
		IdemKey:     req.IdemKey,
		GroupRef:    req.GroupRef,
		Status:      protocol.StatusSubmitted,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpireAt:    now.Add(protocol.TaskRetention),
	}
	// 4. 幂等查重+入账（同一账本事务，TOCTOU 消除——返工 #3）
	if req.IdemKey != "" {
		want, wantBase := localContentHash(req), localBaseContentHash(req)
		dupTask, dup, err := s.cfg.Ledger.AcceptOriginIdempotent(task, ledger.OriginLocal, req.IdemKey,
			func(existing protocol.Task) ledger.IdemMatch {
				er := storedRequest(existing)
				switch {
				case localContentHash(er) == want:
					return ledger.IdemExact
				case localBaseContentHash(er) == wantBase:
					return ledger.IdemOtherGroup // 仅群引用不同（换群重试）→ 新任务
				default:
					return ledger.IdemDifferent
				}
			})
		if errors.Is(err, ledger.ErrIdemConflict) {
			return nil, &RemoteError{Code: "conflict",
				Msg: fmt.Sprintf("幂等键 %q 已被内容不同的本机任务占用（同键不同内容）", req.IdemKey)}
		}
		if err != nil {
			return nil, fmt.Errorf("本地入账失败: %w", err)
		}
		if dup {
			return &protocol.SubmitAckPayload{TaskID: dupTask.ID, TraceID: dupTask.TraceID, Dup: true}, nil
		}
	} else if err := s.cfg.Ledger.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		return nil, fmt.Errorf("本地入账失败: %w", err)
	}
	s.execute(task)
	return &protocol.SubmitAckPayload{TaskID: task.ID, TraceID: task.TraceID}, nil
}

// storedRequest 由账本条目任务还原提交请求视图（幂等指纹比对用；字段与
// localContent/localGroupContent 指纹载体严格对应）。
func storedRequest(t protocol.Task) protocol.TaskSubmitRequest {
	return protocol.TaskSubmitRequest{
		TargetNode: t.TargetNode, TargetAgent: t.TargetAgent,
		Input: t.Input, SubmittedBy: t.SubmittedBy, GroupRef: t.GroupRef,
	}
}

// localContent 全量指纹载体（字段与 json tag 与 protocol 严格一致，指纹跨版本稳定）。
type localContent struct {
	TargetNode  string             `json:"target_node"`
	TargetAgent string             `json:"target_agent"`
	Input       protocol.TaskInput `json:"input"`
	SubmittedBy string             `json:"submitted_by"`
}

// localGroupContent 全量指纹 = 协议内容 + 群引用（omitempty：空群引用编码与
// 纯 content 一致，新旧格式互操作兼容）。
type localGroupContent struct {
	localContent
	GroupRef string `json:"group_ref,omitempty"`
}

// localContentHash 全量指纹（canonical JSON 的 SHA-256 hex）。
func localContentHash(req protocol.TaskSubmitRequest) string {
	return localHashJSON(localGroupContent{
		localContent: localContent{
			TargetNode: req.TargetNode, TargetAgent: req.TargetAgent,
			Input: req.Input.Clone(), SubmittedBy: req.SubmittedBy,
		},
		GroupRef: req.GroupRef,
	})
}

// localBaseContentHash 协议内容指纹（不含群引用，换群判定基准）。
func localBaseContentHash(req protocol.TaskSubmitRequest) string {
	return localHashJSON(localContent{
		TargetNode: req.TargetNode, TargetAgent: req.TargetAgent,
		Input: req.Input.Clone(), SubmittedBy: req.SubmittedBy,
	})
}

// localHashJSON canonical JSON → SHA-256 hex。
func localHashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "" // 纯标量+slice+map 结构，不可达；空串按「不同内容」处理
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
