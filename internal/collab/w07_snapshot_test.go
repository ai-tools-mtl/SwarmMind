// w07_snapshot_test.go 快照持久化钩子测试（W07-02，docs/tasks/v3/W07-hub-routing.md
// 工作块1「持久化经 hub 既有 SQLite 存储惯例」的内核面）：SnapshotStore 钩子装配
// （同一内核零分叉——状态机/journal/回执/意图全部复用，仅落盘面可替换）、损坏拒绝
// 带病启动、Save 失败无回执内存零变更（N05）、默认文件快照回归（既有 spaces.json
// 路径行为零变化锚）。
package collab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSnapshot 内存快照钩子（测试面；Save 故障注入位）。
type fakeSnapshot struct {
	mu      sync.Mutex
	data    []byte
	saves   int
	fail    error
	loadErr error
}

func (f *fakeSnapshot) Load() ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, false, f.loadErr
	}
	if f.data == nil {
		return nil, false, nil
	}
	return append([]byte(nil), f.data...), true, nil
}

func (f *fakeSnapshot) Save(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.data = append([]byte(nil), b...)
	f.saves++
	return nil
}

// TestW07SnapshotHookRoundtrip 钩子装配往返：创建经钩子落盘（Save 被调、字节为
// MarshalIndent+换行形态）；重开同钩子状态恢复（空间/回执/意图全量可见——同一内核
// 语义，仅落盘面替换）。
func TestW07SnapshotHookRoundtrip(t *testing.T) {
	dir := t.TempDir()
	snap := &fakeSnapshot{}
	st, err := Open(dir, Options{Snapshot: snap, Now: func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("Open(钩子): %v", err)
	}
	req := CreateRequest{RequestID: "req-hook-1", Goal: "中心空间", Mode: ModeManual}
	sp, _, err := st.CreateSpace(req, Subject{Name: "owner", Class: ClassOwner})
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if snap.saves == 0 {
		t.Fatal("创建应经钩子落盘（Save 未被调用）")
	}
	if !strings.HasSuffix(string(snap.data), "\n") {
		t.Fatal("快照字节形态应为 MarshalIndent+换行（与文件面一致）")
	}
	// 控制动作（回执/journal 语义照常）：配置成员→就绪。
	if _, _, err := st.Control(sp.ID, ControlRequest{RequestID: "ctl-1", Action: ActConfigureMembers, ExpectedVersion: sp.Version,
		Members: []Member{{Name: "n1/coder"}}}, Subject{Name: "owner", Class: ClassOwner}); err != nil {
		t.Fatalf("Control configure_members: %v", err)
	}
	if _, _, err := st.Control(sp.ID, ControlRequest{RequestID: "ctl-2", Action: ActReady, ExpectedVersion: sp.Version + 1}, Subject{Name: "owner", Class: ClassOwner}); err != nil {
		t.Fatalf("Control ready: %v", err)
	}
	_ = st.Close()

	// 重开：快照经钩子加载，空间与回执全量恢复。
	snap2 := &fakeSnapshot{data: snap.data}
	st2, err := Open(dir, Options{Snapshot: snap2})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer st2.Close()
	got, ok := st2.Space(sp.ID)
	if !ok {
		t.Fatal("重开后空间应恢复")
	}
	if got.Status != StatusReady {
		t.Fatalf("重开后状态应 ready: %s", got.Status)
	}
	if got.Version != sp.Version+2 { // configure_members + ready 各占一版
		t.Fatalf("重开后版本应推进: %d", got.Version)
	}
}

// TestW07SnapshotCorruptRefusesStart 快照损坏（JSON 解析失败）拒绝带病启动；版本
// 未知同样拒绝（宁拒启不瞎门——空间状态是协作事实唯一真相）。
func TestW07SnapshotCorruptRefusesStart(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, Options{Snapshot: &fakeSnapshot{data: []byte("{broken json")}}); err == nil {
		t.Fatal("损坏快照应拒绝启动")
	}
	// 版本未知：合法 JSON 但 version 非当前。
	if _, err := Open(dir, Options{Snapshot: &fakeSnapshot{data: []byte(`{"version":99,"spaces":{}}`)}}); err == nil {
		t.Fatal("版本未知快照应拒绝启动")
	}
}

// TestW07SnapshotSaveErrorNoReceipt Save 失败：apply 错误如实上抛、无回执、内存
// 零变更（重试同 request_id 可成功——副本整体丢弃语义，N05 零虚假接受）。
func TestW07SnapshotSaveErrorNoReceipt(t *testing.T) {
	dir := t.TempDir()
	snap := &fakeSnapshot{}
	st, err := Open(dir, Options{Snapshot: snap})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	snap.fail = errors.New("sqlite busy")
	_, _, err = st.CreateSpace(CreateRequest{RequestID: "req-fail", Goal: "g", Mode: ModeManual}, Subject{Name: "owner", Class: ClassOwner})
	if err == nil || !strings.Contains(err.Error(), "落盘失败") {
		t.Fatalf("Save 失败应如实上抛: %v", err)
	}
	if len(st.Spaces()) != 0 {
		t.Fatal("落盘失败内存零变更（不虚报创建）")
	}
	snap.fail = nil
	sp, dup, err := st.CreateSpace(CreateRequest{RequestID: "req-fail", Goal: "g", Mode: ModeManual}, Subject{Name: "owner", Class: ClassOwner})
	if err != nil || dup {
		t.Fatalf("修复后同键重试应成功: %v dup=%v", err, dup)
	}
	if _, ok := st.Space(sp.ID); !ok {
		t.Fatal("重试创建应成立")
	}
}

// TestW07SnapshotNilFallsBackToFile 默认（Snapshot nil）仍走文件快照：spaces.json
// 落盘且可重开（既有行为零变化锚——node 路径不受钩子改造牵连）。
func TestW07SnapshotNilFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open(文件): %v", err)
	}
	if _, _, err := st.CreateSpace(CreateRequest{RequestID: "req-file", Goal: "g", Mode: ModeManual}, Subject{Name: "owner", Class: ClassOwner}); err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	_ = st.Close()
	if _, err := os.Stat(filepath.Join(dir, fileName)); err != nil {
		t.Fatalf("文件快照应存在: %v", err)
	}
	st2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("文件重开: %v", err)
	}
	defer st2.Close()
	if len(st2.Spaces()) != 1 {
		t.Fatalf("文件路径空间应恢复: %d", len(st2.Spaces()))
	}
}

// TestW07ReservedRequestIDGate 保留前缀门单源（W07-02 收口）：内部命名空间前缀
// 命中即真，普通 request_id 不误伤。
func TestW07ReservedRequestIDGate(t *testing.T) {
	for _, id := range []string{"boot-S-drafting", "converge:r1:converged", "attach:r2", "syspause:sp:v1:t1", "syspausex:sp:v1:t1", " boot-x"} {
		if !RequestIDReserved(id) {
			t.Fatalf("保留前缀应命中: %q", id)
		}
	}
	for _, id := range []string{"req-1", "disp-abc", "bootleg", "my-converge:2", ""} {
		if RequestIDReserved(id) {
			t.Fatalf("普通 request_id 不应命中保留门: %q", id)
		}
	}
}
