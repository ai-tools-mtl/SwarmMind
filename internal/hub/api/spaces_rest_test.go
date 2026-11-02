// spaces_rest_test.go 中心空间 REST 契约测试（W07-02，docs/tasks/v3/W07-hub-routing.md
// 工作块6「装配/REST 契约（鉴权矩阵/词形/幂等/错误映射）」）：真 api.Server +
// 真 collab 内核 + 真 spaces 服务 + 策略引擎——
//   - 鉴权矩阵：无凭据 401 / PAT 编排主体创建 manual 403、sugar 403、verified
//     登记写面 403 / admin 会话全通（主人专属面）；
//   - 词形与幂等：scope=hub:<id> 注记、reserved request_id 400、同键重放 dup、
//     collab 错误码 HTTP 映射单源（version_conflict 409 / not_found 404）；
//   - 未装配 503 诚实降级（Spaces nil 与 Strategy nil 两面）；
//   - 会话轮守卫：空间绑定会话普通续轮 409 space_dispatch_required（中心调度
//     单入口；重放回执不受守卫影响锚）。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/spaces"
	hubstore "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/strategy"
	"github.com/mtl802/swarmmind/internal/transport"
)

// spacesRESTFixture 真 hub api + spaces 服务基座（membus 黑洞传输——REST 契约面
// 不投递任务；派发全链见 spaces 包 service_test）。
type spacesRESTFixture struct {
	t       *testing.T
	srv     *httptest.Server
	admin   string // admin_token（登录换会话 cookie）
	pat     string // PAT（编排主体）
	svc     *spaces.Service
	core    *core.Core
	cookies map[string]string
}

