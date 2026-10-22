// 能力配置编辑 + 热重载（方案 3.5，G4「配置管理靠命令行」缺口）：
//   - GET  /api/v1/local/config          本机 agents 配置表（脱敏同目录规则：
//     本机主人视角完整 ExecProfile，与 GET /api/v1/local/agents 同口径）+
//     pending_restart / config_path / 保活检测元数据；
//   - PUT  /api/v1/local/config          整表提交：服务端强校验 → yaml.Node
//     round-trip 写盘（保留注释与未知字段）→ 普通字段变更热重载即时生效，
//     结构性变更（增删 agent、command/agent_args/kind 变更）标「待应用」；
//     读-改-写全程持 writeConfigMu 事务锁（安全审查修复 3，与 declare/reload/
//     disconnect 互斥），能力表上报在锁外；
//   - POST /api/v1/local/agents/reload   手编 node.yaml 后的网页一键重载（重读
//     文件，同一校验/分类/热应用链；读-校验-改写-热应用全程持 writeConfigMu
//     事务锁，第五轮收敛 1）；
//   - POST /api/v1/local/restart         「应用并重启」：HTTP 应答先返回，装配方
//     延时优雅退出靠保活拉起；裸前台在应答注明手动重启命令。
//
// 鉴权：读 = local_token/prime_token（authRead）；写盘与重启 = 仅 local_token
//（authLocal，本机主人操作——写 agents.command 即执行权，与 setup 写盘同门）。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ConfigOps 配置编辑装配方（cmd/node 接线；nil = 配置端点 503）。
type ConfigOps struct {
	// ConfigPath node.yaml 路径（读改写原路径，与 setup 写盘同纪律）。
	ConfigPath string
	// ReloadExecutors 热重载回调：以新基线重建 Runners/ArtifactRoots 并换表
	//（cmd/node 闭包：buildExecutors + Service.SwapExecutors，与启动装配同源）。
	ReloadExecutors func(profiles []protocol.ExecProfile) error
	// Restart 「应用并重启」回调：应答先返回后调用（装配方延时优雅退出，
	// 保活拉起；与 SetupOps.OnApplied 同型）。nil = 重启端点 503。
	Restart func()
	// RestartCmd 裸前台手动重启命令（应答注明用）。
	RestartCmd string
	// Supervised 保活检测结果（cmd/node detectSupervisor）：true = 重启自动拉起；
	// false = 裸前台，重启端点应答注明需手动重启。
	Supervised bool
}

// AgentProfile yaml 侧 ExecProfile 镜像（自 cmd/node 迁入共享——配置编辑端点与
// 装配方必须同源解析；protocol 包保持纯协议层不引入 yaml tag）。exposed 为指针：
// 未写默认 true（SPEC 4.9 接入开关默认开）。json tag 供 PUT 整表提交解码。
type AgentProfile struct {
	Name           string   `yaml:"name" json:"name"`
	Kind           string   `yaml:"kind" json:"kind"`
	Command        string   `yaml:"command" json:"command"`
	Workdir        string   `yaml:"workdir" json:"workdir"`
	AgentArgs      []string `yaml:"agent_args" json:"agent_args"`
	TimeoutSec     int      `yaml:"timeout_sec" json:"timeout_sec"`
	AllowArgs      bool     `yaml:"allow_args" json:"allow_args"`
	AllowEnv       []string `yaml:"allow_env" json:"allow_env"`
	ConfigHome     string   `yaml:"config_home" json:"config_home"`
	Description    string   `yaml:"description" json:"description"`
	PermissionMode string   `yaml:"permission_mode" json:"permission_mode"`
	Exposed        *bool    `yaml:"exposed" json:"exposed"`
}

