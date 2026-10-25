package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// workCreateReq 测试建单请求捷径：goal + 三步计划（3 依赖 1、2）。
func workCreateReq(goal string) WorkCreateRequest {
	return WorkCreateRequest{
		Goal: goal,
		Steps: []WorkStepInput{
			{Desc: "盘点磁盘", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
			{Desc: "盘点内存", TargetNode: "mac-mini", TargetAgent: "diskcheck"},
			{Desc: "汇总报告", TargetNode: "mac-mini", TargetAgent: "reporter", DependsOn: []int64{1, 2}},
		},
	}
}

// readDiskWorkState 读盘上快照（绕过内存视图；验收「落盘一致/零污染」口径用。
// 文件名取 store 包落盘约定 hub_state.json）。
func readDiskWorkState(t *testing.T, dir string) store.State {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "hub_state.json"))
	if err != nil {
		t.Fatalf("读快照: %v", err)
	}
	var st store.State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析快照: %v", err)
	}
	return st
}

// reopenHub 重启（Close → 同目录 Open → 新 Core），返回新基座（W1 验收 1/2/4
// 的重启后口径）。调用方须已 Close 旧 store（data_dir 单实例 flock）。
func reopenHub(t *testing.T, dir string) (*store.Store, *Core) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("重开 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := New(Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("重构造 core: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return st, c
}

// assertWorkEqual Work 逐字段全量比对（W1 验收 4「重启后逐字段一致」口径，
// codex 审查必须修 3④）：标量、时间（Equal 消除序列化时钟形态差异）、终态
// 时间指针（只比值不比指针）、Steps/Attempts/Events 深层切片逐元素。新增
// Work 字段须同步扩本函数——防重启比对面静默缩水。
func assertWorkEqual(t *testing.T, got, want store.Work) {
	t.Helper()
	bad := func(what string, g, w any) {
		t.Errorf("%s 不一致: got %v want %v", what, g, w)
	}
	if got.ID != want.ID {
		bad("id", got.ID, want.ID)
	}
	if got.Goal != want.Goal {
		bad("goal", got.Goal, want.Goal)
	}
	if got.PrimeTerm != want.PrimeTerm {
		bad("prime_term", got.PrimeTerm, want.PrimeTerm)
	}
	if got.Status != want.Status {
		bad("status", got.Status, want.Status)
	}
	if got.Report != want.Report {
		bad("report", got.Report, want.Report)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		bad("created_at", got.CreatedAt, want.CreatedAt)
	}
	// 终态时间指针：同为 nil 或指向相同时刻（Clone 后不得共享指针——只比值）
	if (got.ClosedAt == nil) != (want.ClosedAt == nil) || (got.ClosedAt != nil && !got.ClosedAt.Equal(*want.ClosedAt)) {
		bad("closed_at", got.ClosedAt, want.ClosedAt)
	}
	if (got.ExpiresAt == nil) != (want.ExpiresAt == nil) || (got.ExpiresAt != nil && !got.ExpiresAt.Equal(*want.ExpiresAt)) {
		bad("expires_at", got.ExpiresAt, want.ExpiresAt)
	}
	if got.EventSeq != want.EventSeq {
		bad("event_seq", got.EventSeq, want.EventSeq)
	}
	if len(got.Steps) != len(want.Steps) {
		t.Fatalf("steps 数不一致: %d want %d", len(got.Steps), len(want.Steps))
	}
	for i := range want.Steps {
		if !reflect.DeepEqual(got.Steps[i], want.Steps[i]) {
			bad(fmt.Sprintf("steps[%d]", i), got.Steps[i], want.Steps[i])
		}
	}
	if len(got.Attempts) != len(want.Attempts) {
		t.Fatalf("attempts 数不一致: %d want %d", len(got.Attempts), len(want.Attempts))
	}
	for i := range want.Attempts {
		if !reflect.DeepEqual(got.Attempts[i], want.Attempts[i]) {
			bad(fmt.Sprintf("attempts[%d]", i), got.Attempts[i], want.Attempts[i])
		}
	}
	if len(got.Events) != len(want.Events) {
		t.Fatalf("events 数不一致: %d want %d", len(got.Events), len(want.Events))
	}
	for i, ge := range got.Events {
		we := want.Events[i]
		if ge.Seq != we.Seq || ge.Kind != we.Kind || ge.By != we.By ||
			ge.Principal != we.Principal || ge.Body != we.Body || !ge.At.Equal(we.At) {
			bad(fmt.Sprintf("events[%d]", i), ge, we)
		}
	}
}

