package session

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/conn"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- 集成测试（SPEC 八.6：投递/确认/重连对账接通真实会话）----------
// 复用 Loop 2 全链断言方式：真实 store/core/session + 真实 conn/service/ledger/
// runner 经真实 WSS（或 WSS+TLS）完成注册与任务往返。

// echoProfile /bin/echo 执行配置（产物约定目录以 workdir 隔离）。
func echoProfile(t *testing.T) protocol.ExecProfile {
	return protocol.ExecProfile{
		Name: "echo-demo", Kind: protocol.AgentKindCLI,
		Command: "/bin/echo", Workdir: t.TempDir(),
		TimeoutSec: 10, AllowArgs: true, Exposed: true,
		Description: "回声测试", PermissionMode: "no-shell",
	}
}

// baseTestEnv 与 runner 测试同口径基线。
func baseTestEnv() map[string]string {
	return map[string]string{"PATH": "/usr/bin:/bin", "TMPDIR": "/tmp", "LANG": "en_US.UTF-8"}
}

// startedNode 真实 node：conn.Client + service + 真账本 + 真 runner。
type startedNode struct {
	Client *conn.Client
	Ledger *ledger.Ledger
	Svc    *service.Service
	cancel context.CancelFunc
}

func startNode(t *testing.T, hubURL string, nodeID, token string, profiles []protocol.ExecProfile,
	tlsSkipVerify bool) *startedNode {
	return startNodeAt(t, t.TempDir(), hubURL, nodeID, token, profiles, tlsSkipVerify)
}

// startNodeAt 以指定账本目录装配真实 node（S2：重启恢复测试复用同一目录）。
// C3：装配 OnRegistered→补传（与 cmd/node/main.go 同构闭包）——重连成功即触发
// ResendPendingAsync，测试因此覆盖「生产装配缺失则补传退化为 60s 周期」的回归。
func startNodeAt(t *testing.T, ledgerDir, hubURL string, nodeID, token string,
	profiles []protocol.ExecProfile, tlsSkipVerify bool) *startedNode {
	t.Helper()
	led, _, err := ledger.Open(ledgerDir)
	if err != nil {
		t.Fatalf("打开账本: %v", err)
	}
	runners := make(map[string]service.Runner, len(profiles))
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, baseTestEnv(), filepath.Join(t.TempDir(), "home"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	var svc *service.Service
	client := conn.NewClient(conn.Config{
		HubURL: hubURL, NodeID: nodeID, Token: token,
		Agents:        service.CardsFromProfiles(profiles),
		TLSSkipVerify: tlsSkipVerify,
		OnRegistered: func() {
			if svc != nil {
				svc.ResendPendingAsync()
			}
		},
	})
	svc = service.New(service.Config{Ledger: led, Runners: runners}, client)
	go func() { _ = client.Run(ctx) }() // 退避重连循环
	n := &startedNode{Client: client, Ledger: led, Svc: svc, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		svc.Close()
		_ = led.Close()
	})
	return n
}

// submitReq 构造提交请求。
func submitReq(targetNode, agent string, args ...string) protocol.TaskSubmitRequest {
	return protocol.TaskSubmitRequest{
		TargetNode: targetNode, TargetAgent: agent,
		Input: protocol.TaskInput{Args: args}, SubmittedBy: "hana",
	}
}

