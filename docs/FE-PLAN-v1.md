# SwarmMind 前端优化方案 v1（三方汇总稿）

> 产生方式：zcode 设计草案 + codex 独立评审 + 编排者裁决（2026-10-23 凌晨）。
> 输入：apple-design-skill 全套 + zfjwork 动效四维度方法论 + FE-AUDIT.md 18 条 + 现状代码。
> codex 评审纠正了审计 2 处不准（h1 已有负字距、设置页已有分组），并新增 10 条可靠性问题——本方案 v1 与 zcode 草案的最大差异是 **P0 从 token 化改为可靠性**。

## 0. 三个决定性判断（全部决策由此推导）

1. **先可信，再好看**：运维控制台的生命线是「显示的即真实的」。假在线、错误响应当空数据、表单被轮询清空、点了没反应的吊销按钮——这些不修，视觉再美也是化妆给病人。
2. **灰阶工具，颜色是报警器**：全站彩色预算 4 个（蓝 accent / 橙 heat / 绿 live / 红 danger），8 个任务状态映射 4 个视觉档位——视觉档位数 < 状态数是故意的。
3. **动效只给召唤层**：弹层/抽屉/toast 配全动效，列表表格路由零动效。只动画 transform/opacity，不动画 blur（裁决 motion.md 内部张力：大面积模糊重算不值）。

## P0 可靠性与诚实性（新增阶段，codex 清单落地）

按性价比排序，全部是本轮必修：

| # | 问题 | 位置 | 修法 |
|---|---|---|---|
| 1 | 错误响应当空数据、假在线 | app.js:188-193,253,462 | fetch 非 200 不覆盖快照；轮询失败保留旧数据+顶栏显示「数据更新于 HH:MM（陈旧）」；节点在线判定与心跳挂钩 |
| 2 | 设置表单被轮询清空 | app.js:246-251,293 | 轮询豁免清单加入设置页（与派单表单同权）；E2E 现有 1500ms sleep 规避改为断言免豁免后真实通过 |
| 3 | 抽屉草稿只护焦点不护草稿 | app.js:935-938 | 抽屉打开期间一律跳过整区重绘，改为对 drawer-body 定点增量更新（此改造同时是 P2 动效的前置） |
| 4 | 假吊销按钮 | app.js:492,1689 | 「吊销」改为「撤下说明」（只读说明弹层），不美化不存在的能力；V0 无撤销 API 就不说有 |
| 5 | 忙态不恢复/重建后复活 | app.js:1649-1665,1695-1705,1725-1748 | 忙态按操作对象存 JS 状态（S.busy set），渲染函数读取，不依赖旧 DOM；失败路径 finally 恢复 |
| 6 | 焦点管理基础 | app.js:1798,938,1880-1890,323 | 弹层 aria-modal 关联标题；抽屉开时移焦入内、关时返回触发元素；总览可点 metric 从 div 改 button（键盘可达） |
| 7 | E2E 水分 | e2e:183-184,60-61,160 | E19 补失败样本；E04 增加下钻筛选结果断言；E11 修未定义 p；全脚本 PASS/FAIL/SKIP 三态化 |

验收：有在途任务+慢请求+断网+表单填写中四场景手测不丂数据不假在线；E2E 22+/22+ 全绿（新增用例）。

## P1 Token + 核心组件（zcode 草案 A+B 全量，含密度修正）

### Token 表
采用 zcode A.1 全表（:root 40+ 变量：表面/灰阶 6 档/发丝线/语义 4 色/圆角 6 档/双层阴影 5 档/字体栈/字号（运维密度整体下移一档）/动效曲线时长）。token 块直插 style.css 首部，不新增 CSS 文件；node 侧同块同步（P1.5）。全局 `tabular-nums` 与 `font-optical-sizing:auto` 两条规则随 token 落地。

验收：token 块之外硬编码色值 grep 命中 0；五页截图地面冷灰、主按钮 Apple 蓝、紫色系全灭。

### 8 状态 → 4 视觉档（zcode A.2 全表）
- needs_review：全站唯一实心彩 chip（heat 底+字+内描边）
- failed：灰 chip 红字（danger 只表示失败/不可逆）
- running：灰 chip 蓝点脉冲（点=还会动态变化）
- submitted/offered/acknowledged：同款灰 chip（握手态语义等价，精确时刻在时间线里）
- completed：灰 chip；cancelled：灰 chip 更淡一档
- 节点 online 绿点/offline 无点灰字