// TestWorkCreateIdempotentResponseLostReturnsSameWork W1 验收 1：建单响应丢失
// 仍返同单——同 goal+steps 指纹重复调用（同进程/重启后）返回原 work_id，零新增
// 单零新增事件；内容变化（goal/steps 任一）不得命中。
func TestWorkCreateIdempotentResponseLostReturnsSameWork(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	bus := transport.NewBus()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}

	w1, dup, err := c.CreateWork(workCreateReq("对比双机磁盘"), "pat:mcp-test")
	if err != nil || dup {
		t.Fatalf("首建应成功非 dup: err=%v dup=%v", err, dup)
	}
	if w1.ID == "" || w1.Status != store.WorkStatusPlanning || len(w1.Steps) != 3 {
		t.Fatalf("首建返回异常: %+v", w1)
	}

	// 响应丢失重试：同指纹重复调用 → 同单 dup，账本零增长
	for i := 0; i < 3; i++ {
		w2, dup, err := c.CreateWork(workCreateReq("对比双机磁盘"), "pat:mcp-test")
		if err != nil {
			t.Fatalf("重试 %d 不应失败: %v", i, err)
		}
		if !dup || w2.ID != w1.ID {
			t.Fatalf("重试应返同单 dup=true: got dup=%v id=%s want %s", dup, w2.ID, w1.ID)
		}
	}
	v := st.View()
	if len(v.Works) != 1 || len(v.WorkIdems) != 1 {
		t.Fatalf("重复建单不得新增单/指纹: works=%d idems=%d", len(v.Works), len(v.WorkIdems))
	}
	if got := v.Works[w1.ID]; len(got.Events) != 1 || got.EventSeq != 1 {
		t.Fatalf("重复回调不得重复记账: events=%d seq=%d", len(got.Events), got.EventSeq)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启后再试（响应在 hub 重启窗口丢失的极端形态）：仍返同单
	st2, c2 := reopenHub(t, dir)
	w3, dup, err := c2.CreateWork(workCreateReq("对比双机磁盘"), "pat:mcp-test")
	if err != nil || !dup || w3.ID != w1.ID {
		t.Fatalf("重启后重试应返同单: err=%v dup=%v id=%s want %s", err, dup, w3.ID, w1.ID)
	}
	if got := len(st2.View().Works); got != 1 {
		t.Fatalf("重启后重复建单不得新增单: works=%d", got)
	}

	// 内容变化不得命中：goal 变 / steps 变（多一步 / 依赖变）各自新单
	for name, req := range map[string]WorkCreateRequest{
		"goal 变": workCreateReq("对比双机内存"),
		"多一步":    {Goal: "对比双机磁盘", Steps: append(workCreateReq("").Steps, WorkStepInput{Desc: "收尾", TargetNode: "mac-mini", TargetAgent: "x"})},
		"依赖变":    {Goal: "对比双机磁盘", Steps: []WorkStepInput{{Desc: "盘点磁盘", TargetNode: "mac-mini", TargetAgent: "diskcheck"}, {Desc: "汇总报告", TargetNode: "mac-mini", TargetAgent: "reporter", DependsOn: []int64{1}}}},
		"目标变":    {Goal: "对比双机磁盘", Steps: []WorkStepInput{{Desc: "盘点磁盘", TargetNode: "baidu-node", TargetAgent: "diskcheck"}}},
	} {
		w, dup, err := c2.CreateWork(req, "pat:mcp-test")
		if err != nil {
			t.Fatalf("%s 建单不应失败: %v", name, err)
		}
		if dup || w.ID == w1.ID {
			t.Fatalf("%s 应视为不同内容新单: dup=%v id=%s", name, dup, w.ID)
		}
	}
	if got := len(st2.View().Works); got != 5 {
		t.Fatalf("内容变化应各建新单: works=%d want 5", got)
	}
}

