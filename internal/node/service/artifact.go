// 产物取回（Loop 4，SPEC 4.2/4.8）：hub 中继 artifact_fetch 帧 → 本文件读本地
// 产物回 artifact_data。node 校验 SHA-256 与登记一致（SPEC 4.8），不一致回
// err(artifact_mismatch)；任务/产物不存在回 err(artifact_not_found)。
package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/mtl802/swarmmind/internal/node/inputs"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 本机取回哨兵错误（FetchArtifactLocal 返回；localapi 按 HTTP 状态映射：
// 404 task_not_found / 404 artifact_not_found / 409 artifact_mismatch）。
var (
	ErrTaskNotFound     = errors.New("任务不存在或已过保留期")
	ErrArtifactNotFound = errors.New("产物不存在（任务无终态结果/下标越界/文件丢失）")
	ErrArtifactMismatch = errors.New("产物内容与登记不一致（SHA-256/大小）")
)

// FetchArtifactLocal 本机直接取回产物内容（localapi GET
// /api/v1/tasks/{id}/artifacts/{index} 数据面，方案 3.4 node MCP
// swarmmind_artifact 的后端）：本地账本定位终态 → ArtifactRoots 解析本机路径 →
// SHA-256 校验 → 返回内容。不经 hub 中继帧（离线自治）。
func (s *Service) FetchArtifactLocal(taskID string, index int) (protocol.Artifact, []byte, error) {
	e, ok := s.cfg.Ledger.Get(taskID)
	if !ok {
		return protocol.Artifact{}, nil, ErrTaskNotFound
	}
	if e.Result == nil || index < 0 || index >= len(e.Result.Artifacts) {
		return protocol.Artifact{}, nil, ErrArtifactNotFound
	}
	meta := e.Result.Artifacts[index]
	root, ok := s.artifactRootFor(e.Task.TargetAgent)
	if !ok {
		// 配置漂移（agent 已从本机移除）：产物根不可知，按不存在处理
		return protocol.Artifact{}, nil, ErrArtifactNotFound
	}
	data, err := readArtifactFile(root, meta)
	if err != nil {
		return meta, nil, artifactSentinel(err)
	}
	return meta, data, nil
}

// artifactSentinel 取回失败 → 包内哨兵错误（localapi 映射 HTTP 状态）。
func artifactSentinel(err error) error {
	if ae, ok := err.(*artifactError); ok && ae.code == protocol.CodeArtifactMismatch {
		return ErrArtifactMismatch
	}
	return ErrArtifactNotFound
}

// handleArtifactFetch 执行 node 侧取回：账本定位任务终态 → 解析本机绝对路径
// （AgentRoots[TargetAgent] = workdir）→ 校验哈希 → base64 回帧。全程不阻塞执行
// 调度（HandleFrame 串行调用；读文件 ≤640KiB，亚秒级）。
func (s *Service) handleArtifactFetch(env *protocol.Envelope) {
	var p protocol.ArtifactFetchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.TaskID == "" {
		log.Printf("[node] artifact_fetch 载荷解析失败: %v", err)
		s.sendErrReply(env.MessageID, "invalid", "artifact_fetch 载荷非法")
		return
	}
	e, ok := s.cfg.Ledger.Get(p.TaskID)
	if !ok || e.Result == nil || p.Index < 0 || p.Index >= len(e.Result.Artifacts) {
		s.sendErrReply(env.MessageID, protocol.CodeArtifactNotFound, "任务不存在、无终态结果或产物下标越界")
		return
	}
	meta := e.Result.Artifacts[p.Index]

	root, ok := s.artifactRootFor(e.Task.TargetAgent)
	if !ok {
		// 配置漂移（agent 已从本机移除）：产物根不可知，按不存在处理
		s.sendErrReply(env.MessageID, protocol.CodeArtifactNotFound, "agent 不在本机配置，无法定位产物")
		return
	}
	data, err := readArtifactFile(root, meta)
	if err != nil {
		log.Printf("[node] 产物取回失败 task=%s index=%d: %v", p.TaskID, p.Index, err)
		s.sendErrReply(env.MessageID, artifactErrCode(err), err.Error())
		return
	}
	s.sendReplyTo(protocol.MsgArtifactData, env.MessageID, protocol.ArtifactDataPayload{
		TaskID:     p.TaskID,
		Index:      p.Index,
		Meta:       meta,
		DataBase64: base64.StdEncoding.EncodeToString(data),
	})
}

