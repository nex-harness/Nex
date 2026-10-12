# Phase 1 — Go agent behind the existing host

Part of [the Go rewrite plan](README.md). Depends on [Phase 0](phase-0-contracts.md).

## Goal

A Go binary `nex-agent` that the **unchanged** TypeScript host can spawn in place of the Node agent
(`app-server --stdio`), and that runs the core loop end to end: handshake, sessions, v4 conversation topic
with snapshot/delta/resume, model streaming, the minimum tool set, permission prompts, cancel, compaction, fork,
queueing, persistence in the shared SQLite store. Desktop and Web UIs work against it for ordinary coding sessions.

Features outside the core loop (subagents, hooks, memory, MCP, plugins, webfetch/websearch, workflows) are
**Phase 4**. In Phase 1 the Go agent must answer `runtime/capabilities` honestly so the host/UI degrade those features
instead of crashing (step 1.4).

## Non-goals

No server work. No change to UI code. No schema change to the SQLite store (D8). No TUI.

## Entry criteria

Gate `scripts/go-rewrite/gate.sh 0` exits 0. In particular the 18 agent traces S01–S18, the replay harness, the
SQLite fixtures and the generated contracts exist.

## Layout (all new)

```
go/cmd/nex-agent/                       entry: `app-server --stdio`
go/internal/protocol/                   LF framing, JSON-RPC-style envelope, v4 wire codec, fragment assembler, CRC32
go/internal/v4/                         topic publisher: snapshot, deltas, logEpoch/seq, resume, coalescing
go/internal/v4/commands/                one file per command group (mirrors bootstrap/.../commands/handlers)
go/internal/session/                    turn machine, runtime, command inbox, queue, residency
go/internal/model/                      provider-neutral model interface, request builder, stream assembler, retry
go/internal/adapters/model/anthropic/   Anthropic Messages SSE client
go/internal/adapters/model/openai/      OpenAI chat-completions + responses SSE client
go/internal/tools/                      registry, executor, one sub-package per tool
go/internal/permission/                 rules, modes, bash read-only policy (stub in Phase 1, full in 1.14)
go/internal/adapters/exec/              process spawn, output collector, process-tree kill (per OS)
go/internal/adapters/fs/                atomic write, text range read
go/internal/adapters/store/             SQLite repositories + migration runner
go/internal/adapters/config/            config discovery (.agents protocol, AGENTS.md), provider_config.json
go/internal/trace/                      traceId / span helpers (from phase 0 stub)
```

Rule: `internal/session`, `internal/v4`, `internal/tools/*` (logic), `internal/permission`, `internal/model` import
**no** `os/exec`, `net/http`, `database/sql` (enforced by the depguard rule from step 0.3). They receive adapters as
interfaces defined in their own package.

## Steps

### 1.1 Stdio framing and message envelope

- **Source:** `packages/services/src/nex-agent/nexStdioTransport.ts` (LF-only framing; explains why `readline` is wrong:
  U+2028/U+2029), `nexProtocolMessageSchema` in `packages/shared/src/nex-protocol/index.ts` (request / notification /
  response / error union), `apps/nex-cli/packages/cli/src/{protocol-console.ts,protocol-stderr.ts,protocol-lifecycle.ts}`
  (stdout is reserved for frames; third-party output is redirected to stderr).
- **Target:** `go/internal/protocol/{frame.go,envelope.go,stdio.go}`, `go/cmd/nex-agent/main.go` (parses `app-server --stdio`;
  also accepts `agent-server` as an alias, which the source treats identically).
- **Do:**
  - Reader: buffered reader splitting on `\n` only, tolerant of `\r\n`, no maximum line length smaller than 16 MiB
    (`logicalFrameAssemblyMaxBytes`, see step 1.9), invalid JSON → protocol error frame, never a crash.
  - Writer: single goroutine owns stdout; all senders go through a channel; one `json.Marshal` + `\n` per message; flush per
    message; never write anything else to stdout (a `log` hook that panics in tests if stdout is touched).
  - `os.Stdout` is replaced at startup by a guarded writer; `fmt.Print*` to stdout is banned by lint (`forbidigo`).
  - EOF on stdin → graceful shutdown (cancel contexts, flush, exit 0) within `STDIO_EOF_EXIT_WAIT_MS`-equivalent 1.8 s;
    the host waits that long (see `nexStdioTransport.ts`), so exceeding it gets the process killed.
- **Tests:**
  - `go/internal/protocol/frame_test.go`: split frames across arbitrary read boundaries (table test with 1-byte reads),
    U+2028/U+2029 inside a JSON string stays one frame, `\r\n`, empty lines ignored, 8 MiB single frame, invalid JSON.
  - Property test (`testing/quick` or `rapid`): marshal → split at random offsets → parse = identity.
  - `cmd/nex-agent/stdout_guard_test.go`: starts the binary, asserts that logs never appear on stdout.
- **Done when:** unit tests pass on three OSes; `nex-trace replay-agent` S01 reaches the handshake step (it may fail later).

### 1.2 Handshake, `runtime/capabilities`, startup storage notification

- **Source:** `packages/shared/src/handshake.ts`; `nexProtocolMethods.runtimeCapabilities = "runtime/capabilities"`;
  notification `startup/storageState` (`nexProtocolNotifications.storageStartup`);
  `apps/nex-cli/packages/bootstrap/src/nex-protocol/{server.ts,server-operations.ts,storage-startup.ts,startup-resource.ts}`;
  host side `packages/services/src/nex-agent/nexStorageStartupGate.ts` and `NexAgentCommand.supportsStorageStartup`.
