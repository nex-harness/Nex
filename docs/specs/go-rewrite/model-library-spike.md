# Model streaming library spike (D14)

Status: **spike complete, recommendation for sign-off**. This document only
replaces the two candidate rows in `decisions.md` ("OpenAI Responses stream",
"Anthropic Messages stream"). No other file was modified.

Everything below was verified by building and running throwaway probes against
the real modules in the module cache, not by reading marketing docs. Probe code
lived in `/tmp` and is not part of the repo.

## Recommendation

| API                                 | Choice                                   | Pin         | Confidence                                          |
| ----------------------------------- | ---------------------------------------- | ----------- | --------------------------------------------------- |
| OpenAI Responses (`/v1/responses`)  | `github.com/openai/openai-go`            | **v1.12.0** | high, with one blocker to work around (R1)          |
| Anthropic Messages (`/v1/messages`) | `github.com/anthropics/anthropic-sdk-go` | **v1.80.0** | high, with one behavioural hole to work around (R3) |
| OpenAI Responses (rejected)         | `github.com/sashabaranov/go-openai`      | v1.43.0     | do not adopt — see "Rejected"                       |

Both are the official Stainless-generated SDKs. They are the same code
generator, the same `option` API, and the same `ssestream` package, so adopting
both is cheaper than adopting two different idioms.

Version pins are exact (no `^`-style ranges in Go, but pin the minor: see R5).

## How this was verified

```
cd /tmp/probe   # module with tools.json copied from go/internal/tooldefs/
go get github.com/openai/openai-go@v1.12.0
go get github.com/anthropics/anthropic-sdk-go@v1.80.0
```

Probes run, all compiling and executing against Go 1.24.4:

1. **Tool JSON** — parsed the real 20-entry `go/internal/tooldefs/tools.json`,
   mapped it into each SDK's request param type, marshalled, unmarshalled, and
   compared the emitted JSON Schema against the source.
2. **Streaming** — drove a hand-written SSE script through `NewStreaming`
   against an `httptest` server, asserting every event type we need.
3. **Fixture coverage** — ran the repo's 20 fixtures in
   `go/testdata/sse/*.txt` through each library's SSE decoder, whole-read and at a
   1-byte-per-`Read` split. This is the D14 gate.
4. **Divergences / risks** — targeted probes for BOM, CR line endings, `[DONE]`,
   truncation, error objects, unknown event names, header control, BaseURL
   joining.

---

## API 1 — OpenAI Responses: `github.com/openai/openai-go` v1.12.0

### 1. Does the responses stream API exist, and is it stable?

Yes. `responses/response.go:76`:

```go
func (r *ResponseService) NewStreaming(ctx context.Context, body ResponseNewParams, opts ...option.RequestOption) (stream *ssestream.Stream[ResponseStreamEventUnion])
```

It is stable in the ordinary sense: v1.12.0 is a plain `v1` release off the
`v0.1.0-alpha.*` → `v0.1.0-beta.*` → `v1.0.0` line, and the module has 25
releases in the v1 series. It is _not_ API-frozen: the file carries the header
`// File generated from our OpenAPI spec by Stainless`, and fields are added
when OpenAI's spec changes.

There is also `GetStreaming` for resuming a response, and
`internal/adapters`-style control via `option`.

### 2. Does it expose every SSE event type we need?

Yes, and more. `responses/response.go:10042` defines `ResponseStreamEventUnion`
as a **flattened union struct discriminated by `Type string`**. Every field of
every variant is present on the one struct, so you switch on `ev.Type` and read
fields directly. The doc comment enumerates 50 event names, including every one
in the closed set:

| Our closed event    | OpenAI event                                                          | Field                                                                                                                  |
| ------------------- | --------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `TextDelta`         | `response.output_text.delta`                                          | `Delta.OfString`                                                                                                       |
| `ReasoningDelta`    | `response.reasoning_summary_text.delta`                               | `Delta.OfString`                                                                                                       |
| `ToolCallStart`     | `response.output_item.added` (item type `function_call`)              | `Item.Type`, `Item.Name`, `Item.ID`, `Item.CallID`                                                                     |
| `ToolCallArgsDelta` | `response.function_call_arguments.delta`                              | `Delta.OfString`                                                                                                       |
| `ToolCallEnd`       | `response.function_call_arguments.done` / `response.output_item.done` | `Arguments`                                                                                                            |
| `Usage`             | `response.completed` → `.Response.Usage`                              | `InputTokens`, `OutputTokens`, `TotalTokens`, `InputTokensDetails.CachedTokens`, `OutputTokensDetails.ReasoningTokens` |
| `Finish`            | `response.completed` / `.incomplete`                                  | `Response.Status`, `Response.IncompleteDetails.Reason`                                                                 |
| `Error`             | `response.failed`                                                     | `Response.Error.Code`, `.Message`                                                                                      |

Verified by probe, decoding a synthetic stream with reasoning, two
function-arg deltas, text, `response.completed` (with
`cached_tokens=2`, `reasoning_tokens=3`) and `response.failed`:

```
event response.output_item.added            item.type=function_call item.name=Agent
event response.function_call_arguments.delta delta="{\"sub"
event response.function_call_arguments.delta delta="agent_type\":\"explore\"}"
event response.reasoning_summary_text.delta  delta="thinking..."
event response.output_text.delta             delta="Hello" item=msg_1
event response.completed  in=10 cached=2 out=5 reasoning=3 total=15 status=completed
event response.failed     status=failed code=rate_limit msg=slow down
```

Also present and therefore free: `response.output_item.added/done` (gives us
`call_id` vs `id`, which chat-completions conflates), `response.incomplete`,
`response.refusal.delta`, `response.output_text.annotation.added`.

**Caveat on ergonomics**: unlike Anthropic, there is no `AsAny()` — you switch
on `ev.Type` yourself and every event carries every field, so a missing field
reads as a zero value rather than a compile error. This is the main ergonomic
cost of the choice. It is tolerable because our own decoder already switches on
a discriminator.

### 3. Can we pass arbitrary tool JSON?

Yes, and this was the decisive question. `responses/response.go:649`:

```go
type FunctionToolParam struct {
	Strict     param.Opt[bool] `json:"strict,omitzero,required"`
	Parameters map[string]any  `json:"parameters,omitzero,required"`
	Name       string          `json:"name,required"`
	...
}
```

`Parameters` is an untyped `map[string]any`, so an arbitrary JSON Schema passes
through with no schema generation and no `invopop/jsonschema`. Requests take
`Tools []ToolUnionParam` (`response.go:12891`); set `OfFunction`.

Probe result with the real 20-tool file: all 20 tools marshalled, 39 906-byte
body, first tool emitted with `type=function`, `$schema` and
`additionalProperties` preserved, 4 properties, and a semantic round-trip
comparison of the emitted schema against the source JSON returned `true`.

**One shape difference to handle.** `tools.json` is stored in chat-completions
form (`{"type":"function","function":{...}}`). The Responses API wants the flat
form (`{"type":"function","name":...,"parameters":{...}}`). The SDK emits flat.
So the loader must flatten one level when it targets Responses and keep the
nested form for chat-completions. This is a ~10-line adapter, not a schema
migration, and it is a real behavioural difference from today's chat path that
needs its own vector test.

### 4. Does it expose `usage`?

Yes, and at finer granularity than our current `Usage` struct — see the table
above. `InputTokensDetails.CachedTokens` and
`OutputTokensDetails.ReasoningTokens` cover the cached/reasoning fields our
`Usage` already models. Note the field naming changes from chat-completions
(`prompt_tokens`/`completion_tokens`) to Responses (`input_tokens`/
`output_tokens`); the mapping is mechanical.

### 5. Can we override `BaseURL`?

Yes — `option.WithBaseURL` (`option/requestoption.go:28`), and it is **required**
for us because the proxy is `https://ai-proxy.tylinux.cn/v1`. Verified path
joining: `BaseURL(srv.URL+"/v1")` produces `POST /v1/responses`, i.e. the base
path is preserved and the SDK appends only `responses`.

### 6. Custom headers / HTTP client?

