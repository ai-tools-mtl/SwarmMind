# SwarmMind SPEC — V0（v3.0 FINAL）

> 本文维护协议、接口与可靠性契约，包含 V0 基础及后续 P3–P5.1 增补。项目范围与实施状态见 [PLAN](docs/PLAN.md)。契约要求、实际运行差异与验收报告分别标注。
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
  - `api_listen`（默认 `127.0.0.1:8444`）：HTTP 管理面 `/api/v1/*`，与节点监听物理分离。默认回环监听。当前实现允许配置非回环地址，但要求预先设置非空 `admin_token`，否则拒绝启动；管理面仍为 HTTP，远程访问需配置 HTTPS 反代或 SSH 转发。
- node 单进程：不监听任何公网端口，仅提供本机回环 API（127.0.0.1:8445，提交/查询）。指纹校验：`hub_pubkey_fp` = 证书 SPKI 的 SHA-256（base64）；TOFU——首连成功将 `host:port→fp` 记入 `data/trusted_hubs.json`，此后不匹配即断开告警；指纹非空则首连即强校验（TLS 握手时完成 pin 验证，验证通过后才发 register_token）；TOFU 记录写失败仅告警不阻断。
- 鉴权：register_token 每 node 独立（hub 配置 `nodes: {<node_id>: <token>}` 映射），token 绑定 node_id，不可跨身份使用。**每次连接全量鉴权**（V0 无会话凭证复用/撤销机制，V2 再建），连接本身即会话。防重复执行保证范围=同一 data_dir 账本（单实例锁，见 4.7）；新增节点应分配独立 node_id 与 token；复制同一身份并发运行可能替换当前会话，不视为安全的新节点注册。

## 三、目录结构

