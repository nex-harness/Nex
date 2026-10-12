# Session event store and sequence numbering

Owner: Agent A. Source of truth: the checked-out TypeScript under
`apps/nex-cli/packages/{contracts,core,bootstrap}`. Verified against the real
Node agent; the Go port is `go/internal/eventstore`.

## What the store is

Every runtime `SessionEvent` in the Node agent is appended through
`AgentRuntime.appendEvent` (`core/src/runtime/methods/events.ts`), which calls
`this.eventStore.append(event)`. The event store is
`InMemorySessionEventStore`
(`contracts/src/events/in-memory-session-event-store.ts`). It is **in-memory
only**: nothing the store holds is written to SQLite. The `session_entry` table
only ever contains `runtime/model_selection` and `runtime/execution_state`
(observed by dumping the database of a recorded S02 run); `SessionEvent`s never
land there. So a restarted process starts a fresh store, and its first event is
numbered 1 again — there is no persisted counter to resume from.

`append` is the only place a `sequenceNumber` is assigned
(`in-memory-session-event-store.ts:62-66`):

```ts
const sequenceNumber =
  event.sequenceNumber > 0 ? event.sequenceNumber : state.latestSequenceNumber + 1;
state.latestSequenceNumber = Math.max(state.latestSequenceNumber, sequenceNumber);
```

Rules that follow, and that the Go port implements verbatim:

1. **Every appended event consumes exactly one number**, including the transient
   events that never reach the protocol.
2. **A caller-assigned positive number is honoured**, and the high-water mark
   jumps to it (`Math.max`). No caller in the agent passes a positive number
   today — the tool executor passes `sequenceNumber: 0`
   (`core/src/tool/executor/events.ts:37` etc.), and `createSessionEvent`
   defaults to 0 (`contracts/src/events/session.events.ts:1141`).
3. **The counter is per session and only ever grows.** Eviction must not lower it,
   or a later `latest + 1` would collide with an already-used number
   (`in-memory-session-event-store.ts:65-66` comment).
4. **There is no turn-scoped counter.** Every `eventSeq`, `createdAtSeq` and
   protocol `seq` value is a view of this one session counter. The turn ids only
   affect _retention_, never numbering.

## Which events consume a number (and which reach the wire)

The numbering input is the **ordered stream of `appendEvent` calls**, not the set
of event types. For the recorded scenarios the order within a turn is:

```
session_title_updated            (title from input, or the async sidecar)
turn_started
session_input_promoted           (user message promotion)
model_request                    (one per model step)
model_network_status …           (model_request_started / failed / retry_scheduled / completed)
model_streaming …                (start, text_start, deltas…, text_end, tool_input_*, tool_call, finish)
streaming_tool_ledger_updated …  (tool-call ledger, interleaved around tool_call_scheduled)
tool_call_scheduled / permission_requested / permission_resolved /
tool_call_started / tool_call_result / tool_batch_complete /
checkpoint_created / stream_recovery_anchor_created
model_complete
turn_complete
… then the title-generation sidecar: model_request, model_network_status …,
  model_complete, session_title_updated
```

Every event in that list — including `model_streaming`,
`model_network_status`, `streaming_tool_ledger_updated` and `tool_call_progress`
— is appended through `appendEvent` and therefore consumes a number. This is why
the recorded S02 turn shows telemetry `eventSeq` values `2, 5, 8, 9, 10, 11, 14,
15, 16` while `session/events` returns only a handful of retained events: the
gaps are the transient events (streaming `start`/`text_start`/`text_end`/`finish`
and the two network status frames) that were numbered but are not exposed.

Only three surfaces expose the raw number, all derived from
`event.sequenceNumber`:

- v4 telemetry `eventSeq`
  (`bootstrap/src/nex-protocol-v4/conversation-telemetry-facts.ts:312`,
  `eventSeq: Math.max(0, Math.floor(event.sequenceNumber))`);
- delta rows `createdAtSeq`
  (`bootstrap/src/nex-protocol-v4/product-projection.ts:5250`);
