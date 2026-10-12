# Tool failures and the streaming-tool lifecycle (Go)

Status: implemented. Owner: agent runtime. Scope: the Go port of the Node tool
failure lifecycle — the pre-handler early exits, the handler-failure payload,
`tool_call_error` (instead of `tool_call_result`), the row/telemetry projection
of a failure, and the event order of streaming tools inside one model step.
Files: `go/internal/runtime/toolfailure.go` (new), `go/internal/runtime/toolinput.go`
(new), `go/internal/runtime/toolsrun.go`, `go/internal/runtime/toolstep.go`,
`go/internal/projection/handlers_tools.go`.

Verified with `node scripts/go-rewrite/record/replay-agent.mjs --scenario S0x`
(S01–S05, S13, 20/20 each) and
`node scripts/go-rewrite/record/compare-agents.mjs --agent <go> --all`
(held-out scenarios, see "Status" at the end).

## The rule everything else follows from

A failed tool call publishes **`tool_call_error`**, never `tool_call_result`.
There is no `tool_call_result {success:false}` in the Node agent: the call
runner's catch block converts the failure into an error result and emits
`emitToolCallError` (`apps/nex-cli/packages/core/src/tool/executor/call-runner.ts:565-640`).
The product row therefore settles through `onToolCallError`, which never sets an
`output`.

## Failure stages

The stage a call fails at decides (a) which events exist and (b) what the model,
the row and the telemetry each see.

| Stage                                  | Node source                                     | Events                       | `error.type`            | `error.code`                         | model sees                                 |
| -------------------------------------- | ----------------------------------------------- | ---------------------------- | ----------------------- | ------------------------------------ | ------------------------------------------ |
| registry miss                          | `call-runner.ts:124`                            | scheduled → **failed**       | `tool_not_found`        | `TOOL_NOT_FOUND`                     | bare message                               |
| input-schema                           | `call-runner.ts:175`                            | scheduled → **failed**       | `tool_execution_failed` | `TOOL_EXECUTION_FAILED`              | `<tool_use_error>InputValidationError: …`  |
| handler failure (`ToolHandlerFailure`) | `call-runner.ts:457`, `errors.ts:63`            | scheduled → started → failed | `tool_execution_failed` | `String(errorCode)` — Edit's `8`/`9` | `<tool_use_error>message</tool_use_error>` |
| handler failure (thrown `CoreError`)   | `handlers/read.ts:229`, `handlers/write.ts:285` | scheduled → started → failed | `tool_execution_failed` | `TOOL_EXECUTION_FAILED`              | bare (projected) message                   |

Both early exits run **before** `emitToolCallStarted`, so the row only ever sees
`scheduled → failed`. A handler failure runs _after_ it, so the row is `running`
when the error lands.

### Why the codes look the way they do

`createCoreError` (`contracts/src/errors/index.ts:73-88`) sets
`error.code = type.toUpperCase()`, and `createErrorResult`'s payload spreads
`code: String(handlerFailure.errorCode)` for a `ToolHandlerFailure`, otherwise
`projectedError.code` (`core/src/errors/error-payload.ts:53`). So:

- the **payload** carries `code` = `"8"` for an Edit no-match (the numeric
  handler code), or `"TOOL_EXECUTION_FAILED"` for a thrown CoreError;
- the **telemetry fact** reads `error.code ?? error.type`, so it shows the same
  upper-cased code (`conversation-telemetry-facts.ts:504`);
- the **row** reads `error.type`, so it shows the lower-case
  `tool_execution_failed` (`product-projection.ts:3032`).

The three surfaces therefore disagree on purpose, and the Go port reproduces all
three.

## Message compaction vs the model-visible text

`projectExecutionErrorPayload` runs every error message through `sanitizeText`
(`error-payload.ts:363`): every whitespace run collapses to one space, the result
is trimmed, and anything over 500 characters is cut to 497 plus `"..."`.

That compaction applies to the **payload**, i.e. the row and the telemetry fact.
The **model** receives `result.modelContent`, which keeps the raw text:

- a `ToolHandlerFailure` → `<tool_use_error>` + the _unmodified_ message +
  `</tool_use_error>` (`errors.ts:20-22`);
- an input-schema failure → the projected `InputValidationError` text;
- any other thrown CoreError → `modelContent` is left undefined and
  `turn-tools.ts:259-262` falls back to the _compacted_ `error.message`.

`read-missing` is the observable proof: the row/telemetry carry
`"File does not exist. Note: your current working directory is <WS>."` while
`edit-no-match` carries `"String to replace not found in file. String: zzz"`
(compacted) in the row but `"<tool_use_error>String to replace not found in
file.\nString: zzz</tool_use_error>"` in the next request.

`go/internal/runtime/toolfailure.go` models this as one `toolFailure` value with
two separate fields: `message` (compacted, for the payload) and `modelContent`
(the text the model gets). `modelText()` returns the latter when set.

