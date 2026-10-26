# SwarmMind 双机部署与验收指南

> 目标：两台真实机器各跑一个 node，各接至少一种真实 agent（如 Windows 跑 codex、Mac 跑 claude code），完成：发现 → 互派 → 执行 → 产物取回，覆盖断线恢复与权限拒绝。

## 环境要求

- hub 已部署（Linux 云机：`dist/swarmmind-hub-linux-amd64`，配置参考 `configs/hub.example.yaml`；`insecure_loopback: false` + 真实证书或自签+指纹分发）
- 两台机器可出站访问 hub 的 node_listen 端口
- 每台机器在 hub 配置的 `nodes:` 段有独立 token

## Windows 机器部署 node

1. 传文件：`dist/swarmmind-node-windows-amd64.exe` → `C:\swarmmind\node.exe`
2. 建目录：`C:\swarmmind\data`（账本与状态）、`C:\swarmmind\work`（任务工作区）
3. 配置 `C:\swarmmind\node.yaml`：

```yaml
node_id: win-box
hub_url: wss://<hub地址>:8443/ws/node
register_token: <hub 为 win-box 签发的 token>
concurrency: 2
data_dir: C:\swarmmind\data
base_env:
  PATH: C:\Windows\System32;C:\Windows
agents:
  - name: codex
    kind: cli
    command: C:\tools\agent\agent.exe   # 示例占位；替换为已安装且可直接执行的 CLI 入口
    workdir: C:\swarmmind\work
    timeout_sec: 600
    allow_args: true
    allow_env: ["TMPDIR"]
    description: Windows 侧 codex
    permission_mode: 按实际执行配置说明权限
    exposed: true
```

配置中的 agent 名称是能力标识，不保证指定产品已经安装。若宿主仅提供 `.cmd`/`.ps1` 脚本，应显式配置所需解释器与固定参数，并验证启动方式；不要把脚本路径假定为可直接启动的原生可执行文件。`permission_mode` 仅为能力说明，不会自动建立沙箱。前台运行使用的账户与系统服务账户还需分别具备 agent 配置和文件访问权限。

4. 启动（PowerShell，前台验证）：

```powershell
C:\swarmmind\node.exe --config C:\swarmmind\node.yaml
```

5. 确认日志显示注册成功，并在 hub 节点列表看到 win-box 在线；epoch 随重连变化，不要求固定为 1。再提交一个可验证任务确认结果，最后退出前台进程并转为服务运行。

## Windows 服务化（P4② 正式支持，替代 schtasks）

swarmmind-node 自身即服务二进制：`service` 子命令把它注册进 Windows 服务管理器（服务名 `swarmmind-node`，启动类型「自动」，开机自启），SCM 拉起的进程内嵌服务主循环、复用前台运行的全部启动/优雅关闭逻辑。**以下命令均需管理员 PowerShell**（右键「以管理员身份运行」）。

```powershell
C:\swarmmind\node.exe service install --config C:\swarmmind\node.yaml   # 注册（拒绝示例配置；路径落死为绝对路径）
C:\swarmmind\node.exe service start                                     # 启动并确认进入运行态
C:\swarmmind\node.exe service status                                    # 状态/启动类型/PID/启动命令
C:\swarmmind\node.exe service stop                                      # 优雅停止（在途任务整组终止收尾）
C:\swarmmind\node.exe service remove                                    # 卸载（运行中会拒绝，须先 stop）
```

运行行为：

- **无人值守自愈**：进程崩溃、自行退出（setup 向导落盘/「应用并重启」）与**非崩溃失败**（配置错误/运行错误以非零退出码收场）一律触发恢复动作，服务管理器按 5s/30s/60s 退避自动重启（24 小时无失败重置计数，超出动作表沿用末项即最坏 1 次/分钟）；非崩溃失败经 `SetRecoveryActionsOnNonCrashFailures` 显式启用（SCM 默认只认进程崩溃）。退出码统一经服务主循环终态上报：干净停止（`service stop`/系统关机）退出码 0 不重启，异常一律服务专属非零码。
- **Running 即真就绪**：服务管理器等到账本打开、启动恢复与本地 API 监听全部完成后才报运行态，`service start` 返回时本地 API 已可用（无「假运行」窗口）。
- **日志**：服务模式无控制台，标准日志落 `<data_dir>\logs\node.log`（追加写，打开与每次写入都检查大小，超 16MiB 轮转为 `node.log.old` 仅留一代——长期运行不无限增长）。前台运行仍输出 stderr，行为不变。选文件而非 Windows 事件日志：事件源需写注册表，文件与 data_dir 同生命周期、`type` 即看，简单可靠。
- **进程治理（Job Object）**：每个任务子进程**先挂起启动、入 Job 后再恢复执行**（KILL_ON_JOB_CLOSE）——执行任何指令前必已受治理，无「先启动再入组」逃逸窗口；超时/取消整组终止（含 agent 派生的孙进程）；Job 装配/入组失败直接拒绝执行该任务（不静默降级）。node 进程自身也入一个兜底 Job：服务停止或 node 被强杀时操作系统兜底终止全部遗留子进程，用于约束 node 异常退出后的残留子进程；实际行为仍需执行真机验证。
- **单实例锁**：同一 `data_dir` 第二个 node 实例（前台或服务）启动即报「已被其他实例锁定」退出（Windows 用 CreateFile 独占 + LockFileEx，与 Mac/Linux 的 flock 同语义，进程退出自动释放）。
- **配置**：格式与前台完全相同（node.yaml 不变）；install 时 `--config`/`--data-dir` 一律绝对化落死，服务并以 exe 所在目录为工作目录；配置里仍建议写绝对路径。
- **移动/更新 node.exe 后**需先 `service remove` 再重新 `service install`（注册的 BinaryPathName 落死旧路径，不会自动跟随）。