// TestWorkReloadRoundTrip W1 验收 2 全链（codex 审查必须修 3②）：不含任何 Work
// 字段的旧版快照加载（normalize 迁移）→ 既有实体逐字段完整、Works/WorkIdems
// 归一空 → **真实 CreateWork 入口**建单 → 重启（Close→Open→新 Core）→ Work 全
// 字段恢复、幂等指纹仍命中（建单响应在重启窗口丢失后重试的真实形态）、旧实体
// 不受影响。store 层归一单元用例见 store 包 TestWorkOldSnapshotCompatible。
func TestWorkReloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old := `{
  "nodes": {"n1": {"epoch": 3, "agents": [{"name": "diskcheck", "kind": "cli", "input_mode": "prompt_text", "output_mode": "answer+artifacts", "interactive": false, "permission_mode": "sandbox", "protocol_ver": 1, "exposed": true}]}},
  "tasks": {"t1": {"id": "t1", "trace_id": "tr", "target_node": "n1", "target_agent": "diskcheck", "submitted_by": "admin", "status": "completed", "created_at": "2026-10-24T08:00:00Z", "updated_at": "2026-10-24T08:01:00Z", "expire_at": "2026-10-31T08:00:00Z"}},
  "mailboxes": {},
  "batches": {},
  "batch_idems": {},
  "workdir_locks": {}
}`
	if err := os.WriteFile(filepath.Join(dir, "hub_state.json"), []byte(old), 0o644); err != nil {
		t.Fatalf("预置旧快照: %v", err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("旧快照加载应成功: %v", err)
	}
	v := st.View()
	if v.Nodes["n1"].Epoch != 3 || len(v.Nodes["n1"].Agents) != 1 || v.Nodes["n1"].Agents[0].Name != "diskcheck" {
		t.Errorf("旧实体 nodes 应完整: %+v", v.Nodes)
	}
	if got := v.Tasks["t1"]; got.ID != "t1" || got.Status != "completed" || got.TargetNode != "n1" {
		t.Errorf("旧实体 tasks 应完整: %+v", got)
	}
	if v.Works == nil || v.WorkIdems == nil || len(v.Works) != 0 || len(v.WorkIdems) != 0 {
		t.Errorf("Work map 应归一为非 nil 空集: %+v %+v", v.Works, v.WorkIdems)
	}

	// 真实建单入口（自报 created_by=mcp + 服务端主体）
	c, err := New(Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	req := WorkCreateRequest{Goal: "接管前巡检", PrimeTerm: "prime-1", CreatedBy: "mcp", Steps: workCreateReq("").Steps}
	w, dup, err := c.CreateWork(req, "pat:mcp-test")
	if err != nil || dup || w.ID == "" {
		t.Fatalf("旧快照上建单应成功: err=%v dup=%v", err, dup)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启：全字段恢复 + 幂等指纹仍命中 + 旧实体保留
	st2, c2 := reopenHub(t, dir)
	got, ok := st2.View().Works[w.ID]
	if !ok {
		t.Fatal("重启后 Work 丢失")
	}
	assertWorkEqual(t, got, w.Work)
	if got := st2.View().Nodes["n1"]; got.Epoch != 3 || len(got.Agents) != 1 {
		t.Errorf("重启后旧实体 nodes 丢失: %+v", got)
	}
	if got := st2.View().Tasks["t1"]; got.ID != "t1" || got.Status != "completed" {
		t.Errorf("重启后旧实体 tasks 丢失: %+v", got)
	}
	w2, dup, err := c2.CreateWork(req, "pat:mcp-test")
	if err != nil || !dup || w2.ID != w.ID {
		t.Fatalf("重启后同指纹应返同单（含依赖指纹重算）: err=%v dup=%v id=%s", err, dup, w2.ID)
	}
	assertWorkEqual(t, w2.Work, w.Work)
	n := 0
	for _, id := range st2.View().WorkIdems {
		n++
		if id != w.ID {
			t.Errorf("指纹映射应指向原单: %s", id)
		}
	}
	if n != 1 {
		t.Errorf("指纹映射应恰 1 条: %d", n)
	}
}

// TestWorkFactsAndEventsConsistent W1 验收 4：事实与事件一致——建单事实与
// work_created 事件同事务可见（无「有事实无事件」窗口）；服务端验证主体独立
// 落账（与自报 created_by 分开存储，RFC 六.2）；dup 零记账；滚动裁剪后 EventSeq
// 单调不回退且事实不受影响；重启后事实与事件**逐字段全量**一致（含 Attempts
// 非空场景，codex 审查必须修 3④）。
func TestWorkFactsAndEventsConsistent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	clock := newFakeClock()
	c, err := New(Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), Now: clock.Now, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}

	w, dup, err := c.CreateWork(WorkCreateRequest{Goal: "全量盘点", PrimeTerm: "prime-t1", CreatedBy: "mcp", Steps: workCreateReq("").Steps}, "pat:mcp-test")
	if err != nil || dup {
		t.Fatalf("建单失败: err=%v dup=%v", err, dup)
	}
	// 事实与事件同事务：视图即完整（1 事件 + 全 pending 步骤 + planning）
	if len(w.Events) != 1 {
		t.Fatalf("建单应恰 1 条事件: %d", len(w.Events))
	}
	ev := w.Events[0]
	if ev.Seq != 1 || w.EventSeq != 1 || ev.Kind != WorkEventCreated || ev.By != "mcp" {
		t.Fatalf("首事件异常: %+v event_seq=%d", ev, w.EventSeq)
	}
	if ev.Principal != "pat:mcp-test" {
		t.Fatalf("服务端验证主体应独立落账: %+v", ev)
	}
	if !ev.At.Equal(clock.Now()) {
		t.Fatalf("事件时刻应取 hub 时钟: %v want %v", ev.At, clock.Now())
	}
	if w.Status != store.WorkStatusPlanning {
		t.Fatalf("建单即 planning, got %s", w.Status)
	}
	for _, s := range w.Steps {
		if s.Status != store.StepStatusPending {
			t.Fatalf("建单步骤应全 pending: %+v", s)
		}
	}
	// dup 命中零记账
	if _, dup, _ = c.CreateWork(WorkCreateRequest{Goal: "全量盘点", Steps: workCreateReq("").Steps}, ""); !dup {
		t.Fatal("应命中 dup")
	}
	if got := st.View().Works[w.ID]; len(got.Events) != 1 || got.EventSeq != 1 {
		t.Fatalf("dup 命中不得新增事件: events=%d seq=%d", len(got.Events), got.EventSeq)
	}

	// 预置执行事实（W1 无派发入口，按 W2 形态经事务补尝试记录）：让重启全量
	// 比对覆盖 Attempts 非空场景
	err = st.Update(func(s *store.State) error {
		w := s.Works[w.ID]
		w.Attempts = []store.WorkAttempt{
			{Seq: 1, StepSeq: 1, TaskID: "t-1", IdemKey: "idem-a", Outcome: store.AttemptOutcomeDone},
			{Seq: 2, StepSeq: 2, IdemKey: "idem-b", Outcome: store.AttemptOutcomeSubmitted},
		}
		s.Works[w.ID] = w
		return nil
	})
	if err != nil {
		t.Fatalf("预置尝试: %v", err)
	}
	want := st.View().Works[w.ID] // 关停前最后已发布状态 = 全量比对基准
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启后逐字段全量一致（含 Steps 依赖、Attempts、Events 主体/时刻/正文）
	st2, _ := reopenHub(t, dir)
	got, ok := st2.View().Works[w.ID]
	if !ok {
		t.Fatal("重启后工作单丢失")
	}
	assertWorkEqual(t, got, want)

	// 滚动裁剪后：EventSeq 单调不回退，事实（steps/attempts）不受影响
	trimmedAt := clock.Now()
	for i := 0; i < store.MaxWorkEvents+5; i++ {
		err := st2.Update(func(s *store.State) error {
			w := s.Works[w.ID]
			appendWorkEvent(&w, trimmedAt, "step_updated", "test", "", fmt.Sprintf("evt-%d", i))
			s.Works[w.ID] = w
			return nil
		})
		if err != nil {
			t.Fatalf("追加事件 %d: %v", i, err)
		}
	}
	want = st2.View().Works[w.ID]
	if want.EventSeq != int64(store.MaxWorkEvents+6) {
		t.Fatalf("EventSeq 应单调累计: %d want %d", want.EventSeq, store.MaxWorkEvents+6)
	}
	if len(want.Events) != store.MaxWorkEvents {
		t.Fatalf("滚动窗口应保留最新 %d 条: %d", store.MaxWorkEvents, len(want.Events))
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st3, _ := reopenHub(t, dir)
	got = st3.View().Works[w.ID]
	assertWorkEqual(t, got, want)
	if got.Events[0].Body != fmt.Sprintf("evt-%d", 5) { // 最老保留：1+5 条被裁，现存最旧 seq=6
		t.Fatalf("窗口头部应为 evt-5: %s", got.Events[0].Body)
	}
	if len(got.Steps) != 3 || len(got.Attempts) != 2 {
		t.Fatalf("裁剪不得影响事实: steps=%d attempts=%d", len(got.Steps), len(got.Attempts))
	}
}

// TestWorkCreateWriteFailureNotPublished W1 验收 3（codex 审查必须修 3③）：
// **真实 CreateWork 入口**遇落盘失败（store 临时写路径被目录占位，对任何用户
// 确定失败）→ 返回错误（非校验错）、内存 View/幂等指纹/盘上快照零残留、临时
// 路径无半截文件；移除障碍重试成功且内存盘一致，随后幂等命中。store 层事务
// 回滚语义由 store 包 TestStoreWriteFailureNoPublishNoAck 覆盖，本例断言入口行为。
func TestWorkCreateWriteFailureNotPublished(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	c, err := New(Config{Store: st, Transport: transport.NewBus().Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core: %v", err)
	}
	tmpPath := filepath.Join(dir, "hub_state.json.tmp") // store 落盘临时写路径约定
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatalf("占位临时路径: %v", err)
	}
	w, dup, err := c.CreateWork(workCreateReq("写失败不发布"), "admin")
	if err == nil {
		t.Fatal("落盘失败 CreateWork 必须返回错误（不 Ack）")
	}
	if errors.Is(err, ErrInvalidWorkSteps) || errors.Is(err, ErrInvalid) {
		t.Fatalf("存储故障不得误报为计划校验错误: %v", err)
	}
	if w != nil || dup {
		t.Fatalf("失败不得返回视图: w=%v dup=%v", w, dup)
	}
	if got := st.View(); len(got.Works) != 0 || len(got.WorkIdems) != 0 {
		t.Errorf("落盘失败内存零残留: works=%d idems=%d", len(got.Works), len(got.WorkIdems))
	}
	// 盘上零污染：快照（可能含 core 启动自愈事务写入的既有内容）不得出现该
	// Work 或指纹（New 时已有快照文件，断言口径为内容不含失败单）
	if disk := readDiskWorkState(t, dir); len(disk.Works) != 0 || len(disk.WorkIdems) != 0 {
		t.Errorf("落盘失败快照零污染: works=%+v idems=%+v", disk.Works, disk.WorkIdems)
	}
	if ents, err := os.ReadDir(tmpPath); err != nil || len(ents) != 0 {
		t.Errorf("临时写路径应无半截文件: ents=%v err=%v", ents, err)
	}

	// 移除障碍重试 → 成功、内存盘一致
	if err := os.Remove(tmpPath); err != nil {
		t.Fatalf("移除障碍: %v", err)
	}
	w2, dup, err := c.CreateWork(workCreateReq("写失败不发布"), "admin")
	if err != nil || dup || w2 == nil {
		t.Fatalf("障碍移除后重试应成功: err=%v dup=%v", err, dup)
	}
	if _, ok := st.View().Works[w2.ID]; !ok {
		t.Error("重试应发布到内存")
	}
	disk := readDiskWorkState(t, dir)
	if got, ok := disk.Works[w2.ID]; !ok || got.Goal != "写失败不发布" || len(got.Steps) != 3 {
		t.Errorf("重试后落盘应含该 Work: %+v", got)
	}
	if len(disk.WorkIdems) != 1 {
		t.Errorf("重试后指纹映射应恰 1 条: %+v", disk.WorkIdems)
	}
	// 首次失败事务不占指纹/ID：再次同指纹调用幂等命中重试单
	if w3, dup, err := c.CreateWork(workCreateReq("写失败不发布"), "admin"); err != nil || !dup || w3.ID != w2.ID {
		t.Fatalf("重试后同指纹应幂等命中: err=%v dup=%v id=%s want %s", err, dup, w3.ID, w2.ID)
	}
}

// TestWorkStepValidation 步骤计划校验（RFC 二.2）：全违规形态 ErrInvalidWorkSteps
// （空 goal 归 ErrInvalid），校验拒绝零发布（写失败不发布的 mutate 拒绝路径）；
// 合法 DAG（菱形/5 步深度边界）通过。
func TestWorkStepValidation(t *testing.T) {
	f := newHub(t)
	base := workCreateReq("基准").Steps

	cases := []struct {
		name string
		req  WorkCreateRequest
		want error
	}{
		{"空 goal", WorkCreateRequest{Steps: base}, ErrInvalid},
		{"空步骤", WorkCreateRequest{Goal: "g"}, ErrInvalidWorkSteps},
		{"空 desc", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{TargetNode: "n", TargetAgent: "a"}}}, ErrInvalidWorkSteps},
		{"空 target", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{Desc: "d", TargetAgent: "a"}}}, ErrInvalidWorkSteps},
		{"依赖越界 0", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{Desc: "d", TargetNode: "n", TargetAgent: "a", DependsOn: []int64{0}}}}, ErrInvalidWorkSteps},
		{"依赖越界超 N", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{Desc: "d", TargetNode: "n", TargetAgent: "a", DependsOn: []int64{2}}}}, ErrInvalidWorkSteps},
		{"自引用", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{Desc: "d", TargetNode: "n", TargetAgent: "a", DependsOn: []int64{1}}}}, ErrInvalidWorkSteps},
		{"重复依赖", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{{Desc: "d", TargetNode: "n", TargetAgent: "a"}, {Desc: "e", TargetNode: "n", TargetAgent: "a", DependsOn: []int64{1, 1}}}}, ErrInvalidWorkSteps},
		{"依赖环", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{
			{Desc: "a", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{2}},
			{Desc: "b", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{1}}}}, ErrInvalidWorkSteps},
		{"深度超限(6步链)", WorkCreateRequest{Goal: "g", Steps: []WorkStepInput{
			{Desc: "1", TargetNode: "n", TargetAgent: "x"},
			{Desc: "2", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{1}},
			{Desc: "3", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{2}},
			{Desc: "4", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{3}},
			{Desc: "5", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{4}},
			{Desc: "6", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{5}}}}, ErrInvalidWorkSteps},
		{"级联超限(依赖10步)", func() WorkCreateRequest {
			steps := []WorkStepInput{{Desc: "根", TargetNode: "n", TargetAgent: "x"}}
			for i := 2; i <= 10; i++ {
				steps = append(steps, WorkStepInput{Desc: fmt.Sprintf("s%d", i), TargetNode: "n", TargetAgent: "x"})
			}
			deps := make([]int64, 9)
			for i := range deps {
				deps[i] = int64(i + 1)
			}
			steps = append(steps, WorkStepInput{Desc: "汇聚", TargetNode: "n", TargetAgent: "x", DependsOn: deps})
			return WorkCreateRequest{Goal: "g", Steps: steps}
		}(), ErrInvalidWorkSteps},
		{"步骤数超上限", func() WorkCreateRequest {
			steps := make([]WorkStepInput, MaxWorkSteps+1)
			for i := range steps {
				steps[i] = WorkStepInput{Desc: fmt.Sprintf("s%d", i), TargetNode: "n", TargetAgent: "x"}
			}
			return WorkCreateRequest{Goal: "g", Steps: steps}
		}(), ErrInvalidWorkSteps},
	}
	for _, tc := range cases {
		if _, _, err := f.core.CreateWork(tc.req, "pat:mcp-test"); !errors.Is(err, tc.want) {
			t.Fatalf("%s: 应 %v, got %v", tc.name, tc.want, err)
		}
	}
	if v := f.st.View(); len(v.Works) != 0 || len(v.WorkIdems) != 0 {
		t.Fatalf("校验拒绝必须零发布: works=%d idems=%d", len(v.Works), len(v.WorkIdems))
	}

	// 合法形态：菱形（深度 2、级联 3）与 5 步链（深度 4=上限）通过
	diamond := []WorkStepInput{
		{Desc: "根", TargetNode: "n", TargetAgent: "x"},
		{Desc: "左", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{1}},
		{Desc: "右", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{1}},
		{Desc: "汇", TargetNode: "n", TargetAgent: "x", DependsOn: []int64{3, 2}},
	}
	if _, dup, err := f.core.CreateWork(WorkCreateRequest{Goal: "菱形", Steps: diamond}, "pat:mcp-test"); err != nil || dup {
		t.Fatalf("菱形应通过: err=%v dup=%v", err, dup)
	}
	chain := make([]WorkStepInput, 5)
	for i := range chain {
		chain[i] = WorkStepInput{Desc: fmt.Sprintf("s%d", i+1), TargetNode: "n", TargetAgent: "x"}
		if i > 0 {
			chain[i].DependsOn = []int64{int64(i)}
		}
	}
	if _, _, err := f.core.CreateWork(WorkCreateRequest{Goal: "五步链", Steps: chain}, "pat:mcp-test"); err != nil {
		t.Fatalf("5 步链（深度 4）应通过: %v", err)
	}
}

