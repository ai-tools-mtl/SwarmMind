package runner

import (
	"os"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// baseTestEnv node 配置基线（configs/node.example.yaml 口径）。
func baseTestEnv() map[string]string {
	return map[string]string{
		"PATH":   "/usr/bin:/bin",
		"TMPDIR": "/tmp",
		"LANG":   "en_US.UTF-8",
	}
}

// newRunner 构造测试 runner：workdir 独立临时目录，ConfigHome 空 → defaultHome。
func newRunner(t *testing.T, mutate func(p *protocol.ExecProfile)) (*Runner, string) {
	t.Helper()
	workdir := t.TempDir()
	home := t.TempDir()
	p := protocol.ExecProfile{
		Name: "test-agent", Kind: protocol.AgentKindCLI,
		Command: "/bin/sh", Workdir: workdir,
		TimeoutSec: 10, AllowArgs: true, AllowEnv: []string{"TMPDIR"},
		Exposed: true,
	}
	if mutate != nil {
		mutate(&p)
	}
	return New(p, baseTestEnv(), home), workdir
}

func simpleTask(args ...string) protocol.Task {
	return protocol.Task{
		ID:      "t-" + strings.Repeat("a", 32),
		TraceID: "trace-x",
		Input:   protocol.TaskInput{Args: args},
	}
}

// TestRunBasicEcho 基础回声：FinalAnswer、stdout tail、退出码、终态。
func TestRunBasicEcho(t *testing.T) {
	r, _ := newRunner(t, nil)
	task := simpleTask("-c", `echo hello swarmmind`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted || res.ExitCode != 0 {
		t.Fatalf("应 completed/0, got %s/%d err=%q", res.Status, res.ExitCode, res.Error)
	}
	if res.FinalAnswer != "hello swarmmind\n" {
		t.Errorf("FinalAnswer = %q", res.FinalAnswer)
	}
	if res.StdoutTail != "hello swarmmind\n" {
		t.Errorf("StdoutTail = %q", res.StdoutTail)
	}
	if res.StartedAt.IsZero() || res.FinishedAt.Before(res.StartedAt) {
		t.Errorf("时间戳非法: %v → %v", res.StartedAt, res.FinishedAt)
	}
}

// TestRunStdin stdin 传递且写后关闭（cat 读到 EOF 正常退出）。
func TestRunStdin(t *testing.T) {
	r, _ := newRunner(t, nil)
	task := simpleTask("-c", `cat`)
	task.Input.Stdin = "stdin-payload\n"
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q", res.Status, res.Error)
	}
	if res.FinalAnswer != "stdin-payload\n" {
		t.Errorf("stdin 未传达到 stdout: %q", res.FinalAnswer)
	}
}

// TestRunStdinToArg StdinToArg 正常态：Args 空时小体量 stdin 整体作为单个尾参
// 追加（args 型 CLI 兼底，2026-10-23）。
func TestRunStdinToArg(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) {
		p.StdinToArg = true
		p.AgentArgs = []string{"-c", `printf "arg:%s stdin:%s\n" "$1" "$(cat)"`, "--"}
	})
	task := simpleTask()
	task.Input.Stdin = "payload-text"
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q", res.Status, res.Error)
	}
	if res.FinalAnswer != "arg:payload-text stdin:payload-text\n" {
		t.Errorf("stdin 未转为尾参: %q", res.FinalAnswer)
	}
}

