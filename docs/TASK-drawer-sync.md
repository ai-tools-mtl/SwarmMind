# TASK: 双端任务抽屉展示风格统一（含「双端统一」规约入档）

> 编排：Hana，2026-10-25 04:30。伦哥发现：node 端任务回复/日志有强调展示（e821bf8 材质深度语法），hub 端没有，要求检查双端差异、统一风格、三方探讨分歧、规约写入开发文档。

## 背景（Hana 摸底结论，供起点）

md 渲染器与 md 样式已是双端 shared block 逐字节一致（web_md_render_test.go 有锁），CSS 变量一致。差异在**抽屉组装层**：

1. hub 抽屉（drawerHtml，hub app.js:1362）：任务输入裸 `<pre>` JSON 平铺、失败原因独立区、运行日志 details、时间线 ol.timeline——区块平铺，无层级节奏。
2. node 抽屉：e821bf8 材质深度语法——最终答复白底浮起（主角）、机器数据沉底灰块（配角）、日志收「诊断日志」折叠。
3. 已知文案/形态分叉：「运行日志」vs「诊断日志（stdout/stderr 尾部）」；原文切换 hub 双 pre vs node 单 pre 双段；空段兜底 hub 跳过 vs node 「（空）」；needs_review 兜底文案两版；时间线样式两版。

## 阶段 A：差异盘点（zcode，只读不改）

双端任务抽屉（含所有状态形态：运行中/完成/失败/取消/needs_review）逐区对比，产出差异清单：
- 每条差异：位置（文件:行）、两端现状、截图对照（docs/prototypes/shots-drawer-sync/，png 留本地）、性质判定（视觉分叉/文案分叉/结构分叉/合理场景差异）
- 特别注意：哪些差异是双端视角不同（hub 管理者 vs node 执行者）的**合理差异**，哪些是漂移——初步分类即可，裁决在阶段 B
- 同时盘点抽屉之外的复用组件（badge/notice/artifact 行等）两端是否有其他漂移

## 阶段 B：三方探讨与规约制定（Hana 主持 + codex + zcode 报告）

对阶段 A 清单逐条裁决：统一为何种形态 / 保留为合理差异（写明理由）。产出：
1. 统一裁决表（每条差异的去向）
2. **「双端 UI 统一规约」写入开发文档**（伦哥钦点必须落档）：写入 AGENTS.md 错误模式清单（新增条目：双端展示漂移）+ 具体规约落点（shared block 机制扩展/抽屉结构契约，落点由三方探讨定，倾向 SPEC web 章节或 AGENTS.md，避免与既有 web_md_render_test 锁重复）

---

# 阶段 B 裁决（Hana 终裁，2026-10-25 05:10）

> 输入：zcode 阶段 A 差异盘点（TASK-drawer-sync-findings.md，D1-D23+C1-C11）+ codex 裁决建议书。三项优先拍板如下，其余按 codex 建议表机械执行，个别调整随条注明。

## 三项优先拍板

1. **D17 时间口径：本阶段统一 UTC + 显式标注**。理由：管理台面向跨机器排障，与日志/导出对齐优先于本地阅读习惯；node 对齐 hub（影响面可控）。契约：完整时间 `YYYY-MM-DD HH:mm:ss UTC`，解析后转 UTC（禁止字符串切片后硬贴 UTC 后缀）；存储/API/导出不动。全站迁移独立成批（C4）。后续若要本地时区，另立「全站显示时区」任务。
2. **D19 node 本机核对能力缺口：文案如实修正，不补后端**。本机来源任务改为「请人工核查实际结果；本控制台暂不支持登记核对结论，请到 hub 管理台登记」；hub 来源才指向 hub。node review 端点补齐另立任务（超出本次展示层边界），登记需求池。
3. **D22 双端抽屉刷新以稳定 DOM 为硬验收**。codex 指出 node 端同样整抽屉 innerHTML 重建，两端一起修：首次开抽屉建壳+稳定分区；同任务刷新只更新变化数据区；details 开合/原文模式/草稿/busy/焦点/滚动状态保持节点身份；数据无变化不碰 DOM；迟到请求按抽屉会话归属丢弃；不得重放入场动画。「重建后恢复 open」「终态停轮询」均不算合格终解（hub 核对记录可在终态后变化）。

