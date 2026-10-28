// grouphost_test.go jev 群主持人接力棒（DESIGN-butler 方案三 3.6 v4）：建群点名
// 第一棒 → 棒任务终态自动推进下一轮 → FINAL/预算/降级停轮挂起（人话注记、
// HostActive 保持）→ HostTurn CAS 防重入（并发终态回调只推进一次）。
// 建群 v4 语义（members 必填 / jev_host 开关 / auto_members 兼容回退）一并锁定。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// newHostFixtureServer 同 newAutoFixtureServer，但群快照可预置 seedJSON（预算
// 停轮等经 REST 造不出的主持状态注入用；"" = 空状态起步）。
func newHostFixtureServer(t *testing.T, mp decision.Provider, seedJSON string) (*Server, *fixture) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gdir := t.TempDir()
	if seedJSON != "" {
		// groups.json 为 group 包快照文件名（包内私有小写常量，此处按字面量注入）
		if err := os.WriteFile(filepath.Join(gdir, "groups.json"), []byte(seedJSON), 0o644); err != nil {
			t.Fatalf("预置群快照: %v", err)
		}
	}
	gs, err := group.Open(gdir)
	if err != nil {
		t.Fatalf("打开群 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0,
		OnTaskTerminal: func(tk protocol.Task) {
			_, _ = gs.RefluxTaskResult(tk.ID, group.TaskTerminalBody(tk))
		}})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	s := New(Config{Core: c, AdminToken: "test-admin-token", Groups: gs,
		DataDir:  t.TempDir(),
		Decision: decision.NewService(mp, time.Second)})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// waitFor 轮询至条件成立（终态钩子的推进 goroutine 异步，5ms 步进）；超时 Fatal。
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// completeTask 以 node result 帧置任务 completed（真实终态钩子链路：hub 收帧 →
// fireTerminal → 回流闭包 + 主持人推进钩子）。
func completeTask(t *testing.T, f *fixture, node, taskID, answer string) {
	t.Helper()
	res := protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: answer,
		FinishedAt: protocol.NowUTC()}
	payload := mustJSON(t, protocol.TaskResultPayload{TaskID: taskID, Status: res.Status, Result: &res})
	if err := f.bus.Endpoint(node).Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: payload,
	}); err != nil {
		t.Fatalf("发 result 帧: %v", err)
	}
}

// registerOnline 注册节点 + exposed 卡 + 上线（候选面准备）。
func registerOnline(t *testing.T, f *fixture, node, agent string) {
	t.Helper()
	if err := f.core.RegisterNode(node, []protocol.AgentCard{{Name: agent, Exposed: true}}); err != nil {
		t.Fatalf("注册 %s/%s: %v", node, agent, err)
	}
	f.core.SetOnline(node, true)
}

// hostState 读群主持状态快照（断言辅助）。
func hostState(s *Server, groupID string) (group.Group, bool) {
	return s.cfg.Groups.GetGroup(groupID)
}

// countNotes 统计群内命中子串的系统注记条数。
func countNotes(t *testing.T, f *fixture, hc *http.Client, groupID, substr string) int {
	t.Helper()
	msgs := f.groupMsgs(hc, groupID)
	n := 0
	for _, m := range msgs {
		if m.Kind == "system" && strings.Contains(m.Body, substr) {
			n++
		}
	}
	return n
}

