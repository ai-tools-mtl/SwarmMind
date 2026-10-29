# SwarmMind 双端控制台 · 前端视觉与交互打磨提案

> 只读分析产出，未修改任何仓库文件。分析对象：`internal/hub/web/`（index.html 24 行 / app.js 3906 行 / style.css 455 行）与 `internal/node/web/`（22 / 2044 / 291 行），以当前 HEAD（`feat/frontend-polish` @ 043f9e3）为准。文中行号均可直接定位。
>
> 遵守既有定案：灰阶工具视觉不变；语义色预算 4 个不扩编；表格字号下限 12.5px；玻璃只给 topbar/toast；动效是反馈不是表演。所有微动效提案均要求并入两端既有 `prefers-reduced-motion` 降级块（hub style.css:372-377 / node style.css:224-226），并在涉及 shared block 时遵守「双端同改同验」。

---

## 〇、现状资产盘点（提案基线）

**已落地的动效**（不重复建设，只在此之上补缺口）：

| 场景 | 实现 | 位置 |
|---|---|---|
| 弹层/抽屉/向导 pane 进出场 | 400ms spring 进 / 260-280ms 出，`--ease-spring` | hub style.css:247-262, 302-305 |
| scrim 遮罩 | 250ms opacity | hub style.css:247 |
| toast（hub） | 320ms spring 进 / 220ms out-quart 出，玻璃胶囊 | hub style.css:319-320 |
| 开关滑块 `.switch` | 180ms out-quart translate | hub 446-450 / node 283-287 |
| 按钮 hover/按压 | bg 180ms + active scale(.97) 100ms | hub 37-41 / node 36-40 |
| 侧栏选中态 | bg 过渡 + 白 pill 阴影 | hub 80-82 / node 68-70 |
| topbar 条件发丝线 | scrollY>8 挂 .scrolled，300ms | hub 59-60 |
| 群卡 hover lift | 200ms out-quart，全站唯一 lift | hub 348-349 |
| running 徽标脉冲 | pulse-dot 2.2s opacity | hub 119,125 / node 90,96 |
| 关闭动画/焦点陷阱/inert/层世代 | D3 状态机完整 | hub app.js:1737-1873 |

**排版资产**：`--mono` 用于 code/pre、`td small`（任务 ID）、`.md-lang`、`.md-log-label`；`tabular-nums` 用于 `#session-left`、`.navnum`、`.metric`、`td`（全列）、`.mini-stat b`、`.ledger-line b`、md 表格 td。

**结构性事实**（决定了下文所有方案的边界）：
- 两端都是「整页/整卡 innerHTML 重建 + 轮询静默」架构：hub `render()` 5s 全量重建 `#app`（app.js:922-951），node `loadAll()` 指纹变化才重建（app.js:604-607, 638-646）。**任何依赖「同节点类切换触发 transition」的动效在轮询路径上都会失效或重放，必须用「数据变化判据 + one-shot animation」实现**。
- 抽屉走 D22 分区 diff（shared block，`drawerZonesApply`，hub app.js:398-410）：缓存一致不碰 DOM——这是现成的「数据真变了」信号，可白嫖为动效触发点。
- 交互路径（点击）大多直接 `render()`，动效只能挂在 render 之后的节点上。

---

## 一、微动效（非召唤层）

### 1.1 抽屉分区内容淡入（数据变化反馈）—— 本提案最推荐的一项

【现状】抽屉 5s 轮询刷新走分区 diff（hub app.js:393-410，node app.js:301-318），内容变化时 `drawerZoneSwap` 直接 `innerHTML` 替换，新旧内容瞬间硬切。用户看不到「哪里变了」，尤其状态徽标从「执行中」跳「已完成」、最终答复从无到有时。
【问题】diff 机制保证了「只有数据真变的分区才重写」，但重写瞬间发生，视觉上与静默无异——系统最值得反馈的时刻（任务到终态、答复出现）恰恰最安静。
【方案】在 shared block 的 `drawerZoneSwap` 里加 one-shot 动画类（**两端同改同验，web_drawer_sync_test 锁同文**）：

```js
// shared block drawerZoneSwap 内，innerHTML 写入后：
el.classList.remove('zone-in'); void el.offsetWidth; el.classList.add('zone-in');
```
```css
/* hub/node style.css 各加一条（非 shared 块，但两端同值） */
@keyframes zone-in{from{opacity:.35}}
[data-zone].zone-in{animation:zone-in 180ms var(--ease-out-quart)}
```
reduced-motion 块内追加 `[data-zone].zone-in{animation:none!important}`。不新增 token：180ms 即 `--dur-hover` 值，可写 `var(--dur-hover)`。
【优先级】P1
【风险】低。触发点被缓存 diff 天然门控（数据没变不触发，轮询不会造成持续闪烁）；details 开合回放不受影响（动画在容器不在子节点）；两端 shared block 必须同改，否则锁测试红。

