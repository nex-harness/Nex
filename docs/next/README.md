# Nex Next — MVP spec

Status: MVP, branch `feature/next`. This directory is the spec of record for the
`next/` Go server and the `packages/next-runner` agent runner. Update it before
changing behavior.

## Goal

Run one Go server on Linux, open its web UI (the existing ZCode web client,
unchanged), and drive pi-based agents on any number of machines (macOS, Linux,
Windows), each running an agent-runner that dials into the server.

MVP acceptance: log in → pick a workspace on any connected runner → new chat →
send a prompt → see streamed assistant text and tool cards (bash/read/edit/write)
→ the session shows in the sidebar → reload the page and reopen it.

Out of scope for MVP: terminals, file browser, git panel, SSH install, config
push from server, permissions UI, sub-agents, port forwarding, desktop app.

## Topology and ownership

```
Browser (packages/web + packages/ui, unchanged)
  │  existing binary channel-RPC over WS /ws  (packages/rpc wire format)
  ▼
Go server (next/server)
  │  owns: auth, runner registry, workspace catalog, UI settings,
  │        channel routing, event fan-out
  │  JSON over WS /runner  (this spec, "runner link")
  ▼
agent-runner (packages/next-runner, Node 24 + pi SDK) × N machines
     owns: pi sessions + transcripts, v4 conversation projection,
           sessions index, model catalog, tool execution
```

| State                                               | Owner                                   | Notes                                                       |
| --------------------------------------------------- | --------------------------------------- | ----------------------------------------------------------- |
| Transcript, session list, conversation seq/logEpoch | runner                                  | pi session files under the runner data dir                  |
| Sidebar task state (pin/archive/unread/rename/delete) | runner (metadata store)               | single writer; see `task-state.md`                          |
| Model catalog, API keys                             | runner (MVP)                            | read from runner env / pi `auth.json`; server push is later |
| Which runners/workspaces exist                      | server (in memory, from runner `hello`) | runner is the source; server forgets on disconnect          |
| UI settings (`setting` channel)                     | server (JSON file)                      |                                                             |
| Drafts, view state                                  | browser                                 |                                                             |

The server never interprets conversation payloads. It routes by workspace and
forwards channel-RPC calls/events as JSON.

## Workspace identity

Current rule: `docs/next/multi-runner.md` (supersedes the earlier
`workspacePath`-only MVP rule).

```
workspaceIdentity = "runner:<runnerId>:<runner-local absolute path>"
workspacePath     = runner-local absolute path (cwd, file ops, display)
```

- Built by the server from runner `hello` (`next/server/internal/runners/identity.go`),
  never parsed by runners.
- Forwarded `nex-agent` / `nex-task` / `nex-session` calls and their listens
  route by identity first, then by a uniquely served `workspacePath`. An
  offline/unknown runner fails with `runner offline`; several owners without an
  identity fail with `ambiguous workspace path; workspaceIdentity required`.
- Duplicate paths across runners are legal. `server-info.workspaces[]` carries
  `{path, label, workspaceIdentity, runnerId, runnerName, runnerOs}`;
  `setting.recentProjects` stays path-only and deduplicated.
- Server routing details: `next/server/README.md`.

## Runner link protocol (server ⇄ runner)

Transport: one WebSocket, `GET /runner`, text frames, one JSON object per frame.
Auth: `Authorization: Bearer <NEX_RUNNER_TOKEN>` on the upgrade request. The
`hello`/`welcome` handshake must complete within 10 s; frames are capped at
32 MiB.

Envelope:

```jsonc
{ "t": "<type>", "id": "<string, request id where applicable>", ... }
```

| t             | Direction | Fields                                                         | Meaning                                                                    |
| ------------- | --------- | -------------------------------------------------------------- | -------------------------------------------------------------------------- |
| `hello`       | R→S       | `runnerId, name, version, os, arch, workspaces:[{path,label}]` | First frame. Server replies `welcome` or closes.                           |
| `welcome`     | S→R       | `serverVersion`                                                |                                                                            |
| `call`        | S→R       | `id, channel, method, arg`                                     | Forwarded UI channel call. `arg` is the JSON bridge form of the RPC value. |
| `result`      | R→S       | `id, ok, value?, error?:{message,name?}`                       | Reply to `call`.                                                           |
| `listen`      | S→R       | `id, channel, event, arg`                                      | Start a dynamic event stream (`onDynamicXxx(arg)`).                        |
| `unlisten`    | S→R       | `id`                                                           | Stop it.                                                                   |
| `event`       | R→S       | `id, value`                                                    | One event fire for listen `id`.                                            |
| `ping`/`pong` | both      |                                                                | 15 s interval, 45 s timeout.                                               |

Rules:

- `arg` is the browser's positional argument array, forwarded verbatim
  (`ProxyChannel` sends `[arg0, ...]`); runners unwrap it. Undefined values are
  `{"$undef":true}`, buffers `{"$buf":"<base64>","$vs":bool}` (see
  `next/server/internal/rpcwire/README.md`).
- Workspace-less forwarded calls (`provider-settings.*` operations without a
  workspace target, `model-selection` operations other than a targeted
  `getView`, `nex-task.listPinnedTaskIds`) go to the first connected runner.
