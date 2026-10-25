# TASK-drawer-sync 阶段 A 差异盘点报告（只读，2026-10-25）

> 执行：zcode。输入：docs/TASK-drawer-sync.md。方法：双端抽屉组装层代码逐区比对 + 真实环境 harness 走查截图（docs/prototypes/shots-drawer-sync/，20 帧，png 留本地）。性质判定为初步分类，裁决在阶段 B。
> 代码基线：main a866e9b。md 渲染器/样式 shared block 已有逐字节锁（web_md_render_test.go），本报告全部差异位于**抽屉组装层与组件词典层**，与既有锁不重叠。

## 结论提要

- 双端抽屉信息骨架同源（元信息/答复/日志/产物/终态），但**组装结构、文案、空态兜底、时间格式全面分叉**：23 条抽屉差异 + 11 条组件漂移。
- 其中 4 项接近 bug：node 时间线零值时间戳显示乱码（D16）、hub 运行中态兜底文案误导（D9）、node needs_review 文案承诺不存在的能力（D19）、hub 轮询重绘收起 details 并打断点击（D22）。
- node 端取消态差异（STATES/TERMINAL/ERRORS 缺 cancelled 系）当前不可达（列表只含本机派单留痕），属潜伏漂移。
- hub「任务输入」裸 JSON pre 是全抽屉唯一未参与 e821bf8 材质深度语法的区块。

---

## A. 抽屉差异清单

### 壳层与头部

**D1 抽屉壳层动效与无障碍** — 视觉+无障碍分叉（漂移）
- hub：`.drawer` 入场/出场动画（translateX+opacity+280ms 出场）、`role=dialog` `aria-modal` `aria-labelledby`、焦点入层/焦点返回、世代化生命周期（hub style.css:280-283；hub app.js:1307-1358）。
- node：纯静态 div（`.drawer` 无 transition 无 aria），`closeDrawer` 直接清空 innerHTML（node style.css:186-187；node app.js:1249-1270）。
- 帧：全部（hub 侧有滑入动效，node 侧无）。

**D2 头部结构** — 结构分叉（漂移）
- hub：三行——「任务详情 · {id}」标题行（含 ×）→「agent @ node」h2 → badge+trace 行 → 「{提交方} 提交 · 创建于 … · 恢复密钥 …」行（hub app.js:1371-1375）。
- node：单行「agent @ node + badge + ×」+ 单 small 元信息行「任务号 · 追踪 · 提交方 · 恢复密钥 · 来源」（node app.js:1318-1319）。
- 同一组信息两种排布；任务号 hub 放标题、node 放元信息行。

**D3 trace_id 呈现** — 文案/结构微差（漂移）
- hub：徽标行右对齐小字，空值无兜底（hub app.js:1374）。
- node：元信息行内「追踪 {trace||'—'}」有兜底（node app.js:1319）。

### 状态引导

**D4 状态介绍条** — 结构+文案分叉（漂移或有意简化，B 裁决）
- hub：五状态各一句人话 notice（运行中/已完成/失败/已取消/needs_review warn 变体，hub app.js:1365-1372）。
- node：完全没有介绍条，仅 needs_review 有 reviewNote（node app.js:1318-1324）。
- 帧：hub-1/2/7/9 vs node-1/2/7。

### 错误与失败呈现

**D5 错误通知条格式与 ERRORS 查找口径** — 文案+结构分叉（漂移）+功能差异
- hub：`<b>{ERRORS[code]||'执行中断'}</b><br>内部标记：{code} · 退出码 {exit_code}`（needs_review 追加第三行人话），ERRORS 用全码直查——组合码（如 `timeout;artifacts_skipped=1`）查不到即落「执行中断」兜底（hub app.js:1376）。
- node：`<b>{ERRORS[code.split(';')[0]]||code}</b> · <code>{code}</code>`，先切分再查（能吃组合码），不显示退出码（退出码在时间线行），原码用 code 样式而非「内部标记：」前缀（node app.js:1301,1325）。
- 帧：hub-9/11 vs node-7/8。

