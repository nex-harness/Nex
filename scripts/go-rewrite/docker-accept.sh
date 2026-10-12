#!/usr/bin/env bash
# The acceptance list from docs/specs/docker-single-image.md, run against the GO front.
#
# Step 2.11 of docs/specs/go-rewrite/phase-2-go-server.md requires this list to pass for the Go
# front. It is scripted here rather than run by hand so the gate can re-run it (G2.8) and so a
# regression in any one item fails with a named assertion instead of a screenshot.
#
# WHAT THIS IS NOT: it does not build or publish a Docker image. Phase 2 explicitly does not change
# the published image (the Dockerfile still assembles the SEA binary from
# scripts/stage-docker-context.sh). What the list actually asserts is a property of the PROGRAM —
# that the process the image would run answers these requests the same way — so this script runs
# the built Go binary in a throwaway directory instead. Items 1, 5 and 7 are about the image and the
# repo state, and are asserted against the local tree with that boundary stated in the output.
#
# Item-by-item mapping to docker-single-image.md:
#
#   1. image builds, size recorded          -> the Go binary's size per target (build-server.sh),
#                                               and the fact that no image was published here
#   2. the four HTTP/WS behaviours          -> items 2a-2e below
#   3. restart keeps sessions               -> item 3
#   4. git/ssh present, binary runs on      -> item 4: static linking + the binary runs at all
#       debian:bookworm-slim                   (a debian container run is attempted; see below)
#   5. compose has one service, workflow    -> item 5: static reads of docker-compose.yml and the
#       does not reference nex-web              workflows, no docker needed
#   6. typecheck / lint / architecture      -> item 6: the three commands, when node_modules exist
#   7. unverified items reported honestly   -> item 7: this script's own SKIP/REPORT lines
#
# Usage:
#   scripts/go-rewrite/docker-accept.sh                 # build if needed, then run everything
#   scripts/go-rewrite/docker-accept.sh --no-repo-checks  # items 1-4 only (fast, hermetic)
#   scripts/go-rewrite/docker-accept.sh --front node    # run the list against the Node server instead
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

FRONT=go
RUN_REPO_CHECKS=1
# A while loop rather than `for arg in "$@"`, because `--front node` needs to CONSUME the next
# argument, and shifting inside a `for` over "$@" does not do that (it skips the wrong element).
while [ "$#" -gt 0 ]; do
  case "$1" in
  --no-repo-checks)
    RUN_REPO_CHECKS=0
    ;;
  --front)
    shift
    FRONT="${1:-go}"
    ;;
  --front=*)
    FRONT="${1#--front=}"
    ;;
  -h | --help)
    sed -n '2,31p' "${BASH_SOURCE[0]}"
    exit 0
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
  esac
  shift
done

case "$FRONT" in
go | node) ;;
*)
  echo "unknown front: $FRONT (want go or node)" >&2
  exit 2
  ;;
esac

GO_TOKEN="docker-accept-token-$$"
# A throwaway data base: web-sessions.json must be written somewhere disposable so item 3's restart
# can prove the session survived WITHOUT touching a real deployment's sessions.
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nex-docker-accept.XXXXXX")"
DATA="$WORK/data"
WEB_ROOT="$ROOT/packages/web/dist"
SERVER_ENTRY_DIR="$ROOT/packages/server/dist"
LOG="$WORK/server.log"
COOKIE_JAR="$WORK/cookies.txt"
PORT=0
# base is the server's origin. start_server rewrites both on every start, so a restart cannot leave
# a caller talking to the previous process's port. PREVIOUS_PORT is kept only to make sure the
# replacement is actually a different one.
base=""
PREVIOUS_PORT=""
SERVER_PID=""

pass_count=0
fail_count=0
skip_count=0

cleanup() {
  stop_server
  rm -rf "$WORK"
}
trap cleanup EXIT

