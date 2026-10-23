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
// 同步规则）：depends_on 数组参数原样进 POST /api/v1/tasks 请求体（TrimSpace 归一；
// 未传不下发字段）。非法输入（空白项等）不在此列——显式拒绝见
// TestHubSubmitDependsOnInvalid，与 REST invalid_dependency 同语义。
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

	// 带依赖：透传（含空白归一）
	if _, err := toolSubmit(map[string]any{
		"node_id": "n1", "agent": "echo", "stdin": "x",
		"depends_on": []any{"up-1", " up-2 "},
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

// TestHubSubmitDependsOnInvalid 非法 depends_on 显式拒绝（P2 修复，与 REST
// invalid_dependency 语义对齐）：非数组、非字符串元素、空白项均返回工具错误
// 提示修正，不静默降级——修复前非数组/空白项被静默剔除成空列表（任务变无
// 条件立即执行）、非字符串元素被字符串化透传。拒绝发生在请求 hub 之前。
func TestHubSubmitDependsOnInvalid(t *testing.T) {
	var (
		seen bool
		got  map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tasks" {
			seen = true
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"t-x"}`))
	}))
	defer srv.Close()

	oldURL, oldTok := hubURL, hubToken
	hubURL, hubToken = srv.URL, "tok"
	t.Cleanup(func() { hubURL, hubToken = oldURL, oldTok })

	bad := []struct {
		name string
		raw  any
	}{
		{"非数组", "up-1"},
		{"对象", map[string]any{"up": "1"}},
		{"非字符串元素（数字）", []any{"up-1", 42}},
		{"非字符串元素（布尔）", []any{true}},
		{"空白项", []any{"up-1", "   "}},
		{"全空白项", []any{"  "}},
	}
	for _, tc := range bad {
		seen = false
		out, err := toolSubmit(map[string]any{
			"node_id": "n1", "agent": "echo", "stdin": "x",
			"depends_on": tc.raw,
		})
		if err == nil {
			t.Errorf("%s: 应报错提示修正, got %q", tc.name, out)
			continue
		}
		if !strings.Contains(err.Error(), "depends_on") {
			t.Errorf("%s: 错误应指向 depends_on 参数, got %v", tc.name, err)
		}
		if seen {
			t.Errorf("%s: 拒绝应发生在请求 hub 之前", tc.name)
		}
	}

	// 空数组＝不声明依赖（合法）：照常提交且不下发字段
	seen = false
	got = nil
	if _, err := toolSubmit(map[string]any{
		"node_id": "n1", "agent": "echo", "stdin": "x",
		"depends_on": []any{},
	}); err != nil {
		t.Fatalf("空数组应视为不声明依赖: %v", err)
	}
	if !seen {
		t.Fatal("空数组应照常提交")
	}
	if _, exists := got["depends_on"]; exists {
		t.Error("空数组不得下发 depends_on 字段")
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