### 1.2 群聊新消息入场动效

【现状】群详情 3s 增量拉取，新消息经 `insertAdjacentHTML('beforeend', groupMsgHtml(m))` 追加（hub app.js:2785-2787），瞬间出现在时间线尾部。
【问题】聊天时间线是新消息最密集的表面，「突然冒出」既不安静也不优雅；增量的作用域天然精确（`fresh` 数组），是全站最容易做对的入场动效。
【方案】追加循环里给新节点挂类：

```js
const li = line.lastElementChild;   // insertAdjacentHTML 后
li?.classList.add('msg-in');
```
```css
@keyframes msg-in{from{opacity:0;translate:0 6px}}
.msg-in{animation:msg-in 220ms var(--ease-out-quart)}
```
只挂在真正追加的节点上，全量重绘路径（`render()`）不加类，避免整列表重放。reduced-motion 关闭。
【优先级】P2
【风险】低。唯一注意点：`insertAdjacentHTML` 后取 `lastElementChild` 前确认是刚追加的那条（循环体内每条都取一次即可）；去重路径（seq 已存在）不加类。

### 1.3 任务列表「新任务行」入场

【现状】任务表（hub `taskTable` app.js:1670-1680 / node `myTasksCard` app.js:1477-1479）随轮询整表重建，行无任何进出动效。提交新任务后要靠自己在表里找哪行是新的。
【问题】与 1.1 同理——「新任务出现」是高价值反馈时刻。但不能 naive 地给 `tr` 挂 CSS animation：整表重建会让全部行每 5s 重放一次，变成噪音灾难。
【方案】按任务 ID 集合差集判据，只给真正新增的行挂 one-shot 类：

```js
// hub：S.knownTaskIds = null 初始；taskTable 渲染处
const known = S.knownTaskIds;
const ids = new Set(list.map(t => t.id));
const isNew = id => known && !known.has(id);
// <tr ... class="${isNew(t.id) ? 'row-in' : ''}">
S.knownTaskIds = ids;   // 渲染即更新：下一轮同批行不再重放
```
首次渲染 `known === null` 不播动画（首屏静默）。筛选/翻页不算「新」——已见过的 ID 天然在集合里。node 端同构（以 `task_id` 为键）。CSS 用 `@keyframes row-in{from{opacity:0;translate:0 4px}}` 240ms out-quart。建议先只在 hub 总览「最近任务」（5 行小表）与任务页落地。
【优先级】P2
【风险】中低。必须保证「集合更新与渲染同事务」，漏更新会导致下轮重放；登出/会话失效时清空集合（对齐 `sessionExpired`/logout 清理点）。reduced-motion 关闭。

### 1.4 关键数字变化 bump（统计格 / 铃铛未读 / 未读浮标）

【现状】总览统计格 `.metric`（hub app.js:1184-1188）、铃铛未读 badge（`paintBell` app.js:2585-2595）、群未读浮标 `setNewMsgPill`（app.js:2800-2805）都是 textContent 直接换值，变化不可感知。
【问题】「等你确认 3 → 5」这类数字是用户最该注意到的事件，现在完全静默。
【方案】写值前比较旧值，不同才触发 one-shot bump：

```js
function setTextBump(el, txt) {
  if (!el || el.textContent === txt) return;
  const changed = el.textContent !== '';
  el.textContent = txt;
  if (!changed) return;
  el.classList.remove('bump'); void el.offsetWidth; el.classList.add('bump');
}
```
```css
@keyframes bump{35%{transform:scale(1.06)}}
.bump{animation:bump 320ms var(--ease-spring)}
```
适用点：总览四格 `.metric`、`.bell-badge`、`.newmsg-pill`。缩放原点 `transform-origin:left center`（metric）/`center`（badge）。reduced-motion 关闭。
【优先级】P2
【风险】低。注意 `.bell-badge` 是 absolute 定位元素，scale 动画安全；`hidden` 切换（badge 隐藏/出现）与 bump 分离，见 1.8。

### 1.5 状态徽标切换

【现状】`.badge` 状态变化（running→completed 等）发生在被重写的分区/整页里，无过渡（innerHTML 重建拿不到同节点 transition）。
【问题】徽标是状态真相的第一眼载体，但给它做独立动效性价比低：1.1 的分区淡入已经覆盖了抽屉内徽标切换的可见性；列表页整表重建路径做徽标级动效需要逐行 diff，投入产出不成立。
【方案】不做独立动效，靠 1.1/1.3 承载；可选低成本增补：`.badge.needs_review` 出现时的一次性 heat 光晕衰减（`@keyframes needs-glow{from{box-shadow:inset 0 0 0 1px var(--heat-ring),0 0 0 6px var(--heat-bg)}}` 600ms），只在 1.3 的 row-in 判据内挂，避免轮询重放。
【优先级】P3（可选）
【风险】低；若与 1.3 同批落地则零额外机制。

