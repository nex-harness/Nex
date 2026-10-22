# Batch 3 — Attachments and File Rewind for the pi-based next-runner

Topology assumed: web UI → Go `/ws` (binary rpcwire Channel RPC) → JSON bridge → `/runner` link (JSON `{"t":"call",…}`) → next-runner on another machine. The runner today has **no** attachment methods, no `fileChanges`/`fileRewindPreview`/`applyFileRewind`/`editUserQuery` (`grep` over `packages/next-runner/src` is empty) and projects `userInput` rows without `attachments` (`projection/fold.ts:55-118`).

---

## A. Attachments

### A.1 Web UI flow (citations)

**Collection.** There are exactly four entry points, all in `useComposerAttachments`:

- **Paste** (`useComposerAttachments.ts:748-793`): if `clipboardData.files` is non-empty and it isn't a spreadsheet text payload (`shouldPreferSpreadsheetClipboardText`), the files become attachments (`:764-769`). Otherwise a long paste ≥15 000 chars (`chatAttachments.ts:36,126-128`) is written to a temp file via `platform.createTempTextAttachment` (`useComposerAttachments.ts:775-780`) — desktop-only; on web it rejects (`packages/web/src/main.tsx:175-176`), so web long-paste degrades into the composer text.
- **Drag/drop** (`:852-867`) — `dataTransfer.files`; workspace-tree drags use `WORKSPACE_FILE_DRAG_MIME` and are passed through (`:854-858`).
- **Picker** (`:717-737`) — desktop uses `platform.selectFiles()` to get _paths_ (`createChatComposerPathAttachment`, `chatAttachments.ts:99-108`, no `File`, size 0); web falls back to a hidden `<input type=file>` (`:722-725`, `handleAttachmentInputChange` `:739-746`).
- **Whiteboard** (`:869-892`) creates a PNG `File`.

**Per-item state** lives in a zustand store keyed by `scopeKey = workspaceIdentity||workspacePath \0 scopeId` (`composerAttachmentUploadStore.ts:6-48`, `buildScopeKey` `useComposerAttachments.ts:138-144`): `uploadStatus: waitingSession|queued|uploading|committing|ready|failed`, `attachmentRef?: {ref,fileName,mime,bytes,previewRef?}`, retry counters (`:14-36`). Uploads run with concurrency 2 (`:48`, `pumpQueue` `:483-489`), one transient retry after 500 ms (`:49,441-456`).

**Dispatch** (`runUpload` `:308-481`):

1. If `localPath && isRemoteAttachmentTarget(target)` — i.e. a desktop _remote_ session with `remoteSessionId` — bytes go through `promptAttachmentTransferService.stage({operationId,sessionId,workspacePath,remoteSessionId,localPath,…})` (`:326-379`), with `onDynamicProgress` (`:334`). A `runner:` identity does **not** take this branch (`:180-189` comment) — runner workspaces use the v4 attachment transaction. Non-host identity plus no `remoteSessionId` parks at `waitingSession` (`:326-333`) — this is exactly the case a browser attaching a file to a _remote_ workspace hits.
2. Otherwise `serializeChatComposerAttachment(item)` (`chatAttachments.ts:140-264`) → `uploadComposerAttachment(attachmentPut, sessionId, serialized)` (`composer/attachmentUpload.ts:40-80`).

**Local zero-copy**: when there is a `localPath` and the target is not non-host (`:654-679`), the item is immediately `ready` with `ref = localPath` and no upload happens; same rule in `uploadComposerAttachment.ts:53-59`. Web never has `localPath` (`getPathForFile` returns null, `main.tsx:173`), so **web always uploads or sends inline text**.

**What gets uploaded** (`chatAttachments.ts`):