### Windows 真机验证清单

管理员 PowerShell 逐条执行，12 条全部符合预期即验收通过：

| # | 操作 | 预期 |
|---|---|---|
| 1 | `C:\swarmmind\node.exe service install --config C:\swarmmind\node.yaml` | 打印「服务已注册：swarmmind-node（开机自启…，非崩溃失败同样触发）」 |
| 2 | `C:\swarmmind\node.exe service start` | 打印「已启动…（PID …）」；`sc query swarmmind-node` 回 `RUNNING`（Running 即账本/本地 API 已就绪） |
| 3 | `C:\swarmmind\node.exe service status` | 状态=运行中；启动类型=自动（开机自启） |
| 4 | hub 侧 `swarmmindctl nodes` | win-box 行 ONLINE=是 |
| 5 | `type C:\swarmmind\data\logs\node.log` | 有「===== 服务进程启动」横幅与「注册成功」行 |
| 6 | 重启 Windows，登录后 `service status` | 状态=运行中（开机自启成立），hub 侧 win-box 回线 |
| 7 | hub 派一个长任务（约 60s），随后 `service stop` | 「已停止」；任务管理器确认无 node 派生的残留子进程；node 本机 `data\ledger.json` 里该任务以 cancelled/needs_review 收尾（非 running 悬挂） |
| 8 | `service start`，再故意改坏 node.yaml（如改错 register_token）后 `service stop` + `start` | start 报「启动后立即停止」或状态非运行中；`node.log` 有可读错误；恢复 node.yaml 后 start 回正常 |
| 9 | `service remove`（须先 stop） | 打印「服务已卸载」；`sc query swarmmind-node` 报服务不存在 |
| 10 | 服务运行时，另开一个前台实例 `node.exe --config <同 data_dir 配置>` | 立即报「data_dir 已被其他实例锁定」退出；`service status` 不受影响仍运行中 |
| 11 | hub 派长任务，任务运行中 `taskkill /f /pid <node 服务 PID>`（模拟崩溃） | 任务管理器确认 agent 及其子进程一并消失（兜底 Job 生效）；随后 `sc query` 观察到服务被自动拉回 RUNNING（恢复动作生效） |
| 12 | `type C:\swarmmind\data\logs\node.log.old`（若存在）或持续运行观察 | 日志超 16MiB 后出现 `node.log.old` 且 `node.log` 重新变小（单代轮转不无限增长） |

清单包含重启、故障注入和日志轮转，耗时取决于机器与任务，不承诺固定完成时间。测试需使用独立节点身份和测试工作目录。

### 备选（不推荐）：schtasks 计划任务保活

仅当服务化不可用（如权限受限无法注册服务）时的降级手段——无优雅停止、无崩溃退避、日志无人接管：

```powershell
schtasks /create /tn SwarmMindNode /tr "C:\swarmmind\node.exe --config C:\swarmmind\node.yaml" /sc onstart /ru SYSTEM
```

## Mac 机器部署 node（第二台 Mac 或本机复用）

```bash
mkdir -p ~/swarmmind/data && cp dist/swarmmind-node-macos-arm64 ~/swarmmind/node
# node.yaml 参考 configs/node.example.yaml，node_id 用独立名+独立 token
~/swarmmind/node --config ~/swarmmind/node.yaml
```

## 验收清单（DoD 11，逐项记录到 P2 验收报告）

1. hub 总览（swarmmindctl nodes）显示两台 node 在线
2. 机器 A 派任务给机器 B 的 agent → completed，FinalAnswer 正确
3. 机器 B 派任务给机器 A 的 agent → completed（互派）
4. 断 B 的网 → A 派 B 的任务入信箱 → 恢复网络 → 自动补推完成
5. 派一个 allow_env 越界的任务 → failed + policy_denied
6. 任务执行中 kill B 的 node → 重启 → 任务 needs_review（非 failed、未重跑）
7. 产物取回：outputs/<task_id>/ 下的文件经 hub API/控制台取回且 sha256 一致

## 验证边界

- 跨平台编译和单测不代表 Windows 服务、账户权限、重启恢复与进程治理已通过本机实测。真机结论必须记录版本、账户和清单结果，不从历史交叉编译记录推导。
- hub 节点接入与管理面是两个入口：前者使用 WSS 与指纹校验，后者远程访问需 HTTPS 反代或 SSH 转发，见 [运维说明](OPERATIONS.md)。
