//go:build unix

package runner

// 孤儿清扫（maturity-orphan-guard）：node 崩溃重启后，对账本 started 条目登记的
// 进程组证据（pgid + 组长启动指纹 + 启动周期标识）做身份校验并整组终止。安全
// 红线（SPEC 五进程治理延伸）：宁可漏杀绝不误杀——pgid 在重启窗口内可能被 OS
// 复用给无关进程组（含同名异路径程序，R1 审查实证），无条件 kill(-pgid) 是误杀
// 事故源；身份证据不足一律放过（任务已收敛 needs_review/node_restarted_midway，
// 主人可见，漏杀后果仅资源浪费）。
//
// 身份证据（全部命中才终止，R2 重构后口径）：
//   1. 证据在场：账本登记 pgid>0、procStart>0、bootIdent 非空（spawn 时同事务
//      落盘；缺失即放过，安全侧）；
//   2. 启动周期一致：账本 bootIdent 与当前系统启动周期相同（Linux boot_id /
//      darwin kern.boottime）——跨重启周期的孤儿已被 OS 全灭，残留条目放过；
//   3. 组长存在且确为组长：Linux /proc/<pid>/stat pgrp==pid；darwin KinfoProc
//      P_pid==pgid && Eproc.Pgid==pgid；
//   4. 实例指纹等值：组长当前启动时刻与账本一致。darwin 微秒精度
//    （Timeval Sec*1e6+Usec，R2：秒精度存在同秒不同实例碰撞，真机实证）；
//      Linux 为 ticks since boot（10ms 粒度，且仅同启动周期内比较）。
//   5. 进程名基名匹配 profile.Command 基名（comm 语义截断：Linux 有效 15 字节、
//      darwin 16——辅助防线；脚本场景 comm 是解释器名，不一致 → 放过）。
//
// 终止动作（R2 必修2：终止全程身份复核）：首杀前全证据校验；SIGKILL 后的存活
// 探测循环每轮补杀前重验指纹——组已易主（原组退出、编号复用）即停，绝不向身份
// 不再成立的组发信号。诚实声明（R3 裁决口径）：校验与信号发送非原子（Go 标准库
// 无 pidfd），「重验通过 → 信号送达」之间存在残余窗口，且窗口无时长上界（goroutine
// 可被调度暂停任意时长）——防线深度 = 每轮重验把误杀面压缩到「窗口内恰好完成
// pid 释放与复用」的多重巧合（同周期 + 指纹等值 + 同名 + 窗口内复用），概率趋零
// 但非零；漏杀方向安全（任务已收敛 needs_review，主人可见）。SPEC 五口径同此。
//
// 平台分支：probeLeader/procStartTime/currentBootIdent 按 GOOS 各自实现于
// orphan_linux.go 与 orphan_darwin.go（build tag 隔离）。Windows 无孤儿清扫
//（orphan_windows.go no-op，Job 兑底）。

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// bootTime 本 node 进程启动时刻（包初始化附近）。darwin 侧以微秒精度作为实例
// 指纹之外的防线（组长启动微秒必须早于本 node 启动——同秒内 pid 复用由微秒
// 精度区分）；Linux 的 starttime 为 ticks since boot 无 wall 语义，靠 ticks
// 粒度（10ms）+ boot_id 周期校验承担同等安全度。
var bootTime = time.Now()

// SetBootTimeForTest 仅供测试注入（maturity-orphan-guard）：模拟「node 重启后」
// 语境——生产中清扫对象恒为本进程启动之前的残留组，而测试内组是 test 进程启动
// 后才 spawn 的，注入未来时刻使「组长早于本 node」防线与生产同构。生产装配不得调用。
func SetBootTimeForTest(ts time.Time) { bootTime = ts }

