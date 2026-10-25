# TESTPLAN：P5.1 D1 批——Jev 决策服务第一批（llm-as-jev + 派单页建议）

> 依据 docs/RFC-jev-dispatch.md v1.1（伦哥 2026-10-26 裁决：D1 先跑 llm-as-jev，
> GLM-5.3-Flash；②置信度显示保守处理：建议+来源标签先行）。测试先行（AGENTS 测试循环制度）。
>
> D1 验收口径（RFC 五 D1 行 + 本批任务书）：
> ① `internal/decision` 包：DecisionProvider 抽象（provider 接口+配置切换）+
>    llm-as-jev 实现（Jev 风格提示：state=任务需求+能力目录快照压缩，Choice 原语=
>    候选 agent@node 列表，输出建议+置信度+概率分布）；超时/降级：provider 不可用
>    返回无建议**不阻塞派单**（决策是建议非必经，RFC 三）。
> ② REST `POST /api/v1/dispatch-suggest` {goal|step_desc} →
>    {suggestion:{target_agent,target_node}|null, confidence, probabilities,
>    provider, degraded, note, reason}；六.六同批：MCP `swarmmind_dispatch_suggest`
>    + 映射表行 + 实测。
> ③ 管理台派单页「建议派给 X（本地决策模型）」一键采纳（人话文案，建议可覆盖不
>    强制；无建议时按钮不出现）。
> ④ 中文实测：构造 20+ 真实场景 state 走真实 GLM API 测准确率，报告落本文件 E 节。
>
> 执行方式：`go test ./internal/decision/ ./internal/hub/api/ ./internal/hub/web/
> ./cmd/swarmmind-mcp/ -race -count=1`；全量回归 `go test ./... -race -count=1` +
> 三平台 build（darwin/linux/windows）+ `go vet ./...`。
>
> 约束：hub 内嵌、零新依赖（SPEC 三白名单纪律）；GLM key 从 hub.yaml decision.api_key
> → 环境变量 GLM_API_KEY → swarm-prime-home（`.zcode/v2/provider_config.json` 的
> providerRules[].config.access.apiKey，参照 PRIME 链路既有做法）三级解析，不硬编码；
> 他会话文档批（docs/ 既有改动、README、deploy/prime/README.md）禁碰，SPEC.md 六.六
> 映射行随批补（AGENTS 同步开发规则①）。

## 设计定案（实施与评审共同基准）

- **Provider 抽象**：`decision.Provider` 接口 `Name() string` +
  `Suggest(ctx, Request) (*Suggestion, error)`；`Request{Goal, Candidates}`，
  `Candidate{NodeID, Agent, Description, Kind, PermissionMode, Inflight}`；
  `Suggestion{TargetNode, TargetAgent, Confidence, Probabilities, Reason, Provider, Model}`。
  Service 持 Provider + 调用预算（超时 context），**验证 choice ∈ 候选**（越权目标
  不采信，降级处理）。D1 仅 Choice 原语；Noul/Score 与多问题并行属 D3，不预占。
- **llm-as-jev**：OpenAI 兼容 `POST {base_url}/chat/completions`（GLM coding 端点
  实测同形），system 提示词固定 Jev 范式（不做长推理、直接输出唯一 JSON：
  `{"choice":"agent@node","confidence":0-1,"probabilities":{候选:概率},"reason":"一句话"}`；
  无合适候选 choice="none"）；user 消息为压缩 state JSON（goal + 候选列表：id/
  描述截断 80 字/kind/权限/inflight）。解析容错：剥 markdown 围栏、截取首尾大括号、
  choice 宽松 trim；choice 不在候选集即视为本次无建议。
- **降级路径（RFC 三「建议非必经」的落地口径）**：REST 端点对 provider 的一切失败
  （未启用/key 缺失/超时/HTTP 非 200/解析失败/choice 越权）一律 **200 + suggestion:null
  + degraded:true + note 人话原因**，绝不 5xx、绝不阻塞派单；仅请求体非法 400、
  未登录 401。网内无在线候选同样 200 + suggestion:null（note 区分）。
