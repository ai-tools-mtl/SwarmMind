package group

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestCreateGroup 覆盖：g- 前缀 / 创建即 discussing / 默认预算 20 / 成员拷贝。
func TestCreateGroup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()

	members := []string{"a", "b"}
	g, err := s.CreateGroup(" 对比两台机器磁盘报告 ", "admin", members)
	if err != nil {
		t.Fatalf("建群: %v", err)
	}
	if !strings.HasPrefix(g.ID, GroupIDPrefix) {
		t.Fatalf("群 ID 应带 %s 前缀, got %s", GroupIDPrefix, g.ID)
	}
	if g.Status != StatusDiscussing {
		t.Fatalf("创建即 discussing, got %s", g.Status)
	}
	if g.Budget.MaxRounds != DefaultMaxRounds {
		t.Fatalf("默认预算应 %d, got %d", DefaultMaxRounds, g.Budget.MaxRounds)
	}
	if g.Host != "admin" || g.CreatedAt.IsZero() {
		t.Fatalf("host/created_at 未正确设置: %+v", g)
	}
	// 成员拷贝：改调用方切片不得影响已存群
	members[0] = "tampered"
	got := s.ListGroups()
	if len(got) != 1 || got[0].Members[0] != "a" {
		t.Fatalf("members 应为创建时快照拷贝, got %+v", got)
	}

	// 参数校验
	if _, err := s.CreateGroup("  ", "admin", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 goal 应 ErrInvalid, got %v", err)
	}
	if _, err := s.CreateGroup("goal", " ", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 host 应 ErrInvalid, got %v", err)
	}
}

// TestPostMessageSeqMonotonic 覆盖：seq 组内从 1 单调递增；并发发言 seq 严格
// 唯一不重不漏（mutex 串行化）。
func TestPostMessageSeqMonotonic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, err := s.CreateGroup("goal", "admin", []string{"a"})
	if err != nil {
		t.Fatalf("建群: %v", err)
	}

	// 串行：1,2,3…
	for i := int64(1); i <= 3; i++ {
		m, err := s.PostMessage(g.ID, "admin", KindUser, "msg")
		if err != nil {
			t.Fatalf("发言 %d: %v", i, err)
		}
		if m.Seq != i {
			t.Fatalf("seq 应单调 %d, got %d", i, m.Seq)
		}
		if !strings.HasPrefix(m.MsgID, MsgIDPrefix) || m.GroupID != g.ID {
			t.Fatalf("msg_id/group_id 未正确设置: %+v", m)
		}
	}

	// 并发：50 goroutine 各发一条 user 消息（不受预算约束），seq 应恰为 4..53 的一个排列
	const n = 50
	seqs := make(chan int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := s.PostMessage(g.ID, "a", KindUser, "concurrent")
			if err != nil {
				t.Errorf("并发发言: %v", err)
				return
			}
			seqs <- m.Seq
		}()
	}
	wg.Wait()
	close(seqs)
	seen := map[int64]bool{}
	for sq := range seqs {
		if seen[sq] {
			t.Fatalf("seq 重复: %d", sq)
		}
		seen[sq] = true
	}
	for i := int64(4); i <= 3+n; i++ {
		if !seen[i] {
			t.Fatalf("seq %d 缺失（应连续分配）", i)
		}
	}
}

// TestListMessagesIncremental 覆盖：since 增量拉取（群补齐语义）、全量、负值钳制、未知群。
func TestListMessagesIncremental(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, _ := s.CreateGroup("goal", "admin", nil)
	for i := 0; i < 5; i++ {
		if _, err := s.PostMessage(g.ID, "admin", KindUser, "m"); err != nil {
			t.Fatalf("发言 %d: %v", i, err)
		}
	}
	all, err := s.ListMessages(g.ID, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("since=0 应全量 5 条, got %d err=%v", len(all), err)
	}
	inc, err := s.ListMessages(g.ID, 3)
	if err != nil || len(inc) != 2 || inc[0].Seq != 4 || inc[1].Seq != 5 {
		t.Fatalf("since=3 应返回 seq 4,5, got %+v err=%v", inc, err)
	}
	neg, err := s.ListMessages(g.ID, -1)
	if err != nil || len(neg) != 5 {
		t.Fatalf("负 since 应按 0 全量, got %d err=%v", len(neg), err)
	}
	if _, err := s.ListMessages("g-none", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知群应 ErrNotFound, got %v", err)
	}
}

// TestCloseGroupTerminal 覆盖：concluded 终态拒收消息（非法流转拒绝）、
// 重复关闭幂等且保留首次关闭审计、未知群。
func TestCloseGroupTerminal(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, _ := s.CreateGroup("goal", "admin", nil)
	if _, err := s.PostMessage(g.ID, "admin", KindUser, "hi"); err != nil {
		t.Fatalf("关闭前发言: %v", err)
	}

	closed, err := s.CloseGroup(g.ID, "admin")
	if err != nil || closed.Status != StatusConcluded {
		t.Fatalf("关闭应 concluded, got %+v err=%v", closed, err)
	}
	if closed.ClosedBy != "admin" || closed.ClosedAt == nil {
		t.Fatalf("首次关闭应记录审计: %+v", closed)
	}
	// 终态拒收（三类 kind 全拒）
	for _, k := range []string{KindUser, KindAgent, KindSystem} {
		if _, err := s.PostMessage(g.ID, "admin", k, "late"); !errors.Is(err, ErrConcluded) {
			t.Fatalf("concluded 后拒收 %s, got %v", k, err)
		}
	}
	// 重复关闭幂等：无错误、审计不变
	again, err := s.CloseGroup(g.ID, "someone-else")
	if err != nil {
		t.Fatalf("重复关闭应幂等成功, got %v", err)
	}
	if again.ClosedBy != "admin" || again.ClosedAt != closed.ClosedAt {
		t.Fatalf("重复关闭不得改写首次审计: %+v vs %+v", again, closed)
	}
	// 未知群 / 空 by
	if _, err := s.CloseGroup("g-none", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知群关闭应 ErrNotFound, got %v", err)
	}
	if _, err := s.CloseGroup(g.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 by 应 ErrInvalid, got %v", err)
	}
}

