// node_sessions.go node 会话四工具（W01-04，docs/tasks/v3/W01-sessions.md；
// SPEC 六.五映射表 node 行 + 六.六豁免⑬核对）：与 hub 会话四工具同参语义，投影
// 本机 agent 面 /api/v1/sessions*（internal/node/localapi/sessions_api.go——W01-03
// 定稿 REST，只读消费不改其面）。
//
// 与 hub 面的如实差异（双端形态对照，任务书「同一参数语义投影」）：
//   - 会话固定建在本机：node_id 参数保留（与 hub schema 同形）但按本机面语义
//     处理——传入时须为本机节点 ID，否则显式拒绝指路 hub 模式（不静默改道）；
//   - permission_mode 参数不收（本机绑定由 node.yaml 能力声明决定）：传入即
//     显式拒绝（同 toolNodeSubmit 对 hub-only 参数口径，不静默丢弃）；
//   - 新增 request_id（创建/投轮幂等）与 title（登记期可见）参数——本机 agent
//     面 REST 原生支持（W01-03 RFC §1/§3），hub schema 无此二参；
//   - pending 会话（创建登记后首轮发布前）如实标注「登记中（首轮未发布）」并
//     展示 request_id/title；
//   - 不含 cancel/close（豁免⑬：取消与关闭是终局干预动作，开放 agent 面即互删
//     通道；本工具面无此能力即无绕行通道——主人专属入口在本机控制台/主人 REST）；
//   - 两种 backend 输出同款未经证实提醒（unverified 轮强制提示，与 hub 同词表
//     sessionStatusCN/sessionErrHints 单源共享）。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// nodeSessionTurnView 本机会话轮解码视图（两形态超集）：内核 protocol.Session.Turns
// （status=ok|failed + work_result + response + error，state 空）与 pending 投影轮
// （state=queued|running|finished + status=账本终态 + error=记账码）字段名同源，
// 解码互不干扰。
type nodeSessionTurnView struct {
	Seq        int64  `json:"seq"`
	Status     string `json:"status"`
	State      string `json:"state"`
	WorkResult string `json:"work_result"`
	Response   string `json:"response"`
	Error      string `json:"error"`
}

// nodeSessionView 本机会话条目解码视图（内核 protocol.Session 与 pending 投影的
// 字段超集：pending 项多 pending/request_id/title，内核项三者缺省零值）。
type nodeSessionView struct {
	ID             string                `json:"id"`
	AgentName      string                `json:"agent_name"`
	Adapter        string                `json:"adapter"`
	PermissionMode string                `json:"permission_mode"`
	Cwd            string                `json:"cwd"`
	Status         string                `json:"status"`
	TurnCount      int                   `json:"turn_count"`
	Turns          []nodeSessionTurnView `json:"turns"`
	Pending        bool                  `json:"pending"`
	RequestID      string                `json:"request_id"`
	Title          string                `json:"title"`
}

// headCN 会话级状态人话（hub/node 同词表：node 内核注册表两值 open/closed（hub
// REST 投影名 active/closed——双端值都识别，hub 工具组同款「活跃/已关闭」词）；
// pending 为本机受理登记事实的投影值——W01-03 RFC §2，如实标注不冒充活跃）。
func (v nodeSessionView) headCN() string {
	if v.Pending || v.Status == "pending" {
		return "登记中（首轮未发布）"
	}
	if v.Status == "open" || v.Status == "active" {
		return "活跃"
	}
	return "已关闭"
}

// nodeTurnStatusCN 轮次状态人话（归一到 hub sessionStatusCN 同词表）：
//   - 内核轮（state 空）：failed → 失败；work_result=unverified → 「已完成（工作
//     结果未经证实）」（node 控制台同款归一，internal/node/web/app.js sessLastState）；
//     其余 → 已完成（不冒充「已证实」）。
//   - pending 投影轮（state 非空）：账本受理事实——queued/running/finished+记账码
//     （W01-03 RFC §2：accepted=queued / started=running / finished=终态+记账码）。
func nodeTurnStatusCN(t nodeSessionTurnView) string {
	if t.State != "" {
		switch t.State {
		case "running":
			return sessionStatusCN("running")
		case "finished":
			if t.Error != "" {
				return "失败（" + t.Error + "）"
			}
			return sessionStatusCN("unverified") // 平台恒不证实工作成功（4.8）
		default:
			return sessionStatusCN("pending")
		}
	}
	switch {
	case t.Status == "failed":
		return sessionStatusCN("failed")
	case t.WorkResult == "unverified":
		return sessionStatusCN("unverified")
	}
	return "已完成"
}

// nodeUnverifiedReminder 未经证实强制提醒（= sessionUnverifiedReminder 单源别名：
// 两种 backend 输出同款由同一 const 构造保证，任务书硬性要求；禁止改写弱化）。
const nodeUnverifiedReminder = sessionUnverifiedReminder

