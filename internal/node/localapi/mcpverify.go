// mcpverify.go MCP 接入六层验证模型（V0–V5，W03-03，docs/tasks/v3/W03-mcp-onboarding.md；
// 上游 docs/PRODUCT-PLAN-v3.md §12.4 分层验证与可复制提示词）。
//
// 本文件是纯模型层：不接路由（端点/页面消费属 W03-04）、不发起任何网络请求
// （V2 探测原语可注入，DefaultV2Probe 仅在 W03-04 装配时使用）、不落盘——全部
// 状态在进程内存（历史保留由调用方按需持久化，本层只存引用+版本+状态+UTC 时间，
// 绝不持久化明文凭据）。既有行为只消费不改动：mcpconfig.go（组装路径/范围契约/
// 回环判定/宿主支持级别/条目范围判定）、mcpcfgfile.go（安全读原语/文件版本
// hash/JSON 粗检）、mcpaccess.go（nowUTCString/maskCredential）。
//
// 六层语义定案表（§12.4 逐层，谁在何处检查/成功意味着什么/失败显示）：
//
//	层   谁在何处检查                        服务端可自证？  本层实现
//	V0   网页/服务端                         是（可自动点亮）RunV0Checks——服务端视角逐项事实
//	V1   外部 MCP 进程实际访问 Node/Hub      否              状态槽位 + RegisterExternalConfirmation
//	V2   目标服务鉴权                        部分：服务端视角探测可执行，外部 V2（调用机凭据被
//	                                         接受）不可代证  RunV2ProbeCheck——结果恒标 server_side_view
//	V3   外部宿主完成 MCP 初始化与 tools/list 否              状态槽位 + 外部确认登记
//	V4   外部宿主调用目录/状态工具（无副作用） 否              状态槽位 + 外部确认登记
//	V5   用户明确发起低影响任务/会话          否              状态槽位 + 外部确认登记（默认不运行）
//
// 硬规则（任务书原话，改动前先读本段）：
//   - 页面默认只保证 V0——V0 pass 绝不推断 V1–V5 任何一层（LayerStates 投影断言锁定）；
//   - V2 服务端探测结果恒标 CheckedBy=server_side_view，绝不渲染成外部验证成功
//     （输出面禁词见 mcpverify_test.go 金丝清单）；
//   - 首版无自动回执通道：外部证据唯一形态是 RegisterExternalConfirmation 的
//     user_confirmed（与服务器验证严格分流），confirmation 绑定配置版本 hash，
//     版本不符=旧结果不点亮新配置（AC10 语义），历史保留时间与来源可查；
//   - 凭据纪律（最高约束）：模型任何输出（检查结果/历史/秘密状态/指引）绝不含
//     明文凭据——秘密状态机只收脱敏引用（超长引用按疑似明文拒绝入库），明文仅
//     作为 V2 探测入参在探测瞬间使用、不进任何返回值（测试预埋假密钥 grep 锁定）；
//   - 时间一律 UTC（RFC3339，Z 后缀）。
package localapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// —— 层标识（§12.4 六层；顺序即展示顺序，绝不重排） ——

const (
	LayerV0 = "v0" // 配置就绪（服务端可证，可自动点亮）
	LayerV1 = "v1" // 调用机连接（外部 MCP 进程实际访问 Node/Hub）
	LayerV2 = "v2" // 授权有效（目标服务鉴权；服务端视角探测可执行但不代证外部）
	LayerV3 = "v3" // 宿主工具发现（外部宿主完成 MCP 初始化和 tools/list）
	LayerV4 = "v4" // 无副作用查询（外部宿主调用目录/状态工具）
	LayerV5 = "v5" // 可选执行验证（用户明确发起；会消耗模型额度，默认不运行）
)

// allLayers 六层全量（LayerStates 投影顺序；新增层先改语义定案表）。
var allLayers = [6]string{LayerV0, LayerV1, LayerV2, LayerV3, LayerV4, LayerV5}

// externalLayers 服务端不可自证层（外部确认登记唯一接受集）。
var externalLayers = [4]string{LayerV1, LayerV3, LayerV4, LayerV5}

// IsExternalLayer 是否服务端不可自证的外部层（v1|v3|v4|v5）。
func IsExternalLayer(layer string) bool {
	for _, l := range externalLayers {
		if l == layer {
			return true
		}
	}
	return false
}

// layerNeedsCredential 该层结果是否依赖凭据有效性（轮换共享凭据后须重新检查/
// 确认）：V2 鉴权本体；V3 tools/list 经桥接进程持凭据访问本端——401 即不可发现；
// V4/V5 工具调用同理。V1（TCP/TLS 可达性）与 V0（配置语法/字段齐备）不依赖
// 凭据有效性——轮换不断开已建立的连接、也不改变配置文件语法事实。
func layerNeedsCredential(layer string) bool {
	switch layer {
	case LayerV2, LayerV3, LayerV4, LayerV5:
		return true
	default:
		return false
	}
}

// LayerLabel 层的人话名（§12.4 表头口径，页面/测试共用）。
func LayerLabel(layer string) string {
	switch layer {
	case LayerV0:
		return "V0 配置就绪"
	case LayerV1:
		return "V1 调用机连接"
	case LayerV2:
		return "V2 授权有效"
	case LayerV3:
		return "V3 宿主工具发现"
	case LayerV4:
		return "V4 无副作用查询"
	case LayerV5:
		return "V5 可选执行验证"
	default:
		return "未知层 " + layer
	}
}

// —— 验证状态与检查视角/证据来源（分流纪律：用户确认与服务器验证严格分离） ——

const (
	VerifyStatusNotChecked    = "not_checked"    // 未验证（缺省态，如实——未运行显示「未验证」）
	VerifyStatusPass          = "pass"           // 服务端检查通过
	VerifyStatusFail          = "fail"           // 服务端检查失败
	VerifyStatusWarn          = "warn"           // 仅用于 V0 检查项：如实警示但不阻塞（未验证宿主等）
	VerifyStatusUserConfirmed = "user_confirmed" // 用户确认（外部证据点亮态，非服务端验证）
)

const (
	VerifySourceServerSideView = "server_side_view" // 服务端视角（V0/V2 服务端检查恒标此值；绝不冒认外部）
	VerifySourceUserConfirmed  = "user_confirmed"   // 用户确认（首版唯一外部证据来源——无自动回执通道）
)

// —— 历史条目结果（invalidated 仅出现在历史：结果因配置/凭据变化而失效的留痕） ——
const verifyResultInvalidated = "invalidated"

// ══════════════════════════ V0 配置就绪（服务端可证，可自动点亮） ══════════════════════════

// V0 检查项 key（逐项事实指向字段；W03-04 页面按 key 渲染定位）。
const (
	V0KeyScopeValid       = "scope_valid"       // 范围取值合法（node|hub）
	V0KeyScopeConsistency = "scope_consistency" // 宿主既有条目 SWARM_TARGET 与目标范围一致
	V0KeyServiceURL       = "service_url"       // 服务地址合法（http/https + host:port）
	V0KeyTopology         = "topology"          // 拓扑无陷阱（回环地址 × 外部调用机 = 危险拓扑）
	V0KeyCommandPath      = "command_path"      // 调用机适配进程路径已填（缺路径=失败）
	V0KeyCredential       = "credential"        // 凭据位在位（缺凭据=失败）
	V0KeyHostSupport      = "host_support"      // 宿主支持级别（未实测适配=warn 不阻塞）
	V0KeyHostFile         = "host_file"         // 宿主配置文件结构粗检 + 文件版本事实
)

