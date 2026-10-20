# Permissions ("Ask before changes") — spec

Status: implemented on `feature/next`. Research with citations: `permissions-research.md`.
Scope: the runner gates pi tool calls; the unchanged web UI renders the approval card from
the v4 snapshot.

## UI contract (what the runner must emit and accept)

Snapshot (`packages/shared/src/nex-protocol-v4/snapshot.ts:230-350`, `rows.ts:191-218`):

- `pendingInteractions[]` entry: `{interactionId, kind:"permission", anchorRowId:<tool row id>,
createdAt, payload}` with `payload = {kind:"permission", toolCallId, toolName, summary,
detail:<tool input>, freeText:true, options}`.
- `options`: `allowOnce`, `allowAlways` (label "Always allow in this session"), `deny`, each
  `{optionId, label, kind, response:{decision:"allow"|"deny"}}`.
- The tool row: `status:"pendingApproval"`, `approvalInteractionId:<interactionId>`.
- Sessions index: `pendingInteractionSummary` for the sidebar attention dot and
  `pendingInteraction {kind:"permission", toolName}` for the tool-specific dot
  (`sessions-index.ts:23-48`).
- Do not set `config.permissionGrant` (the UI would flip the composer to yolo).

Command: `resolveInteraction {interactionId, answer:{optionId, freeText?}}`. ACK `accepted`
with `result:{type:"resolveInteraction", resolvedBy:{clientId, optionId}}`. Unknown or
already-resolved ids are idempotent success (`noop`). An unknown `optionId` on a live
interaction resolves as deny (fail closed), not as a rejected command.

Mode arrives per submission, never as a live command: `createSession.firstInput.mode` /
`createSession.config.mode` / `sendText.mode`, values `build` ("Ask before changes"),
`edit` ("Edit automatically"), `yolo` ("Full access"), plus `planEnabled:boolean`.
`snapshot.config.mode` and `planEnabled` echo the session values (persisted per session in
runner metadata; only used to seed the composer).

## Policy

| pi tool                       | build | edit  | yolo  | planEnabled (any mode) |
| ----------------------------- | ----- | ----- | ----- | ---------------------- |
| `read`, `grep`, `find`, `ls`  | allow | allow | allow | allow                  |
| `edit`, `write`               | ask   | allow | allow | deny                   |
| `bash` read-only (classifier) | allow | allow | allow | allow                  |
| `bash` other                  | ask   | ask   | allow | deny                   |
| unknown / extension tools     | ask   | ask   | allow | deny                   |

Nested calls (`parentToolCallId`) use the same table. "Allow always" = this tool name for
this session, in memory only. Plan mode denies with a reason and never shows a card.

Bash read-only classifier (conservative, `src/engine/permissions/bashClassifier.ts`):

- Not read-only if the command contains a newline or any shell metacharacter: pipe,
  ampersand, semicolon, redirection (`<`, `>`), dollar sign, backtick, or parentheses.
- Otherwise the first token must be one of `ls cat head tail pwd rg grep find wc stat file
echo sort uniq diff which`, or `git` with subcommand `status`, `diff`, `log`, `show`,
  `rev-parse` or `branch`.
- `find` with `-exec`/`-delete` and `sed -i` are never read-only. Anything else → ask.

## Ownership and ordering

- `PermissionGate` per open session, owned by the pi engine; the inline pi extension
  (`DefaultResourceLoader({extensionFactories})`, loader reloaded before
  `createAgentSession({resourceLoader})`) closes over it. Single writer of pending state.
- Projection mirrors it from two engine events: `approval_request` and `approval_settled`.

```
UI            runner commands      PermissionGate (engine)      pi
 │ sendText{mode}  │                     │                       │
 │────────────────▶│ submit(mode) ──────▶│ set mode ────────────▶│ prompt
 │                 │                     │◀── tool_call hook ────│ (after tool_execution_start)
 │                 │                     │ decide: ask            │
 │◀─ snapshot: row pendingApproval + pendingInteractions[] ─────── │ (hook awaiting)
 │ resolveInteraction{optionId}          │                       │
 │────────────────▶│ gate.resolve ──────▶│ allow → undefined ───▶│ execute
 │◀─ ACK accepted  │                     │ deny → {block,reason}▶│ error result
 │◀─ snapshot: row running→success | cancelled, pendingInteractions [] │
```

## Failure behavior (fail closed = never execute without approval)

- No client connected: keep waiting (no timeout). The turn stays paused, the sidebar shows
  the attention dot, the user approves when they come back. Nothing runs unapproved.
- Stop/abort or `deleteSession`: the wait races `ctx.signal`; resolves as deny with reason
  `Approval cancelled: the run was stopped…`, row `cancelled`, interaction removed.
- Deny: `{block:true, reason}` with reason `User denied <tool>: <freeText>` (freeText part
  omitted when empty). Row `cancelled`; a later error result never downgrades `cancelled`.
- Runner restart: pending approvals are not persisted; the turn is lost. On restore a tool
  call with no result restores as `cancelled`, never `success`.

## Acceptance

- Unit: policy table, classifier cases, fold transitions, resolve idempotency, abort while
  pending, restore of an unanswered call, all emitted objects validated with the real zod
  schemas. Real pi engine + faux provider: allow, deny, edit mode, abort.
- E2E runs keep `runner-id.json` and use a dedicated server data dir: a new runner id would
  make restored `runner:<oldId>:…` tabs route as `runner offline` (correctly).
- Browser E2E (`next/e2e/permissions.mjs`, `next/dev.sh --e2e=permissions`): build mode,
  prompt asks to create a file → approval card appears → Allow once → file exists and card
  shows success; second prompt → Deny → file not created, card cancelled; mode "Edit
  automatically" → no card, file written; `ls` runs without a card.