// toolSessionCreate swarmmind_session_create（node 面）：本机创建登记（201，不执行
// 模型不投轮——首轮经 session_send 显式提交）。request_id 可选幂等（同键同文 200+dup
// 返原会话、异文 409）；缺省服务端自动生成（创建必须在返回前持久记录平台 ID）。
func (b nodeBackend) toolSessionCreate(args map[string]any) (string, error) {
	// hub-only 参数显式拒绝（不静默丢弃——静默=调用方以为权限断言生效而实际没有，
	// 同 toolNodeSubmit 对工作单绑定参数口径）。
	if pm := strings.TrimSpace(argString(args["permission_mode"], args["permission_mode"] != nil)); pm != "" {
		return "", fmt.Errorf("permission_mode 仅 hub 模式可用：本机面会话绑定由 node.yaml 能力声明决定（不收权限断言，防静默降权假象）")
	}
	agent := strings.TrimSpace(argString(args["agent"], args["agent"] != nil))
	cwd := strings.TrimSpace(argString(args["cwd"], args["cwd"] != nil))
	if agent == "" || cwd == "" {
		return "", fmt.Errorf("agent 与 cwd 均必填（cwd 为本机工作目录绝对路径）")
	}
	// node_id 本机面语义：可省略；传入须为本机节点 ID（会话只建在本机，无远端
	// 寻址——跨机会话请经 hub 模式）。显式拒绝不静默改道。
	if nodeID := strings.TrimSpace(argString(args["node_id"], args["node_id"] != nil)); nodeID != "" {
		local, err := b.localNodeID()
		if err != nil {
			return "", err
		}
		if nodeID != local {
			return "", fmt.Errorf("node_id %q 不是本机节点（本机为 %s）：node 模式会话只建在本机，跨机请经 hub 模式（SWARM_TARGET=hub）的 swarmmind_session_create", nodeID, local)
		}
	}
	req := map[string]any{"agent": agent, "cwd": cwd}
	if title := strings.TrimSpace(argString(args["title"], args["title"] != nil)); title != "" {
		req["title"] = title
	}
	if rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil)); rid != "" {
		req["request_id"] = rid
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/sessions", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 201 && code != 200 {
		return "", sessionErrHintAt("本机 node", code, data)
	}
	var out struct {
		Session nodeSessionView `json:"session"`
		Dup     bool            `json:"dup"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	s := out.Session
	perm := s.PermissionMode
	if perm == "" {
		perm = "（节点声明默认）"
	}
	adapter := s.Adapter
	if adapter == "" {
		adapter = "（未声明）"
	}
	dupNote := ""
	if out.Dup || code == 200 {
		dupNote = "（request_id 幂等命中：同键同文返回原会话，未重复登记）\n"
	}
	return fmt.Sprintf("会话已创建：%s（%s）%s\n绑定：%s @ 本机（适配器 %s，权限 %s）\n工作目录：%s\nrequest_id：%s\n创建仅登记，不执行模型不投轮——首轮用 swarmmind_session_send 提交；答复与轮次历史用 swarmmind_session_status 查看。",
		s.ID, s.headCN(), dupNote, s.AgentName, adapter, perm, s.Cwd, s.RequestID), nil
}

// toolSessionSend swarmmind_session_send（node 面）：向本机会话投一轮（202 受理即
// 回轮引用，不阻塞等待）。turn_seq 仅重放语义（本机序号由受理推导，不收跳号）；
// request_id 可选幂等（同键同文 200+dup 回原受理、异文 409）。错误指引与 hub 共用
// sessionErrHints 同词表。
func (b nodeBackend) toolSessionSend(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["session_id"], args["session_id"] != nil))
	prompt := argString(args["prompt"], args["prompt"] != nil)
	if sid == "" {
		return "", fmt.Errorf("session_id 必填（swarmmind_session_list 可查）")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt 必填（本轮输入正文）")
	}
	req := map[string]any{"prompt": prompt}
	if v, ok := args["turn_seq"].(float64); ok && v > 0 {
		if v != float64(int64(v)) {
			return "", fmt.Errorf("turn_seq 须为整数（1 起），收到 %v", v)
		}
		req["turn_seq"] = int64(v)
	}
	if rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil)); rid != "" {
		req["request_id"] = rid
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/sessions/"+sid+"/turns", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 202 && code != 200 {
		return "", sessionErrHintAt("本机 node", code, data)
	}
	var out struct {
		Turn struct {
			SessionID string `json:"session_id"`
			TurnSeq   int64  `json:"turn_seq"`
			Op        string `json:"op"`
			TaskID    string `json:"task_id"`
		} `json:"turn"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	opNote := "续轮"
	if out.Turn.Op == "create" {
		opNote = "首轮（建立原生会话映射）"
	}
	if out.Dup {
		return fmt.Sprintf("该轮此前已受理（同键同文幂等命中，未重复投递）：会话 %s 轮 #%d（%s，任务 %s）。\n用 swarmmind_session_status 查询轮终。",
			out.Turn.SessionID, out.Turn.TurnSeq, opNote, out.Turn.TaskID), nil
	}
	return fmt.Sprintf("轮次已受理：会话 %s 轮 #%d（%s，任务 %s）。\n轮终异步回写，用 swarmmind_session_status 查询答复与状态（受理即回，不阻塞等待）。",
		out.Turn.SessionID, out.Turn.TurnSeq, opNote, out.Turn.TaskID), nil
}