// ToExecProfile 转协议层 ExecProfile（kind 空 = cli、exposed 未写 = true，与
// cmd/node 既有装载语义一致）。
func (p AgentProfile) ToExecProfile() protocol.ExecProfile {
	exposed := true
	if p.Exposed != nil {
		exposed = *p.Exposed
	}
	kind := protocol.AgentKind(p.Kind)
	if kind == "" {
		kind = protocol.AgentKindCLI
	}
	return protocol.ExecProfile{
		Name: p.Name, Kind: kind, Command: p.Command, Workdir: p.Workdir,
		AgentArgs: p.AgentArgs, TimeoutSec: p.TimeoutSec, AllowArgs: p.AllowArgs,
		AllowEnv: p.AllowEnv, ConfigHome: p.ConfigHome, Description: p.Description,
		PermissionMode: p.PermissionMode, Exposed: exposed,
	}
}

// ---------- 服务端强校验（矩阵 #13：网页编辑非法输入一律拒绝） ----------

// agentsMaxCount 单机 agent 数上限（注册帧/目录体积护栏；超限提示精简配置）。
const agentsMaxCount = 128

// agentNameRe agent 名白名单：1-64 位字母/数字/.-/_，首字符字母或数字——name
// 进能力卡、目录与日志，禁空格与控制字符；yaml 编码器兜底转义其余注入面。
var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// envKeyRe 环境变量键名白名单；"*" 全开（SPEC 4.9 allow_env 语义）。
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateAgentProfiles 整表校验：name 唯一且格式、kind 仅 cli、command/workdir
// 绝对路径必填、config_home 绝对路径（可空）、timeout_sec 正数、allow_env 仅
// 白名单键名或 "*"。逐条给人话错误（带 agents[i] 定位）。
func ValidateAgentProfiles(agents []AgentProfile) error {
	if len(agents) > agentsMaxCount {
		return fmt.Errorf("agent 数量 %d 超上限 %d，请精简配置", len(agents), agentsMaxCount)
	}
	seen := make(map[string]bool, len(agents))
	for i, p := range agents {
		where := fmt.Sprintf("agents[%d]", i)
		if p.Name == "" {
			return fmt.Errorf("%s: name 必填", where)
		}
		if !agentNameRe.MatchString(p.Name) {
			return fmt.Errorf("%s: name %q 非法（1-64 位字母/数字/.-_，首字符字母或数字）", where, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("agent %q 重名：name 在本机配置内必须唯一", p.Name)
		}
		seen[p.Name] = true
		kind := p.Kind
		if kind == "" {
			kind = string(protocol.AgentKindCLI)
		}
		if kind != string(protocol.AgentKindCLI) {
			return fmt.Errorf("agent %q: kind 仅支持 %q（V0）", p.Name, protocol.AgentKindCLI)
		}
		if p.Command == "" || p.Workdir == "" {
			return fmt.Errorf("agent %q: command / workdir 均为必填", p.Name)
		}
		if !filepath.IsAbs(p.Command) {
			return fmt.Errorf("agent %q: command 必须为绝对路径（收到 %q）", p.Name, p.Command)
		}
		if !filepath.IsAbs(p.Workdir) {
			return fmt.Errorf("agent %q: workdir 必须为绝对路径（收到 %q）", p.Name, p.Workdir)
		}
		if p.ConfigHome != "" && !filepath.IsAbs(p.ConfigHome) {
			return fmt.Errorf("agent %q: config_home 必须为绝对路径（收到 %q；留空 = data_dir/home）", p.Name, p.ConfigHome)
		}
		if p.TimeoutSec <= 0 {
			return fmt.Errorf("agent %q: timeout_sec 必须为正数（收到 %d）", p.Name, p.TimeoutSec)
		}
		for _, k := range p.AllowEnv {
			if k == "*" {
				continue
			}
			if !envKeyRe.MatchString(k) {
				return fmt.Errorf("agent %q: allow_env 含非法键名 %q（仅环境变量键名格式或 \"*\" 全开）", p.Name, k)
			}
		}
	}
	return nil
}

// classifyProfilesChange 对比运行时基线与提交表（热重载分类，方案 3.5）：
// 结构性（须重启）= 增/删 agent、command/agent_args/kind 变更——执行器身份与
// 命令行变化，换表不足以表达语义；其余（description/timeout_sec/allow_args/
// allow_env/config_home/workdir/permission_mode/exposed）为普通字段，重建
// Runners/ArtifactRoots 即时生效。返回结构性原因列表（空 = 可热应用）。
func classifyProfilesChange(cur, next []protocol.ExecProfile) []string {
	curByName := make(map[string]protocol.ExecProfile, len(cur))
	for _, p := range cur {
		curByName[p.Name] = p
	}
	var structural []string
	nextNames := make(map[string]bool, len(next))
	for _, p := range next {
		nextNames[p.Name] = true
		old, ok := curByName[p.Name]
		if !ok {
			structural = append(structural, "新增 agent "+p.Name)
			continue
		}
		if old.Command != p.Command {
			structural = append(structural, p.Name+": command 变更")
		}
		if old.Kind != p.Kind {
			structural = append(structural, p.Name+": kind 变更")
		}
		if strings.Join(old.AgentArgs, "\x00") != strings.Join(p.AgentArgs, "\x00") {
			structural = append(structural, p.Name+": agent_args 变更")
		}
	}
	for _, p := range cur {
		if !nextNames[p.Name] {
			structural = append(structural, "移除 agent "+p.Name)
		}
	}
	sort.Strings(structural) // 稳定输出（断言与展示都按字典序）
	return structural
}

// ---------- HTTP 端点 ----------

// handleGetConfig GET /api/v1/local/config：配置表 + 待应用状态（脱敏同目录规则：
// 返回完整 ExecProfile——本机主人视角，与 GET /api/v1/local/agents 同口径）。
//
// 口径说明（功能性修复 4，2026-10-22；第六轮修复 1 再修）：非 pending 时返回
// 运行时基线（Agents.Effective()，热重载成功后与盘上一致）。结构性变更「待应用」
// 窗口期，盘上 node.yaml 已是新表而运行时基线仍是旧表——改读盘上 node.yaml 解析
// agents 返回（盘上 = 用户最新提交的真相源），并以 pending=true 标注本口径；
// 读盘/解析失败回退运行时基线。原实现恒回运行时旧表：PUT pending 后刷新页面，
// 前端拿到旧表再保存即把盘上新表覆盖回旧值（丢提交）。
// 前端约定：PUT 应答 pending_restart=true 时仍不得以本端点回包覆盖编辑副本
//（编辑副本才是用户最新意图，见 web/app.js cfgSave）。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	ops := s.cfg.ConfigOps
	if ops == nil {
		writeErr(w, http.StatusServiceUnavailable, "config_disabled", "配置编辑未装配（装配方未接线 ConfigOps）")
		return
	}
	agents := s.effectiveProfiles()
	pending := false
	if s.pendingRestart() {
		if disk, err := s.diskAgentProfiles(ops); err == nil {
			agents = disk
			pending = true
		} // 读盘/解析失败：回退运行时基线（pending_restart 状态字段仍如实为真）
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":         s.cfg.NodeID,
		"config_path":     ops.ConfigPath,
		"pending_restart": s.pendingRestart(),
		"pending":         pending,
		"supervised":      ops.Supervised,
		"restart_cmd":     ops.RestartCmd,
		"agents":          agents,
	})
}

