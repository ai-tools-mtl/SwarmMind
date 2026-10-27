package core

import (
	"sort"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 管理面目录视图（GET /api/v1/agents、GET /api/v1/nodes 数据源，SPEC 六）。

// AgentEntry 能力目录单条：对外能力声明（AgentCard，不含本机执行细节、公开声明
// 投影 allow_args/stdin_to_arg 除外，SPEC 六）+ 归属与在线状态。
type AgentEntry struct {
	Card   protocol.AgentCard `json:"card"`
	NodeID string             `json:"node_id"`
	Online bool               `json:"online"`
}

// NodeEntry 节点总览单条。
type NodeEntry struct {
	NodeID   string `json:"node_id"`
	Online   bool   `json:"online"`
	Epoch    int64  `json:"epoch"`
	Inflight int    `json:"inflight"` // offered+acknowledged 计数（投递窗口占用）
	// MailboxUsed 该 node 信箱内 submitted 任务条数（Loop 7：node 本地控制台
	// 状态页「信箱余量」数据源；余量=MailboxQuota-此值。offer 回退可短暂超额）。
	MailboxUsed int `json:"mailbox_used"`
}

// DirectoryEntry 发现目录单条（SPEC 六.五：GET /api/v1/directory）：exposed=true
// 的 AgentCard + 归属 node 与在线状态；卡不包含本机执行细节
// （command/workdir/agent_args/timeout_sec/allow_env/config_home）——公开声明投影
// （如 allow_args/stdin_to_arg）除外，同 SPEC 六 /api/v1/agents 行口径。
type DirectoryEntry struct {
	Card   protocol.AgentCard `json:"card"`
	NodeID string             `json:"node_id"`
	Online bool               `json:"online"`
}

// ListAgents 全网能力目录（含 exposed=false 的审计登记，调用方以 Exposed 为准入
// 判据；快照 + 在线表读合并，顺序按 node_id/agent 名稳定排序由调用方处理——
// 这里按遍历自然序返回，HTTP 层可直接编码）。
func (c *Core) ListAgents() []AgentEntry {
	view := c.st.View()
	out := make([]AgentEntry, 0, len(view.Nodes))
	for nodeID, rec := range view.Nodes {
		online := c.isOnline(nodeID)
		for _, card := range rec.Agents {
			out = append(out, AgentEntry{Card: card, NodeID: nodeID, Online: online})
		}
	}
	return out
}

// ListDirectory 全网发现目录（SPEC 六.五）：仅 exposed=true 的能力条目（与
// ListAgents 的差异：剔除 exposed=false 审计登记），供控制台派单表单与 node
// 目录代理共用。
func (c *Core) ListDirectory() []DirectoryEntry {
	view := c.st.View()
	out := make([]DirectoryEntry, 0, len(view.Nodes))
	for nodeID, rec := range view.Nodes {
		online := c.isOnline(nodeID)
		for _, card := range rec.Agents {
			if !card.Exposed {
				continue
			}
			out = append(out, DirectoryEntry{Card: card, NodeID: nodeID, Online: online})
		}
	}
	return out
}

// ListTasks 全量任务列表（控制台总览统计与任务监管页数据源）：快照读，创建
// 时间倒序（新任务在前），同刻按 ID 稳定排序。V0 面向回环低量管理面，不分页、
// 不做服务端筛选（前端过滤）。
func (c *Core) ListTasks() []protocol.Task {
	view := c.st.View()
	out := make([]protocol.Task, 0, len(view.Tasks))
	for _, t := range view.Tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ListNodes 节点总览（epoch 为持久化代次；inflight 为投递窗口占用计数）。
func (c *Core) ListNodes() []NodeEntry {
	view := c.st.View()
	out := make([]NodeEntry, 0, len(view.Nodes))
	for nodeID, rec := range view.Nodes {
		out = append(out, NodeEntry{
			NodeID:      nodeID,
			Online:      c.isOnline(nodeID),
			Epoch:       rec.Epoch,
			Inflight:    inflightCount(view, nodeID),
			MailboxUsed: len(view.Mailboxes[nodeID].Tasks),
		})
	}
	return out
}
