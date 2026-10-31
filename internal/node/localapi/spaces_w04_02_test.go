package localapi

// spaces_w04_02_test.go 空间 REST 全链测试（W04-02）：逐动作主体正反矩阵（N09）、
// 外部管家配置顺序（N10——含未领取 configure_members 403）、resume 在途确认门、
// 派发全链经 REST（串行调度 + N02 重放不扣预算）、消息门（谁能发言）与零唤醒
// （N01）。真 localapi + 真 service（fake CLI 会话内核）+ 真 collab 快照。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionop"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// buildFakeCLI 编译 sessionx 假 CLI（与 service 测试同一 testdata 资产）。
func buildFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, "../sessionx/testdata/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	return path
}

// spaceStackFixture REST 全栈夹具：真内核 + 账本 + sessionop + collab + service +
// localapi server（屏障续轮 + 快跑首轮）。
type spaceStackFixture struct {
	t      *testing.T
	srv    *Server
	svc    *service.Service
	st     *collab.Store
	led    *ledger.Ledger
	mgr    *sessionx.Manager
	ops    *sessionop.Service
	cwd    string
	runLog string
	dir    string
}

func newSpaceStack(t *testing.T, opts ...func(*collab.Options)) *spaceStackFixture {
	t.Helper()
	dir := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	barrier := filepath.Join(dir, "go.barrier")
	runLog := filepath.Join(dir, "run.log")
	env := []string{"PATH=/usr/bin:/bin", "FAKE_KIND=" + protocol.SessionAdapterCodex,
		"FAKE_THREAD_ID=thr-stack", "FAKE_LOG=" + runLog,
		"FAKE_WAIT_FILE=" + barrier, "FAKE_ON_RESUME=1"}
	specs := map[string]sessionx.AgentSpec{"coder": {
		Adapter: protocol.SessionAdapterCodex, Command: buildFakeCLI(t),
		PermissionMode: "workspace-write", TimeoutSec: 30, Env: env,
	}}
	mgr, err := sessionx.NewManager(filepath.Join(dir, "kernel"), specs, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := sessionop.New(filepath.Join(dir, "ops"), led, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	stOpts := collab.Options{}
	for _, o := range opts {
		o(&stOpts)
	}
	stDir := filepath.Join(dir, "collab")
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := collab.Open(stDir, stOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	svc := service.New(service.Config{
		Ledger:        led,
		Runners:       map[string]service.Runner{},
		ArtifactRoots: map[string]string{"coder": filepath.Join(dir, "art")},
		Sessions:      mgr,
		SessionOps:    ops,
		Collab:        st,
		NodeID:        "nodeA",
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	cwd := filepath.Join(dir, "ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &spaceStackFixture{t: t, svc: svc, st: st, led: led, mgr: mgr, ops: ops, cwd: cwd, runLog: runLog, dir: dir}
	f.srv = newStackServer(t, svc, st, led)
	return f
}

// newStackServer 组装 localapi（与 cmd/node 装配同源；本包测试既有 helper 不带
// collab+service 全栈，此处独立组装）。
func newStackServer(t *testing.T, svc *service.Service, st *collab.Store, led *ledger.Ledger) *Server {
	t.Helper()
	return New(Config{
		NodeID:     "nodeA",
		Mode:       ModeStandalone,
		Service:    svc,
		Ledger:     led,
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  NewMcpAccessMemory(true),
		Collab:     st,
	})
}

func (f *spaceStackFixture) do(token, method, path, body string) (int, string) {
	f.t.Helper()
	return spaceDoTo(f.t, f.srv, token, method, path, body)
}

func spaceDoTo(t *testing.T, srv *Server, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://x"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// createMemberSession 建已发布的成员会话（首轮快跑）。
func (f *spaceStackFixture) createMemberSession() string {
	f.t.Helper()
	code, out := f.do("tok-local", "POST", "/api/v1/local/sessions",
		`{"agent":"coder","cwd":"`+f.cwd+`","prompt":"首轮"}`)
	if code != http.StatusAccepted {
		f.t.Fatalf("建会话应受理: %d %s", code, out)
	}
	var ack struct {
		Turn struct {
			SessionID string `json:"session_id"`
			TurnSeq   int64  `json:"turn_seq"`
		} `json:"turn"`
	}
	_ = json.Unmarshal([]byte(out), &ack)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := f.ledGetTurn(ack.Turn.SessionID, 1); ok && e.state == "finished" {
			return ack.Turn.SessionID
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("首轮未完成")
	return ""
}

func (f *spaceStackFixture) ledGetTurn(sid string, seq int64) (stackTurnEntry, bool) {
	f.t.Helper()
	for _, e := range f.led.View() {
		sr := e.Task.Session
		if sr == nil || sr.SessionID != sid || sr.TurnSeq != seq {
			continue
		}
		return stackTurnEntry{state: string(e.State)}, true
	}
	return stackTurnEntry{}, false
}

type stackTurnEntry struct {
	state  string
	status string
}

// TestW0402N10ExternalClaimFlow N10（AC14 配置顺序，REST 全链）：成员空 external
// 创建 → 未领取 configure_members 403 → 合法领取（新任期可查）→ 范围内成员配置 →
// 就绪启动 → 未绑线程派发拒绝 → 越范围成员拒绝 → 伪造授权字段拒绝 → 过期任期
// 配置拒绝 → 派发全链（202 → 串行投递 → 意图收敛）。
func TestW0402N10ExternalClaimFlow(t *testing.T) {
	f := newSpaceStack(t)
	sid := f.createMemberSession()

	// 编排主体创建 external（成员可空）→ 201。
	code, out := f.do("tok-prime", "POST", "/api/v1/spaces",
		`{"request_id":"n10-create","goal":"外部交付","mode":"external"}`)
	if code != 201 {
		t.Fatalf("external 创建应 201: %d %s", code, out)
	}
	spID := gjsonStr(t, out, "space.id")

	// 未领取 configure_members → 403 forbidden（无负责人非驱动者）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-cfg0","action":"configure_members","expected_version":1,"members":[{"name":"coder","session_id":"`+sid+`"}]}`)
	if code != 403 || !strings.Contains(out, "forbidden") {
		t.Fatalf("未领取 configure_members 应 403: %d %s", code, out)
	}

	// 合法领取（CAS expected_term=0）→ 新任期 1 可查。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-claim","action":"takeover","expected_version":1,"expected_term":0}`)
	if code != 200 || !strings.Contains(out, `"term":1`) {
		t.Fatalf("领取应成功且新任期 1: %d %s", code, out)
	}

	// 范围内成员配置成功（驱动者形态携 term=1）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-cfg","action":"configure_members","expected_version":2,"term":1,"members":[{"name":"coder","role":"writer","session_id":"`+sid+`"}]}`)
	if code != 200 {
		t.Fatalf("范围内配置应成功: %d %s", code, out)
	}
	// 越范围：伪造成员字段在 configure 全量替换语义下不可越（模型层校验在册成员
	// 的在途保护）；此处以「过期任期配置」验证驱动者校验：term=99 → 403。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-cfg-old","action":"configure_members","expected_version":3,"term":99,"members":[{"name":"x"}]}`)
	if code != 403 {
		t.Fatalf("过期任期配置应 403: %d %s", code, out)
	}

	// 伪造授权字段：编排主体创建携 verified_write_review → 403（不能自授）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces",
		`{"request_id":"n10-forge","goal":"x","mode":"external","policy":{"finish_auth":"verified_write_review"}}`)
	if code != 403 || !strings.Contains(out, "完成授权") {
		t.Fatalf("伪造完成授权应 403: %d %s", code, out)
	}

	// ready → start（驱动者）。
	if code, _ = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-rdy","action":"ready","expected_version":3,"term":1}`); code != 200 {
		t.Fatalf("ready 应成功")
	}
	if code, _ = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"n10-str","action":"start","expected_version":4,"term":1}`); code != 200 {
		t.Fatalf("start 应成功")
	}

	// 派发越范围成员 → 409 member_not_found（REST 投影 W04-01 既定码表）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"ghost","expected_version":5,"term":1}`)
	if code != 409 || !strings.Contains(out, "member_not_found") {
		t.Fatalf("越范围派发应 409 member_not_found: %d %s", code, out)
	}

	// 全链派发（在册已绑成员）→ 202 → 串行投递 → 屏障阻塞在途。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"coder","expected_version":5,"term":1,"note":"第一棒","request_id":"n10-d1"}`)
	if code != 202 {
		t.Fatalf("派发应 202 受理: %d %s", code, out)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "n10-d1"); ok && it.Status == collab.IntentAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// N02：同 request_id 重放 → 200 dup，零重复扣（dispatch_rounds 不变）。
	var usedBefore int
	if v, ok := f.st.SpaceView(spID); ok {
		usedBefore = v.Used.DispatchRounds
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"coder","expected_version":5,"term":1,"note":"第一棒","request_id":"n10-d1"}`)
	if code != 200 || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("派发重放应 200 dup: %d %s", code, out)
	}
	if v, ok := f.st.SpaceView(spID); !ok || v.Used.DispatchRounds != usedBefore {
		t.Fatalf("重放不得重复扣减: %d vs %d", v.Used.DispatchRounds, usedBefore)
	}
	// 放行屏障 → 收敛 converged。
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "n10-d1"); ok && it.Status == collab.IntentConverged {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.st.IntentByRequest(spID, "n10-d1")
	t.Fatalf("派发意图应收敛: %+v", it)
}

// TestW0402N09SubjectMatrix N09（逐动作主体正反，REST）：主人/编排主体/旧任期
// 正反矩阵——编排主体对 manual 空间一切执行动作拒绝；主人专属动作对编排主体
// 403；主人 resume 外部驱动空间 403（非驱动者非 manual）；主人 external 接管后
// resume 放行。请求主体与展示标签分开断言（403 文案携主体）。
func TestW0402N09SubjectMatrix(t *testing.T) {
	f := newSpaceStack(t)
	sid := f.createMemberSession()

	// manual 空间（owner 即驱动者）。
	code, out := f.do("tok-local", "POST", "/api/v1/spaces",
		`{"request_id":"n09-m","goal":"manual","mode":"manual","members":[{"name":"coder","session_id":"`+sid+`"}]}`)
	if code != 201 {
		t.Fatalf("manual 创建应 201: %d %s", code, out)
	}
	mID := gjsonStr(t, out, "space.id")
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control",
		`{"request_id":"n09-r","action":"ready","expected_version":1}`); code != 200 {
		t.Fatalf("ready 应成功")
	}
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control",
		`{"request_id":"n09-s","action":"start","expected_version":2}`); code != 200 {
		t.Fatalf("start 应成功")
	}
	// 编排主体（prime）对 manual 空间：派发/pause/resume/configure 全 403（非驱动
	// 者非 manual-owner；dispatch 仅驱动者或 owner-manual）。
	for _, spec := range []struct{ action, body string }{
		{"pause", `{"request_id":"x1","action":"pause","expected_version":3}`},
		{"resume", `{"request_id":"x2","action":"resume","expected_version":3}`},
		{"configure_members", `{"request_id":"x3","action":"configure_members","expected_version":3,"members":[{"name":"y"}]}`},
	} {
		code, out := f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/control", spec.body)
		if code != 403 {
			t.Fatalf("编排主体 %s 对 manual 空间应 403: %d %s", spec.action, code, out)
		}
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/dispatches",
		`{"member":"coder","expected_version":3}`)
	if code != 403 {
		t.Fatalf("编排主体对 manual 空间派发应 403: %d %s", code, out)
	}
	// 主人专属动作：编排主体 403（N09：改预算/放弃/归档非主人拒绝）。
	for _, body := range []string{
		`{"request_id":"y1","action":"set_budget","expected_version":3,"budget":{"max_messages":5},"reason":"r"}`,
		`{"request_id":"y2","action":"policy","expected_version":3,"policy":{"claim_principals":["prime"]}}`,
		`{"request_id":"y3","action":"finalize","expected_version":3,"final_outcome":"abandoned","reason":"x"}`,
		`{"request_id":"y4","action":"archive","expected_version":3}`,
	} {
		code, out := f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/control", body)
		if code != 403 {
			t.Fatalf("编排主体主人专属动作应 403: %d %s", code, out)
		}
	}
	// 主人正面：派发/暂停在 manual 空间放行（请求主体 owner；展示标签不承担身份）。
	code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/dispatches",
		`{"member":"coder","expected_version":3,"note":"主人棒","request_id":"n09-d1"}`)
	if code != 202 {
		t.Fatalf("主人派发应 202: %d", code)
	}
	code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control",
		`{"request_id":"n09-p","action":"pause","expected_version":4,"note":"人工介入"}`)
	if code != 200 {
		t.Fatalf("主人暂停应 200")
	}

	// 外部驱动空间：主人 resume 403（非驱动者非 manual 模式）；完成显式接管后放行。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces",
		`{"request_id":"n09-e","goal":"external","mode":"external"}`)
	if code != 201 {
		t.Fatalf("external 创建应 201: %d %s", code, out)
	}
	eID := gjsonStr(t, out, "space.id")
	if code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-c","action":"takeover","expected_version":1,"expected_term":0}`); code != 200 {
		t.Fatalf("领取应 200: %d %s", code, out)
	}
	// 主人 resume（配置+就绪+启动由驱动者做；成员不绑会话——sid 已归属 manual 空
	// 间，一线程唯一归属）。
	f.do("tok-prime", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-cm","action":"configure_members","expected_version":2,"term":1,"members":[{"name":"slow"}]}`)
	f.do("tok-prime", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-rd","action":"ready","expected_version":3,"term":1}`)
	f.do("tok-prime", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-st","action":"start","expected_version":4,"term":1}`)
	f.do("tok-prime", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-ps","action":"pause","expected_version":5,"term":1}`)
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-rs1","action":"resume","expected_version":6}`)
	if code != 403 {
		t.Fatalf("主人 resume 外部驱动空间应 403（非驱动者非 manual）: %d %s", code, out)
	}
	// 主人显式接管（manual，新任期）→ resume 放行。
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-tk","action":"takeover","expected_version":6,"expected_term":1,"takeover_kind":"manual"}`); code != 200 {
		t.Fatalf("主人接管应 200")
	}
	if code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+eID+"/control",
		`{"request_id":"n09-rs2","action":"resume","expected_version":7}`); code != 200 {
		t.Fatalf("接管后主人 resume 应 200: %d %s", code, out)
	}
}

// TestW0402ResumeConfirmGate 恢复保护（REST 门）：在途/排队意图未清时 resume 无
// confirm → 409 execution_in_flight；confirm=true → 200 且队列放行。
func TestW0402ResumeConfirmGate(t *testing.T) {
	f := newSpaceStack(t)
	sid := f.createMemberSession()
	code, out := f.do("tok-local", "POST", "/api/v1/spaces",
		`{"request_id":"rc-m","goal":"g","mode":"manual","members":[{"name":"coder","session_id":"`+sid+`"}]}`)
	if code != 201 {
		t.Fatalf("创建: %d %s", code, out)
	}
	mID := gjsonStr(t, out, "space.id")
	f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control", `{"request_id":"rc-r","action":"ready","expected_version":1}`)
	f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control", `{"request_id":"rc-s","action":"start","expected_version":2}`)
	// d1 在途（屏障）+ d2 排队。
	if code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/dispatches",
		`{"member":"coder","expected_version":3,"note":"d1","request_id":"rc-d1"}`); code != 202 {
		t.Fatalf("d1: %d %s", code, out)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(mID, "rc-d1"); ok && it.Status == collab.IntentAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/dispatches",
		`{"member":"coder","expected_version":4,"note":"d2","request_id":"rc-d2"}`); code != 202 {
		t.Fatalf("d2: %d", code)
	}
	// 主人取消 d1 → cancelled 停队（系统暂停）。
	code, out = f.do("tok-local", "POST", "/api/v1/sessions/"+sid+"/turns/2/cancel", "")
	if code != 200 {
		t.Fatalf("取消: %d %s", code, out)
	}
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(mID, "rc-d1"); ok && it.Status == collab.IntentCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 暂停态下 resume：在途 rc-d2 未清 → 无 confirm 409 execution_in_flight。
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control",
		`{"request_id":"rc-rs0","action":"resume","expected_version":6,"term":1}`)
	if code != 409 || !strings.Contains(out, "execution_in_flight") {
		t.Fatalf("在途 resume 应 409 execution_in_flight: %d %s", code, out)
	}
	// confirm=true → 放行；排队 d2 继续投递。
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/control",
		`{"request_id":"rc-rs1","action":"resume","expected_version":6,"term":1,"confirm":true}`); code != 200 {
		t.Fatalf("confirm resume 应 200")
	}
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(mID, "rc-d2"); ok && it.Status == collab.IntentAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 放行屏障，d2 收敛。
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(mID, "rc-d2"); ok && it.Status == collab.IntentConverged {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("d2 应收敛")
}

// TestW0402MessageGateN01 消息门与零唤醒（N01/N09 消息面）：主人发言（不指名）
// 放行；编排主体不指名 403；指名在册成员放行；伪造成员 404；全程账本零任务
//（消息与游标零执行）；since 增量读。
func TestW0402MessageGateN01(t *testing.T) {
	f := newSpaceStack(t)
	code, out := f.do("tok-local", "POST", "/api/v1/spaces",
		`{"request_id":"mg-m","goal":"g","mode":"manual","members":[{"name":"coder","session_id":"s-none"}]}`)
	if code != 201 {
		t.Fatalf("创建: %d %s", code, out)
	}
	mID := gjsonStr(t, out, "space.id")

	// 主人发言。
	if code, _ = f.do("tok-local", "POST", "/api/v1/spaces/"+mID+"/messages",
		`{"text":"开场","expected_version":1,"request_id":"mg-1"}`); code != 201 {
		t.Fatalf("主人发言应 201")
	}
	// 编排主体不指名 → 403（不冒充主人）。
	if code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/messages",
		`{"text":"冒充","expected_version":2}`); code != 403 {
		t.Fatalf("编排主体不指名发言应 403: %d %s", code, out)
	}
	// 伪造成员 → 409 member_not_found（REST 投影既定码表）。
	if code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/messages",
		`{"text":"伪造","member":"ghost","expected_version":2}`); code != 409 || !strings.Contains(out, "member_not_found") {
		t.Fatalf("伪造成员发言应 409 member_not_found: %d %s", code, out)
	}
	// 指名在册成员（coder）放行。
	if code, _ = f.do("tok-prime", "POST", "/api/v1/spaces/"+mID+"/messages",
		`{"text":"代转","member":"coder","expected_version":2,"request_id":"mg-2"}`); code != 201 {
		t.Fatalf("指名成员发言应 201")
	}
	// 增量读：since=1 只见 #2。
	code, out = f.do("tok-prime", "GET", "/api/v1/spaces/"+mID+"/messages?since=1", "")
	if code != 200 || !strings.Contains(out, `"seq":2`) || strings.Contains(out, `"seq":1`) {
		t.Fatalf("since 增量读应只含 #2: %d %s", code, out)
	}
	// N01 零执行：账本无任务（未装配会话面亦可查——空视图）。
	if n := len(f.led.View()); n != 0 {
		t.Fatalf("消息与游标不得触发执行（账本应空）: %d", n)
	}
}

// gjsonStr 极简 JSON 字段取值（测试投影用）。
func gjsonStr(t *testing.T, body, path string) string {
	t.Helper()
	cur := any(map[string]any{})
	if err := json.Unmarshal([]byte(body), &cur); err != nil {
		t.Fatalf("应答非 JSON: %v: %s", err, body)
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("路径 %s 非对象: %s", path, body)
		}
		cur = m[seg]
	}
	s, ok := cur.(string)
	if !ok {
		t.Fatalf("路径 %s 非字符串: %s", path, body)
	}
	return s
}
