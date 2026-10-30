package localapi

// MCP 六层验证模型（W03-03）fixture 单测：V0 服务端可证检查各态（齐备/缺必填/
// 范围不一致/回环警告/宿主不支持/文件粗检）、V0 pass 绝不推断上层的投影断言、
// V2 服务端视角探测三态（注入假 HTTP 检查器——零网络，CheckedBy=server_side_view
// 恒在位，输出面禁词金丝）、外部确认登记与旧回执失效（AC10：版本不符不点亮新
// 配置，历史保留时间与来源可查）、一次性秘密生命周期（状态机/轮换失效判定/零
// 明文金丝）、六层指引文案覆盖 §12.4 失败显示列关键词。全部纯模型级：无路由、
// 无网络、无真实模型、无真实宿主（宿主配置为 t.TempDir 内 fixture，纪律同
// W03-01——临时 HOME 绝不写实际 ~/.zcode 或 ~/.codex）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// verifyFakeCred 假密钥（仅测试，非真实凭据；与 mcpconfig_test.go 的
// cfgFakeNodeCred 同纪律）：金丝断言其完整形态不得出现在任何输出面。
const verifyFakeCred = "faketest-verify-cred-DO-NOT-LEAK-abcdef0123456789"

// verifyBannedExternalClaims 输出面禁词（任务书：V2 服务端探测绝不标外部 V2
// 有效；服务端自测不能冒认外部验证成功）。任何检查/指引/板面输出命中即失败。
var verifyBannedExternalClaims = []string{
	"外部已验证", "外部验证通过", "外部验证成功", "外部 V2 有效", "已在外部验证",
}

// verifyNoExternalClaim 禁词金丝。
func verifyNoExternalClaim(t *testing.T, where, out string) {
	t.Helper()
	for _, banned := range verifyBannedExternalClaims {
		if strings.Contains(out, banned) {
			t.Fatalf("%s 不得含外部冒认措辞（禁词 %q 命中）：%s", where, banned, out)
		}
	}
}

