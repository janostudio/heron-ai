#!/bin/sh
# 从源码构建 heron-ai 二进制。
# 用法：bash build.sh [输出路径]   （默认 ./bin/heron）

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# 默认在 heron-ai 仓库根构建；可通过 HERON_REPO 指定仓库路径
HERON_REPO="${HERON_REPO:-$(CDPATH= cd -- "$ROOT/../../.." && pwd)}"
OUTPUT="${1:-$HERON_REPO/bin/heron}"

if [ ! -f "$HERON_REPO/go.mod" ]; then
  echo "错误：未找到 heron-ai 仓库（$HERON_REPO）。请设置 HERON_REPO 环境变量。" >&2
  exit 1
fi

echo "构建 heron-ai 到 $OUTPUT ..."
cd "$HERON_REPO"
go build -o "$OUTPUT" ./cmd/server/
echo "BUILD_OK $OUTPUT"