```
repo/
├── go.mod                      # module github.com/mtl802/swarmmind
├── cmd/hub/main.go
├── cmd/node/main.go
├── cmd/swarmmindctl/main.go       # 人工操作 CLI：submit/query/agents/nodes/review，薄包装管理面 API
├── internal/
│   ├── protocol/               # 信封、消息类型、状态机、常量（hub/node 共享）
│   ├── settings/               # 管理面配置通用 JSON 存储（原子写；2026-10-24 自 hub/ 提升，hub/node 共享）
│   ├── notify/                 # 任务终态通知（渠道/分发/存储/同时通知开关；2026-10-24 自 hub/notify 提升——hub 组网与 node 三模式共用，2026-10-26 同时通知裁决）
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
| golang.org/x/sys | Windows 服务化（svc/svc/mgr）+ Job Object 进程治理（P4②）；unix 源文件逐段 openat 游走（inputs/sourceopen_unix.go，2026-10-24 复审 R1）；windows 源文件逐段 NtCreateFile+OBJ_DONT_REPARSE 游走（inputs/sourceopen_windows.go，2026-10-25 三审 W2） | 半标准库（Go 官方扩展库）：Windows SCM 服务编程唯一正道，无更轻替代；源打开需在 open 原语上强制不跟随——unix 侧标准库 syscall 在 darwin 不导出 openat、os.Root 会跟随根内末段链接；windows 侧 os.Root 遇 reparse point 读链后跟随根内链接、OBJ_DONT_REPARSE 等价旗标封装在 internal/syscall 不可导入。仅 Windows 服务化/Job Object + windows/unix 源打开引入，同模块零新增 |
| modernc.org/sqlite | hub 全量状态持久层（internal/hub/store，2026-10-26 迁移）：实体分表 + key + JSON 体列，diff 增量写入，WAL+FULL | 纯 Go 实现（无 cgo，保 Windows 构建与交叉编译纪律）；单文件嵌入库零部署（对齐自用轻运维定位，无独立 DB 进程）；WAL+synchronous=FULL 兑现掉电耐久性升级（进程崩溃不丢已确认提交）。间接依赖树较大（libc/memory/exp 等）属纯 Go SQLite 方案已知代价，接受。node ledger 不引库（KB 级账本保持 JSON，见 4.8） |

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
| artifact_fetch | 提交方→hub | `{task_id, index}` | 请求取回第 index 个产物（hub 中继到执行 node）。提交方含 node 侧产物挂载准备阶段（P4④跨机取回）：hub 依赖归属鉴权（请求方名下须有依赖该任务 idem 的任务）后按原 mid 中继，应答转发回请求 node；源 node 不应答时中继超时 30s 回 err(fetch_timeout)，离线快回 err(node_offline) |
| artifact_data | 执行 node→请求方（经 hub 中继） | `{task_id, index, meta, data_base64}` | 产物内容（单文件原始 ≤640KiB，base64 后随帧 ≤1MiB；更大产物 V1 分块）；hub 中继对照登记复检 SHA-256 后原样转发（中继链双保险） |
| dep_resolve | node→hub | `{idem}` | 依赖解析（P4④跨机取回）：下游 node 按依赖 idem 请求上游任务定位与产物清单；hub 按 resolveIDem 同口径（最早 completed）解析并做依赖归属鉴权（请求方名下无依赖该 idem 的任务 → err(forbidden)；无 completed 上游/已清理 → err(task_not_found)）；应答 task_status 携**脱敏 Task**（ID/TargetNode/TargetAgent/IdemKey/Status/时间 + Result{Status, Artifacts}；Input/SubmittedBy 等内容隐私不出 hub，node 会话边界同 task_query 的 D10 锁） |
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
| 终态保留 | hub 终态任务 24h 后清理；node 已实现 CleanupAcked（按 first_acked_at+24h），但当前未接入运行循环，不能视作自动清理已生效，审计保留策略待确定 |
| node 执行并发 | 2（可配） |
| 信箱字节预算 | 每 node 8MiB（submitted 任务体字节和），超则 503 `mailbox_full` |
| hub 任务存储全局字节预算 | 256MiB（含终态 24h 保留），超则拒新提交 503 `storage_full`；部署声明：2核2G 云机目标为低负载（≤5 node 且预算内），满配（20 node×100 条×1MiB≈2GiB）不承诺 |

### 4.7 持久化事务（hub store 为 SQLite；node ledger 保持 JSON，同模式同纪律）

**先写后见（不变的事务语义）**：单写 goroutine 串行处理变更 → 构造新状态副本 → 持久化成功 → 才原子替换内存可见状态并发 Ack → 失败则丢弃副本、不发布、不 Ack。禁止先改内存再补盘。node 侧关键顺序不变：accepted 落盘成功 → 发 task_ack；started 落盘成功 → 发 task_started → spawn；finished+result 落盘成功 → 发 task_result。

**hub store 持久层（2026-10-26 迁 SQLite，替代 JSON 全量快照）**：
- **存储形态**：`data_dir/hub_state.db`（SQLite 单库）。实体分表九张（nodes/tasks/mailboxes/batches/batch_idems/workdir_locks/works/work_idems/prime），行 = key（map 键，主键）+ body（条目 JSON 体）。schema 版本记 PRAGMA user_version，库版本高于本代码时拒绝启动（诚实失败优于误读）。
- **增量事务**：Update 后对比 old/new 产出 diff（map key 集合差 + 变 key 条目 JSON 相等比较），仅 UPSERT/DELETE 变化行，单事务提交；**diff 为空零写库**（无变化事务照常发布+Ack，不触盘——顺带消灭 JSON 时代 sweep 空转全量重写问题）。禁止全表重写（仅首启导入路径例外）。
- **掉电耐久性（较 JSON 快照升级，如实声明）**：WAL 模式 + synchronous=FULL + 单连接（SetMaxOpenConns(1)，与单写 goroutine 模型对齐）——进程崩溃不丢已确认提交（WAL 恢复）；主机掉电以 fsync 语义为准（FULL 下提交时同步 WAL，不承诺机房级灾难）。注入点语义沿用：after_rename = COMMIT 成功之后，「已提交但确认丢失」重启以磁盘为准。
- **旧 JSON 自动导入（停机切换，无双写）**：首启检测——SQLite 库为新空库（user_version=0）且 hub_state.json 存在 → 单事务「建表+导入全量+盖版本」→ 成功后将 json 备份改名 `hub_state.json.imported-<unix秒>`；导入/解析失败保留 json 原样、启动报错（下次启动重试）。已初始化库与 json 并存 → 以 SQLite 为准，json 不动。残留 hub_state.json.tmp（JSON 时代产物）忽略。
- **内存模型不变**：内存仍驻留全量 State（View 零拷贝只读契约、写时复制、Clone 深拷贝纪律原样），SQLite 只是持久层。View 遍历式调用方零改动。
- **部署注记**：WAL 需本地盘（网络文件系统上不可靠；生产 hub 为本地盘，满足）。备份口径从「拷 json」变为「拷 hub_state.db（建议 sqlite3 .backup 或停机后拷，避免拷到 WAL 中间态）」。运维自查可用 ReadSnapshot（独立只读连接绕过运行中 Store）。

**node ledger（保持 JSON，不变）**：账本为 KB 级、无容量压力，保持 ledger.json 临时文件+rename 原子写；不引 SQLite（2026-10-26 迁移裁决：一次迁移两侧都动风险翻倍，node 病是纪律病不是容量病）。
**取消墓碑先写后见修正（2026-10-26 双审缺陷修复）**：cancels.json（独立 sidecar，同 flock 保护）的 add/clear/cleanup 一律「副本构造 → 落盘成功 → 替换内存」——落盘失败内存不污染、同键重试不得假成功（旧实现先改内存后写盘，失败后重试命中内存跳过写盘，重启丢墓碑）。

`data_dir` 单实例锁：**OS 文件锁（Unix flock，进程退出自动释放）**，不用 O_EXCL 锁文件（SIGKILL 后残留会永久阻塞重启，DoD 6/10 无法执行）；锁冲突则退出报错。Windows 侧随 P4② 服务化落地同语义实现（CreateFile 独占打开 + LockFileEx，句柄随进程退出由 OS 释放；lock_test.go 跨平台同一套契约断言）。hub 与 node 锁语义均不变，与持久层形态无关。
**取消链路确认语义（2026-10-26 双审缺陷修复）**：取消闭环确认（task_cancelled ack / task_result）必须以持久化成功为前提——墓碑落盘失败不发 ack（hub 重发重试）；MarkFinished 失败区分错误：ErrWrongState（先到终态为准）回幂等 ack，IO 落盘失败不发任何确认（旧实现两错混发 ack，ack 掉则重启后无防复活保障）。

### 4.8 node 执行账本（internal/node/ledger）

`data/ledger.json`：`task_id → {task: 完整Task, state: accepted|started|finished, result?, result_acked}`（持久化完整任务输入，重启后无需 hub 重派）。
启动恢复：
- accepted（未 started）：检查任务 expire_at——已过期 → 不执行，写 finished（status=failed，error=expired）并回传；未过期 → 进入本地调度执行；
- started 未 finished：执行结果不确定 → 写 finished，status=`needs_review`，error=`node_restarted_midway`，回传；**禁止自动重新执行**；
- finished 且未 acked：重发 task_result 直至 result_ack。
已实现的 CleanupAcked 可按 first_acked_at+24h 清理 result_acked 条目，但当前未在运行循环调用；该时间是清理契约而非已生效的自动审计保留期。needs_review 是独立终态：查询可见、语义为「执行结果不确定，需人工核对」，协议与展示层不得折叠为 failed（防止调用方误自动重提）。
**产物隔离与保留**：产物目录按任务隔离（outputs/<task_id>/），进程结束后 runner 冻结产物清单（登记后新增文件不追认）；原设计要求以 result_ack 后 24h 作为产物保留窗口，但当前没有相应的自动文件回收接线；文件实际可取还取决于源文件、账本、节点可达与 hub 任务记录，不能据保留期保证一直可取；artifact_fetch 取回时 node 校验 SHA-256 与登记一致，不一致回 err(artifact_mismatch)。

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
    Error      string       `json:"error,omitempty"` // timeout / policy_denied / expired / user_cancelled / node_restarted_midway / input_prepare_failed（上游产物挂载失败，4.12）...
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
4. **拒收**：不符 → `zombie_result_rejected`：任务维持原状态（不改 Status/UpdatedAt/Result，不动既有任务状态机），`Task.Lease` 记审计（Rejects++/LastRejectedFence/LastAt，随快照持久化、查询面可见），hub stderr 记日志（含 fence_epoch 与 current_lease），并回 err(result_conflict)（msg 注明 zombie_result_rejected，ErrInfo 结构化携带 `fence_epoch`=被拒回带值、`lease_epoch`=当时落库租约）——node **仅当被拒 fence 与账本条目当前 fence 等值才置 closed 停止重发**（拒收旧代次不得关闭已重臂条目，否则新租约结果首发丢失后周期/重启补传全被跳过，任务永久滞留；未携 fence 字段的旧版对端与普通终态冲突按既有规则无条件关闭），zombie 结果不再周期性重入仲裁。
5. **task_cancel 口径（P1-2 修订）**：取消帧自身**不需要门禁**——取消 ⇒ 任务 hub 侧终态 ⇒ 同 task_id 永不重派，故「旧取消命中新派单执行」在结构上不可能；重复取消由幂等 ack/取消墓碑收敛（node 侧既有语义）。帧携 **hub 当前任务落库租约（cancel_fence）**，node 侧取消回写（task_result(cancelled)）前对「账本条目租约 vs cancel_fence」**取大者重臂**后回带——重派已推进租约而 node 未及重臂的滞后（ack 丢后重派、重派 offer 送达失败留 submitted 等）经取消帧补授收敛；hub 对 cancelled 回执按 **cancel_fence ≥ 落库租约**宽校验接受（取消是管理面意图，宽于执行回执的等值）。经当前会话送达的取消帧 zombie 收不到，无法伪造新 cancel_fence；旧代次的 cancelled 回执（< 落库租约）仍被第 3 条门禁拒收。ack/started 类过程帧同理不设门：无结果载荷，无状态污染面。

**与 4.4 会话 epoch 的关系**：正交两层。会话 epoch 保证「帧只经当前会话进入」（传输层，送达方身份可信）；任务租约保证「回执对齐当前派发」（任务层，派发代次一致）。防御目标：同 node_id 被两个进程持有（部署事故/慢 zombie 抢回连接）时，旧进程在旧派单下产生的结果无法覆盖新派单状态。

**已知代价（声明）**：跨代次的执行若任务未重派（如 node 执行中掉线重连、hub 侧任务仍 running），其合法结果与 zombie 结果对 hub 不可区分，按本契约回带原租约**接受**（等值）——状态机安全由会话层身份担保；真正的残留风险窗口仅「同 node_id 双进程且旧进程抢回会话且任务未重派」，此时回执来自当前会话持有者、且 node 单账本保证每任务至多一个执行，污染面为诊断数据而非状态机。租约拒收后的滞留任务（如重派竞态失败留 running）由 7d expire 兜底，V2 自动回收以此为输入。

**MCP 同步检查**：本变更为协议内防写机制 + 审计字段，结果路径无新增/变更 REST 管理面能力，MCP 工具面零变更（任务查询工具返回体透传 `lease` 展示字段，不构成工具面变更）。

### 4.12 任务依赖（P4.3 第一子批，2026-10-23；FILE-COLLAB L2 依赖门控+调度。产物挂载：L2 第二子批第一子轮 2026-10-24 交付同机骨架；第二子轮 2026-10-24 交付跨机取回）

**范围声明**：第一子批做依赖门控与调度；第二子批第一子轮交付**同机产物挂载骨架**（本节「产物挂载」段）；第二子轮交付**跨机取回**（artifact_fetch 方向扩展 + dep_resolve + hub 中继，本节「跨机取回」段）。

**寻址（FILE-COLLAB 终审口径）**：`depends_on` 使用 **idem_key**（跨重跑稳定）而非 task_id；声明后不可变（提交即定，无改边路径）。

**Schema（新字段全部 optional，既有任务状态机/收敛表/终态规则零改动）**：

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

**幂等指纹口径（SPEC 4.5 补充）**：声明 `depends_on` 时提交输入取**生效形态**——stdin 尾部附加依赖清单（上游 idem 列表；幂等判据 `[swarmmind:depends_on]` 标记行，不重复追加），清单为提交请求的确定性函数；请求指纹（ContentHash）与落库任务指纹（taskContentHash）过同一变换，重试 dup 判定不受清单注入影响。未声明依赖时逐字节兼容旧口径。依赖列表本身参与指纹（同键换依赖=不同内容=409）。**清单文案一次性变更声明（2026-10-24，挂载交付）**：清单尾注由「产物自动挂载尚未提供」更新为挂载位置说明（`inputs/<上游task_id>/`）——指纹含生效 stdin 全文，跨本次升级重试「升级前提交的携依赖+idem 任务」将 409 conflict 而非 dup 命中（属一次性声明的不兼容；出现冲突时先查询原任务、核对是否已接受或执行；不要仅为绕过冲突而换键重复提交）。**第二次一次性变更声明（2026-10-24，跨机取回交付）**：清单尾注「跨机场景暂未提供自动取回」更新为跨机自动取回说明（同前例：升级窗口内携依赖任务的幂等重试 409 conflict，冲突时先查询原任务并核对执行事实，需要新的执行时才分配新键）。

**门控与调度**：
- **blocked 不进状态机**：依赖未满足的任务保持 `submitted`，不投递；既有任务状态机、迟到事件收敛表、终态规则零改动。出队 eligible 增补一条：`BlockedReason` 非空即跳过（与 P1.5 workdir 互斥门控同机制复用）。
- **blocked 任务留信箱占配额**：入箱/计量口径与普通 submitted 任务一致；信箱满拒新提交的既有语义不变（SPEC 4.6）。
- **事件触发复评**：上游任务到终态的仲裁事务处——result.go 首次落终态（task_result 接受分支）、CancelTask 置 cancelled、Sweep 三处置终态（expired/排队超时/offer 超时过期）——**同一单写事务内**复评其 idem 的全部下游任务（依赖全 completed → 解除阻塞，发布后按目标节点 dispatch；任一上游 failed/needs_review/cancelled → 保持 blocked 并更新标注）。单事务原子生效，无「上游已 completed 而下游仍 blocked」的事件丢失窗口。
- **Sweep 兜底**：每秒扫描（`sweepInterval` tick）对全部 blocked 的 submitted 任务全量复评，防御未知路径的事件丢失；解除阻塞随本轮 dispatch。
- **排队超时豁免**：blocked 任务不适用 P1.5 `workdir_busy_timeout`（等待的是上游终态而非 workdir，`workdirBusyFor` 不因门控成立）；7d expire 兜底照常（SPEC 4.3/4.6）。
- **放行单向**：已解除阻塞（含未及投递）的任务不因上游终态清理被重新挡回——依赖均为终态不再变化，复评只作用于 `submitted` 且仍 blocked 的任务。
- **hub 重启恢复免费**：依赖门控全部字段随任务快照持久化（Clone/JSON 覆盖），重启后 blocked 状态原样恢复，Sweep 兜底继续复评，无独立恢复流程。

**依赖解析口径**：依赖 idem 解析当前快照——有 completed 任务即满足（任一版本；多活同键（group_ref 装饰边，SPEC 4.5）宽松口径取最早 completed）；否则有非终态任务为等待中；否则（仅 failed/needs_review/cancelled）为上游失败；快照无该 idem 保持等待（`blocked_by:<idem>`，7d expire 兜底）。

**产物挂载（L2 第二子批第一子轮交付同机骨架，第二子轮补跨机取回；实现 `internal/node/inputs`）**：依赖门控放行的下游任务，node 在 **started/spawn 之前**把全部上游任务的产物**纯复制**（禁硬链接——下游改动不得污染上游产物 SHA）到消费者专属输入区 `<下游workdir>/inputs/<上游task_id>/`：

- **时序**：node 执行链 N4 过期重检之后、MarkStarted 之前（输入就绪前不 started 不 spawn，FILE-COLLAB L2）；挂载发生在下游 agent 的 workdir 互斥持有期内，同 agent 任务的输入区写入天然串行；挂载耗时落在 hub 视角 acknowledged→started 之间，无 hub 侧计时依赖。
- **解析**：node 解析依赖 idem → 上游任务（口径=**最早 completed**，与 hub resolveIDem 一致）：先扫本机账本（产物登记（Path/SHA-256/Size）随上游 result 回流时落账本，SPEC 4.8）；本机无 completed 上游（`InputResolver` 组合解析器 `Resolver`，Local 优先 / Remote 兜底）→ 转跨机取回（见下段「跨机取回」）。协议帧零新增于 offer 侧：offer 载荷本就全量携带 `depends_on`。
- **路径口径**：上游产物 `outputs/<上游task_id>/<rel>` → `inputs/<上游task_id>/<rel>`（保留 outputs/<task_id>/ 内部相对结构）；拒绝口径外/穿越/绝对路径；源文件必须常规文件（符号链接不跟随）。
- **文件操作安全（os.Root 定界）**：`inputs` 基目录存在性以 Lstat 校验（符号链接/非常规目录一律拒绝，防跟随链接逃逸 workdir，创建后复验防竞态）；inputs 域内全部盘上操作经 `os.Root`（openat 语义）定界，越界路径不可达；上游源文件整条路径逐段 Lstat（中间段符号链接不跟随），最终以打开后的 fd fstat 确认常规文件——校验对象=读取对象（消除 Lstat→Open 替换竞态）；复制以登记大小+1 为字节上限（防源在检查后增长），复制后核对实得字节数==登记大小。
- **校验与原子发布（journal 事务，per-上游原子）**：先写 `inputs/.staging-<task_id>-<rand>/` 暂存目录，逐文件复制（O_EXCL+刷盘+ctx 贯穿）后复读校验 SHA-256 对照登记；发布前先落 journal（`inputs/.staging-…/journal.json` 发布意图+步骤状态，临时文件+rename 原子写），随后逐上游按依赖声明序紧凑执行「旧目录移入暂存 trash → 整目录 rename 发布 → journal 状态推进」，rename 后目录 fsync——**单上游自身原子**（移开旧+发布新两条 rename 由 journal+fsync 保护，崩溃后重挂先按 journal+盘上实物回放收敛：完成未竟发布或旧目录归位），多上游整体跨上游非原子（如实声明，部分发布由下一上游失败回滚或崩溃恢复收敛）；同上游目录已存在则幂等替换（恢复重挂/同 agent 后续任务同依赖）；任一校验/发布失败 → 尽力回滚（撤销已发布项、旧目录归位、清理暂存），输入区保持挂载前原状；回滚自身失败则保留 trash/暂存/journal 材料不删，错误聚合报告哪个上游旧目录需人工恢复。末次发布后复查超时/取消——超时后输入区不得视为就绪（调用方不得 MarkStarted）。
- **限制（对登记重检，防御超清单）**：每上游 ≤8 件（MaxArtifacts）、单件 ≤640KiB（ArtifactLimit），与既有产物扫描口径一致。
- **失败语义**：上游产物缺失/SHA 不符/超限/解析失败/发布失败 → 下游任务 **accepted 直达 failed**（不 started 不执行，对齐 expired/policy_denied 的 pre-started 失败先例），`error=input_prepare_failed` + 上游任务 ID + 文件定位；该失败即上游失败语义，继续向下游的下游传播 `upstream_failed` 标注。挂载期间管理面取消 → cancelled 收口；服务关闭打断 → 条目保持 accepted 由重启恢复再调度（重挂幂等：先按 journal 回放收敛崩溃残留再挂载）。
- **独立超时**：输入准备独立于任务执行超时（service 装配参数 `InputPrepareTimeout`，默认 60s；本子轮不进 node.yaml），超时按上述失败语义收敛。
- **边界（如实声明）**：同机（同 node）上游直接读本机 outputs 目录；上游在异机 node → 自动跨机取回（见下段）；上游 agent 配置漂移（workdir 不可定位，本机性失败）同失败不转远端（返回明确原因）；上游 completed 零产物登记（答案型）→ 不建目录不失败（本地/远端口径一致）；同一上游任务满足多个 idem → 去重只挂一次。
- **无依赖红线**：无 `depends_on` 的任务不进入挂载路径，行为零变化。

**跨机取回（P4④第二子轮，2026-10-24 交付；FILE-COLLAB L2「跨机交接」的 V0 形态，分块传输 V1 不做）**：下游 node 本机账本无依赖 idem 的 completed 上游时（源在异机 node，或本机账本条目已随保留期清理而 hub 尚存），经 hub 中继自动取回上游产物字节流，进入与同机路径**完全相同**的挂载事务（`UpstreamInputs.Fetch` 字节源缝：SHA 校验/暂存/原子发布/回滚零分叉；standalone/setup 不装配跨机取回，行为与第一子轮一致）。

- **帧流向**：① `dep_resolve`（node→hub，`{idem}`）——hub 按 resolveIDem 同口径解析 + 依赖归属鉴权，脱敏 `task_status` 应答（上游任务 ID/产物清单；Input 等内容隐私不出 hub，node 会话边界同 task_query 的 D10 锁）；② `artifact_fetch`（node→hub，`{task_id, index}`，方向扩展）——hub 鉴权后按 **hub 分配的中继编号** 中继执行 node（在途表以中继编号为键并保存原请求方/原 mid，回传时以原 mid 恢复端到端 ReplyTo 关联——请求帧 mid 由调用方提供可冲突，不作表键，防同 mid 二次请求覆盖在途 waiter，2026-10-24 审查必修），执行 node 回 `artifact_data` → hub 校验应答来源为执行 node（来源不符的帧不消费在途表、不停超时——伪应答不得挤掉合法应答与超时兜底）→ 对照登记复检 SHA-256（中继链双保险，与 HTTP 数据面同口径）→ 原样转发请求 node；源侧 err 帧原样透传。
- **依赖归属鉴权（dep_resolve 与 artifact_fetch 同源）**：请求方 node 名下必须存在 `DependsOn` 含该上游 idem 的任务，否则 err(forbidden)——产物挂载取回是依赖的衍生权限，防 node 会话借道刺探任意任务/产物。artifact_fetch 的无权限场景**统一响应**（探测无效化，2026-10-24 审查建议修）：任务不存在（含已过保留期清理）与无依赖归属不做区分，一律同一 forbidden（同码同文案）——未授权方无法借 task_not_found/artifact_not_found 差异判断任务存在性或探测产物下标；精确错误只授予已通过鉴权的请求方。
- **超时与重试轮询（三层）**：单次尝试——hub 中继应答预算 30s（源不应答回 err(fetch_timeout)，源离线发送失败快回 err(node_offline)）；取回侧单尝试预算与 hub 对齐（30s，兜底帧丢失）；准备阶段总预算=既有独立超时 `InputPrepareTimeout`（默认 60s 不变——失败路径均有确定应答不悬垂，暂无放宽实证）。可重试失败（node_offline/fetch_timeout/传输失败）按 5s 间隔轮询直至准备预算穷尽（源恢复即同一次准备内继续挂载——DoD「node 回来」路径）；穷尽 → `input_prepare_failed`（注明暂不可取，不 spawn）。
- **离线 vs 已清理（严格分类，不静默跳过）**：可重试类（离线/中继超时）=「暂不可取」；`artifact_not_found`/`task_not_found`/`forbidden` = 永久（上游产物已过保留期清理/无 completed 上游/越权），立即失败，文案注明上游任务与文件定位；`artifact_mismatch` = 永久完整性失败（node 侧、hub 中继、取回侧三重校验任一拦截）。
- **限额不变**：≤8 件/上游、单件 ≤640KiB（协议帧 1MiB 上限内），对登记重检口径与同机一致；分块传输 V1 明确不做。
- **一致性口径**：跨机解析同样取 hub 当前快照最早 completed（与门控放行版本可能因上游重跑而不同，与同机「账本清理后取现存最早」同一声明——SHA 校验保证挂载内容与登记一致）；上游重跑后同 idem 新版本 completed 不影响已放行下游的既有语义（superseded 审计见前文）。
- **边界（如实声明）**：hub 是跨机取回的单点中继（hub 不可达 → 准备阶段内重试，穷尽 input_prepare_failed，standalone 无此能力）；中继链完整建立在 SHA-256 端到端校验上（hub 不产出内容只转发+复检）；版本偏斜（新 node + 旧 hub）dep_resolve 未知帧被忽略 → 准备超时失败，如实暴露。

**边界声明**：依赖门控仅 hub 提交链生效（HTTP 管理面 + node 会话 task_submit 帧）；node 本地直执行链（node localapi POST /tasks，origin=local）不经过 hub 状态机，不支持依赖声明。node 收到的 task_offer 载荷可能携带 `depends_on`/`blocked_reason` 字段（Task 全量透传）：node 侧**不做依赖门控**（门控判定只在 hub，node 不复算 blocked）、`blocked_reason` 不感知不入状态机，但网络下发任务携带的 `depends_on` 由产物挂载消费（见上文「产物挂载」段：按依赖 idem 解析上游产物挂载至输入区）。

**MCP 同步**：`swarmmind_submit`（hub 模式）新增 `depends_on` 数组参数透传（见六.六规则 7）；REST 提交请求体透传（handleSubmit 平铺解码 TaskSubmitRequest）、任务查询返回体携带三字段——均为 optional 字段，既有 API 兼容性不变。跨机取回（第二子轮）无 REST/MCP 变更（见六.六规则 9）。

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

node 提供回环 API（默认 `127.0.0.1:8445`），当前 `POST /api/v1/tasks` 走 `SubmitLocalDirect` 在本机账本接受并执行，不要求经 hub 转发；`GET /api/v1/tasks/{id}` 根据本机执行记录与任务来源返回结果或相应指引。调用须通过本机凭据鉴权，编排入口还受 prime_agent/prime_token 规则约束。target_node 只能是本机 node_id，跨机任务统一经 hub 入口提交。`GET /api/v1/local/tasks` 返回本机现存账本任务（含 hub 下发）并合并提交留痕，支持 limit/offset/total：默认 200、上限 1000，非法参数 400，超出 limit 上限按 1000 截取。该清单不代表永久历史；自动账本清理尚未接入，保留策略另行确认。旧 node 会话 task_submit/task_query 协议仍受上一段归属约束，不等于当前本机直执行 API 的实现路径。

**通知渠道管理（2026-10-24 standalone 补盲区；2026-10-26 伦哥裁决「同时通知」起三模式全装配）**：**node 三模式（standalone/networked/setup）全部装配本机渠道存储与分发器**（cmd/node notifyDispatcherFor，2026-10-26 取代「networked 归 hub 不装配」旧裁决）——standalone 本机通知即唯一通知（开关不参与门控）；networked 下 hub 渠道照常通知（hub core 终态旁路），node 在本机任务终态（`MarkFinished` 首次落终态 + `NotifyRecoveredTerminal` 恢复旁路，去重锚点在账本）时按「同时通知」开关决定本机渠道是否**跟发**：`data_dir/notify_settings.json` `{also_notify: bool}` 默认 true（跟发；本机未配渠道自然静默零副作用）、false=本机静默 hub 一份照发；每次终态读盘不缓存（开关运行时即时生效，简单正确优先）；setup 无任务不触达。开关端点：`GET|PUT /api/v1/local/notify/settings`（local_token，与渠道端点同鉴权）。触发挂钩在 node service 终态落账本点（`MarkFinished` 成功 = 首次落终态，账本先到为准天然去重；覆盖正常完成/过期/取消/关闭中断/墓碑收口全部路径，`internal/node/service/notify.go`）。渠道配置存 `data_dir/notify_channels.json`（与 hub 同一 JSON 存储形态：internal/notify.ChannelStore + internal/settings.Store 原子写）。**渠道模型（2026-10-26 notify-unify 改动二，双端同构）**：独立 feishu 类型并入 webhook 的「消息格式」维度——`Channel.Format`（generic=通用 JSON POST+可带自定义头 / feishu=飞书机器人文本消息+无自定义头，空=generic）；新渠道不可再建独立 feishu 类型（准入明确报错指路），存量 feishu 渠道**载入时自动归一**（`migrateLegacyFeishu`：Type=feishu → Type=webhook + Format=feishu + Secrets url 键，内存态即时生效、落盘落新形态、幂等）——升级生产已配飞书渠道无缝继续工作；hub.yaml legacy 导入同样直接落新形态，**node.yaml 不设 notify 段**——控制台 CRUD 要求运行时可改即时生效（node.yaml 编辑走「应用并重启」流程），node 为全新能力无存量配置需兼容（hub 的 YAML legacy 导入是为存量用户付的兼容成本，node 不重复背），凭证放 data_dir（0600，与 local_token/prime_token 同域）也更符合本仓安全纪律。端点组：`GET|POST /api/v1/local/notify/channels`、`PUT|DELETE /api/v1/local/notify/channels/{id}`、`POST /api/v1/local/notify/channels/{id}/test`（列表脱敏/创建/更新占位保留/删除/发测试），全部仅 local_token 可调（主人管理面，六.六豁免⑧）；inbox 类型 node 侧准入即拒绝（站内通知为 hub 控制台铃铛专属，node 控制台无铃铛动线）；Dispatcher 未装配时端点组 503 降级（2026-10-26 起三模式全装配，守卫仅剩防御意义）。安全红线同 hub：读响应脱敏（Channel.Masked）、占位保留更新、投递/展示分离（原始 URL 投递、错误路径剥 path/query/userinfo）、日志不泄凭证、SMTP 全链路 deadline。控制台入口「服务 → 通知」（status.notify_available 门控，三模式装配后恒可用；networked 卡片头部带「同时通知」toggle，standalone 隐藏——语义不存在）。

**统筹凭证与指定·自动接线（docs/archive/PLAN-node-standalone.md §3.2.1 + 任务 t20261023-62a2）**：`POST /api/v1/local/prime/rotate`（轮换 prime_token：重生成→原子写盘→即时重载，旧凭证下一请求失效）与 `POST /api/v1/local/prime/declare`（网页指定统筹载体：校验 agent 存在→指定即换凭证→yaml round-trip 写 node.yaml `prime_agent`→即时生效）均仅 local_token 可调（主人人工操作，六.六豁免③④）。**指定即自动接线**：declare/rotate 成功后，node 自动把 swarmmind MCP 配置片段（含最新 prime_token）合并写入被指定 agent 影子 home（`ExecProfile.ConfigHome`，空则 `data_dir/home`，与 runner HOME 同口径）内的宿主配置文件——布局探测只认 zcode 一种（`<home>/.zcode/cli/setting.json`，注意文件名单数）：存在且为合法 JSON 才命中（合法 = 全文恰一个 JSON 对象：`null`、尾部多余内容均不命中）；命中即合并顶层 `mcpServers.swarmmind`（幂等覆盖同键、不动其他键），写前备份 `<file>.bak-prime`（直接覆盖旧备份不堆积），临时文件+rename 原子写、0600（文件内含凭证）。应答带 `wired: true|false` 与 `wired_target`（命中时的绝对路径），declare/rotate 两口径一致、应答均带 `mcp_config` 片段（降级时主人拿得到手贴内容）；**降级口径**：文件/目录不存在、JSON 非法（含 `null` 与尾部多余内容）、顶层或 mcpServers 非对象、写失败、或 swarmmind-mcp 探测未命中（command 为占位符——占位配置落盘无用，一律不写）→ `wired:false`，绝不硬写、绝不创建宿主目录，应答保留 `mcp_config` 片段与手动指引由主人粘贴（宁可手贴，不猜布局乱写）。rotate 同步刷新影子 home 内的 `SWARM_NODE_PRIME_TOKEN`（否则旧 token 残留，统筹载体下一请求 401）；撤销统筹不清理已写入配置（token 已即时失效，残留无害）。全部接线动作发生在 declare/rotate 这个主人人工操作的事务内，prime_token 仍不经 agent 面分发（豁免③口径不变）。

## 六.五、P3 增补契约（Web 控制台，按 INTERACTION.md 分级清单落地）

### 鉴权（控制台登录制）

- hub 配置新增 `admin_token`；`POST /api/v1/login {"token":"..."}` → 200 + `Set-Cookie: swarmmind_session=...`（HttpOnly；V0 会话内存态，hub 重启需重登）
- 任务、目录、Work、通知等管理 API 要求有效会话 cookie 或 Bearer PAT，否则 401。登录、账户初始化/状态和 A2A 发现卡等显式入口有各自访问规则，路由以 `internal/hub/api/server.go` 为准；admin_token 用于换取会话，不能直接作为 Bearer。
- 中心端「控制能力」实现澄清：hub 不内嵌执行体；控制操作（全网巡检等）= 管理员从中心端向任意 node 的巡检类 agent 派发正式任务（走现有全链），hub 保持登记处与控制面，服务执行始终在 node 侧

### 核对记录持久化

- `POST /api/v1/tasks/{id}/review {"action":"ack_review"|"mark_failed","note":"..."}` → 200
- 仅对 needs_review 任务有效；**不改动协议终态**（保持 needs_review），在任务上追加 `review {by, at, action, note}` 人工记录（入快照）；GET /tasks/{id} 返回含 review；重复提交覆盖旧记录
- swarmmindctl review 展示该记录；node 账本不感知 review（协议层无帧，纯管理面）

### 控制操作批次

- `POST /api/v1/control-operations {"agent":"inspector","targets":["n1","n2","n3"],"input":{...}}` → 202 `{"batch_id","items":[{"target","task_id"}|{"target","skipped":"offline"}]}`
- 实现即批量 Submit：快照时点逐目标走现有校验链，离线/未暴露目标标 skipped 不阻塞其余；批次记录入快照；GET /api/v1/control-operations/{batch_id} 查询逐项状态

### 工作单（P5 W1-W2，RFC-work-resume；端点为 MCP 传输载体与 W3 管理台只读数据源）

- `POST /api/v1/works {"goal","steps":[{desc,target_node,target_agent,depends_on?}],"prime_term?","created_by?"}` → 201 `{"work":{...},"pruned_before_events":0}`；同 goal+steps 内容指纹幂等命中 200 + `dup:true` 返同单（响应丢失重试安全；指纹即寻址键，无 409 冲突路径）。步骤计划不合法（引用越界/自引用/重复依赖/环/深度>4/级联>8/步骤数>64/字段缺失）400 `invalid_work_steps`；goal 缺失 400 `invalid_request`。created_by 缺省 mcp（建单入口 v1 仅 Prime/MCP，六.六裁决 1）；步骤 seq 由 hub 按声明序赋 1..N。建单单事务原子（事实+work_created 事件同事务），无泛化 PUT/DELETE
- `POST /api/v1/works/{id}/takeover {"expected_term","new_term"}` → 200 `{"work":{...},"dup"?}`（P5 W2 原子接管，CAS 单事务）：expected_term 与落库任期不符 409 `work_term_conflict`（同刻仅一方接管成功）；相同操作重放（expected==current==new）200+`dup:true` 返现状零事件；终态单 409 `work_closed`；new_term 缺失 400。接管后旧任期绑定派发被拒、其在途任务结果回流照收（事实不拒）
- 绑定派发（P5 W2，走 `POST /api/v1/tasks` 携 `work_ref`+`step_seq`+`prime_term` 三参）：任务落库+尝试登记+步骤回写+work 状态推进+attempt_recorded 事件**单事务原子**（绑定校验失败整体拒绝零发布；盖章后按盖章体重检体积与容量——绑定字段计入口径，任何失败 mutate 错误整体回滚）；响应附 `work:{work_id,step_seq,attempt_seq,attempt_ref,outcome,task_gone?}`（活任务 dup 重放同形附原尝试定位）。三参成组（缺 `work_ref` 只带其余两参 → 400 `invalid_request`，core 入口统一把关，不静默落为普通任务）。错误码：404 `work_not_found`、409 `work_term_conflict`（旧任期写入拒绝）、409 `work_no_term`（未建立任期须先接管）、409 `work_step_locked`（done/skipped 步骤再派）、409 `conflict`（同键不同内容/绑定）。幂等语义：同键同内容重放 dup 命中原任务不新增尝试；换 work/换步骤/换内容同键 409（绑定与内容入指纹，区别于群引用「换群=新任务」）；prime_term 不入指纹（换任期同键重放命中 dup）；任务终态保留期清理后同键重放命中**工作单去重证据**（尝试登记落盘的提交内容指纹 ContentHash/BaseContentHash，含提交主体=身份边界——知 work+键+步骤的他方不得冒领原 task_id；跨 Work 键复用由证据指纹兜底 409）不重新执行，响应 `task_gone:true`；回执补拉先于依赖准入与容量检查（依赖已清理/信箱与全局预算满不挡回执）
- 终态回写（P5 W2）：任务首落终态（result/cancel/sweep 三路径）同事务回写 `attempt.outcome`+`step.status`（映射：completed→done/done；failed→failed/failed；needs_review→needs_review/needs_review；cancelled→cancelled/failed），非 done 终局 work.status=blocked；枚举在 RFC 二.1 基础上扩展 needs_review/cancelled（SPEC 4.8 不折叠+任务清理后可解释终局），superseded 预留。「任务已终态而尝试未回填」窗口由启动恢复扫描补记（hub 重启自愈，恢复事件可查）
- 收集/关闭（P5 W3；审查修订 2026-10-25）：`POST /api/v1/works/{id}/collect {"prime_term","report","outcome"?}` → 200 `{"work":{...},"dup"?}`（单事务终态闭合）。校验序：report 必填（空 400 `invalid_request`）→ 404 `work_not_found` → 终态回执幂等（prime_term/report/outcome 与落库三同源 → 200+`dup:true` 零写入零事件，响应丢失重试安全；任一不同 → 409 `work_closed`）→ 409 `work_no_term`（先接管）→ 409 `work_term_conflict`（收集是编排写入，旧任期拒绝）→ 409 `work_steps_open`（**收束门：全部步骤终态且全部尝试收束才可收集**——W2 允许同步骤并发尝试，步骤呈 done 不代表执行收束，在途尝试同样拒绝并指明尝试号；outcome=abandoned 下 pending 不算开步，**未派发步骤随关闭同事务记为 skipped**——needs_review 阻断禁重跑/上游失败未补救时下游 pending 永不到终态，放弃剩余步骤是唯一出口）→ 409 `work_steps_not_done`（outcome 缺省/done 要求全部步骤 done，存在 failed/needs_review/skipped → 拒绝并列出，**部分报告不误关闭**；指路 outcome=abandoned）。成功：status 落 done/abandoned、closed_at/expires_at=now/now+30d、report 落库、collect 事件（by=自报任期、principal=服务端主体；abandoned 附跳过步骤 step_updated 事件）；**关闭后迟到的任务结果不回写**（终态回写与恢复扫描对 done/abandoned 单零写入——防御迟到/异常数据改写 30 天存档）；blocked 单恒有出口（续派补救→done 或处置 abandoned——无死环）；终态后派发/接管/收集三写入口全闭（`work_closed`）；skipped 不计入完成关闭。collecting 状态保留枚举、V0 无写入路径（collect 单事务直达终态）；superseded 仍无写入触发器
- 读档注解（P5 W3）：`GET /api/v1/works/{id}` 响应 work 对象增 `tasks_gone`（尝试绑定任务已过 24h 保留期清理的 ID 列表；仅读档路径计算，写入路径响应不携带）——任务清理后工作单仍是可解释执行记忆（步骤/尝试 outcome+幂等键+内容指纹/事件/报告全量在）；列表摘要增 `done_steps`（完成步骤数）
- 接管语义：接管为 CAS（expected_term 代校验）+ takeover 事件入账（by=新任期自报、principal=服务端验证主体分开存储）；原请求重放（expected=旧任期、new=当前任期且账本最近一条接管事件同源同主体）→ 200+`dup:true` 成功回执零写入，他方同参写入按 409 不误认 dup；任期标签为调用方自选字符串，**历史使用过的标签不得复用**（409，防 ABA 旧任期重新放行——接管须换新标签）；绑定派发须带 prime_term 且与落库任期严格相等（事件 by=自报任期、principal=提交链服务端已验证主体——REST 提交=鉴权中间件解析主体（admin 会话/`pat:<令牌名>`）、node 帧=会话 node_id）；恢复扫描按任务终态落盘顺序补写（同步骤多尝试以最后事实为准，与活路径 last-writer-wins 同序）
- `GET /api/v1/works` → 200 `{works:[{id,goal,prime_term?,status,steps,attempts,created_at,closed_at?}]}`（摘要，新建在前）
- `GET /api/v1/works/{id}?since_events=N` → 200 `{"work":{含 steps/attempts/events},"pruned_before_events"}`：事件只返 seq>N 升序增量，`pruned_before_events` 为滚动裁剪已丢事件最大 seq（现存最旧 Seq-1，同群消息 `pruned_before_seq` 口径；事件滚动保留最新 200 条，EventSeq 单调不回退）；未知 id 404 `work_not_found`
- 事件主体双字段（RFC-work-resume 六.2 裁决）：事件条目 `by` 为**请求自报**审计标签（created_by，缺省 mcp，逻辑归属非身份证明）；`principal` 为**服务端验证鉴权主体**（鉴权中间件取得：admin 会话 → `admin`、Bearer PAT → `pat:<令牌名>`），两字段分开存储——请求自报身份不作数，不得覆盖 principal；主体随事件同事务持久化
- 存储形态：Work/WorkIdems 随 hub_state.json 快照持久化（旧快照无该字段加载时归一迁移）；未完成 Work 不被 Sweep 清理；**终态 Work 保留 30 天自关闭起算（P5 W3 落实）**：Sweep 第六阶段对 done/abandoned 过 expires_at 整单删除并同步清除 WorkIdems 指纹映射，到期后 404（无墓碑）
- 管理台工作单视图（P5 W3，hub 端独有；审查修订 2026-10-25）：`#/works` 列表（徽标/进度 done_steps/任期/创建/关闭）+ 详情抽屉（步骤表/尝试表/事件时间线/收集报告；接管/续派/处置三个写动作，**无手工建单**——建单入口 v1 仅 Prime/MCP）。抽屉走分区 diff 局部更新（复用「抽屉分区更新 shared block」泛化 names 参数，双端同改同验）；动作区一次建壳恒定节点、不在分区 diff 内（表单草稿/busy 状态驱动，阶段 D 必须修 3 同款纪律）；**写动作三重迟到防护**（响应先验抽屉身份、发送前作废在途读档、成功推进本地回写版本——迟到的旧读档不得回退写动作已上屏的新事实、不得清空他单表单）；**稳定 DOM**（属性先比后写；续派下拉签名收敛到「可派步骤集合+选中步骤状态」——未选中步骤状态变化不重建选项、选中步不变不重预填，用户草稿不被无关刷新冲掉）；续派=管理凭据直接编排（与 MCP 绑定派发同链同校验，无新增准入旁路）；node 控制台不提供（白名单见下条第六项）

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

