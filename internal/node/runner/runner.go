// Package runner 实现 CLI adapter（SPEC 五执行环境规则/4.9）：spawn、进程组治理、
// 超时整组终止、stdin 传递、干净基线 env + 白名单覆盖 + SWARM_* 系统变量注入、
// stdout/stderr tail、FinalAnswer 提取与产物扫描。
//
// 权限边界（诚实声明，SPEC 4.9）：runner 的权限边界 = 进程启动权 + env 白名单 +
// ExecProfile 固定命令/参数；agent 自身能做什么由其自带权限机制管（AgentArgs
// 映射），调用方以 Card.PermissionMode 为准。
//
// 调用时序（SPEC 4.7）：调用方须先完成 started 落盘并发 task_started 后才调用
// Run；本包只负责一次进程生命周期，不做调度与账本。
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// waitDelay 超时/取消后等待管道排干的兜底时长（SPEC 五：WaitDelay=5s 兑底）。
const waitDelay = 5 * time.Second

// swarmEnvPrefix runner 系统变量命名空间（SPEC 4.9 产物链）：SWARM_TASK_ID、
// SWARM_OUTPUTS_DIR、SWARM_TRACE_ID。由 runner 在 spawn 前注入，是任务定位
// 信息而非任务侧覆盖，不受 allow_env 白名单约束，任务 env 不可改写。
const swarmEnvPrefix = "SWARM_"

// stdinToArgMax StdinToArg 追加尾参的单参字节上限（128KiB 保守值）：平台 execve
// 对单个参数长度有硬约束（Linux MAX_ARG_STRLEN=128KiB，macOS/Windows 各有更紧
// 总量上限），超限 spawn 必败（E2BIG）——任务直接 failed，提示改用任务 args。
const stdinToArgMax = 128 << 10

// Runner 某一 agent（ExecProfile）的任务执行器。
type Runner struct {
	profile protocol.ExecProfile
	baseEnv []string // "K=V" 序列（node 配置 base_env，不继承 daemon 环境）
	homeDir string   // HOME 固定值：ConfigHome，空则 defaultHome（data_dir/home）
	// spawnHook spawn 成功回调（maturity-orphan-guard）：service 装配 OnSpawn
	// 后经 SetSpawnHook 注入，Run 在子进程 Start 后同步调用（pgid、组长启动指纹
	// 与启动周期标识即时可用）。atomic 存取：同一 Runner 并发服务多任务，Store
	// 幂等（service 对同一 OnSpawn 重复 Store 等值覆盖），Load 无锁。
	spawnHook atomic.Pointer[func(task protocol.Task, pgid, procStart int64, bootIdent string)]
}

// SetSpawnHook 注册 spawn 成功回调（maturity-orphan-guard）：pgid 为执行进程组
// ID（Unix=子进程 pid；Windows 恒 0——Job 兑底，service 侧自然不落盘）；
// procStart 为组长启动时刻指纹、bootIdent 为系统启动周期标识（R2 必修1：同周期
// +指纹等值才是同一实例）。回调在 Run 的执行 goroutine 内同步调用，勿做耗时
// 操作（只供账本落盘）。
func (r *Runner) SetSpawnHook(h func(task protocol.Task, pgid, procStart int64, bootIdent string)) {
	r.spawnHook.Store(&h)
}

// New 构造 runner。baseEnv 为 node 配置的执行环境基线（PATH/TMPDIR/LANG 等）；
// defaultHome 为 ExecProfile.ConfigHome 为空时的 HOME 兜底（data_dir/home）。
func New(profile protocol.ExecProfile, baseEnv map[string]string, defaultHome string) *Runner {
	env := make([]string, 0, len(baseEnv)+1)
	for k, v := range baseEnv {
		env = append(env, k+"="+v)
	}
	home := profile.ConfigHome
	if home == "" {
		home = defaultHome
	}
	return &Runner{profile: profile, baseEnv: env, homeDir: home}
}

