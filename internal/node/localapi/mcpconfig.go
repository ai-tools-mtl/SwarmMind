// MCP 接入配置视图模型与宿主模板（W03-01，docs/tasks/v3/W03-mcp-onboarding.md；
// 上游 docs/PRODUCT-PLAN-v3.md §12 P07 六分区字段语义）。
//
// 本文件是纯数据模型层：不接路由（端点属 W03-02/04）、不读宿主配置盘、不探测
// 进程——一切输入由调用方显式给出，从结构上杜绝「服务器程序路径代填调用机路
// 径」「远端路径冒充已验证」类拓扑谎言。既有 zcode 接线事实源是 primewire.go
// （<home>/.zcode/cli/setting.json 顶层 mcpServers 合并，t20261023-62a2 起实测
// 在用）；本文件不复制其写盘逻辑，只产出与其同布局的无秘密模板。
//
// 凭据纪律（本文件最高约束，改动前先读本段）：
//   - 视图模型/宿主模板的任何输出结构体、map、序列化 JSON 中**绝不含明文凭据**；
//     凭据位恒为脱敏占位（maskCredential 形态如 ab********，或明示占位文案）；
//   - 明文只允许作为组装函数入参在函数内被 maskCredential 脱敏后即刻抛弃，
//     输出面不保留任何引用（mcpconfig_test.go 以「预埋假密钥 grep 输出」锁定）；
//   - env 变量名沿用既有契约（cmd/swarmmind-mcp main.go 装配分发）：node 目标
//     SWARM_TARGET/SWARM_NODE_URL/SWARM_NODE_PRIME_TOKEN，hub 目标 SWARM_TARGET/
//     SWARM_HUB_URL/SWARM_HUB_TOKEN——绝不重命名（PRODUCT-PLAN §12.3）；
//   - 本机编排凭据绝不使用 local_token（主人管理凭证，prime.go mcpConfig 同
//     口径，不下发）；中心凭据为 PAT，绝不使用 admin_token 或 Web 登录 cookie。
//
// 宿主支持级别纪律（任务书原话：宿主模板至少验证用户实际使用的 zcode，Codex
// 模板若未验证标通用手动而非支持；不得把 MCP 宿主格式不一致硬套 JSON）：
//   - zcode = verified_zcode（已实测适配：primewire 自动接线持续写该布局，
//     primewire_test 实测覆盖，布局/合并语义以该实现为事实源）；
//   - codex = manual_generic_codex（通用手动：TOML 形态按 codex 通用
//     mcp_servers 结构输出，未在真实 codex 上验证，文档字符串如实标注）；
//   - 其余/未知宿主 = manual_generic（通用 JSON 手动模板 + 未验证标注，
//     绝不冒认已适配）。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// —— 目标范围（P07 §12 ①：调用本机 / 调用中心）——

const (
	ScopeNode = "node" // 调用本机 node（SWARM_TARGET=node）
	ScopeHub  = "hub"  // 调用中心 hub（SWARM_TARGET=hub）
)

// —— 调用环境（P07 §12 ②：外部调用者在哪里）——

const (
	CallerSameMachine = "same_machine" // MCP 适配进程与目标服务同机（默认可探测本机路径）
	CallerRemote      = "remote"       // MCP 适配进程在外部调用机（路径/可达性均待外部验证）
)

// —— 生成形态（P07 §12 ④：配置生成/应用）——

const (
	GenFormPreview   = "preview"    // 预览（脱敏差异）
	GenFormCopy      = "copy"       // 复制（明文仅来自本次人工授予/临时输入，由端点层承担）
	GenFormDownload  = "download"   // 下载到调用者机器
	GenFormAutoWrite = "auto_write" // 自动写入宿主配置（仅同机+已验证宿主；hub 范围永不）
)

// —— 路径验证状态（P07 §12 ②：远端路径标「待外部验证」）——

const (
	PathStatusLocalProbed        = "local_probed"        // 同机且经本地探测存在
	PathStatusUserProvided       = "user_provided"       // 用户手填，本端未验证
	PathStatusExternalUnverified = "external_unverified" // 远端路径：本端无法校验，标待外部验证
	PathStatusPlaceholder        = "placeholder"         // 占位：待用户补填
)