// CleanupOrphanGroup 对疑似孤儿进程组做身份校验与整组终止（maturity-orphan-guard）。
// pgid/procStart/bootIdent 为账本登记的进程组证据三元组（0/空=无证据）。返回动作
// 描述：killed*=已终止（*:消散等待异常/易主停止，信号已发）；skip:<原因>=证据
// 不足/已退出/周期不符，放过；not_applicable=平台无此机制。调用方仅记日志。
// 保守原则：任何一步证据缺失即放过，不重试不猜测。
func (r *Runner) CleanupOrphanGroup(pgid, procStart int64, bootIdent string) string {
	if pgid <= 0 {
		return "skip:pgid 无效"
	}
	if procStart <= 0 {
		return "skip:无组长启动时刻证据（旧条目或落盘降级），放过"
	}
	if cur := currentBootIdent(); bootIdent == "" || cur == "" || bootIdent != cur {
		return "skip:启动周期不符（跨机或跨系统重启，孤儿应已消散），放过"
	}
	wantBase := commBase(r.profile.Command)
	if wantBase == "" {
		return "skip:agent 命令名为空，无法校验身份"
	}
	pid := int(pgid)
	alive, deny := probeLeaderFn(pid, pid, wantBase, procStart)
	if !alive {
		return "skip:" + deny
	}
	if deny != "" {
		return "skip:" + deny + "，放过"
	}
	// 校验全过（指纹等值 = 账本登记的那个进程实例仍在原位）：整组终止。
	// 校验→信号非原子（无 pidfd），残余窗口由 reap 循环每轮重验压缩，见头注释。
	if err := killGroup(pgid); err != nil {
		return fmt.Sprintf("skip:kill 失败（%v），放过", err)
	}
	for i := 0; i < reapWaitRounds; i++ {
		if err := signalGroupZero(pgid); err != nil {
			if err == syscall.ESRCH {
				return "killed" // 全组消散
			}
			return fmt.Sprintf("killed:探活失败（%v），不再等待（SIGKILL 已发）", err) // EPERM 等未知态不冒充消散
		}
		// 补杀前重验身份（R2 必修2）：组已易主即停，绝不向身份不再成立的组重发。
		if alive, deny := probeLeaderFn(pid, pid, wantBase, procStart); !alive || deny != "" {
			return "killed:组易主或消散，停止补杀（信号未再发出）"
		}
		_ = killGroup(pgid)
		time.Sleep(reapWaitInterval)
	}
	return "killed:组消散超时（SIGKILL 已发，不再等待）"
}

// killGroup 整组 SIGKILL（仅身份校验通过后调用）。var 形式供测试注入易主时序
//（R2 必修2 回归位，见 probeLeaderFn）。
var killGroup = func(pgid int64) error {
	return syscall.Kill(-int(pgid), syscall.SIGKILL)
}

// signalGroupZero 整组探活（Signal 0）：全组已退出（ESRCH）返回非 nil。var 同上。
var signalGroupZero = func(pgid int64) error {
	return syscall.Kill(-int(pgid), syscall.Signal(0))
}

// probeLeaderFn probeLeader 的函数变量（同包注入位）：reap 补杀前重验身份的
// 测试接缝（R2 必修2 回归场景需构造「首杀后组易主」时序，真进程无法确定性
// 造出 pid 复用，经此接缝注入）。生产恒指向 probeLeader。
var probeLeaderFn = probeLeader

// commBase 命令基名并截断到本平台 comm 有效宽度（与内核进程名同口径比较）：
// Linux TASK_COMM_LEN=16 含结尾 NUL（有效 15 字节），darwin MAXCOMLEN=16 含
// NUL（P_comm[17] 尾字节为对齐填充，有效 16）——R1 建议修：分平台取边界，
// 15/16 字节长命令不再被统一截断漏配。
func commBase(cmd string) string {
	base := filepath.Base(cmd)
	width := 15
	if runtime.GOOS == "darwin" {
		width = 16
	}
	if len(base) > width {
		base = base[:width]
	}
	return base
}

// commandPath 本执行器的 agent 命令（身份校验用；Profile 私有字段的最小读口）。
func (r *Runner) commandPath() string { return r.profile.Command }

// commString 内核 comm 字节数组 → 去尾 NUL 字符串。
func commString(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ProcStartTime 进程启动时刻指纹读口（maturity-orphan-guard）：procStartTime
// 的导出包装，供跨包真进程测试与账本指纹同源读取（service/orphan_recovery_test
// 预置账本时使用，保证与 spawn 侧同一数据源）。
func ProcStartTime(pid int) int64 { return procStartTime(pid) }

// CurrentBootIdent 当前系统启动周期标识读口（maturity-orphan-guard）：导出包装，
// 供跨包真进程测试与账本 bootIdent 同源（同 ProcStartTime 的读口理由）。
func CurrentBootIdent() string { return currentBootIdent() }
