//go:build windows

package runner

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 进程治理（SPEC 五：Job Object，2026-10-24 P4② 服务化一并落地）：
// 每个子进程一个 Job Object（JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE），对齐 Unix
// 独立进程组语义——超时/取消 TerminateJobObject 整组终止（含 agent 派生的孙
// 进程）；node 进程自身退出（服务停止/崩溃/被杀）时句柄由 OS 关闭，Job 内残留
// 进程一并终止，「服务停止后子进程绝不孤儿化」由此保证。
//
// 逃逸窗口消除与致命语义（2026-10-24 返工必修4）：子进程一律 CREATE_SUSPENDED
// 挂起启动 → 入 Job → ResumeThread，执行任何指令前必已受治理；Job 创建/设限/
// 入组失败即拒绝执行（宁可任务 failed 也不静默降级为仅直接子进程治理）；node
// 自身另入一个兜底 Job（InitSelfJobGuard，进程异常终止时 OS 兜底清理一切遗留
// 子进程，显式 breakaway 按需豁免）。Windows 8+ 支持嵌套 Job，任务级 Job 与兜
// 底 Job 并存。依赖治理：golang.org/x/sys 为 SPEC 三白名单登记项（半标准库，
// Windows 服务化与 Job Object 唯一正道），理由见 SPEC 三与 go.mod。

// jobTrack 每次 spawn 的 Job 句柄表（cmd → Job Object）：configure 时登记、
// spawn 失败即释放、reap 后关闭移除。并发任务各占一条，键唯一。
var jobTrack sync.Map // map[*exec.Cmd]windows.Handle

// reapWaitInterval/reapWaitRounds 与 Unix 同口径：整组终止后的存活探测轮询，
// 上限 2s，确保 Run 返回后 workdir 再无组内写入者（产物扫描与 workdir 互斥
// 释放的前提，P1.5 二轮必修3 同款约束）。
const (
	reapWaitInterval = 10 * time.Millisecond
	reapWaitRounds   = 200
)

// configureProcessGroup Windows：spawn 前置挂起启动标志（CREATE_SUSPENDED，
// 逃逸窗口消除的前提）并创建本任务的 Job Object 设 KILL_ON_JOB_CLOSE。进程句
// 柄 Start 后才有，入组推迟到 launchGoverned。创建/设限失败返回错误由调用方
// 拒绝执行——任务治理装配是硬前提，不做降级（返工必修4②）。
func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("创建 Job Object 失败: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("设置 KILL_ON_JOB_CLOSE 失败: %w", err)
	}
	jobTrack.Store(cmd, h)
	return nil
}

// launchGoverned 挂起启动 → 入 Job → 恢复执行 → Wait（返工必修4①）：入组
// 时子进程尚未执行任何指令，旧实现「先 Start 再入 Job」的逃逸窗口（子进程抢
// 先派生孙进程）不复存在。入组/恢复失败一律终止挂起进程并返回错误，绝不静默
// 降级（返工必修4②）；任务 Job 句柄留给 reapProcessGroup 统一释放（runner 在
// Wait 完成后必调）。
func launchGoverned(cmd *exec.Cmd, afterStart func()) error {
	hv, ok := jobTrack.Load(cmd)
	if !ok {
		// 治理装配未登记（configureProcessGroup 失败已在 runner 侧拦截，防御位）
		return errors.New("任务 Job 未登记，拒绝无治理执行")
	}
	job := hv.(windows.Handle)
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job) // 未 spawn 成功：Job 释放
		jobTrack.Delete(cmd)
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return killSuspended(cmd, fmt.Errorf("打开挂起进程失败: %w", err))
	}
	err = windows.AssignProcessToJobObject(job, ph)
	_ = windows.CloseHandle(ph)
	if err != nil {
		return killSuspended(cmd, fmt.Errorf("进程入 Job 失败: %w", err))
	}
	if err := resumeMainThread(uint32(cmd.Process.Pid)); err != nil {
		return killSuspended(cmd, fmt.Errorf("恢复执行失败: %w", err))
	}
	if afterStart != nil {
		afterStart() // 恢复执行后回调（maturity-orphan-guard；Windows spawnIdentity 恒 (0,0)）
	}
	return cmd.Wait()
}

