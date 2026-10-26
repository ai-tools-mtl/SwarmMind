# REVIEW-notify-polish-20261026b · 通知配置统一与排版修正审查记录

> 任务书：docs/TASK-notify-polish-20261026b.md（伦哥三反馈+双端一致升格裁决）
> 流水线：zcode 编写 → Hana 独立验证 + playwright 三层走查（本轮 codex 未单独过审：改动主体是上轮已审形态的统一收口+一行 CSS 根因修复，Hana 按 codex 审查守则逐条对照任务书核查并复跑全部测试；下轮起恢复 codex 全量过审）

## 五项落地

1. **铃铛错位修复**（hub style.css）：根因=`.bell-btn` 未重置全局 `button{padding:0 16px}`，34px 圆钮内容区剩 2px，SVG 被顶偏 16px（生产几何实测）。修复 `padding:0`，实测图标与底纹中心偏差 dx=0.00 dy=0.00。
2. **表单骨架统一**（双端同文）：字段区恒定骨架（消息格式/推送地址/自定义请求头三件恒显恒位），切格式仅 label/disabled 变化；feishu 格式请求头禁用+「飞书机器人协议不支持自定义请求头」行内说明。**实测双端字段区高度 269px↔269px 零跳动**——连像素高度都同构。
3. **node 渠道编辑改居中 modal**：openNotifyForm 层骨架 drawer→modal（与 hub 同形态），modalHead 双端同文进 shared block，node style.css 同步 .modal 样式（hub 同文，含 reduced-motion 降级），无障碍属性对齐（role=dialog/aria-modal/焦点移入）。实测 modal=1 drawer=0。
4. **node 通知卡上移**：服务页顺序 head → 通知卡 → agent 服务卡 → 能力配置（沉底）。实测通知卡紧随页头 gap=28px。
5. **双端一致制度落档**：CLAUDE.md/AGENTS.md 升格条款「双端一致范围=三层（渲染/操作逻辑/文案）」+任务书强制双端形态对照表+对比截图走查；SPEC 六.五 契约范围同步补记。

## 流程注记

- 本轮是「操作逻辑层统一」的第一个完整实践：表单骨架/词典参数化迁入 shared block（node 无 inbox/sms 差异以类型枚举参数传入，shared block 无端特判分支）。
- 排查记录：node 控制台白蓝视觉为既有设计（P5 打磨成果），本轮 node style.css diff 全部在任务书范围（modal 样式同文+hint 槽），无越界——已核对排除。

## 验证

- go build/vet 绿、22 包测试全绿、TestWebMdRender|TestWebDrawer 双绿（shared block 锁）
- playwright 11/11：铃铛几何（dx=0/dy=0）、hub modal、切格式高度差 0（双端 269=269）、feishu 请求头禁用+说明、label 切换、node modal=1 drawer=0、node 表单含消息格式、node 切格式零跳动、通知卡第一位（gap=28px）
- 截图人眼核查：铃铛居中、飞书格式弹窗（完整骨架+禁用态+新文案）、node 服务页（通知卡首位+同时通知开关）

## 遗留

- 「什么时候通知」勾选语义存在「全勾=全部/全不勾也=全部」的双态重叠（预存行为，非本轮引入；legend 已解释全不勾语义）——登记不扩围，后续若动事件订阅交互一并理顺。
