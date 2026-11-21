#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_PREFIX="${INSTALL_PREFIX:-/opt/nex-v3}"

echo "Installing Nex-v3 Server package to ${INSTALL_PREFIX}..."
mkdir -p "${INSTALL_PREFIX}/bin" "${INSTALL_PREFIX}/runners" "${INSTALL_PREFIX}/config" "${INSTALL_PREFIX}/data" "${INSTALL_PREFIX}/logs"

if [ -f "${SCRIPT_DIR}/bin/nex-server" ]; then
  install -m 0755 "${SCRIPT_DIR}/bin/nex-server" "${INSTALL_PREFIX}/bin/nex-server.new"
  mv -f "${INSTALL_PREFIX}/bin/nex-server.new" "${INSTALL_PREFIX}/bin/nex-server"
fi

if [ -d "${SCRIPT_DIR}/runners" ]; then
  for f in "${SCRIPT_DIR}/runners"/nex-runner-*; do
    [ -e "$f" ] || continue
    base="$(basename "$f")"
    install -m 0755 "$f" "${INSTALL_PREFIX}/runners/${base}.new"
    mv -f "${INSTALL_PREFIX}/runners/${base}.new" "${INSTALL_PREFIX}/runners/${base}"
  done
fi

if [ -d "${SCRIPT_DIR}/config" ]; then
  cp -R "${SCRIPT_DIR}/config/." "${INSTALL_PREFIX}/config/"
  if [ ! -f "${INSTALL_PREFIX}/config/nex-server.env" ] && [ -f "${INSTALL_PREFIX}/config/nex-server.env.example" ]; then
    cp "${INSTALL_PREFIX}/config/nex-server.env.example" "${INSTALL_PREFIX}/config/nex-server.env"
    echo "Created default env config: ${INSTALL_PREFIX}/config/nex-server.env"
  fi
fi

OS="$(uname -s)"
if [ "$OS" = "Linux" ] && command -v systemctl >/dev/null 2>&1; then
  echo ""
  echo "Detected Linux systemd. To install/enable the service:"
  echo "  sudo cp ${INSTALL_PREFIX}/config/systemd/nex-v3-server.service /etc/systemd/system/"
  echo "  sudo systemctl daemon-reload && sudo systemctl enable --now nex-v3-server"
elif [ "$OS" = "Darwin" ]; then
  PLIST_DST="${HOME}/Library/LaunchAgents/com.nex.server.plist"
  echo ""
  echo "Detected macOS launchd. To load service for current user:"
  echo "  mkdir -p ${HOME}/Library/LaunchAgents"
  echo "  cp ${INSTALL_PREFIX}/config/launchd/com.nex.server.plist ${PLIST_DST}"
  echo "  launchctl load -w ${PLIST_DST}"
fi

echo ""
echo "Done. Bundled runners installed in ${INSTALL_PREFIX}/runners:"
ls -lh "${INSTALL_PREFIX}/runners" || true
