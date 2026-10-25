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
