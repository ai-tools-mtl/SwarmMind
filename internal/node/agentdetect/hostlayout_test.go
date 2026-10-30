// 宿主配置布局识别测试（全 fake：t.TempDir() 造文件，绝不读真实用户 HOME）。
package agentdetect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNow 固定 UTC 时间源（时间序列化纪律：断言不依赖墙钟）。
func testNow() time.Time { return time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC) }

// mkdirAllWithFix t.TempDir 下的 MkdirAll（测试辅助，无权限复杂度）。
func mkdirAllWithFix(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("建目录失败 %s: %v", path, err)
	}
}

// writeFileWithFix 写文件（测试辅助）。
func writeFileWithFix(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写文件失败 %s: %v", path, err)
	}
}

// makeZcodeHome 造 zcode 配置 home（含合法 setting.json 与身份键）。
func makeZcodeHome(t *testing.T, settingJSON string) string {
	t.Helper()
	home := t.TempDir()
	mkdirAllWithFix(t, filepath.Join(home, ".zcode", "cli"))
	writeFileWithFix(t, filepath.Join(home, ".zcode", "cli", "setting.json"), settingJSON)
	return home
}

// makeCodexHome 造 codex 配置 home（config.toml + auth.json 可选）。
func makeCodexHome(t *testing.T, configToml string, withAuth bool) string {
	t.Helper()
	home := t.TempDir()
	mkdirAllWithFix(t, filepath.Join(home, ".codex"))
	writeFileWithFix(t, filepath.Join(home, ".codex", "config.toml"), configToml)
	if withAuth {
		writeFileWithFix(t, filepath.Join(home, ".codex", "auth.json"), `{"fake":"credential-content-never-read"}`)
	}
	return home
}

// TestZcodeSettingRecognized 合法 JSON + 身份键：结构识别过 + 标记发现。
func TestZcodeSettingRecognized(t *testing.T) {
	home := makeZcodeHome(t, `{"auth":{"token":"FAKE-SECRET-x"},"theme":"dark"}`)
	lc, marker := recognizeConfig(HostZcode, home, testNow())
	if lc.Status != StatusPass {
		t.Fatalf("结构应识别通过，实得 %+v", lc)
	}
	if lc.Source != "known_layout:zcode" {
		t.Fatalf("来源应标注已知布局，实得 %q", lc.Source)
	}
	if !marker.Checked || !marker.Found {
		t.Fatalf("身份键应被发现，实得 %+v", marker)
	}
	if !strings.Contains(marker.Note, "auth") {
		t.Fatalf("标记注记应指明命中键，实得 %q", marker.Note)
	}
	if lc.Paths[0].Path != filepath.Join(home, ".zcode", "cli", "setting.json") {
		t.Fatalf("应回引用路径，实得 %+v", lc.Paths)
	}
	if lc.Paths[0].Symlink {
		t.Fatalf("普通文件不得注记符号链接，实得 %+v", lc.Paths)
	}
}

// TestZcodeSettingBrokenTable 损坏/非法结构：如实 fail + 原因；标记核查不执行
// （不给损坏配置背书）。
func TestZcodeSettingBrokenTable(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantReason string
	}{
		{"非JSON", "not json at all", "非合法 JSON"},
		{"顶层null", "null", "顶层为 null"},
		{"尾部多余", `{} garbage`, "尾部有多余内容"},
		{"顶层数组", `[1,2]`, "非合法 JSON"},
		{"空文件", "", "空文件"},
		{"纯空白", "   \n\t ", "空文件"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := makeZcodeHome(t, tc.content)
			lc, marker := recognizeConfig(HostZcode, home, testNow())
			if lc.Status != StatusFail {
				t.Fatalf("应判结构不可识别，实得 %+v", lc)
			}
			if !strings.Contains(lc.Reason, tc.wantReason) {
				t.Fatalf("原因应含 %q，实得 %q", tc.wantReason, lc.Reason)
			}
			if marker.Checked {
				t.Fatalf("结构未识别不得核标记（不给损坏配置背书），实得 %+v", marker)
			}
		})
	}
}

