// 四层编排测试：零扫描、派生规则、授权投影透传、UTC 时间、防泄漏（全 fake，
// 绝不调用真实 codex/zcode，绝无网络）。
package agentdetect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// leakSecret fixture 预埋假密钥（防泄漏测试金丝）。
const leakSecret = "FAKE-SECRET-swarmmind-agentdetect-leak-canary-42"

// leakContent 内容金丝：配置文件正文特征串（内容永不进结果）。
const leakContent = "leak-canary-content-line-do-not-return"

// TestDetectZeroScanAllNotChecked 零扫描（无命令/无 config_home/无投影）：
// 四层全「未检测」如实 + 时间零值（没跑过就不记时间）。
func TestDetectZeroScanAllNotChecked(t *testing.T) {
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostZcode})
	for _, tc := range []struct {
		name string
		lc   LayerCheck
	}{{"installed", rep.Installed}, {"config_recognized", rep.ConfigRecognized},
		{"execution_ready", rep.ExecutionReady}, {"call_allowed", rep.CallAllowed}} {
		if tc.lc.Status != StatusNotChecked {
			t.Fatalf("%s 零扫描应 not_checked，实得 %+v", tc.name, tc.lc)
		}
		if tc.lc.Reason == "" {
			t.Fatalf("%s 未检测必须带原因，实得 %+v", tc.name, tc.lc)
		}
		if !tc.lc.CheckedAt.IsZero() {
			t.Fatalf("%s 未检测不得记时间（零值如实），实得 %v", tc.name, tc.lc.CheckedAt)
		}
	}
	if rep.LoginMarker.Checked {
		t.Fatalf("零扫描不得核标记，实得 %+v", rep.LoginMarker)
	}
}

// TestDetectFullFlowExecutionReady 全链就绪：安装过 + 配置识别过 + 标记发现 +
// 投影允许 → ExecutionReady 亮但只亮「模型未验证」；投影逐字透传。
func TestDetectFullFlowExecutionReady(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-zcode", `echo "zcode runtime 0.17.0"`)
	home := makeZcodeHome(t, `{"authToken":"`+leakSecret+`","memo":"`+leakContent+`"}`)
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{
		Host: HostZcode, Name: "butler", Command: cmd, ConfigHome: home,
		Call: &CallProjection{Allowed: true, Source: "cfg:allowed_local", Reason: "主人登记允许本机调用"},
	})
	if rep.Installed.Status != StatusPass {
		t.Fatalf("安装层应过，实得 %+v", rep.Installed)
	}
	if !strings.Contains(rep.Installed.VersionRaw, "zcode runtime 0.17.0") {
		t.Fatalf("版本输出应原样留痕，实得 %q", rep.Installed.VersionRaw)
	}
	if rep.ConfigRecognized.Status != StatusPass {
		t.Fatalf("配置层应过，实得 %+v", rep.ConfigRecognized)
	}
	if !rep.LoginMarker.Found {
		t.Fatalf("标记应发现，实得 %+v", rep.LoginMarker)
	}
	if rep.ExecutionReady.Status != StatusPass {
		t.Fatalf("执行就绪应亮，实得 %+v", rep.ExecutionReady)
	}
	if !strings.Contains(rep.ExecutionReady.Reason, "模型未验证") {
		t.Fatalf("执行就绪必须带「模型未验证」注记，实得 %q", rep.ExecutionReady.Reason)
	}
	if rep.CallAllowed.Status != StatusPass {
		t.Fatalf("投影允许应透传为 pass，实得 %+v", rep.CallAllowed)
	}
	if rep.CallAllowed.Source != "cfg:allowed_local" || rep.CallAllowed.Reason != "主人登记允许本机调用" {
		t.Fatalf("投影 Source/Reason 必须逐字透传，实得 %+v", rep.CallAllowed)
	}
}

