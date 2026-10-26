# TASK：任务筛选 + 双端导航统一（20261026）· v2 定稿

> 状态：定稿（草案经 codex 评审，8 项必须修已全部吸收；待派 zcode）
> 伦哥原话：①任务页加筛选——客户端筛「任务来源」（名词可设计）、中心端筛「下达机器」（名词可设计）；②双端左侧导航统一，名称可重设计，排版/位置可调换。
> 基线：53752f7（dev/p5-work-resume）。前置：等 prompt-preview（t20261026-de69）收尾，同文件冲突避让。

## 〇、方案结论（codex 评审后不推翻项）

- A1 node「我的任务」来源筛选：**前端过滤已载入口径**，不改账本 API（控制改动面，codex 认可）。
- A2 hub 任务页「目标机器」筛选：**放主筛选区 fields 行**（与搜索任务 ID 并排），不放高级折叠区；用词与表格列同词「目标机器」，不引入「下达机器」第二套词（避免被误解为提交来源，codex 认可）。
- B 导航统一：词表+顺序+视觉三件事，方向不变，文案波及面按第五节完整清单执行。

## 一、现状（实测确认 + codex 复核）

### 任务数据与页面
- hub 任务页（`internal/hub/web/app.js` tasksPage ≈1511 起）：已有状态 chips（fchip）/ 搜索任务 ID（trace）/ 高级筛选折叠区（by/error/from-to）。表格列：任务/目标机器/状态/提交方/提交时间/操作。数据字段 `t.target_node`。
- **hub S.f 字面量重建共 5 处状态构造点**（codex 确认）：
  - `app.js:887` 下钻入口（STATES[arg]）
  - `app.js:899` 下钻入口（另一分支）
  - `app.js:1072` 登录成功重置
  - `app.js:2897` clear-filters 动作
  - `app.js:1511` 起 tasksPage 内 `S.f` 读取与 `filteredTasks()`（≈1527）、`filtered` 判定（≈1531）、select 回填（≈1537）为逻辑覆盖点
  - 高级折叠开关 `advOpen` 不含主筛选区 node 字段，无需动。
- node 任务页（`internal/node/web/app.js` myTasksCard ≈1399）：表格已有「来源」列（originBadge：本机派单 | hub 下发；字段 origin：`hub` / `local`），**无筛选**。列表为账本全量分页载入（「加载更早的任务」，S.tasksTotal），留痕行（无 status）已存在。
- node `S.f` 已被派单表单状态机占用（stage/target/stdin/idem），任务筛选新开 `S.tf`。**codex 已核 S 全字段清单，`tf` 无冲突**。

### 导航现状
- hub（`shell()`，menus ≈940，8 项）：`总览/机器/任务监管/任务派发/工作单/策略容量/设置/群聊`；navnum `0${i+1}`（01-08）；nav 带 `aria-label="旅程导航"` + aria-current；workspace 行序 small(协作网 · 管理员) 在上、strong(品牌) 在下；needs_review 计数徽标（≈945）。
- node（`layout()` ≈712，PAGES ≈670，4 项）：`总览/我的服务/派任务·我的任务/连接`；navnum 带圈数字 ①②③④（与 hub 不同）；nav 无 aria-label、无 aria-current；workspace 行序 strong(nodeID) 在上、small(本机控制台) 在下（与 hub 相反）。
- aside-bottom 双端同款已统一；E2E `tests/e2e-console.mjs` 88/104/192 三处依赖导航词。

## 二、方案 A1：node「我的任务」来源筛选

