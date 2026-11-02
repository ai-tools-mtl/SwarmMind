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
	return &httptestServer{srv}, eng
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
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-ph1","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"frozen"}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("表内边 set_phase 应受理落账, got %d: %s", code, out)
	}
	ver++
	// 引擎留痕跟随落账：records 增量读含 phase_changed。
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/records", "")
	if code != http.StatusOK {
		t.Fatalf("records 读应 200: %d %s", code, out)
	}
	if !strings.Contains(out, `"kind":"phase_changed"`) {
		t.Fatalf("落账后留痕应含 phase_changed: %s", out)
	}

	// 表外边 frozen→completed：预检 409 零副作用（阶段/版本不动，不占 request_id）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"lc-bad","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"completed"}`)
	if code != http.StatusConflict || !strings.Contains(out, "illegal_transition") {
		t.Fatalf("表外边应 409 illegal_transition, got %d: %s", code, out)
	}
	if !strings.Contains(out, "表外") {
		t.Fatalf("拒绝人话应点明表外路径: %s", out)
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
