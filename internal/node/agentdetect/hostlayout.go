// 宿主配置布局识别（只读）：codex / zcode 两宿主的位置、结构识别规则与登录态
// 标记；未知宿主一律「通用手动」，不冒认已适配。识别只回元数据（是否可用 +
// 引用路径），文件内容任何情况下不进结果。
//
// 布局事实来源（仓内既有知识，不凭想象）：
//   - zcode：<config_home>/.zcode/cli/setting.json（文件名单数），与
//     localapi/primewire.go 合并写布局同源（SPEC 506）；
//   - codex：<config_home>/.codex/config.toml（TOML，W06-03 fixture 口径
//     「非法 JSON/TOML」即此），登录标记 auth.json 为公开安装布局。
//
// 路径闸门（W06-01 联审必修项收紧）：布局链逐段 Lstat——config_home 本体与
// 其下每个中间目录组件都必须是真实目录（链接/缺失/非目录=整链 fail 并注记
// 哪一段），防 `.zcode → 外部目录` 式中间链接穿透误判 pass；末段保持 Lstat
// 语义（链接=如实注记不读取），打开用 O_NONBLOCK 再以 fd fstat 复核普通文件
// （末段被换为 FIFO/设备时 open 不挂起，非普通即关即拒）。TOCTOU 残窗见 doc.go。
package agentdetect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// zcodeMarkerKeys zcode 登录态身份键 V0 保守清单：宿主未公开文档化私有 schema，
// 清单不命中一律按「无登录标记」处理（fail-closed：只影响 ExecutionReady 不亮，
// 绝不因清单不命中放行，也绝不据此断言「未登录」）。
var zcodeMarkerKeys = []string{"auth", "authToken", "credentials"}

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
	// 必修①（codex 复审 2d03aba）：根表示统一——先 filepath.Clean 再校验/拼接。
	// 未规范化输入（如 /…/link/ 尾随分隔符、/…/link/. ）会让 os.Lstat 解析到
	// 链接目标（真实目录→根检查误 pass），随后 Join 又 Clean 回链接形态继续
	// 穿透——Clean 后「校验的根」与「使用的根」同一表示，Lstat 见到的就是链接
	// 本体，整链 fail。
	home = filepath.Clean(home)
	// 布局以「config_home + 相对段」表达：段校验/打开全部按段逐段 Lstat，
	// 不允许中间目录符号链接穿透（联审必修项 1）。
	var cfgSegs, markerSegs []string
	switch host {
	case HostCodex:
		cfgSegs = []string{".codex", "config.toml"}
		markerSegs = []string{".codex", "auth.json"}
	case HostZcode:
		cfgSegs = []string{".zcode", "cli", "setting.json"}
		markerSegs = cfgSegs // zcode 标记在该文件顶层身份键内，引用同一路径
	default:
		// 未知宿主：明确「通用手动」，绝不猜布局（宁可让人手动确认，不可冒认）。
		lc.Status = StatusFail
		lc.Reason = fmt.Sprintf("未知宿主 %q：无已适配配置布局，按通用手动处理（支持宿主：codex、zcode）", string(host))
		return lc, LoginMarkerCheck{CheckedAt: now, Note: "未知宿主无登录标记规则"}
	}

	raw, ref, cfgErr := readRegularFileCapped(home, cfgSegs, "config", configReadLimit)
	lc.Paths = []PathRef{ref} // 路径注记来自真实 Lstat 事实（exists/regular/symlink）
	if cfgErr != nil {
		lc.Status = StatusFail
		lc.Reason = cfgErr.Error() // 文案已含「不存在/符号链接（含段位）/非普通文件」等事实与引用路径
		return lc, checkLoginMarker(host, home, markerSegs, nil, now)
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
		return lc, checkLoginMarker(host, home, markerSegs, nil, now)
	}
	lc.Status = StatusPass
	if host == HostCodex {
		// 粗检语义（2026-10-30 流程裁决方案 B）：pass ≠ 语法合法——检测层不做
		// TOML 语法校验，注记必须明示「未做语法校验」，语法错误由执行 CLI 自行
		// 解析暴露（绝不冒认语法合法）。
		lc.Reason = "结构粗检通过（启发式，非语法校验）：检测层不做 TOML 语法校验、未做语法校验，语法错误由执行 CLI 自行解析暴露"
	}
	return lc, checkLoginMarker(host, home, markerSegs, doc, now)
}

