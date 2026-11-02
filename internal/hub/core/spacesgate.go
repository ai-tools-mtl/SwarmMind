// spacesgate.go 空间特性版本门（W07-01，docs/tasks/v3/W07-hub-routing.md「增加
// 空间特性的版本门，不支持时明确拒绝，普通 Task 不受牵连」）：中心对 node 下发
// 空间类操作（远端派发/发言/控制投影，W07-02 接线的首个消费者）前，按节点能力
// 表（AgentCard.Spaces 声明，register/card_update 同源上报）核对目标是否声明空
// 间特性——未声明（旧版 node / 未装配 collab）明确拒绝并指路升级，绝不把旧
// node 当有新特性，绝不静默降级为普通任务执行；普通任务路径不经过本门零牵连。
//
// 与 T2 会话门槛（SessionAdapter 声明核对，声明 19 三道防线）同一原则：能力发
// 现降级只允许发生在「声明缺失 → 明确拒绝」方向，禁止「声明缺失 → 照常下发」。
package core

import (
	"fmt"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ErrSpacesUnsupported 空间特性版本门拒绝（W07-01哨兵；errors.Is 判定，HTTP/
// MCP 面映射 protocol.CodeSpacesUnsupported 人话指路升级 node）。目标 node 能
// 力卡未声明空间协作能力（AgentCard.Spaces 空：旧版 node 或未装配 collab）。
var ErrSpacesUnsupported = fmt.Errorf("目标 node 未声明空间协作能力（%s）", protocol.CodeSpacesUnsupported)

// SpacesCapability 查询目标 node/agent 能力卡的空间协作能力声明（门槛数据源）：
// 返回声明版本（protocol.SpacesCapV1）与是否声明。node 未注册/agent 不在能力表
// /卡未声明 → ("", false)。
func (c *Core) SpacesCapability(nodeID, agentName string) (string, bool) {
	rec, ok := c.st.View().Nodes[nodeID]
	if !ok {
		return "", false
	}
	for _, card := range rec.Agents {
		if card.Name == agentName {
			if card.Spaces == "" {
				return "", false
			}
			return card.Spaces, true
		}
	}
	return "", false
}

// SpacesCapable 目标 node/agent 是否声明空间特性（布尔便捷形）。
func (c *Core) SpacesCapable(nodeID, agentName string) bool {
	_, ok := c.SpacesCapability(nodeID, agentName)
	return ok
}

// RequireSpacesCapable 版本门拒绝面（W07-02 远端派发/发言/控制投影下发前强制
// 调用）：未声明返回包 ErrSpacesUnsupported 的人话错误（附 node/agent 定位与
// 升级指路），已声明返回 nil。普通任务派发不调用本门——零牵连。
func (c *Core) RequireSpacesCapable(nodeID, agentName string) error {
	if c.SpacesCapable(nodeID, agentName) {
		return nil
	}
	return fmt.Errorf("%w：node %s 能力 %q 未声明空间特性（spaces），中心空间操作不下发；请升级该 node 至装配空间协作的版本后重试",
		ErrSpacesUnsupported, nodeID, agentName)
}

// SpacesNodeSupport 节点级空间特性声明投影（GET /api/v1/capabilities 的 spaces
// 数据源，W07-01）：nodeID → 是否「任一能力卡声明空间特性」（空间是 node 级
// 能力，声明随每张卡上报，任一即表装配）。未注册 node 不在表内。
func (c *Core) SpacesNodeSupport() map[string]bool {
	view := c.st.View()
	out := make(map[string]bool, len(view.Nodes))
	for nodeID, rec := range view.Nodes {
		supported := false
		for _, card := range rec.Agents {
			if card.Spaces != "" {
				supported = true
				break
			}
		}
		out[nodeID] = supported
	}
	return out
}

// DirectoryAgent 开放目录成员查询（W07-01 校验点：「成员必须来自当前开放目录」
// ——exposed=true 的能力条目；W07-02 hub 空间服务配置成员时的准入判定数据源，
// hub:<id> scope 词形见 collab.HubScope）。命中返回能力卡副本。
func (c *Core) DirectoryAgent(nodeID, agentName string) (protocol.AgentCard, bool) {
	for _, entry := range c.ListDirectory() {
		if entry.NodeID == nodeID && entry.Card.Name == agentName {
			return entry.Card, true
		}
	}
	return protocol.AgentCard{}, false
}