// TestZcodeSettingEmptyObjectNoMarker 空对象：结构合法但无身份键 → 识别过、
// 标记未发现（ExecutionReady 由派生层负责不亮）。
func TestZcodeSettingEmptyObjectNoMarker(t *testing.T) {
	home := makeZcodeHome(t, `{}`)
	lc, marker := recognizeConfig(HostZcode, home, testNow())
	if lc.Status != StatusPass {
		t.Fatalf("空对象结构应可识别，实得 %+v", lc)
	}
	if !marker.Checked || marker.Found {
		t.Fatalf("无身份键应如实记标记未发现，实得 %+v", marker)
	}
	if !strings.Contains(marker.Note, "保守清单") {
		t.Fatalf("注记应说明保守清单口径，实得 %q", marker.Note)
	}
}

// TestCodexConfigRecognized 合法 TOML 形态 + auth.json：启发式识别过 + 标记发现，
// 内容永不进结果。
func TestCodexConfigRecognized(t *testing.T) {
	home := makeCodexHome(t, "# codex config\nmodel = \"gpt-5\"\n[profiles.default]\napproval = \"never\"\n", true)
	lc, marker := recognizeConfig(HostCodex, home, testNow())
	if lc.Status != StatusPass {
		t.Fatalf("结构应识别通过，实得 %+v", lc)
	}
	if !strings.Contains(lc.Reason, "启发式") {
		t.Fatalf("应如实注记启发式口径，实得 %q", lc.Reason)
	}
	if !marker.Checked || !marker.Found {
		t.Fatalf("auth.json 存在应记标记发现，实得 %+v", marker)
	}
	if !strings.Contains(marker.Note, "未验证") {
		t.Fatalf("标记注记必须声明有效性未验证，实得 %q", marker.Note)
	}
}

// TestCodexConfigBrokenTable 损坏结构（二进制/纯注释/空文件）：如实 fail。
func TestCodexConfigBrokenTable(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantReason string
	}{
		{"二进制NUL", "\x00\x01garbage", "NUL"},
		{"纯注释", "# 只有注释\n# 没有结构行\n", "结构特征"},
		{"空文件", "", "空文件"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := makeCodexHome(t, tc.content, false)
			lc, _ := recognizeConfig(HostCodex, home, testNow())
			if lc.Status != StatusFail {
				t.Fatalf("应判结构不可识别，实得 %+v", lc)
			}
			if !strings.Contains(lc.Reason, tc.wantReason) {
				t.Fatalf("原因应含 %q，实得 %q", tc.wantReason, lc.Reason)
			}
		})
	}
}

// TestCodexMissingAuthMarker 配置合法但无登录标记：识别过、标记未发现
// （结构可识别 ≠ 登录成功）。
func TestCodexMissingAuthMarker(t *testing.T) {
	home := makeCodexHome(t, "model = \"gpt\"\n", false)
	lc, marker := recognizeConfig(HostCodex, home, testNow())
	if lc.Status != StatusPass {
		t.Fatalf("结构应识别通过，实得 %+v", lc)
	}
	if !marker.Checked || marker.Found {
		t.Fatalf("无 auth.json 应如实记标记未发现，实得 %+v", marker)
	}
	if !strings.Contains(marker.Note, "不存在") {
		t.Fatalf("注记应说明 auth.json 不存在，实得 %q", marker.Note)
	}
}

// TestConfigSymlinkNotFollowed 配置路径为符号链接：不跟随、不读取、如实注记
// （两宿主对照）。
func TestConfigSymlinkNotFollowed(t *testing.T) {
	t.Run("zcode", func(t *testing.T) {
		// 真实文件放在别处，内含身份键与假密钥——若跟随读取即泄漏（防泄漏
		// 测试另有序列化断言，此处断言根本不读）。
		outside := t.TempDir()
		real := filepath.Join(outside, "real-setting.json")
		writeFileWithFix(t, real, `{"auth":{"token":"SYMLINK-LEAK-CANARY"}}`)

		home := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(home, ".zcode", "cli"))
		setting := filepath.Join(home, ".zcode", "cli", "setting.json")
		if err := os.Symlink(real, setting); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		lc, marker := recognizeConfig(HostZcode, home, testNow())
		if lc.Status != StatusFail {
			t.Fatalf("符号链接应 fail 不跟随，实得 %+v", lc)
		}
		if !strings.Contains(lc.Reason, "符号链接") {
			t.Fatalf("原因应注记符号链接，实得 %q", lc.Reason)
		}
		if !lc.Paths[0].Symlink {
			t.Fatalf("Paths 应注记 symlink=true，实得 %+v", lc.Paths)
		}
		if marker.Checked {
			t.Fatalf("未读取不得核标记，实得 %+v", marker)
		}
	})
	t.Run("codex", func(t *testing.T) {
		outside := t.TempDir()
		real := filepath.Join(outside, "real-config.toml")
		writeFileWithFix(t, real, "model = \"gpt\"\n")

		home := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(home, ".codex"))
		cfg := filepath.Join(home, ".codex", "config.toml")
		if err := os.Symlink(real, cfg); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		lc, _ := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusFail {
			t.Fatalf("符号链接应 fail 不跟随，实得 %+v", lc)
		}
		if !lc.Paths[0].Symlink {
			t.Fatalf("Paths 应注记 symlink=true，实得 %+v", lc.Paths)
		}
	})
}

