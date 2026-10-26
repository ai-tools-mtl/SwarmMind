// node 本地通知渠道管理面测试（standalone 补盲区，2026-10-24）：鉴权（仅
// local_token）、CRUD 全链、脱敏占位更新、inbox 类型准入拒绝、未装配 503、
// 发测试端到端（假 webhook 收包）。安全红线断言：明文口令不出读响应。
package localapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/notify"
)

// newNotifySrv 构造装配了通知 Dispatcher 的本地 API（独立临时 data_dir：渠道存储
// 与「同时通知」开关都落这里，绝不外溢）。
func newNotifySrv(t *testing.T) (*httptest.Server, *notify.Dispatcher) {
	t.Helper()
	disp := notify.NewDispatcher(mustChannelStore(t), nil)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", LocalToken: "local-tok", PrimeToken: "prime-tok",
		DataDir: t.TempDir(),
		Notify:  disp,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, disp
}

// mustChannelStore 临时目录渠道存储（测试辅助）。
func mustChannelStore(t *testing.T) *notify.ChannelStore {
	t.Helper()
	st, err := notify.OpenChannelStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// doJSON 便捷请求（JSON 体 + Bearer 令牌 → 应答解析）。
func reqJSON(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
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
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestNotifyAuthLocalOnly 通知面全部端点仅 local_token：无令牌 401、prime_token
// 亦 401（编排凭证不放行凭据配置，主人管理面口径）。
func TestNotifyAuthLocalOnly(t *testing.T) {
	srv, _ := newNotifySrv(t)
	base := srv.URL + "/api/v1/local/notify/channels"
	for _, tc := range []struct {
		name  string
		token string
	}{{"无令牌", ""}, {"prime_token", "prime-tok"}, {"错误令牌", "wrong"}} {
		code, _ := reqJSON(t, "GET", base, tc.token, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s: GET 应 401，got %d", tc.name, code)
		}
	}
	code, _ := reqJSON(t, "POST", base, "prime-tok", map[string]any{"type": "webhook", "format": "feishu", "name": "x", "secrets": map[string]string{"url": "https://example.com/hook"}})
	if code != http.StatusUnauthorized {
		t.Fatalf("prime_token 创建应 401，got %d", code)
	}
}

// TestNotifyChannelCRUDAndMask CRUD 全链 + 脱敏：创建 email 渠道（含明文口令）→
// 读响应口令 ******；列表同；更新传回占位保留原值（经 Store 明文核对）；删除 404 收敛。
func TestNotifyChannelCRUDAndMask(t *testing.T) {
	srv, disp := newNotifySrv(t)
	base := srv.URL + "/api/v1/local/notify/channels"

	// 创建：201，响应脱敏（password → ******，host/port 明文可核对）
	code, body := reqJSON(t, "POST", base, "local-tok", map[string]any{
		"type": "email", "name": "告警邮箱",
		"secrets": map[string]string{"host": "smtp.example.com", "port": "465", "username": "a@example.com", "password": "plain-pass", "from": "a@example.com", "to": "b@example.com"},
	})
	if code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatal("创建应答缺 id")
	}
	if got := body["secrets"].(map[string]any)["password"]; got != notify.MaskPlaceholder {
		t.Fatalf("创建响应口令应脱敏为 %s，got %v", notify.MaskPlaceholder, got)
	}

	// 列表：同样脱敏
	code, body = reqJSON(t, "GET", base, "local-tok", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", code)
	}
	chs := body["channels"].([]any)
	if len(chs) != 1 {
		t.Fatalf("列表应 1 条，got %d", len(chs))
	}
	if got := chs[0].(map[string]any)["secrets"].(map[string]any)["password"]; got != notify.MaskPlaceholder {
		t.Fatalf("列表口令应脱敏，got %v", got)
	}

	// 更新：占位回传=保留原值（GitHub 式语义）；明文经 Store 核对
	code, body = reqJSON(t, "PUT", base+"/"+id, "local-tok", map[string]any{
		"name": "告警邮箱2", "secrets": map[string]string{"password": notify.MaskPlaceholder},
	})
	if code != http.StatusOK {
		t.Fatalf("更新应 200，got %d: %v", code, body)
	}
	ch, ok := disp.Store().Get(id)
	if !ok {
		t.Fatal("渠道应存在")
	}
	if ch.Secrets["password"] != "plain-pass" {
		t.Fatalf("占位更新应保留原口令，got %q", ch.Secrets["password"])
	}
	if ch.Name != "告警邮箱2" {
		t.Fatalf("名称应更新，got %q", ch.Name)
	}

	// 更新未提供 secrets（启用开关部分更新）：口令不丢
	enabled := false
	code, _ = reqJSON(t, "PUT", base+"/"+id, "local-tok", map[string]any{"enabled": &enabled})
	if code != http.StatusOK {
		t.Fatalf("部分更新应 200，got %d", code)
	}
	if ch, _ = disp.Store().Get(id); ch.Secrets["password"] != "plain-pass" || ch.Enabled {
		t.Fatalf("部分更新应保留口令且停用，got %+v", ch)
	}

	// 删除：200 → 再删 404
	code, _ = reqJSON(t, "DELETE", base+"/"+id, "local-tok", nil)
	if code != http.StatusOK {
		t.Fatalf("删除应 200，got %d", code)
	}
	code, _ = reqJSON(t, "DELETE", base+"/"+id, "local-tok", nil)
	if code != http.StatusNotFound {
		t.Fatalf("重复删除应 404，got %d", code)
	}
}

// TestNotifyInboxTypeRejected node 侧 inbox 类型准入拒绝：创建与类型切换均 400
//（站内通知为 hub 控制台专属，node 不收永远发不出去的配置）。
func TestNotifyInboxTypeRejected(t *testing.T) {
	srv, disp := newNotifySrv(t)
	base := srv.URL + "/api/v1/local/notify/channels"
	code, body := reqJSON(t, "POST", base, "local-tok", map[string]any{
		"type": "inbox", "name": "站内", "secrets": map[string]string{},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("inbox 创建应 400，got %d: %v", code, body)
	}
	if body["error"] != "inbox_unsupported" {
		t.Fatalf("错误码应 inbox_unsupported，got %v", body["error"])
	}
	// 既有渠道切换到 inbox 同拒
	code, body = reqJSON(t, "POST", base, "local-tok", map[string]any{
		"type": "webhook", "format": "feishu", "name": "飞书", "secrets": map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/x"},
	})
	if code != http.StatusCreated {
		t.Fatalf("feishu 创建应 201，got %d", code)
	}
	id := body["id"].(string)
	code, _ = reqJSON(t, "PUT", base+"/"+id, "local-tok", map[string]any{
		"type": "inbox", "secrets": map[string]string{},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("切换到 inbox 应 400，got %d", code)
	}
	if ch, _ := disp.Store().Get(id); ch.Type != notify.TypeWebhook {
		t.Fatalf("被拒更新不得改类型，got %q", ch.Type)
	}
}

// TestNotifyUnavailable503 未装配 Dispatcher（旧构造方防御口径，2026-10-26 起
// 三模式全装配、正常构造恒就绪）：端点组 503 降级。
func TestNotifyUnavailable503(t *testing.T) {
	srv := httptest.NewServer(New(Config{NodeID: "nodeA", LocalToken: "local-tok"}).Handler())
	t.Cleanup(srv.Close)
	base := srv.URL + "/api/v1/local/notify/channels"
	code, body := reqJSON(t, "GET", base, "local-tok", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503，got %d", code)
	}
	if body["error"] != "notify_unavailable" {
		t.Fatalf("错误码应 notify_unavailable，got %v", body["error"])
	}
}

// TestNotifySettingsEndpoints 「同时通知」开关端点（2026-10-26 改动三）：GET
// 默认 true；PUT false 落盘即刻可读；PUT 缺 also_notify / 非布尔 400；与渠道
// 端点同走 local_token 鉴权。
func TestNotifySettingsEndpoints(t *testing.T) {
	srv, _ := newNotifySrv(t)
	base := srv.URL + "/api/v1/local/notify/settings"

	code, body := reqJSON(t, "GET", base, "local-tok", nil)
	if code != http.StatusOK {
		t.Fatalf("GET 应 200，got %d", code)
	}
	if v, ok := body["also_notify"].(bool); !ok || !v {
		t.Fatalf("未配置应默认 true，got %v", body)
	}
	// 写 false → 立即读回 false（读盘不缓存）
	code, body = reqJSON(t, "PUT", base, "local-tok", map[string]any{"also_notify": false})
	if code != http.StatusOK {
		t.Fatalf("PUT 应 200，got %d: %v", code, body)
	}
	if v, _ := body["also_notify"].(bool); v {
		t.Fatalf("PUT 应答应回显 false，got %v", body)
	}
	code, body = reqJSON(t, "GET", base, "local-tok", nil)
	if v, _ := body["also_notify"].(bool); code != http.StatusOK || v {
		t.Fatalf("PUT 后 GET 应读到 false，got %d %v", code, body)
	}
	// 校验：缺 also_notify / 非布尔 / 空请求体一律 400
	for name, payload := range map[string]any{
		"缺字段": map[string]any{},
		"非布尔": map[string]any{"also_notify": "yes"},
		"空体":  nil,
	} {
		code, _ := reqJSON(t, "PUT", base, "local-tok", payload)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，got %d", name, code)
		}
	}
	// 鉴权：prime_token 不放行（凭据管理面同口径）
	code, _ = reqJSON(t, "PUT", base, "prime-tok", map[string]any{"also_notify": true})
	if code != http.StatusUnauthorized {
		t.Fatalf("prime_token PUT 应 401，got %d", code)
	}
}

// TestNotifyChannelTestEndToEnd 发测试端到端：webhook 渠道指向假收端，POST test
// 200 {ok:true}，假收端收到一条 JSON POST；未知渠道 404。
func TestNotifyChannelTestEndToEnd(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)

	srv, _ := newNotifySrv(t)
	base := srv.URL + "/api/v1/local/notify/channels"
	code, body := reqJSON(t, "POST", base, "local-tok", map[string]any{
		"type": "webhook", "name": "外部钩子", "secrets": map[string]string{"url": hook.URL},
	})
	if code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %v", code, body)
	}
	id := body["id"].(string)

	code, body = reqJSON(t, "POST", base+"/"+id+"/test", "local-tok", nil)
	if code != http.StatusOK {
		t.Fatalf("发测试应 200，got %d", code)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("发测试应 ok=true，got %v", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("假 webhook 应恰收 1 次，got %d", hits)
	}

	code, _ = reqJSON(t, "POST", base+"/c-nonexistent/test", "local-tok", nil)
	if code != http.StatusNotFound {
		t.Fatalf("未知渠道发测试应 404，got %d", code)
	}
}
