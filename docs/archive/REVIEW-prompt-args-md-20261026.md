# REVIEW-prompt-args-md-20261026 · 任务指令 args 长文本 md 渲染审查记录

> 任务书：docs/TASK-prompt-args-md-20261026.md（伦哥反馈「任务指令支持渲染 md」）
> 诊断：生产任务（Prime 派发）提示词整篇走 input.args（stdin 空），promptZoneHtml 的 args 分支按「短命令行参数」假设渲染成行内 code 胶囊——数百行 Markdown 无渲染。设计假设与真实派发形态不符。
> 流水线：zcode 编写 → Hana 独立验证 + 走查

## 修复设计（shared block promptZoneHtml 单函数，双端自动同文）

- args 形态判据：join 含换行 **或** 总长 >300 字符 → 提示词文本形态（md 渲染+原文切换）；否则短参数形态（逐参 code 胶囊，保留参数边界——codex 前轮意见适用面收窄至短参数场景）。
- stdin+args 并存盲区补齐（此前 stdin 非空时 args 被忽略）：双小节标题「任务说明（stdin）」/「命令行参数（args · N 段）」，共享原文切换。
- 单源任务不显示小节标题（视觉与现状一致）；底部原样 JSON details 不变。

## 验证

- go build/vet 绿、22 包全绿、TestWebMdRender|TestWebDrawer 双绿（shared block 双端同文）
- playwright 9/9：长文本 args md 渲染（strong/列表断言+无胶囊形态+单源无小节标题）+原文切换含源码；短参数逐参胶囊不回归（--check/--verbose/inv.log）；双源双小节+两段内容断言
- zcode 自建行为级单测（node 子进程跑真实 app.js 的形态判据边界）

## 走查注记

- 走查脚本一处断言笔误（'# 周报整理' 写成 '## 周报整理'）致一次假 FAIL，截图人眼核查裁决功能正确，修正后 9/9。测试数据与断言字符串不一致属脚本质量，非产品代码。
