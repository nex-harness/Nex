#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

OUT_DIR="${ROOT_DIR}/bin/verify"
mkdir -p "$OUT_DIR"
trap 'rm -rf "$OUT_DIR"' EXIT

TARGETS=(
  "darwin/arm64"
  "linux/amd64"
  "windows/amd64"
)

CMDS=()
for d in cmd/*; do
  if [[ -d "$d" ]] && compgen -G "$d/*.go" > /dev/null; then
    CMDS+=("./$d")
  fi
done

echo "==> Verifying CGO_ENABLED=0 cross-platform builds..."
for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  if [[ ${#CMDS[@]} -eq 0 ]]; then
    echo "  -> Checking packages for ${GOOS}/${GOARCH} (CGO_ENABLED=0)..."
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build ./...
  else
    for cmd_pkg in "${CMDS[@]}"; do
      name="$(basename "$cmd_pkg")"
      ext=""
      [[ "$GOOS" == "windows" ]] && ext=".exe"
      out="${OUT_DIR}/${name}-${GOOS}-${GOARCH}${ext}"
      echo "  -> Building ${cmd_pkg} for ${GOOS}/${GOARCH} (CGO_ENABLED=0)..."
      CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" -o "$out" "$cmd_pkg"
    done
  fi
done

echo "==> All CGO_ENABLED=0 cross-platform checks passed!"
