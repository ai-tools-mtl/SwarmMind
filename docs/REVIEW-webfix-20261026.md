# REVIEW-webfix-20261026 · 管理台四处体验修复审查记录

> 任务书：docs/TASK-webfix-20261026.md（伦哥 2026-10-26 晨截图反馈四项）
> 流水线：Hana 编写（zcode 三形态被当前会话权限拦截，按「判定不可用即降级」由 Hana 直接编写并注明）→ codex 审查 → 修复 → playwright 三层走查
> 分支：dev/p5-work-resume（未提交，提交按钮在伦哥手里）

## 改动面

6 文件 +109/-56：
- internal/hub/web/app.js：任务指令分区（DRAWER_ZONES 双端同文 +prompt）、promptZoneHtml、prompt-raw 动作、设置页重排（账户→通知→令牌 + 页内导航 jump-to）、飞书渠道文案、INBOX_SVG 图标
- internal/hub/web/index.html：铃铛 emoji → 内联 SVG
- internal/hub/web/style.css：bell-btn SVG 适配、ni-svg 语义色、page-jump、card scroll-margin-top、cmd-args code 胶囊、ledger-line（node 侧在 node css）
- internal/node/web/app.js：账本计数条（ledger-line 替换诊断折叠）、DRAWER_ZONES 同文行、已完成→已结束
- internal/node/web/style.css：ledger-line 样式
- internal/node/localapi/web_spacing_test.go：契约更新（details 折叠断言 → 计数条断言，旧形态进 banned 清单）

## codex 审查意见与裁决（2026-10-26）

| # | 级别 | 意见 | 裁决 |
|---|------|------|------|
| 1 | 必须修 | app.js:1615 args.join(' ') 丢参数边界（["a b","c"] 与 ["a","b c"] 同形），任务书要求逐参 code | **采纳**。改逐参数 `<code>` 胶囊（cmd-args 样式），实测 ["--filter","a b c","plain","d  e"] 边界完整保留 |
| 2 | 建议修 | scrollIntoView({block:'start'}) 未补 sticky 顶栏，长页目标标题被遮 | **采纳**。.card 加 scroll-margin-top:70px（顶栏 62px 高 + 8px 余量），实测卡顶距顶栏底 447px（长页滚动到底场景也不遮） |
| 3 | 建议修 | node 计数条「已完成」误导：finished 含失败/取消/待确认，非成功数 | **采纳**。改「已结束」，测试断言同步。语义准确优先 |

三条全采纳，零驳回。修复后复查（verify-fix.js）：滚动补偿 PASS、逐参边界 PASS、已结束标签 PASS。

## 三层走查结果（walkthrough.js + 人眼截图核查）

1. 功能断言 23/23 PASS（设置页顺序/锚点/hash 无污染/飞书文案/站内渠道/任务指令 md+raw+跨轮询/args 形态/铃铛 SVG+badge+panel/node 计数条/窄视口 1276）
2. 整页截图人眼核查：设置页、任务抽屉 md 态、node 总览——全部过检（shots 01/05/10）
3. 弹层深层交互态：渠道弹层飞书文案、抽屉原文切换跨轮询保持、铃铛面板展开、窄视口组合态——全部过检（shots 03/06/09/11）

## 验证矩阵

- go build ./... ✅ / go vet ./... ✅ / go test ./... 21 包全绿 ✅
- TestWebMdRender|TestWebDrawer 双绿 ✅（shared block 逐字节锁）
- TestWebOverviewSpacing 契约更新后绿 ✅
- node --check 双 app.js ✅

## 本轮新坑（已沉淀 CLAUDE.md/AGENTS.md）

embed 资产（web/*.js/css/html）改动后必须 `go build` 重建二进制再实测——重启旧二进制等于实测旧代码，复查结论全部作废（本轮实测踩中：args code 修复复查首跑在旧资产上假 FAIL）。

## 遗留

- node 端任务指令分区：Ledger 数据源无输入投影，暂不展示（DRAWER_ZONES 已含 prompt 槽位，未来数据源补齐即可点亮）；SPEC 六.五白名单口径不变。
- 任务提交测试数据（临时实例）已随 /tmp/webfix-hub 清理。
