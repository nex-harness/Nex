#!/usr/bin/env bash
# One-command dev stack for the next/ MVP:
#   Go server (next/server) + pi runner (packages/next-runner) + Vite web (packages/web).
#
# Usage:
#   next/dev.sh               # run the stack until Ctrl-C
#   next/dev.sh --e2e         # run the stack, wait until healthy, run the acceptance E2E, exit
#   next/dev.sh --e2e=acceptance   # same as --e2e
#   next/dev.sh --e2e=permissions  # run the permissions E2E (approval card flow)
#   next/dev.sh --e2e=multi        # run the multi-runner E2E (two runners, same path)
#   next/dev.sh --e2e=tasks        # run the task-state E2E (pin/archive/rename/delete)
#   next/dev.sh --e2e=conversation # run the conversation E2E (runner-owned queue)
#   next/dev.sh --e2e=attachments  # run the attachments E2E (image + text file, restore)
#   next/dev.sh --e2e=files        # run the file-changes E2E (diffs, Undo, edit with rewind)
#   next/dev.sh --e2e=settings     # run the server-owned provider settings E2E (settings -> picker -> chat)
#   next/dev.sh --e2e=mcp          # run the MCP + skills E2E (settings -> echo server -> tool call -> /skill:)
#   next/dev.sh --e2e=all          # every suite, settings and mcp last
#
# Environment:
#   NEX_NEXT_PORT           Go server port                 (default 3031)
#   NEX_NEXT_WEB_PORT       Vite dev port                  (default 5174)
#   NEX_NEXT_DATA           Server/runner data directory   (default ~/.nex-next)
#   NEX_DEMO_WORKSPACE      Workspace served by the runner (default $TMPDIR/nex-next-demo)
#   NEX_MULTI_WORKSPACE     Path served by both E2E runners (default $TMPDIR/nex-next-multi)
#   NEX_NEXT_BROWSER_TOKEN  Browser login token             (default: NEX_SERVER_AUTH_TOKEN or random)
#   NEX_NEXT_RUNNER_TOKEN   Runner bearer token             (default: NEX_RUNNER_TOKEN or random)
#   NEX_SERVER_CONFIG_DIR   Server-owned provider config dir (default <server data>/config)
#   NEX_E2E_AGENT_DIR       Agent dir seeded for --e2e runs (exported to runners as
#                            PI_CODING_AGENT_DIR; default <data>/pi-agent-e2e)
#
# The demo workspace always contains notes.txt ("hello from demo"), which the
# acceptance E2E reads. The permissions E2E owns perm-*.txt in the same workspace
# and removes them at the start of a run. The tasks E2E creates its own throwaway
# chats in that workspace and archives/deletes them. Multi/all modes additionally
# start a second runner (`beta`); both E2E runners serve NEX_MULTI_WORKSPACE, which
# is recreated fresh for every E2E run.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_DIR="$REPO_ROOT/next/server"
SERVER_BIN="$SERVER_DIR/bin/nex-next-server"
E2E_MODE=""

for arg in "$@"; do
  case "$arg" in
    --e2e) E2E_MODE="acceptance" ;;
    --e2e=acceptance) E2E_MODE="acceptance" ;;
    --e2e=permissions) E2E_MODE="permissions" ;;
    --e2e=multi) E2E_MODE="multi" ;;
    --e2e=tasks) E2E_MODE="tasks" ;;
    --e2e=conversation) E2E_MODE="conversation" ;;
    --e2e=attachments) E2E_MODE="attachments" ;;
    --e2e=files) E2E_MODE="files" ;;
    --e2e=settings) E2E_MODE="settings" ;;
    --e2e=mcp) E2E_MODE="mcp" ;;
    --e2e=all) E2E_MODE="all" ;;
    -h | --help)
      sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "[dev] unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done
RUN_E2E=0
[[ -n "$E2E_MODE" ]] && RUN_E2E=1
# Multi mode needs a second runner; `all` runs it after acceptance/permissions.
RUN_MULTI=0
[[ "$E2E_MODE" == "multi" || "$E2E_MODE" == "all" ]] && RUN_MULTI=1

