# 任务书：能力配置「编辑」按钮风格适配（表格行内操作统一）

任务 ID：t20261023-9546
日期：2026-10-23
提出人：伦哥（截图圈注「▶ 编辑」与整体风格不统一）
编排：Hana

## 诊断（Hana 已核）

「编辑」实现于 `cfgRow()`（app.js 514 行）：`<details class="cfg-edit"><summary class="link">编辑</summary>`。两个问题：
1. summary 未清原生 marker（▶ 三角）——summary 全局样式只有 cursor/weight/color（style.css:98），list marker 保留；配 `.link` 蓝字后成了「▶ 编辑」蓝链，是全站唯一的 summary 当行内链接用点。
2. 同为表格行内操作，「最近任务」的「详情」是 `button(...,'link')`（纯蓝字无 marker），两处形态不一。

设计系统现成档位：`.table-action button`（style.css:110，12.5px / height:auto / padding 4px 10px，默认浅灰 pill）——hub/node 同款表格行内操作标准形态。

## 修复要求

1. `.cfg-edit>summary` 改表格行内小 pill 按钮形态：清 marker（`list-style:none` + `::-webkit-details-marker{display:none}` 双清，Chromium/Safari 通吃）+ 复用 .table-action button 参数（12.5px、padding 4px 10px、浅灰底、r-pill、font-weight 500），颜色用 --ink-2（行内操作不抢蓝，蓝色留给跳转/primary）。
2. `.cfg-edit[open]>summary` 展开态：底色加深（如 rgba(0,0,0,.09)）表示「编辑中」，文案可保持「编辑」。
3. `.cfg-edit` 覆盖全局 details 行内样式（全局 details 有 border-top+padding-top 15px+margin 20px 0，行内会撑破表格）：`border-top:0;padding-top:0;margin:0`；`.cfg-edit .cfg-form` 展开表单的上间距自管（约 12px，与表单内部 label 间距协调，以截图为准）。
4. 「详情」（app.js 635）`button('详情', ..., 'link')` → 去 link 类，落 `.table-action button` 默认小 pill，与「编辑」统一。
5. 编辑表单内部已有元素（cfgField/cfg-check/off-btn 删除按钮）不动；全局 details/summary（「编辑字段说明」「配置详情」「编辑表单外的其他展开块」）不动——那些的 marker 是「区块展开」语义，保留。

## 验证（三层走查）

1. **功能**：资产断言（/app.js /style.css 文本级，参照 web_spacing_test.go 姿势）锁住 summary 无 link 类、详情按钮无 link 类；若有 cfg 相关既有 e2e 同步更新。
2. **页面视觉**：headless 截图「我的服务」整页（1400 + 1276），人眼核查编辑 pill 与整页按钮体系协调。
3. **深层态**：展开任一行编辑表单截图（1400）——summary 展开态、表单间距、删除按钮；最近任务行「详情」pill 截图。存 docs/ 并在完成记录引用。
4. `go build ./...` 与 `go test ./... -count=1` 全绿。

## 完成记录

docs/TASK-cfg-edit-style.md 末尾追加完成记录（改动清单 + 截图文件名 + 测试结果）。不要 git commit。

---

## 完成记录（2026-10-23，zcode 执行）

### 改动点清单

**internal/node/web/app.js（2 处，只动标记层）**

| # | 位置 | 改动 |
|---|------|------|
| 1 | cfgRow() 编辑按钮 | `<summary class="link">编辑</summary>` → `<summary>编辑</summary>`（去 link 类，pill 形态由 CSS 接管） |
| 2 | myTasksCard() 详情按钮（635 行） | `button('详情', ..., 'link')` → `button('详情', ...)`（去 link 类，落 .table-action button 默认小 pill） |

**internal/node/web/style.css（1 块，紧跟 .table-action 之后新增）**

