package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// saveSuggestEnv 保存 hub 全局配置并在测试后恢复（测试间互不污染）。
func saveSuggestEnv(t *testing.T) {
	t.Helper()
	oldURL, oldToken := hubURL, hubToken
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldToken })
}

// suggestHub 假 hub：/api/v1/dispatch-suggest 按 body 应答，并回传捕获的请求。
func suggestHub(t *testing.T, status int, respBody string, gotReq *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/dispatch-suggest" {
			http.NotFound(w, r)
			return
		}
		if gotReq != nil {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			*gotReq = req
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// C1 装配：hub 模式含 swarmmind_dispatch_suggest；node 模式不含（hub 专属不静默降级）。
func TestDispatchSuggestToolAssembly(t *testing.T) {
	var hubNames []string
	for _, td := range tools() {
		hubNames = append(hubNames, td.Name)
	}
	if !contains(hubNames, "swarmmind_dispatch_suggest") {
		t.Fatalf("hub 模式应装配 swarmmind_dispatch_suggest: %v", hubNames)
	}
	var nodeNames []string
	nb := nodeBackend{}
	for _, td := range nb.tools() {
		nodeNames = append(nodeNames, td.Name)
	}
	if contains(nodeNames, "swarmmind_dispatch_suggest") {
		t.Fatalf("node 模式不得装配 swarmmind_dispatch_suggest: %v", nodeNames)
	}
	// 描述面向 agent 写「能做什么」，含建议非必经与可改派语义。
	for _, td := range tools() {
		if td.Name == "swarmmind_dispatch_suggest" {
			if !strings.Contains(td.Description, "建议不是必经") || !strings.Contains(td.Description, "改派") {
				t.Fatalf("描述应说明建议非必经与可改派语义: %s", td.Description)
			}
		}
	}
}

// C2 建议应答：canned REST 应答 → 人话文本（目标/置信度/来源/理由/概率/覆盖指引）。
func TestDispatchSuggestCall(t *testing.T) {
	saveSuggestEnv(t)
	var gotReq map[string]any
	hub := suggestHub(t, 200, `{"suggestion":{"target_agent":"diskcheck","target_node":"win-box"},
		"confidence":0.87,"probabilities":{"diskcheck@win-box":0.87,"echo@mac-mini":0.13},
		"provider":"llm-as-jev","degraded":false,"reason":"巡检脚本对口"}`, &gotReq)
	hubURL, hubToken = hub.URL, "t"

	res, rpcErr := callTool("swarmmind_dispatch_suggest", map[string]any{"goal": "检查 C 盘剩余空间"})
	if rpcErr != nil {
		t.Fatalf("不应报协议错误: %v", rpcErr)
	}
	text := resultText(t, res)
	if strings.Contains(text, "isError") {
		t.Fatalf("建议应答不应是错误: %s", text)
	}
	for _, want := range []string{"建议派给 diskcheck @ win-box", "llm-as-jev", "87%", "巡检脚本对口", "echo@mac-mini 13%", "可自行覆盖"} {
		if !strings.Contains(text, want) {
			t.Fatalf("文本缺 %q: %s", want, text)
		}
	}
	if gotReq["goal"] != "检查 C 盘剩余空间" {
		t.Fatalf("请求体应只带 goal（候选集由 hub 组装）: %v", gotReq)
	}
	if _, ok := gotReq["candidates"]; ok {
		t.Fatalf("不得携带候选参数: %v", gotReq)
	}
}

// C3 暂无建议：suggestion:null 是正常应答（isError=false），人话转述原因与手动派单指引。
func TestDispatchSuggestNoSuggestion(t *testing.T) {
	saveSuggestEnv(t)
	hub := suggestHub(t, 200, `{"suggestion":null,"provider":"llm-as-jev","degraded":true,
		"note":"决策模型暂不可用（决策超时）：可手动选择执行者，派单不受影响"}`, nil)
	hubURL, hubToken = hub.URL, "t"
	res, rpcErr := callTool("swarmmind_dispatch_suggest", map[string]any{"goal": "x"})
	if rpcErr != nil {
		t.Fatalf("无建议是正常应答不应报错: %v", rpcErr)
	}
	text := resultText(t, res)
	for _, want := range []string{"暂无建议", "决策模型暂不可用", "swarmmind_submit", "不被此阻塞"} {
		if !strings.Contains(text, want) {
			t.Fatalf("文本缺 %q: %s", want, text)
		}
	}
}

// C4 参数校验：goal 缺失/空白 → error。
func TestDispatchSuggestValidation(t *testing.T) {
	if _, err := toolDispatchSuggest(map[string]any{}); err == nil {
		t.Fatal("缺 goal 应报错")
	}
	if _, err := toolDispatchSuggest(map[string]any{"goal": "   "}); err == nil {
		t.Fatal("纯空白 goal 应报错")
	}
}

// C5 hub 不可达：网络故障是工具错误（与「无建议」语义分界）。
func TestDispatchSuggestHubUnreachable(t *testing.T) {
	saveSuggestEnv(t)
	hub := suggestHub(t, 200, `{}`, nil)
	hubURL, hubToken = hub.URL, "t"
	hub.Close() // 立即关闭制造连接拒绝
	_, err := toolDispatchSuggest(map[string]any{"goal": "x"})
	if err == nil {
		t.Fatal("hub 不可达应报错")
	}
}

// C6 未配置 hub 连接：callTool 统一拦截（既有口径回归）。
func TestDispatchSuggestNoHubConfig(t *testing.T) {
	saveSuggestEnv(t)
	hubURL, hubToken = "", ""
	res, rpcErr := callTool("swarmmind_dispatch_suggest", map[string]any{"goal": "x"})
	if rpcErr != nil {
		t.Fatalf("应 isError 结果而非协议错误: %v", rpcErr)
	}
	if !strings.Contains(resultText(t, res), "未配置") {
		t.Fatalf("应提示未配置: %s", resultText(t, res))
	}
}

// ---------- 复用件 ----------

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// resultText 从 tools/call 结果取首个 text 内容。
func resultText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]textContent)
	if !ok || len(content) == 0 {
		t.Fatalf("结果形态不符: %v", res)
	}
	return content[0].Text
}
