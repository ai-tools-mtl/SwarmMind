# W01-01 基线盘点锁定（2026-10-30）

产出：T1–T3 现状证据、基线测试清单、状态映射表、共享操作服务命名、保留与迁移差异。
效力：本文件是 W01-02/03/04 与 W02-01/W06-01 的实施契约输入；与任务书冲突时以任务书为准并回改本文件，不得静默绕行。

## 1. 现状证据（file:line）

| 面 | 事实 |
|---|---|
| 内核 API | `internal/node/sessionx/sessionx.go`：Create/CreateWithID/Send/Status/List/Close/BindingOf/HasAgent；同步阻塞式，一轮一 CLI 进程；写者门 FIFO（`gate.go`，等待可 ctx 取消、弃队不 spawn 不耗号）；门终身保留上限 4096（`ErrGateTableFull`）；关闭墓碑防复活（`closing` 表 + `persistDirty` 落盘收敛纪律） |
| 内核拒绝面 | `ErrUnknownAgent/ErrSessionNotFound/ErrSessionClosed/ErrSessionExists/ErrAgentMissing/ErrBindingChanged/ErrGateTableFull`（sessionx.go:115-139）；绑定漂移（适配器/权限任一变更）拒绝静默迁移 |
| Hub 注册表 | `internal/hub/core/session.go`：CreateSession（无幂等键，重复调用即新会话）；SubmitTurn（轮任务+轮行同事务；幂等键 `sturn:<sid>:<seq>`；显式 turn_seq 重放按 PromptHash 同源判定；首轮独占；绑定守卫事务内重检）；CloseSession（注册表即闭+close 任务补投；幂等 dup）；投影六态 `projectTurn` |
| Hub REST | `internal/hub/api/sessions.go`：POST /api/v1/sessions(201)、GET 列表/详情、POST /{id}/turns(202/200dup)、POST /{id}/close(200)。鉴权 = s.auth（cookie/PAT） |
| Node 本地主面 | `internal/node/localapi/sessions.go` 五端点（`/api/v1/local/sessions*`，authLocal=local_token）；创建+首轮 202 一体（`SubmitSessionTurnLocal`）；`server.go:263-267` 路由 |
| Node 执行面 | `internal/node/service/sessionturn.go`：hub 派会话轮与本地轮同走 `runSessionTurn`→sessionx；workdir 互斥/信号量/落盘与普通任务共用；轮终 TurnOutcome{TurnSeq,NativeIDOk,WorkResult=unverified}；错误码表 sessionErr*；本地序号双源高水位 `localNextTurnSeq` |
| Node 鉴权面 | `server.go`：authRead（local|prime）、authLocal（仅 local）、authOrchestrate（local|prime 但**要求 prime_agent 非空**否则 403 prime_required，server.go:306-326）；`POST /local/prime/rotate|declare`（authLocal）；prime_token 恒生成存 data_dir（cmd/node/main.go:386-392） |
| MCP | `cmd/swarmmind-mcp/`：hubBackend 含 session 四工具（sessions.go）；nodeBackend 六工具无会话组（main.go:213-229）；node 会话工具缺失=如实（不静默降级） |
| Web | hub 会话导航页+700px 抽屉、node 任务页会话区块+同构抽屉（T3，SESS_UNVERIFIED_NOTE shared block 双端同文） |

## 2. 基线测试清单（AC20 不得删除）

- sessionx：`sessionx_test.go`、`createwithid_test.go`、`adapter_{codex,zcode}_test.go`、`store_test.go`、`lifecycle_t2r{1,2,3}_test.go`（生命周期门/墓碑/门表上限/持久化收敛）
- hub/core：`session_test.go`、`session_t2r1_test.go`（幂等重放/首轮独占/绑定守卫/回写）
- node/service：`sessionturn_test.go`、`sessionturn_t2r1_test.go`
- localapi：`sessions_local_test.go`；MCP：`cmd/swarmmind-mcp/sessions_test.go`、`mcp_integration_test.go`
- E2E：`tests/e2e-console.mjs` sessions 套件（S01-S10/SN01-SN07，本地安全门已有）；**full 套件默认外部 BASE 未设门（:24）→批次1必修**

## 3. 状态与投影映射（新增值全部加法，旧六态不改名不删）

轮次注册表留痕值（store.SessionTurn 终态判定 NativeIDOk+Error）不变；**新增轮次投影值**：

