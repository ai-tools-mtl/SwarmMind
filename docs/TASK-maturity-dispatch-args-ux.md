# TASK-maturity-dispatch-args-ux：派发页参数输入与首跑契约

## 背景（走查实证）

1. 派发页（hub app.js）只有「任务描述」输入（stdin 语义），无命令行参数输入。用户照 README 派 echo 填「hello from web」：描述走 stdin 而 echo 不读 stdin，**人生第一个任务结果为空**（codex 走查 P0 实证）。
2. 工具型 agent（rsync/ffmpeg/cat/echo，吃 args）在旗舰派发页无路可走，只能绕去 CLI（zcode 走查 P1）。
3. 协议层已有缓解机制 `stdin_to_arg`（types.go:75，2026-10-23 立）：stdin 整体转单个尾参；但这是 per-agent 配置，用户不知道、表单不提示。
4. 事实边界（codex 审查必修④纠错，初稿表述有误）：node 控制台**已有本机派单表单**（`internal/node/web/app.js` 「派任务」卡，任务描述走 stdin 语义），并非「无派单表单」；派发页参数输入增强本批 **hub 先行**，node 端表单的 args 输入增强属**后续批次**（SPEC 六.五合理差异白名单已按此登记）；node 本机 API 只能派给本机（SPEC 六边界锁）。

## 目标与改动面

1. **hub 派发页按能力呈现输入语义**（app.js）：选中执行者后读取能力卡（agents 目录 API 已返回 card：`allow_args`、`stdin_to_arg`、`description`）：
   - `allow_args=true`：出现「命令行参数」输入区——**一行一个参数**（避免空格切分边界），空行忽略，提交映射 REST `input.args` 数组（字面量传递，不做 shell 引号展开，与「任务不可更改、逐参数展示」的既有语义一致）
   - `allow_args=false`：无参数输入框，显示说明「该能力未声明接受任务参数，内容将经 stdin 传递（如能力实际支持，请经 CLI/REST 以 args 传递或升级节点）」（R3 必修①改定稿）
   - `stdin_to_arg=true`：附加提示「描述将作为命令行参数传递给该能力」
   - 能力卡不可得（目录异常/离线）时回退现状（仅描述输入），不阻塞派发
2. **README 对齐**：「提交任务并查看结果」节示例改为与网页行为一致的路径（echo 带 `--arg hello` 的 CLI 示例 + 网页参数填写说明），首跑结果非空；「接入自己的 agent」节补 `stdin_to_arg` 字段说明（何时该开）。
3. **docs/INTERACTION.md**：派发页输入语义同步（若该文件有派发页描述则更新，无则不加）。
4. **SPEC 六.五**：派发页参数输入的提交语义登记（args 字面量、逐行映射）。
5. **双端口径**（codex 审查必修④纠错）：node 控制台已有本机派单表单（仅任务描述、stdin 语义），本条目不动它；hub 参数输入区本批先行，node 表单 args 输入增强留后续批次；差异登记 SPEC 六.五合理差异白名单「派发页参数输入区」项（node 补齐 UI 时该项撤销）。

## 双端形态对照表

| 项 | hub 管理台 | node 控制台 |
|---|---|---|
| 派单表单 | 有（本条目增强：+args 输入区） | 有（既有本机派单表单，仅任务描述 stdin 语义；args 输入增强属后续批次，本批不动） |
| 能力语义提示 | 按 allow_args/stdin_to_arg 显示 | 不适用 |
| shared block | 派发表单非 shared block 管辖（词典/抽屉/通知表单才是）；如改动触及 shared block 辖区则逐字节同步 | — |

## 验收 DoD（逐条可执行）

