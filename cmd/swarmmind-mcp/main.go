// swarmmind-mcp：SwarmMind 的 MCP（Model Context Protocol）stdio 服务。
//
// 目的（2026-10-21 验收反馈）：让任意 agent（小零/师者/zcode/codex…）通过 MCP
// 直接向 SwarmMind 派任务、等结果、取产物——agent 网络的统一入口。
//
// 协议：MCP stdio transport（newline-delimited JSON-RPC 2.0），手写实现，
// 零第三方依赖（对齐 SPEC 依赖白名单哲学）。
//
// 目标模式（方案 3.4 backend 分叉，2026-10-22）：SWARM_TARGET 选装配
//（backend interface 两实现，工具路由按模式裁剪，不散落 if）：
//
//	SWARM_TARGET=hub（默认，完全向后兼容）——连 hub 管理面：
//	  SWARM_HUB_URL   管理面地址，如 http://127.0.0.1:8444
//	  SWARM_HUB_TOKEN 访问令牌（smsw_… PAT 或 admin_token）
//	SWARM_TARGET=node——连本机 node（standalone 单机编排入口，端点契约
//	  对照 internal/node/localapi；stdio 传输天然仅本机可达）：
//	  SWARM_NODE_URL         本机 node 地址（默认 http://127.0.0.1:8445）
//	  SWARM_NODE_PRIME_TOKEN prime_token（唯一凭证：编排 submit + 只读六工具
//	    全走它——安全审查修复 1：不再读取 SWARM_NODE_TOKEN，local_token 是本机
//	    主人管理凭证（declare/rotate/restart/PUT config），不随总理连接下发；
//	    prime 认证级 ⊇ 只读，单凭证覆盖全部工具）
//
// 通用（两模式）：
//
//	SWARM_STARTLUX_URL          StartLux-Decision 云端地址（默认 https://api.bamboonode.dev）
//	SWARM_STARTLUX_KEY          Bamboo API key（空 = L0 视为不可用，直接走 L2 规则分配）
//	SWARM_STARTLUX_MODEL        决策模型（默认 startlux-decision-27b-autothink）
//	SWARM_STARTLUX_TIMEOUT_SEC  云端查询单次超时秒数（默认 5）
//
// 工具：hub 模式 12 个（不变）——nodes / agents / submit / status / wait /
// artifact / decide + group_create / group_list / group_say / group_messages /
// group_close（群聊协作空间，GROUP-RFC 六 MVP）。node 模式 6 个——nodes（本机
// 节点信息单条）/ agents（本机目录）/ submit（本机派单，prime_token）/ status /
// wait / artifact（本机直取）。
// 等待语义：wait 轮询至终态；needs_review 作为终态返回并明确提示需人工核对。
// swarmmind_decide（StartLux 归属，PRIME-DESIGN 三级降级）：L0 云端 StartLux 建议 →
// 失败自动降级 L2 本地规则分配（关键词匹配 + inflight 负载决胜），永不停摆，响应标注 used_level。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	hubURL   string
	hubToken string
	client   = &http.Client{Timeout: 35 * time.Second}
)

// StartLux-Decision 云端决策配置（main 启动时读环境变量；startluxClient 为带
// 独立超时的客户端——不用 hub client，L0 超时预算与 hub 调用互不牵连）。
var (
	startluxURL     = "https://api.bamboonode.dev"
	startluxKey     string
	startluxModel   = "startlux-decision-27b-autothink"
	startluxTimeout = 5 * time.Second
	startluxClient  *http.Client
)

func envDefault(key, def string) string { return envGet(os.Getenv, key, def) }

// envGet getenv 参数化的环境变量读取（trim 后取缺省；getenv 注入便于测试装配）。
func envGet(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

/* ---------- MCP 信封 ---------- */

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

/* ---------- hub API 客户端 ---------- */

func hubAPI(method, path string, body any) (int, []byte, error) {
	return httpCall(hubURL, hubToken, client, method, path, body)
}

// httpCall 通用 REST 调用（hub/node 两后端共用）：Bearer 鉴权 + JSON 体 +
// 8MiB 应答上限。client 由调用方给定（hub 共享全局 client，node 自带）。
func httpCall(baseURL, token string, cl *http.Client, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data, err
}

/* ---------- backend 分叉（方案 3.4）：工具清单 + 调用分发按模式装配 ---------- */

// backend MCP 后端：hub（全网管理面）与 node（本机编排入口）两实现。工具路由
// 按模式整体裁剪，不在工具清单里散落 if（zcode 审查 #6）。
type backend interface {
	// tools 本模式工具清单（tools/list 应答体）。
	tools() []toolDef
	// call 单工具分发（tools/call 应答体；rpcErr 非 nil 时回协议错误）。
	call(name string, args map[string]any) (map[string]any, *rpcError)
	// initResult initialize 应答体（node 模式附 instructions 说明总理语义）。
	initResult() map[string]any
}

// hubBackend hub 模式（默认）：全网派发中枢管理面，现有 12 工具行为零变化。
type hubBackend struct{}

func (hubBackend) tools() []toolDef { return tools() }

func (hubBackend) call(name string, args map[string]any) (map[string]any, *rpcError) {
	return callTool(name, args)
}

func (hubBackend) initResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "swarmmind", "version": "0.1.1"},
	}
}

// nodeBackend node 模式（standalone 单机编排入口，方案 3.4）：全部工具直连本机
// 回环 localapi（端点契约见 internal/node/localapi），不经 hub。接入即总理——
// 六工具全走 SWARM_NODE_PRIME_TOKEN 单凭证（安全审查修复 1：编排 submit 与只读
// 查询共用；local_token 是本机主人管理凭证，不随总理连接下发）。
type nodeBackend struct {
	baseURL    string // 本机 node 地址（SWARM_NODE_URL，默认 http://127.0.0.1:8445）
	primeToken string // prime_token（唯一凭证：submit 编排 + 只读工具；localapi authRead 同收）
	client     *http.Client
}

// newNodeBackend 从环境变量装配 node 后端（getenv 注入便于测试）。
func newNodeBackend(getenv func(string) string) nodeBackend {
	return nodeBackend{
		baseURL:    strings.TrimRight(envGet(getenv, "SWARM_NODE_URL", "http://127.0.0.1:8445"), "/"),
		primeToken: strings.TrimSpace(getenv("SWARM_NODE_PRIME_TOKEN")),
		client:     &http.Client{Timeout: client.Timeout},
	}
}

func (nodeBackend) initResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "swarmmind-node", "version": "0.1.1"},
		"instructions": "SwarmMind 本机编排入口（接入即统筹：本机编排）。工具只作用于本机节点：" +
			"全部工具共用统筹凭证 SWARM_NODE_PRIME_TOKEN（submit 编排 + 只读查询；node.yaml 声明 prime_agent 后编排接口才激活）。" +
			"派单目标只能是本机 agent（node 本地 API 边界锁），跨机派单请经 hub 管理面。",
	}
}

