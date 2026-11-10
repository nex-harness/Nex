#!/usr/bin/env bash
# 把 server-cli 的 staging 产物和 web/dist 放进 dist-docker/，供 Dockerfile 使用。
#
# 2.0 的发行形态是 packages/nex-server-cli 的 `stage`：
#   <release>/bin/nex                 launcher（定位发行根后跑 runtime/node）
#   <release>/runtime/node            随包 Node
#   <release>/runtime/server-cli.js   带 supervisor 的 CLI 入口
#   <release>/runtime/server-core.js  HTTP + WS 服务
#   <release>/runtime/nex.cjs         Agent bundle
#   <release>/runtime/packages/       官方插件与 bundled skills
#   <release>/runtime/node_modules/   产物扫描出的生产依赖闭包
#   <release>/manifest.json           版本、target、组件与哈希
#
# 用法：scripts/stage-docker-context.sh [linux-x64|linux-arm64]（默认 linux-x64）
# 前置：NEX_SERVER_TARGET=<target> pnpm --filter @nex/server-cli stage，且已构建 @nex/web。
set -euo pipefail
target="${1:-linux-x64}"
root="$(cd "$(dirname "$0")/.." && pwd)"
release="$root/packages/nex-server-cli/dist-release/nex-server-$target"
web="$root/packages/web/dist"

[[ -d "$release" ]] || {
  echo "missing $release (run: NEX_SERVER_TARGET=$target pnpm --filter @nex/server-cli stage)" >&2
  exit 1
}
[[ -f "$release/manifest.json" ]] || { echo "missing $release/manifest.json" >&2; exit 1; }
[[ -f "$web/index.html" ]] || {
  echo "missing $web/index.html (run pnpm --filter @nex/web build)" >&2
  exit 1
}

rm -rf "$root/dist-docker"
mkdir -p "$root/dist-docker"
cp -R "$release" "$root/dist-docker/nex-server"
cp -rL "$web" "$root/dist-docker/web"
echo "staged server-cli $target -> $root/dist-docker"
du -sh "$root/dist-docker/nex-server" "$root/dist-docker/web"