- **Target:** `go/internal/protocol/server.go`, `go/internal/session/capabilities.go`.
- **Do:**
  - Implement the request router: method name → handler; unknown method → JSON-RPC-style `method not found` error that the
    host already treats as "degrade" (the source comments say old CLIs returned method-not-found and the host ignores it).
  - `runtime/capabilities` returns exactly the shape in the generated schema, with every Phase 4 capability set `false`.
  - Emit `startup/storageState` according to the schema in `database-startup.ts`.
- **Tests:** replay S01 (strict mode) must pass against the Go agent. Add a contract test: the result validates against the
  generated schema (`contracts`).
- **Done when:** S01 passes.
- **Verify first:** which capability keys the UI/host _require_ versus optionally read. Read `packages/ui` and
  `packages/services` consumers of the capabilities result before deciding defaults.

### 1.3 SQLite store: migrations and repositories

- **Source:** `apps/nex-cli/packages/adapters/src/storage/session-store/` — `migrations.ts` (ids `0001_base_session_store` …
  `0022_backfilled_session_reasoning`), `migration-runner.ts`, `sqlite-session-store.ts`, `rows.ts`, `codecs.ts`,
  `paths.ts`, `options.ts`, `errors.ts`, `repositories/*.ts` (sessions, messages, session-entries, session-inputs, todos,
  usage, input-history, local-settings, permission-full-access, debug, dwf-journal*, script-workflow-*).
- **Target:** `go/internal/adapters/store/` (`migrations/NNNN_*.sql` embedded with `//go:embed`, `runner.go`, `sessions.go`,
  `messages.go`, `entries.go`, `inputs.go`, `todos.go`, `usage.go`, `inputhistory.go`, `settings.go`, …).
- **Do:**
  1. Copy each migration's SQL **verbatim** (not paraphrased) from the TypeScript string constants into embedded `.sql` files;
     record `appVersion` and `id` in a manifest file. A test (below) proves they are identical.
  2. Port the runner semantics exactly: how applied migrations are tracked, transactionality, behavior on a database that is
     _newer_ than the binary (read `migration-runner.ts` — likely refuse), and on partially applied state.
  3. Pragmas: read the TypeScript for journal mode, foreign keys, busy timeout; replicate them. Two processes (Node and Go)
     may open the same file during the transition, so WAL + busy timeout matter.
  4. Repositories behind small interfaces defined by the consumer (`session` package), not by the store.
  5. Port the three code-level migrations `0020`, `0021`, `0022` (they are `.ts` files that do more than DDL — read them;
     `0022` backfills reasoning data) as Go functions with the same results.
  6. Port JSON column codecs (`codecs.ts`, `json.ts`) so data written by Go is readable by Node and vice versa.
- **Tests:**
  - `migrations_test.go`: for each fixture `vNNNN.sqlite` from step 0.9, open in Go, run the runner, assert the final
    `sqlite_master` equals the `schema-v0022.sql` dump (normalized whitespace) and the authored dataset is intact.
  - `fresh_test.go`: empty DB → run all migrations → `sqlite_master` equals the v0022 dump.
  - **Cross-engine test** (`scripts/go-rewrite/crosscheck-store.mjs` + Go test): (a) Node creates a DB and writes data through
    the real repositories, Go opens and reads every table through its repositories and compares to an expected JSON;
    (b) Go writes, Node reads. Run for sessions with 0, 1, 1000 messages.
  - `verbatim_test.go`: parse `apps/nex-cli/.../migrations.ts` (a tiny extractor in the test) and assert each SQL string
    equals the embedded file byte for byte. If the TypeScript changes, this test fails and forces a Go update.
  - Concurrency test: two Go processes and one Node process write for 5 s; no `database is locked` after busy timeout;
    `PRAGMA integrity_check` is `ok`.
  - Benchmark `BenchmarkAppendMessage`, `BenchmarkLoadSession1000`; record numbers in the PR. Compare with `node:sqlite` on
    the same workload; if `modernc.org/sqlite` is more than 3× slower, revisit D3 (cgo `mattn/go-sqlite3`) before continuing.
- **Done when:** all of the above pass on three OSes (the cross-engine test needs Node and runs on all three).
- **Verify first:** what `0001`..`0019` contain beyond the part of `0001` that was read; whether any migration depends on
  JS-side logic (only `0020`–`0022` are known to be code).

### 1.4 Session runtime: records, residency, workspace identity

- **Source:** `core/src/runtime/*` (165 files; start with `agent-runtime.ts` 667 lines, `deps.ts`, `internal*.ts`),
  `bootstrap/src/nex-protocol/{session-residency.ts,session-resident-pool.ts,session-mapper.ts,workspace.ts}`,
  `bootstrap/src/app/{session-store.ts,session-facade.ts,paths.ts}`, `packages/shared/src/remote-workspace-identity.ts`,
  `packages/services/src/paths.ts` (data dir resolution: explicit > `NEX_DATA_BASE_DIR` > `$HOME`; `getAppConfigDir()` = `<root>/v2`).
- **Target:** `go/internal/session/{record.go,pool.go,residency.go,workspace.go}`, `go/internal/adapters/config/paths.go`.
- **Do:**
  - Path resolution identical to `paths.ts` for all platforms; the workspace key helper `identity := strings.TrimSpace(workspaceIdentity); if identity == "" { identity = workspacePath }`.
    A single exported function; lint rule bans other ad hoc keying (grep test).
  - `session/create`, `session/resume`, `session/list`, `session/read`, `session/messages`, `session/events`, `session/close`
    methods exactly as the legacy schemas (they still exist and S02/S10 use them). v4 `createSession` command in 1.10.
  - Resident pool: how many sessions stay in memory, eviction, and what happens to a running turn on eviction —
    port the rules from `session-resident-pool.ts` (read before coding).
  - Capability honesty: every feature not implemented returns the documented "unsupported" error or capability `false`.
