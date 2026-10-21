package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// saveDecideEnv 保存 startlux/hub 全局配置并在测试结束后恢复（测试间互不污染）。
func saveDecideEnv(t *testing.T) {
	t.Helper()
	old := struct {
		url, key, model  string
		timeout          time.Duration
		client           *http.Client
		hubURL, hubToken string
	}{startluxURL, startluxKey, startluxModel, startluxTimeout, startluxClient, hubURL, hubToken}
	t.Cleanup(func() {
		startluxURL, startluxKey, startluxModel = old.url, old.key, old.model
		startluxTimeout, startluxClient = old.timeout, old.client
		hubURL, hubToken = old.hubURL, old.hubToken
	})
}

// fakeHub 假 hub：能力目录（diskcheck 双节点 + coder）与节点负载。默认节点表
// mac-mini inflight 2 / baidu-node inflight 0 均在线；传 nodes 可整体覆盖节点表，
// 供离线过滤用例构造全部/部分离线场景。
func fakeHub(t *testing.T, nodes ...map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []map[string]any{
				{"node_id": "mac-mini", "online": true, "card": map[string]any{"name": "diskcheck", "kind": "cli", "description": "磁盘空间巡检与占用分析工具"}},
				{"node_id": "baidu-node", "online": true, "card": map[string]any{"name": "diskcheck", "kind": "cli", "description": "磁盘空间巡检与占用分析工具"}},
				{"node_id": "baidu-node", "online": true, "card": map[string]any{"name": "coder", "kind": "cli", "description": "代码编写与编码实现助手"}},
			}})
		case "/api/v1/nodes":
			ns := nodes
			if len(ns) == 0 {
				ns = []map[string]any{
					{"node_id": "mac-mini", "online": true, "epoch": 1, "inflight": 2},
					{"node_id": "baidu-node", "online": true, "epoch": 1, "inflight": 0},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": ns})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// countingStartLux 假 StartLux：原子计数命中次数（超时场景下 handler goroutine 写入与测试
// goroutine 读取之间没有 happens-before 边，必须用 atomic），status 决定返回码，body 决定响应体，delay 模拟慢响应。
func countingStartLux(t *testing.T, status int, body string, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const l0FixedResponse = `{"answers":{"target":{"choice":"mac-mini/diskcheck","probabilities":{"mac-mini/diskcheck":0.72,"baidu-node/diskcheck":0.28},"rationale":"两节点均具备磁盘巡检能力，mac-mini 当前 inflight 更低"}}}`

// 验收①：L0 成功路径——固定 decisions 响应 → used_level=L0，choice/概率/理由解析正确。
func TestSwarmmindDecideL0Success(t *testing.T) {
	saveDecideEnv(t)
	var gotPath, gotAuth, gotThinkMode, gotState string
	var gotCriteria map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model     string `json:"model"`
			State     string `json:"state"`
			ThinkMode string `json:"think_mode"`
			Questions map[string]struct {
				Type     string         `json:"type"`
				Criteria map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotThinkMode = req.ThinkMode
		gotState = req.State
		if q, ok := req.Questions["target"]; ok {
			gotCriteria = q.Criteria
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(l0FixedResponse))
	}))
	defer srv.Close()

	startluxKey = "test-key-123"
	startluxURL = srv.URL
	startluxModel = "test-model"
	hubURL, hubToken = "", "" // 目录富化尽力而为：hub 缺席不影响 L0

	out, err := swarmmindDecide("检查磁盘占用情况", []string{"mac-mini/diskcheck", "baidu-node/diskcheck"})
	if err != nil {
		t.Fatalf("L0 不应报错：%v", err)
	}
	if out.UsedLevel != "L0" || out.Degraded {
		t.Fatalf("used_level=%s degraded=%v，应为 L0/false", out.UsedLevel, out.Degraded)
	}
	if out.Choice != "mac-mini/diskcheck" {
		t.Fatalf("choice=%q，应为 mac-mini/diskcheck", out.Choice)
	}
	if len(out.Probabilities) != 2 || out.Probabilities["mac-mini/diskcheck"] != 0.72 || out.Probabilities["baidu-node/diskcheck"] != 0.28 {
		t.Fatalf("概率解析错误：%v", out.Probabilities)
	}
	if out.Confidence != 0.72 {
		t.Fatalf("confidence=%v，应为选中项概率 0.72", out.Confidence)
	}
	if out.Rationale == "" {
		t.Fatal("rationale 不应为空")
	}
	// 请求形状：路径/Bearer/model/state/questions
	if gotPath != "/v1/decisions" {
		t.Fatalf("请求路径 %q，应为 /v1/decisions", gotPath)
	}
	if gotAuth != "Bearer test-key-123" {
		t.Fatalf("认证头 %q，应为 Bearer test-key-123", gotAuth)
	}
	if gotThinkMode != "auto" || gotState == "" || !strings.Contains(gotState, "检查磁盘占用情况") {
		t.Fatalf("请求体不合规：think_mode=%q state=%q", gotThinkMode, gotState)
	}
	if _, ok := gotCriteria["mac-mini/diskcheck"]; !ok {
		t.Fatalf("criteria 应包含每个候选：%v", gotCriteria)
	}
}

// 验收②：L0 失败（500 / 超时 / 200 坏 JSON）→ 降级 L2 规则分配，云端恰好调 1 次。
func TestSwarmmindDecideL0FallbackToL2(t *testing.T) {
	hub := fakeHub(t)
	cases := []struct {
		name          string
		status        int
		body          string
		delay         time.Duration
		wantRationale string
	}{
		{"L0 返回 500", 500, `{"error":"boom"}`, 0, "云端返回 500"},
		{"L0 超时", 200, l0FixedResponse, 500 * time.Millisecond, "云端查询失败"},
		{"L0 返回 200 但坏 JSON", 200, `not-json{`, 0, "云端响应解析失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveDecideEnv(t)
			srv, hits := countingStartLux(t, tc.status, tc.body, tc.delay)
			startluxKey = "test-key"
			startluxURL = srv.URL
			if tc.delay > 0 {
				startluxClient = &http.Client{Timeout: 100 * time.Millisecond}
			}
			hubURL, hubToken = hub.URL, "t"

			out, err := swarmmindDecide("检查磁盘占用情况", []string{"mac-mini/diskcheck", "baidu-node/coder"})
			if err != nil {
				t.Fatalf("降级后不应报错：%v", err)
			}
			if hits.Load() != 1 {
				t.Fatalf("StartLux 应被调用恰好 1 次，实际 %d（降级不得在云端重试）", hits.Load())
			}
			if out.UsedLevel != "L2" || !out.Degraded {
				t.Fatalf("used_level=%s degraded=%v，应为 L2/true", out.UsedLevel, out.Degraded)
			}
			if out.Confidence != 0.5 {
				t.Fatalf("L2 confidence=%v，应固定 0.5", out.Confidence)
			}
			if !strings.Contains(out.Rationale, tc.wantRationale) || !strings.Contains(out.Rationale, "降级") {
				t.Fatalf("rationale 应说明降级原因（含 %q）：%s", tc.wantRationale, out.Rationale)
			}
			if out.Choice != "mac-mini/diskcheck" {
				t.Fatalf("choice=%q：子任务「检查磁盘占用情况」应按关键词命中 diskcheck（磁/盘/占/用/检）", out.Choice)
			}
		})
	}
}