// Run 执行一次任务（SPEC 五）：策略检查（不执行即拒）→ 干净 env 组装 → spawn
// （独立进程组）→ stdin 写入后关闭 → 等待/超时整组终止（正常返回路径同样整组
// 收尾，后台派生进程不越出 Run 存活）→ 采集与产物扫描。
// ctx 是任务执行的中止开关（TimeoutSec 仍从它派生，SPEC 五：唯一超时源）——
// 取消即 kill(-pgid) 整组终止（含服务关闭打断在途执行的场景）。返回终态
// TaskResult，status ∈ completed|failed（needs_review 只产生于重启恢复与
// service 关闭收尾，不由本包产生：调用方取消 ctx 时 service 以
// needs_review/node_restarted_midway 覆写本包结果）。返回前进程必已被 Wait
// （CLAUDE.md：kill 后必须 Wait）。
func (r *Runner) Run(ctx context.Context, task protocol.Task) protocol.TaskResult {
	now := protocol.NowUTC()
	res := protocol.TaskResult{StartedAt: now, ExitCode: protocol.ExitCodeNotRun}
	finish := func() protocol.TaskResult {
		res.FinishedAt = protocol.NowUTC()
		return res
	}

	// 策略检查：越界不执行（SPEC 五：任务直接 failed，error=policy_denied）
	if !r.profile.AllowArgs && len(task.Input.Args) > 0 {
		res.Status = protocol.ResultFailed
		res.Error = protocol.CodePolicyDenied
		return finish()
	}
	if bad, ok := deniedEnvKey(r.profile.AllowEnv, task.Input.Env); ok {
		res.Status = protocol.ResultFailed
		res.Error = protocol.CodePolicyDenied
		res.StderrTail = fmt.Sprintf("env key %q not in allow_env", bad)
		return finish()
	}

	// 组装参数：AgentArgs 在前，任务 Args 在后（SPEC 4.9 拼接顺序）。
	// 唯一超时源是 TimeoutSec（SPEC 五：任务执行 context 独立于会话）；
	// 调用方 ctx 取消（服务关闭）同样触发整组终止。
	if r.profile.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.profile.TimeoutSec)*time.Second)
		defer cancel()
	}
	argv := append(append([]string(nil), r.profile.AgentArgs...), task.Input.Args...)
	// StdinToArg 兼底（2026-10-23 伦哥实测抓出）：args 型 CLI（zcode -p 等）从
	// 控制台/表单收到的描述在 stdin 里，Args 空时旗标缺参必打 usage 退出——
	// 此时将 stdin 整体作为单个尾参追加（追加源是 stdin 非任务入参，不绕 AllowArgs 白名单）。
	// 超过 stdinToArgMax 不追加、不 spawn：超长单参平台必拒（E2BIG），与其让
	// spawn_failed 晕到用户，不如 failed + 可读说明（KB 按进位取整，不出现 N>N）。
	if r.profile.StdinToArg && len(task.Input.Args) == 0 && task.Input.Stdin != "" {
		if n := len(task.Input.Stdin); n > stdinToArgMax {
			res.Status = protocol.ResultFailed
			res.Error = protocol.CodePolicyDenied
			res.StderrTail = fmt.Sprintf("stdin 过长无法转为命令行参数（%d KB > 上限 %d KB），请改用任务 args 或缩小输入", (n+1023)/1024, stdinToArgMax/1024)
			return finish()
		}
		argv = append(argv, task.Input.Stdin)
	}
	cmd := exec.CommandContext(ctx, r.profile.Command, argv...)
	cmd.Dir = r.profile.Workdir
	cmd.Env = r.buildEnv(task)
	// spawn 前治理装配（Unix setpgid 独立进程组 / Windows 挂起启动+Job Object，
	// SPEC 五）：装配失败拒绝执行（返工必修4②：宁可任务 failed 也不放弃整组
	// 终止承诺，绝不静默降级）。
	if err := configureProcessGroup(cmd); err != nil {
		res.Status = protocol.ResultFailed
		res.Error = "job_setup_failed: " + err.Error()
		return finish()
	}
	cmd.Cancel = func() error {
		// 超时/取消：整组终止（Unix kill(-pgid) / Windows TerminateJobObject，
		// exec 默认只杀直接子进程）
		return killProcessGroup(cmd)
	}
	cmd.WaitDelay = waitDelay // 管道未排干时兜底强杀+关闭，防 Wait 永久阻塞

	// stdin：写完自动关闭（exec 内部拷贝协程负责）
	if task.Input.Stdin != "" {
		cmd.Stdin = strings.NewReader(task.Input.Stdin)
	}

	// stdout：全文采集（FinalAnswer 预算内）+ 末尾 64KiB；stderr：末尾 64KiB
	stdoutHead := &headCap{limit: protocol.FinalAnswerLimit}
	stdoutTail := &tailRing{capacity: protocol.TailLimit}
	stderrTail := &tailRing{capacity: protocol.TailLimit}
	cmd.Stdout = io.MultiWriter(stdoutHead, stdoutTail)
	cmd.Stderr = stderrTail

	// HOME 目录按需创建（agent 配置/凭证目录，与 workdir 分离）
	if r.homeDir != "" {
		_ = os.MkdirAll(r.homeDir, 0o755)
	}

	runErr := launchGoverned(cmd, func() {
		// spawn 成功即回调（maturity-orphan-guard）：pgid+procStart 尽早落盘，
		// 缩小崩溃窗口。
		if h := r.spawnHook.Load(); h != nil {
			func() {
				defer func() { _ = recover() }() // 回调异常不击穿任务执行（账本落盘失败已由 service 降级处理）
				p, s := spawnIdentity(cmd)
				(*h)(task, p, s, currentBootIdent())
			}()
		}
	})
	timedOut := ctx.Err() != nil  // Cancel 路径下 Run 可能回传信号错误，以 ctx 为准
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode() // 信号终止/未运行 = -1
	}
	if cmd.Process != nil && cmd.ProcessState != nil {
		// 整组收尾（含正常返回路径，P1.5 二轮必修3）：后台派生进程一并终止并
		// 等待全组退出——产物扫描及调用方随后释放的 workdir 互斥都以此为前提，
		// 目录在 Run 返回后不得再有组内写入者。
		reapProcessGroup(cmd)
	}

	switch {
	case timedOut:
		res.Status = protocol.ResultFailed
		res.Error = protocol.CodeTimeout
	case runErr != nil:
		res.Status = protocol.ResultFailed
		if cmd.ProcessState == nil {
			// 未成功 spawn（命令不存在/workdir 缺失等）
			res.Error = "spawn_failed: " + runErr.Error()
			return finish()
		}
		// 非零退出：failed，ExitCode 已记录
	default:
		res.Status = protocol.ResultCompleted
	}

	// FinalAnswer = stdout 全文（UTF-8 清洗 → 截断 512KiB，SPEC 4.9）
	res.FinalAnswer = sanitizeLimit(stdoutHead.bytes(), protocol.FinalAnswerLimit)
	if stdoutHead.truncated {
		res.Error = appendNote(res.Error, protocol.CodeTruncated)
	}
	res.StdoutTail = sanitizeLimit(stdoutTail.bytes(), protocol.TailLimit)
	res.StderrTail = sanitizeLimit(stderrTail.bytes(), protocol.TailLimit)

	// 产物扫描：workdir/outputs/<task_id>/（≤8 条各 ≤640KiB，超限跳过并注记）
	arts, skipped := scanArtifacts(r.profile.Workdir, task.ID)
	res.Artifacts = arts
	if skipped > 0 {
		res.Error = appendNote(res.Error, fmt.Sprintf("artifacts_skipped=%d", skipped))
	}
	return finish()
}

