// Package sessionx 实现 node 侧持久会话内核（T1，docs/VISION-persistent-agent-collaboration.md
// 实施顺序第 2 步第一批）：Create / Send / List(/Status) / Close 四原语 + 平台↔原生
// 会话 ID 映射持久化（data_dir/sessions.json）+ 单写者轮次队列 + codex/zcode 双适配器。
// 命名避开 internal/hub/session 的节点连接语义。
//
// 执行模型（探针适配决策，docs/archive/TESTREPORT-native-session-20261029.md）：
// 每轮启动 CLI + 显式恢复原生 ID，不做 PTY 常驻、不做流式。同步阻塞式 API：一轮
// 调用跑完一轮 CLI，turn 全程留痕；异步执行器留待 T2 随暴露面引入。
//
// 判定纪律：CLI 退出码、输出解析成功、工作完成判据三者分离（两家退出码 0 都不等
// 于工作成功，探针 zcode 首轮实证）；原生 ID 丢失/回带不一致一律显式失败，绝不静默
// 新建线程。
//
// 进程治理：每轮一个进程组、轮终整组回收，语义与实现同源 internal/node/runner
// （SPEC 五；经 runner 导出薄包装复用，TASK-agent-session-core 改动范围 4）。
// 测试命令形态以 fake CLI 注入，绝不真实调用模型，真机实测属 T4。
package sessionx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Logger 会话内核运维日志接口（损坏文件起步、持久化失败等事件）；*log.Logger 直接兼容。
type Logger interface {
	Printf(format string, args ...any)
}

// nopLogger 未装配日志时的静默兜底。
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// AgentSpec 一个 agent 的会话执行配置（装配期给定；T2 接 service 配置面）。
type AgentSpec struct {
	// Adapter 适配器类型：protocol.SessionAdapterCodex | protocol.SessionAdapterZcode。
	Adapter string
	// Command CLI 可执行文件绝对路径（真实接线指向 codex/zcode；测试指向 fake 脚本）。
	Command string
	// PermissionMode 权限模式，语义随适配器：codex = 沙箱名（-s，如 workspace-write），
	// 审批策略恒 never（exec 非交互无审批通道）；zcode = --mode 值（如 edit）。
	// 空 = 适配器保守默认（codex read-only / zcode edit），绝不取 yolo/全权
	//（探针适配决策 4：权限能力独立建模，zcode edit 实测 Bash 无权限客户端，不默认升级）。
	PermissionMode string
	// TimeoutSec 单轮超时秒数；0 = 不设独立超时（沿用调用方 ctx）。负值装配拒绝。
	TimeoutSec int
	// Env 子进程环境（"K=V" 序列，整体替换不追加）；空 = 继承本进程环境。T1 内核
	// 不伪造干净基线，真实接线（T2）由调用方按 SPEC 五给干净 env + HOME=ConfigHome。
	Env []string
}

// Options 管理器可选装配项。
type Options struct {
	// Now 时钟源（测试注入）；nil = protocol.NowUTC（全工程 UTC 基准）。
	Now func() time.Time
	// Log 运维日志；nil = 静默。
	Log Logger
}

// Manager 会话内核：四原语 + 映射持久化 + 单写者。
type Manager struct {
	agents   map[string]AgentSpec // agent 名 → 规范化配置（Open 时校验定形，只读）
	adapters map[string]adapter   // agent 名 → 适配器实例（Open 时构建，无状态可并发）
	path     string               // data_dir/sessions.json
	now      func() time.Time
	log      Logger

	mu       sync.Mutex                   // 守护 sessions/writers 两表与持久化（单文件单写者）
	sessions map[string]*protocol.Session // 平台会话 ID → 会话记录（含轮次留痕）
	writers  map[string]*sessionGate      // 平台会话 ID → 写者门（同会话轮次严格串行；等待可响应 ctx）
}

