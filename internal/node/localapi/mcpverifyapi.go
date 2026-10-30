// mcpverifyapi.go MCP 分层验证板端点接线（v3 W03-04，docs/tasks/v3/W03-04-rfc.md §1）：
// W03-03 模型层（mcpverify.go）此前未接路由，本文件是其唯一接线面——纯加法：
//   - GET  /api/v1/local/mcp/verify            板面全量快照 + 逐层 not_checked 指引
//   - POST /api/v1/local/mcp/verify/v0         服务端 V0 检查（RunV0Checks→RecordV0）
//   - POST /api/v1/local/mcp/verify/v2         服务端视角鉴权探测（恒标 server_side_view）
//   - POST /api/v1/local/mcp/verify/confirm    用户确认登记（v1/v3/v4/v5）
//   - POST /api/v1/local/mcp/verify/config-version  配置版本锚更新（apply/revert 后页面驱动）
//
// 鉴权：五端点全部 authLocal（仅 local_token，prime_token 一律 401）——外部验证
// 证据的登记主体是人、V0/V2 检查读宿主配置结构事实并瞬间使用共享凭据，SPEC 六.六
// 豁免⑮（逐端点依据见 SPEC 原文）；零写盘（板面为进程内存态，重启如实回落）。
//
// 纪律（改动前先读本段）：
//   - 本文件只消费既有模型/端点层公开方法（mcpverify.go/mcpconfig.go/mcpcfgfile.go/
//     mcpaccess.go/primewire.go 零改动）；轮换交接面不经挂钩 rotate 端点实现，改由
//     GET 板面的服务端漂移检测承担（秘密脱敏引用 ≠ 当前凭据脱敏引用 → RotateSecret，
//     RFC §1）——mcpaccess.go 既有行零动。
//   - 凭据纪律：请求/应答绝不含明文凭据——请求面无凭据字段（v0/v2 由服务端自取
//     当前共享凭据，明文只在探测瞬间使用）；应答只有 maskCredential 脱敏引用
//     （板面 secret 引用 + credential_masked 字段），金丝测试锁定。
//   - 点亮纪律：V0/V2 为服务端视角（CheckedBy 恒 server_side_view，模型层强制）；
//     V1/V3/V4/V5 仅用户确认；版本锚变更后旧结果如实回落 not_checked（历史保留）。
//     任何输出面绝不出现「外部已验证」类冒认措辞（mcpverify_test 禁词金丝同源）。
//   - 时间一律 UTC RFC3339（模型层 normalizeUTCTime/nowUTCString 承担）。
package localapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// mcpVerifyProbeTimeout V2 服务端视角探测超时（DefaultV2Probe 装配值；能力元信息
// 只读端点，正常毫秒级，5s 覆盖慢盘/慢网不拖死请求面）。
const mcpVerifyProbeTimeout = 5 * time.Second

// mcpVerifyModel 惰性取验证板实例（进程内存态；凭据 env 变量名沿 W03-01 既有契约
// SWARM_NODE_PRIME_TOKEN——node 范围共享编排凭据在宿主配置中的变量名）。
func (s *Server) mcpVerifyModel() *McpVerifyModel {
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	if s.verifyModel == nil {
		s.verifyModel = NewMcpVerifyModel("", envNodeToken)
	}
	return s.verifyModel
}

// mcpVerifyResponse GET/变更端点公共应答骨架（绝不含明文凭据）。
type mcpVerifyResponse struct {
	Board            VerifyBoardSnapshot       `json:"board"`
	CredentialMasked string                    `json:"credential_masked"`
	CredentialEnvVar string                    `json:"credential_env_var"`
	McpAccessEnabled bool                      `json:"mcp_access_enabled"`
	Guidance         map[string]VerifyGuidance `json:"guidance"` // 逐层「如何检查本层」指引（模型同源文案，页面零二次创作）
	Note             string                    `json:"note,omitempty"`
}

// mcpVerifySeenTok / mcpVerifySetSeenTok 最近一次秘密就位观测的完整凭据值（verifyMu
// 串行；仅内存漂移比对用，绝不进任何应答/日志——脱敏引用只保留前 2 字符，不同令牌
// 可撞同一脱敏形态（fa********），轮换漂移检测必须全长比对，R1 自查修正）。
func (s *Server) mcpVerifySeenTok() string {
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	return s.verifySeenTok
}

func (s *Server) mcpVerifySetSeenTok(tok string) {
	s.verifyMu.Lock()
	s.verifySeenTok = tok
	s.verifyMu.Unlock()
}

