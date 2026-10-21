#!/usr/bin/env bash
# Deploy the next/ stack to a Linux host over SSH.
#
# Two layouts:
#
#   user mode (default)   everything under <remote-home>/nex-next, supervised by
#                         transient systemd *user* units (or setsid when user
#                         linger is off). Good for a test box.
#
#   --system mode         production layout: root-owned code under /opt/nex-next,
#                         user-owned data under <remote-home>/.nex-next, secrets in
#                         <remote-home>/.nex-next/env (mode 600) and two system
#                         units (nex-next-server, nex-next-runner). Needs
#                         passwordless sudo on the host.
#
# Both modes build every artifact locally and only copy binaries:
#   - the Go server cross-compiled for linux/amd64 (or --build-on-host),
#   - the production web client,
#   - the Node runner bundle (packages/next-runner/scripts/bundle.mjs).
#
# Usage:
#   next/deploy-remote.sh <ssh-host> [options]
#
#   --system                    production layout described above
#   --port <n>                  listen port (default 3041; use 80 with --system)
#   --listen <addr>             listen address (default 0.0.0.0)
#   --workspace <path>          runner workspace (default ~/nex-next/ws in user
#                               mode, ~/Developer/Nex in --system mode)
#   --node <dir>                remote directory holding node 24
#                               (default: `node` on PATH, or ~/.local/opt/node2414/bin)
#   --reuse-token-from <file>   copy NEX_SERVER_AUTH_TOKEN from that host file
#                               (e.g. an existing service's EnvironmentFile)
#                               instead of generating one. The value is copied on
#                               the host and never printed.
#   --build-on-host             build on the host instead of cross-compiling
#   --no-start                  install artifacts but do not (re)start
#
# Environment equivalents:
#   NEX_DEPLOY_DIR        user-mode root                (default ~/nex-next)
#   NEX_DEPLOY_PORT       server port                   (default 3041)
#   NEX_DEPLOY_LISTEN     server listen address         (default 0.0.0.0)
#   NEX_DEPLOY_NODE_BIN   remote Node 24 bin directory
#   NEX_DEPLOY_SUPERVISOR auto | systemd | setsid       (user mode, default auto)
#   NEX_REMOTE_WORKSPACE  runner workspace path
#   NEX_DEPLOY_ALLOWED_ORIGINS  extra Origin hosts for /ws (comma separated)
#   NEX_RUNNER_NAME       display name in the picker    (default: short hostname)
#   NEX_RUNNER_ID         fixed runner id               (default: generated once)
#
# Secrets: user mode generates ~/nex-next/secrets.env; --system writes
# <home>/.nex-next/env (mode 600). Both are reused on redeploy — a redeploy never
# rotates tokens. The previous binary and web build are kept as .prev for a quick
# rollback.
#
# Stop / restart / rollback: see next/README.md ("Deploy to a Linux host").

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOST=""
MODE="user"
PORT="${NEX_DEPLOY_PORT:-3041}"
LISTEN="${NEX_DEPLOY_LISTEN:-0.0.0.0}"
REMOTE_DIR="${NEX_DEPLOY_DIR:-}"
REMOTE_WORKSPACE="${NEX_REMOTE_WORKSPACE:-}"
NODE_BIN="${NEX_DEPLOY_NODE_BIN:-}"
SUPERVISOR="${NEX_DEPLOY_SUPERVISOR:-auto}"
ALLOWED_ORIGINS="${NEX_DEPLOY_ALLOWED_ORIGINS:-}"
RUNNER_NAME="${NEX_RUNNER_NAME:-}"
RUNNER_ID="${NEX_RUNNER_ID:-}"
REUSE_TOKEN_FROM=""
BUILD_ON_HOST=0
DO_START=1

