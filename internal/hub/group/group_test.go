package group

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
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
	all, _, err := s.ListMessages(g.ID, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("since=0 应全量 5 条, got %d err=%v", len(all), err)
	}
	inc, _, err := s.ListMessages(g.ID, 3)
	if err != nil || len(inc) != 2 || inc[0].Seq != 4 || inc[1].Seq != 5 {
		t.Fatalf("since=3 应返回 seq 4,5, got %+v err=%v", inc, err)
	}
	neg, _, err := s.ListMessages(g.ID, -1)
	if err != nil || len(neg) != 5 {
		t.Fatalf("负 since 应按 0 全量, got %d err=%v", len(neg), err)
	}
	if _, _, err := s.ListMessages("g-none", 0); !errors.Is(err, ErrNotFound) {
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
	if again.ClosedBy != "admin" || again.ClosedAt == nil || closed.ClosedAt == nil || !again.ClosedAt.Equal(*closed.ClosedAt) {
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
	before, _, _ := s.ListMessages(g.ID, 0)
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

	// 中文多字节边界（UTF-8 每汉字 3 字节）：证明 len() 是字节口径而非字符数——
	// 2730 字=8190 字节恰好放行，2731 字=8193 字节拒绝。
	hin := strings.Repeat("汉", 2730)
	if got := len(hin); got != MaxBodyBytes-2 {
		t.Fatalf("用例自检: 2730 汉字应 %d 字节, got %d", MaxBodyBytes-2, got)
	}
	if _, err := s.PostMessage(g.ID, "admin", KindUser, hin); err != nil {
		t.Fatalf("%d 字节中文应放行: %v", len(hin), err)
	}
	hout := strings.Repeat("汉", 2731)
	if _, err := s.PostMessage(g.ID, "admin", KindUser, hout); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("%d 字节中文应 ErrBodyTooLarge, got %v", len(hout), err)
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
	msgs, _, err := s2.ListMessages(g1.ID, 0)
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
	if _, _, err := s2.ListMessages("g-none", 0); !errors.Is(err, ErrNotFound) {
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

// TestLoadNormalizesBadBudget 覆盖：手改快照把 max_rounds 改成 0/负值后重开
// store，加载时归一为默认 20——预算不被误判为立即耗尽（首条 agent 消息可发）；
// 归一后的预算随先写后见落盘覆盖手改值。
func TestLoadNormalizesBadBudget(t *testing.T) {
	dir := t.TempDir()
	snapshot := `{
  "groups": {
    "g-zero": {"group_id": "g-zero", "goal": "零预算", "status": "discussing", "host": "admin", "members": [], "budget": {"max_rounds": 0}, "created_at": "2026-10-22T00:00:00Z"},
    "g-neg":  {"group_id": "g-neg",  "goal": "负预算", "status": "discussing", "host": "admin", "members": [], "budget": {"max_rounds": -5}, "created_at": "2026-10-22T00:00:01Z"}
  },
  "messages": {"g-zero": [], "g-neg": []},
  "seqs": {"g-zero": 0, "g-neg": 0}
}`
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(snapshot), 0o644); err != nil {
		t.Fatalf("写手改快照: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开手改快照: %v", err)
	}
	defer s.Close()
	for _, gid := range []string{"g-zero", "g-neg"} {
		g, ok := s.GetGroup(gid)
		if !ok {
			t.Fatalf("%s 应可查", gid)
		}
		if g.Budget.MaxRounds != DefaultMaxRounds {
			t.Fatalf("%s 预算应归一为 %d, got %d", gid, DefaultMaxRounds, g.Budget.MaxRounds)
		}
		// 行为验证：0/负预算不会被立即判耗尽，agent 消息可发
		if _, err := s.PostMessage(gid, "a", KindAgent, "first"); err != nil {
			t.Fatalf("%s 首条 agent 消息应放行, got %v", gid, err)
		}
	}
	// 归一随变更落盘：重启后手改值已被覆盖
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	defer s2.Close()
	g, _ := s2.GetGroup("g-zero")
	if g.Budget.MaxRounds != DefaultMaxRounds {
		t.Fatalf("落盘后预算应保持 %d, got %d", DefaultMaxRounds, g.Budget.MaxRounds)
	}
}

// TestAddTaskRef 覆盖（GROUP-RFC 转任务闭环）：登记追加去重（幂等）、随快照
// 持久化（重启恢复）、未知群 ErrNotFound、空 task_id ErrInvalid、concluded 群
// ErrConcluded。
func TestAddTaskRef(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	g, _ := s.CreateGroup("goal", "admin", nil)
	if err := s.AddTaskRef(g.ID, "t-1"); err != nil {
		t.Fatalf("登记任务引用: %v", err)
	}
	// 重复登记幂等：不产生重复条目
	if err := s.AddTaskRef(g.ID, " t-1 "); err != nil {
		t.Fatalf("重复登记应幂等成功: %v", err)
	}
	if err := s.AddTaskRef(g.ID, "t-2"); err != nil {
		t.Fatalf("登记第二条: %v", err)
	}
	got, ok := s.GetGroup(g.ID)
	if !ok || len(got.TaskRefs) != 2 || got.TaskRefs[0] != "t-1" || got.TaskRefs[1] != "t-2" {
		t.Fatalf("引用列表应 [t-1 t-2]（去重）, got %+v ok=%v", got.TaskRefs, ok)
	}
	// 参数与存在性校验
	if err := s.AddTaskRef(g.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 task_id 应 ErrInvalid, got %v", err)
	}
	if err := s.AddTaskRef("g-none", "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知群应 ErrNotFound, got %v", err)
	}
	// concluded 群拒绝登记（创建注记要求群可收消息）
	if _, err := s.CloseGroup(g.ID, "admin"); err != nil {
		t.Fatalf("关群: %v", err)
	}
	if err := s.AddTaskRef(g.ID, "t-3"); !errors.Is(err, ErrConcluded) {
		t.Fatalf("concluded 群应 ErrConcluded, got %v", err)
	}

	// 重启恢复：引用列表随快照持久化
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重启打开: %v", err)
	}
	defer s2.Close()
	got2, _ := s2.GetGroup(g.ID)
	if len(got2.TaskRefs) != 2 || got2.TaskRefs[0] != "t-1" || got2.TaskRefs[1] != "t-2" {
		t.Fatalf("重启后引用列表应恢复, got %+v", got2.TaskRefs)
	}
	// 旧快照兼容：无 task_refs 字段的群读取为空列表不报错
	g3, _ := s2.CreateGroup("legacy", "admin", nil)
	if refs := (Group{}).TaskRefs; refs != nil {
		t.Fatalf("零值 TaskRefs 应为 nil, got %v", refs)
	}
	if _, ok := s2.GetGroup(g3.ID); !ok {
		t.Fatal("新群应可查")
	}
}

