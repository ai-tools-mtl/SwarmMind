package localapi

// spaces_setphase_test.go set_phase REST 接线行为测试（W05-04，取代 W05-03 的
// 501 拒绝态锁）：判定在 strategy、落账在 collab 的端点面兑现——合法转换受理落账
// （引擎留痕跟随）、表外转换预检拒绝零副作用、非法词形 400 与不合法转换 409 分层
// 表述、同值幂等、版本冲突、授权门（driver 族与 resume/start 同门）、start 后循环
// 引导落账、暂停恢复约束、set_phase 后 AdvanceCycle 按新阶段出行动。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/strategy"
)

// newStrategySpaceSrv 策略面全装配 server（真 Store + 真 Engine 同实例双路注入，
// 与 cmd/node 装配同源）。无 strategy 变体沿用 newSpaceSrv（cfg.Strategy 缺省
// nil → 策略面 503 诚实降级路径由 TestSetPhaseStrategyUnavailable 锁定）。
func newStrategySpaceSrv(t *testing.T) (*httptestServer, *strategy.Engine) {
	t.Helper()
	srv, eng, _ := newStrategySpaceSrvStore(t)
	return srv, eng
}

// newStrategySpaceSrvStore 同上并暴露 collab Store（终审必修3/4 用例需绕过端点
// 预检直落模型层构造在案态：completed 相位种子、Store 层 start 的引导缺位态——
// 引导是端点层职责，Store 层 start 只落状态）。
func newStrategySpaceSrvStore(t *testing.T) (*httptestServer, *strategy.Engine, *collab.Store) {
	t.Helper()
	st, err := collab.Open(t.TempDir(), collab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng, err := strategy.NewEngine(strategy.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		NodeID:     "nodeA",
		LocalToken: "tok-local",
		PrimeToken: "tok-prime",
		McpAccess:  NewMcpAccessMemory(true),
		Collab:     st,
		Strategy:   eng,
	}
	srv := httptest.NewServer(New(cfg).Handler())
	t.Cleanup(srv.Close)
	return &httptestServer{srv}, eng, st
}

// trailView records 留痕读取视图（高水位 + note 拼接；终审建议2/必修2 断言辅助）。
type trailView struct {
	hw   int64  // 已见最大留痕 seq
	notes string // 「kind:note」逐行拼接（Contains 断言具体转换内容）
}

// readTrail 读 records（since 游标）为 trailView。
func readTrail(t *testing.T, srv *httptest.Server, tok, id string, since int64) trailView {
	t.Helper()
	code, out := spaceDo(t, srv, tok, http.MethodGet, "/api/v1/spaces/"+id+"/records?since="+itoaVer(since), "")
	if code != http.StatusOK {
		t.Fatalf("records 读应 200: %d %s", code, out)
	}
	var d struct {
		Records []struct {
			Seq  int64  `json:"seq"`
			Kind string `json:"kind"`
			Note string `json:"note"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("records 解析: %v %s", err, out)
	}
	var v trailView
	var b strings.Builder
	for _, rec := range d.Records {
		if rec.Seq > v.hw {
			v.hw = rec.Seq
		}
		b.WriteString(rec.Kind + ":" + rec.Note + "\n")
	}
	v.notes = b.String()
	return v
}

// trailHighWater records 留痕高水位。
func trailHighWater(t *testing.T, srv *httptest.Server, tok, id string) int64 {
	t.Helper()
	return readTrail(t, srv, tok, id, 0).hw
}

// trailNotesSince seq>since 的留痕 note 拼接（断言具体转换内容用）。
func trailNotesSince(t *testing.T, srv *httptest.Server, tok, id string, since int64) string {
	t.Helper()
	return readTrail(t, srv, tok, id, since).notes
}

// httptestServer 薄包装（spaceDo 收 *httptest.Server；策略面测试共用取引用形态）。
type httptestServer struct {
	*httptest.Server
}

// TestSetPhaseRESTWiredLifecycle 接线全生命周期：start 引导 drafting → 表内边受理
// 落账（引擎留痕/记录可查）→ 表外边预检 409 零副作用 → 同值幂等 applied=false →
// 版本冲突 409 → 非法词形 400 → 授权门 403 → AdvanceCycle 按新阶段出行动。
func TestSetPhaseRESTWiredLifecycle(t *testing.T) {
	srv, eng := newStrategySpaceSrv(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","acceptance":"验收：全绿","members":[{"name":"alice","role":"writer"},{"name":"bob","role":"reviewer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201, got %d: %s", code, out)
	}
	id, ver := gjsonStr2(t, out, "space.id"), int64(1)
	// ready → start（start 实际生效触发循环引导：drafting 落账）。
	for _, act := range []string{"ready", "start"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"lc-`+act+`","action":"`+act+`","expected_version":`+itoaVer(ver)+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200, got %d: %s", act, code, out)
		}
		ver++
	}
	// start 后阶段引导已落账：drafting 在位（引导走 ActSetPhase 真实落账——占一版）。
	phase, version := spacePhaseVersion(t, srv.Server, tok, id)
	if phase != "drafting" || version != ver+1 {
		t.Fatalf("start 后应引导 drafting：phase=%q ver=%d（期望 drafting/%d）", phase, version, ver+1)
	}
	ver++

	// 表内边 drafting→frozen：受理落账（200 applied）+ 引擎留痕（records 可查）。
	hwBefore := trailHighWater(t, srv.Server, tok, id)
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-ph1","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"frozen"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("表内边 set_phase 应受理落账, got %d: %s", code, out)
	}
	ver++
	// 引擎留痕跟随落账（终审建议2：Seq 增量 + 具体转换内容——仅断言「存在
	// phase_changed」会被 start 引导留痕假绿，删落账 SetPhase 仍绿）。
	if hw := trailHighWater(t, srv.Server, tok, id); hw <= hwBefore {
		t.Fatalf("落账后引擎留痕应新增（Seq 增量 %d → %d）", hwBefore, hw)
	}
	if notes := trailNotesSince(t, srv.Server, tok, id, hwBefore); !strings.Contains(notes, "phase_changed:drafting → frozen") {
		t.Fatalf("落账应留痕具体转换 drafting → frozen: %q", notes)
	}

	// 表外边 frozen→rereview：预检 409 零副作用（阶段/版本不动，不占 request_id）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-bad","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"rereview"}`)
	if code != http.StatusConflict || !strings.Contains(out, "illegal_transition") {
		t.Fatalf("表外边应 409 illegal_transition, got %d: %s", code, out)
	}
	if !strings.Contains(out, "表外") {
		t.Fatalf("拒绝人话应点明表外路径: %s", out)
	}
	// completed 终态直写门（终审必修3）：frozen→completed 虽同为表外，先行命中
	// completed 专用人话（终态只能经完成动作落账——表内 →completed 边仅供完成链，
	// REST 直写一律预检拦）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-comp","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"completed"}`)
	if code != http.StatusConflict || !strings.Contains(out, "完成动作") {
		t.Fatalf("frozen 直写 completed 应 409 completed 门人话: %d %s", code, out)
	}
	// 同 request_id 改投他动作照常受理（预检拒绝先于事务，不占 ID）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-bad","action":"pause","expected_version":`+itoaVer(ver)+`}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("同 request_id 改投 pause 应受理, got %d: %s", code, out)
	}
	ver++
	// 暂停态冻结阶段保持：paused ∈ 运行面可落状态；恢复后继续。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-res","action":"resume","expected_version":`+itoaVer(ver)+`}`)
	if code != http.StatusOK {
		t.Fatalf("resume 应 200, got %d: %s", code, out)
	}
	ver++

	// 同值幂等：frozen→frozen 直通模型层零变更（applied=false、版本不动）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-same","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"frozen"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":false`) {
		t.Fatalf("同值 set_phase 应幂等零变更, got %d: %s", code, out)
	}
	_, version = spacePhaseVersion(t, srv.Server, tok, id)
	if version != ver {
		t.Fatalf("同值幂等不得递增版本: ver=%d 期望 %d", version, ver)
	}

	// 版本冲突：陈旧 expected_version → 409 version_conflict（预检通过后模型层 CAS）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-vc","action":"set_phase","expected_version":1,"phase":"review"}`)
	if code != http.StatusConflict || !strings.Contains(out, "version_conflict") {
		t.Fatalf("版本冲突应 409 version_conflict, got %d: %s", code, out)
	}

	// 非法词形与不合法转换分层表述：未知词/空值 400 invalid_request。
	for _, bad := range []string{"COMPLETED", "", "drafting;"} {
		body := `{"request_id":"lc-w","action":"set_phase","expected_version":` + itoaVer(ver) + `,"phase":"` + bad + `"}`
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control", body)
		if code != http.StatusBadRequest || !strings.Contains(out, "invalid_request") {
			t.Fatalf("非法词形 %q 应 400 invalid_request, got %d: %s", bad, code, out)
		}
	}

	// 授权门：编排主体对 manual 空间 set_phase → 403 forbidden（driver 族同门——
	// 预检在授权之前不落任何账）。
	code, out = spaceDo(t, srv.Server, "tok-prime", http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-auth","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"review"}`)
	if code != http.StatusForbidden || !strings.Contains(out, "forbidden") {
		t.Fatalf("编排主体 set_phase 应 403 forbidden, got %d: %s", code, out)
	}

	// 推进到 review（表内边 frozen→review）→ AdvanceCycle 按新阶段出行动
	//（review 阶段：审查角色 bob 应执行 review）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-ph2","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"review"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("frozen→review 应受理, got %d: %s", code, out)
	}
	view, ok := spaceViewOf(t, srv.Server, tok, id)
	if !ok {
		t.Fatal("读档失败")
	}
	dir := eng.AdvanceCycle(view, time.Now().UTC())
	if dir.NextActor != "bob" || dir.ActorAction != strategy.ActorReview {
		t.Fatalf("review 阶段 AdvanceCycle 应产出 bob/review: %+v", dir)
	}
}

// TestSetPhasePausedRestoreConstraint 暂停恢复约束（REST 面）：drafting→paused 落账
// 后，恢复到错误目标 409（人话点名暂停前阶段），恢复到 PausedFrom 受理——引擎
// PausedFrom 经投影可见。
func TestSetPhasePausedRestoreConstraint(t *testing.T) {
	srv, _ := newStrategySpaceSrv(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id, ver := gjsonStr2(t, out, "space.id"), int64(1)
	for _, act := range []string{"ready", "start"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"pr-`+act+`","action":"`+act+`","expected_version":`+itoaVer(ver)+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", act, code, out)
		}
		ver++
	}
	ver++ // start 后循环引导 drafting 落账（ActSetPhase 真实事务，占一版）
	// drafting→paused（表内边）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"pr-pp","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"paused"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("drafting→paused 应受理, got %d: %s", code, out)
	}
	ver++
	// PausedFrom 经策略投影可见（恢复目标的展示源）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"paused_from":"drafting"`) {
		t.Fatalf("投影应携暂停前阶段 drafting: %d %s", code, out)
	}
	// 恢复到错误目标（paused→review ≠ PausedFrom）：预检 409 人话点名暂停前阶段。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"pr-wrong","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"review"}`)
	if code != http.StatusConflict || !strings.Contains(out, "drafting") {
		t.Fatalf("暂停恢复错目标应 409 且点名暂停前阶段: %d %s", code, out)
	}
	// 恢复到 PausedFrom：受理。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"pr-back","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"drafting"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("暂停恢复回 PausedFrom 应受理, got %d: %s", code, out)
	}
}

