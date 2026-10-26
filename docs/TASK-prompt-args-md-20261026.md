# TASK-prompt-args-md-20261026 · 任务指令 args 长文本形态 md 渲染（伦哥反馈）

> 来源：伦哥反馈「任务指令希望支持渲染 md」。诊断：生产任务（Prime 派发）的提示词整篇走 `input.args`（stdin 空），当前 args 分支按「短命令行参数」假设渲染成行内 code 胶囊——数百行 Markdown 挤成一坨无渲染。**args 分支设计假设与真实派发形态不符**。
> 基线：0410071（已部署生产双端）。改动面：shared block promptZoneHtml 单函数 + 测试。

## 修复设计

promptZoneHtml（shared block，双端自动同文）内容源判定重构：

1. **args 形态判据**：`args.join('\n')` 含换行符 **或** 总长 >300 字符 → 「提示词文本形态」（md 渲染+查看原文切换）；否则「短参数形态」（逐参 code 胶囊，现状保留——codex 上轮「保留参数边界」意见只适用于短参数场景）。
2. **stdin + args 并存**（此前盲区：stdin 非空时 args 被完全忽略）：
   - stdin 段：小节标题「任务说明（stdin）」，md 渲染
   - args 长文本：小节标题「命令行参数（args · N 段）」，md 渲染
   - args 短参数：小节标题同上，code 胶囊行
   - 共享一个「查看原文」切换（raw 态显示 stdin+args 全部原文，pre 形态；「当前为原始输入。」文案不变）
3. **单源任务**（只有 stdin 或只有 args）：不显示小节标题，直接 md/胶囊（视觉与现状一致）。
4. 都空：分区省略（现状不变）。
5. 底部「任务输入（原样 JSON）」details 保留（现状不变）。

## 已知陷阱

1. shared block 逐字节锁：改后 `TestWebMdRender|TestWebDrawer` 双绿；diff 双端 shared block 为空。
2. 原文切换跨轮询保持（D22 分区 diff）不得回退。
3. esc 转义纪律：md 渲染走 mdToHtml、原文态走 esc，args 内容不可信面不引入注入。
4. embed 改后重建二进制再实测。
5. 工人不执行 git commit。

## DoD

- [ ] go build/vet/test 全绿 + shared block 双绿
- [ ] 走查：args 长文本任务（含 md 语法）→ 任务指令 md 渲染（标题/粗体/列表断言）+ 原文切换
- [ ] 走查：args 短参数任务 → code 胶囊逐参（现状不回归）
- [ ] 走查：stdin+args 并存任务 → 双小节展示
- [ ] 单元测试：形态判据边界（换行/300 字符阈值/双源/单源）
- [ ] 改动文件清单 + 逐条 DoD 结果
