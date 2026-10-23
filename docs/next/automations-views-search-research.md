# Batch 5 research — automations, sidebar views, search

---

## A. Automations / cron

### A.1 UI surface (what the unchanged web client calls)

| Surface                                                                 | File:line                                                                                                                                                                                                         | Calls                                                                                                                                    |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Automations page (list + status filter + toggle/run-now/restart/delete) | `packages/ui/src/settings/AutomationsSection.tsx:354-372` (store hooks), mounted from `SettingsPage.tsx:1088` and `WorkspaceShellLayout.tsx:1765`                                                                 | store → `nexAgentService.*Automation`                                                                                                    |
| Management store (cache per workspace)                                  | `packages/ui/src/store/automationManagementStore.ts:144` `listAllAutomations()`; `:229` create; `:266` update; `:302` delete; `:338` setEnabled; `:386` restart; `:426` runNow; `:473` listRuns; `:512` deleteRun | all with `{workspacePath, workspaceIdentity?}` except the list                                                                           |
| Create/edit page                                                        | `packages/ui/src/settings/AutomationEditView.tsx` (project picker at `:128`), templates via `clientScenesService`                                                                                                 | same store actions; create carries an explicit `targetPath/targetIdentity` (`AutomationsSection.tsx:551-565`)                            |
| Project picker                                                          | `packages/ui/src/hooks/useAutomationProjectOptions.ts:70-83` → `useLocalWorkspaceScopes` (`:75`); `settings/automationWorkspaceOptions.ts`                                                                        | **pure client-side**, derives options from open tabs; no RPC. `runner:` identities are accepted (`useAutomationProjectOptions.ts:14-19`) |
| Run history + “jump to session”                                         | `AutomationsSection.tsx:637-682`, `:743-754`                                                                                                                                                                      | `run.sessionId` is passed to `onOpenSession` and treated as a **task/session id** (`AutomationsSection.tsx:118`)                         |
| Cron badge on rows / grouped “cron” system group                        | `packages/shared/src/nex-task-types.ts:46,58`                                                                                                                                                                     | `meta.cronAutomationId`                                                                                                                  |

Service interface: `packages/services/src/nex-agent/nexAgent.ts:681-692` — `listAutomations(workspaceTarget)`, `listAllAutomations()`, `createAutomation`, `updateAutomation`, `deleteAutomation`, `setAutomationEnabled`, `restartAutomation`, `runAutomationNow` → `{status:"queued"|"duplicate"}` (`nexAgentService.ts:3589-3606`), `listAutomationRuns`, `deleteAutomationRun`. Impl `packages/services/src/nex-agent/nexAgentService.ts:3519-3607`. Domain types: `packages/shared/src/automation-types.ts:65-146` (`NexAutomation`, `NexAutomationRun`), schedule rule `:35-46`.

**Nex-next gap — this is the biggest single hole:** `nex-agent` methods _without_ a workspace in the arg are answered from a fixed local table (`next/server/internal/router/router.go:195-200`), and the runner registers **no** automation methods (`packages/next-runner/src/channels/register.ts` has none; `grep` for automation in `nex-runner/src` = 0). `listAllAutomations()` has no workspace at all, so today the page fails at its very first call. The other calls are rejected too (`TargetLocalUnsupported` is the fall-through for any `nex-agent` method not in `workspaceTargetMethods`). Off-peak (`ServiceChannels.OffPeakTask = "off-peak-task"`, `packages/shared/src/channels.ts:141`) is **not consumed by `packages/ui` at all**; only the CLI has `offPeak/create|list` wire methods (`packages/shared/src/nex-protocol/index.ts:3660-3661`). So ignore off-peak — it is not cheap and not needed.

### A.2 How the old host scheduled and ran

