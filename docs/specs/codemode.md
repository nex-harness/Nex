# Spec: Codemode (sandboxed tool orchestration)

Status: P1, P2, P3 implemented (non-chat model operations not implemented)
Owner: apps/nex-cli/packages/core (agent runtime), with contracts/adapters/ui touch points
Related: `packages/shared/src/nex-protocol/index.ts`, `apps/nex-cli/packages/core/src/tool/`,
`apps/nex-cli/packages/core/src/mcp/index.ts`, `apps/nex-cli/packages/contracts/src/interfaces/mcp.port.ts`

> Reference implementation studied: `@earendil-works/pi` v0.99.1 → v1.0.2
> (`@earendil-works/pi-codemode`, MIT, single dependency `quickjs-wasi@3.6.2`).
> Pi terms map to Nex terms in the Appendix.

## Summary

Codemode lets the model write a JavaScript program that calls Nex tools inside an
isolated sandbox. Only the script's explicit output (`return`, `text()`, `image()`)
reaches the model; nested tool calls and their intermediate results do **not** enter
LLM context. This is the second of two context-cost reductions:

1. **Tool declaration cost** — a tool's full JSON Schema is otherwise sent on every
   request. Codemode + deferred loading keep most tools out of the prompt.
2. **Tool result cost** — intermediate results otherwise enter history as `tool_result`
   entries. Codemode keeps them in the sandbox.

Nex today has **neither**. This spec covers the full feature, including the two
non-chat model operations reachable from scripts: **image generation** and
**classifiers**.

## Motivation (current Nex behavior, verified)

- Every provider-visible tool's complete schema is serialized into every request:
  `apps/nex-cli/packages/core/src/tool/registry.ts:104` (`toContracts()`) →
  `apps/nex-cli/packages/adapters/src/model/tool-transform.ts` (`normalToolInputSchema`).
  There is no deferral, no per-request pruning, and no declaration budget.
- Tool results always enter history; the only mitigation is truncation/artifact
  offload (`tool/executor/result-serialization.ts`) and compaction
  (`core/src/compact/`).
- The only large-tool-set mitigation is a stopgap: when the registry has more than
  `COMPACT_TOOL_KEEP_MAX_COUNT = 100` tools, the _compaction summary_ request is sent
  with **zero** tools (`core/src/runtime/methods/compact-active.ts:73,251-257`). The
  code comment states the real fix — ToolSearch/deferred tools — is not implemented.
- The existing `js` tool (`node-repl-host`) is **not** codemode: it is a
  browser/computer-use-only, full-privilege Node REPL whose `node:vm` is explicitly
  not a security boundary (`core/src/repl/node-repl-session.ts:112`), with no generic
  tool-calling capability. The dynamic-workflow sandbox is a separate deterministic
  automation feature with a fixed `__host` facade. Neither is a general tool
  orchestrator. See Appendix B.

## Non-goals

- Replacing the agent loop or session model. Codemode is one tool among others.
- Extending `node-repl-host` into a general sandbox. It stays browser/CUA-scoped.
- A general-purpose JS runtime for users. The sandbox's only capability is calling
  injected tools.
- Running chat models from scripts. Only `image` and `classifier` model types are
  reachable (see below).

## Phasing

The feature ships in three independently verifiable phases. Each phase is useful and
releasable on its own.

### P1 — On-demand tool declarations

Decouple "a tool exists" from "the model sees it".

- The registry stays the single source of truth. Add a runtime **declared set**: only
  members have their schema placed in the request.
- First version supports three exposure modes: `direct` (always declared),
  `deferred` (declared only after discovery), `hidden` (never declared, never
  callable).
- A single `tool_search` tool both searches and activates: a hit is added to the
  declared set and its full schema appears **in the next request**.
- Matching is keyword/BM25 over tool `name` + `description` + optional namespace.
  No vector index.
- The declared set updates only at request boundaries, so the prompt prefix remains
  cache-stable. It must not change mid-request.

