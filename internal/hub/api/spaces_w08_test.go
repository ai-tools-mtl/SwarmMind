package api

// spaces_w08_test.go W08-02 中心空间 REST 契约（docs/tasks/v3/W08-recovery.md
// 「REST/MCP与页面」表）：归档过滤（默认隐藏 + include_archived 显式包含 +
// archived_hidden 注记；已归档详情/消息读路径不关闭）与诊断投影（spaceDiag
// 在途/留待核对计数；dispatch 查询 awaiting_review/binding——与 spaces 服务
// 重启对账「任务行实物分类」同源：needs_review 终态在途=留待核对）。
//
// needs_review 链为真实任务链：SubmitSpaceTurn 落任务行（黑洞传输不投递）→
// node 身份 task_result 帧（HandleFrame 真实入口）落 needs_review 终态 →
// spaces 收敛钩子按 classifyTaskFact 留待核对（不收敛）→ 诊断投影可见。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/spaces"
	hubstore "github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/strategy"
	"github.com/mtl802/swarmmind/internal/transport"
)

// TestW0802HubSpaceListArchiveFilter 中心列表归档过滤 + 已归档回查（读路径不关闭）。
func TestW0802HubSpaceListArchiveFilter(t *testing.T) {
	f := newSpacesRESTFixture(t)
	code, body := f.adminPost("/api/v1/spaces", `{"goal":"中心归档目标","mode":"manual","request_id":"w08-c-arch"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建归档夹具: %d %v", code, body)
	}
	archID := body["space"].(map[string]any)["id"].(string)
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"中心活跃目标","mode":"manual","request_id":"w08-c-live"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建活跃夹具: %d %v", code, body)
	}
	// archived：abandoned 定版 → 归档 sugar。
	if code, body = f.adminPost("/api/v1/spaces/"+archID+"/finalize",
		`{"expected_version":1,"final_outcome":"abandoned","reason":"W08 中心归档夹具"}`); code != http.StatusOK {
		t.Fatalf("定版: %d %v", code, body)
	}
	if code, body = f.adminPost("/api/v1/spaces/"+archID+"/archive", `{"expected_version":2}`); code != http.StatusOK {
		t.Fatalf("归档: %d %v", code, body)
	}
	// 默认列表：归档隐藏 + archived_hidden=1。
	code, body = f.adminGet("/api/v1/spaces")
	if code != http.StatusOK {
		t.Fatalf("默认列表: %d %v", code, body)
	}
	if v, _ := body["archived_hidden"].(float64); v != 1 {
		t.Fatalf("archived_hidden 应为 1: %v", body["archived_hidden"])
	}
	if lst, _ := body["spaces"].([]any); len(lst) != 1 {
		t.Fatalf("默认列表应只含活跃空间: total=%v", body["total"])
	}
	// 显式包含：两空间齐，archived_hidden 恒 0。
	code, body = f.adminGet("/api/v1/spaces?include_archived=true")
	if code != http.StatusOK {
		t.Fatalf("显式包含: %d %v", code, body)
	}
	if lst, _ := body["spaces"].([]any); len(lst) != 2 {
		t.Fatalf("include_archived=true 应含归档: total=%v", body["total"])
	}
	if v, _ := body["archived_hidden"].(float64); v != 0 {
		t.Fatalf("显式包含时 archived_hidden 恒 0: %v", body["archived_hidden"])
	}
	// 已归档回查：详情（archived=true + diag 投影）与消息读路径不关闭。
	code, body = f.adminGet("/api/v1/spaces/" + archID)
	if code != http.StatusOK {
		t.Fatalf("已归档详情: %d %v", code, body)
	}
	sb, _ := json.Marshal(body["space"])
	if !strings.Contains(string(sb), `"archived":true`) {
		t.Fatalf("详情应标 archived: %s", sb)
	}
	if _, ok := body["diag"].(map[string]any); !ok {
		t.Fatalf("详情应携 diag 投影: %v", body)
	}
	if code, body = f.adminGet("/api/v1/spaces/" + archID + "/messages?since=0"); code != http.StatusOK {
		t.Fatalf("已归档消息读应可查: %d %v", code, body)
	}
}

// TestW0802HubDispatchDiagAwaitingReview 中心派发查询诊断注记（真实任务链）：
// accepted 意图的任务行 needs_review 终态 → awaiting_review（留待核对，与
// spaces 服务 classifyTaskFact 不收敛同源）；绑定登记代次投影（未登记=0 如实）。
func TestW0802HubDispatchDiagAwaitingReview(t *testing.T) {
	f := newSpacesRESTFixture(t)
	// 会话能力卡 + 建会话（注册表行；黑洞传输不投递任务）。
	if err := f.core.RegisterNode("w08N", []protocol.AgentCard{{
		Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write",
		ProtocolVer: 1, Exposed: true, SessionAdapter: protocol.SessionAdapterCodex,
		Spaces: protocol.SpacesCapV1,
	}}); err != nil {
		t.Fatalf("注册节点: %v", err)
	}
	code, body := f.adminPost("/api/v1/sessions", `{"node_id":"w08N","agent":"coder","cwd":"/tmp/w08"}`)
	if code != http.StatusCreated {
		t.Fatalf("建会话: %d %v", code, body)
	}
	sessID := body["session"].(map[string]any)["id"].(string)
	// 空间 + 成员绑定 + ready/start（admin 主人面）。
	code, body = f.adminPost("/api/v1/spaces", `{"goal":"W08 诊断目标","mode":"manual","request_id":"w08-diag-sp"}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间: %d %v", code, body)
	}
	spID := body["space"].(map[string]any)["id"].(string)
	sp, ok := f.svc.Space(spID)
	if !ok {
		t.Fatalf("空间缺失")
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-cfg", Action: collab.ActConfigureMembers,
		ExpectedVersion: sp.Version, Members: []collab.Member{{Name: "w08N/coder", SessionID: sessID}}},
		collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-rdy", Action: collab.ActReady,
		ExpectedVersion: 2}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-str", Action: collab.ActStart,
		ExpectedVersion: 3}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 真实空间轮任务（生产派发链的执行提交步骤；绑定登记省略不影响诊断 join）。
	ref, _, err := f.core.SubmitSpaceTurn(sessID, core.SessionTurnRequest{Prompt: "W08 诊断夹具轮"},
		"system", collab.HubScope(spID))
	if err != nil {
		t.Fatalf("空间轮任务提交: %v", err)
	}
	// 模型层补接单（AttachDispatchTask 同形：TaskID 在位 → accepted——生产链由
	// bind 成功后 attach；此处直证 accepted 意图的诊断 join）。
	cur, _ := f.svc.Space(spID)
	if _, _, _, err := f.svc.RecordDispatch(spID, collab.DispatchRequest{
		RequestID: "w08-diag-d1", Member: "w08N/coder", SessionID: sessID,
		TurnSeq: ref.TurnSeq, Term: 0, TaskID: ref.TaskID, ExpectedVersion: cur.Version,
	}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("补接单受理: %v", err)
	}
	// 受理即受理面锚（未终态任务行在位 → 无缺口注记）。
	code, body = f.adminGet("/api/v1/spaces/" + spID + "/dispatches/w08-diag-d1")
	if code != http.StatusOK {
		t.Fatalf("意图查询: %d %v", code, body)
	}
	if b, _ := json.Marshal(body); strings.Contains(string(b), "awaiting_review") {
		t.Fatalf("任务非终态在途不得带留待核对注记: %s", b)
	}
	// node 身份 needs_review 结果帧（真实 HandleFrame 入口）→ 任务 needs_review
	// 终态 + 轮终回写 + spaces 收敛钩子（classifyTaskFact：不确定 → 不收敛）。
	payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: ref.TaskID, Status: protocol.ResultNeedsReview,
		Result: &protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: protocol.ExitCodeNotRun,
			Error: protocol.CodeNodeRestartedMidway}})
	f.core.HandleFrame(&protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), From: "w08N", To: "hub",
		Payload: payload,
	})
	code, body = f.adminGet("/api/v1/spaces/" + spID + "/dispatches/w08-diag-d1")
	if code != http.StatusOK {
		t.Fatalf("意图查询: %d %v", code, body)
	}
	b, _ := json.Marshal(body)
	if !strings.Contains(string(b), `"awaiting_review":true`) {
		t.Fatalf("needs_review 在途意图应带 awaiting_review: %s", b)
	}
	if strings.Contains(string(b), `"task_gone":true`) {
		t.Fatalf("任务行在位不得带 task_gone: %s", b)
	}
	if binding, _ := body["binding"].(map[string]any); binding == nil {
		t.Fatalf("应携绑定登记投影: %v", body["binding"])
	}
	// 空间详情 diag 同源：awaiting_review=1（意图未被收敛=留待核对）。
	code, body = f.adminGet("/api/v1/spaces/" + spID)
	if code != http.StatusOK {
		t.Fatalf("详情: %d %v", code, body)
	}
	diag := body["diag"].(map[string]any)
	if v, _ := diag["awaiting_review"].(float64); v != 1 {
		t.Fatalf("diag.awaiting_review 应为 1: %v", diag)
	}
	if v, _ := diag["in_flight"].(float64); v != 1 {
		t.Fatalf("diag.in_flight 应为 1: %v", diag)
	}
}

