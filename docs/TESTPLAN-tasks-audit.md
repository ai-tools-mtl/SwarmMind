# TESTPLAN：node 控制台任务可见性修复批（任务列表账本全量驱动）

> 依据：伦哥 2026-10-26 凌晨裁决——「系统审查任务时应该能看到所有从系统体系中执行的任何任务」，
> 机器主人对本机执行过什么有全量审计权。测试先行（实施前定义，AGENTS 测试循环制度）。
>
> 现状缺口：`GET /api/v1/local/tasks` 数据源是 TaskLog（仅本机派单 handleSubmit 写入），
> hub 下发任务在 node 控制台完全不可见；而 node Ledger 已全量记录本机执行的一切任务
> 且逐条带 Origin（local/hub）。
>
> 修复验收口径（唯一真相）：任务列表 = **本机账本全量**（hub 下发 + 本机派单一律在列，
> 逐条标来源）∪ 留痕补尾（账本中已不存在的本机派单历史，origin=local、无状态行；
> 账本保留策略口径见 D2）。
> 详情沿用双路径语义（local=产物/核对在本机，hub=经 hub 链路指引）。

## 修复设计定案（实施与评审共同基准）

- **后端 `handleLocalTasks` 重写数据源**：遍历 `Ledger.View()` 出账本行——字段
  task_id/trace_id/target_node/target_agent/idem_key/created_at 逐条自带，
  `origin` 取条目 Origin（空归 hub，旧账本文件兼容），`status` 按 `ledger.TaskView`
  同源映射出视角状态（轻量投影不取整单深拷贝，审查节④；
  acknowledged|running|completed|failed|needs_review|cancelled）——**列表
  行自带状态，前端不再逐条补查**；TaskLog 中账本已不存在的条目（本机派单历史
  留痕；账本保留策略接线清理后出现，清理当前未接线——见 D2）补尾：origin=local、
  无 status（详情查询 404 = 任务不在账本，页脚说明）；合并按 created_at 新→旧
  排序（同刻按 task_id 稳定序），账本行优先去重。快照顺序：先 TaskLog 后 Ledger
  （提交路径先入账后留痕，审查节①——反序下两次读取之间落地的新提交被误判
  「无状态留痕」）。分页：limit/offset（默认 200、上限 1000）+ total 总数，排序
  在切片前（跨页次序一致），审查节④。
  Ledger 未装配（setup 等场景）时退化为纯留痕行；两源皆空 → `tasks: []`。
- **前端**：列表/总览徽章改用行内 status 直出（退役「最近 8 条并发补查」的 S.live
  机制）；新增「来源」列（本机派单 | hub 下发，与抽屉头部来源词同源）；空态与页脚
  文案改为「本机执行过的全部任务」口径；`taskOrigin()` 读列表 origin 字段——hub 任务
  现在真在列表里，详情抽屉双路径语义（产物区指引/needs_review 指路）对 hub 任务首次
  真正生效（审查节⑤后：来源随抽屉持久化，列表缺失行不再缺省 local）。分页配合：
  首页 200 条 +「已载 X / 共 Y 条」+「加载更早」追加翻页（审查节④）。**shared
  block（词典/时间/事件/答复/分区更新）不触碰**——词典注释中
  「node 本地列表只有本机派单留痕」一句随本批过时，属 shared block 双端同改范围，
  本批不动（避免触碰 hub web 面），遗留观察项见 D 节。
- **TaskLog 保留**：仍由 handleSubmit 登记（本机提交视角的历史，含账本清理后仅存的
  提交记录），不再作为列表唯一真相。
- **W2 遗留观察顺带核验**：node 侧 STATES/TERMINAL cancelled 已补（app.js 词典），
  本批验证无回归（cancelled 任务列表/抽屉/徽章全链）。

## A. 后端单测（go test，可机械执行）

执行：`go test ./internal/node/localapi/ ./internal/node/ledger/ -race -count=1`

- **A1 账本全量驱动（核心缺口关闭）**：账本种入 hub 来源任务（`Accept` → origin=hub）
  终态 completed；`GET /api/v1/local/tasks` 断言该任务在列且 `origin=="hub"`、
  `status=="completed"`、target/submitted/idem/created_at 与账本条目一致。
