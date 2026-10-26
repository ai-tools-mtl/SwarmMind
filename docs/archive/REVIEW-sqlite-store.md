# REVIEW-sqlite-store：hub 持久层迁移 SQLite + node 取消墓碑缺陷修复

> 分支：sqlite-store（基于 dev/p5-work-resume @ 038e049）｜完成：2026-10-26
> 任务书：docs/TASK-sqlite-store.md｜编写：Hana（zcode 两轮无头任务静默中断，按协作制度判定不可用降级 Hana 编写；codex 已完成前置架构审查）
> codex 复审：已回，意见全采纳修复（见第五节）；修复后全仓 21 测试包逐包全绿 + race 三包全绿 + node 全链实测闭环

## 一、实现摘要

### hub store 迁 SQLite（internal/hub/store）
- 新增 `sqlite.go`：九表 schema（key TEXT PRIMARY KEY + body JSON 体），`user_version` 盖章（高版本拒绝启动）；WAL + synchronous=FULL + busy_timeout(5000) 经 DSN _pragma 生效；`SetMaxOpenConns(1)` 与单写 goroutine 对齐。
- `transaction.go` 重写持久化半步：Update 后 `diffState`（key 集合差 + 变 key 条目 JSON 相等比较）产出 UPSERT/DELETE 增量集，单事务提交；**diff 为空零写库**。四阶段故障注入点语义保留（after_rename = COMMIT 后）。
- 首启自动导入：新空库 + 存在 hub_state.json → 单事务「建表+导入+盖版本」→ 成功后 json 备份改名 `hub_state.json.imported-<unix秒>`；失败保留 json 报错退出。已初始化库以 SQLite 为准。
- `ReadSnapshot(dir)` 导出只读快照（独立连接，测试与运维自查用）。
- 对外 API（Open/View/Update/Close）与先写后见语义零变化；内存仍驻留全量 State；flock 不动；core/api 调用方零改动。

### node 取消墓碑缺陷修复（2026-10-26 双审实锤）
1. `ledger/cancels.go`：add/clear/cleanup 改副本式提交（副本构造 → persist 成功 → 替换内存），persist 失败内存不污染、同键重试不再假成功；新增 `pruneCloneLocked` 纯函数与 `persistMapLocked(op, m)`；注入钩子（op+stage，对齐 Ledger.inject 风格）。
2. `service/cancel.go` 两处：
   - 墓碑落盘失败不再发 task_cancelled ack（闭环确认必须以持久化为前提，hub 至少一次取消重发兜底重试）；
   - MarkFinished 失败区分错误：ErrWrongState（先到终态为准）保留幂等 ack，IO 落盘失败不发任何确认（旧实现两错混发 ack）。

### SPEC 修订
- 依赖白名单：登记 modernc.org/sqlite（纯 Go 无 cgo / 单文件零部署 / WAL+FULL 掉电耐久性升级；间接依赖树大属已知代价）。go.mod 同步登记理由注释。
- 4.7 改写：SQLite 存储形态/增量事务/掉电耐久性如实声明（进程崩溃不丢已确认提交；主机掉电以 fsync 语义为准）/停机切换导入流程/内存模型不变声明/WAL 本地盘部署注记/备份口径（.db + .backup 或停机拷）；node ledger 保持 JSON 的裁决与取消墓碑先写后见修正、取消链路确认语义修正。

## 二、关键裁决记录

| 裁决 | 理由 |
|---|---|
| hub 迁 SQLite、node 保持 JSON | node 账本 KB 级无容量压力；一次迁移两侧都动风险翻倍；node 的问题是纪律病（先写后见违反）不是容量病，修缺陷即可。审查演进路径（codex）同建议 |
| 条目体 JSON 列而非 SQL 列展开 | 调用方全走内存 View 遍历，无 SQL 查询需求；JSON 列保结构演进灵活性（YAGNI） |
| diff 空=零写库但仍发布+Ack | 幂等事务发布无变化状态安全；顺带消灭 JSON 时代 sweep 每秒空转全量重写（10MiB 状态≈844GiB/天逻辑写入） |
| rename 后失败语义平移到 COMMIT 后 | 注入点阶段名沿用（after_rename），「已提交但确认丢失，重启以磁盘为准」契约不变 |
| schema 版本高即拒绝启动 | 旧代码误读新 schema 比拒启危险；诚实失败 |
| 预置终态任务导入后被 sweep 清理 | 实测观察到 t-legacy-1（updated_at 超 24h）导入后即被清扫——既有终态保留策略（SPEC 4.3/24h）正常工作，非缺陷 |

## 三、测试与验收结果（任务书第五节逐项）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | go build ./... + go vet ./... | ✅ 全过 |
| 2 | store 既有测试适配后全绿 | ✅ transaction_test/work_test/lock_test 适配完成，包内 ok |
| 3a | 导入测试（等价性/备份改名/不重复导入/损坏拒导） | ✅ sqlite_test.go + TestWorkOldSnapshotCompatible（JSON 导入口径改写） |
| 3b | diff 写入/无变化零写 | ✅ sqlite_test.go 覆盖 |
| 3c | 掉电语义（commit 后重开库恢复） | ✅ sqlite_test.go 覆盖 |
| 3d | 事务失败不发布不 Ack | ✅ 既有注入点测试适配通过 |
| 3e | cancels 缺陷回归 ×3（add 假成功/clear 保留/cleanup 不动） | ✅ cancels_test.go 新增 |
| 3f | cancel service 确认语义回归 ×2（墓碑失败不发 ack/终态失败不发 ack） | ✅ cancel_persist_fail_test.go 新增（OpenForTest 注入） |
| 4 | 全仓 go test ./... | ✅ 20 包 ok（另 cmd×2 无测试文件） |
| 5 | 本地实测 | ✅ 见下 |
| 6 | -race 关键三包（store/ledger/service） | ✅ 全绿 |