// spawnIdentity 执行进程组 ID 与组长启动时刻指纹（maturity-orphan-guard）：
// Windows 无进程组语义，恒 (0, 0)——Job Object KILL_ON_JOB_CLOSE 由 OS 兑底
//（node 崩溃时句柄关闭组员全灭），service 侧 MarkSpawned 对 pgid=0 自然不落盘，
// 恢复清扫无 Windows 条目。
func spawnIdentity(cmd *exec.Cmd) (pgid, procStart int64) { return 0, 0 }

// killSuspended 挂起态致命失败收尾：终止未执行过任何指令的进程并 Wait 收割
// （排干管道拷贝协程，满足「kill 后必须 Wait」纪律）。
func killSuspended(cmd *exec.Cmd, cause error) error {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return cause
}

// resumeMainThread 恢复 CREATE_SUSPENDED 启动的进程（ResumeThread 初始线程）。
// os/exec 不暴露初始线程句柄，经 Toolhelp 线程快照按 PID 查找——挂起态新进程
// 恰有一个线程，取首个命中。
func resumeMainThread(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("创建线程快照失败: %w", err)
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		return fmt.Errorf("遍历线程快照失败: %w", err)
	}
	for {
		if te.OwnerProcessID == pid {
			th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err != nil {
				return fmt.Errorf("打开初始线程 %d 失败: %w", te.ThreadID, err)
			}
			_, err = windows.ResumeThread(th)
			_ = windows.CloseHandle(th)
			if err != nil {
				return fmt.Errorf("恢复初始线程 %d 失败: %w", te.ThreadID, err)
			}
			return nil
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			return fmt.Errorf("初始线程未找到（pid %d）: %w", pid, err)
		}
	}
}

// selfJobGuard 兜底 Job 句柄：进程生命周期持有，永不主动关闭——进程退出（含
// 崩溃/被杀）时 OS 关闭句柄触发 KILL_ON_JOB_CLOSE，终止全部遗留子进程。
var selfJobGuard windows.Handle

// InitSelfJobGuard 把本进程加入兜底 Job（Kill-on-Job-Close；BREAKAWAY_OK 允许
// agent 显式申请脱离的合法例外，不开静默脱离）。任务级 Job 之外的双保险：node
// 异常终止时两路兜底都触发（任务 Job 句柄随进程死亡关闭 + 兜底 Job 全员终止）。
// 幂等；失败仅返回错误由调用方告警，不拒启——任务级治理（spawn 侧致命拦截）
// 不依赖本兜底，优雅关闭路径照常整组收尾。
func InitSelfJobGuard() error {
	if selfJobGuard != 0 {
		return nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("创建兜底 Job 失败: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("设置兜底 Job 限位失败: %w", err)
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("本进程入兜底 Job 失败: %w", err)
	}
	selfJobGuard = h
	return nil
}

// killProcessGroup 整组终止：TerminateJobObject 杀 Job 全体（含孙进程），再
// Process.Kill 兜底直接子进程（Job 未登记/已空时仍与旧版行为一致）。cmd.Wait
// 由 runner 侧保证（kill 后必须 Wait）。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if h, ok := jobTrack.Load(cmd); ok {
		_ = windows.TerminateJobObject(h.(windows.Handle), 1) // Job 已空时幂等成功
	}
	return cmd.Process.Kill()
}

// jobAccounting Win32 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION（ABI 固定布局，
// x/sys 未封装，本地声明）：reap 收尾轮询只读 ActiveProcesses。
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// reapProcessGroup 正常返回路径的整组收尾（对齐 Unix 语义）：TerminateJobObject
// 终止后台派生的残留组员，轮询 Job 清空（ActiveProcesses==0）至多 2s，随后
// CloseHandle——KILL_ON_JOB_CLOSE 兜底终止任何残余。调用前提：cmd 已 Wait。
func reapProcessGroup(cmd *exec.Cmd) {
	hv, ok := jobTrack.LoadAndDelete(cmd)
	if !ok {
		return
	}
	h := hv.(windows.Handle)
	_ = windows.TerminateJobObject(h, 1)
	for i := 0; i < reapWaitRounds; i++ {
		var acc jobAccounting
		var ret uint32
		if err := windows.QueryInformationJobObject(h, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), &ret); err != nil {
			break // 查询失败不再等：CloseHandle 的 kill-on-close 兜底
		}
		if acc.ActiveProcesses == 0 {
			break
		}
		time.Sleep(reapWaitInterval)
	}
	_ = windows.CloseHandle(h)
}
