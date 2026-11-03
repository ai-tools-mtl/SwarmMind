package localapi

// spaces_w08_test.go W08-02 空间面 REST 契约（docs/tasks/v3/W08-recovery.md
// 「REST/MCP与页面」表 + 机械验收 AC16 诊断注记锚）：
//   - 归档过滤（默认隐藏已归档 + include_archived 显式包含 + archived_hidden 注记；
//     已归档详情/消息/记录读路径不关闭仍可回查）；
//   - 诊断投影（spaceDiag：在途/留待核对计数；dispatch 查询 task_gone 历史缺口
//     注记 / awaiting_review 留待核对注记 / binding 绑定登记态）——全部断言只读
//     投影出自既有事实（账本轮终 × collab 意图），零影子状态。
//
// 夹具复用 newSpaceStack（真内核+账本+collab+service+localapi）；needs_review 轮
// 经账本直植（AcceptOrigin+MarkFinished——与 sessionop 恢复降级同款落账形态），
// 意图经真实派发链与 collab.RecordDispatch 模型层受理。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/collab"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// w08ArchivedSpaceFixture 建「已归档 + 活跃」两空间（manual 主人面：终态定版后
// 归档 sugar；返回两空间 ID）。
func w08ArchivedSpaceFixture(t *testing.T, f *spaceStackFixture) (string, string) {
	t.Helper()
	mkSpace := func(rid, goal string) string {
		code, out := f.do("tok-local", "POST", "/api/v1/spaces",
			`{"request_id":"`+rid+`","goal":"`+goal+`","mode":"manual"}`)
		if code != http.StatusCreated {
			t.Fatalf("创建空间应 201: %d %s", code, out)
		}
		return gjsonStr(t, out, "space.id")
	}
	arch := mkSpace("w08-create-arch", "已归档目标")
	done := mkSpace("w08-create-live", "活跃目标")
	// archived：abandoned 定版（终态）→ archive sugar。
	if code, out := f.do("tok-local", "POST", "/api/v1/spaces/"+arch+"/finalize",
		`{"expected_version":1,"final_outcome":"abandoned","reason":"W08 归档过滤夹具"}`); code != http.StatusOK {
		t.Fatalf("abandoned 定版应 200: %d %s", code, out)
	}
	if code, out := f.do("tok-local", "POST", "/api/v1/spaces/"+arch+"/archive",
		`{"expected_version":2}`); code != http.StatusOK || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("归档应 200 applied: %d %s", code, out)
	}
	return arch, done
}

// TestW0802SpaceListArchiveFilter 归档过滤契约：默认隐藏（archived_hidden 注记）、
// include_archived=true 显式包含、已归档详情/消息/记录读路径不关闭（回查不摘除）。
func TestW0802SpaceListArchiveFilter(t *testing.T) {
	f := newSpaceStack(t)
	arch, live := w08ArchivedSpaceFixture(t, f)

	// 默认：归档隐藏，archived_hidden=1。
	code, out := f.do("tok-local", "GET", "/api/v1/spaces", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200: %d %s", code, out)
	}
	var lst struct {
		Spaces         []collab.Space `json:"spaces"`
		Total          int            `json:"total"`
		ArchivedHidden int            `json:"archived_hidden"`
	}
	if err := json.Unmarshal([]byte(out), &lst); err != nil {
		t.Fatalf("应答解析: %v", err)
	}
	if lst.ArchivedHidden != 1 || lst.Total != 1 || len(lst.Spaces) != 1 || lst.Spaces[0].ID != live {
		t.Fatalf("默认列表应只含活跃空间且 archived_hidden=1: hidden=%d total=%d n=%d",
			lst.ArchivedHidden, lst.Total, len(lst.Spaces))
	}
	// 显式包含：两空间齐（archived_hidden 恒 0）。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces?include_archived=true", "")
	if code != http.StatusOK {
		t.Fatalf("显式包含应 200: %d %s", code, out)
	}
	lst = struct {
		Spaces         []collab.Space `json:"spaces"`
		Total          int            `json:"total"`
		ArchivedHidden int            `json:"archived_hidden"`
	}{}
	if err := json.Unmarshal([]byte(out), &lst); err != nil {
		t.Fatalf("应答解析: %v", err)
	}
	if len(lst.Spaces) != 2 || lst.ArchivedHidden != 0 {
		t.Fatalf("include_archived=true 应含归档: n=%d hidden=%d", len(lst.Spaces), lst.ArchivedHidden)
	}
	// 已归档仍可回查：详情 200 且 archived=true；消息读路径不关闭；记录读不被
	// 归档拒绝（本夹具未装配策略引擎，记录面 503 strategy_unavailable 是装配
	// 边界而非归档闸——归档闸只会以 409 拒写面出现，读路径归档不设防）。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+arch, "")
	if code != http.StatusOK || !strings.Contains(out, `"archived":true`) {
		t.Fatalf("已归档详情应可查: %d %s", code, out)
	}
	if code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+arch+"/messages", ""); code != http.StatusOK {
		t.Fatalf("已归档消息读应可查: %d %s", code, out)
	}
	if code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+arch+"/records", ""); code == http.StatusForbidden || code == http.StatusConflict {
		t.Fatalf("已归档记录读不得被归档闸拒绝（403/409=写面权限或状态闸）: %d %s", code, out)
	}
	// 详情应答携诊断投影（零在途 → awaiting_review 省略）。
	if code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+arch, ""); code != http.StatusOK ||
		!strings.Contains(out, `"diag"`) || !strings.Contains(out, `"in_flight":0`) {
		t.Fatalf("详情应携 diag 投影: %d %s", code, out)
	}
}

