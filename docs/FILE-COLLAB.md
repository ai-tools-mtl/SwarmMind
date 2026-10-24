# SwarmMind FILE-COLLAB — 文件协作与并发写语义计划（v2.0 FINAL，2026-10-21）

> 三方评审闭环：Hana 起草 → zcode（实现者）+ codex（架构）并行审查 → 本版融合全部意见定稿。
> 分流：L1 进 SPEC/P1.5、L2 进 GROUP-RFC/P4、L3 进 PLAN/P5。

## 一、问题定义（不变）

三个层次：①同机同 workdir 并发写 ②群协作文件交接 ③并行开发与合并。立场：不发明协同编辑；写权限单点化 + 产物链 + Git 合流。

## 二、L1：Workdir 互斥（P1.5，2-3 人日）

**互斥键来源（终审修正采纳）**：ExecProfile 永不出本机，hub 无法知悉路径——**node 上报派生键**：node 启动时对每个 agent 计算 `workspace_id = sha256(规范化 workdir)`，加入 AgentCard（不泄露路径）。互斥键 = `(node_id, workspace_id)`。

- 路径规范化（node 侧执行）：`filepath.Abs` + `Clean` + `EvalSymlinks`（目录不存在则回退 clean+abs 并注记）；已知陷阱：macOS `/tmp`→`/private/tmp` 同目录双键、APFS 大小写不敏感盘 `/Repo` vs `/repo` 分裂、hardlink/bind mount 无法用路径识别（文档声明局限）——全部进 SPEC 九节已知陷阱
- 排队表达：**复用 submitted + eligible 门控**（不新增状态）：调度出队改为「跳过不 eligible、取首个 eligible」（修复 Dequeue 队首阻塞）；eligible = 非 workdir 互斥中（依赖门控在 P4 同机制复用）
- 投递窗口：占用从 **offered 起算**（offered+acknowledged ≤8）——只算 running 会漏（两个 acked 等并发槽，槽一空全 spawn 击穿互斥）
- 排队超时：新常量 `WorkdirQueueWait`（默认 30 分钟，hub 侧可行因不依赖 ExecProfile 的 timeout）——超时置 failed（error=workdir_busy_timeout）；排队不计入 agent 执行超时
- 生命周期：锁覆盖整个写生命周期——从 spawn 前准备到**进程组退出 + 产物冻结**才释放（node 侧执行互斥为准，hub 的 running 状态只是视图）；node 重启恢复：互斥表从账本重建（started/accepted 未终态任务占锁）
- 限界声明：互斥只覆盖受管执行（node spawn 的任务）；人工编辑器、外部脚本、共享 ConfigHome 不在覆盖内（文档声明）
- 子任务死锁：同一任务禁止声明会等待自身的依赖（L2 相关）；同 workdir 的父子任务串行符合预期

**DoD 增补**：同 workdir 并发提交 → 串行断言（第二任务 started ≥ 第一任务 finished）；异 workdir → 并行断言；排队超 WorkdirQueueWait → failed/workdir_busy_timeout；node 重启 → 互斥表恢复；Head-of-line 断言（nodeB 队首互斥中，nodeB 的异 workdir 任务仍可派发）

## 三、L2：产物链与任务依赖（P4 群聊协作）

**依赖寻址（终审修正采纳）**：`depends_on` 使用 **idem_key**（跨重跑稳定）而非 task_id；「上游重跑自动解锁」删除，改为：上游新版本 completed → 下游任务标 `superseded`（人工决定是否重派，旧任务审计保留）。