**D6 失败原因独立区** — 结构分叉（漂移；信息层级差异最大的条目之一）
- hub：「失败原因（进程错误输出）」h3 + 独立区（默认 md 渲染 + 查看原文切换，`#drawer-fail`/failRaw），条件 `stderr_tail && (failed||error)`（hub app.js:1377,1402-1406）。
- node：无此区；stderr 藏在「诊断日志」折叠内（node app.js:1329）。
- 失败任务 hub 一眼看到原因，node 需展开折叠。帧：hub-5/6/8 vs node-5/6。

### 任务输入

**D7 任务输入区** — 结构差异（合理场景差异候选；形态问题另计）
- hub：独有「任务输入」h3 + 裸 `<pre>` JSON 平铺 args/stdin/env（hub app.js:1378-1379）。
- node：无此区（提交人=本机主人，表单刚填过，诉求弱）。
- 注：该 pre 是 hub 抽屉唯一未参与 e821bf8 材质深度语法梳理的区块（裸 JSON 平铺、无灰块节奏）。帧：hub-1/2 全帧可见。

### 最终答复

**D8 空答复兜底形态** — 视觉+文案分叉（漂移）
- hub：三版 muted 人话——needs_review「结果还没确认——…」/有 stdout「该任务没有单独的文本答复，输出内容见下方诊断日志与产物。」/否则「该任务没有文本答复（例如等待类任务）…」（hub app.js:1381）。
- node：恒调 mdAnswer，空答复渲染为**白底浮起表面 + 「（空）」**——把「无内容」做成了全抽屉视觉最重的主体，语义倒挂（node app.js:1327,1336-1338）。
- 帧：hub-7 vs node-5/7/8。

**D9 运行中态的答复区** — 结构差异（hub 侧接近 bug）
- hub：运行中 r=null 落进「该任务没有文本答复（例如等待类任务）…」——任务还没跑完就说没有答复，误导（hub app.js:1381 第三分支）。
- node：`!res` 时整段替换为「尚未到终态（执行中）。抽屉每 5s 自动刷新，可先去忙别的。」（node app.js:1326）。
- 帧：hub-1 vs node-1。

### 日志区

**D10 日志区标题/渲染条件/保留期说明** — 文案分叉（已知）+结构分叉
- hub：「运行日志」，`stdout_tail||stderr_tail` 才渲染 details（双空整区消失）+「最多保留各 64KiB。」说明（hub app.js:1382）。
- node：「诊断日志（stdout / stderr 尾部）」，有 res 即恒渲染（超时双空也渲染空 details）+无保留期说明（node app.js:1329）。
- 帧：hub-7（无日志区）vs node-7（空日志区展开）。

**D11 空段兜底与原文视图形态** — 文案+结构分叉（已知）
- hub 渲染态：跳过空段（stdout 有 stderr 无 → 只渲染 stdout 段）；原文视图：两个裸 pre，空段为空串（hub app.js:1407-1413）。
- node 渲染态/原文视图：恒显 stdout+stderr 两段、「（空）」兜底；原文为**单 pre 加 `stdout:`/`stderr:` 行前缀**（node app.js:1344-1350）。
- 帧：hub-3/4 vs node-3/4/6/7。

**D12 原文切换提示语** — 文案微差（随 D6 归并）
- hub 两版：「…进程原始输出请切原文。」（失败原因）/「…空格对齐的进程输出请切原文。」（运行日志）（hub app.js:1404,1409）。
- node 一版（同 hub 日志版，node app.js:1346）。

### 产物区