- **Tests:** replay S02, S09 (two concurrent sessions), S10 (resume after restart), S18 (identity keys). Unit tests for the key
  helper with local, remote, whitespace-only identity.
- **Done when:** S02, S09, S10, S18 pass (semantic mode for S09).

### 1.5 Turn machine and runtime core

- **Source:** `core/src/agent/{turn-machine.ts (344 lines),turn-state.ts,message-history.ts,session-history-hydrator.ts,tool-part-order.ts,compact-session.ts}`,
  `core/src/runtime/{command-queue.ts,execution-state.ts,methods/*,helpers/*}`, `bootstrap/src/nex-protocol/background-work-owner.ts`.
- **Target:** `go/internal/session/{turn.go,turnstate.go,history.go,executionstate.go}`.
- **Do:** Port as an explicit state machine (`type TurnState int`, one `Transition(event) (TurnState, []Effect)` pure function,
  effects executed by the runtime). The pure function is what gets heavily tested. Derive the state/event tables by reading
  `turn-machine.ts` and writing them down in the Go file's top comment _before_ coding.
  - Long-running design (from `apps/nex-cli/AGENTS.md`): **no hard cap on tool-call count**. Stops come only from context
    limit → auto-compact, user cancel, permission denial, tool timeout, output truncation, provider retry limit.
  - Every goroutine started for a turn takes the turn `context.Context`; cancellation must be observable within 500 ms in tests.
- **Tests:**
  - Table test for `Transition` covering every (state, event) pair; assert unlisted pairs return an explicit error.
  - Fuzz test (`go test -fuzz`) feeding random event sequences: invariants — never two active turns per session, terminal states
    are absorbing, every started tool call reaches a terminal state before the turn ends.
  - `-race` goroutine leak test using `go.uber.org/goleak` after cancel.
  - Replay S02, S07 (cancel), S13 (retry).
- **Done when:** S02, S07, S13 pass; fuzz runs 60 s clean in CI.

### 1.6 Model layer: provider-neutral interface and stream assembler

- **Source:** `adapters/src/model/` (52 files). Read in this order: `model.ts`, `runner.ts`, `runner-stream.ts`,
  `streaming-tool-call-assembler.ts`, `transform.ts`, `tool-transform.ts`, `system-message-compat.ts`, `retry-policy.ts`,
  `retry-budget.ts`, `empty-completion-retry.ts`, `stream-idle-timeout.ts`, `stream-retry-boundary.ts`, `failure-classifier.ts`,
  `failure-*.ts`, `reasoning-history-normalization.ts`, `anthropic-*.ts`, `openai-responses-json-compat.ts`, `strict-tool-schema.ts`,
  `tool-input-normalization.ts`, `tool-call-validation.ts`, `media-transform-policy.ts`, `request-admission.ts`,
  `offpeak-retry.ts`. Config sources: `packages/provider`, `packages/provider-node` (provider config files and registry).
- **Target:** `go/internal/model/` (interface + pure logic), `go/internal/adapters/model/{anthropic,openai}/` (HTTP + SSE).
- **Do:**
  1. Define `type Streamer interface { Stream(ctx, Request) (<-chan Event, error) }` with a closed set of events:
     `TextDelta`, `ReasoningDelta`, `ToolCallStart`, `ToolCallArgsDelta`, `ToolCallEnd`, `Usage`, `Finish{Reason}`, `Error{Class}`.
  2. Streaming tool-call assembler: incremental JSON arguments → complete call; handle providers that send arguments in one
     chunk, many chunks, or with missing `index`; invalid JSON at end → classify (repairable vs fatal) as in
     `tool-call-validation.ts` and `tool-input-normalization.ts`.
  3. Failure classifier: map HTTP status, provider business codes (`failure-provider-business-codes.ts`), TLS errors,
     idle timeout, mid-stream disconnect into the same classes the Node code produces; retry policy and budget with the same
     numbers (copy constants into `model/retrypolicy.go`, each with a comment citing the TS file).
  4. Request admission / concurrency limits (`request-admission.ts`) and idle-timeout (`stream-idle-timeout.ts`).
  5. Reasoning/thinking history: how prior reasoning is replayed per provider (`reasoning-history-normalization.ts`,
     `anthropic-reasoning-metadata.ts`). This is the highest-risk behavior; write the rules in a table in the Go file's doc comment.
  6. Provider config: parse `provider_config.json` (path from `packages/provider-node/src/runtime-paths.ts`) with the same
     precedence rules as `packages/provider` (`sources.ts`, `config-overlay.ts`, `resolver.ts`); model-option-map DSL evaluation
     (`packages/model-option-map`, 867 lines — port in step 1.7).
  7. Adapters: Anthropic Messages (SSE), OpenAI chat-completions (SSE), OpenAI responses. All HTTP through one
     `adapters/httpclient` constructor (proxy from env via `http.ProxyFromEnvironment`, custom CA, timeouts, trace header injection) —
     no other package may construct an `http.Client` (depguard).