- 词表：沿用来源列现词「全部 / 本机派单 / hub 下发」——不造新词（徽标与筛选同源）。
- 形态：卡头部筛选行放三枚 chip（视觉对齐 hub `.filters` 形态），带计数（如「本机派单 12」）。
- 状态：`S.tf = { origin: '' }`；语义：''=全部，'local'=本机派单，'hub'=hub 下发。
- **S.tf 生命周期约定**（codex 必须修项）：轮询刷新、加载更早翻页、普通页面往返**保留**筛选；登出 / 401 / 换令牌**重置**（与 S.drawer 同模式的复位点挂钩，实现时核对 route/auth 相关复位函数）。
- 口径：前端过滤已载入条目；footer-note 追加「筛选作用于已载入的 N / 共 M 条」。
- 空结果：显示「没有符合筛选的任务」+「清除筛选」按钮（词与 hub 空态同源）；**零命中不得影响「加载更早」翻页入口**（翻页仍可用，codex 必须修项）。
- 留痕行（无 status）参与来源计数与筛选（origin 恒有值，留痕行 origin=local）。
- **词典同源**（codex 必须修项）：originBadge 现为重复字面量；本次将来源词表提为单一出处（shared block 或同文件常量 `ORIGIN_WORDS = { hub: 'hub 下发', local: '本机派单' }`），徽标、筛选 chip、抽屉来源展示全部消费同一词典，不得再复制字面量。

## 三、方案 A2：hub 任务页「目标机器」筛选

- 词表：与表格列同词「目标机器」。选项「全部机器」+ 动态去重 target_node（同 submitters 模式）。
- 状态：`S.f.node`（''=全部机器）。
- **实现方式：统一筛选默认值构造函数**（codex 建议，防再漏点）：
  - 新增 `blankFilters()` 返回 `{ status:'', trace:'', by:'', error:'', from:'', to:'', node:'' }`，5 处字面量重建点（887/899/1072/2897 + 初始化处）全部改调此函数；`clear-filters`（2897）与登出重置同步覆盖。
  - 注意 887 行下钻入口是 `S.f = { status: arg, ... }` 形态：改为 `{ ...blankFilters(), status: arg }`。
- `filteredTasks()` 加 node 条件（`t.target_node === S.f.node`）；「清除筛选」按钮与 `filtered` 判定（是否有激活筛选）同步覆盖 node 字段。
- select 选中值回填（≈1537）含 node；`advOpen` 不含 node（主筛选区字段不进高级折叠区）。
- **select 选项生命周期**（codex 必须修项）：
  - 选项来自**未过滤**的 `S.tasks` 按 target_node 去重排序，value 与显示文本均 `esc()` 转义。
  - 任务清理导致当前选中机器从数据消失时：**保留选中项并显示零结果空态**（不静默回退「全部机器」），空态文案可提示「该机器当前不在快照中」；控件值与 S.f.node 恒一致，禁止「显示全部机器但实际还在过滤」的假状态。
- 导出（≈3375 已复用 filteredTasks()）与批量操作：验证导出结果同样受 node 筛选约束，不需要重复实现过滤，但验收必测。

## 四、方案 B：导航统一（词表 + 顺序 + 视觉）

### 词表定案（名词式、双端同概念同词）
- hub：`总览(01) / 机器(02) / 任务(03) / 派发(04) / 工作单(05) / 群聊(06) / 策略容量(07) / 设置(08)`（「任务监管」→「任务」，监管语义留页面 h1/副标题；「任务派发」→「派发」避免与「任务」重字；群聊提前，策略容量+设置沉底）
- node：`总览(01) / 服务(02) / 任务(03) / 连接(04)`（「我的服务」→「服务」，与 hub「机器」同为资源层；「派任务·我的任务」→「任务」，页面 h1 同步改「任务」，副标题保留「派单与跟踪」说明）

### 视觉统一
- node navnum ①②③④ → `01 02 03 04`（与 hub 同格式）。
- node workspace 行序改为 small(本机控制台) 在上、strong(nodeID) 在下（与 hub 同构）。
- node nav 补 `aria-label="旅程导航"`；**aria-current 语义补齐**（hub 有 `aria-current="page"`，node 也要加，无障碍属性双端同构）。
- 不动的：导航交互机制（node data-go vs hub data-act=go）不改；SETUP_PAGES 裁剪态不动（但 setup 态页面词若含旧词一并按清单核对）；hub needs_review 徽标保留。

### 文案迁移完整清单（codex 必须修项：含 Go 端与 docs，机械可执行）

