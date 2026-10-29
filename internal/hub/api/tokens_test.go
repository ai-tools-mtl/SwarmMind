package api

// PAT 令牌管理单测（2026-10-21 验收反馈 #7）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 创建 → 明文可用（Bearer 鉴权）→ 撤销后立即失效；全程只存哈希。
func TestTokenLifecycle(t *testing.T) {
	// 用 admin_token 直接建会话（实际产品中 admin 走登录页拿 cookie）；
	// 此处验证核心生命周期，auth 层用同一套 cookie。
	sv := New(Config{AdminToken: "admin-tok", DataDir: t.TempDir()})
	sid, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	sv.mu.Lock()
	sv.sessions[sid] = time.Now().UTC().Add(sessionTTL)
	sv.mu.Unlock()

	mk := func(method, path, body string, bearer string) *http.Response {
		var rd *strings.Reader
		if body != "" {
			rd = strings.NewReader(body)
		} else {
			rd = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, rd)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		// 直调 handler 时 ServeMux 未路由：手动注入路径参数
		if strings.Contains(path, "/auth/tokens/") {
			req.SetPathValue("id", strings.TrimPrefix(path, "/api/v1/auth/tokens/"))
		}
		w := httptest.NewRecorder()
		switch {
		case method == "POST" && strings.HasSuffix(path, "/auth/tokens"):
			sv.handleTokenCreate(w, req)
		case method == "GET" && strings.HasSuffix(path, "/auth/tokens"):
			sv.handleTokenList(w, req)
		case method == "DELETE":
			sv.handleTokenRevoke(w, req)
		}
		return w.Result()
	}

	// 1) 创建
	res := mk("POST", "/api/v1/auth/tokens", `{"name":"运维本机"}`, "")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("创建应 201, got %d", res.StatusCode)
	}
	var created struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Prefix string `json:"prefix"`
	}
	_ = json.NewDecoder(res.Body).Decode(&created)
	if created.ID == "" || created.Token == "" {
		t.Fatal("创建响应应含 id 与 token")
	}
	if !strings.HasPrefix(created.Token, "smsw_") {
		t.Fatalf("明文应 smsw_ 前缀, got %q", created.Token[:min(10, len(created.Token))])
	}

	// 2) Bearer 鉴权命中（返回令牌名作服务端验证主体）
	if name, ok := sv.tokens.verify(created.Token); !ok || name != "运维本机" {
		t.Fatalf("明文应通过校验且返回令牌名: name=%q ok=%v", name, ok)
	}
	// 错误明文拒绝
	if _, ok := sv.tokens.verify(created.Token + "x"); ok {
		t.Fatal("篡改明文不应通过")
	}

	// 3) 列表脱敏（不含哈希/明文）
	res = mk("GET", "/api/v1/auth/tokens", "", "")
	var list struct {
		Tokens []map[string]any `json:"tokens"`
	}
	_ = json.NewDecoder(res.Body).Decode(&list)
	if len(list.Tokens) != 1 {
		t.Fatalf("列表应 1 条, got %d", len(list.Tokens))
	}
	if h, _ := list.Tokens[0]["hash"].(string); h != "" {
		t.Fatal("列表不得泄漏哈希")
	}

	// 4) 持久化文件存在且为 0600
	b, err := os.Stat(filepath.Join(sv.cfg.DataDir, "api_tokens.json"))
	if err != nil {
		t.Fatalf("持久化文件应存在: %v", err)
	}
	if b.Mode().Perm() != 0o600 {
		t.Fatalf("持久化文件应 0600, got %v", b.Mode().Perm())
	}

	// 5) 撤销 → 立即失效
	res = mk("DELETE", "/api/v1/auth/tokens/"+created.ID, "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("撤销应 200, got %d", res.StatusCode)
	}
	if _, ok := sv.tokens.verify(created.Token); ok {
		t.Fatal("撤销后明文不应通过校验")
	}
	// 重复撤销 → 404
	if res = mk("DELETE", "/api/v1/auth/tokens/"+created.ID, "", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("重复撤销应 404, got %d", res.StatusCode)
	}
}

// 重启恢复：从磁盘加载后令牌仍可用。
func TestTokenPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{AdminToken: "a", DataDir: dir})
	tok, plain, err := s1.tokens.create("重启测试", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID == "" {
		t.Fatal("应生成 ID")
	}
	s2 := New(Config{AdminToken: "a", DataDir: dir}) // 模拟重启：新实例同 DataDir
	if name, ok := s2.tokens.verify(plain); !ok || name != "重启测试" {
		t.Fatalf("重启后明文令牌应仍可用且主体为令牌名: name=%q ok=%v", name, ok)
	}
}

