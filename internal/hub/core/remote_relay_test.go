// 跨机取回 hub 侧单测（P4④第二子轮/SPEC 4.12）：dep_resolve 解析与依赖归属
// 鉴权、artifact_fetch node 中继（应答转发/错误透传/离线快返/超时兜底/越权拒绝）。
package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// seedRemoteDep 直写 store 预置跨机依赖对：上游任务（nodeA 执行，携 idem 与
// 产物登记）+ 下游任务（nodeB 名下，DependsOn 引用该 idem）——鉴权与解析的
// 数据基础。返回上游任务 ID 与产物登记。
func seedRemoteDep(t *testing.T, f *hubFixture, content string) (string, protocol.Artifact) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	meta := protocol.Artifact{Path: "outputs/t-up/result.txt", Sha256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	now := protocol.NowUTC()
	err := f.st.Update(func(st *store.State) error {
		up := protocol.Task{ID: "t-up", TraceID: "tr-up", TargetNode: "nodeA", TargetAgent: "echo",
			SubmittedBy: "hub", IdemKey: "dep-1", Status: protocol.StatusCompleted, CreatedAt: now, UpdatedAt: now,
			Result: &protocol.TaskResult{Status: protocol.ResultCompleted, Artifacts: []protocol.Artifact{meta}, FinishedAt: now}}
		st.Tasks[up.ID] = up
		down := protocol.Task{ID: "t-down", TraceID: "tr-down", TargetNode: "nodeB", TargetAgent: "echo",
			SubmittedBy: "hub", DependsOn: []string{"dep-1"}, Status: protocol.StatusAcknowledged,
			CreatedAt: now, UpdatedAt: now}
		st.Tasks[down.ID] = down
		return nil
	})
	if err != nil {
		t.Fatalf("预置失败: %v", err)
	}
	return "t-up", meta
}

// reqEnv 以 node 身份构造请求帧。
func reqEnv(typ string, payload []byte) *protocol.Envelope {
	return &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: typ,
		MessageID: protocol.NewMessageID(), To: "hub", Payload: payload,
	}
}

// replyRecorder 应答录制端点（挂 nodeB，收 hub 的回帧；带锁——超时兜底等异步
// 路径在独立 goroutine 回帧）。
func replyRecorder(t *testing.T, ep *transport.Endpoint) func() []*protocol.Envelope {
	t.Helper()
	var mu sync.Mutex
	var rec []*protocol.Envelope
	ep.Subscribe(func(env *protocol.Envelope) {
		mu.Lock()
		defer mu.Unlock()
		rec = append(rec, env)
	})
	return func() []*protocol.Envelope {
		mu.Lock()
		defer mu.Unlock()
		return append([]*protocol.Envelope(nil), rec...)
	}
}

// TestDepResolveAuthorized 鉴权通过：脱敏 task_status 应答——含 ID/TargetNode/
// 产物清单，不含 Input（上游任务输入隐私不出 hub）。
func TestDepResolveAuthorized(t *testing.T) {
	f := newHub(t)
	seedRemoteDep(t, f, "payload-1")
	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	b, _ := jsonMarshal(protocol.DepResolvePayload{Idem: "dep-1"})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgDepResolve, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	frames := got()
	if len(frames) != 1 || frames[0].Type != protocol.MsgTaskStatus {
		t.Fatalf("应回一帧 task_status: %+v", frames)
	}
	if frames[0].ReplyTo == "" {
		t.Fatalf("应答回关联请求 mid")
	}
	var tk protocol.Task
	if err := jsonUnmarshal(frames[0].Payload, &tk); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if tk.ID != "t-up" || tk.TargetNode != "nodeA" || tk.IdemKey != "dep-1" {
		t.Fatalf("定位字段不符: %+v", tk)
	}
	if len(tk.Result.Artifacts) != 1 || tk.Result.Artifacts[0].Path != "outputs/t-up/result.txt" {
		t.Fatalf("产物清单不符: %+v", tk.Result)
	}
	if tk.Input.Stdin != "" || tk.SubmittedBy != "" {
		t.Fatalf("脱敏不彻底（Input/SubmittedBy 不得出 hub）: %+v", tk)
	}
}

