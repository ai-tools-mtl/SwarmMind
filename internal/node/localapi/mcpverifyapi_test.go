package localapi

// MCP 分层验证板端点接线（W03-04，docs/tasks/v3/W03-04-rfc.md §1）fixture 单测：
// 鉴权矩阵（authLocal：prime_token 一律 401）、五端点契约（GET 快照/v0 检查与重锚/
// v2 恒标服务端视角/confirm 绑定版本与旧回执不点亮新配置/config-version 重锚回落）、
// 轮换漂移检测（RotateSecret 零侵入交接面）、零明文金丝（假密钥完整形态不得出现在
// 任何应答）、零写盘断言（DataDir 全程无新文件）。
//
// 纪律：config_home/data_dir 全部注入 t.TempDir，绝不写真实 HOME；测试密钥全为假值。
// v2 探测注入不可达地址（127.0.0.1 保留端口）走连接失败分支，零外部依赖。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// mcpVerifyAPISrv 五端点测试环境：最小装配（不依赖 Service/Ledger），PrimeToken 注入
// 假凭据（v0 凭据项/v2 探测/漂移检测的数据源）。
func mcpVerifyAPISrv(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Config{
		NodeID:     "nodeA",
		LocalToken: "tok-local",
		PrimeToken: verifyFakeCred,
		DataDir:    t.TempDir(),
	}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// mcpVerifyDo 带 Bearer 的 JSON POST/GET 助手；返回解码后的 map。
func mcpVerifyDo(t *testing.T, srv *httptest.Server, method, path, tok string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("应答须为 JSON: %v", err)
	}
	return resp.StatusCode, out
}

// v0ReadyBody V0 齐备输入（zcode fixture 在位 + 假 command 探测存在）。
func v0ReadyBody(home, cmd string) map[string]any {
	return map[string]any{
		"scope": "node", "caller_env": "same_machine", "host": "zcode",
		"server_url": "http://127.0.0.1:8445", "mcp_command_path": cmd,
		"config_name": "swarmmind-node", "config_home": home,
	}
}

// boardLayers 取板面 layers 数组并按 layer 键索引。
func boardLayers(t *testing.T, resp map[string]any) map[string]map[string]any {
	t.Helper()
	board, _ := resp["board"].(map[string]any)
	layers, _ := board["layers"].([]any)
	out := map[string]map[string]any{}
	for _, l := range layers {
		m, ok := l.(map[string]any)
		if !ok {
			t.Fatalf("layers 元素须为对象: %v", l)
		}
		out[m["layer"].(string)] = m
	}
	if len(out) != 6 {
		t.Fatalf("板面须含六层投影，got %d", len(out))
	}
	return out
}

// TestMcpVerifyAuthMatrix 五端点鉴权：prime_token 一律 401（主人管理面，豁免⑮）；
// local_token 放行；无令牌 401。
func TestMcpVerifyAuthMatrix(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/local/mcp/verify", nil},
		{"POST", "/api/v1/local/mcp/verify/v0", v0ReadyBody(t.TempDir(), "")},
		{"POST", "/api/v1/local/mcp/verify/v2", map[string]any{"scope": "node", "service_url": "http://127.0.0.1:8445"}},
		{"POST", "/api/v1/local/mcp/verify/confirm", map[string]any{"layer": "v1", "config_version": "x"}},
		{"POST", "/api/v1/local/mcp/verify/config-version", map[string]any{"config_version": "x"}},
	}
	for _, c := range cases {
		for _, tc := range []struct {
			name, tok string
			want      int
		}{
			{"prime_token 一律 401", "fake-prime-not-local", 401},
			{"无令牌 401", "", 401},
			{"local_token 放行", "tok-local", 0},
		} {
			code, _ := mcpVerifyDo(t, srv, c.method, c.path, tc.tok, c.body)
			if tc.want == 0 {
				if code == 401 {
					t.Errorf("%s %s(%s): 不应 401", c.method, c.path, tc.name)
				}
				continue
			}
			if code != tc.want {
				t.Errorf("%s %s(%s): got %d want %d", c.method, c.path, tc.name, code, tc.want)
			}
		}
	}
}

