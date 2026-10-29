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
	// MaxSessionGates 写者门表容量上限（T2-R3 P2）：门终身保留不回收（理由见
	// writers 字段注释），上限拦截「失败/取消创建的唯一 ID 洪泛」造成的无界增长。
	// 达上限后新创建拒绝（ErrGateTableFull，CLI 不执行、无任何发布），既有会话
	// 的续轮/关闭与持久化重载零影响。0 = DefaultMaxSessionGates；负值装配拒绝。
	MaxSessionGates int
}

// DefaultMaxSessionGates 写者门表容量默认上限（Options.MaxSessionGates 零值时
// 生效）：容纳长期运行的活跃会话及其失败重试冗余，同时拦截唯一 ID 洪泛式失控
// 增长（R3 审查反例：1000 个已取消的唯一 ID 创建常驻 1000 门）。
const DefaultMaxSessionGates = 4096

// Manager 会话内核：四原语 + 映射持久化 + 单写者。
type Manager struct {
	agents   map[string]AgentSpec // agent 名 → 规范化配置（Open 时校验定形，只读）
	adapters map[string]adapter   // agent 名 → 适配器实例（Open 时构建，无状态可并发）
	path     string               // data_dir/sessions.json
	now      func() time.Time
	log      Logger
	maxGates int // 写者门表容量上限（NewManager 归一：0 → DefaultMaxSessionGates）

	mu       sync.Mutex                   // 守护 sessions/writers/closing 三表与持久化（单文件单写者）
	sessions map[string]*protocol.Session // 平台会话 ID → 会话记录（含轮次留痕）
	writers  map[string]*sessionGate      // 平台会话 ID → 写者门（同会话轮次严格串行；等待可响应 ctx）。
	// 门一经登记终身保留、永不回收（T2-R2 N1）：失败创建不拆门——「门由我创建」
	// 不构成拆除依据（排队者可能已持此门句柄在等写权，拆掉后新创建者另立新门，
	// 同一平台 ID 两个写者并行进 CLI）；且「锁内证明无持有者/等待者」不可证（查表
	// 与 acquire 之间无共同锁域，查表者可已持句柄尚未入队），按引用计数回收又须
	// 在查表/释放两处新增锁域协作面。保留门即安全：失败创建遗留的门被后续创建/
	// 续轮/关闭沿用，串行化依旧成立。保留的代价以容量上限兜底（T2-R3 P2）：表按
	// 「曾尝试登记的不同会话 ID 数」（活跃会话 + 失败/取消尝试遗留门）只增不减，
	// 达 maxGates 后拒绝新创建（ErrGateTableFull），既有门/活跃会话门/墓碑零影响；
	// 重启后仅按盘上活跃会话重建门（loadSessions 不受上限约束），失败遗留门随
	// 重启自然消失。
	closing map[string]bool // 关闭墓碑：会话映射发布前收到关闭意图的平台 ID
	//（T2-R1 必修4——关闭先于创建成立时，创建不再发布 open；防复活语义，有意
	// 无界不回收，不计入门表容量，量级为显式关闭过的未建会话数）
	// persistDirty 内存状态含未确认落盘的变更（T2-R2 N2）：置位于任何状态变更、
	// saveSessionsLocked 成功时清零。关闭类路径（墓碑登记/重复请求/已关闭快速
	// 返回）在 dirty 未清零时必须先重试落盘，成功前如实报真实错误——内存已关闭
	// ≠ 磁盘已关闭，虚报幂等成功会让重启后会话复活。
	persistDirty bool
}