Acceptance: first request omits deferred schemas; after `tool_search` the next request
includes them; a `hidden` tool can never be activated; cold-restore yields a consistent
declared set. Measure declaration tokens, search round-trips, and task success versus
full-declaration mode.

#### P1 implementation decisions

- **Exposure** is `ToolMetadata.exposure?: "direct" | "deferred" | "hidden"` (default
  `direct`). The registry stays unchanged as the "tool exists" authority.
- **Opt-in:** `config.toolSearch.enabled` (session config, no env var). When off, every
  tool behaves as `direct` and `ToolSearch` is not declared. When on, MCP tools register
  as `deferred`, except official CUA tools and the host `node_repl` tool, which stay
  `direct`.
- **Declared set is derived, not stored.** `getTools()` computes it from the
  provider-visible message history: names listed by successful `ToolSearch` results.
  One write path (the tool result), no second cache, so cold resume, rewind, branch cut
  and compaction automatically agree with what the model remembers. Compaction that
  drops a `ToolSearch` result drops the activation with it.
- **Request boundary:** `getTools()` is evaluated once per model request in
  `turn-loop.ts`; a `ToolSearch` result only reaches history after its step, so the
  activated schema first appears in the next request.
- **Encoding:** the `ToolSearch` model content is a fixed block
  `<tool_search_results>` with one `- <name>: <description>` line per hit; one module
  owns both encoding and decoding (round-trip tested).
- **Matching:** BM25 (k1=1.5, b=0.75) over tokenized `name` + `description`, name tokens
  split on `_`, `-`, `.`, camelCase and the `mcp__server__tool` separators. Default
  limit 8. Optional `namespace` filters by MCP server segment.
- **`hidden`** tools are omitted from the registry's provider contracts and rejected by
  the executor as "Tool not found", so they can be neither declared nor called.
- **Deferred tools called without discovery** are still executable (the executor is the
  permission authority, not the declaration list); only declaration is deferred.
- `ToolSearch` is omitted from declarations when no deferred tool exists.

### P2 — Nested execution and the three result channels

Before any sandbox exists, make "a script calls tools" correct.

- Script tool calls **must** go through the unified executor
  (`core/src/tool/executor/call-runner.ts`). Never call the MCP adapter directly.
- Add parent/child call linkage, callable-scope filtering, cancellation/timeout
  propagation, concurrency and call-count limits, and self-recursion prevention
  (a script may not call codemode).
- Split one tool result into **three channels** with independent rules:

  | Channel        | Consumer     | Rule                                                  |
  | -------------- | ------------ | ----------------------------------------------------- |
  | Execution data | the script   | raw, un-truncated; still size-capped into the sandbox |
  | Model content  | the LLM      | budgeted, truncated; this is what enters context      |
  | Audit record   | storage / UI | complete, for replay and display                      |

  **Ordering invariant:** take the raw structured value first, then derive the model
  and audit views. Never hand the script already-truncated model text. Pi's
  `coding-agent/src/extensions/mcp/tools.ts` preserves the un-truncated structured
  result for scripts while the model-facing content goes through a separate output
  limit; Nex must do the same.

- Child calls are recorded as events, usage, and audit metadata with a parent id, but
  are **not** separate `tool_result` entries in model context.

Acceptance: a script's tool call obeys permission approval and abort like a model
call; a cancelled script cancels in-flight child calls; the script receives raw data
while the model receives budgeted data; audit shows every child call.

#### P2 implementation decisions

- `NestedToolRunner` (`core/src/tool/nested/runner.ts`) is the only way a script calls a
  tool. It calls `ToolExecutor.execute()` with `parentToolCallId`, so permission,
  validation, abort and result budget are the same code path as a model-issued call.
- Admission order: forbidden names (codemode itself) → `registry.get()` (hidden/unknown
  are "not found") → per-parent call-count limit → concurrency slot → abort check.
- Channels are projected from one `ToolExecutionResult`: `output` (handler value, before
  budget truncation) → script; `modelContent` (budgeted) → `modelText`; the audit record
  keeps raw output, parent id, duration and the model-text length. A raw result larger
  than `maxResultBytes` fails that call instead of entering the sandbox.
