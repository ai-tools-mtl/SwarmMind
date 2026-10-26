# TASK-webfix-20261026 · 管理台四处体验修复（伦哥 2026-10-26 晨反馈）

> 来源：伦哥实测截图五张，逐条反馈。分支 dev/p5-work-resume，工作区干净基线上开工。
> 验收纪律：全绿才交付，三项走查（功能断言 / 整页截图 / 弹层深层交互态截图）缺一不可。

## 背景与事实依据

- 飞书机器人两种形态（官方帮助中心 2026-10-16 版核实）：「自定义机器人」在**群组内**创建（电脑端：群聊右上角「···」→ 设置 → 群机器人 → 添加机器人 → 自定义机器人；手机端添加不了），提供 webhook；「应用机器人」才是在开放平台开发者后台创建的。现有 feishu 渠道文案方向正确但路径不精确，易把人引去开发者后台。
- hub 任务记录含完整 `input.{args, stdin, env}`（`internal/protocol/types.go`，控制台派单走 stdin 语义）；原始提示词就在 `t.input.stdin`，但抽屉里埋在底部「任务输入（原样 JSON）」折叠区且是 raw JSON 形态，等于看不见。
- node 端 Ledger 数据源**没有** stdin/Input 字段（`internal/node/ledger/` 无此投影）→ node 端不展示任务指令（如实，不造假数据）；本任务不动 node 数据源。

## 改动一：node 端总览「诊断：账本计数」展示形式更换

文件：`internal/node/web/app.js`（overviewPage，约 597 行）、`internal/node/web/style.css`。

- 现状：「重启恢复」卡片内 `<details>` 折叠 + 三个裸大数字（0/0/54）配英文小标签 accepted/started/finished。中文界面夹英文标签、数字无上下文、折叠交互多余。
- 改为：去掉 details 折叠，卡片底部一行紧凑计数条，中文标签，如：
  `账本 · 受理 0 · 进行中 0 · 已完成 54`
  muted 小字、数字加粗 `font-variant-numeric: tabular-nums`。零值不做告警色（count=0 退灰原则）。
- `mini-stat` 类若仅此一处使用可移除定义；先 grep 确认无他处引用再删。

## 改动二：hub 任务抽屉新增「任务指令」分区（原始提示词可见）

文件：`internal/hub/web/app.js`（drawerZones 约 1553 行起、drawerShellHtml 分区注册、syncDrawerZones）。

- 新分区「任务指令」放在 intro 之后、fail 之前。次序变为：介绍条 → **任务指令** → 失败原因 → 最终答复 → 诊断日志 → 产物 → 任务输入（原样 JSON）→ 时间线 → 人工核对 → 归档/下一步。
- 内容逻辑：
  - `t.input.stdin` 非空：标题「任务指令」，正文默认 Markdown 渲染 + 「查看原文」切换（复用 failInner 的 raw 切换模式，切换态存 `S.drawer.promptRaw`，分区局部更新纪律不变）。
  - stdin 空、`args` 非空：显示命令行形态（args 逐个 `<code>`）。
  - 都空：分区整体省略。
- 底部「任务输入（原样 JSON）」details **保留不动**（完整技术细节：args/stdin/env）。
- 分区须注册进 drawerShellHtml 的稳定分区清单（data-zone），保持 D22 分区缓存 diff 机制。
- node 端**不加**此分区（数据源无 stdin）；shared block 不动。若 `web_drawer_sync_test.go` 锁分区清单，同步更新测试预期并在测试注释注明「hub 独有分区（任务输入数据 hub 独有）」。

## 改动三：顶栏铃铛与通知条目图标 SVG 重设计

文件：`internal/hub/web/index.html`（约 22 行）、`internal/hub/web/app.js`（INBOX_ICONS 约 382 行、notify panel 渲染约 2264-2276 行）、`internal/hub/web/style.css`（.bell-btn 约 360 行）。

