// W06-02 本机 agent 发现 / 登记预览 / 检测投影测试：全 fake 探测目标（--version
// 假脚本 + t.TempDir 宿主布局），绝不调用真实 codex/zcode、绝无网络、绝不触碰
// 真实 HOME。零扫描、authLocal 门、内容金丝（配置正文永不进结果）、差异三态、
// 全权档位预览即拒、无第二写路径（grep 断言）、投影四值与旧字段零变化（键集
// 快照）逐项覆盖。fixture 的 codex config.toml 一律空格形态（`key = "value"`，
// 见报告「契约修订建议」：agentdetect 粗检对 `=` 两侧空格敏感，紧凑写法保守
// fail，本包不改检测包、如实按其口径出题）。
package localapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// leakCanary 配置正文金丝：setting.json 假凭证值——任何响应出现它即泄漏（内容
// 永不进结果，W06-01 防泄漏红线在接线面的延续）。
const leakCanary = "FAKE-SECRET-w06-02-discover-leak-canary-73"

// writeFakeCLI 造临时可执行 --version 假脚本（同 agentdetect 测试纪律）。
func writeFakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("写假脚本失败: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	return path
}

// makeZcodeHomeFixture 造 zcode 配置 home（合法 setting.json + 身份键）。
func makeZcodeHomeFixture(t *testing.T, settingJSON string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".zcode", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zcode", "cli", "setting.json"), []byte(settingJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// newDiscoverServer 构造挂 tempdir 配置的 localapi 测试服务器（ConfigOps 接线 =
// 既有保存链在位；本地令牌 tok-local / 总理 tok-prime）。
func newDiscoverServer(t *testing.T, dir string, profiles []protocol.ExecProfile) (*httptest.Server, string) {
	t.Helper()
	cfgPath := filepath.Join(dir, "node.yaml")
	initial := "# 测试配置\nnode_id: nodeA\n"
	if err := writeConfigFile(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	ops := &ConfigOps{
		ConfigPath: cfgPath, Supervised: true, RestartCmd: "restart-cmd",
		ReloadExecutors: func([]protocol.ExecProfile) error { return nil },
	}
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Mode: ModeStandalone,
		Agents:     newState(t, t.TempDir(), profiles),
		LocalToken: "tok-local", PrimeToken: "tok-prime",
		ConfigOps: ops,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, cfgPath
}

// postLocal 带 Bearer 的 POST 辅助；解码 JSON 应答为 map。
func postLocal(t *testing.T, srv *httptest.Server, token, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// layerStatusMap 取 report 内单层状态（status/reason 字符串化断言用）。
func layerStatusMap(t *testing.T, report map[string]any, layer string) (string, string) {
	t.Helper()
	lc, ok := report[layer].(map[string]any)
	if !ok {
		t.Fatalf("report 缺 %s 层: %v", layer, report)
	}
	st, _ := lc["status"].(string)
	reason, _ := lc["reason"].(string)
	return st, reason
}

// asReport 规范化应答里的 report 对象。
func asReport(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("report 应为对象, got %T", v)
	}
	return m
}

// changedFieldsOf 取差异 changed_fields（omitempty 缺省 = 空表；nil 安全）。
func changedFieldsOf(t *testing.T, diff map[string]any) []any {
	t.Helper()
	raw, ok := diff["changed_fields"]
	if !ok || raw == nil {
		return nil
	}
	fields, ok := raw.([]any)
	if !ok {
		t.Fatalf("changed_fields 应为数组: %v", raw)
	}
	return fields
}

// TestAgentsDiscoverFourLayers 显式候选全链：fake CLI --version 过 + zcode home
// 身份键发现 → 四层如实（execution_ready pass 必带「模型未验证」注记，绝不合并
// 绿灯）；未登记候选 call_allowed=fail（发现≠登记）；配置正文金丝绝不入响应；
// 零候选 = 零扫描（空结果 + 注记）。
func TestAgentsDiscoverFourLayers(t *testing.T) {
	cmd := writeFakeCLI(t, t.TempDir(), "fake-zcode", `echo "zcode runtime 0.17.0"`)
	home := makeZcodeHomeFixture(t, `{"authToken":"`+leakCanary+`","memo":"leak-canary-content"}`)
	srv, _ := newDiscoverServer(t, t.TempDir(), nil)

	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":"`+home+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("discover 应 200, got %d: %v", code, out)
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("应恰一候选结果: %v", out)
	}
	res := results[0].(map[string]any)
	if res["registered"] != false || res["registered_by"] != nil {
		t.Fatalf("未登记候选 registered 应 false: %v", res)
	}
	rep := asReport(t, res["report"])
	if st, _ := layerStatusMap(t, rep, "installed"); st != "pass" {
		t.Fatalf("安装层应 pass（fake CLI --version 退出码 0）, got %v", rep["installed"])
	}
	if st, _ := layerStatusMap(t, rep, "config_recognized"); st != "pass" {
		t.Fatalf("配置识别层应 pass, got %v", rep["config_recognized"])
	}
	st, reason := layerStatusMap(t, rep, "execution_ready")
	if st != "pass" || !strings.Contains(reason, "模型未验证") {
		t.Fatalf("执行就绪 pass 必带「模型未验证」注记, got %s %q", st, reason)
	}
	st, _ = layerStatusMap(t, rep, "call_allowed")
	if st != "fail" {
		t.Fatalf("未登记候选 call_allowed 应 fail（发现≠登记）, got %s", st)
	}
	// 两开关当前值与差异说明
	lc := res["local_call"].(map[string]any)
	if lc["on"] != false || lc["source"] != "not_registered" {
		t.Fatalf("未登记 local_call 应 false/not_registered: %v", lc)
	}
	if lc["note"] == "" {
		t.Fatalf("local_call 必带差异说明: %v", lc)
	}
	ex := res["exposed"].(map[string]any)
	if ex["on"] != false || ex["note"] == "" {
		t.Fatalf("未登记 exposed 应 false 且带说明: %v", ex)
	}
	// 内容金丝：配置正文（假凭证值）任何情况下不入响应
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), leakCanary) || strings.Contains(string(raw), "leak-canary-content") {
		t.Fatalf("响应泄漏配置内容（金丝命中）: %s", raw)
	}
	// 版本输出原样留痕在位（元数据允许）
	inst := rep["installed"].(map[string]any)
	if vr, _ := inst["version_raw"].(string); !strings.Contains(vr, "zcode runtime 0.17.0") {
		t.Fatalf("版本输出应原样留痕: %v", inst)
	}

	// 零候选：200 + 空结果 + 零扫描注记
	code, out = postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover", `{"candidates":[]}`)
	if code != http.StatusOK {
		t.Fatalf("零候选应 200, got %d: %v", code, out)
	}
	if results, _ = out["results"].([]any); len(results) != 0 {
		t.Fatalf("零候选应空结果: %v", out)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "零扫描") {
		t.Fatalf("零候选应带零扫描注记: %v", out["note"])
	}
}

// TestAgentsDiscoverNotCheckedCandidate 双缺候选（无命令无 config_home）= 零扫描：
// 三个扫描层全 not_checked + 原因，绝不推断；call_allowed 例外——它是当前授权态
// 投影（agentdetect 纯透传，不依赖扫描）：未登记候选即「不允许」，如实 fail，
// 不是「未检测」。
func TestAgentsDiscoverNotCheckedCandidate(t *testing.T) {
	srv, _ := newDiscoverServer(t, t.TempDir(), nil)
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"ghost","host":"zcode"}]}`)
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d: %v", code, out)
	}
	res := out["results"].([]any)[0].(map[string]any)
	rep := asReport(t, res["report"])
	for _, layer := range []string{"installed", "config_recognized", "execution_ready"} {
		st, reason := layerStatusMap(t, rep, layer)
		if st != "not_checked" || reason == "" {
			t.Fatalf("零扫描候选 %s 应 not_checked+原因, got %s %q", layer, st, reason)
		}
	}
	st, reason := layerStatusMap(t, rep, "call_allowed")
	if st != "fail" || !strings.Contains(reason, "未登记") {
		t.Fatalf("未登记候选 call_allowed 应 fail+未登记原因（授权态投影，非扫描层）, got %s %q", st, reason)
	}
}

