// works_test.go Work 工作单 REST 端点契约（P5 W1）：鉴权门禁、201/200 幂等、
// 步骤校验 400 映射、列表/详情/事件游标、404、服务端验证主体落账。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestWorkEndpointsAuthGate 未登录访问工作单端点一律 401（与既有管理面同门禁）。
func TestWorkEndpointsAuthGate(t *testing.T) {
	f := newFixture(t)
	hc := f.client(false)
	for _, tc := range [][2]string{
		{http.MethodPost, "/api/v1/works"},
		{http.MethodGet, "/api/v1/works"},
		{http.MethodGet, "/api/v1/works/w-x"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d", tc[0], tc[1], code)
		}
	}
}

// TestWorkRESTFlow 全链：建单 201 → 幂等重建 200+dup → 校验失败 400 →
// 列表 → 详情（事件游标）→ 未知 404 → 非法游标 400。
func TestWorkRESTFlow(t *testing.T) {
	f := newFixture(t)
	hc := f.client(true)

	body := `{"goal":"对比双机磁盘","steps":[` +
		`{"desc":"盘点磁盘","target_node":"mac-mini","target_agent":"diskcheck"},` +
		`{"desc":"汇总报告","target_node":"mac-mini","target_agent":"reporter","depends_on":[1]}]}`
	code, resp := f.do(hc, http.MethodPost, "/api/v1/works", body)
	if code != http.StatusCreated {
		t.Fatalf("建单应 201, got %d: %s", code, resp)
	}
	var created struct {
		Work struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Events []struct {
				Seq       int64  `json:"seq"`
				Kind      string `json:"kind"`
				By        string `json:"by"`
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("201 响应解析: %v", err)
	}
	if !strings.HasPrefix(created.Work.ID, "w-") || created.Work.Status != "planning" ||
		len(created.Work.Events) != 1 || created.Work.Events[0].Kind != "work_created" || created.Dup {
		t.Fatalf("建单返回异常: %s", resp)
	}
	// 服务端验证主体独立落账（RFC 六.2）：会话入口 principal=admin，
	// 自报标签 by=mcp（请求未带 created_by，缺省值不冒充身份）
	if created.Work.Events[0].Principal != "admin" || created.Work.Events[0].By != "mcp" {
		t.Fatalf("事件主体/自报标签应分开落账: %+v", created.Work.Events[0])
	}

	// 幂等重建：200 + dup:true + 同单 + 零新增事件
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works", body)
	if code != http.StatusOK {
		t.Fatalf("幂等重建应 200, got %d: %s", code, resp)
	}
	var dupResp struct {
		Work struct {
			ID     string `json:"id"`
			Events []struct {
				Seq int64 `json:"seq"`
			} `json:"events"`
		} `json:"work"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(resp, &dupResp); err != nil || !dupResp.Dup || dupResp.Work.ID != created.Work.ID {
		t.Fatalf("幂等重建应 200+dup+同单: code=%d resp=%s err=%v", code, resp, err)
	}
	if len(dupResp.Work.Events) != 1 {
		t.Fatalf("幂等命中不得新增事件: %s", resp)
	}

	// 步骤计划不合法：400 invalid_work_steps
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works",
		`{"goal":"g","steps":[{"desc":"a","target_node":"n","target_agent":"x","depends_on":[3]}]}`)
	if code != http.StatusBadRequest || !strings.Contains(string(resp), "invalid_work_steps") {
		t.Fatalf("计划不合法应 400 invalid_work_steps, got %d: %s", code, resp)
	}
	// goal 缺失：400 invalid_request
	if code, resp = f.do(hc, http.MethodPost, "/api/v1/works", `{"steps":[]}`); code != http.StatusBadRequest {
		t.Fatalf("goal 缺失应 400, got %d: %s", code, resp)
	}

	// 列表
	code, resp = f.do(hc, http.MethodGet, "/api/v1/works", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200, got %d", code)
	}
	var list struct {
		Works []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Steps  int    `json:"steps"`
		} `json:"works"`
	}
	if err := json.Unmarshal(resp, &list); err != nil || len(list.Works) != 1 || list.Works[0].Steps != 2 {
		t.Fatalf("列表异常: %s err=%v", resp, err)
	}

	// 详情：全量事件；游标 since_events=1 → 空 + pruned_before_events=0
	code, resp = f.do(hc, http.MethodGet, "/api/v1/works/"+created.Work.ID, "")
	if code != http.StatusOK {
		t.Fatalf("详情应 200, got %d", code)
	}
	var detail struct {
		Work struct {
			Events []struct {
				Seq int64 `json:"seq"`
			} `json:"events"`
		} `json:"work"`
		PrunedBeforeEvents int64 `json:"pruned_before_events"`
		Dup                bool  `json:"dup"`
	}
	if err := json.Unmarshal(resp, &detail); err != nil || len(detail.Work.Events) != 1 || detail.Dup {
		t.Fatalf("详情异常: %s err=%v", resp, err)
	}
	code, resp = f.do(hc, http.MethodGet, "/api/v1/works/"+created.Work.ID+"?since_events=1", "")
	if err := json.Unmarshal(resp, &detail); err != nil || len(detail.Work.Events) != 0 || detail.PrunedBeforeEvents != 0 {
		t.Fatalf("游标增量异常: %s err=%v", resp, err)
	}
	if code, _ = f.do(hc, http.MethodGet, "/api/v1/works/"+created.Work.ID+"?since_events=-1", ""); code != http.StatusBadRequest {
		t.Fatalf("负游标应 400, got %d", code)
	}

	// 未知 ID：404 work_not_found
	if code, resp = f.do(hc, http.MethodGet, "/api/v1/works/w-missing", ""); code != http.StatusNotFound ||
		!strings.Contains(string(resp), "work_not_found") {
		t.Fatalf("未知单应 404 work_not_found, got %d: %s", code, resp)
	}
}

// TestWorkRESTCreatePrincipalRecords 双入口主体接线（codex 审查必须修 2）：
// 服务端验证主体取自鉴权层（RFC 六.2），随事件独立落账、与自报 created_by
// 分开存储——admin 会话入口记 admin，PAT（Bearer）入口记 pat:<令牌名>；请求
// 自报 created_by 无论报什么都不得冒充主体。
func TestWorkRESTCreatePrincipalRecords(t *testing.T) {
	f := newFixture(t)
	dataDir := t.TempDir() // 启用 PAT 存储（基础 fixture 不带 DataDir）
	srv := httptest.NewServer(New(Config{Core: f.core, AdminToken: "tok", DataDir: dataDir}).Handler())
	t.Cleanup(srv.Close)
	body := `{"goal":"主体审计","created_by":"冒充者自报","steps":[{"desc":"d","target_node":"n","target_agent":"a"}]}`

	// 入口一：admin 会话（cookie）——principal=admin，自报标签原样留 by 字段
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("登录: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = cl.Post(srv.URL+"/api/v1/works", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("会话建单: %v %d", err, resp.StatusCode)
	}
	var created struct {
		Work struct {
			ID     string `json:"id"`
			Events []struct {
				By        string `json:"by"`
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("会话建单响应解析: %v", err)
	}
	resp.Body.Close()
	if len(created.Work.Events) != 1 || created.Work.Events[0].Principal != "admin" ||
		created.Work.Events[0].By != "冒充者自报" {
		t.Fatalf("会话入口主体/自报应分开落账: %+v", created.Work.Events)
	}

	// 入口二：PAT（Bearer）——principal=pat:<令牌名>，与自报标签无关
	//（goal 与会话入口不同：内容指纹幂等按 goal+steps 计，同内容会命中原单）
	resp, err = cl.Post(srv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"mcp-agent"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("铸 PAT: %v %d", err, resp.StatusCode)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.Token == "" {
		t.Fatalf("PAT 响应解析: %v", err)
	}
	resp.Body.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/works",
		strings.NewReader(`{"goal":"主体审计二","created_by":"冒充者自报","steps":[{"desc":"d","target_node":"n","target_agent":"a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("PAT 建单: %v %d", err, resp.StatusCode)
	}
	var patCreated struct {
		Work struct {
			ID     string `json:"id"`
			Events []struct {
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&patCreated); err != nil {
		t.Fatalf("PAT 建单响应解析: %v", err)
	}
	resp.Body.Close()
	if len(patCreated.Work.Events) != 1 || patCreated.Work.Events[0].Principal != "pat:mcp-agent" {
		t.Fatalf("PAT 入口应记令牌名主体: %+v", patCreated.Work.Events)
	}

	// 详情读回：两条建单事件主体各自在账（持久化后不丢）；读端点无凭据 401
	for _, tc := range []struct {
		workID, want string
	}{
		{created.Work.ID, "admin"},
		{patCreated.Work.ID, "pat:mcp-agent"},
	} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/works/"+tc.workID, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("详情无凭据请求: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("详情无凭据应 401, got %d", resp.StatusCode)
		}
		resp.Body.Close()
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		resp, err = http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("详情: %v %d", err, resp.StatusCode)
		}
		var out struct {
			Work struct {
				Events []struct {
					Principal string `json:"principal"`
				} `json:"events"`
			} `json:"work"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("详情解析: %v", err)
		}
		resp.Body.Close()
		if len(out.Work.Events) != 1 || out.Work.Events[0].Principal != tc.want {
			t.Fatalf("详情应带主体 %s: %+v", tc.want, out.Work.Events)
		}
	}
}

// registerEchoNode 测试捷径：注册 mac-mini（diskcheck/reporter exposed）。
func registerEchoNode(f *fixture) {
	f.t.Helper()
	if err := f.core.RegisterNode("mac-mini", []protocol.AgentCard{
		{Name: "diskcheck", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true},
		{Name: "reporter", Kind: protocol.AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true},
	}); err != nil {
		f.t.Fatalf("注册 node: %v", err)
	}
	f.core.SetOnline("mac-mini", true)
}

// createWorkBody 测试捷径：以会话建单（三步计划，term 任期），返回 work_id。
func createWorkBody(f *fixture, hc *http.Client, goal, term string) string {
	f.t.Helper()
	payload := map[string]any{
		"goal": goal,
		"steps": []map[string]any{
			{"desc": "盘点磁盘", "target_node": "mac-mini", "target_agent": "diskcheck"},
			{"desc": "盘点内存", "target_node": "mac-mini", "target_agent": "diskcheck"},
			{"desc": "汇总报告", "target_node": "mac-mini", "target_agent": "reporter", "depends_on": []int{1, 2}},
		},
		"prime_term": term,
	}
	b, _ := json.Marshal(payload)
	code, resp := f.do(hc, http.MethodPost, "/api/v1/works", string(b))
	if code != http.StatusCreated {
		f.t.Fatalf("建单应 201, got %d: %s", code, resp)
	}
	var out struct {
		Work struct {
			ID string `json:"id"`
		} `json:"work"`
	}
	_ = json.Unmarshal(resp, &out)
	return out.Work.ID
}

// TestWorkTakeoverEndpoint W2 REST 接管端点：门禁 401、成功 200、重放 dup、
// 404、409 冲突、终态单 409 work_closed（注入）、接管事件主体落账。
func TestWorkTakeoverEndpoint(t *testing.T) {
	f := newFixture(t)
	registerEchoNode(f)
	hc := f.client(true)
	wid := createWorkBody(f, hc, "接管端点", "prime-1")

	// 未登录 401
	if code, _ := f.do(f.client(false), http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-1","new_term":"prime-2"}`); code != http.StatusUnauthorized {
		t.Fatalf("未登录接管应 401, got %d", code)
	}

	// 成功接管 200（事件 by=新任期、principal=admin）
	code, resp := f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-1","new_term":"prime-2"}`)
	if code != http.StatusOK {
		t.Fatalf("接管应 200, got %d: %s", code, resp)
	}
	var out struct {
		Work struct {
			PrimeTerm string `json:"prime_term"`
			Events    []struct {
				Kind      string `json:"kind"`
				By        string `json:"by"`
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("接管响应解析: %v", err)
	}
	if out.Work.PrimeTerm != "prime-2" || out.Dup {
		t.Fatalf("接管返回: term=%s dup=%v", out.Work.PrimeTerm, out.Dup)
	}
	last := out.Work.Events[len(out.Work.Events)-1]
	if last.Kind != "takeover" || last.By != "prime-2" || last.Principal != "admin" {
		t.Fatalf("接管事件主体: %+v", last)
	}

	// 相同操作重放 → 200 + dup:true
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-2","new_term":"prime-2"}`)
	if code != http.StatusOK {
		t.Fatalf("重放应 200, got %d", code)
	}
	_ = json.Unmarshal(resp, &out)
	if !out.Dup {
		t.Fatalf("重放应 dup:true: %s", resp)
	}

	// 原请求重放（expected=prime-1,new=prime-2——首次成功响应丢失的形态，
	// W2 审查#5）→ 200 + dup:true 成功回执，任期不变
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-1","new_term":"prime-2"}`)
	if code != http.StatusOK {
		t.Fatalf("原请求重放应 200 回执, got %d: %s", code, resp)
	}
	_ = json.Unmarshal(resp, &out)
	if !out.Dup || out.Work.PrimeTerm != "prime-2" {
		t.Fatalf("原请求重放应 dup:true 返现状: %s", resp)
	}

	// 任期标签复用（历史任期 prime-1 重新接管，W2 审查#9 ABA）→ 409
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-2","new_term":"prime-1"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_term_conflict") {
		t.Fatalf("复用历史任期应 409 work_term_conflict: %d %s", code, resp)
	}

	// 代校验失败 409 work_term_conflict；未知单 404
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-1","new_term":"prime-3"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_term_conflict") {
		t.Fatalf("代校验失败应 409 work_term_conflict: %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/w-none/takeover", `{"expected_term":"","new_term":"t"}`)
	if code != http.StatusNotFound || !strings.Contains(string(resp), "work_not_found") {
		t.Fatalf("未知单应 404: %d %s", code, resp)
	}
	// new_term 缺失 400
	if code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-2"}`); code != http.StatusBadRequest {
		t.Fatalf("new_term 缺失应 400: %d %s", code, resp)
	}

	// 终态单 409 work_closed（W2 无产生路径，注入为防御纵深验证）
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[wid]
		w.Status = "done"
		st.Works[wid] = w
		return nil
	}); err != nil {
		t.Fatalf("注入终态: %v", err)
	}
	if code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/takeover", `{"expected_term":"prime-2","new_term":"prime-4"}`); code != http.StatusConflict || !strings.Contains(string(resp), "work_closed") {
		t.Fatalf("终态单应 409 work_closed: %d %s", code, resp)
	}
}

// TestWorkBoundSubmitREST W2 REST 绑定提交：202 + work 定位字段、错误码映射、
// 任务详情回读绑定三字段、接管前派发拒绝。
func TestWorkBoundSubmitREST(t *testing.T) {
	f := newFixture(t)
	registerEchoNode(f)
	hc := f.client(true)
	wid := createWorkBody(f, hc, "绑定提交", "prime-1")

	// 接管前派发：工作单已带任期（建单 prime_term=prime-1）→ 直接可派
	body := `{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},` +
		`"submitted_by":"tester","idem_key":"k1","work_ref":"` + wid + `","step_seq":1,"prime_term":"prime-1"}`
	code, resp := f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusAccepted {
		t.Fatalf("绑定提交应 202, got %d: %s", code, resp)
	}
	var ack struct {
		TaskID string `json:"task_id"`
		Work   struct {
			WorkID     string `json:"work_id"`
			StepSeq    int64  `json:"step_seq"`
			AttemptSeq int64  `json:"attempt_seq"`
			AttemptRef string `json:"attempt_ref"`
			Outcome    string `json:"outcome"`
		} `json:"work"`
	}
	if err := json.Unmarshal(resp, &ack); err != nil {
		t.Fatalf("202 体解析: %v", err)
	}
	if ack.Work.WorkID != wid || ack.Work.StepSeq != 1 || ack.Work.AttemptSeq != 1 ||
		ack.Work.AttemptRef != wid+"#a1" || ack.Work.Outcome != "submitted" {
		t.Fatalf("work 定位字段: %s", resp)
	}

	// 任务详情回读绑定三字段
	code, resp = f.do(hc, http.MethodGet, "/api/v1/tasks/"+ack.TaskID, "")
	if code != http.StatusOK {
		t.Fatalf("任务详情应 200: %d", code)
	}
	var task struct {
		WorkRef    string `json:"work_ref"`
		WorkStep   int64  `json:"work_step_seq"`
		AttemptRef string `json:"attempt_ref"`
	}
	_ = json.Unmarshal(resp, &task)
	if task.WorkRef != wid || task.WorkStep != 1 || task.AttemptRef != wid+"#a1" {
		t.Fatalf("详情绑定字段: %s", resp)
	}

	// 同键重放 → 200 dup + work 信息（W2 审查#10：活任务 dup 与首次提交同形，
	// 带原尝试定位与终局）
	code, resp = f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusOK || !strings.Contains(string(resp), `"dup":true`) {
		t.Fatalf("重放应 200 dup: %d %s", code, resp)
	}
	var dupAck struct {
		TaskID string `json:"task_id"`
		Work   *struct {
			WorkID     string `json:"work_id"`
			AttemptSeq int64  `json:"attempt_seq"`
			Outcome    string `json:"outcome"`
			TaskGone   bool   `json:"task_gone"`
		} `json:"work"`
	}
	if err := json.Unmarshal(resp, &dupAck); err != nil {
		t.Fatalf("dup 体解析: %v", err)
	}
	if dupAck.TaskID != ack.TaskID || dupAck.Work == nil || dupAck.Work.WorkID != wid ||
		dupAck.Work.AttemptSeq != 1 || dupAck.Work.TaskGone {
		t.Fatalf("活任务 dup 应带绑定回执: %s", resp)
	}

	// 绑定提交事件主体双入口（W2 审查#4）：PAT 入口 attempt_recorded principal
	// 记 pat:<令牌名>（不再取 SubmittedBy 固定的 admin），by=自报任期
	patSrv := httptest.NewServer(New(Config{Core: f.core, AdminToken: "test-admin-token", DataDir: t.TempDir()}).Handler())
	t.Cleanup(patSrv.Close)
	jar, _ := cookiejar.New(nil)
	patLogin := &http.Client{Jar: jar}
	lresp, err := patLogin.Post(patSrv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"test-admin-token"}`))
	if err != nil || lresp.StatusCode != http.StatusOK {
		t.Fatalf("PAT 面登录: %v %d", err, lresp.StatusCode)
	}
	lresp.Body.Close()
	tresp, err := patLogin.Post(patSrv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"mcp-bind"}`))
	if err != nil || tresp.StatusCode != http.StatusCreated {
		t.Fatalf("铸 PAT: %v %d", err, tresp.StatusCode)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(tresp.Body).Decode(&tok)
	tresp.Body.Close()
	patBody := `{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},` +
		`"submitted_by":"tester","idem_key":"k-pat","work_ref":"` + wid + `","step_seq":2,"prime_term":"prime-1"}`
	patReq, err := http.NewRequest(http.MethodPost, patSrv.URL+"/api/v1/tasks", strings.NewReader(patBody))
	if err != nil {
		t.Fatal(err)
	}
	patReq.Header.Set("Authorization", "Bearer "+tok.Token)
	patReq.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(patReq)
	if err != nil || presp.StatusCode != http.StatusAccepted {
		t.Fatalf("PAT 绑定提交: %v %d", err, presp.StatusCode)
	}
	presp.Body.Close()
	code, resp = f.do(hc, http.MethodGet, "/api/v1/works/"+wid, "")
	if code != http.StatusOK {
		t.Fatalf("工作单详情应 200: %d", code)
	}
	var wv struct {
		Work struct {
			Events []struct {
				Kind      string `json:"kind"`
				By        string `json:"by"`
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
	}
	_ = json.Unmarshal(resp, &wv)
	var rec *struct {
		Kind      string `json:"kind"`
		By        string `json:"by"`
		Principal string `json:"principal"`
	}
	for i := range wv.Work.Events {
		if wv.Work.Events[i].Kind == "attempt_recorded" {
			rec = &wv.Work.Events[i]
		}
	}
	if rec == nil || rec.By != "prime-1" || rec.Principal != "pat:mcp-bind" {
		t.Fatalf("PAT 绑定提交主体应独立落账: %+v", rec)
	}

	// 错误码映射：旧任期 409 work_term_conflict / 未知单 404 / 锁定步骤 409
	old := `{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},` +
		`"submitted_by":"tester","idem_key":"k9","work_ref":"` + wid + `","step_seq":2,"prime_term":"prime-9"}`
	code, resp = f.do(hc, http.MethodPost, "/api/v1/tasks", old)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_term_conflict") {
		t.Fatalf("旧任期应 409 work_term_conflict: %d %s", code, resp)
	}
	unknown := `{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},` +
		`"submitted_by":"tester","work_ref":"w-none","step_seq":1,"prime_term":"prime-1"}`
	if code, resp = f.do(hc, http.MethodPost, "/api/v1/tasks", unknown); code != http.StatusNotFound || !strings.Contains(string(resp), "work_not_found") {
		t.Fatalf("未知单应 404: %d %s", code, resp)
	}
	// 工作单无任期场景：注入清空任期 → 409 work_no_term
	if err := f.st.Update(func(st *store.State) error {
		w := st.Works[wid]
		w.PrimeTerm = ""
		st.Works[wid] = w
		return nil
	}); err != nil {
		t.Fatalf("注入清任期: %v", err)
	}
	noterm := `{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},` +
		`"submitted_by":"tester","work_ref":"` + wid + `","step_seq":2,"prime_term":"prime-2"}`
	if code, resp = f.do(hc, http.MethodPost, "/api/v1/tasks", noterm); code != http.StatusConflict || !strings.Contains(string(resp), "work_no_term") {
		t.Fatalf("无任期应 409 work_no_term: %d %s", code, resp)
	}
	// 三参不成组 400
	if code, _ = f.do(hc, http.MethodPost, "/api/v1/tasks", `{"target_node":"mac-mini","target_agent":"diskcheck","input":{},"submitted_by":"tester","work_ref":"`+wid+`"}`); code != http.StatusBadRequest {
		t.Fatalf("绑定参数不成组应 400: %d", code)
	}
}
