// node 本地 API 的管理面端点（Loop 7，INTERACTION 客户端三旅程）：
//   - GET   /api/v1/local/agents                   旅程①服务提供者：本机 ExecProfile
//     视图 + 今日任务数（本机主人视角，执行配置可见——ExecProfile 永不出本机，
//     出本机的 register/card_update 仍只携 AgentCard）；
//   - PATCH /api/v1/local/agents/{name}/exposed    接入开关：立即生效（运行时 +
//     持久化 + card_update 上报），已排队任务继续执行至终态；
//   - GET   /api/v1/local/status                   旅程③运维：连接/epoch/账本三态/
//     最近同步（信箱余量是 hub 侧事实，node 不代理展示）；
//   - GET   /api/v1/local/tasks                    旅程②消费者：本机提交任务留痕。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
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

// localStatus 本机状态总览（旅程③）。node 只呈现本机事实：形态/总理声明/
// 连接/epoch/账本/最近同步；信箱余量与投递窗口是 hub 侧事实，不在此代理展示
//（方案 v2 D6）。
type localStatus struct {
	NodeID string `json:"node_id"`
	// Mode 运行形态（setup|standalone|networked，方案 3.1 三态）。
	Mode string `json:"mode"`
	// PrimeAgent 本机唯一总理声明（node.yaml prime_agent；空 = 未钦定，
	// 编排接口未激活——控制台据此展示「本机总理：〈名〉」或提示，方案 3.2.1）。
	PrimeAgent string         `json:"prime_agent"`
	Online     bool           `json:"online"`
	Epoch      int64          `json:"epoch"`
	// LastSyncAt 最近一次注册成功时刻（最近同步）；从未注册为 null。
	LastSyncAt *time.Time `json:"last_sync_at"`
	// Ledger 账本三态计数（accepted/started/finished，SPEC 4.8）。
	Ledger map[string]int `json:"ledger"`
}

// handleLocalStatus GET /api/v1/local/status：本机运维视图。
func (s *Server) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	st := localStatus{
		NodeID:     s.cfg.NodeID,
		Mode:       s.mode(),
		PrimeAgent: s.cfg.PrimeAgent,
		Online:     s.online(),
		Epoch:      s.epoch(),
		Ledger:     s.ledgerCounts(),
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

// handleLocalTasks GET /api/v1/local/tasks：本机提交任务留痕（新→旧）。任务
// 当前状态由前端按 task_id 走 GET /api/v1/tasks/{id}（task_query 帧）补查——
// hub 是任务状态真相源，日志不缓存状态，避免两处真相。
func (s *Server) handleLocalTasks(w http.ResponseWriter, r *http.Request) {
	entries := []TaskLogEntry{}
	if s.cfg.TaskLog != nil {
		entries = s.cfg.TaskLog.List()
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": entries})
}