- `.cfg-edit{border-top:0;padding-top:0;margin:0}`——覆盖全局 details 区块样式（border-top+padding-top 15px+margin 20px 0 在表格行内会撑破行）。
- `.cfg-edit>summary`：marker 双清（`list-style:none` + `::-webkit-details-marker{display:none}`，Chromium/Safari 通吃，Firefox 由 list-style:none 覆盖）+ .table-action button 同档参数（`display:inline-flex`、height:auto、padding 4px 10px、r-pill、浅灰底 rgba(0,0,0,.05)、12.5px、font-weight 500、--ink-2、hover 加深 .08、transition 复用 --dur-hover）。行内操作不抢蓝，蓝色留给跳转/primary。
- `.cfg-edit[open]>summary{background:rgba(0,0,0,.09);margin-bottom:0}`——展开态底色加深示「编辑中」，文案保持「编辑」；margin-bottom:0 清掉全局 `details[open] summary{margin-bottom:18px}` 的下推（特异度 (0,2,1)>(0,1,2) 成立）。
- `.cfg-edit .cfg-form{margin-top:12px}`——展开表单上间距自管（summary pill 与表单首 label 间约 12px，截图核验协调）。
- 未动：编辑表单内部已有元素（cfgField/cfg-check/off-btn）、全局 details/summary（「编辑字段说明」「配置详情」等区块展开语义及 marker 保留）。

### 三层走查结果

**1. 功能层（资产断言）**：新增 `internal/node/localapi/web_cfg_edit_style_test.go::TestWebCfgEditStyle`（参照 web_spacing_test.go 姿势，对 embed 服务出的 /app.js、/style.css 文本级断言）——正向锁 summary 无 link 类标记、详情按钮无 link 类、CSS 六段关键样式（行内覆盖/marker 双清/pill 参数/hover/展开态/cfg-form 间距）；反向锁 `<summary class="link">编辑</summary>` 与 `button('详情', ..., 'link')` 不再出现；另锁全局 `details{border-top…}/details[open] summary{margin-bottom:18px}` 原样保留（行内覆盖只走 .cfg-edit 命名空间）。无 cfg 相关既有 e2e 涉及这两处标记（「详情」按钮全站仅 635 行一处用点，已核实）。PASS。

**2. 页面级视觉走查（整页截图，存 docs/）**：

- `TASK-cfgedit-services-1400.png`（我的服务整页 1400px，常态）——「编辑」为浅灰圆角小 pill、无 ▶ marker、非蓝链，与「暴露/停止暴露」「＋ 新增 agent」「保存全部」等按钮体系协调；「编辑字段说明」区块 summary 语义保留。
- `TASK-cfgedit-services-1276.png`（伦哥实际视口宽 1276px）——pill 形态不变，表格无横向溢出/重叠/换行错乱。

**3. 深层态走查（存 docs/）**：

- `TASK-cfgedit-form-open-1400.png`（展开 echo-demo 行编辑表单）——展开态「编辑」pill 底色较未展开行加深（编辑中）、summary 与表单间距约 12px、表单 label/输入框整齐、「删除此 agent」按钮在位、表格行自然扩展无异常空白/重叠，未展开行保持浅灰 pill。
- `TASK-cfgedit-task-detail-1400.png`（任务页「我的任务」行「详情」pill，1400px）——「详情」为浅灰圆角小 pill 非蓝链、与「再派一单/＋派单」协调、行内（时刻/目标/任务号/状态徽章/详情）排版整齐（走查用真实派单任务：standalone 实例钦定 echo-demo 统筹后派单，状态已完成）。

### 测试结果

- `go build ./...`：通过。
- `go test ./... -count=1`：15 包全绿，一次通过（无 flake）。
- 测试实例已停（18445 端口已释放）、/tmp 临时目录已清理；未 git commit。

---

## 补丁（2026-10-23，zcode 执行）：展开行 td 贴行顶

展开编辑表单撑高行时 td 默认 middle 把 agent 名悬在行中央，视觉散。修法：`cfgSection()` 能力配置表挂 `cfg-table` 命名空间（app.js 495 行 `<table>` → `<table class="cfg-table">`），style.css 同块新增 `.cfg-table td{vertical-align:top}`——作用域只罩该表，实测同页其余表格（svcCard 只读配置详情表）computed 仍 middle。资产断言补锁两条（`<table class="cfg-table">` / `.cfg-table td{vertical-align:top}`）。`TASK-cfgedit-form-open-1400.png` 已重截替换：展开行左侧 echo-demo 贴行顶、表单排版不破、未展开行 pill 不变（computed 核验 cfg 表 td=top，standalone 实例 18445，已停）。`go build ./...` + `go test ./internal/node/localapi -count=1` 绿。未 git commit。