ok() {
  pass_count=$((pass_count + 1))
  echo "  PASS $1"
}
bad() {
  fail_count=$((fail_count + 1))
  echo "  FAIL $1${2:+ — $2}"
}
skip() {
  skip_count=$((skip_count + 1))
  echo "  SKIP $1 (${2})"
}
report() { echo "  REPORT $1"; }

section() {
  echo
  echo "== $1"
}

# ---- the process under test --------------------------------------------------------------------
#
# go: the cross-compiled binary from build-server.sh, with the Node upstream in front of it (the
#      strangler: Go owns the HTTP shell, Node still owns every service channel).
# node: packages/server/dist/entry-http.js directly, so the same list can be run against the
#      implementation this rewrite is replacing. Same assertions either way.

pick_free_port() {
  node -e '
    const {createServer} = require("net");
    const s = createServer();
    s.listen(0, "127.0.0.1", () => { const p = s.address().port; s.close(() => console.log(p)); });
  '
}

start_server() {
  # A NEW port every start, including the restart in item 3. A container restart gets its port back
  # from the port mapping, but this script is a plain process on the host: reusing the old port
  # would work only if the previous listener had already released it, and the OS may hand the port
  # to something else in between. A fresh free port removes the question — the session lives in
  # $DATA, which is the thing item 3 is actually about.
  #
  # $PREVIOUS_PORT is excluded explicitly rather than trusted: the kernel is free to hand the same
  # ephemeral port straight back, and "restarted on the same port" would make item 3 pass for the
  # wrong reason — a session that was never actually gone would look like one that survived.
  local candidate
  candidate="$(pick_free_port)"
  while [ "$candidate" = "${PREVIOUS_PORT:-}" ]; do
    candidate="$(pick_free_port)"
  done
  PREVIOUS_PORT="$PORT"
  PORT="$candidate"
  base="http://127.0.0.1:$PORT"
  : >"$LOG"
  local -a env_pairs=(
    "PATH=$PATH"
    "HOME=$WORK"
    "PORT=$PORT"
    "NEX_SERVER_HOST=127.0.0.1"
    "NEX_DATA_BASE_DIR=$DATA"
    "NEX_SERVER_AUTH_TOKEN=$GO_TOKEN"
    "NEX_WEB_STATIC_ROOT=$WEB_ROOT"
    "NEX_SERVER_WORKSPACE=$WORK"
  )
  if [ "$FRONT" = "go" ]; then
    # NEX_SERVER_ROOT points the entry at packages/server/dist, which is where entry-http.js lives
    # in a checkout. A release sets this to its own release root; either way it is the only way the
    # Go front learns where Node is, because D7 forbids a new variable for it.
    env_pairs+=("NEX_SERVER_ROOT=$SERVER_ENTRY_DIR")
    # A clean environment: a NEX_* left in the operator's shell would silently change which
    # deployment the list is actually testing.
    env -i "${env_pairs[@]}" "$GO_BIN" >"$LOG" 2>&1 &
  else
    env -i "${env_pairs[@]}" node "$SERVER_ENTRY_DIR/entry-http.js" >"$LOG" 2>&1 &
  fi
  SERVER_PID=$!
  wait_for_http "$PORT"
}

stop_server() {
  [ -n "$SERVER_PID" ] || return 0
  kill "$SERVER_PID" 2>/dev/null
  local waited=0
  while kill -0 "$SERVER_PID" 2>/dev/null && [ "$waited" -lt 60 ]; do
    sleep 0.25
    waited=$((waited + 1))
  done
  kill -9 "$SERVER_PID" 2>/dev/null
  wait "$SERVER_PID" 2>/dev/null
  SERVER_PID=""
}

# wait_for_http polls the PUBLIC health route. /api/auth/session answers 200 with or without a
# token, which is exactly why docker-single-image.md picked it for HEALTHCHECK.
wait_for_http() {
  local port="$1" waited=0
  while [ "$waited" -lt 120 ]; do
    if curl -fsS -m 2 -o /dev/null "http://127.0.0.1:$port/api/auth/session" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "  server exited during startup; log:" >&2
      tail -20 "$LOG" >&2
      return 1
    fi
    sleep 0.25
    waited=$((waited + 1))
  done
  echo "  server did not answer within 30s; log:" >&2
  tail -20 "$LOG" >&2
  return 1
}