// writeZcodeFixture 在临时 home 写 zcode 布局宿主配置（<home>/.zcode/cli/setting.json，
// 顶层 mcpServers）。返回文件字节（供版本 hash 断言）。
func writeZcodeFixture(t *testing.T, home, entryName string, servers map[string]any) []byte {
	t.Helper()
	path := filepath.Join(home, ".zcode", "cli", "setting.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"mcpServers": servers}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

// zcodeFixtureServers 默认条目（目标条目在位、指向 node 范围）。
func zcodeFixtureServers() map[string]any {
	return map[string]any{
		"swarmmind-node": map[string]any{
			"command": "/fake/caller/swarmmind-mcp",
			"env": map[string]any{
				"SWARM_TARGET":           "node",
				"SWARM_NODE_URL":         "http://127.0.0.1:8445",
				"SWARM_NODE_PRIME_TOKEN": "stale-old-value",
			},
		},
	}
}

// v0ReadyInput 齐备输入基线（可逐字段覆盖构造各失败态）。
func v0ReadyInput() V0CheckInput {
	return V0CheckInput{
		Scope:          ScopeNode,
		CallerEnv:      CallerSameMachine,
		Host:           "zcode",
		ServerURL:      "http://127.0.0.1:8445",
		McpCommandPath: "/fake/caller/swarmmind-mcp",
		CredentialRef:  "fa********",
	}
}

// v0Item 按 key 取检查项。
func v0Item(t *testing.T, c V0Check, key string) V0CheckItem {
	t.Helper()
	for _, it := range c.Checks {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("V0 检查项 %q 不存在（共 %d 项）", key, len(c.Checks))
	return V0CheckItem{}
}

// TestV0CheckReadyWithFile 齐备态（含宿主配置文件粗检）：逐项 pass、项序稳定、
// 文件版本 hash 即配置版本可见取值源、输出面零明文金丝。
func TestV0CheckReadyWithFile(t *testing.T) {
	home := t.TempDir()
	raw := writeZcodeFixture(t, home, "swarmmind-node", zcodeFixtureServers())
	in := v0ReadyInput()
	in.ConfigHome = home
	check := RunV0Checks(in)

	if check.Status != VerifyStatusPass {
		t.Fatalf("齐备输入应 pass，得 %s（明细：%+v）", check.Status, check.Checks)
	}
	wantKeys := []string{
		V0KeyScopeValid, V0KeyScopeConsistency, V0KeyServiceURL, V0KeyTopology,
		V0KeyCommandPath, V0KeyCredential, V0KeyHostSupport, V0KeyHostFile,
	}
	if len(check.Checks) != len(wantKeys) {
		t.Fatalf("检查项数不符: got %d want %d", len(check.Checks), len(wantKeys))
	}
	for i, it := range check.Checks {
		if it.Key != wantKeys[i] {
			t.Fatalf("检查项顺序漂移: [%d]=%s want %s", i, it.Key, wantKeys[i])
		}
		if it.Status != VerifyStatusPass {
			t.Fatalf("齐备态检查项 %s 应 pass，得 %s：%s", it.Key, it.Status, it.Detail)
		}
	}
	// 文件版本事实：ConfigVersion = 宿主配置文件 sha256（mcpcfgfile file.version 同源）。
	if want := mcpCfgVersionHash(raw); check.ConfigVersion != want {
		t.Fatalf("ConfigVersion 应为文件版本 hash: got %s want %s", check.ConfigVersion, want)
	}
	// 范围一致性经文件实取条目判定（SWARM_TARGET=node 与 scope=node 一致）。
	if it := v0Item(t, check, V0KeyScopeConsistency); !strings.Contains(it.Detail, "一致") {
		t.Fatalf("范围一致性应命中既有条目判定：%s", it.Detail)
	}
	// 边界句硬规则：pass 必带「不推断上层」。
	joined := strings.Join(check.Notes, "\n")
	if !strings.Contains(joined, "不推断 V1") {
		t.Fatalf("V0 pass 必带不推断上层边界句：%s", joined)
	}
	// 输出面金丝：假密钥（宿主配置 fixture 中预埋的旧值与入参引用形态）不外泄。
	out, err := json.Marshal(check)
	if err != nil {
		t.Fatal(err)
	}
	cfgNoSecret(t, "V0 齐备态 JSON", string(out), verifyFakeCred, cfgFakeNodeCred, "stale-old-value")
}

// TestV0CheckReadyWithoutFile 齐备态（未提供 config_home）：文件项 not_checked
// 不阻塞，整体 pass（hub/未应用场景的页面缺省路径）。
func TestV0CheckReadyWithoutFile(t *testing.T) {
	check := RunV0Checks(v0ReadyInput())
	if check.Status != VerifyStatusPass {
		t.Fatalf("无文件齐备输入应 pass，得 %s", check.Status)
	}
	if it := v0Item(t, check, V0KeyHostFile); it.Status != VerifyStatusNotChecked {
		t.Fatalf("未提供 config_home 时 host_file 应 not_checked，得 %s", it.Status)
	}
	if check.ConfigVersion != "" {
		t.Fatalf("文件未读时 ConfigVersion 应为空，得 %s", check.ConfigVersion)
	}
}

// TestV0CheckMissingRequired 缺必填各态：缺路径/缺凭据/非法地址，逐项失败且
// 指向字段，整体 fail。
func TestV0CheckMissingRequired(t *testing.T) {
	t.Run("缺路径_含占位符形态", func(t *testing.T) {
		for _, path := range []string{"", mcpCallerPathPlaceholder, "  "} {
			in := v0ReadyInput()
			in.McpCommandPath = path
			check := RunV0Checks(in)
			if check.Status != VerifyStatusFail {
				t.Fatalf("缺路径应整体 fail，得 %s", check.Status)
			}
			it := v0Item(t, check, V0KeyCommandPath)
			if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "mcp_command_path") {
				t.Fatalf("command_path 失败项须指向字段：%s / %s", it.Status, it.Detail)
			}
		}
	})
	t.Run("缺凭据_占位与未生成形态", func(t *testing.T) {
		for _, cred := range []string{"", mcpCredRefPlaceholder, templateCredentialPlaceholder, maskCredential("")} {
			in := v0ReadyInput()
			in.CredentialRef = cred
			check := RunV0Checks(in)
			if check.Status != VerifyStatusFail {
				t.Fatalf("缺凭据应整体 fail（cred=%q），得 %s", cred, check.Status)
			}
			it := v0Item(t, check, V0KeyCredential)
			if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "credential_ref") {
				t.Fatalf("credential 失败项须指向字段：%s / %s", it.Status, it.Detail)
			}
		}
	})
	t.Run("非法服务地址", func(t *testing.T) {
		in := v0ReadyInput()
		in.ServerURL = "ftp://x"
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyServiceURL)
		if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "service_url") {
			t.Fatalf("service_url 失败项须指向字段：%s / %s", it.Status, it.Detail)
		}
		if check.Status != VerifyStatusFail {
			t.Fatalf("非法地址应整体 fail，得 %s", check.Status)
		}
	})
	t.Run("非法范围", func(t *testing.T) {
		in := v0ReadyInput()
		in.Scope = "cluster"
		check := RunV0Checks(in)
		if it := v0Item(t, check, V0KeyScopeValid); it.Status != VerifyStatusFail {
			t.Fatalf("非法范围应 scope_valid fail：%+v", it)
		}
		if check.Status != VerifyStatusFail {
			t.Fatalf("非法范围应整体 fail，得 %s", check.Status)
		}
	})
}

// TestV0CheckScopeInconsistency 范围不一致：宿主既有条目 SWARM_TARGET=hub 与
// 目标范围 node 不符 → 失败并指向条目与字段（AC07/AC10 同族语义）。
func TestV0CheckScopeInconsistency(t *testing.T) {
	in := v0ReadyInput()
	in.ExistingEntries = map[string]ExistingMcpEntry{
		"swarmmind-node": {
			Command: "/fake/caller/swarmmind-mcp",
			Env:     map[string]string{"SWARM_TARGET": ScopeHub},
		},
	}
	check := RunV0Checks(in)
	if check.Status != VerifyStatusFail {
		t.Fatalf("范围不一致应整体 fail，得 %s", check.Status)
	}
	it := v0Item(t, check, V0KeyScopeConsistency)
	if it.Status != VerifyStatusFail ||
		!strings.Contains(it.Detail, "范围不一致") || !strings.Contains(it.Detail, "SWARM_TARGET") {
		t.Fatalf("范围不一致失败项文案不符：%s / %s", it.Status, it.Detail)
	}
}

