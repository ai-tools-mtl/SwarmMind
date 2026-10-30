// 本机宿主 MCP 配置预览/应用/撤回（v3 W03-02，docs/tasks/v3/W03-02-rfc.md 定案）：
//   - POST /api/v1/local/mcp/config/preview  脱敏差异+可写性+冲突检测+文件版本 hash
//     （零写盘——含备份/临时文件/目录创建）；
//   - POST /api/v1/local/mcp/config/apply    预期版本核对 → 撤回登记 → 整文件备份 →
//     原子合并（只动目标条目；失败原文件保留如实报错）；
//   - POST /api/v1/local/mcp/config/revert   只撤本次 apply 登记的条目（用户后改
//     版本不符 → 409 file_conflict，绝不回滚整文件）。
//
// 鉴权：三端点全部 authLocal（仅 local_token，prime_token 一律 401）——人工宿主配置/
// 凭据部署动作，SPEC 六.六豁免⑭（逐端点依据见 SPEC 原文）；不经 agent 工具调用任意
// 文件写入（写面收敛于本文件单一入口）。
//
// 安全纪律（本文件契约，改动前先读本段）：
//   - 路径闸门与 agentdetect/hostlayout.go 同纪律：config_home 先 filepath.Clean 消
//     根表示漂移 → 本体与中间段逐段 Lstat 真实目录（符号链接=拒绝，防 `.zcode →
//     外部目录` 式中间链接穿透逃逸）→ 末段 Lstat 非链接非异常 → O_NONBLOCK 打开
//     （FIFO 不挂起；Windows 无此语义无需对应物，平台分文件同 agentdetect openflags
//     先例）→ fd fstat 复核普通文件 → 上限读取并核对实得字节数；错误文案只回分类
//     与偏移量，绝不回文件内容（agentdetect validateZcodeSetting 联审口径）。
//   - 凭据纪律：请求/应答/日志绝不含明文凭据——apply 写入的凭据值只取服务端当前
//     prime_token，在内存合并瞬间使用后即弃；应答只出现 maskCredential 脱敏引用；
//     既有条目的 env 值只报 set/unset 结构标记绝不回显（零明文金丝测试锁定：假密钥
//     预埋宿主配置，三端点应答 grep 不到其完整形态）。
//   - 合并纪律（primewire 泛化，primewire.go 本体零改动、atomicWriteFile0600 原样
//     复用）：解码 UseNumber 数值字面量保真（不做 float64 往返换算）、全文恰一个
//     顶层 JSON 对象、尾部多余内容拒绝、mcpServers 非对象拒绝、只动目标键（其他键
//     语义级原样——重序列化键序规范化，值不转型）、备份先于主文件且失败即中止、
//     临时文件+rename 原子写 0600、绝不创建宿主目录（宁可手贴不猜布局乱写）。
//   - codex/未知宿主：apply 一律 manual_only 拒绝（W03-01 GenFormAutoWrite 判定式：
//     仅 node+同机+verified 宿主可自动写；codex=manual_generic_codex 未实测、未知
//     宿主无布局）——agentdetect 粗检裁决口径：本端点不解析 TOML（依赖白名单纪律），
//     绝不冒认可安全合并，指路手动模板。
package localapi

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// —— 常量 ——

// mcpCfgBackupSuffix apply/revert 写前整文件备份后缀（沿 primewire .bak-prime 先例：
// 重复写入直接覆盖旧备份，不堆积；备份与主文件同目录同保护域 0600）。
const mcpCfgBackupSuffix = ".bak-mcpcfg"

// mcpCfgReadLimit 宿主配置读取上限（异常超大文件如实拒绝，不做结构识别）。
const mcpCfgReadLimit = 1 << 20 // 1 MiB

// mcpCfgRegistryFileName 撤回登记文件（data_dir 内，0600 原子写；与 prime_token/
// mcp_access.json 同保护域——登记内原值快照含宿主配置既有凭据，纪律同 primewire
// 备份「旧值本就是该文件既有内容，保护域不变」口径，RFC §6）。
const mcpCfgRegistryFileName = "mcp_config_writes.json"

// mcpCfgRegistryVersion 登记文件格式版本（未知版本拒绝加载——登记不猜测）。
const mcpCfgRegistryVersion = 1

// credential_ref 取值（请求绝不含明文凭据——RFC §2：凭据只以「指向服务端当前
// 凭据的引用」或「占位/临时输入标记」表达）。
const (
	mcpCredRefCurrent     = "current_prime_token" // 写入取服务端当前 prime_token（仅 node 合法）
	mcpCredRefPlaceholder = "placeholder"         // 占位文案/临时输入标记（apply 拒绝——占位凭据落盘无用）
)

// mcpCfgMaxNameLen 配置名长度上限（与 discover 候选名同量级护栏）。
const mcpCfgMaxNameLen = 64

// muMcpCfgFile apply/revert 宿主配置写串行锁（进程内防并发 apply/revert 交错写；
// 跨进程由版本 hash 门兜底——后者 409 file_changed，RFC §8 已知限制）。
var muMcpCfgFile sync.Mutex

// —— 业务错误（code → HTTP 映射见 RFC §9 表） ——

// mcpCfgError 端点层业务错误。
type mcpCfgError struct {
	code string
	msg  string
}

func (e *mcpCfgError) Error() string { return e.msg }

func mcpCfgErrf(code, format string, a ...any) *mcpCfgError {
	return &mcpCfgError{code: code, msg: fmt.Sprintf(format, a...)}
}

// mcpCfgHTTPStatus 错误码 → HTTP 状态（RFC §9 表）。
func mcpCfgHTTPStatus(code string) int {
	switch code {
	case "file_changed", "file_conflict", "name_conflict":
		return http.StatusConflict
	case "revert_not_found":
		return http.StatusNotFound
	case "write_failed":
		return http.StatusInternalServerError
	case "internal":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

// writeMcpCfgErr 统一错误应答（extra 供 name_conflict 附冲突检测结果——建议名/处置
// 说明供页面直接渲染）。
func writeMcpCfgErr(w http.ResponseWriter, e *mcpCfgError, extra map[string]any) {
	body := map[string]any{"error": e.code, "message": e.msg}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, mcpCfgHTTPStatus(e.code), body)
}

// —— 宿主布局（与 agentdetect/hostlayout.go 位置表同源；未知宿主不给布局） ——

// mcpCfgLayout 已知宿主配置布局。
type mcpCfgLayout struct {
	segs   []string // config_home 下相对段（末段=配置文件）
	format string   // json | toml（toml 仅探测不解析——见文件头 codex 纪律）
}

// mcpCfgLayoutFor 宿主 → 布局（未知宿主 ok=false——宁可手贴不猜布局乱写）。
func mcpCfgLayoutFor(host string) (mcpCfgLayout, bool) {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "zcode":
		// 布局事实源：primewire.go（<home>/.zcode/cli/setting.json，文件名单数）。
		return mcpCfgLayout{segs: []string{".zcode", "cli", "setting.json"}, format: "json"}, true
	case "codex":
		return mcpCfgLayout{segs: []string{".codex", "config.toml"}, format: "toml"}, true
	default:
		return mcpCfgLayout{}, false
	}
}

