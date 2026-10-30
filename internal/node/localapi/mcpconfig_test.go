package localapi

// MCP 配置视图模型与宿主模板（W03-01）fixture 单测：宿主支持级别真相口径、
// env 变量名既有契约逐字锁定、模板占位符形态、无明文凭据金丝（预埋假密钥后
// grep 全部输出面）、服务地址校验与回环标注语义、配置名称冲突正反例与替代
// 建议、P07 §12 六分区字段齐备。全部纯函数级：无路由、无盘上 IO、无网络、
// 无真实凭据（纪律：真 token 不进代码库，测试密钥全为假值）。

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	// cfgFakeNodeCred / cfgFakeHubPAT 假密钥（仅测试，非真实凭据）：金丝断言其
	// 完整形态不得出现在任何输出面；脱敏引用按 RFC §3.1 为前 2 字符 + ********。
	cfgFakeNodeCred = "faketest-node-cred-DO-NOT-LEAK-0123456789abcdef"
	cfgFakeHubPAT   = "smsw_FAKETEST-PAT-DO-NOT-LEAK-9876543210"
)

// cfgNoSecret 金丝断言：out 中不得出现任一假密钥的完整形态（命中即明文泄露，
// 失败输出带命中上下文便于定位泄露点）。
func cfgNoSecret(t *testing.T, where, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if i := strings.Index(out, s); i >= 0 {
			lo, hi := i-40, i+len(s)+40
			if lo < 0 {
				lo = 0
			}
			if hi > len(out) {
				hi = len(out)
			}
			t.Fatalf("%s 不得含明文凭据（金丝 %q 命中）…%s…", where, s, out[lo:hi])
		}
	}
}

// cfgRequireMask 正向对照：脱敏引用必须出现在输出面——证明凭据确经此路径流动
// 且已脱敏，金丝不是因输出为空而虚绿。
func cfgRequireMask(t *testing.T, where, out, masked string) {
	t.Helper()
	if !strings.Contains(out, masked) {
		t.Fatalf("%s 应含脱敏引用 %q（正向对照）: %s", where, masked, out)
	}
}