// TestRefluxTaskResult 覆盖（GROUP-RFC 结果回流）：首次回流注入 from=hub 的
// system 消息并返回 true；重复回流幂等（false、不重复入账）；未被引用任务
// (false, nil)；agent 预算用尽后回流仍可达（system 不计预算、不改预算口径）；
// concluded 群仍回流（hub 账本注记）；已回流集合持久化（重启后不二次回流）。
func TestRefluxTaskResult(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	g, _ := s.CreateGroup("goal", "admin", nil)
	if err := s.AddTaskRef(g.ID, "t-done"); err != nil {
		t.Fatalf("登记: %v", err)
	}

	// 预算打满：agent 消息 20 条后用尽
	for i := 0; i < DefaultMaxRounds; i++ {
		if _, err := s.PostMessage(g.ID, "a", KindAgent, "round"); err != nil {
			t.Fatalf("agent 消息 %d: %v", i+1, err)
		}
	}
	if _, err := s.PostMessage(g.ID, "a", KindAgent, "over"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("预算应已用尽, got %v", err)
	}

	// 首次回流：true + 恰好一条 system 消息（预算用尽不拦截 system 类）
	ok, err := s.RefluxTaskResult("t-done", "任务 t-done 已完成：磁盘对比报告已生成")
	if err != nil || !ok {
		t.Fatalf("首次回流应 true, got %v err=%v", ok, err)
	}
	msgs, _, _ := s.ListMessages(g.ID, 0)
	last := msgs[len(msgs)-1]
	if last.Kind != KindSystem || last.From != RefluxFrom || last.Body != "任务 t-done 已完成：磁盘对比报告已生成" {
		t.Fatalf("回流消息不符: %+v", last)
	}

	// 预算不受 system 影响：回流后 agent 仍在 21 条拒绝（预算口径只数 agent）
	if _, err := s.PostMessage(g.ID, "a", KindAgent, "over2"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("system 回流不得改变预算口径, got %v", err)
	}

	// 重复回流幂等：false、消息数不变
	ok, err = s.RefluxTaskResult("t-done", "任务 t-done 已完成：磁盘对比报告已生成")
	if err != nil || ok {
		t.Fatalf("重复回流应 (false,nil), got %v err=%v", ok, err)
	}
	msgs2, _, _ := s.ListMessages(g.ID, 0)
	if len(msgs2) != len(msgs) {
		t.Fatalf("重复回流不得新增消息: %d → %d", len(msgs), len(msgs2))
	}

	// 未被引用任务：false nil（不报错）
	if ok, err := s.RefluxTaskResult("t-orphan", "任务 t-orphan 已完成：x"); err != nil || ok {
		t.Fatalf("未引用任务应 (false,nil), got %v err=%v", ok, err)
	}
	// 参数校验
	if _, err := s.RefluxTaskResult(" ", "body"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 task_id 应 ErrInvalid, got %v", err)
	}
	if _, err := s.RefluxTaskResult("t-x", "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 body 应 ErrInvalid, got %v", err)
	}
	if _, err := s.RefluxTaskResult("t-x", strings.Repeat("a", MaxBodyBytes+1)); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("超 8KiB 应 ErrBodyTooLarge, got %v", err)
	}

	// concluded 群仍回流（hub 账本注记，时间线保留可回溯）：另一条引用任务
	if _, err := s.CloseGroup(g.ID, "admin"); err != nil {
		t.Fatalf("关群: %v", err)
	}
	ok, err = s.RefluxTaskResult("t-done", "任务 t-done 执行失败：expired")
	if err != nil || ok {
		t.Fatalf("已回流任务关群后再调仍应幂等 false, got %v err=%v", ok, err)
	}
	g2, _ := s.CreateGroup("goal2", "admin", nil)
	if err := s.AddTaskRef(g2.ID, "t-late"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	if _, err := s.CloseGroup(g2.ID, "admin"); err != nil {
		t.Fatalf("关群 2: %v", err)
	}
	ok, err = s.RefluxTaskResult("t-late", "任务 t-late 已完成：晚到结果")
	if err != nil || !ok {
		t.Fatalf("concluded 群应仍可回流, got %v err=%v", ok, err)
	}

	// 已回流集合持久化：重启后同任务不再回流
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重启打开: %v", err)
	}
	defer s2.Close()
	if ok, err := s2.RefluxTaskResult("t-done", "任务 t-done 已完成：重放"); err != nil || ok {
		t.Fatalf("重启后重复回流应幂等 false, got %v err=%v", ok, err)
	}
	msgs3, _, _ := s2.ListMessages(g.ID, 0)
	n := 0
	for _, m := range msgs3 {
		if strings.Contains(m.Body, "任务 t-done") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重启重放不得二次入账, 含 t-done 消息 %d 条", n)
	}
}

