# Product projection (SessionEvent → ConversationDelta) — Go port

Status: implemented and verified against the real TypeScript `ProductProjection`.
Scope: `go/internal/projection`, vectors in `go/testdata/vectors/projection-scenarios.json`,
generator `scripts/go-rewrite/gen-projection-vectors.mts`, mutation check
`scripts/go-rewrite/mutation-projection.sh`.

## What this package is

The CLI keeps an authoritative in-memory projection of a conversation. It consumes the CLI event
log (`SessionEvent`, one per fact) and emits `ConversationDelta` operations that a v4 conversation
subscriber applies to its snapshot. The projection also owns the `ConversationSnapshot` that the
deltas advance.

- Input: `Event` — `{id, sessionId, turnId?, type, timestampMs, traceId, sequenceNumber, payload}`.
- Output: `[]Delta` per event, plus `Snapshot()`.
- Sources ported: `product-projection.ts` (`class ProductProjection.applyEvent`),
  `event-normalizer.ts` (`normalizeTurnStarted` / `normalizeModelStreaming`), `projection-state.ts`
  (initial snapshot, availability/inputRouting, revision rule), `projection-rows.ts` (result → header
  state, tool output), `permission-options.ts` (default permission options), and the pure reducers
  `apply.ts` + `coalesce.ts` from `packages/shared/src/nex-protocol-v4`.

## API

```go
type Event struct {
    ID, SessionID, TurnID string
    HasTurnID             bool
    Type                  string
    TimestampMs           int64
    TraceID               string
    SequenceNumber        int64
    Payload               json.RawMessage
}

func New(sessionID, logEpoch string) *Projection
func (p *Projection) Apply(event Event) ([]Delta, error)   // deltas for this event
func (p *Projection) Snapshot() (json.RawMessage, error)   // current ConversationSnapshot
```

`Delta` is an ordered JSON object (`obj`) whose key order mirrors the TypeScript object literal
(`op` first, then the payload field). The snapshot is likewise an `obj`. `Apply` returns deltas that
serialize **byte-for-byte** like the TypeScript ones; subtrees the projection does not model
(`input`, `display`, `detail`, `originMeta`, `usage.cache`/`breakdown`) are carried through as
`json.RawMessage` and re-compacted so their whitespace matches `JSON.stringify`.

### Why ordered objects, not structs/maps

Go maps sort keys, and Go structs must be declared with a single field order per type — but the
TypeScript builds its rows/patches with object literals and spreads whose key order varies by
branch (e.g. a `turnHeader` with `fileChanges` inserts it before `endedAt`). Reproducing the wire
byte-for-byte therefore needs an insertion-ordered object encoder. `obj` provides `set` (append),
`put` (update in place, keeping position — models `{...snapshot, key: value}`), `del`, and
`setIf` (models `...(cond ? {k:v} : {})`).

## Rules (found while porting)

### Snapshot and revisions

- The snapshot is **key-level replacement**, never a deep merge (`state.updated` is `Object.assign`).
- `attachRevision`: an event's delta batch bumps `revision` by one iff it contains a row op or a
  `state.updated` carrying one of `control, availability, inputRouting, meta, config, queue,
pendingInteractions, backgroundWorks, subagents, goal, plan`. `workflowRuns` and the two
  `workflowRun.*` ops are deliberately exempt. The bump is merged into a trailing `state.updated`
  if there is one, else appended as a new `state.updated {revision}`.
- `seq` is set to the consumed event's `sequenceNumber` after the batch applies (it is not part of
  `apply.ts`).
- `activation`: the initial snapshot is `phase: "draft"` etc.; `SessionCreated` changes no visible
  state (draft semantics) and `SessionTitleUpdated` updates `meta`.

### Deltas (closed set)

