// hubbind.go 中心空间成员映射登记表（W07-02 收件门受理面，docs/tasks/v3/
// W07-hub-routing.md「Node 收件门受理面扩展：hub 下发前登记 session↔hub-scope
// 绑定，node 按绑定放行——W07-01 拒绝面预留的扩展点」）：
//
//   - hub 经 space_bind 帧（协议加法帧，RequireSpacesCapable 版本门保证只发往
//     声明空间特性的 node）下发「会话 ↔ hub:<id>」绑定；本表持久落盘，重启后
//     中心空间续轮仍可按绑定放行（绑定随会话生命周期，hub 侧空间绑定不变则
//     登记持续有效）。
//   - 收件门消费：携 Space scope 的会话轮，本机 collab 无该会话绑定（中心空间
//     成员线程在 node 本地无空间归属）时按本表核对——绑定相符放行，不符/未登记
//     fail-closed 拒绝（拒绝话术与本地绑定不符分支分开锚定，互不冒充）。
//   - 执行口守卫（spaceTurnAllowed）同源消费：登记绑定的会话，普通轮（无 Space
//     标记）一律拒绝并指向空间派发——中心空间调度单入口，与本机绑定会话同规。
//
// 存储：data_dir/hub_space_bindings.json 单文件原子快照（独占 tmp + fsync +
// rename + 目录 fsync 尽力而为，0600，UTC）。加载损坏拒绝带病启动（绑定是执行
// 准入证据，宁拒启不瞎门——同 W02-01 mcp_access / W04-01 spaces.json 口径）。
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// hubBindFileName 中心空间绑定登记文件名（相对数据目录）。
const hubBindFileName = "hub_space_bindings.json"

// HubBindEntry 单条绑定（key = session_id，body = 登记事实）。
type HubBindEntry struct {
	// Space 空间 scope（恒 hub:<id> 词形；写入前经 collab.IsHubScope 校验）。
	Space string `json:"space"`
	// Generation 绑定代次（hub 单调分配；登记/解除的代次条件判据——结构性消除
	// ABA/旧重试删新登记/并发补偿删他人绑定：比代次不比 scope）。
	Generation uint64 `json:"generation"`
	// BoundAt 最近登记时刻（UTC；同代幂等刷新时刻）。
	BoundAt time.Time `json:"bound_at"`
}

// HubBindings 中心空间绑定登记表：互斥串行 + 原子快照。零值不可用，须经
// OpenHubBindings 构造（加载失败拒绝启动）。nil 指针（未装配）= 收件门与执行口
// 守卫按「无中心绑定」处理（与本机绑定判定面解耦，各自独立 fail-closed）。
type HubBindings struct {
	path string
	tmp  string

	mu    sync.Mutex
	binds map[string]HubBindEntry
}

