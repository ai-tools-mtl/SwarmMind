package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// suggestProvider 可编程 decision.Provider（REST 层测试装配）。
type suggestProvider struct {
	sug *decision.Suggestion
	err error
}

func (p *suggestProvider) Name() string  { return "mock-decider" }
func (p *suggestProvider) Model() string { return "mock-model" }
func (p *suggestProvider) Suggest(_ context.Context, req decision.Request) (*decision.Suggestion, error) {
	return p.sug, p.err
}

// providerRecorder 函数形 provider（捕获入参断言候选组装）。
type providerRecorder func(req decision.Request) (*decision.Suggestion, error)

func (p providerRecorder) Name() string  { return "recorder" }
func (p providerRecorder) Model() string { return "rec-model" }
func (p providerRecorder) Suggest(_ context.Context, req decision.Request) (*decision.Suggestion, error) {
	return p(req)
}

// newSuggestFixture 建议端点基座：newFixture 同构 + Decision 接线（目录种两台
// 在线 node：mac-mini/echo、win-box/diskcheck）。
func newSuggestFixture(t *testing.T, p decision.Provider) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	if err := c.RegisterNode("mac-mini", []protocol.AgentCard{echoCard(true)}); err != nil {
		t.Fatal(err)
	}
	c.SetOnline("mac-mini", true)
	disk := echoCard(true)
	disk.Name = "diskcheck"
	disk.Description = "磁盘巡检脚本：输出各盘剩余空间"
	disk.PermissionMode = "no-shell"
	if err := c.RegisterNode("win-box", []protocol.AgentCard{disk}); err != nil {
		t.Fatal(err)
	}
	c.SetOnline("win-box", true)

	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token",
		Decision: decision.NewService(p, time.Second)}).Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// newSuggestClient 登录态客户端（本文件自建：与 f.client 同构，因需登录态且
// fixture 现有 client 不共享 jar；skipLogin=true 时裸客户端，401 路径用）。
func (f *fixture) suggestClient(skipLogin bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	if skipLogin {
		return hc
	}
	resp, err := hc.Post(f.srv.URL+"/api/v1/login", "application/json",
		strings.NewReader(`{"token":"test-admin-token"}`))
	if err != nil {
		f.t.Fatalf("登录失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("登录应 200, got %d", resp.StatusCode)
	}
	return hc
}

func TestDispatchSuggestAuthRequired(t *testing.T) {
	f := newSuggestFixture(t, &suggestProvider{})
	hc := f.suggestClient(true)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"检查磁盘"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d: %s", code, out)
	}
}

func TestDispatchSuggestValidation(t *testing.T) {
	f := newSuggestFixture(t, &suggestProvider{})
	hc := f.suggestClient(false)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{}`)
	if code != http.StatusBadRequest || !strings.Contains(string(out), "invalid_request") {
		t.Fatalf("空 goal 应 400, got %d: %s", code, out)
	}
	code, _ = f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"   "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("纯空白 goal 应 400, got %d", code)
	}
	code, _ = f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `not-json`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 body 应 400, got %d", code)
	}
}

func TestDispatchSuggestHappyPath(t *testing.T) {
	p := &suggestProvider{sug: &decision.Suggestion{TargetNode: "win-box", TargetAgent: "diskcheck",
		Confidence: 0.87, Probabilities: map[string]float64{"diskcheck@win-box": 0.87, "echo@mac-mini": 0.13},
		Reason: "巡检脚本对口"}}
	f := newSuggestFixture(t, p)
	hc := f.suggestClient(false)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"检查 C 盘剩余空间并给报告"}`)
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d: %s", code, out)
	}
	var resp struct {
		Suggestion *struct {
			TargetAgent string `json:"target_agent"`
			TargetNode  string `json:"target_node"`
		} `json:"suggestion"`
		Confidence float64            `json:"confidence"`
		Probs      map[string]float64 `json:"probabilities"`
		Provider   string             `json:"provider"`
		Degraded   bool               `json:"degraded"`
		Reason     string             `json:"reason"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("应答解析失败: %v", err)
	}
	if resp.Suggestion == nil || resp.Suggestion.TargetAgent != "diskcheck" || resp.Suggestion.TargetNode != "win-box" {
		t.Fatalf("建议不符: %+v", resp)
	}
	if resp.Confidence != 0.87 || resp.Degraded || resp.Provider != "mock-decider" || resp.Reason == "" {
		t.Fatalf("元数据不符: %+v", resp)
	}
	if len(resp.Probs) != 2 {
		t.Fatalf("概率分布不符: %v", resp.Probs)
	}
	// step_desc 别名同参受理。
	code, out = f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"step_desc":"清理临时文件"}`)
	if code != http.StatusOK || !strings.Contains(string(out), `"diskcheck"`) {
		t.Fatalf("step_desc 应受理, got %d: %s", code, out)
	}
}

