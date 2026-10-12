# v4 conversation topic publisher (Go)

Agent C deliverable. Owner: this document, `go/internal/v4pub/**`,
`scripts/go-rewrite/gen-publisher-vectors.mts`,
`go/testdata/vectors/publisher*.json`, `go/testdata/vectors/publisher-scripts/**`.

Status: implemented and verified against the real TypeScript. Part of the v4
subscribe/publish side of the agent rewrite.

## What this is

The CLI-side authority for one `conversation/<sessionId>` topic: the bounded
in-memory delta log (logEpoch + retention window), the `subscribe(base)`
resume/snapshot verdict, and the per-subscriber flush pipeline
(filter → coalesce → frame). Ports
`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/conversation-topic-publisher.ts`
(1,131 lines) plus the pure functions it depends on: `coalesce.ts`,
`profiles.ts`, `workflow-runs-legacy.ts`, `conversation-workflow-run-deltas.ts`,
and the `workflow-runs-delta.ts` merge used by coalesce rule 6.

Out of scope (owned elsewhere): the product projection that turns
`SessionEvent`s into deltas/snapshots (Agent B), the RPC/stdio envelope dispatch
(`internal/protocol`, `internal/agent`), and `command-inbox.ts` (command
admission and query; it is not on the publish/subscribe path).

## Where the source lives

| Concern                                                  | TypeScript                                                            | Go                                          |
| -------------------------------------------------------- | --------------------------------------------------------------------- | ------------------------------------------- |
| Publisher shell (log, subscribe, buffers, reserve/flush) | `conversation-topic-publisher.ts`                                     | `publisher.go`, `subscribe.go`, `resync.go` |
| Limits + delivery profiles                               | `packages/shared/src/nex-protocol-v4/core.ts`                         | `core.go`                                   |
| Coalesce rules                                           | `packages/shared/src/nex-protocol-v4/coalesce.ts`                     | `coalesce.go`, `coalesce_bytes.go`          |
| Profile filter                                           | `packages/shared/src/nex-protocol-v4/profiles.ts`                     | `profile_filter.go`                         |
| Legacy-consumer folding + clamp                          | `conversation-workflow-run-deltas.ts`, `workflow-runs-legacy.ts`      | `legacy_encode.go`                          |
| Coalesce rule 6 payload merge + wire bounds              | `packages/shared/src/nex-protocol-v4/workflow-runs-delta.ts`          | `wfrun.go`                                  |
| Frame envelope + wire encode                             | `createTopicFrameSchema`, `wire-codec.ts`, `encodeReservedTopicFrame` | `types.go`, `wire_encode.go`                |
| Subscribe ACK / openTiming schemas                       | `packages/shared/src/nex-protocol-v4/transport.ts`                    | `types.go`                                  |

## Interfaces this package defines

The publisher treats deltas and snapshots as opaque JSON. It interprets only the
closed set of op discriminators and two state keys (`workflowRuns`, `revision`)
that the coalescing rules constrain.

```go
type SnapshotSource interface {
    Seq() int64
    SnapshotRaw() json.RawMessage      // JSON.stringify(ConversationSnapshot)
    WindowRowCount() int
    ApplyIngest(event json.RawMessage) ([]Delta, error)
    WorkflowRunsRaw() json.RawMessage  // projection's workflowRuns, or nil
}

type DeltaSink interface {
    OnBuffered(subscriptionID string, coalesced []Delta, encodedBytes int)
    OnOverflow(subscriptionID string)
}
```

`SnapshotSource` is implemented by Agent B's product projection. `DeltaSink` is
optional (nil is fine); tests use it to observe the exact pipeline output.

## Request handlers

- `SubscribeReserved(params) (*SubscribeResult, error)` — the production entry.
  Returns the ACK, the initial-frame reservation, and a rollback closure. The
  public JSON-RPC result is **ACK-only**; the initial frame is a separate
  reservation for the request-scoped post-response outbox.
- `Subscribe(params)` — convenience form that commits the initial reservation.
- `ResyncReserved(subscriptionID, request)` / `Resync` — same-subscriber
  recovery; supersedes the in-flight reservation without changing subId/profile.
- `Unsubscribe(subscriptionID, connectionID?)` — removes, optionally scoped.
- `ReserveFlush(subscriptionID)` / `Flush` — drain one subscriber's buffer on
  the host's `flushWindowMs` timer. No timers live in this package.