- image: `localPath` if file absent or >20 MiB (delegated to agent read path, `:157-170`), else base64; non-path image >20 MiB throws `OversizedInlineImageAttachmentError` (`:147-155`).
- video: `localPath` zero-copy or base64 ≤ min(`VIDEO_INPUT_MAX_BYTES`, 20 MiB) (`:184-211`).
- pdf: `localPath` or base64 ≤20 MiB (`:213-238`).
- file with `localPath` → path ref (`:240-251`); file without path → `textContent` read via `File.text()` truncated at 64 KiB (`:42,253-263,295-300`). Non-text web files with no path end up as metadata-only and are **dropped** with a warning (`attachmentUpload.ts:67-71`) — this is the "web can't upload binaries to your workspace" limitation.

**Chunk transport** (`attachmentUploadTransaction.ts`): chunk size **384 KiB** (`:18`), which divides by 3 so only the last chunk has base64 padding (`:17`); SHA-256 checksum computed once (`:89-97`); `uploadId = upload-<uuid>` (`:99-105`); every request is measured as a physical Channel request (`[100, id, ServiceChannels.NexAgent, method]` + `[params]`) against `maxFrameBytes = 1 MiB` (`:107-120`, `core.ts:65`); total decoded size ≤ `attachmentMaxBytes = 20 MiB` (`:129-132`, `core.ts:84`); on any failure after `begin` it calls `attachmentAbortV4` (`:202-210`).

**Wire schema** (`packages/shared/src/nex-protocol-v4/transport.ts`):

- begin `{connectionId,uploadId,sessionId,fileName(≤255),mime,totalBytes≤20MiB,totalChunks≤64,checksum:sha256:hex}` (`:834-875`), result `staging{nextChunkIndex}` | `committed{ref}` (`:865-886`).
- chunk `{connectionId,uploadId,sessionId,chunkIndex,dataBase64}` with decoded ≤ `attachmentChunkMaxBytes = 512 KiB` (`:903-929`, `core.ts:85`).
- commit/abort `{connectionId,uploadId,sessionId}` (`:939-950`).
- read (sent-media preview) `{sessionId,ref,target?,attachmentIndex?,offset,limit≤512KiB}` (`:955-977`), result `{dataBase64,mediaType(image/video/pdf),totalBytes≤attachmentPreviewMaxBytes=30 MiB,nextOffset}` (`:1008-1060`).
- previewSource `{…,clientMode:"desktop-continuous"|"web-remote-replayable"}` → `{kind:"local_path"}`|`{kind:"chunked"}` (`:979-999`).
- share read/stat `conversationAttachmentRead/Stat` with `target+attachmentIndex` and stat `totalBytes` up to 2 GiB (`:1062-1157`).

**Services** (`packages/services/src/nex-agent/nexAgentService.ts:4353-4501`): each method requires a trusted v4 connection, injects `connectionId`, and re-measures the NDJSON envelope ≤1 MiB (`:565-571`). `attachmentPreviewSourceV4` short-circuits to `chunked` for anything but `desktop-continuous` without `remoteSessionId` (`:4475-4477`) — **web always chunked**. `attachmentRead` reads chunk-by-chunk via `agentService.attachmentReadV4` with `limit = 512 KiB`, cap `attachmentPreviewMaxChunks`, verifies stable `totalBytes/mediaType/nextOffset` (`agentConversationTransport.ts:384-463`); `attachmentPut` wraps the transaction (`:377-383`).

**Send payload.** `prepareForSend()` returns `null` unless every item is `ready` (`useComposerAttachments.ts:1045-1049`); the composer passes `attachments: readyAttachmentRefs` only when non-empty (`ConversationComposer.tsx:1205-1258`); after accepted it calls `adoptSentAttachments` then `clearAttachments(ids)` (`:1292-1298`). `AttachmentRef = {ref:string, fileName, mime, bytes, previewRef?}` (`attachment-ref.ts:4-14`); carried in `sendText.attachments` (`command.ts:84`), `createSession.firstInput.attachments` (`:48-51`), `editUserQuery.attachments` (`:157`), and `ConversationInputIntent.attachments` (default `[]`, `input-intent.ts:47`) → `queueItemSchema.attachments`. Row schema `userInputRowSchema.attachments` is the same shape inline (`rows.ts:138-150`).

