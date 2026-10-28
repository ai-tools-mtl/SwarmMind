// members_llm_test.go SuggestMembers 真实链路上游（R1-07，2026-10-28）：httptest
// 假 GLM 端点真打 llmProvider.SuggestMembers——prompt 组装、members 解析、空集合、
// 围栏容错、非法 JSON、上游错误脱敏逐项验证。members_test.go 的 mock 只覆盖
// Service 出口语义，不经 provider 的 HTTP/提示词/解析层，删 llm.go 也能绿；本文件
// 锁住真实多选协议。
package decision

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// llmUpstream 假 GLM 端点：捕获最近一次判定请求（多选一次调用即决，无多轮），
// 应答由脚本函数给出（完整 HTTP 状态码与响应体）。
type llmUpstream struct {
	srv *httptest.Server

	mu     sync.Mutex
	method string
	path   string
	auth   string
	body   chatRequest
}

func newLLMUpstreamFunc(t *testing.T, fn func(r *http.Request, req chatRequest) (int, string)) *llmUpstream {
	t.Helper()
	up := &llmUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		up.mu.Lock()
		up.method, up.path, up.auth, up.body = r.Method, r.URL.Path, r.Header.Get("Authorization"), req
		up.mu.Unlock()
		status, body := fn(r, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	up.srv = httptest.NewServer(mux)
	t.Cleanup(up.srv.Close)
	return up
}

func newLLMUpstreamContent(t *testing.T, content string) *llmUpstream {
	return newLLMUpstreamFunc(t, func(*http.Request, chatRequest) (int, string) {
		return http.StatusOK, chatOKContent(content)
	})
}

// chatOKContent OpenAI 兼容应答体（content 为模型输出原文，含转义由 json 包处理）。
func chatOKContent(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

// memberLLMService 假上游 + 真实 llmProvider 装配 Service；logs 非空时收集上游
// 原始错误留痕（脱敏断言用）。
func memberLLMService(t *testing.T, up *llmUpstream, logs *[]string) *Service {
	t.Helper()
	cfg := LLMConfig{BaseURL: up.srv.URL, APIKey: "sk-test-key"}
	if logs != nil {
		cfg.Logf = func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) }
	}
	p := NewLLMProvider(cfg)
	if p == nil {
		t.Fatal("llmProvider 应装配成功（APIKey 非空）")
	}
	return NewService(p, time.Second)
}

