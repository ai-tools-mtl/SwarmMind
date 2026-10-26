# 文档修订独立验收报告（2026-10-26）

执行者：独立验收 subagent；依据为当前 `AGENTS.md`、`SPEC.md` 第七节及 `docs/TESTPLAN.md` 文档内容修订专项 DOC-01–10。仅本报告由执行者创建，未修改其他文档或运行代码，未 commit/push/部署。

执行环境：Darwin arm64，Go 1.24.13；HEAD `24bb0c7` 加当前未提交工作区。使用本轮重新构建的二进制、临时目录和随机空闲端口，不连接现有部署。原有 `internal/hub/core/work_dispatch.go` 未提交改动与 `p5-w4-test/` 保留。

## 结论与范围

文档专项 **10 PASS / 0 FAIL**；SPEC DoD **1–10 本机验证通过，11 未执行**。本报告不代表全部产品验收完成，不授予进入下一版本或生产部署的结论。没有发现本轮文档修订范围内的「必须修」或「建议修」问题。

本轮未执行真实双机/真实 coding agent 异构闭环、W4 远程故障、Windows 服务真机、真实模型请求、P4 观察期或生产部署。历史 W4/P5 报告不计入本轮通过项。D1 live 准确率测试明确 SKIP，不计为模型验收通过。

证据目录：`/tmp/swarmmind-docs-final-acceptance/`。日志不包含临时凭据；实例配置含测试用随机令牌，仅保存在隔离目录。手动实例均 terminate 后 Wait；demo 自带 trap 清理。没有触碰现有部署。

## DOC 逐条结果

| 用例 | 结果 | 本轮操作与证据 |
|---|---|---|
| DOC-01 | PASS | Python 扫描 README、ARCHITECTURE、OPERATIONS、docs/README 的本地 Markdown 链接与图片路径，均存在（含本报告）；`git check-ignore` 对 SVG/Mermaid 均退出 1，非忽略文件。证据 `static.log`。 |
| DOC-02 | PASS | 对照 Makefile、四个 cmd 入口、示例配置与 CLI；最小配置按 README 保持 WSS/TOFU/独立 data_dir 语义，仅改成临时目录、随机空闲端口及随机凭据。新构建 hub/node/ctl，mac-mini 注册在线；CLI submit echo `--arg hello`，query 得 completed、`final_answer="hello\n"`。确认 hub TLS 证书、node trusted_hubs.json 与 local_token 存在。证据 `manual.py`、`manual.log`、`readme-task.json`。 |
| DOC-03 | PASS | 新构建 MCP 后独立 stdio JSON-RPC `tools/list`：hub 19，node 6；hub 包含五个 Work 工具及 dispatch_suggest，node 仅六个发现/执行工具。证据 `tools-hub.json`、`tools-node.json`、`static.log`。 |
| DOC-04 | PASS | ElementTree 解析 SVG；Chromium 在 HTML 中渲染 SVG 并截图目检，三节点主动 WSS 连接 hub，建连后的双向消息有说明，管理/节点端口分别 8444/8443，无文字截断、节点或线文字重叠。实际 Mermaid parse：architecture.mmd、ARCHITECTURE 两张内嵌图、RFC-jev 一张，共四份图源全部成功。证据 `architecture.png`、`visual.cjs`、`visual.log`。 |
| DOC-05 | PASS | OPERATIONS 限额逐项对照 `internal/protocol/constants.go:30` 起的保留期/容量常量、鉴权与 inputs 源打开/发布实现；hub Sweep 已运行接线，node CleanupAcked 只有定义与测试、没有运行调用。文档已区分任务记录、账本和实际产物生命周期；凭据用途与执行路径一致。隔离公共 API 实测未启用 dispatch-suggest 返回 HTTP 200、suggestion:null、degraded:true；没有误用内部 Service 未启用分支的 degraded:false。证据 `dispatch-disabled.json`、`manual.log`；源码 `internal/hub/api/dispatch_suggest.go:56`。 |
| DOC-06 | PASS | 实际 `go build -mod=readonly ./...`、`go test -count=1 ./...` 均退出 0；不是缓存冒充新执行。证据 `build.log`（成功无输出）、`test.log`。 |
| DOC-07 | PASS | 实际 `./scripts/demo-loopback.sh` 退出 0，DoD 3–10 全部 PASS，含 WSS/TOFU 与进程组终止；DoD 6 旧帧、DoD 8 重复 offer 注入项由此次全量单测补充。证据 `demo.log`。 |
| DOC-08 | PASS | SPEC 十一项均逐项登记见下表；DoD 11 明确未执行，不引用历史报告冒充通过。 |
| DOC-09 | PASS | Work W1–W3 已实现，collect/保留期/关闭语义有实现与单测依据；D1 为 llm-as-jev、无本地部署或概率校准保证，真实模型终版复验仍待补；node 列表全量账本驱动且支持 limit/offset，清理未接线；群消息写入不自动启动执行；W4 B/C 证据缺口与旧 Prime 行为差异有明确说明。见下节事实核对。 |
| DOC-10 | PASS | 主要文档区分实现、测试、发布和历史记录，说明正文未把宣传性概率或永久保留作为保证；Prime 仅前置安装说明变动，行为节保持运行模板同文，显式 `TestPrimePromptWorkContract` 与 `TestConsoleWorkViewSources` PASS。235 个 cmd/internal 文件 SHA-256 与任务开始快照一致；AGENTS/CLAUDE 除原有标题外正文同文。证据 `prime-lock.log`、`integrity.log`。 |

