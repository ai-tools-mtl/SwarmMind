//go:build unix

package runner

// 孤儿清扫测试（maturity-orphan-guard，//go:build unix——Windows 由 Job 兑底无
// 此机制，no-op 实现无需测试）：真进程组验证清扫与误杀防线两条主线。
//
// 组构造用 /bin/sleep 直执（comm=文件名 "sleep"，身份可控；macOS /bin/sh 是 bash
// 副本且 bash 自改 comm 为 "bash"——名实分离会让 sh 类 agent 走「放过」安全侧，
// 属预期：宁漏不误，见 orphan_unix.go 注释）。
//
// 实例指纹语境（R2 必修1/M3）：生产中清扫对象恒为「本 node 启动前 spawn 的残留
// 实例」，测试内组是 test 进程启动后才 spawn 的——bootTime 注入未来时刻使
//「组长早于本 node」微秒防线与生产同构（同包测试串行，defer 恢复）。指纹为
// darwin 微秒/Linux ticks，仅同平台同启动周期内比较（bootIdent 由
// currentBootIdent 同源提供）。

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// sysProcAttrForTest 测试对照组的进程组装配（与 runner configureProcessGroup
// 同款 Setpgid，使组长 pid=pgid，清扫入口的输入形态与真实残留一致）。
func sysProcAttrForTest() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// sleepRunner 构造 Command=/bin/sleep 的 runner（comm 与组长一致，身份可命中）。
func sleepRunner(t *testing.T, mutate func(p *protocol.ExecProfile)) *Runner {
	t.Helper()
	r, _ := newRunner(t, func(p *protocol.ExecProfile) {
		p.Command = "/bin/sleep"
		if mutate != nil {
			mutate(p)
		}
	})
	return r
}

// groupAlive pgrep -g 探测组内存活（组长与后代组员一并计入）。
func groupAlive(t *testing.T, pgid int64) bool {
	t.Helper()
	out, err := exec.Command("/usr/bin/pgrep", "-g", itoa(pgid)).Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// startSleepGroup 独立 spawn 一个 Setpgid 真进程组（对照组），返回 cmd 与组长
// 启动时刻指纹（procStartTime 读取，与生产 spawn 侧同源）。跨平台保证指纹
// 已取得（>0），取得失败即 Fatal——后续用例输入必须是真实指纹。
func startSleepGroup(t *testing.T, bin string, args ...string) (*exec.Cmd, int64) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = sysProcAttrForTest()
	if err := cmd.Start(); err != nil {
		t.Fatalf("构造对照组失败: %v", err)
	}
	start := procStartTime(cmd.Process.Pid)
	if start <= 0 {
		t.Fatalf("组长 %d 启动时刻指纹读取失败（procStartTime 返回 0）", cmd.Process.Pid)
	}
	return cmd, start
}

// killGroupCleanup 测试收尾：组若仍存活整组补杀并 Wait 收割（kill 后必须 Wait）。
func killGroupCleanup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	pgid := int64(cmd.Process.Pid)
	if groupAlive(t, pgid) {
		_ = syscall.Kill(-int(pgid), syscall.SIGKILL)
	}
	_, _ = cmd.Process.Wait()
}

// TestSpawnHookPgid spawn 成功回调携带 pgid+procStart+bootIdent 且任务进行中组
// 存活（DoD 1 的 runner 半边），回调后清扫整组终止（DoD 2 的 killed 路径）。
// 三元组直接取回调实参（生产侧同一来源），保证指纹链路端到端一致。
func TestSpawnHookPgid(t *testing.T) {
	orig := bootTime
	bootTime = time.Now().Add(time.Hour)
	defer func() { bootTime = orig }()
	r := sleepRunner(t, nil)
	type ident struct {
		pgid, procStart int64
		bootIdent       string
	}
	got := make(chan ident, 1)
	r.SetSpawnHook(func(_ protocol.Task, p, s int64, b string) { got <- ident{p, s, b} })
	resCh := make(chan protocol.TaskResult, 1)
	go func() {
		resCh <- r.Run(context.Background(), simpleTask("30"))
	}()
	var id ident
	select {
	case id = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("spawn 回调未触发（5s 超时）")
	}
	if id.pgid <= 0 || id.procStart <= 0 || id.bootIdent == "" {
		t.Fatalf("pgid/procStart/bootIdent 应非零, got (%d, %d, %q)", id.pgid, id.procStart, id.bootIdent)
	}
	if id.bootIdent != currentBootIdent() {
		t.Fatalf("bootIdent 应与当前启动周期同源: %q != %q", id.bootIdent, currentBootIdent())
	}
	if !groupAlive(t, id.pgid) {
		t.Fatalf("进程组 %d 应存活", id.pgid)
	}
	act := r.CleanupOrphanGroup(id.pgid, id.procStart, id.bootIdent)
	if !strings.HasPrefix(act, "killed") {
		t.Fatalf("清扫应 killed, got %q", act)
	}
	if groupAlive(t, id.pgid) {
		t.Fatal("清扫后组内仍有幸存")
	}
	// 任务结果收尾（清扫后 Run 的 Wait 返回信号错误，正常回归路径）
	if res := <-resCh; res.Status == protocol.ResultCompleted {
		t.Fatalf("被清扫的任务不应 completed, got %s", res.Status)
	}
}

