# DESIGN：管家体系与群聊路由（三方案设计稿）

> **状态（2026-10-28 晚）：方案二、方案三已实施完成并通过独立审查闭环**——
> codex R1 退回（3 P1+4 P2）→ zcode 修复（事务内校验/gjoin 长度前缀编码/指引
> kind=agent/上游取消/空数组/卡片进度/测试补齐）→ codex R2 同编号复审**通过**
>（REVIEW-R2：无必修、无新问题）。22 包 build/vet/test/race 全绿。保留建议（不
> 阻塞）：/tmp 补充验证固化入仓、设计稿归档展示与卡片子任务列表验收口径对齐、
> 双机真实 agent 链路验收。方案一由 TASK-orchestrator-declare 工作线实现（未提交，
> 待伦哥拍板汇聚语义分歧后一并对账）。部署待伦哥点头。

> 历史轮次：v2 修订稿经 codex R3 审查（撞车收缩）→ v3 落实 9 条 P1 后实施；详见下方撞车声明与正文。
>
> **撞车声明**：方案一已被另一工作线的 docs/TASK-orchestrator-declare.md 实现替代
>（AgentCard.Orchestrator 声明 + node 控制台「设为管家」开关 + hub PrimeFromDirectory
> 聚合退役 PUT，14 文件，2026-10-28 上午完成未提交）。本文方案一撤回，改为「对齐
> 说明 + 语义分歧裁决点」；方案二三聚焦展开。
>
> **已确认产品口径与当前实现的差异（待伦哥拍板）**：多机声明管家的汇聚语义——
>   - 当前实现：全网目录**恰一声明生效**；多声明 = 配置漂移冲突（无生效管家，回退
>     名字约定 + 控制台告警）；
>   - 产品口径（伦哥 2026-10-28「每台机器都有自己的管家」）：多机各持管家是常态，
>     目录应**各展示各的**（派发页选中哪台机器即用哪台的管家），无「冲突回退」——
>     影响面：PrimeFromDirectory 冲突态、GET /api/v1/prime 的 conflicts 字段、
>     前端告警横幅。
>   差异影响实现版已写的汇聚/冲突/告警代码去留，请伦哥拍板后定案。
>
> 模型澄清（伦哥 2026-10-28 口径，**推翻 2026-10-23「全网统筹指定」定案**）：
>   1. **每台机器都有自己的管家**：管家是 per-node 本机角色（node.yaml prime_agent /
>      控制台钦定 / agents-state 开关），用途是整合本机 agent——复合任务在本机内拆解、
>      派发本机其他能力、汇总。
>   2. **jev 部署在中心端**：hub 内嵌决策服务（llm-as-jev，P5.1 D1 已落地），客户端
>      没有 jev，也不需要有。
>   3. **全局管家角色当前不存在**：后续要做是独立议题，本设计不预占。

## 方案一：管家身份声明化（已被 TASK-orchestrator-declare 实现替代，本文撤回）

实现要点对齐（供方案二三引用）：

- 协议：AgentCard.`Orchestrator bool`（json:"orchestrator,omitempty"）——node 声明
  本能力承担统筹；ExecProfile 增同名 YAML 基线字段。
- node：agents-state.json Orchestrator 开关覆盖（完全复用 exposed 模式：网页开关叠
  加 YAML 基线、先写后见、重启保持）+ `PATCH /api/v1/local/agents/{name}/orchestrator`
  + 控制台能力行「智行管家」开关，变更即 Sync(SendCardUpdate)。CardsFromProfiles 映射
  上报。
- hub：Core.PrimeFromDirectory() 聚合（0 声明→nil 回退名字约定；恰 1→该条目；≥2→
  冲突态）；GET /api/v1/prime 改聚合输出 `{designation, conflicts}`；PUT 退役删除；
  store.Prime 退役（读取丢弃+diff 自动清表，无 schema 迁移）；前端删「指定智行管家」
  按钮/弹窗，orch-line 改只读展示。
- 文档：SPEC 六.五 prime 节重写（豁免⑥已更新：PUT 退役、声明入口在各机器 node 控制
  台「设为管家」开关，同机互斥由 node 侧保证）。

**裁决点**（见文首）：≥2 声明的处理——实现版按「冲突回退」，伦哥口径指向「常态并列
展示」；拍板后实现版聚合层去留相应调整。

### 1.1 混跑兼容（两版口径共有，不变）

