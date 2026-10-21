package api

// A2A（Agent-to-Agent）协议兼容端点（SPEC 六.五「A2A 兼容端点」，P3 收尾）。
// 外部 A2A 客户端经 /.well-known/agent.json 发现网内能力，经 /a2a/rpc 以
// JSON-RPC 2.0 派单与查询；数据面完全复用 Core（Submit/GetTask/ListDirectory），
// 不引入第二条写路径。对齐 A2A 协议 v0.2.x（a2a-protocol.org，0.2.6 版规范）。
//
// 相对官方 0.2.x 的已知偏差（均为有意简化，随实现演进再补）：
//   - 仅实现 message/send 与 tasks/get：流式/取消/推送通知/任务列表不支持，
//     capabilities 对应位恒 false；
//   - TextPart 同时接受官方 kind:"text" 与简写 type:"text"（宽容解析）；
//   - message.messageId 映射为幂等键（前缀 "a2a:"，与 REST idem_key 命名空间
//     隔离），客户端重试天然去重，同 messageId 不同内容 → -32000 冲突；
//   - A2A 无 submitted_by 概念：任务归属固定 a2a（同 MCP submitted_by=mcp
//     惯例，SPEC 六.六），鉴权依据是 Bearer PAT/会话本身；
//   - RPC 应答 Content-Type 用 application/json（未用 application/a2a+json）；
//   - card 不带 securitySchemes（发现卡只含能力元数据，鉴权在 RPC 端点）；
//   - 任务结果正文（final_answer/产物）不映射进 A2A Task，外部客户端以
//     metadata.swarmmind_status 识别终态后走 REST GET /api/v1/tasks/{id} 取详情。

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/mailbox"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// card 版本常量：protocolVersion 为对齐的 A2A 规范版本，version 为 hub 实现版本。
const (
	a2aSpecVersion = "0.2.6"
	a2aHubVersion  = "0.2.0"
)

// a2aIdentity A2A 派单任务的 submitted_by（仅显示与审计标签，SPEC 六.五 提交身份）。
const a2aIdentity = "a2a"

// ---------- AgentCard（GET /.well-known/agent.json，免鉴权） ----------

// a2aAgentCard A2A AgentCard（v0.2.x 形状）。白名单字段手工构造：skill 只取
// 名称/描述，ExecProfile、workspace_id、permission_mode、exposed 等一概不出
// （对齐 SPEC 六 目录泄漏红线）；免鉴权是 A2A 客户端发现惯例。
type a2aAgentCard struct {
	ProtocolVersion    string     `json:"protocolVersion"`
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	URL                string     `json:"url"`
	Version            string     `json:"version"`
	Capabilities       a2aCaps    `json:"capabilities"`
	DefaultInputModes  []string   `json:"defaultInputModes"`
	DefaultOutputModes []string   `json:"defaultOutputModes"`
	Skills             []a2aSkill `json:"skills"`
}

// a2aCapabilities 能力声明：V0 同步请求/应答，流式与推送通知均不支持。
type a2aCaps struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

// a2aSkill 一个可调用 agent 对应一条（id 全网唯一：node_id/agent_name）。
type a2aSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// handleAgentCard GET /.well-known/agent.json：发现入口。url 从请求 Host 推导
// （反代场景取 X-Forwarded-Proto 判 scheme）。
func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	skills := make([]a2aSkill, 0)
	for _, e := range s.cfg.Core.ListDirectory() {
		desc := e.Card.Description
		if desc == "" {
			desc = "SwarmMind agent（node " + e.NodeID + "）"
		}
		skills = append(skills, a2aSkill{
			ID:          e.NodeID + "/" + e.Card.Name,
			Name:        e.Card.Name,
			Description: desc,
			Tags:        []string{"swarmmind", string(e.Card.Kind)},
		})
	}
	writeJSON(w, http.StatusOK, a2aAgentCard{
		ProtocolVersion:    a2aSpecVersion,
		Name:               "SwarmMind",
		Description:        "跨机器 agent 协作网络",
		URL:                scheme + "://" + r.Host + "/",
		Version:            a2aHubVersion,
		Capabilities:       a2aCaps{Streaming: false, PushNotifications: false},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills:             skills,
	})
}

// ---------- JSON-RPC 2.0（POST /a2a/rpc，s.auth 鉴权） ----------

