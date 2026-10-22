package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corepkg "github.com/mtl802/swarmmind/internal/hub/core"
	sessionpkg "github.com/mtl802/swarmmind/internal/hub/session"
	storepkg "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/conn"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// Loop 7 全链验收（真实 WSS）：本机控制台 PATCH exposed → card_update 上报 →
// hub 注册表更新 → 新任务 404 agent_not_exposed；重开 → 网内恢复可派单。
// 覆盖 SPEC 4.9 开关语义：立即生效、已有任务继续执行、hub 不可达时待同步。

func waitFor(d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestExposedToggleE2E 开关全链：持久化（重启保持）+ card_update 上报 + 新任务 404。
func TestExposedToggleE2E(t *testing.T) {
	// ---- hub 栈：store + core + WSS 会话入口（与 session 包 e2e 同构） ----
	hubStore, err := storepkg.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hubStore.Close() })
	var coreSvc *corepkg.Core
	mgr := sessionpkg.NewManager(sessionpkg.Config{
		Store:  hubStore,
		Tokens: map[string]string{"nodeA": "tok-a"},
		SetOnline: func(nodeID string, online bool) { // 上下线回调接 core（dispatch 依赖在线表）
			if coreSvc != nil {
				coreSvc.SetOnline(nodeID, online)
			}
		},
	})
	coreSvc, err = corepkg.New(corepkg.Config{Store: hubStore, Transport: mgr, SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Subscribe(coreSvc.HandleFrame)
	hubSrv := httptest.NewServer(mgr)
	t.Cleanup(func() {
		_ = mgr.CloseWithBudget(3 * time.Second)
		_ = coreSvc.Close()
		hubSrv.Close()
	})
	wsURL := "ws" + strings.TrimPrefix(hubSrv.URL, "http") + "/ws/node"

	// ---- node 栈：conn + service + 真账本 + 真 runner（echo） ----
	dataDir := t.TempDir()
	profiles := []protocol.ExecProfile{{
		Name: "echo", Kind: protocol.AgentKindCLI, Command: "/bin/echo",
		Workdir: t.TempDir(), TimeoutSec: 10, AllowArgs: true, Exposed: true,
		Description: "回声测试", PermissionMode: "no-shell",
	}}
	agentState, err := OpenAgentState(dataDir, profiles)
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var svc *svcpkg.Service
	client := conn.NewClient(conn.Config{
		HubURL: wsURL, NodeID: "nodeA", Token: "tok-a",
		AgentsFunc:    agentState.Cards, // 每次连接取运行时能力表
		TLSSkipVerify: true,
		OnRegistered: func() {
			if svc != nil {
				svc.ResendPendingAsync()
				agentState.Sync(svc.SendCardUpdate)
			}
		},
	})
	t.Cleanup(func() { _ = client.Close() })
	svc = svcpkg.New(svcpkg.Config{Ledger: led, Runners: runners}, client)
	t.Cleanup(svc.Close)
	go func() { _ = client.Run(ctx) }()

	// ---- 本地 API（控制台入口） ----
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents: agentState, Ledger: led, TaskLog: taskLog,
		CardsReporter:    svc.SendCardUpdate,
		Online:           client.Online,
		Epoch:            client.Epoch,
		LastRegisteredAt: client.LastRegisteredAt,
		LocalToken:       "tok-local",
		PrimeToken:       "tok-prime",
		PrimeAgent:       "echo",
	}).Handler())
	t.Cleanup(srv.Close)

	// doAs 指定凭证调用（编排操作需 prime_token；local_token 调编排 → 403，矩阵 #14）。
	doAs := func(token, method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// do 本机管理操作（local_token 级：exposed 开关、只读查询）。
	do := func(method, path, body string) (int, string) {
		return doAs("tok-local", method, path, body)
	}
	hubExposed := func() bool {
		rec := hubStore.View().Nodes["nodeA"]
		for _, card := range rec.Agents {
			if card.Name == "echo" {
				return card.Exposed
			}
		}
		return false
	}

	if !waitFor(5*time.Second, func() bool {
		_, ok := client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("node 应在 5s 内注册成功")
	}
	if !waitFor(2*time.Second, hubExposed) {
		t.Fatal("前置：注册能力表应使 hub 侧 echo exposed=true")
	}

	// 未知 agent → 404 agent_not_found
	if code, out := do(http.MethodPatch, "/api/v1/local/agents/ghost/exposed", `{"exposed":false}`); code != http.StatusNotFound || !strings.Contains(out, protocol.CodeAgentNotFound) {
		t.Fatalf("未知 agent 应 404 agent_not_found, got %d: %s", code, out)
	}

	// ---- 关开关：200 + hub_synced=true；card_update 经 WSS 入 hub 注册表 ----
	code, out := do(http.MethodPatch, "/api/v1/local/agents/echo/exposed", `{"exposed":false}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH false 应 200, got %d: %s", code, out)
	}
	var pr struct {
		Name      string `json:"name"`
		Exposed   bool   `json:"exposed"`
		HubSynced bool   `json:"hub_synced"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || pr.Exposed || !pr.HubSynced {
		t.Fatalf("PATCH 应答不符: %s err=%v", out, err)
	}
	if !waitFor(5*time.Second, func() bool { return !hubExposed() }) {
		t.Fatalf("card_update 上报后 hub 注册表应 exposed=false（card=%+v）", hubStore.View().Nodes["nodeA"].Agents)
	}
	// 重启保持：同 data_dir 重开 AgentState，开关仍为关
	reopened, err := OpenAgentState(dataDir, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Effective()[0].Exposed {
		t.Fatal("重启后开关应保持为关（agents-state.json）")
	}

	// ---- 本机主权：exposed=false 不拦本机入口（zcode 主权裁决 + 方案 3.2：
	// exposed 是对 hub 准入的开关，本机主人/总理派单不受其约束）；非总理凭证
	// 调编排 → 403（矩阵 #14，环防护可测化） ----
	if code, out = doAs("tok-local", http.MethodPost, "/api/v1/tasks", `{"target_node":"nodeA","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester"}`); code != http.StatusForbidden || !strings.Contains(out, "prime_required") {
		t.Fatalf("local_token 调编排应 403 prime_required, got %d: %s", code, out)
	}
	code, out = doAs("tok-prime", http.MethodPost, "/api/v1/tasks", `{"target_node":"nodeA","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester"}`)
	if code != http.StatusAccepted {
		t.Fatalf("关闭 exposed 后本机入口（总理）派单应 202（主权在本机）, got %d: %s", code, out)
	}

	// ---- 重开开关：网内恢复可派单；提交 202 且本机留痕 ----
	code, out = do(http.MethodPatch, "/api/v1/local/agents/echo/exposed", `{"exposed":true}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH true 应 200, got %d: %s", code, out)
	}
	if !waitFor(5*time.Second, hubExposed) {
		t.Fatal("重开后 hub 注册表应 exposed=true")
	}
	code, out = doAs("tok-prime", http.MethodPost, "/api/v1/tasks", `{"target_node":"nodeA","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester","idem_key":"loop7-1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("重开后提交应 202, got %d: %s", code, out)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(out), &ack); err != nil || ack.TaskID == "" {
		t.Fatalf("提交应答不符: %s", out)
	}
	// 我的任务留痕（submit_ack 成功即登记）：两条 —— exposed=false 时的主权
	// 派单（无幂等键）与本条（idem loop7-1），均合法入账
	if !waitFor(2*time.Second, func() bool { return len(taskLog.List()) == 2 }) {
		t.Fatalf("提交成功应留痕（含 exposed=false 期间的主权派单）: %+v", taskLog.List())
	}
	// 已有任务语义佐证：关开关不影响在途任务——该任务应照常跑到终态 completed
	if !waitFor(5*time.Second, func() bool {
		e, ok := led.Get(ack.TaskID)
		return ok && e.State == ledger.StateFinished && e.Result != nil && e.Result.Status == protocol.ResultCompleted
	}) {
		t.Fatal("任务应正常执行至 completed（开关不影响在途任务）")
	}
}