// TestAgentsDiscoverAuthGate authLocal 门：无令牌 / prime_token 一律 401（主人
// 专属面，编排凭证不放行——SPEC 六.六豁免⑫ 口径）；local_token 放行。
func TestAgentsDiscoverAuthGate(t *testing.T) {
	srv, _ := newDiscoverServer(t, t.TempDir(), nil)
	for _, tc := range []struct {
		tok  string
		want int
	}{
		{"", http.StatusUnauthorized}, {"wrong", http.StatusUnauthorized},
		{"tok-prime", http.StatusUnauthorized}, {"tok-local", http.StatusOK},
	} {
		code, out := postLocal(t, srv, tc.tok, "/api/v1/local/agents/discover", `{"candidates":[]}`)
		if code != tc.want {
			t.Fatalf("discover 令牌 %q 应 %d, got %d: %v", tc.tok, tc.want, code, out)
		}
		code, out = postLocal(t, srv, tc.tok, "/api/v1/local/agents/registration-preview",
			`{"agent":{"name":"x","command":"/bin/echo","workdir":"/tmp","timeout_sec":10}}`)
		if code != tc.want {
			t.Fatalf("preview 令牌 %q 应 %d, got %d: %v", tc.tok, tc.want, code, out)
		}
	}
	// 候选超上限护栏
	var sb strings.Builder
	sb.WriteString(`{"candidates":[`)
	for i := 0; i < discoverMaxCandidates+1; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name":"c%d"}`, i)
	}
	sb.WriteString(`]}`)
	code, _ := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover", sb.String())
	if code != http.StatusBadRequest {
		t.Fatalf("候选超上限应 400, got %d", code)
	}
}