**D13 产物区形态与文案** — 结构+文案分叉；核心动作差异属**合理场景差异**（任务书边界钦点）
- hub：恒渲染「产物文件」h3 + 空态「暂无已登记产物。中断任务不推断存在可交付产物。」+ 行内「取回」按钮（data-id/index）+「产物经中心端中继从执行机器取回，SHA-256 校验一致才允许下载；自结果确认起保留 24 小时。」footer（hub app.js:1383-1385,1414-1418）。
- node：条件渲染「产物（N 个，保留至确认后 24h）」+ 行内相对路径/字节数/sha256/本机绝对路径 + artifactNote 双路径文案（local「产物在本机：…本机直接打开即可，无需经 hub 取回」/hub 来源「经 hub 管理面 GET …取回」）（node app.js:1311-1314,1328）。
- 帧：hub-2/3 vs node-2。

**D14 产物行样式** — 视觉分叉（漂移）
- hub `.artifact` 有 padding+底部分隔线（hub style.css:291）；node 无 `.artifact` 样式，裸 div（node style.css 无此规则）。

### 时间线

**D15 时间线结构与样式** — 结构+视觉分叉（已知「时间线样式两版」）
- hub：`ol.timeline` 事件流（任务已受理/开始执行/{终态}/人工核对完成）+ 左轨圆点 CSS + needs_review 热点（hub app.js:1419-1431；hub style.css:287-290）。
- node：`ul.timeline` 两行拼接（创建·更新 / 开始·结束·退出码）、内联 `list-style:none`、无任何 CSS（node app.js:1320-1323）。
- 帧：hub-2/3 vs node-2。

**D16 零值时间戳守卫** — 漂移（接近 bug）
- hub：`real()` 过滤 Go 零值 `0001-01-01`（hub app.js:1423）。
- node：timeFull 无守卫，needs_review（重启恢复）任务显示「开始 **1/1/08 05:43**」乱码（node app.js:348-351,1322）。
- 帧：node-8（直接可见）。

**D17 时间格式与时区语义** — 文案/视觉分叉（漂移）
- hub：`fmtTime/fmtFull` = ISO 串切片 +「 UTC」后缀，全站 UTC 口径（hub app.js:602-603）。
- node：`timeShort/timeFull` = `toLocaleString('zh-CN')` 本地时区（node app.js:344-351）。
- 同一时刻 hub「2026-10-24 20:41:00 UTC」/ node「2026/10/25 04:42:15」。帧：hub-2 vs node-2。

### needs_review

**D18 兜底文案** — 文案分叉（已知「needs_review 兜底文案两版」）
- hub：「这个任务结果拿不准，等你确认：执行到一半机器重启了，请核对后登记结论，不要重新提交同一任务。」（hub app.js:1365-1366）。
- node：「这个任务结果拿不准，等你确认——执行到一半机器重启了，请勿重新提交（这是单独的「等你确认」状态，不是失败；核对结论由本机主人在本控制台确认 / 核对记录在 hub 控制台登记）」双路径两版（node app.js:1315-1317）。
- 帧：hub-10/11 vs node-8。

**D19 核对能力与文案承诺** — 结构分叉；node 侧文案-能力不匹配接近 bug
- hub：完整人工核对卡（草稿回填 textarea + 确认结果有效/标记为失败按钮）与已核对记录卡（结论/依据/时间/操作人）+ 时间线「人工核对完成」事件（hub app.js:1432-1450）。
- node：无核对表单、localapi 无 review 端点（server.go:15「后续 review/cancel」），但 reviewNote 声称「核对结论由本机主人在本控制台确认」——**承诺了不存在的能力**（node app.js:1315-1316）。
- 帧：hub-11（表单）/hub-12（已核对记录）vs node-8（仅告警条）。

**D20 needs_review 时间线热点** — 视觉分叉（随 D15 归并）
- hub 终态事件点为时间线唯一 heat 色点（`.timeline li.hl`，hub app.js:1427 + style.css:290）；node 无事件流无热点。

### 终态动作与页脚

