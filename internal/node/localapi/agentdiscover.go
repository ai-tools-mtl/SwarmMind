// 本机 agent 发现与登记预览（v3 W06-02，接线 internal/node/agentdetect 四层只读
// 检测包；任务书 docs/tasks/v3/W06-resources.md「检测模型与变更原则」）：
//   - POST /api/v1/local/agents/discover            显式候选只读发现：只探测调用方
//     逐个显式给出的命令（不做磁盘/PATH 全盘扫描，对齐 W06-01 纪律），返回四层
//     Report（纯元数据零秘密）+「允许本机调用」与「exposed 对中心开放」两开关的
//     当前值与差异说明。发现≠登记：零写盘、绝不自动开放能力。
//   - POST /api/v1/local/agents/registration-preview 登记预览：入参登记面（与既有
//     PUT /api/v1/local/config 的 agents[i] 同一 AgentProfile 形态，零双 schema），
//     输出与现有配置的差异预览（新增/修改/无变化 + 逐字段 old→new）+ 检测四层即时
//     结果；权限模式全权档位（yolo/danger-full-access）预览即拒（与 sessions 同值
//     双防线）。保存仍走既有 PUT /api/v1/local/config + POST /local/agents/reload
//     链——本文件不新增任何写盘路径（单测 grep 断言）。
//
// 检测投影：GET /api/v1/local/agents 增量携带检测摘要（installed/config_recognized/
// execution_ready/call_allowed 四值 + 检查时间 + 未验证原因），数据源 = 上次
// discover/preview 结果的进程内存缓存（重启清零——未发现过 = not_checked 如实，
// 不落盘、不外发）。加法字段，旧字段零变化；Hub 目录投影（/api/v1/agents 及 hub
// web）本轮不动——检测摘要属节点本地真相，全网投影归 W07-01/C 阶段。
//
// 鉴权：两端点均 authLocal（仅 local_token，prime_token 一律 401）——扫描探测 =
// 主人授权行为、登记写盘 = 执行权配置（写 agents 表即执行权），SPEC 六.六豁免⑫。
package localapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/node/agentdetect"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// discoverMaxCandidates 单次发现候选上限（体积与子进程次数护栏；发现是主人显式
// 行为，候选量级为个位数，超限提示分批）。
const discoverMaxCandidates = 16

// discoverScanner 四层检测器（包默认超时/输出上限：--version 5s、64KiB；
// agentdetect.Options 零值即默认，无运行期可调面）。无状态可并发复用。
var discoverScanner = agentdetect.New(agentdetect.Options{})

// discoverCandidate 发现候选（调用方显式给出的探测输入；缺省字段对应检测层如实
// not_checked——两者都缺即零扫描候选）。
type discoverCandidate struct {
	Name       string `json:"name"`                  // 展示/缓存键（登记名或预备登记名）
	Host       string `json:"host,omitempty"`        // 宿主标识：codex|zcode；其他值按未知宿主通用手动
	Command    string `json:"command,omitempty"`     // 命令绝对路径；空 = 跳过安装层（不探测）
	ConfigHome string `json:"config_home,omitempty"` // 宿主配置 home 绝对路径；空 = 跳过配置层
}

// discoverRequest POST /api/v1/local/agents/discover 请求体。
type discoverRequest struct {
	Candidates []discoverCandidate `json:"candidates"`
}

// switchState 两开关当前值（「允许本机调用」/「exposed 对中心开放」）。
type switchState struct {
	On     bool   `json:"on"`
	Source string `json:"source,omitempty"` // 当前值来源：config_registered | not_registered
	Note   string `json:"note,omitempty"`   // 差异说明（人话）
}

// discoverResult 单候选发现结果。
type discoverResult struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
	// Command / ConfigHome 候选输入回显（调用方自报输入原样返回，供登记预览
	// 表单预填；纯路径引用，无内容）。
	Command      string             `json:"command,omitempty"`
	ConfigHome   string             `json:"config_home,omitempty"`
	Report       agentdetect.Report `json:"report"`
	Registered   bool               `json:"registered"`              // 是否已登记在本机配置
	RegisteredBy string             `json:"registered_by,omitempty"` // 命中方式：name | command
	LocalCall    switchState        `json:"local_call"`              // 允许本机调用（当前值）
	Exposed      switchState        `json:"exposed"`                 // exposed 对中心开放（当前值）
}

