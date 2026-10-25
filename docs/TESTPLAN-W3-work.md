# TESTPLAN：P5 Work W3 批（收集/关闭+管理台+Prime 提示词+接口与清理补全）

> 依据 docs/RFC-work-resume.md v2 第五节 W3 行；测试先行（实施前定义，AGENTS 测试循环制度）。
> 执行方式：`go test ./internal/hub/core/ ./internal/hub/api/ ./internal/hub/web/ ./cmd/swarmmind-mcp/ -race -count=1`；
> 全量回归 `go test ./... -race -count=1` + `GOOS=windows go build ./...` + `GOOS=linux go build ./...` + `go vet ./...`；
> 管理台行为面按 SPEC 六.五第七条以 headless harness 走查（docs/prototypes/shoot-work-view.mjs，截图留档）。
>
> W3 核心机制定案（实施与评审共同基准）：
> - **收集=单事务终态闭合**：`swarmmind_work_collect(work_id, prime_term, report, outcome?)`
>   在一个 store.Update 内完成「校验 → status 落 done/abandoned → ClosedAt/ExpiresAt
>   （now / now+30d）盖章 → Report 落库 → collect 事件（by=自报 prime_term、
>   principal=服务端验证主体，双字段 RFC 六.2）」。**collecting 状态 V0 无写入路径**
>   （保留枚举；「最后一步终态时自动 executing→collecting」不做——与 blocked 的
>   推导语义竞争（同刻「存在阻断」与「全部终态」），可收集性由步骤状态呈现，管理台
>   据此亮动作）。
> - **校验序**（错误可行动，同 bindWorkSubmit 口径）：report 必填（空 → 400
>   invalid_request——报告是任务 24h 清理后的唯一执行摘要载体，RFC 六.3）→ 404
>   work_not_found → **终态回执判定**（已终态且 prime_term/report/outcome 与落库
>   三同源 → 200+dup:true 零写入零事件，响应丢失重试安全；不同 → 409 work_closed）
>   → 409 work_no_term（未建立任期，先接管）→ 409 work_term_conflict（收集是编排
>   写入，旧任期拒绝；与派发同规，RFC 二.2 步骤 5）→ **步骤覆盖**（任一步骤
>   pending/submitted/running → 409 work_steps_open，列出未终态步骤——全部步骤终态
>   才可收集）→ **完成性判定**（outcome 缺省或 "done"：要求全部步骤 done，存在
>   failed/needs_review/skipped → 409 work_steps_not_done，列出未完成步骤并指路
>   outcome=abandoned——**部分报告不误关闭**：hub 拒绝把部分完成静默标成完成）；
>   outcome="abandoned"：全终态即可，报告即处置理由（**阻断可解释**）。
> - **blocked 无死环**：blocked 单恒有出口——对携带阻断的步骤续派（W2 通道，
>   blocked→executing）修完收集 → done；或 outcome=abandoned 处置关闭 → abandoned。
>   done/abandoned 后三写入口全闭：绑定派发（W2 ErrWorkClosed）、接管（同）、
>   再收集（本批回执判定不匹配即 work_closed）——**done 后不可再派**。
> - **superseded 仍不预写**（W2 边界沿用）：本批无明确写入触发器；collect 的步骤
>   终态门使在途多尝试的步骤不得关闭，语义自洽。
> - **skipped 不计入完成关闭**：跳过=决定不执行≠完成；含 skipped 步骤的关闭只能走
>   abandoned（hub 与提示词同口径，防「跳过的目标」被标成「完成的目标」）。
> - **清理补全（RFC 六.3 落实）**：Sweep 增第六阶段——终态 Work（done/abandoned）
>   过 ExpiresAt（=ClosedAt+30d）整单删除并同步清除 WorkIdems 指纹映射（悬空映射
>   会使同指纹重建误走悬空分支）；未完成 Work 无 ExpiresAt 永不清理。**接口补全**：
>   WorkView 增 `tasks_gone`（GetWork 计算：尝试 task_id 已过 24h 保留期清理的列表
>   ——Work 独立记忆仍解释终局，缺口在「任务详情为何查不到」）；WorkSummary 增
>   `done_steps`（管理台步骤进度 x/y）。测试先行声明的验收口径：任务清理后
>   work_get（REST/MCP）的步骤/尝试（outcome/idem/指纹）/事件/报告全量可读且
>   tasks_gone 标注到位。
> - **管理台 Work 视图（hub 端独有，node 不加）**：新页 #/works——列表（状态徽标/
>   目标/步骤进度/尝试数/任期/创建/关闭）+ 详情抽屉（稳定分区 data-zone：head/
>   intro/steps/attempts/report/events，分区 diff 局部更新；**动作区一次建壳恒定
>   节点**，接管/续派/处置三表单草稿与 busy 不进模板串，状态驱动 disabled/hidden
>   ——阶段 D 必须修 3 同款纪律）。写动作仅接管/续派/处置，无手工建单（RFC 六.六
>   裁决 1；REST 是工具传输载体，管理凭据直接调用口径不变）。分区更新机制共用件
>   `drawerZonesApply` 以默认参数泛化（names 缺省 DRAWER_ZONES）——**shared block
>   双端同改同验**（AGENTS 禁漂移条款；锁测试 web_drawer_sync_test 逐字节比对随改
>   保绿，node 端行为零变化）；Work 状态词典为 hub 独有非任务词典（同 GROUP_STATES
>   留 shared block 外），SPEC 六.五白名单入档。
> - **Prime 提示词同步（两处一致，PRIME-DESIGN 既有约定）**：internal/hub/web/app.js
>   PRIME_PROMPT 与 deploy/prime/PRIME.md 同批增补「工作单（多步工作的断点续管）」
>   节，六条：①建单时机（多步/有依赖目标先建单，单步小任务不建）；②绑定派发三参
>   +幂等键规范（prime-<任期>-<步骤>-<尝试>，重派同键、新尝试新键）；③断点续管
>   （list 发现未完成 → get 读档 → takeover 取得续派权 → 续派）；④重试预算（单步
>   骤尝试 ≤2，任期全局 ≤3 沿用）；⑤收集与部分汇总（全终态才可收集；部分完成不得
>   标 done；needs_review 只上报；abandoned 须附理由）；⑥工具名与参数以实际装配为
>   准。**提示词与接口一致有锁**：web 包新增测试断言 app.js 提示词引用的
>   swarmmind_* 工具名 ⊆ MCP 实际装配集、PRIME_PROMPT 模板体与 PRIME.md 逐字节同文。