// 装配/续接错误（显式拒绝面；调用方 errors.Is 判定）。
var (
	// ErrUnknownAgent agent 名不在装配表。
	ErrUnknownAgent = errors.New("sessionx: agent 不在装配表")
	// ErrSessionNotFound 平台会话 ID 不存在。
	ErrSessionNotFound = errors.New("sessionx: 会话不存在")
	// ErrSessionClosed 会话已关闭：拒绝续轮与重复关闭（显式错误，不做幂等吞并）。
	ErrSessionClosed = errors.New("sessionx: 会话已关闭")
	// ErrSessionExists 平台会话 ID 已存在：CreateWithID 拒绝（T2 会话身份由
	// hub/本机入口分配并先行登记注册表，内核绝不复用或覆盖既有映射——重复 ID
	// 多为极端崩溃窗口的重复投递，显式失败交调用方对账，绝不静默接续）。
	ErrSessionExists = errors.New("sessionx: 平台会话 ID 已存在")
	// ErrAgentMissing 持久会话绑定的 agent 已不在装配表（配置删除或改名）：拒绝
	// 续轮进入执行（R1 审查 R2——恢复路径不得对缺失配置取 nil 适配器执行）。
	// 不可自动恢复；修复配置需显式重建会话。
	ErrAgentMissing = errors.New("sessionx: 会话绑定的 agent 不在装配表")
	// ErrBindingChanged 持久会话的执行绑定与当前配置不一致：拒绝静默迁移（R1 审查
	// R3，上游 VISION 硬要求——绑定变化可追踪、禁止静默换绑或升权）。适配器变更
	// 会把旧原生会话 ID 交给另一 CLI；权限模式变更含提权与降权，显式迁移待 T2
	// 建立有记录的变更通道后进行。
	ErrBindingChanged = errors.New("sessionx: 会话执行绑定与当前配置不一致，拒绝静默迁移")
	// ErrGateTableFull 写者门表已达容量上限（Options.MaxSessionGates，T2-R3 P2）：
	// 门终身保留不回收的上限兜底——拒绝的是「新创建」（CLI 未执行），既有会话
	// 续轮/关闭与持久化重载不受影响。
	ErrGateTableFull = errors.New("sessionx: 会话门表已达容量上限，拒绝新创建")
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
	if opt.MaxSessionGates < 0 {
		return nil, fmt.Errorf("sessionx: MaxSessionGates 为负: %d", opt.MaxSessionGates)
	}
	maxGates := opt.MaxSessionGates
	if maxGates == 0 {
		maxGates = DefaultMaxSessionGates
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
		maxGates: maxGates,
		sessions: map[string]*protocol.Session{},
		writers:  map[string]*sessionGate{},
		closing:  map[string]bool{},
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
// sessionId），建立平台↔原生映射并持久化。平台会话 ID 由内核生成（T1 形态；
// T2 起经注册表的会话一律走 CreateWithID 由入口分配 ID，本方法保留给不经理
// 注册表的直接调用方）。首轮任一路判定不成立（退出码非 0 / 输出非法 / 工作完
// 成判据缺失 / 原生 ID 缺失）不建立会话、不落盘：返回零值会话、轮次记录与错误
// 供诊断。成功返回会话快照（深拷贝）。
func (m *Manager) Create(ctx context.Context, agentName, cwd, prompt string) (protocol.Session, *protocol.Turn, error) {
	return m.createWithID(ctx, "s-"+randomHex(), agentName, cwd, prompt)
}

// CreateWithID 以入口分配的平台会话 ID 执行首轮并建立会话（T2：hub 注册表 /
// node 本机入口先行分配并登记 "s-"+hex 平台身份，内核沿用同一 ID 建映射，保证
// 注册表与内核单一身份）。sessionID 必填非空；与既有会话冲突显式拒绝
// （ErrSessionExists，不覆盖不接续）。其余语义同 Create。
func (m *Manager) CreateWithID(ctx context.Context, sessionID, agentName, cwd, prompt string) (protocol.Session, *protocol.Turn, error) {
	if strings.TrimSpace(sessionID) == "" {
		return protocol.Session{}, nil, errors.New("sessionx: sessionID 为空")
	}
	return m.createWithID(ctx, sessionID, agentName, cwd, prompt)
}

// createWithID 首轮共用实现（Create 生成 ID / CreateWithID 校验并沿用入口 ID）。
//
// 生命周期门（T2-R1 必修4）：执行 CLI 前按平台 ID 登记写者门——Close/Send 与
// 创建共用同一门串行化，关闭在创建在途时排队等首轮收束后落定（不再出现「关闭
// 返回成功而首轮随后发布 open」的复活）；创建未开始（门未登记）时收到的关闭以
// 墓碑留痕并持久化（closing 表 + sessions.json），创建发布前核验墓碑：已关闭
// 意图则不执行 CLI、不发布映射（ErrSessionClosed），关闭决定对尚未开始的创建
// 同样成立。
func (m *Manager) createWithID(ctx context.Context, sessionID, agentName, cwd, prompt string) (protocol.Session, *protocol.Turn, error) {
	ad, spec, err := m.lookupAgent(agentName)
	if err != nil {
		return protocol.Session{}, nil, err
	}
	if err := validateTurnInput(cwd, prompt); err != nil {
		return protocol.Session{}, nil, err
	}
	// 生命周期门登记（守门纪律：既有的门沿用——既有会话/在途创建同门串行）。
	// 门终身保留（T2-R2 N1）：失败路径不拆门，理由见 writers 字段注释。
	m.mu.Lock()
	g := m.writers[sessionID]
	if g == nil {
		// 容量兜底（T2-R3 P2）：门终身保留不回收，上限拦截唯一 ID 洪泛；超限
		// 拒绝新创建并如实报错（CLI 未执行、无任何发布），既有门/活跃会话/墓碑
		// 零影响。持久化重载重建的会话门走 loadSessions、不受此限。
		if len(m.writers) >= m.maxGates {
			n, limit := len(m.writers), m.maxGates
			m.mu.Unlock()
			return protocol.Session{}, nil, fmt.Errorf("%w: %d/%d（门表按曾尝试登记的不同会话 ID 只增不减，超限拒绝新创建，既有会话不受影响）", ErrGateTableFull, n, limit)
		}
		g = &sessionGate{}
		m.writers[sessionID] = g
	}
	m.mu.Unlock()
	release, err := g.acquire(ctx)
	if err != nil {
		// 排队等待期间 ctx 取消/到期：本轮未开始，CLI 未执行、无任何发布
		return protocol.Session{}, nil, err
	}
	defer release()
	// 写权在手，终读关闭意图与既有会话：排队等待期间墓碑/映射可能已就位。
	// 墓碑在先：关闭决定先于本轮成立（含「关闭任务先于离线暂存的创建任务送达」
	// 的补投乱序）——不执行 CLI、不建会话、不发布映射。
	m.mu.Lock()
	if _, tombstoned := m.closing[sessionID]; tombstoned {
		m.mu.Unlock()
		return protocol.Session{}, nil, fmt.Errorf("%w: 关闭意图先于首轮成立（墓碑留痕），不再创建", ErrSessionClosed)
	}
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		// 同 ID 既有会话（注册表已登记 / 重复投递）：显式拒绝，绝不覆盖不接续；
		// CLI 未执行（无原生线程浪费），既有会话零触碰。
		return protocol.Session{}, nil, ErrSessionExists
	}
	m.mu.Unlock()

	turn := &protocol.Turn{ExitCode: protocol.ExitCodeNotRun, StartedAt: m.now()}
	out, execErr := m.runAdapterTurn(ctx, ad, spec, cwd, "", prompt, true, turn)
	turn.Seq = 1

	m.mu.Lock()
	defer m.mu.Unlock()
	if execErr != nil {
		// 原生 ID 缺失/不一致/三路判定失败：会话不建立、映射不落盘（探针适配决策
		// 1）；门保留（T2-R2 N1——排队者/后续创建者沿用同一门，绝不双门并行）。
		return protocol.Session{}, turn, execErr
	}
	if _, exists := m.sessions[sessionID]; exists { // 防御位：门串行下不可达
		turn.Status = protocol.TurnFailed
		turn.Error = "平台会话 ID 已存在（" + ErrSessionExists.Error() + "）"
		return protocol.Session{}, turn, ErrSessionExists
	}
	status := protocol.SessionOpen
	if _, tombstoned := m.closing[sessionID]; tombstoned {
		// 防御位：门持有期间 Close 走写者门排队、不落墓碑，正常不可达；若达
		// （门被外部拆除的极端窗口）以关闭意图为准发布 closed，绝不发布 open。
		status = protocol.SessionClosed
		delete(m.closing, sessionID)
	}
	sess := &protocol.Session{
		ID:             sessionID,
		AgentName:      agentName,
		Adapter:        spec.Adapter,
		Cwd:            cwd,
		NativeID:       out.NativeID,
		PermissionMode: spec.PermissionMode,
		Status:         status,
		TurnCount:      1,
		Turns:          []protocol.Turn{*turn},
		CreatedAt:      m.now(),
		UpdatedAt:      m.now(),
	}
	m.sessions[sess.ID] = sess
	// 门保留（发布成功：门与会话一一对应，Send/Close 沿用）；墓碑已消费。
	delete(m.closing, sessionID)
	m.persistDirty = true
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
	m.persistDirty = true
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

// closeAfterNoGateLookupHook Close「writerFor 无门结论」与「锁内复核/墓碑登记」
// 之间间隙的测试调度栅栏（T2-R3 P1 缺口原位；仓内确定性交错回归
// TestT2R3CloseBeforeGateRegistrationMustClosePublishedSession 用，复刻 R3 审查
// overlay 单行插桩）。nil = 无操作，生产路径零影响。
var closeAfterNoGateLookupHook func()

// Close 平台侧关闭会话：置 closed 并持久化；对在途轮次先等其结束（同样走写者门，
// 无 ctx 版本不设截止期）再关闭。原生线程留存在 CLI 自身存储，不受影响（无删除
// 语义，探针适配决策 3）。
//
// 生命周期门与墓碑（T2-R1 必修4；T2-R3 P1 收口）：映射尚未发布（创建在途——门
// 已登记，本调用排队等其收束；或创建未开始——无门）时，关闭意图以墓碑留痕并
// 持久化（closing 表 + sessions.json），使关闭后的创建不再发布 open（创建发布
// 前核验墓碑直接拒绝 ErrSessionClosed，不执行 CLI）。无门结论与墓碑登记在同一
// 锁域收束：取锁复核发现并发出现的门/会话（writerFor 无门后、复核前创建已登门）
// 即释放重试、改走写者门串行关闭——绝不以 ErrSessionClosed 代替实际关闭，也绝不
// 绕过在途 Send/创建的串行保护。两条无门路径都返回 ErrSessionNotFound 维持既有
// 契约（调用方幂等语义不变），但意图已成立且跨重启留存——绝不虚假关闭成功。
func (m *Manager) Close(sessionID string) (protocol.Session, error) {
	for {
		g, err := m.writerFor(sessionID)
		if err == nil {
			return m.closeWithGate(sessionID, g)
		}
		if closeAfterNoGateLookupHook != nil {
			closeAfterNoGateLookupHook() // 测试调度栅栏（P1 缺口原位），生产恒 nil
		}
		// 无门（创建未开始或从未存在）：同锁域复核 + 登记持久化墓碑，防「关闭
		// 成功后创建复活」。T2-R3 P1：writerFor 的无门结论与墓碑登记之间原本
		// 存在窗口，并发创建可在窗口内登门并发布 open 会话；旧代码把复核到的
		// 会话当「不可达防御位」虚报 ErrSessionClosed（外层吞为幂等成功），会话
		// 保持 open——关闭契约失守。
		m.mu.Lock()
		if _, gated := m.writers[sessionID]; gated {
			// 复核发现并发登门（或在途创建）：释放后重新走当前生命周期门串行
			// 关闭。门终身保留不拆除，重试一轮必命中 writerFor，循环有界。
			m.mu.Unlock()
			continue
		}
		if sess, exists := m.sessions[sessionID]; exists {
			// 防御位：有会话必有门（load/create 同建、门终身保留不拆除），正常
			// 不可达。T2-R3 P1：即使不变量破坏也绝不虚报 ErrSessionClosed（外层
			// 会吞为幂等成功），显式内部错误交调用方诊断。
			m.mu.Unlock()
			return cloneSession(sess), fmt.Errorf("sessionx: 会话 %s 存在但无写者门（内部不变量破坏），拒绝虚报关闭", sessionID)
		}
		terr := m.recordCloseTombstoneLocked(sessionID)
		m.mu.Unlock()
		if terr != nil {
			return protocol.Session{}, terr
		}
		return protocol.Session{}, ErrSessionNotFound
	}
}

// closeWithGate 持生命周期门执行关闭（Close 的门内路径）：等在途轮/在途首轮收束
// 后落定，全程不绕过单写者串行保护。
func (m *Manager) closeWithGate(sessionID string, g *sessionGate) (protocol.Session, error) {
	release, aerr := g.acquire(context.Background())
	if aerr != nil {
		return protocol.Session{}, aerr // 不可达（Background 不取消），防御位
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		// 门在手而映射不在：创建在途后失败（门保留）等窗口——落墓碑（与无门
		// 路径同一收束），让「关闭后到达的创建」不再发布 open。
		if terr := m.recordCloseTombstoneLocked(sessionID); terr != nil {
			return protocol.Session{}, terr
		}
		return protocol.Session{}, ErrSessionNotFound
	}
	if sess.Status == protocol.SessionClosed {
		// 幂等快速返回前先确认磁盘收敛（T2-R2 N2）：内存已关闭 ≠ 磁盘已关闭
		//——上次关闭落盘失败后，直接回 ErrSessionClosed 会让调用方虚报幂等
		// 成功（重启即复活）。dirty 未清零先重试落盘，成功才返回幂等回执，
		// 失败如实上抛真实错误。
		if m.persistDirty {
			if perr := m.saveSessionsLocked(); perr != nil {
				return cloneSession(sess), fmt.Errorf("sessionx: 会话此前已关闭但持久化未确认，重试落盘失败（不宣称幂等达成）: %w", perr)
			}
		}
		return cloneSession(sess), ErrSessionClosed
	}
	sess.Status = protocol.SessionClosed
	sess.UpdatedAt = m.now()
	m.persistDirty = true
	if perr := m.saveSessionsLocked(); perr != nil {
		return cloneSession(sess), fmt.Errorf("sessionx: 已关闭但持久化失败（内存态保留，下次落盘收敛）: %w", perr)
	}
	return cloneSession(sess), nil
}

// recordCloseTombstoneLocked 关闭墓碑登记（调用方持 m.mu）：内存表 + 落盘。重复
// 请求（墓碑已在）不等于已持久化（T2-R2 N2）——上次落盘失败后，dirty 未清零前
// 每次请求都重试保存并如实报错，绝不因幂等分支虚报关闭成功；成功落盘后幂等
// 零写盘返回。落盘失败时内存意图保留（尽力而为），错误如实上抛——调用方不得
// 在墓碑未持久化时宣称关闭成功。
func (m *Manager) recordCloseTombstoneLocked(sessionID string) error {
	if _, dup := m.closing[sessionID]; !dup {
		m.closing[sessionID] = true
		m.persistDirty = true
	} else if !m.persistDirty {
		return nil // 墓碑已在且已确认落盘：幂等达成，零写盘
	}
	if perr := m.saveSessionsLocked(); perr != nil {
		return fmt.Errorf("sessionx: 关闭意图已登记但持久化失败（不宣称关闭成功）: %w", perr)
	}
	return nil
}

// BindingOf agent 的规范化会话绑定（适配器与权限模式——Open 装配期定形后的实际
// 生效值；空权限已填充适配器保守默认）。首轮执行入口在 spawn CLI 前核对断言用
// （T2-R1 必修2：帧断言值 vs 本机实际配置）。不在装配表 ok=false。
func (m *Manager) BindingOf(agentName string) (adapter, permissionMode string, ok bool) {
	spec, ok := m.agents[agentName] // 只读表（Open 后不变），同 lookupAgent 无锁纪律
	if !ok {
		return "", "", false
	}
	return spec.Adapter, spec.PermissionMode, true
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

// HasAgent agent 名是否在装配表（入口层派单前置校验用；只读，无锁）。
func (m *Manager) HasAgent(agentName string) bool {
	_, ok := m.adapters[agentName]
	return ok
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
