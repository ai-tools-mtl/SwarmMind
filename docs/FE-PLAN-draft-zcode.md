# SwarmMind 控制台前端优化实施方案（FE-PLAN · zcode 草案）

> 事实基础：FE-AUDIT.md（18 问题）+ apple-design-skill 全套规范 + style.css/app.js 现状 + e2e-console.mjs 锚点。
> 三个决定性判断，后续所有取舍由此推导：
> 1. **这是灰阶工具，颜色是报警器不是装修**——8 个任务状态里只有「需要人」和「坏了」配得到颜色，其余全靠文字标签与灰阶字重区分。
> 2. **统一面板消灭碎片卡**——总览四张独立白卡合并为一块面板 + 发丝线；fragmentation 是 anti-slop 第一条。
> 3. **动效只给「召唤出来的层」**（弹层/抽屉/toast），列表、表格、页面切换一律零动效——深夜巡检要的是零等待感。

适用范围：`internal/hub/web/*` 为主，`internal/node/web/*` 同 token 同步（P0.5）。不改任何 Go 代码与 API 形状。无构建链：**不新增 CSS 文件**，token 块直接置于 style.css 首部（避免静态服务路由不确定性）。

---

## A. Token 层设计

### A.1 `:root` 变量表（直接替换 style.css 第 2 行，可整体粘贴）

```css
:root{
  /* 表面 */ --bg:#f5f5f7; --surface:#ffffff; --hover:#fbfbfd; --track:#e8e8ed;
  /* 文字灰阶 */ --ink:#1d1d1f; --ink-2:#424245; --ink-3:#6e6e73; --ink-4:#86868b; --ink-5:#aeaeb2; --faint:#d2d2d7;
  /* 线 */ --hairline:rgba(0,0,0,.07);
  /* 语义色（全站预算只有这 4 个，见 A.3） */
  --accent:#0071e3; --accent-press:#0066cc; --accent-bg:rgba(0,113,227,.08); --accent-ring:rgba(0,113,227,.20);
  --heat:#ff6b00; --heat-bg:rgba(255,107,0,.10); --heat-ring:rgba(255,107,0,.35);
  --live:#30d158; --live-ink:#248a3d; --live-bg:rgba(48,209,88,.14);
  --danger:#d70015; --danger-bg:rgba(215,0,21,.06); --danger-line:rgba(215,0,21,.30);
  /* 圆角（不许发明中间值） */ --r-pill:999px; --r-chip:6px; --r-thumb:12px; --r-sheet:16px; --r-card:18px; --r-panel:22px;
  /* 阴影（全部两层，禁止单层硬阴影） */
  --sh-card:0 1px 2px rgba(0,0,0,.04),0 8px 24px rgba(0,0,0,.05);
  --sh-panel:0 1px 3px rgba(0,0,0,.05),0 14px 40px rgba(0,0,0,.05);
  --sh-lift:0 12px 32px rgba(0,0,0,.10);
  --sh-overlay:0 2px 8px rgba(0,0,0,.10),0 30px 80px rgba(0,0,0,.24);
  /* 玻璃（仅 topbar 与 toast） */ --glass-bg:rgba(245,245,247,.72);
  /* 遮罩 */ --scrim:rgba(0,0,0,.32);
  /* 字体 */ --font:-apple-system,BlinkMacSystemFont,'SF Pro Text','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif;
  --mono:ui-monospace,SFMono-Regular,Menlo,monospace;
  /* 字号（运维密度场景整体下移一档：长列表扫读优先，有意偏离营销页阶梯） */
  --fs-h1:24px; --fs-h2:17px; --fs-h3:15px; --fs-body:14px; --fs-small:12px; --fs-micro:11.5px; --fs-metric:28px;
  /* 动效 */ --ease-spring:cubic-bezier(0.32,0.72,0,1); --ease-out-quart:cubic-bezier(0.25,1,0.5,1);
  --dur-enter:400ms; --dur-exit:260ms; --dur-scrim:250ms; --dur-micro:100ms; --dur-hover:180ms;
}
```

