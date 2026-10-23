# SwarmMind 方案书（FINAL v3.0）

> 状态：**FINAL**，2026-10-20。五轮评审收口：R1 商议 → R2 复审（契约闭合）→ R3 尽调（自研结论+M1-M9）→ R4 新定位复审+AgentBridge 实测 → R5 终审（独立上下文，判定 YES WITH NOTES，全部修订项已清）。实现以 SPEC v3.0 FINAL 为唯一执行真相源。
> 进度：执行方式已定（甲），归位与 git 首提已完成；当前处于 P4（V1 长期可用）。DoD 11 双机实测经 P4.0 总理验收覆盖主要断言，断线/权限拒绝两项待正式验收归档。

## 一、项目定位

**愿景（大旗）**：跨机器 agent 协作的统一网络——主流 agent（codex、zcode、HanaAgent、Claude Code、Cursor、OpenCode 等）在同一张网上互相派任务。

**落地定位（V0/V1 执行口径，经尽调+竞品实测收窄）**：**小团队自托管的跨机器 coding agent 协作层**，可验证的差异化=安装更省事（Go 单二进制+token，对比 Rust+SLIM+DID 链）、权限更清楚（干净 env 基线+白名单+harness 权限映射）、交付更可靠（持久化账本/离线信箱/needs_review，对比竞品 InMemoryTaskStore）、产物更完整（完整答复+产物引用）。

**中立性原则**：协议与 Agent Card 不预设任何特定 agent 私有字段；不绑定任何模型厂商；对所有生态一视同仁。

两种服务：

- **hub（中心端）**：组网枢纽（接入、身份、信令）+ agent 能力发现（Agent Card）+ 任务信箱与路由。部署在有公网 IP 的服务器上，全网唯一可达点；不持有任何机器的执行权。
- **node（客户端 daemon）**：声明本机 agent（CLI 形态起步），主动外连 hub 加入网络，取任务，执行，回传。每台机器跑一个，零配置公网可达性。

## 二、总体架构

```
┌──────────────────────────────────────────────┐
│ 应用层：任意 agent 与人                          │
│   CLI：zcode / codex / claude code / opencode    │
│   IDE & 桌面：cursor / codex desktop / HanaAgent │
├──────────────────────────────────────────────┤
│ 协议层：A2A v1.0 语义（Agent Card / Task）      │
├──────────────────────────────────────────────┤
│ 适配层：CLI adapter（V0）→ A2A/MCP adapter（V0.5+）│  ← SwarmMind 核心价值
│         → IDE/app adapter（V2，逐个定制）        │
├──────────────────────────────────────────────┤
│ 组网层：node→hub 外连加密隧道 + hub 中继          │  ← SwarmMind 自有组网
│         （V1 实验：P2P 打洞直连，失败回落中继）    │
└──────────────────────────────────────────────┘
```

**Adapter 路线（统一生态的接入面）**：

- **CLI adapter（V0）**：zcode、codex、claude code、opencode 等主流 coding agent 全是 CLI 形态，spawn + stdin/stdout 即接入，V0 一步覆盖绝大多数目标
- **标准接入（V0.5）**：A2A v1.0 官方端点（a2a-go）；MCP adapter 为薄客户端接入（MCP 客户端能力≠可被远程调度，「双向」需 host 配合，无零改造承诺）
- **IDE/app adapter（V2）**：Cursor 基础接入可经其 CLI/ACP 提前至 V0.5；编辑器会话联动等深层体验逐个定制

**中立性原则（统一生态的设计底线）**：协议与 Agent Card 不预设任何特定 agent 的私有字段；对所有生态一视同仁；SwarmMind 自身不绑定任何模型厂商或 agent 框架。

### 组网层设计（仿 RustDesk 架构自研，Go 实现）

**许可边界先行**：rustdesk-server 是 AGPL-3.0，只借鉴架构思想与协议形态，不抄代码（版权不保护架构，但保护代码）。SwarmMind 自研 Go 实现，无许可证传染。

**RustDesk 原型的组网骨架**（我们参照的靶子）：

- hbbs（ID/信令）：UDP 21116 注册心跳、TCP 21116 打洞协调、TCP 21115 NAT 类型测试
- hbbr（中继）：TCP 21117，打洞失败时转发端到端加密流量（中继只见密文）
- 流程：客户端注册 → A 连 B 时 hbbs 交换地址候选并协调打洞 → 成功 P2P 直连（多数情况）→ 失败回落 hbbr 中继

