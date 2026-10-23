package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestSubmitDependsOnRoundtrip REST 依赖透传（P4.3/SPEC 4.12）：提交请求体
// depends_on 透传 hub 校验链（未知 idem → 400 invalid_dependency）；合法声明
// 落库后查询返回体携带 depends_on / blocked_reason。
func TestSubmitDependsOnRoundtrip(t *testing.T) {
	f := newFixture(t)
	f.core.RegisterNode("nodeB", []protocol.AgentCard{echoCard(true)})
	hc := f.client(true)

	// 未知 idem：400 invalid_dependency（错误码经 writeSubmitErr 映射）
	code, out := f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","input":{"stdin":"x"},"submitted_by":"tester","depends_on":["ghost-x"]}`)
	if code != http.StatusBadRequest || !strings.Contains(string(out), "invalid_dependency") {
		t.Fatalf("未知依赖应 400 invalid_dependency, got %d: %s", code, out)
	}

	// 上游（带 idem）
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","input":{"stdin":"up"},"submitted_by":"tester","idem_key":"rest-up-1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("上游提交应 202, got %d: %s", code, out)
	}
	var up struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(out, &up)

	// 下游声明依赖：202 落库
	code, out = f.do(hc, http.MethodPost, "/api/v1/tasks",
		`{"target_node":"nodeB","target_agent":"echo","input":{"stdin":"down"},"submitted_by":"tester","depends_on":["rest-up-1"]}`)
	if code != http.StatusAccepted {
		t.Fatalf("下游提交应 202, got %d: %s", code, out)
	}
	var down struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(out, &down)

	// 查询返回体携带 depends_on / blocked_reason（任务未终态 → blocked）
	code, out = f.do(hc, http.MethodGet, "/api/v1/tasks/"+down.TaskID, "")
	if code != http.StatusOK {
		t.Fatalf("查询应 200, got %d: %s", code, out)
	}
	var task struct {
		DependsOn     []string `json:"depends_on"`
		BlockedReason string   `json:"blocked_reason"`
		SupersededBy  string   `json:"superseded_by"`
	}
	if err := json.Unmarshal(out, &task); err != nil {
		t.Fatalf("查询体解析失败: %v", err)
	}
	if len(task.DependsOn) != 1 || task.DependsOn[0] != "rest-up-1" {
		t.Errorf("返回体应携带 depends_on, got %v", task.DependsOn)
	}
	if task.BlockedReason != "blocked_by:rest-up-1" {
		t.Errorf("返回体应携带 blocked_reason, got %q", task.BlockedReason)
	}
	if task.SupersededBy != "" {
		t.Errorf("未取代任务 superseded_by 应空, got %q", task.SupersededBy)
	}
}