// TestConfigPathIsDirectory 配置路径为目录（非普通文件）：不打开，如实 fail。
func TestConfigPathIsDirectory(t *testing.T) {
	t.Run("zcode", func(t *testing.T) {
		home := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(home, ".zcode", "cli", "setting.json")) // 目录冒名
		lc, _ := recognizeConfig(HostZcode, home, testNow())
		if lc.Status != StatusFail || !strings.Contains(lc.Reason, "非普通文件") {
			t.Fatalf("目录应判非普通文件，实得 %+v", lc)
		}
		if lc.Paths[0].Regular {
			t.Fatalf("目录不得记 regular=true，实得 %+v", lc.Paths)
		}
	})
	t.Run("codex", func(t *testing.T) {
		home := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(home, ".codex", "config.toml"))
		lc, _ := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusFail || !strings.Contains(lc.Reason, "非普通文件") {
			t.Fatalf("目录应判非普通文件，实得 %+v", lc)
		}
	})
}

// TestConfigMissing 宿主配置缺失：如实 fail 带引用路径（不猜其他位置）。
func TestConfigMissing(t *testing.T) {
	home := t.TempDir()
	lc, _ := recognizeConfig(HostZcode, home, testNow())
	if lc.Status != StatusFail {
		t.Fatalf("配置缺失应 fail，实得 %+v", lc)
	}
	if !strings.Contains(lc.Reason, "不存在") {
		t.Fatalf("原因应说明不存在，实得 %q", lc.Reason)
	}
	if len(lc.Paths) != 1 || lc.Paths[0].Exists {
		t.Fatalf("应回引用路径且 exists=false，实得 %+v", lc.Paths)
	}
}

// TestUnknownHostGenericManual 未知宿主：明确「通用手动」，不冒认已适配。
func TestUnknownHostGenericManual(t *testing.T) {
	home := t.TempDir()
	lc, marker := recognizeConfig(Host("claude"), home, testNow())
	if lc.Status != StatusFail {
		t.Fatalf("未知宿主应 fail，实得 %+v", lc)
	}
	if !strings.Contains(lc.Reason, "通用手动") || !strings.Contains(lc.Reason, "claude") {
		t.Fatalf("原因应注明未知宿主与通用手动，实得 %q", lc.Reason)
	}
	if lc.Source != SourceUnknownHost {
		t.Fatalf("来源应标注 unknown_host_generic_manual，实得 %q", lc.Source)
	}
	if marker.Checked {
		t.Fatalf("未知宿主不得核标记，实得 %+v", marker)
	}
}

// ===== 以下为 W06-01 codex 联审修复新增用例（必修项 1/2 + 建议修 3）=====

// runWithTimeoutGuard 限时执行辅助：f 超过 limit 未返回即 Fatal（防打开原语
// 挂起拖死整测；联审必修项 1 要求 FIFO 用例限时）。
func runWithTimeoutGuard(t *testing.T, limit time.Duration, name string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s 超过 %v 未返回（打开原语疑似挂起）", name, limit)
	}
}

// makeFifoZcodeHome 造 setting.json 位置为 FIFO 的 zcode home（末段被换为
// FIFO 的现实形态）。
func makeFifoZcodeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mkdirAllWithFix(t, filepath.Join(home, ".zcode", "cli"))
	makeFifoForTest(t, filepath.Join(home, ".zcode", "cli", "setting.json"))
	return home
}

