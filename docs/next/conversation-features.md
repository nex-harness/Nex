# Conversation features (batch 2) — spec

Status: planned on `feature/next`. Research with citations:
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

## Batch 2a — implemented (row identity, history, revision CAS, availability)

Status: implemented in `packages/next-runner`. It is the shared prerequisite for every
other batch; nothing here changes the wire schema.

- **Row identity.** `entityId = pi:<entryId>` (`pi:<entry>:text:<n>` /
  `:thinking:<n>` for segments, `pi:tool:<toolCallId>`, `pi:turn:<entry>` for the
  turnHeader). Live rows learn the entry id from a new engine event
  `message_entry{entryId, role}`, emitted from a microtask after `message_end` because pi
  persists messages *after* notifying subscribers and never emits `entry_appended` for
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
- **Availability / routing.** `fork`, `compact`, `queueEdit` and `sendQueuedNow` are denied
  (`guard.actionUnavailable`, or `sendQueuedNowRequiresRunning` when idle); goal commands
  are denied (`noGoalToPause` / `noGoalToResume`); `switchModelConfig` and
  `setFollowupMode` stay allowed. `inputRouting` is `startNow` when idle,
  `enqueue`/`guide` by `followupMode` while running, and never `choice` (held queue is
  batch 2c).
- **Not served yet.** `editUserQuery`, `retryTurn`, `forkAssistant` remain `rejected`
  (`unsupported_command`); the affordances render so the later batches only add handlers.

## Acceptance

- Unit tests per feature; every emitted snapshot/ACK validated with the real zod schemas.
- Real pi + faux provider integration for queue drain, compact, edit, fork.
- Browser E2E `next/e2e/conversation.mjs` (`next/dev.sh --e2e=conversation`, included in
  `all`): queue two messages while a reply streams → both drain in order; delete and
  reorder a queued item; `/compact` shows a compact marker; edit the last message → old
  turn replaced; fork a reply → new chat opens with the history; title becomes generated.
