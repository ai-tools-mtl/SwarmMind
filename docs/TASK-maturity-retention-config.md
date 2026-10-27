# TASK-maturity-retention-config：结果留存可配置 + node 清理接线收口

## 背景（走查双 P0 + 既有已知差异）

1. 用户走查双 P0 之首：任务列表页「终态任务保留 24 小时」硬编码，自托管用户最不缺磁盘却被强制 24h 丢结果；导出仅手动单任务 JSON；保留期纯常量无配置（`internal/protocol/constants.go:29` TerminalRetention=24h）。
2. SPEC 4.8 已知差异：node 清理函数 `CleanupAcked`（ledger.go:470）**从未接入运行循环**——「半成品中间态」，OPERATIONS 与代码注释均自曝未接线。
3. 前端保留期文案写死散布 hub app.js 至少 7 处（任务列表 footer:1613、归档 toast:1647、抽屉:1852、策略容量:2312/2316、撤机器弹窗:3360、toast:3498），配置化后全部须数据驱动。

## 方案设计（实施按此）

### 配置形态

- hub.yaml 与 node.yaml 各新增：`terminal_retention: 24h`（字符串 duration，`time.ParseDuration` 语法：`30m`/`24h`/`72h`/`720h`）
- **`0s` = 永不自动清理**（显式语义，文档写明）
- 非法值（无单位、负数、无法解析）：**拒绝启动**并给人话报错（对齐 insecure_loopback 的严格风格，不静默回退）
- 缺省 = 24h（现状兼容，老配置文件零改动行为不变）

### hub 侧

- hubConfig 增 `TerminalRetention string \`yaml:"terminal_retention"\``，loadConfig 解析为 time.Duration 注入 Core（Core 现用 protocol 常量处改为配置值；protocol 常量保留为默认值语义）
- Sweep 第 5 阶段终态清理用注入值（`c.now()` 已可注入，测试友好）
- 管理面暴露生效值：在既有合适端点（如 settings/info 类；实施时按现状选最小面）返回 `terminal_retention` 实际值，供前端渲染

### node 侧

- nodeConfig 增同名字段，解析同规则
- **CleanupAcked 接入运行循环**：service 运行循环新增周期 ticker（建议 1 分钟固定周期，简单正确）调用 `ledger.CleanupAcked(now)`；清理语义不动：仅清 `result_acked` 且超保留期的条目（result_acked 前不清——结果重发链路与去重证据依赖账本，既有红线不得放宽）
- 本机 status 端点暴露生效值

### 前端（hub 单端 UI；node 控制台无保留期文案，不涉及双端锁辖区）

- 上述 7 处写死文案全部改数据驱动：从 API 取实际 retention 渲染（格式化 `24 小时`/`30 分钟`/`不自动清理`）
- 策略容量页「终态 / 产物保留」行显示实际配置（区分 hub 终态记录保留与产物文件留存语义——既有 OPERATIONS 口径）
- **勿误伤**：app.js:956「有效（24h 内）」是会话有效期，非任务保留期，不改
- 文案语义对齐混跑口径教训：显示的是「本 hub 配置值」，如实呈现

### SPEC 与文档

- SPEC 4.6/4.8 改写：保留期可配置、0=禁用、node 清理已接线（消除「未接入」已知差异）、去重窗口=保留期窗口的语义声明（SPEC 4.5 idem 与任务同生命周期——**保留期即幂等去重窗口**，延长保留=延长去重，清理后同键重提返回新任务属设计语义）
- configs/hub.example.yaml、node.example.yaml 补字段注释（含 0s 语义与去重窗口警示）
- OPERATIONS.md 运行限制表「hub 终态任务保留」「node 执行账本」两行更新为「默认 24h，可配置」

## 双端形态对照表

| 项 | hub 管理台 | node 控制台 |
|---|---|---|
| 保留期文案 | 任务列表/抽屉/策略容量等 7 处数据驱动 | 无既有文案不新增（node 无任务保留期展示面） |
| 配置来源 | hub.yaml terminal_retention | node.yaml terminal_retention |
| status 暴露 | 管理面端点 | 本机 status 端点 |
| shared block | 不触及（无词典/抽屉机制改动） | 不触及 |

## 验收 DoD（逐条可执行）

1. hub 短值实测：`terminal_retention: 1m` 起临时 hub → 提交任务至终态 → 用时间注入或真实等待超 1m → Sweep 后任务被清理（查询 404/列表消失）；node 完整走 ack → 账本条目同窗口清理。
2. node 短值实测：node 配 1m + 任务完成且 result_acked → 超窗后 `CleanupAcked` 生效账本条目清除；**result_acked 之前的条目即使超窗也不清**（构造未 ack 条目验证红线）。
3. `0s` 双侧实测：超窗后任务/账本条目仍在（永不清理）。
4. 默认兼容：不配置时行为=现状 24h（老配置文件零改动）。
5. 去重语义：终态任务清理前同键重提 → 200+dup 原任务；清理后同键重提 → 新任务（非 dup）——证明去重窗口=保留期窗口的设计语义成立（文档已声明，非缺陷）。
6. 非法值拒启：`terminal_retention: abc` 与 `-1h` 均拒绝启动、报错说人话（含合法示例）。
7. 前端数据驱动：配置 1m 时任务列表 footer/抽屉/策略容量页显示「1 分钟」；0s 显示「不自动清理」；**grep 全仓确认无残留写死「保留 24 小时」文案**（app.js:956 会话有效期除外）；截图留证。
8. 工程验收：`go build/vet/test ./...` 全绿 + Sweep/CleanupAcked 参数化新测试（时间注入，不真实 sleep）+ `TestWebMdRender|TestWebDrawer` 双绿 + 断链零 + 零新依赖。

## 已知陷阱

- **去重红线**：CleanupAcked 只清 result_acked 条目（SPEC 4.6/4.8 既有语义）；不得改为按创建时间清理，否则结果重发链路与去重证据被破坏。
- 时间注入：测试一律用 now 注入/短 retention + 真实短等待，禁止长 sleep。
- duration 解析边界：字面 `"0"` 免单位解析成功（=永不清理，与 0s 等价）；负数显式拒绝；报错文案指路 0s。
- 前端 7 处文案逐处数据驱动，防漏改（完工后 grep「保留 24 小时」应仅剩 0 命中或注释）；app.js:956 会话有效期勿动。
- Core 构造签名如需变更，注意全部调用点（含测试 fixture）同步；protocol 常量保留默认值供 node/测试引用。
- node ticker 频率 1 分钟固定即可，不要做成可配置（避免配置面膨胀）；周期到点但账本无超期条目时零写盘（既有 CleanupAcked 行为，不引入无谓 IO）。
- 版本纪律：不 commit；最终输出列改动文件清单与 DoD 逐项结果。
