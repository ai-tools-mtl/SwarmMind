# 任务书：任务结果 Markdown 渲染（最终答复 + 诊断日志）

任务 ID：t20261023-969e
日期：2026-10-23
提出人：伦哥（「将最终答复和诊断日志这里的内容，整理成可以渲染md格式的」）
编排：Hana

## 现状（Hana 已核）

- node 抽屉（app.js:959-961）：最终答复 `<pre>${esc(res.final_answer)}</pre>`；诊断日志 details 内 `<pre>stdout/stderr</pre>`——纯文本，agent 写的 markdown（## 标题、**粗体**、``` 代码块、表格）全部以源码形态裸奔。
- hub 抽屉（app.js:1229-1234）：最终答复 `<p style="white-space:pre-wrap">`、失败原因 pre、运行日志 details 内 pre——同病。
- 两端前端均为原生 JS 单文件（go:embed），无第三方前端库（SPEC 零依赖哲学延续到前端）。

## 设计裁决（Hana 定，按此执行）

1. **手写渲染器，不引第三方库**。理由：内容来自网内 agent 的任意输出，控制台持有 admin_token/local_token/PAT，XSS 面必须最小化；escape-first 架构自写 ~200 行最可控。
2. **最终答复：默认 md 渲染**。
3. **诊断日志：默认 md 渲染 + 「查看原文」切换 link**（details 内小切换）。理由：日志多为进程原始输出，`df -h` 类空格对齐表格被 md 解析会散架；agent 日志又常带 md 结构。默认渲染满足主诉求，误渲染一键回原文，两全。
4. **hub 端同步**：最终答复/失败原因/运行日志同款处理（渲染器两端 app.js 各嵌一份，顶部注释「双端同步维护」，资产断言锁两端一致性）。

## 渲染器规格（mdToHtml(text) -> html，纯函数）

**铁律（安全架构）**：先对全文 HTML escape（& < > " '），再在已转义文本上做结构/行内标记解析——任何 HTML 直通路径不存在，agent 输出 `<script>`/`<img onerror>` 必然显示为字面文本。

**块级（行级解析）**：
- 围栏代码块 ```（可带语言标注 → 语言徽标；内部文本只 escape 不解析行内标记）
- 标题 # ~ ######（h1-h6 映射为抽屉内分级字号）
- 无序/有序列表（支持两层缩进嵌套；任务描述里嵌套不宜更深）
- 引用 >（blockquote 样式）
- 分隔线 --- / ***（单独成行）
- 表格：`| a | b |` 头行 + `|---|---|` 分隔行 + 数据行；未闭合/不匹配则整块按普通段落降级
- 普通段落：连续非空行合并为 <p>，段内换行 <br>

**行内（已转义文本上的受控正则）**：
- `code` → <code>
- **粗体**、*斜体*（不配对则字面保留）
- [文本](url) → <a>；**url 白名单 http:// https:// / 相对路径**，其余（javascript: 等）降级为纯文本；输出带 target="_blank" rel="noopener noreferrer"
- 不支持：图片、脚注、HTML 直通、删除线（agent 输出罕用，做了反而扩攻击面）

## 验证（三层走查）

1. **功能**：资产断言（两端 /app.js 含 mdToHtml 标记 + 关键解析行；两端渲染器一致性锁——同一标记行两端都在）；若有 Go 侧可单测的纯函数抽离则加单测（渲染器若写在 JS 则资产断言 + headless 实测覆盖）。
2. **页面视觉**：抽屉打开态截图（1400 + 1276），构造含 标题/列表/代码块/表格/链接/粗体 的假结果任务实测渲染效果。
3. **深层态走查（重点帧）**：① 恶意输入帧：`<script>alert(1)</script>`、`[x](javascript:alert(1))`、`<img src=x onerror=alert(1)>` ——渲染后均为字面文本、无脚本执行（CDP evaluate 验证无 alert）；② 诊断日志原文切换帧；③ df -h 类空格对齐日志切原文帧（可读性对照）；④ 嵌套列表/表格帧；⑤ 空答复/空日志兜底文案帧。
4. `go build ./...` 与 `go test ./... -count=1` 全绿。

## 完成记录

docs/TASK-md-render.md 末尾追加完成记录（渲染器规格实现说明 + 改动清单 + 每帧截图 + 测试结果）。不要 git commit。

---

## 完成记录（2026-10-23，zcode 实现）

### 渲染器规格实现说明

手写 `mdToHtml(text) -> html` 纯函数（~90 行）+ `mdBlocks`（块级行解析）/ `mdInline`（行内标记）两个内部层，按任务书规格逐条落地：

- **escape-first 铁律**：入口先对全文做与 `esc()` 同款的 `& < > " '` 全量转义，此后所有解析都在已转义文本上进行——引用行首 `>` 以 `&gt;` 形态匹配、链接 url 天然无法逃出属性（`"` 已是 `&quot;`）、任何 HTML 直通路径不存在。
- **块级**：围栏代码（``` 可带语言标注 → `.md-lang` 徽标，内部只直出已转义文本不解析行内；未闭合到文末按已闭合处理）｜标题 `#`~`######`（h1-h6，样式分级字号）｜无序/有序列表（行首 ≥2 空格为二级、更深并入二级=两层嵌套封顶；同层标记类型分段，`- a` 后紧跟 `1. b` 不并同一列表、序号不丢）｜引用 `>`（连续行收齐后递归 `mdBlocks`，引用内可再含标题/列表）｜分隔线 `---`/`***` 单独成行｜表格（头行 + `|---|---|` 分隔行 + 数据行；分隔行每格须为 `:?-+:?`；头/分隔列数不等 → 整块按段落降级）｜段落（连续非空行合并 `<p>`，段内换行 `<br>`；块级起始行断开；表格降级孤立行单独成段防死循环）。
- **行内**（顺序：`` `code` `` 占位保护（内容不参与其他规则）→ `**粗体**` → `*斜体*`（两端紧贴非空白才配对，不配对字面保留）→ `[文本](url)`）：url 白名单 `http(s)://` 与 `/` 相对路径，`javascript:`/`data:` 等一律降级纯文本（含方括号原样保留）；白名单命中输出 `target="_blank" rel="noopener noreferrer"`。不支持图片/脚注/HTML 直通/删除线/强调嵌套（`***x***` 字面保留）。
- **空兜底**：空白输入返回空串，接入层兜底「（空）」muted 文案。
- **双端同步**：渲染器整块（含注释）与配套样式块在 node/hub 两端 app.js / style.css **逐字节一致**，`TestWebMdRender` 抽块比对锁一致性。