Yes, all of it, verified by probe:

- `option.WithHeader` / `WithHeaderAdd` / `WithHeaderDel` (`requestoption.go:111/120/128`)
- `option.WithHTTPClient` (`:58`)
- `option.WithMiddleware(...)` (`:87`) — `type Middleware = func(*http.Request, MiddlewareNext) (*http.Response, error)`
- `option.WithAPIKey` / `WithBearerToken` (`:270`)

Measured: our headers (`x-nex-agent`, `x-nex-session-id`) arrived intact;
`Accept: application/json` was overridden to `text/event-stream`; `User-Agent`
was overridden to `nex-go/0.0.0`; `X-Stainless-Retry-Count` and
`X-Stainless-Timeout` were deleted. Six vendor telemetry headers remain
(`X-Stainless-Arch/Lang/Os/Package-Version/Runtime/Runtime-Version`) and each
is individually deletable via `WithHeaderDel` if D7 ever requires a closed
header set.

### 7. What does `sashabaranov/go-openai` cover for Responses?

It has Responses support — `response.go` + `response_stream.go`, added by v1.43.0
— and it is a **thin, hand-written wrapper**, not generated. 728 lines total
versus openai-go's ~14 000. `ResponseStreamEvent` is a flat struct with a typed
`Type` enum covering ~50 events, plus one genuinely nice feature openai-go lacks:

```go
type ResponseStreamEvent struct {
	// ...
	Raw json.RawMessage `json:"-"`
}
```

`UnmarshalJSON` keeps the whole event for forward compatibility
(`response_stream.go:97-106`), which is a real advantage over openai-go's
`respjson.Field` approach for a proxy that may emit fields we have not seen.

But it loses on the things that matter here:

- **17/20 of our fixtures** decode cleanly, versus 20/20 for both official SDKs.
  It fails on `invalid-json.txt`, `multiline-data.txt`, and
  `sse-error-object.txt`. It also yields _fewer_ events per fixture than the
  official SDKs (2 vs 3 on `content-filter.txt`, 4 vs 5 on `text-basic.txt`),
  because its `streamReader.processLines` (`stream_reader.go:57`) only
  understands `data:` lines and **discards the SSE `event:` field entirely** —
  it is built around chat-completions framing.
- It has **no retry logic at all** (`grep -rn "retry|backoff" client.go` → no
  matches). That is not a blocker (we supply our own retry loop anyway) but it
  means the wrapper adds nothing there.
- Tool typing is `Parameters any` on `FunctionDefinition` with a `map[string]any`
  in practice — workable, but `NewResponseFunctionTool` hand-builds the JSON.

The D14 gate says "a library swap must demonstrate equal or better coverage".
17/20 is worse than the 20/20 we already have. Rejected on evidence.

### 8. Downside of pulling it in?

Measured with `-trimpath -ldflags="-s -w"` against a `net/http` baseline:

| Binary                          | Size     | Δ vs baseline |
| ------------------------------- | -------- | ------------- |
| `net/http` baseline             | 5.36 MB  | —             |
| + `sashabaranov/go-openai`      | 5.94 MB  | +0.58 MB      |
| + `openai-go` (Responses)       | 8.19 MB  | **+2.83 MB**  |
| + `anthropic-sdk-go` (Messages) | 11.11 MB | +5.75 MB      |

(openai-go alone was 12.11 MB unstripped before I forced the streaming paths to
be linked; the 8.19 MB figure is with live references and stripped symbols, and
is the number to quote.)

Third-party dependencies: openai-go pulls in only
`tidwall/gjson` + `tidwall/sjson` (+ `match`/`pretty`). Its `go.mod` also lists
`Azure/azure-sdk-for-go` and `azidentity`, but **neither is linked** into a
Responses-only binary — verified: `go list -deps` contains zero `Azure`
packages. Extra packages linked: 31, of which 19 are the SDK itself.

No cgo. `go 1.21` directive, so Go 1.24 is fine with headroom.

---

## API 2 — Anthropic Messages: `github.com/anthropics/anthropic-sdk-go` v1.80.0

