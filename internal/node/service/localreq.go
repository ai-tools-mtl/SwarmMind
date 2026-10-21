// 本地请求关联（Loop 4，node 本地 API 的数据面）：node 本地 API 把提交/查询转成
// task_submit/task_query 帧，hub 以 submit_ack/task_status/err 帧应答（ReplyTo=
// 请求 mid）——本文件按 mid 维护在途请求表，把应答回给等待中的本地调用方。
//
// 注意：Service.HandleFrame 由 conn 读循环单 goroutine 串行调用，应答投递只在
// HandleFrame 内发生；等待方经 channel 接收，容量 1 + 非阻塞投递（重复应答丢弃）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// ErrHubUnreachable 与 hub 的会话未建立/不可达（本地 API → 503 hub_unreachable）。
var ErrHubUnreachable = errors.New("service: hub 不可达（未连接）")

// RemoteError hub 应答的错误帧（本地 API 按码映射 HTTP 状态）。
type RemoteError struct {
	Code string
	Msg  string
}

// Error 实现 error。
func (e *RemoteError) Error() string {
	return fmt.Sprintf("hub 拒绝（%s）: %s", e.Code, e.Msg)
}

// localTimeout 本地提交/查询等待 hub 应答的时限（调度节拍，非协议常量：hub 处理
// 为亚秒级，30s 覆盖深队列/慢盘；超时后调用方按「结果未知」处理——提交方可携
// 同一 idem_key 重试，SPEC 4.5）。
const localTimeout = 30 * time.Second

// registerWaiter 登记在途请求（mid → 应答通道）。
func (s *Service) registerWaiter(mid string) chan *protocol.Envelope {
	ch := make(chan *protocol.Envelope, 1)
	s.muPend.Lock()
	s.pending[mid] = ch
	s.muPend.Unlock()
	return ch
}

// removeWaiter 摘除在途请求（defer 用；幂等）。
func (s *Service) removeWaiter(mid string) {
	s.muPend.Lock()
	delete(s.pending, mid)
	s.muPend.Unlock()
}

// deliverLocal 按 ReplyTo 投递应答帧；无关联请求返回 false（调用方走原处理路径）。
func (s *Service) deliverLocal(env *protocol.Envelope) bool {
	if env.ReplyTo == "" {
		return false
	}
	s.muPend.Lock()
	ch := s.pending[env.ReplyTo]
	s.muPend.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- env:
	default:
	}
	return true
}

// SubmitLocal 本地提交（node 本地 API POST /api/v1/tasks 数据面）：转 task_submit
// 帧 → 等 submit_ack（成功/幂等命中）或 err 帧（校验链拒绝，码见 submitErrCode 对
// 应的协议码）。超时返回 context.DeadlineExceeded——调用方按结果未知处理。
func (s *Service) SubmitLocal(ctx context.Context, req protocol.TaskSubmitRequest) (*protocol.SubmitAckPayload, error) {
	mid := protocol.NewMessageID()
	ch := s.registerWaiter(mid)
	defer s.removeWaiter(mid)

	if err := s.sendWithID(protocol.MsgTaskSubmit, mid, req); err != nil {
		if errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed) {
			return nil, fmt.Errorf("%w: %v", ErrHubUnreachable, err)
		}
		return nil, fmt.Errorf("task_submit 发送失败: %w", err)
	}
	env, err := waitLocal(ctx, ch)
	if err != nil {
		return nil, err
	}
	switch env.Type {
	case protocol.MsgSubmitAck:
		var ack protocol.SubmitAckPayload
		if err := json.Unmarshal(env.Payload, &ack); err != nil {
			return nil, fmt.Errorf("submit_ack 载荷解析失败: %w", err)
		}
		return &ack, nil
	case protocol.MsgErr:
		return nil, remoteErr(env)
	}
	return nil, fmt.Errorf("submit 应答帧类型异常 type=%s", env.Type)
}

// QueryLocal 本地查询（node 本地 API GET /api/v1/tasks/{id} 数据面）：转
// task_query 帧 → 等 task_status（完整 Task）或 err(task_not_found)。
func (s *Service) QueryLocal(ctx context.Context, taskID string) (*protocol.Task, error) {
	mid := protocol.NewMessageID()
	ch := s.registerWaiter(mid)
	defer s.removeWaiter(mid)

	if err := s.sendWithID(protocol.MsgTaskQuery, mid, protocol.TaskRef{TaskID: taskID}); err != nil {
		if errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed) {
			return nil, fmt.Errorf("%w: %v", ErrHubUnreachable, err)
		}
		return nil, fmt.Errorf("task_query 发送失败: %w", err)
	}
	env, err := waitLocal(ctx, ch)
	if err != nil {
		return nil, err
	}
	switch env.Type {
	case protocol.MsgTaskStatus:
		var t protocol.Task
		if err := json.Unmarshal(env.Payload, &t); err != nil {
			return nil, fmt.Errorf("task_status 载荷解析失败: %w", err)
		}
		return &t, nil
	case protocol.MsgErr:
		return nil, remoteErr(env)
	}
	return nil, fmt.Errorf("query 应答帧类型异常 type=%s", env.Type)
}

// waitLocal 等待应答：本地超时 + ctx 取消。
func waitLocal(ctx context.Context, ch chan *protocol.Envelope) (*protocol.Envelope, error) {
	timer := time.NewTimer(localTimeout)
	defer timer.Stop()
	select {
	case env := <-ch:
		return env, nil
	case <-timer.C:
		return nil, fmt.Errorf("等待 hub 应答超时（%s）——结果未知，提交方可携同一 idem_key 重试", localTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sendWithID 以指定 mid 发帧（本地请求关联键）。
func (s *Service) sendWithID(typ, mid string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("载荷编码失败 type=%s: %w", typ, err)
	}
	return s.tr.Send("hub", &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      typ,
		MessageID: mid,
		To:        "hub",
		Payload:   b,
	})
}

// remoteErr 错误帧 → RemoteError（缺 ErrInfo 视为内部错误）。
func remoteErr(env *protocol.Envelope) error {
	if env.Err == nil {
		return &RemoteError{Code: "internal", Msg: "hub 未提供错误详情"}
	}
	return &RemoteError{Code: env.Err.Code, Msg: env.Err.Msg}
}