// readArtifactFile 解析路径 → 读取 → 校验 SHA-256 与登记一致（SPEC 4.8）。
// 源打开统一经 inputs.OpenSourceFile（2026-10-24 复审 R1：与 Local 挂载同一实现
// ——Remote 自动挂载的源读取两路安全语义必须字面同源）：整条路径由内核在 open
// 原语上强制不跟随（Unix 逐段 openat 游走，中间段 O_DIRECTORY|O_NOFOLLOW、末段
// O_NOFOLLOW|O_NONBLOCK——检查即打开，无 Lstat→Open 换入链接/FIFO 的窗口，打
// 开不返回则 fd 校验无从执行，hub 超时解不开本 node 的串行帧阻塞；Windows 逐
// 段 NtCreateFile 游走，每段 OBJ_DONT_REPARSE 打开时约束 reparse/link 解析，
// 见 inputs/sourceopen_windows.go）+ 打开后 fd fstat 确认常规文件（校验对象=
// 读取对象）+ 越界路径入口即拒。
func readArtifactFile(root string, meta protocol.Artifact) ([]byte, error) {
	notFound := func(format string, a ...any) error {
		return &artifactError{code: protocol.CodeArtifactNotFound, msg: fmt.Sprintf(format, a...)}
	}
	if meta.Size > protocol.ArtifactLimit {
		return nil, notFound("登记产物超过单文件上限")
	}
	rel := path.Clean(filepath.ToSlash(meta.Path))
	if rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, &artifactError{code: protocol.CodeArtifactMismatch, msg: "产物路径越界（拒绝）"}
	}
	f, _, err := inputs.OpenSourceFile(root, rel)
	if err != nil {
		return nil, notFound("产物源不可读（%v）", err)
	}
	defer f.Close()
	// 读取上限登记大小 + 1：文件被追加时在此暴露（哈希校验必然失败，防读爆内存）
	data, err := io.ReadAll(io.LimitReader(f, meta.Size+1))
	if err != nil {
		return nil, notFound("产物读取失败")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != meta.Sha256 || int64(len(data)) != meta.Size {
		return nil, &artifactError{code: protocol.CodeArtifactMismatch, msg: "产物内容与登记不一致（SHA-256/大小）"}
	}
	return data, nil
}

// artifactError 带协议错误码的取回失败。
type artifactError struct {
	code string
	msg  string
}

// Error 实现 error。
func (e *artifactError) Error() string { return e.msg }

// artifactErrCode 取回失败 → err 帧码。
func artifactErrCode(err error) string {
	if ae, ok := err.(*artifactError); ok {
		return ae.code
	}
	return protocol.CodeArtifactNotFound
}

// sendReplyTo 发帧并携带 ReplyTo（应答关联键）。
func (s *Service) sendReplyTo(typ, replyTo string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[node] 载荷编码失败 type=%s: %v", typ, err)
		return
	}
	env := &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      typ,
		MessageID: protocol.NewMessageID(),
		ReplyTo:   replyTo,
		To:        "hub",
		Payload:   b,
	}
	if err := s.tr.Send("hub", env); err != nil && !isBenignSendErr(err) {
		log.Printf("[node] 帧发送失败 type=%s: %v", typ, err)
	}
}

// sendErrReply 错误应答帧（ReplyTo 关联请求 mid）。
func (s *Service) sendErrReply(replyTo, code, msg string) {
	if replyTo == "" {
		return
	}
	env := &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgErr,
		MessageID: protocol.NewMessageID(),
		ReplyTo:   replyTo,
		To:        "hub",
		Err:       &protocol.ErrInfo{Code: code, Msg: msg},
	}
	if err := s.tr.Send("hub", env); err != nil && !isBenignSendErr(err) {
		log.Printf("[node] err 帧发送失败 code=%s: %v", code, err)
	}
}