// TestV0CheckLoopbackTopology 回环拓扑（AC07）：外部调用机 + 回环 = 危险拓扑失败；
// 同机 + 回环 = 正常拓扑通过且带警示说明。
func TestV0CheckLoopbackTopology(t *testing.T) {
	t.Run("外部调用机加回环_危险拓扑失败", func(t *testing.T) {
		in := v0ReadyInput()
		in.CallerEnv = CallerRemote
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyTopology)
		if it.Status != VerifyStatusFail ||
			!strings.Contains(it.Detail, "危险拓扑") || !strings.Contains(it.Detail, "service_url") {
			t.Fatalf("回环+远端应危险拓扑失败并指向字段：%s / %s", it.Status, it.Detail)
		}
		if check.Status != VerifyStatusFail {
			t.Fatalf("危险拓扑应整体 fail，得 %s", check.Status)
		}
	})
	t.Run("同机回环_正常拓扑通过", func(t *testing.T) {
		in := v0ReadyInput() // CallerEnv=same_machine，URL=127.0.0.1
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyTopology)
		if it.Status != VerifyStatusPass || !strings.Contains(it.Detail, "正常拓扑") {
			t.Fatalf("同机回环应 pass 且注明正常拓扑：%s / %s", it.Status, it.Detail)
		}
	})
	t.Run("非法调用环境", func(t *testing.T) {
		in := v0ReadyInput()
		in.CallerEnv = "somewhere"
		check := RunV0Checks(in)
		if it := v0Item(t, check, V0KeyTopology); it.Status != VerifyStatusFail {
			t.Fatalf("非法调用环境应 topology fail：%+v", it)
		}
	})
}

// TestV0CheckHostSupportLevels 宿主支持级别各态：zcode pass；codex/未知宿主
// warn 不阻塞（如实标注未实测，不冒认已适配）；未知宿主不做文件粗检。
func TestV0CheckHostSupportLevels(t *testing.T) {
	for _, tc := range []struct {
		name       string
		host       string
		wantStatus string
		wantHint   string
	}{
		{"zcode_已实测", "zcode", VerifyStatusPass, SupportLevelVerifiedZcode},
		{"codex_通用手动未实测", "codex", VerifyStatusWarn, SupportLevelManualGenericCodex},
		{"未知宿主", "claude-code", VerifyStatusWarn, SupportLevelManualGeneric},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := v0ReadyInput()
			in.Host = tc.host
			check := RunV0Checks(in)
			it := v0Item(t, check, V0KeyHostSupport)
			if it.Status != tc.wantStatus || !strings.Contains(it.Detail, tc.wantHint) {
				t.Fatalf("宿主支持级别不符（%s）：%s / %s（want %s/%s）",
					tc.host, it.Status, it.Detail, tc.wantStatus, tc.wantHint)
			}
			if tc.host == "zcode" {
				if check.Status != VerifyStatusPass {
					t.Fatalf("zcode 齐备输入应整体 pass，得 %s", check.Status)
				}
			} else if check.Status != VerifyStatusPass {
				// warn 不阻塞：其余项齐备时整体仍 pass。
				t.Fatalf("未实测宿主应 warn 不阻塞（整体 pass），得 %s", check.Status)
			}
		})
	}
}

// TestV0CheckHostFileCoarseChecks 宿主配置文件粗检各态：文件缺失/目标条目未写
// 入/非法 JSON=失败；codex TOML 不解析但版本 hash 照实给出（not_checked）；
// 全空输入=整体 not_checked。
func TestV0CheckHostFileCoarseChecks(t *testing.T) {
	t.Run("文件缺失", func(t *testing.T) {
		in := v0ReadyInput()
		in.ConfigHome = t.TempDir()
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyHostFile)
		if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "不存在") {
			t.Fatalf("文件缺失应失败并如实呈现：%s / %s", it.Status, it.Detail)
		}
	})
	t.Run("目标条目未写入", func(t *testing.T) {
		home := t.TempDir()
		writeZcodeFixture(t, home, "swarmmind-node", map[string]any{
			"other-entry": map[string]any{"command": "/x"},
		})
		in := v0ReadyInput()
		in.ConfigHome = home
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyHostFile)
		if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "尚未写入") {
			t.Fatalf("条目未写入应失败：%s / %s", it.Status, it.Detail)
		}
		if check.Status != VerifyStatusFail {
			t.Fatalf("条目未写入应整体 fail，得 %s", check.Status)
		}
	})
	t.Run("非法JSON_错误文案不回内容", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".zcode", "cli", "setting.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
			t.Fatal(err)
		}
		in := v0ReadyInput()
		in.ConfigHome = home
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyHostFile)
		if it.Status != VerifyStatusFail || !strings.Contains(it.Detail, "invalid_json") {
			t.Fatalf("非法 JSON 应失败并带分类：%s / %s", it.Status, it.Detail)
		}
		if strings.Contains(it.Detail, "oops") {
			t.Fatalf("错误文案不得回显文件内容：%s", it.Detail)
		}
	})
	t.Run("codex_TOML不解析但版本事实在位", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".codex", "config.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		raw := []byte("[mcp_servers.swarmmind-hub]\ncommand = \"/fake\"\n")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		in := v0ReadyInput()
		in.Host = "codex"
		in.ConfigHome = home
		check := RunV0Checks(in)
		it := v0Item(t, check, V0KeyHostFile)
		if it.Status != VerifyStatusNotChecked || !strings.Contains(it.Detail, "TOML") {
			t.Fatalf("codex 粗检应 not_checked 并注明 TOML 不解析：%s / %s", it.Status, it.Detail)
		}
		if want := mcpCfgVersionHash(raw); check.ConfigVersion != want {
			t.Fatalf("codex 版本事实应照实给出: got %s want %s", check.ConfigVersion, want)
		}
	})
	t.Run("全空输入_未运行", func(t *testing.T) {
		check := RunV0Checks(V0CheckInput{})
		if check.Status != VerifyStatusNotChecked || len(check.Checks) != 0 {
			t.Fatalf("全空输入应 not_checked 且无检查项: %s / %d 项", check.Status, len(check.Checks))
		}
	})
}

