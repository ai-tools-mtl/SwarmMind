// grouphost.go jev 群主持人接力棒（DESIGN-butler 方案三 3.6，v4 修订：伦哥口径
// 「成员由主人自己选，jev 是群内主持人，轮转点名发言」）。
//
// 模型：建群（HostActive）→ Suggest 单选点名第一棒 → submit 棒任务（ghost 键，
// group_ref 挂群）→ HostTurn=任务 ID → 棒任务终态回调（core.AddOnTaskTerminal
// 注册的旁路钩子）→ 清 HostTurn → 停轮检查 → Suggest 点名下一棒 → CAS 置棒 +
// submit → 循环。同群恒一棒在途：推进经 store 的 HostTurn CAS（AdvanceHostTurn）
// 防重入——并发终态回调只有 CAS 赢家派下一棒，败者静默放弃。
//
// 停轮（HostActive 保持 true、循环挂起、群照常可手动插话/转任务）：
//   - FINAL 收敛候选（FinalSeen 持久化标记，PostMessage 落账同事务登记——消息
//     裁剪不遗失；点名前预检 + 推进事务内复核双道，决策等待窗口内新落账的
//     FINAL 不漏检）；
//   - 发言预算耗尽（AgentMsgs 真实累计口径：一棒可发多条消息、其他 agent 可
//     并发发言，与轮转数不等价；预检 + 推进事务内复核双道）；
//   - HostRound 达 Budget.MaxRounds（轮转上限）；
//   - Suggest 无建议（NoFit/候选空）或降级（provider 失败/超时）。
//
// 异步纪律：终态钩子合同要求不得长时间阻塞（Suggest 预算 15s），推进在独立
// goroutine 内执行；挂起后无自动恢复入口（重启亦然——HostTurn 若在 CAS 后、
// submit 前崩溃残留，循环停留挂起态），主人手动插话/转任务为既定出路，设计稿
// 3.6 明示「主持循环挂起不阻塞群」。
//
// v4 审查修复（2026-10-28 codex 首审退回三 P1）：①发言预算判定改真实累计口径
// （group.SpeechBudgetUsed / AgentMsgs）并在推进事务内复核；②FINAL 收敛候选
// 持久化（group.FinalSeen 标记）且停轮复核与 HostTurn CAS 同一 store 事务——
// Suggest 等待窗口内落账的 FINAL/发言不漏检。
package api