**JS 用户可见文案：**
| 位置 | 现词 | 改为 |
|---|---|---|
| hub app.js:940 menus | 任务监管 / 任务派发 | 任务 / 派发 |
| hub app.js:1121 总览 notice | 「任务监管」页 | 「任务」页 |
| hub app.js:1532 任务页 h1/副标题 | 任务监管 | 任务（副标题保留监管语义） |
| hub app.js:1909 派发页 h1 | 任务派发 | 派发 |
| hub app.js:1928 批次步骤文案 | 「任务监管」页 | 「任务」页 |
| hub app.js:2118 批次按钮 | 下一步：查看任务监管 → | 下一步：查看任务 → |
| hub app.js:2652 footer | 「任务派发」页 | 「派发」页 |
| node app.js:670 PAGES | 我的服务 / 派任务·我的任务 | 服务 / 任务 |
| node app.js:965 services h1 | 我的服务 | 服务 |
| node app.js:1351 tasks h1 | 派任务 · 我的任务 | 任务 |
| node app.js:787 能力引导步骤 | 「我的服务 → 能力配置」 | 「服务 → 能力配置」 |
| node app.js:836 空态文案+按钮 | 「我的服务」/ 去看我的服务 | 「服务」/ 去看服务（按钮词与指路同改） |
| node app.js:859 最近任务 footer | 「派任务 · 我的任务」 | 「任务」页 |
| node app.js:860 空态文案 | 去「派任务」 | 去「任务」 |
| node app.js:1385 任务页空态 | 「我的服务」/ 去看我的服务 | 「服务」/ 去看服务 |

**Go 端返回文案（JS grep 查不到，必须单独改）：**
| 位置 | 现词 | 改为 |
|---|---|---|
| internal/node/localapi/prime.go:215 | 「我的服务 → 能力配置」 | 「服务 → 能力配置」 |

**文档（同 PR 内同步）：**
| 位置 | 处理 |
|---|---|
| README.md:130 | 两个 hub 页面名同步 |
| docs/INTERACTION.md:20、32、33、53、54、65、66 | 页面名称与路径同步；页面表按新顺序排列 |
| SPEC.md:485、650 | 两处「我的服务 → 通知」同步 |
| SPEC.md ≈576 | 双端差异边界登记（见第六节） |
| docs/TESTPLAN.md:55、743、744、748、749、930 | 页面清单/用例名/服务 h1 断言同步；55 行顺带纠正「接入」为「连接」 |
| docs/TESTPLAN-FE.md:97 | 导航操作词同步 |
| docs/TESTPLAN-tasks-audit.md:82 | node 页面指路同步 |

**不改的（防过度替换）：**
- node 表单标题「派任务」、列表卡片「我的任务」、留痕抽屉「回到我的任务列表」——描述动作或区域，保留（node app.js:1377、1403、1695）。
- 「正式任务派发」等业务概念不属于旧导航名，保留。
- 历史证据保留原貌：docs/TESTREPORT-logo.md、docs/archive/FE-AUDIT.md、归档任务书不重写历史；旧原型 dashboard-journey.html 保留（标注历史原型属性可选）；截图脚本注释里的旧词不影响执行。

## 五、双端形态对照表（CLAUDE.md 双端一致三层硬规要求，随本任务书交付）

| 维度 | hub 中心端 | node 客户端 | 一致性判定 |
|---|---|---|---|
| 导航壳 | aside + nav + aside-bottom | 同 | 同构 |
| 导航词表 | 总览/机器/任务/派发/工作单/群聊/策略容量/设置 | 总览/服务/任务/连接 | 概念同词（资源层：机器↔服务） |
| navnum 格式 | 01-08 | 01-04 | 同格式 |
| 激活态/aria | active + aria-current="page" | 同（本次补齐） | 同构 |
| workspace 行序 | small 在上 strong 在下 | 同（本次调换） | 同构 |
| 筛选位置 | 任务页 fields 行（主筛选区） | 任务卡头部筛选行 | **合理差异**：hub 高频维度入 fields；node 卡片布局无 fields 行，放卡头（登记为合法差异） |
| 筛选形态 | select 下拉（机器） | chip 组（来源） | **合理差异**：维度基数不同（机器多值 vs 来源二值+全部），形态随基数走 |
| 选中态/计数 | select 选中值回填 | chip active + 计数 | 同构（选中态显性化） |
| 空态 | 「没有符合筛选的任务」+ 清除筛选 | 同词同按钮 | 文案同源 |
| 清除动作 | clear-filters（全字段） | 清除来源筛选（S.tf） | 同构 |
| 加载入口 | 全量快照无翻页 | 「加载更早」零命中下仍可用 | 结构差异保留，筛选不阻断翻页 |
| setup 裁剪态 | 无 | SETUP_PAGES 不动 | 差异保留 |
| 词典出处 | ORIGIN_WORDS 单一出处消费 | 同 | 逐字节同文（shared block） |

