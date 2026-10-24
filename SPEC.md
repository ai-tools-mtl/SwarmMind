# SwarmMind SPEC — V0（v3.0 FINAL）

> 上位文档：PLAN.md v3.0 FINAL。本文是 V0 的唯一执行真相源。
> 版本轨迹：v2 契约闭合 → v2.1 Schema/产物/权限升级（内容升级时未滚动版本号，终审修正）→ v3.0 FINAL：终审修订——产物拉取协议、FinalAnswer 提取规则、版本号统一、校验链顺序、60s 断开规则、ExecProfile 字段补齐、DoD 十一条。

**目录**

- [一、V0 目标](#一v0-目标)
- [二、进程、端口与 TLS](#二进程端口与-tls)
- [三、目录结构](#三目录结构)
- [四、协议](#四协议internalprotocol)
- [五、node 配置](#五node-配置configsnodeexampleyaml)
- [六、交互面与管理 API](#六交互面与管理-api)
- [六.五、P3 增补契约（Web 控制台）](#六五p3-增补契约web-控制台按-interactionmd-分级清单落地)
- [六.六、MCP 服务](#六六mcp-服务agent-统一入口2026-10-21-立)
- [七、验证标准（DoD 十一条）](#七验证标准dod-十一条)
- [八、实现顺序](#八实现顺序工程路线先协议后网络)
- [九、已知陷阱](#九已知陷阱写代码前必读)

## 一、V0 目标

单机回环跑通完整拓扑：hub + 两个 node 经 WSS 长会话组网，HTTP 管理面提交任务，推送执行，结果回传；断线重连、离线信箱、幂等去重、白名单、needs_review 全部落地。**所有代码按公网部署标准写**（鉴权、配额、背压、epoch），只是部署在回环上。

## 二、进程、端口与 TLS

- hub 单进程双监听：
  - `node_listen`（默认 `:8443`）：node 会话入口，路径 `/ws/node`。TLS 证书 hub 首次启动自签生成存 `data/`（ECDSA P-256）。配置 `insecure_loopback: true` 时降级为 ws:// 且强制校验监听地址为 127.0.0.1/::1，非回环拒绝启动（代码强制，非文档约束）。
  - `api_listen`（默认 `127.0.0.1:8444`）：HTTP 管理面 `/api/v1/*`，与节点监听物理分离。**V0 强制只绑 127.0.0.1**（代码校验，无鉴权面不得离开回环；未来需要内网访问时随管理鉴权一起设计）。
- node 单进程：不监听任何公网端口，仅提供本机回环 API（127.0.0.1:8445，提交/查询）。指纹校验：`hub_pubkey_fp` = 证书 SPKI 的 SHA-256（base64）；TOFU——首连成功将 `host:port→fp` 记入 `data/trusted_hubs.json`，此后不匹配即断开告警；指纹非空则首连即强校验（TLS 握手时完成 pin 验证，验证通过后才发 register_token）；TOFU 记录写失败仅告警不阻断。
- 鉴权：register_token 每 node 独立（hub 配置 `nodes: {<node_id>: <token>}` 映射），token 绑定 node_id，不可跨身份使用。**每次连接全量鉴权**（V0 无会话凭证复用/撤销机制，V2 再建），连接本身即会话。防重复执行保证范围=同一 data_dir 账本（单实例锁，见 4.7）；复制 token 到他机属新 node 需新 token。

## 三、目录结构

```
repo/
├── go.mod                      # module github.com/mtl802/swarmmind
├── cmd/hub/main.go
├── cmd/node/main.go
├── cmd/swarmmindctl/main.go       # 人工操作 CLI：submit/query/agents/nodes/review，薄包装管理面 API
├── internal/
│   ├── protocol/               # 信封、消息类型、状态机、常量（hub/node 共享）
│   ├── hub/
│   │   ├── session/            # 会话表、epoch、心跳、投递窗口、写串行化
│   │   ├── mailbox/            # 信箱：配额、去重、补推
│   │   ├── store/              # 持久化事务（先写后见）
│   │   └── api/                # HTTP 管理面
│   └── node/
│       ├── conn/               # 外连、重连、会话恢复、消息分发
│       ├── ledger/             # 执行账本（持久化完整任务）
│       └── runner/             # CLI adapter：spawn、超时、白名单、干净基线 env
├── configs/hub.example.yaml, node.example.yaml
└── scripts/demo-loopback.sh
```

**依赖白名单（V0 冻结）**：新增依赖必须在本清单与 go.mod 注释同步登记理由，禁止未登记引入。现有全部：

| 依赖 | 用途 | 理由/边界 |
|---|---|---|
| github.com/coder/websocket | node→hub WSS 长会话 | 标准库无 WebSocket 实现 |
| gopkg.in/yaml.v3 | hub/node 配置解析 | 标准库无 yaml |
| golang.org/x/sys | Windows 服务化（svc/svc/mgr）+ Job Object 进程治理（P4②） | 半标准库（Go 官方扩展库）：Windows SCM 服务编程唯一正道，无更轻替代；仅 `//go:build windows` 路径引入，非 Windows 平台编译产物不含相关代码，行为零变化 |

## 四、协议（internal/protocol）

### 4.1 信封（WebSocket 文本帧，每帧一个 JSON；V1 评估二进制帧）

```go
type Envelope struct {
    Version      int             `json:"v"`                    // 当前 1；不支持的版本回 err 帧并断开
    Type         string          `json:"type"`                 // 见 4.2
    MessageID    string          `json:"mid"`                  // 去重键
    From         string          `json:"from"`                 // hub 入站方向强制以连接鉴权身份覆盖，不信帧内值
    To           string          `json:"to"`
    ReplyTo      string          `json:"reply_to,omitempty"`
    IdemKey      string          `json:"idem,omitempty"`
    SessionEpoch int64           `json:"epoch,omitempty"`      // 规则见 4.4
    FenceEpoch   int64           `json:"fence_epoch,omitempty"` // 任务租约代次（P4，规则见 4.11）：offer 携派发代次、cancel 携任务落库租约（cancel_fence，P1-2），task_result 由 node 回带
    TraceID      string          `json:"trace_id,omitempty"`
    Payload      json.RawMessage `json:"payload"`
    Err          *ErrInfo        `json:"err,omitempty"`
    Retryable    bool            `json:"retryable,omitempty"`
    ExpireAt     time.Time       `json:"expire_at,omitempty"`
}
type ErrInfo struct {
      Code string `json:"code"`; Msg string `json:"msg"`
      FenceEpoch int64 `json:"fence_epoch,omitempty"` // 任务租约拒收回执携带：被拒结果回带值（P4/4.11，node 仅当与账本条目 fence 等值才置 closed）
      LeaseEpoch int64 `json:"lease_epoch,omitempty"` // 同上：拒收时刻 hub 任务落库租约（审计对账）
  }
```

### 4.2 消息类型

| type | 方向 | payload | 语义 |
|---|---|---|---|
| register | node→hub | `{node_id, register_token, agents[]}` | 连接后 5s 内首帧，否则 hub 断开 |
| register_ack | hub→node | `{ok, epoch, reason?}` | 成功携带分配的 epoch；拒绝则断开（错误码 auth_failed） |
| ping / pong | 双向 | 空 | 应用层心跳 |
| card_update | node→hub | `{agents[]}` | 能力表全量更新 |
| task_submit | node→hub | TaskSubmitRequest | node 本机派单（node 会话强制 target == env.From，见 §六边界锁；hub 鉴权后走同 HTTP 等价流程） |
| submit_ack | hub→提交方 | `{task_id, trace_id, dup}` | 提交已持久化（dup=true 表示幂等命中） |
| task_offer | hub→node | 完整 Task | 推任务；信封携派发租约 fence_epoch=该 node 当前注册代次（P4，见 4.11） |
| task_ack | node→hub | `{task_id}` | 任务已入账本（accepted 持久化成功后发） |
| task_started | node→hub | `{task_id}` | 执行已开始（started 持久化成功后、spawn 前发） |
| task_result | node→hub | `{task_id, status, result}` | 终态回传（status ∈ completed/failed/needs_review/cancelled；含 FinalAnswer 与产物引用）；信封原样回带 offer 的 fence_epoch（P4 租约，见 4.11） |
| result_ack | hub→node | `{task_id}` | 终态已持久化 |
| result_event | hub→提交方（node 会话时） | `{task_id, status, result}` | 终态推送给提交方，闭合「调用方拿回结果」；HTTP 提交方靠 GET /tasks/{id} 轮询。已知限制：提交方离线错过即弃，靠主动查询补 |
| task_query | 提交方→hub | `{task_id}` | 查询任务状态（node 会话仅限本机任务：Task.TargetNode == env.From，他节点任务拒绝；result_event 离线错过的补查路径） |
| task_status | hub→提交方 | Task | 查询响应（完整 Task 可能接近 1MiB 上限，超大任务以摘要+artifact 引用返回） |
| artifact_fetch | 提交方→hub | `{task_id, index}` | 请求取回第 index 个产物（hub 中继到执行 node） |
| artifact_data | 执行 node→请求方（经 hub 中继） | `{task_id, index, meta, data_base64}` | 产物内容（单文件原始 ≤640KiB，base64 后随帧 ≤1MiB；更大产物 V1 分块） |
| task_cancel | hub→node | `{task_id, reason?}` | 管理面取消（管理面 POST /tasks/{id}/cancel 触发）：node 整组终止在途执行，账本落 finished(cancelled/user_cancelled) 后回 task_result(cancelled)；至少一次+幂等，重复 cancel 安全；信封携 hub 当前任务落库租约（cancel_fence，P4/P1-2）：node 取消回写前对账本租约与 cancel_fence 取大者重臂，回执按宽校验（cancel_fence ≥ 落库租约）接受，见 4.11 |
| task_cancelled | node→hub | `{task_id}` | 取消确认 ack：任务已终态或不在账本（无可取消对象）；执行中断路径不走本帧（回 task_result 走三段确认） |
| err | 双向 | ErrInfo | 错误帧；协议级错误（版本/epoch 残余/格式）另按规则断开 |

### 4.3 任务状态机（hub 视角）与迟到事件收敛

```
submitted → offered → acknowledged → running → completed | failed | needs_review | cancelled
   ↑          |(ack 超时 30s 回退)
   └──────────┘
```

- **终态（completed/failed/needs_review/cancelled）永不回退。**
- offered 30s 未收 task_ack → 回 submitted 重新入队补推（任务过期则置 failed/expired）。
- **管理面取消（task_cancel 生命周期）**：submitted/offered/acknowledged/running 可取消（hub 事务内置 cancelled 终态；submitted 未投递直接撤回，其余向 node 发 task_cancel）；completed/failed/needs_review 不可取消（409）；重复取消幂等命中。cancelled 与 needs_review 同级独立终态：不折叠为 failed，不自动重跑。
- 迟到事件收敛表（hub 侧，全部先过幂等/归属检查——任务归属 node 由提交时绑定，任何 node 的帧只对其名下任务有效）：

| 事件 | 当前状态 | 处理 |
|---|---|---|
| task_result 回带租约过期 | 任意（门禁先于状态仲裁，P4） | 拒收 zombie 结果（zombie_result_rejected）：任务维持原状态（不改 Status/UpdatedAt/Result），Task.Lease 记审计（Rejects++），回 err(result_conflict)（msg 注明 fence_epoch/current_lease，ErrInfo 结构化携带 fence_epoch/lease_epoch）促停 node 重发——node 仅当被拒 fence 与账本条目当前 fence 等值才置 closed；见 4.11 |
| task_ack 迟到 | submitted（曾回退） | 接受 → acknowledged；不覆盖任何终态 |
| task_ack 迟到 | 终态 | 忽略 + 日志告警 |
| task_started 迟到 | acknowledged | 置 running |
| task_started 迟到 | 终态 / submitted | 忽略 + 告警 |
| task_result 到达 | 任意非终态（submitted/offered/acknowledged/running） | 接受 → 置对应终态（结果优先于状态机中间态） |
| task_result 到达（cancelled） | 已 cancelled 且结果未落 | 合并 node 回传的真实执行数据（退出码/日志尾/产物），重发 result_ack（取消分支） |
| task_result 重复 | 已同终态且结果一致 | 重发 result_ack |
| task_result 冲突 | 已终态但结果不同（含取消后 node 迟到的自然终态） | 保留先到，回 err(result_conflict) + 告警，不重发 ack |
| expire_at 到期 | 非终态任意状态 | 置 failed，error=expired（结果后到按冲突行处理） |
| task_cancel 到达（node 侧） | 账本 accepted/started（在途） | 整组终止执行（killProcessGroup），账本落 finished(cancelled/user_cancelled)，回 task_result(cancelled) |
| task_cancel 到达（node 侧） | 已终态 / 不在账本 | 回 task_cancelled 幂等 ack（重复 cancel 安全） |
| task_offer 到达但账本无此任务且 target_agent 不在当前配置 | -（配置漂移） | node 直接回 task_result failed，error=agent_not_found，不进重推循环 |

- node 侧对重复 task_offer 的处理：账本已有该 task_id → 不再执行，直接重发 task_ack（若已 finished 附带重发 task_result）。

### 4.4 epoch 规则

- hub 为每个 node_id 持久化 epoch 计数：首次注册成功 = 1；此后该 node_id 每次新连接注册成功原子 +1 并随快照持久化，register_ack 返回当前值。
- **register 帧豁免 epoch 校验**（首帧无 epoch）；hub 处理 register 的顺序：验证 token → 单写事务内（epoch+1 持久化 + 会话表替换旧连接 + 能力表更新）→ 发 register_ack（携新 epoch）→ 旧会话清理。事务失败则不发 ack、断开连接。
- hub 对其余入站帧校验 epoch：不等于该 node 当前 epoch → 忽略该帧 + 日志（旧连接残余），连续 5 次则断开。
- node 侧：register_ack 后记住 epoch；hub 入站帧（node 视角）epoch 不匹配 → 断开并按新连接重连。
- 历史任务的结果补传使用当前会话 epoch，不因任务创建于旧代次被拒。epoch 只隔离会话，不追溯已发生执行（见账本 needs_review）。
- **epoch 与任务租约（P4）的正交分工**：会话 epoch（本节）在传输层隔离「旧连接残余帧」——帧只能经该 node 当前会话进入仲裁；任务租约（4.11）在任务层隔离「旧派单的迟到回写」——回执必须对齐任务当前派发租约。两层校验独立生效，互不替代（重连后同一进程的结果补传过会话层、由租约层判定；未重派任务的跨代次恢复重发过租约层、按原租约等值接受）。

### 4.5 可靠性契约（三段持久化确认 + 幂等）

| 阶段 | 确认含义 | 未收到确认时 |
|---|---|---|
| submit → submit_ack | hub 已持久化（先写后见，见 4.7） | 提交方携同一 idem_key 重试 |
| offer → task_ack | node 账本 accepted 已持久化 | hub 30s 后回退 submitted 重推 |
| task_result → result_ack | hub 终态已持久化 | node 账本 finished 未 acked，重连后重发 |

幂等规则：idem_key 作用域为 hub 全局，与任务同生命周期（过期/清理同步删除）；同键同内容 → 返回原 task_id + dup=true；同键不同内容 → 409 conflict。**提交流程顺序固定：鉴权 → idem 查重（命中即返回，不做配额检查）→ 依赖准入校验（4.12，P4.3）→ 容量/字节预检 → 持久化（单写事务内全部重检）→ Ack**。保证同键重试在信箱满时仍能取回原 task_id（且旧提交的幂等重试不因依赖此后被清理而失败——依赖准入位于 idem 查重之后）。
重复消息去重后必须重放对应 Ack（见 4.3 收敛表），禁止静默丢弃导致确认悬挂。

**结果重传与终止条件（node 侧）**：账本 finished 未 acked 的结果在每次重连成功后立即重发，此后每 60s 重发一次；收到 result_ack → 停止；收到 result_conflict 或任务已 expired 终态拒绝 → 停止重传，账本标记 `closed`（冲突审计日志保留），不删除记录。

### 4.6 常量（protocol/constants.go）

| 参数 | 值 |
|---|---|
| 注册首帧超时 | 5s |
| 心跳 Ping 周期 / Pong 超时 | 15s / 45s（计时基于上次 pong 收到时刻） |
| 读写 deadline | 60s |
| 重连退避 | 0.5s 起 ×2 ±20% 抖动，上限 60s；注册成功后重置；auth_failed 不重试 |
| offer_ack 超时 | 30s |
| 投递窗口（每 node，已投递未开始 = offered + acknowledged 计数） | 8，超则暂停出队 |
| 发送队列 | 4MiB/会话；满则暂停该 node 出队（控制帧 ping/pong/ack 类走独立 64KiB 小缓冲，控制帧也满 → 断开，视为对端已死）；数据队列持续 60s 无法出队 → 断开重连（补推幂等，安全） |
| 单消息上限 | 1MiB = 1048576 字节，作用于完整信封序列化后字节数；**提交入口按最终 TaskOffer 完整编码实测校验（构建含 ID/时间/身份的完整信封后 marshal 计字节数），超限 400 拒收，不用余量估算**；register/card_update/result 等帧超限 → 回 err(frame_too_large) |
| 信箱配额 | 100 条/node（计 submitted 状态），出队转 offered 即释放；offered 超时回退 submitted 时若配额已满仍回队（重投预留，允许短暂超额，不丢已确认任务）；满则 503 + `mailbox_full` |
| stdout/stderr tail | 64KiB |
| 任务离线保留 | 7 天（expire_at 到期非终态 → failed/error=expired） |
| 终态保留 | 24h 后从快照清理；node 账本 result_acked 条目 24h 后清理 |
| node 执行并发 | 2（可配） |
| 信箱字节预算 | 每 node 8MiB（submitted 任务体字节和），超则 503 `mailbox_full` |
| hub 任务存储全局字节预算 | 256MiB（含终态 24h 保留），超则拒新提交 503 `storage_full`；部署声明：2核2G 云机目标为低负载（≤5 node 且预算内），满配（20 node×100 条×1MiB≈2GiB）不承诺 |

### 4.7 持久化事务（hub store 与 node ledger 同模式）

**先写后见**：单写 goroutine 串行处理变更 → 构造新状态副本 → 落盘（临时文件+rename）→ 成功后才原子替换内存可见状态并发 Ack → 失败则丢弃副本、不发布、不 Ack。禁止先改内存再补盘。掉电耐久性不承诺（已声明）。
node 侧关键顺序：accepted 落盘成功 → 发 task_ack；started 落盘成功 → 发 task_started → spawn；finished+result 落盘成功 → 发 task_result。
`data_dir` 单实例锁：**OS 文件锁（Unix flock，进程退出自动释放）**，不用 O_EXCL 锁文件（SIGKILL 后残留会永久阻塞重启，DoD 6/10 无法执行）；锁冲突则退出报错。Windows 侧随 P4② 服务化落地同语义实现（CreateFile 独占打开 + LockFileEx，句柄随进程退出由 OS 释放；lock_test.go 跨平台同一套契约断言）。

### 4.8 node 执行账本（internal/node/ledger）

`data/ledger.json`：`task_id → {task: 完整Task, state: accepted|started|finished, result?, result_acked}`（持久化完整任务输入，重启后无需 hub 重派）。
启动恢复：
- accepted（未 started）：检查任务 expire_at——已过期 → 不执行，写 finished（status=failed，error=expired）并回传；未过期 → 进入本地调度执行；
- started 未 finished：执行结果不确定 → 写 finished，status=`needs_review`，error=`node_restarted_midway`，回传；**禁止自动重新执行**；
- finished 且未 acked：重发 task_result 直至 result_ack。
result_acked 条目 24h 后清理。needs_review 是独立终态：查询可见、语义为「执行结果不确定，需人工核对」，协议与展示层不得折叠为 failed（防止调用方误自动重提）。
**产物隔离与保留**：产物目录按任务隔离（outputs/<task_id>/），进程结束后 runner 冻结产物清单（登记后新增文件不追认）；产物文件保留至 result_ack 后 24h；artifact_fetch 取回时 node 校验 SHA-256 与登记一致，不一致回 err(artifact_mismatch)。

### 4.9 核心数据结构 Schema（完整字段表，实现以此为准）

**双卡分离（codex 接入方评审修正）**：AgentCard（对外能力声明，远端调用方只看这个）与 ExecProfile（本机执行配置，永不离开本机）严格分离；node 注册时由 ExecProfile 生成 AgentCard 上报。

```go
type AgentKind string // 仅 "cli"

// AgentCard 对外能力声明（对外可见，对齐 A2A Agent Card 语义）
type AgentCard struct {
    Name           string   `json:"name"`              // node 内唯一
    Kind           AgentKind `json:"kind"`
    Description    string   `json:"description,omitempty"` // 技能/用途描述（调用方据此选 agent）
    InputMode      string   `json:"input_mode"`        // 固定 "prompt_text"（V0：一次性自然语言任务）
    OutputMode     string   `json:"output_mode"`       // 固定 "answer+artifacts"
    Interactive    bool     `json:"interactive"`       // V0 恒为 false：不支持澄清/审批/继续输入，任务必须可一次给完
    PermissionMode string   `json:"permission_mode"`   // 映射 agent 自带权限机制（如 codex sandbox 模式），调用方知情
    ProtocolVer    int      `json:"protocol_ver"`      // 适配器协议版本
    Exposed        bool     `json:"exposed"`           // 接入开关（主权在 node）：false=执行准入关闭（新任务 404 agent_not_exposed）；注册表保留一条审计记录（名称/下线时间，不含能力详情）
}

// ExecProfile 本机执行配置（永不离开本机）
type ExecProfile struct {
    Name        string   `json:"name"`            // 必填，node 内唯一（与 AgentCard.Name 一致）
    Kind        AgentKind `json:"kind"`           // 必填，仅 "cli"（与 AgentCard.Kind 一致）
    Command     string   `json:"command"`         // 必填，绝对路径
    Workdir     string   `json:"workdir"`         // 必填，绝对路径（进程 cwd）
    AgentArgs   []string `json:"agent_args"`      // 固定附加参数（如 codex --sandbox/-mode 权限旗标），任务不可改；拼接顺序：AgentArgs 在前，任务 Args 在后
    TimeoutSec  int      `json:"timeout_sec"`     // >0 秒
    AllowArgs   bool     `json:"allow_args"`       // 默认 false：任务带非空 args → policy_denied
    AllowEnv    []string `json:"allow_env"`        // 默认 []；"*" 全开；"PATH" 需显式列出
    ConfigHome  string   `json:"config_home"`     // agent 配置/凭证目录（HOME 指此，不指 workdir）；空则 data_dir/home
    Description string   `json:"description,omitempty"` // 生成 Card.Description 的源
    PermissionMode string `json:"permission_mode"` // 映射 agent 自带权限机制，生成 Card.PermissionMode
    Exposed     bool     `json:"exposed"`          // 接入开关（默认 true）：下线后新任务 404，已排队任务正常继续执行至终态；审计保留一条下线登记
}
```

**FinalAnswer 提取规则（V0 固定）**：FinalAnswer = 进程 stdout 全文（UTF-8 清洗，非法字节替换 U+FFFD）截断至 **512KiB**（编码后预算见下）。产物发现约定：**workdir/outputs/<task_id>/ 目录**——runner 在进程结束后扫描该目录（递归，跳过符号链接），每个文件登记为 Artifact（相对路径+SHA-256+大小）；目录不存在则 Artifacts 为空。结构化提取（按标记/JSON 分离答复与日志）留 V1 的 profile 机制。
**编码后预算（防 base64/JSON 转义爆帧）**：单产物原始 ≤ **640KiB**（base64 后 ≈853KiB）；FinalAnswer ≤512KiB；双 tail 各 ≤64KiB；Artifacts ≤8 条且仅存引用；以上合计经完整信封 JSON 序列化后必须 ≤1MiB，提交与回传两侧均按最终编码实测，超限截断并在 error 标记 `truncated`。
```

**权限模型（诚实声明）**：runner 的权限边界 = 进程启动权 + env 白名单 + ExecProfile 固定命令/参数；agent 自身能做什么由其自带权限机制管（AgentArgs 映射如 codex --sandbox read-only / claude --permission-mode）。Card.PermissionMode 把这个映射结果告知调用方。命令白名单不构成 agent 行为隔离，调用方以 PermissionMode 为准。

type TaskInput struct {
    Args  []string          `json:"args"`            // 追加到 command 后
    Stdin string            `json:"stdin,omitempty"` // UTF-8；写完关 stdin
    Env   map[string]string `json:"env,omitempty"`   // 键必须在 allow_env 名单内
}

type TaskResult struct {
    Status     string       `json:"status"`          // completed | failed | needs_review | cancelled
    ExitCode   int          `json:"exit_code"`       // 信号终止或未运行 = -1
    FinalAnswer string      `json:"final_answer"`    // 完整最终答复（agent 产出正文，≤512KiB，独立于日志 tail）
    StdoutTail string       `json:"stdout_tail"`     // 诊断用日志尾部 64KiB（非正式结果载体）
    StderrTail string       `json:"stderr_tail"`
    Artifacts  []Artifact   `json:"artifacts"`       // 产物引用（路径+哈希，node 本地保留，按需拉取）
    Error      string       `json:"error,omitempty"` // timeout / policy_denied / expired / user_cancelled / node_restarted_midway ...
    StartedAt  time.Time    `json:"started_at,omitempty"`
    FinishedAt time.Time    `json:"finished_at,omitempty"`
}

type Artifact struct {
    Path   string `json:"path"`   // 相对 workdir 的路径（outputs/ 约定目录下）；取回时由 node 解析为本地绝对路径
    Sha256 string `json:"sha256"`
    Size   int64  `json:"size"`
}

type Task struct {
    ID          string      `json:"id"`             // "t-"+16字节hex，hub 生成
    TraceID     string      `json:"trace_id"`
    TargetNode  string      `json:"target_node"`
    TargetAgent string      `json:"target_agent"`
    Input       TaskInput   `json:"input"`
    SubmittedBy string      `json:"submitted_by"`   // HTTP 提交=调用方标记；node 提交=hub 覆盖为该 node_id
    IdemKey     string      `json:"idem,omitempty"`
    GroupRef    string      `json:"group_ref,omitempty"` // 群引用（管理面扩展，SPEC 4.5 指纹口径）
    DependsOn   []string    `json:"depends_on,omitempty"` // 依赖的上游 idem_key 列表（P4.3/4.12，提交后不可变）
    Status      TaskStatus  `json:"status"`         // 见 4.3 状态机
    BlockedReason string    `json:"blocked_reason,omitempty"` // 依赖门控标注（P4.3/4.12，不进状态机）
    SupersededBy  string    `json:"superseded_by,omitempty"`  // 上游重跑审计标注（P4.3/4.12，不进状态机）
    CreatedAt   time.Time   `json:"created_at"`     // UTC RFC3339
    UpdatedAt   time.Time   `json:"updated_at"`
    ExpireAt    time.Time   `json:"expire_at"`      // CreatedAt+7d
    Result      *TaskResult `json:"result,omitempty"`
    Lease       *TaskLease  `json:"lease,omitempty"` // 任务租约（P4，见 4.11）：nil=未派发/旧版记录
}

type TaskLease struct {
    FenceEpoch        int64     `json:"fence_epoch"`                // 当前派发租约代次（每次派发/重派刷新；结果仲裁等值基准）
    Rejects           int       `json:"rejects,omitempty"`          // 累计 zombie_result_rejected 次数
    LastRejectedFence int64     `json:"last_rejected_fence,omitempty"` // 最近被拒结果携带的回带值
    LastAt            time.Time `json:"last_at,omitempty"`          // 最近拒收时刻（UTC）
}

type TaskSubmitRequest struct {
    TargetNode  string    `json:"target_node"`  // 必填
    TargetAgent string    `json:"target_agent"` // 必填
    Input       TaskInput `json:"input"`
    SubmittedBy string    `json:"submitted_by"` // 必填（人/agent 名）
    IdemKey     string    `json:"idem_key,omitempty"`
    GroupRef    string    `json:"group_ref,omitempty"` // 群引用（管理面扩展）
    DependsOn   []string  `json:"depends_on,omitempty"` // 依赖声明（P4.3/4.12）：上游 idem_key 列表
}
```

示例（提交与终态）：

```json
// POST /api/v1/tasks 请求体
{"target_node":"mac-mini","target_agent":"echo-demo","input":{"args":["hello","swarmmind"]},"submitted_by":"hana","idem_key":"demo-001"}
// 终态 Task.Result
{"status":"completed","exit_code":0,"final_answer":"hello swarmmind","stdout_tail":"hello swarmmind\n","stderr_tail":"","artifacts":[],"started_at":"2026-10-20T09:00:00Z","finished_at":"2026-10-20T09:00:00Z"}
```

### 4.11 任务租约（P4，2026-10-23；fencing token 防 zombie 写）

**问题**（PLAN.md 风险节 V1 项）：hub 判定 node 离线（重连 epoch+1）或任务重派后，旧 node 进程（zombie）完成任务并回写结果——过期结果被接受会污染状态机（重派场景下旧执行结果覆盖新派单的状态推进）。

**挂点（侵入最小选型）**：`Envelope.FenceEpoch`（`json:"fence_epoch,omitempty"`，4.1）。不复用 `SessionEpoch`——node 传输层对每帧强制盖章 SessionEpoch=当前会话代次（hub 会话层亦按它校验，承载过期值会被整帧丢弃），语义冲突不可承载；不改 Task 载荷 Schema 主体。task_offer / task_cancel 由 hub 携带，task_result 由 node 从账本原样回带；传输层（node conn.Send / membus）对 unknown 字段透传。

**契约**：
1. **授予**：每次派发（dispatch 事务内）捕获该 node 当前注册代次（4.4）作为租约快照，随 offer 帧下发并落库 `Task.Lease.FenceEpoch`——**重派（重新授予）即按当下注册代次重新落库：node 已换代（重连 epoch+1）后的重派即换新租约；同会话内的补推（未换代，如 offer 超时回退后原代次补推）捕获同值，租约等值不变**。重派写时复制保留 `Lease` 的拒收审计字段（Rejects/LastRejectedFence/LastAt 只随任务累计，不因换租约清零）。代次为 0（membus 等价链路未注册）不落库。
2. **回带**：node 入账时（`ledger Entry.FenceEpoch`）随条目持久化租约快照，task_result（含重连重发/重复 offer 重发等全部路径）原样回带；账本条目的租约只在 hub 显式（重）授予时变更——入账，或**重臂**（重复 task_offer=重派收口，携新代次，node `RearmFence` 对齐后重发；task_cancel=取消收口的对账取大者，见第 5 条）。重臂**落盘失败即不确认不回发**（hub 按 offer-ack 超时重推本帧重试）——带旧租约的确认与回执对新租约 hub 必拒，只会把任务推入永久滞留。重臂换新代次成功时同时解除该条目的 closed（被拒旧代次的停止重发标记随代次一并失效，新租约下的结果重发恢复）；重连重发等 node 自发路径永不改写；zombie 进程无当前会话，收不到授予帧即无法自臂。
3. **仲裁**：hub 结果仲裁在状态收敛前校验——回带 `fence_epoch` 与任务落库租约**等值**才进入收敛表；**缺省宽容**：回带缺失（0，旧版 node/旧账本/未携租约派发）按「缺省=当前租约」放行，向后兼容。**等值基准是任务落库租约而非 node 当前代次**：进程重启恢复（SPEC 4.8 needs_review 回传）、结果重发（4.5）都是「新会话持有者报旧租约执行结果」的合法路径，等值基准取任务租约使这些路径自然通过，同时精确排除「重派后旧派单回写」（唯一被门禁拦下的正是租约不符的回写）。**防御边界声明：缺省宽容是无条件降级通道——旧版 node（不回带 fence_epoch）不受租约门禁保护**，其回写按「缺省=当前租约」放行；本机制的防写边界仅覆盖升级后回带租约的 node，混部期 zombie 若为旧版进程，防线退化为会话层身份担保（4.4）。
4. **拒收**：不符 → `zombie_result_rejected`：任务维持原状态（不改 Status/UpdatedAt/Result，不动七态状态机），`Task.Lease` 记审计（Rejects++/LastRejectedFence/LastAt，随快照持久化、查询面可见），hub stderr 记日志（含 fence_epoch 与 current_lease），并回 err(result_conflict)（msg 注明 zombie_result_rejected，ErrInfo 结构化携带 `fence_epoch`=被拒回带值、`lease_epoch`=当时落库租约）——node **仅当被拒 fence 与账本条目当前 fence 等值才置 closed 停止重发**（拒收旧代次不得关闭已重臂条目，否则新租约结果首发丢失后周期/重启补传全被跳过，任务永久滞留；未携 fence 字段的旧版对端与普通终态冲突按既有规则无条件关闭），zombie 结果不再周期性重入仲裁。
5. **task_cancel 口径（P1-2 修订）**：取消帧自身**不需要门禁**——取消 ⇒ 任务 hub 侧终态 ⇒ 同 task_id 永不重派，故「旧取消命中新派单执行」在结构上不可能；重复取消由幂等 ack/取消墓碑收敛（node 侧既有语义）。帧携 **hub 当前任务落库租约（cancel_fence）**，node 侧取消回写（task_result(cancelled)）前对「账本条目租约 vs cancel_fence」**取大者重臂**后回带——重派已推进租约而 node 未及重臂的滞后（ack 丢后重派、重派 offer 送达失败留 submitted 等）经取消帧补授收敛；hub 对 cancelled 回执按 **cancel_fence ≥ 落库租约**宽校验接受（取消是管理面意图，宽于执行回执的等值）。经当前会话送达的取消帧 zombie 收不到，无法伪造新 cancel_fence；旧代次的 cancelled 回执（< 落库租约）仍被第 3 条门禁拒收。ack/started 类过程帧同理不设门：无结果载荷，无状态污染面。

**与 4.4 会话 epoch 的关系**：正交两层。会话 epoch 保证「帧只经当前会话进入」（传输层，送达方身份可信）；任务租约保证「回执对齐当前派发」（任务层，派发代次一致）。防御目标：同 node_id 被两个进程持有（部署事故/慢 zombie 抢回连接）时，旧进程在旧派单下产生的结果无法覆盖新派单状态。

**已知代价（声明）**：跨代次的执行若任务未重派（如 node 执行中掉线重连、hub 侧任务仍 running），其合法结果与 zombie 结果对 hub 不可区分，按本契约回带原租约**接受**（等值）——状态机安全由会话层身份担保；真正的残留风险窗口仅「同 node_id 双进程且旧进程抢回会话且任务未重派」，此时回执来自当前会话持有者、且 node 单账本保证每任务至多一个执行，污染面为诊断数据而非状态机。租约拒收后的滞留任务（如重派竞态失败留 running）由 7d expire 兜底，V2 自动回收以此为输入。

**MCP 同步检查**：本变更为协议内防写机制 + 审计字段，结果路径无新增/变更 REST 管理面能力，MCP 工具面零变更（任务查询工具返回体透传 `lease` 展示字段，不构成工具面变更）。

### 4.12 任务依赖（P4.3 第一子批，2026-10-23；FILE-COLLAB L2 依赖门控+调度）

**范围声明**：本子批只做依赖门控与调度；产物挂载（`workdir/inputs/<上游id>/` 纯复制 + SHA 校验）与跨机取回为 L2 第二子批，不在本节。

**寻址（FILE-COLLAB 终审口径）**：`depends_on` 使用 **idem_key**（跨重跑稳定）而非 task_id；声明后不可变（提交即定，无改边路径）。

**Schema（新字段全部 optional，七态状态机/收敛表/终态规则零改动）**：

| 字段 | 载体 | 语义 |
|---|---|---|
| `depends_on []string` | TaskSubmitRequest / Task | 依赖的上游任务 idem_key 列表；提交时准入校验后随任务落库 |
| `blocked_reason string` | Task | 依赖门控标注（**不进状态机**）：`blocked_by:<idem>`（等待中，含 idem 已随终态清理消失的兜底等待）/ `upstream_failed:<idem>`（上游 failed/needs_review/cancelled）；空=未阻塞。多依赖时取声明序首个，失败优先于等待（失败不可自愈需人工，等待可自行恢复） |
| `superseded_by string` | Task | 上游重跑审计标注（**不进状态机**）：曾因 `upstream_failed:<idem>` 被挡的下游任务，遇同 idem 新版本 completed 时记录该新任务 ID；任务保持 blocked 不自动放行，人工决定是否重派（「上游重跑自动解锁」已删除——FILE-COLLAB L2 终审；旧任务审计保留） |

**准入校验（提交校验链 idem 查重之后、体积预检之前预检；单写事务内按快照重检——上游在两检之间被清理即拒绝）**，违规 → 400 `invalid_dependency`（err 帧同码）：
1. 未知 idem：快照中无携带该 idem 的任务（不存在或已随终态清理）；
2. 自引用：`depends_on` 含自身 `idem_key`（未携 idem 的提交无从自引用）；
3. 重复依赖；
4. 空项/纯空白项（入库前 TrimSpace 归一）；
5. 深度超限：沿依赖图上溯最长链边数 > 4（链上至多 5 任务）；
6. 级联超限：上溯可达的去重上游任务总数 > 8；
7. 依赖环：边只指向已存在任务天然无环；防御 idem 跨终态清理期被复用构环——上溯链解析回自身 idem 即拒绝。

**幂等指纹口径（SPEC 4.5 补充）**：声明 `depends_on` 时提交输入取**生效形态**——stdin 尾部附加依赖清单（上游 idem 列表；幂等判据 `[swarmmind:depends_on]` 标记行，不重复追加），清单为提交请求的确定性函数；请求指纹（ContentHash）与落库任务指纹（taskContentHash）过同一变换，重试 dup 判定不受清单注入影响。未声明依赖时逐字节兼容旧口径。依赖列表本身参与指纹（同键换依赖=不同内容=409）。

**门控与调度**：
- **blocked 不进状态机**：依赖未满足的任务保持 `submitted`，不投递；七态状态机、迟到事件收敛表、终态规则零改动。出队 eligible 增补一条：`BlockedReason` 非空即跳过（与 P1.5 workdir 互斥门控同机制复用）。
- **blocked 任务留信箱占配额**：入箱/计量口径与普通 submitted 任务一致；信箱满拒新提交的既有语义不变（SPEC 4.6）。
- **事件触发复评**：上游任务到终态的仲裁事务处——result.go 首次落终态（task_result 接受分支）、CancelTask 置 cancelled、Sweep 三处置终态（expired/排队超时/offer 超时过期）——**同一单写事务内**复评其 idem 的全部下游任务（依赖全 completed → 解除阻塞，发布后按目标节点 dispatch；任一上游 failed/needs_review/cancelled → 保持 blocked 并更新标注）。单事务原子生效，无「上游已 completed 而下游仍 blocked」的事件丢失窗口。
- **Sweep 兜底**：每秒扫描（`sweepInterval` tick）对全部 blocked 的 submitted 任务全量复评，防御未知路径的事件丢失；解除阻塞随本轮 dispatch。
- **排队超时豁免**：blocked 任务不适用 P1.5 `workdir_busy_timeout`（等待的是上游终态而非 workdir，`workdirBusyFor` 不因门控成立）；7d expire 兜底照常（SPEC 4.3/4.6）。
- **放行单向**：已解除阻塞（含未及投递）的任务不因上游终态清理被重新挡回——依赖均为终态不再变化，复评只作用于 `submitted` 且仍 blocked 的任务。
- **hub 重启恢复免费**：依赖门控全部字段随任务快照持久化（Clone/JSON 覆盖），重启后 blocked 状态原样恢复，Sweep 兜底继续复评，无独立恢复流程。

**依赖解析口径**：依赖 idem 解析当前快照——有 completed 任务即满足（任一版本；多活同键（group_ref 装饰边，SPEC 4.5）宽松口径取最早 completed）；否则有非终态任务为等待中；否则（仅 failed/needs_review/cancelled）为上游失败；快照无该 idem 保持等待（`blocked_by:<idem>`，7d expire 兜底）。

**本子批的产物过渡**：上游 completed 但产物不挂载（挂载为 L2 第二子批）；本子批放行投递时任务 stdin 已附上游 idem 依赖清单，供 agent 经管理面/MCP（swarmmind_artifact 按上游 task_id）自取。

**边界声明**：依赖门控仅 hub 提交链生效（HTTP 管理面 + node 会话 task_submit 帧）；node 本地直执行链（node localapi POST /tasks，origin=local）不经过 hub 状态机，不支持依赖声明。node 收到的 task_offer 载荷可能携带 `depends_on`/`blocked_reason` 字段（Task 全量透传），node 侧不感知、不入状态机。

**MCP 同步**：`swarmmind_submit`（hub 模式）新增 `depends_on` 数组参数透传（见六.六规则 7）；REST 提交请求体透传（handleSubmit 平铺解码 TaskSubmitRequest）、任务查询返回体携带三字段——均为 optional 字段，既有 API 兼容性不变。

## 五、node 配置（configs/node.example.yaml）

```yaml
node_id: mac-mini
hub_url: ws://127.0.0.1:8443/ws/node   # 开发模式（insecure_loopback=true）；部署时改 wss:// + 证书指纹
register_token: token-for-mac-mini      # 每 node 独立，与 node_id 绑定
hub_pubkey_fp: ""                       # SPKI SHA-256；空则 TOFU 记录首连
concurrency: 2
data_dir: data
base_env:                               # 执行环境基线：不继承 daemon 进程环境
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
  LANG: en_US.UTF-8
agents:
  - name: echo-demo
    kind: cli
    command: /bin/echo                  # 绝对路径；任务不可更改命令与 workdir
    workdir: /tmp
    config_home: ""                     # 空 = data_dir/home；agent 配置/凭证目录（HOME 指此，与 workdir 分离）
    agent_args: []                      # 固定附加参数（如 codex: [--sandbox, read-only]），任务不可改
    timeout_sec: 30
    allow_args: true                    # 授权该命令的全部参数能力（解释器类=通用执行，文档明示）；false 时非空 args → policy_denied
    allow_env: ["TMPDIR"]               # 允许任务覆盖的 env 键；默认 []；"PATH" 需显式列出
    description: 回声测试                 # 生成 AgentCard.Description 的源
    permission_mode: no-shell           # 映射 agent 自带权限机制的说明（对外 Card 可见）
  - name: sleeper
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    config_home: ""
    agent_args: []
    timeout_sec: 1
    allow_args: true
    allow_env: []
    description: 超时测试
    permission_mode: no-shell
```

**hub 配置（configs/hub.example.yaml）**：

```yaml
node_listen: 127.0.0.1:8443       # node 入口；insecure_loopback=true 时应为 ws:// 且仅回环
api_listen: 127.0.0.1:8444        # 管理面，强制回环
tls:
  insecure_loopback: true         # V0 开发模式：ws:// + 回环；部署时改 false + 真实证书
nodes:                            # 每 node 独立 token，绑定 node_id
  mac-mini: token-for-mac-mini
  win-box: token-for-win-box
data_dir: data
```

insecure_loopback=true 时 node 侧 hub_url 应为 `ws://127.0.0.1:8443/ws/node`；部署模式为 `wss://` + 证书指纹。

**执行环境规则**：runner 使用 base_env（+任务在 allow_env 白名单内的覆盖）构造进程环境，不继承 daemon 自身环境（防凭证泄漏进子进程）。**runner 额外注入三个系统变量（不受 base_env/allow_env 约束、任务 env 不可覆盖）：`SWARM_TASK_ID`、`SWARM_OUTPUTS_DIR`（=workdir/outputs/<task_id> 绝对路径，agent 产物约定输出位置）、`SWARM_TRACE_ID`**。**HOME 由 runner 固定设为 ExecProfile.ConfigHome**（agent 配置/凭证目录，默认 data_dir/home，与 workdir 分离——不破坏 agent 的配置查找），cwd=workdir。越界（env 键不在名单）任务直接 failed，error=`policy_denied`，不执行。
**能力边界声明**（写入文档）：CLI agent 的行为边界由 command 本身性质与其自带权限机制决定；stdin/env 是输入面；allow_args 授权解释器类命令等同全权授权，选用即知情；PermissionMode 把 agent 自带权限映射告知调用方。

任务执行 context 独立于会话：唯一超时源是 timeout_sec；会话断开不杀已 spawn 的任务（结果经账本在重连后补传）。**进程组治理（V0 Unix / P4② Windows）**：Unix 下 spawn 前 setpgid 置独立进程组，超时/取消时 kill(-pgid) 整组终止，WaitDelay=5s 兑底；Windows 下子进程 CREATE_SUSPENDED 挂起启动→入 Job Object（KILL_ON_JOB_CLOSE）→ResumeThread（执行任何指令前必已受治理），超时/取消 TerminateJobObject 整组终止，Job 装配/入组失败拒绝执行（不静默降级），node 自身另入兜底 Job（进程异常终止时 OS 兜底清理遗留子进程）。

## 六、交互面与管理 API

**交互形态（V0）**：人工操作走 `swarmmindctl` CLI（submit/query/agents/nodes/review 五命令，薄包装管理面 API，与 hub/node 同仓库同二进制集）；其中 review 在 V0 仅展示待核对上下文与处理指引（核对记录的持久化接口属 P3 增补，见 PLAN 生命周期）；agent/脚本直接调 HTTP API。**Web 控制台已随 P3 交付（生命周期 PLAN 第七节）：登录制 + 派任务 + 任务管理 + needs_review 核对；hub 控制台鉴权（访问令牌登录）已实现，安全模型为「数据面回环 + 控制台登录制」，契约见六.五。**

### hub HTTP 管理面（api_listen，绑回环；P3 起登录制会话鉴权，见六.五）

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | /api/v1/tasks | `{target_node, target_agent, input, submitted_by, idem_key?, depends_on?}` | 202 `{task_id, trace_id}`；幂等命中 200 同 task_id + `dup:true`；同键不同内容 409；依赖声明不合法 400 `invalid_dependency`（4.12）；信箱满 503 `mailbox_full`；目标不存在 404；超限 400 |
| GET | /api/v1/tasks/{id} | - | 200 Task（含状态/结果/needs_review） |
| GET | /api/v1/agents | - | `{agents:[{card, node_id, online}]}`；card=AgentCard，**绝不包含 ExecProfile 字段**（执行配置不出本机） |
| GET | /api/v1/nodes | - | `{nodes:[{node_id, online, epoch, inflight}]}` |
| GET | /api/v1/tasks/{id}/artifacts/{index} | - | 200 产物内容（hub 经 artifact_fetch/artifact_data 中继到执行 node 取回；超时 30s → 504） |
| POST | /api/v1/tasks/{id}/cancel | `{reason?}` | 202 取消已受理（hub 置 cancelled 终态 + 下发 task_cancel，node 结果异步回传）；幂等命中 200；已终态（completed/failed/needs_review）409 `not_cancellable`；不存在 404。管理面能力，**不设 MCP 工具**（取消保留给人工管理员，agent 不可互删任务） |

**无 HTTP 轮询回退**：离线路径=信箱+重连补推，别无分叉（避免绕过账本/Ack 的第二条路径）。旧 `/tasks/fetch` 概念废除。
任务提交校验链（顺序与 §4.5 一致）：**鉴权/目标存在且 exposed=true（未接入 → 404 `agent_not_exposed`）→ idem 查重（命中即返回，不做配额检查）→ 依赖准入校验（P4.3/4.12：未知 idem/自引用/重复/超深度/超级联/环 → 400 `invalid_dependency`）→ 序列化体积实测预检 → 信箱配额与字节预算 → 持久化（单写事务内全部重检）→ submit_ack**。全链失败则无 Ack。

**node 会话边界锁（方案 v2 D9/D10，hub 兜底）**：node 会话的 task_submit 在一切校验前强制 `target == env.From`（身份由会话层覆盖，违规拒绝）；task_query 按 `Task.TargetNode == env.From` 收敛，他节点任务拒绝。hub 核心层双保险保证 node 无全网派发与全网视图。

### node 本地 API（本机派发/查询入口，V0）

node 侧同开回环 API（默认 `127.0.0.1:8445`，仅本机进程可用）：`POST /api/v1/tasks` 与 `GET /api/v1/tasks/{id}`，语义与 hub 管理面一致，内部经 WSS 会话转发为 task_submit/task_query 帧。任意本机进程（脚本/agent）都能对本机派单与查询，但**只限本机**：`target_node` 非本机 node_id 即 400 拒绝（不发帧、不留提交日志），hub 侧 D9/D10 双保险兜底——node 无全网派发与全网视图，跨机协作统一经 hub（MCP/A2A）发起。**已知限制**：提交方若非 node 本机而是 hub 侧回环调用方，取结果靠 hub 管理面轮询；node 提交方的终态经 result_event 推送。

**统筹凭证与钦定·自动接线（docs/archive/PLAN-node-standalone.md §3.2.1 + 任务 t20261023-62a2）**：`POST /api/v1/local/prime/rotate`（轮换 prime_token：重生成→原子写盘→即时重载，旧凭证下一请求失效）与 `POST /api/v1/local/prime/declare`（网页钦定统筹载体：校验 agent 存在→钦定即换凭证→yaml round-trip 写 node.yaml `prime_agent`→即时生效）均仅 local_token 可调（主人人工操作，六.六豁免③④）。**钦定即自动接线**：declare/rotate 成功后，node 自动把 swarmmind MCP 配置片段（含最新 prime_token）合并写入被钦定 agent 影子 home（`ExecProfile.ConfigHome`，空则 `data_dir/home`，与 runner HOME 同口径）内的宿主配置文件——布局探测只认 zcode 一种（`<home>/.zcode/cli/setting.json`，注意文件名单数）：存在且为合法 JSON 才命中（合法 = 全文恰一个 JSON 对象：`null`、尾部多余内容均不命中）；命中即合并顶层 `mcpServers.swarmmind`（幂等覆盖同键、不动其他键），写前备份 `<file>.bak-prime`（直接覆盖旧备份不堆积），临时文件+rename 原子写、0600（文件内含凭证）。应答带 `wired: true|false` 与 `wired_target`（命中时的绝对路径），declare/rotate 两口径一致、应答均带 `mcp_config` 片段（降级时主人拿得到手贴内容）；**降级口径**：文件/目录不存在、JSON 非法（含 `null` 与尾部多余内容）、顶层或 mcpServers 非对象、写失败、或 swarmmind-mcp 探测未命中（command 为占位符——占位配置落盘无用，一律不写）→ `wired:false`，绝不硬写、绝不创建宿主目录，应答保留 `mcp_config` 片段与手动指引由主人粘贴（宁可手贴，不猜布局乱写）。rotate 同步刷新影子 home 内的 `SWARM_NODE_PRIME_TOKEN`（否则旧 token 残留，统筹载体下一请求 401）；撤销统筹不清理已写入配置（token 已即时失效，残留无害）。全部接线动作发生在 declare/rotate 这个主人人工操作的事务内，prime_token 仍不经 agent 面分发（豁免③口径不变）。

## 六.五、P3 增补契约（Web 控制台，按 INTERACTION.md 分级清单落地）

### 鉴权（控制台登录制）

- hub 配置新增 `admin_token`；`POST /api/v1/login {"token":"..."}` → 200 + `Set-Cookie: swarmmind_session=...`（HttpOnly；V0 会话内存态，hub 重启需重登）
- 除 `/api/v1/login` 外全部管理面端点要求有效会话，否则 401
- 中心端「控制能力」实现澄清：hub 不内嵌执行体；控制操作（全网巡检等）= 管理员从中心端向任意 node 的巡检类 agent 派发正式任务（走现有全链），hub 保持登记处与控制面，服务执行始终在 node 侧

### 核对记录持久化

- `POST /api/v1/tasks/{id}/review {"action":"ack_review"|"mark_failed","note":"..."}` → 200
- 仅对 needs_review 任务有效；**不改动协议终态**（保持 needs_review），在任务上追加 `review {by, at, action, note}` 人工记录（入快照）；GET /tasks/{id} 返回含 review；重复提交覆盖旧记录
- swarmmindctl review 展示该记录；node 账本不感知 review（协议层无帧，纯管理面）

### 控制操作批次

- `POST /api/v1/control-operations {"agent":"inspector","targets":["n1","n2","n3"],"input":{...}}` → 202 `{"batch_id","items":[{"target","task_id"}|{"target","skipped":"offline"}]}`
- 实现即批量 Submit：快照时点逐目标走现有校验链，离线/未暴露目标标 skipped 不阻塞其余；批次记录入快照；GET /api/v1/control-operations/{batch_id} 查询逐项状态

### 发现与目录

- `GET /api/v1/directory`：全网 exposed=true 的 AgentCard 列表（不含 ExecProfile）——中心端独有，node 不代理此接口（node 无全网视图）；跨机能力发现经 hub（MCP swarmmind_agents / A2A）发起
- node 本地 API 无目录代理（hub_api_url/hub_api_token 已废除，旧配置字段忽略）；node `GET /api/v1/agents` 仅返回本机能力列表，全网目录唯 hub 可查

### 提交身份

- 控制台提交：submitted_by=admin（会话固定）；node 帧提交：submitted_by=node_id（hub 按连接身份覆盖）
- submitted_by 仅作显示与审计标签，鉴权依据是会话/令牌本身

### A2A 兼容端点（P3 收尾，2026-10-22）

- `GET /.well-known/agent.json`（免鉴权，A2A 发现惯例）：AgentCard（对齐 A2A v0.2.x），skills 由全网 exposed 目录聚合（id=`node_id/agent`）；只含能力元数据，ExecProfile/令牌/工作区互斥键一概不出
- `POST /a2a/rpc`（Bearer PAT `smsw_` 或会话 cookie，与现有同一鉴权）：JSON-RPC 2.0 手写分发（零新依赖）；`message/send` 等价 POST /api/v1/tasks（submitted_by=a2a，messageId 映射幂等键 `a2a:<id>`）、`tasks/get` 等价 GET /tasks/{id}；未知方法 -32601、任务不存在 -32001。外部协议兼容层本身即 agent 调用面（与 MCP 同位），故不另设六.六工具行
- 状态映射（七态→A2A TaskState）：submitted/offered/acknowledged→submitted、running→working、completed→completed、failed→failed、cancelled→canceled（A2A 官方单 L 拼法）、needs_review→input-required（原始状态保留于 metadata.swarmmind_status）；已知偏差清单见 internal/hub/api/a2a.go 头注

## 六.六、MCP 服务（agent 统一入口，2026-10-21 立）

> 定位：MCP 与 REST 管理面、双端控制台同为**一等公民接口面**。此后所有面向管理面的新功能，
> 必须同步设计并交付对应的 MCP 工具（见本节「同步开发规则」）——禁止只写 HTTP 不管 MCP。

### 实现形态

- `cmd/swarmmind-mcp`：MCP stdio 服务，newline-delimited JSON-RPC 2.0 手写实现，
  **零第三方依赖**（对齐 SPEC 三依赖白名单哲学）；新进程、不在 hub/node 内嵌。
- 配置仅环境变量：`SWARM_HUB_URL`（管理面地址）、`SWARM_HUB_TOKEN`
  （`smsw_` PAT 或 admin_token）；不读配置文件（MCP 客户端 env 注入为准）。
- 鉴权：全部调用走 `Authorization: Bearer <token>`，与 REST 同一令牌体系（六.五 PAT）。

### 工具面（与 REST 端点映射）

| MCP 工具 | REST 映射 | 说明 |
|---|---|---|
| swarmmind_nodes | GET /api/v1/nodes | 在线节点与负载 |
| swarmmind_agents | GET /api/v1/directory | 全网能力目录（可调用面） |
| swarmmind_submit | POST /api/v1/tasks | 派任务（submitted_by=mcp；支持 idem_key/trace_id/depends_on 透传，幂等重派、任期追踪与任务依赖 P4.3） |
| swarmmind_status | GET /api/v1/tasks/{id} | 查状态与结果（不等待） |
| swarmmind_wait | （轮询 status） | 等到终态；needs_review 为终态并明确提示需人工核对 |
| swarmmind_artifact | GET /api/v1/tasks/{id}/artifacts/{i} | 取产物（文本回读，二进制提示走控制台） |
| swarmmind_tasks | GET /api/v1/tasks | 任务列表：正在跑/最近完成/失败一屏速览，支持按状态/节点/agent 筛选（P4⑤；REST V0 全量直出不解析 query、不分页，筛选与条数截断在 MCP 侧本地完成、请求恒裸路径；每行短码+状态人话+节点/agent+提交时间+耗时或错误码，行尾附全号供 status/wait/artifact 寻址；hub 视角，node 模式不设） |
| swarmmind_decide | 外部 StartLux /v1/decisions（bamboonode/MLX 本地） | 分配决策查询：云端 L0 → 规则兜底 L2 自动降级，响应标 used_level（P4.1） |
| swarmmind_group_create | POST /api/v1/groups | 建协作群（目标+成员；返回 g- 群 ID，创建即 discussing；GROUP-RFC 六 MVP，P4/V1） |
| swarmmind_group_list | GET /api/v1/groups | 看全部协作群（目标/状态/成员/发言预算） |
| swarmmind_group_say | POST /api/v1/groups/{id}/messages | 群里发言（kind 缺省 user；agent 发言计入预算，用尽报 budget_exhausted；消息体 8KiB 上限） |
| swarmmind_group_messages | GET /api/v1/groups/{id}/messages?since | 拉群消息（since 增量补齐，seq 单调锚点；群消息滚动保留最新 200 条，响应 `pruned_before_seq` 标注已裁区间上界） |
| swarmmind_group_close | POST /api/v1/groups/{id}/close | 结束群（concluded 终态不可回退，重复关闭幂等；by 可选署名审计，缺省 mcp） |

### 语义约定

- `wait` 是 agent 派单的主路径：轮询至终态（completed/failed/needs_review），
  默认 300s 上限 1800s、间隔可配；超时返回当前状态而非报错（agent 可再等或转人工）。
- `needs_review` 是合法终态：MCP 结果必须明确「结果不确定，需人工核对」，
  禁止 agent 基于未核对结果下结论（对齐 4.8 语义）。
- 产物仅回读文本类（无 NUL 字节）；二进制提示走控制台取回。
- 错误一律包装为人话文本（isError=true），不向 agent 泄漏内部堆栈。

### 同步开发规则（强制）

1. **新功能入库检查单新增一条**：面向管理面的新 REST 能力，同一变更内必须：
   ① SPEC 本节表格补一行映射；② cmd/swarmmind-mcp 增补对应工具（含人话描述）；
   ③ MCP 层至少一条实测或单测。
2. 纯内部实现（调度/存储/协议内部）不适用本规则。
3. **人工管理专属豁免（2026-10-22）**：以下管理面端点不设 MCP 工具——
   ① `POST /api/v1/tasks/{id}/cancel`（任务取消）：取消是管理面的人工干预能力，暴露给 agent 会引入互删任务风险；agent 侧只能等待终态（wait 对 cancelled 即时返回并提示已取消）；
   ② `POST /api/v1/nodes/enroll-bundle`（节点接入部署包生成）：端点为管理端人工装机操作专属——生成的部署包内嵌节点 register_token 凭据，agent 无权获取凭据，部署包亦不经 agent 面分发，只对管理端会话开放。
   ③ `POST /api/v1/local/prime/declare`（node 网页钦定统筹载体，2026-10-22）：本机主人人工操作专属（仅 local_token 可调）——钦定即凭据授予（应答带新 prime_token 与 MCP 连接片段），暴露给 agent 面等于 agent 可自行换发统筹凭证，属提权通道；授权交接由本机主人在 node 控制台完成。t20261023-62a2 自动接线补充口径：钦定/轮换后 node 合并写入被钦定 agent 影子 home 内的宿主 MCP 配置，动作发生在 declare/rotate 这个主人人工操作的事务内、走主人授权，不构成 agent 面暴露（豁免结论不变）。
   ④ node 本机主人管理端点（2026-10-22 功能闭环修复轮补豁免）：`PUT /api/v1/local/config`、`POST /api/v1/local/agents/reload`、`POST /api/v1/local/restart`、`POST /api/v1/local/prime/rotate`、`POST /api/v1/local/setup/parse|apply|disconnect`——口径同 declare：本机主人专属管理操作（写 agents 表=执行权、整表落盘/重启/断开切形态、轮换交接统筹凭证），全部仅 local_token 可调，经 agent 面暴露即提权/破坏通道，正式豁免 MCP 映射；node MCP 六工具（本机 nodes/agents/submit/status/wait/artifact）已覆盖 agent 侧全部合法能力。
   ⑤ `GET /api/v1/nodes/enroll-info`（节点接入入口地址查询，2026-10-23 第六轮修复 2）：接入向导人工装机流程的配套元数据端点（与豁免 ② 同一人工流程）——返回 hub 自身节点接入入口地址（node_listen 推导，无凭据），供控制台向导预填 hub_url；agent 面无使用场景，豁免 MCP 映射。
   ⑥ `GET /api/v1/prime`、`PUT /api/v1/prime`（hub 网页指定统筹载体，2026-10-23）：管理员管理面操作（hub 控制台指定/撤销全网唯一统筹载体），统筹智能体自身无需读写该状态，豁免 MCP 映射。
   ⑦ `/api/v1/notify/*` 全组（通知渠道管理，2026-10-24 P4③ 控制台化升级）：人工专属管理面配置（凭据授予类）——渠道配置体内含明文凭证（飞书 webhook 地址、SMTP 口令、自定义头 token），agent 读写该面即等于可自行获取/篡改主人的通知出口与凭据（改渠道=把任务结果重定向到任意地址，属信息外带通道）；通知是「系统主动找人」的人工动线，agent 对通知配置无合法使用场景（agent 感知任务结果走 swarmmind_wait，与通知解耦）。管理台「设置 → 通知」为唯一配置入口；hub.yaml 旧 notify 段仅作一次性兼容导入。
   除本条显式豁免外，新管理面 REST 能力仍须同步 MCP 工具。
4. 工具命名固定前缀 `swarmmind_`；描述面向 agent 写「能做什么」，不写实现细节。
5. MCP 破坏性变更需在 SPEC 本节记录版本（protocolVersion 跟随 MCP 规范日期）。
6. **P4 任务租约同步检查声明（2026-10-23）**：任务租约（4.11）为协议内防写机制（Envelope fence_epoch 门禁 + Task.Lease 审计），结果路径无新增/变更 REST 管理面能力，MCP 工具面零变更——豁免映射表补行；任务查询工具返回体透传新增 `lease` 展示字段，不构成工具面变更。
7. **P4.3 任务依赖同步声明（2026-10-23，第一子批）**：任务依赖（4.12）为既有 REST 能力的参数扩展——`swarmmind_submit`（hub 模式）新增 `depends_on` 数组参数透传（上游 idem_key 列表；node 本机直执行模式不经过 hub 状态机，不支持该参数），本表 `swarmmind_submit` 行已同步；`swarmmind_status`/`swarmmind_wait` 返回体透传 `depends_on`/`blocked_reason`/`superseded_by` 展示字段（含 blocked/upstream_failed/superseded 的人话提示），不构成新工具。依赖声明不合法经 submit 工具以 `invalid_dependency` 人话文本返回。

## 七、验证标准（DoD 十一条）

1. `go build ./...` 过，go.sum 完整（`-mod=readonly` 实测）
2. `go test ./...` 绿，单测至少覆盖：信封编解码、状态机转移表（含 4.3 收敛表逐行）、信箱配额/释放、投递窗口计数、账本三态恢复、runner 超时/进程组整组终止/stdin/env 基线、白名单拒绝、idem 去重与冲突
3. 回环 demo：hub+node → HTTP 提交 echo → submit_ack → offer/ack/started/result/result_ack 全链 → 5s 内 completed 且 FinalAnswer 正确
4. 超时：sleeper 任务 failed、**进程组整组终止**（子进程组无幸存）、error 含 timeout
5. 离线信箱：node 断开 → 提交入信箱 → node 重连补推 → 完成
6. 重连韧性：kill node → 重启 → 退避重连 → epoch 递增；旧连接残余帧被 hub 忽略（单测注入）
7. hub 重启：状态恢复（能力表/任务/epoch/信箱），node 自动重连
8. 幂等：同 idem_key 两次提交同 task_id；同键不同内容 409；重复 offer 只执行一次
9. 白名单：allow_env 外覆盖 → failed + policy_denied；PATH 未列入时覆盖 PATH 同样拒绝
10. needs_review：任务 started 后 kill node → 重启 → 该任务终态为 needs_review（非 failed、不自动重跑），结果回传 hub 可查
11. **真实异构闭环（产品验收，双机）**：两台真实机器各跑 node，各接至少一种真实 agent（如 codex + claude code），A 机经 hub（MCP/A2A）发现 B 机 exposed agent 并派单 → B 机执行 → FinalAnswer 与产物经 hub 取回，覆盖断线恢复与权限拒绝各一次

demo 脚本：bash、`set -euo pipefail`、trap 清理、mktemp 隔离（hub/node 各自独立 data_dir）、每进程记录 PID、用有上限的状态轮询替代固定 sleep、JSON 解析用 python3；无法注入网络黑洞的项（如 6）用进程 kill 模拟并在脚本注释声明局限；超时验收检查直接子进程消失，不能只看错误字符串。

## 八、实现顺序（工程路线，先协议后网络）

1. protocol 包：Schema + 状态机转移表 + 常量，纯函数可单测
2. store / ledger：持久化事务，预设故障注入点（写盘前/rename 后/发布前/Ack 前）
3. **内存 transport 版** hub 任务服务与 node 调度：不接 WebSocket，单测跑通全链（提交→投递→执行→回传→去重）
4. runner：CLI adapter（超时/stdin/env 基线/白名单）
5. WSS 会话：注册、epoch、心跳、重连退避
6. 投递/确认/重连对账接通真实会话
7. HTTP API + 配置 + demo 脚本
8. 故障注入验证与容量实测

原则：先用内存 transport 验证任务协议，再接 WebSocket——文件、网络、状态竞争混在一起时无法判断故障归属（尽调建议采纳）。

## 九、已知陷阱（写代码前必读）

- coder/websocket：读循环每连接一个；写支持并发但 V0 统一走单写队列（理由：应用层排序、控制帧优先、背压计量），队列满暂停出队不断开
- `http.Server.Shutdown` 不管 WebSocket：hub 关闭先遍历会话发 Close 帧再等退出
- `exec.CommandContext` 只杀直接子进程：`WaitDelay=5s` 防管道卡死；杀后确认 ProcessState 非 nil 再取 ExitCode
- 时间一律 UTC；JSON tag 严格按第四节
- 持久化=先写后见+临时文件 rename+单写串行化+写败不发布不 Ack
- 依赖例外清单：yaml.v3、coder/websocket；`go mod tidy` 提交 go.sum
- 工人不 commit；最终输出改动文件清单 + DoD 十一条逐条结果（每条附验证证据）