// TestSetPhaseUninitializedAndUnavailable 补充拒绝面：未初始化阶段 409（指向 start
// 引导）；策略引擎未装配 503 诚实降级（set_phase/strategy/records 四端点同守卫）。
func TestSetPhaseUninitializedAndUnavailable(t *testing.T) {
	srv, _ := newStrategySpaceSrv(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id := gjsonStr2(t, out, "space.id")
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"ui-1","action":"set_phase","expected_version":1,"phase":"frozen"}`)
	if code != http.StatusConflict || !strings.Contains(out, "未初始化") {
		t.Fatalf("未初始化 set_phase 应 409 且指路 start 引导: %d %s", code, out)
	}

	// 引擎未装配：503 strategy_unavailable（策略面四端点一致；零副作用）。
	srv2 := newSpaceSrv(t, true)
	code, out = spaceDo(t, srv2, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id2 := gjsonStr2(t, out, "space.id")
	for _, spec := range [][3]string{
		{http.MethodPost, "/api/v1/spaces/" + id2 + "/control", `{"request_id":"u-1","action":"set_phase","expected_version":1,"phase":"frozen"}`},
		{http.MethodGet, "/api/v1/spaces/" + id2 + "/strategy", ""},
		{http.MethodPost, "/api/v1/spaces/" + id2 + "/records", `{"kind":"candidate","member":"alice","body":"FINAL: x"}`},
		{http.MethodGet, "/api/v1/spaces/" + id2 + "/records", ""},
	} {
		code, out = spaceDo(t, srv2, tok, spec[0], spec[1], spec[2])
		if code != http.StatusServiceUnavailable || !strings.Contains(out, "strategy_unavailable") {
			t.Fatalf("%s %s 引擎未装配应 503, got %d: %s", spec[0], spec[1], code, out)
		}
	}
}

// TestSetPhaseReplayServedIdentical 重放两洞回归（终审必修2，codex 反例锚）：
//  ① drafting→frozen(A)→review 后重放 A（原文原 ID 原版本）：返回原回执 200 dup
//     非 409——预检裁决的是新转换，已服同文重放经 ServedIdentical 短路不被新状态
//     卡死（回执先于预检）；
//  ② dup 命中引擎零回放：重放不追加留痕（Seq 高水位不变）；review→rework(B)→
//     paused 后重放 B：PausedFrom 不被清（投影 paused_from=rework 保持），引擎侧
//     无 paused→rework 回放留痕，恢复 rework 仍合法受理。
func TestSetPhaseReplayServedIdentical(t *testing.T) {
	srv, _ := newStrategySpaceSrv(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"},{"name":"bob","role":"reviewer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id, ver := gjsonStr2(t, out, "space.id"), int64(1)
	for _, act := range []string{"ready", "start"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"rp-`+act+`","action":"`+act+`","expected_version":`+itoaVer(ver)+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", act, code, out)
		}
		ver++
	}
	ver++ // start 后循环引导落账占一版（现版本 4）
	setPhase := func(reqID string, v int64, phase string) (int, string) {
		return spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"` + reqID + `","action":"set_phase","expected_version":` + itoaVer(v) + `,"phase":"` + phase + `"}`)
	}

	// ① drafting→frozen（A）→ frozen→review（阶段推进后重放 A）。
	if code, out = setPhase("rp-a", ver, "frozen"); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("drafting→frozen 应受理: %d %s", code, out)
	}
	ver++
	if code, out = setPhase("rp-a2", ver, "review"); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("frozen→review 应受理: %d %s", code, out)
	}
	ver++
	hw := trailHighWater(t, srv.Server, tok, id)
	code, out = setPhase("rp-a", 4, "frozen") // 原文重放（首次 expected_version=4）
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("已服同文重放应 200 dup 命中原回执（非 409）: %d %s", code, out)
	}
	if _, cur := spacePhaseVersion(t, srv.Server, tok, id); cur != ver {
		t.Fatalf("重放零副作用（版本不动 %d）: %d", ver, cur)
	}
	if hw2 := trailHighWater(t, srv.Server, tok, id); hw2 != hw {
		t.Fatalf("重放不得回放引擎留痕（高水位 %d 不变）: %d", hw, hw2)
	}
	if notes := trailNotesSince(t, srv.Server, tok, id, 0); strings.Contains(notes, "review → frozen") {
		t.Fatalf("重放不得产生 review → frozen 引擎留痕: %q", notes)
	}

	// ② review→rework（B）→ rework→paused（PausedFrom=rework 在案）。
	if code, out = setPhase("rp-b", ver, "rework"); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("review→rework 应受理: %d %s", code, out)
	}
	ver++
	if code, out = setPhase("rp-c", ver, "paused"); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("rework→paused 应受理: %d %s", code, out)
	}
	ver++
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"paused_from":"rework"`) {
		t.Fatalf("投影应携 paused_from=rework: %d %s", code, out)
	}
	hw = trailHighWater(t, srv.Server, tok, id)
	// 重放 B：dup 命中；PausedFrom 不被清；无 paused→rework 回放留痕。
	code, out = setPhase("rp-b", 6, "rework") // 原文重放（首次 expected_version=6）
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("重放 B 应 dup 命中: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"paused_from":"rework"`) {
		t.Fatalf("重放后 PausedFrom 不得被清（仍 rework）: %d %s", code, out)
	}
	if hw2 := trailHighWater(t, srv.Server, tok, id); hw2 != hw {
		t.Fatalf("重放 B 不得回放引擎留痕: %d → %d", hw, hw2)
	}
	if notes := trailNotesSince(t, srv.Server, tok, id, 0); strings.Contains(notes, "paused → rework") {
		t.Fatalf("重放不得产生 paused → rework 回放留痕: %q", notes)
	}
	// 恢复 rework 仍合法受理（PausedFrom 未被重放清除）。
	if code, out = setPhase("rp-d", ver, "rework"); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("paused→rework 恢复应受理: %d %s", code, out)
	}
}

