# W01 会话基础与入口：复用T1–T3补齐产品闭环

上游v3：P01/P02/P09，F01/F08，E02/E07–09/E15，AC01–04/15/19/20。阶段A。共同约束与源码证据I01–I06/I11见[总入口](README.md)。状态：**部分已有，不能整体报完成**。

## 已有与缺口

已有sessionx、两种解析器、cmd/node装配、Task.Session投递、Hub注册表/五端点/四工具、本机五主人端点和T3界面。旧任务书：[T1](../../TASK-agent-session-core.md)、[T2](../../TASK-agent-session-contract.md)、[T3](../../TASK-agent-session-web.md)。本包不重新实现解析器、生命周期门/墓碑、幂等指纹和绑定防线。

缺口：node MCP续聊；注册/首轮的本机统一可查询身份；有界轮队列、取消与状态对账；未开始会话的创建请求重试；两端详情页/导航与v3差异；真实CLI/双机不能从现有fake单测推导。sessionx.Close目前会等待写者，与v3在途关闭应拒绝的交互不同，须在操作服务入口防护并保持内核防复活语义。

## 子任务与顺序

| ID | 具体工作 | 依赖与交付 |
|---|---|---|
| W01-01 | 对T1–T3逐条复核，保存基线测试清单、当前API/状态/装配；定义保留与迁移差异 | 可立即派发；产出契约差异与证据，不把旧提交自报当通过 |
| W01-02 | 完善会话操作服务：创建请求ID与持久描述、首轮/续轮同源账本、串行有界队列/取消、关闭在途防护、工作区权限和互斥 | 依W01-01；RFC先定队列上限/满队列错误、取消/完成竞态、重启不确定收敛，故障测试证明接受与查询一致 |
| W01-03 | Node新增合法agent会话面，复用W01-02服务与W02-01授权；Hub保留现行契约并补稳定创建请求ID | 依W01-02/W02-01；主人旧/local路径零静默变义，pending会话在首轮发布前可查，作用域不可跨机 |
| W01-04 | nodeBackend装配四会话工具，Hub与Node按同一参数语义投影；两种backend输出未经证实提醒 | 依W01-03；端点、注册、main装配与tests/list同一交付，不只加文件 |
| W01-05 | P01/P02增量迁移：双端会话主入口、同构表单/详情页、历史抽屉兼容跳转、排队/取消/关闭状态和草稿保护 | 依W01-02/03/04；中心现有投递后端复用，外部权限批准通道不支持时如实受阻 |

## 改动范围与禁区

允许：internal/node/sessionx/、service/sessionturn.go与调度/取消/工作区相关代码、ledger（必要高水位与请求幂等）、localapi/sessions.go/server.go、hub/core/session.go/store/api会话组、protocol会话字段、cmd/node与MCP装配、双端web、SPEC/INTERACTION/测试。新增共享操作服务命名在W01-01确定。禁止重写普通Task投递/鉴权、删除既有回归或扩大CLI权限；不做协作空间和新MCP页面。

## REST/MCP与兼容

以下新项为拟定，实施同批写SPEC：

| 能力 | REST | MCP / 权限 |
|---|---|---|
| 登记、列表、详情 | Hub已有POST/GET /api/v1/sessions、GET /{id}；Node同路径新增 | swarmmind_session_create/list/status 双backend；Node W02编排凭据，不要求管家身份 |
| 投轮 | POST /api/v1/sessions/{id}/turns，两backend | swarmmind_session_send；沿用prompt/turn_seq，新增稳定request_id；同键同文查回原回执、异文409 |
| 主人旧面 | /api/v1/local/sessions* | 继续local_token；旧创建+首轮202不改，agent新面创建201不执行模型；统一事实不重复分配原生ID |
| 取消/关闭 | 新POST /api/v1/sessions/{id}/turns/{seq}/cancel；已有/{id}/close，两backend主人入口 | 人工专属，实施SPEC逐端点豁免，agent四工具不提供隐式cancel/close；排队取消与执行取消分开记账 |

创建请求必须在返回前持久记录平台ID，不能靠浏览器保留ID补救丢响应。turn_seq历史重放保持旧回执；新request_id参与完整内容指纹且兼容旧调用。取消先确认状态，再等待进程治理/结果，未知保留待核对，不把收到取消等同已停止。队列/状态新增值和旧六态投影映射由W01-01表列，不能把unverified改名done。

## 双端形态对照

| 组件 | Node | Hub | 必须一致 |
|---|---|---|---|
| P01 | 独立会话主入口，由旧任务区链接迁移 | 保留已有会话入口 | 列顺序/空态/筛选/未证实词典 |
| 新建 | agent，固定本机 | node→agent | 绑定/工作区/输入同序，居中modal；旧页内表单有迁移说明 |
| P02 | 独立详情页 | 同 | 绑定、轮次、答复、动作区；执行明细右抽屉；旧700px会话抽屉跳详情 |
| 取消/关闭 | 人工操作 | 人工操作 | 在途Close拒绝并指向取消/等待，关闭不删历史；批准权限不可伪造 |

## 验收、DoD与交付

- AC01–03：fake CLI首轮/续轮、A/B随机约定隔离；同时送17条或RFC上限+1验证有界/可取消；首轮接受后不在内核列表时仍能查请求和平台ID。不能用空列表判没执行。
- AC15：创建/投轮丢回执、同键异文、重复offer、取消与终态竞态、close与首轮竞态、落盘失败、node/hub重启；只执行一次，未知待核对，旧轮回执/墓碑/高水位不退。
- 权限/工作区：越界cwd、同真实工作区不同会话写冲突、配置/适配器漂移拒绝；平台外TUI无法保证独占时明确提示。
- AC04真实CLI两轮与受限工具另采证，本轮任务书不能冒称其完成；AC19双端同组件截图/键盘/轮询保真；AC20旧T1–T3测试不删。
- 执行go build -mod=readonly ./...、go test -count=1 ./...；并发相关包race，node --check双端；重建embed后用临时实例跑sessions套件。**先修E2E full默认外部BASE安全门，缺显式本地目标零网络退出**；不运行现有默认线上full脚本。
- 交付：契约/RFC、实施代码、SPEC/INTERACTION/MCP映射、分阶段TESTPLAN、独立报告/截图与限制；代码不commit。AC04未跑则A不标完整通过。W01不承担W07新的空间跨机验收。