- `computer-use/operation-event` `sequenceNumber`
  (`bootstrap/src/nex-protocol/computer-use-operation-event.ts:37`).

The protocol `session/events` list does **not** expose it: it renumbers by
"protocol-visible event stream" position
(`bootstrap/src/nex-protocol/server-operations.ts`,
`assignProtocolEventSeq`) precisely so the internal ledger (which includes the
filtered `tool_input_delta` etc.) never leaks
(`server-operations.ts:655-662`). `shouldExposeSessionEventToProtocol`
(`bootstrap/src/nex-protocol/session-mapper.ts:358`) hides
`streaming_tool_ledger_updated` and `dynamic_workflow_run_progress`, and hides
`model_streaming` except for `text_delta`/`reasoning_delta`/`tool_input_*`/
`tool_call`.

## Streaming events are appended one per provider chunk

`model_streaming` events are **not** coalesced at append time. In
`core/src/runtime/methods/model.ts` each AI-SDK stream part enqueues one event:
`start`, `text_start`, `text_delta` (one per provider chunk), `text_end`,
`tool_input_start`/`tool_input_delta`/`tool_input_end`, `tool_call`, `finish`
(`model.ts:222-437`). The queue
(`core/src/runtime/methods/model-streaming-event-queue.ts`) only serialises the
appends; it does not merge them. Coalescing happens later, on the _protocol_
side (`mergeProtocolStreamingDeltaBatch`), not in the store. So the number of
sequence numbers a model step consumes equals the number of provider stream
parts.

The recorded mock for S02 sends `"Hello from the mock model."` in 8-char chunks
(4 text chunks), so the turn consumes: `start` + `text_start` + 4 × `text_delta`

- `text_end` + `finish` = 8 streaming events, matching the summary count
  `{start:1, text_start:1, text_delta:4, text_end:1, finish:1}`.

## Retention (eviction) policy

`contracts/src/events/session-event-retention.ts`.
`TRANSIENT_SESSION_EVENT_TYPES` (line 9) = `model_streaming`,
`tool_call_progress`, `streaming_tool_ledger_updated`, `model_network_status`.

`turn-window` (the default) seals a turn on `turn_complete` / `turn_error`, and
evicts the transient events of all **previously sealed** turns when the next
`turn_started` arrives — except the current turn id
(`session-event-retention.ts:65-77`). The lag is deliberate: right after a turn
ends its messages may not be flushed, and cold recovery still needs the
in-memory transient events; the next `turn_started` means the user sent again,
so the previous turn is definitely persisted. Eviction drops only
`isTransient(event) && event.turnId && turnIds.has(event.turnId)` events
(`in-memory-session-event-store.ts:97-106`), so non-transient events and
transient events without a `turnId` always survive.

A time fallback (`SEALED_TURN_TRANSIENT_GRACE_MS = 120_000`,
`session-event-retention.ts:22`) evicts sealed turns with no successor after the
grace period; it is driven by a 60 s tick and covers one-shot sessions such as
subagent children. It never rewinds the counter.

`unbounded` mode never evicts (used for rollback and comparison tests).

`getEvents` returns the retained events in append order; `getEventsAfter(seq)`
filters `sequenceNumber > seq`; `getLatestSequenceNumber` is the monotonic
high-water mark and (unlike the retained list) does not dip after eviction.

## Verified numbers

`scripts/go-rewrite/gen-eventseq-vectors.mts` runs the real Node agent for S02,
S03, S04, S05 and S13 with `NEX_RUNTIME_ENV=development`, recovers the exact
`(type, sequenceNumber)` of every append from the agent's debug log
(`event_store.appended` for low-frequency events,
`event_store.appended.summary` for the aggregated transient ones), reconstructs
the full ordered stream by solving the overlapping summary ranges, and
cross-checks every telemetry `eventSeq` anchor. Output:
`go/testdata/vectors/agent-eventseq.json`; each scenario's `normalizedOutSha256`
matches `go/testdata/traces/agent/<id>/meta.json`, i.e. the runs are the same
observable sessions as the recorded traces.