// —— 宿主支持级别（任务书：verified_zcode / manual_generic_codex 二词锁定）——

const (
	SupportLevelVerifiedZcode      = "verified_zcode"       // zcode：实测适配（primewire 事实源）
	SupportLevelManualGenericCodex = "manual_generic_codex" // codex：通用手动，未实测适配
	SupportLevelManualGeneric      = "manual_generic"       // 未知宿主：通用 JSON 手动，未验证
)

// —— env 变量名既有契约（绝不重命名；cmd/swarmmind-mcp main.go 同名）——

const (
	envTarget    = "SWARM_TARGET"
	envNodeURL   = "SWARM_NODE_URL"
	envNodeToken = "SWARM_NODE_PRIME_TOKEN"
	envHubURL    = "SWARM_HUB_URL"
	envHubToken  = "SWARM_HUB_TOKEN"
)

// mcpCallerPathPlaceholder 调用机侧 MCP 可执行路径占位符。视图模型绝不代填
// 服务器路径：本文件不探测进程，路径只能来自调用方显式输入；远端路径无法由
// 网页校验存在，一律待用户手填并标「待外部验证」（P07 §12）。
const mcpCallerPathPlaceholder = "<调用机上的 swarmmind-mcp 绝对路径（本视图不代填服务器路径，待填写）>"

// templateCredentialPlaceholder 模板凭据位空值占位（无有效凭据时预览不可直接
// 调用——P07 §12：预览无有效凭据时用占位符并标不可直接调用）。
const templateCredentialPlaceholder = "<凭据占位：由人工授权/轮换获取一次性凭据后粘贴，本模板不含明文>"

// scopeEnvContract 目标范围对应的 env 三键契约（变量名既有契约，绝不重命名）。
type scopeEnvContract struct {
	Target   string // SWARM_TARGET 的值
	URLVar   string // 服务地址变量名
	TokenVar string // 凭据变量名
}

// scopeEnv 范围 → env 契约（node 缺省；未知范围由调用方先行校验拒绝）。
func scopeEnv(scope string) scopeEnvContract {
	if scope == ScopeHub {
		return scopeEnvContract{Target: ScopeHub, URLVar: envHubURL, TokenVar: envHubToken}
	}
	return scopeEnvContract{Target: ScopeNode, URLVar: envNodeURL, TokenVar: envNodeToken}
}

// DefaultConfigName 默认配置名（同一宿主允许本机/中心并存，命名以后缀区分；
// P07 §12：配置名称默认区分本机/中心实例）。未知范围返回空串。
func DefaultConfigName(scope string) string {
	switch scope {
	case ScopeNode:
		return "swarmmind-node"
	case ScopeHub:
		return "swarmmind-hub"
	default:
		return ""
	}
}

// ValidateServiceURL 服务地址格式校验（P07 §12：校验 scheme 与格式）。纯校验
// 不发起任何网络请求——本包不提供不受限任意 URL 探测接口。
func ValidateServiceURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("服务地址必填")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("服务地址非法: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("服务地址 scheme 须为 http/https（得 %q）", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("服务地址缺少 host:port")
	}
	return nil
}

// isLoopbackServiceURL 服务地址是否指向回环（AC07：错误回环地址不得显示为外部
// 已验证——远端调用机 + 回环地址是危险拓扑，视图必须明示不可达）。
func isLoopbackServiceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "::1" || h == "127.0.0.1" || strings.HasPrefix(h, "127.")
}

// templateCredentialRef 模板凭据位（脱敏纪律出口）：非空 → maskCredential 脱敏
// 引用；空 → 明示占位文案。任何路径不返回明文。
func templateCredentialRef(cred string) string {
	if cred == "" {
		return templateCredentialPlaceholder
	}
	return maskCredential(cred)
}

