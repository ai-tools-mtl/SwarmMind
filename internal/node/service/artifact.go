// 产物取回（Loop 4，SPEC 4.2/4.8）：hub 中继 artifact_fetch 帧 → 本文件读本地
// 产物回 artifact_data。node 校验 SHA-256 与登记一致（SPEC 4.8），不一致回
// err(artifact_mismatch)；任务/产物不存在回 err(artifact_not_found)。
package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mtl802/swarmmind/internal/protocol"
)

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

	root, ok := s.cfg.ArtifactRoots[e.Task.TargetAgent]
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

// readArtifactFile 解析路径（防穿越）→ 读取 → 校验 SHA-256 与登记一致（SPEC 4.8）。
func readArtifactFile(root string, meta protocol.Artifact) ([]byte, error) {
	cleanRoot := filepath.Clean(root)
	abs := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(meta.Path)))
	if abs != cleanRoot && !strings.HasPrefix(abs, cleanRoot+string(os.PathSeparator)) {
		return nil, &artifactError{protocol.CodeArtifactMismatch, "产物路径越界（拒绝）"}
	}
	if meta.Size > protocol.ArtifactLimit {
		return nil, &artifactError{protocol.CodeArtifactNotFound, "登记产物超过单文件上限"}
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, &artifactError{protocol.CodeArtifactNotFound, "产物文件不存在或不可读"}
	}
	defer f.Close()
	// 读取上限登记大小 + 1：文件被追加时在此暴露（哈希校验必然失败，防读爆内存）
	data, err := io.ReadAll(io.LimitReader(f, meta.Size+1))
	if err != nil {
		return nil, &artifactError{protocol.CodeArtifactNotFound, "产物读取失败"}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != meta.Sha256 || int64(len(data)) != meta.Size {
		return nil, &artifactError{protocol.CodeArtifactMismatch, "产物内容与登记不一致（SHA-256/大小）"}
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