- **key 三级解析（不硬编码）**：hub.yaml `decision.api_key` → 环境变量 `GLM_API_KEY`
  → `decision.prime_home`（空回落 `SWARM_PRIME_HOME` 环境变量，再回落
  `$HOME/swarm-prime-home`）下 `.zcode/v2/provider_config.json`：
  `config.providerConfigRules.providerRules[]` 中取含非空 `config.access.apiKey` 的
  规则（优先 providerId 含 glm / baseUrl 含 bigmodel），apiKey 与 `config.api.baseUrl`
  一并取用。解析仅启动一次并记日志（来源类别，不落 key 值）；读不到 → 决策服务
  置为未启用（端点仍 200 降级，不 crash）。
- **候选来源**：hub 自身目录真相源——`Core.ListDirectory()`（仅 exposed）∩
  `ListNodes()` 在线节点，inflight 随 NodeEntry 合并；REST/MCP 请求不带候选参数
  （单一天真相，防调用方伪造候选集）。管理台派单页同源。
- **UI（hub 控制台派单页独有，node 不加、shared block 不触碰）**：任务描述框下
  建议区——入口「获取派发建议」→ 请求期间「决策模型思考中…」→ 有建议出现一键
  采纳按钮「建议派给 <agent> @ <node>（本地决策模型）」（title 悬停带一句话理由；
  置信度百分比按 v1.1 裁决②保守不上墙，数据在 REST/MCP 透传）；点击采纳=回填
  两级下拉，**可覆盖**（采纳后照常改选、改描述）；无建议/降级时采纳按钮不出现，
  留一行人话说明。表单视图轮询不重绘纪律沿用（pollTick control 豁免），建议状态
  存 S.C（freshControl 扩展字段），重绘后恢复。
- **MCP（hub 模式专属，node 模式不装配不静默降级）**：`swarmmind_dispatch_suggest(goal)`
  → 人话文本：建议（目标/置信度/来源/一句话理由/候选概率）或「暂无建议：<原因>，
  可直接手动派单」。node 模式同 work_* 口径不装配。
- **SPEC 六.六**：映射表补 `swarmmind_dispatch_suggest` 行 + 同步声明规则 13
  （D1 批口径：REST 载体+MCP 同批；决策建议非编排写操作，PAT/会话同权可调）。

## A. decision 包单测（go test，可机械执行）

执行：`go test ./internal/decision/ -race -count=1`

- **A1 Service 建议通道（happy path）**：mock provider 返回候选内 choice →
  Service.Suggest 原样透出（target/confidence/probabilities/reason/provider）。
- **A2 无候选短路**：Candidates 为空 → 不调 provider（mock 记数=0），返回
  无建议 + note；provider 为 nil 同样安全降级。
- **A3 降级不阻塞**：mock provider 返回 error / 返回越权 choice（不在候选集）/
  sleep 超过预算（短预算注入，如 30ms 预算 + 200ms provider）→ 三路均返回
  无建议 + degraded 语义标记（错误吞入 note，不上抛 panic）。
- **A4 llm 请求形态**：httptest 假 GLM（OpenAI chat completions 形）断言：POST
  路径、Authorization Bearer、body 含 model/messages[0].role=system、state 内
  goal 与候选 id 齐全；应答返回带 markdown 围栏的 JSON → 正确解析出 choice/
  confidence/probabilities/reason。
