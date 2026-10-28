// groups_auto_test.go 群聊多 agent 协作（DESIGN-butler 方案三，2026-10-28 伦哥
// 口径）：auto_members jev 智能选人 + 协作任务派发、FINAL 收敛注记、任务详情
// related_works 下钻。降级矩阵全覆盖（未启用/降级/NoFit）。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// autoMemberMock 决策 mock：单选 Suggest 可编程（jev 主持人点名）+ 多选
// SuggestMembers 可编程（建群自动选人回退）。
type autoMemberMock struct {
	members []string // SuggestMembers 多选返回
	reason  string
	err     error
	// pickNode/pickAgent Suggest 单选返回（主持人点名目标）；两者皆空时返回
	// ErrNoFit（与 v3 mock 行为一致：未编程即无建议）。
	pickNode  string
	pickAgent string
	pickErr   error
}

func (m *autoMemberMock) Name() string  { return "mock-jev" }
func (m *autoMemberMock) Model() string { return "mock-model" }
func (m *autoMemberMock) Suggest(ctx context.Context, req decision.Request) (*decision.Suggestion, error) {
	if m.pickErr != nil {
		return nil, m.pickErr
	}
	if m.pickNode == "" && m.pickAgent == "" {
		return nil, decision.ErrNoFit
	}
	return &decision.Suggestion{TargetNode: m.pickNode, TargetAgent: m.pickAgent}, nil
}
func (m *autoMemberMock) SuggestMembers(ctx context.Context, goal string, cands []decision.Candidate) ([]string, string, error) {
	return m.members, m.reason, m.err
}

// newAutoFixtureServer 同 newAutoFixture，另返回 *Server 句柄（幂等键重放/手动
// 补派等包内 white-box 场景用；REST 黑盒路径仍走 fixture.srv）。
func newAutoFixtureServer(t *testing.T, mp decision.Provider) (*Server, *fixture) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := group.Open(t.TempDir())
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
		// DataDir 装配 PAT/账户 store（生产同款 New→newTokenStore 初始化路径）：
		// 群协作集成测试经 Bearer PAT 走真实鉴权链（groups_collab_integration_test.go）
		DataDir: t.TempDir(),
		Decision: decision.NewService(mp, time.Second)})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// newAutoFixture 群基座 + 决策注入（auto_members 链路专用）：newGroupFixture 不带
// Decision，本函数自建同款基座补注（Config.Decision 只能在装配时给；终态回流钩子
// 与生产接线同款）。
func newAutoFixture(t *testing.T, mp decision.Provider) *fixture {
	t.Helper()
	_, f := newAutoFixtureServer(t, mp)
	return f
}