// TestHostFirstBatonOnCreate 建群即点名第一棒（jev_host=true）：HostActive 置位、
// 恰一棒在途（ghost 键/group_ref/点名指引/目标=点名成员）、创建注记恰一条。
func TestHostFirstBatonOnCreate(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	registerOnline(t, f, "win-box", "disk")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"巡检协作","members":["mac-mini/butler","win-box/disk"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}

	grp, ok := hostState(s, g.ID)
	if !ok || !grp.HostActive || grp.HostRound != 1 || grp.HostTurn == "" {
		t.Fatalf("建群应开启主持且首棒在途: %+v ok=%v", grp, ok)
	}
	batons := f.st.View().Tasks
	if len(batons) != 1 {
		t.Fatalf("应恰一棒（非全员并发派发）, got %d", len(batons))
	}
	var baton protocol.Task
	for _, tk := range batons {
		baton = tk
	}
	if baton.ID != grp.HostTurn {
		t.Fatalf("HostTurn 应指向在途棒: turn=%q baton=%q", grp.HostTurn, baton.ID)
	}
	if baton.GroupRef != g.ID || baton.TargetNode != "mac-mini" || baton.TargetAgent != "butler" {
		t.Fatalf("棒任务应挂群且目标=点名成员: %+v", baton)
	}
	if want := hostBatonIdem(g.ID, 1); baton.IdemKey != want {
		t.Fatalf("幂等键应 ghost-<gid>-<round>: got %q want %q", baton.IdemKey, want)
	}
	if !strings.Contains(baton.Input.Stdin, "主持人点名") || strings.Count(baton.Input.Stdin, `kind="agent"`) < 2 {
		t.Fatalf("棒指引应含点名语义且 group_say 示例显式 kind=agent: %.120s", baton.Input.Stdin)
	}
	if n := countNotes(t, f, hc, g.ID, "主持人点名第 1 棒"); n != 1 {
		t.Fatalf("点名创建注记应恰一条, got %d", n)
	}
}

// TestHostAdvanceOnTerminal 终态自动推进（真实钩子链路）：在途棒 completed →
// advanceHostOnTerminal 命中 → 下一棒（round+1、ghost 键轮转、HostTurn 换棒）；
// 非在途棒任务终态不触发推进。
func TestHostAdvanceOnTerminal(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	registerOnline(t, f, "win-box", "disk")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"巡检协作","members":["mac-mini/butler","win-box/disk"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}
	grp, _ := hostState(s, g.ID)
	t1 := grp.HostTurn

	// 反例：与棒无关的任务终态 → 零推进（钩子谓词不命中）
	ack, err := f.core.Submit(protocol.TaskSubmitRequest{TargetNode: "win-box", TargetAgent: "disk",
		Input: protocol.TaskInput{Args: []string{"旁路任务"}}, SubmittedBy: "tester"}, "")
	if err != nil {
		t.Fatalf("旁路任务提交: %v", err)
	}
	completeTask(t, f, "win-box", ack.TaskID, "旁路完成")
	time.Sleep(50 * time.Millisecond)
	if grp2, _ := hostState(s, g.ID); grp2.HostRound != 1 || grp2.HostTurn != t1 {
		t.Fatalf("无关任务终态不得推进主持: round=%d turn=%q", grp2.HostRound, grp2.HostTurn)
	}

	// 在途棒终态 → 自动点名下一棒（等待至 T2 落库：CAS 置棒先于任务持久化，
	// 只看 HostRound 会在置棒与落库之间的窗口假过）
	completeTask(t, f, "mac-mini", t1, "第一棒完成")
	waitFor(t, 2*time.Second, "第二棒应落库且主持推进", func() bool {
		grp2, _ := hostState(s, g.ID)
		if grp2.HostRound != 2 || grp2.HostTurn == "" || grp2.HostTurn == t1 {
			return false
		}
		tk, ok := f.st.View().Tasks[grp2.HostTurn]
		return ok && tk.IdemKey == hostBatonIdem(g.ID, 2) && tk.GroupRef == g.ID
	})
	batons := 0
	for _, tk := range f.st.View().Tasks {
		if tk.GroupRef == g.ID && strings.HasPrefix(tk.IdemKey, "ghost-") {
			batons++
		}
	}
	if batons != 2 {
		t.Fatalf("应恰两棒（无并发多派）, got %d", batons)
	}
	if n := countNotes(t, f, hc, g.ID, "主持人点名第 2 棒"); n != 1 {
		t.Fatalf("第二棒点名注记应恰一条, got %d", n)
	}
}

