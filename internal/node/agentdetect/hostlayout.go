// 宿主配置布局识别（只读）：codex / zcode 两宿主的位置、结构识别规则与登录态
// 标记；未知宿主一律「通用手动」，不冒认已适配。识别只回元数据（是否可用 +
// 引用路径），文件内容任何情况下不进结果。
//
// 布局事实来源（仓内既有知识，不凭想象）：
//   - zcode：<config_home>/.zcode/cli/setting.json（文件名单数），与
//     localapi/primewire.go 合并写布局同源（SPEC 506）；
//   - codex：<config_home>/.codex/config.toml（TOML，W06-03 fixture 口径
//     「非法 JSON/TOML」即此），登录标记 auth.json 为公开安装布局。
package agentdetect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// zcodeMarkerKeys zcode 登录态身份键 V0 保守清单：宿主未公开文档化私有 schema，
// 清单不命中一律按「无登录标记」处理（fail-closed：只影响 ExecutionReady 不亮，
// 绝不因清单不命中放行，也绝不据此断言「未登录」）。
var zcodeMarkerKeys = []string{"auth", "authToken", "credentials"}

// codex TOML 保守启发式（V0 不引入 TOML 解析依赖——SPEC 三依赖白名单不加项）：
// 只认两种结构特征行——[表头] 与 key = value；识别不了就 fail，不冒认已适配。
var (
	tomlTableLineRe = regexp.MustCompile(`^\s*\[[^\]]+\]`)
	tomlKeyLineRe   = regexp.MustCompile(`^\s*[A-Za-z0-9_"'.\-]+\s*=`)
)

// 配置读取闸门的统一文案（测试断言锚点）。
var (
	errSymlinkMsg    = "配置路径为符号链接：不跟随、不读取（如实注记，防经链接读取与逃逸展开）"
	errNotRegularMsg = "配置路径非普通文件（目录/FIFO/设备等）：不打开"
)

// knownLayoutSource 配置层检查来源标注。
func knownLayoutSource(h Host) string {
	switch h {
	case HostCodex:
		return "known_layout:codex"
	case HostZcode:
		return "known_layout:zcode"
	default:
		return SourceUnknownHost
	}
}

// recognizeConfig 按宿主识别配置布局并核登录标记。返回配置识别层记录与标记
// 核查结果。两层独立性口径：标记核查不回滚结构判定（codex 配置缺失仍核
// auth.json 存在性）；结构未识别时 zcode 顶层键核查不执行（不给损坏配置背书，
// Checked=false）。
func recognizeConfig(host Host, home string, now time.Time) (LayerCheck, LoginMarkerCheck) {
	lc := LayerCheck{Source: knownLayoutSource(host), CheckedAt: now}
	var cfgPath, markerPath string
	switch host {
	case HostCodex:
		cfgPath = filepath.Join(home, ".codex", "config.toml")
		markerPath = filepath.Join(home, ".codex", "auth.json")
	case HostZcode:
		cfgPath = filepath.Join(home, ".zcode", "cli", "setting.json")
		markerPath = cfgPath // zcode 标记在该文件顶层身份键内，引用同一路径
	default:
		// 未知宿主：明确「通用手动」，绝不猜布局（宁可让人手动确认，不可冒认）。
		lc.Status = StatusFail
		lc.Reason = fmt.Sprintf("未知宿主 %q：无已适配配置布局，按通用手动处理（支持宿主：codex、zcode）", string(host))
		return lc, LoginMarkerCheck{CheckedAt: now, Note: "未知宿主无登录标记规则"}
	}

	raw, ref, cfgErr := readRegularFileCapped(cfgPath, "config", configReadLimit)
	lc.Paths = []PathRef{ref} // 路径注记来自真实 Lstat 事实（exists/regular/symlink）
	if cfgErr != nil {
		lc.Status = StatusFail
		lc.Reason = cfgErr.Error() // 文案已含「未找到/符号链接/非普通文件」等事实与引用路径
		return lc, checkLoginMarker(host, markerPath, nil, now)
	}
	var parseErr error
	var doc map[string]any // 仅 zcode 使用（JSON 顶层对象）；codex 恒 nil
	switch host {
	case HostCodex:
		parseErr = validateCodexConfigToml(raw)
	case HostZcode:
		doc, parseErr = validateZcodeSetting(raw)
	}
	if parseErr != nil {
		lc.Status = StatusFail
		lc.Reason = parseErr.Error()
		return lc, checkLoginMarker(host, markerPath, nil, now)
	}
	lc.Status = StatusPass
	if host == HostCodex {
		lc.Reason = "结构启发式识别通过（V0 无 TOML 解析依赖，未解析语义）"
	}
	return lc, checkLoginMarker(host, markerPath, doc, now)
}

