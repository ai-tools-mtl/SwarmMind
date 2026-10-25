# SwarmMind 错误模式清单（zcode 自动加载，与 AGENTS.md 同步维护）

> 条目格式：规则式一句话 + 根因 + 自查命令。禁止空话。发现新模式当轮追加。

## 审查守则

- 只读审查，禁止修改文件；结论按「必须修 / 建议修 / 可不修」分级，每条给文件:行号
- 对照 SPEC.md 第七节 DoD 逐条核实；zcode 声明的测试结果必须复跑验证（go build/test 可实际执行，GOCACHE/GOMODCACHE 已全局可用）
- 返工上限：同一条意见修两次不过 → 停止，上报 Hana 裁决

- 面向管理面的新 REST 能力 → 同一变更内必须同步交付 MCP 工具（SPEC 六.六）：补 SPEC 六.六映射表一行 + cmd/swarmmind-mcp 增工具 + 实测；工具名前缀 swarmmind_，描述写「能做什么」。评审者对照本条检查，缺 MCP 同步 = 必须修。豁免：SPEC 六.六豁免清单内的人工专属端点（凭据授予/本机主人管理面/装机向导元数据）不受此约束，但豁免须在 SPEC 六.六显式登记并写明理由

## 通用工程

- 禁止直接覆盖写状态文件 → 崩溃时留下半截 JSON → 原子写：temp + rename。自查：`grep -rn "os.WriteFile" internal/ | grep -v tmp`
- 文件操作安全（agent 可及面路径，2026-10-24 产物挂载审查立）：不可信面（如 workdir 内）的目录创建/前置路径校验一律 Lstat 不跟随（符号链接=拒绝，防 MkdirAll/rename 经链接逃逸），盘上操作经 os.Root 定界、路径型操作（rename 等）每步重验路径组件；源文件以打开后 fd fstat 校验（校验对象=读取对象，消 Lstat→Open TOCTOU）；复制设字节上限并核对实得字节数==登记大小（防源检查后增长）；多步 rename 状态变更写 journal（原子写+目录 fsync）崩溃后按实物回放收敛，回滚失败保留材料聚合报告。自查：`grep -rn "MkdirAll\|os.Rename" internal/node/inputs/`（每处应有 verifyDirRel/重验/放大材料保留语义伴随）
- 源文件打开（读登记产物路径，2026-10-24 跨机取回复审 R1 立）：「不跟随符号链接/不阻塞」必须落在 open 原语本身——Lstat 预检消不掉 Lstat→Open 窗口（os.Root 的 Unix 实现还会跟随根内末段链接：ELOOP 时读链重试，用户传入的 O_NOFOLLOW 被库逻辑架空）；登记文件被换为无写端 FIFO 则 open 挂起，fd 校验无从执行，串行帧处理卡死、外部超时解不开。源读取统一经 inputs.OpenSourceFile：Unix 逐段 openat 游走（中间段 O_DIRECTORY|O_NOFOLLOW、末段 O_NOFOLLOW|O_NONBLOCK，每段锚定上一段目录 fd——检查即打开、零窗口；darwin syscall 不导出 openat，经白名单 x/sys/unix，登记见 go.mod/SPEC 三）；Windows 逐段 NtCreateFile 游走（每段 OBJ_DONT_REPARSE 打开时约束 reparse/link 解析——os.Root 在 Windows 遇 reparse point 读链后跟随根内链接不能承担该契约，O_NOFOLLOW_ANY 封装在 internal/syscall 不可导入，经白名单 x/sys/windows，登记见 go.mod/SPEC 三；Windows 无 FIFO open 阻塞语义，无需 O_NONBLOCK 对应物，2026-10-25 三审 W2）。自查：`grep -rn "OpenSourceFile\|unix.Openat\|\.Open(" internal/node/inputs/ internal/node/service/artifact.go`（源打开不得绕过 OpenSourceFile choke point）
- 帧请求-应答关联（dep_resolve/artifact_fetch 等 node 发起请求，2026-10-24 跨机取回立）：在途表登记**必须先于 Send**，应答按 ReplyTo 关联（容量 1 通道+非阻塞投递，超时/注销后迟到帧落空）——membus 的 Send 在调用方 goroutine 内同步执行对端 handler，hub 中继链（hub→源 node→hub→本 node）可能在 Send 返回前就把应答送进本端 HandleFrame，后注册必丢应答；hub 侧同理（fetchWaiter 先登记再 sendFetchRequest，超时兜底拆表防泄漏）。关联表键一律用**本端分配的编号**（hub 中继分配 relayMid，表内保存原 requester/mid、回传恢复关联），不得直接以对端提供的 mid 作键——同 mid 二次请求会覆盖在途 waiter（旧应答被新 waiter 消费、旧超时回调失效）；消费表项前先锁内核验**预期来源**（env.From 不符的伪应答不得拆表停表，防挤掉合法应答与超时兜底）。自查：`grep -n "registerRemoteWait\|fetches\[" internal/node/service/remotefetch.go internal/hub/core/artifact.go`（登记须在对应 Send 之前；fetches 键须为中继编号非 env.MessageID）
- exec 超时 kill 后必须 Wait → 否则僵尸进程堆积。自查：`grep -n "Process.Kill" internal/node/ | wc -l`（每处 kill 附近应有 Wait）
- store 事务 mutate 内「先变更、后以标志位+return nil 拒绝」= 带着已发生的变更提交事务（2026-10-25 Work W2 立）：txnErr/errCode 类标志位模式只在「拒绝点尚无任何变更」时无害（提交空副本）；mutate 内一旦发生过任何写（登记/回写/盖章），其后的一切失败必须以 **mutate 错误返回**整体拒绝（store 丢弃副本即回滚），不得走标志位路径。自查：`grep -n "txnErr\|return nil$" internal/hub/core/submit.go`（标志位置位处须早于首个变更点；变更后的失败点须 return err）
- 时间序列化一律 UTC，勿格式化成本地字符串存盘
- 审计/事件主体禁止采信请求自报（2026-10-25 Work W1 审查立）：事件账本等审计字段的身份一律由服务端鉴权层解析后**独立字段落账**（hub：auth 中间件注入请求上下文——admin 会话 → `admin`、Bearer PAT → `pat:<令牌名>`；RFC-work-resume 六.2），请求自报标签（created_by/by/submitted_by 类）原样留档但不承担身份证明、不得覆盖服务端主体——自报身份任意调用方可伪造（handleSubmit 的 adminIdentity 覆盖同此原则）。自查：`grep -rn "Principal\|authPrincipal" internal/hub/ | grep -v _test`（写事件处须带服务端主体；W2 起新事件种类同规）
- 禁止在 V0 引入 SPEC 第三节白名单之外的依赖；新增依赖必须在代码注释写理由
- 长驻交互层局部刷新禁止整层 innerHTML 重建（2026-10-25 抽屉 D22 立）：抽屉/弹层/表单视图的轮询刷新必须分区局部更新——首次打开建壳（稳定分区 data-zone），刷新按分区内容 diff 局部写入，数据无变化不碰 DOM（details 开合/原文模式/草稿/焦点/滚动随未动分区保持），分区替换时回放 details 开合态，迟到应答按层会话序号丢弃，入场动画只随壳播放一次；「重建后恢复状态」「终态停轮询」不算合格终解。已按此改造：hub syncDrawerZones / node drawerZonesApply（shared block，机制见 SPEC 六.五「双端 Web 展示一致性契约」第三条）。自查：`grep -n "innerHTML" internal/hub/web/app.js internal/node/web/app.js`（#layer/.drawer/#drawer 层容器级整体重建只允许出现在首次打开建壳、关闭清理、整层错误壳三处）
- 禁止双端展示漂移（2026-10-25 抽屉统一立）：hub/node 控制台共用展示层（md 渲染器、抽屉状态/错误词典、时间工具、事件流、答复与日志区渲染、分区更新机制、组件样式）改动必须双端同改同验，shared block 逐字节锁（internal/node/localapi/web_md_render_test.go、web_drawer_sync_test.go）即真相，单端独改=漂移回潮；跨端同义动作同词（标准词「查看全部任务」）、能力文案不得承诺端上不存在的能力（node 无核对端点只如实指路 hub）；合理差异以 SPEC 六.五白名单为准，白名单外差异按漂移处理。自查：`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 双绿 + diff 双端 app.js 的 `===== … shared block =====` 标记块
- 工人不执行 git commit；提交按钮在伦哥手里。改完跑 `go build ./... && go test ./...` 并在最终输出列出改动文件清单与每条 DoD 的验证结果

## 测试循环制度（2026-10-21 立，强制）

每个开发阶段收尾时执行：① codex 依需求文档（SPEC/INTERACTION）产出测试文档（docs/TESTPLAN.md，用例可机械执行）；② **新开独立会话**（subagent 或新 zcode，不共享开发上下文）逐条执行测试并出报告（每条 PASS/FAIL+证据）；③ FAIL 清单交 zcode 修改；④ 循环 ②-③ 直到全绿；⑤ **全部功能完整、无 bug 方可进入下个版本开发**。上线纪律（全绿才部署）由此流程保证。

## Git 远程操作（2026-10-21 新增，两文件同步）

- **push 前必须先查 remote**：`git remote -v` 确认目标仓库是伦哥指定的，禁止凭记忆猜测用户名/仓库名自建 remote（2026-10-21 SwarmMind 首提误建 mtl802/SwarmMind，正确为 ai-tools-mtl/SwarmMind）。
- 公开仓库 push 前必须扫描敏感信息（真实 token/IP/密钥）：`grep -rn '<敏感前缀>' --include='*'`，无输出才推。
- 两文件（CLAUDE.md/AGENTS.md）内容需保持同步，修改一份必须同步另一份。