### 改动清单

| 文件 | 改动 |
|---|---|
| internal/node/web/app.js | ① 「md 渲染器 shared block」整块嵌入（`/* ===== begin/end ===== */` 标记包裹）；② `refreshDrawer` 抽出 `taskDrawerHtml(t)` 纯渲染函数（修一次抽取时漏改 return 的实现 bug——抽屉渲染成 "undefined"，headless 走查首帧即抓出）；③ 最终答复 `<pre>` 纯文本 → `mdAnswer()`（md 渲染 + 空兜底）；④ 诊断日志 details 改 `#log-wrap` + `logSectionHtml(res)`：默认 md 渲染（stdout/stderr 分节 + `.md-log-label` 流标签），「查看原文/查看渲染」link 切换，原文视图沿用单 pre 双段旧形态；⑤ 事件委托增 `case 'log-raw'`：状态存 `S.drawer.logRaw`，只重绘 `#log-wrap`（details 开合与滚动位置不被冲） |
| internal/hub/web/app.js | ① 同款渲染器 shared block 逐字节嵌入；② `drawerHtml` 三处接入：最终答复 → `mdAnswer()`，失败原因 → `#drawer-fail` + `failInner(t)`（md+切换），运行日志 → `#drawer-runlog` + `runLogInner(t)`（md+切换，原文视图沿用双 pre 旧形态）；③ 新增 `mdAnswer/failInner/runLogInner`；④ ACT 表增 `'log-raw'`/`'fail-raw'`：状态存 `S.drawer.logRaw/failRaw`，只重绘对应内层容器——5s 轮询全量重绘时状态保持 |
| internal/node/web/style.css | 「md 渲染样式 shared block」：`.md-body` 排版（h1-h6 分级字号 17/15/14/13.5/13/12.5px、列表/引用/链接）、`.md-table`（auto 宽 + 横向滚动，收紧内边距不复用列表格大间距）、`.md-code`+`.md-lang` 语言徽标、`.md-log-head`/`.md-log-label` |
| internal/hub/web/style.css | 同款样式块逐字节嵌入；**修复走查实测抓出的泄漏**：hub 全局 `th{position:sticky;top:84px}`（任务列表表头吸顶）泄进 md 表——`.md-table{overflow:auto}` 构成滚动容器，表头被吸到容器顶 +84px 处盖住 studio 数据行。修法：`.md-body th{position:static}` 写入 shared block 两端同步（node 侧全局 th 无 sticky，该声明在 node 恒为 no-op，不改变 node 渲染） |
| internal/node/localapi/web_md_render_test.go（新增） | 三层走查之功能层：① 双端一致性锁（app.js 渲染器块 + style.css 样式块抽块逐字节比对，比标记行更硬）；② 关键解析行断言（escape-first、围栏/标题/引用/分隔线/两层嵌套/表格降级/段落 br/code 占位/粗斜体配对口径/链接白名单+noopener）；③ 接入行断言（node：mdAnswer/#log-wrap/log-raw/切换按钮；hub：mdAnswer/#drawer-fail/#drawer-runlog/fail-raw/log-raw）+ 旧纯文本形态移除锁（node「details 直挂 pre」、hub 两处 white-space:pre-wrap）；④ 样式载体断言（含 `.md-body th{position:static…}` sticky 泄漏修复回归锁） |

