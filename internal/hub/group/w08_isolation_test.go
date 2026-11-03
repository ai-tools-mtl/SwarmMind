package group

// w08_isolation_test.go AC20 Groups fixture 锚（W08-02，docs/tasks/v3/W08-recovery.md
// 机械验收 AC20「旧 schema/oldTasks/Groups/Work/prime 配置与当前 T1–T3 fixture 验证
// 迁移和回滚前置」+ 恢复规则「不能被旧 Group 200条/24h 消息窗口带走活跃空间材料」）：
//   - 旧词形 groups.json fixture（无 host_active/final_seen 等 omitempty 新字段的
//     历史形态）零迁移直读——FinalSeen 由现存消息迁移补记（loadState 既有口径）；
//   - Group 保留清理（24h 过期 + 200 条滚动）只动 groups.json 自己——同目录共存的
//     spaces.json（活跃空间材料）逐字节不动（存储层物理隔离的实证锚，W08-01 契约
//     的机械验收面）。
//
// spaces.json 内容由 collab 真实 Open→CreateSpace→Close 产出（非手编），保证
// 「与当前 T1–T3 fixture 同词形」；比对基准为清理前逐字节快照。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// w08LegacyGroupsJSON 旧词形群快照夹具（T3 引入 omitempty 主持/收敛字段前的
// 历史落盘词形；消息全部超 24h——Sweep 应整组裁空）。
const w08LegacyGroupsJSON = `{
  "groups": {
    "g-old0001": {
      "group_id": "g-old0001",
      "goal": "旧群：结论已转任务",
      "status": "concluded",
      "host": "coder",
      "members": ["coder", "reviewer"],
      "budget": { "max_rounds": 30 },
      "created_at": "2026-10-20T08:00:00Z",
      "closed_by": "owner",
      "closed_at": "2026-10-20T09:00:00Z"
    }
  },
  "messages": {
    "g-old0001": [
      { "msg_id": "m1", "group_id": "g-old0001", "seq": 1, "from": "owner", "kind": "user", "body": "开题", "created_at": "2026-10-20T08:01:00Z" },
      { "msg_id": "m2", "group_id": "g-old0001", "seq": 2, "from": "coder", "kind": "agent", "body": "FINAL: 完成", "created_at": "2026-10-20T08:02:00Z" }
    ]
  },
  "seqs": { "g-old0001": 2 },
  "agent_msgs": { "g-old0001": 1 }
}`

// TestW08AC20LegacyGroupsFixtureIsolation 旧词形 Groups fixture：零迁移直读 +
// FinalSeen 现存消息迁移补记；Sweep（24h 过期清理）裁空群消息后，同目录
// spaces.json（活跃空间材料）逐字节不动。
func TestW08AC20LegacyGroupsFixtureIsolation(t *testing.T) {
	dir := t.TempDir()
	spacePath := filepath.Join(dir, "spaces.json")

	// 活跃空间材料用真实 collab 产出（当前词形 fixture），关闭后不再触碰。
	{
		st, err := collab.Open(dir, collab.Options{})
		if err != nil {
			t.Fatalf("collab Open: %v", err)
		}
		if _, _, err := st.CreateSpace(collab.CreateRequest{
			Goal: "活跃空间目标（Group 清理不得带走）", Mode: collab.ModeManual,
			Members: []collab.Member{{Name: "coder", SessionID: "s-w08ac20member1"}},
		}, collab.Subject{Name: "owner", Class: collab.ClassOwner}); err != nil {
			t.Fatalf("建空间: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("collab Close: %v", err)
		}
	}
	spaceBefore, err := os.ReadFile(spacePath)
	if err != nil {
		t.Fatalf("读空间快照: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(w08LegacyGroupsJSON), 0o644); err != nil {
		t.Fatalf("预置旧群快照: %v", err)
	}
	gst, err := Open(dir)
	if err != nil {
		t.Fatalf("旧词形 groups.json 应零迁移直读: %v", err)
	}
	t.Cleanup(func() { _ = gst.Close() })
	g, ok := gst.GetGroup("g-old0001")
	if !ok || g.Goal != "旧群：结论已转任务" {
		t.Fatalf("旧群应原样载入: %+v ok=%v", g, ok)
	}
	// FinalSeen 历史口径：现存 FINAL 消息迁移补记（标记落地前的旧快照）。
	if !g.FinalSeen {
		t.Fatal("旧快照现存 FINAL 消息应迁移补记 final_seen")
	}

	// Group 保留清理（24h 过期 + 200 滚动）：消息全部超 24h → 整组裁空。
	now := protocol.NowUTC()
	if n := gst.Sweep(now); n == 0 {
		t.Fatal("超龄群消息应被过期清理")
	}
	msgs, _, err := gst.ListMessages("g-old0001", 0)
	if err != nil {
		t.Fatalf("读群消息: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("超 24h 群消息应裁空: %d", len(msgs))
	}

	// 物理隔离实证：Group 清理只动 groups.json——spaces.json 逐字节不动。
	spaceAfter, err := os.ReadFile(spacePath)
	if err != nil {
		t.Fatalf("读空间快照: %v", err)
	}
	if string(spaceBefore) != string(spaceAfter) {
		t.Fatal("Group 保留清理不得触碰活跃空间材料（spaces.json 逐字节比对）")
	}
	// 空间材料仍然可装配（快照完好）。
	st2, err := collab.Open(dir, collab.Options{})
	if err != nil {
		t.Fatalf("Group 清理后空间快照应完好可装配: %v", err)
	}
	sp := st2.Spaces()
	if len(sp) != 1 || sp[0].Goal != "活跃空间目标（Group 清理不得带走）" {
		t.Fatalf("空间材料应完整: n=%d", len(sp))
	}
	_ = st2.Close()
	_ = time.Time{}
}