- **A2 本机派单来源标注**：账本种入 `AcceptOrigin(local)` 任务（started 态），断言
  `origin=="local"`、`status=="running"`。
- **A3 cancelled 无回归（W2 观察项）**：账本种入 hub 来源 cancelled 终态条目
  （result.status=cancelled，result 为空 final_answer 合法态），断言列表行
  `status=="cancelled"`。
- **A4 留痕补尾**：TaskLog 种两条——task_id 命中账本条目（不得重复出现）、task_id
  不在账本（已过保留期）；断言：前者仅账本行一份；后者在列、`origin=="local"`、
  `status` 为空。
- **A5 排序与稳定序**：乱序 created_at 种入 ≥3 条（含同刻两条），断言响应按
  created_at 新→旧，同刻按 task_id 字典序。
- **A6 降级路径**：Ledger=nil 且 TaskLog 有数据 → 仅留痕行；两源皆空 → `tasks` 为
  空数组（非 null）。
- **A7 鉴权门不回归**：`/api/v1/local/tasks` 无令牌/错令牌 → 401（既有
  TestLocalAuthGate 覆盖，随批全绿即证）。
- **A8 TaskLog 内部行为不回归**：TestTaskLogJournal（新→旧/幂等/裁剪/重启保持）全绿。

## B. 全量回归与静态检查

- **B1** `go build ./...` 通过。
- **B2** `go test ./... -race -count=1` 全绿（19 包）。
- **B3** `GOOS=linux go build ./...` + `GOOS=windows go build ./...` + `go vet ./...` 全过。
- **B4** 双端展示一致性锁不回归：`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'`
  双绿（本批不触碰 shared block，锁测试保绿即证）。

## C. 控制台真实链路走查（headless 浏览器，截图留档）

场景搭建：回环开发模式（hub `insecure_loopback=true`，127.0.0.1），node 经 join 串
接入，挂 echo-demo agent（configs/node.example.yaml 形态）。全程生产环境零接触。

- **C1 hub 下发任务入账**：hub 管理台向该 node 的 echo-demo 派单并等终态 completed。
- **C2 本机派单入账**：node 控制台表单向本机 echo-demo 派单并等终态。
- **C3 列表全量+来源列（核心验收）**：node 控制台「派任务 · 我的任务」列表同时呈现
  C1（来源=hub 下发）与 C2（来源=本机派单）两行，状态徽章直出（无需点击查询），
  截图留档。
- **C4 总览最近任务**：总览页「最近任务」同口径呈现两来源行。
- **C5 hub 任务详情双路径**：点开 C1 行详情——头部「来源 hub 下发」、产物区给
  「经 hub 管理面取回」指引（双路径语义对 hub 任务生效）。
- **C6 本机任务详情双路径**：点开 C2 行详情——「来源 本机派单」、产物区给本机
  workdir 绝对路径指引。
- **C7 cancelled 呈现**：构造 cancelled 终态任务（hub 取消链路或账本注入），列表
  徽章「已取消」、详情不落「尚未到终态」误导（cancelled+result 空答复人话兜底）。

## D. 遗留观察项（不在本批修，登记防丢）

- **D1 词典 shared block 注释过时**：app.js 词典注释「node 本地列表虽只有本机派单
  留痕」随本批失真；shared block 双端逐字节锁，须 hub/node 同批同改，本批不触碰
  hub web 面——留待下次双端展示批顺带更新（词典内容本身无需变更）。
- **D2 审计保留策略（待产品裁决，codex 审查②事实纠正后改写）**：node 账本清理
  `CleanupAcked` **未接线**（无调用方）——当前账本只增不减，列表实际全量可见、
  留痕补尾行常态不出现；且「24h」若接线也从首次结果确认（first_acked_at）起算，
  「离线 7 天」是 hub 侧任务过期口径而非账本删除窗口——原文「终态后 24h /
  离线 7 天」与代码不符，本文档与本批 UI/错误文案一律不写具体保留窗口。
  是否接线清理、保留多久（审计窗口诉求 vs 规模增长，审查节④容量边界即源于此）
  属保留期策略变更，**提请产品裁决后另批实施**。
