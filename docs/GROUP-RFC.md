# SwarmMind GROUP-RFC：群聊/协作空间（v1.0 草案，2026-10-20）

> 三方讨论产物：Hana（产品编排）+ zcode（实现者视角）+ codex（协议/架构视角）。
> 状态：RFC 草案，归入生命周期 P4（V1）主特性；V0 仅做架构预留。伦哥批准后生效。
> **R7 终审待闭合项（P4 开工前完成）**：①协议闭环补帧（group_ack/发言许可/stop/转任务与终态回流）；②历史补齐的缺口响应与游标落盘规则；③人类主体与权限模型（observer 权限/邀请授权与 exposed 协调）；④MVP 与 DoD 对齐（waiting_human 与 paused 关系/预算预留结算纳入 MVP）。

## 一、问题与定位

当前任务模型是单发单收（submit→execute→result），agent 每次被调用都是冷启动：无记忆、无延续。复杂目标需要的「讨论、分工、互相评审、迭代」无法表达。

**群聊 = 有目标的多 agent 持续协作空间**：Group 是一等实体（非任务附属），因为目标驱动的协作可能产生多个任务、多轮迭代。人可以是 observer（旁观）或 member（参与）。

## 二、核心模型（采纳 zcode 骨架 + codex 修正）

```
Group        {group_id, goal, success_criteria?, status(forming|discussing|paused|concluded),
              host, host_term, members[], budget, created_at}
Member       {agent_ref{node_id, agent_name}, role(host|member|observer)}
GroupMessage {msg_id, group_id, seq, from, reply_to?, kind(user|agent|system), body, created_at}
Task         + GroupRef string `json:"group_ref,omitempty"`   // 向后兼容
Session      {(group_id, member) → session_id/resume能力映射}   // 协议实体预留（见风险节）
```

- `seq` 由 hub 单调分配：历史一致性的唯一锚点（思想同 epoch）
- `status` 只四态；`concluded` 终态不可回退（对齐任务状态机哲学）
- `success_criteria` 可选：收敛判定的可验证依据（codex：意见一致≠目标完成）

## 三、协议（新增 6 帧，全走现有 Envelope）

| type | 方向 | 语义 |
|---|---|---|
| group_create | 人/agent→hub | goal+members+host? → group_ack{group_id}；hub 校验成员已注册且 exposed |
| group_msg | 成员→hub | hub 落盘分配 seq 后扇出 |
| group_msg_event | hub→成员 node | 完整 GroupMessage；node 回 ack 仅驱动补推游标 |
| group_sync | node→hub | {group_id, since_seq} 重连补拉，按 seq 去重 |
| group_close | host/人→hub | → concluded，终态 |
| group_task_created | hub→群 | 转正式任务时自动注入 kind=system 消息（task_id+status+截断 FinalAnswer） |

**可靠性语义（两方共识）**：群消息不做三段 Ack，做「至少一次 + seq 补齐」——群消息无副作用，重复投递最坏是重复说一句话，msg_id+seq 幂等足以压住；hub 侧先写后见落盘（复用 4.7 事务模式），node 侧不持久化只存 last_seq 游标。agent 发言失败不重试（防发言风暴），失败变 system 消息由 host/人决策。

**独立轻量信箱**：群消息 200 条滚动/24h 过期/单体 8KiB 上限——不与任务信箱（100 条/7 天/8MiB）混用，防群聊刷屏挤兑正经任务。

## 四、编排与护栏（采纳 codex 修正）

1. **发言许可制**：消息全员可见，但只有获得发言许可的成员启动计算；补历史/重放/Ack 一律不触发回复。V1 默认流程：收集意见 → host 归纳 → 定向质疑/补证 → 决策或转 Task → 验证
2. **主持权**：V1 的 host 就是人（防死循环最便宜的手段是发言权在手里）；协议字段预留 `host_term`（任期+轮次+截止），V2 自动主持时做租约 fencing——旧主持人迟到调度必须被拒
3. **预算代码强制（非 prompt 约定）**：总轮数/agent 调用次数/Task 数/墙钟/单次上下文/输出长度/每成员与全群预算；派发前预留、完成结算；CLI 无法报 token 时显示「估算/未知」用次数与上下文大小兜底
4. **收敛**：goal 配 success_criteria；无新证据/同义反复/达预算 → paused/waiting_human；意见一致≠目标完成；副作用执行中断保留 needs_review，禁止因群未完成自动重跑

## 五、会话持续性（最大风险与解法）

**根本张力**：V0 全部可靠性建立在「任务=无状态单次执行」上；群聊要求跨调用记忆，而记忆的两条路各有代价——策略 b（context-injection）每轮全量重放上下文，3 成员×10 轮×32KiB 注入即百万 token 级成本；策略 a（session-resume）记忆存 node 本地，hub 不可见、行为不可解释。

**解法（两方融合）**：
- MVP 只实现策略 b（prompt 模板版本化、固定窗口裁剪替代 LLM 摘要）
- **协议先为 a 留位**：session_id 贯穿群消息帧、node 上报 session 生命周期事件（created/resumed/overflow）、AgentCard 声明 resume 能力；维护 (group_id, member) → session 映射（对齐 A2A contextId 不透明标识经验）
- 记忆漂移（原生 session vs 注入窗口）是设计固有属性，文档声明不强求一致
- desktop 原生会话推送（策略 c）留 V2

## 六、MVP 切法（zcode 案，codex 补充采纳）

一句话：**人建一个 2-3 agent 的群，按序点名发言讨论出结论，人可随时插话，结论转一个正式任务，结果回流群。**

不做：host 自动决策、LLM 摘要、自由讨论模式、策略 a/c 实现。
做：协议 6 帧 + 2 Schema + Task.GroupRef；hub group store（复用先写后见）+ seq + 扇出补推 + 独立信箱；node 策略 b + FinalAnswer 回传；`swarmmindctl group create/say/talk/close`（talk=点名驱动循环，人 stop 或轮数上限即结束）。

群版 DoD 增补（不替代原 11 条）：双成员群 3 轮往返；离线重连 group_sync 补齐不重复；转任务+结果回流；spawn 失败注入 system 消息；同 msg_id 重放不重复发言；旧 host_term 不能继续派发。

## 七、安全与阶段

- hub 可见明文在群聊暴露面变大：群消息体 8KiB 上限 + 文档级警告；端到端加密按规划 V2
- 群是新的信任边界：拉人进群=暴露群历史给新成员，邀请需 host 确认（与 D7 接入主权协调）
- **阶段归属（codex 建议，采纳）**：V0/P1-P2 只留边界（信封寻址扩展、消息类型命名空间，不实现群状态机）；V0.5/P3 验证持续上下文+控制台鉴权；**P4/V1 交付完整协作空间**（Group、邀请授权、历史补齐、主持权、预算护栏、人 observer/member）；V2/P5 深度桌面联动、跨组织联邦、群端到端加密
- 业界借鉴：A2A contextId、SLIM 群会话成员控制、Mattermost since 补取、AgentBridge 有界收敛——V1 不引入 SLIM/MLS/新中间件，维持轻量自研路线
