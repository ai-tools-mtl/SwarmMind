# SwarmMind P2 双机部署指南（DoD 11 产品验收）

> 目标：两台真实机器各跑一个 node，各接至少一种真实 agent（如 Windows 跑 codex、Mac 跑 claude code），完成：发现 → 互派 → 执行 → 产物取回，覆盖断线恢复与权限拒绝。

## 前置

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
    command: C:\Users\<你>\AppData\Roaming\npm\codex.cmd   # where codex 查实际路径
    workdir: C:\swarmmind\work
    timeout_sec: 600
    allow_args: true
    allow_env: ["TMPDIR"]
    description: Windows 侧 codex
    permission_mode: sandbox-read-only
    exposed: true
```

4. 启动（管理员 PowerShell，前台验证）：

```powershell
C:\swarmmind\node.exe --config C:\swarmmind\node.yaml
```

5. 看到「注册成功 node=win-box epoch=1」即入网。
6. 服务化（可选，V1 正式支持）：先用计划任务保活：

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
7. 产物取回：outputs/<task_id>/ 下的文件经 swarmmindctl/hub API 取回且 sha256 一致

## 已知限制（P2 阶段）

- Windows 进程组治理为 Job Object 的完整实现属 V1（V0 用 WaitDelay 兜底，验证直接子进程终止）
- hub 公网部署建议前置反代 + TLS（自签 + 指纹分发方案见 SPEC 二节）
