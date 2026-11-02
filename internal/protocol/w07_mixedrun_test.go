package protocol

// w07_mixedrun_test.go 旧版混跑落盘 fixture（W07-01，docs/tasks/v3/W07-hub-routing.md
// 「Schema升级与旧字段缺省必须有落盘fixture，不能只说明JSON omitempty即可兼容」）：
// 真实 JSON 文件夹具（testdata/w07_old_node_register.json / w07_new_node_card.json）
// 双向容忍实证——
//   - 旧 node × 新 hub：旧版能力卡（spaces/orchestrator/session_adapter 缺省）
//     经注册帧解码进新协议结构，spaces 解码零值（hub 门槛据此如实拒绝，不把旧
//     node 当有新特性），既有字段逐位完好；
//   - 新 node × 旧 schema hub：新卡全量字段 JSON 解码进旧 schema 形态
//     （legacyAgentCard——本批 spaces 字段引入前的 AgentCard 结构快照），未知字段
//     丢弃、既有字段完好（encoding/json 容忍语义的落盘实证，非注释声明）。
// 加法字段 omitempty 形态同批锁定（缺省不落线、非缺省原样承载）。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// legacyAgentCard 旧 schema hub 的 AgentCard 结构快照（W07-01 spaces 字段引入前
// 的词形：含 orchestrator/session_adapter，无 spaces）。仅测试内声明，用于实证
// 旧端解码新 JSON 的容忍面——不参与任何生产编码。
type legacyAgentCard struct {
	Name           string    `json:"name"`
	Kind           AgentKind `json:"kind"`
	InputMode      string    `json:"input_mode"`
	OutputMode     string    `json:"output_mode"`
	Interactive    bool      `json:"interactive"`
	PermissionMode string    `json:"permission_mode"`
	ProtocolVer    int       `json:"protocol_ver"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	Exposed        bool      `json:"exposed"`
	AllowArgs      bool      `json:"allow_args"`
	StdinToArg     bool      `json:"stdin_to_arg"`
	Orchestrator   bool      `json:"orchestrator,omitempty"`
	SessionAdapter string    `json:"session_adapter,omitempty"`
}

// loadW07Fixture 读落盘夹具（真实文件，非内联字符串）。
func loadW07Fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("读取夹具 %s: %v", name, err)
	}
	return b
}

// TestW07OldNodeRegisterPayloadLoads 旧 node × 新 hub：注册帧载荷（无新字段词形）
// 经 RegisterPayload 解码——旧卡 spaces 解码零值、既有字段逐位完好。hub 门槛
// 数据源（SpacesCapability）据此对未声明 node 如实拒绝空间类下发。
func TestW07OldNodeRegisterPayloadLoads(t *testing.T) {
	b := loadW07Fixture(t, "w07_old_node_register.json")
	var p RegisterPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("旧版注册载荷解码失败: %v", err)
	}
	if p.NodeID != "n-old-legacy" || len(p.Agents) != 2 {
		t.Fatalf("载荷身份/能力数不符: node=%s agents=%d", p.NodeID, len(p.Agents))
	}
	c := p.Agents[0]
	if c.Name != "old-coder" || c.Kind != AgentKindCLI || !c.Exposed || !c.AllowArgs || c.StdinToArg {
		t.Fatalf("旧卡既有字段漂移: %+v", c)
	}
	if c.WorkspaceID != "a1b2c3d4e5f60718" {
		t.Fatalf("workspace_id 应完好: %q", c.WorkspaceID)
	}
	for _, card := range p.Agents {
		if card.Spaces != "" {
			t.Fatalf("旧卡不得解码出空间声明（不把旧 node 当有新特性）: %+v", card)
		}
		if card.Orchestrator || card.SessionAdapter != "" {
			t.Fatalf("旧卡不应携带更晚字段: %+v", card)
		}
	}
}

// TestW07NewCardOldSchemaTolerance 新 node × 旧 schema hub：新卡全量字段 JSON
// 解码进旧 schema 形态（legacyAgentCard）——未知字段 spaces 丢弃不报错，既有
// 字段完好。旧 hub 不感知空间声明（能力发现降级），任务/会话面零影响。
func TestW07NewCardOldSchemaTolerance(t *testing.T) {
	b := loadW07Fixture(t, "w07_new_node_card.json")
	var lc legacyAgentCard
	if err := json.Unmarshal(b, &lc); err != nil {
		t.Fatalf("旧 schema 解码新卡应容忍未知字段: %v", err)
	}
	if lc.Name != "new-coder" || lc.Kind != AgentKindCLI || !lc.AllowArgs || !lc.StdinToArg {
		t.Fatalf("旧 schema 解码后既有字段漂移: %+v", lc)
	}
	if lc.SessionAdapter != SessionAdapterCodex || lc.Orchestrator {
		t.Fatalf("session_adapter/orchestrator 应完好: %+v", lc)
	}
	// 反向锚：同一文件进新 schema 完整保真（spaces 在场）。
	var nc AgentCard
	if err := json.Unmarshal(b, &nc); err != nil {
		t.Fatalf("新 schema 解码失败: %v", err)
	}
	if nc.Spaces != SpacesCapV1 {
		t.Fatalf("新 schema 应保留 spaces 声明: %q", nc.Spaces)
	}
}

// TestW07SpacesFieldsOmitEmptyWireForm 加法字段线上形态：缺省不落线（旧端零
// 感知）、非缺省原样承载（往返保真）。两字段（AgentCard.Spaces /
// SessionTurnRef.Space）同一批锁定。
func TestW07SpacesFieldsOmitEmptyWireForm(t *testing.T) {
	b, err := json.Marshal(AgentCard{Name: "a", Kind: AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "rw", ProtocolVer: 1, Exposed: true})
	if err != nil {
		t.Fatal(err)
	}
	if jsonContains(string(b), "spaces") {
		t.Fatalf("未声明 spaces 不得落线（omitempty）: %s", b)
	}
	card := AgentCard{Name: "a", Kind: AgentKindCLI, InputMode: "prompt_text", OutputMode: "answer+artifacts", PermissionMode: "rw", ProtocolVer: 1, Exposed: true, Spaces: SpacesCapV1}
	b, err = json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var back AgentCard
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Spaces != SpacesCapV1 {
		t.Fatalf("spaces 声明往返失真: %q", back.Spaces)
	}

	ref := SessionTurnRef{SessionID: "s-1", TurnSeq: 1, Op: SessionTurnOpSend}
	b, err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if jsonContains(string(b), "space") {
		t.Fatalf("普通会话轮不得落 space 键（omitempty，旧端零感知）: %s", b)
	}
	ref.Space = "hub:abc123"
	b, err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var backRef SessionTurnRef
	if err := json.Unmarshal(b, &backRef); err != nil {
		t.Fatal(err)
	}
	if backRef.Space != "hub:abc123" || backRef.SessionID != "s-1" || backRef.TurnSeq != 1 {
		t.Fatalf("space 标记往返失真: %+v", backRef)
	}
}

// jsonContains 子串判定（测试可读性薄封装）。
func jsonContains(s, sub string) bool { return strings.Contains(s, sub) }