1. README 首跑路径实测：临时环境 echo（allow_args=true）→ 网页派单在参数区填 `hello` → completed 且 FinalAnswer 非空（截图留证）。
2. 参数边界实测：含空格参数（如 `hello world` 单参数）经网页提交后按单参数到达（任务详情「任务指令」区逐参展示正确）；多行参数逐行成数组；空行/纯空白行被忽略；**非空行保留原文含首尾空白**（codex 审查必修①：解析禁 trim，首尾空白属参数内容原样到达执行端）。
3. allow_args=false 能力：无参数输入框且有说明文案；REST 强行带非空 args → policy_denied（既有行为不回归）。
4. stdin_to_arg=true 能力：提示文案正确；该能力经描述区派单后 stdin 转 tail 参行为不变（integration 既有用例不回归）。
5. 智行管家（PRIME）模式切换与派发不回归（占位文案两态切换既有测试语义保持）。
6. D22 局部更新：切换执行者时仅参数区/提示区局部更新，不整层重建表单（details/焦点/草稿保持）；playwright 断言 + 截图。
7. `go build ./... && go vet ./... && go test ./...` 全绿 + `TestWebMdRender|TestWebDrawer` 双绿；断链零；零新依赖。

## 已知陷阱

- playwright 走查先等启动轮询尘埃落定（AGENTS.md）；断言选择器禁「容器.locator(祖先ID)」恒空形态。
- 长驻交互层局部刷新禁止整层 innerHTML 重建（AGENTS.md D22）。
- embed 资产改动必须重建二进制再实测（AGENTS.md）。
- args 字面量语义：不做 shell 展开/引号剥离；「含引号的参数」就是字面含引号。
- 派发页与 PRIME 态（管家切换）共用表单骨架，改动不得破坏两态占位文案切换（app.js:1917 附近既有常量）。
- 能力卡字段以 agents API 实际返回为准（card 结构见 internal/protocol/types.go AgentCard），禁止前端硬编码能力属性猜测。
- 版本纪律：不 commit；最终输出列改动文件清单与 DoD 逐项结果。

## 修复轮记录（2026-10-27 codex 审查退回 → 本轮逐条落实）

- 必修①：`parseCtrlArgs` 去 trim（`map(l=>l.trim())` 会改写用户参数，违背字面量传递）——仅以空白判断过滤空行/纯空白行，非空行保留原文含首尾空白；`args-submit-mapping` 场景补 padded 边界断言（`  a b  ` 原样进 POST body）+ 源码锚点锁改判据并加「禁 trim」反向锁。
- 必修②：混跑兼容声明不成立（codex 旧版二进制实跑：新 hub+旧 node 目录显示 false 但携 args 执行成功；旧 hub+新 node 丢弃字段执行仍成功）——types.go 与 SPEC 六.五改写为「解码双向兼容、能力发现降级」口径：缺字段解码的 false 只是呈现值不代表实际配置，不得把未知宣称为不接受参数；补升级顺序建议（先升 node 或同步升级，混跑期传参任务走 CLI/REST 直派）。
- 必修③：TESTPLAN TC-09 安全边界断言同步（其余执行配置键仍禁；allow_args/stdin_to_arg 改为必须在场且布尔，jq 管道加括号防优先级架空断言）；附录新增 TC-DSP-01/02 派发页参数输入专项（三态呈现/D22 局部更新/字面量边界落库逐字核对/stdin_to_arg 端到端）+ fixture 增 qa-conv（allow_args+stdin_to_arg 双开）。
- 必修④：本任务书「node 控制台无派单表单」为事实错误（node/web/app.js 既有本机派单表单），背景 4/目标 5/对照表三处改为真实口径：hub 先行增强 args 输入，node 表单增强属后续批次，SPEC 白名单已按此登记。
- 建议⑤：参数区刷新合并单入口（`ctrl-agent` change 专用分支与 bindAgentPicker 回调同次切换双重刷新，删前者）；`paintArgsZone` 内容无变化跳过（innerHTML 先比后写）。
- 建议⑥：MCP 两处（hub/node 模式）「描述可转尾参」补「（仅未填 args 时）」；SPEC 六.六 swarmmind_agents 映射行补输入语义说明。
- 建议⑦：SPEC 六 /api/v1/agents 行「绝不包含 ExecProfile 字段」改为「不包含本机执行细节（command/workdir/agent_args/timeout_sec/allow_env/config_home），公开声明投影（如 allow_args/stdin_to_arg）除外」；directory 行与 view.go/state.go 注释同步；card 语义测试泄露清单补 timeout_sec。