// w08SeedLedgerTurn 直植账本轮条目（session+seq+空间来源标记 → 终态 result）——
// needs_review 与 completed 轮的落账形态与投递链 submitSpaceIntent 同款（Session
// 携 Space 来源标记；AcceptOrigin+MarkFinished 同 sessionop 恢复降级落账形态）。
func w08SeedLedgerTurn(t *testing.T, f *spaceStackFixture, spaceID, sid string, seq int64, res string) string {
	t.Helper()
	taskID := "t-w08diag-" + sid + "-" + ivec(seq)
	task := protocol.Task{
		ID: taskID, TraceID: "tr-w08diag", TargetNode: f.svcID(), TargetAgent: "coder",
		Input:   protocol.TaskInput{Args: []string{}, Stdin: "W08 诊断夹具轮"},
		Session: &protocol.SessionTurnRef{SessionID: sid, TurnSeq: seq, Op: "send", Space: spaceID},
	}
	task.CreatedAt = protocol.NowUTC()
	task.UpdatedAt = task.CreatedAt
	if err := f.led.AcceptOrigin(task, ledger.OriginLocal); err != nil {
		t.Fatalf("植账本条目: %v", err)
	}
	if err := f.led.MarkFinished(taskID, protocol.TaskResult{
		Status: res, ExitCode: 0, FinalAnswer: "W08 夹具轮终", FinishedAt: protocol.NowUTC(),
	}); err != nil {
		t.Fatalf("植轮终: %v", err)
	}
	return taskID
}

func ivec(n int64) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// svcID fixture 节点 ID（newStackServer 固定 nodeA）。
func (f *spaceStackFixture) svcID() string { return "nodeA" }