**SwarmMind 的特化改造**（场景差异：远程桌面是按需连接，agent 协作是常驻会话）：

| RustDesk | SwarmMind | 改造理由 |
|---|---|---|
| UDP 注册+心跳，按需建立连接 | node→hub 一条 TLS WebSocket 长会话，会话即注册即心跳 | agent 协作要求随时可达，常驻会话让 hub 实时掌握拓扑，任务可主动推送 |
| 打洞直连为主，中继兜底 | hub 中继为主（V0 唯一路径），V1 打洞直连为优化 | 消息过 hub 本就是路由与审计需要；先保通再提速 |
| 中继只见密文（端到端加密） | V0 hub 可见明文；V2 借鉴其 Ed25519 密钥模型做端到端加密 | 个人规模先跑通，密码学后置但路线预留 |
| 五端口分工 | 单 TCP 端口复用（如 8443）：注册/心跳/信令/中继全走一条 TLS 隧道 | agent 消息量小，不需要拆分；防火墙只开一个口，穿透面最小 |
| hbbs/hbbr 两进程 | V0 hub 单进程一体；V1 可选拆 swarm-relay（大文件直传不挤信令） | 参考其信令/中继分离的扩容思路，V0 不需要 |

**连接模型**：

- 消息信封（借鉴 Syncthing BEP 显式消息头 + Centrifugo 恢复协议）：字段含 version/type/message_id/from/to/reply_to?/idem_key?/session_epoch?/payload/trace_id/err?/retryable?/expire_at?（JSON 字段名以 SPEC 4.1 tag 为准：v/mid/idem/epoch），V0 用 WebSocket 文本帧 JSON。from 由 hub 按连接鉴权身份强制覆盖
- 消息类型：register / register_ack / ping / pong / card_update / task_submit / submit_ack / task_offer / task_ack / task_started / task_result / result_ack / result_event（终态推给提交方）/ artifact_fetch / artifact_data（产物按需拉取，单文件 ≤1MiB）/ err
- **可靠性契约（三段持久化确认，至少一次投递+幂等，语义仿 NATS JetStream）**：submit→submit_ack｜offer→task_ack（node 账本 accepted 落盘后发）｜result→result_ack。任务状态机 submitted→offered→acknowledged→running→终态（completed/failed/needs_review），迟到事件按 SPEC 4.3 收敛表处理，终态永不回退
- **node 执行账本**：持久化完整任务+状态+结果；accepted 重启后安全续执行；started 中断重启 → needs_review（独立终态，禁止自动重跑）；finished 未确认重发结果。任务执行 context 独立于会话，断网不杀任务
- **会话生命周期**（不承诺断开即感知）：Ping 15s / Pong 45s / 读写 deadline 60s / 注册首帧 5s；重连指数退避带抖动，注册成功后重置；**每次连接全量鉴权**（V0 无凭证复用/撤销，V2 再建）；epoch 由 hub 分配持久化、每次重连递增，入站帧不匹配忽略/断开，历史任务跨代次补传不受限
- **背压与配额（单端口≠单连接，参考 MQTT inflight window）**：投递窗口 = 已投递未开始（offered+acknowledged）≤ 8；发送队列 4MiB 满则暂停该 node 出队（控制帧独立小缓冲），持续 60s 无法出队才断开；单消息 1MiB（整帧字节，提交入口预检）；信箱 100 条/node 出队即释放，满则 503 mailbox_full；执行并发默认 2；任务 7 天过期、终态 24h 清理
- 派单入口：HTTP 管理面（独立监听 api_listen 绑回环）+ node 间互派走会话信封；**无 HTTP 轮询回退**（离线=信箱+重连补推，避免绕过可靠性机制的第二条路径）
- 密钥模型：hub 首启生成自签证书；node 预共享**每 node 独立** register_token（绑定 node_id）+ hub 证书 SPKI 指纹（TOFU 首连记录）；鉴权失败不重试；执行环境用声明式干净基线，不继承 daemon 进程环境

## 三、关键技术决策（含备选与理由）