import (
	"fmt"
	"log"
	"strings"

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// hostBatonIdem 主持棒任务幂等键：ghost-<gid>-<round>。gid（hex）与 round（整数）
// 均为 hub 生成值、无成员名类歧义字符，无需 gjoin 的长度前缀编码。
func hostBatonIdem(groupID string, round int) string {
	return fmt.Sprintf("ghost-%s-%d", groupID, round)
}

// advanceHostOnTerminal 终态旁路钩子（api.New 时经 Core.AddOnTaskTerminal 注册）：
// 命中「某主持群的在途棒任务」（group_ref 挂群 + HostTurn==该任务）即异步推进
// 下一轮。其余任务终态零开销（ListGroups 为小集合线性扫描）。
func (s *Server) advanceHostOnTerminal(t protocol.Task) {
	if s.cfg.Groups == nil || s.cfg.Core == nil {
		return
	}
	for _, g := range s.cfg.Groups.ListGroups() {
		if g.HostActive && g.HostTurn == t.ID && group.TaskRefIn(g.TaskRefs, t.ID) {
			go s.advanceHost(g, t)
			return
		}
	}
}

// advanceHost 单轮推进：前置校验 → 停轮检查/点名（hostNextBaton）→ CAS 置棒
// （停轮条件同事务复核）→ submit 棒任务。任何失败路径都把循环收敛到「挂起 +
// 人话注记」，绝不阻塞群。
func (s *Server) advanceHost(seed group.Group, done protocol.Task) {
	gs := s.cfg.Groups
	g, ok := gs.GetGroup(seed.ID)
	if !ok || !g.HostActive || g.HostTurn != done.ID || g.Status != group.StatusDiscussing {
		return // 群已关/主持已推进/钩子重复触发：幂等放弃
	}
	member, stopNote := s.hostNextBaton(g)
	if member == "" {
		if released, _ := gs.ReleaseHostTurn(g.ID, done.ID); released {
			s.postHostNote(g.ID, stopNote) // 只有持棒者发停轮注记（并发败者不发声）
		}
		return
	}
	preID := protocol.NewTaskID()
	// CAS 与停轮复核同一 store 事务（v4 审查修复①②）：Suggest 等待窗口内其他
	// agent 并发发言落 FINAL/耗尽预算，在此兜底复核，命中即停轮挂起。
	updated, stop, advanced, err := gs.AdvanceHostTurn(g.ID, done.ID, preID)
	if err != nil {
		log.Printf("[hub] 主持推进失败 group=%s: %v", g.ID, err)
		return
	}
	if stop != "" {
		// 决策等待期间命中停轮：状态未变（HostTurn 仍=done.ID），释放挂起 +
		// 注记（released 门防并发双发声）。
		if released, _ := gs.ReleaseHostTurn(g.ID, done.ID); released {
			s.postHostNote(g.ID, hostStopNote(stop, g))
		}
		return
	}
	if !advanced {
		return // 并发败者：赢家已推进
	}
	s.submitHostBaton(updated, member, preID)
}

// startHostFirstBaton 建群即点名第一棒（handleGroupCreate 内同步执行，与
// autoSelectMembers 同为请求内 best-effort：Suggest 预算内失败只注记挂起）。
func (s *Server) startHostFirstBaton(g group.Group) {
	member, stopNote := s.hostNextBaton(g) // HostRound==0、HostTurn==""
	if member == "" {
		s.postHostNote(g.ID, stopNote)
		return
	}
	preID := protocol.NewTaskID()
	updated, stop, advanced, err := s.cfg.Groups.AdvanceHostTurn(g.ID, "", preID)
	if err != nil {
		log.Printf("[hub] 主持首棒置位失败 group=%s: %v", g.ID, err)
		return
	}
	if !advanced {
		if stop != "" { // 新建群无 FINAL/零发言/0 轮，理论不可达；防御性按停轮处理
			s.postHostNote(g.ID, hostStopNote(stop, g))
		}
		return
	}
	s.submitHostBaton(updated, member, preID)
}

// submitHostBaton 派棒（预登记 → submit → 回滚/注记，dispatchGroupCollab 同款
// 三段式）：失败释放 HostTurn（循环挂起）+ 停轮注记；dup（ghost 键重放命中）改
// 绑原任务使终态仍能触发下一轮，不重复注记。
func (s *Server) submitHostBaton(g group.Group, member, preID string) {
	node, agent, ok := strings.Cut(member, "/")
	if !ok {
		node, agent = "", member
	}
	if err := s.cfg.Groups.AddTaskRef(g.ID, preID); err != nil {
		log.Printf("[hub] 主持棒预登记失败 group=%s member=%s: %v", g.ID, member, err)
		if released, _ := s.cfg.Groups.ReleaseHostTurn(g.ID, preID); released {
			s.postHostNote(g.ID, "主持人停轮：下一棒任务登记失败，主持挂起——可插话推进或手动转任务")
		}
		return
	}
	req := protocol.TaskSubmitRequest{
		TargetNode:  node,
		TargetAgent: agent,
		Input:       protocol.TaskInput{Stdin: hostBatonStdin(g, member)},
		SubmittedBy: adminIdentity,
		IdemKey:     hostBatonIdem(g.ID, g.HostRound),
		GroupRef:    g.ID,
	}
	ack, err := s.cfg.Core.SubmitWithID(req, preID, "", adminIdentity)
	if err != nil {
		s.unwireGroupTask(g.ID, preID)
		log.Printf("[hub] 主持棒派发失败 group=%s member=%s round=%d: %v", g.ID, member, g.HostRound, err)
		if released, _ := s.cfg.Groups.ReleaseHostTurn(g.ID, preID); released {
			s.postHostNote(g.ID, fmt.Sprintf("主持人停轮：第 %d 棒派发失败（%.120s），主持挂起——可插话推进或手动转任务",
				g.HostRound, err))
		}
		return
	}
	if ack.Dup {
		// ghost 键重放命中：预生成 ID 未落库，回滚预登记并把在途棒改绑原任务
		//（终态回流/下一轮推进挂到原任务上），不重复发创建注记。
		s.unwireGroupTask(g.ID, preID)
		_ = s.cfg.Groups.AddTaskRef(g.ID, ack.TaskID)
		_, _ = s.cfg.Groups.RedirectHostTurn(g.ID, preID, ack.TaskID)
		return
	}
	s.noteGroupTaskCreated(g.ID, ack.TaskID, fmt.Sprintf("主持人点名第 %d 棒 · 成员 %s", g.HostRound, member))
}

// hostNextBaton 下一棒点名决策：先停轮检查（FINAL 标记/发言预算/轮转上限），
// 再组装成员∩在线候选调 Suggest 单选。返回 (member="", stopNote) 即停轮挂起；
// (member, "") 即点名成功（member 形态 node/agent）。此处为省一次决策调用的
// 预检——权威复核在 AdvanceHostTurn 推进事务内（Suggest 等待窗口内群状态可能
// 已变化，见 grouphost.go 头注），两处共用 hostStopNote 同一注记词典。
func (s *Server) hostNextBaton(g group.Group) (string, string) {
	// 停轮 1：FINAL 收敛候选（FinalSeen 持久化标记——PostMessage 落账同事务
	// 登记，消息被滚动裁剪/过期清理也不遗失，不扫描现存消息）。
	if g.FinalSeen {
		return "", hostStopNote(group.HostStopFinal, g)
	}
	// 停轮 2：发言预算（真实累计口径 SpeechBudgetUsed——持久化 AgentMsgs 计数。
	// 一棒可发多条消息、其他 agent 可并发发言，可见消息数会被滚动裁剪低估，
	// 禁止以其推算；达上限即停，等主人定版）。
	if used, ok := s.cfg.Groups.SpeechBudgetUsed(g.ID); ok && used >= g.Budget.MaxRounds {
		return "", hostStopNote(group.HostStopSpeech, g)
	}
	// 停轮 3：主持轮转预算（发言与轮转同一 Budget.MaxRounds 旋钮，达上限即停）。
	if g.HostRound >= g.Budget.MaxRounds {
		return "", hostStopNote(group.HostStopRounds, g)
	}
	if s.cfg.Decision == nil || !s.cfg.Decision.Enabled() {
		return "", "主持人停轮：决策服务未启用（hub.yaml 未配置 decision 段或密钥不可得），主持挂起——可插话推进或手动转任务"
	}
	cands := s.hostCandidates(g)
	if len(cands) == 0 {
		return "", "主持人停轮：群成员当前均不在线或能力未开放，主持挂起——可稍后插话推进或手动转任务"
	}
	msgs, _, err := s.cfg.Groups.ListMessages(g.ID, 0)
	if err != nil {
		return "", "主持人停轮：群消息读取失败，主持挂起——可插话推进或手动转任务"
	}
	res := s.cfg.Decision.Suggest(decision.Request{Goal: hostGoalDigest(g, msgs), Candidates: cands})
	if res.Degraded || res.Suggestion == nil {
		return "", "主持人停轮：" + res.Note + "；主持挂起，可插话推进或等待主人定版"
	}
	return res.Suggestion.TargetNode + "/" + res.Suggestion.TargetAgent, ""
}

// hostStopNote 主持停轮原因 → 人话注记：hostNextBaton 预检与 AdvanceHostTurn
// 事务内复核命中共用同一词典，两处判定口径与文案一致（停轮语义单源）。
func hostStopNote(reason string, g group.Group) string {
	switch reason {
	case group.HostStopFinal:
		return "主持人停轮：已有成员给出收敛候选（FINAL:），等待主人定版"
	case group.HostStopSpeech:
		return fmt.Sprintf("主持人停轮：群发言预算已用尽（上限 %d 条），等待主人定版", g.Budget.MaxRounds)
	case group.HostStopRounds:
		return fmt.Sprintf("主持人停轮：主持轮转已达预算上限（%d 轮），等待主人定版", g.Budget.MaxRounds)
	default:
		return "主持人停轮：主持挂起——可插话推进或手动转任务"
	}
}

// hostCandidates 群成员 ∩ 在线 exposed 候选（目录真相源过滤）：成员形态
// node/agent 与候选 agent@node 精确匹配。无「/」的旧形态裸名成员不参与点名
//（目录候选恒带节点，无锚点可匹配），该形态群首棒即挂起并有人话注记。
func (s *Server) hostCandidates(g group.Group) []decision.Candidate {
	members := make(map[string]bool, len(g.Members))
	for _, m := range g.Members {
		members[m] = true
	}
	all := s.dispatchCandidates()
	cands := make([]decision.Candidate, 0, len(all))
	for _, c := range all {
		if members[c.NodeID+"/"+c.Agent] {
			cands = append(cands, c)
		}
	}
	decision.SortCandidates(cands)
	return cands
}

// hostGoalDigest 主持人点名 goal：主持人指令 + 群目标 + 近期群消息摘要。
// decision.Request 的 state 只由 goal+candidates 组装（D1 语义面），主持语义
// 折叠进 goal 文本。共用 Suggest 系统提示是「选最适合执行该任务的执行者」——
// 不显式覆盖会退化为普通派单评分（v4 审查修复④）：本判定是主持人点名，要求
// 从成员中选下一位能新增贡献的发言者、依据讨论进展避免重复、协作已完成无需
// 继续时选 none。摘要折叠进 goal 文本——末尾 8 条足供「谁刚说过什么、下一步
// 该谁」的点名参考。
func hostGoalDigest(g group.Group, msgs []group.GroupMessage) string {
	const digestTail = 8
	var b strings.Builder
	b.WriteString("【判定任务：协作群主持人点名下一棒发言者（不是任务派单）】\n" +
		"- 从候选成员中选出下一位能新增贡献的发言者：结合近期讨论进展，优先尚未发言、发言少或观点能与已有讨论互补的成员，避免点名已表达过同类观点的成员（避免重复发言）；\n" +
		"- 若讨论已收敛、共识达成或协作目标已完成，无需继续发言，choice 填 none；\n" +
		"- 不要按「谁最适合执行任务」的能力匹配度评分——本次只决定谁来说下一句话。\n")
	fmt.Fprintf(&b, "【群协作目标】%s\n【近期群消息摘要】", g.Goal)
	tail := msgs
	if len(tail) > digestTail {
		tail = tail[len(tail)-digestTail:]
	}
	if len(tail) == 0 {
		b.WriteString("（群内暂无发言：请选出第一棒开场成员）")
		return b.String()
	}
	for i, m := range tail {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s(%s)：%s", m.From, m.Kind, hostDigestTruncate(m.Body, 80))
	}
	return b.String()
}

// hostDigestTruncate 按 rune 截断（中文按字计）；超长以 … 结尾。
func hostDigestTruncate(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n]) + "…"
}

