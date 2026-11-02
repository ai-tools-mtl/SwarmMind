package localapi

// spaces_w05_mech_test.go 外部管家驱动机械用例（W05-04 交付「外部管家驱动集成及
// 机械/真实用例」）：REST 直调本机（无 Hub）驱动讨论+写审两策略各走一条完整链。
// 真 localapi + 真 service（fake CLI 会话内核）+ 真 collab（Hooks）+ 真 strategy
// Engine 同实例装配（与 cmd/node 同源）。断言：
//   - AC12 口径：非法候选/越权登记拒绝且零派发（零意图零在途）；
//   - AC13 口径：写审证据伪造不绿（agent_self_report 永不可证实，伪造后判据仍拒）；
//   - AC17 口径：在途执行阻止 finish/archive（collab 既有守卫的 REST 机械锚）；
//   - 写审全链收口：判据满足 → finalize(done) → archive（数据写入≠执行——全链
//     零自动派发，执行只经显式 dispatch）。

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/mtl802/swarmmind/internal/strategy"
	"github.com/mtl802/swarmmind/internal/transport"
)

// mechFixture 机械用例全栈夹具（newSpaceStack 同源 + 策略引擎双路注入：collab
// Hooks 与 localapi.Strategy 同一 Engine 实例——cmd/node 装配同源）。
type mechFixture struct {
	t      *testing.T
	srv    *httptest.Server
	svc    *service.Service
	st     *collab.Store
	eng    *strategy.Engine
	led    *ledger.Ledger
	cwd    string
	dir    string
}