// TestConfigHomeRootLinkAllFormsRejected 必修①（codex 复审 2d03aba）回归：
// config_home 指向外部配置目录的同一链接，分别以 `/…/link`、`/…/link/`、
// `/…/link/.` 三种形态输入——未规范化输入（尾随分隔符/`.` 组件）会让
// os.Lstat 解析到链接目标而误 pass 并穿透；统一 filepath.Clean 后三种形态
// 全部 fail（根检查见链接本体）、不认外部 auth.json 标记（codex/zcode 双宿主）。
func TestConfigHomeRootLinkAllFormsRejected(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		outside := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(outside))
		writeFileWithFix(t, filepath.Join(outside, "config.toml"), "model = \"gpt\"\n")
		writeFileWithFix(t, filepath.Join(outside, "auth.json"), `{"k":"ROOT-LINK-CANARY"}`)
		home := t.TempDir()
		link := filepath.Join(home, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		for _, form := range []string{link, link + "/", link + "/."} {
			t.Run(form, func(t *testing.T) {
				lc, marker := recognizeConfig(HostCodex, form, testNow())
				if lc.Status != StatusFail {
					t.Fatalf("链接形态 %q 应 fail，实得 %+v", form, lc)
				}
				if !strings.Contains(lc.Reason, "符号链接") || !strings.Contains(lc.Reason, "config_home") {
					t.Fatalf("原因应注记 config_home 本身为符号链接，实得 %q", lc.Reason)
				}
				if strings.Contains(lc.Reason, "ROOT-LINK-CANARY") {
					t.Fatalf("外部内容不得进 reason：%q", lc.Reason)
				}
				if marker.Found {
					t.Fatalf("不得认外部 auth.json 标记，实得 %+v", marker)
				}
				if marker.Checked && !marker.Symlink {
					t.Fatalf("标记路径应注记链接，实得 %+v", marker)
				}
			})
		}
	})
	t.Run("zcode", func(t *testing.T) {
		outside := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(outside, "cli"))
		writeFileWithFix(t, filepath.Join(outside, "cli", "setting.json"),
			`{"authToken":"ROOT-LINK-CANARY"}`)
		home := t.TempDir()
		link := filepath.Join(home, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		for _, form := range []string{link, link + "/", link + "/."} {
			t.Run(form, func(t *testing.T) {
				lc, marker := recognizeConfig(HostZcode, form, testNow())
				if lc.Status != StatusFail {
					t.Fatalf("链接形态 %q 应 fail，实得 %+v", form, lc)
				}
				if !strings.Contains(lc.Reason, "符号链接") {
					t.Fatalf("原因应注记符号链接，实得 %q", lc.Reason)
				}
				if strings.Contains(lc.Reason, "ROOT-LINK-CANARY") {
					t.Fatalf("外部内容不得进 reason：%q", lc.Reason)
				}
				if marker.Checked {
					t.Fatalf("结构未识别不得核标记，实得 %+v", marker)
				}
			})
		}
	})
}

// TestConfigIntermediateDirSymlinkRejected 中间目录符号链接穿透（联审必修项 1）：
// <home>/.zcode（或 .codex）→ 外部真实目录时整链 fail、注记链接段位，绝不
// 读取外部配置（金丝内容不得进 reason），路径引用如实注记 symlink=true。
func TestConfigIntermediateDirSymlinkRejected(t *testing.T) {
	t.Run("zcode", func(t *testing.T) {
		outside := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(outside, "cli"))
		// 外部真实合法配置（含身份键金丝）：若中间链接被穿透即误判 pass 并读外部内容。
		writeFileWithFix(t, filepath.Join(outside, "cli", "setting.json"),
			`{"authToken":"INTERMEDIATE-LINK-CANARY"}`)
		home := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, ".zcode")); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		lc, marker := recognizeConfig(HostZcode, home, testNow())
		if lc.Status != StatusFail {
			t.Fatalf("中间目录链接应整链 fail，实得 %+v", lc)
		}
		if !strings.Contains(lc.Reason, "符号链接") || !strings.Contains(lc.Reason, "中间目录") {
			t.Fatalf("原因应注记中间目录符号链接，实得 %q", lc.Reason)
		}
		if !strings.Contains(lc.Reason, filepath.Join(home, ".zcode")) {
			t.Fatalf("原因应注记是哪一段链接（段路径），实得 %q", lc.Reason)
		}
		if strings.Contains(lc.Reason, "INTERMEDIATE-LINK-CANARY") {
			t.Fatalf("外部配置内容不得进 reason：%q", lc.Reason)
		}
		if len(lc.Paths) != 1 || !lc.Paths[0].Symlink {
			t.Fatalf("Paths 应注记 symlink=true，实得 %+v", lc.Paths)
		}
		if marker.Checked {
			t.Fatalf("结构未识别不得核标记（不给穿透布局背书），实得 %+v", marker)
		}
	})
	t.Run("codex", func(t *testing.T) {
		outside := t.TempDir()
		writeFileWithFix(t, filepath.Join(outside, "config.toml"), "model = \"gpt\"\n")
		writeFileWithFix(t, filepath.Join(outside, "auth.json"), `{"k":"x"}`)
		home := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, ".codex")); err != nil {
			t.Fatalf("造符号链接失败: %v", err)
		}
		lc, marker := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusFail {
			t.Fatalf("中间目录链接应整链 fail，实得 %+v", lc)
		}
		if !strings.Contains(lc.Reason, "符号链接") || !strings.Contains(lc.Reason, "中间目录") {
			t.Fatalf("原因应注记中间目录符号链接，实得 %q", lc.Reason)
		}
		if !lc.Paths[0].Symlink {
			t.Fatalf("Paths 应注记 symlink=true，实得 %+v", lc.Paths)
		}
		// 标记层独立核查：链上有链接不得跟随计数外部 auth.json。
		if marker.Found {
			t.Fatalf("标记路径链上有链接不得计外部 auth.json，实得 %+v", marker)
		}
		if !marker.Symlink || !strings.Contains(marker.Note, "符号链接") {
			t.Fatalf("标记应注记链接且不跟随，实得 %+v", marker)
		}
	})
}

