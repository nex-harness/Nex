#!/usr/bin/env bash
# Stage the per-platform runner bundles for the SSH installer
# (docs/next/runner-install.md decision 3, updated: a private Node ships with
# every bundle so targets need no Node.js installed at all — the VS Code
# Remote approach).
#
# Each <out>/<platform>/ holds runner.mjs, pty-host.mjs,
# node-pty-assets.tar.gz, package.json, lib/ptyHostAssets.mjs and node/ (the
# official Node distribution, bin/node stripped to ~25MB for posix; the
# whole dir for win32 where the dll layout is required).
#
# Usage: next/stage-runner-bundles.sh [out-dir]   (default next/server/bin/runner-bundles)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-$REPO_ROOT/next/server/bin/runner-bundles}"
NODE_VERSION="${NEX_NODE_VERSION:-24.21.0}"
PLATFORMS=(linux-x64 linux-arm64 darwin-arm64 darwin-x64 win32-x64 win32-arm64)

echo "[bundles] staging into $OUT_DIR (node v${NODE_VERSION})"
for platform in "${PLATFORMS[@]}"; do
  echo "[bundles] building $platform"
  (cd "$REPO_ROOT/packages/next-runner" && NEX_BUNDLE_PLATFORM="$platform" node scripts/bundle.mjs)
  target="$OUT_DIR/$platform"
  mkdir -p "$target/lib"
  cp "$REPO_ROOT/packages/next-runner/dist/runner.mjs" "$target/runner.mjs"
  cp "$REPO_ROOT/packages/next-runner/dist/pty-host.mjs" "$target/pty-host.mjs"
  cp "$REPO_ROOT/packages/next-runner/dist/node-pty-assets.tar.gz" "$target/node-pty-assets.tar.gz"
  cp "$REPO_ROOT/packages/next-runner/dist/package.json" "$target/package.json"
  cp "$REPO_ROOT/packages/next-runner/dist/lib/ptyHostAssets.mjs" "$target/lib/ptyHostAssets.mjs"

  # Private Node: the official distribution, cached under /tmp.
  case "$platform" in
    linux-x64)   node_dist="node-v${NODE_VERSION}-linux-x64";;
    linux-arm64) node_dist="node-v${NODE_VERSION}-linux-arm64";;
    darwin-arm64) node_dist="node-v${NODE_VERSION}-darwin-arm64";;
    darwin-x64) node_dist="node-v${NODE_VERSION}-darwin-x64";;
    win32-x64)   node_dist="node-v${NODE_VERSION}-win-x64";;
    win32-arm64) node_dist="node-v${NODE_VERSION}-win-arm64";;
  esac
  node_cache="/tmp/nex-node-dist/${node_dist}"
  if [[ "$platform" == win32-* ]]; then
    node_zip="${node_dist}.zip"
    if [[ ! -d "$node_cache" ]]; then
      mkdir -p "$(dirname "$node_cache")"
      curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/${node_zip}" -o "/tmp/nex-node-dist/${node_zip}"
      unzip -q -o "/tmp/nex-node-dist/${node_zip}" -d /tmp/nex-node-dist/
    fi
    # Windows needs the full distribution layout (node.exe + dlls beside it).
    rm -rf "$target/node"
    cp -R "$node_cache" "$target/node"
  else
    node_tar="${node_dist}.tar.xz"
    if [[ ! -d "$node_cache" ]]; then
      mkdir -p "$(dirname "$node_cache")"
      curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/${node_tar}" -o "/tmp/nex-node-dist/${node_tar}"
      tar -xJf "/tmp/nex-node-dist/${node_tar}" -C /tmp/nex-node-dist/
    fi
    # Posix: the node binary plus npm's cli (the installer runs `node npm-cli.js`
    # for dependency install; corepack/headers are not needed).
    mkdir -p "$target/node/node_modules/npm"
    cp "$node_cache/bin/node" "$target/node/node"
    cp -R "$node_cache/lib/node_modules/npm/." "$target/node/node_modules/npm/"
    rm -f "$target/node/node_modules/npm/*.md" "$target/node/node_modules/npm/LICENSE" 2>/dev/null || true
  fi
done
echo "[bundles] done:"
du -sh "$OUT_DIR"/* | sed 's/^/[bundles]   /'