// 验收③：L2 规则匹配——关键词命中与同分 inflight 决胜。
func TestSwarmmindDecideL2KeywordMatch(t *testing.T) {
	hub := fakeHub(t)
	cases := []struct {
		name       string
		subtask    string
		options    []string
		wantChoice string
	}{
		{"关键词命中 coder", "帮忙编码实现一个排序算法", []string{"mac-mini/diskcheck", "baidu-node/coder"}, "baidu-node/coder"},
		{"同分取 inflight 最低", "磁盘巡检并回报占用", []string{"mac-mini/diskcheck", "baidu-node/diskcheck"}, "baidu-node/diskcheck"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveDecideEnv(t)
			hubURL, hubToken = hub.URL, "t"
			out, err := decideL2(tc.subtask, tc.options, "单测直调")
			if err != nil {
				t.Fatalf("L2 不应报错：%v", err)
			}
			if out.UsedLevel != "L2" || out.Choice != tc.wantChoice {
				t.Fatalf("choice=%q，应为 %q（used_level=%s）", out.Choice, tc.wantChoice, out.UsedLevel)
			}
			if out.Confidence != 0.5 {
				t.Fatalf("L2 confidence=%v，应固定 0.5", out.Confidence)
			}
		})
	}
}

// 验收③补：L2 只考虑在线节点——离线候选即使 inflight 更低也不得选中；
// 候选所在节点全部离线 → 报错「所有候选节点离线，需人工决策」。
func TestSwarmmindDecideL2OfflineFilter(t *testing.T) {
	t.Run("离线节点不参与分配", func(t *testing.T) {
		saveDecideEnv(t)
		hub := fakeHub(t,
			map[string]any{"node_id": "mac-mini", "online": true, "epoch": 1, "inflight": 2},
			map[string]any{"node_id": "baidu-node", "online": false, "epoch": 1, "inflight": 0},
		)
		hubURL, hubToken = hub.URL, "t"
		out, err := decideL2("磁盘巡检并回报占用", []string{"mac-mini/diskcheck", "baidu-node/diskcheck"}, "单测直调")
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if out.Choice != "mac-mini/diskcheck" {
			t.Fatalf("choice=%q：baidu-node 离线（inflight 0）不得决胜，应选在线的 mac-mini/diskcheck", out.Choice)
		}
	})
	t.Run("全部候选节点离线", func(t *testing.T) {
		saveDecideEnv(t)
		hub := fakeHub(t,
			map[string]any{"node_id": "mac-mini", "online": false, "epoch": 1, "inflight": 0},
			map[string]any{"node_id": "baidu-node", "online": false, "epoch": 1, "inflight": 0},
		)
		hubURL, hubToken = hub.URL, "t"
		_, err := decideL2("磁盘巡检并回报占用", []string{"mac-mini/diskcheck", "baidu-node/diskcheck"}, "单测直调")
		if err == nil {
			t.Fatal("全部候选节点离线应报错")
		}
		if !strings.Contains(err.Error(), "所有候选节点离线，需人工决策") {
			t.Fatalf("错误提示不符：%v", err)
		}
	})
}

