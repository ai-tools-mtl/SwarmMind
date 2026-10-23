# 安全审查报告：任务结果 Markdown 渲染（t20261023-969e）

- 审查人：独立安全审查（只读，未改动任何被审文件；本报告为唯一新增产物）
- 日期：2026-10-23
- 审查范围：未提交改动中 md 渲染相关 hunk——`internal/node/web/app.js`、`internal/hub/web/app.js`（渲染器 shared block + 接入）、`internal/node/web/style.css`、`internal/hub/web/style.css`（样式 shared block + th sticky 修复）、新增 `internal/node/localapi/web_md_render_test.go`。工作区 diff 同时捆绑另外两任务的改动（能力配置抽屉 t20261023-a4b1、总览间距 t20261023-f2da），不在本报告范围。
- 威胁模型（按任务书口径）：渲染器处理**网内 agent 任意输出**，控制台持有 admin_token/local_token/PAT。输入上限（internal/protocol/types.go:98-103）：FinalAnswer ≤512KiB、stdout/stderr tail 各 ≤64KiB。

## 结论速览

安全边界（XSS/HTML 注入/链接脚本向量）**未发现绕过路径**，escape-first 架构经全路径审计与对抗实测成立。发现 **1 项必须修**（深引用递归爆栈——agent 输出可让两端任务抽屉渲染崩溃，属鲁棒性/自 DoS，非注入）、2 项建议、其余认可。`go build ./...` ✅；`go test ./... -count=1` 15 包全绿 ✅（实测复跑）。

| 编号 | 级别 | 摘要 | 位置 |
|---|---|---|---|
| R1 | 必须修 | 单行 ≥约 4000 个 `>` 触发 mdBlocks 递归爆栈，抽屉渲染崩溃且每 5s 复发 | node app.js:48-51、hub app.js:46-49 |
| R2 | 建议 | `//host` 与 `/\host` 过链接白名单，构成协议相对外链（非脚本向量，规格精度问题） | node app.js:117、hub app.js:115 |
| R3 | 建议 | 输入内 NUL 字节可伪造 `\x00N\x00` 占位：越界得 `<code>undefined</code>`、命中得内容复制（无注入） | node app.js:120、hub app.js:118 |
| R4 | 可不修 | 5s 轮询全量重绘会冲掉 `<details>` 开合与阅读位置（改动前即如此，非本次回归） | node app.js:342、hub app.js:1295 |

---

## R1（必须修）：引用块递归无深度上限，agent 输出可稳定打崩两端抽屉渲染

**位置**：`internal/node/web/app.js:48-51`、`internal/hub/web/app.js:46-49`（shared block 内，两端逐字节一致，故两端同病）

**机制**：blockquote 分支对连续 `&gt;` 行收齐后递归 `mdBlocks(buf)`，每次递归只剥掉行首**一个** `&gt;`。单行内连续 N 个 `>`（转义后 N 个 `&gt;`）即产生 N 层递归，无深度上限。

**实测证据**（Node 26 直跑 shared block，多次复核）：

- `mdToHtml('>'.repeat(4000))` → `RangeError: Maximum call stack size exceeded`（阈值在约 2000~5600 字符间随执行上下文浮动，4KB 量级稳定触发）
- tail 上限 64KiB、FinalAnswer 上限 512KiB——**触发输入远在协议上限之内**，恶意 agent 一行 `>>>>…` 即可，误触发亦可能（base64/ASCII 艺术类输出）
- 多行每行一个 `>` 不触发（剥一层后即非引用行），风险路径就是单行长串与深层 `>>>` 嵌套

**崩溃落点**（两端均核实）：

- node：`taskDrawerHtml` 在 `refreshDrawer`（async）内抛出 → 未处理 promise rejection。`openDrawer`（app.js:1003-1017）await 首刷在设置 5s timer 之前，异常后抽屉**永久停在「加载中…」**；且 `render()`（app.js:342）每次 5s 轮询都重试 `refreshDrawer` → 每 5s 一次 unhandled rejection。该任务详情永久不可看，控制台其余功能不受影响。
- hub：`drawerHtml` 在 `syncDrawer`（app.js:1293-1299）内抛出。轮询路径被 `pollTick` 的 catch 吞掉但每 5s 复发；首次打开路径 `layerOpenBegin()` 已推进层状态、随后 `layer.innerHTML = …drawerHtml(t)…` 在模板求值时抛出，赋值未完成——**层状态与 DOM 不一致**的中间态。

**定级理由**：任务书铁律是「agent 输出必然安全处理」；在声明的威胁模型（网内 agent 任意输出）下，一个 4KB 输入即可让持有令牌的控制台对该任务的结果/日志查看功能持续失效并刷错误，属渲染器对不可信输入的健壮性契约破损。修复极小，故为必须修（非安全越权，不影响其他任务与页面）。