// TestWorkEventsRollingRetentionAndCursor 账本滚动保留（200，沿群消息模式）与
// 游标读取：裁剪留最新窗口、EventSeq 不回退、pruned_before_events=现存最旧 seq-1、
// 游标只返增量、裁剪不动事实。
func TestWorkEventsRollingRetentionAndCursor(t *testing.T) {
	f := newHub(t)
	w, _, err := f.core.CreateWork(workCreateReq("账本窗口"), "pat:mcp-test")
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	total := store.MaxWorkEvents + 6
	for i := 1; i <= total-1; i++ { // 建单已占 seq=1
		err := f.st.Update(func(s *store.State) error {
			w := s.Works[w.ID]
			appendWorkEvent(&w, at, "step_updated", "test", "", fmt.Sprintf("e%d", i+1))
			s.Works[w.ID] = w
			return nil
		})
		if err != nil {
			t.Fatalf("追加 %d: %v", i, err)
		}
	}

	v, ok := f.core.GetWork(w.ID, 0)
	if !ok {
		t.Fatal("查单失败")
	}
	if len(v.Events) != store.MaxWorkEvents {
		t.Fatalf("窗口应 %d 条: %d", store.MaxWorkEvents, len(v.Events))
	}
	if v.EventSeq != int64(total) || v.Events[0].Seq != int64(total-store.MaxWorkEvents+1) {
		t.Fatalf("seq 口径: event_seq=%d oldest=%d want oldest=%d", v.EventSeq, v.Events[0].Seq, total-store.MaxWorkEvents+1)
	}
	if v.PrunedBeforeEvents != v.Events[0].Seq-1 {
		t.Fatalf("pruned 口径: %d want %d", v.PrunedBeforeEvents, v.Events[0].Seq-1)
	}
	// 游标增量：since=最旧保留-1 → 全窗口；since=最新-2 → 末 2 条；since≥最新 → 空
	full, _ := f.core.GetWork(w.ID, v.PrunedBeforeEvents)
	if len(full.Events) != store.MaxWorkEvents {
		t.Fatalf("对齐游标后应全量: %d", len(full.Events))
	}
	tail, _ := f.core.GetWork(w.ID, int64(total-2))
	if len(tail.Events) != 2 || tail.Events[0].Seq != int64(total-1) || tail.Events[1].Seq != int64(total) {
		t.Fatalf("尾部游标异常: %+v", tail.Events)
	}
	empty, _ := f.core.GetWork(w.ID, int64(total))
	if len(empty.Events) != 0 {
		t.Fatalf("最新游标后应为空: %d", len(empty.Events))
	}
	if len(v.Steps) != 3 || v.Status != store.WorkStatusPlanning {
		t.Fatalf("裁剪不得影响事实: steps=%d status=%s", len(v.Steps), v.Status)
	}
}

