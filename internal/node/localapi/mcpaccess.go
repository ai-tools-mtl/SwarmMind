// MCP 编排授权状态（W02-01，docs/tasks/v3/W02-01-rfc.md）：调用权与管家角色
// 解耦的持久开关——
//   - 状态文件 data_dir/mcp_access.json（0600、原子写、UTC；显式 enabled 是
//     唯一启用态，文件不存在≠启用）；
//   - GET  /api/v1/local/mcp/access        授权状态视图（凭据仅脱敏引用）；
//   - PUT  /api/v1/local/mcp/access        主人显式启停（保存失败不虚报成功）；
//   - POST /api/v1/local/mcp/access/rotate 换发共享编排凭据（一次返回新明文、
//     旧值即失效、No-store）；
//   - POST /api/v1/local/prime/rotate      旧别名 → 同一实现（兼容不删）。
//
// 三端点均仅 local_token 可调（authLocal 门，本机主人管理面；prime_token 调用
// 401）——凭据授予/主人管理面，SPEC 六.六人工专属豁免类（豁免登记见 RFC §七，
// SPEC 表由归属批次回写）。编排门（server.go authOrchestrate）只读 Enabled()；
// prime_token 编排请求需显式 enabled，local_token（主人）恒放行。
//
// 安全纪律：凭据只在轮换应答中一次展示（declare 不再携带明文，prime.go）；
// No-store 响应；绝不入日志；保存/轮换失败如实报错并保留旧态。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// mcpAccessFileName 状态文件名（data_dir 内，与 local_token/prime_token 同域）。
const mcpAccessFileName = "mcp_access.json"

// mcpAccessVersion 状态文件格式版本（读到未知版本拒绝加载——授权状态不猜测）。
const mcpAccessVersion = 1

// McpAccessState 状态文件结构（RFC §一）。
type McpAccessState struct {
	Version   int    `json:"version"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"` // UTC（RFC3339，Z 后缀）
	Source    string `json:"source"`     // migration | owner
}

// 状态来源（McpAccessState.Source）。
const (
	mcpSourceMigration = "migration" // 启动迁移推导落盘
	mcpSourceOwner     = "owner"     // 主人经 PUT 显式变更（authLocal 门已保证主体）
)

// McpAccess 授权开关的运行时真相源：盘上文件 + 内存快照。 mu 串行读写；path 为
// 空表示仅内存态（测试/未装配持久化——门按显式默认禁用判定）。
type McpAccess struct {
	mu      sync.Mutex
	path    string // data_dir/mcp_access.json；空 = 仅内存
	enabled bool
	source  string
	updated string
}

// OpenMcpAccess 打开（或按迁移规则初始化一次）data_dir 内的 MCP 编排授权状态
// （cmd/node 装配点调用；返回 migrated=true 表示本次执行了迁移推导）。
//
// 迁移判定（RFC §二，仅当状态文件不存在时执行一次；判定时点先于 prime_token
// 自动生成——保证「只有启动自动生成的 token 文件不能视为授权」）：
//
//	prime_agent 非空 且 prime_token 文件存在可读（len≥32）→ enabled
//	其余（含全新实例）                                    → disabled
//
// 落盘后显式状态即唯一真相：重启/管家增删不再重推导。加载/迁移写盘失败返回
// 错误（授权状态不允许带病猜测，装配方拒绝启动）。
func OpenMcpAccess(dataDir, primeAgent, primeTokenPath string) (*McpAccess, bool, error) {
	if dataDir == "" {
		return nil, false, errors.New("localapi: data_dir 为空，无法定位 MCP 授权状态文件")
	}
	path := filepath.Join(dataDir, mcpAccessFileName)
	if raw, err := os.ReadFile(path); err == nil {
		var st McpAccessState
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, false, fmt.Errorf("解析 %s 失败（授权状态不猜测；请检查或删除该文件后重启重新迁移）: %w", path, err)
		}
		if st.Version != mcpAccessVersion {
			return nil, false, fmt.Errorf("%s 版本 %d 未知（支持 %d），拒绝按未知格式判定授权", path, st.Version, mcpAccessVersion)
		}
		return &McpAccess{path: path, enabled: st.Enabled, source: st.Source, updated: st.UpdatedAt}, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	// 迁移推导（M2–M5）：角色声明与凭据在位双条件。
	tokenOK := false
	if b, err := os.ReadFile(primeTokenPath); err == nil && len(b) >= 32 {
		tokenOK = true
	}
	enabled := primeAgent != "" && tokenOK
	m := &McpAccess{path: path, enabled: enabled, source: mcpSourceMigration, updated: nowUTCString()}
	if err := m.persistLocked(); err != nil {
		return nil, false, fmt.Errorf("迁移结果落盘 %s 失败（授权状态必须显式落盘，拒绝仅内存推导值启动）: %w", path, err)
	}
	return m, true, nil
}

