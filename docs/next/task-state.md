# Task state (pin / archive / unread / rename / delete)

Status: implemented for the `feature/next` MVP. This is the spec of record for
sidebar task mutations; update it before changing behavior.

## Problem this replaces

`nex-task` used to be read-only on runners: `listTasks` derived rows from live
pi sessions, `listPinnedTasks` / `listArchivedTasks` / `listPinnedTaskIds`
returned `[]`, and `archiveTask` / `unarchiveTask` / `setTaskPinned` /
`setTaskUnread` / `renameTask` / `deleteTask` / `deleteArchivedTask` /
`deleteArchivedTasks` were not registered. The web UI calls `archiveTask`
unconditionally from the sidebar row menu
(`packages/ui/src/WorkspaceSidebarItem.tsx`), so archiving a chat rejected with
`Unknown method nex-task.archiveTask`. Likewise `window-controller` returned
empty lists for every pinned / archived / timeline view.

## Owner

**Single writer: the runner's `SessionMetaStore`**
(`packages/next-runner/src/engine/pi/metaStore.ts`), one JSON file per
workspace under `<dataDir>/workspaces/<base64url(workspacePath)>.json`. pi
session files stay the transcript source of truth; the metadata file carries
everything pi does not model, now including the sidebar's organization state.

The server (`next/server`) never persists task state. `window-controller` only
aggregates runner rows for list queries and controller frames.

```
Browser (packages/ui)
  │ nex-task.archiveTask({taskId, workspacePath, workspaceIdentity})
  │ window-controller.listTaskList({kind, workspaceScopes, sortBy, search, limit})
  ▼
Go server
  │ routes by workspaceIdentity → owning runner (nex-task call)
  │ window-controller fan-out: one nex-task list call per scope
  │ window-controller internal listen: nex-task.onDynamicWorkspaceEvent
  ▼
Runner (packages/next-runner)
  │ NexTaskChannel → PiEngine → SessionMetaStore (single writer)
  └ emits workspace_task_list_changed to listeners
```

## Fields

Per session entry in the metadata file (`SessionMetaEntry`):

| Field        | Type    | Default | Meaning                                                        |
| ------------ | ------- | ------- | -------------------------------------------------------------- |
| `pinned`     | boolean | `false` | Row belongs to the pinned section                               |
| `pinnedAt`   | number  | absent  | Last pin timestamp (informational)                              |
| `archived`   | boolean | `false` | Row belongs to the archived view and is hidden from lists       |
| `archivedAt` | number  | absent  | Last archive timestamp (informational)                          |
| `unreadAt`   | number  | absent  | Timestamp of the newest unread background completion            |
| `deleted`    | boolean | `false` | Tombstone: the session was deleted and must never reappear      |

Back-compat: files written by earlier runner versions have none of these keys.
Reads use truthiness (`entry.pinned === true`, `typeof unreadAt === "number"`);
missing fields mean "not pinned / not archived / read". No migration pass runs.

`deleted` is the same tombstone `deleteSession` already writes for the v4
`deleteSession` command: `nex-task.deleteTask` calls the same engine path, so a
task deleted from either surface cannot reappear (the sidebar also joins this
list via `nex-task.listDeletedTaskIds`).

## Methods

Runner `nex-task` (all workspace-bound, all echo `workspaceIdentity`):