// TestConfigIntermediateDirNotDirectory 中间目录段非目录/缺失：整链 fail 带段位
// （链接/不存在/非目录三类整链拒绝口径的另外两支）。
func TestConfigIntermediateDirNotDirectory(t *testing.T) {
	t.Run("中间段是文件", func(t *testing.T) {
		home := t.TempDir()
		writeFileWithFix(t, filepath.Join(home, ".codex"), "plain file冒名目录")
		lc, _ := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusFail || !strings.Contains(lc.Reason, "中间目录非目录") {
			t.Fatalf(".codex 为文件应整链 fail 并注记非目录，实得 %+v", lc)
		}
		if !strings.Contains(lc.Reason, filepath.Join(home, ".codex")) {
			t.Fatalf("原因应注记段位路径，实得 %q", lc.Reason)
		}
	})
	t.Run("中间段缺失", func(t *testing.T) {
		home := t.TempDir()
		mkdirAllWithFix(t, filepath.Join(home, ".zcode")) // cli 段缺失
		lc, _ := recognizeConfig(HostZcode, home, testNow())
		if lc.Status != StatusFail || !strings.Contains(lc.Reason, "不存在") {
			t.Fatalf("cli 段缺失应整链 fail 注记不存在，实得 %+v", lc)
		}
		if !strings.Contains(lc.Reason, filepath.Join(home, ".zcode", "cli")) {
			t.Fatalf("原因应注记缺失段位路径，实得 %q", lc.Reason)
		}
	})
}

// TestConfigFinalFifoNoHang 末段被换为 FIFO：识别限时返回且 fail（Lstat 判非
// 普通文件即不打开，绝不挂起；联审必修项 1「FIFO 替换不挂起且 fail」端到端面）。
func TestConfigFinalFifoNoHang(t *testing.T) {
	home := makeFifoZcodeHome(t)
	var lc LayerCheck
	runWithTimeoutGuard(t, 5*time.Second, "FIFO 配置识别", func() {
		lc, _ = recognizeConfig(HostZcode, home, testNow())
	})
	if lc.Status != StatusFail {
		t.Fatalf("FIFO 应 fail，实得 %+v", lc)
	}
	if !strings.Contains(lc.Reason, "非普通文件") {
		t.Fatalf("原因应注记非普通文件，实得 %q", lc.Reason)
	}
	if len(lc.Paths) != 1 || lc.Paths[0].Regular {
		t.Fatalf("Paths 不得记 regular=true，实得 %+v", lc.Paths)
	}
}