// —— 路径闸门与安全读（agentdetect hostlayout 同纪律，错误文案不回内容） ——

// mcpCfgFileState 宿主配置文件事实（preview file 字段数据源；来自真实 Lstat/Open 观测）。
type mcpCfgFileState struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Symlink  bool   `json:"symlink"`
	Writable bool   `json:"writable"`
	Version  string `json:"version"` // sha256 hex（原始字节）；文件不存在=空串哨兵
}

// mcpCfgVersionHash 文件版本 hash（SHA-256 hex；防 TOCTOU 口径的版本语义，RFC §4）。
func mcpCfgVersionHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// mcpCfgRead 宿主配置安全读（路径闸门全链）。链缺失返回 Exists=false 且无错误
// （预览按事实呈现、apply 转 config_file_missing——绝不创建宿主目录）；符号链接/
// 非普通文件 → unsafe_path；打开/读取失败 → file_unreadable。TOCTOU 残窗（逐段
// Lstat 之间与 Lstat→Open 的同时换型极端时序）为主人受控面如实登记口径（RFC §8，
// 与 agentdetect/primewire 同级）。
func mcpCfgRead(configHome string, layout mcpCfgLayout) (raw []byte, st mcpCfgFileState, e *mcpCfgError) {
	root := filepath.Clean(configHome) // 根表示统一：消尾随分隔符与 "." 组件（agentdetect 必修①同款）
	st.Path = filepath.Join(append([]string{root}, layout.segs...)...)
	fi, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, st, nil // 配置根不存在：整链缺失（事实呈现，非错误）
		}
		return nil, st, mcpCfgErrf("file_unreadable", "配置根目录状态检查失败：%s（%v）", root, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, st, mcpCfgErrf("unsafe_path", "配置根目录（config_home）为符号链接：不跟随、不读写（防经链接逃逸）：%s", root)
	}
	if !fi.IsDir() {
		return nil, st, mcpCfgErrf("unsafe_path", "配置根目录非目录：%s", root)
	}
	// 中间段逐段 Lstat：缺失=整链缺失事实；符号链接/非目录=unsafe 拒绝（不跟随）。
	for i := 0; i < len(layout.segs)-1; i++ {
		cur := filepath.Join(append([]string{root}, layout.segs[:i+1]...)...)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, st, nil
			}
			return nil, st, mcpCfgErrf("file_unreadable", "配置路径中间目录状态检查失败（段 %d）：%s（%v）", i+1, cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, st, mcpCfgErrf("unsafe_path", "配置路径中间目录段 %d 为符号链接：整链不跟随（防逃逸）：%s", i+1, cur)
		}
		if !fi.IsDir() {
			return nil, st, mcpCfgErrf("unsafe_path", "配置路径中间目录非目录（段 %d）：%s", i+1, cur)
		}
	}
	// 末段：Lstat 事实 → O_NONBLOCK 打开 → fd fstat 复核 → 上限读取核对实得字节数。
	fi, err = os.Lstat(st.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, st, nil
		}
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置状态检查失败：%s（%v）", st.Path, err)
	}
	st.Exists = true
	if fi.Mode()&os.ModeSymlink != 0 {
		st.Symlink = true
		return nil, st, mcpCfgErrf("unsafe_path", "宿主配置为符号链接：不跟随、不读写（防经链接逃逸）：%s", st.Path)
	}
	if !fi.Mode().IsRegular() {
		return nil, st, mcpCfgErrf("unsafe_path", "宿主配置非普通文件（目录/FIFO/设备等）：不打开：%s", st.Path)
	}
	f, err := os.OpenFile(st.Path, mcpcfgOpenReadFlag, 0)
	if err != nil {
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置打开失败：%s（%v）", st.Path, err)
	}
	defer f.Close()
	fst, err := f.Stat() // fd fstat：校验对象=读取对象（消 Lstat→Open 换型面）
	if err != nil {
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置句柄状态复核失败：%s（%v）", st.Path, err)
	}
	if !fst.Mode().IsRegular() {
		return nil, st, mcpCfgErrf("unsafe_path", "宿主配置打开后复核非普通文件（即关即拒）：%s", st.Path)
	}
	if fst.Size() > mcpCfgReadLimit {
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置超过读取上限 %d 字节：异常文件，不做结构识别", mcpCfgReadLimit)
	}
	raw, err = io.ReadAll(io.LimitReader(f, mcpCfgReadLimit+1))
	if err != nil {
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置读取失败：%s（%v）", st.Path, err)
	}
	if int64(len(raw)) > mcpCfgReadLimit { // 声明大小可造假，实得字节数兜底核对
		return nil, st, mcpCfgErrf("file_unreadable", "宿主配置超过读取上限 %d 字节：异常文件，不做结构识别", mcpCfgReadLimit)
	}
	st.Version = mcpCfgVersionHash(raw)
	return raw, st, nil
}

// mcpCfgProbeWritable 写打开探测（O_WRONLY 打开即关，不写任何字节、不改内容）：
// 只读文件/目录在预览即如实呈现「不可写」；apply 真实写失败另有 write_failed 兜底。
func mcpCfgProbeWritable(path string) (bool, string) {
	f, err := os.OpenFile(path, mcpcfgOpenWriteFlag, 0)
	if err != nil {
		return false, fmt.Sprintf("宿主配置当前不可写（%v）", err)
	}
	_ = f.Close()
	return true, ""
}

