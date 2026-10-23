# 任务书：node 控制台总览页区块间距修复（对齐 hub 视觉节奏）

任务 ID：t20261023-f2da
日期：2026-10-23
提出人：伦哥（附截图：hub 端每块有间隔，node 端挤在一起）
编排：Hana

## 问题诊断（Hana 已核）

hub 总览（internal/hub/web/app.js 596-613 行）每个区块都带显式间隔：`grid cols2 spaced`、`section.card spaced`——设计系统里 `.spaced{margin-top:22px}` 是区块间距的唯一载体（原 `.card+.card` 相邻选择器已废除，两端口径一致）。

node 总览（internal/node/web/app.js 295-303 行附近）丢了这套节奏：
1. `<div class="grid cols3">`（三张统计卡：连接状态/执行账本/最近同步）**无 spaced** → 与前块（page-head 或 setup 引导卡）零间距
2. `primeCard()` 返回的 `<section class="card">`（本机统筹·形态，316/320 行两处分支）**无 spaced** → 紧贴三卡
3. `recentTasksCard()` 返回的卡**核实后同修**（预计同样缺 spaced）
4. 顺带核实：三卡内部垂直节奏——「执行账本」的 `.mini-stat.spaced` 在卡内把内容下推 22px，而「连接状态」「最近同步」的 `.metric` 无此推挤，三卡视觉重心不一致；修法自定（统一内容起始节奏，或卡内禁用 spaced 改用等价间距），以整页截图效果为准。

## 修复要求

1. 只动 internal/node/web/app.js 的总览页结构（如需 style.css 配合说明理由）；不改 hub 端。
2. node 总览所有顶层区块之间恢复与 hub 相同的 22px 节奏（用 .spaced 或等价机制，与设计系统一致，不引入新间距常量）。
3. 三卡等高与内容节奏以截图核查为准（grid stretch 默认应等高；如截图发现不等高须查明原因并修）。
4. 检查其他页面是否有同类漏挂 spaced 的顶层区块（治理页/能力配置/任务页等），同类问题一次修完，但**只修顶层区块间距**，不重构页面。

## 验证要求（三层走查制度，缺一不可）

1. **功能层**：node 本地 API 起测试实例（或既有 e2e 基建），总览页 E2E 断言各区块 DOM 存在且 .spaced 类生效（computed margin-top 或类断言）。
2. **页面级视觉走查**：headless 截图总览页整页（桌面 1400px + 窄视口 1276px，伦哥实际视口宽），人眼核查区块间隔与三卡对齐——截图存 docs/ 下并在完成记录里引用文件名。
3. **弹层深层态走查**：总览页「本机统筹」弹层（prime-pick 选择视图、钦定完成视图、轮换 confirm）逐个打开截图核查间距无回归（primeNew 自动接线提示 notice 也在内）。
4. `go build ./...` 与 `go test ./... -count=1` 全绿。

## 完成记录

在 docs/TASK-overview-spacing.md 末尾追加「完成记录」节：改动点清单 + 截图文件名 + 测试结果。不要 git commit。

---

## 完成记录（2026-10-23，zcode 执行）

### 改动点清单

**internal/node/web/app.js（8 处，只加 spaced/去 spaced，不动结构）**

| # | 位置 | 改动 |
|---|------|------|
| 1 | overviewPage 三统计卡容器 | `<div class="grid cols3">` → `grid cols3 spaced` |
| 2 | overviewPage 执行账本卡 | `.mini-stat spaced` → `.mini-stat`（卡内不再 22px 下推，见 style.css 条目） |
| 3 | primeCard setup 分支 | `card` → `card spaced` |
| 4 | primeCard 常态 head | `card` → `card spaced` |
| 5 | recentTasksCard | `card` → `card spaced` |
| 6 | svcCard（我的服务） | `card svc-card` → `card svc-card spaced`（同类漏挂，要求 4 一次修完） |
| 7 | cfgSection 三态（未加载/不可用/编辑） | 三处 `card` → `card spaced` |
| 8 | myTasksCard（任务页） | `card` → `card spaced` |

口径与 hub 一致：**所有顶层区块一律显式 `.spaced`**；首块（grid cols3/svcCard）的 22px 被 `page-head{margin-bottom:28px}` 折叠吸收，渲染无差（实测 28px）。注：hub 端首块不带 spaced（标记层不同构、渲染层同节奏），两端口径统一时再收敛；node 自身单块页（task-form/wizard/空态引导/服务空态）维持首块干净口径。

