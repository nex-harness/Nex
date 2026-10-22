# Conversation features (batch 2) — spec

Status: batches 2a, 2b, 2c and 2d implemented on `feature/next`; generated titles (2b)
and mid-session config changes (2b) are in as well. Research with citations:
`conversation-features-research.md` (section numbers below refer to it).

Scope: compact, fork a reply, retry, edit a sent message, the queue (busy-send routing
and queue controls), generated titles, mid-session model / follow-up mode changes.

## Decisions

1. **Rows get identity and actions first (§0).** Every row carries `entityId`
   (`pi:<entryId>`, stable across restores), `productTurnId = turnId`,
   `visibility:"visible"`, and `actions` computed like the old CLI: edit only on the
   newest real-user row, fork only on the last text segment of a successful turn, retry
   only on the newest complete assistant reply. Without this the UI hides all actions.
2. **History is rebuilt from pi's raw branch** (`sessionManager.getBranch()`), projecting
   message, compaction and branch-summary entries, so compaction markers and pre-compaction
   rows survive restore. `session.messages` (the model context) is not the transcript.
3. **Real revision CAS.** `snapshot.revision` increments on every content change. Commands
   that carry `baseRevision` and target rows are rejected `stale` when it doesn't match.
4. **The runner owns the queue (§5).** pi's follow-up queue cannot remove or reorder items,
   so queued text lives in a runner queue fed to pi one item at a time. Guide messages
   (send while running, follow-up mode `guide`) go to `session.steer()` and stay visible as
   pending guide items until pi consumes them. `inputRouting` is computed like the old
   CLI's `computeInputRouting`. Drain is single-flight: on `agent_settled` + idle +
   `autoDrain`, the head item is promoted. Stop/error pause the queue (`autoDrain=false`
   with `pauseReason`).
5. **Rewind primitive** shared by edit and retry: abort if running, `branch()` to the
   parent of the target user entry, `refreshContext()`, emit `row.removed` from that turn's
   header, then prompt again in the same session. Row ids stay monotonic (never reused).
   Edit with `workspaceMode:"rewind"` (restore files) is answered `blocked` with
   `guard.workspaceRewindUnavailable` until batch 3 adds file checkpoints.
6. **Fork** copies the conversation up to the target into a new pi session file
   (`createBranchedSession` on a second `SessionManager`), records a `nex.fork` custom
   entry for the fork notice, titles it `Fork of <parent>` (`generated`), and returns
   `{type:"forkAssistant", sessionId}`. The parent is never touched.
7. **Compact** maps to `session.compact()`, with a `compact` timeline marker row (running
   → success / noop / cancelled / failed) and `activeWorks` entry. Busy → queued compact
   item; second compact → `compactOperationLock`; Stop cancels it.
8. **Titles**: after the first prompt (≥10 chars, never over a user-set title), call the
   session's model (or a configured cheap model) with the old CLI's title prompt, store as
   `titleSource:"generated"` in runner metadata and pi's `session_info`. Fire-and-forget,
   never blocks the turn.
9. **Mid-session model change**: `switchModelConfig` → `setModel`/`setThinkingLevel` plus a
   `modelChange` marker; `setFollowupMode` → metadata + `inputRouting` recompute.

## Ownership

| State                                      | Owner (single writer)                           |
| ------------------------------------------ | ----------------------------------------------- |
| transcript, branches, compaction entries   | pi session file (via engine)                    |
| queue items, autoDrain, pauseReason        | runner session runtime                          |
| title, titleSource, followupMode, model    | runner metadata store                           |
| rows, availability, inputRouting, revision | projection (derived; never written by commands) |

## Event order: send while busy (queue mode)

```
UI sendText ─▶ runner: routing = enqueue ─▶ queue.push(item) ─▶ ACK inputAccepted{queue}
             snapshot: queue.items += item, inputRouting=enqueue
pi agent_settled ─▶ drain: item reserved → promoting → prompt(item.text)
             snapshot: queue.items -= item, new turn rows
```