// TestGroupAutoMembersJevPick auto_members 兼容路径（v4 主持人语义）：空成员回退
// jev 多选 → 转 node/agent 形态 → 201 + jev 主持开启 + 点名第一棒（ghost 键）。
// v3「建群即全员并发派发」已退役——v4 的唤醒载体是主持人接力棒，删除
// startHostFirstBaton/submitHostBaton 链路本测试必转红（派发对账 R1-07 同口径）。
func TestGroupAutoMembersJevPick(t *testing.T) {
	mp := &autoMemberMock{members: []string{"zcode-dev@mac-mini", "diskcheck@win-box"}, reason: "编码加巡检",
		pickNode: "mac-mini", pickAgent: "zcode-dev"}
	f := newAutoFixture(t, mp)
	// 候选面：两节点在线 + exposed 能力卡（jev 多选与单选点名的判定对象）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "zcode-dev", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	if err := f.core.RegisterNode("win-box", []protocol.AgentCard{{Name: "diskcheck", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("mac-mini", true)
	f.core.SetOnline("win-box", true)
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"盘点磁盘并修复","auto_members":true}`)
	if code != http.StatusCreated {
		t.Fatalf("auto_members 建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID      string   `json:"group_id"`
		Members []string `json:"members"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatal(err)
	}
	if g.ID == "" {
		t.Fatalf("建群响应应带非空 group_id: %s", body)
	}
	if len(g.Members) != 2 || g.Members[0] != "mac-mini/zcode-dev" || g.Members[1] != "win-box/diskcheck" {
		t.Fatalf("jev 结果应转 node/agent 群成员形态: %v", g.Members)
	}

	// 派发对账（R1-07，v4 主持单棒）：恰一棒、目标=点名成员、group_ref 挂群、
	// stdin 指引带 kind=agent、幂等键 ghost-<gid>-<round>。
	tasks := f.st.View().Tasks
	if len(tasks) != 1 {
		t.Fatalf("v4 主持语义应恰派第一棒一单（全员并发派发已退役）, got %d", len(tasks))
	}
	var baton protocol.Task
	for _, tk := range tasks {
		baton = tk
	}
	if baton.GroupRef != g.ID {
		t.Fatalf("棒任务 %s 应挂群 %s, got group_ref %q", baton.ID, g.ID, baton.GroupRef)
	}
	if !strings.Contains(baton.Input.Stdin, `kind="agent"`) || !strings.Contains(baton.Input.Stdin, "主持人点名") {
		t.Fatalf("棒任务 %s stdin 应为主持点名指引（含 kind=agent 示例）: %.80s", baton.ID, baton.Input.Stdin)
	}
	if want := hostBatonIdem(g.ID, 1); baton.IdemKey != want {
		t.Fatalf("幂等键应为 ghost-<gid>-<round>: got %q want %q", baton.IdemKey, want)
	}
	if baton.TargetNode != "mac-mini" || baton.TargetAgent != "zcode-dev" {
		t.Fatalf("第一棒应点名 jev 单选成员, got %s/%s", baton.TargetNode, baton.TargetAgent)
	}

	// 主持状态（GET 群列表视图）：HostActive/HostRound/HostTurn 三字段与在途棒
	// 展示字段（host_turn_member 经任务账本反查）。
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups", "")
	if code != http.StatusOK {
		t.Fatal("群列表应 200")
	}
	var list struct {
		Groups []struct {
			ID             string `json:"group_id"`
			HostActive     bool   `json:"host_active"`
			HostRound      int    `json:"host_round"`
			HostTurn       string `json:"host_turn"`
			HostTurnMember string `json:"host_turn_member"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 1 {
		t.Fatalf("应恰一群, got %d", len(list.Groups))
	}
	v := list.Groups[0]
	if !v.HostActive || v.HostRound != 1 || v.HostTurn != baton.ID || v.HostTurnMember != "mac-mini/zcode-dev" {
		t.Fatalf("主持状态应为首棒在途: %+v（棒任务 %s）", v, baton.ID)
	}
}

// TestGroupAutoMembersDegraded 决策失败 → 503 decision_unavailable 带人话；未启用
// → 503；NoFit → 422 decision_no_fit。三种降级均不建群（回退手选路径）。
func TestGroupAutoMembersDegraded(t *testing.T) {
	cases := []struct {
		name     string
		mp       *autoMemberMock
		wantCode int
		wantKind string
	}{
		{"provider失败", &autoMemberMock{err: context.DeadlineExceeded}, http.StatusServiceUnavailable, "decision_unavailable"},
		{"全不适合", &autoMemberMock{err: decision.ErrNoFit}, http.StatusUnprocessableEntity, "decision_no_fit"},
		{"越界剔除后空", &autoMemberMock{members: []string{"ghost@nowhere"}}, http.StatusUnprocessableEntity, "decision_no_fit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutoFixture(t, tc.mp)
			if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "zcode-dev", Exposed: true}}); err != nil {
				t.Fatal(err)
			}
			f.core.SetOnline("mac-mini", true)
			hc := f.loginClient(true)
			code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"g","auto_members":true}`)
			if code != tc.wantCode || !strings.Contains(string(body), tc.wantKind) {
				t.Fatalf("%s: 应 %d %s, got %d: %s", tc.name, tc.wantCode, tc.wantKind, code, body)
			}
		})
	}
	// Decision 未接线 → 503（与群功能未接线 503 同款护栏，本基座 Decision 为 nil）
	f := newGroupFixture(t)
	hc := f.loginClient(true)
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"g","auto_members":true}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "decision_unavailable") {
		t.Fatalf("未接线应 503, got %d: %s", code, body)
	}
}

// TestGroupSayFinalNote FINAL 收敛注记：agent 发言 FINAL: 前缀 → 系统注记入账本；
// 普通发言/用户插话不触发。
func TestGroupSayFinalNote(t *testing.T) {
	mp := &autoMemberMock{}
	f := newAutoFixture(t, mp)
	hc := f.loginClient(true)

	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"g","members":["a"]}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201: %d %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatalf("建群响应解析: %v（%s）", err, body)
	}
	if g.ID == "" {
		t.Fatalf("群 ID 为空: %s", body)
	}

	// 普通发言：不触发
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"a","kind":"agent","body":"阶段性进展"}`); code != http.StatusCreated {
		t.Fatalf("普通发言应 201, got %d", code)
	}
	// FINAL: 前缀 agent 发言：触发注记
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"a","kind":"agent","body":"FINAL: 最终版本为磁盘清理完成"}`); code != http.StatusCreated {
		t.Fatalf("FINAL 发言应 201, got %d", code)
	}
	// user 插话带 FINAL: 前缀：不触发（仅 agent 发言算收敛候选）
	if code, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"admin","kind":"user","body":"FINAL: 假的"}`); code != http.StatusCreated {
		t.Fatalf("user 插话应 201, got %d", code)
	}

	code, body = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=0", "")
	if code != http.StatusOK {
		t.Fatal("拉取应 200")
	}
	var page struct {
		Messages []struct {
			From string `json:"from"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, m := range page.Messages {
		if m.Kind == "system" && strings.Contains(m.Body, "最终版本候选") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("FINAL 注记应恰好一条, got %d（消息 %d 条）", notes, len(page.Messages))
	}
}