**Rendering.** `ConversationRowView` splits `attachmentKind: "media"|"file"|"all"` (`:1190-1260`); the `UserInputAttachments` component prefetches image/video blobs through `readAttachment({sessionId, ref: previewRef ?? ref, mediaType, target:{rowId,entityId}, attachmentIndex})` with an AbortController (`:339-411`), creates object URLs, and offers gallery/PDF dialogs. Files render as chips from `components/ai-elements/attachments.tsx`. Editing a user row keeps the original indices so preview reads don't drift (`:875-895`).

**Limits summary:** ≤8 attachments (`chatAttachments.ts:35`), 20 MiB upload, 512 KiB chunk, 64 chunks, 64 MiB staged total, 5-min staging TTL, 16 concurrent uploads (`core.ts:84-101`).

### A.2 Old implementation (CLI/services)

- HTTP side: `AttachmentUploadRegistry` (`apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/attachment-upload-registry.ts`) is an in-process table keyed `connectionId\0sessionId\0uploadId`; `begin` is idempotent by metadata (`:95-144`), `chunk` enforces contiguity, no-empty, total bound, global staged-byte budget, and byte-equality on replay (`:146-177`), `commit` hashes and calls `putSessionAttachment(sessionId,{fileName,mime,bytes})` (`:236-270`). Registry pruned by a 30 s timer (`v4-gateway.ts:684-690`).
- Storage: `record.app.writePromptAttachment` (`create-app.ts:984-1007`) writes a **data-URL string** (`data:<mime>;base64,…`, contentType `text/plain`, `retention:"session"`) through `NodeToolArtifactStore.writeToolResultArtifact` → `~…/toolResults/<sessionId>/<toolCallId>-<artifact>.txt`, URI `nex-artifact://<sessionId>/<artifactId>` (`adapters/src/storage/index.ts:58-91`). For media it additionally primes a decoded cache file (`create-app.ts:995-1005`, `index.ts:195-300`).
- Lifetime: session retention (artifact directory lives with session data; video/image caches are derived and re-primable). Reads: `readPromptAttachment` resolves `nex-artifact://` → artifact, anything else → `fileSystemPort.readBinaryFile` (so local-path refs for desktop), chunked at the gateway (`v4-gateway.ts:1994-2038`); authorization is "ref must appear in the current user row's `attachments`" (`:2180-2210`).
- Feeding the model: `mapAttachmentRefsToTurnAttachments` (`commands/attachment-refs.ts:60-120`) — non-URI ref → `{path, type}`; image/video/pdf URI → `{content: <artifactUri>, path: fileName}`; non-image URI >64 KiB → metadata only; else reads the artifact and decodes the data URL to text. `resolveTurnAttachments` (`runtime/helpers/attachments.ts:97-307`): images → resized `{type:"image", mediaType, dataUrl}` content block (also persisted as an artifact, `attachment-media-resolver.ts:57-105`); PDF → pdf block; local text files → **inlined** via `readTextFileForModel` (truncated at `READ_MAX_FILE_SIZE_BYTES`, head read) (`:213-267`); clipboard-text → path-reference text only (`:236-245`); binary → path-reference text `"Attached <mime>: <path>\n…Use the available file reading tools…"` (`attachment-path-reference.ts:40-71`). So: images/PDF inline, text files inline when small, everything else a tool-read reference.

### A.3 pi side

- `AgentSession.prompt(text, options)` with `PromptOptions.images?: ImageContent[]` (`packages/coding-agent/src/core/agent-session.ts:317-331`); `ImageContent = {type:"image", data: base64, mimeType}` (`packages/ai/src/types.ts:430-434`). Images are normalized (auto-resize per model `inputLimits.images.resize`) before the run (`agent-session.ts:2026-2046`), forwarded through input hooks (`:2006-2024`) and queued on steer/follow-up (`:2200-2295`).
- **Only images can be attached to a prompt**; there is no PDF/text-file content part — pi expects text files to be read with the `read` tool (prompt guideline "Use read to examine files instead of cat or sed", `core/tools/read.ts:23-26`). `read` returns text (2000 lines / 50 KB head truncation, `truncate.ts:11-12`) or image blocks (`read.ts:112-160`), with a non-vision note.
- Capability: the runner derives `supportsImage: model.input.includes("image")` (`engine/pi/translate.ts:40`, exposed in `channels/modelViews.ts:219`) — this is what the UI uses to gate image options. `blockImages` replaces images with "Image reading is disabled." (`core/sdk.ts:296-320`). Non-image models get a placeholder note from `read` (`read.ts:80-84`).

