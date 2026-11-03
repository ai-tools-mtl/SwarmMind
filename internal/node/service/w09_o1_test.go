package service

import (
	"errors"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// W09-O1 中心绑定同步核测试（docs/tasks/v3/W09-O1-assessment.md §4 裁决落地）：
// hubBind 登记会话的普通轮两提交面（主人 local/agent）同步拒 space_dispatch_required
//（与 hub 面 409 同形同码——此前 202 受理+执行口异步拒，STAGE-C 采证 O1）。
// 另锁：探针未命中（未登记会话）零过拒；拒绝零副作用（无账本任务条目）。
func TestHubBindPlainTurnSyncReject(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	injectHubBind(t, f)
	sid := f.createSession(f.cwd)
	// 对照会话：hubBind 在场但不登记它——探针未命中零过拒（统一前行为逐字节等价）。
	sidOther := f.createSession(f.cwd2)

	// 主人面：登记前对照会话照常受理（探针未命中不误伤）。
	if _, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", SessionID: sidOther, Prompt: "未登记会话照常"}); err != nil {
		t.Fatalf("未登记会话普通轮应照常受理（探针未命中零过拒）: %v", err)
	}
	// agent 面：同规。
	if _, _, err := f.svc.SubmitAgentSessionTurn(AgentSessionTurnRequest{NodeID: "n1", SessionID: sidOther, Prompt: "未登记会话照常2"}); err != nil {
		t.Fatalf("agent 面未登记会话普通轮应照常受理: %v", err)
	}

	// 中心绑定登记（hub space_bind 帧真实词形，与生产下发同口）。
	sendSpaceBind(t, f.svc, sid, "hub:sp-o1")
	entriesBefore := f.countSessionTasks()

	// 主人面：登记会话普通轮提交侧同步拒（不再 202+异步拒）。
	var re *RemoteError
	_, err := f.svc.SubmitSessionTurnLocal(LocalSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "绕过"})
	if !errors.As(err, &re) || re.Code != collab.CodeSpaceDispatchRequired {
		t.Fatalf("hubBind 登记会话主人面普通轮应同步拒 space_dispatch_required: %v", err)
	}
	// agent 面：同形同码。
	if _, _, err := f.svc.SubmitAgentSessionTurn(AgentSessionTurnRequest{NodeID: "n1", SessionID: sid, Prompt: "绕过2"}); !errors.As(err, &re) || re.Code != collab.CodeSpaceDispatchRequired {
		t.Fatalf("hubBind 登记会话 agent 面普通轮应同步拒 space_dispatch_required: %v", err)
	}
	// 零副作用：拒绝不产生任务实物（对照 hub 面同步 409 口径——无 doomed 账本行）。
	if got := f.countSessionTasks(); got != entriesBefore {
		t.Fatalf("同步拒应零账本副作用：before=%d after=%d", entriesBefore, got)
	}
}