// McpHostTemplate 宿主模板：按宿主输出 mcpServers 配置条目与该宿主形态的文本
// 模板。Entry/Snippet 凭据位恒为脱敏占位（纪律见文件头）；SupportLevel/Verified
// 是对接入能力的唯一真相口径，UI 与端点层不得越级宣称支持。
type McpHostTemplate struct {
	Host         string `json:"host"`          // 宿主标识（zcode | codex | unknown | 调用方给定的未知宿主名）
	SupportLevel string `json:"support_level"` // verified_zcode | manual_generic_codex | manual_generic
	Verified     bool   `json:"verified"`      // 是否实测适配（仅 zcode 为 true）
	Format       string `json:"format"`        // 模板文本形态：json | toml
	// ConfigLayout 该宿主配置文件布局（人话，含事实源说明）。
	ConfigLayout string `json:"config_layout"`
	// EntryName 默认/给定配置名（mcpServers 内的键）。
	EntryName string `json:"entry_name"`
	// Entry 配置条目本体（command + env），凭据位=脱敏占位。zcode 布局下该值
	// 与 primewire 自动接线写入 mcpServers.<键> 的结构同形。
	Entry map[string]any `json:"entry"`
	// Snippet 该宿主形态的完整文本模板（JSON 或 TOML），可直接粘贴；凭据位为
	// 脱敏占位——粘贴前须由用户以一次性凭据替换（端点层复制/下载明文流程属
	// W03-02/04，本层绝不产出明文）。
	Snippet string `json:"snippet"`
	// Note 支持级别判定依据（人话，供页面如实展示；不冒认已适配）。
	Note string `json:"note"`
}

// HostTemplateInput 宿主模板输入。Credential 为明文入参，仅在函数内脱敏后
// 即刻抛弃，输出不保留任何明文引用。
type HostTemplateInput struct {
	Host       string // zcode | codex | 任意（未知宿主走通用手动）
	Scope      string // node | hub
	ServerURL  string // 服务地址（调用机可达口径，由调用方校验后传入）
	McpPath    string // 调用机上的 swarmmind-mcp 绝对路径；空 = 占位符
	Credential string // 明文凭据（仅用于生成脱敏引用，绝不进输出）
	ConfigName string // 配置名；空 = 默认命名（scopeEnv 合法时）
}

// BuildHostTemplate 组装宿主模板。scope 非法返回错误（不猜测）；宿主未知不是
// 错误——按 manual_generic 降级并如实标注。
func BuildHostTemplate(in HostTemplateInput) (McpHostTemplate, error) {
	if in.Scope != ScopeNode && in.Scope != ScopeHub {
		return McpHostTemplate{}, fmt.Errorf("目标范围 %q 非法（合法值 node|hub），不猜测", in.Scope)
	}
	contract := scopeEnv(in.Scope)
	name := strings.TrimSpace(in.ConfigName)
	if name == "" {
		name = DefaultConfigName(in.Scope)
	}
	command := strings.TrimSpace(in.McpPath)
	if command == "" {
		command = mcpCallerPathPlaceholder
	}
	credRef := templateCredentialRef(in.Credential)

	env := map[string]string{
		envTarget:         contract.Target,
		contract.URLVar:   in.ServerURL,
		contract.TokenVar: credRef,
	}
	entry := map[string]any{"command": command, "env": env}

	host := strings.ToLower(strings.TrimSpace(in.Host))
	t := McpHostTemplate{
		EntryName: name,
		Entry:     entry,
	}
	switch host {
	case "zcode":
		// 事实源：primewire.go wirePrimeConfig——<home>/.zcode/cli/setting.json
		// 顶层 mcpServers 合并（t20261023-62a2 起自动接线持续写该布局，实测覆盖）。
		t.Host = "zcode"
		t.SupportLevel = SupportLevelVerifiedZcode
		t.Verified = true
		t.Format = "json"
		t.ConfigLayout = "<home>/.zcode/cli/setting.json 顶层 mcpServers 对象（与自动接线同布局；合并只动本条目，其他键原样保留，数值按原字面量保真）"
		t.Note = "已实测适配（verified_zcode）：declare/rotate 自动接线（primewire.go）持续写入该布局并有实测覆盖，本模板以其为事实源。注意：自动接线的历史条目名为固定 swarmmind，手动条目默认名按本机/中心后缀区分。"
		snippet, err := mcpServersJSONSnippet(name, entry)
		if err != nil {
			return McpHostTemplate{}, err
		}
		t.Snippet = snippet
	case "codex":
		// 任务书：Codex 模板若未验证标通用手动而非支持。TOML 形态按 codex 通用
		// mcp_servers 结构输出，未在真实 codex 上验证——绝不标 verified。
		t.Host = "codex"
		t.SupportLevel = SupportLevelManualGenericCodex
		t.Verified = false
		t.Format = "toml"
		t.ConfigLayout = "~/.codex/config.toml 的 [mcp_servers.<配置名>] 段（TOML 形态）"
		t.Note = "通用手动（manual_generic_codex）：TOML 模板未实测适配，未在真实 codex 上验证；请以 codex 官方文档核对键名与合并点后再粘贴，本模板不构成支持承诺。"
		t.Snippet = mcpServersTOMLSnippet(name, command, env)
	default:
		// 未知宿主：通用 JSON 手动模板 + 未验证标注，绝不冒认已适配。
		if host == "" {
			host = "unknown"
		}
		t.Host = host
		t.SupportLevel = SupportLevelManualGeneric
		t.Verified = false
		t.Format = "json"
		t.ConfigLayout = "宿主配置文件内的 mcpServers JSON 对象（常见 stdio MCP 宿主形态；具体文件位置以宿主文档为准）"
		t.Note = "未知宿主（manual_generic）：仅提供通用 JSON 手动模板，该宿主格式未验证，不冒认已适配；请按宿主文档核对合并点与键名。"
		snippet, err := mcpServersJSONSnippet(name, entry)
		if err != nil {
			return McpHostTemplate{}, err
		}
		t.Snippet = snippet
	}
	return t, nil
}