// deniedEnvKey 返回第一个越界的 env 键（allow_env 白名单外，或试图覆盖 runner
// 固定管理的 HOME）。
func deniedEnvKey(allow []string, overrides map[string]string) (string, bool) {
	wildcard := false
	for _, k := range allow {
		if k == "*" {
			wildcard = true
		}
	}
	for k := range overrides {
		if k == "HOME" {
			return k, true // HOME 由 runner 固定设为 ConfigHome（SPEC 五），任务不可覆盖
		}
		if wildcard {
			continue
		}
		allowed := false
		for _, a := range allow {
			if a == k {
				allowed = true
				break
			}
		}
		if !allowed {
			return k, true
		}
	}
	return "", false
}

// buildEnv 组装进程环境：base_env + HOME=ConfigHome + 白名单内的任务覆盖 +
// SWARM_* 系统变量（SPEC 4.9 产物链：agent 由此得知任务标识与产物输出目录）。
// 系统变量最后写入；任务侧 SWARM_* 覆盖一律丢弃——白名单仅决定该覆盖是否准入
// （准入也不生效），保证 runner 注入值不可被任务 env 改写。不继承 daemon 自身
// 环境（SPEC 五：防凭证泄漏进子进程），键序确定。
func (r *Runner) buildEnv(task protocol.Task) []string {
	env := make([]string, len(r.baseEnv), len(r.baseEnv)+len(task.Input.Env)+4)
	copy(env, r.baseEnv)
	if r.homeDir != "" {
		env = append(env, "HOME="+r.homeDir)
	}
	wildcard := false
	for _, k := range r.profile.AllowEnv {
		if k == "*" {
			wildcard = true
		}
	}
	for k, v := range task.Input.Env {
		if k == "HOME" || strings.HasPrefix(k, swarmEnvPrefix) {
			continue // HOME 固定值，策略层已拒绝，双保险；SWARM_* 系统命名空间同
		}
		if wildcard {
			env = append(env, k+"="+v)
			continue
		}
		for _, a := range r.profile.AllowEnv {
			if a == k {
				env = append(env, k+"="+v)
				break
			}
		}
	}
	// 系统变量（任务定位信息）：与 scanArtifacts 同一目录口径（绝对路径）
	return append(env,
		"SWARM_TASK_ID="+task.ID,
		"SWARM_OUTPUTS_DIR="+filepath.Join(r.profile.Workdir, "outputs", task.ID),
		"SWARM_TRACE_ID="+task.TraceID,
	)
}

// sanitizeLimit UTF-8 清洗（非法字节 → U+FFFD）后按字节截断至 limit（截断点回落
// 到完整 rune 边界，SPEC 4.9：清洗 → 截断）。
func sanitizeLimit(b []byte, limit int) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 {
		rn, size := utf8.DecodeLastRuneInString(s)
		if !(rn == utf8.RuneError && size == 1) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// appendNote 向 Error 追加注记（"; " 连接，主错误码保持首位供 contains 断言）。
func appendNote(err, note string) string {
	if err == "" {
		return note
	}
	return err + "; " + note
}