- **Tests:**
  - Golden SSE fixtures under `go/testdata/sse/{anthropic,openai-chat,openai-responses}/*.sse` (captured from the mock model
    from step 0.7 and, for realism, hand-written edge cases: split UTF-8 across chunks, `data: [DONE]`, comment lines,
    CRLF line endings, tool arguments split mid-string, reasoning + text interleaved, usage only in last chunk).
    The assembler test reads each fixture at every possible chunk split (`for i in 1..len`) and asserts identical events.
  - Failure table test: 40+ (status, body, error) tuples → class and retryability; derive the table by running the Node
    `failure-classifier` over the same inputs (`scripts/go-rewrite/classify-failures.mjs` produces `failures.json`; Go test consumes it).
  - Retry test with a fake clock (`clockwork` or own interface): 429 with `Retry-After`, 500, network reset; assert attempt count and delays.
  - Contract test against the **mock model** (`mock-model/` from 0.7) for both wire formats.
  - Live smoke (optional, manual, **not in CI**, requires user-provided key): `go test -tags live ./internal/adapters/model/... -run Live`
    documented in the package README; never commit keys.
- **Done when:** S02, S03, S13 pass using the mock model; failure table test 100% agrees with Node; fuzz test on the SSE parser runs 60 s clean.
- **Verify first:** the exact list of providers the product supports (`packages/provider`, built-in provider config
  `bundledNexBuiltinProviderConfig.ts`, `scripts/builtin-provider-config.mjs`). `adapters/package.json` lists three AI-SDK providers
  (anthropic, openai, openai-compatible) but vendor-specific behaviors (e.g. the "official coding plan gateway",
  `official-glm`, `opencode-session.ts`) must be read before declaring support.

### 1.7 model-option-map DSL

- **Source:** `packages/model-option-map/src/{tokenizer,parser,evaluator,compiler,merge-patch,option-maps,types}.ts` (867 lines).
- **Target:** `go/internal/modeloptionmap/`.
- **Do:** Direct port; the package has no dependencies. Generate test vectors by running the TS implementation over a corpus
  (`scripts/go-rewrite/option-map-vectors.mjs` → `testdata/option-map-vectors.json`: expression, input context, expected output or error).
- **Tests:** vectors test + fuzz (parser must never panic; TS and Go agree on accept/reject for 10k random expressions generated
  by the script).
- **Done when:** 100% vector agreement.

### 1.8 Tool executor and the minimum tool set

- **Source:** `core/src/tool/{executor/*,executor.ts,compat.ts,input-normalization.ts,input-validation-model-content.ts,json-schema.ts,
model-contract.ts,path-normalization.ts,diff.ts,edit-matchers.ts}`, `core/src/tool/handlers/{read,write,edit,glob,grep,bash*,todo,
ask-user-question,task-output*,task-stop}.ts`, `contracts/src/tools/*.ts` (schemas + metadata), `adapters/src/{exec,fs}`,
  `apps/nex-cli/dependencies/native-search` (bundled `ripgrep`, `bfs`, `ugrep` binaries with `SHA256SUMS`).
- **Target:** `go/internal/tools/{registry.go,executor.go,schema.go}`, `go/internal/tools/{read,write,edit,glob,grep,bash,todo,askuser,taskoutput,taskstop}/`.
- **Tool contract** (from `apps/nex-cli/AGENTS.md`): each tool declares `InputSchema`, `OutputSchema`, `ReadOnly`, `Destructive`,
  `ConcurrencySafe`, `MaxOutputBytes`, `Timeout`, cancellation semantics, permission needs, `SideEffects` (`none|workspace|git|network|system`),
  idempotency. A registry test fails if any registered tool leaves a field unset.
- **Do (tool by tool, each its own PR, each with its own replay or golden test):**
  - **Registry/executor:** schema validation using the exported JSON Schema; model-visible error text for invalid input must match
    `input-validation-model-content.ts` (the model reads it). Large results are written to an artifact file and a preview + reference is
    returned (`MaxOutputBytes`, head 32 KiB / tail 32 KiB are `toolOutputFinalHeadBytes/TailBytes` in `PROTOCOL_V4_LIMITS`).
  - **Read:** text ranges with line numbers, encoding detection (`iconv-lite` is used in Node; port the same set of encodings you decide
    to support, document the rest as unsupported), image/PDF/video variants are Phase 4. Read-state tracking (`read-state.ts` in contracts)
    — required by Edit/Write "read before write" guard.
  - **Write / Edit:** atomic write via temp file + rename in the same directory; preserve file mode; CRLF/LF preservation; the matcher
    cascade in `edit-matchers.ts` (exact → normalized whitespace → …; read to learn the order) and `diff.ts` output format.
  - **Glob / Grep:** use the bundled native tools when present (path resolution from `native-search`, checksum verification at startup via
    `SHA256SUMS`), fall back to a pure-Go implementation (`filepath.WalkDir` + `regexp` honoring `.gitignore` through
    `github.com/sabhiram/go-gitignore` or equivalent). Both paths must pass the same test table.
  - **Bash:** `adapters/exec` — shell selection per OS (`bash-shell-provider.ts`, `windows-executable.ts`), cwd capture
    (`cwd-capture.ts`), startup snapshot (`shell-init-snapshot.ts`), output collector with truncation, file-backed output
    (`bash-file-output.ts`), progress, timeouts (`bash-timeout-policy.ts`), background jobs (`bash-background-lifecycle.ts`;
    full background-job support is Phase 4, in Phase 1 foreground only), process-tree kill (`process-tree.ts`: `setpgid`+`kill(-pgid)` on Unix,
    Job Objects on Windows — build tags `//go:build windows` / `!windows`).
  - **Todo, AskUserQuestion:** the latter issues an `interaction/requestUserInput` request to the host and waits (cancellable).
  - **TaskOutput/TaskStop:** for foreground-only Phase 1, return the documented "no such task" result.