### A.4 Transport

- rpcwire `ToJSON`/`FromJSON` handle `Buffer ⇄ {"$buf":"base64","$vs":bool}`, `undefined ⇄ {"$undef":true}`, reserved-key escaping (`next/server/internal/rpcwire/README.md` "JSON bridge"). `browserws` calls `ToJSON` on call args (`browserws.go:475,503,625`) and `FromJSON` on results (`:551,637`).
- Caps: per-frame payload 64 MiB (`rpcwire/frame.go:106`); browser **outbound** frames >32 MiB rejected, queue budget 64 MiB / 1024 frames (`browserws.go:29-37`); runner link read limit **32 MiB** per WS message (`runnerws/runnerws.go:33,118`); runner link call timeout 10 min (`:29-31`); each runner frame is one `json.Unmarshal` of the whole message (`:261-279`).
- Recommendation: keep **384 KiB** chunks (their 512 KiB base64 + JSON envelope ≈ 700 KB, far below every cap and cheap to `JSON.parse`); for media _reads_ use 512 KiB limit as today. Do not use rpcwire Buffers for attachment chunks — the JSON bridge would base64 them once more, inflating 20 MiB to ~36 MiB, and a _result_ that large exceeds the runner's 32 MiB read limit.

### A.5 Proposed runner attachment design

**Registration** (nex-agent channel, `channels/register.ts`): `attachmentBeginV4`, `attachmentChunkV4`, `attachmentCommitV4`, `attachmentAbortV4`, `attachmentReadV4`, `attachmentPreviewSourceV4` (return `{kind:"chunked"}` unconditionally), plus `conversationAttachmentReadV4/StatV4` for share.

**Storage:** `<runnerDataDir>/attachments/<sessionId>/<uploadId>/` with `meta.json` (fileName, mime, totalBytes, totalChunks, checksum, state) and `blob.part`. `commit` streams a SHA-256 while appending chunks (already validated as contiguous), renames to `blob`, and returns `ref = "nex-runner://<sessionId>/<uploadId>/<sanitizedFileName>"`. Session dir = base58/json-escaped `sessionId`; **never** use `fileName` as a path component — keep it in meta and sanitize with `[^\w.-]→_`, max 120 chars; reject names containing `/`, `\`, `..` (the zod schema already bans NUL/CR/LF, `transport.ts:822-828`). Ref parsing must be `^nex-runner://([^/]+)/([^/]+)/` and then `path.resolve` + `startsWith(sessionDir + sep)` check (same pattern as the old CLI's row-membership authorization; the runner additionally checks the ref is listed in the current user row / queue item).

**Mime sniffing:** trust the declared mime only for images/videos/pdf; on commit read the first 16 bytes and verify the magic (PNG/JPEG/GIF/WEBP/BMP/PDF/MP4) when the declared mime is media; on mismatch and for previews serve with the sniffed type (or fall back to `application/octet-stream` and refuse preview). `content-sniffing` guards against an SVG-`image/svg+xml` XSS vector: SVG is not in pi's readable image list (`read.ts:95`), so classify it as `file`.

**Limits:** enforce exactly the protocol's `attachmentMaxBytes=20 MiB`, `attachmentChunkMaxBytes=512 KiB`, `attachmentUploadMaxChunks=64`, `attachmentUploadMaxConcurrent=16`, `attachmentUploadMaxStagedBytes=64 MiB`, TTL 5 min (`core.ts:84-101`), and add a per-session disk quota (e.g. 512 MiB) with LRU eviction of unreferenced blobs older than `attachmentUnreferencedTtlMs` (24 h).

