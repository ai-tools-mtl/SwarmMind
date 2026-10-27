#!/usr/bin/env bash
# SwarmMind 发布矩阵单源脚本——Makefile 与 CI 共用的唯一产物清单。
# 用法: scripts/release.sh [输出目录]   （默认 dist/）
# 规约（maturity-release-matrix）:
#   - 矩阵只在此处声明，改产物清单只改这里，Makefile/CI 不得再手写 GOOS 构建行
#   - 每条格式: <bin>:<goos>:<goarch>:<产物名>   （产物名 darwin→macos 别名，向后兼容旧命名）
#   - VERSION 同源 Makefile: git describe --tags --always --dirty，兜底 dev
#   - 逐产物非空校验（[ -s ]），任一失败 set -e 立即整体失败——禁止静默残缺发布
set -euo pipefail

cd "$(dirname "$0")/.."

OUT_DIR="${1:-dist}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
LDFLAGS="-s -w -X main.version=${VERSION}"

# 发布矩阵（现状部署口径，不扩散）:
#   hub  仅 linux-amd64（控制面只部署在 linux 服务器）
#   node 四平台（含 linux-arm64，补齐原 Makefile 手写清单的遗漏）
#   ctl  仅 windows-amd64（现状口径）
#   mcp  linux-amd64 + darwin-arm64
MATRIX=(
  "hub:linux:amd64:swarmmind-hub-linux-amd64"
  "node:linux:amd64:swarmmind-node-linux-amd64"
  "node:linux:arm64:swarmmind-node-linux-arm64"
  "node:darwin:arm64:swarmmind-node-macos-arm64"
  "node:windows:amd64:swarmmind-node-windows-amd64.exe"
  "swarmmindctl:windows:amd64:swarmmindctl-windows-amd64.exe"
  "swarmmind-mcp:linux:amd64:swarmmind-mcp-linux-amd64"
  "swarmmind-mcp:darwin:arm64:swarmmind-mcp-macos-arm64"
)

mkdir -p "$OUT_DIR"

for entry in "${MATRIX[@]}"; do
  IFS=':' read -r bin goos goarch name <<<"$entry"
  out="$OUT_DIR/$name"
  echo "==> build $name (version=${VERSION})"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" "./cmd/$bin"
  [ -s "$out" ] || { echo "发布失败：$name 为空" >&2; exit 1; }
done

echo
echo "发布产物清单（version=${VERSION}）:"
ls -la "$OUT_DIR"