// callProjectionFor 按登记态构造调用授权投影（call_allowed 层唯一事实来源是调用
// 方投影，本包不判定、只承载——agentdetect 包口径）：已登记 = 本机派单可调用；
// 未登记 = 不允许（发现≠登记，登记才建立本机调用权）。
func callProjectionFor(registered bool) *agentdetect.CallProjection {
	if registered {
		return &agentdetect.CallProjection{
			Allowed: true,
			Source:  "config_registered_local",
			Reason:  "已登记本机能力：本机派单可调用（与 exposed 远端开放相互独立）",
		}
	}
	return &agentdetect.CallProjection{
		Allowed: false,
		Source:  "not_registered",
		Reason:  "未登记（发现≠登记）：本机调用权随登记建立，登记走预览+能力配置保存",
	}
}

// matchRegistered 在现有配置中按 name（优先）后 command 命中已登记能力。
func matchRegistered(profiles []protocol.ExecProfile, c discoverCandidate) (protocol.ExecProfile, string, bool) {
	for _, p := range profiles {
		if p.Name == c.Name {
			return p, "name", true
		}
	}
	if c.Command != "" {
		for _, p := range profiles {
			if p.Command == c.Command {
				return p, "command", true
			}
		}
	}
	return protocol.ExecProfile{}, "", false
}

// previewBaseline 差异/登记态判定基准（与 GET /api/v1/local/config 第六轮修复 1
// 同口径）：结构性变更「待应用」窗口期，盘上 node.yaml = 用户最新提交真相源——
// 刚保存登记面再预览不得误报「新增」；读盘/解析失败回退运行时基线。
func (s *Server) previewBaseline() []protocol.ExecProfile {
	if ops := s.cfg.ConfigOps; ops != nil && s.pendingRestart() {
		if disk, err := s.diskAgentProfiles(ops); err == nil {
			return disk
		}
	}
	return s.effectiveProfiles()
}

