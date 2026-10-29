// sessions_test.go 会话工具组 MCP 层实测（SPEC 六.六 同步开发规则③ + 规则 18）：
// 真实 api server 全链（工具函数 → HTTP → core → 投递链 → 假 node 回结果），
// 覆盖 hub-only 装配（node 模式不装配）、四工具输出语义、幂等重放人话、
// 错误指引（closed/conflict/unsupported）、参数本地校验拒绝。
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestSessionToolsHubOnly 装配：hub 模式含四会话工具；node 模式不含（本机原生
// 线程管理归主人管理面，六.六豁免⑩——hub-only 同 works 形态，不静默降级）。
func TestSessionToolsHubOnly(t *testing.T) {
	want := map[string]bool{
		"swarmmind_session_create": false, "swarmmind_session_send": false,
		"swarmmind_session_list": false, "swarmmind_session_status": false,
	}
	for _, td := range tools() {
		if _, ok := want[td.Name]; ok {
			want[td.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("hub 模式工具清单缺 %s", name)
		}
	}
	for _, td := range (nodeBackend{}).tools() {
		if _, ok := want[td.Name]; ok {
			t.Fatalf("node 模式不应装配 %s（hub-only 不静默降级）", td.Name)
		}
	}
}

// startFakeSessionNode 注册带会话能力的 node 并以 membus 端点假扮 node：收到
// 会话轮 offer 即回 completed（真实投递链，与 REST 契约测试同型）。
func startFakeSessionNode(t *testing.T) {
	t.Helper()
	c, bus := realHubCoreBus(t)
	if err := c.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder",
		Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true, SessionAdapter: "codex"}}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	ep := bus.Endpoint("mac-mini")
	ep.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgTaskOffer {
			return
		}
		var task protocol.Task
		if err := json.Unmarshal(env.Payload, &task); err != nil || task.Session == nil {
			return
		}
		res := &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0,
			FinalAnswer: "echo:" + task.Input.Stdin, FinishedAt: protocol.NowUTC(),
			Turn: &protocol.TurnOutcome{TurnSeq: task.Session.TurnSeq, NativeIDOk: true,
				WorkResult: protocol.WorkUnverified}}
		payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: task.ID,
			Status: protocol.ResultCompleted, Result: res})
		_ = ep.Send("hub", &protocol.Envelope{Version: protocol.ProtocolVersion,
			Type: protocol.MsgTaskResult, MessageID: protocol.NewMessageID(), To: "hub", Payload: payload})
	})
	c.SetOnline("mac-mini", true)
}

// waitUnverified 轮询 status 工具直到末轮 unverified（投递链异步）。
func waitUnverified(t *testing.T, sid string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := mustCallE(t, "swarmmind_session_status", map[string]any{"session_id": sid})
		if err == nil && strings.Contains(out, "未经证实") && strings.Contains(out, "轮 #1") {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("3s 内轮未至 unverified")
	return ""
}

// mustCallE mustCall 的 error 显式形态（校验失败路径断言用）。
func mustCallE(t *testing.T, name string, args map[string]any) (string, error) {
	t.Helper()
	res, rpcErr := callTool(name, args)
	if rpcErr != nil {
		t.Fatalf("%s rpc 错误: %v", name, rpcErr)
	}
	text := res["content"].([]textContent)[0].Text
	if res["isError"] == true {
		return text, errString(text)
	}
	return text, nil
}

type errString string

func (e errString) Error() string { return string(e) }

// TestSessionToolsFullFlow 全链：create（绑定信息回显）→ send（受理回执 + 指路
// status）→ 轮终 unverified（含未经证实提示）→ list（行含绑定与状态）→ 同号同文
// 重放 dup 人话 → 错误指引矩阵 → 参数本地校验。
func TestSessionToolsFullFlow(t *testing.T) {
	startFakeSessionNode(t)

	out := mustCall(t, "swarmmind_session_create", map[string]any{
		"node_id": "mac-mini", "agent": "coder", "cwd": "/tmp/proj",
	})
	if !strings.Contains(out, "会话已创建") || !strings.Contains(out, "s-") ||
		!strings.Contains(out, "workspace-write") || !strings.Contains(out, "codex") {
		t.Fatalf("create 输出异常: %s", out)
	}
	sid := out[strings.Index(out, "s-"):]
	if i := strings.IndexAny(sid, "\n"); i >= 0 {
		sid = sid[:i]
	}
	sid = strings.TrimSpace(sid)

	// send：受理回执
	out = mustCall(t, "swarmmind_session_send", map[string]any{"session_id": sid, "prompt": "第一轮"})
	if !strings.Contains(out, "轮次已受理") || !strings.Contains(out, "#1") || !strings.Contains(out, "首轮") {
		t.Fatalf("send 输出异常: %s", out)
	}

	// 轮终 unverified（含未经证实强制提示）
	out = waitUnverified(t, sid)
	if !strings.Contains(out, "echo:第一轮") {
		t.Fatalf("status 应含答复: %s", out)
	}

	// list：行含会话与状态
	out = mustCall(t, "swarmmind_session_list", map[string]any{})
	if !strings.Contains(out, sid) || !strings.Contains(out, "活跃") || !strings.Contains(out, "未经证实") {
		t.Fatalf("list 输出异常: %s", out)
	}

	// 同号同文重放：dup 人话
	out = mustCall(t, "swarmmind_session_send", map[string]any{"session_id": sid, "prompt": "第一轮", "turn_seq": float64(1)})
	if !strings.Contains(out, "幂等命中") || !strings.Contains(out, "未重复投递") {
		t.Fatalf("重放应 dup 人话: %s", out)
	}

	// 错误指引矩阵（人话 + isError）
	errCases := []struct {
		name string
		call func() (string, bool)
		want string
	}{
		{"未知会话", func() (string, bool) {
			res, _ := callTool("swarmmind_session_status", map[string]any{"session_id": "s-nope"})
			return res["content"].([]textContent)[0].Text, res["isError"] == true
		}, "会话不存在"},
		{"全权档位断言", func() (string, bool) {
			res, _ := callTool("swarmmind_session_create", map[string]any{
				"node_id": "mac-mini", "agent": "coder", "cwd": "/tmp", "permission_mode": "yolo"})
			return res["content"].([]textContent)[0].Text, res["isError"] == true
		}, "全权档位"},
		{"未知节点", func() (string, bool) {
			res, _ := callTool("swarmmind_session_create", map[string]any{
				"node_id": "ghost-node", "agent": "coder", "cwd": "/tmp"})
			return res["content"].([]textContent)[0].Text, res["isError"] == true
		}, "不可达"},
	}
	for _, tc := range errCases {
		text, isErr := tc.call()
		if !isErr || !strings.Contains(text, tc.want) {
			t.Fatalf("%s 应 isError 且报 %q: %s (isErr=%v)", tc.name, tc.want, text, isErr)
		}
	}

	// 参数本地校验（不发请求即拒绝）
	if _, err := mustCallE(t, "swarmmind_session_send", map[string]any{"session_id": sid}); err == nil ||
		!strings.Contains(err.Error(), "prompt 必填") {
		t.Fatalf("缺 prompt 应参数错误: %v", err)
	}
	if _, err := mustCallE(t, "swarmmind_session_send", map[string]any{"session_id": sid, "prompt": "x", "turn_seq": 1.5}); err == nil ||
		!strings.Contains(err.Error(), "整数") {
		t.Fatalf("非整数 turn_seq 应拒绝（不截断）: %v", err)
	}
}
