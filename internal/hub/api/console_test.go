package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Loop 6 控制台前端（internal/hub/web，go:embed）+ 任务列表端点的冒烟与契约测试。

// TestConsoleStaticEmbed 静态资源冒烟：GET / 返回 200 且含 SwarmMind；
// /app.js /style.css 可达且 Content-Type 正确；静态路径免鉴权（登录页必须可达），
// 未知静态路径 404，且静态资源不泄漏会话接口。
func TestConsoleStaticEmbed(t *testing.T) {
	f := newFixture(t)
	hc := f.client(false) // 未登录：静态资源必须可达

	// GET / 控制台入口：200 + SwarmMind
	code, out := f.do(hc, http.MethodGet, "/", "")
	if code != http.StatusOK {
		t.Fatalf("GET / 应 200, got %d", code)
	}
	if !strings.Contains(string(out), "SwarmMind") {
		t.Fatal("GET / 响应应含 SwarmMind")
	}
	if !strings.Contains(string(out), `href="/style.css"`) || !strings.Contains(string(out), `src="/app.js"`) {
		t.Fatal("index.html 应引用 /style.css 与 /app.js")
	}

	// 静态资源可达 + Content-Type
	for _, tc := range []struct{ path, ctype, needle string }{
		{"/app.js", "text/javascript", "/api/v1/"},
		{"/style.css", "text/css", "--blue"},
	} {
		code, out = f.do(hc, http.MethodGet, tc.path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", tc.path, code)
		}
		resp, err := hc.Get(f.srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		ct := resp.Header.Get("Content-Type")
		resp.Body.Close()
		if !strings.HasPrefix(ct, tc.ctype) {
			t.Fatalf("%s Content-Type 应为 %s, got %s", tc.path, tc.ctype, ct)
		}
		if !strings.Contains(string(out), tc.needle) {
			t.Fatalf("%s 内容不符（缺 %s）", tc.path, tc.needle)
		}
	}

	// 未知静态路径 404（embed 集合即白名单）
	if code, _ := f.do(hc, http.MethodGet, "/nope.js", ""); code != http.StatusNotFound {
		t.Fatalf("未知静态路径应 404, got %d", code)
	}
}

// TestListTasks 任务列表端点（控制台总览统计与监管页数据源）：未登录 401；
// 登录后返回全量任务（含 needs_review 与 review 记录），创建时间倒序。
func TestListTasks(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)

	// 未登录 401
	if code, _ := f.do(f.client(false), http.MethodGet, "/api/v1/tasks", ""); code != http.StatusUnauthorized {
		t.Fatalf("未登录列表应 401, got %d", code)
	}

	// 造三个任务（两个 submitted + 一个 needs_review 带 review）
	id1 := f.submitEcho(hc, "nodeB")
	id2 := f.submitEcho(hc, "nodeB")
	f.makeNeedsReview(id1)
	if _, err := f.core.RecordReview(id1, protocol.ReviewAckReview, "已人工确认", "admin"); err != nil {
		t.Fatal(err)
	}

	code, out := f.do(hc, http.MethodGet, "/api/v1/tasks", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200, got %d: %s", code, out)
	}
	var got struct {
		Tasks []protocol.Task `json:"tasks"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("列表体应为 {tasks:[...]}: %s", out)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("应返回 2 条任务, got %d: %s", len(got.Tasks), out)
	}
	// 倒序：后提交的 id2 在前
	if got.Tasks[0].ID != id2 || got.Tasks[1].ID != id1 {
		t.Fatalf("应按创建时间倒序: [%s %s]", got.Tasks[0].ID, got.Tasks[1].ID)
	}
	// needs_review + review 记录随列表返回
	if got.Tasks[1].Status != protocol.StatusNeedsReview || got.Tasks[1].Review == nil {
		t.Fatalf("needs_review 任务应含 review 记录: %+v", got.Tasks[1])
	}

	// 空库：返回空数组而非 null（前端 .filter 兼容）
	f2 := newFixture(t)
	code, out = f2.do(f2.client(true), http.MethodGet, "/api/v1/tasks", "")
	if code != http.StatusOK || !strings.Contains(string(out), `"tasks":[]`) {
		t.Fatalf("空库应 200 + tasks:[], got %d: %s", code, out)
	}
}
