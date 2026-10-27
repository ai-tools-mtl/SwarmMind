package api

// 管理员账户单测（2026-10-21 验收反馈：密码登录体系）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

func TestAccountLifecycle(t *testing.T) {
	dir := t.TempDir()
	as := newAccountStore(dir)
	now := time.Now()

	if as.initialized() {
		t.Fatal("新 store 不应已初始化")
	}

	// setup
	if err := as.setup("admin", "password123", now); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !as.initialized() {
		t.Fatal("setup 后应已初始化")
	}
	// 弱密码/非法用户名
	if err := as.setup("x", "password123", now); err == nil {
		t.Fatal("已初始化后重复 setup 应拒绝")
	}

	// 登录：正确/错误
	if !as.loginPassword("admin", "password123") {
		t.Fatal("正确密码应通过")
	}
	if as.loginPassword("admin", "wrong-pass") {
		t.Fatal("错误密码应拒绝")
	}
	if as.loginPassword("Admin", "password123") {
		t.Fatal("用户名大小写敏感")
	}

	// 改用户名（需密码）
	if err := as.updateUsername("password123", "lun", now.Add(time.Minute)); err != nil {
		t.Fatalf("updateUsername: %v", err)
	}
	if as.loginPassword("admin", "password123") {
		t.Fatal("旧用户名应失效")
	}
	if !as.loginPassword("lun", "password123") {
		t.Fatal("新用户名应可登录")
	}
	// 错误密码改用户名应拒绝
	if err := as.updateUsername("wrong", "lun2", now); err == nil {
		t.Fatal("密码错误不应允许改用户名")
	}

	// 改密码
	if err := as.changePassword("password123", "newpass456", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("changePassword: %v", err)
	}
	if as.loginPassword("lun", "password123") {
		t.Fatal("旧密码应失效")
	}
	if !as.loginPassword("lun", "newpass456") {
		t.Fatal("新密码应可登录")
	}
	// 相同密码拒绝
	if err := as.changePassword("newpass456", "newpass456", now); err == nil {
		t.Fatal("新旧密码相同应拒绝")
	}

	// root 重置（恢复通道）
	if err := as.resetPasswordRoot("admin", "resetpass789", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("resetPasswordRoot: %v", err)
	}
	if !as.loginPassword("admin", "resetpass789") {
		t.Fatal("root 重置后新凭据应可登录")
	}

	// 持久化：同目录新实例（模拟重启）后仍可登录
	as2 := newAccountStore(dir)
	if !as2.loginPassword("admin", "resetpass789") {
		t.Fatal("重启后账户应保留")
	}
}

