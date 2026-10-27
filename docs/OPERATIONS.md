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

保留 hub 与 node 各自的 `data_dir`、配置文件、节点身份、TLS/信任材料和 agent 所需凭据，具体清单与操作见下方[备份与恢复](#备份与恢复)。停机备份时同时保留 agent 工作目录中的所需产物。不要删除 node 账本或复制同一节点身份并发运行来「修复」任务；去重范围依赖原账本，且同一数据目录有实例锁。

## 备份与恢复

备份是纯文件系统操作（hub 不设备份 API），用[备份脚本](../scripts/backup.sh)或下文手动等价命令均可；优先停机备份（最简单、绝对一致），脚本停机备份会全程持有 `data_dir/lock` 直至结束（备份期间实例无法启动），目标运行中时自动改走 `sqlite3 .backup` 一致快照。无论哪种方式，产物都含管理令牌、TLS 私钥与节点凭证，**敏感等同生产凭据**：脚本创建任何产物前先收 umask 077，备份目录从创建起就是 0700、文件 0600，复制期间即受保护，请勿放入公共网盘或代码仓库。

### 备份什么

hub `data_dir`：

| 文件 | 内容 | 备注 |
|---|---|---|
| `hub_state.db`（连同 `-wal`/`-shm`，或经 `.backup` 合一） | 任务、节点目录、事件账本等全部状态 | 运行中备份必须走 `.backup` 快照，见「热拷陷阱」 |
| `hub_cert.pem` / `hub_key.pem` | TLS 证书与私钥 | 私钥敏感；证书丢失后重启会再生新证书，触发 node TOFU 拒连（见下文） |
| `admin_token` | 管理面登录令牌 | 敏感；文件令牌，缺失或不足 32 字节时启动会再生成新令牌并写启动日志，旧令牌全部失效 |
| `api_tokens.json` | PAT 存储（哈希、明文前缀、撤销状态） | 敏感；独立于文件令牌持久化，不受令牌再生成影响，丢失后全部 PAT 失效，需在管理台逐个重新签发 |
| `account.json` | 管理台管理员账户（用户名与口令哈希） | 敏感；丢失后回到未初始化状态，需重新设置管理员账户 |
| `notify_channels.json` | 通知渠道与凭证 | 含 webhook 地址/密钥，敏感 |
| `groups.json` | 协作群定义 | |

`lock` 是运行时单实例锁文件，原子写临时文件（`*.tmp` 与 `*.tmp-*`，含 `local_token.tmp-*` 等形态）是中间态，均不备份。node `data_dir`：

| 文件 | 内容 | 备注 |
|---|---|---|
| `ledger.json`（连同 `cancels.json`） | 执行账本与取消墓碑：终态去重、审计与重启恢复的依据 | 丢失会破坏去重，已完成的任务可能被重复执行 |
| `trusted_hubs.json` | TOFU 已信任的 hub 指纹 | 删除即重置信任，见「TOFU 指纹变更」 |
| `local_token` / `prime_token` | 本机控制台 / 编排凭证 | 敏感；文件令牌，同 hub「缺失或不足 32 字节即再生成并写启动日志」，旧令牌全部失效 |
| `agents-state.json` | 本机控制台能力开放开关 | 可重建，但丢失人工开关状态 |
| `notify_settings.json` / `notify_channels.json` | 本机通知设置与渠道凭证 | 敏感 |
| `home/` | agent HOME（其配置与凭证目录） | 按需；脚本 `--no-home` 跳过 |
| `logs/` | 运行日志 | 可选；脚本 `--no-logs` 跳过 |

配置文件在 `data_dir` 之外，单独备份：`hub.yaml`、`node.yaml`。频率无硬性要求：建议配置变更、升级、增删节点前各备一次，日常定期备份并异地存放。终态任务 24 小时后会被清理（见上节），备份不能替代导出归档。

### 备份步骤

```bash
# hub：停机或运行中均可，脚本自动识别
scripts/backup.sh --data-dir "<hub data_dir>" --config "<hub.yaml 路径>" --out "<备份目录>"

# node：home/、logs/ 通常不必备
scripts/backup.sh --data-dir "<node data_dir>" --config "<node.yaml 路径>" --no-home --no-logs --out "<备份目录>"
```

脚本行为：停机判定 = 对 `data_dir/lock` 非阻塞抢锁并**全程持有至脚本退出**（探测即持锁，备份期间实例无法启动）；锁文件缺失且 db 存在属异常态，按「运行中」保守处理。目标已停止 → 全量拷贝（db 三件套同拷）；目标运行中且装有 `sqlite3` → db 走 `.backup` 一致快照并 `quick_check` 校验，其余文件照拷；目标运行中且没有 `sqlite3` → 报错退出，不产出任何备份。直拷路径绝不触碰运行中的 db：备份期间新出现 `hub_state.db` / `ledger.json`（实例启动窗口竞态）会拒绝交付。备份目录已存在、显式 `--config` 缺失、`--config` 平铺同名冲突均报错退出；中途失败或被 INT/TERM/HUP 中断自动删除半成品目录，不留可误信的残缺产物。

手动等价命令（停机，全量）：

```bash
mkdir -p "<备份目录>" && chmod 700 "<备份目录>"
cp "<data_dir>"/hub_state.db* "<备份目录>"/        # 停止后三件套一起拷
cp "<data_dir>/hub_cert.pem" "<data_dir>/hub_key.pem" "<data_dir>/admin_token" \
   "<data_dir>/api_tokens.json" "<data_dir>/account.json" \
   "<data_dir>/notify_channels.json" "<data_dir>/groups.json" "<备份目录>"/
cp "<hub.yaml 路径>" "<备份目录>"/
chmod 600 "<备份目录>"/*
```

手动等价命令（运行中，仅 db 需要特殊处理）：

```bash
umask 077 && mkdir -p "<备份目录>" && chmod 700 "<备份目录>"   # 产物含凭据：目录 0700、新文件 0600，与脚本行为一致
# 进入备份目录执行，快照文件名不含特殊字符：路径含撇号/空格时直接可用
# （sqlite3 点命令参数内联含撇号路径会解析错乱）
( cd "<备份目录>" && sqlite3 "<data_dir>/hub_state.db" ".backup hub_state.db" )
# 其余文件与配置同上照拷（含收尾 chmod 600 "<备份目录>"/*）；
# .backup 已合并 -wal 数据，不要把运行中的 -wal/-shm 一并拷走
```

### 热拷陷阱：直接 cp 运行中的 hub_state.db

库是 WAL 模式：已提交事务先进 `-wal`，checkpoint 回写主库之前，主库文件里没有这些数据。对运行中的 hub 单拷主库文件：

| 做法 | 结果 |
|---|---|
| `cp "<data_dir>/hub_state.db" backup.db`（运行中） | `PRAGMA integrity_check` 返回 ok，但库可能缺部分乃至全部已提交数据（实测得到过校验通过的空库）——比明显损坏更危险 |
| `( cd "<备份目录>" && sqlite3 "<data_dir>/hub_state.db" ".backup backup.db" )`（运行中） | 一致快照：同一时刻点合并主库与 `-wal`，`quick_check` ok，恢复即用 |
| 停机后 `cp "<data_dir>"/hub_state.db*`（三件套同拷） | 完整：恢复后 sqlite 打开主库自动回放同目录 `-wal` |

运行中「三件套一起 cp」也不是正解：多次拷贝之间存在时间差，可能 torn。在线场景唯一正解是 `.backup`。

### 恢复步骤

前提：目标实例已停止。以 hub 为例（node 同理，文件换成 node 清单）：

```bash
mv "<data_dir>" "<data_dir>.broken-$(date +%Y%m%d)"     # 原目录整体移走留观，不要原地删除
mkdir -p "<data_dir>" && chmod 700 "<data_dir>"
cp -R "<备份目录>"/. "<data_dir>"/
mv "<data_dir>/<hub.yaml 文件名>" "<原配置路径>"         # 脚本产物平铺，配置文件放回原位置
chmod 600 "<data_dir>/admin_token" "<data_dir>/hub_key.pem"   # 按清单中实际存在的凭据/私钥文件执行
```

- 脚本产物本身已是目录 0700、文件 0600；手动备份最容易带出松权限，还原后按上式收紧（令牌、私钥均属凭据）。
- 令牌文件（`admin_token` / `local_token` / `prime_token`）必须完整还原：配置未显式指定该令牌时，缺失或不足 32 字节会被启动流程视为无效并**再生成新令牌**（启动日志提示新令牌位置），旧令牌全部失效。其中 `admin_token` / `local_token` 若已在 hub.yaml / node.yaml 显式指定，则以配置为准、不读令牌文件；`prime_token` 没有对应配置字段，恒由文件提供。该再生成机制只覆盖文件令牌：`api_tokens.json` / `account.json` 漏备则 PAT 需逐个重新签发、管理员账户需重新初始化，没有任何自动恢复路径。
- node 账本（`ledger.json` 与 `cancels.json`）原样还原后，hub 补投或重派的已完成任务会被账本去重跳过，不会重复执行；不要为了「修复」任务而删除账本（见上节警示）。
- 若只是原目录误操作，从 `.broken-` 目录整目录移回即可回滚。

恢复后启动 hub，登录管理台核对：旧任务可查、节点陆续回连、通知渠道仍在。

### TOFU 指纹变更：hub 重装后 node 拒连

hub `data_dir` 的 `hub_cert.pem` / `hub_key.pem` 丢失后重启会再生新证书，SPKI 指纹随之变化；node 按 TOFU 比对 `trusted_hubs.json` 里的旧指纹后拒绝连接，重连循环持续报错，日志形如「TOFU 校验失败：hub=<地址> 证书指纹变更（记录 <旧指纹> 实得 <新指纹>）——可能中间人或 hub 重装，拒绝连接」。恢复步骤：

1. **先核实指纹变更原因**：是本机重装 hub、重新生成证书等自身操作，还是可能存在中间人？无法确认原因前不要更新信任记录。
2. 确认是自身操作后，删除 node 信任记录，重启 node，首连重新 TOFU 记录新指纹：

   ```bash
   rm "<node data_dir>/trusted_hubs.json"
   ```

3. 多个 node 逐台执行；重连成功后指纹恢复稳定。

若手头有 hub 证书备份，把 `hub_cert.pem` / `hub_key.pem` 还原回 hub `data_dir` 即可——指纹与 node 已信任记录一致，node 无需任何操作。这也是把证书列入备份清单的价值。

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
