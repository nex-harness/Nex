#!/usr/bin/env bash
# Stage the per-platform runner bundles for the SSH installer
# (docs/next/runner-install.md decision 3).
#
# Each <out>/<platform>/ holds runner.mjs, pty-host.mjs,
# node-pty-assets.tar.gz, package.json and lib/ptyHostAssets.mjs — exactly
# what `internal/installer` uploads over SFTP. The bundle build is the same
# scripts/bundle.mjs deploy-remote.sh uses, cross-built for every target.
#
# Usage: next/stage-runner-bundles.sh [out-dir]   (default next/server/bin/runner-bundles)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-$REPO_ROOT/next/server/bin/runner-bundles}"
PLATFORMS=(linux-x64 linux-arm64 darwin-arm64 darwin-x64 win32-x64 win32-arm64)

echo "[bundles] staging into $OUT_DIR"
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
done
echo "[bundles] done:"
du -sh "$OUT_DIR"/* | sed 's/^/[bundles]   /'
