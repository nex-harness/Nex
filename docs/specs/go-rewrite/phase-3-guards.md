# Phase 3 — Guard inventory (step 3.10.5)

Part of [the Go rewrite plan](README.md). Companion to
[phase-3-services.md](phase-3-services.md) step 3.10.5.

## Why this file exists

Step 3.10.5 ports `packages/services/src/nex-agent/nexTaskServiceAdapter.ts` (5 659 lines). The
plan's rule for this step is not "port the methods" but:

> **AGENTS.md: "保留 owner/lease、跨 Host 路由和 stale run 防护，不能仅根据单一路径删除边界判断."**
> Every guard stays unless a test proves it unreachable. Method: first write a guard inventory:
> every `if` that returns early on staleness/ownership, with the TS line and a scenario that
> triggers it; then port; each guard row needs a Go test id. No guard may be dropped without a
> recorded decision.

So this file is written **before** the Go code, it enumerates every early-return that exists to
protect ownership / staleness / routing / queue integrity, and each row names the Go test that
pins it. A row whose test does not exist is a defect in this step.

## Scope of the guard set

Two TS files hold the guard rules the step is about:

| TS file                                                    | Lines  | Role                                                                                                                   |
| ---------------------------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------- |
| `packages/services/src/nex-agent/nexTaskServiceAdapter.ts` | 5 659  | the service adapter itself: active-run bookkeeping, the host command queue, the drain, the pending-emission boundaries |
| `packages/desktop/src/main/taskRealtimeBus.ts`             | ~1 100 | the **cross-host** half: task run leases, owner-command routing, stream-mirror batch ownership                         |

