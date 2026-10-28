//go:build linux

package runner

// parseProcStat 解析单测（R1 必修3 + R2 必修3）：pgrp 字段索引勘误的回归锁
//（旧实现把 fields[1]（ppid）当 pgrp 读，合法组长全被错误放过）、comm 含空格
// 与右括号的合成串边界、真进程指纹链路。

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestParseProcStatBytesSynthetic 合成串解析（纯函数核心，R2 必修3 补测）：
// comm 含空格与右括号、正常字段序（state,ppid,pgrp,...,starttime）提取。
func TestParseProcStatBytesSynthetic(t *testing.T) {
	// 合成 /proc stat 行：pid (comm) state ppid pgrp session ... starttime（第22字段）
	// 字段 1-based：1 pid | 2 comm | 3 state | 4 ppid | 5 pgrp | ... | 22 starttime
	line := "1234 (my agent (v1)) S 999 1234 100 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 77777 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	tail := strings.Fields(line[strings.LastIndex(line, ")")+2:])
	if len(tail) < 20 {
		t.Fatalf("测试串字段不足: %d", len(tail))
	}
	pgrp, start, comm, err := parseProcStatBytes([]byte(line), 1234)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pgrp != 1234 {
		t.Fatalf("pgrp 应取 fields[2]=1234（fields[1] 是 PPID=999 勘误回归锁）, got %d", pgrp)
	}
	if start != 77777 {
		t.Fatalf("starttime 应取 fields[19]=77777, got %d", start)
	}
	if comm != "my agent (v1)" {
		t.Fatalf("comm 应含空格与右括号, got %q", comm)
	}
	// 字段不足：按证据不足处理（返回错误）
	if _, _, _, err := parseProcStatBytes([]byte("1 (x) S 0 1"), 1); err == nil {
		t.Fatal("字段不足应报错（调用方按放过处理）")
	}
	// 数值异常：pgrp 非整数
	bad := "1 (x) S 0 abc " + strings.Repeat("0 ", 19)
	if _, _, _, err := parseProcStatBytes([]byte(bad), 1); err == nil {
		t.Fatal("数值异常应报错")
	}
}

// TestParseProcStatReal 真进程解析：Setpgid 组长的 pgrp 应等于自身 pid、
// starttime>0、comm 为自身可执行基名。
func TestParseProcStatReal(t *testing.T) {
	self := exec.Command("sleep", "0.5")
	self.SysProcAttr = sysProcAttrForTest()
	if err := self.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() {
		_ = self.Process.Kill()
		_, _ = self.Process.Wait()
	}()

	pgrp, start, comm, err := parseProcStat(self.Process.Pid)
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	if pgrp != int64(self.Process.Pid) {
		t.Fatalf("Setpgid 组长 pgrp 应等于 pid %d, got %d（fields[2] 勘误回归锁）", self.Process.Pid, pgrp)
	}
	if start <= 0 {
		t.Fatalf("starttime 应为正 ticks, got %d", start)
	}
	if comm != "sleep" {
		t.Fatalf("comm 应 sleep, got %q", comm)
	}
	if got := procStartTime(self.Process.Pid); got != start {
		t.Fatalf("procStartTime 与 parseProcStat 不同源: %d vs %d", got, start)
	}
	// probeLeader 全链：真组长 + 真实指纹 + 同名 → 全证据命中
	alive, deny := probeLeader(self.Process.Pid, self.Process.Pid, "sleep", start)
	if !alive || deny != "" {
		t.Fatalf("真组长应全证据命中, got alive=%v deny=%q", alive, deny)
	}
}

// TestProbeLeaderLinuxGuard 指纹防线（真进程）：两个先后 spawn 的 Setpgid 组长
// ——spawn 间隔 50ms（>2 ticks@100Hz）保证 starttime 必不等；用旧实例指纹探
// 新组长：alive=true、deny 含「指纹与账本不符」（R2 M3：不等式前提由间隔保证，
// 不再依赖「不同进程必不同 tick」的未验证假设）。
func TestProbeLeaderLinuxGuard(t *testing.T) {
	cmd1, s1 := startSleepGroup(t, "/bin/sleep", "1")
	defer killGroupCleanup(t, cmd1)
	time.Sleep(50 * time.Millisecond) // 跨 tick
	cmd2, s2 := startSleepGroup(t, "/bin/sleep", "1")
	defer killGroupCleanup(t, cmd2)
	if s1 == s2 {
		t.Fatalf("50ms 间隔后 ticks 仍相等（HZ 异常?）: %d", s1)
	}
	alive, deny := probeLeader(cmd2.Process.Pid, cmd2.Process.Pid, "sleep", s1)
	if !alive || !strings.Contains(deny, "指纹与账本不符") {
		t.Fatalf("异实例指纹应 alive=true + 指纹防线, got (%v,%q)", alive, deny)
	}
	// 同实例（组长自身 + 自身指纹）→ 全证据命中
	alive, deny = probeLeader(cmd2.Process.Pid, cmd2.Process.Pid, "sleep", s2)
	if !alive || deny != "" {
		t.Fatalf("同实例应命中, got alive=%v deny=%q", alive, deny)
	}
	// boot 周期防线：currentBootIdent 同源非空（boot_id 可读）
	if currentBootIdent() == "" {
		t.Fatal("/proc/sys/kernel/random/boot_id 不可读，周期防线失效")
	}
}