// 装配/续接错误（显式拒绝面；调用方 errors.Is 判定）。
var (
	// ErrUnknownAgent agent 名不在装配表。
	ErrUnknownAgent = errors.New("sessionx: agent 不在装配表")
	// ErrSessionNotFound 平台会话 ID 不存在。
	ErrSessionNotFound = errors.New("sessionx: 会话不存在")
	// ErrSessionClosed 会话已关闭：拒绝续轮与重复关闭（显式错误，不做幂等吞并）。
	ErrSessionClosed = errors.New("sessionx: 会话已关闭")
	// ErrAgentMissing 持久会话绑定的 agent 已不在装配表（配置删除或改名）：拒绝
	// 续轮进入执行（R1 审查 R2——恢复路径不得对缺失配置取 nil 适配器执行）。
	// 不可自动恢复；修复配置需显式重建会话。
	ErrAgentMissing = errors.New("sessionx: 会话绑定的 agent 不在装配表")
	// ErrBindingChanged 持久会话的执行绑定与当前配置不一致：拒绝静默迁移（R1 审查
	// R3，上游 VISION 硬要求——绑定变化可追踪、禁止静默换绑或升权）。适配器变更
	// 会把旧原生会话 ID 交给另一 CLI；权限模式变更含提权与降权，显式迁移待 T2
	// 建立有记录的变更通道后进行。
	ErrBindingChanged = errors.New("sessionx: 会话执行绑定与当前配置不一致，拒绝静默迁移")
)

// NewManager 装载 sessions.json（缺失/损坏按空表起步并如实记日志）并校验 agent
// 配置：未知适配器 / 命令非绝对路径 / 负超时一律返回错误由调用方拒绝启动——宁可
// 不起也不带病装配。dataDir 不存在时创建（0o755，本机自有数据目录）。
func NewManager(dataDir string, agents map[string]AgentSpec, opt Options) (*Manager, error) {
	if dataDir == "" {
		return nil, errors.New("sessionx: dataDir 为空")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("sessionx: 创建数据目录失败: %w", err)
	}
	now := opt.Now
	if now == nil {
		now = protocol.NowUTC
	}
	log := opt.Log
	if log == nil {
		log = nopLogger{}
	}
	specs := make(map[string]AgentSpec, len(agents))
	ads := make(map[string]adapter, len(agents))
	for name, spec := range agents {
		s, err := normalizeSpec(name, spec)
		if err != nil {
			return nil, err
		}
		ad, err := newAdapter(s)
		if err != nil {
			return nil, fmt.Errorf("sessionx: agent %q: %w", name, err)
		}
		specs[name] = s
		ads[name] = ad
	}
	m := &Manager{
		agents:   specs,
		adapters: ads,
		path:     filepath.Join(dataDir, "sessions.json"),
		now:      now,
		log:      log,
		sessions: map[string]*protocol.Session{},
		writers:  map[string]*sessionGate{},
	}
	m.loadSessions() // 启动期装载，尚未共享，无锁要求
	return m, nil
}

// normalizeSpec 装配期配置定形：填充适配器保守默认权限、校验必填与取值边界。
func normalizeSpec(name string, spec AgentSpec) (AgentSpec, error) {
	if name == "" {
		return AgentSpec{}, errors.New("sessionx: agent 名为空")
	}
	s := spec
	switch s.Adapter {
	case protocol.SessionAdapterCodex:
		if s.PermissionMode == "" {
			s.PermissionMode = codexDefaultPermission
		}
	case protocol.SessionAdapterZcode:
		if s.PermissionMode == "" {
			s.PermissionMode = zcodeDefaultPermission
		}
	default:
		return AgentSpec{}, fmt.Errorf("sessionx: agent %q 适配器类型未知: %q", name, s.Adapter)
	}
	if s.Command == "" || !filepath.IsAbs(s.Command) {
		return AgentSpec{}, fmt.Errorf("sessionx: agent %q 命令必须为非空绝对路径，得 %q", name, s.Command)
	}
	if s.TimeoutSec < 0 {
		return AgentSpec{}, fmt.Errorf("sessionx: agent %q 超时为负: %d", name, s.TimeoutSec)
	}
	return s, nil
}