// TestOpenVerifyReadFifoSwapWindow Lstat→Open 窗口内末段被换为 FIFO 的等价状态
// （直接以 FIFO 调打开-复核原语，确定性覆盖换窗代码路径）：O_NONBLOCK 打开不
// 挂起，fd fstat 复核非普通文件即关即拒——校验对象=读取对象。
func TestOpenVerifyReadFifoSwapWindow(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "setting.json")
	makeFifoForTest(t, fifo)
	var err error
	runWithTimeoutGuard(t, 5*time.Second, "打开复核原语", func() {
		_, err = openVerifyReadCapped(fifo, configReadLimit)
	})
	if err == nil {
		t.Fatalf("FIFO 必须被打开后复核拒绝，实得 nil")
	}
	if !strings.Contains(err.Error(), "非普通文件") || !strings.Contains(err.Error(), "打开后复核") {
		t.Fatalf("应为打开后复核拒绝文案，实得 %q", err.Error())
	}
}

// TestZcodeJSONErrorNoContentEcho JSON 解析失败只回固定分类+偏移量，绝不回显
// 内容（联审建议修 3：err.Error() 上下文会把配置内容字符带进 reason，实证
// authToken 首字符泄漏）。金丝全用大写字母（reason 为小写 ASCII+中文+数字
// 偏移量，无碰撞面），逐字符断言零泄漏。
func TestZcodeJSONErrorNoContentEcho(t *testing.T) {
	canary := "QZWKFLEAK"
	home := makeZcodeHome(t, `{"authToken": `+canary+`}`) // 裸 token 触发 invalid character 类错误
	lc, _ := recognizeConfig(HostZcode, home, testNow())
	if lc.Status != StatusFail {
		t.Fatalf("损坏 JSON 应 fail，实得 %+v", lc)
	}
	if !strings.Contains(lc.Reason, "invalid_json at offset") {
		t.Fatalf("原因应为固定分类+偏移量，实得 %q", lc.Reason)
	}
	for _, r := range canary {
		if strings.ContainsRune(lc.Reason, r) {
			t.Fatalf("原因泄漏金丝字符 %q（err.Error() 回显未封住）：%q", r, lc.Reason)
		}
	}
}

// TestCodexConfigCorruptFormsRejected 明显损坏/非法配置保守拒绝（联审必修项 2
// + codex 复审 2d03aba 必修②三个实测放行反例）：任何非法/损坏形态整文件 fail，
// 原因带「结构无法确认（保守拒绝）」且不与「合法但不支持」措辞混用。
func TestCodexConfigCorruptFormsRejected(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantReason string
	}{
		{"孤立key无值", "model =\n", "孤立 key"},
		{"孤立key带注释行", "model =\n# trailing comment\n", "孤立 key"},
		{"未闭合字符串", "model = \"gpt\n", "未闭合引号"},
		{"未闭合单引号字符串", `name = 'abc` + "\n", "未闭合引号"},
		{"表头后乱码", "[profiles.default] garbage\n", "尾随垃圾"},
		{"表头未闭合", "[profiles.default\n", "表头未闭合"},
		{"值后尾随垃圾", "model = gpt extra\n", "尾随垃圾"},
		{"无法确认行形态", "just some words\n", "结构无法确认"},
		{"合法特征行混损坏行", "model = \"gpt\"\nbroken line here\n", "结构无法确认"},
		// —— 复审必修②三个独立实测放行反例 ——
		{"裸值裸单词", "model = gpt\n", "裸值形态非法"},
		{"表头键段引号未闭合", "[profiles.\"broken]\n", "引号键段未在段内闭合"},
		{"非法转义", "model = \"bad\\q\"\n", "非法转义"},
		{"非法转义短u", "model = \"x\\u12\"\n", "非法转义"},
		{"裸值前导零", "count = 007\n", "裸值形态非法"},
		{"裸值截断浮点", "x = 1.\n", "裸值形态非法"},
		{"裸值拼错布尔", "x = tru\n", "裸值形态非法"},
		{"裸值大写布尔", "x = TRUE\n", "裸值形态非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := makeCodexHome(t, tc.content, false)
			lc, _ := recognizeConfig(HostCodex, home, testNow())
			if lc.Status != StatusFail {
				t.Fatalf("损坏形态应保守拒绝，实得 %+v", lc)
			}
			if !strings.Contains(lc.Reason, tc.wantReason) {
				t.Fatalf("原因应含 %q，实得 %q", tc.wantReason, lc.Reason)
			}
			if !strings.Contains(lc.Reason, "结构无法确认（保守拒绝）") {
				t.Fatalf("原因必须带「结构无法确认（保守拒绝）」，实得 %q", lc.Reason)
			}
			if strings.Contains(lc.Reason, "合法 TOML 但当前不支持") {
				t.Fatalf("非法/损坏不得借用「合法但不支持」措辞，实得 %q", lc.Reason)
			}
		})
	}
}

