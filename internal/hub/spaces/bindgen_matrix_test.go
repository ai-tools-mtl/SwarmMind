// bindgen_matrix_test.go 绑定族细粒度并发交错矩阵（W09 收尾加固工作块 C，
// docs/tasks/v3/W09-hardening.md 工作块 C / SPEC.md 声明 27 降范围面收窄三场景）：
//
//   - 场景 1 解绑×重绑交错：旧代次的借用转移/补偿不得触碰新代次绑定
//     （compensateBindIfOwned 四门 service.go compensateBindIfOwned +
//     node RemoveIfGen 代次精确匹配 hubbind.go RemoveIfGen——矩阵验证）；
//   - 场景 2 自动补偿并发换绑：unbindGenOnce 探针钉代次（学到的 CurrentGen =
//     确定性目标，不追逐）+ 并发 ensureBindSession stale 重同步窗
//     （gen1 stale 拒 → resync → 新代越墓碑/越 live 登记两序）；
//   - 场景 3 迟到回写完备隔离：旧代次会话的迟到回写（bind ack 回写/
//     解除 ack 墓碑回写/结果与取消 writeback 收敛）在新代次绑定下零触碰
//     （noteBound 高水位判据 + 墓碑回写身份判据 + 收敛锚 session+seq）。
//
// 方法纪律（任务书红线）：交错点全部用确定性协调面钉序——
//
//	① bindFrameGate：membus 同步分发的 handler wrapper（Subscribe 单 handler
//	  语义，wrapper 末步原样调原 handler，行为零变化），匹配帧携调用 goroutine
//	  停泊于门直至放行——node 入站门钉「解除帧 vs 换绑帧」的 node 登记序，
//	  hub 入站门钉「ack 抵达 vs hub 侧回写」的消费序；
//	② 空间调度锁屏障（lockSpace——TestD2/TestCloseDrains 既有口径）；
//	③ onConvDequeued 出队观测缝（worker 持有收敛项、停泊锁外的确定性信号）；
//	④ 条件等待（testWaitBudget 预算，非固定 sleep）。零 sleep 碰运气。
//	矩阵 13 cell（3+4+6）：W07-02 方案 A 结构（bindGenEntry/代次条件解除/墓碑
//	单调）覆盖原有 11 cell 实测未破；S3.1b/S3.2b 为 codex R2 实测出的真窗闭环——
//	生产修复一处（noteBound 咨询 genSeq 分配高水位，补偿删缓存后低代迟到回写
//	不复活——分裂面闭合），墓碑低水位推进分支补确定性 cell。
package spaces

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// matrixOpts 矩阵夹具：capable node + 关兜底扫查（retryUnbinds 与测试编排的
// 竞速收口会破坏确定性——矩阵不依赖扫查节拍，收敛/解除全由门放行驱动）。
func newMatrixFixture(t *testing.T, barrier bool) *chainFixture {
	t.Helper()
	return newChainFixtureO(t, chainOpts{barrier: barrier, capable: true, spacesSweep: time.Hour})
}

// ---------- 确定性协调面：帧门 ----------

// bindFrameGate space_bind/ack 帧级确定性屏障：membus Send 在调用方 goroutine 内
// 同步执行对端 handler——wrapper 内先过门再调原 handler，匹配帧（按 SpaceBindPayload
// 谓词）停泊至 release，非匹配帧直通。停泊即冻结整条往返链（SpaceBind/SpaceUnbind
// 调用方 goroutine 停在门内），据此钉住「两操作的 node 登记序」与「ack 抵达 vs
// hub 侧缓存回写序」——零 sleep 的确定性交错。
type bindFrameGate struct {
	pred     func(p protocol.SpaceBindPayload) bool
	arrived  chan protocol.SpaceBindPayload
	proceed  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	released bool
}

func newBindFrameGate(pred func(p protocol.SpaceBindPayload) bool) *bindFrameGate {
	return &bindFrameGate{pred: pred, arrived: make(chan protocol.SpaceBindPayload, 1), proceed: make(chan struct{})}
}

// installNode 拦截 node 入站帧（space_bind/space_unbind 请求——钉 node 登记序）。
func (g *bindFrameGate) installNode(f *chainFixture) {
	f.bus.Endpoint("n1").Subscribe(func(env *protocol.Envelope) {
		g.intercept(env, protocol.MsgSpaceBind)
		f.nodeSvc.HandleFrame(env)
	})
}

// installHub 拦截 hub 入站帧（space_bind_ack——钉 ack 消费 vs hub 缓存回写序）。
func (g *bindFrameGate) installHub(f *chainFixture) {
	f.bus.Endpoint("hub").Subscribe(func(env *protocol.Envelope) {
		g.intercept(env, protocol.MsgSpaceBindAck)
		f.hubCore.HandleFrame(env)
	})
}

// intercept 门体：匹配帧发 arrived 信号后停泊（<proceed）；其余直通。
func (g *bindFrameGate) intercept(env *protocol.Envelope, typ string) {
	if env == nil || env.Type != typ {
		return
	}
	var p protocol.SpaceBindPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if !g.pred(p) {
		return
	}
	g.once.Do(func() {
		select {
		case g.arrived <- p:
		default:
		}
		<-g.proceed
	})
}