// a2aState SwarmMind 七态 → A2A TaskState（v0.2.x 小写连字符枚举）状态映射表：
//
//	SwarmMind    A2A             说明
//	-----------  --------------  ----------------------------------------
//	submitted    submitted       已受理
//	offered      submitted       A2A 无派发中间态，最接近「已受理未开工」
//	acknowledged submitted       同上
//	running      working         执行中
//	completed    completed       终态
//	failed       failed          终态
//	needs_review input-required  结果不确定待人工输入/核对，语义最接近；
//	                             原始值保留在 metadata.swarmmind_status
//	其他/未知    unknown
func a2aState(st protocol.TaskStatus) string {
	switch st {
	case protocol.StatusSubmitted, protocol.StatusOffered, protocol.StatusAcknowledged:
		return "submitted"
	case protocol.StatusRunning:
		return "working"
	case protocol.StatusCompleted:
		return "completed"
	case protocol.StatusFailed:
		return "failed"
	case protocol.StatusNeedsReview:
		return "input-required"
	default:
		return "unknown"
	}
}

// JSON-RPC 2.0 标准错误码 + A2A 专有码（0.2.x TaskNotFoundError）；
// -32000 为 JSON-RPC 预留的实现自定义段，用于容量/冲突类提交失败。
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
	rpcTaskNotFound   = -32001
	rpcServerError    = -32000
)

type a2aRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type a2aRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *a2aRPCError    `json:"error,omitempty"`
}