// toolSessionList swarmmind_session_list（node 面）：内核快照 ∪ 受理中会话（服务端
// 创建时刻升序——与 hub「新会话在前」的差异如实注明）。pending 项标注「登记中
// （首轮未发布）」并展示 request_id/title（首轮发布前可查平台 ID，RFC §2）。
func (b nodeBackend) toolSessionList() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/sessions", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", sessionErrHintAt("本机 node", code, data)
	}
	var out struct {
		Sessions []nodeSessionView `json:"sessions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Sessions) == 0 {
		return "本机还没有会话（swarmmind_session_create 新建；一次性任务不需要会话，直接 swarmmind_submit）。", nil
	}
	var bl strings.Builder
	bl.WriteString(fmt.Sprintf("本机会话列表：共 %d 个（创建时刻升序）", len(out.Sessions)))
	for _, s := range out.Sessions {
		last := "（无轮次）"
		if n := len(s.Turns); n > 0 {
			last = fmt.Sprintf("最近轮 #%d %s", s.Turns[n-1].Seq, nodeTurnStatusCN(s.Turns[n-1]))
		}
		fmt.Fprintf(&bl, "\n%s：%s @ 本机（%s，%d 轮，%s）", s.ID, s.AgentName, s.headCN(), s.TurnCount, last)
		if s.Pending {
			fmt.Fprintf(&bl, " · request_id %s", s.RequestID)
			if s.Title != "" {
				fmt.Fprintf(&bl, " · 标题《%s》", s.Title)
			}
		}
	}
	bl.WriteString("\n续聊用 swarmmind_session_send（传会话 ID）；详情与轮次历史用 swarmmind_session_status。")
	return bl.String(), nil
}

// toolSessionStatus swarmmind_session_status（node 面）：会话详情（轮终查询主路径）。
// unverified 轮强制提醒与 hub 同词表原句；pending 会话展示登记事实（request_id/
// title/受理轮态）；取消/关闭如实指路主人专属入口（MCP 工具面不提供——豁免⑬）。
func (b nodeBackend) toolSessionStatus(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["session_id"], args["session_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("session_id 必填")
	}
	code, data, err := b.nodeAPI("GET", "/api/v1/sessions/"+sid, nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", sessionErrHintAt("本机 node", code, data)
	}
	var out struct {
		Session nodeSessionView `json:"session"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	s := out.Session
	perm := s.PermissionMode
	if perm == "" {
		perm = "（节点声明默认）"
	}
	adapter := s.Adapter
	if adapter == "" {
		adapter = "（未声明）"
	}
	var bl strings.Builder
	fmt.Fprintf(&bl, "会话 %s：%s @ 本机（%s）\n适配器 %s · 权限 %s · 工作目录 %s · 共 %d 轮",
		s.ID, s.AgentName, s.headCN(), adapter, perm, s.Cwd, len(s.Turns))
	if s.Pending {
		fmt.Fprintf(&bl, "\nrequest_id：%s", s.RequestID)
		if s.Title != "" {
			fmt.Fprintf(&bl, " · 标题《%s》", s.Title)
		}
		bl.WriteString("\n登记中会话：平台 ID 已持久可查（创建登记），首轮受理后并入内核快照。")
	}
	for _, t := range s.Turns {
		fmt.Fprintf(&bl, "\n\n—— 轮 #%d：%s", t.Seq, nodeTurnStatusCN(t))
		if t.Error != "" && t.State == "" {
			fmt.Fprintf(&bl, "\n错误：%s", t.Error)
		}
		if strings.TrimSpace(t.Response) != "" {
			bl.WriteString("\n答复：" + truncateForStatus(t.Response))
		}
	}
	if n := len(s.Turns); n > 0 && nodeTurnStatusCN(s.Turns[n-1]) == sessionStatusCN("unverified") {
		bl.WriteString(nodeUnverifiedReminder)
	}
	switch {
	case s.Pending:
		bl.WriteString("\n\n首轮用 swarmmind_session_send 提交。")
	case s.Status == "open" || s.Status == "active":
		bl.WriteString("\n\n续聊用 swarmmind_session_send；取消/关闭为本机主人专属动作（本机控制台或主人 REST 面），MCP 工具面不提供——终局干预归人，误触不可自动恢复。")
	}
	return bl.String(), nil
}
