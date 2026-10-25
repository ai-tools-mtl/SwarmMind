# DoD 11 双机实测归档报告（P4 断线恢复 + 权限拒绝）

> 执行：Hana 编排，2026-10-25 11:07-11:11 UTC+8。伦哥 11:02 下令「现在提交加实测」。
> 归档范围：PLAN.md Gate 修订条款限定的「双机断线/权限拒绝两项」（171 行：不因文档豁免）。

## 一、拓扑与环境（真实双机）

| 端 | 机器 | 角色 | 版本 | 守护 |
|---|---|---|---|---|
| hub | 腾讯云轻量 1.13.158.180（ubuntu x86_64，VM-0-12-ubuntu） | hub + 管理 API | main `9c987a6` 交叉编译（linux/amd64） | systemd `swarmmind-hub` |
| node | 伦哥 Mac mini M6（darwin/arm64） | node_id=`mac-mini`，真实 agent `zcode-dev`（GLM CLI 智能体） | main `9c987a6` 本地构建 | launchd `com.swarmmind.node`（KeepAlive） |

连接路径：公网 `wss://1.13.158.180:8443/ws/node`（TLS 自签 + TOFU 指纹）；管理 API `http://1.13.158.180:8444`（admin_token 登录会话）。实测前两端均升级至 `9c987a6`（含跨机取回 + 界面统一），旧版备份 `hub.bak-1025-drawer` / `node.old-1025-drawer`。

## 二、场景与断言结果

### 场景 0：真实链路冒烟（前置）
- 派单 zcode-dev 在 workdir 创建 `dod11-proof/smoke.txt` → **completed**，FinalAnswer 含 `SWARM_DOD11_SMOKE_OK`，本机文件核实内容精确一致。
- 产物通道专项：要求写入 `$SWARM_OUTPUTS_DIR/proof.txt` → completed + 产物登记（24 字节）→ 经 hub 管理面取回 HTTP 200，内容精确匹配，**SHA-256 前缀一致**（`406ce0c5bfe64e6a`）。
- 证据：`01-smoke-*.json`、`02-artifact-*.json/bin`。

### 场景 1：权限拒绝（DoD 11 必归档项）
- 经 hub 管理面向真实远端执行入口派单，任务环境 `PATH=/forbidden`，指令「不要执行任何操作。」。
- 结果：**failed + `result.error == "policy_denied"`**（6 秒内拒绝，白名单环境校验生效于真实入口）。
- 证据：`03-denied-status.json`、`03-denied-assert.txt`。

### 场景 2：断线恢复（DoD 11 必归档项）
1. `launchctl bootout` 停 Mac node（进程退出、无 8443 连接）→ hub 侧 `mac-mini online: False`。
2. 离线派单 → **status=submitted（离线信箱受理）**。
3. `launchctl bootstrap` 拉起 node → 自动重连补送，首次轮询即 running → **completed**。
4. FinalAnswer 含执行事实（mkdir + 写入产物目录），产物登记 23 字节 → 取回 HTTP 200，内容 `SWARM_DOD11_OFFLINE_OK` 精确一致。
- 证据：`04-offline-*.json/txt/bin`（含停止/拉起 UTC 时间戳）。

## 三、与 TC-54 完整版的差异（如实记录）

TESTPLAN TC-54 完整版要求两台云机 + 双向异构 agent 派单。当前基础设施仅一台云服务器（伦哥设备实况：一台 Mac + 一台腾讯云），故：
- 本归档覆盖：真实双机（云 hub + Mac node）、真实异构 agent（GLM CLI）、断线恢复、权限拒绝——即 Gate 条款点名的两项；
- 保持 BLOCKED 不虚报：第二台云机、B→A 反向派单、第二异构 agent 种类（待有第二台云机时补全 TC-54 全量）。

## 四、结论

**DoD 11 断线恢复与权限拒绝两项：正式验收通过并归档。** P4 Gate 三要件状态：工程验收全绿 ✅（main 9c987a6 全量 -race）· 双机两项归档 ✅（本报告）· 14 天观察期：待细则定义后启动。

原始采证 26 件存本目录（`docs/testing/dod11-p4/`），任务 ID：冒烟 `t-239595f2…`、产物 `t-95c084dd…`、拒绝 `t-2b27bbde…`、断线 `t-13d5df64…`。
