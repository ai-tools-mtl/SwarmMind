# 本机 Codex / zcode 原生会话两轮实测

日期：2026-10-29。真实模型调用，沿用本机默认配置，不覆盖模型或迁移凭据。工作目录在 `/tmp/swarmmind-session-probe.39sOyz/`，两个 agent 各用独立子目录，首轮进程退出后才启动第二轮。未使用 `--last`/`--continue`，没有重发首轮提示词。

## 结果

**两个 CLI 均能在新进程中按明确原生 ID 继续对话。Codex 保留了用户约定和成功工具输出；zcode 保留了用户约定、工具失败信息和文件操作背景。**

| 项目 | Codex | zcode |
|---|---|---|
| 版本 | codex-cli 0.164.0 | zcode-app-cli 3.16.3-41 / runtime 0.17.0 |
| 原生 ID，两轮一致 | `01a185b2-5e57-72c0-8013-643a9fe4d234` | `sess_0f95c375-87da-48b6-95a8-6108e1493bcf` |
| 执行模式 | workspace-write，approval_policy=never | edit |
| 首轮仅在提示词中的随机暗号 | `5aea472e3dcdbac8` | `038139e1b3085ab1` |
| 第二轮回忆暗号与金额乘7规则 | PASS，13→91 | PASS，13→91 |
| 首轮工具产生的随机值 | `fc0af73f2aeabcb3`，第二轮准确回忆 | Bash 因无权限客户端失败，没有生成；第二轮正确回忆失败原因，没有编造值 |
| 文件延续 | 首轮 stage=1，第二轮改为 stage=2，盘上核实 | 首轮 stage=1，第二轮改为 stage=2，盘上核实 |
| 退出码 | 两轮均0 | 两轮均0，首轮包含工具失败，不能据此判工作成功 |
| 用时（单次观测） | 25.91s / 25.24s | 41.53s / 见原始 r2-result |

Codex 原生 rollout 的两条 turn_context 均记录 model=`gpt-6-astra`、effort=`medium`、相同 cwd。zcode 的本次 CLI JSON 未提供明确模型标识，本报告不据默认产品名称推断模型。未读取或导出账号凭据。

## 测试方法与证据强度

首轮要求记住随机暗号、中文答复和金额乘7规则，不把这些写入工作区文件；执行随机输出命令并创建只含 stage=1 的 state.txt。第二轮只提出回忆问题，不包含暗号、规则或工具输出，再要求修改文件。

Codex 的 JSONL 事件显示第二轮在任何工具调用前先正确复述暗号、规则及 TOOL_MARK，随后才读取/写入文件。这是恢复对话和工具输出的直接证据。

zcode 的 `--json` 输出是一份最终 JSON 对象，并非 Codex 那样的实时 JSONL 事件流。第二轮同 sessionId、不同 turnId，回答准确回忆暗号与 Bash 的 `No permission client configured for Bash`，并修改文件。该输出不能独立证明工具调用先后顺序；其 projection.turnCount 两轮均为1，不能把它当会话累计轮数。也未验证 zcode 成功的 Bash 随机输出恢复。

文件持久化本身不证明对话恢复，因此单独核对随机暗号与工具历史。测试仅涉及短对话和正常进程退出后的恢复，不覆盖上下文压缩、长时间间隔、并发线程隔离、执行中崩溃、权限询问闭环或完整隐藏推理状态。

原始提示词、四次输出、退出结果和 SHA-256 清单保存在 [证据目录](../evidence/native-session-20261029/)。文件名以 `codex-`/`zcode-` 区分。调用脚本位于上述临时根目录。原生会话仍保留在各工具自身存储中，后续继续使用应考虑权限模式。

## 已验证命令形态

```bash
# 首轮；PROMPT 为实际提示词，DIR 为独立工作目录
codex exec --json --skip-git-repo-check -s workspace-write \
  -c 'approval_policy="never"' -C "$DIR" "$PROMPT"
# 第二轮，明确 ID；exec 的通用参数放在 resume 前
codex exec --json --skip-git-repo-check -s workspace-write \
  -c 'approval_policy="never"' -C "$DIR" resume "$SESSION_ID" "$FOLLOWUP"

zcode --cwd "$DIR" --mode edit --json -p "$PROMPT"
zcode --cwd "$DIR" --mode edit --json --resume "$SESSION_ID" -p "$FOLLOWUP"
```

Codex 的官方文档也说明了按 ID 恢复非交互会话：[Non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)。实际结论以本机执行日志为依据。

## 适配决策

第一阶段采用**每轮启动 CLI + 显式恢复原生 ID**，无需为这两个已测试版本先引入 PTY 常驻进程。

1. Node 保存平台 Session 到 node、agent、cwd、配置版本和原生 ID 的映射；首轮从 Codex thread.started 或 zcode sessionId 获取，后续明确传入并校验返回 ID 一致。未知/丢失 ID 不得静默新建替代线程。
2. Codex 解析 JSONL 的 thread.started、item、turn.completed 与错误；zcode 当前 headless JSON 先按整份结果解析 sessionId、turnId、response、usage。不要假设两者事件格式相同，也不要用 zcode projection.turnCount 推断累计历史。
3. 轮次单写者，原生状态留在节点；平台保存任务、轮次、结果关联，不靠拼接全部旧提示词模拟会话。每轮进程回收与会话记录保留分开管理。
4. 权限能力独立建模。本轮 zcode edit 可以写文件但 Bash 无权限客户端；自动编写、运行测试之前必须补验其权限交互/所选模式，不能默认升级成 yolo。CLI 退出0、JSON解析成功、agent工作成功应分开判定，优先使用可核实的工具/测试结果。
5. 后续需要流式事件、交互授权或在途取消时，再分别评估两个产品的 app-server；本轮未实测 app-server，不能据此宣布协议适配完成。

## 验收边界

本轮满足“两个指定 CLI 正常退出后，明确原生 ID 的两轮续聊”本机探针；不是公司到家庭双机验收，也不是完整编写—审查循环。未来 S-01–S-10 不整项标绿。SPEC DoD1–10 本轮未重新执行构建/demo，之前文档轮的回归不能当作本轮新证据；DoD11 未执行。只新增测试证据和更新文档，无运行代码变更。