| # | 决策 | 选定方案 | 备选 | 理由 |
|---|------|---------|------|------|
| D1 | 协议底座 | 内部协议语义对齐 A2A Task，V0.5 引入官方 a2a-go 暴露标准端点，状态做显式映射（submitted/running/completed/failed → TASK_STATE_SUBMITTED/WORKING/COMPLETED/FAILED），不做无缝兼容承诺 | V0 直接上 a2a-go / 完全自研 | V0 优先跑通闭环；显式映射可控可验 |
| D2 | 组网模式 | **仿 RustDesk 自研**：node→hub TLS 长会话（注册/心跳/信令/中继单端口复用）+ hub 中继全部消息；V1 hub 信令打洞直连（多数场景可直连时切换），失败回落中继 | 借用 tailnet / libp2p 全家桶 / tsnet | RustDesk 验证过的成熟形态，开源可参照（AGPL 只借架构不搬代码）；常驻会话适配 agent 场景；单端口穿透面最小 |
| D3 | 任务模型 | 全异步：提交即返 task_id，**长会话推送 TaskOffer + Ack**，消息经 hub 中继；无轮询回退 | 同步 RPC / 纯轮询 / HTTP 拉取 | agent 任务分钟到小时级，同步必死；拉取路径会绕过账本与 Ack 体系，已删 |
| D4 | 技术栈 | Go，中心端+客户端同栈；WebSocket 用 coder/websocket（维护活跃、上下文原生），HTTP/TLS 用标准库；依赖例外清单：yaml.v3、coder/websocket | TypeScript / Python / libp2p | 单二进制、低内存、Windows 服务化成熟；标准库无完整 WebSocket，自研帧协议不值；libp2p 依赖树太重 |
| D5 | 信任模型 | hub 准入制：**每 node 独立 token 绑定 node_id** + 证书 SPKI 指纹 TOFU + 干净 env 基线（不继承 daemon 环境）；**node 本地白名单是真闸门**；allow_args 语义=授权该命令全部参数能力（解释器类=通用执行，文档明示）；hub 可见明文，端到端加密 V2 | 零鉴权 / 端到端加密先行 | codex 两轮修正：hub 被攻破可伪造派单；共享 token 可抢注身份刷 epoch；env "*" 时 PATH 类变量可改变命令行为——防线在 node 本地白名单+干净基线 |
| D7 | agent 接入模型 | **接入主权在客户端**：node 自主决定本机暴露哪些 agent（每 agent 接入开关，exposed 语义入契约）；hub 仅做注册登记（事实视图供审计），无全网搜寻市场语义；派单在「已注册且已暴露」范围内显式指定 | 中心目录自由浏览调用 | 接入主权在本机，与 MeshConsole 安全 gate 精神一致；hub 保持登记处而非市场 |
| D6 | hub 部署 | hub 必须公网可达：V0 本机回环开发，V1 上腾讯云（端口避开 443/8080/3000/21115-21119 已占用段，如 TCP 8443） | Mac mini（需端口映射，重新引入穿透问题） | 公网 hub 是组网前提；与 RustDesk/Headscale 同机共存，运维面不扩张 |

## 四、V0 范围

**做**：hub（TLS 长连接接入、注册/心跳/能力表、任务信箱与中继路由、状态原子落盘重启恢复）；node（YAML 声明 CLI agent、外连入网、注册、断线指数退避重连、取任务、spawn 执行、回传）；任务状态机 submitted→offered→acknowledged→running→completed|failed|needs_review；trace_id 全链路；离线信箱；回环 demo 脚本；真实 agent profile（codex/claude code 至少二种，双卡分离+权限映射）；进程组整组终止；产物协议（outputs 目录约定+拉取帧）。

**不做**：A2A 标准端点、P2P 打洞直连、desktop MCP 桥、端到端加密、Windows 服务化、动态能力下发。

**验收（DoD 十一条，详 SPEC 第七节）**：build+go.sum；单测（状态机收敛表/配额/账本/runner/白名单/幂等）；回环全链路（FinalAnswer）；超时+进程组；离线信箱；重连+epoch；hub 重启；幂等去重与 409；白名单 policy_denied；needs_review 路径；真实异构闭环（双机双 agent，归位后执行，不阻塞本地交付）。

**V0.5 预告**：a2a-go 标准端点（统一生态关键一步，A2A v1.0.0 状态显式映射）、MCP 薄客户端 adapter、任务租约（fencing token 防 zombie 写）。
**V1 实验项（带指标再决策）**：P2P 打洞直连（先测直连率/建立耗时/回落耗时，参考 libp2p DCUtR、Pion ICE；TCP 长会话观察到的地址不能直接当 UDP 打洞候选）、QUIC 隧道（连接迁移，需 UDP 可达）。
**V2 构想**：端到端加密（Ed25519 签名+会话密钥，中继只见密文；Ed25519 本身不是加密方案）、能力动态下发、跨机任务链、大产物按需直传（控制流长期走 hub，仅大文件 P2P）、IDE/app adapter 生态（Cursor 等逐个接入）。