// TestDepResolveForbiddenAndUnknown 越权（名下无依赖该 idem 的任务）与未知 idem
// 的确定拒绝。
func TestDepResolveForbiddenAndUnknown(t *testing.T) {
	f := newHub(t)
	seedRemoteDep(t, f, "payload-1")
	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)

	b, _ := jsonMarshal(protocol.DepResolvePayload{Idem: "dep-other"}) // nodeB 名下无此依赖
	if err := nodeB.Send("hub", reqEnv(protocol.MsgDepResolve, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeForbidden {
		t.Fatalf("越权应回 err(forbidden): %+v", fs)
	}

	b, _ = jsonMarshal(protocol.DepResolvePayload{Idem: "dep-1"})
	// 去掉下游任务的依赖声明（模拟越权直接刺探已存在的上游 idem）
	_ = f.st.Update(func(st *store.State) error {
		down := st.Tasks["t-down"]
		down.DependsOn = nil
		st.Tasks["t-down"] = down
		return nil
	})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgDepResolve, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 2 || fs[1].Err.Code != protocol.CodeForbidden {
		t.Fatalf("移除依赖后应仍 forbidden: %+v", fs)
	}

	b, _ = jsonMarshal(protocol.DepResolvePayload{Idem: "dep-1"})
	_ = f.st.Update(func(st *store.State) error {
		down := st.Tasks["t-down"]
		down.DependsOn = []string{"dep-1"}
		up := st.Tasks["t-up"]
		up.Status = protocol.StatusRunning // 上游未 completed：解析不满足
		up.Result = nil
		st.Tasks["t-up"], st.Tasks["t-down"] = up, down
		return nil
	})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgDepResolve, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 3 || fs[2].Err.Code != protocol.CodeTaskNotFound {
		t.Fatalf("无 completed 上游应回 err(task_not_found): %+v", fs)
	}
}

// TestNodeFetchRelayForward 中继正向：nodeB 请求 → nodeA 桩回 artifact_data →
// hub 校验后原样转发 nodeB（ReplyTo=请求 mid），请求方 mid 端到端关联。
func TestNodeFetchRelayForward(t *testing.T) {
	f := newHub(t)
	taskID, meta := seedRemoteDep(t, f, "payload-1")
	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)

	nodeA := f.bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		var p protocol.ArtifactFetchPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{
			TaskID: p.TaskID, Index: p.Index, Meta: meta,
			DataBase64: base64.StdEncoding.EncodeToString([]byte("payload-1")),
		})
		_ = nodeA.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: env.MessageID, To: "hub", Payload: b,
		})
	})

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	frames := got()
	if len(frames) != 1 || frames[0].Type != protocol.MsgArtifactData {
		t.Fatalf("应转发一帧 artifact_data: %+v", frames)
	}
	var p protocol.ArtifactDataPayload
	if err := jsonUnmarshal(frames[0].Payload, &p); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Meta.Path != meta.Path || p.DataBase64 == "" {
		t.Fatalf("转发内容不符: %+v", p)
	}
}

// TestNodeFetchRelayErrAndForbidden 错误透传（源回 err(artifact_not_found) 原样
// 转发请求方）与越权拒绝（名下无依赖不中继）。
func TestNodeFetchRelayErrAndForbidden(t *testing.T) {
	f := newHub(t)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)

	nodeA := f.bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		_ = nodeA.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgErr,
			MessageID: protocol.NewMessageID(), ReplyTo: env.MessageID, To: "hub",
			Err: &protocol.ErrInfo{Code: protocol.CodeArtifactNotFound, Msg: "产物文件不存在"},
		})
	})

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeArtifactNotFound {
		t.Fatalf("源 err 应原样转发: %+v", fs)
	}

	// 越权：无依赖归属的请求直接拒绝，不触达源 node
	_ = f.st.Update(func(st *store.State) error {
		down := st.Tasks["t-down"]
		down.DependsOn = nil
		st.Tasks["t-down"] = down
		return nil
	})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 2 || fs[1].Err.Code != protocol.CodeForbidden {
		t.Fatalf("越权取回应回 err(forbidden): %+v", fs)
	}
}

// TestNodeFetchRelayOffline 源 node 离线（bus 无端点）：快回 err(node_offline)
// ——请求方按「暂不可取」重试轮询，非永久失败。
func TestNodeFetchRelayOffline(t *testing.T) {
	f := newHub(t)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	// 不创建 nodeA 端点 = 不可达

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if fs := got(); len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeNodeOffline {
		t.Fatalf("离线应回 err(node_offline): %+v", fs)
	}
}

