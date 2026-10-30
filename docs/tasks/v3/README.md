# v3 开发任务总入口（2026-10-30）

上游：[最终版产品方案](../../PRODUCT-PLAN-v3.md)第10–17节；本目录是增量开发任务，不是实现完成报告。范围基线：`feat/agent-session`，HEAD `527fe10c6954a447659ba0ec313afd41e8a05cd6`；工作区已有v3文档未提交，不包含本轮代码变更。复查结果以[审查记录](../../archive/REVIEW-v3-taskbooks-20261030.md)为准。旧T1/T2/T3任务书与历史报告保留，不能被新包状态覆盖。

## 1. 源码盘点与缺口（不是仅按提交信息判定）

“已有”指找到装配、实现和测试；“需补验”指没有本轮功能环境证据。单测通过不代表产品AC整项通过。

| 证据 | 已有实现与入口 | v3缺口/归属 |
|---|---|---|
| I01 | [sessionx](../../../internal/node/sessionx/sessionx.go)：CreateWithID/Send/List/Status/Close、生命周期门、绑定校验、原子存储；两种CLI解析器及fake测试 | W01复用，不再重写适配器；排队取消/重启不确定、工作区授权与全局互斥、完整产品生命周期仍须补齐/实证 |
| I02 | [Hub会话](../../../internal/hub/core/session.go)、[REST](../../../internal/hub/api/sessions.go)、[投递执行](../../../internal/node/service/sessionturn.go)、cmd/node装配 | 创建注册表、异步投轮、原回执与三层Hub绑定守卫/Node首轮校验已有；不要重造传输；取消/有界轮队列/创建请求幂等与本机API一致性为W01增量 |
| I03 | [本机会话](../../../internal/node/localapi/sessions.go)：五个/local端点，主人凭据，创建即首轮；首轮完成前内核列表可能无映射 | 不能把“未列出”当未受理；本机agent调用面缺失，W01/W02增独立合法入口，保留主人端点与旧客户端语义 |
| I04 | [MCP会话](../../../cmd/swarmmind-mcp/sessions.go)、main.go nodeBackend | Hub四个session工具；nodeBackend仍六工具，未装会话组，关闭豁免；W01补本机组，W02审权限/能力发现，不说已对等 |
| I05 | [本机鉴权](../../../internal/node/localapi/server.go)、[管家授权](../../../internal/node/localapi/prime.go)、[接线](../../../internal/node/localapi/primewire.go) | authOrchestrate仍检查prime_agent；declare/rotate会换发并写已识别zcode配置。W02解耦授权/身份；W03把生成、应用、验证分开，不重复实现PAT或自动写外部机器 |
| I06 | [Hub Web](../../../internal/hub/web/app.js)、[Node Web](../../../internal/node/web/app.js)、[旧T3任务](../../TASK-agent-session-web.md) | 已有Hub会话导航、Node任务页会话区块、表单、四区抽屉、未证实警示；与v3独立详情页/三导航不符，W01/W08渐进迁移而非当不存在 |
| I07 | [群存储](../../../internal/hub/group/group.go)、[主持](../../../internal/hub/api/grouphost.go)、[群API](../../../internal/hub/api/groups.go) | jev选人/接力棒CAS、FINAL候选、消息预算已有；接力棒仍Submit普通Task，不绑定持续线程；空间所有权、租约、阶段/长期恢复及本机群缺失，W04/W05/W07补齐 |
| I08 | [Work](../../../internal/hub/core/work_dispatch.go)、[决策](../../../internal/decision/decision.go) | Work绑定尝试/接管/关闭已有，已完成步骤锁定；jev Suggest/SuggestMembers已有，Hub调用。W04复用事实，W05增循环阶段及本机provider装配，旧decide保留兼容 |
| I09 | [本机配置](../../../internal/node/localapi/config.go)、[节点接入](../../../internal/node/localapi/setup.go)、[中心向导](../../../internal/hub/api/enroll.go)、[agentid](../../../internal/node/agentid/agentid.go) | 配置/重载、接入/断开与开放能力已有；agentid是工作区键不是agent扫描器。W06补本机安装/配置/可调用检测，不误称已有自动发现 |
| I10 | [Node清理循环](../../../internal/node/service/service.go)、[备份脚本](../../../scripts/backup.sh)、Hub SQLite、Session注册表 | CleanupAcked已接运行循环，不沿用旧“未接线”记忆；备份脚本不是恢复Web API，Session长期引用/空间保留与恢复演练由W08补齐 |
| I11 | [E2E](../../../tests/e2e-console.mjs)、sessionx/API/MCP已有测试 | 会话套件有本地安全门，历史full套件仍有默认外部地址；本轮不启动该套件。W01先消除默认目标风险再运行隔离UI；真实双机/长期会话留各阶段AC采证 |