// TestV0PassDoesNotInferUpperLayers 投影断言（任务书硬规则）：V0 pass 登记后，
// V1–V5 五层缺省全部 not_checked——页面默认只保证 V0，绝不推断任何上层。
func TestV0PassDoesNotInferUpperLayers(t *testing.T) {
	m := NewMcpVerifyModel("ver-A", envNodeToken)
	check := RunV0Checks(v0ReadyInput())
	if check.Status != VerifyStatusPass {
		t.Fatalf("前置：V0 应 pass，得 %s", check.Status)
	}
	m.RecordV0(check)
	states := m.LayerStates()
	if len(states) != 6 {
		t.Fatalf("投影应为六层，得 %d", len(states))
	}
	for i, st := range states {
		if st.Layer != allLayers[i] {
			t.Fatalf("投影层序漂移: [%d]=%s want %s", i, st.Layer, allLayers[i])
		}
		if i == 0 {
			if st.Status != VerifyStatusPass || st.CheckedBy != VerifySourceServerSideView {
				t.Fatalf("V0 应 pass 且 CheckedBy=server_side_view: %+v", st)
			}
			if st.ConfigVersion != "ver-A" {
				t.Fatalf("V0 结果应绑定当前版本 ver-A，得 %q", st.ConfigVersion)
			}
			continue
		}
		if st.Status != VerifyStatusNotChecked {
			t.Fatalf("V0 pass 绝不推断上层：%s 应 not_checked，得 %s（%s）", st.Layer, st.Status, st.Detail)
		}
		if st.CheckedBy == VerifySourceUserConfirmed || st.Status == VerifyStatusUserConfirmed {
			t.Fatalf("上层缺省态不得是用户确认态：%+v", st)
		}
	}
	// V0 输出 JSON 面不得携带任何上层通过/确认结论（字段序 Layer→Status，
	// `"layer":"vX","status":"pass"` 即上层被点亮为服务端通过——命中即失败）。
	out, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, layer := range []string{LayerV1, LayerV3, LayerV4, LayerV5} {
		for _, lit := range []string{VerifyStatusPass, VerifyStatusUserConfirmed} {
			banned := `"layer":"` + layer + `","status":"` + lit + `"`
			if strings.Contains(s, banned) {
				t.Fatalf("板面投影不得把 %s 标为 %s", layer, lit)
			}
		}
	}
}

