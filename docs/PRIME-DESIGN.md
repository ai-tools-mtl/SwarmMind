# SwarmMind 总理智能体与决策引擎设计（PRIME-DESIGN v0.9 草案）

> 2026-10-21。回应伦哥需求：①群聊/团队分工，同时调用不同机器的不同 agent；②总理 agent 负责调度 vs StartLux-Decision-v1.1 决策引擎如何取舍。
> 流程：Hana 起草 → codex 审查 → 修订定稿 → 伦哥批准。归入 P4。

## 一、核心判断：总理是「角色」，不是「组件」

**总理 agent（Prime）不新建任何基础设施。** 它的定义：

> 任何连接了 SwarmMind MCP、被授权调度权、以 LLM 为大脑的智能体，扮演总理角色时即为 Prime。

理由：
- MCP 六工具（nodes/agents/submit/status/wait/artifact）已经是完整的编排原语——看网络、派任务、等结果、取产物，DAG 编排天然成立
- 大脑现成：zcode-dev（GLM）已接入 mac-mini，跑一个「带 MCP 的 zcode 会话」就是 Prime
- GROUP-RFC 的 host 职责（人）与 Prime 的调度职责**分离不混同**：人定目标与验收，Prime 干分解派发收集的活

**V1 的第一个 Prime = mac-mini 上的 zcode-dev（MCP 会话形态）。**

## 二、三层职责边界

```
人      目标输入 · 预算设定 · 结果验收 · 关键决策（host 职责，GROUP-RFC 原则不变）
Prime   目标理解 → 任务分解（DAG）→ 分配决策 → 派发与等待 → 结果收集 → 汇总上报 → 迭代判断
StartLux-Decision  纯函数决策服务：输入（子任务+能力目录+历史）→ 输出（推荐分配+置信度+理由）
hub     路由 / 持久化 / 可靠性（不感知 Prime 存在——Prime 就是普通客户端）
节点 agent  执行（zcode-dev / codex-dev / diskcheck / …）
```

铁律不变：hub 永不执行；节点主权不动；Prime 与所有 agent 一样走同一套任务全链（无特权通道）。

**V1 权限事实声明（M1）**：系统无细粒度调度权限实体——Prime = 持 admin 令牌的普通客户端，能力上限即管理员全权；换发/吊销其令牌即终结调度能力。scoped token 列入 backlog。

## 三、StartLux-Decision：影子建议，不是自动驾驶

**决策链**：Prime 分解出子任务 → 向 StartLux 查询「该子任务派给谁」→ Prime 审核（能力匹配/负载/依赖/成本）→ 派发（采纳或改派，**记录差异**）。

- **影子原则**（对齐 PLAN「影子建议先行」）：V1 阶段 StartLux 输出仅为建议，Prime 拥有否决权；每次「建议 vs 实际」的差异落盘，作为 StartLux 迭代数据
- **接口形态**：独立进程（stdio/HTTP 均支持），StartLux-Decision-v1.1 模型加载；不可用时 Prime 降级为「自身规则分配」（目录匹配 + 负载均衡），系统不停摆
- **晋级路径**：影子数据积累 → 差异率收敛 → 伦哥批准 → StartLux 从「建议」晋级「默认采纳」（Prime 只做安全否决）

## 四、编排能力 = MCP 原语组合（零新增协议）

| 编排需求 | Prime 的做法（全部用现有 MCP 工具） |
|---|---|
| 并行任务 | 连续 submit（hub 并发窗口已支持）→ 逐个 wait |
| 串行依赖 | submit A → wait A → artifact 取 A 产物 → 产物内容嵌入 B 的任务描述 → submit B。**嵌入 ≤200KiB 纯文本**（1MiB 信封硬上限+信箱字节预算）；超限或跨节点大数据依赖在 L2 就绪前禁止编排，上报用户 |
| 失败处理 | wait 返回 failed → 失败矩阵分类（参数越界改描述重派/环境类原样重派/未知上报），同键幂等保护 |
| needs_review | 视为终态阻断：Prime 上报人，不自行重跑（对齐 4.8）；其 FinalAnswer 不进结论 |
| 取消 | **V1 无取消原语**（显式声明）：误派最大代价 = timeout_sec × 受影响任务数；hub→node cancel 帧列入 backlog（runner 进程组终止已就绪，成本可控） |
| 同 workdir | 同名 agent 的并行子任务强制串行（同 agent ⇒ 同 ExecProfile ⇒ 同 workdir）；跨 agent 同节点并行由 hub L1 互斥兜底（workdir_busy_timeout 属环境类可重派一次） |
| 超时预算 | wait 的 timeout_sec 参数 + Prime 自身轮次预算（对齐 GROUP-RFC 预算护栏） |

**不做**：hub 内嵌编排引擎、任务 DAG 协议帧、Prime 特权 API——所有编排智能在 Prime 的 LLM 里，可靠性继续由任务全链保证。

## 五、群聊集成（对齐 GROUP-RFC MVP）

