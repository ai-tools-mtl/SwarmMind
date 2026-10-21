// groups_test.go 群聊 REST 端点契约（GROUP-RFC 六 MVP）：鉴权门禁、201/200
// 状态码、增量拉取、终态/预算/8KiB 护栏的 HTTP 错误码映射、未接线降级 503。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newGroupFixture 在标准基座上追加群存储接线，返回带群端点的 server。
func newGroupFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := group.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开群 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token", Groups: gs}).Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// loginClient 登录后的 HTTP 客户端（fixture.client 同款；login=false 跳过）。
func (f *fixture) loginClient(login bool) *http.Client {
	f.t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	if login {
		resp, err := hc.Post(f.srv.URL+"/api/v1/login", "application/json",
			strings.NewReader(`{"token":"test-admin-token"}`))
		if err != nil {
			f.t.Fatalf("登录失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			f.t.Fatalf("登录应 200, got %d", resp.StatusCode)
		}
	}
	return hc
}

// TestGroupEndpointsAuthGate 未登录访问群端点一律 401（与既有管理面同门禁）。
func TestGroupEndpointsAuthGate(t *testing.T) {
	f := newGroupFixture(t)
	hc := f.loginClient(false)
	for _, tc := range [][2]string{
		{http.MethodPost, "/api/v1/groups"},
		{http.MethodGet, "/api/v1/groups"},
		{http.MethodGet, "/api/v1/groups/g-x/messages"},
		{http.MethodPost, "/api/v1/groups/g-x/messages"},
		{http.MethodPost, "/api/v1/groups/g-x/close"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d", tc[0], tc[1], code)
		}
	}
}

// TestGroupRESTFlow 覆盖全链：建群 201 → 发言 201 → 增量拉取 → 关闭 200 →
// 终态拒收 409 → 预算用尽 409 → 8KiB 超限 400 → 未知群 404。
func TestGroupRESTFlow(t *testing.T) {
	f := newGroupFixture(t)
	hc := f.loginClient(true)

	// 建群 201
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"对比双机磁盘报告","members":["a","b"]}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID     string `json:"group_id"`
		Status string `json:"status"`
		Host   string `json:"host"`
	}
	if err := json.Unmarshal(body, &g); err != nil || !strings.HasPrefix(g.ID, "g-") {
		t.Fatalf("201 应返回 g- 前缀群对象, got %s err=%v", body, err)
	}
	if g.Status != "discussing" || g.Host != "admin" {
		t.Fatalf("创建即 discussing/host=admin, got %+v", g)
	}

	// 空目标 400
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("空 goal 应 400, got %d", code)
	}

	// 发言 201（kind 缺省 user）
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"admin","body":"开始讨论"}`)
	if code != http.StatusCreated {
		t.Fatalf("发言应 201, got %d: %s", code, body)
	}
	var m struct {
		Seq  int64  `json:"seq"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Seq != 1 || m.Kind != "user" {
		t.Fatalf("首条消息 seq=1/kind=user, got %s err=%v", body, err)
	}
	// 第二条 agent
	if code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"我的报告"}`); code != http.StatusCreated {
		t.Fatalf("agent 发言应 201, got %d: %s", code, body)
	}

	// 增量拉取：since=1 只剩 seq=2
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=1", "")
	if code != http.StatusOK {
		t.Fatalf("拉消息应 200, got %d", code)
	}
	var lm struct {
		Messages []struct {
			Seq int64 `json:"seq"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &lm); err != nil || len(lm.Messages) != 1 || lm.Messages[0].Seq != 2 {
		t.Fatalf("since=1 应只返回 seq=2, got %s err=%v", body, err)
	}
	// 非法 since 400
	if code, _ = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=abc", ""); code != http.StatusBadRequest {
		t.Fatalf("since=abc 应 400, got %d", code)
	}

	// 群列表 200
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups", "")
	if code != http.StatusOK || !strings.Contains(string(body), g.ID) {
		t.Fatalf("群列表应 200 且含建群, got %d: %s", code, body)
	}

	// 8KiB 超限 400 body_too_large
	big := `{"from":"admin","body":"` + strings.Repeat("a", 8<<10+1) + `"}`
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", big)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "body_too_large") {
		t.Fatalf("8KiB+1 应 400 body_too_large, got %d: %s", code, body)
	}

	// 预算用尽 409 budget_exhausted（已发 1 条 agent，补到 20 后第 21 条拒）
	for i := 2; i <= 20; i++ {
		if code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"r"}`); code != http.StatusCreated {
			t.Fatalf("第 %d 条 agent 应放行, got %d: %s", i, code, body)
		}
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"a","kind":"agent","body":"over"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "budget_exhausted") {
		t.Fatalf("超预算应 409 budget_exhausted, got %d: %s", code, body)
	}

	// 关闭 200 → 幂等 200 → 终态拒收 409 group_concluded → 未知群 404
	for i := 0; i < 2; i++ {
		if code, _ = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/close", ""); code != http.StatusOK {
			t.Fatalf("关闭应 200（幂等）, got %d", code)
		}
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", `{"from":"admin","body":"late"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "group_concluded") {
		t.Fatalf("concluded 后发言应 409, got %d: %s", code, body)
	}
	if code, _ = f.do(hc, http.MethodPost, "/api/v1/groups/g-none/close", ""); code != http.StatusNotFound {
		t.Fatalf("未知群关闭应 404, got %d", code)
	}
	if code, body = f.do(hc, http.MethodGet, "/api/v1/groups/g-none/messages", ""); code != http.StatusNotFound || !strings.Contains(string(body), "group_not_found") {
		t.Fatalf("未知群拉消息应 404 group_not_found, got %d: %s", code, body)
	}
}

// TestGroupEndpointsUnavailable Groups 未接线（Config.Groups=nil）时群端点
// 统一 503 降级，既有构造方不受影响。
func TestGroupEndpointsUnavailable(t *testing.T) {
	f := newFixture(t) // 标准基座：无 Groups
	hc := f.loginClient(true)
	if code, body := f.do(hc, http.MethodGet, "/api/v1/groups", ""); code != http.StatusServiceUnavailable || !strings.Contains(string(body), "group_unavailable") {
		t.Fatalf("未接线应 503 group_unavailable, got %d: %s", code, body)
	}
}
