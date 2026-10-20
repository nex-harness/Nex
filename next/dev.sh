#!/usr/bin/env bash
# One-command dev stack for the next/ MVP:
#   Go server (next/server) + pi runner (packages/next-runner) + Vite web (packages/web).
#
# Usage:
#   next/dev.sh          # run the stack until Ctrl-C
#   next/dev.sh --e2e    # run the stack, wait until healthy, run the acceptance E2E, exit
#
# Environment:
#   NEX_NEXT_PORT           Go server port                 (default 3031)
#   NEX_NEXT_WEB_PORT       Vite dev port                  (default 5174)
#   NEX_NEXT_DATA           Server/runner data directory   (default ~/.nex-next)
#   NEX_DEMO_WORKSPACE      Workspace served by the runner (default $TMPDIR/nex-next-demo)
#   NEX_NEXT_BROWSER_TOKEN  Browser login token             (default: NEX_SERVER_AUTH_TOKEN or random)
#   NEX_NEXT_RUNNER_TOKEN   Runner bearer token             (default: NEX_RUNNER_TOKEN or random)
#
# The demo workspace always contains notes.txt ("hello from demo"), which the
# acceptance E2E reads.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_DIR="$REPO_ROOT/next/server"
SERVER_BIN="$SERVER_DIR/bin/nex-next-server"
RUN_E2E=0

for arg in "$@"; do
  case "$arg" in
    --e2e) RUN_E2E=1 ;;
    -h | --help)
      sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "[dev] unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

PORT="${NEX_NEXT_PORT:-3031}"
WEB_PORT="${NEX_NEXT_WEB_PORT:-5174}"
DATA_DIR="${NEX_NEXT_DATA:-$HOME/.nex-next}"
WORKSPACE="${NEX_DEMO_WORKSPACE:-${TMPDIR:-/tmp}/nex-next-demo}"
BROWSER_TOKEN="${NEX_NEXT_BROWSER_TOKEN:-${NEX_SERVER_AUTH_TOKEN:-}}"
RUNNER_TOKEN="${NEX_NEXT_RUNNER_TOKEN:-${NEX_RUNNER_TOKEN:-}}"

log() { printf '[dev] %s\n' "$*"; }
die() {
  printf '[dev] error: %s\n' "$*" >&2
  exit 1
}

# --- toolchain checks -------------------------------------------------------

command -v go >/dev/null 2>&1 || die "Go is required (https://go.dev/dl/); 'go' was not found on PATH."
command -v node >/dev/null 2>&1 || die "Node.js >= 24 is required; 'node' was not found on PATH."
NODE_MAJOR="$(node -p 'Number(process.versions.node.split(".")[0])')"
if [[ "$NODE_MAJOR" -lt 24 ]]; then
  die "Node.js >= 24 is required (found $(node -v)). Put Node 24 first on PATH (see mise.toml)."
fi
command -v pnpm >/dev/null 2>&1 || die "pnpm is required; 'pnpm' was not found on PATH."
command -v curl >/dev/null 2>&1 || die "curl is required for the health checks."

if [[ -z "$BROWSER_TOKEN" ]]; then
  command -v openssl >/dev/null 2>&1 || die "openssl is required to generate tokens (or set NEX_NEXT_BROWSER_TOKEN)."
  BROWSER_TOKEN="$(openssl rand -hex 24)"
fi
if [[ -z "$RUNNER_TOKEN" ]]; then
  command -v openssl >/dev/null 2>&1 || die "openssl is required to generate tokens (or set NEX_NEXT_RUNNER_TOKEN)."
  RUNNER_TOKEN="$(openssl rand -hex 24)"
fi

