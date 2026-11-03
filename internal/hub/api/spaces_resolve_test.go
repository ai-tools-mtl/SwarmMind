// spaces_resolve_test.go 留待核对人工裁决 REST 契约测试（W09 工作块 B，docs/tasks/
// v3/W09-hardening.md 工作块 B）：
//   - 鉴权矩阵：未认证 401 / PAT 403 主人专属（豁免⑯同族人工裁决面）/ admin 会话
//     全通（主体=服务端解析 owner:admin——审计禁请求自报）；
//   - 错误映射：非法词形 400 invalid / 未知·pending 意图 409 illegal_transition /
//     异裁决改写 409 request_conflict（collab.HTTPStatus 单源投影）；
//   - 全链锚（真 api.Server + 真 spaces 服务 + 真 core + membus 桩应答）：dispatch
//     202 → needs_review 结果帧经真 task_result 仲裁链落终态（留待核对）→ diag
//     投影可见可裁决行 → pending 反例 409 → admin POST resolve 200
//     （dispatch.converged）→ 串行门解锁推进排队棒（accepted）——O3「无限期滞留」
//     的 REST 面机械闭环 → dup 门（重放 200+dup / 异裁决 409）→ diag 行消失。
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// waitResolveIntent 轮询意图状态至目标值（有界条件等待）。
func waitResolveIntent(t *testing.T, f *spacesRESTFixture, spaceID, rid string, want collab.IntentStatus) collab.DispatchIntent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.svc.IntentByRequest(spaceID, rid); ok && it.Status == want {
			return it
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, _ := f.svc.IntentByRequest(spaceID, rid)
	t.Fatalf("意图 %s 未达 %s（现 %s 在册=%v）", rid, want, it.Status, it.RequestID != "")
	return collab.DispatchIntent{}
}

// resolveBindAckResponder n1 端点最小应答器：space_bind 帧回 ack（载荷同文回带 +
// Accepted + 代次等值——core.SpaceBind 语义核验三字段），派发链的绑定登记受理面桩。
func resolveBindAckResponder(bus *transport.Bus) {
	bus.Endpoint("n1").Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgSpaceBind {
			return
		}
		var p protocol.SpaceBindPayload
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		pl, _ := json.Marshal(protocol.SpaceBindPayload{Space: p.Space, SessionID: p.SessionID,
			Generation: p.Generation, Accepted: true, CurrentGen: p.Generation})
		_ = bus.Endpoint("n1").Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgSpaceBindAck,
			MessageID: "ack-" + protocol.NewMessageID(), To: "hub",
			ReplyTo: env.MessageID, Payload: pl,
		})
	})
}