## 修复轮记录（2026-10-27 codex R2 复审退回 4 必修+2 建议 → R3 逐条落实）

- 必修①：混跑声明残余矛盾清零（codex R2：新 hub+旧 node 实跑 false/网页提示不接受但带参任务成功，与 239/549 旧文矛盾）——SPEC 4.9 StdinToArg 注释删「与旧版 ExecProfile 默认一致，不误导」改为「字段缺失呈现值、不代表实际配置、只影响输入面呈现、不把未知宣称为不接受」；SPEC 六.五 allow_args=false 条与前端 `ARGS_NONE_NOTE`（app.js）统一降级措辞「该能力未声明接受任务参数，内容将经 stdin 传递（如能力实际支持，请经 CLI/REST 以 args 传递或升级节点）」，并写明「呈现值 false 不构成拒绝依据（旧卡缺字段携 args 照常执行；新卡真 false 仍 policy_denied）」；dispatch_test.go 锚点、dispatch_suggest_behavior_test.js 断言、TESTPLAN TC-DSP-01 文案断言同步；card_input_semantics_test.go 失败消息「旧 hub 解码亦不误导」残余同口径清除。
- 必修②：TESTPLAN TC-09 禁键正则补 `timeout_sec`（禁键集与 internal/node/service/card_input_semantics_test.go 泄漏名单六键一致：command/workdir/agent_args/timeout_sec/allow_env/config_home），期望结果列点名全集与出处。
- 必修③：TC-DSP-01 三处机械执行性——①补先 `selectOption(E.N1)` 于 #ctrl-node、agent 选项值改裸能力名（#ctrl-agent option 值=node 内 agent 名，app.js agentOpts 口径；共四处）；②捕获批次应答改先 `route.fulfill({response})` 后 `response.dispose()`（dispose 先行=disposed 错误）；③final_answer 期望按 /bin/echo 真实行为改三空格（参数单空格 join，首参尾部两空格+join 一空格，od -c 实证）。
- 必修④：TC-DSP-02 依赖的两个 helper 阻断——request() 的 `env="${5:-{}}"` 改空串默认+显式赋值（bash 解析 `${5:-{}}` 为默认值 `{` + 字面 `}`：显式传 `{}` 实得 `'{}}'` jq 拒绝，bash 3.2/5.x 实证）；wait_task() 的 `limit` 与 `stop` 拆两条 local（同一命令全部词先于 local 赋值展开，同行引用 limit 在 set -u 下 unbound，实证）。同类形态 mcp()/tool()（TC-47/49/50/52 显式传参即触发 `'{...}}'`）同法一并修复（R3 同类延伸，非 codex 点名项）。
- 建议⑤：dispatch_test.go 「禁 trim」反向锁改正则 `\.map\(\s*l\s*=>\s*l\.trim\(\)\s*\)` 容空白，覆盖紧凑变体 `.map(l=>l.trim())`。
- 建议⑥：view.go:31 DirectoryEntry 注释「AgentCard 本体不含 ExecProfile 字段」改为投影例外口径（卡不包含本机执行细节六键——公开声明投影 allow_args/stdin_to_arg 除外，同 SPEC 六 /api/v1/agents 行与 view.go:11/state.go:25）。
- R3 顺带发现未修（报请 R4 裁决）：TC-40（旧批次 Web 控制批次故障注入用例，:835 附近）与现派发表单形态脱节——`selectOption(E.N1 + "/qa-cat")` 复合值与现裸能力名 option 值不符，且 `input[data-target=…]` 复选框在现表单已不存在（两级下拉取代），整条用例需按现形态重写，超出本轮 6 条范围未动。
