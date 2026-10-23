# SwarmMind

**SwarmMind 是一个人的跨机器派单工作台：在任何一台机器上说清目标，它告诉你这活在哪台机器上干、干完了没有、结果在哪。机器不在线，任务先暂存，上线自动送达；执行中断拿不准的结果会标出来等你确认，绝不悄悄重跑。**

它不是多组织协作平台，不是 agent 应用市场，也不定义协议标准——就是一个人、自己几台机器，把活派出去、放心干、等结果。

单二进制、无外部依赖（Go 实现）。中心端（hub）自带 Web 管理台，节点（node）自带本机控制台，任何支持 MCP 的 agent 都能直接派单、等结果、取产物。

## 架构

```
                ┌─────────────────────────────┐
                │      SwarmMind Hub 中心端    │
                │  WSS :8443   管理台 :8444    │
                │ 任务账本 · 离线暂存 · 批次 · 目录 │
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

<!-- IMAGE: docs/assets/architecture.png | 系统架构图（Hana 生成） -->

- **组网**：仿 RustDesk——节点主动外连中心端（TLS WebSocket 长连接），中心端中继，单端口部署，节点无需公网 IP。
- **任务全链**：提交 → 持久化 → 投递（offer/ack 握手）→ 节点本机执行 → 结果回传 → 产物经中心端中继取回（SHA-256 校验）。
- **断线可靠**：节点离线时任务进离线暂存（100 条 / 8MiB 每节点），重连自动补推；7 天未投递才过期。
- **崩溃语义**：节点执行中重启 → 任务落 `等你确认`，绝不静默重跑；中心端崩溃 → 两阶段持久化批次自动收尾。

## 60 秒见效：跑一遍自带演示

仓库自带 `scripts/demo-loopback.sh`：在你这台机器上同时拉起 1 个中心端 + 2 个节点（临时端口、临时目录，不动仓库、不留现场），自动走完一遍真实全链——派单收结果、超时杀干净进程、离线暂存补推、断线重连、双端重启恢复、重复派单幂等、越权环境变量拒绝、中断标记「等你确认」，外加一段 wss 自签证书 + 首连指纹记录的演示，逐项打印 PASS/FAIL。

```bash
./scripts/demo-loopback.sh
```

前置：Go、python3、curl、bash（macOS / Linux / WSL）。含构建约一分钟；看到「全部通过」即本机回环演示通过（脚本覆盖协议链路演练，不含构建单测与真实双机验收）。失败时现场保留在临时目录，日志齐全（脚本结尾会打印路径）。

这就是 SwarmMind 的全部日常：说目标 → 派出去 → 收结果。下面把它架到你自己的几台机器上。

## 五分钟上手

角色只有两个：**中心端（hub）**——一台常开的机器（VPS、家里主机都行），记账本、管路由；**节点（node）**——每台干活的机器，活在这上面跑。两端是同一份代码构建出的不同二进制。

### 1. 构建

```bash
make build   # 仓库根产出 4 个二进制：hub、swarmmind-node、swarmmindctl、swarmmind-mcp
```

release 产物里中心端叫 `swarmmind-hub-<平台>`，是同一个东西；下文统一写 `./hub`。

### 2. 启动中心端

```bash
openssl rand -hex 32   # 生成管理令牌（下文叫 admin_token）
```

```yaml
# hub.yaml
node_listen: :8443            # 各节点连入的端口（防火墙记得放行）
api_listen: 127.0.0.1:8444    # 管理台端口；只在本机浏览器用，保持回环即可
admin_token: <上一步生成>
data_dir: data                # 账本、快照、证书都落这里
```

```bash
./hub --config hub.yaml
```

浏览器开 `http://127.0.0.1:8444`，输入 admin_token 登录。

