# TASK-agent-session-web（T3：双端会话界面）

> 分支：feat/agent-session（基于 T2 @ 570bd10）。
> 上游：docs/VISION-persistent-agent-collaboration.md 实施顺序第 4 步「日常入口」的前端部分；契约面已交付（SPEC 六.五五端点、六值轮状态机、幂等键）。
> 侦察基础：双端 web 结构、弹层基建、任务列表/派发页模式、E2E 基建均已实测在案（2026-10-29 侦察报告）。

## 范围与形态

### hub 端（新增导航「会话」，navnum 顺延）

1. **sessionsPage 列表页**（参照 tasksPage 模式，app.js:1911）：
   - 状态 chip 筛选行：全部 + 六态徽标 chip（zero 退灰）
   - 列表 `.table-wrap`：会话（agent 名 + 平台 ID 短形式）、目标机器、绑定（adapter/权限模式）、状态徽标、最近活动时间、操作（继续 / 查看 →）
   - 行 `data-session` + tabindex + aria-label；列表槽位局部更新挂点（pollTick 指纹门控沿用）
2. **新建/继续表单**（参照 controlPage 模式，app.js:2366）：
   - `agentPickerHtml` 两级下拉（机器 → 声明 session_adapter 的 agent；`session_adapter` 空值的 agent 禁选并注明「未声明会话适配器」，对应后端 422 诚实降级）
   - 绑定选择：adapter 只读展示（来自能力卡声明）、权限模式下拉（保守默认，绝不含 yolo 档；与 REST 契约一致：UI 不提供 yolo 选项）
   - 工作区路径输入 + 首轮消息 textarea
   - `#sess-feedback` statusline + C 形状态机（idle/submitting/unknown，结果未知锁表单+重试），沿用 controlPage 既有机制
3. **会话详情抽屉**（参照任务抽屉 syncDrawer，700px）：
   - 分区（drawerZones 参数化复用）：①绑定与状态（agent/机器/adapter/权限/工作区）②轮次时间线（序号、状态徽标、时间、幂等键短形式；unverified 显式标注「工作结果未证实」，绝不渲染为成功）③选中轮次详情（答复用现成 mdToHtml shared block 渲染；usage；unverified 时置顶警示条）④动作区（继续此会话 → 预填表单、关闭会话 → slim modal 确认）

### node 端（本机会话视图）

- 任务页新增「本机会话」区块或独立页（按 node 现 4 导航的信息密度裁决，倾向任务页内区块，避免导航膨胀）：本机会话列表 + 新建/续接表单（单机无两级下拉，直接 agent 下拉）+ 会话详情抽屉（同构 hub 四分区，数据走 localapi 五端点）
- 关闭确认用 node 既有 confirmModal（追加式叠加层）——hub 用 slim modal 是**既有白名单差异，不强制统一**，入 SPEC 白名单档

### 双端形态对照表（任务书硬性要求，实现后逐行走查）

| 组件 | hub | node | 一致要求 |
|---|---|---|---|
| 会话列表 | 独立「会话」导航页 | 任务页内区块 | 同列名顺序、同状态词、同徽标形态、同空态文案 |
| 新建/继续表单 | 派发页式两级选择 | 单下拉 | 同字段顺序（目标/绑定/工作区/消息）、同禁选原因文案、同反馈状态机 |
| 详情抽屉 | 700px 四分区 | 同构 | 分区顺序同、轮次时间线布局同、unverified 警示条同文 |
| 关闭确认 | slim modal | confirmModal 叠加层 | 同标题/正文/按钮语义（形态差异白名单） |
| unverified 呈现 | heat 底 + 「工作结果未证实」 | 同 | 词典同源，禁止渲染为成功 |

## 纪律

- shared block 涉改（预计：抽屉展示词典增会话态、抽屉分区更新参数化扩展）：双端同改同验，锁测试 `go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿 + diff 两端标记块
- 语义色不扩编（现预算 4 个）：unverified 用 heat、failed 用 danger、running 用 live、done/cancelled 退灰
- 字号地板 12.5px 显式声明；reduced-motion 新动效全降级；z-index 沿用层叠表
- 双端 web 均为 embed 资产：改动后 go test ./internal/hub/web/ ./internal/node/web/ 全绿（node harness 自动 skip 时的源码锚点锁兜底须保留）
- 轮询豁免表：会话详情抽屉打开期间不整页重绘（参照 groups 群详情既有豁免）
- 不做：伪聊天流 UI、流式渲染、下拉自绘、新导航层级、会话内编辑历史

## 验收（三层走查制度，一项不绿不交付）

1. **功能审查（E2E DOM 断言）**：tests/e2e-console.mjs 新增 hub S01-S06（列表渲染/筛选/新建表单联动/抽屉分区/继续预填/关闭确认）+ node 模式 SN01-SN03；E2E_BASE 显式指向本地起服（**禁止默认线上 hub**）
2. **整页视觉走查**：双端会话页全量截图（1440/1280/1024/680/390 视口矩阵），人眼核查
3. **弹层深层态走查**：抽屉内逐帧——轮次点开、unverified 警示、关闭确认弹层、确认后列表刷新、键盘 Tab/Esc/焦点返回
4. `go build/vet/test -count=1 ./...` 全绿；`node --check` 双端 PASS
5. 不 commit；报告改动清单+走查证据（截图入 /tmp/agent-session/t3-shots/），commit 由编排方执行