### 1. Does it handle Messages streaming?

Yes. `message.go:87` returns `*ssestream.Stream[MessageStreamEventUnion]` and
forces `option.WithJSONSet("stream", true)`.

The union is **strongly typed with an `AsAny()` discriminator**
(`message.go:11395`), the opposite design choice to openai-go and the better one:

```go
switch variant := MessageStreamEventUnion.AsAny().(type) {
case anthropic.MessageStartEvent:
case anthropic.ContentBlockDeltaEvent:
...
}
```

The six events are `message_start`, `message_delta`, `message_stop`,
`content_block_start`, `content_block_delta`, `content_block_stop`.

### 2. Thinking blocks and beta headers

`thinking_delta` and `signature_delta` are both first-class:
`RawContentBlockDeltaUnion` (`message.go:10742`) has
`Text`, `PartialJSON`, `Thinking`, `Signature`, discriminated by `Type` with its
own `AsAny()`. Content-block start carries `Thinking`, `Signature` and `Data`
(redacted) on `ContentBlockStartEventContentBlockUnion` (`message.go:10890`),
with `AsText()`, `AsThinking()`, `AsRedactedThinking()`, `AsToolUse()`.

Verified end-to-end against an `httptest` server with a thinking block, a
signature delta, a text block, a tool_use block, and a trailing `ping`:

```
message_start          input=25 out=1 cache_read=0
content_block_start    idx=0 block.type=thinking
content_block_delta    idx=0 thinking="let me think"
content_block_delta    idx=0 signature="SIGABC"
content_block_stop     idx=0
content_block_start    idx=1 block.type=text
content_block_delta    idx=1 text="Hello"
content_block_stop     idx=1
content_block_start    idx=2 block.type=tool_use id=toolu_1 name=Agent
content_block_delta    idx=2 partial_json="{\"sub"
content_block_delta    idx=2 partial_json="agent_type\":\"x\"}"
content_block_stop     idx=2
message_delta          stop_reason="tool_use" usage.out=42
message_stop
```

`ping` was correctly skipped rather than surfaced. Stop reasons are typed
(`end_turn`, `max_tokens`, `stop_sequence`, `tool_use`, `pause_turn`,
`refusal`, `model_context_window_exceeded`) — note
`model_context_window_exceeded`, which our classifier's context-exceeded
detection cares about.

**Beta headers: the SDK manages none.** `grep -rn "anthropic-beta"` over
`internal/requestconfig` and `client.go` returns nothing; the only beta-ish
header it sets itself is `anthropic-user-profile-id`/`anthropic-workspace-id`
from param fields (`message.go:92-97`). So every `anthropic-beta` we need
(e.g. `fine-grained-tool-streaming-2025-05-14`) is ours to set via
`option.WithHeaderAdd`, and adding a second one appends rather than overwrites
(`option/requestoption.go:236`). Verified: our beta value arrived intact.

### 3. Tool definitions — arbitrary JSON Schema?

Yes, with a slightly different mechanism. `ToolInputSchemaParam`
(`message.go:13181`):

```go
type ToolInputSchemaParam struct {
	Properties  any             `json:"properties,omitzero"`
	Required    []string        `json:"required,omitzero"`
	Type        constant.Object `json:"type" default:"object"`
	ExtraFields map[string]any  `json:"-" api:"extrafields"`
	paramObj
}
```

`Properties any` takes the schema body verbatim and `ExtraFields` carries the
rest (`$schema`, `additionalProperties`, …), marshalled via
`param.MarshalWithExtras`. All 20 of our tools built and marshalled
(39 415 bytes) with `$schema` and `additionalProperties` preserved. No
`invopop/jsonschema` needed at runtime (it is in `go.mod` for the SDK's own
`tools` helper package, but is not linked into a Messages-only binary — verified:
it does not appear in `go list -deps ./anthro/` output).

### 4. BaseURL and HTTP client?

Yes: `option.WithBaseURL` (`:144`), `option.WithHTTPClient` (`:174`),
`WithHeader`/`WithHeaderAdd`/`WithHeaderDel` (`:227/236`), `WithMiddleware`
(`:203`), `WithAPIKey` (`:633`). Same signatures as openai-go.