// TestRunStdinToArgTooLong StdinToArg 长度防御：超 stdinToArgMax 不转参不 spawn，
// failed/policy_denied + stderr_tail 写明原因与出路（平台单参长度硬约束，P2-9）。
func TestRunStdinToArgTooLong(t *testing.T) {
	r, workdir := newRunner(t, func(p *protocol.ExecProfile) {
		p.StdinToArg = true
		p.AgentArgs = []string{"-c", `touch should-not-exist`, "--"}
	})
	task := simpleTask()
	task.Input.Stdin = strings.Repeat("x", stdinToArgMax+1)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultFailed || res.Error != protocol.CodePolicyDenied {
		t.Fatalf("应 failed/policy_denied, got %s/%q", res.Status, res.Error)
	}
	if !strings.Contains(res.StderrTail, "stdin 过长无法转为命令行参数") || !strings.Contains(res.StderrTail, "请改用任务 args") {
		t.Errorf("stderr_tail 应可读说明原因与出路: %q", res.StderrTail)
	}
	if kb := (len(task.Input.Stdin) + 1023) / 1024; !strings.Contains(res.StderrTail, fmt.Sprintf("%d KB > 上限 %d KB", kb, stdinToArgMax/1024)) {
		t.Errorf("stderr_tail 应含进位取整 KB 对比: %q", res.StderrTail)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("未执行退出码应为 -1, got %d", res.ExitCode)
	}
	if _, err := os.Stat(filepath.Join(workdir, "should-not-exist")); !os.IsNotExist(err) {
		t.Error("超长 stdin 不得执行进程")
	}
}

// TestRunArgsOrder 参数拼接顺序：AgentArgs 在前、任务 Args 在后（SPEC 4.9）。
func TestRunArgsOrder(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) {
		p.AgentArgs = []string{"-c", `printf "agent:%s task:%s\n" "$1" "$2"`, "--"}
	})
	res := r.Run(context.Background(), simpleTask("AA", "BB"))
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q", res.Status, res.Error)
	}
	// argv = [sh, -c, script, --, AA, BB]：$1=AA $2=BB（AgentArgs 的 -- 分隔）
	if res.FinalAnswer != "agent:AA task:BB\n" {
		t.Errorf("参数顺序不符: %q", res.FinalAnswer)
	}
}

// TestRunEnvBaseline env 基线：PATH/TMPDIR/LANG 来自 base_env，HOME=ConfigHome
//（ConfigHome 空时 defaultHome），daemon 环境不泄漏。
func TestRunEnvBaseline(t *testing.T) {
	t.Setenv("SWARMMIND_DAEMON_SECRET", "leak-marker") // daemon 环境（不得进入子进程）

	home := t.TempDir()
	r, _ := newRunner(t, func(p *protocol.ExecProfile) { p.ConfigHome = home })
	task := simpleTask("-c", `printf "HOME=%s\nPATH=%s\nTMPDIR=%s\nLANG=%s\nSECRET=%s\n" "$HOME" "$PATH" "$TMPDIR" "$LANG" "$SWARMMIND_DAEMON_SECRET"`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q tail=%q", res.Status, res.Error, res.StderrTail)
	}
	want := "HOME=" + home + "\nPATH=/usr/bin:/bin\nTMPDIR=/tmp\nLANG=en_US.UTF-8\nSECRET=\n"
	if res.FinalAnswer != want {
		t.Errorf("env 基线不符:\n got %q\nwant %q", res.FinalAnswer, want)
	}
	// HOME 指向 ConfigHome 目录（且被按需创建）
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		t.Errorf("ConfigHome 未按需创建: %v", err)
	}
}

// TestRunEnvDefaultHome ConfigHome 为空时 HOME=defaultHome（data_dir/home 口径）。
func TestRunEnvDefaultHome(t *testing.T) {
	fallback := t.TempDir()
	workdir := t.TempDir()
	p := protocol.ExecProfile{
		Name: "a", Kind: protocol.AgentKindCLI,
		Command: "/bin/sh", Workdir: workdir, TimeoutSec: 5, AllowArgs: true,
	}
	r := New(p, baseTestEnv(), fallback)
	res := r.Run(context.Background(), simpleTask("-c", `echo $HOME`))
	if res.FinalAnswer != fallback+"\n" {
		t.Errorf("HOME 应为 defaultHome %q, got %q", fallback, res.FinalAnswer)
	}
}

