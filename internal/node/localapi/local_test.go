package localapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
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
	// 今日任务：echo 名下 2 条（1 条昨日的另一 agent 不计）。「今日」种子锚定当日
	// UTC 起点 +1h/+2h：原先的 now-1h 在 UTC 零点后 1 小时内运行会跌入昨日被正确
	// 排除（2026-10-25 08:11 CST 实测 flake），统计口径是 UTC 自然日，与断言无关
	// 的运行时刻不应决定测试成败。
	now := protocol.NowUTC()
	todayStart := now.Truncate(24 * time.Hour)   // UTC 时刻 Truncate 即当日零点
	for _, task := range []protocol.Task{
		{ID: "t-1", TargetAgent: "echo", CreatedAt: todayStart.Add(time.Hour)},
		{ID: "t-2", TargetAgent: "echo", CreatedAt: todayStart.Add(2 * time.Hour)},
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
	// 边界方案 v2（D6）：信箱余量/投递窗口是 hub 侧事实，响应不得再含这些键
	for _, gone := range []string{`"mailbox"`, `"inflight"`} {
		if strings.Contains(out, gone) {
			t.Fatalf("local/status 不得含 %s 键: %s", gone, out)
		}
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

// TestLocalTasksLedgerDriven 任务列表账本全量驱动（2026-10-26 全量审计裁决；
// TESTPLAN-tasks-audit.md A1-A6）：hub 下发任务在列（origin=hub、状态直出）、
// 本机派单 origin=local、cancelled 无回归（W2 观察项）、留痕补尾（账本中已不存在
// 的本机派单历史：origin=local 无状态，账本在列者不重复）、排序新→旧（同刻按
// task_id 稳定序）、降级路径（Ledger 未装配退纯留痕行；两源皆空 tasks 空数组）。
// codex 审查③断言加固：A1 补 target/trace/created_at 逐值断言；A6 补准确条数/
// 逐行 origin/全无状态的结构化断言（不再字符串 Contains）。
func TestLocalTasksLedgerDriven(t *testing.T) {
	dataDir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	base := protocol.NowUTC().Truncate(time.Second)
	mk := func(id string, at time.Time) protocol.Task {
		return protocol.Task{ID: id, TraceID: "tr-" + id, TargetNode: "nodeA", TargetAgent: "echo",
			SubmittedBy: "by-" + id, IdemKey: "idem-" + id, CreatedAt: at}
	}
	seed := []struct {
		id     string
		at     time.Time
		local  bool
		start  bool
		result *protocol.TaskResult
	}{
		{id: "t-hub-done", at: base.Add(40 * time.Second),
			result: &protocol.TaskResult{Status: "completed", ExitCode: 0, FinalAnswer: "ok", FinishedAt: base.Add(41 * time.Second)}},
		{id: "t-hub-cancel", at: base.Add(30 * time.Second),
			result: &protocol.TaskResult{Status: "cancelled", ExitCode: -1, Error: "user_cancelled"}}, // W2 观察项：cancelled 终态合法出列
		{id: "t-local-run", at: base.Add(20 * time.Second), local: true, start: true},
		{id: "t-local-ack", at: base.Add(20 * time.Second), local: true}, // 与上条同刻：稳定序按 task_id
		{id: "t-hub-ack", at: base.Add(10 * time.Second)},
	}
	for _, s := range seed {
		task := mk(s.id, s.at)
		if s.local {
			err = led.AcceptOrigin(task, ledger.OriginLocal)
		} else {
			err = led.Accept(task)
		}
		if err != nil {
			t.Fatal(err)
		}
		if s.start {
			err = led.MarkStarted(s.id)
		} else if s.result != nil {
			err = led.MarkFinished(s.id, *s.result)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	tl, err := OpenTaskLog(filepath.Join(t.TempDir(), "tasklog"))
	if err != nil {
		t.Fatal(err)
	}
	// 留痕两条：命中账本条目（不得重复出列）+ 账本已不存在（补尾，origin=local 无状态）。
	_ = tl.Record(TaskLogEntry{TaskID: "t-hub-done", TraceID: "tr-t-hub-done", TargetNode: "nodeA", TargetAgent: "echo", CreatedAt: base.Add(39 * time.Second)})
	_ = tl.Record(TaskLogEntry{TaskID: "t-old-local", TraceID: "tr-t-old-local", TargetNode: "nodeA", TargetAgent: "echo", SubmittedBy: "console", CreatedAt: base.Add(-time.Hour)})

	do := func(h http.Handler) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/local/tasks", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("local/tasks 应 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	type rowView struct {
		TaskID      string    `json:"task_id"`
		TraceID     string    `json:"trace_id"`
		TargetNode  string    `json:"target_node"`
		TargetAgent string    `json:"target_agent"`
		IdemKey     string    `json:"idem_key"`
		Origin      string    `json:"origin"`
		Status      string    `json:"status"`
		CreatedAt   time.Time `json:"created_at"`
	}
	parse := func(t *testing.T, out string) (rows []rowView, total int) {
		t.Helper()
		var b struct {
			Tasks []rowView `json:"tasks"`
			Total int       `json:"total"`
		}
		if err := json.Unmarshal([]byte(out), &b); err != nil {
			t.Fatal(err)
		}
		return b.Tasks, b.Total
	}
	h := New(Config{NodeID: "nodeA", Ledger: led, TaskLog: tl, LocalToken: "tok"}).Handler()
	rows, total := parse(t, do(h))
	// 期望序：created_at 新→旧；同刻两条按 task_id 字典序（ack < run）；留痕补尾最旧。
	wantOrder := []string{"t-hub-done", "t-hub-cancel", "t-local-ack", "t-local-run", "t-hub-ack", "t-old-local"}
	if total != len(wantOrder) || len(rows) != len(wantOrder) {
		t.Fatalf("应 %d 行（账本 5 + 留痕补尾 1，账本在列留痕不重；total 同值）, got rows=%d total=%d: %+v", len(wantOrder), len(rows), total, rows)
	}
	byID := map[string]rowView{}
	for i, r := range rows {
		if r.TaskID != wantOrder[i] {
			t.Fatalf("第 %d 行应 %s（新→旧）, got %s: %+v", i, wantOrder[i], r.TaskID, rows)
		}
		byID[r.TaskID] = r
	}
	// A1 断言加固（codex 审查③）：hub 终态行逐值对齐账本条目（target/trace/
	// idem/created_at 证明行确实来自账本条目而非拼装）。
	if r := byID["t-hub-done"]; r.Origin != "hub" || r.Status != "completed" ||
		r.TargetNode != "nodeA" || r.TargetAgent != "echo" ||
		r.TraceID != "tr-t-hub-done" || r.IdemKey != "idem-t-hub-done" ||
		!r.CreatedAt.Equal(base.Add(40*time.Second)) {
		t.Fatalf("hub 终态行不符（A1 加固：target/trace/idem/created_at 逐值）: %+v", r)
	}
	if r := byID["t-hub-cancel"]; r.Origin != "hub" || r.Status != "cancelled" {
		t.Fatalf("hub cancelled 行不符（A3/W2 回归）: %+v", r)
	}
	if r := byID["t-local-run"]; r.Origin != "local" || r.Status != "running" {
		t.Fatalf("local running 行不符（A2）: %+v", r)
	}
	if r := byID["t-local-ack"]; r.Origin != "local" || r.Status != "acknowledged" {
		t.Fatalf("local accepted 行不符（A2）: %+v", r)
	}
	if r := byID["t-hub-ack"]; r.Origin != "hub" || r.Status != "acknowledged" {
		t.Fatalf("hub accepted 行不符: %+v", r)
	}
	// A4 断言加固：留痕补尾行逐值（含 TaskLog 自带 trace_id/created_at）。
	if r := byID["t-old-local"]; r.Origin != "local" || r.Status != "" ||
		r.TraceID != "tr-t-old-local" || !r.CreatedAt.Equal(base.Add(-time.Hour)) {
		t.Fatalf("留痕补尾行不符（A4 加固：origin=local 无状态，trace/created_at 取留痕条目）: %+v", r)
	}

	// 降级 1（A6 断言加固）：Ledger 未装配 → 恰为留痕条数的纯留痕行（结构化断言：
	// 准确条数、逐行 origin=local、逐行无状态），留痕含账本在列任务的提交记录时
	// 无从去重，全部按留痕呈现。
	h2 := New(Config{NodeID: "nodeA", TaskLog: tl, LocalToken: "tok"}).Handler()
	rows, total = parse(t, do(h2))
	if total != 2 || len(rows) != 2 {
		t.Fatalf("A6：降级应恰 2 行（=留痕条数）, got rows=%d total=%d: %+v", len(rows), total, rows)
	}
	for _, r := range rows {
		if r.Origin != "local" || r.Status != "" {
			t.Fatalf("A6：降级行应逐行 origin=local 且无状态, got %+v", r)
		}
	}
	// 降级 2：两源皆空 → tasks 空数组（非 null）且 total=0。
	out := do(New(Config{NodeID: "nodeA", LocalToken: "tok"}).Handler())
	if !strings.Contains(out, `"tasks":[]`) {
		t.Fatalf("两源皆空应 tasks 空数组: %s", out)
	}
	if rows, total = parse(t, out); total != 0 || len(rows) != 0 {
		t.Fatalf("两源皆空应 0 行, got rows=%d total=%d", len(rows), total)
	}
}

// TestLocalTasksPagination 列表分页（codex 审查④容量边界）：limit/offset 切片
// 连续（排序在切片前，跨页次序一致）、total 恒为合并去重后总数、offset 越界空页、
// 非法参数 400、limit 超上限收敛（parseTasksPage 函数级断言）。
func TestLocalTasksPagination(t *testing.T) {
	dataDir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	base := protocol.NowUTC().Truncate(time.Second)
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("t-%02d", i)
		if err := led.Accept(protocol.Task{ID: id, TraceID: "tr-" + id, TargetNode: "nodeA", TargetAgent: "echo",
			CreatedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	tl, err := OpenTaskLog(filepath.Join(t.TempDir(), "tasklog"))
	if err != nil {
		t.Fatal(err)
	}
	_ = tl.Record(TaskLogEntry{TaskID: "t-old", TargetNode: "nodeA", TargetAgent: "echo", CreatedAt: base.Add(-time.Hour)})

	h := New(Config{NodeID: "nodeA", Ledger: led, TaskLog: tl, LocalToken: "tok"}).Handler()
	get := func(query string) (int, []string, int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/local/tasks"+query, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec.Code, nil, 0
		}
		var b struct {
			Tasks []struct {
				TaskID string `json:"task_id"`
			} `json:"tasks"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(b.Tasks))
		for _, r := range b.Tasks {
			ids = append(ids, r.TaskID)
		}
		return rec.Code, ids, b.Total
	}
	// 默认参数（无 limit/offset）：8 行全出、total=8、新→旧（t-06 最新，留痕最旧）。
	code, ids, total := get("")
	if code != http.StatusOK || total != 8 || len(ids) != 8 ||
		ids[0] != "t-06" || ids[6] != "t-00" || ids[7] != "t-old" {
		t.Fatalf("默认应 8 行全出（新→旧）, got code=%d total=%d ids=%v", code, total, ids)
	}
	// limit=3：前 3 行。
	code, ids, total = get("?limit=3")
	if code != http.StatusOK || total != 8 || len(ids) != 3 || ids[0] != "t-06" || ids[2] != "t-04" {
		t.Fatalf("limit=3 应取最新 3 行且 total=8, got code=%d total=%d ids=%v", code, total, ids)
	}
	// limit=3&offset=3：切片连续（恰为第 4-6 行，拼接两页即全序）。
	code, ids, total = get("?limit=3&offset=3")
	if code != http.StatusOK || total != 8 || len(ids) != 3 || ids[0] != "t-03" || ids[2] != "t-01" {
		t.Fatalf("limit=3&offset=3 应连续取第 4-6 行, got code=%d total=%d ids=%v", code, total, ids)
	}
	// offset=7：尾页恰为留痕补尾行。
	code, ids, _ = get("?limit=3&offset=7")
	if code != http.StatusOK || len(ids) != 1 || ids[0] != "t-old" {
		t.Fatalf("offset=7 应取尾行留痕, got code=%d ids=%v", code, ids)
	}
	// offset 越界：空页（tasks 空数组）+ total 仍为全量。
	code, ids, total = get("?offset=99")
	if code != http.StatusOK || total != 8 || len(ids) != 0 {
		t.Fatalf("offset 越界应空页且 total=8, got code=%d total=%d ids=%v", code, total, ids)
	}
	// 非法参数 → 400 invalid_request。
	for _, bad := range []string{"?limit=0", "?limit=-1", "?limit=abc", "?offset=-1", "?offset=abc"} {
		if code, _, _ = get(bad); code != http.StatusBadRequest {
			t.Fatalf("%s 应 400, got %d", bad, code)
		}
	}
	// limit 超上限收敛到 tasksPageMax（函数级：真实请求不便构造 1000+ 行种子）。
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/local/tasks?limit=5000&offset=7", nil)
	limit, offset, err := parseTasksPage(req)
	if err != nil || limit != tasksPageMax || offset != 7 {
		t.Fatalf("limit=5000 应收敛 %d, got limit=%d offset=%d err=%v", tasksPageMax, limit, offset, err)
	}
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/local/tasks", nil)
	limit, offset, err = parseTasksPage(req)
	if err != nil || limit != tasksPageDefault || offset != 0 {
		t.Fatalf("缺省应 %d/0, got limit=%d offset=%d err=%v", tasksPageDefault, limit, offset, err)
	}
}

// TestLocalTasksSubmitInterleave 提交穿插读取窗口不误标（codex 审查①回归）：
// 在 TaskLog 快照与 Ledger 快照之间的窗口内走真实提交路径（handleSubmit：先入账、
// 后留痕）。race detector 不报警的逻辑竞态——修复前顺序为「先 Ledger 后 TaskLog」，
// 本构造下窗口内提交命中「留痕有、账本无」被误判为已清理留痕行（status==""，
// 呈现「已过保留期」）；修复后任一交错下该任务必为带状态的账本行且恰出列一次。
// 确定性由 tasksRaceHook 钩子保证（不依赖调度时序）。
func TestLocalTasksSubmitInterleave(t *testing.T) {
	bus := transport.NewBus()
	dataDir := t.TempDir()
	agentState, err := OpenAgentState(dataDir, []protocol.ExecProfile{echoProfileLocal(t)})
	if err != nil {
		t.Fatal(err)
	}
	led, _, err := ledger.Open(filepath.Join(dataDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	taskLog, err := OpenTaskLog(filepath.Join(dataDir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	baseEnv := map[string]string{"PATH": "/usr/bin:/bin", "TMPDIR": "/tmp", "LANG": "en_US.UTF-8"}
	runners := map[string]svcpkg.Runner{"echo": runner.New(agentState.Effective()[0], baseEnv, filepath.Join(dataDir, "home"))}
	svc := svcpkg.New(svcpkg.Config{Ledger: led, Runners: runners}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	s := New(Config{NodeID: "nodeA", Service: svc, Agents: agentState, Ledger: led,
		TaskLog: taskLog, LocalToken: "tok", PrimeAgent: "echo", PrimeToken: "tok"})
	var submittedID string
	s.tasksRaceHook = func() { // 窗口内：真实提交路径（先入账、后留痕）
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks",
			strings.NewReader(`{"target_node":"nodeA","target_agent":"echo","input":{"stdin":"interleave"},"submitted_by":"race"}`))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		s.handleSubmit(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Errorf("窗口内提交应 202, got %d: %s", rec.Code, rec.Body.String())
			return
		}
		var ack struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &ack)
		submittedID = ack.TaskID
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/local/tasks", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local/tasks 应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if submittedID == "" {
		t.Fatal("读取窗口内的提交未发生（钩子未触发）")
	}
	var body struct {
		Tasks []struct {
			TaskID string `json:"task_id"`
			Origin string `json:"origin"`
			Status string `json:"status"`
		} `json:"tasks"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Tasks) != 1 {
		t.Fatalf("窗口内提交的任务应恰出列一次, got total=%d rows=%+v", body.Total, body.Tasks)
	}
	r := body.Tasks[0]
	if r.TaskID != submittedID {
		t.Fatalf("出列行应为窗口内提交的任务 %s, got %s", submittedID, r.TaskID)
	}
	if r.Origin != "local" {
		t.Fatalf("origin 应为 local, got %q", r.Origin)
	}
	// 关键回归断言：必为带状态账本行，不得误标为无状态留痕行（修复前顺序下此处
	// 恒为 ""，呈现「已过保留期」）。快照与 runner 竞速：acknowledged|running|
	// completed 三态皆合法（echo 秒级完成），无状态即误判。
	if r.Status != "acknowledged" && r.Status != "running" && r.Status != "completed" {
		t.Fatalf("应为带状态账本行, got status=%q（无状态=被误判为已清理留痕行）", r.Status)
	}
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