## 2. 八份任务书与拆分后的执行顺序

| 包 | 任务书 | 当前结论 | 首次可派发子任务 |
|---|---|---|---|
| W01 | [会话基础与入口](W01-sessions.md) | T1–T3已有，补生命周期/本机MCP/形态与实证 | W01-01盘点锁定→W01-02可靠轮服务；无需重做T1–T3 |
| W02 | [MCP授权与桥接](W02-mcp-access.md) | Hub PAT、Node授权已有但角色耦合 | W02-01授权迁移契约，依赖W01-01 |
| W03 | [双端MCP接入页](W03-mcp-onboarding.md) | 当前只有管家卡片/片段，完整P07未实现 | W03-01配置模型，依赖W02-01；不得等待全部W06完成 |
| W04 | [本机协作内核](W04-spaces.md) | Hub群/Work部分可复用，本机空间未实现 | W04-01共享状态/存储契约，依赖W01-02与W02-01 |
| W05 | [讨论与写审策略](W05-strategies.md) | Hub jev与主持已有，无固定线程写审门禁 | W05-01策略/证据契约，依赖W04-01 |
| W06 | [资源与agent接入](W06-resources.md) | 配置接入已有，分层发现/管家解耦未完成 | W06-01检测模型，依赖W01-01，可与W02并行 |
| W07 | [中心协作与远程路由](W07-hub-routing.md) | 远程会话已有；新空间全网范围未完成 | W07-01版本/范围契约，依赖W04-01，不重写会话投递 |
| W08 | [恢复维护与整体验收](W08-recovery.md) | 既有清理/维护可复用，产品闭环待补 | W08-01恢复引用契约，随W01/W04开工，不等D才做可靠性 |

依赖以子任务为准，不能用包名互依造成循环：

1. W01-01 → W01-02、W02-01、W06-01；W02-01不依赖W01本机agent API。
2. W01-02 + W02-01 → W01-03 → W01-04；W02-01 + W01-04 → W02-02。
3. W02-01 → W03-01；W06-01 → W06-02；W03-02自动写入依赖W03-01 + W06-01（宿主识别），不等资源登记表单；W03-03验证依赖W02-02。W06-03默认管家依赖W02-01 + W06-02 + W03-01，仅阻塞P07的可选管家子项。
4. W01-02 + W02-01 → W04-01；W04-02存储/调度依赖W04-01 + W01-03服务入口，不等待MCP装配；W04-03 API/UI依赖W04-02 + W05-01确定的策略接口；W05-01只依赖W04-01，不依赖W04-03。
5. W05-02/03依赖W04-02 + W05-01；阶段B联合交付W04-03与W05-02/03，不发布只有建群没有执行/停止的半成品。
6. W07-01依赖W04-01；W07-02依赖W07-01 + W04-03 + W05-02/03 + W06-02；W07-03依赖W07-02 + W03-01/03/04。W08-01接口约束可前置，W08后续回归在对应包之后。