// TestMcpHostTemplateSupportLevels 宿主支持级别真相口径（任务书锁定二词
// verified_zcode / manual_generic_codex）：zcode=已实测（primewire.go 事实源）、
// codex=通用手动未实测如实标注、未知宿主=通用手动不冒认。全部输出面金丝 +
// 脱敏引用正向对照逐宿主核查。
func TestMcpHostTemplateSupportLevels(t *testing.T) {
	base := func(host string) HostTemplateInput {
		return HostTemplateInput{
			Host: host, Scope: ScopeNode,
			ServerURL:  "http://127.0.0.1:8445",
			McpPath:    "/fake/caller/swarmmind-mcp",
			Credential: cfgFakeNodeCred,
		}
	}
	cases := []struct {
		name         string
		host         string
		wantHost     string
		wantLevel    string
		wantVerified bool
		wantFormat   string
	}{
		{"zcode_已实测适配", "zcode", "zcode", SupportLevelVerifiedZcode, true, "json"},
		{"zcode大小写归一", "ZCode", "zcode", SupportLevelVerifiedZcode, true, "json"},
		{"codex_通用手动未实测", "codex", "codex", SupportLevelManualGenericCodex, false, "toml"},
		{"未知宿主_通用手动", "claude-code", "claude-code", SupportLevelManualGeneric, false, "json"},
		{"空宿主_归unknown", "", "unknown", SupportLevelManualGeneric, false, "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := BuildHostTemplate(base(tc.host))
			if err != nil {
				t.Fatal(err)
			}
			if tmpl.Host != tc.wantHost || tmpl.SupportLevel != tc.wantLevel ||
				tmpl.Verified != tc.wantVerified || tmpl.Format != tc.wantFormat {
				t.Fatalf("宿主真相口径不符: host=%q level=%q verified=%v format=%q, want host=%q level=%q verified=%v format=%q",
					tmpl.Host, tmpl.SupportLevel, tmpl.Verified, tmpl.Format,
					tc.wantHost, tc.wantLevel, tc.wantVerified, tc.wantFormat)
			}
			// 未实测宿主的支持级别词面不得含 verified（不冒认已适配）。
			if !tc.wantVerified && strings.Contains(tmpl.SupportLevel, "verified") {
				t.Fatalf("未实测宿主支持级别不得含 verified 词面: %q", tmpl.SupportLevel)
			}
			if !strings.Contains(tmpl.Note, tc.wantLevel) {
				t.Fatalf("note 应如实标注支持级别 %q: %s", tc.wantLevel, tmpl.Note)
			}
			// 模板与条目全部输出面金丝 + 脱敏引用正向对照。
			cfgNoSecret(t, "host "+tc.wantHost+" snippet", tmpl.Snippet, cfgFakeNodeCred)
			entryJSON, err := json.Marshal(tmpl.Entry)
			if err != nil {
				t.Fatal(err)
			}
			cfgNoSecret(t, "host "+tc.wantHost+" entry", string(entryJSON), cfgFakeNodeCred)
			cfgRequireMask(t, "host "+tc.wantHost+" entry", string(entryJSON), "fa********")
			cfgRequireMask(t, "host "+tc.wantHost+" snippet", tmpl.Snippet, "fa********")
		})
	}

	t.Run("zcode布局以primewire为事实源", func(t *testing.T) {
		tmpl, err := BuildHostTemplate(base("zcode"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(tmpl.ConfigLayout, ".zcode/cli/setting.json") || !strings.Contains(tmpl.ConfigLayout, "mcpServers") {
			t.Fatalf("zcode 布局说明应指向事实源 setting.json 顶层 mcpServers: %s", tmpl.ConfigLayout)
		}
		// Snippet 与 primewire 写盘布局同形：顶层 mcpServers.<名> = {command, env}
		//（wirePrimeConfig 合并写入的正是该结构），且必须是合法 JSON 供直接粘贴。
		var doc struct {
			McpServers map[string]struct {
				Command string            `json:"command"`
				Env     map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(tmpl.Snippet), &doc); err != nil {
			t.Fatalf("zcode snippet 必须是合法 JSON: %v\n%s", err, tmpl.Snippet)
		}
		entry, ok := doc.McpServers[tmpl.EntryName]
		if !ok {
			t.Fatalf("snippet 缺 mcpServers.%s 条目: %s", tmpl.EntryName, tmpl.Snippet)
		}
		if entry.Command != "/fake/caller/swarmmind-mcp" {
			t.Fatalf("entry.command 应为调用机路径原文: %q", entry.Command)
		}
		if _, ok := entry.Env["SWARM_NODE_PRIME_TOKEN"]; !ok {
			t.Fatalf("zcode entry env 缺凭据键: %v", entry.Env)
		}
		if !strings.Contains(tmpl.Note, "已实测适配") || !strings.Contains(tmpl.Note, "primewire") {
			t.Fatalf("zcode note 应写明实测适配与事实源: %s", tmpl.Note)
		}
	})

	t.Run("codex如实标注未验证", func(t *testing.T) {
		tmpl, err := BuildHostTemplate(base("codex"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(tmpl.ConfigLayout, "config.toml") || !strings.Contains(tmpl.ConfigLayout, "mcp_servers") {
			t.Fatalf("codex 布局说明应指向 TOML mcp_servers 段: %s", tmpl.ConfigLayout)
		}
		for _, want := range []string{"[mcp_servers.swarmmind-node]", "command = ", "[mcp_servers.swarmmind-node.env]"} {
			if !strings.Contains(tmpl.Snippet, want) {
				t.Fatalf("codex TOML snippet 缺 %q: %s", want, tmpl.Snippet)
			}
		}
		if !strings.Contains(tmpl.Note, "未实测") || !strings.Contains(tmpl.Note, "不构成支持承诺") {
			t.Fatalf("codex note 应如实标注未实测且无支持承诺: %s", tmpl.Note)
		}
	})

	t.Run("未知宿主不冒认", func(t *testing.T) {
		tmpl, err := BuildHostTemplate(base("claude-code"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(tmpl.Note, "未验证") || !strings.Contains(tmpl.Note, "不冒认") {
			t.Fatalf("未知宿主 note 应标注未验证不冒认: %s", tmpl.Note)
		}
	})
}

// TestMcpHostTemplateEnvContract env 变量名既有契约逐字锁定（PRODUCT-PLAN
// §12.3：绝不重命名）：node=SWARM_TARGET/SWARM_NODE_URL/SWARM_NODE_PRIME_TOKEN、
// hub=SWARM_TARGET/SWARM_HUB_URL/SWARM_HUB_TOKEN；SWARM_TARGET 值随范围、地址
// 原样进入、凭据位恒脱敏。
func TestMcpHostTemplateEnvContract(t *testing.T) {
	cases := []struct {
		name     string
		scope    string
		wantKeys []string
		wantMask string
		fake     string
	}{
		{"node范围", ScopeNode, []string{"SWARM_TARGET", "SWARM_NODE_URL", "SWARM_NODE_PRIME_TOKEN"}, "fa********", cfgFakeNodeCred},
		{"hub范围", ScopeHub, []string{"SWARM_TARGET", "SWARM_HUB_URL", "SWARM_HUB_TOKEN"}, "sm********", cfgFakeHubPAT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := BuildHostTemplate(HostTemplateInput{
				Host: "zcode", Scope: tc.scope,
				ServerURL: "http://127.0.0.1:8445",
				McpPath:   "/fake/mcp", Credential: tc.fake,
			})
			if err != nil {
				t.Fatal(err)
			}
			env, ok := tmpl.Entry["env"].(map[string]string)
			if !ok {
				t.Fatalf("entry.env 缺失或类型不符: %T", tmpl.Entry["env"])
			}
			if len(env) != len(tc.wantKeys) {
				t.Fatalf("env 应恰 %d 键, got %d: %v", len(tc.wantKeys), len(env), env)
			}
			for _, k := range tc.wantKeys {
				if _, ok := env[k]; !ok {
					t.Fatalf("env 缺既有契约键 %q（变量名绝不重命名）: %v", k, env)
				}
			}
			if env["SWARM_TARGET"] != tc.scope {
				t.Fatalf("SWARM_TARGET 应为 %q: %q", tc.scope, env["SWARM_TARGET"])
			}
			if env[tc.wantKeys[1]] != "http://127.0.0.1:8445" {
				t.Fatalf("服务地址应原样进入 env: %q", env[tc.wantKeys[1]])
			}
			if env[tc.wantKeys[2]] != tc.wantMask {
				t.Fatalf("凭据位应为脱敏引用 %q: %q", tc.wantMask, env[tc.wantKeys[2]])
			}
			// 逐字存在断言：序列化后契约变量名逐字出现；金丝 + 正向对照。
			envJSON, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range tc.wantKeys {
				if !strings.Contains(string(envJSON), `"`+k+`"`) {
					t.Fatalf("env JSON 缺契约变量名 %q: %s", k, envJSON)
				}
			}
			cfgNoSecret(t, tc.scope+" env", string(envJSON), tc.fake)
			cfgRequireMask(t, tc.scope+" env", string(envJSON), tc.wantMask)
		})
	}

	t.Run("默认名按范围后缀区分", func(t *testing.T) {
		if DefaultConfigName(ScopeNode) != "swarmmind-node" || DefaultConfigName(ScopeHub) != "swarmmind-hub" {
			t.Fatalf("默认配置名应区分本机/中心: node=%q hub=%q",
				DefaultConfigName(ScopeNode), DefaultConfigName(ScopeHub))
		}
		if DefaultConfigName("bad") != "" {
			t.Fatalf("未知范围默认名应为空串, got %q", DefaultConfigName("bad"))
		}
	})
}

// TestMcpHostTemplatePlaceholderForms 模板占位符形态：无路径=不代填待填写占位、
// 无凭据=明示占位文案（预览不可直接调用语义）、有凭据=只出脱敏引用；配置名
// 缺省/自定义口径；同输入生成逐字节确定（fixture 可锁定）。
func TestMcpHostTemplatePlaceholderForms(t *testing.T) {
	t.Run("无路径_占位不代填", func(t *testing.T) {
		if !strings.Contains(mcpCallerPathPlaceholder, "不代填") || !strings.Contains(mcpCallerPathPlaceholder, "待填写") {
			t.Fatalf("路径占位符应写明不代填待填写: %q", mcpCallerPathPlaceholder)
		}
		for _, host := range []string{"zcode", "codex"} {
			tmpl, err := BuildHostTemplate(HostTemplateInput{
				Host: host, Scope: ScopeNode,
				ServerURL: "http://127.0.0.1:8445", Credential: cfgFakeNodeCred,
			})
			if err != nil {
				t.Fatal(err)
			}
			if cmd, _ := tmpl.Entry["command"].(string); cmd != mcpCallerPathPlaceholder {
				t.Fatalf("[%s] 无路径时 command 应为占位符: %q", host, cmd)
			}
			cfgRequireMask(t, "host "+host+" snippet", tmpl.Snippet, mcpCallerPathPlaceholder)
		}
	})
	t.Run("无凭据_明示占位不含明文", func(t *testing.T) {
		if !strings.Contains(templateCredentialPlaceholder, "凭据占位") || !strings.Contains(templateCredentialPlaceholder, "不含明文") {
			t.Fatalf("凭据占位文案应写明占位且不含明文: %q", templateCredentialPlaceholder)
		}
		for _, host := range []string{"zcode", "codex"} {
			tmpl, err := BuildHostTemplate(HostTemplateInput{
				Host: host, Scope: ScopeNode,
				ServerURL: "http://127.0.0.1:8445", McpPath: "/fake/mcp",
			})
			if err != nil {
				t.Fatal(err)
			}
			cfgRequireMask(t, "host "+host+" snippet", tmpl.Snippet, templateCredentialPlaceholder)
		}
	})
	t.Run("假凭据只出脱敏引用", func(t *testing.T) {
		for _, host := range []string{"zcode", "codex"} {
			tmpl, err := BuildHostTemplate(HostTemplateInput{
				Host: host, Scope: ScopeNode,
				ServerURL: "http://127.0.0.1:8445", McpPath: "/fake/mcp",
				Credential: cfgFakeNodeCred,
			})
			if err != nil {
				t.Fatal(err)
			}
			cfgNoSecret(t, "host "+host+" snippet", tmpl.Snippet, cfgFakeNodeCred)
			cfgRequireMask(t, "host "+host+" snippet", tmpl.Snippet, "fa********")
		}
	})
	t.Run("配置名默认与自定义", func(t *testing.T) {
		tmpl, err := BuildHostTemplate(HostTemplateInput{
			Host: "zcode", Scope: ScopeNode, ServerURL: "http://127.0.0.1:8445",
		})
		if err != nil {
			t.Fatal(err)
		}
		if tmpl.EntryName != "swarmmind-node" {
			t.Fatalf("缺省配置名应 swarmmind-node: %q", tmpl.EntryName)
		}
		tmpl, err = BuildHostTemplate(HostTemplateInput{
			Host: "zcode", Scope: ScopeNode, ServerURL: "http://127.0.0.1:8445",
			ConfigName: "  my-entry  ",
		})
		if err != nil {
			t.Fatal(err)
		}
		if tmpl.EntryName != "my-entry" {
			t.Fatalf("自定义配置名应去首尾空白: %q", tmpl.EntryName)
		}
		cfgRequireMask(t, "snippet 键", tmpl.Snippet, `"my-entry"`)
	})
	t.Run("同输入逐字节确定", func(t *testing.T) {
		in := HostTemplateInput{
			Host: "zcode", Scope: ScopeHub, ServerURL: "https://hub.example.com",
			McpPath: "/fake/mcp", Credential: cfgFakeHubPAT,
		}
		a, err := BuildHostTemplate(in)
		if err != nil {
			t.Fatal(err)
		}
		b, err := BuildHostTemplate(in)
		if err != nil {
			t.Fatal(err)
		}
		if a.Snippet != b.Snippet {
			t.Fatalf("同一输入两次生成应逐字节一致（fixture 可锁定）:\n%s\n---\n%s", a.Snippet, b.Snippet)
		}
	})
}

// TestMcpConfigNoPlaintextCredentialCanary 无明文凭据金丝（凭据纪律出口锁定）：
// 预埋假密钥跑遍 范围×调用环境×宿主×有/无凭据 组合，整个视图序列化 JSON 与
// 两形态模板串不得出现任一假密钥完整形态；正向对照保证脱敏引用确在输出面
// （金丝不因空输出虚绿）。冲突检测结果同样不得回带既有条目内的明文凭据。
func TestMcpConfigNoPlaintextCredentialCanary(t *testing.T) {
	secrets := []string{cfgFakeNodeCred, cfgFakeHubPAT}
	for _, scope := range []string{ScopeNode, ScopeHub} {
		fake := cfgFakeNodeCred
		wantMask := "fa********"
		if scope == ScopeHub {
			fake, wantMask = cfgFakeHubPAT, "sm********"
		}
		for _, caller := range []string{CallerSameMachine, CallerRemote} {
			for _, host := range []string{"zcode", "codex", "unknown-host"} {
				for _, cred := range []string{fake, ""} {
					view, err := BuildMcpConfigView(McpConfigInput{
						Scope: scope, CallerEnv: caller, Host: host,
						ServerURL:     "http://10.0.0.1:8445",
						CallerMcpPath: "/fake/caller/swarmmind-mcp",
						Credential:    cred,
					})
					if err != nil {
						t.Fatal(err)
					}
					full, err := json.Marshal(view)
					if err != nil {
						t.Fatal(err)
					}
					where := scope + "/" + caller + "/" + host
					cfgNoSecret(t, "view "+where, string(full), secrets...)
					cfgNoSecret(t, "snippet "+where, view.Template.Snippet, secrets...)
					if cred != "" {
						cfgRequireMask(t, "view "+where, string(full), wantMask)
					}
				}
			}
		}
	}

	t.Run("冲突结果不回带既有条目明文", func(t *testing.T) {
		existing := map[string]ExistingMcpEntry{
			"swarmmind-node": {
				Command: "/old/swarmmind-mcp",
				// 模拟真实宿主配置内的明文凭据（primewire 写盘含 prime_token 明文）
				Env: map[string]string{"SWARM_TARGET": "node", "SWARM_NODE_PRIME_TOKEN": cfgFakeNodeCred},
			},
		}
		res := DetectConfigNameConflict(ScopeNode, "", existing)
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		cfgNoSecret(t, "conflict result", string(raw), cfgFakeNodeCred)
	})
}

// TestValidateServiceURL 服务地址校验（P07 §12：校验 scheme 与格式；纯校验不
// 发起网络请求）：合法形态放行、缺 scheme/坏 scheme/缺 host/解析失败/含空格
// 一律拒绝，错误信息点名缺项。
func TestValidateServiceURL(t *testing.T) {
	for _, ok := range []string{
		"http://127.0.0.1:8445",
		"https://hub.example.com",
		"http://[::1]:8445",
		"https://hub.example.com/api",
		"HTTP://10.0.0.1:8445", // scheme 大小写归一后合法
	} {
		if err := ValidateServiceURL(ok); err != nil {
			t.Fatalf("合法地址 %q 不应报错: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",                    // 必填
		"   ",                 // 纯空白
		"example.com",         // 缺 scheme
		"ftp://x:1",           // scheme 非法
		"http://",             // 缺 host:port
		"://x",                // 解析失败
		"http://exa mple.com", // host 含空格
		"not a url",           // 解析失败
	} {
		if err := ValidateServiceURL(bad); err == nil {
			t.Fatalf("非法地址 %q 应被拒绝", bad)
		}
	}
	if err := ValidateServiceURL("ftp://x:1"); !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("scheme 错误应点名 scheme: %v", err)
	}
	if err := ValidateServiceURL("http://"); !strings.Contains(err.Error(), "host") {
		t.Fatalf("缺 host 错误应点名 host: %v", err)
	}
	if err := ValidateServiceURL(""); !strings.Contains(err.Error(), "必填") {
		t.Fatalf("空地址错误应点名必填: %v", err)
	}
}

// TestLoopbackServiceSemantics 回环标注语义（AC07：错误回环地址不得显示为外部
// 已验证）：远端调用机+回环地址=危险拓扑必须显式警告；非回环不误报；同机回环
// 是默认正确拓扑不警告；远端一律不提供 auto_write（禁止远程写调用者文件）。
func TestLoopbackServiceSemantics(t *testing.T) {
	t.Run("回环判定", func(t *testing.T) {
		cases := []struct {
			raw  string
			want bool
		}{
			{"http://127.0.0.1:8445", true},
			{"http://localhost:8445", true},
			{"http://[::1]:8445", true},
			{"http://127.200.1.2:9", true},
			// 终审建议修+边缘形态：大小写不归一曾漏 LOCALHOST；IPv4-mapped/zone
			// 后缀/0.0.0.0 三形态顺带覆盖（均属回环，漏警告=AC07 advisory 缺口）。
			{"http://LOCALHOST:8445", true},
			{"http://[::ffff:127.0.0.1]:8445", true},
			{"http://[::1%25eth0]:8445", true},
			{"http://0.0.0.0:8445", true},
			{"http://10.0.0.1:8445", false},
			{"https://hub.example.com", false},
			{"http://[::ffff:10.0.0.1]:8445", false}, // mapped 非回环不误报
			{"not a url", false},
		}
		for _, tc := range cases {
			if got := isLoopbackServiceURL(tc.raw); got != tc.want {
				t.Fatalf("isLoopbackServiceURL(%q)=%v, want %v", tc.raw, got, tc.want)
			}
		}
	})
	t.Run("远端回环必须显式警告", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerRemote, Host: "zcode",
			ServerURL: "http://127.0.0.1:8445", CallerMcpPath: "/fake/mcp",
		})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(view.Notes, "\n")
		for _, want := range []string{"回环", "127.0.0.1", "调用机"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("远端+回环 notes 应含 %q 警告: %s", want, joined)
			}
		}
		for _, f := range view.GenerationForms {
			if f == GenFormAutoWrite {
				t.Fatalf("远端不得提供 auto_write（禁止远程写调用者文件）: %v", view.GenerationForms)
			}
		}
	})
	t.Run("远端非回环不误报", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerRemote, Host: "zcode",
			ServerURL: "http://10.0.0.1:8445", CallerMcpPath: "/fake/mcp",
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(view.Notes, "\n"), "回环") {
			t.Fatalf("非回环地址不得误报回环警告: %v", view.Notes)
		}
	})
	t.Run("同机回环是默认正确拓扑不警告", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerSameMachine, Host: "zcode",
			ServerURL: "http://127.0.0.1:8445", CallerMcpPath: "/fake/mcp",
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(view.Notes, "\n"), "回环") {
			t.Fatalf("同机回环是正确拓扑，不得警告: %v", view.Notes)
		}
	})
}

