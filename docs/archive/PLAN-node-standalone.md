# 方案：node standalone 双形态（单独使用 / 组网使用）

日期：2026-10-22
定调：伦哥（产品形态定义）；Hana 成文
状态：待裁剪分支合入后开工

## 一、产品形态（伦哥定义，本方案标尺）

**两端分离，各自独立成产品，非主从关系：**

1. **中心端（hub）**：全网派发中枢。组网形态下的大脑：节点目录、全网派单、批次、审计、管理台、MCP/A2A 出口（已实现，本方案不动）。
2. **客户端（node）**：**本机所有 agent 的统一入口**，两种部署形态：
   - **standalone（单独部署）**：本机 agent 的管理门面与派发入口。网页控制台管理一切；对外提供一个 MCP 连接（stdio，天然仅本机可达——除非本机有公网，否则外部无法触达），外部 agent 经此一个连接即可指挥本机全部 agent。
   - **组网（加入 hub）**：standalone 全部能力 + 跨机协作。node 外连 hub 听命交活；全网派发经 hub 的 MCP/A2A。node 依旧无全网视图、无全网派发（见 docs/PLAN-prune-node-network-proxy.md 边界方案）。
3. **形态切换**：`hub_url` 有无即形态开关。空 = standalone；非空 = 组网。切换不需重装，网页向导即可完成。

## 二、现状缺口

| # | 缺口 | 现状 | 目标 |
|---|---|---|---|
| G1 | 执行链路绑死 hub | 本机派单也经 WSS 绕 hub（task_submit 往返），无 hub 完全瘫痪 | standalone 本地直派发直执行，本地账本照记 |
| G2 | 无 setup 模式 | 无 node.yaml 进程直接退出，鸡生蛋 | 无配置以「未接入」状态启动，只开本机控制台 |
| G3 | 无 node 侧 MCP | swarmmind-mcp 只认 hub 管理面 | 同一二进制加 target 模式，standalone 下外部 agent 有入口 |
| G4 | 配置管理靠命令行 | agents 增删改要手编 yaml + 手动重启 | 网页表格编辑 + 一键应用重启 |
| G5 | 接入动作靠命令行 | 部署包解压后仍需手编/确认配置 | 网页接入向导（粘贴接入串 → 预览 → 落盘连接） |

## 三、设计

### 3.1 形态状态机（v2 修订：三态消除歧义）

- **node.yaml 不存在 → setup 模式**：仅起本机控制台，页面 = 接入向导。
- **node.yaml 存在但 hub_url 为空 → standalone**：全功能本机自治。
- **node.yaml 存在且 hub_url 非空 → 组网**：现全功能。
- **yaml 解析失败 → 报错退出**（不静默进 setup，防止向导覆盖用户手编配置掩盖笔误）。
- 组网控制台新增「断开组网」入口（确认弹窗提示：断开后 hub 信箱内未收任务将待过期；本机历史任务不受影响）。验证矩阵 #9 由 UI 入口可达。

```
                 node.yaml 不存在 / hub_url 为空
                            │
                            ▼
                    ┌──────────────┐
        网页接入向导 │  setup 模式   │ 只起本机控制台（8445 回环）
                    └──────┬───────┘ 向导：粘贴接入串 → 预览 node.yaml → 保存
                           │ 写盘 + 重载
                 ┌─────────┴─────────┐
                 ▼                   ▼
        ┌──────────────┐    ┌──────────────┐
        │  standalone   │    │    组网在线    │◄── hub_url 非空即组网
        │  本地直执行    │    │  WSS 外连 hub │    连接断开自动重连（现状）
        └──────────────┘    └──────────────┘
```

- setup 模式：控制台页面 = 接入向导 + 有限的本机浏览（可先看本机环境，不能派单——无执行目标配置时向导引导先建 agent）。
- standalone：全部功能，除全网相关（本就无）。
- 组网：现状全功能。

### 3.2 执行链路（G1）：双路径模型

**本机入口的派单永远本地直执行，组不组网行为一致（伦哥 2026-10-22 定：组网后本机统一入口照常可用）。**

- localapi 派单（网页/node MCP）：不论形态，直接本地创建任务 → 本地 ledger 记账 → runner 执行 → 结果回写。不绕 hub。
  - 组网红利（断线补推/统一审计）只适用于 hub 下发的任务；本机自派任务 hub 不可见，此为本机自治的既定语义（执行权在本机主人手里）。
  - 白赚的能力：**离线自治**——hub 挂了/断网，本机总理照常干活。