// checkLoginMarker 登录态标记核查（只回元数据，标记内容与有效性永不读取/返回）：
//   - codex：auth.json 存在性（Lstat，永不读内容）；布局链校验未过时不跟随
//     判定存在性（防中间目录链接把外部对象计成本宿主标记）；
//   - zcode：setting.json 顶层身份键（doc=nil 即结构未识别 → 不核）。
func checkLoginMarker(host Host, home string, markerSegs []string, doc map[string]any, now time.Time) LoginMarkerCheck {
	m := LoginMarkerCheck{CheckedAt: now}
	switch host {
	case HostCodex:
		markerPath := joinLayout(home, markerSegs)
		m.Path = markerPath
		m.Checked = true
		if issue, viaLink := layoutChainIssue(home, markerSegs); issue != "" {
			m.Symlink = viaLink // 链上有符号链接：如实注记，不跟随
			m.Note = "标记路径布局链校验未过，不跟随判定存在性：" + issue
			return m
		}
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
		m.Path = joinLayout(home, markerSegs)
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

// joinLayout config_home + 布局相对段拼出绝对路径（与识别目标路径一一对应）。
func joinLayout(home string, segments []string) string {
	return filepath.Join(append([]string{home}, segments...)...)
}

// layoutChainIssue 布局目录链逐段校验（联审必修项 1：防中间目录符号链接穿透
// ——只对末段 Lstat 时，os.Open 会跟随中间目录链接，`.zcode → 外部目录` 会
// 误判 pass 且 symlink=false）。config_home 本体与其下 segments 的前 len-1 个
// 中间目录组件逐一 Lstat，必须都是真实目录；链接/不存在/非目录即整链 fail，
// 返回含具体段位的失败原因（viaSymlink 标注是否因符号链接）。末段文件不在此
// 校验（保持既有 Lstat 语义）。范围口径：config_home 为调用方显式给出的受控
// 根，按一个整体校验（其上/其内的 OS 层组件不逐段展开——调用方输入与命令
// 路径同责，残窗登记 doc.go）。
func layoutChainIssue(home string, segments []string) (issue string, viaSymlink bool) {
	fi, err := os.Lstat(home)
	if err != nil {
		if os.IsNotExist(err) {
			return "配置根目录（config_home）不存在：" + home, false
		}
		return fmt.Sprintf("配置根目录状态检查失败：%s（%v）", home, err), false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errSymlinkMsg + "（config_home 本身为符号链接：" + home + "）", true
	}
	if !fi.IsDir() {
		return "配置根目录非目录：" + home, false
	}
	for i := 0; i < len(segments)-1; i++ {
		cur := joinLayout(home, segments[:i+1])
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("配置路径中间目录不存在（段 %d/%d）：%s（整链不跟随解析）", i+1, len(segments)-1, cur), false
			}
			return fmt.Sprintf("配置路径中间目录状态检查失败（段 %d/%d）：%s（%v）", i+1, len(segments)-1, cur, err), false
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("%s（中间目录段 %d 为符号链接：%s，整链 fail）", errSymlinkMsg, i+1, cur), true
		}
		if !fi.IsDir() {
			return fmt.Sprintf("配置路径中间目录非目录（段 %d/%d）：%s", i+1, len(segments)-1, cur), false
		}
	}
	return "", false
}

// readRegularFileCapped 带闸门的只读（config_home + 布局相对段）：先逐段校验
// 布局目录链（中间目录符号链接=整链 fail 并注记段位）；末段 Lstat 判符号链接
// 与非普通文件（拒绝即不打开）；打开用 O_NONBLOCK（末段被换为 FIFO/设备时
// open 不挂起）后立即 fd fstat 复核「校验对象=读取对象」（非普通即关即拒）；
// 读取设字节上限并核对实得字节数。无论成败都返回路径引用（来自真实 Lstat
// 事实，供调用方如实注记）；错误文案即配置层失败原因（不回文件内容）。
func readRegularFileCapped(home string, segments []string, kind string, limit int64) ([]byte, PathRef, error) {
	path := joinLayout(home, segments)
	if issue, viaLink := layoutChainIssue(home, segments); issue != "" {
		ref := lstatPath(path).ref(path, kind)
		ref.Symlink = ref.Symlink || viaLink // 链上有符号链接：如实注记
		return nil, ref, errors.New(issue)
	}
	facts := lstatPath(path)
	ref := facts.ref(path, kind)
	if facts.Symlink {
		return nil, ref, errors.New(errSymlinkMsg + "：" + path)
	}
	if !facts.Regular {
		return nil, ref, errors.New(errNotRegularMsg + "：" + path)
	}
	raw, err := openVerifyReadCapped(path, limit)
	if err != nil {
		return nil, ref, err
	}
	return raw, ref, nil
}

