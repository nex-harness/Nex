# Phase 4 — Agent breadth (features beyond the core loop)

Part of [the Go rewrite plan](README.md). Depends on [Phase 1](phase-1-go-agent.md). Runs in parallel with [Phase 3](phase-3-services.md).

## Goal

The Go agent reaches **feature parity with the Node agent** for everything the product exposes, so that Phase 5 can make it the only agent. Phase 1 left these as capability `false`:
full Bash permission policy and background jobs, subagents, hooks, memory, MCP (client, OAuth, plugin-provided servers), plugins and skills loading, custom commands, web tools,
image/PDF/video reading, cron tool, tool-search, goals, file rewind, workspace-hook trust review, saved workflows and workflow runs, codemode/REPL, browser use, CUA. Items that are
JavaScript-bound are delivered through **sidecars** (decision D4) behind a stable Go interface.

## Non-goals

No new product features. No protocol changes. No TUI/CLI port (D6).

## Entry criteria

`gate.sh 1` exits 0. D4 (sidecar vs drop) and D6 resolved in `decisions.md` — **this phase cannot be planned in detail for the JS-bound features without D4**; steps 4.12–4.15 are written for the default (sidecar).

## Size reference (measured, non-test TS lines)

core: subagent 4.1k · hooks 2.8k · memory 1.0k · mcp 1.0k · workflow 5.3k · codemode 1.5k · repl 0.9k · system-reminder 0.4k · browser-client 3.1k · plugin-reference 0.5k.
adapters: mcp 6.0k · plugins 6.3k · skills 0.8k · browser 1.6k · image 0.6k · network 0.7k · auth 0.6k · device 0.8k · pdf 0.3k.
Other: dynamic-workflow 20.5k (94 files) + runtime 1.3k · node-repl-host 1.4k · bootstrap/src/app 22.6k (100 files, mostly workflow-run services).

## Layout additions

```
go/internal/subagent/        profiles, runner, steering, completion notification
go/internal/hooks/           configured hook runner, workspace hook trust (shares code with server via go/internal/hooktrust)
go/internal/memory/          extraction, recall, directory, index content
go/internal/mcp/             client pool, stdio/http transports, OAuth, exposure/tool-search integration
go/internal/plugins/         (shared with server services, built in Phase 3.7)
go/internal/skills/          (shared with server services, built in Phase 3.7)
go/internal/commands/        custom commands (markdown), slash commands
go/internal/tools/{webfetch,websearch,readpdf,readimage,readvideo,cron,skill,toolsearch,sendmessage,agent,...}/
go/internal/bashpolicy/      shell parser + read-only/permission policy
go/internal/sidecar/         JSON-RPC-over-stdio client for Node sidecars (workflow compiler, codemode, node-repl, browser)
go/internal/egress/          public-egress policy shared by webfetch and MCP
sidecars/                    (TypeScript) thin entry points that expose existing packages over the sidecar protocol
```

## Steps

Ordering: 4.1 → 4.2 are prerequisites for most others. 4.3–4.11 are independent after that and can be assigned in parallel. 4.12+ need D4.

### 4.0 Record the Phase 4 golden traces (before porting each feature)

- **Why:** Phase 0 recorded only S01–S18 (core loop). Each step below cites new traces S19–S50; they must exist **before** the Go code for that feature, recorded from the Node agent with the Phase 0 recorder (`scripts/go-rewrite/record/`), so the Go port has an oracle.
- **Target:** `scripts/go-rewrite/record/scenarios/S19…S50.json` + `go/testdata/traces/agent/S19…S50/`, and an extended `mock-model/` able to emit the needed tool calls (Agent, Skill, MCP tools, WebFetch, Read of image/PDF, workflow tools).
- **Do:** Record each trace as its owning step starts (not all up front). Each recording passes the same determinism check as in 0.7 (two recordings normalize to identical bytes). Needed fixtures (synthetic only): a fixture MCP server (`echo-mcp-server.mjs`), a local HTTP site for WebFetch, sample images/PDFs, a skill and a plugin directory, an untrusted workspace hook.
- **Tests:** `node --test scripts/go-rewrite/record/*.test.mjs` includes the new scenarios; `nex-trace replay-agent --cmd <node agent> --all` passes S01–S50 against the Node agent before any Go work on that feature.
- **Done when:** for the feature being started, its traces exist, are deterministic, and replay green against Node.

### 4.1 Egress policy and shared HTTP hardening

