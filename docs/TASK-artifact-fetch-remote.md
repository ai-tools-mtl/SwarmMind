# TASK: 产物挂载跨机取回（P4④第二子轮，artifact_fetch Remote 挂载）

> 编排：Hana，2026-10-24。真相源：docs/FILE-COLLAB.md 第三节、SPEC 4.12、README Roadmap P4④。
> 上一子轮：249941b（同机纯复制挂载骨架，InputResolver 接口缝已留）。

## 一、背景与目标

产物挂载第一子轮已交付同机路径：node 执行准备阶段（started/spawn 前，独立超时），LocalResolver 扫描本机账本+outputs 目录，纯复制+SHA-256 校验+原子发布（journal 事务），失败 `input_prepare_failed` 不进入执行。

本子轮补齐跨机场景：**下游任务的某上游 completed 在另一台 node 上时，自动经 hub 中继取回产物字节并完成挂载**，挂载语义（SHA 校验/原子发布/失败回滚）与同机路径完全一致。

## 二、已有设施（复用，禁止重造）

- 协议帧已存在：`MsgArtifactFetch`（提交方→hub，请求第 index 个产物，hub 中继到执行 node）、`MsgArtifactData`（执行 node→请求方经 hub 中继，产物内容）。见 internal/protocol/envelope.go。MCP 取产物工具已在用这条通道。
- `internal/node/inputs` 包：`InputResolver` 接口、`Prepare` 挂载事务、journal 崩溃恢复、`UpstreamInputs` 结构。
- 上游定位：账本内最早 completed 口径（与 hub resolveIDem 一致），LocalResolver 已实现解析，Remote 场景需知道上游落在哪个 node。

## 三、交付范围

1. **RemoteResolver**（InputResolver 第二实现）：
   - 判定上游任务不在本机（node_id 非本机或账本无此任务但 hub 有）→ 经现有 artifact_fetch 通道向 hub 发起取回，逐件收取 MsgArtifactData 到暂存。
   - 取回后的字节进入与 LocalResolver 相同的挂载事务（校验 SHA → 暂存 → 原子发布），**复用 Prepare 现有事务代码，不另起炉灶**。
   - `UpstreamInputs` 需兼容远端形态（无本机 workdir，产物为字节流或暂存路径），改造时保持 Local 路径行为零变化。
2. **装配切换**：按上游任务归属自动选择 Local/Remote resolver；上游同机走 Local（现状不变），异机走 Remote。
3. **失败与离线语义**（FILE-COLLAB 契约）：
   - 源 node 离线/不可达 → 准备阶段内可重试轮询（间隔自定，建议 5-10s），独立超时（沿用 inputs.DefaultTimeout 或 Config 覆盖口径，可放宽 Remote 默认值并注明理由）穷尽后 `input_prepare_failed`，不 spawn。
   - 上游产物过保留期已被清理（fetch 404）→ 明确错误文案，不静默跳过。
   - 限额沿用：≤8 件、单件 ≤640KiB（协议帧 1MiB 上限内）。
4. **测试**：
   - 单测：Remote resolver 各失败路径（离线/超时/404/SHA 不符/超限额），mock 中继。
   - 集成测试：双 node 场景（可用进程内双 client 或测试 hub），上游 A node completed → 下游 B node 自动取回挂载，SHA 一致断言。
   - 全量回归：所有包 -race 绿，GOOS=windows 交叉编译不回归。

## 四、已知陷阱（项目经验清单，AGENTS.md 同步版）

- 状态文件必须原子写（temp+rename），禁止直接覆盖写。
- 禁止引入 SPEC 第三节白名单外依赖。
- 占用窗口从 offered 起算（不只算 running）——若涉及 workdir 互斥交互注意此口径。
- 上游 completed ≠ 产物目录仍在（保留期以 result_ack 起算）——fetch 失败要区分「离线暂不可取」与「已清理」。
- journal 事务崩溃恢复已存在，Remote 路径不得绕过事务直接写输入区。

## 五、验收 DoD

1. 跨机挂载后下游 inputs/<上游TaskID>/ 内容与上游 outputs/ 逐字节一致（SHA-256 断言）。
2. 源 node 离线：准备阶段重试后在超时内完成（node 回来）或超时 input_prepare_failed（node 不回来），两种路径都有测试。
3. Local 路径零回归（现有 inputs 包测试全数通过，无行为变化）。
4. 全量 -race 绿 + windows 交叉编译过。
5. SPEC 4.12 增补跨机取回段（帧流向/超时/离线语义），README Roadmap 勾选 P4④ 第二子轮（交付时我来做，你不用动 README）。

## 六、边界（本任务不做）

- 分块传输（V1 明确不做，640KiB 单帧足够）。
- 群附件、Git 集成、压缩打包。
- MCP 工具新增（artifact_fetch 能力 MCP 侧已覆盖；如 SPEC 六.六映射表需补行，在交付说明里列出建议文案，由 Hana 裁决）。