// TestMcpVerifyGetBoard GET 快照契约：六层缺省 not_checked + 指引六键 + 凭据脱敏 +
// 秘密占位态；零明文金丝；mcp_access_enabled 为布尔。
func TestMcpVerifyGetBoard(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	code, resp := mcpVerifyDo(t, srv, "GET", "/api/v1/local/mcp/verify", "tok-local", nil)
	if code != 200 {
		t.Fatalf("GET 应 200，got %d: %v", code, resp)
	}
	if cm, _ := resp["credential_masked"].(string); cm == "" || strings.Contains(cm, verifyFakeCred) {
		t.Fatalf("credential_masked 须为脱敏引用且不含明文，got %q", cm)
	}
	if v, _ := resp["credential_env_var"].(string); v != envNodeToken {
		t.Fatalf("credential_env_var = %v，want %s", v, envNodeToken)
	}
	if _, ok := resp["mcp_access_enabled"].(bool); !ok {
		t.Fatalf("mcp_access_enabled 须为布尔，got %T", resp["mcp_access_enabled"])
	}
	guidance, _ := resp["guidance"].(map[string]any)
	if len(guidance) != 6 {
		t.Fatalf("guidance 须含六层，got %d", len(guidance))
	}
	layers := boardLayers(t, resp)
	for _, l := range allLayers {
		if layers[l]["status"] != VerifyStatusNotChecked {
			t.Fatalf("初始板面 %s 须 not_checked，got %v", l, layers[l]["status"])
		}
	}
	secret, _ := resp["board"].(map[string]any)["secret"].(map[string]any)
	if secret["state"] != SecretStatePlaceholder {
		t.Fatalf("初始秘密态须 placeholder，got %v", secret["state"])
	}
	// 零明文金丝。
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), verifyFakeCred) {
		t.Fatal("GET 应答含假密钥明文（零明文金丝命中）")
	}
	// 禁词金丝。
	verifyNoExternalClaim(t, "GET 应答", string(raw))
}

// TestMcpVerifyV0Contract v0 全链：齐备输入 pass + 锚=文件版本 hash + 秘密转
// user_provided；配置失配输入 fail；缺 config_home 的 hub 范围照常运行（宿主配置
// 在调用机上）。
func TestMcpVerifyV0Contract(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	home := t.TempDir()
	raw := writeZcodeFixture(t, home, "swarmmind-node", zcodeFixtureServers())
	cmd := mcpCfgFakeCommand(t)

	code, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, cmd))
	if code != 200 {
		t.Fatalf("v0 应 200，got %d: %v", code, resp)
	}
	check, _ := resp["check"].(map[string]any)
	if check["status"] != VerifyStatusPass {
		t.Fatalf("齐备输入 V0 应 pass，got %v: %v", check["status"], check)
	}
	if check["config_version"] != mcpCfgVersionHash(raw) {
		t.Fatalf("V0 ConfigVersion 应为文件版本 hash")
	}
	layers := boardLayers(t, resp)
	if layers[LayerV0]["status"] != VerifyStatusPass {
		t.Fatalf("板面 V0 应 pass，got %v", layers[LayerV0]["status"])
	}
	if layers[LayerV0]["checked_by"] != VerifySourceServerSideView {
		t.Fatalf("V0 CheckedBy 恒 server_side_view，got %v", layers[LayerV0]["checked_by"])
	}
	if layers[LayerV3]["status"] != VerifyStatusNotChecked {
		t.Fatal("V0 pass 绝不推断 V3（页面默认只保证 V0）")
	}
	secret, _ := resp["board"].(map[string]any)["secret"].(map[string]any)
	if secret["state"] != SecretStateUserProvided || secret["ref"] != maskCredential(verifyFakeCred) {
		t.Fatalf("凭据在位应 MarkSecretProvided（user_provided+脱敏引用），got %v", secret)
	}

	// 非法 scope → 400；hub 范围（无 config_home）→ 200 且 host_file 未检不阻塞。
	if code, _ := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", map[string]any{"scope": "galaxy", "caller_env": "same_machine", "server_url": "http://127.0.0.1:8445"}); code != 400 {
		t.Fatalf("scope 非法应 400，got %d", code)
	}
	code, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", map[string]any{
		"scope": "hub", "caller_env": "remote", "host": "zcode",
		"server_url": "https://hub.example:8443", "mcp_command_path": cmd,
		"config_home": home, // 请求带了也会被强制置空（宿主配置在调用机上）
	})
	if code != 200 {
		t.Fatalf("hub 范围 v0 应 200，got %d: %v", code, resp)
	}
	check, _ = resp["check"].(map[string]any)
	if check["status"] != VerifyStatusFail {
		t.Fatalf("hub 范围凭据占位应 fail（缺凭据），got %v", check["status"])
	}
	for _, it := range check["checks"].([]any) {
		m := it.(map[string]any)
		if m["key"] == V0KeyHostFile && m["status"] != VerifyStatusNotChecked {
			t.Fatalf("hub 范围 host_file 须未检（config_home 强制置空），got %v", m)
		}
	}
	// 零明文金丝（含宿主配置内预埋的旧值）。
	rb, _ := json.Marshal(resp)
	if strings.Contains(string(rb), verifyFakeCred) || strings.Contains(string(rb), "stale-old-value") {
		t.Fatal("v0 应答含明文凭据（金丝命中）")
	}
}