- **A5 llm 解析容错表**：纯净 JSON / ```json 围栏 / 前置说明文字+JSON / choice
  带首尾空白 / probabilities 缺失（回落 {choice: confidence}）/ 概率和≠1（重归一）/
  confidence 越界钳制 [0,1] / choice="none" → 无建议；完全非法文本 → error。
- **A6 key 三级解析**：临时目录伪造 prime-home（`.zcode/v2/provider_config.json`
  按实机 shape：providerRules[].config.access.apiKey + api.baseUrl）→ 命中；
  api_key 配置优先于环境变量、环境变量优先于 prime-home；三级皆空 → 未启用；
  prime-home JSON 非法/文件缺失 → 未启用（不 panic）；providerRules 多条时优先
  glm/bigmodel 规则。
- **A7 概率归一**：probabilities 含候选外键剔除、负值剔除、全空回落、重归一后
  和=1（浮点容差）、键序不影响结果。

## B. REST 端点（go test，可机械执行）

执行：`go test ./internal/hub/api/ -race -count=1 -run TestDispatchSuggest`

- **B1 401**：未登录 POST /api/v1/dispatch-suggest → 401（同管理面既有口径）。
- **B2 400**：goal 与 step_desc 均空/缺 body → 400 invalid_request。
- **B3 happy path**：fixture 种两台在线 node 各 exposed 能力（RegisterNode +
  SetOnline），Decision 接 mock provider（返回候选内目标）→ 200：suggestion.
  target_agent/target_node 与 mock 一致、provider 标注、confidence/probabilities
  透传、degraded=false；请求体 step_desc 别名同样受理。
- **B4 降级 200**：mock provider 返回 error → 200 + suggestion:null +
  degraded:true + note 非空（决策模型不可用人话）；**不断言 5xx**。
- **B5 无候选**：空目录 → 200 + suggestion:null + note 指明网内无在线能力候选；
  仅有离线节点目录 → 同样无候选。
- **B6 未启用**：Config.Decision=nil → 200 + suggestion:null + note 决策服务
  未启用（端点常在，前端无需探活）。
- **B7 真实 llm 适配（可选守卫）**：`SWARM_DECISION_LIVE=1` 且 key 可解析时才跑
  （CI/常规全量跳过）——归 E 节实测覆盖。

## C. MCP 工具（go test，可机械执行）

执行：`go test ./cmd/swarmmind-mcp/ -race -count=1 -run TestDispatchSuggest`

- **C1 装配**：hub 模式 tools/list 含 `swarmmind_dispatch_suggest`（描述面向
  agent 写「能做什么」）；node 模式 tools/list 不含（hub 专属，同 work_* 口径，
  不静默降级）。
- **C2 建议应答**：fake hub（httptest）返回 canned REST 应答 → tools/call 文本
  含「建议派给」、目标 agent@node、置信度、来源标签、理由；isError=false。
- **C3 暂无建议**：fake hub 返回 suggestion:null + note → 文本含「暂无建议」+
  原因 + 「可直接手动派单」指引；isError=false（降级不是工具错误）。
- **C4 参数校验**：goal 缺失/空白 → isError=true 人话提示。
- **C5 hub 不可达**：连接拒绝 → isError=true（网络故障是工具错误，与「无建议」
  语义分界）。

## D. 管理台（go test 断言 + headless 走查）

- **D1 web 包来源锁**：app.js 含建议区挂点（ctrl-suggest / ctrl-adopt 动作、
  /api/v1/dispatch-suggest 调用、「建议派给」「本地决策模型」人话文案、采纳后
  可覆盖的选择回填路径）——`go test ./internal/hub/web/ -run TestConsoleDispatchSuggest`。
- **D2 shared block 零触碰**：双端 app.js shared block 标记块逐字节比对不变
  （`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿 +
  diff shared block）——本批不碰 shared block，该回归证明这一点。
- **D3 行为走查（headless，随实施后人工/harness 验）**：登录 → 派单页 → 填任务
  描述 → 「获取派发建议」→ 出现「建议派给 X（本地决策模型）」→ 点击采纳 →
  两级下拉回填 X → 手动改选覆盖建议 → 提交照常；停决策服务配置（provider=off）
  重启 → 建议区仅入口与「未启用」说明、无采纳按钮。

## E. 中文实测（真实 GLM API，报告落本节）

守卫：`SWARM_DECISION_LIVE=1 go test ./internal/decision/ -run TestLLMLiveAccuracy
-v`（无 key / 无守卫自动 skip，不进常规全量）。设计：

- 22 个真实中文场景（覆盖：编码类×多 agent 竞争、巡检/运维脚本类、跨机同名能力
  负载分派、描述模糊需按能力描述判断、工具 vs 智能体之辨、无需拆解的单点任务、
  诱导性干扰描述）；每场景 3~6 个候选（在线 agent@node + 能力描述 + inflight），
  标注唯一正确答案（构造期保证可判定）。
