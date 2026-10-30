# v3开发任务书：盘点、讨论与文档验收（2026-10-30）

范围：[任务总入口与W01–W08](../tasks/v3/README.md)。基线feat/agent-session，HEAD 527fe10c6954a447659ba0ec313afd41e8a05cd6。只写文档，未改动运行代码、凭据或部署。八本任务书加总入口共31个子任务，均明确源码现状、增量、依赖、改动范围/禁区、REST/MCP、双端形态、机械验收与交付要求。

本轮主会话实际执行 `go build -mod=readonly ./... && go test -count=1 ./...`，退出0，含sessionx/API/MCP/Web已有测试；未来产品AC与真实CLI/双机未执行。任务书不是代码完成声明。

## zcode三轮收敛

继续同一产品原生会话 `sess_ecc59d38-431a-4196-be92-09a584b532cc`，明确--resume，无latest。r6审九份候选全文，r7审修订正文，r8审最终顺序增量；均只读提示、禁Bash/Edit/Write且不运行模型探针。回复原文附后。

| 轮次 | 意见 | 处理与结论 |
|---|---|---|
| r6 | 必须修1：space_control逐动作主体未定界；建议精简W04/W03依赖、加公共本地E2E安全门，及精确依赖/旧接口占位 | 增W04逐动作表，budget/policy/finalize/archive拆主人端点，agent finish仅人预授权写审策略且验证器通过；加owner_space防会话直调绕过；同步W05/W08。其他建议逐项采纳 |
| r7 | 原必须修与建议均关闭；新发现1：未领取者不能配置成员，但原流程语序允许领取前选人 | 固定空成员external创建→先领取term→选人→就绪后启动，增W04-N10与W05 AC14一致断言；README精确W07依赖 |
| r8 | 两项核验通过，无剩余必须修 | 最终结论“可据此派发开发任务书（文档级）”；非阻塞附注已落实到v3 AC14执行顺序说明 |

每项必须修都在首次针对修订后关闭，未触及同一意见两次未解决上限。以上是方案与任务文档审查，不是开发/部署批准，不代表31个子任务已有实现。

## 独立文档验收

另开不继承开发上下文的只读会话，依TESTPLAN读取源码与九份文档；不重复主会话Go测试，不改文件、不调真实模型。初审TB01–TB05均PASS；依赖具体化、逐动作权限及顺序修订分别做增量复核，均PASS。

| ID | 结果 | 证据 |
|---|---|---|
| TB01 | PASS | T1–T3装配/API/MCP/Web已存在；Node六工具/五主人端点/prime门禁、grouphost普通Task、Work已完成锁、CleanupAcked接线等与I01–I11一致；未沿用旧记忆代当前代码 |
| TB02 | PASS | 八本均有范围/禁区/接口映射/人工豁免/双端表/验收与交付；当前与拟定API明确；授权/角色/任期独立 |
| TB03 | PASS | 31个子任务唯一；保守展开阶段门和可选集成依赖后可拓扑排序无环；普通接入不等默认管家；W03/W07先接口装配后实证不双向等待 |
| TB04 | PASS | fake、真实CLI、双机和UI分别采证；明确输入、错误反例、超时/清理/证据要求；安全门先修再跑，未运行默认外部E2E |
| TB05 | PASS | 九份任务文档45个相对链接可达；P/F/E/AC/W引用有效、31定义唯一；git diff --check通过。历史报告/旧任务保留，工作区只改文档 |
| TB06 | PASS（主会话快照回归） | 主会话实际build/test退出0。独立会话没有亲自重跑，以上验收结论不伪称两次执行 |

增量授权反查：主人凭据与PAT有别；普通session_send不能绕过绑定空间；finish默认human_only，agent不能自授；讨论定版/放弃/归档归人；领取前配置403，成员未齐派发拒绝，领取不以成员已齐为前提。

## SPEC第七节DoD逐项边界

| DoD | 本轮验证 |
|---|---|
| 1 | PASS：go build -mod=readonly ./... |
| 2 | PASS：go test -count=1 ./...，全部有测试的包通过 |
| 3 | 独立回环demo未执行 |
| 4 | 相关单测随全量通过；独立超时/进程组演示未执行 |
| 5 | 相关单测随全量通过；独立离线信箱演示未执行 |
| 6 | 相关单测随全量通过；真实kill/restart演示未执行 |
| 7 | 相关单测随全量通过；独立Hub重启演示未执行 |
| 8 | 相关单测随全量通过；独立幂等演示未执行 |
| 9 | 相关单测随全量通过；独立白名单演示未执行 |
| 10 | 相关单测随全量通过；真实kill Node核对演示未执行 |
| 11 | 真实异构双机产品验收未执行 |

v3 AC01–AC20是未来功能验收，本轮未执行；原生CLI探针/Windows真机/新空间与新MCP页面未实测。任务书全绿不能视作A/B/C/D阶段完成。没有运行真实用户配置/凭据、生产实例或外部默认BASE。