## 五、执行方式变更（环境约束，需你知情）

本会话走飞书 Bridge，实测两个硬约束：

1. **写不了 ~/coding**：Bridge 会话沙盒只放行工作台，coding 目录授权只有桌面会话能批。
2. **zcode/codex 拉不起来**：审批请求在 Bridge 上下文弹不出，多种姿势均被拒。

两个走法，选一个：

- **甲（推荐，已按此执行到冻结点）**：我在工作台 `dev/SwarmMind/repo/` 写代码 + 独立上下文审查（codex 不可用时的制度兜底：写审隔离改由全新 subagent 会话承担）+ 本地验证，DoD 1-10 全绿后，你点头，一条命令归位（DoD 11 双机验收归位后单独执行） `~/coding/personal/sandbox/SwarmMind`，git 提交按钮在你手里。
- **乙**：你方便时在 Mac 桌面端开个会话说一声「授权 coding」，桌面会话走标准流水线（zcode 写 + codex 审），本会话把 SPEC/方案全部移交过去。

## 六、开工前尽调与竞品实测（2026-10-20）

**竞品实测（首要对标 AGNTCY AgentBridge v0.3.0，实测报告 agentbridge-test/实测报告.md）**：

| 维度 | AgentBridge（实测/文档确认） | SwarmMind |
|---|---|---|
| 任务存储 | InMemoryTaskStore，崩溃即丢 | 持久化账本+快照恢复 |
| 交付语义 | execute_prompt 单发单收 | 三段 Ack+幂等+投递窗口+needs_review |
| 离线 | 无信箱，不在线即失败 | 离线信箱+重连补推（7 天） |
| 执行治理 | 无超时/白名单/干净 env | 全套（见 SPEC） |
| 结果 | 纯文本 | 完整答复+产物引用+结构化错误 |
| 部署 | Rust+SLIM 节点+DID 身份链 | Go 单二进制+每 node token |
| 强项 | 编排（proposal/vote/converge）、上下文交接、DID 身份、A2A 原生 | 交付可靠性+接入摩擦 |

其余竞品：ContextForge（IBM，MCP/A2A 聚合网关）、SLIM 消息网、Google Agent Mesh（组网重、任务不落中心）、NATS（消息底座）、Dagu（DAG 调度）。企业级 registry（Google/AWS/Copilot Studio）非我们的战场。

**三选一结论（维持）：自研轻量任务层 + 成熟组件**；但差异化必须按上表在「交付可靠性+接入摩擦」上实测对比成立，不以功能清单宣布领先。

**codex 以接入方身份的评审结论**：愿意试点，条件=任务权限明确、结果完整、调用方闭环、异常不盲重跑。据此升级结果协议与权限映射（已并入 SPEC v3.0）。

**工作量**：V0 真实异构闭环约 25-38 人日（原 21-31 + 真实 agent profile 与双机验收）；实现顺序：先内存 transport 验证协议再接 WebSocket（SPEC 第八节）。

## 七、项目生命周期管理（2026-10-20 立，取代「做一步看一步」）

> 交互设计与用户旅程规格见 INTERACTION.md（v1.0：旅程矩阵/页面树/每页主行动/反馈闭环五规则/无提示任务测试验收/分级增补清单）；SPEC 增补按其分级时机执行。

**原则**：方案期（Phase 0）已结束，后续按阶段推进；每阶段有明确交付物与验收关口（Gate）；任何新想法（包括新模型、新 UI、新协议特性）一律进入对应阶段的 backlog，不打断当前阶段。**UI/网页类交付一律原型先行：codex 输出可交互原型（HTML）→ 伦哥过目拍板 → 才允许进入实现。**

**P1 架构预留清单（群聊 RFC 降下的边界，不实现只留位）**：Envelope 的 to 字段预留 group 寻址命名空间；消息类型注册表集中定义（防 V1 加群帧时动协议层）；runner 的 prompt 组装层独立成模块（V1 context-injection 的挂点）。

**MCP 同步开发制（2026-10-21 立，SPEC 六.六）**：MCP 与 REST 管理面、双端控制台同为一级接口面。所有面向管理面的新功能（P4 群聊/任务租约、P5 产物链/Git 集成…）在阶段交付物中必须**同步列出 MCP 工具项**——实现时同一变更内交付 SPEC 六.六映射表行+工具+实测，评审时 codex 对照检查。禁止「HTTP 先行、MCP 以后补」。

