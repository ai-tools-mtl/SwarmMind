# 文档完善专项独立验收报告

> 历史范围说明：本文记录 2026-10-25 的文档与工作区状态；其中工具数量和开发阶段已随代码推进变化。2026-10-26 的修订与验证见 [本轮报告](TESTREPORT-docs-20261026.md)，不以本文充当当前通过证明。

- 日期：2026-10-25；环境：macOS / arm64，Go 1.24.13。
- 执行者：独立验收 subagent，仅从当前 AGENTS.md、SPEC 第七节和 TESTPLAN 的 DOC-01–08 读取要求；未参与实现。
- 范围：当前未提交工作区的文档专项；不代表 P5 W1–W2 或整个产品全部验收完成。未执行真实异构双机与 Windows 服务真机测试。
- 本执行者仅新增本报告；未修改代码、配置或其他项目文档，未 commit。构建程序、实例配置、依赖和截图均放在 `/tmp/swarmmind-docs-acceptance/`；测试实例均已停止。
- 结论：DOC-01–08 最终均 PASS。DOC-04 首轮发现标签与连线重叠，由文档实现者修复后独立复验通过。SPEC DoD 1–10 本轮本机验证通过；DoD 11 **未执行**，不能宣称完整产品验收通过。

## DOC 用例

| ID | 结果 | 本轮操作与证据 |
|---|---|---|
| DOC-01 | PASS | Python 提取 README.md、docs/ARCHITECTURE.md、docs/OPERATIONS.md、docs/README.md 的本地 Markdown 链接和图片路径，逐项检查目标存在；本报告生成后包含其自身的链接也存在。`git check-ignore docs/assets/architecture.svg docs/assets/architecture.mmd` 退出 1、无输出，两文件均非忽略文件。README 内部章节锚点对应当前标题。 |
| DOC-02 | PASS | 对照 Makefile 的四条 build 命令、cmd/hub/main.go:50、cmd/node/main.go:66、cmd/swarmmindctl/main.go:107–145 和 configs 示例核对输出名、参数、cookie 登录与 ws/wss 语义。另在独立临时目录按 README 最小配置启动 WSS hub+node：随机端口 51130/51131/51132（以 manual.log 实际记录为准），分别独立 data_dir、临时生成两份令牌、TOFU 首连、/bin/echo 与 /tmp 工作目录；按 README CLI 提交 `--arg hello` 并 query，完成且退出码 0、FinalAnswer 为 `hello\n`（echo 正常换行）。实际端口、任务 ID 与查询响应见 manual.log；测试进程已 terminate+wait。 |
| DOC-03 | PASS | 实际 `go build -o /tmp/swarmmind-docs-acceptance/mcp ./cmd/swarmmind-mcp`；分别设 SWARM_TARGET=hub/node，向 stdio 发送 JSON-RPC tools/list，两进程均退出 0。hub 返回 17 工具，其中 Work create/get/list/takeover 四个；node 返回发现与执行六个，均有 swarmmind_ 前缀，与 README 一致。原始响应见 tools-hub.json、tools-node.json。 |
| DOC-04 | PASS（修复后） | ElementTree 成功解析 SVG。独立 Playwright Chromium 按 1120×820 实际渲染并查看最终 architecture-final.png：Linux/macOS/Windows 三节点主动向 hub，WSS 双向传输说明、独立管理面/节点入口和 MCP stdio/API 关系正确，文字与边框无截断，标签与连线无重叠。Mermaid 11 实际解析 architecture.mmd、ARCHITECTURE.md 的 sequenceDiagram 与 stateDiagram-v2，分别返回 flowchart-v2 / sequence / stateDiagram，见 visual.log。图源与正文逻辑一致。 |
| DOC-05 | PASS | 容量表逐项对应 internal/protocol/constants.go：100 条/8 MiB 信箱、256 MiB 存储、7 天过期、24 小时终态保留、并发 2、完整帧 1 MiB、产物 640 KiB/8 个、尾部各 64 KiB；终态清理实际使用常量，见 internal/hub/core/sweep.go:117 与 internal/node/ledger/ledger.go:465–477。凭据与服务端主体核对 internal/hub/api/server.go:191–240、tokens.go:47、cmd/node/main.go:318–337、MCP backend 与 CLI 登录实现。产物路径、大小、SHA 与源节点保存核对 internal/node/runner/runner.go:185/259、artifacts.go:17–22、internal/node/inputs/ 和 hub/core/artifact.go。P5 文档明确当前 W1–W2 开发范围，不冒充 W3–W4 已交付。任务时序核对 service.go:381–388：started 落盘 → task_started → 启动，当前 ARCHITECTURE.md:39–41 一致。 |
| DOC-06 | PASS | 实际 `go build -mod=readonly ./...` 退出 0，build.log 无报错；实际 `go test ./...` 退出 0，23 个包中 19 个测试包通过、4 个无测试（部分包使用 Go 缓存，未伪称全部强制重跑）。完整日志见 build.log、test.log。 |
| DOC-07 | PASS | 实际 `./scripts/demo-loopback.sh` 退出 0，demo.log 末尾 DoD 3/4/5/6/7/8/9/10 全部 PASS。回环 echo 在 0.65 秒完成，FinalAnswer=hello swarmmind；整组终止有 pgrep 无幸存证据。DoD6 用 kill 模拟断线而非网络黑洞；旧 epoch 帧注入与 DoD8 重复 offer 分别以本轮 `go test -count=1 -v` 强制复跑补齐，见 epoch.log、duplicate.log，均 PASS。 |
| DOC-08 | PASS | 对照当前 SPEC 第七节逐项记录如下；DoD11 明示未执行，不引用历史报告作为本轮成功依据。 |

