// W06-04 页面数据面 fixture 测试（任务书 fixture 清单本机面补缺）：检测/漂移/
// 工作区冲突/身份冲突的投影行为。检测层 fixture（零扫描/损坏配置/不支持版本/
// 符号链接/超时/无登录）已在 agentdetect 包与 agentdiscover_test 覆盖，本文件
// 锁的是「页面数据面」——GET /api/v1/local/agents 投影逐层明细（layers）、漂移
// 注记（drift）、身份来源（orchestrator）、工作区冲突（workspace_conflicts）、
// 开关变更时刻（switches）。全 fake 探测目标 + t.TempDir，绝不调用真实
// codex/zcode、绝无网络、绝不触碰真实 HOME。
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

	"github.com/mtl802/swarmmind/internal/node/agentdetect"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// getLocalAgents GET /api/v1/local/agents 应答解码（顶层对象 + agents 数组）。
func getLocalAgents(t *testing.T, srv *httptest.Server) (int, map[string]any, []map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/local/agents", nil)
	req.Header.Set("Authorization", "Bearer tok-local")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	var agents []map[string]any
	if raw, ok := out["agents"].([]any); ok {
		for _, a := range raw {
			if m, ok := a.(map[string]any); ok {
				agents = append(agents, m)
			}
		}
	}
	return res.StatusCode, out, agents
}

// TestLocalAgentsDetectLayersCarryFailReasons 损坏配置/无登录的页面数据面行为：
// fail 层的失败原因经投影逐层携带（此前只投影 not_checked 聚合，fail 原因在目录
// 读取面上不可见——W06-04 补齐）。损坏配置（zcode 非法 JSON）→ config_recognized
// fail 且原因在 layers；无登录（合法 JSON 无身份键）→ execution_ready fail 且
// 原因带「登录」；两层原因各自独立呈现，不合并成一盏绿灯话术。
func TestLocalAgentsDetectLayersCarryFailReasons(t *testing.T) {
	cmd := writeFakeCLI(t, t.TempDir(), "fake-zcode", `echo "zcode runtime 1.0"`)
	workdir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := protocol.ExecProfile{
		Name: "zc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir,
		TimeoutSec: 60, Exposed: true,
	}
	srv, _ := newDiscoverServer(t, t.TempDir(), []protocol.ExecProfile{profile})

	discover := func(home string) {
		t.Helper()
		code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
			`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":"`+home+`"}]}`)
		if code != http.StatusOK {
			t.Fatalf("discover 应 200, got %d: %v", code, out)
		}
	}
	layerView := func(t *testing.T, agents []map[string]any, layer string) map[string]any {
		t.Helper()
		det := agents[0]["detect"].(map[string]any)
		layers := det["layers"].(map[string]any)
		lv, ok := layers[layer].(map[string]any)
		if !ok {
			t.Fatalf("layers 缺 %s: %v", layer, layers)
		}
		return lv
	}

	// ① 损坏配置：config_recognized=fail，失败原因在 layers 逐层明细（页面可读）
	broken := makeZcodeHomeFixture(t, `{"authToken": truncated`)
	discover(broken)
	_, _, agents := getLocalAgents(t, srv)
	if len(agents) != 1 {
		t.Fatalf("应恰一 agent: %v", agents)
	}
	cfg := layerView(t, agents, "config_recognized")
	if cfg["status"] != "fail" || !strings.Contains(fmt.Sprint(cfg["reason"]), "invalid_json") {
		t.Fatalf("损坏配置应在投影层明细 fail+invalid_json 原因, got %v", cfg)
	}
	ready := layerView(t, agents, "execution_ready")
	if ready["status"] != "fail" {
		t.Fatalf("损坏配置执行就绪应 fail, got %v", ready)
	}

	// ② 无登录：合法 JSON 无身份键 → execution_ready fail 且原因带「登录态标记」
	nologin := makeZcodeHomeFixture(t, `{"memo":"no identity keys here"}`)
	discover(nologin)
	_, _, agents = getLocalAgents(t, srv)
	ready = layerView(t, agents, "execution_ready")
	if ready["status"] != "fail" || !strings.Contains(fmt.Sprint(ready["reason"]), "登录态标记") {
		t.Fatalf("无登录应 fail+登录态标记原因（结构可识别≠登录成功）, got %v", ready)
	}
	// 每层 source 检查来源在位（可核查事实）
	for _, lk := range []string{"installed", "config_recognized", "execution_ready"} {
		if lv := layerView(t, agents, lk); lv["source"] == "" {
			t.Fatalf("layers.%s 应带检查来源, got %v", lk, lv)
		}
	}
	// 检测层即时应答（discover report）与投影一致：fail 原因不因投影丢失
}