// V0CheckInput V0 检查输入（全部由调用方显式给出——本层零探测进程、零网络；
// 宿主配置文件读取复用 mcpcfgfile.go mcpCfgRead 安全读原语，只读零写盘）。
type V0CheckInput struct {
	Scope     string // node|hub（必填；非法=scope_valid 失败，不猜测）
	CallerEnv string // same_machine|remote（必填；回环拓扑判定的前置）
	Host      string // zcode|codex|其他（未知宿主 manual_generic 降级，不阻塞）
	ServerURL string // 服务地址（ValidateServiceURL 校验）
	// McpCommandPath 调用机上的 swarmmind-mcp 绝对路径；空/占位=缺路径失败。
	McpCommandPath string
	// CredentialRef 脱敏引用或占位标记（本输入绝不含明文）；空/占位/未生成=缺凭据失败。
	CredentialRef string
	// ConfigName 配置名；空 = 按 scope 默认命名（DefaultConfigName）。
	ConfigName string
	// ConfigHome 受控宿主配置根（可选）：已知宿主时安全读文件做结构粗检并取
	// 文件版本 hash；空=文件项 not_checked（hub 范围页面不传——宿主配置在调用机上）。
	ConfigHome string
	// ExistingEntries 宿主既有条目（可选）：文件可读并解析时以文件实取为准；
	// 文件不可得时（如 hub 范围）可用预览端点先前的提取结果回填，供范围一致性检查。
	ExistingEntries map[string]ExistingMcpEntry
}

// V0CheckItem 单项事实。Status ∈ pass|fail|warn|not_checked（warn 只警示不阻塞——
// 未实测宿主等如实标注）。
type V0CheckItem struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Detail string `json:"detail"` // 人话事实，失败指向字段；绝不含文件内容与明文凭据
}

// V0Check V0 配置就绪判定结果（任务书形态：Status pass|fail|not_checked +
// Checks 逐项事实 + CheckedAt）。ConfigVersion 为宿主配置文件版本 hash（可读时），
// 即「配置版本可见取值源」——外部确认登记绑定的就是它。
type V0Check struct {
	Status        string        `json:"status"` // pass | fail | not_checked
	Checks        []V0CheckItem `json:"checks"`
	CheckedAt     string        `json:"checked_at"` // UTC RFC3339
	ConfigVersion string        `json:"config_version,omitempty"`
	Notes         []string      `json:"notes,omitempty"`
}

// RunV0Checks 服务端视角 V0 检查（复用 mcpconfig 组装路径逐项判定：语法=模板
// 组装+文件解析、必填项、范围一致、服务地址、回环拓扑、宿主支持级别、文件版本
// 事实）。只读零写盘零网络；不返回 error——一切输入问题都以失败检查项呈现
// （V0 的职责就是把缺陷指到字段，不是把调用方炸掉）。
//
// 硬规则：本函数结果只证明配置侧就绪。Status=pass 时 Notes 必带「不推断 V1–V5」
// 边界句；页面默认只保证 V0。
func RunV0Checks(in V0CheckInput) V0Check {
	check := V0Check{CheckedAt: nowUTCString()}
	if in.Scope == "" && in.CallerEnv == "" && in.Host == "" && in.ServerURL == "" &&
		in.McpCommandPath == "" && in.CredentialRef == "" && in.ConfigHome == "" &&
		len(in.ExistingEntries) == 0 {
		check.Status = VerifyStatusNotChecked
		check.Notes = append(check.Notes, "未提供任何配置输入：V0 检查未运行（页面按「未验证」呈现）。")
		return check
	}

	// 先取宿主配置文件事实（供 host_file 项与范围一致性/既有条目）。
	name := strings.TrimSpace(in.ConfigName)
	if name == "" {
		name = DefaultConfigName(in.Scope)
	}
	fileItem, fileEntries, fileVersion := v0ProbeHostFile(in, name)
	entries := fileEntries
	if entries == nil {
		entries = in.ExistingEntries // 文件不可得时回退调用方提供的提取结果
	}
	scopeOK := in.Scope == ScopeNode || in.Scope == ScopeHub
	trimmedPath := strings.TrimSpace(in.McpCommandPath)

	// ① 范围取值。
	if scopeOK {
		check.appendItem(V0KeyScopeValid, VerifyStatusPass, "目标范围合法（"+scopeLabel(in.Scope)+"）。")
	} else {
		check.appendItem(V0KeyScopeValid, VerifyStatusFail,
			fmt.Sprintf("目标范围 %q 非法（合法值 node|hub），不猜测——指向字段 scope。", in.Scope))
	}

	// ② 范围一致（宿主既有条目 SWARM_TARGET vs 目标范围；数据不可得如实未检）。
	switch {
	case !scopeOK:
		check.appendItem(V0KeyScopeConsistency, VerifyStatusNotChecked, "范围取值非法：一致性未检。")
	case entries == nil:
		check.appendItem(V0KeyScopeConsistency, VerifyStatusNotChecked,
			"未获得宿主既有条目（文件未读且调用方未提供）：范围一致性未检（不猜测）。")
	default:
		old, ok := entries[name]
		if !ok {
			check.appendItem(V0KeyScopeConsistency, VerifyStatusNotChecked,
				fmt.Sprintf("宿主既有条目中无 %q（配置未应用）：范围一致性未检。", name))
		} else if s := ExistingEntryScope(old); s == in.Scope {
			check.appendItem(V0KeyScopeConsistency, VerifyStatusPass,
				fmt.Sprintf("宿主既有条目 %q 的 SWARM_TARGET=%s 与目标范围一致。", name, s))
		} else if s == "unknown" {
			check.appendItem(V0KeyScopeConsistency, VerifyStatusWarn,
				fmt.Sprintf("宿主既有条目 %q 的 env 无 SWARM_TARGET：目标范围无法判定（可能为旧版模板写入），请人工核对。", name))
		} else {
			check.appendItem(V0KeyScopeConsistency, VerifyStatusFail,
				fmt.Sprintf("范围不一致：宿主既有条目 %q 的 SWARM_TARGET=%s 与目标范围 %s 不符（指向另一范围）——核对配置名，或改用带后缀的配置名并存（同一宿主允许本机/中心并存）。", name, s, in.Scope))
		}
	}

	// ③ 服务地址格式（复用 ValidateServiceURL）。
	if err := ValidateServiceURL(in.ServerURL); err != nil {
		check.appendItem(V0KeyServiceURL, VerifyStatusFail,
			"服务地址非法："+err.Error()+"（指向字段 service_url）。")
	} else {
		check.appendItem(V0KeyServiceURL, VerifyStatusPass, "服务地址格式合法（http/https + host:port）。")
	}

	// ④ 拓扑（复用 isLoopbackServiceURL；AC07：错误回环地址不得显示为外部已
	// 验证——远端调用机 + 回环地址 = 危险拓扑，V0 即失败并指向字段）。
	loopback := isLoopbackServiceURL(in.ServerURL)
	switch {
	case in.CallerEnv != CallerSameMachine && in.CallerEnv != CallerRemote:
		check.appendItem(V0KeyTopology, VerifyStatusFail,
			fmt.Sprintf("调用环境 %q 非法（合法值 same_machine|remote），不猜测——指向字段 caller_env。", in.CallerEnv))
	case loopback && in.CallerEnv == CallerRemote:
		check.appendItem(V0KeyTopology, VerifyStatusFail,
			"危险拓扑：服务地址为回环地址（127.0.0.1/localhost/::1/0.0.0.0），而 MCP 进程在外部调用机上——回环地址指向调用机自身，从调用机连不到本端。请改用调用机可达的转发地址（指向字段 service_url）。")
	case loopback:
		check.appendItem(V0KeyTopology, VerifyStatusPass,
			"服务地址为回环地址：与目标同机时属正常拓扑（MCP 进程与本端同机直连）；若调用环境改为外部调用机，本项将判定为危险拓扑失败。")
	default:
		check.appendItem(V0KeyTopology, VerifyStatusPass, "服务地址非回环，未见拓扑陷阱。")
	}

	// ⑤ 调用机路径（缺路径=失败，指向字段；占位配置无法启动——apply 同口径）。
	switch {
	case trimmedPath == "" || trimmedPath == mcpCallerPathPlaceholder:
		check.appendItem(V0KeyCommandPath, VerifyStatusFail,
			"缺路径：mcp_command_path 为空或仍为占位符——请填调用机上的 swarmmind-mcp 绝对路径（指向字段 mcp_command_path；本端绝不代填服务器路径）。")
	case !filepath.IsAbs(trimmedPath):
		check.appendItem(V0KeyCommandPath, VerifyStatusFail,
			fmt.Sprintf("mcp_command_path 须为绝对路径（收到 %q）——与 apply 装配门同一口径。", trimmedPath))
	case in.CallerEnv == CallerRemote:
		check.appendItem(V0KeyCommandPath, VerifyStatusPass,
			"路径已填（外部调用机路径本端无法核实存在性，属调用机侧事实；能否启动见 V1/V3）。")
	default:
		check.appendItem(V0KeyCommandPath, VerifyStatusPass, "路径已填。")
	}

	// ⑥ 凭据位（占位/未生成=失败，指向字段；明文绝不入模型——输入即脱敏引用）。
	cred := strings.TrimSpace(in.CredentialRef)
	if cred == "" || cred == mcpCredRefPlaceholder || cred == templateCredentialPlaceholder || cred == maskCredential("") {
		check.appendItem(V0KeyCredential, VerifyStatusFail,
			"缺凭据：凭据位仍为占位/未生成——先经人工授权/轮换取得一次性凭据（本机：主人控制台共享编排凭据；中心：管理员签发 PAT），再补齐（指向字段 credential_ref）。")
	} else {
		check.appendItem(V0KeyCredential, VerifyStatusPass, "凭据引用在位（脱敏引用；明文不入模型）。")
	}

	// ⑦ 宿主支持级别（mcpconfig 组装路径复用；未实测适配=warn 不阻塞手动接入，
	// 如实标注不冒认已适配）。
	if !scopeOK {
		check.appendItem(V0KeyHostSupport, VerifyStatusNotChecked, "范围取值非法：宿主模板未组装。")
	} else {
		tmpl, terr := BuildHostTemplate(HostTemplateInput{
			Host: in.Host, Scope: in.Scope, ServerURL: in.ServerURL,
			McpPath: trimmedPath, ConfigName: name,
		})
		if terr != nil {
			check.appendItem(V0KeyHostSupport, VerifyStatusNotChecked, "宿主模板组装失败："+terr.Error())
		} else {
			switch tmpl.SupportLevel {
			case SupportLevelVerifiedZcode:
				check.appendItem(V0KeyHostSupport, VerifyStatusPass,
					"宿主 zcode 为已实测适配（verified_zcode；事实源：primewire 自动接线持续写入该布局并有实测覆盖）。")
			case SupportLevelManualGenericCodex:
				check.appendItem(V0KeyHostSupport, VerifyStatusWarn,
					"宿主 codex 为通用手动模板（manual_generic_codex，未实测适配）：不阻塞手动接入，宿主格式兼容性未验证——请按 codex 官方文档核对键名与合并点。")
			default:
				check.appendItem(V0KeyHostSupport, VerifyStatusWarn,
					fmt.Sprintf("宿主 %q 为未知宿主（manual_generic，未验证）：仅通用 JSON 手动模板，不冒认已适配——请按宿主文档核对。", tmpl.Host))
			}
		}
	}

	// ⑧ 宿主配置文件结构粗检 + 文件版本事实。
	check.appendItem(fileItem.Key, fileItem.Status, fileItem.Detail)

	// 汇总：任一 fail → fail；全空输入已提前返回 not_checked。
	check.Status = VerifyStatusPass
	for _, it := range check.Checks {
		if it.Status == VerifyStatusFail {
			check.Status = VerifyStatusFail
			break
		}
	}
	check.ConfigVersion = fileVersion
	if check.Status == VerifyStatusPass {
		// 边界句硬规则：V0 pass 绝不推断上层（页面默认只保证 V0）。
		check.Notes = append(check.Notes,
			"V0 为服务端可证层：本结果只证明配置侧就绪，绝不推断 V1–V5 任何一层；外部层的点亮须由外部验证或用户确认登记。")
	}
	if loopback {
		check.Notes = append(check.Notes,
			"回环地址提示：同机直连属正常拓扑；外部调用机场景下回环地址即危险拓扑（V0 直接失败），务必改用调用机可达地址。")
	}
	return check
}