// diskAgentProfiles 读盘上 node.yaml 解析 agents 并转 ExecProfile（GET 在 pending
// 窗口期的真相源：盘上 = 用户最新提交）。文件不存在/读失败/解析失败返回错误，
// 调用方回退运行时口径。写盘走临时文件+rename 原子替换（writeConfigFileLocked），
// 并发读只会看到旧表或新表之一的完整内容，故不持 writeConfigMu。
func (s *Server) diskAgentProfiles(ops *ConfigOps) ([]protocol.ExecProfile, error) {
	raw, err := os.ReadFile(ops.ConfigPath)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Agents []AgentProfile `yaml:"agents"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]protocol.ExecProfile, 0, len(parsed.Agents))
	for _, p := range parsed.Agents {
		out = append(out, p.ToExecProfile())
	}
	return out, nil
}

// configPutRequest PUT /api/v1/local/config 请求体（整表提交）。
type configPutRequest struct {
	Agents []AgentProfile `json:"agents"`
}

// handlePutConfig PUT /api/v1/local/config：校验 → round-trip 写盘 → 分类 →
// 热应用或标「待应用」。写盘成功但热应用未完成时，失败步骤之前的状态保持一致
//（热重载失败 = 基线与运行时同为旧值），应答 pending_restart=true（重启即
// 收敛到新盘上配置）。
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	ops := s.configPreflight(w)
	if ops == nil {
		return
	}
	var req configPutRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"agents":[…]} 整表提交: `+err.Error())
		return
	}
	s.applyConfigTable(w, ops, req.Agents, "整表提交")
}