// TestHostStopOnFinal FINAL 停轮：群内出现 FINAL: 收敛候选后，在途棒终态推进时
// 不再点名——HostTurn 释放（循环挂起）、HostActive 保持、停轮注记人话。
func TestHostStopOnFinal(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"收敛验证","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}
	grp, _ := hostState(s, g.ID)
	t1 := grp.HostTurn

	// 成员落 FINAL: 发言（收敛候选登记）
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"butler","kind":"agent","body":"FINAL: 共识已达成"}`); code != http.StatusCreated {
		t.Fatal("FINAL 发言应 201")
	}

	// 在途棒终态 → 推进命中停轮（同步直调 advanceHost，断言确定）
	s.advanceHost(grp, protocol.Task{ID: t1})
	grp2, _ := hostState(s, g.ID)
	if !grp2.HostActive || grp2.HostTurn != "" || grp2.HostRound != 1 {
		t.Fatalf("FINAL 后应停轮挂起（HostActive 保持/HostTurn 清空/轮数不动）: %+v", grp2)
	}
	if n := countNotes(t, f, hc, g.ID, "主持人停轮"); n != 1 {
		t.Fatalf("停轮注记应恰一条, got %d", n)
	}
	if len(f.st.View().Tasks) != 1 {
		t.Fatalf("停轮后不得派新棒, got %d 个任务", len(f.st.View().Tasks))
	}
}

// TestHostStopOnBudget 预算停轮：HostRound 达 Budget.MaxRounds 即不再点名
//（发言与轮转同一预算旋钮）。主持状态经快照预置注入（REST 造不出该状态）。
func TestHostStopOnBudget(t *testing.T) {
	seed := `{"groups":{"g-seed":{"group_id":"g-seed","goal":"预算停轮","status":"discussing",
"host":"admin","members":["mac-mini/butler"],"budget":{"max_rounds":1},
"created_at":"2026-10-28T00:00:00Z","task_refs":["t-prev"],
"host_active":true,"host_round":1,"host_turn":"t-prev"}},"messages":{},"seqs":{"g-seed":0}}`
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, seed)
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	grp, ok := hostState(s, "g-seed")
	if !ok || grp.HostTurn != "t-prev" {
		t.Fatalf("预置主持状态应就位: %+v ok=%v", grp, ok)
	}
	s.advanceHost(grp, protocol.Task{ID: "t-prev"})
	grp2, _ := hostState(s, "g-seed")
	if !grp2.HostActive || grp2.HostTurn != "" || grp2.HostRound != 1 {
		t.Fatalf("预算尽应停轮挂起: %+v", grp2)
	}
	if n := countNotes(t, f, hc, "g-seed", "预算上限"); n != 1 {
		t.Fatalf("预算停轮注记应恰一条, got %d", n)
	}
	if len(f.st.View().Tasks) != 0 {
		t.Fatalf("预算尽不得派新棒, got %d", len(f.st.View().Tasks))
	}
}

// TestHostSuspendOnDegraded jev 降级/无候选挂起：provider 失败（降级）、NoFit、
// 成员全不在线三种点名失败均挂起（HostTurn 空、轮数不动）+ 人话注记；对照：
// 决策未接线建群照常成立但不开启主持（人话注记「主持人未开启」）。
func TestHostSuspendOnDegraded(t *testing.T) {
	cases := []struct {
		name     string
		mp       *autoMemberMock
		register bool
		noteSub  string
	}{
		{"provider失败", &autoMemberMock{pickErr: context.DeadlineExceeded}, true, "暂不可用"},
		{"无合适成员", &autoMemberMock{}, true, "不适合"},
		{"成员全不在线", &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}, false, "均不在线或能力未开放"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newHostFixtureServer(t, tc.mp, "")
			if tc.register {
				registerOnline(t, f, "mac-mini", "butler")
			}
			hc := f.loginClient(true)
			code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
				`{"goal":"降级挂起","members":["mac-mini/butler"],"jev_host":true}`)
			if code != http.StatusCreated {
				t.Fatalf("点名失败建群仍应 201（best-effort）, got %d: %s", code, body)
			}
			var g struct {
				ID string `json:"group_id"`
			}
			if err := json.Unmarshal([]byte(body), &g); err != nil || g.ID == "" {
				t.Fatalf("201 体不符: %s", body)
			}
			grp, _ := hostState(s, g.ID)
			if !grp.HostActive || grp.HostTurn != "" || grp.HostRound != 0 {
				t.Fatalf("点名失败应挂起（HostActive 保持/无在途棒）: %+v", grp)
			}
			if n := countNotes(t, f, hc, g.ID, "主持人停轮"); n != 1 {
				t.Fatalf("挂起注记应恰一条, got %d", n)
			}
			if n := countNotes(t, f, hc, g.ID, tc.noteSub); n != 1 {
				t.Fatalf("注记应带人话原因（%s）, got %d 条含 %q", tc.noteSub, n, tc.noteSub)
			}
			if len(f.st.View().Tasks) != 0 {
				t.Fatalf("挂起不得派棒, got %d", len(f.st.View().Tasks))
			}
		})
	}

	// 对照：决策未接线（newGroupFixture 无 Decision）→ 建群照常、不开主持
	f := newGroupFixture(t)
	hc := f.loginClient(true)
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"无决策","members":["a"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("决策未接线建群应 201, got %d: %s", code, body)
	}
	msgs := f.groupMsgs(hc, mustGroupID(t, body))
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "主持人未开启") {
		t.Fatalf("决策未接线应注记「主持人未开启」, got %+v", msgs)
	}
}

// mustGroupID 从建群 201 响应体提取 group_id。
func mustGroupID(t *testing.T, body []byte) string {
	t.Helper()
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal(body, &g); err != nil || g.ID == "" {
		t.Fatalf("201 体不符: %s", body)
	}
	return g.ID
}

// TestHostTurnReentryGuard HostTurn CAS 防重入：同一在途棒的并发终态回调只有
// CAS 赢家推进一次——恰一棒新派、轮数 +1、败者不派单不注记。
func TestHostTurnReentryGuard(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	registerOnline(t, f, "win-box", "disk")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"防重入","members":["mac-mini/butler","win-box/disk"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)
	grp, _ := hostState(s, gid)
	t1 := grp.HostTurn

	const rivals = 8
	var wg sync.WaitGroup
	for i := 0; i < rivals; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.advanceHost(grp, protocol.Task{ID: t1})
		}()
	}
	wg.Wait()

	grp2, _ := hostState(s, gid)
	if grp2.HostRound != 2 || grp2.HostTurn == "" || grp2.HostTurn == t1 {
		t.Fatalf("并发推进应恰好一轮: round=%d turn=%q", grp2.HostRound, grp2.HostTurn)
	}
	tasks := f.st.View().Tasks
	if len(tasks) != 2 {
		t.Fatalf("并发败者不得派棒: got %d 个任务", len(tasks))
	}
	if tk := tasks[grp2.HostTurn]; tk.IdemKey != hostBatonIdem(gid, 2) {
		t.Fatalf("新棒幂等键应 ghost-<gid>-2: %q", tk.IdemKey)
	}
	if n := countNotes(t, f, hc, gid, "主持人点名第 2 棒"); n != 1 {
		t.Fatalf("第二棒注记应恰一条（败者不发声）, got %d", n)
	}
}

// TestGroupCreateV4Semantics v4 建群语义：members 必填（人话 400）、jev_host
// 显式关闭不开主持、auto_members 与手选 members 共存时以传入为准且主持开启。
func TestGroupCreateV4Semantics(t *testing.T) {
	t.Run("members必填", func(t *testing.T) {
		f := newGroupFixture(t)
		hc := f.loginClient(true)
		code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"缺成员"}`)
		if code != http.StatusBadRequest || !strings.Contains(string(body), "请先选择成员") {
			t.Fatalf("缺 members 应 400 人话提示, got %d: %s", code, body)
		}
	})
	t.Run("jev_host显式关闭", func(t *testing.T) {
		f := newGroupFixture(t)
		hc := f.loginClient(true)
		code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
			`{"goal":"手动群","members":["a"],"jev_host":false}`)
		if code != http.StatusCreated {
			t.Fatalf("应 201, got %d: %s", code, body)
		}
		msgs := f.groupMsgs(hc, mustGroupID(t, body))
		for _, m := range msgs {
			if m.Kind == "system" && (strings.Contains(m.Body, "主持人") || strings.Contains(m.Body, "点名")) {
				t.Fatalf("未开主持不得有主持注记: %+v", msgs)
			}
		}
	})
	t.Run("auto_members与手选共存以传入为准", func(t *testing.T) {
		mp := &autoMemberMock{members: []string{"other@elsewhere"}, pickNode: "mac-mini", pickAgent: "butler"}
		s, f := newHostFixtureServer(t, mp, "")
		registerOnline(t, f, "mac-mini", "butler")
		hc := f.loginClient(true)
		code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
			`{"goal":"兼容路径","members":["mac-mini/butler"],"auto_members":true}`)
		if code != http.StatusCreated {
			t.Fatalf("应 201, got %d: %s", code, body)
		}
		var g struct {
			Members []string `json:"members"`
		}
		if err := json.Unmarshal(body, &g); err != nil {
			t.Fatal(err)
		}
		if len(g.Members) != 1 || g.Members[0] != "mac-mini/butler" {
			t.Fatalf("members 非空时不得被 jev 结果覆盖: %v", g.Members)
		}
		grp, _ := hostState(s, mustGroupID(t, body))
		if !grp.HostActive || grp.HostRound != 1 {
			t.Fatalf("auto_members 应开启主持并派首棒: %+v", grp)
		}
		if len(f.st.View().Tasks) != 1 {
			t.Fatalf("应恰首棒一单, got %d", len(f.st.View().Tasks))
		}
	})
}