// TestRegistrationPreviewDiffThreeStates 差异三态：无变化 / 修改（逐字段 old→new）/
// 新增；登记态命中（name 优先）后 call_allowed 投影为允许；检测段四层在位。
func TestRegistrationPreviewDiffThreeStates(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := protocol.ExecProfile{
		Name: "alpha", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir,
		TimeoutSec: 600, Exposed: true, Description: "旧描述", PermissionMode: "no-shell",
	}
	srv, cfgPath := newDiscoverServer(t, dir, []protocol.ExecProfile{base})
	preview := func(t *testing.T, body string) (int, map[string]any) {
		t.Helper()
		return postLocal(t, srv, "tok-local", "/api/v1/local/agents/registration-preview", body)
	}
	agentJSON := func(name, desc string, timeout int) string {
		return `{"agent":{"name":"` + name + `","kind":"cli","command":"/bin/echo","workdir":"` + workdir +
			`","timeout_sec":` + fmt.Sprint(timeout) + `,"description":"` + desc + `","permission_mode":"no-shell","exposed":true}}`
	}

	// ① 无变化：同字段登记面 → kind=none（changed_fields omitempty 缺省 = 空）
	code, out := preview(t, agentJSON("alpha", "旧描述", 600))
	if code != http.StatusOK {
		t.Fatalf("preview 应 200, got %d: %v", code, out)
	}
	diff := out["diff"].(map[string]any)
	if fields := changedFieldsOf(t, diff); diff["kind"] != "none" || len(fields) != 0 {
		t.Fatalf("同字段应无变化: %v", diff)
	}

	// ② 修改：description + timeout_sec 变更 → kind=update 携逐字段 old→new
	code, out = preview(t, agentJSON("alpha", "新描述", 300))
	if code != http.StatusOK {
		t.Fatalf("preview 应 200, got %d: %v", code, out)
	}
	diff = out["diff"].(map[string]any)
	if diff["kind"] != "update" {
		t.Fatalf("字段变更应 update: %v", diff)
	}
	got := map[string]map[string]any{}
	for _, f := range changedFieldsOf(t, diff) {
		fm := f.(map[string]any)
		got[fm["field"].(string)] = fm
	}
	if len(got) != 2 || got["description"] == nil || got["description"]["old"] != "旧描述" ||
		got["description"]["new"] != "新描述" || got["timeout_sec"]["old"] != "600" || got["timeout_sec"]["new"] != "300" {
		t.Fatalf("逐字段差异不符: %v", diff["changed_fields"])
	}
	if out["registered"] != true {
		t.Fatalf("已登记名应 registered=true: %v", out)
	}

	// ③ 新增：未登记名 → kind=create + 结构性提示（保存后需重启生效）
	code, out = preview(t, agentJSON("beta", "新能力", 600))
	if code != http.StatusOK {
		t.Fatalf("preview 应 200, got %d: %v", code, out)
	}
	if diff = out["diff"].(map[string]any); diff["kind"] != "create" {
		t.Fatalf("未登记名应 create: %v", diff)
	}
	structural, _ := out["structural"].([]any)
	if len(structural) == 0 || !strings.Contains(fmt.Sprint(structural[0]), "beta") {
		t.Fatalf("新增应带结构性提示: %v", out["structural"])
	}

	// 检测段四层在位（/bin/echo 可跑 --version；host 缺省按未知宿主如实）
	rep := asReport(t, out["detect"])
	for _, layer := range []string{"installed", "config_recognized", "execution_ready", "call_allowed"} {
		if _, ok := rep[layer]; !ok {
			t.Fatalf("detect 缺 %s 层: %v", layer, rep)
		}
	}

	// 预览零写盘：node.yaml 字节不动
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "# 测试配置\nnode_id: nodeA\n" {
		t.Fatalf("预览后 node.yaml 应原样: %q", after)
	}
}

