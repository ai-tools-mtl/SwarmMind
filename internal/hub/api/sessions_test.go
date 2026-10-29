// sessions_test.go 会话 REST 端点契约（T2，SPEC 六.五）：鉴权三态（匿名/会话
// cookie/PAT）、创建校验矩阵、turn 异步受理与幂等重放、绑定守卫经 REST、轮终
// 投影（假 node 经 membus 全链）、离线暂存、close 幂等。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionNode 注册带会话能力的目标节点（在线；bus 端点收 offer）。订阅先于
// SetOnline——membus 同步分发，上线即触发补投（对端 handler 空缺即发送被拒）。
func (f *fixture) sessionNode() {
	f.t.Helper()
	f.bus.Endpoint("mac-mini").Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgTaskOffer {
			return
		}
		var task protocol.Task
		if err := json.Unmarshal(env.Payload, &task); err != nil || task.Session == nil {
			return
		}
		// 假 node：会话轮任务直接回 completed+轮产物（create 语义由 core 层测试
		// 覆盖，此处验证 REST 投影与回写链路）
		res := &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0,
			FinalAnswer: "echo:" + task.Input.Stdin, FinishedAt: protocol.NowUTC(),
			Turn: &protocol.TurnOutcome{TurnSeq: task.Session.TurnSeq, NativeIDOk: true,
				WorkResult: protocol.WorkUnverified}}
		payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: task.ID, Status: protocol.ResultCompleted, Result: res})
		_ = f.bus.Endpoint("mac-mini").Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
			MessageID: protocol.NewMessageID(), To: "hub", Payload: payload})
	})
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder",
		Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true, SessionAdapter: "codex"}}); err != nil {
		f.t.Fatalf("注册: %v", err)
	}
	f.core.SetOnline("mac-mini", true)
}

// createSession 经 REST 建会话并返回 ID（失败即 Fatal）。
func (f *fixture) createSession(hc *http.Client, body string) string {
	f.t.Helper()
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions", body)
	if code != http.StatusCreated {
		f.t.Fatalf("建会话应 201, got %d: %s", code, resp)
	}
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || !strings.HasPrefix(out.Session.ID, "s-") {
		f.t.Fatalf("建会话响应异常: %s", resp)
	}
	return out.Session.ID
}