// hostBlockingProvider 可控阻塞的决策 mock：Suggest 进入即关 entered、阻塞至
// release 关闭后返回固定成员——构造「点名决策等待窗口内群状态变化」的确定性
// 时序（不依赖 sleep；生产 Suggest 预算 15s，阻塞窗口远小于预算）。
type hostBlockingProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (*hostBlockingProvider) Name() string  { return "blocking" }
func (*hostBlockingProvider) Model() string { return "blocking" }
func (p *hostBlockingProvider) Suggest(ctx context.Context, _ decision.Request) (*decision.Suggestion, error) {
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &decision.Suggestion{TargetNode: "mac-mini", TargetAgent: "butler"}, nil
}

// hostCaptureProvider 捕获 Suggest 请求的决策 mock：断言主持人点名 goal 组装
// （P2④ 修复：主持人指令/近期发言/摘要裁剪边界）。
type hostCaptureProvider struct {
	mu   sync.Mutex
	reqs []decision.Request
}

func (*hostCaptureProvider) Name() string  { return "capture" }
func (*hostCaptureProvider) Model() string { return "capture" }
func (p *hostCaptureProvider) Suggest(_ context.Context, req decision.Request) (*decision.Suggestion, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return &decision.Suggestion{TargetNode: "mac-mini", TargetAgent: "butler"}, nil
}
func (p *hostCaptureProvider) captured() []decision.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]decision.Request(nil), p.reqs...)
}

