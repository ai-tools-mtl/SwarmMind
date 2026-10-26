# Logo 接入独立验收报告

日期：2026-10-26。依据：AGENTS.md、docs/TESTPLAN.md「Logo 接入专项」及 SPEC.md 第七节。执行者为独立验收 subagent；仅新增本报告，未修改实现、未 commit/push/部署。结论：LOGO-01/02/03/05/06 PASS；LOGO-04 品牌组件 PASS，但原始「窄屏整页无横向溢出」条件 FAIL（已归因为既有机器表格布局），不宣称专项全部全绿。

## 环境与证据

- 仓库当前源码独立重建：`go build -mod=readonly ./...`，再分别 `go build -o /tmp/swarm-logo-accept/hub ./cmd/hub`、`go build -o /tmp/swarm-logo-accept/node ./cmd/node`，均退出 0；二进制 mtime 为本轮 14:33，晚于资产改动。未使用仓库旧二进制或既有运行实例。
- 临时配置/data_dir/PID/日志均在 `/tmp/swarm-logo-accept/`；隔离 hub API `127.0.0.1:50763`、node API `127.0.0.1:50764`，未设置真实模型或 live 环境。
- `go test ./...`、`go test -count=1 ./...`（禁用缓存独立复跑）、`go test ./assets ./internal/node/localapi -run 'TestConsoleLogoAssets|TestWebMdRender|TestWebDrawer'`、`./scripts/demo-loopback.sh` 均退出 0。证据：build.log、test.log、test-uncached.log、shared.log、demo.log。
- Playwright Chromium 实测截图与详细测量：browser.json、baseline.json；实际运行脚本 verify.cjs、baseline.cjs。登录首次切换令牌表单后等待 1.2 秒再输入，避开已知启动重绘，未将测试时序问题当产品失败。
- 截图保存在 `/tmp/swarm-logo-accept/`（本机临时证据，不作为已入 Git 附件）：`compare-login-wide.png`、`compare-authed-wide.png`、`compare-login-narrow.png`、`compare-authed-390.png`，以及对应的 hub/node 单张原图；已实际打开图片逐项目检。

## 专项结果

| ID | 结果 | 实测证据 |
|---|---|---|
| LOGO-01 | PASS | 七个 SVG XML 解析通过，无 text、href 或字体属性；字标为路径。六个 PNG 为 RGBA，透明版 alpha 范围均 0–255，尺寸与文档一致。所有 SVG/PNG `git check-ignore` 返回 1。目检已批准概念板与明暗横幅：延续四模块、S 轮廓、中央负形及靛蓝/青绿，平面化后无光影；未把原概念板作为实际 Logo。 |
| LOGO-02 | PASS | 将脚本与 wordmark-outline.svg 复制到临时目录执行，六个 SVG 导出均逐字节等于工作区文件，未改写源资产。README picture 的 dark source 与 img 指向现存对应横幅。六份 SVG 经浏览器重新栅格化与 PNG 对比，平均通道差仅 0.063–0.185/255（raster.json，抗锯齿差异），图形/字标/尺寸一致；明暗横幅均目检无截断。 |
| LOGO-03 | PASS | 两端在未登录的新浏览器上下文请求 /logo.svg、/favicon.svg 均 200、Content-Type=image/svg+xml；四个响应全部与 assets/brand/logo-mark.svg 字节一致。/unknown-logo-resource.svg 两端均 404、text/plain。assets/brand.go 仅嵌入静态图标，无运行配置或凭据。 |
| LOGO-04 | 部分 PASS / FAIL | 两端登录和登录后所有品牌 img 均 naturalWidth=512；顶部实际30×30、登录64×64、侧栏24×24；favicon 声明均 /favicon.svg 且资源实取成功。1276×900 无溢出；390×900 双端登录、node「我的服务」无横向溢出；hub「机器」scrollWidth=539，见下方既有缺陷。品牌组件本身均未截断；30px/24px图形负形仍可辨。已交付并目检双端同组件对照截图。 |
| LOGO-05 | PASS | hub 令牌恢复通道和 node 本机令牌均真实登录成功，#logout 可见、侧栏出现；hub 切「机器」路由 /#/nodes、node 切「我的服务」 /#/services 均可用，退出/换令牌后回登录。共享展示与资产专项通过。与接入前备份比对，两端 app.js 差异仅登录标记/侧栏标记，未改通知逻辑；通知功能仅做未被改动的差异核验，不声称重跑通知全部专项。 |
| LOGO-06 | PASS（本机回归） | 只读构建、无缓存全量测试、回环演示通过；DoD 逐项见下表。真实双机、Windows 真机、真实模型和既有部署均未执行。 |

## 双端形态核验

