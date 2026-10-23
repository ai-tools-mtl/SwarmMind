// Package core 统筹载体指定（2026-10-23 伦哥定案）：网页指定取代能力名硬编码约定。
// 语义：全网同一时刻至多一个统筹（PRIME 任期语义）；指定只存映射（node_id+agent_name），
// 不校验在线——离线时派发端点自然跳过，与普通任务同策略。前端回退：未指定时按能力名
// 含 zcode-dev 识别（向后兼容旧配置），指定后以指定为准。
package core

import (
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
)

// PrimeDesignation 返回当前统筹指定；未指定返回 nil。
func (c *Core) PrimeDesignation() *store.PrimeDesignation {
	cur := c.st.View() // 快照指针，只读安全
	if cur.Prime == nil {
		return nil
	}
	cp := *cur.Prime
	return &cp
}

// SetPrimeDesignation 指定/撤销统筹载体。agentName 为空 = 撤销（回退默认约定）。
// 设置时校验能力目录中存在该 (node_id, agent_name) 条目——软校验（不要求在线），
// 找不到即拒绝，防手滑指定到不存在的能力。by 为操作者（控制台固定 admin）。
func (c *Core) SetPrimeDesignation(nodeID, agentName, by string) (store.PrimeDesignation, error) {
	var out store.PrimeDesignation
	err := c.st.Update(func(st *store.State) error {
		if strings.TrimSpace(agentName) == "" {
			// 撤销：未指定时撤销是幂等空操作（返回当前态即 nil）
			st.Prime = nil
			out = store.PrimeDesignation{}
			return nil
		}
		if strings.TrimSpace(nodeID) == "" {
			return ErrInvalid
		}
		// 精确匹配该 (node_id, agent_name)：全网找同名 exposed 会把指定落到
		// 无此能力的节点上（跨节点同名能力时目测成立、实际不可派发）。
		rec, ok := st.Nodes[nodeID]
		if !ok {
			return ErrInvalid
		}
		found := false
		for _, a := range rec.Agents {
			// 与目录口径一致（ListDirectory=全网 exposed=true）：未暴露的能力不可指定
			if a.Name == agentName && a.Exposed {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalid
		}
		nd := &store.PrimeDesignation{NodeID: nodeID, AgentName: agentName, SetAt: time.Now().UTC().Format(time.RFC3339), SetBy: by}
		st.Prime = nd
		out = *nd
		return nil
	})
	return out, err
}