---

## 补丁 2（2026-10-23，zcode 执行）：cfg-check 复选框行错位实测修复

截图走查发现展开编辑表单里 allow_args/stdin_to_arg/exposed 三个复选框疑似与标签文字错位，起 standalone 实例（127.0.0.1:18445，echo-demo）headless 实测诊断。

**诊断结论：真错位（但截图读法有偏差——不是「checkbox 悬文字右侧偏上」，而是 checkbox 独占上一行、文字被挤到下一行）。** 修复前实测（viewport 1400，页面内 getBoundingClientRect + getComputedStyle，三项数据一致）：

- label computed `display:block`；checkbox input computed `display:block`、`width:575.2px`（= 该列满宽）、`margin-top:7px`；
- input rect 宽 575.2px 高 13px：checkbox 被撑成满宽块级独占一行，文字另起一行；checkbox 中心比文字行中心高 10px；
- 根因：`.cfg-check` 在 style.css 零定义，全局 `label{display:block}`（style.css:93）与 `input,select,textarea{display:block;width:100%;margin-top:7px}`（style.css:94）对 checkbox 双杀（与任务书猜想的「cfg-check 无样式定义 + label 全局块级」吻合）。

**修复（style.css cfg 块内新增两条，只修布局不动字样——文字排版沿用全局 label 档 fs-small/600/ink-2）：**

- `.cfg-check{display:flex;align-items:center;gap:8px}`——复选框贴行首、与文字同行垂直居中（长文字换行时对整块文字居中）；
- `.cfg-check input{width:auto;flex:none;margin:0}`——收回全局满宽块与 7px 上边距（特异度 (0,1,1)>(0,0,1)）。

**修复后复测（同实例同视口，三项全部）：** label `display:flex`、input `width:13px`、`margin-top:0`；checkbox 左缘 = label 左缘（贴行首）；纯文字矩形（Range 圈文本节点）左缘距 checkbox 右缘恰 8px（=gap）；文字与 checkbox 垂直区间重叠（同行）；中心 Y 差 0.1px（居中）。

**交付：**

- `TASK-cfgedit-form-open-1400.png` 已重截替换（1400 宽整页，新 1400×1566；较旧图 1883 矮是三个复选框行各回收一行所致）；视觉复检三项均「行首方框 + 同行文字 + 垂直居中」，表单其余（cfgField/删除按钮/pill 展开态）不受影响。
- `web_cfg_edit_style_test.go` 资产断言补三条：CSS 两条新规则 + app.js 标记契约 `<label class="cfg-check"><input type="checkbox"`（flex 同行修复依赖 input 在文字前的结构）。
- `go build ./...` 通过；`go test ./internal/node/localapi -count=1` 绿。
- 测试实例已停（18445 释放）、/tmp/swarmmind-cfgcheck 已清理；未 git commit。

### 收尾批（2026-10-23，Hana 执行；审查建议采纳）

- 独立 zcode 会话审查通过（无必须修，3 建议）：① 展开态 hover 无反馈 → 补 `.cfg-edit[open]>summary:hover{background:rgba(0,0,0,.11)}`；② td.table-action 的 nowrap 继承进展开表单致长标签溢出 → `.cfg-form` 补 `white-space:normal`；③ 提交拆分建议（两任务两 commit）留待伦哥点头时执行。
- 资产断言同步补两条锁（hover 反馈 + white-space:normal）。
- 真机部署实测（epoch=86）：1276 展开态实拍确认——编辑 pill 融入按钮体系、复选框贴行首垂直居中、stdin_to_arg 长标签正常折行不溢出、左侧 agent 信息贴行顶。localapi 包测试绿。
