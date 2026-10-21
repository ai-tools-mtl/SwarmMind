# SwarmMind SPEC — V0（v3.0 FINAL）

> 上位文档：PLAN.md v3.0 FINAL。本文是 V0 的唯一执行真相源。
> 版本轨迹：v2 契约闭合 → v2.1 Schema/产物/权限升级（内容升级时未滚动版本号，终审修正）→ v3.0 FINAL：终审修订——产物拉取协议、FinalAnswer 提取规则、版本号统一、校验链顺序、60s 断开规则、ExecProfile 字段补齐、DoD 十一条。

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
    TraceID      string          `json:"trace_id,omitempty"`
    Payload      json.RawMessage `json:"payload"`
    Err          *ErrInfo        `json:"err,omitempty"`
    Retryable    bool            `json:"retryable,omitempty"`
    ExpireAt     time.Time       `json:"expire_at,omitempty"`
}
type ErrInfo struct { Code string `json:"code"`; Msg string `json:"msg"` }
```

### 4.2 消息类型

| type | 方向 | payload | 语义 |
|---|---|---|---|
| register | node→hub | `{node_id, register_token, agents[]}` | 连接后 5s 内首帧，否则 hub 断开 |
| register_ack | hub→node | `{ok, epoch, reason?}` | 成功携带分配的 epoch；拒绝则断开（错误码 auth_failed） |
| ping / pong | 双向 | 空 | 应用层心跳 |
| card_update | node→hub | `{agents[]}` | 能力表全量更新 |
| task_submit | node→hub | TaskSubmitRequest | node 间派单（hub 鉴权后走同 HTTP 等价流程） |
| submit_ack | hub→提交方 | `{task_id, trace_id, dup}` | 提交已持久化（dup=true 表示幂等命中） |
| task_offer | hub→node | 完整 Task | 推任务 |
| task_ack | node→hub | `{task_id}` | 任务已入账本（accepted 持久化成功后发） |
| task_started | node→hub | `{task_id}` | 执行已开始（started 持久化成功后、spawn 前发） |
| task_result | node→hub | `{task_id, status, result}` | 终态回传（status ∈ completed/failed/needs_review/cancelled；含 FinalAnswer 与产物引用） |
| result_ack | hub→node | `{task_id}` | 终态已持久化 |
| result_event | hub→提交方（node 会话时） | `{task_id, status, result}` | 终态推送给提交方，闭合「调用方拿回结果」；HTTP 提交方靠 GET /tasks/{id} 轮询。已知限制：提交方离线错过即弃，靠主动查询补 |
| task_query | 提交方→hub | `{task_id}` | 查询任务状态（result_event 离线错过的补查路径） |
| task_status | hub→提交方 | Task | 查询响应（完整 Task 可能接近 1MiB 上限，超大任务以摘要+artifact 引用返回） |
| artifact_fetch | 提交方→hub | `{task_id, index}` | 请求取回第 index 个产物（hub 中继到执行 node） |
| artifact_data | 执行 node→请求方（经 hub 中继） | `{task_id, index, meta, data_base64}` | 产物内容（单文件原始 ≤640KiB，base64 后随帧 ≤1MiB；更大产物 V1 分块） |
| task_cancel | hub→node | `{task_id, reason?}` | 管理面取消（管理面 POST /tasks/{id}/cancel 触发）：node 整组终止在途执行，账本落 finished(cancelled/user_cancelled) 后回 task_result(cancelled)；至少一次+幂等，重复 cancel 安全 |
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

### 4.5 可靠性契约（三段持久化确认 + 幂等）

| 阶段 | 确认含义 | 未收到确认时 |
|---|---|---|
| submit → submit_ack | hub 已持久化（先写后见，见 4.7） | 提交方携同一 idem_key 重试 |
| offer → task_ack | node 账本 accepted 已持久化 | hub 30s 后回退 submitted 重推 |
| task_result → result_ack | hub 终态已持久化 | node 账本 finished 未 acked，重连后重发 |

幂等规则：idem_key 作用域为 hub 全局，与任务同生命周期（过期/清理同步删除）；同键同内容 → 返回原 task_id + dup=true；同键不同内容 → 409 conflict。**提交流程顺序固定：鉴权 → idem 查重（命中即返回，不做配额检查）→ 容量/字节预检 → 持久化（单写事务内全部重检）→ Ack**。保证同键重试在信箱满时仍能取回原 task_id。
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
`data_dir` 单实例锁：**OS 文件锁（Unix flock，进程退出自动释放）**，不用 O_EXCL 锁文件（SIGKILL 后残留会永久阻塞重启，DoD 6/10 无法执行）；锁冲突则退出报错。Windows 跨平台锁 V1 服务化时补。

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
    Status      TaskStatus  `json:"status"`         // 见 4.3 状态机
    CreatedAt   time.Time   `json:"created_at"`     // UTC RFC3339
    UpdatedAt   time.Time   `json:"updated_at"`
    ExpireAt    time.Time   `json:"expire_at"`      // CreatedAt+7d
    Result      *TaskResult `json:"result,omitempty"`
}

type TaskSubmitRequest struct {
    TargetNode  string    `json:"target_node"`  // 必填
    TargetAgent string    `json:"target_agent"` // 必填
    Input       TaskInput `json:"input"`
    SubmittedBy string    `json:"submitted_by"` // 必填（人/agent 名）
    IdemKey     string    `json:"idem_key,omitempty"`
}
```

