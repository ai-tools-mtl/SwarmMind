# RFC: P5 Work 工作单——执行事实与断点接管

> 状态：v2（2026-10-25，Hana 起草 v1 + codex 架构探讨后修订；三方探讨结论已吸收）
> 依据：PRODUCT-DIRECTION-v2 P5 重定义；zcode 现状盘点（13 条差距）；codex 意见书（必须修：Attempt 模型/六.六同步/保留期矛盾/接管事务）。
> 裁决记录：codex 三条必须修全采；批次重排为 W1-W4（见 5 节）；开放问题三项按 codex 立场采纳。

## 一、问题

Prime（智行管家）把整件工作拆成多任务派发后，编排状态（计划、失败矩阵决策、进度意图）只活在 Prime 的 agent 会话里。Prime 崩溃/掉线：任务继续跑，但**没人收集结果、没人知道计划全貌、接管者只能猜**。v2 定义的两个缺口：执行事实与接管、多步骤断点查看。

## 二、核心设计：一个能活过 Prime 生命周期的 Work 实体

**一句话**：把工作层的身份与记忆从 agent 上下文搬进 hub 快照。

### 2.1 数据模型（hub store 新增）

```
Work { id: w-<rand>       goal: string       prime_term: string
  status: planning|executing|blocked|collecting|done|abandoned
  steps: []Step           events: []Event（账本）  report: string（收集报告）
  created_at / closed_at / expires_at（终态 30 天，未完成不清理） }
Step { seq  desc  target_node/target_agent  depends_on:[]seq
  status: pending|submitted|running|done|failed|skipped  note }
Attempt { seq: int       step_seq: int      task_id: t-…
  idem_key: string        outcome: submitted|done|failed|superseded }
```

**实体链：Work → Step → Attempt → Task**（尝试与任务解耦，codex 必须修 ①）。
- **重试语义统一**：请求级重试沿用同一尝试键（幂等去重命中原任务）；新的执行尝试（换内容/换任期续派）分配**新 Attempt**。换任期本身不产生新尝试。PRIME-DESIGN「失败后同键重派」按此修正（同键重派会命中旧任务或冲突）。
- **Task.WorkRef+AttemptRef**：照抄 GroupRef 先例（帧不携带、参与幂等指纹、管理面登记）。
- **Work 独立记忆**（codex 必须修 ③）：任务 24h 归档清理后，Work 保留 Attempt 摘要（键、终态、关键输入输出摘要）与去重证据——30 天后 Work 仍能解释执行事实；旧任务清理后同尝试重试不会重新执行（去重证据在 Work）；上游依赖材料到期后下游**明确阻断+要求人工决定**，不自动重跑上游补材料。二进制产物不续保（引用到期显示状态）。
- **事件账本保证**：seq 单调、事件编号稳定、重复回调不重复记账、快照变更与事件原子提交；**滚动保留**（沿群消息模式），不冒充永久审计账本。
- 持久化模板：ControlBatch 两阶段 + recoverBatches（盘点 A1）。

### 2.2 生命周期（含接管事务）

1. **建单**：Prime 经 `swarmmind_work_create(goal, steps[])` → 幂等：同 goal+steps 指纹重复调用返回同单（响应丢失重试安全）。步骤校验：唯一/引用有效/无环/级联上限。
2. **派发**：按 depends_on 逐批 submit，任务带 WorkRef+AttemptRef → hub 原子绑定（回写 step+attempt，绑定失败不发布）。
3. **回写**：任务终态 → hub 回写 attempt.outcome + step 状态（任务已落库未回填的窗口由恢复扫描补齐）；needs_review 阻断下游沿现有门控，且 needs_review 不重跑。
4. **收集**：全部步骤终态 → `swarmmind_work_collect(work_id, report)` → 部分报告不误关闭（校验步骤全覆盖）。
5. **接管**（原子事务，codex 必须修 ④）：新任期 `swarmmind_work_get` 读档 → `takeover` 原子操作：**同一时刻仅一方接管成功**（乐观锁/代校验）；接管成功后才允许续派；相同操作重放不新增尝试；**旧任期写入拒绝**（任务结果回流不拒——执行事实照收，编排写入拒）；接管记录入账本。