// TestSetPhaseCompletedGate completed 终态直写门（终审必修3）：review 态 set_phase=
// completed 409 零副作用（终态只能经完成动作判据核验落账——直写绕过判据且阶段
// 终态不可重入会卡死空间；表内 →completed 边不动，仅端点预检面拦）；同值幂等
//（completed→completed）from==to 直通模型层 applied=false，不视为新转换。
func TestSetPhaseCompletedGate(t *testing.T) {
	srv, _, st := newStrategySpaceSrvStore(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"},{"name":"bob","role":"reviewer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id, ver := gjsonStr2(t, out, "space.id"), int64(1)
	for _, act := range []string{"ready", "start"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"cg-`+act+`","action":"`+act+`","expected_version":`+itoaVer(ver)+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", act, code, out)
		}
		ver++
	}
	ver++ // 循环引导 drafting（现版本 4）
	for _, ph := range []string{"frozen", "review"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"cg-p-`+ph+`","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"`+ph+`"}`)
		if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
			t.Fatalf("→%s 应受理: %d %s", ph, code, out)
		}
		ver++
	}
	// 直写拒绝：409 人话 + 零副作用（版本/相位不动，无 completed 留痕）。
	hw := trailHighWater(t, srv.Server, tok, id)
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"cg-x","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"completed"}`)
	if code != http.StatusConflict || !strings.Contains(out, "完成动作") {
		t.Fatalf("review 直写 completed 应 409 人话: %d %s", code, out)
	}
	if phase, cur := spacePhaseVersion(t, srv.Server, tok, id); phase != "review" || cur != ver {
		t.Fatalf("拒绝零副作用（review/%d）: %s/%d", ver, phase, cur)
	}
	if hw2 := trailHighWater(t, srv.Server, tok, id); hw2 != hw {
		t.Fatalf("拒绝零留痕: %d → %d", hw, hw2)
	}
	// 同值幂等路径：构造 completed 相位在案（模型层直落——端点预检拦的是 REST
	// 直写「新转换」，completed 在案属遗留/完成链态；Store 直调等价构造）。
	if _, dup, err := st.Control(id, collab.ControlRequest{
		RequestID: "cg-seed", Action: collab.ActSetPhase, ExpectedVersion: ver, Phase: "completed",
	}, collab.Subject{Name: "owner", Class: collab.ClassOwner}); err != nil || dup {
		t.Fatalf("种子 completed 相位应成立: err=%v dup=%v", err, dup)
	}
	ver++
	// completed→completed：同值幂等直通（不视为新转换）→ applied=false 零变更。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"cg-same","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"completed"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":false`) {
		t.Fatalf("completed→completed 同值应幂等零变更: %d %s", code, out)
	}
	if _, cur := spacePhaseVersion(t, srv.Server, tok, id); cur != ver {
		t.Fatalf("同值幂等不得递增版本: %d → %d", ver, cur)
	}
}

