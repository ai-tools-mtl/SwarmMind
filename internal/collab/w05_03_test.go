package collab

// w05_03_test.go W05-03 模型层增量机械测试：ActSetPhase 循环阶段落账（最小追加
// ——分离边界：转换合法性判定在 strategy.Engine，本包只存不解释 + 值基校验 +
// 运行面状态门槛 + 逐动作授权）。fake 时钟 + 既有夹具同源（collab_test.go）。

import (
	"errors"
	"strings"
	"testing"
)

// phaseAt 直读空间阶段值（含不存在断言）。
func phaseAt(t *testing.T, st *Store, id string) string {
	t.Helper()
	return mustSpace(t, st, id).Phase
}

// TestW0503SetPhaseLands 主人 manual 形态全链落账：引导（空 → drafting）→ 推进
// 落账 → 同值幂等零变更 → 审计事件在案 → 同 request_id 重放回执。
func TestW0503SetPhaseLands(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st)

	// 引导：prev 未初始化 → drafting（落账即事实；本包不解释合法性）。
	res := mustControl(t, st, id, ControlRequest{
		RequestID: "ph1", Action: ActSetPhase, ExpectedVersion: ver, Phase: "drafting",
	}, owner)
	if res.Space.Phase != "drafting" {
		t.Fatalf("阶段应落 drafting: %q", res.Space.Phase)
	}
	// 值基清洗：空白围边剔除后落账。
	mustControl(t, st, id, ControlRequest{
		RequestID: "ph2", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: " frozen ",
	}, owner)
	if got := phaseAt(t, st, id); got != "frozen" {
		t.Fatalf("空白围边应剔除后落 frozen: %q", got)
	}
	// 审计事件在案（kind=set_phase，note 含旧→新）。
	sp := mustSpace(t, st, id)
	found := false
	for _, ev := range sp.Events {
		if ev.Kind == "set_phase" && strings.Contains(ev.Note, "frozen") && strings.Contains(ev.Note, "drafting") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("set_phase 审计事件缺失（含 drafting → frozen）：%+v", sp.Events)
	}
	// 同值重复落账：幂等零变更（applied=false，版本不动）。
	res2, dup, err := st.Control(id, ControlRequest{
		RequestID: "ph3", Action: ActSetPhase, ExpectedVersion: sp.Version, Phase: "frozen",
	}, owner)
	if err != nil || dup || res2.Applied {
		t.Fatalf("同值落账应幂等零变更: applied=%v dup=%v err=%v", res2.Applied, dup, err)
	}
	if res2.Space.Version != sp.Version {
		t.Fatalf("零变更不得推版本: %d → %d", sp.Version, res2.Space.Version)
	}
	// 同 request_id 同文重放：journal 命中原回执（dup=true）。
	if _, dup, err := st.Control(id, ControlRequest{
		RequestID: "ph2", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: " frozen ",
	}, owner); err != nil || !dup {
		t.Fatalf("重放应命中回执: dup=%v err=%v", dup, err)
	}
	// ServedIdentical 只读重放判定（W05-04 终审必修2②，端点预检层专用）：
	// 同文命中；未受理/异文/跨空间/空 ID 一律 false（异文兜底由 apply 报冲突）。
	if !st.ServedIdentical(id, ControlRequest{
		RequestID: "ph2", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: " frozen ",
	}) {
		t.Fatal("同文已受理应命中 ServedIdentical")
	}
	for name, req := range map[string]ControlRequest{
		"未受理": {RequestID: "ph-absent", Action: ActSetPhase, ExpectedVersion: 1, Phase: "frozen"},
		"异文":  {RequestID: "ph2", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: "review"},
		"异动作": {RequestID: "ph2", Action: ActPause, ExpectedVersion: ver + 1},
	} {
		if st.ServedIdentical(id, req) {
			t.Fatalf("%s 不得命中 ServedIdentical: %+v", name, req)
		}
	}
	if st.ServedIdentical("sp-other", ControlRequest{
		RequestID: "ph2", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: " frozen ",
	}) {
		t.Fatal("跨空间不得命中（指纹含目标空间维度）")
	}
	if st.ServedIdentical(id, ControlRequest{Action: ActSetPhase}) {
		t.Fatal("空 request_id 不得命中")
	}
}

// TestW0503SetPhaseDriverForm 驱动者形态（external 领取后 steward 持任期）可落
// 阶段——与 resume/start 同授权族。
func TestW0503SetPhaseDriverForm(t *testing.T) {
	st, _, _ := newTestStore(t)
	members := []Member{{Name: "echo", SessionID: "sess-a"}}
	sp, term := runExternalRunning(t, st, steward, members)
	res := mustControl(t, st, sp.ID, ControlRequest{
		RequestID: "drv-ph1", Action: ActSetPhase, ExpectedVersion: sp.Version,
		Term: term, Phase: "drafting",
	}, steward)
	if res.Space.Phase != "drafting" {
		t.Fatalf("驱动者落阶段应成立: %q", res.Space.Phase)
	}
}