// mcpServersJSONSnippet zcode/通用宿主的 JSON 模板文本（顶层 mcpServers.<名>）。
// encoding/json 键序确定性排序，同一输入产出逐字节一致（fixture 可锁定）。
// SetEscapeHTML(false)：占位符/路径里的 <、> 按原字面输出——\u003c 转义虽语义
// 等价，但复制粘贴模板人不可读，且与 TOML 形态 strconv.Quote（不转义）不一致。
func mcpServersJSONSnippet(name string, entry map[string]any) (string, error) {
	doc := map[string]any{"mcpServers": map[string]any{name: entry}}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("编码 JSON 模板失败: %w", err)
	}
	return b.String(), nil
}

// mcpServersTOMLSnippet codex 手动模板文本（[mcp_servers.<名>] + env 子表）。
// 手工拼接；键名非裸字符时加引号，值一律 strconv.Quote 基本字符串。
func mcpServersTOMLSnippet(name, command string, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s.%s]\n", tomlKey("mcp_servers"), tomlKey(name))
	fmt.Fprintf(&b, "command = %s\n", strconv.Quote(command))
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("\n")
	fmt.Fprintf(&b, "[%s.%s.env]\n", tomlKey("mcp_servers"), tomlKey(name))
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", tomlKey(k), strconv.Quote(env[k]))
	}
	return b.String()
}

// tomlKey TOML 键：裸键（[A-Za-z0-9_-]+）原样，否则基本字符串加引号。
func tomlKey(k string) string {
	if k == "" {
		return strconv.Quote(k)
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return strconv.Quote(k)
		}
	}
	return k
}

// McpConfigView P07 §12 六分区字段全覆盖的配置视图模型（纯数据，无行为、无
// 路由、无 IO）。凭据纪律：本结构任何字段**绝不含明文凭据**——CredentialRef
// 恒为脱敏引用（maskCredential 形态），Template.Entry/Snippet 凭据位恒为脱敏
// 占位；明文只在授权/轮换操作结果中一次展示（mcpaccess.go 口径），后续端点层
// 复制/下载明文也仅来自当次人工授予/临时输入，绝不由本视图携带。
type McpConfigView struct {
	// ① 范围/拓扑：调用本机 / 调用中心。
	TargetScope      string `json:"target_scope"`       // node | hub
	TargetScopeLabel string `json:"target_scope_label"` // 调用本机 | 调用中心

	// ② 调用环境：MCP 适配进程在哪里、宿主类型、调用机路径与验证状态、服务地址。
	CallerEnv      string `json:"caller_env"`       // same_machine | remote
	CallerEnvLabel string `json:"caller_env_label"` // 与目标同机 | 外部调用机
	HostKind       string `json:"host_kind"`        // 宿主标识（视图模板同字段）
	// HostSupportLevel/HostVerified 宿主适配真相口径（verified_zcode 等）。
	HostSupportLevel string `json:"host_support_level"`
	HostVerified     bool   `json:"host_verified"`
	// McpCommandPath 调用机上的 swarmmind-mcp 绝对路径。服务器路径绝不代填：
	// 本文件不探测，路径仅来自调用方输入，空则占位。
	McpCommandPath string `json:"mcp_command_path"`
	McpPathStatus  string `json:"mcp_path_status"` // PathStatus* 常量
	ServiceURL     string `json:"service_url"`

	// ③ 人工授权：授权引用（恒脱敏）与其 env 变量名。
	// CredentialRef 脱敏纪律字段：绝不放明文（结构级契约，测试 grep 锁定）。
	CredentialRef    string `json:"credential_ref"`
	CredentialEnvVar string `json:"credential_env_var"` // SWARM_NODE_PRIME_TOKEN | SWARM_HUB_TOKEN

	// ④ 名称与生成形态。
	ConfigName          string   `json:"config_name"`
	ConfigNameIsDefault bool     `json:"config_name_is_default"`
	GenerationForms     []string `json:"generation_forms"` // preview/copy/download[/auto_write]

	// 宿主模板（凭据位脱敏占位）。
	Template McpHostTemplate `json:"template"`

	// Notes 拓扑/范围人话提示（含 AC07 回环陷阱、远端转发前置说明等）。
	Notes []string `json:"notes"`
}