- hub 经 WSS 下发的任务：保持现投递链路（offer/ack、信箱补推、needs_review 语义）。
- 两路径共享 runner 与 ledger；task_id 均 t- 前缀本地生成，账本同库无冲突面。
- 幂等键：本地直执行路径由本地账本实现同键查重（同键重试返回原任务），不再依赖 hub 幂等。
- **账本条目新增 origin 字段（local/hub）**（zcode 审查补强，阻塞项）：ResendPending/Reschedule/needs_review 回传按 origin 过滤——standalone 期本地任务的结果帧，在切换组网后不得成批推给 hub（避免 task_not_found 噪音）。并发写安全由 ledger 单写 goroutine + flock 既有机制保障，两路径写入同一事务通道。
- 本地直执行仍拒绝 target_agent 非本机 agent（裁剪方案 D8 校验语义保留）。
- **简化红利**：原 SubmitLocal 经 hub 帧往返的路径整体删除，代码更干净。

### 3.2.1 总理语义（伦哥 2026-10-22 定：客户端钦定唯一总理；v2 做实）

- **每台 node 由本机主人钦定唯一总理**：node.yaml 新增 `prime_agent: <agent名>` 声明；node 生成两份凭证：
  - `local_token`（既有）：本机控制台/只读操作（查目录、查状态、取产物）。
  - `prime_token`（新增）：编排操作（submit/review/cancel）专属。**用 local_token 调编排接口 → 403，可测**。
- **唯一性 = 凭证分发纪律**：主人只把 prime_token 配给钦定总理的 MCP 连接（SWARM_NODE_PRIME_TOKEN）。非总理手中无编排凭证，本机任务图强制为星状（总理→工人），意外环结构上被排除。
  - 安全边界诚实声明：prime_token 落在本机文件（0600），本机恶意进程本就可直接跑命令，不经 MCP——防环目标是不经意环（多 agent 都配了 MCP 意外互派），非恶意模型。安全边界 = 本机用户边界。
- node.yaml 与 data_dir 双真相源收敛：prime_token 存 data_dir/prime_token（同 local_token 惯例）；node.yaml 的 prime_agent 仅声明展示。轮换：控制台一键轮换 → 即时重载 + 写盘（替换轮换防半写），旧凭证立即失效；运行中旧总理 MCP 连接下一请求 401，文档写明换总理三步（改声明 → 轮换 → 新总理配新凭证）。
- **两层总理各管各的**：node 总理管本机自治编排（standalone 与组网形态下都可用）；hub 总理（P4.0 复合目标分解）管全网编排。hub 派单直接指定 target_agent，不经 node 总理中转；node 总理不被 hub 感知。
- 工人 agent 只产出结果与产物，无编排接口可调（编排端点验 prime_token，工人只有 local_token 或无凭证）。
- 控制台明示「本机总理：〈agent名〉」；未声明时提示「尚未钦定总理，编排接口未激活」（prime_token 已生成但不分发，编排接口保持 403）。
- setup 模式下编排接口同样 403（无 agents 可编排），MCP 连接得到明确错误提示先完成接入与能力配置。

### 3.3 setup 模式（G2，v2 补强）

- **三态入口**：仅 node.yaml 文件不存在时进 setup（yaml 存在但解析失败报错退出，见 3.1）。
- 仅起 localapi（回环 8445）+ 控制台页面。
- 控制台 setup 页 = 接入向导：
  1. 粘贴接入串（hub 向导页新增「复制接入串」按钮，格式 `swarmmind://join?hub=<wss url>&node=<node_id>&token=<64hex>`，URL encode）
  2. 预览将写入的 node.yaml（agents 段留空可后续网页加）
  3. 保存 → 写盘 → HTTP 应答先返回（用户看到「已写入」）→ 延时优雅退出，launchd/systemd KeepAlive 拉起进入组网模式；裸前台跑显示重启命令
- **写盘路径**：写回启动时的 `--config` 原路径（绝不写示例文件）；`data_dir` 落盘前绝对路径化（防 cwd 漂移导致双 data_dir 双 token）。
- **鉴权**：setup 端点同样验 local_token（首启已生成存 data_dir/local_token），setup 页明示 token 位置与本页粘贴方式——防免鉴权写盘变成 agents.command 注入 RCE 面。
- 校验复用接入串同源规则（见 3.6 下沉 protocol），拒绝非法输入。

### 3.4 node MCP（G3，v2 补强）

- `swarmmind-mcp` 加环境变量 `SWARM_TARGET=node`（默认 hub，向后兼容）：
  - `SWARM_NODE_URL`（默认 `http://127.0.0.1:8445`）+ `SWARM_NODE_TOKEN`（local_token，只读工具用）+ `SWARM_NODE_PRIME_TOKEN`（总理凭证，submit/review/cancel 用）
