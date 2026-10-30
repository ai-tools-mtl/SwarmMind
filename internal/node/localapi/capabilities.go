// capabilities.go 能力元信息端点（W02-02，docs/tasks/v3/W02-mcp-access.md 权限
// 矩阵「能力元信息」行）：GET /api/v1/capabilities——本机身份/构建版本/调用
// 范围/功能装配标记，供 MCP swarmmind_capabilities（node 模式，SPEC 六.六映射表
// 同批行）与外部 agent 发现本机能力与当前编排授权态。
//
// 契约红线：
//   - 元信息只读，响应不含任何凭据/私密配置——mcp_access_enabled 只是授权开关
//     布尔（编排门同源 McpAccess.Enabled()），非凭据非配置内容；
//   - 功能标记反映**实际装配**：会话内核未装配 → sessions=false（与端点 503
//     session_unsupported 同一判定源 Service.SessionsEnabled()）；hub 专属能力
//     （工作单/协作群/派发建议）node 面恒 false——如实标注不虚构；spaces 恒
//     false（未实现）；
//   - 版本复用 Config.Version（cmd/node 经 -X main.version 注入，与
//     /api/v1/local/status 的 server_version 同源，maturity-version-var）；
//   - 鉴权 authRead 只读门（local_token/prime_token 皆可）：元信息无敏感，持任
//     一本机凭证即可读；未认证 401。
//
// SPEC 六.六同步规则判定：新端点但**同一变更内已交付 MCP 工具映射**
// （swarmmind_capabilities node 模式，映射表同批行 + stdio 实测）——同步规则
// ①②③全履行，不属豁免情形（豁免清单仅收「不设 MCP 工具」的人工专属面；
// 对照豁免⑫「既有只读目录加法字段不构成新端点」先例：本端点是新端点而非加法
// 字段，按同步规则正常登记，无需豁免）。
package localapi

import "net/http"

// handleCapabilities GET /api/v1/capabilities：本机能力元信息（装配态快照）。
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"server":  "node",
		"version": s.cfg.Version,
		"scope":   "本机",
		"features": map[string]bool{
			"tasks":    s.cfg.Service != nil,                                    // 本机派单与查询（本地直执行）
			"sessions": s.cfg.Service != nil && s.cfg.Service.SessionsEnabled(), // 与端点 503 降级同一判定源
			// mcp_access_enabled 当前编排授权态（编排门同源；Enabled nil 安全——
			// 未装配按显式默认禁用）。未启用时 prime_token 编排 403 mcp_disabled，
			// 调用方据此先辨「能不能派」再看「派什么」。
			"mcp_access_enabled": s.cfg.McpAccess.Enabled(),
			"works":              false, // hub 专属（工作单账本在 hub，node 模式不装配不静默降级）
			"groups":             false, // hub 专属
			"dispatch_suggest":   false, // hub 专属（决策服务内嵌 hub）
			"notify":             s.cfg.Notify != nil,
			"spaces":             false, // 未实现（规划中），如实标注不虚构
		},
	})
}
