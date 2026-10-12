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
  todo G2.11 "build / packaging / Docker (step 2.11)"
  todo G2.12 "end-to-end with real UI (step 2.12)"
}

gate_unbuilt() { common_block "$1"; todo "G$1.*" "phase $1 checks are defined in its doc but not implemented yet"; }

case "$PHASE" in
  0) gate0 ;;
  1) gate1 ;;
  2) gate2 ;;
  3|4|5) gate_unbuilt "$PHASE" ;;
  *) echo "unknown phase $PHASE" >&2; exit 2 ;;
esac
echo "----"
echo "phase $PHASE: $PASS passed, $FAIL failed, $TODO not built/open, $SKIP skipped"
[ "$FAIL" -eq 0 ] && [ "$TODO" -eq 0 ] && [ "$SKIP" -eq 0 ]
