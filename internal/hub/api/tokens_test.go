package api

// PAT 令牌管理单测（2026-10-21 验收反馈 #7）。

import (
	"encoding/json"
	"net/http"
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