- Child results are returned to the runner only; they are never appended to message
  history, so they cannot become `tool_result` entries.
- Limits: 200 calls, 8 concurrent, 5 MiB per raw result (overridable per runner).

### P3 — The sandbox

- A restricted JS sandbox runs model-written JavaScript. Reuse a mature sandbox — do
  not build a JS engine. `@earendil-works/pi-codemode` (MIT, `quickjs-wasi` only, no
  pi dependency, self-contained) is the reference; vendor or depend on it after
  verifying Nex build/SEA compatibility.
- Isolation guarantees: no `process`, no `require`, no host module loading, no
  arbitrary filesystem, no network, no credentials. Only injected tool calls.
- One execution = one worker + one VM, terminated on completion, so a runaway script
  cannot poison a later run. Interrupt via a shared buffer polled by the VM, because
  `worker.terminate()` cannot stop a thread spinning inside wasm.
- Tool arguments and results cross the host/worker boundary as JSON strings.
- Script globals: `tools.<name>(args)` (async, parallelizable), `ALL_TOOLS`,
  `searchTools(query, {limit, namespace})`, `describeTool`, `describeNamespace`,
  `text`, `image`, `exit`, `store`, `load`, optional leading `// @options:` line.
- `searchTools` searches inside the sandbox and does **not** change the model's
  declared set. This is distinct from the model-invoked `tool_search` of P1.
- Output budget: script output is capped (Pi default 10,000 tokens); overflow spills
  to a temp file and the result carries `fullOutputPath`.
- Resource limits: CPU, memory, timeout, cancellation, concurrency.

Acceptance: a script cannot reach the filesystem/network/host modules; a runaway
script is killed and does not affect the next run; a script can call MCP and built-in
tools through the P2 executor; output over budget spills to a file; the SEA-packaged
CLI loads the wasm asset.

#### P3 implementation decisions

- **Sandbox:** `pi-codemode` host/worker/prelude are vendored under
  `apps/nex-cli/packages/core/src/codemode/` (MIT, provenance in
  `third-party/copied-components.json`); the only dependency is `quickjs-wasi@3.6.2`.
  One execution = one worker thread + one QuickJS VM, terminated on completion.
- **Tool:** `Codemode` (`core/src/tool/handlers/codemode.ts`), registered only when
  `runtimeConfig.codemode.enabled` is true. Its `permission` is low-risk/no-approval
  because the script has no host capability; every child call is permission-checked on
  its own through `NestedToolRunner` → `ToolExecutor.execute()`.
- **Globals:** `tools.<name>`, `ALL_TOOLS`, `text`, `image`, `exit`, `store`, `load`
  come from the vendored prelude. `searchTools(query, {limit, namespace})` and
  `describeTool(name)` are host globals over the registry catalog and the P1 BM25 index;
  they never change the declared set. Both are async in the script.
- **Callable scope:** every non-hidden provider-visible tool except `Codemode` and
  `ToolSearch`, including deferred MCP tools.
- **Description:** fixed intro + globals + a tool list capped at 3000 estimated tokens,
  built from built-in non-deferred tools only and sorted by name, so MCP connect/drop and
  registration order never change it (prompt-cache stable). Everything else is found with
  `searchTools`.
- **Output:** `return` value, `text()`/`console` output and images; budget defaults to
  10,000 tokens (`// @options max_output_tokens`). Over budget, the model text is cut and
  the full text is written through the artifact store, returned as `fullOutputPath`.
- **Failure:** timeout, cancel and script exceptions return an error result (with the
  call trace and output so far), never a partial success. The deadline defaults to 120 s
  and is capped at 600 s.
- **Worker location:** `worker_threads` can only load a real file, so the worker URL is
  resolved next to `host.js` by default and overridable with `setCodemodeWorkerUrl()` for
  packaged builds (see the packaging notes).
- **Out of scope here:** `models.classify` / `models.generateImages` (non-chat model
  typing) are not part of this phase; the `models` global is absent.

### MCP exposure configuration

