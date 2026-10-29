# TASK-agent-session-contract（T2：契约面——hub 路由、REST 与 MCP）

> 分支：feat/agent-session（基于 T1 @ 5d4c8bd）。
> 上游：docs/VISION-persistent-agent-collaboration.md 实施顺序第 2 步剩余部分；T1 已交付 node 侧同步内核（internal/node/sessionx）。
> 开工顺手项（T1 遗留，两个不阻断清理，本批一并做掉）：①testdata/fakecli/main.go 正常路径重复派生写手的旧调用删除；②store.go:104 附近「掉电不回退」旧注释措辞与函数契约对齐。

## 架构方向（编排方已裁决，实施中对齐现有机制）

会话轮的跨机传输**复用现有任务投递链**（conn 层帧、离线暂存、幂等去重、重连补投全部白拿），但身份严格分离：会话轮帧携带平台 session id 与平台 turn id，Task ID 仅作为本次执行的可靠性载体，禁止把 Task ID 当会话身份，也禁止会话身份顶替 Task 身份（VISION 第三节原话的落地）。

- hub 新增会话注册表（SQLite store 扩展，参照 orchestratorDecls 的扩展方式）：平台 session id、目标 node/agent、绑定快照（adapter/权限模式/cwd）、状态（active/closed）、轮次账本引用、时间戳。
- 会话轮在 node 侧：帧处理路径识别会话轮类型后调 sessionx.Create/Send（同步内核），轮终按任务结果链回传 turn 结果（回复文本、原生 ID 校验状态、usage、unverified 标记）。
- 不新增常驻连接、不做流式；离线 node 的会话轮照走既有暂存补投（VISion 场景 A 的连通前提不变）。

## 改动范围

1. `internal/protocol/types.go`：会话轮帧类型与结果类型（含幂等键：session id + turn 序号）。
2. `internal/node/`：帧分派路径接线 sessionx（新帧类型 → sessionx 调用 → 结果回传）；node 账本对会话轮的记录不破坏既有任务语义。
3. `internal/hub/store/`（SQLite）+ `internal/hub/core/`：会话注册表与会话轮次账本（轮序号、状态机：pending/running/awaiting_node/done/failed/unverified、错误留痕）。
4. `internal/hub/api/`：REST 端点组 `POST|GET /api/v1/sessions`、`POST /api/v1/sessions/{id}/turns`、`GET /api/v1/sessions/{id}`、`POST /api/v1/sessions/{id}/close`。鉴权与现有管理 API 同口径（会话 cookie 或 PAT）。Send 语义：返回 turn 引用，轮终状态经详情端点查询（异步，不做长阻塞请求）。
5. `internal/node/localapi/`：本机会话端点——列表/详情/新建/续接/关闭（仅 local_token，同 prime/notify 豁免口径），供 T3 本机控制台使用。
6. `internal/mcp/`（swarmmind-mcp）：新工具组 `swarmmind_session_create / swarmmind_session_send / swarmmind_session_list / swarmmind_session_status`（命名与描述对齐既有工具组风格；send 返回 turn 引用，status 查轮终，与现有 submit/status/wait 的使用心智一致）。
7. `SPEC.md`：六.五新增「会话（Session）与轮次（Turn）」节（端点、鉴权、状态机、幂等键、绑定守卫、unverified 语义）；六.六登记新 REST 组对应的 MCP 工具与本批 localapi 端点的豁免判定（本机主人管理面 vs agent 面合法能力，逐端点写明依据）。
8. 测试：hub api 契约测试（鉴权三态、幂等重放、绑定守卫经 REST 触发）、node 帧分派测试（会话轮→sessionx 调用、离线暂存补投复用现有测试模式）、MCP 工具输出契约测试、store round-trip。

## 边界与纪律

- 普通 Task 路径行为零变化（既有测试全绿为底线）；会话轮是新增帧类型，不改旧帧语义。
- 双端 web 资产（internal/hub/web/、internal/node/web/）本批零改动（T3）。
- MCP 命名固定 swarmmind_ 前缀；描述面向 agent 写能力不写实现。
- 权限模式经 REST/MCP 下发时只允许显式传值，缺省用 node 侧 AgentSpec 配置，绝不注入 yolo。
- 会话轮 Task 的 needs_review 语义：原生 ID 不一致/绑定不兼容/结果 unverified 时的处理与现有核对机制的关系，在 SPEC 节内写明，不引入新的审批状态机。
- go build/vet/test -count=1 全绿（26 包）；node --check 不适用（无前端改动）。

## DoD

1. hub 注册表 + REST 五端点可工作，鉴权与幂等重放有契约测试。
2. 会话轮经现有投递链到 node 并调 sessionx，结果按新状态机落账（离线暂存场景有测试）。
3. MCP 四工具有契约测试，输出与 REST 语义一致。
4. SPEC 六.五/六.六登记完整（含 localapi 豁免逐端点判定）。
5. T1 遗留两个清理项完成。
6. 全仓 build/vet/test -count=1 全绿。
7. 不 commit；报告改动清单与验证证据，commit 由编排方执行。