// OpenHubBindings 打开（或初始化）数据目录下的绑定登记表。文件不存在从空表起步；
// 存在但损坏 → 报错（拒绝带病启动）。
func OpenHubBindings(dir string) (*HubBindings, error) {
	if dir == "" {
		return nil, errors.New("hubbind: 数据目录为空")
	}
	path := filepath.Join(dir, hubBindFileName)
	binds := map[string]HubBindEntry{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &binds); err != nil {
			return nil, fmt.Errorf("hubbind: 绑定登记损坏 %s: %w（拒绝带病启动；请人工检查或移除该文件）", path, err)
		}
		if binds == nil {
			binds = map[string]HubBindEntry{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("hubbind: 读取绑定登记失败: %w", err)
	}
	return &HubBindings{path: path, tmp: path + ".tmp", binds: binds}, nil
}

// Get 查会话的登记绑定（未登记 ok=false；副本返回）。
func (h *HubBindings) Get(sessionID string) (HubBindEntry, bool) {
	if h == nil {
		return HubBindEntry{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.binds[sessionID]
	return e, ok
}

// RemoveIfGen 条件删除绑定（解除路径；代次精确匹配——codex 复审轮 2 核心机制
// 1）：仅当当前登记存在且代次 == generation 才删除并落盘（removed=true）；未
// 登记或代次不等（旧代解除/新代之后的 stale 解除）→ removed=false 零变更
// （ack-noop——结构性保证「旧重试删不掉新登记、并发补偿删不掉他人绑定」）。落盘
// 失败错误如实上抛——hub 收不到成功 ack 不视为已解除，重试面兜底。
func (h *HubBindings) RemoveIfGen(sessionID string, generation uint64) (bool, HubBindEntry, error) {
	if h == nil {
		return false, HubBindEntry{}, errors.New("hubbind: 登记表未装配")
	}
	if sessionID == "" {
		return false, HubBindEntry{}, errors.New("hubbind: session_id 必填")
	}
	if generation == 0 {
		return false, HubBindEntry{}, errors.New("hubbind: generation 必填（代次协议）")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.binds[sessionID]
	if !ok {
		return false, HubBindEntry{}, nil // ack-noop：未登记
	}
	if cur.Generation != generation {
		return false, cur, nil // ack-noop：代次不等（分述日志由受理方输出）
	}
	next := make(map[string]HubBindEntry, len(h.binds))
	for k, v := range h.binds {
		if k == sessionID {
			continue
		}
		next[k] = v
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return false, HubBindEntry{}, err
	}
	b = append(b, '\n')
	if err := atomicWriteFile(h.tmp, h.path, b); err != nil {
		return false, HubBindEntry{}, fmt.Errorf("hubbind: 绑定解除落盘失败: %w", err)
	}
	h.binds = next
	return true, HubBindEntry{}, nil
}

// Space 会话的登记 scope（便捷形；未登记空串）。
func (h *HubBindings) Space(sessionID string) string {
	e, ok := h.Get(sessionID)
	if !ok {
		return ""
	}
	return e.Space
}

// PutIfGenNewer 登记绑定（space_bind 帧受理路径；代次条件生效——codex 复审轮 2
// 核心机制 1）：无登记 → 接受；代次 > 当前登记代次 → 接受（新代覆盖）；代次 ==
// 当前登记代次且 scope 相同 → 幂等接受（重派/补绑定安全）；否则（代次 ≤ 当前
// 且非同代同 scope——含 hub 重启后 stale 代次）拒绝（返回 accepted=false，不改
// 现状）。原子快照落盘成功才成立（落盘失败错误如实上抛，内存不变——不虚报登记
// 成功，hub 收不到成功 ack 即不下发空间轮）。
func (h *HubBindings) PutIfGenNewer(sessionID, scope string, generation uint64, now time.Time) (bool, HubBindEntry, error) {
	if h == nil {
		return false, HubBindEntry{}, errors.New("hubbind: 登记表未装配")
	}
	if sessionID == "" || scope == "" {
		return false, HubBindEntry{}, errors.New("hubbind: session_id 与 space 必填")
	}
	if generation == 0 {
		return false, HubBindEntry{}, errors.New("hubbind: generation 必填（代次协议）")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.binds[sessionID]
	if ok {
		switch {
		case generation > cur.Generation:
			// 新代：接受。
		case generation == cur.Generation && cur.Space == scope:
			return true, cur, nil // 同代同 scope：幂等接受（零落盘零变更）
		default:
			return false, cur, nil // stale 代次：拒绝（hub 重启后经 ack current_gen 重同步）
		}
	}
	next := make(map[string]HubBindEntry, len(h.binds)+1)
	for k, v := range h.binds {
		next[k] = v
	}
	next[sessionID] = HubBindEntry{Space: scope, Generation: generation, BoundAt: now}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return false, HubBindEntry{}, err
	}
	b = append(b, '\n')
	if err := atomicWriteFile(h.tmp, h.path, b); err != nil {
		return false, HubBindEntry{}, fmt.Errorf("hubbind: 绑定登记落盘失败: %w", err)
	}
	h.binds = next
	return true, HubBindEntry{}, nil
}

// handleSpaceBind space_bind 帧受理（W07-02 收件门受理面；hub→node 单向登记）：
// 词形校验（恒 hub:<id>，本机空间不经中心登记）→ session 非空 → 持久登记 → 回
// ack（ReplyTo=请求 mid，载荷原样回带供 hub 语义复核）。校验拒绝回 err 帧（hub
// 据此放弃本次派发，零副作用）。登记失败（落盘错误）同样回 err——hub 收不到
// ack 不下发空间轮（宁拒不瞎投）。
func (s *Service) handleSpaceBind(env *protocol.Envelope) {
	var p protocol.SpaceBindPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		log.Printf("[node] space_bind 载荷解析失败 from=%s: %v", env.From, err)
		s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "invalid", Msg: "space_bind 载荷非法"})
		return
	}
	if p.Space == "" {
		// 解除登记（空 scope 语义；代次精确匹配——比代次不比 scope）：仅当当前
		// 登记代次 == 请求代次才删除；未登记/代次不等 → ack-noop（分述日志；
		// 结构性保证旧重试删不掉新登记、并发补偿删不掉他人绑定）。
		removed, stale, err := s.hubBind.RemoveIfGen(p.SessionID, p.Generation)
		if err != nil {
			log.Printf("[node] space_unbind 失败 session=%s: %v", p.SessionID, err)
			s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
				&protocol.ErrInfo{Code: "bind_store_failed", Msg: "绑定解除落盘失败：" + err.Error()})
			return
		}
		if removed {
			log.Printf("[node] 中心空间绑定已解除 session=%s gen=%d from=%s", p.SessionID, p.Generation, env.From)
		} else {
			log.Printf("[node] space_unbind ack-noop（未登记或代次不等）session=%s gen=%d current=%d from=%s",
				p.SessionID, p.Generation, stale.Generation, env.From)
		}
		p.Accepted = removed
		p.CurrentGen = stale.Generation
		if removed {
			p.CurrentGen = 0
		}
		s.sendBindReply(protocol.MsgSpaceBindAck, env.MessageID, p, nil)
		return
	}
	if !collab.IsHubScope(p.Space) {
		log.Printf("[node] space_bind 拒绝：scope %q 非中心空间词形（须 hub:<id>）from=%s", p.Space, env.From)
		s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: protocol.CodeSpacesUnsupported, Msg: "space_bind scope 词形非法（须为 hub:<id> 中心空间）；本机空间不经中心登记"})
		return
	}
	if p.SessionID == "" {
		s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "invalid", Msg: "space_bind session_id 必填"})
		return
	}
	accepted, stale, err := s.hubBind.PutIfGenNewer(p.SessionID, p.Space, p.Generation, protocol.NowUTC())
	if err != nil {
		log.Printf("[node] space_bind 登记失败 session=%s scope=%s: %v", p.SessionID, p.Space, err)
		s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "bind_store_failed", Msg: "绑定登记落盘失败：" + err.Error()})
		return
	}
	if accepted {
		log.Printf("[node] 中心空间绑定已登记 session=%s scope=%s gen=%d from=%s", p.SessionID, p.Space, p.Generation, env.From)
	} else {
		log.Printf("[node] space_bind ack-noop（stale 代次）session=%s scope=%s gen=%d current=%d from=%s",
			p.SessionID, p.Space, p.Generation, stale.Generation, env.From)
	}
	p.Accepted = accepted
	p.CurrentGen = stale.Generation
	s.sendBindReply(protocol.MsgSpaceBindAck, env.MessageID, p, nil)
}

