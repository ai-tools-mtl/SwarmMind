// 管家声明（W02-01 调用权与管家角色解耦重写，docs/tasks/v3/W02-01-rfc.md §五）：
//   - POST /api/v1/local/prime/declare 控制台网页钦定管家——校验 agent 存在
//     → yaml.Node round-trip 写 node.yaml 顶层 prime_agent（保真纪律同 config.go，
//     读改写持 writeConfigMu 事务锁）→ 即时生效（SetPrimeAgent）→ 卡片
//     Orchestrator 声明联动。**不再换发共享凭据、不再自动改写调用权状态**
//     （mcp_access.json 不被 declare 触碰）——普通「选择管家」不得暗中换发全体
//     调用者共享的密钥；凭据作废由主人显式「轮换」承担（mcpaccess.go）。
//   - POST /api/v1/local/prime/rotate  旧别名 → handleMcpAccessRotate（同一
//     实现，兼容不删；mcpaccess.go）。
//
// 钦定即接线（t20261023-62a2 保留）：declare 成功后仍自动把 swarmmind MCP 配置
// 合并写入被钦定 agent 影子 home 的宿主配置文件（primewire.go，zcode 布局命中时
// 零手贴；应答带 wired/wired_target 口径），但写入的是*当前*共享凭据（写盘≠展示
// ——响应中凭据位一律脱敏，明文一次性展示只发生在轮换事件）。探测未命中降级
// 手动片段指引（真凭据经「轮换」获取）。
//
// 运行中旧管家 MCP 连接不再随换人失效：共享凭据对所有已接线持有者持续有效，
// 直至主人显式轮换（W02-mcp-access 任务书目标行为变更，RFC §五兼容表）。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// primeDeclareRequest POST /api/v1/local/prime/declare 请求体。
type primeDeclareRequest struct {
	PrimeAgent string `json:"prime_agent"`
}

