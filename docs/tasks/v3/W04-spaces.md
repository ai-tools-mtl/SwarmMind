# W04 本机协作内核：共享事实与显式路由

上游v3 P03/P04，F05/F07–09，E06–15，AC11/14–17/19/20。阶段B、C由W07扩范围。状态：**Hub群/Work可复用，本机空间与成员持久线程、负责人租约未实现**；I07/I08见[总入口](README.md)。

## 现状与架构差量

保留Group消息序号、候选标记、任务引用、预算及CAS测试；保留Work任期/尝试/collect，不以“通用工作流重写”替代。旧grouphost在HTTP API层提交普通Task并在终态自动点下一棒，不能直接用于固定会话，也不能把此回调与新负责人同时挂上形成双调度。

抽出无模型、动作封闭的协作服务（拟internal/collab/）：统一空间主状态、成员线程绑定、执行意图/回执、显式路由、暂停/租约/接管、消息读位置。Node提供本地adapter，Hub W07接远程adapter。空间模型扩展/关联Group与Work，不能三份可独立更新的目标/状态并存；W04-01明确权威字段及迁移。旧Group API旧记录保持原语义，v3空间使用新显式契约。

## 子任务

| ID | 工作 | 前置与结果 |
|---|---|---|
| W04-01 | 空间/成员/线程/执行意图/负责人模型，旧群兼容映射、存储事务边界和策略接口 | 依W01-02/W02-01；输出状态转换表与API schema，W05-01可据此定义策略，不等完整空间UI |
| W04-02 | 本机共享存储与串行调度、持久意图/outbox、过期租约暂停、显式接管及取消/恢复保护 | 依W04-01/W01-03；不需Hub，调用会话服务入口而非MCP进程，进程恢复不得凭回调自动重跑 |
| W04-03 | Node新空间REST/MCP与P03/P04；公共组件同步Hub壳，实际中心后端能力标不可用直到W07 | 依W04-02/W05-01；与W05两策略联合B验收，禁空壳上线 |

允许共享新包、Node存储/装配/Local API/通知适配、Group/Work服务抽取、MCP backend、双端web/SPEC/测试；Hub新空间可调用后端仅W07交付，不顺手改普通Task。禁止把Group读取游标当执行确认，禁止把全部群消息自动广播执行。

## 拟定REST/MCP（Node与Hub同义，Hub由W07接线）

| 操作 | REST | MCP |
|---|---|---|
| 创建/列表/详情 | POST/GET /api/v1/spaces；GET /spaces/{id} | swarmmind_space_create/list/status |
| 发言/增量读 | POST/GET /spaces/{id}/messages | swarmmind_space_say/messages；since只读不唤醒 |
| 指名轮次/查询执行 | POST /spaces/{id}/dispatches；GET /spaces/{id}/dispatches/{request_id} | swarmmind_space_dispatch/dispatch_status；绑定现有session+turn，不用Task ID当会话 |
| 有限控制 | POST /spaces/{id}/control | swarmmind_space_control（白名单pause/resume/configure_members/takeover/renew/finish，按动作schema与下表校验）；无任意HTTP/脚本执行 |
| 人工预算/授权策略/定版/归档 | POST /spaces/{id}/budget、/policy、/finalize、/archive | 独立主人接口，逐端点SPEC豁免；agent不能经control代调或伪造人已授权 |

管理能力各动作同批映射，关闭原生线程/取消在途仍为W01主人动作，不让control间接提权。finish的完成判据由W05策略提供；W04只经主人finalize记录abandoned且仍要收束在途，archive只允许终态且不删材料。空间查询/发言不代表获得调度权。外部管家流程固定为：创建external待领取空间（成员可空）→领取当前任期→配置成员→材料/预算就绪后显式启动。领取不要求成员已齐；未领取者不能配置成员，未配置成员不能派发。v3 AC14的“选人/领取”是同一配置阶段的动作集合，本任务明确其执行顺序，不增加预领取权限通道。

### 逐动作授权（W04-01的固定输入）

“主人”是Node local_token或Hub人工管理会话，PAT/编排凭据不冒充人工；“当前驱动者”须服务端已验证主体、当前term和未过期租约均匹配，内置服务使用独立可审计system主体。共享凭据只能识别该授权主体，不能识别是哪一个外部宿主；不承诺同凭据调用者间隔离。所有写操作仍有request_id/预期版本，不能只凭以下角色跳过状态校验。

