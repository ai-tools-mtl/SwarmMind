package localapi

// 本机宿主 MCP 配置预览/应用/撤回（W03-02，docs/tasks/v3/W03-02-rfc.md）fixture 单测，
// 逐条对齐任务书 fixture 清单：多 MCP 条目合并（其他条目语义级不动+数值字面量保真）、
// 同名冲突（拒绝+建议名+显式替换）、非法 JSON（apply 拒绝原文件保留）、只读目录
// （失败如实）、rename/备份失败窗口（注入）、用户后改 revert 409 file_conflict、
// 符号链接三态拒绝、三端点鉴权、零明文金丝、绝不创建宿主目录、版本门。
//
// 纪律：全部 config_home / data_dir 注入 t.TempDir，绝不写真实 HOME；测试密钥全为
// 假值（金丝锁定其完整形态不得出现在任何应答面）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// mcpCfgFakePrime / mcpCfgFakeOldSecret 假密钥（仅测试，非真实凭据）：
	// mcpCfgFakePrime 为测试服务端的共享编排凭据（apply 落盘值）；
	// mcpCfgFakeOldSecret 预埋宿主配置既有条目（金丝：三端点应答绝不回显其完整形态）。
	mcpCfgFakePrime     = "faketest-prime-token-DO-NOT-LEAK-0123456789"
	mcpCfgFakeOldSecret = "faketest-old-secret-DO-NOT-LEAK-9988776655"
)