**Operational trap, measured.** The Anthropic SDK appends the literal path
`v1/messages` (`message.go:101`), whereas openai-go appends just `responses`.
So the two SDKs want _different_ BaseURL conventions:

| BaseURL                                     | Result                   |
| ------------------------------------------- | ------------------------ |
| openai-go, `https://ai-proxy.tylinux.cn/v1` | `POST /v1/responses` ✓   |
| anthropic, `https://ai-proxy.tylinux.cn`    | `POST /v1/messages` ✓    |
| anthropic, `https://ai-proxy.tylinux.cn/v1` | `POST /v1/v1/messages` ✗ |

If both point at the same proxy origin, we must store the bare origin and let
each adapter append its own suffix, or normalise once in a config helper. Worth
a unit test; it fails silently as a 404 that classifies as a generic 4xx.

### 5. Cost: dependency tree and binary size

+5.75 MB stripped, 53 extra packages — the heaviest of the three. But the
weight is **not** the cloud SDKs: `go.mod` lists `aws-sdk-go-v2`,
`google.golang.org/api`, `grpc`, `otel`, `creack/pty`, `modelcontextprotocol/go-sdk`,
and those are only reachable through the `bedrock`/`vertex`/`googlecloud`/
`aws` sub-packages. None are linked into a Messages-only binary (verified: zero
`amazonaws`/`cloud.google`/`grpc`/`otel` packages in the delta). Actual linked
third-party set is `tidwall/gjson`+`sjson`, `bahlo/generic-list-go`,
`buger/jsonparser`, `invopop/jsonschema`, `pb33f/ordered-map/v2`,
`standard-webhooks/…`, `golang.org/x/sync`, `go.yaml.in/yaml/v4`.

No cgo. `go 1.24` — exactly our version. Its `toolchain go1.25.8` line is
advisory; verified that `GOTOOLCHAIN=local go build` on our go1.24.4 succeeds
without downloading another toolchain.

Both SDKs coexist without conflict: gjson unifies to v1.18.0, and the two
`ssestream` packages are distinct import paths, so there is no collision.

---

## What the library handles for us

- HTTP: method/path construction, `Content-Type`, request body marshalling,
  context propagation and cancellation, `X-Stainless-Timeout` plumbing.
- SSE: line framing, `data:` accumulation with `\n` joins, `[DONE]`,
  `ping` suppression, chunk-boundary-safe `bufio` buffering, and JSON decoding
  into typed structs with `respjson.Field` presence tracking (which is what lets
  us distinguish "absent" from zero in `Usage`).
- Errors: `apierror.Error` with `StatusCode int`, `Request *http.Request`,
  `Response *http.Response`, and `RawJSON()`. **This is exactly the classifier's
  input set** (`errors.go` / `inspect.go` need status, headers and body).
- Header control: full set/append/delete plus a middleware chain for telemetry.
- Usage extraction with sub-details.

## What we still have to build

1. **Event mapping to our closed set.** Roughly 200 lines per API, and it is the
   real work. Must produce `start`, `text_start/delta/end`,
   `reasoning_start/delta/end`, `tool_input_start/delta/end`, `tool_call`,
   `usage`, `finish`, `error` from the two dialects. Anthropic needs an
   index-keyed block state machine (a block's kind is known only at
   `content_block_start`, and deltas are positional); OpenAI Responses is mostly
   flat but `output_item.added` vs `function_call_arguments.delta` ordering is
   not guaranteed, so `ToolCallStart` must still buffer until id+name are known.
2. **Finish-reason normalisation.** Our `event.go` constants are the AI SDK's
   unified set (`stop`, `length`, `content-filter`, `tool-calls`, `other`,
   `error`), which is _chat-completions_ semantics. Neither dialect speaks it:
   Anthropic gives `end_turn`/`max_tokens`/`stop_sequence`/`tool_use`, Responses
   gives `status=completed|failed|incomplete` plus `incomplete_details.reason`.
   Two new mapping tables + vectors.