// handleAgentsDiscover POST /api/v1/local/agents/discover：显式候选逐个只读探测
// （r.Context() 贯穿——客户端断开即取消在途 --version 子进程）。候选名命中缓存键
// 记入内存投影源；登记态只影响 call_allowed 投影与两开关展示，不改任何检测层。
func (s *Server) handleAgentsDiscover(w http.ResponseWriter, r *http.Request) {
	var req discoverRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"candidates":[{name,host,command,config_home}]}: `+err.Error())
		return
	}
	if len(req.Candidates) > discoverMaxCandidates {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("候选数 %d 超上限 %d（发现只处理显式候选，请分批）", len(req.Candidates), discoverMaxCandidates))
		return
	}
	profiles := s.previewBaseline()
	results := make([]discoverResult, 0, len(req.Candidates))
	for i, c := range req.Candidates {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			writeErr(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("candidates[%d]: name 必填（展示与检测投影键）", i))
			return
		}
		if len(c.Name) > 64 {
			writeErr(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("candidates[%d]: name 超长（≤64 字符）", i))
			return
		}
		old, by, registered := matchRegistered(profiles, c)
		res := discoverResult{
			Name: c.Name, Host: c.Host,
			Command: c.Command, ConfigHome: c.ConfigHome, // 候选输入回显（登记预览预填）
			Report: discoverScanner.Detect(r.Context(), agentdetect.Request{
				Host:       agentdetect.Host(c.Host),
				Name:       c.Name,
				Command:    c.Command,
				ConfigHome: c.ConfigHome,
				Call:       callProjectionFor(registered),
			}),
			Registered:   registered,
			RegisteredBy: by,
		}
		res.LocalCall = localCallState(registered)
		res.Exposed = exposedState(registered, old.Exposed)
		s.recordDetect(res.Report)
		results = append(results, res)
	}
	note := "发现≠登记：本端点零写盘、不自动开放能力；登记走「登记预览 + 能力配置保存」（既有 PUT /api/v1/local/config 链，无第二写路径）。" +
		"「允许本机调用」随登记建立、本机派单不受 exposed 影响；exposed=false 只关闭 hub 下发新任务的准入，在途任务照常到终态。"
	if len(results) == 0 {
		note = "无候选：零扫描（本端点只探测显式给出的候选，不做磁盘/PATH 扫描）。" + note
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "note": note})
}

// localCallState 「允许本机调用」开关当前值。
func localCallState(registered bool) switchState {
	if registered {
		return switchState{On: true, Source: "config_registered",
			Note: "已登记：本机派单可调用（与 exposed 相互独立——exposed=false 不影响本机）"}
	}
	return switchState{On: false, Source: "not_registered",
		Note: "未登记：本机不可调用；登记（预览+保存）后即建立，无需额外开关"}
}

// exposedState exposed 开关当前值（未登记能力无开关；登记默认 exposed=true 可显式关）。
func exposedState(registered, exposed bool) switchState {
	if !registered {
		return switchState{On: false, Source: "not_registered",
			Note: "未登记能力无 exposed 开关；登记面默认 exposed=true，可在登记面显式关闭"}
	}
	if exposed {
		return switchState{On: true, Source: "config",
			Note: "对中心开放：能力经注册进入 hub 全网目录（hub 下发新任务准入）"}
	}
	return switchState{On: false, Source: "config",
		Note: "未开放：hub 下发新任务 404 agent_not_exposed；本机派单不受影响，在途任务照常到终态"}
}

// ---------- 登记预览（差异三态 + 检测四层；零写盘） ----------

// registrationPreviewRequest POST /api/v1/local/agents/registration-preview 请求体：
// 登记面与既有 PUT /api/v1/local/config 的 agents[i] 同一 AgentProfile 形态（零双
// schema、保存直接整表提交同一份）。DetectHost 为检测宿主标识覆盖（codex|zcode；
// 空 = 按 session_adapter 推导，两者皆空按未知宿主通用手动——如实不冒认）。
type registrationPreviewRequest struct {
	Agent      AgentProfile `json:"agent"`
	DetectHost string       `json:"detect_host,omitempty"`
}

// changedField 差异单字段（old/new 为展示字符串；列表/布尔同形字符串化）。
type changedField struct {
	Field string `json:"field"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
}

// registrationDiff 差异三态：create（新增）/ update（修改，携逐字段差异）/
// none（无变化）。
type registrationDiff struct {
	Kind          string         `json:"kind"`
	ChangedFields []changedField `json:"changed_fields,omitempty"`
}

