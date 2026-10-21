# SwarmMind

> 让每台电脑都成为 Agent 的节点，让每个节点都协同思考。中心端指挥，客户端执行，分布式多智能体协作框架。

SwarmMind 是一个跨机器的 agent 协作层：一台中心端（hub）登记全网节点与能力，任何一台机器上的人或程序都可以把任务派给网内任意节点的任意 agent 执行——跨公网、断线可补推、结果可核对、产物可取回。

单二进制、无外部依赖（Go 实现），中心端自带 Web 管理台，节点自带本机控制台。

## 架构

```
                ┌─────────────────────────────┐
                │      SwarmMind Hub 中心端    │
                │  WSS :8443   管理台 :8444    │
                │  任务账本 · 信箱 · 批次 · 目录 │
                └──────────┬──────────────────┘
                 TLS 长连接 │  （node 外连，hub 不反向连接）
        ┌──────────────────┼──────────────────┐
        │                  │                  │
   ┌────┴─────┐       ┌────┴─────┐       ┌────┴─────┐
   │  node A  │       │  node B  │       │  node C  │
   │ 跨云 VPS │       │  Mac mini │      │ Windows  │
   │ agent×3  │       │  agent×1 │       │  agent×n │
   └──────────┘       └──────────┘       └──────────┘
```

- **组网**：仿 RustDesk——节点主动外连中心端（TLS WebSocket 长连接），中心端中继，单端口部署，节点无需公网 IP。
- **任务全链**：提交 → 持久化 → 投递（offer/ack 握手）→ 节点本机执行 → 结果回传 → 产物经中心端中继取回（SHA-256 校验）。
- **断线可靠**：节点离线时任务进信箱（100 条 / 8MiB 每节点），重连自动补推；7 天未投递才过期。
- **崩溃语义**：节点执行中重启 → 任务落 `待人工核对`，绝不静默重跑；中心端崩溃 → 两阶段持久化批次自动收尾。

## 快速开始

### 1. 启动中心端

```bash
# 生成管理令牌
openssl rand -hex 32   # → admin_token
```

```yaml
# hub.yaml
listen: :8443                    # node 接入（TLS）
api_listen: 127.0.0.1:8444       # 管理台
admin_token: <上面生成的令牌>
data_dir: data
tls:
  cert: hub.crt
  key: hub.key
nodes:
  my-node: <openssl rand -hex 32 生成>
```

```bash
./swarmmind-hub --config hub.yaml
```

管理台：`http://127.0.0.1:8444`，用 admin_token 登录。

### 2. 接入节点

```yaml
# node.yaml（在目标机器上）
node_id: my-node
hub_url: wss://<hub 地址>/ws/node
register_token: <与 hub.yaml 中该节点一致的令牌>
api_listen: 127.0.0.1:8445       # 本机控制台（仅本机可访问）
data_dir: data
concurrency: 2
agents:
  - name: echo
    kind: interpreter
    command: /bin/sh
    exposed: true                # 开放给全网调用
    allow_args: true
```

```bash
./swarmmind-node --config node.yaml
```

节点上线即出现在中心端「节点接入」页；开放的能力（exposed）进入全网目录。

### 3. 派任务

管理台「运维控制」页选能力、勾目标、写指令，一键全网巡检；
或直接调 API：

```bash
curl -X POST https://<hub>:8444/api/v1/tasks \
  -H "Authorization: Bearer <令牌>" -H "Content-Type: application/json" \
  -d '{"target_node":"my-node","target_agent":"echo","input":{"args":["hello"]},"submitted_by":"me"}'
```

### 4. 用 MCP 让任何 agent 派任务

`swarmmind-mcp` 是标准 MCP stdio 服务，任意支持 MCP 的客户端（Claude Desktop /
HanaAgent / 其它 agent）接入后即可自然语言派单：

```json
{
  "mcpServers": {
    "swarmmind": {
      "command": "/opt/swarmmind/swarmmind-mcp",
      "env": {
        "SWARM_HUB_URL": "http://<hub 地址>:8444",
        "SWARM_HUB_TOKEN": "smsw_…（设置页创建的访问令牌）"
      }
    }
  }
}
```

内置 6 个工具：`swarmmind_nodes`（看节点）、`swarmmind_agents`（看能力）、
`swarmmind_submit`（派任务）、`swarmmind_status` / `swarmmind_wait`（查状态 /
等到终态）、`swarmmind_artifact`（取产物）。

## 核心概念

| 概念 | 说明 |
|---|---|
| 中心端（hub） | 登记与监管：节点接入、任务路由、策略容量、管理台 |
| 节点（node） | 执行者：本机跑 agent，执行权在本机主人手里 |
| agent | 节点上的一个可调用服务（解释器或脚本），`exposed` 后进全网目录 |
| 离线信箱 | 节点不在线时的任务暂存区，重连自动补推 |
| 待人工核对 | 执行中节点重启等「结果不确定」场景的终态，人工登记结论，绝不自动重跑 |
| 访问令牌 | 管理台上创建的子令牌（`smsw_` 前缀），给其他管理者/脚本用，随时可撤销 |

## 生产部署

- **systemd**：见 `deploy/`（hub 与 node 的 unit 模板）
- **Makefile**：`make build` / `make test` / `make release`（四平台交叉构建到 `dist/`）
- **CI**：GitHub Actions（build + test，push 即跑）
- **TLS**：生产建议管理台前置 nginx/Caddy 反代 HTTPS；node 接入口 hub 自带 TLS

## Roadmap

- [x] V0：组网、任务全链、断线可靠、needs_review 语义、Workdir 互斥、双端控制台
- [x] 账户体系：密码登录、账户设置、PAT 令牌（GitHub 式）、恢复通道
- [x] MCP 服务：任意 agent 经 stdio MCP 派单/等结果/取产物
- [x] 跨云实测：腾讯云 ↔ 百度云 双节点、25ms 全链、产物跨机取回
- [ ] P4 群聊：多 agent 会话、@提及派单、上下文包共享
- [ ] P5 Git 集成：worktree 分配、commit 归属、PR 流程
- [ ] V2：E2E 加密（中心端不可见明文）、A2A 官方协议对齐、DID 身份

## License

MIT