// TestHostStopOnSpeechBudget 真实发言预算停轮（v4 审查修复①，codex 复现
// TestReviewActualSpeechBudgetStopsHost 等效固化）：第一棒期间多条 agent 发言把
// 持久化 AgentMsgs 累计打满（HostRound 不动、第 max+1 条被拒），在途棒终态推进
// 不得派下一棒——发言数与轮转数不等价（一棒可发多条、其他 agent 可并发发言），
// 推进按真实累计口径复核（hostNextBaton 预检 + AdvanceHostTurn 事务内双道）。
func TestHostStopOnSpeechBudget(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"预算实算","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)
	grp, _ := hostState(s, gid)
	t1 := grp.HostTurn

	// 一棒多条：发满预算（轮数仍 1），第 max+1 条被拒
	for i := 0; i < grp.Budget.MaxRounds; i++ {
		if _, err := s.cfg.Groups.PostMessage(gid, "mac-mini/butler", group.KindAgent, "贡献观点"); err != nil {
			t.Fatalf("预算内发言应成功（第 %d 条）: %v", i+1, err)
		}
	}
	if _, err := s.cfg.Groups.PostMessage(gid, "mac-mini/butler", group.KindAgent, "超预算"); !errors.Is(err, group.ErrBudgetExhausted) {
		t.Fatalf("超预算发言应 ErrBudgetExhausted, got %v", err)
	}
	if used, _ := s.cfg.Groups.SpeechBudgetUsed(gid); used != grp.Budget.MaxRounds {
		t.Fatalf("真实累计发言应 %d, got %d", grp.Budget.MaxRounds, used)
	}

	s.advanceHost(grp, protocol.Task{ID: t1})
	grp2, _ := hostState(s, gid)
	if !grp2.HostActive || grp2.HostTurn != "" || grp2.HostRound != 1 {
		t.Fatalf("真实发言预算耗尽应停轮挂起（轮数不动/无在途棒）: %+v", grp2)
	}
	if len(f.st.View().Tasks) != 1 {
		t.Fatalf("发言预算耗尽不得派新棒, got %d 个任务", len(f.st.View().Tasks))
	}
	if n := countNotes(t, f, hc, gid, "发言预算已用尽"); n != 1 {
		t.Fatalf("发言预算停轮注记应恰一条, got %d", n)
	}
}