// mcpVerifyBuildResponse 组装公共应答。漂移检测先行：秘密已登记 user_provided 且
// 当前共享凭据与就位观测值不符（轮换/换发）→ RotateSecret（凭据相关层失效留痕，
// 历史保留）并把观测值推进到当前值（每轮轮换只触发一次，不重复堆 invalidated
// 历史）——交接面「轮换后 RotateSecret」的零侵入实现（RFC §1）；当前凭据为空不
// 触发（凭据未生成≠已轮换，占位态语义不变）。
func (s *Server) mcpVerifyBuildResponse() mcpVerifyResponse {
	m := s.mcpVerifyModel()
	if sec := m.Secret(); sec.State == SecretStateUserProvided {
		if tok := s.primeToken(); tok != "" && tok != s.mcpVerifySeenTok() {
			m.RotateSecret()
			s.mcpVerifySetSeenTok(tok)
		}
	}
	guidance := make(map[string]VerifyGuidance, len(allLayers))
	for _, layer := range allLayers {
		if g, ok := GuidanceFor(layer, VerifyStatusNotChecked); ok {
			guidance[layer] = g
		}
	}
	enabled, _, _ := s.mcpAccessState()
	return mcpVerifyResponse{
		Board:            m.Snapshot(),
		CredentialMasked: maskCredential(s.primeToken()),
		CredentialEnvVar: envNodeToken,
		McpAccessEnabled: enabled,
		Guidance:         guidance,
		Note: "验证板为本机主人管理面：六层状态、检查视角（服务端视角/用户确认）与配置版本锚均如实呈现；" +
			"任何结果不推断上一层之外的结论，配置 apply/revert/换范围/轮换后相关验证如实回落「未验证」。",
	}
}

// handleMcpVerifyGet GET /api/v1/local/mcp/verify：板面全量快照。
func (s *Server) handleMcpVerifyGet(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	writeJSON(w, http.StatusOK, s.mcpVerifyBuildResponse())
}

// —— POST verify/v0：服务端 V0 配置就绪检查 ——

// mcpVerifyV0Request v0 请求体（RFC §1.1；请求面绝无凭据字段——凭据位服务端自取）。
type mcpVerifyV0Request struct {
	Scope          string `json:"scope"`            // node|hub 必填
	CallerEnv      string `json:"caller_env"`       // same_machine|remote 必填
	Host           string `json:"host"`             // zcode|codex|其他（未知宿主通用手动）
	ServerURL      string `json:"server_url"`       // ValidateServiceURL
	McpCommandPath string `json:"mcp_command_path"` // 调用机上的 swarmmind-mcp 绝对路径
	ConfigName     string `json:"config_name"`      // 空=按 scope 默认命名
	ConfigHome     string `json:"config_home"`      // 受控宿主配置根（node 可选；hub 强制置空）
}

// handleMcpVerifyV0 POST /api/v1/local/mcp/verify/v0：运行 RunV0Checks（宿主配置
// 安全读复用 mcpcfgfile 原语，只读零写盘）→ 版本非空即重锚（旧结果如实回落）→
// RecordV0 → node 凭据在位时 MarkSecretProvided（脱敏引用+当前锚）。应答 {check, board}。
func (s *Server) handleMcpVerifyV0(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpVerifyV0Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-04-rfc.md §1.1）: "+err.Error())
		return
	}
	req.Scope = strings.TrimSpace(req.Scope)
	req.CallerEnv = strings.TrimSpace(req.CallerEnv)
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	req.ConfigName = strings.TrimSpace(req.ConfigName)
	req.ConfigHome = strings.TrimSpace(req.ConfigHome)
	req.McpCommandPath = strings.TrimSpace(req.McpCommandPath)
	if req.Scope != ScopeNode && req.Scope != ScopeHub {
		writeErr(w, http.StatusBadRequest, "invalid_request", "scope 非法（合法值 node|hub），不猜测")
		return
	}
	if req.CallerEnv != CallerSameMachine && req.CallerEnv != CallerRemote {
		writeErr(w, http.StatusBadRequest, "invalid_request", "caller_env 非法（合法值 same_machine|remote），不猜测")
		return
	}
	if err := ValidateServiceURL(req.ServerURL); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Scope == ScopeHub {
		// hub 目标的宿主配置在调用机上：本端不读、页面也不传（RFC §1.1）。
		req.ConfigHome = ""
	}
	// 凭据位（脱敏引用入模型，明文绝不入检查输入）：node=当前共享凭据脱敏引用
	// （未生成=占位语义，V0 凭据项如实失败指路授权入口）；hub=PAT 占位（中心凭据
	// 本端不持有，如实占位）。
	credRef := templateCredentialPlaceholder
	if req.Scope == ScopeNode {
		credRef = maskCredential(s.primeToken())
	}
	m := s.mcpVerifyModel()
	check := RunV0Checks(V0CheckInput{
		Scope: req.Scope, CallerEnv: req.CallerEnv, Host: req.Host,
		ServerURL: req.ServerURL, McpCommandPath: req.McpCommandPath,
		CredentialRef: credRef, ConfigName: req.ConfigName, ConfigHome: req.ConfigHome,
	})
	// 文件版本事实即配置版本锚（模型 ConfigVersion 取值源口径）：锚变更 → 旧绑定
	// 结果批量回落 not_checked（历史保留可查）——「配置变更使旧结果失效」的落地点。
	if check.ConfigVersion != "" && check.ConfigVersion != m.ConfigVersion() {
		if err := m.SetConfigVersion(check.ConfigVersion); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "internal", "配置版本锚更新失败: "+err.Error())
			return
		}
	}
	m.RecordV0(check)
	// 新凭据就位登记：node 范围 + 凭据检查通过 + 锚已定 → 秘密状态机 placeholder/
	// rotated_invalid → user_provided（脱敏引用+当前锚）；观测值同步推进（漂移检测
	// 基准——全长比对，见 mcpVerifySeenTok 注）。
	if req.Scope == ScopeNode && check.ConfigVersion != "" && s.primeToken() != "" {
		for _, it := range check.Checks {
			if it.Key == V0KeyCredential && it.Status == VerifyStatusPass {
				_ = m.MarkSecretProvided(maskCredential(s.primeToken()), check.ConfigVersion)
				s.mcpVerifySetSeenTok(s.primeToken())
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": check, "board": s.mcpVerifyModel().Snapshot()})
}

// —— POST verify/v2：服务端视角鉴权探测 ——

// mcpVerifyV2Request v2 请求体（凭据不入参：服务端自取当前共享凭据，明文只在探测
// 瞬间经 DefaultV2Probe 使用，绝不进应答/日志）。
type mcpVerifyV2Request struct {
	Scope      string `json:"scope"`       // 仅 node（hub 凭据为本端不持有的 PAT）
	ServiceURL string `json:"service_url"` // ValidateServiceURL
}

// handleMcpVerifyV2 POST /api/v1/local/mcp/verify/v2：RunV2ProbeCheck（DefaultV2Probe
// 装配，GET capabilities 零模型零派单无副作用）→ RecordV2。结果恒标
// server_side_view（模型层强制），绝不代证外部调用机的授权有效性。
func (s *Server) handleMcpVerifyV2(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpVerifyV2Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-04-rfc.md §1.1）: "+err.Error())
		return
	}
	req.Scope = strings.TrimSpace(req.Scope)
	if req.Scope != ScopeNode {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"scope 须为 node：中心范围凭据为 PAT（本机不持有），服务端视角鉴权探测仅本机共享编排凭据可执行；中心范围请经 hub 控制台人工核对")
		return
	}
	tok := s.primeToken()
	if tok == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"共享编排凭据未生成：请先在「MCP 编排授权」卡轮换生成后再运行 V2 探测")
		return
	}
	if err := ValidateServiceURL(req.ServiceURL); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	m := s.mcpVerifyModel()
	check := RunV2ProbeCheck(DefaultV2Probe(mcpVerifyProbeTimeout), V2CheckInput{
		ServiceURL: req.ServiceURL, Credential: tok, ConfigVersion: m.ConfigVersion(),
	})
	m.RecordV2(check)
	writeJSON(w, http.StatusOK, map[string]any{"check": check, "board": s.mcpVerifyModel().Snapshot()})
}