// w08ManualRunningSpace manual 空间全流程（创建→绑成员→ready→start），返回空间 ID
// 与版本（start 后版本 4，与 N10 external 链同款递增）。
func (f *spaceStackFixture) w08ManualRunningSpace(t *testing.T, sid string) (string, int64) {
	t.Helper()
	code, out := f.do("tok-local", "POST", "/api/v1/spaces",
		`{"request_id":"w08-create-run","goal":"W08 诊断目标","mode":"manual"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201: %d %s", code, out)
	}
	id := gjsonStr(t, out, "space.id")
	steps := []struct {
		rid    string
		action string
		ver    int64
		body   string
	}{
		{"w08-cfg", "configure_members", 1, `,"members":[{"name":"coder","session_id":"` + sid + `"}]`},
		{"w08-rdy", "ready", 2, ""},
		{"w08-str", "start", 3, ""},
	}
	for _, st := range steps {
		code, out := f.do("tok-local", "POST", "/api/v1/spaces/"+id+"/control",
			`{"request_id":"`+st.rid+`","action":"`+st.action+`","expected_version":`+ivec(st.ver)+st.body+`}`)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", st.action, code, out)
		}
	}
	return id, 4
}

// TestW0802DispatchDiagAnnotations 派发查询诊断注记三面：binding 登记态、task_gone
// 历史缺口（任务过保留期清理后轮次注记仍可查）、awaiting_review 留待核对（needs_review
// 在途不冒充运行中）；空间详情 diag.awaiting_review 同源计数。
func TestW0802DispatchDiagAnnotations(t *testing.T) {
	f := newSpaceStack(t)
	sid := f.createMemberSession()
	spID, ver := f.w08ManualRunningSpace(t, sid)

	// 真实派发（屏障挂起，意图 accepted 在途）。
	code, out := f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"coder","expected_version":`+ivec(ver)+`,"request_id":"w08-d1","note":"第一棒"}`)
	if code != http.StatusAccepted {
		t.Fatalf("派发应 202: %d %s", code, out)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "w08-d1"); ok && it.Status == collab.IntentAccepted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	it, ok := f.st.IntentByRequest(spID, "w08-d1")
	if !ok {
		t.Fatal("意图未受理")
	}

	// 在途+执行中：binding.registered=true；无 task_gone/awaiting_review。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/w08-d1", "")
	if code != http.StatusOK {
		t.Fatalf("意图查询应 200: %d %s", code, out)
	}
	if !strings.Contains(out, `"binding"`) || !strings.Contains(out, `"registered":true`) {
		t.Fatalf("在途意图应携绑定登记态: %s", out)
	}
	if strings.Contains(out, "task_gone") || strings.Contains(out, "awaiting_review") {
		t.Fatalf("健康在途不得带缺口/留待核对注记: %s", out)
	}

	// 放行屏障 → 轮终 completed 收敛 → task_gone 面：CleanupAcked 清掉已确认终态
	// 条目（保留期 48h 前移）→ 意图注记仍在（收敛事实在 collab），task_gone=true。
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := f.st.IntentByRequest(spID, "w08-d1"); ok && cur.Status == collab.IntentConverged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cur, _ := f.st.IntentByRequest(spID, "w08-d1"); cur.Status != collab.IntentConverged {
		t.Fatalf("意图应收敛: %+v", cur)
	}
	if _, err := f.led.CleanupAcked(protocol.NowUTC().Add(48*time.Hour), time.Hour); err != nil {
		t.Fatalf("清理: %v", err)
	}
	if _, ok := f.led.Get(it.TaskID); ok {
		t.Fatalf("终态条目应已被清理（夹具前提）")
	}
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/w08-d1", "")
	if code != http.StatusOK || !strings.Contains(out, `"task_gone":true`) {
		t.Fatalf("任务清理后意图查询应带 task_gone 注记: %d %s", code, out)
	}

	// awaiting_review 面：第二条意图（模型层受理——同成员下一轮号），账本直植
	// needs_review 轮终 → 意图在途+轮终不确定=留待核对（不冒充运行中）。
	nextSeq := it.TurnSeq + 1
	sp, _ := f.st.Space(spID)
	if _, _, _, err := f.st.RecordDispatch(spID, collab.DispatchRequest{
		RequestID: "w08-d2", Member: "coder", SessionID: sid, TurnSeq: nextSeq,
		Term: sp.Leader.Term, ExpectedVersion: sp.Version,
	}, collab.Subject{Class: "owner", Name: "local"}); err != nil {
		t.Fatalf("模型层受理第二意图: %v", err)
	}
	w08SeedLedgerTurn(t, f, spID, sid, nextSeq, protocol.ResultNeedsReview)
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/w08-d2", "")
	if code != http.StatusOK || !strings.Contains(out, `"awaiting_review":true`) {
		t.Fatalf("needs_review 在途意图应带 awaiting_review 注记: %d %s", code, out)
	}
	// 空间详情 diag 同源：awaiting_review=1（在途 1 条且轮终不确定）。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID, "")
	if code != http.StatusOK || !strings.Contains(out, `"awaiting_review":1`) {
		t.Fatalf("空间详情 diag 应计 awaiting_review=1: %d %s", code, out)
	}
	// 解绑后 binding.registered=false 如实（换绑/移除后的登记态投影）。在途意图
	// 先按系统收束取消（AC17 在途护栏：在途成员不可移除——先收束才可改组）。
	if _, err := f.st.ConvergeIntent(spID, "w08-d2", string(collab.IntentCancelled), "",
		"W08 夹具：在途收束后改组", collab.Subject{Name: "system", Class: collab.ClassSystem}); err != nil {
		t.Fatalf("收束在途意图: %v", err)
	}
	if _, applied, err := f.st.SystemPause(spID, "W08 夹具：暂停后改组"); err != nil || !applied {
		t.Fatalf("系统暂停: %v applied=%v", err, applied)
	}
	spV, _ := f.st.SpaceView(spID)
	if _, _, err := f.st.Control(spID, collab.ControlRequest{
		RequestID: "w08-unbind", Action: collab.ActConfigureMembers,
		ExpectedVersion: spV.Version, Term: sp.Leader.Term,
		Members: []collab.Member{{Name: "coder", Role: "writer"}},
	}, collab.Subject{Class: "owner", Name: "local"}); err != nil {
		t.Fatalf("解绑改组: %v", err)
	}
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/w08-d2", "")
	if code != http.StatusOK || !strings.Contains(out, `"registered":false`) {
		t.Fatalf("解绑后绑定登记态应如实 false: %d %s", code, out)
	}
}