## 本轮改动清单

新增docs/tasks/v3/README.md与W01-sessions.md、W02-mcp-access.md、W03-mcp-onboarding.md、W04-spaces.md、W05-strategies.md、W06-resources.md、W07-hub-routing.md、W08-recovery.md；新增本报告。更新docs/README.md导航、docs/TESTPLAN.md专项、docs/PRODUCT-PLAN-v3.md中AC14顺序澄清。此前v2/vision/其他归档等未提交变动保留，本轮未修改其内容。

未commit、push、部署，也未实施任何待开发API。旧T1–T3已实现代码与测试继续作为基线复用。

## zcode原始审查回复


### r6

Session: `sess_ecc59d38-431a-4196-be92-09a584b532cc`; Turn: `turn_5a70c6d5-c26d-4f54-8d62-a217734a8ef6`.

**总判：九份任务书与基线盘点、15.1 阶段拆分、既有 T1–T3 边界核对一致，依赖图无环、无全局阻塞；发现 1 项必须修（仅阻塞 W04-01 定案，不阻塞 W01/W02/W03/W06 先行派发）。**

## 必须修

**1. space_control 七个动作的授权主体未逐项定界**（W04-spaces.md「拟定REST/MCP」表 + README.md §4）。白名单含 pause/resume/configure_members/set_budget/takeover/renew/finish/archive，但 README §4 只声明 pause/resume/dispatch 可由持任期管家执行，其余五个未标主体。并读之下有漏洞：v3 §6 讨论由 Host（人）定版、§11 放弃须人工填原因，而 W05 写「管家不能绕过证据未满足直接 finish」反向暗示证据满足即可由管家 finish。修法：W04-01 契约表逐动作标主体与前置（建议 finish/archive＝主人，或「策略验证器通过＋显式人工授权」；renew 限当前 term；takeover 按 F02 领取语义限待配置/管家空间；configure_members/set_budget 明确）。此表是 W04-01 定案的输入，缺它实现时会二选一：要么扩权越界、要么中途返工。

## 建议修

**2. W04-02 依赖写成 W01-04，语义指向应是 W01-03**（W04-spaces.md 子任务表、README §2.4）。其自述「不需Hub，调用W01 Node会话面」即 W01-03 的 REST 会话面；存储/串行调度不需要 MCP 装配。照抄会把 W04-02 无谓排到 W01-04 之后。两处同改。

**3. W03-02 自动写入依赖 W06-02 值得商榷**（W03-mcp-onboarding.md、README §2.3）。宿主文件位置/版本识别属 W06-01 检测域（I09 已声明 agentid 非扫描器）；登记表单与宿主探测无关。建议改依 W06-01（或 01+02），否则本机自动接线被表单排期拖后。

**4. E2E 默认外部 BASE 安全门只在 W01/W08 出现**（README.md §3、I11）。W03/W07 的 web 走查同样可能触雷，建议在 README §3 共同约束加一行「隔离 UI/E2E 一律显式本地 BASE，缺省目标零网络退出」，避免包间执行者漏读 W01。

## 可不修 / 已核可项

- **依赖无环**：W05-01 只依 W04-01、W04-03 反依之的循环已在两文件显式拆解；W08-01 前置接口约束不构成环；W02-01 与 W01-03 双向依赖已正确断开。
- **现状衔接诚实**：node MCP 六工具、authOrchestrate 仍查 prime_agent、grouphost 普通 Task 接力、Work done 锁定、agentid 非扫描器、CleanupAcked 已接线——各任务书与审计基线逐条吻合，无冒称已具备；旧 declare 换发共享密钥的副作用兼容已列入 W02-01 逐条迁移。
- **豁免纪律**：W01 取消/关闭、W02 access/rotate、W03 preview/apply/revert、W06 discover/probe 均主人专属并逐端点登记；agent 四工具不含隐式 cancel/close；control 不间接提权——与 v3 人工专属边界一致。
- 两处小项自行斟酌：W05 Node 端 dispatch-suggest 在未配 provider 时建议经 capabilities 标缺而非空响应；W02-03 的「中心 PAT→P07 入口链接」实际依赖 W03-04，建议在 W02-03 标注为占位。

**结论**：修掉第 1 条（W04-01 补逐动作授权表，文档级改动）后，本目录可据以拆发开发任务；第 2–4 条随对应包开工前修订即可。拆发本身仍不构成任何 AC 或阶段通过。


### r7

Session: `sess_ecc59d38-431a-4196-be92-09a584b532cc`; Turn: `turn_361cbc6e-10d3-4ad1-9c2c-a33b706c5f7c`.