各包RFC/契约子任务通过后即可派发后续实现；一个契约子任务完成不代表整个阶段完成。A本机会话/MCP先闭环，B本机完整协作，C中心跨机，D综合维护；AC拆分严格按v3第15.1节，真实AC04/18未执行不得绿灯。

## 3. 全包共同交付约束

- 每个子任务实现前提交可审核契约差异，明确新/复用/扩展路由、请求/返回/错误、主体、幂等、数据版本、兼容和回滚；下文API名为**拟定开发契约**，本轮不修改运行SPEC、不注册未实现工具。若实施发现冲突先修订RFC和本目录映射，不能自行绕行。
- 新管理能力REST与MCP同批装配/实测，工具名前缀swarmmind_、描述写能做什么。主人凭据授予、原生终局等保留人工专属，逐端点登记SPEC六.六豁免；不得以“node已有Hub工具”为本机agent能力缺失辩解，也不得直接删除旧豁免把local_token给agent。
- 双端同类页面形态/字段/流程/文案一致；旧T3白名单差异是当前状态不是新设计许可证。变更SPEC六.五白名单与INTERACTION须同批，shared block锁不可删。先读另一端组件；截图须同组件/视口成对，静态截图不代替深层功能。
- 可靠性不后置：接受前durable请求记录，ID/轮号与Task分离；存储失败不虚报成功，不确定结果待核对，禁止resume latest；源码目录与CLI权限由Node核验，敏感信息不离节点。模态/会话状态与执行事实分离，未证实工作不渲染成功。
- 页面/空间删除与原生线程Close、取消轮次不同。暂停不杀在途，接管不重派，归档不清材料；权限模式只断言不远程升权。原生模型/环境保留，不自动切yolo。
- 全部验证隔离临时数据与显式本地地址，不用现有生产端口/凭据；Go单测fake CLI不消耗真实额度；真实CLI与双机需单独记录输入/原生ID/版本/时间/哈希。产品目标不等于已实现；历史报告不代表本轮通过。
- 所有包的UI/E2E一律要求显式批准的本地BASE；缺失或外部目标必须零网络退出，W01先修历史full安全门。W03/W07等执行者也必须核此门，不可仅依赖某次启动时环境恰好正确。
- 每次改代码必须build/test，按风险补vet/race/定向故障；Web embed重建再走查。开发者不commit/push/部署。独立会话依TESTPLAN逐项执行、FAIL修复复验，同一意见两次未过上报Hana。

## 4. 权限与兼容的统一决定

W02将Node普通调用授权与默认管家角色解耦；现有 `SWARM_NODE_PRIME_TOKEN` 保留兼容。**不直接放开旧/local/sessions端点**：它们继续主人管理；W01增加与Hub同语义的 `/api/v1/sessions` agent入口，仅作用本机已允许调用能力。主人仍可用旧创建+首轮接口，新增agent创建先登记、投轮另行异步。两种入口共用注册事实与服务，不能出现两个可写会话指向一个原生线程。

取消/原生Close现行归人：本轮任务不默认向agent开放；W01若需新取消端点须列人工豁免，MCP四项会话能力先对等。空间pause/resume/dispatch是可授权协作动作，可由持当前任期的管家执行，与原生线程终局不是同一权力。空间archive/finish的权限详W04/W05，不能隐含Close。

空间控制以W04逐动作授权表为准：预算调整、人工定版/放弃、归档使用独立主人端点并登记豁免，不藏在agent control白名单。agent的finish仅限人预先授权的写审完成策略且证据满足；讨论定版与放弃由人处理。共享Node凭据不提供按外部宿主的身份隔离，不能把展示名当真实操作者。

## 5. 目录级验收

所有任务均有：现状证据、增量、子任务、依赖、文件范围/禁区、REST/MCP矩阵、双端形态表、AC与故障断言、DoD/交付物。拆单本身只做文档验证；本目录审查通过不把任何待开发子任务或AC标完成。