// TestHostStopOnSpeechBudgetAfterPrune 消息裁剪后预算不失忆（v4 审查修复①）：
// agent 发言计满后灌滚动窗口把全部 agent 消息挤出（可见 agent 发言归零），推进
// 仍按持久化 AgentMsgs 累计停轮——预算判定禁止以可见消息数推算。
func TestHostStopOnSpeechBudgetAfterPrune(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"裁剪后停轮","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)
	grp, _ := hostState(s, gid)

	for i := 0; i < grp.Budget.MaxRounds; i++ { // 发言计满
		if _, err := s.cfg.Groups.PostMessage(gid, "mac-mini/butler", group.KindAgent, "贡献观点"); err != nil {
			t.Fatalf("预算内发言: %v", err)
		}
	}
	for i := 0; i < group.MaxGroupMessages; i++ { // 灌满窗口挤出全部 agent 消息
		if _, err := s.cfg.Groups.PostMessage(gid, "admin", group.KindUser, "插话"); err != nil {
			t.Fatalf("插话: %v", err)
		}
	}
	msgs, _, err := s.cfg.Groups.ListMessages(gid, 0)
	if err != nil {
		t.Fatalf("拉取消息: %v", err)
	}
	visible := 0
	for _, m := range msgs {
		if m.Kind == group.KindAgent {
			visible++
		}
	}
	if visible != 0 {
		t.Fatalf("前置：可见 agent 消息应为 0（全部被裁剪）, got %d", visible)
	}
	if used, _ := s.cfg.Groups.SpeechBudgetUsed(gid); used != grp.Budget.MaxRounds {
		t.Fatalf("裁剪后真实累计应保持 %d, got %d", grp.Budget.MaxRounds, used)
	}

	s.advanceHost(grp, protocol.Task{ID: grp.HostTurn})
	grp2, _ := hostState(s, gid)
	if grp2.HostTurn != "" || grp2.HostRound != 1 || len(f.st.View().Tasks) != 1 {
		t.Fatalf("消息裁剪后仍应按真实累计停轮: %+v tasks=%d", grp2, len(f.st.View().Tasks))
	}
	if n := countNotes(t, f, hc, gid, "发言预算已用尽"); n != 1 {
		t.Fatalf("停轮注记应恰一条, got %d", n)
	}
}