配套两条全局规则：`body{font-family:var(--font);-webkit-font-smoothing:antialiased;font-optical-sizing:auto}`；数字一律 `font-variant-numeric:tabular-nums`（挂 `.metric`、`td`、`.mini-stat b`、`#session-left`）。

### A.2 8 个任务状态在灰阶体系下的区分策略

扫读真相：巡检时只有四个问题——「要我处理吗」「坏了吗」「在跑吗」「其余」。8 个状态映射到 4 个视觉档位，**视觉档位数 < 状态数是故意的**：

| 状态 | 视觉 | 具体 token | 依据 |
|---|---|---|---|
| needs_review 待人工核对 | **全站唯一实心彩 chip**：heat 底+heat 字+heat 内描边 | bg `--heat-bg`、color `--heat`、`box-shadow:inset 0 0 0 1px var(--heat-ring)`、weight 650 | 唯一会「召唤人」的终态；本 Loop 硬要求一眼可辨，给它最贵的视觉预算 |
| failed 执行失败 | 灰 chip + 红字 | bg `rgba(0,0,0,.05)`、color `--danger` | 红在本系统只许表示「失败/不可逆」，与 heat 的「需要注意」形成两个不同报警频道 |
| running 执行中 | 灰 chip + 蓝点脉冲 | 5px 圆点 `--accent`，`pulse 2.2s infinite` | 蓝=系统在工作；脉冲是信息（活性），不是装饰 |
| submitted / offered / acknowledged | 同一款基础灰 chip | bg `rgba(0,0,0,.05)`、color `--ink-3` | 三个握手态对巡检者语义等价（都在排队）——色彩区分是伪信息，抽屉时间线里有精确时刻 |
| completed 已完成 | 基础灰 chip | 同上 | 「好、跳过」不配得到颜色 |
| cancelled 已取消 | 灰 chip 更淡一档 | color `--ink-4` | 终态中的哑态 |
| 节点 online / offline | 灰 chip + 绿点 / 无点灰 chip | 点 `--live`；offline 字 `--ink-4` | 绿色全站只表示「连通性」，绝不挪用 |

点规则：`.badge:before` 默认 `display:none`，仅 `.running`（蓝脉冲）、`.online`（绿常亮）、`.needs_review`（heat 常亮）三个类开点。**点=该状态还会动态变化**；submitted/offered/acknowledged 是瞬时握手，挂点就是噪音。

### A.3 语义色预算与依据

全站允许出现的彩色共 4 个：`--accent`（主操作/链接/焦点/进度）、`--heat`（需要注意，含 needs_review 与警示 notice）、`--live`（连通性绿）、`--danger`（失败/不可逆操作）。审计说「只保留 heat 与 live」，本方案**有意扩展出 danger**：撤下节点、关群、撤销令牌、取消任务都是不可逆动作，现状用橙系 `#a4510b` 同时表达「待核对」「警告」「危险」三种语义，正是彩色 badge 嘈杂的根因；拆出红色后每个颜色恰好对应一种巡检动作。紫色系（#4a6cf7/#e9edff/#f5efff 等）**全部灭失**，无替代——紫在 Apple 体系只存在于渐变第二停点，本控制台没有渐变场景。

---

## B. 组件重构清单（按可粘贴程度给出关键 CSS）

### B.1 统一面板：总览四统计卡合并

app.js `overviewPage()` 的 `metric()` 输出改为面板内格子（**保留 `.metric` 类在数字上**，E2E E03 断言 `.metric` ≥3）：

