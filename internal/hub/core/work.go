// Work 工作单（P5 W1 / RFC-work-resume 五 W1 行）：模型+事务+账本+创建幂等+
// 只读查询。绑定派发/接管/终态回写/恢复扫描见 work_dispatch.go（W2）；收集/
// 关闭随 W3 交付；本文件只覆盖建单与读取。
//
// 持久化形态（对齐 ControlBatch 模板，W1 裁剪）：建单为**单事务原子**——工作单
// 全量（步骤全 pending）与 work_created 事件在同一 store.Update 内落盘，无
// 「骨架→逐项回写」中间窗口，故无独立恢复扫描；W2 派发绑定（任务落库与
// attempt/step 回写跨事务）沿 ControlBatch 两阶段+recoverBatches 模板补恢复。
//
// 创建幂等（RFC 二.2）：同 goal+steps 内容指纹重复调用返回同单（响应丢失重试
// 安全）。指纹即寻址键（无调用方自选键，无 409 冲突路径）：State.WorkIdems
// 指纹 → work_id，预检 + 建单事务内重检，并发同指纹由单写串行化收敛。
// 幂等命中零写入零记账（重复回调不重复记账，事实与事件一致）。
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// ErrInvalidWorkSteps 步骤计划不合法（RFC 二.2：引用有效/无环/级联上限等；
// HTTP 400 invalid_work_steps）。errors.Is 判定，包装错误含定位文案。
var ErrInvalidWorkSteps = errors.New("core: 工作单步骤计划不合法")

// Work 计划准入限制（对齐任务依赖 4.12 口径取值；步骤数为本批新增护栏，
// RFC 未定具体值，取与信箱配额同量级防快照膨胀）。
const (
	// MaxWorkStepDepth 步骤依赖深度上限：沿 depends_on 上溯最长链边数 ≤ 4。
	MaxWorkStepDepth = protocol.MaxDependsDepth
	// MaxWorkStepCascade 步骤依赖级联上限：去重上游步骤总数 ≤ 8。
	MaxWorkStepCascade = protocol.MaxDependsCascade
	// MaxWorkSteps 单工作单步骤数上限。
	MaxWorkSteps = 64
)

// Work 事件类别（WorkEvent.Kind；随批次演进补充：W2 attempt_recorded/takeover，
// W3 collect/closed）。
const WorkEventCreated = "work_created"

// WorkStepInput 建单请求步骤声明（seq 由 hub 按声明序赋 1..N，请求不携带）。
type WorkStepInput struct {
	// Desc 步骤描述（做什么；必填）。
	Desc string `json:"desc"`
	// TargetNode 执行节点（必填；派发时经 Submit 链准入校验）。
	TargetNode string `json:"target_node"`
	// TargetAgent 执行 agent 名（必填）。
	TargetAgent string `json:"target_agent"`
	// DependsOn 依赖的前置步骤序号（1 起的声明序位置）。
	DependsOn []int64 `json:"depends_on,omitempty"`
}

// WorkCreateRequest 建单请求。
type WorkCreateRequest struct {
	// Goal 工作目标（一句话全貌；必填）。
	Goal string `json:"goal"`
	// PrimeTerm 建单任期逻辑归属（可选；W2 接管事务为权威改写点）。
	PrimeTerm string `json:"prime_term,omitempty"`
	// Steps 步骤计划（1..MaxWorkSteps 条）。
	Steps []WorkStepInput `json:"steps"`
	// CreatedBy 动作方审计标签（缺省 mcp；建单入口 v1 仅 Prime/MCP，
	// RFC 六.六裁决 1。逻辑归属非身份证明）。
	CreatedBy string `json:"created_by,omitempty"`
	// OriginTask 来源主任务（DESIGN-butler 方案二，2026-10-28 客户端管家拆解
	// 挂接；可选）：管家建单时携带派给自己的复合任务 task_id（SWARM_TASK_ID
	// 即得）。非空时校验：任务存在且 (TargetNode,TargetAgent) 为目录中带
	// Orchestrator 声明的能力卡（软校验，防乱挂；不校验在线）；幂等指纹含
	// origin（不同主任务相同计划不撞单——DESIGN-butler R3' P1-3 闭合）。
	OriginTask string `json:"origin_task,omitempty"`
}

