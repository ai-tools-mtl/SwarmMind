// node 本地 API 的管理面端点（Loop 7，INTERACTION 客户端三旅程）：
//   - GET   /api/v1/local/agents                   旅程①服务提供者：本机 ExecProfile
//     视图 + 今日任务数（本机主人视角，执行配置可见——ExecProfile 永不出本机，
//     出本机的 register/card_update 仍只携 AgentCard）；
//   - PATCH /api/v1/local/agents/{name}/exposed    接入开关：立即生效（运行时 +
//     持久化 + card_update 上报），已排队任务继续执行至终态；
//   - GET   /api/v1/local/status                   旅程③运维：连接/epoch/账本三态/
//     最近同步（信箱余量是 hub 侧事实，node 不代理展示）；
//   - GET   /api/v1/local/tasks                    旅程②消费者：本机执行任务全量
//     清单（账本全量驱动：hub 下发+本机派单一律在列，逐条标来源，2026-10-26
//     全量审计裁决）。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// localAgentView 本机目录单条：完整 ExecProfile（本机主人视角）+ 今日执行数。
type localAgentView struct {
	protocol.ExecProfile
	TodayTasks int `json:"today_tasks"`
}

// handleLocalAgents GET /api/v1/local/agents：本机 ExecProfile 列表 + 今日任务数
// （账本中该 agent 名下、创建于今天（UTC）的任务条数——旅程①的调用统计）。
func (s *Server) handleLocalAgents(w http.ResponseWriter, r *http.Request) {
	profiles := s.effectiveProfiles()
	views := make([]localAgentView, 0, len(profiles))
	today := protocol.NowUTC()
	for _, p := range profiles {
		views = append(views, localAgentView{ExecProfile: p, TodayTasks: s.todayTaskCount(p.Name, today)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": s.cfg.NodeID, "agents": views})
}

// todayTaskCount 账本中目标 agent 今日（UTC 自然日）的任务条数（快照读，无事务）。
func (s *Server) todayTaskCount(agent string, now time.Time) int {
	if s.cfg.Ledger == nil {
		return 0
	}
	n := 0
	for _, e := range s.cfg.Ledger.View() {
		if e.Task.TargetAgent != agent {
			continue
		}
		y, m, d := e.Task.CreatedAt.Date()
		ny, nm, nd := now.Date()
		if y == ny && m == nm && d == nd {
			n++
		}
	}
	return n
}

// exposedRequest PATCH exposed 请求体。
type exposedRequest struct {
	Exposed bool `json:"exposed"`
}

// handleSetExposed PATCH /api/v1/local/agents/{name}/exposed：开关立即生效——
// 运行时 + 持久化（重启保持）+ card_update 上报 hub（SPEC 4.2 全量能力表）。
// 已有任务不受影响（继续执行至终态）；hub 不可达时 hub_synced=false，控制台按
// 「待同步」呈现，重连注册携全量能力表自动收敛（INTERACTION 四.3 开关三态）。
func (s *Server) handleSetExposed(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Agents == nil {
		writeErr(w, http.StatusInternalServerError, "internal", "未装配 agent 状态（装配方须传入 Agents）")
		return
	}
	var req exposedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {\"exposed\":bool}")
		return
	}
	name := r.PathValue("name")
	p, err := s.cfg.Agents.SetExposed(name, req.Exposed)
	if errors.Is(err, ErrAgentNotFound) {
		writeErr(w, http.StatusNotFound, protocol.CodeAgentNotFound, "agent 不在本机配置中: "+name)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "开关持久化失败（运行时状态未变更，可重试）: "+err.Error())
		return
	}
	synced := true
	if s.cfg.CardsReporter != nil {
		synced = s.cfg.CardsReporter(s.cfg.Agents.Cards()) == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "exposed": p.Exposed, "hub_synced": synced,
	})
}

// orchestratorRequest PATCH orchestrator 请求体。
type orchestratorRequest struct {
	Orchestrator bool `json:"orchestrator"`
}