### 三层走查

**1. 功能层**：`TestWebMdRender`（上表）+ Node 直跑渲染器用例 28 项全过（标题/围栏语言徽标/未闭合围栏/嵌套列表/混排标记分段/表格及降级/引用递归/分隔线/粗斜体不配对字面/链接白名单（含 `JaVaScRiPt:`、`data:`、变体 scheme 全降级）/code 占位/段落 br/空输入/孤立行不卡死/结构完整性）。

**2. 页面视觉层（headless Chromium 实测，真跑 hub+node 回环 + 假 agent 产 md 结果，1400/1276 双宽）**：

- docs/TASK-mdrender-drawer-1400.png：node 抽屉打开态——最终答复 md 渲染（h1/h2/表格/有序+嵌套列表/引用/`bash` 语言徽标代码块/粗体/行内 code），诊断日志 md 态 + 切换 link
- docs/TASK-mdrender-drawer-1276.png：1276 视口同态，700px 抽屉排版无回退异常

**3. 深层态五帧（1400）**：

- ① 恶意输入：docs/TASK-mdrender-evil-1400.png——`<script>`/`<img onerror>`/`javascript:` 链接全部字面显示；CDP 断言：alert 钩子未触发、抽屉内 script/img 实元素 0 个、`.md-body a` 仅白名单 href、粗体/code 照常渲染
- ② 原文切换：docs/TASK-mdrender-log-raw-1400.png——「查看原文」→ 单 pre 原始输出，details 开合保持（局部重绘验证）；切回「查看渲染」见 docs/TASK-mdrender-log-md-1400.png
- ③ df -h 对齐对照：md 态空格坍缩成流（drawer-1400/log-md-1400 下部 stderr 节）↔ 原文态列完全对齐（log-raw-1400），两帧对照成立
- ④ 嵌套列表/表格：docs/TASK-mdrender-nested-1400.png——两层嵌套（更深缩进并入二级）+ 4 列表格 + 分隔线
- ⑤ 空兜底：docs/TASK-mdrender-empty-1400.png——空答复「（空）」、stdout/stderr 各自「（空）」，切换 link 仍在

**hub 端同款（1400）**：docs/TASK-mdrender-hub-1400.png（最终答复 + 运行日志 md 态，表格三行数据齐全）、TASK-mdrender-hub-log-raw-1400.png（原文态，df -h 对齐）、TASK-mdrender-hub-evil-1400.png（恶意输入字面化 + 同款 CDP 断言全过）。

走查中另抓出并修复两处实现 bug：① `taskDrawerHtml` 抽取漏改 return（见改动清单 node ②）；② hub sticky th 泄漏（见改动清单 style.css 行）。两处均有对应断言/回归锁。

说明：node 侧 1-7 号帧摄于 `.md-body th{position:static}` 修复前；该修复对 node 恒为 no-op（node 全局 th 无 sticky），node 帧视觉不受影响，未重摄。hub 每帧截图前以 `S.drawer=null` 冻结 5s 轮询重绘，避免截图跨绘制代撕裂（几何实测 + 冻结后重截双重验证，页面本身无重叠）。

### 测试结果

`go build ./...` ✅；`go test ./... -count=1` ✅ 15 包全绿 0 FAIL（含新增 TestWebMdRender）。未 git commit。

### 审查收尾批（2026-10-23，独立 zcode 安全审查 + Hana 修复）

- 独立审查（docs/REVIEW-md-render.md）：**XSS/注入/链接向量未发现绕过路径**，escape-first 架构经全路径审计与对抗实测成立；1 必须修 + 2 建议 + 1 可不修。
- 已修：① R1 必须修——引用递归深度封顶 32（单行数千 `>` 的爆栈 DoS）+ mdToHtml 整体 try/catch（任何渲染缺陷降级原文 pre，抽屉永不崩）；② R2——URL 白名单拒绝 `//` 协议相对与 `/\` 反斜杠变体；③ R3——NUL 伪造占位越界原样保留。三项均双端同步（shared block 逐字节一致，node 块整体覆写 hub 块）。
- 测试断言同步：新正则锁 + R1/R3 修复回归锁四条。
- 未修（留档）：R4 轮询重绘冲 details 开合（既有行为，非本次回归）。
- 部署：node 真机（epoch=92）+ 云上 hub（systemd active，控制台资产含渲染器与修复）双端上线；真实任务抽屉实拍确认（空答复兜底/日志渲染/原文切换/usage 文本行内 code 均正常）。
- 最终：`go build ./...` ✅；`go test ./... -count=1` ✅ 15 包全绿。