// newMcpCfgSrv 三端点测试环境：最小装配（三端点不依赖 Service/Ledger），DataDir
// 注入 TempDir（撤回登记落点）。config_home 由各用例独立 t.TempDir 注入。
func newMcpCfgSrv(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	srv := httptest.NewServer(New(Config{
		NodeID:     "nodeA",
		LocalToken: "tok-local",
		PrimeToken: mcpCfgFakePrime,
		DataDir:    dataDir,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, dataDir
}

// mcpCfgSeedSetting 预置 zcode 宿主配置 fixture：多个既有 MCP 条目（含假密钥 env、
// 端口数字、浮点、超 float64 精度整数、数组、非 mcpServers 顶层键）——数值保真与
// 「其他条目语义级不动」的断言锚点。
func mcpCfgSeedSetting(t *testing.T, cfgHome string) string {
	t.Helper()
	dir := filepath.Join(cfgHome, ".zcode", "cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{
  "model": "gpt-x",
  "theme": "dark",
  "mcpServers": {
    "other-svc": {
      "command": "/usr/local/bin/other",
      "env": {"OTHER_TOKEN": "` + mcpCfgFakeOldSecret + `"},
      "port": 8445,
      "ratio": 1.50,
      "bigint": 12345678901234567890,
      "tags": ["a", "b"]
    },
    "legacy": {"command": "/bin/legacy", "args": [1, 2]}
  }
}
`
	path := filepath.Join(dir, "setting.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mcpCfgFakeCommand 造一个存在的 swarmmind-mcp 假路径（apply 的 command 探测门要求
// 真实存在——不写占位配置）。
func mcpCfgFakeCommand(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "swarmmind-mcp")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// mcpCfgPreviewBody / mcpCfgApplyBody 请求体构造。
func mcpCfgPreviewBody(cfgHome string) string {
	return `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + mcpCfgPlaceholderCmd + `"}`
}

// mcpCfgPlaceholderCmd preview 用不存在的 command 路径（预览不做 command 存在硬门）。
const mcpCfgPlaceholderCmd = "/nonexistent/fake/swarmmind-mcp"

func mcpCfgApplyBody(cfgHome, cmdPath, expectedVersion string, replace bool) string {
	return `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath +
		`","expected_version":"` + expectedVersion + `","replace_confirmed":` + boolStr(replace) + `}`
}

// mcpCfgDecode 应答 JSON 解码（失败即 Fatal，带原文便于定位）。
func mcpCfgDecode(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("应答非 JSON: %v\n%s", err, out)
	}
	return m
}

// mcpCfgRespCanary 三端点应答零明文金丝 + 脱敏正向对照（既有条目假密钥与共享凭据
// 明文绝不出现；新条目凭据位必须是 maskCredential 脱敏引用——证明凭据确经此路径
// 流动且已脱敏，金丝不是因输出为空而虚绿）。
func mcpCfgRespCanary(t *testing.T, where, out string, requireMasked bool) {
	t.Helper()
	cfgNoSecret(t, where, out, mcpCfgFakeOldSecret, mcpCfgFakePrime)
	if requireMasked && !strings.Contains(out, "fa********") {
		t.Fatalf("%s 应含脱敏引用 fa********（正向对照）: %s", where, out)
	}
}

// mcpCfgSnapshot 目录全量快照（相对路径 → 内容），用于预览零写盘断言。
func mcpCfgSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if fi.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMcpCfgAuthGate 三端点鉴权（任务书 fixture：prime_token 401、无 token 401）；
// local_token 越过鉴权后落业务校验（400/404/503——证明不是被 401 挡住的假绿）。
func TestMcpCfgAuthGate(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()

	t.Run("prime_token与无token一律401", func(t *testing.T) {
		cases := []struct {
			name, method, path, body string
		}{
			{"preview", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome)},
			{"apply", http.MethodPost, "/api/v1/local/mcp/config/apply", mcpCfgApplyBody(cfgHome, mcpCfgPlaceholderCmd, "x", false)},
			{"revert", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"x"}`},
		}
		for _, tc := range cases {
			for _, tok := range []string{"", "tok-prime"} {
				code, out := doJSON(t, srv, tok, tc.method, tc.path, tc.body)
				if code != http.StatusUnauthorized || !strings.Contains(out, "unauthorized") {
					t.Fatalf("%s token=%q 应 401 unauthorized, got %d: %s", tc.name, tok, code, out)
				}
			}
		}
	})

	t.Run("local_token越过鉴权落业务校验", func(t *testing.T) {
		if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", `{`); code != 400 || !strings.Contains(out, "invalid_request") {
			t.Fatalf("preview 非法体应 400 invalid_request, got %d: %s", code, out)
		}
		if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{}`); code != 400 || !strings.Contains(out, "invalid_request") {
			t.Fatalf("revert 缺 revert_id 应 400, got %d: %s", code, out)
		}
	})

	t.Run("DataDir未装配_apply_revert_503", func(t *testing.T) {
		srv2 := httptest.NewServer(New(Config{NodeID: "nodeA", LocalToken: "tok-local"}).Handler())
		t.Cleanup(srv2.Close)
		if code, out := doJSON(t, srv2, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", mcpCfgApplyBody(cfgHome, mcpCfgPlaceholderCmd, "x", false)); code != 503 {
			t.Fatalf("apply 无 DataDir 应 503, got %d: %s", code, out)
		}
		if code, out := doJSON(t, srv2, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"x"}`); code != 503 {
			t.Fatalf("revert 无 DataDir 应 503, got %d: %s", code, out)
		}
	})
}

// TestMcpCfgPreviewApplyRevertFullFlow 主链路：多 MCP 条目合并（其他条目语义级不动
// +数值字面量逐字节保真）→ revert 还原 → 二次 revert 404；预览零写盘；全程零明文
// 金丝（任务书 fixture：多条目合并/数值保真/零明文/临时 HOME）。
func TestMcpCfgPreviewApplyRevertFullFlow(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	settingPath := mcpCfgSeedSetting(t, cfgHome)
	cmdPath := mcpCfgFakeCommand(t)

	// —— preview：零写盘 + 脱敏差异 + 可写性 + 无冲突 ——
	beforeSnap := mcpCfgSnapshot(t, cfgHome)
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if code != 200 {
		t.Fatalf("preview 应 200, got %d: %s", code, out)
	}
	mcpCfgRespCanary(t, "preview", out, true)
	resp := mcpCfgDecode(t, out)
	file, _ := resp["file"].(map[string]any)
	if file == nil || file["exists"] != true || file["version"] == "" {
		t.Fatalf("preview file 事实缺失: %v", resp)
	}
	version, _ := file["version"].(string)
	wr, _ := resp["writability"].(map[string]any)
	if wr["can_auto_write"] != true || wr["support_level"] != SupportLevelVerifiedZcode {
		t.Fatalf("zcode+node 可写性口径不符: %v", wr)
	}
	if resp["conflict_available"] != true {
		t.Fatalf("zcode 应可冲突检测: %v", resp)
	}
	conflict, _ := resp["conflict"].(map[string]any)
	if conflict["conflict"] != false {
		t.Fatalf("默认名无既有条目应无冲突: %v", conflict)
	}
	diff, _ := resp["diff"].(map[string]any)
	if diff["kind"] != "create" {
		t.Fatalf("无既有目标条目 diff.kind 应 create: %v", diff)
	}
	newEntry, _ := diff["new_entry"].(map[string]any)
	env, _ := newEntry["env"].(map[string]any)
	if env["SWARM_NODE_PRIME_TOKEN"] != "fa********" || env["SWARM_TARGET"] != "node" {
		t.Fatalf("new_entry 脱敏/范围口径不符: %v", newEntry)
	}
	afterSnap := mcpCfgSnapshot(t, cfgHome)
	if len(beforeSnap) != len(afterSnap) {
		t.Fatalf("preview 零写盘被破坏：目录条目数 %d → %d", len(beforeSnap), len(afterSnap))
	}
	for k, v := range beforeSnap {
		if afterSnap[k] != v {
			t.Fatalf("preview 零写盘被破坏：%s 内容/存在性变化", k)
		}
	}

	// —— apply：合并写入 + 数值保真 + 其他条目语义级不动 ——
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, cmdPath, version, false))
	if code != 200 {
		t.Fatalf("apply 应 200, got %d: %s", code, out)
	}
	mcpCfgRespCanary(t, "apply", out, false) // 应答不含明文（落盘值也不回显）
	aresp := mcpCfgDecode(t, out)
	if aresp["replaced_existing"] != false {
		t.Fatalf("新建条目 replaced_existing 应 false: %v", aresp)
	}
	revertID, _ := aresp["revert"].(map[string]any)["revert_id"].(string)
	if revertID == "" {
		t.Fatalf("apply 应答缺 revert.revert_id: %v", aresp)
	}
	written, err := os.ReadFile(settingPath)
	if err != nil {
		t.Fatal(err)
	}
	ws := string(written)
	// 其他条目语义级不动：值逐字节（数值字面量不经 float64 洗——1.50 不得变 1.5、
	// 超 float64 精度整数不得舍入）。
	for _, anchor := range []string{
		`"port": 8445`, `"ratio": 1.50`, `"bigint": 12345678901234567890`,
		`"/usr/local/bin/other"`, `"/bin/legacy"`, `"theme": "dark"`,
		mcpCfgFakeOldSecret,
	} {
		if !strings.Contains(ws, anchor) {
			t.Fatalf("apply 破坏其他条目/数值保真：未找到锚点 %s\n%s", anchor, ws)
		}
	}
	// 目标条目已写入：默认名 + 当前共享凭据明文落盘（盘面是凭据的家，应答不是）。
	if !strings.Contains(ws, `"swarmmind-node"`) || !strings.Contains(ws, mcpCfgFakePrime) ||
		!strings.Contains(ws, `"SWARM_NODE_URL": "http://127.0.0.1:8445"`) {
		t.Fatalf("目标条目未按契约写入:\n%s", ws)
	}

	// —— revert：只撤本次条目，其他条目与数值保真不动 ——
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert",
		`{"revert_id":"`+revertID+`"}`)
	if code != 200 {
		t.Fatalf("revert 应 200, got %d: %s", code, out)
	}
	mcpCfgRespCanary(t, "revert", out, false)
	rresp := mcpCfgDecode(t, out)
	if rresp["restored"] != "deleted" {
		t.Fatalf("apply 前无既有条目，revert 应 restored=deleted: %v", rresp)
	}
	restored, err := os.ReadFile(settingPath)
	if err != nil {
		t.Fatal(err)
	}
	rs := string(restored)
	if strings.Contains(rs, "swarmmind-node") || strings.Contains(rs, mcpCfgFakePrime) {
		t.Fatalf("revert 未撤净本次条目:\n%s", rs)
	}
	for _, anchor := range []string{`"port": 8445`, `"ratio": 1.50`, `"bigint": 12345678901234567890`, mcpCfgFakeOldSecret} {
		if !strings.Contains(rs, anchor) {
			t.Fatalf("revert 破坏其他条目/数值保真：未找到锚点 %s\n%s", anchor, rs)
		}
	}

	// —— 二次 revert：登记已清 → 404 ——
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert",
		`{"revert_id":"`+revertID+`"}`); code != 404 || !strings.Contains(out, "revert_not_found") {
		t.Fatalf("二次 revert 应 404 revert_not_found, got %d: %s", code, out)
	}
}

// TestMcpCfgNameConflictFlow 同名冲突（任务书 fixture：拒绝+建议名+显式确认替换），
// 替换后 revert 还原旧条目（旧值快照数值保真）。
func TestMcpCfgNameConflictFlow(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	dir := filepath.Join(cfgHome, ".zcode", "cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	settingPath := filepath.Join(dir, "setting.json")
	original := `{"mcpServers":{"swarmmind-node":{"command":"/old/path","env":{"SWARM_TARGET":"node","SWARM_NODE_PRIME_TOKEN":"` + mcpCfgFakeOldSecret + `"},"port":8080}}}
`
	if err := os.WriteFile(settingPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdPath := mcpCfgFakeCommand(t)

	// preview：冲突=true + 建议名 + replace 差异（旧 command 回显——路径非秘密）。
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if code != 200 {
		t.Fatalf("preview 应 200, got %d: %s", code, out)
	}
	mcpCfgRespCanary(t, "preview", out, true)
	resp := mcpCfgDecode(t, out)
	conflict, _ := resp["conflict"].(map[string]any)
	if conflict["conflict"] != true || conflict["kind"] != ConflictKindSameScope ||
		conflict["suggested_name"] != "swarmmind-node-2" {
		t.Fatalf("冲突检测口径不符: %v", conflict)
	}
	if diff, _ := resp["diff"].(map[string]any); diff["kind"] != "replace" {
		t.Fatalf("撞名时 diff.kind 应 replace: %v", resp)
	}
	if ee, _ := resp["diff"].(map[string]any)["existing_entry"].(map[string]any); ee["command"] != "/old/path" {
		t.Fatalf("existing_entry.command 应回显路径: %v", ee)
	}

	// apply 未确认 → 409 name_conflict（附建议名），原文件字节级保留。
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, cmdPath, resp["file"].(map[string]any)["version"].(string), false))
	if code != 409 || !strings.Contains(out, "name_conflict") || !strings.Contains(out, "swarmmind-node-2") {
		t.Fatalf("未确认替换应 409 name_conflict+建议名, got %d: %s", code, out)
	}
	if b, _ := os.ReadFile(settingPath); string(b) != original {
		t.Fatalf("409 后原文件必须字节级保留")
	}

	// 显式确认替换 → 200 replaced_existing=true。
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, cmdPath, resp["file"].(map[string]any)["version"].(string), true))
	if code != 200 {
		t.Fatalf("确认替换后 apply 应 200, got %d: %s", code, out)
	}
	aresp := mcpCfgDecode(t, out)
	if aresp["replaced_existing"] != true {
		t.Fatalf("替换场景 replaced_existing 应 true: %v", aresp)
	}
	revertID, _ := aresp["revert"].(map[string]any)["revert_id"].(string)

	// revert：还原旧条目（旧 command/旧 token/端口数字 8080 逐字面量回来）。
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert",
		`{"revert_id":"`+revertID+`"}`); code != 200 {
		t.Fatalf("revert 应 200, got %d: %s", code, out)
	}
	if mcpCfgDecode(t, out)["restored"] != "previous_value" {
		t.Fatalf("替换后 revert 应 restored=previous_value: %s", out)
	}
	b, _ := os.ReadFile(settingPath)
	if strings.Contains(string(b), mcpCfgFakePrime) {
		t.Fatalf("revert 后不应残留新凭据:\n%s", b)
	}
	if !strings.Contains(string(b), `"/old/path"`) || !strings.Contains(string(b), `"port": 8080`) ||
		!strings.Contains(string(b), mcpCfgFakeOldSecret) {
		t.Fatalf("revert 未还原旧条目（含数值保真）:\n%s", b)
	}
}

// TestMcpCfgInvalidJSONRefused 非法 JSON（任务书 fixture：apply 拒绝原文件保留）；
// 预览按事实呈现（200+issues）不报错。
func TestMcpCfgInvalidJSONRefused(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	settingPath := mcpCfgSeedSetting(t, cfgHome)
	broken := `{"mcpServers": {` // 非法 JSON
	if err := os.WriteFile(settingPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	// preview：200 + issue 如实（不回错误上下文内容）。
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if code != 200 {
		t.Fatalf("preview 对非法 JSON 应 200+事实呈现, got %d: %s", code, out)
	}
	resp := mcpCfgDecode(t, out)
	if resp["conflict_available"] != false || resp["diff"] != nil {
		t.Fatalf("非法 JSON 下冲突/差异应不可用: %v", resp)
	}

	// apply：400 invalid_json，原文件字节级保留。
	code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, mcpCfgFakeCommand(t), "whatever", false))
	if code != 400 || !strings.Contains(out, "invalid_json") {
		t.Fatalf("apply 对非法 JSON 应 400 invalid_json, got %d: %s", code, out)
	}
	if b, _ := os.ReadFile(settingPath); string(b) != broken {
		t.Fatalf("invalid_json 后原文件必须字节级保留")
	}
}

// TestMcpCfgReadOnlyRefused 只读文件/目录（任务书 fixture：失败如实）。root 用户
// 权限位不生效，跳过。两语义分测：
//   - 只读文件（0400）：写权限位=主人保护信号——预览 writable=false、apply 写闸门
//     500 拒绝（rename 可绕过文件权限位，但显式只读的在用配置不硬改）；
//   - 只读目录（0500）：文件本体可写（探测如实 true），备份 CreateTemp 失败 →
//     apply 500 中止，主文件未动。
func TestMcpCfgReadOnlyRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下权限位不生效")
	}
	srv, _ := newMcpCfgSrv(t)

	t.Run("只读文件_写闸门拒绝", func(t *testing.T) {
		cfgHome := t.TempDir()
		settingPath := mcpCfgSeedSetting(t, cfgHome)
		original, _ := os.ReadFile(settingPath)
		if err := os.Chmod(settingPath, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(settingPath, 0o600) })

		pv, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
		if pv != 200 {
			t.Fatalf("preview 应 200, got %d: %s", pv, out)
		}
		resp := mcpCfgDecode(t, out)
		file, _ := resp["file"].(map[string]any)
		if file["writable"] != false {
			t.Fatalf("只读文件 preview writable 应 false（事实呈现）: %v", resp)
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(cfgHome, mcpCfgFakeCommand(t), file["version"].(string), false))
		if code != 500 || !strings.Contains(out, "write_failed") || !strings.Contains(out, "不可写") {
			t.Fatalf("只读文件 apply 应 500 write_failed, got %d: %s", code, out)
		}
		if b, _ := os.ReadFile(settingPath); string(b) != string(original) {
			t.Fatalf("拒绝后原文件必须字节级保留")
		}
	})

	t.Run("只读目录_备份失败中止", func(t *testing.T) {
		cfgHome := t.TempDir()
		cliDir := filepath.Join(cfgHome, ".zcode", "cli")
		if err := os.MkdirAll(cliDir, 0o700); err != nil {
			t.Fatal(err)
		}
		settingPath := filepath.Join(cliDir, "setting.json")
		original := `{"mcpServers":{}}`
		if err := os.WriteFile(settingPath, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(cliDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cliDir, 0o700) }) // 保证 TempDir 可清理

		pv, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
		if pv != 200 {
			t.Fatalf("preview 应 200, got %d: %s", pv, out)
		}
		// 文件本体可写（探测如实 true——目录权限在创建/改名时才生效）。
		file, _ := mcpCfgDecode(t, out)["file"].(map[string]any)
		if file["writable"] != true {
			t.Fatalf("只读目录下文件本体探测应 writable=true（探测=文件打开语义）: %v", file)
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(cfgHome, mcpCfgFakeCommand(t), file["version"].(string), false))
		if code != 500 || !strings.Contains(out, "write_failed") || !strings.Contains(out, "未动") {
			t.Fatalf("只读目录 apply 应 500 write_failed（主文件未动）, got %d: %s", code, out)
		}
		if b, _ := os.ReadFile(settingPath); string(b) != original {
			t.Fatalf("失败后原文件必须字节级保留")
		}
	})
}

// TestMcpCfgBackupFailureInjection rename 失败窗口注入（任务书 fixture）：备份路径
// 被占为目录 → 备份 rename 失败 → apply 中止 + 撤回登记回滚（主文件未动、无残留
// 登记可撤）。
func TestMcpCfgBackupFailureInjection(t *testing.T) {
	srv, dataDir := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	settingPath := mcpCfgSeedSetting(t, cfgHome)
	cmdPath := mcpCfgFakeCommand(t)
	// 备份落点占为目录：atomicWriteFile0600 的 rename(tmp, <bak>) 必失败。
	if err := os.MkdirAll(settingPath+mcpCfgBackupSuffix, 0o700); err != nil {
		t.Fatal(err)
	}

	pv, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if pv != 200 {
		t.Fatalf("preview 应 200, got %d: %s", pv, out)
	}
	version := mcpCfgDecode(t, out)["file"].(map[string]any)["version"].(string)

	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, cmdPath, version, false))
	if code != 500 || !strings.Contains(out, "write_failed") || !strings.Contains(out, "主文件未动") {
		t.Fatalf("备份 rename 失败应 500 write_failed（主文件未动）, got %d: %s", code, out)
	}
	if b, _ := os.ReadFile(settingPath); strings.Contains(string(b), "swarmmind-node") {
		t.Fatalf("备份失败后主文件不得被改写:\n%s", b)
	}
	// 登记已回滚：无残留可撤（revert 任意 id → 404；登记文件 writes 为空）。
	raw, err := os.ReadFile(filepath.Join(dataDir, mcpCfgRegistryFileName))
	if err != nil {
		t.Fatalf("登记文件应已落盘（回滚形态）: %v", err)
	}
	var reg mcpCfgRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.Writes) != 0 {
		t.Fatalf("apply 失败后登记必须回滚为空, got %d 条", len(reg.Writes))
	}
}

// TestMcpCfgRevertConflictAndNoop 用户后改冲突与 noop 收敛（任务书 fixture：用户后改
// 导致 revert 409 file_conflict，不回滚整文件）；附带同文件单槽登记（新 apply 覆盖
// 旧 revert_id）。
func TestMcpCfgRevertConflictAndNoop(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	settingPath := mcpCfgSeedSetting(t, cfgHome)
	cmdPath := mcpCfgFakeCommand(t)

	apply := func(expectedVersion string) (string, string) {
		// replace=true：本测试关心版本门/单槽语义，不关心冲突门（目标条目在 ④ 中
		// 已存在——重复 apply 即显式替换）。
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(cfgHome, cmdPath, expectedVersion, true))
		if code != 200 {
			t.Fatalf("apply 应 200, got %d: %s", code, out)
		}
		m := mcpCfgDecode(t, out)
		id, _ := m["revert"].(map[string]any)["revert_id"].(string)
		va, _ := m["version_after"].(string)
		return id, va
	}
	previewVersion := func() string {
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
		if code != 200 {
			t.Fatalf("preview 应 200, got %d: %s", code, out)
		}
		return mcpCfgDecode(t, out)["file"].(map[string]any)["version"].(string)
	}

	// ① apply 后用户改文件 → revert 409 file_conflict（不回滚整文件）。
	preApply, _ := os.ReadFile(settingPath)
	id1, _ := apply(previewVersion())
	postApply, _ := os.ReadFile(settingPath)
	if err := os.WriteFile(settingPath, append(postApply, []byte("\n// 用户手改痕迹（JSON 非法也无所谓——版本门先行）")...), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id1+`"}`)
	if code != 409 || !strings.Contains(out, "file_conflict") {
		t.Fatalf("用户后改后 revert 应 409 file_conflict, got %d: %s", code, out)
	}
	// 用户改动的文件保持原样（revert 绝不代写）。
	b, _ := os.ReadFile(settingPath)
	if string(b) == string(preApply) || string(b) == string(postApply) {
		t.Fatalf("409 场景文件不得被 revert 改写")
	}

	// ② 用户手工还原到 apply 后字节 → revert 正常执行。
	if err := os.WriteFile(settingPath, postApply, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id1+`"}`); code != 200 {
		t.Fatalf("还原后 revert 应 200, got %d: %s", code, out)
	}

	// ③ apply 实际未落盘残迹 → revert noop；登记作废后二次 revert 404。
	preBytes, _ := os.ReadFile(settingPath)
	id2, _ := apply(previewVersion())
	if err := os.WriteFile(settingPath, preBytes, 0o600); err != nil { // 模拟"回到 apply 前"
		t.Fatal(err)
	}
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id2+`"}`); code != 200 {
		t.Fatalf("noop revert 应 200, got %d: %s", code, out)
	}
	if mcpCfgDecode(t, out)["restored"] != "noop" {
		t.Fatalf("回写 apply 前字节后 revert 应 noop: %s", out)
	}
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id2+`"}`); code != 404 {
		t.Fatalf("noop 后二次 revert 应 404, got %d: %s", code, out)
	}

	// ④ 同文件单槽：连续两次 apply，旧 revert_id 被覆盖 → 404；新 id 可撤。
	id3, versionAfter3 := apply(previewVersion())
	id4, _ := apply(versionAfter3)
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id3+`"}`); code != 404 {
		t.Fatalf("被覆盖的旧 revert_id 应 404, got %d: %s", code, out)
	}
	if code, out = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/revert", `{"revert_id":"`+id4+`"}`); code != 200 {
		t.Fatalf("新 revert_id 应可撤, got %d: %s", code, out)
	}
}

// TestMcpCfgSymlinkRejected 符号链接三态拒绝（任务书 fixture：config_home 或配置
// 文件为链接）；preview 按事实呈现、apply 一律 400 unsafe_path，绝不跟随。
func TestMcpCfgSymlinkRejected(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cmdPath := mcpCfgFakeCommand(t)

	t.Run("config_home为符号链接", func(t *testing.T) {
		real := t.TempDir()
		if err := mcpCfgSeedSettingX(real); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "home-link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(link, cmdPath, "x", false))
		if code != 400 || !strings.Contains(out, "unsafe_path") || !strings.Contains(out, "符号链接") {
			t.Fatalf("config_home 链接应 400 unsafe_path, got %d: %s", code, out)
		}
		pv, pout := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(link))
		if pv != 200 || !strings.Contains(pout, "符号链接") {
			t.Fatalf("preview 应 200+如实注记符号链接, got %d: %s", pv, pout)
		}
	})

	t.Run("中间目录.zcode为符号链接", func(t *testing.T) {
		cfgHome := t.TempDir()
		external := t.TempDir()
		if err := os.Symlink(external, filepath.Join(cfgHome, ".zcode")); err != nil {
			t.Fatal(err)
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(cfgHome, cmdPath, "x", false))
		if code != 400 || !strings.Contains(out, "unsafe_path") {
			t.Fatalf(".zcode 链接应 400 unsafe_path（整链不跟随，防逃逸）, got %d: %s", code, out)
		}
	})

	t.Run("配置文件为符号链接", func(t *testing.T) {
		cfgHome := t.TempDir()
		settingPath := mcpCfgSeedSetting(t, cfgHome)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(settingPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, settingPath); err != nil {
			t.Fatal(err)
		}
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
			mcpCfgApplyBody(cfgHome, cmdPath, "x", false))
		if code != 400 || !strings.Contains(out, "unsafe_path") {
			t.Fatalf("配置文件链接应 400 unsafe_path, got %d: %s", code, out)
		}
		if _, err := os.Lstat(target); err != nil {
			t.Fatalf("目标文件应未被触碰: %v", err)
		}
	})
}

