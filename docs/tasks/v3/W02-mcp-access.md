# W02 MCP授权与桥接：调用权不依赖管家身份

上游v3：P07/P08/P10，F02/F04/F10，E03–05/E14，AC05/06/08/20。阶段A基础、B管家、C中心补验。现状证据I04/I05，见[总入口](README.md)。状态：**已有凭据与桥接，授权解耦和能力契约待做**。

## 现有基础与不应重做

Hub PAT/cookie权限路径、stdio桥接、SWARM_TARGET、Node六工具、prime_token轮换、local_token主人门、zcode配置合并都有实现。当前authOrchestrate要求prime_agent非空；本机会话主人端点不能被prime_token访问；MCP会话四工具Hub-only。原六.六豁免⑩明确是当前契约，新合法本机agent入口要同批修订映射而非删掉主人边界。

## 子任务

| ID | 工作 | 依赖与完成条件 |
|---|---|---|
| W02-01 | 定义并实现持久授权开关、共享编排凭据与管家角色解耦，迁移旧配置和declare/rotate行为 | 依W01-01；不依赖W01-03。主人可不选管家就启用调用；默认禁用，旧已授权配置有明确无升权迁移 |
| W02-02 | bridge能力信息、两backend错误/参数约定、所需工具发现，去除“接入即总理”等错误指引 | 依W02-01/W01-04；API版本/能力清单可实查，不硬编码工具数量替代tools/list |
| W02-03 | 接线与令牌兼容回归、中心PAT维护的P07入口契约和错误分类 | 依W02-01；先交入口数据/契约，实际P07页面链接在W03-04集成，不阻塞W02；旧env名保留、老调用范围不扩大 |

## 改动范围

localapi/server.go/prime.go及授权存储（拟新增mcpaccess.go）、cmd/node配置与token启动逻辑、cmd/swarmmind-mcp/main.go及backend抽象、hub API能力信息、SPEC鉴权/豁免、授权测试。P07完整布局属W03，P08默认管家选择属W06；不在本包安装/探测远端agent，不建设多租户或按agent资源级权限系统。

## 拟定REST/MCP权限矩阵

| 操作 | REST / 行为 | MCP/主体 |
|---|---|---|
| 本机启用状态/授权变更 | GET/PUT /api/v1/local/mcp/access，enabled/授权引用/影响范围；PUT保存开关不返回旧密钥 | 主人local_token专属，SPEC人工授权豁免；disabled时新编排请求403，查询主人管理仍可用 |
| 换发 | POST /api/v1/local/mcp/access/rotate，一次返回新明文并失效旧值 | 主人专属；兼容旧prime/rotate别名到同一实现，事务次序明确 |
| 能力元信息 | GET /api/v1/capabilities，两backend；版本/范围/功能标记，不含私密配置 | 新swarmmind_capabilities，描述“查看当前调用范围与可用能力”；并实测tools/list |
| 中心授权 | 复用/api/v1/auth/tokens*，PAT人工管理 | 不新增agent签发token工具，admin_token不可作Bearer |

保留SWARM_NODE_PRIME_TOKEN名字，但载荷变为不要求prime_agent的调用凭据。Node首版一共享凭据，不显示逐客户端撤销。标记管家、MCP授权、空间任期三者独立；取消默认管家不等于撤销既有授权，页面解释影响。现行declare会换发授权并自动接线：迁移后普通“选择管家”不得暗中换发所有调用者共享密钥；若用户显式选择“换发并应用”必须显示全体调用者失效影响、一次性材料及配置失败出口。旧declare客户端的响应字段/副作用兼容策略在W02-01逐条列出，不能静默变更。

首次迁移仅旧prime_agent非空且既有有效prime_token的配置可认定已授权；只有启动自动生成的token文件不能视为授权。新实例默认禁用；迁移后显式enabled成为唯一启用状态，重启不因默认管家增删重新推断。保持原已授权者能力范围、不额外开放主人/local端点。

## 安全/持久化与双端页要求

| 项 | Node | Hub | 共通约束 |
|---|---|---|---|
| P07授权区 | 单共享凭据启用/禁用/换发 | PAT引用/创建/撤销入口 | 脱敏状态、操作影响、一次结果，默认不含管理凭据 |
| P08管家 | 本机可选默认agent | 外部管家接入说明/标识 | 不暗授权限或自动接管空间 |
| 版本/能力提示 | 本机实际清单 | 中心实际清单/源node缺项 | 未升级时缺项可见，不降成普通任务伪续聊 |

授权状态文件原子落盘、权限0600；禁用/轮换失败不报成功；服务器主体独立落审计，标签不充当身份。凭据只在人工新建/换发结果一次展示；No-store响应、前端不入持久存储/URL/日志。不得以P07页面生成配置顺便执行agent。

## 验收与交付

- A的AC05授权部分：无prime_agent的standalone用户先人工授权，再外部目录/普通任务/会话调用成功；禁用/旧token/未授权分别拒绝，local_token不被导出。空间/默认管家子项留B。
- AC08 Node部分：两调用者共享旧token→轮换→下一请求都失效；禁用重启仍禁用；轮换落盘失败保留明确状态；模型密钥/主人凭据零输出。
- W02-02 tools/list+capabilities对两backend做实际stdio测试；注册代码/可执行装配缺任一步即FAIL。中心完整AC06在C，不以A静态配置测试替代。
- AC20旧prime门禁、声明、rotate、wire和旧Hub PAT回归；旧豁免与新agent合法面差异在SPEC表一一写出。测试改为验证新产品授权规则而非删除权限断言。
- build/test必跑，鉴权并发改动补race；更新TESTPLAN由独立会话执行。交付RFC与旧新授权迁移表、代码/契约/MCP实测、双端截图；不commit/部署。