| 动作 | 有权主体 | 前置与限制 |
|---|---|---|
| pause | 主人或当前驱动者 | 立即阻止新增；不取消在途，不换term |
| resume / dispatch | 当前驱动者；主人在manual模式或完成显式接管后 | 必需材料、预算、成员与版本有效；权限/待核对阻塞未解除则拒绝 |
| configure_members | 主人或当前驱动者 | 暂停/待配置、涉及成员无在途；只能在空间scope与既已授权能力内选人，不修改凭据、完成策略授权或预算上限 |
| set_budget（/budget） | 仅主人 | 记录原因与新上限；不自动resume；不能用configure_members修改预算 |
| policy（/policy） | 仅主人 | 暂停/待配置且无在途时改完成授权/领取约束，持久化策略版本和审计；agent不能通过create/configure/record伪造同义字段 |
| takeover | 主人可显式CAS选择manual/builtin/external；agent仅可领取已配置external且尚无有效负责人的待领取空间 | agent领取必须符合已记录的授权范围和领取约束；不能顶替有效任期，过期后的接管由人确认，不自动抢回 |
| renew | 仅当前驱动者 | 同主体/term且租约未过期；过期不能通过renew复活，HTTP空闲不判死 |
| finish（control） | 当前驱动者，包括明确授权的内置服务 | 仅写审策略且主人预先开启verified_write_review完成授权；无在途、同版本、测试证据/必须修/验收全满足。授权默认human_only，agent新建空间不能自授；否则403并等待人工定版 |
| finalize(done/abandoned) | 仅主人 | 讨论定版与放弃仅此入口；done仍通过策略验证，abandoned必填原因且无未收束执行，不得假清零任务 |
| archive | 仅主人 | 已完成/已放弃、无在途，仅列表收纳不关原生线程、不清材料 |

完成授权由主人创建/配置时显式写入并审计，变更仅主人管理面，MCP的create/configure请求若携此授权字段应拒绝而非信任；主人可预配置可被外部管家使用的空间或策略模板。任务单写审自动闭环仍可实现，但不能把候选/自报证据当成授权或完成证明。

## 关键契约必须在W04-01定案

- 主体来自服务端；负责人=`manual|builtin|external`及不可复用term，takeover带expected_term原子CAS，原请求重放返回旧回执。renew仅同主体/当前term，不能给旧term续命。租约期/续约频率/时钟漂移与重启策略必须给配置默认和测试边界。
- 暂停/过期只停新增派发；在途结果留账不触发下一轮。每次派发前事务内重检负责人、空间状态、预算、成员/权限/版本；预算扣减与意图同事务，重放不重复扣。
- 共享存储选型：Hub保留既有SQLite；Node可用原子快照/独立事务文件，但跨空间事实和任务接受必须有持久意图与恢复对账，不能“先写Group再发Task”留幽灵执行窗口。第一版按操作ID的journal/outbox实现，不额外引入数据库依赖。
- 原生输入已送达而结果丢失时只查事实，无法确定转待核对；不以CLI模型回复文本猜“成功”。新空间同一成员默认一原生线程，外部未受管TUI共写明确不保证。
- dispatch意图至少持久关联space_id、member/thread、session_id、turn_seq、task_id、request_id、当前term和版本引用；即使Task清理也能对账。会话进入空间后保存唯一owner_space绑定，普通session_send不能绕过空间状态/负责人直接推进成员线程：应拒绝并指向space_dispatch，或在同一服务入口携合法owner/term校验，W04-01固定一种契约。人工另行工作须暂停/显式交接，不靠线程串行锁掩盖逻辑双调度。
- message_count、dispatch_round、墙钟budget各自定义，不把既有MaxRounds发言数直接当执行次数；配置改预算不自动恢复。普通发言、FINAL候选、完成是独立事件。

## 双端形态/验收

| 组件 | Node（B） | Hub（C） | 同义与状态 |
|---|---|---|---|
| P03/P04 | 本机成员，完整独立操作 | 全网开放成员，壳先共享，未接后端不可假可用 | 列表/独立详情、创建modal、事件抽屉、字段顺序/动作词一致 |
| 调度条/接管 | 当前主体/任期/租约/等待原因 | 同 | 旧term拒绝；刷新/退出不更负责人 |
| 成员/消息 | 固定会话与输入范围可见 | 同 | 不全员唤醒；在途成员不可直接移除 |

单测fake时钟与fake执行器机械覆盖：W04-N01普通消息/补拉零调用；N02指名1次持续同ID且重复请求不扣预算；N03租约过期+两方CAS接管只一成功；N04暂停结果落账不再派；N05持久化失败零虚假接受/崩溃恢复不重发；N06成员改/结束/归档有在途拒绝；N07越scope/member/旧term拒绝；N08预算+候选在决策窗口变化仍事务复核。

W04-N09按上表逐动作做主人/当前管家/旧任期/普通PAT与Node共享编排凭据的正反测试：缺完成授权不能finish，伪造授权字段拒绝；讨论不能自动finish，改预算/放弃/归档非主人拒绝；空间已绑定成员不能用普通session_send绕过调度。请求主体与展示标签分开断言。

W04-N10（AC14配置顺序）：成员空的external空间创建成功；未领取configure_members返回403；合法领取成功且新term可查；当前驱动者选范围内成员成功；成员未齐dispatch拒绝；越范围/伪造授权字段/过期任期配置均拒绝。此用例不允许先配置成员作为领取前提。

AC11/14–17本机部分与W05联合执行；AC19双端同组件截图，C时补真实Hub交互；AC20旧群/Work契约不回归。build/test+并发race、重建embed/深层态走查；交付状态表、journal恢复演练、SPEC/MCP矩阵、独立报告。只完成共享模型不得将B标完成。
