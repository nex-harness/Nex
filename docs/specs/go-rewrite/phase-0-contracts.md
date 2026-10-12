# Phase 0 — Contracts, golden traces, harness

Part of [the Go rewrite plan](README.md). Product code: none. Output: the things every later phase is tested against.

## Goal

When this phase is finished:

- The Go module skeleton exists, builds on Linux/macOS/Windows, and runs in CI.
- Every protocol schema that crosses the agent↔host boundary is exported to JSON Schema and has
  generated Go types, with a CI drift check.
- A corpus of **golden traces** recorded from the real TypeScript stack exists in the repo: agent stdio
  transcripts, RPC frame captures, and SQLite fixtures at every migration version.
- A **replay harness** can feed a recorded trace to any implementation (TS or Go) and diff the output
  after normalization.
- The open decisions D1–D9 in the README are resolved and written down.
- `scripts/go-rewrite/gate.sh` exists and runs the gates for all phases (phases 1–5 start red).

## Non-goals

No Go business logic. No change to TypeScript runtime behavior (only additive export scripts and a
deterministic test mode if recording demands it — see step 0.7).

## Entry criteria

- Maintainer agreement to start (this is a multi-month commitment).
- A machine with network access to install pnpm/Node dependencies.

## Steps

### 0.1 Establish and record the TypeScript baseline

- **Source:** `AGENTS.md`, `mise.toml`, root `package.json` scripts.
- **Target:** `docs/specs/go-rewrite/baseline.md` (new, checked in).
- **Do:**
  1. Install the pinned toolchain: Node 24.14.0 and pnpm 10.33.2 (via `mise install`, or corepack).
  2. `pnpm install`.
  3. Run, in order: `node scripts/check-workspace-freshness.mjs`, `pnpm typecheck`, `pnpm lint`,
     `pnpm fmt:check`, `pnpm architecture:check --changed`.
  4. Run every existing test target and record pass/fail counts:
     `pnpm --filter @nex/services test`, `pnpm --filter @nex/core test`, and any other package that defines a
     `test` script (find them with `grep -l '"test"' packages/*/package.json apps/nex-cli/packages/*/package.json`).
  5. Record: command, exit code, duration, and the first failing lines for anything that fails.
- **Tests:** none (this step produces the baseline).
- **Done when:** `baseline.md` exists listing every command with its real exit code. Failures that exist
  before any Go work are labelled "pre-existing"; later gates compare against this file.
- **Why first:** `AGENTS.md` forbids reporting an existing failure as a pass. The baseline is the evidence.

### 0.2 Resolve decisions D1–D9

- **Target:** `docs/specs/go-rewrite/decisions.md` (new).
- **Do:** For each decision in the README table, record: chosen option, who approved, date, and the
  consequence for later phases. Specifically settle:
  - D1: the real Go module path.
  - D4: sidecar vs drop for `dynamic-workflow`, `codemode`/`repl`, `node-repl-host`, Browser Use, CUA. If "drop",
    list the user-visible features that disappear and get product sign-off.
  - D6: what happens to `apps/nex-cli/packages/{cli,tui}`.
  - D8: confirm with the module maintainer that Node and Go may share SQLite files during the transition, and
    that no migration beyond `0022_*` lands without Go parity (see phase-1 step 1.6).
- **Tests:** none.
- **Done when:** every row has a chosen option and an approver. Gate check `G0.2` greps that no row contains the
  string `TBD`.

### 0.3 Go module skeleton, lint, CI

- **Target (new):**
  ```
  go/go.mod                      module <D1 path>, go 1.24
  go/cmd/nex-agent/main.go       prints version, exits 0 (placeholder)
  go/cmd/nex-server/main.go      same
  go/cmd/nex-trace/main.go       replay/diff tool (filled in by 0.10)
  go/internal/trace/             traceId helpers (stub)
  go/.golangci.yml               linters: govet, staticcheck, errcheck, gofmt, depguard, lll off, funlen off
  mise.toml                      add `go = "1.24"` to [tools]
  .github/workflows/go.yaml      matrix: ubuntu-latest, macos-latest, windows-latest
  ```