- **backend interface 重构**：hub/node 两实现，工具路由按模式裁剪，不在 12 个工具里散落 if；工具描述按模式生成（node 模式明示总理语义与本机边界）。
- 工具集（node 模式）：`swarmmind_agents`（本机目录）、`swarmmind_submit`（本机派单，prime_token）、`swarmmind_status`/`swarmmind_wait`、`swarmmind_artifact`。`swarmmind_nodes` 语义改为「本机节点信息」单条返回。
- 安全：stdio 传输天然仅本机；token 只落本机 MCP 客户端配置。
- hub 模式现有工具完全不变。

### 3.5 agents 网页编辑 + 热重载（G4，v2 补强）

- 控制台新增「能力配置」区：ExecProfile 字段表格增删改，**含 allow_args/allow_env/config_home 等安全关键字段**（服务端强校验：command/config_home 绝对路径、allow_env 仅白名单键名），只读字段灰显并注明理由。
- **yaml 保真**：node.yaml 是用户手编文件，读写走 yaml.Node round-trip 保留注释与未知字段，不整文件重 marshal。
- **热重载优先**：exposed 开关已有热生效先例（agents-state overlay）——普通字段编辑（description/timeout_sec/exposed 等）热重载 runners 即时生效；结构性变更（新增/删除 agent、command 变更）标「待应用」，「应用并重启」仅用于此类及 hub_url 变更，避免杀在途任务制造 needs_review 噪音。
- 重启语义同 3.3：HTTP 应答先返回，再延时优雅退出由保活拉起。

### 3.6 接入串协议（G5，hub 侧小改）

- hub 管理台接入向导「安装」步新增「复制接入串」按钮（与部署包下载并列，作为网页化路径）。
- 串格式：`swarmmind://join?hub=<wss>&node=<id>&token=<64hex>`，值 URL encode。
- **校验同源下沉**：node_id/token/hub_url 三个白名单正则从 internal/hub/api/enroll.go 下沉到 protocol 包，hub/node 两端 import 同源（消除两处规则漂移）。
- 不新增 hub 端点，纯前端拼串。邀请码发现式加入（填地址列身份）留作后续迭代。

## 四、验证矩阵（框架，开发时细化）

standalone：
1. 无配置启动 → setup 模式，控制台可访问
2. setup 向导粘贴接入串 → 落盘 → 重启后组网在线
3. standalone 下派单本机 agent → completed，本地账本可查
4. standalone 下 node MCP 全工具冒烟（agents/submit/status/wait/artifact）
5. standalone 下幂等键重试同任务

切换：
6. standalone → 组网（接入串生效）→ hub 下发任务正常投递执行
7. 组网模式下本机入口直派 → 本地直执行 completed（不经 hub，离线自治，本地账本可查）
8. 组网模式下 hub 下发任务 → 正常投递执行（双路径并行互不干扰）
9. 组网 → 断开组网 → 回 standalone，本地派发可用
10. 重启恢复语义两形态一致（执行中重启 → needs_review）

边界：
11. 接入串非法值（坏 node_id/token/hub_url）→ 拒绝并人话提示
12. MCP node 模式 token 错误 → 401 透传
13. 网页 agents 编辑非法输入（相对路径 command、allow_env 非白名单键）→ 拒绝
14. **非总理凭证派单 → 403**（local_token 调 submit/review/cancel；环防护可测化）
15. 未钦定总理（prime_agent 未声明）时编排接口 → 403 + 明确提示
16. setup 端点免凭证写配置 → 401（鉴权鸡蛋题验证）
17. yaml 解析失败启动 → 报错退出（不静默进 setup）
18. 断开组网入口：确认弹窗含信箱提示 + 本机历史任务不受影响
19. yaml.Node round-trip：编辑后原注释与未知字段保留
20. 热重载：exposed/description 等普通字段编辑即时生效不重启
21. 回归：组网模式全部现有行为不变（裁剪分支验证矩阵全数复跑）

> 编号连续无重复（v2 修正：正向 1-5、切换 6-10、边界 11-21，独立会话机械执行不混条）。

## 五、开发顺序（v2 依 zcode 工作量估算修订）

1. 前置：裁剪分支（docs/PLAN-prune-node-network-proxy.md）验证报告全绿 → 合入 main
2. 本方案基于合并后 main 开分支 `node-standalone`
3. 模块序：3.2 执行链路含 origin 字段（2-3 天，最大头） → 3.3 setup 模式（1.5-2 天） → 3.6 接入串含校验下沉（0.5 天） → 3.4 node MCP 含 backend 分叉（1-2 天） → 3.5 网页编辑（2-3 天，前端重于后端）
4. 关键依赖：origin 字段是 3.2 的阻塞子项；总理语义（prime_token）先于 3.4 编码定稿（工具描述文案依赖它）
5. 流程：zcode 编写 → 独立会话审查 → 意见回流 → 独立会话跑验证矩阵 → 全绿交付