### 双端 Web 展示一致性契约（2026-10-25 抽屉统一立，七条长期契约）

品牌资源（2026-10-26）：hub/node 的顶部品牌位、登录页、侧栏与 favicon 使用同一 `assets/brand/logo-mark.svg`，由共享 assets 包 go:embed；两端公开静态路径 `/logo.svg`、`/favicon.svg` 返回 SVG。它们不返回配置或凭据，不属于管理面 REST 能力，无新增 MCP 工具。更新后需重建二进制再走查。仓库首页横幅为 `assets/brand/logo-github.svg` / `logo-github-dark.svg`，与图标使用相同几何源。

> 背景：hub/node 控制台任务抽屉组装层漂移盘点（docs/TASK-drawer-sync.md 阶段 A，D1-D23+C1-C11）与
> Hana 阶段 B 终裁。md 渲染器/样式 shared block 已有逐字节锁（web_md_render_test.go），本节把锁面
> 扩到抽屉组装层的共用件；锁测试位于 internal/node/localapi（web_md_render_test.go、web_drawer_sync_test.go）。

> **一致范围 = 三层（2026-10-26 伦哥裁决，通知统一后升格为硬规）**：①**渲染层**——shared block
> 逐字节同文（下第 1 条）；②**操作逻辑层**——同类功能的弹层形态（居中 modal vs 右侧抽屉）、表单
> 字段结构与顺序、卡片在页面中的位置、交互流程（增删改查路径）必须同构，单端不得发明新交互形态；
> 操作逻辑层不进 shared block 锁（形态由代码结构承载），以「双端形态对照表（任务书逐项列明）+
> 实现后双端同组件对比截图走查」保障；③**文案层**——同义 UI 词典同源（类型徽标/hint/空态/按钮词）。
> 执行规则：实现某端组件前先读另一端同类组件对照，不允许「先做一端再做另一端时发现形态对不上」；
> 涉及双端的功能，任务书必须含双端形态对照表，走查必须含双端同组件对比截图；白名单外差异按漂移处理。