- Scheduler is a separate desktop utility process: `packages/desktop/src/scheduler/index.ts` — 20 s poll (`:24`), 5 min misfire grace (`:29`), `repo.claimDue` + `claimManualRuns` (`:76-83`), `handleClaimed` (`:104`) → `upsertRunClaimed` → `postDispatchRequest` with `runId = automationId:scheduledAt` (`:64`, `:156`), settlement `settleDispatchResult` (`:192`) → `markRunDispatch("dispatched", sessionId)` (`:224-231`), failure backoff `markDispatchFailed` (`:245`).
- Main forwards to a host, which executes: `packages/desktop/src/host/index.ts:493-590` — `nexTaskService.createTask({model,mode,thoughtLevel,automationId})` (`:535`) or resume `targetTaskId` (`:552`), then `sendPrompt({traceId: runId, content, automationId})` (`:581`), tracking outcome through `onDynamicTaskTerminalOutcome(taskId)` filtered by `inputId === traceId` (`:458`), then `settleCronRunTerminalOutcome` → `repo.markRunOutcome` (`packages/desktop/src/host/cronRunLifecycle.ts:116-124`, `automationRepo.ts:1242-1268`), and `setTaskUnread(true)` (`host/index.ts:464-468`).
- Cron math (croner, **parse-only**): `packages/services/src/session/automationCron.ts:1,59-66` (`new Cron(expr).nextRun`), `:90-130` (`computeInitialAutomationNextRunAt`), `:133` (scheduleRule calendar math, local timezone), `:343` `computeAutomationNextRunAt`. Create/update lifecycle + validation: `automationService.ts:144-231`, `:255-425`. Repo accounting (run_count vs scheduled_run_count, maxRuns, endAt): `automationRepo.ts:940-975`, `:995-1030`.
- State: `automations` + `automation_runs` tables in `tasks-index.sqlite` (`automation-types.ts:6`). Run id contract: `parseAutomationRunId` `automation-types.ts:115-126`.
- **Gotcha for design:** the old dispatch session id is the taskId, and `createTask` returns the same string twice (`host/index.ts:590`), which happens to satisfy the UI's `sessionId` assumption (`AutomationsSection.tsx:118`).

### A.3 Proposed nex-next design (server-owned)

**Why server:** definitions, schedules and run history must survive runner offline; a runner-owned schedule silently stops when a laptop sleeps, and the UI queries “all automations” globally.

New server package `next/server/internal/automations` (JSON file in the config dir alongside `config.json`, atomically rewritten on watcher restart / d.ts note; ≤20 automations by `AUTOMATION_CREATE_LIMIT`, history capped 200 runs/automation with oldest-pruned):

```jsonc
// automations.json  (schema 1)
{"schema":1,"automations":[{
  "automationId":"...","title":"...","prompt":"...",
  "cronExpr":"0 9 * * *",                     // display only when scheduleRule set
  "scheduleRule":{"unit":"daily","interval":1,"hour":9,"minute":0,"anchorAt":0}, // authoritative, server tz
  "timezone":"Asia/Shanghai",                  // captured at create from server default
  "workspaceIdentity":"runner:alpha:/src/app","workspacePath":"/src/app",
  "modelSelection":{...},"mode":"build",
  "recurring":true,"maxRuns":null,"endAt":null,
  "enabled":true,"lifecycleStatus":"active",
  "nextRunAt":1790..., "lastRunAt":null, "runCount":0, "scheduledRunCount":0,
  "lastError":null,"createdAt":..,"updatedAt":..}]}
// runs[]: {runId, automationId, workspaceKey, scheduledAt?, trigger:"schedule"|"manual",
//   dispatchStatus:"claimed"|"dispatched"|"failed_to_dispatch"|"skipped",
//   outcome?:"running"|"succeeded"|"failed"|"stopped", sessionId?, error?, attempts}
```