// TestRunEnvWhitelistOverride 白名单内覆盖生效（TMPDIR 在 allow_env）。
func TestRunEnvWhitelistOverride(t *testing.T) {
	r, _ := newRunner(t, nil)
	custom := t.TempDir()
	task := simpleTask("-c", `echo $TMPDIR`)
	task.Input.Env = map[string]string{"TMPDIR": custom}
	res := r.Run(context.Background(), task)
	if res.FinalAnswer != custom+"\n" {
		t.Errorf("白名单覆盖未生效: %q", res.FinalAnswer)
	}
}

// TestRunPolicyDeniedEnv 越界 env 直接 failed/policy_denied 且不执行（SPEC 五）。
func TestRunPolicyDeniedEnv(t *testing.T) {
	r, workdir := newRunner(t, nil)
	marker := filepath.Join(workdir, "should-not-exist")
	task := simpleTask("-c", `touch should-not-exist`)
	task.Input.Env = map[string]string{"NOT_ALLOWED": "1"}
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultFailed || res.Error != protocol.CodePolicyDenied {
		t.Fatalf("应 failed/policy_denied, got %s/%q", res.Status, res.Error)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("未执行退出码应为 -1, got %d", res.ExitCode)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("policy_denied 不得执行进程")
	}
}

// TestRunPolicyDeniedPATH PATH 未显式列入 allow_env 时覆盖 PATH 同样拒绝（DoD 9）。
func TestRunPolicyDeniedPATH(t *testing.T) {
	r, _ := newRunner(t, nil) // allow_env 只有 TMPDIR
	task := simpleTask("-c", `true`)
	task.Input.Env = map[string]string{"PATH": "/evil"}
	res := r.Run(context.Background(), task)
	if res.Error != protocol.CodePolicyDenied {
		t.Errorf("PATH 未列入白名单应 policy_denied, got %q", res.Error)
	}
}

// TestRunPolicyDeniedHOME HOME 由 runner 固定管理，任务覆盖 HOME 拒绝。
func TestRunPolicyDeniedHOME(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) { p.AllowEnv = []string{"*"} })
	task := simpleTask("-c", `true`)
	task.Input.Env = map[string]string{"HOME": "/evil"}
	res := r.Run(context.Background(), task)
	if res.Error != protocol.CodePolicyDenied {
		t.Errorf("覆盖 HOME 应 policy_denied（runner 固定管理）, got %q", res.Error)
	}
}

// TestRunAllowArgsDeny allow_args=false 时非空 args → policy_denied 不执行。
func TestRunAllowArgsDeny(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) { p.AllowArgs = false })
	res := r.Run(context.Background(), simpleTask("arg1"))
	if res.Status != protocol.ResultFailed || res.Error != protocol.CodePolicyDenied {
		t.Fatalf("应 failed/policy_denied, got %s/%q", res.Status, res.Error)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("未执行退出码应为 -1, got %d", res.ExitCode)
	}
	// 空 args 合法
	res = r.Run(context.Background(), simpleTask())
	if res.Status != protocol.ResultCompleted {
		t.Errorf("allow_args=false 空参数应可执行, got %s/%q", res.Status, res.Error)
	}
}

