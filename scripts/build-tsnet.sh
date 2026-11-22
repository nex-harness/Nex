#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${ROOT_DIR}/dist/tsnet"
mkdir -p "$OUT_DIR"

TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
)

cd "${ROOT_DIR}/extras/nex-tsnet"

echo "==> Cross-compiling on-demand nex-tsnet sidecar binaries (CGO_ENABLED=0)..."
for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  ext=""
  [[ "$GOOS" == "windows" ]] && ext=".exe"
  out="${OUT_DIR}/nex-tsnet-${GOOS}-${GOARCH}${ext}"
  echo "  -> ${out}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" -o "$out" .
done

echo "==> Built nex-tsnet sidecars under ${OUT_DIR}/"