| v3 新投影值 | 语义 | 产生路径 |
|---|---|---|
| `cancelled` | 本轮已取消（终态） | 排队取消（未 spawn，Error=`turn_cancelled_queued`）；执行取消已证停止（Error=`turn_cancelled_executed`）|
| 复用 `failed` | 取消结果未知=待核对 | 执行取消但停止事实未证实（Error=`turn_cancel_outcome_unknown`），**绝不记成功**；hub 侧沿用 failed 留痕，不新增审批状态机 |

- 会话注册表状态仍两值 `active/closed`；UI 投影（创建中/执行中/排队/待核对/已关闭）由轮次+任务现态派生，W01-05 前端实现，注册表不加第三态。
- 旧客户端（不带新字段的调用）看到的行为与今天逐字节一致；unverified 语义不变（禁止改名 done）。
- 排队取消的轮**消耗序号**（高水位不回退，T2-R2 N4 纪律延续）；`turn_seq` 重放对 cancelled 行返回原回执。

## 4. 共享操作服务命名（W01-02 实施基准）

- 新包 **`internal/node/sessionop`**，导出类型 `sessionop.Service`。职责：创建请求 ID 登记/解析（持久）、逐会话有界串行队列与取消、关闭在途防护、工作区授权（cwd ∈ 允许根，默认未配置=放行保持现状）与真实路径互斥（EvalSymlinks 归一后跨会话/任务同键互斥）、重启不确定收敛（非终态会话轮启动期落账为待核对，绝不重跑）。
- sessionx 内核**不改四原语语义**（门/墓碑/绑定守卫保持）；sessionop 在其上编排，经 ledger 持久登记。禁止在内核里加 HTTP 概念。
- 新错误哨兵：`ErrQueueFull`（409 `turn_queue_full`）、`ErrCloseInFlight`（409 `close_in_flight`）、`ErrRequestConflict`（409 `request_conflict`）、`ErrWorkspaceDenied`（403 `workspace_denied`）。
- 队列上限默认 **16**/会话（非终态已受理轮含在途；第 17 条拒绝；AC01「17 条或上限+1」即按此验证）。

## 5. 保留与迁移差异（拟定契约，实施同批写 SPEC）

| 项 | 保留 | v3 变更（批次归属） |
|---|---|---|
| Hub POST /sessions | 无 request_id 时行为不变（每次新会话） | 可选 `request_id`：同键同文返回原会话、异文 409（W01-03） |
| Hub POST turns | turn_seq 全语义不变 | 可选 `request_id` 同上；hub 侧队列上限按 §4=16 拒 409（W01-03） |
| Hub close | 幂等 dup | 有非终态轮 → 409 `close_in_flight` 指向取消/等待（W01-03）；Node 主人 close 同防护（W01-03 本地段） |
| Node 主人五端点 | 语义/状态码零变义 | 仅新增 cancel 端点（authLocal，人工豁免登记）（W01-03） |
| Node agent 面 | 无 | 新增 `/api/v1/sessions*`（create 201 不执行模型 + list/get/send 202），编排凭据放行（W02-01 后生效），cwd 工作区授权强制，cancel/close 不在 agent 面（W01-03） |
| Node cancel | 无 | `POST /api/v1/sessions/{id}/turns/{seq}/cancel`：排队取消=确定性；执行取消=进程治理后按 §3 记账；主人面 + Hub 管理面各一（W01-02 交付原语，W01-03 接线） |
| authOrchestrate | local_token 放行不变 | prime_token 放行改为**显式 mcpaccess enabled** 状态判定；prime_agent 声明与调用权解耦（W02-01） |
| prime rotate/declare | 端点保留兼容 | declare 不再换发共享凭据；rotate 走 mcpaccess 实现，旧别名同实现（W02-01） |
| MCP nodeBackend | 六工具不变 | 增四会话工具投影到本机 agent 面，双 backend 输出带未证实提醒（W01-04） |
| E2E | sessions 套件门 | full/node 全套件强制显式本地 BASE，缺失零网络退出（批次1） |

## 6. 批次1文件禁区（防并行冲突）

- W01-02：`internal/node/sessionop/**`（新）、`internal/node/service/{sessionturn.go,service.go}`、`internal/protocol/**`（加法）、对应测试。**禁** localapi/hub/cmd/web。
- W02-01：`internal/node/localapi/{server.go,prime.go,primewire.go,mcpaccess.go(新)}`、`cmd/node/main.go`、对应测试。**禁** sessionx/service/hub。
- W06-01：`internal/node/agentdetect/**`（新）+ 测试。**禁**一切既有包改动。
- E2E 门：`tests/e2e-console.mjs` 单文件。