1. **共用件单一来源（shared block 逐字节锁）**：双端 app.js 的抽屉状态词典（STATES/ERRORS/TERMINAL/
   REVIEW_ACTIONS）、抽屉时间工具、事件流与错误通知构建、答复与日志区渲染、分区更新机制一律收进
   `===== … shared block =====` 标记块，两端逐字节同文，测试抽块比对——禁止单端私改共用件，改必双端同改同验。
2. **材质层级基准（e821bf8 语法，双端同构）**：最终答复白底浮起（主角）→ 诊断日志/任务输入收 details
   折叠（配角沉底）→ 中间结果灰底容器；区块次序两端一致：头部 → 状态介绍 → 失败原因（hub 独有）→
   最终答复 → 诊断日志 → 产物 → 任务输入（hub 独有）→ 时间线 → 人工核对（hub 独有）→ 归档/下一步。
3. **局部更新铁律（D22 硬验收）**：抽屉刷新禁止整层 innerHTML 重建——首次打开建壳（稳定分区 data-zone），
   此后刷新按分区内容 diff 局部写入：数据无变化不碰 DOM（details 开合/原文模式/草稿/焦点/滚动随未动
   分区自然保持）；分区替换时回放 details 开合态；迟到应答按抽屉会话序号丢弃；入场动画只随壳播放一次。
   交互分区（核对表单等）一次创建稳定节点 + 状态驱动更新：用户输入草稿与 busy 态一律不进分区模板串，
   busy 由状态驱动 disabled（阶段 D 终审必须修 3：草稿进模板串会使「输入→缓存与模板分叉→下轮刷新
   整区重建」，节点身份与 busy 全丢）。本地回写（核对登记/取消）使在途列表/单查旧快照失效（数据版本
   判定，任务本地回写版本号）：列表提交永不使任务回退到本地回写之前（阶段 D 必须修 1/2 乱序竞态）。
   「重建后恢复状态」「终态停轮询」不算合格终解（hub 核对记录可在终态后变化；node 账本终态不可变、
   review 不回流，终态停轮询为数据面事实而非替代方案）。