## 其余条目去向

- **bug 级全修**：D16 零值时间戳（建立共同有效时间判定，零值/非法不生成事件）、D9 运行中误导兜底（按状态优先判定）、D19（见上）、cancelled 潜伏漂移（node STATES/TERMINAL/ERRORS/badge 补齐，合成取消态验证，不扩后端）、D23（按 codex 纠正：查 review 回写与轮询/开抽屉的竞态，非「必然旧快照」）。
- **统一到 node 形态（材质层级基准）**：D14 产物行样式（node 补 CSS）、D15/D20 时间线（node 对齐 hub 的 ol.timeline 事件流+热点+零值守卫）、日志区文案「诊断日志（stdout / stderr 尾部）」两端统一、原文切换统一单 pre 双段、空段兜底「（空）」统一。
- **统一到 hub 语义（管理者视角保留）**：任务输入区（D7）保留 hub 独有但收进 details 折叠+沉底灰块（配角定位，与 e821bf8 材质语法一致）；失败原因区（hub 独有）保留但样式统一；导出/取消按钮 hub 独有（白名单）。
- **文案统一**：D18 needs_review 兜底（hub 主文案+双端语义一致）、D21「查看全部任务」为标准词（含 hub）、归档保留说明两端同有。
- **合理差异白名单（保留+入档理由）**：D13 产物语义（取回 vs 本机路径）、D21 导出/取消/重派单视角差异、node 本机路径指引、badge 语义差异（如果有）。
- **规约入档分工（按 codex 案）**：SPEC 六.五新增「双端 Web 展示一致性契约」（七条长期契约）+ AGENTS.md/CLAUDE.md 新增「禁止双端展示漂移」错误模式（含自查口径，指向 SPEC）+ 入档时核对「局部刷新禁止整层 innerHTML 重建」既有规约的实际文档落点，一并补正式条目（含局部更新细则）+ TASK 文档留裁决证据。INTERACTION.md 若有核对入口描述同步。
- **测试分工**：新增抽屉一致性测试锁共用状态词典/时间工具/组件样式（不锁整个 drawerHtml 字节）；行为测试锁刷新前后节点身份/details 开合/草稿/迟到应答；旧接入断言同步更新不双轨。
- **批次拆分**：C1 规约落档 → C2 bug+局部更新 → C3 结构文案材质统一 →（C4 全站 UTC、C5 组件收敛作后续批次，本轮不做，登记任务池）。按路径暂存禁 add -A，每批自洽可验证。

## 阶段 C 实施边界（本轮）

本轮流水线交付 C1+C2+C3（抽屉统一主体）。C4/C5 登记后续。验收：新增行为测试+一致性锁绿，全量 -race 绿，三层走查（抽屉全状态矩阵 1440/1276+深层交互态+迟到应答场景）。

---

# 阶段 D 终审意见（codex，2026-10-25 05:50，必须修 6 + 建议修 2，修复以此为准）

## 必须修

1. **迟到应答竞态**：抽屉刷新请求无失效判定，本地核对回写后旧列表/旧抽屉数据仍可覆盖新态。需数据版本判定：本地回写使在途请求失效。
2. **核对回写与列表提交乱序竞态**（hub app.js:505/2795）：已复现——旧列表含另一任务变化，POST 先成功，旧列表后提交，review 记录卡可被退回表单。列表提交需识别并拒绝过期于本地核对回写的数据。
3. **D22 节点身份与 busy 没保住**（hub app.js:1535/312/2790）：草稿进了模板字符串，编辑后分区缓存与模板不同，离开 textarea 后刷新替换整个 review 区——浏览器复现 textarea/按钮身份均变、按钮 disabled true→false 而 busy 键仍在。修法：review 表单区等交互分区一次创建稳定节点+状态驱动更新（busy 由状态驱动 disabled，不靠重建）；数据分区继续 diff。
4. **D9 仍按 result 判定**（hub app.js:1493、node app.js:1478）：cancelled+result=null 实际显示「尚未到终态（已取消）」（后端合法态，cancel.go:50）。状态优先：terminal(t.status) 先行。
5. **时间契约两处漏**：①hub app.js:263/node app.js:187 受理/终态/核对事件无完整有效时间守卫（零值/非法不生成事件未达成）②hub app.js:1530 抽屉核对记录仍用旧 fmtFull，未解析转 UTC，应 fmtUTC+空值兜底。
6. **测试必须真行为断言**：web_drawer_sync_test.go:119 主要锁源码字符串，:177 把错误实现（!S.drawer 守卫）锁成必需——删除并改行为级；shoot-drawer-sync-c3.mjs:219 保存了 textarea 引用却只断言值不断言身份，未覆盖在途 busy/迟到应答/核对列表乱序——补齐（延迟+乱序应答驱动，验证焦点/滚动/节点身份/无变化零写入）。