// TestTaskDetailRelatedWorks 任务详情 related_works（DESIGN-butler 方案二）：
// 挂接建单后 GET /tasks/{id} 响应平级携带 related_works（创建降序）。
func TestTaskDetailRelatedWorks(t *testing.T) {
	mp := &autoMemberMock{}
	f := newAutoFixture(t, mp)
	hc := f.loginClient(true)

	// 挂接锚点：任务派给 butler@mac-mini（Orchestrator 卡经 RegisterNode 进目录）
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "butler", Orchestrator: true, Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	bus := transport.NewBus()
	_ = bus
	ack, err := f.core.Submit(protocol.TaskSubmitRequest{TargetNode: "mac-mini", TargetAgent: "butler",
		Input: protocol.TaskInput{Args: []string{"复合任务"}}, SubmittedBy: "tester"}, "")
	if err != nil {
		t.Fatal(err)
	}

	// 管家经 MCP 建单挂接（origin_task）
	code, body := f.do(hc, http.MethodPost, "/api/v1/works", `{"goal":"拆解单","origin_task":"`+ack.TaskID+`",
		"steps":[{"desc":"子步","target_node":"mac-mini","target_agent":"butler"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("挂接建单应 201, got %d: %s", code, body)
	}

	code, body = f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID, "")
	if code != http.StatusOK {
		t.Fatal("任务详情应 200")
	}
	var resp struct {
		RelatedWorks []struct {
			ID        string `json:"id"`
			Goal      string `json:"goal"`
			Steps     int    `json:"steps"`
			DoneSteps int    `json:"done_steps"`
		} `json:"related_works"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.RelatedWorks) != 1 || resp.RelatedWorks[0].Goal != "拆解单" {
		t.Fatalf("related_works 应含挂接单: %+v", resp.RelatedWorks)
	}
	// 卡片进度数据源（R1-06 前端步骤进度 x/y）：摘要必须携带 steps/done_steps
	if resp.RelatedWorks[0].Steps != 1 || resp.RelatedWorks[0].DoneSteps != 0 {
		t.Fatalf("摘要应携带步骤进度: %+v", resp.RelatedWorks[0])
	}
}