3. **Business-code classifier integration.** `businesscodes.go` is ported
   verbatim from Node and is the parity oracle for 58 of the 106 failure vectors.
   The SDKs do not replace it. Map `apierror.Error` → our classifier input shape
   and keep the 106 vectors green unchanged.
4. **Retry policy integration.** `retrypolicy.go` owns backoff, jitter, the
   Retry-After parsing (`retry-after-ms`, `x-should-retry`, HTTP-date) and the
   budget. Must **set `option.WithMaxRetries(0)`** on both clients, mirroring
   `maxRetries: 0` in the Node agent, or the SDK's own loop (default
   `MaxRetries: 2`, `internal/requestconfig/requestconfig.go:166`) will retry
   underneath ours and double-count attempts. Note `shouldRetry` at `:242`
   already honours `x-should-retry`, which overlaps our parser — with
   `MaxRetries(0)` that code is simply never reached.
5. **Telemetry hooks.** `option.WithMiddleware` is the seam for
   `model_request_started/completed/failed` and `model_retry_scheduled`, which
   currently flow through `StatusSink`. Middleware sees the request, not the
   decoded events, so per-event telemetry still comes from our mapping layer.
6. **Tool-loader split.** Flatten `tools.json` for Responses, keep nested for
   chat-completions (see §3 above), with a vector test.
7. **Truncation detection.** See R3 — the libraries do not signal it.

## Risks

**R1 — openai-go aborts the stream on a top-level `"error"` key, even when it is
`null`.** `packages/ssestream/ssestream.go` does
`gjson.GetBytes(data, "error"); if ep.Exists() { s.err = ...; return false }`.
`gjson.Exists()` is true for an explicit `null`. Measured:

```
data: {"type":"response.output_text.delta","delta":"hi","error":null}  -> 0 events, err=received error while streaming:
data: {"type":"response.output_text.delta","delta":"hi"}               -> 1 event,  err=<nil>
```

`Response.error` is `null` on every successful Responses payload, and proxies
that echo the full object make this likely. _Mitigation:_ feed the SDK's
`http.Response` body through our own framing (`internal/modelclient/sse.go`,
which already handles this correctly — the `sse-error-object.txt` fixture
decodes cleanly) and JSON-decode into `ResponseStreamEventUnion` directly, using
the SDK only for request building. That costs us the "library handles SSE"
benefit for OpenAI but keeps every other gain. **This is the one blocker to
resolve before committing.**

**R2 — neither SDK strips a BOM or handles CR-only line endings.**
`eventStreamDecoder` is built on `bufio.Scanner`, which splits on `\n` only.
Measured: a CR-only stream yields 0 events from both libraries _and_ from our
own parser (all three agree), so this is not a regression — but the SDKs are
_less_ tolerant than the SSE spec. Our 20 fixtures are LF/CRLF only and all
pass. Low risk; the BOM survives into `data` in both libraries and in ours.