- 群成员里 Prime 是特殊 member（role=member，能力标识 orchestrator）；**host 仍是人**（RFC 护栏不变）
- 群内流程：人发目标 → @Prime → Prime 在群里发「分解方案」（子任务×执行者×顺序，附 StartLux 建议）→ 人确认/修改 → Prime 逐个转正式任务（group_task_created 自动回流）→ 各 agent 结果回流 → Prime 发总结 → 人验收 → conclude
- 预算代码强制不变：Prime 的轮次/调用次数计入群预算

**StartLux-Decision-v1.1 接口情报（2026-10-21 查实，Q3 闭合）**：
开源家族（GitHub StartLuxLabs/StartLux-Decision，HuggingFace startlux-models），0.8B/2B/4B/9B/27B 五档 × Instant/AutoThink 双路线（27B AutoThink 决策榜第一 73.37）。接口 = HTTP `POST /v1/decisions`：state（文本/JSON/图像）+ typed questions（choice/noul/score），输出每选项概率（置信度天然满足影子契约）。Instant 中位 25-52ms，适合 Prime 高频查询。
部署三选：① bamboonode.dev 免费托管 27B AutoThink（P4.1 首选验证，Bamboo API key）；② Mac mini MLX 本地（官方 4-bit/8-bit，9B≈5GB 内存，离线生产）；③ 腾讯云 2C2G 无 GPU 排除。API 兼容 System One /v1/systemone。

**三级降级策略（伦哥 2026-10-21 定：云端验证+必须有兜底）**：

| 级别 | 决策来源 | 触发条件 | 成本 |
|---|---|---|---|
| L0 | bamboonode 云端 27B AutoThink | 默认 | 网络延迟 ~1s；免费额度 |
| L1 | Mac mini MLX 本地（9B/4B） | L0 连续失败 3 次或显式配置切本地 | 内存 5GB，离线可用 |
| L2 | Prime 规则分配（兜底，永在） | L0/L1 皆不可用，或单次查询超时 5s/非 200/解析失败 | 零依赖 |

规则分配算法（L2 具体化）：能力目录关键词匹配（子任务描述 ↔ agent 描述）→ 同能力多节点取 inflight 最低 → 无匹配 → 上报用户。**每次降级记 relation=degraded**（影子数据不丢样本）。

实现形态：swarmmind-mcp 增第七工具 `startlux_decide`（env：SWARM_STARTLUX_URL/KEY；内部自动 L0→L2 降级，响应标注 used_level）。验证脚本：tests/startlux-verify.sh。
集成形态：swarmmind-mcp 增第七工具 `startlux_decide`（state+question → 概率集），对齐 SPEC 六.六 同步规则。

**影子数据契约（P4.1 开工前补齐实现，格式草案）**：每任期 JSONL（deploy/prime/shadow/<term_id>.jsonl），行结构 {ts, term_id, subtask_id, goal_digest, candidates, startlux_rec{node,agent,confidence,rationale}, prime_final, relation: adopted\|overridden\|degraded, task_id, terminal_status}。度量：top-1 命中率 + 采纳/否决组成功率对比。晋级标准（可判定）：连续 ≥50 条、命中率 ≥90%、采纳组成功率不劣 → 报伦哥批准。降级期记 relation=degraded（样本不丢）。**P4.1 验收的「10 个决策」是冒烟非收敛证据**。

## 六、分阶段交付（测试循环制适用）

| 阶段 | 交付 | 验收 |
|---|---|---|
| P4.0 Prime MVP | mac-mini 部署「zcode-dev+MCP」Prime 形态；验证复合目标（以「直接干」前缀派发，全自动形态）：「检查两台机器磁盘并汇总对比报告」→ 自动分解 2 并行 diskcheck → 收集 → 产出汇总 | 全自动完成、报告可读、全程无需人干预中间步骤 |
| P4.1 StartLux 影子 | StartLux-Decision-v1.1 服务接入；Prime 分配前查询；差异日志落盘 | 10 个分配决策有影子记录；StartLux 不可用时降级不故障 |
| P4.2 群聊 MVP | 「GROUP-RFC MVP + Prime 编排扩展」（RFC 本体 MVP 不含 host 自动决策，Prime 编排为扩展项，DoD 单列；需 MCP submit 增 group_ref 透传） | 群版 DoD 增补 + Prime 扩展 DoD 全过 |
| P4.3 依赖与产物链 | FILE-COLLAB L2（depends_on）——Prime 的手工依赖编排升级为平台原语 | L2 DoD |

**预算强制降级声明（S5）**：GROUP-RFC 的预算「代码强制」原则在 P4.0/P4.1 控制台形态下降级为 prompt 层；兜底方案（MCP 进程内调用计数超限报错 / hub 按 token 限速）二选一列入 P4.x。

**并发任期互斥（S6）**：控制台加软约束——任一 Prime 任期进行中禁止新总理派发（避免叠加派发与预算互不可见）。

## 七、风险与开放问题（含评审增补）