旧 hub 收新 node 卡忽略未知字段（不炸）；新 hub 收旧 node 卡 Orchestrator=false →
目录无管家标记，派发走普通路径（现状语义），零破坏。

## 方案二：复合任务拆解闭环（客户端管家编排，hub 记录）

### 2.1 角色与边界

- 拆解智能在客户端管家：管家收到复合目标（stdin 原文 + SWARM_TASK_ID 等系统变量），
  自行拆解为子任务计划。
- hub 只记录与仲裁，**不自动派发 Steps**（CreateWork 只落 pending 计划单，work_dispatch
  明确「hub 不校验步骤 DAG 顺序，决策在 Prime」）——**派发顺序的控制者是管家**：按
  Steps.DependsOn（整数步骤序，工作单内依赖声明）自行决定先后，逐步调 swarmmind_submit
  绑定派发。注意两套依赖机制分工：Work.DependsOn 是步骤间顺序声明（管家编排的依据）；
  submit.depends_on 是任务级字符串幂等键依赖（跨任务门控，本闭环不使用）。
- **不做**：hub 内嵌拆解逻辑；主任务-子任务强生命周期绑定（管家非零退出/超时 → 主任务
  failed；node 中断恢复 → needs_review，各自走既有链路；工作单走既有接管/重派，不联动）。

### 2.2 管家编排工作流（完整执行链；PRIME.md 提示词已覆盖大半，本节补齐主任务挂接）

1. 管家收到复合目标（主任务 stdin），拆解为步骤计划（desc/target_node/target_agent/
   depends_on）。
2. `swarmmind_work_create`（steps+prime_term=本任期；origin_task=SWARM_TASK_ID 见 2.3）
   → 得 work_id。建单幂等（同 goal+steps 指纹返同单）。
3. 按依赖序逐步派发：`swarmmind_submit` 携 `work_ref`+`step_seq`+`prime_term`+`idem_key`
   （幂等键规范 `prime-<任期>-<工作单>-<步骤>-<尝试>`，PRIME.md 既有约定）→ 步骤回写。
4. `swarmmind_wait` 等子任务终态（或 work_get since_events 游标轮询）→ 依赖满足的下一
   步继续派发。
5. 全部步骤终态后 `swarmmind_work_collect`（prime_term+report）关闭工作单。
6. 管家产出汇总报告到 stdout → 主任务 completed，FinalAnswer=汇总。

失败/异常出口（各走既有链路）：步骤失败 → 管家决策重派（同键重试≤预算）或放弃
（work_collect outcome=abandoned+理由）；步骤 needs_review → 工作单 blocked，等人工
核对后再派；管家非零退出/超时 → 主任务 failed；node 中断 → 主任务 needs_review，
工作单等接管（W2 CAS）；接管冲突 → 后来者拒绝，先到者继续。

### 2.3 挂接：主任务 ↔ 工作单（R3' P1-3/P1-4 闭合）

- **基数决策：1 主任务 : 1 工作单**。管家的一个复合任务理应一张单；重复建单走既有
  幂等（同 goal+steps 返同单）。同主任务拆出不同计划=管家决策变更，新单挂同 origin，
  related_works 允许 N（UI 取最新，历史保留）——存储与反查按 1:N 设计，UI 呈现单数化。
- **存储**：store.Work 增 `OriginTask string json:"origin_task,omitempty"`（建单写入，
  不可变）；WorkCreateRequest/MCP work_create 增同名可选参数。
- **幂等命名空间**：有 origin 时指纹含 origin（不同主任务相同计划不撞单）；无 origin
  保持旧指纹口径（既有幂等行为零变化）。storedWorkHash/WorkIdems 同步。
- **反查**：GET /api/v1/tasks/{id} 附带 `related_works`（经 OriginTask 反查，数组按
  CreatedAt 降序；无挂接恒为非 nil 空数组 `[]`，REST 客户端可稳定迭代，R1-05）。
  origin 任务已过 24h 清理：**断链注解由工作单侧承担**（work_get 既有 tasks_gone
  读档注解，RFC-work-resume W3 同款机制）；任务详情端点保持保留期 404 既有语义
  不新增归档态响应（R2 复审口径修订 2026-10-28：原「UI 显示来源任务已归档」的
  任务侧展示不实现——任务归档后详情入口随保留期语义消失，断链解释在单侧完成，
  零新机制）。
