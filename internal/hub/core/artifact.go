package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 产物取回错误（HTTP 管理面映射，SPEC 六：超时 30s → 504；errors.Is 判定）。
var (
	// ErrTaskNotFound 任务不存在（404 task_not_found）。
	ErrTaskNotFound = errors.New("core: 任务不存在")
	// ErrArtifactNotFound 任务无终态结果、下标越界或 node 侧文件缺失（404）。
	ErrArtifactNotFound = errors.New("core: 产物不存在")
	// ErrNodeOffline 目标执行 node 不在线（503 node_offline）。
	ErrNodeOffline = errors.New("core: 目标节点不在线")
	// ErrFetchTimeout node 侧取回应答超时（504 fetch_timeout，SPEC 六：30s）。
	ErrFetchTimeout = errors.New("core: 产物取回超时")
	// ErrArtifactMismatch node 侧校验文件 SHA-256 与登记不一致（409，SPEC 4.8）。
	ErrArtifactMismatch = errors.New("core: 产物哈希与登记不一致")
)

// Artifact 取回的产物：登记元信息 + 原始内容。
type Artifact struct {
	Meta protocol.Artifact
	Data []byte
}

// fetchWaiter 在途取回：按请求帧 mid 关联 node 回传的 artifact_data/err 帧。
type fetchWaiter struct {
	node string
	ch   chan *protocol.Envelope // 容量 1：单应答，重复帧丢弃
}

// FetchArtifact 产物取回中继（SPEC 六/4.2）：定位任务 → 经 artifact_fetch 帧向
// 执行 node 取回 → 等待 artifact_data（timeout 内）→ 校验一致性后返回内容。
// node 侧已做 SHA-256 校验（SPEC 4.8），此处对回传内容再校验一次（中继链双保险）。
func (c *Core) FetchArtifact(taskID string, index int, timeout time.Duration) (*Artifact, error) {
	t, ok := c.GetTask(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.Result == nil || index < 0 || index >= len(t.Result.Artifacts) {
		return nil, ErrArtifactNotFound
	}
	meta := t.Result.Artifacts[index]

	mid := protocol.NewMessageID()
	w := &fetchWaiter{node: t.TargetNode, ch: make(chan *protocol.Envelope, 1)}
	c.muArt.Lock()
	c.fetches[mid] = w
	c.muArt.Unlock()
	defer func() {
		c.muArt.Lock()
		delete(c.fetches, mid)
		c.muArt.Unlock()
	}()

	err := c.sendFetchRequest(t.TargetNode, mid,
		protocol.ArtifactFetchPayload{TaskID: taskID, Index: index})
	if err != nil {
		if errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed) {
			return nil, fmt.Errorf("%w: %v", ErrNodeOffline, err)
		}
		return nil, fmt.Errorf("artifact_fetch 发送失败: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case env := <-w.ch:
			res, err := c.consumArtifactReply(w.node, meta, env)
			if errors.Is(err, errArtifactIgnore) {
				continue // 身份不符等防御路径：继续等待合法应答直至超时
			}
			return res, err
		case <-timer.C:
			return nil, ErrFetchTimeout
		}
	}
}

// errArtifactIgnore 内部哨兵：应答帧不可用但不终止等待（继续等到超时）。
var errArtifactIgnore = errors.New("core: 忽略该应答帧")

// sendFetchRequest 发起 artifact_fetch 关联请求：请求帧 MessageID 即关联键 mid。
// 全仓应答关联约定是「ReplyTo = 请求帧 MessageID」（node 侧 handleArtifactFetch、
// hub 侧 query/submit 应答皆然），故本端发起的请求必须让 waiter 键随 MessageID
// 上路；sendFrame 的 MessageID 恒为新随机值，只适用于应答/推送场景——曾把 waiter
// 键错放在 ReplyTo，致 node 回帧关联不上、恒 fetch_timeout。
func (c *Core) sendFetchRequest(to, mid string, payload protocol.ArtifactFetchPayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.tr.Send(to, &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      protocol.MsgArtifactFetch,
		MessageID: mid,
		To:        to,
		Payload:   b,
	})
}

// consumArtifactReply 解析 node 应答帧：artifact_data → 校验并取内容；
// err 帧 → 按码映射；其余 → 忽略。
func (c *Core) consumArtifactReply(expectNode string, meta protocol.Artifact, env *protocol.Envelope) (*Artifact, error) {
	if env == nil {
		return nil, ErrFetchTimeout
	}
	if env.From != expectNode {
		return nil, errArtifactIgnore // 非 执行 node 的帧：忽略（归属防御）
	}
	if env.Type == protocol.MsgErr {
		code := "internal"
		msg := ""
		if env.Err != nil {
			code, msg = env.Err.Code, env.Err.Msg
		}
		switch code {
		case protocol.CodeArtifactNotFound:
			return nil, fmt.Errorf("%w: %s", ErrArtifactNotFound, msg)
		case protocol.CodeArtifactMismatch:
			return nil, fmt.Errorf("%w: %s", ErrArtifactMismatch, msg)
		}
		return nil, fmt.Errorf("node 取回失败 code=%s msg=%s", code, msg)
	}
	if env.Type != protocol.MsgArtifactData {
		return nil, errArtifactIgnore
	}
	var p protocol.ArtifactDataPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("artifact_data 载荷解析失败: %w", err)
	}
	if p.Meta.Sha256 != meta.Sha256 || p.Meta.Path != meta.Path {
		return nil, fmt.Errorf("%w: 元信息不一致", ErrArtifactMismatch)
	}
	data, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil {
		return nil, fmt.Errorf("artifact_data base64 解码失败: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != meta.Sha256 {
		return nil, fmt.Errorf("%w: 内容哈希与登记不一致", ErrArtifactMismatch)
	}
	return &Artifact{Meta: meta, Data: data}, nil
}

// handleFetchReply node 应答帧路由（artifact_data / err，按 ReplyTo 定位在途取回
// 并投递；容量 1，重复帧丢弃）。无关联在途取回的帧忽略 + 告警。
func (c *Core) handleFetchReply(env *protocol.Envelope) {
	if env.ReplyTo == "" {
		log.Printf("[hub] %s 缺 reply_to，忽略 from=%s", env.Type, env.From)
		return
	}
	c.muArt.Lock()
	w := c.fetches[env.ReplyTo]
	c.muArt.Unlock()
	if w == nil {
		log.Printf("[hub] %s 无在途取回，忽略 from=%s reply=%s", env.Type, env.From, env.ReplyTo)
		return
	}
	select {
	case w.ch <- env:
	default:
	}
}