// mcpCfgSeedSettingX 造 .zcode/cli/setting.json（返回错误而非 Fatal，供子测试用）。
func mcpCfgSeedSettingX(home string) error {
	dir := filepath.Join(home, ".zcode", "cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "setting.json"), []byte(`{"mcpServers":{}}`), 0o600)
}

// TestMcpCfgNoDirCreation 绝不创建宿主目录（.zcode 缺失 → config_file_missing，
// 目录零新增——primewire「宁可手贴不猜布局乱写」同判）。
func TestMcpCfgNoDirCreation(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir() // 只有根，无 .zcode
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, mcpCfgFakeCommand(t), "x", false))
	if code != 400 || !strings.Contains(out, "config_file_missing") {
		t.Fatalf("链缺失 apply 应 400 config_file_missing, got %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(cfgHome, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("绝不创建宿主目录被破坏: %v", err)
	}
}

// TestMcpCfgManualOnlyHosts 宿主支持矩阵（任务书：codex/未知宿主=手动模式不给自动
// 写；hub 目标不自动写；placeholder 凭据不落盘）——agentdetect 粗检口径如实。
func TestMcpCfgManualOnlyHosts(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cmdPath := mcpCfgFakeCommand(t)

	t.Run("codex_apply拒绝且文件不动", func(t *testing.T) {
		cfgHome := t.TempDir()
		dir := filepath.Join(cfgHome, ".codex")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		tomlPath := filepath.Join(dir, "config.toml")
		original := "[mcp_servers.other]\ncommand = \"/bin/other\"\n"
		if err := os.WriteFile(tomlPath, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		body := `{"scope":"node","host":"codex","config_home":"` + cfgHome +
			`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath + `","expected_version":"x"}`
		code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", body)
		if code != 400 || !strings.Contains(out, "manual_only") {
			t.Fatalf("codex apply 应 400 manual_only, got %d: %s", code, out)
		}
		if b, _ := os.ReadFile(tomlPath); string(b) != original {
			t.Fatalf("codex apply 拒绝后文件必须原样")
		}
		// codex preview：文件事实给足 + 粗检口径如实 + 不给自动写。
		pbody := `{"scope":"node","host":"codex","config_home":"` + cfgHome +
			`","server_url":"http://127.0.0.1:8445"}`
		pv, pout := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", pbody)
		if pv != 200 {
			t.Fatalf("codex preview 应 200, got %d: %s", pv, pout)
		}
		resp := mcpCfgDecode(t, pout)
		wr, _ := resp["writability"].(map[string]any)
		if wr["can_auto_write"] != false || wr["support_level"] != SupportLevelManualGenericCodex {
			t.Fatalf("codex 可写性口径不符: %v", wr)
		}
		if !strings.Contains(wr["note"].(string), "非语法校验") {
			t.Fatalf("codex preview 必须如实标注粗检口径（非语法校验）: %v", wr)
		}
		if resp["conflict_available"] != false || resp["diff"] != nil {
			t.Fatalf("codex 不解析 TOML：冲突/差异应缺省: %v", resp)
		}
		if file, _ := resp["file"].(map[string]any); file["exists"] != true || file["version"] == "" {
			t.Fatalf("codex preview 应给文件探测事实: %v", resp)
		}
	})

	t.Run("未知宿主_apply拒绝_preview不触文件", func(t *testing.T) {
		cfgHome := t.TempDir()
		body := `{"scope":"node","host":"claude-code","config_home":"` + cfgHome +
			`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath + `","expected_version":"x"}`
		if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", body); code != 400 || !strings.Contains(out, "manual_only") {
			t.Fatalf("未知宿主 apply 应 400 manual_only, got %d: %s", code, out)
		}
		pv, pout := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview",
			`{"scope":"node","host":"claude-code","config_home":"`+cfgHome+`","server_url":"http://127.0.0.1:8445"}`)
		if pv != 200 {
			t.Fatalf("未知宿主 preview 应 200, got %d: %s", pv, pout)
		}
		resp := mcpCfgDecode(t, pout)
		if _, has := resp["file"]; has {
			t.Fatalf("未知宿主 preview 不触文件（file 应缺省）: %v", resp)
		}
		if wr, _ := resp["writability"].(map[string]any); wr["can_auto_write"] != false {
			t.Fatalf("未知宿主不给自动写: %v", wr)
		}
	})

	t.Run("hub目标与placeholder凭据_apply拒绝", func(t *testing.T) {
		cfgHome := t.TempDir()
		hubBody := `{"scope":"hub","host":"zcode","config_home":"` + cfgHome +
			`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath + `","expected_version":"x"}`
		if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", hubBody); code != 400 || !strings.Contains(out, "manual_only") {
			t.Fatalf("hub 目标 apply 应 400 manual_only, got %d: %s", code, out)
		}
		phBody := `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
			`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath +
			`","credential_ref":"placeholder","expected_version":"x"}`
		if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", phBody); code != 400 || !strings.Contains(out, "manual_only") {
			t.Fatalf("placeholder 凭据 apply 应 400 manual_only, got %d: %s", code, out)
		}
	})
}