**Feeding pi** (`promptOps` / `sessionOps`): `sendText`/queue items carry `attachments: AttachmentRef[]` (already in `QueueItem`, `queueItem.vue` `queueItem.ts:18-30`; `handleSendText` must stop dropping it, `sendTextCommand.ts:30-55`). At submit time:

- `image/*` and runner-owned `nex-runner://` refs → read blob → `{type:"image",data:base64,mimeType}` → `session.prompt(text, {images})` (pi resizes per model; if `supportsImage` is false, still pass — pi/read tool degrades with a note).
- Everything else (pdf, text, binary, `localPath` refs) → append a deterministic "attachment block" to the prompt text: `[Attachment 1: name (mime, N bytes) at /abs/path]` for paths the runner can prove are inside its workspace or under its data dir, plus a pointer "use read". Do **not** inline file contents at admission time — pi's `read` tool already truncates sensibly; inlining would blow the prompt budget and diverge from pi's own semantics.
- PDF: pi has no PDF part; pass as path reference only (old CLI had a PDF block; pi cannot consume it).
- `previewRef`/row projection: the projection must put `attachments` on the `userInput` row at turn creation (`fold.ts:69-90` startTurn options), sourced from the admitted queue item; after restore, re-derive them from the pi user message: persist an `nex.attachments` custom entry (or metadata in the session entry) at submit and rebuild on restore (`projection/restore.ts`).