## 六、SPEC 差异边界登记（codex 必须修项）

在 SPEC.md 六.五 双端一致契约（≈576 附近）补记：
- 本次新增筛选的双端差异边界：hub=select+fields 行，node=chip+卡头，属「基数决定形态」的合法差异，不豁免其他维度的同构要求。
- 来源词典（ORIGIN_WORDS）进 shared block 白名单（渲染层逐字节同文）。
- 防止「同构」要求被扩大解释为整页重做——本任务只动筛选行与导航壳，不重构任务页其余部分。

## 七、验收清单（机械可执行，zcode 交付物必含）

### 构建与单测
- go build + go vet + hub/node 全部测试绿（shared block 一致性锁双绿：prompt 相关既有锁复跑）。
- embed 后重建二进制再跑 E2E（改 web 资产必须重建，历史铁坑）。

### E2E / 行为断言（codex 必须修项：导航定位用稳定选择器）
- **导航选择器**：改词后 `:has-text("任务")` 会宽泛命中（工作单含任务徽标等）——aside 内导航定位改用**结构化选择器**（如 `aside nav button:nth-child(3)` 或加 data-act/data-go 值定位），并单独断言双端可见词表完整（导航 8 项/4 项名称逐个断言）。E2E 同步点：tests/e2e-console.mjs 87/88/91/104/192。
- **node 筛选行为**：混合来源数据下三枚 chip 各自过滤正确；留痕行参与 local 计数；零命中显示空态+清除筛选且「加载更早」仍可点；翻页载入后筛选结果与计数更新；轮询刷新后筛选保持；派单表单草稿（S.f）与筛选（S.tf）互不干扰；登出后 S.tf 重置。
- **hub 筛选行为**：node 与既有条件（status/trace/by/error/from-to）交集过滤正确；5 处状态构造点逐一触发验证（下钻 887/899、登录 1072、清除 2897、初始）；无匹配空态；导出 CSV 受筛选约束；选中机器从快照消失后保留选中+零结果提示；clear-filters 后 select 回「全部机器」。
- **导航行为**：双端名称/顺序/编号快照断言；aria-current 存在；旧深链（#tasks 等 hash 路由）不回归；setup 裁剪态正常；hub needs_review 徽标仍在「任务」项。
- **三层走查**（伦哥 2026-10-23 钦定制度）：①E2E 行为断言；②整页截图人眼核查（双端任务页+导航，含筛选激活态）；③深层交互态走查（chip 点击后、select 展开选中后、空态清除后、翻页+筛选叠加态，逐帧截图）。
- 样式实测注意（codex 建议）：node `.filters` 基础样式已有但无 hub 的 `.filters .badge` 与零数量样式；workspace 品牌字号有显式规则差异——「同款」以实际 DOM 计算样式+截图验收，不靠类名复用想当然。
- 双端对比截图留证（形态对照表逐行）。

## 八、已知陷阱（历史教训清单）

1. shared block 逐字节锁双绿；ORIGIN_WORDS 进 shared block 不得有端特判分支。
2. embed 改后重建二进制再实测。
3. 工人不执行 git commit（提交由 Hana 编排把关）。
4. node 端轮询只刷新首页、历史保序并入（S.tasksGen 代次机制）——筛选必须作用在渲染层而非数据层，否则翻页合并会破坏筛选结果。
5. hub 887 行下钻入口保留 status 直达语义（总览状态 chips → 任务页对应筛选）。
6. 不碰并行任务的改动区（promptZoneHtml 相关），rebase/提交时按文件区隔离。

## 九、终审必修补丁（codex 终审 2026-10-26 17:40，当前不予放行，修完复验）