// TestTaskBodyComposers 覆盖两组注记文案（GROUP-RFC group_task_created 语义）：
// 创建注记截断 100 字；终态注记截断 200 字、状态词映射、空 FinalAnswer 回退。
func TestTaskBodyComposers(t *testing.T) {
	long := strings.Repeat("描", 150)
	body := TaskCreatedBody("t-1", long)
	if !strings.HasPrefix(body, "任务 t-1 已创建（来自群）：") {
		t.Fatalf("创建注记格式不符: %q", body)
	}
	rs := []rune(strings.TrimPrefix(body, "任务 t-1 已创建（来自群）："))
	if len(rs) != 101 || strings.TrimSuffix(string(rs), "…") != strings.Repeat("描", 100) {
		t.Fatalf("描述应截断 100 字 + …, got %d 字", len(rs))
	}

	mk := func(status, answer, resErr string) protocol.Task {
		return protocol.Task{ID: "t-9", Status: protocol.TaskStatus(status),
			Result: &protocol.TaskResult{Status: status, FinalAnswer: answer, Error: resErr}}
	}
	if got := TaskTerminalBody(mk("completed", "报告内容", "")); got != "任务 t-9 已完成：报告内容" {
		t.Fatalf("completed 注记不符: %q", got)
	}
	if got := TaskTerminalBody(mk("needs_review", "结果存疑", "")); got != "任务 t-9 待人工核对：结果存疑" {
		t.Fatalf("needs_review 注记不符: %q", got)
	}
	// 空 FinalAnswer：回退错误码；连错误码也没有：占位
	if got := TaskTerminalBody(mk("failed", "", "expired")); got != "任务 t-9 执行失败：expired" {
		t.Fatalf("failed 注记应回退错误码: %q", got)
	}
	if got := TaskTerminalBody(mk("failed", "", "")); got != "任务 t-9 执行失败：（无最终答复）" {
		t.Fatalf("全空应占位: %q", got)
	}
	// FinalAnswer 截断 200 字
	ans := TaskTerminalBody(mk("completed", strings.Repeat("答", 300), ""))
	rs = []rune(strings.TrimPrefix(ans, "任务 t-9 已完成："))
	if len(rs) != 201 || strings.TrimSuffix(string(rs), "…") != strings.Repeat("答", 200) {
		t.Fatalf("FinalAnswer 应截断 200 字 + …, got %d 字", len(rs))
	}
}