**internal/node/web/style.css（1 条，配合理由）**

`.mini-stat{display:flex;gap:18px;margin:9px 0}` + 补 `.mini-stat.spaced{margin-top:22px}`。理由（诊断第 4 条，修法=「卡内禁用 spaced 改用等价间距」）：执行账本的 `.mini-stat.spaced` 在卡内把内容下推 22px，与另两卡 `.metric` 的 9px 起始节奏不一致；改用与 `.metric` 完全相同的 `margin:9px 0`（复用既有常量，不引入新间距值），三卡内容起始与尾注间距统一为 9px。因该条声明位于 `.spaced`（line 73）之后、同特异性后来者胜，须显式补 `.mini-stat.spaced{margin-top:22px}` 保住统筹卡内 mini-stat 的 22px 下推不被盖掉。hub 端唯一 mini-stat 用点不带 spaced、无卡内节奏需求，未同步（口头说明：两文件该条暂有 1 条声明的差异，属 node 侧修复）。

### 三层走查结果

**1. 功能层（E2E 断言）**：新增 `internal/node/localapi/web_spacing_test.go::TestWebOverviewSpacing`——对 embed 服务出的 /app.js 做类断言（上述 8 处标记 + 反向断言不再存在无 spaced 的 `grid cols3`），对 /style.css 断言 `.spaced{margin-top:22px}`、`.mini-stat{…margin:9px 0}`、`.mini-stat.spaced{margin-top:22px}` 三条不被改坏。PASS。
真机补充（headless Chromium 实测 computed 值，standalone 测试实例 127.0.0.1:18445）：总览 page-head→三卡 28px、三卡→统筹卡 22px、统筹卡→最近任务 22px；三卡 top/bottom/height 完全相等（190.4px，grid stretch 等高成立），卡内 metric 与 mini-stat 的 computed margin 均为 9px/9px；1276px 视口同样 28/22/22 且仍三列；任务页 28/22、服务页 28/22/22。

**2. 页面级视觉走查（整页截图，存 docs/）**：

- `TASK-spacing-overview-1400.png`（桌面 1400px，常态：三卡+统筹+最近任务表 2 行）
- `TASK-spacing-overview-1276.png`（伦哥实际视口宽 1276px）
- 附加证据：`TASK-spacing-services-1400.png`、`TASK-spacing-tasks-1400.png`（同类修复页）

视觉核查（逐张）结论：三卡等高、内容起始对齐、区块间隔均匀无挤压、无重叠溢出，全部合格。

**3. 弹层深层态走查（本机统筹卡，存 docs/）**：

- `TASK-spacing-prime-pick-1400.png`（钦定选择视图·未钦定引导态）
- `TASK-spacing-prime-repick-1400.png`（「换一个统筹」选择视图·替换警示态）
- `TASK-spacing-prime-done-1400.png`（钦定完成视图：绿色双 notice + 一次性凭证 + MCP JSON 片段）
- `TASK-spacing-prime-rotated-1400.png`（轮换后 primeNew 自动接线提示 notice + 新凭证）

深层态全部合格（notice 间距均匀、长凭证串与 JSON 无溢出）。说明两点：①轮换/钦定的 confirm 是原生 OS 级 `window.confirm`，无法出现在页面截图里——走查时真实触发过该对话框并接受（钦定一步即真对话框验证），截图覆盖其后的全部页面深层态；②fullPage 截图对 `position:sticky` 吸顶栏有「停留在滚动位置」的截屏伪影（首版 repick 图顶栏被画在三卡上，视觉核查抓出），已按「先滚回顶部再截」重截全部深层态图消除；几何实测（190.4px×3 等高、28/22/22）证明页面本身无任何重叠。
接线实证：钦定/轮换应答 wired=true，影子 home 的 `.zcode/cli/setting.json` 实际写入了 mcpServers.swarmmind（新 token 随轮换刷新，旧配置备份 .bak-prime）。

### 测试结果

- `go build ./...`：通过。
- `go test ./... -count=1`：15 包全绿。备注：首轮出现过 1 次 `internal/node/service::TestCancelWriteBackReconcilesFence` 失败（取消回执重发的时序竞态），与本任务无关（本任务只动 web 前端资产与 localapi 测试，不触及 service 包）；该用例单独复跑 ×3 通过，全量复跑全绿，判定为既有负载敏感 flake，如实记录不隐瞒。
- 测试实例已停、端口释放、/tmp 临时目录已清理；未 git commit。