4. **时间口径（抽屉面）**：完整时间一律 `YYYY-MM-DD HH:mm:ss UTC`，经 Date 解析转 UTC 格式化（禁止
   ISO 串切片后硬贴 UTC 后缀）；零值（0001-01-01 开头）/非法（NaN）时刻为「无证据」：不生成时间线
   事件、展示位兜底「—」。存储/API/导出口径不动；表格/总览等非抽屉面的全站迁移另批（C4）。
5. **文案单一出处**：跨端同义动作同词（标准词「查看全部任务」）；needs_review 主文案以 hub 版为唯一
   出处；能力描述必须与端上真实能力一致（node 无核对登记端点，文案不得承诺本端登记，只如实指路 hub）；
   五类详情介绍使用一致的状态说明，双端共享内容保持一致。
6. **合理差异白名单**（不强求对称，入档即豁免）：hub 管理面独有——导出任务 JSON、取消任务、人工核对
   表单/记录、「任务输入（原样 JSON）」折叠区（任务指令分区已于 2026-10-26 提升为 shared block 双端同构，不再属白名单差异）、失败原因独立区、产物中继取回按钮、群组词典（GROUP_STATES 徽标兜底，hub
   群聊页专属，node badge 不取词）、**工作单视图（P5 W3：#/works 列表/详情抽屉/接管/续派/处置与
   WORK_STATES 词典，hub 编排面专属——node 无 hub 全网工作单数据源，node 控制台不提供）**、**派单页
   派发建议区（P5.1 D1：「获取派发建议/建议派给 X（本地决策模型）一键采纳/重新获取」建议四态——决策
   服务 hub 内嵌，node 无 hub 全网候选目录，node 派单页不提供）**；node 执行者独有——产物本机绝对路径指引、回表单重新派单、来源
   （本机派单/hub 下发）标注、抽屉/弹层关闭直清（无 hub 的 280ms 出场动画：node #layer 为任务/能力
   配置抽屉与通知编辑弹层共用的直清家族——通知编辑已于 2026-10-26 notify-polish 改动三改居中 modal
   与 hub 同形态，仅关闭生命周期仍直清，让位路径要求立即清场，未引入 hub layerGen 世代机制前豁免，
   阶段 D 建议修 8）。白名单外的新增差异按漂移处理。
   **任务筛选与导航壳（2026-10-26 nav-filter）**：hub 任务页「目标机器」筛选=select
   下拉入 fields 主筛选行，node「我的任务」来源筛选=chip 组入任务卡头部——属「基数
   决定形态」的合法差异（机器多值 vs 来源二值+全部），不豁免其他维度的同构要求；来源
   词典 ORIGIN_WORDS（`hub 下发 | 本机派单`）收进「抽屉展示词典 shared block」双端
   逐字节同文（渲染层/文案层单一起源）。**任务抽屉归档区期限说明按端上事实分叉**
   （2026-10-26 P0 事实纠错入档）：hub=「终态任务记录保留 24 小时，到期自动清理」
   （core/sweep.go TerminalRetention 真实清理），node=「本机账本保留此次记录（当前
   无自动清理）」（CleanupAcked 已实现未接入运行循环，SPEC 4.8）——同位文案按端上
   能力如实表达，不属漂移（web_drawer_sync_test 双端各自断言锁固化）。同批导航统一只动导航壳（词表/顺序/navnum
   格式/aria/workspace 行序）与筛选行，「同构」要求不据此扩大解释为任务页整页重做。
