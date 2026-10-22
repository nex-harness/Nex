# Attachments and file rewind (batch 3) — spec

Status: planned on `feature/next`. Research with citations:
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

## Acceptance

- Unit + real pi/faux integration for upload limits, ref safety, image prompt, restore,
  checkpoint/changed-files/preview/apply, external modification → unsafe, bash → ignored.
- Browser E2E (`next/e2e/attachments.mjs`, `--e2e=attachments`, in `all`): paste/attach
  an image → it renders in the sent message and the model describes it; attach a text
  file → the model reads it; an edit/write card shows a diff; turn "Undo" restores the
  file; edit-with-rewind restores files and re-runs.