## Batch 2b — implemented (titles, mid-session model / follow-up mode)

Status: implemented (see the README). Generated titles (decision 8) and
`switchModelConfig` / `setFollowupMode` (decision 9) are served with real revision CAS; the
`modelChange` marker rides the same fold as batch 2d's compact markers.

## Batch 2c — implemented (runner-owned queue)

Status: implemented in `packages/next-runner`.

- **Owner.** `v4/queue.ts` (`SessionQueue`) is the single writer of queued text for a
  session: items, `autoDrain`, `pauseReason`, admission order and dispatch state. It is
  created with the session runtime (`v4/sessionStore.ts`) and lives in memory. The
  projection's `queue` field is a cache written **only** through the queue's `commit` port;
  the fold never touches it and commands read/write through the owner.
- **Item shape.** Full `queueItemSchema` (`input-intent.ts:41-62`): `sourceCommandId`
  (= the originating command), `queueItemId`, `clientId`, `kind`, `text`, `attachments`,
  frozen `modelSelection`/`mode`/`planEnabled`, `delivery{requested,admitted,fallbackReasonCode}`,
  `order{admissionSeq,queuePosition}`, `steer{state}`, `dispatch{state,reservationId}`
  (`queued|reserved|promoting`), `admittedAt`, `provenance`.
- **Admission (`sendText`).** Idle → `engine.submit` now, ACK `inputAccepted{startNow}`
  (a requested `queue`/`guide` also starts now: there is no run to join). Running +
  `followupMode:"guide"` → `session.steer()` and a pending guide item
  (`delivery.admitted:"guide"`), ACK `guide`; the item is dropped when pi's `queue_update`
  no longer lists its text. Running + queue mode → append, ACK `queue`.
  `requestedDelivery:"startNow"` while busy aborts, waits for idle, then starts. A steer pi
  refuses is kept as a queued item with `fallbackReasonCode:"guide.notSteerable"`.
- **Held queue.** `inputRouting.mode:"choice"` (completed + queued items + `autoDrain=false`)
  requires `heldQueueDisposition`; a missing one is
  `heldQueueDispositionRequired`, and `expectedHeldQueueItemIds` that no longer match is
  `guard.heldQueueConfirmationStale`. `clearQueueAndSend` empties the queue before starting;
  `keepQueueAndSend` starts while leaving the items.
- **Drain.** Single-flight: `settled` (not aborted, phase ≠ `error`) with `autoDrain` and an
  idle engine promotes exactly one head item (`reserved` → `promoting` → `submit` → remove).
  A failed submit rolls the item back to `queued` in place. Stop pauses
  (`autoDrain=false`, `pauseReason:"stopped"`); a failed turn pauses with `"error"`.
  `setAutoDrain(true)` re-arms and drains immediately when idle. Guide items are never the
  drain head; one still queued in pi at settle is cleared from pi and re-queued visibly
  (`fallbackReasonCode:"guide.notDelivered"`).
- **Commands.** `deleteQueueItem` (missing → `noop queue.itemMissing`), `reorderQueueItem`
  (`beforeQueueItemId:null` = tail; unknown anchor no-ops), `editQueueItem` (protocol-only;
  the UI restores the composer by deleting), `sendQueuedNow` (abort → idle barrier →
  reserve/promote that item), `setAutoDrain`. All five carry `baseRevision` and go through
  the batch-2a CAS gate.
- **Snapshot.** `queue.items`/`autoDrain`/`pauseReason` come from the owner; `inputRouting`
  is the full `computeInputRouting` table including `choice`; `availability.queueEdit` is
  allowed and `sendQueuedNow` is allowed only while running.
- **Persistence: none (deliberate).** The old CLI's queue is explicitly not persisted
  (`snapshot.ts` "queue（不持久化）"), so queued items live only in the runner process. A
  runner restart drops them and the UI sees the queue disappear; the user resends. Nothing
  is written to the pi session file for queued text.