- **Cron library:** adopt **`robfig/cron/v3` parse-only** to mirror `computeNextRunAt` (`croner` five-field semantics, including `*/N`, names, ranges) and write the scheduleRule calendar math (`automationCron.ts:133-343`) by hand. Do **not** use its scheduler (`cron.New().AddFunc`): the old loop is a poller with _claim-as-state_ semantics (a skipped window is a first-class record, `scheduler/index.ts:113-136`). Use `robfig/cron` itself as the clock instead of a 20 s poll: compute delay to `min(nextRunAt)`, timer + a 1 s floor, re-arm on every mutation. Rewrite of `automationCron.ts`/`automationService.ts` in Go ≈ **~1 200 LOC** incl. validation (this is the bulk of the estimate).
- **Timezone:** scheduleRule math in the automation's `timezone` via Go `time.LoadLocation`; `cronExpr` parsed in that location. New automations inherit the server's `time.Local` (renamed `zone`). DST: calendar rules fire at the wall-clock minute of that day; skipped hour → fire at the transition minute once.
- **Execution:** at fire time the server resolves `workspaceIdentity` → live runner via `Registry.CallWorkspace` (`next/server/internal/runners/registry.go:503`, routing rule `:472-500`) and sends **one** command: `nex-agent.sendConversationCommandV4 {workspacePath, workspaceIdentity, envelope:{commandId:runId, sessionId:null, baseRevision:0, type:"createSession", payload:{workspaceId:path, firstInput:{text:prompt, modelSelection?, mode?}}}}`. That path already exists end to end: `packages/next-runner/src/v4/commands.ts:99-100, 182-238` creates the session, submits the first input (naming the prompt with the trace id), returns `accepted` + `{type:"createSession", sessionId}`. Runner dedupe on `commandId` (`commands.ts:53-54, 72-80`) makes retries safe. For `targetTaskId` automations (session-created), send `sendText` with that `sessionId` instead. No `createSession+sendText` two-call sequence — `firstInput` is atomic and avoids an empty “New task” row (`docs/next/README.md:232-237`).
- **Outcome tracking:** add `meta.cronAutomationId` on the created row (runner must persist a per-session `cronAutomationId`/`offPeakTaskId` in `SessionMetaStore` and emit it in `toNexTaskMeta`, `packages/next-runner/src/channels/nexTaskRows.ts:26-51`) so the cron badge and grouped “cron” group work. Then the server's existing debounced refresh is the completion signal: when a watched workspace's row for `run.sessionId` turns `status != "running"` (`window_controller.go:309-320, 701-735`), write `outcome = error|succeeded` and, if the session was not watched, `setTaskUnread` already happens runner-side (`nexTask.ts:338-369`) — no server code needed. This replaces `onDynamicTaskTerminalOutcome` (`nexTaskService.ts:714`), which no runner/nex-next path emits.
- **Missed runs:** at tick, `now - nextRunAt > 5 min` → record `dispatchStatus:"skipped"` (`reason:"server_offline"`) and advance; one-shot (`!recurring && maxRuns<=1`) → `completed`. Runner offline at fire time (or `runner offline` from the call): **retry with 60 s backoff, max 3 attempts** (matches `DISABLE`-free transient path in `automationRepo.ts:995-1030`), then `failed_to_dispatch`; recurring keeps its schedule and records `lastError`, one-shot → `lifecycleStatus:"failed", enabled:false` (Restart button revives, as today `automationService.ts:441-450`).
- **Approvals:** automation turns run unattended; the runner's gate waits forever for `ask` (`packages/next-runner/src/engine/permissions/gate.ts:54-57`). A `mode:"plan"`/read-only or an explicit “auto-deny approvals” policy is required, otherwise runs hang (outcome stays `running`).
- **Channel methods (server-local, all JSON not forwarded):** `nex-agent.listAllAutomations`, `listAutomations`, `createAutomation`, `updateAutomation`, `deleteAutomation`, `setAutomationEnabled`, `restartAutomation`, `runAutomationNow`, `listAutomationRuns`, `deleteAutomationRun`. Payloads/returns exactly the TS shapes (camelCase `NexAutomation`/`NexAutomationRun`). `runAutomationNow` inserts a `:manual:uuid` run, dispatches immediately, returns `{status:"duplicate"}` while a manual run for that automation is in flight. Validation errors must use the old messages (`InvalidCronExprError` text, `AUTOMATION_CREATE_LIMIT_REACHED`, `automationService.ts:41-134`) because the UI maps them to toasts (`automationManagementStore.ts:244`). Also add the router's no-workspace `nex-agent` table entry (`router.go:195-200`).

Event order:

```
tick (server timer)
  ├─ claim: automations.json  nextRunAt<=now, lifecycle=active, !running
  ├─ runs[] upsert {runId, claimed}
  ├─ registry.CallWorkspace(identity → runner)
  │     runner: createSession(firstInput) ──> session "running"
  │              onSessionsChanged ──> nex-task event
  │     ack {result.sessionId} ── one timeout, no retry
  ├─ runs[] dispatched{sessionId}; automations.nextRunAt=+1, runCount++
  │   (persist BEFORE publish, so a crash never re-fires)
  └─ push server event "automations/changed" (browser store refresh via onDidChange-like poll/broadcast)
...later
  runner event ──> window-controller debounced refresh
  ├─ row.status terminal → runs[].outcome; lifecycle (maxRuns/endAt) recomputed
  └─ runner already set unreadAt (background terminal, nexTask.ts:338-369)
```

The UI has no automation push event; `AutomationsSection` refreshes on mount/action. A cheap improvement: server broadcasts `broadcast.send({channel:"automations:changed"})`; the store ignores unknown channels today, so treat “reload page / reopen page” as acceptable.

### A.4 Essential UI methods (minimum usable)

`listAllAutomations` (page cannot load without it), `createAutomation`, `updateAutomation`, `deleteAutomation`, `setAutomationEnabled`, `runAutomationNow`, `listAutomationRuns`, `deleteAutomationRun`. Cheap extras: `restartAutomation` (failed one-shots), `listAutomations` (no UI caller). `scheduleRule`/calendar units can be deferred: accept `cronExpr` + `recurring`/`maxRuns` only (templates and the picker send `scheduleRule` — validate then store; but wire `intervalUnit/interval` → 5-field cron is acceptable for v1, rejecting `weekly/monthly/weekday` UI presets is not: `AutomationEditView` maps presets to scheduleRule, so the Go side must at least accept `daily/hourly/minute` rules).

---

## B. Sidebar views

### B.1 Call graph

| View              | Component                                     | Data source                                                                                                                                                                                                                                                                                           |
| ----------------- | --------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Project (default) | `hooks/useWorkspaceTaskLists.ts:195`          | `nex-task.listTasks` + `listArchivedTasks(scope)` + `listPinnedTaskIds` + `listDeletedTaskIds` via `lib/taskListMembershipSets.ts:197-230`; `onDynamicWorkspaceEvent` (`:676`, reason handling `:700-712`); sessions-index only enriches (`buildTaskListResultFromSessions.ts`)                       |
| Pinned            | `WorkspacePinnedTasksSection.tsx:113-120`     | controller `kind:"pinned"`, `limit:20`                                                                                                                                                                                                                                                                |
| Timeline          | `WorkspaceTimelineTasksSection.tsx:140-149`   | controller `kind:"timeline"`, `sortBy`, `limit:visibleTaskLimit`, `searchQuery:""`                                                                                                                                                                                                                    |
| Archived flat     | `WorkspaceArchivedTasksFlatSection.tsx:85-92` | controller `kind:"archived"`, `limit:20`, delete via `nex-task.deleteArchivedTasks` (`:105-120`)                                                                                                                                                                                                      |
| Grouped           | `hooks/useGroupedTaskView.ts:657-663`         | controller `kind:"active"` (facts) + `nexTaskService.listGroupedTaskViewStructure` (`:752`) + membership sets (`:755`); mutations `createTaskGroup` (`:900`), `renameTaskGroup` (`:944`), `updateTaskGroupColor` (`:995`), `applyGroupedTaskViewOrder` (`:853`, `:1027`), `deleteTaskGroup` (`:1066`) |
| Header pin menu   | `WorkspaceHeaderSections.tsx:137-152`         | controller `kind:"pinned"` only when menu open                                                                                                                                                                                                                                                        |

Controller plumbing: `useGlobalTaskList.ts:114-131` builds `{kind, workspaceScopes, sortBy, search?, limit?}` (limit only when collapsed), `:152` → registry cache by `queryKey`; registry subscribes to **both** topics (`v4/windowControllerTaskListRegistry.ts:187-200`) and replaces rows only on `controller/tasks-index` snapshots (`:148-158`). UI membership semantics `matchesTaskListMembershipKind` (`packages/shared/src/nex-protocol-v4/controller.ts:125-139`).

