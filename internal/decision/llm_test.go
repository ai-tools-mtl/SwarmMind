package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGLM OpenAI 兼容 chat/completions 假端点：记录请求并按 content 脚本应答。
type fakeGLM struct {
	t        *testing.T
	gotPath  string
	gotAuth  string
	gotBody  chatRequest
	content  string // choices[0].message.content
	status   int    // 0 = 200
}

func (f *fakeGLM) handler(w http.ResponseWriter, r *http.Request) {
	f.gotPath = r.URL.Path
	f.gotAuth = r.Header.Get("Authorization")
	if err := json.NewDecoder(r.Body).Decode(&f.gotBody); err != nil {
		f.t.Fatalf("请求体解析失败: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	w.WriteHeader(st)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"content": f.content}}},
	})
}

func decisionJSON(choice string, conf float64, probs string) string {
	return `{"choice":"` + choice + `","confidence":` + trimFloat(conf) + `,"probabilities":` + probs + `,"reason":"测试理由"}`
}

func trimFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestLLMRequestShapeAndParse(t *testing.T) {
	fake := &fakeGLM{t: t, content: "```json\n" + decisionJSON("diskcheck@win-box", 0.9, `{"diskcheck@win-box":0.9,"zcode-dev@mac-mini":0.1}`) + "\n```"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "test-key"})
	svc := NewService(p, 2*time.Second)
	res := svc.Suggest(Request{Goal: "检查磁盘占用并输出对比报告", Candidates: testCandidates()})
	if res.Suggestion == nil {
		t.Fatalf("应有建议: %+v", res)
	}
	if fake.gotPath != "/chat/completions" {
		t.Fatalf("路径不符: %s", fake.gotPath)
	}
	if fake.gotAuth != "Bearer test-key" {
		t.Fatalf("鉴权头不符: %q", fake.gotAuth)
	}
	if fake.gotBody.Model != defaultLLMModel || len(fake.gotBody.Messages) != 2 ||
		fake.gotBody.Messages[0].Role != "system" || fake.gotBody.Messages[1].Role != "user" {
		t.Fatalf("请求形态不符: %+v", fake.gotBody)
	}
	// GLM 混合推理必须显式禁用思考（codex 审 D1 ⑤）：思考模式实测把 token 烧在
	// reasoning_content 上致 content 空应答/JSON 截断（E 组实测根因），锁死防回归。
	if fake.gotBody.Thinking == nil || fake.gotBody.Thinking.Type != "disabled" {
		t.Fatalf("thinking 必须 type=disabled: %+v", fake.gotBody.Thinking)
	}
	// state 压缩：goal 与候选 id 齐全，描述截断生效。
	state := fake.gotBody.Messages[1].Content
	if !strings.Contains(state, "检查磁盘占用") || !strings.Contains(state, "diskcheck@win-box") || !strings.Contains(state, "zcode-dev@mac-mini") {
		t.Fatalf("state 缺字段: %s", state)
	}
	if res.Suggestion.TargetAgent != "diskcheck" || res.Suggestion.TargetNode != "win-box" || res.Suggestion.Confidence != 0.9 {
		t.Fatalf("解析结果不符: %+v", res.Suggestion)
	}
	if res.Suggestion.Provider != "llm-as-jev" || res.Suggestion.Model != defaultLLMModel {
		t.Fatalf("来源标签不符: %+v", res.Suggestion)
	}
}

func TestLLMHTTPErrorAndAuth(t *testing.T) {
	fake := &fakeGLM{t: t, status: http.StatusUnauthorized, content: "no"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "bad-key"})
	res := NewService(p, time.Second).Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion != nil || !res.Degraded || res.Note == "" {
		t.Fatalf("HTTP 非 200 应降级: %+v", res)
	}
}

func TestLLMNoKeyDisables(t *testing.T) {
	if NewLLMProvider(LLMConfig{}) != nil {
		t.Fatalf("无 key 应返回 nil（未启用）")
	}
}

