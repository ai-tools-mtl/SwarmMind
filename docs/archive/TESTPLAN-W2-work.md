# TESTPLAN：P5 Work W2 批（原子接管+绑定提交+多次尝试+终态回写+接口补全）

> 依据 docs/RFC-work-resume.md v2 第五节 W2 行；测试先行（实施前定义，AGENTS 测试循环制度）。
> 执行方式：`go test ./internal/protocol/ ./internal/hub/mailbox/ ./internal/hub/store/ ./internal/hub/core/ ./internal/hub/api/ ./cmd/swarmmind-mcp/ -race -count=1`；
> 全量回归 `go test ./... -race -count=1` + `GOOS=windows go build ./...` + `GOOS=linux go build ./...` + `go vet ./...`。
>
> W2 核心机制定案（实施与评审共同基准）：
> - **绑定提交=单事务原子**：任务落库、尝试登记、步骤回写、work 状态推进、attempt_recorded
>   事件在同一 store.Update 内完成；绑定校验任一失败 → mutate 返回错误 → 事务整体拒绝，
>   任务/尝试/事件零发布（RFC 二.2「绑定失败不发布」）。ControlBatch 两阶段模板的**恢复扫描**
>   （recoverWorks）作为防御纵深随批交付，覆盖「任务已终态而尝试未回填」窗口（验收 4 以
>   注入窗口实测恢复，正常路径窗口为零——原子绑定使尝试 task_id 恒非空）。
> - **接管=CAS 事务**：takeover(work_id, expected_term, new_term) 单事务——expected_term
>   与落库 PrimeTerm 不符即拒绝（同刻两接管者仅一方成功）；expected==current==new 为重放
>   幂等（零写入零事件）；成功则改写任期+takeover 事件入账。绑定派发须带 prime_term 且与
>   落库任期严格相等（旧任期编排写入拒绝）；任务终态回写**无任期校验**（执行事实照收）。
> - **指纹规范形**：work_ref + step_seq 参与提交幂等指纹（Task 落库侧以 WorkRef+WorkStep
>   同形参与）；attempt_ref 为 hub 分配号**不进指纹**（重试回放不可声明 hub 分配号，指纹
>   双方只能取请求可声明字段；尝试唯一性由「同键重放短路于 idem 查重（先于绑定）」构造保证）。
>   同键换 work/换步骤 = 409 conflict（绑定是执行去重关键面，区别于群引用「换群=新任务」的
>   路由装饰语义）；prime_term 为任期归属标签非内容，**不进指纹**（换任期后同键重放命中
>   dup 返原任务，不冲突不重复执行）。
> - **回写映射**（任务终态 → attempt.outcome + step.status，均首落终态事务内同账）：
>   completed→done/done；failed→failed/failed；needs_review→needs_review/needs_review；
>   cancelled→cancelled/failed。任一非 done 终局 → work.status=blocked（存在阻断，决策在
>   Prime）；绑定派发使 planning→executing、且对**携带阻断的步骤本身**再派时 blocked→executing。
>   枚举扩展（RFC 四值 outcome/六值 step 之外）：outcome+needs_review/cancelled、
>   step+needs_review——理由：SPEC 4.8 禁止把 needs_review 折叠为 failed；Work 独立记忆须在
>   任务 24h 清理后仍可解释终局（cancelled 折叠为 failed 会丢失取消事实）。superseded 保留
>   W3（本批无明确写入触发器，不预写）。
> - **事件主体**（AGENTS「审计/事件主体禁止采信请求自报」W2 起同规）：attempt_recorded 的
>   principal 取提交链**服务端已验证**身份（REST 提交=服务端覆盖的 SubmittedBy=admin、
>   node 帧提交=会话身份 node_id——hub 按连接/会话覆盖，非请求自报原样采信），by=自报
>   prime_term；系统终态回写事件 by=hub、principal 空（服务端自身动作，无外部请求主体）。

## 一、五条核心验收（RFC W2 行）→ 用例映射