// appendItem 定序追加检查项（固定顺序供 fixture 锁定）。
func (c *V0Check) appendItem(key, status, detail string) {
	c.Checks = append(c.Checks, V0CheckItem{Key: key, Status: status, Detail: detail})
}

// v0ProbeHostFile 宿主配置文件结构粗检（只读零写盘；复用 mcpcfgfile 安全读原语
// 与 JSON 粗检）。返回 检查项、既有条目提取（可 nil）、文件版本 hash（可读时；
// codex/未解析形态同样返回 hash——版本事实不依赖语法解析）。
func v0ProbeHostFile(in V0CheckInput, name string) (V0CheckItem, map[string]ExistingMcpEntry, string) {
	item := V0CheckItem{Key: V0KeyHostFile}
	layout, known := mcpCfgLayoutFor(in.Host)
	switch {
	case !known:
		item.Status = VerifyStatusNotChecked
		item.Detail = fmt.Sprintf("未知宿主（%q，manual_generic）无已适配布局：不猜布局、不做文件粗检；结构是否合法以宿主自身解析为准。",
			strings.ToLower(strings.TrimSpace(in.Host)))
		return item, nil, ""
	case strings.TrimSpace(in.ConfigHome) == "":
		item.Status = VerifyStatusNotChecked
		item.Detail = "未提供 config_home：跳过宿主配置文件粗检（本项事实缺省，不阻塞其他项；hub 范围页面不传——宿主配置在调用机上）。"
		return item, nil, ""
	}
	raw, st, rerr := mcpCfgRead(in.ConfigHome, layout)
	if rerr != nil {
		item.Status = VerifyStatusFail
		item.Detail = rerr.msg // 分类文案，绝不含文件内容（mcpcfgfile 纪律）
		return item, nil, ""
	}
	if !st.Exists {
		item.Status = VerifyStatusFail
		item.Detail = fmt.Sprintf("宿主配置文件不存在（%s）：配置尚未应用或宿主未生成过配置——先经预览/应用写入本条目，或手动粘贴模板。", st.Path)
		return item, nil, ""
	}
	if layout.format == "toml" {
		// codex：TOML 本端不解析（依赖白名单纪律 + agentdetect 粗检裁决同口径）——
		// 语法不冒认；文件存在性与版本 hash 照实给出。
		item.Status = VerifyStatusNotChecked
		item.Detail = fmt.Sprintf("codex TOML 本端不解析（依赖白名单纪律）：仅文件事实——存在、版本 hash=%s；语法合法性以 codex CLI 解析为准。", shortVersionHash(st.Version))
		return item, nil, st.Version
	}
	doc, perr := mcpCfgParseJSONDoc(raw, st.Path)
	if perr != nil {
		item.Status = VerifyStatusFail
		item.Detail = perr.msg // 只回分类与偏移量，不回内容
		return item, nil, st.Version
	}
	servers, serr := mcpCfgServersMap(doc, st.Path)
	if serr != nil {
		item.Status = VerifyStatusFail
		item.Detail = serr.msg
		return item, nil, st.Version
	}
	entries := mcpCfgExistingEntries(servers)
	if _, ok := entries[name]; !ok {
		item.Status = VerifyStatusFail
		item.Detail = fmt.Sprintf("目标条目 %q 尚未写入宿主配置 mcpServers（配置未应用；其余结构粗检通过）。", name)
		return item, entries, st.Version
	}
	item.Status = VerifyStatusPass
	item.Detail = fmt.Sprintf("宿主配置结构粗检通过：JSON 合法、mcpServers 为对象、目标条目 %q 在位；文件版本 hash=%s。", name, shortVersionHash(st.Version))
	return item, entries, st.Version
}

// shortVersionHash 版本 hash 截短（人话展示用；完整 hash 在 ConfigVersion 字段）。
func shortVersionHash(v string) string {
	if len(v) > 12 {
		return v[:12] + "…"
	}
	return v
}

// ══════════════════════════ V2 授权有效（服务端视角，绝不代证外部） ══════════════════════════

