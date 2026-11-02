package strategy

// discussion_test.go 有限讨论策略测试（W05-02；零网络零模型，fake 选择器）：
// 指名顺序推进 / 显式并发指名 / 发言预算按成员过滤与全员耗尽暂停 / 全局预算耗
// 尽复用 EvaluateStop / jev 失败与非法应答手选出口（不增加执行量）/ 主人手选出
// 口 / 候选登记双通道（结构化占位拒绝指明归属）/ 候选登记到定版全链（真 collab
// Store）。

import (
	"errors"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// stubSelector 选择器 fake（记录入参；member/reason/err 可编程）。
type stubSelector struct {
	member   string
	reason   string
	err      error
	called   int
	gotGoal  string
	gotCands []string
}

func (s *stubSelector) SelectSpeaker(goal string, cands []string) (string, string, error) {
	s.called++
	s.gotGoal, s.gotCands = goal, cands
	return s.member, s.reason, s.err
}

// countsOf 发言计数 fixture。
func countsOf(kv ...int64) map[string]int64 {
	// 变参形态：alice、bob 依次传（0 = 不传）。
	m := map[string]int64{}
	if len(kv) > 0 && kv[0] > 0 {
		m["alice"] = kv[0]
	}
	if len(kv) > 1 && kv[1] > 0 {
		m["bob"] = kv[1]
	}
	return m
}

// nextOf 断言指令下一棒恰为期望名单。
func wantNext(t *testing.T, d DiscussionDirective, want ...string) {
	t.Helper()
	if len(d.Next) != len(want) {
		t.Fatalf("下一棒应为 %v，得 %v（dir=%+v）", want, d.Next, d)
	}
	for i := range want {
		if d.Next[i] != want[i] {
			t.Fatalf("下一棒应为 %v，得 %v", want, d.Next)
		}
	}
}

// TestNextSpeakerSequentialSelector 顺序单棒：选择器在候选（成员名单序）中点名
// 并原样透传 goal/候选；指令只产出判定（无暂停/无手选/未收束）。
func TestNextSpeakerSequentialSelector(t *testing.T) {
	eng := newTestEngine(t)
	clk := newFakeClock()
	sel := &stubSelector{member: "bob", reason: "轮到 bob 补充反例"}
	sp := fixtureSpace("sp-ds1", "", 0)
	d := eng.NextSpeaker(sp, clk.Now(), countsOf(), DiscussionPolicy{}, sel)
	wantNext(t, d, "bob")
	if d.Done || d.Pause != nil || d.ManualNeeded != "" || d.Reason == "" {
		t.Fatalf("指令形态不符：%+v", d)
	}
	if sel.called != 1 || sel.gotGoal != sp.Goal {
		t.Fatalf("选择器入参不符：called=%d goal=%q", sel.called, sel.gotGoal)
	}
	if strings.Join(sel.gotCands, ",") != "alice,bob" {
		t.Fatalf("候选应为成员名单序 [alice bob]：%v", sel.gotCands)
	}
}

// TestNextSpeakerNoSelectorManualExit 选择器缺位：手选出口（不增加执行量——
// Next 为空），不猜测不轮转。
func TestNextSpeakerNoSelectorManualExit(t *testing.T) {
	eng := newTestEngine(t)
	d := eng.NextSpeaker(fixtureSpace("sp-ds2", "", 0), newFakeClock().Now(), countsOf(), DiscussionPolicy{}, nil)
	if len(d.Next) != 0 || d.ManualNeeded == "" {
		t.Fatalf("缺选择器应手选出口且不派发：%+v", d)
	}
}

// TestNextSpeakerJevFailureAndIllegalAnswers jev 失败/空应答/越界应答：错误如实
// 进手选出口，不增加执行量、不静默换人；判定零留痕（纯判定无副作用）。
func TestNextSpeakerJevFailureAndIllegalAnswers(t *testing.T) {
	eng := newTestEngine(t)
	clk := newFakeClock()
	sp := fixtureSpace("sp-ds3", "", 0)
	cases := []struct {
		name    string
		sel     *stubSelector
		wantSub string
	}{
		{"provider 故障", &stubSelector{err: errors.New("上游超时")}, "上游超时"},
		{"空应答", &stubSelector{member: "  "}, "为空"},
		{"越界应答（非成员）", &stubSelector{member: "ghost"}, "不在空间成员内"},
	}
	for _, tc := range cases {
		d := eng.NextSpeaker(sp, clk.Now(), countsOf(), DiscussionPolicy{}, tc.sel)
		if len(d.Next) != 0 || d.ManualNeeded == "" {
			t.Fatalf("%s：应手选出口且不派发：%+v", tc.name, d)
		}
		if !strings.Contains(d.ManualNeeded, tc.wantSub) {
			t.Fatalf("%s：错误须如实透传（含 %q）：%s", tc.name, tc.wantSub, d.ManualNeeded)
		}
	}
	// 纯判定零副作用：引擎循环事实无留痕。
	if _, ok := eng.Snapshot("sp-ds3"); ok {
		t.Fatal("讨论判定不得产生循环事实")
	}
}

// TestNextSpeakerBudgetFilterPerMember 发言预算按成员计：耗尽者退出点名候选
// （选择器只见有预算成员）；未配置上限（0=不限）恒可选。
func TestNextSpeakerBudgetFilterPerMember(t *testing.T) {
	eng := newTestEngine(t)
	clk := newFakeClock()
	sp := fixtureSpace("sp-ds4", "", 0)
	pol := DiscussionPolicy{MaxSpeechesPerMember: 1}
	sel := &stubSelector{member: "bob"}
	// alice 已耗尽：候选只剩 bob；bob 点名仍放行。
	d := eng.NextSpeaker(sp, clk.Now(), countsOf(2, 0), pol, sel)
	wantNext(t, d, "bob")
	if strings.Join(sel.gotCands, ",") != "bob" {
		t.Fatalf("耗尽者应退出候选：%v", sel.gotCands)
	}
	// 点名已耗尽成员：校验拒绝（预算不是摆设）。
	sel2 := &stubSelector{member: "alice"}
	d = eng.NextSpeaker(sp, clk.Now(), countsOf(2, 0), pol, sel2)
	if len(d.Next) != 0 || !strings.Contains(d.ManualNeeded, "预算已耗尽") {
		t.Fatalf("点名耗尽成员应拒绝：%+v", d)
	}
	// 0 = 不限：超计数不触发。
	d = eng.NextSpeaker(sp, clk.Now(), countsOf(99, 99), DiscussionPolicy{}, sel)
	wantNext(t, d, "bob")
}

// TestNextSpeakerAllExhaustedPause 全员发言预算耗尽 → 暂停建议（成员预算触发；
// 逐成员列明；不派发）。
func TestNextSpeakerAllExhaustedPause(t *testing.T) {
	eng := newTestEngine(t)
	pol := DiscussionPolicy{MaxSpeechesPerMember: 2}
	d := eng.NextSpeaker(fixtureSpace("sp-ds5", "", 0), newFakeClock().Now(), countsOf(2, 3), pol, nil)
	if d.Pause == nil || !d.Pause.ShouldPause || len(d.Next) != 0 {
		t.Fatalf("全员耗尽应建议暂停且不派发：%+v", d)
	}
	var kinds []StopKind
	for _, trig := range d.Pause.Triggers {
		kinds = append(kinds, trig.Kind)
		if !strings.Contains(trig.Detail, "发言预算耗尽") {
			t.Fatalf("触发细节须点名成员：%+v", trig)
		}
	}
	if len(kinds) != 2 {
		t.Fatalf("应逐成员两条触发（alice/bob）：%v", kinds)
	}
}

// TestNextSpeakerGlobalBudgetReuseEvaluateStop 全局预算耗尽复用 EvaluateStop：
// 消息预算触发原样投影（讨论域共用停止判定单一出口）。
func TestNextSpeakerGlobalBudgetReuseEvaluateStop(t *testing.T) {
	eng := newTestEngine(t)
	sp := fixtureSpace("sp-ds6", "", 0, func(v *collab.SpaceView) {
		v.Budget = collab.Budget{MaxMessages: 3}
		v.Used.Messages = 3
	})
	d := eng.NextSpeaker(sp, newFakeClock().Now(), countsOf(), DiscussionPolicy{}, &stubSelector{member: "alice"})
	if d.Pause == nil || len(d.Pause.Triggers) != 1 || d.Pause.Triggers[0].Kind != StopBudgetMessages {
		t.Fatalf("全局消息预算耗尽应经 EvaluateStop 触发：%+v", d)
	}
}

// TestNextSpeakerDoneByFinalize 候选定版收束：pending 存在不收束；全部定版 →
// Done 不再派发；定版后再登记新 pending → 重新推进（收束判定读事实不读历史）。
func TestNextSpeakerDoneByFinalize(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-ds7"
	space := fixtureSpace(sp, "", 0)
	sel := &stubSelector{member: "alice"}
	// 零候选：不收束，正常点名。
	if d := eng.NextSpeaker(space, newFakeClock().Now(), countsOf(), DiscussionPolicy{}, sel); d.Done || len(d.Next) != 1 {
		t.Fatalf("零候选应继续推进：%+v", d)
	}
	cand, _, err := eng.RegisterCandidate(sp, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	// pending 在场：不收束。
	if d := eng.NextSpeaker(space, newFakeClock().Now(), countsOf(), DiscussionPolicy{}, sel); d.Done {
		t.Fatalf("pending 在场不得收束：%+v", d)
	}
	// 主人定版：收束（定版 ≠ 交付——Reason 声明完成走判据）。
	if _, err := eng.FinalizeCandidate(sp, cand.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	d := eng.NextSpeaker(space, newFakeClock().Now(), countsOf(), DiscussionPolicy{}, sel)
	if !d.Done || len(d.Next) != 0 {
		t.Fatalf("候选定版应收束不派发：%+v", d)
	}
	if !strings.Contains(d.Reason, "定版≠交付") {
		t.Fatalf("收束说明须声明定版≠交付：%s", d.Reason)
	}
	// 新 pending 登记：收束解除（事实驱动）。
	if _, _, err := eng.RegisterCandidateVia(sp, ChannelFinalText, CandidateInput{Proposer: "bob", Body: "FINAL: 方案二", Round: 2}); err != nil {
		t.Fatal(err)
	}
	if d := eng.NextSpeaker(space, newFakeClock().Now(), countsOf(), DiscussionPolicy{}, sel); d.Done || len(d.Next) != 1 {
		t.Fatalf("新 pending 应回到推进：%+v", d)
	}
}

// TestNextSpeakerExplicitParallel 显式并发指名：ParallelSpeakers>1 一次点名前 N
// 名有预算成员（成员名单序）；不经选择器（nil 亦然）；超员取全数。
func TestNextSpeakerExplicitParallel(t *testing.T) {
	eng := newTestEngine(t)
	clk := newFakeClock()
	pol := DiscussionPolicy{ParallelSpeakers: 2}
	d := eng.NextSpeaker(fixtureSpace("sp-ds8", "", 0), clk.Now(), countsOf(), pol, nil)
	wantNext(t, d, "alice", "bob")
	// 预算过滤后并发：alice 耗尽只并发 bob。
	polBudget := DiscussionPolicy{MaxSpeechesPerMember: 1, ParallelSpeakers: 2}
	d = eng.NextSpeaker(fixtureSpace("sp-ds9", "", 0), clk.Now(), countsOf(5, 0), polBudget, nil)
	wantNext(t, d, "bob")
}

// TestPickSpeakersManualExit 主人手选出口：合法指名直出指令；非成员/耗尽/空单
// 拒绝；批量中一员非法整单拒绝（不静默换人补位）。
func TestPickSpeakersManualExit(t *testing.T) {
	eng := newTestEngine(t)
	sp := fixtureSpace("sp-dsa", "", 0)
	pol := DiscussionPolicy{MaxSpeechesPerMember: 1}
	d, err := eng.PickSpeakers(sp, countsOf(), pol, []string{"bob"})
	if err != nil {
		t.Fatal(err)
	}
	wantNext(t, d, "bob")
	d, err = eng.PickSpeakers(sp, countsOf(), pol, []string{"alice", "bob"})
	if err != nil || len(d.Next) != 2 {
		t.Fatalf("手选并发指名应成立：%+v err=%v", d, err)
	}
	if _, err := eng.PickSpeakers(sp, countsOf(), pol, []string{"ghost"}); !errors.Is(err, ErrSpeakerNotMember) {
		t.Fatalf("非成员应拒绝：%v", err)
	}
	if _, err := eng.PickSpeakers(sp, countsOf(1, 0), pol, []string{"alice"}); !errors.Is(err, ErrSpeakerBudgetExhausted) {
		t.Fatalf("耗尽成员手选应拒绝（提高上限属显式配置变更）：%v", err)
	}
	if _, err := eng.PickSpeakers(sp, countsOf(), pol, []string{"alice", "ghost"}); !errors.Is(err, ErrSpeakerNotMember) {
		t.Fatalf("批量含非法成员应整单拒绝：%v", err)
	}
	if _, err := eng.PickSpeakers(sp, countsOf(), pol, nil); !errors.Is(err, ErrSpeakerEmpty) {
		t.Fatalf("空单应拒绝：%v", err)
	}
}

// TestRegisterCandidateViaChannels 双通道：FINAL 文本通道走既有链路（去重/独立
// 索引不变）；结构化通道本版显式拒绝并指明归属（W05-04）；未知通道拒绝。
func TestRegisterCandidateViaChannels(t *testing.T) {
	eng := newTestEngine(t)
	const sp = "sp-dsb"
	_, created, err := eng.RegisterCandidateVia(sp, ChannelFinalText, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 1})
	if err != nil || !created {
		t.Fatalf("FINAL 通道应登记: created=%v err=%v", created, err)
	}
	// 同文重申去重（既有链路语义不变）。
	_, created, err = eng.RegisterCandidateVia(sp, ChannelFinalText, CandidateInput{Proposer: "alice", Body: "FINAL: 方案一", Round: 2})
	if err != nil || created {
		t.Fatalf("同文重申应去重: created=%v err=%v", created, err)
	}
	// 结构化通道：占位拒绝，错误指明归属。
	_, _, err = eng.RegisterCandidateVia(sp, ChannelStructured, CandidateInput{Proposer: "alice", Body: `{}`})
	if !errors.Is(err, ErrStructuredCandidateDeferred) {
		t.Fatalf("结构化通道应显式拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), "W05-04") {
		t.Fatalf("拒绝信息须指明 schema 归属：%v", err)
	}
	// 结构化正文不得经 FINAL 通道伪装登记（缺前缀拒绝——登记唯一依据是前缀）。
	if _, _, err := eng.RegisterCandidateVia(sp, ChannelFinalText, CandidateInput{Proposer: "alice", Body: `{"title":"x"}`}); !errors.Is(err, ErrNotFinalForm) {
		t.Fatalf("非 FINAL 形式应拒绝：%v", err)
	}
	if _, _, err := eng.RegisterCandidateVia(sp, "junk", CandidateInput{Proposer: "alice", Body: "FINAL: x"}); err == nil {
		t.Fatal("未知通道应拒绝")
	}
	// 候选独立索引：登记两次同文只一条（消息裁剪不丢候选——SourceMsgSeq 溯源）。
	snap, _ := eng.Snapshot(sp)
	if len(snap.Candidates) != 1 || snap.Candidates[0].SeenCount != 2 {
		t.Fatalf("候选索引/去重计数不符：%+v", snap.Candidates)
	}
}

// TestDiscussionFullChainWithCollabStore 全链（fake 选择器 + 真 collab Store）：
// 成员发言经消息账本落账（计数由账本投影）→ FINAL 发言登记候选（经 hooks 校验
// 形态）→ 逐轮推进至主人定版 → NextSpeaker 收束不再派发；预算耗尽路径经全局
// 预算触发暂停建议。
func TestDiscussionFullChainWithCollabStore(t *testing.T) {
	eng := newTestEngine(t)
	st, clk := newWiredStore(t, eng)
	id, ver := wiredRunningSpace(t, st)

	// 发言计数投影（装配层口径：从 collab 消息账本按成员归属计数）。
	speechCounts := func(t *testing.T) map[string]int64 {
		t.Helper()
		msgs, _ := st.SpaceMessages(id, 0)
		counts := map[string]int64{}
		for _, m := range msgs {
			if m.Member != "" {
				counts[m.Member]++
			}
		}
		return counts
	}
	// 成员发言落账（装配层：RecordMessage 零唤醒；发言主体归属成员）。
	say := func(t *testing.T, reqID, member, text string) {
		t.Helper()
		sp := mustSpace(t, st, id)
		if _, _, err := st.RecordMessage(id, collab.MessageRequest{
			RequestID: reqID, ExpectedVersion: sp.Version, Member: member, Text: text,
		}, intOwner); err != nil {
			t.Fatalf("发言落账: %v", err)
		}
	}

	pol := DiscussionPolicy{}
	space := func() collab.SpaceView {
		v, _ := st.SpaceView(id)
		return v
	}

	// 轮 1：点名 bob 普通发言（无候选）→ 计数由账本投影推进。
	d := eng.NextSpeaker(space(), clk.Now(), speechCounts(t), pol, &stubSelector{member: "bob"})
	wantNext(t, d, "bob")
	say(t, "m1", "bob", "我先说：问题在序列化边界")
	// 轮 2：点名 alice → alice FINAL 发言 → 候选登记（装配层先 hooks 校验成员
	// 资格与形态，再经双通道入口登记）。
	d = eng.NextSpeaker(space(), clk.Now(), speechCounts(t), pol, &stubSelector{member: "alice"})
	wantNext(t, d, "alice")
	say(t, "m2", "alice", "FINAL: 采用按层分离的证据字段")
	if err := eng.ValidateCandidate(space(), collabCandidate("alice", "FINAL: 采用按层分离的证据字段")); err != nil {
		t.Fatalf("FINAL 发言应过候选校验: %v", err)
	}
	if _, _, err := eng.RegisterCandidateVia(id, ChannelFinalText, CandidateInput{
		Proposer: "alice", Body: "FINAL: 采用按层分离的证据字段", Round: 2, MsgSeq: 2,
	}); err != nil {
		t.Fatalf("候选登记: %v", err)
	}
	// pending 在场：继续推进不收束。
	d = eng.NextSpeaker(space(), clk.Now(), speechCounts(t), pol, &stubSelector{member: "bob"})
	if d.Done {
		t.Fatalf("pending 在场不得收束：%+v", d)
	}
	// 主人定版 → 收束不再派发（定版 ≠ 交付）。
	snap, _ := eng.Snapshot(id)
	if _, err := eng.FinalizeCandidate(id, snap.Candidates[0].ID, "owner"); err != nil {
		t.Fatal(err)
	}
	d = eng.NextSpeaker(space(), clk.Now(), speechCounts(t), pol, &stubSelector{member: "bob"})
	if !d.Done || len(d.Next) != 0 {
		t.Fatalf("定版应收束：%+v", d)
	}
	_ = ver
}

// TestNextSpeakerFinalizedNotCarriedAcrossRestart 定版收束跨重启不延续（行为缺
// 口锚定，doc 注记同源）：候选定版 → Done 收束后换新 Engine 实例（进程重启，内
// 存循环态丢失；collab Store 与消息账本原样，FINAL 发言在案）——同一空间视图
// NextSpeaker 不再判 Done，回到推进态照常点名派发。依据：候选索引（含定版状态）
// 只存于引擎内存 SpaceLoop，Done 判定不从账本 FINAL 发言再推导（登记是写时动
// 作）；RestoreLoop 接线前如实如此（恢复路径：装配层持久化 Snapshot + 启动时
// RestoreLoop 导入）。
func TestNextSpeakerFinalizedNotCarriedAcrossRestart(t *testing.T) {
	eng := newTestEngine(t)
	st, clk := newWiredStore(t, eng)
	id, _ := wiredRunningSpace(t, st)
	sel := &stubSelector{member: "bob"}

	// 重启前：alice FINAL 发言落账 → 候选登记 → 主人定版 → Done 收束。
	sp := mustSpace(t, st, id)
	if _, _, err := st.RecordMessage(id, collab.MessageRequest{
		RequestID: "m-final", ExpectedVersion: sp.Version, Member: "alice",
		Text: "FINAL: 采用方案一",
	}, intOwner); err != nil {
		t.Fatalf("发言落账: %v", err)
	}
	cand, created, err := eng.RegisterCandidateVia(id, ChannelFinalText,
		CandidateInput{Proposer: "alice", Body: "FINAL: 采用方案一", Round: 1, MsgSeq: 1})
	if err != nil || !created {
		t.Fatalf("候选登记: created=%v err=%v", created, err)
	}
	if _, err := eng.FinalizeCandidate(id, cand.ID, "owner"); err != nil {
		t.Fatalf("FinalizeCandidate: %v", err)
	}
	v, _ := st.SpaceView(id)
	if d := eng.NextSpeaker(v, clk.Now(), countsOf(), DiscussionPolicy{}, sel); !d.Done || len(d.Next) != 0 {
		t.Fatalf("重启前定版应收束：%+v", d)
	}

	// 重启后：新 Engine 实例（内存循环态为空）+ 同一空间视图——账本里 FINAL 发
	// 言在案但不自动重建候选索引 → 不判 Done，继续点名派发。
	eng2 := newTestEngine(t)
	if _, ok := eng2.Snapshot(id); ok {
		t.Fatal("新 Engine 实例不得带旧循环事实（重启前提）")
	}
	d2 := eng2.NextSpeaker(v, clk.Now(), countsOf(), DiscussionPolicy{}, sel)
	if d2.Done || d2.Pause != nil || d2.ManualNeeded != "" || len(d2.Next) != 1 {
		t.Fatalf("RestoreLoop 接线前定版收束跨重启不延续——应回到推进态继续派发：%+v", d2)
	}
	wantNext(t, d2, "bob")
}