| # | 核心验收 | 用例（包/函数） | PASS 判据 |
|---|---|---|---|
| 1 | 同刻两接管者仅一方成功；相同操作重放不新增尝试/事件 | core `TestWorkTakeoverAtomicSingleWinner` | CAS：任期满（expected ≠ 落库任期）→ ErrWorkTermConflict（409 work_term_conflict）、零写入零事件；先后接管（T1→T2 成功后 T3 以 expected=T1 再接管）被拒；**并发两接管者**同 expected 同时 takeover → 恰一方成功（另一方 conflict），账本恰 1 条 takeover 事件；重放（expected==current==new）→ dup=true 返现状、零新增事件、Attempts 不变；终态单（注入 status=done）接管被拒 ErrWorkClosed；new_term 空被拒 ErrInvalid；事件 by=新任期自报、principal=服务端主体（随接管持久化，重启后仍在） |
| 2 | 旧任期编排写入拒绝而任务结果事实回流不拒；接管成功后才可续派 | core `TestWorkOldTermRejectedFactsFlowBack` | 建单（term=T1）→ T1 绑定派发成功（尝试 1）→ 接管 T1→T2 → **T1 再绑定派发被拒** ErrWorkTermConflict（零任务零尝试零事件）；**T1 的在途任务终态 → 回写照收**（attempt.outcome/step.status/work.status/事件全落，无任期校验）；建单时未设任期的工作单直接派发被拒 ErrWorkNoTerm（须先接管建立任期）；接管后新任期派发成功 |
| 3 | 绑定提交原子+请求重试同键、新执行尝试新 Attempt（多次尝试）；尝试去重证据 | core `TestWorkBoundSubmitReplayAndAttempts` | 绑定派发（idem=K）→ 尝试 1（task_id 恒非空——原子绑定不变量）、step=submitted、work planning→executing、attempt_recorded 事件恰 1 条；**同请求重放**（同键同内容）→ dup 命中原任务、仍 1 次尝试、账本零增长；**新执行尝试**（同步骤新键）→ 新 Attempt（seq 递增不回退）、旧尝试 outcome 不被改写、step 回 submitted；任务 24h 清理后（Sweep 驱动）同键重放 → **尝试去重证据命中**：零新任务零新尝试、响应标注 task_gone=true 与原尝试终局；绑定校验失败/后续链失败（体积超限）→ **零发布**（无任务无尝试无事件、work 原样） |
| 4 | 任务落库未回填窗口可恢复（快速终态不丢） | core `TestWorkTerminalWriteback`（正常路径+快速终态）+ core `TestWorkRecoverBackfillWindow`（恢复扫描） | 正常路径：绑定后立即回传终态（快速终态，bus 同步链）→ attempt.outcome/step/work.status/事件同事务落全；四映射逐项（completed/failed/needs_review/cancelled——cancel 走 CancelTask、expire 走 Sweep）；重复回传同终态 → dup ack 分支零重复记账（EventSeq 不变、attempt 不变）。恢复：注入窗口（任务已终态、attempt 仍 submitted、step 未回写——模拟回写事务前崩溃）→ Close→Open→新 Core 启动扫描补齐（outcome/step/status/恢复事件各 1 条）→ 二次重启幂等（零新增事件）；不变量：全部正常路径后不存在 task_id 为空的尝试 |
| 5 | needs_review 阻断下游且不重跑 | core `TestWorkNeedsReviewBlocksDownstreamNoRerun` | 上游步骤任务回传 needs_review → attempt.outcome=needs_review、step=needs_review、work=blocked；下游任务（声明任务级 DependsOn=上游 idem，沿现有门控）→ blocked_reason=upstream_failed、不投递（node 零 offer）；**hub 零自动重跑**：推进时钟+Sweep+dispatch 后无新任务无新尝试（work 步骤无自动派发者，RFC 四.4）；同步骤人工再派（新键）**允许**（决策在 Prime，RFC 二.3）→ 新尝试登记、work blocked→executing |

## 二、支撑用例（指纹/校验矩阵/接口面/六.六）