```css
.panel{background:var(--surface);border-radius:var(--r-panel);box-shadow:var(--sh-panel)}
.stat-grid{display:grid;grid-template-columns:repeat(4,1fr)}
.stat-grid>.stat{padding:20px 22px;cursor:pointer}
.stat-grid>.stat:hover{background:var(--hover)}
.stat+.stat{border-left:1px solid var(--hairline)}
@media(max-width:1050px){.stat-grid{grid-template-columns:1fr 1fr}
  .stat{border-left:0!important}.stat:nth-child(even){border-left:1px solid var(--hairline)!important}
  .stat:nth-child(n+3){border-top:1px solid var(--hairline)}}
.metric{font-size:var(--fs-metric);font-weight:700;letter-spacing:-0.02em;font-variant-numeric:tabular-nums}
```

「等待核对」卡的橙左竖条（audit #8）删除，改为数字本身在 nr>0 时着 `--heat`。hover 用底色不用 lift：面板格子抬升会撕裂面板整体，bg 变化即为「可点」信号。既有 `.card` 重定义为 `border:0;border-radius:var(--r-card);box-shadow:var(--sh-card)`，供「中心端状态/需要你关注」等非兄弟区块继续使用。

### B.2 状态 chip/badge（类名冻结，值重写）

app.js `badge()` 输出 `badge ${s}` 不变，CSS 重定义：

```css
.badge{display:inline-flex;align-items:center;gap:6px;padding:3px 10px;border-radius:var(--r-pill);
  font-size:var(--fs-micro);font-weight:550;white-space:nowrap;background:rgba(0,0,0,.05);color:var(--ink-3)}
.badge:before{content:'';width:5px;height:5px;border-radius:50%;background:currentColor;display:none}
.badge.running:before{display:inline-block;background:var(--accent);animation:pulse-dot 2.2s infinite}
.badge.online:before{display:inline-block;background:var(--live)}
.badge.needs_review{background:var(--heat-bg);color:var(--heat);font-weight:650;box-shadow:inset 0 0 0 1px var(--heat-ring)}
.badge.needs_review:before{display:inline-block}
.badge.failed{color:var(--danger)}
.badge.cancelled,.badge.offline{color:var(--ink-4)}
@keyframes pulse-dot{0%,100%{opacity:1}50%{opacity:.35}}
```

### B.3 侧栏选中态（macOS source-list 范式）

```css
aside{background:var(--bg);border-right:1px solid var(--hairline)}
nav button{border-radius:var(--r-pill);padding:9px 14px;color:var(--ink-3)}
nav button:hover{background:rgba(0,0,0,.045)}
nav button.active{background:var(--surface);color:var(--ink);font-weight:600;
  box-shadow:0 1px 3px rgba(0,0,0,.08),0 0 0 1px rgba(0,0,0,.04)}
.navnum{font-size:11px;font-variant-numeric:tabular-nums;color:var(--ink-4)}
```

紫色 tint `#e9edff` 选中底废除；白 pill+微阴影在灰底上就是 macOS 侧栏的语言。01-07 编号结构不动。

### B.4 表格行

```css
th{background:none;color:var(--ink-4);font-size:11.5px;font-weight:550;white-space:nowrap}
th,td{padding:12px 14px;border-bottom:1px solid var(--hairline)}
td{font-variant-numeric:tabular-nums}
tbody tr[data-task]{cursor:pointer}tbody tr:hover{background:var(--hover)}
td small{color:var(--ink-4);font-family:var(--mono);font-size:11.5px}
```

表头灰底 `#fafbfc` 删除（表头靠 11.5px+--ink-4 与数据分层），行内任务 ID 用 mono 小字（工程工具属性，唯一保留的「第二个字体」）。时间列对齐由 tabular-nums 解决。

### B.5 弹层（glass materialize + 宽度不动）

`.modal` 宽度体系（920/560 slim）是既定资产，原样保留；材质与动效重写（动效参数见 C）：

