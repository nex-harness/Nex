#!/usr/bin/env bash
# Phase gate runner (docs/specs/go-rewrite). Usage: scripts/go-rewrite/gate.sh <phase>
# Prints "PASS|FAIL|SKIP|TODO <id> <text>" for every check; exits non-zero if any check is FAIL or TODO.
#   PASS  the command ran and met its condition
#   FAIL  the command ran and did not meet it
#   SKIP  the check cannot run in this environment (missing tool); never counts as a pass in the summary
#   TODO  the thing being checked is not built yet
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
PHASE="${1:-}"
[ -n "$PHASE" ] || { echo "usage: $0 <phase 0-5>" >&2; exit 2; }
PASS=0; FAIL=0; TODO=0; SKIP=0
LOG_DIR="${GATE_LOG_DIR:-/tmp/go-rewrite-gate}"; mkdir -p "$LOG_DIR"

check() { # check <id> <description> <command...>
  local id="$1" desc="$2"; shift 2
  if "$@" >"$LOG_DIR/$id.log" 2>&1; then echo "PASS $id $desc"; PASS=$((PASS+1))
  else echo "FAIL $id $desc   (log: $LOG_DIR/$id.log)"; tail -3 "$LOG_DIR/$id.log" | sed 's/^/       /'; FAIL=$((FAIL+1)); fi
}
todo() { echo "TODO $1 $2"; TODO=$((TODO+1)); }
skip() { echo "SKIP $1 $2 ($3)"; SKIP=$((SKIP+1)); }
have() { command -v "$1" >/dev/null 2>&1; }
pnpm_run() { if have pnpm; then pnpm "$@"; else npx --yes pnpm@10.33.2 "$@"; fi; }

# ---- common block (GN.0): the checks every phase repeats --------------------------------------
common_block() {
  local p="$1"
  if have go; then
    check "G$p.0.gofmt" "gofmt clean" bash -c 'cd go && test -z "$(gofmt -l .)"'
    check "G$p.0.vet" "go vet" bash -c 'cd go && go vet ./...'
    check "G$p.0.test" "go test -race" bash -c 'cd go && go test -race -count=1 ./...'
    check "G$p.0.xbuild" "cross-build 5 targets" bash -c 'cd go && for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -buildvcs=false -o /dev/null ./cmd/nex-agent || exit 1; done'
  else skip "G$p.0.go" "go checks" "go not installed"; fi
  if [ -d node_modules ]; then
    check "G$p.0.typecheck" "pnpm typecheck" pnpm_run typecheck
    check "G$p.0.lint" "pnpm lint" pnpm_run lint
    check "G$p.0.arch" "pnpm architecture:check" pnpm_run architecture:check
    # fmt:check fails on pre-existing files (docs/specs/go-rewrite/baseline.md); only our own paths are gated.
    check "G$p.0.fmt-own" "oxfmt on files this project owns" pnpm_run exec oxfmt --check docs/specs/go-rewrite scripts/go-rewrite
  else skip "G$p.0.node" "typecheck/lint/arch/fmt" "run pnpm install first"; fi
}

gate0() {
  common_block 0
  check G0.1 "baseline.md exists" test -s docs/specs/go-rewrite/baseline.md
  check G0.2 "decisions.md has no TBD cells" bash -c '! grep -nw "TBD" docs/specs/go-rewrite/decisions.md'
  # decisions that are still open are reported, not hidden
  if grep -q "| open" docs/specs/go-rewrite/decisions.md; then echo "TODO G0.2b $(grep -c '| open' docs/specs/go-rewrite/decisions.md) decisions are still open (see decisions.md)"; TODO=$((TODO+1)); fi
  check G0.3 "go build + test" bash -c 'cd go && go build -buildvcs=false ./... && go test -count=1 ./...'
  check G0.4 "schema export: 138 schemas, manifest, method tables" \
    bash -c 'cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/export-schemas.mts >/dev/null && test -s ../../go/contracts/schema/manifest.json'
  check G0.5 "contracts: regenerated == committed (drift)" bash scripts/go-rewrite/check-contracts-drift.sh
  check G0.6 "Go validators agree with zod on the corpus" bash -c 'cd go && go test -count=1 ./contracts/'
  check G0.9 "RPC codec vectors + recorded traces replay byte-identically (R10 auth not recorded)" \
    bash -c 'cd go && go test -count=1 ./internal/rpc/ && cd .. && node scripts/go-rewrite/record/record-rpc.mjs >/dev/null && node --test scripts/go-rewrite/record/record-rpc.test.mjs'
  check G0.11 "inventory regenerates byte-identically" \
    bash -c 'cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/make-inventory.mts >/dev/null && cd ../.. && node --test scripts/go-rewrite/make-inventory.test.mjs'
  check G0.7 "recorder/normalizer/mock/scrub/coverage tests" node --test scripts/go-rewrite/record/mock-model.test.mjs scripts/go-rewrite/record/record-agent-session.test.mjs scripts/go-rewrite/scrub-check.test.mjs scripts/go-rewrite/coverage/check-oracle-coverage.test.mjs
  if [ -f apps/nex-cli/packages/cli/dist/nex.cjs ]; then
    check G0.8 "Node agent replays all committed traces" node scripts/go-rewrite/record/replay-agent.mjs --all
  else skip G0.8 "Node agent replay" "build @nex/cli first"; fi
  check G0.10 "no secret-shaped strings in test data" node scripts/go-rewrite/scrub-check.mjs go/testdata
  # The self-test runs this script with damaged inputs; GATE_SELFTEST stops it from recursing into itself.
  if [ -n "${GATE_SELFTEST:-}" ]; then :; else check G0.12 "gate fails when what it guards is broken" env GATE_SELFTEST=1 scripts/go-rewrite/gate.test.sh; fi
  # G0.13: phase1-owned must-match files must meet the 80% threshold or carry an
  # approved exception in scripts/go-rewrite/coverage/exceptions.json. Phase-4-owned
  # files are not yet in scope and are excluded via --phase phase1.
  check G0.13 "oracle coverage: phase-1 must-match files meet 80% or have approved exceptions" bash -c '
    set -e
    COV_DIR=$(mktemp -d /tmp/nex-cov-gate.XXXXXX)
    trap "rm -rf $COV_DIR" EXIT
    NEX_COVERAGE_SKIP_REPORT=1 bash scripts/go-rewrite/coverage/run-vector-coverage.sh "$COV_DIR" >/dev/null
    node scripts/go-rewrite/coverage/check-oracle-coverage.mjs "" --vectors "$COV_DIR" --phase phase1 --exceptions scripts/go-rewrite/coverage/exceptions.json >/dev/null
  '
}