| Method                                              | Writes                                                  | Returns                        |
| --------------------------------------------------- | ------------------------------------------------------- | ------------------------------ |
| `listTasks`                                         | — (excludes archived, excluded pinned, draft, deleted)  | `NexTaskMeta[]`                |
| `listPinnedTasks`                                   | — (pinned && !archived; all served workspaces if no ws) | `NexTaskMeta[]`                |
| `listArchivedTasks`                                 | — (archived)                                            | `NexTaskMeta[]`                |
| `listPinnedTaskIds`                                 | — (pinned && !archived, all served workspaces)          | `string[]`                     |
| `listDeletedTaskIds`                                | —                                                       | `string[]`                     |
| `archiveTask`                                       | `archived=true, archivedAt=now`                         | `NexTaskMeta`                  |
| `unarchiveTask`                                     | `archived=false`                                        | `NexTaskMeta`                  |
| `setTaskPinned`                                     | `pinned, pinnedAt`                                      | `NexTaskMeta`                  |
| `setTaskUnread`                                     | `unreadAt` (set) / conditional clear                    | `NexTaskMeta`                  |
| `renameTask`                                        | `title, titleSource=custom` (via engine rename)         | `NexTaskMeta`                  |
| `deleteTask`                                        | tombstone + session file removal                        | `void`                         |
| `deleteArchivedTask`                                | conditional tombstone (only while still archived)       | `boolean`                      |
| `deleteArchivedTasks`                               | per-item conditional tombstone                          | `NexArchivedTaskDeletionResult` |

Every returned `NexTaskMeta` is validated against `nexTaskMetaSchema`
(`packages/shared/src/validation.ts`) inside the channel before it leaves the
runner.

Conditional delete is decided inside the metadata store's write queue, so a
"confirm delete" dialog that stays open while another client unarchives cannot
delete a restored task: `deleteArchivedTask` returns `false` (and
`deleteArchivedTasks` reports the id under `skippedTaskIds`) unless the entry is
still archived and not already deleted at write time.

`setTaskUnread({unread:false, expectedUnreadAt})` is a compare-and-clear: the
comparison and the write happen in the same queued write. A stale phone tap that
arrives after a newer background completion therefore leaves the newer
`unreadAt` intact and returns the current row (`cleared: false` semantics are
not exposed by the interface; the returned meta carries the surviving value).
Without `expectedUnreadAt` the clear is unconditional, matching the interface's
documented fallback.

## Unread production

The runner marks a task unread when a run reaches a terminal phase
(`completedSuccess` / `completedInterrupted` / `error`) **and** no browser is
subscribed to that session's conversation at that moment (background
completion). A task the user is watching keeps no badge.

The `nex-task` event for that transition is `task_status_changed` with
`unreadSignal: "background_terminal"` and the fresh `taskMeta`, exactly what
`packages/ui/src/lib/taskStatusUnreadSync.ts` consumes (blue dot + Dock badge,
and it calls `setTaskUnread({unread:true})` itself; that write is idempotent).
`unreadAt` is also persisted runner-side, so a reload shows the same state, and
`setTaskUnread({unread:false, expectedUnreadAt})` clears it on task selection
(`packages/ui/src/app-shell/useWorkspaceTaskNavigation.ts`).

## Rename and the two indexes

`nex-task.renameTask` reuses the engine rename used by the v4 `renameSession`
command: `PiEngine.rename` writes `title` + `titleSource: "custom"` to the
metadata store and `session.setSessionName(title)` to the pi session, then
fires `onSessionsChanged`. Because there is one write path:

- the **sessions index** republishes a snapshot with the new title
  (`SessionsIndexPublisher.publishWorkspace` on `onSessionsChanged`), and
- the **conversation snapshot** for an open session is patched by the same
  `ConversationPublisher.renameSession` used by the v4 command handler, so a
  viewer sees the new title without reopening.

`nex-task` then emits `task_title_changed` with the fresh meta.

## Events

`NexTaskChannel` diffs the previous per-workspace snapshot against the new one
after every metadata change and emits `workspace_task_list_changed` for each
changed row. Reasons and the UI's refresh policy
(`packages/ui/src/lib/taskListRefreshPolicy.ts`):