// handlePrimeDeclare POST /api/v1/local/prime/declare：网页钦定管家（仅
// local_token 可调——管家声明是本机主人操作，agent 面不可达）。
//
// 全程持 muPrime（锁序 muPrime→writeConfigMu→muCfg / Agents.mu，包内无反向
// 路径）：yaml 写盘、声明重载、接线在同一互斥段内完成——与并发 rotate（同经
// muPrime）的「接线写盘」串行，杜绝旧 token 后落盘盖掉新 token 的交错。
//
// 步骤次序（W02-01 去换发后无凭证副作用，任一步失败一切保持旧态、重试即收敛）：
//
//	① 读改写 node.yaml prime_agent（读文件前持 writeConfigMu——修复 3 事务锁）
//	② SetPrimeAgent（声明即时生效；同 writeConfigMu 临界区内——第五轮收敛 2）
//	③ 声明联动（ReplaceOrchestratorHolder 单锁原子替换声明持有人——失败不回滚
//	   声明主契约，orchestrator_error 如实带出）
//	④ 自动接线：把*当前*共享凭据写入被钦定 agent 影子 home（布局未命中/凭据为
//	   空 → wired=false 降级手动指引，不影响钦定成功）
//
// 响应字段兼容表见 RFC §五：prime_token 恒空串（不再随钦定换发展示）、
// mcp_config 凭据位为脱敏引用。
func (s *Server) handlePrimeDeclare(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DeclareConfigPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "declare_disabled", "钦定未装配（装配方未接线 DeclareConfigPath）")
		return
	}
	var req primeDeclareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"prime_agent":"<本机 agent 名>"}`)
		return
	}
	name := strings.TrimSpace(req.PrimeAgent)

	// 1. 校验：非空且在本机 agents 配置中存在（人话提示列出可选名）。校验在
	//    写盘之前——非法请求不产生任何副作用。
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", s.declareHint("prime_agent 必填"))
		return
	}
	if !s.agentConfigured(name) {
		writeErr(w, http.StatusBadRequest, "invalid_request", s.declareHint(fmt.Sprintf("agent %q 不在本机配置中", name)))
		return
	}

	s.muPrime.Lock()
	defer s.muPrime.Unlock()

	// 2. yaml.Node round-trip 写 node.yaml 顶层 prime_agent（注释与未知字段
	//    保留；键不存在则追加；原子写盘 0600）。读改写全程持 writeConfigMu
	//   （修复 3：与 PUT config/reload/disconnect 的读改写事务互斥，防各自读
	//    旧文件互相覆盖），写盘用 writeConfigFileLocked（锁已在本手法内）。
	//    W02-01 去换发：失败路径不再有「凭证已轮换」残局——原文件未改动、
	//    内存声明保持旧值、共享凭据不动，重试即收敛。
	writeConfigMu.Lock()
	raw, err := os.ReadFile(s.cfg.DeclareConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusNotFound, "config_missing",
			"node.yaml 不存在（setup 模式请先完成接入向导）；声明未生效（原状态未变）")
		return
	}
	if err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal",
			"读取 node.yaml 失败；声明未生效（原状态未变）: "+err.Error())
		return
	}
	content, err := setPrimeAgentYAML(raw, name)
	if err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid_config",
			err.Error()+"（node.yaml 未改动，声明未生效）")
		return
	}
	if err := writeConfigFileLocked(s.cfg.DeclareConfigPath, string(content)); err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "write_failed",
			"node.yaml 写盘失败（原文件未改动，声明未生效）: "+err.Error())
		return
	}

	// 3. 即时生效：yaml 落盘成功才切内存声明。第五轮收敛 2：SetPrimeAgent 留
	//    在 writeConfigMu 临界区内——若在锁外，窗口期 PUT config 的读-改-写可
	//    与内存声明切换交错，出现盘上 yaml 与内存声明状态不一致。SetPrimeAgent
	//    内部仅取 muCfg、无阻塞操作，锁序 muPrime→writeConfigMu→muCfg 无反向。
	s.SetPrimeAgent(name)
	writeConfigMu.Unlock()

	// 3b. 声明联动（2026-10-29 部署后走查反馈批 P1-01）：ReplaceOrchestratorHolder
	//     单锁原子替换声明持有人——撤旧+授新整集一次落盘，YAML 基线旧声明可正确
	//     翻转；失败不回滚声明主契约，orchestrator_error 如实带出。持 muPrime
	//     串行于并发 rotate/declare；锁序 muPrime→Agents.mu，无反向路径（PATCH
	//     orchestrator 不取 muPrime）。
	orchErr := ""
	if s.cfg.Agents != nil {
		if _, err := s.cfg.Agents.ReplaceOrchestratorHolder(name); err != nil {
			orchErr = err.Error()
		}
	}

	// 4. 自动接线（t20261023-62a2 保留，primewire.go）：把*当前*共享凭据合并写
	//    入被钦定 agent 影子 home 命中的 zcode 布局——写盘≠展示；凭据为空（内存
	//    无凭证，仅测试构造可出现）→ 降级，不写空凭证配置。仍持 muPrime：与并发
	//    rotate/declare 的「接线」串行，杜绝旧 token 后落盘盖掉新 token 的交错。
	tok := s.primeTok // 已持 muPrime 直读（同旧路径口径；rotate 同锁内更新）
	wired, target := false, ""
	if tok != "" {
		wired, target, _ = s.wirePrime(name, s.mcpConfig(tok))
	}

	// 5. 应答（RFC §五兼容表）：钦定名 + prime_token 兼容位（恒空——不再随钦定
	//    换发/展示）+ mcp_config 片段（凭据位脱敏引用）+ wired/wired_target 接线
	//    口径 note + 声明联动结果（orchestrator 成功位 / orchestrator_error /
	//    hub_synced 上报口径——Agents 未装配的测试构造不携带联动键）。
	resp := map[string]any{
		"prime_agent": name,
		"prime_token": "",
		"mcp_config":  s.mcpConfigMasked(tok),
	}
	if s.cfg.Agents != nil {
		resp["orchestrator"] = orchErr == ""
		if orchErr != "" {
			resp["orchestrator_error"] = orchErr
		}
		// hub_synced 真实语义（P1-02 修正）：reporter 已装配（组网）→ 能力表上报
		// 实况的结果（true=已提交上报 hub——提交成功≠hub 全网恰一确认；false=待
		// 同步，重连注册随能力表收敛）。翻转失败时上报照发（Cards 是本机实况，
		// hub 收敛到真实状态比静默跳过诚实）；未装配 reporter（standalone 未组网）
		// → 字段缺省：无上报环节，控制台按「未组网」呈现，绝不冒充已同步。
		if s.cfg.CardsReporter != nil {
			resp["hub_synced"] = s.cfg.CardsReporter(s.cfg.Agents.Cards()) == nil
		}
	}
	resp["note"] = "已写入 node.yaml prime_agent 并即时生效（无需重启）；钦定不再换发共享凭据（调用权与管家角色已解耦，既有凭据与授权状态不变，作废凭据请使用「轮换」）；" +
		applyWiredFieldsHint(resp, wired, target, "管家 MCP 连接未自动写入（布局未命中或凭据未生成）；如需手动配置，请由主人在「MCP 授权」区轮换获取一次性凭据后配给（SWARM_NODE_PRIME_TOKEN）")
	writeJSON(w, http.StatusOK, resp)
}

// agentConfigured name 是否在本机 agents 配置中（AgentState effective 视图，
// 与目录/开关/注册能力表同源）。
func (s *Server) agentConfigured(name string) bool {
	for _, p := range s.effectiveProfiles() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// declareHint 400 提示附可选 agent 名（本机没配 agent 时指向能力配置入口）。
func (s *Server) declareHint(reason string) string {
	var names []string
	for _, p := range s.effectiveProfiles() {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return reason + "；本机尚未配置任何 agent，请先在「服务 → 能力配置」登记并重启"
	}
	return fmt.Sprintf("%s；可选 agent：%s", reason, strings.Join(names, "、"))
}

// mcpConfig 总理 MCP 连接片段（方案 3.4 node backend 配置口径）：command 探测
// node 可执行文件同目录的 swarmmind-mcp；env 三键一次配齐，粘贴即用。
// 安全审查修复 1：不再下发 SWARM_NODE_TOKEN（local_token 是本机主人管理凭证，
// 可调 declare/rotate/restart/PUT config——随总理片段分发即把主人权限交给总理
// 持有人）。prime_token 认证级 ⊇ 只读（authRead 双收），MCP 六工具单凭证够用。
func (s *Server) mcpConfig(primeTok string) map[string]any {
	// LocalListen 含 IPv6 裸地址（如 "::1:8445"）时需方括号包裹 host 段，
	// 否则拼出的 URL 非法（审查建议修，2026-10-22）。
	listen := s.cfg.LocalListen
	if i := strings.LastIndex(listen, ":"); i >= 0 && strings.Contains(listen[:i], ":") && !strings.HasPrefix(listen, "[") {
		listen = "[" + listen[:i] + "]" + listen[i:]
	}
	return map[string]any{
		"command": s.mcpCommand(),
		"env": map[string]string{
			"SWARM_TARGET":           "node",
			"SWARM_NODE_URL":         "http://" + listen,
			"SWARM_NODE_PRIME_TOKEN": primeTok,
		},
	}
}

// mcpConfigMasked declare 应答用连接片段：与 mcpConfig 同形，凭据位为脱敏引用
//（W02-01：钦定不再随应答展示明文——明文一次性展示只发生在轮换事件，RFC §五）。
// 接线写盘仍用 mcpConfig（真实凭据），本片段仅供主人阅读/降级指引。
func (s *Server) mcpConfigMasked(primeTok string) map[string]any {
	m := s.mcpConfig(primeTok)
	if env, ok := m["env"].(map[string]string); ok {
		env["SWARM_NODE_PRIME_TOKEN"] = maskCredential(primeTok)
	}
	return m
}

// mcpCommandPlaceholder mcpCommand 探测未命中时返回的占位符。接线侧（wirePrime）
// 据此降级（R4 裁决）：占位 command 落盘无用（MCP 启动不了），一律 wired=false
// 不写占位配置。
const mcpCommandPlaceholder = "<swarmmind-mcp 绝对路径>"

// mcpCommand 探测本进程（node）同目录的 swarmmind-mcp：存在返回绝对路径，
// 不存在返回占位符（部署形态二进制同目录放置；开发态明示需自行补路径）。
func (s *Server) mcpCommand() string {
	if s.mcpCommandOverride != "" { // 同包测试注桩（生产不设）：注入确定 command
		return s.mcpCommandOverride
	}
	exe, err := os.Executable()
	if err != nil {
		return mcpCommandPlaceholder
	}
	name := "swarmmind-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe" // Windows 产物带 .exe 后缀（与 enroll 打包同口径）
	}
	cand := filepath.Join(filepath.Dir(exe), name)
	if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
		return cand
	}
	return mcpCommandPlaceholder
}

// setPrimeAgentYAML 读入 node.yaml，原位更新顶层 prime_agent 标量并重编码：
// 其余顶层键（含注释与未知字段）节点一律不动——保真核心（clearHubURL 同型，
// round-trip 最小子集）；键不存在则追加到映射尾部。
// 解析失败/顶层非映射返回错误（调用方拒写，原文件不动）。
func setPrimeAgentYAML(raw []byte, name string) ([]byte, error) {
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
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "prime_agent" {
			v := root.Content[i+1]
			if v.Kind != yaml.ScalarNode {
				// 安全审查修复 4：既有值是别名（prime_agent: *who）或其他非纯量
				// 节点时，原位改 Value/Tag/Style 会产生悬空别名引用（重启报
				// unknown anchor）——构造独立纯量节点整体替换，别名链原地断开，
				// 文件语义回归普通标量。
				old := v
				v = strNode(name)
				v.HeadComment = old.HeadComment
				v.LineComment = old.LineComment
				v.FootComment = old.FootComment
				root.Content[i+1] = v
			} else {
				v.Value = name
				v.Tag = "!!str"
				v.Style = 0 // 清旧引号风格（如手编残留的 ""），按需由编码器重排
			}
			out, err := yaml.Marshal(&doc)
			if err != nil {
				return nil, fmt.Errorf("重编码 node.yaml 失败: %w", err)
			}
			return out, nil
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "prime_agent"},
		strNode(name)) // 键不存在：追加（strNode 即 yaml.TagString 纯量，必要时编码器自动加引号）
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("重编码 node.yaml 失败: %w", err)
	}
	return out, nil
}
