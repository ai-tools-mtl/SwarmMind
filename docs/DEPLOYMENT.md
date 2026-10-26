# 部署指南：中心端与节点端

SwarmMind 由两类角色组成：**中心端（Hub）** 与 **节点端（Node）**（日常口语中的「客户端」即节点端）。两类角色部署在不同机器上，功能不同、配置不同、使用方式也不同。本文按角色分章，先判断这台机器扮演什么角色，再读对应章节。

本文与其它文档的分工：[README](../README.md) 提供十分钟最小闭环；本文提供按角色的完整部署与使用说明；[运维与排障](OPERATIONS.md) 覆盖端口、凭据、限制与常见问题；[双机部署与验收](DEPLOY-P2.md) 面向真实双机验收场景。

## 先分清：这台机器装哪个

| | 中心端 Hub | 节点端 Node |
|---|---|---|
| 一句话定位 | 指挥部：接任务、管节点、存账本、中继产物 | 执行者：跑在本机 agent 里完成实际任务 |
| 部署数量 | 整个网络一台 | 每台要执行任务的机器各一个 |
| 适合的机器 | 常开、地址稳定的机器（云服务器、家里的小主机） | 你的工作机：Mac、Linux、Windows 皆可 |
| 对外监听 | `:8443` 节点入口（WSS）+ `:8444` 管理面 | `127.0.0.1:8445` 本机控制台（仅回环） |
| 需要公网入站 | 需要放行 8443（管理面可留在回环走转发） | **完全不需要**，node 主动外连 hub |
| 凭据 | `admin_token`（管理）、每节点一份 `register_token` | `register_token`（接入）、`local_token`（本机控制台） |
| 数据存储 | 内嵌 SQLite 单文件库 | JSON 执行账本 |
| 谁来使用 | 提任务的人：浏览器、CLI、REST、MCP | 机器本身自动执行；主人经本机控制台管理 |

典型组网：一台云服务器装 Hub，你的 Mac、家里的 Windows 各装一个 Node。也可以全部装在同一台机器上体验（见 [README 回环演示](../README.md)）。

---

## 一、中心端（Hub）部署

### 1.1 Hub 是什么：功能清单

| 功能 | 说明 |
|---|---|
| Web 管理台 | 浏览器提交任务、查看节点/任务/产物、审批核对、配置通知（`api_listen`，默认 `127.0.0.1:8444`） |
| REST API | 供脚本与程序调用的管理面接口，Bearer PAT 鉴权 |
| 节点目录与在线状态 | 登记所有节点，实时显示在线/离线与其开放的能力 |
| 任务账本 | 任务的受理、派发、执行、结果全状态持久化（SQLite 单文件库）；重复提交幂等去重 |
| 离线信箱 | 目标节点离线时暂存任务，重连后自动补推（默认 100 条 / 8 MiB，7 天过期） |
| 调度与投递 | 依赖检查、容量预算、会话 epoch 与任务租约，隔离旧连接与过期执行 |
| 产物中继 | 跨机取回产物时经 hub 中转，hub 不做永久存储 |
| Work 工作单 | 多步任务的正式执行记录：创建、任期接管、绑定派发、终态回写、报告收集 |
| 协作群 | 记录 agent 间讨论与结果消息 |
| 终态通知 | 任务结束推送到飞书/邮件/webhook/站内（管理台可视化配置） |
| 派发建议（可选） | 内嵌 LLM 决策服务，派单页给出「建议派给 X」，不启用不阻塞派单 |

注意：hub 是控制面，**不直接执行用户任务进程**。若中心所在机器也要跑任务，请在该机器另行部署一个 node。

### 1.2 部署步骤

前置：Go 1.24+（构建）或直接使用 `dist/` 下的预编译产物；一个常开机器；若公网部署需放行 TCP 8443。

**① 构建或获取二进制**

```bash
make build          # 产出 ./hub ./swarmmind-node ./swarmmindctl ./swarmmind-mcp
# 或使用 dist/swarmmind-hub-linux-amd64 等预编译产物
```

**② 编写配置** `hub.yaml`（完整注释见 [configs/hub.example.yaml](../configs/hub.example.yaml)）：

```yaml
node_listen: 0.0.0.0:8443        # 节点入口；跨机部署放开监听并放行防火墙
api_listen: 127.0.0.1:8444       # 管理面；保持回环，远程用 SSH 转发或 HTTPS 反代
tls:
  insecure_loopback: false       # 生产必须 false：节点入口走 WSS，证书首次启动自动生成
nodes:                           # 每台节点一行：node_id → 接入令牌（管理台「节点接入」向导可自动生成）
  mac-mini: "<为该节点签发的令牌>"
  win-box: "<为该节点签发的令牌>"
admin_token: ""                  # 留空则首次启动自动生成到 data_dir/admin_token
data_dir: ./data/hub
```

要点：