// TestRunTimeoutKillsProcessGroup 超时：failed + error 含 timeout + 进程组整组
// 终止无幸存（pgrep -g 验证，DoD 4）。
func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) { p.TimeoutSec = 1 })

	// sh 打印自身 PID（=pgid，Setpgid 置组长）后派生两个后台 sleep 同组存活
	task := simpleTask("-c", `echo $$; sleep 30 & sleep 30 & wait`)
	start := time.Now()
	res := r.Run(context.Background(), task)
	elapsed := time.Since(start)

	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, protocol.CodeTimeout) {
		t.Fatalf("应 failed 且 error 含 timeout, got %s/%q", res.Status, res.Error)
	}
	if elapsed > 8*time.Second {
		t.Errorf("超时应约 1s 触发（+5s WaitDelay 上限）, took %v", elapsed)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("信号终止退出码应为 -1, got %d", res.ExitCode)
	}
	// pgid = sh 的 pid（组长）
	pgid := strings.TrimSpace(res.FinalAnswer)
	if pgid == "" {
		t.Fatal("未取得 pgid（stdout 采集失败）")
	}
	// 整组无幸存：轮询 pgrep -g <pgid> 至多 3s；若 sleep 幸存即为整组终止失败
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := exec.Command("/usr/bin/pgrep", "-g", pgid).Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			break // 无幸存
		}
		if time.Now().After(deadline) {
			t.Fatalf("进程组 %s 仍有幸存: %s", pgid, strings.TrimSpace(string(out)))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRunNormalReturnReapsProcessGroup 正常返回路径整组收尾（P1.5 二轮必修3）：
// 主进程立即退出、后台派生进程（stdio 已重定向，不触发 WaitDelay 管道等待）继续
// 存活——Run 返回时整组必须已被终止，产物扫描/互斥释放前目录无组内写入者。
func TestRunNormalReturnReapsProcessGroup(t *testing.T) {
	r, _ := newRunner(t, nil)

	// sh 打印自身 PID（=pgid，Setpgid 置组长）后派生 30s 后台 sleep 并立即退出；
	// sleep 重定向三路 stdio，模拟 daemon 化孙进程（不持有管道）
	task := simpleTask("-c", `echo $$; sleep 30 >/dev/null 2>&1 </dev/null & exit 0`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("主进程正常退出应 completed, got %s/%q", res.Status, res.Error)
	}
	pgid := strings.TrimSpace(res.FinalAnswer)
	if pgid == "" {
		t.Fatal("未取得 pgid（stdout 采集失败）")
	}
	// Run 返回即应收尾完成：立即探测无幸存（无收尾时 sleep 30 仍存活）
	if out, err := exec.Command("/usr/bin/pgrep", "-g", pgid).Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		t.Fatalf("正常返回后进程组 %s 仍有幸存: %s", pgid, strings.TrimSpace(string(out)))
	}
}

// TestRunFinalAnswerTruncation FinalAnswer = stdout 全文清洗截断至 512KiB 并注记
// truncated；stdout tail 为末尾 64KiB（SPEC 4.9）。
func TestRunFinalAnswerTruncation(t *testing.T) {
	r, _ := newRunner(t, nil)
	// 600_000 字节 'a'，后 100 字节 'z'（tail 可辨识）
	task := simpleTask("-c", `head -c 600000 /dev/zero | tr "\0" a; head -c 100 /dev/zero | tr "\0" z`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("截断不改变 completed 终态, got %s/%q", res.Status, res.Error)
	}
	if len(res.FinalAnswer) != protocol.FinalAnswerLimit {
		t.Fatalf("FinalAnswer 应截断至 %d, got %d", protocol.FinalAnswerLimit, len(res.FinalAnswer))
	}
	if !strings.Contains(res.Error, protocol.CodeTruncated) {
		t.Errorf("error 应注记 truncated, got %q", res.Error)
	}
	if !strings.HasPrefix(res.FinalAnswer, "aaaa") {
		t.Error("FinalAnswer 应为 stdout 头部")
	}
	if len(res.StdoutTail) != protocol.TailLimit {
		t.Errorf("stdout tail 应为末尾 %d 字节, got %d", protocol.TailLimit, len(res.StdoutTail))
	}
	if !strings.HasSuffix(res.StdoutTail, strings.Repeat("z", 100)) {
		t.Error("stdout tail 应为输出末尾（含 'z' 尾段）")
	}
}