```css
.overlay{position:fixed;inset:0;z-index:50;display:flex;align-items:center;justify-content:center;
  padding:24px;background:var(--scrim);opacity:0;transition:opacity var(--dur-scrim) ease}
.overlay.open{opacity:1}
.overlay.closing{opacity:0;pointer-events:none}          /* 关闭动画期不得吃掉后续点击（见 E） */
.modal{width:min(920px,94vw);max-height:86vh;overflow:auto;background:var(--surface);
  padding:28px;border-radius:var(--r-sheet);box-shadow:var(--sh-overlay);
  opacity:0;transform:translateY(12px) scale(.98);
  transition:opacity var(--dur-enter) var(--ease-spring),transform var(--dur-enter) var(--ease-spring)}
.overlay.open .modal{opacity:1;transform:none}
.overlay.closing .modal{opacity:0;transform:translateY(8px) scale(.985);
  transition-duration:var(--dur-exit)}
.drawer{width:min(700px,96vw);background:var(--surface);overflow:auto;padding:28px 32px;
  box-shadow:var(--sh-overlay);transform:translateX(48px);
  transition:transform var(--dur-enter) var(--ease-spring),opacity var(--dur-enter) var(--ease-spring);opacity:0}
.overlay.open .drawer{transform:none;opacity:1}
.overlay.closing .drawer{transform:translateX(40px);opacity:0;transition-duration:280ms}
```

弹层是实白表面，不做 backdrop-filter materialize（玻璃只留 topbar/toast）；「materialize 感」由 scrim+位移+缩放共同表达。

### B.6 toast 重设计（底部黑条 → 顶部玻璃胶囊）

```css
.toast{position:fixed;top:74px;left:50%;translate:-50% -10px;opacity:0;z-index:100;pointer-events:none;
  max-width:min(520px,90vw);padding:10px 18px;border-radius:var(--r-pill);
  background:rgba(250,250,252,.86);backdrop-filter:saturate(180%) blur(20px);-webkit-backdrop-filter:saturate(180%) blur(20px);
  border:1px solid rgba(0,0,0,.08);box-shadow:0 2px 8px rgba(0,0,0,.08),0 12px 32px rgba(0,0,0,.12);
  color:var(--ink);font-size:13px;font-weight:550;
  transition:translate 320ms var(--ease-spring),opacity 320ms var(--ease-spring)}
.toast.show{translate:-50% 0;opacity:1}
@media (prefers-reduced-transparency:reduce){.toast{background:#fff;backdrop-filter:none;-webkit-backdrop-filter:none}}
```

JS 改动（app.js `toast()`）：`hidden` 属性换为 `.show` 类切换（`[hidden]{display:none!important}` 会杀死过渡），3400ms 自动消失逻辑与 `#toast` id 保留。移到顶部：底部是长表格末行常驻区，顶部紧邻 topbar 会话状态，信息动线一致。toast 浮在滚动内容之上——这是全站第二个有资格用玻璃的位置。

### B.7 空状态与筛选 chips

```css
.empty{text-align:center;padding:56px 24px}
.empty h2{font-size:17px;font-weight:600;letter-spacing:-0.01em}
.empty p{max-width:440px;margin:8px auto 16px;font-size:13.5px;line-height:1.7;color:var(--ink-3)}
.filters button{border:0;background:rgba(0,0,0,.05);border-radius:var(--r-pill);
  padding:6px 14px;font-size:12.5px;font-weight:500;color:var(--ink-2)}
.filters button:hover{background:rgba(0,0,0,.08)}
.filters button.on{background:var(--ink);color:#fff;font-weight:600;
  box-shadow:0 1px 2px rgba(0,0,0,.22),inset 0 1px 0 rgba(255,255,255,.16)}
.filters .badge{background:none;padding:0;color:inherit;font-weight:650}
```

任务页筛选是**强筛选**，选黑 pill（components.md 两种 segmented 语义的判定）；总览下钻 chips 复用 `.filters` 容器自动获得同款。**`.chip-link` 按钮数量必须恒为 8**（E04 断言），不新增「全部」。高级筛选字段（提交方/失败原因/时间范围）收进默认折叠的 `<details><summary>高级筛选</summary>`，搜索 ID 与状态 chips 常驻——解决 audit #11 的平铺无层级。