// waitArrived 有界等匹配帧抵达（预算内未抵达 = 链路未按预期推进，Fatal）。
func (g *bindFrameGate) waitArrived(t *testing.T) protocol.SpaceBindPayload {
	t.Helper()
	select {
	case p := <-g.arrived:
		return p
	case <-time.After(testWaitBudget):
		t.Fatal("帧门未在预算内捕获匹配帧（链路未按预期推进）")
		return protocol.SpaceBindPayload{}
	}
}

// release 放行停泊帧（幂等；t.Cleanup 兜底——Fatal 路径不得悬挂停泊 goroutine）。
func (g *bindFrameGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.released {
		g.released = true
		close(g.proceed)
	}
}

// ---------- 断言/同步辅助 ----------

// bindCacheOf 读 hub 代次缓存条目（muBindGen 同锁读——包内测试观察面）。
func (f *chainFixture) bindCacheOf(sid string) (bindGenEntry, bool) {
	f.svc.muBindGen.Lock()
	defer f.svc.muBindGen.Unlock()
	e, ok := f.svc.bindGens[sid]
	return e, ok
}

// unbindEntryOf 读解除重试队列条目（muUnbind 同锁读）。
func (f *chainFixture) unbindEntryOf(sid string) (hubUnbind, bool) {
	f.svc.muUnbind.Lock()
	defer f.svc.muUnbind.Unlock()
	u, ok := f.svc.pendingUnbinds[sid]
	return u, ok
}

// plantUnbindEntry 直植队列条目（TestR3ConditionalClears 同款包内直操纵口径）。
func (f *chainFixture) plantUnbindEntry(nodeID, sid string, gen uint64) {
	f.svc.muUnbind.Lock()
	f.svc.pendingUnbinds[sid] = hubUnbind{nodeID: nodeID, sessionID: sid, gen: gen}
	f.svc.muUnbind.Unlock()
}

// waitNodeBindingScope 条件等待 node 登记面到达目标 scope（"" = 无有效绑定）。
func (f *chainFixture) waitNodeBindingScope(sid, want string) {
	t := f.t
	deadline := time.Now().Add(testWaitBudget)
	for time.Now().Before(deadline) {
		if f.binds.Space(sid) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node 登记面未达目标: session=%s want=%q got=%q", sid, want, f.binds.Space(sid))
}

// waitSignal 有界等信号（goroutine 完成/出队缝——超时 Fatal 非碰运气）。
func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testWaitBudget):
		t.Fatalf("等待信号超时: %s（检查门/屏障放行链）", what)
	}
}

// requireNoPendingUnbind 解除重试队列必须空（补偿/解除不残留重试条目）。
func (f *chainFixture) requireNoPendingUnbind() {
	if left := f.svc.PendingUnbindSessions(); len(left) != 0 {
		f.t.Fatalf("解除重试队列应空: %v", left)
	}
}

// ---------- 场景 1：解绑×重绑交错 ----------