**D21 下一步动作区** — 文案/结构分叉；管理动作差异属**合理场景差异**
- hub：终态 notice「任务已归档前保留 24 小时。+ 返回任务列表 →」+ actions「导出此任务 JSON」+ 非终态「取消任务」（hub app.js:1389-1390）。
- node：「终态自带下一步：查看全部任务 /（失败加）回表单重新派单」（node app.js:1330-1331）。
- 导出/取消=hub 管理功能、回表单重派=node 执行者功能（边界钦点保留）；但同义动作文案不同（「返回任务列表」vs「查看全部任务」）、「归档保留 24h」说明 node 端缺失，属漂移。

### 行为差异（走查实测）

**D22 hub 轮询重绘副作用** — 行为分叉（hub 体验缺陷）
- hub 抽屉打开期间 5s 全量重绘 `.drawer` innerHTML：`<details>` 开合态被收回（raw/render 切换态存 S.drawer 存活，开合态是纯 DOM 状态不存活）；harness 实测抽屉内按钮点击被重绘 detach 连续打断（hub app.js:1325-1328）。
- node 终态后 5s 定时器自停，details/阅读位置恒定（node app.js:1258-1263）。

**D23 打开时数据新鲜度** — 行为差异（hub 侧小缺陷，记录备查）
- hub openTask 读 5s 轮询快照 S.tasks：核对完成后立即重开抽屉仍渲染核对前表单态（harness 帧首拍踩中，强刷页面后正常）；node openDrawer 每次现查 API（hub app.js:1198-1218 vs node app.js:1279-1295）。

### 可达性注记（潜伏差异）

node 控制台「我的任务」=本机派单留痕（localapi/local.go:192-193「留痕仅由本机派单写入」），本地无 cancel/review 端点 → node 抽屉的 cancelled 形态与 hub 下发（origin=hub）分支经 UI 不可达。连带潜伏项：node `STATES` 缺 `cancelled`（badge 落原码）、`TERMINAL` 缺 `cancelled`（已取消任务被当非终态**无限 5s 轮询**，node app.js:150,1258-1263）、`ERRORS` 缺 `user_cancelled`。路由一旦打通即暴露。

---

## B. 抽屉外复用组件盘点

| # | 组件 | hub | node | 判定 |
|---|------|-----|------|------|
| C1 | `button()` 助手 | `(label, act, cls, extra)` hub app.js:585 | `(label, act, cls, inner, attr)` node app.js:336 | 签名漂移：参数名/元数不同、语义同为标签内属性串，跨端复制粘贴即错位 |
| C2 | `badge()` 兜底链 | `STATES\|\|GROUP_STATES\|\|s` hub app.js:599 | `STATES\|\|s` node app.js:339 | 漂移（随 C3） |
| C3 | `STATES` 词典 | 含 cancelled/offered/acknowledged | 缺 `cancelled` | 漂移（潜伏，见可达性注记） |
| C4 | `ERRORS` 词典 | 含 `user_cancelled`；全码直查 | 缺 `user_cancelled`；`split(';')` 先切 | 漂移+查找口径功能差异（同 D5） |
| C5 | 时间格式化族 | `fmtTime/fmtFull`（UTC 串） | `timeShort/timeFull`（本地时区） | 漂移，全站页面受影响不止抽屉（同 D17） |
| C6 | `.timeline` CSS | 有（轨道+圆点+hl） | 无（类名在用、靠内联兜底） | 漂移 |
| C7 | `.artifact` CSS | 有 | 无 | 漂移 |
| C8 | `.notice.ok` 变体 | 无 | 有（向导/服务/派单在用，node style.css:77-78） | node 超集，登记备查 |
| C9 | `.drawer` 壳动画+aria | 有 | 无 | 漂移（同 D1） |
| C10 | md 渲染器+md 样式 | shared block 逐字节锁一致（web_md_render_test.go:71-135） | 同左 | 已锁；抽屉组装层不在锁内（仅接入行存在性断言 137-183），阶段 C 可评估升级锁 |
| C11 | `toast` | 玻璃胶囊（P1 B.6） | 简版 | 未深查，登记备查 |

---

## C. 初步分类汇总（供阶段 B 逐条裁决）

