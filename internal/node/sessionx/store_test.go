package sessionx

// sessions.json 持久化三态测试（TASK-agent-session-core DoD 4）：round-trip、
// 原子写失败保留旧数据、损坏文件空表起步。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// seedSession 用 fake CLI 建一个真实会话（含轮次留痕）。
func seedSession(t *testing.T, m *Manager, agent string) protocol.Session {
	t.Helper()
	return mustCreate(t, m, agent, t.TempDir(), "seed-prompt")
}

// TestSessionsJSONRoundTrip 落盘→重开→快照全字段等值（JSON 字节级比对，规避
// time.Time 单调时钟差异的 DeepEqual 假差）。
func TestSessionsJSONRoundTrip(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-roundtrip"}),
	}
	m, dataDir := newManager(t, specs, Options{Now: testNow})
	sess := seedSession(t, m, "codex-dev")
	if _, _, err := m.Send(context.Background(), sess.ID, "followup"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	want, _ := m.Status(sess.ID)
	if want.TurnCount != 2 {
		t.Fatalf("预置轮数: %d", want.TurnCount)
	}

	m2, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	got, ok := m2.Status(sess.ID)
	if !ok {
		t.Fatalf("重开后会话丢失")
	}
	a, _ := jsonMarshal(want)
	b, _ := jsonMarshal(got)
	if string(a) != string(b) {
		t.Fatalf("round-trip 漂移:\n want %s\n got  %s", a, b)
	}
	// 版本号与落盘形态抽检。
	raw, err := os.ReadFile(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatalf("读 sessions.json: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 1`) || !strings.Contains(string(raw), `"native_id": "tid-roundtrip"`) {
		t.Fatalf("落盘形态异常: %s", raw)
	}
}

// TestSessionsJSONCorruptStartsEmpty 损坏文件：按空表起步 + 日志如实记录 + 下次
// 落盘整体重写为合法内容。
func TestSessionsJSONCorruptStartsEmpty(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-c"}),
	}
	dataDir := t.TempDir()
	corrupt := "这不是JSON{{{"
	if err := os.WriteFile(filepath.Join(dataDir, "sessions.json"), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &testLogger{}
	m, err := NewManager(dataDir, specs, Options{Now: testNow, Log: log})
	if err != nil {
		t.Fatalf("损坏文件不得阻断启动: %v", err)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("损坏应空表起步, got %d", n)
	}
	found := false
	for _, l := range log.all() {
		if strings.Contains(l, "损坏") && strings.Contains(l, "空表起步") {
			found = true
		}
	}
	if !found {
		t.Fatalf("损坏起步须记日志: %v", log.all())
	}
	// 空表起步后正常建会话并落盘；重开读到新会话（整体重写生效）。
	sess := seedSession(t, m, "codex-dev")
	m2, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	if _, ok := m2.Status(sess.ID); !ok {
		t.Fatalf("重写后重开应读到会话")
	}
}

// TestSessionsJSONPermAndTmpCleanup 落盘纪律：0600、无 .tmp 残留。
func TestSessionsJSONPermAndTmpCleanup(t *testing.T) {
	fake := buildFakeCLI(t)
	m, dataDir := newManager(t, map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-p"}),
	}, Options{Now: testNow})
	seedSession(t, m, "codex-dev")

	fi, err := os.Stat(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("sessions.json 权限应 0600, got %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "sessions.json.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原子写后不得残留 .tmp: %v", err)
	}
}

// TestSessionsJSONPersistFailureKeepsOldData 原子写失败：磁盘旧数据逐字节保留、
// 内存态不丢、权限恢复后下次落盘收敛（新旧会话并存）。
func TestSessionsJSONPersistFailureKeepsOldData(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-old"}),
	}
	dataDir := t.TempDir()
	m1, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	old := seedSession(t, m1, "codex-dev")
	before, err := os.ReadFile(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}

	// 数据目录只读：tmp 写不进 → rename 不发生 → 旧文件完整。
	if err := os.Chmod(dataDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })

	m2, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	sessB, turnB, perr := m2.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if perr == nil || !strings.Contains(perr.Error(), "持久化失败") {
		t.Fatalf("持久化失败须如实上报, got %v", perr)
	}
	if sessB.ID == "" || turnB.Status != protocol.TurnOK {
		t.Fatalf("轮次已执行，会话应在内存: %+v", sessB)
	}
	after, err := os.ReadFile(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("原子写失败后旧数据被破坏")
	}
	if _, ok := m2.Status(sessB.ID); !ok {
		t.Fatalf("内存态不得因落盘失败丢失")
	}

	// 权限恢复：下一次状态变更触发落盘收敛，新旧会话并存可重开。
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Close(sessB.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m3, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	gotOld, ok1 := m3.Status(old.ID)
	gotNew, ok2 := m3.Status(sessB.ID)
	if !ok1 || !ok2 {
		t.Fatalf("收敛失败: old=%v new=%v", ok1, ok2)
	}
	if gotOld.NativeID != old.NativeID || gotNew.Status != protocol.SessionClosed {
		t.Fatalf("收敛内容异常: %+v / %+v", gotOld, gotNew)
	}
}

// TestSessionsJSONRenameFailureCleansTmpKeepsTarget S3 反例回归：tmp 写入成功但
// rename 失败（sessions.json 位上是目录，rename 文件→目录必然失败）——错误如实
// 上报、旧目标不被半截文件替换、不留 tmp 残留、内存态不丢。
func TestSessionsJSONRenameFailureCleansTmpKeepsTarget(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-r"}),
	}
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "sessions.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("启动不得被阻断: %v", err)
	}
	sess, turn, perr := m.Create(context.Background(), "codex-dev", t.TempDir(), "p")
	if perr == nil || !strings.Contains(perr.Error(), "持久化失败") {
		t.Fatalf("rename 失败须如实上报, got %v", perr)
	}
	if sess.ID == "" || turn == nil || turn.Status != protocol.TurnOK {
		t.Fatalf("轮次已执行，会话应在内存: %+v %+v", sess, turn)
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("rename 失败后旧目标不得被替换: %v %v", fi, err)
	}
	if _, err := os.Stat(target + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename 失败后必须清理 tmp: %v", err)
	}
	if _, ok := m.Status(sess.ID); !ok {
		t.Fatalf("内存态不得因落盘失败丢失")
	}
}

// TestSessionsJSONStaleTmpRecovered S3 反例回归：崩溃残留的 tmp（旧实现 WriteFile
// 会复用它且不收紧权限）——O_EXCL 独占创建遇残留必须清理重试，落盘成功后权限
// 0600、无残留，重开可读。
func TestSessionsJSONStaleTmpRecovered(t *testing.T) {
	fake := buildFakeCLI(t)
	specs := map[string]AgentSpec{
		"codex-dev": codexSpec(fake, map[string]string{"FAKE_THREAD_ID": "tid-stale"}),
	}
	dataDir := t.TempDir()
	stale := filepath.Join(dataDir, "sessions.json.tmp")
	if err := os.WriteFile(stale, []byte("stale-junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	seedSession(t, m, "codex-dev")
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("落盘后不得残留 tmp: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("落盘权限应 0600, got %v", fi.Mode().Perm())
	}
	m2, err := NewManager(dataDir, specs, Options{Now: testNow})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	if _, ok := m2.Status(m.List()[0].ID); !ok {
		t.Fatalf("重开应读到会话")
	}
}

// jsonMarshal 小助手：快照字节级比对（规避 time.Time 单调时钟 DeepEqual 假差）。
func jsonMarshal(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