// V2 指引分类（FailKind；GuidanceFor 入参）。
const (
	V2FailKindUnauthorized = "401"        // 凭据过期/撤销
	V2FailKindForbidden    = "403"        // 未授权（编排开关未启用/权限不足）
	V2FailKindNetwork      = "network"    // 连接未建立（V1 层问题）
	V2FailKindUnexpected   = "unexpected" // 意外状态码，无法判定
)

// v2ProbePath 默认探测路径（node 与 hub 同路径：GET /api/v1/capabilities，
// authRead/auth 均接受编排凭据；零模型零派单无副作用）。
const v2ProbePath = "/api/v1/capabilities"

// V2ProbeFunc 服务端视角鉴权探测原语（可注入——测试注入假检查器实现零网络；
// 生产装配 DefaultV2Probe）。返回观测到的 HTTP 状态码；err=连接层失败（V1 语义）。
type V2ProbeFunc func(ctx context.Context, serviceURL, credential string) (int, error)

// DefaultV2Probe 生产探测原语：对目标端点发起一次 GET {serviceURL}/api/v1/capabilities
// （无副作用——能力元信息只读，零模型零派单）。仅供 W03-04 装配；本批测试零网络，
// 一律注入假检查器。
func DefaultV2Probe(timeout time.Duration) V2ProbeFunc {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, serviceURL, credential string) (int, error) {
		endpoint := strings.TrimRight(serviceURL, "/") + v2ProbePath
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // 排干小响应便于连接复用；不读内容
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
}

// V2CheckInput V2 服务端视角检查输入。Credential 为明文入参：仅在探测瞬间经
// V2ProbeFunc 使用，绝不进任何输出结构/历史/日志（测试假密钥金丝锁定）。
type V2CheckInput struct {
	ServiceURL    string // 目标服务地址（node|hub 口径由调用方保证）
	Credential    string // 明文凭据（仅内存探测瞬间使用）
	ConfigVersion string // 绑定配置版本 hash（结果可见性/失效判定锚）
}

// V2Check V2 服务端视角检查结果。CheckedBy 恒为 server_side_view（探测成功也绝不
// 标外部 V2 有效——外部 V2=调用机凭据被目标服务接受，服务端无法代证；模型层字段
// 语义如实分离，页面据此渲染「服务端视角」）。Status ∈ pass|fail|not_checked：
// 200/401/403 三态判定；连接层失败与意外状态码 → not_checked（不猜测授权结论）。
type V2Check struct {
	Status        string `json:"status"`
	CheckedBy     string `json:"checked_by"` // 恒 server_side_view
	CheckedAt     string `json:"checked_at"` // UTC RFC3339
	HTTPStatus    int    `json:"http_status,omitempty"`
	FailKind      string `json:"fail_kind,omitempty"` // V2FailKind*（指引分类入参）
	Detail        string `json:"detail,omitempty"`
	ConfigVersion string `json:"config_version,omitempty"`
}

// RunV2ProbeCheck 执行 V2 服务端视角鉴权探测。401→fail（过期/撤销，指引转人工
// 授权入口）；403→fail（未授权，转人工授权入口）；200→pass（仅服务端自证）；
// 连接层失败/意外状态码→not_checked（如实：不能对授权有效性下结论）。
// 明文凭据只在本调用内被 probe 使用，返回值不含任何凭据信息。
func RunV2ProbeCheck(probe V2ProbeFunc, in V2CheckInput) V2Check {
	check := V2Check{
		CheckedBy:     VerifySourceServerSideView, // 恒在位：任何结局都不改变检查视角
		CheckedAt:     nowUTCString(),
		ConfigVersion: in.ConfigVersion,
	}
	if probe == nil {
		check.Status = VerifyStatusNotChecked
		check.Detail = "探测原语未装配：V2 服务端视角检查未运行（不猜测授权状态）。"
		return check
	}
	if err := ValidateServiceURL(in.ServiceURL); err != nil {
		check.Status = VerifyStatusNotChecked
		check.Detail = "服务地址非法（先修 V0 配置层）：" + err.Error()
		return check
	}
	status, perr := probe(context.Background(), in.ServiceURL, in.Credential)
	if perr != nil {
		kind := ConnErrorKind(perr)
		check.Status = VerifyStatusNotChecked
		check.FailKind = V2FailKindNetwork
		check.Detail = "连接未建立（" + connKindText(kind) + "）：属 V1 调用机连接层问题——连接未打通时不能对授权有效性下任何结论。"
		return check
	}
	check.HTTPStatus = status
	switch status {
	case http.StatusOK:
		check.Status = VerifyStatusPass
		check.Detail = "服务端视角：目标端点接受本次探测凭据（GET capabilities——零模型零派单无副作用）。此结论仅服务端自证，不构成调用机视角的验证结论。"
	case http.StatusUnauthorized:
		check.Status = VerifyStatusFail
		check.FailKind = V2FailKindUnauthorized
		check.Detail = "凭据未通过目标端点认证（401）：可能已过期或被撤销——转人工授权/轮换入口，更新宿主配置后重试。"
	case http.StatusForbidden:
		check.Status = VerifyStatusFail
		check.FailKind = V2FailKindForbidden
		check.Detail = "凭据被识别但未获授权（403）：编排授权未启用或权限不足——转人工授权入口（本机：主人控制台启用 MCP 编排授权；中心：管理员核对 PAT 权限）。"
	default:
		check.Status = VerifyStatusNotChecked
		check.FailKind = V2FailKindUnexpected
		check.Detail = fmt.Sprintf("目标端点返回意外状态 %d：授权状态无法判定（不猜测），请核对目标服务健康后重试。", status)
	}
	return check
}

// ConnErrorKind 连接错误分类（V1 语义；V2 探测连接层失败与 V1 指引共用）。
// 返回值 ∈ dns | tls | refused | timeout | other。
func ConnErrorKind(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ConnKindDNS
	}
	var tlsVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &tlsVerifyErr) {
		return ConnKindTLS
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return ConnKindTLS
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return ConnKindTLS
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return ConnKindTLS
	}
	msg := err.Error()
	if strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") {
		return ConnKindTLS
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(msg, "connection refused") {
		return ConnKindRefused
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ConnKindTimeout
	}
	if strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "timeout") || strings.Contains(msg, "Timeout") {
		return ConnKindTimeout
	}
	return ConnKindOther
}

// V1 指引分类常量（ConnErrorKind 返回值；GuidanceFor 入参）。
const (
	ConnKindDNS     = "dns"
	ConnKindTLS     = "tls"
	ConnKindRefused = "refused"
	ConnKindTimeout = "timeout"
	ConnKindOther   = "other"
)

// connKind 分类的人话短词。
func connKindText(kind string) string {
	switch kind {
	case ConnKindDNS:
		return "DNS 解析失败"
	case ConnKindTLS:
		return "证书校验失败"
	case ConnKindRefused:
		return "连接被拒绝"
	case ConnKindTimeout:
		return "连接超时"
	default:
		return "连接失败"
	}
}

// ══════════════════════════ 一次性秘密生命周期（模型层） ══════════════════════════

// 秘密状态机（凭证引用/占位状态；轮换/撤销使旧配置相关验证失效）。
const (
	SecretStatePlaceholder    = "placeholder"     // 占位：凭据未就位（模板缺省）
	SecretStateUserProvided   = "user_provided"   // 已提供：脱敏引用在位并绑定配置版本
	SecretStateRotatedInvalid = "rotated_invalid" // 已轮换/撤销：旧配置中的引用即失效
)

// maxMaskedRefLen 脱敏引用长度上限（maskCredential 形态 ≤ 12 字符；超长按疑似
// 明文拒绝入库——模型层结构性防线，明文凭据绝不持久化）。
const maxMaskedRefLen = 16