// TestTaskDetailRelatedWorksEmptyArray 无挂接任务详情的空数组契约（R1-05）：
// related_works 输出 [] 而非 null，字段不省略——依赖稳定数组形态的 REST 客户端
// 可直接迭代。
func TestTaskDetailRelatedWorksEmptyArray(t *testing.T) {
	f := newAutoFixture(t, &autoMemberMock{})
	hc := f.loginClient(true)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "butler", Orchestrator: true, Exposed: true},
	}); err != nil {
		t.Fatal(err)
	}
	ack, err := f.core.Submit(protocol.TaskSubmitRequest{TargetNode: "mac-mini", TargetAgent: "butler",
		Input: protocol.TaskInput{Args: []string{"无挂接任务"}}, SubmittedBy: "tester"}, "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID, "")
	if code != http.StatusOK {
		t.Fatalf("任务详情应 200, got %d: %s", code, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	rw, ok := raw["related_works"]
	if !ok {
		t.Fatalf("related_works 字段不应省略: %s", body)
	}
	if string(rw) != "[]" {
		t.Fatalf("无挂接应为空数组, got %s", rw)
	}
}

// TestGroupCollabGjoinKeyCollisionMembers gjoin 键无歧义编码（R1-02）：边界成员
// a-b/c 与 a/b-c 的朴素拼接曾碰撞出同键（第二成员被判「同键不同内容」确定性
// 漏派）——修复后两名成员各落一单、幂等键互异且可逆解码、创建注记各一条
//（TaskRefs 外显）。v4 语义转向后 gjoin 键仍在显式多成员补派入口
// dispatchGroupCollab 生效（主持人轮转用 ghost 键，不经过本编码），本测试白盒
// 调用该入口锁键的编码不变。
func TestGroupCollabGjoinKeyCollisionMembers(t *testing.T) {
	s, f := newAutoFixtureServer(t, &autoMemberMock{})
	hc := f.loginClient(true)
	// v4 建群语义：成员主人手选（边界形态 a-b/c 与 a/b-c 原样保留）；补派目标
	// 须 exposed（submit 校验链第 3 条），两节点注册 + 上线
	for node, agent := range map[string]string{"a-b": "c", "a": "b-c"} {
		if err := f.core.RegisterNode(node, []protocol.AgentCard{{Name: agent, Exposed: true}}); err != nil {
			t.Fatal(err)
		}
		f.core.SetOnline(node, true)
	}
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"collaborate","members":["a-b/c","a/b-c"]}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatal(err)
	}
	grp, ok := s.cfg.Groups.GetGroup(g.ID)
	if !ok {
		t.Fatal("群应存在")
	}
	s.dispatchGroupCollab(grp, "admin") // 显式多成员补派入口（v4 保留面）

	tasks := f.st.View().Tasks
	if len(tasks) != 2 {
		t.Fatalf("两名边界成员应各落一个协作任务, got %d: %s", len(tasks), body)
	}
	keys := map[string]bool{}
	for _, tk := range tasks {
		if tk.GroupRef != g.ID {
			t.Fatalf("协作任务应挂群 %s, got %q", g.ID, tk.GroupRef)
		}
		if want := groupJoinIdem(g.ID, tk.TargetNode, tk.TargetAgent); tk.IdemKey != want {
			t.Fatalf("幂等键应为三元组长度前缀编码: got %q want %q", tk.IdemKey, want)
		}
		if keys[tk.IdemKey] {
			t.Fatalf("不同成员幂等键不得相同: %q", tk.IdemKey)
		}
		keys[tk.IdemKey] = true
	}
	if len(keys) != 2 {
		t.Fatalf("幂等键应互异, got %v", keys)
	}
	// 创建注记两条 = TaskRefs 两条登记成功的外显（群账本）
	var page struct {
		Messages []struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=0", "")
	if code != http.StatusOK {
		t.Fatal("拉取群消息应 200")
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, m := range page.Messages {
		if m.Kind == "system" && strings.Contains(m.Body, "已创建（来自群）") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("两名成员应各有一条创建注记, got %d（消息 %d 条）", notes, len(page.Messages))
	}
}

// TestGroupCollabIdemReplaySingleTask 同成员重放只一任务（R1-02 幂等语义保持）：
// 手动补派（派发失败后重试路径，同 idem 键）不得重复落任务、不得重复登记
// TaskRefs、不得重复注入创建注记。
func TestGroupCollabIdemReplaySingleTask(t *testing.T) {
	mp := &autoMemberMock{members: []string{"zcode-dev@mac-mini"}}
	s, f := newAutoFixtureServer(t, mp)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "zcode-dev", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("mac-mini", true)
	hc := f.loginClient(true)
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups", `{"goal":"重放收敛","auto_members":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatal(err)
	}

	// 手动补派重放（同群同成员，同 idem 键）
	grp, ok := s.cfg.Groups.GetGroup(g.ID)
	if !ok {
		t.Fatal("群应存在")
	}
	s.dispatchGroupCollab(grp, "admin")

	tasks := f.st.View().Tasks
	if len(tasks) != 1 {
		t.Fatalf("重放后协作任务应仍为 1 个, got %d", len(tasks))
	}
	got, _ := s.cfg.Groups.GetGroup(g.ID)
	if len(got.TaskRefs) != 1 {
		t.Fatalf("TaskRefs 应无重复登记, got %v", got.TaskRefs)
	}
	var page struct {
		Messages []struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	code, body = f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=0", "")
	if code != http.StatusOK {
		t.Fatal("拉取群消息应 200")
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, m := range page.Messages {
		if m.Kind == "system" && strings.Contains(m.Body, "已创建（来自群）") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("重放不得重复创建注记, got %d", notes)
	}
}

// TestGroupCollabGuidanceChain 指引→预算→FINAL 收敛链端到端（R1-03，v4 主持
// 人语义）：真实派发的主持棒任务 stdin（agent 实际拿到的点名指引）每次
// group_say 示例都带 kind="agent"；照指引走 REST——agent 发言占预算、FINAL 注记
// 恰一条、user 插话不触发；预算耗尽拒绝后正式任务提交不受影响。
func TestGroupCollabGuidanceChain(t *testing.T) {
	mp := &autoMemberMock{pickNode: "mac-mini", pickAgent: "butler"}
	f := newAutoFixture(t, mp)
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{{Name: "butler", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("mac-mini", true)
	hc := f.loginClient(true)

	// v4 建群语义：成员主人手选 + jev 主持开启 → 点名第一棒（协作指引随棒派发）
	code, body := f.do(hc, http.MethodPost, "/api/v1/groups",
		`{"goal":"协作收敛","members":["mac-mini/butler"],"jev_host":true}`)
	if code != http.StatusCreated {
		t.Fatalf("建群应 201, got %d: %s", code, body)
	}
	var g struct {
		ID     string `json:"group_id"`
		Budget struct {
			MaxRounds int `json:"max_rounds"`
		} `json:"budget"`
	}
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatal(err)
	}

	// 真实派发的主持棒任务 stdin 即指引：group_say 示例（含 FINAL）全部显式
	// kind="agent"，且带预算耗尽指引
	batons := f.st.View().Tasks
	if len(batons) != 1 {
		t.Fatalf("应恰一棒在途, got %d", len(batons))
	}
	var stdin string
	for _, tk := range batons {
		stdin = tk.Input.Stdin
	}
	if strings.Count(stdin, `kind="agent"`) < 2 {
		t.Fatalf("指引的 group_say 示例（含 FINAL）必须显式携带 kind=\"agent\": %s", stdin)
	}
	if !strings.Contains(stdin, "budget_exhausted") {
		t.Fatalf("指引应包含预算耗尽行为（收尾不重试）: %s", stdin)
	}

	// 按指引发言（MCP/REST 缺 kind 会落 user 插话——agent 依指引必须显式传）
	say := func(from, kind, bodyStr string) int {
		payload := `{"from":"` + from + `","body":"` + bodyStr + `"`
		if kind != "" {
			payload += `,"kind":"` + kind + `"`
		}
		payload += `}`
		c2, _ := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages", payload)
		return c2
	}
	if c2 := say("butler", "agent", "我先盘点磁盘状态"); c2 != http.StatusCreated {
		t.Fatalf("agent 普通发言应 201, got %d", c2)
	}
	if c2 := say("butler", "agent", "FINAL: 磁盘已清理完成"); c2 != http.StatusCreated {
		t.Fatalf("FINAL 发言应 201, got %d", c2)
	}
	if c2 := say("admin", "user", "FINAL: 用户插话带前缀"); c2 != http.StatusCreated {
		t.Fatalf("user 插话应 201, got %d", c2)
	}
	countFinalNotes := func() int {
		var page struct {
			Messages []struct {
				Kind string `json:"kind"`
				Body string `json:"body"`
			} `json:"messages"`
		}
		c2, b2 := f.do(hc, http.MethodGet, "/api/v1/groups/"+g.ID+"/messages?since=0", "")
		if c2 != http.StatusOK {
			f.t.Fatalf("拉取群消息应 200, got %d: %s", c2, b2)
		}
		if err := json.Unmarshal([]byte(b2), &page); err != nil {
			f.t.Fatal(err)
		}
		n := 0
		for _, m := range page.Messages {
			if m.Kind == "system" && strings.Contains(m.Body, "最终版本候选") {
				n++
			}
		}
		return n
	}
	if n := countFinalNotes(); n != 1 {
		t.Fatalf("FINAL 注记应恰一条, got %d", n)
	}

	// 耗尽剩余预算（已用 2 条 agent 发言），第 max+1 条被拒
	for i := 0; i < g.Budget.MaxRounds-2; i++ {
		if c2 := say("butler", "agent", "补充观点"); c2 != http.StatusCreated {
			t.Fatalf("预算内 agent 发言应 201（第 %d 条）, got %d", i+3, c2)
		}
	}
	if c2, b2 := f.do(hc, http.MethodPost, "/api/v1/groups/"+g.ID+"/messages",
		`{"from":"butler","kind":"agent","body":"FINAL: 预算后再发"}`); c2 != http.StatusConflict || !strings.Contains(string(b2), "budget_exhausted") {
		t.Fatalf("预算耗尽应 409 budget_exhausted, got %d: %s", c2, b2)
	}
	if n := countFinalNotes(); n != 1 {
		t.Fatalf("被拒消息不得产生注记, FINAL 注记应为 1, got %d", n)
	}

	// 预算耗尽只约束群发言：正式任务提交不受影响
	if err := f.core.RegisterNode("win-box", []protocol.AgentCard{{Name: "echo", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	code, body = f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"win-box","target_agent":"echo","input":{"args":["hi"]},"submitted_by":"tester"}`)
	if code != http.StatusAccepted {
		t.Fatalf("预算耗尽后正式任务提交应 202, got %d: %s", code, body)
	}
}
