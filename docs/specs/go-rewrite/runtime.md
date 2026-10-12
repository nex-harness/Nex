# Agent runtime (Go): turn loop, sessions, v4 commands

Owner: runtime integration. Directories: `go/internal/runtime/**` (pure
orchestration; interfaces for clock/ids/fs/http/store), `go/internal/agent/**`
(protocol routing), `go/cmd/nex-agent/**` (the only place that touches
os/net/exec/sql). Verified against the recorded traces with
`node scripts/go-rewrite/record/replay-agent.mjs --scenario S02 --agent <go>`.

## Scope

Reproduces the Node agent's observable behaviour for the recorded scenarios
S01–S05 and S13: startup, v4 `createSession`, `v4/conversation/subscribe`, one
turn with model streaming, the minimum tools (Read/Write/Edit), permission
prompts, and HTTP retry. Not ported (Phase 4 / out of scope): queue/guide,
compaction, fork/rewind, subagents, MCP servers (only the `process/mcpTelemetry`
startup frame is emitted), skills, hooks, memory, workflow runs, the
session-state _mapper_ (the `state.updated` patch is derived from the provider
config, see below), cold resume.

## Two sequence-number views

The authoritative raw counter is `internal/eventstore.InMemory` (per session,
every appended event consumes one number, transient events included). Two
surfaces read it differently:

- **Telemetry / computer-use frames** expose the raw `event.sequenceNumber`
  (`v4/telemetry/event.eventSeq`, `computer-use/operation-event.sequenceNumber`).
- **The v4 publisher / product projection** sees a _transport_ sequence: the
  Node v4 gateway (`normalizeRuntimeEventSequence`) ingests a synthesized
  `session_created` event with `sequenceNumber: 0` first, which consumes
  transport seq 1, and then every raw event gets `transportSeq = rawSeq + 1`
  (the offset is +1 for every recorded scenario). Row `createdAtSeq` and
  snapshot `seq` carry this transport number.

The runtime keeps both: it appends to the event store for the raw number
(telemetry), and feeds the publisher a copy of the event with
`sequenceNumber = raw + 1`.

Verified for S02–S05/S13: `finalToSeq == maxRawSeq + 1` and the first
`createdAtSeq == 3` while telemetry `turn.started.eventSeq == 2`
(`go/testdata/traces/agent/*/out.normalized.json`).

## Component ownership (per session)

| Concern                   | Owner                                                   |
| ------------------------- | ------------------------------------------------------- |
| raw numbering             | `eventstore.InMemory`                                   |
| transport numbering       | runtime (`session.nextTransportSeq`)                    |
| deltas / snapshot         | `projection.Projection`                                 |
| log + subscribe/flush     | `v4pub.Publisher`                                       |
| telemetry/operation       | `emitters.Telemetry`                                    |
| persistence               | `sessionstore.Store` (adapter: `adapters/store`)        |
| model HTTP + retry        | `modelclient.Client` (+ `adapters/model/openai`)        |
| tools                     | `tools` + `permission` (+ `adapters/fsx`)               |
| authority for every write | the runtime actor goroutine (single writer per session) |

All outbound frames are written through a single `Outbox` (the protocol
`FrameWriter`), so ordering is whatever the runtime emits, in order.

## createSession flow (recorded order)

`v4/command{type:createSession}`:

1. request `session/requestRuntimePreferences` scope `runtime-materialization`
   (agent chooses the id, e.g. `server-1`), await the host reply
   (`memoryEnabled`, …).
2. emit `process/mcpTelemetry` `session_startup` (all-zero counts when no MCP
   servers are configured).
3. request `session/requestRuntimePreferences` scope `user-execution`
   (`server-2`), await reply (`integratedTerminalShell` supplies the env shell).
4. persist the session row + two `session_entry` rows.
5. respond `{commandId, status:"accepted", revisionAtDecision:0,
result:{type:"createSession", sessionId, input:{delivery:"startNow", inputId}},
memoryEnabled}`.
6. **after** the response, schedule the turn on the session actor. The turn's
   first observable event is `turn_started`; the host sends
   `v4/conversation/subscribe` immediately after the response, so the snapshot is
   taken before `turn_started` (recorded: snapshot `seq 2`, empty rows).