Event counts: S02 16, S03 39, S04 66, S05 41, S13 18. `ToolCallProgress` is not
exercised by these five scenarios (no provider streams tool progress); its type
membership in the transient set is asserted structurally, not from a recording.

## Rules I could not fully determine

- **Ordering inside a `model_network_status` aggregate.** The network payload has
  no `kind` field (`payloadKinds` shows `<missing>`), and flush boundaries can cut
  a single attempt's `started`/`failed`/`retry_scheduled`/`completed` into
  different log blocks. The reconstruction only needs the _type_, so the vector
  is exact at the type level; the per-`kind` order is inferred from the runtime
  code (`runner-stream.ts:306,613,759,1609`) plus the telemetry `model.request.status`
  events, and is consistent for every observed seq, but is not asserted
  independently.
- **`agentTitleUpdated` vs sidecar.** S02 writes `session_title_updated` at seq 1
  (synchronous title from input); S03–S05/S13 write their async title
  `session_title_updated` after the sidecar's `model_request`/`model_complete` at
  the very end. Which path runs is a turn-start scheduling decision
  (`maybeStartSessionTitleGeneration`), not a store rule; the Go store does not
  decide it — the caller supplies the ordered stream.
- **Subagent mirror / live-only events with `sequenceNumber: 0`.** They bypass
  the parent store (`server-operations.ts` comment). Not covered here.
- **`cold-event-merge` renumbering.** Hydration synthesises events with
  `sequenceNumber: index + 1` (`bootstrap/src/nex-protocol-v4/cold-event-merge.ts:714`),
  which the store would honour as a passthrough if fed in. Not exercised by the
  five scenarios.

## Go port

`go/internal/eventstore`:

- `store.go` — the `Store` interface and `Event`/`Stats` types.
- `types.go` — the `SessionEventType` string constants the store depends on.
- `retention.go` — `transientTypes`, `IsTransient`, `NewRetentionPolicy`,
  `unbounded`/`turn-window` policies, `SealedTurnTransientGraceMs`.
- `memory.go` — `InMemory`, the port of `InMemorySessionEventStore`.
- `memory_test.go` — the numbering, passthrough, monotonicity, retention, prune,
  per-session and copy rules.
- `vector_test.go` — replays `agent-eventseq.json` and requires identical
  sequence numbers for all five scenarios.

## Mutation checks

Each mutation below was applied to the Go implementation and `go test
./internal/eventstore/` had to fail:

| #   | Mutation                                                     | Result                                                                                                             |
| --- | ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------ |
| M1  | assign `latestSeq` instead of `latestSeq+1`                  | caught                                                                                                             |
| M2  | drop the `max()` (counter can regress)                       | caught                                                                                                             |
| M3  | ignore an explicit `SequenceNumber`                          | caught                                                                                                             |
| M4  | seal evicts immediately on `turn_complete` (no one-turn lag) | caught                                                                                                             |
| M5  | evict the current turn too on `turn_started`                 | caught                                                                                                             |
| M6  | do not clear `sealedAt` on a new `turn_started`              | **not caught** (behaviour is identical — re-evicting an already-evicted turn is a no-op; documented, not asserted) |
| M7  | drop `model_network_status` from the transient set           | caught                                                                                                             |
| M8  | evict non-transient events too                               | caught                                                                                                             |
| M9  | prune uses `>` instead of `>=`                               | caught                                                                                                             |
| M10 | `Stats` counts non-transient as `RetainedTransient`          | caught                                                                                                             |
| M11 | `turn-window` degrades to no-op `onAppend`                   | caught                                                                                                             |
| M12 | `Events()` returns the internal slice (aliasing)             | caught                                                                                                             |
| M13 | reset the counter on `turn_started` (per-turn counter)       | caught                                                                                                             |
| M14 | prune iterates `openTurns` instead of `sealedAt`             | caught                                                                                                             |

M6 is the only uncaught mutation; it is behaviourally equivalent to the correct
code, so no test can distinguish it without asserting internal state.