## 建议修

7. SPEC.md:549 GROUP_STATES 徽标兑底误列为 node 独有，实际属 hub——修正。
8. node app.js:1392 D1 关闭立即清层未对齐 hub 出场动画——补齐或明确豁免声明。

## 终审已认可（不要动坏）

分区 diff/未变化分区保留/details 开合回放/原文模式保存/刷新不重放入场动画主体、node 会话+请求双序号守卫、D19 撤承诺、cancelled 词典补齐、C3 时间线/日志/输入折叠/头部骨架/白名单、SPEC 七条+AGENTS/CLAUDE 两错误模式落档。

---

# 复审节（codex 阶段 D 复审，2026-10-25 06:20，3 必须修）

1. **P1 迟到单查抢开抽屉**（hub app.js:2793 刷新/1340 悬空补查）：以 !S.drawer 判开层不够，已浏览器复现关闭后旧应答抢开、补查期间换任务被旧应答抢开。需抽屉会话归属+请求顺序校验（关闭/换任务后在途应答一律丢弃，不重开层）。
2. **P2 无条件 DOM 写入**（hub app.js:1591）：状态更新无条件设 disabled/textContent，MutationObserver 实测状态不变仍产生 2 次 childList 变更。需比较后再写。
3. **P2 行为测试缺口**（shoot-drawer-sync-c3.mjs）：零写入断言只比身份/滚动检测不到 #2；缺单查迟到后关闭/换会话/同任务乱序/单查跨回写失效四场景。需真实应答控制+mutation 断言。

其余全部通过：数据版本接线/稳定表单主体/D9/时间守卫/fmtUTC/GROUP_STATES 归属/D1 豁免。

## 复审修复记录（zcode，2026-10-25，3 必须修全闭合）

- **必须修 1（P1 迟到单查抢开抽屉）**：`!S.drawer` 判层升级为「抽屉会话归属 + 请求顺序」双守卫。新增 `S.drawerQuerySeq` 单查请求序号（openTask 悬空补查 / drawer-refresh 两路径共用单调计数：任一新单查发出即作废更早在途应答含快照——同任务连点乱序、补查期间又查他任务时仅最新应答可落地）；会话归属以层世代 `S.layerGen` 判定（应答仅在「发起时层世代未变」时允许开层/顶替——关闭、换任务、期间开过任意层都使世代变化，在途应答一律只落快照不重开层，错误 toast 同样按归属抑制）。
- **必须修 2（P2 无条件 DOM 写入）**：applyDrawerFormState 属性写入一律先比后写——disabled 与标签（data-label /「登记中…」）先比较再写，textContent 同值覆写（每轮 2 次 childList 变更，codex MutationObserver 实测）归零，无变化长驻层零打扰。
- **必须修 3（P2 行为测试缺口）**：shoot-drawer-sync-c3.mjs 补四场景+零写入断言（真实应答控制 = route 压住/改写/延迟放行）：① 单查迟到后已关闭（刷新压住→关抽屉→放行：不重开层只落快照）② 补查期间换会话（悬空补查压住→开另一任务→不顶替→关闭→放行：跨换会话+关闭均不抢开）③ 同任务请求乱序（首刷压住并改写 running 旧快照、次刷直通先应用：迟到旧应答不回退列表与抽屉）④ 单查跨本地回写失效（悬空补查压住→取消成功 taskVer+1→放行回写前旧快照：不落列表不抢开层）⑤ 无变化零写入（MutationObserver 监听 review 分区，状态不变重复应用表单状态+真实轮询一轮零记录）。**变异抽验双向证实**：全还原（!S.drawer 判层+无条件写入）→ P1①/P2② 断言红、P1② 幻影抽屉物理阻断后续交互（复现 codex 浏览器现象）；定向还原（仅去 seq/数据版本守卫）→ P1③/P1④ 断言红、P1①/P1② 保持绿——各断言各自钉住各自维度。修复版 35 PASS / 0 FAIL。
- **驱动外修复（时间边界 flake，全量验证拦路）**：local_test.go TestLocalAgentsAndStatus 种子 `now-1h` 在 UTC 零点后 1 小时内运行跌入昨日被正确排除（2026-10-25 08:11 CST 实测全量红）——「今日」种子改锚定当日 UTC 起点 +1h/+2h，统计口径不变、运行时刻免疫。
- **验证**：`go test ./... -race -count=1` 19 包全绿；GOOS=linux/darwin/windows build + linux/darwin vet 干净；六 shared block 双端逐字节一致；harness 35 PASS / 0 FAIL（含新 5 断言）；登录环节补 pageerror 捕获+失败截图（环境瞬态超时一次，未复现，诊断留存）。