// TestNodeFetchRelayTimeout 源不应答：中继超时兜底 err(fetch_timeout)，waiter
// 拆表不泄漏（超时后的迟到应答落空）。
func TestNodeFetchRelayTimeout(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), RelayFetchTimeout: 80 * time.Millisecond, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: c}
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 收到也不应答：黑洞 */ })

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeFetchTimeout {
		t.Fatalf("超时应回 err(fetch_timeout): %+v", fs)
	}
	c.muArt.Lock()
	n := len(c.fetches)
	c.muArt.Unlock()
	if n != 0 {
		t.Fatalf("超时后 waiter 表应清空，剩 %d", n)
	}
}

// TestNodeFetchRelayMidCollision 同 mid 双请求不互踩（2026-10-24 审查必修1）：
// 同一请求方以相同 MessageID 先后请求同任务两个不同产物——hub 为每个请求分配
// 独立中继编号（源 node 看到的两个中继 mid 必不同），各自应答对照各自登记校验
// 后以原 mid 转发，互不消费、无误判。旧实现（原 mid 作表键）第二个 waiter 覆盖
// 第一个：首应答按错误登记校验回 mismatch、次应答落空、旧超时回调失效。
func TestNodeFetchRelayMidCollision(t *testing.T) {
	f := newHub(t)
	sum0 := sha256.Sum256([]byte("payload-0"))
	sum1 := sha256.Sum256([]byte("payload-1"))
	metas := []protocol.Artifact{
		{Path: "outputs/t-up/a.txt", Sha256: hex.EncodeToString(sum0[:]), Size: 9},
		{Path: "outputs/t-up/b.txt", Sha256: hex.EncodeToString(sum1[:]), Size: 9},
	}
	now := protocol.NowUTC()
	if err := f.st.Update(func(st *store.State) error {
		up := protocol.Task{ID: "t-up", TraceID: "tr-up", TargetNode: "nodeA", TargetAgent: "echo",
			SubmittedBy: "hub", IdemKey: "dep-1", Status: protocol.StatusCompleted, CreatedAt: now, UpdatedAt: now,
			Result: &protocol.TaskResult{Status: protocol.ResultCompleted, Artifacts: metas, FinishedAt: now}}
		st.Tasks[up.ID] = up
		down := protocol.Task{ID: "t-down", TraceID: "tr-down", TargetNode: "nodeB", TargetAgent: "echo",
			SubmittedBy: "hub", DependsOn: []string{"dep-1"}, Status: protocol.StatusAcknowledged,
			CreatedAt: now, UpdatedAt: now}
		st.Tasks[down.ID] = down
		return nil
	}); err != nil {
		t.Fatalf("预置失败: %v", err)
	}

	// nodeA 桩：只记录到达的中继请求（mid+index），应答由测试显式驱动
	type relayReq struct {
		relayMid string
		index    int
	}
	var mu sync.Mutex
	var recd []relayReq
	nodeA := f.bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		var p protocol.ArtifactFetchPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			t.Errorf("解析失败: %v", err)
			return
		}
		mu.Lock()
		recd = append(recd, relayReq{relayMid: env.MessageID, index: p.Index})
		mu.Unlock()
	})

	nodeB := f.bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)

	// 同 mid 两个请求先后发出（应答推迟：两 waiter 同时在途）
	sameMid := protocol.NewMessageID()
	for _, idx := range []int{0, 1} {
		b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: "t-up", Index: idx})
		if err := nodeB.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactFetch,
			MessageID: sameMid, To: "hub", Payload: b,
		}); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}

	mu.Lock()
	if len(recd) != 2 {
		t.Fatalf("nodeA 应收到 2 个中继请求, got %d", len(recd))
	}
	r1, r2 := recd[0], recd[1]
	mu.Unlock()
	if r1.relayMid == r2.relayMid {
		t.Fatalf("hub 应为两个请求分配不同中继编号，got 相同 %s（同 mid 互踩）", r1.relayMid)
	}

	// nodeA 各按其 index 的真实内容回帧（ReplyTo=各自中继 mid）
	for _, rr := range []relayReq{r1, r2} {
		content := fmt.Sprintf("payload-%d", rr.index)
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{
			TaskID: "t-up", Index: rr.index, Meta: metas[rr.index],
			DataBase64: base64.StdEncoding.EncodeToString([]byte(content)),
		})
		if err := nodeA.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: rr.relayMid, To: "hub", Payload: b,
		}); err != nil {
			t.Fatalf("应答发送失败: %v", err)
		}
	}

	fs := got()
	if len(fs) != 2 {
		t.Fatalf("请求方应收 2 帧 artifact_data 转发（无误判 err），got %+v", fs)
	}
	for i, fr := range fs {
		if fr.Type != protocol.MsgArtifactData || fr.ReplyTo != sameMid {
			t.Fatalf("帧 %d 应为 artifact_data 且以原 mid 回关联: %+v", i, fr)
		}
		var p protocol.ArtifactDataPayload
		if err := jsonUnmarshal(fr.Payload, &p); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		content, _ := base64.StdEncoding.DecodeString(p.DataBase64)
		if string(content) != fmt.Sprintf("payload-%d", i) || p.Meta.Path != metas[i].Path {
			t.Fatalf("帧 %d 应为其自身请求的产物（不互踩）: content=%q meta=%+v", i, content, p.Meta)
		}
	}
}