### B.2 What nex-next returns today

- `listTaskList` (`next/server/internal/local/window_controller.go:217-263`): fans out per scope to `listPinnedTasks`/`listArchivedTasks`/`listTasks` (`:389-417`), kind predicate `:279-290` (= shared matcher, correct), sort/limit OK.
- `controller/workspaces` (`:793-795`) → **always `workspaces: []`**; that topic is unused by the UI (registry returns early, `:148-151`), so empty is fine.
- Controller snapshots: the server pushes _all cached rows_ with full membership (`:827-842`), so the list API and frames are consistent.

### B.3 Precise gaps (bugs first)

1. **`workspaceIdentity: ""` poisons routing (B, bug).** `window_controller.go:373-387` marshals `"workspaceIdentity": scope.WorkspaceIdentity` unconditionally even when empty. `registry.resolveLocked` uses `strings.TrimSpace(target.WorkspaceIdentity) != ""` so an empty string is harmless _there_, but the forwarded runner arg carries `workspaceIdentity: ""` — harmless — **however** for scopes opened with an identity the server also passes `"workspacePath": scope.WorkspacePath` only; if a tab carries identity but empty path → `decodeTaskMetas` mislabels rows. Fix: omit the field when empty and require a non-empty path.
2. **`kind:"active"` is not a UI kind** (`NexTaskListKind` = pinned|archived|timeline|active, `nexTaskListTypes.ts:3`; grouped passes `active`). Server maps `active` correctly (`matchesKind` default branch → `!archived`, `:285-286`) — but the semantics differ from `timeline`: grouped builds its membership from `listGroupedTaskViewStructure` (missing) so **grouped view is empty except top-level facts**.
3. **Task groups: 100% missing on nex-next.** `nex-task` runner channel registers no `createTaskGroup/renameTaskGroup/updateTaskGroupColor/deleteTaskGroup/listGroupedTaskViewStructure/applyGroupedTaskViewOrder` (`register.ts:123-207`), and `SessionMetaStore` has no group tables. `useGroupedTaskView.ts` therefore rejects (`Promise` fails) → empty view + console error. Also the Cron system group (`CRON_DEFAULT_GROUP_ID`, `nex-task-types.ts:46`) has no producer.
4. **Timeline is a different set from the old app.** Old reference used the same `listTaskList` (`windowHostControllerService.ts:621-647`, `channel-research.md` §3), so parity is OK — but nex-next's timeline excludes pinned rows (`:288` default) exactly like the shared matcher, fine. The visible breakage is elsewhere: `listPinnedTasks`/`listArchivedTasks` are _workspace-less or scoped_ (`nexTask.ts:85-99`) and the server always passes a scope, OK.
5. **Archive section's delete-all** requires `nex-task.deleteArchivedTasks` — implemented (`register.ts:191-198`), OK.
6. **Header/composer re-open refresh works** only because the server self-subscribes (`window_controller.go:648-697`); if the runner reconnects, `RegisterListen` re-issues (`registry.go:546-560`), OK.
7. **Project-mode rows are `listTasks` = “neither pinned nor archived”** (`nexTaskRows.ts:17-20`), matching the old `pinned:false, archived:false` query (`nexTaskServiceAdapter.ts:2320-2328`). OK.
8. **Grouped view’s “cron” group membership** needs `cronAutomationId` on rows (A.3) — otherwise even after implementing groups, cron sessions never appear in the system group.
9. Auto-archive (`taskAutoArchiveEnabled`) is a **no-op** on nex-next (old path runs only inside `listGroupedTaskViewStructure`, `nexTaskServiceAdapter.ts:2408`); acceptable to defer.
10. **Timeline pagination**: `limit:visibleTaskLimit` is passed; `hasMore` works (`:250-257`). Fine.

### B.4 Cheap, correct fixes