SVG 首次用独立 SVG 页面截图遇到 Playwright 截图超时；随后把相同 SVG 原文内嵌 HTML 成功渲染截图并目检，没有修改图源。这是验收工具呈现问题，不当作产品或文档失败。

## SPEC 第七节 DoD 1–11

| DoD | 结果 | 本轮验证证据与局限 |
|---|---|---|
| 1 构建与依赖 | PASS | `go build -mod=readonly ./...` 退出 0，go.sum 无需更新；四个程序另构建到临时目录用于真实执行。 |
| 2 单测覆盖 | PASS | `go test -count=1 ./...` 全包退出 0；核对信封 `TestEnvelopeRoundtrip`、状态全网格 `TestTransitionCheckFullGrid`，收敛表由 result/cancel/late-ack/expiry 路径测试覆盖；mailbox 配额/释放、delivery window、ledger 恢复、runner stdin/env/timeout/进程组、policy deny 与 idem 冲突均有此次运行的测试。证据 `test.log`、`test-evidence.txt` 及源码各测试文件。真实模型 live 用例 SKIP 另记。 |
| 3 回环任务链 | PASS | demo 实测 0.74s 内 completed，FinalAnswer 为 hello swarmmind；独立 README WSS/TOFU 首单另证 hello。 |
| 4 超时进程组 | PASS | demo sleeper failed/error=timeout，并检查 sleep 41 无幸存；`TestRunTimeoutKillsProcessGroup` 在全测中执行。 |
| 5 离线信箱 | PASS | demo nodeB 离线后提交返回 202/submitted，重连后 completed、FinalAnswer=offline-mailbox。 |
| 6 重连韧性 | PASS（本机模拟） | demo kill -9 后 epoch 2→3；`TestStaleEpochIgnoredThenDisconnect` 等单测补旧连接残帧注入。不宣称真实网络黑洞实测。 |
| 7 hub 重启 | PASS | demo 双 node 自动重连、能力目录恢复、重启后新任务完成；单测 `TestHubRestartRecovery` 补状态恢复。 |
| 8 幂等 | PASS | demo 同键同内容同 task_id/dup=true、不同内容 409；单测 `TestDuplicateOffer` 验证重复投递仅执行一次。 |
| 9 白名单 | PASS | demo 越权 PATH 返回 failed/policy_denied/exit_code=-1；runner 的普通 env 与 PATH 拒绝测试此次全测通过。 |
| 10 needs_review | PASS | demo running 后 kill/restart 得 needs_review/error=node_restarted_midway，非 failed，未自动重跑，hub 可查询且 CLI 输出核对指引。 |
| 11 真实异构双机 | 未执行 | 本轮没有两台真实机器、真实 agent/MCP或A2A发现派单及断线/权限/产物闭环。不得把 W4 历史报告、模拟网络和本机两个 node 计为本轮通过。 |

## 关键事实与既有未验收项

