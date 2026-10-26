# 文档导航

文档分两区：**活文档区**（docs/ 根，与当前代码同步维护）与**归档区**（docs/archive/，任务书、审查记录、测试报告等过程文档，按原样保存，不随后续改动更新）。功能状态以当前源码与验证记录共同判断，不从标题中的阶段编号或旧的「FINAL」标记推断已经发布。

## 使用与运维

| 文档 | 内容 |
|---|---|
| [项目 README](../README.md) | 项目用途、最小配置、首次任务、MCP 接入与功能状态 |
| [架构详解](ARCHITECTURE.md) | 组件职责、任务流转、恢复语义与权限边界 |
| [部署指南](DEPLOYMENT.md) | 中心端与节点端分章：功能清单、部署步骤、部署后使用与连通验证 |
| [部署与排障](OPERATIONS.md) | 端口、凭据、TLS、数据保留与常见问题 |
| [双机与 Windows 部署](DEPLOY-P2.md) | 部署模板、系统服务与真实环境验收清单 |
| [Prime 配置](../deploy/prime/README.md) | 全网和本机两种编排入口、实际验证方式 |
| [架构 SVG](assets/architecture.svg) / [Mermaid 图源](assets/architecture.mmd) | 可预览和编辑的系统拓扑 |

## 设计契约

| 文档 | 当前定位 |
|---|---|
| [SPEC](../SPEC.md) | 协议、接口、可靠性契约（4.7 持久化事务：hub SQLite + node JSON）与已知实现差异 |
| [项目计划](PLAN.md) | 技术决策、阶段目标、验收与发布条件 |
| [产品方向](PRODUCT-DIRECTION-v2.md) | 场景、优先级、术语说明与后续范围 |
| [交互设计](INTERACTION.md) | 当前页面职责、任务流程与反馈要求 |
| [Prime 设计](PRIME-DESIGN.md) | 编排职责、重试语义、建议接口与模板差异 |
| [文件协作](FILE-COLLAB.md) | 已有互斥/依赖/挂载，及尚未实现的 Git 集成 |
| [群聊设计](GROUP-RFC.md) | 当前 REST/MCP 消息协作与暂缓的完整协议 |
| [分配建议 RFC](RFC-jev-dispatch.md) | D1 已实现；终版模型实测待补，D2/D3 规划 |

## 验证

| 文档 | 范围 |
|---|---|
| [TESTPLAN](TESTPLAN.md) | 基础回归及文档专项；不代表所有扩展功能仅由基础用例覆盖 |
| [前端计划](TESTPLAN-FE.md) | 控制台走查清单（对照归档原型 dashboard-prototype-final.html） |

## 归档区（docs/archive/）

任务书（TASK-*）、审查记录（REVIEW-*）、测试报告（TESTREPORT-*）、已执行完毕的阶段性计划与 RFC（TESTPLAN-W1~W4 / TESTPLAN-D1 / TESTPLAN-tasks-audit / RFC-work-resume / OBSERVATION-P4-plan / PLAN-info-convergence）等 51 份过程文档，以及历史原型与走查脚本（prototypes/ 子目录）。归档文档按写入时点的代码版本解读，不反映当前状态；需要追溯时按文件名在 archive 内查找。

历史验收的机器证据（docs/testing/）与走查截图（docs/prototypes/shots-*，未跟踪）已随 2026-10-26 文档整理移出仓库或保留在本地，结论均沉淀在对应 TESTREPORT/REVIEW 中，原始文件见当时提交的 git 历史。