func TestParseDecisionVariants(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"纯净 JSON", `{"choice":"a@n","confidence":0.8,"probabilities":{"a@n":0.8},"reason":"r"}`, "a@n", false},
		{"围栏", "```json\n{\"choice\":\"a@n\",\"confidence\":0.8}\n```", "a@n", false},
		{"前置杂文", "好的，判定如下：{\"choice\": \"a@n\", \"confidence\": 0.8} 以上。", "a@n", false},
		{"choice 带空白", `{"choice":" a@n ","confidence":0.5}`, "a@n", false},
		{"none", `{"choice":"none","confidence":0}`, "none", false},
		{"空 choice", `{"confidence":0.5}`, "", false},
		{"纯文本", "我觉得选 a@n 比较好", "", true},
		{"截断 JSON", `{"choice":"a@n","confidence":`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := parseDecision(tc.content)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应报错: %+v", dec)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if dec.Choice != tc.want {
				t.Fatalf("choice = %q, want %q", dec.Choice, tc.want)
			}
		})
	}
}

func TestLLMChoiceResolution(t *testing.T) {
	fake := &fakeGLM{t: t, content: decisionJSON(" diskcheck@win-box ", 0.6, `{"diskcheck@win-box":0.6}`)}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "k"})
	res := NewService(p, time.Second).Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion == nil || res.Suggestion.TargetAgent != "diskcheck" {
		t.Fatalf("宽松去空白应命中: %+v", res)
	}
}

func TestLLMChoiceOutSideCandidatesDegrades(t *testing.T) {
	fake := &fakeGLM{t: t, content: decisionJSON("root@ghost", 1, `{}`)}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "k"})
	res := NewService(p, time.Second).Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion != nil || !res.Degraded {
		t.Fatalf("候选外 choice 应被 Service 弃用: %+v", res)
	}
}

func TestLLMNoFit(t *testing.T) {
	fake := &fakeGLM{t: t, content: decisionJSON("none", 0, `{}`)}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "k"})
	res := NewService(p, time.Second).Suggest(Request{Goal: "x", Candidates: testCandidates()})
	if res.Suggestion != nil || res.Degraded || res.Note == "" {
		t.Fatalf("none 应为正常无建议: %+v", res)
	}
}

func TestBuildStateCompression(t *testing.T) {
	longDesc := strings.Repeat("很", 200)
	req := Request{Goal: "目标", Candidates: []Candidate{
		{NodeID: "n", Agent: "a", Description: longDesc, Inflight: 3, PermissionMode: "沙盒"},
	}}
	state := buildState(req)
	if strings.Count(state, "很") > stateDescLimit+1 { // +1 容 …
		t.Fatalf("描述应被压缩: %d", strings.Count(state, "很"))
	}
	if !strings.Contains(state, `"inflight":3`) || !strings.Contains(state, `"a@n"`) {
		t.Fatalf("state 缺字段: %s", state)
	}
	// 合法 JSON（可被模型稳定读取）。
	var v map[string]any
	if err := json.Unmarshal([]byte(state), &v); err != nil {
		t.Fatalf("state 应为合法 JSON: %v", err)
	}
}

func TestNewLLMProviderDefaults(t *testing.T) {
	p := NewLLMProvider(LLMConfig{APIKey: "k"})
	if p.cfg.BaseURL != defaultLLMBaseURL || p.cfg.Model != defaultLLMModel || p.cfg.Timeout != defaultLLMTimeout {
		t.Fatalf("缺省值不符: %+v", p.cfg)
	}
	if p.Name() != "llm-as-jev" || p.Model() != defaultLLMModel {
		t.Fatalf("名称不符")
	}
}

func TestLLMRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 挂住应答直到测试结束
	}))
	defer srv.Close()
	defer close(release)
	p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: "k"})
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Suggest(ctx, Request{Goal: "x", Candidates: testCandidates()})
	if err == nil {
		t.Fatalf("ctx 取消应报错")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("取消应即时生效, took %v", elapsed)
	}
}

func TestProbabilitiesJSONNumbers(t *testing.T) {
	// GLM 应答概率可能带全角/科学计数等边缘形态——只承诺标准 JSON 数值域内行为。
	dec, err := parseDecision(`{"choice":"a@n","confidence":1,"probabilities":{"a@n":1e0,"b@m":0}}`)
	if err != nil {
		t.Fatalf("科学计数应可解析: %v", err)
	}
	if math.Abs(dec.Probabilities["a@n"]-1) > 1e-9 {
		t.Fatalf("概率解析不符: %v", dec.Probabilities)
	}
}

// logCapture Logf 注入捕获（防泄漏断言用；并发安全由 Service.call 的
// happens-before 通道语义保证——断言读发生在 Suggest 返回之后）。
type logCapture struct {
	buf strings.Builder
}

