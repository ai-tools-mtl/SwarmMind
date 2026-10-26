# TESTPLAN：P5 Work W1 批（模型+事务+账本+创建幂等+只读查询）

> 依据 docs/RFC-work-resume.md v2 第五节 W1 行；测试先行（实施前定义，AGENTS 测试循环制度）。
> W1 范围裁剪声明：Work 建单为**单事务原子**（骨架即全量：全步骤 pending + created 事件同事务落盘），
> 不存在 ControlBatch「骨架→逐项回写」的中间窗口，故 W1 无独立 recoverWorks——ControlBatch
> 两阶段+recoverBatches 模板服务于**逐项创建**类写入，W2 派发绑定（任务落库与尝试/步骤回写跨事务）
> 沿该模板补恢复扫描；本批事实与事件的原子性由「同一 store.Update 事务内变更+记账」结构保证。
> 执行方式：`go test ./internal/hub/store/ ./internal/hub/core/ ./internal/hub/api/ ./cmd/swarmmind-mcp/ -race -count=1`；
> 全量回归 `go test ./... -race -count=1` + `GOOS=windows/linux go build ./...` + `go vet ./...`。

## 一、四条核心验收（RFC W1 行）→ 用例映射

| # | 核心验收 | 用例（包/函数） | PASS 判据 |
|---|---|---|---|
| 1 | 建单响应丢失仍返同单 | core `TestWorkCreateIdempotentResponseLostReturnsSameWork` | 同 goal+steps 重复 CreateWork：同 work_id、dup=true、不新增事件、State.Works 恒 1 条、WorkIdems 恒 1 条；重启（Close→Open→新 Core）后再调仍返同单；不同 goal 或不同 steps 指纹不得命中（必须新建） |
| 2 | 旧快照兼容 | store `TestWorkOldSnapshotCompatible`（归一单元）+ core `TestWorkReloadRoundTrip`（全链） | 单元：预置**不含任何 Work 字段**的旧版快照 JSON（含 nodes/tasks 既有实体）→ Open 成功、既有实体逐字段完整、Works/WorkIdems 归一为非 nil 空 map、迁移后可写入。全链（审查修 3②）：同一旧快照加载 → **真实 CreateWork 入口**建单（含依赖声明）→ 重启后 Work/Steps(含 depends_on)/Events(含主体)/EventSeq 逐字段恢复、同指纹幂等仍命中、旧实体不受影响（store 包不可反向依赖 core，故全链落 core 包） |
| 3 | 写失败不发布 | core `TestWorkCreateWriteFailureNotPublished`（真实 CreateWork 入口，审查修 3③） | ① store 临时写路径被目录占位（对任何用户确定失败）→ **CreateWork 返回错误**（非校验错、不返回视图）、内存 View 无该 Work、盘上快照无该 Work/指纹、临时路径无半截文件；移除障碍重试成功且内存盘一致、随后幂等命中（失败事务不占指纹/ID）。store 层事务回滚语义由 store 包 TestStoreWriteFailureNoPublishNoAck 覆盖；② mutate 拒绝路径（步骤校验失败）零发布见 core TestWorkStepValidation |
| 4 | 事实与事件一致 | core `TestWorkFactsAndEventsConsistent` | 建单后：Work 落库与 work_created 事件**同事务可见**（无「有事实无事件」窗口）：事件恰 1 条、seq=1、EventSeq=1、步骤全 pending、状态 planning；服务端验证主体独立落账（principal 与自报 by 分开）；dup 命中**零新增事件**（重复回调不重复记账）；重启后与关停前已发布状态**逐字段全量比对**（含 Attempts 非空场景、Steps 依赖、Events 时刻/主体/正文，审查修 3④）；滚动裁剪后 EventSeq 单调不回退且事实（steps/attempts）不受影响，裁剪态重启后再次逐字段全量比对 |

## 二、支撑用例（模型/校验/账本/查询面）

