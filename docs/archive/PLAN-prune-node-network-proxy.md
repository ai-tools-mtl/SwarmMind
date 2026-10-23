# 方案 v2：裁剪 node 全网代理能力，收敛为「本机管理 + 听命交活」

日期：2026-10-22
发起：伦哥定架构边界；Hana 成文 v1；codex 审查后修订为 v2（审查记录见 docs/REVIEW-prune-node-network-proxy.md）
状态：已定稿，进入 worktree 开发

## 一、架构原则（伦哥定义，本方案的标尺）

三层分工，权责干净切分：

1. **节点客户端（node）**：只管理本机 agent（暴露开关、配置、状态）和派发本机任务、看本机留痕。
   - node **可以配置连接指定的中心端**（`hub_url` + `register_token`，WSS 外连长连接）。
   - 连接的用途仅限：听命（接收 hub 下发的任务）与交活（上报执行结果、心跳注册）。
   - node **不具备任何主动的全网派发能力或全网视图**：不能查全网目录、不能向其他节点派任务、不能查询其他节点的事实、不展示 hub 侧事实。
2. **中心端（hub）**：全网唯一管理面。节点目录、全网派单、批次、策略容量、审计全在这里。hub 是全网能力对外的唯一汇聚面。
3. **对外接口**：hub 之上 **MCP 为主要编程接口，A2A 为同位兼容端点**（均挂 hub）。任何外部 agent 经 hub 接口全网派单，不直接接触任何 node。node 侧无任何对外暴露。

一句话：`hub_url` 是主门（连接、听命、交活），`hub_api_url` 是侧门（node 代理 hub 管理面），本方案**封侧门、留主门，并在 hub 核心层上第二道锁**。

## 二、裁剪清单

### 删除（代理基础设施与全网视图）

| # | 位置 | 动作 |
|---|---|---|
| D1 | `cmd/node/main.go` nodeConfig 的 `HubAPIURL`/`HubAPIToken` 字段及装配（约 74、229 行） | 删除 |
| D2 | `configs/node.example.yaml` 的 `hub_api_url`/`hub_api_token`（第 7-8 行） | 删除 |
| D3 | `internal/node/localapi/server.go` 全网目录代理：`/api/v1/agents` 应答的 `network` 分支（约 195、207 行）及**完整代理基础设施**：`Config.HubAPI*`、`hubProxyTimeout`、`Server.hubMu/hubClient`、`fetchHubAgents`、`hubGetPath`、hub 会话登录函数、专属 import（约 35、54、76、212、240、254 行） | 全部删除；`/api/v1/agents` 只返回本机 `local` 列表 |
| D4 | `internal/node/localapi/local.go` 信箱余量代理 `hubMailbox`（约 131、167 行）及配套 | 删除 |
| D5 | `internal/node/web/app.js`：`network.agents` 消费逻辑（约 93 行）、总览页「信箱余量」卡片、派单页「从能力目录选目标」全网文案（约 197、214、312、318 行） | 删除/改写为本机语境；注意保留 `e.network`（本机 HTTP 错误语义，约 66、285 行）不动 |
| D6 | `internal/node/localapi/local.go` `localStatus` 结构的 `Mailbox` 与 `Inflight` 字段（均来自 hub，约 99、109、111 行） | 两字段一并删除 |
| D7 | `internal/node/localapi/server_test.go`：`TestHubAgentsProxyE2E` 删除（约 144 行起）；`TestLocalAuthGate` 的 network 断言适配（约 103、129、139 行）；`local_test.go` 的 mailbox 断言适配（约 204、215 行） | 删除/适配 |

### 新增边界锁（审查补强，封死绕行路径）

| # | 位置 | 动作 |
|---|---|---|
| D8 | `internal/node/localapi/server.go` `handleSubmit`（约 138 行）：`target_node != 本机 node_id` → 400 拒绝，不发帧、不留提交日志、不静默改写 | 新增校验 |
| D9 | `internal/hub/core/submit.go`（约 38、147 行）：node 会话的 `task_submit` 强制 `target == env.From`（身份由会话层覆盖，可信），违规拒绝 | hub 侧兜底，双保险 |
| D10 | `internal/hub/core/query.go`（约 11、21、27 行）：`task_query` 按 `Task.TargetNode == env.From` 限制范围，他节点任务拒绝 | 查询收敛为本机任务 |
| D11 | `scripts/demo-loopback.sh`（约 228、357 行）：去代理配置生成与全网目录断言 | 适配 |
| D12 | `docs/TESTPLAN.md`（约 684、1197、1965、2478 行）：`.network.agents`/`.mailbox` 用例按新契约改写 | 适配 |
| D13 | `SPEC.md`（约 352、354、378、440、452 行：node 能力表述、「互派」语义、DoD 第 11 项）+ `docs/INTERACTION.md`（约 13、22、23、95 行）：node 能力收敛为本机；跨机协作统一表述为「经 hub（MCP/A2A）发起」 | 契约同步 |

## 三、明确保留（不动）

| # | 内容 | 理由 |
|---|---|---|
| K1 | `hub_url` + `register_token` + WSS 外连、注册、任务下发、结果回传、连接状态/epoch 展示（本机连接事实） | 「连接指定中心端」是架构必需通道 |
| K2 | 本机派发链路：本机控制台 `POST /api/v1/tasks` → `task_submit` 帧 → hub 校验（D9 锁生效后 target 必为本机）→ 投递回本机执行 | 账本、幂等、断线补推、审计统一在 hub 侧；中转不赋予全网管理权 |
| K3 | `GET /api/v1/tasks/{id}` 状态补查 | D10 生效后仅限本机任务 |
| K4 | `swarmmind-mcp` 全部现有工具（12 个，含群聊五工具） | 唯一全网编程出口，挂 hub |
| K5 | hub 管理台、A2A 端点（挂 hub） | 不涉及 node 边界 |
| K6 | `exposed=true` 准入语义、前端未暴露能力过滤 | 现有规则不动 |

## 四、验证矩阵（独立会话逐条执行出 PASS/FAIL 报告）

正向：
1. `go build ./...` 通过
2. `go vet ./...` 干净
3. `go test ./...` 全绿
4. 端到端：node 启动 → 本机派发 echo-demo → completed、账本落盘
5. 前端：三 tab 正常；页面无「信箱余量」「能力目录」「hub_api_url」字样
6. 老配置兼容：带 `hub_api_url` 的 yaml 启动正常（字段被忽略）
7. 回归：从 hub 派任务到 mac-mini 正常执行（下发通道不受影响）

边界反例（审查要求，证明权限收敛）：
8. localapi 派单 `target_node` 为外机 → 400，无帧、无提交日志
9. 直发 WSS `task_submit` 帧外机目标 → hub 拒绝
10. `task_query` 他节点任务 → 拒绝
11. 本机任务补查 → 成功
12. `/api/v1/agents` 与 `/api/v1/local/status` 响应不含 `network`/`mailbox`/`inflight` 键

## 五、开发方式

主分支先落方案与审查记录（本文档），`git worktree add` 建隔离分支 `prune-node-network-proxy`，zcode 在 worktree 内开发，codex 审查代码，FAIL 回流循环（同一条意见修两次不过 → 上报伦哥），独立会话执行验证矩阵出报告，全绿后向伦哥交付摘要。