### B.8 其余一次性换血（值直接给）

- **按钮**：`button{border:0;background:rgba(0,0,0,.05);border-radius:var(--r-pill);height:34px;padding:0 16px}` hover `rgba(0,0,0,.08)`；`.primary{background:var(--accent);color:#fff}` hover `--accent-press`；`.danger{background:#fff;border:1px solid var(--danger-line);color:var(--danger)}` hover `--danger-bg`；全站 `:active{transform:scale(.97);transition:transform var(--dur-micro) ease-out}`（pointer-down 反馈）。
- **输入**：`border:1px solid var(--faint);border-radius:var(--r-thumb)`；`:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-ring)}`；label `margin-bottom:6px`、`.fields{gap:16px}`（修 audit #14 均匀无呼吸）。
- **notice**：默认 `background:rgba(0,0,0,.04);border:0;border-radius:var(--r-thumb);color:var(--ink-2)`；`.warn` 用 `--heat-bg`+`--heat` 标题；`.notice.purple` 类删除（needs_review 引导文案改用 heat 底）；`.enroll-ok` 用 `--live-bg`+`--live-ink`。
- **向导步骤**：done `background:var(--accent-bg);color:var(--accent)`、on `background:var(--accent);color:#fff`、连接线 done `rgba(0,113,227,.35)`；蓝单一色相表达进度。
- **群聊气泡**：user `background:var(--accent-bg);border-color:rgba(0,113,227,.15)`，radius 16；agent 白+hairline；system `rgba(0,0,0,.04)`。
- **timeline**：`border-left:1px solid var(--hairline)`、圆点 `--faint`（历史记录是灰的，只有 needs_review 事件点用 heat）。

---

## C. 动效预算表（zfjwork 四维度：对象与触发 → 运动与物理 → 视觉细节 → 边界与性能）

**全动效（召唤层，interruptible）：**

| 对象/触发 | 运动与物理 | 视觉细节 | 边界与性能 |
|---|---|---|---|
| 弹层 modal；`openLayerPlain`/`closeLayer`（Esc/遮罩/按钮） | 进 400ms `--ease-spring`（临界阻尼弹簧，无过冲）；出 260ms 同路径反向 | scrim `--scrim` 250ms 淡入淡出（比面板快）；面板 opacity+translateY(12px)+scale(.98)→none；transform-origin 默认中心 | CSS transition 非 keyframes（中途再触发可反转）；只动 opacity/transform；closing 期 `pointer-events:none` |
| 抽屉 drawer；行点击/`openTask` ↔ Esc | 进 400ms spring，出 280ms，同路径 | `translateX(48px)→0` 右侧升入；scrim 同上；transform-origin 右缘 | 同上；轮询重绘只换 `.drawer-body` 子树（见 E） |
| toast；任何 `toast()` 调用 | 进 320ms spring，出 220ms `--ease-out-quart` | 玻璃胶囊 `translate -10px→0`+opacity；顶部居中 | 单实例复用（clearTimeout 现制保留）；reduced-motion 只剩 200ms 淡入淡出 |
| 按钮按压；pointer-down | 100ms ease-out | `scale(.97)` | 全站统一；touch 无场景（桌面豁免 44px） |

**只配 hover 层：** 群卡片 `translateY(-2px)`+`--sh-lift`，200ms `--ease-out-quart`（真独立对象才配 lift）；筛选 chip/seg/链接/表格行 = 底色或字色瞬变（180ms 内），无位移。