- 走真实 GLM API（llm-as-jev provider，key 走 swarm-prime-home 解析链）。
- 产出：逐场景 PASS/FAIL 表 + top-1 准确率 + 延迟（端到端 ms）+ 概率分布样本；
  结果追加在本节（报告落 TESTPLAN，任务书口径）。
- 验收线：top-1 准确率 ≥ 80%（20+ 样本下 ≥ 18/22）；达不到 → 记录失败形态
  （提示词措辞敏感/候选描述质量），提示词迭代一轮后复测并双份留档。

## F. 回归与收尾（DoD）

1. `go build ./...` 过；`GOOS=linux GOARCH=amd64 go build ./...`、
   `GOOS=windows GOARCH=amd64 go build ./...` 过（本机 darwin/arm64 全量 test）。
2. `go test ./... -race -count=1` 全绿。
3. `go vet ./...` 干净。
4. SPEC 六.六：映射表 `swarmmind_dispatch_suggest` 行 + 同步声明规则 13 在案。
5. key 不落日志、不落应答（grep 审查：decision 包日志仅来源类别与 base_url/model）。
6. 他会话文档批零触碰（git status 对照：本批新增/改动仅 TESTPLAN-D1-jev.md、
   SPEC.md 六.六、internal/decision/、internal/hub/api/、internal/hub/web/app.js、
   cmd/hub/main.go、cmd/swarmmind-mcp/、configs/hub.example.yaml）。

## 执行记录（实施后追加，2026-10-26）

