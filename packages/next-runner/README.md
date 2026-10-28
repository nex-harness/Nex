# @nex/next-runner

Agent runner for the `next/` server. It dials the server's `/runner` WebSocket, announces
its workspaces, and serves pi-backed sessions to the unchanged ZCode web UI.

Spec of record: [`docs/next/README.md`](../../docs/next/README.md),
[`docs/next/permissions.md`](../../docs/next/permissions.md) and
[`docs/next/channels-research.md`](../../docs/next/channels-research.md).

## Run

Node 24 is required (see `mise.toml` and the package `engines`).

```sh
# from the repo root
pnpm --filter @nex/next-runner dev

# or directly
cd packages/next-runner
NEX_SERVER_URL=ws://127.0.0.1:3030/runner \
NEX_RUNNER_TOKEN=runner-secret \
NEX_RUNNER_WORKSPACES="$HOME/projects/demo,$HOME/projects/other" \
pnpm dev
```

### Environment

| Variable                 | Required    | Default                      | Meaning                                                                                                    |
| ------------------------ | ----------- | ---------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `NEX_SERVER_URL`         | no          | `ws://127.0.0.1:3030/runner` | ws/wss URL; `/runner` is appended when missing                                                             |
| `NEX_RUNNER_TOKEN`       | recommended | —                            | Bearer token for the upgrade request (`Authorization: Bearer …`)                                           |
| `NEX_RUNNER_ID`          | no          | persisted random id          | Stable identity; persisted in `<data>/runner-id.json`                                                      |
| `NEX_RUNNER_NAME`        | no          | hostname                     | Display name in the server registry                                                                        |
| `NEX_RUNNER_WORKSPACES`  | **yes**     | —                            | Absolute paths, `,`- or path-delimiter-separated (at least one); env roots are never removable from the UI |
| `NEX_RUNNER_DATA_DIR`    | no          | `~/.nex-next/runner`         | Sessions, per-workspace metadata and `workspaces.json`                                                     |
| `NEX_RUNNER_LOG_LEVEL`   | no          | `info`                       | `debug` / `info` / `warn` / `error`                                                                        |
| `NEX_RUNNER_VERSION`     | no          | package version              | Reported in `hello`                                                                                        |
| `NEX_RUNNER_TITLES`      | no          | on                           | `off` disables model-generated session titles                                                              |
| `NEX_RUNNER_TITLE_MODEL` | no          | session model                | `provider/modelId` used only for title generation                                                          |

Logs go to stderr. Tokens and API keys are never logged; the model catalog view exposes
an empty `apiKey` placeholder only.

## Link protocol (server ⇄ runner)

One text-frame JSON WebSocket. The runner sends `hello` first, then handles server
frames:

| Frame                 | Direction | Handling                                                                 |
| --------------------- | --------- | ------------------------------------------------------------------------ |
| `hello` / `welcome`   | R→S / S→R | handshake; `hello.lastAppliedVersion` = the config version last applied  |
| `call`                | S→R       | `(channel, method) → async handler(arg)`; replies `result{ok, value?}`   |
| `listen` / `unlisten` | S→R       | `(channel, event) → (arg, emit) → dispose`                               |
| `event`               | R→S       | one emit for the matching listen id                                      |
| `config`              | S→R       | full merged snapshot; applied one at a time (a late version is ignored)  |
| `configApplied`       | R→S       | `{version, ok, errors[], facts}` for every applied (or rejected) push    |
| `workspaces`          | R→S       | full served-folder list after `runner-workspaces.add`/`remove`           |
| `ping` / `pong`       | both      | application ping every 15 s; missing inbound traffic for 45 s reconnects |

Rules implemented here:

- Browser Channel RPC carries **positional argument arrays** (`[arg]`); the server forwards
  them verbatim, so the link unwraps a single-element array. `{"$undef":true}` maps to
  `undefined`; an `undefined` return omits `value`.
- Unknown methods fail with `Unknown method <channel>.<method>`.
- Unknown events are accepted and never emit.
- Reconnect uses exponential backoff (1 s → 10 s). On disconnect all listens are dropped;
  the UI resubscribes in snapshot mode, so the runner keeps no server-side state.