> 语境注记（17:56）：R1 改动已随 147edf7 入库（连同 prompt-preview 与登录竞争修复）。必修 1 经实扫**仍在**：147edf7 的同形态指纹跳过（loginMode+wantPw）修的是迟到回调冲表单，未清业务分支的 dataset.loginPage，退出→登录分支指纹误匹配跳过重建的 bug 依旧；修法不变。R2 施工基线即 147edf7，prompt-preview 区已提交不再是未提交工作区改动，但 promptZoneHtml 函数体仍属并行任务领地不可动。

### 必修 1：登录页缓存跨登录保留（hub app.js ≈925/933，prompt-preview 附带改动撞进本次验收边界）
- 问题：`app.dataset.loginPage` 指纹缓存在登录成功进业务页时未清除；退出/401 后再进登录分支时指纹相同跳过写入 → DOM 停留在业务页，登录表单出不来。codex 已用真实 render()+DOM 桩复现。
- 修法：进入已登录业务渲染分支时清除 `app.dataset.loginPage`（或跳过条件加「当前 DOM 确实是登录视图」）；补浏览器回归：登录→退出→再登录、登录→401→登录表单出现。

### 必修 2：node 快照变空后来源筛选不可见无法清除（node app.js ≈1426/1433）
- 问题：chip 行/空态/清除按钮全在 `all.length` 非零分支；选 hub 后快照变空 → 只显示首次派单空态，筛选控件消失但 S.tf 仍锁 hub → 后续 local 任务被隐藏。codex 补测复现：空快照+origin=hub 时 hasChips=false、hasClear=false。
- 修法：来源筛选行移出 `all.length` 条件；激活筛选+空列表 →「没有符合筛选的任务」+清除筛选；未激活+真无任务 → 首次派单空态。禁止用自动重置筛选规避（任务书要求轮询保留筛选）。补行为锁：选 hub→空快照→新增 local→清除筛选全程。

### 必修 3：筛选作用域数字口径错误（node app.js ≈1432 + 测试锁 ≈347）
- 问题：footer 实际输出 `rows.length / all.length`（命中数/已载入数），任务书要求「已载入 N / 共 M 条」（all.length/S.tasksTotal）；测试把错误口径锁死了。
- 修法：命中数与作用域分开表达（如「命中 12 条；筛选作用于已载入的 200 / 共 500 条」）；同步改测试，加 rows/all/total 三者互不相等的分页场景 + 零命中时仍显示已载入与总量。

### 必修 4：行为验收与证据缺口（E2E + TESTPLAN）
- E20 名称「含导出同源过滤」但未实测导出文件：实测下载并解析导出 JSON 验证同源过滤，或改名删掉括号注释。
- E20c 未验证 clear-filters 清除机器选择：零命中场景叠加机器筛选，断言清除后 select 也回「全部机器」。
- 无机器选项跳过分支计 PASS：改为 SKIP 单列（汇总区分 PASS/SKIP/FAIL）。
- E2E 缺 node 页面流程：补轮询保持筛选、翻页叠加筛选、登出后 S.tf 重置的浏览器级断言（node 令牌门登录）。
- TESTPLAN 三份只改了名，未补本次新增机械执行步骤：按 E-NAV/E20 系列补测试计划条目。
- 截图证据补充：node 零命中态、翻页+筛选叠加态、机器从快照消失态（验证环节的截图脚本草稿填法有 bug，另修重拍）。

## 十、评审记录

- codex 评审（2026-10-26，read-only 全仓复核）：方案 A1/A2/B 方向全部认可不推翻；8 项必须修已吸收进 v2：①S.f 五处构造点+统一构造函数；②select 选项生命周期；③S.tf 生命周期；④文案迁移完整清单（含 prime.go:215 Go 端遗漏）；⑤双端形态对照表+词典同源；⑥SPEC 差异登记；⑦E2E 稳定选择器+行为验收扩展；⑧验证交付要求（SPEC DoD 十一条）。
- Hana 独立实扫修正：S.f 字面量 4 处（草案误记 3 处）；hub app.js:1121 可见文案引用旧页名（草案 grep 面遗漏，codex 复核确认并扩展至完整清单）。
