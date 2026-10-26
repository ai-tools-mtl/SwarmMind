package store

import (
	"errors"
	"fmt"
	"path/filepath"
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

// readDiskState 直接从磁盘状态库读全量状态（独立连接，绕过内存），验证落盘内容。
func readDiskState(t *testing.T, s *Store) State {
	t.Helper()
	st, err := ReadSnapshot(filepath.Dir(s.dbPath))
	if err != nil {
		t.Fatalf("读状态库 %s: %v", s.dbPath, err)
	}
	return *st
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

// TestStoreWriteFailureNoPublishNoAck 底层库句柄关闭触发失败（SQLite 持久层无
// 每事务临时文件，介质/驱动故障以关闭句柄模拟，对任何平台确定失败）→ 断言内存
// 未变、无 Ack、盘上无该行；重开句柄（移除障碍）后重试成功。
func TestStoreWriteFailureNoPublishNoAck(t *testing.T) {
	s := openTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Tasks["t0"] = protocol.Task{ID: "t0"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	// 模拟存储故障：直接关闭底层库连接（真实 I/O 路径上的确定失败，非注入）
	if err := s.db.Close(); err != nil {
		t.Fatalf("关闭底层库: %v", err)
	}
	err := s.Update(func(st *State) error {
		st.Tasks["t9"] = protocol.Task{ID: "t9"}
		return nil
	})
	if err == nil {
		t.Fatal("写库失败必须返回错误（不 Ack）")
	}
	if _, ok := s.View().Tasks["t9"]; ok {
		t.Error("落盘失败不得发布到内存")
	}
	if disk := readDiskState(t, s); len(disk.Tasks) != 1 {
		t.Errorf("落盘失败不得产生任务行: %+v", disk.Tasks)
	}

	// 移除障碍：重开底层库句柄后重试成功
	db, err := openDB(s.dbPath)
	if err != nil {
		t.Fatalf("重开底层库: %v", err)
	}
	s.db = db
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
// after_rename / before_publish 为「盘已提交、内存未发布」崩溃窗口（2026-10-26
// 复审：diff 模式下内存基准分岔，置毒拒绝后续事务，重开以磁盘为准；JSON 全量
// 覆写时代靠全量重写自然收敛，该窗口语义已收紧）。
func TestStoreInjectionStages(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Update(func(st *State) error {
		st.Tasks["t1"] = protocol.Task{ID: "t1"}
		return nil
	}); err != nil {
		t.Fatalf("预置 Update: %v", err)
	}
	diskBefore := readDiskState(t, s)
	addTask := func(id string) func(*State) error {
		return func(st *State) error {
			st.Tasks[id] = protocol.Task{ID: id}
			return nil
		}
	}

	// before_write：盘与内存都不变（普通失败，可直接重试）
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
	if diskAfter := readDiskState(t, s); len(diskAfter.Tasks) != len(diskBefore.Tasks) {
		t.Errorf("before_write 失败不得落盘: before=%d after=%d", len(diskBefore.Tasks), len(diskAfter.Tasks))
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
	// 置毒：内存基准已与磁盘分岔，后续事务拒绝（防 diff 永久漂移）
	if err := s.Update(addTask("t9")); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("after_rename 后续事务应被拒（ErrPoisoned）: %v", err)
	}
	// 重开：以磁盘为准（t1+t2），后续事务正常
	if err := s.Close(); err != nil {
		t.Fatalf("重开前 Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if got := s2.View().Tasks; len(got) != 2 || got["t2"].ID != "t2" {
		t.Fatalf("重开应以磁盘为准（含已提交未发布的 t2）: %+v", got)
	}
	s = s2

	// before_publish：表现同 after_rename（盘新、内存未发布）→ 置毒 + 重开
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
	if err := s.Update(addTask("t9")); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("before_publish 后续事务应被拒（ErrPoisoned）: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("重开前 Close: %v", err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	t.Cleanup(func() { _ = s3.Close() })
	if got := s3.View().Tasks; len(got) != 3 || got["t3"].ID != "t3" {
		t.Fatalf("重开应以磁盘为准（含已提交未发布的 t3）: %+v", got)
	}
	s = s3

	// before_ack：内存已发布但调用方未获 Ack（模拟「发布成、确认丢」）——
	// 非「盘成内存败」分岔，不置毒，后续事务合法
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
	// before_ack 后续事务仍可正常提交（不置毒）
	if err := s.Update(addTask("t5")); err != nil {
		t.Fatalf("before_ack 后续事务应正常: %v", err)
	}
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