The two preference requests are emitted from inside the command handler, so the
handler must run off the frame reader (the reader also has to read the host's
replies). Responses to agent→host requests are matched to pending calls by id.

## Turn event stream (S02, raw numbers)

```
1  session_title_updated      (title from first input, source first_input)
2  turn_started               (messageId, inputId=commandId, foregroundExecutionId, intent)
3  session_input_promoted
4  model_request
5  model_network_status       (model_request_started)
6..13 model_streaming         (start, text_start, 4x text_delta, text_end, finish)
14 model_network_status       (model_request_completed)
15 model_complete
16 turn_complete
```

Events that are never exposed to the protocol (`model_streaming`,
`model_network_status`) still consume a raw number. Each appended event is fed
to the telemetry emitter (raw number) and to the publisher (raw+1).

`model_complete` carries `content, querySource, stopReason, usage, contextWindow,
toolCallCount, cacheHit, contextUsageBreakdown`; the breakdown comes from
`emitters.ContextUsage` over the same prompt sections sent to the model, the 20
default tool contracts and the request messages.

### Title sidecar and file changes

- The session-title sidecar runs on the first turn when the first input is at
  least `MIN_GENERATED_TITLE_INPUT_CHARS` (10) runes: a short input already reads
  as a title, so "Say hello" (S02) has no sidecar while "Read hello.txt" (S03)
  does. It issues a non-streaming request (`transport: http`, no tools) with the
  copied system prompt, appends `model_request` + two `model_network_status`
  (started/completed) + `model_complete` (querySource `session_title`) and a
  final `session_title_updated`. Its telemetry goes to the normalizer's `title`
  lane.
- `model_complete` carries a `fileChanges` summary only on the **final** model
  step; the value is the workspace diff (a created file counts its lines once),
  not the sum of tool patches.

## Flush

The desktop-continuous delivery profile flushes every 30 ms
(`v4pub.DeliveryProfiles`). The host normally drives that timer; this runtime
drives its own (`StartFlushTimer`, wired in `cmd/nex-agent`) and also flushes at
points the recorded frames show always coincide with the timer: after a
`model_request_started` / `model_retry_scheduled` status (the request round-trip
lets the timer fire), before the title sidecar, and after `turn_complete`. The
normalizer flattens the per-frame boundaries and compares the delta stream, so a
frame split only matters through the flush-window coalescing it induces.

## Post-turn `state.updated`

Emitted once after the turn completes, reason `prompt_completed`, patch
`{mode:{current}, model:{available,current,lastUsed}, permission:{mode},
thoughtLevel}`. The patch is produced by the session-state mapper in Node,
which is **not ported**. The runtime builds it from the mock provider config the
agent reads from `$HOME/.nex/v2/provider_config.json`:
`available` = the one enabled model (ref, label, providerLabel, contextWindow,
maxOutputTokens, reasoning levels from optionSpecs, input/output format from
properties); `current` = the session selection with its options; `mode` = build;
`permission.mode` = build; `thoughtLevel` from the reasoning optionSpecs.
Session/model id derivations and title rules: see session-store.md.

## Not ported (deliberate)

- `session-mapper.ts` / `model-mapper.ts`: the `state.updated` patch is derived
  from the provider config, not from live app state. Multi-provider workspaces,
  `lastUsed` history and thought-level availability beyond the selected model are
  simplified.
- The title sidecar is ported for the mock provider (fixed prompt, 5000-token
  cap, JSON/first-line title cleaning); the model-registry `titleGeneration`
  enable/disable config and `MAX_TITLE_INPUT_CHARS` beyond the copied constants
  are simplified.
- The `state.updated` model option's `contextWindow` is the app registry default
  (200000 for the mock), which differs from the provider config's
  `properties.contextWindow` (100000) that the usage patch carries as
  `maxTokens`. The registry/mapper that supplies the former is not ported.
- queue/guide, compaction, fork/rewind, subagents, hooks, memory, MCP servers,
  skills, dynamic workflow, project context section, media reads, Bash/Glob/Grep
  tools.