### 2.3 失败矩阵外置

hub 只记事实（步骤终态+事件+尝试），**决策由任期内的 Prime 做并向 Work 登记**——hub 不抢编排权，只保证事实不丢、接管者有据可续。

## 三、接口面（六.六同步开发制，同批交付）

- **MCP 工具组**（hub 侧）：`swarmmind_work_create` / `swarmmind_work_get`（含事件游标读取，不另增工具）/ `swarmmind_work_list` / `swarmmind_work_collect` / `swarmmind_work_takeover`（接管+续派）。`swarmmind_submit` 扩展 work_ref/attempt 绑定参数（援引规则 7 参数扩展）。hub-only 工具 node 模式明确不可用（不静默降级）。
- **REST 与 MCP 同批**（codex 必须修 ②）：W1 若暴露 REST 则同批交付映射表+工具+实测；不提供泛化 DELETE/PUT（Work 无 update/delete，状态只由生命周期动作推进）。
- **管理台**：hub 端 Work 视图（列表+详情：步骤表+尝试表+事件时间线+收集报告）。写动作逐项列明：仅接管/续派/处置（放弃），无手工建单。双端契约+稳定 DOM 随行；node 端 v1 不加（白名单入档）。
- **Prime 提示词同步**（W3）：建单、发现未完成工作、取得续管权、重试预算、部分汇总、幂等键规范全部进 PRIME_PROMPT——只加工具不会自然形成可靠续管行为。

## 四、边界与推迟（防范围膨胀）

1. agent 间协商协议：不做（读档续管替代）。
2. node 端步骤粒度 ledger：不做。
3. Git 集成（L3）：维持推迟。
4. hub 自动重派/自动选主/后台接管守护：不做。
5. 通用工作流语言/任意计划编辑（改 DAG、插步骤、回滚已完成步骤）：不做；计划建单时定死。
6. agent 签名体系：不做（hub 记账+鉴权主体区分逻辑归属，codex 开放问题 ②立场）。
7. 二进制产物永久镜像/整任务日志永久保留：不做（24h 沿既有）。
8. 泛化 DELETE/PUT：不提供。
9. 群聊公示可选发，不承担绑定语义。
10. 收集报告永久保留：不做（随 Work 30 天，支持导出）。

**不推迟项**（codex）：代次检查、原子绑定、去重证据、最小输入/结果留存——直接决定能否避免盲目重派。

## 五、分批实施（dev/p5-work-resume 分支，codex 修正版）

各批 TESTPLAN/DoD 在**实施前**定义（codex：DoD 不能当验收物）；合入 main 服从 P4 观察期 Gate。

| 批 | 内容 | 核心验收 |
|---|---|---|
| W1 | 模型+事务+账本+创建幂等+只读查询；REST 暴露则同批 MCP | 建单响应丢失仍返同单；旧快照兼容；写失败不发布；事实与事件一致 |
| W2 | 原子接管+绑定提交+多次尝试+终态回写+接口补全 | 两接管者仅一方成功；旧任期被拒；快速终态不丢；任务落库未回填可恢复；needs_review 不重跑 |
| W3 | 收集/关闭+管理台视图+Prime 提示词+接口缺口复核 | 部分报告不误关闭；blocked 无死环；任务清理后仍可解释事实；双端契约合规 |
| W4 | 真实双机全生命周期故障验收（杀 Prime/杀 hub） | 恢复后无额外尝试、无丢失绑定、无重复收集；断线+权限拒绝复验 |

## 六、开放问题裁决（Hana 终裁，采 codex 立场）

1. **建单入口**：v1 仅 Prime/MCP 建单；管理台只读+接管/处置（写动作逐项列明）。
2. **账本身份**：hub 记账，服务端记录可验证鉴权主体；prime_term 是逻辑归属非身份证明，两者分开陈述。
3. **清理策略**：任务/二进制沿 24h；终态 Work 保留 30 天（自关闭起算），未完成 Work 不静默清理；报告随 Work 保留+支持导出，不单独永久化。