- Initial/recovery frames are emitted on `setImmediate` **after** the subscribe ACK is
  written. Bytes-on-the-wire ordering matters here: the UI stages frames until the ACK is
  applied, but an initial frame must not be lost if it races the ACK.

## Module map

```
src/
  main.ts                 CLI entry: env/flags, wiring, signals
  config.ts               env parsing, runner id persistence, workspace validation
  logger.ts               leveled stderr logger
  link/
    frames.ts             link frame types, decodeArg/decodeCallArg, frame parsing
    registry.ts           (channel, method) / (channel, event) registry
    client.ts             WebSocket client, dispatch, reconnect, ping/pong
  engine/
    types.ts              Engine interface (the only surface the rest of the runner uses)
    checkpoints/          shadow-git checkpoints (spec B): git spawn boundary, turn trees,
                          per-workspace service, changed files / preview / journaled apply
    permissions/
      policy.ts           tool → allow/ask/deny table + plan-mode gate
      bashClassifier.ts   conservative read-only bash classifier
      gate.ts             PermissionGate: the single writer of pending approvals
    pi/
      piEngine.ts         pi implementation (AgentSession, SessionManager, ModelRuntime)
      piSessionFactory.ts session + inline nex-permission-gate extension bootstrap
      piCheckpointHook.ts turn/checkpoint bookkeeping on pi's tool_call/tool_result hooks
      piFileOps.ts        per-workspace checkpoint services + git-backed reads
      piFileFactsOps.ts   nex.checkpoints/nex.rewind → changed files / preview / apply
      piSessionFiles.ts   session dir/file lookup
      piSessionOps.ts     metadata transitions, prompt start, model selection
      permissionExtension.ts  pi `tool_call` hook → gate.authorize
      events.ts           pi AgentSessionEvent → EngineEvent (drops cumulative `partial`)
      translate.ts        transcript → EngineMessage; pi model → EngineModelInfo
      metaStore.ts        per-workspace runner metadata (title/phase/mode/tombstones)
      sessionHelpers.ts   model/thinking/usage projections
      modelCatalog.ts     available models + default selection
  projection/
    state.ts              ProjectionState + row types (pure data)
    fold.ts               EngineEvent → ProjectionState (assistant/reasoning/turn)
    toolEvents.ts         EngineEvent → ProjectionState (tool rows)
    approvalEvents.ts     approval_request/settled → pendingApproval rows + cards
    toolMapping.ts        pi tool name/args → UI tool name/args
    restore.ts            pi branch → ProjectionState (restart recovery, incl. compact markers)
    entityIds.ts          pi entry id → row entityId (`pi:<entryId>`) derivation
    messageIdentity.ts    `message_entry` → row entityId attachment (live path)
    rowActions.ts         edit/retry/fork + canRewindFiles affordances (old CLI materializeCommandRowActions)
    turnCheckpointProjection.ts  turnHeader.fileChanges + restored nex.checkpoints/nex.rewind
    availability.ts       snapshot.availability + inputRouting table
    snapshot.ts           ProjectionState → v4 ConversationSnapshot
  v4/
    revisionCas.ts        baseRevision/baseLogEpoch CAS gate (stale ACKs)
    publisher.ts          facade: conversation + sessions-index + commands
    sessionStore.ts       per-session projection owner, throttled snapshot frames
    indexPublisher.ts     sessions-index snapshots
    commands.ts           command admission + idempotency + ACK shapes
    fileRewindMethods.ts  conversationFileChangesV4 / conversationFileRewindPreviewV4
    fileRewindCommands.ts applyFileRewind (journaled restore, nex.rewind marker)
    envelopes.ts          wire envelope builders (complete frames only)
    sessionsIndex.ts      SessionSummary projection (incl. pendingInteraction)
    throttle.ts           leading+trailing 10 fps throttle
  channels/
    register.ts           channel/method wiring
    workspaceGuard.ts     live served-folder guard + machine-scope gate
    runnerWorkspaces.ts   runner-workspaces.list/add/remove
    workspaceTools.ts     file / file-watcher / media-preview / git channels
    terminalChannel.ts    terminal + system channels (pty-host)
    nexTask.ts            sidebar task rows + workspace events
    nexSession.ts         readWorkspacePresentation
    modelViews.ts         model-selection / provider-settings views
  workspaces/
    workspaceRoots.ts     served folders (env + persisted), the single owner
    machineScope.ts       `workspacePath: "~"` browse-only rules
    workspaceFrames.ts    `{t:"workspaces"}` push on change
```