- **blocked 不进状态机**（两方共识）：保持 submitted + `blocked_reason` 门控标注（七态状态机、收敛表、终态规则零改动）；blocked 任务留在信箱**占配额**（口径简单，信箱满则新提交被拒的既有语义不变）
- 调度：上游终态仲裁处（result.go）事件触发下游复评 + Sweep 每秒兜底扫描；hub 重启恢复免费（全在快照）
- 依赖图准入：拒绝未知/自引用/重复依赖；只允许引用已存在任务且边不可变（按此约束天然无环）；最大深度 4、级联 ≤8
- **产物挂载（两方共识）**：纯复制（**删硬链接选项**——下游改动会污染上游产物 SHA，artifact 校验必炸）→ 消费者专属输入区 `workdir/inputs/<上游id>/` → 复制后 SHA-256 校验 → 原子发布；上游 completed ≠ 目录仍在（保留期语义对齐）。**交付状态（2026-10-24）：挂载骨架已交付（本机路径）**——node 执行准备阶段（started/spawn 前，独立超时，失败 `input_prepare_failed` 不进入执行），解析口径=账本最早 completed，SPEC 4.12「产物挂载」段为实现契约；**跨机取回 artifact_fetch 方向扩展另行**（第二子轮，`InputResolver` 接口已留 Remote 实现位）。
- **跨机交接（P4 最贵项，预算单列）**：需新增 node→hub 发起的 artifact_fetch 方向扩展 + hub 路由回上游 node 的中继 + node 侧挂载代码；约束：单文件 ≤640KiB、≤8 件、源 node 离线标「暂不可取」、输入就绪前不 spawn（准备阶段独立超时）；分块传输 V1
- 代码改动的表达：outputs 不自动包含仓库修改——L3 落地前的「代码交接」= 打补丁文件（diff）或整目录快照，文档明示

**DoD 增补**：依赖顺序断言、依赖 failed→blocked_reason 断言、superseded 流程、产物挂载 SHA 一致、跨机取回（P4 联调）、依赖环拒绝

## 四、L3：Git 集成（P5，8-12 人日，成本上调采纳）

- `ExecProfile.git_mode: none | worktree | branch`（默认 none 行为不变；**worktree 模式豁免 L1 workdir 互斥**——每任务 workdir 天然唯一，写入两处 DoD 防断言打架）
- 生命周期时序：**started 落盘 → task_started → worktree add（基线=任务绑定 commit）→ spawn → finished 落盘 → 自动 commit → 产物登记 → task_result**——Git 操作纳入账本生命周期，崩溃各阶段可安全重试（worktree add 幂等：同名已存在则复用）
- 基线固定：任务绑定的基线 commit 记录进账本（不用漂移的 HEAD）
- **commit 卫生（zcode 坑采纳）**：`git add -A` 排除 `outputs/` 与 `inputs/`（.gitignore 或路径过滤，产物与输入不入库）；`.worktrees/` 移出主工作区目录并 gitignore（防污染主区 status）
- 合流：隔离集成 worktree 中串行尝试 merge（区分冲突/认证错误/成功）；记录双方 commit+目标分支+冲突清单；冲突 → 自动生成 needs_review 任务（hub 系统事件来源，持久化去重）+ 回群讨论
- 清理与保留：worktree 清理窗口与 SPEC 4.8 产物保留对齐（**以 result_ack 起算**——worktree 里藏着 outputs/，先删会令 artifact_fetch 提前 404）；failed 保留诊断 24h；被引用分支 pin 不自动删；janitor 对账清扫（扫 .worktrees vs 账本）+ 磁盘配额上限
- 凭证：ConfigHome 只是位置——需声明凭证授权范围（只读拉取 vs 推送）、非交互认证、失效处理、日志脱敏
- 大文件与二进制：原始字节产物+SHA 校验可传；Git 二进制冲突走人工选取流程；LFS/submodule V2 不支持（声明）

## 五、归属与时机（终审修订采纳）

| 层 | 归属 | 交付物 | Gate 增补 |
|---|---|---|---|
| L1 Workdir 互斥 | **P1.5**（P1 收尾增量，2-3 人日，填 P2 等待期） | 调度互斥表+eligible 门控+workspace_id 上报+测试 | DoD 增补四项断言 |
| L2 产物链+依赖 | **P4**（2026-10-24 起与群聊解耦：文件交接是执行基础能力，群聊只是可能的展示入口，见 PRODUCT-DIRECTION-v2；4-6 人日起） | depends_on（idem 寻址）+纯复制挂载+跨机取回方向扩展（独立 L2 验收：依赖顺序/failed 阻塞/superseded/挂载 SHA 一致/跨机取回；群附件转实验区随群聊冻结） | L2 DoD（独立，不绑群版） |
| L3 Git 集成 | **P5**（8-12 人日，前置=凭证+保留窗对齐） | worktree/branch 模式+自动 commit+冲突回群+janitor | V2 按需定义 |

## 六、DoD 故障矩阵扩容（评审采纳）

现有 11 条保留，各层增补（详见二/三/四节）：路径别名（symlink/大小写）、依赖环、排队超时、保留期交互、二进制冲突、并发峰值、磁盘恢复、force 鉴权审计、权限拒绝扩展（普通主体冒充控制主体）。