## 一、四条核心验收（RFC W3 行）→ 用例映射

| # | 核心验收 | 用例（包/函数） | PASS 判据 |
|---|---|---|---|
| 1 | 部分报告不误关闭 | core `TestWorkCollectGates` | 全部步骤终态才可收集：任一步骤 pending/submitted/running → ErrWorkStepsOpen（409 work_steps_open，错误文案列出步骤号与现状）、**零写入零事件**（status/Report/ClosedAt/EventSeq 原样）；非全 done + outcome 缺省/done → ErrWorkStepsNotDone（409 work_steps_not_done，列出 failed/needs_review/skipped 步骤、文案指路 outcome=abandoned）、零写入——**部分报告不得使工作单变 done**；全 done + collect → done（status/ClosedAt/ExpiresAt=now+30d/Report 落库、collect 事件恰 1 条、主体双字段 by=自报任期/principal=服务端主体）；abandoned 关闭 → abandoned 终态同上；**回执幂等**：同参（prime_term+report+outcome 与终态同源）重放 → 200+dup:true 零新增事件；报告不同/任期不同的「重放」→ 409 work_closed 不误认 dup |
| 2 | 阻断可解释 + blocked 无死环 + done 后不可再派 | core `TestWorkBlockedExitsAndClosedRejected` | blocked 单两出口：①步骤 failed → 续派（W2 通道，blocked→executing）→ 完成 → 全 done → collect → done；②直接 collect outcome=abandoned → abandoned（Report 含处置理由，事件正文含终局统计——阻断可解释）；终态后三写入口全闭：bindWorkSubmit/takeover/collect 各返回 ErrWorkClosed 且零写入（done 后不可再派含再收集）；无任期单 collect → ErrWorkNoTerm；旧任期 collect → ErrWorkTermConflict（零写入） |
| 3 | 任务清理后仍有执行摘要 | core `TestWorkSurvivesTaskCleanup` + core `TestWorkSweepTerminalRetention` | 清理面：绑定派发→任务终态回写→collect done→把任务 UpdatedAt 回拨过 24h→Sweep（任务删除）→ GetWork 仍全量可读：步骤终态/尝试（outcome、idem_key、ContentHash/BaseContentHash、attempt_ref）/事件账本/报告逐项在，`tasks_gone` 恰含已清理任务 ID（REST GET 与 MCP work_get 读档同形，MCP 尝试行标注「已过保留期」）；未终态任务清理前 tasks_gone 为空。工作单保留面：终态单 ExpiresAt 前（推进时钟 29 天）Sweep 不清；到期（31 天）Sweep 整单删（Works 无、**WorkIdems 映射同删**、同 goal+steps 重建=新 ID 非命中）；未完成单（无 ExpiresAt）推进时钟 Sweep 永不清 |
| 4 | 提示词与接口一致 | web `TestPrimePromptWorkContract` + mcp `TestWorkToolsW3Collect` | web：app.js PRIME_PROMPT 含六条锚文案（建单时机/绑定三参+幂等键/断点续管/重试预算/收集与部分汇总/工具口径）；提示词引用的全部 swarmmind_* 工具名 ⊆ cmd/swarmmind-mcp 实际装配集（提示词不给接口不存在的用法）；PRIME_PROMPT 模板体与 deploy/prime/PRIME.md 对应节**逐字节同文**（两处一致，单改文档不算改行为）；{{GOAL}} 模板标记完好。mcp：tools() 含 swarmmind_work_collect 且 hub 全链可用（建单→绑定派发→任务完成→collect done，输出含 done 与报告存档提示）；部分完成 collect 人话报错（work_steps_not_done 文案指路 abandoned）；abandoned 关闭成功；同参重放输出幂等回执；空 report 工具层拒绝（请求不发出）；node 模式不装配（hub-only 不静默降级） |