- [x] **A 组**：`go test ./internal/decision/ -race -count=1` 全绿——A1 happy path / A2 nil 与零值未启用（NotEnabledNote 口径）/ A3 降级四路（provider 错误、越权目标、30ms 预算硬超时（goroutine+ctx 双保险按时返回）、choice=none 正常无建议非降级）/ A4 请求形态（POST /chat/completions、Bearer、system+user 消息、state 含 goal 与候选 id）/ A5 解析容错表（围栏/前置杂文/空白 choice/none/空 choice/纯文本/截断 JSON）/ A6 key 三级解析（GLM 规则优先、逐级回落、缺失/非法不命中）/ A7 概率收敛（P(choice)=conf 校准口径：choice 占 conf、其余候选按原比例归一到 1-conf；conf 缺失回落 probabilities[choice]；全空回落 choice=1）。
- [x] **B 组**：`go test ./internal/hub/api/ -run TestDispatchSuggest -race -count=1` 全绿——401 / 空参与空白 400 / happy path（step_desc 别名同参受理）/ 降级 200+degraded+note（provider=mock-decider 标注）/ 无候选 note / 未接线 200+未启用 note / 离线 node 与 exposed=false 能力不入候选且候选序稳定（echo@mac-mini → diskcheck@win-box）。
- [x] **C 组**：`go test ./cmd/swarmmind-mcp/ -race -count=1` 全绿——hub 模式装配（含描述语义断言「建议不是必经/可改派」）、node 模式不装配 / 建议人话应答（目标/置信度 87%/来源/理由/概率分布/覆盖指引；请求体只带 goal 不带候选）/ suggestion:null → 「暂无建议+swarmmind_submit 指引」isError=false / 参数校验 / hub 不可达 isError=true / 未配置 hub 人话提示；TestHubToolListRegression 清单锁同步 19 工具。
- [x] **D 组**：D1 web 来源锁 `TestConsoleDispatchSuggestSources` 绿（端点/动作/容器/渲染函数/人话文案/回填路径九锚点）；D2 shared block 零触碰——`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿（本批未改 node 端任何文件）；**D3 行为走查（真实 hub+真实 node+真实 GLM，headless Chromium）**：
  - 派单页建议区四态齐备：入口（获取派发建议）→ 思考中 → 「建议派给 translator @ d1-node（本地决策模型）」+ 一句话理由（title 悬停）+ 重新获取；
  - 点击采纳 → 两级下拉回填 d1-node/translator、摘要行「将在 d1-node 上用 translator 执行」、任务描述保留、toast「已采纳建议…（提交前可自行调整）」；截图留档；
  - **建议可覆盖**：采纳后手动改选 coder，摘要行同步更新；
  - 无建议时采纳按钮不出现；空目录/未启用走 note 人话转述；
  - **走查抓出并修复一处真 bug**：`button()` 助手生成的建议区按钮缺 `type="button"`，在表单内默认 submit——点击「获取派发建议」连带触发整表提交（toast「请先选择执行机器」）。修复后空描述点击 toast 正确为「请先填写任务描述，再获取派发建议」，无表单提交副作用。
- [x] **E 组中文实测**：见下节报告。
- [x] **F 组回归**：`go build ./...` 过；darwin/arm64 + linux/amd64 + windows/amd64 三平台 build 过；`go test ./... -race -count=1` 21 包全绿；`go vet ./...` 干净；SPEC 六.六映射行+同步声明规则 13 在案；密钥不落日志/应答（启动日志仅记来源类别 env/config/prime-home + 端点 + 型号，grep 审查过）；git status 对照——本批改动仅 SPEC.md、configs/hub.example.yaml、cmd/hub/main.go、cmd/swarmmind-mcp/{main,mcp_integration_test,dispatch_suggest_test}.go、internal/hub/api/{server.go,dispatch_suggest*.go}、internal/hub/web/{app.js,dispatch_test.go}、internal/decision/、docs/TESTPLAN-D1-jev.md，他会话文档批（README、deploy/prime/README.md、docs/ 既有改动、internal/hub/core/work_dispatch.go）零触碰。

### E 组：中文实测报告（真实 GLM API，llm-as-jev）

环境：真实 GLM coding 端点（glm-5.3-flash，key 经 swarm-prime-home 解析链注入）、22 个真实中文场景（编码/巡检/备份/评审/翻译/报表六类 + 同名能力负载分派两例 + 干扰描述一例），守卫 `SWARM_DECISION_LIVE=1 go test ./internal/decision/ -run TestLLMLiveAccuracy -v`（常规全量自动 skip）。三轮迭代：

| 轮次 | 配置 | top-1 正确 | 无建议/链路错误 | 答错 | 平均端到端延迟 |
|---|---|---|---|---|---|
| 1 | 预算 8s，思考模式默认（enabled） | 18/22（82%） | 4（全部为 8s 预算超时） | **0** | 5.48s |
| 2 | 预算 15s，思考模式默认 | 20/22（91%） | 2（一例 content 空、一例 JSON 截断） | **0** | 5.29s |
| 3（终版） | 预算 15s + `thinking:disabled` + max_tokens 2000 | **22/22（100%）** | 0 | **0** | **1.98s** |

结论与发现：

- **22/22 全对（验收线 ≥80% 达成）**，且三轮均零答错——失败模式全是链路层（超时/空应答/截断），无一是判断错误。
- **根因发现（本轮最有价值的产出）**：GLM-5.3-Flash 是混合推理模型，默认思考模式会把 token 烧在 `reasoning_content` 上，导致 `content` 空应答或 JSON 被截断；探针实测 `thinking:{"type":"disabled"}` 后应答干净（25 completion tokens 出完整 JSON）且**延迟从 5.3s 降到 2.0s**——Jev 范式「系统一判定不做长推理」的提示词意图，需要模型侧思考开关配合才能真正落地（单靠提示词压不住）。该发现随 `chatThinking` 字段固化进 llm.go，注释留档。
- 预算定案 15s（DefaultBudget）：`thinking:disabled` 后实测 1.7~2.4s，15s 为保守尾部余量；超时降级不阻塞派单，仅是人多等一会。
- 校准概率样本（场景「C盘清理」）：diskcheck@win-box 0.98 / ops-check@mac-mini 与 backup-tool@mac-mini 合计 0.02——区分度与直觉一致；百分比对用户展示仍按 RFC v1.1 裁决②保守不上墙，REST/MCP 透传待后续裁决。
- 局限声明：场景目录为单一 node 三能力 + 双节点同名两类拓扑，未覆盖跨机大目录（>32 候选被 stateCandidateCap 截断的形态）；同义改写鲁棒性（对措辞敏感是 RFC 一已列的 Jev/LLM 共性局限）未系统测量，D3 本地部署对照时补。

---

# 审查节（codex，2026-10-26 04:00，必须修 4 + 建议修 3）

1. **[P2] 概率归一缺口+凭空 100%**（decision.go:179）：两候选 confidence=0.6 概率仅含所选目标时总和 0.6；模型省略概率/置信度被补成 100%。→ 明确缺失值策略：缺失概率按均匀分布补齐至合法分布；confidence 缺失不得补 1.0（以 0.5+「低置信」标记或直接降级无建议）。
2. **[P2] 旧建议可采纳**（app.js:3300）：响应未绑任务描述版本，改描述后旧建议仍可采纳（已复现翻译→改编译修复→仍采纳 translator）。→ 建议绑描述指纹，描述变化即失效+过期响应丢弃。
3. **[P2] 准确率测试假绿**（llm_live_test.go:115）：选错只 Logf，最终只查调用数——0% 准确率也 PASS；key 未命中 SKIP 不能确认 22/22。→ 硬断言 ≥18/22 答对；逐场景结果落报告。
4. **[P2] 上游错误原文泄入管理面**（llm.go:143→decision.go:128 note）：上游回显凭据/请求内容会泄入 REST/MCP/UI，截断≠脱敏。→ 固定人话错误类别，原文只进 hub 日志；补防泄漏断言。
5. **建议**：llm_test 锁 thinking.type=disabled；dispatch_test 补行为断言（旧建议误用/按钮误提交拦截）；SPEC 规则 13 degraded 实现/文档对齐；RFC 50 例→22 例变更依据写明；候选 >32 截断给可追踪标记或说明。

（修复闭合记录由实施方追加）

## 审查闭合记录（2026-10-26，实施方逐条修复）

**必须修 ①（P2 概率归一缺口+凭空 100%）——已闭合**：`normalizeProbs` 重写（internal/decision/decision.go）：

- choice 概率恒等于 confidence；conf 缺失回落 `probabilities[choice]`；两者皆缺按**均匀分布 1/n 兜底**——均匀即天然低置信，不再凭空补 1.0（单候选时 1 是唯一合法分布，属事实非造假）；
- 其余候选缺失（键缺失/非正数/候选外）按**显式权重均值填补**（无任何显式信号则 (1−conf) 内均匀）——codex 复现形态「两候选 conf=0.6 概率仅含所选目标」现输出 {目标 0.6, 另一候选 0.4}；分布恒覆盖全部候选且和为 1（浮点容差）；
- 附带堵住旧暗门：conf 缺失且概率无 choice 键时旧实现产出 **0 置信建议**（choice 概率 0 仍当选），现同走均匀兜底。
- 测试：`TestNormalizeProbs` 扩至 9 子例（复现形态/全空均匀/三候选 1/3/均值填补/单候选恒 1/0 置信暗门/conf 越界/和为 1 等）；Service 层「confidence=归一后 probabilities[choice]」断言保持。

**必须修 ②（P2 旧建议可采纳）——已闭合**：建议绑任务描述指纹（internal/hub/web/app.js，三道闸）：

- 请求态：`ctrl-suggest` 发起时捕获描述存 `C.suggest.goal`；
- 应答态：`await` 返回后先比对当前描述与指纹，不符则**整轮应答作废**（建议与降级说明都不落地，转「已失效请重取」）；
- 渲染态+采纳态：`suggestLineHtml` 指纹不符不出现采纳按钮（四态扩五态：新增「建议已失效」）；`ctrl-adopt` 采纳前重验指纹，不符拒绝回填。指纹比对以 DOM 真相为准（`C.stdin` 是既有全局 input 委托的实时镜像）。
- 测试：`TestConsoleDispatchSuggestFingerprint`（三道闸源锚点锁，codex 审 ⑤「dispatch_test 行为断言」同项交付）。

**必须修 ③（P2 准确率测试假绿）——机制已闭合；终版逐场景报告待 key 复跑补录**：`TestLLMLiveAccuracy` 双硬断言——API 成功调用 <18 → Fatal（链路故障报告无效）；**top-1 正确 <18/22 → Fatal**（验收线 80%；选错/无建议均计入不合格，逐场景 Logf 只作记录不再决定 PASS）。逐场景 PASS/FAIL+延迟经 `-v` 输出即 TESTPLAN E 节报告素材。本轮执行环境 key 不可得（GLM_API_KEY 空、prime-home provider_rules 空，实测自动 skip 未烧额度）——**终版逐场景 PASS/FAIL 表待 key 恢复后复跑补录本节**，汇总口径与验收线不变；不存在凭旧数据伪报。

**必须修 ④（P2 上游错误原文泄入管理面）——已闭合**：llm.go 全部失败路径改「**原文只进 hub 日志、对外固定人话类别**」：`LLMConfig.Logf` 注入 hub 日志出口（config.go `New` 接线，nil 落标准 log）；HTTP 非 200/网络错误/应答读取失败/应答非 JSON/上游 error 字段/判定解析失败/choice 越权七路均先 `%.500s` 留痕日志，再返回固定类别错误（如「决策模型端点返回 HTTP 401」「决策模型应答不是合法判定 JSON」）；`parseDecision` 亦改固定类别（不再拼 JSON 库错误细节）。Service note（→ REST/MCP/控制台）只含类别，上游回显的凭据/请求内容无法抵达管理面。
- 测试：`TestLLMErrorNoLeak` 四子例（HTTP 401 回显凭据+goal/上游 error 字段回显/非判定 JSON 应答/choice 越权），断言 **note 不含**密钥与 goal 标记、**注入日志必须含**原文（双向证明「只进日志」）。

**建议修 ⑤——五子项全闭合**：

- **thinking 锁**：`TestLLMRequestShapeAndParse` 断言请求体 `thinking.type=disabled`（GLM 混合推理致 content 空/截断的实测根因，防回归）；
- **dispatch_test 行为断言**：`TestConsoleDispatchSuggestFingerprint`（旧建议误用三道闸）+ `TestConsoleDispatchSuggestButtonType`（建议区四按钮 `type="button"` 锁死——D 组走查实 bug「点击建议连带整表提交」的回归锁）；
- **SPEC 规则 13 degraded 实现/文档对齐**：实现语义本就分层（链路故障 `Degraded=true`；正常无建议 `Degraded=false`+note），文档错标「候选为空→degraded:true」已纠正——规则 13 与六.六映射行改为：链路故障（未启用/网络错误/超时/解析失败/目标越权）200+suggestion:null+`degraded:true`+固定人话类别；正常无建议（无在线候选/choice=none）200+suggestion:null+`degraded:false`（note 区分）；
- **RFC 50 例→22 例变更依据**：RFC 五 D1 行原文「中文 state 实测 50 例」，D1 实际 22 例。依据：场景须逐例标注唯一正确答案且拓扑可判定，D1 网内只有两类拓扑（单 node 三能力/双节点同名负载），22 例已覆盖六类任务×两类拓扑的全部可判定形态+干扰形态，再加例只能同构重复；样本 22 下验收线 ≥80% 即 ≥18 对，与 RFC 验收强度（50 例≥80%）同置信方向；50 例量级留 **D3 本地部署对照**时达成——届时跨机大目录与同义改写维度入目录，场景自然放量（E 组局限声明已列该两项为 D3 补测项）；
- **截断标记**：候选 >`stateCandidateCap`(32) 时 `buildState` 在 state 内带 `candidates_note`（模型可感知「共 N 个仅按稳定序列出前 32」），provider 侧同步日志留痕（可追踪）；`TestBuildStateCandidateCap` 锁死（截断标记在场+超限候选不入列+state 合法 JSON）。

**回归（闭合后全量）**：`go test ./... -race -count=1` 全绿；`go vet ./...` 干净；darwin/arm64 + linux/amd64 + windows/amd64 三平台 build 过；node 端 shared block 回归双绿（`TestWebMdRender|TestWebDrawer`——本批未触碰 node 端任何文件）。本批改动文件：internal/decision/{decision,llm,config}.go 及同目录 {decision,llm,llm_live}_test.go、internal/hub/web/{app.js,dispatch_test.go}、SPEC.md（仅六.六规则 13 与映射行两处）、docs/TESTPLAN-D1-jev.md（本文件）；他会话文档批与 internal/hub/core/work_dispatch.go 零触碰。

## 复审剩余意见闭合记录（2026-10-26 第二轮：2 修 + 1 挂起登记）

**复审①（P2 改描述后旧采纳按钮仍显示）——已闭合**：input 委托 `#ctrl-stdin` 分支补**就地失效转移**（internal/hub/web/app.js）：描述一改且有建议在场，立即销毁建议（与请求态/采纳态闸门同语义：指纹不符即失效，改回原文不复活）并 `paintSuggestLine()` 局部重绘——采纳按钮当场消失，不再等下一次整页渲染；失效态重试按钮文案与渲染态闸门对齐（失效说明「请重新获取」→按钮同词「重新获取」，note 态条件文案，无建议/降级仍「再试一次」）。原复现序列（翻译→改编译失败→旧按钮不残留）入行为断言（见复审②场景一）。