## Engine interface

`src/engine/types.ts` is the boundary. Projection and channels never import pi; only
`engine/pi/` does. A pi-durable implementation can replace `PiEngine` without touching
the publisher:

```ts
interface Engine {
  init(): Promise<void>;
  createSession(workspacePath, { modelSelection? }): Promise<{ sessionId }>;
  openSession(workspacePath, sessionId): Promise<void>;   // lazy restore from disk
  deleteSession(workspacePath, sessionId): Promise<void>;
  listSessions(workspacePath): Promise<EngineSessionState[]>;
  listDeletedSessionIds(workspacePath): Promise<string[]>;
  state(sessionId): EngineSessionState | null;
  history(sessionId): Promise<EngineMessage[]>;
  subscribe(sessionId, listener): { dispose(): void };
  submit(sessionId, text, { requestId, modelSelection?, mode?, planEnabled? }): Promise<{ status, inputId }>;
  resolveApproval(sessionId, interactionId, { optionId?, freeText? }): Promise<{ resolved, outcome? }>;
  permissionMode(sessionId): { mode, planEnabled } | null;
  abort(sessionId): Promise<void>;
  rename(sessionId, title): Promise<void>;
  setModel(sessionId, selection): Promise<void>;
  catalog(): Promise<EngineCatalog>;
  onSessionsChanged(listener): () => void;
  dispose(): Promise<void>;
}
```

`submit` is idempotent on `requestId` (the v4 `commandId`): a repeated id returns
`duplicate` without re-running the prompt. `submit` never awaits the model run; the
run's progress arrives through `subscribe`. The submission's `mode`/`planEnabled` are
applied to the session's permission gate before the prompt starts.

## State ownership

| State                                             | Owner                                                              |
| ------------------------------------------------- | ------------------------------------------------------------------ |
| pi session files + transcript                     | pi `SessionManager` under `<data>/sessions/<sha1(workspacePath)>/` |
| served folders (env + persisted)                  | `WorkspaceRoots` → `<data>/workspaces.json` (atomic write)         |
| title/phase/mode/deleted tombstones/seen commands | `SessionMetaStore` (runner is the single writer)                   |
| pending approvals + "allow always" set            | `PermissionGate` per open session (engine-owned, in memory)        |
| `ConversationSnapshot`                            | derived per session by `SessionRuntimeStore`                       |
| sessions-index list                               | derived on demand from the engine + projection store               |
| command idempotency                               | `CommandHandler` (session bucket + per-workspace create bucket)    |

Rows are numbered monotonically within a session's `logEpoch`. A runner restart starts a
new `logEpoch`; the UI resyncs on epoch change. Every published frame is a full snapshot
(`fromSeq: 0`, `toSeq: seq`, `snapshot.seq === toSeq`) throttled to ≤ 10 fps per session
with a trailing flush.

### Row identity, actions and revision

- Every row carries `entityId` = `pi:<pi entry id>` (`pi:<entry>:text:<n>` for the nth text
  part, `pi:<entry>:thinking:<n>` for thinking, `pi:tool:<toolCallId>` for tool calls,
  `pi:turn:<entry>` for the turnHeader). Live rows learn it from the `message_entry` engine
  event emitted right after pi persists the message; restore derives the same value from
  `SessionManager.getBranch()`. rowIds are per-epoch, entityIds are not — that is what makes
  an action target survive a restart.