| 用例 | 覆盖点 | PASS 判据 |
|---|---|---|
| mailbox `TestWorkBindingFingerprint` | 绑定指纹规范形 | 同键同内容同 work 同步骤重放 → dup；同键仅 work_ref 不同 → ErrConflict；同键仅 step_seq 不同 → ErrConflict；同键仅 prime_term 不同 → **dup**（任期不进指纹）；未绑定请求指纹不受结构扩展影响（未绑定请求间重放仍 dup、与绑定请求互不命中） |
| core `TestWorkBindingValidationMatrix` | 绑定准入 | 未知 work → ErrWorkNotFound；step_seq 越界（0/超 N）→ ErrInvalid；work 终态（注入 done）→ ErrWorkClosed；步骤已 done/skipped（注入）→ ErrWorkStepLocked 再派被拒；失败/needs_review 步骤再派**放行**（新尝试）；WorkRef 给出而 step_seq 缺失（或反之）→ ErrInvalid；node 帧路径同链同语义（SubmitFrame 冒烟） |
| api `TestWorkTakeoverEndpoint` | REST 接管端点 | 未登录 401；POST /api/v1/works/{id}/takeover 成功 200（dup 重放 200+dup:true）；404 work_not_found；409 work_term_conflict；终态单 409 work_closed（注入）；事件主体 admin/PAT 双入口落账（同 W1 主体口径） |
| api `TestWorkBoundSubmitREST` | REST 绑定提交 | POST /api/v1/tasks 携 work_ref/step_seq/prime_term → 202 + 响应含 work{work_id,step_seq,attempt_seq,attempt_ref,outcome:"submitted"}；错误码映射 404 work_not_found / 409 work_term_conflict / 409 work_no_term / 409 work_step_locked；任务详情 GET 回读 work_ref/work_step_seq/attempt_ref |
| mcp `TestWorkToolsW2BindingAndTakeover` | 六.六 同批（规则 11） | tools() 含 swarmmind_work_takeover；nodeBackend.tools() **不含**且 node 模式 submit 携绑定参数**显式报错**（不静默忽略）；真实 hub 全链：建单→接管→submit（绑定参数）→work_get 见尝试与 executing；takeover 冲突人话化（isError=true）；step_seq 严格校验（"2.5"/"1" 字符串拒绝、请求不发出）；status 返回体透传工作单绑定行 |

## 三、边界声明（防验收口径漂移）

- W2 无收集/关闭：collecting/done/abandoned 状态本批不产生（终态单相关校验以注入形态测试，为 W3 防御纵深）；ExpiresAt/ClosedAt 恒零值；Sweep 不清理未完成 Work。
- hub 不强制步骤 DAG 门控（RFC 二.3「hub 不抢编排权」）：绑定派发准入只校验步骤存在/未完成/任期相符；下游阻断沿任务级 DependsOn 现有门控（验收 5 的「阻断下游」即该机制）+ hub 无自动重派者（RFC 四.4）。
- 步骤并发尝试合法（Prime 决策自由）：同步骤多在途尝试时，步骤状态以**最后落盘的终态事实**为准（单写事务串行化，last-writer-wins）；旧尝试 outcome 各自随其任务回写、互不覆盖。
- 尝试去重证据仅覆盖「同键重放不重新执行」；跨 Work 的 idem 键复用由任务级指纹冲突兜底（同键不同 work_ref → conflict）。
- 接管事件 by=new_term（自报任期标签）、principal=服务端主体；attempt_recorded 事件 by=自报 prime_term、principal=提交链服务端已验证身份（设计定案见头部）；系统回写 by=hub。
- 协议层零破坏：Task/TaskSubmitRequest 新字段全 omitempty，未绑定路径帧字节不变、指纹不变；node 间帧不携带绑定参数（同 GroupRef 口径），node 本地模式显式拒绝。
- MCP swarmmind_submit 为参数扩展（六.六规则 7 援引），非新工具；swarmmind_work_takeover 新增并同批补映射表行+规则 11 声明；hub-only node 模式不装配（不静默降级）。

---

# W2 审查节（codex，实施后追加于此）

（预留）

# W2 实施闭合记录（zcode，2026-10-25）

## 用例落地（五条核心验收 + 支撑，全数 PASS）

- 验收 1 → core `TestWorkTakeoverAtomicSingleWinner`：CAS 拒绝零写入、并发恰一方成功（账本恰 1 条 takeover）、重放 dup 零记账、终态单拒绝、事件 by/principal 双字段断言。
- 验收 2 → core `TestWorkOldTermRejectedFactsFlowBack`：旧任期绑定派发被拒（零任务零尝试零事件）、其任务终态照收回写（回写无任期校验）、无任期单先拒后接管可续派、任期声明缺失 ErrInvalid。
- 验收 3 → core `TestWorkBoundSubmitReplayAndAttempts`：原子绑定不变量（task_id 恒非空）、同请求重放 dup 零新增、同键换步骤 409、新执行尝试 seq 递增旧尝试不改写、任务清理后同键重放命中去重证据（task_gone=true 带原尝试终局、零新任务零新尝试）、超限输入绑定失败零发布（含绑定回写整体回滚）。
- 验收 4 → core `TestWorkTerminalWriteback`（快速终态+四映射+重复回传零记账+cancel/Sweep 路径）+ core `TestWorkRecoverBackfillWindow`（注入窗口 → 重启恢复补齐+恢复事件 → 二次重启幂等 + 空 task_id 不变量）。
- 验收 5 → core `TestWorkNeedsReviewBlocksDownstreamNoRerun`：needs_review 回写+work blocked、下游任务级 DependsOn 门控不投递、推进时钟+Sweep+dispatch 后零自动重跑、同步骤人工再派允许且 blocked→executing。
- 支撑 → mailbox `TestWorkBindingFingerprint`、core `TestWorkBindingValidationMatrix`（含 node 帧路径 SubmitFrame 冒烟）、api `TestWorkTakeoverEndpoint`、api `TestWorkBoundSubmitREST`、mcp `TestWorkToolsW2BindingAndTakeover` + `TestWorkBindingSubmitStrictValidation` + `TestNodeSubmitRejectsWorkBinding`。

