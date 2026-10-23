package mailbox

import (
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestIdemCheckWithDepends 依赖任务的幂等指纹（P4.3）：请求指纹过 EffectiveInput
// 生效形态（stdin 附依赖清单），落库任务体同变换——同请求重试 dup 命中不因清单
// 注入漂移；依赖列表本身参与指纹（同键换依赖=不同内容=冲突）。
func TestIdemCheckWithDepends(t *testing.T) {
	st := newState()
	req := protocol.TaskSubmitRequest{
		TargetNode:  "n1",
		TargetAgent: "echo-demo",
		Input:       protocol.TaskInput{Stdin: "s"},
		SubmittedBy: "hana",
		IdemKey:     "dep-001",
		DependsOn:   []string{"up-1", "up-2"},
	}
	// 落库任务体按 newTask 同口径构造：Input=EffectiveInput 生效形态 + DependsOn
	orig := newTask("t-dep", "")
	orig.IdemKey = req.IdemKey
	orig.TargetNode = req.TargetNode
	orig.TargetAgent = req.TargetAgent
	orig.SubmittedBy = req.SubmittedBy
	orig.Input = protocol.EffectiveInput(req.Input, req.DependsOn)
	orig.DependsOn = append([]string(nil), req.DependsOn...)
	st.Tasks[orig.ID] = *orig

	// 同键同内容（含同依赖）：dup 命中——清单注入不破坏重试
	id, dup, err := IdemCheck(st, req)
	if err != nil || !dup || id != orig.ID {
		t.Fatalf("依赖任务同键重试应命中 %s, got id=%s dup=%v err=%v", orig.ID, id, dup, err)
	}
	// 同键换依赖：不同内容 → 冲突
	changed := req
	changed.DependsOn = []string{"up-1"}
	if _, _, err := IdemCheck(st, changed); err == nil {
		t.Error("同键不同依赖应冲突")
	}
	// 落库任务的 stdin 已含清单：taskContentHash 走 EffectiveInput 幂等路径，
	// 与请求指纹逐字节一致（双重断言：ContentHash(req) == taskContentHash(orig)）
	if ContentHash(req) != taskContentHash(orig) {
		t.Error("请求指纹与落库任务指纹应一致")
	}
	if BaseContentHash(req) != taskBaseContentHash(orig) {
		t.Error("基准指纹与落库任务基准指纹应一致")
	}
}