// SecretRef 秘密引用快照（只存脱敏引用+版本+状态+UTC 时间；绝不明文）。
type SecretRef struct {
	Ref       string `json:"ref"`        // 脱敏引用（maskCredential 形态）或空（占位态）
	EnvVar    string `json:"env_var"`    // SWARM_NODE_PRIME_TOKEN | SWARM_HUB_TOKEN
	State     string `json:"state"`      // SecretState*
	Version   string `json:"version"`    // 状态绑定的配置版本 hash（空=尚未绑定）
	UpdatedAt string `json:"updated_at"` // UTC RFC3339
}

// SecretLifecycle 一次性秘密生命周期（内存态；持久化由调用方按需落 JSON——
// 本结构可序列化且永不含明文）。
type SecretLifecycle struct {
	mu  sync.Mutex
	ref SecretRef
}

// NewSecretLifecycle 构造（初始 placeholder；envVar 为该凭据在宿主配置中的
// 变量名，沿用 mcpconfig 既有契约，绝不重命名）。
func NewSecretLifecycle(envVar string) *SecretLifecycle {
	return &SecretLifecycle{ref: SecretRef{
		EnvVar: envVar, State: SecretStatePlaceholder, UpdatedAt: nowUTCString(),
	}}
}

// MarkUserProvided 占位→已提供（maskedRef 必须是脱敏引用形态；绑定配置版本）。
// 超长引用按疑似明文拒绝；占位/未生成标记不能冒充已提供。
func (l *SecretLifecycle) MarkUserProvided(maskedRef, configVersion string) error {
	maskedRef = strings.TrimSpace(maskedRef)
	switch {
	case maskedRef == "":
		return errors.New("凭据引用必填（脱敏引用形态，如 ab********；明文绝不入模型）")
	case len(maskedRef) > maxMaskedRefLen:
		return fmt.Errorf("凭据引用疑似明文（长度 %d 超脱敏引用上限 %d 字符）：拒绝入库（只存脱敏引用+版本+状态）", len(maskedRef), maxMaskedRefLen)
	case maskedRef == mcpCredRefPlaceholder || maskedRef == templateCredentialPlaceholder || maskedRef == maskCredential(""):
		return errors.New("占位/未生成标记不能标记为 user_provided：请先经人工授权/轮换取得一次性凭据")
	case strings.TrimSpace(configVersion) == "":
		return errors.New("config_version 必填：凭据状态绑定配置版本（换发后旧配置相关验证据此失效）")
	}
	l.mu.Lock()
	l.ref = SecretRef{
		Ref: maskedRef, EnvVar: l.ref.EnvVar,
		State: SecretStateUserProvided, Version: strings.TrimSpace(configVersion),
		UpdatedAt: nowUTCString(),
	}
	l.mu.Unlock()
	return nil
}

// MarkRotated 已提供→已轮换/撤销（version=轮换发生时配置版本：该版本配置中
// 仍载旧凭据，其相关验证即失效——判定见 SecretInvalidates）。
func (l *SecretLifecycle) MarkRotated(configVersion string) {
	l.mu.Lock()
	l.ref = SecretRef{
		Ref: l.ref.Ref, EnvVar: l.ref.EnvVar,
		State: SecretStateRotatedInvalid, Version: strings.TrimSpace(configVersion),
		UpdatedAt: nowUTCString(),
	}
	l.mu.Unlock()
}

// Snapshot 状态快照（绝不明文）。
func (l *SecretLifecycle) Snapshot() SecretRef {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ref
}

// SecretInvalidates 判定（纯函数）：秘密状态机是否使绑定 configVersion 的
// 凭据相关验证失效——状态为 rotated_invalid 且轮换正打中该配置版本（该配置
// 仍载旧凭据）。换发后重新 apply 新配置并 MarkUserProvided 绑定新版本，即不
// 再命中旧版本。授权「禁用」（mcp_disabled）不经此函数：禁用由 V2 服务端视角
// 探测以 403 如实呈现并指引授权入口。
func SecretInvalidates(ref SecretRef, configVersion string) bool {
	return ref.State == SecretStateRotatedInvalid &&
		ref.Version != "" && ref.Version == configVersion
}

// ══════════════════════════ 六层验证板（状态槽位 + 外部确认登记 + 历史） ══════════════════════════

// VerifyLayerState 单层状态槽位投影。检查视角（CheckedBy）与配置版本
// （ConfigVersion）恒可见——页面据此分流「服务端验证」与「用户确认」。
type VerifyLayerState struct {
	Layer         string `json:"layer"`
	Status        string `json:"status"`     // VerifyStatus*
	CheckedBy     string `json:"checked_by"` // server_side_view | user_confirmed | ""（未检查）
	CheckedAt     string `json:"checked_at"` // UTC RFC3339
	ConfigVersion string `json:"config_version"`
	Detail        string `json:"detail,omitempty"`
	// Invalidated/InvalidReason 结果被显式标记失效（轮换等）；投影时按 not_checked
	// 呈现、历史保留。
	Invalidated   bool   `json:"invalidated,omitempty"`
	InvalidReason string `json:"invalid_reason,omitempty"`
}

// VerifyHistoryEntry 状态历史条目（时间与来源可查；§12.4：历史结果保留时间与
// 来源可查——Result=invalidated 的条目记录失效事件本身）。
type VerifyHistoryEntry struct {
	Layer         string `json:"layer"`
	Result        string `json:"result"` // pass|fail|not_checked|user_confirmed|invalidated
	Source        string `json:"source"` // server_side_view | user_confirmed
	ConfigVersion string `json:"config_version"`
	At            string `json:"at"` // UTC RFC3339
	Note          string `json:"note,omitempty"`
}

// ExternalConfirmation 外部证据登记（首版无自动回执通道——唯一外部证据形态是
// 用户确认）。ConfigVersion 必填：确认绑定配置版本 hash，版本不符=旧结果不点亮
// 新配置（AC10）。Note 为用户备注（如宿主回显文本）；不得粘贴明文凭据（调用方
// 纪律，模型层不做内容审查但绝不主动回显到指引）。
type ExternalConfirmation struct {
	Layer         string `json:"layer"`          // 仅接受 v1|v3|v4|v5（服务端不可自证层）
	Source        string `json:"source"`         // 空=补 user_confirmed；其他值拒绝
	ConfigVersion string `json:"config_version"` // 必填（绑定锚）
	ConfirmedAt   string `json:"confirmed_at"`   // 空=登记时刻；RFC3339，归一 UTC
	Note          string `json:"note,omitempty"`
}

// McpVerifyModel 六层验证板（进程内存态；mu 串行全部读写）。
type McpVerifyModel struct {
	mu            sync.Mutex
	configVersion string
	layers        map[string]VerifyLayerState
	history       []VerifyHistoryEntry
	secret        *SecretLifecycle
}

// NewMcpVerifyModel 构造（configVersion 为当前配置版本锚，可空=尚未锚定——任何
// 结果都不点亮直至 SetConfigVersion；credentialEnvVar 为共享凭据 env 变量名）。
func NewMcpVerifyModel(configVersion, credentialEnvVar string) *McpVerifyModel {
	return &McpVerifyModel{
		configVersion: configVersion,
		layers:        map[string]VerifyLayerState{},
		secret:        NewSecretLifecycle(credentialEnvVar),
	}
}

// ConfigVersion 当前配置版本锚。
func (m *McpVerifyModel) ConfigVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configVersion
}

// SetConfigVersion 变更配置版本锚（换地址/换凭据/换宿主格式/换范围 → 页面在
// 配置应用后调用）：绑定旧版本的结果一律不再点亮（AC10——旧回执/旧配置失效），
// 历史保留可查。
func (m *McpVerifyModel) SetConfigVersion(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("配置版本锚不得为空（外部确认与检查结果须绑定版本 hash 才可判定新旧）")
	}
	m.mu.Lock()
	m.configVersion = strings.TrimSpace(v)
	m.mu.Unlock()
	return nil
}

