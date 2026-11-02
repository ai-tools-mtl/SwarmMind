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
// 力卡未声明空间协作能力，或声明版本不受本 hub 支持（声明词形见各类错误消息）。
var ErrSpacesUnsupported = fmt.Errorf("目标 node 未声明空间协作能力（%s）", protocol.CodeSpacesUnsupported)

// SpacesCapability 查询目标 node/agent 能力卡的空间协作能力声明（门槛数据源）：
// 声明版本经 protocol.SupportedSpacesCap 判定（单源），受支持才返回（版本，true）；
// 未声明（含旧版 node 词形缺字段）/声明版本不受支持（v0 旧版、词形非法、空白）/
// node 未注册/agent 不在能力表 → ("", false)。
func (c *Core) SpacesCapability(nodeID, agentName string) (string, bool) {
	card, ok := c.cardByName(nodeID, agentName) // 目标卡选择单规则（与强制面同助手）
	if !ok || !protocol.SupportedSpacesCap(card.Spaces) {
		return "", false
	}
	return card.Spaces, true
}

// SpacesCapable 目标 node/agent 是否声明且受支持空间特性（布尔便捷形，判定单源
// 同 SpacesCapability）。
func (c *Core) SpacesCapable(nodeID, agentName string) bool {
	_, ok := c.SpacesCapability(nodeID, agentName)
	return ok
}

// RequireSpacesCapable 版本门拒绝面（W07-02 远端派发/发言/控制投影下发前强制
// 调用）：声明且受支持返回 nil；否则包 ErrSpacesUnsupported 人话拒绝，话术按
// 声明态三分——未声明（空：旧版 node / 未装配）与合法词形非受支持版本（v0 旧版
// 或 v2/v10 等本 hub 未支持的新词形）指路核对双方版本，词形非法（garbage/空白）
// 如实注记异常配置（不冒充「旧版本」）。目标卡选择与 SpacesCapability 同规则
// （slice 序首个同名命中——codex 三审必修：同名重复卡时三处判定必须同一张卡，
// 否则查询判 true 强制门判 false 自相矛盾；注册入口重复名未拒时的确定性口径）。
// 普通任务派发不调用本门——零牵连。
func (c *Core) RequireSpacesCapable(nodeID, agentName string) error {
	if _, ok := c.SpacesCapability(nodeID, agentName); ok {
		return nil // 与查询面同卡同判定（单源 SupportedSpacesCap）
	}
	card, found := c.cardByName(nodeID, agentName)
	if !found {
		return fmt.Errorf("%w：node %s 能力 %q 未声明空间特性（spaces），中心空间操作不下发；请升级该 node 至装配空间协作的版本后重试",
			ErrSpacesUnsupported, nodeID, agentName)
	}
	switch {
	case card.Spaces == "":
		// 未声明（旧版 node / 未装配 collab）：统一未声明话术（指路升级）。
		return fmt.Errorf("%w：node %s 能力 %q 未声明空间特性（spaces），中心空间操作不下发；请升级该 node 至装配空间协作的版本后重试",
			ErrSpacesUnsupported, nodeID, agentName)
	case protocol.ValidSpacesCapWordform(card.Spaces):
		return fmt.Errorf("%w：node %s 能力 %q 空间特性声明版本 %q 不受本 hub 支持（本 hub 支持 %s）：请核对双方版本（声明方过旧则升级 node；声明方更新则升级 hub）",
			ErrSpacesUnsupported, nodeID, agentName, card.Spaces, protocol.SpacesCapV1)
	default:
		return fmt.Errorf("%w：node %s 能力 %q 空间特性声明 %q 词形非法（异常配置，疑似实现缺陷或人为篡改），中心空间操作不下发；请核对该 node 版本与配置",
			ErrSpacesUnsupported, nodeID, agentName, card.Spaces)
	}
}

// cardByName 目标能力卡定位（slice 序首个同名命中——与 SpacesCapability 的遍历
// 停止规则同规则；未注册 node/无同名卡 → ("", false)）。抽公共助手消除「查询面
// 与强制面各自遍历、同名重复卡时命中不同卡」的分歧行为。
func (c *Core) cardByName(nodeID, agentName string) (protocol.AgentCard, bool) {
	rec, ok := c.st.View().Nodes[nodeID]
	if !ok {
		return protocol.AgentCard{}, false
	}
	for _, card := range rec.Agents {
		if card.Name == agentName {
			return card, true
		}
	}
	return protocol.AgentCard{}, false
}

// SpacesNodeSupport 节点级空间特性声明投影（GET /api/v1/capabilities 的 spaces
// 数据源，W07-01）：nodeID → 是否「任一能力卡声明受支持的空间特性版本」（判定
// 单源 protocol.SupportedSpacesCap；空间是 node 级能力，声明随每张卡上报，任一
// 即表装配）。未声明/声明不受支持（v0 等）/未注册 node → false 或不在表内。
func (c *Core) SpacesNodeSupport() map[string]bool {
	view := c.st.View()
	out := make(map[string]bool, len(view.Nodes))
	for nodeID, rec := range view.Nodes {
		supported := false
		for _, card := range rec.Agents {
			if protocol.SupportedSpacesCap(card.Spaces) {
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