usage() { sed -n '2,62p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --system)
      MODE="system"
      shift
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --listen)
      LISTEN="$2"
      shift 2
      ;;
    --workspace)
      REMOTE_WORKSPACE="$2"
      shift 2
      ;;
    --node)
      NODE_BIN="$2"
      shift 2
      ;;
    --reuse-token-from)
      REUSE_TOKEN_FROM="$2"
      shift 2
      ;;
    --build-on-host)
      BUILD_ON_HOST=1
      shift
      ;;
    --no-start)
      DO_START=0
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    -*)
      echo "[deploy] unknown flag: $1" >&2
      exit 2
      ;;
    *)
      if [[ -n "$HOST" ]]; then
        echo "[deploy] only one ssh host may be given" >&2
        exit 2
      fi
      HOST="$1"
      shift
      ;;
  esac
done

[[ -n "$HOST" ]] || {
  echo "[deploy] usage: next/deploy-remote.sh <ssh-host> [--system] [--port N]" >&2
  exit 2
}

log() { printf '[deploy] %s\n' "$*"; }
die() {
  printf '[deploy] error: %s\n' "$*" >&2
  exit 1
}

# ssh joins its arguments into one remote shell command string, so an empty
# positional argument silently shifts every later one. Quote each argument
# explicitly and invoke the remote script through `bash -s --`.
remote_bash() {
  local script="$1"
  shift
  local cmd="bash -s --"
  local arg
  for arg in "$@"; do
    cmd+=" $(printf '%q' "$arg")"
  done
  "${SSH[@]}" "$cmd" <<<"$script"
}

remote_sudo_bash() {
  local script="$1"
  shift
  local cmd="sudo -n bash -s --"
  local arg
  for arg in "$@"; do
    cmd+=" $(printf '%q' "$arg")"
  done
  "${SSH[@]}" "$cmd" <<<"$script"
}

command -v ssh >/dev/null 2>&1 || die "ssh is required."
command -v rsync >/dev/null 2>&1 || die "rsync is required."
SSH=(ssh -o BatchMode=yes "$HOST")
RSYNC=(rsync -az)

# --- local builds -----------------------------------------------------------

TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/nex-deploy.XXXXXX")"
trap 'rm -rf "$TMP_ROOT"' EXIT

if [[ "$BUILD_ON_HOST" -eq 1 ]]; then
  log "building on the host (requires Go and pnpm there)"
else
  log "building the linux server binary locally"
  command -v go >/dev/null 2>&1 || die "go is required to cross-compile (or pass --build-on-host)."
  (
    cd "$REPO_ROOT/next/server"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
      -o "$TMP_ROOT/nex-next-server" ./cmd/nex-next-server
  )
fi

log "building the web client"
(
  cd "$REPO_ROOT"
  pnpm --filter @nex/web build
)

log "building the runner bundle"
(
  cd "$REPO_ROOT"
  pnpm --filter @nex/next-runner bundle
)

# --- remote paths -----------------------------------------------------------

REMOTE_HOME="$("${SSH[@]}" 'printf %s "$HOME"')"

case "$MODE" in
  user)
    REMOTE_DIR_EXPANDED="${REMOTE_DIR:-$REMOTE_HOME/nex-next}"
    TOKEN_VAR="NEX_NEXT_BROWSER_TOKEN"
    ;;
  system)
    REMOTE_DIR_EXPANDED="${REMOTE_DIR:-/opt/nex-next}"
    TOKEN_VAR="NEX_SERVER_AUTH_TOKEN"
    NODE_BIN="${NODE_BIN:-$REMOTE_HOME/.local/opt/node2414/bin}"
    ;;
esac
# Expand a leading ~/ before deriving paths from the directory.
case "$REMOTE_DIR_EXPANDED" in "~/"*) REMOTE_DIR_EXPANDED="$REMOTE_HOME/${REMOTE_DIR_EXPANDED#\~/}" ;; esac
case "$REMOTE_WORKSPACE" in "~/"*) REMOTE_WORKSPACE="$REMOTE_HOME/${REMOTE_WORKSPACE#\~/}" ;; esac