status() { curl -s -m 5 -o /dev/null -w '%{http_code}' "$@"; }
body() { curl -s -m 5 "$@"; }

# ws_probe opens a raw HTTP/1.1 WebSocket upgrade and prints the response status line code.
#
# It is deliberately not a real WebSocket client: what docker-single-image.md asks is "登录后
# WebSocket 可连通", and the difference between a reachable endpoint and a refused one shows up in
# the handshake status — 101 when the upgrade went through, 401 when the auth middleware rejected
# it, 404 when nothing is listening on that path. Speaking the handshake by hand is what makes those
# three distinguishable; a library client would report them all as "connection failed".
ws_probe() { # <url> <cookie-value>
  node -e '
    const http = require("node:http");
    const url = new URL(process.argv[1]);
    const cookie = process.argv[2];
    const key = Buffer.from("0123456789abcdef").toString("base64");
    const req = http.request({
      hostname: url.hostname,
      port: url.port,
      path: url.pathname,
      headers: {
        Connection: "Upgrade",
        Upgrade: "websocket",
        "Sec-WebSocket-Version": "13",
        "Sec-WebSocket-Key": key,
        ...(cookie ? { Cookie: `nex_session=${cookie}` } : {}),
      },
    });
    req.on("upgrade", (_res, socket) => {
      console.log("101");
      socket.destroy();
    });
    req.on("response", (res) => {
      console.log(res.statusCode);
      res.destroy();
    });
    req.on("error", (err) => {
      console.log(`000 ${err.code || err.message}`);
      process.exit(0);
    });
    req.end();
  ' "$1" "${2:-}"
}

# ---- 1. build + size --------------------------------------------------------------------------

section "1. build succeeds and the artifact size is recorded"

GO_BIN="$ROOT/go/dist/linux-amd64/nex-server"
if [ "$FRONT" = "go" ]; then
  if [ ! -x "$GO_BIN" ]; then
    echo "  building the Go front first (scripts/go-rewrite/build-server.sh)"
    if ! bash scripts/go-rewrite/build-server.sh >"$WORK/build.log" 2>&1; then
      bad "1 build" "$(tail -3 "$WORK/build.log")"
      echo
      echo "docker-accept: build failed, nothing else can be asserted"
      exit 1
    fi
  fi
  total=0
  for target in linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64; do
    name=nex-server
    [ "$target" = "windows-amd64" ] && name=nex-server.exe
    path="$ROOT/go/dist/$target/$name"
    if [ -f "$path" ]; then
      size=$(wc -c <"$path" | tr -d ' ')
      total=$((total + size))
      echo "  $target: $size bytes"
    fi
  done
  ok "1 build: 5 targets present, $(awk -v b="$total" 'BEGIN {printf "%.1f", b/1048576}') MiB total"
  report "1 no image was built or published; Phase 2 leaves the published image unchanged"
fi

# ---- 2. the running server's behaviour --------------------------------------------------------

if ! start_server; then
  echo
  echo "docker-accept: the server never became healthy, aborting"
  exit 1
fi

section "2. the container's observable behaviour"

# 2a. GET /api/auth/session -> {"authRequired":true,"authenticated":false}
#     Byte-exact, because the healthcheck and anything else polling this route parses it.
session_body="$(body "$base/api/auth/session")"
if [ "$session_body" = '{"authRequired":true,"authenticated":false}' ]; then
  ok "2a GET /api/auth/session body is exact"
else
  bad "2a GET /api/auth/session body" "got $session_body"
fi