节点接入走 TLS，但你不用碰任何证书：hub 首次启动自动生成自签证书存 `data_dir`（`hub_cert.pem` / `hub_key.pem`）。首次连接的证书需自行可信（TOFU：首连记录指纹，之后指纹变化才会拒绝）；需要防范首连冒充时，预填可信指纹 `hub_pubkey_fp`。完整配置项见 `configs/hub.example.yaml`。

### 3. 添加一台机器

在管理台「**节点接入**」页点「生成接入配置」，四步向导：

1. **起名**：给这台机器起个 ID（如 `mac-mini`、`build-linux`）；
2. **登记**：向导在浏览器本地生成这台机器的接入令牌（只显示一次）——按提示把两行 `nodes:` 配置粘进 hub.yaml、重启 hub；
3. **安装**：三选一——部署包（Linux/macOS，含二进制、node.yaml 和安装脚本）、Docker 构建包、手动复制；目标机器已经装过 node 的话更简单，复制「接入串」，到那台机器的本机控制台（`http://127.0.0.1:8445`）「接入中心端」粘贴、预览、保存即可；
4. **等上线**：节点列表亮「在线」即接入完成。

偏好手写配置的话，node.yaml 长这样（所有字段都过服务端校验，照抄可跑）：

```yaml
# node.yaml —— 在被派活的机器上
node_id: mac-mini
hub_url: wss://<hub 地址>:8443/ws/node
register_token: <hub.yaml 里这一行的接入令牌>
hub_pubkey_fp: ""               # 留空 = 首连自动记录指纹（TOFU：之后指纹变化才拒绝）；预填可防首连冒充
local_listen: 127.0.0.1:8445    # 本机控制台，只监听本机
data_dir: data                  # 本机管理令牌自动生成在 data/local_token
concurrency: 2
base_env:                       # 任务执行环境基线（不继承 node 进程的环境变量）
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:                         # 这台机器开放哪些能力
  - name: echo
    command: /bin/echo          # 绝对路径，任务不可更改
    workdir: /tmp               # 绝对路径，必填
    timeout_sec: 30             # 必填，秒
    allow_args: true            # 允许任务传命令行参数
    description: 回声测试
```

`./swarmmind-node --config node.yaml` 启动。不写 `kind` 即 `cli`；不写 `exposed` 即开放给网内其他机器调用。本机控制台 `http://127.0.0.1:8445` 的登录令牌就是 `data/local_token` 文件内容。

### 4. 派第一个任务，收结果

管理台「**任务派发**」页：选机器、选能力、写指令，提交；「**任务监管**」页看进度、收结果、取产物。命令行同款：

```bash
./swarmmindctl --hub http://127.0.0.1:8444 --token <admin_token> \
  submit --node mac-mini --agent echo --arg hello
./swarmmindctl --hub http://127.0.0.1:8444 --token <admin_token> query --id t-xxxx
```

### 5. 进阶：直接调 API

给脚本和外部程序用的令牌（PAT）在管理台「**设置**」页创建：`smsw_` 前缀、明文只显示一次、随时可撤销。注意 admin_token 是你登录管理台用的，**不能**当 API 令牌——Bearer 方式携带它会 401：

```bash
curl -X POST http://127.0.0.1:8444/api/v1/tasks \
  -H "Authorization: Bearer smsw_xxx" -H "Content-Type: application/json" \
  -d '{"target_node":"mac-mini","target_agent":"echo","input":{"args":["hello"]},"submitted_by":"me"}'
```

（或者先 `POST /api/v1/login` 用 admin_token 换会话 cookie 再携带 cookie，效果相同。）

## 核心概念（就这 8 个）