`row.appended`, `row.upserted`, `row.removed {fromRowId}`, `row.delta {rowId, path, append}`,
`state.updated {patch}`. `row.delta` paths: `text` (assistantText/reasoning), `inputText`
(toolCall), `output.text` (toolCall), `summaryText` (subagent). A path/kind mismatch is a no-op.
`row.upserted` of an unloaded rowId is a no-op; `row.removed` clears `firstRowId`+`totalCount` when
it trims from the head.

### Turns

- `turnIdOf`: the runtime turn id, remapped through `productTurnIdByRuntimeTurnId` after a queue
  drain splits a turn. `normalizeTurnStarted` sets `productTurnId = transcriptMessageId ?? runtime`.
- TurnStarted appends `turnHeader` (origin/executionKind/state running) and, when the input is
  visible, a `userInput` row. Then a `state.updated` control patch (`phase: running`, active work,
  availability, inputRouting). A `modelChange` timelineMarker is emitted only when the current
  provider/model differs from the previous turn (first turn is silent).
- `TurnComplete` closes streaming text/reasoning and open foreground tools, upserts the header with
  the terminal state, and emits a control patch (`completedSuccess` / `completedInterrupted` /
  `error`). A successful turn marks the latest same-turn assistant row `canFork`.

### Model streaming

- `text_start` opens an `assistantText` row; `text_delta` appends via `row.delta {path:"text"}`
  (opening the row if none is open); `text_end` seals it `complete`. Same for reasoning.
- `tool_input_start` opens a `toolCall` row keyed on the **toolCallId** as its `entityId`, status
  `inputStreaming`. `tool_input_delta` appends `row.delta {path:"inputText"}`. `tool_call` seals the
  row, replacing `inputText` with the canonical stringified input.
- **File-tool preview throttle**: for Write/Edit the input deltas are throttled to one publish per
  `NEX_FILE_STREAMING_TOOL_INPUT_PREVIEW_MIN_INTERVAL_MS` (1000 ms), buffering the pending append.
  Read and other tools append immediately.
- After a retry, the first valid model progress (`text_delta`/`reasoning_delta` with non-empty text,
  or a `tool_*` with a toolCallId) clears `control.apiRetry` with an extra `state.updated`.

### Model complete / network status

- Only a `main_turn` model complete updates `usage.contextWindow`/`cumulative` (subagent completes
  still project `fileChanges` onto the turn header). `contextWindow` stays null when there is no
  known max, and `cache`/`breakdown` carry through only when present.
- `model_network_status`: `model_retry_scheduled` sets `control.apiRetry` (`fault.provider.rateLimited`
  for rate_limited/offpeak_queued, `fault.provider.serverError`, `fault.network.*`, …), with
  `nextRetryAt = eventTime + delayMs`. `model_request_completed`/non-retryable failure clears it.

### Tools

- `tool_call_scheduled` creates or updates the toolCall row (`inputStreaming`, full `inputText`).
- `tool_call_started` → `running` + `startedAt`; `tool_call_result` → `success`/`error` + `output`
  - `endedAt`; `tool_call_error` → `error`/`cancelled` + `endedAt`. Any event arriving when the
    session is not running (or is a mirrored subagent event) is dropped.

### Permissions

- `permission_requested` sets the tool row `pendingApproval` + `approvalInteractionId` and appends a
  `permission` interaction to `pendingInteractions` (options built by `buildProtocolPermissionOptions`
  default policy: allowOnce / allowAlways (+project rule) / deny).
- `permission_resolved`/`permission_denied` call `settlePermission`: deny → the tool row becomes
  `cancelled`, otherwise `running`; the interaction is removed from `pendingInteractions`.

### Row actions materialization

After each event's own deltas, `materializeCommandRowActions` re-derives the action flags and emits
`row.upserted` for changed rows: `userInput` gets `canEdit + editDisposition:"rewind"` when it is the
latest realUser row and has a canonical target; `assistantText` gets `canRetry` (latest complete
assistant with a canonical cause) and `canFork` (latest same-turn assistant of a completed turn);
`turnHeader` gets `canRewindFiles` when a stable fileChanges summary exists.

