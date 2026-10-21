package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

var errInjected = errors.New("注入故障")

// openTestStore 在临时目录打开 store 并注册清理。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// readDiskState 直接从磁盘读快照（绕过内存），验证落盘内容。
func readDiskState(t *testing.T, s *Store) State {
	t.Helper()
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("读快照 %s: %v", s.path, err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析快照: %v", err)
	}
	return st
}

// TestStoreUpdatePublishAndPersist 事务成功路径：内存发布 + 落盘一致。
func TestStoreUpdatePublishAndPersist(t *testing.T) {
	s := openTestStore(t)
	err := s.Update(func(st *State) error {
		st.Nodes["n1"] = NodeRecord{Epoch: 3, Agents: []protocol.AgentCard{{Name: "echo-demo", Kind: protocol.AgentKindCLI}}}
		st.Tasks["t1"] = protocol.Task{ID: "t1", Status: protocol.StatusSubmitted}
		st.Mailboxes["n1"] = Mailbox{Tasks: []string{"t1"}}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	v := s.View()
	if v.Nodes["n1"].Epoch != 3 || len(v.Nodes["n1"].Agents) != 1 {
		t.Errorf("内存 Nodes 未发布: %+v", v.Nodes)
	}
	if _, ok := v.Tasks["t1"]; !ok {
		t.Error("内存 Tasks 未发布")
	}
	if len(v.Mailboxes["n1"].Tasks) != 1 {
		t.Errorf("内存 Mailboxes 未发布: %+v", v.Mailboxes)
	}
	disk := readDiskState(t, s)
	if disk.Nodes["n1"].Epoch != 3 || len(disk.Tasks) != 1 || len(disk.Mailboxes["n1"].Tasks) != 1 {
		t.Errorf("落盘内容与内存不一致: %+v", disk)
	}
}

// TestStoreReloadAfterClose 关闭后重开，状态从快照恢复（DoD 7 的存储基础）。
func TestStoreReloadAfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = s.Update(func(st *State) error {
		st.Nodes["n1"] = NodeRecord{Epoch: 9}
		st.Tasks["t1"] = protocol.Task{ID: "t1", TraceID: "tr", Status: protocol.StatusSubmitted}
		st.Mailboxes["n1"] = Mailbox{Tasks: []string{"t1"}}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开 Open: %v", err)
	}
	defer s2.Close()
	v := s2.View()
	if v.Nodes["n1"].Epoch != 9 {
		t.Errorf("重启后 Epoch=%d, 期望 9", v.Nodes["n1"].Epoch)
	}
	if got := v.Tasks["t1"]; got.ID != "t1" || got.TraceID != "tr" {
		t.Errorf("重启后任务未恢复: %+v", got)
	}
	if len(v.Mailboxes["n1"].Tasks) != 1 {
		t.Errorf("重启后信箱未恢复: %+v", v.Mailboxes)
	}
}

// TestStoreBatchReloadAfterClose 控制批次随快照持久化（SPEC 六.五）：含 review
// 记录的任务与批次逐项状态重启后恢复；exposed=false 审计登记一并恢复。
func TestStoreBatchReloadAfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	at := protocol.NowUTC()
	err = s.Update(func(st *State) error {
		tt := protocol.Task{ID: "t1", TraceID: "tr", Status: protocol.StatusNeedsReview,
			Review: &protocol.TaskReview{By: "admin", At: at, Action: protocol.ReviewAckReview, Note: "已核对"}}
		st.Tasks["t1"] = tt
		st.Batches["b1"] = ControlBatch{
			ID: "b1", Agent: "inspector", Input: protocol.TaskInput{Args: []string{"patrol"}},
			CreatedAt: at,
			Items: []ControlItem{
				{Target: "n1", TaskID: "t1"},
				{Target: "n2", Skipped: "offline"},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开 Open: %v", err)
	}
	defer s2.Close()
	v := s2.View()
	rv := v.Tasks["t1"].Review
	if rv == nil || rv.By != "admin" || rv.Action != protocol.ReviewAckReview || !rv.At.Equal(at) {
		t.Errorf("重启后核对记录未恢复: %+v", rv)
	}
	b, ok := v.Batches["b1"]
	if !ok {
		t.Fatalf("重启后批次未恢复: %+v", v.Batches)
	}
	if b.Agent != "inspector" || len(b.Items) != 2 ||
		b.Items[0].TaskID != "t1" || b.Items[1].Skipped != "offline" {
		t.Errorf("重启后批次内容不符: %+v", b)
	}
	if !b.CreatedAt.Equal(at) || len(b.Input.Args) != 1 || b.Input.Args[0] != "patrol" {
		t.Errorf("批次时间/输入不符: %+v", b)
	}
}

// TestStoreMutateRejectNoPublishNoAck mutate 拒绝：不落盘、不发布、不 Ack（Update 返回错误）。
func TestStoreMutateRejectNoPublishNoAck(t *testing.T) {
	s := openTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Tasks["t1"] = protocol.Task{ID: "t1"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	before := readDiskState(t, s)

	err := s.Update(func(st *State) error {
		st.Tasks["t2"] = protocol.Task{ID: "t2"}
		return errors.New("容量预检失败")
	})
	if err == nil {
		t.Fatal("mutate 失败必须返回错误（不 Ack）")
	}
	if _, ok := s.View().Tasks["t2"]; ok {
		t.Error("失败事务不得发布到内存")
	}
	after := readDiskState(t, s)
	if len(after.Tasks) != len(before.Tasks) {
		t.Errorf("失败事务不得落盘：before=%d after=%d", len(before.Tasks), len(after.Tasks))
	}
}

// TestStoreWriteFailureNoPublishNoAck 写坏目录触发失败：临时文件路径被目录占位后
// 落盘必败（对任何用户确定失败）→ 断言内存未变、无 Ack；移除障碍后重试成功。
func TestStoreWriteFailureNoPublishNoAck(t *testing.T) {
	s := openTestStore(t)
	if err := os.Mkdir(s.tmp, 0o755); err != nil {
		t.Fatalf("占位临时路径: %v", err)
	}
	err := s.Update(func(st *State) error {
		st.Tasks["t9"] = protocol.Task{ID: "t9"}
		return nil
	})
	if err == nil {
		t.Fatal("写盘失败必须返回错误（不 Ack）")
	}
	if _, ok := s.View().Tasks["t9"]; ok {
		t.Error("落盘失败不得发布到内存")
	}
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("落盘失败不得产生快照文件: %v", err)
	}

	if err := os.Remove(s.tmp); err != nil {
		t.Fatalf("移除障碍: %v", err)
	}
	if err := s.Update(func(st *State) error {
		st.Tasks["t9"] = protocol.Task{ID: "t9"}
		return nil
	}); err != nil {
		t.Fatalf("障碍移除后 Update 应成功: %v", err)
	}
	if _, ok := s.View().Tasks["t9"]; !ok {
		t.Error("重试事务应发布到内存")
	}
}

// TestStoreInjectionStages 四个预设注入点逐一验证先写后见语义（SPEC 八.2）。
func TestStoreInjectionStages(t *testing.T) {
	s := openTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Tasks["t1"] = protocol.Task{ID: "t1"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	diskBefore, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("读快照: %v", err)
	}
	addTask := func(id string) func(*State) error {
		return func(st *State) error {
			st.Tasks[id] = protocol.Task{ID: id}
			return nil
		}
	}

	// before_write：盘与内存都不变
	s.inject = func(stage string) error {
		if stage == stageBeforeWrite {
			return errInjected
		}
		return nil
	}
	if err := s.Update(addTask("t2")); !errors.Is(err, errInjected) {
		t.Fatalf("before_write 注入应使 Update 失败: %v", err)
	}
	if _, ok := s.View().Tasks["t2"]; ok {
		t.Error("before_write 失败不得发布到内存")
	}
	if diskAfter, _ := os.ReadFile(s.path); string(diskAfter) != string(diskBefore) {
		t.Error("before_write 失败不得落盘")
	}

	// after_rename：盘已是新状态、内存未发布（模拟「盘成内存败」崩溃窗口）
	s.inject = func(stage string) error {
		if stage == stageAfterRename {
			return errInjected
		}
		return nil
	}
	if err := s.Update(addTask("t2")); !errors.Is(err, errInjected) {
		t.Fatalf("after_rename 注入应使 Update 失败: %v", err)
	}
	if _, ok := s.View().Tasks["t2"]; ok {
		t.Error("after_rename 失败不得发布到内存")
	}
	if disk := readDiskState(t, s); len(disk.Tasks) != 2 {
		t.Errorf("after_rename 时磁盘应已含新状态: %+v", disk.Tasks)
	}

	// 做一次干净事务，让内存重新领先并覆写磁盘，对齐两侧
	s.inject = nil
	if err := s.Update(addTask("t2")); err != nil {
		t.Fatalf("对齐 Update: %v", err)
	}

	// before_publish：表现同 after_rename（盘新、内存未发布）
	s.inject = func(stage string) error {
		if stage == stageBeforePublish {
			return errInjected
		}
		return nil
	}
	if err := s.Update(addTask("t3")); !errors.Is(err, errInjected) {
		t.Fatalf("before_publish 注入应使 Update 失败: %v", err)
	}
	if _, ok := s.View().Tasks["t3"]; ok {
		t.Error("before_publish 失败不得发布到内存")
	}
	if disk := readDiskState(t, s); len(disk.Tasks) != 3 {
		t.Errorf("before_publish 时磁盘应已含新状态: %+v", disk.Tasks)
	}

	// before_ack：内存已发布但调用方未获 Ack（模拟「发布成、确认丢」）
	s.inject = nil
	if err := s.Update(addTask("t3")); err != nil {
		t.Fatalf("对齐 Update: %v", err)
	}
	s.inject = func(stage string) error {
		if stage == stageBeforeAck {
			return errInjected
		}
		return nil
	}
	if err := s.Update(addTask("t4")); !errors.Is(err, errInjected) {
		t.Fatalf("before_ack 注入应使 Update 失败: %v", err)
	}
	if _, ok := s.View().Tasks["t4"]; !ok {
		t.Error("before_ack 失败时内存应已发布（Ack 丢失≠未发布）")
	}
	s.inject = nil
}

// TestStoreCloneIsolation 事务在深拷贝副本上进行：事务内修改不污染先前发布的快照，
// 读者持有的旧 View() 保持稳定，读者改旧快照也不影响已发布状态（写时复制）。
func TestStoreCloneIsolation(t *testing.T) {
	s := openTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Tasks["t1"] = protocol.Task{
			ID:    "t1",
			Input: protocol.TaskInput{Args: []string{"orig"}, Env: map[string]string{"K": "v"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	reader := s.View() // 读者持有旧快照
	if err := s.Update(func(st *State) error {
		c := st.Tasks["t1"] // 事务内是本事务深拷贝，可自由修改
		c.Input.Args[0] = "mutated"
		c.Input.Env["K"] = "mutated"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if reader.Tasks["t1"].Input.Args[0] != "orig" || reader.Tasks["t1"].Input.Env["K"] != "v" {
		t.Errorf("读者持有的旧快照被事务污染（写时复制被破坏）: %+v", reader.Tasks["t1"].Input)
	}
	if got := s.View().Tasks["t1"]; got.Input.Args[0] != "mutated" || got.Input.Env["K"] != "mutated" {
		t.Errorf("事务修改未在新状态生效: %+v", got.Input)
	}
	// 读者反向修改自己持有的旧快照，不得影响已发布状态
	reader.Tasks["t1"].Input.Args[0] = "reader-mutated"
	if got := s.View().Tasks["t1"].Input.Args[0]; got != "mutated" {
		t.Errorf("读者修改旧快照影响了已发布状态: %q", got)
	}
}

// TestStoreConcurrentUpdatesSerialized 并发 Update 由单写 goroutine 串行执行，更新不丢。
func TestStoreConcurrentUpdatesSerialized(t *testing.T) {
	s := openTestStore(t)
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Update(func(st *State) error {
				r := st.Nodes["n1"]
				r.Epoch++
				st.Nodes["n1"] = r
				return nil
			})
			if err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := s.View().Nodes["n1"].Epoch; got != n {
		t.Errorf("Epoch=%d, 期望 %d（单写串行保证不丢更新）", got, n)
	}
}

// TestStoreConcurrentReadersWriters 并发读者与写者竞争：写者每事务同时追加 Tasks 与
// Mailbox（快照内不变量：两者条数相等），读者并发取快照断言不变量成立且长度不回退——
// 任何「读到半程状态/撕裂快照」都会破坏这两条。
func TestStoreConcurrentReadersWriters(t *testing.T) {
	s := openTestStore(t)
	const writers = 8
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				id := fmt.Sprintf("tw-%d-%d", w, j)
				err := s.Update(func(st *State) error {
					st.Tasks[id] = protocol.Task{ID: id}
					mb := st.Mailboxes["n1"]
					mb.Tasks = append(mb.Tasks, id)
					st.Mailboxes["n1"] = mb
					return nil
				})
				if err != nil {
					t.Errorf("写者 Update: %v", err)
					return
				}
			}
		}(w)
	}

	const readers = 4
	var readerWG sync.WaitGroup
	for r := 0; r < readers; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			last := -1
			for k := 0; k < 200; k++ {
				v := s.View()
				if len(v.Mailboxes["n1"].Tasks) != len(v.Tasks) {
					t.Errorf("快照撕裂/不自洽：tasks=%d mailbox=%d", len(v.Tasks), len(v.Mailboxes["n1"].Tasks))
					return
				}
				if last >= 0 && len(v.Tasks) < last {
					t.Errorf("读者观察到快照长度回退: %d < %d", len(v.Tasks), last)
					return
				}
				last = len(v.Tasks)
			}
		}()
	}
	readerWG.Wait()
	close(stop)
	wg.Wait()
}

// TestStoreCloseUpdateRace Close 与 Update 竞争：并发事务要么成功要么返回 ErrClosed，
// 绝不 panic（禁止向已关闭通道发送）；Close 幂等返回，关闭后新事务一律 ErrClosed。
func TestStoreCloseUpdateRace(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("tr-%d", i)
			err := s.Update(func(st *State) error {
				st.Tasks[id] = protocol.Task{ID: id}
				return nil
			})
			if err != nil && !errors.Is(err, ErrClosed) {
				t.Errorf("Update 只应成功或 ErrClosed，得到 %v", err)
			}
		}(i)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	wg.Wait()
	if err := <-closeDone; err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := s.Update(func(*State) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后 Update 应 ErrClosed，得到 %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close 应幂等: %v", err)
	}
}

// TestStoreConcurrentUpdatesDiskState 并发完成后磁盘状态断言：N 个并发事务全部落盘，
// 磁盘快照与内存一致、逐条可查（单写串行保证并发更新不丢）。
func TestStoreConcurrentUpdatesDiskState(t *testing.T) {
	s := openTestStore(t)
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("td-%d", i)
			if err := s.Update(func(st *State) error {
				st.Tasks[id] = protocol.Task{ID: id}
				return nil
			}); err != nil {
				t.Errorf("Update: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(s.View().Tasks); got != n {
		t.Errorf("内存任务数=%d, 期望 %d", got, n)
	}
	disk := readDiskState(t, s)
	if len(disk.Tasks) != n {
		t.Errorf("磁盘任务数=%d, 期望 %d（并发完成后落盘不得丢）", len(disk.Tasks), n)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("td-%d", i)
		if _, ok := disk.Tasks[id]; !ok {
			t.Errorf("磁盘缺任务 %s", id)
		}
	}
}

// TestStoreClosed Close 后 Update 返回 ErrClosed，Close 幂等。
func TestStoreClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Update(func(*State) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后 Update 应返回 ErrClosed，得到 %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close 应幂等: %v", err)
	}
}

// TestStoreOpenLockConflict 同一 data_dir 第二个 Store 打开必须因 flock 冲突失败。
func TestStoreOpenLockConflict(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("同一 data_dir 第二个 Store 应因 flock 冲突打开失败")
	}
}
