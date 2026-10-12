# Session persistence (Go port, Agent H)

Status: **implemented and verified** for the tables a text turn and a tool turn
write. This document lists every table/column written per turn, how it is pinned
against the Node agent, and what is **not** yet ported.

Owner directories: `go/internal/sessionstore/**`,
`go/internal/adapters/store/sessions*.go` (new files only),
`scripts/go-rewrite/gen-store-vectors.mts`,
`scripts/go-rewrite/crosscheck-sessions-node.mts`,
`scripts/go-rewrite/crosscheck-sessions.mjs`,
`go/testdata/vectors/store-scenarios.json`.

## What this is

Node writes a turn's durable facts through
`apps/nex-cli/packages/adapters/src/storage/session-store/*` (the
`SqliteSessionStore` repositories). The Go port must write the **same rows and
the same JSON column values** so a session created by one engine is readable by
the other.

- **Port (pure):** `go/internal/sessionstore` — no `database/sql`, no I/O. It
  also owns the ordered-JSON encoders for `message.data` and `part.data`, because
  Node serialises with `JSON.stringify` (insertion order) and Go maps would sort
  keys.
- **Adapter:** `go/internal/adapters/store` — SQLite implementation reusing the
  same `*sql.DB` from `migrate.go` `Open()` (single connection, `busy_timeout`,
  `foreign_keys=ON`, WAL).

## The turn sequence (what the loop calls)

A text turn (matches scenario S02):

| #                | Call                                                                                               | Node source                                                               |
| ---------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| 1                | `CreateSession` (+ two `UpsertSessionEntry`: `runtime/model_selection`, `runtime/execution_state`) | `events.ts ensureSessionPersisted`, `turn-model.ts`, `execution-state.ts` |
| 2                | `AdmitInput` (`session_input`, status `admitted`)                                                  | `events.ts` `TurnSteerQueued` branch                                      |
| 3                | `PromoteInput` (user `message` + `part`s + `session_input.status='promoted'`, one transaction)     | `message-persistence.ts persistUserPrompt` → `promoteSessionInput`        |
| 4                | `SaveMessage` (assistant), `SavePart` × N                                                          | `message-persistence.ts`                                                  |
| 5                | `RecordModelUsage`                                                                                 | `usage-observability.ts recordModelUsageFact`                             |
| 6                | `UpsertTurnUsage`                                                                                  | `recordTurnUsageFact`                                                     |
| 7                | `RecordInputHistory`                                                                               | `input-history.ts recordInputHistory`                                     |
| 8                | `UpdateTitle` (title_source `first_input`)                                                         | `events.ts`                                                               |
| (tool turn adds) | `UpsertToolUsage` per tool, on each lifecycle event                                                | `recordToolUsageFromEvent`                                                |

## Tables and columns written

Column names are the real SQLite columns. "Encoding" notes the value shape.

### `session` — 1 row (25 columns)

`id, project_id, workspace_id, parent_id, trace_id, task_type, slug, directory,
path, title, title_source, title_message_id, version, share_url,
summary_additions, summary_deletions, summary_files, summary_diffs, revert,
permission, time_created, time_updated, time_title_updated, time_compacting,
time_archived`.

- `permission` is JSON (`{"mode":"build"}`).
- `share_url`, `summary_*`, `revert`, `time_compacting`, `time_archived` are
  `NULL` on create.
- `time_title_updated = time_updated` when a `title_source`/`title_message_id`
  is present, else `NULL`.
- `task_type` defaults to `interactive`, `title_source` to `first_input`.
- Later writes only `max(time_updated, ?)` (from `touchSession`) and the title
  fields; the upsert keeps `trace_id` and `permission` via `coalesce`.

### `message` — 2 rows per text turn

`id, session_id, sequence, time_created, time_updated, data`.

- `sequence` = `max(sequence)+1` within the session; on same-session re-save it
  **keeps** the existing sequence (including `NULL`).
- `time_updated` = `time.created` for user messages, `time.completed` for
  assistant messages.
- `data` JSON key order (user):
  `role, time, agent, modelSelection, contextSnapshot, semantics, anchor, tools,
metadata?, model`.
  `model` is a legacy snapshot `{providerID, modelID, variant?}` re-derived from
  `modelSelection`, always present (possibly `{}`).
- `data` JSON key order (assistant):
  `role, parentID, modelId, providerId, mode, planEnabled, agent, path, time,
cost, tokens, finish, semantics, anchor`.

### `part` — 4 rows per text turn (2 user-block + step-start/text/step-finish)

`id, message_id, session_id, sequence, time_created, time_updated, data`.

- `sequence` = `max(sequence)+1` within the message (same keep rule).
- `time_created` = `partCreatedAt`: `time.start` for text/reasoning/compaction/
  timeline, `state.time.start` for tools, `time.created` for retry, else the
  clock (step-start/step-finish).
- `time_updated` = clock at write.
- `data` shapes: `{"type":"text","text","time":{"start","end"}}`,
  `{"type":"step-start"}`, `{"type":"step-finish","reason","cost","tokens"}`,
  `{"type":"tool","callID","declarationIndex","tool","state"}`.

### `session_entry` — 2 rows on session create

`id, session_id, type, time_created, time_updated, data`.

