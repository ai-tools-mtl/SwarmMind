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
	// BoundAt 最近登记时刻（UTC；重复登记刷新时刻不改 scope——同键同 scope 幂等）。
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

// RemoveIfScope 条件删除绑定（codex 复审必修D-1 代际语义）：仅当当前登记 scope
// 与 expectScope 一致才删除并落盘（返回 removed=true）；未登记（幂等 ack-noop）
// 或 scope 不符（旧代解绑重试不得删新代登记）返回 removed=false 零变更。落盘
// 失败错误如实上抛——hub 收不到 ack 不视为已解除，重试面兜底。
func (h *HubBindings) RemoveIfScope(sessionID, expectScope string) (bool, error) {
	if h == nil {
		return false, errors.New("hubbind: 登记表未装配")
	}
	if sessionID == "" {
		return false, errors.New("hubbind: session_id 必填")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.binds[sessionID]
	if !ok || cur.Space != expectScope {
		return false, nil // 幂等 no-op：未登记或代际不符
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
		return false, err
	}
	b = append(b, '\n')
	if err := atomicWriteFile(h.tmp, h.path, b); err != nil {
		return false, fmt.Errorf("hubbind: 绑定解除落盘失败: %w", err)
	}
	h.binds = next
	return true, nil
}

// Space 会话的登记 scope（便捷形；未登记空串）。
func (h *HubBindings) Space(sessionID string) string {
	e, ok := h.Get(sessionID)
	if !ok {
		return ""
	}
	return e.Space
}

// Put 登记绑定（space_bind 帧受理路径；幂等——同键同 scope 刷新时刻，同键异
// scope 以最近登记为准并落日志由调用方承担）。原子快照落盘成功才成立（落盘失败
// 错误如实上抛，内存不变——不虚报登记成功，hub 收不到 ack 即不下发空间轮）。
func (h *HubBindings) Put(sessionID, scope string, now time.Time) error {
	if h == nil {
		return errors.New("hubbind: 登记表未装配")
	}
	if sessionID == "" || scope == "" {
		return errors.New("hubbind: session_id 与 space 必填")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[string]HubBindEntry, len(h.binds)+1)
	for k, v := range h.binds {
		next[k] = v
	}
	next[sessionID] = HubBindEntry{Space: scope, BoundAt: now}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := atomicWriteFile(h.tmp, h.path, b); err != nil {
		return fmt.Errorf("hubbind: 绑定登记落盘失败: %w", err)
	}
	h.binds = next
	return nil
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
		// 解除登记（空 scope 语义；codex 复审必修D-1 条件删除）：仅当当前登记
		// scope 与预期一致才删除——旧代解绑重试不得删掉新代登记（不符如实
		// ack-noop；未登记幂等 ack）。普通轮恢复放行由删除成立承担。
		removed, err := s.hubBind.RemoveIfScope(p.SessionID, p.ExpectSpace)
		if err != nil {
			log.Printf("[node] space_unbind 失败 session=%s: %v", p.SessionID, err)
			s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
				&protocol.ErrInfo{Code: "bind_store_failed", Msg: "绑定解除落盘失败：" + err.Error()})
			return
		}
		if removed {
			log.Printf("[node] 中心空间绑定已解除 session=%s scope=%s from=%s", p.SessionID, p.ExpectSpace, env.From)
		} else {
			log.Printf("[node] space_unbind ack-noop（当前登记与预期不符或未登记）session=%s expect=%s from=%s",
				p.SessionID, p.ExpectSpace, env.From)
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
	if err := s.hubBind.Put(p.SessionID, p.Space, protocol.NowUTC()); err != nil {
		log.Printf("[node] space_bind 登记失败 session=%s scope=%s: %v", p.SessionID, p.Space, err)
		s.sendBindReply(protocol.MsgErr, env.MessageID, nil,
			&protocol.ErrInfo{Code: "bind_store_failed", Msg: "绑定登记落盘失败：" + err.Error()})
		return
	}
	log.Printf("[node] 中心空间绑定已登记 session=%s scope=%s from=%s", p.SessionID, p.Space, env.From)
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