- **Never pi's follow-up queue.** Queued text is only ever submitted by the runner; pi's
  `followUp` channel is unused, so no item can become unobservable or unreorderable.

## Batch 2d — implemented (compact, edit/retry rewind, fork)

Status: implemented in `packages/next-runner`.

- **Compact (§1, decision 7).** `compact` is not CAS (an input command; the command id is
  the idempotency boundary). Admission lives in the queue (`SessionQueue.admitCompact`):
  a running or queued compaction → `rejected compactOperationLock`; busy → a
  `kind:"compact"` item joined to the same FIFO as user text and ACKed `accepted`; idle →
  started now. `v4/compactOwner.ts` is the single writer of the marker lifecycle: it
  creates the `running` row before calling `engine.compact()` (so the UI shimmers), then
  upserts it `success` / `noop` / `cancelled` / `failed` with `tokensBefore`/`tokensAfter`.
  pi's `compaction_start`/`compaction_end` are translated (`compact_start`/`compact_end`);
  `reason:"threshold"|"overflow"` becomes an `origin:"auto"` marker through the fold, so an
  automatic compaction is visible exactly like a manual one. Stop → `session.abort()` also
  cancels compaction (`canStop` is true while only a compaction runs); a failed/cancelled
  manual compaction pauses a non-empty queue (`pauseReason:"error"`). `activeWorks` carries
  `kind:"compact"` (alongside `primaryTurn` while a turn runs). Restore reads the pi entry
  and the metadata row's `lastCompact {entryId, sourceCommandId}` to keep `origin:"manual"`
  and lineage. `availability.compact` is allowed unless a compact is running/queued
  (`compactOperationLock`); the old CLI's `idleCannotCompact` is never advertised (an empty
  session answers `noop`).
- **Rewind primitive (decision 5).** `v4/rewindCommands.ts` implements edit and retry on
  one path: guard → cut the live rows from the target turn's header (`rewindProjection.ts`;
  rowIds stay monotonic) → `engine.rewind` (`piRewindOps.ts`: abort + `waitForIdle` if
  running, `sessionManager.branch(parentOf(userEntry))` — `resetLeaf()` for the first
  message — `refreshContext()`, then `prompt(newText)` in the same session) → ACK. Snapshots
  are projections of the row set, so dropping rows _is_ the removal; restore agrees because
  `getBranch()` walks from the branch leaf.
- **`editUserQuery` (§4).** Guards: empty `newText` → `proto.invalidPayload`; a non-empty
  `attachments` list → `guard.attachmentsUnsupported` (attachments land in batch 3; omitted
  = keep and `[]` = none are accepted); target must be the newest real-user row
  (`guard.latestQueryEditOnly`, the same rule that renders the edit entry); unresolved
  target → `guard.rewindTargetUnresolved`. `workspaceMode:"rewind"` → `accepted` with
  `{type:"editUserQuery", disposition:"blocked", reasonCode:"guard.workspaceRewindUnavailable",
preview}` (an empty, valid preview: `canApply:false`, no files). Otherwise
  `{type:"editUserQuery", disposition:"rewind", sessionId}`. The rewritten turn keeps the
  original prompt's `sourceCommandId` lineage (turnHeader + userInput row).
- **`retryTurn` (§3).** Same primitive with the original user text, resolved _before_ the
  cut from pi (`engine.userMessageBefore`: the nearest user entry at or before the assistant
  entry on the active branch). Guard: newest complete assistant only
  (`guard.latestAssistantRetryOnly`, exactly `actions.canRetry`). ACK is a plain `accepted`
  (the old CLI returns no result); the UI keeps hiding the retry entry.
