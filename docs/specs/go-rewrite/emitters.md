# Non-conversation frames and context usage — Go port

Status: implemented and verified against the real TypeScript.
Scope: `go/internal/emitters`, vectors in `go/testdata/vectors/emitters.json` and
`go/testdata/vectors/emitters-context.json`, generator
`scripts/go-rewrite/gen-emitter-vectors.mts`, mutation check
`scripts/go-rewrite/mutation-emitters.sh`.

## What this package is

Around a turn the Node agent emits frames that are **not** the conversation
snapshot/delta stream. This package ports them and the context-usage numbers the
usage patch carries:

| Method                              | Producer (TypeScript)                                                                    | Go                          |
| ----------------------------------- | ---------------------------------------------------------------------------------------- | --------------------------- |
| `v4/telemetry/event`                | `bootstrap/src/nex-protocol-v4/conversation-telemetry-facts.ts`                          | `Telemetry.OnSessionEvent`  |
| `computer-use/operation-event`      | `bootstrap/src/nex-protocol/computer-use-operation-event.ts`                             | `Telemetry.OnSessionEvent`  |
| `process/mcpTelemetry`              | `adapters/src/mcp/telemetry.ts` `recordSessionStartup` (schema in `shared/nex-protocol`) | `MCPStartupFrame`           |
| `state.updated`                     | `bootstrap/src/nex-protocol/{server-operations,session-mapper}.ts`                       | `StateUpdatedFrame`         |
| `session/requestRuntimePreferences` | `bootstrap/src/nex-protocol/server-operations.ts`                                        | `RuntimePreferencesRequest` |
| `contextUsageBreakdown`             | `core/src/runtime/methods/context-usage.ts`                                              | `ContextUsage`              |

## API

```go
type Metadata struct {
    ModelName, ModelProvider string
    MemoryEnabled            *bool // omit for undefined
}
func New(sessionID string, meta Metadata) *Telemetry

// One call per SessionEvent. Returns the frames in the wire order the Node
// agent emits them: computer-use operation event first, then telemetry.
func (t *Telemetry) OnSessionEvent(ev projection.Event) ([]protocol.Message, error)
func (t *Telemetry) OnSessionEventTelemetry(ev projection.Event) (protocol.Message, bool, error)
func (t *Telemetry) ClearSession()

func MCPStartupFrame(in MCPStartupInput) protocol.Message
func RuntimePreferencesRequest(id protocol.RequestID, sessionID string, scope PrefsScope) protocol.Message
func StateUpdatedFrame(sessionID string, workspace WorkspaceRef, reason string, revision int64, patch StatePatch) protocol.Message
func ContextUsage(sections []ContextUsageSection, tools []ToolContract, messages []ContextUsageMessage) []BreakdownItem
```

`OnSessionEvent` is the single entry point for the lead's turn loop: feed it every
appended `projection.Event` and write the returned notifications. The other
constructors are for frames that are not derived from a `SessionEvent` (startup,
post-turn state, prefs requests).

### Why ordered JSON

The wire is byte-compared by the host. Go maps sort keys and structs have one
fixed field order, so every frame is built as an insertion-ordered object
(`objBuilder`) and serialized through `internal/v4pub`'s JSONValue, which already
implements `JSON.stringify`-compatible string escaping and number formatting.
**Key order is the zod schema's order, not the TypeScript object-literal order**:
the normalizer builds facts with spreads and then calls `schema.parse`, and zod
re-orders keys to the schema's declaration order. This is why `turn.started`
carries `sourceCommandId` _before_ `turnId` even though the literal spreads
`turnId` inside `base`. Emitters that are not zod-parsed (`mcpTelemetry`,
`operation-event`, `state.updated`) keep their literal order.

## Behaviour ported

### `v4/telemetry/event` (`ConversationTelemetryFactNormalizer.normalize`)

Ported kinds: `turn.started`, `model.request.status`, `stream.chunk`,
`usage.delta`, `turn.terminal`, `tool.lifecycle`, `permission.lifecycle`.
Not ported (return no fact here; they are Phase 4 breadth): `subagent.lifecycle`,
`workflow.lifecycle`, `compaction.terminal`.

Per-session side tables (bounded to 2000 entries, oldest evicted):
source inputId per turn, first stream chunk per `(turn, channel, partId,
parentToolCallId)`, tool name per `(turn, toolCallId)`, last model per session,
and the queue of completed model requests used to attach `requestId`/provider
identity to `usage.delta`.

Rules that matter:

- `model_request_queued` / `model_request_admitted` produce **no** fact.
- `stream.chunk` only for `text_delta` (channel `text`) and `reasoning_delta`
  (channel `thought`); `chunkLength` is the **UTF-16** length (`String.length`),
  so an astral character counts as two.
- `usage.delta` only for step-usage sources: `querySource` undefined, `main_turn`,
  `subagent`, `workflow_child`; the title sidecar and `tool_internal` are dropped.
  `totalTokens` uses `totalTokens ?? input ?? cacheRead+cacheWrite + output`.
- `tool.lifecycle` `performance` is an explicit whitelist flattening of
  `result.perf.detail`; command/filesystem/patch fields are emitted in schema
  order with `patchMatchMs` between `fsWriteMs` and `fileCount`.
- `turn.terminal` `resultType` maps `success→success`, `cancelled→interrupted`
  (+ `USER_INTERRUPT`), others→`failed`.
- `clearTurn` drops the per-turn tables on `turn_complete`.

### `computer-use/operation-event` (`mapComputerUseOperationEvent`)

`turn-started` / `turn-completed` (need a turnId), `tool-scheduled` (needs turnId,
toolCallId, toolName; sets `computerUse: true` only when the tool is
`mcp__node_repl__js` and the input code contains `setupComputerUseRuntime`),
`tool-started` (needs toolCallId), `session-closed` on `session_ended`.

### Frame ordering

For one `SessionEvent` the operation event is emitted **before** the telemetry
fact (S02 index 32/33). `OnSessionEvent` returns them in that order.

### `process/mcpTelemetry` `session_startup`

Built by `recordSessionStartup` when a session's MCP pool first reports status.
Counts: `configuredCount` = enabled servers, `connectedCount` = connected,
`failedCount` = configured − connected, `processCount` = connected stdio servers.
Emitted once per session (`sessionStartupReported` guard).

### `session/requestRuntimePreferences`

Agent→host **requests** (they carry an id). Two scopes, in this recorded order:

1. `runtime-materialization` — sent first, before the session runtime exists; its
   reply supplies `memoryEnabled` / `toolSearchEnabled` / `codemodeEnabled` /
   `nativeSearchEnhancementsEnabled` and the ask-user-question auto-resolution
   flag.
2. `user-execution` — requested lazily by `resolveInitialBashShellSelection`, i.e.
   after the runtime materializes. In the recorded S02 the MCP pool reports its
   `session_startup` between the two, so the observed order is
   **prefs#1 → mcpTelemetry → prefs#2 → command response**.

### `state.updated`

Envelope: `{patch, reason, revision, scope:"session", sessionId, type:"state.updated",
workspace:{workspacePath, workspaceKey}}`. The `patch` is the session settings
(`{mode, model:{available,current,lastUsed}, permission, thoughtLevel}`).

**Gap:** the patch content is produced by the session-state mapper
(`session-mapper.ts:mapSessionSettings` + `model-mapper.ts`), which reads live app
state (`app.getMode()`, `app.listModels()`, `runtime.getSessionModelSelection()`,
thought levels). That mapper is **not ported** here. `StateUpdatedFrame` takes the
patch fields as structured input and serializes them in the exact key order; the
runtime/session layer must supply the values. The S02 recorded frame is reproduced
byte-for-byte by the test.

### `contextUsageBreakdown`

`buildContextUsageSnapshot` computes per-category totals; the wire breakdown keeps
only categories with `tokens > 0` whose `chars > 0`, in order:
`system_prompt`, `meta_user_context`, `skills`, `tool_prompt`,
`system_tool_schemas`, `mcp_tool_schemas`, `messages`.

- `system_prompt` = sum of section `chars` where `injectionTarget == "system"`
  and `source ∉ {skills, tools}`.
- `meta_user_context` = sum where `injectionTarget == "meta_user"` and
  `source != skills`.
- `skills` / `tool_prompt` = the `source == "skills"` / `"tools"` sections.
- `system_tool_schemas` = sum over tools of
  `JSON.stringify({name, description, capability, inputSchema, outputSchema,
readOnly, destructive, sideEffectScope, permission, resultBudget})` (undefined
  fields omitted), for non-`mcp__` tools; `mcp_tool_schemas` for `mcp__` tools.
  With the 20 default tools this is **81936 chars**, matching the recorded
  traces. The exact value depends on the registry branch:
  `includeDynamicWorkflow: false` + `embeddedSearchEnabled: true`
  (`includeAgent/Skill/SendMessage/Automation` on; OffPeak off).
- `messages` = sum over non-system messages, excluding meta-user reminders
  (`isMetaUserContextMessage`: role user and content text, left-trimmed, starts
  with `<system-reminder>`), of
  `JSON.stringify({role, content, toolCalls, toolCallId, toolName})`.