### ACK shape (matches recorded S02)

```
{ ack: {
    subscriptionId: "sub-<logEpoch>-<n>",
    mode: "snapshot" | "resume",
    logEpoch,
    openTiming?: { version: 1, initialFrameEncodeMs, sessionRuntimeState: "warm",
                   snapshotRowCount }
} }
```

`openTiming` is attached by the gateway after `subscribeReserved`; `version`,
`sessionRuntimeState` and `snapshotRowCount` come from this package's data.
The initial snapshot frame is sent **after** the response line (post-response
outbox ordering), as `go/testdata/traces/agent/S02` shows.

## Subscribe verdict

`resumable = base != nil && base.logEpoch == logEpoch && floorSeq <= base.seq <= currentSeq`.
Otherwise snapshot. Re-subscribing from the same connectionId replaces the old
subscription (old subId stops producing frames; a late commit on the replaced
reservation returns false).

| base                      | verdict                                           |
| ------------------------- | ------------------------------------------------- |
| none                      | snapshot                                          |
| wrong epoch               | snapshot                                          |
| seq < floorSeq (too old)  | snapshot                                          |
| seq > currentSeq          | snapshot                                          |
| seq == currentSeq         | resume, no initial frame (client already aligned) |
| floorSeq <= seq < current | resume with (base.seq, current] replay            |

## Frame envelope

Logical frame members in wire order: `topic, subscriptionId, sentAt, fromSeq,
toSeq, payload`; payload `{kind:"snapshot", snapshot}` or `{kind:"deltas",
deltas}`. Physical wire frames are produced by the existing
`protocol.EncodeTopicWireFrames` (`wire_encode.go`), carrying `wireVersion 3`,
`kind complete|fragment`, `deliveryKind initial|online|recovery`,
`logicalFrameId "<subId>-lf-<n>"` (global serial), `logicalFrameOrdinal`
(per-subscription), `topic`, `subscriptionId`.

Frame interval `(fromSeq, toSeq]` uses `sentSeq` as `fromSeq`, so gaps for
seqs filtered out by the profile still leave the client's
`frame.fromSeq === store.seq` contiguity intact. `snapshot` frames use
`fromSeq 0`.

**JS-exact JSON.** Frame bytes must equal `JSON.stringify`'s output because the
wire encoder computes fragment boundaries from them. `json.go` implements
`appendJSString` / `appendJSFloat` / `appendJSInt` accordingly: no HTML escaping
of `<`, `>`, `&`; pass-through of U+2028/U+2029; ECMAScript `Number::toString`
for floats; `\u00xx` for control characters.

## Coalesce rules (port of coalesce.ts)

1. adjacent `row.delta` with the same `(rowId, path)` → concatenate `append`;
2. adjacent `state.updated` → shallow-merge patches (key-level replacement);
3. `row.upserted` swallows earlier same-rowId `row.delta`, stopping at the
   nearest barrier or a same-rowId `row.upserted`/`row.appended`;
4. `row.removed` is a barrier; no rule crosses it;
5. frame splitting is not done here;
6. `workflowRun.updated` for the same runId merges into the **latest** one,
   rejecting the merge when the payload would exceed the wire bounds
   (`WorkflowRunWireBounds`, default 1024/1024); `workflowRun.removed` swallows
   the run's earlier increments; a `state.updated` carrying `workflowRuns` is a
   rule-6 barrier.

## Backpressure

Per subscriber: `appendSubscriberBuffer` coalesces current+incoming, then
enforces `subscriberBufferMaxOps` (500) and `subscriberBufferMaxBytes` (1 MiB,
the UTF-8 length of `{"kind":"deltas","deltas":[...]}`). On overflow the buffer
is cleared, `resyncRequired` is set, and the sink is notified; further deltas are
not buffered until a flush emits a recovery snapshot.

## Delivery profiles (read from core.ts)

| profile    | clientMode            | flushWindowMs | streamPaths                               |
| ---------- | --------------------- | ------------- | ----------------------------------------- |
| continuous | desktop-continuous    | 30            | text, inputText, output.text, summaryText |
| replayable | web-remote-replayable | 150           | text only                                 |