- **D3 文档同步**：INTERACTION 旅程②「我的任务」描述与 SPEC 六（node 本地 API 端点
  说明）需随本批语义更新；docs/ 与 README 当前有其他会话文档批改动禁碰，挂账下批
  文档批合并处理。
- **D4 抽屉归档文案保留期失真（跨端同文锁，本批不能动）**：抽屉归档区
  「任务已归档前保留 24 小时。」为 hub/node 跨端同文锁
  （web_drawer_sync_test 锁定），随 D2 它属事实失真文案；纠正须 hub/node 同批
  同改，挂账下次双端展示批与 D1 合并处理。

---

# 审查节（codex，2026-10-26 02:00，必须修 2 + 建议修 3）

1. **[P2] 并发竞态**：Ledger 先读、TaskLog 后读（local.go:197），提交路径先入账后写留痕（server.go:337）——两次读取间的新提交被误判「无状态留痕=已过保留期」。→ 调换快照顺序（先 TaskLog 后 Ledger）+确定性并发测试。race detector 不报警的逻辑竞态。
2. **[P2 澄清] D2 保留期事实不符**：TESTPLAN「24h/7 天」与代码不符（CleanupAcked 未接线；接线后也从首次结果确认起算，7 天是任务过期口径非账本删除窗口）→ 改登记为「清理未接线+审计保留策略待定」，提请产品裁决。
3. **[建议] A1/A6 断言加固**：target/trace/created_at 值断言；A6 补准确条数/逐行 origin/全无状态。
4. **[建议] 容量边界**：全账本轮询无分页（10K 行 430ms 且清理未接线规模增长）→ 轻量状态投影+分页或虚拟列表。
5. **[P3] 详情来源依赖当前列表**（app.js:1432 列表缺失行默认 local 误判）→ 抽屉保存来源。

（修复闭合记录由实施方追加）

---

# 修复闭合记录（实施方追加，2026-10-26）

逐条处置（对应审查节编号，全部闭合）：

**① [P2] 并发竞态 — 已闭合**
- `handleLocalTasks` 快照顺序调换：先 TaskLog 后 Ledger（internal/node/localapi/local.go）。
  提交路径序为「先入账后留痕」，调换后任一交错下窗口内提交至多出列一次且必为带状态
  账本行（完整推理见函数注释）。
- 确定性并发测试 `TestLocalTasksSubmitInterleave`（local_test.go）：新增同包测试钩子
  `Server.tasksRaceHook`（沿用 mcpCommandOverride「同包测试专用」先例）在两次快照之间
  走真实提交路径（handleSubmit：先入账、后留痕）。变异钉死：精确复刻旧序
  （Ledger→提交→TaskLog）该测试红 `应为带状态账本行, got status=""（无状态=被误判为
  已清理留痕行）`，新序绿——回归断言确实咬人。

**② [P2 澄清] D2 保留期事实不符 — 已闭合**
- TESTPLAN D 节 D2 改写为「CleanupAcked 未接线 + 审计保留策略待产品裁决」，删除
  「终态后 24h / 离线 7 天」具体窗口；修复设计定案与验收口径同步中性化。
- 同一 falsehood 的 UI/API 文案随批纠正（一律不写具体窗口）：我的任务卡页脚、总览卡
  页脚、抽屉查询失败壳、留痕行徽章（「已过保留期」→「仅存留痕」）、404 错误消息两处
  （server.go handleQuery / artifact.go）。
- 新增遗留观察项 **D4**：抽屉归档文案「任务已归档前保留 24 小时。」为 hub/node 跨端
  同文锁（web_drawer_sync_test 锁定），纠正须双端同批；本批不触碰 hub web 面，挂账
  下次双端展示批与 D1 合并处理。

**③ [建议] A1/A6 断言加固 — 已闭合**
- A1：t-hub-done 行 target_node/target_agent/trace_id/idem_key/created_at 逐值对齐
  账本条目（TestLocalTasksLedgerDriven）；A4 留痕补尾行补 trace_id/created_at 逐值。