// TestWorkListWorks 列表摘要：创建时间倒序、同刻按 ID 稳定；摘要不含事件明细；
// 未终态摘要不输出 closed_at（指针承载 omitempty——time.Time 值形态的 omitempty
// 对结构体零值失效，codex 审查建议修 5 回归口径）。
func TestWorkListWorks(t *testing.T) {
	f := newHub(t)
	ids := make([]string, 0, 3)
	for _, g := range []string{"第一个", "第二个", "第三个"} {
		w, _, err := f.core.CreateWork(workCreateReq(g), "pat:mcp-test")
		if err != nil {
			t.Fatalf("建单 %s: %v", g, err)
		}
		ids = append(ids, w.ID)
		f.clock.Advance(time.Second)
	}
	list := f.core.ListWorks()
	if len(list) != 3 {
		t.Fatalf("列表应 3 条: %d", len(list))
	}
	if list[0].ID != ids[2] || list[1].ID != ids[1] || list[2].ID != ids[0] {
		t.Fatalf("应新建在前: %v", list)
	}
	if list[0].Steps != 3 || list[0].Status != store.WorkStatusPlanning {
		t.Fatalf("摘要字段异常: %+v", list[0])
	}
	b, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("摘要序列化: %v", err)
	}
	if strings.Contains(string(b), "closed_at") {
		t.Errorf("未终态摘要不应输出 closed_at: %s", b)
	}
	if list[0].ClosedAt != nil {
		t.Errorf("未终态摘要 ClosedAt 应为 nil: %v", list[0].ClosedAt)
	}
	if _, ok := f.core.GetWork("w-nonexistent", 0); ok {
		t.Fatal("未知 ID 不应命中")
	}
	if _, ok := f.core.GetWork(protocol.NewWorkID(), 0); ok {
		t.Fatal("不存在 ID 不应命中")
	}
}
