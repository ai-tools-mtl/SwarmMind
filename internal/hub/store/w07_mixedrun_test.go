package store

// w07_mixedrun_test.go 旧版混跑落盘 fixture（W07-01）：hub store 节点记录行体
// （SQLite 每节点一行 JSON body，sqlite.go put 同形）双向容忍实证——
//   - 旧 schema 行体（能力卡无 spaces 字段）解码进现结构：零值容忍，既有字段
//     完好（新 hub 读旧快照 = 未声明如实，门槛据此拒绝不臆造）；
//   - 新 schema 行体（能力卡携 spaces/orchestrator/session_adapter）解码进旧
//     schema 形态（legacyNodeRecord/legacyCard——本批 spaces 引入前的结构快照）：
//     未知字段丢弃不报错，既有字段完好（旧 hub 读新快照 = 降级容忍，任务面
//     不受影响）。
// 行体形态与 sqlite.go 持久化写出的 json.Marshal(NodeRecord) 同构，落盘文件夹具
// 非 omitempty 口头声明。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// legacyCard 旧 schema hub 的 AgentCard 快照（spaces 引入前词形，含
// orchestrator/session_adapter）。仅测试用，不参与生产编码。
type legacyCard struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	InputMode      string `json:"input_mode"`
	OutputMode     string `json:"output_mode"`
	PermissionMode string `json:"permission_mode"`
	ProtocolVer    int    `json:"protocol_ver"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	Exposed        bool   `json:"exposed"`
	AllowArgs      bool   `json:"allow_args"`
	StdinToArg     bool   `json:"stdin_to_arg"`
	Orchestrator   bool   `json:"orchestrator,omitempty"`
	SessionAdapter string `json:"session_adapter,omitempty"`
}

// legacyNodeRecord 旧 schema hub 的 NodeRecord 快照。
type legacyNodeRecord struct {
	Epoch  int64        `json:"epoch"`
	Agents []legacyCard `json:"agents"`
}

func loadW07Record(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取夹具 %s: %v", name, err)
	}
	return b
}

// TestW07NodeRecordOldBodyLoads 旧 schema 行体进现结构：spaces 解码零值（未声明
// 如实），既有字段完好。
func TestW07NodeRecordOldBodyLoads(t *testing.T) {
	var rec NodeRecord
	if err := json.Unmarshal(loadW07Record(t, "w07_node_record_old.json"), &rec); err != nil {
		t.Fatalf("旧行体解码失败: %v", err)
	}
	if rec.Epoch != 3 || len(rec.Agents) != 1 {
		t.Fatalf("旧行体既有字段漂移: epoch=%d agents=%d", rec.Epoch, len(rec.Agents))
	}
	c := rec.Agents[0]
	if c.Name != "old-coder" || !c.Exposed || !c.AllowArgs || c.StdinToArg {
		t.Fatalf("旧行体能力卡字段漂移: %+v", c)
	}
	if c.Spaces != "" || c.SessionAdapter != "" || c.Orchestrator {
		t.Fatalf("旧行体不得解码出更晚字段: %+v", c)
	}
}

// TestW07NodeRecordNewBodyLegacyTolerance 新 schema 行体进旧 schema 形态：未知
// 字段（spaces/orchestrator/session_adapter）丢弃不报错，既有字段完好。
func TestW07NodeRecordNewBodyLegacyTolerance(t *testing.T) {
	var rec legacyNodeRecord
	if err := json.Unmarshal(loadW07Record(t, "w07_node_record_new.json"), &rec); err != nil {
		t.Fatalf("旧 schema 解码新行体应容忍未知字段: %v", err)
	}
	if rec.Epoch != 7 || len(rec.Agents) != 1 {
		t.Fatalf("旧 schema 解码后既有字段漂移: epoch=%d agents=%d", rec.Epoch, len(rec.Agents))
	}
	c := rec.Agents[0]
	if c.Name != "new-coder" || !c.Exposed || !c.AllowArgs || !c.StdinToArg || c.SessionAdapter != "codex" {
		t.Fatalf("旧 schema 解码后能力卡字段漂移: %+v", c)
	}
}