// TestNodeFetchRelaySpoofedReplyIgnored 伪应答不挤掉合法应答（2026-10-24 审查
// 必修2）：来源与执行 node 不符的应答帧不消费 waiter、不停超时——随后到达的
// 合法应答正常转发成功；只发伪应答不回合法应答时超时兜底照常回
// err(fetch_timeout)。旧实现：伪应答先拆表停表，来源校验失败即 return——合法
// 应答与超时双双丢失。
func TestNodeFetchRelaySpoofedReplyIgnored(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), RelayFetchTimeout: 250 * time.Millisecond, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: c}
	taskID, meta := seedRemoteDep(t, f, "payload-1")

	// nodeA 桩：记录中继 mid，应答由测试显式驱动
	var mu sync.Mutex
	var relayMids []string
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		mu.Lock()
		relayMids = append(relayMids, env.MessageID)
		mu.Unlock()
	})
	// spoof 以第三方身份（nodeEvil 端点，From 由传输层盖章）伪造匹配 ReplyTo 的应答
	spoof := func(replyMid string) {
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{TaskID: taskID, Index: 0,
			Meta:       protocol.Artifact{Path: "outputs/t-up/evil.txt", Sha256: hex.EncodeToString(make([]byte, 32)), Size: 1},
			DataBase64: base64.StdEncoding.EncodeToString([]byte("evil"))})
		if err := bus.Endpoint("nodeEvil").Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: replyMid, To: "hub", Payload: b,
		}); err != nil {
			t.Errorf("伪应答发送失败: %v", err)
		}
	}
	reply := func(replyMid string) {
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{TaskID: taskID, Index: 0, Meta: meta,
			DataBase64: base64.StdEncoding.EncodeToString([]byte("payload-1"))})
		if err := nodeA.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: replyMid, To: "hub", Payload: b,
		}); err != nil {
			t.Errorf("合法应答发送失败: %v", err)
		}
	}

	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})

	// 第一轮：伪应答先行 → 合法应答仍成功转发
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	mu.Lock()
	mid1 := relayMids[0]
	mu.Unlock()
	spoof(mid1)
	reply(mid1)
	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgArtifactData {
		t.Fatalf("伪应答后合法应答应成功转发: %+v", fs)
	}
	var p protocol.ArtifactDataPayload
	if err := jsonUnmarshal(fs[0].Payload, &p); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Meta.Path != meta.Path {
		t.Fatalf("转发应为合法内容: %+v", p)
	}

	// 第二轮：只发伪应答不回合法应答 → 超时兜底照常触发（伪应答不得停表）
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	mu.Lock()
	mid2 := relayMids[1]
	mu.Unlock()
	spoof(mid2)
	deadline := time.Now().Add(3 * time.Second)
	for len(got()) == 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	fs = got()
	if len(fs) != 2 || fs[1].Type != protocol.MsgErr || fs[1].Err.Code != protocol.CodeFetchTimeout {
		t.Fatalf("伪应答不得停掉超时兜底: %+v", fs)
	}
	c.muArt.Lock()
	n := len(c.fetches)
	c.muArt.Unlock()
	if n != 0 {
		t.Fatalf("超时拆表后 waiter 表应清空，剩 %d", n)
	}
}