### 1.6 筛选 chip / 导航 tab 选中的按压反馈

【现状】chip 选中态切换走 `render()` 重建（如 `fchip` act，hub app.js:3130），选中黑 pill 瞬间出现；侧栏 nav 同理。现有反馈只有全局 `:active` scale(.97)，选中态本身无反馈。
【问题】选中是强操作，黑底白字的强对比切换其实已经足够「响」；此条只建议最小增补，且必须避开轮询重放。
【方案】在交互路径（ACT 处理器）render 之后给选中节点挂 one-shot 类：

```js
fchip: b => { S.f.status = ...; render();
  document.querySelector('.filters button.on')?.classList.add('just-set'); },
```
```css
@keyframes just-set{from{transform:scale(.96)}}
.filters button.just-set, aside nav button.just-set{animation:just-set 140ms var(--ease-out-quart)}
```
animation 类不主动移除也安全：animation 只在类挂上时播一次，轮询重建的节点不带此类。**不要做成「滑块指示器」**（见第七节第 2 条）。
【优先级】P2
【风险】低。注意 ACT 内多处 `render()` 后追加一行即可，不碰渲染函数本体。

### 1.7 node toast 进出过渡（双端机制对齐）

【现状】hub toast 走 `.show` 类 + 双向过渡（style.css:319-320，JS 1042-1046）；node toast 用 `hidden` 属性硬切（app.js:1860-1866，CSS :237），出现/消失都是瞬间闪现，且 `[hidden]{display:none!important}`（node css:289）会杀死任何过渡。
【问题】同一动作（复制成功、开关结果）两端反馈质感不一致；node 的闪现式 toast 在深色文字页面上尤其突兀。
【方案】node 移植 hub 的 `.show` 机制，**保留 node 黑条视觉与底部位置（白名单差异不动，只统一运动机制）**：

```css
/* node style.css .toast 改造：去 [hidden] 依赖 */
.toast{position:fixed;bottom:25px;left:50%;translate:0 10px;opacity:0;z-index:100;pointer-events:none; ...现有黑条样式; transition:translate 220ms var(--ease-out-quart),opacity 220ms var(--ease-out-quart)}
.toast.show{translate:0 0;opacity:1;transition:translate 320ms var(--ease-spring),opacity 320ms var(--ease-spring)}
```
JS 的 `el.hidden = ...` 改为 `classList.toggle('show')`（对齐 hub toast() 1042-1046 的 clearTimeout 单实例模式）。差异登记：node toast=底部黑条无玻璃，属 SPEC 六.五白名单，本改动不触碰该差异。
【优先级】P1
【风险】低。检查 node 所有 `toast` 调用无 hidden 依赖（只有 toast() 自身操作 hidden，安全）；reduced-motion 块补 `.toast{translate:0 0!important;transition:opacity 200ms ease!important}`（对齐 hub 374 的写法，注意 node translate 不承担居中，归位值是 0 0 而非 -50% 0）。

### 1.8 铃铛 badge / 新消息浮标的出现动画

【现状】`.bell-badge`（hub index.html:15，hidden 切换）与 `#group-newmsg` 浮标（hub app.js:2800-2805）出现/消失均为硬切。
【问题】从 0 → 有未读是重要时刻，但被 1.4 的 bump 部分覆盖；浮标是 absolute 定位胶囊，pop-in 收益中等。
【方案】出现时挂 one-shot `@keyframes pop-in{from{transform:scale(.6);opacity:0}}` 200ms spring；消失不做（延迟清理复杂度不值）。
【优先级】P3
【风险】低。

---

## 二、过渡与缓动体系

### 2.1 token 覆盖面盘点与结论

【现状】`--ease-spring/--ease-out-quart + --dur-enter/exit/scrim/micro/hover` 五时长两缓动（两端 :root 28-29 行），已覆盖：召唤层全套、toast、开关滑块、群卡 lift、topbar 发丝线。
【问题】覆盖缺口集中在三处：① 非召唤层内容变化无 token 化动效（第一节各条即补此缺口，建议统一复用 `--dur-hover`(180ms) + `--ease-out-quart`，**不新增 token**——五时长两缓动够用，加 token 反而破坏「不许发明中间值」的纪律）；② 一批微过渡用了裸 `ease`/`ease-out` 字面量（见 2.2）；③ hover 位移除群卡 lift 外无——这是**预算内正确状态**，不是缺口。
【方案】结论性规则写进 style.css 头注：「召唤层用 spring/enter-exit；微反馈一律 `var(--dur-hover) var(--ease-out-quart)`；不新增 token」。
【优先级】P3（口径落档）
【风险】无。

