package localapi

// agentsstate_w07_test.go 能力卡空间声明盖章测试（W07-01）：SetSpacesCapability
// 注入后 Cards() 对全部能力卡盖章 AgentCard.Spaces（register/card_update 同源）；
// 未注入（旧词形/未装配 collab）不落 spaces 键。声明版本恒 protocol.SpacesCapV1，
// 值随注入参数（不给生产代码留第二个真值源）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// jsonContainsBytes 子串判定（测试可读性薄封装）。
func jsonContainsBytes(s, sub string) bool { return strings.Contains(s, sub) }

func TestW07AgentStateSpacesStamping(t *testing.T) {
	baseline := []protocol.ExecProfile{
		{Name: "coder", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 30, PermissionMode: "workspace-write", Exposed: true},
		{Name: "reviewer", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 30, PermissionMode: "read-only", Exposed: true},
	}
	dir := t.TempDir()

	// 未注入：不声明（未装配 collab 词形）。
	st, err := OpenAgentState(dir, baseline)
	if err != nil {
		t.Fatal(err)
	}
	cards := st.Cards()
	if len(cards) != 2 {
		t.Fatalf("能力卡数: %d", len(cards))
	}
	for _, c := range cards {
		if c.Spaces != "" {
			t.Fatalf("未装配不得声明空间特性: %+v", c)
		}
	}
	// 线上形态：序列化不落 spaces 键（omitempty，旧 hub 零感知）。
	b, _ := json.Marshal(cards[0])
	if jsonContainsBytes(string(b), `"spaces"`) {
		t.Fatalf("未声明卡不得落 spaces 键: %s", b)
	}

	// 注入（collab 装配同源，cmd/node 装配序）：全部卡盖章。
	st2, err := OpenAgentState(t.TempDir(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	st2.SetSpacesCapability(protocol.SpacesCapV1)
	cards2 := st2.Cards()
	for _, c := range cards2 {
		if c.Spaces != protocol.SpacesCapV1 {
			t.Fatalf("装配后应全部声明空间特性: %+v", c)
		}
	}
	b2, _ := json.Marshal(cards2[0])
	if !jsonContainsBytes(string(b2), `"spaces":"v1"`) {
		t.Fatalf("声明卡应落 spaces 键: %s", b2)
	}
}