## 二、支撑用例（REST/接口面/管理台）

| 用例 | 覆盖点 | PASS 判据 |
|---|---|---|
| api `TestWorkCollectEndpoint` | REST 收集端点 | 未登录 POST /api/v1/works/{id}/collect 401；成功 200（回执重放 200+dup:true）；错误码映射 404 work_not_found / 409 work_closed / 409 work_no_term / 409 work_term_conflict / 409 work_steps_open / 409 work_steps_not_done / 400 invalid_request（report 空）；PAT 入口 collect 事件 principal=pat:<令牌名>（主体双字段同 W1 口径）；响应体 work 含 report/closed_at/expires_at |
| core `TestWorkListSummaryDoneSteps` | 列表摘要补全 | WorkSummary.done_steps 统计 done 步骤数（0/N…N/N）；closed_at 指针仅终态输出（沿用 W1 口径回归）；列表不含 Events/Steps 明细（轻量不变） |
| api `TestWorkAuthGateCollectRoute` | 新路由门禁归并 | /api/v1/works/{id}/collect 并入 TestWorkEndpointsAuthGate 的 401 矩阵 |
| web `TestConsoleWorkViewSources` | 管理台结构锁（源码面） | hub app.js：WORK_ZONES 分区壳 + 动作区一次建壳（`work-actions` 区不在分区 diff 内）；无整层重建回潮（work 抽屉刷新走 drawerZonesApply）；工作单状态词典 hub 独有留 shared block 外；S.works 经 loadResource 指纹通道（无变化不重绘）。锁测试双端 shared block 逐字节比对保绿（drawerZonesApply 泛化为默认参数，node 端字节随改） |
| harness `docs/prototypes/shoot-work-view.mjs` | 管理台三层走查（截图留档） | ①列表：真实 hub+node 播种多状态工作单，列表徽标/进度/任期呈现；②详情：抽屉六分区（头部/生命周期介绍/步骤表/尝试表/报告/事件时间线）渲染正确，打开 ≥2 轮询周期后分区节点身份不变（数据无变化不碰 DOM）、details 开合与表单草稿跨轮询保持；③动作：接管（expected 预填、成功后任期更新上屏）→ 续派（表单预填步骤声明目标、成功后尝试表+1）→ 处置（abandoned 关闭、终态徽标+报告区+动作区收起）三步全链截图，关键断言输出 stdout |

## 三、边界声明（防验收口径漂移）

