// Package core 统筹载体声明汇聚（2026-10-28 伦哥定案：客户端声明，hub 只接收汇聚，
// 中心端不再提供配置入口；2026-10-23 的网页指定 PUT 通路退役）。
// 语义：智行管家是 agent 的自我声明角色（AgentCard.Orchestrator，随能力表上报），
// hub 全网汇聚——恰一声明即生效；多声明为配置漂移冲突（无生效管家，回退名字约定 +
// 控制台警告）；零声明回退能力名含 zcode-dev 的名字约定（向后兼容）。指定只存映射
// 语义不变：不校验在线——离线时派发端点自然跳过，与普通任务同策略。
package core

import (
	"sort"
	"github.com/mtl802/swarmmind/internal/hub/store"
)

// PrimeDesignation 返回当前统筹指定（从全网能力卡聚合，不再读 store 持久化指定）：
// 恰一个 orchestrator 声明 → 该条目；零声明或多声明冲突 → nil（前端回退名字约定，
// 冲突经 PrimeConflicts 展示）。
func (c *Core) PrimeDesignation() *store.PrimeDesignation {
	nodeID, agentName, ok := c.primeFromDirectory()
	if !ok {
		return nil
	}
	return &store.PrimeDesignation{NodeID: nodeID, AgentName: agentName}
}

// PrimeConflicts 统筹声明冲突清单（≥2 个 orchestrator 声明时非空；恰一/零声明为空）。
func (c *Core) PrimeConflicts() []store.PrimeDesignation {
	decls := c.orchestratorDecls()
	if len(decls) < 2 {
		return nil
	}
	out := make([]store.PrimeDesignation, 0, len(decls))
	for _, d := range decls {
		out = append(out, d)
	}
	return out
}

// orchestratorDecls 扫全网能力卡（exposed 口径与目录一致）收集统筹声明，按
// (node_id, agent_name) 稳定排序（map 遍历无序，清单确定供展示与测试）。
func (c *Core) orchestratorDecls() []store.PrimeDesignation {
	var out []store.PrimeDesignation
	for _, e := range c.ListDirectory() {
		if e.Card.Orchestrator {
			out = append(out, store.PrimeDesignation{NodeID: e.NodeID, AgentName: e.Card.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].AgentName < out[j].AgentName
	})
	return out
}

// primeFromDirectory 单声明时返回 (node, agent, true)；零/多声明返回 false。
func (c *Core) primeFromDirectory() (string, string, bool) {
	decls := c.orchestratorDecls()
	if len(decls) != 1 {
		return "", "", false
	}
	return decls[0].NodeID, decls[0].AgentName, true
}

// SetPrimeDesignation 已退役（2026-10-28 客户端声明制）：中心端不再提供写入口，
// 指定权在客户端（AgentCard.Orchestrator 随能力表上报，PrimeDesignation 聚合读取）。
// 保留空实现仅为编译兼容排查期，调用方应已随 API 删除同步移除。
func (c *Core) SetPrimeDesignation(nodeID, agentName, by string) (store.PrimeDesignation, error) {
	return store.PrimeDesignation{}, ErrInvalid
}

// IsOrchestratorCard 目录能力卡统筹声明判定（DESIGN-butler 方案二 origin_task
// 挂接校验用，2026-10-28）：精确 (node_id, agent_name) 匹配目录（exposed 口径，
// 与 orchestratorDecls 同源）中带 Orchestrator 声明的条目。软校验只读，不校验
// 在线；旧 node 缺声明字段 → 目录无标记 → false（调用方拒绝并指路升级）。
func (c *Core) IsOrchestratorCard(nodeID, agentName string) bool {
	for _, d := range c.orchestratorDecls() {
		if d.NodeID == nodeID && d.AgentName == agentName {
			return true
		}
	}
	return false
}
