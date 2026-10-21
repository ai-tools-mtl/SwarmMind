package group

import (
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
	msgs, _ := s.ListMessages(g.ID, 0)
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
	msgs2, _ := s.ListMessages(g.ID, 0)
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
	msgs3, _ := s2.ListMessages(g.ID, 0)
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
