//go:build unix

package service

// 启动恢复孤儿清扫测试（maturity-orphan-guard）：New 内对 started 且登记
// pgid+ProcStart 的恢复条目做身份校验清扫；pgid=0 旧条目零行为变化（向后兼
// 容锁）；指纹缺失（ProcStart=0）条目进清单但清扫侧证据不足放过（R1 必修1）。

import (
	"context"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// seedStartedWithPgid 直开账本预置 started 条目（New 之外的独立句柄，模拟「上一次
// 进程生命周期」留下的账本）；procStart/bootIdent 为组长证据三元组的另外两元。
func seedStartedWithPgid(t *testing.T, dir string, taskID string, pgid, procStart int64, bootIdent string) {
	t.Helper()
	l, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	task := protocol.Task{
		ID: taskID, TraceID: "tr-" + taskID, TargetNode: "n1",
		TargetAgent: "orphan-probe", SubmittedBy: "hana",
		Status: protocol.StatusOffered,
		CreatedAt: protocol.NowUTC(), UpdatedAt: protocol.NowUTC(),
	}
	if err := l.Accept(task); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	if err := l.MarkStarted(taskID); err != nil {
		t.Fatalf("seed MarkStarted: %v", err)
	}
	if pgid > 0 {
		if err := l.MarkSpawned(taskID, pgid, procStart, bootIdent); err != nil {
			t.Fatalf("seed MarkSpawned: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}

// orphanProbeRunner 提供 CleanupOrphanGroup 的最小执行器替身（类型断言识别面）：
// 记录清扫请求与应答，不动真进程——清扫的真实终止动作在 runner 包真进程测试
//（orphan_test.go）覆盖，本处验证 service 的接线与账本面。
type orphanProbeRunner struct {
	Runner
	got     [][3]any // (pgid, procStart, bootIdent) 清扫请求记录
	replies map[int64]string
}

func (o *orphanProbeRunner) CleanupOrphanGroup(pgid, procStart int64, bootIdent string) string {
	o.got = append(o.got, [3]any{pgid, procStart, bootIdent})
	return o.replies[pgid]
}

func (o *orphanProbeRunner) Run(_ context.Context, _ protocol.Task) protocol.TaskResult {
	return protocol.TaskResult{}
}

// TestNewSweepsStartedWithPgid 恢复扫描接线：started+pgid>0 条目在 New 内被
// 清扫，且账本 ProcStart 指纹随行传递（R1 必修1：pid+starttime 实例匹配的
// 数据链路）；账本条目保持 needs_review 降级（终态由恢复链路收敛，清扫不越
// 权改态）。
func TestNewSweepsStartedWithPgid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	seedStartedWithPgid(t, dir, "t-orphan-1", 9999, 777777, "boot-a")
	led, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	probe := &orphanProbeRunner{replies: map[int64]string{9999: "killed"}}
	runners := map[string]Runner{"orphan-probe": probe}
	bus := transport.NewBus()
	_ = New(Config{Ledger: led, Runners: runners}, bus.Endpoint("n1"))

	if len(probe.got) != 1 || probe.got[0][0] != int64(9999) || probe.got[0][1] != int64(777777) || probe.got[0][2] != "boot-a" {
		t.Fatalf("恢复扫描应携带 (9999, 777777, boot-a) 恰一次, got %v", probe.got)
	}
	// 条目已被 recover 降级 needs_review（既有机制，清扫不越权改态）
	e, ok := led.Get("t-orphan-1")
	if !ok || e.State != ledger.StateFinished || e.Result == nil ||
		e.Result.Status != protocol.ResultNeedsReview {
		t.Fatalf("条目应已降级 needs_review（recover 既有链路）, ok=%v state=%v result=%v",
			ok, e.State, e.Result)
	}
}

// TestNewSkipsLegacyNoPgid 向后兼容锁（DoD 4）：pgid=0 旧条目恢复扫描直接放过，
// 不触发任何清扫动作、账本零变化。
func TestNewSkipsLegacyNoPgid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	seedStartedWithPgid(t, dir, "t-legacy-1", 0, 0, "")
	led, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	probe := &orphanProbeRunner{replies: map[int64]string{}}
	runners := map[string]Runner{"orphan-probe": probe}
	bus := transport.NewBus()
	_ = New(Config{Ledger: led, Runners: runners}, bus.Endpoint("n1"))

	if len(probe.got) != 0 {
		t.Fatalf("pgid=0 旧条目不应触发清扫, got %v", probe.got)
	}
	// 旧条目照常走 recover 降级（needs_review），清扫清单不含它——零行为变化
	if e, _ := led.Get("t-legacy-1"); e.Result == nil || e.Result.Status != protocol.ResultNeedsReview {
		t.Fatalf("旧条目应照常降级 needs_review, got %+v", e.Result)
	}
}

// TestNewSweepRealProcess 真进程组端到端（DoD 2 的 service 面）：预置 started+
// 真 pgid 与组长真实指纹（procStartTime 同源读取，R1 必修1 数据链路）→ New →
// 组无幸存（含日志面断言：清扫动作有记录）。
func TestNewSweepRealProcess(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("构造真组失败: %v", err)
	}
	pgid := int64(cmd.Process.Pid)
	start := runner.ProcStartTime(cmd.Process.Pid)
	boot := runner.CurrentBootIdent()
	if start <= 0 || boot == "" {
		t.Fatalf("组长证据读取失败: start=%d boot=%q", start, boot)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-int(pgid), syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	dir := filepath.Join(t.TempDir(), "ledger")
	seedStartedWithPgid(t, dir, "t-orphan-real", pgid, start, boot)
	led, _, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	// 真 runner：Command=/bin/sleep（与组长 comm 匹配）；bootTime 注入同款
	// 语境（组晚于 test 进程启动，见 runner 包 orphan_test.go 注释）。
	runner.SetBootTimeForTest(time.Now().Add(time.Hour))
	defer runner.SetBootTimeForTest(time.Now())
	sweepRunner := runner.New(protocol.ExecProfile{
		Name: "orphan-probe", Kind: protocol.AgentKindCLI,
		Command: "/bin/sleep", Workdir: t.TempDir(),
		AllowArgs: true, Exposed: true,
	}, map[string]string{"PATH": "/usr/bin:/bin"}, t.TempDir())
	runners := map[string]Runner{"orphan-probe": sweepRunner}
	bus := transport.NewBus()
	// 捕获标准 log（New 内用全局 logger 记清扫动作；同包测试串行，无交叉污染）
	var logs strings.Builder
	oldLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLog)
	_ = New(Config{Ledger: led, Runners: runners}, bus.Endpoint("n1"))

	time.Sleep(200 * time.Millisecond)
	out, err := exec.Command("/usr/bin/pgrep", "-g", strconv.FormatInt(pgid, 10)).Output()
	if err == nil && len(out) > 0 {
		t.Fatalf("清扫后组内仍有幸存: %s", out)
	}
	if !strings.Contains(logs.String(), "孤儿清扫") {
		t.Fatalf("清扫动作应有日志记录（DoD 3 service 面）: %q", logs.String())
	}
}