- collecting 状态本批仍无写入路径（保留枚举；collect 单事务直达终态，见头部定案——自动推进与 blocked 推导冲突，V0 不做）；管理台可收集性由步骤状态推导呈现，不依赖该状态。
- dup 回执判定=「prime_term 与落库任期同 + report 与落库报告同 + outcome 与终态匹配」三同源；任一不同按 work_closed 拒绝（报告是回执身份，防他方以相同参数冒领回执——对齐 W2 接管回执的主体同源纪律）。
- 管理台续派/处置=持管理凭据的直接编排调用（SPEC 六.五「REST 是工具的传输载体」口径）：无新增角色权限，鉴权仍是会话/PAT 本身；「无管理台按钮≠权限强制」的反向同样成立——有按钮不等于 agent 获准（agent 侧纪律由提示词约束，RFC 二.3）。
- 续派表单生成的任务与 MCP 绑定派发同链同校验（同 ErrWorkStepLocked/任期/成组三参语义），管理台不新增准入旁路。
- 终态 Work 30 天自 ClosedAt 起算（ExpiresAt=ClosedAt+30d）；删除是整单硬删（快照体积纪律），不留墓碑——「30 天内可解释、到期归零」在 REST/MCP 表现为 404 work_not_found。
- Sweep 工作单清理阶段在既有五阶段之后追加，不改变任务清扫语义；WorkIdems 悬空映射随单同删（CreateWork 的悬空防御分支保留不动）。
- WorkView.tasks_gone 仅在 GetWork 读档路径计算（建单/接管/收集响应不携带——返回的是写入视角现状，非解释面注解）；MCP work_get 据此在尝试行标注，不额外发请求探测。
- 提示词增补不改任何工具 schema/参数（提示词与接口一致=用法描述与实际装配对齐，接口面零变更——除本批新工具 collect 外）。
- 文档面：RFC-work-resume.md 的「W3 仍为后续交付范围」状态行**本批不改**（工作区有他会话未提交改动，禁触碰）；状态行更新随 Hana 提交轮处理。SPEC 六.五/六.六、deploy/prime/PRIME.md 为本批提交面。
- W4（真实双机故障验收：无额外尝试/丢失绑定/重复收集）不在本批；重复收集的幂等回执本批以注入形态覆盖（核心 1）。

---

# W3 审查节（codex，实施后追加于此）

（预留）

# W3 实施闭合记录（zcode，2026-10-25）

## 用例落地（四条核心验收 + 支撑，全数 PASS）

- 验收 1（部分报告不误关闭）→ core `TestWorkCollectGates`：pending/submitted 步骤 → `ErrWorkStepsOpen`（文案列出 #3 与现状）零写入零记账；{done,failed,needs_review} 全终态但非全 done → outcome 缺省/done 均 `ErrWorkStepsNotDone`（列出 #2/#3、指路 abandoned）零写入；全 done → done 终态盖章（ClosedAt/ExpiresAt=+30d/Report/collect 事件主体双字段 by=prime-1、principal=pat:mcp）；abandoned 处置关闭（事件正文含 1/3 终局统计）；同参重放 dup 回执零记账、换报告/换任期 → `ErrWorkClosed` 不误认 dup；空 report 与非法 outcome → `ErrInvalid`。
- 验收 2（阻断可解释+无死环+终态全闭）→ core `TestWorkBlockedExitsAndClosedRejected`：出口 A（续派 failed 步骤 → executing → 全 done → collect → done）；出口 B（直接处置 → abandoned）；done 后三写入口全闭（bind/takeover/collect 均 `ErrWorkClosed` 零写入）；无任期 collect → `ErrWorkNoTerm`；旧任期 collect → `ErrWorkTermConflict` 零记账。
- 验收 3（清理后仍有执行摘要+保留清理）→ core `TestWorkSurvivesTaskCleanup`（任务回拨 24h+Sweep 删除后：步骤/尝试 outcome+幂等键+双指纹/事件/报告全量可读，`tasks_gone` 恰 3 项；清理前为空）+ core `TestWorkSweepTerminalRetention`（done 到期 -31d 整单删+WorkIdems 同删+同指纹重建=新 ID；abandoned 未到期不清；未完成单无 ExpiresAt 永不清）。
- 验收 4（提示词与接口一致）→ web `TestPrimePromptWorkContract`（六条锚文案；提示词引用工具名 ⊆ main.go 实际装配集（源码提取），且 work 五工具全部被提及；`{{GOAL}}` 恰 1 处+替换逻辑在场；PRIME_PROMPT 模板体与 PRIME.md 行为节（自「## 分配决策」起）逐字节同文恰 1 次）+ mcp `TestWorkToolsW3Collect`（hub-only 装配；真实全链建单→绑定派发→终态→collect done（输出含「已收集关闭/3 步全部完成/30 天」）→重放回执→终态再收集人话报错；部分完成 → work_steps_not_done 人话指路 abandoned → abandoned 处置关闭（输出含 1/2 统计）；空 report/缺任期/非法 outcome 工具层拒绝且请求不发出（httptest seen 断言）；node 模式不装配）。
- 支撑 → api `TestWorkCollectEndpoint`（401 门禁；200/回执 dup；错误码映射 404/work_closed/work_steps_open/work_steps_not_done/invalid_request/work_term_conflict 全数命中；PAT 入口 collect 事件 principal=pat:collector；终态盖章字段回读）+ api `TestWorkTasksGoneREST`（REST 读档 tasks_gone 与 core 同形）+ api 401 矩阵并入 collect 路由 + core `TestWorkListSummaryDoneSteps` + web `TestConsoleWorkViewSources`（分区壳/动作区一次建/无整层重建回潮/词典归属）+ harness `shoot-work-view.mjs`（21 PASS / 0 FAIL，见下）。