// TestCleanupOrphanKillsUnknownGroup 对「非本 runner 启动的真进程组」的清扫：
// 独立 Setpgid 组 + 组内后代（第二进程 setpgid 加入组长组，模拟 agent 派生
// 孙进程）+ 同名 runner（与真实场景「agent 命令与残留组长同名」同构），指纹与
// 启动周期同源——验证身份校验命中与整组终止动作（含后代，DoD 2）。
func TestCleanupOrphanKillsUnknownGroup(t *testing.T) {
	orig := bootTime
	bootTime = time.Now().Add(time.Hour)
	defer func() { bootTime = orig }()
	cmd, start := startSleepGroup(t, "/bin/sleep", "30")
	pgid := int64(cmd.Process.Pid)
	defer killGroupCleanup(t, cmd)
	// 组内后代：另一进程加入组长所在组（agent 派生孙进程的组内形态）。不在主
	// 流程同步 Wait（会阻塞到清扫前）：收尾由组末尾整组补杀 + Wait 回收。
	member := exec.Command("/bin/sleep", "30")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: int(pgid)}
	if err := member.Start(); err != nil {
		t.Fatalf("构造组内后代失败: %v", err)
	}
	defer func() {
		_ = member.Process.Kill()
		_, _ = member.Process.Wait()
	}()
	if !groupAlive(t, pgid) {
		t.Fatal("对照组未存活")
	}
	r := sleepRunner(t, nil) // Command=/bin/sleep，与组长 comm 匹配
	act := r.CleanupOrphanGroup(pgid, start, currentBootIdent())
	if !strings.HasPrefix(act, "killed") {
		t.Fatalf("应 killed, got %q", act)
	}
	if groupAlive(t, pgid) {
		t.Fatal("清扫后组内仍有幸存（DoD 2，含组内后代）")
	}
}

// TestCleanupOrphanSkipsStartMismatch pgid 复用防线（R1 必修1 核心负例，R2 必修
// M3 强化）：两个先后 spawn 的 /bin/sleep 是同名（comm 基名匹配）不同实例——
// 用旧实例的真实指纹清扫新实例的组，必须放过且新组存活。
//
// 时序与断言（R2 M3：负例必须锁住指纹防线本身，不被 bootTime 防线掩盖）：
//   - spawn 间隔 50ms 保证 darwin 微秒指纹必不等（真机实证同秒 spawn 碰撞）；
//   - bootTime 注入未来（模拟重启后语境：两实例都「早于新 node」，bootTime
//     防线放行）——skip 只能来自指纹不等防线；
//   - 断言 skip 原因含「指纹与账本不符」，锁定具体防线而非笼统 skip。
//
// 生产对应：node 崩溃后 pid 被任意新进程复用（含同名异路径程序），账本指纹
// 对不上即放过；R1 旧实现（comm+bootTime）在此场景被实证误杀
//（identity-probe.log：同名异路径进程 probe=true,true,true → killed）。
//
// 注：R1 overlay 负例的「复制二进制」变体在本测试树不可直接构造——macOS 对
// 复制二进制强制代码签名校验，spawn 即死成僵尸；其安全语义（同名 + 指纹不符 →
// 放过）由本测试全覆盖：防线维度是 comm 基名 + 实例指纹，路径不参与身份判定。
func TestCleanupOrphanSkipsStartMismatch(t *testing.T) {
	oldCmd, oldStart := startSleepGroup(t, "/bin/sleep", "30")
	defer killGroupCleanup(t, oldCmd)
	time.Sleep(50 * time.Millisecond) // 跨微秒（darwin 指纹精度），保证异实例指纹必不等
	newCmd, newStart := startSleepGroup(t, "/bin/sleep", "30")
	defer killGroupCleanup(t, newCmd)
	newPgid := int64(newCmd.Process.Pid)
	if newStart == oldStart {
		t.Fatalf("异实例指纹应不等（微秒精度）: %d", newStart)
	}

	orig := bootTime
	bootTime = time.Now().Add(time.Hour) // 重启后语境：两实例均早于「新 node」
	defer func() { bootTime = orig }()

	r := sleepRunner(t, nil)
	act := r.CleanupOrphanGroup(newPgid, oldStart, currentBootIdent())
	if !strings.HasPrefix(act, "skip:") {
		t.Fatalf("指纹不符应 skip 放过, got %q", act)
	}
	if !strings.Contains(act, "指纹与账本不符") {
		t.Fatalf("应命中指纹防线（而非 bootTime 防线）, got %q", act)
	}
	if !groupAlive(t, newPgid) {
		t.Fatal("误杀：指纹不符的新进程组被终止（安全红线）")
	}
}