- **Tests (per tool):**
  - Schema conformance: the Go tool's `InputSchema` equals the exported JSON Schema (deep equal) — drift fails.
  - Table tests with a temp-dir workspace; each table row has a name matching a behavior in the TS handler.
  - **Differential tests** (`scripts/go-rewrite/tool-diff/`): a Node runner executes the same tool input through the real TS handler against
    a copy of a fixture workspace and writes `{input, output, fsAfter}` JSON; Go does the same; compare normalized. Minimum 30 inputs
    each for Read, Edit, Write, Grep, Glob; 60 for Bash arg/output handling.
  - Bash: process-tree kill test spawns `sh -c 'sleep 60 & sleep 60'` (Unix) / `cmd /c "start /b ping -n 60 127.0.0.1 & ping -n 60 127.0.0.1"` (Windows) and asserts
    no descendant remains after cancel (check via `/proc` or `tasklist`); output truncation test with 10 MiB output; timeout test; cwd persistence test;
    non-UTF-8 output test; Windows `.cmd`/space-in-path test (Windows CI only).
  - Windows/macOS/Linux matrix mandatory for exec, fs tools.
- **Done when:** replay S03, S04, S06, S07, S08, S17 pass; differential tests pass; the registry completeness test passes.

### 1.9 v4 wire: logical frames, fragmentation, CRC32 (encoder side only)

> **Scope correction (found while building it):** the plan originally also listed a Go _assembler_ here. `TopicWireFrameAssembler` is used only by the host and the UI
> (`packages/ui/src/v4/*`, `packages/services/.../windowHostSessionsIndexObserver.ts`, `nexTaskIndexSyncer.ts`); the agent only ever calls `encodeTopicWireFrames`
> (`bootstrap/src/nex-protocol-v4/v4-gateway.ts:515`). The assembler therefore moves to **Phase 3** (step 3.10.2, host-side protocol client). Phase 1 builds the encoder and a
> small reference decoder used only in tests.