## 实施中的设计定案（均为 TESTPLAN 头部定案的落地形态或修订）

1. **REST 响应组装**（实施修正）：`WorkView.TasksGone` 挂 WorkView 外层而 workViewBody 序列化 `wv.Work`（store.Work）——首版 REST 响应丢 tasks_gone（测试拦住）；改为 api 包 `workBody{store.Work; TasksGone}` 包装序列化，注解随 work 对象输出（描述 work 自身事实，不落顶层）。
2. **MCP collect 输出补终局统计**：abandoned 路径输出含 `N/M 步完成`（与 hub 事件正文口径一致），部分完成场景的调用方免二次查询。
3. **管理台续派的任期跟随**：续派/处置表单提交时携带 `S.workDrawer.work.prime_term`（当前事实），接管动作成功后抽屉即刷新——harness 实测「接管后旧任期 REST 派发 409、携新任期放行」（服务端契约正确的正向证据）。
4. **loadAll 第五资源接线**（实施缺陷测试拦住）：`loadResource('works',…)` 加入 Promise.all 后漏 `commit('works', works)`——S.works 恒空、S.fresh 无 works 条目；probe 定位后补上（提交点 seq 二次校验/指纹去重与其他资源同机制）。
5. **drawerZonesApply 泛化**：shared block 双端同改（names 默认参数+注释一行），node 端零行为变化；`TestWebDrawerSharedBlocks` 逐字节锁随改保绿（未删断言）。
6. **工具清单回归 17→18**（swarmmind_work_collect 列工作单组末尾）；SPEC 六.六映射表补行+规则 12 声明；SPEC 六.五「工作单」节补 W3 三条契约（收集关闭/读档注解/存储清理）+白名单第六项（工作单视图 hub 独有）。

## 管理台三层走查（docs/prototypes/shoot-work-view.mjs，真实 hub+node，21 PASS / 0 FAIL）

- 层 1 列表：四态工作单徽标（已完成/受阻/执行中/计划中）、进度分式 2/2、任期/创建/关闭列。
- 层 2 详情抽屉：头部/介绍/步骤表/尝试表/事件时间线五分区渲染正确、无报告区空分区收敛；**12s（≥2 轮询）数据无变化分区子节点身份不变（打标记验证不碰 DOM）**、续派 details 开合跨轮询保持、表单草稿跨轮询保持（动作区一次建壳恒定节点）。
- 层 3 动作全链：接管（expected=prime-1 预填 → prime-7 上屏，头部同步）→ 续派（下拉 2 项、选步预填声明目标、改能力提交 → 尝试表 +1）→ REST 补派步骤 2 到终态 → 处置（abandoned+报告 → 已放弃徽标、动作区收起、报告区浮现、时间线收集关闭热点）；done 单动作区同样收起。
- 截图留档 docs/prototypes/shots-work-view/01…06（列表/稳定 DOM/接管/续派/处置关闭/done 抽屉）。

