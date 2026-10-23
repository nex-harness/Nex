# Attachments and file rewind (batch 3) — spec

Status: implemented on `feature/next`. Research with citations:
`attachments-rewind-research.md` (A = attachments, B = rewind).

## A. Attachments

The UI's existing v4 attachment transaction is used unchanged:
`attachmentBeginV4` → `attachmentChunkV4` (384 KiB raw, base64 in JSON) →
`attachmentCommitV4` → `ref`, and `attachmentReadV4` / `attachmentPreviewSourceV4`
(always `chunked`) for previews. Bytes travel as base64 strings, never as RPC Buffers,
so the Go bridge does not double-encode them and no frame approaches the 32 MiB runner
link limit.

Runner (single writer of attachment blobs):

- Storage `<runnerData>/attachments/<sessionId>/<uploadId>/{meta.json, blob}`; ref
  `nex-runner://<sessionId>/<uploadId>/<sanitized name>`. File names never become path
  components; ref resolution is checked to stay inside the session directory, and a ref
  is only readable when a row or queue item of that session lists it.
- Limits are the protocol's: 20 MiB per file, 512 KiB per chunk, 64 chunks, 64 MiB staged
  per connection, 16 concurrent uploads, 5 min staging TTL. Media mime types are checked
  against magic bytes on commit; SVG is treated as a plain file.
- Feeding pi: images → `prompt(text, {images})`. Everything else (pdf, text, binary) →
  an attachment block appended to the prompt text naming the file and its on-disk path
  inside the runner's data dir, so the model reads it with the `read` tool. Contents are
  never inlined at admission.
- Rows: `userInput.attachments` set from the admitted input; persisted as a `nex.attachments`
  custom entry in the pi session so restore re-renders them.
- `sendText`, `createSession.firstInput`, queue items and `editUserQuery` keep attachment
  semantics (`editUserQuery`: omitted = keep, `[]` = remove all).
- Cleanup: abort, staging TTL, and a sweep of unreferenced blobs older than 24 h.

Known UI limitation (not fixed here): on web, binary files without a local path are
dropped by the composer before upload; text files are sent as text, images upload.

## B. File changes and rewind

Checkpoints in a shadow git repository per workspace, independent of the workspace's own
git:

```
git --git-dir=<runnerData>/checkpoints/<sha256(realpath ws)[0:16]>.git --work-tree=<ws>
```

- Before the first mutating tool call of a turn (`edit`, `write`, other tools marked
  mutating): `add -A` (respects `.gitignore`), `write-tree`, `commit-tree`, ref
  `refs/nex/<sessionId>/<turn>`. After each mutating tool result: record the file's
  post-state blob. `bash`/`powershell` are not checkpointed; files they change show as
  "ignored (bash)" in rewind previews.
- Windows: spawn `git` with an argument array; shadow repo sets `core.autocrlf=false`,
  `core.fileMode=false`, `core.longpaths=true`. Very large workspaces (> 100k files) fall
  back to per-file snapshots only.
- Tool cards: edit/write rows get `output.display = {kind:"file_diff", filePath,
additions, deletions, structuredPatch}` (edit from pi's `details.patch`; write from the
  checkpoint pre-state).
- Turn headers carry `fileChanges {files, additions, deletions, state}`;
  `actions.canRewindFiles` follows the old CLI rule (no active work, no pending
  interaction, turn finished, state active).
- `conversationFileChangesV4`, file rewind preview, `applyFileRewind`, and
  `editUserQuery{workspaceMode:"rewind"}` implement the old CLI semantics: a file whose
  current hash differs from its last recorded post-state is `unsafe (external_modified)`;
  `canApply` = safe files > 0 and no unsafe files; apply restores the turn-start contents
  (deleting files the turn created) atomically with a reverse journal, then appends a
  `nex.rewind` entry so the turn shows `reverted`.

Ordering (edit with rewind):

```
editUserQuery{rewind} ─▶ preview ─ blocked? ─yes─▶ ACK disposition:"blocked" + preview
                                    │no
                                    ▼
                        restore files (journaled) ─▶ nex.rewind entry
                        ─▶ branch cut (batch 2d primitive) ─▶ prompt(newText, attachments)
```