// TestExecutionReadyRequiresConfigHome 缺 config_home：前层未检测 → 执行就绪
// not_checked（未运行不推断下一层）。
func TestExecutionReadyRequiresConfigHome(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "v1"`)
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostCodex, Command: cmd})
	if rep.ExecutionReady.Status != StatusNotChecked {
		t.Fatalf("配置层未检测时执行就绪应 not_checked，实得 %+v", rep.ExecutionReady)
	}
	if !strings.Contains(rep.ExecutionReady.Reason, "配置识别层未检测") {
		t.Fatalf("原因应指明配置层未检测，实得 %q", rep.ExecutionReady.Reason)
	}
}

// TestExecutionReadyBlockedByInstalledFail 安装失败但配置可识别：执行就绪
// fail，原因指向安装证据（配置可识别不得推断可执行）。
func TestExecutionReadyBlockedByInstalledFail(t *testing.T) {
	missing := t.TempDir() + "/no-such-cli"
	home := makeCodexHome(t, "model = \"gpt\"\n", true) // 配置与标记齐备
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostCodex, Command: missing, ConfigHome: home})
	if rep.ConfigRecognized.Status != StatusPass {
		t.Fatalf("配置层独立应过，实得 %+v", rep.ConfigRecognized)
	}
	if rep.ExecutionReady.Status != StatusFail {
		t.Fatalf("安装失败应拖住执行就绪为 fail，实得 %+v", rep.ExecutionReady)
	}
	if !strings.Contains(rep.ExecutionReady.Reason, "安装证据未通过") {
		t.Fatalf("原因应指向安装证据，实得 %q", rep.ExecutionReady.Reason)
	}
}

// TestExecutionReadyBlockedByNoMarker 前两层齐但无登录标记：执行就绪不亮
// （结构可识别 ≠ 登录成功）。
func TestExecutionReadyBlockedByNoMarker(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-zcode", `echo "zcode 1.0"`)
	home := makeZcodeHome(t, `{"theme":"dark"}`) // 无身份键
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: HostZcode, Command: cmd, ConfigHome: home})
	if rep.Installed.Status != StatusPass || rep.ConfigRecognized.Status != StatusPass {
		t.Fatalf("前两层应过，实得 installed=%+v config=%+v", rep.Installed, rep.ConfigRecognized)
	}
	if rep.ExecutionReady.Status != StatusFail {
		t.Fatalf("无标记执行就绪应不亮，实得 %+v", rep.ExecutionReady)
	}
	if !strings.Contains(rep.ExecutionReady.Reason, "登录态标记") {
		t.Fatalf("原因应指向登录标记缺失，实得 %q", rep.ExecutionReady.Reason)
	}
}

// TestCallAllowedProjectionOnly 投影只透传不判定：拒绝投影不影响其他层；
// nil 投影如实 not_checked。
func TestCallAllowedProjectionOnly(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "v1"`)
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{
		Host: HostZcode, Command: cmd,
		Call: &CallProjection{Allowed: false, Source: "caller_x", Reason: "主人未开放本机调用"},
	})
	if rep.CallAllowed.Status != StatusFail {
		t.Fatalf("拒绝投影应透传为 fail，实得 %+v", rep.CallAllowed)
	}
	if rep.CallAllowed.Reason != "主人未开放本机调用" || rep.CallAllowed.Source != "caller_x" {
		t.Fatalf("投影必须逐字透传，实得 %+v", rep.CallAllowed)
	}
	if rep.Installed.Status != StatusPass {
		t.Fatalf("授权投影不得影响安装层判定，实得 %+v", rep.Installed)
	}
	nilRep := s.Detect(context.Background(), Request{Host: HostZcode, Command: cmd})
	if nilRep.CallAllowed.Status != StatusNotChecked || !strings.Contains(nilRep.CallAllowed.Reason, "未传入授权投影") {
		t.Fatalf("nil 投影应 not_checked 带原因，实得 %+v", nilRep.CallAllowed)
	}
}

