package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorkOldSnapshotCompatible W1 验收 2 的 store 层归一单元（SQLite 迁移后口径
// 改为「旧 JSON 导入兼容」）：不含任何 Work 字段的旧版快照经 JSON→SQLite 导入可
// 加载（normalize 迁移），既有实体逐字段完整、Work map 归一非 nil 空集、导入后
// 可正常写入；旧 json 备份改名。全链口径（旧快照→真实 CreateWork→重启恢复）
// 由 core 包 TestWorkReloadRoundTrip 覆盖（store 不可反向依赖 core）。
func TestWorkOldSnapshotCompatible(t *testing.T) {
	dir := t.TempDir()
	old := `{
  "nodes": {"n1": {"epoch": 3, "agents": [{"name": "diskcheck", "kind": "cli", "input_mode": "prompt_text", "output_mode": "answer+artifacts", "interactive": false, "permission_mode": "sandbox", "protocol_ver": 1, "exposed": true}]}},
  "tasks": {"t1": {"id": "t1", "trace_id": "tr", "target_node": "n1", "target_agent": "diskcheck", "submitted_by": "admin", "status": "completed", "created_at": "2026-10-24T08:00:00Z", "updated_at": "2026-10-24T08:01:00Z", "expire_at": "2026-10-31T08:00:00Z"}},
  "mailboxes": {},
  "batches": {},
  "batch_idems": {},
  "workdir_locks": {}
}`
	if err := os.WriteFile(dir+"/hub_state.json", []byte(old), 0o644); err != nil {
		t.Fatalf("预置旧快照: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("旧快照导入应成功: %v", err)
	}
	// 旧 json 备份改名（导入语义：原文件不在，恰一个 imported-* 备份）
	if _, err := os.Stat(dir + "/hub_state.json"); !os.IsNotExist(err) {
		t.Errorf("导入后旧 json 应已改名: %v", err)
	}
	if backups, _ := filepath.Glob(dir + "/hub_state.json.imported-*"); len(backups) != 1 {
		t.Errorf("应恰一个导入备份: %v", backups)
	}
	v := s.View()
	if v.Nodes["n1"].Epoch != 3 || len(v.Nodes["n1"].Agents) != 1 {
		t.Errorf("旧实体 nodes 应完整: %+v", v.Nodes)
	}
	if got := v.Tasks["t1"]; got.ID != "t1" || got.Status != "completed" {
		t.Errorf("旧实体 tasks 应完整: %+v", got)
	}
	if v.Works == nil || v.WorkIdems == nil {
		t.Errorf("Work map 应归一非 nil: %+v %+v", v.Works, v.WorkIdems)
	}
	if len(v.Works) != 0 || len(v.WorkIdems) != 0 {
		t.Errorf("迁移后应为空集: %d %d", len(v.Works), len(v.WorkIdems))
	}

	// 迁移后可正常写入 Work（含事件账本）
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	w := Work{
		ID: "w-abc", Goal: "全量盘点", PrimeTerm: "prime-t1", Status: WorkStatusPlanning,
		Steps:    []WorkStep{{Seq: 1, Desc: "盘点", TargetNode: "n1", TargetAgent: "diskcheck", Status: StepStatusPending}},
		Attempts: []WorkAttempt{{Seq: 1, StepSeq: 1, IdemKey: "k1", Outcome: AttemptOutcomeSubmitted}},
		Events:   []WorkEvent{{Seq: 1, At: at, Kind: "work_created", By: "mcp", Body: "建单"}},
		EventSeq: 1, CreatedAt: at,
	}
	if err := s.Update(func(st *State) error { st.Works[w.ID] = w; st.WorkIdems["fp"] = w.ID; return nil }); err != nil {
		t.Fatalf("迁移后写 Work: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启：Work 全字段恢复（含 steps/attempts/events 深层与 event_seq）
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer s2.Close()
	got, ok := s2.View().Works["w-abc"]
	if !ok {
		t.Fatal("重启后 Work 丢失")
	}
	if got.Goal != "全量盘点" || got.PrimeTerm != "prime-t1" || got.Status != WorkStatusPlanning ||
		len(got.Steps) != 1 || got.Steps[0].Desc != "盘点" || got.Steps[0].Status != StepStatusPending ||
		len(got.Attempts) != 1 || got.Attempts[0].IdemKey != "k1" ||
		len(got.Events) != 1 || got.Events[0].Kind != "work_created" || got.EventSeq != 1 ||
		!got.CreatedAt.Equal(at) || !got.Events[0].At.Equal(at) {
		t.Errorf("重启后 Work 字段不符: %+v", got)
	}
	if s2.View().WorkIdems["fp"] != "w-abc" {
		t.Errorf("指纹映射未恢复: %+v", s2.View().WorkIdems)
	}
}

// TestWorkCloneIsolationWorkFields Clone/CoW 纪律对 Work 深层字段成立（codex
// 审查建议修 4：非空依赖/Attempt/终态时间指针全上——空值形态测不出浅拷贝回归；
// 探针用**就地改写**而非 append：append 是否复用底层数组取决于容量，只有就地
// 写能确定性地暴露浅拷贝）。事务内改副本不污染先前发布快照；读者改旧快照
// （含解引用指针）不影响已发布状态。
func TestWorkCloneIsolationWorkFields(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	closedAt := at.Add(time.Hour)
	expiresAt := at.Add(31 * 24 * time.Hour)
	if err := s.Update(func(st *State) error {
		st.Works["w1"] = Work{
			ID: "w1", Goal: "g", Status: WorkStatusDone,
			Steps: []WorkStep{
				{Seq: 1, Desc: "d1", TargetNode: "n", TargetAgent: "a", Status: StepStatusDone},
				{Seq: 2, Desc: "d2", TargetNode: "n", TargetAgent: "a", DependsOn: []int64{1}, Status: StepStatusPending},
			},
			Attempts:  []WorkAttempt{{Seq: 1, StepSeq: 1, TaskID: "t-1", IdemKey: "k1", Outcome: AttemptOutcomeDone}},
			Events:    []WorkEvent{{Seq: 1, At: at, Kind: "work_created", By: "mcp", Principal: "admin", Body: "建单"}},
			EventSeq:  1,
			CreatedAt: at, ClosedAt: &closedAt, ExpiresAt: &expiresAt,
		}
		return nil
	}); err != nil {
		t.Fatalf("预置: %v", err)
	}
	reader := s.View() // 读者持有旧快照
	if err := s.Update(func(st *State) error {
		w := st.Works["w1"] // 事务内为本事务深拷贝，可自由修改
		w.Status = WorkStatusAbandoned
		w.Steps[0].Note = "mutated"
		w.Steps[1].DependsOn[0] = 99 // 就地改写：Clone 未逐层复制依赖切片即穿透
		w.Attempts[0].Outcome = AttemptOutcomeSuperseded
		w.Events[0].Body = "mutated"
		*w.ClosedAt = at.Add(2 * time.Hour) // 解引用改写：Clone 未换新指针即穿透
		w.Events = append(w.Events, WorkEvent{Seq: 2, Kind: "x"})
		w.EventSeq = 2
		st.Works["w1"] = w
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	old := reader.Works["w1"]
	if old.Status != WorkStatusDone ||
		old.Steps[0].Note != "" || old.Steps[1].DependsOn[0] != 1 ||
		old.Attempts[0].Outcome != AttemptOutcomeDone || len(old.Attempts) != 1 ||
		old.Events[0].Body != "建单" || len(old.Events) != 1 || old.EventSeq != 1 ||
		!old.ClosedAt.Equal(closedAt) || !old.ExpiresAt.Equal(expiresAt) {
		t.Errorf("读者旧快照被事务污染（CoW 被破坏）: %+v", old)
	}
	got := s.View().Works["w1"]
	if got.Status != WorkStatusAbandoned ||
		got.Steps[0].Note != "mutated" || got.Steps[1].DependsOn[0] != 99 ||
		got.Attempts[0].Outcome != AttemptOutcomeSuperseded ||
		got.Events[0].Body != "mutated" || len(got.Events) != 2 || got.EventSeq != 2 ||
		!got.ClosedAt.Equal(at.Add(2*time.Hour)) {
		t.Errorf("事务修改未在新状态生效: %+v", got)
	}
	// 读者反向修改旧快照（含解引用终态指针）不得影响已发布状态
	old.Steps[0].Note = "reader-mutated"
	old.Steps[1].DependsOn[0] = 42
	old.Events[0].Body = "reader-mutated"
	*old.ClosedAt = at.Add(99 * time.Hour)
	got = s.View().Works["w1"]
	if got.Steps[0].Note != "mutated" || got.Steps[1].DependsOn[0] != 99 ||
		got.Events[0].Body != "mutated" || !got.ClosedAt.Equal(at.Add(2*time.Hour)) {
		t.Errorf("读者修改旧快照影响已发布状态: %+v", got)
	}
}

// TestWorkJSONRoundTrip Work 结构序列化兼容（omitempty 语义：未关闭单不出
// closed_at/expires_at，报告空不出 report）。
func TestWorkJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	w := Work{ID: "w1", Goal: "g", Status: WorkStatusPlanning, CreatedAt: at,
		Steps: []WorkStep{{Seq: 1, Desc: "d", TargetNode: "n", TargetAgent: "a", Status: StepStatusPending}}}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{"closed_at", "expires_at", "report", "attempts", "prime_term"} {
		if strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("零值字段 %s 不应输出: %s", field, b)
		}
	}
	var back Work
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != w.ID || len(back.Steps) != 1 || !back.CreatedAt.Equal(at) {
		t.Errorf("round trip 不符: %+v", back)
	}
}
