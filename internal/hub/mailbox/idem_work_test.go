package mailbox

import (
	"errors"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// workBindTaskReq 绑定派发请求捷径（指纹用例）。
func workBindTaskReq(work string, step int64, term string) protocol.TaskSubmitRequest {
	return protocol.TaskSubmitRequest{
		TargetNode:  "n1",
		TargetAgent: "echo",
		Input:       protocol.TaskInput{Args: []string{"run"}},
		SubmittedBy: "tester",
		IdemKey:     "k1",
		WorkRef:     work,
		StepSeq:     step,
		PrimeTerm:   term,
	}
}

// seedWorkBoundTask 预置一条已落库的绑定任务（idem=k1，指定绑定形态）。
func seedWorkBoundTask(t *testing.T, st *store.State, work string, step int64) protocol.Task {
	t.Helper()
	task := protocol.Task{
		ID: "t-bound", TraceID: "tr", TargetNode: "n1", TargetAgent: "echo",
		Input:       protocol.EffectiveInput(protocol.TaskInput{Args: []string{"run"}}, nil),
		SubmittedBy: "tester", IdemKey: "k1",
		WorkRef: work, WorkStep: step, AttemptRef: work + "#a1",
		Status: protocol.StatusSubmitted,
	}
	st.Tasks[task.ID] = task
	return task
}

// TestWorkBindingFingerprint W2 指纹规范形：绑定入指纹（同键换 work/换步骤=
// 冲突，区别于群引用「换群=新任务」）；prime_term 不入指纹（换任期重放命中
// dup）；未绑定请求间互不受结构扩展影响。
func TestWorkBindingFingerprint(t *testing.T) {
	st := store.NewState()
	orig := seedWorkBoundTask(t, st, "w-1", 2)

	// 同键同内容同绑定重放 → dup
	if id, dup, err := IdemCheck(st, workBindTaskReq("w-1", 2, "prime-1")); err != nil || !dup || id != orig.ID {
		t.Fatalf("同键同绑定应 dup: id=%s dup=%v err=%v", id, dup, err)
	}
	// 换任期声明重放 → 仍 dup（任期归属标签非内容，不入指纹）
	if id, dup, err := IdemCheck(st, workBindTaskReq("w-1", 2, "prime-2")); err != nil || !dup || id != orig.ID {
		t.Fatalf("换任期重放应 dup: id=%s dup=%v err=%v", id, dup, err)
	}
	// 同键换工作单 → 冲突（绑定是执行去重关键面，不落新任务）
	if _, dup, err := IdemCheck(st, workBindTaskReq("w-2", 2, "prime-1")); dup || !errors.Is(err, ErrConflict) {
		t.Fatalf("同键换工作单应冲突: dup=%v err=%v", dup, err)
	}
	// 同键换步骤 → 冲突
	if _, dup, err := IdemCheck(st, workBindTaskReq("w-1", 3, "prime-1")); dup || !errors.Is(err, ErrConflict) {
		t.Fatalf("同键换步骤应冲突: dup=%v err=%v", dup, err)
	}
	// 同键未绑定 → 冲突（带与不带绑定不同指纹）
	unbound := workBindTaskReq("w-1", 2, "prime-1")
	unbound.WorkRef, unbound.StepSeq, unbound.PrimeTerm = "", 0, ""
	if _, dup, err := IdemCheck(st, unbound); dup || !errors.Is(err, ErrConflict) {
		t.Fatalf("同键带绑定与不带应冲突: dup=%v err=%v", dup, err)
	}

	// 未绑定请求（同键）与既有绑定任务不互相命中（冲突），未绑定重放间仍 dup
	st.Tasks["t-plain"] = protocol.Task{
		ID: "t-plain", TraceID: "tr", TargetNode: "n1", TargetAgent: "echo",
		Input:       protocol.EffectiveInput(protocol.TaskInput{Args: []string{"run"}}, nil),
		SubmittedBy: "tester", IdemKey: "k2", Status: protocol.StatusSubmitted,
	}
	plain := unbound
	plain.IdemKey = "k2"
	if id, dup, err := IdemCheck(st, plain); err != nil || !dup || id != "t-plain" {
		t.Fatalf("未绑定重放应 dup 命中原任务: id=%s dup=%v err=%v", id, dup, err)
	}
	plainWithWork := plain
	plainWithWork.WorkRef, plainWithWork.StepSeq, plainWithWork.PrimeTerm = "w-1", 2, "prime-1"
	if _, dup, err := IdemCheck(st, plainWithWork); dup || !errors.Is(err, ErrConflict) {
		t.Fatalf("未绑定任务遇绑定重放应冲突: dup=%v err=%v", dup, err)
	}
}