**Read path** (`attachmentReadV4`): resolve ref→blob under the session dir, stat size, `offset/limit`, return `nextOffset`; media only. `conversationAttachmentStat` returns `{mediaType,totalBytes,mtimeMs}` (no bytes). **PreviewSource** returns `chunked` always (web renders blobs; the old CLI's local-path fast path requires `createLocalMediaPreviewUrl`, absent on web).

**Repeater/lifetime nuance:** if the pi session is compacted/forked, attachments remain addressed by `sessionId+uploadId`, so they need no compaction handling. Cleanup: on `attachmentAbort`, on TTL expiry, and a daily sweep that deletes blobs no _current_ row or queue item references and older than 24 h.

**Effort:** ~1.5-2 days (registry + API 0.5 d, prompt mapping + projection/restore 0.75 d, tests + e2e through Go bridge 0.5 d).

---

## B. File rewind / changed files

### B.1 UI

- `conversationFileChangesV4` params `{sessionId,target,baseRevision,baseLogEpoch}` (`transport.ts:562-570`), result `{files,additions,deletions,state?:active|reverted, items:[{path,additions,deletions,writeCount,toolNames,patches: hunk[]}]}` (`:572-593`). Transport `fileChanges` (`agentConversationTransport.ts:353-364`). SessionPane caches 20 in-flight/terminal entries keyed by `revision | fileChangesState | rowId | entityId` (`SessionPane.tsx:358,1386-1444`) and calls `recoverFromStaleAuthority()` on `proto.stale*`.
- `v4ConversationFileRewindPreviewResultSchema` (`transport.ts:710-768`): `{canApply, safeFiles:[{action:restore|delete, operationCount, path, toolNames}], unsafeFiles:[{reason: checkpoint_missing|checkpoint_unreadable|external_modified|file_read_failed|unsupported_checkpoint, currentHash?, expectedHash?, message?, …}], ignoredFiles:[{reason:"bash_ignored", …}]}`.
- `applyFileRewind` command payload `{target}` (`command.ts:155`), result `{applied, preview, response}` (`:391-396`).
- `editUserQuery` `{target,newText,attachments?,workspaceMode:"preserve"|"rewind"}` (`command.ts:157-165`), result `{disposition:"rewind"|"fork"|"blocked", sessionId, reasonCode?, preview?}` (`:397-404`). UI: `handleEdit` dispatches, then `ConversationRowView` shows the inline conflict dialog when `disposition==="blocked" && preview` (`SessionPane.tsx:2764-2812`, `ConversationRowView.tsx:986-1020,1160-1180`) — variant `editConflict` in `ConversationFileRewindDialog.tsx`.
- **Changed-files panel**: `ConversationFileSummaryPanel` (rendered per `turnHeader.fileChanges`) reads `header.fileChanges {additions,deletions,files,state}` (`rows.ts:87-94`), lazily fetches full `fileChanges` when expanded, "Undo" requires `header.actions.canRewindFiles === true && state!=="reverted"` (`ConversationFileSummaryPanel.tsx:75-100`), previews via `previewFileRewind`, applies via `applyFileRewind`. Old derivation: `canRewindFiles` unless any active work, any pending interaction, turn running, or `fileChanges.state!=="active"` (`product-projection.ts:1264-1271`). Runner's `rowActions.ts:9-11` explicitly defers this.
- **Per-tool diff cards**: `ToolCallBlocks/fileSummaries.ts` reads `output.display` with `kind:"file_diff"` (path/additions/deletions/structuredPatch) or `file_diffs` (`:23-110`), else falls back to `raw.changes`/input `old_string/new_string` heuristics. Protocol schema for the display: `toolDisplay.ts:24-42`. The runner currently emits only `output.text` and no `display` (`projection/snapshot.ts:320-345`), and drops `details` entirely (`engine/pi/events.ts:59-69`).

**What the runner must emit for edit/write cards:** in `tool_call_end`, attach `output.display = {kind:"file_diff", filePath, additions, deletions, structuredPatch}`. pi 1.3's `tool_execution_end.event.result.details` holds exactly this for edits: `EditToolDetails {diff, patch, firstChangedLine}` (`core/tools/edit.ts:70-77`); `write` has `details: undefined` (`core/tools/write.ts:87`) — for writes the runner must synthesize a patch (previous content from its checkpoint index, or all-added from `input.content`). `structuredPatch` must be built from `details.patch` (parse the unified diff into hunks) or via `generateUnifiedPatch` (`edit-diff.ts:365`). Additions/deletions can be counted from `+`/`-` lines. Note: `events.ts:29-114` must pass `details` through (add `details?: unknown` to `EngineEvent.tool_call_end` in `engine/types.ts:177`), and `ToolCallRowState`/snapshot need a `display` field.

### B.2 Old implementation

- **Per-tool checkpoints, not per-turn**: after every successful tool result, `emitFileMutationCheckpoint` (`runtime/methods/tools.ts:171-240`) writes a one-file `WorkspaceCheckpointArtifact` (`kind:"workspace_file_before_change"`, `files:[{path,existedBefore,beforeContent,afterContent?,structuredPatch}]`, `contracts/src/rewind/index.ts:100-124`) and appends `CheckpointCreated {checkpointId,messageId,targetMessageId,toolMessageId,scope:Workspace,snapshotRef:artifact.uri,fileCount:1}`. Artifacts live in the same session artifact store; checkpoints are persisted as session entries for cold resume (`workspace-checkpoint-persistence.ts:29-60,104-177`).
- **Preview/apply** (`runtime/methods/file-rewind.ts`): select checkpoints by message ids/turn (`:480-`), for each file compute `expectedHash = hash(afterContent)`; read the current file; if `hash ≠ expectedHash` → `external_modified` (`:420-440`). Applying walks operations in reverse with a before-state journal, writing atomically, compensating in reverse on failure (`:140-170,225-250`). `bash`/`shell`/`terminal` artifacts → `ignoredFiles reason:"bash_ignored"` (`:580-599`). "unsafe/unavailable/applyConflict": `unsafeFiles>0` → `guard.workspaceRewindUnsafeFiles`; ignored >0 → `guard.workspaceRewindIgnoredFiles`; no safe files → `guard.workspaceRewindUnavailable`; apply failure → `guard.workspaceRewindApplyConflict` (`commands/handlers/fork-edit-retry.ts:150-188`). Apply emits `RewindTriggered` and the projection flips `turnHeader.fileChanges.state:"reverted"` (`product-projection.ts:1786-1790`).
- **Changed files** = aggregation of checkpoint artifacts in the turn window, deduped per path with the _last_ patch as final diff (`cold-file-change-summaries.ts:192-330`), with a fallback to `ModelComplete` fileChanges.

### B.3 pi

- Example `examples/extensions/git-checkpoint.ts`: on `turn_start` runs `git stash create`, stores the ref keyed by `ctx.sessionManager.getLeafEntry().id`, and on `session_before_fork` offers `git stash apply <ref>`; clears on `agent_settled`. Fragile (untracked files ignored by `stash create`, cleared each run) and unsuitable as-is.
- **Hook points available to the runner**: pi 1.3.0 exposes `pi.on("tool_result", …)` (`core/extensions/types.ts:1267-1316` with typed `EditToolResultEvent` carrying `details: EditToolDetails`), `"turn_start"/"turn_end"` (agent loop emits at run start and per tool-round, `packages/agent/src/agent-loop.ts:118,147,207`), and `"session_before_fork"` (`agent-session-runtime.ts:151-168`). Inline extensions are already loaded (`engine/pi/piSessionFactory.ts:52-58` `extensionFactories: [createPermissionGateExtension(gate)]`) — the checkpoint extension can be added to the same array; agent-loop events also surface as `AgentSessionEvent` on `session.subscribe(...)` (`piSessionFactory.ts:65-72`), so the runner could do it without a second extension (except fork, which is not a session event).
- Edit results do **not** include the old file content (only diff/patch). Write results include nothing. So a checkpoint must snapshot the file itself before the write (hook `tool_call` for edit/write/write-like tools, or on `tool_result` read `details.patch` backwards is unsafe). pi does not otherwise record prior contents.

### B.4 Proposed runner design

**Checkpoint store (works for non-git workspaces):** one shadow repo per _workspace_, not per session:
`git --git-dir=<runnerData>/checkpoints/<wsHash>.git --work-tree=<ws>` (bare-style `--git-dir` with a separate workspace; `core.worktree` set). `wsHash = sha256(realpath(ws))[0:16]`.

- **Before a turn that may write**: hook `tool_call` for mutating tools (`edit`, `write`, any custom tool with a mutating hint; **bash/powershell are not checkpoints** — mark any later file-changed-via-bash as `ignored`). On the _first_ mutating tool call of a turn, run a prefix snapshot:
  `git -C <ws> --git-dir=… --work-tree=<ws> add -A` (respects `.gitignore` and the workspace's own `.git` if present) then `git write-tree` → `commit-tree` with parents `HEAD` and `-p <parent>` (the previous turn's checkpoint), message `nex <sessionId> <turnEntityId> <seq>`. Update `refs/nex/<sessionId>/<turnSeq>` (per-turn refs keep history; `HEAD` per session namespace `refs/nex/heads/<sessionId>` advanced to the last turn).
  Then each mutating `tool_result` records the **post-state of just that file** (`git hash-object -w` of current file → tree entry) so the changed-files panel and rewind can replay per-tool checkpoints without a full-tree commit per tool.
- **Gating:** skip snapshot when the tool input path is outside `realpath(ws)`, is a symlink pointing outside, or file > `maxCheckpointFileBytes` (e.g. 2 MiB for text; binaries > 16 MiB are stored as blobs only when `.gitignore` doesn't exclude them and the tree size stays < 200 MiB). Skip workspace roots with >50 k files unless `core.fsmonitor`/`untrackedCache` is enabled; measure with `git ls-files -z | wc -c` once per session.
- **Windows:** avoid shell; spawn `git.exe` with args array; set `core.autocrlf=false`, `core.safecrlf=false`, `core.longpaths=true`, `core.fileMode=false` on the shadow repo (file mode bits are not meaningful on NTFS and would make every file look changed); use `--pathspec-from-file` for large add sets; normalize paths via `realpath` → forward slashes before `hash-object`.
- **Changed files** (`conversationFileChangesV4`): from per-tool post-state records of the target turn(s): first pre-state of the turn (from the turn checkpoint tree) vs last post-state per path → `structuredPatch` hunks (old CLI capped at 8 hunks / 160 lines, `result-display.ts:37-38`), additions/deletions line counts, `writeCount`, `toolNames`; `state` flips to `reverted` when a `RewindTriggered`-equivalent marker entry is written into pi's session (`customType "nex.rewind"`). Populate `turnHeader.fileChanges` in the projection so the panel appears without the query.
- **Rewind (`applyFileRewind` / `editUserQuery{workspaceMode:"rewind"}`)**:
  1. Resolve target user row entityId → pi entry id → its turn; collect per-tool checkpoints for that turn and later turns.
  2. For each path (newest operation first), compute `expectedHash` = hash of the latest post-state; if the working file's current hash differs → `unsafeFiles: external_modified` (with `currentHash/expectedHash`). If a step lacks a post-state (file modified by bash) → `ignoredFiles: bash_ignored`. If the turn checkpoint/tree is missing → `checkpoint_unavailable`.
  3. Write the **turn-start** tree contents for those paths (restore content; delete files created in the turn) atomically: write temp file in same dir, `fsync`, `rename`. Journal the prior state and compensate in reverse on failure (old CLI's semantics).
  4. Only when all files written: commit the conversation branch cut (pi `sessionManager` branch to parent of the target entry, then `prompt(newText)`), and append the `nex.rewind` marker.
     `canApply = safeFiles>0 && unsafeFiles==0`.
- **Ordering diagram**

```
tool_call(edit/write) ──► ensureTurnCheckpoint()
                           git add -A (first call of turn) ─► write-tree ─► commit-tree
                           refs/nex/<sess>/<turn> = C_t
        │
        ▼ (tool runs; result.details.patch)
tool_result ──► hash-object(post file) ─► per-tool checkpoint record
             ──► projection: tool row display.file_diff + turn fileChanges

user clicks Undo on turn T
  conversationFileChangesV4 (reads checkpoints) ─► panel
  fileRewindPreviewV4 ─► safe/unsafe/ignored (hash compare vs post-state)
  applyFileRewind ─► journaled restore C_T... → turnHeaders state="reverted"
                    (also pi entry "nex.rewind")

editUserQuery(workspaceMode=rewind)
  preview ─► blocked? ─(yes)─► ACK disposition:"blocked"+preview ─► inline conflict dialog
                │(no)
                ▼
  applyFileRewind(commitAfterApply = branchCut to parent of target user entry)
                ▼
  prompt(newText, images=attachments)
```

- **Cost/performance:** first snapshot of a large repo (50 k files) ≈ 1-3 s cold, native `git add -A` ≈ 100-300 ms warm; each later turn's first write typically hashes only dirty files. Prefer `git diff-files --quiet` before `add` to skip clean tree. On huge repos (>100 k files) fall back to per-tool `hash-object` snapshots only (no full-tree) and mark `checkpoint_partial` for files not captured.
- **Restart/restore:** checkpoints are on disk with refs, so projections after restart read `turnHeader.fileChanges` from the shadow repo via recorded turn→commit map persisted in an `nex.checkpoints` custom entry in the pi session file (or `meta.json` in the shadow repo). If the shadow repo is lost → `checkpoint_unavailable`, UI hides Undo.

**Effort:** ~3-4 days: checkpoint service + extension hook 1 d; projection (tool display, turn header fileChanges, canRewindFiles) 0.75 d; fileChanges/preview/apply commands 1-1.5 d; Windows + tests 0.75 d.

---

## Risks / notes

1. The Go bridge is **not** a bottleneck for 384 KiB chunks; but never use `Buffer` args for attachment bytes (`$buf` double-inflation) and never return >32 MiB call results (runner read limit).
2. `canRewindFiles` is intentionally absent in `rowActions.ts`; the UI's Undo button and the inline rewind both depend on it — implement alongside checkpoints, not before.
3. `editUserQuery` attachments semantics (`omitted` = keep, `[]` = delete all, `ConversationRowView.tsx:994-999`, `fork-edit-retry.ts:134-140`) must be preserved in the runner.
4. Web cannot attach arbitrary binaries to runner workspaces (no local path, non-text dropped); this is a pre-existing UI gap, not a runner design fault — the runner should answer `attachmentUnsupported` clearly rather than fabricate refs.