// TestLLMSuggestMembersPromptAndParse 真实链路 happy path：系统提示词=多选判定
// prompt（非单选 jevSystemPrompt）、user 消息=state JSON（goal + agent@node 候选）、
// 鉴权头携带 APIKey；应答 members/reason 正确解析且盖章来源。
func TestLLMSuggestMembersPromptAndParse(t *testing.T) {
	up := newLLMUpstreamContent(t, `{"members":["zcode-dev@mac-mini","diskcheck@win-box"],"reason":"编码与巡检互补"}`)
	s := memberLLMService(t, up, nil)
	res := s.SuggestMembers(Request{Goal: "盘点两台机器磁盘并汇总", Candidates: testCandidates()})
	if res.Degraded || len(res.Members) != 2 || res.Members[0] != "zcode-dev@mac-mini" {
		t.Fatalf("应解析出成员集合: %+v", res)
	}
	if res.Reason != "编码与巡检互补" || res.Provider != "llm-as-jev" {
		t.Fatalf("理由/来源不符: %+v", res)
	}

	if up.method != http.MethodPost || up.path != "/chat/completions" {
		t.Fatalf("应 POST /chat/completions, got %s %s", up.method, up.path)
	}
	if up.auth != "Bearer sk-test-key" {
		t.Fatalf("鉴权头不符: %q", up.auth)
	}
	if len(up.body.Messages) != 2 || up.body.Messages[0].Role != "system" ||
		up.body.Messages[0].Content != memberSystemPrompt {
		t.Fatalf("系统提示词应为多选判定 prompt: %.120s", up.body.Messages)
	}
	var state struct {
		Goal       string `json:"goal"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(up.body.Messages[1].Content), &state); err != nil {
		t.Fatalf("user 消息应为 state JSON: %v（%.120s）", err, up.body.Messages[1].Content)
	}
	if state.Goal != "盘点两台机器磁盘并汇总" {
		t.Fatalf("state goal 不符: %q", state.Goal)
	}
	ids := map[string]bool{}
	for _, c := range state.Candidates {
		ids[c.ID] = true
	}
	if !ids["zcode-dev@mac-mini"] || !ids["diskcheck@win-box"] {
		t.Fatalf("候选 id 应为 agent@node 形态: %v", ids)
	}
}

// TestLLMSuggestMembersEmptyMembersNoFit 空集合 = ErrNoFit 路径：非降级、无成员、
// 带人话 Note（与 provider 故障降级区分）。
func TestLLMSuggestMembersEmptyMembersNoFit(t *testing.T) {
	up := newLLMUpstreamContent(t, `{"members":[],"reason":"候选均不适合"}`)
	s := memberLLMService(t, up, nil)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if res.Degraded || res.Members != nil || res.Note == "" {
		t.Fatalf("空 members 应走 NoFit 转述: %+v", res)
	}
}

// TestLLMSuggestMembersFenceTolerance 围栏容错：模型应答带 markdown 围栏与前后
// 杂文时仍能截取 JSON 解析（parseMemberDecision 纪律）。
func TestLLMSuggestMembersFenceTolerance(t *testing.T) {
	up := newLLMUpstreamContent(t, "好的，以下是组队判定：\n```json\n{\"members\":[\"diskcheck@win-box\"],\"reason\":\"对口\"}\n```\n请查收。")
	s := memberLLMService(t, up, nil)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if res.Degraded || len(res.Members) != 1 || res.Members[0] != "diskcheck@win-box" {
		t.Fatalf("围栏应答应容错解析: %+v", res)
	}
}

// TestLLMSuggestMembersInvalidJSONSanitized 非法 JSON：对外固定人话类别降级，
// 原始应答不外泄（只进日志留痕）。
func TestLLMSuggestMembersInvalidJSONSanitized(t *testing.T) {
	raw := "我觉得选 zcode-dev 和 diskcheck 都可以，不需要 JSON。"
	up := newLLMUpstreamContent(t, raw)
	var logs []string
	s := memberLLMService(t, up, &logs)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded || res.Note == "" {
		t.Fatalf("非法 JSON 应降级带 Note: %+v", res)
	}
	if strings.Contains(res.Note, raw) {
		t.Fatalf("原始应答不得外泄进 Note: %s", res.Note)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "组队应答解析失败") || !strings.Contains(joined, raw) {
		t.Fatalf("原始应答应留痕日志: %v", logs)
	}
}

// TestLLMSuggestMembersUpstreamErrorSanitized 上游错误脱敏：HTTP 401 应答体回显
// 凭据——对外 Note 只带固定类别（含状态码），凭据仅入日志。
func TestLLMSuggestMembersUpstreamErrorSanitized(t *testing.T) {
	secret := "sk-secret-credential-xyz"
	up := newLLMUpstreamFunc(t, func(*http.Request, chatRequest) (int, string) {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "invalid api key: " + secret}})
		return http.StatusUnauthorized, string(b)
	})
	var logs []string
	s := memberLLMService(t, up, &logs)
	res := s.SuggestMembers(Request{Goal: "g", Candidates: testCandidates()})
	if !res.Degraded || res.Note == "" {
		t.Fatalf("上游错误应降级带 Note: %+v", res)
	}
	if !strings.Contains(res.Note, "HTTP 401") {
		t.Fatalf("Note 应带固定类别（HTTP 401）: %s", res.Note)
	}
	if strings.Contains(res.Note, secret) {
		t.Fatalf("凭据不得外泄进 Note: %s", res.Note)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, secret) {
		t.Fatalf("原始错误体应留痕日志（仅日志）: %v", logs)
	}
}
