# 部署与常见问题

按角色分端的部署步骤与使用说明见[部署指南](DEPLOYMENT.md)。先完成 [README 最小闭环](../README.md)，再接真实机器和真实 agent。原始配置参考 [hub](../configs/hub.example.yaml) / [node](../configs/node.example.yaml)。

## 端口与远程访问

| 入口 | 默认值 | 访问者 / 部署要求 |
|---|---|---|
| hub 节点接入 | `:8443`，路径 `/ws/node` | node 主动连接；跨机时放行，使用 WSS |
| hub 管理面 | `127.0.0.1:8444` | Web、CLI、REST、hub 模式 MCP；远程访问需要转发或反代 |
| node 本机 API | `127.0.0.1:8445` | 本机控制台与 node 模式 MCP，回环监听 |

端口都可配置；仓库 hub 示例把节点入口也绑回环，且启用明文开发模式，只适用于本机演练。

远端 hub 保持管理面回环监听时，在运行浏览器 / MCP 的机器执行：

```bash
ssh -N -L 18444:127.0.0.1:8444 '<SSH用户>@<hub主机>'
```

随后用 `http://127.0.0.1:18444` 访问管理台，或将其设为 `SWARM_HUB_URL`。SSH 会话需要持续运行，本地端口须空闲。该转发只覆盖管理面；node 仍连 hub 的 WSS 节点入口。

也可为管理面配置 HTTPS 反代；将 `api_listen` 绑定非回环地址时，必须预先配置非空 `admin_token`。节点 WSS 自签证书不自动为管理面提供 HTTPS。

## 凭据类型与用途

| 凭据 | 来源 | 用途 |
|---|---|---|
| `admin_token` | hub 配置；留空时生成到 hub `data_dir/admin_token` | 浏览器令牌登录、CLI `--token`，换会话 cookie |
| PAT：`smsw_…` | hub 管理台「设置」创建 | REST Bearer、`SWARM_HUB_TOKEN` |
| `register_token` | hub `nodes` 映射中每节点一份 | node 注册到 hub，必须匹配 node_id |
| `local_token` | node 配置或 node `data_dir/local_token` | 本机控制台与主人管理 API |
| `prime_token` | node `data_dir/prime_token` | node 模式 MCP 编排；还须配置 `prime_agent` |

CLI 的环境变量是 `SWARMMIND_HUB_TOKEN`，MCP 的环境变量是 `SWARM_HUB_TOKEN`，拼写不同，且使用的凭据类型不同。不要互换。

REST 提交示例（hub 管理台先创建 PAT）：

```bash
curl --fail-with-body http://127.0.0.1:8444/api/v1/tasks \
  -H 'Authorization: Bearer smsw_替换为实际PAT' \
  -H 'Content-Type: application/json' \
  -d '{"target_node":"mac-mini","target_agent":"echo","input":{"args":["hello"]},"submitted_by":"script"}'
```

`submitted_by` 是请求标签；服务端独立记录经过鉴权的身份。管理令牌直接作为 Bearer 会被拒绝。

## TLS 与首次连接

生产部署设置 `tls.insecure_loopback: false`，node 使用 `wss://<hub地址>:8443/ws/node`。hub 首次生成 `hub_cert.pem` 和 `hub_key.pem`。`hub_pubkey_fp` 是证书 SPKI 的 SHA-256 base64 指纹，不是证书文件本身的 SHA-256。

已取得可信的 hub 证书后，可在装有 OpenSSL 的机器计算：

```bash
openssl x509 -in /可信路径/hub_cert.pem -pubkey -noout \
  | openssl pkey -pubin -outform DER \
  | openssl dgst -sha256 -binary \
  | openssl base64 -A
```

将输出填入 node 的 `hub_pubkey_fp`。留空采用 TOFU：首次记入 node `data_dir/trusted_hubs.json`，后续比对。遇到指纹变化先核对证书与主机变更原因，再决定如何更新信任记录。

## 运行限制与恢复材料

以下默认值来自 `internal/protocol/constants.go`，协议语义见 [SPEC 4.6](../SPEC.md)：

| 项目 | 当前默认限制 |
|---|---|
| 每节点离线信箱 | 100 条 submitted 任务 / 8 MiB |
| hub 任务存储预算 | 256 MiB |
| 未完成任务过期 | 创建后 7 天 |
| hub 终态任务保留 | 24 小时后清理任务记录 |
| node 执行账本 | 提供 24 小时清理函数，但当前运行循环未接入自动清理；审计保留策略仍待确定 |
| Work 保留 | 未完成单不自动清理；关闭后保留 30 天，不延长 Task 或产物可取时间 |
| node 并发 | 2，可配置；同工作目录还受互斥调度约束 |
| 单消息 | 完整编码后 1 MiB |
| 单产物 / 每任务数量 | 640 KiB / 最多 8 个，还需满足完整帧限制 |
| stdout / stderr 尾部 | 各 64 KiB |

任务元数据保留和磁盘产物文件生命周期不是同一件事：不能依赖 hub 做永久归档。重要结果应及时导出，确保源节点和原文件可用。

保留 hub 与 node 各自的 `data_dir`、配置文件、节点身份、TLS/信任材料和 agent 所需凭据。停机备份时同时保留 agent 工作目录中的所需产物。不要删除 node 账本或复制同一节点身份并发运行来「修复」任务；去重范围依赖原账本，且同一数据目录有实例锁。

## 常见问题

| 现象 | 检查与下一步 |
|---|---|
| node 无法上线 | node_id / register_token 是否与 hub 一致；hub `nodes` 修改后是否重启；目标 `/ws/node`、端口、ws/wss 与 TLS 配置是否匹配 |
| MCP / REST 返回 401 | 使用 PAT，确认未撤销；管理令牌只用于登录换 cookie；核对 MCP 的 URL 是否指向实际可达管理面 |
| node 模式 submit 返回 `prime_required` | node.yaml 声明 `prime_agent`，MCP 使用 `prime_token` 而非 `local_token` |
| 目录中找不到 agent | 本机已配置并可执行、节点在线、能力已开放；控制台开放开关持久化在 agents-state.json，会覆盖 YAML 基线 |
| 工具启动后找不到配置或命令 | 核对 command/workdir 绝对路径、config_home、agent_args、base_env；任务不继承 node 全部环境 |
| `policy_denied` | 核对 allow_args / allow_env，PATH 覆盖同样要显式授权；按任务需要授权 |
| `needs_review` | 先核对实际执行副作用、日志和输出，再到 hub 记录人工核对；不要立即重复提交 |
| 产物取回失败 | 核对源节点在线、登记文件存在且未改变、大小/数量限制与 SHA-256；MCP 文本工具不替代二进制下载 |
| 信箱或存储满 | 检查任务积压、目标节点状态与预算；503 `mailbox_full` / `storage_full` 不表示任务已被接受 |

## 系统服务与验证

Linux 使用 [hub](../deploy/swarmmind-hub.service) / [node](../deploy/swarmmind-node.service) systemd 单元，先核对其中二进制、配置与 WorkingDirectory，再安装。Windows node 服务命令和管理员权限要求见[双机部署指南](DEPLOY-P2.md)。原生 Windows 配置应使用 Windows 工具绝对路径与目录，不能直接使用 README 的 `/bin/echo` 示例。

构建与本机回环不能替代 Windows 服务真机测试，也不能替代 [SPEC DoD 11](../SPEC.md) 的真实异构双机闭环。专项报告见[文档导航](README.md)。