## 变异验证（断言非摆设）

- 变异 A：CollectWork 去掉 `work_steps_not_done` 完成性门 → `TestWorkCollectGates` FAIL（部分完成被标 done）✓
- 变异 B：去掉终态回执 dup 分支 → 同用例 FAIL（同参重放 409）✓
- 变异 C：sweepWorks 不清 WorkIdems → `TestWorkSweepTerminalRetention` FAIL（悬空映射）✓
- 变异 D：workViewBody 回退为裸 `wv.Work` → `TestWorkTasksGoneREST` FAIL（响应丢 tasks_gone，实施中真实发生并由测试拦住）✓
- 三处变异还原后复跑全 PASS（变异 D 为实施缺陷即改即验）。

## 验证结果

`go test ./... -race -count=1` 全绿（19 包）✅；`GOOS=windows go build ./...` + `GOOS=linux go build ./...` + 本机 build ✅；`go vet ./...` ✅。未 commit（提交在伦哥）。

改动文件清单：docs/TESTPLAN-W3-work.md（新）、internal/hub/core/{work_collect.go（新）,work_collect_test.go（新）,work.go,sweep.go}、internal/hub/api/{works.go,works_test.go,server.go}、cmd/swarmmind-mcp/{main.go,works_test.go,mcp_integration_test.go}、internal/hub/web/{app.js,prompt_test.go（新）}、internal/node/web/app.js（shared block 双端同改）、deploy/prime/PRIME.md、SPEC.md、docs/prototypes/shoot-work-view.mjs（新）。禁触碰面（他会话改动）未动：docs/PLAN.md、docs/README.md、docs/RFC-work-resume.md、README.md、deploy/prime/README.md 等——RFC 头部「W3 仍为后续交付范围」状态行留待 Hana 提交轮更新。

（遗留声明：collecting 状态与 superseded 结局本批仍无写入路径（边界声明见上）；管理台续派表单不带 args/env 高级参数（V0 stdin 承载，与派单页统筹模式口径一致）；tasks_gone 仅 GET 读档路径计算。）

---

# W3 审查节（codex，2026-10-25 21:10，必须修 7）

1. **[P1] collect 在途收束门缺失**（work_collect.go:119 只查 Step 状态）：W2 允许同步骤并发尝试，A 在途 B 先完成时 Step=done 即可 collect；A 随后失败经 work_dispatch.go:372 回写把已关闭 Work 改回 blocked（保留关闭时间与报告且重新可派）。→ collect 须校验全部尝试收束；关闭后迟到结果不得破坏终态。
2. **[P1] blocked 放弃出口不完整**：needs_review/failed+下游 pending 时 abandoned 被 work_steps_open 拒绝，又无把未执行步骤处置为 skipped 的路径（needs_review 按提示词必须阻断禁重跑，永远到不了「全部终态」）。出口 B 测试绕开了实际阻断场景。→ 补未执行步骤处置路径，统一契约与测试。
3. **[P1] MCP 读档丢失执行摘要与恢复证据**（main.go:2070）：toolWorkGet 不输出 report/尝试 idem_key/双指纹/attempt_ref——任务清理后 Prime 经 MCP 读不回报告也拿不到原键安全重放；work_list 未透传 done_steps。→ MCP 输出与 REST 全量对齐。
4. **[P2] 写动作迟到防护不完整**（app.js:2731）：动作响应不校验抽屉身份、不使回写前 GET 失效。复现：接管成功 p2 后迟到 GET 退回 p1；A 单响应清空 B 单输入。→ 抽屉生命周期+写动作顺序+本地回写版本三重防护。
5. **[P2] 稳定 DOM 未达标**（app.js:2673/2666）：全部可派步骤状态入签名——未选中步骤状态变化重建选项并覆盖用户修改的执行智能体草稿；终态抽屉无变化刷新仍两次 hidden 写入。→ 签名收敛到选中步骤+先比后写。
6. **[P2] Prime 幂等键规范缺工作单命名空间**（app.js:175）：prime-<任期>-<步骤>-<尝试> 在 hub 全局键作用域下，同任期两张单各自步骤 1 尝试 1 会撞键；接管后「传输重试保留原键原内容、submitted/running 继续跟踪、仅授权新执行分配新键」未写明。→ 键规范加 work 维度+接管重放说明。
7. **[P2] 验收断言不足**（harness:247 等）：闭合记录所依据的断言覆盖不完备，须按上述修复补真实场景断言。

