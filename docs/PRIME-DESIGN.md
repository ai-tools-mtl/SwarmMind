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

## 三、StartLux-Decision：影子建议，不是自动驾驶

**决策链**：Prime 分解出子任务 → 向 StartLux 查询「该子任务派给谁」→ Prime 审核（能力匹配/负载/依赖/成本）→ 派发（采纳或改派，**记录差异**）。

- **影子原则**（对齐 PLAN「影子建议先行」）：V1 阶段 StartLux 输出仅为建议，Prime 拥有否决权；每次「建议 vs 实际」的差异落盘，作为 StartLux 迭代数据
- **接口形态**：独立进程（stdio/HTTP 均支持），StartLux-Decision-v1.1 模型加载；不可用时 Prime 降级为「自身规则分配」（目录匹配 + 负载均衡），系统不停摆
- **晋级路径**：影子数据积累 → 差异率收敛 → 伦哥批准 → StartLux 从「建议」晋级「默认采纳」（Prime 只做安全否决）

## 四、编排能力 = MCP 原语组合（零新增协议）

| 编排需求 | Prime 的做法（全部用现有 MCP 工具） |
|---|---|
| 并行任务 | 连续 submit（hub 并发窗口已支持）→ 逐个 wait |
| 串行依赖 | submit A → wait A → artifact 取 A 产物 → 产物内容嵌入 B 的任务描述 → submit B |
| 失败处理 | wait 返回 failed → 读 stderr → 重派（新幂等键）或上报人 |
| needs_review | 视为终态阻断：Prime 上报人，不自行重跑（对齐 4.8） |
| 超时预算 | wait 的 timeout_sec 参数 + Prime 自身轮次预算（对齐 GROUP-RFC 预算护栏） |

**不做**：hub 内嵌编排引擎、任务 DAG 协议帧、Prime 特权 API——所有编排智能在 Prime 的 LLM 里，可靠性继续由任务全链保证。

## 五、群聊集成（对齐 GROUP-RFC MVP）

- 群成员里 Prime 是特殊 member（role=member，能力标识 orchestrator）；**host 仍是人**（RFC 护栏不变）
- 群内流程：人发目标 → @Prime → Prime 在群里发「分解方案」（子任务×执行者×顺序，附 StartLux 建议）→ 人确认/修改 → Prime 逐个转正式任务（group_task_created 自动回流）→ 各 agent 结果回流 → Prime 发总结 → 人验收 → conclude
- 预算代码强制不变：Prime 的轮次/调用次数计入群预算

## 六、分阶段交付（测试循环制适用）

| 阶段 | 交付 | 验收 |
|---|---|---|
| P4.0 Prime MVP | mac-mini 部署「zcode-dev+MCP」Prime 形态；验证复合目标：「检查两台机器磁盘并汇总对比报告」→ 自动分解 2 并行 diskcheck → 收集 → 产出汇总 | 全自动完成、报告可读、全程无需人干预中间步骤 |
| P4.1 StartLux 影子 | StartLux-Decision-v1.1 服务接入；Prime 分配前查询；差异日志落盘 | 10 个分配决策有影子记录；StartLux 不可用时降级不故障 |
| P4.2 群聊 MVP | GROUP-RFC MVP 全量（协议 6 帧+群页+Prime 群内编排） | 群版 DoD 增补全过 |
| P4.3 依赖与产物链 | FILE-COLLAB L2（depends_on）——Prime 的手工依赖编排升级为平台原语 | L2 DoD |

## 七、风险与开放问题（供 codex 重点审查）

1. **Prime 单点**：Prime 所在节点离线 → 编排中断。V1 接受（人可从控制台接管）；V2 考虑 Prime 多实例选主
2. **LLM 决策不可解释**：Prime 为什么这么分解——要求 Prime 每步输出「决策理由」（群消息/任务 trace_id 关联），可审计
3. **StartLux 评估文档已散佚**（工作台清理）——P4.1 开工前需伦哥提供 StartLux-Decision-v1.1 的接口规格/模型文件位置
4. **成本**：Prime 每次复合目标消耗 GLM tokens——预算代码是否有 token 维度（GROUP-RFC 已留「估算/未知兜底」条款）
5. **并发写冲突**：Prime 派发的子任务若操作同一 workdir，触碰 Workdir 互斥（P1.5）——Prime 分解时须感知（F-COLLAB L2 的 depends_on 是正解，MVP 靠 Prime 自觉避开同 workdir 并行）

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