// hostBatonStdin 主持棒任务指引模板（沿用 groupCollabStdin 纪律：group_say 示例
// 一律显式 kind="agent"，附预算耗尽收尾指引——缺 kind 按 user 插话处理不计预算、
// FINAL 不登记）。v4 口径：本棒由主持人点名，发言落定即收尾等下一位接棒。
func hostBatonStdin(g group.Group, member string) string {
	return fmt.Sprintf(`你是协作群 %s 的成员（%s）——本轮由群主持人（jev）点名你第 %d 棒发言，其他成员会依次接棒：
%s

协作方式（swarmmind MCP 群工具）：
1. 先用 swarmmind_group_messages（group_id=%s，since=0）读取已有讨论——其他成员的观点与进展都在里面，先读再说话，避免重复劳动。
2. 贡献你的部分：需要落正式工作用 swarmmind_submit（务必带 group_ref="%s"，结果会自动回流群）；分析观点直接成文。
3. 用 swarmmind_group_say（group_id=%s，from="%s"，kind="agent"）把贡献/观点发到群里——必须带 kind="agent"，发言才计入群预算并参与收敛；漏传会被当成用户插话，FINAL 不被登记。
4. 若讨论已收敛（共识达成或目标可定版），直接用 swarmmind_group_say（group_id=%s，from="%s"，kind="agent"）发一条以 FINAL: 开头的总结（恰发一条）——主持人见到 FINAL 即停轮，等待主人定版；否则发言落定后本棒任务即结束，等主持人点名下一位。
注意：群发言预算全群共享，agent 发言计入（本群上限 %d 条），不要刷屏；预算用尽（budget_exhausted）或 FINAL 已发出后立即收尾，不要再发必被拒绝的消息。正式工作成果以 submit 任务结果为准（自动回流，无需复述全文）。`,
		g.ID, member, g.HostRound, g.Goal, g.ID, g.ID, g.ID, member, g.ID, member, g.Budget.MaxRounds)
}

// postHostNote 主持状态系统注记（best-effort）：失败只记日志，不影响群/任务。
func (s *Server) postHostNote(groupID, body string) {
	if _, err := s.cfg.Groups.PostMessage(groupID, group.RefluxFrom, group.KindSystem, body); err != nil {
		log.Printf("[hub] 主持状态注记失败 group=%s: %v", groupID, err)
	}
}