| 组件 | hub | node | 结论 |
|---|---|---|---|
| 顶部品牌 | 30×30图标 + SwarmMind + 中心端 | 30×30图标 + SwarmMind + 本机 | PASS；图片、间距、位置一致，角色差异保留 |
| 登录卡片 | 64×64图标 + 管理员入口/恢复令牌表单 | 64×64图标 + 本机令牌表单 | PASS；相同图形及尺寸，原表单职责保留 |
| 侧栏品牌 | 24×24图标 + SwarmMind 中心端 | 24×24图标 + logo-node | PASS；同形态同位置，名称按角色；hub 名称自然换行，未裁切 |
| 页签 | /favicon.svg | /favicon.svg | PASS；同字节公开资源、HTML图标声明一致；未宣称操作系统浏览器标签原生UI截图验收 |
| 窄屏 | 顶部品牌可见；侧栏工作区按原响应规则隐藏 | 同左 | 品牌 PASS；机器整页溢出另计 FAIL |

## 发现分级

- **必须修：无本轮 Logo 接入新增缺陷。**
- **建议修（既有，LOGO-04 整页条件仍 FAIL）：** `internal/hub/web/style.css:158` 的 `.table-wrap{overflow:visible}` 配合 `internal/hub/web/style.css:160` 表头 nowrap 及 `internal/hub/web/style.css:167` 表格操作 nowrap，使「机器」页面在390px宽度下表格右缘到539.28px、document.scrollWidth=539。`hub-overflow-before.png` 与 `hub-overflow-without-logo.png` 在同一运行页面可逆隐藏全部 `.brand-logo` 前后均539；接入前备份含相同表格CSS，本轮CSS差异仅品牌布局规则。证据 baseline.json。没有把既有溢出归为Logo回归，也没有删除原测试条件或宣称全绿。修复应单独评估 sticky 表头与窄屏滚动行为，验收者未改源码。
- **可不修：** 宽屏侧栏「SwarmMind 中心端」自然折成两行；本轮目检未出现文字裁切或图标挤压，节点名仍按自身长度显示。

## SPEC 第七节 DoD

| DoD | 结果 | 当前执行证据与边界 |
|---|---|---|
| 1 | PASS | `go build -mod=readonly ./...` 退出0；本轮重建 hub/node 并实测其嵌入资产。 |
| 2 | PASS（macOS适用测试） | `go test -count=1 ./...` 退出0；protocol编解码/转移表、core信箱/投递窗口/幂等/迟到收敛、ledger恢复、runner stdin/env/超时进程组/白名单等测试包通过。Windows build-tag测试不在macOS执行。 |
| 3 | PASS | demo 全链完成0.6秒≤5秒，FinalAnswer=hello swarmmind，含本地API派单补充检查。 |
| 4 | PASS | demo sleeper failed，error=timeout；pgrep sleep 41 无幸存，runner整组终止测试亦通过。 |
| 5 | PASS | demo nodeB离线入信箱202；重连后completed，FinalAnswer=offline-mailbox。 |
| 6 | PASS（kill模拟边界） | demo kill/restart后epoch 2→3；旧帧门禁由本次全量单测覆盖。未注入真实网络黑洞。 |
| 7 | PASS | demo hub重启后nodeA/B自动重连、能力目录恢复，新任务完成。 |
| 8 | PASS | demo同键同内容返回同task_id，异内容409；重复offer/账本去重由本次全量单测补充。 |
| 9 | PASS | demo覆盖PATH越界返回failed+policy_denied，exit_code=-1；runner白名单测试通过。 |
| 10 | PASS | demo任务running后kill nodeB并重启，终态needs_review，error=node_restarted_midway，未自动重跑；CLI核对上下文显示正常。 |
| 11 | 未执行 | 两台真实机器、真实异构agent、跨机产物回取及断线/权限拒绝产品验收未执行。不能由本机回环替代，也不引用历史报告冒充通过。 |

## 改动清单与收尾

独立验收者唯一仓库写入：`docs/TESTREPORT-logo.md`。临时构建、配置、浏览器脚本、截图和日志仅位于 `/tmp/swarm-logo-accept/`；未覆盖用户既有通知改动，未提交/推送，未操作既有部署。验收隔离实例结束后已关闭；证据目录保留供复查。

### 实现改动文件（主代理补充）

以下为 Logo 实现的改动，不包含验收开始前已有的通知界面、AGENTS/CLAUDE 或共享抽屉测试修改：

- 共享资产服务与回归测试：`assets/brand.go`、`assets/brand_test.go`。
- 品牌源与交付：`assets/brand/README.md`、`wordmark-outline.svg`；`logo-mark`、`logo-lockup`、`logo-github`、`logo-github-dark`、`logo-mono`、`logo-white` 各一份 SVG 与 PNG（均在 `assets/brand/`）。
- 可编辑导出脚本：`scripts/build-brand.py`。
- 仓库首页：`README.md`。
- hub：`internal/hub/api/server.go`、`internal/hub/web/web.go`、`index.html`、`app.js`、`style.css`（后三项均在该 web 目录）。
- node：`internal/node/localapi/server.go`、`internal/node/web/web.go`、`index.html`、`app.js`、`style.css`（后三项均在该 web 目录）。
- 规范与验证：`SPEC.md` 的品牌静态资源说明、`docs/TESTPLAN.md` Logo 专项与双端形态表、本报告。

对初始备份的比较表明：双端 app.js 本轮仅替换登录与侧栏的两处品牌标记，CSS 仅增加品牌尺寸和排列规则；既有通知改动保持原样。现有部署未重建或重启，新界面仅在本轮隔离重建实例中实测。