`ProfileForClientMode` maps the trusted clientMode; the gateway injects it.

## Verification

Vectors are generated by running the real TypeScript
(`scripts/go-rewrite/gen-publisher-vectors.mts`, run from `packages/shared`):

```
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-publisher-vectors.mts
```

The generator drives the real `ConversationTopicPublisher` with a scripted
projection (it replaces the private `projection` field at runtime), subscribes
in both modes, flushes with a fake clock, and records every ACK and frame after
the same normalization `record/normalize.mjs` applies. Go replays the same
script (`go/testdata/vectors/publisher-scripts/<name>.json`) and compares ACK
fields, delivery kind, logicalFrameId/ordinal, and the frame bytes.

- `go/testdata/vectors/publisher.json` — limits, profiles, coalesce cases (65),
  filter cases, legacy-encode cases, clamp cases, and 16 scenarios.
- `go/testdata/vectors/publisher-scripts/<name>.json` — the Go-driven scripts.

Tests (`go/internal/v4pub/`):

| test                                        | proves                                   |
| ------------------------------------------- | ---------------------------------------- |
| `TestLimitsMatchTypeScript`                 | limits + flushWindowMs not drifted       |
| `TestCoalesceAgreesWithTypeScript`          | all 65 coalesce cases byte/element equal |
| `TestFilterAgreesWithTypeScript`            | profile filtering                        |
| `TestLegacyEncodeAgreesWithTypeScript`      | whole-key folding                        |
| `TestClampWorkflowRunsAgreesWithTypeScript` | legacy-bound clamp                       |
| `TestScenariosAgreeWithTypeScript`          | 16 scenarios: ACK + frames               |
| `TestSubscribeAckMatchesS02Shape`           | ACK shape from recorded S02              |
| `TestInitialFrameAfterResponseOrdering`     | ACK-only result + separate initial wire  |
| `TestFrameJSONKeyOrder`                     | frame member order                       |
| `TestS02InitialFrameByteIdentical`          | snapshot frame equals recorded S02       |
| `TestJSONNumberFormattingMatchesECMAScript` | Number::toString boundaries              |
| `TestJSONStringEscapingMatchesECMAScript`   | JS string escaping                       |
| `TestSupersededReservationCannotCommit`     | generation guard                         |
| `TestRollbackRestoresReplacedSubscription`  | atomic replacement rollback              |
| `TestSinkObservesFanoutAndOverflow`         | DeltaSink contract                       |
| `TestUnsubscribeConnectionScope`            | connection-scoped removal                |

### Mutation checks (caught / total)

Each rule was broken deliberately and a test had to fail. Caught: **27 / 27**.
Groups: coalesce rules 1–6 (7), profile filter, buffer op/byte limits (2), JSON
backslash / control-char / HTML-escaping / float-exponent threshold (4), subscribe
floor / epoch / future bounds (3), resubscribe replacement, reserve-frame ordinal,
frame `fromSeq`, commit guard, rollback guard, resync deliveryKind (7), legacy
encoding first-vs-last fold + passthrough (2), clamp limit + live-preference (2),
frame member order.

One path is deliberately left un-mutated (documented, not a silent gap): clearing
`sub.buffer` on the `resyncRequired` path in `ReserveFlush` is defensive — the
buffer is always empty on that path (overflow clears it, and buffering stops
while `resyncRequired`), so removing the clear produces no observable
difference. Kept for parity with the TS source.

## Verification results

- `gofmt -l internal/v4pub` → 0 files.
- `go vet ./internal/v4pub/` → clean.
- `go test -race -count=1 ./internal/v4pub/` → ok.

Repo-wide `go vet ./...` / `go test ./...` fail in **other agents' concurrent
packages** (`internal/tools`, `internal/modelclient`, `internal/permission`,
`internal/rpc`, `internal/archcheck` line-limit reports for those packages), not
in `internal/v4pub`. See the owner's report for details.

## Known gaps

- Concurrency/goroutine discipline is the host's; this package is a single-thread
  state machine (no locks), as the source is.
- The projection (`SnapshotSource`) is Agent B's. The vectors use a scripted
  double; end-to-end equality with the real projection is not proven here.
- `command-inbox.ts` is not ported (out of this package's path).
- The wire envelope carrier sizes are `internal/protocol`'s; this package only
  calls the encoder.