- Every row also carries `productTurnId = turnId` and `visibility:"visible"`.
- `actions` is derived per snapshot (never stored): `canEdit`+`editDisposition:"rewind"` on
  the newest user row, `canRetry` on the newest complete assistant with a user cause,
  `canFork` on a turn's last complete text segment when that turn ended `completedSuccess`.
- `seq` advances on every published change; `revision` follows the old CLI's closed rule
  (row add/update/remove and meta/config/queue/interaction changes, **not** append-only text
  growth). Commands in `COMMANDS_REQUIRING_BASE_REVISION` are answered `stale` with the real
  `revisionAtDecision` when `baseRevision`/`baseLogEpoch` does not match.

### History is the raw pi branch

`Engine.history()` reads `sessionManager.getBranch()`, not `session.messages`: the latter is
the projected model context and drops summarized messages after compaction. Restore projects
`message` entries 1:1, a `compaction` entry into a `timelineMarker` compact row (lane
`assistantWork`, status `success`, origin `manual`/`auto`), and skips `branch_summary`
entries (old CLI parity).

## Server-owned provider config (P0)

Spec: `docs/next/config-mcp-skills.md`. The `config` frame carries the desired
providers/models of one version; the runner builds an immutable RuntimeConfig
per version:

- **Fallback mode** (`doc.owned:false`, no enabled provider): today's behavior —
  every new session reads the agent dir's `models.json`/`auth.json` and
  `SettingsManager.create(cwd)`.
- **Server mode:** a derived `<data>/derived/<version>/models.json` plus an
  in-memory credential store (`setRuntimeApiKey`; `auth.json` is never written),
  `SettingsManager.inMemory` seeded from the doc's settings layer, cwd = the
  workspace. New sessions use the latest generation; running sessions keep the
  runtime and settings manager they were created with.
- A rejected frame (`ok:false`, e.g. unknown schema) keeps the previous
  generation. Per-provider problems (missing secret, a model pi rejected) are
  reported as `errors[]` with `ok:true` and surfaced as `issues` in Settings.
- `<data>/applied-config.json` caches the applied version/mode (never secrets).
- `config.exportLocal` returns the runner's local models.json providers, its
  `auth.json` api keys (values only over the link, stored as server secrets by
  `POST /api/config/import-runner`), settings defaults and raw `mcp.json`.

## Permissions ("Ask before changes")

Spec: [`docs/next/permissions.md`](../../docs/next/permissions.md).

One `PermissionGate` per open session is the single writer of pending state. An inline pi
extension (`DefaultResourceLoader({extensionFactories})`, reloaded before
`createAgentSession({resourceLoader})`) registers a `tool_call` handler that awaits
`gate.authorize(...)`. pi emits `tool_execution_start` before the hook, so the tool row
already exists when the card appears.

Event order for one gated call:

```
pi tool_execution_start ──▶ projection row "running"
pi tool_call hook ────────▶ gate.authorize
                              ├─ allow  → undefined (tool executes)
                              ├─ ask    → emit approval_request → row "pendingApproval",
                              │           snapshot.pendingInteractions[] with anchorRowId,
                              │           sessions-index pendingInteraction (+ summary)
                              └─ deny   → { block:true, reason }
resolveInteraction ───────▶ gate.resolve → emit approval_settled
                              ├─ allow  → hook returns, row "running"
                              └─ deny/aborted → hook returns blocked, row "cancelled"
```

Policy (`engine/permissions/policy.ts`):

| pi tool                      | build | edit  | yolo  | planEnabled |
| ---------------------------- | ----- | ----- | ----- | ----------- |
| `read`, `grep`, `find`, `ls` | allow | allow | allow | allow       |
| `edit`, `write`              | ask   | allow | allow | deny        |
| `bash` classified read-only  | allow | allow | allow | allow       |
| `bash` other                 | ask   | ask   | allow | deny        |
| unknown / extension tools    | ask   | ask   | allow | deny        |

Nested calls (`parentToolCallId`) go through the same hook. "Always allow" is per tool
name for that session, in memory only.

Failure behavior (fail closed):

- No client connected: the hook keeps waiting (no timeout); nothing runs unapproved.
- Stop/abort or `deleteSession`: `gate.abortPending()` settles live cards as `aborted`, so
  the hook returns and the run can settle; the row ends `cancelled`.