- `insecure_loopback: true` 是本机演练专用（明文 + 强制回环），生产一律 `false`。
- `api_listen` 绑定非回环地址时必须预先配置非空 `admin_token`，否则拒绝启动。
- 新增节点 = 在 `nodes:` 段加一行并重启 hub；令牌与 `node_id` 绑定，不可跨身份使用。

**③ 启动**

```bash
./hub --config hub.yaml
```

Linux 生产环境建议用 systemd 常驻：参考 [deploy/swarmmind-hub.service](../deploy/swarmmind-hub.service)，核对其中二进制路径、配置路径与 WorkingDirectory 后安装启用。

**④ 防火墙**：放行 TCP 8443（节点入口）。管理面 8444 建议不暴露公网，远程访问用 SSH 转发：

```bash
ssh -N -L 18444:127.0.0.1:8444 '<SSH用户>@<hub主机>'
# 之后浏览器开 http://127.0.0.1:18444
```

### 1.3 部署后如何使用

**登录管理台**：浏览器打开管理面地址，用 `admin_token` 登录（首次可从 hub 机器上 `cat data_dir/admin_token` 读取）。

**接入节点**：管理台「节点接入」向导生成 `node_id + register_token` 对，把两行配置交给对应机器的 node.yaml（见第二章）。节点上线后在「机器」页看到在线状态与其开放的能力。

**提交任务**（四种入口任选）：

| 入口 | 适合 | 凭据 |
|---|---|---|
| Web 管理台 | 人工派单、看状态、取产物 | admin_token 或账户密码登录 |
| CLI `swarmmindctl` | 命令行习惯：submit / query / agents / nodes / review | `--token` 或环境变量 `SWARMMIND_HUB_TOKEN`（管理令牌） |
| REST API | 脚本与程序集成 | Bearer PAT（`smsw_` 前缀，管理台「设置」创建） |
| MCP（hub 模式） | agent 直接派单 | 环境变量 `SWARM_HUB_TOKEN`（PAT）；注意与 CLI 环境变量拼写不同 |

**日常管理动作**：

- 任务进入 `needs_review`（执行中断、结果不确定）时，在任务页核对实际副作用与日志后人工裁决，或 `swarmmindctl review`。不要直接重复提交。
- 「设置 → 通知」配置任务终态推送渠道（飞书 / 邮件 / webhook / 站内），运行时可改，凭证存 `data_dir/notify_channels.json`。
- 派发建议：在 `hub.yaml` 的 `decision:` 段配置 `provider: llm-as-jev` 后，派单页出现「建议派给 X」；不配置则完全无感。

---

## 二、节点端（Node）部署

### 2.1 Node 是什么：功能清单

| 功能 | 说明 |
|---|---|
| 主动外连 hub | WSS 长连接，**无需公网入站、无需端口转发**；断线自动重连、补推结果 |
| 能力注册 | 把本机 CLI/agent 声明为命名能力，开放到全网能力目录供他人派单 |
| 任务执行 | 固定命令与工作目录、参数/环境变量逐项授权、超时与进程组整组终止 |
| 执行账本 | 每个任务的接受、开始、结果全部落盘；崩溃重启后恢复，结果不确定的进入 `needs_review` |
| 产物登记 | agent 把输出写进 `$SWARM_OUTPUTS_DIR`，结束后自动登记（路径/大小/SHA-256），可被跨机取回 |
| 本机控制台 | `http://127.0.0.1:8445`：配置本机服务、查看本机任务、管理能力开放开关 |
| MCP 编排入口（可选） | node 模式 MCP + `prime_agent`，让本机 agent 作为 Prime 编排全网任务 |
| 独立模式（可选） | 不连 hub 单机运行，本机提交执行任务 |
| 终态通知 | 本机渠道推送任务终态，「同时通知」开关与 hub 侧联动 |
| Windows 服务化 | `service` 子命令注册系统服务：开机自启、崩溃退避自愈、日志轮转 |

### 2.2 部署步骤

前置：能出站访问 hub 的 8443 端口；在 hub 的 `nodes:` 段已有本机的 `node_id + register_token`。

**macOS / Linux：**

```bash
mkdir -p ~/swarmmind/data
cp dist/swarmmind-node-macos-arm64 ~/swarmmind/node   # Linux 用对应产物
```

编写 `~/swarmmind/node.yaml`（完整注释见 [configs/node.example.yaml](../configs/node.example.yaml)）：

```yaml
node_id: mac-mini                        # 与 hub nodes: 段一致
hub_url: wss://<hub地址>:8443/ws/node    # 生产 wss；本机演练 ws://127.0.0.1:8443/ws/node
register_token: "<hub 为本机签发的令牌>"
hub_pubkey_fp: ""                        # 留空 = TOFU 首连自动记录；防首连冒充可预填指纹
local_listen: 127.0.0.1:8445             # 本机控制台，仅回环
concurrency: 2                           # 同时执行的任务数
data_dir: ~/swarmmind/data
base_env:                                # 任务环境基线：不继承 node 进程自身环境
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents: []                               # 能力配置见 2.3
```