1. **Prime 单点**：Prime 所在节点离线 → 编排中断。V1 接受（人可从控制台接管）；V2 考虑 Prime 多实例选主
2. **LLM 决策不可解释**：Prime 为什么这么分解——要求 Prime 每步输出「决策理由」（群消息/任务 trace_id 关联），可审计
3. **StartLux 评估文档已散佚**（工作台清理）——P4.1 开工前需伦哥提供 StartLux-Decision-v1.1 的接口规格/模型文件位置
4. **成本**：Prime 每次复合目标消耗 GLM tokens——预算代码是否有 token 维度（GROUP-RFC 已留「估算/未知兜底」条款）
5. **并发写冲突**：同名 agent 并行子任务强制串行；跨 agent 同节点由 hub 互斥兜底（workdir_busy_timeout 环境类可重派）；完备解是 L2 depends_on
6. **无取消原语（M6）**：误派代价 = timeout_sec × 任务数；cancel 帧入 backlog
7. **Prime 会话崩溃接管（S7）**：in-flight 任务照常执行至终态但无人收集；任期幂等键约定（prime-<term>-<seq>）是程序化重建状态的前提
8. **提示词注入面（S7）**：Prime 持 admin 令牌而 stderr 是任意文本——提示词已含注入防御条款；MCP 层后续加内容转义
9. **产物 24h 保留与延迟验收的时序竞争（S7）**：超时统一上报/人延迟验收路径下产物可能过期——重要产物 Prime 任期内即时回读并存入报告

## 八、总理智能体规格（v1 提示词与编排算法，2026-10-21 补）

### 8.1 运行形态（V1）

- **载体**：mac-mini 的 zcode-dev 会话。人在控制台勾选「总理模式」派复合目标 → 控制台以 PRIME 系统提示词包装目标 → zcode 执行完整编排循环 → 会话即总理任期
- **提示词资产化**：系统提示词存 `deploy/prime/PRIME.md`（版本管理，控制台派发时拼接），修改走 git
- 每次派发 = 一次总理任期（无跨任期记忆）；任期结束输出完整报告

### 8.2 系统提示词（deploy/prime/PRIME.md，全文如下，修改走评审）

见仓库 `deploy/prime/PRIME.md`（与本节同步维护；要点：角色使命/MCP 六工具认知/八步工作循环/并发等待规则/失败处理矩阵/五条护栏/输出格式）。

### 8.3 编排循环状态机

```
理解目标 → 查能力目录（agents）与节点在线（nodes）
  → 分解 DAG（标注并行/串行依赖，展示分解方案）
  → [派发批] 并行 submit（≤4 并发，超出的排队）
  → [收集批] 逐个 wait（单任务 600s，二轮制）
  → 终态分流：
      completed → 收集答复/产物
      failed    → 失败矩阵处理（最多重派 1 次）
      needs_review → 阻断依赖链，立即上报人
      超时×2    → 标记超时，继续可继续部分，最后统一上报
  → 全部终态 → 汇总报告
```

### 8.4 失败处理矩阵

| 终态/错误 | 分类 | 动作 | 上限 |
|---|---|---|---|
| failed + policy_denied | 参数越界 | 修正描述后重派 | 1 次 |
| failed + 超时/连接/节点离线 | 环境类 | 原样重派（hub 幂等保护） | 1 次 |
| failed + 其他 | 未知 | 上报人（附 stderr） | 不重试 |
| needs_review | 结果不确定 | **阻断下游依赖，上报人**，不重跑 | — |
| wait 超时×2 | 进行中 | 标记超时继续其余，最后统一上报 | — |

### 8.5 护栏（写入提示词的硬约束）

1. 总工具调用 ≤60 次即停（预算保护）
2. 分解方案先行展示，再执行（人对齐窗口）
3. 前序产物未收集，不派发依赖它的子任务
4. 产物路径原样引用，禁止虚构
5. needs_review 语义神圣：不重跑、不猜测、只上报

### 8.6 并发与 hub 语义对齐

- 并行度 4 < hub 投递窗口 8/node——Prime 不会挤兑单节点窗口
- 子任务操作同一 workdir 时**禁止并行**（Workdir 互斥 P1.5 会排队，Prime 主动串行化更优）
- 幂等：重派自然产生新幂等键（hub 按 idem 去重保护同键重试）


## 八、评审闭环记录（2026-10-21）

- 评审：codex 不可用（区域 403），按协作制度降级独立 zcode 会话评审 → docs/PRIME-REVIEW.md（M1-M6/S1-S7/Q1-Q6）
- 修订：M1-M6 全部闭合（M2 含 MCP submit 增 idem_key/trace_id 参数与 SPEC 六.六同步）；S3 八处提示词补丁全收；S1 契约草案入文档（P4.1 实现）；S4c 群范围标签修正；S5/S6/S7 声明纳入
- 闭合：Q1 禁自干（入提示词）；Q2 跨机大数据 V1 禁用（入分解合法性规则）；Q5 mac diskcheck 已就绪（epoch=24 agents=3）
- 待办：Q3 已闭合（开源模型接口查实，见上）；Q4 群模式提示词变体（P4.2 决策）；Q6 反代 QoS（部署时定）