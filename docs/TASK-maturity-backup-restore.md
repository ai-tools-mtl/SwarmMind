# TASK-maturity-backup-restore：备份与恢复闭环

## 背景（三条实证）

1. 运维文档对备份只有一句「保留 data_dir」（OPERATIONS.md 恢复材料节），无清单、无步骤、无演练。
2. 互换轮运维用户实测热拷陷阱：运行中 `cp hub_state.db` 得到的库 `PRAGMA integrity_check` 返回 ok 但 0 张表（活数据在未拷走的 -wal，实测 78KB）——用户会带着「校验通过的空备份」安心数月。
3. hub 重装证书再生后全网 node TOFU 拒连，恢复方法（删 node `trusted_hubs.json`）全站无文档，拦截日志只无限重试不指路。

## 方案裁决（实施按此，不另起炉灶）

- **不新增 swarmmindctl backup 子命令**：备份本质是文件系统操作，hub 无备份 API，CLI 在远端拿不到文件；加 API 改动面与本条目不成比例。
- **scripts/backup.sh**（新增）：对给定 data_dir 与配置文件执行备份。主推停机备份（直接拷贝全量文件）；检测到目标运行中且 `sqlite3` 可用时，对 db 用 `sqlite3 <db> ".backup '<目标>'"` 取一致快照；sqlite3 缺失且运行中时**明确报错退出，绝不产出静默坏备份**。
- **docs/OPERATIONS.md** 新增「备份与恢复」节（放在「运行限制与恢复材料」之后）：清单表、备份步骤（引用脚本 + 手动等价命令）、恢复步骤、热拷陷阱警示、TOFU 指纹变更恢复小节。

## 文件清单（写进文档，以此为准）

- **hub data_dir**：`hub_state.db`（连同 `-wal`/`-shm`，或经 .backup 合一）、`hub_cert.pem`、`hub_key.pem`、`admin_token`、`notify_channels.json`、`groups.json`。`lock` 为运行时文件不备份。
- **node data_dir**：`ledger.json`、`trusted_hubs.json`、`local_token`、`prime_token`、`agents-state.json`、`notify_settings.json`、`notify_channels.json`、`data_dir/home/`（agent HOME，按需）。`logs/` 可选。
- **配置文件**（data_dir 之外，单独列）：`hub.yaml`、`node.yaml`。

## 验收 DoD（逐条可执行）

1. **在线备份**：临时环境起 hub（写入至少 1 条任务数据）→ `scripts/backup.sh` → 备份中的库可查询且包含该任务（证明非空库快照）；备份目录含清单所列凭据与证书文件。
2. **恢复演练**：延续场景 → 销毁原 data_dir → 按文档步骤还原 → hub 启动成功、旧任务可查 → node 还原后重连成功且**不重复执行**已完成任务（账本去重生效）。
3. **热拷陷阱进文档**：文档以对比形式写明「cp 单 db 文件 = integrity_check ok 的空库」与「.backup / 三件套同拷 = 完整」，附两命令；本条验收 = 文档表述与互换轮实测现象一致。
4. **TOFU 恢复**：实测「删 hub data_dir 证书再生 → node 拒连 → 删 node `trusted_hubs.json` → 重启 node → 重连成功」，文档步骤与实测一致；文档同时保留「先核实指纹变更原因再更新信任」的安全提醒（既有语义不删）。
5. 脚本 `bash -n` 通过；`sqlite3` 缺失 + 运行中场景脚本报错退出并给指引（不允许静默坏备份）；备份产物权限 0600（含凭据，文档写明敏感性）。
6. 零 go 代码改动、零新依赖、`go test ./...` 全绿（确认零回归）；文档本地链接断链检查通过。

## 已知陷阱

- `sqlite3 .backup` 是 WAL 模式下在线一致快照的正解；脚本不得用「先 cp 三件套」当在线方案（时序窗口仍可 torn）。
- 备份产物含 admin_token、TLS 私钥、节点令牌：文档必须显著标注敏感性，脚本对备份目录 chmod 700、文件 0600。
- 恢复时还原文件权限（token 类 0600），否则 hub/node 可能拒启或告警。
- 文档风格与 OPERATIONS 既有节一致（表格 + 命令块 + 说人话）；「先核实变更原因」的安全语义保留，不能为易用性删掉。
- 版本纪律：不 commit；最终输出列改动文件清单与 DoD 逐项结果。