// checkLoginMarker 登录态标记核查（只回元数据，标记内容与有效性永不读取/返回）：
//   - codex：auth.json 存在性（Lstat，永不读内容）；
//   - zcode：setting.json 顶层身份键（doc=nil 即结构未识别 → 不核）。
func checkLoginMarker(host Host, markerPath string, doc map[string]any, now time.Time) LoginMarkerCheck {
	m := LoginMarkerCheck{CheckedAt: now}
	switch host {
	case HostCodex:
		m.Path = markerPath
		m.Checked = true
		facts := lstatPath(markerPath)
		m.Symlink = facts.Symlink
		if facts.Exists {
			m.Found = true
			m.Note = "发现登录态标记（auth.json 存在；仅存在性线索，有效性未验证，内容未读取）"
			return m
		}
		m.Note = "auth.json 不存在（未发现 codex 登录态标记）"
		return m
	case HostZcode:
		m.Path = markerPath
		if doc == nil {
			m.Note = "配置结构未识别，不核顶层身份键（不给损坏配置背书）"
			return m
		}
		m.Checked = true
		for _, key := range zcodeMarkerKeys {
			if v, ok := doc[key]; ok && markerValueNonEmpty(v) {
				m.Found = true
				m.Note = "发现登录态标记（setting.json 顶层身份键 " + key + " 非空；仅结构线索，有效性未验证，值未返回）"
				return m
			}
		}
		m.Note = "setting.json 顶层无身份键命中（V0 保守清单：auth/authToken/credentials）"
		return m
	default:
		m.Note = "未知宿主无登录标记规则"
		return m
	}
}

// markerValueNonEmpty 标记键值非空判定：非空字符串/true/非空对象/非空数组/其他
// 非 nil 标量算「有标记」；null/空串/false/空容器不算。值本身永不返回。
func markerValueNonEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return t
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	default: // json.Number 等标量：非 nil 即算（仅作线索，有效性未验证）
		return true
	}
}

// readRegularFileCapped 带闸门的只读：Lstat 先判符号链接与非普通文件（拒绝即
// 不打开）；打开后 fd fstat 复核「校验对象=读取对象」（消 Lstat→Open 窗口换型）；
// 读取设字节上限并核对实得字节数（防声明大小与实际不符/检查后增长）。无论成败
// 都返回路径引用（来自真实 Lstat 事实，供调用方如实注记）；错误文案即配置层
// 失败原因（不回文件内容）。
func readRegularFileCapped(path, kind string, limit int64) ([]byte, PathRef, error) {
	facts := lstatPath(path)
	ref := facts.ref(path, kind)
	if facts.Err != nil {
		if os.IsNotExist(facts.Err) {
			return nil, ref, fmt.Errorf("宿主配置不存在：%s", path)
		}
		return nil, ref, fmt.Errorf("宿主配置状态检查失败：%s（%v）", path, facts.Err)
	}
	if facts.Symlink {
		return nil, ref, errors.New(errSymlinkMsg + "：" + path)
	}
	if !facts.Regular {
		return nil, ref, errors.New(errNotRegularMsg + "：" + path)
	}
	f, err := os.Open(path) // 仅在 Lstat 判非链接/非异常型后打开
	if err != nil {
		return nil, ref, fmt.Errorf("宿主配置打开失败：%s（%v）", path, err)
	}
	defer f.Close()
	st, err := f.Stat() // fd fstat：校验对象=读取对象
	if err != nil {
		return nil, ref, fmt.Errorf("宿主配置句柄状态复核失败：%s（%v）", path, err)
	}
	if !st.Mode().IsRegular() {
		return nil, ref, errors.New(errNotRegularMsg + "（打开后复核）：" + path)
	}
	if st.Size() > limit {
		return nil, ref, fmt.Errorf("宿主配置超过读取上限 %d 字节：不做结构识别（异常文件，如实上报）", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, ref, fmt.Errorf("宿主配置读取失败：%s（%v）", path, err)
	}
	if int64(len(raw)) > limit { // 声明大小可造假，实得字节数兜底核对
		return nil, ref, fmt.Errorf("宿主配置超过读取上限 %d 字节：不做结构识别（异常文件，如实上报）", limit)
	}
	return raw, ref, nil
}

// validateZcodeSetting zcode setting.json 结构识别：与 primewire 合法性口径同源
// ——全文恰一个顶层 JSON 对象；null、数组、尾部多余内容、空文件均不命中。
// 返回解析出的顶层对象（供标记键核查；内容不外返）。
func validateZcodeSetting(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("setting.json 为空文件：结构不可识别")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("setting.json 非合法 JSON（顶层须为对象）：%v", err)
	}
	if doc == nil {
		// `null` 解码成功但 doc 为 nil：无安全识别点，判结构不可识别（同 primewire R1）。
		return nil, errors.New("setting.json 顶层为 null：结构不可识别")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("setting.json JSON 尾部有多余内容（仅容空白）：结构不可识别")
	}
	return doc, nil
}

// validateCodexConfigToml codex config.toml 保守启发式识别：非空、非二进制
// （无 NUL 字节）、至少一行 [表头] 或 key = 形态。不是 TOML 语义解析（V0 不引
// 解析依赖），识别不了就 fail 不冒认——语义级适配待宿主布局确证后再进。
func validateCodexConfigToml(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("config.toml 为空文件：结构不可识别")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("config.toml 含 NUL 字节（二进制内容）：结构不可识别")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if tomlTableLineRe.MatchString(t) || tomlKeyLineRe.MatchString(t) {
			return nil // 命中任一结构特征行即通过
		}
	}
	return errors.New("config.toml 未发现 TOML 结构特征（无 [表头] 或 key = 行）：结构不可识别")
}