// TestDetectConfigNameConflict 配置名称冲突模型：正例（同名同范围/跨范围/范围
// 未知）一律 Conflict=true 并给替代建议与人话处置——绝不建议静默覆盖；反例
// （无同名/默认名错开）Conflict=false；建议名 <名>-N 跳过占用。
func TestDetectConfigNameConflict(t *testing.T) {
	nodeEntry := ExistingMcpEntry{
		Command: "/old/swarmmind-mcp",
		Env:     map[string]string{"SWARM_TARGET": "node"},
	}

	t.Run("无冲突_直接可用", func(t *testing.T) {
		res := DetectConfigNameConflict(ScopeNode, "", map[string]ExistingMcpEntry{"other": nodeEntry})
		if res.Conflict || res.Kind != "" || res.ExistingScope != "" || res.SuggestedName != "" || res.Resolution != "" {
			t.Fatalf("无冲突应零值回执: %+v", res)
		}
		if res.RequestedName != "swarmmind-node" {
			t.Fatalf("空名应按默认名补齐: %q", res.RequestedName)
		}
	})
	t.Run("同范围冲突_改名或显式替换", func(t *testing.T) {
		res := DetectConfigNameConflict(ScopeNode, "", map[string]ExistingMcpEntry{"swarmmind-node": nodeEntry})
		if !res.Conflict || res.Kind != ConflictKindSameScope || res.ExistingScope != ScopeNode {
			t.Fatalf("同范围冲突判定不符: %+v", res)
		}
		if res.ExistingCommand != "/old/swarmmind-mcp" {
			t.Fatalf("应回带既有 command 供差异辨认: %q", res.ExistingCommand)
		}
		if res.SuggestedName != "swarmmind-node-2" {
			t.Fatalf("建议名应为 <名>-2: %q", res.SuggestedName)
		}
		if !strings.Contains(res.Resolution, "不静默覆盖") || !strings.Contains(res.Resolution, "swarmmind-node-2") {
			t.Fatalf("resolution 应写明不静默覆盖并给建议名: %s", res.Resolution)
		}
	})
	t.Run("跨范围撞名_建议并存", func(t *testing.T) {
		hubEntry := ExistingMcpEntry{Command: "/old/mcp", Env: map[string]string{"SWARM_TARGET": "hub"}}
		res := DetectConfigNameConflict(ScopeNode, "swarmmind-node", map[string]ExistingMcpEntry{"swarmmind-node": hubEntry})
		if !res.Conflict || res.Kind != ConflictKindCrossScope || res.ExistingScope != ScopeHub {
			t.Fatalf("跨范围冲突判定不符: %+v", res)
		}
		if !strings.Contains(res.Resolution, "并存") || !strings.Contains(res.Resolution, "显式确认") {
			t.Fatalf("跨范围 resolution 应给并存建议与显式确认口径: %s", res.Resolution)
		}
	})
	t.Run("范围未知_如实标注", func(t *testing.T) {
		legacy := ExistingMcpEntry{Command: "/legacy/mcp", Env: map[string]string{"OTHER": "x"}}
		res := DetectConfigNameConflict(ScopeNode, "swarmmind-node", map[string]ExistingMcpEntry{"swarmmind-node": legacy})
		if !res.Conflict || res.Kind != ConflictKindUnknownScope || res.ExistingScope != "unknown" {
			t.Fatalf("范围未知判定不符: %+v", res)
		}
		if !strings.Contains(res.Resolution, "不静默覆盖") {
			t.Fatalf("范围未知 resolution 应同样不静默覆盖: %s", res.Resolution)
		}
	})
	t.Run("建议名跳过占用", func(t *testing.T) {
		existing := map[string]ExistingMcpEntry{
			"swarmmind-node":   nodeEntry,
			"swarmmind-node-2": nodeEntry,
		}
		res := DetectConfigNameConflict(ScopeNode, "", existing)
		if res.SuggestedName != "swarmmind-node-3" {
			t.Fatalf("建议名应跳过已占用 -2: %+v", res)
		}
	})
	t.Run("本机中心默认名天然错开", func(t *testing.T) {
		existing := map[string]ExistingMcpEntry{"swarmmind-node": nodeEntry}
		if res := DetectConfigNameConflict(ScopeHub, "", existing); res.Conflict {
			t.Fatalf("中心默认名不应与本机默认名冲突（默认区分并存）: %+v", res)
		}
	})
	t.Run("自定义名去空白后比对", func(t *testing.T) {
		res := DetectConfigNameConflict(ScopeNode, "  swarmmind-node  ", map[string]ExistingMcpEntry{"swarmmind-node": nodeEntry})
		if !res.Conflict || res.RequestedName != "swarmmind-node" {
			t.Fatalf("自定义名应去首尾空白后比对: %+v", res)
		}
	})
}