// RecordV0 登记服务端 V0 检查结果（绑定当前版本；CheckedBy=server_side_view）。
func (m *McpVerifyModel) RecordV0(check V0Check) {
	at := check.CheckedAt
	if at == "" {
		at = nowUTCString()
	}
	detail := v0Summary(check)
	m.mu.Lock()
	m.layers[LayerV0] = VerifyLayerState{
		Layer: LayerV0, Status: check.Status,
		CheckedBy: VerifySourceServerSideView, CheckedAt: at,
		ConfigVersion: m.configVersion, Detail: detail,
	}
	m.history = append(m.history, VerifyHistoryEntry{
		Layer: LayerV0, Result: check.Status, Source: VerifySourceServerSideView,
		ConfigVersion: m.configVersion, At: at, Note: detail,
	})
	m.mu.Unlock()
}

// RecordV2 登记服务端视角 V2 探测结果（绑定当前版本；CheckedBy 取自 check——
// RunV2ProbeCheck 恒填 server_side_view）。
func (m *McpVerifyModel) RecordV2(check V2Check) {
	at := check.CheckedAt
	if at == "" {
		at = nowUTCString()
	}
	note := check.Detail
	if check.HTTPStatus != 0 {
		note = fmt.Sprintf("HTTP %d：%s", check.HTTPStatus, check.Detail)
	}
	m.mu.Lock()
	m.layers[LayerV2] = VerifyLayerState{
		Layer: LayerV2, Status: check.Status,
		CheckedBy: check.CheckedBy, CheckedAt: at,
		ConfigVersion: m.configVersion, Detail: note,
	}
	m.history = append(m.history, VerifyHistoryEntry{
		Layer: LayerV2, Result: check.Status, Source: check.CheckedBy,
		ConfigVersion: m.configVersion, At: at, Note: note,
	})
	m.mu.Unlock()
}

// RegisterExternalConfirmation 登记外部证据（用户确认）。校验：层须为 v1|v3|v4|v5
// （v0/v2 为服务端可证层，不收用户确认——分流硬规则）；source 恒 user_confirmed
// （首版无自动回执通道，其他来源拒绝——不冒认自动检测成功）；config_version 必填。
// 登记成功返回该层投影：确认版本与当前锚一致 → user_confirmed 点亮；不一致 →
// not_checked（旧回执不点亮新配置），历史保留可查。
func (m *McpVerifyModel) RegisterExternalConfirmation(c ExternalConfirmation) (VerifyLayerState, error) {
	if !IsExternalLayer(c.Layer) {
		return VerifyLayerState{}, fmt.Errorf("层 %q 不接受外部确认登记：外部确认仅用于服务端不可自证层 v1|v3|v4|v5（v0/v2 为服务端可证层，请运行服务端检查）", c.Layer)
	}
	src := strings.TrimSpace(c.Source)
	if src == "" {
		src = VerifySourceUserConfirmed
	}
	if src != VerifySourceUserConfirmed {
		return VerifyLayerState{}, fmt.Errorf("confirmation.source %q 非法：首版无自动回执通道，外部证据唯一来源为 %q（用户确认与服务端验证严格分流，不以未经实现的自动回执通道冒认成功）", c.Source, VerifySourceUserConfirmed)
	}
	if strings.TrimSpace(c.ConfigVersion) == "" {
		return VerifyLayerState{}, errors.New("confirmation.config_version 必填：用户确认绑定配置版本 hash（旧回执不得点亮新配置，AC10）")
	}
	at, err := normalizeUTCTime(c.ConfirmedAt)
	if err != nil {
		return VerifyLayerState{}, err
	}
	m.mu.Lock()
	m.layers[c.Layer] = VerifyLayerState{
		Layer: c.Layer, Status: VerifyStatusUserConfirmed,
		CheckedBy: VerifySourceUserConfirmed, CheckedAt: at,
		ConfigVersion: strings.TrimSpace(c.ConfigVersion), Detail: c.Note,
	}
	m.history = append(m.history, VerifyHistoryEntry{
		Layer: c.Layer, Result: VerifyStatusUserConfirmed, Source: VerifySourceUserConfirmed,
		ConfigVersion: strings.TrimSpace(c.ConfigVersion), At: at, Note: c.Note,
	})
	m.mu.Unlock()
	return m.LayerState(c.Layer), nil
}

// RotateSecret 轮换共享凭据（页面轮换操作后调用）：秘密状态机迁移到
// rotated_invalid（绑定当前版本——该版本配置仍载旧凭据），凭据相关层（v2/v3/v4/v5）
// 的既有结果即失效（历史保留 invalidated 留痕）；v0/v1 不依赖凭据有效性，不动。
func (m *McpVerifyModel) RotateSecret() SecretRef {
	m.mu.Lock()
	ver := m.configVersion
	m.secret.MarkRotated(ver)
	ref := m.secret.Snapshot()
	for _, layer := range []string{LayerV2, LayerV3, LayerV4, LayerV5} {
		if st, ok := m.layers[layer]; ok && !st.Invalidated && st.Status != VerifyStatusNotChecked {
			st.Invalidated = true
			st.InvalidReason = "共享凭据已轮换（旧配置中的凭据即失效，须重新配置后重新检查/确认）"
			m.layers[layer] = st
			m.history = append(m.history, VerifyHistoryEntry{
				Layer: layer, Result: verifyResultInvalidated, Source: st.CheckedBy,
				ConfigVersion: ver, At: nowUTCString(), Note: st.InvalidReason,
			})
		}
	}
	m.mu.Unlock()
	return ref
}

// MarkSecretProvided 登记新凭据就位（换发后重新 apply 配置并绑定新版本时调用）。
func (m *McpVerifyModel) MarkSecretProvided(maskedRef, configVersion string) error {
	return m.secret.MarkUserProvided(maskedRef, configVersion)
}

// Secret 秘密状态快照（绝不明文）。
func (m *McpVerifyModel) Secret() SecretRef {
	return m.secret.Snapshot()
}

// VerifyStale 判定（纯函数）：一条已登记的层结果在当前板面状态下是否失效
// （不点亮）。失效路径：①配置版本不符（旧回执不点亮新配置，AC10）；②登记后被
// 显式标记失效（轮换留痕）；③凭据相关层遇共享凭据轮换且轮换打中当前版本。
// not_checked 本身即视为不点亮。
func VerifyStale(stored VerifyLayerState, currentConfigVersion string, secret SecretRef) bool {
	if stored.Status == VerifyStatusNotChecked || stored.Status == "" {
		return true
	}
	if stored.ConfigVersion == "" || stored.ConfigVersion != currentConfigVersion {
		return true
	}
	if stored.Invalidated {
		return true
	}
	return layerNeedsCredential(stored.Layer) && SecretInvalidates(secret, currentConfigVersion)
}

// LayerState 单层投影（未登记=not_checked 缺省态；版本不符/失效/凭据轮换=
// not_checked 并注明原因，历史保留可查——旧结果绝不点亮新配置）。
func (m *McpVerifyModel) LayerState(layer string) VerifyLayerState {
	m.mu.Lock()
	stored, ok := m.layers[layer]
	cur := m.configVersion
	secret := m.secret.Snapshot()
	m.mu.Unlock()
	if !ok {
		return VerifyLayerState{Layer: layer, Status: VerifyStatusNotChecked, Detail: unlitDefaultText(layer)}
	}
	if VerifyStale(stored, cur, secret) {
		return unlitProjection(stored, cur)
	}
	return stored
}

// LayerStates 六层全量投影（v0→v5 定序）。
func (m *McpVerifyModel) LayerStates() []VerifyLayerState {
	m.mu.Lock()
	stored := make(map[string]VerifyLayerState, len(m.layers))
	for k, v := range m.layers {
		stored[k] = v
	}
	cur := m.configVersion
	secret := m.secret.Snapshot()
	m.mu.Unlock()
	out := make([]VerifyLayerState, 0, len(allLayers))
	for _, layer := range allLayers {
		st, ok := stored[layer]
		if !ok {
			out = append(out, VerifyLayerState{Layer: layer, Status: VerifyStatusNotChecked, Detail: unlitDefaultText(layer)})
			continue
		}
		if VerifyStale(st, cur, secret) {
			out = append(out, unlitProjection(st, cur))
		} else {
			out = append(out, st)
		}
	}
	return out
}

