package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHubSubmitDependsOnPassthrough swarmmind_submit 依赖透传（P4.3/SPEC 六.六
// 同步规则）：depends_on 数组参数原样进 POST /api/v1/tasks 请求体（TrimSpace 归一、
// 空项剔除）；未传不下发字段。
func TestHubSubmitDependsOnPassthrough(t *testing.T) {
	var got map[string]any
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tasks" {
			seen = true
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"t-x","trace_id":"tr-x"}`))
	}))
	defer srv.Close()

	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = srv.URL, "tok"
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	// 带依赖：透传（含空白归一；空项剔除）
	if _, err := toolSubmit(map[string]any{
		"node_id": "n1", "agent": "echo", "stdin": "x",
		"depends_on": []any{"up-1", " up-2 ", ""},
	}); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if !seen {
		t.Fatal("请求未达 hub")
	}
	deps, ok := got["depends_on"].([]any)
	if !ok || len(deps) != 2 {
		t.Fatalf("depends_on 应透传 2 项, got %v", got["depends_on"])
	}
	if deps[0] != "up-1" || deps[1] != "up-2" {
		t.Errorf("depends_on 应归一空白, got %v", deps)
	}

	// 未传：不下发字段
	got = nil
	seen = false
	if _, err := toolSubmit(map[string]any{"node_id": "n1", "agent": "echo"}); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if _, exists := got["depends_on"]; exists {
		t.Error("未声明依赖不得下发 depends_on 字段")
	}
}

// TestHubSubmitToolSchemaDependsOn hub 模式 submit 工具声明 depends_on 参数
// （工具面与 REST 能力同步，SPEC 六.六）。
func TestHubSubmitToolSchemaDependsOn(t *testing.T) {
	for _, td := range (hubBackend{}).tools() {
		if td.Name != "swarmmind_submit" {
			continue
		}
		props, _ := td.InputSchema["properties"].(map[string]any)
		dep, ok := props["depends_on"].(map[string]any)
		if !ok {
			t.Fatal("swarmmind_submit 工具缺 depends_on 参数声明")
		}
		if !strings.Contains(fmt.Sprint(dep), "array") {
			t.Errorf("depends_on 应为 array 型, got %v", dep)
		}
		return
	}
	t.Fatal("未找到 swarmmind_submit 工具")
}