// TestBindMatrixS1UnbindRebind 场景 1 矩阵：unbind 与 rebind 并发交错下，旧代次
// 的借用转移/补偿不得触碰新代次绑定。
func TestBindMatrixS1UnbindRebind(t *testing.T) {
	t.Run("S1.1 unbind先完成_rebind越墓碑复活_旧代迟到解除不碰新代", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		// 全序前半：bind gen1 → 解除成立（node 墓碑1 + hub 缓存墓碑{1,revoked}）。
		g1, owned1, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned1 || g1 != 1 {
			t.Fatalf("bind gen1: gen=%d owned=%v err=%v", g1, owned1, err)
		}
		f.svc.UnbindSessionGen("n1", f.sessID, g1)
		if got := f.binds.Space(f.sessID); got != "" {
			t.Fatalf("解除后 node 应无有效绑定: %q", got)
		}
		if got := f.binds.CurrentGen(f.sessID); got != 1 {
			t.Fatalf("node 墓碑高水位应保留 1: %d", got)
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 1 {
			t.Fatalf("hub 缓存应为墓碑{1,revoked}: %+v ok=%v", e, ok)
		}
		// rebind：缓存墓碑 → 新代 gen2 越墓碑高水位复活（node PutIfGenNewer 2>1）。
		g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || g2 != 2 {
			t.Fatalf("rebind gen2: gen=%d owned=%v err=%v", g2, owned2, err)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("rebind 后 node 登记: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		// 交错点：旧代解除迟到重试（gen1）→ node 代次不符 ack-noop + hub
		// clearBoundIfGen(gen1) 对 {2,live} 条件不中——新代绑定零触碰。
		f.svc.UnbindSessionGen("n1", f.sessID, g1)
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("旧代迟到解除不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 2 {
			t.Fatalf("hub 缓存应保持 {2,live}: %+v ok=%v", e, ok)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S1.2 真Dispatch补偿_换绑先落node_旧代解除帧后到ack-noop", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		// node 门：钉「A 的补偿解除帧（gen1）」——换绑帧在其停泊期间先行登记。
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 1 && p.SessionID == f.sessID
		})
		gate.installNode(f)
		t.Cleanup(gate.release)
		// 空间锁屏障：A 的 Dispatch 完成绑定后停泊在登记段锁外（TestD2 口径）。
		m := f.svc.lockSpace(f.spaceID)
		m.Lock()
		dispErr := make(chan error, 1)
		go func() {
			view, _ := f.svc.SpaceView(f.spaceID)
			_, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
				RequestID: "s1-a1", ExpectedVersion: view.Version, Note: "A 的登记"}, fakeHubSubject)
			dispErr <- err
		}()
		f.waitNodeBindingScope(f.sessID, Scope(f.spaceID)) // A 的 bind gen1 已登记 node
		// 窗内 pause（直调模型层——绕调度锁）：A 的 RecordDispatch 将被状态拒绝。
		view, _ := f.svc.SpaceView(f.spaceID)
		if _, _, err := f.svc.Control(f.spaceID, collab.ControlRequest{RequestID: "s1-pause",
			Action: collab.ActPause, ExpectedVersion: view.Version}, fakeHubSubject); err != nil {
			t.Fatalf("窗内 pause: %v", err)
		}
		m.Unlock()
		// A：登记失败 → compensateBindIfOwned(gen1) 四门通过 → 原子 claim 删缓存 →
		// 解除帧停泊 node 门。
		gate.waitArrived(t)
		if _, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatal("前置：补偿 claim 应已删除缓存条目")
		}
		// 并发换绑（真交错窗）：缓存已被 claim 清空 → 新代 gen2；node: 2 > live gen1
		// → PutIfGenNewer 接受——换绑先于旧代解除帧落 node。
		g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || g2 != 2 {
			t.Fatalf("并发换绑 gen2: gen=%d owned=%v err=%v", g2, owned2, err)
		}
		// 放行旧代解除帧：node RemoveIfGen(1) vs 现登记 gen2 → ack-noop——
		// 结构性保证：旧代补偿不触碰新代绑定（比代次不比 scope）。
		gate.release()
		if err := <-dispErr; err == nil {
			t.Fatal("A 的登记应被窗内 pause 状态拒绝")
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("旧代补偿不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 2 {
			t.Fatalf("hub 缓存应保持 {2,live}: %+v ok=%v", e, ok)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S1.3 补偿门四门逐门矩阵", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		// 门② borrowed：借用转移后创建者补偿被拒（所有权随成功借用转移——
		// compensateBindIfOwned 单锁判据 entry.borrowed）。
		g1, owned1, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned1 {
			t.Fatalf("bind gen1: %v", err)
		}
		if _, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID)); err != nil || owned2 {
			t.Fatalf("借用应 owned=false: %v", err)
		}
		f.svc.compensateBindIfOwned("n1", f.sessID, g1)
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) {
			t.Fatalf("borrowed 门：借用后补偿应跳过（绑定保留）: %q", got)
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.borrowed || e.revoked {
			t.Fatalf("缓存应 {1,borrowed,live}: %+v ok=%v", e, ok)
		}
		// 门③ revoked：已解除墓碑条目上补偿被拒（不重发解除不降高水位）。
		f.svc.UnbindSessionGen("n1", f.sessID, g1)
		f.svc.compensateBindIfOwned("n1", f.sessID, g1)
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 1 {
			t.Fatalf("revoked 门：墓碑应保留 {1,revoked}: %+v ok=%v", e, ok)
		}
		if got := f.binds.CurrentGen(f.sessID); got != 1 {
			t.Fatalf("node 墓碑高水位应保留: %d", got)
		}
		f.requireNoPendingUnbind()
		// 门① gen 匹配：缓存已被新代推进（{2,live}），旧代补偿按 gen 不符跳过。
		g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || g2 != 2 {
			t.Fatalf("rebind gen2: %v", err)
		}
		f.svc.compensateBindIfOwned("n1", f.sessID, 1)
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("gen 匹配门：旧代补偿不得触碰新代绑定: %q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		// hub 缓存面断言（W09-C codex R2 必修①：变异反例——摘 entry.gen != gen 判据
		// 会经 :1513 误删 gen2 缓存，node 断言照绿）：gen 门拦截后缓存必须保持
		// {2,live}（删除即缓路面缺口——后续 ensureBound 会走补绑定链）。
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.gen != 2 || e.revoked || e.borrowed {
			t.Fatalf("gen 匹配门：缓存应保持 {2,live}: %+v ok=%v", e, ok)
		}
		// 门⓪ 空条目：无缓存条目（未绑定过的 session）→ 跳过零副作用。
		f.svc.compensateBindIfOwned("n1", "s-none", 7)
		if _, ok := f.bindCacheOf("s-none"); ok {
			t.Fatal("空条目门：不得为未知 session 建条目")
		}
		f.requireNoPendingUnbind()
	})
}