## 第三轮修复记录（zcode，2026-10-25，1 必须修闭合）

- **必须修（换任务不换代）**：openTask 直切路径（层内已有抽屉、目标任务在 S.tasks 中）不递增 `S.layerGen`，任务会话切换未使旧单查失效——A 刷新压住 → openTask(B) → openTask(A) → 放行旧 A 的 404，世代未变恰命中 `S.drawer.id===A` 弹归档 toast。修法：直切目标任务时递增层世代（换任务=新抽屉会话，对齐复审必须修 1 的「换任务使世代变化」设计意图；同任务重开不换代，乱序仍归序号守卫），并补偿换代拦下的首开入场 rAF（同帧内换任务时补点亮 overlay .open）。harness 场景 9 复现序列三断言（切 B 壳不重开/旧 404 不弹归档 toast/旧应答不扰现会话）；**变异验证**：仅去换代一行 → 恰归档 toast 断言红、其余 37 条绿。node 端无此缺口（drawerSeq 每次 openDrawer 必递增）。
- **验证**：`go test ./internal/node/... ./internal/hub/... -race -count=1` 全绿（node 7 包 + hub 6 包）；c3 harness 38 PASS / 0 FAIL（含新 3 断言）。

## 阶段 D 修复记录（zcode，2026-10-25，必须修 6 + 建议修 2 全闭合）

