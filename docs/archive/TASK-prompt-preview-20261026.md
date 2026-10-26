# TASK-prompt-preview-20261026 · 任务指令长文本预览折叠（伦哥反馈）

> 来源：伦哥反馈「任务指令显示预览，需要时点击全部显示，目标就现在这种形式」。
> 现状：args 长文本（Prime 系统提示词数百行）在抽屉里完整渲染，抽屉被撑得极长。
> 基线：53752f7。改动面：shared block promptZoneHtml + 少量 CSS + 动作 + 测试。

## 交互设计

- **渲染形式不变**（md 渲染 + 查看原文切换 + 小节结构 + 恒定位骨架），只加「预览折叠」层。
- **折叠判据（字符数，模板可算无 JS 测量）**：任务指令总内容（stdin + args join，按现有拼接口径）长度 > 1200 字符 → 默认折叠预览；≤1200 直接全显（现状形态，无展开按钮）。
- **折叠形态**：内容容器 max-height 180px + overflow hidden + 底部 60px 渐隐遮罩（渐变到容器底色）+「展开全部」按钮。
- **展开态**：点击「展开全部」→ 完整显示，按钮变「收起」；再点收回预览。展开/收起态存 `S.drawer.promptExpanded`（与 promptRaw 同模式），渲染函数从状态算 HTML——轮询重建/分区 diff 天然保持（D22）。
- **原文态同样折叠**（raw 数百行同样长），展开态共享同一个 S.drawer.promptExpanded。
- 短参数胶囊形态不参与折叠（本来就短）。
- 双源（stdin+args 小节）折叠作用于整个内容容器（一个按钮控制全部）。

## 落点

- internal/hub/web/app.js + internal/node/web/app.js：promptZoneHtml（shared block 双端同文）+ 'prompt-expand' 动作（对齐 prompt-raw 模式）+ S.drawer.promptExpanded 语义注释。
- 双端 style.css：折叠容器/渐隐遮罩/按钮样式同文（注意渐隐底色：md-body 浅灰底与页面白底不同，遮罩渐变需贴合实际容器底色——md 态渐变到 --surface 近似即可，误差可接受；或遮罩只盖内容容器用白底。以视觉核查为准）。
- 锁测试同步（shared block 锁面若有 prompt 相关断言）。

## 已知陷阱

1. shared block 逐字节锁双绿；无端特判分支。
2. D22：展开态是渲染状态（进 S.drawer），分区 diff 从状态算 HTML——禁止「重建后 JS 恢复展开」的反模式。
3. 折叠判定阈值 1200 字符为模板常量（含注释说明取值理由：约一屏 md 渲染量）。
4. embed 改后重建二进制再实测。
5. 工人不执行 git commit。

## DoD

- [ ] build/vet/test 全绿 + shared block 双绿
- [ ] 走查：长文本（>1200 字符）默认折叠（max-height+遮罩+展开按钮断言）→ 点展开全显 → 点收起回折叠；轮询后状态保持
- [ ] 走查：短内容（<1200）无折叠无按钮（现状形态）
- [ ] 走查：原文态同样折叠/展开联动
- [ ] 截图：折叠态（含渐隐）/展开态对比
- [ ] 改动文件清单 + 逐条 DoD 结果