- Runner restart: pending approvals are not persisted. A restore marks every tool call
  without a tool result as `cancelled` (never `success`).
- A blocked call's error tool result does not overwrite `cancelled`.
- Plan mode denies with a reason and never shows a card.

Modes arrive per submission (`createSession.firstInput.mode`, `createSession.config.mode`,
`sendText.mode`, `planEnabled`). `snapshot.config.mode` / `.planEnabled` echo the session's
current values for the composer; `config.permissionGrant` is never set (the UI would flip
the composer to yolo).

## Tool name mapping

The UI chooses card renderers by exact tool name (`packages/shared/src/tool-identity.ts`).

| pi tool               | UI tool                     | Input mapping                                  |
| --------------------- | --------------------------- | ---------------------------------------------- |
| `bash` / `powershell` | `Bash`                      | `{command}`                                    |
| `read`                | `Read`                      | `{file_path: path, offset?, limit?}`           |
| `write`               | `Write`                     | `{file_path: path, content}`                   |
| `edit` (1 edit)       | `Edit`                      | `{file_path: path, old_string, new_string}`    |
| `edit` (N edits)      | `Edit`                      | `{file_path: path, edits:[{oldText,newText}]}` |
| `grep`                | `Grep`                      | `{pattern, path?, glob?}`                      |
| `find`                | `Glob`                      | `{pattern, path?}`                             |
| `ls`                  | `ls` (not in the known set) | `{path?}`, falls back to the raw card          |
| anything else         | unchanged                   | unchanged                                      |

`edit` with multiple replacements keeps all of them in `edits`; the UI's single-file diff
heuristics understand `oldText`/`newText` pairs.

## Channels

| Channel             | Methods                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `nex-agent`         | `subscribeConversationV4`, `resyncConversationV4`, `unsubscribeConversationV4`, `conversationRowsRangeV4`, `conversationPlansV4`, `conversationWorkflowRunsV4` (always `{runs:[]}`), `conversationFileChangesV4`, `conversationFileRewindPreviewV4`, `sendConversationCommandV4`, `queryConversationCommandsV4`, `onDynamicConversationFrame`, sessions-index subscribe/resync/unsubscribe, `onDynamicSessionsIndexFrame` |
| `nex-task`          | `listTasks`, `listPinnedTasks`, `listArchivedTasks`, `listDeletedTaskIds`, `listPinnedTaskIds`, `getTaskSessionFilePath`, `getTaskNativeSessionLogFile`, `onDynamicWorkspaceEvent`                                                                                                                                                                                                                                        |
| `nex-session`       | `readWorkspacePresentation`                                                                                                                                                                                                                                                                                                                                                                                               |
| `model-selection`   | `getView`, `onDidChange` (never fires; catalog is static per process). Served for old servers; the nex-next server answers it itself once configured (fallback forwards here)                                                                                                                                                                                                                                             |
| `provider-settings` | `getView`, `onDidChange` (never fires); same fallback role as `model-selection`                                                                                                                                                                                                                                                                                                                                           |
| `config`            | `apply`, `status`, `exportLocal`                                                                                                                                                                                                                                                                                                                                                                                          |

Commands: `createSession` (with or without `firstInput`; draft prewarm works),
`sendText`, `deleteSession`, `renameSession`, `stop`, `resolveInteraction`,
`switchModelConfig`, `setFollowupMode`, `compact`, `editUserQuery`, `retryTurn`,
`forkAssistant`, `deleteQueueItem`, `editQueueItem`, `reorderQueueItem`, `sendQueuedNow`,
`setAutoDrain`, `applyFileRewind`. Unknown commands return `rejected` with
`reasonCode:"unsupported_command"`.

### File changes and rewind (batch 3, spec B)

Spec: [`docs/next/attachments-rewind.md`](../../docs/next/attachments-rewind.md).