## Implementation notes (B)

- **Module layout.** `engine/checkpoints/` owns the shadow repo: `checkpointGit.ts` (spawn
  boundary, `core.autocrlf=false`/`core.fileMode=false`/`core.longpaths=true`, forward-slash
  paths, no shell), `checkpointGitOps.ts` (side-index `add -A` → `write-tree` →
  `commit-tree`, blob hashing, whole-file patch), `checkpointService.ts` (per-workspace live
  turn), `rewindStore.ts` (changed files / preview / journaled apply) and
  `checkpointFiles.ts` (temp-file + rename + fsync, reverse journal).
- **Turn ownership** follows the product turn: `message_end(user)` opens `turn-<n>`,
  `message_entry(user)` supplies the rewind anchor (the persisted pi entry id), the first
  mutating `tool_call` snapshots, every mutating `tool_result` records the post-state, and
  `agent_settled` persists `nex.checkpoints` (retried once if the anchor is still pending;
  also flushed on session teardown).
- **Persisted records.** `nex.checkpoints {version,userEntryId,turnId,sequence,files,
operations[{path,toolCallId,toolName,postBlob,additions,deletions,patch}],partial?}` and
  `nex.rewind {version,userEntryId,turnId,revertedAt,sourceCommandId,files}`; restore
  projects them onto tool rows (`output.display`) and turn headers (`fileChanges.state`).
- **Per-path diff** = the LAST operation's patch (so a write→edit pair reports the edit, not
  the write). `turnHeader.fileChanges` aggregates the turn's tool displays live and from the
  restored records.
- **Ignored files.** Shell tools (`bash`/`powershell`/`shell`/`terminal`) are never
  checkpointed; a simple `touch/rm/mv/… <path>` command records a path hint. Such paths are
  reported as `ignoredFiles: bash_ignored` and do not by themselves block a rewind (the
  safe-set rule decides), matching the old CLI's `canApply = safe > 0 && unsafe == 0`.
- **Ordering deviation (edit with rewind).** Files are restored first, journaled; only when
  every write is down does the branch cut run (`editUserQuery{workspaceMode:"rewind"}` →
  apply files → cut → prompt). If the cut fails, the error is reported and the files stay
  restored: the conversation can never be rewound without its files. The spec's literal
  "cut inside apply's commit step" would compensate the files after a cut that cannot be
  undone, so this direction is deliberate.
- **E2E observation point (files suite).** The restore finishes inside the runner while the
  re-run reply streams to the browser over the same link; the reply can render before the
  debounced snapshot that carries the `nex.rewind` state. `file-changes.mjs` therefore polls
  the disk state (file gone / first line absent) after the rewind click instead of sampling
  once. A runner-side trace of `editUserQuery{rewind}` shows `applied: true` with the file
  already absent when the command returns, so the deleted-file result is the product
  behaviour, not a timing window to work around.
- **Preview gate.** `safeFiles > 0 && unsafeFiles == 0` (both `applyFileRewind` and
  `editUserQuery{rewind}`); the internal `startBlob` never leaves the runner (the wire
  preview stays `{action,operationCount,path,toolNames}`).
- **Git missing** → the first checkpoint attempt probes `git --version` before creating any
  directory, logs `file checkpoints disabled: git is unavailable` once, and every turn has no
  `fileChanges` (Undo hidden). Non-git workspaces work identically (the shadow repo lives in
  `<runnerData>/checkpoints/<sha256(realpath ws)[0:16]>.git`).
- **Fail-closed details.** The empty tree (`4b825dc…`) is a valid snapshot id: the service
  checks for `undefined`, never truthiness. A turn whose snapshot failed is `partial` →
  `checkpoint_missing` on rewind.

## Acceptance

- Unit + real pi/faux integration for upload limits, ref safety, image prompt, restore,
  checkpoint/changed-files/preview/apply, external modification → unsafe, bash → ignored.
- Browser E2E (`next/e2e/attachments.mjs`, `--e2e=attachments`, in `all`): paste/attach
  an image → it renders in the sent message and the model describes it; attach a text
  file → the model reads it; an edit/write card shows a diff; turn "Undo" restores the
  file; edit-with-rewind restores files and re-runs.
