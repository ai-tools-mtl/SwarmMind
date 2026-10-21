package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// sleeperProfile 长任务执行器：TimeoutSec 远大于测试时长，保证打断只能来自
// 服务关闭（Close），而非超时。
func sleeperProfile(workdir string) protocol.ExecProfile {
	return protocol.ExecProfile{
		Name: "sleep-demo", Kind: protocol.AgentKindCLI,
		Command: "/bin/sh", Workdir: workdir,
		TimeoutSec: 60, AllowArgs: true, Exposed: true,
		PermissionMode: "no-shell",
	}
}

// sleeperTask 30s 长任务：echo 打印自身 PID（=pgid，Setpgid 置组长）作关闭后
// 取证；两个后台 sleep 同组存活，模拟实测缺陷中的派生子进程。
func sleeperTask(id string) protocol.Task {
	return protocol.Task{
		ID: id, TraceID: "tr", TargetNode: "n1", TargetAgent: "sleep-demo",
		Input: protocol.TaskInput{Args: []string{"-c", `echo $$; touch ready.flag; sleep 30 & sleep 30 & wait`}},
		SubmittedBy: "hub", Status: protocol.StatusOffered,
	}
}

// TestCloseInterruptsInflightNeedsReview 优雅关闭打断在途执行（SPEC 4.8，P2
// 实测缺陷回归）：Close → 任务 ctx 取消 → kill(-pgid) 子进程整组终止 → 账本写
// finished（needs_review/node_restarted_midway）并回传 task_result；绝不等待
// 自然跑完、绝不以 completed 收尾/回传。
func TestCloseInterruptsInflightNeedsReview(t *testing.T) {
	workdir := t.TempDir()
	f := newFixture(t, []protocol.ExecProfile{sleeperProfile(workdir)})
	task := sleeperTask("t-shutdown")
	f.sendOffer(t, task)

	// 等 started 落盘 + spawn 完成（ready.flag 由任务进程自己落盘，避免
	// task_started 帧（spawn 前发）与实际 spawn 之间的竞态）
	if !waitFor(5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(workdir, "ready.flag"))
		return err == nil
	}) {
		t.Fatal("等待 spawn 就绪超时")
	}

	start := time.Now()
	f.svc.Close() // 幂等：fixture Cleanup 重复 Close 无副作用
	elapsed := time.Since(start)

	if elapsed > 15*time.Second {
		t.Fatalf("Close 应打断在途执行而非等自然结束（sleep 30s）, took %v", elapsed)
	}
	// 账本收尾：finished + needs_review/node_restarted_midway，绝不 completed
	e, ok := f.svc.cfg.Ledger.Get(task.ID)
	if !ok || e.State != ledger.StateFinished {
		t.Fatalf("账本应 finished: ok=%v %+v", ok, e)
	}
	if e.Result == nil || e.Result.Status != protocol.ResultNeedsReview ||
		e.Result.Error != protocol.CodeNodeRestartedMidway {
		t.Fatalf("收尾结果应为 needs_review/node_restarted_midway: %+v", e.Result)
	}
	if e.Result.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("信号终止退出码应为 -1, got %d", e.Result.ExitCode)
	}
	if e.ResultAcked {
		t.Error("关闭期结果尚未被 ack")
	}
	// 回传：task_result 帧已发出（membus 在线），且全链路无 completed 终态帧
	var sent *protocol.TaskResultPayload
	for _, env := range f.ofTypes(protocol.MsgTaskResult) {
		var p protocol.TaskResultPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.TaskID != task.ID {
			continue
		}
		if p.Status == protocol.ResultCompleted {
			t.Fatalf("被打断的任务不得以 completed 收尾/回传: %+v", p)
		}
		sent = &p
	}
	if sent == nil || sent.Status != protocol.ResultNeedsReview {
		t.Fatalf("needs_review 结果应已回传: %+v", sent)
	}
	// 子进程整组终止无幸存：pgid = sh 的 pid（stdout 首行保留进 FinalAnswer），
	// 轮询 pgrep -g 至多 3s
	pgid := strings.TrimSpace(e.Result.FinalAnswer)
	if pgid == "" {
		t.Fatal("未取得 pgid（部分 stdout 未保留进收尾结果）")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := exec.Command("/usr/bin/pgrep", "-g", pgid).Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			break // 无幸存
		}
		if time.Now().After(deadline) {
			t.Fatalf("关闭后进程组 %s 仍有幸存: %s", pgid, strings.TrimSpace(string(out)))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
