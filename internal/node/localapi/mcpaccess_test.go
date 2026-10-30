package localapi

// MCP 编排授权（W02-01，docs/tasks/v3/W02-01-rfc.md）单测：授权矩阵（主人恒
// 放行 / prime_token 需显式 enabled）、轮换一次性明文与失败不虚报、迁移判定表
// 逐行、declare 去换发回归、状态文件原子性与 0600。全部走 fake/临时目录，绝无
// 真实模型与网络；测试 token 全为假值（纪律：真 token 不进代码库）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newAccessSrv 授权矩阵测试环境：echo 桩执行器 + 指定授权开关（nil=未装配，
// 门按缺省禁用判定）。返回 server 与「以给定 token 提交本机任务」的闭包。
func newAccessSrv(t *testing.T, access *McpAccess, rotate func() (string, error)) (*httptest.Server, func(tok string) (int, string)) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		LocalToken: "tok-local", PrimeToken: "tok-prime",
		McpAccess:   access,
		RotatePrime: rotate,
	}).Handler())
	t.Cleanup(srv.Close)
	submit := func(tok string) (int, string) {
		return doJSON(t, srv, tok, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}
	return srv, submit
}

// doJSONHdr 同 doJSON，并回传响应头（Cache-Control 等传输面契约断言用）。
func doJSONHdr(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string, http.Header) {
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
	return resp.StatusCode, string(b), resp.Header
}

// requireNoStore 断言响应携带 Cache-Control: no-store（授权/凭据面传输纪律，
// RFC §三「响应统一 no-store」——此前测试名声称覆盖但未查头，终审建议⑥补断言）。
func requireNoStore(t *testing.T, h http.Header, where string) {
	t.Helper()
	if cc := h.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("%s 应带 Cache-Control: no-store, got %q", where, cc)
	}
}