// McpConfigInput 配置视图组装输入。Credential 为明文入参，仅在 Build 内脱敏
// 后即刻抛弃，输出不保留任何明文引用。
type McpConfigInput struct {
	Scope     string // node | hub（必填，非法报错）
	CallerEnv string // same_machine | remote（必填，非法报错）
	Host      string // zcode | codex | 任意（未知宿主走通用手动）
	ServerURL string // 服务地址（必填，ValidateServiceURL 校验）
	// CallerMcpPath 调用机上的 swarmmind-mcp 绝对路径；空 = 占位符。同机探测
	// 结果由端点层（W03-02）传入并置 PathProbedLocally，本函数不探测。
	CallerMcpPath string
	// PathProbedLocally 路径是否经同机本地探测存在（仅 same_machine 语义有效；
	// remote 下本端一律无法验证，状态强制 external_unverified）。
	PathProbedLocally bool
	Credential        string // 明文凭据（仅用于生成脱敏引用，绝不进输出）
	ConfigName        string // 配置名；空 = 默认命名
}

// BuildMcpConfigView 组装 P07 §12 配置视图模型。枚举型字段（范围/调用环境）与
// 服务地址非法即报错——视图模型不猜测；宿主未知不是错误（manual_generic 降级）。
//
// 自动写入口径（P07 §12 ④：仅本机可验证的支持宿主可自动写入；hub 端复制/下载
// 不自动应用；禁止远程写调用者文件）：GenerationForms 含 auto_write 当且仅当
// node 范围 + 同机 + 宿主已验证（zcode）。
func BuildMcpConfigView(in McpConfigInput) (McpConfigView, error) {
	if in.Scope != ScopeNode && in.Scope != ScopeHub {
		return McpConfigView{}, fmt.Errorf("目标范围 %q 非法（合法值 node|hub），不猜测", in.Scope)
	}
	if in.CallerEnv != CallerSameMachine && in.CallerEnv != CallerRemote {
		return McpConfigView{}, fmt.Errorf("调用环境 %q 非法（合法值 same_machine|remote），不猜测", in.CallerEnv)
	}
	if err := ValidateServiceURL(in.ServerURL); err != nil {
		return McpConfigView{}, err
	}
	contract := scopeEnv(in.Scope)

	tmpl, err := BuildHostTemplate(HostTemplateInput{
		Host:       in.Host,
		Scope:      in.Scope,
		ServerURL:  in.ServerURL,
		McpPath:    in.CallerMcpPath,
		Credential: in.Credential,
		ConfigName: in.ConfigName,
	})
	if err != nil {
		return McpConfigView{}, err
	}

	// 路径与验证状态（P07 §12：远端路径无法由网页校验存在，标待外部验证）。
	// 先 TrimSpace 再判定（与 BuildHostTemplate 内同口径）：McpCommandPath、
	// Entry.command 与 Template.Snippet 三处同一 command 逐字节一致，不因调用
	// 方入参首尾空白漂移（fixture 锁定）。
	trimmed := strings.TrimSpace(in.CallerMcpPath)
	path, pathStatus := trimmed, ""
	switch {
	case trimmed == "":
		path, pathStatus = mcpCallerPathPlaceholder, PathStatusPlaceholder
	case in.CallerEnv == CallerRemote:
		pathStatus = PathStatusExternalUnverified
	case in.PathProbedLocally:
		pathStatus = PathStatusLocalProbed
	default:
		pathStatus = PathStatusUserProvided
	}
	tmpl.Entry["command"] = path // 与路径状态口径一致（占位/同 Snippet 逐字节一致）

	// 生成形态：基础三形态恒有；auto_write 仅 node+同机+已验证宿主。
	forms := []string{GenFormPreview, GenFormCopy, GenFormDownload}
	if in.Scope == ScopeNode && in.CallerEnv == CallerSameMachine && tmpl.Verified {
		forms = append(forms, GenFormAutoWrite)
	}

	// 配置名默认口径。
	name := tmpl.EntryName
	isDefault := strings.TrimSpace(in.ConfigName) == ""

	view := McpConfigView{
		TargetScope:         in.Scope,
		TargetScopeLabel:    scopeLabel(in.Scope),
		CallerEnv:           in.CallerEnv,
		CallerEnvLabel:      callerEnvLabel(in.CallerEnv),
		HostKind:            tmpl.Host,
		HostSupportLevel:    tmpl.SupportLevel,
		HostVerified:        tmpl.Verified,
		McpCommandPath:      path,
		McpPathStatus:       pathStatus,
		ServiceURL:          in.ServerURL,
		CredentialRef:       maskCredential(in.Credential),
		CredentialEnvVar:    contract.TokenVar,
		ConfigName:          name,
		ConfigNameIsDefault: isDefault,
		GenerationForms:     forms,
		Template:            tmpl,
		Notes:               viewNotes(in, isLoopbackServiceURL(in.ServerURL)),
	}
	return view, nil
}