// WorkView 工作单详情视图：全量记录 + 账本游标读取（事件只返回 seq > sinceEvents
// 的升序尾部；不另增查询端点，RFC 三：work_get 含事件游标读取）。
type WorkView struct {
	store.Work
	// PrunedBeforeEvents 滚动裁剪已丢事件的最大 seq（现存最旧 Seq-1；从未裁剪
	// 为 0）——sinceEvents 小于该值说明请求区间有拉不回的缺口，调用方应据此对
	// 齐游标（同群消息 pruned_before_seq 口径）。
	PrunedBeforeEvents int64 `json:"pruned_before_events"`
	// TasksGone 尝试绑定的任务 ID 已过 24h 保留期清理的列表（W3 接口补全，
	// 仅 GetWork 读档路径计算）：Work 独立记忆仍解释终局（尝试 outcome/幂等键/
	// 内容指纹随单保留），本列表标注「任务详情为何查不到」。写入路径响应
	//（建单/接管/收集）不携带——返回的是写入视角现状，非解释面注解。
	TasksGone []string `json:"tasks_gone,omitempty"`
}

// WorkSummary 工作单列表摘要（不含 Events/Steps/Attempts 明细，列表面轻量）。
type WorkSummary struct {
	ID        string `json:"id"`
	Goal      string `json:"goal"`
	PrimeTerm string `json:"prime_term,omitempty"`
	Status    string `json:"status"`
	Steps     int    `json:"steps"`
	// DoneSteps 已完成（done）步骤数（W3 管理台进度 x/y 用；W3 接口补全）。
	DoneSteps int       `json:"done_steps"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	// ClosedAt 终态时刻（未终态 nil 不输出；指针承载 omitempty——time.Time
	// 结构体零值不触发 omitempty，对齐 store.Work.ClosedAt 口径）。
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// workStep 规范化后的指纹/落库载体（TrimSpace 后；desc/target 非空已由校验保证）。
type workStep struct {
	Seq         int64   `json:"seq"`
	Desc        string  `json:"desc"`
	TargetNode  string  `json:"target_node"`
	TargetAgent string  `json:"target_agent"`
	DependsOn   []int64 `json:"depends_on,omitempty"`
}

// workContent 建单内容指纹载体：goal + 规范化步骤序表（字段名与 json tag 固定，
// 保证指纹跨版本稳定；同 mailbox/batch 指纹口径）。
type workContent struct {
	Goal  string     `json:"goal"`
	// OriginTask 参与指纹（DESIGN-butler 方案二）：omitempty 保证无 origin 的
	// 建单指纹与历史字节一致（既有 WorkIdems 映射零失效）；有 origin 时不同
	// 主任务的相同计划不撞单（幂等命名空间，R3' P1-3）。
	OriginTask string     `json:"origin_task,omitempty"`
	Steps []workStep `json:"steps"`
}

// normalizeWorkSteps 规范化步骤声明：TrimSpace 全文本字段、赋 seq 1..N、依赖
// 列表整形（空表归一 nil，声明序保留——与任务依赖 normDeps 同口径：序变即不同
// 内容）。校验在 validateWorkPlan（错误定位需原始下标，两步分离）。
func normalizeWorkSteps(in []WorkStepInput) []workStep {
	out := make([]workStep, len(in))
	for i, s := range in {
		deps := s.DependsOn
		if len(deps) == 0 {
			deps = nil
		} else {
			deps = append([]int64(nil), s.DependsOn...)
		}
		out[i] = workStep{
			Seq:         int64(i + 1),
			Desc:        strings.TrimSpace(s.Desc),
			TargetNode:  strings.TrimSpace(s.TargetNode),
			TargetAgent: strings.TrimSpace(s.TargetAgent),
			DependsOn:   deps,
		}
	}
	return out
}

// workContentHash 建单内容指纹：canonical JSON 的 SHA-256 hex。
func workContentHash(goal string, steps []workStep, originTask string) string {
	b, err := json.Marshal(workContent{Goal: goal, OriginTask: originTask, Steps: steps})
	if err != nil {
		// 纯标量+slice 结构，不可达；防御性返回空串（调用方按不同内容处理）
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// storedWorkHash 已持久化工作单 → 同口径指纹（用于幂等命中复核；与建单指纹
// 同一变换，规则化字段使重算稳定）。
func storedWorkHash(w store.Work) string {
	steps := make([]workStep, len(w.Steps))
	for i, s := range w.Steps {
		deps := s.DependsOn
		if len(deps) == 0 {
			deps = nil
		}
		steps[i] = workStep{Seq: s.Seq, Desc: s.Desc, TargetNode: s.TargetNode, TargetAgent: s.TargetAgent, DependsOn: deps}
	}
	return workContentHash(w.Goal, steps, w.OriginTask)
}

// validateWorkPlan 步骤计划准入校验（RFC 二.2：引用有效/无环/级联上限；唯一性
// 由 hub 赋 seq 构造保证）。计划自包含（不跨 Work 引用），在事务外预检 +
// 建单事务内重检（同 submit 依赖准入双检口径——W1 步骤不引用外部可变实体，
// 双检纯防御未来扩展）。规则：
//
//  1. goal 非空；步骤数 1..MaxWorkSteps；
//  2. 逐步 desc/target_node/target_agent 非空；
//  3. 依赖引用有效：∈[1,N] 且非自引用且步内不重复；
//  4. 依赖环拒绝：沿 depends_on 的 DFS 链上回到任一已在栈内步骤；
//  5. 深度（最长链边数）≤ MaxWorkStepDepth；级联（去重上游步骤数）≤
//     MaxWorkStepCascade。
func validateWorkPlan(goal string, steps []workStep) error {
	if strings.TrimSpace(goal) == "" {
		return fmt.Errorf("%w: goal 必填", ErrInvalid)
	}
	if len(steps) == 0 {
		return fmt.Errorf("%w: steps 不能为空", ErrInvalidWorkSteps)
	}
	if len(steps) > MaxWorkSteps {
		return fmt.Errorf("%w: 步骤数 %d 超上限 %d", ErrInvalidWorkSteps, len(steps), MaxWorkSteps)
	}
	n := int64(len(steps))
	bySeq := make(map[int64]int, len(steps)) // seq → 下标（seq 恒 = 下标+1，映射纯为可读）
	for i, s := range steps {
		if s.Desc == "" || s.TargetNode == "" || s.TargetAgent == "" {
			return fmt.Errorf("%w: 第 %d 步 desc/target_node/target_agent 均必填", ErrInvalidWorkSteps, i+1)
		}
		bySeq[s.Seq] = i
	}
	for i, s := range steps {
		seen := make(map[int64]bool, len(s.DependsOn))
		for _, d := range s.DependsOn {
			switch {
			case d < 1 || d > n:
				return fmt.Errorf("%w: 第 %d 步依赖 %d 越界（有效 1..%d）", ErrInvalidWorkSteps, i+1, d, n)
			case d == s.Seq:
				return fmt.Errorf("%w: 第 %d 步自引用", ErrInvalidWorkSteps, i+1)
			case seen[d]:
				return fmt.Errorf("%w: 第 %d 步重复依赖 %d", ErrInvalidWorkSteps, i+1, d)
			}
			seen[d] = true
		}
	}
	// 深度（最长链边数）记忆化 DFS + 环检测（口径同任务依赖 validateDepends：
	// 层序 BFS 去重会截断长路径，深度必须逐节点最长链计算）。
	depthMemo := make(map[int64]int, n)
	onPath := make(map[int64]bool, n)
	var depthOf func(seq int64) (int, error)
	depthOf = func(seq int64) (int, error) {
		if d, ok := depthMemo[seq]; ok {
			return d, nil
		}
		if onPath[seq] {
			return 0, fmt.Errorf("%w: 依赖环（第 %d 步链回自身）", ErrInvalidWorkSteps, seq)
		}
		onPath[seq] = true
		defer delete(onPath, seq)
		longest := 0
		for _, d := range steps[bySeq[seq]].DependsOn {
			dv, err := depthOf(d)
			if err != nil {
				return 0, err
			}
			if dv+1 > longest {
				longest = dv + 1
			}
		}
		depthMemo[seq] = longest
		return longest, nil
	}
	for _, s := range steps {
		d, err := depthOf(s.Seq)
		if err != nil {
			return err
		}
		if d > MaxWorkStepDepth {
			return fmt.Errorf("%w: 第 %d 步依赖深度超过上限 %d", ErrInvalidWorkSteps, s.Seq, MaxWorkStepDepth)
		}
	}
	// 级联（去重上游步骤数）：BFS + 全局去重（深度把关已由上方完成）。
	for _, s := range steps {
		visited := make(map[int64]bool, n)
		visited[s.Seq] = true
		frontier := append([]int64(nil), s.DependsOn...)
		cascade := 0
		for len(frontier) > 0 {
			var next []int64
			for _, d := range frontier {
				if visited[d] {
					continue
				}
				visited[d] = true
				cascade++
				if cascade > MaxWorkStepCascade {
					return fmt.Errorf("%w: 第 %d 步依赖级联超过上限 %d", ErrInvalidWorkSteps, s.Seq, MaxWorkStepCascade)
				}
				next = append(next, steps[bySeq[d]].DependsOn...)
			}
			frontier = next
		}
	}
	return nil
}

// orchestratorCardInState 统筹卡判定（State 副本上的纯函数形态）：口径与
// IsOrchestratorCard 一致（exposed 能力卡 + Orchestrator 声明，精确
// (node,agent) 匹配），但作用于传入的 State——CreateWork 建单事务内用它校验
// origin，校验与落单消费同一事务快照（R1-01：消除「目录快照校验通过 → 落单
// 前声明被撤销」的跨快照窗口）。两函数的一致性由
// TestOrchestratorCardStateViewAgree 锁定。
func orchestratorCardInState(st *store.State, nodeID, agentName string) bool {
	for _, card := range st.Nodes[nodeID].Agents {
		if card.Name == agentName && card.Exposed && card.Orchestrator {
			return true
		}
	}
	return false
}

// CreateWork 建单（RFC 二.2 步骤 1）：规范化+校验 → 幂等预检 → 单事务落库
// （工作单全量 + work_created 事件同事务，事实与事件一致）→ 详情视图。
//
// actor 服务端验证的鉴权主体（REST 层自鉴权中间件取得：admin 会话 /
// pat:<令牌名>；RFC 六.2 裁决——事件账本以独立字段 Principal 落账，与请求自报
// 的 created_by/prime_term 分开存储，自报标签不承担身份证明）。传空则事件无
// 主体字段（仅限直连 core 的内部调用方；REST 入口恒非空）。
//
// 返回 dup=true 表示幂等命中（同 goal+steps 指纹）：返回原单、零写入零记账。
// origin 软校验只在「确定新建」分支执行且用写事务内同一 State 副本（R1-01）：
// 幂等命中（快读或事务内复核）先返回原单回执、不重校验——建单成功后声明被
// 撤销或主任务先于工作单清理时，原样重试仍得 dup 回执而非 ErrInvalid（SPEC
// 规则 16「幂等回执补拉不重校验」的字面兑现）。建单事务失败（校验拒绝或存储
// 故障）返回错误：内存与磁盘零变化，重试安全（落盘失败不占 seq——副本整体
// 丢弃，重试重新分配 ID，指纹亦未登记）。
func (c *Core) CreateWork(req WorkCreateRequest, actor string) (*WorkView, bool, error) {
	goal := strings.TrimSpace(req.Goal)
	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = "mcp" // 建单入口 v1 仅 Prime/MCP（RFC 六.六裁决 1）
	}
	steps := normalizeWorkSteps(req.Steps)
	if err := validateWorkPlan(goal, steps); err != nil {
		return nil, false, err
	}
	// origin_task 规范化（DESIGN-butler 方案二/R3' P1-4/R1-01）；软校验延后到
	// 建单事务内「确定新建」分支（orchestratorCardInState + st.Tasks）。
	origin := strings.TrimSpace(req.OriginTask)
	fingerprint := workContentHash(goal, steps, origin)

	// 幂等预检（快读路径；权威判定在建单事务内重检）。命中复核存储单的重算
	// 指纹（对齐 storedBatchHash 口径）：指纹即寻址键，正常由构造保证一致，
	// 重算不合（快照手改/迁移异常）视为未命中，按新单登记覆盖映射。
	if prev, ok := c.st.View().WorkIdems[fingerprint]; ok {
		if w, ok := c.st.View().Works[prev]; ok && storedWorkHash(w) == fingerprint {
			return workViewOf(w, 0), true, nil
		}
	}

	now := c.now()
	var (
		dupID string
		wv    *WorkView
	)
	err := c.st.Update(func(st *store.State) error {
		if prev, ok := st.WorkIdems[fingerprint]; ok {
			if w, ok := st.Works[prev]; ok && storedWorkHash(w) == fingerprint {
				dupID = prev
				return nil
			}
			// 指纹悬空或与存储单不符（Work 无删除路径，不可达；防御）：
			// 按新单登记覆盖映射
		}
		// 确定新建：origin 软校验（R1-01 校验时点契约）。挂接锚点 = 原任务派发
		// 目标 (TargetNode,TargetAgent) 精确匹配同一事务副本目录中带 Orchestrator
		// 声明的能力卡；不校验在线；校验先于本事务一切写入，拒绝即副本整体丢弃。
		// 目录漂移（声明事后撤销）不追溯已建单。旧 node 缺声明 → 无标记 → 拒绝
		// 并指路升级。
		if origin != "" {
			task, ok := st.Tasks[origin]
			if !ok {
				return fmt.Errorf("%w: origin_task %q 不存在（须为账本内主任务 ID；任务 24h 保留期后已清理不可挂接）", ErrInvalid, origin)
			}
			if !orchestratorCardInState(st, task.TargetNode, task.TargetAgent) {
				return fmt.Errorf("%w: origin_task %q 的派发目标 %s/%s 不是带统筹声明的能力卡（声明入口在各机器 node 控制台总览页「本机管家 · 形态」卡）", ErrInvalid, origin, task.TargetNode, task.TargetAgent)
			}
		}
		w := store.Work{
			ID:         protocol.NewWorkID(),
			Goal:       goal,
			OriginTask: origin,
			PrimeTerm:  strings.TrimSpace(req.PrimeTerm),
			Status:    store.WorkStatusPlanning,
			Steps:     make([]store.WorkStep, len(steps)),
			CreatedAt: now,
		}
		if w.PrimeTerm != "" {
			// 建单任期播种历史使用集（接管复用拒绝的基线，见 TakeoverWork）
			w.TermsUsed = []string{w.PrimeTerm}
		}
		for i, s := range steps {
			deps := s.DependsOn
			if len(deps) == 0 {
				deps = nil
			}
			w.Steps[i] = store.WorkStep{
				Seq:         s.Seq,
				Desc:        s.Desc,
				TargetNode:  s.TargetNode,
				TargetAgent: s.TargetAgent,
				DependsOn:   deps,
				Status:      store.StepStatusPending,
			}
		}
		// 事实与事件同事务记账（appendWorkEvent 内含滚动裁剪与 EventSeq 推进）
		appendWorkEvent(&w, now, WorkEventCreated, createdBy, actor,
			fmt.Sprintf("工作单创建：%s（%d 步）", goal, len(w.Steps)))
		st.Works[w.ID] = w
		st.WorkIdems[fingerprint] = w.ID
		wv = workViewOf(w, 0)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if dupID != "" {
		// 并发同指纹被建单事务收敛为命中（快读路径未命中才走到这）
		w, ok := c.st.View().Works[dupID]
		if !ok {
			return nil, false, ErrInvalid // 不可达：命中即必存在
		}
		return workViewOf(w, 0), true, nil
	}
	return wv, false, nil
}

// appendWorkEvent 事件记账（事务内调用）：seq 单调推进、随 Work 整体落盘；
// 滚动保留最新 MaxWorkEvents 条（头部丢弃，EventSeq 不回退——对齐群消息
// appendAndTrim 口径）。落盘失败随事务副本整体丢弃：seq 未占用、账本未变。
// by 为请求自报审计标签；principal 为服务端验证主体（RFC 六.2，分开存储）。
func appendWorkEvent(w *store.Work, at time.Time, kind, by, principal, body string) {
	w.EventSeq++
	w.Events = append(w.Events, store.WorkEvent{Seq: w.EventSeq, At: at, Kind: kind, By: by, Principal: principal, Body: body})
	if len(w.Events) > store.MaxWorkEvents {
		w.Events = append(make([]store.WorkEvent, 0, store.MaxWorkEvents), w.Events[len(w.Events)-store.MaxWorkEvents:]...)
	}
}

// GetWork 工作单详情（只读；副本返回）。sinceEvents 为事件游标：只返回 seq >
// sinceEvents 的事件（升序）；PrunedBeforeEvents 标注已裁区间上界。不存在返回
// false。tasks_gone 读档注解在此计算（唯一有任务存现可对照的读路径）。
func (c *Core) GetWork(workID string, sinceEvents int64) (*WorkView, bool) {
	view := c.st.View()
	w, ok := view.Works[workID]
	if !ok {
		return nil, false
	}
	wv := workViewOf(w, sinceEvents)
	wv.TasksGone = tasksGoneForWork(view, w)
	return wv, true
}

// workViewOf 快照记录 → 详情视图：账本按游标裁剪 + pruned 标注（裁剪只从头部
// 丢、seq 从 1 连续分配，现存最旧 Seq-1 即已裁区间上界——同群消息 ListMessages
// 口径，无需额外持久化游标）。
func workViewOf(w store.Work, sinceEvents int64) *WorkView {
	if sinceEvents < 0 {
		sinceEvents = 0
	}
	var prunedBefore int64
	if len(w.Events) > 0 {
		prunedBefore = w.Events[0].Seq - 1
	}
	events := make([]store.WorkEvent, 0, len(w.Events))
	for _, ev := range w.Events {
		if ev.Seq > sinceEvents {
			events = append(events, ev)
		}
	}
	w.Events = events
	return &WorkView{Work: w, PrunedBeforeEvents: prunedBefore}
}

// ListWorks 工作单列表摘要（只读；创建时间倒序、同刻按 ID 稳定排序——同
// ListTasks 口径。V0 回环低量不分页，筛选由前端完成）。
func (c *Core) ListWorks() []WorkSummary {
	view := c.st.View()
	out := make([]WorkSummary, 0, len(view.Works))
	for _, w := range view.Works {
		// CoW 纪律：View 零拷贝共享底层指针，摘要换新指针复制（调用方解引用
		// 不得穿透已发布状态，口径同 State.Clone）
		var closed *time.Time
		if w.ClosedAt != nil {
			t := *w.ClosedAt
			closed = &t
		}
		doneSteps := 0
		for _, s := range w.Steps {
			if s.Status == store.StepStatusDone {
				doneSteps++
			}
		}
		out = append(out, WorkSummary{
			ID:        w.ID,
			Goal:      w.Goal,
			PrimeTerm: w.PrimeTerm,
			Status:    w.Status,
			Steps:     len(w.Steps),
			DoneSteps: doneSteps,
			Attempts:  len(w.Attempts),
			CreatedAt: w.CreatedAt,
			ClosedAt:  closed,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RelatedWorks 来源主任务反查（DESIGN-butler 方案二，2026-10-28）：OriginTask ==
// taskID 的工作单摘要，按创建时间降序（最新在前）。1:N 允许（同主任务决策变更
// 建新单，历史保留）；无挂接返回非 nil 空切片（HTTP 序列化为 []，R1-05：REST
// 客户端可对稳定数组形态直接迭代）。任务 24h 清理后反查仍可用（Work.OriginTask
// 随单保留 30 天）——断链注解由读端呈现。
func (c *Core) RelatedWorks(taskID string) []WorkSummary {
	out := make([]WorkSummary, 0)
	for _, w := range c.st.View().Works {
		if w.OriginTask != taskID {
			continue
		}
		done := 0
		for _, st := range w.Steps {
			if st.Status == store.StepStatusDone {
				done++
			}
		}
		out = append(out, WorkSummary{
			ID: w.ID, Goal: w.Goal, PrimeTerm: w.PrimeTerm, Status: w.Status,
			Steps: len(w.Steps), DoneSteps: done, Attempts: len(w.Attempts),
			CreatedAt: w.CreatedAt, ClosedAt: w.ClosedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