port_in_use() {
  command -v lsof >/dev/null 2>&1 || return 1
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

port_in_use "$PORT" && die "port $PORT is already in use; stop that process or set NEX_NEXT_PORT."
port_in_use "$WEB_PORT" && die "port $WEB_PORT is already in use; stop that process or set NEX_NEXT_WEB_PORT."

# --- workspace and data -----------------------------------------------------

RUNNER_DATA="$DATA_DIR/runner"
if [[ "$RUN_E2E" -eq 1 ]]; then
  # The acceptance run asserts an empty "real task" sidebar before the send.
  # Use a dedicated runner data dir and reset it so repeat runs start clean.
  RUNNER_DATA="$DATA_DIR/runner-e2e"
  rm -rf "$RUNNER_DATA"
fi
mkdir -p "$WORKSPACE" "$DATA_DIR/server" "$RUNNER_DATA"
# $TMPDIR can end with a slash; the runner resolves the path, so normalize it
# before using it in the health check and in what the runner announces.
WORKSPACE="$(cd "$WORKSPACE" && pwd -P)"
if [[ ! -f "$WORKSPACE/notes.txt" ]]; then
  printf 'hello from demo\n' >"$WORKSPACE/notes.txt"
  log "created $WORKSPACE/notes.txt"
fi

# --- build the Go server ----------------------------------------------------

log "building nex-next-server into next/server/bin"
(
  cd "$SERVER_DIR"
  mkdir -p bin
  go build -o bin/nex-next-server ./cmd/nex-next-server
)

# --- process supervision ----------------------------------------------------

CHILD_PIDS=()
CLEANED_UP=0

kill_tree() {
  local pid="$1" child
  [[ -n "$pid" ]] || return 0
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_tree "$child"
  done
  kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
  [[ "$CLEANED_UP" -eq 0 ]] || return 0
  CLEANED_UP=1
  log "stopping children"
  local pid
  for pid in "${CHILD_PIDS[@]}"; do
    kill_tree "$pid"
  done
  for pid in "${CHILD_PIDS[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

# start_prefixed NAME COMMAND... : run COMMAND with every output line prefixed.
start_prefixed() {
  local name="$1"
  shift
  "$@" > >(sed -u "s/^/[$name] /") 2> >(sed -u "s/^/[$name] /" >&2) &
  CHILD_PIDS+=("$!")
}

log "server:   http://127.0.0.1:$PORT (data: $DATA_DIR/server)"
log "runner:   $WORKSPACE (data: $RUNNER_DATA)"
log "web:      http://localhost:$WEB_PORT"
log "browser token: $BROWSER_TOKEN"

start_prefixed server env \
  NEX_SERVER_ADDR="127.0.0.1:$PORT" \
  NEX_DATA_DIR="$DATA_DIR/server" \
  NEX_WEB_STATIC_ROOT="$REPO_ROOT/packages/web/dist" \
  NEX_SERVER_AUTH_TOKEN="$BROWSER_TOKEN" \
  NEX_RUNNER_TOKEN="$RUNNER_TOKEN" \
  "$SERVER_BIN"

start_prefixed runner env \
  NEX_SERVER_URL="ws://127.0.0.1:$PORT/runner" \
  NEX_RUNNER_TOKEN="$RUNNER_TOKEN" \
  NEX_RUNNER_WORKSPACES="$WORKSPACE" \
  NEX_RUNNER_DATA_DIR="$RUNNER_DATA" \
  NEX_RUNNER_NAME="${NEX_RUNNER_NAME:-nex-next-local}" \
  NEX_RUNNER_LOG_LEVEL="${NEX_RUNNER_LOG_LEVEL:-info}" \
  pnpm --dir "$REPO_ROOT" --filter @nex/next-runner dev

start_prefixed web env \
  NEX_WEB_DEV_PORT="$WEB_PORT" \
  NEX_WEB_DEV_SERVER_ORIGIN="http://127.0.0.1:$PORT" \
  pnpm --dir "$REPO_ROOT" --filter @nex/web dev

# --- optional acceptance E2E ------------------------------------------------

if [[ "$RUN_E2E" -eq 1 ]]; then
  log "waiting for server and runner to list $WORKSPACE"
  healthy=0
  for ((attempt = 1; attempt <= 120; attempt++)); do
    info="$(curl -fsS "http://127.0.0.1:$PORT/api/server-info?token=$BROWSER_TOKEN" 2>/dev/null || true)"
    if [[ -n "$info" ]] && printf '%s' "$info" | grep -Fq "\"$WORKSPACE\""; then
      healthy=1
      break
    fi
    sleep 1
  done
  [[ "$healthy" -eq 1 ]] || die "runner did not announce $WORKSPACE within 120s"

  log "waiting for Vite on http://localhost:$WEB_PORT"
  vite_ready=0
  for ((attempt = 1; attempt <= 60; attempt++)); do
    if curl -fsS -o /dev/null "http://localhost:$WEB_PORT/" 2>/dev/null; then
      vite_ready=1
      break
    fi
    sleep 1
  done
  [[ "$vite_ready" -eq 1 ]] || die "Vite did not respond on http://localhost:$WEB_PORT within 60s"

  log "running acceptance E2E"
  set +e
  (
    cd "$REPO_ROOT"
    NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
      NEX_E2E_TOKEN="$BROWSER_TOKEN" \
      node next/e2e/acceptance.mjs
  )
  e2e_status=$?
  set -e
  log "acceptance E2E exited with $e2e_status"
  exit "$e2e_status"
fi

wait