## 实施中的设计定案（评审对照基准，均为 TESTPLAN 头部定案的落地形态或修订）

1. **绑定挂点=全部校验之后、任务落库之前**（submit.go 步骤 5 重排）：实施中发现绑定若先于体积复查，后续链失败会经 txnErr+nil 带着绑定回写提交事务（标志位模式只对「拒绝点尚无变更」无害）——绑定一旦发生，其后任何失败（盖章后体积复查、信箱入队）改以 **mutate 错误返回**整体拒绝。新模式已入 AGENTS/CLAUDE 通用工程清单（两文件同步）。
2. **去重证据先于派发准入**（bindWorkSubmit 校验次序修订）：同键重放是对既有尝试的回执补拉而非新编排写入，故去重查重前移到终态单/任期/步骤锁定之前——完成步骤上的任务清理后重放照常取回原尝试（零写入零事件），未命中才做派发准入。TESTPLAN 头部「绑定失败不发布」语义不变。
3. **ErrWorkNoTerm 语义收敛**：绑定派发必须声明任期（缺失=ErrInvalid 400）；声明了任期而工作单从未建立任期=ErrWorkNoTerm 409（先接管，报错可行动）；已建立任期而不符=ErrWorkTermConflict 409。三态分工有测试钉住。
4. **事件主体接线**：attempt_recorded 的 principal 取 task.SubmittedBy（提交链服务端覆盖值：REST=admin、node 帧=会话 node_id，非请求自报原样）；接管事件 principal=REST 鉴权主体（authPrincipal）；系统回写 by=hub、principal 空。
5. 工具清单回归 16→17（swarmmind_work_takeover 列工作单组末尾）；SPEC 六.六映射表补行+submit 行更新+规则 11 声明；SPEC「工作单」节补 W2 五条契约（takeover 端点/绑定派发/终态回写/接管语义/错误码）。

## 变异验证（断言非摆设）

- 变异 A：ContentHash 去掉绑定字段 → `TestWorkBindingFingerprint` FAIL（同键换单变 dup 被拦）✓
- 变异 B：bindWorkSubmit 旁路任期校验 → `TestWorkOldTermRejectedFactsFlowBack` FAIL ✓
- 变异 C：result 路径旁路 writebackWorkTerminal → `TestWorkTerminalWriteback` FAIL ✓
- 三处还原后复跑全 PASS。

## 验证结果

`go test ./... -race -count=1` 全绿（19 包）✅；`GOOS=windows go build ./...` + `GOOS=linux go build ./...` + 本机 build ✅；`go vet ./...` ✅。未 commit（提交在伦哥）。

改动文件清单：docs/TESTPLAN-W2-work.md（新）、internal/protocol/types.go、internal/hub/mailbox/idem.go、internal/hub/mailbox/idem_work_test.go（新）、internal/hub/store/work.go、internal/hub/core/work_dispatch.go（新）、internal/hub/core/work_dispatch_test.go（新）、internal/hub/core/{submit,result,cancel,sweep,core,work}.go、internal/hub/api/{server,works}.go、internal/hub/api/works_test.go、cmd/swarmmind-mcp/{main.go,mcp_integration_test.go,works_test.go}、SPEC.md、AGENTS.md、CLAUDE.md。

---

# W2 审查节（codex，2026-10-25 14:20，必须修 8 + 建议修 2）

