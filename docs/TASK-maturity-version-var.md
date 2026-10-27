# TASK-maturity-version-var：四件套版本可见

## 背景

Makefile LDFLAGS 注入 `-X main.version=$(VERSION)`（VERSION=git describe），但四个 cmd 的 main 包均无 version 变量，链接器静默忽略；四个二进制无 --version、无法回答自身版本；swarmmind-mcp 的 serverInfo 硬编码 "0.1.1"（cmd/swarmmind-mcp/main.go:168、195）。运维无法确认运行中程序版本（maturity 走查 P0 路线第一条，实锤于 final-report.md）。

## 目标与改动面

1. `cmd/hub/main.go`、`cmd/node/main.go`、`cmd/swarmmindctl/main.go`、`cmd/swarmmind-mcp/main.go` 各声明包级 `var version = "dev"`。四个 main 包各自独立链接，现有 Makefile LDFLAGS（`-X main.version`）无需改动即对四者生效。
2. 四件套支持 `--version`：打印 `<程序名> <版本号>` 后以 exit 0 退出。hub/node 融入现有 flag 集；swarmmindctl 与 mcp 按各自参数处理风格融入。
3. mcp serverInfo 两处硬编码 "0.1.1" 改用 version 变量。
4. hub 启动日志打印版本（贴合既有 `[hub] ...` 日志风格）。
5. hub 管理台页脚显示版本：版本值经管理面 API 传递（建议登录响应或既有 session/info 端点附带 `server_version` 字段，前端获取后渲染页脚；**禁止把版本硬编码进 embed 的 JS 资产**）。node 控制台同构实现（本机 API 既有 status 类端点可附带）。
6. SPEC 同步：管理面响应新增字段在六登记；MCP serverInfo 语义如变化在六.六核对。

## 双端形态对照（本条目含新 UI 元素，双端一致契约生效）

| 项 | hub 管理台 | node 控制台 |
|---|---|---|
| 位置 | 页脚区 | 页脚区（同构位置） |
| 形态 | 纯文本，`v<版本号>` 前缀 | 同左 |
| 数据源 | 管理面 API 字段（非硬编码） | 本机 API 字段（非硬编码） |
| 资产 | embed JS 渲染 | embed JS 渲染（shared block 如适用则同步） |

## 验收 DoD（逐条可执行）

1. `make VERSION=mat-test-001 build` 后：`./hub --version`、`./swarmmind-node --version`、`./swarmmindctl --version`、`./swarmmind-mcp --version` 均输出 `mat-test-001`。
2. 不带 VERSION 的常规构建（git describe 生效）时 --version 输出非空且 ≠ 注入值（证明 -X 真实生效，非默认值冒充）。
3. 启动 hub 日志含版本号；管理台页脚显示的版本与 `./hub --version` 一致（浏览器实测 + 截图）。
4. node 控制台页脚显示的版本与 `./swarmmind-node --version` 一致；双端页脚同构（附双端对比截图）。
5. swarmmind-mcp 的 serverInfo version 与其 --version 输出一致。
6. `go build ./... && go vet ./... && go test ./...` 全绿；改动 web 资产时：重建二进制后实测 + `TestWebMdRender|TestWebDrawer` 双绿。
7. 零新依赖（不动 go.mod）。

## 已知陷阱

- `-X main.version` 对每个 main 包分别生效，不要写成 `cmd/hub.version` 等带路径形态。
- web 资产是 go:embed：改 JS/CSS 后必须重建二进制再实测（AGENTS.md 既有规则）。
- playwright 走查：先等启动轮询尘埃落定再断言（AGENTS.md）。
- 页脚若涉及渲染样式，遵循双端词典/样式 shared block 现状，不单端发明新形态。
- 版本纪律：不 commit、不打 tag；改完跑全量测试并在最终输出列出改动文件清单与每条 DoD 验证结果。