- A6 结构化断言：降级恰 2 行（=留痕条数）+ total 同值、逐行 origin=local、逐行无
  状态；两源皆空断言 tasks 空数组 + total=0（替换原字符串 Contains 断言）。

**④ [建议] 容量边界 — 已闭合**
- 轻量状态投影：`ledgerListStatus` 与 ledger.TaskView 同源映射、只投影状态字段，
  不再逐行 TaskView 深拷贝整单；行字段收敛为列表所需最小集（task_id/trace_id/
  target_node/target_agent/idem_key/created_at/origin/status，去 submitted_by——
  详情经 GET /api/v1/tasks/{id}）。
- 分页：limit/offset（默认 200、超上限收敛 1000、非法值 400、offset 越界空页）+
  total（合并去重后总数），排序在切片前（跨页次序一致）。
- 前端配合：首页 200 +「已载 X / 共 Y 条」+「加载更早」追加翻页（在途防重入、轮询
  全量重载穿插时丢弃拼接防缺页）；总览计数改用 total。
- MCP 同步口径：本端点是 node 本机控制台只读面（回环监听+本机令牌）的容量改造，
  非新管理面能力；node MCP 六工具覆盖不变（SPEC 六.六豁免④口径），映射表零变更。
- 单测 `TestLocalTasksPagination`（默认值/切片连续/尾页/越界/非法 400×5/clamp）。

**⑤ [P3] 详情来源依赖当前列表 — 已闭合**
- openDrawer 开抽屉时刻从列表行捕获 origin 存入 `S.drawer.origin`（随抽屉会话
  持久化）；`taskOrigin` 改三级回退：drawer.origin → 当前列表行 → 缺省 local
  （open-submitted 直开 = 本机派单 by construction）。列表分页/刷新使行失位不再把
  hub 任务误标本机派单（错误产物指引的根因）。

**复验记录**
- `go test ./... -race -count=1`：20 包全绿（0 FAIL）。
- `go build ./...` + `GOOS=linux go build ./...` + `GOOS=windows go build ./...` +
  `go vet ./...`：全过。
- 双端展示一致性锁：TestWebMdRender / TestWebDrawerSharedBlocks /
  TestWebDrawerComponentStyles / TestWebDrawerUnifiedCopyStructure 全绿
  （shared block 未触碰）。
- harness 复验（docs/prototypes/shoot-tasks-audit-fix.mjs：真实 hub + nodeA 隔离
  环境、回环 TLS、生产零接触；node 启动前账本注入 205 条 hub 来源终态种子〔C7 认可
  的注入法〕+ hub/本机各真实派一单，共 207）：**24 PASS / 0 FAIL**——API 分页契约
  （默认 200 + total=207、切片连续、尾页、越界空页、非法 400×5、投影字段）、控制台
  首屏 200 行/「已载 200 / 共 207 条」/加载更早追至 207 且按钮退场、来源+状态徽标
  直出、任务页无具体保留窗口文案、hub/本机抽屉来源双正、列表缺失行重算后抽屉来源
  不回退 local（⑤）、跨轮询壳不重建、总览计数用 total、全程无 JS 错误。
  截图 docs/prototypes/shots-tasks-audit/ 留本地。
- 纪律遵守：他会话文档批未触碰（本批 docs 仅动 TESTPLAN-tasks-audit.md 与新增
  harness/截图）；workers 不 commit，提交按钮在伦哥手里。

---

# 复审节（codex，2026-10-26 02:40，新增必须修 2，均在 app.js）

1. **轮询清掉已加载历史页**（app.js:387）：loadAll 每 5s 首页覆盖 S.tasks（207/207→200/207），历史无法浏览。→ 轮询只刷新已加载范围，历史页保持；跨轮询测试覆盖。
2. **翻页迟到应答污染**（app.js:410）：仅比长度不识别首页已刷新；轮询提前解除 busy；复现「旧尾页返回漏 t-199 且加载按钮消失」。→ 请求代次校验+在途 busy 保护。