// TestCleanupOrphanSkipsBootMismatch 启动周期防线（R2 必修1）：账本 bootIdent
// 与当前周期不等（跨机/跨系统重启）即整条证据过期——即使 pid/指纹/名字全部
// 巧合等值也必须放过（孤儿必已随旧周期消亡，此场景进程为无关新进程）。
func TestCleanupOrphanSkipsBootMismatch(t *testing.T) {
	cmd, start := startSleepGroup(t, "/bin/sleep", "30")
	defer killGroupCleanup(t, cmd)
	pgid := int64(cmd.Process.Pid)
	r := sleepRunner(t, nil)
	for _, bad := range []string{"", "0.0", "other-boot-cycle"} {
		act := r.CleanupOrphanGroup(pgid, start, bad)
		if !strings.HasPrefix(act, "skip:") || !strings.Contains(act, "启动周期不符") {
			t.Fatalf("bootIdent=%q 应 skip:启动周期不符, got %q", bad, act)
		}
		if !groupAlive(t, pgid) {
			t.Fatalf("误杀：周期不符的进程组被终止（安全红线）bootIdent=%q", bad)
		}
	}
}

// TestCleanupOrphanSkipsMismatch 误杀防线（DoD 3）：组长 comm 与 agent 命令基名
// 不匹配 → skip 放过，组长存活（安全红线：绝不误杀）。bootTime 注入未来排除
// 防线干扰，锁定「名称不符」这一具体防线。
func TestCleanupOrphanSkipsMismatch(t *testing.T) {
	orig := bootTime
	bootTime = time.Now().Add(time.Hour)
	defer func() { bootTime = orig }()
	cmd, start := startSleepGroup(t, "/bin/sleep", "30")
	defer killGroupCleanup(t, cmd)
	pgid := int64(cmd.Process.Pid)
	r := sleepRunner(t, func(p *protocol.ExecProfile) { p.Command = "/nonexistent-dir-zz/other-agent" })
	act := r.CleanupOrphanGroup(pgid, start, currentBootIdent())
	if !strings.HasPrefix(act, "skip:") || !strings.Contains(act, "进程名不匹配") {
		t.Fatalf("应 skip:进程名不匹配, got %q", act)
	}
	if !groupAlive(t, pgid) {
		t.Fatal("误杀：不匹配的进程组被终止（安全红线）")
	}
}

// TestCleanupOrphanSkipsInvalid pgid<=0、指纹<=0（旧条目/落盘降级）与不存在的
// 组直接放过（DoD 4 输入面 + ESRCH 放过路径）。
func TestCleanupOrphanSkipsInvalid(t *testing.T) {
	r := sleepRunner(t, nil)
	cur := currentBootIdent()
	for _, tc := range [][3]any{{int64(0), int64(1), cur}, {int64(-5), int64(1), cur}, {int64(1 << 22), int64(0), cur}, {int64(1), int64(0), cur}, {int64(1), int64(-3), cur}} {
		pgid, procStart, bootIdent := tc[0].(int64), tc[1].(int64), tc[2].(string)
		if act := r.CleanupOrphanGroup(pgid, procStart, bootIdent); !strings.HasPrefix(act, "skip:") {
			t.Fatalf("pgid=%d procStart=%d 应 skip, got %q", pgid, procStart, act)
		}
	}
	// 不存在的组：从高位找空闲 pid（探测确认无组后断言 skip:组长不存在）
	dead := int64(1 << 22)
	for dead > 1 && groupAlive(t, dead) {
		dead--
	}
	if act := r.CleanupOrphanGroup(dead, 12345, cur); !strings.HasPrefix(act, "skip:") {
		t.Fatalf("不存在组应 skip, got %q", act)
	}
}

