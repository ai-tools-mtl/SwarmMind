package sessionx

// zcode 适配器解析与命令拼装测试：样本结构对齐探针 evidence
//（docs/evidence/native-session-20261029/zcode-r1，整份终态 JSON）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TestZcodeArgvProbeForms 命令拼装逐参对照探针已验证形态（--resume 在 --json
// 之后、-p 之前）。
func TestZcodeArgvProbeForms(t *testing.T) {
	a := zcodeAdapter{command: "/usr/local/bin/zcode", mode: "edit"}
	const dir = "/tmp/probe/zcode"

	gotFirst := a.argv(true, dir, "", "首轮提示词")
	wantFirst := []string{"--cwd", dir, "--mode", "edit", "--json", "-p", "首轮提示词"}
	if strings.Join(gotFirst, "\x1f") != strings.Join(wantFirst, "\x1f") {
		t.Fatalf("首轮形态漂移:\n got %v\nwant %v", gotFirst, wantFirst)
	}

	gotResume := a.argv(false, dir, "sess_0f95c375-87da-48b6-95a8-6108e1493bcf", "续轮提示词")
	wantResume := []string{
		"--cwd", dir, "--mode", "edit", "--json",
		"--resume", "sess_0f95c375-87da-48b6-95a8-6108e1493bcf", "-p", "续轮提示词",
	}
	if strings.Join(gotResume, "\x1f") != strings.Join(wantResume, "\x1f") {
		t.Fatalf("续轮形态漂移:\n got %v\nwant %v", gotResume, wantResume)
	}
}

// zcodeR1Sample 探针 zcode-r1 的结构保真简化（sessionId/turnId/response/usage/
// projection 全保留；首轮含 Bash 无权限失败，退出码 0 不代表工作成功——探针实证）。
const zcodeR1Sample = `{
  "sessionId": "sess_0f95c375-87da-48b6-95a8-6108e1493bcf",
  "traceId": "5262b1f2-cdb6-4cdd-a07a-f4f57d3815b2",
  "turnId": "turn_47051a5c-0103-4c56-bb02-541460c0ca88",
  "response": "无法按原样完成：Bash 工具不可用（无权限客户端），我不会编造 TOOL_MARK。",
  "usage": {
    "source": "provider",
    "modelRequestCount": 3,
    "inputTokens": 59170,
    "outputTokens": 1240,
    "totalTokens": 60410,
    "cacheReadTokens": 27264,
    "cacheWriteTokens": 0,
    "reasoningTokens": 0,
    "webFetchRequests": 0,
    "webSearchRequests": 0
  },
  "eventCount": 1178,
  "projection": {
    "status": "idle",
    "turnCount": 1,
    "totalTokenCount": 60410,
    "contextUsed": 20773,
    "contextWindow": 1000000
  }
}
`