**复审②（dispatch_test.go:40 仍是 strings.Contains 源码检查）——已闭合**：`TestConsoleDispatchSuggestFingerprint` 重写为**行为断言**——`go test` 经 node 子进程在桩 DOM（document/window/location/fetch/事件委托全桩）里加载 app.js 全文，驱动的是真实全局 click/input 委托处理器、真实 ACT 动作与真实渲染函数（非源码匹配）；harness 与 runner：internal/hub/web/dispatch_suggest_behavior_test.js（桩+驱动，`__hs_` 前缀隔离）+ dispatch_test.go 内嵌 runner（裸 vm 按「桩+app.js+驱动」同脚本求值）。四场景：

1. **输入失效**（复审①复现序列）：翻译任务出建议（采纳按钮在场）→ 改编译失败描述 → 采纳按钮**当场**消失、失效文案在场、再次局部重绘不残留；
2. **迟到响应**：思考中改描述（不误切失效态，判定在应答门）→ 应答后到 → 整轮作废不落地 + 采纳点击不回填；
3. **点击拒绝**：绕过 input 委托直改 DOM（程序化赋值，C.stdin 镜像不知情）→ 采纳前重验以 DOM 真相为准：不回填、销毁建议、失效 toast 在场；
4. **按钮不触发表单提交**：五态（入口/思考中/有建议/已失效/降级说明）渲染产物逐态断言零缺 `type="button"` 的按钮 + 全程 fetch 留痕无 control-operations 调用（建议动作零表单提交副作用）。