// TestMcpAccessAuthorizationMatrix 授权矩阵（RFC §4.1 判定表）：
//   - 未启用（含未装配）：prime_token 编排 403 mcp_disabled（人话指路主人）；
//   - standalone（无 prime_agent）主人先显式启用 → prime_token 编排成功（调用
//     权与管家角色解耦的核心场景）；
//   - 禁用 → prime 403 mcp_disabled，local_token（主人）编排仍成功；
//   - 主人恒放行：disabled/未装配下 local_token 均直落业务校验（202）。
func TestMcpAccessAuthorizationMatrix(t *testing.T) {
	const disabledHint = "编排授权未启用"

	t.Run("未装配_默认禁用", func(t *testing.T) {
		_, submit := newAccessSrv(t, nil, nil)
		code, out := submit("tok-prime")
		if code != http.StatusForbidden || !strings.Contains(out, "mcp_disabled") || !strings.Contains(out, disabledHint) {
			t.Fatalf("未装配默认禁用：prime 编排应 403 mcp_disabled, got %d: %s", code, out)
		}
		if code, out := submit("tok-local"); code != http.StatusAccepted {
			t.Fatalf("主人恒放行：local_token 编排应 202, got %d: %s", code, out)
		}
	})

	t.Run("内存禁用态_主人启用后_prime放行", func(t *testing.T) {
		acc := NewMcpAccessMemory(false)
		srv, submit := newAccessSrv(t, acc, nil)
		// 前置：禁用下 prime 403（主人 202 不受影响）
		if code, out := submit("tok-prime"); code != http.StatusForbidden || !strings.Contains(out, "mcp_disabled") {
			t.Fatalf("禁用下 prime 编排应 403 mcp_disabled, got %d: %s", code, out)
		}
		if code, _ := submit("tok-local"); code != http.StatusAccepted {
			t.Fatalf("禁用不拦主人：local_token 编排应 202")
		}
		// 主人先启用（standalone 无 prime_agent 同样可启用——解耦核心）
		code, out := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":true}`)
		if code != http.StatusOK || !strings.Contains(out, `"enabled":true`) {
			t.Fatalf("PUT 启用应 200 且回显 enabled=true, got %d: %s", code, out)
		}
		if code, out := submit("tok-prime"); code != http.StatusAccepted {
			t.Fatalf("启用后 prime 编排应 202, got %d: %s", code, out)
		}
	})

	t.Run("再禁用_prime拒_local仍放行", func(t *testing.T) {
		acc := NewMcpAccessMemory(true)
		srv, submit := newAccessSrv(t, acc, nil)
		if code, _ := submit("tok-prime"); code != http.StatusAccepted {
			t.Fatalf("前置启用态 prime 应 202")
		}
		if code, out := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":false}`); code != http.StatusOK {
			t.Fatalf("PUT 禁用应 200, got %d: %s", code, out)
		}
		code, out := submit("tok-prime")
		if code != http.StatusForbidden || !strings.Contains(out, "mcp_disabled") {
			t.Fatalf("禁用后 prime 编排应 403 mcp_disabled, got %d: %s", code, out)
		}
		if code, _ := submit("tok-local"); code != http.StatusAccepted {
			t.Fatalf("禁用后 local_token 编排仍应 202（主人恒放行）")
		}
	})

	t.Run("管理端点鉴权_prime不可触达", func(t *testing.T) {
		srv, _ := newAccessSrv(t, NewMcpAccessMemory(false), nil)
		for _, tc := range []struct {
			name, method, path, body string
		}{
			{"get", http.MethodGet, "/api/v1/local/mcp/access", ""},
			{"put", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":true}`},
			{"rotate", http.MethodPost, "/api/v1/local/mcp/access/rotate", ""},
			{"rotate旧别名", http.MethodPost, "/api/v1/local/prime/rotate", ""},
		} {
			if code, out := doJSON(t, srv, "tok-prime", tc.method, tc.path, tc.body); code != http.StatusUnauthorized {
				t.Fatalf("prime_token 调 %s 应 401, got %d: %s", tc.path, code, out)
			}
			if code, _ := doJSON(t, srv, "", tc.method, tc.path, tc.body); code != http.StatusUnauthorized {
				t.Fatalf("无令牌调 %s 应 401", tc.path)
			}
		}
	})

	t.Run("PUT体非法_400", func(t *testing.T) {
		srv, _ := newAccessSrv(t, NewMcpAccessMemory(false), nil)
		for _, body := range []string{`not-json`, `{}`, `{"enabled":"yes"}`} {
			if code, _ := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", body); code != http.StatusBadRequest {
				t.Fatalf("PUT 体 %q 应 400", body)
			}
		}
	})
}