// mcpCfgProbeCommand 调用机 command 路径探测（W03-01 交接面：同机路径探测由端点层
// 传入 → PathProbedLocally）：Lstat 不跟随；符号链接如实注记不拒绝（exec 由内核
// 解析链接，本端不解析、不声称其他位置——agentdetect 口径）。
func mcpCfgProbeCommand(p string) (exists, symlink bool) {
	if p == "" {
		return false, false
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return false, false
	}
	return true, fi.Mode()&os.ModeSymlink != 0
}

// —— JSON 解析/合并（primewire 合法性口径同源；数值保真） ——

// mcpCfgJSONErrOffset JSON 解析失败定位偏移：只回数字，绝不回错误上下文文本（其
// 文本含配置内容字符——agentdetect 联审建议修项 3 同款防线）。
func mcpCfgJSONErrOffset(dec *json.Decoder, err error) int64 {
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

// mcpCfgParseJSONDoc 宿主配置 JSON 真解析：全文恰一个顶层 JSON 对象；空文件、null、
// 数组、尾部多余内容均不命中（primewire R1/R2 与 agentdetect validateZcodeSetting
// 同口径）——无安全合并点即拒绝，绝不硬写。
func mcpCfgParseJSONDoc(raw []byte, path string) (map[string]any, *mcpCfgError) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, mcpCfgErrf("invalid_json", "%s 为空文件：无安全合并点，不硬写", path)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 数值保真：json.Number 原字面量往返，不做 float64 换算
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, mcpCfgErrf("invalid_json", "%s 非合法 JSON（顶层须为对象），不硬写（invalid_json at offset %d）",
			path, mcpCfgJSONErrOffset(dec, err))
	}
	if doc == nil {
		return nil, mcpCfgErrf("invalid_json", "%s 顶层为 null：无安全合并点，不硬写", path)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, mcpCfgErrf("invalid_json", "%s JSON 尾部有多余内容（仅容空白），不硬写", path)
	}
	return doc, nil
}

// mcpCfgServersMap 取（或新建）顶层 mcpServers 对象；存在但非对象即拒绝（primewire
// 同判）。返回值与 doc 内该键同引用——对返回 map 的变更即对 doc 的变更。
func mcpCfgServersMap(doc map[string]any, path string) (map[string]any, *mcpCfgError) {
	v, exists := doc["mcpServers"]
	if !exists {
		m := map[string]any{}
		doc["mcpServers"] = m // 键不存在：新建，不动其他顶层键
		return m, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, mcpCfgErrf("invalid_json", "%s 的 mcpServers 非对象，不硬写", path)
	}
	return m, nil
}