## 首轮发现与复验

- **建议修（已关闭）**：docs/assets/architecture.svg:22–25 原顶部三条竖直 HTTP 箭线穿过 HTTP / HTTP+PAT 标签，违反 DOC-04 的无重叠条件。独立首轮截图：`/tmp/swarmmind-docs-acceptance/architecture.png`。实现者将标签水平移动至 x=203/457/1005；本执行者重新实际渲染并查看 `architecture-final.png`，已无重叠。一次修复即通过，未触及两次返工上限。
- 必须修：无未关闭项。建议修：无未关闭项。可不修：无新增项。

## SPEC 第七节 DoD 十一项

| DoD | 结果 | 本轮验证证据与边界 |
|---|---|---|
| 1 构建与 go.sum | PASS | `go build -mod=readonly ./...` 实际退出 0，build.log；未修改 go.mod/go.sum。 |
| 2 单测 | PASS | `go test ./...` 实际退出 0，test.log。源码核对覆盖：EnvelopeRoundtrip；TransitionCheckFullGrid（8×8 与收敛转移）及 ResultArbitration/LateAckConvergence/LateStartedIgnored/ExpireSweep/CancelTaskStateMatrix；EnqueueQuota/DequeueReleases；DeliveryWindow；LedgerRecoveryAccepted*/StartedNeedsReview/FinishedUnacked；RunStdin/RunEnvBaseline/RunTimeoutKillsProcessGroup；RunPolicyDeniedEnv/PATH；SubmitIdempotency。 |
| 3 回环全链 | PASS | demo.log 的 DoD3：HTTP CLI 提交、completed 0.65 秒≤5 秒、FinalAnswer=hello swarmmind；另 README 手工 WSS echo 返回 hello 换行。链路单测 OfferRoundTrip/FullChain 所属包本轮通过。 |
| 4 超时整组终止 | PASS | demo.log：sleeper failed、error=timeout、`pgrep sleep 41` 无幸存；runner 整组终止测试所属包本轮通过。 |
| 5 离线信箱 | PASS | demo.log：nodeB 离线，提交 202/submitted，重连补推 completed，FinalAnswer=offline-mailbox。 |
| 6 重连韧性 | PASS（本机模拟） | demo.log：kill -9 nodeB 后重启，epoch 递增。`go test -count=1 -v ./internal/hub/session -run '^TestStaleEpochIgnoredThenDisconnect$'` 退出 0；epoch.log 记录旧 epoch 帧忽略并达到阈值断开。未注入真实网络黑洞。 |
| 7 hub 重启恢复 | PASS | demo.log：hub 重启持久化任务/目录/epoch 恢复，node 自动重连；HubRestartRecovery 与 E2EHubRestartDiskRecovery 所属包本轮通过。 |
| 8 幂等与重复 offer | PASS | demo.log：同键同内容返回同 task_id、dup=true；异内容 409。`go test -count=1 -v ./internal/node/service -run '^TestDuplicateOffer$'` 退出 0，duplicate.log：重复 offer 重发确认且仅执行一次。 |
| 9 白名单 | PASS | demo.log：越界 PATH 环境覆盖 failed+policy_denied、exit_code=-1；RunPolicyDeniedEnv 与 RunPolicyDeniedPATH 所属 runner 包本轮通过。 |
| 10 needs_review | PASS | demo.log：running 后 kill/restart，结果 needs_review/error=node_restarted_midway，非 failed、不重跑，CLI review 能查询上下文。 |
| 11 真实异构双机 | **未执行** | 本轮仅当前 Mac 的隔离本机实例和测试；未安排两台真实机器、真实异构 agent、跨机产物取回、真实断线恢复及权限拒绝。**该项不通过声明，不以历史报告或本机 demo 代替。** |

## 本轮证据路径

全部原始临时证据位于 `/tmp/swarmmind-docs-acceptance/`：`build.log`、`test.log`、`demo.log`、`manual.log`、`tools-hub.json`、`tools-node.json`、`visual.log`、`epoch.log`、`duplicate.log`、`architecture.png`、`architecture-final.png`。临时路径不随 Git 提交持久保存；关键结果已摘录在本报告。Mermaid 运行时下载到临时目录，未改项目 package.json、lockfile 或依赖。

手工验证脚本首两次因验收脚本假定 CLI 默认输出 JSON、以及遗漏 /bin/echo 正常末尾换行而自身断言失败；修正验收脚本后用新的独立 data_dir 重跑通过。该过程未要求修改产品行为，也未作为产品 FAIL 隐瞒。