启动：`~/swarmmind/node --config ~/swarmmind/node.yaml`。Linux 常驻参考 [deploy/swarmmind-node.service](../deploy/swarmmind-node.service)。

**Windows：**

1. `dist/swarmmind-node-windows-amd64.exe` → `C:\swarmmind\node.exe`，建 `C:\swarmmind\data` 与 `C:\swarmmind\work`。
2. node.yaml 用 Windows 绝对路径（`PATH` 用 `C:\Windows\System32;C:\Windows`，agent command 指向实际 exe；`.cmd/.ps1` 脚本需显式配置解释器，不要假定可直接启动）。
3. 前台验证：PowerShell 运行 `C:\swarmmind\node.exe --config C:\swarmmind\node.yaml`，确认注册成功、hub 侧显示在线。
4. 服务化（管理员 PowerShell）：`node.exe service install --config ...` 注册开机自启服务，`service start / status / stop / remove` 管理。崩溃退避自愈、单实例锁、日志轮转（`data\logs\node.log`）等行为细节与 12 条真机验收清单见 [双机部署指南](DEPLOY-P2.md)。

### 2.3 配置本机能力（agents 段）

每个 agent 一个条目，这些字段划定了「这台机器允许被派什么活」：

```yaml
agents:
  - name: claude-code              # 能力名：全网派单时的 target_agent
    kind: cli
    command: /opt/homebrew/bin/claude   # 绝对路径；任务不可更改命令与工作目录
    workdir: /Users/me/swarm-work       # 任务工作目录；同目录任务自动互斥排队
    config_home: ""                # agent 的 HOME；空 = data_dir/home，与 workdir 分离
    agent_args: []                 # 固定附加参数（任务不可改），如 [--sandbox, read-only]
    timeout_sec: 600
    allow_args: true               # 任务能否传命令行参数；false 时非空 args 直接拒绝
    allow_env: ["TMPDIR"]          # 允许任务覆盖的环境变量；"PATH" 与 "*" 都需显式授权
    description: 代码分析与生成     # 显示在全网能力目录的一句话
    permission_mode: no-shell      # 对 agent 自带权限机制的说明，仅展示
    exposed: true                  # 是否开放到全网；本机控制台的开关会覆盖此基线
```

三个自动注入的环境变量（任务不可覆盖）：`SWARM_TASK_ID`、`SWARM_TRACE_ID`、`SWARM_OUTPUTS_DIR=<workdir>/outputs/<task_id>`。agent 把产物写进 `$SWARM_OUTPUTS_DIR` 即自动登记、可跨机取回。

注意：`data_dir/agents-state.json` 由本机控制台的开放开关自动维护（覆盖 YAML 的 `exposed` 基线，重启保持），勿手工编辑；要改基线请改本文件并重启 node。

### 2.4 部署后如何使用

**本机控制台**：浏览器开 `http://127.0.0.1:8445`（登录令牌是 `data_dir/local_token`，或 node.yaml 里配置的 `local_token`）。可以：管理本机服务配置、查看本机任务清单与结果、开关能力的全网开放。

**日常状态**：部署完成后 node 是无人值守的——hub 派单、本机执行、结果回传全自动，主人只在两件事时出手：改能力配置（node.yaml + 重启，或控制台开关）、排查异常任务（控制台看日志与账本状态）。

**让本机 agent 当 Prime（可选）**：node.yaml 配置 `prime_agent` 后，用 `prime_token`（`data_dir/prime_token`）启动 node 模式 MCP，本机 agent 即可拆解任务并派发给全网节点执行。

**独立模式（可选）**：不配置 `hub_url` 时 node 单机运行，仅本机提交执行，适合先在单机验证能力配置再入网。

**上线自查**：hub「机器」页本机在线且能力目录里能看到你开放的 agent → 从 hub 派一个 echo 级别的最小任务验证 → 结果与产物正常回传，即接入完成。真实双机互派、断线补推等完整验收清单见 [DEPLOY-P2](DEPLOY-P2.md)。

---

## 三、两端连通验证

1. hub 启动后，管理台能正常登录。
2. node 启动后日志出现「注册成功」，hub「机器」页该节点 ONLINE。
3. 首次连接安全确认：node 留空 `hub_pubkey_fp` 时采用 TOFU（首连记录指纹到 `data_dir/trusted_hubs.json`，之后指纹变化即拒绝）；对中间人风险有要求时，在 hub 机器上用 OpenSSL 计算证书指纹预填（命令见 [运维文档](OPERATIONS.md)）。
4. 从任一入口派一个最小任务到新节点 → `completed` 且 FinalAnswer 正确，全链路即通。

端口、凭据类型、运行限制与常见故障对照表统一见 [运维与排障](OPERATIONS.md)。
