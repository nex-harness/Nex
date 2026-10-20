# @nex/next-runner

Agent runner for the `next/` server. It dials the server's `/runner` WebSocket, announces
its workspaces, and serves pi-backed sessions to the unchanged ZCode web UI.

Spec of record: [`docs/next/README.md`](../../docs/next/README.md) and
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

| Variable                | Required    | Default                      | Meaning                                                          |
| ----------------------- | ----------- | ---------------------------- | ---------------------------------------------------------------- |
| `NEX_SERVER_URL`        | no          | `ws://127.0.0.1:3030/runner` | ws/wss URL; `/runner` is appended when missing                   |
| `NEX_RUNNER_TOKEN`      | recommended | —                            | Bearer token for the upgrade request (`Authorization: Bearer …`) |
| `NEX_RUNNER_ID`         | no          | persisted random id          | Stable identity; persisted in `<data>/runner-id.json`            |
| `NEX_RUNNER_NAME`       | no          | hostname                     | Display name in the server registry                              |
| `NEX_RUNNER_WORKSPACES` | **yes**     | —                            | Absolute paths, `,`- or path-delimiter-separated (at least one)  |
| `NEX_RUNNER_DATA_DIR`   | no          | `~/.nex-next/runner`         | Sessions + per-workspace metadata                                |
| `NEX_RUNNER_LOG_LEVEL`  | no          | `info`                       | `debug` / `info` / `warn` / `error`                              |
| `NEX_RUNNER_VERSION`    | no          | package version              | Reported in `hello`                                              |

Logs go to stderr. Tokens and API keys are never logged; the model catalog view exposes
an empty `apiKey` placeholder only.

## Link protocol (server ⇄ runner)

One text-frame JSON WebSocket. The runner sends `hello` first, then handles server
frames:

| Frame                 | Direction | Handling                                                               |
| --------------------- | --------- | ---------------------------------------------------------------------- |
| `hello` / `welcome`   | R→S / S→R | handshake                                                              |
| `call`                | S→R       | `(channel, method) → async handler(arg)`; replies `result{ok, value?}` |
| `listen` / `unlisten` | S→R       | `(channel, event) → (arg, emit) → dispose`                             |
| `event`               | R→S       | one emit for the matching listen id                                    |
| `ping` / `pong`       | both      | 15 s interval; missing inbound traffic for 45 s reconnects             |

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
    pi/
      piEngine.ts         pi implementation (AgentSession, SessionManager, ModelRuntime)
      events.ts           pi AgentSessionEvent → EngineEvent (drops cumulative `partial`)
      translate.ts        transcript → EngineMessage; pi model → EngineModelInfo
      metaStore.ts        per-workspace runner metadata (title/phase/tombstones)
      sessionHelpers.ts   model/thinking/usage projections
      modelCatalog.ts     available models + default selection
  projection/
    state.ts              ProjectionState + row types (pure data)
    fold.ts               EngineEvent → ProjectionState (assistant/reasoning/turn)
    toolEvents.ts         EngineEvent → ProjectionState (tool rows)
    toolMapping.ts        pi tool name/args → UI tool name/args
    restore.ts            transcript → ProjectionState (restart recovery)
    snapshot.ts           ProjectionState → v4 ConversationSnapshot
  v4/
    publisher.ts          facade: conversation + sessions-index + commands
    sessionStore.ts       per-session projection owner, throttled snapshot frames
    indexPublisher.ts     sessions-index snapshots
    commands.ts           command admission + idempotency + ACK shapes
    envelopes.ts          wire envelope builders (complete frames only)
    sessionsIndex.ts      SessionSummary projection
    throttle.ts           leading+trailing 10 fps throttle
  channels/
    register.ts           channel/method wiring
    nexTask.ts            sidebar task rows + workspace events
    nexSession.ts         readWorkspacePresentation
    modelViews.ts         model-selection / provider-settings views
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
  submit(sessionId, text, { requestId, modelSelection? }): Promise<{ status, inputId }>;
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
run's progress arrives through `subscribe`.

## State ownership

