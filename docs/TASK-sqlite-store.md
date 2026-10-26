# TASK-sqlite-store：hub 持久层迁移 SQLite + node 取消墓碑缺陷修复

> 状态：进行中 | 开工：2026-10-26 | 分支：sqlite-store（基于 dev/p5-work-resume @ 038e049）
> 背景审查：2026-10-26 存储架构双审（Hana + codex），审查记录要点见本文「缺陷修复」节。

## 一、背景与动机

hub 持久层现为 JSON 全量快照（`internal/hub/store`，SPEC 4.7）：每次事务序列化整个 State 写盘 + rename。双审结论：当前负载可用，但存在结构性边界——写放大 O(全量状态)、掉电耐久性不承诺、容量演进受限（10MiB 级快照仅 sweep 空转即每天数百 GiB 逻辑写入）。伦哥拍板提前迁移 SQLite。

## 二、目标与范围

### 范围内
1. **hub store 迁 SQLite**（`internal/hub/store`）：持久层换 SQLite 单库，对外接口与事务语义零变化。
2. **node 取消墓碑缺陷修复**（正确性必修，见第四节）。
3. **JSON→SQLite 自动迁移导入**：旧部署无缝升级。
4. **SPEC 同步修订**：4.7 持久化事务、三依赖白名单、部署注记。

### 范围外（明确不做）
- node ledger 整体迁 SQLite（node 账本为 KB 级、无容量压力，保持 JSON；该裁决理由：一次迁移两侧都动风险翻倍，node 病是纪律病不是容量病，修缺陷即可）。
- SQL 列展开 schema（调用方全部走内存 View 遍历，无 SQL 查询需求，YAGNI）。
- 双写/在线迁移（采用停机切换，无双写恢复复杂度）。

## 三、hub store 迁移方案（硬约束，逐条遵守）

1. **对外 API 不变**：`Open(dir) / View() / Update(mutate) / Close()` 签名与语义全部保持。先写后见：mutate 在深拷贝副本上执行 → 持久化成功 → 才原子发布内存并 Ack；任一步失败丢弃副本、不发布、不 Ack。rename 后失败 = 已提交但确认丢失的语义声明保持（现有注释与注入点结构保留适配）。
2. **写时复制纪律不变**：State.Clone 深拷贝、View 零拷贝只读契约、条目整体替换禁止原地改——全部原样保留。View 语义不变意味着 core/api 全部调用方零改动（这是本方案的立意：渐进替换持久层，不激进修剪内存模型）。
3. **flock 单实例锁不变**（`store/lock.go` / `lock_windows.go` 不动）。
4. **驱动：modernc.org/sqlite**（纯 Go 无 cgo，保 Windows 构建与交叉编译；依赖树大属预期，SPEC 白名单注释写明理由）。
5. **SQLite 配置**：WAL 模式 + `synchronous=FULL` + `busy_timeout`；连接池 `SetMaxOpenConns(1)`（与单写 goroutine 模型对齐，SQLite 单写者天然吻合）。WAL 需本地盘——写进 SPEC 部署注记（生产 hub 为本地盘，满足）。
6. **Schema：实体分表 + JSON 条目体列**。九类状态各一张表（nodes / tasks / mailboxes / batches / batch_idems / workdir_locks / works / work_idems / prime），行 = key + JSON 体。主键即 map key。schema 版本表（user_version 或独立表）预留演进。
7. **diff 写入**：Update 后对比 old/new（map key 集合差 + 变 key 条目深度相等比较），仅 UPSERT/DELETE 变化条目，单事务提交。**禁止全表重写**。条目相等比较用 JSON 序列化比较即可（量级小，成本可忽略），但须处理 map 遍历序稳定（marshal 前不必排序，JSON 对象键序 Go 稳定）。
8. **diff 为空跳过写库**：无变化事务零磁盘写（顺带消灭 sweep 空转重写问题）。发布内存照常（幂等事务发布无变化状态是安全操作）。注意：无变化时仍走「发布 + Ack」，只是不触盘。
9. **迁移导入**：`Open` 时检测——SQLite 库为新空库且 `hub_state.json` 存在 → 单事务导入全部状态 → 将 json 改名 `hub_state.json.imported-<unix秒>` 备份 → 正常启动。导入失败：保留 json 原样、启动报错退出。非空 SQLite 库 + json 并存 → 以 SQLite 为准（不重复导入），json 不动。`.tmp` 残留文件忽略（JSON 时代产物）。
10. **内存仍驻留全量 State**：SQLite 只是持久层，View 返回内存镜像。启动时从 SQLite 全量加载重建 State。

## 四、node 侧缺陷修复（2026-10-26 双审实锤，必修）

