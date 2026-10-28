// AgentState 本机 agent 运行时状态（Loop 7，INTERACTION 旅程①「服务提供者」）：
// exposed 接入开关的运行时真相源。YAML 是配置基线（只读），本状态在其上叠加
// 控制台开关覆盖，持久化 data/agents-state.json 使重启保持；effective 视图同时
// 供注册能力表（register/card_update）与执行器装配使用，三处同源。
//
// 开关语义（SPEC 4.9）：exposed=false = 执行准入关闭（新任务 hub 侧 404
// agent_not_exposed），已排队任务正常继续执行至终态——本状态不参与任何执行路径。
// 持久化遵循先写后见（SPEC 4.7）：落盘成功才发布内存，失败不改变运行时状态。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// agentsStateFileName 状态文件名（相对 data_dir）。
const agentsStateFileName = "agents-state.json"

// ErrAgentNotFound 开关目标不在本机配置中（配置漂移：node.yaml 已删除该 agent）。
var ErrAgentNotFound = errors.New("localapi: agent 不在本机配置中")

// agentsStateDisk agents-state.json 磁盘结构。
type agentsStateDisk struct {
	Version int `json:"version"`
	// Exposed agent 名 → 开关覆盖与变更时刻（审计：谁在本机何时下线了它）。
	Exposed map[string]agentExposedRecord `json:"exposed"`
	// Orchestrator 统筹载体声明覆盖（2026-10-28 客户端声明制）。
	Orchestrator map[string]agentOrchRecord `json:"orchestrator,omitempty"`
}

// agentExposedRecord 单 agent 的开关覆盖记录（Exposed/Orchestrator 共用形态）。
type agentExposedRecord struct {
	Exposed   bool      `json:"exposed"`
	UpdatedAt time.Time `json:"updated_at"` // UTC
}

// agentOrchRecord 单 agent 的统筹声明覆盖记录。
type agentOrchRecord struct {
	Orchestrator bool      `json:"orchestrator"`
	UpdatedAt    time.Time `json:"updated_at"` // UTC
}

// AgentState exposed 开关运行时状态（并发安全）。
type AgentState struct {
	mu           sync.Mutex
	baseline     []protocol.ExecProfile // YAML 基线（构造后只读）
	override     map[string]agentExposedRecord
	orchOverride map[string]agentOrchRecord // 统筹声明覆盖（2026-10-28 客户端声明制）
	path         string
	tmp          string
}

// OpenAgentState 打开（或创建）本机 agent 状态：加载 data_dir/agents-state.json，
// 把开关覆盖叠加到 YAML 基线上。状态文件中的未知 agent（配置已删除）忽略并告警，
// 不视为损坏——配置漂移不应阻塞 node 启动。
func OpenAgentState(dataDir string, baseline []protocol.ExecProfile) (*AgentState, error) {
	s := &AgentState{
		baseline: append([]protocol.ExecProfile(nil), baseline...),
		override: map[string]agentExposedRecord{},
		path:     filepath.Join(dataDir, agentsStateFileName),
		tmp:      filepath.Join(dataDir, agentsStateFileName+".tmp"),
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil // 首次启动：无覆盖，exposed 全取 YAML 值（默认 true）
	}
	if err != nil {
		return nil, fmt.Errorf("localapi: 读取状态文件失败: %w", err)
	}
	var disk agentsStateDisk
	if err := json.Unmarshal(b, &disk); err != nil {
		return nil, fmt.Errorf("localapi: 状态文件损坏 %s: %w", s.path, err)
	}
	known := map[string]bool{}
	for _, p := range s.baseline {
		known[p.Name] = true
	}
	for name, rec := range disk.Exposed {
		if !known[name] {
			log.Printf("[node] 状态文件中的 agent %q 不在当前配置，忽略", name)
			continue
		}
		s.override[name] = rec
	}
	for name, rec := range disk.Orchestrator {
		if !known[name] {
			continue // 同 exposed：配置漂移忽略不阻塞启动
		}
		s.orchOverride[name] = rec
	}
	return s, nil
}

// Effective 返回叠加开关覆盖后的 ExecProfile 视图（注册能力表与执行器装配的
// 同源基准；顺序与 YAML 一致）。
func (s *AgentState) Effective() []protocol.ExecProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]protocol.ExecProfile(nil), s.baseline...)
	for i := range out {
		if rec, ok := s.override[out[i].Name]; ok {
			out[i].Exposed = rec.Exposed
		}
		if rec, ok := s.orchOverride[out[i].Name]; ok {
			out[i].Orchestrator = rec.Orchestrator
		}
	}
	return out
}

// Cards 由 effective 视图生成对外能力表（register/card_update 同源）。
func (s *AgentState) Cards() []protocol.AgentCard {
	return service.CardsFromProfiles(s.Effective())
}