// TestRegistrationPreviewRejects 全权档位预览即拒（yolo / danger-full-access，
// 与 sessions 同值防线）+ 非法登记面被同源校验拒绝（消息与保存链同源）。
func TestRegistrationPreviewRejects(t *testing.T) {
	srv, _ := newDiscoverServer(t, t.TempDir(), nil)
	for _, mode := range []string{"yolo", "danger-full-access"} {
		body := `{"agent":{"name":"x","command":"/bin/echo","workdir":"/tmp","timeout_sec":10,"permission_mode":"` + mode + `"}}`
		code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/registration-preview", body)
		if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["message"]), "全权档位") {
			t.Fatalf("全权档位 %s 预览即拒, got %d: %v", mode, code, out)
		}
	}
	// 相对路径 command：保存链 ValidateAgentProfiles 同源拒绝
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/registration-preview",
		`{"agent":{"name":"x","command":"echo","workdir":"/tmp","timeout_sec":10}}`)
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["message"]), "command 必须为绝对路径") {
		t.Fatalf("非法登记面应同源拒绝, got %d: %v", code, out)
	}
}

// TestNoSecondWritePathInDiscover 保存链唯一性（grep 断言）：发现/预览文件不出现
// 任何写盘原语——登记保存只经既有 PUT /api/v1/local/config 链（零双写纪律）。
func TestNoSecondWritePathInDiscover(t *testing.T) {
	b, err := os.ReadFile("agentdiscover.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, ban := range []string{
		"os.WriteFile", "os.Rename", "writeConfigFile", "writeConfigFileLocked",
		"updateAgentsYAML", "MkdirAll", "persist",
	} {
		if strings.Contains(src, ban) {
			t.Errorf("agentdiscover.go 不应含写盘原语 %q（登记只经既有 config PUT，无第二写路径）", ban)
		}
	}
}