// 验收④：无任何关键词匹配 → 明确报错需人工决策。
func TestSwarmmindDecideNoMatch(t *testing.T) {
	saveDecideEnv(t)
	hub := fakeHub(t)
	hubURL, hubToken = hub.URL, "t"
	_, err := decideL2("煮一杯咖啡并拍照留念", []string{"mac-mini/diskcheck", "baidu-node/coder"}, "单测直调")
	if err == nil {
		t.Fatal("无匹配应报错")
	}
	if !strings.Contains(err.Error(), "无匹配能力，需人工决策") {
		t.Fatalf("错误提示不符：%v", err)
	}
}

// 验收⑤：KEY 为空 → 不触碰云端，直接 L2。
func TestSwarmmindDecideKeyEmptyGoesStraightL2(t *testing.T) {
	saveDecideEnv(t)
	srv, hits := countingStartLux(t, 200, l0FixedResponse, 0)
	startluxKey = ""
	startluxURL = srv.URL
	hub := fakeHub(t)
	hubURL, hubToken = hub.URL, "t"

	out, err := swarmmindDecide("检查磁盘占用情况", []string{"mac-mini/diskcheck", "baidu-node/coder"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("KEY 为空时不得调用云端，实际调用 %d 次", hits.Load())
	}
	if out.UsedLevel != "L2" || !out.Degraded || out.Choice != "mac-mini/diskcheck" {
		t.Fatalf("used_level=%s degraded=%v choice=%s，应为 L2/true/mac-mini/diskcheck", out.UsedLevel, out.Degraded, out.Choice)
	}
}

// 输入校验：候选不足 2 项、空描述必须报错。
func TestSwarmmindDecideInputValidation(t *testing.T) {
	if _, err := toolDecide(map[string]any{"subtask_description": "x", "capability_options": []any{"only/one"}}); err == nil {
		t.Fatal("候选仅 1 项应报错")
	}
	if _, err := toolDecide(map[string]any{"capability_options": []any{"a", "b"}}); err == nil {
		t.Fatal("缺 subtask_description 应报错")
	}
}

// 容错解析：非典型响应形态也能取到 choice（ answers.target 为字符串 / decisions 层 / value 同义键）。
func TestParseStartluxDecisionsTolerance(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"answers.target 字符串", `{"answers":{"target":"mac-mini/diskcheck"}}`, "mac-mini/diskcheck"},
		{"decisions 层对象", `{"decisions":{"target":{"value":"baidu-node/coder","probs":{"baidu-node/coder":0.9}}}}`, "baidu-node/coder"},
		{"顶层 choice", `{"choice":"x/y","probabilities":{"x/y":"0.55"}}`, "x/y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choice, _, _, err := parseStartluxDecisions([]byte(tc.body))
			if err != nil || choice != tc.want {
				t.Fatalf("choice=%q err=%v，应为 %q/nil", choice, err, tc.want)
			}
		})
	}
	if _, _, _, err := parseStartluxDecisions([]byte(`{"unrelated":1}`)); err == nil {
		t.Fatal("无决策结果应报解析错误")
	}
	if _, _, _, err := parseStartluxDecisions([]byte("not-json")); err == nil {
		t.Fatal("非 JSON 应报解析错误")
	}
}
