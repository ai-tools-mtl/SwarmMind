package localapi

// spaces_w04_03_test.go 主人专属四 sugar 端点测试（W04-03，任务书 REST 表「人工
// 预算/授权策略/定版/归档」行）：authLocal 门矩阵（prime_token 401——资源主权动作
// 不经编排凭据入口）、同族动作映射契约（字段落点/状态机前置如实投影）、幂等重放
// 与终态链（finalize→archive→重复归档零变更）。模型层语义（授权表/状态机/预算）
// 由 internal/collab 单测机械覆盖，本文件只锁 HTTP 面与 sugar 映射。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/collab"
)

// TestSpacesSugarAuthGate sugar 端点主人专属门：prime_token/无令牌/错令牌一律 401
// （authLocal——编排凭据不进资源主权入口；与写面 authSpaceSubject 的 403 分层不同）。
func TestSpacesSugarAuthGate(t *testing.T) {
	srv := newSpaceSrv(t, true)
	for _, tok := range []string{"", "wrong", "tok-prime"} {
		for _, path := range []string{"/budget", "/policy", "/finalize", "/archive"} {
			code, out := spaceDo(t, srv, tok, http.MethodPost, "/api/v1/spaces/sp-x"+path,
				`{"expected_version":1}`)
			if code != http.StatusUnauthorized {
				t.Fatalf("token=%q %s 应 401（主人专属独立入口）, got %d: %s", tok, path, code, out)
			}
		}
	}
	// 未装配 503 守卫先行于动作映射（spaceReady 在解码前）。
	srv2 := newSpaceSrv(t, false)
	if code, out := spaceDo(t, srv2, "tok-local", http.MethodPost, "/api/v1/spaces/sp-x/budget",
		`{"expected_version":1}`); code != http.StatusServiceUnavailable || !strings.Contains(out, "spaces_unavailable") {
		t.Fatalf("未装配 sugar 应 503 spaces_unavailable, got %d: %s", code, out)
	}
}