### 2.2 裸缓动字面量清算

【现状】以下位置绕过了 token 体系，用裸 `ease`/`ease-out`/裸时长：hub button 基类（css:37 `ease-out`）、nav 按钮（css:80）、bell-btn（css:387 `160ms ease`）、`.switch` 背景色（css:446 `180ms ease`）、topbar（css:59 `300ms ease`）、help-dot tooltip（css:412 `.15s ease`）；node button（css:36）、nav（css:68）、switch（css:283）。
【问题】同一「微反馈」语义两套手感；且 help-dot 用了 token 表之外的 150ms，正是「发明中间值」。
【方案】统一替换为 `var(--dur-hover) var(--ease-out-quart)`（background/opacity 类属性）与 `var(--dur-micro) ease-out`（transform 类，保持现有按压手感可不动）。纯文本替换，零行为风险。
【优先级】P3
【风险】极低；注意 hub/node 同名规则同值替换（两文件无逐字节锁，但值应同源）。

### 2.3 输入框焦点环过渡

【现状】`input/select/textarea:focus` 的 accent 环（`box-shadow:0 0 0 3px var(--accent-ring)`，hub css:150 / node css:107）瞬现瞬失。
【问题】焦点是全站最高频的状态切换，120ms 的环淡入让表单「活」一点，成本一行。
【方案】`input,select,textarea{transition:border-color 120ms ease,box-shadow 120ms ease}`（值可写 token 化的 `var(--dur-micro)`）。
【优先级】P3
【风险】极低；注意 disabled 切换不受影响。

### 2.4 骨架屏过渡——判定为不做

【现状】加载态全部是文本「…加载中」（token 列表 hub app.js:2423/2428、通知区 2519、node 1294 等）。
【问题】无；node 本机回环与 hub 同机房加载都在亚秒级，骨架屏是营销页语汇。
【方案】不做；若要一丝质感，可给「加载中」文本加 pulse-dot 同款 2.2s opacity 动画（复用现有 keyframes），一行 CSS，P3 可选。
【风险】无。

---

## 三、下拉菜单

### 3.1 现状盘点

【现状】全站下拉全部是原生 `<select>`，仅复用 input 的边框/圆角/内边距（hub css:148-151 / node css:105-108），未做任何 chrome 定制。实例清单：
- hub：任务筛选 `f.node/f.by/f.error`（app.js:1657/1661/1662）、派发两级 `ctrl-node/ctrl-agent`（2226-2227）、转任务 `task-node/task-agent`（2885）、工作单 `wa-step/wa-outcome`（2971/2982）、通知 `nf-type/nf-format`（shared block）
- node：派单 `f-target`（app.js:1434）、管家钦定 `prime-select`（849）、通知 `nf-type/nf-format`

【问题】
1. **观感**：未 `appearance:none`，箭头是 OS 原生控件样式——macOS 是系统小箭头、Windows 是经典灰箭头，与自绘的 input 圆角体系脱节；高度/内边距与同排 input 有细微不一致。
2. **信息可达性（比观感更重要）**：大量关键说明挂在 `<option title>` 上（hub `pickerNodeOptions` 2137、`pickerAgentOptions` 2174、错误筛选 1662；node `optOf` 1414）——**原生 option 的 title 在主流浏览器中根本不显示**。hub 端因有摘要行（`pickerSummaryHtml`）尚可补救；node 端派单页选目标后只有一段 `targetNote`（1430），而 option 里的「离线（任务先存着，上线就送）」后缀、权限说明在选项列表里实际不可见。
3. **optgroup**：两端都用 optgroup 分「智行管家/普通」（hub 2178-2181 / node 1419-1420），原生 optgroup 样式极弱（灰字无分隔），分区语义传达有限。
4. **键盘**：原生 select 键盘可用性是免费的（方向键/首字母跳转/Esc），这是**不做自绘组件的最强理由**（见第七节第 1 条）。

### 3.2 方案 A（推荐）：原生 select 的 chrome 精修（纯 CSS，双端同值）

```css
/* 追加在两端 input 样式附近 */
select{
  appearance:none;-webkit-appearance:none;
  background-image:url("data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='10' height='6' viewBox='0 0 10 6'%3E%3Cpath d='M1 1l4 4 4-4' fill='none' stroke='%236e6e73' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  background-repeat:no-repeat;background-position:right 13px center;
  padding-right:34px;cursor:pointer;
}
select:hover:not(:disabled){border-color:var(--ink-4)}
select:disabled{background-image:none;cursor:not-allowed}
```
要点：chevron 色写死 ink-3 `#6e6e73`（background-image 无法用 currentColor，两端同值）；padding-right 预留防文字压箭头；`cursor:pointer` 补 select 缺省（全局 button 有、select 没有）。与 2.3 的焦点过渡自动叠加。
【优先级】P1
【风险】极低。`appearance:none` 全现代浏览器支持；注意 Windows 上 `option` 下拉列表本身仍是 OS 渲染——这是接受原生控件的代价，也是收益。

