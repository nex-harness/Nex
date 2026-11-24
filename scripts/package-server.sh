#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

SERVER_OS="${1:-$(go env GOOS)}"
SERVER_ARCH="${2:-$(go env GOARCH)}"
VERSION="${VERSION:-dev}"
COMMIT_SHA="${COMMIT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
PKG_NAME="nex-server-${SERVER_OS}-${SERVER_ARCH}"
STAGE_DIR="dist/pkg/${PKG_NAME}"
OUT_TARBALL="dist/${PKG_NAME}.tar.gz"

RUNNER_PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/arm64"
  "darwin/amd64"
  "windows/amd64"
)

if [ "${SKIP_WEB:-0}" != "1" ] && [ -f clients/web/package.json ]; then
  echo "==> Building embedded Web UI..."
  NODE_DIR="$(dirname "$(command -v node 2>/dev/null || echo /Users/tylinux/.nvm/versions/node/v23.9.0/bin/node)")"
  PATH="${NODE_DIR}:$PATH" npm --prefix clients/web run build
fi

rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR/bin" "$STAGE_DIR/runners" "$STAGE_DIR/config"

SERVER_BIN="nex-server"
if [ "$SERVER_OS" = "windows" ]; then
  SERVER_BIN="nex-server.exe"
fi

echo "==> Building ${SERVER_BIN} (${SERVER_OS}/${SERVER_ARCH}, CGO_ENABLED=0, version=${VERSION})..."
CGO_ENABLED=0 GOOS="$SERVER_OS" GOARCH="$SERVER_ARCH" \
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.buildSHA=${COMMIT_SHA}" -o "${STAGE_DIR}/bin/${SERVER_BIN}" ./cmd/nex-server

for plat in "${RUNNER_PLATFORMS[@]}"; do
  os="${plat%/*}"
  arch="${plat#*/}"
  ext=""
  if [ "$os" = "windows" ]; then
    ext=".exe"
  fi
  out_name="nex-runner-${os}-${arch}${ext}"
  echo "==> Cross-compiling ${out_name} (CGO_ENABLED=0, version=${VERSION})..."
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o "${STAGE_DIR}/runners/${out_name}" ./cmd/nex-runner
done

VERSION="${VERSION}" COMMIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" \
  bash "${ROOT_DIR}/scripts/gen-runner-manifest.sh" "${STAGE_DIR}/runners"

echo "==> Copying startup configs & installer..."
cp deploy/nex-server.env.example "$STAGE_DIR/config/nex-server.env.example"
cp -R deploy/systemd "$STAGE_DIR/config/systemd"
cp -R deploy/launchd "$STAGE_DIR/config/launchd"
cp -R deploy/windows "$STAGE_DIR/config/windows"
cp deploy/install.sh "$STAGE_DIR/install.sh"
chmod +x "$STAGE_DIR/install.sh"

cat > "$STAGE_DIR/version.json" <<EOF
{
  "version": "${VERSION}",
  "os": "${SERVER_OS}",
  "arch": "${SERVER_ARCH}",
  "built_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

cat > "$STAGE_DIR/README.txt" <<EOF
Nex-v3 Server Distribution (${SERVER_OS}/${SERVER_ARCH})

Contents:
  bin/${SERVER_BIN}            - Control plane binary (with embedded Web UI)
  runners/nex-runner-*          - Bundled multi-platform runner binaries (linux/darwin/windows)
  config/nex-server.env.example - Environment configuration template
  config/systemd/               - Linux systemd unit files
  config/launchd/               - macOS launchd plist files
  config/windows/               - Windows installation PowerShell script
  install.sh                    - Quick installer to /opt/nex-v3 (INSTALL_PREFIX=/opt/nex-v3)

Quick start (Linux/macOS):
  sudo ./install.sh
  # OR run directly:
  ./bin/${SERVER_BIN} -addr :8080 -runners-dir ./runners
EOF

mkdir -p dist
tar -czf "$OUT_TARBALL" -C dist/pkg "$PKG_NAME"
echo "==> Packaged ${OUT_TARBALL}:"
ls -lh "$OUT_TARBALL"
tar -tzf "$OUT_TARBALL" | head -n 40