// TestZcodeParseEvidenceR1 r1 形态：sessionId/turnId/response/usage 直取；
// projection.turnCount 恒 1 不得影响任何累计口径（累计 = Session.TurnCount）。
func TestZcodeParseEvidenceR1(t *testing.T) {
	a := zcodeAdapter{}
	out, err := a.parse([]byte(zcodeR1Sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.NativeID != "sess_0f95c375-87da-48b6-95a8-6108e1493bcf" {
		t.Fatalf("NativeID: %q", out.NativeID)
	}
	if out.NativeTurnID != "turn_47051a5c-0103-4c56-bb02-541460c0ca88" {
		t.Fatalf("NativeTurnID: %q", out.NativeTurnID)
	}
	if !strings.Contains(out.Response, "无权限客户端") {
		t.Fatalf("Response: %q", out.Response)
	}
	if out.Usage != (protocol.TurnUsage{InputTokens: 59170, OutputTokens: 1240, TotalTokens: 60410}) {
		t.Fatalf("Usage: %+v", out.Usage)
	}
	if err := a.nativeTurnComplete(out); err != nil {
		t.Fatalf("整份 JSON 即终态: %v", err)
	}
}

// TestWorkResultUnverifiedOnRealEvidence R6 反例回归：真实探针 zcode-r1 样本正文
// 明说 Bash 无权限、工作未完成——平台不得借 TurnOK 冒充工作成功。整份 JSON 三路
// 成立（TurnOK 合法），但第四路必须如实记 WorkUnverified，且真实答复原样保留供
// 后续验收；禁止文本关键词猜测（内核不据正文改判状态）。
func TestWorkResultUnverifiedOnRealEvidence(t *testing.T) {
	a := zcodeAdapter{}
	out, err := a.parse([]byte(zcodeR1Sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := a.nativeTurnComplete(out); err != nil {
		t.Fatalf("三路应成立: %v", err)
	}
	// 内核侧终态：全链跑通后 Turn.WorkResult 恒 unverified（此断言在解析产物与
	// 契约常量两侧钉死，防止未来实现偷加「成功推断」）。
	if protocol.WorkUnverified != "unverified" {
		t.Fatalf("unverified 契约常量漂移: %q", protocol.WorkUnverified)
	}
	if !strings.Contains(out.Response, "无权限") || !strings.Contains(out.Response, "不会编造") {
		t.Fatalf("真实答复必须原样保留供验收: %q", out.Response)
	}
}

// TestZcodeRequiredFieldsFromEvidence R5 反例回归：从真实探针样本逐字段删减——
// sessionId/turnId/response 任一缺失、显式 null、空 sessionId/turnId 或类型不符
// 都判失败，绝不判完整成功（旧实现只查 sessionId，缺 turnId/response 也过）。
func TestZcodeRequiredFieldsFromEvidence(t *testing.T) {
	a := zcodeAdapter{}
	mutate := func(t *testing.T, f func(map[string]any)) string {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(zcodeR1Sample), &m); err != nil {
			t.Fatalf("样本解码: %v", err)
		}
		f(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing-sessionid", func(m map[string]any) { delete(m, "sessionId") }, "sessionId"},
		{"null-sessionid", func(m map[string]any) { m["sessionId"] = nil }, "sessionId"},
		{"empty-sessionid", func(m map[string]any) { m["sessionId"] = "" }, "sessionId"},
		{"type-sessionid", func(m map[string]any) { m["sessionId"] = 123 }, "类型不符"},
		{"missing-turnid", func(m map[string]any) { delete(m, "turnId") }, "turnId"},
		{"null-turnid", func(m map[string]any) { m["turnId"] = nil }, "turnId"},
		{"empty-turnid", func(m map[string]any) { m["turnId"] = "" }, "turnId"},
		{"type-turnid", func(m map[string]any) { m["turnId"] = true }, "类型不符"},
		{"missing-response", func(m map[string]any) { delete(m, "response") }, "response"},
		{"null-response", func(m map[string]any) { m["response"] = nil }, "response"},
		{"type-response", func(m map[string]any) { m["response"] = []any{"x"} }, "类型不符"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := mutate(t, tc.mutate)
			_, err := a.parse([]byte(in))
			if err == nil {
				t.Fatalf("必填字段损坏应判失败: %s", in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应提及 %q: %v", tc.want, err)
			}
		})
	}
	// usage 可选边界：缺省 → 零值 usage，轮次照常完整。
	in := mutate(t, func(m map[string]any) { delete(m, "usage") })
	out, err := a.parse([]byte(in))
	if err != nil {
		t.Fatalf("usage 缺省不得判失败: %v", err)
	}
	if out.Usage != (protocol.TurnUsage{}) || out.NativeID == "" {
		t.Fatalf("usage 缺省边界: usage=%+v native=%q", out.Usage, out.NativeID)
	}
	// response 空串 = 合法空值（在场且字符串即可；工作成败另见 WorkResult）。
	in = mutate(t, func(m map[string]any) { m["response"] = "" })
	if out, err = a.parse([]byte(in)); err != nil || out.Response != "" {
		t.Fatalf("空串 response 应为合法空值: %+v %v", out, err)
	}
}

// TestZcodeParseRejects 非 JSON / JSON 数组 / sessionId 缺失 / 前后夹杂垃圾字节。
func TestZcodeParseRejects(t *testing.T) {
	a := zcodeAdapter{}
	cases := []struct{ name, in, want string }{
		{"garbage", "not json at all", "非整份 JSON"},
		{"array", `[{"sessionId":"x"}]`, "非整份 JSON"},
		{"no-sessionid", `{"turnId":"t","response":"r"}`, "sessionId"},
		{"trailing-junk", `{"sessionId":"s"}` + "\nJUNK\n", "非整份 JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应 %q 失败, got %v", tc.want, err)
			}
		})
	}
}

// TestZcodeDefaultPermission 保守默认模式 = edit（探针唯一实测档位，绝不默认 yolo）。
func TestZcodeDefaultPermission(t *testing.T) {
	spec, err := normalizeSpec("zcode-dev", AgentSpec{
		Adapter: protocol.SessionAdapterZcode, Command: "/usr/local/bin/zcode",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.PermissionMode != zcodeDefaultPermission || zcodeDefaultPermission != "edit" {
		t.Fatalf("zcode 默认权限: %q", spec.PermissionMode)
	}
	ad, err := newAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	got := ad.argv(true, "/d", "", "p")
	if !containsAll(got, []string{"--mode", "edit"}) {
		t.Fatalf("默认模式未生效: %v", got)
	}
	if containsAll(got, []string{"yolo"}) {
		t.Fatalf("默认面出现 yolo: %v", got)
	}
}