PORT="${NEX_NEXT_PORT:-3031}"
WEB_PORT="${NEX_NEXT_WEB_PORT:-5174}"
DATA_DIR="${NEX_NEXT_DATA:-$HOME/.nex-next}"
WORKSPACE="${NEX_DEMO_WORKSPACE:-${TMPDIR:-/tmp}/nex-next-demo}"
MULTI_WORKSPACE="${NEX_MULTI_WORKSPACE:-${TMPDIR:-/tmp}/nex-next-multi}"
BROWSER_TOKEN="${NEX_NEXT_BROWSER_TOKEN:-${NEX_SERVER_AUTH_TOKEN:-}}"
RUNNER_TOKEN="${NEX_NEXT_RUNNER_TOKEN:-${NEX_RUNNER_TOKEN:-}}"
ALPHA_NAME="nex-next-alpha"
BETA_NAME="nex-next-beta"

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
BETA_RUNNER_DATA="$DATA_DIR/runner-e2e-beta"
SERVER_DATA="$DATA_DIR/server"
# Server-owned config lives in its own directory (NEX_SERVER_CONFIG_DIR) so the
# provider store can be isolated from the rest of the server state.
SERVER_CONFIG="${NEX_SERVER_CONFIG_DIR:-$SERVER_DATA/config}"
DATA_E2E_CONFIG="$DATA_DIR/server-e2e-config"
if [[ "$RUN_E2E" -eq 1 ]]; then
  # The acceptance run asserts an empty "real task" sidebar before the send.
  # Reset sessions/metadata, but keep runner-id.json: the browser persists
  # `runner:<runnerId>:<path>` workspace identities, and a new id every run turns
  # the restored workspace into "runner offline" (sidebar + task list fail).
  RUNNER_DATA="$DATA_DIR/runner-e2e"
  rm -rf "$RUNNER_DATA/sessions" "$RUNNER_DATA/workspaces"
  # The server persists the browser's last workspace tabs (with their identities)
  # in settings.json. A stale identity from an earlier runner id is routed as
  # "runner offline", so E2E runs get their own settings store.
  SERVER_DATA="$DATA_DIR/server-e2e"
  rm -rf "$SERVER_DATA"
  # Server-owned provider config (config/config.json, keys/, secrets.enc) is
  # global state: a provider left in a developer's ~/.nex-next/server would be
  # pushed to every E2E runner and break model selection. The E2E store lives in
  # its own directory, which also lets the settings suite start clean.
  SERVER_CONFIG="$DATA_E2E_CONFIG"
  rm -rf "$SERVER_CONFIG"
  # Fallback-mode runners read the agent dir's models.json. Seeded with pi-ai's
  # faux provider: every suite then has one deterministic model whose requests
  # never leave the process, independent of the developer's real ~/.pi/agent
  # (zero models, missing plans, expiring OAuth). Only the settings suite, which
  # asserts the composer catalog starts without its own provider, gets an empty
  # dir. This is the E2E harness only: production runners keep ~/.pi/agent.
  E2E_AGENT_DIR="${NEX_E2E_AGENT_DIR:-$DATA_DIR/pi-agent-e2e}"
  rm -rf "$E2E_AGENT_DIR"
  mkdir -p "$E2E_AGENT_DIR/extensions"
  if [[ "$E2E_MODE" == "settings" ]]; then
    # The settings suite asserts the provider is absent before it is added.
    printf '{}\n' >"$E2E_AGENT_DIR/models.json"
  else
    cp "$REPO_ROOT/packages/next-runner/test/fixtures/e2e-agent/models.json" "$E2E_AGENT_DIR/models.json"
    cp "$REPO_ROOT/packages/next-runner/test/fixtures/e2e-agent/settings.json" "$E2E_AGENT_DIR/settings.json"
    # The extension is loaded from the agent dir; point it at the repo bundle.
    cp "$REPO_ROOT/packages/next-runner/src/extensions/e2eProvider.ts" "$E2E_AGENT_DIR/extensions/e2eProvider.ts"
  fi
  # The runner reads the seed from NEX_E2E_SEED_MODELS when its agent dir has no
  # models.json yet (see packages/next-runner/src/config.ts): runner data dirs
  # survive across E2E runs, while the agent dir is recreated here.
  export NEX_E2E_SEED_MODELS="$E2E_AGENT_DIR/models.json"
  export PI_CODING_AGENT_DIR="$E2E_AGENT_DIR"
fi
# Exported after the E2E override so both branches reach the server.
export NEX_SERVER_CONFIG_DIR="$SERVER_CONFIG"
if [[ "$RUN_MULTI" -eq 1 ]]; then
  rm -rf "$BETA_RUNNER_DATA/sessions" "$BETA_RUNNER_DATA/workspaces"
  # Fresh path per run: the multi E2E also asserts alpha never persisted a session for it.
  rm -rf "$MULTI_WORKSPACE"
