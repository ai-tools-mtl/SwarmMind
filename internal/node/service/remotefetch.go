// 跨机取回的帧通道（P4④第二子轮 / SPEC 4.12）：inputs.RemoteFetch 的 service
// 实现——dep_resolve（依赖解析）与 artifact_fetch（产物取回）各一次尝试，应答
// 经在途请求表按 ReplyTo 关联回投。重试节奏、失败分类的消费在 inputs.RemoteResolver；
// 本文件只做帧收发与错误码归类。
//
// 并发与重入（membus 同步链关键）：在途表登记**必须先于 Send**——membus 的
// Send 在调用方 goroutine 内同步执行对端 handler，hub 中继链（hub→源 node→hub
// →本 node）可能在本调用返回前就把应答送进 HandleFrame，后注册会丢应答。
// 应答通道容量 1 + 非阻塞投递，重复/迟到帧安全丢弃。
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	"github.com/mtl802/swarmmind/internal/node/inputs"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// remoteInputs inputs.RemoteFetch 的帧通道实现（Config.RemoteInputs 注入；仅
// networked 模式装配——standalone 无 hub，跨机取回语义不成立）。
type remoteInputs struct {
	s *Service
}

// awaitReply 单次在途请求：登记 mid → 发帧 → 等待应答（ctx 预算）。发送失败
// 与等待超时/取消均按可重试归类；应答帧原样返回（分类见 remoteErrFromFrame）。
func (r remoteInputs) awaitReply(ctx context.Context, typ string, payload any) (*protocol.Envelope, error) {
	mid := protocol.NewMessageID()
	ch := r.s.registerRemoteWait(mid)
	defer r.s.removeRemoteWait(mid)
	if err := r.s.sendWithID(typ, mid, payload); err != nil {
		return nil, fmt.Errorf("%w: 帧发送失败: %v", inputs.ErrRemoteRetryable, err)
	}
	select {
	case env := <-ch:
		return env, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", inputs.ErrRemoteRetryable, ctx.Err())
	}
}

// ResolveDep 依赖解析一次尝试（dep_resolve → task_status/err）。
func (r remoteInputs) ResolveDep(ctx context.Context, idem string) (*inputs.DepTarget, error) {
	env, err := r.awaitReply(ctx, protocol.MsgDepResolve, protocol.DepResolvePayload{Idem: idem})
	if err != nil {
		return nil, err
	}
	if env.Type == protocol.MsgTaskStatus {
		var t protocol.Task
		if err := json.Unmarshal(env.Payload, &t); err != nil || t.ID == "" {
			return nil, fmt.Errorf("%w: task_status 载荷解析失败: %v", inputs.ErrRemoteRetryable, err)
		}
		tgt := &inputs.DepTarget{TaskID: t.ID, TargetNode: t.TargetNode}
		if t.Result != nil {
			tgt.Artifacts = append([]protocol.Artifact(nil), t.Result.Artifacts...)
		}
		return tgt, nil
	}
	return nil, remoteErrFromFrame(env, "依赖解析")
}

// FetchArtifact 产物取回一次尝试（artifact_fetch → artifact_data/err）；返回前
// 校验 base64 解码与登记大小（SHA 终检归 RemoteResolver——它持登记清单）。
func (r remoteInputs) FetchArtifact(ctx context.Context, taskID string, index int, meta protocol.Artifact) ([]byte, error) {
	env, err := r.awaitReply(ctx, protocol.MsgArtifactFetch,
		protocol.ArtifactFetchPayload{TaskID: taskID, Index: index})
	if err != nil {
		return nil, err
	}
	if env.Type != protocol.MsgArtifactData {
		return nil, remoteErrFromFrame(env, "产物取回")
	}
	var p protocol.ArtifactDataPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("%w: artifact_data 载荷解析失败: %v", inputs.ErrRemoteRetryable, err)
	}
	data, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil {
		return nil, fmt.Errorf("%w: artifact_data base64 解码失败: %v", inputs.ErrRemoteMismatch, err)
	}
	if int64(len(data)) != meta.Size {
		return nil, fmt.Errorf("%w: 产物 %s（登记 %d 字节，实得 %d）",
			inputs.ErrRemoteMismatch, meta.Path, meta.Size, len(data))
	}
	return data, nil
}

// remoteErrFromFrame err 帧 → 三类哨兵（供 RemoteResolver 决定重试或永久失败）：
// task_not_found/artifact_not_found/forbidden/artifact_mismatch = 永久（已清理/
// 不存在/越权/完整性）；node_offline/fetch_timeout 及未知码 = 可重试（暂不可取，
// 预算兜底）。
func remoteErrFromFrame(env *protocol.Envelope, what string) error {
	code, msg := "internal", ""
	if env.Err != nil {
		code, msg = env.Err.Code, env.Err.Msg
	}
	switch code {
	case protocol.CodeTaskNotFound, protocol.CodeArtifactNotFound:
		return fmt.Errorf("%w: %s（%s）", inputs.ErrRemoteGone, msg, what)
	case protocol.CodeForbidden:
		return fmt.Errorf("%w: %s（%s 被拒绝）", inputs.ErrRemoteGone, msg, what)
	case protocol.CodeArtifactMismatch:
		return fmt.Errorf("%w: %s", inputs.ErrRemoteMismatch, msg)
	default:
		return fmt.Errorf("%w: %s 失败 code=%s msg=%s", inputs.ErrRemoteRetryable, what, code, msg)
	}
}

// ---------- 在途请求表（应答关联） ----------

// registerRemoteWait 登记在途请求（mid → 容量 1 应答通道）。
func (s *Service) registerRemoteWait(mid string) chan *protocol.Envelope {
	ch := make(chan *protocol.Envelope, 1)
	s.muRemote.Lock()
	s.remoteWaits[mid] = ch
	s.muRemote.Unlock()
	return ch
}

// removeRemoteWait 注销在途请求（defer 兜底；超时/取消后的迟到应答落空）。
func (s *Service) removeRemoteWait(mid string) {
	s.muRemote.Lock()
	delete(s.remoteWaits, mid)
	s.muRemote.Unlock()
}

// deliverRemoteReply 应答回投：ReplyTo 命中在途请求即投递（非阻塞，容量 1）。
// 返回是否命中——命中则帧已消费，调用方不再走既有处理分支；未命中（含缺
// ReplyTo）返回 false，帧按原语义处理（result_conflict 等 err 帧不受影响）。
func (s *Service) deliverRemoteReply(env *protocol.Envelope) bool {
	if env.ReplyTo == "" {
		return false
	}
	s.muRemote.Lock()
	ch := s.remoteWaits[env.ReplyTo]
	s.muRemote.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- env:
	default:
		log.Printf("[node] 迟到/重复应答丢弃 reply=%s type=%s", env.ReplyTo, env.Type)
	}
	return true
}