### 本地实测记录
1. build hub → 预置旧版 hub_state.json（2 任务+1 节点+信箱）→ 首启：hub_state.db 生成（含 -wal/-shm，WAL 确认）、json 备份改名 imported-1793002665、nodes/mailboxes 导入完整；超保留期的终态任务被启动 sweep 按既有策略清理（符合 SPEC）。
2. 管理面登录 + 提交 2 任务（POST /api/v1/tasks）→ sqlite 查得 tasks 行新增、状态 submitted。
3. kill hub → 重启 → API 查询：导入任务与新提交任务全部恢复（tasks_total=3，状态一致）。
4. sqlite3 直查表结构与行数核对一致。

## 四、遗留问题（不阻塞交付，如实登记）

1. node `CleanupAcked` 仍未接运行循环（既有状态，SPEC 4.8 已如实声明）；hub Batches 无独立清理路径（既有）——快照体积演进的两处增长点，容量门槛（10MiB）触达前应处理。
2. 目录 fsync 缺口（rename 后父目录未 fsync）：SPEC 已声明掉电语义以 fsync 为准，SQLite WAL+FULL 下提交路径不再经过 rename，本缺口仅剩导入备份改名一步，影响面可忽略。
3. 主工作区 dev 线通知测试红为已提交代码的独立问题（codex 全量测试时发现），不在本分支范围（本分支基线干净全绿）。
4. 生产部署时序：hub 二进制替换 + 重启即完成自动导入；回滚路径 = 停 hub → 用 imported-*.json 换回（SQLite 弃用）→ 旧二进制重启。

## 五、codex 复审意见与处置（2026-10-26 二轮，全部采纳）

| # | 级别 | 意见 | 处置 |
|---|---|---|---|
| 1 | 必须修 | core 两测试未适配（work_test 占位 JSON tmp 路径模拟写失败 / workdir_test 改旧 JSON 快照），全仓测试实际失败，「全绿」声明不成立 | 属实，认账（首轮全仓验证被输出截断误导）。work_test 改用 SetInjectForTest 注入写失败 + ReadSnapshot 读盘；workdir_test 的 stripWorkdirLocks 改 SQL DELETE 清互斥表；两测试复跑绿 |
| 2 | 必须修 | SQLite DSN 未转义 URI 保留字符（%/?#），路径含字面量 %41 时库路径与 flock 锁路径分离 | 新增 sqliteURIPath（%/？/# 转义）替换裸 ToSlash 拼接；补 TestSQLiteURIPathEscapedChars 回归 |
| 3 | 必须修 | ReadSnapshot 复用可创建库的 openDB 且逐表查询无读事务：跨表不一致 + 非只读 + 会创建空库 | 改为 mode=ro 只读 DSN + 单读事务（BeginTx + loadStateFromDB 参数化 dbQueryer）+ 库不存在报错不创建；补 TestReadSnapshotMissingDBNoCreate |
| 4 | 建议修 | after_rename/before_publish 注入失败后内存/磁盘基准分岔，后续事务 diff 以旧内存为基准会永久漂移（磁盘残留僵尸行）；JSON 全量覆写时代靠重写自然收敛，diff 模式下该语义缺口真实 | 采纳升级为必修。新增 ErrPoisoned 置毒机制：两阶段注入失败后拒绝后续事务（真实对应进程崩溃，不存在后续事务；测试中须重开 Store）；TestStoreInjectionStages 改造验证置毒+重开以磁盘为准；before_ack 不置毒（发布成功仅 Ack 丢失，后续事务合法） |
| 5 | 建议修 | Close 在 db.Close() 报错时跳过 flock 释放且 closeOnce 阻止重试 | 改为两步清理均执行 + errors.Join 合并错误 |
| 6 | 建议修 | 验收证据缺口：实测停在 submitted 未走全链；导入中途失败回滚、高版本拒启无专门测试 | 补齐：真 hub+node 回环组网实测（提交→派发→执行→回执，sqlite 落 status=completed/exit=0，node 账本 finished）；新增 TestSQLiteImportMidwayFailRollback（列不兼容表触发导入中途失败，验证整体回滚+json 保留+无备份）与 TestSQLiteFutureSchemaVersionRejected |

复审核实为「暂不通过」→ 全部修复后重验：build/vet 干净，全仓 21 测试包逐包全绿（无截断逐包核对），store/core/ledger -race 全绿，node 全链实测闭环。codex 复审同时确认：diff 九类状态覆盖完整、导入事务原子性设计正确、cancels 副本式提交无并发缺口、cancel.go 错误分流正确（可不修项全部通过）。

## 六、流程注记

- zcode 两轮无头任务均静默中断（第二轮续做推进 store 测试适配后再次中断，死时无 API 报错痕迹），按「判定不可用即降级」制度由 Hana 接管完成剩余全部工作；zcode 已完成部分（sqlite.go 主体/transaction.go 接线/测试适配初稿）质量良好，经审查后保留。
- codex 前置架构审查（2026-10-26）产出本任务的问题定义与演进路径建议；编码完成后 codex 复审意见另行追加本文件。