**修复建议**（任选其一，两端同改同验）：① blockquote 分支加深度参数，超过如 32 层直接按已转义文本平铺（不再递归解析块结构）；② 剥 `&gt;` 时循环剥尽行首全部 `&gt;` 再递归，使深度与单行 `>` 数解耦（注意这会改变嵌套视觉语义，需走查确认）。另建议纵深防御：`taskDrawerHtml`/`drawerHtml` 调用点包 try/catch，渲染异常时降级为原文 `<pre>`，保证**任何**渲染器缺陷都退化为「显示原文」而非「抽屉崩溃」。

## R2（建议）：协议相对 URL 过白名单，超出「相对路径」规格口径

**位置**：`internal/node/web/app.js:117`、`internal/hub/web/app.js:115`（`/^(https?:\/\/|\/)/i`）

实测：`[x](//evil.com)` → `href="//evil.com"`（按页面 scheme 补全为外链）；`[x](/\evil.com)` → `href="/\evil.com"`，按 WHATWG URL 反斜杠规则解析为 `https://evil.com/`（已用 WHATWG URL 实现复核）。两者都绕出了任务书「/ 相对路径」的口径。

**定级**：建议而非必须——影响与白名单明示允许的 `https://evil.com` 完全同类（`target="_blank" rel="noopener noreferrer"` 在位），无脚本向量（`javascript:`/`data:`/实体/反斜杠 scheme 变体实测全部降级纯文本），不越过任何信任边界；属规格精度与纵深防御问题。若修：拒绝前导 `/` 后紧跟 `/` 或 `\` 的 url（一行改动，两端同步）。

## R3（建议）：NUL 字节可伪造 code 占位（美观级，无注入）

**位置**：`internal/node/web/app.js:120`、`internal/hub/web/app.js:118`（`/\x00(\d+)\x00/g` 还原）

占位符用字面 `\x00`+序号+\x00，而 escape 不处理 NUL，进程二进制输出里真实存在的 NUL 可被攻击者拼成假占位：越界序号还原成 `<code>undefined</code>`；命中序号则把已捕获的 code 内容复制到别处。**已验证不构成注入**：还原内容恒为已转义文本或 `undefined`，无引号、无 `<`；即便伪造占位落在 `<a href>` 属性内（url 字符类放行 NUL），也只是属性值内出现 `<code>` 字面串，浏览器按字面解析，无法破出属性。修复：还原时校验 `codes[+k]` 越界则原样保留，或改用每次渲染随机的哨兵串。

## R4（可不修）：轮询全量重绘冲 details 开合（既有行为，非本次回归）

改动前后 `refreshDrawer`（node app.js:342 触发）/`syncDrawer`（hub app.js:1295）都是抽屉整体 innerHTML 重绘，`<details>` 开合态与滚动位置每 5s 被重置——改动前同样存在。本任务要求核验的 **logRaw/failRaw 状态保持成立**（见下文认可 E）；完成记录中「details 开合不被冲」仅指切换动作只重绘 `#log-wrap`/`#drawer-fail`/`#drawer-runlog`，该表述准确无夸大。若要改善阅读体验属独立 UX 任务，不计入本件。

---

## 重点核对项逐条结论（任务书审查指令 1-6）

**1. escape-first 绕过路径——未发现，认可。** 全路径审计：转义后文本中不存在字面 `<`/`>`/`"`/`'`，渲染器输出 HTML 中所有输入衍生插值仅有四处——① 块内容 `buf.join`（已转义直出）；② 围栏语言徽标 `fence[1]`（字符类 `[\w#+.-]`，不含 HTML 特殊字符）；③ `<a href="${url}">`（已转义 url 进双引号属性，`"` 已是 `&quot;`，实体单遍解码不产生二义，实测 `[x](https://a"b)` 输出 `href="https://a&quot;b"` 无法破出属性）；④ `mdInline` 输出（受控模板包裹已转义文本，`<strong>/<em>/<a>/<code>` 均成对生成）。三个专项：**code 占位机制**——code 内容先捕获后置入，粗/斜/链接均在占位替换后的串上运行，占位内容不参与规则、还原时已是转义文本，无逃逸；**属性上下文**——实测 `" onmouseover="alert(1)` 字面化，href 属性实测不可破；**表格单元格拼接**——`mdCells` 拆格后逐格独立 `mdInline`（codes 数组每格独立），拼接的是已转义片段，无跨格重组危险序列的可能。对抗实测：`<script>`/`<img onerror>`/属性逃逸载荷全部字面显示。

**2. 链接白名单——脚本向量全部封死，认可（残留见 R2）。** 实测降级：`javascript:`、`JaVaScRiPt:`、`data:`、`vbscript:`、`javascript&colon;`（`&` 先转义为 `&amp;` 故白名单必不命中，且属性实体单遍解码也无二义）、`\javascript:`（反斜杠起头不匹配白名单）。放行侧：`http(s)://`、`/相对路径` 正常出链且 `rel="noopener noreferrer"` 在位。`/\evil.com`→`https://evil.com/` 的 WHATWG 反斜杠解析已在真实 URL 实现复核，归入 R2。