### 3.3 方案 B（不推荐）：自绘 dropdown/listbox——见第七节第 1 条

### 3.4 node 端补「选择摘要行」，对齐 hub 选择组件形态

【现状】hub 两级下拉有 `pickerSummaryHtml`（app.js:2186-2199，「将在 X 上用 Y 执行」+ 管家徽标）；node 派单页只有一段 `targetNote`（1430），且 option title 信息实际不可达（3.1 问题 2）。
【问题】双端一致三层②：同类功能的交互结构应同构；node 用户选执行者时拿不到 hub 级别的确认信息。
【方案】node 选目标后渲染与 hub 同构的摘要行（名字/描述/权限/在线态徽标已有雏形，补齐为独立 `row` 行并放管家徽标 `.badge orch`——node 端目前无 orch 徽标样式，需同步 hub css:134 的定义）。这是信息架构修复顺带视觉对齐。
【优先级】P2
【风险】低；涉及双端同义词表（「智行管家」文案已有词典）。

### 3.5 optgroup 与长文案

【现状/问题】optgroup 样式弱 + option 文案冗长（node `optOf` 把离线说明塞进 option 文本）。
【方案】optgroup 不可样式化（`label` 属性之外无 hook），接受原生；文案层可做减法：option 内只留「名字 +（离线）」短后缀，完整说明移到 3.4 的摘要行/悬停 tooltip（摘要行已承担）。hub 端文案已收敛，node 对齐即可。
【优先级】P3
【风险】低。

---

## 四、按钮体系

### 4.1 loading 态标准化（busy 助手）——本节最高优先级

【现状】全站唯一的 spinner 是 hub 部署包下载按钮（`.spin` css:298 + `enrollSetDlBtn` app.js:1478-1483）；其余 **14+ 处** busy 全是「disabled + textContent 换文案」手工模式：hub 的 notify-save（2556）、notify-test（3319）、notify-del-confirm（3336）、token-revoke-confirm（3373）、task-cancel-confirm（3487）、group-create（3398）、group-close-confirm（3459）、task-from-msg-submit（3424）、artifact 取回（3562-3584）、doLogin（1138）、派发提交（2103）；node 的 cfgSave（906）、cfgRestart（951）、primeDeclare（1621）、notify-save（1329）等。node 端甚至没有 `.spin` 样式。
【问题】① 文案变短/变长导致**按钮宽度跳变**（「确认取消任务」→「取消中…」收缩，pill 布局抖动）；② 无 spinner，用户对「点了有没有生效」感知弱（INTERACTION 四的 300ms 反馈纪律只有 disable 一半）；③ 14 处手工样板代码，恢复文案全靠 each 处手写，**已有一处真 bug**：hub `doLogin` 失败恢复把按钮文案写成 `'进入 Hub 中心端 →'`（app.js:1167），而该按钮原文是「用令牌登录」（1092）——失败一次按钮文案就永久漂移。
【方案】双端各加一对助手 + 一个类：

```js
function busyBtn(btn, label) {
  if (!btn) return;
  btn.dataset.label = btn.textContent;
  btn.style.minWidth = btn.offsetWidth + 'px';   // 锁宽防抖
  btn.disabled = true; btn.classList.add('loading');
  btn.innerHTML = '<span class="spin"></span>' + esc(label);
}
function idleBtn(btn) {
  if (!btn) return;
  btn.disabled = false; btn.classList.remove('loading');
  btn.textContent = btn.dataset.label || btn.textContent;
  btn.style.minWidth = '';
}
```
```css
button.loading{opacity:1;pointer-events:none}   /* loading 不吃 disabled 的 .48 灰：转圈即反馈 */
```
node style.css 需同步 hub 的 `.spin` 定义（css:298-299，双端同值）。逐步替换 14 处样板；`doLogin` 的文案 bug 随助手自然修复（恢复取 `dataset.label`）。异步路径已在 `finally` 恢复的写法不变，只换函数。
【优先级】P1
【风险】低中。注意两处非标准恢复：`artifact`/`notify-test` 失败恢复文案是固定原文（助手兼容）；hub 向导下载按钮已有 `enrollSetDlBtn` 可保留（它就是助手雏形，可收编为同一实现）。

### 4.2 node 危险按钮形态漂移（双端一致硬规项）