// TestMcpAccessRotateOneTimePlaintext 轮换（RFC §3.3）：旧值即失效、新明文仅在
// 轮换应答出现一次（GET/PUT 应答与轮换失败应答绝无明文）、No-store（新旧两路径
// 与 GET/PUT 均断查响应头，终审建议⑥）、新旧别名同一实现、失败不报成功（旧凭证
// 仍有效，重试即成功）。
func TestMcpAccessRotateOneTimePlaintext(t *testing.T) {
	const newTok = "tok-rotated-fresh"
	n := 0
	rotate := func() (string, error) {
		n++
		if n == 2 {
			return "", errors.New("disk full（注入失败）")
		}
		return newTok, nil
	}
	srv, submit := newAccessSrv(t, NewMcpAccessMemory(true), rotate)

	// 前置：旧凭据可编排（enabled）
	if code, _ := submit("tok-prime"); code != http.StatusAccepted {
		t.Fatalf("前置旧凭据应 202")
	}
	// 新端点轮换：一次返回新明文 + No-store 响应头
	code, out, hdr := doJSONHdr(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/access/rotate", "")
	if code != http.StatusOK || !strings.Contains(out, newTok) {
		t.Fatalf("rotate 应 200 且一次返回新明文, got %d: %s", code, out)
	}
	requireNoStore(t, hdr, "rotate 新路径")
	if !strings.Contains(out, `"enabled":true`) || !strings.Contains(out, `"prime_agent":""`) {
		t.Fatalf("rotate 应答应带 enabled 与 prime_agent 兼容字段: %s", out)
	}
	// 旧值即失效、新值可编排
	if code, _ := submit("tok-prime"); code != http.StatusUnauthorized {
		t.Fatalf("轮换后旧凭据应 401")
	}
	if code, _ := submit(newTok); code != http.StatusAccepted {
		t.Fatalf("轮换后新凭据应 202")
	}
	// 明文仅此一次：GET 视图只含脱敏引用；响应头 No-store
	code, out, hdr = doJSONHdr(t, srv, "tok-local", http.MethodGet, "/api/v1/local/mcp/access", "")
	if code != http.StatusOK {
		t.Fatalf("GET 应 200, got %d: %s", code, out)
	}
	requireNoStore(t, hdr, "GET 视图")
	if strings.Contains(out, newTok) {
		t.Fatalf("GET 应答不得含明文凭据: %s", out)
	}
	var view struct {
		Enabled          bool   `json:"enabled"`
		CredentialMasked string `json:"credential_masked"`
		Scope            string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.CredentialMasked != "to********" || !strings.Contains(view.Scope, "禁用不等于撤销凭据") {
		t.Fatalf("GET 视图不符（脱敏引用/影响说明）: %s", out)
	}
	// PUT 应答同样无明文（保存开关不返回任何密钥）且 No-store；随后恢复启用供失败轮换断言
	code, out, hdr = doJSONHdr(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":false}`)
	if code != http.StatusOK || strings.Contains(out, newTok) {
		t.Fatalf("PUT 应答不得含明文凭据, got %d: %s", code, out)
	}
	requireNoStore(t, hdr, "PUT 视图")
	if code, out = doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("恢复启用应 200, got %d: %s", code, out)
	}
	// 注入轮换失败：500 如实报错、不虚报成功、旧凭证（此刻为 newTok）仍有效
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusInternalServerError || !strings.Contains(out, "轮换失败") || strings.Contains(out, newTok) {
		t.Fatalf("轮换失败应 500 如实报错且不含明文, got %d: %s", code, out)
	}
	if code, _ := submit(newTok); code != http.StatusAccepted {
		t.Fatalf("轮换失败后旧凭据（未换发）应仍 202")
	}
	// 旧别名成功路径同一实现同传输纪律：失败后重试 200 + No-store（新旧两路径
	// 响应头均断查——终审建议⑥，此前旧别名仅断言失败分支未查头）
	code, out, hdr = doJSONHdr(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusOK || !strings.Contains(out, newTok) {
		t.Fatalf("旧别名重试轮换应 200 返回新明文, got %d: %s", code, out)
	}
	requireNoStore(t, hdr, "rotate 旧别名")
	if code, _ := submit(newTok); code != http.StatusAccepted {
		t.Fatalf("旧别名轮换后新凭据应 202")
	}
}

// openAccessForTest 在临时 data_dir 打开禁用态迁移产物（落盘授权状态）。
func openAccessForTest(t *testing.T) (*McpAccess, string) {
	t.Helper()
	dir := t.TempDir()
	acc, _, err := OpenMcpAccess(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return acc, dir
}

// readAccessState 读盘上状态文件为结构（失败 Fatal）。
func readAccessState(t *testing.T, path string) McpAccessState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st McpAccessState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("状态文件必须可解析: %v\n%s", err, b)
	}
	return st
}

// TestMcpAccessPutPersistence PUT 持久化与失败保留旧态（RFC §一）：成功落盘
// 0600 + source=owner + UTC；写失败如实 500、内存与盘上均保留旧态、不虚报成功。
func TestMcpAccessPutPersistence(t *testing.T) {
	acc, dir := openAccessForTest(t)
	srv, _ := newAccessSrv(t, acc, nil)
	path := filepath.Join(dir, mcpAccessFileName)

	// 迁移产物：禁用 + source=migration
	if st := readAccessState(t, path); st.Enabled || st.Source != mcpSourceMigration || st.Version != mcpAccessVersion {
		t.Fatalf("迁移产物不符: %+v", st)
	}
	// 启用：200 回显 + 落盘 owner + 0600 + UTC
	code, out := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":true}`)
	if code != http.StatusOK || !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("PUT 启用应 200, got %d: %s", code, out)
	}
	st := readAccessState(t, path)
	if !st.Enabled || st.Source != mcpSourceOwner {
		t.Fatalf("落盘状态不符: %+v", st)
	}
	if ts, err := time.Parse(time.RFC3339, st.UpdatedAt); err != nil || !strings.HasSuffix(st.UpdatedAt, "Z") {
		t.Fatalf("updated_at 应为 UTC RFC3339: %q err=%v", st.UpdatedAt, err)
	} else if ts.Location() != time.UTC {
		t.Fatalf("updated_at 应为 UTC: %v", ts.Location())
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("状态文件应 0600, got %v err=%v", fi.Mode().Perm(), err)
	}

	// 失败场景一：rename 目标被目录占位（EISDIR——temp 写成功、rename 失败窗口）
	// updated 以固定历史时刻取样：nowUTCString 秒级粒度，同秒内跑会掩盖「回滚缺
	// 失 updated」的回归（终审必修④——三元组断言须能稳定区分新时刻与旧态）。
	const seededUpdated = "2026-01-01T00:00:00Z"
	dir2 := t.TempDir()
	blocked := filepath.Join(dir2, mcpAccessFileName)
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	acc2 := &McpAccess{path: blocked, enabled: true, source: mcpSourceOwner, updated: seededUpdated}
	srv2, _ := newAccessSrv(t, acc2, nil)
	beforeEn, beforeSrc, beforeUp := acc2.Snapshot()
	code, out = doJSON(t, srv2, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":false}`)
	if code != http.StatusInternalServerError || !strings.Contains(out, "保存失败") || !strings.Contains(out, "启用") {
		t.Fatalf("写失败应 500 且注明旧态保留（启用）, got %d: %s", code, out)
	}
	if en, src, up := acc2.Snapshot(); en != beforeEn || src != beforeSrc || up != beforeUp {
		t.Fatalf("写失败后内存三元组应与操作前逐字节一致, got (%v,%s,%s) want (%v,%s,%s)", en, src, up, beforeEn, beforeSrc, beforeUp)
	}
	// 失败场景二：目录只读（CreateTemp 失败）——盘上旧文件逐字节保留
	dir3 := t.TempDir()
	path3 := filepath.Join(dir3, mcpAccessFileName)
	if err := os.WriteFile(path3, []byte(`{"version":1,"enabled":true,"updated_at":"`+seededUpdated+`","source":"owner"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path3)
	if err := os.Chmod(dir3, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir3, 0o755) })
	acc3 := &McpAccess{path: path3, enabled: true, source: mcpSourceOwner, updated: seededUpdated}
	srv3, _ := newAccessSrv(t, acc3, nil)
	beforeEn3, beforeSrc3, beforeUp3 := acc3.Snapshot()
	code, out = doJSON(t, srv3, "tok-local", http.MethodPut, "/api/v1/local/mcp/access", `{"enabled":false}`)
	if code != http.StatusInternalServerError || strings.Contains(out, `"enabled":false`) {
		t.Fatalf("只读目录写失败应 500 不虚报, got %d: %s", code, out)
	}
	if got := mustRead(t, path3); got != before {
		t.Fatalf("写失败不得损坏盘上旧态:\n got %s\nwant %s", got, before)
	}
	if en, src, up := acc3.Snapshot(); en != beforeEn3 || src != beforeSrc3 || up != beforeUp3 {
		t.Fatalf("写失败后内存三元组应与操作前逐字节一致, got (%v,%s,%s) want (%v,%s,%s)", en, src, up, beforeEn3, beforeSrc3, beforeUp3)
	}
}

// TestMcpAccessMigrationTable 迁移判定表逐行（RFC §二 M1–M5 + 损坏拒载）。
func TestMcpAccessMigrationTable(t *testing.T) {
	writeToken := func(t *testing.T, path string, n int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Repeat("a", n)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("M2_有管家有token_启用", func(t *testing.T) {
		dir := t.TempDir()
		writeToken(t, filepath.Join(dir, "prime_token"), 64)
		acc, migrated, err := OpenMcpAccess(dir, "echo", filepath.Join(dir, "prime_token"))
		if err != nil || !migrated || !acc.Enabled() {
			t.Fatalf("旧已授权配置应迁移为 enabled: migrated=%v enabled=%v err=%v", migrated, acc.Enabled(), err)
		}
		if st := readAccessState(t, filepath.Join(dir, mcpAccessFileName)); !st.Enabled || st.Source != mcpSourceMigration {
			t.Fatalf("迁移落盘不符: %+v", st)
		}
	})
	t.Run("M3_无管家_禁用", func(t *testing.T) {
		dir := t.TempDir()
		writeToken(t, filepath.Join(dir, "prime_token"), 64)
		acc, migrated, err := OpenMcpAccess(dir, "", filepath.Join(dir, "prime_token"))
		if err != nil || !migrated || acc.Enabled() {
			t.Fatalf("无 prime_agent 应迁移为 disabled: migrated=%v enabled=%v err=%v", migrated, acc.Enabled(), err)
		}
	})
	t.Run("M4a_token缺失_禁用", func(t *testing.T) {
		dir := t.TempDir()
		acc, _, err := OpenMcpAccess(dir, "echo", filepath.Join(dir, "prime_token"))
		if err != nil || acc.Enabled() {
			t.Fatalf("token 文件缺失应 disabled: enabled=%v err=%v", acc.Enabled(), err)
		}
	})
	t.Run("M4b_token过短_禁用", func(t *testing.T) {
		dir := t.TempDir()
		writeToken(t, filepath.Join(dir, "prime_token"), 10)
		acc, _, err := OpenMcpAccess(dir, "echo", filepath.Join(dir, "prime_token"))
		if err != nil || acc.Enabled() {
			t.Fatalf("token 文件过短应 disabled: enabled=%v err=%v", acc.Enabled(), err)
		}
	})
	t.Run("M5_全新实例_禁用", func(t *testing.T) {
		acc, migrated, err := OpenMcpAccess(t.TempDir(), "", "")
		if err != nil || !migrated || acc.Enabled() {
			t.Fatalf("全新实例应 disabled: migrated=%v enabled=%v err=%v", migrated, acc.Enabled(), err)
		}
	})
	t.Run("M1_显式状态不重推导", func(t *testing.T) {
		dir := t.TempDir()
		writeToken(t, filepath.Join(dir, "prime_token"), 64)
		// 先落显式禁用状态
		acc1, migrated1, err := OpenMcpAccess(dir, "", filepath.Join(dir, "prime_token"))
		if err != nil || !migrated1 || acc1.Enabled() {
			t.Fatalf("前置迁移失败: %v", err)
		}
		// 此后即便 prime_agent 与 token 均在位，重启也不重推导
		acc2, migrated2, err := OpenMcpAccess(dir, "echo", filepath.Join(dir, "prime_token"))
		if err != nil || migrated2 {
			t.Fatalf("显式状态存在不得再迁移: migrated=%v err=%v", migrated2, err)
		}
		if acc2.Enabled() {
			t.Fatal("显式禁用状态应保持（管家增删不重推导）")
		}
		// 反向同规：显式启用后撤掉管家与 token 文件，状态仍启用
		if err := acc2.Set(true); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "prime_token")); err != nil {
			t.Fatal(err)
		}
		acc3, _, err := OpenMcpAccess(dir, "", filepath.Join(dir, "prime_token"))
		if err != nil || !acc3.Enabled() {
			t.Fatalf("显式启用应唯一真相: enabled=%v err=%v", acc3.Enabled(), err)
		}
	})
	t.Run("损坏与未知版本_拒载", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, mcpAccessFileName)
		if err := os.WriteFile(path, []byte(`{oops`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenMcpAccess(dir, "", ""); err == nil {
			t.Fatal("损坏状态文件应拒载（授权状态不猜测）")
		}
		if err := os.WriteFile(path, []byte(`{"version":99,"enabled":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenMcpAccess(dir, "", ""); err == nil || !strings.Contains(err.Error(), "版本") {
			t.Fatalf("未知版本应拒载: %v", err)
		}
	})
}

// TestDeclareKeepsTokenAndAccessState declare 去换发回归（RFC §五）：declare 只
// 做管家声明——共享凭据不变（enabled 下旧 token 仍编排成功）、调用权状态不变
// （disabled 下 declare 后仍禁用）、应答无明文（prime_token 恒空、mcp_config 凭据
// 位脱敏）、不触发轮换回调。
func TestDeclareKeepsTokenAndAccessState(t *testing.T) {
	build := func(t *testing.T, enabled bool) (*httptest.Server, string, *int) {
		t.Helper()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "node.yaml")
		if err := os.WriteFile(cfgPath, []byte(declareYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		bus := transport.NewBus()
		led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = led.Close() })
		svc := svcpkg.New(svcpkg.Config{
			Ledger:  led,
			Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
		}, bus.Endpoint("nodeA"))
		t.Cleanup(svc.Close)
		n := 0
		s := New(Config{
			NodeID: "nodeA", Service: svc, Ledger: led,
			Agents: newState(t, dir, []protocol.ExecProfile{echoProfileLocal(t),
				{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep",
					Workdir: t.TempDir(), TimeoutSec: 1, Exposed: false}}),
			LocalToken:        "tok-local",
			PrimeToken:        "tok-old",
			LocalListen:       "127.0.0.1:8445",
			DeclareConfigPath: cfgPath,
			McpAccess:         NewMcpAccessMemory(enabled),
			RotatePrime: func() (string, error) {
				n++
				return fmt.Sprintf("tok-should-not-rotate-%d", n), nil
			},
		})
		s.mcpCommandOverride = "/fake/swarmmind-mcp"
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(srv.Close)
		return srv, cfgPath, &n
	}
	submit := func(srv *httptest.Server, tok string) (int, string) {
		return doJSON(t, srv, tok, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}

	t.Run("enabled_凭据与调用权均不变", func(t *testing.T) {
		srv, _, rotates := build(t, true)
		if code, _ := submit(srv, "tok-old"); code != http.StatusAccepted {
			t.Fatalf("前置旧凭据应 202")
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`)
		if code != http.StatusOK {
			t.Fatalf("declare 应 200, got %d: %s", code, out)
		}
		if strings.Contains(out, "tok-old") || strings.Contains(out, "tok-should-not-rotate") {
			t.Fatalf("declare 应答不得含任何明文凭据: %s", out)
		}
		var body struct {
			PrimeToken string         `json:"prime_token"`
			MCPConfig  map[string]any `json:"mcp_config"`
			Wired      bool           `json:"wired"`
			Note       string         `json:"note"`
		}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatal(err)
		}
		if body.PrimeToken != "" {
			t.Fatalf("prime_token 兼容位应恒空串, got %q", body.PrimeToken)
		}
		env, _ := body.MCPConfig["env"].(map[string]any)
		if got, _ := env["SWARM_NODE_PRIME_TOKEN"].(string); got != "to********" {
			t.Fatalf("mcp_config 凭据位应为脱敏引用, got %q", got)
		}
		// 本夹具 echo 无 ConfigHome/DataDir → 影子 home 不可解，接线降级（命中
		// 布局时以当前凭据接线的口径由 primewire_test 声明系用例锁定）；
		// note 必须声明不换发并给手动指引。
		if body.Wired || !strings.Contains(body.Note, "不再换发") || !strings.Contains(body.Note, "手动配置") {
			t.Fatalf("declare 降级口径不符: wired=%v note=%s", body.Wired, body.Note)
		}
		// 共享凭据不变：旧 token 仍编排成功；轮换回调未被触发
		if code, _ := submit(srv, "tok-old"); code != http.StatusAccepted {
			t.Fatalf("declare 后共享凭据应不变（旧 token 仍 202）")
		}
		if *rotates != 0 {
			t.Fatalf("declare 不得触发轮换回调, got %d 次", *rotates)
		}
		// 调用权状态不变：仍启用
		if code, out := doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/mcp/access", ""); code != http.StatusOK || !strings.Contains(out, `"enabled":true`) {
			t.Fatalf("declare 不得改写调用权状态, got %d: %s", code, out)
		}
	})

	t.Run("disabled_declare不暗启调用权", func(t *testing.T) {
		srv, _, _ := build(t, false)
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare", `{"prime_agent":"echo"}`)
		if code != http.StatusOK {
			t.Fatalf("declare 应 200, got %d: %s", code, out)
		}
		if code, out := doJSON(t, srv, "tok-local", http.MethodGet, "/api/v1/local/mcp/access", ""); code != http.StatusOK || !strings.Contains(out, `"enabled":false`) {
			t.Fatalf("declare 后调用权应保持禁用, got %d: %s", code, out)
		}
		if code, out := submit(srv, "tok-old"); code != http.StatusForbidden || !strings.Contains(out, "mcp_disabled") {
			t.Fatalf("declare 后 prime 编排应仍 403 mcp_disabled, got %d: %s", code, out)
		}
		if code, _ := submit(srv, "tok-local"); code != http.StatusAccepted {
			t.Fatalf("declare 后主人编排应仍恒放行 202")
		}
	})
}

// TestMcpAccessRotateWiresAndSerializes 并发轮换 + 接线刷新（W02-01 语义保留项）：
// 轮换与接线同 muPrime 互斥段——并发轮换后宿主配置内是最后换发的凭证、无旧凭证
// 残留；轮换不改授权开关。
func TestMcpAccessRotateWiresAndSerializes(t *testing.T) {
	home := t.TempDir()
	setting := writeZcodeSetting(t, home, `{"mcpServers":{"other":{"command":"keep-me"}}}`)
	echoHome := home
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(cfgPath, []byte(declareYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	echo := echoProfileLocal(t)
	echo.ConfigHome = echoHome
	n := 0
	var mu sync.Mutex
	s := New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		Agents: newState(t, dir, []protocol.ExecProfile{echo}),
		LocalToken: "tok-local", PrimeToken: "tok-seed", PrimeAgent: "echo",
		LocalListen: "127.0.0.1:8445", DataDir: dir,
		McpAccess: NewMcpAccessMemory(true),
		RotatePrime: func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			n++
			return fmt.Sprintf("tok-rot-%02d", n), nil
		},
	})
	s.mcpCommandOverride = "/fake/swarmmind-mcp"
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	const k = 4
	tokens := make([]string, k)
	var wg sync.WaitGroup
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/api/v1/local/mcp/access/rotate"
			if i%2 == 1 {
				path = "/api/v1/local/prime/rotate" // 旧别名混跑：同一实现
			}
			code, out := doJSON(t, srv, "tok-local", http.MethodPost, path, "")
			if code != http.StatusOK {
				t.Errorf("并发轮换应 200, got %d: %s", code, out)
				return
			}
			var body struct {
				PrimeToken string `json:"prime_token"`
			}
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Errorf("应答解析失败: %v", err)
				return
			}
			tokens[i] = body.PrimeToken
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok == "" || seen[tok] {
			t.Fatalf("并发轮换应各得唯一凭证, got %q", tokens)
		}
		seen[tok] = true
	}
	// 任一时刻恰一张有效凭证：非最终凭证全 401，最终凭证 202
	live, liveTok := 0, ""
	for _, tok := range tokens {
		code, _ := doJSON(t, srv, tok, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
		switch code {
		case http.StatusAccepted:
			live++
			liveTok = tok
		case http.StatusUnauthorized:
		default:
			t.Fatalf("非最终凭证应 401, got %d（%s）", code, tok)
		}
	}
	if live != 1 {
		t.Fatalf("并发轮换后应恰一张有效编排凭证, got %d", live)
	}
	// 接线：宿主配置内是唯一存活凭证，无旧凭证残留，其他条目原样
	doc := readSettingDoc(t, setting)
	if tok := entryToken(t, doc); tok != liveTok {
		t.Fatalf("setting.json 内 token=%q, want 存活凭证 %q", tok, liveTok)
	}
	raw := mustRead(t, setting)
	for _, tok := range tokens {
		if tok != liveTok && strings.Contains(raw, tok) {
			t.Fatalf("旧凭证 %q 不得残留在宿主配置", tok)
		}
	}
	if other := serversOf(t, doc)["other"].(map[string]any); other["command"] != "keep-me" {
		t.Fatalf("其他 MCP 条目应原样: %v", other)
	}
}