// sendBindReply space_bind 应答帧（ack/err；ReplyTo=请求 mid 端到端关联）。
func (s *Service) sendBindReply(typ, replyTo string, payload any, errInfo *protocol.ErrInfo) {
	env := &protocol.Envelope{
		Version:   protocol.ProtocolVersion,
		Type:      typ,
		MessageID: protocol.NewMessageID(),
		To:        "hub",
		ReplyTo:   replyTo,
		Payload:   nil,
		Err:       errInfo,
	}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			log.Printf("[node] space_bind 应答编码失败: %v", err)
			return
		}
		env.Payload = b
	}
	if err := s.tr.Send("hub", env); err != nil && !isBenignSendErr(err) {
		log.Printf("[node] space_bind 应答发送失败 type=%s: %v", typ, err)
	}
}

// atomicWriteFile 原子写文件（独占 tmp + fsync + rename + 目录 fsync 尽力而为；
// 0600——AGENTS 原子写纪律，与 collab 文件快照同模式）。
func atomicWriteFile(tmp, path string, b []byte) error {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// 上次落盘崩溃残留的 tmp：清掉重试一次（旧正本未动，安全）。
		if rmErr := os.Remove(tmp); rmErr != nil {
			return fmt.Errorf("清理残留临时文件失败: %w", rmErr)
		}
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename 落盘失败: %w", err)
	}
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		if serr := d.Sync(); serr != nil {
			log.Printf("hubbind: 目录 fsync 失败（尽力而为，rename 已完成）: %v", serr)
		}
		_ = d.Close()
	}
	return nil
}