// a2aTask A2A Task 响应形状：contextId 取 SwarmMind trace_id；metadata 携带
// SwarmMind 侧字段（含原始状态，A2A 侧可逆查，信息不因映射丢失）。
type a2aTask struct {
	ID        string            `json:"id"`
	ContextID string            `json:"contextId"`
	Status    a2aTaskStatus     `json:"status"`
	Kind      string            `json:"kind"` // 恒 "task"（A2A 判别字段）
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type a2aTaskStatus struct {
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"` // 序列化为 RFC3339
}

// a2aTaskFromProtocol SwarmMind Task → A2A Task。
func a2aTaskFromProtocol(t protocol.Task) a2aTask {
	return a2aTask{
		ID:        t.ID,
		ContextID: t.TraceID,
		Status:    a2aTaskStatus{State: a2aState(t.Status), Timestamp: t.UpdatedAt},
		Kind:      "task",
		Metadata: map[string]string{
			"swarmmind_status": string(t.Status),
			"target_node":      t.TargetNode,
			"target_agent":     t.TargetAgent,
			"submitted_by":     t.SubmittedBy,
		},
	}
}

// a2aTextPart 官方 TextPart 为 {kind:"text", text}；简写变体 {type:"text"} 一并接受。
type a2aTextPart struct {
	Kind string `json:"kind,omitempty"`
	Type string `json:"type,omitempty"`
	Text string `json:"text"`
}

type a2aMessage struct {
	Role      string        `json:"role,omitempty"`
	Parts     []a2aTextPart `json:"parts"`
	MessageID string        `json:"messageId,omitempty"`
}

// a2aMessageSendParams message/send 入参：目标经 metadata 携带（A2A Message
// 本体无寻址字段；metadata 为自由对象，非字符串值忽略）。
type a2aMessageSendParams struct {
	Message  a2aMessage     `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type a2aTaskGetParams struct {
	ID string `json:"id"`
}

// handleA2ARPC POST /a2a/rpc：JSON-RPC 2.0 分发（信封与错误码手写，零新依赖，
// 对齐 cmd/swarmmind-mcp 做法）。应用层错误一律 HTTP 200 + error 对象
// （JSON-RPC over HTTP 惯例）；HTTP 层 401 由 s.auth 产出。
func (s *Server) handleA2ARPC(w http.ResponseWriter, r *http.Request) {
	var req a2aRPCRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		a2aWriteRPC(w, nil, nil, &a2aRPCError{Code: rpcParseError, Message: "JSON 解析失败: " + err.Error()})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		a2aWriteRPC(w, req.ID, nil, &a2aRPCError{Code: rpcInvalidRequest, Message: "非法 JSON-RPC 2.0 请求（缺 jsonrpc:\"2.0\" 或 method）"})
		return
	}
	switch req.Method {
	case "message/send":
		s.a2aMessageSend(w, req.ID, req.Params)
	case "tasks/get":
		s.a2aTasksGet(w, req.ID, req.Params)
	default:
		a2aWriteRPC(w, req.ID, nil, &a2aRPCError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method})
	}
}

// a2aMessageSend message/send：text parts 拼接为任务描述 → Core.Submit 全链
// （等价 POST /api/v1/tasks；submitted_by 固定 a2a）。messageId 非空时映射为
// 幂等键（前缀 a2a:，与 REST idem_key 命名空间隔离），重试去重。
func (s *Server) a2aMessageSend(w http.ResponseWriter, id json.RawMessage, raw json.RawMessage) {
	var p a2aMessageSendParams
	if err := json.Unmarshal(raw, &p); err != nil {
		a2aWriteRPC(w, id, nil, &a2aRPCError{Code: rpcInvalidParams, Message: "params 解析失败: " + err.Error()})
		return
	}
	var b strings.Builder
	for _, part := range p.Message.Parts {
		if part.Kind == "text" || part.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(part.Text)
		}
	}
	text := strings.TrimSpace(b.String())
	targetNode, _ := p.Metadata["target_node"].(string)
	targetAgent, _ := p.Metadata["target_agent"].(string)
	if text == "" || targetNode == "" || targetAgent == "" {
		a2aWriteRPC(w, id, nil, &a2aRPCError{Code: rpcInvalidParams,
			Message: "须含非空 text part，且 metadata.target_node / metadata.target_agent 必填"})
		return
	}
	req := protocol.TaskSubmitRequest{
		TargetNode:  targetNode,
		TargetAgent: targetAgent,
		Input:       protocol.TaskInput{Args: []string{text}},
		SubmittedBy: a2aIdentity,
	}
	if mid := strings.TrimSpace(p.Message.MessageID); mid != "" {
		req.IdemKey = "a2a:" + mid
	}
	ack, err := s.cfg.Core.Submit(req, "")
	if err != nil {
		a2aWriteRPC(w, id, nil, a2aSubmitRPCErr(err))
		return
	}
	t, ok := s.cfg.Core.GetTask(ack.TaskID) // 提交成功必有；Dup 时取当前真实状态
	if !ok {
		log.Printf("[hub] A2A message/send 提交后查询不到任务 %s（不应发生）", ack.TaskID)
		a2aWriteRPC(w, id, nil, &a2aRPCError{Code: rpcInternalError, Message: "提交失败"})
		return
	}
	a2aWriteRPC(w, id, a2aTaskFromProtocol(t), nil)
}

// a2aSubmitRPCErr 提交校验链错误 → JSON-RPC 错误（对照 writeSubmitErr 的 HTTP
// 映射）：目标/参数类 → -32602；容量与冲突类 → -32000（实现自定义服务错误段）。
func a2aSubmitRPCErr(err error) *a2aRPCError {
	switch {
	case errors.Is(err, core.ErrAgentNotExposed),
		errors.Is(err, core.ErrInvalid),
		errors.Is(err, protocol.ErrFrameTooLarge):
		return &a2aRPCError{Code: rpcInvalidParams, Message: err.Error()}
	case errors.Is(err, mailbox.ErrConflict),
		errors.Is(err, mailbox.ErrMailboxFull),
		errors.Is(err, core.ErrStorageFull):
		return &a2aRPCError{Code: rpcServerError, Message: err.Error()}
	default:
		log.Printf("[hub] A2A message/send 内部错误: %v", err)
		return &a2aRPCError{Code: rpcInternalError, Message: "提交失败"}
	}
}

// a2aTasksGet tasks/get：按 id 查询 → A2A Task 形状；不存在 → -32001
// （A2A TaskNotFoundError）。
func (s *Server) a2aTasksGet(w http.ResponseWriter, id json.RawMessage, raw json.RawMessage) {
	var p a2aTaskGetParams
	if err := json.Unmarshal(raw, &p); err != nil || p.ID == "" {
		a2aWriteRPC(w, id, nil, &a2aRPCError{Code: rpcInvalidParams, Message: "params.id 必填"})
		return
	}
	t, ok := s.cfg.Core.GetTask(p.ID)
	if !ok {
		a2aWriteRPC(w, id, nil, &a2aRPCError{Code: rpcTaskNotFound, Message: "Task not found: " + p.ID})
		return
	}
	a2aWriteRPC(w, id, a2aTaskFromProtocol(t), nil)
}

// a2aWriteRPC 统一 JSON-RPC 应答（result 与 error 互斥；通知类缺 id 补 null）。
func a2aWriteRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *a2aRPCError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}