// TestE2ETaskRoundTrip 全链往返（DoD 3 链路经真实 WSS）：注册 → HTTP 等价提交
// （core.Submit，submitterNode=""）→ offer/ack/started/result/result_ack →
// 5s 内 completed 且 FinalAnswer 正确。
func TestE2ETaskRoundTrip(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeB": "tok-b"}, true, time.Second, nil)
	n := startNode(t, h.wsURL(), "nodeB", "tok-b", []protocol.ExecProfile{echoProfile(t)}, false)
	if !waitFor(5*time.Second, func() bool {
		_, ok := n.Client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("node 应在 5s 内注册成功")
	}
	if n.Client.Epoch() != 1 {
		t.Fatalf("首次注册 epoch 应为 1, got %d", n.Client.Epoch())
	}

	req := submitReq("nodeB", "echo-demo", "hello", "swarmmind")
	req.IdemKey = "e2e-001"
	ack, err := h.core.Submit(req, "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		task, ok := h.taskOf(ack.TaskID)
		return ok && task.Status.Terminal()
	}) {
		t.Fatal("任务未在 5s 内到终态")
	}
	task, _ := h.taskOf(ack.TaskID)
	if task.Status != protocol.StatusCompleted || task.Result == nil {
		t.Fatalf("应 completed, got %s %+v", task.Status, task.Result)
	}
	if task.Result.FinalAnswer != "hello swarmmind\n" || task.Result.ExitCode != 0 {
		t.Errorf("结果不符: %+v", task.Result)
	}
	// node 账本：finished 且已 acked（三段持久化确认闭合，SPEC 4.5；result_ack
	// 为异步回程，轮询至收敛）
	if !waitFor(2*time.Second, func() bool {
		e, ok := n.Ledger.Get(ack.TaskID)
		return ok && e.State == ledger.StateFinished && e.ResultAcked
	}) {
		e, _ := n.Ledger.Get(ack.TaskID)
		t.Fatalf("账本应 finished+acked, got %+v", e)
	}
}

// TestE2ETLS TLS 链路（SPEC 二）：EnsureCert 自签生成（ECDSA P-256、重启复用）、
// ServeTLS 监听、node 经 wss 完成注册与任务往返（指纹固定 TOFU 属 Loop 4，
// 测试以 TLSSkipVerify 拨号）。
func TestE2ETLS(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeT": "tok-t"}, true, time.Second, nil)
	h.stopServer()

	certDir := t.TempDir()
	cert, err := sessionLoadCert(certDir)
	if err != nil {
		t.Fatalf("生成证书: %v", err)
	}
	// 重启复用：再次生成应返回同一证书（不换证书，node 侧指纹才稳定）
	cert2, err := sessionLoadCert(certDir)
	if err != nil {
		t.Fatalf("复用证书: %v", err)
	}
	if string(cert.Certificate[0]) != string(cert2.Certificate[0]) {
		t.Fatal("重启后应复用既有证书")
	}
	// 证书断言：ECDSA P-256（tls.Certificate.Certificate[0] 为 DER 编码）
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := parsed.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("应 ECDSA P-256, got %T", parsed.PublicKey)
	}

	h.serveTLS(cert)
	url := "wss://" + h.ln.addr().String() + "/ws/node"
	n := startNode(t, url, "nodeT", "tok-t", []protocol.ExecProfile{echoProfile(t)}, true)
	if !waitFor(5*time.Second, func() bool {
		_, ok := n.Client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("node 应经 wss 注册成功")
	}
	ack, err := h.core.Submit(submitReq("nodeT", "echo-demo", "via", "tls"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		task, ok := h.taskOf(ack.TaskID)
		return ok && task.Status == protocol.StatusCompleted &&
			task.Result != nil && task.Result.FinalAnswer == "via tls\n"
	}) {
		t.Fatalf("任务应经 TLS 链路完成, got %+v", func() *protocol.Task {
			tk, _ := h.taskOf(ack.TaskID)
			return &tk
		}())
	}
}

// serveTLS 以自签证书重启 hub 监听（wss）。
func (h *hubStack) serveTLS(cert tls.Certificate) {
	h.t.Helper()
	ln, err := net.Listen("tcp", h.ln.addr().String())
	if err != nil {
		h.t.Fatalf("TLS 监听失败: %v", err)
	}
	h.ln.swapListener(ln)
	mux := http.NewServeMux()
	mux.Handle("GET /ws/node", h.mgr)
	h.srv = &http.Server{Handler: mux, TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	}}
	srv := h.srv
	wrapped := h.ln
	go func() { _ = srv.ServeTLS(wrapped, "", "") }()
}