// purge 硬删除（2026-10-29 部署后走查反馈五）：未撤销 409 拒绝、已撤销 200 删除、
// 不存在 404、重启（同 DataDir 新实例）不复活。
func TestTokenPurge(t *testing.T) {
	sv := New(Config{AdminToken: "admin-tok", DataDir: t.TempDir()})
	tokActive, _, err := sv.tokens.create("未撤销", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tokRevoked, _, err := sv.tokens.create("已撤销", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !sv.tokens.revoke(tokRevoked.ID) {
		t.Fatal("撤销应成功")
	}

	purge := func(id string) *http.Response {
		req := httptest.NewRequest("POST", "/api/v1/auth/tokens/"+id+"/purge", nil)
		// 直调 handler 时 ServeMux 未路由：手动注入路径参数（同 TestTokenLifecycle）
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		sv.handleTokenPurge(w, req)
		return w.Result()
	}

	// 1) 未撤销 → 409 拒绝（先撤销再删除），条目保留
	if res := purge(tokActive.ID); res.StatusCode != http.StatusConflict {
		t.Fatalf("未撤销 purge 应 409, got %d", res.StatusCode)
	}
	if got := len(sv.tokens.listSafe()); got != 2 {
		t.Fatalf("未撤销拒绝后列表应仍 2 条, got %d", got)
	}

	// 2) 已撤销 → 200 删除，列表不再包含
	if res := purge(tokRevoked.ID); res.StatusCode != http.StatusOK {
		t.Fatalf("已撤销 purge 应 200, got %d", res.StatusCode)
	}
	for _, t2 := range sv.tokens.listSafe() {
		if t2.ID == tokRevoked.ID {
			t.Fatal("purge 后列表不应再含已删条目")
		}
	}

	// 3) 不存在的 ID → 404
	if res := purge("tk-absent"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在 ID purge 应 404, got %d", res.StatusCode)
	}

	// 4) 重启（同 DataDir 新实例）不复活
	sv2 := New(Config{AdminToken: "a", DataDir: sv.cfg.DataDir})
	for _, t2 := range sv2.tokens.listSafe() {
		if t2.ID == tokRevoked.ID {
			t.Fatal("重启后已 purge 条目不应复活")
		}
	}
	if got := len(sv2.tokens.listSafe()); got != 1 {
		t.Fatalf("重启后应剩 1 条（未撤销那条）, got %d", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestTokenPurgePersistFailureKeepsList P2-02（2026-10-29 走查反馈批）：purge
// 落盘失败（tmp 不可写）时内存不得删、盘上旧记录原样——旧实现先 splice 后
// persist，写失败后内存已空而盘上犹存，重启记录复活（探针实证 memory=0/disk=1）。
// 修法后：独立 next 列表先落盘、成功才替换内存。
func TestTokenPurgePersistFailureKeepsList(t *testing.T) {
	d := t.TempDir()
	ts := newTokenStore(d)
	tok, _, err := ts.create("victim", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !ts.revoke(tok.ID) {
		t.Fatal("撤销应成功")
	}
	// tmp 写失败构造：tmp 路径换成目录（WriteFile 对目录 EISDIR）
	if err := os.Mkdir(ts.path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(ts.path + ".tmp") })

	found, active, err := ts.purge(tok.ID)
	if err == nil || !found || active {
		t.Fatalf("落盘失败应 found=true active=false err!=nil, got found=%v active=%v err=%v", found, active, err)
	}
	if got := len(ts.listSafe()); got != 1 {
		t.Fatalf("落盘失败内存列表应原样保留 1 条, got %d", got)
	}
	// 重载一致性：同 DataDir 新 store 读盘仍见该记录（写失败=盘未动）
	if got := len(newTokenStore(d).listSafe()); got != 1 {
		t.Fatalf("盘上应仍保留 1 条, got %d", got)
	}
	// 恢复构造后重试同一 purge 收敛成功（列表/盘一致=0 条）
	if err := os.Remove(ts.path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	found, active, err = ts.purge(tok.ID)
	if err != nil || !found || active {
		t.Fatalf("恢复后 purge 应成功, got found=%v active=%v err=%v", found, active, err)
	}
	if got := len(ts.listSafe()); got != 0 {
		t.Fatalf("成功 purge 后内存应为空, got %d", got)
	}
	if got := len(newTokenStore(d).listSafe()); got != 0 {
		t.Fatalf("成功 purge 后盘上应为空, got %d", got)
	}
}

// TestTokenPurgeRenameFailureKeepsList P2-02 rename 失败分支：tmp 写成功、
// rename（tmp→状态文件）失败（状态文件路径是目录，EISDIR）——内存同样不得删，
// 且失败后重试仍能找到该条目（无半删态）。
func TestTokenPurgeRenameFailureKeepsList(t *testing.T) {
	d := t.TempDir()
	ts := newTokenStore(d)
	tok, _, err := ts.create("victim", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !ts.revoke(tok.ID) {
		t.Fatal("撤销应成功")
	}
	// rename 失败构造：先落盘成功 create/revoke 后，把状态文件换成目录
	if err := os.Remove(ts.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ts.path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ts.path) })

	found, active, err := ts.purge(tok.ID)
	if err == nil || !found || active {
		t.Fatalf("rename 失败应返回错误, got found=%v active=%v err=%v", found, active, err)
	}
	if got := len(ts.listSafe()); got != 1 {
		t.Fatalf("rename 失败内存列表应原样保留 1 条, got %d", got)
	}
	found2, _, err2 := ts.purge(tok.ID)
	if !found2 || err2 == nil {
		t.Fatalf("失败后重试应仍找到该条目并再次失败, got found=%v err=%v", found2, err2)
	}
}

// TestTokenRoutesAdminSessionOnly P1-03（2026-10-29 走查反馈批）：令牌管理四条
// 路由（创建/列表/撤销/purge）admin 会话专属——有效 PAT 一律 403（身份成立、
// 授权不成立）、匿名一律 401、admin 会话全链可用。经真实 Handler 路由打中间件，
// 锁的是门禁层而非 handler 本体（对应 codex 探针 TestCodexPATPurge 场景转正）。
func TestTokenRoutesAdminSessionOnly(t *testing.T) {
	sv := New(Config{AdminToken: "admin-tok", DataDir: t.TempDir()})
	srv := httptest.NewServer(sv.Handler())
	t.Cleanup(srv.Close)

	// admin 会话（真实登录链拿 cookie，铸 PAT→撤销→purge 全链 2xx）
	adminJar, _ := cookiejar.New(nil)
	admin := &http.Client{Jar: adminJar}
	lresp, err := admin.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"admin-tok"}`))
	if err != nil || lresp.StatusCode != http.StatusOK {
		lresp.Body.Close()
		t.Fatalf("admin 登录应 200, got %v %v", err, lresp.StatusCode)
	}
	lresp.Body.Close()
	do := func(cl *http.Client, method, path, body string, hdr http.Header) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = hdr
		resp, err := cl.Do(req) // 传入的客户端决定凭据形态（admin 带 jar / anon 净）

		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	adminHdr := http.Header{}
	code, out := do(admin, "POST", "/api/v1/auth/tokens", `{"name":"probe-script"}`, adminHdr)
	if code != http.StatusCreated {
		t.Fatalf("admin 会话铸 PAT 应 201, got %d: %s", code, out)
	}
	var pat struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &pat); err != nil || pat.Token == "" {
		t.Fatalf("PAT 响应解析: %v %s", err, out)
	}
	if code, _ = do(admin, "GET", "/api/v1/auth/tokens", "", adminHdr); code != http.StatusOK {
		t.Fatalf("admin 会话列表应 200, got %d", code)
	}

	// PAT 与匿名矩阵用净客户端（无 cookie）：PAT=纯 Bearer 头、匿名=无任何凭据
	anon := &http.Client{}
	// PAT 面对四条路由：一律 403（存活 PAT 亦不放行管理组；403=身份成立、授权不成立）
	patHdr := http.Header{"Authorization": {"Bearer " + pat.Token}}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/auth/tokens", `{"name":"should-not-exist"}`},
		{"GET", "/api/v1/auth/tokens", ""},
		{"DELETE", "/api/v1/auth/tokens/" + pat.ID, ""},
		{"POST", "/api/v1/auth/tokens/" + pat.ID + "/purge", ""},
	} {
		if code, out = do(anon, tc.method, tc.path, tc.body, patHdr); code != http.StatusForbidden {
			t.Fatalf("PAT %s %s 应 403, got %d: %s", tc.method, tc.path, code, out)
		}
	}

	// 匿名面对四条路由：一律 401
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/tokens"},
		{"GET", "/api/v1/auth/tokens"},
		{"DELETE", "/api/v1/auth/tokens/" + pat.ID},
		{"POST", "/api/v1/auth/tokens/" + pat.ID + "/purge"},
	} {
		if code, out = do(anon, tc.method, tc.path, "", http.Header{}); code != http.StatusUnauthorized {
			t.Fatalf("匿名 %s %s 应 401, got %d: %s", tc.method, tc.path, code, out)
		}
	}

	// PAT 被拒（403）与被 401 后账本无损：令牌组仍只有 probe-script 一条
	code, out = do(admin, "GET", "/api/v1/auth/tokens", "", adminHdr)
	if code != http.StatusOK || strings.Count(out, `"id"`) != 1 {
		t.Fatalf("被拒后令牌组应原样 1 条, got %d: %s", code, out)
	}

	// admin 会话可用性收尾：victim 铸→撤→purge 全链 2xx（门禁不误伤合法主人）
	code, out = do(admin, "POST", "/api/v1/auth/tokens", `{"name":"victim"}`, adminHdr)
	if code != http.StatusCreated {
		t.Fatalf("victim 铸造应 201, got %d: %s", code, out)
	}
	var victim struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &victim)
	if code, _ = do(admin, "DELETE", "/api/v1/auth/tokens/"+victim.ID, "", adminHdr); code != http.StatusOK {
		t.Fatalf("admin 撤销应 200, got %d", code)
	}
	if code, _ = do(admin, "POST", "/api/v1/auth/tokens/"+victim.ID+"/purge", "", adminHdr); code != http.StatusOK {
		t.Fatalf("admin purge 应 200, got %d", code)
	}
	if code, out = do(admin, "GET", "/api/v1/auth/tokens", "", adminHdr); code != http.StatusOK || strings.Count(out, `"id"`) != 1 {
		t.Fatalf("purge 后应剩 probe-script 一条, got %d: %s", code, out)
	}
}