// NewMcpAccessMemory 仅内存态授权开关（测试/未装配持久化场景）：门按显式给定
// 的 enabled 判定，无盘上真相（进程内一致，重启不复存）。
func NewMcpAccessMemory(enabled bool) *McpAccess {
	return &McpAccess{enabled: enabled, source: mcpSourceOwner, updated: nowUTCString()}
}

// Enabled 当前是否显式启用（编排门读此处；禁用=唯一缺省）。
func (m *McpAccess) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// Set 显式启停（PUT 端点）：持久化成功才提交内存态；持久化失败返回错误且内存
// 保留旧值、盘上旧文件原样——不虚报成功（RFC §一失败语义）。
func (m *McpAccess) Set(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.enabled
	m.enabled = enabled
	m.source = mcpSourceOwner
	m.updated = nowUTCString()
	if err := m.persistLocked(); err != nil {
		m.enabled = old // 回滚内存态：调用方拿到错误即旧态仍生效
		return err
	}
	return nil
}

// Snapshot 状态快照（GET 视图数据源）。
func (m *McpAccess) Snapshot() (enabled bool, source string, updatedAt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled, m.source, m.updated
}

// persistLocked 落盘（调用方持 mu）：原子写 + 0600；path 为空 = 仅内存态跳过。
func (m *McpAccess) persistLocked() error {
	if m.path == "" {
		return nil
	}
	raw, err := json.Marshal(McpAccessState{
		Version:   mcpAccessVersion,
		Enabled:   m.enabled,
		UpdatedAt: m.updated,
		Source:    m.source,
	})
	if err != nil {
		return err
	}
	return atomicWriteFile0600(m.path, raw)
}

// nowUTCString 当前时刻 UTC（RFC3339；全工程时间基准统一 UTC，SPEC 第九节）。
func nowUTCString() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// maskCredential 凭据脱敏引用（RFC §3.1）：前 2 字符 + ********；空 = 未生成；
// 异常短值（≤4 字符）不给前缀位。任何应答绝不含明文。
func maskCredential(tok string) string {
	switch {
	case tok == "":
		return "未生成"
	case len(tok) <= 4:
		return "sw****"
	default:
		return tok[:2] + "********"
	}
}

// mcpAccessScopeText 授权开关的人话影响说明（GET/PUT 应答 scope 字段）。
const mcpAccessScopeText = "启用后，持共享编排凭据（prime_token）的外部 MCP 调用方可向本机派发任务；" +
	"禁用期间这些调用被拒绝（403 mcp_disabled），主人本机管理（local_token）不受影响。" +
	"禁用不等于撤销凭据——作废旧凭据请使用「轮换」。"

// mcpAccessView GET/PUT 应答形态（不含任何明文凭据）。
type mcpAccessView struct {
	Enabled          bool   `json:"enabled"`
	CredentialMasked string `json:"credential_masked"`
	Scope            string `json:"scope"`
	PrimeAgent       string `json:"prime_agent"`
	UpdatedAt        string `json:"updated_at"`
}

// mcpAccessView 组装当前视图（凭据掩码取自 muPrime 下的当前共享凭据）。
func (s *Server) mcpAccessView() mcpAccessView {
	enabled, _, updated := s.mcpAccessState()
	return mcpAccessView{
		Enabled:          enabled,
		CredentialMasked: maskCredential(s.primeToken()),
		Scope:            mcpAccessScopeText,
		PrimeAgent:       s.primeAgent(),
		UpdatedAt:        updated,
	}
}