| 概念 | 人话 |
|---|---|
| 机器 | 你的电脑。一台常开的当中心端（hub）记账管路由，其余当节点（node）干活 |
| 任务（task） | 一条派出去的指令：给哪台机器的哪个能力、输入什么、干到哪一步了 |
| 结果 | 任务的最终答复和退出码；监管页与 `swarmmindctl query` 都能看 |
| 产物（artifact） | 任务落盘的文件：agent 往 `$SWARM_OUTPUTS_DIR` 写，结束自动登记，跨机取回带 SHA-256 校验 |
| 等你确认（needs_review） | 执行中机器重启等「结果拿不准」的情况：标出来等你人工核对，系统绝不自动重跑 |
| 令牌（token） | 每台机器一把接入令牌（register_token）；你本人用管理令牌（admin_token）登录管理台；给脚本发子令牌（PAT，`smsw_` 前缀） |
| 权限 | 能力默认锁死：命令与工作目录写死不可改，能否传参（allow_args）、能覆盖哪些环境变量（allow_env）、是否开放给其他机器（exposed）逐项授权 |
| 在线状态（online） | 节点是否连着中心端。不在线时任务进离线暂存（mailbox），上线自动送达，7 天未送达过期 |

其余内部机制都是为这件事服务的实现细节，诊断与错误报告里可查，日常不用学。

<details>
<summary>技术细节：这些机制分别是什么</summary>

epoch（任务版本号，旧版本的回写自动拒绝）、租约（执行权定时续期，过期即失去回写资格）、幂等键（重复提交只算一次）、依赖门控（声明的前置任务未完成前保持等待）。
</details>

## 智行管家（Prime）

指定一个 agent 当管家：你只说目标，管家自己拆子任务、派给网内合适的 agent、盯完汇总报告。它不是常驻服务，就是你的某个 agent 扮演的角色，随时可换人。

两层用法：

- **全网管家**：管理台一键指定（机器契约：`PUT /api/v1/prime`），复合目标跨机器拆解派发；
- **本机管家**：node 启动时即生成编排凭证 `data/prime_token`（0600，给本机进程和 MCP node 模式用）；node.yaml 声明 `prime_agent` 后，本机编排入口才对管家开放（未声明时编排接口返回 403）。

## 用 MCP 让任何 agent 派单

`swarmmind-mcp` 是标准 MCP stdio 服务，Claude Desktop / zcode / 其它支持 MCP 的客户端接入后即可自然语言派单：

```json
{
  "mcpServers": {
    "swarmmind": {
      "command": "/opt/swarmmind/swarmmind-mcp",
      "env": {
        "SWARM_HUB_URL": "http://<hub 地址>:8444",
        "SWARM_HUB_TOKEN": "smsw_…（管理台「设置」页创建的访问令牌）"
      }
    }
  }
}
```

hub 模式 13 个工具：

| 工具 | 干什么 |
|---|---|
| `swarmmind_nodes` | 看哪些机器在线、各自多少任务在进行 |
| `swarmmind_agents` | 看全网开放了哪些能力（agent 目录） |
| `swarmmind_submit` | 派任务：指定机器和能力，支持幂等键、依赖声明、挂到协作群 |
| `swarmmind_status` | 查某个任务当前进度（不等待） |
| `swarmmind_wait` | 等到任务出结果再返回（轮询至终态） |
| `swarmmind_artifact` | 取回任务的产物文件内容 |
| `swarmmind_tasks` | 列出网内任务：看正在跑的、最近完成的、失败的，支持按状态/节点筛选 |
| `swarmmind_decide` | 拿不准派给谁时问一句：返回建议执行者与理由（云端模型不可用自动退回本地规则） |
| `swarmmind_group_create` | 建协作群：围绕一个目标把几个 agent 拉进同一讨论空间 |
| `swarmmind_group_list` | 看现有协作群与状态 |
| `swarmmind_group_say` | 在群里发一条消息（agent 发言计入预算） |
| `swarmmind_group_messages` | 拉群消息（增量，适合轮询） |
| `swarmmind_group_close` | 结束群（终态，不再收消息） |

**跨机可达**：MCP 连的是管理台（api_listen），默认只绑 127.0.0.1。MCP 跑在另一台机器上时三选一：把 api_listen 改绑内网/公网地址（必须已配置 admin_token，否则拒绝启动；明文 HTTP 会打印警告，建议前置 nginx/Caddy 做 TLS）、或 SSH 端口转发、或反代。