// nodeTools node 模式工具清单：本机六件套（全网目录/群聊/decide 为 hub 专属）。
func (nodeBackend) tools() []toolDef {
	return []toolDef{
		{Name: "swarmmind_nodes", Description: "查看本机节点信息（单条）：节点 ID、形态（setup/standalone/组网）、本机统筹、hub 连接与执行账本三态计数", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_agents", Description: "查看本机可用能力（agent 目录）：本机提供哪些可调用的 agent、各自描述与今日执行次数", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_submit", Description: "向本机 agent 派发任务（接入即统筹：本机编排，需 SWARM_NODE_PRIME_TOKEN；返回 403 prime_required 即统筹未钦定或凭证不符）。返回任务 ID，用 swarmmind_wait 等结果；传 idem_key 重试不重复执行", InputSchema: toolObj(map[string]any{
			"agent":    toolStr("目标 agent 名（swarmmind_agents 可查；只能是本机 agent）"),
			"args":     toolArr("命令行参数（可选；agent 须 allow_args）"),
			"stdin":    toolStr("任务指令正文/标准输入（可选，推荐把完整任务描述写在这里）"),
			"idem_key": toolStr("幂等键（可选；同键同内容重试不会重复执行）"),
		}, "agent")},
		{Name: "swarmmind_status", Description: "查询本机任务当前状态与结果（不等待）", InputSchema: toolObj(map[string]any{
			"task_id": toolStr("任务 ID"),
		}, "task_id")},
		{Name: "swarmmind_wait", Description: "等待本机任务完成并返回最终结果（轮询至终态：completed/failed/needs_review/cancelled）", InputSchema: toolObj(map[string]any{
			"task_id":      toolStr("任务 ID"),
			"timeout_sec":  toolNum("最长等待秒数（默认 300，上限 1800）"),
			"interval_sec": toolNum("轮询间隔秒数（默认 3）"),
		}, "task_id")},
		{Name: "swarmmind_artifact", Description: "取回本机任务产物文件内容（本机直取不经 hub 中继；文本类产物直接返回，任务需已完成且有产物登记）", InputSchema: toolObj(map[string]any{
			"task_id": toolStr("任务 ID"),
			"index":   toolNum("产物序号（从 0 开始，默认 0）"),
		}, "task_id")},
	}
}

/* ---------- 工具定义 ---------- */

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// 工具 schema 助手（hub/node 两清单共用）。
func toolObj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func toolStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func toolNum(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

func toolArr(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func tools() []toolDef {
	return []toolDef{
		{Name: "swarmmind_nodes", Description: "查看 SwarmMind 网内节点：哪些机器在线、各有多少进行中任务", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_agents", Description: "查看全网可用能力（agent 目录）：每台节点上开放了哪些可调用的 agent", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_submit", Description: "向指定节点的指定 agent 派发任务。返回任务 ID；用 swarmmind_wait 等结果。传 group_ref 可把任务挂到协作群：创建时群里出现系统消息，任务终态结果自动回流群", InputSchema: toolObj(map[string]any{
			"node_id":   toolStr("目标节点 ID（swarmmind_nodes 可查）"),
			"agent":     toolStr("目标 agent 名（swarmmind_agents 可查）"),
			"args":      toolArr("命令行参数（可选）"),
			"stdin":     toolStr("任务指令正文/标准输入（可选，推荐把完整任务描述写在这里）"),
			"trace_id":  toolStr("追踪号（可选，便于关联；统筹任期建议 prime-<任期号>-<序号>）"),
			"idem_key":  toolStr("幂等键（可选；同键同内容重试不会重复执行，重派时沿用原键）"),
			"group_ref": toolStr("协作群 ID（可选；swarmmind_group_list 可查。任务与群关联：群里注入创建系统消息，终态结果自动回流该群）"),
			"depends_on": toolArr("上游任务幂等键列表（可选，P4.3 任务依赖）：全部上游 completed 后本任务才投递；上游 failed/needs_review/cancelled 则保持阻塞（blocked_reason=upstream_failed）。请传上游 idem_key（跨重跑稳定），勿传 task_id"),
		}, "node_id", "agent")},
		{Name: "swarmmind_status", Description: "查询任务当前状态与结果（不等待）", InputSchema: toolObj(map[string]any{
			"task_id": toolStr("任务 ID"),
		}, "task_id")},
		{Name: "swarmmind_wait", Description: "等待任务完成并返回最终结果（轮询至终态：completed/failed/needs_review/cancelled）", InputSchema: toolObj(map[string]any{
			"task_id":      toolStr("任务 ID"),
			"timeout_sec":  toolNum("最长等待秒数（默认 300，上限 1800）"),
			"interval_sec": toolNum("轮询间隔秒数（默认 3）"),
		}, "task_id")},
		{Name: "swarmmind_artifact", Description: "取回任务产物文件内容（文本类产物；任务需已完成且有产物登记）", InputSchema: toolObj(map[string]any{
			"task_id": toolStr("任务 ID"),
			"index":   toolNum("产物序号（从 0 开始，默认 0）"),
		}, "task_id")},
		{Name: "swarmmind_decide", Description: "分配决策查询：给定子任务描述与候选执行者列表，返回推荐执行者与置信度；云端决策模型（StartLux）不可用时自动降级本地规则，响应含 used_level 与 degraded 标记", InputSchema: toolObj(map[string]any{
			"subtask_description": toolStr("子任务描述（做什么，越具体越准）"),
			"capability_options":  toolArr("候选执行者列表，node_id/agent 形式（如 mac-mini/diskcheck），至少 2 项"),
		}, "subtask_description", "capability_options")},
		{Name: "swarmmind_group_create", Description: "创建协作群：围绕一个目标把多个 agent 拉进同一持续讨论空间（有 seq 消息账本，可增量拉取）。返回群 ID", InputSchema: toolObj(map[string]any{
			"goal":    toolStr("群目标（一句话说清要讨论出什么）"),
			"members": toolArr("成员列表（agent 或人的标识，如 [\"coder\",\"reviewer\"]）"),
		}, "goal")},
		{Name: "swarmmind_group_list", Description: "查看全部协作群：群 ID、目标、状态（discussing 进行中 / concluded 已结束）、成员与发言预算", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_group_say", Description: "在协作群里发一条消息。默认按 user（人转述）发言；agent 参与讨论请传 kind=agent（计入发言预算，默认 20 轮，用尽返回 budget_exhausted）", InputSchema: toolObj(map[string]any{
			"group_id": toolStr("群 ID（swarmmind_group_list 可查）"),
			"from":     toolStr("发言者标识（谁在说话，如 coder / admin）"),
			"body":     toolStr("消息正文（8KiB 上限）"),
			"kind":     toolStr("消息类别（可选：user|agent，默认 user；system 为 hub 回流保留字，传入将被 400 拒绝）"),
		}, "group_id", "from", "body")},
		{Name: "swarmmind_group_messages", Description: "拉取协作群消息：给 since（已见到的最大 seq）只返回之后的新消息，适合轮询补齐。群消息滚动只保留最新 200 条（GROUP-RFC 独立信箱约定），更早消息不可再拉，响应会标注裁剪位置 pruned_before_seq", InputSchema: toolObj(map[string]any{
			"group_id": toolStr("群 ID"),
			"since":    toolNum("已见最大 seq（默认 0=全量；传上次拉到的最大序号即增量）"),
		}, "group_id")},
		{Name: "swarmmind_group_close", Description: "结束协作群：置 concluded 终态（不可回退、不再收消息）；重复关闭幂等。关闭者默认记为 mcp，可传 by 自定义审计署名", InputSchema: toolObj(map[string]any{
			"group_id": toolStr("群 ID"),
			"by":       toolStr("关闭者标识（可选，默认 mcp；记入群审计 closed_by，重复关闭不改写首次记录）"),
		}, "group_id")},
	}
}

