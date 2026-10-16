# Phase 3 — Port services to Go, channel by channel

Part of [the Go rewrite plan](README.md). Depends on [Phase 1](phase-1-go-agent.md) (Go agent) and [Phase 2](phase-2-go-server.md) (Go RPC front + gateway).

## Goal

The Go server owns every RPC channel and HTTP route that the Web UI and Desktop UI use, runs the **Go agent** itself (spawn, protocol client, v4 connection scopes), and
no longer needs the Node upstream for any service listed in `inventory.md` — except features explicitly assigned to Phase 4 sidecars. Each port is switched on by a line in
`gateway-routes.json` (Phase 2), proven by differential tests against the Node implementation, and can be switched back.

## Non-goals

No UI changes. No new RPC methods or changed shapes. No schema migration of any SQLite database. Features owned by Phase 4 (plugins' runtime, MCP, workflows, CUA, browser-use) keep their
Node implementation behind the gateway until Phase 4 ports them.

## Entry criteria

`gate.sh 1` and `gate.sh 2` exit 0.

## Service inventory (from `packages/services/src`, measured)

Registered in `createLocalServices` (`node.ts`): `IBroadcastService IClientScenesService ICodingPlanSubscriptionService ICommandsService IConversationShareService ICredentialService
ICuaPermissionService ICuaPipSessionService IFileService IFileWatcherService IGitCheckpointService IGitService IMcpSyncService IMediaPreviewService IMemoryService IModelSelectionService
INexAgentService INexSessionService INexTaskService IOnboardingRecordService IPetService IPluginManagementService IPluginsService IPluginSyncService IPromptAttachmentTransferService
ISettingService ISettingsSyncService ISkillsService ISkillSyncService ISubagentsService ISystemService ITerminalService`, plus `IBotsService`, `IProviderSettingsService`,
`IProviderProvisioningTargetService`, `IWindowControllerService` (see `node.ts` lines ~2132–2177; regenerate the authoritative list from `inventory.md`). The storage module (`storage/contract.ts`) and pets are registered separately.

Directory sizes (non-test TypeScript lines, `wc -l`): nex-agent 18.8k · bots 13.8k · session 8.5k · conversation-share 5.5k · git 4.8k · model-provider 3.3k · settings-sync 2.4k ·
process 2.3k · cua-permission-broker 1.9k · window-controller 1.8k · feedback 1.7k · plugin-sync 1.5k · terminal 1.5k · subagents 1.4k · skills 1.4k · file 1.3k · system 1.2k ·
commands 1.2k · storage 1.1k · runtime-tools 1.1k · skill-sync 0.9k · mcp-sync 0.9k · nex-session 0.9k · setting 0.7k · providers 0.7k · hooks 0.7k · fs 0.5k · memory 0.4k · others < 0.4k each.

## Port procedure (identical for every channel — "the recipe")

Every service is ported by following these seven sub-steps. Each phase step below says only what is _special_.

| #   | Sub-step                                                                                                                                                                                                                                                                    | Output                        |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| a   | **Contract:** read the interface (`<svc>/<svc>.ts`) and list every method and `on*` event with parameter/result types; mark each `done/todo` in `inventory.md`. Generate Go types (Phase 0 pipeline).                                                                       | `inventory.md` rows           |
| b   | **Behavior capture:** a recording script drives the _Node_ service through every method with fixture inputs (success, each documented error, edge cases) through the real RPC (so serialization is included) and saves `go/testdata/services/<svc>/<method>/{in,out}.json`. | golden files                  |
| c   | **Port** into `go/internal/services/<svc>/` (logic) + `go/internal/adapters/...` (I/O). Constructor takes interfaces for clock, filesystem, process runner, HTTP client, logger.                                                                                            | Go package                    |
| d   | **Differential test:** `scripts/go-rewrite/svc-diff.mjs <svc>` runs the same recorded calls against Go and Node on identical fixture directories; compares result JSON and the resulting on-disk state (tree hash).                                                         | test target `make diff-<svc>` |
| e   | **Event test:** for each `on*` event, a scripted sequence of triggers; compare event payload sequences.                                                                                                                                                                     | golden event logs             |
| f   | **Routing switch:** add `"<ChannelName>": "go"` in `gateway-routes.json`; run the replay + UI smoke for that service.                                                                                                                                                       | config change + smoke log     |
| g   | **Soak/leak:** `go test -race`, goleak, 10-minute loop of the recorded calls with memory stable.                                                                                                                                                                            | CI job                        |

A service is **done** when a–g are complete, its `inventory.md` rows are `ported`, and `gate.sh 3` check `G3.<svc>` passes.

Rules that apply to all steps:

- Method-name semantic: methods named `onX`/`onDynamicX` are events (see Phase 2 fact 4). The Go `Channel.Listen` must expose exactly those.
- Error `name`/`code`/`kind` strings are part of the contract (UI branches on them). Copy them from the TS source; a lint test greps Go for each `code` string seen in `packages/ui` branches (`inventory.md` lists them).
- `workspaceIdentity` / `workspacePath` rules from `AGENTS.md` apply to every params struct that has both fields (a shared `workspace.Key()` helper; compile-time ban on other keying via a test that greps for `workspacePath` used as a map key).
- Async IO only (Go: every blocking call takes `context.Context`).

## Steps

### 3.1 Shared service infrastructure

- **Source:** `packages/services/src/{collection.ts,accessor.ts,descriptors.ts,paths.ts,logger/serviceLogger.ts,fs/atomicFileUtils.ts,fs/fsFaultInjection.ts,broadcast/*,types/*}`, `packages/shared/src/{paths…,env.ts,runtimeEnv.ts}`.
- **Target:** `go/internal/services/{registry.go,broadcast/,paths/}`, `go/internal/adapters/fsx/` (atomic write, fault injection hook, temp file policy), `go/internal/adapters/logx/`.
- **Do:**
  - `Registry`: name → `rpc.Channel`; per-connection overrides (the three overrides from Phase 2 fact 7) via a `ConnectionScope` interface.
  - `paths`: byte-for-byte port of `paths.ts` for data dir resolution: explicit setter > `NEX_DATA_BASE_DIR` > `HOME`; `getAppConfigDir() = <root>/v2`; task index path `tasks-index.sqlite`; workspace hash; the legacy locations. Include Windows rules from `DataBaseDirTargetValidationOptions` (read it).
  - `broadcast`: port `IBroadcastService` (read it — theme/language fan-out; **the loop-prevention rule** from `AGENTS.md` must be preserved: a broadcast must not echo to its originator).
  - Atomic file helpers shared by settings, sessions file, pets etc.; fault-injection hook for tests (port `fsFaultInjection.ts`).
- **Tests:** path table (30 cases from `paths.ts` incl. Windows drive/UNC with `GOOS` build-tagged tests and a pure function variant taking `platform` so they all run everywhere); atomic write crash test (kill between temp write and rename → either old or new content, never partial); broadcast no-echo test.
- **Done when:** tests pass; Registry used by Phase 2's server in place of the stub.

### 3.2 Tier 1 — stateless I/O adapters: `ISystemService`, `IFileService`, `IFileWatcherService`, `IMediaPreviewService`

- **Source:** `system/{system.ts,systemService.ts,integratedTerminalShells.ts,sshConfigAlias.ts}`, `file/{file.ts,fileService.ts,workspaceFileIgnore.ts,workspaceFileSearch.ts,workspaceFileMentionFilter.ts}`, `fileWatcher/*`, `media-preview/*`; shared `packages/shared/src/{workspaceFileSearch.ts,workspaceFileEntriesCodec.ts,intranetProbe.ts,media-preview.ts}`.
- **Methods (verified from `file.ts`):** `searchWorkspaceFiles readdir stat checkFilesExist resolvePath ensureConversationWorkspace createDefaultWorkspace createScratchWorkspace readTextFile readMediaPreview readFileRange readBinaryPreview
listWorkspaceFilesLength listWorkspaceFilesRange readWorkspaceFileSearchIgnore applyWorkspaceFileSearchIgnoreTransform writeWorkspaceFileSearchIgnore` (+ others after line 79 — re-read); system: `info listIntegratedTerminalShells probeIntranet`.
- **Special:**
  - `listWorkspaceFilesRange` returns a custom codec (`workspaceFileEntriesCodec.ts`) — port byte-exactly with vectors.
  - Ignore-file logic (`workspaceFileIgnore.ts`, 428 lines) must match `.gitignore` semantics the TS library (`ignore` package) implements; use a Go gitignore library **only after** a 200-case corpus (generated by running the `ignore` package) passes; otherwise port the matcher.
  - `fileWatcher`: `fsnotify`; debounce/coalesce rules from the TS; Linux inotify limits and macOS/Windows differences documented; tests with real file operations in temp dirs and a polling fallback test.
  - `probeIntranet`: network probe with timeouts — uses the shared HTTP client adapter; test with `httptest`.
  - `sshConfigAlias.ts` (708 lines): parses `~/.ssh/config` (Include, Host patterns, Match?). Port with the TS test corpus; add 50 real-world-shaped configs (synthetic, no real hosts).
- **Tests:** recipe a–g; plus symlink, case-insensitive FS (macOS/Windows CI), long path (Windows `\\?\`), non-UTF-8 filename, permission denied, huge directory (100k entries, memory bound), binary vs text detection vectors.
- **Done when:** `G3.system`, `G3.file`, `G3.watcher`, `G3.media`.

### 3.3 Tier 1b — `IGitService`, `IGitCheckpointService`

- **Source:** `git/{git.ts,gitService.ts,gitCheckpointService.ts,gitCheckpoint.ts,gitCommitMessageGenerator.ts,commitMessageFileScope.ts,config.ts,providers/,repo/}` (4.8k lines). Interface verified: `getRepositorySummary getWorkspaceRepositoryInfo getLocalBranches getCommitGraph switchBranch createBranchAndSwitch getChanges getIgnoredPaths getDiff getBranchComparison stagePaths unstagePaths discardPaths generateCommitMessage commit push getIdentity refresh`.
- **Special:**
  - Shell out to the `git` binary through the exec adapter (never a Go git library: output formats such as `--porcelain=v2` and hook behavior must be identical). Argument arrays only; `LC_ALL=C`; no interactive prompts (`GIT_TERMINAL_PROMPT=0` — confirm what the TS sets in `config.ts`).
  - `generateCommitMessage` calls a model: depends on the model client from Phase 1 (`internal/model`) and on `modelSelection`. Port after 3.6; until then this one method stays on Node via a **method-level** override in the routing table (extend the routes file to allow `"IGitService.generateCommitMessage": "node"`; implement partial-channel routing in the router: a channel with mixed ownership forwards unlisted methods).
  - `push` uses credentials from the user's git config/credential helpers: tests use a local bare repo only; no network.
- **Tests:** recipe plus: golden outputs from a fixture repo built by a script (branches, merges, renames, binary files, submodule, detached HEAD, empty repo, worktree, 10k-commit graph for `getCommitGraph` performance); error cases (not a repo, bare repo, locked index, dirty tree on switch); Windows path/CRLF (`core.autocrlf`) cases on the Windows runner.
- **Done when:** `G3.git`, `G3.gitcheckpoint`.

### 3.4 Tier 2 — `ITerminalService`

- **Source:** `terminal/{terminal.ts,terminalService.ts,terminalProfile*.ts,terminalPtyFallback.ts,terminalPtyLoader.ts}`, `system/integratedTerminalShells.ts`. Interface: `create write resize dispose` + dynamic events `onDynamicData(id)`, `onDynamicExit(id)`.
- **Special:**
  - PTY: Unix `github.com/creack/pty`; Windows ConPTY via `github.com/UserExistsError/conpty` (or direct `CreatePseudoConsole`) — decide by a spike (2 days) that passes the tests below on the Windows runner. The TS has a **fallback** (`terminalPtyFallback.ts`, 140 lines — read why: probably when `node-pty` fails to load); Go has no native-module failure mode, so the fallback is dropped **only if** the spike is green on all platforms; record this in `decisions.md`.
  - Shell/profile selection (`terminalProfile*.ts`, macOS-specific 367 lines): port with table tests; macOS-specific code behind `//go:build darwin` with a pure-function core testable on Linux.
  - Event semantics: data events are UTF-8 strings (decoder must handle split multi-byte sequences at read boundaries); `onDynamicExit` fires once; `dispose` kills the process tree (reuse Phase 1 process-tree code).
- **Tests:** spawn a shell, write `echo hi`, expect output; resize propagates (`stty size` / `mode con`); split-UTF-8 stress (random chunking of a 1 MB emoji stream); exit code propagation; dispose kills children (orphan check); 50 terminals in parallel; high-throughput test (`yes | head -c 50M`) with bounded memory and backpressure through the RPC event buffer.
- **Done when:** `G3.terminal` passes on 3 OSes.

### 3.5 Tier 3 — settings family: `ISettingService`, `ISettingsSyncService`, `IClientConfigService`, `IClientScenesService`, `IOnboardingRecordService`, `ICredentialService`, `IDeviceService`, `ICodingPlanSubscriptionService`

- **Source:** `setting/*` (448+ lines: `settingsWriteQueue.ts`, `normalizeSettingsPatch.ts`, `observableSettingService.ts`, `legacyAccountConnectionSettings.ts`), `settings-sync/*` (2.4k), `client-config/*`, `client-scenes/*`, `onboarding/*`, `credential/*`, `device/*`, `coding-plan-subscription/*`; shared `validationAppSettings.ts`, `config-schema.ts`, `settings-source.ts`, `settings-sync.ts`, `settings-errors.ts`, `clientConfig.ts`.
- **Special:**
  - **State owner:** `setting.json` (path via `paths`); writes serialized by a queue (port `settingsWriteQueue.ts` semantic: coalescing? ordering? — read it). Node and Go may both write during the transition → use the same lock-file/atomic strategy the TS uses; if it has none, add **read-modify-write with mtime check and retry** in Go and open a ticket to add the same in Node (document in `decisions.md`). Settings merge-patch semantics from `normalizeSettingsPatch.ts` verbatim.
  - `updateDataBaseDir`: moves the whole data directory → critical; port last in this tier with a filesystem-fault test suite (disk full, cross-device rename, partial copy, concurrent writer).
  - `credential`: how secrets are stored (keychain? file?) — read; if it uses OS keychain through Electron (`safeStorage`) it is **desktop-main-only** and out of scope; verify with the `IPlatformService` boundary in `AGENTS.md`.
  - Validation: `validationAppSettings.ts` zod → Go validators agreeing on the Phase 0 corpus.
  - Broadcast: settings changes fan out through `IBroadcastService`; preserve no-loop rule.
  - `settings-sync` (2.4k): a sync protocol with remote servers — read fully; likely HTTP + merge. Treat as its own sub-step with its own captures.
- **Tests:** recipe plus concurrency (two writers), crash-safety, legacy file migrations (fixtures from `legacyDataDirCopy.test.ts` cases), validation corpus, broadcast loop test.
- **Done when:** `G3.settings` and sub-gates for each service.

### 3.6 Tier 3b — providers and models: `IModelSelectionService`, `IProviderSettingsService`, `IProviderProvisioningTargetService`, model-provider, `packages/provider`, `packages/provider-node`

- **Source:** `model-provider/*` (3.3k), `providers/*`, `packages/provider/src/*` (23 files, 4.8k lines), `packages/provider-node/src/*` (13 files, 1.2k), `packages/model-option-map` (done in 1.7), `modelsDevCatalog.ts`, `remoteModelList.ts`, `providerSettingsConnectivity.ts`, shared `model-*.ts`, `provider-*.ts`.
- **Special:**
  - Phase 1 already ported the _read_ side needed by the agent (provider config parsing, model-option-map). Phase 3 ports the **write/management** side: personal provider config repository, builtin provider config materialization (`bundledNexBuiltinProviderConfig.ts` — the config is bundled at build time via `scripts/builtin-provider-config.mjs`; the Go server must embed the same artifact), registry sync, model selection config, connectivity tests (`testModelConnectivity` uses HTTP to providers — use the shared client).
  - **One implementation of provider config parsing** must remain: Go agent (1.6) and Go server share `go/internal/providerconfig/` (move the Phase 1 code there in this step; Phase 1 tests must still pass). Do not duplicate.
  - `providerProvisioning*`: **security-sensitive** (carries cross-environment credentials; only `desktop-continuous` connections may use it — Phase 2 fact 7). Port with explicit tests: web-remote connection calling `apply` → rejected with the exact error; trusted host → allowed.
  - Legacy importers (`legacyNexConfigProviderReader.ts`, `legacyPersonalProviderConfigImporter.ts`, `legacy-cli-personal-provider-config-importer.ts` in bootstrap): fixtures from real-shaped legacy configs (synthetic) → identical output.
  - models.dev catalog fetch: cache/ETag behavior; tests with `httptest` and a recorded catalog sample.
- **Tests:** recipe plus config precedence table, overlay merge vectors, secret redaction in logs (grep test on captured logs for fake key `sk-test-…`), connectivity test against mock.
- **Done when:** `G3.providers`; `GitService.generateCommitMessage` switched to Go.

### 3.7 Tier 4 — knowledge extensions (stateful on disk): `ISkillsService`, `ISkillSyncService`, `ICommandsService`, `IMemoryService`, `IHooks…` services, `IPluginsService` (read side), `IPluginManagementService`, `IPluginSyncService`, `IMcpSyncService`

- **Source:** `skills/*` (1.4k), `skill-sync/*`, `commands/*` (1.2k), `memory/*`, `hooks/*` (652), `plugins/*`, `plugin-sync/*` (1.5k), `mcp-sync/*` (873); shared `skills-types.ts`, `skill-scan-policy.ts`, `plugin-types.ts`, `plugin-sync.ts`, `plugin-marketplaces.ts`, `mcp-sync.ts`, `workspace-hook-*.ts` (digest, discovery, mutation, review-monotonicity, trust-store-file); `CONTEXT.md` (plugin store vocabulary — read before touching plugin names).
- **Special:**
  - These services overlap with agent-side adapters (`apps/nex-cli/packages/adapters/src/{skills,plugins}`). **Single implementation rule:** shared logic lives in `go/internal/skills`, `go/internal/plugins` (frontmatter parsing, scan policy, marketplace metadata, version compare, atomic dir install); both server services and agent use it. The agent side (loading for runs) is Phase 4 step 4.7; Phase 3 builds the library and the management services on top.
  - Archive handling: `zip`/`tar.gz` extraction with zip-slip protection, size limits, symlink policy (`yauzl`/`yazl` used in TS). Security tests: zip-slip, absolute paths, symlink escape, zip bomb ratio limit (set the same limits as `skill-scan-policy.ts`).
  - Workspace hook trust (`workspace-hook-*`): **security/consent state** — monotonicity rules (`workspace-hook-review-monotonicity.ts`), digest algorithm, trust-store file format must be byte-compatible with Node (the agent reads it too: `adapters/src/storage/workspace-hook-trust-store.ts`). Differential tests on digest vectors; property test for monotonicity (trust never silently widens).
  - Marketplace network operations through the shared HTTP client: CDN partitions and merged metadata (`~/.nex/cli/plugins/marketplaces/...` layout from `apps/nex-cli/README.md`).
  - Plugin _runtime_ effects (MCP servers started by plugins) belong to Phase 4; here only list/install/enable/disable/describe/validate/uninstall (`plugins/*` methods from the legacy protocol list: `list overview describe install update uninstall setEnabled configure resetConfig restoreBuiltin validate marketplace/{add,remove,update} cancelOperation operationProgress referenceCatalog resolveSuggestedReference`).
- **Tests:** recipe plus archive security suite, frontmatter corpus (200 files from bundled skills in `apps/nex-cli/packages/bundled-skills` + synthetic edge cases), cross-implementation trust-store compatibility, offline mode (no network → cached metadata).
- **Done when:** `G3.skills`, `G3.plugins`, `G3.mcpsync`, `G3.hooks`, `G3.memory`.

### 3.8 Tier 5 — peripheral: `IPetService`, `IConversationShareService`, `IPromptAttachmentTransferService`, feedback, `IUsageStats`, `IStorageService`, process telemetry, `ICuaPermissionService`/`ICuaPipSessionService`

- **Source:** `pets/*` (+ `docs/specs/desktop-pets.md`, `docs/research/codex-pets-implementation.md`, route `/api/pets/:id/spritesheet`), `conversation-share/*` (5.5k, 8 files), `prompt-attachment-transfer/*`, `feedback/*` (1.7k), `usage-stats/*`, `storage/*` (managed module; layers `domain/app/adapters`; public contract `storage/contract.ts` — keep layering in the Go package layout), `process/*` (2.3k), `cua-permission-broker/*` (1.9k, **desktop-only**: macOS accessibility — confirm; likely stays Desktop/Node), `nex-cua` package.
- **Special:** CUA services are tied to macOS native brokers (`packages/nex-cua`, `packages/desktop/native`): classify as **keep-as-is** (served by Desktop main/host or Node sidecar) unless D4 says otherwise; the gateway routes them to Node. Record in `inventory.md` as `kept` with reason.
  `conversation-share` (5.5k): read first — uploads conversation data to an external service? Privacy-sensitive: no real endpoints in tests, redaction rules (`feedbackPrivacy.ts`) vectors. `storage` module: managed architecture — port the domain/app/adapters split exactly (scan jobs, clean plans); tests on a synthetic data dir with known sizes.
- **Tests:** recipe plus privacy redaction vectors; pets spritesheet HTTP (content-type by extension, cache header `private, max-age=60`, 404 bodies `{"error":"Pet not found"}` / `{"error":"Spritesheet unreadable"}`, path confinement test); storage scan accuracy vs `du`-style walk.
- **Done when:** `G3.peripheral` and every row in `inventory.md` for these services is `ported` or `kept` with a reason.

### 3.9 Tier 6a — bots: `IBotsService` and HTTP `/api/bots/*`

- **Source:** `bots/*` (27 files, 13.8k lines), `packages/shared/src/bots.ts`, route handler `handleBotCallback` in `http.ts` (read above: supports provider `webhook` only over HTTP; others use long-poll/websocket SDKs).
- **Special:**
  - Runtimes: `feishuChannelRuntime.ts`, `telegramChannelRuntime.ts`, `weixinChannelRuntime.ts`. Feishu uses `@larksuiteoapi/node-sdk`; Go equivalent `github.com/larksuite/oapi-sdk-go/v3`. Telegram: `go-telegram-bot-api` or raw HTTP. WeChat: read the TS (custom protocol?).
  - Bots **create sessions and send prompts** → depends on the Go agent service port (3.10). Order: port bots _after_ 3.10, or keep Node bots until then.
  - HTTP semantics to preserve exactly: status mapping (400/401/503 pass-through, 200 otherwise), body shape, `x-nex-bot-secret` header, `rawBody` forwarding, unsupported-provider errors (`{"error":"Unsupported provider: X"}`, `{"error":"Provider X does not support HTTP callbacks."}`, 503 `Bots service is not available.`).
  - Retry/offset correctness bug-history in comments (Telegram offset must not advance on failure) → port those comments as tests.
- **Tests:** recipe with fake provider servers (`httptest`) for each platform's API shape; webhook secret tests; idempotency/duplicate delivery tests; offset persistence crash test; command parser table (`commandParser.ts`, `commandOrder.ts`); reply formatter golden outputs (Markdown conversions per platform).
- **Done when:** `G3.bots`.

### 3.10 Tier 6b — the agent bridge and task services (the critical path)

This is the hardest step. Do it only after tiers 1–5 are routed to Go and stable for at least one week of soak.

- **Source (sizes measured):** `nex-agent/nexAgentService.ts` 4 890 lines · `nexAgentProcessManager.ts` 1 520 · `nexAgentConnectionScope.ts` 1 069 · `nexAgent.ts` (interface) 871 · `nexProtocolClient.ts` 397 ·
  `nexStdioTransport.ts` 389 · `nexTaskServiceAdapter.ts` 5 659 · `nexTaskIndexSyncer.ts` 1 956 · `nexSessionEventCoalescer.ts` 202 · `session/taskIndexRepo.ts` 2 567 · `session/nexTaskService.ts` 746 ·
  `session/automation{Repo,Service,Cron,…}.ts` ~2.2k · `nex-session/*` 859 · `window-controller/*` 1 786 · `session/tasksDatabase/*` · `claude-native/*` (import of Claude native sessions) ·
  `process/processTreeTerminator.ts` and `runtimeProcessLifecycle.ts`.
- **Interfaces:** `INexAgentService` (≈70 methods: prepareStorage, getStorageStartupState, onDynamicStorageStartupState, initialize, syncAppRuntimePreferences, createSession, resumeSession, listSessions, readSession\*, subscribe/resync/unsubscribe conversation + sessions-index + workspace-config topics, plugin/skill/workflow reference catalogs, saved workflows, MCP status, hook trust, …), `INexTaskService` (setConfigOption, setModel, getTaskTokenUsage, restartWorkspaceProcess, deleteTask, renameTask, setTaskPinned, archive…), `INexSessionService`, `IWindowControllerService`.
- **Sub-steps (each separately gated):**
  1. **3.10.1 Process manager** (Go): spawn the Go agent (`GLM_BINARY_PATH` semantics → now an internal resolver; keep the file-based override), per-workspace residency keyed by `workspaceKey = identity || path`, spawn cwd (`nexAgentSpawnCwd.ts`), environment construction (`buildAgentWorkspaceIdentityEnv`, proxy env — `agentProxyEnv.ts`), stdio transport with process-tree termination (`processTreeTerminator`), timeouts and idle reclaim, runtime identity/epoch, stderr collector, lifecycle logging (`NEX_AGENT_LIFECYCLE_LOG_MARKER`), diagnostics parsing (`parseNexProcessDiagnostic`).
     Tests: fake agent (Go test helper binary) scripted crashes, hangs, slow start, EOF behavior (the 1.8 s stdio EOF wait), 100 spawn/kill cycles with no zombie (check process table), per-workspace isolation, restart with `bumpRuntimeEpoch`.
  2. **3.10.2 Protocol client** — **done, `G3.protoclient`**: `go/internal/agentproc/proto`. Includes the Go port of `TopicWireFrameAssembler` (`wire-assembler.ts`, ~560 lines; moved here from Phase 1 because only the host consumes it) with the vectors already generated in `go/testdata/vectors/wire.json` (`assemblyCases`: 25 scenarios covering 14 distinct fault reasons, ordinals, supersede and timeout rules) — **all 25 pass byte-exact**. Request/response correlation, notification fan-out, timeouts, cancellation; version/capability negotiation; `startup/storageState`; `interaction/*` requests from the agent routed to the right connection/window (permission, user input, browser, provider runtime headers, official MCP auth headers — each is an agent→host _request_ needing a host answer).
     Routing seam: `Client.RegisterInteractionHandler(method, fn)` per connection/window; the default with no handler is JSON-RPC method-not-found, which the agent already degrades on.
     Tests: the 25 assembler vectors; a live round trip against the **real Go agent binary** (S01 handshake shape, `-32601` for an unknown method, gate release, concurrent correlation); correlation/cancellation/timeout/observation-vs-business timeout split; interaction routing; goroutine-leak delta check. The Phase 0 S01–S18 host-side transcript equivalence and the Go-host↔Node-agent comparison remain 3.10.3's scope (they need the connection scope that lands there).
  3. **3.10.3 V4 connection scope facade** — **done, `G3.connscope`**: `go/internal/services/agentscope` (`nexAgentConnectionScope.ts`, ~1k lines). The per-connection subscription ownership: `hello`/`clientHello` handshake, role (`terminal-client` vs `trusted-host-relay`), `clientMode`, `workflowRunDeltas` capability negotiation, frame routing by `(connectionId, topic)`, unsubscribe routes, flow control (`V4ConnectionFlowState`), disposal, plus the `interaction/*` routing seam on top of 3.10.2's `RegisterInteractionHandler`.
     - The shared base agent service is the `AgentBase` interface (one method per topic family, one `PassThrough` table for the ~40 forward-only methods), so the agent bridge of 3.10.5 implements it and the tests implement it in memory.
     - The wire vocabulary (topic keys, `HelloMessage`/`ClientHello`, `V4ConnectionFlowState`, frame decoding, fault strings) is **reused from `internal/agentproc/proto`** rather than re-declared, so the facade and the protocol client cannot drift.
     - Tests: **both client modes × both roles** on every rule; one regression test per comment-described past bug in the TS — `hello` required before `initialize`, a client that changes mid-connection, the strict `clientHello` parse, the caller-supplied `workflowRunDeltas`/`connectionId`/`clientMode`/`deliveryProfile`/`subscriberScope` being stripped, the relay's length-prefixed `connectionId` namespace (the `a/b`+`c` vs `a`+`b/c` collision), the ACK-race staging window (early frames released in arrival order, only for the acked `subscriptionId`, whole-batch overflow failure with a reverse unsubscribe, a late ACK after close reverse-unsubscribed), `available` being **not** a runtime-restart boundary while `unavailable` **is**, a late ACK refused without unsubscribing the new runtime's same-named subscription, the relay unsubscribe route pinning vs the terminal no-guess rule, `Saturated` replayed on subscribe (waiting for the next high-water edge would leave a route unsignalled), `closed` being terminal, the last subscription closing its route **before** unsubscribing, a failed control RPC not breaking the chain, the command `clientId` mismatch and the clock-probe exemption from the workspace pin, the late attachment-begin abort, and disposal leaving other connections untouched. Plus 12 concurrent connections proving no cross-delivery and no loss, and a goroutine-leak delta check over every pump exit path.
  4. **3.10.4 Task index & syncer**: `tasks-index.sqlite` schema (`tasksDatabase/{schema-v1,migrations,provider-selection-v2,official-glm-selection-v3,prepared,startup}.ts`) — migrations verbatim as in 1.3; repo (`taskIndexRepo.ts` 2.5k lines) and the syncer that mirrors agent sessions into the index (`nexTaskIndexSyncer.ts`). Cross-engine tests as in 1.3.
  5. **3.10.5 Task service adapter** (5.6k lines): owner/lease, cross-Host routing, stale-run protection, queue, pending optimistic state boundaries. **AGENTS.md: "保留 owner/lease、跨 Host 路由和 stale run 防护，不能仅根据单一路径删除边界判断."** Every guard stays unless a test proves it unreachable. Method: first write a **guard inventory** (`phase-3-guards.md`): every `if` that returns early on staleness/ownership, with the TS line and a scenario that triggers it; then port; each guard row needs a Go test id. No guard may be dropped without a recorded decision.
  6. **3.10.6 Window controller** (`IWindowControllerService`; server variant in `packages/server/src/windowController.ts`): sidebar list controller topics `controller/workspaces`, `controller/tasks-index`; per-connection attachment.
  7. **3.10.7 Automation** (cron): `croner` semantics → Go cron library with **identical next-fire computation** (vector file of 500 cron expressions × start times → fire times, produced by `croner`; DST transitions and time zones included; `automationCronValidation.ts` rules). Persistence in `automationRepo.ts` (1.5k).
  8. **3.10.8 Claude-native import** (`session/claude-native/*`): importer of Claude Code sessions; parser fixtures (synthetic jsonl).
  9. **3.10.9 Cutover of the agent leg**: switch `INexAgentService`, `INexTaskService`, `INexSessionService`, `IWindowControllerService` to `go` in routes; Node upstream no longer spawns agents.
- **Special tests for this step:**
  - **Record-replay at the RPC level**: run the Phase 2 e2e UI scenarios (create task, prompt, tool call, permission, cancel, reload mid-turn, archive, pin, rename, delete, fork, queue, compact, model switch) against Node-front and Go-front; compare the _normalized sequence of RPC frames and v4 frames_ per scenario. Differences must be zero or explained in `phase-3-diffs.md` and approved.
  - **Chaos suite** (`scripts/go-rewrite/chaos-agent.sh`): kill agent mid-turn, kill server mid-turn, drop WebSocket mid-frame, delay frames 2 s, duplicate a client command (same `commandId`), resubscribe with stale `base`; invariants: no duplicate assistant messages, no lost user input, session recoverable, no two active turns, no stale-run write.
  - **Multi-window / multi-client**: desktop-continuous + web-remote-replayable attached to the same Host simultaneously (phone remote-control design: "手机远控连接桌面已有 Host attachment，复用会话运行时") — one agent process per workspace, both clients converge to the same final state.
  - **Remote workspaces**: SSH/WSL/Docker still deployed by Node code until Phase 5; the Go task service must work with `workspaceIdentity` remote identities through the existing Node `connectRemote` path (route `/ws/remote/:id` stays Node) — verify identity keys are carried end to end (grep test that no Go code keys by path alone).
- **Done when:** `G3.agentbridge` (below) plus the guard inventory fully covered.

### 3.11 Remote/Docker/SSH services — decision point

- **Source:** `packages/server/src/remote/*` (≈40 files: SSH backend with `ssh2`, WSL backend, Docker backend, deploy, asset installer/CDN/cache, handshake, `stdio-socket.ts`), `/api/connect-remote`, `/ws/remote/:id`.
- **Plan:** keep on Node through Phase 3 (they are only reachable via `/api/connect-remote` and `/ws/remote/:id`, both already routed by the gateway). Their port is Phase 5 step 5.4 because it depends on the Go agent being the deployable artifact. Record as `deferred → phase 5` in `inventory.md`.

## Exit gate — `scripts/go-rewrite/gate.sh 3`

| Check                                                                                                            | Command                                                      | Pass condition                                                                                        |
| ---------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| G3.0                                                                                                             | common `GN.0` block                                          | exit 0 (or baseline-listed failures only)                                                             |
| G3.1                                                                                                             | `gate.sh 1 && gate.sh 2`                                     | exit 0                                                                                                |
| G3.2                                                                                                             | `node scripts/go-rewrite/inventory-status.mjs --phase 3`     | every row for phase-3 services is `ported`, `kept` (with reason) or `deferred → phase 5`; zero `todo` |
| G3.system/file/watcher/media/git/terminal/settings/providers/skills/plugins/mcpsync/hooks/memory/peripheral/bots | `node scripts/go-rewrite/svc-diff.mjs <svc> --all`           | 0 mismatches against Node on all recorded calls and events                                            |
| G3.agentbridge                                                                                                   | `bash scripts/go-rewrite/e2e-rpc-compare.sh --scenarios all` | 0 unexplained diffs between Node-front and Go-front frame sequences                                   |
| G3.guards                                                                                                        | `node scripts/go-rewrite/guards-coverage.mjs`                | every row in `phase-3-guards.md` references an existing, passing Go test                              |
| G3.chaos                                                                                                         | `bash scripts/go-rewrite/chaos-agent.sh --iterations 200`    | all invariants hold in all iterations                                                                 |
| G3.multiclient                                                                                                   | `go test ./internal/services/agent/... -run MultiClient`     | converge in both client modes simultaneously                                                          |
| G3.routes                                                                                                        | `jq '[.channels[]] \| unique' <routes>`                      | only `"go"` except explicitly listed `kept/deferred` channels (list in `decisions.md`)                |
| G3.node-off                                                                                                      | `bash scripts/go-rewrite/run-without-node-upstream.sh`       | with Node upstream disabled, all UI scenarios except `kept/deferred` features pass                    |
| G3.soak                                                                                                          | `bash scripts/go-rewrite/soak-server.sh --hours 4`           | no goroutine/FD/RSS growth beyond thresholds in the script header; no crash                           |
| G3.sec                                                                                                           | `go test ./... -run Security`                                | zip-slip, path traversal, auth bypass, provisioning rejection, secret redaction all pass              |
| G3.lines                                                                                                         | `bash scripts/go-rewrite/check-file-lines.sh`                | no Go file over 400 lines                                                                             |
| G3.ui                                                                                                            | web + desktop checklist in `phase-3-usage.md`                | every item ticked with operator and date                                                              |

## Rollback

Per service: set the channel back to `node` in `gateway-routes.json` and restart; data compatibility is guaranteed by shared formats (verified by cross-engine tests). For the agent bridge (3.10.9) rollback = route the four channels to Node; sessions remain valid because the agent protocol and the store are unchanged.
A canary procedure: route one service at a time for one week of dogfooding before the next tier.

## Verify-first list

1. Full method lists for every service (only `file`, `git`, `system`, `terminal`, `setting`, part of `INexAgentService` and `INexTaskService` were read). Regenerate from the schemas in Phase 0.
2. Whether `ICredentialService` depends on Electron `safeStorage`/OS keychain (desktop-only) or a file.
3. Whether `cua-permission-broker`, `window-controller`, `process`, `pets` have Desktop-main-only halves.
4. What `terminalPtyFallback.ts` falls back to and why.
5. Settings write concurrency model in TS (lock vs queue vs none).
6. Which bots platforms are shipped enabled, and WeChat's protocol.
7. `conversation-share` data flows (external endpoints, what is uploaded).
8. How `claude-native` import is triggered (UI action vs startup).
9. Whether partial-channel routing (method-level overrides in the router) is acceptable to the maintainers, versus whole-channel ports only.
