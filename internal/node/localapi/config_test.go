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
