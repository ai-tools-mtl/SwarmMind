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

	// ① 输入漂移：缓存记录 command=A（带已记录路径事实，使「不叠加路径注记」
	// 断言真分岔——实现若错误地跑路径复核，此断言必红），现配置 command=B
	rep := agentdetect.Report{Name: "a", Installed: agentdetect.LayerCheck{
		Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC(),
		Paths: []agentdetect.PathRef{{Path: "/old/path/cli", Kind: "command", Exists: true, Regular: true}},
	}}
	s.recordDetect(rep, "/old/path/cli", "/old/home")
	dr := detectDriftFor(protocol.ExecProfile{Name: "a", Command: "/new/path/cli", ConfigHome: "/old/home"}, s.detectCache["a"])
	if !dr.Drifted || len(dr.Notes) != 1 || !strings.Contains(dr.Notes[0], "登记命令已变化") || !strings.Contains(dr.Notes[0], "需重新检测") {
		t.Fatalf("输入漂移应单条注记「登记命令已变化…需重新检测」, got %+v", dr)
	}
	// 输入不一致时不得再比对旧输入的路径事实（避免误导性双报；报告带 Paths 后
	// 本断言有牙——错误实现会给「路径已不存在」注记撞红）
	if strings.Contains(strings.Join(dr.Notes, "；"), "路径已不存在") {
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

// TestLocalAgentsDetectDriftPerLayerBasis 必修2 反例回归（codex 场景）：上次仅
// 检测 command（configHome 空）、installed=pass；随后命令文件删除 + 登记面新增
// 非空 config_home → 空变非空无 config 注记（该层当时未检测如实）、但 installed
// 层按独立基准照常复核 → drifted=true 且注记指向命令路径（旧实现双项一致门槛
// 跳过全部路径检查 → 漏报旧绿灯无过期）。
func TestLocalAgentsDetectDriftPerLayerBasis(t *testing.T) {
	s := New(Config{NodeID: "nodeA", Mode: ModeStandalone,
		Agents: newState(t, t.TempDir(), nil), LocalToken: "tok-local"})
	rep := agentdetect.Report{Name: "a", Installed: agentdetect.LayerCheck{
		Status: agentdetect.StatusPass, CheckedAt: protocol.NowUTC(),
		Paths: []agentdetect.PathRef{{Path: "/gone/cli", Kind: "command", Exists: true, Regular: true}},
	}}
	s.recordDetect(rep, "/gone/cli", "")
	dr := detectDriftFor(protocol.ExecProfile{Name: "a", Command: "/gone/cli", ConfigHome: "/newly/added/home"}, s.detectCache["a"])
	if !dr.Drifted {
		t.Fatalf("必修2 反例：仅 command 检测过+文件删除+config_home 新增，应 drifted=true（installed 层独立基准复核）, got %+v", dr)
	}
	joined := strings.Join(dr.Notes, "；")
	if !strings.Contains(joined, "/gone/cli") || !strings.Contains(joined, "需重新检测") {
		t.Fatalf("注记应指向已删命令路径: %+v", dr)
	}
	if strings.Contains(joined, "config_home 已变化") {
		t.Fatalf("空→非空（该层当时未检测）不得给「已变化」注记（无旧基准可比）: %+v", dr)
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

	// ④ 零声明+旧名命中：回退识别生效态——name_fallback 列出且注记点名候选
	//（建议4②：破坏零声明回退文案必红）
	assert(t, []protocol.ExecProfile{mk("zcode-dev-solo", solo, false)}, func(t *testing.T, top map[string]any) {
		orch := top["orchestrator"].(map[string]any)
		if fb := orch["name_fallback"].([]any); len(fb) != 1 || fb[0] != "zcode-dev-solo" {
			t.Fatalf("零声明旧名命中 name_fallback 应列出候选: %v", orch)
		}
		if n := orch["note"].(string); !strings.Contains(n, "按旧名字约定识别") || !strings.Contains(n, "zcode-dev-solo") {
			t.Fatalf("零声明回退文案应如实点名候选与来源: %v", n)
		}
	})

	// ⑤ 工作区归一（建议3）：/work 与 /work/ 应合并为一条冲突（filepath.Clean
	// 比较口径）；条目展示首见原始写法
	assert(t, []protocol.ExecProfile{mk("w1", shared, false), mk("w2", shared+"/", false)}, func(t *testing.T, top map[string]any) {
		conf := top["workspace_conflicts"].([]any)
		if len(conf) != 1 {
			t.Fatalf("/work 与 /work/ 应归一合并为一条冲突, got %v", top["workspace_conflicts"])
		}
		c := conf[0].(map[string]any)
		if c["workdir"] != shared || len(c["agents"].([]any)) != 2 {
			t.Fatalf("归一条目应展示首见原始写法且列全声明: %v", c)
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

// TestLocalAgentsDetectDriftSymlinkChainSemantics 必修1 反例回归（链路链接口径
// 与检测逐位对齐）：① .zcode 本就是符号链接（检测时链上链接已如实置位
// PathRef.Symlink=true）且未变化 → 不得注记「符号链接状态已变化」（旧实现末段
// Lstat 误报）；② 检测通过后目录链被换成指向原目录的符号链接 → 必报（旧实现
// 末段 Lstat 漏报）。两例走真实 discover（PathRef 来自检测器真实记录，非构造）。
func TestLocalAgentsDetectDriftSymlinkChainSemantics(t *testing.T) {
	cmd := writeFakeCLI(t, t.TempDir(), "fake-zcode", `echo "zcode runtime 1.0"`)

	// newCase 注册与检测输入一致的 profile 并跑一次真实 discover；返回投影 drift
	// 读取器与该次 discover 的 report（供前置条件断言检测记录的链接口径位）。
	newCase := func(t *testing.T, home string) (drift func(t *testing.T) map[string]any, report map[string]any) {
		t.Helper()
		dir := t.TempDir()
		workdir := filepath.Join(dir, "work")
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
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
		report = asReport(t, out["results"].([]any)[0].(map[string]any)["report"])
		return func(t *testing.T) map[string]any {
			t.Helper()
			_, _, agents := getLocalAgents(t, srv)
			det := agents[0]["detect"].(map[string]any)
			dr, ok := det["drift"].(map[string]any)
			if !ok {
				t.Fatalf("应有 drift 注记: %v", det)
			}
			return dr
		}, report
	}

	// ① 链上链接未变化：检测时 PathRef.Symlink=true（链上链接口径——codex 复审
	// 实证场景：root 本身即链接），实况不变 → drifted=false 且无链接变化注记
	//（旧实现末段比对误报）。布局经链接后为完整 zcode 布局（.zcode/cli/
	// setting.json 在链接目标内如实存在）。
	realDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realDir, ".zcode", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, ".zcode", "cli", "setting.json"), []byte(`{"authToken":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	linkHome := filepath.Join(t.TempDir(), "linkhome")
	if err := os.Symlink(realDir, linkHome); err != nil {
		t.Fatal(err)
	}
	drift1, rep1 := newCase(t, linkHome)
	cfgRef := rep1["config_recognized"].(map[string]any)["paths"].([]any)[0].(map[string]any)
	if cfgRef["symlink"] != true {
		t.Fatalf("前置：链上链接检测应记录配置路径 symlink=true（复合位）, got %v", cfgRef)
	}
	if dr := drift1(t); dr["drifted"] != false || strings.Contains(fmt.Sprint(dr["notes"]), "符号链接状态已变化") {
		t.Fatalf("必修1 反例①：链上链接未变化不得报漂移/链接变化, got %v", dr)
	}

	// ② 检测通过后目录被换成指向原目录的链接：必报（旧实现末段比对漏报）
	realHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realHome, ".zcode", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realHome, ".zcode", "cli", "setting.json"), []byte(`{"authToken":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	drift2, rep2 := newCase(t, realHome)
	if rep2["config_recognized"].(map[string]any)["status"] != "pass" {
		t.Fatalf("前置：真实目录配置识别应 pass, got %v", rep2["config_recognized"])
	}
	if dr := drift2(t); dr["drifted"] != false {
		t.Fatalf("前置：检测后未变化应 drifted=false, got %v", dr)
	}
	if err := os.Rename(filepath.Join(realHome, ".zcode"), filepath.Join(realHome, ".zcode.real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realHome, ".zcode.real"), filepath.Join(realHome, ".zcode")); err != nil {
		t.Fatal(err)
	}
	dr := drift2(t)
	if dr["drifted"] != true || !strings.Contains(fmt.Sprint(dr["notes"]), "符号链接状态已变化") {
		t.Fatalf("必修1 反例②：检测后换链链接必报, got %v", dr)
	}

	// ③ 建议1 反例：codex 登录标记 auth.json 检测后被换成悬空符号链接 → 必报
	//（旧实现只比 found→不存在，悬空链接 Lstat 存在 → 漏报；内容不读不覆盖——
	// 悬空链接无内容可读，报告的是链接状态位变化）
	codexHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(codexHome, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, ".codex", "config.toml"), []byte("model = \"gpt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, ".codex", "auth.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir3 := t.TempDir()
	workdir3 := filepath.Join(dir3, "work")
	if err := os.MkdirAll(workdir3, 0o755); err != nil {
		t.Fatal(err)
	}
	profile3 := protocol.ExecProfile{
		Name: "cx", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir3,
		ConfigHome: codexHome, TimeoutSec: 60, Exposed: true,
	}
	srv3, _ := newDiscoverServer(t, dir3, []protocol.ExecProfile{profile3})
	code3, out3 := postLocal(t, srv3, "tok-local", "/api/v1/local/agents/discover",
		`{"candidates":[{"name":"cx","host":"codex","command":"`+cmd+`","config_home":"`+codexHome+`"}]}`)
	if code3 != http.StatusOK {
		t.Fatalf("discover 应 200: %v", out3)
	}
	rep3 := asReport(t, out3["results"].([]any)[0].(map[string]any)["report"])
	if rep3["login_marker"].(map[string]any)["found"] != true {
		t.Fatalf("前置：codex 标记应发现, got %v", rep3["login_marker"])
	}
	if err := os.Remove(filepath.Join(codexHome, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(codexHome, ".codex", "nonexistent.json"), filepath.Join(codexHome, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	_, _, agents3 := getLocalAgents(t, srv3)
	det3 := agents3[0]["detect"].(map[string]any)
	dr3 := det3["drift"].(map[string]any)
	if dr3["drifted"] != true || !strings.Contains(fmt.Sprint(dr3["notes"]), "登录态标记路径符号链接状态已变化") {
		t.Fatalf("建议1 反例：codex 标记换悬空链接必报链接状态变化, got %v", dr3)
	}
}

// TestLocalAgentsDetectPermissionFailure 权限失败 fixture（W06-04 补缺）：宿主
// 配置文件对读取者无权限 → config_recognized fail 且原因带「打开失败」；并断言
// GET /api/v1/local/agents 投影层明细同 fail+原因（页面数据面可见，不冒认通过；
// 建议4④：页面投影断言为本用例实际范围的一部分，非仅检测层即时应答）。
// root 不受权限约束，跳过。
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
	workdir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := protocol.ExecProfile{
		Name: "zc", Kind: protocol.AgentKindCLI, Command: cmd, Workdir: workdir,
		ConfigHome: home, TimeoutSec: 60, Exposed: true,
	}
	srv, _ := newDiscoverServer(t, t.TempDir(), []protocol.ExecProfile{profile})
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
	// 页面数据面：GET 投影 layers.config_recognized 同 fail+原因
	_, _, agents := getLocalAgents(t, srv)
	det := agents[0]["detect"].(map[string]any)
	layers := det["layers"].(map[string]any)
	lv := layers["config_recognized"].(map[string]any)
	if lv["status"] != "fail" || !strings.Contains(fmt.Sprint(lv["reason"]), "打开失败") {
		t.Fatalf("权限失败应在 GET 投影逐层明细可见, got %v", lv)
	}
}