**r6 修复核验**：逐动作授权表实质闭合原必须修——主人＝local_token/Hub 人工会话、PAT 不冒充人工；budget/policy/finalize/archive 拆为独立主人端点并逐端点豁免；finish 仅限当前驱动者＋主人预开 verified_write_review＋无在途＋证据全满足，默认 human_only；讨论定版/放弃归 finalize 人工专属；agent create/configure 携授权字段一律拒绝；owner_space 绑定堵住 session_send 绕过空间调度；W04-N09 正反用例配套；W04/W05 两文件口径一致。

三条建议修均已闭合：W04-02 改依 W01-03 服务入口（README §2.4 与文件一致）；W03-02 改依 W06-01 宿主只读识别、W06-01 相应补识别职责；README §3 增全局显式本地 BASE 门并点名 W03/W07 执行者核验。OPTIONAL 亦落实：W02-03 契约先行不阻塞、W07-03 对 W03-01/03/04 单向依赖消除双向等待、W05 明确 provider 未配置时 capabilities 标 enabled=false 且不得误报为版本缺工具。

**剩余必须修（1 处，文档级一行可修）**：W04 逐动作表与自身承诺矛盾——configure_members 主体仅「主人或当前驱动者」，但 W04 正文写「外部管家待配置空间允许先创建再配置成员/领取」、AC14 流程为「MCP 建成员未定空间→管家选人/领取→推进」，均要求**领取前**由外部管家配置成员；此刻管家既非主人也非驱动者，按表必被拒。修法二选一并钉入 W04-01：① 主人预授权模板内的创建者持预领取配置信封（限 scope 选人、不可改授权/预算/完成策略），W04-N09 补预领取配置正反用例；② 显式改 AC14 顺序为先领取后配置。

**无其他新矛盾**：README §2.6 未把 W07-03 的 W03 依赖列入（以文件行为准，可不修）；W08-04 分阶段门（A 依 W01-05/W02-02/W03-04 本机/W06-02 等）与 v3 15.1 映射一致，消除「等 C 才测 A」；W02/W03/W06/W08 改动未触碰既有豁免与 shared block 纪律。

**结论**：修掉上列 1 处后，本目录可据以拆发开发任务；拆发通过仅代表文档基线，不代表任何实现、子任务或 AC 已通过。


### r8

Session: `sess_ecc59d38-431a-4196-be92-09a584b532cc`; Turn: `turn_b217573a-5dda-480d-a71c-49ae1c9544f5`.

**核验通过，r7 剩余问题已关闭。**

① 顺序冲突消除：新文「创建成员可空的 external 待领取空间→领取新任期→配置成员→材料/预算就绪后显式启动」与逐动作授权表自洽——configure_members 主体＝主人或当前驱动者，领取后管家即满足主体要求，不再有被拒流。「领取不要求成员齐、未领取不可配置、未配置不可派发」同时保住 R2「先选人才能让管家选人的循环依赖不得存在」与 v3 §11「成员未确定前不启动」两条产品边界；任务书把「选人/领取」钉为同一配置阶段的有序动作集，属对 v3 未尽事项的明确化，不构成越权改边界。W04-N10/AC14 用例覆盖全部正反路径（空成员创建、未领取 403、合法领取新 term 可查、范围内选人、成员未齐 dispatch 拒绝、越范围/伪授权/旧 term 拒绝），W05 同步顺序，闭合完整。

② README §2.6 补齐 W07-02/W07-03 依赖，与文件行一致，上轮「可不修」项关闭。

**结论：可据此派发开发任务书（文档级）。** 一条不阻塞的附注：PRODUCT-PLAN v3 §15 AC14 原文语序（选人在领取前）与本任务书钉定的顺序不同，该文档下次修订时补一句执行顺序说明即可。派发与本次通过均不代表任何实现、子任务或 AC 已验收；各包仍按 RFC 契约先行、15.1 阶段门与独立测试循环执行。

## Final taskbook snapshot SHA-256

- README.md: `6eb7342418e60b7ac16ec97d9de9074012ef0f58bfa29a85e27384f5939408b0`
- W01-sessions.md: `d3f7948698f7fcff3fef186e38665aedcde32174166683bfc629dfca1a0b8913`
- W02-mcp-access.md: `b95e0289c5017537d85419a492c20f8a8714b51a4cffc5f96e6df022c919207f`
- W03-mcp-onboarding.md: `dad5c97ccd847605f5a65f1e5e112d0b48c151556c85a0b6b078cd985f48c249`
- W04-spaces.md: `fba3761b7914523a151908973f29839181f5ba4ee8d57d0e16b0e4f110bde7f8`
- W05-strategies.md: `0cd9a32a59ba293ed5c376334421155dae213fe08f38b939cbe5c2a55de96a03`
- W06-resources.md: `7df9e627ac3cc0eb12269e4e0685f397322834277b4514f67dba0be8e9facedb`
- W07-hub-routing.md: `04935d1f68dcbeabf3d6e006cd5ee0d7bd82673593fed2852539a3248b5eaed3`
- W08-recovery.md: `069cbfaf5a459349e23de399459f79642df5f82fc80d51d7697a274a8794f5b3`
