package ledger

// sessionhw_test.go 会话轮平台序号高水位（T2-R2 N4）：受理同事务推进、随账本
// 文件持久化（重启不丢）、条目保留期清理不回退；close 恒 TurnSeq=0 不占号。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionTurnTask 构造带会话身份段的任务（seq=0 表示 close，不占号）。
func sessionTurnTask(id, sid string, seq int64) protocol.Task {
	t := sampleTask(id, protocol.NowUTC().Add(time.Hour))
	t.Session = &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: protocol.SessionTurnOpSend}
	return t
}

// TestSessionTurnHighWaterAcceptAtomic 受理即推进：Accept 同事务写高水位（含在
// 途未终态条目），取 max 单调不回退；close 任务（seq=0）不产生条目不占号。
func TestSessionTurnHighWaterAcceptAtomic(t *testing.T) {
	dir := t.TempDir()
	l, _ := mustOpen(t, dir)
	const sid = "s-hw000000000000000000000000000000000000000000000001"
	if hw := l.SessionTurnHighWater(sid); hw != 0 {
		t.Fatalf("无受理记录应为 0: %d", hw)
	}
	if err := l.Accept(sessionTurnTask("t-hw-2", sid, 2)); err != nil {
		t.Fatal(err)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("受理 #2 后高水位应 2: %d", hw)
	}
	// 乱序受理更小号：高水位取大不回退
	if err := l.Accept(sessionTurnTask("t-hw-1", sid, 1)); err != nil {
		t.Fatal(err)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("高水位取大不回退: %d", hw)
	}
	// close 任务（TurnSeq=0）不推进
	closeTask := sampleTask("t-hw-close", protocol.NowUTC().Add(time.Hour))
	closeTask.Session = &protocol.SessionTurnRef{SessionID: sid, TurnSeq: 0, Op: protocol.SessionTurnOpClose}
	if err := l.Accept(closeTask); err != nil {
		t.Fatal(err)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("close 不占号: %d", hw)
	}
	// 状态转移（非受理操作）不回退高水位
	if err := l.MarkFinished("t-hw-2", protocol.TaskResult{Status: protocol.ResultCancelled, FinishedAt: protocol.NowUTC()}); err != nil {
		t.Fatal(err)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("状态转移不回退高水位: %d", hw)
	}
}

// TestSessionTurnHighWaterPersistsAcrossRestart N4 重启口径：高水位随账本文件
// 持久化，重开后保留；条目被保留期清理后仍不回退（与 hub 持久轮行同口径）。
func TestSessionTurnHighWaterPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	const sid = "s-hwrestart000000000000000000000000000000000000000001"
	seedLedger(t, dir, func(l *Ledger) {
		// local 受理形态（本机续投；落终态自确认，进入保留期清理范围）
		if err := l.AcceptOrigin(sessionTurnTask("t-hw-a", sid, 2), OriginLocal); err != nil {
			t.Fatal(err)
		}
		if err := l.MarkFinished("t-hw-a", protocol.TaskResult{Status: protocol.ResultCancelled, FinishedAt: protocol.NowUTC()}); err != nil {
			t.Fatal(err)
		}
	})
	l, _ := mustOpen(t, dir)
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("重启后高水位应保留: %d", hw)
	}
	// 保留期清理该条目：高水位不随条目消失回退
	n, err := l.CleanupAcked(protocol.NowUTC().Add(48*time.Hour), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("清理: n=%d err=%v", n, err)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("条目清理后高水位不回退: %d", hw)
	}
	_ = l.Close()
	// 清理后重启：高水位仍留存（文件形态携带，不依赖条目重算）
	l2, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l2.Close() })
	if hw := l2.SessionTurnHighWater(sid); hw != 2 {
		t.Fatalf("清理后重启高水位仍应留存: %d", hw)
	}
}

// TestLedgerLegacyFileWithoutEnvelope 旧版裸 map 文件零迁移直读：条目照常装载，
// 高水位自条目重算（受理条目携带 Task.Session.TurnSeq）。
func TestLedgerLegacyFileWithoutEnvelope(t *testing.T) {
	dir := t.TempDir()
	const sid = "s-hwlegacy0000000000000000000000000000000000000000001"
	seedLedger(t, dir, func(l *Ledger) {
		if err := l.Accept(sessionTurnTask("t-hw-legacy", sid, 3)); err != nil {
			t.Fatal(err)
		}
	})
	_ = mustDiskEntries(t, dir) // 确认文件已在盘上
	// 把 v1 信封文件改写为旧版裸 map 形态（模拟升级前的账本文件）
	writeLegacyLedger(t, dir, mustDiskEntries(t, dir))
	l, _ := mustOpen(t, dir)
	if e, ok := l.Get("t-hw-legacy"); !ok || e.State != StateAccepted {
		t.Fatalf("旧形态条目应照常装载: %+v ok=%v", e, ok)
	}
	if hw := l.SessionTurnHighWater(sid); hw != 3 {
		t.Fatalf("旧形态高水位应自条目重算: %d", hw)
	}
}

// writeLegacyLedger 把盘上 v1 信封文件改写为旧版裸 map 形态（测试辅助）。
func writeLegacyLedger(t *testing.T, dir string, entries map[string]Entry) {
	t.Helper()
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ledgerFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