// TestMcpCfgVersionGate 文件版本门（任务书：预期版本防 TOCTOU——apply 前重读+版本
// 不符 409 file_changed；apply 必带 expected_version）。
func TestMcpCfgVersionGate(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	settingPath := mcpCfgSeedSetting(t, cfgHome)
	cmdPath := mcpCfgFakeCommand(t)

	// 未带 expected_version → 400 expected_version_required。
	body := `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + cmdPath + `"}`
	if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", body); code != 400 || !strings.Contains(out, "expected_version_required") {
		t.Fatalf("缺 expected_version 应 400, got %d: %s", code, out)
	}

	// 预览后文件被改 → apply 409 file_changed。
	pv, pout := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if pv != 200 {
		t.Fatalf("preview 应 200, got %d: %s", pv, pout)
	}
	version := mcpCfgDecode(t, pout)["file"].(map[string]any)["version"].(string)
	if err := os.WriteFile(settingPath, []byte(`{"mcpServers":{"newcomer":{"command":"/x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply",
		mcpCfgApplyBody(cfgHome, cmdPath, version, false))
	if code != 409 || !strings.Contains(out, "file_changed") {
		t.Fatalf("版本不符 apply 应 409 file_changed, got %d: %s", code, out)
	}

	// preview 携带过期版本 → 200 + version_matches=false（预览只读，如实标注）。
	stale := `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","expected_version":"` + version + `"}`
	pv, pout = doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", stale)
	if pv != 200 {
		t.Fatalf("preview 应 200, got %d: %s", pv, pout)
	}
	if mcpCfgDecode(t, pout)["version_matches"] != false {
		t.Fatalf("过期预期版本应 version_matches=false: %s", pout)
	}
}

// TestMcpCfgCommandPathGate command 探测门（不写占位配置——primewire R4 同口径）。
func TestMcpCfgCommandPathGate(t *testing.T) {
	srv, _ := newMcpCfgSrv(t)
	cfgHome := t.TempDir()
	mcpCfgSeedSetting(t, cfgHome)
	pv, pout := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/preview", mcpCfgPreviewBody(cfgHome))
	if pv != 200 {
		t.Fatalf("preview 应 200, got %d: %s", pv, pout)
	}
	version := mcpCfgDecode(t, pout)["file"].(map[string]any)["version"].(string)

	missing := `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","mcp_command_path":"` + mcpCfgPlaceholderCmd + `","expected_version":"` + version + `"}`
	if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", missing); code != 400 || !strings.Contains(out, "command_missing") {
		t.Fatalf("command 不存在应 400 command_missing, got %d: %s", code, out)
	}
	relative := `{"scope":"node","host":"zcode","config_home":"` + cfgHome +
		`","server_url":"http://127.0.0.1:8445","mcp_command_path":"rel/mcp","expected_version":"` + version + `"}`
	if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/mcp/config/apply", relative); code != 400 || !strings.Contains(out, "invalid_request") {
		t.Fatalf("相对路径 command 应 400 invalid_request, got %d: %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(cfgHome, ".zcode", "cli", "setting.json")); strings.Contains(string(b), "swarmmind-node") {
		t.Fatalf("command 门拒绝后文件不得被改写")
	}
}