- **必须修 1+2（数据版本判定）**：hub 新增 `S.taskVer`（taskId → 本地回写版本，review/cancel 成功 +1）；列表提交点（loadAll commit）按「发起时版本快照 vs 提交时版本」判定，版本已变的任务以本地权威态（回写应答）原位替换、其余照常提交——列表提交永不使任务回退到本地回写之前（codex 复现的「旧列表退回记录卡」序列被拦）；单查两路径（openTask 悬空补查/drawer-refresh）版本不符即弃应答。终审认可项（D23 抽屉归属守卫）原样保留。
- **必须修 3（稳定节点 + 状态驱动 busy）**：reviewSection 表单形态改零动态值（草稿/busy 一律不进模板串），表单结构仅首建/记录卡切换时写入；新增 `applyDrawerFormState`（syncDrawerZones 每次分区应用后调用）：草稿回填（未聚焦且值不同才写）、busy 由状态驱动 disabled 与「登记中…」标签（data-label 恢复原文案），ACT.review 弃手动 DOM 拨钮改状态驱动。数据分区照常 diff。
- **必须修 4（D9 状态优先）**：答复区收进「抽屉答复与日志 shared block」新增 `drawerAnswerHtml(t, res, needs)` 双端同文——TERMINAL.has(t.status) 先行，cancelled+result=null（cancel.go Result 留空合法态）落无答复人话兜底，仅非终态无结果才显示「尚未到终态」。
- **必须修 5（时间契约补漏）**：①「抽屉事件与错误 shared block」drawerTimelineHtml——受理/终态/核对三处一律 realTime 完整有效判定，无证据时刻连事件骨架一起不生成（此前只守 started_at/fin，受理与核对事件可出空 `<small>` 行）；②hub 核对记录卡 fmtFull→fmtUTC+「—」兜底（解析转 UTC，弃切片）。
- **必须修 6（测试真行为断言）**：web_drawer_sync_test.go 删三组实现锁定——分区机制关键行（与抽块逐字节锁重复）、node/hub 分区接线正向字符串、D23 守卫逐字串+S.drawer 重赋形态扫描（!S.drawer 守卫系错误实现被锁成必需）；保留五个 shared block 逐字节锁+契约关键行（新增 D9/时间守卫行）+整层重建反向禁令+终态集单一来源+fmtFull(t.review.at) 禁令；行为面移交 shoot-drawer-sync-c3.mjs：新增 review 表单节点身份（focus/失焦双周期）/焦点保持/滚动保持/review 分区零写入/在途 busy disabled+分区重算不回退+节点同一/旧列表迟到提交不回退记录卡（route 先抓快照再压住复现乱序）/D9 cancelled+result=null 真实服务端态/列表 payload 合成零值时间全事件不生成。
- **建议修 7**：SPEC 六.五第六条 GROUP_STATES 徽标兜底从 node 独有移至 hub 独有（hub 群聊页专属词典，node badge 不取词）。
- **建议修 8**：node 关闭直清（无 280ms 出场动画）明确豁免入档：SPEC 六.五第六条白名单 + node app.js closeDrawer 注释（#layer 三抽屉共用直清家族，让位路径要求立即清场，未引入 layerGen 前豁免；直清同时即刻作废在途应答，无 closing 窗口竞态面）。
- **契约入档**：SPEC 六.五第三条局部更新铁律补两句——交互分区一次创建稳定节点+状态驱动更新（草稿/busy 不进分区模板串，busy 由状态驱动 disabled）；本地回写使在途列表/单查旧快照失效（数据版本判定），列表提交不得使任务回退到本地回写之前。
- **验证**：`go test ./... -race -count=1` 全绿；GOOS=linux/darwin build+vet 干净；c3 harness 行为断言全 PASS（迟到/乱序/busy 均实测驱动）；shared block 双端 diff 逐字节一致；web_md_render_test.go 旧接入断言随 drawerAnswerHtml 收块同步更新（不双轨）。

## 阶段 C：实施（zcode）

按裁决表统一双端；shared block/一致性锁测试同步扩展（抽屉组装层若可锁则加锁）；文案统一。

## 阶段 C 实施记录（zcode，2026-10-25，批次 C1→C2→C3）

### C1 规约落档

- SPEC 六.五新增「双端 Web 展示一致性契约」七条（共用件单一来源/材质层级基准/局部更新铁律/时间口径/文案单一出处/合理差异白名单/变更纪律）；白名单条目即本文件阶段 B 裁决的合理差异清单。
- AGENTS.md/CLAUDE.md（同步维护）新增两条错误模式：「长驻交互层局部刷新禁止整层 innerHTML 重建」（含局部更新细则与自查命令；入档核对结论：既有文档无此规约落点，本次为正式首立）+「禁止双端展示漂移」（自查指向 SPEC 六.五与一致性锁测试）。
- INTERACTION.md 核对入口描述为旅程级（无抽屉文案引用），无双端分叉，无需同步。

### C2 bug + 局部更新（稳定 DOM）