// TestRegistrationAdapterPersists 登记面「适配器」落盘（W06-02）：预览确认后的
// 登记面经既有 config PUT 保存，session_adapter 必须落 yaml（此前它是未知键——
// 更新被保留、新增永不落盘，登记面写适配器会静默丢失）；再预览时差异回 none。
func TestRegistrationAdapterPersists(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, cfgPath := newDiscoverServer(t, dir, nil)
	// 既有保存链 PUT：带 session_adapter 与 config_home 的登记面
	body := `{"agents":[{"name":"mycodex","kind":"cli","command":"/bin/echo","workdir":"` + workdir +
		`","timeout_sec":600,"session_adapter":"codex","config_home":"` + dir + `"}]}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/local/config", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-local")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT 应 200: %d", res.StatusCode)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "session_adapter: codex") {
		t.Fatalf("session_adapter 应落盘:\n%s", raw)
	}
	// 再预览同登记面：差异应 none（round-trip 闭合）
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/registration-preview",
		`{"agent":{"name":"mycodex","kind":"cli","command":"/bin/echo","workdir":"`+workdir+
			`","timeout_sec":600,"session_adapter":"codex","config_home":"`+dir+`"}}`)
	if code != http.StatusOK {
		t.Fatalf("preview 应 200: %v", out)
	}
	if diff := out["diff"].(map[string]any); diff["kind"] != "none" {
		t.Fatalf("round-trip 后再预览应无变化: %v", diff)
	}
	// 检测宿主按适配器推导（未传 detect_host）：codex 布局识别层应真实探测
	// （home 下无 .codex → fail 且原因带 codex 布局段 .codex，绝不冒认）
	rep := asReport(t, out["detect"])
	if st, reason := layerStatusMap(t, rep, "config_recognized"); st != "fail" || !strings.Contains(reason, ".codex") {
		t.Fatalf("adapter=codex 应按 codex 布局探测（无配置如实 fail）, got %s %q", st, reason)
	}
}

// TestLocalAgentsDetectProjection 检测投影：未发现=not_checked+如实原因；
// discover 后四值与检查时间落位；旧字段 JSON 零变化（键集快照：条目键 ⊆
// ExecProfile 全集 ∪ {today_tasks, detect}，detect 对象键恰五个）。
func TestLocalAgentsDetectProjection(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := writeFakeCLI(t, t.TempDir(), "fake-cli", `echo "cli 1.0"`)
	profile := protocol.ExecProfile{
		Name: "zc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir,
		TimeoutSec: 60, Exposed: true,
	}
	srv, _ := newDiscoverServer(t, dir, []protocol.ExecProfile{profile})

	getAgents := func(t *testing.T) []map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/local/agents", nil)
		req.Header.Set("Authorization", "Bearer tok-local")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Agents []map[string]any `json:"agents"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.Agents
	}

	// ① 未发现：not_checked 如实（四值 + 原因，无检查时间）
	agents := getAgents(t)
	if len(agents) != 1 {
		t.Fatalf("应恰一 agent: %v", agents)
	}
	det, ok := agents[0]["detect"].(map[string]any)
	if !ok {
		t.Fatalf("应有 detect 摘要: %v", agents[0])
	}
	for _, k := range []string{"installed", "config_recognized", "execution_ready", "call_allowed"} {
		if det[k] != "not_checked" {
			t.Fatalf("未发现 %s 应 not_checked, got %v", k, det[k])
		}
	}
	if det["not_checked_reason"] == "" || det["checked_at"] != nil {
		t.Fatalf("未发现应带原因且无检查时间: %v", det)
	}

	// ② 旧字段零变化（键集快照）：条目键只允许既有全集 + detect
	execKeys := []string{"name", "kind", "command", "workdir", "agent_args", "timeout_sec",
		"allow_args", "allow_env", "config_home", "description", "permission_mode",
		"exposed", "stdin_to_arg", "orchestrator", "session_adapter"}
	allowed := map[string]bool{"today_tasks": true, "detect": true}
	for _, k := range execKeys {
		allowed[k] = true
	}
	for k := range agents[0] {
		if !allowed[k] {
			t.Fatalf("出现旧字段集之外的新键 %q（旧字段 JSON 零变化被破坏）: %v", k, agents[0])
		}
	}

	// ③ discover 后投影四值落位 + 检查时间
	home := makeZcodeHomeFixture(t, `{"authToken":"x"}`)
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":"`+home+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("discover 应 200: %v", out)
	}
	agents = getAgents(t)
	det = agents[0]["detect"].(map[string]any)
	if det["installed"] != "pass" || det["config_recognized"] != "pass" || det["execution_ready"] != "pass" {
		t.Fatalf("发现后投影应 pass×3: %v", det)
	}
	if det["call_allowed"] != "pass" { // 已登记名命中 → 本机调用允许
		t.Fatalf("已登记能力 call_allowed 投影应 pass: %v", det)
	}
	if det["checked_at"] == nil || det["checked_at"] == "" {
		t.Fatalf("发现后应有检查时间: %v", det)
	}
	// detect 对象键集恰五个（快照纪律：投影字段不擅自增减）
	for k := range det {
		switch k {
		case "installed", "config_recognized", "execution_ready", "call_allowed", "checked_at", "not_checked_reason":
		default:
			t.Fatalf("detect 摘要出现未登记键 %q: %v", k, det)
		}
	}
	// 版本留痕元数据不因投影出现（投影只回四值+时间+原因，VersionRaw 不进目录响应）
	raw, _ := json.Marshal(agents[0])
	if strings.Contains(string(raw), "cli 1.0") {
		t.Fatalf("目录响应不应携带版本留痕（仅 discover 应答回带）: %s", raw)
	}
}

// TestDiscoverRegisteredSwitches 已登记命中的两开关投影：name 命中优先；exposed
// 随配置真实值；命中 command 的回退路径如实标注 registered_by=command。
func TestDiscoverRegisteredSwitches(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := writeFakeCLI(t, t.TempDir(), "fake-cli", `echo "cli 2.0"`)
	profile := protocol.ExecProfile{
		Name: "svc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir,
		TimeoutSec: 60, Exposed: false, // 显式关闭：投影应如实 false
	}
	srv, _ := newDiscoverServer(t, dir, []protocol.ExecProfile{profile})

	// name 命中
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"svc","host":"zcode","command":"`+cmd+`","config_home":""}]}`)
	if code != http.StatusOK {
		t.Fatalf("应 200: %v", out)
	}
	res := out["results"].([]any)[0].(map[string]any)
	if res["registered"] != true || res["registered_by"] != "name" {
		t.Fatalf("应按 name 命中: %v", res)
	}
	lc := res["local_call"].(map[string]any)
	if lc["on"] != true || lc["source"] != "config_registered" {
		t.Fatalf("已登记 local_call 应 true: %v", lc)
	}
	ex := res["exposed"].(map[string]any)
	if ex["on"] != false {
		t.Fatalf("exposed=false 配置应如实投影 false: %v", ex)
	}

	// command 回退命中（名字不同但命令相同）
	code, out = postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"other-alias","host":"zcode","command":"`+cmd+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("应 200: %v", out)
	}
	res = out["results"].([]any)[0].(map[string]any)
	if res["registered"] != true || res["registered_by"] != "command" {
		t.Fatalf("同命令异名应按 command 回退命中: %v", res)
	}
}
