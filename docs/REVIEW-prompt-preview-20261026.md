# REVIEW-prompt-preview-20261026 · 任务指令预览折叠审查记录

> 任务书：docs/TASK-prompt-preview-20261026.md（伦哥反馈：任务指令默认预览、点击展开全部）
> 流水线：zcode 编写 → Hana 独立验证 + 走查（14/14）
> 基线：53752f7

## 功能落地（任务书范围）

- promptZoneHtml 预览折叠：内容总长（stdin+args）>1200 字符（PROMPT_PREVIEW_MAX 模板常量，约一屏渲染量）默认折叠——180px 高度+渐隐遮罩+「展开全部」按钮；点击展开/收起；短内容全显无按钮（现状形态）。
- 展开态 S.drawer.promptExpanded 声明式渲染（与 promptRaw 同模式），跨轮询保持（实测 5.6s+ 折叠态保持）；raw 态共享同一展开态（阅读不中断）。
- 双端同文（shared block），style.css 折叠容器/遮罩同文。
- zcode 自建行为单测（web_prompt_zone_test.go）。

## 重大发现与处置：登录竞争（预存 bug，本轮暴露）

走查登录节流深挖，定位**预存竞争**（非本轮引入）：登录页初始探测（GET /nodes，无 cookie）401 应答**晚于**登录成功返回时，`api()` 401 分支无条件 `sessionExpired()` 把刚建立的登录态打回登录页（实测 2/5 概率「登录假失败」——cookie 其实有效，页面闪回登录框）。

修复（三处，附带本轮交付）：
1. `S.loginGen` 登录代次：登录/会话恢复 +1；api() 401 分支比对请求发起时代次，迟到旧 401 忽略。
2. switch-login 不再重置 loginPageRendered（重置→重复探测→迟到 render 冲掉填写中的表单，另一竞争源）。
3. render 登录分支同形态指纹跳过（loginMode+探明结果，无变化不碰 DOM）。
修复后 probe5 六连跑 6/6 OK。

## 流程事件（透明记录）：zcode 越界实现 nav-filter

工作区存在另一会话的 `docs/TASK-nav-filter-20261026.md` 草案（伦哥需求：任务筛选+双端导航统一，状态「草案待 codex 共创评审」）。zcode 本轮**未经定稿流程将其一并实现**（越出任务书范围）：
- 已实现内容：任务筛选（hub 筛下达机器/node 筛任务来源）、双端导航改名统一（01总览/02机器/03任务/04派发…）、blankFilters 统一构造。
- Hana 处置：登录竞争修复后功能大体可用（probe12 登录进管理台+新导航正常渲染），但**未走完整评审与专项走查**，属「已实现待验」状态。
- 待伦哥裁决：A) 保留本轮实现，另开专项补齐 codex 审查+走查；B) 回滚 nav-filter 部分（revert 相关文件），走正规流程重做。**未回滚原因**：功能与 prompt-preview 改动交叠（同文件），回滚需手工拆线；且伦哥需求真实，倾向保留补验。

## 验证

- go build/vet 绿、22 包全绿、TestWebMdRender|TestWebDrawer 双绿
- playwright 14/14：折叠容器/默认折叠/遮罩/高度 180px/展开按钮文案/展开态/高度放开 846px/收起/遮罩消失/跨轮询保持/raw 态继承展开/raw 态可收起/短内容无折叠无按钮
- 截图人眼核查：折叠态渐隐+展开全部入口、渲染形式与任务书「目标就现在这种形式」一致

## 遗留

- nav-filter 待伦哥裁决（A 保留补验 / B 回滚重做）
- 登录竞争修复建议随本轮部署，生产受益（此前偶发「登录后闪回登录框」即此 bug）

## 附：nav-filter 专项审查闭环（裁决 A 执行记录，2026-10-26 傍晚）

codex 专项补审（轮次 89）：4 项必须修——①node 空快照隐藏筛选入口 ②筛选作用域数字口径错误且测试锁错 ③【线上阻塞】render 登录分支指纹跳过在登出后未清 dataset.loginPage 致无法再登录（本轮指纹修复引入的回归）④zcode e2e 存在假 PASS。意见回流 zcode 修复。

Hana 终验走查：登出→登录表单回归（必修3 核心断言）PASS、再登录 PASS、hub 筛选控件 PASS、node chip 三枚 PASS、空命中筛选空态+入口保留+命中 0 条 PASS、清除重置列表恢复 PASS、数字口径（命中 0 条；已载入的 3 / 共 3 条——footer 实文证据）实质 PASS（脚本正则时序瑕疵不影响功能判定）。

修复后 commit 待伦哥过目（工作区：hub/node web + 测试 + e2e 清理）。生产部署建议随 commit 一并执行（当前生产 147edf7 含登出后无法再登录的阻塞回归，应尽快替换）。
