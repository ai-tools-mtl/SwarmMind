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