// TestRunTailCap stderr 超 64KiB 截为末尾 64KiB。
func TestRunTailCap(t *testing.T) {
	r, _ := newRunner(t, nil)
	task := simpleTask("-c", `head -c 200000 /dev/zero | tr "\0" e >&2`)
	res := r.Run(context.Background(), task)
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("stderr 溢出不影响 completed, got %s", res.Status)
	}
	if len(res.StderrTail) != protocol.TailLimit {
		t.Errorf("stderr tail 应为 %d 字节, got %d", protocol.TailLimit, len(res.StderrTail))
	}
	if !strings.HasPrefix(res.StderrTail, "ee") || !strings.HasSuffix(res.StderrTail, "ee") {
		t.Error("stderr tail 内容应为 'e' 填充")
	}
}

// TestRunUTF8Sanitize 非法 UTF-8 字节清洗为 U+FFFD（SPEC 4.9）。
// 用八进制 \377（POSIX 保证）：dash 0.5.12（ubuntu-latest 的 /bin/sh）不支持 \xHH，
// 会输出字面文本导致非法字节从未产生（CI 首日全红根因，2026-10-24 诊断实锤）。
func TestRunUTF8Sanitize(t *testing.T) {
	r, _ := newRunner(t, nil)
	task := simpleTask("-c", `printf 'ok\377bad'`)
	res := r.Run(context.Background(), task)
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s", res.Status)
	}
	if want := "ok\uFFFDbad"; res.FinalAnswer != want {
		t.Errorf("非法字节应替换 U+FFFD: got %q want %q", res.FinalAnswer, want)
	}
}

// TestRunSpawnFailure 命令不存在 → failed/spawn_failed，退出码 -1。
func TestRunSpawnFailure(t *testing.T) {
	r, _ := newRunner(t, func(p *protocol.ExecProfile) { p.Command = "/no/such/binary" })
	res := r.Run(context.Background(), simpleTask())
	if res.Status != protocol.ResultFailed || !strings.Contains(res.Error, "spawn_failed") {
		t.Fatalf("应 failed/spawn_failed, got %s/%q", res.Status, res.Error)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("退出码应为 -1, got %d", res.ExitCode)
	}
}