**明确不加动效：** 5s 轮询重绘（**禁止入场渐显 keyframes**——重渲染会卡在 0）；hash 路由页面切换；badge 状态翻转；表格排序/筛选；面板格子与「查看全部→」（bg hover 即可）。**信息性循环保留**：running 蓝点脉冲 2.2s、spinner 0.8s linear。**轻动效一处**：向导步进/方式切卡换内容 200ms `translateY(4px)`+opacity 进场（用户主动触发的新 pane，可给）——但必须走「弹层内容替换」而非整层重开（见 E）。

**三条媒体查询为 P2 交付物，缺一不验收：**

```css
@media (prefers-reduced-motion:reduce){.overlay,.modal,.drawer,.toast{transform:none!important;transition:opacity 200ms ease}}
@media (prefers-reduced-transparency:reduce){.topbar,.toast{background:#fff;backdrop-filter:none;-webkit-backdrop-filter:none}}
@media (prefers-contrast:more){.topbar,.toast{background:#fff;border-bottom:1px solid rgba(0,0,0,.35)}}
```

---

## D. 分阶段实施顺序与验收标准

**P0 token 化换血（纯值替换，零结构改动）**
做法：style.css 首部插入 A.1 token 块；全部硬编码色/圆角/阴影替换为 `var()`，旧变量名（--bg/--ink/--blue/--line/--shadow）重指向新值作为过渡别名。app.js 无硬编码色，不动。P0.5：同块复制进 `internal/node/web/style.css`。
验收：① token 块之外 `grep -E '#[0-9a-fA-F]{3,8}|rgba?\('` 命中 0；② 五页截图肉眼过：地面冷灰、主按钮蓝、无暖米；③ go test 全绿 + E2E 20/20。

**P1 组件重构（DOM 可改，锚点冻结）**
做法：B.1–B.8 全量。改 app.js 的 `metric()`（stat 格子化）、高级筛选 `<details>`、`overviewPage` 删紫 notice；CSS 重写 badge/侧栏/表格/chips/按钮/输入。
验收：① `.metric` 计数仍 ≥3、`.chip-link` 恒 8、`th:has-text("提交时间")` 在、全部 `[data-act]`/`#id` 原样；② `grep -iE '4a6cf7|5b7fd4|e9edff|edf0ff|c5cffd|f5efff|f7f6f3|faf9f6'` 命中 0；③ 总览单面板+发丝线；④ E2E 20/20。

**P2 动效层（动 app.js 层生命周期，动效唯一碰 JS 的阶段）**
做法：`openLayerPlain/closeLayer/syncDrawer/toast/enrollGo` 改造（进出场类切换、closing 指针隔离、抽屉 body 增量替换、向导内容替换不整层重开）；`:active` 按压；topbar `.scrolled` 发丝线滚动监听（`scrollY>8`，passive）；三条媒体查询。
验收：① 动画中途连按 Esc 不错乱、不卡死层；② 关闭动画期点击穿透为 0（`pointer-events:none`）；③ DevTools 模拟三种 reduced-* 均生效；④ E06 的 `.modal` 文本断言仍过（内容替换后类名结构不变）；⑤ E2E 20/20。

**P3 细节打磨**
做法：h1/h2 负字距（-0.022em/-0.015em）；全站 tabular-nums（metric/表格/倒计时）；topbar 永久 hairline 改 `.scrolled` 条件发丝线；timeline/向导连接线等余量灰阶微调；与 reference.html 截图走查。
验收：① getComputedStyle 抽查 h1 letter-spacing 与 .metric font-variant-numeric；② 页顶时 topbar 无线、滚动 8px 后出现；③ 20/20 收官。

---

## E. 风险与回归清单

