# W09 收尾加固批次任务书（2026-11-03）

> 前置：v3 全案收口（FINAL-REPORT.md，基线后 HEAD 1aec205 起）。本批次处理 FINAL-REPORT §4 登记项中的三个可开发项 + 一项书面评估。全部本地 feat/product-v3，工人不 git commit（提交按钮在集成者手里）。
>
> 流程沿用既定流水线：子 agent 开发 → 集成者门禁（go build/vet/test + race 覆盖 + 对抗读码逐项对照声明）→ 本地提交 → codex 对抗复审（**固定 `codex exec -m gpt-6.1-sol -c model_reasoning_effort=medium`，禁最高配**）→ 必修循环 → 双绿定通过。
>
> 纪律（红线）：
> - 报告必须逐条附 `文件:function行号` + 原文读回摘录——集成者逐项对照磁盘代码核验，声明无摘录 = 视为未实现（W07-02 报告失实事件处置纪律）。
> - 同一条意见修两次不过 → 停止，上报集成者裁决（AGENTS.md 返工上限）。
> - 变异验证：变异前 `cp` 备份原文件，还原只用备份副本，**绝不用 git checkout 还原**（会连带回退未提交改动）。
> - 禁触碰：双端 shared block（`===== … shared block =====` 标记块）逐字节锁；SPEC 第三节依赖白名单；时间序列化一律 UTC。

## 工作块 A：Engine.RestoreLoop 快照持久化接线（FINAL-REPORT §4.3 / 采证 O2）

**背景**：`strategy.Engine` 循环事实（候选/版本/问题/证据/trail）hub/node 两端同为引擎内存态——重启后如实空态、完成判据如实拒绝（宁拒不猜语义保持为兜底）。`Engine.Snapshot`/`Engine.RestoreLoop` API 与往返等价/损坏拒绝/旧格式迁移/序号收紧测试已在 `internal/strategy/restore_test.go` 全绿，**缺的只是装配层持久化 + 启动导入接线**（`cmd/node/main.go` 约 :311、`cmd/hub/main.go` 约 :229 两处装配点注记「接线归后续」）。

**需求**：

1. **引擎变更通知**：`Config` 增加 `OnPersist func()`（nil = 零操作，现行为逐字节不变）。全部会改写 `loops` 的引擎公开方法在成功变更后触发；实现走单一汇聚点（各方法尾部统一通知），禁止散点手滑漏接。**死锁红线**：通知触发点必须在 `e.mu` 写锁释放之后；持久化 worker 组包调 `Snapshot`/枚举时取读锁——写锁内同步回调 = 死锁面，禁止。
2. **枚举面**：引擎提供全部已载入循环的枚举（如 `LoopIDs() []string`，读锁下拷贝）供装配层逐空间取 Snapshot。
3. **装配层持久化（两端同态同形）**：node（`cmd/node/main.go`）与 hub（`cmd/hub/main.go`）装配点各自挂 `OnPersist` → `data_dir/strategy_loops.json` 原子写（0600，temp+rename+fsync；AGENTS.md 禁直接覆盖写）。格式 `{"version":1,"loops":[SpaceLoop...]}`，时间一律 UTC。写入策略两端**一致取其一**：默认推荐「变更即同步写」（循环事实变更频率=轮终/候选级，低频；零丢失窗最诚实）；若做节流合并则必须交付关停 flush（进程退出/引擎弃用时 dirty 即落盘），禁止「靠 defer 不 flush」的半截面。
4. **启动恢复**：装配序 = 引擎创建后、任何空间操作发生前，读 `strategy_loops.json` 逐空间 `RestoreLoop`。文件不存在 = 零操作（老部署/首次启动，如实空态）；**文件损坏或任一条目 RestoreLoop 校验失败 = 拒绝启动**（同 spaces.json 宁拒启口径——部分恢复会让判据半真半假，不带病），错误信息列明空间 ID 与原因。
5. **注记迁移**：接线落地后，`strategy.go`/`discussion.go`/`cmd/node/main.go` 内所有「RestoreLoop 接线前…」「快照接线归 W04-03/后续」类注记迁移为接线后语义（只改注记与指路，不改判据行为）。引擎级「未恢复引擎 = 如实拒绝」的既有断言保留为对照锚（assembly 恢复 ≠ 引擎凭空延续）。

**DoD**：
- 单测：OnPersist 触发计数（变更即触发、读方法零触发）、持久化文件往返（写→新引擎 RestoreLoop→候选/版本/问题/trail 逐字段等价）、损坏 JSON 拒绝、缺 version 拒绝、单条目非法拒绝、OnPersist nil 时全行为不变（既有 28 包绿即锚）；
- race 覆盖 internal/strategy；全仓 build/vet/test 绿；
- 装配锚：cmd 层可构造级测试或等价集成锚（不要求起真进程，但恢复路径的「文件→引擎」链必须有测试覆盖）。

**禁改**：`Snapshot`/`RestoreLoop` 校验语义零变化；完成判据/阶段推进判定行为零变化；collab 存储层零变化。

## 工作块 B：needs_review 释放入口（FINAL-REPORT §4.4 / 采证 O3）