// TestW0503SetPhasePausedSpace 暂停态可落阶段（预算暂停的循环阶段面
// paused/PausedFrom 回写经同通道；暂停不换任期——驱动者形态仍在）。
func TestW0503SetPhasePausedSpace(t *testing.T) {
	st, _, _ := newTestStore(t)
	id, ver := mustRunning(t, st)
	mustControl(t, st, id, ControlRequest{RequestID: "pz", Action: ActPause, ExpectedVersion: ver}, owner)
	mustControl(t, st, id, ControlRequest{
		RequestID: "php", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: "paused",
	}, owner)
	if got := phaseAt(t, st, id); got != "paused" {
		t.Fatalf("暂停态落阶段应成立: %q", got)
	}
}

// TestW0503SetPhaseRejected 值基/授权/状态/版本拒绝面逐项（零副作用）。
func TestW0503SetPhaseRejected(t *testing.T) {
	t.Run("值基_空值_字符集_超长", func(t *testing.T) {
		st, _, _ := newTestStore(t)
		id, ver := mustRunning(t, st)
		for name, phase := range map[string]string{
			"空值":   "",
			"空白值":  "   ",
			"非法字符": "drafting;",
			"大写":   "Drafting",
			"内嵌空白": "draf ting",
			"超长":   strings.Repeat("a", 33),
		} {
			if _, _, err := st.Control(id, ControlRequest{
				RequestID: "bad-" + name, Action: ActSetPhase, ExpectedVersion: ver, Phase: phase,
			}, owner); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s 应 invalid_request，得 %v", name, err)
			}
		}
		if got := phaseAt(t, st, id); got != "" {
			t.Fatalf("拒绝面零副作用被破坏: %q", got)
		}
	})
	t.Run("授权_非驱动者编排主体拒绝", func(t *testing.T) {
		st, _, _ := newTestStore(t)
		id, ver := mustRunning(t, st)
		if _, _, err := st.Control(id, ControlRequest{
			RequestID: "denied", Action: ActSetPhase, ExpectedVersion: ver, Phase: "drafting",
		}, steward); !errors.Is(err, ErrForbidden) {
			t.Fatalf("非驱动者 orchestrate 应 forbidden，得 %v", err)
		}
	})
	t.Run("状态_待配置_待领取_终态拒绝", func(t *testing.T) {
		st, _, _ := newTestStore(t)
		pending, _, err := st.CreateSpace(CreateRequest{Goal: "g", Mode: ModeManual}, owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Control(pending.ID, ControlRequest{
			RequestID: "pc1", Action: ActSetPhase, ExpectedVersion: pending.Version, Phase: "drafting",
		}, owner); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("待配置态应 illegal_transition，得 %v", err)
		}
		leader, _, err := st.CreateSpace(CreateRequest{Goal: "g", Mode: ModeExternal}, steward)
		if err != nil {
			t.Fatal(err)
		}
		// 待领取空间无有效驱动者（任期 0）：授权层先行拒绝（forbidden）——状态
		// 门槛是纵深防御，授权先于状态判定是 Control 既有层序。
		if _, _, err := st.Control(leader.ID, ControlRequest{
			RequestID: "pl1", Action: ActSetPhase, ExpectedVersion: leader.Version, Phase: "drafting",
		}, steward); !errors.Is(err, ErrForbidden) {
			t.Fatalf("待领取态无驱动者应 forbidden（授权先行），得 %v", err)
		}
		// 终态（abandoned）时间线只读。
		id, ver := mustRunning(t, st)
		mustControl(t, st, id, ControlRequest{
			RequestID: "ab", Action: ActFinalize, ExpectedVersion: ver, FinalOutcome: "abandoned", Reason: "放弃",
		}, owner)
		if _, _, err := st.Control(id, ControlRequest{
			RequestID: "tm", Action: ActSetPhase, ExpectedVersion: ver + 1, Phase: "drafting",
		}, owner); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("终态应 illegal_transition，得 %v", err)
		}
	})
	t.Run("版本_CAS", func(t *testing.T) {
		st, _, _ := newTestStore(t)
		id, ver := mustRunning(t, st)
		if _, _, err := st.Control(id, ControlRequest{
			RequestID: "vc", Action: ActSetPhase, ExpectedVersion: ver + 99, Phase: "drafting",
		}, owner); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("版本不符应 version_conflict，得 %v", err)
		}
	})
}