**R3 — silent truncation is not signalled.** A stream that ends without
`[DONE]` and without a terminal event returns `Err() == nil` in both SDKs, so a
mid-stream proxy cut looks like a clean finish. Measured on both. Our current
`Stream` has the same property (`model-client.md` says "`Stream` itself does not
judge emptiness"; the empty-completion retry is the agent's job), so this is
existing behaviour, not new — but it becomes more likely once a proxy is in the
path, so a terminal-event assertion belongs in the new mapping layer.

**R4 — anthropic silently drops any SSE event whose `event:` name is not in a
hardcoded allow-list.** `packages/ssestream/ssestream.go` `Stream.Next` switches
on `s.decoder.Event().Type` against a literal list of ~55 names and has no
`default`. Measured: a stream containing `event: response.completed` followed by
`event: message_start` yielded only `message_start`; and a stream with **no
`event:` lines at all** yielded 0 events with `err=nil`. If our proxy ever
omits `event:` lines, or forwards an OpenAI-Responses event name through the
Anthropic path, events disappear without error. Anthropic's own API always sets
both `event:` and a matching `type`, so the mitigation is: **branch on the JSON
`type` field, not the SSE `event:` field**, and add a test that a data-only
stream still decodes. This is a stronger argument for our own framing here than
it is for openai-go, where the decoder is allow-list-free.

**R5 — version churn.** Both SDKs are generated from a live OpenAPI spec and
add fields on every release (anthropic alone lists 15 dated variants of
`ToolTextEditor*`). Pin the minor, and keep the tool schema mapping in one file
so a bump is a one-line diff plus `go test`.

**R6 — the Anthropic module's `toolchain go1.25.8` line.** Harmless today
(verified building under `GOTOOLCHAIN=local` with go1.24.4), but if the repo
later sets `GOTOOLCHAIN=auto` without network access, that line could attempt a
download. Note it if the CI toolchain policy changes.

**R7 — Cloud SDKs in `go.mod`.** Both modules require `aws-sdk-go-v2`,
`google.golang.org/api`, `grpc` and `otel` at the _module_ level. They are not
linked today, but `go mod tidy` will keep them in `go.sum` and they will appear
in `go mod graph`. Not a build-size or runtime cost, but it will look alarming
in dependency audits; the mitigation is that they are only reachable via
sub-packages we never import.

**R8 — header set drift.** The SDKs add 15-16 vendor headers by default. D7
forbids new env vars, not headers, and `WithHeaderDel` gives full control, so
this is manageable — but the outbound header set must be pinned by a test like
`TestBuildHeadersMatchCapture`, or a dependency bump will silently change what
the proxy receives.

## D14 gate result

The rule is "a library swap must demonstrate equal or better coverage" than the
20 fixtures plus the fuzz target. Measured, all 20 fixtures, whole-read and at a
1-byte-per-`Read` split:

| Decoder                          | Whole read clean | 1-byte split clean    | Events            |
| -------------------------------- | ---------------- | --------------------- | ----------------- |
| `internal/modelclient` (current) | 20/20            | 20/20                 | 65                |
| `openai-go` `ssestream`          | **20/20**        | **20/20**             | 65                |
| `anthropic-sdk-go` `ssestream`   | **20/20**        | **20/20**             | 65                |
| `sashabaranov/go-openai`         | 17/20            | not tested (rejected) | fewer per fixture |

Both official SDKs tie the current implementation exactly. The two remaining
gaps in the gate are:

- The 106 failure vectors cannot be replayed through the SDKs — they test
  `classifyModelFailure`, which the SDKs do not replace. They stay green
  unchanged; the new work is proving `apierror.Error` maps onto their inputs.
- The fuzz target has no library equivalent. If we keep our own framing
  (which R1 and R4 both push us toward) the fuzz target keeps protecting the
  parser. If we hand framing to the SDK, we lose that coverage and D14's
  "equal or better" claim weakens.

Both risks point the same way: **keep `internal/modelclient/sse.go` as the
framing layer and use the SDKs for request construction, typed decoding, and
error plumbing.** That is not a retreat to hand-rolling — it is the split that
keeps 20/20, the fuzz target, and the BOM/CR/`error`-null behaviour we already
have, while deleting the hand-written request marshalling and response typing.
Adopt that shape unless the spike's implementer finds a way to get
`ResponseStreamEventUnion` decoding that survives `"error": null` (R1).

## Suggested `decisions.md` rows

| Concern                   | Library                       | Version pinned | Why                                                                                                                                                                                         |
| ------------------------- | ----------------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OpenAI Responses stream   | `github.com/openai/openai-go` | `v1.12.0`      | official; every event in the closed set; `Parameters map[string]any` takes our raw tool schema; `WithBaseURL`/headers/middleware cover the proxy; 20/20 fixtures. Keep our SSE framing (R1) |
| Anthropic Messages stream | `github.com/anthropic-sdk-go` | `v1.80.0`      | official; `thinking_delta`/`signature_delta`/`input_json_delta` all typed; `ExtraFields` takes our schema; manages no beta headers so we own them. BaseURL must be the origin (R4)          |
| Generic SSE               | n/a                           | —              | not needed: both SDKs ship `ssestream`, and it ties 20/20 rather than beating it                                                                                                            |