**高危（会直接打红 E2E）：**
1. **关闭动画吃点击**：Esc 关抽屉后 E2E 立刻点侧栏「任务派发」——若 overlay 还在 DOM 且接收指针，点击会命中遮罩。对策：`.closing{pointer-events:none}` + 280ms 后再清 `#layer`。这是 P2 第一验收项。
2. **轮询重绘重放开场动画**：抽屉开着时 5s tick → `syncDrawer` 重建 innerHTML 会把 `.drawer` 打回 opacity:0，每 5s 闪一次。对策：抽屉已开时只替换 `.drawer-body` 子节点，section/overlay/类名不动；首开才走 rAF 加 `.open`。
3. **向导步进重播弹层入场**：现状 `enrollGo` 走 `openLayerPlain` 整层重开，加动效后每步会重播 400ms 弹出。对策：层已开时替换 `.modal` 子内容+轻动效，不重建 overlay。E06 断言 `.modal` 文本含「e2e-drill/hub.yaml/复制令牌」——内容替换必须保持同一 `.modal` 节点。
4. **Playwright 可见性判定**：`waitForSelector` 认为 opacity:0 仍算 visible，动效不会拖慢等待；但 `state:'detached'`（E06 尾）要求节点真移除——280ms 延迟清层在 15s 超时内，安全。
5. **计数锚点**：`.chip-link`===8（不得增删下钻 chip）、`.metric`≥3（数字元素必须保留类名）、`/在线/g`≥2（节点表 badge 文案「在线」冻结）、`text=已提交`（批次报告 mini-stat 措辞冻结）。

**中危：**
6. 全部 `data-act`/`data-id`/`data-fchip`/`data-picker-*` 与表单 id（`#ctrl-node/#ctrl-agent/#ctrl-prime/#ctrl-stdin/#ctrl-summary-line/#enroll-*/#token-*/#acc-*`）为断言或联动锚点，P1 重构时逐个对照保留；summary 行「将在…执行」文案冻结（E09b）。
7. placeholder 前缀「描述复合目标」「用自然语言描述」被 E10 精确断言，改文案必须同步 E2E。
8. 总理提示词 `PRIME_PROMPT` 开头串是 E10b 断言（`# SwarmMind 总理智能体（Prime）`）——本轮绝不动它。
9. `.card` 类被登录页（`.login`）复用，重定义阴影/圆角时确认 450px 登录卡不破版。
10. `#toast` 从 `hidden` 属性改 `.show` 类：检查 `[hidden]{display:none!important}` 全局规则残留是否影响其他调用点（现仅 toast 用 hidden 切换，改造后无残留调用）。

---

## F. 明确不做什么（restraint is luxury）

1. **不自建下拉组件**：两处 `<select>`（audit #13 嫌原生丑）保留原生。无构建链下自绘下拉要手写键盘可达性/外点关闭/滚动锁，换来的只是好看一点——只重绘外框（边框/圆角/焦点环），列表本身交给浏览器。
2. **不做暗色模式**：深夜巡检场景诱惑极大，但 token 表翻倍、每张截图回归翻倍、状态色对比需整套重调。P3 收官后单独立项。
3. **玻璃不上内容表面**：列表、表格、面板、抽屉主体永远实白。全站玻璃仅 topbar 与 toast 两个「真重叠」位置——over-glassing 在白底工具里读起来是脏。
4. **列表/卡片不加入场渐显**：motion.md 红线（重渲染卡 0），且 5s 轮询的工具页加入场动画等于自造闪烁。
5. **不加图标库**：现有 ◈ ⓘ ✓ × 已覆盖全部语义；引入 Lucide 是加分项不是痛点，26 处按钮逐个配图标只会制造噪音。
6. **不做 CTA 渐变块/orbs**：控制台没有营销 hero，`--grad-cta` 场景为零。
7. **不追 44px 触控目标**：桌面键鼠运维工具（硬约束明示），34px 控制高度是密度正解；触屏非本产品场景，写入豁免而非欠账。
8. **不自定义滚动条**：`::-webkit-scrollbar` 美化是常见 AI 味，原生滚动条在目标平台（macOS/Linux 桌面）已足够。
9. **不动信息架构与文案**：01-07 编号、页面名、按钮词、「需要你关注」等标题全冻结——它们同时是 E2E 锚点和伦哥已定的 UX 资产。