// —— POST verify/confirm：用户确认登记 ——

// mcpVerifyConfirmRequest confirm 请求体（透传 ExternalConfirmation 模型契约：仅
// v1/v3/v4/v5、source 恒 user_confirmed、config_version 必填、疑似明文 note 拒收）。
type mcpVerifyConfirmRequest struct {
	Layer         string `json:"layer"`
	ConfigVersion string `json:"config_version"`
	Note          string `json:"note,omitempty"`
}

// handleMcpVerifyConfirm POST /api/v1/local/mcp/verify/confirm：登记用户确认（首版
// 唯一外部证据形态）。模型校验失败（层非法/版本空/疑似明文 note）→ 400 原样透传
// 模型错误文案（不二次包装——文案即指引）。确认版本与当前锚不符时模型按 not_checked
// 呈现（旧回执不点亮新配置），应答 state 如实回传。
func (s *Server) handleMcpVerifyConfirm(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpVerifyConfirmRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-04-rfc.md §1.1）: "+err.Error())
		return
	}
	m := s.mcpVerifyModel()
	state, err := m.RegisterExternalConfirmation(ExternalConfirmation{
		Layer:         strings.TrimSpace(req.Layer),
		ConfigVersion: strings.TrimSpace(req.ConfigVersion),
		Note:          req.Note,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "board": s.mcpVerifyModel().Snapshot()})
}

// —— POST verify/config-version：配置版本锚更新 ——

// mcpVerifyVersionRequest config-version 请求体（apply/revert 应答的 version_after）。
type mcpVerifyVersionRequest struct {
	ConfigVersion string `json:"config_version"`
}

// handleMcpVerifyVersion POST /api/v1/local/mcp/verify/config-version：配置版本锚
// 更新（W03-03 交接面「apply 后 SetConfigVersion」的页面驱动落点）——apply/revert
// 成功后页面以应答 version_after 调用，旧绑定结果如实回落 not_checked（历史保留）。
func (s *Server) handleMcpVerifyVersion(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req mcpVerifyVersionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON（docs/tasks/v3/W03-04-rfc.md §1.1）: "+err.Error())
		return
	}
	m := s.mcpVerifyModel()
	if err := m.SetConfigVersion(strings.TrimSpace(req.ConfigVersion)); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board": s.mcpVerifyModel().Snapshot()})
}