// handleSetAgentOrchestrator PATCH /api/v1/local/agents/{name}/orchestrator：
// 统筹载体声明开关（2026-10-28 客户端声明制）——运行时 + 持久化（重启保持）+
// card_update 上报 hub（hub 全网汇聚：恰一声明生效，多声明冲突告警回退名字约定）。
// 同机互斥由 SetAgentOrchestrator 保证（已有其他声明则 409）。
func (s *Server) handleSetAgentOrchestrator(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Agents == nil {
		writeErr(w, http.StatusInternalServerError, "internal", "未装配 agent 状态（装配方须传入 Agents）")
		return
	}
	var req orchestratorRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {\"orchestrator\":bool}")
		return
	}
	name := r.PathValue("name")
	p, err := s.cfg.Agents.SetAgentOrchestrator(name, req.Orchestrator)
	if errors.Is(err, ErrAgentNotFound) {
		writeErr(w, http.StatusNotFound, protocol.CodeAgentNotFound, "agent 不在本机配置中: "+name)
		return
	}
	if err != nil {
		writeErr(w, http.StatusConflict, "orchestrator_conflict", err.Error())
		return
	}
	// hub_synced 真实语义（P1-02 残余修正，与 prime/declare 应答同口径）：reporter
	// 已装配（组网）→ 能力表上报实况（true=已提交上报；false=待同步，重连随注册
	// 收敛）；未装配 reporter（standalone 未组网）→ 字段缺省——无上报环节，控制台
	// 按「未组网」呈现，绝不冒充已同步（旧初值 true 使 standalone 重试链谎报
	// 「已提交上报 hub」）。选缺省而非 false：declare 端同形，控制台消费端以
	// 「字段是否为 boolean」判定，缺省=未组网语义在双消费点（完成页/状态提示）
	// 与初始渲染基线一致。
	resp := map[string]any{"name": name, "orchestrator": p.Orchestrator}
	if s.cfg.CardsReporter != nil {
		resp["hub_synced"] = s.cfg.CardsReporter(s.cfg.Agents.Cards()) == nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// localStatus 本机状态总览（旅程③）。node 只呈现本机事实：形态/总理声明/
// 连接/epoch/账本/最近同步；信箱余量与投递窗口是 hub 侧事实，不在此代理展示
//（方案 v2 D6）。
type localStatus struct {
	NodeID string `json:"node_id"`
	// Mode 运行形态（setup|standalone|networked，方案 3.1 三态）。
	Mode string `json:"mode"`
	// ServerVersion 二进制版本（cmd/node -X main.version 注入；本机控制台
	// 页脚数据源，与 swarmmind-node --version 同源）。
	ServerVersion string `json:"server_version"`
	// PrimeAgent 本机唯一总理声明（node.yaml prime_agent；空 = 未钦定，
	// 编排接口未激活——控制台据此展示「本机总理：〈名〉」或提示，方案 3.2.1）。
	PrimeAgent string `json:"prime_agent"`
	Online     bool   `json:"online"`
	Epoch      int64  `json:"epoch"`
	// LastSyncAt 最近一次注册成功时刻（最近同步）；从未注册为 null。
	LastSyncAt *time.Time `json:"last_sync_at"`
	// NotifyAvailable 通知渠道管理面可用（2026-10-26 起三模式全装配 Dispatcher，
	// 正常构造恒 true；判定随装配态而非模式硬编码——控制台据此决定是否渲染
	// 「通知」区块）。
	NotifyAvailable bool `json:"notify_available"`
	// TerminalRetentionSec 账本终态保留时长生效值（秒；node.yaml terminal_retention
	// 经 protocol.ParseTerminalRetention 解析，service.TerminalRetention() 读侧，
	// maturity-retention-config）。0 = 永不自动清理（terminal_retention: 0s 显式
	// 语义）；默认 86400（24h）。Service 未装配（旧测试构造方）回落协议默认。
	TerminalRetentionSec int `json:"terminal_retention_sec"`
	// Ledger 账本三态计数（accepted/started/finished，SPEC 4.8）。
	Ledger map[string]int `json:"ledger"`
}

// handleLocalStatus GET /api/v1/local/status：本机运维视图。
func (s *Server) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	st := localStatus{
		NodeID:          s.cfg.NodeID,
		Mode:            s.mode(),
		ServerVersion:   s.cfg.Version,
		PrimeAgent:      s.primeAgent(),
		Online:          s.online(),
		Epoch:           s.epoch(),
		NotifyAvailable: s.cfg.Notify != nil,
		Ledger:          s.ledgerCounts(),
	}
	if s.cfg.Service != nil {
		st.TerminalRetentionSec = int(s.cfg.Service.TerminalRetention() / time.Second)
	} else {
		st.TerminalRetentionSec = int(protocol.TerminalRetention / time.Second) // 未装配回落默认（旧构造方不感知）
	}
	if s.cfg.LastRegisteredAt != nil {
		if at, ok := s.cfg.LastRegisteredAt(); ok {
			st.LastSyncAt = &at
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// mode 运行形态（Setup 装配优先；未装配 Mode 按 networked 呈现）。
func (s *Server) mode() string {
	if s.cfg.Setup {
		return ModeSetup
	}
	if s.cfg.Mode == ModeStandalone {
		return ModeStandalone
	}
	return ModeNetworked
}

// online / epoch / ledgerCounts 状态字段装配。
func (s *Server) online() bool {
	if s.cfg.Online == nil {
		return false
	}
	return s.cfg.Online()
}

func (s *Server) epoch() int64 {
	if s.cfg.Epoch == nil {
		return 0
	}
	return s.cfg.Epoch()
}

// ledgerCounts 账本三态计数快照（accepted/started/finished；恒含三键，前端免判）。
func (s *Server) ledgerCounts() map[string]int {
	counts := map[string]int{
		string(ledger.StateAccepted): 0,
		string(ledger.StateStarted):  0,
		string(ledger.StateFinished): 0,
	}
	if s.cfg.Ledger != nil {
		for _, e := range s.cfg.Ledger.View() {
			counts[string(e.State)]++
		}
	}
	return counts
}

// 分页参数默认与上限（codex 审查④容量边界）：账本清理未接线（D2，审计保留
// 策略待产品裁决）规模只增不减，全量单响应在万行级已达百毫秒级——列表首屏与
// 轮询取首页（默认 200、上限 1000），更早历史经 offset 翻页；响应携 total（合并
// 去重后总行数）供前端「已载 X / 共 Y 条」与「加载更早」。
const (
	tasksPageDefault = 200
	tasksPageMax     = 1000
)

// parseTasksPage 解析 limit/offset 查询参数：缺省 200/0；非法值报 400；limit 超
// 上限收敛到 tasksPageMax（宽容——上限是保护而非契约边界）。
func parseTasksPage(r *http.Request) (limit, offset int, err error) {
	q := r.URL.Query()
	limit, offset = tasksPageDefault, 0
	if v := q.Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n <= 0 {
			return 0, 0, errors.New("limit 须为正整数")
		}
		limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, errors.New("offset 须为非负整数")
		}
		offset = n
	}
	if limit > tasksPageMax {
		limit = tasksPageMax
	}
	return limit, offset, nil
}

// handleLocalTasks GET /api/v1/local/tasks：本机执行任务全量清单（新→旧，
// limit/offset 分页 + total 总数）。
// 全量审计裁决（2026-10-26）：机器主人对这台机器执行过什么有全量审计权——数据源
// 从「本机派单留痕 TaskLog」改为「本机账本 Ledger 全量驱动」，hub 下发任务不再
// 不可见：
//   - 账本行：本机执行过的一切任务（local=本机派单、hub=hub 下发）逐条在列，
//     origin 取条目 Origin（空归 hub，旧版账本文件兼容），status 按 ledger.TaskView
//     同源映射出视角状态（轻量投影不取整单深拷贝，codex 审查④）——列表行自带
//     状态，前端不再逐条补查；
//   - 留痕补尾：TaskLog 中账本已不存在的条目（本机派单历史留痕；账本保留策略
//     接线清理后出现，清理当前未接线——D2 待产品裁决）补在列表尾——origin=local、
//     无 status（历史留痕全部是本机派单，语义无例外；详情查询 404 = 任务不在账本）；
//   - 合并按 created_at 新→旧排序（同刻按 task_id 稳定序），账本行优先（同 id
//     不重），排序在分页切片前完成（跨页次序一致）。Ledger 未装配时退化为纯留痕行。
//
// 快照顺序（codex 审查①）：先 TaskLog 后 Ledger。提交路径序为「先入账、后留痕」
// （handleSubmit → SubmitLocalDirect → TaskLog.Record）——若先读 Ledger 后读
// TaskLog，两次快照之间落地的新提交命中「留痕有、账本无」，被误判为已清理的
// 留痕行（无状态=已过保留期）；调换后任一交错下该任务至多出列一次且必为带状态
// 的账本行：留痕快照含它 ⇒ 入账必已发生 ⇒ 账本快照必含它（账本行优先去重）；
// 留痕快照不含它而账本快照含 ⇒ 账本行；两者皆不含 ⇒ 本响应不出现（下轮轮询可见）。
func (s *Server) handleLocalTasks(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parseTasksPage(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	traces := s.taskLogSnapshot()
	if s.tasksRaceHook != nil {
		s.tasksRaceHook() // 同包测试专用：在两次快照之间确定性注入一次提交
	}
	views := []taskListItem{}
	inLedger := map[string]bool{}
	if s.cfg.Ledger != nil {
		for _, e := range s.cfg.Ledger.View() {
			inLedger[e.Task.ID] = true
			origin := e.Origin
			if origin == "" {
				origin = ledger.OriginHub // 防御：未归一条目按 hub 处理（同账本 normalized 口径）
			}
			views = append(views, taskListItem{
				TaskID: e.Task.ID, TraceID: e.Task.TraceID,
				TargetNode: e.Task.TargetNode, TargetAgent: e.Task.TargetAgent,
				IdemKey:  e.Task.IdemKey,
				CreatedAt: e.Task.CreatedAt,
				Origin:    origin,
				Status:    ledgerListStatus(e),
			})
		}
	}
	for _, e := range traces {
		if inLedger[e.TaskID] {
			continue // 账本行已覆盖：不重复出列
		}
		views = append(views, taskListItem{
			TaskID: e.TaskID, TraceID: e.TraceID,
			TargetNode: e.TargetNode, TargetAgent: e.TargetAgent,
			IdemKey:  e.IdemKey,
			CreatedAt: e.CreatedAt,
			Origin:    ledger.OriginLocal,
		})
	}
	total := len(views)
	sort.Slice(views, func(i, j int) bool {
		if !views[i].CreatedAt.Equal(views[j].CreatedAt) {
			return views[i].CreatedAt.After(views[j].CreatedAt)
		}
		return views[i].TaskID < views[j].TaskID
	})
	if offset >= len(views) {
		views = []taskListItem{}
	} else if end := offset + limit; end < len(views) {
		views = views[offset:end]
	} else {
		views = views[offset:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": views, "total": total})
}

// taskLogSnapshot 留痕全量快照（TaskLog 未装配为空表）。
func (s *Server) taskLogSnapshot() []TaskLogEntry {
	if s.cfg.TaskLog == nil {
		return nil
	}
	return s.cfg.TaskLog.List()
}

// ledgerListStatus 列表行状态投影——与 ledger.TaskView 的状态映射同源：
// accepted→acknowledged、started→running、finished→result.status（缺 result 防御
// 为 failed）。列表不取 TaskView（按行深拷贝整单），只投影状态一个字段。
func ledgerListStatus(e ledger.Entry) string {
	switch e.State {
	case ledger.StateStarted:
		return string(protocol.StatusRunning)
	case ledger.StateFinished:
		if e.Result != nil {
			return string(e.Result.Status)
		}
		return string(protocol.StatusFailed)
	default:
		return string(protocol.StatusAcknowledged)
	}
}

// taskListItem 「我的任务」列表行（轻量投影：列表所需最小字段，codex 审查④；
// submitted_by 等详情字段经 GET /api/v1/tasks/{id} 取）：账本全量行（带 status）
// ∪ 留痕补尾行（账本中已不存在，status 为空）。origin 供前端区分双路径
//（local=产物/核对在本机；hub=经 hub 链路）与来源列展示。
type taskListItem struct {
	TaskID      string `json:"task_id"`
	TraceID     string `json:"trace_id"`
	TargetNode  string `json:"target_node"`
	TargetAgent string `json:"target_agent"`
	IdemKey     string `json:"idem_key,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Origin 任务来源（local|hub；ledger.Origin）。
	Origin ledger.Origin `json:"origin"`
	// Status 视角状态（账本行：acknowledged|running|completed|failed|
	// needs_review|cancelled）；留痕补尾行（账本中已不存在）为空。
	Status string `json:"status,omitempty"`
}
