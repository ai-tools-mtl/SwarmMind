// sessions.go 会话（Session）工具组（T2 持久会话，SPEC 六.五「会话与轮次」节 +
// 六.六映射表四行 + 同步声明 18/19）。hub 模式专属（node 模式不装配不静默降级——
// node 本机原生线程管理是主人管理面，agent 面合法能力 = hub 会话组全量）。
// 使用心智与 submit/status/wait 同构：create 拿会话引用 → send 投轮（受理即回
// 引用，异步）→ status 查轮终（unverified 为合法完成态：执行成立但工作结果未经
// 证实，禁止 agent 冒充工作成功结论）。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sessionStatusCN 轮次状态人话（与 REST 投影态同词表；unverified 必须带
// 「未经证实」提示——4.8 语义的会话面镜像，禁止折叠为已完成）。
func sessionStatusCN(st string) string {
	switch st {
	case "pending":
		return "排队中"
	case "awaiting_node":
		return "已派发待节点确认"
	case "running":
		return "执行中"
	case "done":
		return "已完成（工作结果已证实）"
	case "unverified":
		return "已完成（工作结果未经证实）"
	case "failed":
		return "失败"
	}
	return st
}

// sessionErrHint 会话错误 → 人话（含处理指引；错误统一 isError 文本不泄内部）。
func sessionErrHint(code int, data []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	hint := map[string]string{
		"session_not_found":   "会话不存在（swarmmind_session_list 可查现有会话）",
		"session_closed":      "会话已关闭（拒绝续轮；原生线程留存节点侧不受影响）。继续协作请 swarmmind_session_create 新建会话",
		"session_unsupported": "目标能力未声明会话支持（session_adapter 未配置或节点版本过旧）：" + e.Message,
		"binding_mismatch":    "绑定不一致（禁止静默换绑/升权）：" + e.Message + "。换绑定须显式新建会话",
		"turn_seq_conflict":   "轮次序号冲突：" + e.Message + "。响应丢失重试须原样重发同一 turn_seq 与同一 prompt；新输入用下一个序号（或不传由 hub 分配）",
		"turn_in_flight":      "首轮尚未成立（在途执行中）：" + e.Message + "。等 swarmmind_session_status 显示首轮终态后再投下一轮",
		"agent_not_exposed":   "目标节点/能力不可达（未注册或已下线）：" + e.Message,
		"mailbox_full":        "目标节点信箱已满（稍后重试）：" + e.Message,
		"storage_full":        "hub 存储预算已满：" + e.Message,
		"frame_too_large":     "请求体超限（prompt 过长）：" + e.Message,
		"invalid_request":     e.Message,
	}[e.Error]
	if hint == "" {
		hint = truncate(data)
	}
	return fmt.Errorf("hub %d：%s", code, hint)
}

// toolSessionCreate swarmmind_session_create：新建会话（绑定远端节点/能力/工作区，
// 返回会话 ID；首轮经 swarmmind_session_send 投递）。
func toolSessionCreate(args map[string]any) (string, error) {
	node, _ := args["node_id"].(string)
	agent, _ := args["agent"].(string)
	cwd, _ := args["cwd"].(string)
	if strings.TrimSpace(node) == "" || strings.TrimSpace(agent) == "" || strings.TrimSpace(cwd) == "" {
		return "", fmt.Errorf("node_id/agent/cwd 均必填（cwd 为执行节点上的工作目录绝对路径）")
	}
	req := map[string]any{"node_id": strings.TrimSpace(node), "agent": strings.TrimSpace(agent), "cwd": strings.TrimSpace(cwd)}
	if pm, _ := args["permission_mode"].(string); strings.TrimSpace(pm) != "" {
		req["permission_mode"] = strings.TrimSpace(pm)
	}
	code, data, err := hubAPI("POST", "/api/v1/sessions", req)
	if err != nil {
		return "", err
	}
	if code != 201 {
		return "", sessionErrHint(code, data)
	}
	var out struct {
		Session struct {
			ID             string `json:"id"`
			NodeID         string `json:"node_id"`
			AgentName      string `json:"agent_name"`
			Adapter        string `json:"adapter"`
			PermissionMode string `json:"permission_mode"`
			Cwd            string `json:"cwd"`
		} `json:"session"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	s := out.Session
	perm := s.PermissionMode
	if perm == "" {
		perm = "（适配器保守默认）"
	}
	return fmt.Sprintf("会话已创建：%s\n绑定：%s @ %s（适配器 %s，权限 %s）\n工作目录：%s\n首轮用 swarmmind_session_send 提交；每轮对应同一原生会话线程，答复与轮次历史用 swarmmind_session_status 查看。",
		s.ID, s.AgentName, s.NodeID, s.Adapter, perm, s.Cwd), nil
}

// toolSessionSend swarmmind_session_send：向会话投一轮（异步受理——返回轮引用，
// 轮终用 swarmmind_session_status 查询，不阻塞等待）。turn_seq 可选：响应丢失
// 重试传同一序号+同一 prompt 命中回执不重复执行；缺省由 hub 分配下一序号。
func toolSessionSend(args map[string]any) (string, error) {
	sid, _ := args["session_id"].(string)
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(sid) == "" {
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
	code, data, err := hubAPI("POST", "/api/v1/sessions/"+sid+"/turns", req)
	if err != nil {
		return "", err
	}
	if code != 202 && code != 200 {
		return "", sessionErrHint(code, data)
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
		return fmt.Sprintf("该轮此前已受理（同序号同内容幂等命中，未重复投递）：会话 %s 轮 #%d（%s，任务 %s）。\n用 swarmmind_session_status 查询轮终。",
			out.Turn.SessionID, out.Turn.TurnSeq, opNote, out.Turn.TaskID), nil
	}
	return fmt.Sprintf("轮次已受理：会话 %s 轮 #%d（%s，任务 %s）。\n轮终异步回写，用 swarmmind_session_status 查询答复与状态（任务书语义：受理即回，不阻塞等待）。",
		out.Turn.SessionID, out.Turn.TurnSeq, opNote, out.Turn.TaskID), nil
}

// toolSessionList swarmmind_session_list：列出全部会话（ID/绑定/状态/轮数/最近
// 一轮状态），发现可续聊线程。
func toolSessionList() (string, error) {
	code, data, err := hubAPI("GET", "/api/v1/sessions", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", sessionErrHint(code, data)
	}
	var out struct {
		Sessions []struct {
			ID        string `json:"id"`
			NodeID    string `json:"node_id"`
			AgentName string `json:"agent_name"`
			Status    string `json:"status"`
			TurnCount int    `json:"turn_count"`
			LastTurn  *struct {
				Seq    int64  `json:"seq"`
				Status string `json:"status"`
			} `json:"last_turn"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Sessions) == 0 {
		return "还没有会话（swarmmind_session_create 新建；一次性任务不需要会话，直接 swarmmind_submit）。", nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("会话列表：共 %d 个（新会话在前）", len(out.Sessions)))
	for _, s := range out.Sessions {
		st := "活跃"
		if s.Status != "active" {
			st = "已关闭"
		}
		last := "（无轮次）"
		if s.LastTurn != nil {
			last = fmt.Sprintf("最近轮 #%d %s", s.LastTurn.Seq, sessionStatusCN(s.LastTurn.Status))
		}
		fmt.Fprintf(&b, "\n%s：%s @ %s（%s，%d 轮，%s）", s.ID, s.AgentName, s.NodeID, st, s.TurnCount, last)
	}
	b.WriteString("\n续聊用 swarmmind_session_send（传会话 ID）；详情与轮次历史用 swarmmind_session_status。")
	return b.String(), nil
}

