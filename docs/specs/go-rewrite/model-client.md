# Model client (Go)

Status: built and verified. Owner: Agent D. Packages:

- `go/internal/modelclient/**` — pure logic: SSE parsing, chunk decoding, the
  closed event set, the tool-call assembler, the failure classifier, the retry
  policy/budget, and the streaming loop. No `net`/`net/http`/`os` imports.
- `go/internal/adapters/model/openai/**` — the `net/http` transport and the exact
  outbound header set.
- `scripts/go-rewrite/gen-model-vectors.mts` — vector generator.
- `scripts/go-rewrite/classify-failures.mts` — extended failure-classification
  vector generator (G1.7).
- `scripts/go-rewrite/mutation-modelclient.sh` — mutation check for the
  classifier and retry rules (G1.7).
- `go/testdata/vectors/model-*.json`, `go/testdata/sse/*.txt` — generated vectors
  and fixtures.

## What this reproduces

The Node agent does not implement the HTTP/SSE layer itself: it delegates to the
Vercel `ai` SDK (v6.0.193) plus `@ai-sdk/openai-compatible` (v2.0.60) plus
`eventsource-parser`. Those three are the parity oracle for parsing, finish-reason
normalisation, usage, tool-call assembly, and what counts as an error. On top of
them the agent runs its own retry loop (`runner-stream.ts`, `runner-generate.ts`)
whose classification, backoff and telemetry this package ports.

Key facts discovered by reading the SDK source and confirmed with vectors:

1. `parseJsonEventStream` = `TextDecoderStream` (streaming UTF-8) →
   `EventSourceParserStream` → `[DONE]` skip → JSON parse. So bytes are decoded to
   text **before** the SSE parser; the parser sees complete multi-byte sequences
   and a whole leading BOM. `TextDecoderStream` is why a 1-byte-at-a-time feed of a
   UTF-8/BOM stream still parses correctly. The Go port therefore inserts a
   `textDecoder` (streaming UTF-8, buffering partial sequences) ahead of the SSE
   parser, exactly like the SDK.
2. Finish reasons are normalised to the AI SDK unified set: `stop`, `length`,
   `content-filter`, `tool-calls`, `error`, `other`. Raw `function_call` and
   `tool_calls` both map to `tool-calls`; `content_filter` maps to `content-filter`.
3. A malformed chunk (bad JSON / schema failure) is **not** a thrown error: the
   SDK emits an `error` part, sets the finish reason to `error`, and continues to
   the flush. Only the flush-time invalid tool call (a delta whose
   `function.name` never arrived, or a missing `id`) throws.
4. The agent sets `maxRetries: 0` on `streamText`/`generateText`
   (`runner-options.ts`), so the SDK does **not** retry; the agent's own loop does.
   Retry-After and the classifier are therefore the agent's behaviour, not the
   SDK's.
5. `[DONE]` is skipped by the JSON layer, not the SSE parser; a missing `[DONE]`
   makes no difference — the stream simply ends and flushes.

## Event model (`event.go`)

A closed set: `start`, `text_start`, `text_delta`, `text_end`, `reasoning_start`,
`reasoning_delta`, `reasoning_end`, `tool_input_start`, `tool_input_delta`,
`tool_input_end`, `tool_call`, `usage`, `finish`, `error`. This is the subset of
`ModelStreamEvent` (@nex/contracts) that an OpenAI chat-completions response can
produce, plus stream errors. Text and reasoning blocks use the SDK's fixed ids
`txt-0` and `reasoning-0`.

## SSE parser (`sse.go`)

A faithful Go port of `eventsource-parser`'s line logic: BOM strip on the first
decoded string, buffering of a chunk with no line terminator, the no-CR fast path
(with the "`data:` line followed immediately by LF emits now" shortcut), CR/LF/CRLF
line ends, a trailing lone CR buffered, multi-line `data` joined with `\n`,
comment/`id`/`event`/`retry` fields, unknown fields ignored. `maxBufferSize` is not
implemented (the SDK does not set it).

