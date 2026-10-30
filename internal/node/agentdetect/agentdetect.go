// 四层只读检测模型核心（v3 W06-01）：类型、Scanner 编排与派生规则。
// 本文件只做编排与数据结构；子进程纪律在 probe.go，宿主布局识别在 hostlayout.go。
package agentdetect

import (
	"context"
	"strings"
	"time"
)

// Host 受支持的 agent 宿主标识；其他值 = 未知宿主（按通用手动处理，不冒认已适配）。
type Host string

const (
	HostCodex Host = "codex"
	HostZcode Host = "zcode"
)

// LayerStatus 层状态三值：pass（检查通过）/ fail（检查未过）/ not_checked
// （未检测——输入缺失或本包不判定，如实呈现，不推断）。
type LayerStatus string

const (
	StatusPass       LayerStatus = "pass"
	StatusFail       LayerStatus = "fail"
	StatusNotChecked LayerStatus = "not_checked"
)

// 检查来源（Source 字段取值；来源必须是可核查的事实，不得冒认）。
const (
	SourceCallerCommand = "caller_command" // 安装层：调用方显式给出的绝对路径命令
	SourceDerivedReady  = "derived(installed+config_recognized+login_marker)"
	SourceUnknownHost   = "unknown_host_generic_manual"
	SourceDefaultCall   = "caller_projection" // 调用方未自报来源时的默认
)

const (
	// DefaultVersionTimeout --version 子进程默认超时（任务书：默认 5s，可配）。
	DefaultVersionTimeout = 5 * time.Second
	// DefaultOutputLimit 版本输出捕获上限（任务书：64KB，超限截断并注记）。
	DefaultOutputLimit = int64(64 << 10)
	// configReadLimit 配置文件读取字节上限（识别只需判结构，防异常大文件拖爆内存）。
	configReadLimit = int64(1 << 20)
	// execReadyNote ExecutionReady 通过时的强制注记（任务书原文口径）：不合并成一盏绿灯。
	execReadyNote = "执行前置检查通过（模型未验证）：未运行真实模型探针，不声称账号/额度可用"
	// symlinkNote 路径为符号链接时的如实注记（不跟随、不展开）。
	symlinkNote = "路径为符号链接（Lstat 如实注记，未跟随展开）"
)

// Options 检测器选项（零值可用：超时/上限取默认；Now 供测试注入时间源）。
type Options struct {
	// VersionTimeout --version 子进程超时；<=0 取 DefaultVersionTimeout。
	VersionTimeout time.Duration
	// OutputLimit 版本输出捕获上限（字节）；<=0 取 DefaultOutputLimit。
	OutputLimit int64
	// Now 时间源；nil 取 time.Now。落账一律 .UTC()（时间序列化纪律）。
	Now func() time.Time
}

// Scanner 只读检测器（无状态，可并发复用）。
type Scanner struct {
	versionTimeout time.Duration
	outputLimit    int64
	now            func() time.Time
}

// New 构造 Scanner：零值/非法选项回退默认（显式归一，不留半配置态）。
func New(opts Options) *Scanner {
	s := &Scanner{versionTimeout: DefaultVersionTimeout, outputLimit: DefaultOutputLimit, now: time.Now}
	if opts.VersionTimeout > 0 {
		s.versionTimeout = opts.VersionTimeout
	}
	if opts.OutputLimit > 0 {
		s.outputLimit = opts.OutputLimit
	}
	if opts.Now != nil {
		s.now = opts.Now
	}
	return s
}

// PathRef 路径引用（纯元数据：只回引用与存在性注记，永不回内容——HOME 配置
// 只回「是否可用 + 引用路径」）。
type PathRef struct {
	Path    string `json:"path"`              // 引用路径（绝对）
	Kind    string `json:"kind"`              // command | config
	Exists  bool   `json:"exists"`            // Lstat 存在判定（不跟随链接）
	Regular bool   `json:"regular"`           // 是否普通文件（目录/FIFO/设备如实为 false）
	Symlink bool   `json:"symlink,omitempty"` // 符号链接如实注记（不展开）
}

// LayerCheck 单层检查记录：检查来源、检查时间（UTC）、能力版本信息、未验证
// 原因——四要素齐备，缺一层缺失即如实记 not_checked + 原因。
type LayerCheck struct {
	Status    LayerStatus `json:"status"`
	Source    string      `json:"source"`               // 检查来源（可核查事实）
	CheckedAt time.Time   `json:"checked_at,omitempty"` // UTC；未检测为零值（没跑过就不记时间）
	// VersionRaw 能力版本信息：--version stdout+stderr 合流原样留痕（V0 不解析，
	// 截断上限内逐字节保留）；非命令探测层为空。
	VersionRaw string `json:"version_raw,omitempty"`
	// VersionTruncated 版本输出超上限被截断（截断即 Installed 不采信）。
	VersionTruncated bool `json:"version_truncated,omitempty"`
	// Reason 未验证原因/失败原因；pass 且无注记时为空。
	Reason string    `json:"reason,omitempty"`
	Paths  []PathRef `json:"paths,omitempty"` // 本层涉猎路径引用（纯元数据）
}