gate1() {
  common_block 1
  # Entry criterion: gate 0 must pass. Reported, not hidden, because it does not today.
  if GATE_SELFTEST=1 "$0" 0 >"$LOG_DIR/G1.1.log" 2>&1; then echo "PASS G1.1 gate 0 passes"; PASS=$((PASS+1))
  else echo "FAIL G1.1 gate 0 does not pass yet (phase 1 entry criterion)   (log: $LOG_DIR/G1.1.log)"; FAIL=$((FAIL+1)); fi
  check G1.3 "store: migrations, ledger, rollback, tamper/newer refusal" bash -c 'cd go && go test -count=1 ./internal/adapters/store/'
  check G1.5 "wire vectors: crc32/base64/encoder agree with the TS code" bash -c 'cd go && go test -count=1 -run "CRC32|Encoder|Limits|AppendJS" ./internal/protocol/'
  check G1.stdio "stdio framing, envelope vs zod, process-level agent tests" bash -c 'cd go && go test -race -count=1 ./cmd/nex-agent/ ./internal/protocol/'
  if have node && [ -f apps/nex-cli/packages/cli/dist/nex.cjs ]; then
    mkdir -p /tmp/go-rewrite-bin && (cd go && go build -buildvcs=false -o /tmp/go-rewrite-bin/nex-agent ./cmd/nex-agent)
    check G1.4 "Node and Go create identical databases and open each other's" node scripts/go-rewrite/crosscheck-store.mjs /tmp/go-rewrite-bin/nex-agent
    # G1.2: every recorded scenario, one line each, so partial progress is visible and a pass is never implied.
    for dir in go/testdata/traces/agent/*/; do
      id="$(basename "$dir")"
      check "G1.2.$id" "Go agent reproduces recorded scenario $id" node scripts/go-rewrite/record/replay-agent.mjs --scenario "$id" --agent /tmp/go-rewrite-bin/nex-agent
    done
  else skip G1.2 "scenario replay against the Go agent" "build @nex/cli first"; fi
  check G1.7a "system prompt + meta-user context: 40 TS-derived vectors" bash -c 'cd go && go test -count=1 ./internal/promptctx/'
  check G1.7b "model request body byte-identical to Node's; 20 tool defs in order" bash -c 'cd go && go test -count=1 ./internal/modelreq/'
  check G1.6 "v4 conversation topic: snapshot, deltas, resume, both client modes (step 1.10)" bash -c 'cd go && go test -count=1 -run "BothModes|Subscribe|Snapshot|Resume|Scenarios|Backpressure|Fault|Filter|Coalesce|LegacyEncode|Clamp" ./internal/v4pub/'
  check G1.7 "model layer: SSE streaming, tool-call assembly, failure classifier (106 Node vectors), retry (step 1.6)" bash -c 'cd go && go test -count=1 ./internal/modelclient/'
  # G1.8: Read/Write/Edit replay the vectors produced by the real TypeScript handlers
  # (scripts/go-rewrite/gen-tool-vectors.mts). replayVectors fails when a tool has
  # fewer than 30 *asserting* cases, so the step-1.8 floor is enforced, not assumed.
  # The drift check regenerates the vectors and requires byte-identical output, so a
  # hand-edited vector file cannot pass. It needs tsx; without it the whole check is a
  # SKIP (never a silent pass).
  if [ -x node_modules/.bin/tsx ]; then
    check G1.8 "tool differential vectors agree with the TS handlers (Read/Write/Edit >= 30 asserting cases each)" \
      bash -c 'cd go && go test -count=1 -run "TestReadVectors|TestWriteVectors|TestEditVectors|TestMatcherVectors|TestDiffVectors|TestValidationVectors|TestToolMetadataMatchesTypeScript|TestResultBudgetMatchesTypeScript|TestReadStateKeyVectors|TestScenarioToolResultsMatchRecording" ./internal/tools/ && cd .. && node scripts/go-rewrite/check-tool-vector-drift.mjs'
  else
    skip G1.8 "tool differential vectors" "node_modules/.bin/tsx not installed"
  fi
  # G1.9 is about the state machine's own tests and its fuzz run. The common block
  # already ran the whole suite, so these narrow to what it cannot see: the
  # exhaustive (state, event) table and the fuzz run, neither of which `go test`
  # executes at their intended depth.
  check G1.9a "turn state machine: every (state, event) pair, illegal ones return an explicit error" \
    bash -c 'cd go && go test -race -count=1 -run "TurnState|Transition" ./internal/runtime/'
  check G1.9b "turn state machine: goroutine leak and cancel observability (step 1.5)" \
    bash -c 'cd go && go test -race -count=1 -run "Leak|Cancel|Concurrency" ./internal/runtime/'
  check G1.9c "turn state machine fuzz: FuzzTurn (15 s)" \
    bash -c 'cd go && go test -count=1 -run xxx -fuzz "^FuzzTurn$" -fuzztime 15s ./internal/runtime/'
  # G1.10 (step 1.14): the original spec wanted a full e2e through @nex/server.
  # What we gate on today is stronger in one axis and weaker in another:
  #   - G1.10a drives the Go agent directly against the *real* provider configured
  #     on this machine (deepseek-flash via ai-proxy.tylinux.cn, openai-responses
  #     dialect), and asserts the turn reaches terminal with model_request_completed.
  #     That is a stronger correctness signal than a mock.
  #   - G1.10b is the spec's shape: spawn @nex/server with GLM_BINARY_PATH pointed
  #     at the Go agent, drive a session through @nex/client, assert the answer
  #     and the SQLite rows. Not built yet; requires the server's spawn path.
  if [ -f /var/lib/nex-server/.nex/v2/provider_config.json ]; then
    mkdir -p /tmp/go-rewrite-bin && (cd go && go build -buildvcs=false -o /tmp/go-rewrite-bin/nex-agent ./cmd/nex-agent)
    check G1.10a "live provider: deepseek-flash via openai-responses, one full turn" \
      bash -c 'NEX_AGENT=/tmp/go-rewrite-bin/nex-agent node scripts/go-rewrite/live-deepseek.mjs >/dev/null'
  else
    skip G1.10a "live provider test" "no provider_config.json on this machine"
  fi
  # G1.10b: the spec's full shape. Spawns @nex/server with GLM_BINARY_PATH at the Go
  # binary, drives a session over @nex/rpc, asserts the mock model's reply and the
  # SQLite rows. Needs the server build (packages/server/dist/entry-http.js) and tsx.
  if [ -f packages/server/dist/entry-http.js ] && [ -x node_modules/.bin/tsx ]; then
    mkdir -p /tmp/go-rewrite-bin && (cd go && go build -buildvcs=false -o /tmp/go-rewrite-bin/nex-agent ./cmd/nex-agent)
    check G1.10b "end-to-end smoke through @nex/server with GLM_BINARY_PATH (step 1.14)" \
      bash -c 'node --import tsx scripts/go-rewrite/e2e-agent-smoke.mjs --agent /tmp/go-rewrite-bin/nex-agent 2>&1 | tail -1 | grep -q "^agent-binary=go$\|^\\[e2e-agent-smoke\\] agent-binary=go$"'
  else
    skip G1.10b "end-to-end smoke through @nex/server" "build @nex/server first (pnpm --filter @nex/server build)"
  fi
  # G1.11 (step 1.8): the exec adapter kills whole process trees. The scenarios
  # run here; the Windows half is a Job Object path that cannot run on this
  # machine, so the gate also cross-compiles the package (and its tests) for
  # windows to keep that half honest. The OS matrix itself lives in
  # .github/workflows/go.yaml.
  check G1.11 "process-tree kill tests: no orphan descendants, cancellation within 500ms, 10 MiB truncation (step 1.8)" \
    bash -c 'cd go && go test -race -count=1 -run "ProcessTree|KillTree|OutputCollector|Cancellation|Exec" ./internal/adapters/exec/ && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o /dev/null ./internal/adapters/exec/'
  # G1.13a automates the smallest slice of the "Web and Desktop manual checklist"
  # that a machine can prove: @nex/server boots with GLM_BINARY_PATH at the Go agent,
  # auth fail-closes and hands out a session cookie, the web bundle is served from
  # the same origin, and /ws carries the v4 handshake. When a chromium executable is
  # present it also drives the real UI (login -> composer -> streamed reply); when it
  # is absent the script says so instead of passing silently. G1.13 below stays TODO:
  # the rest of the checklist (permission prompts, file editing, Desktop app) is
  # still manual.
  if [ -f packages/server/dist/entry-http.js ] && [ -f packages/web/dist/index.html ] && [ -x node_modules/.bin/tsx ]; then
    mkdir -p /tmp/go-rewrite-bin && (cd go && go build -buildvcs=false -o /tmp/go-rewrite-bin/nex-agent ./cmd/nex-agent)
    check G1.13a "web smoke: server boots, auth, static, ws connect" \
      bash -c 'node --import tsx scripts/go-rewrite/e2e-web-smoke.mjs --agent /tmp/go-rewrite-bin/nex-agent 2>&1 | tee /dev/stderr | tail -1 | grep -q "^\\[e2e-web-smoke\\] web-smoke=ok$"'
  else
    skip G1.13a "web smoke" "build @nex/server and @nex/web first (pnpm --filter @nex/server build && pnpm --filter @nex/web build)"
  fi
  todo G1.13 "Web and Desktop manual checklist (step 1.14)"
}

gate2() {
  common_block 2
  # G2.1: phase 0 must pass.
  if GATE_SELFTEST=1 "$0" 0 >"$LOG_DIR/G2.1.log" 2>&1; then echo "PASS G2.1 gate 0 passes"; PASS=$((PASS+1))
  else echo "FAIL G2.1 gate 0 does not pass (phase 2 entry criterion)   (log: $LOG_DIR/G2.1.log)"; FAIL=$((FAIL+1)); fi
  # G2.2: value model and serialization agree with the recorded vectors and the
  # reference TypeScript codec. rpc-vectors.json carries the byte-exact hex for
  # every primitive shape; fuzz keeps the decoder honest.
  check G2.2 "rpc value model: vectors + fuzz + Windows cross-build (step 2.1)" \
    bash -c 'cd go && go test -race -count=1 ./internal/rpc/ && go test -count=1 -run xxx -fuzz "^FuzzUnmarshal$" -fuzztime 15s ./internal/rpc/ && go test -count=1 -run xxx -fuzz "^FuzzFrameReader$" -fuzztime 15s ./internal/rpc/ && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o /dev/null ./internal/rpc/'
  # G2.3 (step 2.3): the channel server. Covers the call/event/cancel/dispose
  # semantics, the pending-request queue with its 1 s timeout, the bounded
  # listener buffer with overflow-dispose, and a race/leak suite that closes a
  # connection with 100 pending calls and 10 listeners. Replays R02-R08 and R11
  # against the in-memory socket pair.
  check G2.3 "channel server: call/event/cancel/pending (step 2.3)" \
    bash -c 'cd go && go test -race -count=1 ./internal/rpc/ -run "Server|Channel|Listen|Cancel|Pending|Overflow|Replay|Leak|Backpressure|Dispose"'
  # G2.4 (step 2.4): the Go channel client. Covers the Initialize handshake and id
  # sequencing, the 202/203 error reconstruction (including every passthrough key), cancel
  # before start and mid-flight, event listen/dispose, client-side overflow, fail-closed on
  # Close, and a goroutine-leak check with 100 in-flight calls and 10 listeners. Replays
  # R01-R08 and R11 through the real Go client against a Go server, comparing the client's
  # own frames with the TypeScript recordings byte for byte.
  check G2.4 "rpc client: in-memory pair + Node integration (step 2.4)" \
    bash -c 'cd go && go test -race -count=1 ./internal/rpc/ -run "Client|Call|Listen|Cancel|Leak"'
  # G2.5 (step 2.5): the HTTP shell. Covers the static serving rules (MIME table, cache
  # headers, traversal guard, SPA fallback, HEAD) and GET /api/server-info (shape, optional
  # name, capabilities, authRequired including the NEX_SERVER_TOKEN / NEX_SERVER_AUTH_TOKEN
  # inconsistency). TestStaticAndServerInfoMatchRecordedTypeScript replays 47 exchanges
  # recorded from packages/server/src/http.ts by scripts/go-rewrite/record/record-http.mjs and
  # compares status, headers and body SHA-256, so a difference the UI could observe fails here.
  check G2.5 "http shell: static, server-info, headers match recorded TypeScript responses (step 2.5)" \
    bash -c 'cd go && go test -race -count=1 ./internal/server/ -run "Static|ServerInfo|Http|Traversal|SPA"' 
  # G2.6 (step 2.6): web authentication. Covers the decision table (auth disabled|enabled x
  # protected|page|ws x no token|good ?token=|bad ?token=|valid cookie|invalid cookie|locked out),
  # the three auth routes over a real HTTP server, cookie attributes including the Secure rule,
  # 10 000 failed logins from one address, session create/validate/revoke/expiry/rotation against a
  # real web-sessions.json, and the constant-time token comparison (AST check).
  check G2.6 "web authentication: session store, limiter, cookie (step 2.6)" bash -c 'cd go && go test -race -count=1 ./internal/server/webauth/'
  check G2.7 "host capability + WebSocket endpoints (step 2.7)" bash -c 'cd go && go test -race -count=1 ./internal/server/hostcap/ ./internal/server/ -run "HostCap|WebSocket|Ws|Capability|Connect"'
  check G2.8 "gateway: routing table + upstream forwarding (step 2.8)" bash -c 'cd go && go test -race -count=1 ./internal/gateway/'
  check G2.9 "node upstream supervision (step 2.9)" bash -c 'cd go && go test -race -count=1 ./internal/adapters/node/'
  # G2.10 (step 2.10): observability and tracing. The pins here are the level policy
  # (info/debug/warn/error per AGENTS.md), the connection/trace id minting, the
  # traceId passthrough into RPC error payloads, and the shared log-format prefix.
  check G2.10 "observability and tracing (step 2.10)" \
    bash -c 'cd go && go test -race -count=1 ./internal/server/ -run "Log|Trace|ConnectionID|TraceID|Format|Accepted|Closed|Forwarded|Upstream|InfoLevel|Identifiers|PrefixShape" && go test -race -count=1 ./internal/gateway/ -run "Log|Trace"'
  # G2.11 (step 2.11): build, packaging, Docker. Cross-compiles go/cmd/nex-server for all five
  # release targets. The flags that make the build reproducible (-trimpath, -buildvcs=false, the
  # pinned SOURCE_DATE_EPOCH, -ldflags "-s -w") and the static-linking assertion live in the script,
  # so a dependency that reintroduced cgo fails here rather than in a release that would not start on
  # debian-slim. The `ls` is the artifact assertion: five directories, each with a binary.
  # The acceptance list from docker-single-image.md is scripts/go-rewrite/docker-accept.sh (G2.8);
  # it is not run here because it starts a server and, when docker is present, pulls an image.
  check G2.11 "build / packaging / Docker (step 2.11)" bash -c 'bash scripts/go-rewrite/build-server.sh && ls go/dist/*/nex-server*'
  # The entry point behind those artifacts: --version must report the version the TypeScript build
  # injects (one package.json, two bundlers), the Node upstream must resolve from the release
  # layout, and the session store must survive across processes — the regression that made
  # acceptance item 3 ("a restart keeps sessions") fail.
  check G2.11.entry "nex-server entry: version, upstream resolution, session persistence" \
    bash -c 'cd go && go test -count=1 ./cmd/nex-server/ && go test -count=1 -run "Session|NoToken|DefaultSessionsPath" ./internal/server/'
  # G2.12 (step 2.12): the real UI against the Go front. The script drives Chromium through
  # login -> workspace -> new task -> "Say hello" -> the mock's canned answer, then repeats the
  # same flow against the pure-Node front and requires the (channel, method) sequence the UI
  # emitted inside the scenario to be identical. Screenshots, console logs and both sequences
  # land in go/testdata/e2e/phase2/. The script carries its own 90 s budget per front, so this
  # check cannot hang the gate.
  # SKIP (never a silent pass) when a prerequisite is missing: a browser, playwright-core, the
  # built web/server bundles, or the two binaries. Each reason names the command that fixes it.
  # A browser is needed. The candidate list mirrors the script's own (system chromium or chrome),
  # so a machine that ships google-chrome instead of chromium is not skipped for no reason.
  have_browser=0
  for candidate in "${CHROMIUM_PATH:-}" /usr/bin/chromium /usr/bin/chromium-browser \
                   /usr/bin/google-chrome /usr/bin/google-chrome-stable; do
    [ -n "$candidate" ] && [ -x "$candidate" ] && { have_browser=1; break; }
  done
  if [ "$have_browser" -eq 1 ] && [ -f go/dist/linux-amd64/nex-server ] \
     && [ -f packages/server/dist/entry-http.js ] && [ -f packages/web/dist/index.html ] \
     && [ -x node_modules/.bin/tsx ] && [ -d node_modules/playwright-core ]; then
    mkdir -p /tmp/go-rewrite-bin && (cd go && go build -buildvcs=false -o /tmp/go-rewrite-bin/nex-agent ./cmd/nex-agent)
    check G2.12 "end-to-end with real UI (step 2.12)" \
      bash -c 'node --import tsx scripts/go-rewrite/e2e-web-phase2.mjs 2>&1 | tail -1 | grep -q "phase2-e2e=ok"'
  else
    skip G2.12 "end-to-end with real UI" \
      "missing a prerequisite: build nex-server (@nex/server + @nex/web, build-server.sh), install node_modules (tsx + playwright-core), or install chromium"
  fi
}