- **Checkpoints.** One shadow git repository per workspace
  (`<data>/checkpoints/<sha256(realpath ws)[0:16]>.git`, `--work-tree=<ws>`), independent of
  the workspace's own repository. On the first mutating tool call (`edit`/`write`) of a
  turn: side-index `add -A` (honours `.gitignore`) → `write-tree` → `commit-tree`, with the
  previous turn's tree as the base; every mutating result then records `hash-object -w`.
  `bash`/`powershell` are not checkpointed (their files show as `bash_ignored`).
- **Cards and headers.** `edit` rows get `output.display = {kind:"file_diff"}` from pi's
  `details.patch`; `write` rows get a synthesized whole-file patch. The turn header carries
  `fileChanges {files,additions,deletions,state}` and `actions.canRewindFiles` (turn not
  running, no pending interaction, no compaction, `state:"active"`).
- **Reads/commands.** `conversationFileChangesV4` / `conversationFileRewindPreviewV4` are
  read-only lookups; `applyFileRewind {target}` restores the turn-start files (deleting files
  the turn created) through a reverse journal and appends `nex.rewind` (header `reverted`).
  `editUserQuery {workspaceMode:"rewind"}` previews, restores the files, cuts the branch and
  re-prompts; an unsafe/empty set answers `blocked` with the preview.
- **Persistence.** `nex.checkpoints` / `nex.rewind` custom entries in the pi session file, so
  a restart rebuilds the same diffs, summaries and `reverted` state.
- **No git on the host** → checkpoints are disabled (one `warn` log), no turn carries
  `fileChanges`, and Undo is hidden.

### Conversation features (batch 2d)

Spec: [`docs/next/conversation-features.md`](../../docs/next/conversation-features.md)
(decisions 5-7, "Batch 2d — implemented").

- **compact.** `compact` (no payload, no CAS) admits through the queue: a running/queued
  compact → `rejected compactOperationLock`; busy → a `kind:"compact"` queue item (ACK
  `accepted`) drained in FIFO order; idle → started now. `v4/compactOwner.ts` writes the
  `timelineMarker` compact row (`running` → `success|noop|cancelled|failed`, origin
  `manual|auto`, `tokensBefore`/`tokensAfter`, `sourceCommandId`) and owns
  `engine.compact()`. pi's automatic compaction (`compaction_start/end` with reason
  `threshold|overflow`) gets the same `origin:"auto"` marker through the fold. Stop aborts
  a compaction (`canStop` while only compaction runs); a failed/cancelled manual compact
  pauses a non-empty queue. `activeWorks` includes `kind:"compact"`.
- **edit / retry (rewind).** `editUserQuery {target,newText,workspaceMode?}` rewinds the
  newest real-user turn: cut rows from its turn header (rowIds stay monotonic), branch pi
  to the parent of that user entry, `refreshContext()`, prompt the new text in the same
  session; ACK `{type:"editUserQuery", disposition:"rewind", sessionId}`. `workspaceMode
:"rewind"` → `blocked` + `guard.workspaceRewindUnavailable` + an empty preview.
  `retryTurn {target}` reuses the primitive with the original user text (guarded to the
  newest complete assistant). The rewritten turn carries the original command lineage.
- **fork.** `forkAssistant {target}` copies the conversation up to a stable completed
  reply into a new session file (second `SessionManager`, `createBranchedSession`), writes
  the `nex.fork` custom entry, titles it `Fork of <parent>` (`generated`), registers it in
  metadata/sessions index, and ACKs `{type:"forkAssistant", sessionId}`. The child's
  restore shows a `forkNotice` marker; the parent is never modified.

`switchModelConfig {provider, model, thought}` applies pi's `setModel` + clamped
`setThinkingLevel`, persists the selection, and appends a `timelineMarker`
(`modelChange`, lane `lightBoundary`) when the model identity changes. Identical values
return `noop` (`config.unchanged`); an unknown provider/model returns `failed`
(`provider.notInRegistry`). `setFollowupMode {mode}` stores `queue`/`guide` in metadata and
the snapshot's `config.followupMode`; pi's own follow-up drain granularity is untouched.