**3. 双端一致性比对逻辑——真实有效，认可。** `web_md_render_test.go:174-194` `sharedBlock` 以标记串 `strings.Index` 抽块后 `nodeBlock != hubBlock` 逐字节比对，并断言长度；node 侧经 `go:embed` + httptest HTTP 取证（web_md_render_test.go:60），hub 侧直读源文件（:61-68）——hub `//go:embed index.html app.js style.css`（internal/hub/web/web.go:15）保证源文件即二进制发布内容，测试注释已如实声明该口径差异。两端渲染器/样式块当前确实一致（本审查复跑通过）。微小弱点（不构成缺陷）：`strings.Index` 取首个标记命中，若他日在块前文本中出现同名标记串会错位抽取；现状文件中标记串唯一，无风险。node 侧走 embed+HTTP、hub 侧走文件系统，路径口径不对称但都有 embed 背书，接受。

**4. 正则灾难回溯——无，认可（实测背书）。** 结构分析：粗体/斜体 `\*\*`/`\*` 字面锚定尝试起点，`[^*]*` 每个极大无星 run 至多被回溯 O(run) 一次，总量 O(N)；链接正则全部字符类有界且 `[` 锚定。实测 512KiB 输入上限级病态构造：`**`+512K 无闭合 1ms、成组 `**a**` 511KiB 1ms、512K 连续 `*` 0ms、2.7 万个链接 4ms、323KiB 表格 5ms、192KiB 引用行 25ms——全部无卡顿。唯一资源问题是 R1 的递归栈，非性能回溯。

**5. hub 三处接入与旧形态移除——干净，认可。** 三处：最终答复 `mdAnswer(r.final_answer)`（hub app.js:1349）、失败原因 `#drawer-fail`+`failInner`（:1345，定义 :1370）、运行日志 `#drawer-runlog`+`runLogInner`（:1350，定义 :1375）。旧形态清除：两文件 grep `pre-wrap` 零残留，hub 两处旧串（`<p style="white-space:pre-wrap">${esc(r.final_answer)}</p>`、`<pre style="white-space:pre-wrap">${esc(r.stderr_tail)}</pre>`）有测试 ban（web_md_render_test.go:162-169），node 两处旧串（最终答复 pre 直出、details 直挂 pre）同锁（:136-144），且原文切换视图里的 pre 属预期不在 ban 列——口径准确。轮询状态保持：`logRaw`/`failRaw` 存于 `S.drawer`，hub 四处开抽屉均新建 `S.drawer = {id, task}` 对象（app.js:1175/1184/2325/2383）故跨任务不串态，`syncDrawer` 全量重绘（:1295）时 `failInner/runLogInner` 重读 `S.drawer` 状态 → 轮询重绘保持成立；切换只重绘对应内层容器（ACT :2308-2318）。node 侧同理：`S.drawer` 每次 `openDrawer` 新建（app.js:1008），`case 'log-raw'` 有 `S.drawer?.task?.result` 守卫（:1202-1206）。hub 侧 `fail-raw`/`log-raw` 无 result 空判，但按钮只存在于已渲染 `result` 的容器内且 `w` 判空后求值，实际不可达 TypeError——不单列意见。

**6. `.md-body th{position:static}` 两端同步——成立，认可。** 该行在两端样式 shared block 内（node style.css:163 / hub style.css:195），一致性锁含此行精确断言（web_md_render_test.go:112），回归锁有效。压制关系核实：hub 全局吸顶规则 `th{position:sticky;top:84px}`（hub style.css:150，特异性 0,0,1）被 `.md-body th`（0,1,1）稳定压过，`.md-table{overflow:auto}` 滚动容器场景下表头不再钉在 +84px；node 全局无 th sticky（仅 .topbar/.cfg-drawer-foot），该声明在 node 恒为 no-op，符合完成记录声明。

## 规程对照（AGENTS.md 审查守则 / SPEC）

- **MCP 同步**：本改动零新增 REST 端点（纯前端资产 + 测试），SPEC 六.六同步义务不触发。
- **DoD**：十一条中与本件相关的是第 1/2 条——`go build ./...` ✅、`go test ./... -count=1` 15 包全绿 ✅（本审查实跑，与完成记录声明一致）。其余为系统级项，本改动未触及协议/状态机。
- **测试文档**：三层走查记录于 docs/TASK-md-render.md 完成记录，截图帧与声明一致存在；本审查对功能层的 `TestWebMdRender` 做了复跑验证。
- **返工口径**：R1 为该条意见第一次提出，按两轮上限流程交 zcode 修复后复审即可。

## 审查者声明

本审查全程只读：被审四个文件与既有测试文件零改动；唯一写入为本报告。对抗用例在 /tmp 临时目录以 Node 26 直跑渲染器源码完成，全部结论附可复现输入。
