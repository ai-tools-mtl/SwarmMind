// works_test.go Work 工作单 REST 端点契约（P5 W1）：鉴权门禁、201/200 幂等、
// 步骤校验 400 映射、列表/详情/事件游标、404、服务端验证主体落账。
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{http.MethodPost, "/api/v1/works/w-x/collect"}, // W3 收集/关闭同门禁
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

// completeStepREST 绑定派发一步并以 node 身份回传 completed（membus 同步链，
// 身份=端点名；api 层造终态步骤供收集用例）。baseURL+client 由调用方给
// （f.srv+会话 或 PAT 专用 srv+PAT 客户端）。
func completeStepREST(f *fixture, hc *http.Client, baseURL, wid, term string, step int64, idem string) string {
	f.t.Helper()
	body := fmt.Sprintf(`{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},`+
		`"idem_key":%q,"work_ref":%q,"step_seq":%d,"prime_term":%q}`, idem, wid, step, term)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tasks", strings.NewReader(body))
	if err != nil {
		f.t.Fatalf("构造提交: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp2, err := hc.Do(req)
	if err != nil {
		f.t.Fatalf("绑定派发: %v", err)
	}
	defer resp2.Body.Close()
	resp, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusAccepted {
		f.t.Fatalf("绑定派发应 202, got %d: %s", resp2.StatusCode, resp)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(resp, &ack); err != nil || ack.TaskID == "" {
		f.t.Fatalf("202 体解析: %s", resp)
	}
	b, err := json.Marshal(protocol.TaskResultPayload{TaskID: ack.TaskID, Status: protocol.ResultCompleted,
		Result: &protocol.TaskResult{Status: protocol.ResultCompleted, ExitCode: 0, FinalAnswer: "ok"}})
	if err != nil {
		f.t.Fatalf("结果编码: %v", err)
	}
	if err := f.bus.Endpoint("mac-mini").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: b,
	}); err != nil {
		f.t.Fatalf("结果回传: %v", err)
	}
	return ack.TaskID
}

// TestWorkCollectEndpoint 收集端点契约（P5 W3）：401 门禁、200/回执 dup、
// 错误码映射（404/work_closed/work_no_term/work_term_conflict/work_steps_open/
// work_steps_not_done/invalid_request）、PAT 主体落账、终态盖章字段回读。
func TestWorkCollectEndpoint(t *testing.T) {
	f := newFixture(t)
	registerEchoNode(f)
	hc := f.client(true)

	// 未登录 401
	if code, _ := f.do(f.client(false), http.MethodPost, "/api/v1/works/w-x/collect", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("未登录收集应 401, got %d", code)
	}

	// 全 done → 收集 200；终态盖章与报告回读
	wid := createWorkBody(f, hc, "收集端点", "prime-1")
	completeStepREST(f, hc, f.srv.URL, wid, "prime-1", 1, "c1")
	completeStepREST(f, hc, f.srv.URL, wid, "prime-1", 2, "c2")
	completeStepREST(f, hc, f.srv.URL, wid, "prime-1", 3, "c3")
	code, resp := f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/collect",
		`{"prime_term":"prime-1","report":"三步全部完成。","outcome":"done"}`)
	if code != http.StatusOK {
		t.Fatalf("收集应 200, got %d: %s", code, resp)
	}
	var out struct {
		Work struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			Report    string  `json:"report"`
			ClosedAt  *string `json:"closed_at"`
			ExpiresAt *string `json:"expires_at"`
			Events    []struct {
				Kind      string `json:"kind"`
				By        string `json:"by"`
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("200 体解析: %v", err)
	}
	if out.Work.Status != "done" || out.Work.Report != "三步全部完成。" || out.Work.ClosedAt == nil || out.Work.ExpiresAt == nil || out.Dup {
		t.Fatalf("终态盖章回读: %s", resp)
	}
	last := out.Work.Events[len(out.Work.Events)-1]
	if last.Kind != "collect" || last.Principal != "admin" || last.By != "prime-1" {
		t.Fatalf("collect 事件主体双字段: %+v", last)
	}

	// 同参重放 → 200+dup:true；不同报告 → 409 work_closed
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/collect",
		`{"prime_term":"prime-1","report":"三步全部完成。","outcome":"done"}`)
	if code != http.StatusOK || !strings.Contains(string(resp), `"dup":true`) {
		t.Fatalf("回执重放应 200+dup: %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/collect",
		`{"prime_term":"prime-1","report":"另写的报告","outcome":"done"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_closed") {
		t.Fatalf("报告不同应 409 work_closed: %d %s", code, resp)
	}

	// 未终态步骤 → 409 work_steps_open
	widOpen := createWorkBody(f, hc, "有在途步骤", "prime-1")
	completeStepREST(f, hc, f.srv.URL, widOpen, "prime-1", 1, "o1")
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+widOpen+"/collect",
		`{"prime_term":"prime-1","report":"x"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_steps_open") {
		t.Fatalf("未终态应 409 work_steps_open: %d %s", code, resp)
	}

	// 部分完成 → 409 work_steps_not_done（指路 abandoned）；abandoned 关闭 200
	completeStepREST(f, hc, f.srv.URL, widOpen, "prime-1", 2, "o2")
	body := fmt.Sprintf(`{"target_node":"mac-mini","target_agent":"diskcheck","input":{"args":["run"]},"idem_key":"o3","work_ref":%q,"step_seq":3,"prime_term":"prime-1"}`, widOpen)
	code, resp = f.do(hc, http.MethodPost, "/api/v1/tasks", body)
	if code != http.StatusAccepted {
		t.Fatalf("派发 3 应 202: %d %s", code, resp)
	}
	var ack struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(resp, &ack)
	rb, _ := json.Marshal(protocol.TaskResultPayload{TaskID: ack.TaskID, Status: protocol.ResultFailed,
		Result: &protocol.TaskResult{Status: protocol.ResultFailed, ExitCode: 2, Error: "boom"}})
	if err := f.bus.Endpoint("mac-mini").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: rb,
	}); err != nil {
		t.Fatalf("失败回传: %v", err)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+widOpen+"/collect", `{"prime_term":"prime-1","report":"x"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_steps_not_done") {
		t.Fatalf("部分完成应 409 work_steps_not_done: %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+widOpen+"/collect",
		`{"prime_term":"prime-1","report":"步骤 2 失败，用户决策放弃。","outcome":"abandoned"}`)
	if code != http.StatusOK {
		t.Fatalf("abandoned 关闭应 200: %d %s", code, resp)
	}
	var ab struct {
		Work struct {
			Status string `json:"status"`
			Report string `json:"report"`
		} `json:"work"`
	}
	_ = json.Unmarshal(resp, &ab)
	if ab.Work.Status != "abandoned" || ab.Work.Report == "" {
		t.Fatalf("abandoned 盖章: %s", resp)
	}

	// W3 审查②：pending 步骤随 abandoned 关闭记 skipped（REST 契约同 core）
	widPending := createWorkBody(f, hc, "pending 随放弃跳过", "prime-1")
	completeStepREST(f, hc, f.srv.URL, widPending, "prime-1", 1, "ps1")
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+widPending+"/collect",
		`{"prime_term":"prime-1","report":"步骤 1 完成，其余放弃。","outcome":"abandoned"}`)
	if code != http.StatusOK {
		t.Fatalf("pending 随 abandoned 关闭应 200: %d %s", code, resp)
	}
	var pd struct {
		Work struct {
			Status string `json:"status"`
			Steps  []struct {
				Seq    int64  `json:"seq"`
				Status string `json:"status"`
			} `json:"steps"`
		} `json:"work"`
	}
	_ = json.Unmarshal(resp, &pd)
	if pd.Work.Status != "abandoned" || len(pd.Work.Steps) != 3 ||
		pd.Work.Steps[1].Status != "skipped" || pd.Work.Steps[2].Status != "skipped" {
		t.Fatalf("未派发步骤应记 skipped: %s", resp)
	}

	// 未知单 404；空报告 400；旧任期 409 work_term_conflict
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/w-missing/collect", `{"prime_term":"t","report":"x"}`)
	if code != http.StatusNotFound || !strings.Contains(string(resp), "work_not_found") {
		t.Fatalf("未知单应 404: %d %s", code, resp)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+createWorkBody(f, hc, "空报告", "prime-1")+"/collect", `{"prime_term":"prime-1","report":"  "}`)
	if code != http.StatusBadRequest || !strings.Contains(string(resp), "invalid_request") {
		t.Fatalf("空报告应 400: %d %s", code, resp)
	}
	widT := createWorkBody(f, hc, "旧任期收集", "prime-1")
	code, _ = f.do(hc, http.MethodPost, "/api/v1/works/"+widT+"/takeover", `{"expected_term":"prime-1","new_term":"prime-2"}`)
	if code != http.StatusOK {
		t.Fatalf("接管应 200: %d", code)
	}
	code, resp = f.do(hc, http.MethodPost, "/api/v1/works/"+widT+"/collect", `{"prime_term":"prime-1","report":"x"}`)
	if code != http.StatusConflict || !strings.Contains(string(resp), "work_term_conflict") {
		t.Fatalf("旧任期收集应 409 work_term_conflict: %d %s", code, resp)
	}

	// PAT 入口收集：事件 principal=pat:<令牌名>
	dataDir := t.TempDir()
	srv := httptest.NewServer(New(Config{Core: f.core, AdminToken: "tok2", DataDir: dataDir}).Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	patc := &http.Client{Jar: jar}
	r2, err := patc.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader(`{"token":"tok2"}`))
	if err != nil || r2.StatusCode != http.StatusOK {
		t.Fatalf("登录2: %v %d", err, r2.StatusCode)
	}
	r2.Body.Close()
	r2, err = patc.Post(srv.URL+"/api/v1/auth/tokens", "application/json", strings.NewReader(`{"name":"collector"}`))
	if err != nil || r2.StatusCode != http.StatusCreated {
		t.Fatalf("铸 PAT: %v %d", err, r2.StatusCode)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&tok)
	r2.Body.Close()
	// PAT 客户端建单（同一 srv：createWorkBody 固定打 f.srv，此处手工建）
	pw, _ := json.Marshal(map[string]any{
		"goal": "PAT 收集", "prime_term": "prime-1",
		"steps": []map[string]any{
			{"desc": "盘点磁盘", "target_node": "mac-mini", "target_agent": "diskcheck"},
			{"desc": "盘点内存", "target_node": "mac-mini", "target_agent": "diskcheck"},
			{"desc": "汇总报告", "target_node": "mac-mini", "target_agent": "reporter", "depends_on": []int{1, 2}},
		},
	})
	r4, err := patc.Post(srv.URL+"/api/v1/works", "application/json", strings.NewReader(string(pw)))
	if err != nil || r4.StatusCode != http.StatusCreated {
		t.Fatalf("PAT 建单: %v %d", err, r4.StatusCode)
	}
	var pwOut struct {
		Work struct {
			ID string `json:"id"`
		} `json:"work"`
	}
	_ = json.NewDecoder(r4.Body).Decode(&pwOut)
	r4.Body.Close()
	widP := pwOut.Work.ID
	completeStepREST(f, patc, srv.URL, widP, "prime-1", 1, "pc1")
	completeStepREST(f, patc, srv.URL, widP, "prime-1", 2, "pc2")
	completeStepREST(f, patc, srv.URL, widP, "prime-1", 3, "pc3")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/works/"+widP+"/collect",
		strings.NewReader(`{"prime_term":"prime-1","report":"PAT 入口收集。"}`))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	r3, err := http.DefaultClient.Do(req)
	if err != nil || r3.StatusCode != http.StatusOK {
		t.Fatalf("PAT 收集: %v %v", err, r3)
	}
	var patOut struct {
		Work struct {
			Status string `json:"status"`
			Events []struct {
				Principal string `json:"principal"`
			} `json:"events"`
		} `json:"work"`
	}
	_ = json.NewDecoder(r3.Body).Decode(&patOut)
	r3.Body.Close()
	if patOut.Work.Status != "done" {
		t.Fatalf("PAT 收集应 done: %s", patOut.Work.Status)
	}
	evs := patOut.Work.Events
	if len(evs) == 0 || evs[len(evs)-1].Principal != "pat:collector" {
		t.Fatalf("PAT 入口 collect 事件主体: %+v", evs)
	}
}

// TestWorkTasksGoneREST 任务 24h 清理后工作单读档仍全量可解释，且 tasks_gone
// 标注到位（W3 接口补全；核心 3「REST 与 MCP 读档同形」的 REST 面）。
func TestWorkTasksGoneREST(t *testing.T) {
	f := newFixture(t)
	registerEchoNode(f)
	hc := f.client(true)
	wid := createWorkBody(f, hc, "清理后读档", "prime-1")
	var taskIDs []string
	for i, key := range []string{"g1", "g2", "g3"} {
		taskIDs = append(taskIDs, completeStepREST(f, hc, f.srv.URL, wid, "prime-1", int64(i+1), key))
	}
	code, resp := f.do(hc, http.MethodPost, "/api/v1/works/"+wid+"/collect", `{"prime_term":"prime-1","report":"完成。"}`)
	if code != http.StatusOK {
		t.Fatalf("收集应 200: %d %s", code, resp)
	}
	// 清理前 tasks_gone 为空
	_, resp = f.do(hc, http.MethodGet, "/api/v1/works/"+wid, "")
	if strings.Contains(string(resp), "tasks_gone") {
		t.Fatalf("清理前不应有 tasks_gone: %s", resp)
	}
	// 回拨任务过 24h → Sweep 清理 → 读档仍全量 + tasks_gone 标注
	if err := f.st.Update(func(st *store.State) error {
		for _, id := range taskIDs {
			tk := st.Tasks[id]
			tk.UpdatedAt = tk.UpdatedAt.Add(-protocol.TerminalRetention - time.Minute)
			st.Tasks[id] = tk
		}
		return nil
	}); err != nil {
		t.Fatalf("回拨: %v", err)
	}
	f.core.Sweep()
	code, resp = f.do(hc, http.MethodGet, "/api/v1/works/"+wid, "")
	if code != http.StatusOK {
		t.Fatalf("清理后读档应 200: %d", code)
	}
	var out struct {
		Work struct {
			Status   string `json:"status"`
			Report   string `json:"report"`
			Attempts []struct {
				TaskID  string `json:"task_id"`
				Outcome string `json:"outcome"`
				IdemKey string `json:"idem_key"`
			} `json:"attempts"`
			TasksGone []string `json:"tasks_gone"`
		} `json:"work"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("读档解析: %v", err)
	}
	if out.Work.Status != "done" || out.Work.Report == "" || len(out.Work.Attempts) != 3 {
		t.Fatalf("清理后执行摘要缺失: %s", resp)
	}
	if len(out.Work.TasksGone) != 3 {
		t.Fatalf("tasks_gone 应 3 项: %s", resp)
	}
	for _, id := range taskIDs {
		if !strings.Contains(string(resp), id) {
			t.Fatalf("tasks_gone 缺 %s", id)
		}
	}
}