func TestDispatchSuggestDegraded(t *testing.T) {
	p := &suggestProvider{err: errors.New("模型不可用")}
	f := newSuggestFixture(t, p)
	hc := f.suggestClient(false)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"x"}`)
	if code != http.StatusOK {
		t.Fatalf("降级必须 200 不阻塞派单, got %d: %s", code, out)
	}
	var resp struct {
		Suggestion *struct{} `json:"suggestion"`
		Degraded   bool      `json:"degraded"`
		Note       string    `json:"note"`
		Provider   string    `json:"provider"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Suggestion != nil || !resp.Degraded || resp.Note == "" || resp.Provider != "mock-decider" {
		t.Fatalf("降级应答形态不符: %+v", resp)
	}
}

func TestDispatchSuggestNoCandidates(t *testing.T) {
	// 空 store：无任何 node → 无候选（不调 provider 即有 note）。
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token",
		Decision: decision.NewService(&suggestProvider{}, time.Second)}).Handler())
	t.Cleanup(srv.Close)
	f := &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
	hc := f.suggestClient(false)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"x"}`)
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d: %s", code, out)
	}
	if !strings.Contains(string(out), `"suggestion":null`) || !strings.Contains(string(out), "在线能力候选") {
		t.Fatalf("无候选应 suggestion:null + 人话 note: %s", out)
	}
}

func TestDispatchSuggestOfflineAndHiddenExcluded(t *testing.T) {
	// 离线 node 与 exposed=false 能力不得进入候选；候选稳定序断言。
	var gotReq decision.Request
	p := providerRecorder(func(req decision.Request) (*decision.Suggestion, error) {
		gotReq = req
		return &decision.Suggestion{TargetNode: "mac-mini", TargetAgent: "echo", Confidence: 0.8}, nil
	})
	f := newSuggestFixture(t, p)
	// nodeC 注册但不在线；nodeE 在线但能力未暴露。
	if err := f.core.RegisterNode("nodeC", []protocol.AgentCard{echoCard(true)}); err != nil {
		t.Fatal(err)
	}
	if err := f.core.RegisterNode("nodeE", []protocol.AgentCard{echoCard(false)}); err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("nodeE", true)
	hc := f.suggestClient(false)
	code, _ := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"x"}`)
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d", code)
	}
	if len(gotReq.Candidates) != 2 {
		t.Fatalf("候选应只含两台在线 node 的 exposed 能力: %+v", gotReq.Candidates)
	}
	if gotReq.Candidates[0].Key() != "echo@mac-mini" || gotReq.Candidates[1].Key() != "diskcheck@win-box" {
		t.Fatalf("候选序应稳定: %+v", gotReq.Candidates)
	}
}

func TestDispatchSuggestDisabled(t *testing.T) {
	// Decision 为 nil（未接线）：端点常在，200 + 未启用 note（前端无需探活）。
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	srv := httptest.NewServer(New(Config{Core: c, AdminToken: "test-admin-token"}).Handler())
	t.Cleanup(srv.Close)
	f := &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
	hc := f.suggestClient(false)
	code, out := f.do(hc, http.MethodPost, "/api/v1/dispatch-suggest", `{"goal":"x"}`)
	if code != http.StatusOK || !strings.Contains(string(out), `"suggestion":null`) || !strings.Contains(string(out), "未启用") {
		t.Fatalf("未启用应 200+suggestion:null+note, got %d: %s", code, out)
	}
}