`system_prompt` is **environment-dependent** (it embeds the working directory and
OS version): the recorded S02 value is 6499 for the recorded temp workspace, while
the vectors use `/work/proj` and produce 6475. `system_tool_schemas` (81936) and
`messages` (37/42/57) are environment-independent and match the trace exactly.

## Vectors and how to regenerate

`scripts/go-rewrite/gen-emitter-vectors.mts` builds each scenario's ordered
`SessionEvent` stream (payload shapes copied from `core/src/runtime/methods/*.ts`
and `tool/executor/events.ts`; sequence numbers from
`go/testdata/vectors/agent-eventseq.json`), then runs the **real**
`ConversationTelemetryFactNormalizer` and `mapComputerUseOperationEvent` and the
real `buildContextUsageSnapshot` (driven with the real `ContextBuilder` sections
and the real `ModelToolContract`s from `registerBuiltInTools`).

```
cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/gen-emitter-vectors.mts
```

Output: `emitters.json` (facts + operation events for S02/S03/S04/S05/S13) and
`emitters-context.json` (20 tool contracts as data + per-case breakdown, section
and message inputs).

## Tests

| Test                                                                                                                                                                  | What it proves                                                            |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `TestVectorsReproduceTelemetryFacts`                                                                                                                                  | every telemetry fact, byte-for-byte (key order included), all 5 scenarios |
| `TestVectorsReproduceOperationEvents`                                                                                                                                 | every operation-event frame, byte-for-byte                                |
| `TestTelemetryFactsAppearInRecordedOrder`                                                                                                                             | operation-before-telemetry order across a full replay                     |
| `TestContextUsageMatchesTypeScript`                                                                                                                                   | breakdown from Go inputs; pins 81936 and 37/42/57                         |
| `TestAgainstRecordedTraces`                                                                                                                                           | Go output's frame kinds + sequence numbers equal the recorded traces      |
| `TestMCPStartupFrameMatchesRecorded`, `TestRuntimePreferencesRequestMatchesRecorded`, `TestStateUpdatedFrameMatchesRecorded`                                          | constructor bytes equal the recorded S02 frames                           |
| `TestJSLenCountsUTF16CodeUnits`, `TestChunkLengthCountsUTF16`                                                                                                         | UTF-16 length rules                                                       |
| `TestUsageDeltaPrefersTotalTokens`, `TestUsageDeltaFallsBackToInputPlusOutput`                                                                                        | usage total rules                                                         |
| `TestQueuedAndAdmittedAreSkipped`, `TestSidecarModelCompleteProducesNoUsage`                                                                                          | dropped facts                                                             |
| `TestToolNameLookupSurvivesStarted`, `TestCronCreateAutomationID`, `TestCronCreateAutomationIDWithPerformance`                                                        | tool.lifecycle table rules                                                |
| `TestTurnStartedOperationNeedsTurnID`, `TestCancelledTurnTerminal`, `TestMCPToolSchemasSeparateFromSystemTools`, `TestEstimateTokensDivisor`, `TestKeyOrderPreserved` | edges                                                                     |

## Mutation checks

`scripts/go-rewrite/mutation-emitters.sh` breaks the implementation in 23 ways;
all 23 are caught:

base field order (turnId↔sourceCommandId), dropped `sourceCommandId` slot, byte
vs UTF-16 chunk length, `firstChunk` always true, queued status not skipped,
retry `delayMs` order, `totalTokens` ignored, tool `automationId` order,
tool-name not cleared after completion, fs/patch field order, permission
`toolName` on resolved, `cancelled→failed`, direct `inputId` ignored, operation
turn-started without turnId, operation tool-started dropping toolName,
mcpTelemetry key order, prefs key order, state.updated missing revision, mcp
tools folded into system tools, zero-char categories kept, meta-user reminder
counted, tool `inputSchema` dropped, `estimateTokens` divisor.

## Not done / limitations

- **`state.updated` patch content** is not computed here (session-state mapper not
  ported); only the serialization is. See above.
- Unported telemetry kinds (`subagent.lifecycle`, `workflow.lifecycle`,
  `compaction.terminal`) return no fact rather than erroring.
- `session_ended` operation frames are handled but the scenarios do not exercise
  them; a test covers the shape only indirectly.
- The `mcpTelemetry` counts and the prefs ordering are taken from the recorded
  trace and the TypeScript control flow, not generated as vectors.
- No `os`/`net`/`exec` imports; all I/O stays in the caller.
