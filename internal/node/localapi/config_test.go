package localapi

// 方案 3.5 配置编辑单测（第四轮收口 1）：
//   - updateAgentsYAML round-trip：补写缺失键（返工核心修复的验证件）、注释与
//     未知字段保留、缺省值键不写（最小形态）、空表清空、agents 键缺失追加、
//     解析失败拒写（HTTP 层验证原文件不动）；
//   - ValidateAgentProfiles 校验矩阵；classifyProfilesChange 热重载分类；
//   - ReplaceBaseline 基线整表替换与读取一致；FetchArtifactLocal 错误分支
//     （mismatch / not_found）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
	"gopkg.in/yaml.v3"
)

// parseAgents 从 yaml 文本解析 agents 表（round-trip 断言用）。
func parseAgents(t *testing.T, raw []byte) []AgentProfile {
	t.Helper()
	var parsed struct {
		Agents []AgentProfile `yaml:"agents"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip 产物应可解析: %v\n%s", err, raw)
	}
	return parsed.Agents
}

// TestUpdateAgentsYAMLBackfillMissingKeys 补写缺失键（返工核心修复验证件）：
// 既有条目缺 description/allow_env/timeout_sec 键，提交非缺省值后盘上三键齐且
// 值正确、按规范序插入；既有注释与未知字段（顶层+条目内）保留；缺省值键不写。
func TestUpdateAgentsYAMLBackfillMissingKeys(t *testing.T) {
	initial := `# 顶层手写注释
node_id: node-a
unknown_top: keep-me
agents:
  # alpha 条目注释
  - name: alpha
    command: /bin/echo
    workdir: /tmp/w
    custom_keep: yes
  - name: beta
    kind: cli
    command: /bin/cat
    workdir: /tmp/b
`
	agents := []AgentProfile{
		// alpha：三键均缺失且提交非缺省 → 必须补写
		{Name: "alpha", Kind: "cli", Command: "/bin/echo", Workdir: "/tmp/w",
			TimeoutSec: 600, AllowEnv: []string{"HOME", "PATH"}, Description: "第一个"},
		// beta：提交值全为缺省（description 空等）→ 缺失键保持不写（最小形态）
		{Name: "beta", Kind: "cli", Command: "/bin/cat", Workdir: "/tmp/b"},
	}
	out, err := updateAgentsYAML([]byte(initial), agents)
	if err != nil {
		t.Fatalf("updateAgentsYAML: %v", err)
	}

	got := parseAgents(t, out)
	if len(got) != 2 {
		t.Fatalf("应保留两条 agent: %+v", got)
	}
	a := got[0]
	if a.Description != "第一个" {
		t.Errorf("description 应补写为提交值, got %q", a.Description)
	}
	if strings.Join(a.AllowEnv, ",") != "HOME,PATH" {
		t.Errorf("allow_env 应补写为提交值, got %v", a.AllowEnv)
	}
	if a.TimeoutSec != 600 {
		t.Errorf("timeout_sec 应补写为提交值, got %d", a.TimeoutSec)
	}
	// 补写键按规范字段序：timeout_sec < allow_env < description
	iTimeout, iEnv, iDesc := strings.Index(string(out), "timeout_sec"), strings.Index(string(out), "allow_env"), strings.Index(string(out), "description")
	if !(0 <= iTimeout && iTimeout < iEnv && iEnv < iDesc) {
		t.Errorf("补写键应按规范序插入 timeout_sec<allow_env<description: %d %d %d\n%s", iTimeout, iEnv, iDesc, out)
	}
	// beta：缺省值键不写（最小形态）
	b := got[1]
	if b.Description != "" || len(b.AllowEnv) != 0 || b.TimeoutSec != 0 {
		t.Errorf("beta 提交全缺省，不应新增键: %+v", b)
	}
	betaSeg := string(betaEntry(t, out))
	for _, key := range []string{"description", "allow_env", "timeout_sec"} {
		if strings.Contains(betaSeg, key+":") {
			t.Errorf("beta 不应补写缺省键 %s\n%s", key, out)
		}
	}
	// 注释与未知字段保留（顶层 + 条目内 + 各级注释）
	for _, keep := range []string{"# 顶层手写注释", "unknown_top: keep-me", "# alpha 条目注释", "custom_keep: yes", "node_id: node-a"} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("round-trip 应保留 %q\n%s", keep, out)
		}
	}
}

// betaEntry 取 round-trip 产物中 beta 条目的原始 yaml 片段（断言最小形态用）。
func betaEntry(t *testing.T, raw []byte) []byte {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, "name: beta")
	if i < 0 {
		t.Fatalf("产物中应有 beta 条目:\n%s", raw)
	}
	return []byte(s[i:])
}

// TestUpdateAgentsYAMLEmptyTableAndAppend 整表清空（键位与键注释保留）与
// agents 键缺失时追加（新增条目最小形态）。
func TestUpdateAgentsYAMLEmptyTableAndAppend(t *testing.T) {
	// 空表：agents 置空序列，键位与注释不动
	cleared, err := updateAgentsYAML([]byte("# agents 注释\nagents:\n  - name: a\n    command: /bin/echo\n    workdir: /x\n"), nil)
	if err != nil {
		t.Fatalf("清空: %v", err)
	}
	if !strings.Contains(string(cleared), "agents: []") {
		t.Errorf("清空后应为空序列:\n%s", cleared)
	}
	if !strings.Contains(string(cleared), "# agents 注释") {
		t.Errorf("清空应保留键注释:\n%s", cleared)
	}
	if got := parseAgents(t, cleared); len(got) != 0 {
		t.Errorf("清空后应 0 条: %+v", got)
	}

	// agents 键不存在：追加（新增条目最小形态：缺省键不写）
	appended, err := updateAgentsYAML([]byte("node_id: node-a\n"), []AgentProfile{
		{Name: "gamma", Command: "/bin/echo", Workdir: "/w", TimeoutSec: 60},
	})
	if err != nil {
		t.Fatalf("追加: %v", err)
	}
	got := parseAgents(t, appended)
	if len(got) != 1 || got[0].Name != "gamma" || got[0].Kind != "cli" || got[0].TimeoutSec != 60 {
		t.Fatalf("agents 键缺失应追加条目: %+v", got)
	}
	for _, key := range []string{"agent_args", "allow_args", "allow_env", "config_home", "description", "permission_mode", "exposed"} {
		if strings.Contains(string(appended), key+":") {
			t.Errorf("新增条目缺省键 %s 不应写盘（最小形态）:\n%s", key, appended)
		}
	}
}

// TestUpdateAgentsYAMLErrors 解析失败/顶层非映射/agents 非列表 → 报错（调用方拒写）。
func TestUpdateAgentsYAMLErrors(t *testing.T) {
	cases := []struct {
		name, raw, wantErr string
	}{
		{"解析失败", "agents: [oops\n", "解析 node.yaml 失败"},
		{"顶层非映射", "- a\n- b\n", "顶层不是键值映射"},
		{"agents 非列表", "node_id: n\nagents: 5\n", "agents 段不是列表"},
	}
	agents := []AgentProfile{{Name: "a", Command: "/bin/echo", Workdir: "/x", TimeoutSec: 1}}
	for _, c := range cases {
		_, err := updateAgentsYAML([]byte(c.raw), agents)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 应报 %q, got %v", c.name, c.wantErr, err)
		}
	}
}

// TestValidateAgentProfiles 校验矩阵：合法表通过；重名/相对路径 command/
// allow_env 非法键/超上限/空 name 拒。
func TestValidateAgentProfiles(t *testing.T) {
	valid := func(name string) AgentProfile {
		return AgentProfile{Name: name, Kind: "cli", Command: "/bin/echo", Workdir: "/w", TimeoutSec: 10}
	}
	if err := ValidateAgentProfiles([]AgentProfile{valid("a"), valid("b.c_d-1")}); err != nil {
		t.Fatalf("合法表应通过: %v", err)
	}
	if err := ValidateAgentProfiles(nil); err != nil {
		t.Fatalf("空表应通过, got %v", err)
	}

	// 重名：name 在本机配置内必须唯一
	if err := ValidateAgentProfiles([]AgentProfile{valid("dup"), valid("dup")}); err == nil || !strings.Contains(err.Error(), "重名") {
		t.Errorf("重名应拒, got %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(p *AgentProfile)
		wantErr string
	}{
		{"相对路径 command", func(p *AgentProfile) { p.Command = "echo" }, "绝对路径"},
		{"allow_env 非法键", func(p *AgentProfile) { p.AllowEnv = []string{"BAD KEY"} }, "allow_env"},
		{"空 name", func(p *AgentProfile) { p.Name = "" }, "name 必填"},
	}
	for _, c := range cases {
		p := valid("ok")
		c.mutate(&p)
		if err := ValidateAgentProfiles([]AgentProfile{p}); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 应拒（含 %q）, got %v", c.name, c.wantErr, err)
		}
	}

	tooMany := make([]AgentProfile, 0, agentsMaxCount+1)
	for i := 0; i <= agentsMaxCount; i++ {
		tooMany = append(tooMany, valid(fmt.Sprintf("a%d", i)))
	}
	if err := ValidateAgentProfiles(tooMany); err == nil || !strings.Contains(err.Error(), "超上限") {
		t.Errorf("超 %d 上限应拒, got %v", agentsMaxCount, err)
	}
}

// TestClassifyProfilesChange 热重载分类：增/删/command 变更 → 结构性；
// 纯 description 变更 → 空（可热应用）。
func TestClassifyProfilesChange(t *testing.T) {
	base := []protocol.ExecProfile{
		{Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: "/w", TimeoutSec: 10, Exposed: true},
		{Name: "cat", Kind: protocol.AgentKindCLI, Command: "/bin/cat", Workdir: "/w", TimeoutSec: 10, Exposed: false},
	}
	withDesc := []protocol.ExecProfile{base[0], base[1]}
	withDesc[0].Description = "只改描述"

	// 纯 description（含 timeout/allow_env 等普通字段）变更 → 可热应用
	if got := classifyProfilesChange(base, withDesc); len(got) != 0 {
		t.Errorf("纯普通字段变更应可热应用, got %v", got)
	}

	cmdChanged := []protocol.ExecProfile{base[0], base[1]}
	cmdChanged[0].Command = "/bin/echo2"
	if got := classifyProfilesChange(base, cmdChanged); len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "echo: command 变更") {
		t.Errorf("command 变更应结构性, got %v", got)
	}

	added := append(append([]protocol.ExecProfile{}, base...), protocol.ExecProfile{Name: "new", Kind: protocol.AgentKindCLI, Command: "/bin/ls", Workdir: "/w", TimeoutSec: 5})
	if got := classifyProfilesChange(base, added); len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "新增 agent new") {
		t.Errorf("新增应结构性, got %v", got)
	}

	removed := base[:1]
	if got := classifyProfilesChange(base, removed); len(got) == 0 || !strings.Contains(strings.Join(got, ";"), "移除 agent cat") {
		t.Errorf("删除应结构性, got %v", got)
	}
}

// TestReplaceBaselineRoundTrip 基线整表替换与读取一致：覆盖集清空、Effective
// 即提交表、落盘后重开（模拟重启）一致。
func TestReplaceBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenAgentState(dir, mustProfile(t)) // echo(exposed) + sleeper(未暴露)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetExposed("echo", false); err != nil { // 造一个覆盖
		t.Fatal(err)
	}

	next := []protocol.ExecProfile{
		{Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: "/w", TimeoutSec: 30, Exposed: true, Description: "新表"},
		{Name: "extra", Kind: protocol.AgentKindCLI, Command: "/bin/ls", Workdir: "/w", TimeoutSec: 5},
	}
	if err := st.ReplaceBaseline(next); err != nil {
		t.Fatalf("ReplaceBaseline: %v", err)
	}
	eff := st.Effective()
	if len(eff) != len(next) {
		t.Fatalf("替换后表大小不符: %+v", eff)
	}
	for i := range next {
		if !reflect.DeepEqual(eff[i], next[i]) {
			t.Errorf("替换后 Effective[%d] 应与提交表一致:\n got %+v\nwant %+v", i, eff[i], next[i])
		}
	}
	if len(st.override) != 0 {
		t.Errorf("整表替换应清空既有覆盖集, got %+v", st.override)
	}

	// 落盘验证：状态文件 exposed 覆盖已空；重开（模拟重启）读取一致
	b, err := os.ReadFile(filepath.Join(dir, agentsStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var disk agentsStateDisk
	if err := json.Unmarshal(b, &disk); err != nil || len(disk.Exposed) != 0 {
		t.Fatalf("替换后状态文件覆盖集应为空: %s err=%v", b, err)
	}
	reopened, err := OpenAgentState(dir, next)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Effective(); !reflect.DeepEqual(got, next) {
		t.Errorf("重开读取应与基线一致:\n got %+v\nwant %+v", got, next)
	}
}

// TestFetchArtifactLocalErrors 本机取回错误分支：任务不存在 → ErrTaskNotFound；
// SHA-256 不一致 → ErrArtifactMismatch；文件丢失/无终态 → ErrArtifactNotFound。
func TestFetchArtifactLocalErrors(t *testing.T) {
	dir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	root := filepath.Join(dir, "artifacts")
	bus := transport.NewBus()
	svc := svcpkg.New(svcpkg.Config{
		Ledger:        led,
		Runners:       map[string]svcpkg.Runner{},
		ArtifactRoots: map[string]string{"echo": root},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)

	// 任务不存在 → not_found
	if _, _, err := svc.FetchArtifactLocal("t-missing", 0); !errors.Is(err, svcpkg.ErrTaskNotFound) {
		t.Fatalf("未知任务应 ErrTaskNotFound, got %v", err)
	}

	// 登记终态产物：内容与登记一致可取回；篡改盘上文件 → mismatch
	artData := []byte("本机产物内容")
	if err := os.MkdirAll(filepath.Join(root, "outputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	artPath := filepath.Join(root, "outputs", "report.txt")
	if err := os.WriteFile(artPath, artData, 0o644); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewTaskID()
	if err := led.AcceptOrigin(protocol.Task{ID: id, TargetNode: "nodeA", TargetAgent: "echo", SubmittedBy: "it"}, ledger.OriginLocal); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artData)
	if err := led.MarkFinished(id, protocol.TaskResult{Status: protocol.ResultCompleted,
		Artifacts: []protocol.Artifact{{Path: "outputs/report.txt", Sha256: hex.EncodeToString(sum[:]), Size: int64(len(artData))}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.FetchArtifactLocal(id, 99); !errors.Is(err, svcpkg.ErrArtifactNotFound) {
		t.Fatalf("下标越界应 ErrArtifactNotFound, got %v", err)
	}
	if err := os.WriteFile(artPath, []byte("篡改后的内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.FetchArtifactLocal(id, 0); !errors.Is(err, svcpkg.ErrArtifactMismatch) {
		t.Fatalf("SHA-256 不一致应 ErrArtifactMismatch, got %v", err)
	}
}

// TestPutConfigRoundTripAndReject HTTP 全链：PUT 补齐缺失三键落盘且注释/未知
// 字段保留（普通字段变更 → 热应用）；node.yaml 解析失败 → 400 拒写且原文件不动。
func TestPutConfigRoundTripAndReject(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	workdir := filepath.Join(dir, "work")
	initial := `# 顶部注释
node_id: nodeA
unknown_top: keep
agents:
  # alpha 条目注释
  - name: alpha
    command: /bin/echo
    workdir: ` + workdir + `
`
	if err := writeConfigFile(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	st := newState(t, t.TempDir(), []protocol.ExecProfile{{
		Name: "alpha", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir, Exposed: true,
	}})
	ops := &ConfigOps{
		ConfigPath: cfgPath, Supervised: true, RestartCmd: "restart-cmd",
		ReloadExecutors: func([]protocol.ExecProfile) error { return nil },
	}
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Mode: ModeStandalone, Agents: st, LocalToken: "tok-local", ConfigOps: ops,
	}).Handler())
	t.Cleanup(srv.Close)

	put := func(t *testing.T) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(configPutRequest{Agents: []AgentProfile{{
			Name: "alpha", Kind: "cli", Command: "/bin/echo", Workdir: workdir,
			TimeoutSec: 600, AllowEnv: []string{"HOME"}, Description: "网页补全",
		}}})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/local/config", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer tok-local")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	// 首次 PUT：三键缺失 + 提交非缺省 → 盘上补齐；普通字段变更 → 热应用
	code, out := put(t)
	if code != http.StatusOK || out["applied"] != true {
		t.Fatalf("PUT 应 200 applied=true, got %d %v", code, out)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := parseAgents(t, b)
	if len(got) != 1 || got[0].TimeoutSec != 600 || strings.Join(got[0].AllowEnv, ",") != "HOME" || got[0].Description != "网页补全" {
		t.Fatalf("盘上三键应齐且值正确: %+v", got)
	}
	for _, keep := range []string{"# 顶部注释", "unknown_top: keep", "# alpha 条目注释"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("PUT 后应保留 %q\n%s", keep, b)
		}
	}

	// 解析失败拒写：写坏文件后 PUT 合法表 → 400 invalid_config，原文件字节不动
	corrupted := "agents: [oops\n"
	if err := writeConfigFile(cfgPath, corrupted); err != nil {
		t.Fatal(err)
	}
	code, out = put(t)
	if code != http.StatusBadRequest || out["error"] != "invalid_config" {
		t.Fatalf("解析失败应 400 invalid_config, got %d %v", code, out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != corrupted {
		t.Errorf("拒写后原文件应原样不动:\n got %q\nwant %q", after, corrupted)
	}
}

// ---------- reload 端点（第五轮收敛 1：读表纳入事务锁；此前无端点级覆盖） ----------

// TestAgentsReloadEndpoint reload HTTP 全链：手编 node.yaml 后重载走「读-校验-
// 分类-热应用」，ReloadExecutors 收到盘上新表；解析失败/校验失败 → 400 且原文件
// 字节不动；node.yaml 缺失 → 404。
func TestAgentsReloadEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	workdir := filepath.Join(dir, "work")
	initial := `# 顶部注释
node_id: nodeA
agents:
  # alpha 条目注释
  - name: alpha
    command: /bin/echo
    workdir: ` + workdir + `
    timeout_sec: 600
`
	if err := writeConfigFile(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	st := newState(t, t.TempDir(), []protocol.ExecProfile{{
		Name: "alpha", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir, Exposed: true,
	}})
	var mu sync.Mutex
	var applied []protocol.ExecProfile
	ops := &ConfigOps{
		ConfigPath: cfgPath, Supervised: true,
		ReloadExecutors: func(p []protocol.ExecProfile) error {
			mu.Lock()
			applied = p
			mu.Unlock()
			return nil
		},
	}
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Mode: ModeStandalone, Agents: st, LocalToken: "tok-local", ConfigOps: ops,
	}).Handler())
	t.Cleanup(srv.Close)
	reload := func() (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/local/agents/reload", nil)
		req.Header.Set("Authorization", "Bearer tok-local")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	readFile := func() string {
		t.Helper()
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// ① 手编改 description 后重载：200 applied=true，热应用收到盘上新表
	edited := strings.Replace(initial, "timeout_sec: 600",
		"timeout_sec: 600\n    description: 手编新描述", 1)
	if err := writeConfigFile(cfgPath, edited); err != nil {
		t.Fatal(err)
	}
	code, out := reload()
	if code != http.StatusOK || out["applied"] != true {
		t.Fatalf("reload 应 200 applied=true, got %d %v", code, out)
	}
	mu.Lock()
	got := applied
	mu.Unlock()
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Description != "手编新描述" || got[0].TimeoutSec != 600 {
		t.Fatalf("热应用应收到盘上手编表: %+v", got)
	}

	// ② 解析失败 → 400 invalid_config，原文件字节不动
	corrupted := "agents: [oops\n"
	if err := writeConfigFile(cfgPath, corrupted); err != nil {
		t.Fatal(err)
	}
	code, out = reload()
	if code != http.StatusBadRequest || out["error"] != "invalid_config" {
		t.Fatalf("解析失败应 400 invalid_config, got %d %v", code, out)
	}
	if readFile() != corrupted {
		t.Errorf("拒重载后原文件应原样不动:\n%q", readFile())
	}

	// ③ 校验失败 → 400「文件重载被拒绝」（timeout_sec 非正数），原文件不动
	bad := strings.Replace(initial, "timeout_sec: 600", "timeout_sec: 0", 1)
	if err := writeConfigFile(cfgPath, bad); err != nil {
		t.Fatal(err)
	}
	code, out = reload()
	if code != http.StatusBadRequest || out["error"] != "invalid_request" ||
		!strings.Contains(fmt.Sprint(out["message"]), "文件重载被拒绝") {
		t.Fatalf("校验失败应 400 文件重载被拒绝, got %d %v", code, out)
	}
	if readFile() != bad {
		t.Errorf("校验拒载后原文件应原样不动:\n%q", readFile())
	}

	// ④ node.yaml 缺失 → 404 config_missing
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if code, out = reload(); code != http.StatusNotFound || out["error"] != "config_missing" {
		t.Fatalf("文件缺失应 404 config_missing, got %d %v", code, out)
	}
}

// TestReloadAndPutConfigConcurrentNoClobber 第五轮收敛 1 行为级验证：reload 的
// 读文件+解析纳入 writeConfigMu 临界区后，与 PUT config 并发不得再现「reload
// 锁外读旧表 → PUT 写新表 → reload 以旧表覆盖回盘」。PUT 链式发车锁定最后一轮
//（应答在临界区内写回，同 TestDeclareAndPutConfigConcurrentNoClobber 口径），
// reload 群全并发——reload 读到哪一版就写回哪一版，终态 agents 表必须与最后
// 一次成功 PUT 的提交表完全一致（逐字段全表比对，echo 必然存在）。
func TestReloadAndPutConfigConcurrentNoClobber(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	wd := t.TempDir()
	exposed := true
	table := func(desc string) []AgentProfile {
		return []AgentProfile{
			{Name: "echo", Kind: "cli", Command: "/bin/echo", Workdir: wd, TimeoutSec: 10,
				Description: desc, PermissionMode: "no-shell", Exposed: &exposed},
			{Name: "sleeper", Kind: "cli", Command: "/bin/sleep", Workdir: wd, TimeoutSec: 1, Exposed: &exposed},
		}
	}
	// 初始盘上表与运行时基线同构（同名同 command → 全程非结构性变更，全部热应用）
	initial, err := updateAgentsYAML([]byte("node_id: nodeA\n"), table("desc-init"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(cfgPath, string(initial)); err != nil {
		t.Fatal(err)
	}
	st := newState(t, t.TempDir(), []protocol.ExecProfile{
		{Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: wd, TimeoutSec: 10, Exposed: true},
		{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep", Workdir: wd, TimeoutSec: 1, Exposed: true},
	})
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Mode: ModeStandalone, Agents: st, LocalToken: "tok-local",
		ConfigOps: &ConfigOps{ConfigPath: cfgPath, Supervised: true,
			ReloadExecutors: func([]protocol.ExecProfile) error { return nil }},
	}).Handler())
	t.Cleanup(srv.Close)

	const rounds = 8
	putDone := make([]chan struct{}, rounds)
	for i := range putDone {
		putDone[i] = make(chan struct{})
	}
	var lastPut []AgentProfile // 第 rounds-1 轮提交表（仅该 goroutine 写，wg.Wait 后主 goroutine 读）
	var wg sync.WaitGroup
	// reload 群：与 PUT 全并发（竞态检测主体）
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/agents/reload", ""); code != http.StatusOK {
				t.Errorf("reload 应 200, got %d: %s", code, out)
			}
		}()
	}
	// PUT 链：第 i 轮等第 i-1 轮应答后发车（收到应答 = 第 i-1 轮写盘已完成）
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer close(putDone[i])
			if i > 0 {
				<-putDone[i-1]
			}
			tbl := table(fmt.Sprintf("desc-%d", i))
			if i == rounds-1 {
				lastPut = tbl
			}
			body, _ := json.Marshal(configPutRequest{Agents: tbl})
			if code, out := doJSON(t, srv, "tok-local", http.MethodPut, "/api/v1/local/config", string(body)); code != http.StatusOK {
				t.Errorf("PUT config 应 200, got %d: %s", code, out)
			}
		}(i)
	}
	wg.Wait()
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseAgents(t, b)
	hasEcho := false
	for _, a := range doc {
		if a.Name == "echo" {
			hasEcho = true
		}
	}
	if !hasEcho {
		t.Fatalf("终态表缺 echo（PUT 写盘丢失或被截断）\n%s", b)
	}
	wantRaw, err := updateAgentsYAML([]byte("agents: []\n"), lastPut)
	if err != nil {
		t.Fatal(err)
	}
	want := parseAgents(t, wantRaw)
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("终态表应与最后一次成功 PUT 的提交表完全一致（reload 以旧表覆盖回盘）:\n got %+v\nwant %+v\n%s", doc, want, b)
	}
}

// TestGetConfigServesDiskTableWhilePending（第六轮修复 1）：PUT 结构性变更进入
// 待应用窗口后，GET /api/v1/local/config 必须回盘上 node.yaml 的新表（用户最新
// 提交的真相源）并标 pending=true——原实现恒回运行时旧基线，前端刷新拿到旧表
// 再保存即把盘上新表覆盖回旧值（丢提交）。读盘失败（文件损坏）回退运行时基线
//（pending=false，pending_restart 仍如实为真）。
func TestGetConfigServesDiskTableWhilePending(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	workdir := filepath.Join(dir, "work")
	initial := `node_id: nodeA
agents:
  - name: alpha
    command: /bin/echo
    workdir: ` + workdir + `
`
	if err := writeConfigFile(cfgPath, initial); err != nil {
		t.Fatal(err)
	}
	st := newState(t, t.TempDir(), []protocol.ExecProfile{{
		Name: "alpha", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: workdir, Exposed: true,
	}})
	ops := &ConfigOps{
		ConfigPath: cfgPath, Supervised: true,
		ReloadExecutors: func([]protocol.ExecProfile) error { return nil },
	}
	svc := New(Config{NodeID: "nodeA", Mode: ModeStandalone, Agents: st, LocalToken: "tok-local", ConfigOps: ops})
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)

	get := func(t *testing.T) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/local/config", nil)
		req.Header.Set("Authorization", "Bearer tok-local")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET config 应 200, got %d", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("GET 应答须为 JSON: %v", err)
		}
		return out
	}
	agentNames := func(t *testing.T, out map[string]any) []string {
		t.Helper()
		raw, _ := json.Marshal(out["agents"])
		var ps []AgentProfile
		if err := json.Unmarshal(raw, &ps); err != nil {
			t.Fatalf("agents 解码失败: %v (%s)", err, raw)
		}
		names := make([]string, 0, len(ps))
		for _, p := range ps {
			names = append(names, p.Name)
		}
		return names
	}

	// 基线：非 pending 回运行时基线（= 盘上旧表），pending=false
	out := get(t)
	if names := agentNames(t, out); len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("非 pending 应回运行时基线 [alpha], got %v (%v)", names, out)
	}
	if out["pending"] != false || out["pending_restart"] != false {
		t.Fatalf("基线态 pending/pending_restart 均应 false: %v", out)
	}

	// PUT 追加 beta（结构性变更）：应答 pending_restart=true；盘上已是 alpha+beta，
	// 运行时基线仍 [alpha]
	putBody, _ := json.Marshal(configPutRequest{Agents: []AgentProfile{
		{Name: "alpha", Kind: "cli", Command: "/bin/echo", Workdir: workdir, TimeoutSec: 60},
		{Name: "beta", Kind: "cli", Command: "/bin/echo", Workdir: workdir, TimeoutSec: 60},
	}})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/local/config", strings.NewReader(string(putBody)))
	req.Header.Set("Authorization", "Bearer tok-local")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var putOut struct {
		PendingRestart bool `json:"pending_restart"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&putOut); err != nil {
		t.Fatalf("PUT 应答须为 JSON: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !putOut.PendingRestart {
		t.Fatalf("结构性 PUT 应 200 + pending_restart=true, got %d %+v", resp.StatusCode, putOut)
	}

	// 待应用窗口期 GET（模拟前端刷新）：必须回盘上新表 [alpha beta] 且 pending=true
	out = get(t)
	if out["pending"] != true || out["pending_restart"] != true {
		t.Fatalf("pending 窗口期 GET 应标 pending=true 且 pending_restart=true: %v", out)
	}
	if names := agentNames(t, out); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("pending 窗口期 GET 应回盘上新表 [alpha beta]（防刷新丢提交）, got %v", names)
	}

	// 读盘失败回退：盘上文件破坏为非法 yaml → GET 回运行时基线、pending=false
	//（pending_restart 状态字段仍如实为真）
	if err := os.WriteFile(cfgPath, []byte("agents: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = get(t)
	if out["pending"] != false || out["pending_restart"] != true {
		t.Fatalf("读盘失败应回退运行时口径（pending=false、pending_restart=true）: %v", out)
	}
	if names := agentNames(t, out); len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("读盘失败应回运行时基线 [alpha], got %v", names)
	}

	// 重启收敛（模拟标志复位）：GET 回运行时口径且 pending_restart=false
	svc.setPendingRestart(false)
	out = get(t)
	if out["pending"] != false || out["pending_restart"] != false {
		t.Fatalf("标志复位后应回运行时口径: %v", out)
	}
}