- **校验**（仅新建时做，幂等回执补拉不重校验——不破坏建单回执幂等）：origin_task
  存在于任务账本且 (TargetNode, TargetAgent) 与建单调用来源精确匹配（hub MCP 无
  「调用者所在机器」概念，以原任务派发目标为锚）；不匹配 400 `invalid_request`。
  旧 node 缺 Orchestrator 声明 → 目录无标记 → 挂接校验失败 400（明确指路升级）。
  声明事后撤销：已建单不追溯（校验时点=建单写事务内查目录快照）。

### 2.3 下钻 UI（hub 任务详情页）

主任务详情显示目标指令原文（stdin 已有）+ 关联工作单卡片（goal / 步骤进度 x/y；
子任务明细经工作单详情下钻查看——R2 复审口径修订 2026-10-28：卡片承载摘要进度，
明细不再在卡片展开，与既有工作单抽屉单一详情源原则一致，避免两处维护同一明细）。数据源 WorkView 既有字段；抽屉分区 diff
局部更新（D22 纪律），related_works 查询随任务详情同请求返回（不做二次拉取）。
node 控制台不下钻（工作单视图 hub 端独有，SPEC 六.五 白名单既有口径）。

### 2.4 Work MCP 接线契约（R3' P1-2：管家的 hub MCP 从哪来）

- **networked 管家**（连 hub 的机器）：需 hub 模式 MCP（SWARM_HUB_URL + SWARM_HUB_TOKEN，
  work_* 工具 hub 模式专属）。获得方式：主人在管家的接入配置（如 zcode 宿主 MCP 配置）
  里添加 hub 模式 mcpServers 条目——hub 侧 PAT 经 hub 控制台签发，地址为 wss 域名对应
  的管理面。与 node 模式 MCP（SWARM_TARGET=node，本机编排/本机 prime_token）**并存**
  两个 mcpServers 条目：node 模式管本机编排（本机 submit/群工具六件套），hub 模式管
  全网编排（work_*/tasks/dispatch_suggest）。PRIME.md 增补「两模式并存」节说明分工。
- **声明与接线的权威关系**：Orchestrator 声明（能力卡）只承担「全网目录标记」；编排
  接线（prime_token + MCP 配置写入影子 home）仍由既有 prime_agent/declare/rotate
  机制承担——**两机制并存，声明不触发接线**（避免声明权与凭证权耦合；接线变更仍走
  主人人工事务，SPEC 六.六豁免③④口径不变）。
- **「本机内拆解」边界澄清**：Work 机制本身允许步骤跨节点（Steps.TargetNode 自由
  声明）——管家拆解可以派网内任意机器，这是能力而非缺陷；「整合本机 agent」指管家
  默认编排本机能力，不构成协议层限制。若未来需要硬限本机，属独立需求本设计不预占。
- **standalone 边界**：standalone node 无 hub，work_* 工具不装配（既有口径），本机
  管家在 standalone 下仅本机编排（node 模式 MCP submit 直执行），无工作单闭环——
  如实声明，不做伪装。

### 2.5 DoD（R3' P1-9：崩溃预期准确化 + 接口映射 + 机械用例）

1. 真机闭环：派复合目标给管家（Orchestrator 卡）→ 管家建单（origin_task 挂接）→
   按依赖序绑定派发 → 管家汇总 → 主任务 completed 且 FinalAnswer 为汇总报告。
2. 任务详情 API related_works：挂接/无挂接/origin 已归档三态正确（单测）。
3. 下钻 UI：目标原文 + 工作单卡片呈现，无挂接不出现卡片；分区 diff 局部更新、
   迟到响应丢弃（D22 纪律）；node 端不下钻（白名单核对）。
4. origin_task 校验：不存在 / TargetNode+TargetAgent 与建单来源不符 / 旧 node 无
   Orchestrator 声明 → 400（单测三分支）；幂等回执补拉不重校验（单测）。
5. 幂等命名空间：有 origin 指纹含 origin（不同主任务同计划不撞单）；无 origin 旧
   口径零变化（单测双分支）。
6. 失效预期（准确口径）：管家 agent 非零退出/超时 → 主任务 failed；管家所在 node
   中断恢复 → 主任务 needs_review；工作单在两态下均走既有接管（W2 CAS）——各自
   回归锁。
7. 接口与工具映射：WorkCreateRequest/MCP work_create 增 origin_task（SPEC 六.六
   规则表补行）；GET tasks/{id} 增 related_works（响应字段，SPEC 六.五登记）；
   无新增端点。