// TestBuildMcpConfigViewSixSections P07 §12 六分区字段齐备：①范围/拓扑
// ②调用环境（宿主真相/调用机路径状态/服务地址）③人工授权（脱敏引用+env 变量
// 名）④名称与生成形态（默认区分本机中心；auto_write 仅 node+同机+已验证宿主）。
// ⑤分层验证/⑥维护故障属 W03-03/04 交接面，本模型不携带验证结果字段（如实
// 登记边界）。另锁：路径状态四态、非法枚举不猜测、路径三处表示逐字节一致。
func TestBuildMcpConfigViewSixSections(t *testing.T) {
	t.Run("node同机zcode全字段", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerSameMachine, Host: "zcode",
			ServerURL:     "http://127.0.0.1:8445",
			CallerMcpPath: "/fake/caller/swarmmind-mcp", PathProbedLocally: true,
			Credential: cfgFakeNodeCred,
		})
		if err != nil {
			t.Fatal(err)
		}
		// ① 范围/拓扑
		if view.TargetScope != ScopeNode || view.TargetScopeLabel != "调用本机" {
			t.Fatalf("①范围不符: %q/%q", view.TargetScope, view.TargetScopeLabel)
		}
		// ② 调用环境（含宿主类型/支持级别/路径状态/服务地址）
		if view.CallerEnv != CallerSameMachine || view.CallerEnvLabel != "与目标同机" {
			t.Fatalf("②调用环境不符: %q/%q", view.CallerEnv, view.CallerEnvLabel)
		}
		if view.HostKind != "zcode" || view.HostSupportLevel != SupportLevelVerifiedZcode || !view.HostVerified {
			t.Fatalf("②宿主真相口径不符: %q/%q/%v", view.HostKind, view.HostSupportLevel, view.HostVerified)
		}
		if view.McpCommandPath != "/fake/caller/swarmmind-mcp" || view.McpPathStatus != PathStatusLocalProbed {
			t.Fatalf("②路径/状态不符: %q/%q", view.McpCommandPath, view.McpPathStatus)
		}
		if view.ServiceURL != "http://127.0.0.1:8445" {
			t.Fatalf("②服务地址应原样回显: %q", view.ServiceURL)
		}
		// ③ 人工授权（恒脱敏）
		if view.CredentialRef != "fa********" {
			t.Fatalf("③凭据引用应为脱敏: %q", view.CredentialRef)
		}
		if view.CredentialEnvVar != "SWARM_NODE_PRIME_TOKEN" {
			t.Fatalf("③凭据 env 变量名不符: %q", view.CredentialEnvVar)
		}
		// ④ 名称与生成形态（node+同机+已验证 → 恰四形态）
		if view.ConfigName != "swarmmind-node" || !view.ConfigNameIsDefault {
			t.Fatalf("④默认名口径不符: %q/%v", view.ConfigName, view.ConfigNameIsDefault)
		}
		wantForms := map[string]bool{GenFormPreview: true, GenFormCopy: true, GenFormDownload: true, GenFormAutoWrite: true}
		if len(view.GenerationForms) != len(wantForms) {
			t.Fatalf("④node+同机+已验证应四形态: %v", view.GenerationForms)
		}
		for _, f := range view.GenerationForms {
			if !wantForms[f] {
				t.Fatalf("④未知生成形态 %q: %v", f, view.GenerationForms)
			}
			delete(wantForms, f)
		}
		if len(wantForms) != 0 {
			t.Fatalf("④缺生成形态 %v（got %v）", wantForms, view.GenerationForms)
		}
		// 模板随行：条目 command 与视图路径一致、配置名同键、凭据位脱敏、snippet 非空。
		if cmd, _ := view.Template.Entry["command"].(string); cmd != view.McpCommandPath {
			t.Fatalf("模板条目 command 应与视图路径一致: %q vs %q", cmd, view.McpCommandPath)
		}
		env, ok := view.Template.Entry["env"].(map[string]string)
		if !ok || env["SWARM_NODE_PRIME_TOKEN"] != "fa********" {
			t.Fatalf("模板凭据位应为脱敏引用: %v", view.Template.Entry["env"])
		}
		if view.Template.Snippet == "" || view.Template.EntryName != view.ConfigName {
			t.Fatalf("模板不全: %+v", view.Template)
		}
		// notes：凭据纪律必在（人话口径）。
		if len(view.Notes) == 0 || !strings.Contains(strings.Join(view.Notes, "\n"), "明文凭据") {
			t.Fatalf("notes 应携带凭据纪律说明: %v", view.Notes)
		}
	})

	t.Run("hub同机zcode中心口径", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeHub, CallerEnv: CallerSameMachine, Host: "zcode",
			ServerURL:     "https://hub.example.com",
			CallerMcpPath: "/fake/caller/swarmmind-mcp", PathProbedLocally: true,
			Credential: cfgFakeHubPAT,
		})
		if err != nil {
			t.Fatal(err)
		}
		if view.TargetScopeLabel != "调用中心" || view.CredentialEnvVar != "SWARM_HUB_TOKEN" {
			t.Fatalf("中心口径不符: %q/%q", view.TargetScopeLabel, view.CredentialEnvVar)
		}
		if view.CredentialRef != "sm********" || view.ConfigName != "swarmmind-hub" {
			t.Fatalf("中心凭据引用/默认名不符: %q/%q", view.CredentialRef, view.ConfigName)
		}
		if len(view.GenerationForms) != 3 {
			t.Fatalf("hub 范围永不自动写，应恰三形态: %v", view.GenerationForms)
		}
		joined := strings.Join(view.Notes, "\n")
		if !strings.Contains(joined, "PAT") || !strings.Contains(joined, "不自动写") {
			t.Fatalf("中心 notes 应写明 PAT 口径与不自动写: %s", joined)
		}
	})

	t.Run("路径状态四态", func(t *testing.T) {
		cases := []struct {
			name       string
			path       string
			probed     bool
			caller     string
			wantStatus string
			wantPath   string
		}{
			{"空路径_占位", "", false, CallerSameMachine, PathStatusPlaceholder, mcpCallerPathPlaceholder},
			{"同机未探测_用户自填", "/fake/mcp", false, CallerSameMachine, PathStatusUserProvided, "/fake/mcp"},
			{"同机已探测", "/fake/mcp", true, CallerSameMachine, PathStatusLocalProbed, "/fake/mcp"},
			{"远端_强制待外部验证", "/fake/mcp", true, CallerRemote, PathStatusExternalUnverified, "/fake/mcp"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				view, err := BuildMcpConfigView(McpConfigInput{
					Scope: ScopeNode, CallerEnv: tc.caller, Host: "zcode",
					ServerURL:     "http://127.0.0.1:8445",
					CallerMcpPath: tc.path, PathProbedLocally: tc.probed,
				})
				if err != nil {
					t.Fatal(err)
				}
				if view.McpPathStatus != tc.wantStatus || view.McpCommandPath != tc.wantPath {
					t.Fatalf("路径状态不符: got %q/%q, want %q/%q",
						view.McpPathStatus, view.McpCommandPath, tc.wantStatus, tc.wantPath)
				}
			})
		}
	})

	t.Run("生成形态矩阵_auto_write仅node同机已验证", func(t *testing.T) {
		cases := []struct {
			name     string
			scope    string
			caller   string
			host     string
			wantAuto bool
		}{
			{"node同机zcode", ScopeNode, CallerSameMachine, "zcode", true},
			{"node同机codex未验证宿主", ScopeNode, CallerSameMachine, "codex", false},
			{"node同机未知宿主", ScopeNode, CallerSameMachine, "weird-host", false},
			{"node远端zcode禁远程写", ScopeNode, CallerRemote, "zcode", false},
			{"hub同机zcode永不自动写", ScopeHub, CallerSameMachine, "zcode", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				view, err := BuildMcpConfigView(McpConfigInput{
					Scope: tc.scope, CallerEnv: tc.caller, Host: tc.host,
					ServerURL: "http://127.0.0.1:8445", CallerMcpPath: "/fake/mcp",
				})
				if err != nil {
					t.Fatal(err)
				}
				has := false
				for _, f := range view.GenerationForms {
					if f == GenFormAutoWrite {
						has = true
					}
				}
				if has != tc.wantAuto {
					t.Fatalf("auto_write 有无不符: forms=%v wantAuto=%v", view.GenerationForms, tc.wantAuto)
				}
			})
		}
	})

	t.Run("非法枚举与服务地址不猜测", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   McpConfigInput
		}{
			{"范围空", McpConfigInput{CallerEnv: CallerSameMachine, ServerURL: "http://127.0.0.1:1"}},
			{"范围未知值", McpConfigInput{Scope: "local", CallerEnv: CallerSameMachine, ServerURL: "http://127.0.0.1:1"}},
			{"调用环境空", McpConfigInput{Scope: ScopeNode, ServerURL: "http://127.0.0.1:1"}},
			{"调用环境未知值", McpConfigInput{Scope: ScopeNode, CallerEnv: "same", ServerURL: "http://127.0.0.1:1"}},
			{"服务地址空", McpConfigInput{Scope: ScopeNode, CallerEnv: CallerSameMachine}},
			{"服务地址scheme非法", McpConfigInput{Scope: ScopeNode, CallerEnv: CallerSameMachine, ServerURL: "ftp://x:1"}},
		} {
			if _, err := BuildMcpConfigView(tc.in); err == nil {
				t.Fatalf("%s 应报错不猜测: %+v", tc.name, tc.in)
			}
		}
	})

	t.Run("自定义配置名", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerSameMachine, Host: "zcode",
			ServerURL: "http://127.0.0.1:8445", CallerMcpPath: "/fake/mcp",
			ConfigName: "swarmmind-node-2",
		})
		if err != nil {
			t.Fatal(err)
		}
		if view.ConfigName != "swarmmind-node-2" || view.ConfigNameIsDefault {
			t.Fatalf("自定义名口径不符: %q/%v", view.ConfigName, view.ConfigNameIsDefault)
		}
		if view.Template.EntryName != view.ConfigName {
			t.Fatalf("模板键应随自定义名: %q", view.Template.EntryName)
		}
	})

	t.Run("路径首尾空白三处表示一致", func(t *testing.T) {
		view, err := BuildMcpConfigView(McpConfigInput{
			Scope: ScopeNode, CallerEnv: CallerSameMachine, Host: "zcode",
			ServerURL:     "http://127.0.0.1:8445",
			CallerMcpPath: "  /fake/caller/swarmmind-mcp  ",
		})
		if err != nil {
			t.Fatal(err)
		}
		if view.McpCommandPath != "/fake/caller/swarmmind-mcp" {
			t.Fatalf("视图路径应去首尾空白: %q", view.McpCommandPath)
		}
		if cmd, _ := view.Template.Entry["command"].(string); cmd != view.McpCommandPath {
			t.Fatalf("条目 command 应与视图路径逐字节一致: %q vs %q", cmd, view.McpCommandPath)
		}
		if !strings.Contains(view.Template.Snippet, `"command": "/fake/caller/swarmmind-mcp"`) {
			t.Fatalf("snippet 内 command 应与视图路径逐字节一致: %s", view.Template.Snippet)
		}
	})
}