fi
mkdir -p "$WORKSPACE" "$SERVER_DATA" "$RUNNER_DATA"
# $TMPDIR can end with a slash; the runner resolves the path, so normalize it
# before using it in the health check and in what the runner announces.
WORKSPACE="$(cd "$WORKSPACE" && pwd -P)"
if [[ ! -f "$WORKSPACE/notes.txt" ]]; then
  printf 'hello from demo\n' >"$WORKSPACE/notes.txt"
  log "created $WORKSPACE/notes.txt"
fi
# 修复依据：RUNNER_WORKSPACES 必须在 WORKSPACE 归一化之后再拼装。此前在归一化前赋值，
# 单 runner 的健康检查会拿归一化路径去匹配 runner 上报的未归一化 $TMPDIR 路径，永远超时。
RUNNER_WORKSPACES="$WORKSPACE"
if [[ "$RUN_MULTI" -eq 1 ]]; then
  mkdir -p "$MULTI_WORKSPACE" "$BETA_RUNNER_DATA"
  MULTI_WORKSPACE="$(cd "$MULTI_WORKSPACE" && pwd -P)"
  printf 'hello from multi\n' >"$MULTI_WORKSPACE/notes.txt"
  # 多 runner 模式下两个 runner 必须声明同一组 workspace：acceptance 的自动打开取
  # server-info.workspaces[0]，两台机器都声明 demo + multi 才能让结果与连接顺序无关。
  RUNNER_WORKSPACES="$WORKSPACE,$MULTI_WORKSPACE"
  log "multi workspace: $MULTI_WORKSPACE (alpha + beta)"
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

if [[ "$RUN_E2E" -eq 1 ]]; then
  # Deterministic scripted model for every suite (see the fixture's header).
  # The server is also part of the runner's fallback path, so it is never
  # dropped while the suites share one stack.
  start_prefixed faux-provider env \
    E2E_FAUX_PORT="${NEX_E2E_FAUX_PORT:-18080}" \
    node "$REPO_ROOT/packages/next-runner/test/fixtures/e2e-provider/server.mjs"
fi

log "server:   http://127.0.0.1:$PORT (data: $SERVER_DATA)"
log "runner:   $RUNNER_WORKSPACES (data: $RUNNER_DATA)"
if [[ "$RUN_MULTI" -eq 1 ]]; then
  log "beta:     $RUNNER_WORKSPACES (data: $BETA_RUNNER_DATA)"
fi
log "web:      http://localhost:$WEB_PORT"
log "browser token: $BROWSER_TOKEN"

start_prefixed server env \
  NEX_SERVER_ADDR="127.0.0.1:$PORT" \
  NEX_DATA_DIR="$SERVER_DATA" \
  NEX_WEB_STATIC_ROOT="$REPO_ROOT/packages/web/dist" \
  NEX_SERVER_AUTH_TOKEN="$BROWSER_TOKEN" \
  NEX_RUNNER_TOKEN="$RUNNER_TOKEN" \
  "$SERVER_BIN"

start_prefixed runner env "PI_CODING_AGENT_DIR=${PI_CODING_AGENT_DIR:-}" \
  "NEX_E2E_SEED_MODELS=${NEX_E2E_SEED_MODELS:-}" \
  NEX_SERVER_URL="ws://127.0.0.1:$PORT/runner" \
  NEX_RUNNER_TOKEN="$RUNNER_TOKEN" \
  NEX_RUNNER_WORKSPACES="$RUNNER_WORKSPACES" \
  NEX_RUNNER_DATA_DIR="$RUNNER_DATA" \
  NEX_RUNNER_ID="alpha" \
  NEX_RUNNER_NAME="$ALPHA_NAME" \
  NEX_RUNNER_LOG_LEVEL="${NEX_RUNNER_LOG_LEVEL:-info}" \
  pnpm --dir "$REPO_ROOT" --filter @nex/next-runner dev