【现状】node 删除类按钮用 `'off-btn'` 类（app.js:1109「删除此 agent」、1315「删除通知渠道」），但 node style.css 中 `.off-btn` 只有 `.toggle .off-btn.selected` 一条规则（css:255）——**脱离 toggle 的裸 off-btn 无任何样式，呈现为默认灰 pill**。hub 同类动作是红描边 danger（`.danger` hub css:44-45，删除渠道/撤销令牌/取消任务全用）。
【问题】双端一致三层③文案层+②操作逻辑层：同为「删除/不可逆」动作，hub 红描边、node 灰钮，危险信号在 node 端缺失。这实质是**样式类引用漂移**（疑似从 toggle 语境误用到独立按钮）。
【方案】二选一（推荐 a）：a) node style.css 增加 `.danger` 定义（与 hub 44-45 行逐字同值），三处调用点改 `'danger'`；b) 若有意保留灰钮弱化（防止能力配置页满屏红），则在 node css 显式登记 `.off-btn{...}` 弱化样式并注释与 hub danger 的差异理由——但不能维持现在的「未定义类」状态。
另：node 8 处 `window.confirm`（删渠道 1389、cfg-del 1907、cfg-reload 1931、cfg-restart 1935、连接保存 1569、凭证轮换 1592、钦定管家 1619、断开组网 1647）vs hub 全部 slim modal 二次确认（撤销令牌 3368、删除渠道 3331、取消任务 3480 等）——同类破坏性确认，hub 是弹层、node 是浏览器原生框，双端一致三层②的直接违例面。建议 node 复用 `openNotifyForm` 的 modal 壳基建（已有 role=dialog/焦点移入，1349-1356）落一个 `confirmModal(title, body, act)` 小助手。
【优先级】danger 形态 P1；confirm→modal P2（改动面大，可分批）
【风险】danger 改色零风险；confirm 替换要注意 node `#layer` 直清家族无出场动画（白名单豁免在案，node style.css:1731 注释），modal 关闭是硬切——可接受，与 hub closing 动画的差异已在白名单内。

### 4.3 图标按钮归一 `.icon-btn`

【现状】铃铛按钮手写全套（hub css:387-394，含著名的 `padding:0` 显式重置注释——全局 `button{padding:0 16px}` 吃掉 34px 圆钮内容区的坑）；关闭钮 `.close` 是「× 文本按钮」（hub css:306 / node css:234）。
【问题】图标按钮的几何规则散落，下一个图标按钮还会踩 padding 坑。
【方案】提 `.icon-btn` 基类（34×34、`display:grid;place-items:center;padding:0;border-radius:var(--r-pill)`），bell-btn/未来的图标钮迁移；`.close` 文本钮保持（× 是字面字符不是图标，现状可用）。
【优先级】P2
【风险】低；bell-btn 迁移后回归 t20261026 的几何断言（svg 中心偏差 <1px）。

### 4.4 尺寸变体收敛

【现状】按钮字号/内边距现存 6 档 ad hoc 覆盖：默认 34px；`.table-action button`（12.5px / 4-10，hub css:170 / node 121）；`.chan-actions button` **hub 12px（css:433）vs node 12.5px（css:281）——双端漂移**；`.page-jump button`（28px 高，hub css:406）；`.os-tabs`（6-16 / 12.5px，hub css:284-285）；`.filters button`（6-14 / 12.5px，css:129）。
【问题】没有名义上的「小按钮」档，每处手写一套值；hub/node 同位不同值。
【方案】定两档并落档名：`button` 默认（34px / 14px）+ `.btn-sm`（height:28px;padding:0 12px;font-size:12.5px）。`.table-action`、`.chan-actions`（hub 提到 12.5px 对齐 node）、`.page-jump`、`.os-tabs` 逐步收编到 `.btn-sm` 或保留既有覆盖但在注释里声明档位归属。**字号全部 ≥12.5px，符合表格字号定案的精神（虽非表格，向同一地板对齐）**。
【优先级】P2
【风险】低；hub chan-actions 12→12.5px 是可见变化，走查确认卡片不换行（chan-actions 已 flex-wrap，安全）。

### 4.5 `.warning` 类的冗余说明

【现状】hub `.danger,.warning` 共享同一红描边形态（css:44-45），唯一用点是人工核对「标记为失败」按钮（app.js:1950）；node 无 `.warning`。
【方案】不动视觉（红色只表达「失败/不可逆」的语义拆色定案正确）；建议注释标明 `.warning` 是 `.danger` 的语义别名（「失败向的危险动作」），防止未来有人给它发明第二种颜色。
【优先级】P3
【风险】无。

---

## 五、等宽与数字排版

### 5.1 现状