// TestCodexConfigUnsupportedFormsWording 合法但当前不支持的形态（联审建议修 1）：
// 行内数组/行内表/多行字符串是合法 TOML、无法行级确认——fail 但措辞必须与
// 「非法/损坏」区分（带「合法 TOML 但当前不支持该结构」）。
func TestCodexConfigUnsupportedFormsWording(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"行内数组", "tags = [\"a\", \"b\"]\n", "行内数组"},
		{"行内表", "inline = {a = 1}\n", "行内表"},
		{"多行字符串", "desc = \"\"\"abc\n", "多行字符串"},
		{"多行字面串", "desc = '''abc\n", "多行字符串"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := makeCodexHome(t, tc.content, false)
			lc, _ := recognizeConfig(HostCodex, home, testNow())
			if lc.Status != StatusFail {
				t.Fatalf("合法但不支持形态应 fail（保守拒绝），实得 %+v", lc)
			}
			if !strings.Contains(lc.Reason, "合法 TOML 但当前不支持该结构") {
				t.Fatalf("原因必须带「合法 TOML 但当前不支持该结构」，实得 %q", lc.Reason)
			}
			if !strings.Contains(lc.Reason, tc.want) {
				t.Fatalf("原因应含 %q，实得 %q", tc.want, lc.Reason)
			}
			if strings.Contains(lc.Reason, "损坏或非法") || strings.Contains(lc.Reason, "结构无法确认") {
				t.Fatalf("合法但不支持不得借用损坏措辞，实得 %q", lc.Reason)
			}
		})
	}
}

// TestCodexConfigLegalFormsNewlySupported 联审建议修 1 合法形态补充：值后紧邻
// 注释（无需空白）、空格分隔日期时间、各类裸值合法字面 → 识别过不回归。
func TestCodexConfigLegalFormsNewlySupported(t *testing.T) {
	cases := []string{
		"auto = true#comment\n",             // 值后紧邻注释，无空白
		"count = 42# trailing\n",            // 数字后紧邻注释
		"ts = 1979-05-27T07:32:00Z\n",       // T 分隔日期时间
		"ts2 = 1979-05-27 07:32:00\n",       // 空格分隔日期时间
		"local = 07:32:00.999\n",            // 本地时间带小数
		"big = 1_000_000\n",                 // 整数下划线分隔
		"hex = 0xDEAD_beef\n",               // 十六进制
		"neg = -1.5e3\n",                    // 负浮点带指数
		"day = 1979-05-27\n",                // 纯日期
		"flags = false\n",                   // 布尔
		"quoted = \"a\\t\\u00e9b\" # 注释\n", // 转义白名单内（\t、\uXXXX）+ 行尾注释
		"lit = 'C:\\path\\raw'\n",           // 单引号字面串不转义
	}
	for i, content := range cases {
		home := makeCodexHome(t, content, false)
		lc, _ := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusPass {
			t.Fatalf("case %d (%q) 应识别通过，实得 %+v", i, content, lc)
		}
	}
}

// TestCodexConfigMinimalLegalFormsPass 合法最小形态不回归（联审必修项 2 的
// pass 面）：裸键+引号值、[表头]、[[数组表]]、裸值、注释与空行、行尾注释 →
// 识别过。
func TestCodexConfigMinimalLegalFormsPass(t *testing.T) {
	cases := []string{
		"model = \"gpt-5\"\n",
		"[profiles.default]\n",
		"[[mcp_servers]]\n",
		"auto = true\n",
		"retries = 3\n",
		"# 只有注释头\nmodel = \"gpt\" # 行尾注释\n\n[profiles.default]\n",
		"timeout_ms = 1500\nwait = false\n",
		"[projects.\"quoted-name\"]\n",
		"[projects.\"quoted-name\".sub]\n", // 点引号混写表头（合法，逐段确认）
		"\"quoted key\" = \"v\"\n",         // 引号键
		"a.b.c = 1\n",                      // 点键
	}
	for i, content := range cases {
		home := makeCodexHome(t, content, false)
		lc, _ := recognizeConfig(HostCodex, home, testNow())
		if lc.Status != StatusPass {
			t.Fatalf("case %d (%q) 应识别通过，实得 %+v", i, content, lc)
		}
	}
}
