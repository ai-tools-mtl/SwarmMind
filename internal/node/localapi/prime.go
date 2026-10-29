// 总理凭证与钦定（方案 3.2.1，2026-10-22）：
//   - POST /api/v1/local/prime/rotate  控制台一键轮换 prime_token——重生成
//     → 原子写盘（data_dir/prime_token，0600）→ 即时重载（旧凭证下一请求即
//     失效）。新凭证仅在应答中返回一次（与 hub 令牌创建同约定）。
//   - POST /api/v1/local/prime/declare 控制台网页钦定总理——校验 agent 存在
//     → 自动轮换出新 prime_token（钦定即换凭证：旧持有者先失效——安全审查修复
//     2 的次序不变式：声明生效时旧凭证必已失效）→ yaml.Node round-trip 写
//     node.yaml 顶层 prime_agent（保真纪律同 config.go，读改写持 writeConfigMu
//     事务锁）→ 即时生效（SetPrimeAgent）→ 应答带 mcp_config 配置片段（仅 prime
//     凭证，不含 local_token）。
//
// 钦定即接线（t20261023-62a2，2026-10-23）：declare/rotate 成功后自动把
// swarmmind MCP 配置合并写入被钦定 agent 影子 home 的宿主配置文件
// （primewire.go，zcode 布局命中时零手贴；应答带 wired/wired_target 口径），
// 探测未命中降级手动片段——mcp_config 始终随应答给出，降级时人工粘贴兜底。
//
// 换总理原三步（文档口径：改 node.yaml prime_agent 声明 → 轮换 → 新总理配新
// 凭证）由 declare 一次收口；运行中旧总理 MCP 连接下一请求 401/403。
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

// handlePrimeRotate POST /api/v1/local/prime/rotate：重生成总理凭证（仅
// local_token 可调——轮换是本机主人操作，不随编排凭证下发）。
//
// 轮换串行化（返工 #4）：生成（RotatePrime 回调，含写盘）与内存重载纳入同一
// muPrime 互斥段——并发轮换若不加锁会出现「A 写盘 → B 写盘 → B 更新 → A 更新」
// 交错，盘上凭证与内存凭证分叉（持盘上凭证的新总理反而 401）。
func (s *Server) handlePrimeRotate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RotatePrime == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "统筹凭证轮换未装配（RotatePrime 回调缺失）")
		return
	}
	s.muPrime.Lock()
	tok, err := s.cfg.RotatePrime() // 重生成 + 原子写盘（装配方接线）
	if err != nil {
		s.muPrime.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal", "轮换失败（旧凭证仍有效，可重试）: "+err.Error())
		return
	}
	s.primeTok = tok // 同锁内即时重载：旧凭证下一请求即失效

	// 自动接线（t20261023-62a2，primewire.go）：已钦定总理的影子 home 内刷新
	// SWARM_NODE_PRIME_TOKEN——否则旧 token 留在宿主配置里，统筹载体下一请求
	// 401。仍在 muPrime 互斥段内：轮换+接线串行，杜绝并发轮换的「旧 token 后
	// 落盘盖掉新 token」交错。未钦定/布局未命中 → 降级手动片段，不影响轮换成功。
	agent := s.primeAgent()
	mcp := s.mcpConfig(tok) // R3（codex 审查）：与 declare 复用同一构造，应答同带 mcp_config——降级时主人拿得到片段（SPEC/PRIME 承诺口径）
	wired, target, _ := s.wirePrime(agent, mcp)
	s.muPrime.Unlock()

	resp := map[string]any{
		"prime_token": tok,
		"prime_agent": agent,
		"mcp_config":  mcp,
	}
	resp["note"] = applyWiredFields(resp, wired, target) + "；旧凭证已即时失效"
	writeJSON(w, http.StatusOK, resp)
}

// primeDeclareRequest POST /api/v1/local/prime/declare 请求体。
type primeDeclareRequest struct {
	PrimeAgent string `json:"prime_agent"`
}