// ---------- 场景 2：自动补偿并发换绑（探针钉代次 + stale 重同步窗） ----------

// TestBindMatrixS2ProbeVsRebind 场景 2 矩阵：unbindGenOnce 探针学得代次与并发
// ensureBindSession stale 重同步的窗口两序全覆盖。
func TestBindMatrixS2ProbeVsRebind(t *testing.T) {
	t.Run("S2.1a 探针学旧代_换绑stale重同步先落node_学得代次解除后到ack-noop", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		// node 既有登记事实 gen=5（旧进程遗留形态；hub 进程内 genSeq 不知情）。
		if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID),
			SessionID: f.sessID, Generation: 5}, 2*time.Second); err != nil || !ack.Accepted {
			t.Fatalf("前置 bind gen5: %v ack=%+v", err, ack)
		}
		// node 门：钉「学得代次解除帧（gen5）」；探针帧（gen0）直通。
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 5 && p.SessionID == f.sessID
		})
		gate.installNode(f)
		t.Cleanup(gate.release)
		ugen := uint64(0) // 发起代次未知（gen=0 → 探针重建）
		done := make(chan struct{}, 1)
		go func() {
			f.svc.unbindGenOnce("n1", f.sessID, &ugen)
			close(done)
		}()
		gate.waitArrived(t) // 探针已学得 5、学得代次解除帧已停泊 node 门
		// 并发换绑（真交错窗）：genSeq 0 → alloc gen1 → node stale 拒（1≤5）→
		// resyncBindGen(5) → alloc gen6 → 接受——stale 重同步窗被实踏。
		gen, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned || gen != 6 {
			t.Fatalf("换绑应 stale 重同步后 gen6 owned: gen=%d owned=%v err=%v", gen, owned, err)
		}
		if got := f.binds.CurrentGen(f.sessID); got != 6 {
			t.Fatalf("node 应登记 gen6: %d", got)
		}
		// 放行学得代次解除帧：RemoveIfGen(5) vs 现登记 6 → ack-noop——探针学到的
		// 代次=确定性目标（不追逐当前值 6），新代绑定零触碰。
		gate.release()
		waitSignal(t, done, "unbindGenOnce 完成")
		if ugen != 5 {
			t.Fatalf("探针应钉住学得代次 5（不追逐）: %d", ugen)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 6 {
			t.Fatalf("学得代次解除不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 6 {
			t.Fatalf("hub 缓存应 {6,live}: %+v ok=%v", e, ok)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S2.1b 解除先成立_换绑stale重同步越墓碑自愈", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID),
			SessionID: f.sessID, Generation: 5}, 2*time.Second); err != nil || !ack.Accepted {
			t.Fatalf("前置 bind gen5: %v ack=%+v", err, ack)
		}
		// 交错序一：解除先成立——探针学 5 → 携 5 解除成立（node 墓碑5 + hub 墓碑）。
		f.svc.UnbindSessionGen("n1", f.sessID, 0)
		if got := f.binds.Space(f.sessID); got != "" || f.binds.CurrentGen(f.sessID) != 5 {
			t.Fatalf("解除应成立且高水位保留: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 5 {
			t.Fatalf("hub 缓存应墓碑{5,revoked}: %+v ok=%v", e, ok)
		}
		// 交错序二：换绑 stale 重同步越墓碑自愈（gen1 拒 → resync5 → gen6 越墓碑）。
		gen, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned || gen != 6 {
			t.Fatalf("换绑应越墓碑 gen6 owned: gen=%d owned=%v err=%v", gen, owned, err)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 6 {
			t.Fatalf("换绑后 node 登记: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S2.2 队列学得代次回写身份收口_收口窗内换绑新代完好", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID),
			SessionID: f.sessID, Generation: 5}, 2*time.Second); err != nil || !ack.Accepted {
			t.Fatalf("前置 bind gen5: %v ack=%+v", err, ack)
		}
		f.plantUnbindEntry("n1", f.sessID, 0) // 队列条目 gen=0（解除时代次未知）
		// hub 门：钉「学得代次解除的 ack（gen5）」——node 已删、hub 回写未及的窗。
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 5 && p.SessionID == f.sessID
		})
		gate.installHub(f)
		t.Cleanup(gate.release)
		done := make(chan struct{}, 1)
		go func() {
			f.svc.retryUnbinds()
			close(done)
		}()
		gate.waitArrived(t) // 探针学 5 → node RemoveIfGen(5) 已成立 → ack 停泊
		// 收口窗内换绑：node 现为墓碑5 → gen1 stale 拒 → resync5 → gen6 越墓碑接受。
		gen, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned || gen != 6 {
			t.Fatalf("窗内换绑 gen6: gen=%d owned=%v err=%v", gen, owned, err)
		}
		// 放行 ack：unbindGenOnce 语义收口（Accepted 墓碑回写遇 {6,live} 被身份判据
		// 跳过）→ retryUnbinds 学得代次回写条目（0→5）→ deletePendingIfGen(5) 收口。
		gate.release()
		waitSignal(t, done, "retryUnbinds 完成")
		if _, ok := f.unbindEntryOf(f.sessID); ok {
			t.Fatal("学得代次回写后条目应收口出队")
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 6 {
			t.Fatalf("收口不得触碰窗内新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 6 {
			t.Fatalf("hub 缓存应 {6,live}: %+v ok=%v", e, ok)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S2.3 队列条目被并发新解除替换_学得回写不覆盖_旧代收口不误删", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		if ack, err := f.hubCore.SpaceBind("n1", protocol.SpaceBindPayload{Space: Scope(f.spaceID),
			SessionID: f.sessID, Generation: 5}, 2*time.Second); err != nil || !ack.Accepted {
			t.Fatalf("前置 bind gen5: %v ack=%+v", err, ack)
		}
		f.plantUnbindEntry("n1", f.sessID, 0)
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 5 && p.SessionID == f.sessID
		})
		gate.installHub(f)
		t.Cleanup(gate.release)
		done := make(chan struct{}, 1)
		go func() {
			f.svc.retryUnbinds()
			close(done)
		}()
		gate.waitArrived(t) // 探针学 5 → node 已删（墓碑5）→ ack 停泊
		// 窗内并发：同 session 新解除条目替换（UnbindSessionGen 对同 session 条目
		// 无条件覆写的生产形态——并发两次 configure_members 移除同会话）。
		f.plantUnbindEntry("n1", f.sessID, 9)
		gate.release()
		waitSignal(t, done, "retryUnbinds 完成")
		// 回写身份校验：cur.gen=9 ≠ u.gen=0 → 学得代次（5）不得回写覆盖新条目；
		// deletePendingIfGen(5) 对 {9} 条件不中 → 新条目不得被旧代收口误删。
		if u, ok := f.unbindEntryOf(f.sessID); !ok || u.gen != 9 {
			t.Fatalf("新条目应存活且不被覆盖: %+v ok=%v", u, ok)
		}
		// 首轮解除的 hub 侧墓碑回写成立（缓存空条目 → !ok 分支写 {5,revoked}）。
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 5 {
			t.Fatalf("hub 缓存应墓碑{5,revoked}: %+v ok=%v", e, ok)
		}
		// 下一轮收口：新条目 gen9 → node 墓碑5 ack-noop 语义满足 → 出队；墓碑高水位
		// 不被降级（clearBoundIfGen(9) 对墓碑 {5,revoked} 条件不中）。
		f.svc.retryUnbinds()
		f.requireNoPendingUnbind()
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 5 {
			t.Fatalf("墓碑高水位应单调保留 {5,revoked}: %+v ok=%v", e, ok)
		}
		if got := f.binds.CurrentGen(f.sessID); got != 5 {
			t.Fatalf("node 墓碑高水位应保留 5: %d", got)
		}
	})
}