// TestReadInterfacesDeepCopy 覆盖（双审批 C 修 1）：全部 Group 读/写出口
// （GetGroup/ListGroups/CreateGroup/CloseGroup）返回深拷贝——调用方对返回值
// 的 Members/TaskRefs 改写元素或 append 都不得触及 store 内部状态（否则可绕过
// 锁污染内部状态，审查例证）。
func TestReadInterfacesDeepCopy(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	defer s.Close()

	// CreateGroup 返回值污染
	created, err := s.CreateGroup("goal", "admin", []string{"a", "b"})
	if err != nil {
		t.Fatalf("建群: %v", err)
	}
	created.Members[0] = "tampered"
	created.Members = append(created.Members, "intruder")
	if g, _ := s.GetGroup(created.ID); g.Members[0] != "a" || len(g.Members) != 2 {
		t.Fatalf("CreateGroup 返回值污染不得入内: %+v", g.Members)
	}

	// GetGroup 返回值污染（含 TaskRefs）
	if err := s.AddTaskRef(created.ID, "t-1"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	got, ok := s.GetGroup(created.ID)
	if !ok {
		t.Fatal("群应可查")
	}
	got.Members[0] = "tampered"
	got.Members = append(got.Members, "intruder")
	got.TaskRefs[0] = "tampered"
	got.TaskRefs = append(got.TaskRefs, "t-intruder")
	again, _ := s.GetGroup(created.ID)
	if again.Members[0] != "a" || len(again.Members) != 2 ||
		again.TaskRefs[0] != "t-1" || len(again.TaskRefs) != 1 {
		t.Fatalf("GetGroup 返回值污染不得入内: members=%+v taskRefs=%+v", again.Members, again.TaskRefs)
	}

	// ListGroups 返回值污染
	list := s.ListGroups()
	list[0].Members[0] = "tampered"
	list[0].Members = append(list[0].Members, "intruder")
	list[0].TaskRefs[0] = "tampered"
	if g, _ := s.GetGroup(created.ID); g.Members[0] != "a" || len(g.Members) != 2 || g.TaskRefs[0] != "t-1" {
		t.Fatalf("ListGroups 返回值污染不得入内: %+v", g)
	}

	// CloseGroup 返回值污染
	closed, err := s.CloseGroup(created.ID, "admin")
	if err != nil {
		t.Fatalf("关群: %v", err)
	}
	closed.Members[0] = "tampered"
	closed.Members = append(closed.Members, "intruder")
	if g, _ := s.GetGroup(created.ID); g.Members[0] != "a" || len(g.Members) != 2 {
		t.Fatalf("CloseGroup 返回值污染不得入内: %+v", g.Members)
	}
}

// TestRollingTrim 覆盖（双审批 C 修 2，GROUP-RFC 三 200 条滚动窗口）：
// 210 条 → 只保最新 200 条（seq 11..210）、裁剪按 seq 从小到大丢；
// ListMessages since 增量对被裁区间从现存最旧起返回并带 prunedBefore 游标；
// agent 预算计数独立累计（旧 agent 消息被裁不得让预算失忆重新放行）；
// 裁剪随先写后见落盘（快照恰好 200 条），重启恢复仍 200、seq 续接、预算仍耗尽；
// 回流注入同受窗口约束（system 类也参与滚动）。
func TestRollingTrim(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	g, _ := s.CreateGroup("goal", "admin", nil)

	// 20 条 agent（seq 1..20，打满预算）+ 190 条 user（seq 21..210）= 210 条
	for i := 0; i < DefaultMaxRounds; i++ {
		if _, err := s.PostMessage(g.ID, "a", KindAgent, "round"); err != nil {
			t.Fatalf("agent %d: %v", i+1, err)
		}
	}
	for i := 0; i < 190; i++ {
		if _, err := s.PostMessage(g.ID, "admin", KindUser, "filler"); err != nil {
			t.Fatalf("user %d: %v", i+1, err)
		}
	}

	// 全量拉取：恰 200 条，seq 11..210，prunedBefore=10
	all, pruned, err := s.ListMessages(g.ID, 0)
	if err != nil || len(all) != MaxGroupMessages {
		t.Fatalf("滚动窗口应保留 %d 条, got %d err=%v", MaxGroupMessages, len(all), err)
	}
	if all[0].Seq != 11 || all[len(all)-1].Seq != 210 {
		t.Fatalf("应保留最新 seq 11..210, got %d..%d", all[0].Seq, all[len(all)-1].Seq)
	}
	if pruned != 10 {
		t.Fatalf("prunedBefore 应为 10, got %d", pruned)
	}

	// since 增量语义：since 落在被裁区间 → 从现存最旧起返回 + prunedBefore 点破缺口
	trimmed, pb, err := s.ListMessages(g.ID, 5)
	if err != nil || len(trimmed) != 200 || trimmed[0].Seq != 11 || pb != 10 {
		t.Fatalf("since=5 < prunedBefore 应从 seq 11 起全量返回, got len=%d head=%d pb=%d err=%v", len(trimmed), trimmed[0].Seq, pb, err)
	}
	inc, pb2, err := s.ListMessages(g.ID, 15)
	if err != nil || len(inc) != 195 || inc[0].Seq != 16 || pb2 != 10 {
		t.Fatalf("since=15 应返回 seq 16..210, got len=%d head=%d pb=%d err=%v", len(inc), inc[0].Seq, pb2, err)
	}
	tail, _, err := s.ListMessages(g.ID, 209)
	if err != nil || len(tail) != 1 || tail[0].Seq != 210 {
		t.Fatalf("since=209 应只返回 seq 210, got %+v err=%v", tail, err)
	}

	// 预算独立累计：窗口内现存 agent 消息只剩 10 条（seq 11..20），若按现存
	// 计数第 21 条 agent 会被误放行；独立计数应为 20 → 拒绝。
	nAgent := 0
	for _, m := range all {
		if m.Kind == KindAgent {
			nAgent++
		}
	}
	if nAgent != 10 {
		t.Fatalf("用例自检: 窗口内应剩 10 条 agent, got %d", nAgent)
	}
	if _, err := s.PostMessage(g.ID, "a", KindAgent, "over"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("agent 消息被裁后预算不得失忆: got %v", err)
	}
	// user 发言不受影响，seq 续接 211（裁剪与被拒消息都不占 seq）
	m211, err := s.PostMessage(g.ID, "admin", KindUser, "post-trim")
	if err != nil || m211.Seq != 211 {
		t.Fatalf("裁剪后 seq 应续接 211, got %d err=%v", m211.Seq, err)
	}

	// 回流注入（system）同受滚动窗口约束：窗口已满（seq 12..211）再入一条，
	// 最旧 seq 12 被挤掉，回流消息占 seq 212
	if err := s.AddTaskRef(g.ID, "t-r"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	if ok, err := s.RefluxTaskResult("t-r", "任务 t-r 已完成：回流"); err != nil || !ok {
		t.Fatalf("回流: ok=%v err=%v", ok, err)
	}
	after, pb3, _ := s.ListMessages(g.ID, 0)
	if len(after) != MaxGroupMessages || after[0].Seq != 13 || pb3 != 12 {
		t.Fatalf("回流后应滚动为 seq 13..212, got len=%d head=%d pb=%d", len(after), after[0].Seq, pb3)
	}
	if last := after[len(after)-1]; last.Seq != 212 || last.Kind != KindSystem || last.From != RefluxFrom {
		t.Fatalf("末条应为回流消息 seq 212, got %+v", last)
	}

	// 落盘裁剪：快照恰好 200 条消息（groups.json 不无限膨胀）
	s.Close()
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("读快照: %v", err)
	}
	if n := bytes.Count(b, []byte(`"msg_id"`)); n != MaxGroupMessages {
		t.Fatalf("快照应恰含 %d 条消息, got %d", MaxGroupMessages, n)
	}

	// 重启恢复：仍 200 条、prunedBefore 保持、seq 续接 213、预算仍耗尽
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重启打开: %v", err)
	}
	defer s2.Close()
	recov, pb4, err := s2.ListMessages(g.ID, 0)
	if err != nil || len(recov) != MaxGroupMessages || recov[0].Seq != 13 || pb4 != 12 {
		t.Fatalf("重启后应恢复 %d 条 seq 13..212 pb=12, got len=%d head=%d pb=%d err=%v", MaxGroupMessages, len(recov), recov[0].Seq, pb4, err)
	}
	m213, err := s2.PostMessage(g.ID, "admin", KindUser, "post-restart")
	if err != nil || m213.Seq != 213 {
		t.Fatalf("重启后 seq 应续接 213, got %d err=%v", m213.Seq, err)
	}
	if _, err := s2.PostMessage(g.ID, "a", KindAgent, "over-after-restart"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("重启后预算仍应耗尽（AgentMsgs 持久化）, got %v", err)
	}
}
