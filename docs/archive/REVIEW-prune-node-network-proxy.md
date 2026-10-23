# REVIEW：裁剪 node 全网代理方案审查记录

## 轮次 1（2026-10-22，codex 只读审查）

结论：**需修订，方向可行**。原方案 D1-D7 封不严边界，以下全部采纳：

### 采纳（必须修 → 已并入方案 v2）

1. **新增 D8**：localapi 派单 API 服务端强制 `target_node == 本机 node_id`，外机目标返回 400，不发帧、不留提交日志、不静默改写。（codex Q2 裁决升级）
2. **新增 D9**：hub 核心层对 node 会话的 `task_submit` 强制 `target == env.From`（会话层已强制覆盖身份，可信），双保险，防绕过 localapi 直发帧。
3. **新增 D10**：hub `task_query` 按认证节点限制范围：`Task.TargetNode == env.From`。原 K3「保留任务补查」的前提是查询收敛为本机任务。
4. **D3/D4 扩充**：删完整代理基础设施——`Config.HubAPI*`、`hubProxyTimeout`、`Server.hubMu/hubClient`、`fetchHubAgents`、`hubGetPath`、hub 会话登录函数、`hubMailbox`，清理专属 import。
5. **D6 修正**：结构实为 `localStatus`，删 `Mailbox` 与 `Inflight` 两字段（均来自 hub）；`network` 是动态响应键非结构字段。
6. **新增 D11**：`scripts/demo-loopback.sh`（228、357 行附近）去代理配置与全网断言。
7. **新增 D12**：`docs/TESTPLAN.md`（684、1197、1965、2478 行附近）`.network.agents`/`.mailbox` 用例按新契约改写。
8. **新增 D13**：`SPEC.md`（352、354、378、440、452 行附近）与 `docs/INTERACTION.md`（13、22、23、95 行附近）契约同步：node 能力表述收敛为本机；「互派」语义改为经 hub（MCP/A2A）发起的双机闭环；DoD 第 11 项同步。
9. **验证矩阵加边界反例**：跨节点派单 localapi 400、WSS 跨节点提交被 hub 拒、查询他节点任务被拒、本机任务补查成功、`/api/v1/agents` 与 `/api/v1/local/status` 响应不含 `network`/`mailbox`/`inflight`。
10. **验收流程**：按测试循环制度，新开独立会话逐条执行验证矩阵出 PASS/FAIL 报告，FAIL 回流 zcode，循环至全绿。

### 裁决记录

- **A2A 定位**（codex 指出「MCP 唯一出口」与 SPEC 336/388 行 A2A 同位条款冲突）：裁决定为——hub 是全网能力对外的唯一汇聚面，其上 **MCP 为主要编程接口、A2A 为同位兼容端点**，均挂 hub；node 侧无任何对外暴露。此为表述精确化，不改变 A2A 现有实现（挂 hub 不挂 node）。待伦哥知悉，如有异议再调。
- **Q1 本机派单经 hub 中转**：保留。中转不赋予全网管理权（前提是 D8/D9 目标收敛落地），换账本统一、幂等、断线补推。codex 同意。
- **Q3 老配置兼容**：`yaml.Unmarshal` 非严格模式，删字段后旧 `hub_api_url` 被忽略，无需迁移。启动实测列入验证矩阵第 6 条。
- **K4 表述**：MCP 工具现有 12 个（含群聊五工具），方案表述改为「全部现有工具」。
- **不机械删除**：前端 `e.network`（本机 HTTP 错误语义）与 hub 侧信箱机制（可靠投递）保留，裁的只是 node 视角的代理展示。
- **exposed 准入语义**：hub 要求 `exposed=true` 才准入、前端过滤未暴露能力，现有规则不动。

### 环境备注

- codex 本轮可用（未降级）。消耗 67,350 tokens。