// handlePrimeDeclare POST /api/v1/local/prime/declare：网页钦定总理（仅
// local_token 可调——钦定即凭据授予，本机主人操作，agent 面不可达）。
//
// 全程持 muPrime（锁序 muPrime→writeConfigMu→muCfg，包内无反向路径）：凭证
// 轮换、yaml 写盘、声明重载在同一互斥段内完成——并发 declare/rotate 与之交错
// 会出现盘上声明、内存声明、内存凭证、盘上凭证四方分叉（rotate 返工 #4 同型）。
// muPrime 覆盖 writeConfigMu：包内无「持 writeConfigMu 再取 muPrime」的路径，
// 无锁序反转；yaml 写盘（②）与声明生效（③）同处一个 writeConfigMu 临界区
// （第五轮收敛 2）。
//
// 步骤次序（安全审查修复 2：失败语义一致化）——先轮换、再写声明、最后生效：
//
//	① 轮换 prime_token（写盘+内存，旧凭证立即失效）
//	② 读改写 node.yaml prime_agent（读文件前持 writeConfigMu——修复 3 事务锁）
//	③ SetPrimeAgent（声明即时生效；同 writeConfigMu 临界区内——第五轮收敛 2）
//	④b 声明联动（2026-10-29 走查反馈批 P1-01：ReplaceOrchestratorHolder 单锁
//	   原子替换声明持有人——撤旧+授新整集一次落盘，YAML 基线旧声明可正确翻转；
//	   失败不回滚凭证，orchestrator_error 如实带出）
//
// 不变式：声明生效（③ 执行）时旧凭证必已失效（① 先行）——永不出现「新总理已
// 声明、旧持有人仍有权」的组合。任一步失败：已成功步骤保留，应答 500/4xx 如实
// 说明进行到哪一步，重试 declare 即收敛（yaml 写幂等，每次重试重新轮换）；②
// 失败时不调 SetPrimeAgent（内存声明保持旧值），新凭证已在 data_dir/prime_token
// （0600）但未随应答展示——持有人未变的旧 MCP 连接已 401，重试 declare 换发。
func (s *Server) handlePrimeDeclare(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DeclareConfigPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "declare_disabled", "钦定未装配（装配方未接线 DeclareConfigPath）")
		return
	}
	if s.cfg.RotatePrime == nil {
		writeErr(w, http.StatusServiceUnavailable, "declare_disabled", "钦定未装配（RotatePrime 回调缺失，无法交接凭证）")
		return
	}
	var req primeDeclareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"prime_agent":"<本机 agent 名>"}`)
		return
	}
	name := strings.TrimSpace(req.PrimeAgent)

	// 1. 校验：非空且在本机 agents 配置中存在（人话提示列出可选名）。校验在
	//    轮换之前——非法请求不触发凭证轮换（旧 MCP 连接不被无谓打断）。
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

	// 2. 钦定即换凭证：先轮换出新 prime_token（重生成+原子写盘，同 rotate 语义）
	//    并同锁即时重载——旧持有者从这一刻起失效。
	tok, err := s.cfg.RotatePrime()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal",
			"钦定未生效：凭证轮换失败（node.yaml 未改动、声明未生效、旧凭证仍有效），重试 declare 即可: "+err.Error())
		return
	}
	s.primeTok = tok

	// 3. yaml.Node round-trip 写 node.yaml 顶层 prime_agent（注释与未知字段
	//    保留；键不存在则追加；原子写盘 0600）。读改写全程持 writeConfigMu
	//   （修复 3：与 PUT config/reload/disconnect 的读改写事务互斥，防各自读
	//    旧文件互相覆盖），写盘用 writeConfigFileLocked（锁已在本手法内）。
	writeConfigMu.Lock()
	raw, err := os.ReadFile(s.cfg.DeclareConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusNotFound, "config_missing",
			"node.yaml 不存在（setup 模式请先完成接入向导）；凭证已轮换（旧凭证失效、新凭证未展示），完成接入后重试 declare 即收敛")
		return
	}
	if err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal",
			"读取 node.yaml 失败；凭证已轮换（旧凭证失效、新凭证未展示），重试 declare 即收敛: "+err.Error())
		return
	}
	content, err := setPrimeAgentYAML(raw, name)
	if err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusBadRequest, "invalid_config",
			err.Error()+"（node.yaml 未改动）；凭证已轮换（旧凭证失效、新凭证未展示），修正文件后重试 declare 即收敛")
		return
	}
	if err := writeConfigFileLocked(s.cfg.DeclareConfigPath, string(content)); err != nil {
		writeConfigMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "write_failed",
			"node.yaml 写盘失败（原文件未改动）；凭证已轮换（旧凭证失效、新凭证已落 data_dir/prime_token 但未展示），重试 declare 即收敛: "+err.Error())
		return
	}

	// 4. 即时生效：yaml 落盘成功才切内存声明——声明生效时旧凭证必已失效（②）。
	//    第五轮收敛 2：SetPrimeAgent 留在 writeConfigMu 临界区内——若在锁外，
	//    窗口期 PUT config 的读-改-写可与内存声明切换交错，出现盘上 yaml 与内存
	//    声明状态不一致。SetPrimeAgent 内部仅取 muCfg、无阻塞操作，锁序
	//    muPrime→writeConfigMu→muCfg 无反向。
	s.SetPrimeAgent(name)
	writeConfigMu.Unlock()

	// 4b. 声明联动（2026-10-29 部署后走查反馈一：管家入口收敛形态卡后，能力卡
	// 「设为管家」按钮退役——钦定必须同时完成卡片 Orchestrator 声明，否则 hub
	// 目录无声明位：派发页无管家分组、工作单 origin 锚点校验被拒、双端管家分组
	// 全部落空）。P1-01 修正（同日反馈批）：不再「先撤旧、再授新」两次独立
	// SetAgentOrchestrator——撤旧写 override=false 不改 YAML 基线，授新的互斥按
	// 基线遍历必拒（基线单/双声明均实证复现）；改为 AgentState 单锁原子替换声明
	// 持有人（ReplaceOrchestratorHolder）：单锁构造完整目标覆盖集——基线声明位与
	// 既有覆盖项无论当前有效态真假一律显式压 false、新持有人授 true（fix2 收敛：
	// 只压「现在有效」项会在下次替换时丢既有 false 覆盖致基线复活），一次落盘后
	// 发布——成功即本机恰一（无零声明窗口），落盘失败旧有效声明原样保留。声明翻转失败不回滚凭证（declare 主契约已达成，SPEC
	// 失败语义不变），orchestrator_error 如实带出由控制台完成页呈现。持 muPrime
	// 串行于并发 declare/rotate；锁序 muPrime→Agents.mu，无反向路径（PATCH
	// orchestrator 不取 muPrime）。
	orchErr := ""
	if s.cfg.Agents != nil {
		if _, err := s.cfg.Agents.ReplaceOrchestratorHolder(name); err != nil {
			orchErr = err.Error()
		}
	}

	// 5. 自动接线（t20261023-62a2，primewire.go）：钦定即接线——被钦定 agent
	//    影子 home 命中 zcode 布局（.zcode/cli/setting.json）则合并写入
	//    mcpServers.swarmmind，纸条不经人的手；布局未命中降级手动片段，不影响
	//    钦定成功。仍持 muPrime：与并发 rotate/declare 的「轮换+接线」串行，
	//    杜绝旧 token 后落盘盖掉新 token 的交错。
	mcp := s.mcpConfig(tok)
	wired, target, _ := s.wirePrime(name, mcp)

	// 6. 应答：钦定名 + 一次性凭证 + mcp_config 片段（降级口径保留，wired 命中
	//    时控制台优先展示已写入路径）+ wired/wired_target 接线口径 note + 声明
	//    联动结果（orchestrator 成功位 / orchestrator_error 失败原因 /
	//    hub_synced 上报口径——控制台完成页三态的数据源；Agents 未装配的测试
	//    构造不携带联动键）。
	resp := map[string]any{
		"prime_agent": name,
		"prime_token": tok,
		"mcp_config":  mcp,
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
	resp["note"] = "已写入 node.yaml prime_agent 并即时生效（无需重启）；" + applyWiredFields(resp, wired, target)
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
