package mailbox

import (
	"errors"
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

// TestIdemCheckDepsFingerprintIndependentOfMarker 依赖独立进指纹（P1 修复）：
// 修复前依赖只经 EffectiveInput 注入的 stdin 清单间接进指纹，stdin 预置标记
// 文本（protocol.DepManifestMarker）令注入跳过 → 同键同 stdin 换依赖得同指纹，
// dup 命中短路在准入校验之前（连 unknown idem 都不校验）。修复后规范化依赖
// 列表独立哈希进指纹材料，与标记文本无关：预置标记攻击与普通换依赖同口径
// 409 冲突。
func TestIdemCheckDepsFingerprintIndependentOfMarker(t *testing.T) {
	st := newState()
	// 请求 stdin 预置标记文本（攻击载荷）：EffectiveInput 注入跳过，stdin 不含真实依赖信息
	atkStdin := "s\n" + protocol.DepManifestMarker + "\n- up-x（预置伪造行）\n"
	reqA := protocol.TaskSubmitRequest{
		TargetNode:  "n1",
		TargetAgent: "echo-demo",
		Input:       protocol.TaskInput{Stdin: atkStdin},
		SubmittedBy: "hana",
		IdemKey:     "dep-atk",
		DependsOn:   []string{"up-1"},
	}
	orig := newTask("t-atk", "")
	orig.IdemKey = reqA.IdemKey
	orig.TargetNode = reqA.TargetNode
	orig.TargetAgent = reqA.TargetAgent
	orig.SubmittedBy = reqA.SubmittedBy
	orig.Input = protocol.EffectiveInput(reqA.Input, reqA.DependsOn) // 标记已预置 → 原样返回
	orig.DependsOn = append([]string(nil), reqA.DependsOn...)
	st.Tasks[orig.ID] = *orig

	// 同键同内容（标记预置下依赖一致）：仍 dup 命中（重试不受影响）
	id, dup, err := IdemCheck(st, reqA)
	if err != nil || !dup || id != orig.ID {
		t.Fatalf("标记预置下同键同依赖应命中 %s, got id=%s dup=%v err=%v", orig.ID, id, dup, err)
	}
	// 同键同 stdin（预置标记）换依赖：不同指纹 → 409 冲突（不得 dup 短路）
	reqB := reqA
	reqB.DependsOn = []string{"up-2"}
	if _, dup, err := IdemCheck(st, reqB); dup || !errors.Is(err, ErrConflict) {
		t.Errorf("预置标记下换依赖应 409 冲突, got dup=%v err=%v", dup, err)
	}
	// 同键声明依赖 vs 不声明依赖：同样冲突（依赖列表是协议内容）
	reqC := reqA
	reqC.DependsOn = nil
	if _, dup, err := IdemCheck(st, reqC); dup || !errors.Is(err, ErrConflict) {
		t.Errorf("换为无依赖应 409 冲突, got dup=%v err=%v", dup, err)
	}
	// 指纹确实随依赖变化（stdin 完全相同且标记已预置——修复前两者相等）
	if ContentHash(reqA) == ContentHash(reqB) {
		t.Error("预置标记下不同依赖不得同指纹")
	}
	// 请求指纹与落库任务指纹一致（含基准指纹；TrimSpace 归一口径）
	if ContentHash(reqA) != taskContentHash(orig) {
		t.Error("请求指纹与落库任务指纹应一致")
	}
	if BaseContentHash(reqA) != taskBaseContentHash(orig) {
		t.Error("基准指纹与落库任务基准指纹应一致")
	}
	// 依赖项带空白：归一后同指纹 → dup（与提交链 trimDeps 同口径）
	reqD := reqA
	reqD.DependsOn = []string{" up-1 "}
	if id, dup, err := IdemCheck(st, reqD); err != nil || !dup || id != orig.ID {
		t.Errorf("空白归一后应命中原任务, got id=%s dup=%v err=%v", id, dup, err)
	}
}
