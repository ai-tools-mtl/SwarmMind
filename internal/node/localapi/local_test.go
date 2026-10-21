package localapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// Loop 7 单测：AgentState 开关持久化（重启保持 / 先写后见 / YAML 基线回归）、
// 本机目录与状态端点、任务日志、控制台 embed 冒烟。开关上报与新任务 404 的
// 全链（真实 WSS）见 exposed_e2e_test.go。

// mustProfile 单 agent 基线（echo，exposed=true + sleeper，exposed=false）。
func mustProfile(t *testing.T) []protocol.ExecProfile {
	t.Helper()
	return []protocol.ExecProfile{
		echoProfileLocal(t),
		{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep",
			Workdir: t.TempDir(), TimeoutSec: 1, AllowArgs: true, Exposed: false,
			Description: "睡眠测试", PermissionMode: "no-shell"},
	}
}

func TestAgentStatePersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenAgentState(dir, mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}

	// 初值=基线：echo exposed，sleeper not exposed
	if cards := st.Cards(); cards[0].Exposed != true || cards[1].Exposed != false {
		t.Fatalf("初值应取 YAML 基线: %+v", cards)
	}

	// 关 echo：返回更新后视图；落盘文件存在且含覆盖
	p, err := st.SetExposed("echo", false)
	if err != nil || p.Exposed {
		t.Fatalf("SetExposed(echo,false) 应成功, got %+v err=%v", p, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, agentsStateFileName))
	if err != nil {
		t.Fatalf("状态文件应已落盘: %v", err)
	}
	var disk agentsStateDisk
	if err := json.Unmarshal(b, &disk); err != nil || disk.Version != 1 {
		t.Fatalf("状态文件格式不符: %s err=%v", b, err)
	}
	if !disk.Exposed["echo"].UpdatedAt.After(time.Time{}) || disk.Exposed["echo"].Exposed {
		t.Fatalf("覆盖记录应含 exposed=false 与 updated_at: %+v", disk.Exposed)
	}

	// 重开（模拟重启）：覆盖保持
	st2, err := OpenAgentState(dir, mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	eff := st2.Effective()
	if eff[0].Exposed || eff[1].Exposed {
		t.Fatalf("重启后覆盖应保持（echo 关、sleeper 保持关）: %+v", eff)
	}

	// 回开 echo（与 YAML 基线一致）：覆盖应删除（状态文件不积累）
	if p, err := st2.SetExposed("echo", true); err != nil || !p.Exposed {
		t.Fatalf("SetExposed(echo,true) 应成功: %+v err=%v", p, err)
	}
	st3, err := OpenAgentState(dir, mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(st3.override) != 0 {
		t.Fatalf("与基线一致后覆盖应清空, got %+v", st3.override)
	}
}

func TestAgentStateErrors(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenAgentState(dir, mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	// 未知 agent → ErrAgentNotFound（404 语义），状态不变
	if _, err := st.SetExposed("ghost", true); err == nil {
		t.Fatal("未知 agent 应报错")
	}
	// 落盘失败（目录被换成文件）不改变运行时状态：先写后见
	bad, err := OpenAgentState(filepath.Join(dir, "unwritable"), mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(bad.tmp, 0o755) // 占住临时文件路径 → WriteFile 失败
	if _, err := bad.SetExposed("echo", false); err == nil {
		t.Fatal("落盘失败应报错")
	}
	if cards := bad.Cards(); !cards[0].Exposed {
		t.Fatalf("落盘失败运行时状态不得变更: %+v", cards)
	}
}

// TestLocalAgentsAndStatus 本机目录（ExecProfile 字段全量 + 今日任务数）与状态端点。
func TestLocalAgentsAndStatus(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	dataDir := t.TempDir()
	profiles := mustProfile(t)
	st := newState(t, dataDir, profiles)

	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	// 今日任务：echo 名下 2 条（1 条昨日的另一 agent 不计）
	now := protocol.NowUTC()
	for _, task := range []protocol.Task{
		{ID: "t-1", TargetAgent: "echo", CreatedAt: now},
		{ID: "t-2", TargetAgent: "echo", CreatedAt: now.Add(-time.Hour)},
		{ID: "t-3", TargetAgent: "sleeper", CreatedAt: now.Add(-24 * time.Hour)},
	} {
		if err := led.Accept(task); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents: st, Ledger: led,
		Online:           func() bool { return true },
		Epoch:            func() int64 { return 7 },
		LastRegisteredAt: func() (time.Time, bool) { return now, true },
		LocalToken:       "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)
	do := func(method, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok-local")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, out := do(http.MethodGet, "/api/v1/local/agents")
	if code != http.StatusOK {
		t.Fatalf("local/agents 应 200, got %d: %s", code, out)
	}
	var la struct {
		Agents []struct {
			Name       string `json:"name"`
			Command    string `json:"command"`
			Workdir    string `json:"workdir"`
			TimeoutSec int    `json:"timeout_sec"`
			AllowArgs  bool   `json:"allow_args"`
			AllowEnv   []any  `json:"allow_env"`
			ConfigHome string `json:"config_home"`
			AgentArgs  []any  `json:"agent_args"`
			Permission string `json:"permission_mode"`
			Exposed    bool   `json:"exposed"`
			TodayTasks int    `json:"today_tasks"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(out), &la); err != nil {
		t.Fatal(err)
	}
	if len(la.Agents) != 2 {
		t.Fatalf("应两个 agent: %s", out)
	}
	echo, sleeper := la.Agents[0], la.Agents[1]
	if echo.Name != "echo" || echo.Command != "/bin/echo" || echo.Workdir == "" ||
		echo.TimeoutSec != 10 || !echo.AllowArgs || !echo.Exposed || echo.Permission != "no-shell" {
		t.Fatalf("echo ExecProfile 视图不符: %+v", echo)
	}
	if echo.TodayTasks != 2 {
		t.Fatalf("echo 今日任务数应 2（昨日不计）, got %d: %s", echo.TodayTasks, out)
	}
	if sleeper.Exposed || sleeper.TodayTasks != 0 {
		t.Fatalf("sleeper 视图不符: %+v", sleeper)
	}

	code, out = do(http.MethodGet, "/api/v1/local/status")
	if code != http.StatusOK {
		t.Fatalf("local/status 应 200, got %d: %s", code, out)
	}
	var ls struct {
		NodeID     string         `json:"node_id"`
		Online     bool           `json:"online"`
		Epoch      int64          `json:"epoch"`
		LastSyncAt *time.Time     `json:"last_sync_at"`
		Ledger     map[string]int `json:"ledger"`
		Mailbox    map[string]any `json:"mailbox"`
	}
	if err := json.Unmarshal([]byte(out), &ls); err != nil {
		t.Fatal(err)
	}
	if ls.NodeID != "nodeA" || !ls.Online || ls.Epoch != 7 || ls.LastSyncAt == nil {
		t.Fatalf("状态不符: %s", out)
	}
	if ls.Ledger["accepted"] != 3 || ls.Ledger["started"] != 0 || ls.Ledger["finished"] != 0 {
		t.Fatalf("账本三态计数不符: %+v", ls.Ledger)
	}
	if ls.Mailbox["error"] == "" {
		t.Fatalf("未配代理时信箱应如实呈现 error: %s", out)
	}

	// 状态文件路径约定：AgentState 指向 dataDir（agents-state.json 由此派生）
	if filepath.Dir(st.path) != dataDir {
		t.Fatalf("状态文件应在 data_dir 下: %s", st.path)
	}
}

// TestWebConsoleSmoke 控制台 embed 冒烟：GET / 200（HTML）+ 静态资源 200。
func TestWebConsoleSmoke(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	for path, want := range map[string]string{
		"/":          "text/html",
		"/app.js":    "text/javascript",
		"/style.css": "text/css",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !startsWith(ct, want) {
			t.Fatalf("GET %s Content-Type 应 %s, got %s", path, want, ct)
		}
		if len(b) == 0 {
			t.Fatalf("GET %s 应有内容", path)
		}
	}
	// 未在白名单的路径 → 404（embed 集合即白名单）
	resp, err := http.Get(srv.URL + "/nope.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知静态路径应 404, got %d", resp.StatusCode)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestTaskLogJournal 留痕：新→旧、同 task_id 幂等、上限裁剪、重启保持。
func TestTaskLogJournal(t *testing.T) {
	dir := t.TempDir()
	tl, err := OpenTaskLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := protocol.NowUTC()
	_ = tl.Record(TaskLogEntry{TaskID: "t-1", TargetNode: "nB", TargetAgent: "a", CreatedAt: base})
	_ = tl.Record(TaskLogEntry{TaskID: "t-2", TargetNode: "nB", TargetAgent: "a", CreatedAt: base.Add(time.Second)})
	_ = tl.Record(TaskLogEntry{TaskID: "t-1", TargetNode: "nB", TargetAgent: "a", CreatedAt: base.Add(2 * time.Second)}) // 幂等命中

	got := tl.List()
	if len(got) != 2 || got[0].TaskID != "t-2" || got[1].TaskID != "t-1" {
		t.Fatalf("应新→旧且幂等: %+v", got)
	}
	if got[1].CreatedAt != base {
		t.Fatalf("幂等命中不得重置创建时刻: %+v", got[1])
	}

	// 重开（重启）：落盘保持
	tl2, err := OpenTaskLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l := tl2.List(); len(l) != 2 {
		t.Fatalf("重启后留痕应保持: %+v", l)
	}

	// 上限裁剪：超 taskLogMax 丢最旧
	for i := 0; i < taskLogMax; i++ {
		_ = tl2.Record(TaskLogEntry{TaskID: fmt.Sprintf("t-x-%d", i), CreatedAt: base.Add(time.Duration(i) * time.Millisecond)})
	}
	if l := tl2.List(); len(l) != taskLogMax || l[0].TaskID == "t-1" {
		t.Fatalf("应裁剪至上限且丢最旧: len=%d first=%s", len(l), l[0].TaskID)
	}
}
