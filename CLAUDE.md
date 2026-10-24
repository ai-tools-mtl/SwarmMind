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
- exec 超时 kill 后必须 Wait → 否则僵尸进程堆积。自查：`grep -n "Process.Kill" internal/node/ | wc -l`（每处 kill 附近应有 Wait）
- 时间序列化一律 UTC，勿格式化成本地字符串存盘
- 禁止在 V0 引入 SPEC 第三节白名单之外的依赖；新增依赖必须在代码注释写理由
- 工人不执行 git commit；提交按钮在伦哥手里。改完跑 `go build ./... && go test ./...` 并在最终输出列出改动文件清单与每条 DoD 的验证结果

## 测试循环制度（2026-10-21 立，强制）

每个开发阶段收尾时执行：① codex 依需求文档（SPEC/INTERACTION）产出测试文档（docs/TESTPLAN.md，用例可机械执行）；② **新开独立会话**（subagent 或新 zcode，不共享开发上下文）逐条执行测试并出报告（每条 PASS/FAIL+证据）；③ FAIL 清单交 zcode 修改；④ 循环 ②-③ 直到全绿；⑤ **全部功能完整、无 bug 方可进入下个版本开发**。上线纪律（全绿才部署）由此流程保证。

## Git 远程操作（2026-10-21 新增，两文件同步）

- **push 前必须先查 remote**：`git remote -v` 确认目标仓库是伦哥指定的，禁止凭记忆猜测用户名/仓库名自建 remote（2026-10-21 SwarmMind 首提误建 mtl802/SwarmMind，正确为 ai-tools-mtl/SwarmMind）。
- 公开仓库 push 前必须扫描敏感信息（真实 token/IP/密钥）：`grep -rn '<敏感前缀>' --include='*'`，无输出才推。
- 两文件（CLAUDE.md/AGENTS.md）内容需保持同步，修改一份必须同步另一份。