// handleAgentsReload POST /api/v1/local/agents/reload：手编 node.yaml 后的网页
// 一键重载——重读盘上配置走同一校验/分类/热应用链（普通字段即时生效不重启，
// 结构性变更标待应用）。
//
// 第五轮收敛 1：读文件+解析纳入 writeConfigMu 临界区（与 PUT/disconnect 同锁，
// 锁内完成读-校验-分类-写盘-热应用全链）——原实现在锁外读盘解析出旧表再进
// 加锁写流程，存在「reload 读旧表 → PUT 写新表 → reload 以旧表覆盖回盘」竞态
// 窗口。锁内无阻塞网络操作：能力表上报（CardsReporter）留在锁外尽力执行。
func (s *Server) handleAgentsReload(w http.ResponseWriter, r *http.Request) {
	ops := s.configPreflight(w)
	if ops == nil {
		return
	}
	if !s.applyReloadLocked(w, ops) {
		return
	}
	if s.cfg.CardsReporter != nil && s.cfg.Agents != nil {
		_ = s.cfg.CardsReporter(s.cfg.Agents.Cards()) // 锁外尽力上报；离线待重连收敛
	}
}

// configPreflight 配置写端点公共前置：装配存在性（nil → 503）。
func (s *Server) configPreflight(w http.ResponseWriter) *ConfigOps {
	if s.cfg.ConfigOps == nil || s.cfg.ConfigOps.ReloadExecutors == nil {
		writeErr(w, http.StatusServiceUnavailable, "config_disabled", "配置编辑未装配（装配方未接线 ConfigOps/ReloadExecutors）")
		return nil
	}
	return s.cfg.ConfigOps
}

// readConfigRaw 读 node.yaml（不存在 → 404；读失败 → 500）。
func (s *Server) readConfigRaw(w http.ResponseWriter, ops *ConfigOps) ([]byte, bool) {
	raw, err := os.ReadFile(ops.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, "config_missing", "node.yaml 不存在（setup 模式请先完成接入向导）")
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取 node.yaml 失败: "+err.Error())
		return nil, false
	}
	return raw, true
}

// applyConfigTable PUT 提交主体：校验 → 事务锁内「读文件→改写→写盘→热应用」→
// 分类结果应答。source 仅用于错误文案定位。
//
// 安全审查修复 3：共享配置的完整读-改-写纳入同一 writeConfigMu 临界区（读文件
// 前持锁，写盘与内存生效完成后释放）——否则 declare 与 PUT config 并发时各自
// 读旧文件互相覆盖（实测刚保存的 description 被 declare 覆盖）。锁内不做网络
// 操作：能力表上报（CardsReporter）移到锁外尽力执行。reload 同纪律走
// applyReloadLocked（第五轮收敛 1：其读文件+解析亦在同一临界区内）。
func (s *Server) applyConfigTable(w http.ResponseWriter, ops *ConfigOps, agents []AgentProfile, source string) {
	if err := ValidateAgentProfiles(agents); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", source+"被拒绝: "+err.Error())
		return
	}
	if s.applyConfigTableLocked(w, ops, agents) && s.cfg.CardsReporter != nil && s.cfg.Agents != nil {
		_ = s.cfg.CardsReporter(s.cfg.Agents.Cards()) // 锁外尽力上报；离线待重连收敛
	}
}

