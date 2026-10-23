# Automations, sidebar views and search (batch 5) — spec

Status: planned on `feature/next`. Research with citations:
`automations-views-search-research.md` (A automations, B views, C search, D phasing).

## A. Automations (server-owned)

Today the Automations page fails on its first call (`nex-agent.listAllAutomations` has no
workspace and nothing serves it).

- **Owner: the server** (definitions, schedules, run history), because runners may be
  offline and the page lists all automations globally. Store
  `<config dir>/automations.json` (schema 1, atomic rewrite), ≤ 20 automations, ≤ 200 runs
  per automation (oldest pruned).
- **Channel methods** served locally by the server, with the exact `NexAutomation` /
  `NexAutomationRun` shapes and the old validation error texts the UI maps to toasts:
  `listAllAutomations`, `listAutomations`, `createAutomation`, `updateAutomation`,
  `deleteAutomation`, `setAutomationEnabled`, `restartAutomation`, `runAutomationNow`,
  `listAutomationRuns`, `deleteAutomationRun`.
- **Schedule**: `cronExpr` parsed with `robfig/cron/v3` (parse only); `scheduleRule`
  calendar math (minute / hourly / daily / weekly / weekday / monthly as the editor's
  presets send) ported from `automationCron.ts`, evaluated in the automation's timezone
  (captured from the server's local zone at create). One timer armed to the earliest
  `nextRunAt`, re-armed on every change; persist before dispatch so a crash never
  re-fires.
- **Dispatch**: at fire time the server sends one `sendConversationCommandV4`
  `createSession{firstInput:{text, modelSelection?, mode?}}` (commandId = runId, so
  retries are idempotent) to the runner owning `workspaceIdentity`; automations bound to
  an existing session send `sendText` instead. The run records the returned `sessionId`.
  Sessions carry `cronAutomationId` in runner metadata so rows show the cron badge.
- **Unattended approvals**: automation turns run with the automation's mode; the runner
  answers any approval request in an automation session as **deny** with reason
  "approval needed: automation runs unattended" (never waits), so a run cannot hang.
  Users who want writes pick "Edit automatically" or "Full access" for that automation.
- **Outcome**: from the session's status in the runner's task rows (the server's
  window-controller already subscribes to runner task events): running → succeeded /
  failed / stopped.
- **Missed / offline**: fire time missed by > 5 min (server down) → run `skipped`, advance.
  Runner offline at fire time → retry every 60 s, max 3, then `failed_to_dispatch`;
  recurring automations keep their schedule, one-shots become failed (Restart revives).

```
timer ─▶ claim due automation ─▶ run{claimed} persisted ─▶ runner createSession(firstInput)
      ◀─ ACK {sessionId} ─ run{dispatched, sessionId}, nextRunAt advanced, persisted
runner task event ─▶ server controller refresh ─▶ row status terminal ─▶ run.outcome
```

## B. Sidebar views

- Timeline, pinned and archived-flat already work through `window-controller`.
- Fix: controller reads must omit an empty `workspaceIdentity` and require a path.
- **Task groups** (grouped view): runner metadata gains groups, members, ordering; the
  `nex-task` group methods (`listGroupedTaskViewStructure`, `createTaskGroup`,
  `renameTaskGroup`, `updateTaskGroupColor`, `deleteTaskGroup`,
  `applyGroupedTaskViewOrder`) are served by the runner with the shared shapes; the cron
  system group appears when a row has `cronAutomationId`.

## C. Search (⌘K command center)

- Runner `nex-task.searchTasks({workspacePath, workspaceIdentity?, query, limit})`: titles
  plus visible message text (pi `SessionManager.list` `allMessagesText`, capped 100 KB per
  session), in-memory per workspace, invalidated on session changes. Snippets ported from
  the old `buildSearchSnippets` (same radii and limits) because the command center renders
  them.
- Server: `window-controller.listTaskList` with `search` fans out to the scopes' runners
  in parallel (3 s timeout per runner), merges, sorts, applies the limit; results are not
  cached as list rows. Queries shorter than 2 characters match titles only.
- Target p95 ≤ 400 ms warm for 3 runners × 2,000 sessions.

## Acceptance

- Go: automation store, validation messages, cron + scheduleRule next-run parity with
  fixtures from the old implementation, timezone/DST, missed and offline policies,
  run-now duplicate, dispatch idempotency; search fan-out merge/limit/timeout.
- Runner: unattended deny in automation sessions, `cronAutomationId` rows, task groups,
  search + snippets.
- Browser E2E `next/e2e/automations.mjs` (`--e2e=automations`, in `all`): create an
  automation from the page → run now → a new chat appears with the prompt and the reply,
  run history shows succeeded and jumps to the chat; pause/resume/delete; a 1-minute
  schedule fires on its own. `next/e2e/search-groups.mjs` (`--e2e=search`): ⌘K finds a
  chat by a word only in its transcript and shows the snippet; create / rename / delete a
  task group and move a chat into it; reload keeps it.