示例（提交与终态）：

```json
// POST /api/v1/tasks 请求体
{"target_node":"mac-mini","target_agent":"echo-demo","input":{"args":["hello","swarmmind"]},"submitted_by":"hana","idem_key":"demo-001"}
// 终态 Task.Result
{"status":"completed","exit_code":0,"final_answer":"hello swarmmind","stdout_tail":"hello swarmmind\n","stderr_tail":"","artifacts":[],"started_at":"2026-10-20T09:00:00Z","finished_at":"2026-10-20T09:00:00Z"}
```

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
```

**执行环境规则**：runner 使用 base_env（+任务在 allow_env 白名单内的覆盖）构造进程环境，不继承 daemon 自身环境（防凭证泄漏进子进程）。**runner 额外注入三个系统变量（不受 base_env/allow_env 约束、任务 env 不可覆盖）：`SWARM_TASK_ID`、`SWARM_OUTPUTS_DIR`（=workdir/outputs/<task_id> 绝对路径，agent 产物约定输出位置）、`SWARM_TRACE_ID`**。**HOME 由 runner 固定设为 ExecProfile.ConfigHome**（agent 配置/凭证目录，默认 data_dir/home，与 workdir 分离——不破坏 agent 的配置查找），cwd=workdir。越界（env 键不在名单）任务直接 failed，error=`policy_denied`，不执行。
**能力边界声明**（写入文档）：CLI agent 的行为边界由 command 本身性质与其自带权限机制决定；stdin/env 是输入面；allow_args 授权解释器类命令等同全权授权，选用即知情；PermissionMode 把 agent 自带权限映射告知调用方。

任务执行 context 独立于会话：唯一超时源是 timeout_sec；会话断开不杀已 spawn 的任务（结果经账本在重连后补传）。**进程组治理（V0 必做，接真实 agent 的前提）**：Unix 下 spawn 前 setpgid 置独立进程组，超时/取消时 kill(-pgid) 整组终止，WaitDelay=5s 兑底；V0 只承诺 Unix 进程组，Windows Job Object V1。

## 六、交互面与管理 API

**交互形态（V0）**：人工操作走 `swarmmindctl` CLI（submit/query/agents/nodes/review 五命令，薄包装管理面 API，与 hub/node 同仓库同二进制集）；其中 review 在 V0 仅展示待核对上下文与处理指引（核对记录的持久化接口属 P3 增补，见 PLAN 生命周期）；agent/脚本直接调 HTTP API。**Web 控制台列入 P3（生命周期 PLAN 第七节）：登录制 + 派任务 + 任务管理 + needs_review 核对（原型 v2 已出，亮色带操作页）；实现时 hub 需增加控制台鉴权（访问令牌登录），安全模型从「纯回环无鉴权」扩展为「数据面回环 + 控制台登录制」，具体契约在 P3 开工前的 SPEC 增补中定义。**

### hub HTTP 管理面（api_listen，V0 无鉴权但绑回环）

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | /api/v1/tasks | `{target_node, target_agent, input, submitted_by, idem_key?}` | 202 `{task_id, trace_id}`；幂等命中 200 同 task_id + `dup:true`；同键不同内容 409；信箱满 503 `mailbox_full`；目标不存在 404；超限 400 |
| GET | /api/v1/tasks/{id} | - | 200 Task（含状态/结果/needs_review） |
| GET | /api/v1/agents | - | `{agents:[{card, node_id, online}]}`；card=AgentCard，**绝不包含 ExecProfile 字段**（执行配置不出本机） |
| GET | /api/v1/nodes | - | `{nodes:[{node_id, online, epoch, inflight}]}` |
| GET | /api/v1/tasks/{id}/artifacts/{index} | - | 200 产物内容（hub 经 artifact_fetch/artifact_data 中继到执行 node 取回；超时 30s → 504） |
| POST | /api/v1/tasks/{id}/cancel | `{reason?}` | 202 取消已受理（hub 置 cancelled 终态 + 下发 task_cancel，node 结果异步回传）；幂等命中 200；已终态（completed/failed/needs_review）409 `not_cancellable`；不存在 404。管理面能力，**不设 MCP 工具**（取消保留给人工管理员，agent 不可互删任务） |

**无 HTTP 轮询回退**：离线路径=信箱+重连补推，别无分叉（避免绕过账本/Ack 的第二条路径）。旧 `/tasks/fetch` 概念废除。
任务提交校验链（顺序与 §4.5 一致）：**鉴权/目标存在且 exposed=true（未接入 → 404 `agent_not_exposed`）→ idem 查重（命中即返回，不做配额检查）→ 序列化体积实测预检 → 信箱配额与字节预算 → 持久化（单写事务内全部重检）→ submit_ack**。全链失败则无 Ack。

### node 本地 API（双机互派的提交/查询入口，V0）

node 侧同开回环 API（默认 `127.0.0.1:8445`，仅本机进程可用）：`POST /api/v1/tasks` 与 `GET /api/v1/tasks/{id}`，语义与 hub 管理面一致，内部经 WSS 会话转发为 task_submit/task_query 帧。这样任意本机进程（脚本/agent）都能派单与查询，跨机互派闭环：本机进程 → 本机 node → hub → 目标 node。**已知限制**：提交方若非 node 本机而是 hub 侧回环调用方，取结果靠 hub 管理面轮询；node 提交方的终态经 result_event 推送。

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

- `GET /api/v1/directory`：全网 exposed=true 的 AgentCard 列表（不含 ExecProfile）——中心端与 node 代理共用
- node 本地 API 的目录代理已实现（hub_api_url/hub_api_token），双机可达方案：hub 管理面 P3 起绑定公网反代后（带鉴权），node 配置指向反代地址

### 提交身份

- 控制台提交：submitted_by=admin（会话固定）；node 帧提交：submitted_by=node_id（hub 按连接身份覆盖）
- submitted_by 仅作显示与审计标签，鉴权依据是会话/令牌本身

### A2A 兼容端点（P3 收尾，2026-10-22）

- `GET /.well-known/agent.json`（免鉴权，A2A 发现惯例）：AgentCard（对齐 A2A v0.2.x），skills 由全网 exposed 目录聚合（id=`node_id/agent`）；只含能力元数据，ExecProfile/令牌/工作区互斥键一概不出
- `POST /a2a/rpc`（Bearer PAT `smsw_` 或会话 cookie，与现有同一鉴权）：JSON-RPC 2.0 手写分发（零新依赖）；`message/send` 等价 POST /api/v1/tasks（submitted_by=a2a，messageId 映射幂等键 `a2a:<id>`）、`tasks/get` 等价 GET /tasks/{id}；未知方法 -32601、任务不存在 -32001。外部协议兼容层本身即 agent 调用面（与 MCP 同位），故不另设六.六工具行
- 状态映射（七态→A2A TaskState）：submitted/offered/acknowledged→submitted、running→working、completed→completed、failed→failed、needs_review→input-required（原始状态保留于 metadata.swarmmind_status）；已知偏差清单见 internal/hub/api/a2a.go 头注

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
| swarmmind_submit | POST /api/v1/tasks | 派任务（submitted_by=mcp；支持 idem_key/trace_id 透传，幂等重派与任期追踪） |
| swarmmind_status | GET /api/v1/tasks/{id} | 查状态与结果（不等待） |
| swarmmind_wait | （轮询 status） | 等到终态；needs_review 为终态并明确提示需人工核对 |
| swarmmind_artifact | GET /api/v1/tasks/{id}/artifacts/{i} | 取产物（文本回读，二进制提示走控制台） |
| swarmmind_decide | 外部 StartLux /v1/decisions（bamboonode/MLX 本地） | 分配决策查询：云端 L0 → 规则兜底 L2 自动降级，响应标 used_level（P4.1） |
| swarmmind_group_create | POST /api/v1/groups | 建协作群（目标+成员；返回 g- 群 ID，创建即 discussing；GROUP-RFC 六 MVP，P4/V1） |
| swarmmind_group_list | GET /api/v1/groups | 看全部协作群（目标/状态/成员/发言预算） |
| swarmmind_group_say | POST /api/v1/groups/{id}/messages | 群里发言（kind 缺省 user；agent 发言计入预算，用尽报 budget_exhausted；消息体 8KiB 上限） |
| swarmmind_group_messages | GET /api/v1/groups/{id}/messages?since | 拉群消息（since 增量补齐，seq 单调锚点） |
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
3. **人工管理专属豁免（2026-10-22）**：POST /api/v1/tasks/{id}/cancel（任务取消）不设 MCP 工具——取消是管理面的人工干预能力，暴露给 agent 会引入互删任务风险；agent 侧只能等待终态（wait 对 cancelled 即时返回并提示已取消）。除本条显式豁免外，新管理面 REST 能力仍须同步 MCP 工具。
3. 工具命名固定前缀 `swarmmind_`；描述面向 agent 写「能做什么」，不写实现细节。
4. MCP 破坏性变更需在 SPEC 本节记录版本（protocolVersion 跟随 MCP 规范日期）。

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
11. **真实异构闭环（产品验收，双机）**：两台真实机器各跑 node，各接至少一种真实 agent（如 codex + claude code），完成发现 → 互派 → 执行 → FinalAnswer 与产物取回，覆盖断线恢复与权限拒绝各一次

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
