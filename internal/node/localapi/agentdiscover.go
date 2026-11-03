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
	"os"
	"path/filepath"
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
		s.recordDetect(res.Report, c.Command, c.ConfigHome)
		results = append(results, res)
	}
	note := "发现≠登记：本端点零写盘、不自动开放能力；登记走「登记预览 + 能力配置保存」（既有 PUT /api/v1/local/config 链，无第二写路径）。" +
		"「允许本机调用」随登记建立、本机派单不受 exposed 影响；exposed=false 只关闭 hub 下发新任务的准入，在途任务照常到终态。"
	if len(results) == 0 {
		note = "无候选：零扫描（本端点只探测显式给出的候选，不做磁盘/PATH 扫描）。" + note
	}
	// 检测摘要随卡补上报（W07-03，Loop 7 同通道尽力执行）：发现写入检测缓存=卡
	// 内容变化——带最新摘要的卡即时补报 hub；离线失败静默（注册重连携全量收敛，
	// 同 AgentState.Sync 口径）。
	if s.cfg.CardsReporter != nil && s.cfg.Agents != nil {
		_ = s.cfg.CardsReporter(s.cfg.Agents.Cards())
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
	s.recordDetect(rep, next.Command, next.ConfigHome)
	// 检测摘要随卡补上报（W07-03，Loop 7 同通道尽力执行）：登记预览写入检测缓存
	// =卡内容变化——带最新摘要的卡即时补报 hub；离线失败静默（注册重连携全量收敛，
	// 同 AgentState.Sync 口径）。
	if s.cfg.CardsReporter != nil && s.cfg.Agents != nil {
		_ = s.cfg.CardsReporter(s.cfg.Agents.Cards())
	}
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

// detectCacheEntry 缓存条目：上次检测报告 + 检测输入快照 + 入缓存时刻（UTC）。
// 输入快照供「登记面漂移」比对（W06-04）：登记面（command/config_home）在上次
// 检测后被修改而未重跑发现时，旧检测结论对应旧输入——如实注记，不冒认仍有效。
type detectCacheEntry struct {
	rep        agentdetect.Report
	command    string // 检测时输入的命令绝对路径（空=安装层未检测）
	configHome string // 检测时输入的宿主配置 home（空=配置层未检测）
	at         time.Time
}

// recordDetect 记入投影缓存（键=候选名；空名不记——投影按已登记能力名取）。
// command/configHome 为本次检测输入快照（W06-04 漂移比对基准）。
func (s *Server) recordDetect(rep agentdetect.Report, command, configHome string) {
	if rep.Name == "" {
		return
	}
	s.detectMu.Lock()
	if s.detectCache == nil {
		s.detectCache = map[string]detectCacheEntry{}
	}
	s.detectCache[rep.Name] = detectCacheEntry{rep: rep, command: command, configHome: configHome, at: time.Now().UTC()}
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
	// Layers 四层逐层明细（W06-04 加法）：每层独立的检查来源/检查时刻/未验证或失败
	// 原因——配置识别≠登录成功≠执行就绪≠调用允许，各层原因不得合并成一句绿灯话术。
	// 未发现过（无缓存）缺省。
	Layers *agentDetectLayers `json:"layers,omitempty"`
	// Drift 漂移注记（W06-04 加法）：缓存检测结论与当前实况的便宜复核（纯 Lstat，
	// 零子进程零模型调用）——登记面输入变化或已记录路径事实变化时如实注记「需重新
	// 检测」；只注记不回写四层状态（旧绿灯不被静默沿用，也不被本检查改写）。未发现
	// 过缺省。
	Drift *agentDetectDrift `json:"drift,omitempty"`
}

// agentDetectLayerView 单层明细（检查来源/时刻/原因——任务书四要素在投影面的逐层落位）。
type agentDetectLayerView struct {
	Status    string     `json:"status"`               // pass|fail|not_checked
	Source    string     `json:"source,omitempty"`     // 检查来源（可核查事实）
	CheckedAt *time.Time `json:"checked_at,omitempty"` // UTC；未检测为零值不落
	Reason    string     `json:"reason,omitempty"`     // 未验证原因/失败原因/口径注记
}

// agentDetectLayers 四层逐层明细容器。
type agentDetectLayers struct {
	Installed        agentDetectLayerView `json:"installed"`
	ConfigRecognized agentDetectLayerView `json:"config_recognized"`
	ExecutionReady   agentDetectLayerView `json:"execution_ready"`
	CallAllowed      agentDetectLayerView `json:"call_allowed"`
}

// agentDetectDrift 漂移注记：drifted=true 时 notes 逐条人话说明哪些基础事实与上次
// 检测不一致；页面据此呈现「需重新检测」，绝不静默沿用旧绿灯。
type agentDetectDrift struct {
	Drifted bool     `json:"drifted"`
	Notes   []string `json:"notes,omitempty"`
}

// legacyPrimeNameKey 旧名字约定键（2026-10-23 伦哥定案的兼容回退：零声明时能力名
// 含该键按智行管家识别；2026-10-28 客户端声明制后降为向后兼容回退，声明位优先）。
// 与 hub/node web 端 isPrime 判定的字面量同源口径，如改须三处同改（hub web isPrimeDir、
// node web tasks 页 isPrime、本常量）。
const legacyPrimeNameKey = "zcode-dev"

// layerViewOf 单层 LayerCheck → 投影明细（时间一律 UTC 序列化纪律）。
func layerViewOf(lc agentdetect.LayerCheck) agentDetectLayerView {
	v := agentDetectLayerView{Status: string(lc.Status), Source: lc.Source, Reason: lc.Reason}
	if !lc.CheckedAt.IsZero() {
		at := lc.CheckedAt
		v.CheckedAt = &at
	}
	return v
}

// detectSummaryFor 单能力检测摘要：未发现过 = 四值 not_checked + 如实原因
// （绝不冒认已检测）；已发现 = 四层状态 + 最新检查时刻 + not_checked 层原因聚合
// + 逐层明细与漂移注记（W06-04）。漂移复核对每次目录读取实时执行（纯 Lstat 便宜
// 检查，不缓存——注记永远反映此刻实况）。
func (s *Server) detectSummaryFor(p protocol.ExecProfile) agentDetectSummary {
	s.detectMu.Lock()
	ent, ok := s.detectCache[p.Name]
	s.detectMu.Unlock()
	if !ok {
		return agentDetectSummary{
			Installed:        string(agentdetect.StatusNotChecked),
			ConfigRecognized: string(agentdetect.StatusNotChecked),
			ExecutionReady:   string(agentdetect.StatusNotChecked),
			CallAllowed:      string(agentdetect.StatusNotChecked),
			NotCheckedReason: "本进程尚未执行过本机发现（发现走「资源 → 能力配置 → 发现本机 agent」，原「服务」，W08-03 导航词迁移；未发现过=not_checked 如实呈现）",
		}
	}
	rep := ent.rep
	out := agentDetectSummary{
		Installed:        string(rep.Installed.Status),
		ConfigRecognized: string(rep.ConfigRecognized.Status),
		ExecutionReady:   string(rep.ExecutionReady.Status),
		CallAllowed:      string(rep.CallAllowed.Status),
		Layers: &agentDetectLayers{
			Installed:        layerViewOf(rep.Installed),
			ConfigRecognized: layerViewOf(rep.ConfigRecognized),
			ExecutionReady:   layerViewOf(rep.ExecutionReady),
			CallAllowed:      layerViewOf(rep.CallAllowed),
		},
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
	out.Drift = detectDriftFor(p, ent)
	return out
}

// detectDriftFor 漂移复核（W06-04）：对比「上次检测输入快照/已记录路径事实」与
// 当前实况。三类漂移，基准按层独立（必修2）：
//  1. 登记面输入漂移——installed 层基准=ent.command、config 层基准=ent.configHome，
//     各自非空且与现配置不一致时注记「对应旧输入」；空→非空且该层当时带实际结论
//     的异常形态注记「新增输入无检测基准」（正常形态该层即 not_checked，如实呈现
//     不额外注记）；
//  2. 路径事实漂移——installed 层路径复核条件=ent.command 非空且等于现配置；
//     config 层=ent.configHome 同理（上次未检测的层无基准，缺省不是漂移）；
//  3. 登录标记漂移（同 config 层基准）——发现→不存在、链/末段链接状态变化；
//     内容变化不覆盖（检测本来不读标记内容，复核同样不读——如实口径）；
//     LoginMarkerCheck 只记录存在/链接两位，无类型位可比对。
//
// 符号链接口径与检测逐位对齐（必修1）：installed 命令路径的 PathRef.Symlink 是
// 纯末段位；config/标记路径是复合位（末段链接 || 目录链链接）——复核经
// agentdetect.CheckPathLinkState（与检测器布局链校验同一实现）折算当前复合位再
// 比对，消除「本就是链上链接未变化」误报与「检测后目录被换成链接」漏报。
//
// 纯 Lstat（不跟随符号链接、不读内容、零子进程零模型调用）；只产出注记，绝不
// 回写四层状态（重新检测须主人显式跑「发现本机 agent」，检测是主人授权行为）。
func detectDriftFor(p protocol.ExecProfile, ent detectCacheEntry) *agentDetectDrift {
	var notes []string
	seen := map[string]bool{} // 同一路径只报一条（zcode 登录标记与配置同文件，去重噪音）
	rep := ent.rep
	// ① 登记面输入漂移（按层独立基准）。「新增输入无检测基准」只在异常形态
	//（该层当时带实际结论 pass/fail）时防御性注记——正常形态空输入层即
	// not_checked（含零值层），如实呈现不额外注记。
	switch {
	case ent.command == "" && p.Command != "" &&
		(rep.Installed.Status == agentdetect.StatusPass || rep.Installed.Status == agentdetect.StatusFail):
		notes = append(notes, "登记命令为新增输入、上次检测无此层基准：检测结果不含该层结论，需重新检测")
	case ent.command != "" && ent.command != p.Command:
		notes = append(notes, "登记命令已变化（上次检测 "+ent.command+" → 现配置 "+p.Command+"）：检测结果对应旧输入，需重新检测")
	}
	switch {
	case ent.configHome == "" && p.ConfigHome != "" &&
		(rep.ConfigRecognized.Status == agentdetect.StatusPass || rep.ConfigRecognized.Status == agentdetect.StatusFail):
		notes = append(notes, "登记 config_home 为新增输入、上次检测无此层基准：检测结果不含该层结论，需重新检测")
	case ent.configHome != "" && ent.configHome != p.ConfigHome:
		notes = append(notes, "登记 config_home 已变化（上次检测 "+ent.configHome+" → 现配置 "+displayOrEmpty(p.ConfigHome)+"）：检测结果对应旧输入，需重新检测")
	}
	// ② 路径事实漂移：按层独立基准（上次检测过该层且输入未变才复核；旧输入的
	// 路径与现配置无关，比较会产出误导性注记）。
	if ent.command != "" && ent.command == p.Command {
		for _, ref := range rep.Installed.Paths {
			if n := leafPathDriftNote(ref, seen); n != "" {
				notes = append(notes, n)
			}
		}
	}
	if ent.configHome != "" && ent.configHome == p.ConfigHome {
		home := filepath.Clean(ent.configHome) // 与检测器 Clean 入口同表示（识别时已 Clean）
		for _, ref := range rep.ConfigRecognized.Paths {
			if n := chainPathDriftNote(ref, home, seen); n != "" {
				notes = append(notes, n)
			}
		}
		// ③ 登录标记复核（建议1）：基准同 config 层；内容变化不覆盖（检测不读
		// 标记内容，复核同样不读）；无类型位记录，只比存在与复合链接位（该位仅
		// codex 记录——zcode 标记与配置同文件，其链接状态已由配置路径的复合位
		// 复核覆盖，路径同物故先按路径去重，不比未记录的位）。
		for _, ref := range rep.ConfigRecognized.Paths {
			seen[ref.Path] = true // 标记与配置同路径（zcode）→ 配置复核已覆盖，不重复报
		}
		if mp := rep.LoginMarker.Path; mp != "" && !seen[mp] {
			seen[mp] = true
			segs := relSegments(home, mp)
			if segs != nil {
				st, _ := agentdetect.CheckPathLinkState(home, segs)
				switch {
				case rep.LoginMarker.Found && !st.LeafExists:
					notes = append(notes, "登录态标记文件已不存在（上次检测发现）："+mp+"——执行就绪结论可能已失效，需重新检测")
				case !rep.LoginMarker.Found && st.LeafExists && !st.ChainBroken:
					notes = append(notes, "登录态标记现已出现（上次检测未发现）："+mp+"——执行就绪结论可能已变化，需重新检测")
				case st.LeafExists && rep.LoginMarker.Symlink != (st.LeafSymlink || st.ChainSymlink):
					notes = append(notes, "登录态标记路径符号链接状态已变化（目录链或末段）："+mp+"——需重新检测")
				}
			}
		}
	}
	if len(notes) == 0 {
		return &agentDetectDrift{Drifted: false}
	}
	return &agentDetectDrift{Drifted: true, Notes: notes}
}

// ---------- 能力卡检测摘要投影（W07-03，hub P05 目录数据源） ----------

// CardDetectSummaries 全表检测摘要投影（能力卡盖章数据源；AgentState.Cards 经
// SetDetectProvider 注入本方法调用——单卡片装配点盖章，register/card_update 同源）。
// 未发现过的能力不出现在 map（卡上 Detect 保持 nil → hub 如实「未上报」，不冒认）。
func (s *Server) CardDetectSummaries() map[string]protocol.CardDetectSummary {
	out := map[string]protocol.CardDetectSummary{}
	for _, p := range s.previewBaseline() {
		if cs, ok := s.cardDetectFor(p); ok {
			out[p.Name] = cs
		}
	}
	return out
}

// cardDetectFor 单能力卡检测摘要（detectCache 只读；never 写缓存）。脱敏边界：
// 逐层 reason 原样透传仅当不含本机路径——config 识别层 fail 的原因是 cfgErr 文案
//（内含引用路径），换为固定脱敏人话；drift 明细注记含本机路径，一律折叠为
// 「drifted + 固定指路」——差异明细留在该机器 node 控制台（隐私投影同界：凭据与
// HOME 内容永不出本机，路径不出本机）。
func (s *Server) cardDetectFor(p protocol.ExecProfile) (protocol.CardDetectSummary, bool) {
	s.detectMu.Lock()
	ent, ok := s.detectCache[p.Name]
	s.detectMu.Unlock()
	if !ok {
		return protocol.CardDetectSummary{}, false
	}
	rep := ent.rep
	cs := protocol.CardDetectSummary{
		Installed:        string(rep.Installed.Status),
		ConfigRecognized: string(rep.ConfigRecognized.Status),
		ExecutionReady:   string(rep.ExecutionReady.Status),
		CallAllowed:      string(rep.CallAllowed.Status),
	}
	var latest time.Time
	for _, lc := range []agentdetect.LayerCheck{rep.Installed, rep.ConfigRecognized, rep.ExecutionReady, rep.CallAllowed} {
		if lc.CheckedAt.After(latest) {
			latest = lc.CheckedAt
		}
	}
	if !latest.IsZero() {
		at := latest.UTC()
		cs.CheckedAt = &at
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
		cs.NotCheckedReason = strings.Join(miss, "；")
	}
	cs.Layers = &protocol.CardDetectLayers{
		Installed:        cardSanitizedInstalledLayer(rep.Installed),
		ConfigRecognized: cardSanitizedConfigLayer(rep.ConfigRecognized),
		ExecutionReady:   cardSanitizedReadyLayer(rep.ExecutionReady),
		CallAllowed:      cardLayerOf(rep.CallAllowed),
	}
	if dr := detectDriftFor(p, ent); dr != nil {
		note := ""
		if dr.Drifted {
			note = "登记面输入或路径基础事实与上次检测不一致：四层状态仍为上次检测值，需在该机器重新检测（明细见该机 node 控制台「能力配置 → 发现本机 agent」）"
		}
		cs.Drift = &protocol.CardDetectDrift{Drifted: dr.Drifted, Note: note}
	}
	return cs, true
}

// cardLayerOf 单层投影（UTC 时刻纪律；reason 原样——call_allowed 的 reason 无本机
// 绝对路径：安全性来自本端 localapi 固定投影源（callProjectionFor/localCallState
// 固定文案，agentdetect 包内 callAllowed 层不产文案、只承载投影），非「包内任意
// reason 都安全」的词表断言——新增 reason 文案须复核此界（codex 审查建议2 注记
// 更正）。installed 层 reason 同为 agentdetect 固定人话；config/execution_ready
// 两层 fail 已由 cardSanitized*Layer 定点脱敏）。
func cardLayerOf(lc agentdetect.LayerCheck) protocol.CardDetectLayer {
	v := protocol.CardDetectLayer{Status: string(lc.Status), Reason: lc.Reason}
	if !lc.CheckedAt.IsZero() {
		at := lc.CheckedAt.UTC()
		v.CheckedAt = &at
	}
	return v
}

// cardSanitizedInstalledLayer 安装层投影：fail 态 reason 含探测启动失败原文
//（os/exec StartErr 典型内含命令绝对路径——fork/exec /Users/…: no such file），
// 换固定脱敏人话（版本输出走 VersionRaw 字段不进 reason，非零/超限 reason 为
// 固定格式无路径；pass/not_checked 原样透传）。集成者门禁补漏：cardLayerOf 原
// 注释「installed 词表无路径」不成立。
func cardSanitizedInstalledLayer(lc agentdetect.LayerCheck) protocol.CardDetectLayer {
	v := cardLayerOf(lc)
	if lc.Status == agentdetect.StatusFail {
		v.Reason = "命令探测未通过（失败细节属本机真相：详见该机器 node 控制台检测面）"
	}
	return v
}

// cardSanitizedConfigLayer 配置识别层投影：fail 态 reason（cfgErr 文案）内含引用
// 路径，换固定脱敏人话——路径事实属本机真相，中心面只表事实类别与指路
//（not_checked/pass 的 reason 词表无路径，原样透传）。
func cardSanitizedConfigLayer(lc agentdetect.LayerCheck) protocol.CardDetectLayer {
	v := cardLayerOf(lc)
	if lc.Status == agentdetect.StatusFail {
		v.Reason = "宿主配置结构未识别（引用路径与差异明细属本机真相：详见该机器 node 控制台检测面）"
	}
	return v
}

// cardSanitizedReadyLayer 执行就绪层投影：fail 态 reason 聚合安装/配置/登录标记
// 三源失败事实——其中配置链与登录标记链的失败文案内含本机绝对路径
//（layoutChainIssue 词表），一律换固定脱敏人话（pass/not_checked 的 reason 词表
// 无路径，原样透传；not_checked 聚合只收前层 not_checked 原因，同为无路径词表）。
func cardSanitizedReadyLayer(lc agentdetect.LayerCheck) protocol.CardDetectLayer {
	v := cardLayerOf(lc)
	if lc.Status == agentdetect.StatusFail {
		v.Reason = "执行就绪未通过（安装证据/配置结构/登录态标记至少一层未过；含本机路径的逐层明细留该机器 node 控制台检测面）"
	}
	return v
}

// leafPathDriftNote 命令路径（纯末段口径）事实漂移注记：PathRef 与当前 Lstat
// 逐位比对（存在性/末段符号链接/普通文件）；一致返回空串。
func leafPathDriftNote(ref agentdetect.PathRef, seen map[string]bool) string {
	if seen[ref.Path] {
		return ""
	}
	now := lstatRef(ref.Path)
	var n string
	switch {
	case ref.Exists && !now.Exists:
		n = "路径已不存在（上次检测时存在）：" + ref.Path + "——需重新检测"
	case !ref.Exists && now.Exists:
		n = "路径现已存在（上次检测时不存在）：" + ref.Path + "——需重新检测"
	case ref.Exists && ref.Symlink != now.Symlink:
		n = "路径符号链接状态已变化：" + ref.Path + "——需重新检测"
	case ref.Exists && ref.Regular != now.Regular:
		n = "路径文件类型已变化（普通文件/非普通文件）：" + ref.Path + "——需重新检测"
	}
	if n != "" {
		seen[ref.Path] = true
	}
	return n
}

// chainPathDriftNote 配置层路径（复合链接口径）事实漂移注记：存在性/类型按末段
// Lstat 比对；链接位经 agentdetect.CheckPathLinkState（与检测器布局链校验同一
// 实现）折算当前复合位（末段||目录链）再比对——必修1 口径对齐。
func chainPathDriftNote(ref agentdetect.PathRef, home string, seen map[string]bool) string {
	if seen[ref.Path] {
		return ""
	}
	now := lstatRef(ref.Path)
	var n string
	switch {
	case ref.Exists != now.Exists:
		n = "路径已不存在（上次检测时存在）：" + ref.Path + "——需重新检测"
		if !ref.Exists && now.Exists {
			n = "路径现已存在（上次检测时不存在）：" + ref.Path + "——需重新检测"
		}
	case ref.Exists && ref.Regular != now.Regular:
		n = "路径文件类型已变化（普通文件/非普通文件）：" + ref.Path + "——需重新检测"
	case ref.Exists:
		if segs := relSegments(home, ref.Path); segs != nil {
			st, _ := agentdetect.CheckPathLinkState(home, segs)
			if ref.Symlink != (st.LeafSymlink || st.ChainSymlink) {
				n = "配置路径符号链接状态已变化（目录链或末段）：" + ref.Path + "——需重新检测"
			}
		}
	}
	if n != "" {
		seen[ref.Path] = true
	}
	return n
}

// relSegments root→path 的相对段分解（检测时路径由 joinLayout(home, segs) 构造，
// 复核按同一切分还原；无法还原（异常/逃逸形态）返回 nil——调用方只做末段事实
// 比对，不做链路折算）。
func relSegments(home, path string) []string {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	return strings.Split(rel, string(filepath.Separator))
}

// lstatRef 当前时刻的路径事实（与 agentdetect 同口径：Lstat 不跟随符号链接，
// 只回元数据；错误一律折算为不存在——Lstat 失败即无法确认存在）。
func lstatRef(path string) (f agentdetect.PathRef) {
	fi, err := os.Lstat(path)
	if err != nil {
		return agentdetect.PathRef{Path: path}
	}
	return agentdetect.PathRef{Path: path, Exists: true, Symlink: fi.Mode()&os.ModeSymlink != 0, Regular: fi.Mode().IsRegular()}
}

// displayOrEmpty 空串显示兜底（漂移注记里「空 config_home」用可读形态）。
func displayOrEmpty(s string) string {
	if s == "" {
		return "（空）"
	}
	return s
}

// agentOrchIdentity 本机统筹声明身份投影（W06-04）：声明实况 + 旧名字约定回退
// 来源——「唯一管家已生效」不得在多声明/零声明回退态被冒认，来源必须可见。
type agentOrchIdentity struct {
	// Declared 有效声明 orchestrator 的能力名（effective 视图：YAML 基线 ∪ 网页
	// 覆盖层，配置表序）。
	Declared []string `json:"declared"`
	// Conflict 本机声明 >1：多声明并存，无唯一生效声明（网页端据此把「已声明」
	// 绿灯改呈现冲突警示）。
	Conflict bool `json:"conflict"`
	// NameFallback 未声明但名字含 zcode-dev 的能力名（旧名字约定兼容识别——派单
	// 侧会把它们识别为管家；来源是名字约定而非主人声明，必须如实标注）。
	NameFallback []string `json:"name_fallback,omitempty"`
	// Note 人话说明（来源呈现）。
	Note string `json:"note"`
}

// workspaceConflictView 同工作区多声明冲突条目（W06-04）：同一 workdir 被 ≥2 个
// 声明共用。数据事实如实列出（并发任务同区执行可能互相影响），是否拆分由主人裁量。
type workspaceConflictView struct {
	Workdir string   `json:"workdir"`
	Agents  []string `json:"agents"` // 声明序
}

// orchIdentityFor 本机声明身份投影：effective 视图收集声明位与旧名字约定命中。
func orchIdentityFor(profiles []protocol.ExecProfile) agentOrchIdentity {
	out := agentOrchIdentity{Declared: []string{}}
	var fallback []string
	for _, p := range profiles {
		if p.Orchestrator {
			out.Declared = append(out.Declared, p.Name)
			continue
		}
		if strings.Contains(p.Name, legacyPrimeNameKey) {
			fallback = append(fallback, p.Name)
		}
	}
	switch {
	case len(out.Declared) > 1:
		out.Conflict = true
		out.Note = "本机多份声明（" + strings.Join(out.Declared, "、") + "）并存：无唯一生效声明，本端不作「唯一管家已生效」呈现；hub 汇聚全网声明——冲突时 hub 告警并回退名字识别（能力名含 " + legacyPrimeNameKey + "），本机派单则声明优先、旧名回退仅零声明态生效；请到声明开关取消多余声明"
	case len(out.Declared) == 1:
		out.Note = "本机恰一声明（" + out.Declared[0] + "）；全网是否恰一以 hub 目录为准（跨机冲突由 hub 告警）"
		if len(fallback) > 0 {
			out.Note += "。另有旧名字约定候选（" + strings.Join(fallback, "、") + "，能力名含 " + legacyPrimeNameKey + "）：零声明态才参与回退识别，声明存在时不生效"
		}
	default:
		if len(fallback) > 0 {
			out.Note = "本机零声明：派单侧按旧名字约定识别（能力名含 " + legacyPrimeNameKey + "：" + strings.Join(fallback, "、") + "）——兼容回退，非主人声明；钦定管家即随能力表上报正式声明"
		} else {
			out.Note = "本机零声明且无旧名字约定命中：派单侧无智行管家分组"
		}
	}
	out.NameFallback = fallback
	return out
}

// workspaceConflictsFor 同工作区多声明盘点（声明序稳定；恰一/零共用 = 空表）。
// 比较口径（建议3）：比较键=filepath.Clean(workdir)——归一尾部分隔符与「.」段
// （/work 与 /work/ 合并）；大小写不折算、符号链接不解析——不同写法指向同一真实
// 目录的极端形态不合并（跨文件系统大小写语义不一，保持原样）；条目展示首见原始
// 写法，口径在页面如实披露。
func workspaceConflictsFor(profiles []protocol.ExecProfile) []workspaceConflictView {
	idx := map[string]int{}
	var out []workspaceConflictView
	for _, p := range profiles {
		key := filepath.Clean(p.Workdir)
		if i, ok := idx[key]; ok {
			out[i].Agents = append(out[i].Agents, p.Name)
			continue
		}
		idx[key] = len(out)
		out = append(out, workspaceConflictView{Workdir: p.Workdir, Agents: []string{p.Name}})
	}
	conf := out[:0]
	for _, c := range out {
		if len(c.Agents) > 1 {
			conf = append(conf, c)
		}
	}
	return conf
}