// Create 执行首轮并建立会话：从输出取回原生 ID（codex thread.started / zcode
// sessionId），建立平台↔原生映射并持久化。首轮任一路判定不成立（退出码非 0 /
// 输出非法 / 工作完成判据缺失 / 原生 ID 缺失）不建立会话、不落盘：返回零值会话、
// 轮次记录与错误供诊断。成功返回会话快照（深拷贝）。
func (m *Manager) Create(ctx context.Context, agentName, cwd, prompt string) (protocol.Session, *protocol.Turn, error) {
	ad, spec, err := m.lookupAgent(agentName)
	if err != nil {
		return protocol.Session{}, nil, err
	}
	if err := validateTurnInput(cwd, prompt); err != nil {
		return protocol.Session{}, nil, err
	}
	turn := &protocol.Turn{ExitCode: protocol.ExitCodeNotRun, StartedAt: m.now()}
	out, execErr := m.runAdapterTurn(ctx, ad, spec, cwd, "", prompt, true, turn)
	turn.Seq = 1
	if execErr != nil {
		// 原生 ID 缺失/不一致/三路判定失败：会话不建立、映射不落盘（探针适配决策 1）
		return protocol.Session{}, turn, execErr
	}
	sess := &protocol.Session{
		ID:             "s-" + randomHex(),
		AgentName:      agentName,
		Adapter:        spec.Adapter,
		Cwd:            cwd,
		NativeID:       out.NativeID,
		PermissionMode: spec.PermissionMode,
		Status:         protocol.SessionOpen,
		TurnCount:      1,
		Turns:          []protocol.Turn{*turn},
		CreatedAt:      m.now(),
		UpdatedAt:      m.now(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sess.ID] = sess
	m.writers[sess.ID] = &sessionGate{}
	if perr := m.saveSessionsLocked(); perr != nil {
		return cloneSession(sess), turn, fmt.Errorf("sessionx: 会话已建立但持久化失败（内存态保留，下次落盘收敛）: %w", perr)
	}
	return cloneSession(sess), turn, nil
}

// Send 以存储的原生 ID 续接一轮。同会话调用严格串行（写者门，后到者排队等待，
// 不拒绝，等待可响应 ctx 取消；R1 审查 R4）；跨会话并行不设限。排队中取消不 spawn、
// 不消耗轮号、不留痕。续接前核验 agent 存在性（R2）与持久绑定一致性（R3，上游
// VISION 硬要求：禁止静默换绑/升权），核验失败不进入执行、不消耗轮号。续轮回带的
// 原生 ID 缺失或与存储不一致：本轮判失败、映射保持不变，绝不静默换线程（探针适配
// 决策 1）。失败轮同样持久化留痕（R1 审查 R1 修订），执行错误与持久化错误分开
// 保留。返回会话快照（深拷贝）。
func (m *Manager) Send(ctx context.Context, sessionID, prompt string) (protocol.Session, *protocol.Turn, error) {
	g, err := m.writerFor(sessionID)
	if err != nil {
		return protocol.Session{}, nil, err
	}
	release, err := g.acquire(ctx)
	if err != nil {
		// 排队等待期间 ctx 取消/到期：本轮未开始，轮号未消耗
		return protocol.Session{}, nil, err
	}
	defer release()
	// 拿到写权后再次核验取消（acquire 直接命中路径不经过 select）；过期即还权，
	// 不 spawn。
	if cerr := ctx.Err(); cerr != nil {
		return protocol.Session{}, nil, cerr
	}
	// 写权在手，重读状态：排队等待期间会话可能已被 Close。
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return protocol.Session{}, nil, ErrSessionNotFound
	}
	if sess.Status != protocol.SessionOpen {
		m.mu.Unlock()
		return protocol.Session{}, nil, ErrSessionClosed
	}
	ad, agentOK := m.adapters[sess.AgentName]
	spec := m.agents[sess.AgentName]
	persistedAdapter, persistedPerm, nativeID, cwd := sess.Adapter, sess.PermissionMode, sess.NativeID, sess.Cwd
	seq := int64(sess.TurnCount + 1)
	m.mu.Unlock()
	// 续接前置核验（R2/R3）：不进入执行、不消耗轮号、会话状态不动。
	if !agentOK {
		return protocol.Session{}, nil, fmt.Errorf("%w: %q（配置可能已删除或改名）；拒绝续轮，会话记录保持不变", ErrAgentMissing, sess.AgentName)
	}
	if spec.Adapter != persistedAdapter {
		return protocol.Session{}, nil, fmt.Errorf("%w: 适配器变更 持久=%s 当前=%s（原生会话 ID 属于原 CLI，换绑续接已拒绝）", ErrBindingChanged, persistedAdapter, spec.Adapter)
	}
	if spec.PermissionMode != persistedPerm {
		return protocol.Session{}, nil, fmt.Errorf("%w: 权限模式变更 持久=%s 当前=%s（含提权与降权，均须走显式迁移通道）", ErrBindingChanged, persistedPerm, spec.PermissionMode)
	}

	turn := &protocol.Turn{ExitCode: protocol.ExitCodeNotRun, StartedAt: m.now()}
	_, execErr := m.runAdapterTurn(ctx, ad, spec, cwd, nativeID, prompt, false, turn)
	turn.Seq = seq

	m.mu.Lock()
	defer m.mu.Unlock()
	// 写者权在手，Close（同样先取写者权）不可能插入，会话必在且状态未变；
	// 重取与不存在分支是防御位。
	sess, ok = m.sessions[sessionID]
	if !ok {
		return protocol.Session{}, turn, ErrSessionNotFound
	}
	sess.Turns = append(sess.Turns, *turn)
	sess.TurnCount++
	sess.UpdatedAt = m.now()
	// 失败轮同样持久化（R1 审查 R1：失败留痕与序号在重启后必须完整）；执行错误
	// 与持久化错误分开保留，谁都不吞谁。
	perr := m.saveSessionsLocked()
	if execErr != nil {
		if perr != nil {
			return cloneSession(sess), turn, fmt.Errorf("%w（此外留痕落盘失败，内存态保留，下次落盘收敛）: %w", execErr, perr)
		}
		return cloneSession(sess), turn, execErr
	}
	if perr != nil {
		return cloneSession(sess), turn, fmt.Errorf("sessionx: 本轮已执行并留痕，但持久化失败（内存态保留，下次落盘收敛）: %w", perr)
	}
	return cloneSession(sess), turn, nil
}

