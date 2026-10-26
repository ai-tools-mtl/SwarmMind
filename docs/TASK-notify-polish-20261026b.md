# TASK-notify-polish-20261026b · 通知配置统一与排版修正（伦哥 2026-10-26 午后三反馈+双端一致裁决）

> 来源：伦哥部署后实测三截图。基线：038e049（已部署生产双端）。
> **本轮最高纪律：双端一致范围 = 三层（渲染/操作逻辑/文案），CLAUDE.md 2026-10-26 升格条款全程适用。**

## 双端形态对照表（实现与走查的对照基准）

| 项 | hub 中心端 | node 客户端 | 目标 |
|---|---|---|---|
| 渠道配置入口位置 | 设置页第二区块 | 服务页**最底部** | node 上移至服务页页头之后第一位（agent 卡之前） |
| 渠道添加/编辑形态 | 居中 modal（#layer .modal） | **右侧抽屉**（#layer .drawer） | node 改居中 modal，与 hub 同形态 |
| 表单骨架（切消息格式） | 字段数量/位置随格式变化（generic 多 headers、feishu 无）导致布局跳动 | 同 hub 实现 | 双端同构骨架：格式/地址/请求头三件恒显恒位，切格式只变 label 与 disabled，零跳动 |
| 表单类型词典 | NOTIFY_TYPES+NOTIFY_FORMATS | 家族副本 | 双端同文进 shared block（node 无 inbox 差异参数化保留） |
| 铃铛按钮 | .bell-btn padding 未重置致图标偏移 | 无铃铛（hub 专属） | hub 修 padding:0，图标居中 |

## 改动一：铃铛图标与底纹错位修复（hub）

文件：internal/hub/web/style.css。

- 根因（生产实测几何）：`.bell-btn` 未重置全局 `button{padding:0 16px}`，34px 圆钮内容区仅剩 2px，18px SVG 被 padding 顶至 x 偏移 16px（应 8px）——图标在底纹中明显偏右。
- 修法：`.bell-btn` 加 `padding:0`。修复后断言：svg 中心与按钮中心横纵偏差均 <1px。
- 自查整个 style.css：还有没有其他小尺寸图标按钮吃全局 padding 的同类隐患（如有一并修，注明）。

## 改动二：通知渠道表单骨架统一 + 双端同文（hub+node）

文件：internal/hub/web/app.js、internal/node/web/app.js、（样式若需同补则双端 style.css）。

- **字段区恒定骨架**（webhook 类型，双端同构）：
  1. 消息格式（select：通用 JSON / 飞书机器人）——恒显恒位
  2. 推送地址（label/placeholder 随格式变：推送地址 ↔ 飞书机器人 Webhook 地址）——恒显恒位
  3. 自定义请求头（textarea）——**恒显恒位**：generic 可编辑；feishu 时 disabled + 行内说明「飞书机器人协议不支持自定义请求头」
- 切换格式仅 label/disabled 变化，**字段区高度不变**（断言：切格式前后 #nf-type-fields 高度差 = 0，或仅 label 文本差异）。
- hint（飞书路径文案/通用说明）位置恒定。
- **双端同文**：notifyFormHtml 表单骨架、notifyTypeFieldsHtml 字段区、NOTIFY_TYPES/NOTIFY_FORMATS 词典迁入 shared block，差异（node 无 inbox/sms 类型）以参数传入（类型枚举表参数化），shared block 内不得出现 `S.page`/端特判分支。既有差异白名单（node 无 inbox）在词典参数中体现，不破坏「无 inbox」如实口径。
- 迁移后 shared block 锁测试同步更新。

## 改动三：node 渠道配置改居中 modal（与 hub 同形态）

文件：internal/node/web/app.js、internal/node/web/style.css。

- openNotifyForm 的层骨架从 `drawer-overlay + .drawer` 改为 `overlay + .modal`（与 hub openLayerPlain 同构），表单内容不变。
- modalHead（`<div class="row"><h2>…</h2>×按钮</div>`）进 shared block 双端同文（hub 已有 modalHead，node 迁入同文）。
- node style.css 从 hub style.css 同步 .modal/.overlay 相关样式（对齐 hub 的 modal 视觉：圆角/阴影/宽度/入场动效），不发明新样式。
- 关闭行为对齐 hub：× 按钮 + 遮罩点击关闭（现有 nfMaskClose 改 modal 适配）；Esc 关闭若有则保留。
- 无障碍属性对齐 hub：role=dialog、aria-modal、aria-labelledby、焦点移入。

## 改动四：node 通知卡上移

文件：internal/node/web/app.js（servicesPage）。

- 区块顺序：head → **notifyCard()** → 空态/agent 服务卡 → cfgSection()（能力配置编辑区沉底）。
- 通知卡从页面最底上移至页头后第一位；footer-note 等文案随迁不丢。

## 改动五：SPEC 六.五 契约范围扩展说明

文件：SPEC.md 六.五。

- 「双端 Web 展示一致性契约」范围补记：一致 = 渲染层（shared block）+ 操作逻辑层（弹层形态/表单结构/卡片位置/交互流程同构）+ 文案层（词典同源）；操作逻辑层不进 shared block 锁（形态由代码结构承载），以「双端形态对照表+对比截图走查」保障。注明 2026-10-26 伦哥裁决。

## 已知陷阱

1. shared block 逐字节锁：本次新增/迁移的同文块（notifyFormHtml 骨架、notifyTypeFieldsHtml、NOTIFY_TYPES/FORMATS、modalHead）迁移后 `TestWebMdRender|TestWebDrawer` 双绿；参数化不得引入端特判 if 分支进 shared block。
2. D2 局部刷新纪律：node 通知卡上移不改 notifyCard 内部结构；modal 内保存流程局部更新不变。
3. 0 值退灰、hash 禁锚点、CJK 窄列——沿用既有纪律。
4. 改 style.css 后**必须重建二进制再实测**（embed 坑，CLAUDE.md 2026-10-26 条）。
5. 工人不执行 git commit。

## DoD 验证矩阵

- [ ] go build/vet/test 全绿 + TestWebMdRender|TestWebDrawer 双绿
- [ ] 铃铛几何断言：svg 中心与按钮中心偏差 <1px（生产同版本二进制实测）
- [ ] 走查：hub 弹窗切 generic↔feishu 格式，#nf-type-fields 高度差 0，仅 label/disabled 变化（截图对比）
- [ ] 走查：node 渠道编辑为居中 modal（形态断言+截图与 hub 同组件对比）
- [ ] 走查：node 服务页通知卡位于页头后第一位
- [ ] 双端同组件对比截图入证据（表单/modal/卡片三组）
- [ ] 改动文件清单 + 逐条 DoD 结果输出