| State                                        | Owner                                                              |
| -------------------------------------------- | ------------------------------------------------------------------ |
| pi session files + transcript                | pi `SessionManager` under `<data>/sessions/<sha1(workspacePath)>/` |
| title/phase/deleted tombstones/seen commands | `SessionMetaStore` (runner is the single writer)                   |
| `ConversationSnapshot`                       | derived per session by `SessionRuntimeStore`                       |
| sessions-index list                          | derived on demand from the engine                                  |
| command idempotency                          | `CommandHandler` (session bucket + per-workspace create bucket)    |

Rows are numbered monotonically within a session's `logEpoch`. A runner restart starts a
new `logEpoch`; the UI resyncs on epoch change. Every published frame is a full snapshot
(`fromSeq: 0`, `toSeq: seq`, `snapshot.seq === toSeq`) throttled to ≤ 10 fps per session
with a trailing flush.

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

| Channel             | Methods                                                                                                                                                                                                                                                                                                                                                   |
| ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `nex-agent`         | `subscribeConversationV4`, `resyncConversationV4`, `unsubscribeConversationV4`, `conversationRowsRangeV4`, `conversationPlansV4`, `conversationWorkflowRunsV4` (always `{runs:[]}`), `sendConversationCommandV4`, `queryConversationCommandsV4`, `onDynamicConversationFrame`, sessions-index subscribe/resync/unsubscribe, `onDynamicSessionsIndexFrame` |
| `nex-task`          | `listTasks`, `listPinnedTasks`, `listArchivedTasks`, `listDeletedTaskIds`, `listPinnedTaskIds`, `getTaskSessionFilePath`, `getTaskNativeSessionLogFile`, `onDynamicWorkspaceEvent`                                                                                                                                                                        |
| `nex-session`       | `readWorkspacePresentation`                                                                                                                                                                                                                                                                                                                               |
| `model-selection`   | `getView`, `onDidChange` (never fires; catalog is static per process)                                                                                                                                                                                                                                                                                     |
| `provider-settings` | `getView`, `onDidChange` (never fires)                                                                                                                                                                                                                                                                                                                    |

Commands: `createSession` (with or without `firstInput`; draft prewarm works),
`sendText`, `deleteSession`, `renameSession`, `stop`, `resolveInteraction` (accepted
no-op). Unknown commands return `rejected` with `reasonCode:"unsupported_command"`.

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

The unit tests import the real zod schemas from `@nex/shared/nex-protocol-v4` and validate
every emitted object: `conversationSnapshotSchema`, `conversationTopicFrameSchema`,
`conversationTopicWireFrameSchema`, `sessionsIndexSnapshotSchema`,
`sessionsIndexTopicWireFrameSchema`, `commandAckSchema`, plus `nexWorkspacePresentationSchema`
and `modelSelectionSchema` from `@nex/shared`. A fake engine drives the projection and
publisher tests; an in-process `ws` server drives the link test.

## Known gaps (MVP)

- **Full snapshots only.** No deltas; large sessions resend up to the tail window on every
  flush (`snapshotTailWindowRows`-sized cap of 200 rows).
- **`conversationRowsRangeV4` returns an empty page.** Since `rows.firstRowId` is the
  window head, the UI never asks for older rows; very long sessions will show only the
  tail window.
- **`conversationPlansV4` is always empty**; pi has no ExitPlanMode catalog.
- **Sessions index is rebuilt from the runner metadata file**, not from pi's own session
  list, so sessions created by running `pi` directly in the same workspace are not listed.
- **No attachments** (`attachment*` / `conversationFileChangesV4` etc. are not
  implemented); the composer's happy path does not need them.
- **No permission/user-input interactions.** pi runs with its own tool policy; no
  `permissionRequest` rows are projected.
- **Titles are not model-generated.** Default is the first user message truncated to 60
  chars (`titleSource:"default"`); rename sets `"custom"`.
- **`thinkingLevel` mapping uses pi's own capability set**; levels beyond
  `off|minimal|low|medium|high` (`xhigh`, `max`) are exposed only when pi reports them.
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