// mcpAccessState 授权开关快照（cfg.McpAccess 未装配时按显式默认禁用——内存
// 缺省即禁用态，不存在「无状态=放行」的路径）。
func (s *Server) mcpAccessState() (bool, string, string) {
	if s.cfg.McpAccess == nil {
		return false, mcpSourceOwner, ""
	}
	enabled, source, updated := s.cfg.McpAccess.Snapshot()
	return enabled, source, updated
}

// noStore 为授权/凭据相关应答统一追加 No-store（凭据一次展示纪律的传输面）。
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// handleMcpAccessGet GET /api/v1/local/mcp/access：授权状态视图（凭据仅脱敏）。
func (s *Server) handleMcpAccessGet(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	writeJSON(w, http.StatusOK, s.mcpAccessView())
}

// handleMcpAccessPut PUT /api/v1/local/mcp/access：主人显式启停。持久化失败 →
// 500 如实报错（旧态保留，内存与盘上均不变），绝不返回成功形态；成功 → 返回同
// GET 形态（不返回任何密钥——任务书权限矩阵）。
func (s *Server) handleMcpAccessPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.McpAccess == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "MCP 授权状态未装配（装配方须传入 McpAccess）")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil || req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"enabled":true|false}`)
		return
	}
	if err := s.cfg.McpAccess.Set(*req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal",
			fmt.Sprintf("保存失败（授权状态保留为 %s，可重试）: %v", enabledWord(s.cfg.McpAccess.Enabled()), err))
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, s.mcpAccessView())
}

// enabledWord 布尔态的人话词（错误文案用）。
func enabledWord(v bool) string {
	if v {
		return "启用"
	}
	return "禁用"
}

// handleMcpAccessRotate POST /api/v1/local/mcp/access/rotate 与旧别名
// POST /api/v1/local/prime/rotate 的同一实现（RFC §3.3）：换发共享编排凭据——
// 重生成 → 原子写盘 → 同锁（muPrime）内存重载（旧值即失效）→ 自动接线刷新管家
// 影子 home 内 SWARM_NODE_PRIME_TOKEN；一次返回新明文、No-store、绝不入日志；
// 失败不报成功（旧凭证仍有效，可重试）。
//
// 轮换串行化（返工 #4 沿袭）：生成（RotatePrime 回调，含写盘）与内存重载纳入
// 同一 muPrime 互斥段——并发轮换不得出现「A 写盘 → B 写盘 → B 更新 → A 更新」
// 的盘上/内存分叉。轮换不改变授权开关状态（enabled 独立持久）。
func (s *Server) handleMcpAccessRotate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RotatePrime == nil {
		writeErr(w, http.StatusServiceUnavailable, "internal", "共享编排凭据轮换未装配（RotatePrime 回调缺失）")
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

	// 自动接线（t20261023-62a2，primewire.go）：已钦定管家的影子 home 内刷新
	// SWARM_NODE_PRIME_TOKEN——否则旧 token 留在宿主配置里，管家载体下一请求
	// 401。仍在 muPrime 互斥段内：轮换+接线串行，杜绝并发轮换的「旧 token 后
	// 落盘盖掉新 token」交错。未钦定/布局未命中 → 降级手动片段，不影响轮换成功。
	agent := s.primeAgent()
	mcp := s.mcpConfig(tok) // 应答带 mcp_config（轮换=凭据授予事件，明文一次展示口径）
	wired, target, _ := s.wirePrime(agent, mcp)
	enabled, _, _ := s.mcpAccessState() // 锁内取一致快照（仅取 McpAccess.mu，锁序 muPrime→McpAccess.mu 无反向）
	s.muPrime.Unlock()

	noStore(w)
	resp := map[string]any{
		"prime_token": tok, // 仅此一次展示
		"prime_agent": agent,
		"mcp_config":  mcp,
		"enabled":     enabled,
	}
	resp["note"] = applyWiredFields(resp, wired, target) + "；旧凭证已即时失效"
	writeJSON(w, http.StatusOK, resp)
}