（修复闭合记录由实施方追加）

---

# W3 审查修复闭合记录（zcode，2026-10-25，7 条全闭合）

## 逐条闭合

1. **[P1] collect 在途收束门+关闭后迟到回写不破坏终态**（审查①）：CollectWork
   步骤覆盖门扩为「步骤终态**且全部尝试收束**」双门（`workAttemptSettled`：结局
   ≠submitted；work_steps_open 文案指明在途尝试「#N（步骤 #M）」）——W2 允许同
   步骤并发尝试，A 在途 B 先完成时步骤呈 done 而执行未收束；`writebackWorkTerminal`
   与 `recoverWorks` 对 done/abandoned 单**零写入**（终态 30 天存档关闭后零变更；
   collect 全尝试门使正常流程不可达，本 guard 为防御迟到/异常数据的纵深）。测试：
   core `TestWorkCollectAttemptsGate`（A 先完成 B 在途 → 拒收零写入；B 终局 → 收集
   done）+ `TestWorkClosedWritebackImmune`（直调回写/恢复扫描终态单原样）。
2. **[P1] blocked 放弃出口补全**（审查②）：outcome=abandoned 校验序修订——
   pending 不算开步，随关闭**同事务**记为 skipped（逐步 step_updated 事件「跳过=
   决定不执行，非完成」；collect 事件含「N/M 步完成、K 步未执行随关闭跳过」）；
   submitted/running 与在途尝试仍拒。needs_review 阻断禁重跑+下游 pending 的真实
   场景由此到得了 abandoned（否则「blocked 无死环」承诺不成立）。契约文案四处
   同步：core 错误文案 / MCP collect 描述与输出（含「未派发随关闭跳过」行）/
   hub 控制台 intro 与处置表单提示 / Prime 提示词收集条。测试：core 出口 B 重写为
   真实阻断场景（failed×2+pending → abandoned，#3 skipped、事件恰 2 条）+ 出口 B'
   （needs_review 阻断合法放弃，原状态保留）+ 在途处置拒绝；api pending-skip 200
   响应断言；MCP 阻断放弃输出断言。
3. **[P1] MCP 读档与 REST 全量对齐**（审查③）：toolWorkGet 补收集报告全文/创建·
   关闭·保留至盖章时刻（fmtUTCFull，UTC 带秒）/尝试行幂等键+尝试引用
   （`<id>#aN`）+双指纹（64 hex 全量）；toolWorkList 补 done_steps（N/M 步）；工具
   描述同步（work_get「含幂等键/尝试引用/内容指纹」；work_collect「无在途执行后…
   未派发步骤随放弃关闭自动记为跳过」）；work_steps_open 人话提示更新。测试：
   `TestWorkToolsW3Collect` 增读档对齐断言（报告/键/引用/指纹正则/盖章时刻）+
   work_list 3/3 步 + 阻断放弃输出。
4. **[P2] 管理台写动作三重迟到防护**（审查④）：接管/续派/处置三动作统一——发送前
   `wd.seq++` 作废回写前在途读档（防护②）；响应先验抽屉身份 `S.workDrawer !== wd`
   （防护①：换单/关层后响应作废，不清他单表单、不误刷新）；成功推进 `wd.rev++`
   本地回写版本（防护③），fetchWorkDetail 在途期间版本变化即丢弃过时应答（防写
   动作已上屏的新事实被旧快照回退）。源码锁：prompt_test 断言三动作各含身份校验/
   seq++/rev++（计数=3）+ rev 丢弃守卫在场。
5. **[P2] 稳定 DOM 收敛**（审查⑤）：`sec.hidden`/`box.hidden` 先比后写（无变化
   刷新零属性写）；续派下拉签名收敛为「可派步骤 seq 集+选中步骤状态」——未选中
   步骤状态流转不重建选项；预填守卫 `sel.value !== prev` 才 workStepPicked（选中
   步不变时刷新不重预填，用户改过的执行者草稿不被无关刷新冲掉）。源码锁：先比后
   写两行+预填守卫在场、旧全量状态签名不得回潮。