// LoginMarkerCheck 登录态标记核查（ExecutionReady 的第三道输入）：只回标记
// 是否发现与引用路径，标记内容与其有效性永不读取、永不返回。
type LoginMarkerCheck struct {
	Checked   bool      `json:"checked"`           // 是否执行过标记核查（未知宿主/配置未识别 = false）
	Found     bool      `json:"found"`             // 标记线索是否发现（发现 ≠ 登录成功）
	Path      string    `json:"path,omitempty"`    // 标记引用路径（存在性注记，无内容）
	Symlink   bool      `json:"symlink,omitempty"` // 标记路径为符号链接（如实注记）
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Note      string    `json:"note,omitempty"` // 线索说明（含未发现原因）
}

// CallProjection 调用方传入的授权投影：CallAllowed 层唯一事实来源，本包不判定、
// 只承载（「允许本机调用」与 exposed 远端开放的差异由 W06-02 在调用方定义）。
type CallProjection struct {
	Allowed bool   `json:"allowed"`
	Source  string `json:"source,omitempty"` // 授权来源自报（如 "config_allowed_local"）；空取默认
	Reason  string `json:"reason,omitempty"` // 授权依据说明；空取默认透传注记
}

// Request 单次检测请求。Command / ConfigHome 均为可选输入：缺失对应层如实
// not_checked；两者都不提供即零扫描（四层全未检测）。Call 为 nil 时 CallAllowed
// 层 not_checked。
type Request struct {
	Host       Host            `json:"host"`
	Name       string          `json:"name,omitempty"`        // 展示用别名（登记名），仅透传
	Command    string          `json:"command,omitempty"`     // 调用方显式给出的命令绝对路径；空 = 跳过安装层
	ConfigHome string          `json:"config_home,omitempty"` // 宿主配置 home 绝对路径；空 = 跳过配置层
	Call       *CallProjection `json:"call,omitempty"`
}

// Report 四层检测结果（纯数据结构，可直接序列化给 W06-02 投影）。
type Report struct {
	Host             Host             `json:"host"`
	Name             string           `json:"name,omitempty"`
	Installed        LayerCheck       `json:"installed"`
	ConfigRecognized LayerCheck       `json:"config_recognized"`
	ExecutionReady   LayerCheck       `json:"execution_ready"`
	CallAllowed      LayerCheck       `json:"call_allowed"`
	LoginMarker      LoginMarkerCheck `json:"login_marker"`
}

// Detect 执行一次只读检测。各输入层独立探测（安装层不依赖配置层、反之亦然），
// 派生层（ExecutionReady）严格由下层层态推导——未运行不推断下一层。
func (s *Scanner) Detect(ctx context.Context, req Request) Report {
	now := s.now().UTC()
	rep := Report{Host: req.Host, Name: req.Name}
	rep.Installed = s.detectInstalled(ctx, req, now)
	rep.ConfigRecognized, rep.LoginMarker = s.detectConfig(req, now)
	rep.ExecutionReady = deriveExecutionReady(rep.Installed, rep.ConfigRecognized, rep.LoginMarker, now)
	rep.CallAllowed = projectCall(req.Call, now)
	return rep
}

// detectInstalled 安装层：调用方显式命令存在 + --version 退出码 0（probe.go）。
// 未提供命令时不记时间（没跑过就不记时间，如实零值）。
func (s *Scanner) detectInstalled(ctx context.Context, req Request, now time.Time) LayerCheck {
	lc := LayerCheck{Source: SourceCallerCommand}
	if req.Command == "" {
		lc.Status = StatusNotChecked
		lc.Reason = "未提供候选命令（安装层未检测：只探测调用方显式给出的命令，不做磁盘/PATH 扫描）"
		return lc
	}
	lc.CheckedAt = now
	ref := PathRef{Path: req.Command, Kind: "command"}
	lc.Paths = []PathRef{ref}
	if !isAbsPath(req.Command) {
		lc.Status = StatusFail
		lc.Reason = "命令非绝对路径，拒绝探测：只接受调用方显式给出的绝对路径命令（裸名会触发 PATH 搜索，不构成安装证据）"
		return lc
	}
	facts := lstatPath(req.Command)
	lc.Paths[0] = facts.ref(req.Command, "command")
	if !facts.Exists {
		lc.Status = StatusFail
		lc.Reason = "命令路径不存在（Lstat 未命中，不跟随符号链接判定）"
		return lc
	}
	if facts.Symlink {
		// 符号链接：如实注记后照常探测——exec 由内核按调用方给出的路径原样
		// 解析，本包不展开、不声称命令位于其他位置（路径探测纪律）。
		lc.Reason = symlinkNote
	} else if !facts.Regular {
		lc.Status = StatusFail
		lc.Reason = "命令路径非普通文件（目录/FIFO/设备等），不作为可执行命令探测"
		return lc
	}
	pr := probeVersion(ctx, req.Command, s.versionTimeout, s.outputLimit)
	lc.VersionRaw = pr.Raw
	lc.VersionTruncated = pr.Truncated
	switch {
	case pr.Timeout:
		lc.Status = StatusFail
		lc.appendReason("版本探测超时：超过上限 " + s.versionTimeout.String() + " 已终止进程并 Wait 收尸")
	case pr.Canceled:
		lc.Status = StatusFail
		lc.appendReason("版本探测被取消")
	case pr.StartErr != nil:
		lc.Status = StatusFail
		lc.appendReason("版本探测启动失败: " + pr.StartErr.Error())
	case pr.NonZero:
		lc.Status = StatusFail
		lc.appendReason(pr.nonZeroReason())
	case pr.Truncated:
		lc.Status = StatusFail
		lc.appendReason(pr.overLimitReason(s.outputLimit))
	case pr.WaitErr != nil:
		lc.Status = StatusFail
		lc.appendReason("版本探测失败: " + pr.WaitErr.Error())
	default:
		lc.Status = StatusPass // 退出码 0 = 安装证据；版本信息见 VersionRaw 原样留痕
		// V0 不解析版本（doc.go 口径）：未知/不支持版本形态只留痕不判定，安装
		// 判定不受版本值影响；结果如实注记未做版本兼容性判断（支持版本表归
		// W06-02 交付）。
		lc.appendReason("版本输出仅原样留痕：未解析版本、未做版本兼容性判断（版本值不影响安装判定）")
	}
	return lc
}