gate3() {
  common_block 3
  # G3.1: phases 0-2 must pass.
  if GATE_SELFTEST=1 "$0" 2 >"$LOG_DIR/G3.1.log" 2>&1; then echo "PASS G3.1 gates 0-2 pass"; PASS=$((PASS+1))
  else echo "FAIL G3.1 gates 0-2 do not pass (phase 3 entry criterion)   (log: $LOG_DIR/G3.1.log)"; FAIL=$((FAIL+1)); fi
  # G3.2 (step 3.1): shared service infrastructure — registry, broadcast, paths,
  # atomic file helpers. These are the foundations every later tier builds on.
  check G3.shared "shared infrastructure: registry, broadcast, paths, atomic writes (step 3.1)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/ ./internal/services/paths/ ./internal/services/broadcast/ ./internal/adapters/fsx/ ./internal/adapters/logx/'
  # G3.4 (terminal, step 3.4) is deferred by user decision 2026-10-12: the
  # implementation will be redesigned before porting. Recorded here so the gate
  # reports it rather than silently omitting it.
  todo G3.terminal "terminal service (step 3.4) deferred — implementation will be redesigned first (user 2026-10-12)"
  check G3.system "system service: info, shells, intranet probe (step 3.2)" bash -c 'cd go && go test -race -count=1 ./internal/services/system/'
  check G3.file "file service: search, read, write, ignore (step 3.2)" bash -c 'cd go && go test -race -count=1 ./internal/services/file/'
  check G3.watcher "file watcher (step 3.2)" bash -c 'cd go && go test -race -count=1 ./internal/services/filewatcher/'
  check G3.media "media preview (step 3.2)" bash -c 'cd go && go test -race -count=1 ./internal/services/mediapreview/'
  # G3.3 (step 3.3): git services shell out to the git binary through the exec
  # adapter (never a Go git library). The suites cover the porcelain=v2 parser
  # (vectors from real git output), branch/diff/stage/commit flows against
  # fixture repos, and the checkpoint store atomic manifest writes.
  check G3.git "git service: summary/branches/diff/stage/commit (step 3.3)" bash -c 'cd go && go test -race -count=1 ./internal/services/git/'
  check G3.gitcheckpoint "git checkpoint service (step 3.3)" bash -c 'cd go && go test -race -count=1 ./internal/services/gitcheckpoint/'
  # G3.settings (step 3.5): the settings family — 8 packages' race suites.
  # setting.json state ownership, write queue semantics, schema validation
  # parity, the credential cipher (AES-256-GCM, Node-key parity vector), the
  # onboarding record, device identity lock, the data-dir copy fault suite,
  # settings-sync discovery/import across the four categories, and the three
  # HTTP-facing services against mocks (client-config, client-scenes,
  # coding-plan-subscription). device has no RPC channel (ensureDeviceMid is
  # a host entry point); its suite runs with the family, plus the registry
  # wiring test in the services root.
  check G3.settings "settings family: setting, settings-sync, client-config, client-scenes, onboarding, credential, device, coding-plan-subscription (step 3.5)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/setting/ ./internal/services/settingssync/ ./internal/services/clientconfig/ ./internal/services/clientscenes/ ./internal/services/onboarding/ ./internal/services/credential/ ./internal/services/device/ ./internal/services/codingplansubscription/ ./internal/services/'
  # G3.providers (step 3.6): providers and models. The shared
  # internal/providerconfig extraction (agent + server in one package), the
  # personal provider config repository + file codec, the builtin release
  # materialization (the embedded nex-builtin.json artifact), the registry
  # service + facades, models.dev + remote model list against httptest, the
  # legacy importers, and the security suite: the provisioning permission
  # matrix (web-remote rejected with the exact trusted-host error) and secret
  # redaction. The services-root suite runs the registry wiring test that
  # proves the connection-scoped channel flows only through a scope.
  check G3.providers "providers and models: providerconfig, modelprovider, providers, registry wiring (step 3.6)" \
    bash -c 'cd go && go test -race -count=1 ./internal/providerconfig/ ./internal/services/modelprovider/ ./internal/services/providers/ ./internal/services/ && go test -race -count=1 -run Security ./internal/services/providers/' 
  # G3 knowledge extensions (step 3.7): the shared single-implementation
  # libraries (internal/skills: frontmatter, scan policy, bounded walk, sync
  # path policy, tar.gz archive with the zip-slip/symlink/bomb security
  # suite and the bundled-skills frontmatter corpus; internal/plugins:
  # manifest reading, marketplace partitions + merged metadata, version
  # compare, atomic dir install, plugin archive codec) and the nine service
  # packages built on them: skills, skill-sync (candidate/export/import),
  # commands (.nex/.agents discovery + enable overrides), memory (Project
  # Memory catalog + guarded stable read), hooktrust (declaration/bundle
  # digests over JSON.stringify-compatible canonical JSON, the review
  # monotonicity property test, the byte-compatible trust-store codec,
  # fail-closed corrupt handling), plugin-management (local read side +
  # agent-required mutation refusals), the retired plugins channel stub,
  # plugin-sync (archives + marketplace source mirrors), and mcp-sync
  # (mcp.servers/mcpServers dual source, legacy enable migration,
  # filesystem path rewriting). The services-root suite carries the tier-4
  # registry wiring test.
  check G3.skills "knowledge extensions: skills, skill-sync (internal/skills + internal/services/skills + skillsync) (step 3.7)" \
    bash -c 'cd go && go test -race -count=1 ./internal/skills/ ./internal/services/skills/ ./internal/services/skillsync/'
  check G3.plugins "knowledge extensions: plugins, plugin-management, plugin-sync (internal/plugins + internal/services/plugins*) (step 3.7)" \
    bash -c 'cd go && go test -race -count=1 ./internal/plugins/ ./internal/services/plugins/ ./internal/services/pluginmgmt/ ./internal/services/pluginsync/'
  check G3.mcpsync "knowledge extensions: mcp-sync (dual-source read, legacy migration, path rewrite) (step 3.7)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/mcpsync/'
  check G3.hooks "knowledge extensions: hooktrust (digests, monotonicity property, trust-store compat, fail-closed) (step 3.7)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/hooktrust/'
  check G3.memory "knowledge extensions: memory (Project Memory catalog + stable read) and commands (step 3.7)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/memory/ ./internal/services/commands/'
  # G3.peripheral (step 3.8): the peripheral tier. Pets (manifest/image
  # parsing, grid tiling, path confinement), conversation-share (canonical-JSON
  # digests against Node-computed vectors, the cross-RPC redaction boundary,
  # the projection's identity re-minting, the publish/upload/preflight flow
  # against loopback httptest servers only, and the local artifact source's
  # symlink containment), prompt-attachment transfer, feedback (redaction +
  # the local ticket store + the diagnostic log archive), usage-stats, storage
  # (domain/app/adapters, scan accuracy), the shared feedbackprivacy redaction
  # vectors, and process telemetry (/proc parsing, the CPU differential with
  # its PID-reuse guard, the Host descendant attribution, and a live Linux
  # child-process tree).
  check G3.peripheral "peripheral services: pets, conversation-share, prompt-attachment, feedback, usage-stats, storage, process telemetry (step 3.8)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/pets/ ./internal/services/conversationshare/ ./internal/services/promptattachment/ ./internal/services/feedback/ ./internal/services/usagestats/ ./internal/services/storage/ ./internal/services/processtelemetry/ ./internal/feedbackprivacy/' 
  # G3.bots (step 3.9): the bots tier. IBotsService + the two POST
  # /api/bots/* routes. The suite covers the four-branch status contract
  # (400 unsupported / 400 no-HTTP-callback / 503 no-service / 200) and its
  # three byte-exact bodies, the x-nex-bot-secret and rawBody forwarding, the
  # commandParser table case by case, the reply-formatter golden outputs
  # generated from the TS by scripts/go-rewrite/vectors/bots-reply.mts, the
  # webhook-secret reply rule, the inbound delivery dedupe (including its TTL
  # and its per-chat key), and the Telegram cursor discipline: the offset must
  # NOT advance when the callback failed and MUST advance by update_id+1 when
  # it succeeded. Provider HTTP is exercised only against httptest fakes, plus
  # the Weixin AES-128-ECB CDN decrypt round trip.
  check G3.bots "bots: IBotsService + POST /api/bots/* (status contract, secrets, dedupe, telegram offset discipline, parser table, formatter goldens) (step 3.9)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/bots/ ./internal/server/' 
  # G3.10.1: the agent process manager — spawn via GLM_BINARY_PATH resolution,
  # per-workspace residency keyed by workspaceKey, exec-adapter tree kill, idle
  # reclaim, runtime epoch, stderr lifecycle logging.
  check G3.agentproc "agent process manager: spawn, residency, tree kill (step 3.10.1)" bash -c 'cd go && go test -race -count=1 ./internal/agentproc/'
  # G3.protoclient (step 3.10.2): the host side of the agent protocol. The
  # TopicWireFrameAssembler against the 25 vectors recorded from the TypeScript
  # assembler (go/testdata/vectors/wire.json assemblyCases, 14 distinct fault
  # reasons, ordinals, supersede and timeout rules), the request/response client
  # (correlation by id, out-of-order answers, per-call timeout, cancellation,
  # observation/business timeout split, close), the startup/storageState gate, the
  # interaction/* routing seam, capability negotiation, and a live round trip
  # against the REAL Go agent binary (built on demand into a temp dir) plus the
  # goroutine-leak delta check.
  check G3.protoclient "protocol client + wire assembler (step 3.10.2)" bash -c 'cd go && go test -race -count=1 ./internal/agentproc/proto/'
  # G3.connscope (step 3.10.3): the V4 connection-scoped service facade, one per
  # accepted WebSocket. The suite is the port of nexAgentConnectionScope.ts and its
  # comment-described past bugs, one test each: the hello → clientHello handshake
  # (hello required before initialize, a client that changes mid-connection, the
  # strict clientHello parse) across BOTH client modes x BOTH roles; the
  # workflowRunDeltas capability negotiated as a CONNECTION fact with every
  # caller-supplied delta bit stripped; frame routing by (connectionId, topic) with
  # concurrent connections proving no cross-delivery and no loss; the ACK-race
  # staging window (early frames released in arrival order, only for the acked
  # subscription id, whole-batch overflow failure, late-ACK reverse unsubscribe);
  # runtime-generation invalidation (available is NOT a boundary, unavailable IS,
  # a late ACK is refused WITHOUT unsubscribing the new runtime); the
  # unsubscribe route pinning and the terminal no-guess rule; flow control per
  # V4ConnectionFlowState (terminal forbidden, untrusted downstream refused,
  # unowned route refused, edge order preserved, close is terminal, saturated
  # replayed on subscribe, last subscription closes before unsubscribing, late
  # attachment begin aborted); the trusted-carrier strip of every forgeable field;
  # the command/query rules (clientId mismatch refused, clock probe exempt from the
  # workspace pin); interaction/* routing to the owning connection with
  # method-not-found when none is live; disposal tearing down every owned
  # subscription and leaving other connections untouched; and the goroutine-leak
  # delta check over every pump exit path.
  check G3.connscope "V4 connection scope: handshake, ownership, flow control, disposal, interaction routing (step 3.10.3)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/agentscope/'
  # G3.taskindex (step 3.10.4): the task index store, repository and syncer.
  # The suite is the port of tasksDatabase/{schema-v1,migrations,provider-selection-v2,
  # official-glm-selection-v3,startup}.ts + taskIndexRepo.ts + nexTaskIndexSyncer.ts:
  #   - migrations: every recorded Node version (v0001, v0002, fresh) migrates to
  #     a byte-identical sqlite_master and ledger; a second open executes nothing;
  #     a tampered checksum and a ledger from a newer build are both refused; a
  #     failed migration rolls the whole run back; concurrent opens serialize.
  #   - the two code-level migrations are checked against Node's row-level output:
  #     the frozen 0002 legacy-selection decoding and the 0003 GLM rename, plus the
  #     scheduled_run_count backfill and the duplicate-binding retention policy.
  #   - repo: remote identity isolation on a shared path, the primary key winning
  #     over a stale identity column, monotonic updatedAt, no terminal-status
  #     rewind, manual titles preserved, indexed body preserved by agent patches,
  #     strictly increasing unread markers, conditional clear, atomic tombstone,
  #     one-shot system grouping, one-shot grouped order, serialized concurrent
  #     writes, archive sweep selectivity.
  #   - syncer: the silent baseline (no broadcast, no terminal replay), terminal
  #     transition → status patch + reason + unread signal + terminal/ready order,
  #     title changes, gap → same-sub resync from the proven base, no base → forced
  #     snapshot, duplicate/foreign/wrong-topic frames dropped, assembly fault →
  #     recovery, drafts never mirrored, session.removed keeps the row, recovery
  #     snapshot re-diffs, stale online snapshot dropped, provider-not-ready /
  #     transient / runtime-unavailable paths, runtime restart resubscribe, no
  #     runtime → no subscribe, dispose stops everything.
  check G3.taskindex "task index store, repo and syncer (step 3.10.4)" \
    bash -c 'cd go && go test -race -count=1 ./internal/adapters/taskindex/ ./internal/services/taskindex/'
  # G3.taskindexx (step 3.10.4): the cross-engine check from step 1.3, applied to
  # tasks-index.sqlite. Node and Go must create identical schemas and ledgers,
  # open each other's database without migrating, and read each other's rows.
  if have node && [ -x node_modules/.bin/tsx ] && have go; then
    check G3.taskindexx "Node/Go create byte-identical task-index databases and open each other's" \
      node scripts/go-rewrite/crosscheck-taskindex.mjs
  else
    skip G3.taskindexx "task index cross-engine check" "node/tsx/go not available"
  fi
  # G3.taskadapter (step 3.10.5): the task service adapter — the local owner-run
  # bookkeeping, the cross-host task run lease, the owner-command route, the host
  # command queue and its drain, and the pending optimistic boundaries. The guard
  # inventory is docs/specs/go-rewrite/phase-3-guards.md: 61 rows, each naming the
  # test here that pins it, and each recording a decision rather than being dropped.
  #   - owner/lease local (A1–A7): the desktop leg passes unchecked, a dead run is
  #     NO_ACTIVE_TASK_OWNER, an older run is STALE_TASK_OWNER_COMMAND, an unloaded
  #     task is NEX_SESSION_TARGET_NOT_FOUND, cronAutomationId stays sticky.
  #   - owner/lease cross-host (B1–B12): two hosts one task — a held lease refuses
  #     another host AND the same host on a different run; a re-acquire keeps
  #     acquiredAt; a non-matching release is ignored; a window close releases only its
  #     own leases; the owner command reaches the lease owner only, a foreign result is
  #     dropped, an unanswered command times out, a dead owner host refuses
  #     immediately.
  #   - stale-run CAS (C1–C8): a CAS command without baseRevision throws, duplicate/noop
  #     are idempotent success, a stale ack converges on revisionAtDecision with a NEW
  #     commandId per attempt, exhaustion throws, and a stale base never executes.
  #   - queue and drain (D1–D23): an active run and a running command both block the
  #     drain, drains serialize per task and re-arm for what arrived mid-send, a failed
  #     send is marked failed, promote/cancel have their three outcomes each, enqueue is
  #     FIFO and idempotent per commandId, a turn terminal closes the queue but does NOT
  #     drain while the ready boundary does.
  #   - pending optimistic (E1–E8): a new run clears the previous live buffers, the
  #     snapshot reads the HOST queue, empty elicitations are replayable-only, events fan
  #     out to both emitters, the live projection is bounded, and a cancel emits only when
  #     it removed something.
  #   - scenarios: the two-host handover, the stale command that never executes, the
  #     50-command FIFO under concurrent drains, and the goroutine-leak delta.
  check G3.taskadapter "task service adapter: owner/lease, cross-host routing, stale-run, queue, pending state (step 3.10.5)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/taskadapter/'
  # G3.windowcontroller (step 3.10.6): IWindowControllerService + the server
  # variant's per-connection attachment. The guard inventory is
  # docs/specs/go-rewrite/phase-3-guards.md § "Step 3.10.6".
  #   - topics (W1-W4): the two topics carry INDEPENDENT cursors, subscribe
  #     delivers the snapshot before the ack, an empty delta batch publishes
  #     nothing (no phantom gap), and a remote reconnect that changes
  #     remoteSessionId publishes old removals + new upserts in ONE frame.
  #   - projection (W5-W11): liveStatus comes only from the live overlay (a
  #     persisted running status is not a live fact), a disconnect FREEZES the
  #     rows instead of dropping them, a failed read keeps the last trusted
  #     snapshot, the membership buckets partition, local and remote rows with
  #     the same id stay separate, an unresolvable scope fails closed, and a
  #     remote scope without an identity is refused.
  #   - refresh and writes (W12-W19): the read is single-flight, a write routes
  #     to the source and converges the projection, delete returns no meta,
  #     open/resume are not writes, an offline source refuses writes, a batch
  #     naming a replaced source is refused, search is per source and one
  #     source's failure does not empty the answer, and sort+limit are total.
  #   - per-connection boundary (C1-C10): two connections get separate
  #     subscriptions over shared rows, disposing one removes only its own ids,
  #     a disposed connection receives nothing and refuses every entry point, a
  #     foreign unsubscribe is a no-op, runtime dispose releases everything,
  #     removing a source stops its observer, 16 concurrent connections leave no
  #     attachment or subscription behind, and 50 connect/dispose rounds leave no
  #     goroutine growth.
  #   The suite runs -race: the parallel-refresh cursor advance is a real race
  #   the TypeScript cannot have, and it was found here rather than by review.
  check G3.windowcontroller "window controller: two topics, frozen offline projection, per-connection attachment, single-flight refresh (step 3.10.6)" \
    bash -c 'cd go && go test -race -count=1 ./internal/services/windowcontroller/'
  todo G3.agentbridge "agent bridge (step 3.10)"
}

gate_unbuilt() { common_block "$1"; todo "G$1.*" "phase $1 checks are defined in its doc but not implemented yet"; }

case "$PHASE" in
  0) gate0 ;;
  1) gate1 ;;
  2) gate2 ;;
  3) gate3 ;;
  4|5) gate_unbuilt "$PHASE" ;;
  *) echo "unknown phase $PHASE" >&2; exit 2 ;;
esac
echo "----"
echo "phase $PHASE: $PASS passed, $FAIL failed, $TODO not built/open, $SKIP skipped"
[ "$FAIL" -eq 0 ] && [ "$TODO" -eq 0 ] && [ "$SKIP" -eq 0 ]