**变异验证四钉死**：分别删除 input 失效转移 / 应答门 / 采纳重验门 / 采纳按钮 type 属性 → 对应场景各自转红（场景一红时呈报的正是复审①的旧按钮残留形态），还原后四场景全绿；还原只用备份 diff 核对无损。node 不可得的环境 `TestConsoleDispatchSuggestFingerprint` 自动 skip——源码锚点锁（Sources/ButtonType，ButtonType 随条件文案更新锚点）仍常驻兜底。

**复审③（GLM key 当前不可用，疑似配额限流）——挂起登记（环境依赖待办，不阻塞其余核验）**：E 组**终版逐场景 PASS/FAIL 表补录**挂起，等 key 恢复。最终验收条件唯一且已登记：key 恢复后执行
`SWARM_DECISION_LIVE=1 go test ./internal/decision/ -run TestLLMLiveAccuracy -v`
将逐场景输出补录本文件 E 节，即为终验收。汇总口径不变（top-1 ≥ 18/22 硬断言已内置——API 成功调用 <18 或答对 <18 均 Fatal，不存在假绿）；上表第三轮 22/22 为 key 可用期的历史实测，补录以复跑为准、不沿用旧数据。挂起期间其余核验（A/B/C/D/F 组 + 本轮两修的闭合）不受影响。

**回归（第二轮闭合后）**：`go test ./internal/hub/web/ -run TestConsoleDispatchSuggest -race -count=1` 三测试全绿（行为断言 ~3.5s）；全量 `go test ./... -race -count=1` + vet + 双平台 build 见本轮执行记录。本批改动文件：internal/hub/web/{app.js,dispatch_test.go,dispatch_suggest_behavior_test.js（新增）}、docs/TESTPLAN-D1-jev.md（本文件）；其余零触碰。