1. **[P1] 清理后去重证据不足**（work_dispatch.go:191）：只比 work 内 idem_key+step_seq，无内容指纹/主体/目标证据——同键改输入返回 dup 而非 conflict；跨 Work 键不查重可产生新执行；知 Work+键+步骤的另一 node 可获原 task_id。→ 持久化与原提交一致的规范指纹+必要身份边界，明确跨 Work 键冲突规则（允许旧任期补拉回执≠允许不同内容/主体命中）。
2. **[P2] 尝试去重未先于完整派发准入**（submit.go:99→173）：原任务+上游清理后重放先报 invalid_dependency；信箱/存储满时取不回回执。→ 拆「只读查重」与「新尝试绑定写入」。
3. **[P2] 三参校验不对称**（submit.go:172）：仅 work_ref 非空才校验绑定，漏传 work_ref 只带 step_seq/prime_term 会静默派普通任务（REST/帧入口无 MCP 层防护）。→ core 公共入口统一拒绝不成组参数。
4. **[P2] 绑定事件主体不符六.2**（server.go:283 固定 admin 被用作 principal）：真实 MCP/PAT 提交丢 pat:令牌名。→ 独立传 authPrincipal(r)，保留 SubmittedBy 兼容语义。
5. **[P2] takeover 原请求重放无成功回执幂等**（work_dispatch.go:102）：expected=T1,new=T2 成功后原样重放 409；测试改参数绕过。→ 原请求响应丢失后重放应返回成功回执，且不把其他接管者误认 dup。
6. **[P2] recoverWorks 多尝试恢复覆盖更新事实**（work_dispatch.go:327）：按尝试登记序补写，旧尝试 failed 会把已 done 步骤改回 failed/Work 改 blocked。→ 确定恢复时事实排序与覆盖规则（按终态落盘顺序）。
7. **[P2] 回滚测试没到绑定点**（work_dispatch_test.go:319）：2MiB 输入在事务外预检即失败。→ 构造绑定盖章后失败的边界，核对任务/尝试/步骤/状态/事件/信箱全部不变。
8. **[P2] 全局存储预算按绑定前体计量**（submit.go:111）：绑定字段不计入全局预算（信箱有 Enqueue 兜底、全局无）。→ 绑定后重查全局容量。
9. **建议修**：任期 ABA（T1→T2→T1 重新放行旧 T1）→ 限制任期复用或服务端单调代次。
10. **建议修**：活任务 dup 响应缺 Work 字段（与首次提交/task_gone 不一致；测试注释声称检查实际未查）。

已确认通过：接管 CAS 同事务/绑定挂点后现有错误分支均 mutate/三终态路径全覆盖/needs_review 语义/六.六交付。

# W2 审查修复闭合记录（zcode，2026-10-25，8 必须修 + 2 建议修全数闭合）

