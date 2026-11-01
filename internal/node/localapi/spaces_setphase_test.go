package localapi

// spaces_setphase_test.go set_phase 端点面拒绝态锁（W05-03 终审必修2）：REST
// control 直调 set_phase 在装配层接线（W05-04：strategy.ValidatePhaseTransition
// 预检后放行）开放前一律 501 拒绝——防接线前误开放（直通模型层只做值基校验，
// 可落表外边/旁路暂停恢复约束）。模型层语义（值基/授权/状态门槛/幂等）由
// internal/collab 单测覆盖，本文件只锁 HTTP 门与零副作用。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSetPhaseRESTRejectedUntilWired set_phase REST 拒绝态：501 + 人话指路；拒绝
// 零副作用（阶段未动）且不占 request_id（同 ID 改投 pause 照常受理）。
func TestSetPhaseRESTRejectedUntilWired(t *testing.T) {
	srv := newSpaceSrv(t, true)
	tok := "tok-local"
	// 建一个 manual 空间（owner 直连；成员在位满足 ready/start 前置）。
	code, out := spaceDo(t, srv, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","acceptance":"a","members":[{"name":"alice","role":"writer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201, got %d: %s", code, out)
	}
	var created struct {
		Space struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"space"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("建空间应答解析: %v", err)
	}
	id, ver := created.Space.ID, created.Space.Version
	// 就绪 + 显式启动（pause 前置：running 态）。
	for _, act := range []string{"ready", "start"} {
		code, out := spaceDo(t, srv, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
			`{"request_id":"sp-`+act+`","action":"`+act+`","expected_version":`+itoaVer(ver)+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200, got %d: %s", act, code, out)
		}
		ver++
	}

	// set_phase 直调（主人形态、合法词形）→ 501 显式拒绝。
	code, out = spaceDo(t, srv, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"sp-req-1","action":"set_phase","expected_version":`+itoaVer(ver)+`,"phase":"completed"}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("set_phase REST 直调应 501（接线前显式拒绝，含表外词形 completed）, got %d: %s", code, out)
	}
	if !strings.Contains(out, "set_phase_not_wired") || !strings.Contains(out, "W05-04") {
		t.Fatalf("拒绝应答须携 code 与接线指路: %s", out)
	}

	// 零副作用：详情阶段未动（未初始化）、版本不动。
	code, out = spaceDo(t, srv, tok, http.MethodGet, "/api/v1/spaces/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("详情应 200: %d %s", code, out)
	}
	var detail struct {
		Space struct {
			Phase   string `json:"phase"`
			Version int64  `json:"version"`
		} `json:"space"`
	}
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("详情解析: %v", err)
	}
	if detail.Space.Phase != "" || detail.Space.Version != ver {
		t.Fatalf("拒绝须零副作用：phase=%q ver=%d（期望空/%d）", detail.Space.Phase, detail.Space.Version, ver)
	}

	// 不占 request_id：同 request_id 改投 pause 照常受理（拒绝先于事务，无回执）。
	code, out = spaceDo(t, srv, tok, http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"sp-req-1","action":"pause","expected_version":`+itoaVer(ver)+`}`)
	if code != http.StatusOK {
		t.Fatalf("同 request_id 改投 pause 应受理（set_phase 拒绝不占 ID）, got %d: %s", code, out)
	}
	if !strings.Contains(out, `"applied":true`) {
		t.Fatalf("pause 应实际变更: %s", out)
	}
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