// openVerifyReadCapped 末段打开-复核-读取三件套（联审必修项 1：打开原语防
// 阻塞）：O_NONBLOCK 打开（末段被换为 FIFO/设备时 open 立即返回不挂起）→
// fd fstat 复核必须是普通文件（非普通即关即拒——校验对象=读取对象，消
// Lstat→Open 窗口内换型为 FIFO 的挂起面与误读面）→ 上限内读取并核对实得
// 字节数。错误文案不回文件内容。
func openVerifyReadCapped(path string, limit int64) ([]byte, error) {
	f, err := openNoBlock(path) // O_NONBLOCK：末段换型为 FIFO 时 open 立即返回
	if err != nil {
		return nil, fmt.Errorf("宿主配置打开失败：%s（%v）", path, err)
	}
	defer f.Close()
	st, err := f.Stat() // fd fstat：校验对象=读取对象
	if err != nil {
		return nil, fmt.Errorf("宿主配置句柄状态复核失败：%s（%v）", path, err)
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New(errNotRegularMsg + "（打开后复核，即关即拒）：" + path)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("宿主配置超过读取上限 %d 字节：不做结构识别（异常文件，如实上报）", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("宿主配置读取失败：%s（%v）", path, err)
	}
	if int64(len(raw)) > limit { // 声明大小可造假，实得字节数兜底核对
		return nil, fmt.Errorf("宿主配置超过读取上限 %d 字节：不做结构识别（异常文件，如实上报）", limit)
	}
	return raw, nil
}

// openNoBlock 以非阻塞语义打开末段文件（openNoBlockFlag 平台分文件定义）：
// Unix 加 O_NONBLOCK 防 FIFO/设备 open 挂起；打开后必须 fd fstat 复核普通
// 文件才算通过（见 openVerifyReadCapped）。
func openNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, openNoBlockFlag, 0)
}

// validateZcodeSetting zcode setting.json 结构识别：与 primewire 合法性口径同源
// ——全文恰一个顶层 JSON 对象；null、数组、尾部多余内容、空文件均不命中。
// 返回解析出的顶层对象（供标记键核查；内容不外返）。这是 stdlib encoding/json
// 真解析（非文法模拟），保留不动。
func validateZcodeSetting(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("setting.json 为空文件：结构不可识别")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		// 只回固定错误分类+偏移量，绝不拼接 err.Error()——解码错误上下文会回显
		// 配置内容字符（实证：authToken 首字符进 reason，联审建议修项 3）。
		return nil, fmt.Errorf("setting.json 非合法 JSON（顶层须为对象）：invalid_json at offset %d", jsonErrOffset(dec, err))
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

// jsonErrOffset JSON 解析失败的定位偏移：优先取 SyntaxError/UnmarshalTypeError
// 的 Offset，兜底用解码器当前输入偏移；只回数字，绝不回错误上下文文本（错误
// 文本含配置内容字符，防泄漏红线见 validateZcodeSetting）。
func jsonErrOffset(dec *json.Decoder, err error) int64 {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return syn.Offset
	case errors.As(err, &typ):
		return typ.Offset
	default:
		return dec.InputOffset()
	}
}

// validateCodexConfigToml codex config.toml 结构粗检（2026-10-30 流程裁决方案 B
// 降范围：检测层不承担 TOML 语法校验职责——字面文法层【正则字面集/转义白名单/
// 键段扫描器/三值分类器】整层删除，从构造上消除「非法字面放行/合法形态误拒/
// Unicode 转义」三类必修问题，不再 chase 文法）。判定四步：
//
//  1. 空文件（0 字节或全空白行）→ fail「为空文件」；
//  2. 含 NUL 字节 → fail「二进制内容，非文本配置」；
//  3. 无任何行匹配松散形态（[ 开头表头行 或 含 " = " 的键值行，跳过空行与
//     注释行）→ fail「无法识别出任何 TOML 键值/表头结构」；
//  4. 否则 pass（nil）。
//
// pass 仅代表「已知布局位置存在配置文件且非明显损坏」，绝不声称语法合法——
// 语法非法输入（裸单词值、非法转义、非法日期等）同样 pass，由 pass 注记明示
// 「未做语法校验，语法错误由执行 CLI 自行解析暴露」（见 recognizeConfig）。
func validateCodexConfigToml(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("config.toml 为空文件（0 字节或全空白行）：结构不可识别")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("config.toml 含 NUL 字节（二进制内容，非文本配置）：结构不可识别")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") || strings.Contains(t, " = ") {
			return nil // 松散结构形态命中：粗检通过（语法校验不归检测层，见函数注释）
		}
	}
	return errors.New("config.toml 未发现 TOML 结构特征（无 [表头] 行或含 \" = \" 的键值行）：无法识别出任何 TOML 键值/表头结构，结构不可识别")
}