- 双端 app.js 新增五个 shared block（抽块逐字节锁，web_drawer_sync_test.go）：抽屉展示词典（STATES/ERRORS/TERMINAL/REVIEW_ACTIONS，node 补 cancelled 系）、抽屉时间工具（realTime/fmtUTC，解析转 UTC+零值守卫）、抽屉事件与错误（drawerTimelineHtml/taskErrorNotice）、抽屉答复与日志（mdAnswer/drawerLogInner，C3 批次入块）、抽屉分区更新（DRAWER_ZONES/drawerShellHtml/drawerZoneSwap/drawerZonesApply）。
- D22 硬验收：双端抽屉改「建壳+稳定分区（data-zone）+缓存 diff 局部写入」——数据无变化不碰 DOM；分区替换回放 details 开合态；原文切换（log-raw/fail-raw）与轮询共用 syncDrawerZones 入口保缓存一致；node 壳补 role=dialog/aria/焦点入层/关闭焦点返回/入场动画（D1 对齐 hub）；迟到防护保持 node drawerSeq+drawerReqSeq，hub 新增三处 D23 守卫（openTask 悬空补查/drawer-refresh/review 登记的迟到应答按抽屉归属丢弃，不得顶替会话）。
- bug 级：D16（事件流 realTime 守卫，node-8 乱码消失）、D9（运行中不落「没有文本答复」兜底，双端统一「尚未到终态」）、D19（node 文案如实：本机「本控制台暂不支持登记核对结论，请人工核查实际结果，并到 hub 管理台登记」/hub 来源「核对结论请到 hub 管理台登记」，主文案 D18 同 hub）、cancelled 潜伏漂移（词典补齐后 node badge/TERMINAL/ERRORS 全就位，取消态不再无限轮询）。合成取消态经全状态矩阵 harness hub-drawer-9 帧验证（node 侧本地不可达保持白名单现状）。

### C3 结构文案材质统一

- 统一到 node 形态（材质基准）：D14（node 补 .artifact/.timeline CSS，双端抽行比对锁）、D15/D20（node 弃 ul 两行拼接改 ol.timeline 事件流+热点）、日志区（双端「诊断日志（stdout / stderr 尾部）」+条件式渲染+「最多保留各 64KiB。」+恒两段「（空）」兜底+原文单 pre 双段，全部收进 shared block）、产物标题「产物文件」恒渲染+空态同文。
- 统一到 hub 语义（管理者视角）：D7 任务输入收 details 折叠沉底（「任务输入（原样 JSON）」）；失败原因区 hub 独有保留（failInner 单 pre 原文形态不变）；导出/取消 hub 独有。
- 文案统一：D5（taskErrorNotice 组合码首段查词典，人话在前+原码+退出码，废除「内部标记：」）、D18（主文案逐字同源）、D21（标准词「查看全部任务」含 hub；归档保留说明两端同有）、D2/D3（头部三行紧凑形同构：任务详情·id 标题行/agent@node+badge/追踪·提交方·创建于·恢复密钥 元信息行，node 加「来源」标注）、D4（五态介绍条双端同有）、D8（空答复三版人话兜底双端同文）、D17（抽屉时间全 UTC+显式标注；页面级 fmtTime/fmtFull 留待 C4）。
- 白名单保留：D13 产物语义（hub 取回按钮/node 本机绝对路径指引+来源标注）、D21 动作差异（hub 导出/取消、node 回表单重新派单）、hub 失败原因独立区、hub GROUP_STATES 徽标兜底。

### 测试与走查

- 一致性锁：internal/node/localapi/web_drawer_sync_test.go（五个 shared block 抽块逐字节+关键行；分区接线与 D22/D23 反向禁令；组件样式双端抽行比对；跨端同文文案/旧形态禁令）。web_md_render_test.go/web_cfg_edit_drawer_test.go 旧接入断言随改造同步更新（不双轨）。
- 行为走查（docs/prototypes/shoot-drawer-sync-c3.mjs，真实 hub+nodeA，15/15 PASS）：hub 12s 两轮询周期 logs 分区节点身份不变/details open+原文保持/真实点击无 detach/草稿跨轮询保值/核对登记就地换记录卡；node 运行中→完成终态迁移 #drawer 壳节点同一/D4/D8/D9/D16/D19/D21/aria 逐项过。
- 全状态矩阵（shoot-drawer-sync.mjs 更新两帧语义：node 超时双空日志区按 D10 条件式整区消失）20 帧重生成，png 留本地 docs/prototypes/shots-drawer-sync/。

## 阶段 D：审查与走查（codex 审 + Hana 三层走查）

codex 审规约落地与回归；三层走查（E2E 断言+整页截图 1440/1276+弹层深层交互态）；全量 -race 绿。

## 边界

- 不改 API/协议/Go 业务逻辑（纯展示层+测试+文档）。
- 合理差异保留：hub 管理功能（导出 JSON/取消/核对登记）与 node 执行者功能（本机路径指引/重派单）不强求对称。
- 提交等伦哥点头。