func newMechStack(t *testing.T) *mechFixture {
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
		"FAKE_THREAD_ID=thr-mech", "FAKE_LOG=" + runLog,
		"FAKE_WAIT_FILE=" + barrier, "FAKE_ON_RESUME=1"}
	name := "fakecli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cliPath := filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", cliPath, "../sessionx/testdata/fakecli").CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecli: %v\n%s", err, out)
	}
	mgr, err := sessionx.NewManager(filepath.Join(dir, "kernel"), map[string]sessionx.AgentSpec{
		"coder": {Adapter: protocol.SessionAdapterCodex, Command: cliPath,
			PermissionMode: "workspace-write", TimeoutSec: 30, Env: env},
	}, sessionx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := sessionop.New(filepath.Join(dir, "ops"), led, sessionop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	stDir := filepath.Join(dir, "collab")
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := collab.Open(stDir, collab.Options{Hooks: eng})
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
	srv := httptest.NewServer(New(Config{
		NodeID:     "nodeA",
		Mode:       ModeStandalone,
		Service:    svc,
		Ledger:     led,
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  NewMcpAccessMemory(true),
		Collab:     st,
		Strategy:   eng,
	}).Handler())
	t.Cleanup(srv.Close)
	return &mechFixture{t: t, srv: srv, svc: svc, st: st, eng: eng, led: led, cwd: cwd, dir: dir}
}

func (f *mechFixture) do(token, method, path, body string) (int, string) {
	f.t.Helper()
	return spaceDo(f.t, f.srv, token, method, path, body)
}

// mechMemberSession 建已发布成员会话（首轮快跑，屏障未放行即返）。
func (f *mechFixture) mechMemberSession() string {
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
		for _, e := range f.led.View() {
			if sr := e.Task.Session; sr != nil && sr.SessionID == ack.Turn.SessionID && sr.TurnSeq == 1 && e.State == "finished" {
				return ack.Turn.SessionID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("首轮未完成")
	return ""
}

// mechAwaitInFlight 等意图进入 accepted（串行投递完成）。
func (f *mechFixture) mechAwaitInFlight(spaceID, requestID string) {
	f.t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spaceID, requestID); ok && it.Status == collab.IntentAccepted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("意图未进入在途: %s", requestID)
}

// sha 内容 hash fixture。
func mechHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// TestW05MechDiscussionChain 讨论策略完整链（外部管家驱动，REST 直调无 Hub）：
// external 创建 → 领取 → 配置成员 → 显式启动 → 指名派发（发言棒）→ 成员发言 →
// 候选登记（FINAL）→ AC12 越权/非法候选拒绝且零派发 → 投影如实（待定版≠交付）。
func TestW05MechDiscussionChain(t *testing.T) {
	f := newMechStack(t)
	sid := f.mechMemberSession()
	// 1. 创建 external 待领取（编排主体）→ 领取 → 配置成员（讨论组，绑定原生线程）。
	code, out := f.do("tok-prime", "POST", "/api/v1/spaces",
		`{"request_id":"mc-create","goal":"收敛实现方案","mode":"external","budget":{"max_messages":20}}`)
	if code != http.StatusCreated {
		t.Fatalf("external 创建应 201: %d %s", code, out)
	}
	spID := gjsonStr2(t, out, "space.id")
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"mc-claim","action":"takeover","expected_version":1,"expected_term":0,"term":1}`)
	if code != http.StatusOK {
		t.Fatalf("领取应成功: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"mc-cfg","action":"configure_members","expected_version":2,"term":1,"members":[{"name":"alice","role":"discussant","session_id":"`+sid+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("配置成员应成功: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"mc-ready","action":"ready","expected_version":3,"term":1}`)
	if code != http.StatusOK {
		t.Fatalf("ready 应成功: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"mc-start","action":"start","expected_version":4,"term":1}`)
	if code != http.StatusOK {
		t.Fatalf("start 应成功: %d %s", code, out)
	}

	// 2. 指名派发（发言棒；202 受理 → 串行投递 → 屏障阻塞在途）。start 引导落账
	// 后现版本 6。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"alice","expected_version":6,"term":1,"note":"请给出你的实现方案结论（FINAL: 前缀）","request_id":"mc-d1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("派发应 202: %d %s", code, out)
	}
	f.mechAwaitInFlight(spID, "mc-d1")

	// 3. AC12：在途期间越权候选登记拒绝且零派发（不新增意图不改在途）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/records",
		`{"kind":"candidate","member":"ghost","body":"FINAL: 越权候选"}`)
	if code != http.StatusForbidden || !strings.Contains(out, "proposer_not_member") {
		t.Fatalf("越权候选应 403: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/records",
		`{"kind":"candidate","member":"alice","body":"没有前缀的非法候选"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "FINAL") {
		t.Fatalf("非法候选应 400: %d %s", code, out)
	}
	if n := f.st.InFlightCount(spID); n != 1 {
		t.Fatalf("拒绝须零派发副作用（在途应保持 1）: %d", n)
	}
	// 零登记：无 candidate 族留痕（引导 phase_changed 控制留痕不算记录）。
	for _, ev := range func() []strategy.TrailEvent {
		cands, _ := f.eng.RecordsSince(spID, 0)
		return cands
	}() {
		if ev.Kind.RecordFamily() == "candidate" {
			t.Fatalf("拒绝须零候选登记: %+v", ev)
		}
	}

	// 4. 放行屏障收束发言棒 → 成员发言（编排主体代成员归属）→ 候选登记。
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "mc-d1"); ok && it.Status == collab.IntentConverged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/messages",
		`{"member":"alice","expected_version":7,"text":"FINAL: 采用方案一（按层分离证据）"}`)
	if code != http.StatusCreated {
		t.Fatalf("成员发言应 201: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 采用方案一（按层分离证据）","round":1}`)
	if code != http.StatusCreated {
		t.Fatalf("候选登记应 201: %d %s", code, out)
	}
	candID := gjsonStr2(t, out, "candidate.id")

	// 5. 投影如实：候选 pending（待定版 ≠ 已交付）；在途归零。
	code, out = f.do("tok-prime", "GET", "/api/v1/spaces/"+spID+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"status":"pending"`) {
		t.Fatalf("投影应显示候选待定版（FINAL 登记不冒充已交付）: %d %s", code, out)
	}
	if n := f.st.InFlightCount(spID); n != 0 {
		t.Fatalf("收束后在途应归零: %d", n)
	}

	// 6. 候选定版/否决主人产品面（集成者裁决修复：含候选的讨论空间完成授权面——
	// W05 硬门槛「讨论候选由主人/finalize 定版、默认等待人工」）：orchestrate 永不
	// 放行（403 人话指路，零副作用）→ 404 锚（不存在候选）→ 主人定版 200（主体
	// 服务端派生 finalized_by=owner）→ 投影 finalized 且候选缺口清零 → 否决路径 +
	// 重否决幂等 → 留痕可查。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/candidates/"+candID+"/finalize", "")
	if code != http.StatusForbidden || !strings.Contains(out, "主人人工动作") {
		t.Fatalf("orchestrate 定版应 403 人话指路主人控制台: %d %s", code, out)
	}
	if n := f.st.InFlightCount(spID); n != 0 {
		t.Fatalf("403 须零副作用（在途保持 0）: %d", n)
	}
	code, out = f.do("tok-prime", "GET", "/api/v1/spaces/"+spID+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"status":"pending"`) {
		t.Fatalf("403 后候选应仍待定版（零副作用）: %d %s", code, out)
	}
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/candidates/cand-notexist/finalize", "")
	if code != http.StatusNotFound || !strings.Contains(out, "candidate_not_found") {
		t.Fatalf("不存在候选定版应 404 candidate_not_found: %d %s", code, out)
	}
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/candidates/"+candID+"/finalize", "")
	if code != http.StatusOK || !strings.Contains(out, `"status":"finalized"`) || !strings.Contains(out, `"finalized_by":"owner"`) {
		t.Fatalf("主人定版应 200 且主体服务端派生: %d %s", code, out)
	}
	code, out = f.do("tok-prime", "GET", "/api/v1/spaces/"+spID+"/strategy", "")
	var proj struct {
		Strategy struct {
			Candidates []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"candidates"`
			Gaps []struct {
				Field string `json:"field"`
			} `json:"gaps"`
		} `json:"strategy"`
	}
	if err := json.Unmarshal([]byte(out), &proj); err != nil {
		t.Fatalf("投影解析: %v %s", err, out)
	}
	if len(proj.Strategy.Candidates) != 1 || proj.Strategy.Candidates[0].ID != candID || proj.Strategy.Candidates[0].Status != "finalized" {
		t.Fatalf("投影候选应 finalized: %s", out)
	}
	for _, g := range proj.Strategy.Gaps {
		if g.Field == "candidate" {
			t.Fatalf("定版后候选缺口应清零（CompletionGaps 第 3 条）: %s", out)
		}
	}
	// 否决路径：第二候选登记 → 主人否决 200 → 重否决幂等 200（引擎状态机原样）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 方案二（否决路径用）","round":2}`)
	if code != http.StatusCreated {
		t.Fatalf("第二候选登记应 201: %d %s", code, out)
	}
	cand2 := gjsonStr2(t, out, "candidate.id")
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/candidates/"+cand2+"/dismiss", "")
	if code != http.StatusOK || !strings.Contains(out, `"status":"dismissed"`) {
		t.Fatalf("主人否决应 200: %d %s", code, out)
	}
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/candidates/"+cand2+"/dismiss", "")
	if code != http.StatusOK || !strings.Contains(out, `"status":"dismissed"`) {
		t.Fatalf("重否决应幂等 200: %d %s", code, out)
	}
	// 留痕可查：定版/否决均落 candidate 族记录（records 增量读可见）。
	code, out = f.do("tok-prime", "GET", "/api/v1/spaces/"+spID+"/records", "")
	if code != http.StatusOK || !strings.Contains(out, `"kind":"candidate_finalized"`) || !strings.Contains(out, `"kind":"candidate_dismissed"`) {
		t.Fatalf("records 应含定版/否决留痕: %d %s", code, out)
	}
}

// TestW05MechWriteReviewChain 写审策略完整链（REST 直调无 Hub）：引导 drafting →
// 版本登记 → 阶段机推进（set_phase 预检放行）→ 问题登记 → 返工修复复审 → AC13
// 伪造证据不绿 → 四层证实 → AC17 在途阻止 finish/finalize/archive → 收束后判据
// 满足 finalize(done) → archive。全程数据写入零自动派发。
func TestW05MechWriteReviewChain(t *testing.T) {
	f := newMechStack(t)
	sid := f.mechMemberSession()
	// manual 空间（owner 即驱动者）+ verified_write_review（写审自动完成授权）。
	code, out := f.do("tok-local", "POST", "/api/v1/spaces",
		`{"request_id":"mw-create","goal":"写审交付","mode":"manual","acceptance":"go test 全绿","policy":{"finish_auth":"verified_write_review"},"members":[{"name":"alice","role":"writer","session_id":"`+sid+`"},{"name":"bob","role":"reviewer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("manual 创建应 201: %d %s", code, out)
	}
	spID := gjsonStr2(t, out, "space.id")
	ver := int64(1)
	ctrl := func(reqID, action string, extra string) (int, string) {
		code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/control",
			`{"request_id":"`+reqID+`","action":"`+action+`","expected_version":`+itoaVer(ver)+``+extra+`}`)
		if code == http.StatusOK {
			ver++
		}
		return code, out
	}
	if code, _ = ctrl("mw-ready", "ready", ""); code != 200 {
		t.Fatalf("ready 应成功")
	}
	if code, _ = ctrl("mw-start", "start", ""); code != 200 {
		t.Fatalf("start 应成功")
	}
	// start 引导后阶段=rafting（引导 ActSetPhase 真实落账，占一版）。
	if p, v := spacePhaseVersion(t, f.srv, "tok-local", spID); p != "drafting" || v != ver+1 {
		t.Fatalf("start 后应引导 drafting: phase=%s ver=%d 期望 %d", p, v, ver+1)
	}
	ver++

	v1 := mechHash("mech-v1")
	rec := func(body string) (int, string) {
		return f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/records", body)
	}
	// 版本登记（编写产出固定）。
	if code, out = rec(`{"kind":"review","op":"revision","baseline":"main","content_sha256":"` + v1 + `","author":"alice","author_round":1}`); code != http.StatusCreated {
		t.Fatalf("版本登记应 201: %d %s", code, out)
	}
	// drafting→frozen→review（表内边预检放行）。
	if code, out = ctrl("mw-p1", "set_phase", `,"phase":"frozen"}`); code != 200 {
		t.Fatalf("drafting→frozen 应受理: %d %s", code, out)
	}
	if code, out = ctrl("mw-p2", "set_phase", `,"phase":"review"}`); code != 200 {
		t.Fatalf("frozen→review 应受理: %d %s", code, out)
	}
	// 问题登记（初审锚定 v1）。
	if code, out = rec(`{"kind":"issue","reviewer":"bob","severity":"must","file":"internal/x.go","line":3,"title":"边界缺失","found_in_version":"` + v1 + `"}`); code != http.StatusCreated {
		t.Fatalf("问题登记应 201: %d %s", code, out)
	}
	// review→rework；修复声明 + 新版本 v2；rework→rereview。
	if code, out = ctrl("mw-p3", "set_phase", `,"phase":"rework"}`); code != 200 {
		t.Fatalf("review→rework 应受理: %d %s", code, out)
	}
	v2 := mechHash("mech-v2")
	if code, out = rec(`{"kind":"review","op":"revision","baseline":"main","content_sha256":"` + v2 + `","author":"alice","author_round":2}`); code != http.StatusCreated {
		t.Fatalf("v2 登记应 201: %d %s", code, out)
	}
	// 修复声明（针对问题）需要 issue ID——从投影取。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/strategy", "")
	var proj struct {
		Strategy struct {
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
		} `json:"strategy"`
	}
	if err := json.Unmarshal([]byte(out), &proj); err != nil || len(proj.Strategy.Issues) != 1 {
		t.Fatalf("投影应含 1 条问题: %s", out)
	}
	issueID := proj.Strategy.Issues[0].ID
	if code, out = rec(`{"kind":"review","op":"claim_fix","issue_id":"` + issueID + `","version_hash":"` + v2 + `","author_round":2,"note":"已修边界"}`); code != http.StatusCreated {
		t.Fatalf("修复声明应 201: %d %s", code, out)
	}
	if code, out = ctrl("mw-p4", "set_phase", `,"phase":"rereview"}`); code != 200 {
		t.Fatalf("rework→rereview 应受理: %d %s", code, out)
	}

	// AC13：伪造证据不绿——自报标证实记录面拒绝；伪造后完成判据仍拒（finalize done）。
	if code, out = rec(`{"kind":"evidence","version":"` + v2 + `","layer":"cli_exit","source":"agent_self_report","verified":true,"ref":"agent-said-so"}`); code != http.StatusBadRequest {
		t.Fatalf("自报伪造应 400: %d %s", code, out)
	}
	// AC13 越权通道（终审必修1）：编排主体伪造人工核对证据（human_confirmation+
	// verified=true）→ 403 零副作用——已证实标记是「有权人工核对」的登记，只能由
	// 主人作出（local_token=人工核对信任锚）；非自报来源标已证实同样拒（来源声明
	// 不能自证自身）。owner 登记放行由下方四层 work_accept/human_confirmation 承担
	//（登记后判据通过 finalize 200=放行实证）。
	code, out = f.do("tok-prime", "POST", "/api/v1/spaces/"+spID+"/records",
		`{"kind":"evidence","version":"` + v2 + `","layer":"work_accept","source":"human_confirmation","verified":true,"ref":"prime-said-so"}`)
	if code != http.StatusForbidden || !strings.Contains(out, "不得自证") {
		t.Fatalf("编排主体伪造已证实证据应 403: %d %s", code, out)
	}
	for _, ev := range func() []strategy.TrailEvent {
		r, _ := f.eng.RecordsSince(spID, 0)
		return r
	}() {
		if strings.Contains(ev.Note, "prime-said-so") {
			t.Fatalf("伪造 403 须零登记副作用: %+v", ev)
		}
	}
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/finalize",
		`{"expected_version":` + itoaVer(ver) + `,"final_outcome":"done"}`)
	if code != http.StatusConflict || !strings.Contains(out, "completion_not_satisfied") {
		t.Fatalf("伪造证据下 finalize(done) 应 409 判据拒: %d %s", code, out)
	}

	// 四层分离证据（回执/协议/服务端事实/人工核对）+ 复审确认（版本匹配推进）。
	for _, ev := range []string{
		`{"layer":"cli_exit","source":"executor_receipt","verified":true,"ref":"run-1"}`,
		`{"layer":"protocol_parse","source":"protocol_receipt","verified":true,"ref":"parse-1"}`,
		`{"layer":"round_complete","source":"host_observation","verified":true,"ref":"turn-1"}`,
		`{"layer":"work_accept","source":"human_confirmation","verified":true,"ref":"owner"}`,
	} {
		if code, out = rec(`{"kind":"evidence","version":"` + v2 + `",` + ev[1:]); code != http.StatusCreated {
			t.Fatalf("证据登记应 201: %d %s", code, out)
		}
	}
	if code, out = rec(`{"kind":"review","op":"rereview","issue_id":"` + issueID + `","reviewer":"bob","against_hash":"` + v2 + `","result":"confirmed_fixed"}`); code != http.StatusCreated {
		t.Fatalf("复审确认应 201: %d %s", code, out)
	}

	// AC17：在途执行阻止 finish / finalize(done) / archive（collab 既有守卫的
	// REST 机械锚）。派发受理占一版（意图落账）——读最新版本再发拒绝面断言。
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"alice","expected_version":` + itoaVer(ver) + `,"note":"顺手核查一轮","request_id":"mw-d1"}`)
	if code != http.StatusAccepted {
		t.Fatalf("派发应 202: %d %s", code, out)
	}
	f.mechAwaitInFlight(spID, "mw-d1")
	_, ver = spacePhaseVersion(t, f.srv, "tok-local", spID)
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/control",
		`{"request_id":"mw-fin","action":"finish","expected_version":` + itoaVer(ver) + `,"term":1}`)
	if code != http.StatusConflict || !strings.Contains(out, "execution_in_flight") {
		t.Fatalf("在途 finish 应 409 execution_in_flight: %d %s", code, out)
	}
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/finalize",
		`{"expected_version":` + itoaVer(ver) + `,"final_outcome":"done"}`)
	if code != http.StatusConflict || !strings.Contains(out, "execution_in_flight") {
		t.Fatalf("在途 finalize 应 409: %d %s", code, out)
	}
	// archive 在运行态被状态门前置拒绝（仅已完成/已放弃可归档）——在途护栏的
	// execution_in_flight 锚由 finish/finalize 承担（归档对象已是终态，在途不可能
	// 在位；状态门与在途护栏双层把守，此处锁状态门锚）。
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/archive",
		`{"expected_version":` + itoaVer(ver) + `}`)
	if code != http.StatusConflict || !strings.Contains(out, "illegal_transition") || !strings.Contains(out, "archive") {
		t.Fatalf("运行态 archive 应 409 illegal_transition: %d %s", code, out)
	}

	// 收束在途（执行侧事实落账——owner 主体），判据全绿 → finalize(done) → archive。
	if _, err := f.st.ConvergeIntent(spID, "mw-d1", "converged", "t-mech", "机械用例收束",
		collab.Subject{Name: "owner", Class: collab.ClassOwner}); err != nil {
		t.Fatalf("意图收敛: %v", err)
	}
	_, ver = spacePhaseVersion(t, f.srv, "tok-local", spID) // 收束事务版本推进对齐
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/finalize",
		`{"request_id":"mw-done","expected_version":` + itoaVer(ver) + `,"final_outcome":"done"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("判据满足 finalize(done) 应受理: %d %s", code, out)
	}
	ver++
	code, out = f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/archive",
		`{"request_id":"mw-arch","expected_version":` + itoaVer(ver) + `}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("archive 应受理: %d %s", code, out)
	}
	if _, ok := f.st.SpaceView(spID); !ok {
		t.Fatal("归档后空间仍应可查（列表收纳不删材料）")
	}
}