1. **#1（P1）去重证据指纹+身份边界+跨 Work 键**：尝试登记新增 `ContentHash`/`BaseContentHash` 双指纹（store.WorkAttempt，绑定事务内按 `mailbox.ContentHash/BaseContentHash` 同口径计算落盘，含提交主体=身份边界）；查重拆出只读函数 `lookupAttemptEvidence`（work_dispatch.go）——全局扫描同键尝试，全量指纹合=回执命中（TaskGone 按任务现存判定），仅群引用不同=按新提交（同任务级换群口径），其余（换内容/换 work/换步骤/换提交主体）=409 conflict；跨 Work 键复用在任务清理后由证据指纹兜底（与任务级 IdemCheck 同构，清理边界两侧语义一致）。TESTPLAN 边界声明「跨 Work 键复用由任务级指纹冲突兜底」修订为「任务清理后由证据指纹兜底」。
2. **#2 只读查重与绑定写入拆分**：证据查重为独立读操作，两处接入提交链——预检 4b（idem 查重之后、依赖准入与容量预检之前）+ 事务内重检（idem 重检之后、体积/容量/依赖重检之前）；bindWorkSubmit 只做准入+原子写入（不再内嵌查重）。依赖已清理的重放不报 invalid_dependency，信箱/全局预算满仍取回回执（`TestWorkReceiptReplayBeatsAdmission` 三腿：deps 清理/信箱满注入/全局预算满注入）。
3. **#3 三参成组校验**：submit 链 2b 步——`work_ref` 缺失而携带 `step_seq`/`prime_term` 任一 → ErrInvalid（400 invalid_request），REST/帧入口统一把关不静默落普通任务；node 帧路径同规（`TestWorkBindingValidationMatrix`）。
4. **#4 绑定事件主体**：submit 链贯通 actor 参数——`SubmitWithID` 新增 actor（REST=`authPrincipal(r)`，PAT 提交不再丢令牌名）、`SubmitFrame`=会话 node_id、`Submit`=空（仅限内部/测试）；attempt_recorded principal 取 actor，不再取 `task.SubmittedBy`（ SubmittedBy 兼容语义不变，仅显示/审计标签）。api `TestWorkBoundSubmitREST` 补 PAT 入口落账断言。
5. **#5 接管原请求重放回执**：CAS 不符分支先判回执——任期已是 new_term 且账本最近一条 takeover 事件 by=new_term 且主体同源（actor 非空须相等）→ dup=true 返现状零写入；他方同参写入按 conflict 不误认 dup（主体校验）；任期推进后旧回执失效；账本裁剪找不到接管事件按 conflict（安全侧）。core+REST 双层断言。
6. **#6 恢复按终态落盘顺序**：recoverWorks 先收集待回填 (尝试，任务) 对、按任务 `UpdatedAt`（终态落盘时刻）升序排序（同刻按尝试序）后逐条补写——与活路径单写事务串行化 last-writer-wins 同序；`TestWorkRecoverOrderingAfterWindow` 构造「后注册尝试先终态 completed、先注册尝试后终态 failed」窗口，断言步骤终局 failed+恢复事件时序与终态时序一致（登记序补写会错成 done，变异实测可拦）。
7. **#7 回滚测试到绑定点之后**：新增 `bindEdgeReq` 构造器——以盖章前后 offer 信封字节差为余量取 stdin 填充，未盖章预检恰好放行、盖章后复查越限（自检两条边界），替换原 2MiB（事务外预检即拒，未到绑定点）构造；断言任务/尝试/步骤/状态/事件/信箱全不变。
8. **#8 全局预算按盖章体复核**：绑定后事务内 `checkCapacity(st, node, mailbox.TaskBytes(&task))`（盖章体）以 mutate 错误整体拒绝；`TestWorkBindStorageBudgetRecheck` 把字节缓存抬到「预算−未盖章体−盖章增量/2」，预检放行、盖章后越限（变异 F 实测：改回绑定前体计量即 FAIL）。
9. **#9（建议修）任期复用拒绝**：`Work.TermsUsed` 历史使用集（建单播种、接管成功追加、Clone 同步拷贝）；接管 new_term 命中历史集 → 409 work_term_conflict（重放 new==current 不受限）；core+REST 双层断言。
10. **#10（建议修）活任务 dup 带 Work 字段**：`workDupInfo` 补全——idem dup 命中（预检+事务两路）响应附原尝试定位与现终局（TaskGone=false），与首次提交/task_gone 同形；core+REST 断言补齐（原测试注释声称检查实际未查，已实查）。

## 修复变异验证（断言非摆设，逐项还原后复跑全绿）

- 变异 A：证据命中不比指纹（同键即 dup）→ `TestWorkAttemptEvidenceBoundary` FAIL（同键换内容应 conflict 得 nil）✓
- 变异 B：删除接管回执分支 → `TestWorkTakeoverAtomicSingleWinner` FAIL ✓
- 变异 C：删除任期复用拒绝 → 同上 FAIL（复用历史任期标签应拒）✓
- 变异 D：recoverWorks 去掉排序（登记序补写）→ `TestWorkRecoverOrderingAfterWindow` FAIL（步骤错成 done）✓
- 变异 E：删除三参成组校验 → `TestWorkBindingValidationMatrix` FAIL（半参得 nil）✓
- 变异 F：盖章后容量重检改用绑定前体 → `TestWorkBindStorageBudgetRecheck` FAIL ✓
- 变异 G：删除 4b 证据预检（仅留事务内重检）→ `TestWorkReceiptReplayBeatsAdmission` FAIL ✓

## 验证结果

`go test ./... -race -count=1` 全绿 ✅；`GOOS=windows go build ./...` + `GOOS=linux go build ./...` + 本机 build ✅；`go vet ./...` ✅。未 commit（提交在伦哥）。

改动文件清单（本批修复）：internal/hub/store/work.go、internal/hub/core/{work_dispatch,submit,work}.go、internal/hub/api/server.go、internal/hub/core/work_dispatch_test.go、internal/hub/api/works_test.go、SPEC.md、docs/TESTPLAN-W2-work.md。

（遗留声明：事件滚动窗口裁剪后接管回执判定找不到 takeover 事件按 conflict 处理（安全侧）；TermsUsed 无上限——任期接管频度即其长度上界，V0 不裁剪。）