func (c *logCapture) logf(format string, args ...any) { fmt.Fprintf(&c.buf, format, args...) }

func (c *logCapture) has(s string) bool { return strings.Contains(c.buf.String(), s) }

// TestLLMErrorNoLeak 防泄漏断言（codex 审 D1 ④）：上游回显凭据/请求内容时，
// 原文只进注入的日志出口，Service note（→ REST/MCP/UI）只允许固定人话类别。
func TestLLMErrorNoLeak(t *testing.T) {
	const secret = "sk-LEAKKEY1234567890"
	const goalMark = "绝密目标C盘清理"
	// state 内含 goalMark（上游可能原样回显请求内容），各失败路径不应把它带出。
	newReq := func() Request {
		return Request{Goal: goalMark, Candidates: testCandidates()}
	}
	assertClean := func(t *testing.T, res Result, lc *logCapture) {
		t.Helper()
		if res.Suggestion != nil || !res.Degraded || res.Note == "" {
			t.Fatalf("应降级: %+v", res)
		}
		if strings.Contains(res.Note, secret) || strings.Contains(res.Note, goalMark) {
			t.Fatalf("note 泄漏上游原文: %s", res.Note)
		}
		if !lc.has(secret) {
			t.Fatalf("原文应留痕日志（仅日志）: %s", lc.buf.String())
		}
	}
	t.Run("HTTP 非 200 回显凭据", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid api key: ` + secret + `" goal=` + goalMark + `}`))
		}))
		defer srv.Close()
		lc := &logCapture{}
		p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: secret, Logf: lc.logf})
		res := NewService(p, time.Second).Suggest(newReq())
		assertClean(t, res, lc)
		if !strings.Contains(res.Note, "暂不可用") {
			t.Fatalf("note 应为固定人话类别: %s", res.Note)
		}
	})
	t.Run("上游 error 字段回显", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded for ` + secret + ` / ` + goalMark + `"}}`))
		}))
		defer srv.Close()
		lc := &logCapture{}
		p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: secret, Logf: lc.logf})
		res := NewService(p, time.Second).Suggest(newReq())
		assertClean(t, res, lc)
	})
	t.Run("应答非判定 JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"抱歉，我无法处理 ` + secret + ` ` + goalMark + `"}}]}`))
		}))
		defer srv.Close()
		lc := &logCapture{}
		p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: secret, Logf: lc.logf})
		res := NewService(p, time.Second).Suggest(newReq())
		assertClean(t, res, lc)
	})
	t.Run("choice 越权（模型输出原文）", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"choice\":\"` + secret + `@evil\",\"confidence\":0.9}"}}]}`))
		}))
		defer srv.Close()
		lc := &logCapture{}
		p := NewLLMProvider(LLMConfig{BaseURL: srv.URL, APIKey: secret, Logf: lc.logf})
		res := NewService(p, time.Second).Suggest(newReq())
		assertClean(t, res, lc)
	})
}

// TestBuildStateCandidateCap 截断说明标记（codex 审 D1 ⑤）：候选超上限时 state
// 内带 candidates_note（模型可感知），且只列前 stateCandidateCap 个。
func TestBuildStateCandidateCap(t *testing.T) {
	cands := make([]Candidate, 0, stateCandidateCap+5)
	for i := 0; i < stateCandidateCap+5; i++ {
		cands = append(cands, Candidate{NodeID: fmt.Sprintf("n%02d", i), Agent: "a"})
	}
	state := buildState(Request{Goal: "x", Candidates: cands})
	if !strings.Contains(state, "candidates_note") || !strings.Contains(state, "超过上限") {
		t.Fatalf("截断应带说明标记: %s", state)
	}
	if strings.Contains(state, "a@n35") { // 超出上限的候选不得入列（n35 是第 36 个）
		t.Fatalf("应只列前 %d 个: %s", stateCandidateCap, state)
	}
	var v struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
		Note string `json:"candidates_note"`
	}
	if err := json.Unmarshal([]byte(state), &v); err != nil {
		t.Fatalf("state 应为合法 JSON: %v", err)
	}
	if len(v.Candidates) != stateCandidateCap || v.Note == "" {
		t.Fatalf("截断后候选数/说明不符: %d 个, note=%q", len(v.Candidates), v.Note)
	}
}