// TestHostStopFinalDuringDecision 决策等待期间落 FINAL 仍不得派棒（v4 审查修复②，
// codex 复现 TestReviewFinalDuringDecisionStopsHost 等效固化）：Suggest 阻塞期间
// 群内落 FINAL: 发言（收敛标记同事务持久化），决策返回合法成员后停轮复核与
// HostTurn CAS 在同一 store 事务命中——轮数不动、无新棒、HostTurn 清空挂起。
func TestHostStopFinalDuringDecision(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"决策期FINAL","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)
	grp, _ := hostState(s, gid)

	p := &hostBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s.cfg.Decision = decision.NewService(p, 0) // 默认预算 15s，阻塞至 release 放行
	done := make(chan struct{})
	go func() { defer close(done); s.advanceHost(grp, protocol.Task{ID: grp.HostTurn}) }()
	<-p.entered // 预检已过、点名决策在途
	if _, err := s.cfg.Groups.PostMessage(gid, "mac-mini/butler", group.KindAgent, "FINAL: agreed"); err != nil {
		t.Fatalf("决策期间 FINAL 发言: %v", err)
	}
	if got, _ := hostState(s, gid); !got.FinalSeen {
		t.Fatal("决策等待窗口内落账的 FINAL 应持久化收敛标记")
	}
	close(p.release)
	<-done

	after, _ := hostState(s, gid)
	if after.HostRound != 1 || after.HostTurn != "" || !after.HostActive {
		t.Fatalf("决策期间落 FINAL 应停轮挂起（round 不动/HostTurn 清空/HostActive 保持）: %+v", after)
	}
	if len(f.st.View().Tasks) != 1 {
		t.Fatalf("决策期间 FINAL 不得派新棒, got %d 个任务", len(f.st.View().Tasks))
	}
	if n := countNotes(t, f, hc, gid, "主持人停轮"); n != 1 {
		t.Fatalf("停轮注记应恰一条, got %d", n)
	}
}

// TestHostStopSpeechBudgetDuringDecision 决策等待期间预算耗尽仍不得派棒（v4 审查
// 修复①事务内复核道）：预检通过后 Suggest 阻塞，期间其他 agent 并发发言把
// AgentMsgs 打满；决策返回合法成员后推进事务内复核命中发言预算——停轮挂起。
func TestHostStopSpeechBudgetDuringDecision(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	s, f := newHostFixtureServer(t, mp, "")
	registerOnline(t, f, "mac-mini", "butler")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"决策期预算","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)
	grp, _ := hostState(s, gid)

	p := &hostBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	s.cfg.Decision = decision.NewService(p, 0)
	done := make(chan struct{})
	go func() { defer close(done); s.advanceHost(grp, protocol.Task{ID: grp.HostTurn}) }()
	<-p.entered
	for i := 0; i < grp.Budget.MaxRounds; i++ { // 决策窗口内并发发言打满预算
		if _, err := s.cfg.Groups.PostMessage(gid, "mac-mini/butler", group.KindAgent, "并发补充"); err != nil {
			t.Fatalf("决策期间发言: %v", err)
		}
	}
	close(p.release)
	<-done

	after, _ := hostState(s, gid)
	if after.HostRound != 1 || after.HostTurn != "" || len(f.st.View().Tasks) != 1 {
		t.Fatalf("决策期间预算耗尽应停轮挂起: %+v tasks=%d", after, len(f.st.View().Tasks))
	}
	if n := countNotes(t, f, hc, gid, "发言预算已用尽"); n != 1 {
		t.Fatalf("发言预算停轮注记应恰一条, got %d", n)
	}
}

