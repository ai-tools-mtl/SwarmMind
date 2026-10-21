package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

// TestCardAndProfileJSONKeys AgentCard/ExecProfile 编码键名金样（json tag 严格按 SPEC 4.9）。
func TestCardAndProfileJSONKeys(t *testing.T) {
	card, _ := json.Marshal(AgentCard{})
	gotCard := map[string]json.RawMessage{}
	if err := json.Unmarshal(card, &gotCard); err != nil {
		t.Fatalf("card unmarshal: %v", err)
	}
	for _, k := range []string{"name", "kind", "input_mode", "output_mode", "interactive", "permission_mode", "protocol_ver", "exposed"} {
		if _, ok := gotCard[k]; !ok {
			t.Errorf("AgentCard 缺键 %q（编码结果：%s）", k, card)
		}
	}
	if _, ok := gotCard["description"]; ok {
		t.Errorf("AgentCard 零值下 description 不应出现（omitempty）：%s", card)
	}

	profile, _ := json.Marshal(ExecProfile{})
	gotProfile := map[string]json.RawMessage{}
	if err := json.Unmarshal(profile, &gotProfile); err != nil {
		t.Fatalf("profile unmarshal: %v", err)
	}
	for _, k := range []string{"name", "kind", "command", "workdir", "agent_args", "timeout_sec", "allow_args", "allow_env", "config_home", "permission_mode", "exposed"} {
		if _, ok := gotProfile[k]; !ok {
			t.Errorf("ExecProfile 缺键 %q（编码结果：%s）", k, profile)
		}
	}
	if _, ok := gotProfile["description"]; ok {
		t.Errorf("ExecProfile 零值下 description 不应出现（omitempty）：%s", profile)
	}
}

// TestTaskJSONKeys Task/TaskSubmitRequest 编码键名金样（json tag 严格按 SPEC 4.9）。
func TestTaskJSONKeys(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	task, _ := json.Marshal(Task{ID: "t-1", TraceID: "tr", TargetNode: "n", TargetAgent: "a", Status: StatusSubmitted, CreatedAt: now, UpdatedAt: now, ExpireAt: now})
	got := map[string]json.RawMessage{}
	if err := json.Unmarshal(task, &got); err != nil {
		t.Fatalf("task unmarshal: %v", err)
	}
	for _, k := range []string{"id", "trace_id", "target_node", "target_agent", "input", "submitted_by", "status", "created_at", "updated_at", "expire_at"} {
		if _, ok := got[k]; !ok {
			t.Errorf("Task 缺键 %q（编码结果：%s）", k, task)
		}
	}
	for _, k := range []string{"idem", "result"} {
		if _, ok := got[k]; ok {
			t.Errorf("Task 零值下 %q 不应出现（omitempty）：%s", k, task)
		}
	}

	req, _ := json.Marshal(TaskSubmitRequest{TargetNode: "n", TargetAgent: "a", SubmittedBy: "hana"})
	gotReq := map[string]json.RawMessage{}
	if err := json.Unmarshal(req, &gotReq); err != nil {
		t.Fatalf("req unmarshal: %v", err)
	}
	for _, k := range []string{"target_node", "target_agent", "input", "submitted_by"} {
		if _, ok := gotReq[k]; !ok {
			t.Errorf("TaskSubmitRequest 缺键 %q（编码结果：%s）", k, req)
		}
	}
	if _, ok := gotReq["idem_key"]; ok {
		t.Errorf("TaskSubmitRequest 零值下 idem_key 不应出现（omitempty）：%s", req)
	}
}

// TestResultJSONKeys TaskResult/Artifact 编码键名金样（json tag 严格按 SPEC 4.9）。
func TestResultJSONKeys(t *testing.T) {
	res, _ := json.Marshal(TaskResult{})
	got := map[string]json.RawMessage{}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	for _, k := range []string{"status", "exit_code", "final_answer", "stdout_tail", "stderr_tail", "artifacts"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TaskResult 缺键 %q（编码结果：%s）", k, res)
		}
	}
	// 注：SPEC 4.9 的 tag 逐字照抄，started_at/finished_at 带 omitempty；但 Go
	// encoding/json 的 omitempty 对 struct（time.Time）不生效，零值会序列化为零时刻
	// 字符串——这是按 SPEC 原样实现的行为，实现不做偏离。
	for _, k := range []string{"error"} {
		if _, ok := got[k]; ok {
			t.Errorf("TaskResult 零值下 %q 不应出现（omitempty）：%s", k, res)
		}
	}
	for _, k := range []string{"started_at", "finished_at"} {
		if string(got[k]) != `"0001-01-01T00:00:00Z"` {
			t.Errorf("TaskResult 零值 %q 应序列化为零时刻字符串：%s", k, res)
		}
	}

	art, _ := json.Marshal(Artifact{Path: "outputs/x/a.txt", Sha256: "ab", Size: 3})
	gotArt := map[string]json.RawMessage{}
	if err := json.Unmarshal(art, &gotArt); err != nil {
		t.Fatalf("artifact unmarshal: %v", err)
	}
	for _, k := range []string{"path", "sha256", "size"} {
		if _, ok := gotArt[k]; !ok {
			t.Errorf("Artifact 缺键 %q（编码结果：%s）", k, art)
		}
	}
}

// TestTaskCloneIsolation Clone 必须深拷贝容器，修改副本不得影响原值（先写后见的前提）。
func TestTaskCloneIsolation(t *testing.T) {
	src := Task{
		ID:    "t-1",
		Input: TaskInput{Args: []string{"a"}, Env: map[string]string{"K": "v"}},
		Result: &TaskResult{
			Artifacts: []Artifact{{Path: "outputs/x/a.txt", Sha256: "ab", Size: 3}},
		},
	}
	c := src.Clone()
	c.Input.Args[0] = "x"
	c.Input.Env["K"] = "x"
	c.Result.Artifacts[0].Path = "mutated"

	if src.Input.Args[0] != "a" {
		t.Errorf("Args 被副本污染：%q", src.Input.Args[0])
	}
	if src.Input.Env["K"] != "v" {
		t.Errorf("Env 被副本污染：%q", src.Input.Env["K"])
	}
	if src.Result.Artifacts[0].Path != "outputs/x/a.txt" {
		t.Errorf("Artifacts 被副本污染：%q", src.Result.Artifacts[0].Path)
	}
}

// TestNewIDs ID 生成器格式与唯一性。
func TestNewIDs(t *testing.T) {
	id1, id2 := NewTaskID(), NewTaskID()
	if len(id1) != 2+32 || id1[:2] != "t-" {
		t.Errorf("TaskID 格式不符：%q", id1)
	}
	if id1 == id2 {
		t.Error("TaskID 应唯一")
	}
	m1, m2 := NewMessageID(), NewMessageID()
	if len(m1) != 32 || m1 == m2 {
		t.Errorf("MessageID 应为 32 位 hex 且唯一：%q", m1)
	}
	tr := NewTraceID()
	if len(tr) != 32 {
		t.Errorf("TraceID 应为 32 位 hex：%q", tr)
	}
}