- **Source:** `packages/shared/src/nex-protocol-v4/{wire.ts,wire-codec.ts,wire-binary.ts,core.ts}`; limits in `PROTOCOL_V4_LIMITS` (`core.ts`).
- **Target (built):** `go/internal/protocol/{limits.go,wire_binary.go,wire_measure.go,wire_jsstring.go,wire_encode.go}`.
- **Done so far:**
  - CRC32 is `hash/crc32` IEEE, asserted equal to `crc32WireBytes` by vectors, not assumed (a Castagnoli mutation fails the tests).
  - `MeasureEnvelope` ports `measureTopicNotificationEnvelopeBytes` (CLI NDJSON, Channel socket and mobile relay carriers; the encoder uses the maximum of the three).
  - `EncodeTopicWireFrames` ports the complete-vs-fragment decision, the binary search for the largest chunk that fits the worst-case fragment, and fail-closed re-measurement.
  - Wire JSON is built by hand in the key order TypeScript emits, and strings are escaped like `JSON.stringify` (U+2028/U+2029 and `<>&` stay raw; Go's `encoding/json` would change byte counts).
  - Limits live in one file with a test that compares them with the values the TypeScript module exports.
- **Tests (all passing):**
  - Vectors from the real TypeScript code (`scripts/go-rewrite/gen-wire-vectors.mts` → `go/testdata/vectors/wire.json`): crc32/base64 cases, 14 base64 decode accept/reject cases, 9 encode cases
    (single frame, 2 MiB, multibyte, astral, escapes, lone surrogate, and the `proto.frameEnvelopeTooLarge` refusal). Every wire frame is compared by length and sha256; sizes by `measured`.
  - Round-trip tests with a reference decoder: 300 random frames across limits, a sweep of limits through the complete/fragment boundary, and a fuzz target.
  - Five encoder mutations (off-by-one budget, wrong worst-case index width, rounded-down base64 size, Go-style U+2028 escaping, Castagnoli CRC) and a dropped-byte mutation each make a test fail.
- **Known difference from TypeScript:** an explicit `MaxPhysicalFrameBytes: 0` means "protocol maximum" in Go (TypeScript throws `proto.invalidLimit.*` for 0 and uses `undefined` for the default); negative values are rejected in both.
- **Still to do for 1.9:** wire the encoder into the topic publisher in step 1.10; replay S12 (large message forcing fragmentation) once the recorder exists. The encoder takes
  an already-serialised logical frame, so whoever builds the frame must match `JSON.stringify` for numbers. Measured against Node 24: Go's `encoding/json` agrees on integers, exponents
  (`1e+21`, `1e-7`), shortest round-trip floats (`0.30000000000000004` at runtime) and large integers, and **differs only on** `-0` (Go `-0`, JS `0`) and `NaN`/`±Inf`
  (Go returns an error, JS emits `null`). The frame builder must normalise those three cases.
- **Done when:** vectors agree (done); S12 passes (blocked on Phase 0 recorder).

### 1.10 v4 conversation topic: snapshot, deltas, resume, rows

- **Source:** `packages/shared/src/nex-protocol-v4/{snapshot.ts,delta.ts,apply.ts,coalesce.ts,rows.ts,fork.ts,submission.ts,input-intent.ts,
attachment-ref.ts,sessions-index*.ts,workspace-config.ts,transport.ts}`; `bootstrap/src/nex-protocol-v4/` (the gateway and projection:
  `product-projection.ts`, `cold-event-merge.ts`, `cold-session-resume.ts`, `cold-file-change-summaries.ts`, `persistent-command-index.ts`,
  `persistent-command-facts.ts`, `queue-auto-drain.ts`, `stable-fork-target.ts`, `create-session-command-fact.ts`),
  `bootstrap/src/nex-protocol/v4-bridge.ts` (binds `ConversationV4Gateway` to the protocol; frames go out as notification `v4/conversation/frame`).
- **Methods to implement** (`V4_METHODS`, `packages/shared/src/nex-protocol-v4/transport.ts:332`; the names found in the source):
  `v4/command`, `v4/commands/query`, `v4/conversation/{subscribe,unsubscribe,resync,rowsRange,usage,plans,fileChanges,fileRewindPreview,
attachmentRead,attachmentStat,backgroundBashOutput}`, `v4/controller/{subscribe,unsubscribe,resync}`, `v4/attachment/{begin,chunk,commit,abort,read,previewSource}`,
  `v4/connection/flow`, `v4/usage/stats`. Workflow-run reads (`v4/conversation/workflowRun*`) are Phase 4.
  Notifications: `v4/conversation/frame`, `v4/telemetry/event`, `v4/telemetry/local-ttft`, `v4/cua/permission-observation` (CUA is Phase 4).
- **Target:** `go/internal/v4/{gateway.go,publisher.go,snapshot.go,delta.go,apply.go,coalesce.go,rows.go,resume.go,cold.go,projection.go}`.
- **Do:**
  - **State owner:** the per-session event log (`logEpoch` + monotonic `seq`) is owned by the Go `v4.Publisher`; subscribers hold only a
    cursor. No other component writes the log (single writer, documented at the top of `publisher.go`).
  - Subscribe: with `base{logEpoch,seq}` valid → `mode:"resume"` and replay deltas from `base.seq`; otherwise snapshot. Define "valid" exactly
    as the TS does (epoch equality, seq within retained window `eventRetentionPerSession` = 2000).
  - Per-subscriber backpressure: `subscriberBufferMaxOps` / `Bytes`; on overflow → emit resync-required fault (see TS) rather than drop silently.
  - `clientMode`/delivery profile: `desktop-continuous` vs `web-remote-replayable` must behave per `helloMessageSchema.superRefine`
    (they must match: continuous ↔ desktop-continuous, replayable ↔ web-remote-replayable). **Both must be tested** (AGENTS.md).
  - Coalescing (`coalesce.ts`) of text deltas: port with the same flush thresholds; a differential test is mandatory because the UI
    performance depends on it.
  - Cold resume from the SQLite store (messages + session_entry) must regenerate the same rows as the live projection (`cold-event-merge.ts`).
  - Idempotency table per session (`idempotencyTablePerSession` 512) and `commandPendingTtlMs` (24 h), persisted via the same store
    tables as `persistent-command-facts.ts` (find the table; if it is not in the 22 migrations it may be JSON — verify).
- **Tests:**
  - Replay S11 (snapshot → deltas → reconnect-resume) in **both** client modes.
  - Property test: for a random sequence of ops, `apply(snapshot, deltas…)` equals the final snapshot (client reducer invariant).
    Use the TS `apply.ts` as an oracle via vectors (`scripts/go-rewrite/apply-vectors.mjs`: 200 random op sequences → expected final state).
  - Resume tests: base too old, wrong epoch, seq in the future, restart of the agent (new epoch) → snapshot.
  - Backpressure test: slow subscriber exceeds the buffer → fault, other subscribers unaffected.
  - Cold vs live equivalence test: run S02 live, restart, cold-resume, compare rows.
- **Done when:** S11, S12, S14, S10 pass in both modes; vectors agree.

### 1.11 v4 commands (core subset)

- **Source:** `commandPayloadSchemas` in `packages/shared/src/nex-protocol-v4/command.ts` — **34 command types** (names verified):
  `createSession createSelectionSideSession sendText sendGoalCommand stop compact forkAssistant applyFileRewind editUserQuery retryTurn
setAssistantFeedback sendQueuedNow editQueueItem reorderQueueItem deleteQueueItem setAutoDrain resolveInteraction
respondWorkspaceHookReview toggleWorkspaceHookReviewItem revokeWorkspaceHookTrust requestWorkspaceHookReview
snoozeInteractionAutoResolution switchModelConfig switchCollaborationMode setFollowupMode pauseGoal resumeGoal cancelBackgroundWork
resumeWorkflowRun startSavedWorkflow amendWorkflowRunSettings renameSession deleteSession discardSharedContext`;
  handlers in `bootstrap/src/nex-protocol-v4/commands/handlers/{session-flow,queue,session-mgmt,goal-compact,model-config,interaction-background,
fork-edit-retry,file-rewind,assistant-feedback,selection-side-session}.ts`; executor `commands/executor.ts` (command result statuses:
  `accepted, rejected, stale, duplicate, noop, failed`; selection-side-chat restricts 7 commands).
- **Phase 1 implements (18):** `createSession sendText stop compact forkAssistant editUserQuery retryTurn sendQueuedNow editQueueItem
reorderQueueItem deleteQueueItem setAutoDrain resolveInteraction switchModelConfig switchCollaborationMode setFollowupMode renameSession
deleteSession`.
  **Deferred to Phase 4 (16):** `createSelectionSideSession sendGoalCommand pauseGoal resumeGoal applyFileRewind setAssistantFeedback
respondWorkspaceHookReview toggleWorkspaceHookReviewItem revokeWorkspaceHookTrust requestWorkspaceHookReview snoozeInteractionAutoResolution
cancelBackgroundWork resumeWorkflowRun startSavedWorkflow amendWorkflowRunSettings discardSharedContext`. Unimplemented commands return
  `V4CapabilityUnsupportedError`-equivalent results (read `interaction-background.ts` for the exact error shape) and the capability flag is `false`.
- **Target:** `go/internal/v4/commands/{executor.go,sessionflow.go,queue.go,sessionmgmt.go,compact.go,modelconfig.go,forkretry.go,interaction.go}`.
- **Do:** `CommandInbox` semantic — accepted busy/running inputs are admitted **serially** (`AGENTS.md`); Renderer keeps only drafts and optimistic overlay;
  Host owner/lease handles routing. Implement admission as a per-session channel consumed by one goroutine. Preserve stale-run protection: a command
  carrying a `baseRevision`/run id that no longer matches the session returns `stale`, never executes.
- **Tests:** per-command table tests (status + resulting deltas) generated from the TS handlers' behavior via `scripts/go-rewrite/command-vectors.mjs`;
  replay S02, S14 (queue while busy), S15 (compact), S16 (fork), S17; concurrency test: 100 `sendText` commands from 10 goroutines → exactly one active
  turn at a time, FIFO within a client, no loss, no duplicate on retried `commandId` (idempotency).
- **Done when:** S02, S14, S15, S16, S17 pass; vectors agree.

### 1.12 Permissions and interaction broker

- **Source:** `core/src/permission/{broker,service,rule-matching,plan-mode-policy}.ts`, `bootstrap/src/nex-protocol/{interaction-broker.ts,
interaction-preferences.ts,interaction-response-race.ts,permission-options.ts}`, `bootstrap/src/permission-options.ts`,
  `core/src/tool/handlers/{bash-command-permission-policy,bash-command-rule-evaluator,bash-cwd-policy,bash-git-runtime-safety}.ts`,
  `packages/shared/src/permission-request-preview.ts`.
- **Target:** `go/internal/permission/`, `go/internal/session/interaction.go`.
- **Do:** modes `build|edit|plan|yolo` (from `switchCollaborationMode`); rule matching for allow/deny lists from config
  (`.nex/cli/config.json` → `permission`); "autoApproveHighRisk" and "allowMediumRiskInAuto" keys (seen in `bootstrap/test/e2e/README.md`);
  the interaction request/response race (user answers after cancel → ignored) from `interaction-response-race.ts`.
  Bash read-only policy: in Phase 1 only a **conservative stub** (treat every Bash command as requiring permission unless exactly in a short allowlist of
  the simplest read-only commands); the full port is step 4.x because it is ~30 files plus a bash parser (see Phase 4).
- **Tests:** replay S04, S05; unit tests for rule matching (table, derived from `rule-matching.ts` cases); race test for response-after-cancel;
  property test: no tool classified `Destructive` ever executes without a recorded permission decision (instrument executor with a fake broker).
- **Done when:** S04, S05 pass; the destructive-without-decision property test passes.

### 1.13 Context building, system prompt, compaction

- **Source:** `core/src/context/{builder,dynamic-sections,sections/*,types,utils}.ts`, `core/src/compact/{policy,microcompact,manual,prompt,rounds}.ts`,
  `core/src/system-reminder/*`, `bootstrap/src/{custom-command-prompt,runtime-platform-headers}.ts`, locales in `apps/nex-cli/packages/i18n`.
- **Target:** `go/internal/session/context/`, `go/internal/session/compact/`.
- **Do:** prompt text is _data_: copy prompt templates verbatim into `go/internal/session/context/prompts/*.tmpl` (embedded) with a test that compares them to the
  TS source strings. Dynamic sections (env info, git status, AGENTS.md discovery via `.agents` protocol, date) behind interfaces. Compaction: trigger thresholds
  from `policy.ts`, microcompact rules, compact prompt, rounds. Token counting: find how `policy.ts` estimates tokens (likely heuristic) and port that.
- **Tests:** snapshot test of the full system prompt for 6 fixture contexts equals the TS output (`scripts/go-rewrite/prompt-vectors.mjs`);
  compaction policy table test; replay S15; long-conversation test with the mock model: 200 turns, assert compact triggers exactly when TS would.
- **Done when:** prompt snapshots byte-equal; S15 passes.
- **Verify first:** whether the prompt includes any value that is non-deterministic or environment-specific (hostname, locale) and how TS normalizes it.

### 1.14 Switch, packaging, end-to-end acceptance

- **Source:** `packages/services/src/nex-agent/nexAgentProcessManager.ts` (`resolveDefaultNexAgentCommand`, command precedence), its test
  `packages/services/test/nexAgentCommandResolution.test.ts`, `providerRuntimeResolver.ts` (`GLM_BINARY_PATH`, bundled-agents dirs),
  `apps/nex-cli/package.json` build scripts, `scripts/build-nex.mjs`.
- **Target:** `scripts/go-rewrite/build-agent.sh` (cross-compiles `nex-agent` for 5 targets into `go/dist/<os>-<arch>/nex-agent[.exe]`);
  `docs/specs/go-rewrite/phase-1-usage.md` (how to run the UI against the Go agent).
- **Do:** No host code change is required if `GLM_BINARY_PATH` is honored for the process the user starts (confirmed in `providerRuntimeResolver.ts`,
  `findNexAgentRuntimeBinary`). Verify that claim by experiment in this step (below). If the resolver prefers another path in some modes (dev runtime prefers the
  monorepo bundle — see the test file's comments), document the exact recipe per mode.
- **Tests:**
  - **Smoke (automated):** `scripts/go-rewrite/e2e-agent-smoke.mjs` starts `@nex/server` (HTTP entry) with `GLM_BINARY_PATH=<go agent>`, mock model, throwaway data dir;
    uses `@nex/client` + RPC to create a session, send a prompt, wait for the final assistant message, and asserts: the process tree contains the Go binary
    (not node), the answer text is the mock's, the SQLite store contains the messages.
  - **Web UI e2e (semi-automated):** follow `bootstrap/test/e2e/README.md` (ToolSearch/Codemode needs Phase 4; run the simple scenario) using the `agent-browser`/`dogfood`
    skills in `.agents/skills` against `pnpm dev:web` with `GLM_BINARY_PATH` set. Record screenshots under `go/testdata/e2e/phase1/` and the checklist result in
    `phase-1-usage.md`. Checklist: create task, send prompt, streamed text visible, edit file with permission prompt, deny a permission, cancel a running bash,
    reload page mid-turn (replayable resume), close/reopen task, switch model, rename, delete.
  - Both client modes: the Web UI covers `web-remote-replayable`; Desktop (`pnpm dev:desktop`) covers `desktop-continuous` — run the same checklist once and record it.
  - Cross-engine compatibility: after a session created by the Go agent, start the Node agent on the same data dir and open that session (and vice versa).
  - Soak: 2 h session with the mock model replaying 500 turns with tool calls; memory (RSS) growth < 50 MiB after warm-up; no goroutine growth (`runtime.NumGoroutine` logged at debug).
- **Done when:** all of the above pass/recorded.

## Exit gate — `scripts/go-rewrite/gate.sh 1`

| Check | Command                                                                                                             | Pass condition                                                                                                                                               |
| ----- | ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| G1.0  | common `GN.0` block                                                                                                 | exit 0 (or baseline-listed failures only)                                                                                                                    |
| G1.1  | `gate.sh 0`                                                                                                         | exit 0 (earlier gates keep passing)                                                                                                                          |
| G1.2  | `cd go && go run ./cmd/nex-trace replay-agent --cmd ./dist/linux-amd64/nex-agent --args "app-server --stdio" --all` | S01–S18 each PASS or are listed in `phase-1-deferred.md` with the Phase 4 feature that owns them (S01–S18 above are all core-loop; expected deferrals: none) |
| G1.3  | `go test ./internal/adapters/store/... -run Migrations\|Verbatim\|Fresh`                                            | all 22 fixtures migrate; verbatim SQL test green                                                                                                             |
| G1.4  | `node scripts/go-rewrite/crosscheck-store.mjs`                                                                      | Node↔Go round trip equal in both directions                                                                                                                  |
| G1.5  | `go test ./internal/protocol/... -run Wire\|Vectors`                                                                | wire vectors agree both directions                                                                                                                           |
| G1.6  | `go test ./internal/v4/... -run Vectors\|Resume\|BothModes`                                                         | apply/coalesce/command vectors agree; resume in both client modes                                                                                            |
| G1.7  | `go test ./internal/model/... ./internal/adapters/model/...`                                                        | SSE fixtures at every chunk split; failure table agrees with Node                                                                                            |
| G1.8  | `go test ./internal/tools/... -run Differential`                                                                    | tool differential tests agree with TS handlers                                                                                                               |
| G1.9  | `go test -race ./internal/session/... && go test -run xxx -fuzz FuzzTurn -fuzztime 60s ./internal/session/`         | clean                                                                                                                                                        |
| G1.10 | `node scripts/go-rewrite/e2e-agent-smoke.mjs --agent go`                                                            | exit 0 and prints `agent-binary=go`                                                                                                                          |
| G1.11 | `go test ./internal/adapters/exec/... -run ProcessTree` on all three OSes                                           | no orphan descendants                                                                                                                                        |
| G1.12 | `bash scripts/go-rewrite/build-agent.sh && ls go/dist/*/nex-agent*`                                                 | 5 binaries; each `--version` prints (native ones)                                                                                                            |
| G1.13 | manual checklist in `phase-1-usage.md`                                                                              | every item ticked with date and operator, for Web and Desktop                                                                                                |
| G1.14 | `grep -rn "os/exec\|net/http\|database/sql" go/internal/{session,v4,permission,model,tools}` (excluding adapters)   | no hits                                                                                                                                                      |
| G1.15 | `bash scripts/go-rewrite/check-file-lines.sh`                                                                       | no Go file over 400 lines                                                                                                                                    |

## Rollback

The Go agent is opt-in via `GLM_BINARY_PATH`; unsetting it restores the Node agent. Data written by the Go agent stays readable by Node
because migrations are shared and verbatim (G1.3, G1.4). If a defect corrupts data, the SQLite files are standard; restore from the
backup the user keeps before enabling the Go agent (state this in `phase-1-usage.md`).

## Verify-first list

1. `GLM_BINARY_PATH` precedence in every run mode (desktop packaged, desktop dev, server, SEA). Only `findNexAgentRuntimeBinary` was read.
2. Whether `0001`–`0019` migrations contain JS-only logic; whether the tasks database (separate SQLite file under `packages/services`) is touched by the agent at all
   (it appears to be host-side — `tasks-index.sqlite` under `getAppConfigDir()`).
3. The exact set of capability keys the host/UI require from `runtime/capabilities`.
4. The set of providers and vendor quirks (`official-coding-plan-gateway.ts`, `opencode-session.ts`, `openai-responses-json-compat.ts`) required for launch.
5. Where `persistent-command-facts` data lives (table vs file).
6. Wire fragment size and the `complete`/`fragment` decision rule in `wire-codec.ts` (not read).
7. Token estimation method used by compaction policy.
8. Whether the host sends v4 attachment methods during the Phase 1 scenarios; if so, attachments move from Phase 4 into this phase.