- **Timeline/archived/pinned:** already work. Verify with `next/e2e/task-actions.mjs`; add an archived-flat case (archive from project menu → archived section shows row; unarchive from `WorkspaceArchivedTasksFlatSection.tsx:234` returns it to timeline).
- **Groups (P2):** runner-side `SessionMetaStore` tables `groups(id, workspace_key, title, color)` and `group_members(workspace_key, group_id, task_id, sort_order, added_at)` + `top_orders`; channel methods returning the exact shapes (`nexTaskListTypes.ts:86-118`). Server needs **no** new code: `nex-task` calls with a workspace target already route by identity (`router.go:221-236`). The system cron group is created lazily when a row first gets `cronAutomationId` (mirror `taskIndexRepo.ts:1364-1370`). ≈ 3 days.
- Until groups land, grouped view is functionally “flat list of unfiled tasks” only if `listGroupedTaskViewStructure` returns `{groups:[],members:[],topLevelOrders:[]}` — then `buildGroupedTaskViewFromSessions` shows every active task at top level. **Implement that stub on the runner in 30 min** so the view isn’t broken/erroring.

---

## C. Search

### C.1 Entry points

- **Command center** (⌘K / `openCommandCenter`, bound in `App.tsx:941`, toggle `:648-650`): `CommandCenterDialog.tsx:471-478` calls controller `listTaskList({kind:"active", workspaceScopes: all open workspace tabs, sortBy:"updated", search, limit:80})` (`App.tsx:398` passes every workspace tab; `COMMAND_CENTER_TASK_RESULT_LIMIT = 80`, `CommandCenterDialog.tsx:63`; section preview 3 rows, `:60`). Results render title + `searchSnippet` (`:870-886`), so **snippets are load-bearing**.
- **Sidebar header search**: there is none — sidebar sections pass `searchQuery: ""` (`Pinned:118`, `Timeline:145`, `Archived:90`). The only sidebar search entry is the command center shortcut label (`WorkspaceSidebar.tsx:351`).
- **Project mode**: `useWorkspaceTaskLists` does not send `search`.
- Old list search (`nex-task.listTaskList`) existed but has no nex-next UI caller — controller's `listTaskList` is the only consumer (`useGlobalTaskList.ts:152`).

### C.2 What the old implementation searched

`taskIndexRepo.queryTaskList` (`packages/services/src/session/taskIndexRepo.ts:1801-1885`): SQL over `tasks` with `LOWER(title) LIKE ? OR LOWER(searchable_text) LIKE ?` (`:1828`), kind membership in SQL, `LIMIT`, totals via `COUNT(1)`. `searchable_text` is a **precomputed** blob (≤200 000 chars, `nexTaskIndexSyncer.ts:1898`), built from user+assistant _visible_ text only (no thinking/tools, `:1901-1937`), refreshed on every synced snapshot (`:1677-1685`). Snippets computed in JS: match-centered windows, prefix 20 / suffix 72 chars, ≤140 chars, ≤4 snippets, deduped (`taskIndexRepo.ts:151-154, 315-356`), returned as `searchSnippet/searchSnippets` (`:358-366`).

### C.3 Proposed design

