package main

import (
	"strings"
	"testing"
)

// TestRenderTaskBlockedHints 依赖门控展示字段渲染（P4.3/SPEC 六.六规则 7）：
// status/wait 返回体透传 depends_on/blocked_reason/superseded_by 并附人话提示；
// 未声明依赖的任务渲染不变（向后兼容）。
func TestRenderTaskBlockedHints(t *testing.T) {
	// blocked_by：等待中
	text, done := renderTaskOK("t-b1", []byte(`{"status":"submitted","target_agent":"echo",
		"target_node":"n1","depends_on":["up-1"],"blocked_reason":"blocked_by:up-1"}`))
	if done {
		t.Fatal("blocked 任务非终态，wait 不得结束")
	}
	for _, want := range []string{"依赖：up-1", "上游 up-1 尚未完成", "暂不派发"} {
		if !strings.Contains(text, want) {
			t.Errorf("blocked_by 渲染缺 %q, got %s", want, text)
		}
	}

	// upstream_failed：失败阻塞 + superseded 审计
	text, done = renderTaskOK("t-b2", []byte(`{"status":"submitted","target_agent":"echo",
		"target_node":"n1","depends_on":["up-2"],"blocked_reason":"upstream_failed:up-2",
		"superseded_by":"t-new"}`))
	if done {
		t.Fatal("upstream_failed 任务非终态，wait 不得结束")
	}
	for _, want := range []string{"上游 up-2 已失败", "不会自动执行", "t-new 已完成", "不自动放行"} {
		if !strings.Contains(text, want) {
			t.Errorf("upstream_failed/superseded 渲染缺 %q, got %s", want, text)
		}
	}

	// 未声明依赖：不渲染依赖段（既有任务输出零变化）
	text, _ = renderTaskOK("t-p", []byte(`{"status":"completed","target_agent":"echo",
		"target_node":"n1","result":{"status":"completed","exit_code":0}}`))
	if strings.Contains(text, "依赖") || strings.Contains(text, "阻塞") {
		t.Errorf("无依赖任务不得渲染依赖段, got %s", text)
	}
}