## Decoder (`decoder.go`, `decoder_schema.go`, `decoder_toolcalls.go`)

Reproduces the `@ai-sdk/openai-compatible` `doStream` transform and flush: usage
from any chunk replaces the running usage; the first choice's `finish_reason` sets
the finish; `reasoning_content`/`reasoning` open the reasoning block; `content`
closes reasoning, opens text, emits deltas; `tool_calls` are assembled by
`index`, buffering `id`/`name` until both are known, with the SDK's exact
`tool-input-start`/`tool-input-delta`/`tool-input-end`/`tool-call` ordering and its
`InvalidResponseDataError` messages.

## Tool-call assembler (`assembler.go`) and input normalisation (`toolinput.go`)

Ports `StreamingToolCallAssembler`: dedupe by id; a `tool_call` that arrives while
a streaming input is open (no matching `tool_input_end`) is dropped; the name is
normalised (`normalizeModelToolName`) and the final JSON input parsed
(`normalizeModelToolInput`), where a missing input, empty string, JSON `null`, or
unparsable JSON all recover to `{}` and are recorded (a leading U+FEFF is
stripped). `HasProviderExecuted` distinguishes an explicit `false` from absent.

## Failure classifier (`errors.go`, `inspect.go`, `business.go`, `businesscodes.go`, …)

Ports `classifyModelFailure` and its inspectors with the same ordering and the
same constants: `invalid_model_response`/`invalid_model_request`, abort, stream
idle timeout, provider business errors (from a `ProviderBusinessError` shape, the
AI SDK `data.error` shape, or a response body), timeout, 429, 529, 401/403,
400/422 (context-exceeded detection), 5xx, TLS, proxy, network, retryable flag.
The provider business code table (`failure-provider-business-codes.ts`) is copied
verbatim, including the programmatically added terminal/retryable codes.
`parseRetryAfterMs` reproduces the `x-should-retry: false` block, `retry-after-ms`,
numeric seconds and HTTP-date handling.

The failure table is the G1.7 gate input. It is **106 vectors** in two files, both
produced by running the real `classifyModelFailure`:

- `model-failure.json` — 48 inputs from `gen-model-vectors.mts` (status codes,
  AI SDK `data.error` bodies, plain network/TLS/proxy codes, abort, idle timeout,
  the `isRetryable` flag, cause chains).
- `model-failure-extended.json` — 58 inputs from `classify-failures.mts`, which
  covers the branches the first file leaves unreached: a `ProviderBusinessError`
  thrown directly by the model-execution layer (the classifier's primary
  business path), unknown and unmapped business codes, `success: false` bodies,
  the bracketed/nested provider-code recovery, every `Retry-After` spelling
  (`retry-after-ms` precedence, fractional and negative seconds, header case,
  padding, `x-should-retry: 0`), status boundaries (402/451/418/599), the
  remaining network/timeout/TLS/proxy codes, every context-exceeded phrase
  including a negative `max_tokens` case that must **not** be treated as a
  context overflow, and abort/TLS/context signals arriving through a `cause`.

## Retry loop verification (`retry_clock_test.go`)

`Clock` is the package's own interface (`client.go`); no clock library is
introduced. `recordingClock` advances a virtual `now` on `Sleep` and records every
delay, so the retry runs cost no wall-clock time. `TestRetryWithFakeClock*` drives
the real `Stream` loop over a `fakeTransport`:

| test                                          | covers                                                                                                               |
| --------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `…429RetryAfter`                              | Retry-After (3 s) overrides the 2000 ms curve; 2 attempts, 1 retry event                                             |
| `…500Backoff`                                 | 3× 500 then success; delays 2000/4000/8000, 4 attempts, 14000 ms total                                               |
| `…NetworkReset`                               | 2× `ECONNRESET` then success; delays 2000/4000; failures classify `network_error` with no status code                |
| `…ExhaustsBudget`                             | `maxAttempts: 3` yields 3 attempts, 2 retries, then a terminal `model_rate_limited`                                  |
| `…TerminalIsNotRetried`                       | 401 makes one attempt, schedules no retry, and never advances the clock                                              |
| `…CancelledContext` / `…SleepErrorIsReturned` | a failing `Sleep` ends the loop instead of silently retrying immediately                                             |
| `…BackoffCap`                                 | attempt 7 caps at 60 s; an absurd Retry-After loses to the cap                                                       |
| `…EmptyCompletion`                            | `EMPTY_COMPLETION_MAX_RETRIES`/`EmptyCompletionFailure` semantics, and that `Stream` itself does not judge emptiness |