// History 状态历史（时间序；时间与来源可查）。返回副本，调用方改不动内部账。
func (m *McpVerifyModel) History() []VerifyHistoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]VerifyHistoryEntry, len(m.history))
	copy(out, m.history)
	return out
}

// VerifyBoardSnapshot 板面全量快照（W03-04 页面一次取全；可 JSON 序列化且永不含
// 明文凭据——测试金丝锁定）。
type VerifyBoardSnapshot struct {
	ConfigVersion string               `json:"config_version"`
	Layers        []VerifyLayerState   `json:"layers"`
	History       []VerifyHistoryEntry `json:"history"`
	Secret        SecretRef            `json:"secret"`
}

// Snapshot 板面全量快照。
func (m *McpVerifyModel) Snapshot() VerifyBoardSnapshot {
	return VerifyBoardSnapshot{
		ConfigVersion: m.ConfigVersion(),
		Layers:        m.LayerStates(),
		History:       m.History(),
		Secret:        m.Secret(),
	}
}

// unlitProjection 失效/版本不符的投影形态：槽位回落 not_checked 并如实注明原因
// （旧结果的事实仍在历史可查，但绝不点亮新配置）。
func unlitProjection(stored VerifyLayerState, currentVersion string) VerifyLayerState {
	out := VerifyLayerState{Layer: stored.Layer, Status: VerifyStatusNotChecked, CheckedAt: stored.CheckedAt}
	switch {
	case stored.ConfigVersion != currentVersion:
		out.Detail = fmt.Sprintf("配置已变更：既有结果绑定旧配置版本 %s，不点亮当前版本 %s（历史保留可查；请对新配置重新检查或由用户重新确认）。",
			versionOrEmpty(stored.ConfigVersion), versionOrEmpty(currentVersion))
	case stored.Invalidated:
		out.Detail = "结果已失效（" + stored.InvalidReason + "）；历史保留可查，请重新检查或由用户重新确认。"
	default:
		out.Detail = "凭据已轮换：旧配置相关验证失效（凭据相关层须重新配置后重新检查/确认；历史保留可查）。"
	}
	return out
}

// versionOrEmpty 版本展示（空锚如实）。
func versionOrEmpty(v string) string {
	if v == "" {
		return "（未锚定）"
	}
	return shortVersionHash(v)
}

// unlitDefaultText 未登记层的缺省态文案（§12.4：未运行显示「未验证」；V5 默认
// 不运行；v1/v3/v4 等待外部验证）。
func unlitDefaultText(layer string) string {
	switch layer {
	case LayerV0:
		return "未验证：V0 服务端检查未运行（页面默认只保证 V0；运行检查后如实点亮）。"
	case LayerV2:
		return "未验证：V2 服务端视角鉴权探测未运行（结果恒标服务端视角，不代证外部调用机）。"
	case LayerV5:
		return "未运行：执行验证默认不运行（会消耗模型额度），须用户明确发起后另行登记。"
	default:
		return "未验证：等待外部验证（本层服务端不可自证，首版无自动回执通道；外部事实以用户确认登记并标注来源）。"
	}
}