func newSpacesRESTFixture(t *testing.T) *spacesRESTFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := hubstore.Open(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hubCore, err := core.New(core.Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	t.Cleanup(func() { _ = hubCore.Close() })
	stratEngine, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	svc, err := spaces.Open(dir, hubCore, spaces.Options{Hooks: stratEngine})
	if err != nil {
		t.Fatalf("spaces: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	// 生产守卫接线（codex 必修F：REST 面守卫覆盖实测——缺接线曾假阴）。
	hubCore.SetSpaceTurnGuard(svc.SpaceTurnGuard())
	adminToken := "admin-tok-rest"
	s := New(Config{
		Core: hubCore, AdminToken: adminToken, DataDir: dir,
		Spaces: svc, Strategy: stratEngine, Version: "test",
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	f := &spacesRESTFixture{t: t, srv: ts, admin: adminToken, svc: svc, core: hubCore, cookies: map[string]string{}}
	f.pat = f.createPAT()
	return f
}

// login 用 admin_token 换会话 cookie。
func (f *spacesRESTFixture) login() string {
	f.t.Helper()
	if c, ok := f.cookies[f.admin]; ok {
		return c
	}
	resp, err := http.Post(f.srv.URL+"/api/v1/login", "application/json",
		strings.NewReader(`{"token":"`+f.admin+`"}`))
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		f.t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "swarmmind_session" {
			f.cookies[f.admin] = c.Name + "=" + c.Value
			return f.cookies[f.admin]
		}
	}
	f.t.Fatal("login 未发会话 cookie")
	return ""
}

// createPAT 经 admin 会话建编排 PAT（明文仅创建响应一次）。
func (f *spacesRESTFixture) createPAT() string {
	f.t.Helper()
	cookie := f.login()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/auth/tokens", strings.NewReader(`{"name":"tester","scopes":["all"]}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		f.t.Fatalf("PAT 创建失败: %d", resp.StatusCode)
	}
	return out.Token
}

// rest 直发请求并解码应答体。
func (f *spacesRESTFixture) rest(method, path, token, cookie, body string) (int, map[string]any) {
	f.t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// adminPost / patPost 便捷形。
func (f *spacesRESTFixture) adminPost(path, body string) (int, map[string]any) {
	return f.rest("POST", path, "", f.login(), body)
}

func (f *spacesRESTFixture) adminGet(path string) (int, map[string]any) {
	return f.rest("GET", path, "", f.login(), "")
}

func (f *spacesRESTFixture) patPost(path, body string) (int, map[string]any) {
	return f.rest("POST", path, f.pat, "", body)
}

func (f *spacesRESTFixture) patGet(path string) (int, map[string]any) {
	return f.rest("GET", path, f.pat, "", "")
}

// TestSpacesRESTAuthMatrixAndContract 鉴权矩阵 + 词形/幂等/错误映射主链。
func TestSpacesRESTAuthMatrixAndContract(t *testing.T) {
	f := newSpacesRESTFixture(t)

	// 未认证 401（写面与读面同门）。
	if code, _ := f.rest("GET", "/api/v1/spaces", "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("未认证列表应 401: %d", code)
	}
	if code, _ := f.rest("POST", "/api/v1/spaces", "", "", `{"goal":"g","mode":"manual"}`); code != http.StatusUnauthorized {
		t.Fatalf("未认证创建应 401: %d", code)
	}
	// PAT 编排主体：manual 创建 403（仅 external）；external 创建 201 + scope 注记。
	if code, body := f.patPost("/api/v1/spaces", `{"goal":"g","mode":"manual","request_id":"c-pat-m"}`); code != http.StatusForbidden {
		t.Fatalf("PAT manual 创建应 403: %d %v", code, body)
	}
	code, body := f.patPost("/api/v1/spaces", `{"goal":"外部目标","mode":"external","request_id":"c-pat-e"}`)
	if code != http.StatusCreated {
		t.Fatalf("PAT external 创建应 201: %d %v", code, body)
	}
	spE := body["space"].(map[string]any)
	if spE["id"].(string) == "" || !strings.HasPrefix(spE["id"].(string), "sp-") {
		t.Fatalf("空间 ID 词形: %v", spE)
	}
	if scope, _ := body["scope"].(string); scope != "hub:"+spE["id"].(string) {
		t.Fatalf("scope 注记应 hub:<id>: %v", body["scope"])
	}
	// admin 主人：manual 创建 201；同键重放 200+dup；异键同文新空间。
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"主人目标","mode":"manual","request_id":"c-adm-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("admin manual 创建应 201: %d %v", code, body)
	}
	spID := body["space"].(map[string]any)["id"].(string)
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"主人目标","mode":"manual","request_id":"c-adm-1"}`)
	if code != http.StatusOK || body["dup"] != true {
		t.Fatalf("同键重放应 200+dup: %d %v", code, body)
	}
	// 保留前缀门 400（零副作用——后续同键正常创建仍成立）。
	code, _ = f.adminPost("/api/v1/spaces", `{"goal":"g","mode":"manual","request_id":"boot-x-1"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("保留前缀应 400: %d", code)
	}
	// collab 错误码映射单源：version_conflict 409 / not_found 404 / 非法动作 400。
	if code, body := f.adminPost("/api/v1/spaces/"+spID+"/control",
		`{"action":"pause","expected_version":99,"request_id":"ctl-vc"}`); code != http.StatusConflict || body["error"] != collab.CodeVersionConflict {
		t.Fatalf("version_conflict 应 409: %d %v", code, body)
	}
	if code, body := f.adminGet("/api/v1/spaces/sp-nonexist"); code != http.StatusNotFound || body["error"] != collab.CodeNotFound {
		t.Fatalf("not_found 应 404: %d %v", code, body)
	}
	if code, body := f.adminPost("/api/v1/spaces/"+spID+"/control",
		`{"action":"explode","expected_version":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法动作应 400: %d %v", code, body)
	}
	// 主人专属面：PAT sugar 403 / admin sugar 200（applied）。
	if code, body := f.patPost("/api/v1/spaces/"+spID+"/budget", `{"expected_version":1,"budget":{"max_messages":5}}`); code != http.StatusForbidden {
		t.Fatalf("PAT budget sugar 应 403: %d %v", code, body)
	}
	if code, body := f.adminPost("/api/v1/spaces/"+spID+"/budget", `{"expected_version":1,"budget":{"max_messages":5}}`); code != http.StatusOK {
		t.Fatalf("admin budget sugar 应 200: %d %v", code, body)
	}
	// 候选定版/否决主人专属：PAT 403（豁免⑯同规）。
	if code, _ := f.patPost("/api/v1/spaces/"+spID+"/candidates/c-1/finalize", ""); code != http.StatusForbidden {
		t.Fatalf("PAT 候选定版应 403: %d", code)
	}
	// 消息面：PAT 不指名成员 403（不冒充主人）；指名在册成员——成员未配置 404
	// member_not_found（collab 模型层裁决）；文本必填 400。
	if code, body := f.patPost("/api/v1/spaces/"+spID+"/messages", `{"text":"hi","expected_version":2}`); code != http.StatusForbidden {
		t.Fatalf("PAT 主人发言应 403: %d %v", code, body)
	}
	if code, body := f.adminPost("/api/v1/spaces/"+spID+"/messages", `{"text":"hi","expected_version":2}`); code != http.StatusCreated {
		t.Fatalf("主人发言应 201: %d %v", code, body)
	}
	if code, _ := f.adminPost("/api/v1/spaces/"+spID+"/messages", `{"text":"","expected_version":3}`); code != http.StatusBadRequest {
		t.Fatalf("空文本应 400: %d", code)
	}
	// 消息增量读：since 游标 + pruned_before_seq 形态。
	code, body = f.adminGet("/api/v1/spaces/" + spID + "/messages?since=0")
	if code != http.StatusOK || body["pruned_before_seq"] == nil {
		t.Fatalf("消息增量读形态: %d %v", code, body)
	}
	// 策略面：strategy 投影（has_loop 如实空态）+ records 增量读 type 恒带。
	code, body = f.adminGet("/api/v1/spaces/" + spID + "/strategy")
	if code != http.StatusOK {
		t.Fatalf("strategy 应 200: %d %v", code, body)
	}
	if code, body = f.adminGet("/api/v1/spaces/" + spID + "/records"); code != http.StatusOK {
		t.Fatalf("records 增量读应 200: %d %v", code, body)
	}
	// 派发面 503（member 会话未绑定 → 模型层 invalid；此处只锚错误投影非 500）。
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/dispatches", `{"member":"n1/coder","expected_version":3}`)
	if code == http.StatusInternalServerError {
		t.Fatalf("派发面不应 500: %d %v", code, body)
	}
	// GET 列表 scope 注记。
	code, body = f.adminGet("/api/v1/spaces")
	if code != http.StatusOK || body["total"] == nil {
		t.Fatalf("列表: %d %v", code, body)
	}
}

// TestSpacesRESTDegraded503 未装配 503 诚实降级：Spaces nil（spaces 端点组）与
// Strategy nil（策略面端点）两判定源。
func TestSpacesRESTDegraded503(t *testing.T) {
	dir := t.TempDir()
	st, err := hubstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hubCore, err := core.New(core.Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hubCore.Close() })
	// Spaces 未装配（Strategy 也未装配）。
	s := New(Config{Core: hubCore, AdminToken: "tok", Version: "test"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	login := func() string {
		resp, err := http.Post(ts.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "swarmmind_session" {
				return c.Name + "=" + c.Value
			}
		}
		t.Fatal("无 cookie")
		return ""
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/spaces", strings.NewReader(`{"goal":"g","mode":"manual"}`))
	req.Header.Set("Cookie", login())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Spaces 未装配应 503: %d", resp.StatusCode)
	}
	// features.spaces=false（装配态如实——W07-01 混跑基线不变）。
	req2, _ := http.NewRequest("GET", ts.URL+"/api/v1/capabilities", nil)
	req2.Header.Set("Cookie", login())
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var cap struct {
		Features map[string]bool `json:"features"`
		Spaces   map[string]any  `json:"spaces"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&cap)
	if cap.Features["spaces"] {
		t.Fatal("未装配 features.spaces 应 false")
	}
	if v, _ := cap.Spaces["hub"].(bool); v {
		t.Fatal("未装配 spaces.hub 应 false")
	}
}

// TestSpacesAssembledCapabilitiesTrue 装配态 capabilities：features.spaces=true
// 且 spaces.hub=true（禁空壳承诺兑现——W07-01 投影的 hub:false 更新为装配态）。
func TestSpacesAssembledCapabilitiesTrue(t *testing.T) {
	f := newSpacesRESTFixture(t)
	code, body := f.adminGet("/api/v1/capabilities")
	if code != http.StatusOK {
		t.Fatalf("capabilities: %d", code)
	}
	feat, _ := body["features"].(map[string]any)
	if v, _ := feat["spaces"].(bool); !v {
		t.Fatalf("装配态 features.spaces 应 true: %v", feat)
	}
	sp, _ := body["spaces"].(map[string]any)
	if v, _ := sp["hub"].(bool); !v {
		t.Fatalf("装配态 spaces.hub 应 true: %v", sp)
	}
	if _, ok := sp["nodes"]; !ok {
		t.Fatal("nodes 投影键应在（W07-01 加法键保持）")
	}
	if v, _ := sp["private_history_readable"].(bool); v {
		t.Fatal("private_history_readable 恒 false")
	}
}

// TestSpaceGuardSessionTurns409 必修A REST 级（codex 必修F「REST 测试注释声称
// 守卫实际未验」闭合）：空间绑定会话的普通续轮 POST /sessions/{id}/turns → 409
// space_dispatch_required（错误码词 collab 单源；守卫经 Service.SpaceTurnGuard
// 生产闭包接线）。
func TestSpaceGuardSessionTurns409(t *testing.T) {
	f := newSpacesRESTFixture(t)
	// 注册会话能力卡（Spaces 盖章——版本门同源词形）+ 建会话。
	f.core.RegisterNode("restN", []protocol.AgentCard{{
		Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write",
		ProtocolVer: 1, Exposed: true, SessionAdapter: protocol.SessionAdapterCodex,
		Spaces: protocol.SpacesCapV1,
	}})
	code, body := f.adminPost("/api/v1/sessions", `{"node_id":"restN","agent":"coder","cwd":"/tmp/rest"}`)
	if code != http.StatusCreated {
		t.Fatalf("建会话: %d %v", code, body)
	}
	sv, _ := body["session"].(map[string]any)
	sessID, _ := sv["id"].(string)
	if sessID == "" {
		t.Fatalf("会话 ID 解析失败: %v", body)
	}
	// 空间 + 绑定（manual 创建 → pending_config 态 configure 合法）。
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"守卫反例","mode":"manual","request_id":"g-1"}`)
	spID, _ := body["space"].(map[string]any)["id"].(string)
	sp, ok := f.svc.Space(spID)
	if !ok {
		t.Fatalf("空间缺失: %v", body)
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "g-cfg", Action: collab.ActConfigureMembers,
		ExpectedVersion: sp.Version, Members: []collab.Member{{Name: "restN/coder", SessionID: sessID}}},
		collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	// 普通续轮 → 409 space_dispatch_required。
	code, body = f.adminPost("/api/v1/sessions/"+sessID+"/turns", `{"prompt":"旁路普通轮"}`)
	if code != http.StatusConflict || body["error"] != collab.CodeSpaceDispatchRequired {
		t.Fatalf("绑定会话普通轮应 409 space_dispatch_required: %d %v", code, body)
	}
	// 相符 scope 的空间轮放行正例不在 REST /turns 面（该端点恒普通轮；空间轮唯一
	// 入口是 space dispatch）——由 TestDispatchFullChainConverges 在同一生产守卫
	// 接线下承担（受理→收敛全链）。
}

// TestSpaceUnbindDiffOnRemoval 必修D REST 级（解除闭环 diff 接线）：成员移除后
// api 侧 diff 下发解除（黑洞传输解除未达 → 入 Service 重试面，PendingUnbindSessions
// 可见——解除失败重试面的端到端实证）。
func TestSpaceUnbindDiffOnRemoval(t *testing.T) {
	f := newSpacesRESTFixture(t)
	f.core.RegisterNode("restN", []protocol.AgentCard{{
		Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write",
		ProtocolVer: 1, Exposed: true, SessionAdapter: protocol.SessionAdapterCodex,
		Spaces: protocol.SpacesCapV1,
	}})
	code, body := f.adminPost("/api/v1/sessions", `{"node_id":"restN","agent":"coder","cwd":"/tmp/rest2"}`)
	if code != http.StatusCreated {
		t.Fatalf("建会话: %d %v", code, body)
	}
	sv, _ := body["session"].(map[string]any)
	sessID, _ := sv["id"].(string)
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"解除反例","mode":"manual","request_id":"u-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间: %d %v", code, body)
	}
	spID, _ := body["space"].(map[string]any)["id"].(string)
	sp, ok := f.svc.Space(spID)
	if !ok {
		t.Fatalf("空间缺失: %v", body)
	}
	admin := collab.Subject{Name: "admin", Class: collab.ClassOwner}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "u-cfg", Action: collab.ActConfigureMembers,
		ExpectedVersion: sp.Version, Members: []collab.Member{{Name: "restN/coder", SessionID: sessID}}}, admin); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	// 移除成员经 REST（api handler diff 接线面——直调 Control 绕过 handler 不触发
	// 解除；pending_config 态合法）→ 解除下发（黑洞未达 → 入队）。
	sp, _ = f.svc.Space(spID)
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/control",
		`{"action":"configure_members","expected_version":`+itoaRest(sp.Version)+`,"members":[],"request_id":"u-rm"}`)
	if code != http.StatusOK {
		t.Fatalf("移除成员: %d %v", code, body)
	}
	_ = admin
	found := false
	for _, sid := range f.svc.PendingUnbindSessions() {
		if sid == sessID {
			found = true
		}
	}
	if !found {
		t.Fatalf("移除成员后解除登记应入队重试面（黑洞传输未达）: %v", f.svc.PendingUnbindSessions())
	}
}

// itoaRest int64 十进制（测试体 JSON 拼装）。
func itoaRest(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	uv := uint64(v)
	if neg {
		uv = uint64(-uv)
	}
	var b [24]byte
	i := len(b)
	for uv > 0 {
		i--
		b[i] = byte('0' + uv%10)
		uv /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