- `runtime/model_selection`: `data = {"modelSelection": <selection>}`.
- `runtime/execution_state`: `data = {"mode","planEnabled"}`.
- `touchSession: false` for both (no `session.time_updated` bump).
- The model-selection upsert has a special `json_set(data,'$.modelSelection',
...)` branch on conflict.

### `session_input` — 1 row

`id, session_id, kind, delivery, payload, admitted_sequence, promoted_sequence,
promoted_message_id, status, status_reason, time_created, time_updated`.

- `admitted_sequence` = `max(admitted_sequence)+1` within the session; a
  re-admission with the same id keeps the sequence and status (only kind,
  delivery, payload are updated).
- Promotion sets `status='promoted'`, `promoted_message_id` and
  `promoted_sequence = max(promoted_sequence)+1` in the **same transaction** as
  the user message/parts.
- `payload` is the ordered JSON `{text, intent, conversationInputIntent,
attachments, sourceCommandType}` (key order preserved on queue edit).

### `input_history` — 1 row

`id, project_id, session_id, text, attachments, kind, time_created`.

- `id` = `input_<base36(now)>_<uuid>` — the one **excluded** column (Node uses
  `crypto.randomUUID()`); Go asserts the prefix/format instead.
- A duplicate of the newest text for the same project is skipped (the call
  returns no id).
- Only the newest 100 rows are kept.

### `model_usage` — 1 row (2+ when retried)

40 columns; see migration `0010`. Notes:

- `computed_total_tokens` = `inputTokens` (or cache creation+read when input is
  zero) + `outputTokens` when not supplied.
- `raw_usage_json` / `provider_metadata_json` are the raw provider JSON.
- `variant` carries `reasoningLevel` (legacy column name).
- Every write prunes rows with `started_at < now - 30 days`.

### `turn_usage` — 1 row (PK `session_id, turn_id`)

26 columns; upsert semantics per `upsertTurnUsage` (e.g. `started_at = min(...)`,
`trace_id/user_message_id/error_*` via `coalesce`).

### `tool_usage` — 1 row per tool call (unique `session_id, tool_call_id`)

27 columns; a terminal row is not downgraded by a later `running` event;
`output_bytes/stdout_bytes/stderr_bytes/truncated` take the max.

## How it is pinned (differential testing)

1. `scripts/go-rewrite/gen-store-vectors.mts` drives the **real** Node
   repositories (`sessions.ts`, `messages.ts`, `session-entries.ts`,
   `session-inputs.ts`, `usage.ts`, `input-history.ts`) with a fixed scripted
   sequence and a fixed clock (`Date.now` overridden to a constant), then dumps
   every row to `go/testdata/vectors/store-scenarios.json`. Scenarios:
   `text-turn`, `tool-turn`, `retry`, `denied`, `admit-settle-cancelled`,
   `queue-edit`.
2. `go/internal/adapters/store/sessions_scenarios_test.go` drives the Go adapter
   with the same inputs and requires column-by-column equality for every table.
   The only excluded column is `input_history.id`, listed in the vector file's
   `excludedColumns`.
3. `scripts/go-rewrite/crosscheck-sessions.mjs` runs both directions against the
   real `SqliteSessionStore` after a reopen: Node writes → Go `ListMessages`
   reads (equal to Node's own stored rows), and Go writes → Node's real store
   reads.

Regenerate vectors:

```bash
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-store-vectors.mts
```

Run the crosscheck:

```bash
node scripts/go-rewrite/crosscheck-sessions.mjs
```

## Not yet ported / out of scope

- **Fork / shared-context** writes in `promoteSessionInput` (the
  `v4/shared_context_import` branch). The plain promote path is ported.
- `updateSession` beyond the title branch (summary, revert, permission, archive,
  path self-heal, `repair*` helpers).
- `todos` (`updateTodos`/`readTodos`) is implemented but not exercised by a
  differential vector; only a unit path exists.
- `permission`, `local_setting`, `session_target*`, workflow
  (`workflow_*`, `session_task_link`), `dwf_*`, and debug counts are not ported.
- Usage **queries** (`queryAppUsage`, `queryTaskUsage`) and the query-based
  baseline arithmetic are not ported; only the write path plus `pruneUsage`.
- `removeMessage`, `removePart`, `markSessionInputPromoted` (ported but only
  reachable via settle flows), copy-from legacy snapshots, and the
  `preserveLegacyMembers` conflict-preservation SQL are simplified: the Go
  upsert keeps the same row data but does not re-merge old flat fields
  (`model`, `providerID`, …) into an existing row.
- The retry scenario records two `model_usage` rows but the vectors do not cover
  `first_token_at` on a failed first attempt beyond what is stored.
- Multi-turn history (assistant tool call + tool result messages feeding the next
  request) is a **model-layer** concern, not storage; the storage side accepts
  arbitrary messages/parts.

## Honest limitations

- The vectors fix the clock to a constant in the future so that `pruneUsage`
  (30-day window) does not delete the rows. A fixed **past** clock would prune
  them — that behaviour is real and is separately tested
  (`TestUsagePrunesOldRows`).
- `gen-store-vectors.mts` overrides the global `Date.now`; it does **not** stub
  `crypto.randomUUID`, hence the single excluded column.
- The `internal/emitters` and `internal/projection` packages briefly failed in
  one full run because a parallel engineer was editing them at the time; the
  final `go test -race ./...` run is green, and they do not import the
  session-store packages.