**背景**：意图 accepted 后任务终态 needs_review（轮终不确定）或证据缺失（任务已清理）→ hub 侧「留待核对」不收敛（`classifyTaskFact` outcome 空 = 留待核对），同空间后续 pending 被 `admissionLocked` 的 accepted 在途串行门无限期占住。现状 honest-block 是安全行为（绝不猜测、绝不重跑）；本块交付**驱动者显式裁决入口**——人核验后显式放行，仍然零自动。

**需求**：

1. **端点**：`POST /api/v1/spaces/{id}/intents/{request_id}/resolve`，body `{"verdict":"accept"|"reject","note":"..."}`。
   - `accept`：该轮以「人工核验通过」收束——**复用既有收敛原语**（classifyTaskFact 的 done 收敛路径），禁第二套收敛实现；意图 accepted→终态，注记人工核验（服务端主体）。**不改写 node 侧任务实物**（事实不可篡改，裁决只落 hub 侧意图/留痕）。
   - `reject`：人工否决收束——意图 accepted→cancelled + 注记；不重发不转投（重派由驱动者显式 dispatch）。
   - **宁窄勿宽的准入边界**：仅当该意图处于「留待核对」态（任务实物终态非运行中，或任务证据缺失）才可 resolve；任务仍在运行中的 accepted → 409 指路既有取消路径（防冒认在途）。非 accepted 态/无此意图/已收束 → 409/同形幂等安全拒绝（dup 门同既有五入口口径，错误码同规）。
2. **鉴权与审计**：服务端主体链（adminIdentity 既有模式）；留痕/事件主体禁请求自报（AGENTS.md 审计红线）；空间操作权限语义同 ControlSpace 族。
3. **MCP 同步（AGENTS.md 硬规）**：新 REST 管理面能力 → `cmd/swarmmind-mcp` 增工具（`swarmmind_` 前缀，描述写「能做什么」）+ SPEC 六.六映射表一行 + 真调用实测。
4. **hub 页面**：空间详情留待核对意图行加显式释放操作（accept/reject + note）。双端对照：node 页无空间管理面（hub 管理动作），按 SPEC 六.五白名单核对——白名单未覆盖的差异须登记注记。shared block 零触碰。
5. **串行门解锁锚**：resolve 收敛后 `advanceSpace` 能推进同空间下一棒（集成级机械断言——本块的核心验收，O3 的「无限期滞留」由实测闭环）。

**DoD**：
- 五场景单测：accept 收敛+串行门解锁推进、reject 收束+不重发、重复 resolve 幂等安全、运行中 resolve 409、非在途/未知意图 409；
- 留痕带服务端主体断言；node 侧零 diff 断言（涉 node 包零改动或在报告列明）；
- MCP 工具实测 + SPEC 六.六行；web 行为锚（同既有 route_compat/spaces_poll harness 口径或源码锚，随改动面定）；
- 全仓 build/vet/test 绿；race 覆盖 hub spaces/api。

**禁改**：`classifyTaskFact` 分类语义零变化；恢复对账路径（`reconcileIntents` 族）零变化；node 侧零变化；串行门判定本身零变化（解锁靠意图真实收敛，不靠放行判定）。

## 工作块 C：绑定族细粒度并发加固（FINAL-REPORT §4.2 / SPEC 声明 27）——**B 落地后串行**（同文件 internal/hub/spaces/service.go）

**范围**（SPEC 声明 27 降范围面收窄为三场景）：
1. **解绑×重绑交错**：unbind 与 rebind 并发交错下，旧代次的借用转移/补偿不得触碰新代次绑定（`compensateBindIfOwned` 四门已有——补确定性交错测试矩阵验证）；
2. **自动补偿并发换绑**：`unbindGenOnce` 探针钉代次 + 并发 `ensureBindSession` stale 重同步的窗口；
3. **迟到回写完备隔离**：旧代次会话的迟到回写（结果/取消 writeback）在新代次绑定下零触碰。

**方法（测试先行，洞实测为证）**：先写确定性交错测试矩阵（-race 下跑；交错点用注入/sync 点位控制，不用 sleep 碰运气），实测出洞才修。修复必须落在既有代次协议结构内（bindGenEntry/代次条件解除/墓碑单调），**禁止引入新协议**。若矩阵全绿（W07-02 方案 A 结构已覆盖），如实交付「加固=测试矩阵补齐」结论——不造洞、不推测性改码。

**上限**：同一意见修两次不过 → 停止上报集成者裁决（W07-02 已两度触发该条款，本块预期收敛快）。

**DoD**：交错矩阵测试落盘 + race 绿 + 结论如实（修了什么/没修什么/为什么；全绿则声明矩阵覆盖面）。

## 工作块 D（随批书面交付，不改码）：O1 两端口径评估

node 本地轮「202 受理 + 执行口异步拒」vs hub 中心派发「同步 409」：出书面评估（两端口径差异表、对调用方的影响、是否统一的建议与代价），落 `docs/tasks/v3/W09-O1-assessment.md`。只写文档。

## 验收门（批次级）

1. 集成者：`go build ./... && go vet ./... && go test ./... -count=1` 全绿 + race 覆盖涉改包 + 声明逐项读回核验；
2. codex 对抗复审（gpt-6.1-sol medium 固定档位，先 smoke 再正式）→ 必修循环至双绿；
3. 提交：集成者按工作块分别本地提交，信息如实写验证证据与遗留。