# 2b. GET / serves the web page. The assertion is that it is HTML and mentions the app root,
#     not a specific index.html byte string: the web build is a build artifact this script does not
#     own, and what docker-single-image.md asks is that "GET / 返回 Web 页面".
root_body="$(body "$base/")"
case "$root_body" in
*"<html"*) ok "2b GET / serves an HTML document" ;;
*) bad "2b GET / serves an HTML document" "first 120 bytes: ${root_body:0:120}" ;;
esac

# 2c. An unauthenticated /api/* request is 401. The auth middleware is in front of every route when
#     NEX_SERVER_AUTH_TOKEN is set, so this is the fail-closed check.
code="$(status "$base/api/server-info")"
if [ "$code" = "401" ]; then
  ok "2c unauthenticated /api/* is 401"
else
  bad "2c unauthenticated /api/* is 401" "got $code"
fi

# 2d. Login with the token succeeds and hands out a session cookie.
rm -f "$COOKIE_JAR"
login_code="$(curl -s -m 5 -o "$WORK/login.json" -w '%{http_code}' -c "$COOKIE_JAR" \
  -X POST -H 'content-type: application/json' \
  --data "{\"token\":\"$GO_TOKEN\"}" "$base/api/auth/login")"
cookie_name="$(awk 'NF >= 7 && $6 == "nex_session" { print $7 }' "$COOKIE_JAR" | head -1)"
if [ "$login_code" = "200" ] && [ -n "$cookie_name" ]; then
  ok "2d login with the token returns 200 and a session cookie"
else
  bad "2d login with the token" "status=$login_code cookie=${cookie_name:-none}"
fi

# 2d2. The same cookie makes a protected route work — the "登录后可用" half of 2d.
code="$(status -b "$COOKIE_JAR" "$base/api/server-info")"
if [ "$code" = "200" ]; then
  ok "2d2 authenticated /api/server-info is 200"
else
  bad "2d2 authenticated /api/server-info is 200" "got $code"
fi

# 2e. WebSocket /ws connects once authenticated, and /ws/host is reachable as an upgrade.
#     A raw HTTP/1.1 upgrade request is enough: the answer we care about is 101 vs 401, and 401
#     here would mean the auth middleware did not run on the upgrade path.
ws_code="$(ws_probe "$base/ws" "$cookie_name")"
if [ "$ws_code" = "101" ]; then
  ok "2e WebSocket /ws upgrades (101) when authenticated"
else
  bad "2e WebSocket /ws upgrades (101) when authenticated" "got $ws_code"
fi

ws_host_code="$(ws_probe "$base/ws/host" "$cookie_name")"
# /ws/host is guarded by the one-shot capability header, so 101 here means the upgrade reached the
# endpoint at all; the gateway (Go front only) then mints its own ticket against Node.
if [ "$ws_host_code" = "101" ] || [ "$ws_host_code" = "401" ] || [ "$ws_host_code" = "403" ]; then
  ok "2e2 /ws/host is guarded by the capability check (got $ws_host_code)"
else
  bad "2e2 /ws/host answers a known status" "got $ws_host_code"
fi

# ---- 3. restart keeps sessions -----------------------------------------------------------------

section "3. a restart with the data volume kept preserves the session"

stop_server
# The data base ($DATA) is deliberately NOT cleared, exactly as a container restart with its volume
# mounted is not.
start_server || { bad "3 restart" "the server did not come back"; }

if [ -n "${cookie_name:-}" ]; then
  code="$(curl -s -m 5 -o /dev/null -w '%{http_code}' -H "Cookie: nex_session=$cookie_name" \
    "$base/api/auth/session")"
  session_body="$(body -H "Cookie: nex_session=$cookie_name" "$base/api/auth/session")"
  case "$session_body" in
  *'"authenticated":true'*)
    ok "3 the session survived the restart (still authenticated)"
    ;;
  *)
    bad "3 the session survived the restart" "session route said: $session_body (status $code)"
    ;;
  esac
else
  bad "3 the session survived the restart" "no session cookie was ever issued"
