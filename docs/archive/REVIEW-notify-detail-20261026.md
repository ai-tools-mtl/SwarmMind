# REVIEW：通知面板三细节修复（2026-10-26 午后）

伦哥生产实拍三反馈，诊断、修复、验证全程 Hana 独立完成，commit `217632d`（仅两端 style.css，+14/-4）。

## 三问题与根因

| # | 现象 | 根因（生产几何实测定位） | 修法 |
|---|------|------------------------|------|
| 1 | 侧栏「SwarmMind 中心端」「端」字孤行 | 218px 侧栏经 aside 17px×2 + workspace 13px×2 三重 padding，文本区仅 125px；14px 加粗标题需 128px，差 3px | 字号收 13px + `white-space:nowrap`；node 端同规加 nowrap 防长 nodeID 掉行 |
| 2 | 空态面板与页面内容叠印 | `backdrop-filter` 取样以最近带 blur 祖先为 backdrop root：面板嵌玻璃 `.topbar` 内，blur 取不到 topbar 之后的页面背景，实际完全透明 | 面板收实底 `rgba(245,245,247,.95)` 并去 blur；`prefers-reduced-transparency` 白底降级规则保留（不冲突） |
| 3 | 面板底部横向拖动条 | `white-space` 继承自 `.top-right`（nowrap 防顶栏折行），footer-note 被顶成单行 381px，撑出 `scrollWidth 395 > clientWidth 378` | 面板显式回置 `white-space:normal` |

诊断手法备忘：`elementFromPoint` 扫面板区域 + `scrollWidth/clientWidth` 逐元素排查定位元凶；问题 3 的 17px 溢出量与 footer-note 单行宽 381-350 完全吻合，问题 1 的 3px 缺口由 range rects（文本 114+14 两段）证实。

## 验收（本地 dev hub + Playwright，三层走查）

- 功能断言 5/5：单行 rect（logo 24 + 文本 16 高单条）/ 实底 .95 无 blur / 面板零横向溢出（scrollWidth==clientWidth==378）/ 注入双条目渲染正常（标题单 rect、语义色图标）/ 已读 POST 请求通路。
- 截图人眼核查：空态不再叠印、有内容态横条消失、footer-note 正常折行、侧栏单行。
- 回归：`go build ./...`、`go vet`、hub+node 全部包测试绿；本轮仅 CSS，未触碰 JS 行为。
- 未验项：窄视口（≤680px）媒体查询形态未实测（改动不影响该分支几何）；生产部署后建议伦哥刷新强缓（CSS 是 embed 静态资源，URL 不变，需 Cmd+Shift+R）。

## 隔离说明

工作区同期存在并行任务（prompt-args-md 任务指令 Markdown 渲染）的未提交改动（两端 app.js、web_drawer_sync_test.go、web_prompt_zone_test.go 等），本次提交严格只含两端 style.css，未混入。

## 待办

- 生产部署（云服务器 hub 重编译 + Mac node 控制台二进制替换，node 侧注意 Text file busy：stop→cp→start）——待伦哥过目本报告后执行。
- 飞书流水线日志同步（本轮会话 shell 审批收紧，lark-cli 写入未完成）。