// TestW0802TurnSeqReuseNoCrossClaim 轮号复用反例（W08-02 codex 必修2 overlay
// 复现形）：旧意图（pending 未投递即取消收束，TaskID 空）的轮号被新意图复用并
// 完成——旧意图查询不得挂上新轮的 completed 答复（零跨认领）；新意图（TaskID
// 在位）按 TaskID 精确取轮不受影响。
func TestW0802TurnSeqReuseNoCrossClaim(t *testing.T) {
	f := newSpaceStack(t)
	sid := f.createMemberSession()
	spID, ver := f.w08ManualRunningSpace(t, sid)

	// 新意图 reuse-b：真实派发（受理期预留 seq N）→ 放行屏障 → 轮终 completed
	// 收敛（TaskID 在位、账本轮条目在案）。
	if code, out := f.do("tok-local", "POST", "/api/v1/spaces/"+spID+"/dispatches",
		`{"member":"coder","expected_version":`+ivec(ver)+`,"request_id":"reuse-b","note":"复用占轮"}`); code != http.StatusAccepted {
		t.Fatalf("派发 reuse-b 应 202: %d %s", code, out)
	}
	var seqB int64
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "reuse-b"); ok && it.TaskID != "" {
			seqB = it.TurnSeq
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seqB == 0 {
		t.Fatal("reuse-b 未接单")
	}
	if err := os.WriteFile(filepath.Join(f.dir, "go.barrier"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := f.st.IntentByRequest(spID, "reuse-b"); ok && it.Status == collab.IntentConverged {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 旧意图 reuse-a：模型层受理（同会话同号 seqB、TaskID 空=pending）→ 立即取消
	// 收束（未投递取消形态——受理对账「无实物才取消」，本意图从未投递）。
	curSp, _ := f.st.Space(spID)
	if _, _, _, err := f.st.RecordDispatch(spID, collab.DispatchRequest{
		RequestID: "reuse-a", Member: "coder", SessionID: sid, TurnSeq: seqB,
		Term: 1, ExpectedVersion: curSp.Version,
	}, collab.Subject{Class: "owner", Name: "local"}); err != nil {
		t.Fatalf("受理 reuse-a: %v", err)
	}
	if _, err := f.st.ConvergeIntent(spID, "reuse-a", string(collab.IntentCancelled), "",
		"复用反例夹具：未投递取消收束", collab.Subject{Name: "system", Class: collab.ClassSystem}); err != nil {
		t.Fatalf("取消 reuse-a: %v", err)
	}

	// 反例断言：旧意图查询零新轮答复（无 turn/final_answer/status 注记——
	// 未投递取消不认领后到轮）。
	code, out := f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/reuse-a", "")
	if code != http.StatusOK {
		t.Fatalf("reuse-a 查询应 200: %d %s", code, out)
	}
	for _, banned := range []string{`"turn"`, "final_answer", "task_gone", "awaiting_review"} {
		if strings.Contains(out, banned) {
			t.Fatalf("已取消未投递意图不得认领后到轮（含 %s）: %s", banned, out)
		}
	}
	// 新意图查询不受影响：TaskID 精确取轮，completed 答复在位。
	code, out = f.do("tok-local", "GET", "/api/v1/spaces/"+spID+"/dispatches/reuse-b", "")
	if code != http.StatusOK || !strings.Contains(out, `"final_answer"`) {
		t.Fatalf("reuse-b 应按 TaskID 精确取轮: %d %s", code, out)
	}
}