// TestRunCancelKillsProcessGroup 调用方取消 ctx（服务关闭打断在途执行的底层
// 机制，SPEC 五/4.8）：kill(-pgid) 整组终止无幸存，Run 立即返回、不等待自然
// 结束（对照 30s sleep）。
func TestRunCancelKillsProcessGroup(t *testing.T) {
	r, workdir := newRunner(t, func(p *protocol.ExecProfile) { p.TimeoutSec = 60 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// sh 打印自身 PID（=pgid，Setpgid 置组长）→ ready.flag 标记 spawn 完成 →
	// 两个后台 sleep 同组存活；取消 ctx 模拟服务关闭打断。
	task := simpleTask("-c", `echo $$; touch ready.flag; sleep 30 & sleep 30 & wait`)
	type runOut struct{ res protocol.TaskResult }
	done := make(chan runOut, 1)
	go func() { done <- runOut{r.Run(ctx, task)} }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workdir, "ready.flag")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("等待 spawn 就绪超时")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	cancel()
	res := (<-done).res
	elapsed := time.Since(start)

	if res.Status != protocol.ResultFailed {
		t.Fatalf("取消后应 failed 终态（service 层覆写 needs_review）, got %s/%q", res.Status, res.Error)
	}
	if res.ExitCode != protocol.ExitCodeNotRun {
		t.Errorf("信号终止退出码应为 -1, got %d", res.ExitCode)
	}
	if elapsed > 8*time.Second {
		t.Errorf("取消应立即整组终止（+5s WaitDelay 上限）, took %v", elapsed)
	}
	// 整组无幸存：pgid = sh 的 pid（stdout 首行），轮询 pgrep -g 至多 3s
	pgid := strings.TrimSpace(res.FinalAnswer)
	if pgid == "" {
		t.Fatal("未取得 pgid（stdout 采集失败）")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		out, err := exec.Command("/usr/bin/pgrep", "-g", pgid).Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			break // 无幸存
		}
		if time.Now().After(deadline) {
			t.Fatalf("取消后进程组 %s 仍有幸存: %s", pgid, strings.TrimSpace(string(out)))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRunSwarmEnvInjection runner 系统变量注入（SPEC 4.9 产物链前置）：agent 经
// SWARM_TASK_ID/SWARM_OUTPUTS_DIR/SWARM_TRACE_ID 得知任务标识与产物输出目录。
func TestRunSwarmEnvInjection(t *testing.T) {
	r, workdir := newRunner(t, nil)
	task := simpleTask("-c", `printf "%s\n%s\n%s\n" "$SWARM_TASK_ID" "$SWARM_OUTPUTS_DIR" "$SWARM_TRACE_ID"`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q tail=%q", res.Status, res.Error, res.StderrTail)
	}
	want := task.ID + "\n" + filepath.Join(workdir, "outputs", task.ID) + "\n" + task.TraceID + "\n"
	if res.FinalAnswer != want {
		t.Errorf("系统变量注入不符:\n got %q\nwant %q", res.FinalAnswer, want)
	}
}

// TestRunSwarmEnvNotOverridable 任务 env 试图覆盖系统变量：即便键已列入
// allow_env，子进程仍见 runner 注入值（系统变量最后写入，SWARM_* 覆盖丢弃）。
func TestRunSwarmEnvNotOverridable(t *testing.T) {
	r, workdir := newRunner(t, func(p *protocol.ExecProfile) {
		p.AllowEnv = []string{"TMPDIR", "SWARM_TASK_ID", "SWARM_OUTPUTS_DIR", "SWARM_TRACE_ID"}
	})
	task := simpleTask("-c", `printf "%s|%s|%s\n" "$SWARM_TASK_ID" "$SWARM_OUTPUTS_DIR" "$SWARM_TRACE_ID"`)
	task.Input.Env = map[string]string{
		"SWARM_TASK_ID":     "t-evil",
		"SWARM_OUTPUTS_DIR": "/evil/outputs",
		"SWARM_TRACE_ID":    "trace-evil",
	}
	res := r.Run(context.Background(), task)

	// 白名单内的覆盖尝试不触发 policy_denied（白名单只管任务侧覆盖的准入），
	// 只是注入值不生效
	if res.Status != protocol.ResultCompleted {
		t.Fatalf("白名单内的 SWARM_* 覆盖应放行执行, got %s/%q", res.Status, res.Error)
	}
	want := task.ID + "|" + filepath.Join(workdir, "outputs", task.ID) + "|" + task.TraceID + "\n"
	if res.FinalAnswer != want {
		t.Errorf("系统变量被任务 env 改写:\n got %q\nwant %q", res.FinalAnswer, want)
	}
}

// TestRunSwarmOutputsDirRoundTrip 产物链闭环：agent 仅凭 SWARM_OUTPUTS_DIR 写
// 产物，进程结束后 runner 扫描登记（SPEC 4.9 产物发现约定对 agent 可见）。
func TestRunSwarmOutputsDirRoundTrip(t *testing.T) {
	r, _ := newRunner(t, nil)
	task := simpleTask("-c", `mkdir -p "$SWARM_OUTPUTS_DIR" && printf payload > "$SWARM_OUTPUTS_DIR/answer.txt"`)
	res := r.Run(context.Background(), task)

	if res.Status != protocol.ResultCompleted {
		t.Fatalf("应 completed, got %s err=%q tail=%q", res.Status, res.Error, res.StderrTail)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("应登记 1 条产物, got %d", len(res.Artifacts))
	}
	a := res.Artifacts[0]
	if a.Path != "outputs/"+task.ID+"/answer.txt" || a.Size != 7 || a.Sha256 != wantSha(t, "payload") {
		t.Errorf("产物登记不符: %+v", a)
	}
}