// handleRegistrationPreview POST /api/v1/local/agents/registration-preview：
// 校验（与保存链同一 ValidateAgentProfiles）→ 差异（对运行时基线）→ 检测四层
// 即时结果。预览零写盘；结果记入检测投影缓存。全权档位预览即拒。
func (s *Server) handleRegistrationPreview(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Agents == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "未装配 agent 状态（装配方须传入 Agents）")
		return
	}
	var req registrationPreviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"agent":{…登记面…}}: `+err.Error())
		return
	}
	// 全权档位预览即拒（与 sessions 断言同值同防线：平台面不注入 yolo/
	// danger-full-access，提权须主人在 node.yaml 显式改配并重启）。
	if m := strings.TrimSpace(req.Agent.PermissionMode); m == "yolo" || m == "danger-full-access" {
		writeErr(w, http.StatusBadRequest, "invalid_request",
			"permission_mode "+m+" 为全权档位，登记预览即拒（平台不注入全权档位；如确需请手工编辑 node.yaml 并自行承担后果）")
		return
	}
	// 与保存链同源校验：预览被拒 = 保存必被拒（同一函数、同一消息）。
	if err := ValidateAgentProfiles([]AgentProfile{req.Agent}); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "登记面将被保存链拒绝: "+err.Error())
		return
	}
	next := req.Agent.ToExecProfile()
	profiles := s.previewBaseline()
	old, registered := findByName(profiles, next.Name)
	diff := diffRegistration(old, registered, req.Agent, next)
	// 结构性提示按单能力视角分类（不把「其余能力看似被删」的整表分类误报进预览）：
	// 新增/command/agent_args/kind/session_adapter 变更 → 保存后需「应用并重启」。
	basis := []protocol.ExecProfile(nil)
	if registered {
		basis = []protocol.ExecProfile{old}
	}
	structural := classifyProfilesChange(basis, []protocol.ExecProfile{next})

	rep := discoverScanner.Detect(r.Context(), agentdetect.Request{
		Host:       agentdetect.Host(previewDetectHost(req.DetectHost, req.Agent.SessionAdapter)),
		Name:       next.Name,
		Command:    next.Command,
		ConfigHome: next.ConfigHome,
		Call:       callProjectionFor(registered),
	})
	s.recordDetect(rep)
	writeJSON(w, http.StatusOK, map[string]any{
		"diff":       diff,
		"detect":     rep,
		"structural": structural,
		"registered": registered,
		"note":       "预览零写盘；确认后请将本登记面加入能力配置表并「保存全部（校验+写盘）」——保存走既有 PUT /api/v1/local/config（无第二写路径），结构性变更需「应用并重启」生效",
	})
}

// previewDetectHost 检测宿主标识：显式覆盖优先，其次按会话适配器推导（codex/
// zcode），皆空 = 未知宿主（检测层按通用手动处理，如实不冒认）。
func previewDetectHost(explicit, adapter string) string {
	if explicit != "" {
		return explicit
	}
	switch adapter {
	case protocol.SessionAdapterCodex:
		return string(agentdetect.HostCodex)
	case protocol.SessionAdapterZcode:
		return string(agentdetect.HostZcode)
	}
	return ""
}

// findByName 按名查现有配置。
func findByName(profiles []protocol.ExecProfile, name string) (protocol.ExecProfile, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	return protocol.ExecProfile{}, false
}

// diffRegistration 登记面 vs 现有配置差异（三态）。old/new 均经 ExecProfile 归一
// （同 ToExecProfile 口径，kind/exposed 缺省值对齐后逐字段比对）。
func diffRegistration(old protocol.ExecProfile, registered bool, in AgentProfile, next protocol.ExecProfile) registrationDiff {
	if !registered {
		return registrationDiff{Kind: "create"}
	}
	var changed []changedField
	add := func(field, o, n string) {
		if o != n {
			changed = append(changed, changedField{Field: field, Old: o, New: n})
		}
	}
	add("kind", string(old.Kind), string(next.Kind))
	add("command", old.Command, next.Command)
	add("workdir", old.Workdir, next.Workdir)
	add("agent_args", joinDisp(old.AgentArgs), joinDisp(in.AgentArgs))
	add("timeout_sec", itoa(old.TimeoutSec), itoa(in.TimeoutSec))
	add("allow_args", boolStr(old.AllowArgs), boolStr(in.AllowArgs))
	add("allow_env", joinDisp(old.AllowEnv), joinDisp(in.AllowEnv))
	add("config_home", old.ConfigHome, next.ConfigHome)
	add("description", old.Description, in.Description)
	add("permission_mode", old.PermissionMode, in.PermissionMode)
	add("exposed", boolStr(old.Exposed), boolStr(next.Exposed))
	add("stdin_to_arg", boolStr(old.StdinToArg), boolStr(in.StdinToArg))
	add("session_adapter", old.SessionAdapter, in.SessionAdapter)
	if len(changed) == 0 {
		return registrationDiff{Kind: "none"}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Field < changed[j].Field })
	return registrationDiff{Kind: "update", ChangedFields: changed}
}

// joinDisp 展示用列表串（逗号连接；空 = 空串）。
func joinDisp(items []string) string { return strings.Join(items, ",") }

// boolStr / itoa 展示字符串化。
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

// ---------- 检测投影缓存（进程内存态；GET /api/v1/local/agents 的数据源） ----------

// detectCacheEntry 缓存条目：上次检测报告 + 入缓存时刻（UTC）。
type detectCacheEntry struct {
	rep agentdetect.Report
	at  time.Time
}

// recordDetect 记入投影缓存（键=候选名；空名不记——投影按已登记能力名取）。
func (s *Server) recordDetect(rep agentdetect.Report) {
	if rep.Name == "" {
		return
	}
	s.detectMu.Lock()
	if s.detectCache == nil {
		s.detectCache = map[string]detectCacheEntry{}
	}
	s.detectCache[rep.Name] = detectCacheEntry{rep: rep, at: time.Now().UTC()}
	s.detectMu.Unlock()
}

// agentDetectSummary GET /api/v1/local/agents 增量投影字段（加法；旧字段零变化）。
type agentDetectSummary struct {
	Installed        string     `json:"installed"`                    // pass|fail|not_checked
	ConfigRecognized string     `json:"config_recognized"`            // 同上（codex 为粗检语义）
	ExecutionReady   string     `json:"execution_ready"`              // 同上（pass 只代表前置检查过、模型未验证）
	CallAllowed      string     `json:"call_allowed"`                 // 同上（本机调用授权当前值）
	CheckedAt        *time.Time `json:"checked_at,omitempty"`         // 四层最新检查时刻（UTC）；从未跑过缺省
	NotCheckedReason string     `json:"not_checked_reason,omitempty"` // 未验证原因（含「从未发现过」如实说明）
}

// detectSummaryFor 单能力检测摘要：未发现过 = 四值 not_checked + 如实原因
// （绝不冒认已检测）；已发现 = 四层状态 + 最新检查时刻 + not_checked 层原因聚合。
func (s *Server) detectSummaryFor(name string) agentDetectSummary {
	s.detectMu.Lock()
	ent, ok := s.detectCache[name]
	s.detectMu.Unlock()
	if !ok {
		return agentDetectSummary{
			Installed:        string(agentdetect.StatusNotChecked),
			ConfigRecognized: string(agentdetect.StatusNotChecked),
			ExecutionReady:   string(agentdetect.StatusNotChecked),
			CallAllowed:      string(agentdetect.StatusNotChecked),
			NotCheckedReason: "本进程尚未执行过本机发现（发现走「服务 → 能力配置 → 发现本机 agent」，未发现过=not_checked 如实呈现）",
		}
	}
	rep := ent.rep
	out := agentDetectSummary{
		Installed:        string(rep.Installed.Status),
		ConfigRecognized: string(rep.ConfigRecognized.Status),
		ExecutionReady:   string(rep.ExecutionReady.Status),
		CallAllowed:      string(rep.CallAllowed.Status),
	}
	// 检查时刻取四层最新非零值（UTC 序列化纪律）；全零（理论不可达：进缓存必跑过
	// 至少一层）不落字段。
	var latest time.Time
	for _, lc := range []agentdetect.LayerCheck{rep.Installed, rep.ConfigRecognized, rep.ExecutionReady, rep.CallAllowed} {
		if lc.CheckedAt.After(latest) {
			latest = lc.CheckedAt
		}
	}
	if !latest.IsZero() {
		at := latest
		out.CheckedAt = &at
	}
	var miss []string
	for _, tc := range []struct {
		k  string
		lc agentdetect.LayerCheck
	}{
		{"installed", rep.Installed}, {"config_recognized", rep.ConfigRecognized},
		{"execution_ready", rep.ExecutionReady}, {"call_allowed", rep.CallAllowed},
	} {
		if tc.lc.Status == agentdetect.StatusNotChecked && tc.lc.Reason != "" {
			miss = append(miss, tc.k+": "+tc.lc.Reason)
		}
	}
	if len(miss) > 0 {
		out.NotCheckedReason = strings.Join(miss, "；")
	}
	return out
}