// TestMcpVerifyReanchorOnApplyAndV0 apply/config-version/V0 三路重锚：版本锚变更后
// 旧 user_confirmed 如实回落 not_checked（历史保留），AC10 语义。
func TestMcpVerifyReanchorOnApplyAndV0(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	home := t.TempDir()
	raw := writeZcodeFixture(t, home, "swarmmind-node", zcodeFixtureServers())
	cmd := mcpCfgFakeCommand(t)

	// 先跑 V0 定锚（=文件版本 hash）。
	_, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, cmd))
	check, _ := resp["check"].(map[string]any)
	ver, _ := check["config_version"].(string)
	if ver == "" {
		t.Fatal("V0 应产出文件版本锚")
	}
	// 用户确认 V3（绑定当前锚 → 点亮）。
	code, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
		map[string]any{"layer": "v3", "config_version": ver})
	if code != 200 {
		t.Fatalf("confirm 应 200，got %d: %v", code, resp)
	}
	layers := boardLayers(t, resp)
	if layers[LayerV3]["status"] != VerifyStatusUserConfirmed {
		t.Fatalf("绑定当前锚的确认应点亮 user_confirmed，got %v", layers[LayerV3])
	}

	// 模拟 apply 后版本推进：config-version 端点携新 hash → 旧确认回落 not_checked。
	newVer := mcpCfgVersionHash([]byte("next-version-bytes"))
	code, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/config-version", "tok-local",
		map[string]any{"config_version": newVer})
	if code != 200 {
		t.Fatalf("config-version 应 200，got %d: %v", code, resp)
	}
	layers = boardLayers(t, resp)
	if layers[LayerV3]["status"] != VerifyStatusNotChecked {
		t.Fatalf("版本锚变更后旧确认须回落 not_checked（旧回执不点亮新配置），got %v", layers[LayerV3])
	}
	if !strings.Contains(layers[LayerV3]["detail"].(string), "旧配置版本") {
		t.Fatalf("回落须注明旧版本原因，got %v", layers[LayerV3]["detail"])
	}
	// 旧版本重放确认 → 不点亮（AC10）。
	if _, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
		map[string]any{"layer": "v3", "config_version": ver}); boardLayers(t, resp)[LayerV3]["status"] != VerifyStatusNotChecked {
		t.Fatal("旧版本确认不得点亮新配置")
	}
	// 新版本确认 → 点亮。
	if _, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
		map[string]any{"layer": "v3", "config_version": newVer}); boardLayers(t, resp)[LayerV3]["status"] != VerifyStatusUserConfirmed {
		t.Fatal("新版本确认应点亮")
	}
	// V0 重跑读到旧文件 → 锚拉回旧 hash → 上一条新锚确认回落。
	_ = raw
	_, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, cmd))
	layers = boardLayers(t, resp)
	if layers[LayerV3]["status"] != VerifyStatusNotChecked {
		t.Fatalf("V0 重锚后旧锚确认须回落，got %v", layers[LayerV3])
	}
	// config-version 空锚 → 400。
	if code, _ = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/config-version", "tok-local",
		map[string]any{"config_version": "  "}); code != 400 {
		t.Fatalf("空锚应 400，got %d", code)
	}
}