7. **变更纪律**：任何一端改动抽屉组装层或共用组件（badge/notice/时间线/产物行/日志区/时间工具），必须
   对照本节评估另一端并同步；一致性锁测试随改动同步更新，禁止删除断言以规避失败（旧接入断言同步更新、不双轨）；
   行为面（刷新保真/迟到防护/深层交互态）由 headless 三层走查覆盖（docs/prototypes 截图留档）。

## 六.六、MCP 服务（agent 统一入口，2026-10-21 立）

> 定位：MCP 与 REST 管理面、双端控制台同为**一等公民接口面**。此后所有面向管理面的新功能，
> 必须同步设计并交付对应的 MCP 工具（见本节「同步开发规则」）——禁止只写 HTTP 不管 MCP。

### 实现形态

- `cmd/swarmmind-mcp`：MCP stdio 服务，newline-delimited JSON-RPC 2.0 手写实现，
  **零第三方依赖**（对齐 SPEC 三依赖白名单哲学）；新进程、不在 hub/node 内嵌。
- 配置仅环境变量：`SWARM_HUB_URL`（管理面地址）、`SWARM_HUB_TOKEN`
  （`smsw_` PAT；admin_token 仅用于人工/CLI 登录换会话，不能用于此 Bearer 配置）；不读配置文件（MCP 客户端 env 注入为准）。