// scopeLabel / callerEnvLabel 人话标签（P07 §12 顶部固定口径）。
func scopeLabel(scope string) string {
	if scope == ScopeHub {
		return "调用中心"
	}
	return "调用本机"
}

func callerEnvLabel(env string) string {
	if env == CallerRemote {
		return "外部调用机"
	}
	return "与目标同机"
}

// viewNotes 拓扑/范围人话提示（视图级 note；宿主适配说明在 Template.Note）。
func viewNotes(in McpConfigInput, loopback bool) []string {
	var notes []string
	notes = append(notes,
		"本视图不含明文凭据：凭据引用恒脱敏，明文仅在授权/轮换操作结果中一次展示；复制/下载明文仅来自当前人工授予或临时输入。",
	)
	if in.CallerEnv == CallerRemote {
		notes = append(notes,
			"MCP 适配进程运行在外部调用机上：服务器探测成功不代表外部可达；跨机调用须经已配置的安全转发访问，默认不开放管理面公网入口。",
		)
		if loopback {
			// AC07：错误回环地址不得显示为外部已验证——拓扑陷阱必须显式说明。
			notes = append(notes,
				"警告：服务地址为回环地址——127.0.0.1/localhost 属 MCP 进程所在机器（即外部调用机），从调用机连不到本端；外部调用请改用调用机可达的转发地址。",
			)
		}
	}
	if in.Scope == ScopeHub {
		notes = append(notes,
			"中心模式凭据为管理员授予的 PAT（smsw_…），绝不使用 admin_token 或 Web 登录 cookie；Hub 端仅预览/复制/下载，不自动写宿主配置。",
		)
	} else {
		notes = append(notes,
			"本机模式凭据为主人授予的本机编排调用凭据，绝不使用 local_token（主人管理凭证不进 MCP 配置）。",
		)
	}
	return notes
}

// —— 配置名称冲突模型（同一宿主允许本机/中心并存；冲突显式返回，绝不建议
// 静默覆盖——P07 §12：不静默覆盖同名已有条目，冲突可改名或显式确认替换）——

// ExistingMcpEntry 宿主配置内既有 mcpServers 条目的最小视图（检测方从宿主
// 配置解析后提供；W03-02 preview 使用）。
type ExistingMcpEntry struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
}

