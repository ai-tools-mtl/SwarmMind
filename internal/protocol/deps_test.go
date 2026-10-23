package protocol

import (
	"strings"
	"testing"
)

// TestEffectiveInputManifest EffectiveInput 生效形态：声明依赖时 stdin 尾部附
// 依赖清单（上游 idem 供 agent 自取；挂载为 L2 第二子批）；幂等不重复追加；
// 未声明依赖时逐字节不变（向后兼容）。
func TestEffectiveInputManifest(t *testing.T) {
	in := TaskInput{Stdin: "do it"}
	// 未声明依赖：原样
	if out := EffectiveInput(in, nil); out.Stdin != "do it" {
		t.Errorf("无依赖应原样返回, got %q", out.Stdin)
	}
	deps := []string{"up-1", "up-2"}
	out := EffectiveInput(in, deps)
	if !strings.HasPrefix(out.Stdin, "do it") {
		t.Errorf("清单应追加在原 stdin 之后, got %q", out.Stdin)
	}
	for _, want := range []string{DepManifestMarker, "- up-1\n", "- up-2\n"} {
		if !strings.Contains(out.Stdin, want) {
			t.Errorf("清单缺 %q: %q", want, out.Stdin)
		}
	}
	// 幂等：已含清单的 stdin 不重复追加（幂等指纹与落库任务共用本函数的前提）
	again := EffectiveInput(out, deps)
	if again.Stdin != out.Stdin {
		t.Errorf("EffectiveInput 应幂等:\n once=%q\n twice=%q", out.Stdin, again.Stdin)
	}
	// 原 stdin 不被就地修改（Clone 纪律）
	if in.Stdin != "do it" {
		t.Errorf("入参 stdin 不应被修改, got %q", in.Stdin)
	}
}

// TestTaskCloneDependsOn Task.Clone 深拷贝 DependsOn（写时复制纪律：hub 重启
// 恢复与事务副本不得共享底层数组）。
func TestTaskCloneDependsOn(t *testing.T) {
	src := Task{ID: "t-1", DependsOn: []string{"up-1", "up-2"}}
	cp := src.Clone()
	cp.DependsOn[0] = "mutated"
	cp.DependsOn = append(cp.DependsOn, "extra")
	if src.DependsOn[0] != "up-1" || len(src.DependsOn) != 2 {
		t.Errorf("Clone 应深拷贝 DependsOn, src=%v", src.DependsOn)
	}
	// nil 依赖保持 nil（JSON 反序列化旧快照口径）
	var nilTask Task
	if c := nilTask.Clone(); c.DependsOn != nil {
		t.Errorf("nil DependsOn 克隆后应保持 nil, got %v", c.DependsOn)
	}
}

// TestBlockedReasonFormats 门控标注构造与格式（SPEC 4.12）。
func TestBlockedReasonFormats(t *testing.T) {
	if got := BlockedByReason("up-1"); got != "blocked_by:up-1" {
		t.Errorf("blocked_by 格式不符: %q", got)
	}
	if got := UpstreamFailedReason("up-1"); got != "upstream_failed:up-1" {
		t.Errorf("upstream_failed 格式不符: %q", got)
	}
}