// injectNeedsReviewResult n1 端点注入 needs_review 终态结果帧（真 task_result
// 仲裁链：归属核验 TargetNode==From、缺省租约宽容放行、fireTerminal 终态钩子）。
func injectNeedsReviewResult(t *testing.T, f *spacesRESTFixture, taskID string) {
	t.Helper()
	pl, err := json.Marshal(protocol.TaskResultPayload{TaskID: taskID, Status: protocol.ResultNeedsReview,
		Result: &protocol.TaskResult{Status: protocol.ResultNeedsReview, ExitCode: -1,
			Error: protocol.CodeNodeRestartedMidway, FinalAnswer: "轮终不确定（REST 注入）"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.bus.Endpoint("n1").Send("hub", &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
		MessageID: "tr-" + protocol.NewMessageID(), To: "hub", Payload: pl,
	}); err != nil {
		t.Fatalf("注入 needs_review 结果帧: %v", err)
	}
}

// waitResolveTaskStatus 轮询 hub 任务状态至目标值。
func waitResolveTaskStatus(t *testing.T, f *spacesRESTFixture, taskID string, want protocol.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := f.core.GetTask(taskID); ok && got.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := f.core.GetTask(taskID)
	t.Fatalf("任务 %s 未达 %s（现 %s）", taskID, want, got.Status)
}

// resolveRunningSpace admin REST 铺设 running 手动空间 + n1/coder 绑定成员
// （bind 桩应答器在位），返回空间 ID。
func resolveRunningSpace(t *testing.T, f *spacesRESTFixture) string {
	t.Helper()
	resolveBindAckResponder(f.bus)
	card := protocol.AgentCard{Name: "coder", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "workspace-write", ProtocolVer: 1, Exposed: true,
		SessionAdapter: protocol.SessionAdapterCodex, Spaces: protocol.SpacesCapV1}
	if err := f.core.RegisterNode("n1", []protocol.AgentCard{card}); err != nil {
		t.Fatal(err)
	}
	f.core.SetOnline("n1", true)
	sv, _, err := f.core.CreateSession(core.SessionCreateRequest{NodeID: "n1", AgentName: "coder", Cwd: "/tmp/resolve"}, "admin")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	code, body := f.adminPost("/api/v1/spaces", `{"goal":"裁决契约空间","mode":"manual","request_id":"c-res-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间: %d %v", code, body)
	}
	spID := body["space"].(map[string]any)["id"].(string)
	ver := int(body["space"].(map[string]any)["version"].(float64))
	steps := []struct {
		rid    string
		action string
		body   string
	}{
		{"cfg-res-1", "configure_members", `,"members":[{"name":"n1/coder","session_id":"` + sv.ID + `"}]`},
		{"rdy-res-1", "ready", ""},
		{"start-res-1", "start", ""},
	}
	for _, st := range steps {
		code, body := f.adminPost("/api/v1/spaces/"+spID+"/control",
			`{"request_id":"`+st.rid+`","action":"`+st.action+`","expected_version":`+strconv.Itoa(ver)+st.body+`}`)
		if code != http.StatusOK {
			t.Fatalf("control %s: %d %v", st.action, code, body)
		}
		ver = int(body["result"].(map[string]any)["space"].(map[string]any)["version"].(float64))
	}
	return spID
}

// resolveDispatch admin REST 指名派发一棒（读档版本乐观锁），返回任务 ID
// （pending 态派发无任务 ID——串行门滞留形态——返回空串）。
func resolveDispatch(t *testing.T, f *spacesRESTFixture, spID, rid string) string {
	t.Helper()
	code, body := f.adminGet("/api/v1/spaces/" + spID)
	if code != http.StatusOK {
		t.Fatalf("读空间: %d %v", code, body)
	}
	ver := int(body["space"].(map[string]any)["version"].(float64))
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"n1/coder","expected_version":`+strconv.Itoa(ver)+`,"request_id":"`+rid+`","note":"REST 裁决轮"}`)
	if code != http.StatusAccepted {
		t.Fatalf("派发 %s: %d %v", rid, code, body)
	}
	taskID, _ := body["dispatch"].(map[string]any)["task_id"].(string)
	return taskID
}

// TestSpaceIntentResolveAuthAndContract 鉴权矩阵 + 错误映射（无链路形态）。
func TestSpaceIntentResolveAuthAndContract(t *testing.T) {
	f := newSpacesRESTFixture(t)
	code, body := f.adminPost("/api/v1/spaces", `{"goal":"g","mode":"manual","request_id":"c-resx-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间: %d %v", code, body)
	}
	spID := body["space"].(map[string]any)["id"].(string)
	path := "/api/v1/spaces/" + spID + "/intents/rid-x/resolve"
	// 未认证 401。
	if code, _ := f.rest("POST", path, "", "", `{"verdict":"accept"}`); code != http.StatusUnauthorized {
		t.Fatalf("未认证 resolve 应 401: %d", code)
	}
	// PAT 编排主体 403（主人专属人工裁决——豁免⑯同族；认证已过授权拒绝）。
	code, body = f.patPost(path, `{"verdict":"accept"}`)
	if code != http.StatusForbidden || body["error"] != collab.CodeForbidden {
		t.Fatalf("PAT resolve 应 403 forbidden: %d %v", code, body)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "主人专属") {
		t.Fatalf("403 应人话指路主人面: %v", body)
	}
	// admin：未知意图 409 illegal_transition。
	code, body = f.adminPost(path, `{"verdict":"accept"}`)
	if code != http.StatusConflict || body["error"] != collab.CodeIllegalTransition {
		t.Fatalf("未知意图应 409 illegal_transition: %d %v", code, body)
	}
	// admin：非法词形 400 invalid（服务侧单源校验）。
	code, body = f.adminPost(path, `{"verdict":"maybe"}`)
	if code != http.StatusBadRequest || body["error"] != "invalid" {
		t.Fatalf("非法 verdict 应 400 invalid: %d %v", code, body)
	}
}

// TestSpaceIntentResolveFullChainAndSerialGate REST 面全链锚：
// 裁决前 diag 可见可裁决行 → pending 反例 409 → accept 收束 → 串行门解锁推进
// 排队棒 → 重放 dup/异裁决 409 → diag 行消失。O3「同空间后续 pending 被无限期
// 滞留」由本测试机械闭合。
func TestSpaceIntentResolveFullChainAndSerialGate(t *testing.T) {
	f := newSpacesRESTFixture(t)
	spID := resolveRunningSpace(t, f)

	// 第一棒派发（202 受理 → accepted）→ 注入 needs_review（真 task_result 仲裁
	// 链）→ 任务落终态、意图留待核对（不收敛）。
	task1 := resolveDispatch(t, f, spID, "d-res-rest-1")
	waitResolveIntent(t, f, spID, "d-res-rest-1", collab.IntentAccepted)
	injectNeedsReviewResult(t, f, task1)
	waitResolveTaskStatus(t, f, task1, protocol.StatusNeedsReview)
	waitResolveIntent(t, f, spID, "d-res-rest-1", collab.IntentAccepted) // 留待核对

	// diag 投影：可裁决行在册（request_id/member/task_id/reason）。
	code, body := f.adminGet("/api/v1/spaces/" + spID)
	if code != http.StatusOK {
		t.Fatalf("读空间(裁前): %d %v", code, body)
	}
	diag := body["diag"].(map[string]any)
	rows := diag["awaiting_review_intents"].([]any)
	if len(rows) != 1 {
		t.Fatalf("diag 应恰一条可裁决行: %v", diag)
	}
	row := rows[0].(map[string]any)
	if row["request_id"] != "d-res-rest-1" || row["task_id"] != task1 || row["reason"] != "needs_review" || row["member"] != "n1/coder" {
		t.Fatalf("可裁决行字段不符: %v", row)
	}

	// 第二棒排入：串行门被 accepted 的第一棒占住 → pending（O3 滞留形态）。
	resolveDispatch(t, f, spID, "d-res-rest-2")
	waitResolveIntent(t, f, spID, "d-res-rest-2", collab.IntentPending)

	// pending 反例：排队待投递意图不在裁决面（409 illegal_transition）。
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/intents/d-res-rest-2/resolve", `{"verdict":"accept"}`)
	if code != http.StatusConflict || body["error"] != collab.CodeIllegalTransition {
		t.Fatalf("pending 意图 resolve 应 409 illegal_transition: %d %v", code, body)
	}

	// admin accept 裁决（主人管理会话；主体服务端解析，非请求自报）。
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/intents/d-res-rest-1/resolve",
		`{"verdict":"accept","note":"人工核对通过"}`)
	if code != http.StatusOK || body["dup"] != false {
		t.Fatalf("accept 裁决应 200 非dup: %d %v", code, body)
	}
	disp := body["dispatch"].(map[string]any)
	if disp["status"] != string(collab.IntentConverged) {
		t.Fatalf("裁决后意图应 converged: %v", disp)
	}
	if note, _ := disp["note"].(string); !strings.Contains(note, "人工核验通过（accept）") || !strings.Contains(note, "人工核对通过") {
		t.Fatalf("注记应含人工核验词与调用方 note: %v", disp)
	}

	// 核心验收（REST 面机械断言）：串行门解锁——排队棒被推进接单（accepted，
	// 任务在案——提交落库，被裁决空间的流转零滞留）。覆盖层次如实区分（W09-B
	// codex 复审视角）：本 fixture 为无执行器的最小应答器（bind ack 桩），任务
	// 停在 offered 属实——「真实执行 completed」断言由 internal/hub/spaces 服务
	// 包全链测试承担（有执行链），REST 层锁「接单+投递成立」。
	it2 := waitResolveIntent(t, f, spID, "d-res-rest-2", collab.IntentAccepted)
	if it2.TaskID == "" {
		t.Fatalf("排队棒应已接单（TaskID 在位）: %+v", it2)
	}
	if _, ok := f.core.GetTask(it2.TaskID); !ok {
		t.Fatalf("排队棒任务应已落库（投递成立）: %s", it2.TaskID)
	}

	// dup 门：同裁决重放 200+dup 原样返回；异裁决 409 request_conflict。
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/intents/d-res-rest-1/resolve", `{"verdict":"accept"}`)
	if code != http.StatusOK || body["dup"] != true {
		t.Fatalf("重放应 200+dup: %d %v", code, body)
	}
	code, body = f.adminPost("/api/v1/spaces/"+spID+"/intents/d-res-rest-1/resolve", `{"verdict":"reject"}`)
	if code != http.StatusConflict || body["error"] != collab.CodeRequestConflict {
		t.Fatalf("异裁决应 409 request_conflict: %d %v", code, body)
	}

	// diag 行随收束消失（第二棒 accepted 在途且任务非终态，不在留待核对清单）。
	code, body = f.adminGet("/api/v1/spaces/" + spID)
	if code != http.StatusOK {
		t.Fatalf("读空间(裁后): %d %v", code, body)
	}
	diag = body["diag"].(map[string]any)
	if rows, ok := diag["awaiting_review_intents"].([]any); ok && len(rows) != 0 {
		t.Fatalf("裁决后可裁决行应清空: %v", diag)
	}
}