if [[ "$MODE" == "system" ]]; then
  REMOTE_WORKSPACE="${REMOTE_WORKSPACE:-$REMOTE_HOME/Developer/Nex}"
  DATA_DIR="$REMOTE_HOME/.nex-next/server"
  RUNNER_DATA_DIR="$REMOTE_HOME/.nex-next/runner"
  SECRETS_FILE="$REMOTE_HOME/.nex-next/env"
else
  REMOTE_WORKSPACE="${REMOTE_WORKSPACE:-$REMOTE_DIR_EXPANDED/ws}"
  DATA_DIR="$REMOTE_DIR_EXPANDED/data/server"
  RUNNER_DATA_DIR="$REMOTE_DIR_EXPANDED/data/runner"
  SECRETS_FILE="$REMOTE_DIR_EXPANDED/secrets.env"
fi
[[ "$REMOTE_WORKSPACE" = /* ]] || die "workspace must be absolute: $REMOTE_WORKSPACE"

log "mode=$MODE code=$REMOTE_DIR_EXPANDED data=$DATA_DIR workspace=$REMOTE_WORKSPACE"

# --- remote directories and secrets ----------------------------------------

remote_bash "$(cat <<'REMOTE_SETUP'
set -euo pipefail
MODE="$1"
DIR="$2"
DATA_DIR="$3"
RUNNER_DATA_DIR="$4"
SECRETS_FILE="$5"
WORKSPACE="$6"
mkdir -p "$DATA_DIR" "$RUNNER_DATA_DIR" "$WORKSPACE"
if [[ "$MODE" == "user" ]]; then
  mkdir -p "$DIR"/{bin,web,runner,logs}
fi
# Create secrets only when the file is missing; a redeploy keeps existing tokens.
if [[ ! -f "$SECRETS_FILE" ]]; then
  mkdir -p "$(dirname "$SECRETS_FILE")"
  umask 077
  if [[ "$MODE" == "system" ]]; then
    {
      printf 'NEX_SERVER_AUTH_TOKEN=%s\n' "$(openssl rand -hex 24)"
      printf 'NEX_RUNNER_TOKEN=%s\n' "$(openssl rand -hex 24)"
    } >"$SECRETS_FILE"
  else
    {
      printf 'NEX_NEXT_BROWSER_TOKEN=%s\n' "$(openssl rand -hex 24)"
      printf 'NEX_NEXT_RUNNER_TOKEN=%s\n' "$(openssl rand -hex 24)"
    } >"$SECRETS_FILE"
  fi
fi
chmod 600 "$SECRETS_FILE"
[[ -f "$WORKSPACE/notes.txt" ]] || printf 'hello from linux\n' >"$WORKSPACE/notes.txt"
REMOTE_SETUP
)" "$MODE" "$REMOTE_DIR_EXPANDED" "$DATA_DIR" "$RUNNER_DATA_DIR" "$SECRETS_FILE" "$REMOTE_WORKSPACE"

if [[ "$MODE" == "system" ]]; then
  # Root-owned, world-readable code tree; staging stays owned by the SSH user so
  # rsync does not need sudo (the final swap below runs as root).
  remote_sudo_bash "$(cat <<'REMOTE_SYSDIRS'
set -euo pipefail
DIR="$1"
SSH_USER="$2"
mkdir -p "$DIR/bin" "$DIR/web" "$DIR/runner"
chown root:root "$DIR" "$DIR/bin" "$DIR/web" "$DIR/runner"
chmod 0755 "$DIR" "$DIR/bin" "$DIR/web" "$DIR/runner"
# Staging belongs to the SSH user so rsync never needs sudo; clear any leftover
# tree from a failed run first (it may contain root-owned files).
rm -rf "$DIR/.stage"
install -d -o "$SSH_USER" -g "$SSH_USER" "$DIR/.stage"
REMOTE_SYSDIRS
)" "$REMOTE_DIR_EXPANDED" "$("${SSH[@]}" 'id -un')"
  mkdir -p "$TMP_ROOT/web"
else
  mkdir -p "$TMP_ROOT/web"
fi

# --- reuse an existing login token ------------------------------------------

if [[ -n "$REUSE_TOKEN_FROM" ]]; then
  log "copying NEX_SERVER_AUTH_TOKEN from $REUSE_TOKEN_FROM (value never printed)"
  remote_bash "$(cat <<'REMOTE_TOKEN'
set -euo pipefail
SOURCE="$1"
TARGET="$2"
TARGET_VAR="$3"
[[ -r "$SOURCE" ]] || { echo "[deploy] cannot read $SOURCE" >&2; exit 1; }
python3 - "$SOURCE" "$TARGET" "$TARGET_VAR" <<'PY'
import os, re, sys
source, target, target_var = sys.argv[1], sys.argv[2], sys.argv[3]
value = None
with open(source, "r", encoding="utf-8") as handle:
    for line in handle:
        match = re.match(r"\s*(?:export\s+)?NEX_SERVER_AUTH_TOKEN\s*=\s*(.*)$", line)
        if not match:
            continue
        raw = match.group(1).strip()
        if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in "\"'":
            raw = raw[1:-1]
        if raw:
            value = raw
            break
if not value:
    sys.exit("source has no NEX_SERVER_AUTH_TOKEN")
text = ""
if os.path.exists(target):
    with open(target, "r", encoding="utf-8") as handle:
        text = handle.read()
lines = [
    line for line in text.splitlines() if not re.match(rf"\s*{re.escape(target_var)}\s*=", line)
]
if target_var != "NEX_SERVER_AUTH_TOKEN":
    lines = [
        line
        for line in lines
        if not re.match(r"\s*NEX_SERVER_AUTH_TOKEN\s*=", line)
    ]
lines.append(f"{target_var}={value}")
with open(target, "w", encoding="utf-8") as handle:
    handle.write("\n".join(lines).strip() + "\n")
os.chmod(target, 0o600)
PY
REMOTE_TOKEN
)" "$REUSE_TOKEN_FROM" "$SECRETS_FILE" "$TOKEN_VAR"
fi

# --- copy artifacts ---------------------------------------------------------

copy_code() {
  local dest="$1"
  "${RSYNC[@]}" "$REPO_ROOT/packages/web/dist/" "$HOST:$dest/web/"
  "${RSYNC[@]}" "$REPO_ROOT/packages/next-runner/dist/" "$HOST:$dest/runner/"
  if [[ "$BUILD_ON_HOST" -eq 1 ]]; then
    log "copying sources for a host-side build"
    "${SSH[@]}" "mkdir -p $(printf %q "$dest")/src"
    "${RSYNC[@]}" --exclude node_modules --exclude dist --exclude .git \
      "$REPO_ROOT/next" "$REPO_ROOT/packages" "$HOST:$dest/src/"
    remote_bash "$(cat <<'REMOTE_BUILD'
set -euo pipefail
DIR="$1"
NODE_BIN="$2"
[[ -n "$NODE_BIN" ]] && export PATH="$NODE_BIN:$PATH"
cd "$DIR/src"
(cd next/server && CGO_ENABLED=0 go build -trimpath -o "$DIR/bin/nex-next-server.new" ./cmd/nex-next-server)
pnpm --dir "$DIR/src" --filter @nex/web build
rm -rf "$DIR/web" && mv packages/web/dist "$DIR/web"
rm -rf src
REMOTE_BUILD
)" "$dest" "$NODE_BIN"
  else
    "${RSYNC[@]}" "$TMP_ROOT/nex-next-server" "$HOST:$dest/bin/nex-next-server.new"
    "${SSH[@]}" "chmod 0755 $(printf %q "$dest")/bin/nex-next-server.new"
  fi
}

# Install the staged binary, keeping the previous one as .prev. Runs as the SSH
# user (user mode) or through sudo (system mode, after the staged file has been
# moved into the root-owned bin dir).
swap_binary() {
  local dest="$1"
  remote_bash "$(cat <<'REMOTE_BINARY'
set -euo pipefail
DIR="$1"
[[ -f "$DIR/bin/nex-next-server.new" ]] || { echo "[deploy] no staged binary in $DIR/bin" >&2; exit 1; }
if [[ -f "$DIR/bin/nex-next-server" ]]; then
  cp -a "$DIR/bin/nex-next-server" "$DIR/bin/nex-next-server.prev"
fi
install -m 0755 "$DIR/bin/nex-next-server.new" "$DIR/bin/nex-next-server"
rm -f "$DIR/bin/nex-next-server.new"
REMOTE_BINARY
)" "$dest"
}

# Runner dependencies come from the shipped dist/package.json (pinned pi SDK).
install_runner_deps() {
  local dest="$1"
  remote_bash "$(cat <<'REMOTE_DEPS'
set -euo pipefail
DIR="$1"
NODE_BIN="$2"
[[ -n "$NODE_BIN" ]] && export PATH="$NODE_BIN:$PATH"
cd "$DIR/runner"
[[ -d node_modules ]] || npm install --omit=dev --no-audit --no-fund
REMOTE_DEPS
)" "$dest" "$NODE_BIN"
}

if [[ "$MODE" == "system" ]]; then
  log "copying artifacts to $HOST:$REMOTE_DIR_EXPANDED (root-owned code tree)"
  # .stage is owned by the SSH user so rsync needs no sudo; one rename per
  # component then moves it into place as root.
  remote_bash "$(cat <<'REMOTE_STAGE'
set -euo pipefail
DIR="$1"
mkdir -p "$DIR/.stage/web" "$DIR/.stage/runner" "$DIR/.stage/bin"
REMOTE_STAGE
)" "$REMOTE_DIR_EXPANDED"
  copy_code "$REMOTE_DIR_EXPANDED/.stage"
  install_runner_deps "$REMOTE_DIR_EXPANDED/.stage"
  remote_sudo_bash "$(cat <<'REMOTE_SWAP'
set -euo pipefail
DIR="$1"
# The binary is rsynced into the staging dir (owned by the SSH user); move it in.
if [[ -f "$DIR/.stage/bin/nex-next-server.new" ]]; then
  install -m 0755 "$DIR/.stage/bin/nex-next-server.new" "$DIR/bin/nex-next-server.new"
fi
# Keep the previous build for a one-command rollback, then swap in the new one.
if [[ -f "$DIR/bin/nex-next-server" ]]; then
  cp -a "$DIR/bin/nex-next-server" "$DIR/bin/nex-next-server.prev"
fi
if [[ -f "$DIR/bin/nex-next-server.new" ]]; then
  install -m 0755 "$DIR/bin/nex-next-server.new" "$DIR/bin/nex-next-server"
  rm -f "$DIR/bin/nex-next-server.new"
fi
rm -rf "$DIR/web.prev"
[[ -d "$DIR/web" ]] && mv "$DIR/web" "$DIR/web.prev"
mv "$DIR/.stage/web" "$DIR/web"
rm -rf "$DIR/runner.prev"
[[ -d "$DIR/runner" ]] && mv "$DIR/runner" "$DIR/runner.prev"
mv "$DIR/.stage/runner" "$DIR/runner"
rm -rf "$DIR/.stage"
chown -R root:root "$DIR/bin" "$DIR/web" "$DIR/runner"
chmod 0755 "$DIR" "$DIR/bin" "$DIR/web" "$DIR/runner"
REMOTE_SWAP
)" "$REMOTE_DIR_EXPANDED"
else
  log "copying artifacts to $HOST:$REMOTE_DIR_EXPANDED"
  copy_code "$REMOTE_DIR_EXPANDED"
  swap_binary "$REMOTE_DIR_EXPANDED"
  install_runner_deps "$REMOTE_DIR_EXPANDED"
fi

if [[ "$DO_START" -eq 0 ]]; then
  log "artifacts deployed; --no-start given"
  exit 0
fi

# --- start ------------------------------------------------------------------

if [[ "$MODE" == "system" ]]; then
  log "installing system units on $HOST"
  remote_sudo_bash "$(cat <<'REMOTE_SYSTEM_UNIT'
set -euo pipefail
DIR="$1"
PORT="$2"
LISTEN="$3"
WORKSPACE="$4"
SECRETS="$5"
DATA_DIR="$6"
RUNNER_DATA="$7"
NODE_BIN="$8"
ORIGINS="$9"
SERVICE_USER="${10}"
SERVICE_HOME="${11}"
RUNNER_NAME="${12}"
RUNNER_ID="${13}"
UNIT_DIR=/etc/systemd/system

NODE="$(command -v node)"
if [[ -n "$NODE_BIN" && -x "$NODE_BIN/node" ]]; then
  NODE="$NODE_BIN/node"
fi
NODE_MAJOR="$("$NODE" -p 'Number(process.versions.node.split(".")[0])')"
[[ "$NODE_MAJOR" -ge 24 ]] || { echo "[deploy] $NODE is not Node >= 24" >&2; exit 1; }

RUNNER_ID_ENV=""
if [[ -n "$RUNNER_ID" ]]; then
  RUNNER_ID_ENV="Environment=NEX_RUNNER_ID=$RUNNER_ID"
fi
ORIGIN_ENV=""
if [[ -n "$ORIGINS" ]]; then
  ORIGIN_ENV="Environment=NEX_ALLOWED_ORIGINS=$ORIGINS"
fi

cat >"$UNIT_DIR/nex-next-server.service" <<UNIT
[Unit]
Description=nex-next server (Go: web client, /ws and /runner)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
Environment=HOME=$SERVICE_HOME
EnvironmentFile=$SECRETS
$ORIGIN_ENV
ExecStart=$DIR/bin/nex-next-server -addr $LISTEN:$PORT -web-root $DIR/web -data-dir $DATA_DIR
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

cat >"$UNIT_DIR/nex-next-runner.service" <<UNIT
[Unit]
Description=nex-next runner (Node, pi agent sessions)
After=network-online.target nex-next-server.service
Wants=network-online.target nex-next-server.service

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
Environment=HOME=$SERVICE_HOME
Environment=NEX_SERVER_URL=ws://127.0.0.1:$PORT/runner
EnvironmentFile=$SECRETS
Environment=NEX_RUNNER_NAME=$RUNNER_NAME
$RUNNER_ID_ENV
Environment=NEX_RUNNER_WORKSPACES=$WORKSPACE
Environment=NEX_RUNNER_DATA_DIR=$RUNNER_DATA
WorkingDirectory=$WORKSPACE
ExecStart=$NODE $DIR/runner/runner.mjs
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable nex-next-server.service >/dev/null
systemctl enable nex-next-runner.service >/dev/null
systemctl restart nex-next-server.service
# The runner dials 127.0.0.1:$PORT; wait for the listener before starting it.
for _ in $(seq 1 100); do
  if timeout 1 bash -c "</dev/tcp/127.0.0.1/$PORT" 2>/dev/null; then break; fi
  sleep 0.2
done
systemctl restart nex-next-runner.service
REMOTE_SYSTEM_UNIT
)" "$REMOTE_DIR_EXPANDED" "$PORT" "$LISTEN" "$REMOTE_WORKSPACE" "$SECRETS_FILE" "$DATA_DIR" "$RUNNER_DATA_DIR" "$NODE_BIN" "$ALLOWED_ORIGINS" "$("${SSH[@]}" 'id -un')" "$REMOTE_HOME" "${RUNNER_NAME:-$("${SSH[@]}" 'hostname -s')}" "$RUNNER_ID"
else
  log "starting server and runner on $HOST (user units)"
  remote_bash "$(cat <<'REMOTE_START'
set -euo pipefail
DIR="$1"
PORT="$2"
LISTEN="$3"
NODE_BIN="$4"
SUPERVISOR="$5"
WORKSPACE="$6"
SECRETS="$7"
ORIGINS="$8"
[[ -n "$NODE_BIN" ]] && export PATH="$NODE_BIN:$PATH"
set -a
# shellcheck disable=SC1090
. "$SECRETS"
set +a

node_major="$(node -p 'Number(process.versions.node.split(".")[0])')"
[[ "$node_major" -ge 24 ]] || { echo "[deploy] remote node must be >= 24" >&2; exit 1; }

if [[ "$SUPERVISOR" == "auto" ]]; then
  if [[ "$(loginctl show-user "$USER" -p Linger 2>/dev/null || true)" == "Linger=yes" ]]; then
    SUPERVISOR="systemd"
  else
    SUPERVISOR="setsid"
  fi
fi
printf '%s\n' "$SUPERVISOR" >"$DIR/supervisor"
printf '%s\n' "$PORT" >"$DIR/port"
printf '%s\n' "$LISTEN" >"$DIR/listen"
printf '%s\n' "$WORKSPACE" >"$DIR/workspace.path"
if [[ -n "$ORIGINS" ]]; then
  printf '%s\n' "$ORIGINS" >"$DIR/allowed-origins"
else
  rm -f "$DIR/allowed-origins"
fi

# Per-component scripts so the server and the runner restart independently (the
# cross-machine E2E stops only the runner).
cat >"$DIR/bin/start-server.sh" <<'SRV'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="$(cat "$DIR/port")"
LISTEN="$(cat "$DIR/listen")"
set -a
# shellcheck disable=SC1090
. "$DIR/secrets.env"
set +a
ORIGINS=()
if [[ -f "$DIR/allowed-origins" ]]; then
  ORIGINS=(--setenv="NEX_ALLOWED_ORIGINS=$(cat "$DIR/allowed-origins")")
fi
SUPERVISOR="$(cat "$DIR/supervisor")"
if [[ "$SUPERVISOR" == "systemd" ]]; then
  systemctl --user stop nex-next-server.service 2>/dev/null || true
  systemd-run --user --unit=nex-next-server --collect \
    --working-directory="$DIR" \
    --setenv=NEX_SERVER_AUTH_TOKEN="$NEX_NEXT_BROWSER_TOKEN" \
    --setenv=NEX_RUNNER_TOKEN="$NEX_NEXT_RUNNER_TOKEN" \
    "${ORIGINS[@]}" \
    "$DIR/bin/nex-next-server" \
    -addr "$LISTEN:$PORT" -web-root "$DIR/web" -data-dir "$DIR/data/server"
else
  cd "$DIR"
  NEX_SERVER_AUTH_TOKEN="$NEX_NEXT_BROWSER_TOKEN" \
  NEX_RUNNER_TOKEN="$NEX_NEXT_RUNNER_TOKEN" \
  setsid nohup "$DIR/bin/nex-next-server" \
    -addr "$LISTEN:$PORT" -web-root "$DIR/web" -data-dir "$DIR/data/server" \
    >>"$DIR/logs/server.log" 2>&1 </dev/null &
  echo $! >"$DIR/server.pid"
fi
SRV
  chmod +x "$DIR/bin/start-server.sh"

  cat >"$DIR/bin/start-runner.sh" <<'RUN'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="$(cat "$DIR/port")"
WS="$(cat "$DIR/workspace.path")"
set -a
# shellcheck disable=SC1090
. "$DIR/secrets.env"
set +a
SUPERVISOR="$(cat "$DIR/supervisor")"
if [[ "$SUPERVISOR" == "systemd" ]]; then
  systemctl --user stop nex-next-runner.service 2>/dev/null || true
  systemd-run --user --unit=nex-next-runner --collect \
    --working-directory="$WS" \
    --setenv=NEX_SERVER_URL="ws://127.0.0.1:$PORT/runner" \
    --setenv=NEX_RUNNER_TOKEN="$NEX_NEXT_RUNNER_TOKEN" \
    --setenv=NEX_RUNNER_NAME="${NEX_RUNNER_NAME:-$(hostname -s)}" \
    --setenv=NEX_RUNNER_WORKSPACES="$WS" \
    --setenv=NEX_RUNNER_DATA_DIR="$DIR/data/runner" \
    node "$DIR/runner/runner.mjs"
else
  cd "$WS"
  NEX_SERVER_URL="ws://127.0.0.1:$PORT/runner" \
  NEX_RUNNER_TOKEN="$NEX_NEXT_RUNNER_TOKEN" \
  NEX_RUNNER_NAME="${NEX_RUNNER_NAME:-$(hostname -s)}" \
  NEX_RUNNER_WORKSPACES="$WS" \
  NEX_RUNNER_DATA_DIR="$DIR/data/runner" \
  setsid nohup node "$DIR/runner/runner.mjs" >>"$DIR/logs/runner.log" 2>&1 </dev/null &
  echo $! >"$DIR/runner.pid"
fi
RUN
  chmod +x "$DIR/bin/start-runner.sh"

  for component in server runner; do
    cat >"$DIR/bin/stop-$component.sh" <<'STOP'
#!/usr/bin/env bash
set -uo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="$(basename "$0" .sh)"
NAME="${NAME#stop-}"
systemctl --user stop "nex-next-$NAME.service" 2>/dev/null || true
if [[ -f "$DIR/$NAME.pid" ]]; then
  pid="$(cat "$DIR/$NAME.pid")"
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  kill -KILL "$pid" 2>/dev/null || true
  rm -f "$DIR/$NAME.pid"
fi
STOP
    chmod +x "$DIR/bin/stop-$component.sh"
  done

  cat >"$DIR/bin/start.sh" <<'START'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$DIR/bin/start-server.sh"
sleep 1
"$DIR/bin/start-runner.sh"
START
  chmod +x "$DIR/bin/start.sh"

  cat >"$DIR/bin/stop.sh" <<'STOPALL'
#!/usr/bin/env bash
set -uo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$DIR/bin/stop-runner.sh"
"$DIR/bin/stop-server.sh"
STOPALL
  chmod +x "$DIR/bin/stop.sh"

  cat >"$DIR/bin/restart.sh" <<'RESTART'
#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$DIR/bin/stop.sh" || true
exec "$DIR/bin/start.sh"
RESTART
  chmod +x "$DIR/bin/restart.sh"

"$DIR/bin/stop.sh" >/dev/null 2>&1 || true
"$DIR/bin/start-server.sh"
sleep 1
"$DIR/bin/start-runner.sh"
echo "[deploy] supervisor: $SUPERVISOR"
REMOTE_START
)" "$REMOTE_DIR_EXPANDED" "$PORT" "$LISTEN" "$NODE_BIN" "$SUPERVISOR" "$REMOTE_WORKSPACE" "$SECRETS_FILE" "$ALLOWED_ORIGINS"
fi

# --- health check -----------------------------------------------------------

log "waiting for /api/server-info on port $PORT"
remote_bash "$(cat <<'REMOTE_HEALTH'
set -euo pipefail
SECRETS="$1"
PORT="$2"
set -a
# shellcheck disable=SC1090
. "$SECRETS"
set +a
TOKEN="${NEX_SERVER_AUTH_TOKEN:-${NEX_NEXT_BROWSER_TOKEN:-}}"
for _ in $(seq 1 60); do
  if payload="$(curl -fsS "http://127.0.0.1:$PORT/api/server-info?token=$TOKEN" 2>/dev/null)"; then
    printf '%s' "$payload" | python3 -c 'import json,sys;d=json.load(sys.stdin);print("[deploy] server",d.get("version"),"| workspaces:",len(d.get("workspaces") or []))'
    exit 0
  fi
  sleep 2
done
echo "[deploy] server did not become healthy; check its journal/log" >&2
exit 1
REMOTE_HEALTH
)" "$SECRETS_FILE" "$PORT"

if [[ "$MODE" == "system" ]]; then
  log "done. Sign in at the browser-facing host; the login token is NEX_SERVER_AUTH_TOKEN in $SECRETS_FILE"
else
  log "done. Open http://<host>:$PORT and sign in with NEX_NEXT_BROWSER_TOKEN from $SECRETS_FILE"
fi