// TestReapStopsWhenGroupReowned 首杀后易主回归（R2 必修2）：确定性故障注入——
// 首次 probe 通过 → 首杀 → 探活报「组还在」→ 补杀前重验发现易主（指纹不符）→
// 必须停止，绝不对易主后的组发第二次信号。断言：SIGKILL 恰一次、探活后停止。
func TestReapStopsWhenGroupReowned(t *testing.T) {
	origProbe, origKill, origZero := probeLeaderFn, killGroup, signalGroupZero
	defer func() { probeLeaderFn, killGroup, signalGroupZero = origProbe, origKill, origZero }()

	cur := currentBootIdent()
	kills := 0
	probeLeaderFn = func(pid, pgid int, wantBase string, procStart int64) (bool, string) {
		// 首次全证据命中；首杀后（reap 重验）组已易主 → 指纹不符
		if kills == 0 {
			return true, ""
		}
		return true, "组长启动指纹与账本不符，疑似 pgid 复用"
	}
	killGroup = func(pgid int64) error { kills++; return nil }
	signalGroupZero = func(pgid int64) error { return nil } // 探活恒「组还在」，驱动重验路径

	r := sleepRunner(t, nil)
	act := r.CleanupOrphanGroup(424242, 1, cur)
	if !strings.HasPrefix(act, "killed") || !strings.Contains(act, "停止补杀") {
		t.Fatalf("易主后应停止补杀, got %q", act)
	}
	if kills != 1 {
		t.Fatalf("易主后不得重发信号：应恰 1 次 SIGKILL, got %d", kills)
	}
}

// TestReapStopsWhenGroupGone 首杀后组消散回归（正常 killed 路径的注入版）：
// 探活报 ESRCH → 立即返回 killed，不再有第二次信号。
func TestReapStopsWhenGroupGone(t *testing.T) {
	origProbe, origKill, origZero := probeLeaderFn, killGroup, signalGroupZero
	defer func() { probeLeaderFn, killGroup, signalGroupZero = origProbe, origKill, origZero }()

	probeLeaderFn = func(pid, pgid int, wantBase string, procStart int64) (bool, string) {
		return true, ""
	}
	kills := 0
	killGroup = func(pgid int64) error { kills++; return nil }
	signalGroupZero = func(pgid int64) error { return syscall.ESRCH }

	r := sleepRunner(t, nil)
	if act := r.CleanupOrphanGroup(424242, 1, currentBootIdent()); act != "killed" {
		t.Fatalf("组消散应 killed, got %q", act)
	}
	if kills != 1 {
		t.Fatalf("应恰 1 次 SIGKILL, got %d", kills)
	}
}

// TestCommBaseWidth comm 截断分平台边界（R2 建议修）：darwin 有效 16（含 NUL
// 的 MAXCOMLEN=16）、Linux 有效 15（TASK_COMM_LEN=16 含 NUL）——基名截断与
// 内核 comm 同口径，15/16/17 字节名不再被统一截断漏配。
func TestCommBaseWidth(t *testing.T) {
	want := "abcdefghijklmno" // 15 字节：两平台均不截
	if runtime.GOOS == "darwin" {
		want = "abcdefghijklmnop" // darwin 有效 16
	}
	if got := commBase("/usr/bin/abcdefghijklmnop"); got != want {
		t.Fatalf("长名截断 = %q, want %q (%s)", got, want, runtime.GOOS)
	}
	if got := commBase("/usr/bin/abcdefghijklmno"); got != "abcdefghijklmno" {
		t.Fatalf("15 字节名不应截断, got %q", got)
	}
	if got := commBase("/usr/bin/sleep"); got != "sleep" {
		t.Fatalf("短名直通, got %q", got)
	}
}