// applyConfigTableLocked PUT 事务段（调用方随后按返回值上报能力表）：持
// writeConfigMu 完成「读文件→改写→写盘→分类→热应用」与应答；返回 true = 已写入
// 且热应用成功。
func (s *Server) applyConfigTableLocked(w http.ResponseWriter, ops *ConfigOps, agents []AgentProfile) bool {
	writeConfigMu.Lock()
	defer writeConfigMu.Unlock()
	raw, ok := s.readConfigRaw(w, ops)
	if !ok {
		return false
	}
	return s.applyParsedLocked(w, ops, raw, agents)
}

// applyReloadLocked reload 事务段（第五轮收敛 1）：读文件+解析+校验与后续
// 改写-写盘-分类-热应用在同一 writeConfigMu 临界区内完成——解析出的 agents 表
// 不再有锁外陈旧窗口（旧实现锁外读盘解析，PUT 插写新表后 reload 以旧表覆盖回
// 盘）。返回 true = 已写入且热应用成功。
func (s *Server) applyReloadLocked(w http.ResponseWriter, ops *ConfigOps) bool {
	writeConfigMu.Lock()
	defer writeConfigMu.Unlock()
	raw, ok := s.readConfigRaw(w, ops)
	if !ok {
		return false
	}
	var parsed struct {
		Agents []AgentProfile `yaml:"agents"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_config", "node.yaml 解析失败（先修正文件再重载）: "+err.Error())
		return false
	}
	if err := ValidateAgentProfiles(parsed.Agents); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "文件重载被拒绝: "+err.Error())
		return false
	}
	return s.applyParsedLocked(w, ops, raw, parsed.Agents)
}

// applyParsedLocked 事务后半段（调用方已持 writeConfigMu）：以 raw 为底稿改写
// agents 表并写盘，分类后热应用或标「待应用」，应答就地写出。返回 true = 已写入
// 且热应用成功。
func (s *Server) applyParsedLocked(w http.ResponseWriter, ops *ConfigOps, raw []byte, agents []AgentProfile) bool {
	content, err := updateAgentsYAML(raw, agents)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_config", err.Error()+"（未改动原文件）")
		return false
	}
	if err := writeConfigFileLocked(ops.ConfigPath, string(content)); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", "node.yaml 写盘失败（未改动原文件可重试）: "+err.Error())
		return false
	}
	next := make([]protocol.ExecProfile, 0, len(agents))
	for _, p := range agents {
		next = append(next, p.ToExecProfile())
	}
	structural := classifyProfilesChange(s.effectiveProfiles(), next)
	if len(structural) > 0 {
		s.setPendingRestart(true)
		writeJSON(w, http.StatusOK, map[string]any{
			"written": ops.ConfigPath, "applied": false, "pending_restart": true,
			"structural": structural,
			"note":       "已写入 node.yaml（未生效）：含结构性变更（增删 agent / command / agent_args / kind），重启后生效；可在本页点「应用并重启」",
		})
		return false
	}
	if err := s.applyHotConfig(ops, next); err != nil {
		s.setPendingRestart(true)
		writeJSON(w, http.StatusOK, map[string]any{
			"written": ops.ConfigPath, "applied": false, "pending_restart": true,
			"error": err.Error(),
			"note":  "已写入 node.yaml，但热应用未完成（原因见 error）：重启后按盘上新配置收敛生效，可在本页点「应用并重启」",
		})
		return false
	}
	s.setPendingRestart(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"written": ops.ConfigPath, "applied": true, "pending_restart": false,
		"note": "已写入 node.yaml 并热重载生效（未重启进程，在途任务不受影响）",
	})
	return true
}

// applyHotConfig 普通字段热应用（顺序即失败语义）：先重建执行器换表——失败时
// 基线与运行时一致为旧值（磁盘已是新表，重启收敛）；成功后才整表替换基线提交
//（覆盖集一并清空——提交表即权威，否则盘上 yaml 与生效值分叉、重启回跳）。
// 能力表上报（CardsReporter）不在本函数——修复 3：它在 writeConfigMu 事务锁外
// 由 applyConfigTable 尽力执行，锁内不得阻塞于网络。须持 writeConfigMu 调用。
func (s *Server) applyHotConfig(ops *ConfigOps, next []protocol.ExecProfile) error {
	if s.cfg.Agents == nil {
		return errors.New("未装配 agent 状态（装配方须传入 Agents）")
	}
	if err := ops.ReloadExecutors(next); err != nil {
		return fmt.Errorf("执行器热重载失败（基线与运行时保持旧值，重启后生效）: %w", err)
	}
	if err := s.cfg.Agents.ReplaceBaseline(next); err != nil {
		return fmt.Errorf("基线落盘失败（执行器已按提交表运行，基线保持旧值，重启后收敛）: %w", err)
	}
	return nil
}

// handleRestart POST /api/v1/local/restart：「应用并重启」——HTTP 应答先返回
//（JSON 已触发），再回调装配方延时优雅退出靠保活拉起（方案 3.5 同 3.3 时序）。
// 裸前台（保活检测未命中）在应答注明手动重启命令与检测方式。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	ops := s.cfg.ConfigOps
	if ops == nil || ops.Restart == nil {
		writeErr(w, http.StatusServiceUnavailable, "restart_disabled", "重启未装配（装配方未接线 ConfigOps.Restart）")
		return
	}
	note := "node 将在约 1.5 秒后优雅退出，由保活（launchd/systemd）拉起并加载新配置；本页稍后自动恢复。"
	if !ops.Supervised {
		note = "检测为裸前台运行（未命中保活特征：SWARM_SUPERVISED=1 / systemd INVOCATION_ID / 父进程为 launchd）：" +
			"进程即将退出，请手动重启后刷新本页——" + ops.RestartCmd
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restarting": true, "supervised": ops.Supervised, "note": note,
	})
	ops.Restart() // 只排定延时退出，不阻塞应答（先返回，后退出）
}

// pendingRestart / setPendingRestart 待应用状态（结构性变更写盘未生效期间为真；
// 热应用成功或重启加载新配置后归假——重启即进程退出，内存态自然清零）。
func (s *Server) pendingRestart() bool {
	s.muCfg.Lock()
	defer s.muCfg.Unlock()
	return s.pendingRestartFlag
}

func (s *Server) setPendingRestart(v bool) {
	s.muCfg.Lock()
	s.pendingRestartFlag = v
	s.muCfg.Unlock()
}

// ---------- yaml.Node round-trip（保留注释与未知字段，矩阵 #19） ----------

// agentFieldOrder 已知字段规范序（配置编辑字段全集；与示例配置同序）。补写缺失键
// 按此序定位插入点；agentYAMLFields 由其派生，防两处漂移。
var agentFieldOrder = []string{
	"name", "kind", "command", "workdir", "agent_args", "timeout_sec",
	"allow_args", "allow_env", "config_home", "description", "permission_mode", "exposed",
}

// agentFieldRank 字段规范序位（未知键 -1）。
func agentFieldRank(key string) int {
	for i, k := range agentFieldOrder {
		if k == key {
			return i
		}
	}
	return -1
}

// agentYAMLFields 配置编辑已知的条目字段（原位更新范围）；条目内未知键原样
// 保留（用户手写注释与自定义字段不丢）。
var agentYAMLFields = func() map[string]bool {
	m := make(map[string]bool, len(agentFieldOrder))
	for _, k := range agentFieldOrder {
		m[k] = true
	}
	return m
}()

// updateAgentsYAML 读入 node.yaml，原位更新顶层 agents 序列并重编码：
//   - 顶层非 agents 键（含注释与未知字段）节点不动——保真核心；
//   - 已有条目按 name 匹配：已知字段原位更新值（键序保留、键与值的注释保留），
//     缺失的已知键按规范序补写（提交值非缺省才写，第三轮返工必须修 2——防
//     「热重载生效、盘上未写、重启回跳」），未知键原样保留；无法定位 name 的
//     既有条目视为被删除；
//   - 新增条目按规范字段序追加；删除条目即从序列移除；整表为空时 agents 置空序列。
//
// 解析失败/顶层非映射/agents 非列表返回错误（调用方拒写，原文件不动）。
func updateAgentsYAML(raw []byte, agents []AgentProfile) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 node.yaml 失败: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("node.yaml 顶层不是文档，无法安全编辑")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("node.yaml 顶层不是键值映射，无法安全编辑")
	}
	agentsKeyIdx := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "agents" {
			agentsKeyIdx = i
			break
		}
	}
	if len(agents) == 0 {
		// 整表清空：保留键位（及其注释），值置空序列
		empty := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if agentsKeyIdx >= 0 {
			root.Content[agentsKeyIdx+1] = empty
		} else {
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "agents"}, empty)
		}
	} else {
		var seq *yaml.Node
		if agentsKeyIdx >= 0 {
			seq = root.Content[agentsKeyIdx+1]
			if seq.Kind != yaml.SequenceNode {
				return nil, errors.New("agents 段不是列表，无法安全编辑（请手工修正后重试）")
			}
		} else {
			seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "agents"}, seq)
		}
		byName := make(map[string]*yaml.Node, len(seq.Content))
		for _, entry := range seq.Content {
			if name := entryName(entry); name != "" {
				byName[name] = entry
			}
		}
		rebuilt := make([]*yaml.Node, 0, len(agents))
		for _, p := range agents {
			if entry, ok := byName[p.Name]; ok {
				updateAgentEntry(entry, p)
				rebuilt = append(rebuilt, entry)
			} else {
				rebuilt = append(rebuilt, newAgentEntry(p))
			}
		}
		seq.Content = rebuilt
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("重编码 node.yaml 失败: %w", err)
	}
	return out, nil
}

// entryName 取条目 name（非映射或无名返回 ""——视为不可匹配，将被移除）。
func entryName(entry *yaml.Node) string {
	if entry.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if entry.Content[i].Value == "name" && entry.Content[i+1].Kind == yaml.ScalarNode {
			return entry.Content[i+1].Value
		}
	}
	return ""
}

// updateAgentEntry 原位更新单条目：已知字段换值节点（键节点与注释保留、未知键
// 不动）；提交值缺省（空串/空列表/默认 true/false）且键已存在时写显式缺省值，
// 保留键位与注释——最小改动，不重排不删键。
// 补写缺失键（第三轮返工必须修 2）：原实现只遍历条目既有键——提交值里条目原本
// 缺失的已知键永不落盘（热重载生效新值但盘上静默未写，重启回跳）。现对规范字段序
// 的每个键：缺失且提交值非缺省 → 按规范位插入；缺失且缺省 → 不写（保持最小形态）。
func updateAgentEntry(entry *yaml.Node, p AgentProfile) {
	for i := 0; i+1 < len(entry.Content); i += 2 {
		key := entry.Content[i].Value
		if !agentYAMLFields[key] {
			continue // 未知键：原样保留
		}
		old := entry.Content[i+1]
		nv := agentFieldNode(key, p)
		nv.HeadComment = old.HeadComment
		nv.LineComment = old.LineComment
		nv.FootComment = old.FootComment
		entry.Content[i+1] = nv
	}
	addMissingFields(entry, p)
}

// addMissingFields 补写条目缺失的已知键：提交值非缺省才写（缺省且键不存在 →
// 不写，保持与新增条目一致的最小形态）；插入点按规范字段序（最后一个规范序在
// 其前的既有键之后），既有键相对顺序与注释不动。
func addMissingFields(entry *yaml.Node, p AgentProfile) {
	for _, key := range agentFieldOrder {
		if entryHasKey(entry, key) || agentFieldIsDefault(key, p) {
			continue
		}
		insertAgentField(entry, key, agentFieldNode(key, p))
	}
}

// entryHasKey 条目是否已有该键。
func entryHasKey(entry *yaml.Node, key string) bool {
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if entry.Content[i].Value == key {
			return true
		}
	}
	return false
}

// insertAgentField 把 key: value 插到规范位（最后一个规范序在其前的既有键之后；
// 无则插在映射头部——被 name 匹配命中的条目必有 name，实际不可达）。
func insertAgentField(entry *yaml.Node, key string, value *yaml.Node) {
	rank := agentFieldRank(key)
	at := 0
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if r := agentFieldRank(entry.Content[i].Value); r >= 0 && r < rank {
			at = i + 2
		}
	}
	entry.Content = append(entry.Content, nil, nil)
	copy(entry.Content[at+2:], entry.Content[at:])
	entry.Content[at] = &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	entry.Content[at+1] = value
}

// agentFieldIsDefault 字段缺省判定（与 newAgentEntry 的最小形态同口径）：缺省值
// 且键不存在 → 不写。timeout_sec 校验强制正数，实际恒非缺省。
func agentFieldIsDefault(field string, p AgentProfile) bool {
	switch field {
	case "name":
		return p.Name == ""
	case "kind":
		k := p.Kind
		if k == "" {
			k = string(protocol.AgentKindCLI)
		}
		return k == string(protocol.AgentKindCLI)
	case "command":
		return p.Command == ""
	case "workdir":
		return p.Workdir == ""
	case "agent_args":
		return len(p.AgentArgs) == 0
	case "timeout_sec":
		return p.TimeoutSec <= 0
	case "allow_args":
		return !p.AllowArgs
	case "allow_env":
		return len(p.AllowEnv) == 0
	case "config_home":
		return p.ConfigHome == ""
	case "description":
		return p.Description == ""
	case "permission_mode":
		return p.PermissionMode == ""
	case "exposed":
		return p.Exposed == nil || *p.Exposed
	}
	return true
}

// newAgentEntry 新增条目：规范字段序；缺省值字段不写（kind=cli、allow_args=false、
// exposed=true、空串/空列表），保持与示例配置同款最小形态。
func newAgentEntry(p AgentProfile) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, n *yaml.Node) {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, n)
	}
	add("name", strNode(p.Name))
	kind := p.Kind
	if kind == "" {
		kind = string(protocol.AgentKindCLI)
	}
	add("kind", strNode(kind))
	add("command", strNode(p.Command))
	add("workdir", strNode(p.Workdir))
	if len(p.AgentArgs) > 0 {
		add("agent_args", seqNode(p.AgentArgs))
	}
	add("timeout_sec", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(p.TimeoutSec)})
	if p.AllowArgs {
		add("allow_args", boolNode(true))
	}
	if len(p.AllowEnv) > 0 {
		add("allow_env", seqNode(p.AllowEnv))
	}
	if p.ConfigHome != "" {
		add("config_home", strNode(p.ConfigHome))
	}
	if p.Description != "" {
		add("description", strNode(p.Description))
	}
	if p.PermissionMode != "" {
		add("permission_mode", strNode(p.PermissionMode))
	}
	if p.Exposed != nil && !*p.Exposed {
		add("exposed", boolNode(false))
	}
	return m
}

// agentFieldNode 由提交档案生成字段值节点（对应 agentYAMLFields 的每个键）。
func agentFieldNode(field string, p AgentProfile) *yaml.Node {
	switch field {
	case "name":
		return strNode(p.Name)
	case "kind":
		kind := p.Kind
		if kind == "" {
			kind = string(protocol.AgentKindCLI)
		}
		return strNode(kind)
	case "command":
		return strNode(p.Command)
	case "workdir":
		return strNode(p.Workdir)
	case "agent_args":
		return seqNode(p.AgentArgs)
	case "timeout_sec":
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(p.TimeoutSec)}
	case "allow_args":
		return boolNode(p.AllowArgs)
	case "allow_env":
		return seqNode(p.AllowEnv)
	case "config_home":
		return strNode(p.ConfigHome)
	case "description":
		return strNode(p.Description)
	case "permission_mode":
		return strNode(p.PermissionMode)
	case "exposed":
		exposed := true
		if p.Exposed != nil {
			exposed = *p.Exposed
		}
		return boolNode(exposed)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
}

func strNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func boolNode(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
}

func seqNode(items []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it})
	}
	return seq
}