// TestNodeFetchProbeInvalidated 探测无效化（2026-10-24 审查建议修5）：无权限方
// 对「存在的任务」「存在任务越界下标」「不存在的任务」的取回请求一律同一
// forbidden（同码同文案），无法借 task_not_found/artifact_not_found 差异判断
// 任务存在性或探测产物下标；已通过鉴权的请求方仍得精确 artifact_not_found。
func TestNodeFetchProbeInvalidated(t *testing.T) {
	f := newHub(t)
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeC := f.bus.Endpoint("nodeC")
	gotC := replyRecorder(t, nodeC)

	probes := []protocol.ArtifactFetchPayload{
		{TaskID: taskID, Index: 0},     // 任务存在、下标有效
		{TaskID: taskID, Index: 7},     // 任务存在、下标越界
		{TaskID: "t-absent", Index: 0}, // 任务不存在
	}
	first := ""
	for i, p := range probes {
		b, _ := jsonMarshal(p)
		if err := nodeC.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
		fs := gotC()
		if len(fs) != i+1 || fs[i].Type != protocol.MsgErr || fs[i].Err.Code != protocol.CodeForbidden {
			t.Fatalf("无权限探测帧 %d 应统一 forbidden: %+v", i, fs)
		}
		sig := fs[i].Err.Code + "|" + fs[i].Err.Msg
		if i == 0 {
			first = sig
		} else if sig != first {
			t.Fatalf("无权限场景响应应逐字节一致: %q vs %q", sig, first)
		}
	}

	// 有权限方（nodeB 名下 t-down 依赖 dep-1）：越界下标仍得精确 artifact_not_found
	nodeB := f.bus.Endpoint("nodeB")
	gotB := replyRecorder(t, nodeB)
	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 7})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	fs := gotB()
	if len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeArtifactNotFound {
		t.Fatalf("有权限方越界下标应得精确 artifact_not_found: %+v", fs)
	}
}

// TestCloseStopsRelayTimers Close 收束在途中继（2026-10-24 审查建议修6）：Close
// 停存活 relay timer、清在途表——超时点过后请求方不再收到任何帧（关闭后不调
// transport）；关闭后新到取回请求直接统一拒绝，不再登记在途。
func TestCloseStopsRelayTimers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := New(Config{Store: st, Transport: bus.Endpoint("hub"), RelayFetchTimeout: 150 * time.Millisecond, SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)

	f := &hubFixture{t: t, bus: bus, hubEP: bus.Endpoint("hub"), st: st, core: c}
	taskID, _ := seedRemoteDep(t, f, "payload-1")
	nodeB := bus.Endpoint("nodeB")
	got := replyRecorder(t, nodeB)
	nodeA := bus.Endpoint("nodeA")
	nodeA.Subscribe(func(env *protocol.Envelope) { /* 黑洞：不应答 */ })

	b, _ := jsonMarshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: 0})
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 超时点（150ms）已过：timer 已停，不得有超时 err 回帧
	time.Sleep(300 * time.Millisecond)
	if fs := got(); len(fs) != 0 {
		t.Fatalf("Close 后不得再回帧: %+v", fs)
	}
	c.muArt.Lock()
	n, closed := len(c.fetches), c.artClosed
	c.muArt.Unlock()
	if n != 0 || !closed {
		t.Fatalf("Close 应清空在途表并置关闭位: n=%d closed=%v", n, closed)
	}

	// 关闭后新请求：不登记在途，统一回 node_offline（可重试），无 timer 残留
	if err := nodeB.Send("hub", reqEnv(protocol.MsgArtifactFetch, b)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	fs := got()
	if len(fs) != 1 || fs[0].Type != protocol.MsgErr || fs[0].Err.Code != protocol.CodeNodeOffline {
		t.Fatalf("关闭后新请求应统一拒绝 err(node_offline): %+v", fs)
	}
	time.Sleep(200 * time.Millisecond)
	if fs := got(); len(fs) != 1 {
		t.Fatalf("关闭后不得有额外帧: %+v", fs)
	}
	c.muArt.Lock()
	n = len(c.fetches)
	c.muArt.Unlock()
	if n != 0 {
		t.Fatalf("关闭后不得登记在途: %d", n)
	}
}