- **Runner-side `nex-task.searchTasks({workspacePath, workspaceIdentity?, query, limit?})`**; server fan-out in `listTaskList` when `search != ""`.
- Data: pi already gives us almost exactly this — `SessionManager.list(cwd, sessionDir)` returns `SessionInfo{path,id,name,created,modified,messageCount,firstMessage,allMessagesText}` (`@earendil-works/pi-coding-agent/dist/core/session-manager.d.ts:163-177, 399`). `allMessagesText` is the "searchable text"; `firstMessage` backfills the title. Session dir helper exists (`packages/next-runner/src/engine/pi/piSessionFiles.ts:5-9`).
- **V1 (cheap, no persistence):** in-memory index in `NexTaskChannel`, built lazily per workspace on first search: `SessionManager.list(...)` → lowercase titles + `allMessagesText.slice(0,100_000)` per session, keyed by `sessionId`; invalidated on `onSessionsChanged` for sessions whose `lastActivityAt` changed (row refresh already reads them). Filter out `deleted`, `draft`, and anything not in the runner's metadata (server passes membership separately anyway — keep server-side kind filter as today, `window_controller.go:230`).
- **V2 (if warm latency > target):** persist the index in the runner JSON data dir (`sessions-index.json`: `{sessionId, title, mtimeMs, size, text}`), rebuild incrementally when `mtime` changed; cap 200 KB/session.
- **Snippets:** port `buildSearchSnippets` verbatim to `packages/next-runner/src/channels/searchSnippets.ts` (same radii/limits) so command-center highlighting (`HighlightedMatchText`, `CommandCenterDialog.tsx:881-886`) is identical.
- **Server merge:** per scope call runner `searchTasks` (with query, `limit = browserLimit` capped 200) in parallel; dedupe by `key()`, sort, then apply `limit`, then compute `total` as sum of runner `total`s (exact when `hasMore` false; approximate otherwise — the UI only reads `hasMore`). Reuse the existing fan-out and cache path but **do not** persist search results in `rowsByWorkspace`.
- **Limits/latency:** search debounced client-side? No — command center fires on every keystroke. Server side: 150 ms debounce per connection+query, 3 s per-runner timeout; **target p95 ≤ 400 ms** for 3 runners × 2 000 sessions warm, ≤ 1.5 s cold (first `SessionManager.list` reads every JSONL in the dir — progressive `onProgress` may return partials so the first frame at ≤500 ms can be partial and a second frame follows). Search only with `limit` (80) and never `expanded:true`.
- Queries shorter than 2 chars: title-only (same contains as today).

---

## D. Phasing and estimates

Assumes one engineer, nex-next conventions (spec + Go tests + runner tests + a `next/e2e` case per phase).

| Phase                           | Scope                                                                                                                                                                                                                                                                                                                                                                                                   | Est.      |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| **B0 (bug sweep, 0.5 d)**       | omit empty `workspaceIdentity` in controller reads; add `listGroupedTaskViewStructure` stub `{groups:[],members:[],topLevelOrders:[]}`, `applyGroupedTaskViewOrder` echo no-op, `create/rename/color/deleteTaskGroup` local echo on the runner so grouped view is flat-but-usable; `next/e2e` archived-flat + grouped smoke                                                                             | **0.5 d** |
| **C1 (search, 2.5 d)**          | `searchTasks` runner (title + `allMessagesText`, snippets port, 3 runner tests); server fan-out in `listTaskList` with debounce/timeout; e2e: type transcript-only word in ⌘K → row + snippet                                                                                                                                                                                                           | **2.5 d** |
| **A1 (automation core, 4–5 d)** | Go `automations` package: JSON store, `cronExpr`+`scheduleRule` validation (`robfig/cron/v3` parse), calendar next-run in automation tz, timer loop, run ledger, missed/offline policy, `runAutomationNow`; `createSession(firstInput)` dispatch; terminal outcome via controller refresh; channel methods + router table; Go tests (cron parities vs `croner` fixtures, misfire, retry, maxRuns/endAt) | **4–5 d** |
| **A2 (UI edge cases, 1 d)**     | limit error text, update semantics (`null` clears), toast-visible errors, jump-to-session id, cron badge (`cronAutomationId` in runner meta + events)                                                                                                                                                                                                                                                   | **1 d**   |
| **B1 (groups, 3 d)**            | runner group tables + 6 methods + ordering persistence + cron system group; e2e rename/drag                                                                                                                                                                                                                                                                                                             | **3 d**   |
| **B2 (polish, 1 d)**            | auto-archive no-op note or implement in `listTasks`; archived bulk delete e2e                                                                                                                                                                                                                                                                                                                           | **1 d**   |

**Recommended cut for the first usable version (~5 days):** **B0 + C1 + A1 (without `scheduleRule` UI presets beyond minute/hourly/daily)**. That yields: sidebar views that no longer error (grouped = flat), cross-session search in ⌘K with snippets, and server-scheduled automations that create + run a session on the right runner, with history, run-now, pause, delete and offline/missed semantics. Defer task groups (B1) and cron grouping until the automation dispatcher is merged, since both depend on `cronAutomationId` plumbing. Keep two explicit follow-ups in the spec (they cause silent “nothing happened” reports): **unattended approvals** (`permissions/gate.ts:54-57`) and **search cold-start latency** on large session dirs.
