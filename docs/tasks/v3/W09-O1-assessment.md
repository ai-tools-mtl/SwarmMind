# W09-O1 评估：绑定会话普通轮两端口径（node 202+异步拒 vs hub 同步 409）

> 来源：STAGE-C-EVIDENCE.md 观察项 O1（采证项 3，ev3/02+04 实证）。本文件为工作块 D 书面交付：差异表、影响面、统一方案与代价、集成者裁决。只评估，评估落定后统一动作按裁决执行。

## 1. 现状口径差异表

同一拒绝语义（绑定协作空间的会话不得经普通轮推进成员线程，错误码 `space_dispatch_required` 两端同规、零执行零触碰）：

| 面 | 入口 | 同步形态 | 拒绝发生点 | 调用方可见序列 |
|---|---|---|---|---|
| hub 中心空间成员线程 | hub 本机 collab 绑定 + hubBind 登记核对 | 同步 409 | 提交侧（派发单入口前置） | 一次请求即 409，无任务实物产生 |
| node 本地（本机 collab 绑定） | localapi 普通轮提交 | **同步 409** | 提交侧守卫（`SubmitSessionTurnLocal`，internal/node/service/sessionturn.go:522-527——`s.collab.SessionOwnerSpace` 命中即拒） | 一次请求即 409（STAGE-B O1 已收口，单测锁定） |
| node 本地（hubBind 登记，本机 collab 无绑定） | localapi 普通轮提交 | **202 受理** | 执行口守卫（`spaceTurnAllowed`，internal/node/service/spacedispatch.go:605-636——hubBind scope 核对） | 202 拿任务号 → 任务落 failed（`space_dispatch_required`；内核零触碰：turn_count 不变、CLI 未 spawn） |

**机制根因**：提交侧同步守卫（sessionturn.go:522-527）只探**本机 collab 绑定**；中心空间成员线程的绑定登记在 hubBind（hub 经 space_bind 帧下发，本机 collab 无绑定），该核对被设计放在收件门/执行口（spacedispatch.go:605-611 注记明示分工）。于是第三行的会话在提交侧探不到绑定 → 走通用受理路径 202 → 执行口拒。

## 2. 影响面

- **语义安全：无差**。两侧最终都零执行（任务落 failed、内核账本 turn_count 不变），错误码与文案同规。
- **调用方体验有差**：202 先行给调用方「已受理」信号，需二跳查任务才发现拒绝；自动化调用方若把 202 当终态即误判。hub 面（驱动者/编排的主调用面）无此问题；受影响的是 node 本地面直接对 hubBind 登记会话投普通轮的调用方（少见但非不可能——本机主人用 localapi 直连成员线程）。
- **账本噪声**：异步拒产生一条 failed 任务实物（随保留期清理）；hub 同步 409 零实物。失败记录本身是诚实事实（拒绝发生过），不算缺陷，但同语义两形态下只有 node 面产生。

## 3. 统一方案与代价

**方案（建议采纳）**：`SubmitSessionTurnLocal` 既有空间绑定守卫处（sessionturn.go:522-527 同一锁域）追加 hubBind 探针：`s.hubBind.Space(sessionID)` 命中（非空）且本请求无相符 Space scope → 同步拒（`spaceDispatchRequiredErr()`，HTTP 投影 409——`writeSpaceDispatchErr` 既有映射，localapi/spaces.go:784-788 已含 `CodeSpaceDispatchRequired`）。

- **代价**：同锁域一次 map 探针，微不足道；改动面 = sessionturn.go 守卫段 + 单测。
- **回归风险：零**。本地普通轮请求（`LocalSessionTurnRequest`）本就无 Space scope 形态，hubBind 命中即拒是纯收紧；hubBind 登记迟到窗（bind 帧未达时提交）探不到 → 仍走既有异步拒路径——**执行口守卫保持权威兜底不变**，同步探针只是快路径。本机 collab 绑定路径（第二行）行为不变。
- **不动**：执行口守卫零变化（它是权威与兜底——同步探针漏过的窗全由它收口）；hub 侧零变化。

## 4. 集成者裁决

**统一，采纳上述方案**：node 本地面对 hubBind 登记会话的普通轮提交改为同步 409（与 hub 面同形同码），执行口守卫原样保留为权威兜底。随本批次波 2 落地（node 侧文件面与工作块 C 的 hub service.go 不相交，无冲突）；DoD：hubBind 登记会话普通轮提交同步 409 单测 + 绑定迟到窗仍被执行口拒收的对照单测 + 全仓绿。

**不统一的备选被否理由**：保持 202+异步拒须在 API 文档为同一错误码写两种 HTTP 形态叙事，调用方适配成本永久存在；收益仅「少一次探针」——不成立。
