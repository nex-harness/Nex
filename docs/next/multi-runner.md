# Multi-runner workspaces — spec

Status: in progress on `feature/next`. Supersedes the "route by `workspacePath` only"
rule in `README.md` once landed. Research with citations: `multi-runner-research.md`.

## Problem

One server, many runners (macOS/Linux/Windows). The same absolute path can exist on two
machines, and the user must see and pick which machine a workspace is on. The UI's
existing `remote:` mechanism cannot be reused on web: remote sessions are registered only
by the desktop renderer's MessagePort bridge, and web stubs `connectRemote`.

## Decision

Server-routed workspace identity carried over the single `/ws` connection:

```
workspaceIdentity = "runner:<runnerId>:<absolute runner-local path>"
workspacePath     = runner-local absolute path (cwd, file ops, display)
```

- The server builds identities from runner `hello`; runners never parse them for IO.
- One shared helper per language builds/parses it (Go `internal/runners`, TS
  `packages/shared/src/server-workspace-identity.ts`). No hand-splitting elsewhere.
- Key rule stays `identity?.trim() || path` everywhere in the UI.

## Ownership

| State                                          | Owner                                                          |
| ---------------------------------------------- | -------------------------------------------------------------- |
| runnerId, name, os, workspaces                 | runner (`hello`)                                               |
| identity ↔ runner routing table                | server registry (single writer)                                |
| tabs with `{workspacePath, workspaceIdentity}` | browser tab store, persisted in `setting.lastWorkspaceSession` |
| task rows / sessions per workspace             | runner, echoing `workspaceIdentity` back                       |

## Routing (server)

For forwarded channels (`nex-agent`, `nex-task`, `nex-session`) and their dynamic events:

1. Arg has a valid `runner:` identity → that runner. Unknown/offline runner →
   `runner offline` error (never fall back to another runner).
2. Else arg has `workspacePath` served by exactly one runner → that runner (legacy).
3. Else path served by several runners → error `ambiguous workspace path; identity
required`.

`model-selection.getView` / `provider-settings.getView` / their `onDidChange`: route by
the optional workspace target in the arg when present (UI change below), else first runner.

Duplicate path across runners is legal. Duplicate identity (same runnerId) follows the
existing newest-connection-wins rule.

`/api/server-info.workspaces[]` = `{path, label:"<runnerName>: <basename>",
workspaceIdentity, runnerId, runnerName, runnerOs}`; extra fields are ignored by old
clients. `setting.recentProjects` stays path-only and is not a picker.

## Event order: opening a workspace on machine B

```
User          UI (picker)           Go server               runner B
 │ open picker  │ GET /api/server-info │                        │
 │─────────────▶│─────────────────────▶│ registry.Workspaces()  │
 │              │◀── [{path, identity:"runner:B:/src/app", runnerName:"win-box"}…]
 │ pick win-box │ addTab(path,{workspaceIdentity}) + startDraft(path, identity)
 │              │ nex-agent/nex-task calls {workspacePath, workspaceIdentity}
 │              │─────────────────────▶│ route by identity ────▶│ handle (cwd = path)
 │              │◀──────────── result / frames (rows echo workspaceIdentity) ◀│
```

Runner B offline: calls fail fast with `runner offline`; tab stays, sidebar shows the
runner's tasks again after it reconnects (server re-issues listens).

## UI changes (minimal)

1. "Identity means remote" becomes "identity is a `remote:` identity": add
   `isClientRemoteWorkspaceIdentity()` (wraps shared `isRemoteWorkspaceIdentity`) and use
   it at every truthiness site listed in `multi-runner-research.md` §2C
   (`useWorkspaceServices.tsx`, `workspaceServiceResolver.ts`,
   `workspaceRpcAvailability.ts`, `tabWorkspaceIdentity.ts`, `useLocalWorkspaceScopes.ts`,
   `remoteWorkspaceHistory.ts`, `useRemoteWorkspaceTabLifecycle.ts`, `ChatEmptyState.tsx`,
   `Root.tsx`). Desktop SSH/WSL/Docker behavior must not change.
2. Persistence: optional `workspaceIdentity` on local `lastWorkspaceSession` entries; save
   and restore it.
3. Picker: on web (`preferDirectoryBrowser`), "Open folder" shows a machine → workspace
   list fed by `/api/server-info` when the server reports runner workspaces, falling back
   to the directory browser otherwise. Reuse existing dialog/list components, follow
   `DESIGN.md`, i18n keys in all locales, works on phone width.
4. Workspace header shows the machine name for `runner:` identities.
5. `model-selection.getView` / `provider-settings.getView` input gains optional
   `{workspacePath, workspaceIdentity}` from the active draft so machine B shows B's models.

## Runner changes

Echo `workspaceIdentity` from the request: `nex-task` rows and workspace events,
`readWorkspacePresentation.workspace.workspaceKey` (and `workspaceIdentity`),
sessions-index snapshot `workspaceId` and topic (`identity?.trim() || path`). The sidebar
joins task rows by `buildTaskWorkspaceKey(path, identity)`.

The runner also enforces its own workspace boundary: every registered call/listen whose
argument carries a `workspacePath` must match one of the runner's configured workspaces
(normalized absolute paths), otherwise it fails with
`workspace not served by this runner: <path>`. This is one wrapper around registration
(`createWorkspaceGuardedRegistry` in `packages/next-runner/src/channels/register.ts`), not
a per-handler check, because a stale tab can carry this runner's id for a path it never
advertised in `hello`. Calls with no workspace in the argument (model catalog,
`listPinnedTaskIds`) are unaffected.

## Acceptance

- Go: registry/router tests for identity routing, ambiguity error, offline runner, duplicate
  paths, server-info fields.
- UI: unit tests for the predicate (remote vs runner vs empty identity) and persistence
  round-trip.
- Runner: unit tests for identity echo (`readWorkspacePresentation`, sessions-index
  `workspaceId`/topic, `nex-task` rows) validated with the real `@nex/shared` schemas, and
  for rejecting unserved workspace paths.
- Browser E2E `next/e2e/multi-runner.mjs`: two runners (`alpha`, `beta`) both serving the
  same path → picker lists both → create a session on `beta` → it appears only under
  `beta` → reload restores both tabs with the right machine → existing acceptance still
  passes. `next/dev.sh --e2e=multi` starts the second runner; `--e2e=all` runs acceptance,
  permissions and multi in that order. Both E2E runners advertise the demo workspace first
  so the auto-opened tab (and the acceptance "0 tasks before send") stays deterministic.