### 缺陷 1：`internal/node/ledger/cancels.go` 违反先写后见
- 现状：`add`/`clear` 先改内存 map 再 `persistLocked()`，persist 失败不回滚。`add` 重试时命中内存直接返回 nil，永久跳过写盘 → 重启后墓碑丢失 → 迟到 offer 重新执行已取消任务。
- 修法：对齐 store 包纪律——持锁下构造新 map 副本（或记录待应用变更）→ persist 成功 → 才替换/提交内存状态。`clear` 同理（失败时内存墓碑仍在，语义为「未清除成功」，重试可再清）。
- 测试：复用 ledger 包现有 failure injection 机制，断言 persist 失败后（a）内存状态未污染（has/clear 语义正确）（b）同键重试不再假成功（会再次尝试写盘）（c）注入解除后重试成功落盘。

### 缺陷 2：`internal/node/service/cancel.go` 落盘失败仍发取消确认
- 现状：两处（约 164/192 行附近）`Ledger.MarkFinished`/墓碑登记报错后仅记日志，仍发送 `MsgTaskCancelled` 成功确认 → hub 认为取消闭环，node 侧却无持久化保障。
- 修法：落盘失败路径不发成功确认，只记日志后返回/跳过发送；未闭合状态由既有重发与对账机制兜底（cancel.go 现有注释描述了该兜底链路，保持一致）。
- 测试：注入落盘失败，断言不发送 MsgTaskCancelled；解除注入后对账重发路径闭环。

## 五、测试与验收（上线纪律：一项不绿不交付）

1. `go build ./...`、`go vet ./...` 全过。
2. `internal/hub/store` 既有测试全部适配迁移后全绿：事务语义（clone 隔离/写时复制）、锁互斥、work round-trip、旧快照兼容测试改为「旧 JSON 导入兼容」。
3. 新增测试：
   - 导入：JSON→SQLite 后 View 状态逐字段等价；json 已备份改名；重启不重复导入；损坏 json 拒绝导入且不删原文件。
   - diff 写入：连续 Update 仅变化行触写（SQLite 侧 hook/计数验证）；无变化 Update 零写。
   - 掉电语义：commit 后重开库数据完整（WAL 恢复路径）。
   - 事务失败：mutate 报错/写库报错 → 不发布不 Ack（沿用现有注入点风格）。
   - 第四节两缺陷的回归测试。
4. 全仓 `go test ./...` 全绿（本 worktree 基线干净；主工作区 dev 线通知测试红与本任务无关，勿混淆）。
5. 实测：本地起 hub（临时 data_dir）→ 用 swarmmindctl 或直接 API 提交 2-3 个任务走完 提交→派发→结果→终态清扫 链路 → kill 后重启 hub 状态恢复 → 检查 .db 文件存在、旧 json 已备份、WAL 文件在。
6. node 侧：现有 ledger/cancel 测试全绿 + 新增回归测试绿。

## 六、SPEC 修订要求

- 4.7「持久化事务」：改写为 SQLite 事务语义（保留先写后见声明；更新掉电耐久性表述——WAL+FULL 后进程崩溃不丢已确认提交，主机掉电仍以 fsync 语义为准，如实声明不夸大）。
- 依赖白名单：登记 modernc.org/sqlite + 理由（纯 Go 无 cgo、单文件嵌入零部署、WAL 满足持久化升级需求）。
- 4.9 数据结构 Schema：表结构描述（key + JSON 体列）。
- 部署注记：WAL 需本地盘；首次启动自动导入旧 json 的行为；备份口径从「拷 json」变为「拷 .db（建议 checkpoint 后）」。
- node 侧 4.8 不动（存储仍 JSON），仅补取消墓碑持久化语义修正说明。

## 七、已知陷阱

- modernc.org/sqlite 依赖树大，go.mod 会显著膨胀——预期内，勿试图精简。
- Windows 无 flock：`lock_windows.go` 已有同语义实现，本任务锁层零改动。
- `store` 包测试用 `t.TempDir()`，Windows 路径分隔符相关断言勿引入。
- embed 资产/前端本任务不涉及，无需重建二进制套路。
- 全量测试基线：本 worktree 应全绿（基于 038e049 干净检出）；若发现基线本身就红（通知相关），如实记录并继续本任务范围，不越界修 dev 线问题。

## 八、交付物

1. 代码 + 全部测试绿（第五节逐项）。
2. SPEC.md 修订（第六节逐项）。
3. `docs/REVIEW-sqlite-store.md`：实现摘要、关键裁决记录（含 node 不迁移的理由）、遗留问题。
4. 向 Hana 回报改动摘要（文件清单 + 测试结果 + 实测记录），commit 由伦哥最终确认。