// normalizeUTCTime 时间归一 UTC（空=当前时刻；非 RFC3339 拒绝——时间序列化一律
// UTC，勿格式化成本地字符串）。
func normalizeUTCTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nowUTCString(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("时间须为 RFC3339 格式（UTC）: %w", err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// v0Summary V0 结果的人话摘要（历史/槽位 Detail；不回显凭据、不回显文件内容）。
func v0Summary(check V0Check) string {
	switch check.Status {
	case VerifyStatusPass:
		return fmt.Sprintf("服务端 V0 检查通过（%d 项；仅证明配置侧就绪，不推断 V1–V5）。", len(check.Checks))
	case VerifyStatusNotChecked:
		return "服务端 V0 检查未运行（输入不足）。"
	default:
		var fails []string
		for _, it := range check.Checks {
			if it.Status == VerifyStatusFail {
				fails = append(fails, it.Key+"："+it.Detail)
			}
		}
		return "V0 未通过（" + strings.Join(fails, "；") + "）"
	}
}

// ══════════════════════════ 诊断指引文本生成器（§12.4 失败显示列逐层） ══════════════════════════

// VerifyGuidance 单层失败/未验状态的可复制人话指引。
type VerifyGuidance struct {
	Layer string   `json:"layer"`
	Label string   `json:"label"` // LayerLabel(layer)
	Kind  string   `json:"kind"`  // 失败细分（层内词表）或 not_checked
	Title string   `json:"title"`
	Steps []string `json:"steps"` // 可复制指引步骤
}

// V1 指引细分：转发缺失（外部调用机无安全转发通路）。
const ConnKindForwardMissing = "forward_missing"

// GuidanceFor 生成单层指引。kind 为层内失败细分或 not_checked：
//
//	v0: command_path | credential | service_url | format | topology | scope | not_checked
//	v1: dns | refused | tls | timeout | forward_missing | other | not_checked
//	v2: 401 | 403 | network | unexpected | not_checked
//	v3: not_reloaded | start_failed | protocol_missing | not_checked
//	v4: empty_directory | connection_failed | not_checked
//	v5: accepted | execution_failed | tool_permission | work_failed | not_checked
//
// 未知层/未知 kind → ok=false（不猜测、不产出含糊文案）。
func GuidanceFor(layer, kind string) (VerifyGuidance, bool) {
	g := VerifyGuidance{Layer: layer, Label: LayerLabel(layer), Kind: kind}
	switch layer {
	case LayerV0:
		return v0Guidance(&g, kind)
	case LayerV1:
		return v1Guidance(&g, kind)
	case LayerV2:
		return v2Guidance(&g, kind)
	case LayerV3:
		return v3Guidance(&g, kind)
	case LayerV4:
		return v4Guidance(&g, kind)
	case LayerV5:
		return v5Guidance(&g, kind)
	default:
		return VerifyGuidance{}, false
	}
}

func v0Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	switch kind {
	case V0KeyCommandPath:
		g.Title = "V0 配置就绪失败：缺路径"
		g.Steps = []string{
			"字段 mcp_command_path 缺失或仍为占位符：请填写调用机上的 swarmmind-mcp 绝对路径（本端不代填服务器路径）。",
			"填好后重新运行 V0 检查；自动写入（apply）同样要求该路径真实存在。",
		}
	case V0KeyCredential:
		g.Title = "V0 配置就绪失败：缺凭据"
		g.Steps = []string{
			"凭据位仍为占位：先经人工授权/轮换取得一次性凭据（本机：主人控制台共享编排凭据；中心：管理员签发 PAT），再补齐配置。",
			"明文凭据只在授权操作结果中一次展示；模型与页面只保留脱敏引用。",
		}
	case V0KeyServiceURL:
		g.Title = "V0 配置就绪失败：服务地址非法"
		g.Steps = []string{
			"字段 service_url 非法：须为 http/https 且带 host:port（详见检查明细）。",
			"外部调用机场景请填写调用机可达的地址（经安全转发），不要使用仅本端可达的地址。",
		}
	case "format":
		g.Title = "V0 配置就绪失败：配置格式错误"
		g.Steps = []string{
			"宿主配置文件解析失败（JSON 语法/结构问题，明细只含错误偏移量、不含文件内容）。",
			"先修复宿主配置文件语法，或经预览/应用重新生成本条目；非法文件不会被自动合并。",
		}
	case V0KeyTopology:
		g.Title = "V0 配置就绪失败：危险拓扑（回环地址）"
		g.Steps = []string{
			"服务地址为回环地址（127.0.0.1/localhost/::1/0.0.0.0），而 MCP 进程在外部调用机上：回环地址指向调用机自身，从调用机连不到本端。",
			"请改用调用机可达的转发地址后重新检查；错误回环地址不会点亮任何外部验证层。",
		}
	case V0KeyScopeValid, V0KeyScopeConsistency:
		g.Title = "V0 配置就绪失败：范围不一致"
		g.Steps = []string{
			"目标范围须为 node（调用本机）或 hub（调用中心）；宿主既有条目的 SWARM_TARGET 与所选范围不一致时须核对配置名与目标。",
			"同一宿主允许本机/中心并存：建议改用带后缀的配置名，或显式确认替换。",
		}
	case VerifyStatusNotChecked:
		g.Title = "V0 未检查"
		g.Steps = []string{"提供配置输入（范围/宿主/服务地址/路径/凭据引用）后运行服务端 V0 检查；页面默认只保证 V0。"}
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}

func v1Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	switch kind {
	case ConnKindDNS:
		g.Title = "V1 调用机连接失败：DNS 解析失败"
		g.Steps = []string{
			"在调用机上核对服务地址主机名能否解析（nslookup/ping）：检查域名拼写与内网 DNS 配置。",
			"若经转发/内网穿透访问，确认转发域名仍在有效期。",
			"连接未打通前，授权与工具发现都无从谈起（先过 V1）。",
		}
	case ConnKindRefused:
		g.Title = "V1 调用机连接失败：连接被拒绝"
		g.Steps = []string{
			"地址可达但端口未监听或被防火墙拒绝：核对服务地址与端口、服务是否在监听、转发规则是否放行。",
			"本端管理面只监听回环地址：外部访问须经已配置的安全转发，不要直接开放公网入口。",
		}
	case ConnKindTLS:
		g.Title = "V1 调用机连接失败：证书错误"
		g.Steps = []string{
			"TLS 证书校验失败：核对证书域名、系统时间与 CA 链是否受调用机信任。",
			"不建议跳过 TLS 验证（--insecure/忽略证书类开关）：先修证书身份。",
		}
	case ConnKindTimeout:
		g.Title = "V1 调用机连接失败：连接超时"
		g.Steps = []string{
			"网络不可达或转发未放行：核对调用机到本端的路由、防火墙与转发配置后重试。",
		}
	case ConnKindForwardMissing:
		g.Title = "V1 调用机连接失败：转发缺失"
		g.Steps = []string{
			"外部调用机到本端没有已配置的安全转发通路：先配置转发/隧道（默认不开放管理面公网入口），再用调用机可达地址重试。",
		}
	case ConnKindOther:
		g.Title = "V1 调用机连接失败"
		g.Steps = []string{
			"在调用机上访问服务地址的能力元信息端点（GET " + v2ProbePath + "）复现错误，按报错分类处理。",
		}
	case VerifyStatusNotChecked:
		g.Title = "V1 未验证：等待外部验证"
		g.Steps = []string{
			"本层由外部 MCP 进程实际访问 Node/Hub 证实，服务端不能自证（首版无自动回执通道）。",
			"可在调用机连通后由用户在此登记确认（标注为用户确认，非服务端验证）。",
		}
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}

func v2Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	switch kind {
	case V2FailKindUnauthorized:
		g.Title = "V2 授权检查失败（服务端视角）：凭据未通过认证（401）"
		g.Steps = []string{
			"凭据可能已过期或被撤销：转人工授权入口重新签发/轮换（本机：主人控制台轮换共享编排凭据；中心：管理员 PAT）。",
			"更新宿主配置中的新凭据后重试；旧凭据不会自动恢复。",
		}
	case V2FailKindForbidden:
		g.Title = "V2 授权检查失败（服务端视角）：未授权（403）"
		g.Steps = []string{
			"凭据被识别但未获授权：编排授权未启用或权限不足——转人工授权入口（本机：主人控制台启用 MCP 编排授权；中心：管理员核对 PAT 权限）。",
		}
	case V2FailKindNetwork:
		g.Title = "V2 授权检查未执行：连接未建立"
		g.Steps = []string{
			"探测在连接层失败（属 V1 调用机连接层问题，非授权问题）：先按 V1 指引打通连接。",
			"连接未建立时不能对凭据有效或无效下任何结论。",
		}
	case V2FailKindUnexpected:
		g.Title = "V2 授权检查未得出结论（服务端视角）"
		g.Steps = []string{
			"目标端点返回意外状态：核对目标服务健康状态后重试（不猜测授权结论）。",
		}
	case VerifyStatusNotChecked:
		g.Title = "V2 未检查"
		g.Steps = []string{
			"运行服务端视角鉴权探测（GET " + v2ProbePath + "，零模型零派单）；结果恒标服务端视角——外部调用机的授权有效性以外部实际调用为准。",
		}
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}

func v3Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	switch kind {
	case "not_reloaded":
		g.Title = "V3 宿主工具发现失败：宿主配置未重载"
		g.Steps = []string{
			"宿主可能仍在用旧配置：重载/重启宿主（或其 MCP 配置重载机制）后重新执行 tools/list。",
			"apply 成功不等于宿主已重载——写入与生效是两步。",
		}
	case "start_failed":
		g.Title = "V3 宿主工具发现失败：适配进程启动失败"
		g.Steps = []string{
			"在调用机上手动运行配置的 swarmmind-mcp 命令查看启动报错（路径/权限/依赖）。",
			"修复后重载宿主再试。",
		}
	case "protocol_missing":
		g.Title = "V3 宿主工具发现失败：协议或版本缺项"
		g.Steps = []string{
			"核对宿主与 swarmmind-mcp 的 MCP 协议版本兼容性；缺工具时升级对应一端。",
			"普通 REST 查询日志只证明被调用，不证明宿主完成 tools/list——此层不能靠服务端自测冒认。",
		}
	case VerifyStatusNotChecked:
		g.Title = "V3 未验证：等待外部验证"
		g.Steps = []string{
			"宿主完成 MCP 初始化与 tools/list 只能由外部宿主证实（首版无自动回执通道）。",
			"可在宿主中确认工具列表后由用户在此登记确认（标注为用户确认，非服务端验证）。",
		}
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}

func v4Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	switch kind {
	case "empty_directory":
		g.Title = "V4 无副作用查询：目录为空"
		g.Steps = []string{
			"工具已可调用且返回了空目录：目录为空≠连接失败——接入链路已通，先登记 agent/接入节点。",
			"按返回内容核对范围（本机/全网）与来源是否可核实。",
		}
	case "connection_failed":
		g.Title = "V4 无副作用查询失败：工具调用未成功"
		g.Steps = []string{
			"区分网络问题（回 V1）与授权问题（回 V2）后，重试只读目录/状态工具。",
		}
	case VerifyStatusNotChecked:
		g.Title = "V4 未验证：等待外部验证"
		g.Steps = []string{
			"由外部宿主调用目录/状态类工具（零副作用）验证范围与来源；服务端不能自证此层。",
		}
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}

func v5Guidance(g *VerifyGuidance, kind string) (VerifyGuidance, bool) {
	// V5 公共边界句：执行验证会消耗模型额度，默认不运行（§12.4）。
	common := []string{
		"执行验证会消耗模型额度，默认不运行；须用户明确发起一项低影响任务/会话（独立工作区、指定测试 agent），确认后才提交。",
		"结局须区分四类：任务被接受/执行失败/工具权限拒绝/工作失败——逐类核对，不以「已连接」掩盖失败。",
	}
	switch kind {
	case "accepted":
		g.Title = "V5 执行验证：任务被接受"
		g.Steps = []string{
			"任务被接受≠执行成功：继续核对执行状态与工作结果。",
			common[0], common[1],
		}
	case "execution_failed":
		g.Title = "V5 执行验证：执行失败"
		g.Steps = append([]string{"任务被接受但执行失败：按任务追踪与轮次结果定位原因。"}, common...)
	case "tool_permission":
		g.Title = "V5 执行验证：工具权限拒绝"
		g.Steps = append([]string{"执行中工具权限被拒：由有权用户在支持通道批准，或改用已授权工具子集重试。"}, common...)
	case "work_failed":
		g.Title = "V5 执行验证：工作失败"
		g.Steps = append([]string{"执行完成但工作结果不合格：核对工作区与验收说明后决定重试或放弃。"}, common...)
	case VerifyStatusNotChecked:
		g.Title = "V5 默认不运行"
		g.Steps = common
	default:
		return VerifyGuidance{}, false
	}
	return *g, true
}