| 用例 | 覆盖点 | PASS 判据 |
|---|---|---|
| store `TestWorkCloneIsolationWorkFields` | Clone/CoW 纪律（State.Clone 对 Works 深拷贝：Steps/Attempts/Events 切片与 Step.DependsOn 逐层复制） | **非空依赖+Attempt+终态时间指针**全上（审查建议修 4），探针用就地改写（append 是否复用底层数组取决于容量，只有就地写能确定性暴露浅拷贝）：事务内改副本（含 `*ClosedAt` 解引用写）不污染先前发布快照；读者改旧快照（含解引用指针）不影响已发布状态（对齐 TestStoreCloneIsolation 口径） |
| core `TestWorkStepValidation` | 步骤校验：引用有效（越界/自引用）、重复依赖、依赖环、深度>4、级联>8、空 desc/target、步骤数 0/>64 | 全部 ErrInvalidWorkSteps（包装错误含定位文案）；合法 DAG（菱形/多父）通过 |
| core `TestWorkEventsRollingRetentionAndCursor` | 账本滚动保留（200，沿群消息模式）+ 游标读取 | 第 201 条入账后仅存最新 200 条；EventSeq=201 不回退；GetWork(since=N) 只返 seq>N 且升序；pruned_before_events = 现存最旧 seq-1；裁剪不影响 steps/attempts |
| core `TestWorkListWorks` | 只读列表 | 新建在前稳定排序；摘要不含 Events 大字段；未终态摘要不输出 closed_at（指针承载 omitempty，审查建议修 5 回归口径） |
| api `TestWorkEndpointsAuthGate` | 管理面门禁 | 未登录 POST/GET /api/v1/works* 一律 401 |
| api `TestWorkRESTFlow` | REST 契约 | 建单 201（dup 200+dup:true）；校验失败 400 invalid_work_steps；GET 列表/详情/游标；未知 id 404 work_not_found；事件 principal=admin 且 by=自报缺省 mcp（分开落账） |
| api `TestWorkRESTCreatePrincipalRecords` | 服务端验证主体接线（审查修 2） | admin 会话入口 principal=admin、PAT（Bearer）入口 principal=pat:<令牌名>；请求自报 created_by 原样留 by 不冒充主体；主体随详情读回（持久化）；详情无凭据 401 |
| mcp `TestWorkToolsHubOnlyAndFullFlow` | 六.六 同步开发规则③ | tools() 含 swarmmind_work_create/get/list；nodeBackend.tools() **不含**（hub-only 不静默降级）；真实 hub 全链：建→列表→详情（含事件游标）→幂等重建返同单；**读档步骤输出含 depends_on**（与 REST 对齐，审查修 1）、事件行含服务端验证主体（pat:<测试令牌名>）；错误人话化（isError=true） |

## 三、边界声明（防验收口径漂移）

- W1 无派发/接管/收集/关闭：Work.Status 停留 planning；ExpiresAt 恒零值（终态 30 天自关闭起算，W3 落）；Sweep 不触碰 Works（未完成 Work 不清理，RFC 六.3）。
- Attempt 在 W1 只保证模型与 Clone 正确（W2 绑定派发时产生记录）；建单产物 Attempts 为空。
- 建单入口 v1 仅 Prime/MCP（六.六开放问题裁决 1）：REST POST /api/v1/works 为 MCP 传输载体（created_by 缺省 mcp），W3 管理台只读。
- 事件账本主体双字段（审查修 2 定案）：`by` = 请求自报审计标签（不可信，原样留档）；`principal` = 服务端验证鉴权主体（鉴权中间件注入上下文：admin 会话 / pat:<令牌名>），分开存储随事件持久化（SPEC「工作单」节、RFC-work-resume 六.2）。
- 步骤 seq 由 hub 按声明序赋 1..N（唯一性由构造保证，RFC「唯一」校验落在这一定义上）；depends_on 引用该序号。
- 协议层零变更：W1 不动 protocol 帧/Task 结构（Task.WorkRef+AttemptRef 随 W2 绑定交付）。

---

# W1 审查节（codex，2026-10-25 13:15，必须修 3 + 建议修 2）

1. **必须修**：MCP work 输出缺 depends_on（main.go:1969 附近步骤解码及输出均无，REST 有 MCP 无）→ 补齐+断言。
2. **必须修**：事件账本未记录服务端验证鉴权主体（works.go:34 / work.go:327 请求 created_by 原样成 By，任意调用者可冒充身份）→ 服务端从鉴权层取主体入独立字段，与 prime_term/自报标签分开存储。
3. **必须修**：②③④三条核心验收断言不到位——旧快照兼容未走 CreateWork 路径且计划中的 TestWorkReloadRoundTrip 不存在；写失败测的是手写 Update 非 CreateWork 入口；事实事件一致性重启后只比部分字段且 Attempts 恒空。→ 全部加固为真实入口全链断言（含 Attempts 非空场景）。
4. **建议修**：Clone 测试空依赖无 Attempt，测不出浅拷贝回归 → 非空依赖+Attempt+关闭时间指针验证隔离。
5. **建议修**：closed_at time.Time+omitempty 零值仍输出（core/work.go:92）→ 改指针。