8. 回归：`go build ./... && go vet ./... && go test ./...` 全绿（具体以命令为准）；
   work 既有测试零回归。

## 方案三：群聊多 agent 协作（jev 选成员，并发协作，收敛最终版本）

> 2026-10-28 伦哥口径修正：群聊是**多 agent 协作圆桌**，不是单执行者派发——多个
> agent 同时开始在群里工作，消息共享碰撞思维火花，或共同解决一件事，收敛出最终
> 版本。jev 的角色 = 建群时判定**参与集合**（谁该在场），无需人工确认。

### 3.1 模型

- **建群即协作启动**：主人只给协作目标（goal），members 由 jev 判定（全网在线∩
  exposed 能力中多选出适合参与的集合）——无需人工挑人。
- **并发协作任务**：每个被选中的成员各自收到一个「群协作任务」（hub 对每个成员
  submit，group_ref 挂群，并发执行）——任务 stdin 携协作指引（群 ID/目标/工具用法），
  agent 在任务内经 MCP 群工具读写群消息（group_messages 增量拉取共享上下文、
  group_say 发言贡献），多任务并发即「同时开始」。
- **消息共享**：群消息账本对全体成员可见（group_messages 增量拉取，既有机制）——
  每个成员都能看到其他成员的贡献，思维碰撞的载体。
- **收敛**：任一成员发言以 `FINAL:` 前缀标记即登记「最终版本候选」（系统注记）；
  发言预算（MaxRounds）约束总协作量，耗尽后群消息提示预算用尽；Host（主人）可
  随时 concluded 定版。MVP 不做自动语义收敛（LLM 判定「已共识」属后续增强）。

### 3.2 jev 多选（decision 包扩展）

- 新增 `SuggestMembers(ctx, goal, candidates, topK) → []RankedMember`：LLM 输出
  「适合参与该协作的候选 id 集合 + 逐个理由」（新 prompt：多选判定，区别于 D1
  Choice 单选——两者语义不同不可复用单选概率分布，R3' P1-6 教训在此落地：多选
  有独立输出契约）。
- 校验：越界 id 剔除；集合为空/全不适合 → 空集合（调用方走降级）；provider 失败/
  超时 → 降级语义与 D1 同款（建议非必经）。
- 既有 `Suggest`（单选）零改动——派发建议（dispatch-suggest）与协作选人（SuggestMembers）
  是两个语义面。
- 降级链：jev 未启用/失败/空集合 → 建群回退「主人手动选成员」（建群表单 members
  手选路径保留，现状形态），不阻塞。

### 3.3 路由与交付流程

1. 主人建群入口提交 goal（勾选「智能选人」）→ hub 落建群请求。
2. hub 调 `SuggestMembers` → 成员集合（如 [zcode@mac-mini, analyst@win-box]）→
   建群（Members=集合，Host=admin）→ 群消息 kind=system 注记「成员由智能路由选定」。
3. 对每个成员**并发 submit 群协作任务**（idem_key=`gjoin-<group_id>-<member>` 稳定
   可重放；group_ref 挂群；stdin=协作指引模板：群 ID、群目标、你的角色建议、工具
   用法提示 group_messages/group_say、收敛约定 FINAL: 前缀）。并发任务即「同时
   开始」；node 并发槽（concurrency）排队属运行时细节。
4. 各成员在任务内循环：group_messages 拉增量 → 贡献发言/执行擅长部分 → group_say
   发言（计预算）→ 直到自身贡献完成或预算将尽，以 FINAL: 前缀给出最终版本候选。
5. 收敛定版：Host 群聊页 concluded（终态后消息冻结）；群页 FINAL 候选高亮展示。
   关联子任务（成员协作中经 swarmmind_submit 落的正式任务）照常 TaskRefs 挂群 +
   终态回流（既有机制）。

### 3.4 边界与降级

- GROUP-RFC「消息不自动扇出不自动启动成员」边界的突破范围：**仅限本节定义的
  群协作任务交付链**（建群时的并发 submit 是显式派发协议），普通群消息仍不自动
  唤醒成员；GROUP-RFC 文档同步此边界修订。
- Budget：协作任务的 stdin 指引中告知预算余量（成员自行节制发言）；预算耗尽后
  group_say 被拒（budget_exhausted）→ 成员任务应以 FINAL: 收尾或静默退出（指引
  约定）；正式任务准入与预算解耦（既有口径）。
- 结果链单链：成员协作中落的正式任务经 group_ref 系统回流（既有）；成员发言只是
  协作面，不承担结果链。
