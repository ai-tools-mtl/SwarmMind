// swarmmind-mcp：SwarmMind 的 MCP（Model Context Protocol）stdio 服务。
//
// 目的（2026-10-21 验收反馈）：让任意 agent（小零/师者/zcode/codex…）通过 MCP
// 直接向 SwarmMind 网派任务、等结果、取产物——agent 网络的统一入口。
//
// 协议：MCP stdio transport（newline-delimited JSON-RPC 2.0），手写实现，
// 零第三方依赖（对齐 SPEC 依赖白名单哲学）。
//
// 配置（环境变量）：
//
//	SWARM_HUB_URL   管理面地址，如 http://127.0.0.1:8444
//	SWARM_HUB_TOKEN 访问令牌（smsw_… PAT 或 admin_token）
//
// 工具（6 个）：nodes / agents / submit / status / wait / artifact。
// 等待语义：wait 轮询至终态；needs_review 作为终态返回并明确提示需人工核对。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	hubURL   string
	hubToken string
	client   = &http.Client{Timeout: 35 * time.Second}
)

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
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(hubURL, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+hubToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data, err
}

/* ---------- 工具定义 ---------- */

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func tools() []toolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	s := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	n := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	a := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return []toolDef{
		{Name: "swarmmind_nodes", Description: "查看 SwarmMind 网内节点：哪些机器在线、各有多少进行中任务", InputSchema: obj(map[string]any{})},
		{Name: "swarmmind_agents", Description: "查看全网可用能力（agent 目录）：每台节点上开放了哪些可调用的 agent", InputSchema: obj(map[string]any{})},
		{Name: "swarmmind_submit", Description: "向指定节点的指定 agent 派发任务。返回任务 ID；用 swarmmind_wait 等结果", InputSchema: obj(map[string]any{
			"node_id":  s("目标节点 ID（swarmmind_nodes 可查）"),
			"agent":    s("目标 agent 名（swarmmind_agents 可查）"),
			"args":     a("命令行参数（可选）"),
			"stdin":    s("任务指令正文/标准输入（可选，推荐把完整任务描述写在这里）"),
			"trace_id": s("追踪号（可选，便于关联；总理任期建议 prime-<任期号>-<序号>）"),
			"idem_key": s("幂等键（可选；同键同内容重试不会重复执行，重派时沿用原键）"),
		}, "node_id", "agent")},
		{Name: "swarmmind_status", Description: "查询任务当前状态与结果（不等待）", InputSchema: obj(map[string]any{
			"task_id": s("任务 ID"),
		}, "task_id")},
		{Name: "swarmmind_wait", Description: "等待任务完成并返回最终结果（轮询至终态：completed/failed/needs_review）", InputSchema: obj(map[string]any{
			"task_id":      s("任务 ID"),
			"timeout_sec":  n("最长等待秒数（默认 300，上限 1800）"),
			"interval_sec": n("轮询间隔秒数（默认 3）"),
		}, "task_id")},
		{Name: "swarmmind_artifact", Description: "取回任务产物文件内容（文本类产物；任务需已完成且有产物登记）", InputSchema: obj(map[string]any{
			"task_id": s("任务 ID"),
			"index":   n("产物序号（从 0 开始，默认 0）"),
		}, "task_id")},
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

// taskStatus 拉取并汇总单个任务的可读状态。
func taskStatus(taskID string) (string, bool) {
	code, data, err := hubAPI("GET", "/api/v1/tasks/"+taskID, nil)
	if err != nil {
		return "查询失败：" + err.Error(), false
	}
	if code == 404 {
		return "任务不存在或已归档（终态保留 24 小时）。", true
	}
	if code != 200 {
		return fmt.Sprintf("hub 返回 %d: %s", code, truncate(data)), true
	}
	var t struct {
		Status      string `json:"status"`
		TargetAgent string `json:"target_agent"`
		TargetNode  string `json:"target_node"`
		Result      *struct {
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
	return b.String(), t.Status == "completed" || t.Status == "failed" || t.Status == "needs_review"
}

func toolWait(args map[string]any) (string, error) {
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
		text, done := taskStatus(taskID)
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
	// 文本类直接返回；二进制给提示
	if isText(data) {
		return fmt.Sprintf("产物内容（%d 字节）：\n%s", len(data), truncateBytes(data, 8000)), nil
	}
	return fmt.Sprintf("产物为二进制（%d 字节），MCP 文本通道不展示；请经控制台取回。", len(data)), nil
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

func statusCN(s string) string {
	return map[string]string{
		"submitted": "已提交", "offered": "已投递", "acknowledged": "已接收",
		"running": "执行中", "completed": "已完成", "failed": "执行失败", "needs_review": "待人工核对（结果不确定）",
	}[s]
}

func truncate(b []byte) string { return truncateBytes(b, 300) }
func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…（截断）"
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
			text, _ = taskStatus(id)
		}
	case "swarmmind_wait":
		text, err = toolWait(args)
	case "swarmmind_artifact":
		text, err = toolArtifact(args)
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
	hubURL = strings.TrimSpace(os.Getenv("SWARM_HUB_URL"))
	hubToken = strings.TrimSpace(os.Getenv("SWARM_HUB_TOKEN"))

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	write := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}

	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue // 非法行忽略（MCP 无 id 时无法回错误）
		}
		switch req.Method {
		case "initialize":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "swarmmind", "version": "0.1.1"},
			}})
		case "notifications/initialized", "notifications/cancelled":
			// 通知，无需应答
		case "ping":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		case "tools/list":
			write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools()}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			result, rpcErr := callTool(p.Name, p.Arguments)
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
	_ = strconv.Itoa // 保持 import 简洁
}
