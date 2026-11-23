#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

TARGET="${1:-desktop}"
MYGO_CMD=(go run github.com/egoist/mygo/cmd/mygo@v0.10.0)

NODE_BIN_DIR="$(dirname "$(command -v node || echo /Users/tylinux/.nvm/versions/node/v23.9.0/bin/node)")"
export PATH="${NODE_BIN_DIR}:${PATH}"

ensure_web_dist() {
  echo "==> Building unified React/TS Web UI into pkg/webui/dist..."
  npm --prefix clients/web run build
}

build_desktop() {
  local extra_flags=()
  if ! command -v makensis >/dev/null 2>&1; then
    echo "==> makensis not found; passing -skip-installer for Windows target"
    extra_flags+=("-skip-installer")
  fi
  echo "==> Building mygo Desktop clients (darwin/arm64, linux/amd64, windows/amd64)..."
  "${MYGO_CMD[@]}" build -skip-build-command "${extra_flags[@]}" -platform darwin/arm64,linux/amd64,windows/amd64
}

build_desktop_macos() {
  echo "==> Building mygo Desktop client for macOS (darwin/arm64)..."
  "${MYGO_CMD[@]}" build -skip-build-command -platform darwin/arm64
}

build_desktop_linux() {
  echo "==> Building mygo Desktop client for Linux (linux/amd64)..."
  "${MYGO_CMD[@]}" build -skip-build-command -platform linux/amd64
}

build_desktop_windows() {
  local extra_flags=()
  if ! command -v makensis >/dev/null 2>&1; then
    echo "==> makensis not found; passing -skip-installer for Windows target"
    extra_flags+=("-skip-installer")
  fi
  echo "==> Building mygo Desktop client for Windows (windows/amd64)..."
  "${MYGO_CMD[@]}" build -skip-build-command "${extra_flags[@]}" -platform windows/amd64
}

build_ios_sim() {
  echo "==> Building mygo iOS Simulator app..."
  "${MYGO_CMD[@]}" build -skip-build-command -platform ios/simulator
}

build_ios() {
  echo "==> Building mygo signed iOS .ipa..."
  "${MYGO_CMD[@]}" build -skip-build-command -platform ios
}

build_android() {
  echo "==> Building mygo Android .apk & .aab..."
  "${MYGO_CMD[@]}" build -skip-build-command -platform android/arm64
}

case "$TARGET" in
  desktop)
    ensure_web_dist
    build_desktop
    ;;
  desktop-macos)
    ensure_web_dist
    build_desktop_macos
    ;;
  desktop-linux)
    ensure_web_dist
    build_desktop_linux
    ;;
  desktop-windows)
    ensure_web_dist
    build_desktop_windows
    ;;
  ios-sim)
    ensure_web_dist
    build_ios_sim
    ;;
  ios)
    ensure_web_dist
    build_ios
    ;;
  android)
    ensure_web_dist
    build_android
    ;;
  all)
    ensure_web_dist
    build_desktop
    if [[ "$(uname -s)" == "Darwin" ]]; then
      build_ios_sim
    fi
    build_android
    ;;
  *)
    echo "Usage: $0 [desktop|desktop-macos|desktop-linux|desktop-windows|ios-sim|ios|android|all]" >&2
    exit 1
    ;;
esac

echo "==> Client build artifacts written under dist/clients/"