- 鉴权：全部调用走 `Authorization: Bearer <token>`，与 REST 同一令牌体系（六.五 PAT）。

### 工具面（与 REST 端点映射）

| MCP 工具 | REST 映射 | 说明 |
|---|---|---|
| swarmmind_nodes | GET /api/v1/nodes | 在线节点与负载 |
| swarmmind_agents | GET /api/v1/directory | 全网能力目录（可调用面） |
| swarmmind_submit | POST /api/v1/tasks | 派任务（submitted_by=mcp；支持 idem_key/trace_id/depends_on 透传，幂等重派、任期追踪与任务依赖 P4.3；work_ref/step_seq/prime_term 三参成组绑定工作单步骤，P5 W2——同键重试不新增尝试，任务清理后同键重放命中去重证据不重新执行） |
| swarmmind_status | GET /api/v1/tasks/{id} | 查状态与结果（不等待） |
| swarmmind_wait | （轮询 status） | 等到终态；needs_review 为终态并明确提示需人工核对 |
| swarmmind_artifact | GET /api/v1/tasks/{id}/artifacts/{i} | 取产物（文本回读，二进制提示走控制台） |
| swarmmind_tasks | GET /api/v1/tasks | 任务列表：正在跑/最近完成/失败一屏速览，支持按状态/节点/agent 筛选（P4⑤；REST V0 全量直出不解析 query、不分页，筛选与条数截断在 MCP 侧本地完成、请求恒裸路径；每行任务短标识+状态说明+节点/agent+提交时间+耗时或错误码，行尾附全号供 status/wait/artifact 寻址；hub 视角，node 模式不设） |
| swarmmind_decide | 外部 StartLux /v1/decisions（bamboonode/MLX 本地） | 分配决策查询：云端 L0 → 规则兜底 L2 自动降级，响应标 used_level（P4.1） |
| swarmmind_group_create | POST /api/v1/groups | 建协作群（目标+成员；返回 g- 群 ID，创建即 discussing；GROUP-RFC 六 MVP，P4/V1） |
| swarmmind_group_list | GET /api/v1/groups | 看全部协作群（目标/状态/成员/发言预算） |
| swarmmind_group_say | POST /api/v1/groups/{id}/messages | 群里发言（kind 缺省 user；agent 发言计入预算，用尽报 budget_exhausted；消息体 8KiB 上限） |
| swarmmind_group_messages | GET /api/v1/groups/{id}/messages?since | 拉群消息（since 增量补齐，seq 单调锚点；群消息滚动保留最新 200 条，响应 `pruned_before_seq` 标注已裁区间上界） |
| swarmmind_group_close | POST /api/v1/groups/{id}/close | 结束群（concluded 终态不可回退，重复关闭幂等；by 可选署名审计，缺省 mcp） |
| swarmmind_work_create | POST /api/v1/works | 创建工作单：把多步工作落成可断点接管的正式执行记录（P5 W1，RFC-work-resume）；步骤计划建单定死，同 goal+steps 指纹重复调用返回同单（响应丢失重试安全，201 新建/200 幂等命中） |
| swarmmind_work_get | GET /api/v1/works/{id}?since_events=N | 查看工作单详情：目标/步骤状态表/执行尝试/事件时间线（since_events 游标增量；事件滚动保留最新 200 条，`pruned_before_events` 标注已裁区间上界）。接管未完成工作前先读档 |
| swarmmind_work_list | GET /api/v1/works | 列出全部工作单：ID/目标/状态/步骤与尝试数（发现历史未完成工作） |
| swarmmind_work_takeover | POST /api/v1/works/{id}/takeover | 接管工作单（P5 W2 断点续管，CAS 单事务）：expected_term 代校验（同刻仅一方接管成功，后来者 409 work_term_conflict）；相同操作重放幂等返回现状不新增事件；旧任期派发即被拒、其在途任务结果照常回流。接管成功后经 swarmmind_submit 携 work_ref/step_seq/prime_term 续派 |
| swarmmind_work_collect | POST /api/v1/works/{id}/collect | 收集并关闭工作单（P5 W3，单事务终态闭合；审查修订：全部步骤终态**且全部尝试收束**才可收集（409 work_steps_open），abandoned 下未派发步骤随关闭记 skipped）；outcome 缺省/done 要求全部步骤 done，部分完成 hub 拒绝（409 work_steps_not_done，部分报告不误关闭），放弃剩余步骤用 outcome=abandoned；report 必填（任务清理后的执行摘要，随单保留 30 天）；相同请求重放幂等回执（三同源 200+dup，零事件）；终态后不可再派发/接管/收集（work_closed） |
| swarmmind_dispatch_suggest | POST /api/v1/dispatch-suggest | 派发建议查询（P5.1 D1，RFC-jev-dispatch）：给定任务目标，由 hub 决策服务（llm-as-jev，GLM）从网内在线能力（agent@node 候选，hub 目录组装）中推荐执行者，附模型置信度与归一化候选分布（不构成统计校准证明）；建议可覆盖非必经——链路故障（provider 不可用/超时）200 + suggestion:null + degraded:true + note 使用概括性错误类别（上游原文只进 hub 日志）；候选为空/模型判断均不适合 200 + suggestion:null + degraded:false（note 区分），不阻塞派单（hub 模式专属，node 模式不装配） |

### 语义约定

- `wait` 是 agent 派单的主路径：轮询至终态（completed/failed/needs_review），
  默认 300s 上限 1800s、间隔可配；超时返回当前状态而非报错（agent 可再等或转人工）。
- `needs_review` 是合法终态：MCP 结果必须明确「结果不确定，需人工核对」，
  禁止 agent 基于未核对结果下结论（对齐 4.8 语义）。
- 产物仅回读文本类（无 NUL 字节）；二进制提示走控制台取回。
- 错误统一为包含原因与处理指引的文本（isError=true），不向 agent 泄漏内部堆栈。

### 同步开发规则（强制）

1. **新功能入库检查单新增一条**：面向管理面的新 REST 能力，同一变更内必须：
   ① SPEC 本节表格补一行映射；② cmd/swarmmind-mcp 增补对应工具（含用途说明）；
   ③ MCP 层至少一条实测或单测。