**合理差异候选**（双端视角不同——hub 管理者 vs node 执行者/本机主人，倾向保留但措辞可对齐）：
- D7 任务输入区（hub-only；但「裸 JSON 平铺」形态建议随材质语法重梳）
- D13 产物核心动作（取回按钮 vs 本机路径指引）；标题句式/空态/说明语仍属漂移
- D19 的一部分（核对入口归属：hub 下发任务回 hub 核对合理；node 本机任务文案-能力不匹配是另一回事）
- D21 的一部分（导出/取消/回表单重派单）

**漂移**（倾向统一为单一形态，B 定哪边）：
D1-D6、D8、D10-D12、D14-D18、D20-D22、C1-C7、C9

**Bug 级（建议不论裁决结果都修）**：
- D16 node 零值时间戳乱码（缺 `0001-01-01` 守卫）
- D9 hub 运行中态「没有文本答复」误导兜底
- D19 node「由本机主人在本控制台确认」承诺不存在的能力（或补端点，或改文案指路）
- D22 hub 5s 重绘收起 details、打断抽屉内交互
- 潜伏：node TERMINAL 缺 cancelled → 取消任务无限轮询；STATES/ERRORS 缺 cancelled 系原码出镜

---

## 附：截图索引（docs/prototypes/shots-drawer-sync/，1440×2400 fullPage）

| 帧 | 内容 | 关键证据 |
|----|------|----------|
| hub-drawer-1-running | 运行中 | D4/D7/D9（误导兜底）/hub 独有骨架 |
| hub-drawer-2-completed | 完成（md 答复+产物+日志折叠） | D2/D13/D15/D17 |
| hub-drawer-3-completed-runlog-open | 日志展开（渲染态） | D10/D11（跳过空段）/D15 时间线事件流 |
| hub-drawer-4-completed-runlog-raw | 日志原文（双裸 pre） | D11 |
| hub-drawer-5-failed-stderr | 失败·stderr（失败原因区渲染态） | D6 |
| hub-drawer-6-failed-stderr-raw | 失败原因原文 | D11/D12 |
| hub-drawer-7-failed-timeout | 失败·超时（无 stderr→无失败原因区无日志区） | D5/D10 |
| hub-drawer-8-failed-policy | 失败·白名单（有 stderr） | D5 |
| hub-drawer-9-cancelled | 已取消（user_cancelled 通知条+无取消按钮） | D5/D21 |
| hub-drawer-10-needsreview-form | 等你确认·空表单 | D18/D19 |
| hub-drawer-11-needsreview-filled | 等你确认·草稿已填 | D18/D19（草稿回填） |
| hub-drawer-12-reviewed | 已核对记录卡+时间线核对事件 | D19/D20 |
| node-drawer-1-running | 运行中（尚未到终态整段） | D4/D9（node 正确处理） |
| node-drawer-2-completed | 完成（md 答复+产物本机路径+诊断日志折叠） | D2/D13/D15/D17 |
| node-drawer-3-completed-log-open | 诊断日志渲染态（stdout+stderr 双段恒显） | D10/D11 |
| node-drawer-4-completed-log-raw | 诊断日志原文（单 pre+stdout:/stderr: 前缀+（空）） | D11 |
| node-drawer-5-failed-stderr | 失败·stderr（无失败原因区+白卡（空）答复） | D6/D8 |
| node-drawer-6-failed-stderr-raw | 同上原文 | D11 |
| node-drawer-7-failed-timeout-log-empty | 失败·超时（空日志区+（空）×2+白卡（空）） | D8/D10 |
| node-drawer-8-needsreview | 等你确认·本机（双告警条+无核对表单+**1/1/08 乱码时间**） | D16/D18/D19 |

harness：docs/prototypes/shoot-drawer-sync.mjs（惯例沿用 shoot-copy-sweep.mjs：真实 hub+nodeA+nodeB、播种全状态、playwright 全页截图；抽屉内交互经 evaluate 触发以规避 hub 5s 重绘 detach——本身即 D22 证据）。
