# docs 目录索引

顶层只留「活文档」（持续使用或持续追加）；过程性文档（已完成任务、已取代的规划、一次性评审）归档至 `archive/`。

## 顶层文档

| 文档 | 定位 | 状态 |
|---|---|---|
| PLAN.md | 总方案书（FINAL v3.0）：定位、架构、生命周期 P0-P5、风险与开放问题 | FINAL（进度随阶段更新） |
| INTERACTION.md | 交互设计与生命周期规格：旅程矩阵、页面树、反馈闭环五规则 | FINAL |
| GROUP-RFC.md | 群聊/协作空间 RFC（P4 主特性）：MVP 已合入，实现偏差与恢复归属见第八节 | 草案 v1.0 · MVP 已合入 · 关门清单跟踪中 |
| FILE-COLLAB.md | 文件协作计划：L1 互斥（已交付）/ L2 产物链与依赖（进行中）/ L3 Git 集成（P5） | FINAL（L2 进行中） |
| PRIME-DESIGN.md | 统筹载体（Prime）设计：角色边界、StartLux 影子决策与 MCP 编排原语、提示词规格 | 评审闭环 |
| TESTPLAN.md | hub/node/E2E 测试计划（持续使用） | 持续追加 |
| TESTPLAN-FE.md | 前端验收规程（FE-PLAN v3 的用例本体，持续回归用） | 持续回归 |
| TESTREPORT.md | 测试报告（按轮次追加） | 持续追加 |
| DEPLOY-P2.md | 双机部署指南与 DoD 11 验收清单 | 稳定 |

## 子目录

| 目录 | 内容 |
|---|---|
| agentbridge/ | AGNTCY AgentBridge 竞品实测报告 |
| prototypes/ | Web 控制台 HTML 原型（实现前经伦哥拍板的依据） |
| archive/ | 过程性文档：已完成 TASK 任务文档与验收截图、已交付的 FE-PLAN 各版、一次性 REVIEW 评审记录、已完成专项计划 |

## 归档约定

- 验收截图（png）不入版本库（`.gitignore` 的 `docs/**/*.png`），本地保留，TASK 文档中的文字引用不受影响
- 归档时机：规划被新版本取代、任务文档使命完成、评审通过后的 REVIEW 记录；移动后原路径引用需同步修正