`Stream` now propagates the real `Clock.Sleep` error instead of `ctx.Err()`: a
sleep that fails while the context is still live used to return `nil`, silently
turning a failed backoff into an immediate retry.

`scripts/go-rewrite/mutation-modelclient.sh` breaks one classifier or retry rule
at a time and asserts the suite fails. All 30 mutations are caught, including the
429/529/401/403/5xx branches, the network/timeout/TLS/proxy predicates, the
business-code table, each context-exceeded clause, both Retry-After rules, the
backoff exponent and cap, the budget gate and the retry boundary.

## Retry policy (`retrypolicy.go`, `retrydelay.go`)

`resolveAiSdkModelRetryOptions`, `calculateRetryDelay` (base 2s, factor 2, cap 60s,
jitter ×[0.5,1.0], Retry-After wins when "reasonable"), the retry budget
(`default`/`unbounded` with sentinel `0`), empty-completion retry
(`maxRetries 1`), and the stream idle timeout (default 600000 ms, +30000 ms per
retry). `maxAttempts` defaults to `10 + 1 = 11`, matching S13's `maxAttempts: 11`.

## Streaming loop (`stream.go`, `streamutil.go`, `status.go`)

`Client.Stream(ctx, Request)` returns a `StreamHandle` whose channel carries
decoded events; `Wait()` returns the terminal error. Per attempt it publishes
`model_request_started`, then either `model_request_completed` or
`model_request_failed` (+ `model_retry_scheduled` + backoff when retryable).
Retry gates: setup failures are gated only by budget + `isRetryableFailure`; a
stream failure additionally requires that no visible output was committed (the
"retry boundary"). A partial attempt that only produced retry-safe prelude events
is dropped and replayed. The `StatusSink` seam is what the lead's turn loop turns
into the `v4/telemetry/event` frames (S13).

## HTTP adapter (`internal/adapters/model/openai/**`)

`Transport.Do` sends the body produced by `go/internal/modelreq` (taken as
`[]byte`) with the header set captured from the Node agent
(`NEX_REC_BODY_DIR=… node record-agent-session.mjs`). `BuildHeaders` reproduces
every Nex/attribution header byte-for-byte (authorization, http-referer,
user-agent with the `ai-sdk/provider-utils/4.0.39 runtime/node.js/<major>` suffix,
x-client-language, x-client-timezone, x-nex-agent, x-nex-app-version,
x-nex-session-type, x-nex-trace-id, x-os-category, x-os-version, x-platform,
x-query-id, x-release-channel, x-request-id, x-session-id, x-title). Values that
vary per run (ids, version, platform, os) are `HeaderInput` fields. `host`,
`connection`, `content-length`, `accept*` and `sec-fetch-mode` are transport
concerns (undici/Go defaults) and are not reproduced; gateways do not key on them.
Network errors are mapped to the classifier's shape (`ECONNRESET`, `EPIPE`,
timeout, TLS, abort) so mid-stream disconnects classify like Node's.

## How it was verified

Vectors are generated by **running the real TypeScript**: `gen-model-vectors.mts`
drives `OpenAICompatibleChatLanguageModel.doStream` (real SDK) over authored SSE
fixtures, feeds the resulting events through the real `StreamingToolCallAssembler`,
and runs the real `classifyModelFailure` over 48 inputs. It also emits 1/2/3-byte
chunked decodes, retry-delay, retry-option and idle-timeout vectors. Run it with:

```
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-model-vectors.mts
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/classify-failures.mts
```

Both are byte-stable: re-running them reproduces the committed files exactly.

Go tests reproduce every vector exactly:

- `TestDecoderMatchesVectors` (20 SSE fixtures, whole read),
  `TestDecoderSplitMatchesVectors` (1/2/3-byte chunks), `TestDecoderSplitAtEveryByte`
  and `TestDecoderSplitAtEveryBoundary` (against the SDK's own split results).
- `FuzzDecoderNeverPanics` — 770k+ execs, no panic, byte-split ⊆ whole.
- `TestAssemblerMatchesVectors`, `TestAssemblerDedupesByID`.
- `TestFailureClassifierMatchesVectors` (106 inputs across both files; also
  asserts the names are unique), `TestFailureClassifierCoversEveryClass` (every
  reason and retry reason is reached, and the corpus stays balanced between
  retryable and terminal outcomes), `TestS13ScenarioRetryClassification`,
  `TestMidStreamDisconnectClassification`.
- `TestRetryDelayMatchesVectors`, `TestRetryOptionsMatchVectors`,
  `TestIdleTimeoutMatchesVectors`, `TestRetryBudgetBoundaries`,
  `TestCalculateRetryDelayJitterRange`.
- `TestRetryWithFakeClock*` (see above).
- `TestNormalizeToolInput`, `TestNormalizeToolName`,
  `TestProviderBusinessDetectionFromBody`.
- `TestStream*` (happy path, idle timeout, retry boundary).
- Mutation: `scripts/go-rewrite/mutation-modelclient.sh` — 30/30 caught.
- Adapter: `TestBuildHeadersMatchCapture` (vs the recorded capture), and
  `TestS13RetryThenSuccess`, `TestSelfRetryOnServerError` (httptest), plus
  `TestAgainstNodeMockModel`, which drives the **real** `mock-model.mjs` from a
  node subprocess and streams its bytes through the Go client.

Mutation checks (deliberately broken code; every one failed ≥1 test): BOM strip,
CRLF line ends, UTF-8 partial buffering, tool-call index handling, all finish-reason
mappings, provider-code mappings, context-exceeded phrases, retry-after-ms and
`x-should-retry`, 429/529/401/403 branches, backoff exponent, maxAttempts, idle
increment, assembler dedupe and open-stream drop, tool-input null/BOM recovery,
empty-name-with-id, retry boundary gate, budget boundary, jitter range, and every
adapter header.

## Known gaps

- The header set omits undici-only defaults (`accept`, `accept-language`,
  `accept-encoding`, `sec-fetch-mode`, `connection`, `host`); Go's `net/http`
  sets its own. Rendered capture lines in `headers_test.go` are the source of
  truth for what is asserted.
- Reasoning/`thoughtSignature` provider metadata is decoded but not threaded into
  an emitted metadata field (no fixture exercised it); only the field plumbing
  exists.
- The `modelIoFullRetentionEnabled` debug capture (`runner-debug.ts`) is not
  ported — it writes request/response dumps, which is an adapter-side concern the
  lead can add behind the transport.
- Provider authentication/`resolveModelForAttempt` runtime-header refresh,
  off-peak (`offpeak-retry.ts`), reasoning-signature repair, and the
  `request-admission` queue are **not** in scope here; the `Stream` API takes an
  already-resolved `Request` (URL + headers + body) and an optional admission is
  expected to wrap the transport or the lead's loop.
- `createAttemptStatusContext` rotates the request id per attempt in Node; here a
  caller-supplied `x-request-id` is reused across attempts, so S13's two attempts
  share `requestId` where Node would mint a new one. The lead can thread a
  per-attempt id via the status sink if exact id parity is needed.

## Verify commands

```
cd go
gofmt -l internal/modelclient internal/adapters/model/openai   # expect empty
go vet ./internal/modelclient/ ./internal/adapters/model/openai/
go test -race -count=1 ./internal/modelclient/ ./internal/adapters/model/openai/
```