### Coalesce (flush-window merge)

`coalesceConversationDeltas` ports `coalesce.ts`: adjacent same-`(rowId,path)` `row.delta` merge;
adjacent `state.updated` shallow-merge; a `row.upserted` drops earlier `row.delta` of the same row
(back to the last barrier or the row's previous appended/upserted); `row.removed` is a barrier. Its
invariant — `apply(o, coalesce(ds)) == apply(o, ds)` — is tested.

## Vectors and how to regenerate

`go/testdata/vectors/projection-scenarios.json` is produced by running the **real** TypeScript
`ProductProjection` (loaded from the compiled bootstrap via the `@nex/bootstrap/v4-replay` barrel)
over hand-built `SessionEvent` sequences whose payload shapes are copied from the runtime emitters
(`apps/nex-cli/packages/core/src/runtime/methods/*.ts`, `tool/executor/events.ts`). The
`sequenceNumber`s are inputs, aligned to the recorded traces' `createdAtSeq`/`finalToSeq`.

```
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-projection-vectors.mts
```

Scenarios: S02 (draft + streamed text answer — 14 coalesced deltas), S03 (Read tool), S04 (Write +
Edit with approvals), S05 (Write denied → cancelled), S13 (rate-limited retry then success).

## Tests

| Test                                                                                                                                                   | What it proves                                                 |
| ------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------- |
| `TestScenariosReproduceTypeScriptRawDeltas`                                                                                                            | every event's deltas, byte-for-byte (key order included)       |
| `TestScenariosReproduceCoalescedStream`                                                                                                                | the merged stream matches the TS coalesce                      |
| `TestScenariosReproduceFinalSnapshot`                                                                                                                  | the final snapshot matches the TS snapshot                     |
| `TestApplyMatchesOracleOnVectors`                                                                                                                      | the `apply.ts` port reproduces the TS snapshot from the deltas |
| `TestCoalescePreservesApplySemantics`                                                                                                                  | `apply(o, coalesce(ds)) == apply(o, ds)`                       |
| `TestCoalesceMatchesTypeScript`                                                                                                                        | Go coalesce == TS coalesce                                     |
| `TestS02CoalescedMatchesRecordedTrace`                                                                                                                 | S02's 14 deltas match the recorded Node agent trace            |
| `TestApplyRowRemovedClearsActiveBranch`, `TestAppendToRowRespectsKindGuard`, `TestAvailabilityDraftForbidsCompactAndFork`, `TestToolCallResultFailure` | real paths the scenarios do not exercise                       |

Mutation check (`scripts/go-rewrite/mutation-projection.sh`) breaks the implementation in 16 ways
(coalesce rules, apply bookkeeping, revision keys, availability, turn/entity ids, usage accumulation,
throttle, permission status, action materialization, encoder key order); all 16 are caught.

## Not done / limitations

- **Unported event types** fall through to a no-op: steer/queue, compact, goal, fork, rewind, hooks,
  subagents, workflowRuns, background tasks, feedback, stream recovery. The stop/resume and
  `TurnError` failure path (beyond `TurnComplete` with a failed result) are not ported.
- **`makeStableForkAssistant` / output-token Continue** re-open path is only partially modelled; the
  scenarios contain no Continue.
- **`materializeCommandRowActions`** covers turnHeader/userInput/assistantText; the file-tool preview
  side-table exists but `flushStreamingToolInput` is a no-op (no pending buffer modelled) and
  `shouldHideInvalidToolCallFromProduct` and the subagent-mirror guard are not modelled.
- The **coalesced stream vs recorded traces** is byte-exact for S02; for S03–S05/S13 the raw
  per-event deltas are exact but the coalesced _framing_ depends on the real event ordering /
  sequence allocation (Agent A's reverse-engineering), so those are not compared to the trace.
- `usage` numeric fields assume `inputTokens/outputTokens` style; `getModelUsageContextTokens`
  prefers `totalTokens` then `input+output`.