Exposure is configured on the MCP server entry, at two levels (modelled on pi's `mcp.json`):

```json
{
  "mcp": {
    "servers": {
      "github": {
        "type": "http",
        "url": "https://example.test/mcp",
        "exposure": "deferred",
        "toolExposure": { "read_issue": "direct", "delete_*": "hidden" }
      }
    }
  }
}
```

- `exposure`: `"direct" | "deferred" | "hidden"` for every tool of the server.
- `toolExposure`: overrides per tool. Keys are the tool names _as the server offers them_
  (not the `mcp__server__tool` form), or patterns where `*` matches any characters. An exact
  name wins over patterns; among patterns the first match in object order wins.
- Resolution for one tool: `toolExposure` match → server `exposure` → session default.
- **Session default** (when neither is set): `deferred` if Settings → "On-demand MCP tools"
  is on, otherwise `direct`. So existing configs behave exactly as before.
- `hidden` tools are never registered, so they cannot be declared, searched or called, from a
  script or from a subagent. They are not registered at all (not "registered but unreachable"),
  which keeps `registry.has()` truthful.
- Official computer-use tools and the host `node_repl` tool ignore these fields and stay
  `direct`: their model-visible names are a provider contract.
- `ToolSearch` is registered whenever the session default is `deferred` **or** any server or
  tool resolves to `deferred`, so a single `deferred` entry is enough to get a working
  discovery path without turning the global switch on.
- Invalid values are rejected where the config is validated, never silently coerced.
- The field travels with the rest of the server entry: settings UI → `McpServerConfig`
  (shared) → `NexAgentMcpServer` (protocol DTO) → runtime `McpServerConfig` → `registerMcpTools`.
- UI: Settings → MCP shows a per-server exposure selector (default / direct / deferred /
  hidden) in the server form. Per-tool overrides are edited in the config file.

## Non-chat model operations

Scripts reach non-chat models through a `models` global, using the session's resolved
credentials. This is why the spec covers image generation and classifiers: they are
codemode-only surfaces in the reference implementation, and they reuse the same
registry, auth, cost accounting, and result-block machinery as chat.

Both operations are gated behind a `models` flag on the codemode tool (Pi's
`CodemodeDescriptionOptions.models`); when off, the `models` global and its
description line are absent.

### Model typing (prerequisite)

Image generation and classifiers require extending the model registry from a single
`chat` shape to a discriminated union. This is the shared prerequisite for both.

```
type ModelType = "chat" | "image" | "classifier";
```

- `image` model: usable with `generateImages()` only; declares output modalities.
- `classifier` model: usable with `classify()` only.
- Chat models may be listed but are not runnable from scripts.
- Non-chat models do not appear in the model picker.

Costs of this prerequisite (Nex-side): extend the provider/model config schema
(`packages/shared/src/model-config.ts`), the registry, the remote-catalog refresh, and
the model picker. This is the largest single piece of work in this section and is
shared with any future non-chat feature.

### Image generation

- Contract: `generateImages(model, context, options): Promise<AssistantImages>`, where
  `context.input` is a list of text and/or reference image blocks, and the result's
  `output` is a list of text and/or image blocks.
- Reference implementation is thin: a single chat-completions request to an
  OpenAI-compatible endpoint with `modalities: ["image"]` (or `["image","text"]` when
  the model can also return text). Generated images come back as `data:` URLs on the
  message and are decoded into `{type:"image", mimeType, data}` blocks.
- Auth reuses the provider's existing credential. Input images (image-to-image /
  editing) are passed inline as base64 in the same message.
- Usage maps to the same cost model as chat; it is added to the codemode tool result
  and counts toward session cost.

### Classifiers

- Classifiers **do not chat**. They answer typed questions about JSON state: a
  `choice` among criteria, a `bool`, or a `score`, each with probabilities.
- Contract: `classify(model, { state, questions }, options): Promise<ClassifierResult>`;
  the result carries `answers` keyed by question name, and usage/cost when the service
  reports tokens.
- Provided by services such as TypeSafe Jev, Cloudflare Clef / Clef Flash, and a
  llama.cpp model (answered from next-token label probabilities). Three API families
  cover these in the reference: `typesafe-system-one`, `cloudflare-workers-ai-system-one`,
  `llama-cpp-classify`.