# Accepting runs assert the auto-opened workspace is the demo workspace. The server
# sorts each runner's paths, and workspaces[0] comes from the first-registered
# runner; starting alpha first makes that deterministic.
wait_for_workspace() {
  local path="$1" name="$2" attempts="${3:-120}" info
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    info="$(curl -fsS "http://127.0.0.1:$PORT/api/server-info?token=$BROWSER_TOKEN" 2>/dev/null || true)"
    if [[ -n "$info" ]] && printf '%s' "$info" | grep -Fq "\"$path\""; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# --- optional second runner (multi-runner E2E) ------------------------------

if [[ "$RUN_MULTI" -eq 1 ]]; then
  log "waiting for alpha to list $WORKSPACE before starting beta"
  wait_for_workspace "$WORKSPACE" alpha 120 || die "alpha did not announce $WORKSPACE within 120s"
  start_prefixed beta env \
    NEX_SERVER_URL="ws://127.0.0.1:$PORT/runner" \
    NEX_RUNNER_TOKEN="$RUNNER_TOKEN" \
    NEX_RUNNER_WORKSPACES="$RUNNER_WORKSPACES" \
    NEX_RUNNER_DATA_DIR="$BETA_RUNNER_DATA" \
    NEX_RUNNER_ID="beta" \
    NEX_RUNNER_NAME="$BETA_NAME" \
    NEX_RUNNER_LOG_LEVEL="${NEX_RUNNER_LOG_LEVEL:-info}" \
    pnpm --dir "$REPO_ROOT" --filter @nex/next-runner dev
fi

start_prefixed web env \
  NEX_WEB_DEV_PORT="$WEB_PORT" \
  NEX_WEB_DEV_SERVER_ORIGIN="http://127.0.0.1:$PORT" \
  VITE_NEX_E2E_STORE_BRIDGE=1 \
  pnpm --dir "$REPO_ROOT" --filter @nex/web dev

# --- optional E2E -----------------------------------------------------------

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
  if [[ "$RUN_MULTI" -eq 1 ]]; then
    log "waiting for both runners to list $MULTI_WORKSPACE"
    info=""
    multi_healthy=0
    for ((attempt = 1; attempt <= 120; attempt++)); do
      info="$(curl -fsS "http://127.0.0.1:$PORT/api/server-info?token=$BROWSER_TOKEN" 2>/dev/null || true)"
      if [[ -n "$info" ]] && [[ "$(printf '%s' "$info" | grep -Fo "\"$MULTI_WORKSPACE\"" | wc -l | tr -d ' ')" -ge 2 ]]; then
        multi_healthy=1
        break
      fi
      sleep 1
    done
    [[ "$multi_healthy" -eq 1 ]] || die "both runners did not announce $MULTI_WORKSPACE within 120s"
  fi

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

  log "running $E2E_MODE E2E"
  set +e
  acceptance_status=0
  permissions_status=0
  tasks_status=0
  conversation_status=0
  attachments_status=0
  files_status=0
  multi_status=0
  settings_status=0
  mcp_status=0
  if [[ "$E2E_MODE" == "acceptance" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        node next/e2e/acceptance.mjs
    )
    acceptance_status=$?
    log "acceptance E2E exited with $acceptance_status"
  fi
  if [[ "$E2E_MODE" == "permissions" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        node next/e2e/permissions.mjs
    )
    permissions_status=$?
    log "permissions E2E exited with $permissions_status"
  fi
  if [[ "$E2E_MODE" == "tasks" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      # `all` runs acceptance first, so the sidebar already has rows by the time the
      # task suite starts; it then asserts "exactly one new row" instead of "empty".
      tasks_allow_existing=0
      [[ "$E2E_MODE" == "all" ]] && tasks_allow_existing=1
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        NEX_E2E_ALLOW_EXISTING_TASKS="$tasks_allow_existing" \
        node next/e2e/task-actions.mjs
    )
    tasks_status=$?
    log "task-actions E2E exited with $tasks_status"
  fi
  if [[ "$E2E_MODE" == "conversation" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      # `all` runs other suites first, so the sidebar already has rows by the time the
      # conversation suite starts; it then skips the "starts empty" assertion.
      conversation_allow_existing=0
      [[ "$E2E_MODE" == "all" ]] && conversation_allow_existing=1
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        NEX_E2E_ALLOW_EXISTING_TASKS="$conversation_allow_existing" \
        node next/e2e/conversation.mjs
    )
    conversation_status=$?
    log "conversation E2E exited with $conversation_status"
  fi
  if [[ "$E2E_MODE" == "files" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      # The suite opens its own chat and asserts files on disk, so earlier suites' sidebar
      # rows do not matter (no "starts empty" assertion).
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        node next/e2e/file-changes.mjs
    )
    files_status=$?
    log "file-changes E2E exited with $files_status"
  fi
  if [[ "$E2E_MODE" == "attachments" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        node next/e2e/attachments.mjs
    )
    attachments_status=$?
    log "attachments E2E exited with $attachments_status"
  fi
  if [[ "$E2E_MODE" == "multi" || "$E2E_MODE" == "all" ]]; then
    (
      cd "$REPO_ROOT"
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_MULTI_WORKSPACE="$MULTI_WORKSPACE" \
        NEX_E2E_RUNNER_DATA_ALPHA="$RUNNER_DATA" \
        NEX_E2E_RUNNER_DATA_BETA="$BETA_RUNNER_DATA" \
        NEX_E2E_ALPHA_NAME="$ALPHA_NAME" \
        NEX_E2E_BETA_NAME="$BETA_NAME" \
        node next/e2e/multi-runner.mjs
    )
    multi_status=$?
    log "multi-runner E2E exited with $multi_status"
  fi
  if [[ "$E2E_MODE" == "settings" || "$E2E_MODE" == "all" ]]; then
    # Every browser suite starts from the script's first answer (and earlier
    # stacks cannot tell a tool step from its neighbours by model inference).
    curl -fsS -X DELETE http://127.0.0.1:18080/state >/dev/null 2>&1 || true
    if [[ "$E2E_MODE" == "settings" ]]; then
      # Only the settings suite runs on this stack: give it the empty fallback
      # catalog and drop sessions that would reference the previous one.
      rm -rf "$RUNNER_DATA/sessions" "$RUNNER_DATA/workspaces" "$BETA_RUNNER_DATA/sessions" "$BETA_RUNNER_DATA/workspaces"
    fi
    (
      cd "$REPO_ROOT"
      # Runs last in `all`: it adds a provider to the server config store, so
      # every other suite still sees the runner's local (fallback) catalog.
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_SERVER_CONFIG="$SERVER_CONFIG" \
        NEX_E2E_SERVER_DATA="$SERVER_DATA" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        node next/e2e/settings.mjs
    )
    settings_status=$?
    log "settings E2E exited with $settings_status"
  fi
  if [[ "$E2E_MODE" == "mcp" || "$E2E_MODE" == "all" ]]; then
    # Own stack state: the server config doc (an MCP server and a skill stay
    # after the run) and the scripted provider's prompt log.
    curl -fsS -X DELETE http://127.0.0.1:18080/state >/dev/null 2>&1 || true
    if [[ "$E2E_MODE" == "mcp" ]]; then
      rm -rf "$SERVER_CONFIG" "$RUNNER_DATA/sessions" "$RUNNER_DATA/workspaces"
    fi
    (
      cd "$REPO_ROOT"
      # Runs after `settings` in `all`: it needs the doc it creates itself, and
      # the settings suite asserts an empty provider catalog first.
      NEX_E2E_BASE_URL="http://localhost:$WEB_PORT" \
        NEX_E2E_TOKEN="$BROWSER_TOKEN" \
        NEX_E2E_SERVER_CONFIG="$SERVER_CONFIG" \
        NEX_E2E_SERVER_DATA="$SERVER_DATA" \
        NEX_E2E_WORKSPACE="$WORKSPACE" \
        NEX_E2E_RUNNER_DATA="$RUNNER_DATA" \
        NEX_E2E_FAUX_URL="http://127.0.0.1:${NEX_E2E_FAUX_PORT:-18080}" \
        node next/e2e/mcp-skills.mjs
    )
    mcp_status=$?
    log "mcp-skills E2E exited with $mcp_status"
  fi
  set -e
  if [[ "$acceptance_status" -ne 0 ]]; then
    exit "$acceptance_status"
  fi
  if [[ "$permissions_status" -ne 0 ]]; then
    exit "$permissions_status"
  fi
  if [[ "$tasks_status" -ne 0 ]]; then
    exit "$tasks_status"
  fi
  if [[ "$conversation_status" -ne 0 ]]; then
    exit "$conversation_status"
  fi
  if [[ "$attachments_status" -ne 0 ]]; then
    exit "$attachments_status"
  fi
  if [[ "$files_status" -ne 0 ]]; then
    exit "$files_status"
  fi
  if [[ "$multi_status" -ne 0 ]]; then
    exit "$multi_status"
  fi
  if [[ "$settings_status" -ne 0 ]]; then
    exit "$settings_status"
  fi
  exit "$mcp_status"
fi

wait