// mcpCfgMarshalDoc 重编码（primewire 同款 MarshalIndent + 尾换行）：UseNumber 解码
// 的数值字面量原样回写；其他条目语义级原样（值不转型，键序按 encoding/json 规范化
// ——全文重序列化的已知形态，RFC §5「字节级不动按语义级解释」定案）。
func mcpCfgMarshalDoc(doc map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// mcpCfgDeepCopyJSON JSON 值深拷贝（marshal → UseNumber unmarshal；json.Number 字面
// 量往返保真）。撤回登记的原值快照存这份，绝不共享可变引用。
func mcpCfgDeepCopyJSON(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return out
}

// mcpCfgExistingEntries 既有条目 → 冲突检测输入（ExistingMcpEntry 契约）。env 值仅
// 内存用于 SWARM_TARGET 范围判定，绝不进应答；ExistingCommand 为路径非秘密可回显。
func mcpCfgExistingEntries(servers map[string]any) map[string]ExistingMcpEntry {
	out := make(map[string]ExistingMcpEntry, len(servers))
	for name, v := range servers {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		e := ExistingMcpEntry{}
		if c, ok := m["command"].(string); ok {
			e.Command = c
		}
		env := map[string]string{}
		if em, ok := m["env"].(map[string]any); ok {
			for k, ev := range em {
				if s, ok := ev.(string); ok {
					env[k] = s
				}
			}
		}
		e.Env = env
		out[name] = e
	}
	return out
}

// —— 请求契约（RFC §2；绝不含明文凭据） ——

// mcpCfgRequest 三端点公共请求体。
type mcpCfgRequest struct {
	Scope            string `json:"scope"`             // node|hub 必填
	Host             string `json:"host"`              // zcode|codex|其他（未知宿主通用手动）
	ConfigName       string `json:"config_name"`       // 空=按 scope 默认命名
	ConfigHome       string `json:"config_home"`       // 受控宿主配置根（绝对路径）
	ServerURL        string `json:"server_url"`        // 服务地址（ValidateServiceURL）
	McpCommandPath   string `json:"mcp_command_path"`  // 调用机上的 swarmmind-mcp 绝对路径
	CredentialRef    string `json:"credential_ref"`    // current_prime_token | placeholder
	ExpectedVersion  string `json:"expected_version"`  // 预期文件版本 hash（apply 必填）
	ReplaceConfirmed bool   `json:"replace_confirmed"` // 同名冲突显式确认替换
}

// normalize 归一入参并做公共校验（trim/小写/凭据引用缺省）。
func (req *mcpCfgRequest) normalize() *mcpCfgError {
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	req.ConfigName = strings.TrimSpace(req.ConfigName)
	req.ConfigHome = strings.TrimSpace(req.ConfigHome)
	req.McpCommandPath = strings.TrimSpace(req.McpCommandPath)
	req.CredentialRef = strings.TrimSpace(req.CredentialRef)
	req.ExpectedVersion = strings.TrimSpace(req.ExpectedVersion)
	if len(req.ConfigName) > mcpCfgMaxNameLen {
		return mcpCfgErrf("invalid_request", "config_name 超长（≤%d 字符）", mcpCfgMaxNameLen)
	}
	if req.Scope != ScopeNode && req.Scope != ScopeHub {
		return mcpCfgErrf("invalid_request", "scope %q 非法（合法值 node|hub），不猜测", req.Scope)
	}
	if err := ValidateServiceURL(req.ServerURL); err != nil {
		return mcpCfgErrf("invalid_request", "%v", err)
	}
	if req.CredentialRef == "" { // 缺省：node 取当前凭据引用，hub 只能占位（PAT 由中心管理）
		if req.Scope == ScopeHub {
			req.CredentialRef = mcpCredRefPlaceholder
		} else {
			req.CredentialRef = mcpCredRefCurrent
		}
	}
	if req.CredentialRef != mcpCredRefCurrent && req.CredentialRef != mcpCredRefPlaceholder {
		return mcpCfgErrf("invalid_request", "credential_ref %q 非法（合法值 current_prime_token|placeholder；本请求面绝不含明文凭据）", req.CredentialRef)
	}
	if req.CredentialRef == mcpCredRefCurrent && req.Scope == ScopeHub {
		return mcpCfgErrf("invalid_request", "scope=hub 的凭据为 PAT（由 hub 控制台人工管理）：credential_ref 只能 placeholder，node 不持有中心凭据")
	}
	return nil
}

// requireConfigHome zcode/codex 布局必需的受控根校验。
func (req *mcpCfgRequest) requireConfigHome() *mcpCfgError {
	if req.ConfigHome == "" {
		return mcpCfgErrf("invalid_request", "config_home 必填（受控宿主配置根，绝对路径）")
	}
	if !filepath.IsAbs(req.ConfigHome) {
		return mcpCfgErrf("invalid_request", "config_home 须为绝对路径（收到 %q）", req.ConfigHome)
	}
	return nil
}

// —— preview ——

// mcpCfgDiffEntryView 既有条目结构视图：env 只报 set/unset 标记，值绝不回显
// （RFC §3「只报结构差异不回显内容」——宿主配置旧密钥零明文金丝锁定）。
type mcpCfgDiffEntryView struct {
	Command string            `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // 键名 → set|unset（值不回显）
}

// mcpCfgDiff 脱敏差异（create=无既有条目；replace=将覆盖既有条目，apply 须显式确认）。
type mcpCfgDiff struct {
	Kind          string               `json:"kind"`
	NewEntry      map[string]any       `json:"new_entry,omitempty"` // 凭据位=脱敏引用/占位（W03-01 同形态）
	ExistingEntry *mcpCfgDiffEntryView `json:"existing_entry,omitempty"`
}

// mcpCfgWritability 可写性（can_auto_write = W03-01 GenFormAutoWrite 判定式：node+
// 同机+verified 宿主；同机为端点拓扑恒量）。reasons 只承载宿主/范围层不可写原因；
// 文件态问题（缺失/非法 JSON/不可写/链接）在 file.issues 如实呈现。
type mcpCfgWritability struct {
	CanAutoWrite      bool     `json:"can_auto_write"`
	SupportLevel      string   `json:"support_level"`
	Reasons           []string `json:"reasons,omitempty"`
	ApplyRequirements []string `json:"apply_requirements,omitempty"`
	Note              string   `json:"note,omitempty"`
}

// handleMcpCfgPreview POST /api/v1/local/mcp/config/preview。
func (s *Server) handleMcpCfgPreview(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpCfgRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-02-rfc.md §2）: "+err.Error())
		return
	}
	if e := req.normalize(); e != nil {
		writeMcpCfgErr(w, e, nil)
		return
	}
	payload, e := s.mcpCfgPreviewResponse(&req)
	if e != nil {
		writeMcpCfgErr(w, e, nil)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// mcpCfgPreviewResponse preview 应答组装（零写盘）。文件态问题（缺失/非法 JSON/
// 不可写/符号链接）一律 200+事实呈现（file.issues），apply 时才成为硬门——预览的
// 职责是把事实给足，不是替代 apply 校验。
func (s *Server) mcpCfgPreviewResponse(req *mcpCfgRequest) (map[string]any, *mcpCfgError) {
	// 凭据解析：current → 服务端当前 prime_token（仅在内存中进组装函数，即刻被
	// maskCredential 脱敏——W03-01 纪律：明文只作入参、输出面不保留引用）。
	cred := ""
	if req.CredentialRef == mcpCredRefCurrent {
		cred = s.primeToken()
		if cred == "" {
			return nil, mcpCfgErrf("invalid_request", "共享编排凭据未生成：请先在控制台轮换生成后再以 current_prime_token 预览")
		}
	}
	// 同机 command 探测（端点层探测 → PathProbedLocally 交接面）。
	cmdExists, cmdSymlink := mcpCfgProbeCommand(req.McpCommandPath)
	view, err := BuildMcpConfigView(McpConfigInput{
		Scope:             req.Scope,
		CallerEnv:         CallerSameMachine, // 本端点写本机宿主配置：拓扑恒同机（RFC §1）
		Host:              req.Host,
		ServerURL:         req.ServerURL,
		CallerMcpPath:     req.McpCommandPath,
		PathProbedLocally: cmdExists,
		Credential:        cred,
		ConfigName:        req.ConfigName,
	})
	if err != nil {
		return nil, mcpCfgErrf("invalid_request", "%v", err)
	}

	notes := []string{
		"预览零写盘：本应答不含任何明文凭据；既有条目只报结构差异（env 键 set/unset），绝不回显旧密钥。",
	}
	applyReqs := []string{
		"apply 须携带本次预览返回的 file.version（预期版本 hash，不符 409 file_changed）",
		"目标配置名已存在时须 replace_confirmed=true 显式确认替换（409 name_conflict 附建议名）",
		"自动写入要求 command 路径真实存在（占位配置落盘无用，primewire R4 同口径）",
	}
	writability := mcpCfgWritability{
		CanAutoWrite:      req.Scope == ScopeNode && req.Host == "zcode", // GenFormAutoWrite 判定式（同机恒真）
		SupportLevel:      view.HostSupportLevel,
		ApplyRequirements: applyReqs,
	}
	// hub 目标永不自动写（禁远程写调用者文件）：reasons 必须携带原因供页面渲染
	// 手动指引（终审建议②——此前 hub+已知宿主 reasons 为空，apply_requirements
	// 三条对 hub 场景误导；apply 对 hub 实为 400 manual_only）。
	if req.Scope == ScopeHub {
		writability.Reasons = append(writability.Reasons,
			"目标范围为 hub：中心端只提供调用机手动指引，宿主配置写入属调用机本机动作（禁远程写调用者文件）")
	}
	if cmdSymlink {
		notes = append(notes, "command 路径为符号链接：如实注记不拒绝（exec 由内核解析，本端不展开）")
	}

	var (
		fileState      *mcpCfgFileState
		diff           *mcpCfgDiff
		conflict       *ConfigNameConflict
		conflictAvail  bool
		issues         []string
		versionMatches = true
	)

	layout, knownHost := mcpCfgLayoutFor(req.Host)
	switch {
	case !knownHost:
		// 未知宿主：无已适配布局，不触文件（宁可手贴不猜布局乱写）。
		writability.Reasons = append(writability.Reasons,
			"未知宿主（manual_generic）不给自动写：无已适配配置布局，请按宿主文档手动粘贴通用模板")
		writability.Note = "未知宿主：本端点不触文件（file 缺省）；通用模板见 view.template.snippet，凭据位为脱敏占位。"
	case req.Host == "codex":
		// codex：文件探测事实给足（存在性/hash/可写性）；TOML 不解析（SPEC 三依赖
		// 白名单 + agentdetect 粗检裁决：检测层不做语法校验，本端点绝不冒认可安全
		// 合并）——差异与冲突检测不可用，如实标注。
		if e := req.requireConfigHome(); e != nil {
			return nil, e
		}
		writability.Reasons = append(writability.Reasons,
			"codex 为通用手动宿主（manual_generic_codex，未实测适配）：不支持自动写入，请按 TOML 模板手动粘贴")
		writability.Note = "结构粗检口径（非语法校验）：本端点不做 TOML 解析，差异与冲突检测不可用（conflict/diff 缺省）；语法是否合法由 codex CLI 解析暴露。"
		_, st, rerr := mcpCfgRead(req.ConfigHome, layout) // 内容不读用（TOML 不解析）——只要闸门事实
		st.Writable = false
		fileState = &st
		if rerr != nil {
			issues = append(issues, rerr.Error())
			break
		}
		if !st.Exists {
			issues = append(issues, "codex 配置文件不存在（"+st.Path+"）")
			break
		}
		if ok, why := mcpCfgProbeWritable(st.Path); ok {
			st.Writable = true
		} else {
			issues = append(issues, why)
		}
		issues = append(issues, "TOML 形态本端不解析：仅提供文件探测事实（存在性/版本 hash/可写性）")
	default: // zcode：JSON 真解析 → 冲突 + 脱敏差异
		if e := req.requireConfigHome(); e != nil {
			return nil, e
		}
		raw, st, rerr := mcpCfgRead(req.ConfigHome, layout)
		st.Writable = false
		fileState = &st
		switch {
		case rerr != nil:
			issues = append(issues, rerr.Error())
		case !st.Exists:
			issues = append(issues, "宿主配置不存在（"+st.Path+"；可能宿主未安装或未生成过配置）——本端点绝不创建宿主目录")
		default:
			if ok, why := mcpCfgProbeWritable(st.Path); ok {
				st.Writable = true
			} else {
				issues = append(issues, why)
			}
			doc, perr := mcpCfgParseJSONDoc(raw, st.Path)
			if perr != nil {
				issues = append(issues, perr.msg)
				break
			}
			servers, serr := mcpCfgServersMap(doc, st.Path)
			if serr != nil {
				issues = append(issues, serr.msg)
				break
			}
			existing := mcpCfgExistingEntries(servers)
			c := DetectConfigNameConflict(req.Scope, req.ConfigName, existing)
			conflict = &c
			conflictAvail = true
			diff = &mcpCfgDiff{Kind: "create", NewEntry: view.Template.Entry}
			if old, taken := existing[c.RequestedName]; taken {
				diff.Kind = "replace"
				ev := map[string]string{}
				for k := range old.Env {
					ev[k] = "set" // 值绝不回显（零明文金丝）
				}
				diff.ExistingEntry = &mcpCfgDiffEntryView{Command: old.Command, Env: ev}
				applyReqs = append(applyReqs, c.Resolution)
			}
		}
	}

	// 预期版本核对（可选入参：不符仍 200 返回最新状态+标注——预览只读无副作用）。
	if req.ExpectedVersion != "" {
		current := ""
		if fileState != nil {
			current = fileState.Version
		}
		versionMatches = fileState != nil && fileState.Exists && current == req.ExpectedVersion
		if !versionMatches {
			notes = append(notes, "预期版本与当前文件不符（文件已变化）：以下为最新状态，请以本次预览结果为准。")
		}
	}

	payload := map[string]any{
		"view":               view,
		"writability":        writability,
		"conflict_available": conflictAvail,
		"notes":              notes,
	}
	if fileState != nil {
		payload["file"] = fileState
	}
	if diff != nil {
		payload["diff"] = diff
	}
	if conflict != nil {
		payload["conflict"] = conflict
	}
	if req.ExpectedVersion != "" {
		payload["version_matches"] = versionMatches
	}
	return payload, nil
}

// —— 撤回登记（data_dir/mcp_config_writes.json，0600 原子写） ——

// mcpCfgWriteRecord 单次 apply 的撤回登记（同文件单槽：新 apply 覆盖旧登记，旧
// revert_id 失效——跨两次写入的撤回必然撞版本门，保留无益）。PrevEntry 为原值
// 快照（UseNumber 解码深拷贝，数值字面量保真）；HadPrev=false=删除标记（apply 前
// 无该条目，revert 即移除该键）。快照含宿主配置既有凭据：data_dir 0600 保护域
// （RFC §6，与 prime_token 同域纪律）。
type mcpCfgWriteRecord struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"` // UTC（RFC3339）
	Host        string `json:"host"`
	Scope       string `json:"scope"`
	ConfigName  string `json:"config_name"`
	ConfigHome  string `json:"config_home"`
	File        string `json:"file"`
	VersionPre  string `json:"version_pre"`  // apply 前版本（noop 收敛判定用）
	VersionPost string `json:"version_post"` // apply 后版本（revert 版本门）
	HadPrev     bool   `json:"had_prev"`
	PrevEntry   any    `json:"prev_entry,omitempty"` // 原值快照；nil+HadPrev=true 理论不可达（防御按删除处理）
}

// mcpCfgRegistry 登记文件结构。
type mcpCfgRegistry struct {
	Version int                 `json:"version"`
	Writes  []mcpCfgWriteRecord `json:"writes"`
}

func (s *Server) mcpCfgRegistryPath() string {
	return filepath.Join(s.cfg.DataDir, mcpCfgRegistryFileName)
}

// mcpCfgRegistryLoad 加载登记（不存在=空登记；解析失败/版本未知拒绝——登记不猜测）。
// 解码 UseNumber：原值快照的数值字面量随登记往返保真（否则 1.50 会被 float64 洗成
// 1.5、超精度整数被舍入——撤回恢复即破坏既有条目数值保真契约）。
func (s *Server) mcpCfgRegistryLoad() (*mcpCfgRegistry, error) {
	raw, err := os.ReadFile(s.mcpCfgRegistryPath())
	if errors.Is(err, os.ErrNotExist) {
		return &mcpCfgRegistry{Version: mcpCfgRegistryVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var reg mcpCfgRegistry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", s.mcpCfgRegistryPath(), err)
	}
	if reg.Version != mcpCfgRegistryVersion {
		return nil, fmt.Errorf("%s 版本 %d 未知（支持 %d），拒绝按未知格式撤回", s.mcpCfgRegistryPath(), reg.Version, mcpCfgRegistryVersion)
	}
	return &reg, nil
}

// mcpCfgRegistrySave 原子落盘（0600，复用 primewire atomicWriteFile0600——签名与
// 行为零变化）。
func (s *Server) mcpCfgRegistrySave(reg *mcpCfgRegistry) error {
	reg.Version = mcpCfgRegistryVersion
	raw, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	return atomicWriteFile0600(s.mcpCfgRegistryPath(), raw)
}

func (reg *mcpCfgRegistry) findByID(id string) (mcpCfgWriteRecord, bool) {
	for _, r := range reg.Writes {
		if r.ID == id {
			return r, true
		}
	}
	return mcpCfgWriteRecord{}, false
}

func (reg *mcpCfgRegistry) indexByFile(path string) int {
	for i, r := range reg.Writes {
		if r.File == path {
			return i
		}
	}
	return -1
}

// upsert 同文件单槽写入（同文件已有登记即覆盖——新 apply 使旧 revert_id 失效）。
func (reg *mcpCfgRegistry) upsert(rec mcpCfgWriteRecord) {
	if i := reg.indexByFile(rec.File); i >= 0 {
		reg.Writes[i] = rec
		return
	}
	reg.Writes = append(reg.Writes, rec)
}

// removeByID 按 ID 移除登记（apply 失败回滚与 revert 成功清理共用）。
func (reg *mcpCfgRegistry) removeByID(id string) bool {
	for i, r := range reg.Writes {
		if r.ID == id {
			reg.Writes = append(reg.Writes[:i], reg.Writes[i+1:]...)
			return true
		}
	}
	return false
}

// mcpCfgNewWriteID 撤回登记 ID（UTC 时间戳 + 随机后缀）。
func mcpCfgNewWriteID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "mcpw-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// —— apply ——

// handleMcpCfgApply POST /api/v1/local/mcp/config/apply。
func (s *Server) handleMcpCfgApply(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpCfgRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-02-rfc.md §2）: "+err.Error())
		return
	}
	if e := req.normalize(); e != nil {
		writeMcpCfgErr(w, e, nil)
		return
	}
	if e := s.mcpCfgApplyValidate(&req); e != nil {
		writeMcpCfgErr(w, e, nil)
		return
	}

	muMcpCfgFile.Lock()
	defer muMcpCfgFile.Unlock()
	resp, conflict, e := s.mcpCfgApplyDo(&req)
	if e != nil {
		extra := map[string]any(nil)
		if conflict != nil {
			extra = map[string]any{"conflict": conflict}
		}
		writeMcpCfgErr(w, e, extra)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpCfgApplyValidate apply 前置门（装配/宿主支持/凭据/命令/版本；文件态门在持锁后
// mcpCfgApplyDo 内——校验与写入同锁串行）。
func (s *Server) mcpCfgApplyValidate(req *mcpCfgRequest) *mcpCfgError {
	if s.cfg.DataDir == "" {
		return mcpCfgErrf("internal", "撤回登记未装配（装配方须传入 DataDir）：无登记不落盘")
	}
	// 自动写入口径 = W03-01 GenFormAutoWrite 判定式（node+同机+verified 宿主；
	// 同机为端点拓扑恒量）：codex/未知宿主/hub 目标一律手动。
	if req.Scope != ScopeNode {
		return mcpCfgErrf("manual_only",
			"scope=%s 不支持自动写入（GenFormAutoWrite 口径：中心目标配置的凭据为 PAT，由 hub 控制台人工管理）——请经 hub 控制台复制/下载人工应用", req.Scope)
	}
	if req.Host != "zcode" {
		return mcpCfgErrf("manual_only",
			"宿主 %q 不支持自动写入（manual_generic 口径：宁可手贴，不猜布局乱写）——请按模板手动粘贴；仅 verified_zcode 支持自动写入", req.Host)
	}
	if req.CredentialRef != mcpCredRefCurrent {
		return mcpCfgErrf("manual_only",
			"自动写入须携带可用凭据引用（current_prime_token）；占位凭据落盘无用（primewire R4 同口径）——临时输入请走手动模板粘贴")
	}
	if s.primeToken() == "" {
		return mcpCfgErrf("invalid_request", "共享编排凭据未生成：请先在控制台轮换生成后再 apply")
	}
	if e := req.requireConfigHome(); e != nil {
		return e
	}
	if req.McpCommandPath == "" {
		return mcpCfgErrf("command_missing", "mcp_command_path 必填：自动写入不落占位 command（占位配置无法启动）")
	}
	if !filepath.IsAbs(req.McpCommandPath) {
		return mcpCfgErrf("invalid_request", "mcp_command_path 须为绝对路径（收到 %q）", req.McpCommandPath)
	}
	if exists, _ := mcpCfgProbeCommand(req.McpCommandPath); !exists {
		return mcpCfgErrf("command_missing", "swarmmind-mcp 路径探测不存在：%s（请核对后重试；不写占位配置）", req.McpCommandPath)
	}
	if req.ExpectedVersion == "" {
		return mcpCfgErrf("expected_version_required", "apply 须携带 preview 返回的 expected_version（预期文件版本 hash，防 TOCTOU）")
	}
	if _, known := mcpCfgLayoutFor(req.Host); !known {
		return mcpCfgErrf("manual_only", "未知宿主 %q 无已适配布局：不猜布局乱写", req.Host)
	}
	return nil
}

// mcpCfgApplyDo 持锁执行（调用方持 muMcpCfgFile）：文件态门 → 版本门 → 冲突门 →
// 撤回登记先行 → 备份 → 原子合并写。写序列失败即中止并回滚登记（原文件保留）。
func (s *Server) mcpCfgApplyDo(req *mcpCfgRequest) (resp map[string]any, conflict *ConfigNameConflict, e *mcpCfgError) {
	layout, _ := mcpCfgLayoutFor(req.Host)
	raw, st, rerr := mcpCfgRead(req.ConfigHome, layout)
	if rerr != nil {
		return nil, nil, rerr // unsafe_path / file_unreadable 原样拒绝
	}
	if !st.Exists {
		return nil, nil, mcpCfgErrf("config_file_missing",
			"宿主配置不存在（%s）：本端点绝不创建宿主目录/文件——请先手动创建合法配置或核对 config_home", st.Path)
	}
	doc, perr := mcpCfgParseJSONDoc(raw, st.Path)
	if perr != nil {
		return nil, nil, perr // invalid_json：原文件保留不硬写
	}
	// 写闸门：宿主配置自身的写权限位如实尊重——只读文件=主人保护信号，rename
	// 虽可绕过文件权限位替换内容，但显式只读的在用配置不硬改（预览 writable=false
	// 与此处同一判定原语，前后口径一致；primewire 影子 home 无此语义，本端点面对
	// 主人在用配置取严格侧）。
	if ok, why := mcpCfgProbeWritable(st.Path); !ok {
		return nil, nil, mcpCfgErrf("write_failed", "宿主配置当前不可写（原文件未动）: %s", why)
	}
	servers, serr := mcpCfgServersMap(doc, st.Path)
	if serr != nil {
		return nil, nil, serr
	}
	// 版本门（防 TOCTOU 口径：以本次安全读的当前 hash 为准，RFC §4）。
	if st.Version != req.ExpectedVersion {
		return nil, nil, mcpCfgErrf("file_changed",
			"宿主配置已变化（当前版本与 expected_version 不符）：请重新 preview 后再 apply")
	}
	// 冲突门：不静默覆盖，替换须显式确认（W03-01 DetectConfigNameConflict 契约）。
	c := DetectConfigNameConflict(req.Scope, req.ConfigName, mcpCfgExistingEntries(servers))
	if c.Conflict && !req.ReplaceConfirmed {
		return nil, &c, mcpCfgErrf("name_conflict", "%s", c.Resolution)
	}
	name := c.RequestedName

	// 组装将写入条目（env 变量名沿用 W03-01 既有契约，绝不重命名）；凭据明文只在
	// 此 map 中存在，落盘后即弃，绝不进应答/登记回显面。
	contract := scopeEnv(req.Scope)
	entry := map[string]any{
		"command": req.McpCommandPath,
		"env": map[string]string{
			envTarget:         contract.Target,
			contract.URLVar:   req.ServerURL,
			contract.TokenVar: s.primeToken(),
		},
	}

	prevRaw, hadPrev := servers[name]
	var prevSnapshot any
	if hadPrev {
		prevSnapshot = mcpCfgDeepCopyJSON(prevRaw)
	}
	servers[name] = entry // 只动目标键；其他键为同一引用原样保留（数值字面量经 UseNumber 保真）
	out, merr := mcpCfgMarshalDoc(doc)
	if merr != nil {
		return nil, nil, mcpCfgErrf("write_failed", "重编码宿主配置失败（原文件未动）: %v", merr)
	}
	versionAfter := mcpCfgVersionHash(out)

	// 撤回登记先行（RFC §5 写序列：保证「apply 成功则 revert 必可用」——登记失败
	// 即中止，宿主配置未动）。
	reg, lerr := s.mcpCfgRegistryLoad()
	if lerr != nil {
		return nil, nil, mcpCfgErrf("write_failed", "撤回登记读取失败（宿主配置未动）: %v", lerr)
	}
	slot := reg.indexByFile(st.Path)
	var oldRec mcpCfgWriteRecord
	hadOld := slot >= 0
	if hadOld {
		oldRec = reg.Writes[slot]
	}
	rec := mcpCfgWriteRecord{
		ID: mcpCfgNewWriteID(), CreatedAt: nowUTCString(),
		Host: req.Host, Scope: req.Scope,
		ConfigName: name, ConfigHome: filepath.Clean(req.ConfigHome), File: st.Path,
		VersionPre: st.Version, VersionPost: versionAfter,
		HadPrev: hadPrev, PrevEntry: prevSnapshot,
	}
	reg.upsert(rec)
	if err := s.mcpCfgRegistrySave(reg); err != nil {
		return nil, nil, mcpCfgErrf("write_failed", "撤回登记落盘失败（宿主配置未动）: %v", err)
	}
	rollback := func() {
		if hadOld {
			reg.Writes[slot] = oldRec // 恢复被覆盖的旧登记
		} else {
			reg.removeByID(rec.ID) // 移除本次新追加的登记（slot=-1，按 ID 移除）
		}
		_ = s.mcpCfgRegistrySave(reg) // 回滚尽力而为；残记由版本门兜底（revert 走 noop/file_conflict）
	}

	// 备份先于主文件且失败即中止（primewire 纪律：不留无备份的改写）。
	if err := atomicWriteFile0600(st.Path+mcpCfgBackupSuffix, raw); err != nil {
		rollback()
		return nil, nil, mcpCfgErrf("write_failed", "写备份失败（主文件未动）: %v", err)
	}
	if err := atomicWriteFile0600(st.Path, out); err != nil {
		rollback()
		return nil, nil, mcpCfgErrf("write_failed", "写盘失败（原文件保留）: %v", err)
	}

	note := fmt.Sprintf("已合并写入 %s（原文件备份于 %s，重复写入覆盖旧备份）；宿主重载/重启后生效。", st.Path, st.Path+mcpCfgBackupSuffix) +
		"注意：后续轮换共享凭据只自动刷新管家影子 home，本文件需重新 apply 或手动更新。"
	if hadPrev {
		note += "本条目为显式确认替换（原值已入撤回登记，可 revert）。"
	}
	resp = map[string]any{
		"applied":           true,
		"host":              req.Host,
		"scope":             req.Scope,
		"file":              st.Path,
		"config_name":       name,
		"backup":            st.Path + mcpCfgBackupSuffix,
		"version_before":    st.Version,
		"version_after":     versionAfter,
		"replaced_existing": hadPrev,
		"revert": map[string]any{
			"revert_id":   rec.ID,
			"config_name": name,
			"file":        st.Path,
		},
		"note": note,
	}
	return resp, nil, nil
}

// —— revert ——

// mcpCfgRevertRequest revert 请求体。
type mcpCfgRevertRequest struct {
	RevertID string `json:"revert_id"`
}

// handleMcpCfgRevert POST /api/v1/local/mcp/config/revert：只撤本次 apply 登记条目。
func (s *Server) handleMcpCfgRevert(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpCfgRevertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"revert_id":"…"}: `+err.Error())
		return
	}
	req.RevertID = strings.TrimSpace(req.RevertID)
	if req.RevertID == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "revert_id 必填（apply 应答 revert.revert_id）")
		return
	}
	if s.cfg.DataDir == "" {
		writeErr(w, http.StatusServiceUnavailable, "internal", "撤回登记未装配（装配方须传入 DataDir）")
		return
	}

	muMcpCfgFile.Lock()
	defer muMcpCfgFile.Unlock()
	reg, lerr := s.mcpCfgRegistryLoad()
	if lerr != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", "撤回登记读取失败: "+lerr.Error()) // 与 apply 侧同场景同码（RFC §9：登记失败=write_failed；internal 仅限 DataDir 未装配）——终审建议①
		return
	}
	rec, ok := reg.findByID(req.RevertID)
	if !ok {
		writeErr(w, http.StatusNotFound, "revert_not_found",
			"撤回登记不存在（已撤回、或被同文件更新的 apply 覆盖）")
		return
	}
	resp, e := s.mcpCfgRevertDo(reg, rec)
	if e != nil {
		writeMcpCfgErr(w, e, nil)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpCfgRevertDo 持锁执行（调用方持 muMcpCfgFile）：版本三路（RFC §4/§6）——
// 当前==apply 后版本 → 撤回；==apply 前版本 → 登记作废 noop；其余（用户后改）→
// 409 file_conflict，绝不回滚整文件。
func (s *Server) mcpCfgRevertDo(reg *mcpCfgRegistry, rec mcpCfgWriteRecord) (resp map[string]any, e *mcpCfgError) {
	layout, known := mcpCfgLayoutFor(rec.Host)
	if !known {
		return nil, mcpCfgErrf("internal", "撤回登记宿主 %q 无已知布局（登记异常，拒绝执行）", rec.Host)
	}
	// 单次安全读：闸门、版本门与撤回合并基于同一次读取的字节（不二次读——多一次
	// 读就多一倍 TOCTOU 窗口）。
	raw, st, rerr := mcpCfgRead(rec.ConfigHome, layout)
	if rerr != nil {
		return nil, rerr // unsafe_path / file_unreadable：状态异常拒绝（原文件未动）
	}
	if !st.Exists {
		return nil, mcpCfgErrf("file_conflict",
			"宿主配置已被删除（%s）：revert 不代建、不回滚整文件，请核对后手动处理", st.Path)
	}
	// 写闸门（与 apply 同一判定原语）：只读文件不硬改。
	if ok, why := mcpCfgProbeWritable(st.Path); !ok {
		return nil, mcpCfgErrf("write_failed", "宿主配置当前不可写（原文件未动）: %s", why)
	}
	switch st.Version {
	case rec.VersionPost:
		// 正常撤回路径（继续执行下方合并/写盘）。
	case rec.VersionPre:
		// apply 实际未落盘的残迹（登记已写、主文件写失败后回滚残窗等）：登记作废。
		reg.removeByID(rec.ID)
		resp = map[string]any{
			"reverted":      true,
			"restored":      "noop",
			"file":          st.Path,
			"config_name":   rec.ConfigName,
			"version_after": st.Version,
			"note":          "宿主配置保持 apply 前版本（本次 apply 未实际生效），撤回登记已作废。",
		}
		if err := s.mcpCfgRegistrySave(reg); err != nil {
			resp["warning"] = "登记作废已生效，但落盘失败（重复 revert 将重走 noop 收敛）: " + err.Error()
		}
		return resp, nil
	default:
		return nil, mcpCfgErrf("file_conflict",
			"apply 之后宿主配置已被修改（当前版本与撤回登记不符）：revert 只撤本次条目、绝不回滚整文件——请核对 %s 后手动处理，或重新 apply 后再撤", st.Path)
	}
	doc, derr := mcpCfgParseJSONDoc(raw, st.Path)
	if derr != nil {
		// 版本门已过仍解析失败：文件在两读之间被换为非法内容（残窗）——如实拒绝。
		return nil, mcpCfgErrf("file_conflict", "宿主配置解析失败（原文件保留，请手动核对）: %s", derr.msg)
	}
	servers, serr := mcpCfgServersMap(doc, st.Path)
	if serr != nil {
		return nil, mcpCfgErrf("file_conflict", "宿主配置 mcpServers 非对象（原文件保留，请手动核对）: %s", serr.msg)
	}
	if rec.HadPrev && rec.PrevEntry != nil {
		servers[rec.ConfigName] = rec.PrevEntry // 原值快照回写（数值字面量经 UseNumber 保真）
	} else {
		delete(servers, rec.ConfigName) // 删除标记：仅移除本键
	}
	out, merr := mcpCfgMarshalDoc(doc)
	if merr != nil {
		return nil, mcpCfgErrf("write_failed", "重编码宿主配置失败（原文件未动）: %v", merr)
	}
	if err := atomicWriteFile0600(st.Path+mcpCfgBackupSuffix, raw); err != nil {
		return nil, mcpCfgErrf("write_failed", "写备份失败（主文件未动）: %v", err)
	}
	if err := atomicWriteFile0600(st.Path, out); err != nil {
		return nil, mcpCfgErrf("write_failed", "撤回写盘失败（原文件保留）: %v", err)
	}
	restored := "deleted"
	if rec.HadPrev && rec.PrevEntry != nil {
		restored = "previous_value"
	}
	// 撤回已生效后才清登记：清理失败不虚报失败（200+warning；重复 revert 走 noop
	// 收敛，无双重撤回风险——登记版本门兜底）。
	reg.removeByID(rec.ID)
	resp = map[string]any{
		"reverted":      true,
		"restored":      restored,
		"file":          st.Path,
		"config_name":   rec.ConfigName,
		"version_after": mcpCfgVersionHash(out),
		"note":          "已撤回本次 apply 写入的条目（原文件备份于 " + st.Path + mcpCfgBackupSuffix + "）；宿主重载/重启后生效。",
	}
	if err := s.mcpCfgRegistrySave(reg); err != nil {
		resp["warning"] = "撤回已生效，但撤回登记清理失败（记录保留；重复 revert 将按 noop 收敛）: " + err.Error()
	}
	return resp, nil
}
