# TASK-maturity-release-matrix

> 条目5 / maturity-p0 批次 / 分支 developing/maturity-p0
> 前置：version-var（5784abf）已完成，`var version` 四件套已就位，本条目让发布链路真正吃到它。

## 1. 背景与问题（现状实测，2026-10-26）

1. **Makefile `release` 手写 6 条构建**，与顶部 `PLATFORMS` 变量（linux/amd64 linux/arm64 darwin/arm64 windows/amd64）脱节：PLATFORMS 定义了但没人消费；产物清单（hub 仅 linux-amd64、mcp 仅 linux/darwin）全靠手抄，改一处漏一处。
2. **CI `.github/workflows/ci.yml` release job 与 Makefile 清单不一致**：CI 缺 `swarmmind-mcp-linux-amd64`、`swarmmind-mcp-macos-arm64` 两个产物；且不跑 `embed-nodes`——tag 发布出来的包没有内嵌 node，与 deploy-hub 路径产物不对齐。
3. **embed-nodes 预编译 node 不注版本**（version-var 报备遗留项）：`embed-nodes` 目标里 `-ldflags '-s -w'` 没有 `-X main.version`，部署包内嵌 node `--version` 显示 `dev`，与 hub（有版本）不一致，排障时无法判断内嵌 node 的新旧。
4. **CI release job 只有 tag 触发**，无手动触发。版本纪律要求不打真实 tag 验证，需要 `workflow_dispatch` 手动触发作为验收路径。

## 2. 目标

发布矩阵单源化：**一份脚本定义矩阵（哪些二进制 × 哪些平台），Makefile 与 CI 都消费它**；embed-nodes 补版本注入；CI 补手动触发。改完后任何产物清单变更只改一处。

## 3. 方案

### 3.1 scripts/release.sh（新增，单源矩阵）

- 顶部集中声明矩阵，保持现有口径不扩散：
  - hub: linux-amd64（部署目标只有它）
  - node: linux-amd64 linux-arm64 darwin-arm64 windows-amd64
  - ctl: windows-amd64（现状口径）
  - mcp: linux-amd64 darwin-arm64（现状口径）
- 每个产物：`GOOS/GOARCH CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)'`，产物名 `swarmmind-<bin>-<os>-<arch>[.exe]`
- `VERSION` 取法与 Makefile 同源：`git describe --tags --always --dirty`，兜底 `dev`
- 逐产物非空校验（`[ -s ]`），任一失败 `set -e` 立即整体失败
- 用法：`scripts/release.sh [输出目录]`，默认 `dist/`

### 3.2 Makefile 对齐

- `release` 目标改为调 `scripts/release.sh`（保留 `@ls -la dist/` 收尾）
- `embed-nodes` 目标补版本注入：`-ldflags '-s -w -X main.version=$(VERSION)'`（修复背景问题 3）
- `PLATFORMS` 死变量删除或改为脚本消费，二选一，不留「定义了没人用」

### 3.3 CI ci.yml 对齐

- release job 构建步骤改为 `bash scripts/release.sh`（版本注入走同一脚本）
- 加 `workflow_dispatch:` 触发（手动，验收路径；**不打真实 tag**）
- 保留 tag 触发与 `softprops/action-gh-release` 上传逻辑
- test job 不动

### 3.4 embed-nodes 产物版本可验证

`--version` 旗标 version-var 已实现（`main.go` 已有 `var version` + flag），embed 产物注入后无需改 Go 代码。

## 4. DoD（验收标准，逐条给证据）

1. `bash scripts/release.sh` 本地实跑：产物数 = **8 个**（hub×1 + node×4 + ctl×1 + mcp×2），逐个非空，`ls` 清单留证（embed 5 个 gz 另计，由 embed-nodes 产出）。
2. 版本注入实证：本地构建的 node 与 mcp 二进制 `--version` 输出真实版本（git describe 串，非 `dev`）；embed-nodes 任一平台 gz 解包后二进制 `--version` 同样非 `dev`。
3. `make release` 与脚本产物清单一致（同一次实跑对比）。
4. ci.yml：release job 出现 `workflow_dispatch` 触发与 `bash scripts/release.sh`，产物清单与脚本一致（人工比对 + grep 留证）；不再手写任何 `GOOS=` 构建行于 ci.yml。
5. 回归：`go build ./... && go vet ./... && go test ./...` 全绿（本条目不改 Go 代码，但必须证明无回归）。
6. 断链检查：新增/改动 markdown 内部链接 0 断链。
7. **版本纪律**：不打真实 tag、不 push、不触发线上 release；CI 改动只提交到分支，实机验收以本地实跑 + yaml 审读为准（workflow_dispatch 实际触发留给伦哥后续自主操作，不阻塞本条目）。
8. 文档同步：`docs/OPERATIONS.md` 或 `docs/DEPLOYMENT.md` 发布节补一段「发布矩阵与共用脚本」说明（几行即可，讲清单源关系与手动触发方法）。

## 5. 已知陷阱

- **不要扩散矩阵**：hub 加平台看着美好，但 hub 未在 windows/mac 实测过，发布产物应为「实际部署口径」而非「理论能编译」。矩阵变更留给伦哥后续拍板。
- `-trimpath` 对 embed 与 release 都保留（现状一致），别只加一边。
- git describe 在浅克隆/CI checkout 下可能拿不到 tag——脚本兜底 `dev` 即可，不要为此引入额外 git 参数。
- deploy-hub 目标引用 `$(EMBED_DIR)`，改 embed-nodes 时不要破坏该路径约定。
- Windows 产物 `.exe` 后缀只在 windows 平台加，其余裸名（现状一致）。
- 版本纪律：不 commit（提交由 Hana 在伦哥点头后统一处理——本条目产出工作区改动即可）。