**node 模式**（`SWARM_TARGET=node`）：不连 hub，把本机 node 当单机编排入口。环境变量 `SWARM_NODE_URL`（默认 `http://127.0.0.1:8445`）+ `SWARM_NODE_PRIME_TOKEN`（取 node 的 `data/prime_token`；node.yaml 声明 prime_agent 后编排接口才激活，否则 submit 返回 403 prime_required）。只作用于本机，6 个工具：

| 工具 | 干什么 |
|---|---|
| `swarmmind_nodes` | 看本机节点信息与执行账本计数 |
| `swarmmind_agents` | 看本机有哪些可用能力 |
| `swarmmind_submit` | 向本机 agent 派任务 |
| `swarmmind_status` | 查本机任务进度 |
| `swarmmind_wait` | 等本机任务出结果 |
| `swarmmind_artifact` | 取本机任务产物（本机直取，不经 hub 中继） |

## 生产部署

- **systemd**：`deploy/swarmmind-hub.service`（配置读 `/etc/swarmmind/hub.yaml`，二进制 `/opt/swarmmind/hub`）与 `deploy/swarmmind-node.service`（配置读 `/opt/swarmmind/node.yaml`）
- **Makefile**：`make build` / `make test` / `make release`（四平台交叉构建到 `dist/`）
- **CI**：GitHub Actions（build + test，push 即跑）
- **TLS**：节点接入口 hub 自带自签 TLS（节点 TOFU 记录指纹）；管理台默认明文 HTTP，对外暴露建议前置 nginx/Caddy 反代 HTTPS，api_listen 绑非回环地址时必须已配置 admin_token
- **Windows 节点**：目前用计划任务（schtasks）拉起，服务化排在 P4

## Roadmap

项目按 P0-P5 生命周期推进（详见 `docs/PLAN.md` 与 `docs/PRODUCT-DIRECTION-v2.md`），当前处于 **P4：一个人每天用得顺**。

- [x] P0-P1 V0 核心：组网、任务全链、断线可靠、needs_review 语义、Workdir 互斥、双端控制台
- [x] 账户体系：密码登录、账户设置、PAT 令牌（GitHub 式）、恢复通道
- [x] MCP 服务：任意 agent 经 stdio MCP 派单/等结果/取产物（hub 12 工具 / node 6 工具）
- [x] A2A 兼容端点：外部 A2A 客户端发现并调用网内 agent（对齐 spec v0.2.6 兼容子集，v1.0 完整对齐推迟到实验区）
- [x] 智行管家（原「统筹载体」）：复合目标一键派发，指定智能体自动分解/并行调度/汇总报告（双机实测验收）
- [x] 跨云实测：腾讯云 ↔ 百度云 双节点、25ms 全链、产物跨机取回
- [x] 任务租约：执行权过期后旧执行的回写自动拒绝，不会覆盖新结果（P4）
- [x] 任务依赖门控：任务可声明前置依赖，前置未完成不启动；有更新版本时旧的自动作废（P4）
- [x] 群聊 MVP：建群、@提及发言、发言预算、滚动窗口、控制台群聊页、MCP 五工具（P4）
- [x] README 重写与文档黑话清扫（PRODUCT-DIRECTION-v2 P4 第一项）
- [ ] 界面文案黑话清扫（控制台/管理台残留术语，逐页替换为人话）
- [ ] P4 剩余：Windows 服务化、任务终态 IM 通知、产物挂载与跨机取回、swarmmind_tasks 列表工具
- [ ] P5 复杂工作可恢复：执行事实与接管、多步骤断点查看；Git 集成推迟至真实场景出现
- [ ] 实验区（冻结，有真实需求才晋级）：群聊完整协议、StartLux 决策、A2A 深化、P2P 直连、E2E 加密、IDE adapter

## License

MIT