// newAuthFixture 带数据目录与版本注入的管理面测试基座（账户/密码登录与
// server_version 下发链路用；其余接线与 server_test.go newFixture 同源）。
func newAuthFixture(t *testing.T, version string) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	srv := httptest.NewServer(New(Config{
		Core: c, AdminToken: "test-admin-token", DataDir: t.TempDir(), Version: version,
	}).Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// TestAuthAccountServerVersionLock server_version 下发锁（codex 审查建议修，
// TASK-maturity-version-var 回归）：版本号只随认证态下发、且值与注入一致——
//   - 未认证（无/伪会话 cookie）GET /api/v1/auth/account 响应不得含 server_version
//     （版本号不对未认证方披露）；
//   - token 登录与密码登录两条路径的成功响应均含 server_version == 注入值；
//   - 登录态账户视图（控制台页脚数据源）含 server_version == 注入值。
func TestAuthAccountServerVersionLock(t *testing.T) {
	const ver = "mat-test-001"
	f := newAuthFixture(t, ver)

	// ① 未认证：无 cookie → 仅 initialized，无 server_version
	hc := f.client(false)
	code, out := f.do(hc, http.MethodGet, "/api/v1/auth/account", "")
	if code != http.StatusOK {
		t.Fatalf("账户状态应 200, got %d: %s", code, out)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("账户状态体应为 JSON: %s", out)
	}
	if _, ok := body["server_version"]; ok {
		t.Fatalf("未认证响应不得含 server_version: %s", out)
	}

	// ② 伪会话 cookie：同样不得披露版本（有 cookie 但会话无效须走未认证分支）
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/auth/account", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", sessionCookie+"=bogus-session-id")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBuf := &bytes.Buffer{}
	_, _ = bodyBuf.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("伪 cookie 应 200 未认证分支, got %d", resp.StatusCode)
	}
	body = nil
	if err := json.Unmarshal(bodyBuf.Bytes(), &body); err != nil {
		t.Fatalf("伪 cookie 响应体应为 JSON: %s", bodyBuf.String())
	}
	if _, ok := body["server_version"]; ok {
		t.Fatalf("伪 cookie 响应不得含 server_version: %s", bodyBuf.String())
	}

	// ③ token 登录路径：响应含 server_version 且与注入一致（cookie 入 jar）
	code, out = f.do(hc, http.MethodPost, "/api/v1/login", `{"token":"test-admin-token"}`)
	if code != http.StatusOK {
		t.Fatalf("token 登录应 200, got %d: %s", code, out)
	}
	var login struct {
		OK            bool   `json:"ok"`
		ServerVersion string `json:"server_version"`
	}
	if err := json.Unmarshal(out, &login); err != nil || !login.OK {
		t.Fatalf("token 登录体不符: %s", out)
	}
	if login.ServerVersion != ver {
		t.Fatalf("token 登录 server_version 应为注入值 %q, got %q", ver, login.ServerVersion)
	}

	// ④ 登录态账户视图：含 server_version 且一致（控制台页脚/会话恢复数据源）
	code, out = f.do(hc, http.MethodGet, "/api/v1/auth/account", "")
	if code != http.StatusOK {
		t.Fatalf("登录态账户状态应 200, got %d: %s", code, out)
	}
	body = nil
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("账户状态体应为 JSON: %s", out)
	}
	if body["server_version"] != ver {
		t.Fatalf("登录态 server_version 应为注入值 %q, got %v", ver, body["server_version"])
	}

	// ⑤ 密码登录路径：setup + login-password 响应含 server_version 且一致
	hc2 := f.client(false)
	code, out = f.do(hc2, http.MethodPost, "/api/v1/auth/setup", `{"username":"lun","password":"password123"}`)
	if code != http.StatusCreated {
		t.Fatalf("账户初始化应 201, got %d: %s", code, out)
	}
	code, out = f.do(hc2, http.MethodPost, "/api/v1/auth/login-password", `{"username":"lun","password":"password123"}`)
	if code != http.StatusOK {
		t.Fatalf("密码登录应 200, got %d: %s", code, out)
	}
	if err := json.Unmarshal(out, &login); err != nil || !login.OK {
		t.Fatalf("密码登录体不符: %s", out)
	}
	if login.ServerVersion != ver {
		t.Fatalf("密码登录 server_version 应为注入值 %q, got %q", ver, login.ServerVersion)
	}
	// 密码登录会话的账户视图：完整信息 + server_version 一致
	code, out = f.do(hc2, http.MethodGet, "/api/v1/auth/account", "")
	if code != http.StatusOK {
		t.Fatalf("密码会话账户状态应 200, got %d: %s", code, out)
	}
	var acct struct {
		Initialized   bool   `json:"initialized"`
		Username      string `json:"username"`
		ServerVersion string `json:"server_version"`
	}
	if err := json.Unmarshal(out, &acct); err != nil {
		t.Fatalf("账户视图体不符: %s", out)
	}
	if !acct.Initialized || acct.Username != "lun" {
		t.Fatalf("密码会话账户视图应含用户名: %s", out)
	}
	if acct.ServerVersion != ver {
		t.Fatalf("密码会话 server_version 应为注入值 %q, got %q", ver, acct.ServerVersion)
	}
}

// PBKDF2 RFC 6070 风格自检（SHA-256 参考向量）。
func TestPBKDF2Vector(t *testing.T) {
	// PBKDF2-HMAC-SHA256("password", "salt", 1, 32) 已知值前 8 字节 120fb6cffcf8b32c
	got := pbkdf2([]byte("password"), []byte("salt"), 1, 32)
	wantPrefix := []byte{0x12, 0x0f, 0xb6, 0xcf, 0xfc, 0xf8, 0xb3, 0x2c}
	for i, w := range wantPrefix {
		if got[i] != w {
			t.Fatalf("PBKDF2 向量不匹配 byte[%d]=%02x want %02x", i, got[i], w)
		}
	}
	// 迭代次数变化 → 输出变化
	if string(pbkdf2([]byte("password"), []byte("salt"), 2, 32)) == string(got) {
		t.Fatal("不同迭代次数不应产生相同输出")
	}
}
