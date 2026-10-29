package runner

// 进程组治理的导出薄包装（TASK-agent-session-core 改动范围 4：runner 允许纯追加
// 导出包装，是 T1 任务书唯一的范围例外）。唯一目的：sessionx（会话内核）复用同一
// 套平台治理实现——独立进程组/Job + 超时/取消整组终止 + 轮终整组收尾——删除其包内
// 复制体，进程组治理单一真相源。
//
// 本文件零逻辑：直通委托 platform 侧同名未导出实现（process_unix.go /
// process_windows.go），不改变任何现有实现、调用链与孤儿治理语义；普通 Task 路径
// 零触碰。未来治理语义演进只改 runner 原函数，sessionx 自动跟随。

import "os/exec"

// ConfigureProcessGroup spawn 前置治理装配（Unix setpgid 独立进程组 / Windows
// CREATE_SUSPENDED + KILL_ON_JOB_CLOSE Job）。见 process_unix.go /
// process_windows.go configureProcessGroup。
func ConfigureProcessGroup(cmd *exec.Cmd) error { return configureProcessGroup(cmd) }

// LaunchGoverned spawn 全程（Start + Wait；Windows 侧挂起启动 → 入 Job → 恢复 →
// Wait）。afterStart 语义见 process_unix.go launchGoverned——sessionx 无 spawn 期
// 回调需求，传 nil。见 launchGoverned。
func LaunchGoverned(cmd *exec.Cmd, afterStart func()) error { return launchGoverned(cmd, afterStart) }

// KillProcessGroup 整组终止（超时/取消路径）：Unix kill(-pgid, SIGKILL) /
// Windows TerminateJobObject + Process.Kill 兜底。见 killProcessGroup。
func KillProcessGroup(cmd *exec.Cmd) error { return killProcessGroup(cmd) }

// ReapProcessGroup 正常返回路径的整组收尾：探测并终止后台派生的残留组员至全组
// 消失（至多 2s 兜底），确保调用返回后工作目录再无组内写入者。见 reapProcessGroup。
func ReapProcessGroup(cmd *exec.Cmd) { reapProcessGroup(cmd) }
