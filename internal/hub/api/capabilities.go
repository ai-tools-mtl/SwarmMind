// capabilities.go 能力元信息端点（W02-02，docs/tasks/v3/W02-mcp-access.md 权限
// 矩阵「能力元信息」行）：GET /api/v1/capabilities——服务端身份/构建版本/调用
// 范围/功能装配标记，供 MCP swarmmind_capabilities（hub 模式，SPEC 六.六映射表
// 同批行）与外部 agent 发现「我在跟谁说话、当前面能做什么」。
//
// 契约红线：
//   - 元信息只读，响应不含任何私密配置/凭据引用——features 只反映装配布尔
//     （依赖是否接线），不携带 URL/令牌/路径等配置内容；
//   - 功能标记反映**实际装配**：依赖未接线（对应端点组 503 降级）→ false 如实，
//     spaces 恒 false（未实现）——不虚构能力、不硬编码工具数量（工具面以
//     tools/list 实际发现为准，本端点是范围与能力元信息，不是工具清单）；
//   - 版本复用 Config.Version（cmd/hub 经 -X main.version 注入，与登录响应
//     server_version 同源，maturity-version-var）；
//   - 鉴权同既有管理面 s.auth（cookie/PAT）——未认证不可见，与全部管理 API 同门。
package api

import "net/http"

// handleCapabilities GET /api/v1/capabilities：能力元信息（装配态快照，每次请求
// 现读装配字段——无状态缓存，装配变化随进程生命周期如实反映）。
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	core := s.cfg.Core != nil
	writeJSON(w, http.StatusOK, map[string]any{
		"server":  "hub",
		"version": s.cfg.Version,
		"scope":   "hub 全网",
		"features": map[string]bool{
			"tasks":            core, // 任务派发与查询（五端点数据面）
			"sessions":         core, // 持久会话（T2，SPEC 六.五）
			"works":            core, // 工作单（P5，RFC-work-resume）
			"groups":           s.cfg.Groups != nil,
			"dispatch_suggest": s.cfg.Decision != nil, // 派发建议（P5.1 D1；nil 时端点 200+null 降级，此处如实标未装配）
			"notify":           s.cfg.Notify != nil,
			"spaces":           false, // 中心空间服务未装配（W07-02 接线前如实 false 不虚构；节点侧声明见下方 spaces 投影）
		},
		// 空间能力投影（W07-01，加法键既有应答零变化）：当前中心与目标 node 的
		// 支持情况——features.spaces 是 hub 侧服务装配态；nodes 按节点能力表
		// （AgentCard.Spaces 声明）如实投影哪些 node 可承载空间成员线程。本投影
		// 只表「可承载/可派发」，不表示任何一端私有会话历史可读（恒 false，
		// 隐私投影边界）。
		"spaces": s.spacesProjection(),
	})
}

// spacesProjection 空间支持投影（W07-01）：hub 侧装配态（W07-02 前恒 false）+
// 节点级声明投影（Core 未装配时节点表为空 map，如实无数据）。
func (s *Server) spacesProjection() map[string]any {
	nodes := map[string]bool{}
	if s.cfg.Core != nil {
		nodes = s.cfg.Core.SpacesNodeSupport()
	}
	return map[string]any{
		"hub":                      false, // 中心空间服务未装配（W07-02 接线前如实 false——禁空壳承诺）
		"nodes":                    nodes,
		"private_history_readable": false, // 支持情况不表示私有历史可读（恒 false）
	}
}