- **Source:** `adapters/src/http/{public-egress-policy.ts,response-body.ts,index.ts}`, `adapters/src/network/{http-config.ts,proxy-fetch.ts,subprocess-env.ts}`, `contracts/src/network/*` (`getPublicEgressIpBlockReason`, `normalizeIpAddressLiteral`, `createHttpClientError`), `core/src/tool/handlers/webfetch-egress-guard.ts`.
- **Target:** `go/internal/egress/{policy.go,dialer.go}`, `go/internal/adapters/httpclient` (extends Phase 1's client).
- **Do:** A `net.Dialer.Control`/custom `DialContext` that resolves, checks **every resolved address** against the block list (loopback, private, link-local, CGNAT, multicast, IPv4-mapped IPv6, metadata addresses such as 169.254.169.254, etc. — copy the table from `contracts`), and dials the _checked_ IP (prevents DNS rebinding). Re-check on every redirect hop. Response body size limit and streaming reader (`response-body.ts`). Proxy support through env (`proxy-from-env` equivalent) with `NO_PROXY`; custom CA bundle; `subprocess-env.ts` (which proxy env to pass to child processes).
- **Tests:** IP classification table generated from the TS function over 2 000 addresses (`scripts/go-rewrite/egress-vectors.mjs`), including IPv6 forms (`::ffff:127.0.0.1`, `[::1]`, zone ids, decimal/octal/hex IPv4 literals — `normalizeIpAddressLiteral`); rebinding test (DNS first answers public then private → blocked); redirect-to-private test; size-limit test; proxy matrix (env combos).
- **Done when:** vectors 100% agree; security tests pass.

### 4.2 Bash parser and permission/read-only policy (full)

- **Source:** `core/src/tool/handlers/bash-*.ts` (≈35 files: `bash-command-parser.ts`, `bash-semantics.ts`, `bash-readonly-policy*.ts`, `bash-command-permission-policy.ts`, `bash-command-rule-evaluator.ts`, `bash-cwd-policy.ts`, `bash-git-runtime-safety.ts`, `bash-gh-rate-limit.ts`, `bash-timeout-policy.ts`, `bash-background-policy.ts`, `bash-metadata.ts`); the TS parser dependency is `unbash`.
- **Target:** `go/internal/bashpolicy/`; parser: `mvdan.cc/sh/v3/syntax`.
- **Do:**
  1. **Corpus first.** Build `go/testdata/bash-corpus/*.sh` ≥ 1 500 commands: all test strings in the TS policy tests, real-shaped agent commands (pipes, `&&`, subshells, heredocs, process substitution, `$(...)`, backticks, redirections, env prefix assignments, `cd x && git status`, `find -exec`, `sed -i`, `xargs`, `awk` with system(), quoting edge cases, unicode, CRLF, extremely long lines, syntax errors).
  2. Run the **TS policy** over the corpus (`scripts/go-rewrite/bash-policy-vectors.mjs`) → `{command, parsed ok, classification (readonly/needs-permission/denied reason), extracted argv list, cwd effect, written paths}`.
  3. Port the policy structure (readonly command tables, flag tables for git/gh/file/process/text commands, multi-word commands) as **data tables** (Go `map`/`switch`), not as ad-hoc code, so diffs against the TS tables are reviewable (`scripts/go-rewrite/diff-policy-tables.mjs` extracts TS tables and compares keys to Go).
  4. Where `mvdan/sh` and `unbash` parse differently (e.g. unsupported syntax), define the safe default: **unparseable or ambiguous → needs permission**. Never "allow on parse failure".
- **Tests:** vector agreement on the corpus: for every command, `Go.classification` must be **no more permissive** than TS (a Go "readonly" where TS says "needs-permission" is a _failure_; the reverse is reported as a regression-to-review but does not fail). Property test: appending `; rm -rf /` or `$(...)`/backtick/redirect-to-file to any readonly command flips it to non-readonly. Fuzz parser with 60 s. Windows: policy for `cmd`/PowerShell commands as the TS does (read `bash-shell-provider.ts` / `windows-executable.ts` for what is supported).
- **Done when:** 0 "more permissive" mismatches on the corpus; all property tests pass. **This is a security gate.**

### 4.3 Bash background jobs, TaskOutput/TaskStop, cron tool

- **Source:** `core/src/tool/handlers/{bash-background-lifecycle,bash-background-policy,task-output,task-output-bash,task-output-projection,task-stop,cron}.ts`, `core/src/runtime-task/*`, `adapters/src/exec/{bash-progress-poller,bash-file-output,output-collector}.ts`, `bootstrap/src/nex-protocol/background-work-owner.ts`, v4 `cancelBackgroundWork` + `v4/conversation/backgroundBashOutput`, `packages/shared/src/background-bash-*.ts`.
- **Target:** `go/internal/tools/{taskoutput,taskstop,cron}/`, `go/internal/session/background/`.
- **Do:** Registry of runtime tasks with owner (session/turn); persisted output files; notification policy (`runtime-task/notification-policy.ts`): when a background job finishes, a synthetic user message/notification is injected; survives agent restart? (read: jobs die with the process? — port exactly); cancel via v4 command; output reads are bounded; cron tool schedules in-session timers (semantics from `cron.ts`).
- **Tests:** replay S07 variants (background start, poll, output, stop, finish notification); agent restart with a running job (assert TS behavior); output truncation/streaming; 20 concurrent jobs; process-tree kill on stop; v4 commands `cancelBackgroundWork` table test; cron vectors reuse the Phase 3.10.7 cron vector file (shared library `go/internal/cron`).
- **Done when:** new traces S19 (background job) and S20 (cron) recorded from Node and passing.

### 4.4 Subagents and the Agent tool

- **Source:** `core/src/subagent/*` (22 files, 4.1k), `core/src/tool/nested/{runner.ts,types.ts}`, `core/src/tool/handlers/{agent,send-message,respond-to-coordinator,submit-result}.ts`, `bootstrap/src/subagents.ts`, `bootstrap/src/nex-protocol/subagent-session-query.ts`, `packages/services/src/subagents/*` (profiles management service, 1.4k — server side, Phase 3 left it? **verify and assign**), `packages/shared/src/subagent-*.ts`, `subagents-types.ts`.
- **Target:** `go/internal/subagent/`, `go/internal/tools/{agent,sendmessage,respondcoordinator,submitresult}/`.
- **Do:** Profiles (markdown with frontmatter: `profile.ts`, `profile-frontmatter.ts`, model selection `profile-model-selection.ts`), built-ins (`general-purpose.ts`, `explore.ts`, `explore-tools.ts`), tool policy per profile (`tool-policy.ts`), context building, nested runner = a child session with its own turn machine, **same `traceId`** (parent/child spans — `AGENTS.md`), message steering (`message-steering.ts`: send message to running subagent), completion notification, tool-event mirroring to the parent (`tool-event-mirror.ts`), runtime-task registry, borrowed MCP port (`borrowed-mcp-port.ts`: child uses parent's MCP connections), computer-use policy, persistent memory per profile, coordinator response flow.
- **Tests:** replay new traces S21 (spawn explore subagent), S22 (parallel subagents), S23 (steer running subagent), S24 (subagent permission request surfaced to parent), S25 (cancel parent cancels children); tool-policy table; depth/concurrency limits per TS; trace propagation test (all child spans share `traceId`); goroutine-leak after cancel.
- **Done when:** S21–S25 pass; sessions-index shows children as the TS does (`session/subagents`, `listSessionSubagents`).

### 4.5 Hooks and workspace hook trust (agent side)

- **Source:** `core/src/hooks/*` (19 files, 2.8k: `runner.ts`, `configured-runner*.ts`, `workspace-hook-policy.ts`, `workspace-hook-review-flow.ts`, `workspace-hook-runtime-admission.ts`, `workspace-hook-telemetry.ts`, `session-mailbox.ts`, `output.ts`), `contracts/src/hooks/*`, `bootstrap/src/{workspace-hook-trust-cli.ts}`, `bootstrap/src/nex-protocol/workspace-hook-trust.ts`, `adapters/src/storage/workspace-hook-trust-store.ts`, `packages/shared/src/workspace-hook-*.ts`, v4 commands `respondWorkspaceHookReview toggleWorkspaceHookReviewItem revokeWorkspaceHookTrust requestWorkspaceHookReview` and capability `workspaceHookReview`.
- **Target:** `go/internal/hooks/`, `go/internal/hooktrust/` (shared with server 3.7 — if already built there, import it).
- **Do:** Event types and payload JSON (stdin to the hook process), exit-code semantics (block/allow/modify), timeouts, output parsing, ordering when multiple hooks match, discovery from config layers (.agents protocol), trust admission (a workspace hook runs only if its digest is trusted; **monotonicity**: trust never widens silently), review flow with the host (interaction requests), telemetry.
- **Tests:** hook runner table (exit codes × output shapes); trust digest vectors shared with server; monotonicity property test; review flow replay trace S26 (untrusted hook → review request → grant → runs; revoke → blocked); malicious hook cases (huge output, never exits → timeout kill tree, writes to stdout non-JSON).
- **Security gate:** an untrusted workspace hook must never execute (test asserts no process spawned).
- **Done when:** S26 passes; vectors agree.

### 4.6 Memory

- **Source:** `core/src/memory/*` (10 files, 1.0k: `extraction.ts`, `memory-agent-loop.ts`, `recall/*`, `directory.ts`, `index-content.ts`, `memory-file-path.ts`, `origin-session.ts`, `project-root.ts`), `packages/services/src/memory/*`, `packages/shared/src/memoryDiagnostics.ts`, panel spec `docs/specs/memory-panel-preview.md`.
- **Target:** `go/internal/memory/`.
- **Do:** Memory directory layout and file formats (markdown + index) identical (users' existing memory must load); extraction runs a model loop in the background after turns (policy in `extraction.ts`); recall selects memories into context (`recall/*`); project root resolution; path safety (memory file path cannot escape directory).
- **Tests:** file-format round trip vs fixtures from Node; recall selection vectors (given memories+query → selected set, requires deterministic selection: if the TS uses a model call, use the mock model with canned outputs); path traversal tests.
- **Done when:** trace S27 (memory write + later recall) passes.

### 4.7 Skills, plugins, custom commands loading (agent side)

- **Source:** `adapters/src/skills/*` (0.8k), `adapters/src/plugins/*` (15 files, 6.3k), `adapters/src/commands/*`, `bootstrap/src/{skills.ts,plugins.ts,custom-commands.ts,custom-command-prompt.ts,custom-command-shell-expansion.ts,skill-command-overrides.ts,slash-command-surface.ts,builtin-prompt-command.ts,builtin-workflow-command.ts,bundled-plugins.ts?}`, `bootstrap/src/app/{bundled-plugins,bundled-skills,official-plugin-*}.ts`, `packages/bundled-skills`, `core/src/tool/handlers/skill.ts`, `core/src/plugin-reference/*`, `bootstrap/src/nex-protocol/{plugins.ts,plugin-reference-catalog.ts,skill-reference-catalog.ts,slash-commands.ts}`, README plugin docs (`apps/nex-cli/README.md`: plugin dirs, manifest `.nex-plugin/plugin.json`, `.mcp.json`, `mcpServers` precedence).
- **Target:** reuse `go/internal/{skills,plugins}` from 3.7; add `go/internal/commands/` and `go/internal/tools/skill`.
- **Do:** Discovery roots and precedence (user/project/plugin/bundled; `.agents` protocol), skill frontmatter and trigger rules, `Skill` tool, custom commands with shell expansion (`custom-command-shell-expansion.ts` — executes shell: goes through the Bash permission path, never directly), plugin manifest parsing, enable/disable state, official plugin seeding (`official-plugin-seed-lock.ts` — file lock semantics), bundled plugin assets embedded (`//go:embed` of the same assets), slash command surface list returned to the UI.
- **Tests:** discovery tables over fixture trees (precedence, shadowing, disabled plugin); manifest corpus (valid/invalid/legacy); shell-expansion safety tests; seed-lock concurrency test (two processes); golden slash-command list equals TS for the bundled set.
- **Done when:** trace S28 (skill invocation), S29 (custom command with args), S30 (plugin-provided command) pass; `plugins/*` legacy methods return identical results to Node on fixtures.

### 4.8 MCP client

- **Source:** `adapters/src/mcp/*` (19 files, 6.0k: `pool.ts`, `stdio-transport.ts`, `network.ts`, `oauth*.ts` ×10, `official-auth.ts`, `descriptor.ts`, `timeout.ts`, `telemetry.ts`, `resource-telemetry.ts`, `process-tree.ts`, `windows-job-object.ts`), `core/src/mcp/*` (exposure, name mapping, image normalization), `bootstrap/src/{mcp-config.ts}`, `bootstrap/src/nex-protocol/{mcp.ts,protocol-mcp-config.ts,official-mcp-auth-port.ts}`, tool-search (`core/src/tool/handlers/tool-search.ts`, `core/test/tool-search*.test.ts`, `mcp-exposure.test.ts`), `packages/shared/src/{mcp.ts,official-mcp-auth.ts,official-mcp-tool-error.ts}`. Dependency in TS: `@modelcontextprotocol/client`.
- **Target:** `go/internal/mcp/`; SDK: `github.com/modelcontextprotocol/go-sdk` — **evaluate first** (spike, 3 days): must support stdio and streamable HTTP/SSE transports, tool/resource/prompt listing, progress, cancellation, roots, sampling (if used), OAuth hooks. If it does not, write the minimal client (JSON-RPC 2.0 over stdio/HTTP) ourselves.
- **Do:** Config merge from layers (global/project/plugin; plugin `mcpServers` vs `.mcp.json` precedence), pool lifecycle (lazy start, idle stop, restart policy, max concurrency, per-server timeouts), stdio processes through the exec adapter with process-tree control (Windows job object — `windows-job-object.ts`: via `golang.org/x/sys/windows` `CreateJobObject`), name mapping `mcp__<server>__<tool>` and collision rules, **exposure** (which tools are visible vs deferred behind ToolSearch — `mcp-exposure`), image/content normalization, large result → artifact, OAuth (authorization code with PKCE, client credentials, refresh, lease/locking `oauth-lease.ts`, credential storage `oauth-credentials.ts` + `auth/credential-cipher.ts`: **encryption format must match Node's** so credentials saved by one are readable by the other — read the cipher; if it depends on OS keychain/machine id, document), localhost callback server (`auth/localhost-callback.ts`), official auth headers via host (`interaction/requestOfficialMcpAuthHeaders`), telemetry notifications (`process/mcpTelemetry`, `process/mcpResourceSamples`), `mcp/list` status method.
- **Tests:** against `apps/nex-cli/packages/bootstrap/test/fixtures/echo-mcp-server.mjs` (exists; referenced by the e2e README) and a Go fake MCP server for each transport; failure matrix (server crashes, hangs, invalid JSON, oversize result, slow init); OAuth against a mock authorization server (`httptest`) for each flow including refresh race (two requests concurrently refresh once — `oauth-lease`); credential cipher cross-compat vectors; tool-search/exposure replay of the **ToolSearch + Codemode e2e** from `bootstrap/test/e2e/README.md` (needs codemode sidecar 4.13 — run the ToolSearch part now, the Codemode part after 4.13) with the expected request-log table (3 requests: tools declared / tool-result counts).
- **Done when:** traces S31 (MCP tool call with permission), S32 (OAuth flow), S33 (tool-search deferral) pass.

### 4.9 Web tools: WebFetch, WebSearch

- **Source:** `core/src/tool/handlers/webfetch*.ts` (12 files: cache, constants, content, egress-guard, errors, network, processing, trace, types, url), `websearch*.ts`, `contracts/src/tools/{webfetch,websearch}.ts`; deps `cheerio`, `marked`, `iconv-lite` in TS.
- **Target:** `go/internal/tools/{webfetch,websearch}/`; HTML→markdown: `github.com/JohannesKaufmann/html-to-markdown` or own converter; DOM: `golang.org/x/net/html` / `goquery`; charset: `golang.org/x/text/encoding`.
- **Do:** URL validation, redirect policy (same-host only? — read `webfetch-url.ts`), egress guard (4.1), content processing (readability-like extraction? — read `webfetch-content.ts`/`processing.ts`), cache (TTL, key, size — `webfetch-cache.ts`), truncation and model-facing summary format; WebSearch backend(s): read `websearch-support.ts`/`websearch-results.ts` — **which provider/API is used and how it is authenticated** must be identified before coding (no real endpoints/keys in tests).
- **Tests:** golden HTML→text corpus (100 pages, synthetic, covering tables, code blocks, scripts/styles, encodings GBK/Shift-JIS/UTF-16 BOM, huge pages); fetch semantics against `httptest` (redirect chains, 304, gzip/br, content-type switch, timeouts, size cap); egress tests via 4.1; cache TTL with fake clock. Output must match TS output **textually** for the corpus (or differences reviewed and listed in `phase-4-diffs.md`).
- **Done when:** corpus agreement ≥ 98% byte-equal, remainder reviewed; trace S34.

### 4.10 Media readers: images, PDF, video; attachments

- **Source:** `core/src/tool/handlers/{read-image,read-pdf,read-video}.ts`, `adapters/src/{image,pdf}/*` (jimp compression, webp passthrough, budget), `adapters/src/model/{media-transform-policy,tool-result-media-projection}.ts`, v4 attachments (`v4/attachment/*`, `bootstrap/src/nex-protocol-v4/attachment-upload-registry.ts`, `packages/shared/src/nex-protocol-v4/{attachment-ref,attachment-faults}.ts`), limits `attachmentMaxBytes` 20 MiB, `attachmentChunkMaxBytes` 512 KiB, `VIDEO_INPUT_MAX_BYTES`, `bootstrap/src/nex-protocol/model-io-preferences.ts`.
- **Target:** `go/internal/tools/{readimage,readpdf,readvideo}/`, `go/internal/v4/attachments/`, `go/internal/adapters/image`, `go/internal/adapters/pdf`.
- **Do:** Image resize/compress to the budget (`image-budget.ts`) with `image/jpeg|png`, `golang.org/x/image/webp` (decode), `github.com/disintegration/imaging` — output need not be byte-identical but must satisfy the same **size and dimension budget** and format rules; PDF text extraction (TS uses? — read `adapters/pdf/index.ts`, 290 lines; likely an external tool or lib) → Go equivalent (`github.com/ledongthuc/pdf` or `pdfcpu`/poppler via exec — decide by corpus accuracy); video: how it is passed to the model (probably as file reference/base64 up to the limit) per provider; attachment upload protocol (begin/chunk/commit/abort/read/previewSource): staging dir, checksum, abort cleanup, size limits, faults enumerated in `attachment-faults.ts`.
- **Tests:** attachment protocol table (every fault); chunk reorder/duplicate/oversize; abort removes staging; budget tests (10 images of varying sizes → within budget, dimension caps); PDF text corpus (20 synthetic PDFs incl. scanned/no-text/encrypted/corrupt) vs TS text with a normalized similarity ≥ 0.95; video size gate.
- **Done when:** traces S35 (image attach + read), S36 (PDF read), S37 (attachment upload + send) pass.

### 4.11 Goals, file rewind, assistant feedback, selection side chat

- **Source:** v4 commands `sendGoalCommand pauseGoal resumeGoal applyFileRewind setAssistantFeedback createSelectionSideSession discardSharedContext` with handlers `goal-compact.ts`, `file-rewind.ts`, `assistant-feedback.ts`, `selection-side-session.ts`; `bootstrap/src/nex-protocol-v4/{cold-file-change-summaries.ts,shared-context-*}`, `packages/shared/src/nex-protocol-v4/{shared-context-import.ts,shared-context-ref.ts,workspace-config.ts}`, `core/src/rewind` (contracts `rewind/`), checkpoint (`gitCheckpoint` is server side; agent side: `adapters/src/context/git-snapshot.ts`), queries `v4/conversation/{fileChanges,fileRewindPreview,plans}`, goal verification schemas (`nexSessionGoalVerificationSchema`, `goalVerificationsRetained` = 20), plan identity (`plan-identity.ts`), `independentPlanState` capability.
- **Target:** `go/internal/v4/commands/{goal.go,rewind.go,feedback.go,sidechat.go}`, `go/internal/session/goal/`, `go/internal/rewind/`.
- **Do:** Goal state machine (set/pause/resume/verify loop: the objective verification runs a model — deterministic with mock), file change tracking per turn (diff snapshots), rewind preview/apply with `workspaceMode: preserve|rewind`, side sessions with command restrictions (7 restricted commands — copy list from `executor.ts`), shared-context import with size limits.
- **Tests:** per-command vector tests (generated from TS) and traces S38 (goal set → verified), S39 (rewind preview + apply, workspace restored byte-equal), S40 (selection side chat restricted commands rejected with `guard.selectionSideChatRestrictedCommand`).
- **Done when:** S38–S40 pass; all 34 v4 commands now implemented: `scripts/go-rewrite/check-v4-commands.mjs` compares Go's registered set with `commandPayloadSchemas` keys → equal.

### 4.12 Sidecar protocol and process (decision D4: sidecar path)

- **Source:** `apps/nex-cli/packages/node-repl-host/src/*` (MCP server hosting a Node REPL — already an out-of-process MCP server), `dynamic-workflow-runtime/src/{protocol,harness,child-entry-file,child-source}.ts` (**an existing child-process protocol** for running workflow scripts — reuse it), `bootstrap/src/app/script-workflow-*.ts`, `core/src/repl/*`, `core/src/codemode/*`.
- **Target:** `go/internal/sidecar/{client.go,supervisor.go,protocol.go}`, `sidecars/{workflow,codemode,node-repl}/` (TypeScript entry points reusing the existing packages).
- **Design:**

  ```
  Go agent ──stdio JSON-RPC (LF-delimited, same framing as 1.1)──► Node sidecar process (one per sidecar kind per agent process)
     owner of state: Go (sessions, permissions, artifacts, persistence)
     sidecar is stateless w.r.t. durable data: it computes (compile/analyze/execute) and calls BACK into Go
     for every external effect (tools, fs, network, model) via the same tool executor → permissions/audit/traceId preserved.
  ```

  - The sidecar never touches the SQLite store, never calls the network, never spawns processes by itself; it requests effects over the protocol (D4 consequence: effects stay observable and approvable, per `apps/nex-cli/AGENTS.md` "外部 I/O 边界收敛").
  - Lifecycle: lazy start on first use; health ping; restart on crash with budget; killed with the agent (process tree); version handshake (sidecar bundle version == agent version).

- **Do:** define the protocol (JSON Schema, versioned, in `go/contracts/sidecar/`), implement the Go client and supervisor, build the sidecar bundle from the TS packages (esbuild/tsup config reuse), package it next to the agent binary, `NEX_*` env: none new (path resolved relative to the executable, D7).
- **Tests:** protocol conformance tests with a fake sidecar (Go) and with the real one; crash/restart/timeout/oversize-message tests; effect-callback tests (sidecar requests `Read` → permission prompt appears exactly as if the model had called it); no-leak test (sidecar killed when agent exits, including SIGKILL of the agent: sidecar detects stdin EOF and exits).
- **Done when:** a "hello" sidecar round trip and the three real sidecars start/stop cleanly on 3 OSes.

### 4.13 Codemode and Node REPL

- **Source:** `core/src/codemode/*` (12 files: `runtime/`, `wasm.ts`, `embedded.ts`, `description.ts`, `output.ts`, `source.ts`, `identifier.ts`; QuickJS WASM via `quickjs-wasi`, parser `meriyah`), `core/src/tool/handlers/{codemode,node-repl,node-repl-model-content}.ts`, `core/src/repl/*`, `docs/specs/codemode.md`, `core/test/{codemode-sandbox,codemode-description-output,nested-runner}.test.ts`, `bootstrap/test/codemode-e2e.test.ts`, `packages/node-repl-host`, `packages/browser-use-plugin`.
- **Target:** `sidecars/codemode/` (wraps existing code), `go/internal/tools/{codemode,noderepl}/` (tool wrappers, schema, permission mapping).
- **Do:** Read `docs/specs/codemode.md` first. The sandboxed script calls `tools.<name>(args)`; each call is forwarded to Go's tool executor (nested runner semantics incl. permission, output size caps, "intermediate results do not enter model context" — the e2e property). Node REPL: MCP server (`node-repl-host`) is already external — Go simply runs it as an MCP server via 4.8 (config `built-in-node-repl.ts`).
- **Tests:** reuse the existing TS tests by running them against the sidecar entry (they test the sandbox itself); plus Go-side: ToolSearch + Codemode e2e from `bootstrap/test/e2e/README.md` with the mock model: expected request log (3 requests; tools declared; toolResultCount 0/1/2; issue bodies absent from the log); sandbox escape attempts (`process`, `require`, `fetch`, infinite loop → timeout, memory bomb → limit) must fail identically to TS.
- **Done when:** the e2e table matches; trace S41.

### 4.14 Dynamic workflows (saved workflows, runs, retune, journal)

- **Source:** `apps/nex-cli/packages/dynamic-workflow/src/*` (94 files, 20.5k: analysis, compiler, engine, facade, lowering, schema; uses `typescript`), `dynamic-workflow-runtime/src/*`, `core/src/workflow/*` (31 files, 5.3k: scheduler, expert, lifecycle, definition), `bootstrap/src/app/dynamic-workflow-*.ts` & `workflow-*.ts` & `script-workflow-*.ts` (≈60 files; service layer), tools `create-workflow*, amend-workflow*, eval-workflow-snippet*, get-workflow-run*, list-saved-workflows, list-workflow-runs, resume-workflow-run, save-workflow*, saved-workflows, resolve-workflow-question, workflow-*`, store tables `dwf_journal`, `script_workflow_*` (repositories `dwf-journal*.ts`, `script-workflow-*.ts`), protocol `workflows/*` and `v4/conversation/workflowRun*`, v4 commands `resumeWorkflowRun startSavedWorkflow amendWorkflowRunSettings`, capability `workflowRunDeltas`, delta ops `workflowRun.*` and reducer files `nex-protocol-v4/workflow-runs-*.ts` (≈20 files in shared).
- **Decision:** the workflow **compiler/analyzer/engine** (TypeScript source → plan → execution) stays in the sidecar (it embeds the TypeScript compiler). The **runs, journal, scheduler state, artifacts, observation/projection to v4 and persistence** are Go-owned. Concretely:
  - Sidecar API: `analyze(source) → diagnostics+graph`, `compile(source) → plan`, `step(plan, journal) → effects` (the engine is event-sourced by the journal if the code confirms this — **verify by reading `engine/` and `dynamic-workflow-run-journal.ts`/`replay.ts` first**).
  - Go: executes effects (spawn subagent actors, run tools), appends journal rows (SQLite `dwf_journal`), computes `workflowRuns` projections and emits `workflowRun.*` deltas, enforces concurrency governor (`workflow-concurrency-*`), quiescence, reconcile after crash (`dynamic-workflow-run-reconcile.ts`), retune.
  - Port the **v4 workflow-runs reducer** (`packages/shared/src/nex-protocol-v4/workflow-runs-*.ts`, ≈20 files) to Go or keep generating via sidecar? The reducer is _host-side/UI-side logic in TypeScript shared with UI_; the Go agent only needs to **emit** state/deltas that this reducer accepts. Port the emit side; verify with the TS reducer as an oracle (`apply-vectors`).
- **Tests:** workflow corpus from `docs`/bundled workflows + 30 authored workflows (fan-out, retries, escalation, ask-user epilogue, failure, cancel, resume); journal replay determinism: run → kill agent at random points (50 iterations) → reconcile → resume → final result equals uninterrupted run; delta/snapshot equivalence with TS reducer on all runs; concurrency governor property tests (never exceed ceiling); traces S42–S47.
- **Done when:** S42–S47 pass; `workflowRunDeltas` capability advertised only if the delta vectors pass.
- **Risk:** largest feature; if D4 says "drop", delete this step and record the user-visible removal.

### 4.15 Browser Use and CUA

- **Source:** `core/src/browser-client/*` (7 files, 3.1k), `adapters/src/browser/*` (1.6k, Playwright-based), `packages/browser-use-plugin`, `packages/node-repl-host/src/{browser-bridge,cua-bridge,cua-broker}.ts`, `packages/nex-cua` (broker/frames/pip-session JS package + `packages/desktop/native` for macOS), `bootstrap/src/app/{node-repl-browser-broker,built-in-node-repl}.ts`, `bootstrap/src/nex-protocol/browser-control-broker.ts`, shared `browser-use/*`, `cuaPermission.ts`, `docs/cua-product-implementation-analysis.md`, interaction requests `interaction/browserExecute`, `interaction/browserList`, notification `computer-use/operation-event`, `v4/cua/permission-observation`.
- **Plan:** unchanged architecture: browser/CUA automation is executed by the **Desktop host** (browser control broker request `interaction/browserExecute`) or by Node REPL (Playwright). Go's job is only the **agent side of the protocol**: issue `browserExecute`/`browserList` requests, handle results, project tool displays, forward `computer-use/operation-event`. The Playwright-based adapter (`adapters/src/browser`) used for headless web/`cli` flows runs in a sidecar (4.12) or is dropped (D4).
- **Tests:** broker request/response replay (recorded from Node: S48 browser command, S49 browser list, S50 CUA permission observation); error and timeout paths; cancel mid-command.
- **Done when:** S48–S50 pass with the existing Desktop host/Node REPL doing the actual automation.

### 4.16 Remaining protocol surface and capabilities flip

- **Source:** legacy methods still referenced by the host: `usage/stats`, `process/*` (`childProcesses`, `resourceSample`, `toolExecResource`), `provider/{testModelConnectivity,updateAccountConfig}`, `automation/*`, `offPeak/*`, `workspace/{generateText,cancelGenerateText,readPresentation,updateDynamicWorkflowPolicy,updateInteractionPreferences,updateModelIoPreferences,updateOffPeakToolPolicy,hooks/trustGrant}`, `session/{goal,usage,debug,promptContext,subagents,requestRuntimePreferences,...}`; `bootstrap/src/nex-protocol/{usage-stats-builder,process-child-processes,resource-sampler,runtime-resources,off-peak-port,automation-port,session-debug,session-prompt-context,workspace-model-runtime,model-mapper,message-mapper,...}`, `bootstrap/src/process-resource-sampler.ts`, `adapters/src/device/process-probe-*.ts`.
- **Target:** `go/internal/protocol/legacy/…`, `go/internal/telemetry/`.
- **Do:** Close every row of `inventory.md` for the agent side: implement or explicitly mark `dropped` with the host/UI consumer proven absent (`grep` evidence recorded). Process telemetry: per-OS probes (`/proc`, `ps`, Windows API via `golang.org/x/sys/windows`/PDH). Then flip `runtime/capabilities` flags to `true` feature by feature **only when that feature's traces pass**; a test asserts no capability is `true` without a passing trace id registered in `go/testdata/capabilities.json`.
- **Tests:** `check-protocol-coverage.mjs`: every method in `nexProtocolMethods` and `V4_METHODS` is either handled by Go or listed `dropped` with evidence; telemetry sampling tests on 3 OSes with a known child process tree.
- **Done when:** coverage script passes with zero unlisted gaps.

### 4.17 Parity sweep and soak

- **Do:** Run **every trace S01–S50** against Go and Node agents with the same mock model; write `phase-4-diffs.md` listing each remaining difference with a decision (accepted / fix). Run a 24 h soak with the mock model replaying a mixed workload (chat, tools, subagents, MCP, background jobs, workflows) at 1 turn / 5 s: RSS, FDs, goroutines, child processes, SQLite WAL size monitored (script prints thresholds).
- **Done when:** all traces PASS or are accepted in the diffs file; soak within thresholds.

## Exit gate — `scripts/go-rewrite/gate.sh 4`

| Check | Command                                                                                                                                                                             | Pass condition                                                                                     |
| ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| G4.0  | common `GN.0` block                                                                                                                                                                 | exit 0 (or baseline-listed failures only)                                                          |
| G4.1  | `gate.sh 1`                                                                                                                                                                         | exit 0                                                                                             |
| G4.2  | `go test ./internal/egress/... ./internal/adapters/httpclient/...`                                                                                                                  | IP classification vectors 100%                                                                     |
| G4.3  | `go test ./internal/bashpolicy/... -run Corpus`                                                                                                                                     | 0 "more permissive than TS" outcomes; property tests pass                                          |
| G4.4  | `go run ./cmd/nex-trace replay-agent --cmd ./dist/linux-amd64/nex-agent --all`                                                                                                      | S01–S50 each PASS or listed in `phase-4-diffs.md` as accepted                                      |
| G4.5  | `node scripts/go-rewrite/check-v4-commands.mjs`                                                                                                                                     | Go registered command set == `commandPayloadSchemas` keys (34)                                     |
| G4.6  | `node scripts/go-rewrite/check-protocol-coverage.mjs`                                                                                                                               | no unlisted gaps in `nexProtocolMethods` / `V4_METHODS`                                            |
| G4.7  | `go test ./internal/hooks/... -run Security`                                                                                                                                        | untrusted hook never spawns; trust monotonicity property holds                                     |
| G4.8  | `go test ./internal/mcp/... ` incl. OAuth, cipher cross-compat                                                                                                                      | pass; credentials saved by Node readable by Go and vice versa                                      |
| G4.9  | `bash scripts/go-rewrite/e2e-toolsearch-codemode.sh`                                                                                                                                | request-log table matches `bootstrap/test/e2e/README.md` (3 requests; counts 0/1/2; bodies absent) |
| G4.10 | `go test ./internal/sidecar/... && bash scripts/go-rewrite/sidecar-orphan-check.sh`                                                                                                 | no orphan sidecar after agent SIGKILL on 3 OSes                                                    |
| G4.11 | `bash scripts/go-rewrite/workflow-chaos.sh --iterations 50`                                                                                                                         | resumed run result == uninterrupted result in all iterations                                       |
| G4.12 | `go test ./internal/... -run Capabilities`                                                                                                                                          | no capability `true` without registered passing trace                                              |
| G4.13 | `bash scripts/go-rewrite/soak-agent.sh --hours 24`                                                                                                                                  | thresholds respected                                                                               |
| G4.14 | UI checklist (`phase-4-usage.md`): plugins page, skills page, MCP status, subagent panel, workflow run card, memory panel, hooks review dialog, goal UI, rewind dialog, attachments | all items ticked (Web and Desktop)                                                                 |
| G4.15 | `bash scripts/go-rewrite/check-file-lines.sh`                                                                                                                                       | no Go file over 400 lines                                                                          |

## Rollback

Agent features are enabled per capability flag; the Node agent remains the default until Phase 5. If a Go feature regresses, set its flag back to `false` (host/UI degrade) or keep using the Node agent via `GLM_BINARY_PATH` unset. Sidecars can be disabled by removing their bundle from the distribution (agent reports capability false).

## Verify-first list

1. D4 and D6 decisions (blocks 4.12–4.15).
2. Whether `packages/services/src/subagents/*` (profile management service) was assigned in Phase 3; assign it explicitly in `inventory.md`.
3. The parser semantics difference between `unbash` and `mvdan.cc/sh` on bash-specific syntax; only the corpus run can answer.
4. MCP Go SDK feature coverage (spike 4.8).
5. Credential cipher (`auth/credential-cipher.ts`) key source — keychain, machine id, or file.
6. WebSearch backend and authentication (4.9).
7. PDF text extraction method in `adapters/pdf/index.ts` and `read-video.ts` behavior.
8. Whether the workflow engine is event-sourced over the journal (decides the sidecar API in 4.14).
9. Resolved: `apps/nex-cli/packages/bootstrap/test/fixtures/echo-mcp-server.mjs` exists. Still to check: which tools it exposes (the e2e needs `read_issue`) and whether it needs npm packages at runtime.
10. Which of `core/src/telemetry`, `system-reminder` and `embedded-search` hold behavior that affects model prompts (prompt snapshots in 1.13 must include them).