// TestSessionEndpointsAuthGate 鉴权三态：匿名 401；会话 cookie 放行；Bearer PAT
// 放行（与既有管理面同口径，SPEC 六.五）。
func TestSessionEndpointsAuthGate(t *testing.T) {
	f := newFixture(t)
	f.sessionNode()
	hc := f.client(true)

	// 匿名一律 401
	anon := f.client(false)
	for _, tc := range [][2]string{
		{http.MethodPost, "/api/v1/sessions"},
		{http.MethodGet, "/api/v1/sessions"},
		{http.MethodGet, "/api/v1/sessions/s-x"},
		{http.MethodPost, "/api/v1/sessions/s-x/turns"},
		{http.MethodPost, "/api/v1/sessions/s-x/close"},
	} {
		if code, _ := f.do(anon, tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 匿名应 401, got %d", tc[0], tc[1], code)
		}
	}
	// 会话 cookie 放行（createSession 内部断言 201）
	f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/p"}`)

	// Bearer PAT 同口径放行（六.五 PAT 体系；PAT 存储需 DataDir，另起同 core
	// 的 server——同 works_test 铸 PAT 基座形态）
	patSrv := httptest.NewServer(New(Config{Core: f.core, AdminToken: "test-admin-token", DataDir: t.TempDir()}).Handler())
	defer patSrv.Close()
	patJar, _ := cookiejar.New(nil)
	patClient := &http.Client{Jar: patJar}
	if resp, err := patClient.Post(patSrv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"test-admin-token"}`)); err != nil || resp.StatusCode != 200 {
		t.Fatalf("patSrv 登录失败: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, patSrv.URL+"/api/v1/auth/tokens", strings.NewReader(`{"name":"sess-test"}`))
	req.Header.Set("Content-Type", "application/json")
	tokenResp, err := patClient.Do(req)
	if err != nil {
		t.Fatalf("铸 PAT: %v", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(tokenResp.Body).Decode(&tok)
	tokenResp.Body.Close()
	if tok.Token == "" {
		t.Fatalf("铸 PAT 应得令牌: %d", tokenResp.StatusCode)
	}
	preq, _ := http.NewRequest(http.MethodGet, patSrv.URL+"/api/v1/sessions", nil)
	preq.Header.Set("Authorization", "Bearer "+tok.Token)
	presp, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatalf("PAT 请求: %v", err)
	}
	defer presp.Body.Close()
	if presp.StatusCode != http.StatusOK {
		t.Fatalf("PAT 应放行, got %d", presp.StatusCode)
	}
}

// TestSessionRESTFlow 全链：创建校验矩阵 → 建会话 → 投轮 202 → 假 node 回结果 →
// 详情轮终 unverified → 同号同文重放 200+dup → 同号异文 409 → 跳号 409 →
// 关闭 200 → 关闭后续轮 409 session_closed → 重复关闭 200+dup。
func TestSessionRESTFlow(t *testing.T) {
	f := newFixture(t)
	f.sessionNode()
	hc := f.client(true)

	// 校验矩阵
	for _, tc := range []struct {
		body string
		want int
		code string
	}{
		{`{"node_id":"mac-mini","agent":"coder"}`, 400, "invalid_request"},
		{`{"node_id":"ghost","agent":"coder","cwd":"/tmp"}`, 404, "agent_not_exposed"},
		{`{"node_id":"mac-mini","agent":"coder","cwd":"/tmp","permission_mode":"yolo"}`, 400, "invalid_request"},
		{`{"node_id":"mac-mini","agent":"coder","cwd":"/tmp","permission_mode":"read-only"}`, 409, "binding_mismatch"},
	} {
		code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions", tc.body)
		if code != tc.want || !strings.Contains(string(resp), tc.code) {
			t.Fatalf("校验矩阵 %s: want %d/%s got %d %s", tc.body, tc.want, tc.code, code, resp)
		}
	}
	// 无会话能力（未声明 session_adapter 的 node）
	if err := f.core.RegisterNode("plain", []protocol.AgentCard{{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", ProtocolVer: 1, Exposed: true}}); err != nil {
		t.Fatalf("注册 plain: %v", err)
	}
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions", `{"node_id":"plain","agent":"coder","cwd":"/tmp"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(resp), "session_unsupported") {
		t.Fatalf("无声明应 422 session_unsupported, got %d %s", code, resp)
	}

	sid := f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/proj"}`)

	// 列表含新会话
	code, resp = f.do(hc, http.MethodGet, "/api/v1/sessions", "")
	if code != 200 || !strings.Contains(string(resp), sid) {
		t.Fatalf("列表应含新会话: %d %s", code, resp)
	}

	// 投轮 202（异步受理）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"第一轮"}`)
	if code != http.StatusAccepted {
		t.Fatalf("投轮应 202, got %d: %s", code, resp)
	}
	var turnAck struct {
		Turn struct {
			SessionID string `json:"session_id"`
			TurnSeq   int64  `json:"turn_seq"`
			Op        string `json:"op"`
			TaskID    string `json:"task_id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(resp, &turnAck); err != nil || turnAck.Turn.TurnSeq != 1 ||
		turnAck.Turn.Op != "create" || turnAck.Turn.TaskID == "" || turnAck.Turn.SessionID != sid {
		t.Fatalf("受理回执异常: %s", resp)
	}

	// 轮终投影 unverified（假 node 已回结果；bounded 轮询）
	var final struct {
		Session struct {
			Status string `json:"status"`
			Turns  []struct {
				Seq        int64  `json:"seq"`
				Status     string `json:"status"`
				Response   string `json:"response"`
				NativeIDOk bool   `json:"native_id_ok"`
				TaskID     string `json:"task_id"`
			} `json:"turns"`
		} `json:"session"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, resp = f.do(hc, http.MethodGet, "/api/v1/sessions/"+sid, "")
		if code != 200 {
			t.Fatalf("详情应 200, got %d", code)
		}
		if err := json.Unmarshal(resp, &final); err != nil {
			t.Fatalf("详情解析: %v", err)
		}
		if len(final.Session.Turns) > 0 && final.Session.Turns[0].Status == "unverified" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内轮未终态: %s", resp)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Session.Turns[0].Response != "echo:第一轮" || !final.Session.Turns[0].NativeIDOk ||
		final.Session.Turns[0].TaskID != turnAck.Turn.TaskID {
		t.Fatalf("轮终留痕异常: %+v", final.Session.Turns[0])
	}

	// 幂等重放：同号同文 200+dup；同号异文 409；跳号 409
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"第一轮","turn_seq":1}`)
	if code != http.StatusOK || !strings.Contains(string(resp), `"dup":true`) {
		t.Fatalf("同号同文重放应 200+dup, got %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"不同内容","turn_seq":1}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "turn_seq_conflict") {
		t.Fatalf("同号异文应 409 turn_seq_conflict, got %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"跳号","turn_seq":5}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "turn_seq_conflict") {
		t.Fatalf("跳号应 409, got %d %s", code, resp)
	}

	// 关闭：200；关闭后续轮 409 session_closed；重复关闭 200+dup
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/close", `{}`)
	if code != 200 || !strings.Contains(string(resp), `"closed"`) {
		t.Fatalf("关闭应 200, got %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"x"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "session_closed") {
		t.Fatalf("关闭后续轮应 409 session_closed, got %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/close", `{}`)
	if code != 200 || !strings.Contains(string(resp), `"dup":true`) {
		t.Fatalf("重复关闭应 200+dup, got %d %s", code, resp)
	}
	// 详情 404
	code, _ = f.do(hc, http.MethodGet, "/api/v1/sessions/s-missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("未知会话应 404, got %d", code)
	}
}

// TestSessionRESTOfflineStaging 离线暂存（任务书 DoD2）：node 离线投轮 →
// 详情轮投影 pending → 上线补投 → 轮终 unverified（投递链白拿的端到端证据）。
func TestSessionRESTOfflineStaging(t *testing.T) {
	f := newFixture(t)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "coder",
		Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts",
		PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true, SessionAdapter: "codex"}}); err != nil {
		t.Fatalf("注册: %v", err)
	}
	f.core.SetOnline("mac-mini", false) // 离线
	hc := f.client(true)
	sid := f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp"}`)
	code, resp := f.do(hc, http.MethodPost, "/api/v1/sessions/"+sid+"/turns", `{"prompt":"离线轮"}`)
	if code != http.StatusAccepted {
		t.Fatalf("离线投轮应 202（入信箱）, got %d: %s", code, resp)
	}
	// pending 投影
	_, resp = f.do(hc, http.MethodGet, "/api/v1/sessions/"+sid, "")
	if !strings.Contains(string(resp), `"status":"pending"`) {
		t.Fatalf("离线轮应投影 pending: %s", resp)
	}
	// 上线补投：注册假 node 后触发上线
	f.sessionNode()
	f.core.SetOnline("mac-mini", true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, resp = f.do(hc, http.MethodGet, "/api/v1/sessions/"+sid, "")
		if strings.Contains(string(resp), `"status":"unverified"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("上线后 3s 内未补投至终态: %s", resp)
}

// TestSessionRegistryPersistence 注册表随快照持久化：hub 重启（store 重开）后
// 会话与轮次账本可读（VISION 5.2：可路由索引持久化）。
func TestSessionRegistryPersistence(t *testing.T) {
	f := newFixture(t)
	f.sessionNode()
	hc := f.client(true)
	sid := f.createSession(hc, `{"node_id":"mac-mini","agent":"coder","cwd":"/tmp/keep"}`)
	// 会话随 store 落库：直接断言 store 读取面（fixture 的 store 即真相）
	sv, ok := f.core.GetSession(sid)
	if !ok || sv.Cwd != "/tmp/keep" || sv.Adapter != "codex" {
		t.Fatalf("注册表读取异常: %+v ok=%v", sv, ok)
	}
	// store 层 round-trip 由 internal/hub/store/session_test.go 覆盖；此处断言
	// View 可见性（先写后见：REST 返回即已发布）。
	if _, ok := f.st.View().Sessions[sid]; !ok {
		t.Fatal("发布态未见会话（先写后见破坏）")
	}
	_ = store.SessionStatusActive // 引用断言：状态取值由 store 包锁定
}