// TestHostGoalDigestModeratorSemantics 主持人点名语义（v4 审查修复④）：捕获
// Suggest 请求断言 goal 组装——主持人指令显式在场（选下一位能新增贡献的发言者/
// 依据讨论进展避免重复/协作已完成选 none）、旧「你是第一棒开场」角色混淆文案退役；
// 近期成员发言进入摘要，且末 8 条裁剪边界成立（更早消息不出现、超长发言截 80 rune）。
func TestHostGoalDigestModeratorSemantics(t *testing.T) {
	cp := &hostCaptureProvider{}
	s, f := newHostFixtureServer(t, cp, "")
	registerOnline(t, f, "mac-mini", "butler")
	registerOnline(t, f, "win-box", "disk")
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"点名语义验证","members":["mac-mini/butler","win-box/disk"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	gid := mustGroupID(t, body)

	// 第一次点名（第一棒）：主持人指令 + 空摘要开场口径
	reqs := cp.captured()
	if len(reqs) != 1 {
		t.Fatalf("建群应恰好一次点名, got %d", len(reqs))
	}
	g0 := reqs[0].Goal
	for _, want := range []string{
		"主持人点名下一棒发言者", "新增贡献", "避免重复", "choice 填 none",
		"不是任务派单", "点名语义验证", "群内暂无发言：请选出第一棒开场成员",
	} {
		if !strings.Contains(g0, want) {
			t.Fatalf("点名 goal 缺 %q: %s", want, g0)
		}
	}
	if strings.Contains(g0, "你是第一棒开场") {
		t.Fatal("旧「你是第一棒开场」角色混淆文案应退役（开场=选第一位成员，非模型自己开场）")
	}

	// 造 >8 条消息：更早标记句 + 超长发言 + 7 条填充（窗口外/窗口内边界齐备）
	say := func(from, bodyStr string) {
		if c, b := f.do(hc, http.MethodPost, "/api/v1/groups/"+gid+"/messages",
			`{"from":"`+from+`","kind":"agent","body":"`+bodyStr+`"}`); c != http.StatusCreated {
			t.Fatalf("发言应 201, got %d: %s", c, b)
		}
	}
	say("butler", "更早的第一条发言 EARLY-MARKER")
	long := strings.Repeat("长", 100)
	say("disk", long)
	for i := 0; i < 7; i++ {
		say("butler", fmt.Sprintf("补充讨论 %d", i+1))
	}

	// 第二次点名（直调推进预检白盒，终态钩子时序无关）：末 8 条窗口边界
	grp, ok := hostState(s, gid)
	if !ok {
		t.Fatal("群应存在")
	}
	member, stopNote := s.hostNextBaton(grp)
	if member == "" {
		t.Fatalf("点名不应停轮: %s", stopNote)
	}
	reqs = cp.captured()
	if len(reqs) != 2 {
		t.Fatalf("应捕获两次点名, got %d", len(reqs))
	}
	g1 := reqs[1].Goal
	if strings.Contains(g1, "EARLY-MARKER") {
		t.Fatal("摘要应只含末 8 条：更早消息不得进入点名参考")
	}
	if !strings.Contains(g1, "补充讨论 7") {
		t.Fatal("摘要应含最新发言（点名参考数据源）")
	}
	if !strings.Contains(g1, strings.Repeat("长", 80)+"…") || strings.Contains(g1, strings.Repeat("长", 81)) {
		t.Fatal("超长发言应按 80 rune 截断")
	}
	if !strings.Contains(g1, "主持人点名下一棒发言者") {
		t.Fatal("后续轮点名 goal 同样带主持人指令（非仅第一棒）")
	}
}