// TestStartReservedRequestIDAndConverge 终审必修4：①客户端 request_id 撞内部保留
// 前缀 "boot-" 显式 400（reserved_request_id——撞号会占住引导回执使引导落账
// conflict、重试永败、阶段空卡死；MCP 面同理由服务端承担）；②start 已落账而循环
// 阶段空（引导缺位——Store 层 start 只落状态不引导，等价「首次引导失败」后在案态）
// 时重放 start（dup 命中）幂等引导补课收敛 phase=drafting；收敛后再重放零变更
//（阶段合法不进引导分支，BootstrapCycle 幂等不占版）。
func TestStartReservedRequestIDAndConverge(t *testing.T) {
	srv, _, st := newStrategySpaceSrvStore(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","members":[{"name":"alice","role":"writer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id, ver := gjsonStr2(t, out, "space.id"), int64(1)
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"cv-ready","action":"ready","expected_version":1}`)
	if code != http.StatusOK {
		t.Fatalf("ready 应 200: %d %s", code, out)
	}
	ver++
	// ① 保留前缀 400 零副作用（以引导真实撞号形态：boot-<space>-drafting）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"boot-`+id+`-drafting","action":"set_phase","expected_version":2,"phase":"drafting"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "reserved_request_id") {
		t.Fatalf("boot- 前缀应 400 reserved_request_id: %d %s", code, out)
	}
	if phase, cur := spacePhaseVersion(t, srv.Server, tok, id); phase != "" || cur != ver {
		t.Fatalf("400 零副作用（空相位/版本 %d）: %s/%d", ver, phase, cur)
	}
	// ② 引导缺位构造：Store 层 start（只落状态，装配引导归端点层）→ running+空相位。
	if _, dup, err := st.Control(id, collab.ControlRequest{
		RequestID: "cv-start", Action: collab.ActStart, ExpectedVersion: ver,
	}, collab.Subject{Name: "owner", Class: collab.ClassOwner}); err != nil || dup {
		t.Fatalf("Store 层 start 应成立: err=%v dup=%v", err, dup)
	}
	// 重放 start（同 ID 同文）：dup 命中 → 收敛引导补课 → phase=drafting。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"cv-start","action":"start","expected_version":2}`)
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("start 重放应 dup 命中并触发收敛引导: %d %s", code, out)
	}
	if phase, cur := spacePhaseVersion(t, srv.Server, tok, id); phase != "drafting" {
		t.Fatalf("收敛引导后应 drafting（ver=%d）: %s/%d", ver+1, phase, cur)
	}
	_, ver = spacePhaseVersion(t, srv.Server, tok, id)
	// 收敛后再重放：阶段合法不进引导分支，零变更零占版。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"cv-start","action":"start","expected_version":2}`)
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("收敛后重放仍应 dup 命中: %d %s", code, out)
	}
	if phase, cur := spacePhaseVersion(t, srv.Server, tok, id); phase != "drafting" || cur != ver {
		t.Fatalf("收敛后重放零变更（drafting/%d）: %s/%d", ver, phase, cur)
	}
}