// TestE2EDeliveryWindowPauseResume 投递窗口（SPEC 4.6：offered+acknowledged ≤8
// 暂停出队；started/终态释放后恢复）：桩 node 只 ack 不 start → 恰投 8；
// 4 次 started 后补投剩余 2。
func TestE2EDeliveryWindowPauseResume(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeW": "tok-w"}, true, time.Second, nil)

	// 桩 node：注册 1 个 agent，收 offer 只 ack 不 start
	var offers atomic.Int64
	c := wsDial(t, h.wsURL(), nil)
	ackP := wsRegister(t, c, "nodeW", "tok-w", []protocol.AgentCard{cardDemo("w")})
	if !ackP.OK || ackP.Epoch != 1 {
		t.Fatalf("注册失败: %+v", ackP)
	}
	stopPump := make(chan struct{})
	var pumpOnce sync.Once
	go func() {
		for {
			select {
			case <-stopPump:
				return
			default:
			}
			ctx, cancel := contextWithTimeout(500 * time.Millisecond)
			typ, data, err := c.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			env, err := protocol.Decode(data)
			if err != nil {
				continue
			}
			switch env.Type {
			case protocol.MsgPing:
				wsWriteEnv(t, c, &protocol.Envelope{
					Version: protocol.ProtocolVersion, Type: protocol.MsgPong,
					MessageID: protocol.NewMessageID(), To: "hub",
					SessionEpoch: env.SessionEpoch,
				})
			case protocol.MsgTaskOffer:
				offers.Add(1)
				var task protocol.Task
				_ = jsonUnmarshal(env.Payload, &task)
				wsWriteEnv(t, c, &protocol.Envelope{ // 只 ack，不 started → 占满窗口
					Version: protocol.ProtocolVersion, Type: protocol.MsgTaskAck,
					MessageID: protocol.NewMessageID(), To: "hub",
					SessionEpoch: env.SessionEpoch,
					Payload:      mustJSON(protocol.TaskRef{TaskID: task.ID}),
				})
			}
		}
	}()
	t.Cleanup(func() { pumpOnce.Do(func() { close(stopPump) }) })

	// 提交 10 个任务 → 窗口 8 满投，2 滞留 submitted
	var ids []string
	for i := 0; i < 10; i++ {
		ack, err := h.core.Submit(submitReq("nodeW", "w", "job"), "")
		if err != nil {
			t.Fatalf("提交 %d: %v", i, err)
		}
		ids = append(ids, ack.TaskID)
	}
	if !waitFor(3*time.Second, func() bool { return offers.Load() == 8 }) {
		t.Fatalf("窗口应暂停在 8 次 offer, got %d", offers.Load())
	}
	statusCount := func() map[protocol.TaskStatus]int {
		m := map[protocol.TaskStatus]int{}
		for _, id := range ids {
			tk, _ := h.taskOf(id)
			m[tk.Status]++
		}
		return m
	}
	got := statusCount()
	// 桩节点秒回 ack：offered 会立即转为 acknowledged，两态合计即窗口占用
	if got[protocol.StatusOffered]+got[protocol.StatusAcknowledged] != 8 || got[protocol.StatusSubmitted] != 2 {
		t.Fatalf("窗口应占用 8、滞留 2, got %v", got)
	}
	// 释放 4 格（started ×4）→ 恢复出队，补投 2 → 全部 10 次投递
	for i := 0; i < 4; i++ {
		wsWriteEnv(t, c, &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgTaskStarted,
			MessageID: protocol.NewMessageID(), To: "hub", SessionEpoch: 1,
			Payload: mustJSON(protocol.TaskRef{TaskID: ids[i]}),
		})
	}
	if !waitFor(3*time.Second, func() bool { return offers.Load() == 10 }) {
		t.Fatalf("started 释放窗口后应补投至 10, got %d", offers.Load())
	}
	// 补投任务的 ack 在途，轮询至全链收敛：4 running + 6 acknowledged
	if !waitFor(3*time.Second, func() bool {
		g := statusCount()
		return g[protocol.StatusRunning] == 4 && g[protocol.StatusAcknowledged] == 6 &&
			g[protocol.StatusOffered] == 0 && g[protocol.StatusSubmitted] == 0
	}) {
		g := statusCount()
		t.Fatalf("状态应收敛为 4 running + 6 acknowledged, got %v", g)
	}
	pumpOnce.Do(func() { close(stopPump) })
}