（修复闭合记录由实施方追加）

---

# 复审闭合记录（实施方追加，2026-10-26）

逐条处置（对应复审节编号，全部闭合；方案取意见给的第二条路「首页刷新+历史页保序合并」）：

**① 轮询清掉已加载历史页 — 已闭合**
- `loadAll` 不再整列表覆盖：取新首页后经 `mergeTaskPages`（app.js）与已载列表
  合并——新首页在前、旧载行按原相对序接尾、按 task_id 去重。正确性依据：提交
  路径新任务插队首部、旧行相对序不变，新首页必含旧载行前段（有重叠），其后旧载
  行接尾覆盖无缺口（间隔新任务 < 一页时恒成立）；零重叠（间隔新任务超一整页，
  保序拼接必有缺口）整段弃用退回纯首页，按钮仍在可重新展开。
- total 一律以后端为准；轮询数据无变化时合并结果与原列表全等，snap 指纹不变、
  不触发重绘（轮询空转零 DOM 触碰不回归）。
- 合并后 S.tasks 仍为服务端全序的无缺前缀（排序在切片前契约），翻页 offset=
  已载行数继续成立。

**② 翻页迟到应答污染 — 已闭合**
- 新增列表代次 `S.tasksGen`：loadAll 每次写入列表态 +1；`loadMoreTasks` 发起时
  捕获代次，应答落地时代次不一致 = 中途发生过轮询刷新（offset 基线已失效），
  整体丢弃（不拼接、不更新 total），替换原先「仅比长度」的失效检查——长度检查
  在「轮询刷新但无新任务」时恰好相等，正是旧尾页漏行/重复的根因。
- busy 归属收敛：`S.tasksMoreBusy` 只由翻页流程自身解除，loadAll 删除「全量重载
  复位翻页在途态」一行——轮询不得提前解除在途翻页的 busy（防解除后二次翻页与
  双在途拼接）。

**跨轮询 harness 断言（shoot-tasks-audit-fix.mjs 新增 8 条，24→32 PASS）**
- 加载 2 页→轮询 2 轮：历史保持（仍全量 207 行、按钮退场、计数收口）+ 无漏条
  （DOM 行序逐条 === 服务端全序，老历史行不丢、无重复）。
- 轮询间新任务真实入账（追派一单等终态）：合并后仍全量无缺口（行数 +1、行序
  === 服务端全序、新单列首）。
- 确定性交错构造（fetch 包装把 offset 应答延迟 500ms，翻页在途时手动 pollTick
  先行完成）：在途 busy 不被轮询提前解除（轮询完成后仍 true）；迟到翻页应答
  作废（不拼接、busy 收口、列表仍首页 200）；作废后 DOM 无污染。

**变异钉死**（备份还原法，未碰 git）
- 突变 A（loadAll 回退整列表覆盖+复位 busy）：harness 27 PASS / 5 FAIL——
  「历史保持」「无漏条」「新任务合并」「计数收口」「busy 提前解除」全红。
- 突变 B（去掉翻页代次校验）：30 PASS / 2 FAIL——迟到应答拼接污染实证
  （n=208=200+8 旧尾页行拼上新列表、DOM 行序失配）。

**复验记录**
- `go test ./... -race -count=1`：20 包全绿（0 FAIL）。
- `go build ./...` + `GOOS=linux go build ./...` + `GOOS=windows go build ./...` +
  `go vet ./...`：全过。
- 双端展示一致性锁：TestWebMdRender / TestWebDrawerSharedBlocks /
  TestWebDrawerComponentStyles / TestWebDrawerUnifiedCopyStructure 全绿
  （改动均在 shared block 之外）。
- harness 复验（环境同前：真实 hub + nodeA 隔离回环、生产零接触）：**32 PASS /
  0 FAIL**（原 24 条不回归 + 新增 8 条全过），截图 tasks-cross-polling.png 留本地。
- 纪律遵守：他会话文档批未触碰（本批 docs 仅动 TESTPLAN-tasks-audit.md 复审闭合
  与既有 harness 追加）；workers 不 commit，提交按钮在伦哥手里。