// detectConfig 配置识别层 + 登录标记核查（hostlayout.go）。
func (s *Scanner) detectConfig(req Request, now time.Time) (LayerCheck, LoginMarkerCheck) {
	if req.ConfigHome == "" {
		return LayerCheck{
			Status: StatusNotChecked,
			Source: knownLayoutSource(req.Host),
			Reason: "未提供 config_home（宿主配置识别未检测；HOME 配置只按引用路径核查，无 home 即不猜位置）",
		}, LoginMarkerCheck{}
	}
	return recognizeConfig(req.Host, req.ConfigHome, now)
}

// deriveExecutionReady 执行就绪层：前两层全过 + 登录标记发现才亮，亮也只亮
// 「前置检查通过（模型未验证）」。未运行不推断下一层：任一下层 not_checked
// 本层 not_checked，任一下层 fail 本层 fail。
func deriveExecutionReady(inst, cfg LayerCheck, marker LoginMarkerCheck, now time.Time) LayerCheck {
	lc := LayerCheck{Source: SourceDerivedReady}
	var missing []string
	if inst.Status == StatusNotChecked {
		missing = append(missing, "安装层未检测（"+inst.Reason+"）")
	}
	if cfg.Status == StatusNotChecked {
		missing = append(missing, "配置识别层未检测（"+cfg.Reason+"）")
	}
	if len(missing) > 0 {
		lc.Status = StatusNotChecked
		lc.Reason = strings.Join(missing, "；") + "；前层未运行不得推断执行就绪"
		return lc
	}
	lc.CheckedAt = now
	var failed []string
	if inst.Status == StatusFail {
		failed = append(failed, "安装证据未通过（"+inst.Reason+"）")
	}
	if cfg.Status == StatusFail {
		failed = append(failed, "配置结构未识别（"+cfg.Reason+"）")
	}
	if len(failed) > 0 {
		lc.Status = StatusFail
		lc.Reason = strings.Join(failed, "；") + "；配置可识别不代表可执行，安装证据缺失不得推断就绪"
		return lc
	}
	// 前两层全过：登录标记是第三道门槛（结构可识别 ≠ 登录成功）。
	if !marker.Checked {
		lc.Status = StatusFail
		lc.Reason = "宿主无已知登录态标记可核（未知宿主按通用手动处理）；不推断登录，执行就绪不亮"
		return lc
	}
	if !marker.Found {
		lc.Status = StatusFail
		lc.Reason = "未发现登录态标记：" + marker.Note + "；结构可识别不代表登录成功，绝不声称账号/额度可用"
		return lc
	}
	lc.Status = StatusPass
	lc.Reason = execReadyNote
	return lc
}

// projectCall 调用授权层：纯透传。本包没有也不产生任何授权判定证据。
func projectCall(cp *CallProjection, now time.Time) LayerCheck {
	if cp == nil {
		return LayerCheck{
			Status: StatusNotChecked,
			Source: SourceDefaultCall,
			Reason: "调用方未传入授权投影（本包不判定，只承载）",
		}
	}
	lc := LayerCheck{Source: cp.Source, CheckedAt: now, Reason: cp.Reason}
	if lc.Source == "" {
		lc.Source = SourceDefaultCall
	}
	if lc.Reason == "" {
		lc.Reason = "调用方授权投影透传（本包未判定）"
	}
	if cp.Allowed {
		lc.Status = StatusPass
	} else {
		lc.Status = StatusFail
	}
	return lc
}

// appendReason 追加原因（注记与失败原因共存时换行拼接，保留两条信息）。
func (lc *LayerCheck) appendReason(msg string) {
	if lc.Reason == "" {
		lc.Reason = msg
		return
	}
	lc.Reason = lc.Reason + "；" + msg
}
