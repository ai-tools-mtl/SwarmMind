package service

// spacedispatch_w08_test.go AC16 机械锚（W08-02，docs/tasks/v3/W08-recovery.md
// 「恢复与清理具体规则」）：终态清理（CleanupAcked 清走 result_acked 任务条目）
// 与消息窗口滚动**不带走活跃空间材料**——重启后目标/讨论消息/成员 session 绑定/
// 版本/意图注记（执行事实）/归属登记全部在，空间照常续推（新派发收敛）；
// 协作快照损坏明确拒启（不加载空表当「新实例」）——拒启半边由
// internal/collab TestW07SnapshotCorruptRefusesStart 既有锚承担，本文件锚
// 「清理后重启仍能续」正面。
//
// 关键口径（W08 任务书原文）：「活跃空间目标/决定/问题/成员session绑定/版本/
// 必要证据单独保留，不能被旧 Group 200条/24h 消息窗口或 Task 清理带走」——空间
// 账本是独立存储（spaces.json），Group 消息窗口（groups.json 独立信箱）与 Task
// 保留清理（ledger.json result_acked）在存储层就不触达空间材料；本测试用真实
// 清理 + 真实重启证明。

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// TestW08AC16CleanupRestartSpaceMaterialsSurvive 终态清理 → 重启 → 空间材料
// 完整 + 可续推（AC16 同机可采部分）。
func TestW08AC16CleanupRestartSpaceMaterialsSurvive(t *testing.T) {
	f := newSpaceSvcFixture(t, spaceSvcOpt{})
	sid := f.createSession(f.cwd)
	sp := f.newManualSpace(sid)

	// 活跃空间材料：讨论消息（决定载体）+ 一轮派发收敛（执行事实与注记）。
	if _, _, err := f.st.RecordMessage(sp.ID, collab.MessageRequest{
		RequestID: "ac16-msg-1", Text: "决定：先做恢复锚，再做诊断投影", ExpectedVersion: sp.Version,
	}, spOwner); err != nil {
		t.Fatalf("主人发言: %v", err)
	}
	if _, _, err := f.dispatch(sp.ID, "ac16-d1", "第一棒（执行事实）"); err != nil {
		t.Fatalf("派发: %v", err)
	}
	f.waitIntent(sp.ID, "ac16-d1", collab.IntentConverged, 8*time.Second)
	gen1 := f.mustSpace(sp.ID)
	gen1Events := len(gen1.Events)
	gen1Msgs, _ := f.st.SpaceMessages(sp.ID, 0)
	if gen1Events == 0 || len(gen1Msgs) == 0 {
		t.Fatalf("夹具前提：事件与消息应在案 events=%d msgs=%d", gen1Events, len(gen1Msgs))
	}
	// 归属登记在案（space_dispatch_required 守卫的判定源）。
	if owner, ok := f.st.SessionOwnerSpace(sid); !ok || owner != sp.ID {
		t.Fatalf("夹具前提：会话归属登记应指向本空间 owner=%q ok=%v", owner, ok)
	}

	// 终态清理：result_acked 条目超保留期（local 来源落终态即自确认）——
	// CleanupAcked（真实清理入口，1 分钟周期接线于 service.cleanupLoop；此处按
	// 同一函数前移时钟直驱）清走全部任务条目（AC16「清 Task」）。
	n, err := f.led.CleanupAcked(protocol.NowUTC().Add(48*time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("终态清理: %v", err)
	}
	if n == 0 {
		t.Fatal("夹具前提：清理应清走已确认终态条目")
	}
	if left := len(f.led.View()); left != 0 {
		t.Fatalf("夹具前提：清理后账本应空（Task 已清），实余 %d", left)
	}

	// ---- 重启：全链 Close + 重开（同 dataDir；与重启对账测试同款装配）。----
	f.svc.Close()
	_ = f.st.Close()
	_ = f.led.Close()
	dir := f.dir
	led2, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led2.Close() })
	mgr2, err := sessionx.NewManager(filepath.Join(dir, "kernel"), map[string]sessionx.AgentSpec{
		"coder": {Adapter: protocol.SessionAdapterCodex, Command: f.fake,
			PermissionMode: "workspace-write", TimeoutSec: 30,
			Env: []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
				"FAKE_THREAD_ID=thr-sp-1", "FAKE_LOG=" + f.runLog}},
	}, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops2, err := sessionop.New(filepath.Join(dir, "ops"), led2, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := collab.Open(filepath.Join(dir, "collab"), collab.Options{})
	if err != nil {
		t.Fatalf("重开协作快照（损坏/版本未知必须在此明确失败）: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	bus2 := transport.NewBus()
	svc2 := New(Config{
		Ledger: led2, Runners: map[string]Runner{}, ArtifactRoots: map[string]string{"coder": filepath.Join(dir, "artifacts")},
		Sessions: mgr2, SessionOps: ops2, Collab: st2, NodeID: "n1",
	}, bus2.Endpoint("n1"))
	t.Cleanup(func() { svc2.Close() })

	// 材料完整性：目标/讨论消息/成员绑定/版本/事件/意图注记（执行事实）全在。
	sp2, ok := st2.Space(sp.ID)
	if !ok {
		t.Fatal("重启后空间应原样在（不能当新实例空表）")
	}
	if sp2.Goal != gen1.Goal || sp2.Version < gen1.Version {
		t.Fatalf("目标/版本应保留: goal=%q version=%d (<%d)", sp2.Goal, sp2.Version, gen1.Version)
	}
	if len(sp2.Members) != 1 || sp2.Members[0].SessionID != sid {
		t.Fatalf("成员 session 绑定应保留: %+v", sp2.Members)
	}
	sp2Msgs, _ := st2.SpaceMessages(sp.ID, 0)
	if len(sp2Msgs) != len(gen1Msgs) || len(sp2Msgs) == 0 {
		t.Fatalf("讨论消息应保留: %d != %d", len(sp2Msgs), len(gen1Msgs))
	}
	if len(sp2.Events) < gen1Events {
		t.Fatalf("审计事件应保留: %d < %d", len(sp2.Events), gen1Events)
	}
	if it, ok := st2.IntentByRequest(sp.ID, "ac16-d1"); !ok || it.Status != collab.IntentConverged || it.Note == "" {
		t.Fatalf("已收敛意图与注记应保留（执行事实不随 Task 清理失联）: %+v ok=%v", it, ok)
	}
	if owner, ok := st2.SessionOwnerSpace(sid); !ok || owner != sp.ID {
		t.Fatalf("会话归属登记应保留: owner=%q ok=%v", owner, ok)
	}

	// 可续推：重启后新派发照常收敛（Task 清理不影响续空间；轮号自内核高水位续）。
	f2 := &spaceSvcFixture{t: t, svc: svc2, ep: bus2.Endpoint("hub"), mgr: mgr2, led: led2, ops: ops2, st: st2, dir: dir, cwd: f.cwd, cwd2: f.cwd2, fake: f.fake, runLog: f.runLog}
	if _, _, err := f2.dispatch(sp.ID, "ac16-d2", "重启后续棒"); err != nil {
		t.Fatalf("重启后派发: %v", err)
	}
	f2.waitIntent(sp.ID, "ac16-d2", collab.IntentConverged, 8*time.Second)
}