// toolSessionStatus swarmmind_session_status：会话详情（绑定快照 + 轮次账本投影
// 状态 + 答复摘要）。轮终查询主路径；unverified 轮明确提示工作结果未经证实。
func toolSessionStatus(args map[string]any) (string, error) {
	sid, _ := args["session_id"].(string)
	if strings.TrimSpace(sid) == "" {
		return "", fmt.Errorf("session_id 必填")
	}
	code, data, err := hubAPI("GET", "/api/v1/sessions/"+sid, nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", sessionErrHint(code, data)
	}
	var out struct {
		Session struct {
			ID             string `json:"id"`
			NodeID         string `json:"node_id"`
			AgentName      string `json:"agent_name"`
			Adapter        string `json:"adapter"`
			PermissionMode string `json:"permission_mode"`
			Cwd            string `json:"cwd"`
			Status         string `json:"status"`
			Turns          []struct {
				Seq        int64  `json:"seq"`
				Status     string `json:"status"`
				Response   string `json:"response"`
				Error      string `json:"error"`
				WorkResult string `json:"work_result"`
				TaskID     string `json:"task_id"`
				TaskGone   bool   `json:"task_gone"`
			} `json:"turns"`
		} `json:"session"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	s := out.Session
	head := "活跃"
	if s.Status != "active" {
		head = "已关闭"
	}
	var b strings.Builder
	perm := s.PermissionMode
	if perm == "" {
		perm = "（适配器保守默认）"
	}
	fmt.Fprintf(&b, "会话 %s：%s @ %s（%s）\n适配器 %s · 权限 %s · 工作目录 %s · 共 %d 轮",
		s.ID, s.AgentName, s.NodeID, head, s.Adapter, perm, s.Cwd, len(s.Turns))
	for _, t := range s.Turns {
		fmt.Fprintf(&b, "\n\n—— 轮 #%d：%s", t.Seq, sessionStatusCN(t.Status))
		if t.TaskID != "" {
			fmt.Fprintf(&b, "（任务 %s", t.TaskID)
			if t.TaskGone {
				b.WriteString("，已过保留期清理")
			}
			b.WriteString("）")
		}
		if t.Error != "" {
			fmt.Fprintf(&b, "\n错误：%s", t.Error)
		}
		if strings.TrimSpace(t.Response) != "" {
			b.WriteString("\n答复：" + truncateForStatus(t.Response))
		}
	}
	if len(s.Turns) > 0 {
		last := s.Turns[len(s.Turns)-1]
		if last.Status == "unverified" {
			b.WriteString("\n\n注意：最近一轮工作结果未经证实（平台只证明执行成立，不证明工作成功）——需要你自行核对答复内容，禁止基于未经核对的轮次下结论。")
		}
	}
	if s.Status == "active" {
		b.WriteString("\n\n续聊用 swarmmind_session_send；关闭用 REST POST /api/v1/sessions/{id}/close（无 MCP 工具——关闭是收尾动作，误关会断线程）。")
	}
	return b.String(), nil
}

// truncateForStatus 答复摘要截断（MCP 面防长答复刷屏；完整答复属 T3 控制台面）。
func truncateForStatus(s string) string {
	const limit = 600
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit]) + "…（截断）"
}