2. 纯内部实现（调度/存储/协议内部）不适用本规则。
3. **人工管理专属豁免（2026-10-22）**：以下管理面端点不设 MCP 工具——
   ① `POST /api/v1/tasks/{id}/cancel`（任务取消）：取消是管理面的人工干预能力，暴露给 agent 会引入互删任务风险；agent 侧只能等待终态（wait 对 cancelled 即时返回并提示已取消）；
   ② `POST /api/v1/nodes/enroll-bundle`（节点接入部署包生成）：端点为管理端人工装机操作专属——生成的部署包内嵌节点 register_token 凭据，agent 无权获取凭据，部署包亦不经 agent 面分发，只对管理端会话开放。
   ③ `POST /api/v1/local/prime/declare`（node 网页指定统筹载体，2026-10-22）：本机主人人工操作专属（仅 local_token 可调）——指定即凭据授予（应答带新 prime_token 与 MCP 连接片段），暴露给 agent 面等于 agent 可自行换发统筹凭证，属提权通道；授权交接由本机主人在 node 控制台完成。t20261023-62a2 自动接线补充口径：指定/轮换后 node 合并写入被指定 agent 影子 home 内的宿主 MCP 配置，动作发生在 declare/rotate 这个主人人工操作的事务内、走主人授权，不构成 agent 面暴露（豁免结论不变）。
   ④ node 本机主人管理端点（2026-10-22 功能闭环修复轮补豁免）：`PUT /api/v1/local/config`、`POST /api/v1/local/agents/reload`、`POST /api/v1/local/restart`、`POST /api/v1/local/prime/rotate`、`POST /api/v1/local/setup/parse|apply|disconnect`——口径同 declare：本机主人专属管理操作（写 agents 表=执行权、整表落盘/重启/断开切形态、轮换交接统筹凭证），全部仅 local_token 可调，经 agent 面暴露即提权/破坏通道，正式豁免 MCP 映射；node MCP 六工具（本机 nodes/agents/submit/status/wait/artifact）已覆盖 agent 侧全部合法能力。
   ⑤ `GET /api/v1/nodes/enroll-info`（节点接入入口地址查询，2026-10-23 第六轮修复 2）：接入向导人工装机流程的配套元数据端点（与豁免 ② 同一人工流程）——返回 hub 自身节点接入入口地址（node_listen 推导，无凭据），供控制台向导预填 hub_url；agent 面无使用场景，豁免 MCP 映射。
   ⑥ `GET /api/v1/prime`、`PUT /api/v1/prime`（hub 网页指定统筹载体，2026-10-23）：管理员管理面操作（hub 控制台指定/撤销全网唯一统筹载体），统筹智能体自身无需读写该状态，豁免 MCP 映射。
   ⑦ `/api/v1/notify/*` 全组（通知渠道管理，2026-10-24 P4③ 控制台化升级）：人工专属管理面配置（凭据授予类）——渠道配置体内含明文凭证（飞书 webhook 地址、SMTP 口令、自定义头 token），agent 读写该面即等于可自行获取/篡改主人的通知出口与凭据（改渠道=把任务结果重定向到任意地址，属信息外带通道）；通知是「系统主动找人」的人工动线，agent 对通知配置无合法使用场景（agent 感知任务结果走 swarmmind_wait，与通知解耦）。管理台「设置 → 通知」为唯一配置入口；hub.yaml 旧 notify 段仅作一次性兼容导入。
   ⑧ `/api/v1/local/notify/*` 全组（node 本机通知渠道管理，standalone 补盲区，2026-10-24）：③④⑦ 三重口径叠加——本机主人人工操作专属（仅 local_token 可调，prime_token 编排凭证不放行）+ 凭据授予类（渠道配置体内含明文凭证，改渠道=把本机任务结果重定向到任意地址的信息外带通道）+ 通知是「系统主动找人」的人工动线，agent 感知任务结果走 node 模式 swarmmind_status/wait，与通知配置解耦。node 控制台「服务 → 通知」为唯一配置入口（含「同时通知」开关 settings 端点；2026-10-26 起三模式全装配，hub 侧 ⑦ 与 node 侧各管各的渠道面——hub 渠道归 hub 本机，node 渠道归 node 本机，互不重叠）。
   除本条显式豁免外，新管理面 REST 能力仍须同步 MCP 工具。
4. 工具命名固定前缀 `swarmmind_`；描述面向 agent 写「能做什么」，不写实现细节。
5. MCP 破坏性变更需在 SPEC 本节记录版本（protocolVersion 跟随 MCP 规范日期）。
6. **P4 任务租约同步检查声明（2026-10-23）**：任务租约（4.11）为协议内防写机制（Envelope fence_epoch 门禁 + Task.Lease 审计），结果路径无新增/变更 REST 管理面能力，MCP 工具面零变更——豁免映射表补行；任务查询工具返回体透传新增 `lease` 展示字段，不构成工具面变更。
7. **P4.3 任务依赖同步声明（2026-10-23，第一子批）**：任务依赖（4.12）为既有 REST 能力的参数扩展——`swarmmind_submit`（hub 模式）新增 `depends_on` 数组参数透传（上游 idem_key 列表；node 本机直执行模式不经过 hub 状态机，不支持该参数），本表 `swarmmind_submit` 行已同步；`swarmmind_status`/`swarmmind_wait` 返回体透传 `depends_on`/`blocked_reason`/`superseded_by` 展示字段（含 blocked/upstream_failed/superseded 的状态说明），不构成新工具。依赖声明不合法经 submit 工具以 `invalid_dependency` 错误说明返回。
8. **P4 L2 产物挂载同步声明（2026-10-24，第二子批第一子轮）**：产物挂载（4.12「产物挂载」段）为 node 执行准备阶段的内部行为（spawn 前纯复制+SHA 校验+原子发布），无新增/变更 REST 管理面能力，MCP 工具面零变更——`swarmmind_artifact` 保留（跨机场景与逐件自取仍经它），不因自动挂载删除。任务 stdin 的依赖清单文案同步为告知挂载位置（`inputs/<上游task_id>/`），属 EffectiveInput 文本更新，不构成工具面变更。
9. **P4④ 跨机取回同步声明（2026-10-24，第二子轮）**：跨机取回（4.12「跨机取回」段）为 node→hub 协议帧扩展（dep_resolve 新增 + artifact_fetch 方向扩展 + hub 中继），无新增/变更 REST 管理面能力（hub HTTP 面零变更），MCP 工具面零变更、映射表不补行；`swarmmind_artifact` 保留（跨机自动取回是挂载准备阶段的内部行为，人工逐件自取仍走它）。任务 stdin 依赖清单尾注随能力更新为跨机自动取回说明（EffectiveInput 文本一次性变更，见 4.12 指纹口径第二次声明），不构成工具面变更。
10. **P5 Work 工作单同步声明（2026-10-25，W1 批）**：工作单（RFC-work-resume，执行事实与断点接管）W1 交付模型+建单幂等+只读查询，本表补 `swarmmind_work_create/get/list` 三行；REST `/api/v1/works*` 组为 MCP 传输载体与 W3 管理台只读视图的数据源，建单入口 v1 仅 Prime/MCP（开放问题裁决 1：管理台无手工建单）。三工具 hub 模式专属，node 模式不装配（不静默降级）。`swarmmind_work_collect/takeover`（收集/接管续派）随 W2/W3 交付同批补行，不预占工具名。Work 无泛化 PUT/DELETE（状态只由生命周期动作推进，RFC 四.8），不设 update/delete 工具。
11. **P5 Work W2 绑定派发与接管同步声明（2026-10-25，W2 批）**：原子接管+绑定提交+多次尝试+终态回写（RFC-work-resume W2 行）。本表补 `swarmmind_work_takeover` 一行；`swarmmind_submit` 行更新（work_ref/step_seq/prime_term 三参成组绑定派发，援引规则 7 参数扩展；step_seq 运行时严格校验 ≥1 整数不截断；node 本机直执行模式三参显式拒绝不静默）。绑定派发同键重试短路于幂等查重不新增尝试（任务 24h 清理后同键重放命中工作单去重证据，响应 task_gone 标注、不重新执行）；接管成功前/旧任期的绑定派发 409 work_term_conflict、未建立任期 409 work_no_term、完成步骤再派 409 work_step_locked。`swarmmind_status/wait` 返回体透传工作单绑定展示行（work_ref/work_step_seq/attempt_ref），不构成新工具。`swarmmind_work_collect`（收集/关闭）仍随 W3 同批补行。
12. **P5 Work W3 收集关闭+接口补全同步声明（2026-10-25，W3 批）**：收集/关闭+清理补全（RFC-work-resume W3 行）。本表补 `swarmmind_work_collect` 一行（hub-only，node 模式不装配）；`swarmmind_work_get` 返回体透传 `tasks_gone` 读档注解、`swarmmind_work_list` 透传 `done_steps`（展示字段，不构成新工具）；工具输出与 REST 详情全量对齐（work_get 附收集报告/幂等键/尝试引用/双指纹/盖章时刻，任务清理后 agent 经 MCP 读得回执行摘要与原键）。收集/关闭/清理契约见六.五「工作单」节（回执幂等、部分报告不误关闭、终态三写入口全闭、Sweep 30 天保留清理）。Prime 提示词同批增补「工作单（多步工作的断点续管）」节（internal/hub/web/app.js PRIME_PROMPT 与 deploy/prime/PRIME.md 两处一致，web 包测试锁工具名引用 ⊆ 实际装配集+双处同文；幂等键规范含工作单命名空间维度与接管重放口径）——提示词为行为约定，不构成接口面变更。管理台工作单视图为 hub 控制台人工操作面（六.五白名单入档），无 MCP 对应物（MCP 工具面即 agent 侧全部合法能力）。
13. **P5.1 D1 派发建议同步声明（2026-10-26）**：`POST /api/v1/dispatch-suggest` 与 `swarmmind_dispatch_suggest` 同批交付，hub 模式专属。当前 D1 provider 为 llm-as-jev，使用配置的 LLM 服务；本地 provider 和群聊自动派发未实现。请求提供 goal/step_desc，候选由 hub 的在线且开放能力目录构造。未启用服务：公共 REST 返回 HTTP 200 + `suggestion:null` + `degraded:true`；无候选或模型判断均不适合：200 + `suggestion:null` + `degraded:false`，note 说明原因；已启用 provider 的请求/解析/超时/越权目标失败：200 + `suggestion:null` + `degraded:true`。仅请求体非法 400，鉴权失败 401；无建议不影响普通任务提交。上游原始错误只进入日志，响应返回概括类别。概率覆盖全部候选并归一化，缺失值按规则补齐，不代表已证明统计校准或实际成功率。密钥来源依次为配置、GLM_API_KEY 与 Prime HOME provider 配置。管理台采纳只回填目标选择，描述变化后建议失效；当前固定来源标签「本地决策模型」不能当作本地部署证明。真实模型终版逐场景复验待补录，详见 TESTPLAN-D1-jev。

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
