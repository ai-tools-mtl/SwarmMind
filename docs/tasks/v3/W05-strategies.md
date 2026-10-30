# W05 讨论与编写审查：两个完成策略

上游P04/P08，F05/F06，E02/E10–14，AC12/13/14/17。状态：**Hub jev选择/接力主持和FINAL候选已有；固定原生线程写审、可信版本/测试门禁、本机provider装配待做**。依[总入口](README.md)I07/I08，不重写已有LLM provider或Work任期逻辑。

## 子任务、范围与依赖

| ID | 具体工作 | 依赖与输出 |
|---|---|---|
| W05-01 | 策略接口、候选/审查/证据schema、循环阶段模型、停止/完成状态转换 | 仅依W04-01；提供无副作用验证器供W04-03使用，不依赖完整API/UI，从而无循环 |
| W05-02 | 有限讨论策略，Node装配jev、校验选人、顺序或显式并发指名，候选登记与人定版 | 依W04-02/W05-01；移接旧grouphost思想到共享服务，不让旧callback再驱动新空间 |
| W05-03 | 编写→冻结→审查→返工→复审→完成/暂停；问题编号和版本记录，复用原成员会话 | 依W04-02/W05-01；Work新增循环事实不是重派done步骤，追加式关联旧attempt/step并给迁移说明 |
| W05-04 | P04策略区和P08决策设置双端同构，外部管家驱动集成及机械/真实用例 | 依W04-03/W05-02/03；B本机两种策略闭环，C W07重验 |

允许：internal/decision/服务提取、共享collab策略、Hub旧主持兼容adapter、Work模型扩展、Node装配、双端web、API/MCP策略数据、SPEC/GROUP-RFC/PRIME-DESIGN/INTERACTION。禁止引入完整agent运行时到Hub、provider概率阈值即派发、通用流程图编辑器或自动Git push。

## 拟定REST/MCP与权限

| 能力 | REST | MCP / 语义 |
|---|---|---|
| 候选执行者建议 | 复用Hub POST /api/v1/dispatch-suggest；Node新增同路径 | swarmmind_dispatch_suggest两backend；实际候选由服务端目录/开放/预算/空间范围构造 |
| 记录候选/审查/材料 | POST /api/v1/spaces/{id}/records，GET /spaces/{id}/records?since | swarmmind_space_record/records，record.kind限定candidate/review/issue/evidence，类型字段校验；数据写入不等于执行 |
| 策略启动/暂停/结束 | W04 dispatch/control；finish调用本策略验证器 | 复用space_dispatch/control，不再增加另一套控制循环 |
| 决策配置/测试 | 各端主人管理面决策配置读/写/测试，具体路径W05-01列入RFC | 凭据授予/配置写入不得自动暴露agent；人工豁免逐端点说明，非凭据的可调用建议能力如上MCP同步 |

旧swarmmind_decide参数/外部StartLux降级与dispatch_suggest不同，保留兼容adapter并明确来源；新主线统一decision服务，不静默改变旧调用的候选语义。jev未配置普通工作照常，内置语义选人暂停并给手选出口；非法/越权候选拒绝，不偷偷换人。

完成动作遵守W04逐动作表：讨论候选由主人/finalize定版，写审仅在主人预先启用verified_write_review且证据满足时由当前驱动者control.finish；默认等待人工，放弃/归档均人工。策略或MCP记录不能修改完成授权。未配置provider时capabilities明确enabled=false，建议接口保持可调用并返回degraded/无建议，不能将配置未启用误报成版本缺工具。

## 写审与讨论的硬门槛

讨论只组织成员内容，不由Hub生成内容。内置规则可推进确定动作；开放需求拆解由用户/外部管家给计划。`FINAL:`沿用旧候选登记，候选可多条、消息裁剪不丢候选索引，完成须验证验收，不自动等于deliver。

写审的`revision`至少含基线、内容/补丁SHA-256、编写轮次、依赖产物引用与测试执行证据。issue至少含稳定ID、级别、文件:行、发现版本、回复/修复次数与复审结果。只收到agent自报测试通过不得给已证实；CLI退出0、协议解析成功、轮完成、工作验收四层分离，适配器输出unverified保持未知，必须额外有明确证据/有权人工核对才能完成。

代码改变使旧review无效；不同节点交付可用已有产物协议，登记范围/大小限制继承，超限说明阻塞而不是自动同步全仓。相同工作区写者互斥。内置执行测试命令只用Node主人预授权配置，不允许外部record声明任意shell就执行。测试证据绑定真实执行回执/hash，人工确认另有来源标签。

同一问题修两次不过对SwarmMind上报Hana；通用项目按显式策略。讨论发言/执行次数/时间预算各自收束；额度耗尽暂停保留原生会话，不换模型/不无限重试。外部管家可提计划，执行仍须W04 term、状态和权限校验，不能绕过“写审证据未满足”直接finish。

## 双端形态与验收

| 组件 | Node | Hub | 一致要求 |
|---|---|---|---|
| P04策略/问题/版本 | 本机角色/代码来源 | 远端来源，实际路径留执行端 | 同布局/状态词/必需字段；证据缺口与问题次数显式 |
| P08决策 | 本机provider，未启用不阻手动 | 中心provider，无agent依赖 | 同配置顺序、测试结果脱敏；不声称概率校准 |
| 候选与交付 | 主人核对/策略验证 | 同 | 候选/通过/结束/归档分开，不能把FINAL前缀显示为已交付 |

AC12 fake provider有效/空/超时/非法/预算耗尽，断言非法时零派发；AC13用固定缺陷writer和reviewer验证两ID稳定、版本变更旧审查失效、连续两次未修暂停、证据伪造不绿；AC14管家断线恢复读账而非只信历史；AC17未收束不能finish/archive。P08保存/测试的秘密不回显，测试会调用模型时显式提示/用户动作，默认fake不消耗额度。

AC14初始化顺序沿W04-N10：外部管家创建成员未定空间→先领取任期→再选人→显式启动；不得为了配置成员临时放宽普通PAT权限。

build/test/race与旧grouphost/decision/Work回归；SPEC六.六同批映射，TESTPLAN机械报告，B必须本机无Hub完整运行两策略，真实模型质量不由fake通过证明。不commit/部署。