// w08ClockFixture 必修3 反例夹具：可注入假时钟与终态保留的完整中心栈（真实
// store/core/spaces/api），用于真实构造「轮行在案、任务已清理、Attach 未落」的
// pending 证据缺失形态——needs_review 结果帧落终态后前移时钟驱动 core.Sweep，
// 任务行过保留期被清理而会话轮行与 pending 意图留存（与 spaces 服务恢复对账
// turnFactMissing 分类同源的实物形态）。
type w08ClockFixture struct {
	t       *testing.T
	srv     *httptest.Server
	core    *core.Core
	svc     *spaces.Service
	now     *time.Time
	admin   string
	cookies map[string]string
}

func newW08ClockFixture(t *testing.T) *w08ClockFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := hubstore.Open(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	retention := time.Hour
	hubCore, err := core.New(core.Config{Store: st, Transport: transport.NewBus().Endpoint("hub"),
		SweepInterval: 0, TerminalRetention: &retention, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	t.Cleanup(func() { _ = hubCore.Close() })
	stratEngine, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	svc, err := spaces.Open(dir, hubCore, spaces.Options{Hooks: stratEngine})
	if err != nil {
		t.Fatalf("spaces: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	hubCore.SetSpaceTurnGuard(svc.SpaceTurnGuard())
	adminToken := "w08-clock-admin"
	s := New(Config{Core: hubCore, AdminToken: adminToken, DataDir: dir, Spaces: svc, Strategy: stratEngine, Version: "test"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &w08ClockFixture{t: t, srv: ts, core: hubCore, svc: svc, now: &now, admin: adminToken, cookies: map[string]string{}}
}

func (f *w08ClockFixture) advance(d time.Duration) { *f.now = f.now.Add(d) }

func (f *w08ClockFixture) login() string {
	f.t.Helper()
	if c, ok := f.cookies[f.admin]; ok {
		return c
	}
	resp, err := http.Post(f.srv.URL+"/api/v1/login", "application/json",
		strings.NewReader(`{"token":"`+f.admin+`"}`))
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		f.t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "swarmmind_session" {
			f.cookies[f.admin] = c.Name + "=" + c.Value
			return f.cookies[f.admin]
		}
	}
	f.t.Fatal("login 未发会话 cookie")
	return ""
}

func (f *w08ClockFixture) rest(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Cookie", f.login())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestW0802HubPendingEvidenceMissing 必修3 反例回归：pending 意图 + 轮行在案 +
// 任务已清理（Attach 未落）= 执行证据缺失留待核对——diag.awaiting_review ≥1、
// 派发查询注记在位（真实任务链：SubmitSpaceTurn 轮行 → needs_review 结果帧 →
// 时钟前移驱动 Sweep 清理任务行）。
func TestW0802HubPendingEvidenceMissing(t *testing.T) {
	f := newW08ClockFixture(t)
	if err := f.core.RegisterNode("w08CN", []protocol.AgentCard{{
		Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write",
		ProtocolVer: 1, Exposed: true, SessionAdapter: protocol.SessionAdapterCodex,
		Spaces: protocol.SpacesCapV1,
	}}); err != nil {
		t.Fatalf("注册节点: %v", err)
	}
	code, body := f.rest("POST", "/api/v1/sessions", `{"node_id":"w08CN","agent":"coder","cwd":"/tmp/w08m"}`)
	if code != http.StatusCreated {
		t.Fatalf("建会话: %d %v", code, body)
	}
	sessID := body["session"].(map[string]any)["id"].(string)
	code, body = f.rest("POST", "/api/v1/spaces", `{"goal":"W08 证据缺失反例","mode":"manual","request_id":"w08-miss-sp"}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间: %d %v", code, body)
	}
	spID := body["space"].(map[string]any)["id"].(string)
	sp, ok := f.svc.Space(spID)
	if !ok {
		t.Fatalf("空间缺失")
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-m-cfg", Action: collab.ActConfigureMembers,
		ExpectedVersion: sp.Version, Members: []collab.Member{{Name: "w08CN/coder", SessionID: sessID}}},
		collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("配置成员: %v", err)
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-m-rdy", Action: collab.ActReady,
		ExpectedVersion: 2}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if _, _, err := f.svc.Control(spID, collab.ControlRequest{RequestID: "w08-m-str", Action: collab.ActStart,
		ExpectedVersion: 3}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// pending 意图（TaskID 空）+ 同 request_id 的轮行落库（受理→投递崩溃窗形态：
	// 生产链 advanceSpace 以意图 request_id 提交轮）。
	cur, _ := f.svc.Space(spID)
	if _, _, _, err := f.svc.RecordDispatch(spID, collab.DispatchRequest{
		RequestID: "w08-miss-d1", Member: "w08CN/coder", SessionID: sessID, TurnSeq: 1,
		Term: 0, ExpectedVersion: cur.Version,
	}, collab.Subject{Name: "admin", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("受理 pending 意图: %v", err)
	}
	ref, _, err := f.core.SubmitSpaceTurn(sessID, core.SessionTurnRequest{Prompt: "W08 证据缺失夹具轮", RequestID: "w08-miss-d1"},
		"system", collab.HubScope(spID))
	if err != nil {
		t.Fatalf("空间轮提交: %v", err)
	}
	if ref.TurnSeq != 1 {
		t.Fatalf("夹具前提：轮号应=1 got %d", ref.TurnSeq)
	}
	// needs_review 结果帧（真实 HandleFrame 入口）→ 任务 needs_review 终态；
	// 收敛钩子 classifyTaskFact：不确定 → 不收敛（意图留 pending 留待核对）。
	payload, _ := json.Marshal(protocol.TaskResultPayload{TaskID: ref.TaskID, Status: protocol.ResultNeedsReview,
		Result: &protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: protocol.ExitCodeNotRun,
			Error: protocol.CodeNodeRestartedMidway}})
	f.core.HandleFrame(&protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), From: "w08CN", To: "hub",
		Payload: payload,
	})
	if it, ok := f.svc.IntentByRequest(spID, "w08-miss-d1"); !ok || it.Status != collab.IntentPending {
		t.Fatalf("夹具前提：needs_review 后意图应留 pending: %+v ok=%v", it, ok)
	}
	// 时钟前移过保留期 → core.Sweep 清理 needs_review 任务行（轮行/意图留存）
	// ——「轮行在案、任务已清理、Attach 未落」实物形态构造完成。
	f.advance(2 * time.Hour)
	f.core.Sweep()
	if _, ok := f.core.GetTask(ref.TaskID); ok {
		t.Fatal("夹具前提：任务行应已过保留期被清理")
	}
	// diag：awaiting_review ≥1（证据缺失留待核对可见）。
	code, body = f.rest("GET", "/api/v1/spaces/"+spID, "")
	if code != http.StatusOK {
		t.Fatalf("详情: %d %v", code, body)
	}
	diag := body["diag"].(map[string]any)
	if v, _ := diag["awaiting_review"].(float64); v < 1 {
		t.Fatalf("证据缺失形态 diag.awaiting_review 应 ≥1: %v", diag)
	}
	// 派发查询注记在位。
	code, body = f.rest("GET", "/api/v1/spaces/"+spID+"/dispatches/w08-miss-d1", "")
	if code != http.StatusOK {
		t.Fatalf("意图查询: %d %v", code, body)
	}
	b, _ := json.Marshal(body)
	if !strings.Contains(string(b), `"awaiting_review":true`) {
		t.Fatalf("证据缺失 pending 意图应带 awaiting_review: %s", b)
	}
	turn, _ := body["turn"].(map[string]any)
	if turn == nil || turn["task_gone"] != true {
		t.Fatalf("轮行注记应标 task_gone（任务行已清理）: %v", body["turn"])
	}
}