（修复闭合记录由实施方追加于此节之后）

# W1 审查修复闭合记录（zcode，2026-10-25，必须修 3 + 建议修 2 全闭合）

- **必须修 1（MCP 输出缺 depends_on）**：`toolWorkGet` 步骤解码结构补 `DependsOn` 字段，读档输出有依赖的步骤追加「（依赖 #1,#2）」后缀（建单入参 depends_on 解码原有，未动）；`works_test.go` 读档断言补 `（依赖 #1）`（步骤 2 依赖 1 的展示实测）。
- **必须修 2（事件账本缺服务端验证主体）**：`store.WorkEvent` 新增 `principal` 字段（omitempty），与自报 `by` 分开存储（RFC-work-resume 六.2 裁决落地）。`tokens.verify` 改返回 (令牌名, bool)；`auth` 中间件校验通过即注入请求上下文：admin 会话 → `admin`、Bearer PAT → `pat:<令牌名>`；`CreateWork` 增 `actor` 参数独立于自报 `created_by`，事件随事务落账主体；REST `handleWorkCreate` 接线 `authPrincipal(r)`。MCP 读档事件行展示 `（by｜主体 principal）`（by=principal 时不重复展示）。断言三层：core（主体随事件落账+重启保留）、api `TestWorkRESTCreatePrincipalRecords`（会话/PAT 双入口 + 自报「冒充者自报」只进 by 不进 principal + 详情持久化读回 + 无凭据 401）、MCP（真实 PAT 全链输出含 `主体 pat:mcp-test`）。SPEC「工作单」节补事件主体双字段契约行；TESTPLAN 二表补 api 主体用例行、三节边界声明补双字段定案。
- **必须修 3②（旧快照兼容全链）**：core 新增 `TestWorkReloadRoundTrip`（计划中点名但原不存在的用例）：旧版快照预置 → Open（既有实体逐字段完整、Work map 归一空）→ **真实 CreateWork**（三步含依赖+主体）→ Close → 重启 → Work 全字段逐项恢复（`assertWorkEqual`）、同指纹幂等仍命中（依赖参与指纹重算）、旧实体保留、指纹映射恰 1 条指向原单。store 层 `TestWorkOldSnapshotCompatible` 保留为归一单元（store 不可反向依赖 core，全链只能落 core 包），TESTPLAN 验收 2 映射行同步改写。
- **必须修 3③（写失败测真实入口）**：store 版 `TestWorkCreateWriteFailureNotPublished`（手写 Update）删除（store 事务回滚语义已有 `TestStoreWriteFailureNoPublishNoAck` 通用覆盖，无覆盖损失）；core 新增同名用例走 **CreateWork 入口**：临时写路径目录占位 → 返回错误（且断言非校验错、不返回视图）、内存 View/指纹零残留、盘上快照无该 Work/指纹（core.New 启动自愈事务已建快照文件，断言口径为内容零污染而非文件不存在）、临时路径无半截文件；移除障碍重试成功、内存盘一致、随后同指纹幂等命中（失败事务不占指纹/ID）。TESTPLAN 验收 3 映射行同步改写。
- **必须修 3④（重启逐字段全量比对+Attempts 非空）**：`TestWorkFactsAndEventsConsistent` 加固——新增 `assertWorkEqual` 逐字段比对器（标量/时间 Equal/终态指针比值/Steps/Attempts/Events 逐元素；注释约束新增字段必须同步扩展，防比对面静默缩水）；建单后按 W2 形态经事务预置 2 条尝试记录（TaskID/IdemKey/Outcome 齐全），以关停前最后已发布状态为基准重启后全量比对；滚动裁剪后再重启二次全量比对+窗口头部 seq+事实不受影响断言。
- **建议修 4（Clone 测试空值测不出浅拷贝）**：store `TestWorkCloneIsolationWorkFields` 预置改非空依赖（步 2 依赖 [1]）+1 条 Attempt+ClosedAt/ExpiresAt 双指针；探针改**就地改写**（`DependsOn[0]=99`、`Attempts[0].Outcome`、`Events[0].Body`、`*w.ClosedAt` 解引用写——append 是否复用底层数组取决于容量，只有就地写能确定性暴露浅拷贝）；正向（事务改副本不污染旧快照）、反向（读者改旧快照含解引用不穿透已发布）双向断言。
- **建议修 5（closed_at omitempty 失效）**：`core.WorkSummary.ClosedAt` 改 `*time.Time`（time.Time 结构体零值不触发 omitempty，值形态恒输出 `"closed_at":"0001-01-01T00:00:00Z"`）；`ListWorks` 换新指针复制（View 零拷贝共享，禁共享内部指针，CoW 口径）；`TestWorkListWorks` 补双断言（JSON 不含 closed_at 键 + 指针 nil）钉住回归。