| 阶段 | 目标 | 交付物 | Gate（进门条件=上一阶段完成） |
|---|---|---|---|
| **P0 方案期** | 方案冻结 | PLAN/SPEC FINAL、竞品实测报告、模型评估 | ✅ 已完成（五轮评审+实测） |
| **P1 V0 核心** | 单机回环+真实 agent profile | hub / node / swarmmindctl / demo 脚本 | DoD 1-10 全绿 + 伦哥验收归位 ~/coding + git 首提 |
| **P2 V0 产品验收** | 真实异构闭环 | 双机部署文档+实测记录 | DoD 11（双机双 agent：发现/互派/产物取回/断线/权限拒绝） |
| **P1.5 文件协作互斥**（填 P2 等待期，2-3 人日） | 同 workdir 串行性 | 调度互斥表+eligible 门控+workspace_id 上报+DoD 四项断言 | FILE-COLLAB L1 DoD 全过 |
| **P3 V0.5 标准接入** | 生态标准接口+Web 控制台 | A2A v1.0 端点（a2a-go）、MCP 薄客户端、**Web 控制台（codex 原型 v2 已出，亮色带操作：登录制+派任务+任务管理+needs_review 核对，实现前原型经伦哥终版拍板）** + **账户体系（密码登录/PAT/恢复通道，v0.1.1 已交付）+ MCP 统一入口（6 工具，v0.1.1 已交付，SPEC 六.六）** | A2A 互操作实测（外部 A2A 客户端能发现并调用网内 agent）+ 控制台上线（登录制鉴权） |
| **P4 V1 长期可用** | 无人值守稳定 + 协作空间 | 任务租约（fencing token）、Windows 服务化、凭证管理、DecisionProvider 实验（StartLux 影子建议先行）、**群聊/协作空间（GROUP-RFC，MVP：人建群点名发言+转任务+结果回流）+ 产物链与依赖（FILE-COLLAB L2：depends_on idem 寻址/纯复制挂载/跨机取回）** | 连续 14 天无人值守稳定 + 降级演练通过 + 群版 DoD 增补通过 |
| **P5 V2 生态扩展** | 按需 | 端到端加密、IDE/app adapter、能力下发、**Git 集成（FILE-COLLAB L3：worktree/branch 模式+自动 commit+冲突回群+janitor，8-12 人日，前置=凭证方案+保留窗对齐）**、开源发布工程（若开源：文档/CI/社区） | 按需定义 |

**角色分工**：Hana=编排与验证；zcode=写（桌面授权后）/codex=审+UI 原型（read-only 评审本会话可跑）；伦哥=每个 Gate 的唯一验收人。**阶段性回望**：每阶段 Gate 通过时，Hana 输出阶段总结（交付物清单/偏离记录/下一阶段计划）同步飞书。

## 八、风险与开放问题

- 任务租约：V0 无自动回收，靠 node 执行账本保证不重复执行；V1 加 fencing token 防 zombie 写
- Windows 节点：V1 服务化时验证（Job Object 进程治理、信号、服务包装、WaitDelay 防管道卡死）
- 持久化边界：V0 保证进程崩溃恢复（快照+原子写+变更串行化+写败不确认）；掉电耐久性不承诺（无全量 fsync），个人规模可接受，写入文档
- Go 关闭细节：http.Server.Shutdown 不关 WebSocket，hub 自维连接集合并等待退出；进程组治理为 V0 必做（Unix setpgid+kill(-pgid)），Windows Job Object V1
- **开放问题处理状态**：①副作用策略→已定（needs_review 语义，禁止自动重跑）；②hub 角色→已定（V0 hub 即管理员即授权，V2 再细分）；③容量边界→已定（写入 SPEC 4.6：node ≤20、离线 7 天、单任务 ≤1MiB、字节预算、低负载部署 ≤5 node）。终审 R5 判定 YES WITH NOTES，全部修订项已清（PLAN v3.0 / SPEC v3.0 FINAL）。历史开放问题原文留档：
  1. 任务是否允许不可重复副作用？若允许，重试策略按任务声明区分（幂等任务可积极重投，副作用任务只人工核对）
  2. hub 角色：纯路由还是派单授权者？建议 V0 hub 即管理员即授权，V2 再细分授权体系
  3. 「开箱即用」覆盖边界：企业代理/仅 443 环境 V0 不承诺，文档写清适用范围；容量边界建议 V0 定 node ≤20、离线保留 7 天、单任务 ≤1MB、信箱 ≤100 条/节点

## 决策请求

请对 D1-D6 逐项表态（同意/改），并对「执行方式甲/乙」选一个。全部确认后我按方案继续。