// ---------- 场景 3：迟到回写完备隔离 ----------

// TestBindMatrixS3LateWritebackIsolation 场景 3 矩阵：旧代次会话的迟到回写
// （bind ack 回写/解除 ack 墓碑回写/结果与取消 writeback）在新代次绑定下零触碰。
func TestBindMatrixS3LateWritebackIsolation(t *testing.T) {
	t.Run("S3.1 旧代bind_ack迟到_noteBound高水位判据_旧代解除权被gen门约束", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		// hub 门：钉「A 的 bind gen1 ack」——A 停泊在 SpaceBind 内、noteBound 未及。
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == Scope(f.spaceID) && p.Generation == 1 && p.SessionID == f.sessID
		})
		gate.installHub(f)
		t.Cleanup(gate.release)
		type bindRes struct {
			gen   uint64
			owned bool
			err   error
		}
		resA := make(chan bindRes, 1)
		go func() {
			g, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
			resA <- bindRes{g, owned, err}
		}()
		gate.waitArrived(t) // A 的 gen1 bind 已登记 node、ack 停泊（缓存尚空）
		// 并发换绑：缓存空（A 未回写）→ 新代 gen2；node: 2 > live gen1 → 接受。
		genB, ownedB, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !ownedB || genB != 2 {
			t.Fatalf("换绑 gen2: gen=%d owned=%v err=%v", genB, ownedB, err)
		}
		// 放行迟到 ack：noteBound(1) 双层高水位判据（条目 cur.gen=2 > 1 为第一层；
		// genSeq 分配高水位=2 为第二层——W09-C R3 基线注记：M1「单摘条目门」的
		// 变异咬合在 genSeq 门引入前的 41a4b70 基线成立，当前提交单摘条目门时
		// S3.1 仍被 genSeq 门拦下，须两层同摘方破）跳过——旧代回写不覆盖新登记。
		gate.release()
		ra := <-resA
		if ra.err != nil || ra.gen != 1 || !ra.owned {
			t.Fatalf("A 应返回 (1,true,nil): %+v", ra)
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 2 {
			t.Fatalf("迟到回写不得覆盖新登记: %+v ok=%v", e, ok)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("node 应保持 gen2: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		// A 名义上持有 gen1 解除权（owned=true）——补偿门 gen 匹配判据约束之：
		// 对 gen1 的补偿在 {2,live} 条目上跳过，新代绑定零触碰。
		f.svc.compensateBindIfOwned("n1", f.sessID, 1)
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("旧代解除权不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S3.2 旧代解除ack迟到_墓碑回写遇live新代条目被身份判据跳过", func(t *testing.T) {
		f := newMatrixFixture(t, false)
		g1, owned1, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned1 {
			t.Fatalf("bind gen1: %v", err)
		}
		// hub 门：钉「补偿解除的 ack（gen1）」——node 已删（墓碑1）、hub 墓碑回写未及。
		gate := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 1 && p.SessionID == f.sessID
		})
		gate.installHub(f)
		t.Cleanup(gate.release)
		done := make(chan struct{}, 1)
		go func() {
			f.svc.compensateBindIfOwned("n1", f.sessID, g1)
			close(done)
		}()
		gate.waitArrived(t) // 四门通过 → claim 删缓存 → node RemoveIfGen(1) 成立 → ack 停泊
		if _, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatal("前置：补偿 claim 应已删缓存")
		}
		// 窗内换绑：缓存空 → gen2；node: 2 > 墓碑1 → PutIfGenNewer 接受。
		gen2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || gen2 != 2 {
			t.Fatalf("窗内换绑 gen2: gen=%d owned=%v err=%v", gen2, owned2, err)
		}
		// 放行迟到 ack：墓碑回写判据（!ok || e.gen==1 || (e.revoked && <1)）对
		// {2,live} 全不中 → 跳过——旧代解除回写不覆盖新代登记。
		gate.release()
		waitSignal(t, done, "compensateBindIfOwned 完成")
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 2 {
			t.Fatalf("墓碑回写不得覆盖新代登记: %+v ok=%v", e, ok)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 2 {
			t.Fatalf("新代绑定应完好: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S3.1b 补偿删缓存后低代迟到回写不复活_genSeq高水位咨询_新派发新代自愈", func(t *testing.T) {
		// W09-C codex R2 必修②反例闭环：A/B/C 三派发交错（版本预检→暂停→CAS 失败
		// 补偿→恢复）可达的真实生产窗——补偿 delete-claim 删缓存后，迟到的低代
		// bind ack（缓存空）无 genSeq 咨询时会把 {1,live} 写回，与 node 侧新代墓碑
		// 形成「hub live 旧代 / node 墓碑新代」分裂面：ensureBound 缓存命中跳过
		// 补绑定 → 后续派发在 node 执行口全拒（会话派发失效）。确定性收缩形态：
		// 直接以 noteBound(1) 代「迟到的低代 ack 回写」这一写动作（传输延迟即门
		// 语义，写面同一函数）。
		f := newMatrixFixture(t, false)
		type bindRes struct {
			gen   uint64
			owned bool
			err   error
		}
		// 门A：钉「gen1 bind ack」——gen1 已落 node、hub 回写未及（缓存空）。
		gateA := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == Scope(f.spaceID) && p.Generation == 1 && p.SessionID == f.sessID
		})
		gateA.installHub(f)
		t.Cleanup(gateA.release)
		resA := make(chan bindRes, 1)
		go func() {
			g, owned, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
			resA <- bindRes{g, owned, err}
		}()
		gateA.waitArrived(t)
		// 换绑 gen2（缓存空 → 新代；node 2 > live gen1 接受）——ack 直通（门A 只钉
		// gen1）→ {2,live}，genSeq=2。
		g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || g2 != 2 {
			t.Fatalf("换绑 gen2: gen=%d owned=%v err=%v", g2, owned2, err)
		}
		gateA.release()
		ra := <-resA
		if ra.err != nil || ra.gen != 1 || !ra.owned {
			t.Fatalf("gen1 应 (1,true,nil): %+v", ra)
		}
		// 门B（重订阅——Subscribe 单 handler 后装顶替先装，门A wrapper 内已停泊
		// goroutine 持自身闭包不受影响）：钉「补偿解除 ack gen2」。
		gateB := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 2 && p.SessionID == f.sessID
		})
		gateB.installHub(f)
		t.Cleanup(gateB.release)
		done := make(chan struct{}, 1)
		go func() {
			f.svc.compensateBindIfOwned("n1", f.sessID, g2)
			close(done)
		}()
		gateB.waitArrived(t) // claim 删缓存（genSeq=2 保留）→ node 墓碑2 → ack 停泊
		if _, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatal("前置：补偿 claim 应已删缓存（genSeq 保留 2）")
		}
		// 反例窗：迟到的低代 bind ack（gen1）在缓存空时回写——genSeq 高水位咨询
		// 必须拦下（缓存不得出现 {1,live}；否则分裂面成立）。
		f.svc.noteBound(f.sessID, 1)
		if e, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatalf("低代迟到回写不得复活（genSeq 高水位=2）: %+v ok=%v", e, ok)
		}
		// 新派发自愈：缓存空 → 分配 gen3（> 墓碑2）→ node PutIfGenNewer 接受。
		g3, owned3, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned3 || g3 != 3 {
			t.Fatalf("自愈换绑 gen3: gen=%d owned=%v err=%v", g3, owned3, err)
		}
		// 放行解除 ack2：Accepted 分支墓碑回写判据对 {3,live} 全不中 → 跳过零触碰。
		gateB.release()
		waitSignal(t, done, "compensateBindIfOwned 完成")
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != 3 {
			t.Fatalf("解除 ack2 回写不得触碰新代: %+v ok=%v", e, ok)
		}
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != 3 {
			t.Fatalf("node 应保持 gen3: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S3.2b 低墓碑推进_两代解除ack逆序消费_墓碑单调前进真分支", func(t *testing.T) {
		// W09-C codex R2 必修③反例闭环：`e.revoked && e.gen < *gen`（service.go
		// 墓碑回写判据第三支）并非防御性不可达——P/Q 两派发先后登记失败各自补偿
		// （cache 删除+node 解除），解除 ack 逆序消费：ack1 先落（缓存空 → !ok 分支
		// 写 {1,revoked}），ack2 后落 → 命中第三支推进墓碑 {2,revoked}。node 侧
		// RemoveIfGen 代次精确匹配不改写既有墓碑，hub 侧高水位推进由本分支承担。
		f := newMatrixFixture(t, false)
		g1, owned1, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned1 {
			t.Fatalf("bind gen1: %v", err)
		}
		// 双 ack 逆序消费的实现口径：Subscribe 单 handler（后装顶替先装）——门1
		// 先装钉停 ack1，门2 重装后 bind2/解除 ack2 走门2 wrapper；门1 wrapper 内
		// 已停泊 goroutine 持自身闭包，release 后仍直呼 hubCore.HandleFrame 落账。
		gate1 := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 1 && p.SessionID == f.sessID
		})
		gate1.installHub(f)
		t.Cleanup(gate1.release)
		done1 := make(chan struct{}, 1)
		go func() {
			f.svc.compensateBindIfOwned("n1", f.sessID, g1)
			close(done1)
		}()
		gate1.waitArrived(t) // P：claim 删缓存 → node 墓碑1 → 解除 ack1 停泊
		if _, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatal("前置 P：补偿 claim 应已删缓存")
		}
		// Q：缓存空 → gen2（> node 墓碑1，PutIfGenNewer 接受）→ {2,live}；
		// 随即补偿 gen2（claim 删缓存 → node 墓碑2 → 解除 ack2 停泊门2）。
		// 先重装门2（钉解除 ack gen2）——此后 bind2 ack（Space=scope）按谓词直通。
		gate2 := newBindFrameGate(func(p protocol.SpaceBindPayload) bool {
			return p.Space == "" && p.Generation == 2 && p.SessionID == f.sessID
		})
		gate2.installHub(f)
		t.Cleanup(gate2.release)
		g2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || g2 != 2 {
			t.Fatalf("Q bind gen2: %v", err)
		}
		done2 := make(chan struct{}, 1)
		go func() {
			f.svc.compensateBindIfOwned("n1", f.sessID, g2)
			close(done2)
		}()
		gate2.waitArrived(t)
		if _, ok := f.bindCacheOf(f.sessID); ok {
			t.Fatal("前置 Q：补偿 claim 应已删缓存")
		}
		// 先消费 ack1：缓存空 → Accepted 分支 !ok 形态写 {1,revoked}。
		gate1.release()
		waitSignal(t, done1, "补偿 gen1 完成")
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 1 {
			t.Fatalf("ack1 应落墓碑 {1,revoked}: %+v ok=%v", e, ok)
		}
		// 后消费 ack2：命中 e.revoked && e.gen(1) < 2 → 推进 {2,revoked}（真分支）。
		gate2.release()
		waitSignal(t, done2, "补偿 gen2 完成")
		if e, ok := f.bindCacheOf(f.sessID); !ok || !e.revoked || e.gen != 2 {
			t.Fatalf("ack2 应推进墓碑 {2,revoked}（低墓碑推进真分支）: %+v ok=%v", e, ok)
		}
		if got := f.binds.CurrentGen(f.sessID); got != 2 {
			t.Fatalf("node 墓碑高水位应单调保留 2: %d", got)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S3.3a 迟到结果writeback_换绑窗内收敛_新代绑定零触碰", func(t *testing.T) {
		f := newMatrixFixture(t, true) // barrier：棒阻塞在途
		it1, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
			RequestID: "s3-result-1", ExpectedVersion: 4, Note: "迟到结果回写"}, fakeHubSubject)
		if err != nil || it1.Status != collab.IntentAccepted || it1.TaskID == "" {
			t.Fatalf("Dispatch: %v %+v", err, it1)
		}
		gen1 := f.svc.BoundGenOf(f.sessID)
		// 先占空间锁再放行：终态收敛项被 worker 持有、停泊锁外（落账未发生）。
		m := f.svc.lockSpace(f.spaceID)
		m.Lock()
		dequeued := make(chan struct{}, 8)
		f.svc.setOnConvDequeued(func() {
			select {
			case dequeued <- struct{}{}:
			default:
			}
		})
		f.releaseBarrier()
		waitSignal(t, dequeued, "收敛项出队")
		// 换绑窗：旧代解除（墓碑 gen1）→ 新代 gen2——结果 writeback（收敛落账）
		// 尚未发生，迟到于换绑。
		f.svc.UnbindSessionGen("n1", f.sessID, gen1)
		gen2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || gen2 != gen1+1 {
			t.Fatalf("窗内换绑: gen=%d owned=%v err=%v", gen2, owned2, err)
		}
		if cur, ok := f.svc.IntentByRequest(f.spaceID, it1.RequestID); !ok || cur.Status != collab.IntentAccepted {
			t.Fatalf("前置：收敛应尚未落账: %+v ok=%v", cur, ok)
		}
		// 放锁 → 迟到 writeback 收敛 converged；收敛锚 session+seq 只触意图账，
		// 不触绑定面。
		m.Unlock()
		f.waitIntentStatus("s3-result-1", collab.IntentConverged)
		if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != gen2 {
			t.Fatalf("迟到结果回写不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
		}
		if e, ok := f.bindCacheOf(f.sessID); !ok || e.revoked || e.gen != gen2 {
			t.Fatalf("hub 缓存应保持新代 {live}: %+v ok=%v", e, ok)
		}
		f.requireNoPendingUnbind()
	})

	t.Run("S3.3b 迟到取消writeback_收敛停队在换绑窗内_新代绑定零触碰", func(t *testing.T) {
		f := newMatrixFixture(t, true)
		it1, _, err := f.svc.Dispatch(DispatchRequest{SpaceID: f.spaceID, Member: "n1/coder",
			RequestID: "s3-cancel-1", ExpectedVersion: 4, Note: "迟到取消回写"}, fakeHubSubject)
		if err != nil || it1.Status != collab.IntentAccepted {
			t.Fatalf("Dispatch: %v %+v", err, it1)
		}
		gen1 := f.svc.BoundGenOf(f.sessID)
		m := f.svc.lockSpace(f.spaceID)
		m.Lock()
		dequeued := make(chan struct{}, 8)
		f.svc.setOnConvDequeued(func() {
			select {
			case dequeued <- struct{}{}:
			default:
			}
		})
		// 取消在途棒：合并链恒两回调（hub 首落 nil 结果项直通退出 + node 合并真实
		// 事实项停泊锁外——TestCloseDrains 同口径）。
		if _, _, err := f.hubCore.CancelTask(it1.TaskID, "人工取消"); err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
		deadline := time.Now().Add(testWaitBudget)
		merged := false
		for time.Now().Before(deadline) {
			if tk, ok := f.hubCore.GetTask(it1.TaskID); ok && tk.Result != nil {
				merged = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !merged {
			t.Fatal("前置：取消合并应已触发")
		}
		waitSignal(t, dequeued, "收敛项一出队")
		waitSignal(t, dequeued, "收敛项二出队")
		// 换绑窗：旧代解除 → 新代 gen2——取消 writeback（收敛+停队）迟到于换绑。
		f.svc.UnbindSessionGen("n1", f.sessID, gen1)
		gen2, owned2, err := f.svc.ensureBindSession("n1", f.sessID, Scope(f.spaceID))
		if err != nil || !owned2 || gen2 != gen1+1 {
			t.Fatalf("窗内换绑: gen=%d owned=%v err=%v", gen2, owned2, err)
		}
		if cur, ok := f.svc.IntentByRequest(f.spaceID, it1.RequestID); !ok || !cur.Status.InFlight() {
			t.Fatalf("前置：收敛应尚未落账: %+v ok=%v", cur, ok)
		}
		// 放锁 → 迟到取消 writeback：cancelled + 系统暂停两落账。
		m.Unlock()
		deadline = time.Now().Add(testWaitBudget)
		for time.Now().Before(deadline) {
			cur, ok := f.svc.IntentByRequest(f.spaceID, it1.RequestID)
			sp, spok := f.svc.Space(f.spaceID)
			if ok && cur.Status == collab.IntentCancelled && spok && sp.Status == collab.StatusPaused {
				if got := f.binds.Space(f.sessID); got != Scope(f.spaceID) || f.binds.CurrentGen(f.sessID) != gen2 {
					t.Fatalf("迟到取消回写不得触碰新代绑定: space=%q gen=%d", got, f.binds.CurrentGen(f.sessID))
				}
				if e, ok2 := f.bindCacheOf(f.sessID); !ok2 || e.revoked || e.gen != gen2 {
					t.Fatalf("hub 缓存应保持新代 {live}: %+v ok=%v", e, ok2)
				}
				f.requireNoPendingUnbind()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("迟到取消 writeback 应收敛 cancelled 且空间暂停停队")
	})
}