| Change                     | Reason               | UI action                                    |
| -------------------------- | -------------------- | -------------------------------------------- |
| row appears                | `task_created`       | refetch membership + insert row              |
| `archived` false→true      | `task_archived`      | refetch membership (row leaves project list) |
| `archived` true→false      | `task_unarchived`    | refetch membership (row returns)             |
| `pinned` false→true        | `task_pinned`        | refetch membership (row moves to pinned)     |
| `pinned` true→false        | `task_unpinned`      | refetch membership                           |
| `title` changed            | `task_title_changed` | sessions-index detail only (no membership)   |
| `unreadAt` changed         | `task_meta_changed`  | refetch membership (unread lives in rows)    |
| `phase` changed            | `task_status_changed`| status/activity only                         |
| other `lastActivityAt`     | `task_meta_changed`  | refetch membership                           |
| tombstoned / disappeared   | `task_deleted`       | refetch with deleted tombstone join          |

`task_model_changed` is never emitted by the runner (no model mutation surface
here). Events always carry `workspacePath` and, when the scope was opened with
one, `workspaceIdentity` — the UI groups rows by
`workspaceIdentity?.trim() || workspacePath`.

## `window-controller` aggregation

`listTaskList({kind, workspaceScopes, sortBy, search?, limit?})`:

1. Dedupe scopes by `workspaceIdentity?.trim() || workspacePath`.
2. Resolve each scope to its owning runner (identity first, then unique path) and
   call the `nex-task` list method(s) the `kind` needs:
   `pinned → listPinnedTasks`, `archived → listArchivedTasks`,
   `timeline → listTasks`, `active → listTasks + listPinnedTasks` merged.
   A scope whose runner is offline or ambiguous contributes no rows; other
   scopes are unaffected.
3. Merge, sort by `createdAt` (or `updatedAt`) descending with `taskId` as the
   tiebreak, filter `search` by title (case-insensitive substring), compute
   `total`, apply `limit`, and return `{items, total, hasMore}`.
4. Each item is the runner's `NexTaskMeta` plus controller fields
   (`sourceAvailability: "online"`, `liveStatus` derived from `status`:
   `running → running`, `error → error`, `completed → completed`, else
   `idle`). Address identity (`taskId`, `workspacePath`, `workspaceIdentity`)
   is echoed unchanged so the client join keys match.

`deleteArchivedTask({address})` / `deleteArchivedTasks({address, taskIds})`
forward to the owning runner's `nex-task` equivalents and return their result.

`listPinnedTaskIds` stays workspace-less and routed to the first connected
runner (`router.nexusTaskGlobalMethods`). It is only a supplemental hint: the UI
also folds every `listPinnedTasks(scope)` row into its pinned id set
(`packages/ui/src/lib/taskListMembershipSets.ts`), so a server-side fan-out
would add no information. Documented decision: keep first-runner behavior; the
per-scope pinned rows are authoritative.

### Controller frames

The server subscribes itself to each runner workspace it knows about
(`nex-task.onDynamicWorkspaceEvent` via `Registry.RegisterListen`, so the
binding is re-issued after a runner reconnect). On any event for workspace
`W` it schedules one debounced refresh, re-reads `W`'s three partitions from the
owning runner, replaces its cached rows for `W`, and pushes a **snapshot**
frame to every `controller/tasks-index` subscriber. Snapshots (rather than
deltas) are required because `NexTaskMeta` does not carry membership: the server
cannot build a complete delta row from an event alone.

```
mutation (browser)                 runner                      server                        browser
archiveTask ──────────────────────▶ metaStore write
                                    emit task_archived
                                    ──────────────▶ internal listen
                                                    refresh W (3 list calls)
                                                    emit snapshot frame ──────────▶ registry revision++
                                                                                    listTaskList refetch
```

Frames use the existing shape (`packages/shared/src/nex-protocol-v4/controller.ts`):
`{topic, subscriptionId, logEpoch, fromSeq:0, toSeq:seq, sentAt, payload:{kind:"snapshot", snapshot:{protocolVersion:1, logEpoch, tasks:[row]}}}`.
`row = {address, meta, membership:{pinned, archived, active}, sourceAvailability, liveStatus}`.

## Facade gaps (unchanged by this spec)

`branchTaskFromPrompt`, import/session-file paths, automation, and workflow runs
remain unimplemented on the runner; they are out of scope for task state.
