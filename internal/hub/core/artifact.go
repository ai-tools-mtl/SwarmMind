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

// relayFetchTimeout node 发起的产物取回中继的应答预算默认值（与 HTTP 数据面 30s
// 同口径；Config.RelayFetchTimeout 可覆盖，测试注入短预算）：超时删 waiter 并回
// err(fetch_timeout)——请求方（下游 node 挂载准备）按「暂不可取」分类重试轮询；
// 不设超时则 node 不应答时 waiter 泄漏且请求方挂死。
const relayFetchTimeout = 30 * time.Second

// relayTimeout 中继应答预算取值（Config 覆盖口径）。
func (c *Core) relayTimeout() time.Duration {
	if c.relayFetch > 0 {
		return c.relayFetch
	}
	return relayFetchTimeout
}

// fetchWaiter 在途取回：按请求帧 mid 关联 node 回传的 artifact_data/err 帧。
// 两种使用形态：
//   - hub 自身取回（HTTP 数据面，FetchArtifact）：应答投递 ch，由调用方消费；
//   - node 发起的中继（P4④跨机取回，requester 非 ""）：应答校验后原样转发回
//     requester（ReplyTo=请求 mid 端到端关联），一次性转发即拆表。
type fetchWaiter struct {
	node      string                  // 期望应答方（执行 node）
	ch        chan *protocol.Envelope // 容量 1：单应答，重复帧丢弃（hub 自身取回用）
	requester string                  // 非 "" = node 发起的中继：应答转发目标
	origMid   string                  // 中继：请求方原 mid（回传时恢复端到端 ReplyTo 关联；在途表键=hub 中继编号）
	meta      protocol.Artifact       // 中继校验基准（转发前对照登记复检）
	timer     *time.Timer             // 中继超时（到点拆表并回 fetch_timeout）
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
// 并投递；容量 1，重复帧丢弃）。无关联在途取回的帧忽略 + 告警。node 发起的中继
//（requester 非 ""）走一次性转发：首应答即拆表停表，内容校验后转发回请求方。
// 来源校验（2026-10-24 跨机取回审查必修2）：锁内确认 env.From 即预期执行 node
// 后才消费 waiter——来源不符的伪应答不拆表不停超时，合法应答与超时兜底均保留。
// 转发外发收敛（2026-10-24 复审 R2）：锁内 artClosed=false 复核通过才登记收敛
// 单元并锁外转发——Close 置位后到达的应答就地消化不再外发，已启动的转发由
// Close 等待完成（拆表决定与外发登记同临界区，无「锁外转发」窗口）。
func (c *Core) handleFetchReply(env *protocol.Envelope) {
	if env.ReplyTo == "" {
		log.Printf("[hub] %s 缺 reply_to，忽略 from=%s", env.Type, env.From)
		return
	}
	c.muArt.Lock()
	w := c.fetches[env.ReplyTo]
	if w != nil && w.node != env.From {
		want := w.node
		c.muArt.Unlock()
		log.Printf("[hub] %s 应答来源与执行 node 不符，丢弃 from=%s want=%s reply=%s",
			env.Type, env.From, want, env.ReplyTo)
		return
	}
	relay := w != nil && w.requester != ""
	forward := false
	if relay {
		delete(c.fetches, env.ReplyTo) // 中继一次性：首应答即拆表（重复帧落空）
		if w.timer != nil {
			w.timer.Stop()
		}
		forward = !c.artClosed
		if forward {
			c.artWG.Add(1) // 覆盖转发及其校验回帧（Close 置位后不再登记）
		}
	}
	c.muArt.Unlock()
	if w == nil {
		log.Printf("[hub] %s 无在途取回，忽略 from=%s reply=%s", env.Type, env.From, env.ReplyTo)
		return
	}
	if relay {
		if !forward {
			return // Close 已收束：应答就地消化（请求方准备阶段超时自会重试收敛）
		}
		defer c.artWG.Done()
		c.forwardFetchReply(env, w)
		return
	}
	select {
	case w.ch <- env:
	default:
	}
}

// handleNodeFetch node 发起的产物取回中继（P4④跨机取回/SPEC 4.12）：下游 node
// 挂载准备阶段按上游 task_id+index 请求产物字节。归属鉴权与 dep_resolve 同源
//（请求方名下须有依赖该上游 idem 的任务），且先于结果/下标检查（2026-10-24
// 审查建议修5：无权限场景统一响应——任务不存在与无依赖归属一律 forbidden 同码
// 同文案，未授权方无法借 task_not_found/artifact_not_found 差异判断任务存在性
// 或探测产物下标；精确错误只授予已通过鉴权的请求方）。定位后按 **hub 分配的中
// 继编号** 中继到执行 node（审查必修1：请求帧 mid 由调用方提供可冲突，同 mid
// 二次请求会覆盖在途 waiter——在途表键一律用本端随机编号，表内保存原
// requester/mid，回传时恢复端到端关联），并设 relayFetchTimeout 兜底（执行
// node 不应答时回 err(fetch_timeout)，请求方按暂不可取重试）。
func (c *Core) handleNodeFetch(env *protocol.Envelope) {
	var p protocol.ArtifactFetchPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.TaskID == "" {
		log.Printf("[hub] artifact_fetch 载荷解析失败 from=%s: %v", env.From, err)
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "invalid", Msg: "artifact_fetch 载荷非法"})
		return
	}
	c.muArt.Lock()
	closed := c.artClosed
	c.muArt.Unlock()
	if closed {
		// Close 已收束在途表（不受理新中继，防关闭后再挂 30s 级 timer 调
		// transport）；按暂不可取回帧，请求方重试自会落到别处。
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeNodeOffline, Msg: "hub 关闭中，暂不受理取回中继"})
		return
	}
	t, ok := c.GetTask(p.TaskID)
	// 鉴权先行（统一响应，探测无效化）：任务不存在（含已过保留期清理）与无依赖
	// 归属不做区分。
	if !ok || t.IdemKey == "" || !nodeDependsOnIDem(c.st.View(), env.From, t.IdemKey) {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeForbidden, Msg: "任务不存在或本 node 名下无依赖该任务的 idem（越权拒绝）"})
		return
	}
	if t.Result == nil || p.Index < 0 || p.Index >= len(t.Result.Artifacts) {
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeArtifactNotFound, Msg: "任务无终态结果或产物下标越界"})
		return
	}
	// hub 分配中继编号：请求帧原 mid 只作回传关联（origMid），不作在途表键。
	// 登记发布一气呵成（2026-10-24 复审 R2/R3，同一 muArt 临界区）：关闭复核
	//（关闭后拒绝登记，Close 清表与登记同锁序——不存在「Close 返回后新建
	// waiter」的交错）→ 表登记 → 启动超时 timer（先登后启：回调起手先取 muArt，
	// 必见已登记 waiter，短超时/调度暂停不再丢失超时兜底）→ 初始 Send 收敛组
	// 登记（复审 R2：Close 等其在途 Send 完成）。锁内零 Send（membus 同步回调
	// 重入纪律）。
	c.muArt.Lock()
	if c.artClosed {
		c.muArt.Unlock()
		// Close 已收束在途表（不受理新中继，防关闭后再挂 30s 级 timer 调
		// transport）；按暂不可取回帧，请求方重试自会落到别处。
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeNodeOffline, Msg: "hub 关闭中，暂不受理取回中继"})
		return
	}
	relayMid := protocol.NewMessageID()
	w := &fetchWaiter{node: t.TargetNode, requester: env.From, origMid: env.MessageID,
		meta: t.Result.Artifacts[p.Index]}
	c.fetches[relayMid] = w
	w.timer = time.AfterFunc(c.relayTimeout(), func() {
		c.relayTimeoutFired(relayMid, w, env.From, env.MessageID)
	})
	c.artWG.Add(1) // 覆盖随后的初始 Send 及其失败收尾回帧（defer Done）
	c.muArt.Unlock()
	defer c.artWG.Done()

	err := c.sendFetchRequest(t.TargetNode, relayMid,
		protocol.ArtifactFetchPayload{TaskID: p.TaskID, Index: p.Index})
	if err == nil {
		return
	}
	// 中继发送失败（源 node 离线/链路关闭）：立即拆表回 err(node_offline)，
	// 请求方按暂不可取在准备阶段内重试轮询。拆表与回帧决定在锁内（cur==w 且未
	// 关闭才回帧）；本回帧在初始 Send 收敛单元内，Close 等其完成。
	c.muArt.Lock()
	cur := c.fetches[relayMid]
	reply := cur == w && !c.artClosed
	if cur == w {
		delete(c.fetches, relayMid)
	}
	c.muArt.Unlock()
	w.timer.Stop()
	if reply {
		code := protocol.CodeFetchTimeout
		if errors.Is(err, transport.ErrNoEndpoint) || errors.Is(err, transport.ErrClosed) {
			code = protocol.CodeNodeOffline
		}
		_ = c.sendFrame(env.From, protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: code, Msg: "源 node 暂不可达（可重试）"})
	}
}

