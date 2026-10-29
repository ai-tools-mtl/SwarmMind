# TASK-agent-session-core（T1：会话内核与原生适配）

> 分支：feat/agent-session（基于 dev @ 73b75ef）。
> 上游方案：docs/VISION-persistent-agent-collaboration.md 实施顺序第 2 步「单 agent 持久会话」的内核部分。
> 探针证据：docs/evidence/native-session-20261029/（真实 CLI 输出，解析器测试以此为准绳）与 docs/archive/TESTREPORT-native-session-20261029.md（已验证命令形态、适配决策五条）。

## 背景

现行 Task 模型是一次性 command：runner 启动进程、关 stdin、收结果、回收进程组。用户需要的是跨轮持久的原生会话：首轮拿到原生会话 ID，后续每轮显式带 ID 续接，审查意见能回到原编写线程。本批只做 node 侧内核，不含 REST/MCP 暴露（T2）、不含界面（T3）。

已实证的 CLI 能力（不得凭想象改写，以探针为准）：

- codex-cli 0.164.0：`codex exec --json ... "$PROMPT"` 首轮，`codex exec --json ... resume "$SESSION_ID" "$FOLLOWUP"` 续轮；输出为 JSONL 事件流，`thread.started` 携带原生 thread id；探针两轮 ID 一致，暗号/工具历史/文件状态衔接。
- zcode 0.17.0：`zcode --cwd "$DIR" --mode edit --json -p "$PROMPT"` 首轮，`zcode --cwd "$DIR" --mode edit --json --resume "$SESSION_ID" -p "$FOLLOWUP"` 续轮；输出为整份 JSON 对象（非实时事件流），字段含 sessionId/turnId/response/usage；`projection.turnCount` 恒为 1，不得当累计轮数。
- zcode edit 模式 Bash 无权限客户端（工具失败如实记录在探针第二轮）：权限模式必须是适配器的显式配置字段，默认值绝不取 yolo。
- 两家退出码 0 都不等于工作成功：退出码、输出解析成功、工具/工作结果三者分离判定。

## 目标

本机 node 侧会话内核四原语：Create（首轮）/ Send（续接）/ List / Close，加上原生 ID 映射持久化与单写者轮次队列。同步阻塞式 API（一轮调用跑完一轮 CLI），turn 全程留痕；异步执行器留待 T2 随暴露面引入。

## 改动范围

1. `internal/protocol/types.go`：Session / Turn 相关契约类型（平台 session id、原生会话 id、agent 名、cwd、适配器类型、权限模式、状态、轮次摘要、时间戳）。类型只进 protocol，不带行为。
2. `internal/node/sessionx/`（新包，命名避开既有 `internal/hub/session` 节点连接语义）：SessionManager。
   - Create(agentName, cwd, prompt) → 执行首轮，从输出取回原生 ID，建立平台↔原生映射并持久化。
   - Send(sessionID, prompt) → 以存储的原生 ID 续接；返回的原生 ID 与存储不一致时判失败，绝不静默换线程。
   - List() / Status(sessionID) / Close(sessionID)。
   - 单写者：同 sessionID 的调用串行（内部队列或互斥），跨 session 并行不设限。
   - 映射持久化 `data_dir/sessions.json`：原子写（tmp+rename）、0600、格式参照 api_tokens.json 惯例；损坏/缺失按空表起步并如实记日志。
3. 适配器 `sessionx/adapter_codex.go`、`sessionx/adapter_zcode.go` + 公共接口：
   - 命令拼装严格按探针已验证形态；首/续轮差异封装在适配器内。
   - codex：逐行解析 JSONL，取 thread.started / item / turn.completed / 错误事件。
   - zcode：整份 JSON 解析，取 sessionId/turnId/response/usage；输出非 JSON 或字段缺失判失败。
   - 权限模式为配置字段（codex 对应 sandbox/approval_policy 组合，zcode 对应 --mode），默认保守值，由调用方显式给。
   - 超时沿用 context；超时判 turn 失败并如实标注「进程已终止、原生侧状态不确定」。
4. 复用 runner 的进程组管理：每轮一个进程组、轮终回收；不改动 runner 现有超时/孤儿清理语义，普通 Task 路径零触碰。
5. 单元测试（不调真实模型）：
   - fake CLI 可执行脚本分别模拟 codex JSONL 与 zcode 整份 JSON 两种输出形态（样本取自探针 evidence 的结构）；
   - 首轮建映射、续轮 ID 一致性、ID 不一致拒绝、未知 sessionID 拒绝；
   - 同 session 并发 Send 串行化（注入慢速 fake 验证互斥）、跨 session 并行不互锁；
   - sessions.json round-trip、原子写失败保留旧数据、损坏文件空表起步；
   - 退出码非 0 / 输出非法 / 超时三路失败语义。

## 边界与纪律

- 不新增 REST/MCP 契约，不动 SPEC 六.五/六.六（T2 才登记）；SPEC 本批不改。
- 不碰 hub 侧任何代码；internal/hub/、internal/notify/、双端 web 资产本批零改动。
- 不引入第三方依赖（go.mod 零新增）。
- 原生 ID 丢失/不匹配 → 显式错误，禁止静默新建线程。
- 不做 PTY 常驻、不做流式（探针决策：每轮 CLI + 显式 ID）。
- 测试用 fake CLI，绝不真实调用 codex/zcode 模型（真机实测属 T4）。

## DoD

1. SessionManager 四原语在 fake CLI 下全部可工作，测试覆盖上述全部场景。
2. 两适配器解析逻辑与 docs/evidence/native-session-20261029/ 输出结构一致（测试样本从 evidence 结构简化而来，字段对得上）。
3. 单写者测试证明：同 session 并发 Send 严格串行。
4. 持久化 round-trip + 原子写失败保留旧数据 + 损坏起步三态有断言。
5. `go build ./... && go vet ./... && go test -count=1 ./...` 全绿（22 包）。
6. 不 commit；完成后报告改动清单与验证证据，由编排方审查后执行 commit。