// TestUnknownHostEndToEnd 未知宿主全链：命令可探测（宿主无关），配置层「通用
// 手动」，执行就绪不亮。
func TestUnknownHostEndToEnd(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "v1"`)
	home := t.TempDir()
	s := New(Options{})
	rep := s.Detect(context.Background(), Request{Host: Host("claude"), Command: cmd, ConfigHome: home})
	if rep.Installed.Status != StatusPass {
		t.Fatalf("命令探测宿主无关应过，实得 %+v", rep.Installed)
	}
	if rep.ConfigRecognized.Status != StatusFail || !strings.Contains(rep.ConfigRecognized.Reason, "通用手动") {
		t.Fatalf("未知宿主配置层应 fail 通用手动，实得 %+v", rep.ConfigRecognized)
	}
	if rep.ExecutionReady.Status != StatusFail {
		t.Fatalf("未知宿主执行就绪不得亮，实得 %+v", rep.ExecutionReady)
	}
}

// TestCheckedAtUTC 检查时间一律 UTC：注入本地时区时间源，落账转 UTC，
// JSON 序列化为 Z 后缀。
func TestCheckedAtUTC(t *testing.T) {
	local := time.FixedZone("TEST+8", 8*3600)
	s := New(Options{Now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, local) }})
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "v1"`)
	home := makeZcodeHome(t, `{"auth":"x"}`)
	rep := s.Detect(context.Background(), Request{Host: HostZcode, Command: cmd, ConfigHome: home,
		Call: &CallProjection{Allowed: true}})
	for _, tc := range []struct {
		name string
		lc   LayerCheck
	}{{"installed", rep.Installed}, {"config", rep.ConfigRecognized},
		{"ready", rep.ExecutionReady}, {"call", rep.CallAllowed}} {
		if tc.lc.Status == StatusNotChecked {
			continue
		}
		if tc.lc.CheckedAt.Location() != time.UTC {
			t.Fatalf("%s 落账必须 UTC，实得 %v (%s)", tc.name, tc.lc.CheckedAt, tc.lc.CheckedAt.Location())
		}
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\"checked_at\":\"2026-10-30T04:00:00Z\"") {
		t.Fatalf("序列化应为 UTC Z 后缀，实得 %s", b)
	}
}

// TestNoSecretLeakInJSON 防泄漏：结果序列化 JSON 中 grep 不到 fixture 内预埋的
// 假密钥串与正文金丝（双宿主全链对照）。
func TestNoSecretLeakInJSON(t *testing.T) {
	cmd := writeScript(t, t.TempDir(), "fake-cli", `echo "v1"`)
	zhome := makeZcodeHome(t, `{"auth":{"token":"`+leakSecret+`"},"memo":"`+leakContent+`"}`)
	chome := makeCodexHome(t, "model = \"gpt\"\n# "+leakContent+"\n", true)
	// auth.json 内也预埋密钥（本包只做存在性检查，永不读内容——最强对照）。
	writeFileWithFix(t, chome+"/.codex/auth.json", `{"api_key":"`+leakSecret+`"}`)

	s := New(Options{})
	zrep := s.Detect(context.Background(), Request{Host: HostZcode, Command: cmd, ConfigHome: zhome,
		Call: &CallProjection{Allowed: true}})
	crep := s.Detect(context.Background(), Request{Host: HostCodex, Command: cmd, ConfigHome: chome,
		Call: &CallProjection{Allowed: true}})
	b, err := json.Marshal([]Report{zrep, crep})
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, leakSecret) {
		t.Fatalf("结果 JSON 泄漏假密钥串：%s", out)
	}
	if strings.Contains(out, leakContent) {
		t.Fatalf("结果 JSON 泄漏配置文件内容：%s", out)
	}
	// 完整性对照：两宿主检测须真实跑过（结构识别 pass），证明金丝确实在场而非空跑。
	if zrep.ConfigRecognized.Status != StatusPass || crep.ConfigRecognized.Status != StatusPass {
		t.Fatalf("对照前提不成立：识别应 pass，z=%+v c=%+v", zrep.ConfigRecognized, crep.ConfigRecognized)
	}
}

// TestOptionsDefaults 零值选项回退默认（5s / 64KiB）。
func TestOptionsDefaults(t *testing.T) {
	s := New(Options{})
	if s.versionTimeout != DefaultVersionTimeout || s.outputLimit != DefaultOutputLimit {
		t.Fatalf("零值选项应取默认，实得 timeout=%v limit=%d", s.versionTimeout, s.outputLimit)
	}
	s2 := New(Options{VersionTimeout: time.Second, OutputLimit: 128})
	if s2.versionTimeout != time.Second || s2.outputLimit != 128 {
		t.Fatalf("显式选项应生效，实得 timeout=%v limit=%d", s2.versionTimeout, s2.outputLimit)
	}
}