// TestMcpVerifyConfirmGuards confirm 模型守卫透传：v0/v2 不收用户确认；疑似明文
// note 拒收；普通语句不误拒。
func TestMcpVerifyConfirmGuards(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	for _, layer := range []string{"v0", "v2"} {
		if code, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
			map[string]any{"layer": layer, "config_version": "x"}); code != 400 {
			t.Fatalf("层 %s 不收用户确认（应 400），got %d: %v", layer, code, resp)
		}
	}
	if code, _ := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
		map[string]any{"layer": "v1", "config_version": "x", "note": "token " + verifyFakeCred}); code != 400 {
		t.Fatal("疑似明文 note 应拒收（noteSuspectedPlaintext 防线）")
	}
	if code, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
		map[string]any{"layer": "v1", "config_version": "x", "note": "宿主显示工具列表正常，八个工具都在。"}); code != 200 {
		t.Fatalf("普通语句不误拒（应 200），got %d: %v", code, resp)
	}
}

// TestMcpVerifyV2ServerSideView v2：200 pass 恒标 server_side_view（不冒认外部）；
// hub 范围 400；凭据未生成 400；连接失败 → not_checked（V1 语义）；输出面禁词金丝。
func TestMcpVerifyV2ServerSideView(t *testing.T) {
	srv := mcpVerifyAPISrv(t)
	// 127.0.0.1:1 保留端口：连接拒绝 → not_checked（零外部依赖）。
	code, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v2", "tok-local",
		map[string]any{"scope": "node", "service_url": "http://127.0.0.1:1"})
	if code != 200 {
		t.Fatalf("v2 应 200（结果在 body），got %d: %v", code, resp)
	}
	check, _ := resp["check"].(map[string]any)
	if check["status"] != VerifyStatusNotChecked || check["checked_by"] != VerifySourceServerSideView {
		t.Fatalf("连接失败 v2 须 not_checked + server_side_view，got %v", check)
	}
	layers := boardLayers(t, resp)
	if layers[LayerV2]["status"] != VerifyStatusNotChecked {
		t.Fatal("板面 V2 应 not_checked（连接失败不对授权下结论）")
	}
	// hub 范围拒绝；非法 scope 拒绝；非法 URL 拒绝。
	if code, _ = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v2", "tok-local",
		map[string]any{"scope": "hub", "service_url": "http://127.0.0.1:8445"}); code != 400 {
		t.Fatal("hub 范围 v2 应 400（本端不持有 PAT）")
	}
	if code, _ = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v2", "tok-local",
		map[string]any{"scope": "node", "service_url": "ftp://x"}); code != 400 {
		t.Fatal("非法 URL 应 400")
	}
	// 凭据未生成 → 400。
	srv2 := httptest.NewServer(New(Config{NodeID: "nodeA", LocalToken: "tok-local"}).Handler())
	defer srv2.Close()
	if code, resp = mcpVerifyDo(t, srv2, "POST", "/api/v1/local/mcp/verify/v2", "tok-local",
		map[string]any{"scope": "node", "service_url": "http://127.0.0.1:8445"}); code != 400 {
		t.Fatalf("凭据未生成 v2 应 400，got %d: %v", code, resp)
	}
	// 禁词金丝（v2 应答）。
	rb, _ := json.Marshal(resp)
	verifyNoExternalClaim(t, "v2 应答", string(rb))
}