// relayTimeoutFired 中继超时回调（AfterFunc 独立 goroutine，2026-10-24 复审
// R2/R3）：锁内复核消费——应答已先行拆表（cur≠w）或 Close 已收束（shutting）
// 则静默退出；决定回超时帧后在收敛组登记（锁内 Add，Close 的 Wait 与置位同锁
// 序，保证 Wait 期间零新增）再锁外 Send——Close 等待已启动回调外发完成后再返
// 回，不留关闭后调 transport 的残尾。
func (c *Core) relayTimeoutFired(relayMid string, w *fetchWaiter, requester, origMid string) {
	c.muArt.Lock()
	cur := c.fetches[relayMid]
	if cur == w {
		delete(c.fetches, relayMid)
	}
	reply := cur == w && !c.artClosed
	if reply {
		c.artWG.Add(1)
	}
	c.muArt.Unlock()
	if !reply {
		return
	}
	defer c.artWG.Done()
	_ = c.sendFrame(requester, protocol.MsgErr, origMid, nil,
		&protocol.ErrInfo{Code: protocol.CodeFetchTimeout, Msg: "源 node 取回应答超时（可重试）"})
}

// forwardFetchReply 中继应答转发：先做归属与完整性校验（与 hub 自身取回同一
// 双保险口径——执行 node 已校验，此处对照 hub 登记再验一次；归属已在
// handleFetchReply 锁内预检，此处复核为防御纵深），通过后原帧载荷转发请求方
//（ReplyTo 改写为请求方原 mid——表键是 hub 中继编号，端到端关联靠 origMid
// 恢复）；不符回 err（码沿用取回语义）。转发失败（请求方离线）仅记录——请求
// 方准备阶段超时自会收敛。
func (c *Core) forwardFetchReply(env *protocol.Envelope, w *fetchWaiter) {
	if env.From != w.node {
		log.Printf("[hub] 中继应答归属不符，丢弃 from=%s want=%s", env.From, w.node)
		return
	}
	if env.Type == protocol.MsgArtifactData && c.relayArtifactOk(w, env) != nil {
		return // 校验失败已在内部回 err 帧
	}
	fwd := *env
	fwd.From = ""
	fwd.ReplyTo = w.origMid // 恢复端到端关联（请求方按自身请求 mid 关联应答）
	if err := c.tr.Send(w.requester, &fwd); err != nil && !errors.Is(err, transport.ErrNoEndpoint) && !errors.Is(err, transport.ErrClosed) {
		log.Printf("[hub] 中继应答转发失败 to=%s: %v", w.requester, err)
	}
}

// relayArtifactOk 中继 artifact_data 的完整性复检（对照 hub 登记元信息与内容
// 哈希，与 hub 自身取回 consumArtifactReply 同一双保险口径）；不符回 err 给
// 请求方（永久失败，请求方不重试），返回非 nil。
func (c *Core) relayArtifactOk(w *fetchWaiter, env *protocol.Envelope) error {
	_, err := c.consumArtifactReply(w.node, w.meta, env)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrArtifactNotFound) {
		_ = c.sendFrame(w.requester, protocol.MsgErr, w.origMid, nil,
			&protocol.ErrInfo{Code: protocol.CodeArtifactNotFound, Msg: "上游产物已清理或下标越界"})
		return err
	}
	_ = c.sendFrame(w.requester, protocol.MsgErr, w.origMid, nil,
		&protocol.ErrInfo{Code: protocol.CodeArtifactMismatch, Msg: "中继校验：取回内容与登记不一致"})
	return err
}