/* ---------- 工具实现 ---------- */

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func callResult(text string) map[string]any {
	return map[string]any{"content": []textContent{{Type: "text", Text: text}}, "isError": false}
}
func errResult(text string) map[string]any {
	return map[string]any{"content": []textContent{{Type: "text", Text: text}}, "isError": true}
}

// pretty 紧凑 JSON（一层缩进可读）。
func pretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func toolNodes() (string, error) {
	code, data, err := hubAPI("GET", "/api/v1/nodes", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("hub 返回 %d: %s", code, truncate(data))
	}
	var out struct {
		Nodes []struct {
			NodeID   string `json:"node_id"`
			Online   bool   `json:"online"`
			Epoch    int    `json:"epoch"`
			Inflight int    `json:"inflight"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(data, &out)
	var b strings.Builder
	for _, nd := range out.Nodes {
		st := "离线"
		if nd.Online {
			st = "在线"
		}
		fmt.Fprintf(&b, "%s：%s（进行中 %d）\n", nd.NodeID, st, nd.Inflight)
	}
	if b.Len() == 0 {
		return "网内还没有节点接入。", nil
	}
	return b.String(), nil
}

func toolAgents() (string, error) {
	code, data, err := hubAPI("GET", "/api/v1/directory", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("hub 返回 %d: %s", code, truncate(data))
	}
	var out struct {
		Directory []struct {
			NodeID string `json:"node_id"`
			Card   struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Kind        string `json:"kind"`
			} `json:"card"`
		} `json:"directory"`
	}
	_ = json.Unmarshal(data, &out)
	if len(out.Directory) == 0 {
		return "全网还没有开放的能力（agent 未 exposed）。", nil
	}
	var b strings.Builder
	for _, d := range out.Directory {
		desc := d.Card.Description
		if desc == "" {
			desc = "（无描述）"
		}
		fmt.Fprintf(&b, "%s @ %s：%.120s\n", d.Card.Name, d.NodeID, desc)
	}
	return b.String(), nil
}

// depArgs 解析 depends_on 工具参数（P4.3 任务依赖透传）：JSON 数组 → 去空白
// 字符串列表（空项剔除；全空返回 nil 不下发字段）。
func depArgs(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	deps := make([]string, 0, len(arr))
	for _, v := range arr {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			deps = append(deps, s)
		}
	}
	return deps
}

func toolSubmit(args map[string]any) (string, error) {
	node, _ := args["node_id"].(string)
	agent, _ := args["agent"].(string)
	if node == "" || agent == "" {
		return "", fmt.Errorf("node_id 与 agent 必填")
	}
	input := map[string]any{}
	if raw, ok := args["args"].([]any); ok {
		ss := make([]string, 0, len(raw))
		for _, v := range raw {
			ss = append(ss, fmt.Sprint(v))
		}
		input["args"] = ss
	}
	if stdin, ok := args["stdin"].(string); ok && stdin != "" {
		input["stdin"] = stdin
	}
	body := map[string]any{
		"target_node":  node,
		"target_agent": agent,
		"input":        input,
		"submitted_by": "mcp",
	}
	if tid, ok := args["trace_id"].(string); ok && tid != "" {
		body["trace_id"] = tid
	}
	if idem, ok := args["idem_key"].(string); ok && idem != "" {
		body["idem_key"] = idem
	}
	if gr, ok := args["group_ref"].(string); ok && gr != "" {
		body["group_ref"] = gr
	}
	if deps := depArgs(args["depends_on"]); len(deps) > 0 {
		body["depends_on"] = deps
	}
	code, data, err := hubAPI("POST", "/api/v1/tasks", body)
	if err != nil {
		return "", err
	}
	if code != 200 && code != 201 && code != 202 {
		return "", fmt.Errorf("提交失败（hub %d）: %s", code, truncate(data))
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(data, &out)
	return fmt.Sprintf("任务已提交：%s（目标 %s @ %s）\n用 swarmmind_status 或 swarmmind_wait 查询结果。", out.TaskID, agent, node), nil
}

// taskStatus 拉取并汇总单个任务的可读状态（hub 模式入口）。行为保持不变
//（node 模式见 nodeBackend.taskStatus——功能性修复 8 后两后端错误语义分叉）。
func taskStatus(taskID string) (string, bool, error) {
	code, data, err := hubAPI("GET", "/api/v1/tasks/"+taskID, nil)
	if err != nil {
		// hub 既有语义：网络错误非终态、非 error——waitLoop 继续轮到超时。
		return "查询失败：" + err.Error(), false, nil
	}
	if code == 404 {
		return "任务不存在或已归档（终态保留 24 小时）。", true, nil
	}
	if code != 200 {
		return fmt.Sprintf("hub 返回 %d: %s", code, truncate(data)), true, nil
	}
	text, done := renderTaskOK(taskID, data)
	return text, done, nil
}

// renderTaskOK HTTP 200 任务视图渲染（hub/node 双后端共用；done=true 表示已到
// 终态 completed/failed/needs_review/cancelled）。HTTP 错误分支在各自后端的
// taskStatus 里（修复 8：node 侧 4xx/5xx 一律 error 透传，不再当终态）。
func renderTaskOK(taskID string, data []byte) (string, bool) {
	var t struct {
		Status        string   `json:"status"`
		TargetAgent   string   `json:"target_agent"`
		TargetNode    string   `json:"target_node"`
		DependsOn     []string `json:"depends_on"`
		BlockedReason string   `json:"blocked_reason"`
		SupersededBy  string   `json:"superseded_by"`
		Result        *struct {
			Status      string `json:"status"`
			Error       string `json:"error"`
			ExitCode    *int   `json:"exit_code"`
			FinalAnswer string `json:"final_answer"`
			StdoutTail  string `json:"stdout_tail"`
			Artifacts   []struct {
				Path string `json:"path"`
				Size int    `json:"size"`
			} `json:"artifacts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return "结果解析失败：" + err.Error(), true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "任务 %s（%s @ %s）\n状态：%s\n", taskID, t.TargetAgent, t.TargetNode, statusCN(t.Status))
	// 依赖门控展示字段（P4.3/SPEC 六.六规则 7：blocked/superseded 人话提示；
	// blocked 不进状态机，任务仍 submitted——wait 继续轮询至放行或超时）
	if len(t.DependsOn) > 0 {
		fmt.Fprintf(&b, "依赖：%s\n", strings.Join(t.DependsOn, "、"))
	}
	switch {
	case strings.HasPrefix(t.BlockedReason, "blocked_by:"):
		fmt.Fprintf(&b, "依赖等待：上游 %s 尚未完成，本任务暂不派发；全部上游完成后自动执行。\n",
			strings.TrimPrefix(t.BlockedReason, "blocked_by:"))
	case strings.HasPrefix(t.BlockedReason, "upstream_failed:"):
		fmt.Fprintf(&b, "依赖阻塞：上游 %s 已失败/待人工核对/被取消，本任务不会自动执行；请人工决定是否重派上游。\n",
			strings.TrimPrefix(t.BlockedReason, "upstream_failed:"))
	}
	if t.SupersededBy != "" {
		fmt.Fprintf(&b, "上游已重跑：新版本任务 %s 已完成；本任务保持阻塞不自动放行，请人工决定是否重派（swarmmind_status 查新版本结果）。\n", t.SupersededBy)
	}
	if r := t.Result; r != nil {
		if r.Error != "" {
			fmt.Fprintf(&b, "中断原因：%s\n", r.Error)
		}
		if r.ExitCode != nil {
			fmt.Fprintf(&b, "退出码：%d\n", *r.ExitCode)
		}
		if r.FinalAnswer != "" {
			fmt.Fprintf(&b, "\n最终答复：\n%.4000s\n", r.FinalAnswer)
		} else if r.StdoutTail != "" {
			fmt.Fprintf(&b, "\n输出（节选）：\n%.2000s\n", r.StdoutTail)
		}
		if len(r.Artifacts) > 0 {
			fmt.Fprintf(&b, "\n产物 %d 项：\n", len(r.Artifacts))
			for i, a := range r.Artifacts {
				fmt.Fprintf(&b, "  [%d] %s（%d 字节）—— swarmmind_artifact 可取回\n", i, a.Path, a.Size)
			}
		}
	}
	if t.Status == "needs_review" {
		b.WriteString("\n注意：结果不确定（执行中节点重启），需人工核对，请勿凭此结果下结论。\n")
	}
	if t.Status == "cancelled" {
		b.WriteString("\n注意：此任务已被管理员取消，进程已整组终止，不会自动重跑；请勿凭此结果下结论。\n")
	}
	return b.String(), t.Status == "completed" || t.Status == "failed" || t.Status == "needs_review" || t.Status == "cancelled"
}

func toolWait(args map[string]any) (string, error) {
	return waitLoop(args, taskStatus)
}

// waitLoop 轮询等待至终态（hub/node 双后端共用；status 各自后端注入）。
// status 返回 error（功能性修复 8）：node 后端的 HTTP 4xx/5xx（含 401/403/404）
// 属查询失败而非任务终态——立即以 error 上抛（tools/call 层 isError=true 返回），
// 绝不当作终态结束条件或继续空转轮询。
func waitLoop(args map[string]any, status func(string) (string, bool, error)) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id 必填")
	}
	timeout := 300
	if v, ok := args["timeout_sec"].(float64); ok && v > 0 {
		timeout = int(v)
	}
	if timeout > 1800 {
		timeout = 1800
	}
	interval := 3
	if v, ok := args["interval_sec"].(float64); ok && v >= 1 {
		interval = int(v)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		text, done, err := status(taskID)
		if err != nil {
			return "", err
		}
		if done {
			return text, nil
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("等待超时（%d 秒）——任务仍在进行中。\n%s", timeout, text), nil
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func toolArtifact(args map[string]any) (string, error) {
	taskID, _ := args["task_id"].(string)
	idx := 0
	if v, ok := args["index"].(float64); ok {
		idx = int(v)
	}
	if taskID == "" {
		return "", fmt.Errorf("task_id 必填")
	}
	code, data, err := hubAPI("GET", fmt.Sprintf("/api/v1/tasks/%s/artifacts/%d", taskID, idx), nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		reason := map[int]string{
			404: "产物不存在（任务无终态结果/下标越界/已过保留期）",
			409: "SHA-256 校验不一致，已禁止交付",
			503: "执行节点不在线，暂无法中继取回",
			504: "中继取回超时",
		}[code]
		if reason == "" {
			reason = truncate(data)
		}
		return "", fmt.Errorf("取回失败（hub %d）：%s", code, reason)
	}
	return renderArtifact(data), nil
}

// renderArtifact 产物内容渲染（hub/node 双后端共用）：文本类直接返回；二进制给提示。
func renderArtifact(data []byte) string {
	if isText(data) {
		return fmt.Sprintf("产物内容（%d 字节）：\n%s", len(data), truncateBytes(data, 8000))
	}
	return fmt.Sprintf("产物为二进制（%d 字节），MCP 文本通道不展示；请经控制台取回。", len(data))
}

func isText(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

/* ---------- node 模式工具实现（本机回环 localapi 直连，方案 3.4） ---------- */

// nodeAPI 本机 node API 调用：tok 由调用方传入（唯一凭证 prime_token——修复 1
// 后只读与编排不再分票）。
func (b nodeBackend) nodeAPI(method, path string, body any, tok string) (int, []byte, error) {
	return httpCall(b.baseURL, tok, b.client, method, path, body)
}

// nodeAPIErr node 端非 2xx → error：服务端 JSON 错误体 {error,message} 的原话
// 透传；403 prime_required 特判附「检查总理配置」提示（接入即总理的故障入口）。
func nodeAPIErr(code int, data []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	if e.Error == "prime_required" {
		msg := e.Message
		if msg == "" {
			msg = truncate(data)
		}
		return fmt.Errorf("本机 node 403 prime_required：%s\n请检查统筹配置：node.yaml 已声明 prime_agent，且 SWARM_NODE_PRIME_TOKEN 与本机 data_dir/prime_token 一致（轮换后须同步新凭证）。", msg)
	}
	if e.Message != "" {
		return fmt.Errorf("本机 node 返回 %d（%s）：%s", code, e.Error, e.Message)
	}
	return fmt.Errorf("本机 node 返回 %d: %s", code, truncate(data))
}

// toolNodeNodes 本机节点信息（GET /api/v1/local/status，单条）。
func (b nodeBackend) toolNodes() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/local/status", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var st struct {
		NodeID     string         `json:"node_id"`
		Mode       string         `json:"mode"`
		PrimeAgent string         `json:"prime_agent"`
		Online     bool           `json:"online"`
		Epoch      int64          `json:"epoch"`
		Ledger     map[string]int `json:"ledger"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	mode := map[string]string{
		"setup":      "setup（未接入，仅向导）",
		"standalone": "standalone（本机自治，不连 hub）",
		"networked":  "组网（外连 hub）",
	}[st.Mode]
	if mode == "" {
		mode = st.Mode
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "本机节点 %s\n形态：%s\nhub 连接：%s（epoch %d）\n", st.NodeID, mode, map[bool]string{true: "在线", false: "离线"}[st.Online], st.Epoch)
	if st.PrimeAgent != "" {
		fmt.Fprintf(&bld, "本机统筹：%s（编排接口已激活，接入即统筹）\n", st.PrimeAgent)
	} else {
		bld.WriteString("本机统筹：未钦定（编排接口未激活，submit 将 403 prime_required）\n")
	}
	fmt.Fprintf(&bld, "执行账本三态：accepted %d / started %d / finished %d",
		st.Ledger["accepted"], st.Ledger["started"], st.Ledger["finished"])
	return bld.String(), nil
}

// toolNodeAgents 本机能力目录（GET /api/v1/local/agents：完整 ExecProfile 视图 +
// 今日执行数；node 无全网视图）。
func (b nodeBackend) toolAgents() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/local/agents", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		NodeID string `json:"node_id"`
		Agents []struct {
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			Command    string `json:"command"`
			TimeoutSec int    `json:"timeout_sec"`
			AllowArgs  bool   `json:"allow_args"`
			AllowEnv   []string `json:"allow_env"`
			Desc       string `json:"description"`
			Exposed    bool   `json:"exposed"`
			TodayTasks int    `json:"today_tasks"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Agents) == 0 {
		return fmt.Sprintf("本机 %s 还没有配置任何 agent（node.yaml agents 段为空）。", out.NodeID), nil
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "本机 %s 共 %d 个 agent：\n", out.NodeID, len(out.Agents))
	for _, a := range out.Agents {
		desc := a.Desc
		if desc == "" {
			desc = "（无描述）"
		}
		vis := "仅本机"
		if a.Exposed {
			vis = "网内可调用"
		}
		fmt.Fprintf(&bld, "%s（%s，%s）：%.120s\n  command %s · 超时 %ds · 今日已执行 %d 次\n",
			a.Name, a.Kind, vis, desc, a.Command, a.TimeoutSec, a.TodayTasks)
	}
	return bld.String(), nil
}

// toolNodeSubmit 本机派单（POST /api/v1/tasks，prime_token 编排门）：target_node
// 自动补齐本机 node_id（node 本地 API 边界锁只收本机目标，调用方无需知道节点名）。
func (b nodeBackend) toolSubmit(args map[string]any) (string, error) {
	agent, _ := args["agent"].(string)
	if strings.TrimSpace(agent) == "" {
		return "", fmt.Errorf("agent 必填（目标只能是本机 agent）")
	}
	if b.primeToken == "" {
		return "", fmt.Errorf("未配置 SWARM_NODE_PRIME_TOKEN：本机编排需统筹凭证（接入即统筹：本机编排）")
	}
	nodeID, err := b.localNodeID()
	if err != nil {
		return "", err
	}
	input := map[string]any{}
	if raw, ok := args["args"].([]any); ok {
		ss := make([]string, 0, len(raw))
		for _, v := range raw {
			ss = append(ss, fmt.Sprint(v))
		}
		input["args"] = ss
	}
	if stdin, ok := args["stdin"].(string); ok && stdin != "" {
		input["stdin"] = stdin
	}
	body := map[string]any{
		"target_node":  nodeID,
		"target_agent": agent,
		"input":        input,
		"submitted_by": "mcp",
	}
	if idem, ok := args["idem_key"].(string); ok && idem != "" {
		body["idem_key"] = idem
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/tasks", body, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 && code != 202 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Dup    bool   `json:"dup"`
	}
	_ = json.Unmarshal(data, &out)
	note := ""
	if out.Dup {
		note = "（幂等命中原任务，未重复执行）"
	}
	return fmt.Sprintf("任务已提交：%s%s（目标 %s @ 本机 %s）\n用 swarmmind_status 或 swarmmind_wait 查询结果。", out.TaskID, note, agent, nodeID), nil
}

// localNodeID 本机身份（GET /api/v1/local/status 的 node_id；submit 边界锁补齐用）。
func (b nodeBackend) localNodeID() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/local/status", nil, b.primeToken)
	if err != nil {
		return "", fmt.Errorf("本机 node 不可达（%v）", err)
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var st struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.NodeID == "" {
		return "", fmt.Errorf("本机 node 应答缺 node_id")
	}
	return st.NodeID, nil
}

// taskStatus 本机任务视图（GET /api/v1/tasks/{id}，账本直出；与 hub 管理面同
// 协议层 Task 形态，渲染共用 renderTaskOK）。
// 错误语义（功能性修复 8，2026-10-22）：HTTP 4xx/5xx（含 401/403/404）与网络
// 错误一律作为 error 上抛——tools/call 层 isError=true 返回、message 透传服务端
// 原话（nodeAPIErr）；wait 轮询遇之立即返回，不得当作终态。与 hub 后端分叉：
// hub 侧 404/非 200 沿用既有「文本+done」语义（wait 超时兜底）。
func (b nodeBackend) taskStatus(taskID string) (string, bool, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/tasks/"+taskID, nil, b.primeToken)
	if err != nil {
		return "", false, fmt.Errorf("本机 node 不可达（%v）", err)
	}
	if code != 200 {
		return "", false, nodeAPIErr(code, data)
	}
	text, done := renderTaskOK(taskID, data)
	return text, done, nil
}

// toolNodeArtifact 本机直取产物（GET /api/v1/tasks/{id}/artifacts/{index}，
// application/octet-stream；错误为 JSON 体，原话透传）。
func (b nodeBackend) toolArtifact(args map[string]any) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id 必填")
	}
	idx := 0
	if v, ok := args["index"].(float64); ok {
		idx = int(v)
	}
	code, data, err := b.nodeAPI("GET", fmt.Sprintf("/api/v1/tasks/%s/artifacts/%d", taskID, idx), nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	return renderArtifact(data), nil
}

// call 本机六工具分发（未知工具回协议错误 -32602，与 hub 同型）。
// 错误语义（功能性修复 8，2026-10-22）：node 后端一切 error（含 HTTP 401/403/404
// 等本机 node 4xx/5xx 原话透传）→ isError=true；无「HTTP 错误当正常结果回文」
// 的路径。无 err 且文本非空 → isError=false。
func (b nodeBackend) call(name string, args map[string]any) (map[string]any, *rpcError) {
	var (
		text string
		err  error
	)
	switch name {
	case "swarmmind_nodes":
		text, err = b.toolNodes()
	case "swarmmind_agents":
		text, err = b.toolAgents()
	case "swarmmind_submit":
		text, err = b.toolSubmit(args)
	case "swarmmind_status":
		if id, _ := args["task_id"].(string); id == "" {
			return errResult("task_id 必填"), nil
		} else {
			text, _, err = b.taskStatus(id)
		}
	case "swarmmind_wait":
		text, err = waitLoop(args, b.taskStatus)
	case "swarmmind_artifact":
		text, err = b.toolArtifact(args)
	default:
		return nil, &rpcError{Code: -32602, Message: "未知工具: " + name}
	}
	if err != nil {
		return errResult("操作失败：" + err.Error()), nil
	}
	return callResult(text), nil
}

/* ---------- swarmmind_decide：总理分配决策（L0 云端 → L2 规则兜底） ---------- */

// 设计依据 PRIME-DESIGN 三级降级策略与影子原则：L0（bamboonode 云端
// StartLux-Decision）默认；L0 任一环失败（key 空/超时/非 200/解析失败）自动降级
// L2 本地规则分配（能力目录关键词匹配 + inflight 最低决胜），永不停摆；
// 响应统一标注 used_level 与 degraded，rationale 说明降级原因（影子数据留痕）。

// decideOutcome 统一响应结构（L0 额外带 probabilities；L2 confidence 固定 0.5）。
type decideOutcome struct {
	UsedLevel     string             `json:"used_level"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Rationale     string             `json:"rationale"`
	Degraded      bool               `json:"degraded"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

func toolDecide(args map[string]any) (string, error) {
	subtask, _ := args["subtask_description"].(string)
	if strings.TrimSpace(subtask) == "" {
		return "", fmt.Errorf("subtask_description 必填")
	}
	raw, _ := args["capability_options"].([]any)
	options := make([]string, 0, len(raw))
	for _, v := range raw {
		sv, ok := v.(string)
		if !ok || strings.TrimSpace(sv) == "" {
			return "", fmt.Errorf("capability_options 必须为非空字符串数组")
		}
		options = append(options, strings.TrimSpace(sv))
	}
	if len(options) < 2 {
		return "", fmt.Errorf("capability_options 至少 2 项（候选执行者）")
	}
	out, err := swarmmindDecide(subtask, options)
	if err != nil {
		return "", err
	}
	return pretty(out), nil
}

func swarmmindDecide(subtask string, options []string) (decideOutcome, error) {
	if startluxKey != "" {
		out, err := decideL0(subtask, options)
		if err == nil {
			return out, nil
		}
		// L0 任一失败（超时/非 200/解析失败）→ 降级 L2，不在云端重试（5s 预算内快速兜底）。
		return decideL2(subtask, options, err.Error())
	}
	return decideL2(subtask, options, "SWARM_STARTLUX_KEY 未配置")
}

// decideL0 POST {URL}/v1/decisions：state=网内状态+子任务 JSON 文本，
// questions.target=choice（criteria 以候选为键，尽力附上能力描述辅助云端判断）。
func decideL0(subtask string, options []string) (decideOutcome, error) {
	state := map[string]any{
		"subtask":    map[string]any{"description": subtask},
		"candidates": options,
	}
	if nodes, err := fetchDecideNodes(); err == nil {
		state["nodes"] = nodes // 网内负载/在线状态尽力附带，失败不影响查询
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return decideOutcome{}, err
	}
	criteria := map[string]string{}
	agents, _ := fetchDecideAgents() // 同上，描述缺失时以候选原文为描述
	for _, opt := range options {
		criteria[opt] = decideCapabilityText(opt, agents)
	}
	body := map[string]any{
		"model": startluxModel,
		"state": string(stateBytes),
		"questions": map[string]any{
			"target": map[string]any{
				"type":         "choice",
				"instructions": "该子任务应派给哪个执行者？",
				"criteria":     criteria,
			},
		},
		"think_mode": "auto",
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return decideOutcome{}, err
	}
	req, err := http.NewRequest("POST", strings.TrimRight(startluxURL, "/")+"/v1/decisions", bytes.NewReader(buf))
	if err != nil {
		return decideOutcome{}, err
	}
	req.Header.Set("Authorization", "Bearer "+startluxKey)
	req.Header.Set("Content-Type", "application/json")
	cl := startluxClient
	if cl == nil {
		cl = &http.Client{Timeout: startluxTimeout}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return decideOutcome{}, fmt.Errorf("云端查询失败：%v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return decideOutcome{}, fmt.Errorf("云端返回 %d: %s", resp.StatusCode, truncate(data))
	}
	choice, probs, rationale, err := parseStartluxDecisions(data)
	if err != nil {
		return decideOutcome{}, err
	}
	conf := 0.5
	if p, ok := probs[choice]; ok {
		conf = p
	}
	return decideOutcome{
		UsedLevel:     "L0",
		Choice:        choice,
		Confidence:    conf,
		Rationale:     rationale,
		Degraded:      false,
		Probabilities: probs,
	}, nil
}

// parseStartluxDecisions 容错解析：响应结构以实际为准——依次探测
// answers/decisions/target 各层，choice 取常见同义键，概率支持对象与数组两种形态。
func parseStartluxDecisions(data []byte) (string, map[string]float64, string, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", nil, "", fmt.Errorf("云端响应解析失败：%v", err)
	}
	obj := findTargetObj(root, "answers", "decisions", "results", "output")
	if obj == nil {
		return "", nil, "", fmt.Errorf("云端响应解析失败：未找到 target 决策结果")
	}
	choice := firstString(obj, "choice", "value", "selected", "option", "answer", "decision", "target")
	if choice == "" {
		return "", nil, "", fmt.Errorf("云端响应解析失败：未找到 choice 结果")
	}
	probs := map[string]float64{}
	for _, key := range []string{"probabilities", "probs", "distribution", "confidence_map", "probability"} {
		if m, ok := obj[key].(map[string]any); ok {
			for k, v := range m {
				if f, ok := anyFloat(v); ok {
					probs[k] = f
				}
			}
			if len(probs) > 0 {
				break
			}
		}
	}
	if len(probs) == 0 {
		if arr, ok := obj["choices"].([]any); ok { // 数组形态：[{label, probability}, …]
			for _, it := range arr {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				label := firstString(m, "label", "option", "name", "choice", "value", "target")
				if label == "" {
					continue
				}
				if f, ok := firstAnyFloat(m, "probability", "p", "prob", "value"); ok {
					probs[label] = f
				}
			}
		}
	}
	rationale := firstString(obj, "rationale", "reason", "reasoning", "explanation", "justification")
	return strings.TrimSpace(choice), probs, rationale, nil
}

// findTargetObj 逐层下钻：{<layer>:{target:{…}}} → {<layer>:{…含 choice 键…}} → 根对象本身。
func findTargetObj(root map[string]any, layers ...string) map[string]any {
	for _, layer := range layers {
		sub, ok := root[layer].(map[string]any)
		if !ok {
			continue
		}
		if t, ok := sub["target"].(map[string]any); ok {
			return t
		}
		if ts, ok := sub["target"].(string); ok && ts != "" {
			return map[string]any{"choice": ts}
		}
		if _, ok := sub["choice"]; ok { // answers 即 target 本体
			return sub
		}
	}
	if t, ok := root["target"].(map[string]any); ok {
		return t
	}
	if _, ok := root["choice"]; ok {
		return root
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func anyFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func firstAnyFloat(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		if f, ok := anyFloat(m[k]); ok {
			return f, true
		}
	}
	return 0, false
}

// ---------- L2 规则兜底：hub 目录关键词匹配 + inflight 决胜 ----------

type decideAgent struct {
	NodeID      string `json:"node_id"`
	AgentName   string `json:"agent_name"`
	Description string `json:"description"`
}

func fetchDecideAgents() ([]decideAgent, error) {
	code, data, err := hubAPI("GET", "/api/v1/agents", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("hub 返回 %d: %s", code, truncate(data))
	}
	var out struct {
		Agents []struct {
			NodeID string `json:"node_id"`
			Card   struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"card"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	agents := make([]decideAgent, 0, len(out.Agents))
	for _, a := range out.Agents {
		agents = append(agents, decideAgent{NodeID: a.NodeID, AgentName: a.Card.Name, Description: a.Card.Description})
	}
	return agents, nil
}

type decideNode struct {
	NodeID   string `json:"node_id"`
	Online   bool   `json:"online"`
	Inflight int    `json:"inflight"`
}

func fetchDecideNodes() ([]decideNode, error) {
	code, data, err := hubAPI("GET", "/api/v1/nodes", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("hub 返回 %d: %s", code, truncate(data))
	}
	var out struct {
		Nodes []decideNode `json:"nodes"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// decideL2 规则分配：子任务与候选能力描述做关键词重叠评分（中文单字+西文整词，
// 忽略标点），同分取 inflight 最低节点；候选所在节点全部离线、或全部 0 分 →
// 报错需人工决策（节点在线状态取不到时跳过离线过滤，尽力而为）。
func decideL2(subtask string, options []string, whyL2 string) (decideOutcome, error) {
	agents, err := fetchDecideAgents()
	if err != nil {
		return decideOutcome{}, fmt.Errorf("降级 L2 失败：无法获取能力目录（%v）", err)
	}
	inflight := map[string]int{}
	online := map[string]bool{}
	haveStatus := false
	if nodes, err := fetchDecideNodes(); err == nil {
		haveStatus = true
		for _, nd := range nodes {
			inflight[nd.NodeID] = nd.Inflight
			online[nd.NodeID] = nd.Online
		}
	}
	// 只考虑在线节点（hub /api/v1/nodes 的 online 字段；不在节点表即视为离线）。
	cands := make([]int, 0, len(options))
	for i, opt := range options {
		if haveStatus && !online[decideOptionNode(opt, agents)] {
			continue
		}
		cands = append(cands, i)
	}
	if haveStatus && len(cands) == 0 {
		return decideOutcome{}, fmt.Errorf("所有候选节点离线，需人工决策（StartLux 不可用：%s）", whyL2)
	}
	subTokens := tokenize(subtask)
	best := -1
	bestScore := 0
	bestInflight := 0
	var bestHits []string
	for _, i := range cands {
		opt := options[i]
		score, hits := overlapScore(subTokens, tokenize(decideCapabilityText(opt, agents)))
		infl := inflight[decideOptionNode(opt, agents)]
		if best == -1 || score > bestScore || (score == bestScore && infl < bestInflight) {
			best, bestScore, bestInflight, bestHits = i, score, infl, hits
		}
	}
	if bestScore <= 0 {
		return decideOutcome{}, fmt.Errorf("无匹配能力，需人工决策：子任务与候选能力描述无关键词重叠（StartLux 不可用：%s）", whyL2)
	}
	detail := fmt.Sprintf("关键词命中 %d 处（%s），节点 %s inflight=%d",
		bestScore, strings.Join(bestHits, "/"), decideOptionNode(options[best], agents), bestInflight)
	return decideOutcome{
		UsedLevel:  "L2",
		Choice:     options[best],
		Confidence: 0.5,
		Rationale:  fmt.Sprintf("StartLux 不可用（%s），降级规则分配：%s", whyL2, detail),
		Degraded:   true,
	}, nil
}

// decideCapabilityText 候选对应的能力描述：node/agent 形式按目录取该 agent 描述；
// 裸 agent 名全网匹配；裸 node_id 取该节点全部能力描述拼接；查不到用候选原文。
func decideCapabilityText(opt string, agents []decideAgent) string {
	node, agent, hasSlash := strings.Cut(opt, "/")
	if hasSlash && agent != "" {
		for _, a := range agents {
			if a.AgentName == agent && (node == "" || a.NodeID == node) {
				return a.AgentName + " " + a.Description
			}
		}
	}
	if !hasSlash {
		for _, a := range agents {
			if a.AgentName == opt {
				return a.AgentName + " " + a.Description
			}
		}
		var b strings.Builder
		found := false
		for _, a := range agents {
			if a.NodeID == opt {
				found = true
				b.WriteString(a.AgentName + " " + a.Description + " ")
			}
		}
		if found {
			return b.String()
		}
	}
	return opt
}

// decideOptionNode 候选归属节点（inflight 决胜用）。
func decideOptionNode(opt string, agents []decideAgent) string {
	if node, _, hasSlash := strings.Cut(opt, "/"); hasSlash {
		return node
	}
	for _, a := range agents {
		if a.AgentName == opt {
			return a.NodeID
		}
	}
	return opt
}

// tokenize 分词：中文按单字、西文/数字按连续串（小写化），空格与标点均为分词边界。
func tokenize(s string) map[string]struct{} {
	set := make(map[string]struct{})
	var word []rune
	flush := func() {
		if len(word) > 0 {
			set[strings.ToLower(string(word))] = struct{}{}
			word = word[:0]
		}
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			set[string(r)] = struct{}{}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return set
}

func overlapScore(a, b map[string]struct{}) (int, []string) {
	hits := make([]string, 0, 8)
	for t := range a {
		if _, ok := b[t]; ok {
			hits = append(hits, t)
		}
	}
	sort.Strings(hits)
	return len(hits), hits
}

func statusCN(s string) string {
	return map[string]string{
		"submitted": "已提交", "offered": "已投递", "acknowledged": "已接收",
		"running": "执行中", "completed": "已完成", "failed": "执行失败", "needs_review": "待人工核对（结果不确定）",
		"cancelled": "已取消（管理员手动终止）",
	}[s]
}

func truncate(b []byte) string { return truncateBytes(b, 300) }
func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…（截断）"
}

/* ---------- 群聊协作空间（GROUP-RFC 六 MVP） ---------- */

// groupErrHint 将 hub 错误码转成 agent 可行动的人话提示。
func groupErrHint(code int, data []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	hint := map[string]string{
		"group_not_found":  "群不存在（swarmmind_group_list 可查现有群）",
		"group_concluded":  "群已结束（终态），不能再发言",
		"budget_exhausted": "发言预算（agent 轮数）已用尽：可 swarmmind_group_close 结束后另建群，或由人插话推进",
		"body_too_large":   "消息体超 8KiB 上限，请精简正文后再发",
		"invalid_request":  e.Message,
	}[e.Error]
	if hint == "" {
		hint = truncate(data)
	}
	return fmt.Errorf("hub %d：%s", code, hint)
}

func toolGroupCreate(args map[string]any) (string, error) {
	goal, _ := args["goal"].(string)
	if strings.TrimSpace(goal) == "" {
		return "", fmt.Errorf("goal 必填（一句话说清要讨论出什么）")
	}
	raw, _ := args["members"].([]any)
	members := make([]string, 0, len(raw))
	for _, v := range raw {
		sv, ok := v.(string)
		if !ok || strings.TrimSpace(sv) == "" {
			return "", fmt.Errorf("members 须为非空字符串数组")
		}
		members = append(members, strings.TrimSpace(sv))
	}
	code, data, err := hubAPI("POST", "/api/v1/groups", map[string]any{"goal": goal, "members": members})
	if err != nil {
		return "", err
	}
	if code != 201 {
		return "", groupErrHint(code, data)
	}
	var g struct {
		ID      string   `json:"group_id"`
		Members []string `json:"members"`
	}
	_ = json.Unmarshal(data, &g)
	return fmt.Sprintf("协作群已创建：%s\n目标：%s\n成员：%s\n用 swarmmind_group_say 发言、swarmmind_group_messages 拉消息、swarmmind_group_close 结束。",
		g.ID, goal, strings.Join(g.Members, "、")), nil
}

func toolGroupList() (string, error) {
	code, data, err := hubAPI("GET", "/api/v1/groups", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", groupErrHint(code, data)
	}
	var out struct {
		Groups []struct {
			ID      string   `json:"group_id"`
			Goal    string   `json:"goal"`
			Status  string   `json:"status"`
			Members []string `json:"members"`
			Budget  struct {
				MaxRounds int `json:"max_rounds"`
			} `json:"budget"`
		} `json:"groups"`
	}
	_ = json.Unmarshal(data, &out)
	if len(out.Groups) == 0 {
		return "还没有协作群（swarmmind_group_create 可建）。", nil
	}
	var b strings.Builder
	for _, g := range out.Groups {
		st := "讨论中"
		if g.Status == "concluded" {
			st = "已结束"
		} else if g.Status == "forming" {
			st = "组建中"
		}
		fmt.Fprintf(&b, "%s：%s（%s｜成员 %s｜发言上限 %d 轮）\n", g.ID, g.Goal, st, strings.Join(g.Members, "、"), g.Budget.MaxRounds)
	}
	return b.String(), nil
}

func toolGroupSay(args map[string]any) (string, error) {
	gid, _ := args["group_id"].(string)
	from, _ := args["from"].(string)
	body, _ := args["body"].(string)
	kind, _ := args["kind"].(string)
	if gid == "" || from == "" {
		return "", fmt.Errorf("group_id 与 from 必填")
	}
	if body == "" {
		return "", fmt.Errorf("body 必填")
	}
	req := map[string]any{"from": from, "kind": kind, "body": body}
	if kind == "" {
		req["kind"] = "user"
	}
	code, data, err := hubAPI("POST", "/api/v1/groups/"+gid+"/messages", req)
	if err != nil {
		return "", err
	}
	if code != 201 {
		return "", groupErrHint(code, data)
	}
	var m struct {
		Seq int64 `json:"seq"`
	}
	_ = json.Unmarshal(data, &m)
	return fmt.Sprintf("已发到群 %s（seq=%d）。\n用 swarmmind_group_messages（since=%d）拉新消息。", gid, m.Seq, m.Seq), nil
}

func toolGroupMessages(args map[string]any) (string, error) {
	gid, _ := args["group_id"].(string)
	if gid == "" {
		return "", fmt.Errorf("group_id 必填")
	}
	since := int64(0)
	if v, ok := args["since"].(float64); ok && v > 0 {
		since = int64(v)
	}
	code, data, err := hubAPI("GET", fmt.Sprintf("/api/v1/groups/%s/messages?since=%d", gid, since), nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", groupErrHint(code, data)
	}
	var out struct {
		Messages []struct {
			Seq  int64  `json:"seq"`
			From string `json:"from"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"messages"`
		PrunedBeforeSeq int64 `json:"pruned_before_seq"`
	}
	_ = json.Unmarshal(data, &out)
	if len(out.Messages) == 0 {
		if out.PrunedBeforeSeq > 0 && since < out.PrunedBeforeSeq {
			return fmt.Sprintf("群 %s 自 seq=%d 后没有新消息。注意：群消息滚动只保留最新 200 条，seq≤%d 的更早消息已被裁剪、不可再拉。", gid, since, out.PrunedBeforeSeq), nil
		}
		return fmt.Sprintf("群 %s 自 seq=%d 后没有新消息。", gid, since), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "群 %s 新增 %d 条：\n", gid, len(out.Messages))
	for _, m := range out.Messages {
		fmt.Fprintf(&b, "[#%d] %s(%s)：%s\n", m.Seq, m.From, m.Kind, m.Body)
	}
	// since 落在被裁区间：调用方按 lastSeq 轮询会误以为无缺口，显式点破
	// （GROUP-RFC 三 200 条滚动窗口语义）。
	if out.PrunedBeforeSeq > 0 && since < out.PrunedBeforeSeq {
		fmt.Fprintf(&b, "注意：群消息滚动只保留最新 200 条，seq≤%d 的更早消息已被裁剪（含你请求的 since=%d 区间），从现存最旧 [#%d] 起返回。", out.PrunedBeforeSeq, since, out.Messages[0].Seq)
	} else if out.PrunedBeforeSeq > 0 {
		fmt.Fprintf(&b, "注意：群消息滚动只保留最新 200 条，seq≤%d 的更早消息已被裁剪。", out.PrunedBeforeSeq)
	}
	b.WriteString("按 #序号 继续 swarmmind_group_messages(since=最大序号) 轮询。")
	return b.String(), nil
}

func toolGroupClose(args map[string]any) (string, error) {
	gid, _ := args["group_id"].(string)
	if gid == "" {
		return "", fmt.Errorf("group_id 必填")
	}
	// 审计粒度：by 可选（缺省 mcp）——谁关的群记谁，不再硬编码。
	by, _ := args["by"].(string)
	by = strings.TrimSpace(by)
	if by == "" {
		by = "mcp"
	}
	code, data, err := hubAPI("POST", "/api/v1/groups/"+gid+"/close", map[string]any{"by": by})
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", groupErrHint(code, data)
	}
	return fmt.Sprintf("群 %s 已结束（concluded 终态，不再收消息）。", gid), nil
}

/* ---------- 工具路由 ---------- */

func callTool(name string, args map[string]any) (map[string]any, *rpcError) {
	if hubURL == "" || hubToken == "" {
		return errResult("未配置：请设置 SWARM_HUB_URL 与 SWARM_HUB_TOKEN 环境变量"), nil
	}
	var (
		text string
		err  error
	)
	switch name {
	case "swarmmind_nodes":
		text, err = toolNodes()
	case "swarmmind_agents":
		text, err = toolAgents()
	case "swarmmind_submit":
		text, err = toolSubmit(args)
	case "swarmmind_status":
		if id, _ := args["task_id"].(string); id == "" {
			return errResult("task_id 必填"), nil
		} else {
			text, _, _ = taskStatus(id) // hub 既有语义：HTTP 错误折进文本（修复 8 后 node 侧分叉为 error）
		}
	case "swarmmind_wait":
		text, err = toolWait(args)
	case "swarmmind_artifact":
		text, err = toolArtifact(args)
	case "swarmmind_decide":
		text, err = toolDecide(args)
	case "swarmmind_group_create":
		text, err = toolGroupCreate(args)
	case "swarmmind_group_list":
		text, err = toolGroupList()
	case "swarmmind_group_say":
		text, err = toolGroupSay(args)
	case "swarmmind_group_messages":
		text, err = toolGroupMessages(args)
	case "swarmmind_group_close":
		text, err = toolGroupClose(args)
	default:
		return nil, &rpcError{Code: -32602, Message: "未知工具: " + name}
	}
	if err != nil {
		return errResult("操作失败：" + err.Error()), nil
	}
	return callResult(text), nil
}

/* ---------- stdio 主循环 ---------- */

func main() {
	// 装配分发（方案 3.4）：SWARM_TARGET 选 backend——hub（默认，完全向后兼容）
	// 或 node（本机编排入口）；非法值启动即报错退出并列出合法值，绝不静默回退 hub。
	var bd backend
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SWARM_TARGET"))) {
	case "", "hub":
		hubURL = strings.TrimSpace(os.Getenv("SWARM_HUB_URL"))
		hubToken = strings.TrimSpace(os.Getenv("SWARM_HUB_TOKEN"))
		bd = hubBackend{}
	case "node":
		bd = newNodeBackend(os.Getenv)
	default:
		fmt.Fprintf(os.Stderr, "[swarmmind-mcp] SWARM_TARGET=%q 非法：合法值为 hub | node（不设默认 hub）\n",
			os.Getenv("SWARM_TARGET"))
		os.Exit(2)
	}

	startluxURL = envDefault("SWARM_STARTLUX_URL", "https://api.bamboonode.dev")
	startluxKey = strings.TrimSpace(os.Getenv("SWARM_STARTLUX_KEY"))
	startluxModel = envDefault("SWARM_STARTLUX_MODEL", "startlux-decision-27b-autothink")
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWARM_STARTLUX_TIMEOUT_SEC"))); err == nil && n > 0 {
		startluxTimeout = time.Duration(n) * time.Second
	}
	startluxClient = &http.Client{Timeout: startluxTimeout}

	if err := runStdio(os.Stdin, os.Stdout, bd); err != nil {
		fmt.Fprintf(os.Stderr, "[swarmmind-mcp] %v\n", err)
		os.Exit(1)
	}
}

// runStdio MCP stdio 主循环（newline-delimited JSON-RPC 2.0）：initialize /
// tools/list / tools/call 全走 backend 分发（工具清单与调用语义按模式装配）。
func runStdio(in io.Reader, out io.Writer, bd backend) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	w := bufio.NewWriter(out)
	defer w.Flush()

	write := func(v any) {
		b, _ := json.Marshal(v)
		w.Write(b)
		w.WriteByte('\n')
		w.Flush()
	}

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue // 非法行忽略（MCP 无 id 时无法回错误）
		}
		switch req.Method {
		case "initialize":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: bd.initResult()})
		case "notifications/initialized", "notifications/cancelled":
			// 通知，无需应答
		case "ping":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		case "tools/list":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": bd.tools()}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			result, rpcErr := bd.call(p.Name, p.Arguments)
			if rpcErr != nil {
				write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
				continue
			}
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		default:
			if req.ID != nil {
				write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
			}
		}
	}
	return sc.Err()
}