// SetExposed 变更开关：先写后见——构造新覆盖集并落盘，成功后才发布内存。
// 返回变更后的 ExecProfile 视图；未知 agent 返回 ErrAgentNotFound（404）。
func (s *AgentState) SetExposed(name string, exposed bool) (protocol.ExecProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.baseline {
		if s.baseline[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return protocol.ExecProfile{}, ErrAgentNotFound
	}
	// 磁盘值与 YAML 基线一致时可直接删除覆盖（回归 YAML 值），状态文件不积累
	next := make(map[string]agentExposedRecord, len(s.override)+1)
	for k, v := range s.override {
		next[k] = v
	}
	if exposed == s.baseline[idx].Exposed {
		delete(next, name)
	} else {
		next[name] = agentExposedRecord{Exposed: exposed, UpdatedAt: protocol.NowUTC()}
	}
	if err := s.persist(next); err != nil {
		return protocol.ExecProfile{}, err // 落盘失败：不发布，运行时状态不变
	}
	s.override = next
	p := s.baseline[idx]
	p.Exposed = exposed
	return p, nil
}

// SetAgentOrchestrator 变更统筹载体声明（2026-10-28 客户端声明制）：镜像 SetExposed
// 的先写后见语义——落盘成功才发布内存；回归 YAML 基线值时删除覆盖项防状态文件积累。
// 声明即身份：本机恰一个声明由调用方（node 控制台）保证互斥（同机多声明在开关层拒绝）。
func (s *AgentState) SetAgentOrchestrator(name string, orchestrator bool) (protocol.ExecProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.baseline {
		if s.baseline[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return protocol.ExecProfile{}, ErrAgentNotFound
	}
	if orchestrator {
		// 同机互斥：本机已有其他 agent 持声明则拒绝（全网多声明属跨机配置漂移，
		// hub 聚合层告警；本机双声明在源头即可避免）。
		for k, rec := range s.orchOverride {
			if k != name && rec.Orchestrator {
				return protocol.ExecProfile{}, fmt.Errorf("localapi: 本机 %q 已声明为智行管家（全网同一时刻至多一个统筹，先取消原声明）", k)
			}
		}
		for _, p := range s.baseline {
			if p.Name != name && p.Orchestrator {
				return protocol.ExecProfile{}, fmt.Errorf("localapi: 本机 %q 已声明为智行管家（yaml 基线），先改配置再声明", p.Name)
			}
		}
	}
	// 回归基线值时删除覆盖（状态文件不积累）；orchOverride 与盘上 Exposed 结构分离存储
	next := make(map[string]agentOrchRecord, len(s.orchOverride)+1)
	for k, v := range s.orchOverride {
		next[k] = v
	}
	if orchestrator == s.baseline[idx].Orchestrator {
		delete(next, name)
	} else {
		next[name] = agentOrchRecord{Orchestrator: orchestrator, UpdatedAt: protocol.NowUTC()}
	}
	if err := s.persistOrch(next); err != nil {
		return protocol.ExecProfile{}, err // 落盘失败：不发布，运行时状态不变
	}
	s.orchOverride = next
	p := s.baseline[idx]
	p.Orchestrator = orchestrator
	return p, nil
}

// ReplaceBaseline 整表替换 YAML 基线（方案 3.5 网页编辑，PUT /api/v1/local/config
// 热应用链）：提交表落盘后即权威配置，既有 exposed 覆盖一并清除——覆盖残留会让
// 盘上 yaml 与生效值分叉（PATCH 开关写覆盖、PUT 写基线，基线刷新后覆盖语义失效），
// 下次重启会回跳旧开关值。先写后见：落盘成功才发布内存。
func (s *AgentState) ReplaceBaseline(profiles []protocol.ExecProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persist(map[string]agentExposedRecord{}); err != nil {
		return err // 落盘失败：不发布，基线与覆盖集均不变
	}
	s.baseline = append([]protocol.ExecProfile(nil), profiles...)
	s.override = map[string]agentExposedRecord{}
	return nil
}

// persist exposed 覆盖集落盘（临时文件+rename，SPEC 4.7）；调用方持 mu（R3' 审查：
// 内部不得重复加锁——SetExposed/ReplaceBaseline 均持 mu 调用，二次 Lock 即自锁死锁）。
func (s *AgentState) persist(override map[string]agentExposedRecord) error {
	return s.persistDisk(agentsStateDisk{Version: 1, Exposed: override, Orchestrator: s.orchOverride})
}

// persistOrch 统筹声明覆盖集落盘（同 persist；调用方持 mu）。
func (s *AgentState) persistOrch(orch map[string]agentOrchRecord) error {
	return s.persistDisk(agentsStateDisk{Version: 1, Exposed: s.override, Orchestrator: orch})
}

// persistDisk agents-state.json 原子落盘（临时文件+rename）。
func (s *AgentState) persistDisk(disk agentsStateDisk) error {
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("localapi: 状态编码失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("localapi: 创建数据目录失败: %w", err)
	}
	if err := os.WriteFile(s.tmp, b, 0o644); err != nil {
		return fmt.Errorf("localapi: 写状态临时文件失败: %w", err)
	}
	if err := os.Rename(s.tmp, s.path); err != nil {
		return fmt.Errorf("localapi: 状态落盘失败: %w", err)
	}
	return nil
}

// Sync 把当前能力表上报 hub（每次注册成功回调触发）：重启/重连后与 hub 注册表
// 收敛，开关不因连接重建而漂移。离线时上报失败静默——register 已携全量能力表。
func (s *AgentState) Sync(reporter func([]protocol.AgentCard) error) {
	if reporter == nil {
		return
	}
	if err := reporter(s.Cards()); err != nil {
		log.Printf("[node] 能力表上报未送达（注册帧已携全量，重连即收敛）: %v", err)
	}
}