// TestLocalAgentsDetectDriftAnnotation 漂移的页面数据面行为（W06-04）：登记后
// 命令文件/宿主配置文件被删除 → 目录投影 drift.drifted=true + 逐条人话注记
// （「需重新检测」），四层状态保持上次检测值（注记不自动改写，也绝不静默沿用
// 旧绿灯不提示）；登记面输入变化（检测后改 command）→ 登记面漂移注记。
func TestLocalAgentsDetectDriftAnnotation(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdDir := t.TempDir()
	cmd := writeFakeCLI(t, cmdDir, "fake-zcode", `echo "zcode runtime 1.0"`)
	home := makeZcodeHomeFixture(t, `{"authToken":"x"}`)
	profile := protocol.ExecProfile{
		Name: "zc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir,
		ConfigHome: home, TimeoutSec: 60, Exposed: true,
	}
	srv, _ := newDiscoverServer(t, dir, []protocol.ExecProfile{profile})
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":"`+home+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("discover 应 200: %v", out)
	}

	driftOf := func(t *testing.T) map[string]any {
		t.Helper()
		_, _, agents := getLocalAgents(t, srv)
		det := agents[0]["detect"].(map[string]any)
		dr, ok := det["drift"].(map[string]any)
		if !ok {
			t.Fatalf("应有 drift 注记: %v", det)
		}
		return dr
	}

	// ① 刚检测完（输入与路径事实一致）：未漂移
	if dr := driftOf(t); dr["drifted"] != false {
		t.Fatalf("刚检测完应 drifted=false, got %v", dr)
	}

	// ② 命令文件被删 → drifted=true + 注记含路径与「需重新检测」；四层状态不被改写
	if err := os.Remove(cmd); err != nil {
		t.Fatal(err)
	}
	dr := driftOf(t)
	if dr["drifted"] != true {
		t.Fatalf("命令文件删除后应 drifted=true, got %v", dr)
	}
	notes := fmt.Sprint(dr["notes"])
	if !strings.Contains(notes, cmd) || !strings.Contains(notes, "需重新检测") {
		t.Fatalf("漂移注记应含命令路径与「需重新检测」: %v", dr)
	}
	_, _, agents := getLocalAgents(t, srv)
	det := agents[0]["detect"].(map[string]any)
	if det["installed"] != "pass" || det["execution_ready"] != "pass" {
		t.Fatalf("漂移注记不得改写四层状态（如实保持上次检测值）: %v", det)
	}

	// ③ 宿主配置文件被删 → 注记含配置路径
	if err := os.Remove(filepath.Join(home, ".zcode", "cli", "setting.json")); err != nil {
		t.Fatal(err)
	}
	dr = driftOf(t)
	if dr["drifted"] != true || !strings.Contains(fmt.Sprint(dr["notes"]), "setting.json") {
		t.Fatalf("配置文件删除后注记应含配置路径, got %v", dr)
	}
}

// TestLocalAgentsDetectDriftInputMismatch 登记面漂移（白盒）：检测后登记面的
// command/config_home 被改而未重跑发现 → 注记「检测输入已变化」；零扫描缓存
// （空输入）与任何登记面之间不产出误导性漂移。
func TestLocalAgentsDetectDriftInputMismatch(t *testing.T) {
	s := New(Config{NodeID: "nodeA", Mode: ModeStandalone,
		Agents: newState(t, t.TempDir(), nil), LocalToken: "tok-local"})

	// ① 输入漂移：缓存记录 command=A，现配置 command=B
	rep := agentdetect.Report{Name: "a", Installed: agentdetect.LayerCheck{Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC()}}
	s.recordDetect(rep, "/old/path/cli", "/old/home")
	dr := detectDriftFor(protocol.ExecProfile{Name: "a", Command: "/new/path/cli", ConfigHome: "/old/home"}, s.detectCache["a"])
	if !dr.Drifted || len(dr.Notes) != 1 || !strings.Contains(dr.Notes[0], "登记命令已变化") || !strings.Contains(dr.Notes[0], "需重新检测") {
		t.Fatalf("输入漂移应单条注记「登记命令已变化…需重新检测」, got %+v", dr)
	}
	// 输入不一致时不得再比对旧输入的路径事实（避免误导性双报）
	if strings.Contains(strings.Join(dr.Notes, "；"), "路径") {
		t.Fatalf("输入漂移态不得叠加旧输入路径事实注记: %+v", dr)
	}

	// ② config_home 漂移
	s.recordDetect(rep, "/same/cli", "/old/home")
	dr = detectDriftFor(protocol.ExecProfile{Name: "a", Command: "/same/cli"}, s.detectCache["a"])
	if !dr.Drifted || !strings.Contains(strings.Join(dr.Notes, "；"), "config_home 已变化") {
		t.Fatalf("config_home 漂移应注记, got %+v", dr)
	}

	// ③ 零扫描缓存（空输入快照）：对任何登记面不产出漂移（未检测如实，不冒认「缺了就是变了」）
	s.recordDetect(agentdetect.Report{Name: "z"}, "", "")
	dr = detectDriftFor(protocol.ExecProfile{Name: "z", Command: "/x", ConfigHome: "/y"}, s.detectCache["z"])
	if dr.Drifted {
		t.Fatalf("零扫描缓存不得产出漂移注记: %+v", dr)
	}
}

// TestLocalAgentsIdentityAndWorkspaceProjection 身份/工作区冲突投影（W06-04）：
// 本机多声明 → orchestrator.conflict=true + 人话注记（不冒认唯一生效）；零声明
// + 旧名字约定命中 → name_fallback 列出（来源标注）；同工作区多声明 →
// workspace_conflicts 如实列出；恰一/无共用 → 冲突面为空/非冲突。
func TestLocalAgentsIdentityAndWorkspaceProjection(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	solo := filepath.Join(dir, "solo")
	for _, d := range []string{shared, solo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name, workdir string, orch bool) protocol.ExecProfile {
		return protocol.ExecProfile{
			Name: name, Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir,
			TimeoutSec: 60, Exposed: true, Orchestrator: orch,
		}
	}

	assert := func(t *testing.T, profiles []protocol.ExecProfile, check func(t *testing.T, top map[string]any)) {
		t.Helper()
		srv, _ := newDiscoverServer(t, t.TempDir(), profiles)
		code, top, _ := getLocalAgents(t, srv)
		if code != http.StatusOK {
			t.Fatalf("GET 应 200, got %d: %v", code, top)
		}
		check(t, top)
	}

	// ① 恰一声明 + 旧名回退命中 + 同工作区两声明
	assert(t, []protocol.ExecProfile{
		mk("boss", shared, true),
		mk("zcode-dev-helper", shared, false),
		mk("solo-agent", solo, false),
	}, func(t *testing.T, top map[string]any) {
		orch := top["orchestrator"].(map[string]any)
		if decl := orch["declared"].([]any); len(decl) != 1 || decl[0] != "boss" {
			t.Fatalf("declared 应恰 boss: %v", orch)
		}
		if orch["conflict"] != false {
			t.Fatalf("恰一声明 conflict 应 false: %v", orch)
		}
		if fb := orch["name_fallback"].([]any); len(fb) != 1 || fb[0] != "zcode-dev-helper" {
			t.Fatalf("name_fallback 应恰 zcode-dev-helper: %v", orch)
		}
		if !strings.Contains(orch["note"].(string), legacyPrimeNameKey) {
			t.Fatalf("注记应披露旧名字约定来源: %v", orch["note"])
		}
		conf := top["workspace_conflicts"].([]any)
		if len(conf) != 1 {
			t.Fatalf("同工作区两声明应恰一条冲突: %v", top["workspace_conflicts"])
		}
		c := conf[0].(map[string]any)
		if c["workdir"] != shared {
			t.Fatalf("冲突 workdir 应为共享目录: %v", c)
		}
		if agents := c["agents"].([]any); len(agents) != 2 {
			t.Fatalf("冲突条目应列全两个声明: %v", c)
		}
	})

	// ② 本机多声明：conflict=true + 注记点名多声明（不冒认唯一管家已生效）
	assert(t, []protocol.ExecProfile{mk("a", solo, true), mk("b", filepath.Join(dir, "solo2"), true)}, func(t *testing.T, top map[string]any) {
		orch := top["orchestrator"].(map[string]any)
		if orch["conflict"] != true {
			t.Fatalf("双声明 conflict 应 true: %v", orch)
		}
		if n := orch["note"].(string); !strings.Contains(n, "多份声明") {
			t.Fatalf("多声明注记应如实点名: %v", n)
		}
	})

	// ③ 零声明无旧名命中：declared 空、name_fallback 缺省（omitempty）
	assert(t, []protocol.ExecProfile{mk("plain", solo, false)}, func(t *testing.T, top map[string]any) {
		orch := top["orchestrator"].(map[string]any)
		if len(orch["declared"].([]any)) != 0 {
			t.Fatalf("零声明 declared 应空: %v", orch)
		}
		if _, ok := orch["name_fallback"]; ok {
			t.Fatalf("无旧名命中 name_fallback 应缺省: %v", orch)
		}
	})
}

// TestLocalAgentsSwitchStampsProjection 开关变更时刻投影（W06-04 配置历史兼容）：
// 基线值 → source=yaml 无时刻；网页覆盖 → source=override + updated_at（UTC）。
// 这是本机唯一有数据面支撑的「变更时间」，页面据此展示、不编造其余历史。
func TestLocalAgentsSwitchStampsProjection(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := protocol.ExecProfile{
		Name: "svc", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir,
		TimeoutSec: 60, Exposed: true,
	}
	srv, _ := newDiscoverServer(t, dir, []protocol.ExecProfile{profile})

	patch := func(t *testing.T, path, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-local")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("PATCH %s 应 200, got %d: %v", path, res.StatusCode, out)
		}
		return out
	}

	switchOf := func(t *testing.T, key string) map[string]any {
		t.Helper()
		_, _, agents := getLocalAgents(t, srv)
		sw, ok := agents[0]["switches"].(map[string]any)
		if !ok {
			t.Fatalf("应有 switches 投影: %v", agents[0])
		}
		return sw[key].(map[string]any)
	}

	// ① 基线：source=yaml，无时刻
	if st := switchOf(t, "exposed"); st["source"] != "yaml" || st["updated_at"] != nil {
		t.Fatalf("无覆盖应 source=yaml 且无时刻: %v", st)
	}
	// ② 网页覆盖 exposed=false：source=override + UTC 时刻
	patch(t, "/api/v1/local/agents/svc/exposed", `{"exposed":false}`)
	st := switchOf(t, "exposed")
	if st["source"] != "override" || st["updated_at"] == nil {
		t.Fatalf("覆盖后应 source=override 且带时刻: %v", st)
	}
	if !strings.Contains(fmt.Sprint(st["updated_at"]), "Z") {
		t.Fatalf("时刻应 UTC 序列化: %v", st)
	}
	// ③ 管家声明覆盖同形态
	patch(t, "/api/v1/local/agents/svc/orchestrator", `{"orchestrator":true}`)
	if st := switchOf(t, "orchestrator"); st["source"] != "override" || st["updated_at"] == nil {
		t.Fatalf("声明覆盖应 source=override 且带时刻: %v", st)
	}
}

// TestLocalAgentsDetectPermissionFailure 权限失败 fixture（W06-04 补缺）：宿主
// 配置文件对读取者无权限 → config_recognized fail 且原因带「打开失败」，投影层
// 明细可见（不冒认通过）。root 不受权限约束，跳过。
func TestLocalAgentsDetectPermissionFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 不受文件权限约束：权限失败 fixture 不适用")
	}
	cmd := writeFakeCLI(t, t.TempDir(), "fake-zcode", `echo "zcode runtime 1.0"`)
	home := makeZcodeHomeFixture(t, `{"authToken":"x"}`)
	setting := filepath.Join(home, ".zcode", "cli", "setting.json")
	if err := os.Chmod(setting, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(setting, 0o644) }) // TempDir 清理兜底
	srv, _ := newDiscoverServer(t, t.TempDir(), nil)
	code, out := postLocal(t, srv, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"zc","host":"zcode","command":"`+cmd+`","config_home":"`+home+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("discover 应 200, got %d: %v", code, out)
	}
	res := out["results"].([]any)[0].(map[string]any)
	rep := asReport(t, res["report"])
	st, reason := layerStatusMap(t, rep, "config_recognized")
	if st != "fail" || !strings.Contains(reason, "打开失败") {
		t.Fatalf("无权限配置应 fail+打开失败原因, got %s %q", st, reason)
	}
}
