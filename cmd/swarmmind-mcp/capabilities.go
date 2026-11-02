// capabilities.go swarmmind_capabilities（W02-02，docs/tasks/v3/W02-mcp-access.md
// 权限矩阵「能力元信息」行）：两 backend 同名装配——hub 面打 hub 管理面
// GET /api/v1/capabilities（internal/hub/api）、node 面打本机同路径端点
// （internal/node/localapi，authRead 只读门）。工具描述写「查看当前调用范围与
// 可用能力」（任务书原话），输出人话：服务端身份/版本/调用范围/功能清单/工具面
// 提示。元信息不是工具清单：输出恒注明「可用工具以 tools/list 实际发现为准，
// 本工具不替代 tools/list」——API 版本与能力标记经端点实查，不硬编码工具数量。
//
// 渲染器双 backend 共用单源实现（同 sessions.go sessionErrHints 单源先例，防
// 双端漂移）：应答两端口同形（server/version/scope/features），差异由 server
// 身份驱动（范围人话与编排授权行只在本机面出现）。
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// capView GET /api/v1/capabilities 应答形态（hub/node 两端口同形契约）。
type capView struct {
	Server   string          `json:"server"`
	Version  string          `json:"version"`
	Scope    string          `json:"scope"`
	Features map[string]bool `json:"features"`
	// Spaces 空间支持投影（W07-01 加法键，hub 面专属——node 面无此键走
	// features.spaces 装配标记，渲染器对 nil 如实跳过不虚构）。
	Spaces *capSpacesView `json:"spaces,omitempty"`
}

// capSpacesView hub 应答 spaces 投影形态（W07-01）。
type capSpacesView struct {
	// Hub 中心空间服务装配态（W07-02 接线前恒 false 如实）。
	Hub bool `json:"hub"`
	// Nodes 节点级声明投影（nodeID → 能力卡是否声明空间特性）。
	Nodes map[string]bool `json:"nodes"`
	// PrivateHistoryReadable 支持情况不表示私有历史可读（恒 false）。
	PrivateHistoryReadable bool `json:"private_history_readable"`
}

// capScopeText 调用范围人话（按 server 身份展开——端点 scope 是短词，工具输出把
// 「能编排到哪」说清，防误把本机面当全网面（或反之）跨机派单被边界锁拒绝）。
var capScopeText = map[string]string{
	"hub":  "hub 全网：可发现并编排网内全部节点上的 exposed 能力（跨机派单、跨机会话、协作群、工作单在此面）",
	"node": "本机：只能发现并编排本机 agent（node 本地 API 边界锁；跨机派单请经 hub 模式）",
}

// capFeatureCN 功能标记人话词典（键与两端点 features 契约同词表；未收录键回退
// 原样键名——服务端新增标记旧客户端仍可见，不静默吞）。
func capFeatureCN(k string) string {
	return map[string]string{
		"tasks":              "任务派发与查询",
		"sessions":           "持久会话",
		"works":              "工作单（多步断点续管）",
		"groups":             "协作群",
		"dispatch_suggest":   "派发建议（决策服务）",
		"notify":             "通知",
		"mcp_access_enabled": "本机 MCP 编排授权",
		"spaces":             "空间",
	}[k]
}

// renderCapabilities 能力元信息人话渲染（hub/node 两 backend 单源共用）。
func renderCapabilities(data []byte) (string, error) {
	var v capView
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	ver := strings.TrimSpace(v.Version)
	if ver == "" {
		ver = "未知" // 端点未注入版本（旧构造方/测试）如实标注，不虚构版本号
	}
	var b strings.Builder
	fmt.Fprintf(&b, "服务端：%s（版本 %s）\n", v.Server, ver)
	if scope, ok := capScopeText[v.Server]; ok {
		fmt.Fprintf(&b, "调用范围：%s\n", scope)
	} else if v.Scope != "" {
		fmt.Fprintf(&b, "调用范围：%s\n", v.Scope)
	}
	// 本机编排授权行（node 面专属标记）：先辨「能不能派」再看「派什么」——
	// 未启用时 submit 类编排 403 mcp_disabled，指路主人控制台（W02-01 授权语义）。
	if enabled, ok := v.Features["mcp_access_enabled"]; ok {
		if enabled {
			b.WriteString("本机 MCP 编排授权：已启用（编排可派）\n")
		} else {
			b.WriteString("本机 MCP 编排授权：未启用（编排派单将被 403 mcp_disabled 拒绝；请由本机主人在控制台启用——调用权与管家角色解耦，启用无需钦定管家）\n")
		}
	}
	// 功能清单按键名排序（输出确定性，便于对比两次实查；未收录键原样展示）。
	keys := make([]string, 0, len(v.Features))
	for k := range v.Features {
		if k == "mcp_access_enabled" { // 授权态已在上方单列，不重复进清单
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("功能清单：\n")
		for _, k := range keys {
			name := capFeatureCN(k)
			if name == "" {
				name = k
			}
			st := "未提供"
			if v.Features[k] {
				st = "可用"
			}
			fmt.Fprintf(&b, "- %s：%s\n", name, st)
		}
	}
	// 空间支持投影（W07-01，hub 面专属键）：中心装配态 + 节点级声明，支持情
	// 况不表示私有历史可读（隐私投影边界随行人话，防「能派发=能翻历史」误读）。
	if v.Spaces != nil {
		declared := 0
		for _, ok := range v.Spaces.Nodes {
			if ok {
				declared++
			}
		}
		state := "未装配（空间操作暂不可用，空间面升级后开放）"
		if v.Spaces.Hub {
			state = "已装配"
		}
		fmt.Fprintf(&b, "空间协作：中心服务%s；已声明空间特性的节点 %d/%d\n", state, declared, len(v.Spaces.Nodes))
		if !v.Spaces.PrivateHistoryReadable {
			b.WriteString("（支持情况只表可承载与可派发，不表示任何一端私有会话历史可读）\n")
		}
	}
	b.WriteString("工具面提示：以上为服务端能力元信息，不替代 tools/list——本连接实际可用的工具以 tools/list 发现结果为准。")
	return b.String(), nil
}

// toolCapabilities hub 面实现（GET /api/v1/capabilities，管理面 s.auth 鉴权）。
func toolCapabilities() (string, error) {
	code, data, err := hubAPI("GET", "/api/v1/capabilities", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("hub 返回 %d: %s", code, truncate(data))
	}
	return renderCapabilities(data)
}

// toolCapabilities node 面实现（GET /api/v1/capabilities，本机 authRead 只读门；
// 单凭证 prime_token 即可读——元信息无敏感）。
func (b nodeBackend) toolCapabilities() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/capabilities", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	return renderCapabilities(data)
}

// capToolDef swarmmind_capabilities 工具定义（hub/node 两清单同文——描述面向
// agent 写「能做什么」，双端同词表防漂移；无参数）。
func capToolDef() toolDef {
	return toolDef{
		Name: "swarmmind_capabilities",
		Description: "查看当前调用范围与可用能力：服务端身份（hub 全网/本机）、版本、功能装配清单（任务/会话/工作单/协作群等）与本机编排授权态。" +
			"能力元信息以服务端实查为准，不替代 tools/list——实际可用工具以 tools/list 发现结果为准",
		InputSchema: toolObj(map[string]any{}),
	}
}