// 配置名称冲突类别。
const (
	ConflictKindSameScope    = "same_scope"    // 同名条目已存在且同指向本范围（多为旧版本）
	ConflictKindCrossScope   = "cross_scope"   // 同名条目已存在且指向另一范围（本机/中心撞名）
	ConflictKindUnknownScope = "unknown_scope" // 同名条目存在但目标范围无法判定
)

// ExistingEntryScope 既有条目指向的目标范围（env SWARM_TARGET 判定；判定不了
// 返回 unknown——primewire 自动接线写入的条目带 SWARM_TARGET=node，可正确归类）。
func ExistingEntryScope(e ExistingMcpEntry) string {
	switch strings.ToLower(strings.TrimSpace(e.Env[envTarget])) {
	case ScopeNode:
		return ScopeNode
	case ScopeHub:
		return ScopeHub
	default:
		return "unknown"
	}
}

// ConfigNameConflict 配置名称冲突检测结果。Conflict=false 表示可直接使用；
// Conflict=true 时 Resolution 给出人话处置（改名并存或显式确认替换）——绝不
// 静默覆盖，替换必须由用户显式确认（端点层把确认动作落到 apply 语义，W03-02）。
type ConfigNameConflict struct {
	Conflict      bool   `json:"conflict"`
	RequestedName string `json:"requested_name"`
	// Kind 冲突类别（Conflict=true 时有效）：same_scope | cross_scope |
	// unknown_scope。
	Kind string `json:"kind,omitempty"`
	// ExistingScope 既有条目指向（node|hub|unknown；Conflict=false 时空）。
	ExistingScope string `json:"existing_scope,omitempty"`
	// ExistingCommand 既有条目 command（供差异辨认，不含凭据）。
	ExistingCommand string `json:"existing_command,omitempty"`
	// SuggestedName 改名并存建议（默认名被占时按 -2/-3 递增找空位）。
	SuggestedName string `json:"suggested_name,omitempty"`
	// Resolution 人话处置说明。
	Resolution string `json:"resolution,omitempty"`
}

// DetectConfigNameConflict 检测配置名在给定宿主既有条目中的冲突。requestedName
// 为空时按默认命名补齐。同一宿主允许本机/中心并存：默认名以后缀区分天然错开；
// 撞名（含与另一范围撞名）一律返回 Conflict=true 而非覆盖建议。
// scope 须为 node|hub（调用方先行校验；非法时 DefaultConfigName 为空串、检测按
// 原样名字进行，不猜测范围归属）。
func DetectConfigNameConflict(scope, requestedName string, existing map[string]ExistingMcpEntry) ConfigNameConflict {
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = DefaultConfigName(scope)
	}
	res := ConfigNameConflict{RequestedName: name}
	old, exists := existing[name]
	if !exists {
		return res
	}
	res.Conflict = true
	res.ExistingScope = ExistingEntryScope(old)
	res.ExistingCommand = old.Command
	res.SuggestedName = suggestAlternativeName(name, existing)
	switch res.ExistingScope {
	case scope:
		res.Kind = ConflictKindSameScope
		res.Resolution = fmt.Sprintf("配置名 %q 已存在且同指向 %s 目标（多为本配置旧版本）。不静默覆盖：请显式确认替换该条目，或改用建议名 %q 并存。",
			name, scope, res.SuggestedName)
	case "unknown":
		res.Kind = ConflictKindUnknownScope
		res.Resolution = fmt.Sprintf("配置名 %q 已存在（env 无 SWARM_TARGET，目标范围无法判定）。不静默覆盖：请核对既有条目后显式确认替换，或改用建议名 %q 并存。",
			name, res.SuggestedName)
	default:
		res.Kind = ConflictKindCrossScope
		res.Resolution = fmt.Sprintf("配置名 %q 已存在且指向另一范围（%s）。同一宿主允许本机/中心并存，建议改用 %q；若确要替换须显式确认——替换将影响另一范围的既有接入。",
			name, res.ExistingScope, res.SuggestedName)
	}
	return res
}

// suggestAlternativeName 找第一个空闲的 <名>-N（N 从 2 递增）。
func suggestAlternativeName(base string, existing map[string]ExistingMcpEntry) string {
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, taken := existing[cand]; !taken {
			return cand
		}
	}
}