// TestBudgetExhausted 覆盖：agent 消息恰好 max_rounds 条后第 max_rounds+1 条
// agent 消息拒绝；user/system 消息不受预算约束。
func TestBudgetExhausted(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, _ := s.CreateGroup("goal", "admin", nil)
	for i := 0; i < DefaultMaxRounds; i++ {
		if _, err := s.PostMessage(g.ID, "a", KindAgent, "round"); err != nil {
			t.Fatalf("第 %d 条 agent 消息应放行: %v", i+1, err)
		}
	}
	if _, err := s.PostMessage(g.ID, "a", KindAgent, "over"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("超预算 agent 消息应 ErrBudgetExhausted, got %v", err)
	}
	// 预算只约束 agent：user/system 仍可发
	if _, err := s.PostMessage(g.ID, "admin", KindUser, "插话"); err != nil {
		t.Fatalf("预算用尽后 user 消息不应受限: %v", err)
	}
	if _, err := s.PostMessage(g.ID, "hub", KindSystem, "system 通知"); err != nil {
		t.Fatalf("预算用尽后 system 消息不应受限: %v", err)
	}
	// 拒绝的消息不占 seq：下一条成功消息 seq 连续
	before, _ := s.ListMessages(g.ID, 0)
	m, err := s.PostMessage(g.ID, "admin", KindUser, "after")
	if err != nil {
		t.Fatalf("发言: %v", err)
	}
	if m.Seq != before[len(before)-1].Seq+1 {
		t.Fatalf("被拒消息不应消耗 seq: got %d want %d", m.Seq, before[len(before)-1].Seq+1)
	}
}

// TestBodyLimit 覆盖：8KiB 恰好放行、8KiB+1 拒绝（按字节实测）。
func TestBodyLimit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, _ := s.CreateGroup("goal", "admin", nil)

	exact := strings.Repeat("a", MaxBodyBytes)
	if _, err := s.PostMessage(g.ID, "admin", KindUser, exact); err != nil {
		t.Fatalf("%d 字节应放行: %v", MaxBodyBytes, err)
	}
	over := strings.Repeat("a", MaxBodyBytes+1)
	if _, err := s.PostMessage(g.ID, "admin", KindUser, over); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("%d 字节应 ErrBodyTooLarge, got %v", MaxBodyBytes+1, err)
	}
}

// TestPersistRestartRecovery 覆盖：重启（重开 store）后群/消息/seq 完整恢复，
// seq 续接单调；已关闭群恢复后仍终态拒收。
func TestPersistRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	g1, _ := s1.CreateGroup("goal-one", "admin", []string{"a", "b"})
	for i := 0; i < 3; i++ {
		if _, err := s1.PostMessage(g1.ID, "admin", KindUser, "m"); err != nil {
			t.Fatalf("发言: %v", err)
		}
	}
	g2, _ := s1.CreateGroup("goal-two", "admin", nil)
	if _, err := s1.CloseGroup(g2.ID, "admin"); err != nil {
		t.Fatalf("关闭: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}
	// 关闭后写拒绝
	if _, err := s1.PostMessage(g1.ID, "admin", KindUser, "x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后写应 ErrClosed, got %v", err)
	}

	// 重启：新 store 同目录
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重启打开: %v", err)
	}
	defer s2.Close()
	groups := s2.ListGroups()
	if len(groups) != 2 {
		t.Fatalf("重启后应恢复 2 个群, got %d", len(groups))
	}
	msgs, err := s2.ListMessages(g1.ID, 0)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("重启后消息应恢复 3 条, got %d err=%v", len(msgs), err)
	}
	m, err := s2.PostMessage(g1.ID, "admin", KindUser, "post-restart")
	if err != nil || m.Seq != 4 {
		t.Fatalf("重启后 seq 应续接 4, got %d err=%v", m.Seq, err)
	}
	// 已关闭群：恢复后仍终态
	if _, err := s2.PostMessage(g2.ID, "admin", KindUser, "x"); !errors.Is(err, ErrConcluded) {
		t.Fatalf("恢复的已关闭群应拒收, got %v", err)
	}
	if _, err := s2.ListMessages("g-none", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知群应 ErrNotFound, got %v", err)
	}
}

// TestInvalidMessages 覆盖：from 空 / kind 非法 / body 空白 → ErrInvalid。
func TestInvalidMessages(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()
	g, _ := s.CreateGroup("goal", "admin", nil)
	for _, tc := range []struct{ from, kind, body string }{
		{" ", KindUser, "body"},
		{"admin", "bot", "body"},
		{"admin", "", "body"},
		{"admin", KindUser, "   "},
	} {
		if _, err := s.PostMessage(g.ID, tc.from, tc.kind, tc.body); !errors.Is(err, ErrInvalid) {
			t.Fatalf("(%q,%q,%q) 应 ErrInvalid, got %v", tc.from, tc.kind, tc.body, err)
		}
	}
}
