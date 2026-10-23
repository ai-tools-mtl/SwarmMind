//go:build windows

// Windows 进程治理单测（返工必修4）：darwin 侧交叉编译+静态审查，Windows 真
// 机跑真实进程验证挂起启动→入 Job→恢复执行全链路与致命语义。
package runner

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// configureProcessGroup：挂起启动标志 + Job 登记齐备。
func TestConfigureProcessGroupSuspendsAndRegistersJob(t *testing.T) {
	cmd := &exec.Cmd{}
	if err := configureProcessGroup(cmd); err != nil {
		t.Fatalf("治理装配失败: %v", err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Fatal("应以 CREATE_SUSPENDED 挂起启动（消除先 Start 再入 Job 的逃逸窗口）")
	}
	hv, ok := jobTrack.Load(cmd)
	if !ok {
		t.Fatal("应登记任务 Job")
	}
	_ = windows.CloseHandle(hv.(windows.Handle))
	jobTrack.Delete(cmd)
}

// launchGoverned 全链路：挂起启动→入 Job→恢复→Wait，reap 后 Job 登记移除。
func TestLaunchGovernedSuspendsAssignsResumes(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := configureProcessGroup(cmd); err != nil {
		t.Fatalf("治理装配失败: %v", err)
	}
	if err := launchGoverned(cmd); err != nil {
		t.Fatalf("受治理执行失败: %v", err)
	}
	reapProcessGroup(cmd)
	if _, ok := jobTrack.Load(cmd); ok {
		t.Fatal("reap 后应移除 Job 登记")
	}
}

// 致命语义：未登记 Job 拒绝执行，绝不静默降级为无治理 spawn。
func TestLaunchGovernedRefusesWithoutJob(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := launchGoverned(cmd); err == nil {
		t.Fatal("未登记任务 Job 应拒绝执行")
	}
	if cmd.Process != nil {
		t.Fatal("拒绝路径不应 spawn 进程")
	}
}

// 兜底 Job：幂等加入成功。
func TestInitSelfJobGuardIdempotent(t *testing.T) {
	if err := InitSelfJobGuard(); err != nil {
		t.Fatalf("本进程入兜底 Job 失败: %v", err)
	}
	if err := InitSelfJobGuard(); err != nil {
		t.Fatalf("重复加入应幂等: %v", err)
	}
}
