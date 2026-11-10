#!/usr/bin/env bash
# 把已构建好的 SEA 二进制和 web/dist 放进 dist-docker/，供 Dockerfile 使用。
# 前置：packages/server 下已执行 `node scripts/build-sea.mjs --target <target>`，且已构建 @nex/web。
# 用法：scripts/stage-docker-context.sh [linux-x64|linux-arm64]（默认 linux-x64）
#
# 2.0 基线说明：packages/server/scripts/build-sea.mjs 尚未迁移（它随旧 fork 的 SEA 发行改造引入），
# 因此当前分支上没有 SEA 产物，第一步的缺失检查会直接失败，不会生成半成品上下文。
set -euo pipefail
target="${1:-linux-x64}"
root="$(cd "$(dirname "$0")/.." && pwd)"
binary="$root/packages/server/dist/sea/nex-server-$target"
web="$root/packages/web/dist"
[[ -f "$binary" ]] || { echo "missing $binary (run build-sea.mjs --target $target)" >&2; exit 1; }
[[ -f "$web/index.html" ]] || { echo "missing $web/index.html (run pnpm --filter @nex/web build)" >&2; exit 1; }
rm -rf "$root/dist-docker"
mkdir -p "$root/dist-docker"
cp "$binary" "$root/dist-docker/nex-server"
cp -rL "$web" "$root/dist-docker/web"
echo "staged $target -> $root/dist-docker"