The adapter holds the _local_ half of ownership (`activePromptInputIds` = "which input run owns
this task right now"); the bus holds the _cross-host_ half (`TaskRunLease` = "which Host may
publish this task's run, and which Host must answer owner commands"). AGENTS.md requires both to
survive, so step 3.10.5 owns both halves in Go: the adapter's own state machine in
`go/internal/services/taskadapter/`, and the lease/routing table in the same package because the
two are one decision (`assertCurrentOwnerRun` on the adapter side is the leaf check of the bus's
`lease.runId !== command.runId` on the bus side).

## Guard rows

`TS line` is the line in the file named in the section. `Go test` is the test id in
`go/internal/services/taskadapter/` that pins the row; `G3.taskadapter` runs them all.

### A. Owner / lease — local ownership (adapter)

| #   | Guard                                                                                         | TS line    | Scenario that triggers it                                                                                                                         | Go test                                   |
| --- | --------------------------------------------------------------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------- |
| A1  | `assertCurrentOwnerRun` returns **without checking** when the command carries no `ownerRunId` | 352–354    | Desktop-continuous `enqueueTaskCommand` has no `ownerRunId` (only the phone replayable leg sets one), so the local host never refuses its own UI. | `TestOwnerGuard_NoOwnerRunIDPasses`       |
| A2  | `!activeRunId` → `NO_ACTIVE_TASK_OWNER`                                                       | 357–360    | A phone owner command (enqueue/promote/cancel) arrives after the turn already closed: `activePromptInputIds` was deleted by turn.completed.       | `TestOwnerGuard_NoActiveOwnerRefused`     |
| A3  | `activeRunId !== ownerRunId` → `STALE_TASK_OWNER_COMMAND`                                     | 362–366    | The phone sent an enqueue for run A; the desktop started run B; the enqueue lands late and would otherwise mutate B's queue.                      | `TestOwnerGuard_StaleRunRefused`          |
| A4  | `getTaskTarget` throws `NEX_SESSION_TARGET_NOT_FOUND`                                         | 975–983    | `sendPrompt`/`closeTask`/`getTaskConfigOptions` name a task this host never created or resumed (a different window owns it).                      | `TestOwnerGuard_UnknownTaskRefused`       |
| A5  | `rememberTaskTarget` keeps the sticky `cronAutomationId` when the new params omit it          | 964–973    | `resumeTask`/`onDynamicTaskEvent` carry no `cronAutomationId`; without the merge the task silently loses its automation attribution.              | `TestOwnerGuard_CronAutomationIDIsSticky` |
| A6  | `disposed` short-circuits `handleTaskIndexTerminalEvent` / `handleTaskIndexReadyEvent`        | 1686, 1714 | A terminal/ready event queued as a microtask lands after `disposeAllAndWait`; it must not touch released state.                                   | `TestDispose_QueuedEventsAreInert`        |
| A7  | `disposeAllAndWait` is idempotent (`if (disposed) return`)                                    | 3123–3126  | Dispose is called twice on app quit (host exit + window teardown); the second call must not re-run the agent-service teardown.                    | `TestDispose_Idempotent`                  |

### B. Owner / lease — cross-host routing (taskRealtimeBus)

| #   | Guard                                                                                                            | TS line (bus) | Scenario that triggers it                                                                                                                           | Go test                                       |
| --- | ---------------------------------------------------------------------------------------------------------------- | ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| B1  | `acquireLease`: an existing lease owned by **another host or another run** refuses acquisition                   | 320–327       | Host A holds the lease for (ws, task, run A). Host B asks for the same task → `acquired:false, owned_by_other_host`; two Desktop windows, one task. | `TestLease_HandoverRefusesOtherHost`          |
| B2  | `acquireLease` for the **same host + same run** refreshes in place and keeps `acquiredAt`                        | 329–343       | A reconnect/retry from the owning host must not lose the lease nor reset `acquiredAt` (it is the lease's identity timestamp).                       | `TestLease_ReacquireSameRunKeepsAcquiredAt`   |
| B3  | `acquireLease` for the same host but a **different run** is refused (run id is part of ownership)                | 320           | The same host starts run B for the same task while run A is still recorded; the stale run must not silently take over the record.                   | `TestLease_NewRunOnSameHostRefused`           |
| B4  | `releaseLease` ignores a lease that is not `(hostId, runId)`-owned                                               | 362–367       | A late release from the previous run (or from a host that lost the lease) must not free the **current** owner's lease.                              | `TestLease_ReleaseByNonOwnerIgnored`          |
| B5  | `releaseLeasesForHost` only releases that host's leases                                                          | 378–389       | A window closes; every lease it held is flushed and released, and no other host's lease is touched.                                                 | `TestLease_DisposeReleasesOnlyOwnLeases`      |
| B6  | `handleOwnerCommandRequest`: no lease → `NO_ACTIVE_TASK_OWNER` (to the **requester**)                            | 847–855       | The phone sends `enqueue_task_command` after the run ended; the bus answers the requester without any host call.                                    | `TestRoute_NoLeaseRefusesRequester`           |
| B7  | `handleOwnerCommandRequest`: `lease.runId !== command.runId` → `STALE_TASK_OWNER_COMMAND`                        | 857–865       | The phone's command names run A while the lease is run B; nothing is delivered to the owner host.                                                   | `TestRoute_StaleRunRefused`                   |
| B8  | `handleOwnerCommandRequest`: lease exists but its `ownerHostId` host is **gone** → `NO_ACTIVE_TASK_OWNER`        | 866–875       | The owning window crashed between acquire and command; the requester gets the honest refusal instead of a timeout.                                  | `TestRoute_DeadOwnerHostRefuses`              |
| B9  | `handleOwnerCommandRequest` registers the pending command with a **timeout** that answers `OWNER_COMMAND_FAILED` | 877–893       | The owner host never answers; the requester is not left hanging forever.                                                                            | `TestRoute_OwnerTimeoutFails`                 |
| B10 | `handleOwnerCommandResult`: a result whose `ownerHostId` is not the pending owner is **dropped**                 | 900–907       | A second host (or the requester) posts a result for the same `commandRequestId`; only the owner may answer.                                         | `TestRoute_ForeignResultDropped`              |
| B11 | `handleStreamOpPublish`: no lease, foreign `ownerHostId`, or foreign `runId` → the op is **dropped**             | 396–399       | Two windows both mirror a run; the non-owner must not publish into the shared stream.                                                               | `TestStreamMirror_NonOwnerOpDropped`          |
| B12 | `handleStreamOpPublish` **deletes the lease** on a terminal stream event                                         | 405–411       | A terminal event (`task_complete`/`task_error`) ends the run; the lease must be released so the next run can acquire it.                            | `TestStreamMirror_TerminalEventReleasesLease` |

### C. Stale-run protection — v4 CAS (nexV4HostCommand.ts)

| #   | Guard                                                                                                             | TS line | Scenario that triggers it                                                                                                         | Go test                                      |
| --- | ----------------------------------------------------------------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- |
| C1  | `createHostCommandEnvelope`: a CAS command **without** `baseRevision` throws                                      | 60–62   | `switchModelConfig`/`switchCollaborationMode` submitted without a base → must fail loudly instead of becoming an unguarded write. | `TestCAS_MissingBaseRevisionRefused`         |
| C2  | `createHostCommandEnvelope`: a row-target command without `baseLogEpoch` throws                                   | 63–65   | Same class for row-targeting commands.                                                                                            | `TestCAS_MissingBaseLogEpochRefused`         |
| C3  | `assertV4CommandAckOk` treats `accepted`/`duplicate`/`noop` as success                                            | 98–100  | A retried `commandId` replays as `duplicate`; that is idempotent success, not a failure.                                          | `TestCAS_DuplicateIsIdempotentSuccess`       |
| C4  | `assertV4CommandAckOk` raises `NEX_V4_COMMAND_REJECTED` for `rejected`/`stale`/`failed`, carrying `reasonCode`    | 100–105 | The model switch lost the race; the caller must branch on the code, not the message text.                                         | `TestCAS_RejectedCarriesReasonCode`          |
| C5  | `sendHostCasCommandV4` retries on `stale`, converging on `ack.revisionAtDecision`                                 | 131–145 | The host has no local v4 projection, so its first `baseRevision=0` is a probe; one stale is the normal path.                      | `TestCAS_StaleConvergesOnRevisionAtDecision` |
| C6  | `sendHostCasCommandV4` uses a **new commandId per attempt** (a stale verdict never enters the idempotency table)  | 136–142 | Otherwise a later retry of the same id would replay the stale verdict forever.                                                    | `TestCAS_StaleRetryUsesNewCommandID`         |
| C7  | `sendHostCasCommandV4` gives up after `maxAttempts` (default 4) and throws                                        | 146–157 | Pathological concurrency: every attempt is stale; the write must fail rather than loop.                                           | `TestCAS_StaleExhaustionThrows`              |
| C8  | A command carrying a stale `baseRevision` **never executes** — the agent answers `stale` and the effect is absent | 100–105 | The core stale-run protection: the stale write is refused at the CLI boundary, and no model change is observable.                 | `TestStaleCommand_NeverExecutes`             |

### D. Queue + drain (adapter host command queue)

| #   | Guard                                                                                            | TS line (adapter) | Scenario that triggers it                                                                                                           | Go test                                        |
| --- | ------------------------------------------------------------------------------------------------ | ----------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------- |
| D1  | `drainRuntimeCommands`: an active prompt input blocks the drain                                  | 594–596           | The task is mid-turn; an accepted command must stay queued rather than sending mid-turn.                                            | `TestDrain_ActiveInputBlocksDrain`             |
| D2  | `drainRuntimeCommands`: any command already `running` blocks the drain                           | 598–600           | A previous drain is in flight; a second accepted command must not start before it finishes (one turn at a time).                    | `TestDrain_RunningCommandBlocksDrain`          |
| D3  | `drainRuntimeCommands`: no `accepted` `send_prompt` command → return                             | 601–607           | Only failed/empty queue → nothing to send.                                                                                          | `TestDrain_NoAcceptedCommandIsNoop`            |
| D4  | `scheduleRuntimeCommandDrain` **serializes** per task key (one drain promise per key)            | 662–684           | enqueue + promote + ready race: the second drain runs after the first settles, and only one drain is in flight.                     | `TestDrain_SerializesPerTask`                  |
| D5  | The drain failure path marks the command `failed` and keeps the queue                            | 633–644           | The agent rejects the send; the command must not silently disappear and must not retry forever.                                     | `TestDrain_FailureMarksCommandFailed`          |
| D6  | `markRuntimeCommandRunning` flips only the matching `commandId`                                  | 495–512           | Two queued commands; draining the first must not flip the second.                                                                   | `TestQueue_MarkRunningIsPerCommand`            |
| D7  | `promoteTaskCommand` moves the command to the **head** and re-arms it                            | 1983–1990         | The phone re-sends a failed/deferred command; promote re-prioritises without changing its id.                                       | `TestQueue_PromoteMovesToHead`                 |
| D8  | `promoteTaskCommand` on a `running` command is a **no-op success**                               | 1980–1982         | A retried promote for a command already in flight must not reset it or double-send.                                                 | `TestQueue_PromoteRunningIsNoop`               |
| D9  | `promoteTaskCommand` on an unknown `commandId` throws `OWNER_COMMAND_FAILED`                     | 1974–1979         | The host queue was flushed (dispose / cancel) while the phone was promoting.                                                        | `TestQueue_PromoteUnknownThrows`               |
| D10 | `cancelTaskCommand` on an unknown `commandId` answers `canceled:true, reason:not_found`          | 1998–2011         | Cancel is idempotent: the phone's delete must not error on an already-gone command.                                                 | `TestQueue_CancelUnknownIsNotFound`            |
| D11 | `cancelTaskCommand` on a `running` command answers `canceled:false, already_running`             | 2012–2026         | Deleting a command that has already started must not stop the send in flight.                                                       | `TestQueue_CancelRunningRefused`               |
| D12 | `cancelTaskCommand` deletes from the **host** queue and emits a snapshot update                  | 2029–2033         | The renderer-only delete would be undone by the next snapshot; the host queue is the fact source.                                   | `TestQueue_CancelRemovesFromHostQueue`         |
| D13 | `enqueueTaskCommand` **appends** (FIFO) rather than prepending                                   | 1963              | Three commands from one client keep submission order.                                                                               | `TestQueue_FIFOWithinClient`                   |
| D14 | `enqueueTaskCommand` with a **retried `commandId`** does not duplicate                           | 1942–1967         | The phone retries after a timeout; the retried command must not become a second queue entry.                                        | `TestQueue_RetriedCommandIDIsIdempotent`       |
| D15 | `completeRuntimeCommandByInputId` only removes a `running` command with a **matching `traceId`** | 555–574           | A terminal event for a different input must not clear the queue.                                                                    | `TestQueue_TerminalOnlyCompletesMatchingInput` |
| D16 | `completeRuntimeCommandByInputId` without a terminal input id returns                            | 560–562           | The v4 sessions-index terminal event carries no `inputId`; only the locally recorded active input may close the queue.              | `TestQueue_NoTerminalInputIsNoop`              |
| D17 | `handleTaskIndexTerminalEvent` returns when neither an active input nor a queue exists           | 1672–1674         | A terminal event for a task this host does not own must not create state.                                                           | `TestTerminal_NoLocalStateIsNoop`              |
| D18 | `handleTaskIndexReadyEvent` returns when neither an active input nor a queue exists              | 1699–1701         | Same for the ready boundary.                                                                                                        | `TestReady_NoLocalStateIsNoop`                 |
| D19 | The ready boundary **schedules the next drain** (it does not send immediately)                   | 1708              | The agent releases its abort controller at `prompt_completed`; the next queued command is sent from there, not on a fixed delay.    | `TestReady_SchedulesNextDrain`                 |
| D20 | `handleTaskIndexTerminalEvent` deletes the active input but does **not** drain                   | 1678–1681         | Turn terminal ≠ agent lock released; sending the next command here would race the CLI's active lock.                                | `TestTerminal_DoesNotDrain`                    |
| D21 | `activePromptInputIds` is set on `turn.started` payload `inputId`                                | 1628–1635         | A remote/replayable subscription is not started by `sendPrompt`, so `turn.started` is the protocol fact that establishes ownership. | `TestOwnership_TurnStartedClaimsInput`         |
| D22 | `activePromptInputIds` is cleared on `turn.completed`/`turn.failed`                              | 1658–1660         | Otherwise the task would stay "owned" forever and refuse every later phone command.                                                 | `TestOwnership_TurnTerminalReleasesInput`      |
| D23 | `sendPrompt` failure clears the active input id                                                  | 470–483           | A failed send must not leave a phantom owner run that blocks every later command.                                                   | `TestOwnership_SendFailureReleasesInput`       |

### E. Pending optimistic state boundaries

| #   | Guard                                                                                                     | TS line (adapter) | Scenario that triggers it                                                                              | Go test                                          |
| --- | --------------------------------------------------------------------------------------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------ | ------------------------------------------------ |
| E1  | `sendPromptToAgent` clears live-tool projection and the streaming-input cache **before** claiming the run | 386–394           | A new input must not inherit the previous round's live-only child tools into its snapshot.             | `TestPending_NewInputClearsLiveProjection`       |
| E2  | The snapshot's `pendingCommands` is the **host queue**, never the renderer's local store                  | 1572              | On phone refresh the host queue is the only fact source; a renderer-only pending list would be lost.   | `TestPending_SnapshotReadsHostQueue`             |
| E3  | `pendingElicitations` is included **only** when non-empty or when the caller is replayable                | 1569–1571         | desktop-continuous must not receive an "empty set" that would clear live prompts; replayable needs it. | `TestPending_EmptyElicitationsOnlyForReplayable` |
| E4  | `emitTaskEvent` fires **both** the task-keyed and the taskId-global emitter                               | 1044–1047         | The task view and the workspace list subscribe differently; dropping one loses events.                 | `TestPending_TaskEventFansOutToBothEmitters`     |
| E5  | `getLiveToolProjectionMap` evicts the **oldest** task beyond `MAX_LIVE_TOOL_PROJECTION_TASKS`             | 686–699           | The projection table is a bounded cache; unbounded growth is a memory leak on a long-lived host.       | `TestPending_LiveProjectionIsBounded`            |
| E6  | `trimLiveToolProjectionMap` evicts the **lowest order** tool beyond the per-task budget                   | 701–715           | Per task, the oldest tool goes first — a live projection must not drop the newest tool being streamed. | `TestPending_PerTaskTrimKeepsNewest`             |
| E7  | `cancelTaskCommand` on success emits `task_snapshot_updated`                                              | 2033              | Other observers of the task (and the pending list) must observe the removal.                           | `TestPending_CancelEmitsSnapshotUpdate`          |
| E8  | `cancelTaskCommand` on a `running` command does **not** emit a snapshot update                            | 2012–2026         | Nothing changed, so a snapshot update would be a lie the UI has to reconcile.                          | `TestPending_CancelRunningNoSnapshotUpdate`      |

## Step-level scenarios

The rows above pin individual guards. These tests exercise the whole guard set the way
the product hits it, and are part of `G3.taskadapter` for the same reason.

| Test                                              | What it proves                                                                                                                                                                                                                   |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestScenario_OwnerLeaseHandover_TwoHostsOneTask` | Two hosts, one task: host B is refused the lease host A holds; a phone's owner command reaches host A and only host A may answer it; after host A closes, host B takes over and the OLD run becomes stale against the new lease. |
| `TestScenario_StaleCommandNeverExecutes`          | A command naming a dead run is refused before the queue, so nothing stale ever reaches the agent; the CLI boundary refuses a stale `baseRevision` too, so the write does not execute.                                            |
| `TestScenario_QueueFIFO_NoLossNoDuplicate`        | 50 commands from one client drain in submission order under three concurrent drains: none lost, none sent twice.                                                                                                                 |
| `TestScenario_RetriedCommandIDUnderConcurrency`   | 32 concurrent retries of one `commandId` produce one queue entry and one send.                                                                                                                                                   |
| `TestGoroutineLeak_Delta`                         | 20 build/use/dispose rounds with a pending owner-command timer outstanding at dispose leave no goroutine behind.                                                                                                                 |

Supporting cases that pin a rule the rows depend on but do not name separately:

| Test                                     | Why                                                                                  |
| ---------------------------------------- | ------------------------------------------------------------------------------------ |
| `TestOwnerGuard_CurrentRunAccepted`      | A3's boundary is a STALENESS check, not a lockout: the current run must be accepted. |
| `TestLease_HandoverSucceedsAfterRelease` | B1's refusal is about a HELD lease: once released, another host takes the same task. |
| `TestRoute_OwnerRunReachesLeaseOwner`    | B7's boundary from the other side: the matching run IS delivered, and only once.     |

## Decisions on guards

Recorded per the rule "no guard may be dropped without a recorded decision".

| Guard(s) | Decision                                                                                                                                                                                                                                                                                                                               |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A4       | **Kept.** A second window may name a task this host never loaded; the honest refusal is the only thing that keeps the wrong host from driving a foreign session.                                                                                                                                                                       |
| E5/E6    | **Kept** as bounds, not as correctness guards: they cap memory. The budgets are constants in the TS; they stay constants in Go.                                                                                                                                                                                                        |
| B5       | **Kept** and extended to dispose: `disposeAllAndWait` must release the host's leases, otherwise a restarted host's task could never acquire one.                                                                                                                                                                                       |
| A7       | **Kept** (idempotent dispose) because `releaseWorkspacePreparation` (A: 1756–1760) and app quit both reach dispose from different call sites.                                                                                                                                                                                          |
| —        | The **projection** members (`limitTaskSnapshotMessages`, `parseModelPickerValue`, `toNexMode`, synthetic-timeline helpers) are pure functions of a snapshot, with no ownership, queue or staleness decision. They are out of this step's guard set and land with the task bridge (3.10.9), which needs the snapshot projection anyway. |

## Why the cross-host half lives in this package

`taskRealtimeBus.ts` is desktop-main code, not `packages/services`. It is ported here because the
step's scope is owner/lease + cross-host routing, and because B6–B10 and A2–A3 are the **same
decision seen from two ends**: the bus refuses a stale cross-host command, and the adapter refuses
a stale local owner command. Splitting them across two Go packages would recreate the exact
"duplicate state, two write paths" arrangement AGENTS.md forbids. `go/internal/services/taskadapter/`
therefore owns:

- `lease.go` — the `TaskRunLease` table and its acquire/release/timeout rules (B1–B5, B9, B12).
- `route.go` — the pending owner-command table and its routing/result rules (B6–B10).
- `owner.go` — the adapter's local owner-run check (A1–A3).
- `queue.go` — the host command queue and the drain (D1–D23).
- `pending.go` — the pending optimistic state boundaries (E1–E8).

## Step 3.10.6 — window controller (`G3.windowcontroller`)

`go/internal/services/windowcontroller/` ports `IWindowControllerService` and the server variant in
`packages/server/src/windowController.ts`. The guard set is small because the controller's whole
job is a projection with two topics and one attachment per connection; each row below is a rule
whose violation the sidebar (not a crash) would show.

| Guard | Rule                                                                                                                                                                                                             | Test                                                                                              |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| W1    | The two topics have **independent** cursors: distinct `logEpoch`, separate `seq`. A consumer of one must never be told its cursor is current on the other.                                                       | `TestTopicsHaveIndependentCursors`                                                                |
| W2    | Subscribe delivers the **snapshot before the ack**. A UI that applied the ack first would render an empty list and then correct itself.                                                                          | `TestSubscribeDeliversSnapshotBeforeAck`                                                          |
| W3    | An **empty delta batch publishes nothing and does not advance the cursor**. Advancing would make the next real delta carry a `fromSeq` the consumer never saw — a gap that forces a resync.                      | `TestEmptyDeltaBatchPublishesNothing`                                                             |
| W4    | A reconnect that changes `remoteSessionId` publishes the old rows' removals AND the new rows' upserts in **ONE** delta frame, removals first. A consumer never sees an intermediate empty projection.            | `TestReconnectReplacesRowsInOneDeltaFrame`                                                        |
| W5    | `liveStatus` comes **only** from the live overlay. Without one, a row falls back to completed/error/idle and never claims running or waiting — a running status in SQLite may be from the previous process exit. | `TestLiveStatusComesOnlyFromTheOverlay`                                                           |
| W6    | A disconnected source **freezes** its rows with an offline badge instead of dropping them, and its workspace fact is `disconnected`/`failed` (the schema refinement).                                            | `TestDisconnectedSourceFreezesItsRows`                                                            |
| W7    | A **failed read keeps** the last trusted snapshot. One source's read error must not empty the list.                                                                                                              | `TestFailedReadKeepsTheLastTrustedSnapshot`                                                       |
| W8    | The four membership kinds partition the buckets: `timeline` excludes pinned and archived, `active` includes pinned.                                                                                              | `TestMembershipBucketsAreDisjoint`                                                                |
| W9    | A local and a remote row with the **same task id stay separate**, keyed by (remote marker, identity, id). A remote row is never keyed by path alone.                                                             | `TestLocalAndRemoteRowsStayIsolated`                                                              |
| W10   | A scope that **does not resolve contributes nothing** and never falls back to another scope's rows (the server variant's remote fail-closed).                                                                    | `TestUnresolvableScopeIsSkippedNotAnsweredLocally`                                                |
| W11   | A remote scope **without** `workspaceIdentity` is refused at registration, so the producer cannot emit an address the wire schema rejects.                                                                       | `TestRemoteSourceWithoutIdentityIsRefused`                                                        |
| W12   | The refresh is **single-flight** per source: N concurrent list calls issue one index read.                                                                                                                       | `TestConcurrentRefreshIsSingleFlight`                                                             |
| W13   | A write **routes to the source** and the projection converges onto it; the projection is a cache, never a second write path.                                                                                     | `TestWriteRoutesToTheSourceAndConvergesTheProjection`                                             |
| W14   | A **delete returns no meta** for the removed row. Returning the old meta would re-add a task the index just removed.                                                                                             | `TestDeleteRemovesTheRowAndReturnsNoMeta`                                                         |
| W15   | `open`/`resume` are **not writes**: they verify that exactly one source owns the address and re-read nothing.                                                                                                    | `TestOpenAndResumeAreNotWrites`                                                                   |
| W16   | A write against an **offline source is refused**, not silently dropped, so the UI can offer "retry".                                                                                                             | `TestOfflineSourceRefusesWrites`                                                                  |
| W17   | An archived **batch** naming a source that was replaced mid-confirmation is refused before it reaches the replacement's rows.                                                                                    | `TestArchivedBatchRejectsAReplacedSource`                                                         |
| W18   | A **search** goes to each source's own index and is enriched from the projection; one source's search failure does not empty the answer.                                                                         | `TestSearchGoesToTheSourceAndEnrichesFromTheProjection`, `TestSearchFailureDoesNotEmptyTheAnswer` |
| W19   | Sort + limit are **total and deterministic**: the task id breaks a timestamp tie, so two list calls never disagree.                                                                                              | `TestLimitAndSortAreTotalAndDeterministic`                                                        |

### The per-connection boundary

This is the part that is not a singleton, so it has its own set:

| Guard | Rule                                                                                                                                                               | Test                                                                                          |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------- |
| C1    | Two connections get **different subscription ids** and **separate frame streams**, over the same shared rows.                                                      | `TestTwoConnectionsAreIsolated`                                                               |
| C2    | Disposing one connection removes **only its own** subscriptions; the other connection's subscription, cursor and delivery are untouched.                           | `TestTwoConnectionsAreIsolated`                                                               |
| C3    | A disposed connection receives **no later frame**. This is the leak the TS comment warns about.                                                                    | `TestDisposedConnectionReceivesNothing`                                                       |
| C4    | Every entry point of a disposed connection **refuses** (`ErrClosed`), including subscribe — a subscribe after close would register a subscription nobody releases. | `TestDisposedConnectionRefusesEveryEntryPoint`                                                |
| C5    | Unsubscribe for an id this connection **does not own is a no-op**, not an error and not another connection's teardown.                                             | `TestUnsubscribeStopsDeliveryAndIsNotAnError`, `TestResyncReplaysSnapshotAndRefusesForeignID` |
| C6    | A topic outside the closed set of two is **refused**; a subscription that would never produce a frame is not minted.                                               | `TestUnknownTopicIsRefused`                                                                   |
| C7    | Runtime dispose releases **every** subscription and every row; nothing survives it.                                                                                | `TestNoSubscriptionSurvivesRuntimeDispose`                                                    |
| C8    | Removing a source **stops its live observer**; a stopped observer can no longer write to the projection.                                                           | `TestDisposeReleasesTheSourceObserver`                                                        |
| C9    | 16 connections subscribing, listing, mutating, resyncing, unsubscribing and disposing concurrently leave **zero** attachments and **zero** subscriptions.          | `TestConcurrentConnectionsDoNotRace`                                                          |
| C10   | 50 connect/dispose rounds leave **no goroutine growth**.                                                                                                           | `TestNoGoroutineLeakAcrossConnectAndDispose`                                                  |

### Recorded decisions

| Guard | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| W5    | **Kept, and strengthened.** The Go port's overlay seam is `SessionObserver` (source.go), not the TS's `windowHostSessionsIndexObserver`. That observer is NOT re-implemented here: `internal/services/taskindex` (step 3.10.4) already owns the existing-only sessions-index subscription, its frame assembly and its runtime-generation handling. Re-implementing it would create a second owner for one subscription, which is the exact arrangement AGENTS.md forbids. The rule W5 protects — that only a live overlay may assert running/waiting — is therefore enforced at the seam and pinned here. |
| W12   | **Kept and completed.** The TS short-circuits a repeated read with `sourceSnapshotTaskServices`. The Go port's `snapshotted` map is that same marker; it is set only by a SUCCESSFUL read and cleared by every invalidating transition, so a failed read retries instead of being answered from an empty projection.                                                                                                                                                                                                                                                                                      |
| —     | The **cursor advance** is not a listed guard because the TS is single-threaded and gets it free. In Go two sources refresh in parallel, so the advance and the subscriber snapshot had to move into one critical section (topics.go). Without that, two parallel refreshes claimed the same `fromSeq`. This was found by `go test -race -count=3`, not by review.                                                                                                                                                                                                                                         |
