package sessionx

// codex 适配器解析与命令拼装测试：样本结构对齐探针 evidence
//（docs/evidence/native-session-20261029/codex-r1|r2.jsonl，DoD 2 的解析准绳）。

import (
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestCodexArgvProbeForms 命令拼装逐参对照探针已验证形态（首/续轮，通用参数在
// resume 之前）。
func TestCodexArgvProbeForms(t *testing.T) {
	a := codexAdapter{command: "/usr/local/bin/codex", sandbox: "workspace-write"}
	const dir = "/tmp/probe/codex"

	gotFirst := a.argv(true, dir, "", "首轮提示词")
	wantFirst := []string{
		"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write",
		"-c", `approval_policy="never"`, "-C", dir, "首轮提示词",
	}
	if strings.Join(gotFirst, "\x1f") != strings.Join(wantFirst, "\x1f") {
		t.Fatalf("首轮形态漂移:\n got %v\nwant %v", gotFirst, wantFirst)
	}

	gotResume := a.argv(false, dir, "01a185b2-5e57-72c0-8013-643a9fe4d234", "续轮提示词")
	wantResume := []string{
		"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write",
		"-c", `approval_policy="never"`, "-C", dir,
		"resume", "01a185b2-5e57-72c0-8013-643a9fe4d234", "续轮提示词",
	}
	if strings.Join(gotResume, "\x1f") != strings.Join(wantResume, "\x1f") {
		t.Fatalf("续轮形态漂移:\n got %v\nwant %v", gotResume, wantResume)
	}
}

// codexR1Sample 探针 codex-r1.jsonl 的结构保真简化（事件种类与字段全保留）。
const codexR1Sample = `{"type":"thread.started","thread_id":"01a185b2-5e57-72c0-8013-643a9fe4d234"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"我会执行本机命令并创建指定文件。"}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"python3 -c print","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"python3 -c print","aggregated_output":"TOOL_MARK=fc0af73f2aeabcb3\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"printf stage=1 > state.txt","aggregated_output":"","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"ROUND1_READY"}}
{"type":"turn.completed","usage":{"input_tokens":42082,"cached_input_tokens":34048,"cache_write_input_tokens":0,"output_tokens":139,"reasoning_output_tokens":0}}
`

// TestCodexParseEvidenceR1 r1 形态：thread id 取回、agent_message 提取
// （command_execution 不混入答复）、usage 派生总量、完成判据成立。
func TestCodexParseEvidenceR1(t *testing.T) {
	a := codexAdapter{}
	out, err := a.parse([]byte(codexR1Sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.NativeID != "01a185b2-5e57-72c0-8013-643a9fe4d234" {
		t.Fatalf("NativeID: %q", out.NativeID)
	}
	if out.Response != "我会执行本机命令并创建指定文件。\n\nROUND1_READY" {
		t.Fatalf("Response: %q", out.Response)
	}
	if out.Usage != (protocol.TurnUsage{InputTokens: 42082, OutputTokens: 139, TotalTokens: 42221}) {
		t.Fatalf("Usage: %+v", out.Usage)
	}
	if err := a.nativeTurnComplete(out); err != nil {
		t.Fatalf("完成判据: %v", err)
	}
}

// TestCodexParseEvidenceR2 r2 形态：一轮内两条 agent_message 按序拼接（探针 r2
// 实证），turn.completed usage 覆盖。
func TestCodexParseEvidenceR2(t *testing.T) {
	sample := `{"type":"thread.started","thread_id":"01a185b2-5e57-72c0-8013-643a9fe4d234"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"凭原会话历史回忆暗号。"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"python3","aggregated_output":"BEFORE=stage=1\nAFTER=stage=2\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"{\"回忆内容\": {\"暗号\": \"5aea472e3dcdbac8\"}}"}}
{"type":"turn.completed","usage":{"input_tokens":84903,"cached_input_tokens":76288,"cache_write_input_tokens":0,"output_tokens":437,"reasoning_output_tokens":0}}
`
	a := codexAdapter{}
	out, err := a.parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Response != "凭原会话历史回忆暗号。\n\n{\"回忆内容\": {\"暗号\": \"5aea472e3dcdbac8\"}}" {
		t.Fatalf("两条 agent_message 拼接: %q", out.Response)
	}
	if out.Usage.TotalTokens != 84903+437 {
		t.Fatalf("r2 usage 派生: %+v", out.Usage)
	}
}

// TestCodexParseRejects 错误事件 / 非 JSON 行 / 缺 turn.completed / 缺 thread id
// 的解析面行为。
func TestCodexParseRejects(t *testing.T) {
	a := codexAdapter{}

	out, err := a.parse([]byte(`{"type":"error","message":"no thread found"}`))
	if err == nil || !strings.Contains(err.Error(), "cli_error_event") || !strings.Contains(err.Error(), "no thread found") {
		t.Fatalf("错误事件应显式失败: %v", err)
	}
	if out.nativeTurnComplete {
		t.Fatalf("错误事件后不得置完成位")
	}

	if _, err := a.parse([]byte("this line is not json\n")); err == nil ||
		!strings.Contains(err.Error(), "非 JSON") {
		t.Fatalf("非 JSON 行应失败: %v", err)
	}

	// exit 0 但事件流截断（无 turn.completed）：解析成功、完成判据不成立。
	out, err = a.parse([]byte(`{"type":"thread.started","thread_id":"tid"}` + "\n" + `{"type":"turn.started"}` + "\n"))
	if err != nil {
		t.Fatalf("截断流解析应成功: %v", err)
	}
	if err := a.nativeTurnComplete(out); err == nil || !strings.Contains(err.Error(), "turn_incomplete") {
		t.Fatalf("完成判据应不成立: %v", err)
	}

	// thread.started 空 id：NativeID 为空 → 由内核判 native_id_missing。
	out, err = a.parse([]byte(`{"type":"thread.started","thread_id":""}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if out.NativeID != "" {
		t.Fatalf("空 thread_id 应得空 NativeID: %q", out.NativeID)
	}
}

// TestCodexKnownEventPayloadCorruptionRejected S2 反例回归：已知事件
// （item.started / item.completed）的 item 载荷缺失、null、非对象、缺 type，以及
// agent_message 缺 text / text=null，必须显式报错（旧实现静默吞掉或零值化放行，
// 可能掩盖结构损坏冒充完整轮次）；未知扩展事件与未知合法 item 类型仍兼容放行。
func TestCodexKnownEventPayloadCorruptionRejected(t *testing.T) {
	a := codexAdapter{}
	for _, tc := range []struct{ name, in string }{
		// 行整体合法、item 载荷对 codexItem 类型损坏（id 数字化）——显式报错
		{"item-completed-wrongtype", `{"type":"item.completed","item":{"id":1,"type":"agent_message","text":"hi"}}`},
		{"item-completed-absent", `{"type":"item.completed","usage":{}}`},
		{"item-completed-stringitem", `{"type":"item.completed","item":"not-an-object"}`},
		{"item-started-wrongtype", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.started","item":"not-an-object"}`},
		// R2 复审 S2 反例：JSON null/空对象/缺 type 解码不报错、成零值静默放行
		{"item-completed-null", `{"type":"item.completed","item":null}`},
		{"item-completed-emptyobj", `{"type":"item.completed","item":{}}`},
		{"item-completed-onlyid", `{"type":"item.completed","item":{"id":"item_0"}}`},
		// agent_message 的 text 缺失/null 与合法空串必须可区分：前者损坏
		{"item-completed-text-null", `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":null}}`},
		{"item-completed-text-absent", `{"type":"item.completed","item":{"id":"item_0","type":"agent_message"}}`},
		{"item-started-null", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.started","item":null}`},
		{"item-started-agentmsg-text-null", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.started","item":{"id":"i","type":"agent_message","text":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), "载荷损坏") {
				t.Fatalf("已知事件载荷损坏应报错, got %v", err)
			}
		})
	}
	// 兼容边界（合法放行）：agent_message text="" 合法（本轮无正文，不进答复）；
	// 未知 item 类型（type 非空）兼容忽略；未知扩展事件忽略后 thread id/usage 照取。
	out, err := a.parse([]byte(`{"type":"thread.started","thread_id":"t"}` + "\n" +
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":""}}` + "\n" +
		`{"type":"item.completed","item":{"id":"i1","type":"web_search","query":"x"}}` + "\n" +
		`{"type":"future.event","mystery":{"nested":true}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`))
	if err != nil {
		t.Fatalf("合法空串/未知 item 类型/未知事件不得击穿解析: %v", err)
	}
	if out.Response != "" || out.NativeID != "t" || !out.nativeTurnComplete {
		t.Fatalf("兼容边界放行后字段异常: %+v", out)
	}
}

// TestCodexConflictingThreadIDRejected S2 反例回归：同一事件流出现两个不同
// thread id = 结构损坏或线程漂移，必须显式拒绝（旧实现静默保留第一个）；相同
// id 重复出现允许放行。
func TestCodexConflictingThreadIDRejected(t *testing.T) {
	a := codexAdapter{}
	conflict := `{"type":"thread.started","thread_id":"tid-a"}` + "\n" +
		`{"type":"thread.started","thread_id":"tid-b"}` + "\n" +
		`{"type":"turn.completed"}`
	if _, err := a.parse([]byte(conflict)); err == nil || !strings.Contains(err.Error(), "native_id_conflict") {
		t.Fatalf("冲突 thread id 应拒绝, got %v", err)
	}
	same := `{"type":"thread.started","thread_id":"tid-a"}` + "\n" +
		`{"type":"thread.started","thread_id":"tid-a"}` + "\n" +
		`{"type":"turn.completed"}`
	if _, err := a.parse([]byte(same)); err != nil {
		t.Fatalf("同 id 重复应放行: %v", err)
	}
}

// TestCodexDefaultPermission 保守默认沙箱 = read-only，绝不默认提权。
func TestCodexDefaultPermission(t *testing.T) {
	spec, err := normalizeSpec("codex-dev", AgentSpec{
		Adapter: protocol.SessionAdapterCodex, Command: "/usr/local/bin/codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.PermissionMode != codexDefaultPermission || codexDefaultPermission != "read-only" {
		t.Fatalf("codex 默认权限: %q", spec.PermissionMode)
	}
	ad, err := newAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	got := ad.argv(true, "/d", "", "p")
	if !containsAll(got, []string{"-s", "read-only"}) {
		t.Fatalf("默认沙箱未生效: %v", got)
	}
}

func containsAll(hay, needles []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range hay {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