- 现状：emoji 🔔 彩色渲染依赖系统字体，与克制线性设计语言不符；panel 条目 ✅❌⚠️🚫 同为 emoji，跨平台渲染不一致。
- 改为：内联 SVG 描边图标，`stroke="currentColor"`、`fill="none"`、stroke-width 1.6：
  - 铃铛按钮：18px 铃铛 SVG（index.html 内联），颜色随按钮 currentColor；badge 未读数字机制保持（hidden 逻辑不动，0 值不显示）。
  - panel 条目图标：statusIcon(status) 函数返回 SVG（completed=对勾圆、failed=叉圆、needs_review=三角叹号、cancelled=禁止圆、test=小铃铛），16px，语义色仅描边（成功绿/失败红/警示橙/禁灰），面板底色不变。
  - 删除 INBOX_ICONS emoji 词典，替换为 SVG 映射。
- CSS：.bell-btn 内 svg 尺寸对齐、去掉 font-size 依赖；.ni-icon svg 对齐。

## 改动四：设置页重排 + 飞书渠道文案修正

文件：`internal/hub/web/app.js`（tokensPage 约 2045 行起、NOTIFY_TYPES 约 377 行）。

- 区块顺序改为：账户信息 → **通知** → 访问令牌（新令牌回执卡保持在令牌区之前）。
- 页头（head 之后）加一行页内导航：「账户信息 · 通知 · 访问令牌」，点击 scrollIntoView 平滑滚动到对应 section（用 button data-act 实现，**禁止 hash 锚点**——hash 路由是 #/xxx 形态，会撞路由）。
- D2 局部刷新守卫（`S.page !== 'tokens'` 不碰 DOM）与 #notify-section 机制**不得破坏**；重排后回归验证通知渠道列表加载、添加/编辑弹层、删除确认均正常。
- NOTIFY_TYPES.feishu.form 改为：
  「路径：电脑端进群 → 右上角「···」→ 设置 → 群机器人 → 添加机器人 → 自定义机器人（手机端加不了）。创建后把 Webhook 地址贴进来——这是群内创建的机器人，不需要去开放平台开发者后台建应用。」
- 站内通知（inbox）表单提示里「顶栏的铃铛」指路不变。

## 已知陷阱（审查与自查对照）

1. 长驻交互层局部刷新禁止整层 innerHTML 重建（CLAUDE.md 抽屉 D22 条）：抽屉新分区必须走分区 diff 机制，禁止整层重绘。
2. 双端展示漂移禁令：shared block 逐字节锁；本任务 shared block 不动，node 端 app.js 仅改总览计数条（不涉 shared block）。自查：`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿。
3. hash 路由冲突：页内跳转用 scrollIntoView，禁止 `href="#sec-xxx"`。
4. 0 值徽章假告警：bell-badge 与新计数条零值退灰/隐藏逻辑不得回退。
5. details 开合/原文切换态在轮询重绘中必须保持（分区回放机制）。
6. 工人不执行 git commit。

## DoD 验证矩阵（逐项列出结果，缺一不交付）

- [ ] `go build ./...` 三平台构建绿（darwin/amd64+arm64、linux/amd64、windows/amd64 至少主平台 darwin 全绿）
- [ ] `go vet ./...` 绿
- [ ] `go test ./...` 全绿（重点：internal/hub/web、internal/node/localapi、internal/node/web 相关包）
- [ ] `go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿
- [ ] browser 实测走查：hub 设置页新区块顺序 + 页内导航跳转 + 通知渠道增删改弹层（含飞书新文案）截图
- [ ] browser 实测走查：hub 任务抽屉「任务指令」分区（md 态 / 原文态 / args 态 / 空省略态）截图
- [ ] browser 实测走查：铃铛按钮 + panel 展开态（含条目图标、badge）截图
- [ ] browser 实测走查：node 总览页计数条新形态截图
- [ ] 改动文件清单 + 每条 DoD 结果在最终输出列出