【已用 `tabular-nums`】hub：`#session-left`（css:69）、`.navnum`（83）、`.metric`（97）、`td` 全列（165）、`.mini-stat b`（337）；node：`.metric`（79）、`td`（115）、`.mini-stat b`（245）、`.ledger-line b`（249）；两端 md 表格 td（shared block）。
【已用 `--mono`】code/pre、`td small`（hub css:169 / node 119——任务 ID 在表格里已是 mono）、`.md-lang`、`.md-log-label`、`.readonly` 无（纯文字无需）。
【问题】覆盖是「点状」的：表格内很好，**表格外的动态数字（徽标计数、时间戳、分页文案、等待秒数）全是比例数字**——5s 轮询下数字宽度跳动会让相邻文字位移，时钟列不对齐，这是工具界面最隐蔽的「廉价感」来源。

### 5.2 系统性方案（推荐）：全局 tabular-nums 一行落地

【方案】两端 `body` 加一条：

```css
body{...font-variant-numeric:tabular-nums}   /* 工具界面：数字一律等宽， */
```
理由：① 表格 `td` 已全局 tabular（两端现行做法），证明该口径在本产品成立；② 中文正文零影响，拉丁数字纹理略「机械」恰是工具语汇（Apple 工具页同口径）；③ 一行 CSS 消灭下文 5.4 清单的全部逐点补丁与未来新增面的遗忘风险。
【优先级】P1
【风险】极低。唯一可见变化是正文句中数字（如「保留 24 小时」）变等宽——走查一遍即可。若个别位置想回比例（如大号 metric 想更「展示」），局部 `font-variant-numeric:proportional-nums` 覆盖，但目前 `.metric` 用 tabular 观感正确，无需。

### 5.3 mono 清单（该用没用 `--mono` 的位置）

【方案】逐条补 `font-family:var(--mono)`：
- `.artifact small`（两端 css 均无）——产物行的 sha256 前缀与字节数是典型技术态，现用正文比例字（hub app.js:1930 / node 1844）；
- node `f.idem`/恢复密钥行内 `<code>` 已 mono（正确）；hub 同（正确）。
其余该 mono 的（任务号、令牌、yaml 片段、追踪码）现状均已 mono，无需扩。**mono 不建议全局化**——mono 只给「可复制的技术字符串」，句中中文夹 mono 的离析感已有 0.92em 规约处理，全局化会破坏正文。
【优先级】P2（随 5.4 同批）
【风险】低；`.artifact small` 变 mono 后字宽变大，行内可能换行点变化，走查产物行。

### 5.4 tabular 补丁清单（若不采纳 5.2 的全局方案，则逐条打 here；采纳则跳过）

| 端 | 选择器/位置 | 内容 |
|---|---|---|
| hub | `.bell-badge`（css:401） | 未读计数 1→10→99+ 宽度跳 |
| hub | `.ni-time`（css:422） | 通知时间列 |
| hub | `.chan-meta`（css:431） | 创建时间 |
| hub | `pagerHtml` 输出行（app.js:1634，现 inline style） | 「共 N 条 · 第 X/Y 页」——建议顺带给该行加 class 去掉 inline style |
| hub | `.group-card .row small`（app.js:2665） | 「N 条消息」 |
| hub | `.msg-meta`（css:356） | `fmtFull` 时间戳，聊天列对齐 |
| hub | 总览「登录剩余时间」`b`（app.js:1194） | `humanLeft()` 每分钟变宽 |
| hub | 向导 Step4 等待秒数 small（app.js:1565） | 「已等待 N 秒」逐秒跳 |
| hub | nav 内任务数 `<small>(nr)</small>`（app.js:976） | `.navnum` 有、它没有 |
| hub | `.filters button`（css:129） | chip 计数（全部 (N)） |
| node | `.badge` 基类（css:88） | conn-chip「重连 N 次」（render 746） |
| node | svcCard 今日执行 `b`（app.js:1133） | 计数 |
| node | myTasksCard footer（app.js:1483） | 「已载 X / 共 Y 条」 |
| node | `tasks-more` 按钮文案（app.js:1482） | 「还有 N 条」 |
| node | `.chan-meta`（css:282） | 创建时间 |
【优先级】P1（若走全局则就是 5.2 本身）
【风险】逐条方案无风险；注意 5.4 表中 hub `.filters button` 与 1.6 的 just-set 动画互不冲突。

### 5.5 顺带提示（不在本轮范围）

node 列表时间用 `toLocaleTimeString` 本地时区（app.js:706-715），hub 用 UTC 切片（app.js:1037）——时间口径统一是既定的 C4 迁移项，本提案不触碰；但 5.2 落地后两端时间列至少宽度行为一致了。

---

## 六、实施顺序建议（按优先级汇总）

