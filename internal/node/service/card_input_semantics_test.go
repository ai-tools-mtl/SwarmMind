package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCardsFromProfilesInputSemantics 能力卡携带输入语义声明（maturity-dispatch-args-ux，
// SPEC 4.9）：AllowArgs/StdinToArg 由 ExecProfile 原样映射进 AgentCard 上报——hub 目录
// 与控制台据此呈现传参面，属能力声明而非本机执行细节；JSON 序列化含 allow_args/
// stdin_to_arg 键（调用方可发现），且卡内不泄露 ExecProfile 其余字段。
func TestCardsFromProfilesInputSemantics(t *testing.T) {
	cards := CardsFromProfiles([]protocol.ExecProfile{
		{Name: "tool", Kind: protocol.AgentKindCLI, Command: "/bin/echo", Workdir: t.TempDir(),
			TimeoutSec: 1, Exposed: true, AllowArgs: true, StdinToArg: true},
		{Name: "strict", Kind: protocol.AgentKindCLI, Command: "/bin/cat", Workdir: t.TempDir(),
			TimeoutSec: 1, Exposed: true},
	})
	if len(cards) != 2 {
		t.Fatalf("应生成 2 张卡, got %d", len(cards))
	}
	if !cards[0].AllowArgs || !cards[0].StdinToArg {
		t.Fatalf("双开能力卡应携带 allow_args/stdin_to_arg=true: %+v", cards[0])
	}
	if cards[1].AllowArgs || cards[1].StdinToArg {
		t.Fatalf("缺省能力卡应为 false（与 ExecProfile 默认一致）: %+v", cards[1])
	}
	b, err := json.Marshal(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"allow_args":true`, `"stdin_to_arg":true`} {
		if !strings.Contains(s, key) {
			t.Fatalf("能力卡 JSON 缺输入语义键 %s: %s", key, s)
		}
	}
	// 本机执行细节清单与 SPEC 六 /api/v1/agents 行口径一致：公开声明投影仅
	// allow_args/stdin_to_arg 两键，其余执行细节（含 timeout_sec）一概不出卡。
	for _, leak := range []string{"command", "workdir", "agent_args", "timeout_sec", "allow_env", "config_home"} {
		if strings.Contains(s, `"`+leak+`"`) {
			t.Fatalf("能力卡不得泄露本机执行细节 %s: %s", leak, s)
		}
	}
}