// TestV2ProbeCheckThreeStates V2 服务端视角三态（注入假 HTTP 检查器，零网络）：
// 200/401/403 判定、连接层失败与意外状态码 not_checked、CheckedBy 恒在位、
// 输出面禁词与明文金丝。
func TestV2ProbeCheckThreeStates(t *testing.T) {
	const ver = "ver-A"
	fake := func(status int, err error) V2ProbeFunc {
		return func(_ context.Context, _, _ string) (int, error) { return status, err }
	}
	cases := []struct {
		name       string
		probe      V2ProbeFunc
		wantStatus string
		wantKind   string
		wantHTTP   int
	}{
		{"200_服务端视角通过", fake(http.StatusOK, nil), VerifyStatusPass, "", http.StatusOK},
		{"401_过期撤销", fake(http.StatusUnauthorized, nil), VerifyStatusFail, V2FailKindUnauthorized, http.StatusUnauthorized},
		{"403_未授权", fake(http.StatusForbidden, nil), VerifyStatusFail, V2FailKindForbidden, http.StatusForbidden},
		{"500_无法判定", fake(http.StatusInternalServerError, nil), VerifyStatusNotChecked, V2FailKindUnexpected, http.StatusInternalServerError},
		{"连接层失败_not_checked", func(_ context.Context, _, _ string) (int, error) {
			return 0, fmt.Errorf("dial tcp: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		}, VerifyStatusNotChecked, V2FailKindNetwork, 0},
		{"探测原语未装配", nil, VerifyStatusNotChecked, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := RunV2ProbeCheck(tc.probe, V2CheckInput{
				ServiceURL: "http://127.0.0.1:8445", Credential: verifyFakeCred, ConfigVersion: ver,
			})
			if check.Status != tc.wantStatus || check.FailKind != tc.wantKind || check.HTTPStatus != tc.wantHTTP {
				t.Fatalf("三态判定不符: got status=%s kind=%s http=%d, want %s/%s/%d",
					check.Status, check.FailKind, check.HTTPStatus, tc.wantStatus, tc.wantKind, tc.wantHTTP)
			}
			// CheckedBy 恒在位：任何结局都不改变检查视角（绝不冒认外部）。
			if check.CheckedBy != VerifySourceServerSideView {
				t.Fatalf("CheckedBy 恒为 server_side_view，得 %q", check.CheckedBy)
			}
			if check.ConfigVersion != ver {
				t.Fatalf("结果应绑定配置版本 %s，得 %q", ver, check.ConfigVersion)
			}
			if _, err := time.Parse(time.RFC3339, check.CheckedAt); err != nil || !strings.HasSuffix(check.CheckedAt, "Z") {
				t.Fatalf("CheckedAt 须为 UTC RFC3339: %q (%v)", check.CheckedAt, err)
			}
			out, err := json.Marshal(check)
			if err != nil {
				t.Fatal(err)
			}
			verifyNoExternalClaim(t, "V2 检查输出", string(out))
			// 明文金丝：探测凭据绝不进输出。
			cfgNoSecret(t, "V2 检查输出", string(out), verifyFakeCred)
			// 语义分流正向断言：连接层失败须指回 V1；通过态须带服务端视角边界句。
			switch tc.wantKind {
			case V2FailKindNetwork:
				if !strings.Contains(check.Detail, "V1") {
					t.Fatalf("连接层失败应指回 V1：%s", check.Detail)
				}
			case "":
				if tc.wantStatus == VerifyStatusPass && !strings.Contains(check.Detail, "服务端视角") {
					t.Fatalf("通过态须明示服务端视角：%s", check.Detail)
				}
			}
		})
	}
	t.Run("非法服务地址_未检", func(t *testing.T) {
		check := RunV2ProbeCheck(fake(http.StatusOK, nil), V2CheckInput{ServiceURL: "nonsense"})
		if check.Status != VerifyStatusNotChecked || check.CheckedBy != VerifySourceServerSideView {
			t.Fatalf("非法地址应 not_checked 且视角恒在位: %+v", check)
		}
	})
	t.Run("401与403指引转人工授权入口", func(t *testing.T) {
		for kind, want := range map[string][]string{
			V2FailKindUnauthorized: {"撤销", "人工授权"},
			V2FailKindForbidden:    {"403", "人工授权"},
		} {
			g, ok := GuidanceFor(LayerV2, kind)
			if !ok {
				t.Fatalf("V2 指引 %q 应可生成", kind)
			}
			joined := g.Title + strings.Join(g.Steps, "\n")
			for _, kw := range want {
				if !strings.Contains(joined, kw) {
					t.Fatalf("V2 %s 指引缺关键词 %q：%s", kind, kw, joined)
				}
			}
			verifyNoExternalClaim(t, "V2 指引 "+kind, joined)
		}
	})
}

// TestRegisterExternalConfirmation 外部确认登记：校验链（层集合/来源/版本/时间）、
// 点亮态、版本变更后旧确认不点亮新配置（AC10）、历史保留时间与来源可查、缺省态
// 如实「等待外部验证」。
func TestRegisterExternalConfirmation(t *testing.T) {
	m := NewMcpVerifyModel("ver-A", envNodeToken)

	t.Run("缺省态_等待外部验证", func(t *testing.T) {
		for _, layer := range []string{LayerV1, LayerV3, LayerV4, LayerV5} {
			st := m.LayerState(layer)
			if st.Status != VerifyStatusNotChecked || st.CheckedBy != "" {
				t.Fatalf("%s 缺省应未验证无视角：%+v", layer, st)
			}
			// 缺省文案按层分流：v1/v3/v4 等待外部验证；v5 默认不运行（会消耗模型额度）。
			want := "等待外部验证"
			if layer == LayerV5 {
				want = "默认不运行"
			}
			if !strings.Contains(st.Detail, want) {
				t.Fatalf("%s 缺省文案应含 %q：%s", layer, want, st.Detail)
			}
		}
	})
	t.Run("校验链", func(t *testing.T) {
		for _, c := range []ExternalConfirmation{
			{Layer: LayerV0, ConfigVersion: "ver-A"},                            // 服务端可证层不收
			{Layer: LayerV2, ConfigVersion: "ver-A"},                            // 服务端可证层不收
			{Layer: "v9", ConfigVersion: "ver-A"},                               // 未知层
			{Layer: LayerV3, Source: "auto_receipt", ConfigVersion: "ver-A"},    // 无自动回执通道
			{Layer: LayerV3, ConfigVersion: ""},                                 // 版本必填
			{Layer: LayerV3, ConfigVersion: "ver-A", ConfirmedAt: "not-a-time"}, // 时间格式
		} {
			if _, err := m.RegisterExternalConfirmation(c); err == nil {
				t.Fatalf("应拒绝登记：%+v", c)
			}
		}
	})
	t.Run("登记点亮_版本时间来源可见", func(t *testing.T) {
		got, err := m.RegisterExternalConfirmation(ExternalConfirmation{
			Layer: LayerV3, ConfigVersion: "ver-A",
			ConfirmedAt: "2026-10-30T08:00:00Z", Note: "宿主重载后 tools/list 可见 6 工具",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != VerifyStatusUserConfirmed || got.CheckedBy != VerifySourceUserConfirmed {
			t.Fatalf("登记后应 user_confirmed：%+v", got)
		}
		if got.ConfigVersion != "ver-A" || got.CheckedAt != "2026-10-30T08:00:00Z" {
			t.Fatalf("确认须绑定版本与时间：%+v", got)
		}
	})
	t.Run("非UTC时间归一UTC", func(t *testing.T) {
		got, err := m.RegisterExternalConfirmation(ExternalConfirmation{
			Layer: LayerV4, ConfigVersion: "ver-A", ConfirmedAt: "2026-10-30T10:00:00+02:00",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.CheckedAt != "2026-10-30T08:00:00Z" {
			t.Fatalf("确认时间应归一 UTC： got %q", got.CheckedAt)
		}
	})
	t.Run("版本变更_旧确认不点亮新配置_历史保留", func(t *testing.T) {
		if err := m.SetConfigVersion("ver-B"); err != nil {
			t.Fatal(err)
		}
		st := m.LayerState(LayerV3)
		if st.Status != VerifyStatusNotChecked {
			t.Fatalf("版本变更后旧确认不得点亮新配置（AC10），应回落 not_checked：%+v", st)
		}
		if !strings.Contains(st.Detail, "旧配置版本") {
			t.Fatalf("回落态须注明旧版本绑定：%+v", st)
		}
		hist := m.History()
		found := false
		for _, h := range hist {
			if h.Layer == LayerV3 && h.Result == VerifyStatusUserConfirmed &&
				h.ConfigVersion == "ver-A" && h.Source == VerifySourceUserConfirmed && h.At != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("历史须保留旧确认（来源/时间/版本可查）：%+v", hist)
		}
		// 新版本重新确认后点亮。
		got, err := m.RegisterExternalConfirmation(ExternalConfirmation{Layer: LayerV3, ConfigVersion: "ver-B"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != VerifyStatusUserConfirmed {
			t.Fatalf("新配置上的新确认应点亮：%+v", got)
		}
	})
	t.Run("SetConfigVersion空锚拒绝", func(t *testing.T) {
		if err := m.SetConfigVersion("  "); err == nil {
			t.Fatal("空版本锚应拒绝")
		}
	})
	t.Run("板面输出金丝", func(t *testing.T) {
		out, err := json.Marshal(m.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		verifyNoExternalClaim(t, "板面快照", string(out))
		cfgNoSecret(t, "板面快照", string(out), verifyFakeCred, cfgFakeNodeCred)
	})
}

// TestSecretLifecycle 一次性秘密生命周期：状态机迁移、失效判定、疑似明文拒绝、
// 全程零明文。
func TestSecretLifecycle(t *testing.T) {
	l := NewSecretLifecycle(envNodeToken)
	ref := l.Snapshot()
	if ref.State != SecretStatePlaceholder || ref.Ref != "" || ref.EnvVar != envNodeToken {
		t.Fatalf("初始应为 placeholder 空引用：%+v", ref)
	}
	if SecretInvalidates(ref, "ver-A") {
		t.Fatal("placeholder 不应触发失效判定")
	}
	if _, err := time.Parse(time.RFC3339, ref.UpdatedAt); err != nil {
		t.Fatalf("UpdatedAt 须 UTC RFC3339：%q", ref.UpdatedAt)
	}

	// placeholder → user_provided。
	if err := l.MarkUserProvided("ab********", "ver-A"); err != nil {
		t.Fatal(err)
	}
	ref = l.Snapshot()
	if ref.State != SecretStateUserProvided || ref.Ref != "ab********" || ref.Version != "ver-A" {
		t.Fatalf("迁移后状态不符：%+v", ref)
	}
	if SecretInvalidates(ref, "ver-A") {
		t.Fatal("user_provided 不应触发失效判定")
	}

	// user_provided → rotated_invalid：打中 ver-A 配置（仍载旧凭据）。
	l.MarkRotated("ver-A")
	ref = l.Snapshot()
	if ref.State != SecretStateRotatedInvalid || ref.Version != "ver-A" {
		t.Fatalf("轮换后状态不符：%+v", ref)
	}
	if !SecretInvalidates(ref, "ver-A") {
		t.Fatal("轮换应使命中版本的凭据相关验证失效")
	}
	if SecretInvalidates(ref, "ver-B") {
		t.Fatal("轮换不应波及其他版本（新配置绑定新版本后另行判定）")
	}

	// 换发后重新绑定新配置版本。
	if err := l.MarkUserProvided("cd********", "ver-B"); err != nil {
		t.Fatal(err)
	}
	if SecretInvalidates(l.Snapshot(), "ver-B") {
		t.Fatal("新凭据绑定新版本后不应再命中失效判定")
	}

	// 拒绝面：空引用/疑似明文（超长）/占位标记/空版本。
	for _, tc := range []struct {
		name string
		ref  string
		ver  string
	}{
		{"空引用", "", "ver-X"},
		{"疑似明文_超长", strings.Repeat("x", 40), "ver-X"},
		{"占位标记", mcpCredRefPlaceholder, "ver-X"},
		{"模板占位文案", templateCredentialPlaceholder, "ver-X"},
		{"未生成掩码", maskCredential(""), "ver-X"},
		{"空版本", "ab********", " "},
	} {
		if err := l.MarkUserProvided(tc.ref, tc.ver); err == nil {
			t.Fatalf("%s 应拒绝入库（%q）", tc.name, tc.ref)
		}
	}
	// 状态机拒绝后保持原态（不半迁移）。
	if ref := l.Snapshot(); ref.State != SecretStateUserProvided || ref.Version != "ver-B" {
		t.Fatalf("拒绝后不得改变状态：%+v", ref)
	}
}

// TestRotateSecretInvalidates 板面集成：轮换共享凭据 → 凭据相关层（v2/v3/v4/v5）
// 既有结果即失效（not_checked + 历史 invalidated 留痕），v0/v1 不受凭据轮换影响。
func TestRotateSecretInvalidates(t *testing.T) {
	m := NewMcpVerifyModel("ver-A", envNodeToken)
	if err := m.MarkSecretProvided("ab********", "ver-A"); err != nil {
		t.Fatal(err)
	}
	m.RecordV0(RunV0Checks(v0ReadyInput())) // v0 pass（不依赖凭据有效性）
	check := RunV2ProbeCheck(func(_ context.Context, _, _ string) (int, error) {
		return http.StatusOK, nil
	}, V2CheckInput{ServiceURL: "http://127.0.0.1:8445", Credential: verifyFakeCred, ConfigVersion: "ver-A"})
	m.RecordV2(check)
	if _, err := m.RegisterExternalConfirmation(ExternalConfirmation{
		Layer: LayerV4, ConfigVersion: "ver-A", Note: "目录可查",
	}); err != nil {
		t.Fatal(err)
	}
	if st := m.LayerState(LayerV2); st.Status != VerifyStatusPass {
		t.Fatalf("前置：v2 应 pass：%+v", st)
	}

	ref := m.RotateSecret()
	if ref.State != SecretStateRotatedInvalid || ref.Version != "ver-A" {
		t.Fatalf("轮换后秘密状态不符：%+v", ref)
	}

	if st := m.LayerState(LayerV0); st.Status != VerifyStatusPass {
		t.Fatalf("v0 不依赖凭据有效性，轮换后应保持 pass：%+v", st)
	}
	for _, layer := range []string{LayerV2, LayerV3, LayerV4, LayerV5} {
		if st := m.LayerState(layer); st.Status != VerifyStatusNotChecked {
			t.Fatalf("%s 轮换后应回落 not_checked：%+v", layer, st)
		}
	}
	found := 0
	for _, h := range m.History() {
		if h.Result == verifyResultInvalidated {
			found++
		}
	}
	if found != 2 { // v2 与 v4 有既有结果可失效；v3/v5 本就未验证，无结果可失效
		t.Fatalf("历史应留痕 v2/v4 两条 invalidated，得 %d", found)
	}
	// 明文金丝：轮换全程板面输出不含假密钥。
	out, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	cfgNoSecret(t, "轮换后板面", string(out), verifyFakeCred, cfgFakeNodeCred)
}

// TestVerifyStale 失效判定纯函数：版本不符/显式失效/凭据轮换三路径，v1 不受
// 凭据轮换影响，not_checked 本身即不点亮。
func TestVerifyStale(t *testing.T) {
	rotated := SecretRef{State: SecretStateRotatedInvalid, Version: "ver-A"}
	lit := func(layer string) VerifyLayerState {
		return VerifyLayerState{Layer: layer, Status: VerifyStatusPass, ConfigVersion: "ver-A"}
	}
	cases := []struct {
		name   string
		stored VerifyLayerState
		cur    string
		secret SecretRef
		want   bool
	}{
		{"版本不符", lit(LayerV2), "ver-B", SecretRef{}, true},
		{"空绑定版本", VerifyLayerState{Layer: LayerV2, Status: VerifyStatusPass}, "ver-A", SecretRef{}, true},
		{"同版本新鲜", lit(LayerV2), "ver-A", SecretRef{}, false},
		{"同版本但已显式失效", func() VerifyLayerState {
			st := lit(LayerV2)
			st.Invalidated = true
			return st
		}(), "ver-A", SecretRef{}, true},
		{"凭据层遇轮换打中当前版本", lit(LayerV2), "ver-A", rotated, true},
		{"凭据层确认态同样受轮换影响", func() VerifyLayerState {
			return VerifyLayerState{Layer: LayerV3, Status: VerifyStatusUserConfirmed, ConfigVersion: "ver-A"}
		}(), "ver-A", rotated, true},
		{"v1 不受凭据轮换影响", lit(LayerV1), "ver-A", rotated, false},
		{"not_checked 即失效", VerifyLayerState{Layer: LayerV2, Status: VerifyStatusNotChecked, ConfigVersion: "ver-A"}, "ver-A", SecretRef{}, true},
	}
	for _, tc := range cases {
		if got := VerifyStale(tc.stored, tc.cur, tc.secret); got != tc.want {
			t.Fatalf("%s: VerifyStale=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestGuidanceCoverage 六层指引：失败态各自生成、文案覆盖 §12.4 失败显示列
// 关键词、未知层/未知 kind 拒绝产出。
func TestGuidanceCoverage(t *testing.T) {
	cases := []struct {
		layer string
		kind  string
		want  []string // 关键词须出现在 标题+步骤 合文中
	}{
		// V0 失败显示列：缺路径/凭据/格式/危险拓扑，指向字段。
		{LayerV0, V0KeyCommandPath, []string{"mcp_command_path", "路径"}},
		{LayerV0, V0KeyCredential, []string{"凭据"}},
		{LayerV0, V0KeyServiceURL, []string{"service_url"}},
		{LayerV0, "format", []string{"格式", "语法"}},
		{LayerV0, V0KeyTopology, []string{"危险拓扑", "127.0.0.1"}},
		{LayerV0, V0KeyScopeConsistency, []string{"范围"}},
		{LayerV0, VerifyStatusNotChecked, []string{"未检查"}},
		// V1 失败显示列：DNS/拒绝连接/证书错误/转发缺失；不建议跳过 TLS 验证。
		{LayerV1, ConnKindDNS, []string{"DNS"}},
		{LayerV1, ConnKindRefused, []string{"拒绝"}},
		{LayerV1, ConnKindTLS, []string{"证书", "不建议跳过 TLS 验证"}},
		{LayerV1, ConnKindTimeout, []string{"超时"}},
		{LayerV1, ConnKindForwardMissing, []string{"转发"}},
		{LayerV1, ConnKindOther, []string{v2ProbePath}},
		{LayerV1, VerifyStatusNotChecked, []string{"等待外部验证"}},
		// V2 失败显示列：401 过期/撤销 或 403 未授权，转人工授权入口。
		{LayerV2, V2FailKindUnauthorized, []string{"401", "撤销"}},
		{LayerV2, V2FailKindForbidden, []string{"403", "人工授权"}},
		{LayerV2, V2FailKindNetwork, []string{"V1"}},
		{LayerV2, V2FailKindUnexpected, []string{"不猜测"}},
		{LayerV2, VerifyStatusNotChecked, []string{"服务端视角"}},
		// V3 失败显示列：宿主配置未重载/启动失败/协议或版本缺项，不能靠服务端自测冒认。
		{LayerV3, "not_reloaded", []string{"重载"}},
		{LayerV3, "start_failed", []string{"启动失败"}},
		{LayerV3, "protocol_missing", []string{"tools/list", "不能靠服务端自测冒认"}},
		{LayerV3, VerifyStatusNotChecked, []string{"等待外部验证"}},
		// V4 失败显示列：目录为空 ≠ 连接失败（目录为空也可以接入成功）。
		{LayerV4, "empty_directory", []string{"目录为空", "连接失败"}},
		{LayerV4, "connection_failed", []string{"V1", "V2"}},
		{LayerV4, VerifyStatusNotChecked, []string{"等待外部验证"}},
		// V5 失败显示列：区分接受/执行/工具权限/工作失败；会消耗模型额度，默认不运行。
		{LayerV5, "accepted", []string{"接受"}},
		{LayerV5, "execution_failed", []string{"执行失败"}},
		{LayerV5, "tool_permission", []string{"工具权限"}},
		{LayerV5, "work_failed", []string{"工作失败"}},
		{LayerV5, VerifyStatusNotChecked, []string{"模型额度", "默认不运行"}},
	}
	for _, tc := range cases {
		g, ok := GuidanceFor(tc.layer, tc.kind)
		if !ok {
			t.Fatalf("%s/%s 指引应可生成", tc.layer, tc.kind)
		}
		if g.Label != LayerLabel(tc.layer) {
			t.Fatalf("指引层名不符: got %q want %q", g.Label, LayerLabel(tc.layer))
		}
		joined := g.Title + "\n" + strings.Join(g.Steps, "\n")
		for _, kw := range tc.want {
			if !strings.Contains(joined, kw) {
				t.Fatalf("%s/%s 指引缺 §12.4 关键词 %q：%s", tc.layer, tc.kind, kw, joined)
			}
		}
		if len(g.Steps) == 0 {
			t.Fatalf("%s/%s 指引须有可复制步骤", tc.layer, tc.kind)
		}
		verifyNoExternalClaim(t, fmt.Sprintf("指引 %s/%s", tc.layer, tc.kind), joined)
		cfgNoSecret(t, fmt.Sprintf("指引 %s/%s", tc.layer, tc.kind), joined, verifyFakeCred)
	}
	// 未知层与未知 kind 一律拒绝（不猜测、不产出含糊文案）。
	for _, tc := range []struct{ layer, kind string }{
		{"v9", "anything"}, {"", "anything"}, {LayerV0, "no-such-kind"}, {LayerV3, "401"},
	} {
		if g, ok := GuidanceFor(tc.layer, tc.kind); ok {
			t.Fatalf("非法指引入参应拒绝: %s/%s → %+v", tc.layer, tc.kind, g)
		}
	}
}

// TestConnErrorKind 连接错误分类（V1 语义：DNS/拒绝/证书/超时/其他）。
func TestConnErrorKind(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"DNS", &net.DNSError{Err: "no such host", Name: "x.example"}, ConnKindDNS},
		{"拒绝连接", fmt.Errorf("dial tcp %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), ConnKindRefused},
		{"拒绝连接_文本兜底", errors.New("connect: connection refused"), ConnKindRefused},
		{"证书错误", errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), ConnKindTLS},
		{"超时_文本兜底", errors.New(`Get "https://x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), ConnKindTimeout},
		{"其他", errors.New("boom"), ConnKindOther},
	}
	for _, tc := range cases {
		if got := ConnErrorKind(tc.err); got != tc.want {
			t.Fatalf("%s: ConnErrorKind=%q want %q", tc.name, got, tc.want)
		}
	}
}

// TestMcpVerifyModelConcurrent 板面并发烟测（-race 下验证锁完备性）：投影、
// 登记、轮换、历史读取交错无数据竞争。
func TestMcpVerifyModelConcurrent(t *testing.T) {
	m := NewMcpVerifyModel("ver-A", envNodeToken)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = m.LayerStates()
			_, _ = m.RegisterExternalConfirmation(ExternalConfirmation{
				Layer: LayerV3, ConfigVersion: "ver-A", Note: fmt.Sprintf("并发确认-%d", i),
			})
			_ = m.History()
			_ = m.Secret()
			_ = m.Snapshot()
		}(i)
	}
	wg.Wait()
	if err := m.SetConfigVersion("ver-B"); err != nil {
		t.Fatal(err)
	}
	if m.ConfigVersion() != "ver-B" {
		t.Fatalf("版本锚未更新：%q", m.ConfigVersion())
	}
}

// TestW0303ReviewSuggestions 终审 S1/S2 闭环回归：①RecordV2 对手造 CheckedBy
// 的 V2Check 恒写 server_side_view（视角分流不可经入参绕过）；②ExternalConfirmation.Note
// 疑似明文拒收（字母数字混排 16+ 连续段），普通语句不受影响。
func TestW0303ReviewSuggestions(t *testing.T) {
	m := NewMcpVerifyModel("cfg-hash-1", "SWARM_NODE_PRIME_TOKEN")
	// S1：手造任意视角/任意状态 → 板面与历史 Source 恒 server_side_view
	check := V2Check{Status: "pass", CheckedBy: "user_confirmed"}
	m.RecordV2(check)
	st := m.LayerState(LayerV2)
	if st.CheckedBy != VerifySourceServerSideView {
		t.Fatalf("RecordV2 应恒写 server_side_view, got %q", st.CheckedBy)
	}
	for _, e := range m.History() {
		if e.Layer == LayerV2 && e.Source != VerifySourceServerSideView {
			t.Fatalf("历史 Source 应恒写 server_side_view, got %q", e.Source)
		}
	}
	// S2：Note 含疑似明文 → 拒绝登记
	_, err := m.RegisterExternalConfirmation(ExternalConfirmation{
		Layer: LayerV3, ConfigVersion: "cfg-hash-1", Note: "确认时 token 为 sk-abc123def4567890xyz 有效",
	})
	if err == nil || !strings.Contains(err.Error(), "疑似明文") {
		t.Fatalf("疑似明文 Note 应拒绝登记: %v", err)
	}
	// S2 正向：普通语句（无字母数字混排 16+ 段）不受影响
	if _, err := m.RegisterExternalConfirmation(ExternalConfirmation{
		Layer: LayerV3, ConfigVersion: "cfg-hash-1", Note: "用户在控制台确认宿主工具发现已通过",
	}); err != nil {
		t.Fatalf("普通 Note 不应误拒: %v", err)
	}
}