- **Do:**
  - `depguard` rules: packages under `internal/core/...` and `internal/session/...` must not import `os/exec`,
    `net/http`, `database/sql`, or `os` (except `os.Getenv`-free constants). Only `internal/adapters/...` and
    `cmd/...` may. This encodes the "external I/O only in adapters" rule.
  - A tiny script `scripts/go-rewrite/check-file-lines.sh` fails if any `go/**/*.go` (excluding `*_test.go`
    and `*.gen.go`) exceeds 400 lines.
  - CI steps: `gofmt -l`, `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, cross-build for
    `linux/amd64, linux/arm64, darwin/arm64, darwin/amd64, windows/amd64`.
- **Tests:** `go/cmd/nex-agent/main_test.go` asserts the placeholder prints a version string (proves the test
  pipeline runs on all three OSes).
- **Done when:** the CI workflow is green on all three runners; `go build ./...` succeeds for all five targets.
- **Verify first:** whether `golangci-lint` is acceptable as a new CI dependency; if not, use `go vet` +
  `staticcheck` only.

### 0.4 Export protocol schemas to JSON Schema

- **Source:** `packages/shared/src/nex-protocol/index.ts` (legacy, ~3.7k lines), `packages/shared/src/nex-protocol-v4/*.ts`
  (`transport.ts`, `wire.ts`, `command.ts`, `delta.ts`, `snapshot.ts`, `rows.ts`, …), `packages/shared/src/nex-protocol-legacy-types.ts`,
  `packages/shared/src/handshake.ts`.
- **Target (new):** `scripts/go-rewrite/export-schemas.mjs` → writes `go/contracts/schema/*.json` and
  `go/contracts/schema/manifest.json` (list of exported names → source file).
- **Do:**
  1. Import each module's exported `*Schema` constants (zod objects). Skip non-zod exports.
  2. Convert with `z.toJSONSchema(schema, { unrepresentable: "any", io: "input" })`; record names that cannot be
     converted in `manifest.json` under `unsupported` with the reason.
  3. Also export the **method tables**: `nexProtocolMethods` and `nexProtocolNotifications`
     (`packages/shared/src/nex-protocol/index.ts`) and `V4_NOTIFICATIONS`, as JSON, so Go can generate constants.
  4. Output must be deterministic (sorted keys, stable ordering) so drift diffs are meaningful.
- **Tests:**
  - `scripts/go-rewrite/export-schemas.test.mjs` (node:test): run the exporter twice, assert byte-identical output;
    assert `manifest.json` contains `session/create` params/result schemas and the `hello`/`clientHello` schemas;
    assert `unsupported` is below an agreed ceiling (set after the first run; record it in the test).
- **Done when:** running the exporter on a clean tree yields a committed, non-empty `go/contracts/schema/` and
  the determinism test passes.
- **Verify first:**
  - `packages/shared` pins `zod 4.6.5` (confirmed from its `package.json`), where `z.toJSONSchema` exists. One of
    `apps/nex-cli/package.json` / `packages/contracts/package.json` declares `zod ^3.24.0` together with
    `zod-to-json-schema` — **which one was not checked**. Agent tool schemas in `contracts` may therefore need the
    zod-3 conversion path (`zod-to-json-schema`). The exporter must use the right converter per package.
  - Several schemas use `.superRefine`, `.strict()`, `.passthrough()` and discriminated unions. JSON Schema cannot
    express refinements (e.g. `helloMessageSchema` requires `clientMode` and `deliveryProfile` to match). List every
    `superRefine` in the exported set in `manifest.json` as `refinements`, and port each by hand in step 0.5.

### 0.5 Generate Go types and hand-written validators

- **Target (new):** `go/contracts/` (package `contracts`), generated files named `*.gen.go`, hand-written `*_validate.go`.
- **Do:**
  1. Choose the generator in this step (candidates: `github.com/atombender/go-jsonschema`, `quicktype`). Criteria:
     discriminated unions become tagged Go types with custom `UnmarshalJSON`; `.strict()` becomes
     `DisallowUnknownFields`-equivalent; optional vs null vs absent is preserved (use pointer or a small `Optional[T]`).
     Pick by generating the `transport.ts` and `command.ts` schemas and compiling them.
  2. Add `scripts/go-rewrite/gen-contracts.sh`: runs the exporter, runs the generator, runs `gofmt`.
  3. Hand-write validators for each recorded refinement (the hello/deliveryProfile rule and the others found in 0.4).
  4. Add Go constants for protocol numbers: `V4_WIRE_PROTOCOL_VERSION = 3`, `PROTOCOL_V4_LIMITS.*` (read from
     `packages/shared/src/nex-protocol-v4/core.ts`), RPC request/response type codes 100–103 / 200–204.
- **Tests:**
  - `go/contracts/contracts_test.go`: for every JSON instance in `go/testdata/schema-instances/` (valid and invalid,
    produced by step 0.6 and by TypeScript `schema.safeParse` run over the same files), assert the Go validator agrees
    with the zod verdict. Disagreement fails the test.
  - Drift check (CI): `scripts/go-rewrite/gen-contracts.sh && git diff --exit-code go/contracts` — fails if the
    committed generated output is stale.
- **Done when:** `go test ./contracts/...` passes and the drift check is clean.
- **Risk:** a few hundred exported schemas, some with recursion (workflow rows). Budget extra time; permit
  `json.RawMessage` for deeply recursive sub-trees at first, with a tracking list in `manifest.json`.

### 0.6 Schema agreement corpus

- **Target (new):** `scripts/go-rewrite/make-instances.mjs`, output `go/testdata/schema-instances/{valid,invalid}/*.json`
  plus `verdicts.json` (zod's verdict for every file).
- **Do:** For each exported schema, generate instances: (a) every instance observed in the recorded traces from
  steps 0.7–0.8, (b) hand-written boundary cases (missing required field, unknown field on a strict object, wrong enum,
  oversize string, negative where nonnegative required), (c) mutations of valid instances (drop one key at a time).
  Run zod `safeParse` over all of them and write the verdicts.
- **Tests:** the script's own test asserts every schema in the manifest has at least one valid and one invalid instance.
- **Done when:** corpus committed; step 0.5's agreement test consumes it.

### 0.7 Deterministic recording mode for the TypeScript stack

- **Source:** `packages/services/src/nex-agent/nexStdioTransport.ts`, `nexProtocolClient.ts`,
  `nexStdioTapDevConfig.ts` (there is already a stdio **tap** dev facility — read it first),
  `apps/nex-cli/packages/bootstrap/test/e2e/mock-model.mjs` (scripted OpenAI-compatible server, 57 lines).
- **Target (new):** `scripts/go-rewrite/record/` — `record-agent-session.mjs`, `scenarios/*.json`, `mock-model/` (extended).
- **Do:**
  1. Read `nexStdioTapDevConfig.ts` and determine whether it already writes both directions of the stdio stream to a file.
     If yes, reuse it; if no, add the smallest additive tap (spec first, per `AGENTS.md`). Do **not** add a new environment
     variable without a spec (`apps/nex-cli/AGENTS.md`); prefer a CLI flag or config key.
  2. Extend `mock-model.mjs` into a scenario-driven server: a scenario is a JSON list of canned model turns
     (text deltas, tool calls with arguments, reasoning chunks, finish reasons, simulated errors such as HTTP 429/500 and
     mid-stream disconnect). It must speak OpenAI chat-completions streaming (what the existing mock does) **and** the
     Anthropic Messages SSE format (needed for phase 1 step 1.8). Log each request body to JSONL for later assertions.
  3. Recording harness: start the real Node agent as `app-server --stdio` with a throwaway `HOME`/`NEX_DATA_BASE_DIR`,
     a `provider_config.json` pointing at the mock (same layout as `bootstrap/test/e2e/README.md`), then replay the
     scenario's _client_ messages and capture every frame in both directions with monotonic timestamps.
  4. Normalization map (see 0.10): UUIDs, timestamps, temp paths, PIDs, version strings, durations.
- **Status (built):** `scripts/go-rewrite/record/` has the recorder (`record-agent-session.mjs`), a scenario-driven mock model (`mock-model.mjs`), the normalizer (`normalize.mjs`), a replay tool (`replay-agent.mjs`) and 14 tests. Recorded and committed under `go/testdata/traces/agent/`: **S01** handshake, **S02** chat, **S03** Read tool, **S04** Write+Edit with permission, **S05** permission denied, **S13** HTTP 429 retry. Each was recorded 20 times and normalizes to identical bytes.
- **Driving the agent:** the recorder plays the host. It sends `v4/command createSession` with `firstInput` (the production path; the legacy `session/create` leaves no session row for later commands), subscribes with `v4/conversation/subscribe`, and answers the agent's own requests (`session/requestRuntimePreferences`, `interaction/requestPermission` by `optionId`, everything else `-32601`).
- **Normalization rules, all derived from observed run-to-run diffs:** ids and uuids become numbered placeholders; epoch-ms values become `<time>`; timing counters (`*Ms` plus the tool `performance` block) become 0 while behavioural counters beside them are kept; `session_title` telemetry runs concurrently with the turn, so it is moved to its own ordered lane (its position relative to the main stream flipped once in 5 runs); conversation delta frames are compared as one flattened delta stream because the agent batches them on a 30 ms timer, but **frame contiguity (`fromSeq` equals the previous `toSeq`) is enforced and a gap throws**.
- **Replay is script-driven, not byte replay:** inbound frames contain agent-assigned session ids, so they cannot be re-sent verbatim. `replay-agent.mjs` runs the same host script against any agent executable and diffs the normalized output (decision D11).
- **Still to add:** S06-S12 and S14-S18 from the table below. S09 (two concurrent sessions), S10 (restart and resume), S11 (reconnect with `base`) need new script operations (`restartAgent`, `resubscribe`).
- **Scenarios (original target list):**
  | ID | Scenario | What it exercises |
  | -- | -------- | ----------------- |
  | S01 | handshake + `runtime/capabilities` + `startup/storageState` | connection setup |
  | S02 | create session, send text, streamed text answer, close | basic turn |
  | S03 | tool call `Read` then answer | tool loop, read-state tracking |
  | S04 | `Write` + `Edit` new/existing file, with permission prompt allowed | permission request/response |
  | S05 | permission denied | denial path |
  | S06 | `Bash` command, short | exec adapter |
  | S07 | `Bash` long-running, cancelled via `stop` | cancellation, process-tree kill |
  | S08 | `Glob` + `Grep` | search tools |
  | S09 | two sessions concurrently | isolation |
  | S10 | resume session after agent restart | persistence + cold resume |
  | S11 | v4 subscribe → snapshot, then deltas; reconnect with `base{logEpoch,seq}` → resume | replay semantics |
  | S12 | large assistant message forcing wire fragmentation + CRC32 | fragment assembly |
  | S13 | model 429 then success (retry); model mid-stream disconnect | retry classification |
  | S14 | queue a second input while busy | `CommandInbox` serial admission |
  | S15 | `compact` | compaction |
  | S16 | `fork` at a message | fork |
  | S17 | `todo` tool + `AskUserQuestion` | interaction requests |
  | S18 | workspace identity: local path vs remote identity string | identity keying |
- **Tests:** `record-agent-session.test.mjs`: record S02 twice, normalize both, assert byte-identical (determinism).
  This is the test that proves the recordings are usable as an oracle; if it fails, fix nondeterminism first
  (fixed clock/ID seeds only via existing config, never ad hoc env vars).
- **Done when:** S01–S18 recorded under `go/testdata/traces/agent/<ID>/{in.ndjson,out.ndjson,meta.json}` and the
  determinism test passes for all of them.
- **Verify first:** whether the Node agent can be driven headless from a script without Electron (the dev e2e README
  starts a full server and uses the web UI to send prompts — a script-driven client for `app-server --stdio` may need
  to be written from the v4 command schemas; allow time for it).

### 0.8 Record RPC (UI ↔ server) frames

- **Source:** `packages/rpc/src/{serialization,channelServer,channelClient,protocol,persistent-protocol}.ts`,
  `packages/client/src/websocket.ts`, `packages/server/src/http.ts`.
- **Target (new):** `scripts/go-rewrite/record/record-rpc.mjs` → `go/testdata/traces/rpc/<ID>/frames.jsonl`
  (each line: direction, timestamp, `hex` of the raw WebSocket binary frame, and the decoded `{header, body}`).
- **Do:**
  1. Start `@nex/server` HTTP entry (`entry-http.ts`) against a throwaway data dir and the mock model.
  2. A Node client built on `@nex/rpc` `ChannelClient` + `@nex/client` connects to `/ws/host`, performs the host
     capability handshake (`POST /api/rpc-host-capability`, `NEX_RPC_HOST_CAPABILITY_HEADER`) and records frames.
  3. Scenarios: R01 connect + initialize; R02 call a trivial service method; R03 listen to an event and dispose it;
     R04 cancel an in-flight promise; R05 request for an unregistered channel (the server buffers it ~1 s —
     `ChannelServer` `timeoutDelay = 1000`); R06 binary payload (`Uint8Array`) as argument and as result;
     R07 nested `Uint8Array` inside an object (the `__nex_rpc_nested_uint8array_v1` marker path); R08 error with
     `name/message/stack/code`; R09 a single message larger than 1 MiB and a burst of 1 000 small messages (the `connection-flow-v1` saturated/drained control messages exist only on
     the Electron MessagePort transport in `packages/rpc/src/protocol.ts`; `http.ts` and `packages/client/src/websocket.ts` use plain `SocketProtocol`, so they are
     **not** part of the WebSocket codec); R10 auth: `401` without token, login, then connect;
     R11 `clientMode` `desktop-continuous` vs `web-remote-replayable`.
  4. Also dump a **serialization vector file**: `go/testdata/rpc-vectors.json` = list of `{value, hex}` pairs produced by
     `serialize()` for hand-picked values: `undefined`, `""`, `"é中"`, ints `0, 1, 127, 128, 16383, 16384, 2^31-1`,
     negative ints (record what the JS does — see below), floats (`1.5`), `null`, `true`, nested arrays, objects with
     unicode, `Uint8Array(0)`, `Uint8Array(300)`, objects containing `Uint8Array`.
- **Tests:** `record-rpc.test.mjs`: decode every recorded frame with `deserialize()` and assert re-serialization is
  byte-identical (proves the capture is faithful).
- **Done when:** traces R01–R11 and `rpc-vectors.json` committed.
- **Verify first (known edge cases to pin down by experiment, not by reading):**
  - `writeInt32VQL` loops `for (v = value; v !== 0; v = v >>> 7)`. For a **negative** int, `data | 0 === data` is true so
    the `Int` tag is used, but the loop terminates differently (`>>>` makes it unsigned) and the reader uses `<<`/`|=`
    on 32-bit signed values. Record exactly what bytes JS emits for `-1`, `-2147483648` and what `deserialize` returns.
    The Go codec must reproduce this or the plan must prove negatives never occur on the wire.
  - Numbers that are not 32-bit integers (e.g. `2^31`, `1e12`, `NaN`) go through the JSON `Object` branch. Record them.
  - `null`: `typeof null === "object"` and not an array, so it takes the JSON branch. Record the bytes.

### 0.9 Storage fixtures at every migration version

- **Source:** `apps/nex-cli/packages/adapters/src/storage/session-store/migrations.ts` (migration ids `0001`…`0022`, verified
  list), `.../migrations/002{0,1,2}-*.ts`, `.../repositories/*.ts`; and the tasks database under
  `packages/services/src/session/tasksDatabase/{schema-v1,migrations,startup,prepared}.ts`.
- **Target (new):** `scripts/go-rewrite/record/make-sqlite-fixtures.mjs` → `go/testdata/sqlite/session-store/vNNNN.sqlite`
  (+ `schema-vNNNN.sql` dump) and `go/testdata/sqlite/tasks-db/*.sqlite`.
- **Do:**
  1. For the session store: create a database at each migration step (apply migrations 1..N, N = 1…22), then insert a
     fixed, small, hand-authored dataset (2 sessions, messages with parts, todos, an input ledger row, a usage row,
     a dwf journal row once N≥19). Use Node's `node:sqlite` (same driver family as production).
  2. Dump `sqlite_master` for each version into `schema-vNNNN.sql` so schema drift is reviewable as text.
  3. Do the same for the tasks database; read `startup.ts` first to learn how it opens and migrates.
  4. Produce `legacy/` fixtures for any data the code migrates on startup (e.g. `legacyTaskSessionFile.ts` JSON snapshots).
  5. Scrubber: `scripts/go-rewrite/scrub-check.mjs` scans all fixtures and traces for secret-shaped strings
     (`sk-…`, `Bearer …`, JWT-like, email, home-directory paths) and fails on any hit.
- **Tests:** `make-sqlite-fixtures.test.mjs`: opens each fixture with `node:sqlite`, runs `PRAGMA integrity_check`,
  asserts the row counts of the authored dataset. Scrubber test: seeded known-bad string is detected.
- **Done when:** 22 session-store fixtures + tasks-db fixtures + schema dumps committed; scrubber passes.

### 0.10 Replay harness and normalizer (`nex-trace`)

- **Target (new):** `go/cmd/nex-trace`, `go/internal/tracecheck/` (library), `go/testdata/normalize.yaml`.
- **Do:**
  - Subcommands:
    - `nex-trace replay-agent --cmd <agent-binary> [--args app-server --stdio] --trace go/testdata/traces/agent/S02`
      spawns the agent in a throwaway `HOME`, feeds `in.ndjson` frames (waiting for the matching responses recorded in
      `out.ndjson`, not fixed sleeps), captures output, normalizes, and diffs against the recorded `out.ndjson`.
    - `nex-trace replay-rpc --url ws://… --trace go/testdata/traces/rpc/R02`.
    - `nex-trace diff <a> <b>` structural JSON diff with ignore rules.
  - Normalizer rules in `normalize.yaml`: replace UUID-like ids by stable placeholders in first-seen order;
    replace ISO and epoch-ms timestamps; replace temp dirs; ignore `meta.pid`, durations, `agentVersion`;
    **do not** normalize event _order_ or `seq`/`logEpoch` relationships — those are semantic.
  - Two comparison modes: `strict` (byte-equal after normalization) and `semantic` (set-equal for messages the protocol
    does not order, ordered for v4 deltas). Each scenario declares its mode in `meta.json`.
  - The harness must point at the **Node** agent too (`--cmd node --args <nex.cjs> app-server --stdio`). The Node agent
    replaying its own recordings must pass 18/18 — that proves the harness, not the Go code.
- **Tests:** `go test ./internal/tracecheck/...`: unit tests for the normalizer and differ; an end-to-end test that runs
  `replay-agent` against a tiny **fake agent** (a Go test helper that echoes the recorded output) and asserts PASS, then
  mutates one field and asserts FAIL.
- **Done when:** `nex-trace replay-agent` with the Node agent passes S01–S18 on Linux; CI runs this job (it needs the
  built Node CLI bundle `apps/nex-cli/packages/cli/dist/nex.cjs`, built by `pnpm build:nex`).

### 0.11 Behavior inventory

- **Target (new):** `docs/specs/go-rewrite/inventory.md`, generated by `scripts/go-rewrite/make-inventory.mjs`.
- **Do:** Produce tables that later phases tick off: (a) every legacy method/notification in `nexProtocolMethods` /
  `nexProtocolNotifications` (the source lists ~84 names — regenerate rather than trusting this count);
  (b) every v4 command kind and topic, from `command.ts` and `transport.ts` (read the discriminated union; the
  `z.literal` grep in this plan's research was incomplete); (c) every service channel name and its methods, from the 36
  files that call `createServiceDescriptor` plus their interface definitions; (d) every tool (names from
  `apps/nex-cli/packages/contracts/src/tools/*.ts`, 48 files, and `core/src/tool/handlers`); (e) every SQLite table
  and migration id; (f) every HTTP route in `packages/server/src/http.ts` and `webAuth.ts`; (g) every config file the
  code reads or writes (grep `getAppConfigDir`, `~/.nex`, `.agents`). Each row has columns:
  `item | source file | owner phase | status (todo/ported/dropped) | trace/test id`.
- **Tests:** `make-inventory.test.mjs`: re-running the generator on a clean tree produces identical output (so the
  inventory cannot silently rot); a count check compares the number of tools against `contracts/src/tools`.
- **Done when:** `inventory.md` committed, every row has an owner phase, no row has an empty owner.

### 0.13 Measure what the oracle covers (added after the baseline showed thin existing tests)

- **Why:** `test-sufficiency.md` shows the existing unit tests do not exercise most backend logic, so the recorded traces (0.7, 0.8) are the oracle. An oracle is only as good as the code it exercises; this step measures that instead of assuming it.
- **Target (new):** `scripts/go-rewrite/coverage/run-with-coverage.mjs`, report `docs/specs/go-rewrite/oracle-coverage.md`.
- **Do:** Re-run all scenarios from 0.7/0.8 with the Node agent and server started under `NODE_V8_COVERAGE=<dir>`, merge the results (e.g. `c8 report` or `v8-to-istanbul`), and report per source file under `apps/nex-cli/packages/{core,adapters,bootstrap}/src` and `packages/{services,server,rpc,shared}/src`: line and function coverage achieved by the traces alone. Rank files by size × (1 − coverage).
- **Rule:** every file in the "must match" set (`serialization.ts`, `channelServer.ts`, `wire-*.ts`, `turn-machine.ts`, `edit-matchers.ts`, `bash-readonly-policy*.ts`, `failure-classifier.ts`, `streaming-tool-call-assembler.ts`, `command-inbox.ts`, `migration-runner.ts`) must be at least 80% line-covered by traces plus vectors, or carry a written exception in `oracle-coverage.md`. Uncovered files get new scenarios or vector scripts **before** the phase that ports them starts.
- **Tests:** `run-with-coverage.test.mjs`: coverage of a known tiny script is reported correctly; the report generator is deterministic.
- **Done when:** `oracle-coverage.md` is committed and the "must match" rule holds or has approved exceptions.

### 0.12 Gate runner

- **Target (new):** `scripts/go-rewrite/gate.sh` (bash; Windows runs it under Git Bash), `scripts/go-rewrite/gates/g{0..5}.sh`.
- **Do:** Implement `G0.*` below fully. For phases 1–5, create the files with every check from their phase documents
  wired in, each failing with `NOT IMPLEMENTED` until its phase supplies the target. This makes "phase done" a
  mechanical question.
- **Tests:** `scripts/go-rewrite/gate.test.sh`: `gate.sh 0` exits 0 on a good tree; temporarily corrupt a fixture
  (script does it in a temp copy) and assert `gate.sh 0` exits non-zero and prints `FAIL G0.…`.
- **Done when:** `gate.sh 0` is green and `gate.sh 1` prints `FAIL` for each unimplemented check.

## Exit gate — `scripts/go-rewrite/gate.sh 0`

| Check | Command                                                                                                 | Pass condition                                                           |
| ----- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| G0.0  | the common `GN.0` block from the README                                                                 | all exit 0, or failures match `baseline.md` "pre-existing" list exactly  |
| G0.1  | `test -s docs/specs/go-rewrite/baseline.md`                                                             | file exists and lists every baseline command with an exit code           |
| G0.2  | `! grep -n 'TBD' docs/specs/go-rewrite/decisions.md`                                                    | no undecided rows                                                        |
| G0.3  | `cd go && go build ./... && go test ./...` on ubuntu, macos, windows CI jobs                            | green on all three                                                       |
| G0.4  | `node scripts/go-rewrite/export-schemas.mjs && git diff --exit-code go/contracts/schema`                | no diff                                                                  |
| G0.5  | `scripts/go-rewrite/gen-contracts.sh && git diff --exit-code go/contracts`                              | no diff                                                                  |
| G0.6  | `cd go && go test ./contracts/...`                                                                      | Go validators agree with zod verdicts on the whole corpus                |
| G0.7  | `node --test scripts/go-rewrite/record/*.test.mjs`                                                      | determinism tests pass                                                   |
| G0.8  | `cd go && go run ./cmd/nex-trace replay-agent --cmd node --args "<nex.cjs> app-server --stdio" --all`   | 18/18 scenarios PASS against the Node agent                              |
| G0.9  | `cd go && go run ./cmd/nex-trace replay-rpc --url <node-server> --all`                                  | 11/11 RPC scenarios PASS against the Node server                         |
| G0.10 | `node scripts/go-rewrite/scrub-check.mjs go/testdata`                                                   | exit 0                                                                   |
| G0.11 | `node scripts/go-rewrite/make-inventory.mjs && git diff --exit-code docs/specs/go-rewrite/inventory.md` | no diff                                                                  |
| G0.12 | `bash scripts/go-rewrite/gate.test.sh`                                                                  | exit 0                                                                   |
| G0.13 | `node scripts/go-rewrite/coverage/check-oracle-coverage.mjs`                                            | every "must match" file ≥ 80% line-covered, or has an approved exception |

## Rollback

Everything is additive (new directories, new scripts). Rolling back means deleting `go/`,
`scripts/go-rewrite/`, `docs/specs/go-rewrite/` and the `mise.toml` line. Any TypeScript change made for step 0.7 must
be a separate commit so it can be reverted independently.

## Verify-first list (not confirmed while writing this plan)

1. Which package declares `zod ^3.24.0` (`apps/nex-cli/package.json` or `packages/contracts/package.json`), and whether
   the agent tool schemas are zod 3 or 4.
2. Whether `nexStdioTapDevConfig.ts` already records both stdio directions.
3. Whether the Node agent can be driven by a script without a UI, and which v4 commands the script must send to run a turn.
4. Exact bytes JS emits for negative and large integers in `serialize()` (step 0.8).
5. The full list of v4 command kinds and service channel names (the `z.literal` grep only found a few).
6. Whether CI runners can run the Node CLI build needed by G0.8 within the job time limit.
7. Whether `pnpm test` works at all at the repo root (no root `test` script exists; each package defines its own).