- Primary use is decision-making on cheap/fast models, e.g. rating task complexity to
  choose a planning model (Pi's `jev-router.ts` example). In scripts, a classifier can
  label batches of tool-returned data without those results entering model context.
- Concurrency: `classify()` and `generateImages()` share a small in-flight cap (Pi: 4);
  excess calls wait, so `Promise.all` over many items is safe.
- The result's usage is added to the codemode tool result and counts toward session
  cost.

### Script-visible error handling for model calls

`classify()` and `generateImages()` do **not** throw on provider errors; a script must
check `stopReason` and `errorMessage`. This keeps a failing generation from aborting
the whole script. Malformed arguments are rejected with the expected shape (see the
recovery-oriented error messages below).

## System prompt and description budget

Codemode's contribution to the system prompt is deliberately minimal; the detail lives
in the tool description, not the system prompt.

- System prompt contribution: **one** tool-list line (snippet) and **one** guideline,
  e.g. "Use codemode to batch independent tool calls, chain them, or filter large
  output, instead of many separate calls."
- The tool description carries: a fixed intro (sandbox semantics, `tools.<name>()`,
  the `// @options` line), one line per global, and one line per listed tool.
- **Declaration budget:** the listed tool section is capped (Pi default ~3,000
  estimated tokens, ~4 chars/token). Tools that do not fit are treated exactly like
  deferred tools and left to in-script search. A namespace heading marks
  `(tools not listed)`.
- **Cache stability:** the description lists tools by _exposure_, not by the active
  set, so it does not change when MCP servers connect or change their tools. This
  keeps the prompt prefix cache-stable across MCP churn.
- Listed tools use a one-line compressed form: description plus how scripts call it and
  what the call resolves to — not a full schema dump.
- Complex capabilities (e.g. `models`) appear only as an index pointing at a docs file
  the model reads on demand.

### Recovery-oriented errors

Following the reference, codemode errors should tell the model how to recover: an
unknown tool or `models` member names close matches (`tools.Bash` suggests
`tools.bash`); a malformed `classify()` / `generateImages()` call states the expected
shape; an unknown model points at `getAvailableOfType()`. A script that generates
images but never shows them gets a note to call `image(block)`.

## Tool exposure modes

| Mode                | In model request                   | Callable from script | Notes                                                                |
| ------------------- | ---------------------------------- | -------------------- | -------------------------------------------------------------------- |
| `direct`            | full declaration                   | yes                  | high-frequency core tools                                            |
| `deferred`          | no (until discovered)              | yes                  | default for most MCP tools; found via `tool_search` or `searchTools` |
| `codemode`          | no; listed in codemode description | yes                  | script-first exposure                                                |
| `codemode-deferred` | no; not listed                     | yes                  | discovered by in-script search                                       |
| `hidden`            | no                                 | no                   | disabled                                                             |

P1 implements `direct` / `deferred` / `hidden` only; `codemode` / `codemode-deferred`
gain meaning once P3 exists. Listing is by exposure so the description stays stable.

## Ownership and data flow

- **Tool registry** (`core/src/tool/registry.ts`): single source of truth for all
  tools. Unchanged in semantics; unchanged as the "tool exists" authority.
- **Declared set** (new runtime state, `core/src/runtime/methods/config.ts`
  `getTools()`): the only authority for "what the model sees this request". Owned by
  the agent runtime, updated at request boundaries.
- **Tool executor** (`core/src/tool/executor/call-runner.ts`): the single entry point
  for every tool call, including script-originated calls. Owns validation, permission,
  abort, and result budget.
- **Codemode sandbox** (new): owns one isolated execution per script. Talks to the
  executor only through the P2 bridge. Holds no session/tool business state.
- **Result channels** (new): one raw value, projected into execution / model / audit
  views by the executor, in that order.
- **Model registry** (`packages/provider`): after model typing, the authority for
  chat/image/classifier models and their auth.

```text
model request tools  ←  declared set  ←  tool_search (P1)
                                          ↑
codemode tool → P2 bridge → tool executor → raw result
   ↑                                          ↓ split (raw first)
script in sandbox (P3)                    ├─ execution data → script
   └─ models.classify/generateImages ─────┤─ model content  → request context
      → model registry → provider          └─ audit record   → storage/UI
```

## Invariants

- The declared set changes only at request boundaries; never mid-request.
- `hidden` tools are never declared and never callable, including from scripts.
- Script tool calls never bypass the unified executor or the permission gate.
- Raw result values are captured before any truncation; the script never receives
  truncated model text.
- A script cannot call codemode (no self-recursion).
- The sandbox has no host filesystem, network, module loading, or credentials.
- Each execution is isolated and torn down on completion; a runaway script cannot
  affect a later run.
- Codemode intermediate results produce audit records but no separate model-facing
  `tool_result` entries.
- Codemode's description does not change when MCP servers connect or change tools.
- Non-chat model calls use the session's resolved credentials and report usage into
  session cost.

## Failure semantics

- Sandbox timeout / cancellation → execution aborted, worker terminated, child calls
  cancelled; the model receives an error result, not a partial success.
- Script throws → the error and its call trace are returned to the model; other
  scripts are unaffected.
- Provider error in `classify()` / `generateImages()` → `stopReason: "error"` with
  `errorMessage`; the script decides whether to continue.
- `tool_search` returns no hits → no tools activated; the model may retry with a
  different query.
- MCP server disconnects after a tool was declared → the tool remains in the declared
  set but calls fail; the declared set is not silently rewritten mid-conversation.
- Output over budget → spill to a temp file with `fullOutputPath`; a note is added.

## Acceptance scenarios (top level)

1. A session with many MCP tools sends a first request containing no deferred schemas.
2. `tool_search("read github issue")` activates matching tools; the next request
   contains their schemas.
3. A script calls an MCP tool and a built-in tool through the executor; the model sees
   only the script's `return`, while audit shows both child calls.
4. A script generates an image via `models.generateImages()` and shows it with
   `image()`; the image block reaches the model and usage is added to session cost.
5. A script classifies a batch of tool results via `models.classify()`; only the
   script's summary reaches the model, and usage is added to session cost.
6. A runaway script is terminated; the next script runs normally.
7. In the SEA-packaged CLI, a codemode script runs (wasm asset loads).
8. A `hidden` tool cannot be searched, declared, or called.
9. A script cannot call codemode; the attempt is rejected.

## Implementation status and verification

P1, P2 and P3 are implemented behind two opt-in switches (Settings → "On-demand MCP tools",
"Codemode"), both default off. Not implemented: the `models` global (`classify`,
`generateImages`) and the model-type prerequisite it needs; see "Non-chat model operations".

| Acceptance scenario                                                    | Status           | Evidence                                                                                                                                                                                                                               |
| ---------------------------------------------------------------------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. First request has no deferred schemas                               | verified         | `core/test/tool-search-declared-set.test.ts`; request log in `bootstrap/test/e2e` (request 1: 23 tools, no `read_issue`)                                                                                                               |
| 2. `ToolSearch` activates; next request has the schema                 | verified         | same tests; request 2 declares `mcp__fixture__read_issue`                                                                                                                                                                              |
| 3. Script calls MCP + built-in tools; model sees only the return value | verified for MCP | `bootstrap/test/codemode-e2e.test.ts` (real stdio MCP server, real `ToolExecutor`); browser e2e: the 2 KB bodies never reach the model. A script calling a built-in tool is covered by the nested-runner unit tests, not by an e2e run |
| 4. `models.generateImages()`                                           | not implemented  |                                                                                                                                                                                                                                        |
| 5. `models.classify()`                                                 | not implemented  |                                                                                                                                                                                                                                        |
| 6. Runaway script terminated, next run works                           | verified         | `core/test/codemode-sandbox.test.ts`, `bootstrap/test/codemode-e2e.test.ts`, and inside the SEA binary                                                                                                                                 |
| 7. Codemode runs in the SEA-packaged CLI                               | verified         | `packages/server` SEA binary served the browser e2e turn (ToolSearch → Codemode → answer); `cli/scripts/verify-codemode-embed.mjs` runs the sandbox from a bundle with no `node_modules`                                               |
| 8. `hidden` tool cannot be searched, declared or called                | verified (unit)  | `core/test/tool-search*.test.ts`, `core/test/nested-runner.test.ts`. No production path sets `hidden` yet                                                                                                                              |
| 9. Script cannot call codemode                                         | verified         | `core/test/nested-runner.test.ts`, `bootstrap/test/codemode-e2e.test.ts`                                                                                                                                                               |

Known limits:

- Permission prompts for child calls: each child call goes through the normal permission
  gate. The browser e2e needed one approval in the dev run and two in the SEA run for the
  three parallel `read_issue` calls; whether the second prompt is per-call or a re-prompt
  was not investigated. Setting `autoApproveHighRisk` in the e2e config did not suppress it.
- `getTools()` is derived from message history on every call, so a compaction that drops a
  `ToolSearch` result also drops its activations (the model must search again).
- `ToolSearch` is not declared when no deferred tool exists, and the deferred set is
  recomputed from the registry each request, so MCP connect/disconnect is reflected at the
  next request boundary, never mid-request.

## Open questions

- Vendor `@earendil-works/pi-codemode` vs. depend on it: needs the SEA/wasm asset
  packaging check in `apps/nex-cli` (`build:sea`).
- BM25 vs. simple keyword matching for P1's first cut.
- Whether `codemode` / `codemode-deferred` exposure ship with P1 or wait for P3.
- Classifier providers to support first (TypeSafe / Cloudflare / llama.cpp).
- Image provider to support first (OpenRouter-style `modalities` endpoint).

---

## Appendix A — Pi term → Nex term map

| Pi                                                                   | Nex                                                  | Notes                              |
| -------------------------------------------------------------------- | ---------------------------------------------------- | ---------------------------------- |
| `pi-codemode` `CodemodeSandbox`                                      | new sandbox module                                   | P3                                 |
| `tools.<name>()` bridge                                              | P2 bridge → `call-runner.ts`                         | must not bypass executor           |
| `tool_search`                                                        | new tool                                             | P1; activates declared set         |
| `searchTools()` (in-script)                                          | same                                                 | P3; does not touch declared set    |
| exposure `direct`/`deferred`/`codemode`/`codemode-deferred`/`hidden` | same                                                 | P1 supports 3                      |
| `pi.mcp.json`                                                        | settings `mcpServers` + plugin manifest `mcpServers` | Nex has no `.mcp.json` file        |
| `ModelTypeMap` chat/image/classifier                                 | extend Nex model config                              | prerequisite                       |
| `models.classify()`                                                  | `models.classify()`                                  | classifier path                    |
| `models.generateImages()`                                            | `models.generateImages()`                            | image path                         |
| `promptSnippet` / `promptGuidelines`                                 | tool-list line + rule                                | Nex: `context/builder.ts` sections |
| `inlineBudget` (3000 tokens)                                         | declaration budget                                   | P1                                 |

## Appendix B — Why Nex's existing sandboxes are not codemode

|                                 | Pi codemode             | Nex `js` (node-repl-host)                         | Nex dynamic-workflow       |
| ------------------------------- | ----------------------- | ------------------------------------------------- | -------------------------- |
| Only capability = call any tool | yes                     | no: two hardcoded bridges (browser, CUA)          | no: fixed `__host` facade  |
| Can call MCP tools              | yes                     | no (MCP tools are model-side siblings)            | no                         |
| Real isolation                  | yes (QuickJS)           | no: full `require`/`process`, `vm` not a boundary | yes, but facade-only       |
| Parallel arbitrary tool calls   | yes                     | intra-script only; cross-call serialized          | structured, engine-limited |
| Always available                | yes (via tools setting) | no: browser-use/CUA plugins only                  | gated by host flag         |

Therefore codemode is recorded as absent, not "equivalent", until P3 lands.