// TestSpacesSugarContract 同族动作映射契约：budget/policy/finalize/archive 四端点
// 字段落点、版本推进、前置校验如实投影、幂等重放与终态链。
func TestSpacesSugarContract(t *testing.T) {
	srv := newSpaceSrv(t, true)
	code, out := spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"sg-c1","goal":"交付 v3","mode":"manual","members":[{"name":"echo"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", code, out)
	}
	var created struct {
		Space collab.Space `json:"space"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Space.ID
	sugar := func(path, body string) (int, string) {
		return spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces/"+id+path, body)
	}

	// ---- expected_version 必填（400 先于任何变更）。 ----
	if code, out = sugar("/budget", `{}`); code != http.StatusBadRequest {
		t.Fatalf("缺 expected_version 应 400, got %d: %s", code, out)
	}

	// ---- budget：字段落点 + 版本推进 + 原因留档。 ----
	code, out = sugar("/budget", `{"request_id":"sg-b1","expected_version":1,"budget":{"max_messages":10,"max_dispatch_rounds":3,"max_wall_seconds":600},"reason":"控制成本"}`)
	if code != http.StatusOK || !strings.Contains(out, `"max_messages":10`) || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("budget 应 200 applied, got %d: %s", code, out)
	}
	var bres struct {
		Result collab.ControlResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &bres); err != nil {
		t.Fatal(err)
	}
	if bres.Result.Space.Budget.MaxMessages != 10 || bres.Result.Space.Budget.MaxDispatchRounds != 3 ||
		bres.Result.Space.Version != 2 {
		t.Fatalf("budget 落点不符: %+v", bres.Result.Space)
	}
	// 负值预算 → 400（模型层 invalid_request 零变更）。
	if code, _ = sugar("/budget", `{"expected_version":2,"budget":{"max_messages":-1}}`); code != http.StatusBadRequest {
		t.Fatalf("负值预算应 400, got %d", code)
	}
	// 幂等重放：同 request_id 同文 → 200 dup=true 原回执（版本不再推进）。
	code, out = sugar("/budget", `{"request_id":"sg-b1","expected_version":1,"budget":{"max_messages":10,"max_dispatch_rounds":3,"max_wall_seconds":600},"reason":"控制成本"}`)
	if code != http.StatusOK || !strings.Contains(out, `"dup":true`) {
		t.Fatalf("budget 重放应 dup=true, got %d: %s", code, out)
	}
	// 同 request_id 异文 → 409 request_conflict。
	if code, out = sugar("/budget", `{"request_id":"sg-b1","expected_version":2,"budget":{"max_messages":11}}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeRequestConflict) {
		t.Fatalf("同键异文应 409, got %d: %s", code, out)
	}

	// ---- policy：待配置态可变更；策略版本递增。 ----
	code, out = sugar("/policy", `{"request_id":"sg-p1","expected_version":2,"policy":{"finish_auth":"verified_write_review"}}`)
	if code != http.StatusOK || !strings.Contains(out, `"verified_write_review"`) {
		t.Fatalf("policy 应 200, got %d: %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &bres); err != nil {
		t.Fatal(err)
	}
	if bres.Result.Space.Policy.FinishAuth != collab.FinishAuthVerifiedWriteReview ||
		bres.Result.Space.Policy.PolicyVersion != 2 || bres.Result.Space.Version != 3 {
		t.Fatalf("policy 落点不符: %+v", bres.Result.Space.Policy)
	}
	// 非法 finish_auth → 400。
	if code, _ = sugar("/policy", `{"expected_version":3,"policy":{"finish_auth":"magic"}}`); code != http.StatusBadRequest {
		t.Fatalf("非法 finish_auth 应 400, got %d", code)
	}

	// ---- 前置校验如实投影：非待配置态 policy → 409 illegal_transition。 ----
	// （ready 走 control 面——sugar 四端点封闭，不含 ready/start 等协作动作。）
	if code, out = spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces/"+id+"/control",
		`{"request_id":"sg-r1","action":"ready","expected_version":3}`); code != http.StatusOK {
		t.Fatalf("ready 应 200, got %d: %s", code, out)
	}
	if code, out = sugar("/policy", `{"request_id":"sg-p2","expected_version":4,"policy":{"finish_auth":"human_only"}}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeIllegalTransition) {
		t.Fatalf("ready 态 policy 应 409 illegal_transition, got %d: %s", code, out)
	}

	// ---- archive：非终态拒绝。 ----
	if code, out = sugar("/archive", `{"request_id":"sg-a0","expected_version":4}`); code != http.StatusConflict || !strings.Contains(out, collab.CodeIllegalTransition) {
		t.Fatalf("非终态 archive 应 409, got %d: %s", code, out)
	}

	// ---- finalize：参数校验与放弃链。 ----
	if code, _ = sugar("/finalize", `{"expected_version":4}`); code != http.StatusBadRequest {
		t.Fatalf("缺 final_outcome 应 400, got %d", code)
	}
	if code, out = sugar("/finalize", `{"request_id":"sg-fx","expected_version":4,"final_outcome":"magic"}`); code != http.StatusBadRequest {
		t.Fatalf("非法 final_outcome 应 400, got %d: %s", code, out)
	}
	if code, _ = sugar("/finalize", `{"expected_version":4,"final_outcome":"abandoned"}`); code != http.StatusBadRequest {
		t.Fatalf("放弃缺原因应 400, got %d", code)
	}
	// done 仍过策略判据：hooks 未装配 → 503 finish_policy_unavailable（W05 接线前
	// 如实拒绝——sugar 不旁路判据）。
	if code, out = sugar("/finalize", `{"request_id":"sg-fd","expected_version":4,"final_outcome":"done"}`); code != http.StatusServiceUnavailable || !strings.Contains(out, collab.CodeFinishPolicyUnavailable) {
		t.Fatalf("finalize done 无策略应 503 finish_policy_unavailable, got %d: %s", code, out)
	}
	code, out = sugar("/finalize", `{"request_id":"sg-f1","expected_version":4,"final_outcome":"abandoned","reason":"目标取消"}`)
	if code != http.StatusOK || !strings.Contains(out, `"status":"abandoned"`) {
		t.Fatalf("放弃应 200, got %d: %s", code, out)
	}

	// ---- 终态链：archive → 幂等重放零变更。 ----
	code, out = sugar("/archive", `{"request_id":"sg-a1","expected_version":5}`)
	if code != http.StatusOK || !strings.Contains(out, `"archived":true`) {
		t.Fatalf("归档应 200 archived, got %d: %s", code, out)
	}
	code, out = sugar("/archive", `{"request_id":"sg-a2","expected_version":6}`)
	if code != http.StatusOK || !strings.Contains(out, `"applied":false`) {
		t.Fatalf("重复归档应 200 applied=false（幂等零变更）, got %d: %s", code, out)
	}
	// request_id 缺省自动生成：可成功（不承担重放回执）。
	code, out = spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces",
		`{"request_id":"sg-c2","goal":"第二个空间","mode":"manual"}`)
	if code != http.StatusCreated {
		t.Fatalf("第二空间应 201, got %d: %s", code, out)
	}
	var c2 struct {
		Space collab.Space `json:"space"`
	}
	_ = json.Unmarshal([]byte(out), &c2)
	if code, out = spaceDo(t, srv, "tok-local", http.MethodPost, "/api/v1/spaces/"+c2.Space.ID+"/budget",
		`{"expected_version":1,"budget":{"max_messages":5}}`); code != http.StatusOK {
		t.Fatalf("request_id 缺省应自动生成并 200, got %d: %s", code, out)
	}
}