`resolveInteraction {interactionId, answer:{optionId, freeText?}}` forwards the answer to
the session's `PermissionGate`. ACK: `accepted` with
`result:{type:"resolveInteraction", resolvedBy:{clientId, optionId}}`; unknown or
already-resolved ids return `noop` (`reasonCode:"already_resolved"`) with the same result
shape. A repeated `commandId` replays the stored ACK as `duplicate`.

`nex-task` rows and workspace events echo the `workspaceIdentity` the server forwarded
(`runner:<runnerId>:<path>`). The UI groups sidebar rows by
`workspaceIdentity?.trim() || workspacePath`, so omitting it leaves every row out of its
workspace group.

View contracts (details in `docs/next/README.md`):

- `model-selection.getView({selection})` returns `effectiveSelection` /
  `selectionIssue` for the caller's intent; the composer requires it to enable
  the model picker and submit.
- `ConversationSnapshot.config.modelSelection` echoes the session model so
  reopening a session initializes the composer.
- `phase:"draft"` sessions never appear in `sessions-index` or `nex-task` rows;
  the same session appears once it turns `running` after `sendText`.

## Tests

```sh
pnpm --filter @nex/next-runner typecheck
pnpm --filter @nex/next-runner lint
pnpm --filter @nex/next-runner test          # unit + in-process link tests
```

The permission suite runs with the default `pnpm test` (no env vars):

- `test/permissions-policy.test.ts` — policy table (build/edit/yolo/planEnabled, allow
  always) and the bash classifier, including composition/redirect/substitution negatives.
- `test/permissions-gate.test.ts` — fold transitions, resolve idempotency, abort while
  pending (no hang), allowAlways session scoping, concurrent approvals.
- `test/permissions-projection.test.ts` — every emitted snapshot / sessions-index frame /
  ACK parsed with the real zod schemas; restore of an unanswered call; mode echo.
- `test/permissions-pi-integration.test.ts` — the **real** pi engine and pi-ai's faux
  provider drive a `write` through the inline `tool_call` hook: allow writes the file,
  deny blocks it, edit mode skips the card, abort settles a pending card.
- `test/conversation-features.test.ts` — batch 2d through the command handler + projection
  over the engine double: compact lifecycle (success/noop/cancelled/failed, lock, queued
  drain, pause on failure), edit/retry guards and rewind, fork guards, availability, the
  auto marker fold and the forkNotice restore.
- `test/conversation-pi-integration.test.ts` — the **real** pi engine + faux provider:
  compact success then noop, small-session noop, Stop-cancelled compaction, queued compact
  draining after a run, edit + restore (old rows stay gone), retry, fork (child file,
  fork notice, parent bytes unchanged), and pi's own overflow compaction (`origin:"auto"`).
- `test/checkpoints-service.test.ts` — real `git` + a temp workspace: turn snapshot,
  `.gitignore`, created/edited files, external modification → unsafe, bash → ignored,
  journaled apply, failure compensation, restart, non-git workspace, git missing.
- `test/file-changes.test.ts` — file summaries/`canRewindFiles`, the two read queries,
  `applyFileRewind` (accepted/unsafe) and `editUserQuery{workspaceMode:"rewind"}` over the
  command handler (every ACK/query parsed with the real schemas).
- `test/file-changes-pi-integration.test.ts` — real pi + faux: write/edit/bash turn shows
  diffs and fileChanges, Undo restores/deletes files and the header becomes `reverted`, an
  external change blocks, and edit-with-rewind restores the files and re-runs the turn.

Opt-in real-model smoke test (uses `~/.pi/agent` credentials):

```sh
RUN_PI_SMOKE=1 pnpm --filter @nex/next-runner test
```

Opt-in full-stack test against a running `next/server` (browser Channel RPC over `/ws`):

```sh
RUN_NEXT_E2E=1 \
NEX_E2E_WS_URL=ws://127.0.0.1:3030/ws \
NEX_E2E_COOKIE="nex_session=…" \
NEX_E2E_WORKSPACE=/path/on/runner \
pnpm --filter @nex/next-runner test
```

Browser E2Es (see `next/dev.sh --help`):