验证：`go build ./...` ✅；`go test ./... -race -count=1` 全绿（19 包）✅；`GOOS=windows go build ./...` + `GOOS=linux go build ./...` ✅；`go vet ./...` ✅。未 commit（提交在伦哥）。

# W1 复审节（codex，2026-10-25，必须修 1）

1. **必须修**：MCP 建单静默改写依赖（cmd/swarmmind-mcp/main.go:1864 附近）——`depends_on:[1.9]` 经 `int64(dv)` 截断为 `[1]`（number 但非整数放行）、字符串 `"1"` 因 `[]any` 断言失败整段丢弃，固化 DAG 偏离调用者声明。→ MCP 层严格校验 depends_on（拒绝非整数、越界数字、非数组类型；校验失败返回工具错误且不得发送建单请求）+ 回归断言（`[1.9]`/`"1"`/`[0 越界]` 等变体全部拒绝且 hub 无建单）。

# W1 复审修复闭合记录（zcode，2026-10-25，必须修 1 闭合）

- **必须修（MCP 依赖静默改写）**：`toolWorkCreate` 依赖处理重写为**严格校验、零改写、两段判定**——第一段逐步收结构：`depends_on` 键在场且非 nil 时必须是数组，否则报「收到的是字符串/数字/…」（`jsonArgType` 报 JSON 类型名，调用方视角可读）；null 视为未声明依赖（与 hub REST nil 切片语义一致，不误拒）。第二段收齐全计划后判元素：非数字元素（字符串/布尔）拒绝、非整数值拒绝（`dv != math.Trunc(dv)`，`[1.9]` 报「非整数序号 1.9」，不再截断）、范围 1..总步数拒绝（hub 允许前向引用故上界须收齐后判，两段式由此而来）。任何失败返回工具错误，**建单请求不发出**。工具 schema `depends_on` items `"number"` 收紧为 `"integer"` 并在描述声明拒绝语义（schema 是劝导，运行时校验是权威）。
- **回归断言**（works_test.go，真实 api server 全链）：拒绝矩阵七变体（`[1.9]` 非整数 / `"1"` 字符串依赖 / 裸数字 `1` / `[0]` 下界越界 / `[3]`（2 步单）上界越界 / `["1"]` 字符串元素 / `[true]` 布尔元素）全部断言 isError + 定位文案；矩阵后 `work_list` 断言被拒 goal 零泄漏（校验失败不得建单）；自引用 `[1]` 变体 MCP 放行（整数且界内）由 hub 终检拒绝，保留 hub 路径覆盖；空数组/null 两形态放行建单（不误拒）。**变异验证**：①禁用整数检查（恢复截断）→ 测试 FAIL；②非数字元素改静默丢弃 → 测试 FAIL；还原后复跑 PASS，断言非摆设。
- 原「越界依赖打 hub」用例口径改写：`[3]`（2 步单）上界越界现由 MCP 层拒绝（原断言 hub `invalid_work_steps` 文案），hub 侧准入不瘦身（REST 与防御纵深仍在，core/store 层用例未动）。

验证：`go build ./...` ✅；`go test ./... -race -count=1` 全绿（19 包）✅；`GOOS=windows go build ./...` + `GOOS=linux go build ./...` ✅；`go vet ./...` ✅。未 commit（提交在伦哥）。