- **`forkAssistant` (§2, decision 6).** Guards: `actions.canFork` (last text segment of a
  `completedSuccess` turn) else `guard.forkTargetNotStable`; compact running → the lock.
  `piForkOps.ts` opens a **second** `SessionManager` on the parent file and calls
  `createBranchedSession(targetEntryId)` → a new session file (new id, `parentSession`
  header); the `nex.fork {parentSessionId,parentRowId}` custom entry is written through that
  file's manager, then the child opens its own `AgentSession`, inheriting mode/thinking and
  titled `Fork of <parent>` (`titleSource:"generated"`). It is registered in the engine,
  metadata (so the sessions index/sidebar include it) and fires `sessionsChanged`. ACK
  `{type:"forkAssistant", sessionId}`. The parent file is never written: the integration
  test asserts byte equality. Restore turns the custom entry into a head `forkNotice`
  marker (`lane:"lightBoundary"`, `entityId:"pi:marker:<entryId>"`).
- **Availability.** `fork` and `compact` are real; `guard.actionUnavailable` is no longer
  used by this runner.
- **Not served yet.** `applyFileRewind`, `setAssistantFeedback` and the goal commands remain
  `rejected`/denied; there is no file checkpoint for `workspaceMode:"rewind"`.

## Batch 2a — implemented (row identity, history, revision CAS, availability)

Status: implemented in `packages/next-runner`. It is the shared prerequisite for every
other batch; nothing here changes the wire schema.

- **Row identity.** `entityId = pi:<entryId>` (`pi:<entry>:text:<n>` /
  `:thinking:<n>` for segments, `pi:tool:<toolCallId>`, `pi:turn:<entry>` for the
  turnHeader). Live rows learn the entry id from a new engine event
  `message_entry{entryId, role}`, emitted from a microtask after `message_end` because pi
  persists messages _after_ notifying subscribers and never emits `entry_appended` for
  them. Restore derives the same ids from `SessionManager.getBranch()`, so an action target
  survives a restart; `productTurnId = turnId` and `visibility:"visible"` on every row.
  Turn numbering (`turn-<n>`, header ordinal) is shared by both paths — it is the UI's
  render-unit key.
- **Actions.** `projection/rowActions.ts` is a pure function over the row set, ported from
  `materializeCommandRowActions`; `entityId` presence replaces the old CLI's message-id
  table check.
- **History.** `Engine.history()` returns the raw branch: messages keep their entry ids, a
  `compaction` entry becomes a `timelineMarker` compact row (lane `assistantWork`, status
  `success`, `origin` from `fromHook`, `tokensBefore`), `branch_summary` is skipped.
- **Revision CAS.** `revision` follows `deltaBumpsRevision`: row structure and
  meta/config/queue/interaction changes count, append-only text growth does not. Commands
  in `COMMANDS_REQUIRING_BASE_REVISION` are compared after hydration and answered
  `stale` (`proto.staleRevision` / `proto.staleLogEpoch`) with the real
  `revisionAtDecision`; the UI retries config commands with that value.
- **Availability / routing.** (Batch 2a originally denied `fork`/`compact` with
  `guard.actionUnavailable`; batch 2d makes both real — see above.) Goal commands are denied
  (`noGoalToPause` / `noGoalToResume`); `switchModelConfig`, `setFollowupMode`, `queueEdit`
  and `sendQueuedNow` (while running) are allowed.
  `inputRouting` is `startNow` when idle, `enqueue`/`guide` by `followupMode` while running.
- **Not served yet (as of 2a).** `editUserQuery`, `retryTurn`, `forkAssistant` were
  `rejected` (`unsupported_command`) until batch 2d added their handlers.

## Acceptance

- Unit tests per feature; every emitted snapshot/ACK validated with the real zod schemas.
- Real pi + faux provider integration for queue drain, compact, edit, fork.
- **E2E coverage today (batch 2a + 2c + 2d).** `next/e2e/conversation.mjs` covers the queue
  section (busy turn, three queued messages, drag-reorder, delete, drain order, Stop →
  paused banner → Resume → drain) and the batch 2d sections: `/compact` shows a marker
  (`Context compressed` or `Context is up to date` for a short session) and chat still
  works; the user row's inline editor rewinds the last turn so the old reply disappears and
  the new one replaces it; forking an assistant reply opens the child with the history plus
  a "Forked from conversation" notice and adds a sidebar row.