- **Work W3**：当前 `internal/hub/core/work_collect.go` 与测试、hub Work 视图、MCP 工具和提示词均存在。本轮全量单测及提示词锁通过，验证本机实现；没有重新执行远程 Work 故障或产品级 Prime 任务。
- **D1**：`internal/decision/llm.go:113` provider 名为 llm-as-jev，调用配置的模型 HTTP 服务；归一化不能证明统计校准。公共未启用降级响应实际验证通过。`TestLLMLiveAccuracy` 显式运行得到 SWARM_DECISION_LIVE 未设置而 SKIP，证据 `live-skip.log`；终版逐场景真实模型报告仍待补（`docs/RFC-jev-dispatch.md:64`）。
- **node 任务与保留**：`internal/node/localapi/local.go:237` 合并账本和留痕后排序分页；`rg -n CleanupAcked cmd/ internal/node/` 只有定义/测试，未发现运行接线。`docs/INTERACTION.md:37` 与 `docs/OPERATIONS.md:73` 已如实说明。
- **群消息**：`internal/hub/api/groups.go:190` 的消息写入不调用任务提交；`docs/GROUP-RFC.md:39` 明确需要正式提交执行，群成员和提及不构成派发协议。
- **W4 历史采证**：独立读取 `docs/testing/w4/b5-work-after-restart.json` 的 `.work.status=executing`，步骤 2 pending；`c5-work-final.json` 的 `.work.status=blocked`，步骤 2 failed。它们不能证明 B/C 最终关闭。本轮核对注在 `docs/TESTREPORT-w4-p5.md:7` 起明确此缺口；原历史正文保留，不算本轮重新全绿。
- **Prime 旧行为**：`deploy/prime/PRIME.md:9`、`docs/PRIME-DESIGN.md:82` 已列出旧决策工具名、修改内容同键重派、跨机文件限制等既有语义差异。本轮只改 PRIME.md 前部安装说明，未改行为节或运行模板。**锁测试通过仅证明资产与运行模板一致，不表示这些旧行为差异已修复。**

这些项目不构成本轮文档真实性的失败，但真实双机/模型/Windows/观察期与运行 Prime 行为修复仍需对应后续验收，不能由 DOC 通过消除。

## 本轮主要修订文件清单

此清单描述送验工作区；独立执行者仅新增本报告。`internal/hub/core/work_dispatch.go` 与 `p5-w4-test/` 为用户既有改动/材料，不纳入本轮文档修改。

- 入口与导航：`README.md`、`docs/README.md`。
- 新架构与运维：`docs/ARCHITECTURE.md`、`docs/OPERATIONS.md`、`docs/assets/architecture.svg`、`docs/assets/architecture.mmd`。
- 规格与配置说明：`SPEC.md`、`configs/hub.example.yaml`（注释）。
- 总体与交互：`docs/PLAN.md`、`docs/PRODUCT-DIRECTION-v2.md`、`docs/INTERACTION.md`。
- 专题设计：`docs/FILE-COLLAB.md`、`docs/GROUP-RFC.md`、`docs/PRIME-DESIGN.md`、`docs/RFC-work-resume.md`、`docs/RFC-jev-dispatch.md`。
- 安装与部署：`deploy/prime/README.md`、`deploy/prime/PRIME.md`（前置安装说明）、`docs/DEPLOY-P2.md`、`docs/OBSERVATION-P4-plan.md`。
- 测试与记录：`docs/TESTPLAN.md`、`docs/TESTPLAN-FE.md`、`docs/TESTPLAN-D1-jev.md`、`docs/TESTREPORT-w4-p5.md`（核对注）、`docs/TESTREPORT-docs.md`（前序文档核对）、`docs/TESTREPORT-docs-20261026.md`（本轮独立报告）。
- 双指令文件：`AGENTS.md`、`CLAUDE.md`（同一条自动清理运行调用链规则，正文保持同步）。

## 重现入口

```bash
go build -mod=readonly ./...
go test -count=1 ./...
./scripts/demo-loopback.sh
go test -count=1 -v ./internal/hub/web -run 'TestPrimePromptWorkContract|TestConsoleWorkViewSources'
go test -count=1 -v ./internal/decision -run '^TestLLMLiveAccuracy$'
```

最后一条在未设置 SWARM_DECISION_LIVE 的常规环境应 SKIP，不应设置凭据启用 live 后将其混入本轮无真实模型请求的范围。stdio tools/list 输入、README 隔离首单与图源解析实际脚本和输出均在本报告注明的临时证据目录。