// TestMcpVerifyRotateDrift 轮换漂移检测（RFC §1 零侵入交接面）：凭据换值后 GET 板面
// 即 RotateSecret——凭据相关层（v2/v3/v4/v5）失效回落 not_checked，v0/v1 不动，历史
// 保留 invalidated 留痕；秘密状态机转 rotated_invalid。
func TestMcpVerifyRotateDrift(t *testing.T) {
	// 直构 Server（同包可持引用）：轮换在生产由 rotate 端点同锁调 SetPrimeToken，
	// 本用例直接调用该公开方法推进凭据（不挂 rotate 端点——接线零侵入口径）。
	srvReal := New(Config{NodeID: "nodeC", LocalToken: "tok-local", PrimeToken: verifyFakeCred, DataDir: t.TempDir()})
	srv := wrapSrv(t, srvReal)
	home := t.TempDir()
	writeZcodeFixture(t, home, "swarmmind-node", zcodeFixtureServers())

	// V0 定锚 + 凭据就位；确认 V3/V4 点亮。
	_, resp := mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, mcpCfgFakeCommand(t)))
	ver, _ := resp["check"].(map[string]any)["config_version"].(string)
	for _, l := range []string{"v3", "v4"} {
		if _, resp = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local",
			map[string]any{"layer": l, "config_version": ver}); boardLayers(t, resp)[l]["status"] != VerifyStatusUserConfirmed {
			t.Fatalf("%s 确认应点亮", l)
		}
	}
	srvReal.SetPrimeToken("faketest-rotated-new-cred-DO-NOT-LEAK-xyz") // 生产由 rotate 端点同锁调用
	code, resp := mcpVerifyDo(t, srv, "GET", "/api/v1/local/mcp/verify", "tok-local", nil)
	if code != 200 {
		t.Fatalf("GET 应 200，got %d", code)
	}
	layers := boardLayers(t, resp)
	for _, l := range []string{LayerV2, LayerV3, LayerV4, LayerV5} {
		if layers[l]["status"] != VerifyStatusNotChecked {
			t.Fatalf("轮换后 %s 须回落 not_checked，got %v", l, layers[l])
		}
	}
	if layers[LayerV0]["status"] != VerifyStatusPass {
		t.Fatal("轮换不动 v0（配置语法事实不依赖凭据有效性）")
	}
	secret, _ := resp["board"].(map[string]any)["secret"].(map[string]any)
	if secret["state"] != SecretStateRotatedInvalid {
		t.Fatalf("秘密状态机应转 rotated_invalid，got %v", secret)
	}
	history, _ := resp["board"].(map[string]any)["history"].([]any)
	found := false
	for _, h := range history {
		if m, _ := h.(map[string]any); m["result"] == verifyResultInvalidated {
			found = true
		}
	}
	if !found {
		t.Fatal("历史须保留 invalidated 留痕")
	}
	// 轮换后凭据引用与旧脱敏引用不一致 → 再跑 V0 重新就位（新版本绑定）。
	_, resp = mcpVerifyDo(t, wrapSrv(t, srvReal), "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, mcpCfgFakeCommand(t)))
	secret, _ = resp["board"].(map[string]any)["secret"].(map[string]any)
	if secret["state"] != SecretStateUserProvided {
		t.Fatalf("V0 重跑应重新就位 user_provided，got %v", secret)
	}
}

// wrapSrv 用 httptest 包装直构 Server（漂移用例需持 Server 引用做 SetPrimeToken）。
func wrapSrv(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestMcpVerifyZeroDisk 五端点零写盘：DataDir 全程无新文件（板面进程内存态）。
func TestMcpVerifyZeroDisk(t *testing.T) {
	dataDir := t.TempDir()
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", LocalToken: "tok-local", PrimeToken: verifyFakeCred, DataDir: dataDir,
	}).Handler())
	t.Cleanup(srv.Close)
	home := t.TempDir()
	writeZcodeFixture(t, home, "swarmmind-node", zcodeFixtureServers())
	_, _ = mcpVerifyDo(t, srv, "GET", "/api/v1/local/mcp/verify", "tok-local", nil)
	_, _ = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/v0", "tok-local", v0ReadyBody(home, mcpCfgFakeCommand(t)))
	_, _ = mcpVerifyDo(t, srv, "POST", "/api/v1/local/mcp/verify/confirm", "tok-local", map[string]any{"layer": "v1", "config_version": "x"})
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("五端点零写盘：DataDir 出现 %v", ents)
	}
}