// TestE2EReconnectEpochIncrementAndMailbox 重连韧性与离线信箱（DoD 5/6 链路）：
// hub 宕机 → node 退避重连 → 恢复后自动注册（epoch 递增）；宕机期间提交入信箱，
// 重连补推完成。
// 能力边界（S2）：本测试复用同一 store/core/Manager、仅重启监听，不证明磁盘恢复；
// 完整销毁重建见 TestE2EHubRestartDiskRecovery，kill node 见
// TestE2EKillStartedTaskNeedsReview。
func TestE2EReconnectEpochIncrementAndMailbox(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeR": "tok-r"}, true, time.Second, nil)
	n := startNode(t, h.wsURL(), "nodeR", "tok-r", []protocol.ExecProfile{echoProfile(t)}, false)
	if !waitFor(5*time.Second, func() bool {
		_, ok := n.Client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("node 应注册成功")
	}
	ack1, err := h.core.Submit(submitReq("nodeR", "echo-demo", "first"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool {
		tk, _ := h.taskOf(ack1.TaskID)
		return tk.Status == protocol.StatusCompleted
	}) {
		t.Fatal("首个任务应完成")
	}

	// hub 宕机：断开全部连接；node 进入退避重连
	h.stopServer()
	if !waitFor(5*time.Second, func() bool {
		tr := h.onlineOf("nodeR")
		return len(tr) >= 2 && !tr[len(tr)-1]
	}) {
		t.Fatal("宕机后 node 应离线")
	}
	// 离线提交 → 入信箱（无第二条投递路径，SPEC 六）
	ack2, err := h.core.Submit(submitReq("nodeR", "echo-demo", "offline"), "")
	if err != nil {
		t.Fatal(err)
	}
	if tk, _ := h.taskOf(ack2.TaskID); tk.Status != protocol.StatusSubmitted {
		t.Fatalf("离线目标任务应滞留 submitted, got %s", tk.Status)
	}

	// hub 恢复：node 自动重连注册（epoch 递增）→ 信箱补推完成
	h.restartServer()
	if !waitFor(10*time.Second, func() bool {
		tk, _ := h.taskOf(ack2.TaskID)
		return tk.Status == protocol.StatusCompleted
	}) {
		log.Printf("[test] hub 状态: %+v", h.store.View().Mailboxes)
		t.Fatal("重连后离线任务应补推完成")
	}
	if got := h.store.View().Nodes["nodeR"].Epoch; got < 2 {
		t.Fatalf("重连注册应使 epoch 递增, got %d", got)
	}
	if n.Client.Epoch() < 2 {
		t.Fatalf("client 应记录新 epoch, got %d", n.Client.Epoch())
	}
	_, synced := n.Client.LastRegisteredAt()
	if !synced {
		t.Fatal("应有最近同步时间")
	}
}

// sessionLoadCert 供测试调用（包内转发，避免直接依赖 tls.LoadX509KeyPair 细节）。
func sessionLoadCert(dir string) (tls.Certificate, error) {
	certPath, keyPath, err := EnsureCert(dir)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// relistenAt 在指定地址重建监听并重启 server（S2：完整销毁重建后保持原地址——
// 既有 node 的重连目标 URL 不变，自动重连测试的前提）。
func (h *hubStack) relistenAt(addr string) {
	h.t.Helper()
	h.waitOldServe()
	_ = h.ln.Close() // 关闭 newHubStackAt 自带的随机端口监听
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		h.t.Fatalf("重建监听 %s 失败: %v", addr, err)
	}
	h.ln.swapListener(ln)
	h.startServing()
}

// TestE2EHubRestartDiskRecovery S2/C3（DoD 7）：完整销毁 store/core/Manager 并在
// 同一 data_dir 重建（区别于仅重启监听的 TestE2EReconnectEpochIncrementAndMailbox）：
// epoch 计数、能力表、任务从磁盘恢复；存活的原 node 不做任何重启、自动重连到
// 重建后的 hub（epoch 基于磁盘恢复计数递增），宕机期间提交的离线任务经信箱补推。
//
// 能力边界（S2 局限声明）：本测试不重启 node 进程——真实进程 kill 在 V0 测试
// 沙箱不可行（进程无法 SIGKILL 自身后继续断言）；node 侧销毁重建以同进程取消
// 模拟，见 TestE2EKillStartedTaskNeedsReview（含同样的局限声明）。
func TestE2EHubRestartDiskRecovery(t *testing.T) {
	dir := t.TempDir()
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	tokens := map[string]string{"nodeD": "tok-d"}

	h1 := newHubStackAt(t, dir, tokens, true, time.Second, nil)
	hubAddr := h1.ln.addr().String() // 重建后同端口重开，原 node 的重连目标不变
	n1 := startNodeAt(t, ledgerDir, h1.wsURL(), "nodeD", "tok-d", []protocol.ExecProfile{echoProfile(t)}, false)
	if !waitFor(5*time.Second, func() bool {
		_, ok := n1.Client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("首次注册应成功")
	}
	ack, err := h1.core.Submit(submitReq("nodeD", "echo-demo", "before", "restart"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool {
		tk, _ := h1.taskOf(ack.TaskID)
		return tk.Status == protocol.StatusCompleted
	}) {
		t.Fatal("重启前任务应完成")
	}

	// 完整销毁 hub：会话 → core → 监听 → store（SPEC 九顺序）；node 保持存活，
	// 进入退避重连（自动重连断言的前提）
	if err := h1.mgr.CloseWithBudget(3 * time.Second); err != nil {
		t.Fatalf("关闭会话: %v", err)
	}
	_ = h1.core.Close()
	_ = h1.srv.Close()
	_ = h1.store.Close()
	if !waitFor(5*time.Second, func() bool {
		tr := h1.onlineOf("nodeD")
		return len(tr) >= 2 && !tr[len(tr)-1]
	}) {
		t.Fatal("hub 宕机后原 node 应离线")
	}

	// 磁盘恢复核验（独立只读窗口，先于重建打开 store）：避免与 node 自动重连
	// 的注册写并发，epoch 断言因此确定
	stChk, err := store.Open(dir)
	if err != nil {
		t.Fatalf("只读核验打开 store: %v", err)
	}
	rec := stChk.View().Nodes["nodeD"]
	if rec.Epoch != 1 {
		t.Fatalf("epoch 计数应从磁盘恢复为 1, got %d", rec.Epoch)
	}
	if len(rec.Agents) != 1 || rec.Agents[0].Name != "echo-demo" {
		t.Fatalf("能力表应从磁盘恢复, got %+v", rec.Agents)
	}
	if tk, ok := stChk.View().Tasks[ack.TaskID]; !ok || tk.Status != protocol.StatusCompleted {
		t.Fatalf("已完成任务应从磁盘恢复, got %+v", tk)
	}
	_ = stChk.Close()

	// 同一 data_dir 重建 store/core/Manager，并在原地址重建监听
	h2 := newHubStackAt(t, dir, tokens, true, time.Second, nil)
	h2.relistenAt(hubAddr)

	// node 仍处于退避重连中（离线）：此刻提交 → 入信箱滞留 submitted；
	// 随后自动重连注册即触发信箱补推（待投递信箱恢复）
	ack2, err := h2.core.Submit(submitReq("nodeD", "echo-demo", "offline", "rebuild"), "")
	if err != nil {
		t.Fatal(err)
	}
	if tk, _ := h2.taskOf(ack2.TaskID); tk.Status != protocol.StatusSubmitted {
		t.Fatalf("离线目标任务应滞留 submitted, got %s", tk.Status)
	}

	// 原 node 自动重连：注册 epoch 基于磁盘恢复的计数递增（≥2），离线任务补推完成。
	// 预算按退避节拍取宽（0.5s×2^n±20%：宕机期间多次快失败后下一拍可达 8s+）
	if !waitFor(30*time.Second, func() bool { return n1.Client.Epoch() >= 2 }) {
		t.Fatalf("原 node 应自动重连注册（epoch 递增）, got %d", n1.Client.Epoch())
	}
	if _, ok := n1.Client.LastRegisteredAt(); !ok {
		t.Fatal("重连注册后应有最近同步时间")
	}
	if got := h2.store.View().Nodes["nodeD"].Epoch; got < 2 {
		t.Fatalf("重连注册应使持久化 epoch 递增, got %d", got)
	}
	if tr := h2.onlineOf("nodeD"); len(tr) < 1 || !tr[len(tr)-1] {
		t.Fatalf("重连后应重新上线, got %v", tr)
	}
	if !waitFor(15*time.Second, func() bool {
		tk, _ := h2.taskOf(ack2.TaskID)
		return tk.Status == protocol.StatusCompleted && tk.Result.FinalAnswer == "offline rebuild\n"
	}) {
		t.Fatal("重连后离线任务应经信箱补推完成")
	}
}

// TestE2EKillStartedTaskNeedsReview S2（DoD 10 链路）：任务 started 后 kill node、
// 重启（同账本目录）→ 启动恢复置 needs_review/node_restarted_midway（不自动重跑），
// 结果经重连注册回传 hub 可查（C3：注册事件触发补传）。
func TestE2EKillStartedTaskNeedsReview(t *testing.T) {
	h := newHubStack(t, map[string]string{"nodeK": "tok-k"}, true, time.Second, nil)
	sleepProfile := func(t *testing.T) protocol.ExecProfile {
		return protocol.ExecProfile{
			Name: "sleeper", Kind: protocol.AgentKindCLI,
			Command: "/bin/sleep", Workdir: t.TempDir(),
			TimeoutSec: 30, AllowArgs: true, Exposed: true,
			Description: "睡眠测试", PermissionMode: "no-shell",
		}
	}
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	n1 := startNodeAt(t, ledgerDir, h.wsURL(), "nodeK", "tok-k", []protocol.ExecProfile{sleepProfile(t)}, false)
	if !waitFor(5*time.Second, func() bool {
		_, ok := n1.Client.LastRegisteredAt()
		return ok
	}) {
		t.Fatal("注册应成功")
	}
	ack, err := h.core.Submit(submitReq("nodeK", "sleeper", "5"), "")
	if err != nil {
		t.Fatal(err)
	}
	// 等任务真实进入 started（进程已在跑）
	if !waitFor(5*time.Second, func() bool {
		e, ok := n1.Ledger.Get(ack.TaskID)
		return ok && e.State == ledger.StateStarted
	}) {
		t.Fatal("前置：任务应已 started")
	}

	// kill node：取消 Run 并断连、关闭账本（不等待在途执行——模拟进程死亡）。
	// S2 局限声明：真实进程 kill（SIGKILL 后由 OS 关闭 socket、强释放文件锁、
	// 子进程变孤儿）在 V0 测试沙箱不可行——测试进程无法杀死自身后继续断言；
	// 此处仅覆盖同进程取消的可见语义（连接断开、账本锁释放、started 无终态），
	// 不覆盖 OS 级终止时序。
	n1.cancel()
	_ = n1.Client.Close()
	_ = n1.Ledger.Close()
	time.Sleep(100 * time.Millisecond)

	// 重启：同一账本目录重新 Open → started 恢复为 needs_review（禁止自动重跑）
	n2 := startNodeAt(t, ledgerDir, h.wsURL(), "nodeK", "tok-k", []protocol.ExecProfile{sleepProfile(t)}, false)
	if !waitFor(5*time.Second, func() bool { return n2.Client.Epoch() >= 2 }) {
		t.Fatalf("重启后应重新注册, epoch=%d", n2.Client.Epoch())
	}
	// 恢复条目经注册成功回调补传（C3），hub 终态为 needs_review
	if !waitFor(8*time.Second, func() bool {
		tk, ok := h.taskOf(ack.TaskID)
		return ok && tk.Status == protocol.StatusNeedsReview
	}) {
		tk, _ := h.taskOf(ack.TaskID)
		t.Fatalf("任务应为 needs_review, got %+v", tk)
	}
	tk, _ := h.taskOf(ack.TaskID)
	if tk.Result == nil || !strings.Contains(tk.Result.Error, protocol.CodeNodeRestartedMidway) {
		t.Fatalf("错误应含 node_restarted_midway, got %+v", tk.Result)
	}
	// 账本确认不自动重跑：结果为 needs_review 且未再次 spawn
	e, ok := n2.Ledger.Get(ack.TaskID)
	if !ok || e.State != ledger.StateFinished || e.Result.Status != protocol.ResultNeedsReview {
		t.Fatalf("账本应为 finished/needs_review, got %+v", e)
	}
}