**P1（建议一批落地，多为纯 CSS/小 JS，风险低收益确定）**
1. 5.2 全局 `tabular-nums`（两端各一行）
2. 3.2 select chrome 精修（纯 CSS 双端同值）
3. 1.7 node toast `.show` 机制移植（消除最刺眼的动效缺失）
4. 1.1 抽屉分区淡入（shared block 双端同改，D22 纪律内）
5. 4.1 busyBtn/idleBtn 助手 + node `.spin` 补齐（顺带修 doLogin 文案 bug）
6. 4.2a node `.danger` 定义与三处调用点改类

**P2（第二批，需要少量状态/判据设计）**
7. 4.2b node `confirm()` → slim modal（分批：先删渠道/cfg 删两个高危）
8. 1.2 群消息入场动效；9. 1.3 任务新行入场（先总览试点）；10. 1.4 数字 bump；11. 1.6 chip 选中反馈
12. 3.4 node 摘要行对齐 hub；13. 4.3 `.icon-btn`；14. 4.4 `.btn-sm` 尺寸归一（含 hub chan-actions 12→12.5px）；15. 5.3 `.artifact small` mono

**P3（清扫与锦上添花，可搭车任意批次）**
16. 2.2 裸缓动字面量清算；17. 2.3 焦点环过渡；18. 1.5/1.8 徽标光晕与 pop-in；19. 4.5 `.warning` 别名注释；20. 2.1 口径注释落档
21. 附带清理：hub `.switcher`（css:66）已无任何 JS 引用（已 grep 验证），建议删除死 CSS

**验收纪律**：所有动效项走查时必须各验三态——正常播放、`prefers-reduced-motion` 降级、轮询 5s 下不重放；shared block 涉改项跑 `go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿 + diff 两端标记块。

---

## 七、不建议做的事（防过度设计）

1. **自绘 dropdown/listbox 组件**。原生 select 的键盘/读屏/移动端体验免费且正确；自绘要补 ARIA combobox 全套（aria-expanded/activedescendant/Typeahead）、焦点管理，且在「整页 innerHTML 重建」架构里每次轮询都要处理下拉态恢复——投入大、引入可访问性回归面、违反动效克制。chrome 精修（3.2）已解决 90% 观感问题。
2. **导航/筛选的滑块指示器（sliding pill）**。白 pill 选中态随 innerHTML 重建，滑块要么用 absolute 元素跨重建保持（需 measuring + resize 监听 + 轮询后重定位），要么每轮重放——两者都是为表演引入常驻 JS 复杂度。现选中态对比度已足够强。
3. **骨架屏 shimmer**。加载源是本机回环/同机房 API，亚秒级；shimmer 是营销页语汇，「文本加载中 + 可选 pulse」已够（2.4）。
4. **数字滚动 count-up / odometer 动画**。5s 轮询下统计数字变化频繁，count-up 会让页面永远有东西在动，直接违反「动效是反馈不是表演」。bump（1.4）已是上限。
5. **表格行 FLIP 重排动画**。任务列表按 created desc 排序，新任务插入会使全部行位移；FLIP 需要首末位置快照 + 每轮轮询比对，基础设施成本高且「所有行滑动让位」在工具表里是噪音。行级一次性入场（1.3）足够。
6. **details/summary 展开高度动画**。D22 分区机制按 innerHTML 重建分区，details 开合态靠回放保持；高度动画要么与回放冲突要么需要 grid-template-rows hack 逐分区调试，收益（200ms 的展开过程）远小于回归风险。
7. **路由切换整页过渡（View Transitions API）**。hash 路由切换应即时；跨页淡入会让「点侧栏 → 内容切换」的可预期性变差，且与 window.scrollTo(0,0) 的时序耦合。工具界面跳转不是页面转场表演。
8. **hover lift / hover 位移扩编**。群卡 lift 是 C 动效预算钦定的全站唯一 lift（hub css:344-349 注释在案）；统计格已明确「hover 用底色不用 lift，格子抬升会撕裂面板」（css:89-91 注释在案）。不再新增任何 lift 面。
9. **语义色扩编**。FINAL 金框（css:455）是特批孤例，不构成先例；任何「再来一个颜色」的提议（如 info 蓝、success 绿横幅）都在四色预算外，一律回绝。
10. **成功庆祝动效**（打勾描画、confetti、微弹跳祝贺）。任务完成是常态不是里程碑；toast + 分区淡入已传达。
11. **玻璃面扩编**。notify-panel 曾因 glass 取样失败翻车回实底（hub css:380-386 注释在案）——玻璃只有 topbar/toast 两个真重叠位置成立有技术依据，非纯审美决定，不动。
12. **深色模式**。范围外的大工程（六灰色阶 + 四语义色全部重配，`.msg-final` 金框等孤例色重估），与「显示效果打磨」不是一个量级的任务，单独立项。

---

*提案完。所有行号基于 feat/frontend-polish @ 043f9e3；落地前请以当时 HEAD 复核行号。*