```sh
next/dev.sh --e2e               # acceptance (read + ls -la, no cards)
next/dev.sh --e2e=permissions   # approval card: allow / deny / edit mode / read-only ls
next/dev.sh --e2e=files         # write/edit diff cards, changed-files panel, Undo, edit with rewind
next/dev.sh --e2e=attachments   # image/file attachment, restore
next/dev.sh --e2e=all           # every suite, acceptance first
```

The unit tests import the real zod schemas from `@nex/shared/nex-protocol-v4` and validate
every emitted object: `conversationSnapshotSchema`, `conversationTopicFrameSchema`,
`conversationTopicWireFrameSchema`, `sessionsIndexSnapshotSchema`,
`sessionsIndexTopicWireFrameSchema`, `commandAckSchema`, plus `nexWorkspacePresentationSchema`
and `modelSelectionSchema` from `@nex/shared`. A fake engine drives the projection and
publisher tests; an in-process `ws` server drives the link test.

## Known gaps (MVP)

- **Full snapshots only.** No deltas; a change republishes the whole snapshot (rows capped
  at a 200-row tail window).
- **`conversationRowsRangeV4` returns an empty page.** `rows.firstRowId` is the window
  head, so the UI never asks for older rows; very long sessions show only the tail window.
- **`conversationPlansV4` is always empty**; pi has no ExitPlanMode catalog.
- **Sessions index is rebuilt from the runner metadata file**, not pi's own session list,
  so sessions created by running `pi` directly in the same workspace are not listed.
- **Attachment rows after a runner restart** re-render text-only until the persisted
  `nex.attachments` entry restore lands (tracked with batch 3 A).
- **File rewind limits.** Only `edit`/`write` are checkpointed; shell-modified files are
  reported as ignored. Above 100k tracked files a turn falls back to per-file states
  (`partial`), and rewind then answers `checkpoint_missing`.
- **Auto-compaction is marker-complete, not manual.** pi's threshold/overflow compaction
  shows an `origin:"auto"` marker, but the runner cannot queue it (pi decides). A manual
  `compact` runs only from the runner queue.
- **`setAssistantFeedback` / goal commands are not served.** Feedback is a no-op on the
  runner; `pauseGoal`/`resumeGoal` stay denied (`noGoalToPause`/`noGoalToResume`).
- **Permissions are session-scoped and in memory.** Pending approvals do not survive a
  runner restart, and "Always allow in this session" is a per-gate set. Plan mode is a hard
  deny, not a planning UI.
- **Titles are model-generated on the first prompt** (decision 8); `NEX_RUNNER_TITLES=off`
  disables them and a rename (`custom`) is never overwritten.
- **`thinkingLevel` mapping uses pi's own capability set**; levels beyond
  `off|minimal|low|medium|high` are exposed only when pi reports them.
- **Workspace path uniqueness is the server's job.** The runner just announces its list.

## Spec ambiguities resolved

1. **`deliveryKind` on the initial frame after a draft prewarm.** `createSession` with no
   `firstInput` creates a pi session with `phase:"draft"` and no rows. Subscribers get an
   `initial` snapshot with `phase:"draft"`, then `online` frames once `sendText` starts
   the run.
2. **`seq` per session vs. per frame.** `seq` increments on every projection change and
   is shared by all subscriptions of that session; every frame still carries the full
   snapshot, so a late subscriber's `initial` frame may have a higher `toSeq` than an
   earlier subscriber's `online` frame. This is legal for snapshot-mode subscriptions.
3. **`nex-task.listPinnedTaskIds`** has no workspace argument; it returns `[]` (the server
   routes it to the first connected runner).
4. **`edit` with multiple replacements.** The UI's `Edit` renderer has one card per file;
   the runner sends the first edit as `old_string`/`new_string` plus an `edits` array so a
   future multi-hunk renderer can use it. Today the file-diff heuristics read
   `oldText`/`newText` from `edits[]` too.
5. **`ls` has no canonical UI tool.** It stays `"ls"` and renders through the fallback
   card instead of being mislabeled `Glob`/`Grep`.