The 500-character cap counts UTF-16 code units like `String.prototype.slice`. A
cut that lands mid-surrogate-pair cannot be represented in a Go `string` (JS
yields a lone surrogate, Go's decoder yields U+FFFD); that boundary is a stated
limit, not a silent approximation.

## The streaming-tool lifecycle

Node's streaming coordinator (`runtime/methods/streaming-tool-coordinator.ts`)
`accept()`s a tool call from the stream's tool-call callback but stores the
execution promise **without awaiting it**. The accepted call's own events
therefore interleave with the rest of the stream instead of blocking it, and
`executeDuringStream` drives one call through:

```
persistPendingToolPart
→ tool_call_closed          ledger
→ tool_call_scheduled
→ tool_queued               ledger
→ executeTools → batch_start → tool_started ledger
→ executeToolCall → tool_call_started → tool_call_result | tool_call_error
```

Two consequences the recorded traces pin down:

1. **Interleaving, not batching.** Two read-only calls in one model step come out
   as `scheduled(c1) … started(c1) … scheduled(c2) … started(c2)` — _not_
   "schedule both, then start both" (`read-two-files`: 14, 19, 21, 25). The Go
   runtime starts each streaming call from `applyStreamEvent`, and the previous
   call drains at the next yield (`drainStreamedTools`).
2. **`tool_call_started` lands after the stream's `finish` event.** Because the
   accepted chain resumes only when the parser yields, `emitToolCallStarted` —
   its last await — is appended after `model_streaming {kind:"finish"}`. S03
   records `scheduled=12, started=16, completed=17` with exactly that gap; the Go
   port parks the started event in `pendingStart` and flushes it when the stream
   closes.

For the two-call step the rule generalises as: the _previous_ call's
`tool_call_started` lands after the next call's `tool_call` streaming event but
before that call's `inputText` row update (read-two-files' row deltas show
`row3 running` between `row4`'s two `inputText` upserts).

The **deferred** path (Write/Edit, side-effecting or approval-gated) is
different and stays synchronous: `turn-tools.ts` emits `tool_call_closed`, the
whole schedule's `tool_call_scheduled`, one `tool_queued` per call, then
`batch_start`'s `tool_started` and the executor's `ToolCallStarted`. S04 records
`scheduled=15, permission_requested=18, resolved=19, started=20, result=21`.

## What a failure does _not_ emit

- **No `checkpoint_created`.** `emitFileMutationCheckpoint` returns early unless
  `result.success` (`runtime/methods/tools.ts:170`); a failed or denied
  Write/Edit changed nothing. Before the fix Go emitted one and every scenario
  with a failed Edit was off by exactly one raw sequence number.
- **No file change.** `recordTurnFileChange` is reached only through the same
  checkpoint path, so a failed call contributes no `fileChanges`. A _successful_
  Edit does contribute: the map is keyed by path and the first write's counts
  win (`helpers/turn-file-changes.ts:17-30`), which is what makes
  `edit-replace-all` report `additions:2, deletions:2, files:1`.

## Projection

`onToolCallError` (`product-projection.ts:3032`, ported in
`go/internal/projection/handlers_tools.go`) upserts the tool row with:

- `status: "error"` — or `"cancelled"` when the payload error is a
  cancellation (`type === tool_cancelled || code === "TOOL_CANCELLED"`), which
  also _drops_ any error, because a row already written as error is not
  corrected by the later cancelled-turn reducer;
- `error: {code: payload.error.type, message: payload.error.message}`;
- `endedAt`;
- **no `output` key** — a failed call never received one.

Any event arriving while the session is not running is dropped, and an event for
an unknown tool call id is a no-op. The success path is unchanged: a
`tool_call_result` still sets `success`/`error` + `output`/`endedAt`.

## Tests

`go/internal/runtime/toolstep_test.go` drives a full model step against a
temporary workspace and asserts the emitted `tool.lifecycle` facts:

| Test                                               | Pins                                                                                                               |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `TestHandlerFailureEmitsToolCallErrorNotResult`    | scheduled → started → failed; `code:"8"`; compacted message; the model gets the multi-line `<tool_use_error>` text |
| `TestAmbiguousEditCarriesCodeNine`                 | the same shape with `code:"9"` and the ambiguity message                                                           |
| `TestUnknownToolFailsBeforeStarted`                | scheduled → failed with **no** started; `tool_not_found`/`TOOL_NOT_FOUND`; no wrapper                              |
| `TestBadInputFailsBeforeStarted`                   | scheduled → failed; fixed CoreError message; the model gets the projected `InputValidationError`                   |
| `TestWriteWithoutPriorReadStartsThenFails`         | a handler failure still emits `tool_call_started`; a thrown CoreError has no numeric code                          |
| `TestTwoStreamingCallsScheduleAndStartInterleaved` | the interleave order and monotonic sequence numbers                                                                |
| `TestStreamingToolStartedLandsAfterTheFinishEvent` | the exact raw event-type order, including `finish` before `tool_call_started`                                      |
| `TestCheckpointOnlyForSuccessfulWrites`            | a failed/denied Write/Edit emits no checkpoint                                                                     |
| `TestRecordChangedFilesCountsEachFileOnce`         | the per-path, first-write-wins map; a failed write records nothing                                                 |
| `TestSanitizeErrorTextCollapsesAndCaps`            | collapse, blank, the 500-unit cap and its UTF-16 counting                                                          |
| `TestFailureModelTextPerStage`                     | the model-visible text for each failure stage                                                                      |

`go/internal/projection/units_test.go` adds
`TestToolCallErrorRowHasErrorAndNoOutput`,
`TestToolCallErrorCancellationDropsError` and
`TestToolCallErrorBeforeScheduledStillUpserts` (the existing
`TestToolCallResultFailure` still covers the success-path shape).
`go/internal/emitters/units_test.go` adds `TestToolLifecycleFailedFact`
(`errorCode` is `code ?? type`, no `durationMs`/`performance`) and
`TestToolLifecycleFailedForgetsToolName`.

### Mutation checks

Eleven deliberate mutations, all caught:

| Mutation                                               | Caught by                                             |
| ------------------------------------------------------ | ----------------------------------------------------- |
| emit `tool_call_result` instead of `tool_call_error`   | `TestHandlerFailure…`                                 |
| remove the registry-miss exit                          | `TestUnknownToolFailsBeforeStarted`                   |
| remove the input-validation exit                       | `TestBadInputFailsBeforeStarted`                      |
| emit the streaming `tool_call_started` inline          | `TestStreamingToolStartedLands…`, `TestTwoStreaming…` |
| downgrade the Edit failure to a plain CoreError        | `TestHandlerFailure…`, `TestAmbiguous…`               |
| remove the sanitisation cap                            | `TestSanitizeErrorText`                               |
| remove the whitespace collapse                         | `TestSanitizeErrorText`, `TestHandlerFailure…`        |
| drop the `<tool_use_error>` wrapper                    | `TestHandlerFailure…`, `TestFailureModelTextPerStage` |
| add an `output` to the failed row                      | `TestToolCallErrorRowHasErrorAndNoOutput`             |
| report `error.code` instead of `error.type` on the row | `TestToolCallErrorRowHasErrorAndNoOutput`             |
| drop the cancelled branch                              | `TestToolCallErrorCancellationDropsError`             |

## Status against the oracle

`compare-agents.mjs --all`, Go agent built from `go/`:

| Scenario                                                                                                     | Result                                                                                                                                  |
| ------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| `read-missing`                                                                                               | SAME                                                                                                                                    |
| `unknown-tool`                                                                                               | SAME                                                                                                                                    |
| `write-without-read`                                                                                         | SAME                                                                                                                                    |
| `edit-replace-all`                                                                                           | SAME                                                                                                                                    |
| `write-new`, `write-allow-project`, `read-range`, `unicode-text`, `empty-answer`, `long-answer`, `429-twice` | SAME                                                                                                                                    |
| `edit-no-match`, `edit-ambiguous`                                                                            | DIFFERENT — only the `messages` char count of the usage patch in the model step **after** the failed Edit (Go 594 vs Node 574, +20)     |
| `read-two-files`                                                                                             | DIFFERENT — `tool_call_scheduled` at 16 instead of 14 (see below)                                                                       |
| `bad-input`                                                                                                  | DIFFERENT — `model.request.status.attempt` is `1` in Go and absent in Node; that is the model-retry/apiRetry surface, not the tool path |

### Known gaps

- **The 20-character usage-patch delta after a failed Edit.** The breakdown
  counts `messages` by stringifying `{role, content, toolCalls, toolCallId,
toolName}` per runtime message (`runtime/methods/context-usage.ts:358-377`).
  Node and Go agree exactly on the two earlier steps (33 / 250) and on every
  other field; they diverge by exactly the length of `,"replace_all":false` once
  a _failed_ Edit is in the history, so the difference is in how the assistant
  tool-call `input` is projected for a call that failed before `resolveInput`.
  It affects only the `usage.contextWindow.breakdown` numbers the UI shows; the
  model-visible request bodies are byte-identical (verified: the request
  comparison reports SAME for both scenarios). The shared
  `emitters.ContextUsage` / `runtime/contextusage.go` accounting is outside the
  tool-failure scope and was left untouched rather than special-cased.
- **`read-two-files`' extra `tool_input_end`.** Go's model client synthesises a
  `tool_input_end` for the second tool call where the Node AI SDK does not emit
  one (the stream finishes first), so Go has one extra `model_streaming` event
  before the first `tool_call_scheduled` and everything after it is shifted by
  one. The tool lifecycle order itself now matches Node exactly
  (scheduled → started → scheduled → started → completed); only the raw sequence
  numbers after the stream differ. The fix belongs in
  `internal/modelclient`'s decoder, not the tool layer.