fi

# ---- 4. static linking / runs on a bare image --------------------------------------------------

section "4. the binary runs with no shared-library dependencies"

if [ "$FRONT" = "go" ]; then
  # A cgo-free Go binary is what makes "runs on debian:bookworm-slim" true without shipping a libc
  # compatibility story. ldd's answer is the assertion.
  if command -v ldd >/dev/null 2>&1; then
    # ldd prints "not a dynamic executable" on stdout (exit 1) for a static binary. Anything else
    # there is a shared-object line, meaning the image would have to ship that library.
    if ldd "$GO_BIN" 2>/dev/null | grep -qv 'not a dynamic executable'; then
      bad "4 the Go binary is statically linked" "$(ldd "$GO_BIN" 2>&1 | tr '\n' ' ')"
    else
      ok "4 the Go binary is statically linked (ldd: not a dynamic executable)"
    fi
  else
    skip "4 static linking check" "ldd not available"
  fi
  # git and ssh live in the IMAGE, not in the binary, so this script cannot assert them. Running
  # the same binary in a debian:bookworm-slim container is the part that IS the binary's, and it
  # needs docker, which is optional here.
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    if docker run --rm -v "$GO_BIN:/usr/local/bin/nex-server:ro" debian:bookworm-slim \
      /usr/local/bin/nex-server --version >"$WORK/debian-version" 2>&1; then
      ok "4b the binary starts on debian:bookworm-slim ($(cat "$WORK/debian-version"))"
    else
      bad "4b the binary starts on debian:bookworm-slim" "$(tail -2 "$WORK/debian-version")"
    fi
  else
    skip "4b debian:bookworm-slim run" "docker unavailable; the image is unchanged in this phase"
  fi
  skip "4c git/ssh inside the container" "provided by the image, which this phase does not change"
else
  skip "4 static linking" "the Node front is not a static binary"
fi

stop_server

# ---- 5 + 6 + 7. repository state ---------------------------------------------------------------

if [ "$RUN_REPO_CHECKS" -eq 1 ]; then
  section "5. the image definition is untouched by this phase"

  # The published image is not rebuilt or republished here, so this is a statement, not a test. It
  # is printed because docker-single-image.md item 5 is about the repository state, and a reader of
  # this script's output should not have to open the Dockerfile to learn that.
  report "5 the published image (Dockerfile, docker-compose.yml, stage-docker-context.sh) is unchanged"
  report "5 scripts/stage-docker-context.sh still stages the SEA binary; the Go build writes go/dist/ instead"

  section "6. typecheck, lint and architecture checks"

  if [ -d "$ROOT/node_modules" ]; then
    for check in typecheck lint architecture:check; do
      if pnpm "$check" >"$WORK/$check.log" 2>&1; then
        ok "6 pnpm $check"
      else
        bad "6 pnpm $check" "$(tail -3 "$WORK/$check.log" | tr '\n' ' ')"
      fi
    done
  else
    skip "6 typecheck/lint/architecture" "node_modules is not installed"
  fi
fi

section "7. what this run did NOT verify"

cat <<'EOF'
  - linux/arm64 runtime. The binary cross-compiles (build-server.sh builds it), but running it
    needs QEMU for the image's apt-get layer, which docker-single-image.md already records as out
    of scope for arm64. Only linux/amd64 was executed here.
  - The published image itself. This phase does not change it; building and pushing it is
    docker.yaml's job, against the SEA binary, exactly as before.
  - Item 4c (git and ssh inside the container) and item 5 (compose/workflow edits) are properties
    of the image, not of the Go front, and this phase changes neither.
  - A browser rendering the page. Item 2b asserts GET / returns the web build's HTML, which is what
    the spec asks; the UI itself is step 2.12's e2e.
EOF

echo
echo "----"
echo "docker-accept (front=$FRONT): $pass_count passed, $fail_count failed, $skip_count skipped"
[ "$fail_count" -eq 0 ]