# 产品目标澄清文档验收（2026-10-29）

范围：远程原生 agent 与持久会话协作方案及入口文档；只修改 Markdown，不实现功能、不提交、不部署。主会话与不继承开发上下文的独立 subagent 分别执行构建/测试；本报告由主会话汇总独立验收返回的证据。

## 文档专项

| 用例 | 结果 | 证据 |
|---|---|---|
| VDOC-01 | PASS | VISION 一至四节覆盖三个痛点与两条场景；README 开头区分目标和现状；明确无头与持久化的区别、不保证恢复内部思维链 |
| VDOC-02 | PASS | protocol/types.go 的 ExecProfile/TaskInput/Task、runner.go 的 New/Run/buildEnv 支撑现状；core/work_dispatch.go 拒绝 done/skipped 步骤再派；hub/session 是节点连接 |
| VDOC-03 | PASS | VISION 五至七节覆盖网络、单写者、幂等、崩溃窗口、代码版本、停止条件、Work 限制、REST/MCP 与双端形态；新能力标为待实现 |
| VDOC-04 | PASS | 独立验收检查八份文档共 75 个相对 Markdown 文件链接，目标存在；git diff --check 退出 0；原有四份 FE-POLISH 未跟踪文档不在本轮修改范围 |
| VDOC-05 | PASS | 主会话与独立会话均执行 go build -mod=readonly ./... && go test -count=1 ./...，退出 0，所有有测试的包通过 |

独立审查结论：必须修 0、建议修 0。未来功能用例未执行属于范围边界，不是功能通过证明。

## SPEC 七逐项边界

| DoD | 本轮结果 |
|---|---|
| 1 | PASS：全量 readonly 构建成功 |
| 2 | PASS：全量非缓存测试成功，包含信封、状态机、信箱、投递窗口、账本、runner、白名单与幂等相关测试 |
| 3 | 未执行独立回环 demo，不记整项 PASS |
| 4 | TestRunTimeoutKillsProcessGroup 通过；独立 demo 未执行 |
| 5 | 相关全链测试通过；独立离线信箱 demo 未执行 |
| 6 | epoch/旧帧与重连相关测试通过；真实 kill/restart demo 未执行 |
| 7 | TestHubRestartRecovery 等通过；独立 hub 重启 demo 未执行 |
| 8 | TestSubmitIdempotency 等通过；独立 demo 未执行 |
| 9 | TestRunPolicyDeniedEnv 等通过；独立 demo 未执行 |
| 10 | TestLedgerRecoveryStartedNeedsReview 等通过；真实 kill node demo 未执行 |
| 11 | 未执行真实异构双机/真实 agent 产品验收 |

S-01–S-10 全部未执行；真实模型、公司与家庭网络、Windows 真机均未验证。通过文档专项不表示持久会话和自动协作循环已经交付。

## 本轮文件清单

- 新增 docs/VISION-persistent-agent-collaboration.md：核心方案、源码现状、实施顺序、未来验收用例。
- 更新 README.md：项目目标与当前实现边界。
- 更新 SPEC.md：标明新方案尚非已有协议。
- 更新 docs/README.md：文档导航。
- 更新 docs/PLAN.md：项目目标补充。
- 更新 docs/PRODUCT-DIRECTION-v2.md：目标流程与下一阶段优先级。
- 更新 docs/PRIME-DESIGN.md：固定线程循环与当前 Work 限制。
- 更新 docs/TESTPLAN.md：文档专项验收步骤。
- 新增本报告 docs/archive/TESTREPORT-vision-20261029.md。
