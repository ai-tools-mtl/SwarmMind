package localapi

// spaces REST 最小面测试（W04-01，docs/tasks/v3/W04-01-rfc.md §六）：authLocal
// 门矩阵（无令牌/prime_token 401；local_token 放行）、CRUD+control 契约（201/200
// dup/400/404/409 投影）、未装配 503 诚实降级。模型层语义（状态机/CAS/租约/预算/
// 授权矩阵）由 internal/collab 单测机械覆盖，本文件只锁 HTTP 面。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

func newSpaceSrv(t *testing.T, withCollab bool) *httptest.Server {
	t.Helper()
	cfg := Config{
		NodeID:     "nodeA",
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  NewMcpAccessMemory(true),
	}
	if withCollab {
		st, err := collab.Open(t.TempDir(), collab.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		cfg.Collab = st
	}
	srv := httptest.NewServer(New(cfg).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func spaceDo(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSpacesAuthGateMatrix(t *testing.T) {
	srv := newSpaceSrv(t, true)
	// 无令牌 / 错令牌 → 401（全部空间端点）。
	for _, tok := range []string{"", "wrong"} {
		for _, spec := range [][2]string{
			{http.MethodPost, "/api/v1/spaces"},
			{http.MethodGet, "/api/v1/spaces"},
			{http.MethodGet, "/api/v1/spaces/sp-x"},
			{http.MethodPost, "/api/v1/spaces/sp-x/control"},
			{http.MethodPost, "/api/v1/spaces/sp-x/dispatches"},
			{http.MethodGet, "/api/v1/spaces/sp-x/dispatches/disp-x"},
			{http.MethodPost, "/api/v1/spaces/sp-x/messages"},
			{http.MethodGet, "/api/v1/spaces/sp-x/messages"},
		} {
			code, out := spaceDo(t, srv, tok, spec[0], spec[1], `{"goal":"g","mode":"manual"}`)
			if code != http.StatusUnauthorized {
				t.Fatalf("token=%q %s %s 应 401, got %d: %s", tok, spec[0], spec[1], code, out)
			}
		}
	}
	// W04-02 豁免⑯收窄（分层开线）：prime_token（编排凭据）——
	//   读面（列表/详情/派发查询/消息读）authRead 放行（与 agent 会话查询同界）；
	//   写面 authSpaceSubject 放行到模型层逐动作授权（主人专属动作 403 forbidden，
	//   非法动作参数照常 4xx）——不再是一律 401 的恒错门。
	// 未启用 mcpaccess 时写面 403 mcp_disabled：见 TestSpacesOrchestrateGate。
	code, out := spaceDo(t, srv, "tok-prime", http.MethodGet, "/api/v1/spaces", "")
	if code != http.StatusOK {
		t.Fatalf("prime_token 列表应 200（authRead）, got %d: %s", code, out)
	}
	if code, out := spaceDo(t, srv, "tok-prime", http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual"}`); code != http.StatusForbidden || !strings.Contains(out, "编排主体仅可创建 external") {
		t.Fatalf("prime_token 建 manual 应 403（模型层逐动作授权）, got %d: %s", code, out)
	}
	// 主人建一个空间供主体判定用（set_budget 的 403 需真实空间——存在性校验先于授权）。
	code, cout := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"c-mx","goal":"g","mode":"manual"}`)
	if code != http.StatusCreated {
		t.Fatalf("主人建空间应 201, got %d: %s", code, cout)
	}
	var created struct {
		Space collab.Space `json:"space"`
	}
	_ = json.Unmarshal([]byte(cout), &created)
	if code, out := spaceDo(t, srv, "tok-prime", http.MethodPost, "/api/v1/spaces/"+created.Space.ID+"/control",
		`{"request_id":"c-p1","action":"set_budget","expected_version":1,"budget":{}}`); code != http.StatusForbidden || !strings.Contains(out, collab.CodeForbidden) {
		t.Fatalf("prime_token set_budget 应 403（主人专属，豁免收窄保留面）, got %d: %s", code, out)
	}
	// local_token 放行到达业务层。
	code, out = spaceDo(t, srv, "tok-local", http.MethodGet, "/api/v1/spaces", "")
	if code != http.StatusOK {
		t.Fatalf("local_token 列表应 200, got %d: %s", code, out)
	}
}

func TestSpacesUnavailableDegraded(t *testing.T) {
	srv := newSpaceSrv(t, false)
	for _, spec := range [][2]string{
		{http.MethodPost, "/api/v1/spaces"},
		{http.MethodGet, "/api/v1/spaces"},
		{http.MethodPost, "/api/v1/spaces/sp-x/control"},
	} {
		code, out := spaceDo(t, srv, "tok-local", spec[0], spec[1], `{"goal":"g","mode":"manual"}`)
		if code != http.StatusServiceUnavailable || !strings.Contains(out, "spaces_unavailable") {
			t.Fatalf("未装配应 503 spaces_unavailable, got %d: %s", code, out)
		}
	}
	// 详情路径 503 优先于 404（装配守卫在路由解析后仍先行）。
	code, _ := spaceDo(t, srv, "tok-local", http.MethodGet, "/api/v1/spaces/sp-x", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("详情未装配应 503, got %d", code)
	}
}

func TestSpacesCRUDAndControlContract(t *testing.T) {
	srv := newSpaceSrv(t, true)

	// 创建：缺参 400；非法 mode 400。
	if code, _ := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces", `{"goal":""}`); code != http.StatusBadRequest {
		t.Fatalf("缺 goal 应 400")
	}
	if code, _ := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces", `{"goal":"g","mode":"magic"}`); code != http.StatusBadRequest {
		t.Fatalf("非法 mode 应 400")
	}
	if code, out := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces", `not-json`); code != http.StatusBadRequest {
		t.Fatalf("坏请求体应 400, got %d: %s", code, out)
	}

	// 创建 manual → 201；external 由编排语义——主人也可建（owner 任意方式）。
	code, out := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"c1","goal":"交付 v3","acceptance":"全绿","mode":"manual",
		  "members":[{"name":"echo","role":"writer","session_id":"sess-echo"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", code, out)
	}
	var created struct {
		Space collab.Space `json:"space"`
		Dup   bool         `json:"dup"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if created.Space.ID == "" || created.Space.Status != collab.StatusPendingConfig ||
		created.Space.Leader.Term != 1 || created.Space.Version != 1 {
		t.Fatalf("创建结果不符: %+v", created.Space)
	}
	// 同键同文 → 200+dup 原空间；同键异文 → 409 request_conflict。
	code, out = spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"c1","goal":"交付 v3","acceptance":"全绿","mode":"manual",
		  "members":[{"name":"echo","role":"writer","session_id":"sess-echo"}]}`)
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("重放应 200 dup, got %d: %s", code, out)
	}
	if code, out = spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"c1","goal":"换目标","mode":"manual"}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeRequestConflict) {
		t.Fatalf("同键异文应 409 request_conflict, got %d: %s", code, out)
	}

	// 列表与详情。
	code, out = spaceDo(t, srv, "tok-local", http.MethodGet, "/api/v1/spaces", "")
	if code != http.StatusOK || !strings.Contains(out, `"total":1`) {
		t.Fatalf("列表应 200 total=1, got %d: %s", code, out)
	}
	code, out = spaceDo(t, srv, "tok-local", http.MethodGet, "/api/v1/spaces/"+created.Space.ID, "")
	if code != http.StatusOK || !strings.Contains(out, "sess-echo") {
		t.Fatalf("详情应 200 含成员绑定, got %d: %s", code, out)
	}
	if code, out = spaceDo(t, srv, "tok-local", http.MethodGet, "/api/v1/spaces/sp-none", ""); code != http.StatusNotFound || !strings.Contains(out, collab.CodeNotFound) {
		t.Fatalf("未知空间应 404, got %d: %s", code, out)
	}

	// 控制骨架：ready（v1→2）→ start（v2→3）→ pause（v3→4）。
	ctrl := func(body string) (int, string) {
		return spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces/"+created.Space.ID+"/control", body)
	}
	if code, out = ctrl(`{"request_id":"rd","action":"ready","expected_version":1}`); code != http.StatusOK || !strings.Contains(out, `"status":"ready"`) {
		t.Fatalf("ready 应 200 ready, got %d: %s", code, out)
	}
	// 非法转换：ready 重复 → 409 illegal_transition。
	if code, out = ctrl(`{"request_id":"rd2","action":"ready","expected_version":2}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeIllegalTransition) {
		t.Fatalf("ready 重入应 409, got %d: %s", code, out)
	}
	if code, out = ctrl(`{"request_id":"st","action":"start","expected_version":2}`); code != http.StatusOK || !strings.Contains(out, `"status":"running"`) {
		t.Fatalf("start 应 200 running, got %d: %s", code, out)
	}
	// 版本冲突：stale expected_version → 409 version_conflict。
	if code, out = ctrl(`{"request_id":"ps0","action":"pause","expected_version":1}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeVersionConflict) {
		t.Fatalf("stale 版本应 409 version_conflict, got %d: %s", code, out)
	}
	// request_id 必填 → 400。
	if code, _ = ctrl(`{"action":"pause","expected_version":3}`); code != http.StatusBadRequest {
		t.Fatalf("缺 request_id 应 400, got %d", code)
	}
	if code, out = ctrl(`{"request_id":"ps1","action":"pause","expected_version":3,"note":"人工介入"}`); code != http.StatusOK || !strings.Contains(out, `"status":"paused"`) {
		t.Fatalf("pause 应 200 paused, got %d: %s", code, out)
	}
	// 控制重放：同 request_id 同文 → dup=true 原回执（不重复变更）。
	code, out = ctrl(`{"request_id":"ps1","action":"pause","expected_version":3,"note":"人工介入"}`)
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("控制重放应 dup=true, got %d: %s", code, out)
	}
	// 放弃定版（主人；原因必填）→ 归档。
	if code, out = ctrl(`{"request_id":"fz0","action":"finalize","expected_version":4,"final_outcome":"abandoned"}`); code != http.StatusBadRequest {
		t.Fatalf("放弃缺原因应 400, got %d: %s", code, out)
	}
	if code, out = ctrl(`{"request_id":"fz1","action":"finalize","expected_version":4,"final_outcome":"abandoned","reason":"目标取消"}`); code != http.StatusOK || !strings.Contains(out, `"status":"abandoned"`) {
		t.Fatalf("放弃应 200, got %d: %s", code, out)
	}
	if code, out = ctrl(`{"request_id":"ar1","action":"archive","expected_version":5}`); code != http.StatusOK || !strings.Contains(out, `"archived":true`) {
		t.Fatalf("归档应 200 archived, got %d: %s", code, out)
	}
	// 未知动作 → 400。
	if code, _ = ctrl(`{"request_id":"nx","action":"explode","expected_version":6}`); code != http.StatusBadRequest {
		t.Fatalf("未知动作应 400, got %d", code)
	}
}