// Status 返回会话快照（深拷贝，调用方改动不影响内核状态）；不存在返回 false。
func (m *Manager) Status(sessionID string) (protocol.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return protocol.Session{}, false
	}
	return cloneSession(s), true
}

// List 返回全部会话快照，按创建时刻升序（同时刻按 ID 字典序，排序确定）。
func (m *Manager) List() []protocol.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, cloneSession(s))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Close 平台侧关闭会话：置 closed 并持久化；对在途轮次先等其结束（同样走写者门，
// 无 ctx 版本不设截止期）再关闭。原生线程留存在 CLI 自身存储，不受影响（无删除
// 语义，探针适配决策 3）。
func (m *Manager) Close(sessionID string) (protocol.Session, error) {
	g, err := m.writerFor(sessionID)
	if err != nil {
		return protocol.Session{}, err
	}
	release, err := g.acquire(context.Background())
	if err != nil {
		return protocol.Session{}, err // 不可达（Background 不取消），防御位
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return protocol.Session{}, ErrSessionNotFound
	}
	if sess.Status == protocol.SessionClosed {
		return cloneSession(sess), ErrSessionClosed
	}
	sess.Status = protocol.SessionClosed
	sess.UpdatedAt = m.now()
	if perr := m.saveSessionsLocked(); perr != nil {
		return cloneSession(sess), fmt.Errorf("sessionx: 已关闭但持久化失败（内存态保留，下次落盘收敛）: %w", perr)
	}
	return cloneSession(sess), nil
}

// writerFor 取会话写者门句柄；不存在返回 ErrSessionNotFound（Create 未完成前
// Send/Close 亦走此路径）。
func (m *Manager) writerFor(sessionID string) (*sessionGate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.writers[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return g, nil
}

// lookupAgent 装配表查询（只读表，无锁）。
func (m *Manager) lookupAgent(agentName string) (adapter, AgentSpec, error) {
	ad, ok := m.adapters[agentName]
	if !ok {
		return nil, AgentSpec{}, fmt.Errorf("%w: %q", ErrUnknownAgent, agentName)
	}
	return ad, m.agents[agentName], nil
}

// validateTurnInput 轮次入参校验：cwd 绝对路径、prompt 非空（空提示词会让 args 型
// CLI 旗标缺参打 usage 退出，与 runner StdinToArg 教训同源）。
func validateTurnInput(cwd, prompt string) error {
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("sessionx: cwd 必须为绝对路径，得 %q", cwd)
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("sessionx: prompt 为空")
	}
	return nil
}

// cloneSession 深拷贝会话快照（Turns 逐条复制；string 不可变可共享）。
func cloneSession(s *protocol.Session) protocol.Session {
	c := *s
	c.Turns = append([]protocol.Turn(nil), s.Turns...)
	return c
}

// randomHex 16 字节加密随机数 hex（crypto/rand 失败属系统级异常，直接 panic，
// 对齐 protocol.randomHex 不做静默降级）。
func randomHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sessionx: crypto/rand 读取失败: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// sanitizeLimit UTF-8 清洗（非法字节 → U+FFFD）后按字节截断至 limit（截断点回落
// 到完整 rune 边界；与 runner 同款纪律，清洗 → 截断）。
func sanitizeLimit(s string, limit int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 {
		rn, size := utf8.DecodeLastRuneInString(s)
		if !(rn == utf8.RuneError && size == 1) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