### 组件重构（zcode B.1-B.8 + codex 密度修正）
- 统一面板：总览四统计卡合并一格一面板+发丝线，`.metric` 类保留（E2E 锚点），hover 用底色不用 lift
- 侧栏：macOS source-list 白 pill 选中+微阴影，紫色 tint 灭失
- 表格：**不限宽**（codex：保留宽表扫读密度，1080px 内容站容器不照搬）；表头去灰底靠字号字重分层；**sticky 表头**（codex 补充 #7）；任务 ID mono 小字+断行策略；**1.85 行高不进表格**（codex：dense data UI 允许收紧，但字号不低于 12.5px）
- 筛选 chips：白 pill 常态/黑 pill 选中；高级筛选（提交方/失败原因/时间范围）收进默认折叠 `<details>`；`.chip-link` 恒 8 个（E2E 锚点）
- 弹层：宽屏体系不动，实白表面+深层双层阴影（codex：宽弹层优先实底可读，玻璃不进弹层）
- toast：底部黑条 → 顶部玻璃胶囊（全站第二处玻璃位），`.show` 类切换替代 hidden 属性
- 按钮/输入/notice/向导步骤/群气泡/timeline：按 zcode B.8 值表全量换血；`:active` scale(.97) pointer-down 反馈
- **原生 select 保留**，只重绘外框（codex+zcode 一致：无构建链下自绘下拉的键盘可达性成本>收益）
- 群消息：**只在原本接近底部时跟随**，回看时右上角「有新消息」浮标（codex 补充 #8）
- CSS 作用域收敛：`tbody tr:hover` 限定 `tr[data-task]`（不误伤只读表）；`.card+.card` 相邻选择器改显式间距类；`--accent` 未定义引用修复（codex 补充 #9）

验收：E2E 20/20+ 新增用例全绿；`grep -iE '4a6cf7|e9edff|f7f6f3'` 命中 0；总览单面板；长任务列表 sticky 表头生效。

## P2 动效层（前置：P0#3 的弹层外壳常驻改造）

zcode C 节预算表全量，E 节风险清单为验收基准：

- 弹层：进 400ms `--ease-spring`（临界阻尼无过冲）/出 260ms 同路径；scrim 250ms 略快于面板；`.closing{pointer-events:none}` 关闭动画期不吃点击（高危 #1）；280ms 后清 DOM（E06 detached 断言兼容）
- 抽屉：右缘升入 translateX(48→0)；开时只增量换 body 子树（轮询 5s 不重播动画——高危 #2）
- 向导：步进只换内容+200ms 轻动效，不整层重开（高危 #3）；E06 `.modal` 断言期间节点恒在
- toast：320ms spring 进/220ms 出，顶部居中
- 全站 `:active` 按压 100ms
- topbar `.scrolled` 条件发丝线（scrollY>8，passive 监听）
- 三条媒体查询缺一不验收：reduced-motion（降为 200ms 淡入淡出）/reduced-transparency（玻璃转实白）/prefers-contrast（边框增强）
- 明确零动效：轮询重绘、路由切换、badge 翻转、筛选、排序；列表禁止入场渐显 keyframes（重渲染卡 0 红线）
- hover lift 仅群卡片（真独立对象）；表格行/链接/面板格子用底色瞬变（codex：目标移动妨碍连续扫读）

验收：动画中途连按 Esc 不错乱；DevTools 三种 reduced-* 生效；E2E 20/20。

## P3 细节打磨

- 标题负字距补齐（h2 -0.015em；h1 已有 -1px 保留，审计第 5 条已由 codex 纠正）
- topbar 发丝线滚动条件化（若 P2 未覆盖）
- 与 reference.html 走查收尾：色值/圆角/阴影 token 抽查
- 暗色模式：**不做**，单独立项（codex+zcode 一致）

## 明确不做（restraint is luxury，zcode F 节 9 条全采纳）

原生下拉不换自绘 / 暗色模式不做 / 玻璃不上内容表面 / 列表无入场渐显 / 不引图标库 / 无 CTA 渐变 / 不追 44px 触控（桌面工具豁免）/ 不自定义滚动条 / 不动信息架构与既有文案锚点。

## E2E 与锚点纪律（贯穿全程）

- 业务锚点冻结：`data-act/data-id/data-task`、表单 id 全集、`.metric`/`.chip-link` 计数、「在线」文案、summary「将在…执行」、placeholder 前缀、PRIME_PROMPT 开头串
- 视觉类选择器（.drawer/.modal 等结构类）语义保持；文案改动必须同步 E2E
- 每 P 阶段交付都跑 15 包 go test + E2E 全量；「全绿」须附覆盖说明（codex：声明新增用例与跳过项）
- 禁止用延长 sleep 或放宽断言换取绿灯（codex 点名的现行坏例：e2e:231 的 1500ms 等待随 P0#2 修复移除）

## 实施分工

zcode 主写（它出草案最熟）；编排者逐阶段审计+E2E；每阶段完成交**新会话 codex** 复核（伦哥要求的循环审查制）；node 侧（internal/node/web）在 P1.5 用同 token 块同步，E2E node 断言不动。