- A forwarded call whose argument carries a `runner:<runnerId>:<path>`
  `workspaceIdentity` goes to that runner; an offline/unknown runner returns
  `runner offline` and never falls back to another machine.
- Ids are server-generated strings, unique per runner connection.
- On disconnect, the server fails pending calls with `runner offline` and drops
  listens; the browser's v4 layer resubscribes (snapshot) when the runner returns.
- Workspace-less listens (`model-selection`/`provider-settings` `onDidChange`)
  move to the next connected runner immediately when their runner disconnects.
- `/ws` accepts only same-host `Origin` (plus `NEX_ALLOWED_ORIGINS`): it
  authenticates with a session cookie, which does not prevent cross-site
  WebSocket handshakes.
- Runner-side conversation ordering is owned by the v4 projection's `seq`; the
  link adds no ordering of its own beyond WebSocket FIFO.

## Browser-facing channels (server)

Contract details with citations: `channels-research.md`.

Implemented by the server itself:

- `setting` (full default AppSettings; `recentProjects` = routed workspaces),
  `broadcast`
- `window-controller`: `listTaskList` fans out to the runner owning each
  `workspaceScopes[]` entry (`nex-task.listPinnedTasks` / `listArchivedTasks` /
  `listTasks`), merges/sorts/searches/limits, and pushes snapshot controller
  frames. `deleteArchivedTask(s)` / `mutateTask` forward to the owning runner's
  `nex-task` mutations. Task-state semantics: `task-state.md`.
- `model-selection`, `provider-settings`: `getView` forwards to the runner named
  by the optional `workspaceIdentity` in its input, else to the first connected
  runner; `onDidChange` stays on the first connected runner (the model catalog
  lives on runners).
- Stubs that keep the UI quiet: `system`, `git`, `mcp-sync`,
  `coding-plan-subscription`, `settings-sync` (`getFirstRunPromptState →
{handled:true}`), `subagents`, `client-scenes`, `onboarding-record`
  (`getLatestEntry → null`; writes rejected — the server has no per-device
  record store), `bots` (`syncAppRuntimePreferences → null`; the MVP has no bot
  runtime) (shapes in `channels-research.md` §9).
- `nex-agent` methods with no workspace in the arg (`helloConversationV4`,
  `initializeConversationV4`, `syncAppRuntimePreferences`, …) are answered
  locally from a fixed table.

Forwarded to the owning runner, by `workspaceIdentity` first and
`workspacePath` as the legacy fallback (see "Workspace identity"):

- `nex-agent`: v4 conversation subscribe/resync/unsubscribe/command/rowsRange,
  `onDynamicConversationFrame`, sessions-index subscribe/resync/unsubscribe,
  `onDynamicSessionsIndexFrame`.
- `nex-task`: `listTasks` (project rows: not draft/pinned/archived),
  `listPinnedTasks`, `listArchivedTasks`, `listPinnedTaskIds` (first runner;
  supplemental), `listDeletedTaskIds`, `archiveTask`, `unarchiveTask`,
  `setTaskPinned`, `setTaskUnread`, `renameTask`, `deleteTask`,
  `deleteArchivedTask`, `deleteArchivedTasks`, `onDynamicWorkspaceEvent`. The
  project sidebar builds rows from these, not from the sessions index. Return
  values are validated against `nexTaskMetaSchema`; semantics and event reasons
  are specified in `task-state.md`.
- `nex-session.readWorkspacePresentation`.

Unknown event names on a known channel return an empty event (never fire).

Runner projection strategy (MVP): every change is published as a full snapshot
frame (`fromSeq: 0, toSeq: seq`, `snapshot.seq == toSeq`). The UI replaces state
on snapshots without gap checks. Streaming text is throttled (≤ 10 snapshots/s
per session). Deltas are a later optimization.

### Runner view contracts the unchanged UI depends on

- `model-selection.getView({selection})` resolves the caller's intent into
  `effectiveSelection` (+ `selectionIssue` when it is not executable), matching
  `ModelSelectionFacade.getView(input)`. The composer treats
  `effectiveSelection ?? undefined` as the current draft model, so omitting it
  leaves the toolbar at "Choose model" and disables submit even when
  `preferredSelection` is valid.
- `ConversationSnapshot.config.modelSelection` echoes the session's
  provider/model/reasoning. Opening an existing session initializes the
  composer from `snapshot.config`, so a snapshot with only `provider`/`model`
  blanks the model picker and disables sending after reopen.
- `phase:"draft"` sessions are excluded from both `sessions-index` snapshots
  and `nex-task` rows/events. A draft is the composer's `createSession`
  prewarm: no input, no title. The UI's `nexTaskIndexSyncer` already skips
  drafts; the runner must agree or every prewarm/reload adds another empty
  "New task" row. Promotion is the same session turning `running` after
  `sendText`.
- A repeated `tool_call_start` for the same `toolCallId` updates the existing
  row (pi emits it from both the assistant stream and `tool_execution_start`);
  appending twice renders duplicate Read/Bash cards.
- `nex-task.getTaskSessionFilePath` / `getTaskNativeSessionLogFile` answer
  `{path:"", exists:false}` / `{provider:null, path:null, exists:false}`
  (pi session files have no UI-facing path), and
  `nex-agent.conversationWorkflowRunsV4` answers `{runs:[]}`. The UI reads all
  three unconditionally when opening a session.