- 降级：jev 不可用 → 建群表单回退手选成员（现状形态，零新机制）。

### 3.5 DoD

1. jev 启用真机：提交协作目标 → 成员集合由 jev 选定 → 建群 + 各成员并发收到协作
   任务 → 成员经群工具发言（消息共享可见）→ FINAL: 候选登记 → Host concluded。
2. jev 关闭/失败/空集合：建群回退手选成员 + 人话注明（单测注入故障）。
3. SuggestMembers：越界剔除/空集合/超时降级（单测 mock provider）；与 Suggest
   语义隔离（单选面零改动）。
4. 并发协作任务幂等：gjoin 键重放不重复派发（单测）。
5. Budget：协作发言计预算、耗尽拒绝、正式任务不受影响（单测）。
6. FINAL: 标记登记与群页高亮（前端）。
7. 群聊既有测试零回归；SPEC 六.五（群聊节边界修订）+ GROUP-RFC（边界修订）+
   六.六（SuggestMembers 经建群内部接线，无新增 agent 直调端点——建群 REST 已有，
   扩展参数即可；MCP group_create 增 auto_members 参数同步）。
## 实施顺序与依赖（R3' 建议采纳）

1. **先修声明制基线**：另一工作线实现的阻塞性死锁 bug（agentsstate.go persist 自锁，
   见附录）必须先修 + 多机汇聚语义裁决落地——这是方案二三的地基。
2. 方案二：MCP 接线契约（networked 管家 hub MCP 获得方式）→ origin 挂接（幂等命名
   空间+校验+反查）→ 下钻 UI → 编排约定文档增补。
3. 方案三：交付/幂等闭环（submit 交付链+路由幂等键）先于智能选人 → SuggestMulti
   （排序备选呈现）→ 降级链 → UI。
4. UI 与独立验收（TESTPLAN 机械用例 + 独立会话执行）最后统一做。

## 已知陷阱

- AgentCard.Orchestrator 的混跑口径照抄 AllowArgs 注释先例（缺字段 false 仅为呈现
  值），不把「旧 node 未声明」宣称为「该机无管家」（实现版已落实，方案二沿用）。
- 退役 PUT /api/v1/prime 属 API 删除：SPEC 六.五 端点表同步 + MCP 工具面核查
  （swarmmind_prime 相关工具若存在须同步退役/改派生）。
- 方案二 origin_task 软校验「target_agent 是本机 Orchestrator 卡」：校验时点在 work_create
  （写事务内查目录快照），目录漂移（声明后撤销）不追溯已建单。
- 方案三 task-assign 消息计入 Seq 账本但不计 Budget.MaxRounds（只计 kind=agent）；
  jev 建议集合含离线成员时过滤（候选组装仅在线∩exposed，与 D1 同款）。
- 方案二 Work MCP 接线：networked 管家需 hub 模式 MCP（与 node 模式并存两 mcpServers
  条目），standalone 无工作单闭环（如实声明不伪装）；Orchestrator 声明不触发接线
  （接线权在 prime_agent/declare 主人事务，两机制并存——R3' P1-2）。
- 方案三交付链不违反 GROUP-RFC 边界（消息不自动扇出/不自动启动成员）：路由产物是
  submit 正式任务（显式派发协议），主人确认是动作主体；task-assign 类「消息投递唤醒」
  形态已否决（R3' P1-5）。
- 路由幂等键 `groute-<group_id>-<seq>`：seq 为 goal 消息组内序号（单调稳定），换目标
  改派键不变；同消息重复确认命中幂等不重复执行。
- 版本纪律：不 commit、不发版，设计稿审查通过后按批次排期实施。

## 附录：R3' 复跑发现的声明制实现阻塞性 bug（2026-10-28）

**非本设计稿缺陷，属另一工作线（TASK-orchestrator-declare）实现基线问题**：
`internal/node/localapi/agentsstate.go:225` 的 `persist()` 内再次 `s.mu.Lock()`，
而调用方 `SetExposed`（agentsstate.go:128）已持同一 mutex → **自锁死锁**。
`TestReplaceBaselineRoundTrip` 在包级 30s / 单用例 5s 复跑均超时 panic，栈一致
（persist ← SetExposed ← 测试）。修法：persist/persistOrch 去掉内部加锁（注释已写
「调用方持 mu」，直接删 Lock/Unlock 两行），修后全量重跑。此项必须先于方案二三实施。