6. **[P2] 幂等键规范加工作单命名空间+接管重放说明**（审查⑥）：键格式
   `prime-<任期>-<工作单>-<步骤>-<尝试>`（hub 全局键作用域，工作单维度必带防同
   任期跨单撞键）；「工作单」节新增**幂等键与接管重放**条：传输重试保留原键原
   内容（同键重放命中幂等回执）、原尝试 submitted/running 继续 wait 跟踪不换键、
   仅明确授权的新执行分配新键。PRIME_PROMPT 与 PRIME.md 两处同批同改（逐字节锁
   随改保绿），prompt_test 锚文案更新（新条三段锚+收集条新口径）。
7. **[P2] harness 真实场景断言**（审查⑦）：shoot-work-view.mjs 21→26 断言——
   层 2.5 草稿保护（播种第五单：选步 1 改执行者草稿 → REST 派发步骤 2 长任务 →
   轮询见状态流转后选中与草稿保持）；层 4 迟到防护（取接管前快照 → 显式触发一次
   读档并经一次性 route fulfill 延迟 2.5s 回放旧应答——真实「应答已在途、服务端
   随后变更」迟到形态 → 接管响应先落地 → 旧读档到达不回退任期展示与头部）。

## 设计定案修订（对头部定案两处修订，随审查闭合生效）

- 「步骤覆盖：任一步骤 pending/submitted/running → work_steps_open」修订为：
  done 关闭要求全部步骤执行完（pending 亦拒）；abandoned 关闭只需无在途执行
  （pending 随关闭记 skipped）且**全部尝试收束**。
- 「abandoned：全终态即可」修订为「无在途执行即可，未派发步骤随关闭处置为
  skipped」——needs_review 阻断禁重跑场景因此有合法放弃出口。

## 变异验证（新断言非摆设）

- 变异 A（去掉尝试收束门）→ `TestWorkCollectAttemptsGate` FAIL ✓
- 变异 B（去掉终态回写保护）→ `TestWorkClosedWritebackImmune` FAIL（status=
  blocked、seq 8→9，迟到回写被如实放行）✓
- 变异 C（去掉 pending-skip 路径）→ `TestWorkBlockedExitsAndClosedRejected`
  出口 B FAIL（真实阻断场景被 work_steps_open 拒绝）✓
- 变异 D（接管不 seq++ + 读档不比 rev）→ 源码锁 `TestConsoleWorkViewSources`
  FAIL（wd.seq++ 计数 2/3 + rev 守卫缺失）✓；harness 层 4 行为面对该变异不敏感
  （5s 轮询 tick 会在迟到窗口内 bump seq 使旧读档失效）——防护缺失的确定性检测
  由源码锁承担，行为正确性由层 4 场景正向钉住，两层各司其职
- 变异 E（旧全量签名回潮）→ 源码锁 FAIL（旧签名禁回潮断言）✓；harness 层 2.5
  仍 PASS——预填守卫（选中不变不重预填）独立挡住草稿冲刷，签名收敛由源码锁逐字
  钉死（用户可见行为+实现形状双层钉住）
- 全部变异还原后复跑全 PASS（grep 确认无本批 MUTATION 标记残留；work_dispatch.go
  `"sort" // MUTATION-D` 为 W2 批既有注释非本批产物）

## 验证结果

`go test ./... -race -count=1` 全绿（19 包）✅；`GOOS=windows` + `GOOS=linux` +
本机 build ✅；`go vet ./...` ✅；harness 四层走查 26 PASS / 0 FAIL（截图 01–08
留档 docs/prototypes/shots-work-view/）✅。未 commit（提交在伦哥）。

改动文件清单（本修复轮）：internal/hub/core/{work_collect.go,work_dispatch.go,
work_collect_test.go}、internal/hub/api/works_test.go、cmd/swarmmind-mcp/{main.go,
works_test.go}、internal/hub/web/{app.js,prompt_test.go}、deploy/prime/PRIME.md、
SPEC.md（六.五收集契约与管理台视图两行、六.六映射行与规则 12）、
docs/prototypes/shoot-work-view.mjs、docs/TESTPLAN-W3-work.md（本记录）。
禁触碰面未动：docs/PLAN.md、docs/README.md、docs/RFC-work-resume.md、README.md、
deploy/prime/README.md 等他会话改动（RFC 头部状态行仍留 Hana 提交轮）。