// spacePhaseVersion 读详情相位/版本（断言辅助）。
func spacePhaseVersion(t *testing.T, srv *httptest.Server, tok, id string) (string, int64) {
	t.Helper()
	code, out := spaceDo(t, srv, tok, http.MethodGet, "/api/v1/spaces/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("详情应 200: %d %s", code, out)
	}
	var d struct {
		Space struct {
			Phase   string `json:"phase"`
			Version int64  `json:"version"`
		} `json:"space"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("详情解析: %v", err)
	}
	return d.Space.Phase, d.Space.Version
}

// spaceViewOf 读档为 collab.SpaceView（AdvanceCycle 判定入参；REST 详情视图同形）。
func spaceViewOf(t *testing.T, srv *httptest.Server, tok, id string) (collab.SpaceView, bool) {
	t.Helper()
	code, out := spaceDo(t, srv, tok, http.MethodGet, "/api/v1/spaces/"+id, "")
	if code != http.StatusOK {
		return collab.SpaceView{}, false
	}
	var d struct {
		Space collab.SpaceView `json:"space"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("详情解析: %v", err)
	}
	return d.Space, true
}

// gjsonStr2 取顶层 path 字符串（与既有 gjsonStr 同形；本文件独立命名避免跨文件
// 依赖漂移）。
func gjsonStr2(t *testing.T, body, path string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("应答解析: %v", err)
	}
	cur := any(m)
	for _, seg := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("路径 %s 解析失败", path)
		}
		cur = mm[seg]
	}
	s, _ := cur.(string)
	return s
}

// itoaVer 测试内 int64 十进制（请求体拼装）。
func itoaVer(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [21]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
