# Delta frames and rows-range paging — spec

Status: planned on `feature/next`.

## Decisions

1. Snapshots stay for subscribe and resync. Online updates become `deltas` frames.
2. Deltas are computed by diffing the previous and next **wire** snapshot per
   subscription at each flush (option a below), using the same snapshot builder, so a
   missed fold path degrades to a larger upsert, never divergence. Any doubt (epoch
   change, head change, too many upserts, delta > 64 KB, produced delta fails schema)
   sends a snapshot instead.
3. `row.removed` only for branch truncation (edit/retry), never for window-head eviction.
4. `rows.firstRowId` = oldest row of the active branch and `totalCount` = branch length,
   so the UI's `hasOlderRows` works; `conversationRowsRangeV4` pages from the full branch
   (`beforeRowId`, `limit` 1..200, `hasMore`), as the old CLI did.
5. Frames larger than `maxFrameBytes` (1 MiB) are fragmented with the shared wire codec.
6. Referee test: applying the emitted deltas with the UI's own `applyConversationDeltas`
   to the previous snapshot must equal the next snapshot, for every runner scenario.

## Research

**Delta frames + real `rowsRange` for next-runner — design**

Assumption (verified): the wire path passes frames as untyped JSON, with no fragment codec and no `maxFrameBytes` guard. The UI store already implements the delta contract. The work is therefore 95% runner-side.

---

## 1. UI delta contract (exact constraints)

**Schema** — `packages/shared/src/nex-protocol-v4/delta.ts:94-144`, `conversationDeltaSchema` is a closed discriminated union on `op`:

- `row.appended{row}` (`:96`) — append to tail.
- `row.upserted{row}` (`:98`) — whole-row replace by `rowId`.
- `row.removed{fromRowId}` (`:100`) — "delete this row and every loaded row with `rowId >= fromRowId`" (edit/retry branch truncation).
- `row.delta{rowId,path,append}` (`:102-107`) — `path ∈ streamablePathSchema = text|inputText|output.text|summaryText` (`core.ts:23`).
- `state.updated{patch}` (`:108`) — `statePatchSchema` keys (`delta.ts:39-61`): `revision, control, sharedContextImport, availability, inputRouting, meta, config, modelTransition, usage, queue, pendingInteractions, pendingCommands, backgroundWorks, subagents, workflowRuns, goal, plan, workspaceHookAdmission`. **Key-level whole replacement, never deep merge.** There is no `row.inserted`, no `row.moved`, no field patch — anything else must be a snapshot resync (file header `delta.ts:1-5`).
- `workflowRun.updated` / `workflowRun.removed` (`:121-143`) — runner emits none.

**Apply semantics** — `packages/shared/src/nex-protocol-v4/apply.ts`:

- `row.appended` (`:73-82`): push; `totalCount+1`; `firstRowId ??= row.rowId`. **Note it does not cap the window**: a client built on this helper grows unbounded unless it prunes.
- `row.upserted` (`:83-90`): if `rowId` not in `window` → **no-op** (older pages can't be touched).
- `row.removed` (`:91-110`): keep `rowId < fromRowId`; if `fromRowId <= firstRowId` then `totalCount=0`, `firstRowId=null` (active-branch reset; rewind keeps monotonically increasing rowIds); else `totalCount -= removed`.
- `row.delta` (`:111-118`): find by rowId; missing row → no-op; `appendToRow` (`:39-65`) only appends on the matching kind (`assistantText|reasoning`→`text`, `toolCall`→`inputText`, `toolCall` with `output`→`output.text`), otherwise no-op. The **server must only send it for streaming rows**.
- `state.updated` (`:119-121`): `{...snapshot, ...patch}`.
- The mutable twin (`:153-210`) has identical semantics; use it server-side for candidate snapshots only.

**UI store** — `packages/ui/src/v4/conversationProjectionStore.ts`:

- `handleFrame` (`:602-646`): drop frames whose `subscriptionId` ≠ current (`:608`); **a delta frame before any applied base → `requestRecovery`** (`:634-639`); route initial/recovery/online.
- `applyFrame` (`:648-758`): snapshot → wholesale replace (`:657-697`). Deltas:
  - **drop if `toSeq <= snapshot.seq`** (late/duplicate) (`:701-705`);
  - **gap check `frame.fromSeq !== snapshot.seq`** (`:706-721`) → gap never guessed: on a fresh subscribe's resume frame → `connect({forceSnapshot:true})`; on an active online/recovery gap → same-sub `requestRecovery`;
  - else `applyConversationDeltas` then **`seq = frame.toSeq`** (`:722-724`).
- Optional `state.updated.revision` is applied but **not required**; the store tracks seq only. `logEpoch` only matters on subscribe ACK (`ack.mode === "resume" && ack.logEpoch === snapshot.logEpoch`, `:456-458`) and rowsRange staleness (`:1004`).
- Frame envelope: `transport.ts:159-190` — `fromSeq` (0 iff snapshot), `toSeq`; subscription/ordinal monotonicity validated by the assembler (`wire-assembler.ts:187-233`) against `maxFrameBytes` = 1 MiB (`core.ts:66`) per physical fragment, 16 MiB logical (`core.ts:68`).
- Fragments: `wire.ts:26-45` + `wire-codec.ts:169-200`. Runner has no encoder (`encodeTopicWireFrames` only in the old CLI gateway `v4-gateway.ts:515`), so it always emits `kind:"complete"`.

**Constraints the runner must satisfy (hard):**

1. `fromSeq === client's current seq`; `toSeq` strictly increasing per subscription; **first frame after ACK is a snapshot**.
2. Each frame's deltas, applied in order by `apply.ts`, must yield exactly the state whose `seq` equals `toSeq` (and the snapshot the runner would build at that seq). One frame = one projection state.
3. `row.delta` only for rows present in the client's window and only for streaming kinds; any non-append change to that row is a `row.upserted`.
4. Emulated window (it is schema-visible) must maintain `window.length ≤ N`, `window[0].rowId === firstRowId ⟺ at head`, `totalCount` ≥ whole-branch rows.
5. `state.updated` full-key replacement means the runner may send any key, but sending nothing for a changed key silently diverges — the fallback rule (snapshot) covers doubt.
6. Wire size: deltas are tiny; **snapshots** must be `≤ maxFrameBytes` or fragmented. Today a 200-row snapshot with long tool output can exceed 1 MiB and is not guarded → add fragmentation (see §5).

---

## 2. Rows range

**Schema** — `transport.ts:511-531`: params `{sessionId, clientMode?, beforeRowId?, limit ∈ 1..200 (rowsRangeMaxLimit, core.ts:76)}`; result `{rows (rowId asc), atSeq, atRevision, atLogEpoch, hasMore}`.

**Runner today** — `commands.ts:167-176` returns `rows: []`, `hasMore:false`. `nexAgentCalls.ts:68-73` forwards only `sessionId` (drops `beforeRowId`/`limit`). Service passthrough is fine (`nexAgentService.ts:4504-4517`).

**UI usage:**

- `hasOlderRows = window[0].rowId > rows.firstRowId` (`conversationProjectionStore.ts:227-232`). The runner sets `firstRowId = window[0].rowId` (`projection/snapshot.ts:48`), so **it is always false today**.
- `SessionPane` auto-loads when the leading turn is truncated (`SessionPane.tsx:3365-3388`), on scroll-to-top (`timelineScrollAnchor.ts:262-271` via `canLoadOlder`, `SessionPane.tsx:4376`), and `loadAllOlder` for sharing/navigator.
- `loadOlder(limit = PROTOCOL_V4_LIMITS.snapshotTailWindowRows)` (`conversationProjectionStore.ts:987-1026`): cursor = `window[0].rowId`; discards on epoch mismatch (`:1004`) or cursor moved (`:1012`); `mergeOlderRows` prepends only `rowId < first` (`:255-263`). `loadAllOlder` loops until `hasMore === false` (`:1071-1107`).

**Required relationship:** `totalCount` = number of rows in the whole current active branch (old CLI: `projection-state.ts` snapshot is the whole log, `conversation-topic-publisher.ts:304-330`: wire snapshot = full snapshot then `window.slice(-N)`, **`totalCount`/`firstRowId` keep whole-log semantics**, even though a later line overwrites them — see note). `firstRowId` = `rowId` of the oldest row on the branch (not of the window). Then when `window.length = N`, `hasOlderRows` is true iff `totalCount > N`, and when the user has loaded `k` pages, `window[0] ≥ firstRowId`; the last page returns `hasMore:false` because `eligible.length === page.length`.

**Old CLI implementation:** source = **full projection rows**, not the wire window: `getRowsRange` (`conversation-topic-publisher.ts:439-463`): `limit = clamp(1..200)`, `eligible = beforeRowId ? rows.filter(r => r.rowId < beforeRowId) : rows`, `rows = eligible.slice(-limit)` (the page closest to the cursor, ascending), `hasMore = eligible.length > rows.length`, plus `atSeq/atRevision/atLogEpoch`. Window size: `snapshotTailWindowRows = 60` (`core.ts:75`); old frame cadence: `continuous.flushWindowMs = 30`, `replayable = 150` (`core.ts:29-55`).

⚠ Bug to fix while porting: `:323-326` overwrites `totalCount = visibleRows.length` and `firstRowId = visibleRows[0]` **before** slicing to 60, so the old CLI also reports `totalCount = whole branch` but `firstRowId = head of the (unsliced) branch** — it works only because window == branch when short. For the runner, define `firstRowId`= branch head **and**`totalCount` = branch length at the branch head, then slice.

---

## 3. Runner today and where to put deltas

- **Snapshots built** in `projection/snapshot.ts:38-135` (`TAIL_WINDOW = 200`, `:25`); published by `sessionStore.ts:391-420` (`emit`: build snapshot → `FrameOrdinal.next` → `buildConversationFrame` in `envelopes.ts:48-76`, `fromSeq:0`), from `flush` (`:324-331`), `subscribe` (`:179-181`), `resync` (`:199-201`).
- **Throttle**: `SnapshotThrottle` 10/s trailing-edge (`throttle.ts:14-73`, created `sessionStore.ts:118`). It is **global per session**, not per subscription; today all subscribers get the same snapshot.
- **State production**: every engine event → `applyEngineEvent` (`projection/fold.ts:36-140`) → new `ProjectionState` (`projection/state.ts:153-225`); `bump` = `seq+1, revision+1` (`:288-290`), `touch` = `seq+1` only (`:301-303`, used for streaming text/thinking at `fold.ts:189,232`). Queue writes `bump` (`sessionStore.ts:147-149`); compact owner likewise. A binary "revision-bearing?" bit already exists (`bump` vs `touch`) but isn't carried.
- Other subscribers of the state: attachment posting (`sessionStore.ts:345-347`), queue/compact owners — none read rows.

**Option (a) diff successive snapshots (recommended).**
At each flush (and only there), for each subscription:

1. Let `prev` = the last wire snapshot/state the subscription was sent (stored per subscription, server-side), `next` = `buildConversationSnapshot(projection)` at the current `seq`. **The delta target is the wire snapshot**, because the UI applies `apply.ts` which does not add `actions`, `visibility`, `productTurnId`, `outputPreview` — diffing raw projection rows would need client-side derivation.
2. Diff:
   - structural rows: compare `next.rows.window` and `prev.rows.window` by `rowId`, using an `isDeepStrictEqual` on serialized rows, with rows that are _semantically_ equal but `outputPreview`-truncated. Handling:
   - common prefix (all but the tail few) identical → tail ops;
   - for `i` from the end: unchanged → nothing; same `rowId` & same kind & only streamable text grew (prefix check, one of the four paths) → `row.delta{rowId,path,append:suffix}`; otherwise → `row.upserted`;
   - rows present in `prev` but absent from `next` (truncated from head by window cap) → **no op** (client will prune itself or keep them; keeping them is harmless for rendering older rows and avoids a `row.removed` that would delete client-loaded older pages — but it means client window grows: see §5 prune);
   - `next` has rows `prev` lacked, all with `rowId` > `prev` tail → `row.appended` in order;
   - branch reset (`next.rows[0].rowId < prev.rows[0].rowId` or any removal not at the head) → `row.removed{fromRowId: firstRemoved}`; if the branch changed head, fall back to snapshot.
   - state: compare each of the 18 `statePatchSchema` keys by deep equality; emit one `state.updated{patch}` with changed keys only. `seq`/`revision` are not patch keys. Deltas carry no explicit seq; the frame's `fromSeq/toSeq` is the accounting.
3. Emit `{fromSeq: prevSeq, toSeq: nextSeq, payload:{kind:"deltas"}}`; frame-level coalescing via existing `coalesceConversationDeltas` (`coalesce.ts:80-173`) as a cheap size guard.

**Why (a) over (b):**

- **Correctness by construction**: output is compared to the actual wire snapshot with the same `buildConversationSnapshot` used for snapshots, so the §4 property is nearly tautological; a missed fold path or a new derived field degrades to a larger upsert, never to divergence. Option (b) (emit at fold time) must replicate every derived field (`actions` from `deriveRowActions`, `availability`, `inputRouting`, `outputPreview`, compaction work, `pendingInteractions`), and each fold change risks a silent UI/runner split; it also needs a separate per-event diff for `actions` (the old CLI needed exactly such a post-pass, `product-projection.ts:1258-1301`) plus revision accounting (`:1305-1318`).
- **Cost**: (a) serializes ~200 rows + 1 snapshot every flush — at 10 flush/s per _watched_ session that's ~10× a 200-row JSON compare, i.e., microseconds-to-low-ms; acceptable for ≤10/s. (b) is O(1) per event but ~3-5× the code and review surface. (a) can later be optimized to diff only the last `K` rows (rows below `nextRowId - K` never change except `actions`) behind the same interface.
- **Doubt ⇒ snapshot**: any of — diff size > e.g. 64 KB, `next.logEpoch !== prev.logEpoch`, structural change touching the head, more than `M` upserts in one window, schema-parse failure of produced deltas — emit a snapshot instead (`fromSeq:0`). The UI handles both uniformly.

**Revision/seq semantics:** keep `ProjectionState.seq` as the frame watermark (advances on every fold result, `bump` or `touch`), and `revision` as today (`bump` only). In the diff, a frame's `state.updated` should **not** need `revision`: but to preserve CAS expectations, when the computed patch contains any of `control, availability, inputRouting, meta, config, queue, pendingInteractions, backgroundWorks, subagents, goal, plan` (the old CLI's `REVISION_BEARING_PATCH_KEYS`, `projection-state.ts:188-215`), or contains `row.upserted/appended/removed`, the runner's projection was already `bump`ed by the fold (that's what `bump` means) — so `revision` in the snapshot is correct and need not be sent in the patch. For robustness include `revision: next.revision` in the patch whenever it changed (cheap, idempotent, and mirrors the UI's `state.updated.revision` convention from the old CLI).

**Multiple subscriptions per session:** keep **one `ProjectionState` per session** (fold once). Store per-subscription `prevWire` + `prevSeq` + ordinal; each flush loops subscribers and diffs against _their_ `prevWire` (today all are identical; this stays correct if a subscriber joins late or resumes). `SnapshotThrottle` stays session-level; each flush produces one delta list per subscription (from the same `next`), so cost is `O(subscribers × rows)` only at flush.

**Throttle/coalescing of text:** the existing 10/s throttle gives ≤100 ms text batching; since `row.delta` is produced by the diff, successive flushes naturally produce one appends-only delta per streamed row (the suffix since the last flush). No extra per-row coalescer needed; optionally cap `append` length by splitting into multiple `row.delta` ops (each ≤ e.g. 8 KB) or fall back to `row.upserted` when `append` > a threshold. Consider raising to old CLI's 30 ms (`continuous.flushWindowMs`) once deltas are small; keep 10/s initially.

**Fallbacks:** `subscribe` → snapshot (`sendSnapshot`, `sessionStore.ts:179-181` already uses `afterAck` + wait-hydrated `:333-342`); `resync` → snapshot today — for deltas, resync may **resume**: if `request.base.seq ∈ [prevSeq, currentSeq]` replay a fresh diff from the snapshot at `base.seq`… simplest correct choice: **resync ⇒ snapshot** (keep today's behavior). On unsubscribe flush pending first (`:205`). On any exception in the diff, catch and send snapshot.

---

## 4. Test strategy

**Property test (per scenario):**

```
for each scripted scenario / FakeEngine event script (existing test scenarios + generated):
  state0 = projection after event k
  for k in events:
     s_prev = ConversationSnapshot built at flush k-1 (wire)
     deltas  = diff(prevWire, build(state_k))     // runner's producer
     s_next  = buildConversationSnapshot(state_k)
     assert deepEqual(applyConversationDeltas(s_prev, deltas), stripSeq(s_next))
        with s_next.seq set to frame.toSeq — and assert s_prev.seq === frame.fromSeq
     assert conversationDeltaSchema.parse(d) for each d
     assert frame passes conversationTopicFrameSchema and byte size ≤ maxFrameBytes or fragments
```

Using **`applyConversationDeltas` from `packages/shared/src/nex-protocol-v4/apply.ts:138-147`** literally (not a copy) makes it the referee. `s_next` comes from the same builder as the snapshot frames, so the check is exact (including `actions`, `firstRowId`, `totalCount`). Add the inverse test: `applyConversationDeltas(s_prev, deltas)` also via `applyConversationDeltaMutable` accumulator (`apply.ts:213`) → same bytes (covers §4's twin).

**Where:** a new `packages/next-runner/test/delta-projection.test.ts` with a harness `runDeltaScenario(events)` placed next to `test/helpers/fakeEngine.ts`, reusing `projectionFor(scenario())` (`projection.test.ts:300-346`). Parameterize over existing scenarios: text streaming, thinking, tool input streaming, tool result/checkpoint display, approval request/settle, compact start/end, model change marker, rewind/`row.removed`, title/model/phase changes, queue updates, restore (`projectHistory`), window truncation >200 rows, branch reset. Plus a fuzz/random-walk test over `EngineEvent` variants (seeded) and a "join mid-stream" test (subscribe at seq k, receive snapshot, then deltas).

**Wire tests:** extend `publisher.test.ts` (currently asserts snapshot trail, `:49-124`) — assert first frame snapshot, subsequent frames `payload.kind === "deltas"`, `fromSeq` chains; `resync` yields snapshot; two subscribers each get coalesced-consistent frames. `rowsRange` test at `publisher.test.ts:244-260` becomes: page sizes, cursor, `hasMore`, epoch, branch head after rewind.

**Client contract test:** apply each produced frame sequence to a copy of `ConversationProjectionStore`'s `applyFrame` logic (or instantiate the store against a fake transport) and assert final snapshot equals the runner's final snapshot and `hasOlderRows`/`loadOlder` converge.

---

## 5. Estimate (+ prerequisites)

| Item                                                                                                                                                                                                                 | Effort                                              |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- |
| `wireSnapshotDiff.ts` (row diff, text-append detection, state patch, fallbacks) + types                                                                                                                              | 2.5–3 d                                             |
| `sessionStore` per-subscription `prevWire/prevSeq`, delta emit, resync/subscribe, unsubscribe flush                                                                                                                  | 1–1.5 d                                             |
| `envelopes.ts` add `buildDeltaFrame`; wire type widening (`CompleteConversationWireFrame` → any complete topic frame)                                                                                                | 0.5 d                                               |
| Fragmentation + `maxFrameBytes` guard (port `encodeTopicWireFrames` use; host/UI assembler already exist: `agentConversationTransport.ts` `barrier`/`wire-assembler`)                                                | 1.5–2 d                                             |
| `rowsRange` real impl: full-row source in projection (retain rows beyond 200, or keep transcript-derived index), `beforeRowId/limit` plumbing in `nexAgentCalls.ts`, `totalCount/firstRowId` semantics, branch reset | 1.5–2 d                                             |
| Guard: `payload.tools` with `allowSteer` is already 400 on `guide`? no — use `inputs: true, tools: true`? **No such flags — ignore**                                                                                 | —                                                   |
| Tests (property harness + scenarios, publisher/wire, rowsRange, store contract)                                                                                                                                      | 2–2.5 d                                             |
| Docs (`docs/next/README.md:175-178`, `packages/next-runner/README.md:472-477` known gaps)                                                                                                                            | 0.5 d                                               |
| **Total**                                                                                                                                                                                                            | **~9.5–12 dev-days** (1 engineer), risk buffer +20% |

**Prerequisites / gotchas not to miss:**

1. **UI client window pruning**: `apply.ts::row.appended` never trims (`apply.ts:73-82`), and `loadOlder` prepends rows that also never get trimmed — the runner must be the authority (same as old CLI's wire snapshot) and the client is expected to keep the window bounded. Today's snapshot path bounds it implicitly. With deltas, either (i) leave it (UI grows only by live-appended rows; acceptable for a session's lifetime), or (ii) add a reconcile in the UI store after deltas to drop rows beyond `N + loadedPages`. Recommend (i) now, (ii) later — it is an existing gap in the UI contract, not created by deltas.
2. **`row.removed` vs client-loaded older pages**: because `apply.ts:92` filters all `rowId >= fromRowId`, the runner must send `row.removed{fromRowId}` only when the branch is truncated from that point — never for window-head eviction (see §3).
3. **`actions` recomputation**: because `deriveRowActions` reads the **whole** row set (`projection/snapshot.ts:44`, `rowActions.ts`), a new turn can flip `canEdit/canRetry` on an older visible row. Diff (a) catches this automatically as `row.upserted`; a fold-time emitter would have to reproduce this post-pass — another point for (a).
4. **`outputPreview` truncation (`snapshot.ts:389-402`) and `fullText`**: row equality should compare wire rows after building both snapshots, so mid-output `row.upserted` churn is bounded by preview size (4 KB) — fine.
5. **Ordering**: fold is synchronous and single-threaded per event (`sessionStore.ts:301-318`), so `prevWire` capture and diff at flush are race-free as long as flush reads `runtime.projection` once.
6. **Old CLI cross-check**: reference implementation of delta emission is `apps/nex-cli/.../product-projection.ts` (`attachRevision` `:1305-1318`, actions pass `:1258-1301`) and rowsRange `conversation-topic-publisher.ts:439-463`; port only the semantics, not the structure.

**Line-index caveat:** line numbers are from the current `feature/next` checkout at the time of reading (files cited as `file:line`).
